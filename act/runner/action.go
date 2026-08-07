// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2022 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/common/git"
	"gitea.com/gitea/runner/act/container"
	"gitea.com/gitea/runner/act/model"

	"github.com/kballard/go-shellquote"
)

type actionStep interface {
	step

	getActionModel() *model.Action
	getCompositeRunContext(context.Context) *RunContext
	getCompositeSteps() *compositeSteps
}

type readAction func(ctx context.Context, step *model.Step, actionDir, actionPath string, readFile actionYamlReader, writeFile fileWriter) (*model.Action, error)

type actionYamlReader func(filename string) (io.Reader, io.Closer, error)

type fileWriter func(filename string, data []byte, perm fs.FileMode) error

type runAction func(step actionStep, actionDir string, remoteAction *remoteAction) common.Executor

//go:embed res/trampoline.js
var trampoline embed.FS

var (
	ContainerImageExistsLocally     = container.ImageExistsLocally
	ContainerNewDockerBuildExecutor = container.NewDockerBuildExecutor
)

func readActionImpl(ctx context.Context, step *model.Step, actionDir, actionPath string, readFile actionYamlReader, writeFile fileWriter) (*model.Action, error) {
	logger := common.Logger(ctx)
	allErrors := []error{}
	addError := func(fileName string, err error) {
		if err != nil {
			allErrors = append(allErrors, fmt.Errorf("failed to read '%s' from action '%s' with path '%s' of step %w", fileName, step.String(), actionPath, err))
		} else {
			// One successful read, clear error state
			allErrors = nil
		}
	}
	reader, closer, err := readFile("action.yml")
	addError("action.yml", err)
	if os.IsNotExist(err) {
		reader, closer, err = readFile("action.yaml")
		addError("action.yaml", err)
		if os.IsNotExist(err) {
			_, closer, err := readFile("Dockerfile")
			addError("Dockerfile", err)
			if err == nil {
				closer.Close()
				action := &model.Action{
					Name: "(Synthetic)",
					Runs: model.ActionRuns{
						Using: "docker",
						Image: "Dockerfile",
					},
				}
				logger.Debugf("Using synthetic action %v for Dockerfile", action)
				return action, nil
			}
			if step.With != nil {
				if val, ok := step.With["args"]; ok {
					var b []byte
					if b, err = trampoline.ReadFile("res/trampoline.js"); err != nil {
						return nil, err
					}
					err2 := writeFile(filepath.Join(actionDir, actionPath, "trampoline.js"), b, 0o400)
					if err2 != nil {
						return nil, err2
					}
					action := &model.Action{
						Name: "(Synthetic)",
						Inputs: map[string]model.Input{
							"cwd": {
								Description: "(Actual working directory)",
								Required:    false,
								Default:     filepath.Join(actionDir, actionPath),
							},
							"command": {
								Description: "(Actual program)",
								Required:    false,
								Default:     val,
							},
						},
						Runs: model.ActionRuns{
							Using: "node12",
							Main:  "trampoline.js",
						},
					}
					logger.Debugf("Using synthetic action %v", action)
					return action, nil
				}
			}
		}
	}
	if allErrors != nil {
		return nil, errors.Join(allErrors...)
	}
	defer closer.Close()

	action, err := model.ReadAction(reader)
	// For Gitea, reduce log noise
	// logger.Debugf("Read action %v from '%s'", action, "Unknown")
	return action, err
}

// cachedActionTar returns the action's tree from the action cache, which only a remote action
// has an entry in.
func cachedActionTar(ctx context.Context, step actionStep, name, includePrefix string) (io.ReadCloser, error) {
	remote, ok := step.(*stepActionRemote)
	if !ok {
		return nil, fmt.Errorf("action %q is a remote action but runs as %T", name, step)
	}
	return step.getRunContext().Config.ActionCache.GetTarArchive(ctx, remote.cacheDir, remote.resolvedSha, includePrefix)
}

func maybeCopyToActionDir(ctx context.Context, step actionStep, actionDir, actionPath, containerActionDir string) error {
	logger := common.Logger(ctx)
	rc := step.getRunContext()
	stepModel := step.getStepModel()

	if stepModel.Type() != model.StepTypeUsesActionRemote {
		return nil
	}

	var containerActionDirCopy string
	containerActionDirCopy = strings.TrimSuffix(containerActionDir, actionPath)
	logger.Debug(containerActionDirCopy)

	if !strings.HasSuffix(containerActionDirCopy, `/`) {
		containerActionDirCopy += `/`
	}

	if rc.Config != nil && rc.Config.ActionCache != nil {
		ta, err := cachedActionTar(ctx, step, stepModel.Uses, "")
		if err != nil {
			return err
		}
		defer ta.Close()
		return rc.JobContainer.CopyTarStream(ctx, containerActionDirCopy, ta)
	}

	defer git.AcquireCloneLock(actionDir)()

	if err := removeGitIgnore(ctx, actionDir); err != nil {
		return err
	}

	return rc.JobContainer.CopyDir(containerActionDirCopy, actionDir+"/", rc.Config.UseGitIgnore)(ctx)
}

func runActionImpl(step actionStep, actionDir string, remoteAction *remoteAction) common.Executor {
	rc := step.getRunContext()
	stepModel := step.getStepModel()

	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		actionPath := ""
		if remoteAction != nil && remoteAction.Path != "" {
			actionPath = remoteAction.Path
		}

		action := step.getActionModel()
		// For Gitea, reduce log noise
		// logger.Debugf("About to run action %v", action)

		err := setupActionEnv(ctx, step, remoteAction)
		if err != nil {
			return err
		}

		actionLocation := path.Join(actionDir, actionPath)
		actionName, containerActionDir := getContainerActionPaths(stepModel, actionLocation, rc)

		logger.Debugf("type=%v actionDir=%s actionPath=%s workdir=%s actionCacheDir=%s actionName=%s containerActionDir=%s", stepModel.Type(), actionDir, actionPath, rc.Config.Workdir, rc.ActionCacheDir(), actionName, containerActionDir)

		x := action.Runs.Using
		switch {
		case x.IsNode():
			if err := maybeCopyToActionDir(ctx, step, actionDir, actionPath, containerActionDir); err != nil {
				return err
			}
			containerArgs := []string{"node", path.Join(containerActionDir, action.Runs.Main)}
			logger.Debugf("executing remote job container: %s", containerArgs)

			rc.ApplyExtraPath(ctx, step.getEnv())

			return rc.execJobContainer(containerArgs, *step.getEnv(), "", "")(ctx)
		case x.IsDocker():
			location := actionLocation
			if remoteAction == nil {
				location = containerActionDir
			}
			return execAsDocker(ctx, step, actionName, actionDir, location, remoteAction == nil, stepStageMain)
		case x.IsComposite():
			if err := maybeCopyToActionDir(ctx, step, actionDir, actionPath, containerActionDir); err != nil {
				return err
			}

			return execAsComposite(step)(ctx)
		case x == model.ActionRunsUsingGo:
			if err := maybeCopyToActionDir(ctx, step, actionDir, actionPath, containerActionDir); err != nil {
				return err
			}

			rc.ApplyExtraPath(ctx, step.getEnv())

			execFileName := action.Runs.Main + ".out"
			buildArgs := []string{"go", "build", "-o", execFileName, action.Runs.Main}
			execArgs := []string{filepath.Join(containerActionDir, execFileName)}

			return common.NewPipelineExecutor(
				rc.execJobContainer(buildArgs, *step.getEnv(), "", containerActionDir),
				rc.execJobContainer(execArgs, *step.getEnv(), "", ""),
			)(ctx)
		default:
			return fmt.Errorf("The runs.using key must be one of: %v, got %s", []string{
				model.ActionRunsUsingDocker,
				model.ActionRunsUsingNode12,
				model.ActionRunsUsingNode16,
				model.ActionRunsUsingNode20,
				model.ActionRunsUsingNode24,
				model.ActionRunsUsingComposite,
				model.ActionRunsUsingGo,
			}, action.Runs.Using)
		}
	}
}

func setupActionEnv(ctx context.Context, step actionStep, _ *remoteAction) error {
	rc := step.getRunContext()

	// A few fields in the environment (e.g. GITHUB_ACTION_REPOSITORY)
	// are dependent on the action. That means we can complete the
	// setup only after resolving the whole action model and cloning
	// the action
	rc.withGithubEnv(ctx, step.getGithubContext(ctx), *step.getEnv())
	populateEnvsFromSavedState(step.getEnv(), step, rc)
	populateEnvsFromInput(ctx, step.getEnv(), step.getActionModel(), rc)

	return nil
}

// https://github.com/nektos/act/issues/228#issuecomment-629709055
// files in .gitignore are not copied in a Docker container
// this causes issues with actions that ignore other important resources
// such as `node_modules` for example
func removeGitIgnore(ctx context.Context, directory string) error {
	gitIgnorePath := path.Join(directory, ".gitignore")
	if _, err := os.Stat(gitIgnorePath); err == nil {
		// .gitignore exists
		common.Logger(ctx).Debugf("Removing %s before docker cp", gitIgnorePath)
		err := os.Remove(gitIgnorePath)
		if err != nil {
			return err
		}
	}
	return nil
}

// dockerActionImageTag derives the local docker image tag used when an action
// is built from a Dockerfile.
//
// For Gitea: a local action (`uses: ./` or `uses: ./path`) has an actionName
// that is the workspace-relative path of the action. That path is identical
// across repositories (e.g. "./" for a self-referencing action), so without
// namespacing, every repository's local docker action would build and reuse the
// same `act-dockeraction:latest` image on a shared docker daemon. A subsequent
// repository would then silently run the image built for an earlier one.
// Including the repository keeps the tag stable for caching within a repository
// while preventing cross-repository collisions.
// See https://gitea.com/gitea/runner/issues/1039.
func dockerActionImageTag(repository, actionName string, localAction bool) string {
	name := actionName
	if localAction {
		name = path.Join(repository, actionName)
	}
	// The human-readable name is sanitized by collapsing every non-alphanumeric character to "-".
	sanitized := regexp.MustCompile("[^a-zA-Z0-9]").ReplaceAllString(name, "-")
	if localAction {
		// For local actions a short hash of the raw repository and action path is appended so the tag stays unique per repository.
		sum := sha256.Sum256([]byte(repository + "\x00" + actionName))
		sanitized += "-" + hex.EncodeToString(sum[:])[:12]
	}
	// "-dockeraction" ensures that "./", "./test " won't get converted to "act-:latest", "act-test-:latest" which are invalid docker image names
	image := fmt.Sprintf("%s-dockeraction:%s", sanitized, "latest")
	image = "act-" + strings.TrimLeft(image, "-")
	return strings.ToLower(image)
}

// TODO: break out parts of function to reduce complexicity
func execAsDocker(ctx context.Context, step actionStep, actionName, actionDir, basedir string, localAction bool, stage stepStage) error {
	logger := common.Logger(ctx)
	rc := step.getRunContext()
	if rc.Config.DisallowDockerActions {
		return errors.New("Docker actions are disabled for this execution environment")
	}
	action := step.getActionModel()

	var prepImage common.Executor
	var image string
	forcePull := false
	if after, ok := strings.CutPrefix(action.Runs.Image, "docker://"); ok {
		image = after
		// Apply forcePull only for prebuild docker images
		forcePull = rc.Config.ForcePull
	} else {
		image = dockerActionImageTag(step.getGithubContext(ctx).Repository, actionName, localAction)
		contextDir, fileName := filepath.Split(filepath.Join(basedir, action.Runs.Image))

		anyArchExists, err := ContainerImageExistsLocally(ctx, image, "any")
		if err != nil {
			return err
		}

		correctArchExists, err := ContainerImageExistsLocally(ctx, image, rc.Config.ContainerArchitecture)
		if err != nil {
			return err
		}

		if anyArchExists && !correctArchExists {
			wasRemoved, err := container.RemoveImage(ctx, image, true, true)
			if err != nil {
				return err
			}
			if !wasRemoved {
				return fmt.Errorf("failed to remove image '%s'", image)
			}
		}

		if !correctArchExists || rc.Config.ForceRebuild {
			logger.Debugf("image '%s' for architecture '%s' will be built from context '%s", image, rc.Config.ContainerArchitecture, contextDir)
			var buildContext io.ReadCloser
			if localAction {
				buildContext, err = rc.JobContainer.GetContainerArchive(ctx, contextDir+"/.")
				if err != nil {
					return err
				}
				defer buildContext.Close()
			} else if rc.Config.ActionCache != nil {
				buildContext, err = cachedActionTar(ctx, step, actionName, contextDir)
				if err != nil {
					return err
				}
				defer buildContext.Close()
			}
			prepImage = ContainerNewDockerBuildExecutor(container.NewDockerBuildExecutorInput{
				ContextDir:   contextDir,
				Dockerfile:   fileName,
				ImageTag:     image,
				BuildContext: buildContext,
				Platform:     rc.Config.ContainerArchitecture,
				BuildArgs:    rc.proxyBuildArgs(),
			})
			if buildContext == nil {
				// Held across the whole build: the daemon drains contextDir lazily.
				inner := prepImage
				prepImage = func(ctx context.Context) error {
					defer git.AcquireCloneLock(actionDir)()
					return inner(ctx)
				}
			}
		} else {
			logger.Debugf("image '%s' for architecture '%s' already exists", image, rc.Config.ContainerArchitecture)
		}
	}
	eval := rc.NewStepExpressionEvaluator(ctx, step)
	cmd, err := shellquote.Split(eval.Interpolate(ctx, step.getStepModel().With["args"]))
	if err != nil {
		return err
	}
	if len(cmd) == 0 {
		cmd = action.Runs.Args
		evalDockerArgs(ctx, step, action, &cmd)
	}
	entrypoint, err := dockerEntrypoint(ctx, step, eval, stage)
	if err != nil {
		return err
	}
	stepContainer := newStepContainer(ctx, step, image, cmd, entrypoint)
	return common.NewPipelineExecutor(
		prepImage,
		stepContainer.Pull(forcePull),
		stepContainer.Remove().IfBool(!rc.Config.ReuseContainers),
		stepContainer.Create(rc.Config.ContainerCapAdd, rc.Config.ContainerCapDrop),
		stepContainer.Start(true),
	).Finally(
		stepContainer.Remove().IfBool(!rc.Config.ReuseContainers && !rc.Config.AutoRemove),
	).Finally(stepContainer.Close())(ctx)
}

// dockerEntrypoint returns the entrypoint the action's image runs with for the given
// stage. Only the main stage honours the `entrypoint` input.
func dockerEntrypoint(ctx context.Context, step actionStep, eval ExpressionEvaluator, stage stepStage) ([]string, error) {
	runs := step.getActionModel().Runs

	var entrypoint string
	switch stage {
	case stepStagePre:
		entrypoint = runs.PreEntrypoint
	case stepStagePost:
		entrypoint = runs.PostEntrypoint
	default:
		if fields := strings.Fields(eval.Interpolate(ctx, step.getStepModel().With["entrypoint"])); len(fields) > 0 {
			return fields, nil
		}
		entrypoint = runs.Entrypoint
	}

	if entrypoint == "" {
		return nil, nil
	}
	return shellquote.Split(entrypoint)
}

func evalDockerArgs(ctx context.Context, step step, action *model.Action, cmd *[]string) {
	rc := step.getRunContext()
	stepModel := step.getStepModel()

	inputs := make(map[string]string)
	eval := rc.NewExpressionEvaluator(ctx)
	// Set Defaults
	for k, input := range action.Inputs {
		inputs[k] = eval.Interpolate(ctx, input.Default)
	}
	if stepModel.With != nil {
		for k, v := range stepModel.With {
			inputs[k] = eval.Interpolate(ctx, v)
		}
	}
	mergeIntoMap(step, step.getEnv(), inputs)

	stepEE := rc.NewStepExpressionEvaluator(ctx, step)
	for i, v := range *cmd {
		(*cmd)[i] = stepEE.Interpolate(ctx, v)
	}
	mergeIntoMap(step, step.getEnv(), action.Runs.Env)

	ee := rc.NewStepExpressionEvaluator(ctx, step)
	for k, v := range *step.getEnv() {
		(*step.getEnv())[k] = ee.Interpolate(ctx, v)
	}
}

func newStepContainer(ctx context.Context, step step, image string, cmd, entrypoint []string) container.Container {
	rc := step.getRunContext()
	stepModel := step.getStepModel()
	rawLogger := common.Logger(ctx).WithField("raw_output", true)
	logWriter := common.NewLineWriter(rc.commandHandler(ctx), func(s string) bool {
		if rc.Config.LogOutput {
			rawLogger.Infof("%s", s)
		} else {
			rawLogger.Debugf("%s", s)
		}
		return true
	})
	envList := make([]string, 0)
	for k, v := range *step.getEnv() {
		envList = append(envList, fmt.Sprintf("%s=%s", k, v))
	}

	envList = append(envList, rc.runnerEnv(ctx)...)

	binds, mounts := rc.GetBindsAndMounts()
	networkMode := "container:" + rc.jobContainerName()
	if rc.IsHostEnv(ctx) {
		networkMode = "default"
	}
	stepContainer := ContainerNewContainer(&container.NewContainerInput{
		Cmd:          cmd,
		Entrypoint:   entrypoint,
		WorkingDir:   rc.JobContainer.ToContainerPath(rc.Config.Workdir),
		Image:        image,
		Name:         createContainerName(rc.jobContainerName(), "STEP-"+stepModel.ID),
		Env:          envList,
		Mounts:       mounts,
		NetworkMode:  networkMode,
		Binds:        binds,
		Stdout:       logWriter,
		Stderr:       logWriter,
		Privileged:   rc.Config.Privileged,
		UsernsMode:   rc.Config.UsernsMode,
		Platform:     rc.Config.ContainerArchitecture,
		Options:      rc.Config.ContainerOptions,
		AutoRemove:   rc.Config.AutoRemove,
		ValidVolumes: rc.validVolumes(),
		AllocatePTY:  rc.Config.AllocatePTY,
	})
	return stepContainer
}

func populateEnvsFromSavedState(env *map[string]string, step actionStep, rc *RunContext) {
	state, ok := rc.IntraActionState[step.getStepModel().ID]
	if ok {
		for name, value := range state {
			envName := "STATE_" + name
			(*env)[envName] = value
		}
	}
}

func populateEnvsFromInput(ctx context.Context, env *map[string]string, action *model.Action, rc *RunContext) {
	eval := rc.NewExpressionEvaluator(ctx)
	for inputID, input := range action.Inputs {
		envKey := regexp.MustCompile("[^A-Z0-9-]").ReplaceAllString(strings.ToUpper(inputID), "_")
		envKey = "INPUT_" + envKey
		if _, ok := (*env)[envKey]; !ok {
			(*env)[envKey] = eval.Interpolate(ctx, input.Default)
		}
	}
}

func getContainerActionPaths(step *model.Step, actionDir string, rc *RunContext) (string, string) {
	actionName := ""
	containerActionDir := "."
	if step.Type() != model.StepTypeUsesActionRemote {
		actionName = getOsSafeRelativePath(actionDir, rc.Config.Workdir)
		containerActionDir = rc.JobContainer.ToContainerPath(rc.Config.Workdir) + "/" + actionName
		actionName = "./" + actionName
	} else if step.Type() == model.StepTypeUsesActionRemote {
		actionName = getOsSafeRelativePath(actionDir, rc.ActionCacheDir())
		containerActionDir = rc.JobContainer.GetActPath() + "/actions/" + actionName
	}

	if actionName == "" {
		actionName = filepath.Base(actionDir)
		if runtime.GOOS == "windows" {
			actionName = strings.ReplaceAll(actionName, "\\", "/")
		}
	}
	return actionName, containerActionDir
}

func getOsSafeRelativePath(s, prefix string) string {
	actionName := strings.TrimPrefix(s, prefix)
	if runtime.GOOS == "windows" {
		actionName = strings.ReplaceAll(actionName, "\\", "/")
	}
	actionName = strings.TrimPrefix(actionName, "/")

	return actionName
}

func shouldRunPreStep(step actionStep) common.Conditional {
	return func(ctx context.Context) bool {
		log := common.Logger(ctx)

		if step.getActionModel() == nil {
			log.Debugf("skip pre step for '%s': no action model available", step.getStepModel())
			return false
		}

		return true
	}
}

func hasPreStep(step actionStep) common.Conditional {
	return func(ctx context.Context) bool {
		action := step.getActionModel()
		return action.Runs.Using.IsComposite() ||
			(action.Runs.Using.IsNode() &&
				action.Runs.Pre != "") ||
			(action.Runs.Using.IsDocker() &&
				action.Runs.PreEntrypoint != "") ||
			(action.Runs.Using == model.ActionRunsUsingGo &&
				action.Runs.Pre != "")
	}
}

// actionStagePaths resolves where a step's action lives and where the job container sees
// it, for the pre and post stage.
func actionStagePaths(step actionStep) (actionDir, actionPath, actionName, containerActionDir string) {
	rc := step.getRunContext()
	stepModel := step.getStepModel()

	if sar, ok := step.(*stepActionRemote); ok {
		actionDir = sar.actionDir()
		actionPath = newRemoteAction(stepModel.Uses).Path
	} else {
		actionDir = filepath.Join(rc.Config.Workdir, stepModel.Uses)
	}

	actionName, containerActionDir = getContainerActionPaths(stepModel, path.Join(actionDir, actionPath), rc)
	return actionDir, actionPath, actionName, containerActionDir
}

// execDockerActionStage runs a docker action's image for its pre or post stage.
func execDockerActionStage(ctx context.Context, step actionStep, stage stepStage) error {
	actionDir, actionPath, actionName, containerActionDir := actionStagePaths(step)

	_, remote := step.(*stepActionRemote)
	location := containerActionDir
	if remote {
		location = path.Join(actionDir, actionPath)
	}
	return execAsDocker(ctx, step, actionName, actionDir, location, !remote, stage)
}

func runPreStep(step actionStep) common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		logger.Debugf("run pre step for '%s'", step.getStepModel())

		rc := step.getRunContext()
		action := step.getActionModel()

		actionDir, actionPath, _, containerActionDir := actionStagePaths(step)

		x := action.Runs.Using
		switch {
		case x.IsNode():
			// defaults in pre steps were missing, however provided inputs are available
			populateEnvsFromInput(ctx, step.getEnv(), action, rc)

			if err := maybeCopyToActionDir(ctx, step, actionDir, actionPath, containerActionDir); err != nil {
				return err
			}

			containerArgs := []string{"node", path.Join(containerActionDir, action.Runs.Pre)}
			logger.Debugf("executing remote job container: %s", containerArgs)

			rc.ApplyExtraPath(ctx, step.getEnv())

			return rc.execJobContainer(containerArgs, *step.getEnv(), "", "")(ctx)

		case x.IsDocker():
			// defaults in pre steps were missing, however provided inputs are available
			populateEnvsFromInput(ctx, step.getEnv(), action, rc)

			return execDockerActionStage(ctx, step, stepStagePre)

		case x.IsComposite():
			if step.getCompositeSteps() == nil {
				step.getCompositeRunContext(ctx)
			}

			if steps := step.getCompositeSteps(); steps != nil && steps.pre != nil {
				return steps.pre(ctx)
			}
			return errors.New("missing steps in composite action")

		case x == model.ActionRunsUsingGo:
			// defaults in pre steps were missing, however provided inputs are available
			populateEnvsFromInput(ctx, step.getEnv(), action, rc)

			if err := maybeCopyToActionDir(ctx, step, actionDir, actionPath, containerActionDir); err != nil {
				return err
			}

			rc.ApplyExtraPath(ctx, step.getEnv())

			execFileName := action.Runs.Pre + ".out"
			buildArgs := []string{"go", "build", "-o", execFileName, action.Runs.Pre}
			execArgs := []string{filepath.Join(containerActionDir, execFileName)}

			return common.NewPipelineExecutor(
				rc.execJobContainer(buildArgs, *step.getEnv(), "", containerActionDir),
				rc.execJobContainer(execArgs, *step.getEnv(), "", ""),
			)(ctx)
		default:
			return nil
		}
	}
}

func shouldRunPostStep(step actionStep) common.Conditional {
	return func(ctx context.Context) bool {
		log := common.Logger(ctx)
		stepResults := step.getRunContext().getStepsContext()
		stepResult := stepResults[step.getStepModel().ID]

		if stepResult == nil {
			log.WithField("stepResult", model.StepStatusSkipped).Debugf("skipping post step for '%s'; step was not executed", step.getStepModel())
			return false
		}

		if stepResult.Conclusion == model.StepStatusSkipped {
			log.WithField("stepResult", model.StepStatusSkipped).Debugf("skipping post step for '%s'; main step was skipped", step.getStepModel())
			return false
		}

		if step.getActionModel() == nil {
			log.WithField("stepResult", model.StepStatusSkipped).Debugf("skipping post step for '%s': no action model available", step.getStepModel())
			return false
		}

		return true
	}
}

func hasPostStep(step actionStep) common.Conditional {
	return func(ctx context.Context) bool {
		action := step.getActionModel()
		return action.Runs.Using.IsComposite() ||
			(action.Runs.Using.IsNode() &&
				action.Runs.Post != "") ||
			(action.Runs.Using.IsDocker() &&
				action.Runs.PostEntrypoint != "") ||
			(action.Runs.Using == model.ActionRunsUsingGo &&
				action.Runs.Post != "")
	}
}

func runPostStep(step actionStep) common.Executor {
	return func(ctx context.Context) error {
		logger := common.Logger(ctx)
		logger.Debugf("run post step for '%s'", step.getStepModel())

		rc := step.getRunContext()
		action := step.getActionModel()

		actionDir, actionPath, _, containerActionDir := actionStagePaths(step)

		x := action.Runs.Using
		switch {
		case x.IsNode():

			populateEnvsFromSavedState(step.getEnv(), step, rc)

			containerArgs := []string{"node", path.Join(containerActionDir, action.Runs.Post)}
			logger.Debugf("executing remote job container: %s", containerArgs)

			rc.ApplyExtraPath(ctx, step.getEnv())

			return rc.execJobContainer(containerArgs, *step.getEnv(), "", "")(ctx)

		case x.IsDocker():
			populateEnvsFromSavedState(step.getEnv(), step, rc)

			return execDockerActionStage(ctx, step, stepStagePost)

		case x.IsComposite():
			if err := maybeCopyToActionDir(ctx, step, actionDir, actionPath, containerActionDir); err != nil {
				return err
			}

			if steps := step.getCompositeSteps(); steps != nil && steps.post != nil {
				return steps.post(ctx)
			}
			return errors.New("missing steps in composite action")

		case x == model.ActionRunsUsingGo:
			populateEnvsFromSavedState(step.getEnv(), step, rc)
			rc.ApplyExtraPath(ctx, step.getEnv())

			execFileName := action.Runs.Post + ".out"
			buildArgs := []string{"go", "build", "-o", execFileName, action.Runs.Post}
			execArgs := []string{filepath.Join(containerActionDir, execFileName)}

			return common.NewPipelineExecutor(
				rc.execJobContainer(buildArgs, *step.getEnv(), "", containerActionDir),
				rc.execJobContainer(execArgs, *step.getEnv(), "", ""),
			)(ctx)

		default:
			return nil
		}
	}
}
