// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package guestagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitea.com/gitea/runner/internal/pkg/guestproto"
)

var sshUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

type SSHConfig struct {
	Executable     string
	IdentityFile   string
	KnownHostsFile string
	HostKeyAlias   string
	User           string
	Address        string
	Port           uint16
	ConnectTimeout time.Duration
	GuestBinary    string
	GuestRoot      string
}

type SSHSession struct {
	Client *Client
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *boundedBuffer
	once   sync.Once
}

func StartSSHSession(ctx context.Context, config SSHConfig, hello guestproto.Hello, hostPrivateKey ed25519.PrivateKey, guestPublicKey ed25519.PublicKey) (*SSHSession, error) {
	if err := validateSSHConfig(config); err != nil {
		return nil, err
	}
	arguments := []string{
		"-F", "/dev/null", "-T", "-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + config.KnownHostsFile,
		"-o", "HostKeyAlias=" + config.HostKeyAlias,
		"-o", "ConnectTimeout=" + strconv.Itoa(max(1, int(config.ConnectTimeout.Seconds()))),
		"-i", config.IdentityFile, "-p", strconv.Itoa(int(config.Port)),
		config.User + "@" + config.Address,
		config.GuestBinary, "guest",
		"--root", config.GuestRoot,
		"--installation", hello.InstallationID,
		"--lease", hello.LeaseID,
		"--worker", hello.WorkerID,
		"--task", strconv.FormatInt(hello.TaskID, 10),
		"--nonce", hello.Nonce,
		"--revision", hello.Revision,
	}
	command := exec.CommandContext(ctx, config.Executable, arguments...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr := &boundedBuffer{limit: 64 * 1024}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start pinned SSH guest session: %w", err)
	}
	client, err := NewClient(stdout, stdin, hello, hostPrivateKey, guestPublicKey)
	if err != nil {
		_ = stdin.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("authenticate SSH guest session: %w: %s", err, stderr.String())
	}
	client.SetGuestIdentity(config.User, "/Users/"+config.User)
	client.SetInterrupt(func() {
		_ = stdin.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	})
	client.SetDiagnostic(func(readErr error) error {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return fmt.Errorf("guest SSH stream failed: %w: %s", readErr, detail)
		}
		return fmt.Errorf("guest SSH stream failed: %w", readErr)
	})
	return &SSHSession{Client: client, cmd: command, stdin: stdin, stderr: stderr}, nil
}

func (s *SSHSession) Close(ctx context.Context) error {
	var result error
	s.once.Do(func() {
		if err := s.Client.Close(ctx); err != nil {
			result = err
		}
		_ = s.stdin.Close()
		if err := s.cmd.Wait(); err != nil {
			waitErr := fmt.Errorf("guest SSH process exited: %w: %s", err, s.stderr.String())
			result = errors.Join(result, waitErr)
		}
	})
	return result
}

func validateSSHConfig(config SSHConfig) error {
	for name, path := range map[string]string{
		"executable": config.Executable, "identity": config.IdentityFile,
		"known hosts": config.KnownHostsFile, "guest binary": config.GuestBinary,
		"guest root": config.GuestRoot,
	} {
		if path == "" || path[0] != '/' {
			return fmt.Errorf("SSH %s path must be absolute", name)
		}
	}
	knownHosts, err := os.Lstat(config.KnownHostsFile)
	if err != nil {
		return fmt.Errorf("inspect SSH known-hosts file: %w", err)
	}
	if !knownHosts.Mode().IsRegular() || knownHosts.Mode().Perm()&0o022 != 0 {
		return errors.New("SSH known-hosts must be a regular file not writable by group or others")
	}
	if !sshUserPattern.MatchString(config.User) {
		return errors.New("SSH guest user is invalid")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`).MatchString(config.HostKeyAlias) {
		return errors.New("SSH host-key alias is invalid")
	}
	address, err := netip.ParseAddr(config.Address)
	if err != nil || address.IsUnspecified() || address.IsMulticast() || address.IsLoopback() {
		return errors.New("SSH guest address is invalid")
	}
	if config.Port == 0 || config.ConnectTimeout <= 0 {
		return errors.New("SSH port or timeout is invalid")
	}
	for _, path := range []string{config.IdentityFile, config.KnownHostsFile} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("SSH identity inputs must be regular files")
		}
	}
	return nil
}

type boundedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	limit  int
}

func (b *boundedBuffer) Write(content []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(content) < remaining {
			remaining = len(content)
		}
		_, _ = b.buffer.Write(content[:remaining])
	}
	return len(content), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
