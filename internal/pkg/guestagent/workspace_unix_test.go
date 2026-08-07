//go:build !windows

// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package guestagent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrepareWorkspaceCreatesRemoteEnvironmentDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "jobs", "worker")
	require.NoError(t, PrepareWorkspace(root))

	for _, name := range []string{"work", "act", "tmp", "toolcache"} {
		info, err := os.Stat(filepath.Join(root, name))
		require.NoError(t, err)
		require.True(t, info.IsDir())
		require.Zero(t, info.Mode().Perm()&0o077)
	}
}
