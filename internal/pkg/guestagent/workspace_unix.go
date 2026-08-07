//go:build !windows

// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package guestagent

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func PrepareWorkspace(root string) error {
	if os.Geteuid() == 0 {
		return errors.New("guest agent must run as an unprivileged account")
	}
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	if err := validateOwnedDirectory(parent); err != nil {
		return err
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return err
	}
	if err := validateOwnedDirectory(root); err != nil {
		return err
	}
	for _, name := range []string{"work", "act", "tmp", "toolcache"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		if err := validateOwnedDirectory(path); err != nil {
			return err
		}
	}
	return nil
}

func validateOwnedDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || int(stat.Uid) != os.Geteuid() {
		return errors.New("guest workspace must be an owner-only directory owned by the agent account")
	}
	return nil
}
