// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"gitea.com/gitea/runner/act/container"
	actrunner "gitea.com/gitea/runner/act/runner"
	"gitea.com/gitea/runner/internal/pkg/config"
	"gitea.com/gitea/runner/internal/pkg/guestagent"
	"gitea.com/gitea/runner/internal/pkg/guestproto"
	"gitea.com/gitea/runner/internal/pkg/lume"
	"gitea.com/gitea/runner/internal/pkg/metrics"
)

type preparedLumeEnvironment struct {
	factory actrunner.ExecutionEnvironmentFactory
	cleanup func(context.Context) error
}

// ProbeLumeGuest verifies the real clone, boot, attestation, and guest transport
// path without executing workflow-provided code. Cleanup uses the normal durable
// ownership checks even when the probe fails.
func ProbeLumeGuest(ctx context.Context, cfg *config.Config, profileName string) error {
	taskID := time.Now().UnixNano()
	prepared, err := prepareLumeEnvironment(ctx, cfg, taskID, profileName)
	if err != nil {
		return err
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), cfg.Lume.Profiles[profileName].CleanupTimeout)
	defer cancel()
	return prepared.cleanup(cleanupContext)
}

func prepareLumeEnvironment(ctx context.Context, cfg *config.Config, taskID int64, profileName string) (*preparedLumeEnvironment, error) {
	if err := CheckLumeResources(cfg); err != nil {
		return nil, err
	}
	profile, ok := cfg.Lume.Profiles[profileName]
	if !ok {
		return nil, fmt.Errorf("unknown Lume profile %q", profileName)
	}
	guestPublicKey, err := guestagent.LoadEd25519PublicKey(cfg.Lume.GuestPublicKeyFile)
	if err != nil {
		return nil, err
	}
	imageSigningPublicKey, err := guestagent.LoadEd25519PublicKey(cfg.Lume.ImageSigningPublicKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load image signing public key: %w", err)
	}
	manifest, err := lume.LoadImageManifest(profile.Manifest, imageSigningPublicKey, guestPublicKey, profile.Image, cfg.Lume.Storage)
	if err != nil {
		return nil, fmt.Errorf("validate signed Lume image manifest: %w", err)
	}
	leaseID, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	workerID, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	nonce, err := randomHex(32)
	if err != nil {
		return nil, err
	}
	vmName := "grl-" + workerID
	now := time.Now().UTC()
	lease := lume.Lease{
		SchemaVersion: 1, TaskID: taskID, Profile: profileName, Phase: lume.PhaseAccepted,
		Ownership: lume.OwnershipEvidence{
			InstallationID: cfg.Lume.InstallationID, LeaseID: leaseID,
			VMID: vmName, VMName: vmName, Storage: cfg.Lume.Storage,
			BaseGeneration: manifest.Payload.Generation, Nonce: nonce,
		},
		CreatedAt: now, UpdatedAt: now, Deadline: now.Add(cfg.Runner.Timeout),
	}
	if err := lease.Validate(cfg.Lume.InstallationID, cfg.Lume.Storage); err != nil {
		return nil, err
	}
	store, err := lume.NewLeaseStore(cfg.Lume.StateDir + "/leases")
	if err != nil {
		return nil, err
	}
	if err := store.Create(lease); err != nil {
		return nil, fmt.Errorf("persist accepted Lume lease: %w", err)
	}
	provider, err := lume.NewProvider(cfg.Lume.Executable, cfg.Lume.Storage, profile.CleanupTimeout, nil)
	if err != nil {
		return nil, err
	}
	baseVM, err := provider.Get(ctx, profile.Image)
	if err != nil {
		return nil, fmt.Errorf("inspect configured Lume base image: %w", err)
	}
	if baseVM.State != lume.StateStopped || baseVM.Storage != cfg.Lume.Storage {
		return nil, errors.New("configured Lume base image is not stopped in the expected storage")
	}
	phaseStarted := time.Now()
	transition := func(phase lume.Phase, errorClass string) error {
		metrics.LumePhaseDuration.WithLabelValues(profileName, string(lease.Phase)).Observe(time.Since(phaseStarted).Seconds())
		var transitionErr error
		lease, transitionErr = lease.Transition(phase, time.Now().UTC(), errorClass)
		if transitionErr != nil {
			return transitionErr
		}
		phaseStarted = time.Now()
		return store.Save(lease)
	}
	if err := transition(lume.PhaseCloning, ""); err != nil {
		return nil, err
	}
	var process *lume.ManagedProcess
	var session *guestagent.SSHSession
	workerCounted := false
	var cleanupOnce sync.Once
	var cleanupResult error
	cleanup := func(cleanupContext context.Context) error {
		cleanupOnce.Do(func() {
			if workerCounted {
				metrics.LumeWorkers.WithLabelValues(profileName).Dec()
				workerCounted = false
			}
			_ = transition(lume.PhaseStopping, "")
			if err := provider.Stop(cleanupContext, vmName, false); err != nil {
				if forceErr := provider.Stop(cleanupContext, vmName, true); forceErr != nil {
					cleanupResult = errors.Join(err, forceErr)
				}
			}
			if process != nil {
				_ = process.Kill()
				_ = process.Wait()
			}
			if session != nil {
				_ = session.Close(cleanupContext)
			}
			_ = transition(lume.PhaseDeleting, "")
			if err := provider.DeleteOwned(cleanupContext, lease.Ownership, cfg.Lume.InstallationID); err != nil {
				metrics.LumeCleanupFailures.WithLabelValues("delete").Inc()
				_ = transition(lume.PhaseCleanupError, "delete")
				cleanupResult = errors.Join(cleanupResult, err)
				return
			}
			_ = transition(lume.PhaseComplete, "")
		})
		return cleanupResult
	}
	failureCleanup := true
	defer func() {
		if failureCleanup {
			cleanupContext, cancel := context.WithTimeout(context.Background(), profile.CleanupTimeout)
			defer cancel()
			_ = cleanup(cleanupContext)
		}
	}()
	if err := provider.Clone(ctx, profile.Image, vmName); err != nil {
		_ = transition(lume.PhaseCleanupError, "clone")
		return nil, err
	}
	metrics.LumeWorkers.WithLabelValues(profileName).Inc()
	workerCounted = true
	if err := provider.Configure(ctx, vmName, profile.CPU, profile.MemoryGB, profile.DiskGB); err != nil {
		_ = transition(lume.PhaseCleanupError, "configure")
		return nil, err
	}

	if err := transition(lume.PhaseBooting, ""); err != nil {
		return nil, err
	}
	process, err = provider.StartManaged(ctx, vmName)
	if err != nil {
		return nil, err
	}
	vm, err := waitForRunningVM(ctx, provider, vmName, cfg.Lume.Storage, profile.BootTimeout)
	if err != nil {
		return nil, err
	}
	if err := transition(lume.PhaseAttesting, ""); err != nil {
		return nil, err
	}
	hostPrivateKey, err := guestagent.LoadEd25519PrivateKey(cfg.Lume.SSHIdentityFile)
	if err != nil {
		return nil, err
	}
	hello := guestproto.Hello{
		InstallationID: cfg.Lume.InstallationID, LeaseID: leaseID,
		WorkerID: workerID, TaskID: taskID, Nonce: nonce, Revision: manifest.Payload.GuestRevision,
		OS: manifest.Payload.GuestOS, Architecture: manifest.Payload.GuestArchitecture,
		OSVersion: manifest.Payload.GuestOSVersion, AgentSHA256: manifest.Payload.GuestAgentSHA256,
		UID: manifest.Payload.GuestUID,
	}
	guestRoot := "/private/var/tmp/gitea-runner-lume/" + workerID
	session, err = guestagent.StartSSHSession(ctx, guestagent.SSHConfig{
		Executable: "/usr/bin/ssh", IdentityFile: cfg.Lume.SSHIdentityFile,
		KnownHostsFile: cfg.Lume.KnownHostsFile, User: cfg.Lume.SSHUser,
		HostKeyAlias: profile.Image,
		Address:      vm.IPAddress, Port: cfg.Lume.SSHPort, ConnectTimeout: cfg.Lume.ConnectTimeout,
		GuestBinary: cfg.Lume.GuestBinary, GuestRoot: guestRoot,
	}, hello, hostPrivateKey, guestPublicKey)
	if err != nil {
		return nil, err
	}
	if err := transition(lume.PhaseExecuting, ""); err != nil {
		return nil, err
	}
	client := &lifecycleClient{Client: session.Client, cleanup: cleanup}
	var factoryOnce sync.Once
	var environment container.ExecutionsEnvironment
	factory := func(_ context.Context, input actrunner.ExecutionEnvironmentInput) (container.ExecutionsEnvironment, error) {
		factoryOnce.Do(func() {
			environment = &container.RemoteEnvironment{
				Client: client, Path: guestRoot + "/work", Workdir: input.Workdir,
				ActPath: guestRoot + "/act", TmpDir: guestRoot + "/tmp",
				ToolCache: guestRoot + "/toolcache", Stdout: input.Stdout,
			}
		})
		if environment == nil {
			return nil, errors.New("Lume execution environment was already consumed")
		}
		return environment, nil
	}
	failureCleanup = false
	return &preparedLumeEnvironment{factory: factory, cleanup: cleanup}, nil
}

type lifecycleClient struct {
	*guestagent.Client
	cleanup func(context.Context) error
}

func (c *lifecycleClient) Close(ctx context.Context) error { return c.cleanup(ctx) }

func waitForRunningVM(parent context.Context, provider *lume.Provider, id, storage string, timeout time.Duration) (lume.VM, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		vm, err := provider.Get(ctx, id)
		if err == nil && vm.ID == id && vm.Storage == storage && vm.State == lume.StateRunning && vm.IPAddress != "" && vm.SSHAvailable {
			return vm, nil
		}
		select {
		case <-ctx.Done():
			return lume.VM{}, fmt.Errorf("wait for Lume guest readiness: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func randomHex(bytesCount int) (string, error) {
	value := make([]byte, bytesCount)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}
