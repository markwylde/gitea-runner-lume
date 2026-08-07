// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package lume

import (
	"errors"
	"regexp"
)

var (
	installationPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
	leasePattern        = regexp.MustCompile(`^[a-f0-9]{32}$`)
	noncePattern        = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type OwnershipEvidence struct {
	InstallationID string `json:"installation_id"`
	LeaseID        string `json:"lease_id"`
	VMID           string `json:"vm_id"`
	VMName         string `json:"vm_name"`
	Storage        string `json:"storage"`
	BaseGeneration string `json:"base_generation"`
	Nonce          string `json:"nonce"`
}

func (e OwnershipEvidence) Validate(expectedInstallationID, expectedStorage string) error {
	if !installationPattern.MatchString(e.InstallationID) || e.InstallationID != expectedInstallationID {
		return errors.New("ownership installation identity mismatch")
	}
	if !leasePattern.MatchString(e.LeaseID) || !noncePattern.MatchString(e.Nonce) {
		return errors.New("ownership lease or nonce is invalid")
	}
	if !validIdentifier(e.VMID) || !validIdentifier(e.VMName) || !validIdentifier(e.Storage) {
		return errors.New("ownership VM identity is invalid")
	}
	if e.Storage != expectedStorage || !validIdentifier(e.BaseGeneration) {
		return errors.New("ownership storage or base generation mismatch")
	}
	return nil
}
