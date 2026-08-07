// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package run

import "fmt"

func freeDiskBytes(path string) (uint64, error) {
	return 0, fmt.Errorf("free disk space checks are not supported for %s", path)
}
