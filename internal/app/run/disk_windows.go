// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build windows

package run

import "golang.org/x/sys/windows"

func freeDiskBytes(path string) (uint64, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(pathPtr, &available, nil, nil); err != nil {
		return 0, err
	}
	return available, nil
}
