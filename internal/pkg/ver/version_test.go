// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package ver

import "testing"

func TestVersion(t *testing.T) {
	// version defaults to "dev" and is overridden at build time via -ldflags
	if got := Version(); got != version {
		t.Errorf("Version() = %q, want %q", got, version)
	}
}
