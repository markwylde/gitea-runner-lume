// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefault_RejectsExternalServerWithoutSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cache:
  enabled: true
  external_server: "http://cache.invalid/"
`), 0o600))

	_, err := LoadDefault(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "external_secret")
}

func TestLoadDefault_AcceptsExternalServerWithSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cache:
  enabled: true
  external_server: "http://cache.invalid/"
  external_secret: "shh"
`), 0o600))

	_, err := LoadDefault(path)
	require.NoError(t, err)
}

func TestLoadDefault_DefaultsWorkdirCleanupAge(t *testing.T) {
	cfg, err := LoadDefault("")
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, cfg.Runner.WorkdirCleanupAge)
	assert.Equal(t, 10*time.Minute, cfg.Runner.IdleCleanupInterval)
}

func TestLoadDefault_HealthChecksAreOptIn(t *testing.T) {
	cfg, err := LoadDefault("")
	require.NoError(t, err)
	assert.False(t, cfg.HealthCheck.Enabled)
	assert.Equal(t, int64(1024), cfg.HealthCheck.MinFreeDiskSpaceMB)
	assert.Empty(t, cfg.HealthCheck.Script)
	assert.Equal(t, 30*time.Second, cfg.HealthCheck.Interval)
	assert.Equal(t, 10*time.Second, cfg.HealthCheck.Timeout)
	assert.False(t, cfg.Metrics.Enabled)
}

func TestLoadDefault_ValidatesLumeProfilesAndLabels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runner:
  labels:
    - macos-arm64:lume://xcode-16
lume:
  enabled: true
  executable: /opt/homebrew/bin/lume
  storage: default
  storage_path: /var/lib/lume
  state_dir: /var/lib/gitea-runner-lume
  installation_id: 0123456789abcdef0123456789abcdef
  max_running_vms: 2
  supported_versions: ["lume 0.4.0"]
  ssh_identity_file: /var/lib/gitea-runner-lume/host.key
  known_hosts_file: /var/lib/gitea-runner-lume/known_hosts
  guest_public_key_file: /var/lib/gitea-runner-lume/guest.pub
  image_signing_public_key_file: /var/lib/gitea-runner-lume/image-signing.pub
  ssh_user: runner
  guest_binary: /usr/local/bin/gitea-runner-lume
  ssh_port: 22
  connect_timeout: 30s
  profiles:
    xcode-16:
      image: grl-xcode-16
      manifest: /var/lib/gitea-runner-lume/images/xcode-16.json
      cpu: 6
      memory_gb: 16
      disk_gb: 100
      boot_timeout: 5m
      cleanup_timeout: 2m
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.True(t, cfg.Lume.Enabled)
	assert.Equal(t, "grl-xcode-16", cfg.Lume.Profiles["xcode-16"].Image)
}

func TestLoadDefault_RejectsUnsafeLumeConfiguration(t *testing.T) {
	tests := map[string]string{
		"relative executable":  "executable: lume",
		"too much concurrency": "max_running_vms: 3",
		"host fallback label":  "labels:\n    - macos-arm64:host",
		"unknown profile":      "labels:\n    - macos-arm64:lume://missing",
		"duplicate label":      "labels:\n    - macos-arm64:lume://xcode-16\n    - macos-arm64:lume://xcode-16",
	}
	base := `
runner:
  labels:
    - macos-arm64:lume://xcode-16
lume:
  enabled: true
  executable: /opt/homebrew/bin/lume
  storage: default
  storage_path: /var/lib/lume
  state_dir: /var/lib/gitea-runner-lume
  installation_id: 0123456789abcdef0123456789abcdef
  max_running_vms: 2
  supported_versions: ["lume 0.4.0"]
  ssh_identity_file: /var/lib/gitea-runner-lume/host.key
  known_hosts_file: /var/lib/gitea-runner-lume/known_hosts
  guest_public_key_file: /var/lib/gitea-runner-lume/guest.pub
  image_signing_public_key_file: /var/lib/gitea-runner-lume/image-signing.pub
  ssh_user: runner
  guest_binary: /usr/local/bin/gitea-runner-lume
  ssh_port: 22
  connect_timeout: 30s
  profiles:
    xcode-16:
      image: grl-xcode-16
      manifest: /var/lib/gitea-runner-lume/images/xcode-16.json
      cpu: 6
      memory_gb: 16
      disk_gb: 100
      boot_timeout: 5m
      cleanup_timeout: 2m
`
	for name, replacement := range tests {
		t.Run(name, func(t *testing.T) {
			content := base
			switch name {
			case "relative executable":
				content = strings.Replace(content, "executable: /opt/homebrew/bin/lume", replacement, 1)
			case "too much concurrency":
				content = strings.Replace(content, "max_running_vms: 2", replacement, 1)
			default:
				content = strings.Replace(content, "labels:\n    - macos-arm64:lume://xcode-16", replacement, 1)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			_, err := LoadDefault(path)
			require.Error(t, err)
		})
	}
}

func TestLoadDefault_DiskAndReadinessSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
health_check:
  enabled: true
  min_free_disk_space_mb: 4096
  script: /usr/local/bin/runner-health
  interval: 15s
  timeout: 3s
metrics:
  readiness_grace: 45s
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.True(t, cfg.HealthCheck.Enabled)
	assert.Equal(t, int64(4096), cfg.HealthCheck.MinFreeDiskSpaceMB)
	assert.Equal(t, "/usr/local/bin/runner-health", cfg.HealthCheck.Script)
	assert.Equal(t, 15*time.Second, cfg.HealthCheck.Interval)
	assert.Equal(t, 3*time.Second, cfg.HealthCheck.Timeout)
	assert.Equal(t, 45*time.Second, cfg.Metrics.ReadinessGrace)
}

func TestLoadDefault_UsesConfiguredWorkdirCleanupAge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runner:
  workdir_cleanup_age: 2h30m
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, 150*time.Minute, cfg.Runner.WorkdirCleanupAge)
}

func TestLoadDefault_UsesConfiguredIdleCleanupInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runner:
  idle_cleanup_interval: 45m
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, 45*time.Minute, cfg.Runner.IdleCleanupInterval)
}

func TestLoadDefault_AllowsDisablingWorkdirCleanup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runner:
  workdir_cleanup_age: 0s
  idle_cleanup_interval: 0s
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, time.Duration(0), cfg.Runner.WorkdirCleanupAge)
	assert.Equal(t, time.Duration(0), cfg.Runner.IdleCleanupInterval)
}

func TestLoadDefault_AllowsNegativeWorkdirCleanupValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runner:
  workdir_cleanup_age: -1s
  idle_cleanup_interval: -1s
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, -1*time.Second, cfg.Runner.WorkdirCleanupAge)
	assert.Equal(t, -1*time.Second, cfg.Runner.IdleCleanupInterval)
}

func TestLoadDefault_LoadsPostTaskScript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runner:
  post_task_script: /usr/local/bin/post-task.sh
  post_task_script_timeout: 2m
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, "/usr/local/bin/post-task.sh", cfg.Runner.PostTaskScript)
	assert.Equal(t, 2*time.Minute, cfg.Runner.PostTaskScriptTimeout)
}

func TestLoadDefault_DefaultsPostTaskScriptTimeout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runner:
  post_task_script: /usr/local/bin/post-task.sh
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, cfg.Runner.PostTaskScriptTimeout)
}

func TestLoadDefault_LoadsJobHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
runner:
  hooks:
    job_started: /hooks/started.sh
    job_completed: /hooks/completed.sh
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, "/hooks/started.sh", cfg.Runner.Hooks.JobStarted)
	assert.Equal(t, "/hooks/completed.sh", cfg.Runner.Hooks.JobCompleted)
}

// TestLoadDefault_MalformedYAMLReturnsParseError pins the error surfaced for
// invalid YAML to the canonical "parse config file" message rather than the
// "for defaults metadata" variant — i.e. the main yaml.Unmarshal runs first.
func TestLoadDefault_MalformedYAMLReturnsParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("runner:\n  capacity: [unterminated\n"), 0o600))

	_, err := LoadDefault(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse config file")
	assert.NotContains(t, err.Error(), "defaults metadata")
}

func TestLoadDefault_WarnsOnUnknownKeysButStillLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("container:\n  volumes:\n    - /host:/ctr\n  privileged: true\n"), 0o600))

	hook := test.NewGlobal()
	defer hook.Reset()

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.True(t, cfg.Container.Privileged)
	require.Len(t, hook.Entries, 1)
	assert.Contains(t, hook.LastEntry().Message, "field volumes not found")
}

func TestContainerNetworkCreateOptions(t *testing.T) {
	// Verify that the enable_ipv4/enable_ipv6 YAML keys unmarshal into the *bool fields,
	// distinguishing an explicit true/false from an omitted key (nil). A nil here is
	// forwarded as-is to Docker, which applies its own default.
	loadOptions := func(t *testing.T, yaml string) ContainerNetworkCreateOptions {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))

		cfg, err := LoadDefault(path)
		require.NoError(t, err)
		return cfg.Container.NetworkCreateOptions
	}

	t.Run("enable_ipv6 true unmarshals to non-nil true", func(t *testing.T) {
		opts := loadOptions(t, "container:\n  network_create_options:\n    enable_ipv6: true\n")
		require.NotNil(t, opts.EnableIPv6)
		assert.True(t, *opts.EnableIPv6)
	})

	t.Run("enable_ipv6 false unmarshals to non-nil false", func(t *testing.T) {
		opts := loadOptions(t, "container:\n  network_create_options:\n    enable_ipv6: false\n")
		require.NotNil(t, opts.EnableIPv6)
		assert.False(t, *opts.EnableIPv6)
	})

	t.Run("enable_ipv4 false unmarshals to non-nil false", func(t *testing.T) {
		opts := loadOptions(t, "container:\n  network_create_options:\n    enable_ipv4: false\n")
		require.NotNil(t, opts.EnableIPv4)
		assert.False(t, *opts.EnableIPv4)
	})

	t.Run("omitted keys stay nil", func(t *testing.T) {
		opts := loadOptions(t, "container:\n  network_create_options:\n    enable_ipv4: true\n")
		require.NotNil(t, opts.EnableIPv4)
		assert.True(t, *opts.EnableIPv4)
		assert.Nil(t, opts.EnableIPv6, "an omitted enable_ipv6 must remain nil so Docker's default applies")
	})

	t.Run("omitted block leaves both nil", func(t *testing.T) {
		opts := loadOptions(t, "container:\n  network: \"\"\n")
		assert.Nil(t, opts.EnableIPv4)
		assert.Nil(t, opts.EnableIPv6)
	})
}

func TestLoadDefault_ReadsExternalSecretFromFile(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "cache.secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("  s3cr3t\n"), 0o600))

	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cache:
  enabled: true
  external_server: "http://cache.invalid/"
  external_secret_file: "`+secretPath+`"
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", cfg.Cache.ExternalSecret)
}

func TestLoadDefault_ReadsExternalSecretFromFileWhenCacheDisabled(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "cache.secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("s3cr3t"), 0o600))

	// the file has to be resolved even when cache is disabled
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cache:
  enabled: false
  external_secret_file: "`+secretPath+`"
`), 0o600))

	cfg, err := LoadDefault(path)
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", cfg.Cache.ExternalSecret)
}

func TestLoadDefault_RejectsBothExternalSecretAndFile(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "cache.secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("s3cr3t"), 0o600))

	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cache:
  enabled: true
  external_server: "http://cache.invalid/"
  external_secret: "inline"
  external_secret_file: "`+secretPath+`"
`), 0o600))

	_, err := LoadDefault(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both set")
}

func TestLoadDefault_RejectsMissingExternalSecretFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cache:
  enabled: true
  external_server: "http://cache.invalid/"
  external_secret_file: "`+filepath.Join(dir, "absent.secret")+`"
`), 0o600))

	_, err := LoadDefault(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read cache.external_secret_file")
}

func TestLoadDefault_RejectsEmptyExternalSecretFile(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "cache.secret")
	require.NoError(t, os.WriteFile(secretPath, []byte("\n  \n"), 0o600))

	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
cache:
  enabled: true
  external_server: "http://cache.invalid/"
  external_secret_file: "`+secretPath+`"
`), 0o600))

	_, err := LoadDefault(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "contains no secret")
}

// The shipped configs must parse, hold no key the config does not know, and leave
// every option at its default, as all of their values are commented out.
func TestLoadDefault_ShippedConfigsChangeNothing(t *testing.T) {
	hook := test.NewGlobal()
	defer hook.Reset()

	defaults, err := LoadDefault("")
	require.NoError(t, err)

	dir := t.TempDir()
	for name, content := range map[string][]byte{"config.example.yaml": Example, "minimal.yaml": []byte(Minimal)} {
		file := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(file, content, 0o600))

		cfg, err := LoadDefault(file)
		require.NoError(t, err, name)
		assert.Equal(t, defaults, cfg, name)
	}
	assert.Empty(t, hook.AllEntries())
}

func TestLoadDefaultRejectsAbandonedWebhookConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schemaVersion":1,"gitea":{},"webhook":{}}`), 0o600))
	_, err := LoadDefault(path)
	require.ErrorContains(t, err, "abandoned webhook-based")
}

func TestLoadDefaultRejectsUnknownLumeKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("lume:\n  enabled: false\n  webhook: forbidden\n"), 0o600))
	_, err := LoadDefault(path)
	require.ErrorContains(t, err, "unknown lume configuration key")
}
