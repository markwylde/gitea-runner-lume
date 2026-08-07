// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package process

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// startSleeperIn launches a long-running child whose cwd is dir but whose
// executable lives outside it — what the path scan cannot see.
func startSleeperIn(t *testing.T, dir string) *exec.Cmd {
	t.Helper()
	ping := filepath.Join(os.Getenv("SystemRoot"), "System32", "ping.exe")
	cmd := exec.Command(ping, "-n", "60", "127.0.0.1")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

// processCWD opens pid and reads its working directory. Test-only; the reaper
// shares one handle across read and kill via processCWDHandle.
func processCWD(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return processCWDHandle(h)
}

// awaitCWD waits until pid's PEB reports a readable cwd (not populated the
// instant the process starts) and returns it.
func awaitCWD(t *testing.T, pid int) string {
	t.Helper()
	var cwd string
	require.Eventually(t, func() bool {
		var err error
		cwd, err = processCWD(uint32(pid))
		return err == nil && cwd != ""
	}, 5*time.Second, 50*time.Millisecond, "process cwd should become readable")
	return cwd
}

func TestProcessCWDReadsWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	cmd := startSleeperIn(t, dir)
	cwd := awaitCWD(t, cmd.Process.Pid)
	require.True(t, dirContainsPath(dir, cwd), "cwd %q should be within %q", cwd, dir)
}

func TestKillProcessesWithCWDUnderTerminatesMatch(t *testing.T) {
	dir := t.TempDir()
	// Executable outside dir and arguments carry no workspace path, so only the
	// working directory links this process to dir.
	cmd := startSleeperIn(t, dir)
	awaitCWD(t, cmd.Process.Pid)

	killed, err := KillProcessesWithCWDUnder(context.Background(), []string{dir})
	require.NoError(t, err)
	require.GreaterOrEqual(t, killed, 1)

	require.Eventually(t, func() bool {
		return !processAlive(cmd.Process.Pid)
	}, 5*time.Second, 50*time.Millisecond, "matched process should exit")
}

func TestKillProcessesWithCWDUnderSparesUnrelated(t *testing.T) {
	workspace := t.TempDir()
	elsewhere := t.TempDir()
	cmd := startSleeperIn(t, elsewhere)
	awaitCWD(t, cmd.Process.Pid)

	_, err := KillProcessesWithCWDUnder(context.Background(), []string{workspace})
	require.NoError(t, err)
	require.True(t, processAlive(cmd.Process.Pid), "process outside owned dirs must be spared")
}
