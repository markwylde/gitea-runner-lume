// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !windows && !plan9

package process

import (
	"errors"
	"os"
	"syscall"
)

// Killer terminates a started process together with its whole process group,
// which is the Unix counterpart of the Windows Job Object tree-kill.
//
// Background: a process (a step or a post-task script) often launches a process
// tree (a shell that starts a child which in turn spawns further background
// processes). The default exec.CommandContext cancellation only kills the
// direct child, so cancelling left the rest of the tree running. Because those
// orphans inherited the parent's stdout/stderr pipe, cmd.Wait() also blocked
// forever and the runner hung.
//
// Processes are started with Setpgid (or Setsid for the PTY path, see
// SysProcAttr), which makes the process the leader of a new process group whose
// ID equals its PID. Signalling the negative PID delivers to every process
// still in that group, so we can tear down the whole tree atomically on
// cancellation, which also closes the inherited pipe handles so cmd.Wait() can
// return.
type Killer struct {
	pgid int
}

// NewKiller captures the process group of p (an already-started process).
// Because the process is launched with Setpgid/Setsid, p is a group leader and
// its PGID equals its PID; children spawned afterwards stay in the same group
// unless they explicitly create their own.
func NewKiller(p *os.Process) (*Killer, error) {
	return &Killer{pgid: p.Pid}, nil
}

// Kill sends SIGKILL to the entire process group (the process and every
// descendant that stayed in the group). A missing group (ESRCH) means the
// processes already exited and is not treated as an error.
func (k *Killer) Kill() error {
	if k == nil || k.pgid <= 0 {
		return nil
	}
	if err := syscall.Kill(-k.pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// Close is a no-op on Unix; there is no job handle to release.
func (k *Killer) Close() error { return nil }
