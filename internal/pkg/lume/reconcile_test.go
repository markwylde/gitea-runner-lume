// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package lume

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReconcileCompletesLeaseWhenVMWasNeverCreated(t *testing.T) {
	store, err := NewLeaseStore(filepath.Join(t.TempDir(), "leases"))
	require.NoError(t, err)
	lease := validLease()
	require.NoError(t, store.Create(lease))
	runner := &fakeRunner{results: []Result{{Stdout: []byte(`[]`)}}}
	provider, err := NewProvider("/opt/homebrew/bin/lume", "default", time.Second, runner)
	require.NoError(t, err)
	results, err := Reconcile(t.Context(), store, provider, lease.Ownership.InstallationID, lease.Ownership.Storage)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, PhaseComplete, results[0].Phase)
	require.Len(t, runner.calls, 1)
}

func TestReconcileDoesNotDeleteUnleasedInventory(t *testing.T) {
	store, err := NewLeaseStore(filepath.Join(t.TempDir(), "leases"))
	require.NoError(t, err)
	runner := &fakeRunner{results: []Result{{Stdout: []byte(stoppedVMJSON)}}}
	provider, err := NewProvider("/opt/homebrew/bin/lume", "default", time.Second, runner)
	require.NoError(t, err)
	results, err := Reconcile(t.Context(), store, provider, "0123456789abcdef0123456789abcdef", "default")
	require.NoError(t, err)
	require.Empty(t, results)
	require.Len(t, runner.calls, 1)
}

func TestReconcileDeletesResidualVMForCompletedLease(t *testing.T) {
	store, err := NewLeaseStore(filepath.Join(t.TempDir(), "leases"))
	require.NoError(t, err)
	lease := validLease()
	lease.Phase = PhaseComplete
	require.NoError(t, store.Create(lease))
	runner := &fakeRunner{results: []Result{
		{Stdout: []byte(stoppedVMJSON)},
		{Stdout: []byte(stoppedVMJSON)},
		{},
		{Stdout: []byte(`[]`)},
	}}
	provider, err := NewProvider("/opt/homebrew/bin/lume", "default", time.Second, runner)
	require.NoError(t, err)

	results, err := Reconcile(t.Context(), store, provider, lease.Ownership.InstallationID, lease.Ownership.Storage)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)
	require.Equal(t, PhaseComplete, results[0].Phase)
	require.Equal(t, []string{"/opt/homebrew/bin/lume", "delete", "worker-a", "--force", "--storage", "default"}, runner.calls[2])
}

func TestReconcileQuarantinesStorageMismatchWithoutMutation(t *testing.T) {
	store, err := NewLeaseStore(filepath.Join(t.TempDir(), "leases"))
	require.NoError(t, err)
	lease := validLease()
	require.NoError(t, store.Create(lease))
	foreign := strings.ReplaceAll(stoppedVMJSON, `"locationName":"default"`, `"locationName":"foreign"`)
	runner := &fakeRunner{results: []Result{{Stdout: []byte(foreign)}}}
	provider, err := NewProvider("/opt/homebrew/bin/lume", "default", time.Second, runner)
	require.NoError(t, err)
	results, err := Reconcile(t.Context(), store, provider, lease.Ownership.InstallationID, lease.Ownership.Storage)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Error(t, results[0].Err)
	require.Equal(t, PhaseCleanupError, results[0].Phase)
	require.Len(t, runner.calls, 1)
}
