// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"testing"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/model"

	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookContainer records the command a hook was run with and answers with what the hook
// wrote to its GITHUB_ENV and GITHUB_PATH files.
type hookContainer struct {
	fakeContainer
	cmd     []string
	env     map[string]string
	err     error
	envFile map[string]string
	pathTar []byte
}

func (c *hookContainer) ToContainerPath(path string) string { return path }
func (c *hookContainer) IsEnvironmentCaseInsensitive() bool { return false }

func (c *hookContainer) GetRunnerContext(context.Context) map[string]any {
	return map[string]any{"os": "Linux"}
}

func (c *hookContainer) Exec(command []string, env map[string]string, _, _ string) common.Executor {
	return func(context.Context) error {
		c.cmd, c.env = command, env
		return c.err
	}
}

func (c *hookContainer) UpdateFromEnv(_ string, env *map[string]string) common.Executor {
	return func(context.Context) error {
		maps.Copy(*env, c.envFile)
		return nil
	}
}

func (c *hookContainer) GetContainerArchive(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(c.pathTar)), nil
}

// newHookRunContext returns a RunContext and the context to run a hook with, whose logger is
// silenced so the hook's job-log output does not reach the test output.
func newHookRunContext(jobContainer *hookContainer, config *Config) (*RunContext, context.Context) {
	// Env is left nil so that it is built from the config, as it is for a real job.
	rc := &RunContext{
		Config:       config,
		Run:          &model.Run{JobID: "job", Workflow: &model.Workflow{Jobs: map[string]*model.Job{"job": {}}}},
		JobContainer: jobContainer,
	}
	logger, _ := test.NewNullLogger()
	ctx := common.WithJobErrorContainer(common.WithLogger(context.Background(), logger.WithField("test", true)))
	rc.ExprEval = rc.NewExpressionEvaluator(ctx)
	return rc, ctx
}

func TestRunJobHook(t *testing.T) {
	t.Run("runs the hook with the job environment", func(t *testing.T) {
		jobContainer := &hookContainer{}
		rc, ctx := newHookRunContext(jobContainer, &Config{
			JobStartedHook: "/hooks/started.sh",
			Env:            map[string]string{"A_VAR": "value", jobStartedHookEnv: "/from/env.sh"},
		})

		require.NoError(t, rc.runJobStartedHook(ctx))

		// The setting wins over the environment variable.
		assert.Equal(t, []string{"bash", "-e", "/hooks/started.sh"}, jobContainer.cmd)
		assert.Equal(t, "value", jobContainer.env["A_VAR"])
		// The github environment is there too, so a hook can tell which job it runs for.
		assert.Equal(t, "job", jobContainer.env["GITHUB_JOB"])
		assert.Equal(t, "/var/run/act/workflow/hook-envs.txt", jobContainer.env["GITHUB_ENV"])
		assert.Equal(t, "/var/run/act/workflow/hook-path.txt", jobContainer.env["GITHUB_PATH"])
	})

	// Each hook reads its own variable, so a swapped constant cannot pass.
	t.Run("falls back to the GitHub environment variables", func(t *testing.T) {
		for name, hook := range map[string]struct {
			env string
			run func(*RunContext, context.Context) error
		}{
			"started":   {jobStartedHookEnv, (*RunContext).runJobStartedHook},
			"completed": {jobCompletedHookEnv, (*RunContext).runJobCompletedHook},
		} {
			t.Run(name, func(t *testing.T) {
				jobContainer := &hookContainer{}
				rc, ctx := newHookRunContext(jobContainer, &Config{Env: map[string]string{hook.env: "/from/env.sh"}})

				require.NoError(t, hook.run(rc, ctx))
				assert.Equal(t, []string{"bash", "-e", "/from/env.sh"}, jobContainer.cmd)
			})
		}
	})

	t.Run("exports what the hook wrote to GITHUB_ENV and GITHUB_PATH", func(t *testing.T) {
		jobContainer := &hookContainer{
			envFile: map[string]string{"FROM_HOOK": "1"},
			pathTar: tarArchive(t, tarEntry{name: "hook-path.txt", body: "/opt/tool/bin\n"}),
		}
		rc, ctx := newHookRunContext(jobContainer, &Config{JobStartedHook: "/hooks/started.sh"})

		require.NoError(t, rc.runJobStartedHook(ctx))

		assert.Equal(t, "1", rc.Env["FROM_HOOK"])
		assert.Equal(t, []string{"/opt/tool/bin"}, rc.ExtraPath)
	})

	t.Run("a failing hook fails the job", func(t *testing.T) {
		rc, ctx := newHookRunContext(&hookContainer{err: errors.New("boom")}, &Config{JobStartedHook: "/hooks/started.sh"})

		err := rc.runJobStartedHook(ctx)

		require.ErrorContains(t, err, `the job started hook "/hooks/started.sh" failed`)
		require.ErrorContains(t, err, "boom")
		// The failure has to flip the job status, or success()-default steps would still
		// run and the task would be reported successful despite the missing setup.
		assert.Equal(t, "failure", rc.getJobContext().Status)
		require.ErrorContains(t, common.JobError(ctx), "boom")
	})

	t.Run("is a no-op without a hook", func(t *testing.T) {
		jobContainer := &hookContainer{}
		rc, ctx := newHookRunContext(jobContainer, &Config{})

		require.NoError(t, rc.runJobStartedHook(ctx))
		require.NoError(t, rc.runJobCompletedHook(ctx))
		assert.Nil(t, jobContainer.cmd)
	})
}

// actions/runner deliberately runs a hook without the flags it gives `run:` steps, and an
// executable without a known extension speaks for itself through its shebang.
func TestHookCommand(t *testing.T) {
	for hookPath, want := range map[string]struct {
		cmd   []string
		shell string
	}{
		"/hooks/started.sh":  {[]string{"bash", "-e", "/hooks/started.sh"}, "bash -e {0}"},
		"/hooks/started.PS1": {[]string{"pwsh", "-command", ". '/hooks/started.PS1'"}, `pwsh -command ". '{0}'"`},
		"/hooks/started":     {[]string{"/hooks/started"}, ""},
	} {
		cmd, shell := hookCommand(hookPath)
		assert.Equal(t, want.cmd, cmd, hookPath)
		assert.Equal(t, want.shell, shell, hookPath)
	}
}
