// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package lume

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type CleanupResult struct {
	LeaseID string
	VMID    string
	Phase   Phase
	Err     error
}

// Reconcile cleans every non-complete durable lease. Provider deletion remains
// gated by the complete ownership evidence persisted before VM creation.
func Reconcile(ctx context.Context, store *LeaseStore, provider *Provider, installationID, storage string) ([]CleanupResult, error) {
	leases, err := store.List(installationID, storage)
	if err != nil {
		return nil, err
	}
	vms, err := provider.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Lume inventory before reconciliation: %w", err)
	}
	inventory := make(map[string]VM, len(vms))
	for _, vm := range vms {
		inventory[vm.ID] = vm
	}
	results := make([]CleanupResult, 0, len(leases))
	for _, lease := range leases {
		result := CleanupResult{LeaseID: lease.Ownership.LeaseID, VMID: lease.Ownership.VMID, Phase: lease.Phase}
		vm, exists := inventory[lease.Ownership.VMID]
		if lease.Phase == PhaseComplete && !exists {
			results = append(results, result)
			continue
		}
		if lease.Phase == PhaseComplete {
			if vm.Name != lease.Ownership.VMName || vm.Storage != lease.Ownership.Storage {
				result.Err = errors.New("residual VM does not match completed durable ownership evidence")
				results = append(results, result)
				continue
			}
			if vm.State != StateStopped {
				if err := provider.Stop(ctx, vm.ID, false); err != nil {
					result.Err = fmt.Errorf("stop residual owned VM: %w", err)
					results = append(results, result)
					continue
				}
			}
			if err := provider.DeleteOwned(ctx, lease.Ownership, installationID); err != nil {
				result.Err = fmt.Errorf("delete residual owned VM: %w", err)
			}
			results = append(results, result)
			continue
		}
		if !exists {
			lease, err = lease.Transition(PhaseComplete, time.Now().UTC(), "")
			if err == nil {
				err = store.Save(lease)
			}
			if err != nil {
				result.Err = err
			} else {
				result.Phase = PhaseComplete
			}
			results = append(results, result)
			continue
		}
		if vm.Name != lease.Ownership.VMName || vm.Storage != lease.Ownership.Storage {
			result.Err = errors.New("inventory VM does not match durable ownership evidence")
			lease, _ = lease.Transition(PhaseCleanupError, time.Now().UTC(), "ownership_mismatch")
			_ = store.Save(lease)
			result.Phase = lease.Phase
			results = append(results, result)
			continue
		}
		lease, err = lease.Transition(PhaseStopping, time.Now().UTC(), "")
		if err == nil {
			err = store.Save(lease)
		}
		if err == nil && vm.State != StateStopped {
			if stopErr := provider.Stop(ctx, lease.Ownership.VMID, false); stopErr != nil {
				if forceErr := provider.Stop(ctx, lease.Ownership.VMID, true); forceErr != nil {
					err = fmt.Errorf("stop owned VM: %w", errors.Join(stopErr, forceErr))
				}
			}
		}
		if err == nil {
			lease, err = lease.Transition(PhaseDeleting, time.Now().UTC(), "")
			if err == nil {
				err = store.Save(lease)
			}
		}
		if err == nil {
			err = provider.DeleteOwned(ctx, lease.Ownership, installationID)
		}
		if err == nil {
			lease, err = lease.Transition(PhaseComplete, time.Now().UTC(), "")
			if err == nil {
				err = store.Save(lease)
			}
		}
		if err != nil {
			lease, _ = lease.Transition(PhaseCleanupError, time.Now().UTC(), "reconcile")
			_ = store.Save(lease)
			result.Err = err
		}
		result.Phase = lease.Phase
		results = append(results, result)
	}
	return results, nil
}
