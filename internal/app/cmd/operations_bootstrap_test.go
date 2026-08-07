// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"gitea.com/gitea/runner/internal/pkg/config"

	"github.com/stretchr/testify/require"
)

func TestRequireGuestBootstrapExplainsMissingPrerequisites(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Lume.GuestPublicKeyFile = filepath.Join(dir, "guest.pub")
	cfg.Lume.KnownHostsFile = filepath.Join(dir, "known_hosts")

	err := requireGuestBootstrap(cfg)
	require.ErrorContains(t, err, "guest bootstrap incomplete")
	require.ErrorContains(t, err, "docs/lume-setup.md")

	require.NoError(t, os.WriteFile(cfg.Lume.GuestPublicKeyFile, []byte("key"), 0o644))
	err = requireGuestBootstrap(cfg)
	require.ErrorContains(t, err, "pinned SSH host key")
}
