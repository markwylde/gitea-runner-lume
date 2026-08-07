// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package run

import (
	"errors"
	"fmt"

	"gitea.com/gitea/runner/internal/pkg/config"
	"golang.org/x/sys/unix"
)

func CheckLumeResources(cfg *config.Config) error {
	if !cfg.Lume.Enabled {
		return nil
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(cfg.Lume.StoragePath, &stat); err != nil {
		return fmt.Errorf("inspect Lume storage free space: %w", err)
	}
	free := uint64(stat.Bavail) * uint64(stat.Bsize)
	maxDiskGB, maxMemoryGB := 0, 0
	for _, profile := range cfg.Lume.Profiles {
		if profile.DiskGB > maxDiskGB {
			maxDiskGB = profile.DiskGB
		}
		if profile.MemoryGB > maxMemoryGB {
			maxMemoryGB = profile.MemoryGB
		}
	}
	requiredDisk := uint64(maxDiskGB*cfg.Lume.MaxRunningVMs+10) * 1024 * 1024 * 1024
	if free < requiredDisk {
		return fmt.Errorf("Lume storage has %d bytes free; %d required", free, requiredDisk)
	}
	memory, err := physicalMemoryBytes()
	if err != nil {
		return err
	}
	requiredMemory := uint64(maxMemoryGB*cfg.Lume.MaxRunningVMs+4) * 1024 * 1024 * 1024
	if memory < requiredMemory {
		return errors.New("physical memory cannot satisfy configured concurrent Lume profiles plus controller reserve")
	}
	return nil
}
