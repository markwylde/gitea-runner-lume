// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package guestagent

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"

	"golang.org/x/crypto/ssh"
)

func LoadEd25519PrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := readOwnerOnly(path)
	if err != nil {
		return nil, err
	}
	key, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse Ed25519 private key: %w", err)
	}
	privateKey, ok := key.(*ed25519.PrivateKey)
	if ok {
		return *privateKey, nil
	}
	value, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not Ed25519")
	}
	return value, nil
}

func LoadEd25519PublicKey(path string) (ed25519.PublicKey, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("public key must be a regular file not writable by group or others")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	publicKey, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse Ed25519 public key: %w", err)
	}
	cryptoKey, ok := publicKey.(ssh.CryptoPublicKey)
	if !ok {
		return nil, errors.New("public key has no crypto representation")
	}
	ed25519Key, ok := cryptoKey.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("public key is not Ed25519")
	}
	return ed25519Key, nil
}

func readOwnerOnly(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("private key must be an owner-only regular file")
	}
	return os.ReadFile(path)
}
