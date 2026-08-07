// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gitea.com/gitea/runner/internal/pkg/guestagent"

	"github.com/stretchr/testify/require"
)

func TestInitCreatesDefaultConfigurationAndKeysIdempotently(t *testing.T) {
	home := t.TempDir()
	bin := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "lume"), []byte("#!/bin/sh\nprintf '0.5.1\\n'\n"), 0o700))

	run := func() string {
		command := NewRootCommand(t.Context())
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs([]string{"init"})
		require.NoError(t, command.Execute())
		return output.String()
	}
	first := run()
	require.Contains(t, first, "created Lume configuration")
	require.Contains(t, first, "image create --profile xcode-16")

	dir := filepath.Join(home, ".config", "gitea-runner-lume")
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{
		{filepath.Join(dir, "config.yaml"), 0o600},
		{filepath.Join(dir, "host.key"), 0o600},
		{filepath.Join(dir, "host.key.pub"), 0o644},
		{filepath.Join(dir, "image-signing.key"), 0o600},
		{filepath.Join(dir, "image-signing.pub"), 0o644},
		{filepath.Join(dir, "images"), 0o700},
	} {
		info, err := os.Stat(item.path)
		require.NoError(t, err, item.path)
		require.Equal(t, item.mode, info.Mode().Perm(), item.path)
	}
	require.NotNil(t, mustLoadPrivateKey(t, filepath.Join(dir, "host.key")))

	privateBefore, err := os.ReadFile(filepath.Join(dir, "host.key"))
	require.NoError(t, err)
	second := run()
	require.Contains(t, second, "using existing configuration")
	privateAfter, err := os.ReadFile(filepath.Join(dir, "host.key"))
	require.NoError(t, err)
	require.Equal(t, privateBefore, privateAfter)
}

func TestInitRefusesIncompleteExistingKeyPair(t *testing.T) {
	home := t.TempDir()
	bin := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "lume"), []byte("#!/bin/sh\nprintf '0.5.1\\n'\n"), 0o700))
	dir := filepath.Join(home, ".config", "gitea-runner-lume")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "host.key"), []byte("partial"), 0o600))

	command := NewRootCommand(t.Context())
	command.SetArgs([]string{"init"})
	err := command.Execute()
	require.ErrorContains(t, err, "key pair is incomplete")
}

func TestInitRefusesInsecureExistingConfigurationDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	require.NoError(t, os.Mkdir(dir, 0o755))
	command := NewRootCommand(t.Context())
	command.SetArgs([]string{"--config", filepath.Join(dir, "config.yaml"), "init"})
	err := command.Execute()
	require.ErrorContains(t, err, "accessible only by its owner")
}

func mustLoadPrivateKey(t *testing.T, path string) []byte {
	t.Helper()
	key, err := guestagent.LoadEd25519PrivateKey(path)
	require.NoError(t, err)
	return key
}
