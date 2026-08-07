// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/internal/pkg/process"
)

func (r *Runner) checkConfiguredHealth(ctx context.Context) (bool, string) {
	script := r.cfg.HealthCheck.Script
	if script == "" {
		return true, "ok"
	}

	now := time.Now()
	if r.now != nil {
		now = r.now()
	}
	if !r.healthCheckLast.IsZero() && now.Sub(r.healthCheckLast) < r.cfg.HealthCheck.Interval {
		return r.healthCheckReady, r.healthCheckReason
	}

	env := processEnvironment()
	maps.Copy(env, r.cloneEnvs())
	env["GITEA_RUNNER_HEALTH_CHECK"] = "true"
	env["GITEA_RUNNER_NAME"] = r.name
	if r.client != nil {
		env["GITEA_INSTANCE_URL"] = r.client.Address()
	}
	env["GITEA_RUNNER_RUNNING_JOBS"] = strconv.FormatInt(r.RunningCount(), 10)

	runner := r.runHealthCheck
	if runner == nil {
		runner = executeHealthCheck
	}
	err := runner(ctx, script, r.cfg.HealthCheck.Timeout, env)
	r.healthCheckLast = now
	r.healthCheckReady = err == nil
	if err != nil {
		r.healthCheckReason = "runner health check failed: " + err.Error()
	} else {
		r.healthCheckReason = "ok"
	}
	return r.healthCheckReady, r.healthCheckReason
}

func executeHealthCheck(ctx context.Context, script string, timeout time.Duration, env map[string]string) error {
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, script)
	cmd.Env = envListFromMap(env)
	cmd.SysProcAttr = process.SysProcAttr(script, false)
	writer := common.NewLineWriter(func(line string) bool {
		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			common.Logger(ctx).Infof("health check: %s", line)
		}
		return true
	})
	cmd.Stdout = writer
	cmd.Stderr = writer

	treeKill := process.NewTreeKill(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	if killer, err := treeKill.Capture(cmd.Process); err == nil {
		defer killer.Close()
	}
	err := cmd.Wait()
	common.FlushWriter(writer)
	if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %s", timeout)
	}
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("exited with code %d", exitErr.ExitCode())
	}
	return err
}

func processEnvironment() map[string]string {
	environ := os.Environ()
	env := make(map[string]string, len(environ))
	for _, value := range environ {
		if key, item, ok := strings.Cut(value, "="); ok {
			env[key] = item
		}
	}
	return env
}
