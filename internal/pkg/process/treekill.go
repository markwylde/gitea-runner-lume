// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package process

import (
	"os"
	"os/exec"
	"sync/atomic"
	"time"
)

// treeKillWaitDelay bounds how long Wait lingers for the command's I/O pipes to
// drain after the process exits before force-closing them and returning. It also
// covers a command that backgrounds a process holding a pipe open after a clean
// exit.
const treeKillWaitDelay = 10 * time.Second

// TreeKill wires an exec.Cmd so that cancelling it tears down the command's
// whole process tree (see Killer) rather than only the direct child, and bounds
// the post-exit I/O wait so a leftover pipe writer can never hang cmd.Wait.
//
// Background: a command often launches a process tree (a shell that starts a
// child which spawns further background processes). The default
// exec.CommandContext cancellation only kills the direct child, leaving the rest
// of the tree running; and because the orphans inherit cmd's stdout/stderr pipe,
// cmd.Wait() would block forever, hanging the caller.
//
// Callers still set cmd.SysProcAttr (via SysProcAttr) themselves, because the
// value differs between the plain and PTY execution paths.
type TreeKill struct {
	killer atomic.Pointer[Killer]
}

// NewTreeKill sets cmd.Cancel and cmd.WaitDelay. Call it before cmd.Start, then
// call Capture once after a successful Start.
func NewTreeKill(cmd *exec.Cmd) *TreeKill {
	t := &TreeKill{}
	cmd.Cancel = func() error {
		if k := t.killer.Load(); k != nil {
			return k.Kill()
		}
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = treeKillWaitDelay
	return t
}

// Capture assigns the started process (and the descendants it spawns) to a
// Killer so cancellation can reach the whole tree — a Job Object on Windows
// (children spawned afterwards are auto-included) and the process group on Unix.
// Call it once after cmd.Start. On failure the command falls back to the default
// single-process kill and the returned error is for logging only; WaitDelay
// still bounds the wait. The returned Killer should be closed when the command
// finishes (Close is nil-safe).
func (t *TreeKill) Capture(p *os.Process) (*Killer, error) {
	k, err := NewKiller(p)
	if err != nil {
		return nil, err
	}
	t.killer.Store(k)
	return k, nil
}
