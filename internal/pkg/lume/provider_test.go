// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package lume

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const stoppedVMJSON = `[{"name":"worker-a","os":"macOS","cpuCount":4,"memorySize":8589934592,"diskSize":{"allocated":1,"total":107374182400},"display":"1024x768","status":"stopped","provisioningOperation":null,"vncUrl":null,"ipAddress":null,"sshAvailable":null,"locationName":"default","sharedDirectories":null,"networkMode":"nat","downloadProgress":null}]`
const runningVMJSON = `[{"name":"worker-a","os":"macOS","cpuCount":4,"memorySize":8589934592,"diskSize":{"allocated":1,"total":107374182400},"display":"1024x768","status":"running","provisioningOperation":null,"vncUrl":null,"ipAddress":"192.168.64.4","sshAvailable":true,"locationName":"default","sharedDirectories":null,"networkMode":"nat","downloadProgress":null}]`

const provisioningVMJSON = `[{"name":"worker-a","os":"macOS","cpuCount":4,"memorySize":8589934592,"diskSize":{"allocated":1,"total":107374182400},"display":"1024x768","status":"provisioning","provisioningOperation":"installing macOS","vncUrl":null,"ipAddress":null,"sshAvailable":null,"locationName":"default","sharedDirectories":null,"networkMode":"nat","downloadProgress":42.5}]`

type fakeRunner struct {
	results []Result
	calls   [][]string
}

type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ string, _ []string, _ int) (Result, error) {
	<-ctx.Done()
	return Result{}, ctx.Err()
}

func (f *fakeRunner) Run(_ context.Context, executable string, args []string, _ int) (Result, error) {
	f.calls = append(f.calls, append([]string{executable}, args...))
	if len(f.results) == 0 {
		return Result{}, errors.New("no fake result")
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result, nil
}

func TestProviderUsesStructuredArgumentsAndStrictOutput(t *testing.T) {
	runner := &fakeRunner{results: []Result{
		{Stdout: []byte(stoppedVMJSON)},
		{Stdout: []byte(runningVMJSON)},
		{}, {}, {},
	}}
	provider, err := NewProvider("/opt/homebrew/bin/lume", "default", time.Second, runner)
	require.NoError(t, err)
	vms, err := provider.List(t.Context())
	require.NoError(t, err)
	require.Len(t, vms, 1)
	vm, err := provider.Get(t.Context(), "worker-a")
	require.NoError(t, err)
	require.Equal(t, "192.168.64.4", vm.IPAddress)
	require.NoError(t, provider.Clone(t.Context(), "base-image", "worker-b"))
	require.NoError(t, provider.Start(t.Context(), "worker-b"))
	require.NoError(t, provider.Stop(t.Context(), "worker-b", true))
	require.Equal(t, []string{"/opt/homebrew/bin/lume", "stop", "worker-b", "--storage", "default"}, runner.calls[4])
	for _, call := range runner.calls {
		require.False(t, slices.Contains(call, "sh"))
	}
}

func TestProviderExposesStructuredProvisioningProgress(t *testing.T) {
	runner := &fakeRunner{results: []Result{{Stdout: []byte(provisioningVMJSON)}}}
	provider, err := NewProvider("/opt/homebrew/bin/lume", "default", time.Second, runner)
	require.NoError(t, err)

	vm, err := provider.Get(t.Context(), "worker-a")
	require.NoError(t, err)
	require.Equal(t, StateProvisioning, vm.State)
	require.Equal(t, "installing macOS", vm.ProvisioningOperation)
	require.NotNil(t, vm.DownloadProgress)
	require.Equal(t, 42.5, *vm.DownloadProgress)
}

func TestProviderRejectsInvalidProvisioningProgress(t *testing.T) {
	runner := &fakeRunner{results: []Result{{Stdout: []byte(strings.Replace(provisioningVMJSON, "42.5", "101", 1))}}}
	provider, err := NewProvider("/opt/homebrew/bin/lume", "default", time.Second, runner)
	require.NoError(t, err)
	_, err = provider.Get(t.Context(), "worker-a")
	require.ErrorContains(t, err, "download progress")
}

func TestConfigurePreservesInheritedCloneDisk(t *testing.T) {
	runner := &fakeRunner{results: []Result{{}}}
	provider, err := NewProvider("/opt/homebrew/bin/lume", "default", time.Second, runner)
	require.NoError(t, err)

	require.NoError(t, provider.Configure(t.Context(), "worker-a", 4, 8, 100))
	require.Equal(t, []string{
		"/opt/homebrew/bin/lume", "set", "worker-a", "--cpu", "4",
		"--memory", "8GB", "--storage", "default",
	}, runner.calls[0])
	require.NotContains(t, runner.calls[0], "--disk-size")
}

func TestProviderRejectsUntrustedOutputAndIdentifiers(t *testing.T) {
	runner := &fakeRunner{results: []Result{{Stdout: []byte(`[{"name":"../../host","os":"macOS","cpuCount":4,"memorySize":8,"diskSize":{"allocated":1,"total":2},"display":"x","status":"running","provisioningOperation":null,"vncUrl":null,"ipAddress":null,"sshAvailable":null,"locationName":"default","sharedDirectories":null,"networkMode":null,"downloadProgress":null}]`)}}}
	provider, err := NewProvider("/opt/homebrew/bin/lume", "default", time.Second, runner)
	require.NoError(t, err)
	_, err = provider.List(t.Context())
	require.Error(t, err)
	require.Error(t, provider.Clone(t.Context(), "base", "worker;touch-host"))
}

func TestProviderRejectsNonMacOSAndHostSharedDirectories(t *testing.T) {
	for name, output := range map[string]string{
		"wrong OS":   strings.ReplaceAll(stoppedVMJSON, `"os":"macOS"`, `"os":"linux"`),
		"host share": strings.ReplaceAll(stoppedVMJSON, `"sharedDirectories":null`, `"sharedDirectories":[{"hostPath":"/Users/operator","tag":"host","readOnly":false}]`),
	} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{results: []Result{{Stdout: []byte(output)}}}
			provider, err := NewProvider("/opt/homebrew/bin/lume", "default", time.Second, runner)
			require.NoError(t, err)
			_, err = provider.List(t.Context())
			require.Error(t, err)
		})
	}
}

func TestDeleteRequiresMatchingDurableAndProviderEvidence(t *testing.T) {
	evidence := OwnershipEvidence{
		InstallationID: "0123456789abcdef0123456789abcdef",
		LeaseID:        "abcdef0123456789abcdef0123456789",
		VMID:           "worker-a",
		VMName:         "worker-a",
		Storage:        "default",
		BaseGeneration: "generation-1",
		Nonce:          "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	runner := &fakeRunner{results: []Result{
		{Stdout: []byte(stoppedVMJSON)},
		{},
		{Stdout: []byte(`[]`)},
	}}
	provider, err := NewProvider("/opt/homebrew/bin/lume", "default", time.Second, runner)
	require.NoError(t, err)
	require.NoError(t, provider.DeleteOwned(t.Context(), evidence, evidence.InstallationID))
	require.Equal(t, []string{"/opt/homebrew/bin/lume", "delete", "worker-a", "--force", "--storage", "default"}, runner.calls[1])

	evidence.VMName = "other-worker"
	require.Error(t, provider.DeleteOwned(t.Context(), evidence, evidence.InstallationID))
}

func TestProviderBoundsCommandsWithTimeout(t *testing.T) {
	provider, err := NewProvider("/opt/homebrew/bin/lume", "default", 10*time.Millisecond, blockingRunner{})
	require.NoError(t, err)
	_, err = provider.Version(t.Context())
	require.ErrorContains(t, err, "timed out")
}

func TestProviderRejectsStorageMismatchWithoutDelete(t *testing.T) {
	evidence := validLease().Ownership
	foreign := strings.ReplaceAll(stoppedVMJSON, `"locationName":"default"`, `"locationName":"foreign"`)
	runner := &fakeRunner{results: []Result{{Stdout: []byte(foreign)}}}
	provider, err := NewProvider("/opt/homebrew/bin/lume", "default", time.Second, runner)
	require.NoError(t, err)
	require.Error(t, provider.DeleteOwned(t.Context(), evidence, evidence.InstallationID))
	require.Len(t, runner.calls, 1)
}
