// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"os/exec"
	"runtime"
	"testing"

	"gitea.com/gitea/runner/act/container"

	mobyclient "github.com/moby/moby/client"
)

// requireLinuxDocker skips on non-Linux hosts. Some integration workflows need Docker features
// that only a Linux daemon provides (host networking, host /proc bind mounts); Docker Desktop
// on macOS/Windows does not, so those tests can only run on Linux.
func requireLinuxDocker(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("skipping: requires a Linux Docker host")
	}
}

// requireDocker skips the test unless a reachable docker daemon is available.
// GetDockerClient succeeds even without a running daemon (its ping is best-effort),
// so the daemon has to be pinged explicitly here to decide whether to skip.
func requireDocker(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	cli, err := container.GetDockerClient(ctx)
	if err != nil {
		t.Skipf("skipping: docker client unavailable: %v", err)
	}
	defer cli.Close()
	if _, err := cli.Ping(ctx, mobyclient.PingOptions{}); err != nil {
		t.Skipf("skipping: docker daemon unreachable: %v", err)
	}
}

// requireHostTools skips the test unless every named executable is on PATH. Used by the
// self-hosted (host environment) suite, which runs steps directly on the host.
func requireHostTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("skipping: required host tool %q not found: %v", tool, err)
		}
	}
}
