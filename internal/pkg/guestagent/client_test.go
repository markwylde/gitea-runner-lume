// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package guestagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitea.com/gitea/runner/act/container"
	"gitea.com/gitea/runner/internal/pkg/guestproto"

	"github.com/stretchr/testify/require"
)

func TestAuthenticatedClientDrivesRemoteEnvironment(t *testing.T) {
	root := t.TempDir()
	hostConnection, guestConnection := net.Pipe()
	t.Cleanup(func() { _ = hostConnection.Close(); _ = guestConnection.Close() })
	hostPublic, hostPrivate, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	guestPublic, guestPrivate, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	expected := sessionHello()
	server, err := NewServer(root, guestConnection, guestConnection, expected, hostPublic, guestPrivate)
	require.NoError(t, err)
	serverResult := make(chan error, 1)
	go func() { serverResult <- server.Serve(context.Background()) }()

	client, err := NewClient(hostConnection, hostConnection, expected, hostPrivate, guestPublic)
	require.NoError(t, err)
	environment := &container.RemoteEnvironment{
		Client: client, Path: root, Workdir: root, ActPath: filepath.Join(root, "act"),
		TmpDir: filepath.Join(root, "tmp"), ToolCache: filepath.Join(root, "toolcache"),
		Stdout: &bytes.Buffer{},
	}
	require.NoError(t, environment.Copy(root, &container.FileEntry{
		Name: "script.sh", Mode: 0o700, Body: "#!/bin/sh\nprintf integrated",
	})(t.Context()))
	var output bytes.Buffer
	environment.Stdout = &output
	require.NoError(t, environment.Exec([]string{filepath.Join(root, "script.sh")}, map[string]string{"PATH": "/usr/bin:/bin"}, "runner", root)(t.Context()))
	require.Equal(t, "integrated", output.String())
	require.NoError(t, environment.Close()(t.Context()))
	require.NoError(t, <-serverResult)
	_, err = os.Stat(filepath.Join(root, "script.sh"))
	require.NoError(t, err)
}

func TestClientRejectsWrongGuestIdentity(t *testing.T) {
	root := t.TempDir()
	hostConnection, guestConnection := net.Pipe()
	t.Cleanup(func() { _ = hostConnection.Close(); _ = guestConnection.Close() })
	hostPublic, hostPrivate, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	_, guestPrivate, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	wrongGuestPublic, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	expected := sessionHello()
	server, err := NewServer(root, guestConnection, guestConnection, expected, hostPublic, guestPrivate)
	require.NoError(t, err)
	go func() { _ = server.Serve(context.Background()) }()
	_, err = NewClient(hostConnection, hostConnection, expected, hostPrivate, wrongGuestPublic)
	require.Error(t, err)
}

func TestClientCancellationInterruptsBlockedGuestRead(t *testing.T) {
	hostConnection, guestConnection := net.Pipe()
	t.Cleanup(func() { _ = hostConnection.Close(); _ = guestConnection.Close() })
	client := &Client{reader: guestproto.NewReader(hostConnection), writer: guestproto.NewWriter(hostConnection)}
	client.SetInterrupt(func() { _ = hostConnection.Close() })
	go func() { _, _ = io.Copy(io.Discard, guestConnection) }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := client.Exec(ctx, []string{"/bin/sleep", "10"}, nil, "", "/tmp", nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), time.Second)
}
