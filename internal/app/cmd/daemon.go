// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"gitea.com/gitea/runner/internal/app/poll"
	"gitea.com/gitea/runner/internal/app/run"
	"gitea.com/gitea/runner/internal/pkg/client"
	"gitea.com/gitea/runner/internal/pkg/config"
	"gitea.com/gitea/runner/internal/pkg/envcheck"
	"gitea.com/gitea/runner/internal/pkg/labels"
	"gitea.com/gitea/runner/internal/pkg/lock"
	"gitea.com/gitea/runner/internal/pkg/metrics"
	"gitea.com/gitea/runner/internal/pkg/ver"

	"connectrpc.com/connect"
	"github.com/mattn/go-isatty"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

func runDaemon(ctx context.Context, daemArgs *daemonArgs, configFile *string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadDefault(*configFile)
		if err != nil {
			return fmt.Errorf("invalid configuration: %w", err)
		}

		initLogging(cfg)
		log.Infoln("Starting runner daemon")

		reg, err := config.LoadRegistration(cfg.Runner.File)
		if os.IsNotExist(err) {
			log.Error("registration file not found, please register the runner first")
			return err
		} else if err != nil {
			return fmt.Errorf("failed to load registration file: %w", err)
		}

		// Guard against a second runner process sharing this runner file: two
		// processes with the same identity are indistinguishable to Gitea and
		// end up cancelling each other's jobs.
		releaseLock, err := lock.TryLock(cfg.Runner.File)
		if errors.Is(err, lock.ErrLocked) {
			log.Errorf("another gitea-runner process is already using %q; each runner process needs its own runner file (runner.file)", cfg.Runner.File)
			return err
		} else if err != nil {
			// Best-effort guard: if the lock file can't be created (e.g. a
			// read-only runner-file mount), warn and start anyway rather than
			// refusing to run.
			log.Warnf("could not lock runner file %q, continuing without the single-process guard: %v", cfg.Runner.File, err)
		} else {
			// Held until shutdown finishes: the draining runner still owns this
			// identity on the server, so releasing early would let a restart
			// reintroduce the duplicate-identity job cancellations.
			defer func() { _ = releaseLock() }()
		}

		lbls := resolveLabels(daemArgs.Labels, cfg.Runner.Labels, reg.Labels)
		if err := cfg.ValidateLumeLabels(lbls); err != nil {
			return fmt.Errorf("invalid Lume runner labels: %w", err)
		}

		ls := labels.Labels{}
		for _, l := range lbls {
			label, err := labels.Parse(l)
			if err != nil {
				log.WithError(err).Warnf("ignored invalid label %q", l)
				continue
			}
			ls = append(ls, label)
		}
		if len(ls) == 0 {
			log.Warn("no labels configured, runner may not be able to pick up jobs")
		}

		// A restarted controller must finish ownership-checked cleanup before it
		// advertises capacity or asks Gitea for another task.
		if results, err := run.ReconcileLume(ctx, cfg); err != nil {
			return fmt.Errorf("Lume startup reconciliation failed: %w", err)
		} else if len(results) > 0 {
			log.Infof("reconciled %d durable Lume worker leases", len(results))
		}
		if err := run.ValidateLumeImages(ctx, cfg); err != nil {
			return fmt.Errorf("Lume image admission failed: %w", err)
		}
		if err := run.CheckLumeResources(cfg); err != nil {
			return fmt.Errorf("Lume resource admission failed: %w", err)
		}

		// Before the first Docker API call: the standard library resolves the proxy
		// environment once. Ungated because host labels still reach the daemon.
		if dockerSocketPath, err := getDockerSocketPath(cfg.Container.DockerHost); err == nil {
			run.BypassProxyForDockerHost(dockerSocketPath)
		} else {
			log.Debugf("cannot resolve the docker socket path, so Docker API calls are not exempted from the proxy: %v", err)
		}

		if ls.RequireDocker() || cfg.Container.RequireDocker {
			// Wait for dockerd be ready
			if timeout := cfg.Container.DockerTimeout; timeout > 0 {
				tctx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				keepRunning := true
				for keepRunning {
					dockerSocketPath, err := getDockerSocketPath(cfg.Container.DockerHost)
					if err != nil {
						log.Errorf("Failed to get socket path: %s", err.Error())
					} else if err = envcheck.CheckIfDockerRunning(tctx, dockerSocketPath); errors.Is(err, context.Canceled) {
						log.Infof("Docker wait timeout of %s expired", timeout.String())
						break
					} else if err != nil {
						log.Errorf("Docker connection failed: %s", err.Error())
					} else {
						log.Infof("Docker is ready")
						break
					}
					select {
					case <-time.After(time.Second):
					case <-tctx.Done():
						log.Infof("Docker wait timeout of %s expired", timeout.String())
						keepRunning = false
					}
				}
			}
			// Require dockerd be ready
			dockerSocketPath, err := getDockerSocketPath(cfg.Container.DockerHost)
			if err != nil {
				return err
			}
			if err := envcheck.CheckIfDockerRunning(ctx, dockerSocketPath); err != nil {
				return err
			}
			// if dockerSocketPath passes the check, override DOCKER_HOST with dockerSocketPath
			os.Setenv("DOCKER_HOST", dockerSocketPath)
			run.WarnIfDaemonHasNoProxy(ctx)
			// empty cfg.Container.DockerHost means runner need to find an available docker host automatically
			// and assign the path to cfg.Container.DockerHost
			if cfg.Container.DockerHost == "" {
				cfg.Container.DockerHost = dockerSocketPath
			}
			// check the scheme, if the scheme is not npipe or unix
			// set cfg.Container.DockerHost to "-" because it can't be mounted to the job container
			if protoIndex := strings.Index(cfg.Container.DockerHost, "://"); protoIndex != -1 {
				scheme := cfg.Container.DockerHost[:protoIndex]
				if !strings.EqualFold(scheme, "npipe") && !strings.EqualFold(scheme, "unix") {
					cfg.Container.DockerHost = "-"
				}
			}
		}

		if !slices.Equal(reg.Labels, ls.ToStrings()) {
			reg.Labels = ls.ToStrings()
			if err := config.SaveRegistration(cfg.Runner.File, reg); err != nil {
				return fmt.Errorf("failed to save runner config: %w", err)
			}
			log.Infof("labels updated to: %v", reg.Labels)
		}

		cli := client.New(
			reg.Address,
			cfg.Runner.Insecure,
			reg.UUID,
			reg.Token,
		)

		runner := run.NewRunner(cfg, reg, cli)
		defer func() {
			if err := runner.Close(); err != nil {
				log.Warnf("runner %s: cache server shutdown: %v", reg.Name, err)
			}
		}()

		// declare the labels of the runner before fetching tasks
		resp, err := runner.Declare(ctx, ls.Names())
		if err != nil && connect.CodeOf(err) == connect.CodeUnimplemented {
			log.Errorf("Your Gitea version is too old to support runner declare, please upgrade to v1.21 or later")
			return err
		} else if err != nil {
			log.WithError(err).Error("fail to invoke Declare")
			return err
		} else {
			log.Infof("runner: %s, with version: %s, with labels: %v, declare successfully",
				resp.Msg.Runner.Name, resp.Msg.Runner.Version, resp.Msg.Runner.Labels)
		}
		runner.SetCapabilitiesFromDeclare(resp)

		poller := poll.New(cfg, cli, runner)

		if cfg.Metrics.Enabled {
			metrics.Init()
			metrics.RunnerInfo.WithLabelValues(ver.Version(), resp.Msg.Runner.Name).Set(1)
			metrics.RunnerCapacity.Set(float64(cfg.Runner.Capacity))
			metrics.RegisterUptimeFunc(time.Now())
			metrics.RegisterRunningJobsFunc(runner.RunningCount, cfg.Runner.Capacity)
			metrics.StartServer(ctx, cfg.Metrics.Addr, func() (bool, string) {
				return poller.Ready(cfg.Metrics.ReadinessGrace)
			})
		}

		if daemArgs.Once || reg.Ephemeral {
			done := make(chan struct{})
			go func() {
				defer close(done)
				poller.PollOnce()
			}()

			// shutdown when we complete a job or cancel is requested
			select {
			case <-ctx.Done():
			case <-done:
			}
		} else {
			go poller.Poll()

			// Stop either on an external cancellation or when the poller shuts
			// itself down (e.g. after the runner has been unregistered).
			select {
			case <-ctx.Done():
			case <-poller.Done():
			}
		}

		log.Infof("runner: %s shutdown initiated, waiting %s for running jobs to complete before shutting down", resp.Msg.Runner.Name, cfg.Runner.ShutdownTimeout)

		ctx, cancel := context.WithTimeout(context.Background(), cfg.Runner.ShutdownTimeout)
		defer cancel()

		err = poller.Shutdown(ctx)
		if err != nil {
			log.Warnf("runner: %s cancelled in progress jobs during shutdown", resp.Msg.Runner.Name)
		}

		if poller.Unregistered() {
			return errors.New("runner is no longer registered with the server; please register it again")
		}

		return nil
	}
}

type daemonArgs struct {
	Once   bool
	Labels string
}

// resolveLabels picks the labels to run with: --labels/GITEA_RUNNER_LABELS > config > .runner.
// The flag lets a registered runner change its labels without deleting the .runner file.
func resolveLabels(argLabels string, cfgLabels, regLabels []string) []string {
	if lbls := splitLabels(argLabels); len(lbls) > 0 {
		return lbls
	}
	if len(cfgLabels) > 0 {
		return cfgLabels
	}
	return regLabels
}

func splitLabels(s string) []string {
	var lbls []string
	for l := range strings.SplitSeq(s, ",") {
		if l = strings.TrimSpace(l); l != "" {
			lbls = append(lbls, l)
		}
	}
	return lbls
}

// initLogging setup the global logrus logger.
func initLogging(cfg *config.Config) {
	callPrettyfier := func(f *runtime.Frame) (string, string) {
		// get function name
		s := strings.Split(f.Function, ".")
		funcname := "[" + s[len(s)-1] + "]"
		// get file name and line number
		_, filename := path.Split(f.File)
		filename = "[" + filename + ":" + strconv.Itoa(f.Line) + "]"
		return funcname, filename
	}

	isTerm := isatty.IsTerminal(os.Stdout.Fd())
	format := &log.TextFormatter{
		DisableColors:    !isTerm,
		FullTimestamp:    true,
		CallerPrettyfier: callPrettyfier,
	}
	log.SetFormatter(format)

	l := cfg.Log.Level
	if l == "" {
		log.Infof("Log level not set, sticking to info")
		return
	}

	level, err := log.ParseLevel(l)
	if err != nil {
		log.WithError(err).
			Errorf("invalid log level: %q", l)
	}

	// debug level
	switch level {
	case log.DebugLevel, log.TraceLevel:
		log.SetReportCaller(true) // Only in debug or trace because it takes a performance toll
		log.Infof("Log level %s requested, setting up report caller for further debugging", level)
	}

	if log.GetLevel() != level {
		log.Infof("log level set to %v", level)
		log.SetLevel(level)
	}
}

var commonSocketPaths = []string{
	"/var/run/docker.sock",
	"/run/podman/podman.sock",
	"$HOME/.colima/docker.sock",
	"$XDG_RUNTIME_DIR/docker.sock",
	"$XDG_RUNTIME_DIR/podman/podman.sock",
	`\\.\pipe\docker_engine`,
	"$HOME/.docker/run/docker.sock",
}

func getDockerSocketPath(configDockerHost string) (string, error) {
	// a `-` means don't mount the docker socket to job containers
	if configDockerHost != "" && configDockerHost != "-" {
		return configDockerHost, nil
	}

	socket, found := os.LookupEnv("DOCKER_HOST")
	if found {
		return socket, nil
	}

	for _, p := range commonSocketPaths {
		if _, err := os.Lstat(os.ExpandEnv(p)); err == nil {
			if strings.HasPrefix(p, `\\.\`) {
				return "npipe://" + filepath.ToSlash(os.ExpandEnv(p)), nil
			}
			return "unix://" + filepath.ToSlash(os.ExpandEnv(p)), nil
		}
	}

	return "", errors.New("daemon Docker Engine socket not found and docker_host config was invalid")
}
