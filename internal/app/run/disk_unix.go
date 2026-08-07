// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package run

import "golang.org/x/sys/unix"

func freeDiskBytes(path string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil //nolint:unconvert // Bavail/Bsize signedness differs by platform
}
