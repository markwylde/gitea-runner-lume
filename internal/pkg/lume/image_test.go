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

func TestSignedImageManifestBindsImageStorageRevisionAndGuestIdentity(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	fingerprint := sha256.Sum256(publicKey)
	payload := ImageManifestPayload{
		SchemaVersion: 2, Image: "xcode-image", Storage: "default",
		Generation: "generation-1", GuestRevision: "runner-revision",
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
	_, err = LoadImageManifest(path, publicKey, publicKey, payload.Image, payload.Storage, payload.GuestRevision)
	require.NoError(t, err)

	tampered := manifest
	tampered.Payload.Image = "other-image"
	data, err = EncodeImageManifest(tampered)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	_, err = LoadImageManifest(path, publicKey, publicKey, "other-image", payload.Storage, payload.GuestRevision)
	require.Error(t, err)
}
