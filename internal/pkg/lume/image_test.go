// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package lume

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSignedImageManifestAllowsCompatibleControllerUpgradeAndBindsGuestRevision(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	fingerprint := sha256.Sum256(publicKey)
	payload := ImageManifestPayload{
		SchemaVersion: 2, Image: "xcode-image", Storage: "default",
		Generation: "generation-1", GuestRevision: "v0.4.0",
		GuestPublicKeyFingerprint: hex.EncodeToString(fingerprint[:]),
		GuestOS:                   "darwin", GuestArchitecture: "arm64", GuestOSVersion: "15.6",
		GuestAgentSHA256: strings.Repeat("a", 64), GuestUID: 501,
		ValidatedAt: time.Now().UTC(),
	}
	manifest, err := SignImageManifest(payload, privateKey)
	require.NoError(t, err)
	data, err := EncodeImageManifest(manifest)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "manifest.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	loaded, err := LoadImageManifest(path, publicKey, publicKey, payload.Image, payload.Storage)
	require.NoError(t, err)
	require.Equal(t, "v0.4.0", loaded.Payload.GuestRevision)

	tamperedRevision := manifest
	tamperedRevision.Payload.GuestRevision = "v0.4.1"
	data, err = EncodeImageManifest(tamperedRevision)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	_, err = LoadImageManifest(path, publicKey, publicKey, payload.Image, payload.Storage)
	require.ErrorContains(t, err, "signature verification failed")

	tampered := manifest
	tampered.Payload.Image = "other-image"
	data, err = EncodeImageManifest(tampered)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	_, err = LoadImageManifest(path, publicKey, publicKey, "other-image", payload.Storage)
	require.Error(t, err)
}
