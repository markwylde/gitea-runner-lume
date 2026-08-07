// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package run

import (
	"context"
	"fmt"
	"time"

	"gitea.com/gitea/runner/internal/pkg/config"
	"gitea.com/gitea/runner/internal/pkg/lume"
)

func ReconcileLume(ctx context.Context, cfg *config.Config) ([]lume.CleanupResult, error) {
	if !cfg.Lume.Enabled {
		return nil, nil
	}
	timeout := time.Minute
	for _, profile := range cfg.Lume.Profiles {
		if profile.CleanupTimeout > timeout {
			timeout = profile.CleanupTimeout
		}
	}
	provider, err := lume.NewProvider(cfg.Lume.Executable, cfg.Lume.Storage, timeout, nil)
	if err != nil {
		return nil, err
	}
	store, err := lume.NewLeaseStore(cfg.Lume.StateDir + "/leases")
	if err != nil {
		return nil, err
	}
	results, err := lume.Reconcile(ctx, store, provider, cfg.Lume.InstallationID, cfg.Lume.Storage)
	if err != nil {
		return nil, err
	}
	for _, result := range results {
		if result.Err != nil {
			return results, fmt.Errorf("reconcile lease %s: %w", result.LeaseID, result.Err)
		}
	}
	return results, nil
}
