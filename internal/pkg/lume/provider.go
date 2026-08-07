// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package lume

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	defaultOutputLimit = 256 * 1024
	maxIdentifierLen   = 63
)

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

type VMState string

const (
	StateStopped      VMState = "stopped"
	StateRunning      VMState = "running"
	StateProvisioning VMState = "provisioning"
	StateSuspended    VMState = "suspended"
)

type VM struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	State                 VMState  `json:"state"`
	Storage               string   `json:"storage"`
	IPAddress             string   `json:"ip_address,omitempty"`
	ProvisioningOperation string   `json:"provisioning_operation,omitempty"`
	DownloadProgress      *float64 `json:"download_progress,omitempty"`
	SSHAvailable          bool     `json:"ssh_available,omitempty"`
}

type lumeDiskSize struct {
	Allocated uint64 `json:"allocated"`
	Total     uint64 `json:"total"`
}

type lumeSharedDirectory struct {
	HostPath string `json:"hostPath"`
	Tag      string `json:"tag"`
	ReadOnly bool   `json:"readOnly"`
}

// lumeVMDetails mirrors Lume 0.4's documented JSON output. Conversion keeps
// provider details out of the rest of the runner.
type lumeVMDetails struct {
	Name                  string                `json:"name"`
	OS                    string                `json:"os"`
	CPUCount              int                   `json:"cpuCount"`
	MemorySize            uint64                `json:"memorySize"`
	DiskSize              lumeDiskSize          `json:"diskSize"`
	Display               string                `json:"display"`
	Status                string                `json:"status"`
	ProvisioningOperation *string               `json:"provisioningOperation"`
	VNCURL                *string               `json:"vncUrl"`
	IPAddress             *string               `json:"ipAddress"`
	SSHAvailable          *bool                 `json:"sshAvailable"`
	LocationName          string                `json:"locationName"`
	SharedDirectories     []lumeSharedDirectory `json:"sharedDirectories"`
	NetworkMode           *string               `json:"networkMode"`
	DownloadProgress      *float64              `json:"downloadProgress"`
}

type Storage struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Path           string `json:"path"`
	AvailableBytes int64  `json:"available_bytes"`
}

type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

type CommandRunner interface {
	Run(ctx context.Context, executable string, args []string, outputLimit int) (Result, error)
}

type Provider struct {
	executable string
	storage    string
	timeout    time.Duration
	runner     CommandRunner
}

func NewProvider(executable, storage string, timeout time.Duration, runner CommandRunner) (*Provider, error) {
	if executable == "" || !strings.HasPrefix(executable, "/") {
		return nil, errors.New("Lume executable must be an absolute path")
	}
	if !validIdentifier(storage) {
		return nil, errors.New("Lume storage identifier is invalid")
	}
	if timeout <= 0 {
		return nil, errors.New("Lume command timeout must be positive")
	}
	if runner == nil {
		runner = OSCommandRunner{}
	}
	return &Provider{executable: executable, storage: storage, timeout: timeout, runner: runner}, nil
}

func (p *Provider) Version(ctx context.Context) (string, error) {
	result, err := p.run(ctx, "--version")
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(result.Stdout))
	if version == "" || len(version) > 128 {
		return "", errors.New("Lume returned an invalid version")
	}
	return version, nil
}

func (p *Provider) List(ctx context.Context) ([]VM, error) {
	result, err := p.run(ctx, "ls", "--format", "json", "--storage", p.storage)
	if err != nil {
		return nil, err
	}
	var details []lumeVMDetails
	if err := decodeStrict(result.Stdout, &details); err != nil {
		return nil, fmt.Errorf("decode Lume VM list: %w", err)
	}
	vms := make([]VM, len(details))
	for i := range details {
		vms[i], err = convertVM(details[i])
		if err != nil {
			return nil, fmt.Errorf("invalid VM at index %d: %w", i, err)
		}
	}
	for i := range vms {
		if err := validateVM(vms[i]); err != nil {
			return nil, fmt.Errorf("invalid VM at index %d: %w", i, err)
		}
	}
	return vms, nil
}

func (p *Provider) Get(ctx context.Context, id string) (VM, error) {
	if !validIdentifier(id) {
		return VM{}, errors.New("VM identifier is invalid")
	}
	result, err := p.run(ctx, "get", id, "--format", "json", "--storage", p.storage)
	if err != nil {
		return VM{}, err
	}
	var details []lumeVMDetails
	if err := decodeStrict(result.Stdout, &details); err != nil {
		return VM{}, fmt.Errorf("decode Lume VM: %w", err)
	}
	if len(details) != 1 {
		return VM{}, errors.New("Lume get returned an unexpected VM count")
	}
	vm, err := convertVM(details[0])
	if err != nil {
		return VM{}, err
	}
	if err := validateVM(vm); err != nil {
		return VM{}, err
	}
	return vm, nil
}

func (p *Provider) Clone(ctx context.Context, base, target string) error {
	if !validIdentifier(base) || !validIdentifier(target) || base == target {
		return errors.New("clone VM identifiers are invalid")
	}
	_, err := p.run(ctx, "clone", base, target, "--source-storage", p.storage, "--dest-storage", p.storage)
	return err
}

func (p *Provider) Configure(ctx context.Context, id string, cpu, memoryGB, diskGB int) error {
	if !validIdentifier(id) || cpu < 1 || memoryGB < 1 || diskGB < 1 {
		return errors.New("VM configuration is invalid")
	}
	// A worker clone inherits the validated base image disk. Lume 0.5.1 treats
	// setting a disk to its current size as a forbidden shrink, so only mutable
	// compute resources belong in the post-clone configuration call.
	_, err := p.run(ctx, "set", id, "--cpu", fmt.Sprint(cpu), "--memory", fmt.Sprintf("%dGB", memoryGB), "--storage", p.storage)
	return err
}

func (p *Provider) Create(ctx context.Context, id, ipsw, unattended string, cpu, memoryGB, diskGB int) error {
	if !validIdentifier(id) || (ipsw != "latest" && !strings.HasPrefix(ipsw, "/")) || unattended == "" {
		return errors.New("VM creation parameters are invalid")
	}
	_, err := p.run(ctx, "create", id, "--os", "macOS", "--cpu", fmt.Sprint(cpu), "--memory", fmt.Sprintf("%dGB", memoryGB), "--disk-size", fmt.Sprintf("%dGB", diskGB), "--ipsw", ipsw, "--storage", p.storage, "--unattended", unattended, "--no-display")
	return err
}

func (p *Provider) Start(ctx context.Context, id string) error {
	if !validIdentifier(id) {
		return errors.New("VM identifier is invalid")
	}
	_, err := p.run(ctx, "run", id, "--no-display", "--storage", p.storage)
	return err
}

func (p *Provider) Stop(ctx context.Context, id string, force bool) error {
	if !validIdentifier(id) {
		return errors.New("VM identifier is invalid")
	}
	args := []string{"stop", id, "--storage", p.storage}
	_ = force // Lume 0.4 stop has no force flag; process termination is separate.
	_, err := p.run(ctx, args...)
	return err
}

func (p *Provider) DeleteOwned(ctx context.Context, evidence OwnershipEvidence, installationID string) error {
	if err := evidence.Validate(installationID, p.storage); err != nil {
		return err
	}
	vm, err := p.Get(ctx, evidence.VMID)
	if err != nil {
		return fmt.Errorf("inspect VM before deletion: %w", err)
	}
	if vm.ID != evidence.VMID || vm.Name != evidence.VMName || vm.Storage != evidence.Storage {
		return errors.New("provider VM does not match durable ownership evidence")
	}
	if vm.State != StateStopped {
		return errors.New("owned VM must be stopped before deletion")
	}
	if _, err = p.run(ctx, "delete", evidence.VMID, "--force", "--storage", p.storage); err != nil {
		return err
	}
	vms, err := p.List(ctx)
	if err != nil {
		return fmt.Errorf("verify VM deletion: %w", err)
	}
	for _, candidate := range vms {
		if candidate.ID == evidence.VMID {
			return errors.New("owned VM still exists after deletion")
		}
	}
	return nil
}

func (p *Provider) run(parent context.Context, args ...string) (Result, error) {
	ctx, cancel := context.WithTimeout(parent, p.timeout)
	defer cancel()
	result, err := p.runner.Run(ctx, p.executable, args, defaultOutputLimit)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{}, fmt.Errorf("Lume command timed out after %s", p.timeout)
	}
	if err != nil {
		return Result{}, err
	}
	if result.ExitCode != 0 {
		return Result{}, fmt.Errorf("Lume command failed with exit code %d: %s", result.ExitCode, sanitizedError(result.Stderr))
	}
	return result, nil
}

func validateVM(vm VM) error {
	if !validIdentifier(vm.ID) || !validIdentifier(vm.Name) || !validIdentifier(vm.Storage) {
		return errors.New("VM identity fields are invalid")
	}
	switch vm.State {
	case StateStopped, StateRunning, StateProvisioning, StateSuspended:
	default:
		return fmt.Errorf("unknown VM state %q", vm.State)
	}
	if vm.IPAddress != "" {
		if _, err := netip.ParseAddr(vm.IPAddress); err != nil {
			return errors.New("VM IP address is invalid")
		}
	}
	if len(vm.ProvisioningOperation) > 128 || strings.ContainsAny(vm.ProvisioningOperation, "\r\n\x00") {
		return errors.New("VM provisioning operation is invalid")
	}
	if vm.DownloadProgress != nil && (*vm.DownloadProgress < 0 || *vm.DownloadProgress > 100) {
		return errors.New("VM download progress is invalid")
	}
	return nil
}

func convertVM(details lumeVMDetails) (VM, error) {
	state := VMState(details.Status)
	if state == "pulling" {
		state = StateProvisioning
	}
	ip := ""
	if details.IPAddress != nil {
		ip = *details.IPAddress
	}
	operation := ""
	if details.ProvisioningOperation != nil {
		operation = *details.ProvisioningOperation
	}
	vm := VM{
		ID: details.Name, Name: details.Name, State: state, Storage: details.LocationName,
		IPAddress: ip, ProvisioningOperation: operation, DownloadProgress: details.DownloadProgress,
	}
	if details.SSHAvailable != nil {
		vm.SSHAvailable = *details.SSHAvailable
	}
	if err := validateVM(vm); err != nil {
		return VM{}, err
	}
	if details.OS != "macOS" || details.CPUCount < 1 || details.MemorySize == 0 || details.DiskSize.Total == 0 {
		return VM{}, errors.New("Lume VM resource fields are invalid")
	}
	if len(details.SharedDirectories) != 0 {
		return VM{}, errors.New("Lume VM host shared directories are forbidden")
	}
	return vm, nil
}

func validIdentifier(value string) bool {
	return len(value) <= maxIdentifierLen && identifierPattern.MatchString(value)
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func sanitizedError(stderr []byte) string {
	value := strings.TrimSpace(string(stderr))
	if len(value) > 512 {
		value = value[:512]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return ' '
		}
		return r
	}, value)
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = w.buffer.Write(p[:remaining])
		return len(p), nil
	}
	return w.buffer.Write(p)
}

type OSCommandRunner struct{}

func (OSCommandRunner) Run(ctx context.Context, executable string, args []string, outputLimit int) (Result, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = outputLimit, outputLimit
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := Result{Stdout: stdout.buffer.Bytes(), Stderr: stderr.buffer.Bytes()}
	if err == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return Result{}, fmt.Errorf("start Lume command: %w", err)
}

type ManagedProcess struct {
	command *exec.Cmd
	stderr  *limitedBuffer
	once    sync.Once
}

func (p *Provider) StartManaged(ctx context.Context, id string) (*ManagedProcess, error) {
	if !validIdentifier(id) {
		return nil, errors.New("VM identifier is invalid")
	}
	command := exec.CommandContext(ctx, p.executable, "run", id, "--no-display", "--storage", p.storage)
	stderr := &limitedBuffer{limit: defaultOutputLimit}
	command.Stdout = io.Discard
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start Lume VM process: %w", err)
	}
	return &ManagedProcess{command: command, stderr: stderr}, nil
}

func (p *ManagedProcess) Wait() error {
	var result error
	p.once.Do(func() {
		if err := p.command.Wait(); err != nil {
			result = fmt.Errorf("Lume VM process exited: %w: %s", err, sanitizedError(p.stderr.buffer.Bytes()))
		}
	})
	return result
}

func (p *ManagedProcess) Kill() error {
	if p == nil || p.command == nil || p.command.Process == nil {
		return nil
	}
	return p.command.Process.Kill()
}
