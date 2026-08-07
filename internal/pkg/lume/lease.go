// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package lume

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Phase string

const (
	PhaseAccepted     Phase = "accepted"
	PhaseCloning      Phase = "cloning"
	PhaseBooting      Phase = "booting"
	PhaseAttesting    Phase = "attesting"
	PhaseExecuting    Phase = "executing"
	PhaseStopping     Phase = "stopping"
	PhaseDeleting     Phase = "deleting"
	PhaseCleanupError Phase = "cleanup_error"
	PhaseComplete     Phase = "complete"
)

var phaseOrder = map[Phase]int{
	PhaseAccepted: 0, PhaseCloning: 1, PhaseBooting: 2, PhaseAttesting: 3,
	PhaseExecuting: 4, PhaseStopping: 5, PhaseDeleting: 6,
	PhaseCleanupError: 7, PhaseComplete: 8,
}

type Lease struct {
	SchemaVersion  int               `json:"schema_version"`
	TaskID         int64             `json:"task_id"`
	Profile        string            `json:"profile"`
	Phase          Phase             `json:"phase"`
	Ownership      OwnershipEvidence `json:"ownership"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	Deadline       time.Time         `json:"deadline"`
	LastErrorClass string            `json:"last_error_class,omitempty"`
}

func (l Lease) Validate(installationID, storage string) error {
	if l.SchemaVersion != 1 || l.TaskID <= 0 || !validIdentifier(l.Profile) {
		return errors.New("lease schema, task, or profile is invalid")
	}
	if _, ok := phaseOrder[l.Phase]; !ok {
		return errors.New("lease phase is invalid")
	}
	if l.CreatedAt.IsZero() || l.UpdatedAt.Before(l.CreatedAt) || l.Deadline.IsZero() {
		return errors.New("lease timestamps are invalid")
	}
	return l.Ownership.Validate(installationID, storage)
}

func (l Lease) Transition(phase Phase, now time.Time, errorClass string) (Lease, error) {
	current, currentOK := phaseOrder[l.Phase]
	next, nextOK := phaseOrder[phase]
	if !currentOK || !nextOK {
		return Lease{}, errors.New("lease transition contains an unknown phase")
	}
	cleanupRestart := l.Phase == PhaseCleanupError && phase == PhaseStopping
	enteringCleanup := phase == PhaseStopping || phase == PhaseCleanupError
	if !cleanupRestart && !enteringCleanup && next < current {
		return Lease{}, fmt.Errorf("lease cannot move backward from %s to %s", l.Phase, phase)
	}
	if l.Phase == PhaseComplete && phase != PhaseComplete {
		return Lease{}, errors.New("completed lease cannot transition")
	}
	l.Phase = phase
	l.UpdatedAt = now.UTC()
	l.LastErrorClass = errorClass
	return l, nil
}

type LeaseStore struct {
	directory string
}

func NewLeaseStore(directory string) (*LeaseStore, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return nil, errors.New("lease directory must be absolute")
	}
	return &LeaseStore{directory: directory}, nil
}

func (s *LeaseStore) Create(lease Lease) error {
	if err := os.MkdirAll(s.directory, 0o700); err != nil {
		return err
	}
	if err := rejectSymlink(s.directory); err != nil {
		return err
	}
	data, err := json.Marshal(lease)
	if err != nil {
		return err
	}
	path := s.path(lease.Ownership.LeaseID)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return syncDirectory(s.directory)
}

func (s *LeaseStore) Save(lease Lease) error {
	data, err := json.Marshal(lease)
	if err != nil {
		return err
	}
	path := s.path(lease.Ownership.LeaseID)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("lease file is not a secure owner-only regular file")
	}
	temporaryPath := path + ".new"
	if info, statErr := os.Lstat(temporaryPath); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("stale lease temporary file is insecure")
		}
		if err := os.Remove(temporaryPath); err != nil {
			return err
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	temporary, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = temporary.Write(data); err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(temporaryPath)
		return closeErr
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(s.directory)
}

func (s *LeaseStore) Load(leaseID string) (Lease, error) {
	if !leasePattern.MatchString(leaseID) {
		return Lease{}, errors.New("lease identifier is invalid")
	}
	path := s.path(leaseID)
	info, err := os.Lstat(path)
	if err != nil {
		return Lease{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 64*1024 {
		return Lease{}, errors.New("lease file is not a secure bounded owner-only regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Lease{}, err
	}
	var lease Lease
	if err := decodeStrict(data, &lease); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

// List loads every durable lease in deterministic order. Unknown files are
// ignored, while malformed lease files fail recovery rather than disappearing.
func (s *LeaseStore) List(installationID, storage string) ([]Lease, error) {
	if err := rejectSymlink(s.directory); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return nil, err
	}
	leases := make([]Lease, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		leaseID := strings.TrimSuffix(entry.Name(), ".json")
		lease, err := s.Load(leaseID)
		if err != nil {
			return nil, fmt.Errorf("load lease %q: %w", leaseID, err)
		}
		if err := lease.Validate(installationID, storage); err != nil {
			return nil, fmt.Errorf("validate lease %q: %w", leaseID, err)
		}
		leases = append(leases, lease)
	}
	sort.Slice(leases, func(i, j int) bool {
		return leases[i].Ownership.LeaseID < leases[j].Ownership.LeaseID
	})
	return leases, nil
}

func (s *LeaseStore) path(leaseID string) string {
	return filepath.Join(s.directory, leaseID+".json")
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("lease directory is not a secure owner-only directory")
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
