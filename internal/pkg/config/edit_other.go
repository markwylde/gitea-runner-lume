// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build windows || plan9

package config

import "os"

// preserveOwner is a no-op where a new file inherits its ownership from the directory.
func preserveOwner(_ string, _ os.FileInfo) error {
	return nil
}
