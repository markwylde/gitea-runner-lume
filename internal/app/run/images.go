// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package run

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gitea.com/gitea/runner/internal/pkg/config"
	"gitea.com/gitea/runner/internal/pkg/guestagent"
	"gitea.com/gitea/runner/internal/pkg/lume"
)

func ValidateLumeImages(ctx context.Context, cfg *config.Config) error {
	if !cfg.Lume.Enabled {
		return nil
	}
	guestKey, err := guestagent.LoadEd25519PublicKey(cfg.Lume.GuestPublicKeyFile)
	if err != nil {
		return fmt.Errorf("load guest identity: %w", err)
	}
	signingKey, err := guestagent.LoadEd25519PublicKey(cfg.Lume.ImageSigningPublicKeyFile)
	if err != nil {
		return fmt.Errorf("load image signing identity: %w", err)
	}
	provider, err := lume.NewProvider(cfg.Lume.Executable, cfg.Lume.Storage, time.Minute, nil)
	if err != nil {
		return err
	}
	version, err := provider.Version(ctx)
	if err != nil {
		return fmt.Errorf("read Lume version: %w", err)
	}
	supported := false
	for _, allowed := range cfg.Lume.SupportedVersions {
		if version == allowed {
			supported = true
			break
		}
	}
	if !supported {
		return fmt.Errorf("unsupported Lume version %q", version)
	}
	for name, profile := range cfg.Lume.Profiles {
		if _, err := lume.LoadImageManifest(profile.Manifest, signingKey, guestKey, profile.Image, cfg.Lume.Storage); err != nil {
			return fmt.Errorf("validate profile %q manifest: %w", name, err)
		}
		vm, err := provider.Get(ctx, profile.Image)
		if err != nil {
			return fmt.Errorf("inspect profile %q base VM: %w", name, err)
		}
		if vm.State != lume.StateStopped || vm.Storage != cfg.Lume.Storage {
			return fmt.Errorf("profile %q: %w", name, errors.New("base VM must be stopped in configured storage"))
		}
	}
	return nil
}
