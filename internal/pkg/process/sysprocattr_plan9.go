// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build plan9

package process

import "syscall"

// SysProcAttr returns the platform attributes used to start a process. Plan 9
// has no process-group tree-kill (see Killer), so we only request a new rfork
// note group here.
func SysProcAttr(cmdLine string, tty bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Rfork: syscall.RFNOTEG,
	}
}
