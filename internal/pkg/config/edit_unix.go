// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !windows && !plan9

package config

import (
	"errors"
	"os"
	"syscall"
)

// preserveOwner keeps a config that root edits owned by the service user it was created for.
func preserveOwner(file string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	// A caller that may replace the file but not chown it is no worse off than before.
	if err := os.Chown(file, int(stat.Uid), int(stat.Gid)); err != nil && !errors.Is(err, os.ErrPermission) {
		return err
	}
	return nil
}
