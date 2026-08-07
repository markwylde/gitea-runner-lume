// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build plan9

package process

import "os"

// Killer falls back to single-process termination on platforms without a
// process-group / Job Object tree-kill. The Job Object (Windows) and process
// group (Unix) based tree-kills live in killer_windows.go / killer_unix.go;
// here we just kill the direct child, matching the previous default behaviour.
type Killer struct {
	p *os.Process
}

func NewKiller(p *os.Process) (*Killer, error) {
	return &Killer{p: p}, nil
}

func (k *Killer) Kill() error {
	if k == nil || k.p == nil {
		return nil
	}
	return k.p.Kill()
}

func (k *Killer) Close() error { return nil }
