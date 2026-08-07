// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// processAlive reports whether pid refers to a still-running process.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259 // STILL_ACTIVE
	return code == stillActive
}

// TestKillerKillsTree verifies that a process assigned to the Job Object is
// terminated together with a child it spawns afterwards. This mirrors a step or
// post-task script that launches a child which spawns further processes, where
// cancelling must take down the whole tree, not just the direct child.
func TestKillerKillsTree(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	// Parent powershell spawns a detached, long-lived child powershell (writing
	// its PID to a file) and then sleeps. The child is launched AFTER the parent
	// has been assigned to the job, so it must be captured by the job too.
	script := fmt.Sprintf(
		`$c = Start-Process powershell -PassThru -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 600'; `+
			`Set-Content -LiteralPath %q -Value $c.Id; Start-Sleep -Seconds 600`, pidFile)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", script)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	killer, err := NewKiller(cmd.Process)
	require.NoError(t, err)
	defer killer.Close()

	// Wait for the child PID to be reported.
	var childPID int
	require.Eventually(t, func() bool {
		b, e := os.ReadFile(pidFile)
		if e != nil {
			return false
		}
		s := strings.TrimSpace(string(b))
		if s == "" {
			return false
		}
		childPID, _ = strconv.Atoi(s)
		return childPID > 0 && processAlive(childPID)
	}, 20*time.Second, 200*time.Millisecond, "child process should start")

	// Killing the job must terminate both the parent and the detached child.
	require.NoError(t, killer.Kill())

	require.Eventually(t, func() bool {
		return !processAlive(cmd.Process.Pid) && !processAlive(childPID)
	}, 20*time.Second, 200*time.Millisecond, "parent and child should both be terminated")
}
