// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gitea.com/gitea/runner/internal/pkg/labels"

	"github.com/joho/godotenv"
	log "github.com/sirupsen/logrus"
	"go.yaml.in/yaml/v4"
)

// DefaultPostTaskScriptTimeout is the fallback cap on how long the post-task
// script may run when post_task_script is set without an explicit timeout. It is
// applied both at config load (for a configured script) and at the point of use
// (so a programmatically built config still gets a sane bound).
const DefaultPostTaskScriptTimeout = 5 * time.Minute

// Minimal is the smallest config file that runs the runner: options it does not
// name keep their default, and it names none.
const Minimal = `# Minimal config file. Every option it does not set keeps its default.
# "gitea-runner-lume config generate" prints all options, "config set <key> <value>" sets one here.
`

// Log represents the configuration for logging.
type Log struct {
	Level string `yaml:"level"` // Level indicates the logging level.
}

// Runner represents the configuration for the runner.
type Runner struct {
	File                  string            `yaml:"file"`                     // File specifies the file path for the runner.
	Capacity              int               `yaml:"capacity"`                 // Capacity specifies the capacity of the runner.
	Envs                  map[string]string `yaml:"envs"`                     // Envs stores environment variables for the runner.
	EnvFile               string            `yaml:"env_file"`                 // EnvFile specifies the path to the file containing environment variables for the runner.
	Timeout               time.Duration     `yaml:"timeout"`                  // Timeout specifies the duration for runner timeout.
	ShutdownTimeout       time.Duration     `yaml:"shutdown_timeout"`         // ShutdownTimeout specifies the duration to wait for running jobs to complete during a shutdown of the runner.
	Insecure              bool              `yaml:"insecure"`                 // Insecure indicates whether the runner operates in an insecure mode.
	FetchTimeout          time.Duration     `yaml:"fetch_timeout"`            // FetchTimeout specifies the timeout duration for fetching resources.
	FetchInterval         time.Duration     `yaml:"fetch_interval"`           // FetchInterval specifies the interval duration for fetching resources.
	FetchIntervalMax      time.Duration     `yaml:"fetch_interval_max"`       // FetchIntervalMax specifies the maximum backoff interval when idle.
	WorkdirCleanupAge     time.Duration     `yaml:"workdir_cleanup_age"`      // WorkdirCleanupAge removes stale bind-workdir task directories and orphaned host-mode scratch dirs older than this duration during idle cleanup.
	IdleCleanupInterval   time.Duration     `yaml:"idle_cleanup_interval"`    // IdleCleanupInterval runs the idle cleanup (stale directories and orphaned docker networks) periodically while the runner is idle. Set to 0 to disable cleanup cadence.
	LogReportInterval     time.Duration     `yaml:"log_report_interval"`      // LogReportInterval specifies the base interval for periodic log flush.
	LogReportMaxLatency   time.Duration     `yaml:"log_report_max_latency"`   // LogReportMaxLatency specifies the max time a log row can wait before being sent.
	LogReportBatchSize    int               `yaml:"log_report_batch_size"`    // LogReportBatchSize triggers immediate log flush when buffer reaches this size.
	StateReportInterval   time.Duration     `yaml:"state_report_interval"`    // StateReportInterval specifies the interval for state reporting.
	ReportCloseTimeout    time.Duration     `yaml:"report_close_timeout"`     // ReportCloseTimeout caps each RPC attempt when flushing the final logs and task state at job completion, on a detached context so a server cancel can't block the acknowledgement.
	Labels                []string          `yaml:"labels"`                   // Labels specify the labels of the runner. Labels are declared on each startup
	GithubMirror          string            `yaml:"github_mirror"`            // GithubMirror defines what mirrors should be used when using github
	ActionShallowClone    *bool             `yaml:"action_shallow_clone"`     // ActionShallowClone fetches only the requested ref of an action repository at depth 1 instead of cloning every branch's full history. It is a pointer to distinguish between false and not set; if not set, it defaults to true.
	SetActEnv             *bool             `yaml:"set_act_env"`              // SetActEnv controls whether the ACT=true environment variable is injected into jobs. It is a pointer to distinguish between false and not set; if not set, it defaults to true. Set it to false so workflows gated on `if: ${{ !env.ACT }}` behave like on GitHub.
	AllocatePTY           bool              `yaml:"allocate_pty"`             // AllocatePTY allocates a pseudo-TTY for each step's process. Default is false, matching GitHub's actions/runner. Enable only for jobs that need an interactive terminal; tools like docker build emit redrawing progress frames into the captured log when a TTY is present. Applies to both host and docker backends.
	PostTaskScript        string            `yaml:"post_task_script"`         // PostTaskScript is the path to an executable script run on the host after each task's cleanup completes. Empty disables the hook. On Windows use .exe/.bat/.cmd; PowerShell (.ps1) is not supported yet as the configured path.
	PostTaskScriptTimeout time.Duration     `yaml:"post_task_script_timeout"` // PostTaskScriptTimeout caps how long the post-task script may run. Default is 5m when post_task_script is set.
	Hooks                 RunnerHooks       `yaml:"hooks"`                    // Hooks are scripts run inside the job environment around the job's steps.
}

// RunnerHooks represents the scripts run inside the job environment around the job's steps.
type RunnerHooks struct {
	JobStarted   string `yaml:"job_started"`   // JobStarted is the path of a script run before the job's first step. Falls back to ACTIONS_RUNNER_HOOK_JOB_STARTED; a failure fails the job.
	JobCompleted string `yaml:"job_completed"` // JobCompleted is the path of a script run after the job's last step, while the job environment is still up. Falls back to ACTIONS_RUNNER_HOOK_JOB_COMPLETED; a failure fails the job.
}

// Cache represents the configuration for caching.
type Cache struct {
	Enabled            *bool  `yaml:"enabled"`              // Enabled indicates whether caching is enabled. It is a pointer to distinguish between false and not set. If not set, it will be true.
	Dir                string `yaml:"dir"`                  // Dir specifies the directory path for caching.
	Host               string `yaml:"host"`                 // Host specifies the caching host.
	Port               uint16 `yaml:"port"`                 // Port specifies the caching port.
	ExternalServer     string `yaml:"external_server"`      // ExternalServer specifies the URL of external cache server
	ExternalSecret     string `yaml:"external_secret"`      // ExternalSecret is a shared secret between this runner and an external gitea-runner cache-server, enabling per-job ACTIONS_RUNTIME_TOKEN authentication and repo scoping over the network. Required whenever ExternalServer is set; ExternalSecretFile is the alternative way to provide it.
	ExternalSecretFile string `yaml:"external_secret_file"` // ExternalSecretFile is the path to a file holding the ExternalSecret value, so the secret can be mounted instead of stored in the config file. LoadDefault reads it into ExternalSecret; setting both is an error.
	OfflineMode        bool   `yaml:"offline_mode"`         // OfflineMode reuses a cached action without fetching from the remote; a moved tag or branch stays at the cached commit until the cache entry is removed.
	V2                 *bool  `yaml:"v2"`                   // V2 serves the actions cache service v2 API to jobs, used by actions/cache@v4.2 and later, and edits the action bundles that would otherwise refuse it. Unset means enabled.
}

// Container represents the configuration for the container.
type Container struct {
	Network              string                        `yaml:"network"`                // Network specifies the network for the container.
	NetworkCreateOptions ContainerNetworkCreateOptions `yaml:"network_create_options"` // Add options when the network need to be created by the runner
	NetworkMode          string                        `yaml:"network_mode"`           // Deprecated: use Network instead. Could be removed after Gitea 1.20
	Privileged           bool                          `yaml:"privileged"`             // Privileged indicates whether the container runs in privileged mode.
	Options              string                        `yaml:"options"`                // Options specifies additional options for the container.
	WorkdirParent        string                        `yaml:"workdir_parent"`         // WorkdirParent specifies the parent directory for the container's working directory.
	ValidVolumes         []string                      `yaml:"valid_volumes"`          // ValidVolumes specifies the volumes (including bind mounts) can be mounted to containers.
	DockerHost           string                        `yaml:"docker_host"`            // DockerHost specifies the Docker host. It overrides the value specified in environment variable DOCKER_HOST.
	ForcePull            bool                          `yaml:"force_pull"`             // Pull docker image(s) even if already present, except digest-pinned ones. A pull that fails while a local copy exists is a warning, not a job failure.
	ForceRebuild         bool                          `yaml:"force_rebuild"`          // Rebuild docker image(s) even if already present
	RequireDocker        bool                          `yaml:"require_docker"`         // Always require a reachable docker daemon, even if not required by runner
	DockerTimeout        time.Duration                 `yaml:"docker_timeout"`         // Timeout to wait for the docker daemon to be reachable, if docker is required by require_docker or runner
	BindWorkdir          bool                          `yaml:"bind_workdir"`           // BindWorkdir binds the workspace to the host filesystem instead of using Docker volumes. Required for DinD when jobs use docker compose with bind mounts.
	ServiceReadyTimeout  time.Duration                 `yaml:"service_ready_timeout"`  // ServiceReadyTimeout bounds how long a job waits for a service container that declares a healthcheck to report healthy. Negative disables waiting.
}

type ContainerNetworkCreateOptions struct {
	EnableIPv4 *bool `yaml:"enable_ipv4"` // Enable or disable IPv4 for the network (true for docker by default)
	EnableIPv6 *bool `yaml:"enable_ipv6"` // Enable or disable IPv6 for the network (false for docker by default)
}

// Host represents the configuration for the host.
type Host struct {
	WorkdirParent string `yaml:"workdir_parent"` // WorkdirParent specifies the parent directory for the host's working directory.
}

type Lume struct {
	Enabled                   bool                   `yaml:"enabled"`
	Executable                string                 `yaml:"executable"`
	Storage                   string                 `yaml:"storage"`
	StoragePath               string                 `yaml:"storage_path"`
	StateDir                  string                 `yaml:"state_dir"`
	InstallationID            string                 `yaml:"installation_id"`
	MaxRunningVMs             int                    `yaml:"max_running_vms"`
	SupportedVersions         []string               `yaml:"supported_versions"`
	Profiles                  map[string]LumeProfile `yaml:"profiles"`
	SSHIdentityFile           string                 `yaml:"ssh_identity_file"`
	KnownHostsFile            string                 `yaml:"known_hosts_file"`
	GuestPublicKeyFile        string                 `yaml:"guest_public_key_file"`
	ImageSigningPublicKeyFile string                 `yaml:"image_signing_public_key_file"`
	SSHUser                   string                 `yaml:"ssh_user"`
	GuestBinary               string                 `yaml:"guest_binary"`
	SSHPort                   uint16                 `yaml:"ssh_port"`
	ConnectTimeout            time.Duration          `yaml:"connect_timeout"`
}

type LumeProfile struct {
	Image          string        `yaml:"image"`
	Manifest       string        `yaml:"manifest"`
	CPU            int           `yaml:"cpu"`
	MemoryGB       int           `yaml:"memory_gb"`
	DiskGB         int           `yaml:"disk_gb"`
	BootTimeout    time.Duration `yaml:"boot_timeout"`
	CleanupTimeout time.Duration `yaml:"cleanup_timeout"`
}

// Metrics represents the configuration for the Prometheus metrics endpoint.
type Metrics struct {
	Enabled        bool          `yaml:"enabled"`         // Enabled indicates whether the metrics endpoint is exposed.
	Addr           string        `yaml:"addr"`            // Addr specifies the listen address for the metrics HTTP server (e.g., ":9101").
	ReadinessGrace time.Duration `yaml:"readiness_grace"` // ReadinessGrace permits transient polling errors before /readyz becomes unhealthy.
}

// HealthCheck represents local checks that control whether the runner accepts
// new tasks. The entire feature is opt-in through Enabled.
type HealthCheck struct {
	Enabled            bool          `yaml:"enabled"`                // Enabled activates local task-admission health checks.
	MinFreeDiskSpaceMB int64         `yaml:"min_free_disk_space_mb"` // MinFreeDiskSpaceMB is the minimum free space required on the work volume.
	Script             string        `yaml:"script"`                 // Script is an optional executable used as an additional health check.
	Interval           time.Duration `yaml:"interval"`               // Interval controls how long a script result is cached.
	Timeout            time.Duration `yaml:"timeout"`                // Timeout caps one health-check script invocation.
}

// Config represents the overall configuration.
type Config struct {
	Log         Log         `yaml:"log"`          // Log represents the configuration for logging.
	Runner      Runner      `yaml:"runner"`       // Runner represents the configuration for the runner.
	Cache       Cache       `yaml:"cache"`        // Cache represents the configuration for caching.
	Container   Container   `yaml:"container"`    // Container represents the configuration for the container.
	Host        Host        `yaml:"host"`         // Host represents the configuration for the host.
	Metrics     Metrics     `yaml:"metrics"`      // Metrics represents the configuration for the Prometheus metrics endpoint.
	HealthCheck HealthCheck `yaml:"health_check"` // HealthCheck controls opt-in local task-admission checks.
	Lume        Lume        `yaml:"lume"`         // Lume configures disposable macOS virtual-machine execution.
}

// LoadDefault returns the default configuration.
// If file is not empty, it will be used to load the configuration.
func LoadDefault(file string) (*Config, error) {
	cfg := &Config{}
	definedRunnerKeys := map[string]bool{}
	if file != "" {
		content, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("open config file %q: %w", file, err)
		}
		var legacy map[string]json.RawMessage
		if json.Unmarshal(content, &legacy) == nil && legacy["schemaVersion"] != nil && (legacy["webhook"] != nil || legacy["gitea"] != nil) {
			return nil, errors.New("this is an abandoned webhook-based gitea-runner-lume configuration; create an upstream-compatible YAML config and register with the normal Gitea runner token")
		}
		if err := rejectUnknownLumeKeys(content); err != nil {
			return nil, fmt.Errorf("parse config file %q: %w", file, err)
		}
		if err := yaml.Unmarshal(content, cfg); err != nil {
			return nil, fmt.Errorf("parse config file %q: %w", file, err)
		}
		warnUnknownKeys(file, content)
		definedRunnerKeys, err = definedRunnerConfigKeys(content)
		if err != nil {
			return nil, fmt.Errorf("parse config file %q for defaults metadata: %w", file, err)
		}
	}

	if cfg.Runner.EnvFile != "" {
		if stat, err := os.Stat(cfg.Runner.EnvFile); err == nil && !stat.IsDir() {
			envs, err := godotenv.Read(cfg.Runner.EnvFile)
			if err != nil {
				return nil, fmt.Errorf("read env file %q: %w", cfg.Runner.EnvFile, err)
			}
			if cfg.Runner.Envs == nil {
				cfg.Runner.Envs = map[string]string{}
			}
			maps.Copy(cfg.Runner.Envs, envs)
		}
	}

	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Runner.File == "" {
		cfg.Runner.File = ".runner"
	}
	if err := validateLume(cfg); err != nil {
		return nil, err
	}
	if cfg.Runner.Capacity <= 0 {
		cfg.Runner.Capacity = 1
	}
	if cfg.Runner.Timeout <= 0 {
		cfg.Runner.Timeout = 3 * time.Hour
	}
	if cfg.Runner.ActionShallowClone == nil {
		b := true
		cfg.Runner.ActionShallowClone = &b
	}
	if cfg.Runner.SetActEnv == nil {
		b := true
		cfg.Runner.SetActEnv = &b
	}
	if cfg.Cache.Enabled == nil {
		b := true
		cfg.Cache.Enabled = &b
	}
	// Resolved regardless of cache.enabled, because the `cache-server` command reads the secret from the same key without checking cache.enabled.
	if err := resolveCacheExternalSecret(cfg); err != nil {
		return nil, err
	}
	if *cfg.Cache.Enabled {
		if cfg.Cache.Dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("cache.dir is unset and the user home directory could not be determined: %w", err)
			}
			cfg.Cache.Dir = filepath.Join(home, ".cache", "actcache")
		}
		if cfg.Cache.ExternalServer != "" && cfg.Cache.ExternalSecret == "" {
			return nil, errors.New("cache.external_server is set but no shared secret is configured; set cache.external_secret (or cache.external_secret_file) to the same value used by the gitea-runner cache-server")
		}
	}
	if cfg.Container.WorkdirParent == "" {
		cfg.Container.WorkdirParent = "workspace"
	}
	if cfg.Host.WorkdirParent == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("host.workdir_parent is unset and the user home directory could not be determined: %w", err)
		}
		cfg.Host.WorkdirParent = filepath.Join(home, ".cache", "act")
	}
	if cfg.Runner.FetchTimeout <= 0 {
		cfg.Runner.FetchTimeout = 5 * time.Second
	}
	if cfg.Runner.FetchInterval <= 0 {
		cfg.Runner.FetchInterval = 2 * time.Second
	}
	if cfg.Runner.FetchIntervalMax <= 0 {
		cfg.Runner.FetchIntervalMax = 5 * time.Second
	}
	if cfg.Runner.WorkdirCleanupAge == 0 && !definedRunnerKeys["workdir_cleanup_age"] {
		cfg.Runner.WorkdirCleanupAge = 24 * time.Hour
	}
	if cfg.Runner.IdleCleanupInterval == 0 && !definedRunnerKeys["idle_cleanup_interval"] {
		cfg.Runner.IdleCleanupInterval = 10 * time.Minute
	}
	if cfg.Runner.LogReportInterval <= 0 {
		cfg.Runner.LogReportInterval = 5 * time.Second
	}
	if cfg.Runner.LogReportMaxLatency <= 0 {
		cfg.Runner.LogReportMaxLatency = 3 * time.Second
	}
	if cfg.Runner.LogReportBatchSize <= 0 {
		cfg.Runner.LogReportBatchSize = 100
	}
	if cfg.Runner.StateReportInterval <= 0 {
		cfg.Runner.StateReportInterval = 5 * time.Second
	}
	if cfg.Runner.ReportCloseTimeout <= 0 {
		cfg.Runner.ReportCloseTimeout = 10 * time.Second
	}
	if cfg.Runner.PostTaskScript != "" && cfg.Runner.PostTaskScriptTimeout <= 0 {
		cfg.Runner.PostTaskScriptTimeout = DefaultPostTaskScriptTimeout
	}
	if cfg.HealthCheck.MinFreeDiskSpaceMB <= 0 {
		cfg.HealthCheck.MinFreeDiskSpaceMB = 1024
	}
	if cfg.HealthCheck.Interval <= 0 {
		cfg.HealthCheck.Interval = 30 * time.Second
	}
	if cfg.HealthCheck.Timeout <= 0 {
		cfg.HealthCheck.Timeout = 10 * time.Second
	}
	if cfg.Metrics.Addr == "" {
		cfg.Metrics.Addr = "127.0.0.1:9101"
	}
	if cfg.Metrics.ReadinessGrace <= 0 {
		cfg.Metrics.ReadinessGrace = 30 * time.Second
	}

	// Validate and fix invalid config combinations to prevent confusing behavior.
	if cfg.Runner.FetchIntervalMax < cfg.Runner.FetchInterval {
		log.Warnf("fetch_interval_max (%v) is less than fetch_interval (%v), setting fetch_interval_max to fetch_interval",
			cfg.Runner.FetchIntervalMax, cfg.Runner.FetchInterval)
		cfg.Runner.FetchIntervalMax = cfg.Runner.FetchInterval
	}
	if cfg.Runner.LogReportMaxLatency >= cfg.Runner.LogReportInterval {
		log.Warnf("log_report_max_latency (%v) >= log_report_interval (%v), the max-latency timer will never fire before the periodic ticker; consider lowering log_report_max_latency",
			cfg.Runner.LogReportMaxLatency, cfg.Runner.LogReportInterval)
	}

	// although `container.network_mode` will be deprecated, but we have to be compatible with it for now.
	if cfg.Container.NetworkMode != "" && cfg.Container.Network == "" {
		log.Warn("You are trying to use deprecated configuration item of `container.network_mode`, please use `container.network` instead.")
		if cfg.Container.NetworkMode == "bridge" {
			// Previously, if the value of `container.network_mode` is `bridge`, we will create a new network for job.
			// But “bridge” is easily confused with the bridge network created by Docker by default.
			// So we set the value of `container.network` to empty string to make `runner` automatically create a new network for job.
			cfg.Container.Network = ""
		} else {
			cfg.Container.Network = cfg.Container.NetworkMode
		}
	}

	return cfg, nil
}

func rejectUnknownLumeKeys(content []byte) error {
	var root map[string]any
	if err := yaml.Unmarshal(content, &root); err != nil {
		return err
	}
	raw, exists := root["lume"]
	if !exists {
		return nil
	}
	if raw == nil {
		return nil
	}
	lumeMap, ok := raw.(map[string]any)
	if !ok {
		return errors.New("lume configuration must be a mapping")
	}
	allowed := map[string]bool{"enabled": true, "executable": true, "storage": true, "storage_path": true, "state_dir": true, "installation_id": true, "max_running_vms": true, "supported_versions": true, "profiles": true, "ssh_identity_file": true, "known_hosts_file": true, "guest_public_key_file": true, "image_signing_public_key_file": true, "ssh_user": true, "guest_binary": true, "ssh_port": true, "connect_timeout": true}
	for key := range lumeMap {
		if !allowed[key] {
			return fmt.Errorf("unknown lume configuration key %q", key)
		}
	}
	profiles, exists := lumeMap["profiles"]
	if !exists {
		return nil
	}
	profileMap, ok := profiles.(map[string]any)
	if !ok {
		return errors.New("lume.profiles must be a mapping")
	}
	profileAllowed := map[string]bool{"image": true, "manifest": true, "cpu": true, "memory_gb": true, "disk_gb": true, "boot_timeout": true, "cleanup_timeout": true}
	for name, rawProfile := range profileMap {
		fields, ok := rawProfile.(map[string]any)
		if !ok {
			return fmt.Errorf("lume profile %q must be a mapping", name)
		}
		for key := range fields {
			if !profileAllowed[key] {
				return fmt.Errorf("unknown key %q in lume profile %q", key, name)
			}
		}
	}
	return nil
}

var lumeNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func validateLume(cfg *Config) error {
	if !cfg.Lume.Enabled {
		return nil
	}
	if cfg.Lume.Executable == "" || !filepath.IsAbs(cfg.Lume.Executable) {
		return errors.New("lume.executable must be an absolute path when Lume execution is enabled")
	}
	if cfg.Lume.StateDir == "" || !filepath.IsAbs(cfg.Lume.StateDir) {
		return errors.New("lume.state_dir must be an absolute path when Lume execution is enabled")
	}
	for name, path := range map[string]string{
		"ssh_identity_file":             cfg.Lume.SSHIdentityFile,
		"known_hosts_file":              cfg.Lume.KnownHostsFile,
		"guest_public_key_file":         cfg.Lume.GuestPublicKeyFile,
		"image_signing_public_key_file": cfg.Lume.ImageSigningPublicKeyFile,
		"guest_binary":                  cfg.Lume.GuestBinary,
	} {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("lume.%s must be an absolute path", name)
		}
	}
	if !regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`).MatchString(cfg.Lume.SSHUser) || cfg.Lume.SSHPort == 0 || cfg.Lume.ConnectTimeout <= 0 {
		return errors.New("lume SSH user, port, or connect timeout is invalid")
	}
	if !lumeNamePattern.MatchString(cfg.Lume.Storage) {
		return errors.New("lume.storage is invalid")
	}
	if cfg.Lume.StoragePath == "" || !filepath.IsAbs(cfg.Lume.StoragePath) {
		return errors.New("lume.storage_path must be an absolute path")
	}
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(cfg.Lume.InstallationID) {
		return errors.New("lume.installation_id must be 32 lowercase hexadecimal characters")
	}
	if cfg.Lume.MaxRunningVMs < 1 || cfg.Lume.MaxRunningVMs > 2 {
		return errors.New("lume.max_running_vms must be between 1 and 2")
	}
	if len(cfg.Lume.SupportedVersions) == 0 {
		return errors.New("lume.supported_versions must list at least one tested exact version")
	}
	for _, version := range cfg.Lume.SupportedVersions {
		if version == "" || len(version) > 128 || strings.ContainsAny(version, "\r\n") {
			return errors.New("lume.supported_versions contains an invalid version")
		}
	}
	if cfg.Runner.Capacity > cfg.Lume.MaxRunningVMs {
		return errors.New("runner.capacity cannot exceed lume.max_running_vms")
	}
	if cfg.Runner.PostTaskScript != "" {
		return errors.New("runner.post_task_script is not supported with Lume because it executes on the controller")
	}
	if len(cfg.Lume.Profiles) == 0 {
		return errors.New("lume.profiles must contain at least one profile")
	}
	for name, profile := range cfg.Lume.Profiles {
		if !lumeNamePattern.MatchString(name) {
			return fmt.Errorf("lume profile %q has an invalid name", name)
		}
		if !lumeNamePattern.MatchString(profile.Image) {
			return fmt.Errorf("lume profile %q has an invalid image", name)
		}
		if profile.Manifest == "" || !filepath.IsAbs(profile.Manifest) {
			return fmt.Errorf("lume profile %q manifest must be an absolute path", name)
		}
		if profile.CPU < 1 || profile.CPU > 32 || profile.MemoryGB < 4 || profile.MemoryGB > 128 || profile.DiskGB < 20 || profile.DiskGB > 2048 {
			return fmt.Errorf("lume profile %q has resources outside supported bounds", name)
		}
		if profile.BootTimeout <= 0 || profile.CleanupTimeout <= 0 {
			return fmt.Errorf("lume profile %q must configure positive boot and cleanup timeouts", name)
		}
	}
	return cfg.ValidateLumeLabels(cfg.Runner.Labels)
}

func (cfg *Config) ValidateLumeLabels(rawLabels []string) error {
	if !cfg.Lume.Enabled {
		return nil
	}
	if len(rawLabels) == 0 {
		return errors.New("at least one lume:// runner label is required while Lume execution is enabled")
	}
	seenNames := make(map[string]struct{}, len(rawLabels))
	for _, raw := range rawLabels {
		label, err := labels.Parse(raw)
		if err != nil {
			return fmt.Errorf("invalid runner label %q: %w", raw, err)
		}
		if _, exists := seenNames[label.Name]; exists {
			return fmt.Errorf("runner label name %q is configured more than once", label.Name)
		}
		seenNames[label.Name] = struct{}{}
		profile, ok := label.LumeProfile()
		if !ok {
			return fmt.Errorf("runner label %q must use lume:// while Lume execution is enabled", raw)
		}
		if _, exists := cfg.Lume.Profiles[profile]; !exists {
			return fmt.Errorf("runner label %q references unknown Lume profile %q", raw, profile)
		}
	}
	return nil
}

// warnUnknownKeys reports keys the config does not define, which are otherwise ignored
// without a trace. It only warns, so a config carrying keys from another runner version
// still loads.
func warnUnknownKeys(file string, content []byte) {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)

	var typeErr *yaml.TypeError
	if err := decoder.Decode(&Config{}); errors.As(err, &typeErr) {
		for _, message := range typeErr.Errors {
			log.Warnf("config file %q: %s, it will be ignored", file, message)
		}
	}
}

func definedRunnerConfigKeys(content []byte) (map[string]bool, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, err
	}

	defined := map[string]bool{}
	if len(root.Content) == 0 {
		return defined, nil
	}

	doc := root.Content[0]
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key := doc.Content[i]
		value := doc.Content[i+1]
		if key.Value != "runner" || value.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(value.Content); j += 2 {
			defined[value.Content[j].Value] = true
		}
		break
	}

	return defined, nil
}

// resolveCacheExternalSecret loads cache.external_secret from the file named by cache.external_secret_file,
// so deployments can mount the secret instead of committing it to the config file.
func resolveCacheExternalSecret(cfg *Config) error {
	if cfg.Cache.ExternalSecretFile == "" {
		return nil
	}
	if cfg.Cache.ExternalSecret != "" {
		return errors.New("cache.external_secret and cache.external_secret_file are both set; configure only one of them")
	}
	content, err := os.ReadFile(cfg.Cache.ExternalSecretFile)
	if err != nil {
		return fmt.Errorf("read cache.external_secret_file %q: %w", cfg.Cache.ExternalSecretFile, err)
	}
	secret := strings.TrimSpace(string(content))
	if secret == "" {
		return fmt.Errorf("cache.external_secret_file %q contains no secret", cfg.Cache.ExternalSecretFile)
	}
	cfg.Cache.ExternalSecret = secret
	return nil
}
