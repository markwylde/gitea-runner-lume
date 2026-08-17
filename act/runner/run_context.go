// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	maps0 "maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/container"
	"gitea.com/gitea/runner/act/exprparser"
	"gitea.com/gitea/runner/act/model"
	"gitea.com/gitea/runner/internal/pkg/lock"

	"github.com/docker/cli/cli/compose/loader"
	"github.com/docker/go-connections/nat"
	"github.com/moby/moby/api/types/mount"
	"github.com/opencontainers/selinux/go-selinux"
	"golang.org/x/sync/errgroup"
)

// RunContext contains info about current job
type RunContext struct {
	Name        string
	Config      *Config
	Matrix      map[string]any
	Run         *model.Run
	EventJSON   string
	Env         map[string]string
	GlobalEnv   map[string]string // to pass env changes of GITHUB_ENV and set-env correctly, due to dirty Env field
	ExtraPath   []string
	CurrentStep string
	// CurrentStepIndex is the index of the top-level job step currently executing
	// (model.Step.Number). Composite sub-steps inherit the outer step's index by
	// walking the Parent chain; see topLevelRunContext.
	CurrentStepIndex    int
	StepResults         map[string]*model.StepResult
	IntraActionState    map[string]map[string]string
	ExprEval            ExpressionEvaluator
	JobContainer        container.ExecutionsEnvironment
	serviceContainers   []*serviceContainer
	OutputMappings      map[MappableOutput]MappableOutput
	JobName             string
	ActionPath          string
	Parent              *RunContext
	Masks               []string
	cleanUpJobContainer common.Executor
	caller              *caller // job calling this RunContext (reusable workflows)
	// summaryFileInitialized tracks which per-step summary files (workflow/step-summary-N.md)
	// have already been created on the JobContainer. The runner sets up file-command files
	// via JobContainer.Copy at the start of every phase, which truncates them — fine for
	// GITHUB_ENV/OUTPUT/STATE/PATH (consumed per phase) but wrong for GITHUB_STEP_SUMMARY,
	// which has accumulating semantics. We initialize each step's summary file exactly once
	// so writes from later phases and from composite sub-steps append to the same file.
	// Only populated on the top-level RunContext; child RCs walk Parent via topLevelRunContext.
	summaryFileInitialized map[int]bool
	// outputTemplate is this combination's pristine snapshot of the job's output expressions,
	// captured before execution so each matrix combo interpolates from the originals rather
	// than from a sibling's already-resolved values written into the shared Job.Outputs.
	outputTemplate map[string]string
	// jobCancelled records that this job's run was cancelled (context.Canceled). It makes
	// getJobContext report the "cancelled" status so cancelled()/always() evaluate the way
	// GitHub Actions does, letting cleanup and always() steps run while normal steps skip.
	jobCancelled bool
	// jobFailed records failures outside normal main-step results, such as action pre-step
	// failures. Those failures must still make success() false and failure() true for later
	// main-step if evaluation.
	jobFailed bool
	// empty for a host-mode job, which starts no container
	jobContainerID string
	jobNetworkName string
	// stepEnv is a copy of the running step's environment, so that workflow commands parsed out
	// of the container's output can be judged against it. Written by runStepExecutor and read on
	// the log-writer goroutine, hence unsecureCommandMu, which also guards unsecureCommandErr.
	stepEnv            map[string]string
	unsecureCommandErr error // refused ::set-env::/::add-path::, turned into a step failure
	unsecureCommandMu  sync.Mutex
}

// serviceContainer pairs a service container with the workflow id that keys job.services.
type serviceContainer struct {
	name       string
	image      string
	container  container.ExecutionsEnvironment
	logsDumped bool
	info       *container.Info // last poll, the source of the `job.services` entry
}

// setCurrentStepEnv records the environment of the step about to run.
func (rc *RunContext) setCurrentStepEnv(env map[string]string) {
	rc.unsecureCommandMu.Lock()
	defer rc.unsecureCommandMu.Unlock()
	rc.stepEnv = env
}

func (rc *RunContext) currentStepEnv() map[string]string {
	rc.unsecureCommandMu.Lock()
	defer rc.unsecureCommandMu.Unlock()
	return rc.stepEnv
}

// markCancelled flags the job as cancelled so subsequent step `if` evaluations and the
// job status context observe the "cancelled" state.
func (rc *RunContext) markCancelled() {
	rc.jobCancelled = true
}

// markFailed flags the job as failed so subsequent step `if` evaluations observe
// failure even when the error happened outside a main step result.
func (rc *RunContext) markFailed() {
	rc.jobFailed = true
}

// markInterrupted records the job's interruption status from a context error so later step `if` evaluations and the job result observe it,
// keeping the timeout path symmetric with the cancel path:
//   - context.Canceled (server cancel) marks the job cancelled, matching GitHub's "only always()/cancelled() run on cancel".
//   - context.DeadlineExceeded (job timeout-minutes) marks the job failed, matching the "Timeout -> FAILURE" reporting semantics.
func (rc *RunContext) markInterrupted(err error) {
	switch {
	case errors.Is(err, context.Canceled):
		rc.markCancelled()
	case errors.Is(err, context.DeadlineExceeded):
		rc.markFailed()
	}
}

func (rc *RunContext) AddMask(mask string) {
	rc.Masks = append(rc.Masks, mask)
}

type MappableOutput struct {
	StepID     string
	OutputName string
}

func (rc *RunContext) String() string {
	name := fmt.Sprintf("%s/%s", rc.Run.Workflow.Name, rc.Name)
	if rc.caller != nil {
		// prefix the reusable workflow with the caller job
		// this is required to create unique container names
		name = fmt.Sprintf("%s/%s", rc.caller.runContext.Name, name)
	}
	return name
}

// GetEnv returns the env for the context
func (rc *RunContext) GetEnv() map[string]string {
	if rc.Env == nil {
		rc.Env = map[string]string{}
		if rc.Run != nil && rc.Run.Workflow != nil && rc.Config != nil {
			job := rc.Run.Job()
			if job != nil {
				rc.Env = mergeMaps(rc.Run.Workflow.Env, job.Environment(), rc.Config.Env)
			}
		}
	}
	if !rc.Config.DisableActEnv {
		rc.Env["ACT"] = "true"
	}

	if !rc.Config.NoSkipCheckout {
		rc.Env["ACT_SKIP_CHECKOUT"] = "true"
	}

	return rc.Env
}

func (rc *RunContext) jobContainerName() string {
	nameParts := []string{rc.Config.ContainerNamePrefix, "WORKFLOW-" + rc.Run.Workflow.Name, "JOB-" + rc.Name}
	if rc.caller != nil {
		nameParts = append(nameParts, "CALLED-BY-"+rc.caller.runContext.JobName)
	}
	return createContainerName(nameParts...) // For Gitea
}

// networkNameForGitea return the name of the network
func (rc *RunContext) networkNameForGitea() (string, bool) {
	if rc.Config.ContainerNetworkMode != "" {
		return string(rc.Config.ContainerNetworkMode), false
	}
	return fmt.Sprintf("%s-%s-network", rc.jobContainerName(), rc.Run.JobID), true
}

func getDockerDaemonSocketMountPath(daemonPath string) string {
	if before, after, ok := strings.Cut(daemonPath, "://"); ok {
		scheme := before
		if strings.EqualFold(scheme, "npipe") {
			// linux container mount on windows, use the default socket path of the VM / wsl2
			return "/var/run/docker.sock"
		} else if strings.EqualFold(scheme, "unix") {
			return after
		} else if strings.IndexFunc(scheme, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z')
		}) == -1 {
			// unknown protocol use default
			return "/var/run/docker.sock"
		}
	}
	return daemonPath
}

// containerDaemonSocket returns the configured Docker daemon socket, applying the default
// without mutating the shared Config. Parallel jobs in a plan share one *Config, so a job
// must never write to it.
func (rc *RunContext) containerDaemonSocket() string {
	if rc.Config.ContainerDaemonSocket == "" {
		return "/var/run/docker.sock"
	}
	return rc.Config.ContainerDaemonSocket
}

// validVolumes returns the volumes allowed on this job's containers: the configured base
// plus the volumes the runner mounts automatically. It derives a fresh slice every call and
// never mutates the shared Config (see containerDaemonSocket).
func (rc *RunContext) validVolumes() []string {
	name := rc.jobContainerName()
	volumes := slices.Clone(rc.Config.ValidVolumes)
	// TODO: add a new configuration to control whether the docker daemon can be mounted
	return append(volumes, "act-toolcache", name, name+"-env",
		getDockerDaemonSocketMountPath(rc.containerDaemonSocket()))
}

// toolCache returns the tool cache path the job sees, relocatable through RUNNER_TOOL_CACHE.
func (rc *RunContext) toolCache(fallback string) string {
	if path := rc.GetEnv()["RUNNER_TOOL_CACHE"]; path != "" {
		return path
	}
	return fallback
}

// runnerEnv returns a container's RUNNER_* variables, derived from the values runner.tool_cache
// and friends report so the two cannot drift apart.
func (rc *RunContext) runnerEnv(ctx context.Context) []string {
	ext := container.LinuxContainerEnvironmentExtensions{}
	runnerContext := ext.GetRunnerContext(ctx)
	runnerContext["tool_cache"] = rc.toolCache(container.DefaultToolCache)

	env := make([]string, 0, len(runnerContext))
	for key, value := range runnerContext {
		env = append(env, fmt.Sprintf("RUNNER_%s=%s", strings.ToUpper(key), value))
	}
	slices.Sort(env)
	return env
}

// splitVolumes routes volume specs into binds and a source:target mount map, and returns the
// container paths they mount onto. Only a plain source:target volume fits the map, everything
// else (anonymous volumes, host binds, mount options) stays a bind.
func splitVolumes(specs []string) ([]string, map[string]string, map[string]bool) {
	binds := []string{}
	mounts := map[string]string{}
	targets := map[string]bool{}

	for _, spec := range specs {
		parsed, err := loader.ParseVolume(spec)
		if err != nil {
			binds = append(binds, spec) // let Docker report the malformed spec
			continue
		}
		targets[parsed.Target] = true
		if parsed.Type == string(mount.TypeVolume) && parsed.Source != "" && !parsed.ReadOnly {
			mounts[parsed.Source] = parsed.Target
		} else {
			binds = append(binds, spec)
		}
	}
	return binds, mounts, targets
}

// Returns the binds and mounts for the container, resolving paths as appopriate
func (rc *RunContext) GetBindsAndMounts() ([]string, map[string]string) {
	name := rc.jobContainerName()
	ext := container.LinuxContainerEnvironmentExtensions{}

	var volumes []string
	if job := rc.Run.Job(); job != nil {
		if container := job.Container(); container != nil {
			for _, v := range container.Volumes {
				if rc.ExprEval != nil {
					v = rc.ExprEval.Interpolate(context.Background(), v)
				}
				volumes = append(volumes, v)
			}
		}
	}
	// the runner's own mounts below yield to the targets the job claims
	binds, mounts, claimed := splitVolumes(volumes)

	if daemonSocket := rc.containerDaemonSocket(); daemonSocket != "-" && !claimed["/var/run/docker.sock"] {
		binds = append(binds, getDockerDaemonSocketMountPath(daemonSocket)+":/var/run/docker.sock")
	}
	if toolCache := rc.toolCache(container.DefaultToolCache); !claimed[toolCache] {
		mounts["act-toolcache"] = toolCache
	}
	mounts[name+"-env"] = ext.GetActPath() // runner-internal, never overridable

	if workdir := ext.ToContainerPath(rc.Config.Workdir); !claimed[workdir] {
		if rc.Config.BindWorkdir {
			bindModifiers := ""
			if runtime.GOOS == "darwin" {
				bindModifiers = ":delegated"
			}
			if selinux.GetEnabled() {
				bindModifiers = ":z"
			}
			binds = append(binds, fmt.Sprintf("%s:%s%s", rc.Config.Workdir, workdir, bindModifiers))
		} else {
			mounts[name] = workdir
		}
	}

	return binds, mounts
}

func (rc *RunContext) startHostEnvironment() common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		rawLogger := logger.WithField(rawOutputField, true)
		logWriter := common.NewLineWriter(rc.commandHandler(ctx), func(s string) bool {
			if rc.Config.LogOutput {
				rawLogger.Infof("%s", s)
			} else {
				rawLogger.Debugf("%s", s)
			}
			return true
		})
		cacheDir := rc.ActionCacheDir()
		randBytes := make([]byte, 8)
		_, _ = rand.Read(randBytes)
		miscpath := filepath.Join(cacheDir, hex.EncodeToString(randBytes))
		actPath := filepath.Join(miscpath, "act")
		if err := os.MkdirAll(actPath, 0o777); err != nil {
			return err
		}
		path := filepath.Join(miscpath, "hostexecutor")
		if err := os.MkdirAll(path, 0o777); err != nil {
			return err
		}
		runnerTmp := filepath.Join(miscpath, "tmp")
		if err := os.MkdirAll(runnerTmp, 0o777); err != nil {
			return err
		}
		toolCache := rc.toolCache(filepath.Join(cacheDir, "tool_cache"))
		if err := os.MkdirAll(toolCache, 0o777); err != nil {
			return err
		}
		if rc.Config.ExecutionEnvironmentFactory != nil {
			environment, err := rc.Config.ExecutionEnvironmentFactory(ctx, ExecutionEnvironmentInput{
				Workdir: rc.Config.Workdir, ActPath: actPath, TmpDir: runnerTmp,
				ToolCache: toolCache, Stdout: logWriter,
			})
			if err != nil {
				return fmt.Errorf("create execution environment: %w", err)
			}
			rc.JobContainer = environment
		} else {
			rc.JobContainer = &container.HostEnvironment{
				Path:         path,
				TmpDir:       runnerTmp,
				ToolCache:    toolCache,
				Workdir:      rc.Config.Workdir,
				CleanWorkdir: rc.Config.CleanWorkdir,
				ActPath:      actPath,
				CleanUp: func() {
					os.RemoveAll(miscpath)
				},
				StdOut:      logWriter,
				AllocatePTY: rc.Config.AllocatePTY,
			}
		}
		rc.cleanUpJobContainer = rc.JobContainer.Remove()
		for k, v := range rc.getRunnerContext(ctx) {
			if v, ok := v.(string); ok {
				rc.Env["RUNNER_"+strings.ToUpper(k)] = v
			}
		}
		// Host-process environment belongs only to true host execution. Lume
		// guests must not inherit controller HOME, USER, TMPDIR, or SSH agent
		// sockets; npm and similar tools would then write to the physical Mac
		// user's paths inside the VM.
		if rc.Config.ExecutionEnvironmentFactory == nil {
			for _, env := range os.Environ() {
				if k, v, ok := strings.Cut(env, "="); ok {
					// don't override
					if _, ok := rc.Env[k]; !ok {
						rc.Env[k] = v
					}
				}
			}
		}

		return common.NewPipelineExecutor(
			rc.JobContainer.Copy(rc.JobContainer.GetActPath()+"/", &container.FileEntry{
				Name: "workflow/event.json",
				Mode: 0o644,
				Body: rc.EventJSON,
			}, &container.FileEntry{
				Name: "workflow/envs.txt",
				Mode: 0o666,
				Body: "",
			}),
		)(ctx)
	}
}

// printStartJobContainerGroup mirrors actions/runner's "Starting job container"
// section: emit the group header and summary, return a closer for ::endgroup::.
func printStartJobContainerGroup(ctx context.Context, image, name, network string) func() {
	rawLogger := common.Logger(ctx).WithField(rawOutputField, true)
	rawLogger.Infof("::group::Starting job container")
	rawLogger.Infof("image: %s", image)
	rawLogger.Infof("name: %s", name)
	rawLogger.Infof("network: %s", network)
	return func() {
		rawLogger.Infof("::endgroup::")
	}
}

// newContainer is a variable so tests can substitute a container that needs no Docker daemon.
var newContainer = container.NewContainer

func (rc *RunContext) startJobContainer() common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		image := rc.platformImage(ctx)
		rawLogger := logger.WithField(rawOutputField, true)
		logWriter := common.NewLineWriter(rc.commandHandler(ctx), func(s string) bool {
			if rc.Config.LogOutput {
				rawLogger.Infof("%s", s)
			} else {
				rawLogger.Debugf("%s", s)
			}
			return true
		})

		username, password, err := rc.handleCredentials(ctx)
		if err != nil {
			return fmt.Errorf("failed to handle credentials: %s", err)
		}

		name := rc.jobContainerName()
		// For gitea, to support --volumes-from <container_name_or_id> in options.
		// We need to set the container name to the environment variable.
		rc.Env["JOB_CONTAINER_NAME"] = name

		envList := make([]string, 0)

		envList = append(envList, rc.runnerEnv(ctx)...)
		envList = append(envList, fmt.Sprintf("%s=%s", "LANG", "C.UTF-8")) // Use same locale as GitHub Actions

		ext := container.LinuxContainerEnvironmentExtensions{}
		binds, mounts := rc.GetBindsAndMounts()

		// specify the network to which the container will connect when `docker create` stage. (like execute command line: docker create --network <networkName> <image>)
		// if using service containers, will create a new network for the containers.
		// and it will be removed after at last.
		networkName, createAndDeleteNetwork := rc.networkNameForGitea()

		// add service containers
		for serviceID, spec := range rc.Run.Job().Services {
			// GitHub compatibility: skip services whose image evaluates to an
			// empty string, enabling conditional services via expressions
			serviceImage := rc.ExprEval.Interpolate(ctx, spec.Image)
			if serviceImage == "" {
				logger.Infof("The service '%s' will not be started because the container definition has an empty image.", serviceID)
				continue
			}
			// interpolate env
			interpolatedEnvs := make(map[string]string, len(spec.Env)+len(rc.Config.ProxyEnv))
			// a service reaches the internet the way the job does; its own env still wins
			maps0.Copy(interpolatedEnvs, rc.Config.ProxyEnv)
			for k, v := range spec.Env {
				interpolatedEnvs[k] = rc.ExprEval.Interpolate(ctx, v)
			}
			envs := make([]string, 0, len(interpolatedEnvs))
			for k, v := range interpolatedEnvs {
				envs = append(envs, fmt.Sprintf("%s=%s", k, v))
			}
			// interpolate cmd
			interpolatedCmd := make([]string, 0, len(spec.Cmd))
			for _, v := range spec.Cmd {
				interpolatedCmd = append(interpolatedCmd, rc.ExprEval.Interpolate(ctx, v))
			}
			// keep these local: reusing username/password would overwrite the
			// credentials the job container is pulled with further down
			serviceUsername, servicePassword, err := rc.handleServiceCredentials(ctx, spec.Credentials)
			if err != nil {
				return fmt.Errorf("failed to handle service %s credentials: %w", serviceID, err)
			}

			interpolatedVolumes := make([]string, 0, len(spec.Volumes))
			for _, volume := range spec.Volumes {
				interpolatedVolumes = append(interpolatedVolumes, rc.ExprEval.Interpolate(ctx, volume))
			}
			serviceBinds, serviceMounts := rc.GetServiceBindsAndMounts(interpolatedVolumes)

			interpolatedPorts := make([]string, 0, len(spec.Ports))
			for _, port := range spec.Ports {
				interpolatedPorts = append(interpolatedPorts, rc.ExprEval.Interpolate(ctx, port))
			}
			exposedPorts, portBindings, err := nat.ParsePortSpecs(interpolatedPorts)
			if err != nil {
				return fmt.Errorf("failed to parse service %s ports: %w", serviceID, err)
			}

			serviceContainerName := createContainerName(rc.jobContainerName(), serviceID)
			c := newContainer(&container.NewContainerInput{
				Name:           serviceContainerName,
				WorkingDir:     ext.ToContainerPath(rc.Config.Workdir),
				Image:          serviceImage,
				Username:       serviceUsername,
				Password:       servicePassword,
				Cmd:            interpolatedCmd,
				Env:            envs,
				Mounts:         serviceMounts,
				Binds:          serviceBinds,
				Stdout:         logWriter,
				Stderr:         logWriter,
				Privileged:     rc.Config.Privileged,
				UsernsMode:     rc.Config.UsernsMode,
				Platform:       rc.Config.ContainerArchitecture,
				AutoRemove:     false, // so a dead service's log survives, cleanupJobResources removes it
				Options:        rc.ExprEval.Interpolate(ctx, spec.Options),
				NetworkMode:    networkName,
				NetworkAliases: []string{serviceID},
				ExposedPorts:   exposedPorts,
				PortBindings:   portBindings,
				AllocatePTY:    rc.Config.AllocatePTY,
			})
			rc.serviceContainers = append(rc.serviceContainers, &serviceContainer{name: serviceID, image: serviceImage, container: c})
		}

		rc.cleanUpJobContainer = rc.cleanupJobResources(networkName, createAndDeleteNetwork)

		// For Gitea, `jobContainerNetwork` should be the same as `networkName`
		jobContainerNetwork := networkName

		rc.JobContainer = newContainer(&container.NewContainerInput{
			Cmd:            nil,
			Entrypoint:     []string{"/bin/sleep", fmt.Sprint(rc.Config.ContainerMaxLifetime.Round(time.Second).Seconds())},
			WorkingDir:     ext.ToContainerPath(rc.Config.Workdir),
			Image:          image,
			Username:       username,
			Password:       password,
			Name:           name,
			Env:            envList,
			Mounts:         mounts,
			NetworkMode:    jobContainerNetwork,
			NetworkAliases: []string{rc.Name},
			Binds:          binds,
			Stdout:         logWriter,
			Stderr:         logWriter,
			Privileged:     rc.Config.Privileged,
			UsernsMode:     rc.Config.UsernsMode,
			Platform:       rc.Config.ContainerArchitecture,
			Options:        rc.options(ctx),
			AutoRemove:     rc.Config.AutoRemove,
			ValidVolumes:   rc.validVolumes(),
			AllocatePTY:    rc.Config.AllocatePTY,
		})
		if rc.JobContainer == nil {
			return errors.New("Failed to create job container")
		}

		rc.jobNetworkName = networkName

		defer printStartJobContainerGroup(ctx, image, name, networkName)()
		return common.NewPipelineExecutor(
			rc.pullServicesImages(rc.Config.ForcePull),
			rc.JobContainer.Pull(rc.Config.ForcePull),
			rc.stopJobContainer(),
			container.NewDockerNetworkCreateExecutor(networkName, rc.Config.ContainerNetworkCreateOptions).
				IfBool(createAndDeleteNetwork),
			rc.startServiceContainers(),
			rc.reportUnstartedServices(),
			rc.waitForServiceContainers(),
			rc.JobContainer.Create(rc.Config.ContainerCapAdd, rc.Config.ContainerCapDrop),
			rc.JobContainer.Start(false),
			rc.captureJobContainerInfo(),
			rc.JobContainer.Copy(rc.JobContainer.GetActPath()+"/", &container.FileEntry{
				Name: "workflow/event.json",
				Mode: 0o644,
				Body: rc.EventJSON,
			}, &container.FileEntry{
				Name: "workflow/envs.txt",
				Mode: 0o666,
				Body: "",
			}),
		)(ctx)
	}
}

// cleanupJobResources removes everything the job created, continuing past failures.
// Only job container and volume errors are returned, the rest are logged.
func (rc *RunContext) cleanupJobResources(networkName string, createAndDeleteNetwork bool) common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		removeJobContainer := rc.JobContainer != nil && !rc.Config.ReuseContainers

		var errs []error
		if removeJobContainer {
			errs = append(errs, rc.JobContainer.Remove()(ctx))
		}
		if len(rc.serviceContainers) > 0 {
			logger.Infof("Cleaning up services for job %s", rc.JobName)
			if err := rc.stopServiceContainers()(ctx); err != nil {
				logger.Errorf("Error while cleaning services: %v", err)
			}
		}
		if removeJobContainer {
			// after the containers using them, services can hold these via `--volumes-from`
			name := rc.jobContainerName()
			errs = append(errs,
				container.NewDockerVolumeRemoveExecutor(name, false)(ctx),
				container.NewDockerVolumeRemoveExecutor(name+"-env", false)(ctx))
		}
		if createAndDeleteNetwork {
			// last, once every container has detached
			logger.Infof("Cleaning up network for job %s, and network name is: %s", rc.JobName, networkName)
			if err := container.NewDockerNetworkRemoveExecutor(networkName)(ctx); err != nil {
				logger.Errorf("Error while cleaning network: %v", err)
			}
		}
		return errors.Join(errs...)
	}
}

func (rc *RunContext) execJobContainer(cmd []string, env map[string]string, user, workdir string) common.Executor { //nolint:unparam // pre-existing issue from nektos/act
	return func(ctx context.Context) error {
		return rc.JobContainer.Exec(cmd, env, user, workdir)(ctx)
	}
}

func (rc *RunContext) ApplyExtraPath(ctx context.Context, env *map[string]string) {
	if len(rc.ExtraPath) > 0 {
		path := rc.JobContainer.GetPathVariableName()
		if rc.JobContainer.IsEnvironmentCaseInsensitive() {
			// On windows system Path and PATH could also be in the map
			for k := range *env {
				if strings.EqualFold(path, k) {
					path = k
					break
				}
			}
		}
		if (*env)[path] == "" {
			cenv := map[string]string{}
			var cpath string
			if err := rc.JobContainer.UpdateFromImageEnv(&cenv)(ctx); err == nil {
				if p, ok := cenv[path]; ok {
					cpath = p
				}
			}
			if len(cpath) == 0 {
				cpath = rc.JobContainer.DefaultPathVariable()
			}
			(*env)[path] = cpath
		}
		(*env)[path] = rc.JobContainer.JoinPathVariable(append(rc.ExtraPath, (*env)[path])...)
	}
}

func (rc *RunContext) UpdateExtraPath(ctx context.Context, githubEnvPath string) error {
	if common.Dryrun(ctx) {
		return nil
	}
	pathTar, err := rc.JobContainer.GetContainerArchive(ctx, githubEnvPath)
	if err != nil {
		return err
	}
	defer pathTar.Close()

	reader := tar.NewReader(pathTar)
	_, err = reader.Next()
	if err != nil && err != io.EOF {
		return err
	}
	s := bufio.NewScanner(reader)
	for s.Scan() {
		line := s.Text()
		if len(line) > 0 {
			rc.addPath(ctx, line)
		}
	}
	return nil
}

// stopJobContainer removes the job container (if it exists) and its volume (if it exists)
func (rc *RunContext) stopJobContainer() common.Executor {
	return func(ctx context.Context) error {
		if rc.cleanUpJobContainer != nil {
			return rc.cleanUpJobContainer(ctx)
		}
		return nil
	}
}

func (rc *RunContext) pullServicesImages(forcePull bool) common.Executor {
	return func(ctx context.Context) error {
		execs := []common.Executor{}
		for _, svc := range rc.serviceContainers {
			execs = append(execs, svc.container.Pull(forcePull))
		}
		return common.NewParallelExecutor(len(execs), execs...)(ctx)
	}
}

func (rc *RunContext) startServiceContainers() common.Executor {
	return func(ctx context.Context) error {
		execs := []common.Executor{}
		for _, svc := range rc.serviceContainers {
			execs = append(execs, common.NewPipelineExecutor(
				svc.container.Pull(false),
				svc.container.Create(rc.Config.ContainerCapAdd, rc.Config.ContainerCapDrop),
				svc.container.Start(false),
			))
		}
		return common.NewParallelExecutor(len(execs), execs...)(ctx)
	}
}

func (rc *RunContext) stopServiceContainers() common.Executor {
	return func(ctx context.Context) error {
		execs := []common.Executor{}
		for _, svc := range rc.serviceContainers {
			execs = append(execs, svc.container.Remove().Finally(svc.container.Close()))
		}
		return common.NewParallelExecutor(len(execs), execs...)(ctx)
	}
}

const (
	defaultServiceReadyTimeout = 5 * time.Minute
	serviceReadyPollMax        = 32 * time.Second
)

var serviceReadyPollInterval = 2 * time.Second // a variable so tests need not wait

// reportUnstartedServices logs a service that did not start. The steps that need it
// report it better than the runner can, so the job carries on.
func (rc *RunContext) reportUnstartedServices() common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		for _, svc := range rc.serviceContainers {
			info, err := svc.inspect(ctx)
			if err != nil {
				logger.Debugf("unable to inspect service '%s': %v", svc.name, err)
				continue
			}
			if info.State == container.StateRunning {
				continue
			}
			svc.dumpLogs(ctx)
			logger.Warnf("Docker container %s is not in running state: %s (%d)", info.ID, info.State, info.ExitCode)
		}
		return nil
	}
}

// waitForServiceContainers blocks until every service that declares a healthcheck reports
// healthy, as GitHub does, so a first step cannot connect before the service listens.
func (rc *RunContext) waitForServiceContainers() common.Executor {
	return func(ctx context.Context) error {
		if len(rc.serviceContainers) == 0 {
			return nil
		}

		timeout := rc.Config.ServiceReadyTimeout
		switch {
		case timeout < 0:
			// disabled, but still describe the containers for `job.services`
			for _, svc := range rc.serviceContainers {
				if _, err := svc.inspect(ctx); err != nil && !errors.Is(err, container.ErrContainerNotFound) {
					return err
				}
			}
			return nil
		case timeout == 0:
			timeout = defaultServiceReadyTimeout
		}

		// the first error cancels the rest, so a failure does not wait out a sibling's timeout
		group, groupCtx := errgroup.WithContext(ctx)
		for _, svc := range rc.serviceContainers {
			group.Go(func() error {
				return svc.waitUntilHealthy(groupCtx, timeout)
			})
		}
		return group.Wait()
	}
}

// waitUntilHealthy waits on the healthcheck alone, so a container that declares none is
// ready at once and one that exited is left to the steps that need it.
func (svc *serviceContainer) waitUntilHealthy(ctx context.Context, timeout time.Duration) error {
	rawLogger := common.Logger(ctx).WithField(rawOutputField, true)
	interval := serviceReadyPollInterval

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		info, err := svc.inspect(ctx)
		if ctxErr := ctx.Err(); ctxErr != nil { // the wait ended, an inspect error only noticed it
			if errors.Is(ctxErr, context.DeadlineExceeded) {
				return fmt.Errorf("the service '%s' did not become healthy within %s%s", svc.name, timeout, svc.healthOutputSuffix())
			}
			return ctxErr
		}
		switch {
		case errors.Is(err, container.ErrContainerNotFound):
			return nil // gone, so there is no health left to wait on
		case err != nil:
			return err
		}

		switch {
		case info.Health == container.HealthUnhealthy:
			svc.dumpLogs(ctx)
			common.Logger(ctx).Errorf("Failed to initialize container %s", svc.image)
			return fmt.Errorf("the service '%s' is unhealthy%s", svc.name, svc.healthOutputSuffix())
		case info.Health != container.HealthStarting:
			rawLogger.Infof("%s service is healthy.", svc.name)
			return nil
		}

		rawLogger.Infof("%s service is starting, waiting %d seconds before checking again.", svc.name, int(interval.Seconds()))
		select {
		case <-ctx.Done(): // reported at the top of the loop
		case <-time.After(interval):
		}
		interval = min(interval*2, serviceReadyPollMax)
	}
}

// dumpLogs writes the container's log to the job log once, however often it is reported.
func (svc *serviceContainer) dumpLogs(ctx context.Context) {
	if svc.logsDumped {
		return
	}
	svc.logsDumped = true
	if err := svc.container.DumpLogs(ctx); err != nil {
		common.Logger(ctx).Debugf("unable to read the log of service '%s': %v", svc.name, err)
	}
}

// inspect also records the state for the `job.services` context.
func (svc *serviceContainer) inspect(ctx context.Context) (*container.Info, error) {
	info, err := svc.container.Inspect(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect service '%s': %w", svc.name, err)
	}
	svc.info = info
	return info, nil
}

func (svc *serviceContainer) healthOutputSuffix() string {
	if svc.info == nil || svc.info.HealthOutput == "" {
		return ""
	}
	return ": " + svc.info.HealthOutput
}

// captureJobContainerInfo is a convenience: failing to describe the container must not
// fail the job.
func (rc *RunContext) captureJobContainerInfo() common.Executor {
	return func(ctx context.Context) error {
		info, err := rc.JobContainer.Inspect(ctx)
		if err != nil {
			common.Logger(ctx).Debugf("unable to inspect the job container: %v", err)
			return nil
		}
		rc.jobContainerID = info.ID
		return nil
	}
}

// Prepare the mounts and binds for the worker

// ActionCacheDir is for rc
func (rc *RunContext) ActionCacheDir() string {
	if rc.Config.ActionCacheDir != "" {
		return rc.Config.ActionCacheDir
	}
	var xdgCache string
	var ok bool
	if xdgCache, ok = os.LookupEnv("XDG_CACHE_HOME"); !ok || xdgCache == "" {
		if home, err := os.UserHomeDir(); err == nil {
			xdgCache = filepath.Join(home, ".cache")
		} else if xdgCache, err = filepath.Abs("."); err != nil {
			// It's almost impossible to get here, so the temp dir is a good fallback
			xdgCache = os.TempDir()
		}
	}
	return filepath.Join(xdgCache, "act")
}

// Interpolate outputs after a job is done
// jobMutexes serializes per-job result/output aggregation across the matrix combinations that
// share one *model.Job and run in parallel. Keyed by the shared *model.Job (mirrors the
// per-directory AcquireCloneLock pattern).
var jobMutexes lock.Keyed[*model.Job]

func lockJob(job *model.Job) func() {
	return jobMutexes.Lock(job)
}

func (rc *RunContext) interpolateOutputs() common.Executor {
	return func(ctx context.Context) error {
		ee := rc.NewExpressionEvaluator(ctx)
		job := rc.Run.Job()
		// Matrix combinations share this Job and its Outputs map. Interpolate from this combo's
		// pristine snapshot (outputTemplate) and write under the lock, so each combo overwrites
		// with its own resolved values (last wins, as on GitHub) instead of the first combo's
		// resolved values freezing the shared template against later combos.
		defer lockJob(job)()
		for k, v := range rc.outputTemplate {
			job.Outputs[k] = ee.Interpolate(ctx, v)
		}
		return nil
	}
}

func (rc *RunContext) startContainer() common.Executor {
	return func(ctx context.Context) error {
		var err error
		if rc.IsHostEnv(ctx) {
			err = rc.startHostEnvironment()(ctx)
		} else {
			err = rc.startJobContainer()(ctx)
		}
		if err != nil {
			// The job executor's teardown only runs after a successful start, so a failed
			// start would otherwise leak the per-job network and container.
			rc.cleanupFailedStart(ctx)
		}
		return err
	}
}

func (rc *RunContext) cleanupFailedStart(ctx context.Context) {
	if rc.cleanUpJobContainer == nil {
		return
	}
	cleanCtx := ctx
	if ctx.Err() != nil {
		// the start likely failed because ctx was cancelled, detach so teardown still runs
		var cancel context.CancelFunc
		cleanCtx, cancel = context.WithTimeout(common.WithLogger(context.Background(), common.Logger(ctx)), time.Minute)
		defer cancel()
	}
	if err := rc.cleanUpJobContainer(cleanCtx); err != nil {
		common.Logger(ctx).Errorf("Error while cleaning up after failed container start for job %s: %v", rc.JobName, err)
	}
}

func (rc *RunContext) IsHostEnv(ctx context.Context) bool {
	platform := rc.runsOnImage(ctx)
	image := rc.containerImage(ctx)
	return image == "" && (strings.EqualFold(platform, "-self-hosted") ||
		(rc.Config.ExecutionEnvironmentFactory != nil && strings.HasPrefix(platform, "lume://")))
}

func (rc *RunContext) stopContainer() common.Executor {
	return rc.stopJobContainer()
}

func (rc *RunContext) closeContainer() common.Executor {
	return func(ctx context.Context) error {
		if rc.JobContainer != nil {
			return rc.JobContainer.Close()(ctx)
		}
		return nil
	}
}

func (rc *RunContext) matrix() map[string]any {
	return rc.Matrix
}

func (rc *RunContext) result(result string) {
	rc.Run.Job().Result = result
}

func (rc *RunContext) steps() []*model.Step {
	// Return per-job copies of the steps. Matrix combinations run in parallel and share the
	// workflow model, but step execution mutates per-job fields and evaluates the If/Env nodes
	// in place, so the *model.Step instances must not be shared across jobs (see Step.Clone).
	shared := rc.Run.Job().Steps
	steps := make([]*model.Step, len(shared))
	for i, step := range shared {
		if step == nil {
			continue
		}
		steps[i] = step.Clone()
	}
	return steps
}

// topLevelRunContext walks the Parent chain to the outermost RunContext. Composite
// actions create child RunContexts whose sub-steps need to share the outer job step's
// summary file path so that nested writes accumulate under the right step_index.
func (rc *RunContext) topLevelRunContext() *RunContext {
	top := rc
	for top.Parent != nil {
		top = top.Parent
	}
	return top
}

// Executor returns a pipeline executor for all the steps in the job
func (rc *RunContext) Executor() (common.Executor, error) {
	var executor common.Executor
	jobType, err := rc.Run.Job().Type()

	switch jobType {
	case model.JobTypeDefault:
		executor = newJobExecutor(rc, &stepFactoryImpl{}, rc)
	case model.JobTypeReusableWorkflowLocal:
		executor = newLocalReusableWorkflowExecutor(rc)
	case model.JobTypeReusableWorkflowRemote:
		executor = newRemoteReusableWorkflowExecutor(rc)
	case model.JobTypeInvalid:
		return nil, err
	}

	return func(ctx context.Context) error {
		res, err := rc.isEnabled(ctx)
		if err != nil {
			// Record the failure so a job whose if-expression fails to evaluate
			// gets a result (and therefore a stop time) instead of being left
			// unfinished. rc.caller is only set for reusable workflows.
			rc.result("failure")
			if rc.caller != nil { // For Gitea
				rc.caller.setReusedWorkflowJobResult(rc.JobName, "failure")
			}
			return err
		}
		if res {
			return executor(ctx)
		}
		return nil
	}, nil
}

func (rc *RunContext) containerImage(ctx context.Context) string {
	job := rc.Run.Job()

	c := job.Container()
	if c != nil {
		return rc.ExprEval.Interpolate(ctx, c.Image)
	}

	return ""
}

func (rc *RunContext) runsOnImage(ctx context.Context) string {
	if rc.Run.Job().RunsOn() == nil {
		common.Logger(ctx).Errorf("'runs-on' key not defined in %s", rc.String())
	}

	job := rc.Run.Job()
	runsOn := job.RunsOn()
	for i, v := range runsOn {
		runsOn[i] = rc.ExprEval.Interpolate(ctx, v)
	}

	if pick := rc.Config.PlatformPicker; pick != nil {
		if image := pick(runsOn); image != "" {
			return image
		}
	}

	for _, platformName := range rc.runsOnPlatformNames(ctx) {
		image := rc.Config.Platforms[strings.ToLower(platformName)]
		if image != "" {
			return image
		}
	}

	return ""
}

func (rc *RunContext) runsOnPlatformNames(ctx context.Context) []string {
	job := rc.Run.Job()

	if job.RunsOn() == nil {
		return []string{}
	}

	// Evaluate a copy: RawRunsOn is shared across parallel matrix jobs, so interpolating it in
	// place would race and leak one matrix combination's runs-on into the others.
	rawRunsOn := model.CloneYamlNode(job.RawRunsOn)
	if err := rc.ExprEval.EvaluateYamlNode(ctx, &rawRunsOn); err != nil {
		common.Logger(ctx).Errorf("Error while evaluating runs-on: %v", err)
		return []string{}
	}

	return model.RunsOnFromNode(rawRunsOn)
}

func (rc *RunContext) platformImage(ctx context.Context) string {
	if containerImage := rc.containerImage(ctx); containerImage != "" {
		return containerImage
	}

	return rc.runsOnImage(ctx)
}

func (rc *RunContext) options(ctx context.Context) string {
	job := rc.Run.Job()
	c := job.Container()
	if c != nil {
		return rc.Config.ContainerOptions + " " + rc.ExprEval.Interpolate(ctx, c.Options)
	}

	return rc.Config.ContainerOptions
}

func (rc *RunContext) isEnabled(ctx context.Context) (bool, error) {
	job := rc.Run.Job()
	l := common.Logger(ctx)
	runJob, runJobErr := EvalBool(ctx, rc.ExprEval, job.If.Value, exprparser.DefaultStatusCheckSuccess)
	jobType, jobTypeErr := job.Type()

	if runJobErr != nil {
		return false, fmt.Errorf("if-expression %q evaluation failed: %s", job.If.Value, runJobErr)
	}

	if jobType == model.JobTypeInvalid {
		return false, jobTypeErr
	}

	if !runJob {
		if rc.caller != nil { // For Gitea
			rc.caller.setReusedWorkflowJobResult(rc.JobName, "skipped")
			return false, nil
		}
		l.WithField("jobResult", "skipped").Debugf("Skipping job '%s' due to '%s'", job.Name, job.If.Value)
		return false, nil
	}

	if jobType != model.JobTypeDefault {
		return true, nil
	}

	img := rc.platformImage(ctx)
	if img == "" {
		for _, platformName := range rc.runsOnPlatformNames(ctx) {
			l.Infof("Skipping unsupported platform -- Try running with `-P %+v=...`", platformName)
		}
		return false, nil
	}
	return true, nil
}

// proxyBuildArgs returns the job's proxy variables as docker build args. The docker CLI
// pre-populates these from its own client configuration, but act builds through the API,
// so without them a Dockerfile action's RUN steps have no network behind a proxy.
func (rc *RunContext) proxyBuildArgs() map[string]*string {
	if len(rc.Config.ProxyEnv) == 0 {
		return nil
	}
	args := make(map[string]*string, len(rc.Config.ProxyEnv))
	for name, value := range rc.Config.ProxyEnv {
		args[name] = &value
	}
	return args
}

func mergeMaps(maps ...map[string]string) map[string]string {
	rtnMap := make(map[string]string)
	for _, m := range maps {
		maps0.Copy(rtnMap, m)
	}
	return rtnMap
}

func createContainerName(parts ...string) string {
	name := strings.Join(parts, "-")
	pattern := regexp.MustCompile("[^a-zA-Z0-9]")
	name = pattern.ReplaceAllString(name, "-")
	name = strings.ReplaceAll(name, "--", "-")
	hash := sha256.Sum256([]byte(name))

	// SHA256 is 64 hex characters. So trim name to 63 characters to make room for the hash and separator
	trimmedName := strings.Trim(trimToLen(name, 63), "-")

	return fmt.Sprintf("%s-%x", trimmedName, hash)
}

func trimToLen(s string, l int) string {
	if l < 0 {
		l = 0
	}
	if len(s) > l {
		return s[:l]
	}
	return s
}

func (rc *RunContext) getJobContext() *model.JobContext {
	jobStatus := "success"
	if rc.jobFailed {
		jobStatus = "failure"
	}
	for _, stepStatus := range rc.StepResults {
		if stepStatus.Conclusion == model.StepStatusFailure {
			jobStatus = "failure"
			break
		}
	}
	// A cancelled run takes precedence over success/failure so cancelled() is true and
	// success()/failure() are false, matching GitHub Actions: on cancellation only
	// always() and cancelled() steps run.
	if rc.jobCancelled {
		jobStatus = "cancelled"
	}

	jobContext := &model.JobContext{
		Status:   jobStatus,
		Services: map[string]model.JobService{}, // an empty map, never null
	}
	if rc.jobContainerID != "" {
		jobContext.Container.ID = rc.jobContainerID
		jobContext.Container.Network = rc.jobNetworkName
	}
	for _, svc := range rc.serviceContainers {
		if svc.info == nil {
			continue
		}
		jobContext.Services[svc.name] = model.JobService{
			ID:      svc.info.ID,
			Network: rc.jobNetworkName,
			Ports:   svc.info.Ports,
		}
	}
	return jobContext
}

func (rc *RunContext) getStepsContext() map[string]*model.StepResult {
	return rc.StepResults
}

// getRunnerContext returns the `runner` context: what the execution environment knows
// (os, arch, temp, tool_cache) plus what only the runner process knows.
func (rc *RunContext) getRunnerContext(ctx context.Context) map[string]any {
	runnerContext := map[string]any{}
	if rc.JobContainer != nil {
		maps0.Copy(runnerContext, rc.JobContainer.GetRunnerContext(ctx))
		defaultToolCache, _ := runnerContext["tool_cache"].(string)
		runnerContext["tool_cache"] = rc.toolCache(defaultToolCache)
	}
	runnerContext["name"] = rc.Config.RunnerName
	runnerContext["environment"] = "self-hosted"
	if rc.Config.RunnerDebug() {
		runnerContext["debug"] = "1"
	}
	return runnerContext
}

func (rc *RunContext) getGithubContext(ctx context.Context) *model.GithubContext {
	logger := common.Logger(ctx)
	ghc := &model.GithubContext{
		Event:            make(map[string]any),
		Workflow:         rc.Run.Workflow.Name,
		RunID:            rc.Config.Env["GITHUB_RUN_ID"],
		RunNumber:        rc.Config.Env["GITHUB_RUN_NUMBER"],
		Actor:            rc.Config.Actor,
		EventName:        rc.Config.EventName,
		Action:           rc.CurrentStep,
		Token:            rc.Config.Token,
		Job:              rc.Run.JobID,
		ActionPath:       rc.ActionPath,
		ActionRepository: rc.Env["GITHUB_ACTION_REPOSITORY"],
		ActionRef:        rc.Env["GITHUB_ACTION_REF"],
		RepositoryOwner:  rc.Config.Env["GITHUB_REPOSITORY_OWNER"],
		RetentionDays:    rc.Config.Env["GITHUB_RETENTION_DAYS"],
		RunnerPerflog:    rc.Config.Env["RUNNER_PERFLOG"],
		RunnerTrackingID: rc.Config.Env["RUNNER_TRACKING_ID"],
		Repository:       rc.Config.Env["GITHUB_REPOSITORY"],
		Ref:              rc.Config.Env["GITHUB_REF"],
		Sha:              rc.Config.Env["SHA_REF"],
		RefName:          rc.Config.Env["GITHUB_REF_NAME"],
		RefType:          rc.Config.Env["GITHUB_REF_TYPE"],
		BaseRef:          rc.Config.Env["GITHUB_BASE_REF"],
		HeadRef:          rc.Config.Env["GITHUB_HEAD_REF"],
		Workspace:        rc.Config.Env["GITHUB_WORKSPACE"],
	}
	if rc.JobContainer != nil {
		ghc.EventPath = rc.JobContainer.GetActPath() + "/workflow/event.json"
		ghc.Workspace = rc.JobContainer.ToContainerPath(rc.Config.Workdir)
	}

	if ghc.RunID == "" {
		ghc.RunID = "1"
	}

	if ghc.RunNumber == "" {
		ghc.RunNumber = "1"
	}

	if ghc.RetentionDays == "" {
		ghc.RetentionDays = "0"
	}

	if ghc.RunnerPerflog == "" {
		ghc.RunnerPerflog = "/dev/null"
	}

	// Backwards compatibility for configs that require
	// a default rather than being run as a cmd
	if ghc.Actor == "" {
		ghc.Actor = "nektos/act"
	}

	{ // Adapt to Gitea
		if preset := rc.Config.PresetGitHubContext; preset != nil {
			ghc.Event = preset.Event
			ghc.RunID = preset.RunID
			ghc.RunNumber = preset.RunNumber
			ghc.RunAttempt = preset.RunAttempt
			ghc.Actor = preset.Actor
			ghc.Repository = preset.Repository
			ghc.EventName = preset.EventName
			ghc.Sha = preset.Sha
			ghc.Ref = preset.Ref
			ghc.RefName = preset.RefName
			ghc.RefType = preset.RefType
			ghc.HeadRef = preset.HeadRef
			ghc.BaseRef = preset.BaseRef
			ghc.Token = preset.Token
			ghc.RepositoryOwner = preset.RepositoryOwner
			ghc.RetentionDays = preset.RetentionDays

			instance := rc.Config.GitHubInstance
			if !strings.HasPrefix(instance, "http://") &&
				!strings.HasPrefix(instance, "https://") {
				instance = "https://" + instance
			}
			ghc.ServerURL = instance
			ghc.APIURL = instance + "/api/v1" // the version of Gitea is v1
			ghc.GraphQLURL = ""               // Gitea doesn't support graphql
			return ghc
		}
	}

	if rc.EventJSON != "" {
		err := json.Unmarshal([]byte(rc.EventJSON), &ghc.Event)
		if err != nil {
			logger.Errorf("Unable to Unmarshal event '%s': %v", rc.EventJSON, err)
		}
	}

	ghc.SetBaseAndHeadRef()
	repoPath := rc.Config.Workdir
	ghc.SetRepositoryAndOwner(ctx, rc.Config.GitHubInstance, rc.Config.RemoteName, repoPath)
	if ghc.Ref == "" {
		ghc.SetRef(ctx, rc.Config.DefaultBranch, repoPath)
	}
	if ghc.Sha == "" {
		ghc.SetSha(ctx, repoPath)
	}

	ghc.SetRefTypeAndName()

	// defaults
	ghc.ServerURL = "https://github.com"
	ghc.APIURL = "https://api.github.com"
	ghc.GraphQLURL = "https://api.github.com/graphql"
	// per GHES
	if rc.Config.GitHubInstance != "github.com" {
		ghc.ServerURL = "https://" + rc.Config.GitHubInstance
		ghc.APIURL = fmt.Sprintf("https://%s/api/v3", rc.Config.GitHubInstance)
		ghc.GraphQLURL = fmt.Sprintf("https://%s/api/graphql", rc.Config.GitHubInstance)
	}

	{ // Adapt to Gitea
		instance := rc.Config.GitHubInstance
		if !strings.HasPrefix(instance, "http://") &&
			!strings.HasPrefix(instance, "https://") {
			instance = "https://" + instance
		}
		ghc.ServerURL = instance
		ghc.APIURL = instance + "/api/v1" // the version of Gitea is v1
		ghc.GraphQLURL = ""               // Gitea doesn't support graphql
	}

	// allow to be overridden by user
	if rc.Config.Env["GITHUB_SERVER_URL"] != "" {
		ghc.ServerURL = rc.Config.Env["GITHUB_SERVER_URL"]
	}
	if rc.Config.Env["GITHUB_API_URL"] != "" {
		ghc.APIURL = rc.Config.Env["GITHUB_API_URL"]
	}
	if rc.Config.Env["GITHUB_GRAPHQL_URL"] != "" {
		ghc.GraphQLURL = rc.Config.Env["GITHUB_GRAPHQL_URL"]
	}

	return ghc
}

func isLocalCheckout(ghc *model.GithubContext, step *model.Step) bool {
	if step.Type() == model.StepTypeInvalid {
		// This will be errored out by the executor later, we need this here to avoid a null panic though
		return false
	}
	if step.Type() != model.StepTypeUsesActionRemote {
		return false
	}
	remoteAction := newRemoteAction(step.Uses)
	if remoteAction == nil {
		// IsCheckout() will nil panic if we dont bail out early
		return false
	}
	if !remoteAction.IsCheckout() {
		return false
	}

	if repository, ok := step.With["repository"]; ok && repository != ghc.Repository {
		return false
	}
	if repository, ok := step.With["ref"]; ok && repository != ghc.Ref {
		return false
	}
	return true
}

func nestedMapLookup(m map[string]any, ks ...string) (rval any) {
	var ok bool

	if len(ks) == 0 { // degenerate input
		return nil
	}
	if rval, ok = m[ks[0]]; !ok {
		return nil
	} else if len(ks) == 1 { // we've reached the final key
		return rval
	} else if m, ok = rval.(map[string]any); !ok {
		return nil
	} else { // 1+ more keys
		return nestedMapLookup(m, ks[1:]...)
	}
}

func (rc *RunContext) withGithubEnv(ctx context.Context, github *model.GithubContext, env map[string]string) {
	env["CI"] = "true"
	env["GITHUB_WORKFLOW"] = github.Workflow
	env["GITHUB_RUN_ID"] = github.RunID
	env["GITHUB_RUN_NUMBER"] = github.RunNumber
	env["GITHUB_ACTION"] = github.Action
	env["GITHUB_ACTION_PATH"] = github.ActionPath
	env["GITHUB_ACTION_REPOSITORY"] = github.ActionRepository
	env["GITHUB_ACTION_REF"] = github.ActionRef
	env["GITHUB_ACTIONS"] = "true"
	env["GITHUB_ACTOR"] = github.Actor
	env["GITHUB_REPOSITORY"] = github.Repository
	env["GITHUB_EVENT_NAME"] = github.EventName
	env["GITHUB_EVENT_PATH"] = github.EventPath
	env["GITHUB_WORKSPACE"] = github.Workspace
	env["GITHUB_SHA"] = github.Sha
	env["GITHUB_REF"] = github.Ref
	env["GITHUB_REF_NAME"] = github.RefName
	env["GITHUB_REF_TYPE"] = github.RefType
	env["GITHUB_JOB"] = github.Job
	env["GITHUB_REPOSITORY_OWNER"] = github.RepositoryOwner
	env["GITHUB_RETENTION_DAYS"] = github.RetentionDays
	env["RUNNER_PERFLOG"] = github.RunnerPerflog
	env["RUNNER_TRACKING_ID"] = github.RunnerTrackingID
	env["GITHUB_BASE_REF"] = github.BaseRef
	env["GITHUB_HEAD_REF"] = github.HeadRef
	env["GITHUB_SERVER_URL"] = github.ServerURL
	env["GITHUB_API_URL"] = github.APIURL
	env["GITHUB_GRAPHQL_URL"] = github.GraphQLURL

	{ // Adapt to Gitea
		instance := rc.Config.GitHubInstance
		if !strings.HasPrefix(instance, "http://") &&
			!strings.HasPrefix(instance, "https://") {
			instance = "https://" + instance
		}
		env["GITHUB_SERVER_URL"] = instance
		env["GITHUB_API_URL"] = instance + "/api/v1" // the version of Gitea is v1
		env["GITHUB_GRAPHQL_URL"] = ""               // Gitea doesn't support graphql
		env["GITHUB_RUN_ATTEMPT"] = github.RunAttempt
	}

	env["RUNNER_NAME"] = rc.Config.RunnerName
	env["RUNNER_ENVIRONMENT"] = "self-hosted"
	if workspace := parentDir(github.Workspace); workspace != "" {
		env["RUNNER_WORKSPACE"] = workspace
	}
	if rc.Config.RunnerDebug() {
		env["RUNNER_DEBUG"] = "1"
	}

	if rc.Config.ArtifactServerPath != "" {
		setActionRuntimeVars(rc, env)
	}

	if imageOS := rc.imageOS(ctx); imageOS != "" {
		env["ImageOS"] = imageOS
	}
}

// parentDir returns the directory containing p, or "" when p names no parent. Both
// separators are accepted rather than filepath's, as p may describe a container while
// the runner itself runs on Windows, or the other way round.
func parentDir(p string) string {
	if slash := strings.LastIndexAny(p, `/\`); slash > 0 {
		return p[:slash]
	}
	return ""
}

// imageOS returns ImageOS, which setup-* actions use to tell one runner image release
// from another. The resolved image tag is preferred over the runs-on label because it
// still names a release when the label is a rolling one such as ubuntu-latest.
func (rc *RunContext) imageOS(ctx context.Context) string {
	if rc.Run.Job().RunsOn() == nil {
		// A composite action runs on a synthetic job, and resolving its image would only
		// log that runs-on is missing.
		return ""
	}
	if imageOS := imageOSFromImage(rc.platformImage(ctx)); imageOS != "" {
		return imageOS
	}

	for _, platformName := range slices.Backward(rc.runsOnPlatformNames(ctx)) {
		if platformName == "ubuntu-latest" {
			// Rolling label whose image names no release either, so keep the historical value.
			return "ubuntu20"
		} else if platformName != "" {
			return strings.SplitN(strings.Replace(platformName, `-`, ``, 1), `.`, 2)[0]
		}
	}
	return ""
}

// imageOSTag matches an image reference tagged with an OS family ImageOS can report plus
// its release, such as "docker.gitea.com/runner-images:ubuntu-24.04". Anything else
// ("ubuntu-latest", "app:22.04", "catthehacker/ubuntu:act-22.04", or a registry port) is
// left to the runs-on label rather than turned into a bogus OS.
var imageOSTag = regexp.MustCompile(`:(ubuntu|win|macos)-?([0-9]+)[^/]*$`)

// imageOSFromImage derives ImageOS from an image reference, e.g.
// "docker.gitea.com/runner-images:ubuntu-24.04" yields "ubuntu24".
func imageOSFromImage(image string) string {
	if match := imageOSTag.FindStringSubmatch(image); match != nil {
		return match[1] + match[2]
	}
	return ""
}

func setActionRuntimeVars(rc *RunContext, env map[string]string) {
	actionsRuntimeURL := os.Getenv("ACTIONS_RUNTIME_URL")
	if actionsRuntimeURL == "" {
		actionsRuntimeURL = fmt.Sprintf("http://%s:%s/", rc.Config.ArtifactServerAddr, rc.Config.ArtifactServerPort)
	}
	env["ACTIONS_RUNTIME_URL"] = actionsRuntimeURL

	actionsRuntimeToken := os.Getenv("ACTIONS_RUNTIME_TOKEN")
	if actionsRuntimeToken == "" {
		actionsRuntimeToken = "token"
	}
	env["ACTIONS_RUNTIME_TOKEN"] = actionsRuntimeToken
}

func (rc *RunContext) handleCredentials(ctx context.Context) (string, string, error) {
	container := rc.Run.Job().Container()
	if container == nil || container.Credentials == nil {
		return "", "", nil
	}

	if len(container.Credentials) != 2 {
		err := errors.New("invalid property count for key 'credentials:'")
		return "", "", err
	}

	ee := rc.NewExpressionEvaluator(ctx)
	var username, password string
	if username = ee.Interpolate(ctx, container.Credentials["username"]); username == "" {
		err := errors.New("failed to interpolate container.credentials.username")
		return "", "", err
	}
	if password = ee.Interpolate(ctx, container.Credentials["password"]); password == "" {
		err := errors.New("failed to interpolate container.credentials.password")
		return "", "", err
	}

	if container.Credentials["username"] == "" || container.Credentials["password"] == "" {
		err := errors.New("container.credentials cannot be empty")
		return "", "", err
	}

	return username, password, nil
}

func (rc *RunContext) handleServiceCredentials(ctx context.Context, creds map[string]string) (username, password string, err error) {
	if creds == nil {
		return username, password, err
	}
	if len(creds) != 2 {
		err = errors.New("invalid property count for key 'credentials:'")
		return username, password, err
	}

	ee := rc.NewExpressionEvaluator(ctx)
	if username = ee.Interpolate(ctx, creds["username"]); username == "" {
		err = errors.New("failed to interpolate credentials.username")
		return username, password, err
	}

	if password = ee.Interpolate(ctx, creds["password"]); password == "" {
		err = errors.New("failed to interpolate credentials.password")
		return username, password, err
	}

	return username, password, err
}

// GetServiceBindsAndMounts returns the binds and mounts for the service container, resolving paths as appopriate
func (rc *RunContext) GetServiceBindsAndMounts(svcVolumes []string) ([]string, map[string]string) {
	binds, mounts, claimed := splitVolumes(svcVolumes)
	if daemonSocket := rc.containerDaemonSocket(); daemonSocket != "-" && !claimed["/var/run/docker.sock"] {
		binds = append(binds, getDockerDaemonSocketMountPath(daemonSocket)+":/var/run/docker.sock")
	}
	return binds, mounts
}
