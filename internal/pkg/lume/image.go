// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package lume

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type ImageManifestPayload struct {
	SchemaVersion             int       `json:"schema_version"`
	Image                     string    `json:"image"`
	Storage                   string    `json:"storage"`
	Generation                string    `json:"generation"`
	GuestRevision             string    `json:"guest_revision"`
	GuestPublicKeyFingerprint string    `json:"guest_public_key_fingerprint"`
	GuestOS                   string    `json:"guest_os"`
	GuestArchitecture         string    `json:"guest_architecture"`
	GuestOSVersion            string    `json:"guest_os_version"`
	GuestAgentSHA256          string    `json:"guest_agent_sha256"`
	GuestUID                  int       `json:"guest_uid"`
	ValidatedAt               time.Time `json:"validated_at"`
}

type ImageManifest struct {
	Payload   ImageManifestPayload `json:"payload"`
	Signature string               `json:"signature"`
}

func LoadImageManifest(path string, signingPublicKey, guestPublicKey ed25519.PublicKey, expectedImage, expectedStorage, expectedRevision string) (ImageManifest, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ImageManifest{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() <= 0 || info.Size() > 64*1024 {
		return ImageManifest{}, errors.New("image manifest must be a bounded regular file not writable by group or others")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ImageManifest{}, err
	}
	var manifest ImageManifest
	if err := decodeStrict(data, &manifest); err != nil {
		return ImageManifest{}, err
	}
	payload := manifest.Payload
	if payload.SchemaVersion != 2 || payload.Image != expectedImage || payload.Storage != expectedStorage || payload.GuestRevision != expectedRevision {
		return ImageManifest{}, errors.New("image manifest does not match configured image, storage, or guest revision")
	}
	if err := validateImageManifestPayload(payload); err != nil {
		return ImageManifest{}, err
	}
	fingerprint := sha256.Sum256(guestPublicKey)
	if payload.GuestPublicKeyFingerprint != hex.EncodeToString(fingerprint[:]) {
		return ImageManifest{}, errors.New("image manifest guest identity mismatch")
	}
	signature, err := hex.DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return ImageManifest{}, errors.New("image manifest signature is invalid")
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return ImageManifest{}, err
	}
	if !ed25519.Verify(signingPublicKey, canonical, signature) {
		return ImageManifest{}, errors.New("image manifest signature verification failed")
	}
	return manifest, nil
}

func SignImageManifest(payload ImageManifestPayload, privateKey ed25519.PrivateKey) (ImageManifest, error) {
	if err := validateImageManifestPayload(payload); err != nil {
		return ImageManifest{}, err
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return ImageManifest{}, err
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return ImageManifest{}, fmt.Errorf("invalid image signing key")
	}
	return ImageManifest{Payload: payload, Signature: hex.EncodeToString(ed25519.Sign(privateKey, canonical))}, nil
}

func validateImageManifestPayload(payload ImageManifestPayload) error {
	if payload.SchemaVersion != 2 || !validIdentifier(payload.Image) || !validIdentifier(payload.Storage) ||
		!validIdentifier(payload.Generation) || payload.GuestRevision == "" || len(payload.GuestRevision) > 128 ||
		!noncePattern.MatchString(payload.GuestPublicKeyFingerprint) || payload.ValidatedAt.IsZero() {
		return errors.New("image manifest identity, schema, revision, or validation time is invalid")
	}
	if payload.GuestOS != "darwin" || payload.GuestArchitecture != "arm64" || payload.GuestOSVersion == "" ||
		len(payload.GuestOSVersion) > 128 || !noncePattern.MatchString(payload.GuestAgentSHA256) || payload.GuestUID <= 0 {
		return errors.New("image manifest guest platform attestation is invalid")
	}
	return nil
}

func EncodeImageManifest(manifest ImageManifest) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func WriteImageManifest(path string, manifest ImageManifest) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("image manifest path must be absolute")
	}
	data, err := EncodeImageManifest(manifest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".new"
	if info, statErr := os.Lstat(temporary); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("stale image-manifest temporary file is insecure")
		}
		if err := os.Remove(temporary); err != nil {
			return err
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(temporary)
		return errors.Join(err, closeErr)
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
