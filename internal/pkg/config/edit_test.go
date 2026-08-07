// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const editFixture = `# A leading comment.
log:
  # The logging level.
  level: info

runner:
  capacity: 1
  envs:
    EXISTING: value
  timeout: 3h
  labels:
    - ubuntu-latest:docker://node:20
    - self-hosted
`

func writeEditFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(editFixture), 0o600))
	return path
}

func TestEditValues(t *testing.T) {
	tests := []struct {
		name   string
		edit   func(file string) error
		assert func(t *testing.T, cfg *Config, content string)
	}{
		{
			name: "set scalar",
			edit: func(file string) error { return SetValue(file, "runner.capacity", "4") },
			assert: func(t *testing.T, cfg *Config, _ string) {
				assert.Equal(t, 4, cfg.Runner.Capacity)
			},
		},
		{
			name: "set duration",
			edit: func(file string) error { return SetValue(file, "runner.timeout", "90m") },
			assert: func(t *testing.T, cfg *Config, content string) {
				assert.Equal(t, 90*time.Minute, cfg.Runner.Timeout)
				assert.Contains(t, content, "timeout: 1h30m0s")
			},
		},
		{
			name: "set a pointer field in a missing section",
			edit: func(file string) error {
				return SetValue(file, "container.network_create_options.enable_ipv4", "false")
			},
			assert: func(t *testing.T, cfg *Config, _ string) {
				require.NotNil(t, cfg.Container.NetworkCreateOptions.EnableIPv4)
				assert.False(t, *cfg.Container.NetworkCreateOptions.EnableIPv4)
			},
		},
		{
			name: "set map entry",
			edit: func(file string) error { return SetValue(file, "runner.envs.ADDED", "yes") },
			assert: func(t *testing.T, cfg *Config, _ string) {
				assert.Equal(t, map[string]string{"EXISTING": "value", "ADDED": "yes"}, cfg.Runner.Envs)
			},
		},
		{
			name: "set replaces a list",
			edit: func(file string) error { return SetValue(file, "runner.labels", "one", "two") },
			assert: func(t *testing.T, cfg *Config, _ string) {
				assert.Equal(t, []string{"one", "two"}, cfg.Runner.Labels)
			},
		},
		{
			name: "add appends to a list",
			edit: func(file string) error { return AddValue(file, "runner.labels", "ubuntu:docker://node:22") },
			assert: func(t *testing.T, cfg *Config, _ string) {
				assert.Equal(t, []string{"ubuntu-latest:docker://node:20", "self-hosted", "ubuntu:docker://node:22"}, cfg.Runner.Labels)
			},
		},
		{
			name: "remove drops a list entry",
			edit: func(file string) error { return RemoveValue(file, "runner.labels", "self-hosted") },
			assert: func(t *testing.T, cfg *Config, _ string) {
				assert.Equal(t, []string{"ubuntu-latest:docker://node:20"}, cfg.Runner.Labels)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := writeEditFixture(t)
			require.NoError(t, tt.edit(file))

			raw, err := os.ReadFile(file)
			require.NoError(t, err)
			content := string(raw)
			cfg, err := LoadDefault(file)
			require.NoError(t, err)

			tt.assert(t, cfg, content)

			assert.Contains(t, content, "# A leading comment.")
			assert.Contains(t, content, "  # The logging level.")
			assert.Contains(t, content, "\n\nrunner:")
		})
	}
}

func TestEditValuesRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		edit    func(file string) error
		wantErr string
	}{
		{
			name:    "unknown key",
			edit:    func(file string) error { return SetValue(file, "runner.labl", "x") },
			wantErr: `unknown config key "runner.labl"`,
		},
		{
			name:    "value is not a number",
			edit:    func(file string) error { return SetValue(file, "runner.capacity", "many") },
			wantErr: `"many" is not a valid int`,
		},
		{
			name:    "value is not a duration",
			edit:    func(file string) error { return SetValue(file, "runner.timeout", "soon") },
			wantErr: `"soon" is not a duration`,
		},
		{
			name:    "value is not a boolean",
			edit:    func(file string) error { return SetValue(file, "runner.insecure", "maybe") },
			wantErr: `"maybe" is not a boolean`,
		},
		{
			name:    "set needs a single value",
			edit:    func(file string) error { return SetValue(file, "runner.capacity", "1", "2") },
			wantErr: "takes exactly one value",
		},
		{
			name:    "set on a section",
			edit:    func(file string) error { return SetValue(file, "runner", "x") },
			wantErr: "is a section",
		},
		{
			name:    "add on a scalar",
			edit:    func(file string) error { return AddValue(file, "runner.capacity", "4") },
			wantErr: "is not a list",
		},
		{
			name:    "add a duplicate",
			edit:    func(file string) error { return AddValue(file, "runner.labels", "self-hosted") },
			wantErr: `already contains "self-hosted"`,
		},
		{
			name:    "remove a missing entry",
			edit:    func(file string) error { return RemoveValue(file, "runner.labels", "absent") },
			wantErr: `does not contain "absent"`,
		},
		{
			name:    "sub-key of a free-form map entry",
			edit:    func(file string) error { return SetValue(file, "runner.envs.A.B", "x") },
			wantErr: "has no sub-keys",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := writeEditFixture(t)
			err := tt.edit(file)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)

			content, err := os.ReadFile(file)
			require.NoError(t, err)
			assert.Equal(t, editFixture, string(content), "a rejected edit must leave the file untouched")
		})
	}
}

func TestGetValue(t *testing.T) {
	file := writeEditFixture(t)

	value, err := GetValue(file, "runner.capacity")
	require.NoError(t, err)
	assert.Equal(t, "1", value)

	value, err = GetValue(file, "runner.labels")
	require.NoError(t, err)
	assert.Equal(t, "ubuntu-latest:docker://node:20\nself-hosted", value)

	value, err = GetValue(file, "runner.envs")
	require.NoError(t, err)
	assert.Equal(t, "EXISTING=value", value)

	// A section has no single-line rendering.
	value, err = GetValue(file, "runner")
	require.NoError(t, err)
	assert.Contains(t, value, "labels:\n  - ubuntu-latest:docker://node:20")

	_, err = GetValue(file, "metrics.addr")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not set")
}

func TestEditValuesFileHandling(t *testing.T) {
	t.Run("reports a missing file", func(t *testing.T) {
		err := SetValue(filepath.Join(t.TempDir(), "absent.yaml"), "runner.capacity", "4")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not exist")
	})

	t.Run("writes through a symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "real.yaml")
		link := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(target, []byte(editFixture), 0o600))
		require.NoError(t, os.Symlink(target, link))

		require.NoError(t, SetValue(link, "runner.capacity", "4"))

		info, err := os.Lstat(link)
		require.NoError(t, err)
		assert.NotZero(t, info.Mode()&os.ModeSymlink, "the symlink must not be replaced by a regular file")

		content, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Contains(t, string(content), "capacity: 4")
	})

	t.Run("keeps CRLF line endings", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "config.yaml")
		require.NoError(t, os.WriteFile(file, []byte(strings.ReplaceAll(editFixture, "\n", "\r\n")), 0o600))

		require.NoError(t, SetValue(file, "runner.capacity", "4"))

		content, err := os.ReadFile(file)
		require.NoError(t, err)
		assert.Contains(t, string(content), "capacity: 4\r\n")
		assert.NotContains(t, strings.ReplaceAll(string(content), "\r\n", ""), "\n")
	})
}

// An edit has to give the file back unchanged around it, down to the indentation of
// a commented-out option, as that documentation is what the user reads and edits.
func TestEditValuesPreservesFileText(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		edit    func(file string) error
		added   string // the only text the edit may add
	}{
		{
			name:    "example config",
			content: Example,
			edit:    func(file string) error { return AddValue(file, "runner.labels", "ubuntu:docker://node:22") },
			added:   "  labels:\n    - ubuntu:docker://node:22\n",
		},
		{
			name:    "minimal config",
			content: []byte(Minimal),
			edit:    func(file string) error { return SetValue(file, "runner.capacity", "4") },
			added:   "runner:\n  capacity: 4\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(file, tc.content, 0o600))

			require.NoError(t, tc.edit(file))

			content, err := os.ReadFile(file)
			require.NoError(t, err)
			assert.Equal(t, string(tc.content), strings.Replace(string(content), tc.added, "", 1))
		})
	}
}
