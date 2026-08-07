// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package lock provides a cross-platform, non-blocking advisory file lock used
// to ensure a single runner process owns a given runner file.
package lock

import (
	"errors"
	"fmt"
)

// ErrLocked is returned by TryLock when another process already holds the lock.
var ErrLocked = errors.New("runner file is already locked by another process")

// TryLock takes a non-blocking exclusive advisory lock tied to runnerFile. The
// lock is placed on a sibling "<runnerFile>.lock" so it never interferes with
// in-place rewrites of the runner file itself.
//
// It returns a release function that drops the lock. The operating system also
// releases the lock automatically when the process exits, including on a hard
// kill, so a crashed runner never leaves a stale lock behind.
//
// If another process already holds the lock, it returns ErrLocked.
func TryLock(runnerFile string) (func() error, error) {
	path := runnerFile + ".lock"
	release, err := tryLock(path)
	if errors.Is(err, ErrLocked) {
		return nil, ErrLocked
	}
	if err != nil {
		return nil, fmt.Errorf("lock %q: %w", path, err)
	}
	return release, nil
}
