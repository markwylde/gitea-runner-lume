// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package lume

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func validLease() Lease {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	return Lease{
		SchemaVersion: 1,
		TaskID:        42,
		Profile:       "xcode-16",
		Phase:         PhaseAccepted,
		Ownership: OwnershipEvidence{
			InstallationID: "0123456789abcdef0123456789abcdef",
			LeaseID:        "abcdef0123456789abcdef0123456789",
			VMID:           "worker-a",
			VMName:         "worker-a",
			Storage:        "default",
			BaseGeneration: "generation-1",
			Nonce:          "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		CreatedAt: now,
		UpdatedAt: now,
		Deadline:  now.Add(time.Hour),
	}
}

func TestLeaseStoreCreatesOwnerOnlyAndTransitionsMonotonically(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "leases")
	store, err := NewLeaseStore(directory)
	require.NoError(t, err)
	lease := validLease()
	require.NoError(t, lease.Validate(lease.Ownership.InstallationID, lease.Ownership.Storage))
	require.NoError(t, store.Create(lease))

	info, err := os.Stat(store.path(lease.Ownership.LeaseID))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	lease, err = lease.Transition(PhaseCloning, lease.UpdatedAt.Add(time.Second), "")
	require.NoError(t, err)
	require.NoError(t, store.Save(lease))
	loaded, err := store.Load(lease.Ownership.LeaseID)
	require.NoError(t, err)
	require.Equal(t, PhaseCloning, loaded.Phase)
	_, err = loaded.Transition(PhaseAccepted, loaded.UpdatedAt.Add(time.Second), "")
	require.Error(t, err)
	failed, err := loaded.Transition(PhaseCleanupError, loaded.UpdatedAt.Add(time.Second), "delete")
	require.NoError(t, err)
	_, err = failed.Transition(PhaseStopping, failed.UpdatedAt.Add(time.Second), "")
	require.NoError(t, err)
}

func TestLeaseStoreRejectsSymlinkAndDuplicateCreation(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "leases")
	store, err := NewLeaseStore(directory)
	require.NoError(t, err)
	lease := validLease()
	require.NoError(t, store.Create(lease))
	require.Error(t, store.Create(lease))

	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.WriteFile(target, []byte("{}"), 0o600))
	lease.Ownership.LeaseID = "11111111111111111111111111111111"
	require.NoError(t, os.Symlink(target, store.path(lease.Ownership.LeaseID)))
	_, err = store.Load(lease.Ownership.LeaseID)
	require.Error(t, err)
}

func TestLeaseStoreListSortsValidatedLeases(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "leases")
	store, err := NewLeaseStore(directory)
	require.NoError(t, err)
	for _, id := range []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		lease := validLease()
		lease.Ownership.LeaseID = id
		require.NoError(t, store.Create(lease))
	}
	lease := validLease()
	leases, err := store.List(lease.Ownership.InstallationID, lease.Ownership.Storage)
	require.NoError(t, err)
	require.Len(t, leases, 2)
	require.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", leases[0].Ownership.LeaseID)
}

func TestLeaseStoreRecoversOwnerOnlyInterruptedSave(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "leases")
	store, err := NewLeaseStore(directory)
	require.NoError(t, err)
	lease := validLease()
	require.NoError(t, store.Create(lease))
	require.NoError(t, os.WriteFile(store.path(lease.Ownership.LeaseID)+".new", []byte("partial"), 0o600))
	lease, err = lease.Transition(PhaseCloning, lease.UpdatedAt.Add(time.Second), "")
	require.NoError(t, err)
	require.NoError(t, store.Save(lease))
	loaded, err := store.Load(lease.Ownership.LeaseID)
	require.NoError(t, err)
	require.Equal(t, PhaseCloning, loaded.Phase)
}

func TestLeaseTransitionMatrix(t *testing.T) {
	phases := []Phase{PhaseAccepted, PhaseCloning, PhaseBooting, PhaseAttesting, PhaseExecuting, PhaseStopping, PhaseDeleting, PhaseCleanupError, PhaseComplete}
	for _, from := range phases {
		for _, to := range phases {
			t.Run(string(from)+"_to_"+string(to), func(t *testing.T) {
				lease := validLease()
				lease.Phase = from
				_, err := lease.Transition(to, lease.UpdatedAt.Add(time.Second), "test")
				fromOrder, toOrder := phaseOrder[from], phaseOrder[to]
				allowed := from != PhaseComplete || to == PhaseComplete
				allowed = allowed && (toOrder >= fromOrder || to == PhaseStopping || to == PhaseCleanupError)
				if allowed {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}

	lease := validLease()
	lease.Phase = "unknown"
	_, err := lease.Transition(PhaseComplete, lease.UpdatedAt, "")
	require.Error(t, err)
	_, err = validLease().Transition("unknown", lease.UpdatedAt, "")
	require.Error(t, err)
}
