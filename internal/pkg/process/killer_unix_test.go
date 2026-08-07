// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !windows && !plan9

package process

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// processAlive reports whether pid refers to a still-running process. Signal 0
// performs error checking without delivering a signal: a nil error (or EPERM)
// means the process exists, ESRCH means it is gone.
//
// On Linux, zombie processes (state Z in /proc/<pid>/stat) appear alive to
// kill(0) but have already terminated — their corpse lingers until the parent
// calls wait(). In a Docker container the child may be reparented to a PID 1
// that does not reap promptly, so we treat zombies as not alive.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err != nil {
		return false
	}
	// On Linux /proc is available; check whether the process is a zombie.
	if b, readErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); readErr == nil {
		// Format: "pid (comm) state ..." — state follows the closing ')' of the
		// command name (which may itself contain spaces and parens).
		rest := string(b)
		if idx := strings.LastIndex(rest, ") "); idx >= 0 {
			fields := strings.Fields(rest[idx+2:])
			if len(fields) > 0 && fields[0] == "Z" {
				return false // zombie: terminated but not yet reaped
			}
		}
	}
	return true
}

// TestKillerKillsTree verifies that a process group captured by the killer is
// terminated together with a child the process spawns afterwards. This mirrors
// a step or post-task script that launches a child which spawns further
// processes, where cancelling must take down the whole tree, not just the
// direct child.
func TestKillerKillsTree(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	// Parent shell backgrounds a long-lived child (writing its PID to a file)
	// and then sleeps. With job control off (non-interactive sh) the backgrounded
	// child stays in the parent's process group, so the group kill must reach it.
	script := fmt.Sprintf(`sleep 600 & echo $! > %q; sleep 600`, pidFile)
	cmd := exec.Command("/bin/sh", "-c", script)
	// Launch as its own process-group leader, exactly like a real process does
	// (see SysProcAttr), so the killer's PGID == the process PID.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})

	killer, err := NewKiller(cmd.Process)
	require.NoError(t, err)
	defer killer.Close()

	// Wait for the backgrounded child PID to be reported.
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
	}, 20*time.Second, 100*time.Millisecond, "child process should start")

	// Killing the group must terminate both the parent and the backgrounded child.
	require.NoError(t, killer.Kill())
	// Reap the parent so it does not linger as a zombie (which would still report
	// as alive); SIGKILL makes Wait return promptly.
	_ = cmd.Wait()

	require.Eventually(t, func() bool {
		return !processAlive(childPID)
	}, 20*time.Second, 100*time.Millisecond, "backgrounded child should be terminated")
}
