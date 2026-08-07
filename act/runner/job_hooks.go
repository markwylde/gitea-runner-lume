// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"path"
	"strings"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/container"
)

// GitHub's job-hook variables, read as a fallback when the settings are unset.
const (
	jobStartedHookEnv   = "ACTIONS_RUNNER_HOOK_JOB_STARTED"
	jobCompletedHookEnv = "ACTIONS_RUNNER_HOOK_JOB_COMPLETED"
)

// Kept apart from the per-step file-command files, which are truncated on every step.
const (
	hookEnvFileCommand  = "workflow/hook-envs.txt"
	hookPathFileCommand = "workflow/hook-path.txt"
)

func (rc *RunContext) runJobStartedHook(ctx context.Context) error {
	return rc.runJobHook(ctx, cmp.Or(rc.Config.JobStartedHook, rc.Config.Env[jobStartedHookEnv]), "job started")
}

func (rc *RunContext) runJobCompletedHook(ctx context.Context) error {
	return rc.runJobHook(ctx, cmp.Or(rc.Config.JobCompletedHook, rc.Config.Env[jobCompletedHookEnv]), "job completed")
}

// runJobHook runs one hook in the job environment. Either hook failing fails the job, as
// on GitHub, where the operator is responsible for the hook's own resilience.
func (rc *RunContext) runJobHook(ctx context.Context, hookPath, name string) error {
	if hookPath == "" {
		return nil
	}

	cmd, shell := hookCommand(hookPath)
	rawLogger := common.Logger(ctx).WithField(rawOutputField, true)
	defer rawLogger.Infof("::endgroup::")
	rawLogger.Infof("::group::Run '%s'", EscapeCommandData(hookPath))
	rawLogger.Infof("A %s hook has been configured by the runner administrator", name)
	if shell != "" {
		rawLogger.Infof("shell: %s", shell)
	}

	env := maps.Clone(rc.GetEnv())
	if jobContainer := rc.Run.Job().Container(); jobContainer != nil {
		maps.Copy(env, jobContainer.Env)
	}
	rc.withGithubEnv(ctx, rc.getGithubContext(ctx), env)
	rc.ApplyExtraPath(ctx, &env)

	err := rc.setupHookFileCommands(ctx, env)
	if err == nil {
		err = rc.JobContainer.Exec(cmd, env, "", "")(ctx)
	}
	// Processed even on failure, so a hook that exports what it managed to set up before
	// failing still hands it to the job.
	err = cmp.Or(err, rc.processHookFileCommands(ctx))
	if err == nil {
		return nil
	}

	err = fmt.Errorf("the %s hook %q failed: %w", name, hookPath, err)
	// Flip the job status the way a failing pre step does, so success()-default main steps
	// skip and the task is reported failed.
	reportStepError(ctx, rc, err)
	return err
}

// setupHookFileCommands points the hook at its GITHUB_ENV and GITHUB_PATH files, so it can
// export to the job's steps, and truncates them so the second hook does not re-read what
// the first one wrote.
func (rc *RunContext) setupHookFileCommands(ctx context.Context, env map[string]string) error {
	actPath := rc.JobContainer.GetActPath()
	env["GITHUB_ENV"] = path.Join(actPath, hookEnvFileCommand)
	env["GITHUB_PATH"] = path.Join(actPath, hookPathFileCommand)
	env["GITEA_ENV"] = env["GITHUB_ENV"]
	env["GITEA_PATH"] = env["GITHUB_PATH"]

	return rc.JobContainer.Copy(actPath,
		&container.FileEntry{Name: hookEnvFileCommand, Mode: 0o666},
		&container.FileEntry{Name: hookPathFileCommand, Mode: 0o666},
	)(ctx)
}

func (rc *RunContext) processHookFileCommands(ctx context.Context) error {
	if err := processRunnerEnvFileCommand(ctx, hookEnvFileCommand, rc, rc.setEnv); err != nil {
		return err
	}
	return rc.UpdateExtraPath(ctx, path.Join(rc.JobContainer.GetActPath(), hookPathFileCommand))
}

// hookCommand mirrors actions/runner, which deliberately does not apply the shell flags it
// gives `run:` steps — a hook sets its own. See docs/adrs/1751-runner-job-hooks.md there.
// The second return value is how the invocation is shown in the log, empty when the file is
// executed directly.
func hookCommand(hookPath string) (cmd []string, shell string) {
	switch strings.ToLower(path.Ext(hookPath)) {
	case ".sh":
		return []string{"bash", "-e", hookPath}, "bash -e {0}"
	case ".ps1":
		return []string{"pwsh", "-command", ". '" + hookPath + "'"}, `pwsh -command ". '{0}'"`
	default:
		return []string{hookPath}, ""
	}
}
