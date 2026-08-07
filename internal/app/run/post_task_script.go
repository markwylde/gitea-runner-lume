// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/internal/pkg/config"
	"gitea.com/gitea/runner/internal/pkg/metrics"
	"gitea.com/gitea/runner/internal/pkg/process"
	"gitea.com/gitea/runner/internal/pkg/report"

	runnerv1 "gitea.dev/actions-proto-go/runner/v1"
	log "github.com/sirupsen/logrus"
)

func (r *Runner) runPostTaskScript(ctx context.Context, reporter *report.Reporter, task *runnerv1.Task, workdir string) {
	script := r.cfg.Runner.PostTaskScript
	if script == "" {
		return
	}

	timeout := r.cfg.Runner.PostTaskScriptTimeout
	if timeout <= 0 {
		timeout = config.DefaultPostTaskScriptTimeout
	}

	scriptCtx, cancel := postTaskScriptContext(ctx, timeout)
	defer cancel()

	env := r.postTaskScriptEnv(reporter, task, workdir)
	log.Infof("running post-task script %q for task %d", script, task.Id)

	cmd := exec.CommandContext(scriptCtx, script)
	cmd.Env = envListFromMap(env)
	cmd.SysProcAttr = process.SysProcAttr(script, false)

	stdout := postTaskScriptLogWriter("stdout")
	stderr := postTaskScriptLogWriter("stderr")
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// Kill the script's whole process tree on cancellation and bound the post-exit
	// I/O wait, so a backgrounded child inheriting cmd's stdout/stderr pipe can
	// never hang cmd.Wait() and the runner. See process.TreeKill.
	treeKill := process.NewTreeKill(cmd)

	if err := cmd.Start(); err != nil {
		log.Warnf("post-task script %q for task %d: %v", script, task.Id, err)
		return
	}
	if k, kerr := treeKill.Capture(cmd.Process); kerr != nil {
		log.Warnf("post-task script %q for task %d: process tree kill setup failed, falling back to single-process kill: %v", script, task.Id, kerr)
	} else {
		defer k.Close()
	}

	err := cmd.Wait()
	// Flush any trailing, not-yet-newline-terminated output now that the I/O
	// copiers have finished (cmd.Wait, bounded by WaitDelay above, guarantees it).
	common.FlushWriter(stdout)
	common.FlushWriter(stderr)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			log.Warnf("post-task script %q for task %d: %v", script, task.Id, err)
			return
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			log.Warnf("post-task script %q for task %d exited with code %d", script, task.Id, exitErr.ExitCode())
			return
		}
		log.Warnf("post-task script %q for task %d: %v", script, task.Id, err)
	}
}

func postTaskScriptContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	// Detach from the task context's deadline and cancellation: the task has
	// already finished by the time the post-task script runs, so the script must
	// get its full configured timeout. Inheriting the task deadline would silently
	// truncate that budget when the job completed close to its own timeout (and an
	// already-cancelled task context would skip the script entirely).
	// context.WithoutCancel keeps the context values while dropping the deadline.
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func (r *Runner) postTaskScriptEnv(reporter *report.Reporter, task *runnerv1.Task, workdir string) map[string]string {
	env := r.cloneEnvs()
	env["GITEA_TASK_ID"] = strconv.FormatInt(task.Id, 10)
	env["GITEA_WORKSPACE"] = workdir
	// GITEA_JOB_RESULT shares the runner's canonical result vocabulary
	// (success/failure/cancelled/skipped/unknown), the same strings the reporter
	// parses and the metrics labels use.
	env["GITEA_JOB_RESULT"] = metrics.ResultToStatusLabel(reporter.Result())
	if v := task.Context.Fields["run_id"].GetStringValue(); v != "" {
		env["GITEA_RUN_ID"] = v
	}
	if v := task.Context.Fields["repository"].GetStringValue(); v != "" {
		env["GITEA_REPOSITORY"] = v
	}
	return env
}

func envListFromMap(env map[string]string) []string {
	envList := make([]string, 0, len(env))
	for k, v := range env {
		envList = append(envList, fmt.Sprintf("%s=%s", k, v))
	}
	return envList
}

// postTaskScriptLogWriter returns an io.Writer that logs the script's output one
// line at a time, tagged with the stream name. It is passed as cmd.Stdout/Stderr
// (rather than a StdoutPipe) so that cmd.WaitDelay governs the copying goroutine:
// a backgrounded process holding the pipe open can never block cmd.Wait()
// indefinitely. Flush any trailing partial line with common.FlushWriter after
// cmd.Wait() returns.
func postTaskScriptLogWriter(stream string) io.Writer {
	return common.NewLineWriter(func(line string) bool {
		log.Infof("post-task script %s: %s", stream, strings.TrimRight(line, "\r\n"))
		return true
	})
}
