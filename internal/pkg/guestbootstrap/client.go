// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package guestbootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const outputLimit = 256 * 1024

type Client struct {
	address string
	user    string
	timeout time.Duration
	auth    []ssh.AuthMethod

	mu      sync.Mutex
	hostKey ssh.PublicKey
}

func NewClient(address string, port int, user string, timeout time.Duration, auth ...ssh.AuthMethod) (*Client, error) {
	if net.ParseIP(address) == nil || port < 1 || port > 65535 || user == "" || timeout <= 0 || len(auth) == 0 {
		return nil, errors.New("guest bootstrap SSH configuration is invalid")
	}
	return &Client{
		address: net.JoinHostPort(address, strconv.Itoa(port)),
		user:    user, timeout: timeout, auth: auth,
	}, nil
}

func (c *Client) HostKey() ssh.PublicKey {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hostKey
}

func (c *Client) Run(ctx context.Context, command string, input []byte) ([]byte, error) {
	return c.run(ctx, command, input, nil)
}

func (c *Client) RunWithOutput(ctx context.Context, command string, input []byte, progress io.Writer) ([]byte, error) {
	return c.run(ctx, command, input, progress)
}

func (c *Client) run(ctx context.Context, command string, input []byte, progress io.Writer) ([]byte, error) {
	if command == "" || len(command) > 16*1024 {
		return nil, errors.New("guest bootstrap command is invalid")
	}
	config := &ssh.ClientConfig{
		User: c.user, Auth: c.auth, Timeout: c.timeout,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.hostKey != nil && !bytes.Equal(c.hostKey.Marshal(), key.Marshal()) {
				return errors.New("guest SSH host key changed during bootstrap")
			}
			c.hostKey = key
			return nil
		},
	}
	connection, err := ssh.Dial("tcp", c.address, config)
	if err != nil {
		return nil, fmt.Errorf("connect to bootstrap guest: %w", err)
	}
	defer connection.Close()
	session, err := connection.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	if input != nil {
		session.Stdin = bytes.NewReader(input)
	}
	var output limitedBuffer
	output.limit = outputLimit
	writer := io.Writer(&output)
	if progress != nil {
		writer = io.MultiWriter(&output, &lockedWriter{writer: progress})
	}
	session.Stdout, session.Stderr = writer, writer
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	select {
	case <-ctx.Done():
		_ = session.Close()
		return nil, ctx.Err()
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("guest bootstrap command failed: %w: %s", err, output.String())
		}
		return output.Bytes(), nil
	}
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
	mu     sync.Mutex
}

func (w *limitedBuffer) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := w.limit - w.buffer.Len()
	if remaining > 0 {
		_, _ = w.buffer.Write(value[:min(len(value), remaining)])
	}
	return len(value), nil
}

func (w *limitedBuffer) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buffer.Bytes()...)
}

func (w *limitedBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

var _ io.Writer = (*limitedBuffer)(nil)

type lockedWriter struct {
	writer io.Writer
	mu     sync.Mutex
}

func (w *lockedWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(value)
}
