// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !plan9

package lock

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestTryLock(t *testing.T) {
	runnerFile := filepath.Join(t.TempDir(), ".runner")

	release, err := TryLock(runnerFile)
	if err != nil {
		t.Fatalf("first TryLock failed: %v", err)
	}

	// A second lock on the same file must be refused while the first is held.
	if _, err := TryLock(runnerFile); !errors.Is(err, ErrLocked) {
		t.Fatalf("second TryLock: want ErrLocked, got %v", err)
	}

	if err := release(); err != nil {
		t.Fatalf("release failed: %v", err)
	}

	// After release the lock is available again.
	release2, err := TryLock(runnerFile)
	if err != nil {
		t.Fatalf("TryLock after release failed: %v", err)
	}
	if err := release2(); err != nil {
		t.Fatalf("second release failed: %v", err)
	}
}

// TestTryLockUncreatable ensures a lock file that cannot be created reports a
// non-ErrLocked error, so callers can tell "already locked by another process"
// apart from "couldn't lock" and degrade gracefully (e.g. a read-only mount).
func TestTryLockUncreatable(t *testing.T) {
	runnerFile := filepath.Join(t.TempDir(), "missing-dir", ".runner")

	_, err := TryLock(runnerFile)
	if err == nil {
		t.Fatal("TryLock on an uncreatable lock file: want error, got nil")
	}
	if errors.Is(err, ErrLocked) {
		t.Fatal("TryLock on an uncreatable lock file: want non-ErrLocked error, got ErrLocked")
	}
}
