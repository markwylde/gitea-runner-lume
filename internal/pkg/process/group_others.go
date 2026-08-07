// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !windows

package process

import "os"

// Group is a no-op outside Windows: process groups already reclaim a step's
// tree, and an orphan does not block workspace deletion the way an open Windows
// handle does.
type Group struct{}

// NewGroup returns a Group that owns nothing.
func NewGroup() (*Group, error) {
	return &Group{}, nil
}

// Assign does nothing.
func (g *Group) Assign(_ *os.Process) error {
	return nil
}

// Close does nothing.
func (g *Group) Close() error {
	return nil
}
