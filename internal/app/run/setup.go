// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"

	"gitea.com/gitea/runner/internal/pkg/report"
	"gitea.com/gitea/runner/internal/pkg/ver"

	runnerv1 "gitea.dev/actions-proto-go/runner/v1"
)

// osReleasePath describes the host distribution on Linux; absent elsewhere, where the platform
// falls back to the Go runtime alone. A var so tests can point it at a fixture.
var osReleasePath = "/etc/os-release"

// reportSetup opens the job log the way actions/runner opens its "Set up job" step. The action
// downloads and the closing job name are written later, as the job starts.
func (r *Runner) reportSetup(reporter *report.Reporter, task *runnerv1.Task) {
	for _, line := range r.setupLines(task) {
		reporter.Logf("%s", line)
	}
}

// setupLines names the runner, then reports what it was asked to run and the host it runs on, each
// in its own group.
func (r *Runner) setupLines(task *runnerv1.Task) []string {
	fields := task.Context.Fields
	lines := []string{
		fmt.Sprintf("%s(version:%s)", r.name, ver.Version()),
		"::group::Runner Information",
	}
	if names := r.labels.Names(); len(names) > 0 {
		lines = append(lines, "Runner labels: "+strings.Join(names, ", "))
	}
	lines = append(lines,
		// The task id correlates the job log with the runner's log and the server's task list.
		fmt.Sprintf("Task: %d", task.Id),
		"Job: "+fields["job"].GetStringValue(),
		"Repository: "+fields["repository"].GetStringValue(),
		"Triggered by event: "+fields["event_name"].GetStringValue(),
		"::endgroup::",
		"::group::Operating System",
	)
	lines = append(lines, osInfo()...)
	return append(lines, "::endgroup::")
}

// osInfo describes the host the runner executes on.
func osInfo() []string {
	lines := make([]string, 0, 2)
	if name := prettyOSName(); name != "" {
		lines = append(lines, name)
	}
	return append(lines, fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH))
}

// prettyOSName reads PRETTY_NAME (e.g. "Ubuntu 24.04.4 LTS") from os-release, or "" when absent.
func prettyOSName() string {
	data, err := os.ReadFile(osReleasePath)
	if err != nil {
		return ""
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || key != "PRETTY_NAME" {
			continue
		}
		// Values are shell-quoted, but the quotes are optional.
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
		return value
	}
	return ""
}
