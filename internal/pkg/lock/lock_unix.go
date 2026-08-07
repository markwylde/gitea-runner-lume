// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !windows && !plan9

package lock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLock opens (creating if needed) the lock file and takes a non-blocking
// exclusive flock on it. The returned release closes the file, which drops the
// lock; the kernel also drops it when the process exits.
func tryLock(path string) (func() error, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return func() error {
		// Best-effort unlock; closing the fd releases the lock regardless.
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		return f.Close()
	}, nil
}
