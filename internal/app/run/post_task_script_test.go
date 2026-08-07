// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitea.com/gitea/runner/internal/pkg/config"
	"gitea.com/gitea/runner/internal/pkg/metrics"
	"gitea.com/gitea/runner/internal/pkg/report"

	runnerv1 "gitea.dev/actions-proto-go/runner/v1"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestRunPostTaskScriptSkippedWhenEmpty(t *testing.T) {
	r := &Runner{
		cfg: &config.Config{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)
	task := &runnerv1.Task{Id: 1, Context: taskCtx}
	reporter := report.NewReporter(ctx, cancel, nil, task, r.cfg)

	require.NotPanics(t, func() {
		r.runPostTaskScript(ctx, reporter, task, "/workspace/owner/repo")
	})
}

func TestRunPostTaskScriptNonZeroExitDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fail.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 2\n"), 0o700))

	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	cfg.Runner.PostTaskScript = scriptPath

	r := &Runner{cfg: cfg}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)
	task := &runnerv1.Task{Id: 1, Context: taskCtx}
	reporter := report.NewReporter(ctx, cancel, nil, task, cfg)

	require.NotPanics(t, func() {
		r.runPostTaskScript(ctx, reporter, task, "/workspace/owner/repo")
	})
}

func TestPostTaskScriptContextUsesFullTimeout(t *testing.T) {
	const timeout = 5 * time.Minute

	// A task context that finished close to its own deadline must not truncate the
	// script's budget: the script should still get its full configured timeout.
	near, cancelNear := context.WithTimeout(context.Background(), time.Second)
	defer cancelNear()
	scriptCtx, cancel := postTaskScriptContext(near, timeout)
	defer cancel()
	deadline, ok := scriptCtx.Deadline()
	require.True(t, ok)
	assert.Greater(t, time.Until(deadline), time.Minute, "script timeout truncated to task deadline")

	// An already-cancelled task context must not cancel the script either.
	cancelledCtx, cancelIt := context.WithCancel(context.Background())
	cancelIt()
	scriptCtx2, cancel2 := postTaskScriptContext(cancelledCtx, timeout)
	defer cancel2()
	assert.NoError(t, scriptCtx2.Err(), "script context inherited the cancelled task context")
}

func TestPostTaskScriptEnv(t *testing.T) {
	cfg, err := config.LoadDefault("")
	require.NoError(t, err)

	r := &Runner{
		cfg:  cfg,
		envs: map[string]string{"BASE": "1"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	taskCtx, err := structpb.NewStruct(map[string]any{
		"run_id":     "99",
		"repository": "acme/widget",
	})
	require.NoError(t, err)
	task := &runnerv1.Task{Id: 3, Context: taskCtx}
	reporter := report.NewReporter(ctx, cancel, nil, task, cfg)
	setReporterJobResult(t, reporter, runnerv1.Result_RESULT_FAILURE)

	env := r.postTaskScriptEnv(reporter, task, "/tmp/workspace")
	assert.Equal(t, "1", env["BASE"])
	assert.Equal(t, "3", env["GITEA_TASK_ID"])
	assert.Equal(t, "99", env["GITEA_RUN_ID"])
	assert.Equal(t, "acme/widget", env["GITEA_REPOSITORY"])
	assert.Equal(t, "/tmp/workspace", env["GITEA_WORKSPACE"])
	assert.Equal(t, "failure", env["GITEA_JOB_RESULT"])
}

func TestRunPostTaskScriptIntegration(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "out.txt")
	scriptPath := filepath.Join(dir, "post-task.sh")
	script := "#!/bin/sh\nprintf '%s %s %s' \"$GITEA_TASK_ID\" \"$GITEA_JOB_RESULT\" \"$CUSTOM\" > \"" + outFile + "\"\n"
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o700))

	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	cfg.Runner.PostTaskScript = scriptPath

	r := &Runner{
		cfg:  cfg,
		envs: map[string]string{"CUSTOM": "runner-env"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	taskCtx, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)
	task := &runnerv1.Task{Id: 11, Context: taskCtx}
	reporter := report.NewReporter(ctx, cancel, nil, task, cfg)
	setReporterJobResult(t, reporter, runnerv1.Result_RESULT_SUCCESS)

	r.runPostTaskScript(ctx, reporter, task, "/workspace/acme/repo")

	content, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Equal(t, "11 success runner-env", string(content))
}

func setReporterJobResult(t *testing.T, reporter *report.Reporter, result runnerv1.Result) {
	t.Helper()
	require.NoError(t, reporter.Fire(&log.Entry{
		Time:    time.Now(),
		Message: "job finished",
		Data: log.Fields{
			"stage":     "Post",
			"jobResult": metrics.ResultToStatusLabel(result),
		},
	}))
}
