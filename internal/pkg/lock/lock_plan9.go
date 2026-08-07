// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build plan9

package lock

// tryLock is a best-effort no-op on plan9, which lacks flock/LockFileEx.
func tryLock(_ string) (func() error, error) {
	return func() error { return nil }, nil
}
