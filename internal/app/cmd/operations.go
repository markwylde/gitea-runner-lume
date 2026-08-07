// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"gitea.com/gitea/runner/internal/app/run"
	"gitea.com/gitea/runner/internal/pkg/client"
	"gitea.com/gitea/runner/internal/pkg/config"
	"gitea.com/gitea/runner/internal/pkg/guestagent"
	"gitea.com/gitea/runner/internal/pkg/guestproto"
	"gitea.com/gitea/runner/internal/pkg/lock"
	"gitea.com/gitea/runner/internal/pkg/lume"
	"gitea.com/gitea/runner/internal/pkg/ver"
	pingv1 "gitea.dev/actions-proto-go/ping/v1"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

func loadStatusCmd(ctx context.Context, configFile *string) *cobra.Command {
	var asJSON bool
	command := &cobra.Command{Use: "status", Short: "Show local runner and Lume worker state", Args: cobra.NoArgs}
	command.Flags().BoolVar(&asJSON, "json", false, "print bounded JSON")
	command.RunE = func(command *cobra.Command, _ []string) error {
		cfg, err := loadCommandConfig(command, configFile)
		if err != nil {
			return err
		}
		reg, regErr := config.LoadRegistration(cfg.Runner.File)
		store, err := lume.NewLeaseStore(cfg.Lume.StateDir + "/leases")
		if err != nil {
			return err
		}
		leases, err := store.List(cfg.Lume.InstallationID, cfg.Lume.Storage)
		if err != nil {
			return err
		}
		type worker struct {
			TaskID int64      `json:"task_id"`
			VMID   string     `json:"vm_id"`
			Phase  lume.Phase `json:"phase"`
		}
		out := struct {
			Registered     bool     `json:"registered"`
			GiteaReachable bool     `json:"gitea_reachable"`
			Runner         string   `json:"runner,omitempty"`
			Address        string   `json:"address,omitempty"`
			Labels         []string `json:"labels,omitempty"`
			Workers        []worker `json:"workers"`
		}{Workers: make([]worker, 0, len(leases))}
		if regErr == nil {
			out.Registered, out.Runner, out.Address, out.Labels = true, reg.Name, reg.Address, reg.Labels
			_, pingErr := client.New(reg.Address, cfg.Runner.Insecure, reg.UUID, reg.Token).Ping(ctx, connect.NewRequest(&pingv1.PingRequest{Data: "status"}))
			out.GiteaReachable = pingErr == nil
		} else if !os.IsNotExist(regErr) {
			return regErr
		}
		for _, lease := range leases {
			if lease.Phase != lume.PhaseComplete {
				out.Workers = append(out.Workers, worker{lease.TaskID, lease.Ownership.VMID, lease.Phase})
			}
		}
		if asJSON {
			return json.NewEncoder(command.OutOrStdout()).Encode(out)
		}
		fmt.Fprintf(command.OutOrStdout(), "registered: %t\n", out.Registered)
		fmt.Fprintf(command.OutOrStdout(), "gitea reachable: %t\n", out.GiteaReachable)
		if out.Registered {
			fmt.Fprintf(command.OutOrStdout(), "runner: %s\ninstance: %s\nlabels: %v\n", out.Runner, out.Address, out.Labels)
		}
		fmt.Fprintf(command.OutOrStdout(), "active workers: %d\n", len(out.Workers))
		for _, item := range out.Workers {
			fmt.Fprintf(command.OutOrStdout(), "task %d: %s (%s)\n", item.TaskID, item.VMID, item.Phase)
		}
		return nil
	}
	return command
}

func loadDoctorCmd(ctx context.Context, configFile *string) *cobra.Command {
	var liveGuestProfile string
	command := &cobra.Command{Use: "doctor", Short: "Validate runner, Lume, image, and recovery prerequisites", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		cfg, err := loadCommandConfig(command, configFile)
		if err != nil {
			return err
		}
		checks := 0
		check := func(name string, err error) error {
			checks++
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			fmt.Fprintf(command.OutOrStdout(), "ok: %s\n", name)
			return nil
		}
		if err := check("Apple Silicon host", func() error {
			if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
				return errors.New("requires darwin/arm64")
			}
			return nil
		}()); err != nil {
			return err
		}
		reg, regErr := config.LoadRegistration(cfg.Runner.File)
		if err := check("registration", regErr); err != nil {
			return err
		}
		_, pingErr := client.New(reg.Address, cfg.Runner.Insecure, reg.UUID, reg.Token).Ping(ctx, connect.NewRequest(&pingv1.PingRequest{Data: "doctor"}))
		if err := check("Gitea runner protocol", pingErr); err != nil {
			return err
		}
		provider, err := lume.NewProvider(cfg.Lume.Executable, cfg.Lume.Storage, time.Minute, nil)
		if err := check("Lume configuration", err); err != nil {
			return err
		}
		version, err := provider.Version(ctx)
		if err := check("Lume executable", err); err != nil {
			return err
		}
		supported := false
		for _, allowed := range cfg.Lume.SupportedVersions {
			if version == allowed {
				supported = true
				break
			}
		}
		if err := check("Lume supported version", func() error {
			if !supported {
				return fmt.Errorf("unsupported Lume version %q", version)
			}
			return nil
		}()); err != nil {
			return err
		}
		if err := check("Lume host resources", run.CheckLumeResources(cfg)); err != nil {
			return err
		}
		guestKey, err := guestagent.LoadEd25519PublicKey(cfg.Lume.GuestPublicKeyFile)
		if err := check("guest public key", err); err != nil {
			return err
		}
		signingKey, err := guestagent.LoadEd25519PublicKey(cfg.Lume.ImageSigningPublicKeyFile)
		if err := check("image signing public key", err); err != nil {
			return err
		}
		for name, profile := range cfg.Lume.Profiles {
			_, err := lume.LoadImageManifest(profile.Manifest, signingKey, guestKey, profile.Image, cfg.Lume.Storage, ver.Version())
			if err := check("image "+name, err); err != nil {
				return err
			}
		}
		store, err := lume.NewLeaseStore(cfg.Lume.StateDir + "/leases")
		if err == nil {
			_, err = store.List(cfg.Lume.InstallationID, cfg.Lume.Storage)
		}
		if err := check("durable leases", err); err != nil {
			return err
		}
		configPath, err := resolveConfigFile(command, configFile)
		if err == nil {
			err = validateInstalledService(configPath, liveGuestProfile == "")
		}
		if err := check("installed service", err); err != nil {
			return err
		}
		if liveGuestProfile != "" {
			if err := requireServiceStopped(); err != nil {
				return err
			}
			release, err := lock.TryLock(cfg.Runner.File)
			if errors.Is(err, lock.ErrLocked) {
				return errors.New("live guest probe requires every runner daemon using this registration to be stopped")
			}
			if err != nil {
				return fmt.Errorf("lock runner registration for live guest probe: %w", err)
			}
			defer func() { _ = release() }()
			if _, ok := cfg.Lume.Profiles[liveGuestProfile]; !ok {
				return fmt.Errorf("live guest profile %q is not configured", liveGuestProfile)
			}
			if err := check("live guest transport "+liveGuestProfile, run.ProbeLumeGuest(ctx, cfg, liveGuestProfile)); err != nil {
				return err
			}
		}
		fmt.Fprintf(command.OutOrStdout(), "doctor completed: %d checks passed\n", checks)
		return nil
	}}
	command.Flags().StringVar(&liveGuestProfile, "live-guest-profile", "", "clone, boot, authenticate, and delete a disposable worker for this profile")
	return command
}

func loadCleanupCmd(ctx context.Context, configFile *string) *cobra.Command {
	var apply bool
	command := &cobra.Command{Use: "cleanup", Short: "Plan or apply ownership-checked worker cleanup", Args: cobra.NoArgs}
	command.Flags().BoolVar(&apply, "apply", false, "stop and delete workers proven owned by this installation")
	command.RunE = func(command *cobra.Command, _ []string) error {
		cfg, err := loadCommandConfig(command, configFile)
		if err != nil {
			return err
		}
		store, err := lume.NewLeaseStore(cfg.Lume.StateDir + "/leases")
		if err != nil {
			return err
		}
		leases, err := store.List(cfg.Lume.InstallationID, cfg.Lume.Storage)
		if err != nil {
			return err
		}
		pending := 0
		for _, lease := range leases {
			if lease.Phase != lume.PhaseComplete {
				pending++
				fmt.Fprintf(command.OutOrStdout(), "%s task=%d vm=%s phase=%s\n", map[bool]string{true: "apply", false: "would-clean"}[apply], lease.TaskID, lease.Ownership.VMID, lease.Phase)
			}
		}
		if !apply {
			fmt.Fprintf(command.OutOrStdout(), "plan: %d worker(s); pass --apply to execute\n", pending)
			return nil
		}
		results, err := run.ReconcileLume(ctx, cfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(command.OutOrStdout(), "cleanup complete: %d lease(s) reconciled\n", len(results))
		return nil
	}
	return command
}

func loadImageCmd(ctx context.Context, configFile *string) *cobra.Command {
	imageCmd := &cobra.Command{Use: "image", Short: "Manage trusted Lume base images", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	var createProfile, ipsw, unattended string
	create := &cobra.Command{Use: "create", Short: "Create an unattended macOS base VM with Lume", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		cfg, err := loadCommandConfig(command, configFile)
		if err != nil {
			return err
		}
		profile, ok := cfg.Lume.Profiles[createProfile]
		if !ok {
			return fmt.Errorf("unknown Lume profile %q", createProfile)
		}
		provider, err := lume.NewProvider(cfg.Lume.Executable, cfg.Lume.Storage, cfg.Runner.Timeout, nil)
		if err != nil {
			return err
		}
		createDone := make(chan struct{})
		go reportImageCreateProgress(ctx, command.ErrOrStderr(), provider, profile.Image, createDone)
		if err := provider.Create(ctx, profile.Image, ipsw, unattended, profile.CPU, profile.MemoryGB, profile.DiskGB); err != nil {
			close(createDone)
			return err
		}
		close(createDone)
		fmt.Fprintf(command.OutOrStdout(), "created stopped base VM %s; install and harden the guest agent, then run image adopt\n", profile.Image)
		return nil
	}}
	create.Flags().StringVar(&createProfile, "profile", "", "configured profile to create")
	create.Flags().StringVar(&ipsw, "ipsw", "latest", "absolute IPSW path or latest")
	create.Flags().StringVar(&unattended, "unattended", "tahoe", "Lume unattended preset or YAML path")
	_ = create.MarkFlagRequired("profile")
	imageCmd.AddCommand(create)
	var profileName string
	validate := &cobra.Command{Use: "validate", Short: "Verify configured signed image manifests and stopped VMs", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		cfg, err := loadCommandConfig(command, configFile)
		if err != nil {
			return err
		}
		return validateImages(ctx, command, cfg, profileName)
	}}
	validate.Flags().StringVar(&profileName, "profile", "", "validate only this profile")
	imageCmd.AddCommand(validate)
	var adoptProfile, signingKeyFile string
	adopt := &cobra.Command{Use: "adopt", Short: "Sign a validated stopped Lume VM as a base image", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		cfg, err := loadCommandConfig(command, configFile)
		if err != nil {
			return err
		}
		profile, ok := cfg.Lume.Profiles[adoptProfile]
		if !ok {
			return fmt.Errorf("unknown Lume profile %q", adoptProfile)
		}
		if err := requireGuestBootstrap(cfg); err != nil {
			return err
		}
		provider, err := lume.NewProvider(cfg.Lume.Executable, cfg.Lume.Storage, profile.CleanupTimeout, nil)
		if err != nil {
			return err
		}
		vm, err := provider.Get(ctx, profile.Image)
		if err != nil {
			return err
		}
		if vm.State != lume.StateStopped || vm.Storage != cfg.Lume.Storage {
			return errors.New("base image must be stopped in configured storage")
		}
		attestation, err := attestBaseImage(ctx, cfg, profile, guestproto.Hello{})
		if err != nil {
			return fmt.Errorf("attest base image: %w", err)
		}
		privateKey, err := guestagent.LoadEd25519PrivateKey(signingKeyFile)
		if err != nil {
			return err
		}
		configuredSigningKey, err := guestagent.LoadEd25519PublicKey(cfg.Lume.ImageSigningPublicKeyFile)
		if err != nil {
			return err
		}
		if !ed25519.PublicKey(privateKey.Public().(ed25519.PublicKey)).Equal(configuredSigningKey) {
			return errors.New("signing private key does not match configured image signing public key")
		}
		guestKey, err := guestagent.LoadEd25519PublicKey(cfg.Lume.GuestPublicKeyFile)
		if err != nil {
			return err
		}
		generationBytes := make([]byte, 16)
		if _, err := rand.Read(generationBytes); err != nil {
			return err
		}
		fingerprint := sha256.Sum256(guestKey)
		manifest, err := lume.SignImageManifest(lume.ImageManifestPayload{
			SchemaVersion: 2, Image: profile.Image, Storage: cfg.Lume.Storage,
			Generation: hex.EncodeToString(generationBytes), GuestRevision: ver.Version(),
			GuestPublicKeyFingerprint: hex.EncodeToString(fingerprint[:]),
			GuestOS:                   attestation.OS, GuestArchitecture: attestation.Architecture,
			GuestOSVersion: attestation.OSVersion, GuestAgentSHA256: attestation.AgentSHA256,
			GuestUID: attestation.UID, ValidatedAt: time.Now().UTC(),
		}, privateKey)
		if err != nil {
			return err
		}
		if err := lume.WriteImageManifest(profile.Manifest, manifest); err != nil {
			return err
		}
		fmt.Fprintf(command.OutOrStdout(), "adopted image %s for profile %s\n", profile.Image, adoptProfile)
		return nil
	}}
	adopt.Flags().StringVar(&adoptProfile, "profile", "", "configured profile to adopt")
	adopt.Flags().StringVar(&signingKeyFile, "signing-key-file", "", "owner-only Ed25519 image signing private key")
	_ = adopt.MarkFlagRequired("profile")
	_ = adopt.MarkFlagRequired("signing-key-file")
	imageCmd.AddCommand(adopt)
	return imageCmd
}

type imageProgressProvider interface {
	Get(context.Context, string) (lume.VM, error)
}

func reportImageCreateProgress(ctx context.Context, output io.Writer, provider imageProgressProvider, image string, done <-chan struct{}) {
	started := time.Now()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	fmt.Fprintf(output, "creating base VM %s; macOS installation can take several minutes\n", image)
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			elapsed := time.Since(started).Round(time.Second)
			pollContext, cancel := context.WithTimeout(ctx, 5*time.Second)
			vm, err := provider.Get(pollContext, image)
			cancel()
			fmt.Fprintln(output, formatImageCreateProgress(image, elapsed, vm, err))
		}
	}
}

func formatImageCreateProgress(image string, elapsed time.Duration, vm lume.VM, pollErr error) string {
	prefix := fmt.Sprintf("creating base VM %s", image)
	if pollErr != nil {
		return fmt.Sprintf("%s (%s elapsed; waiting for Lume progress)", prefix, elapsed)
	}
	if vm.DownloadProgress != nil {
		return fmt.Sprintf("%s: downloading %.0f%% (%s elapsed)", prefix, *vm.DownloadProgress, elapsed)
	}
	if vm.ProvisioningOperation != "" {
		return fmt.Sprintf("%s: %s (%s elapsed)", prefix, vm.ProvisioningOperation, elapsed)
	}
	return fmt.Sprintf("%s: %s (%s elapsed)", prefix, vm.State, elapsed)
}

func attestBaseImage(ctx context.Context, cfg *config.Config, profile config.LumeProfile, expectedAttestation guestproto.Hello) (guestproto.Hello, error) {
	provider, err := lume.NewProvider(cfg.Lume.Executable, cfg.Lume.Storage, profile.CleanupTimeout, nil)
	if err != nil {
		return guestproto.Hello{}, err
	}
	process, err := provider.StartManaged(ctx, profile.Image)
	if err != nil {
		return guestproto.Hello{}, err
	}
	defer func() {
		_ = provider.Stop(context.Background(), profile.Image, false)
		_ = process.Kill()
		_ = process.Wait()
	}()
	deadline := time.Now().Add(profile.BootTimeout)
	var vm lume.VM
	for time.Now().Before(deadline) {
		vm, err = provider.Get(ctx, profile.Image)
		if err == nil && vm.State == lume.StateRunning && vm.IPAddress != "" {
			break
		}
		select {
		case <-ctx.Done():
			return guestproto.Hello{}, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if vm.State != lume.StateRunning || vm.IPAddress == "" {
		return guestproto.Hello{}, errors.New("base image did not become network-ready")
	}
	hostPrivateKey, err := guestagent.LoadEd25519PrivateKey(cfg.Lume.SSHIdentityFile)
	if err != nil {
		return guestproto.Hello{}, err
	}
	guestPublicKey, err := guestagent.LoadEd25519PublicKey(cfg.Lume.GuestPublicKeyFile)
	if err != nil {
		return guestproto.Hello{}, err
	}
	ids := make([]byte, 80)
	if _, err := rand.Read(ids); err != nil {
		return guestproto.Hello{}, err
	}
	hello := guestproto.Hello{InstallationID: cfg.Lume.InstallationID, LeaseID: hex.EncodeToString(ids[:16]), WorkerID: hex.EncodeToString(ids[16:32]), TaskID: 1, Nonce: hex.EncodeToString(ids[32:64]), Revision: ver.Version()}
	hello.OS, hello.Architecture, hello.OSVersion = expectedAttestation.OS, expectedAttestation.Architecture, expectedAttestation.OSVersion
	hello.AgentSHA256, hello.UID = expectedAttestation.AgentSHA256, expectedAttestation.UID
	root := "/private/var/tmp/gitea-runner-lume/" + hello.WorkerID
	session, err := guestagent.StartSSHSession(ctx, guestagent.SSHConfig{Executable: "/usr/bin/ssh", IdentityFile: cfg.Lume.SSHIdentityFile, KnownHostsFile: cfg.Lume.KnownHostsFile, HostKeyAlias: profile.Image, User: cfg.Lume.SSHUser, Address: vm.IPAddress, Port: cfg.Lume.SSHPort, ConnectTimeout: cfg.Lume.ConnectTimeout, GuestBinary: cfg.Lume.GuestBinary, GuestRoot: root}, hello, hostPrivateKey, guestPublicKey)
	if err != nil {
		return guestproto.Hello{}, err
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), profile.CleanupTimeout)
	defer cancel()
	attestation := session.Client.PeerHello()
	if err := session.Close(closeCtx); err != nil {
		return guestproto.Hello{}, err
	}
	return attestation, nil
}

func validateImages(ctx context.Context, command *cobra.Command, cfg *config.Config, selected string) error {
	if err := requireGuestBootstrap(cfg); err != nil {
		return err
	}
	guestKey, err := guestagent.LoadEd25519PublicKey(cfg.Lume.GuestPublicKeyFile)
	if err != nil {
		return err
	}
	signingKey, err := guestagent.LoadEd25519PublicKey(cfg.Lume.ImageSigningPublicKeyFile)
	if err != nil {
		return err
	}
	provider, err := lume.NewProvider(cfg.Lume.Executable, cfg.Lume.Storage, time.Minute, nil)
	if err != nil {
		return err
	}
	matched := false
	for name, profile := range cfg.Lume.Profiles {
		if selected != "" && name != selected {
			continue
		}
		matched = true
		manifest, err := lume.LoadImageManifest(profile.Manifest, signingKey, guestKey, profile.Image, cfg.Lume.Storage, ver.Version())
		if err != nil {
			return fmt.Errorf("profile %s: %w", name, err)
		}
		vm, err := provider.Get(ctx, profile.Image)
		if err != nil {
			return err
		}
		if vm.State != lume.StateStopped || vm.Storage != cfg.Lume.Storage {
			return fmt.Errorf("profile %s base VM is not stopped in configured storage", name)
		}
		expected := guestproto.Hello{OS: manifest.Payload.GuestOS, Architecture: manifest.Payload.GuestArchitecture, OSVersion: manifest.Payload.GuestOSVersion, AgentSHA256: manifest.Payload.GuestAgentSHA256, UID: manifest.Payload.GuestUID}
		if _, err := attestBaseImage(ctx, cfg, profile, expected); err != nil {
			return fmt.Errorf("profile %s attestation: %w", name, err)
		}
		fmt.Fprintf(command.OutOrStdout(), "valid: %s (%s)\n", name, profile.Image)
	}
	if !matched {
		return fmt.Errorf("unknown Lume profile %q", selected)
	}
	return nil
}

func requireGuestBootstrap(cfg *config.Config) error {
	for description, path := range map[string]string{
		"guest public key":    cfg.Lume.GuestPublicKeyFile,
		"pinned SSH host key": cfg.Lume.KnownHostsFile,
	} {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("guest bootstrap incomplete: %s is missing at %s; complete docs/lume-setup.md before image adopt", description, path)
		} else if err != nil {
			return fmt.Errorf("inspect %s: %w", description, err)
		}
	}
	return nil
}

func loadCommandConfig(command *cobra.Command, configFile *string) (*config.Config, error) {
	file, err := resolveConfigFile(command, configFile)
	if err != nil {
		return nil, err
	}
	return config.LoadDefault(file)
}
