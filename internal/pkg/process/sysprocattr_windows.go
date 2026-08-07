// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package process

import "syscall"

// SysProcAttr returns the platform attributes used to start a process so that a
// Killer can later tear down its whole process tree. On Windows the process is
// placed in a new process group; the descendant tree is reclaimed via the Job
// Object set up by NewKiller.
func SysProcAttr(cmdLine string, tty bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CmdLine: cmdLine, CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}
