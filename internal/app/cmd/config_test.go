// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"gitea.com/gitea/runner/internal/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateLumeStarterDiscoversVersionAndParses(t *testing.T) {
	bin := t.TempDir()
	lumePath := filepath.Join(bin, "lume")
	require.NoError(t, os.WriteFile(lumePath, []byte("#!/bin/sh\nprintf 'lume 0.4.0\\n'\n"), 0o700))
	t.Setenv("PATH", bin)
	configDir := t.TempDir()
	content, err := generateLumeStarter("xcode-16", "grl-xcode-16", "home", filepath.Join(t.TempDir(), ".lume"), configDir)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, content, 0o600))
	cfg, err := config.LoadDefault(path)
	require.NoError(t, err)
	require.True(t, cfg.Lume.Enabled)
	require.Equal(t, []string{"lume 0.4.0"}, cfg.Lume.SupportedVersions)
	require.Equal(t, []string{"xcode-16:lume://xcode-16"}, cfg.Runner.Labels)
	require.Equal(t, filepath.Join(configDir, ".runner"), cfg.Runner.File)
}

func runConfigCmd(t *testing.T, configFile string, args ...string) (string, string, error) {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	cmd := loadConfigCmd(&configFile)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestConfigCmdGeneratePrintsTheExample(t *testing.T) {
	out, _, err := runConfigCmd(t, "", "generate")
	require.NoError(t, err)
	assert.Equal(t, string(config.Example), out)
}

func TestConfigCmdInitWritesTheMinimalConfig(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")

	out, _, err := runConfigCmd(t, file, "init")
	require.NoError(t, err)
	assert.Contains(t, out, file)
	content, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, config.Minimal, string(content))

	_, _, err = runConfigCmd(t, file, "init")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--force")

	_, _, err = runConfigCmd(t, file, "init", "--force")
	require.NoError(t, err)

	home := t.TempDir()
	t.Setenv("HOME", home)
	_, _, err = runConfigCmd(t, "", "init")
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(home, ".config", "gitea-runner-lume", "config.yaml"))
}

func TestConfigCmdInitCreatesMissingParentDirectories(t *testing.T) {
	file := filepath.Join(t.TempDir(), "nested", "configuration", "config.yaml")
	out, _, err := runConfigCmd(t, file, "init")
	require.NoError(t, err)
	assert.Contains(t, out, file)
	content, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, config.Minimal, string(content))
	info, err := os.Stat(filepath.Dir(file))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// The subcommands only wire arguments through, so one pass over all of them is enough.
func TestConfigCmdEditsTheFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(file, []byte("runner:\n  labels:\n    - self-hosted\n"), 0o600))

	_, _, err := runConfigCmd(t, file, "set", "container.options", "--cpus 2")
	require.NoError(t, err)
	_, _, err = runConfigCmd(t, file, "add", "runner.labels", "ubuntu:docker://node:22")
	require.NoError(t, err)
	_, _, err = runConfigCmd(t, file, "remove", "runner.labels", "self-hosted")
	require.NoError(t, err)

	out, _, err := runConfigCmd(t, file, "get", "runner.labels")
	require.NoError(t, err)
	assert.Equal(t, "ubuntu:docker://node:22\n", out)

	out, _, err = runConfigCmd(t, file, "get", "container.options")
	require.NoError(t, err)
	assert.Equal(t, "--cpus 2\n", out)
}

func TestConfigCmdResolvesTheConfigFile(t *testing.T) {
	t.Run("uses the home default instead of the working directory", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		file := filepath.Join(home, ".config", "gitea-runner-lume", "config.yaml")
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
		require.NoError(t, os.WriteFile(file, []byte("runner:\n  capacity: 3\n"), 0o600))
		workingDirectory := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(workingDirectory, "config.yaml"), []byte("runner:\n  capacity: 2\n"), 0o600))
		t.Chdir(workingDirectory)

		out, errOut, err := runConfigCmd(t, "", "get", "runner.capacity")
		require.NoError(t, err)
		assert.Equal(t, "3\n", out)
		assert.Empty(t, errOut)
	})

	t.Run("reports the missing home default", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)

		_, _, err := runConfigCmd(t, "", "set", "runner.capacity", "4")
		require.Error(t, err)
		assert.Contains(t, err.Error(), filepath.Join(home, ".config", "gitea-runner-lume", "config.yaml"))
		assert.Contains(t, err.Error(), "gitea-runner-lume init")
		assert.Contains(t, err.Error(), "--config")
	})
}

func TestRootCommandDefaultsConfigFlagToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	command := NewRootCommand(t.Context())
	flag := command.PersistentFlags().Lookup("config")
	require.NotNil(t, flag)
	assert.Equal(t, filepath.Join(home, ".config", "gitea-runner-lume", "config.yaml"), flag.DefValue)
}
