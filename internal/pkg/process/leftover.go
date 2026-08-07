// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package process

import (
	"path/filepath"
	"strings"
)

// pathWithin is the OS-agnostic core of dirContainsPath: dir and target must
// already be cleaned, and fold folds case.
func pathWithin(dir, target string, fold bool) bool {
	if dir == "" || target == "" || dir == "." || target == "." {
		return false
	}
	if fold {
		dir = strings.ToLower(dir)
		target = strings.ToLower(target)
	}
	if dir == target {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(dir, sep) {
		dir += sep
	}
	return strings.HasPrefix(target, dir)
}
