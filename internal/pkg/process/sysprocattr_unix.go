// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !windows && !plan9

package process

import "syscall"

// SysProcAttr returns the platform attributes used to start a process so that a
// Killer can later tear down its whole process tree. On Unix the process becomes
// the leader of a new process group (or session, for the PTY path), so a
// signal to the negative PID reaches every descendant that stayed in the group.
func SysProcAttr(_ string, tty bool) *syscall.SysProcAttr {
	if tty {
		return &syscall.SysProcAttr{
			Setsid:  true,
			Setctty: true,
		}
	}
	return &syscall.SysProcAttr{
		Setpgid: true,
	}
}
