// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package guestagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"gitea.com/gitea/runner/act/container"
	"gitea.com/gitea/runner/internal/pkg/guestproto"

	"github.com/stretchr/testify/require"
)

func TestServerCopiesExecutesAndClosesInsideGuestRoot(t *testing.T) {
	root := t.TempDir()
	var input, output bytes.Buffer
	requests := guestproto.NewWriter(&input)
	expected, hostPublic, guestPrivate := authenticatedHello(t, requests)
	require.NoError(t, requests.Write("copy", guestproto.CopyRequest{
		Destination: root,
		Files:       []guestproto.File{{Name: "script.sh", Mode: 0o700, Body: []byte("#!/bin/sh\nprintf guest-output")}},
	}))
	require.NoError(t, requests.Write("exec", guestproto.ExecRequest{
		Command: []string{filepath.Join(root, "script.sh")},
		Env:     map[string]string{"PATH": "/usr/bin:/bin"}, Workdir: root,
	}))
	require.NoError(t, requests.Write("close", struct{}{}))

	server, err := NewServer(root, &input, &output, expected, hostPublic, guestPrivate)
	require.NoError(t, err)
	require.NoError(t, server.Serve(context.Background()))
	content, err := os.ReadFile(filepath.Join(root, "script.sh"))
	require.NoError(t, err)
	require.Contains(t, string(content), "guest-output")

	responses := guestproto.NewReader(&output)
	var guestHello guestproto.Hello
	require.NoError(t, responses.Read("guest_hello", &guestHello))
	require.NoError(t, responses.Read("copied", &struct{}{}))
	var event guestproto.Data
	require.NoError(t, responses.Read("output", &event))
	require.Equal(t, []byte("guest-output"), event.Bytes)
	var result guestproto.ExecResult
	require.NoError(t, responses.Read("exec_result", &result))
	require.Zero(t, result.ExitCode)
	require.NoError(t, responses.Read("closed", &struct{}{}))
}

func TestEnvironmentNameAllowsActionInputHyphens(t *testing.T) {
	require.True(t, envNamePattern.MatchString("INPUT_SET-SAFE-DIRECTORY"))
	require.False(t, envNamePattern.MatchString("INPUT_BAD=VALUE"))
	require.False(t, envNamePattern.MatchString(""))
}

func TestResolveExecutableUsesRequestedAbsolutePath(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "node")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o700))

	resolved, err := resolveExecutable("node", "relative:"+directory)
	require.NoError(t, err)
	require.Equal(t, executable, resolved)
	_, err = resolveExecutable("missing", directory)
	require.Error(t, err)
}

func TestGuestExecutionPathPreservesRequestedEntriesAndAddsImageTools(t *testing.T) {
	requested := "/opt/action/bin:/usr/bin"
	actual := guestExecutionPath(requested)
	require.Equal(t, []string{
		"/opt/action/bin", "/usr/bin", "/usr/local/bin", "/bin", "/usr/sbin", "/sbin",
	}, filepath.SplitList(actual))
	require.Equal(t, 1, strings.Count(actual, "/usr/bin"))
}

func TestGuestExecutionPathSuppliesToolsWhenRequestOmitsPath(t *testing.T) {
	require.Equal(t,
		[]string{"/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"},
		filepath.SplitList(guestExecutionPath("")),
	)
	require.Equal(t, container.GuestImagePath, guestExecutionPath(""))
}

func TestGuestExecutionPathDropsControllerHomeAndKeepsWorkflowExtras(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	requested := strings.Join([]string{
		filepath.Join(home, ".local/bin"),
		"/opt/action/bin",
		"/usr/bin",
		"relative/bin",
	}, string(filepath.ListSeparator))
	require.Equal(t, []string{
		"/opt/action/bin", "/usr/bin", "/usr/local/bin", "/bin", "/usr/sbin", "/sbin",
	}, filepath.SplitList(guestExecutionPath(requested)))
}

func TestGuestProcessEnvReplacesControllerHomeWithGuestIdentity(t *testing.T) {
	environment, err := guestProcessEnv(map[string]string{
		"HOME": "/Users/mark",
		"USER": "mark",
		"CI":   "true",
	})
	require.NoError(t, err)
	values := map[string]string{}
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		require.True(t, ok)
		values[name] = value
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.Equal(t, home, values["HOME"])
	require.Equal(t, "true", values["CI"])
	require.Equal(t, container.GuestImagePath, values["PATH"])
}

func TestStripHostIdentityEnvDropsControllerHome(t *testing.T) {
	env := map[string]string{
		"HOME":          "/Users/mark",
		"USER":          "mark",
		"LOGNAME":       "mark",
		"TMPDIR":        "/var/folders/xx/T",
		"SSH_AUTH_SOCK": "/private/tmp/com.apple.launchd.xxx/Listeners",
		"CI":            "true",
		"PATH":          "/usr/bin",
	}
	stripHostIdentityEnv(env)
	require.NotContains(t, env, "HOME")
	require.NotContains(t, env, "USER")
	require.NotContains(t, env, "LOGNAME")
	require.NotContains(t, env, "TMPDIR")
	require.NotContains(t, env, "SSH_AUTH_SOCK")
	require.Equal(t, "true", env["CI"])
	require.Equal(t, "/usr/bin", env["PATH"])
}

func TestClientReplacesControllerHomeWithGuestIdentity(t *testing.T) {
	env := map[string]string{
		"HOME": "/Users/mark",
		"USER": "mark",
		"CI":   "true",
		"PATH": "/opt/action/bin",
	}
	applyGuestIdentity(env, "lume", "/Users/lume")
	require.Equal(t, "/Users/lume", env["HOME"])
	require.Equal(t, "lume", env["USER"])
	require.Equal(t, "lume", env["LOGNAME"])
	require.Equal(t, "true", env["CI"])
	require.Equal(t, []string{
		"/opt/action/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin",
	}, filepath.SplitList(env["PATH"]))
}

func TestServerRejectsPathEscapeBeforeWriting(t *testing.T) {
	root := t.TempDir()
	var input, output bytes.Buffer
	requests := guestproto.NewWriter(&input)
	expected, hostPublic, guestPrivate := authenticatedHello(t, requests)
	require.NoError(t, requests.Write("copy", guestproto.CopyRequest{
		Destination: filepath.Dir(root),
		Files:       []guestproto.File{{Name: "escaped", Mode: 0o600, Body: []byte("no")}},
	}))
	server, err := NewServer(root, &input, &output, expected, hostPublic, guestPrivate)
	require.NoError(t, err)
	err = server.Serve(context.Background())
	require.Error(t, err)
	require.False(t, errors.Is(err, io.EOF))
	_, err = os.Stat(filepath.Join(filepath.Dir(root), "escaped"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestServerRejectsUnauthenticatedHostBeforeOperations(t *testing.T) {
	root := t.TempDir()
	var input, output bytes.Buffer
	hostPublic, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	_, guestPrivate, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	requests := guestproto.NewWriter(&input)
	require.NoError(t, requests.Write("copy", guestproto.CopyRequest{}))
	server, err := NewServer(root, &input, &output, sessionHello(), hostPublic, guestPrivate)
	require.NoError(t, err)
	require.Error(t, server.Serve(context.Background()))
	require.Empty(t, output.Bytes())
}

func TestServerCancellationKillsGuestProcessGroup(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "child.pid")
	var input, output bytes.Buffer
	requests := guestproto.NewWriter(&input)
	expected, hostPublic, guestPrivate := authenticatedHello(t, requests)
	require.NoError(t, requests.Write("exec", guestproto.ExecRequest{
		Command: []string{"/bin/sh", "-c", "sleep 60 & echo $! > child.pid; wait"},
		Env:     map[string]string{"PATH": "/usr/bin:/bin"}, Workdir: root,
	}))

	server, err := NewServer(root, &input, &output, expected, hostPublic, guestPrivate)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- server.Serve(ctx) }()

	var childPID int
	require.Eventually(t, func() bool {
		data, readErr := os.ReadFile(pidFile)
		if readErr != nil {
			return false
		}
		childPID, readErr = strconv.Atoi(strings.TrimSpace(string(data)))
		return readErr == nil && childPID > 0
	}, time.Second, 10*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	require.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(childPID, 0), syscall.ESRCH)
	}, time.Second, 10*time.Millisecond)
}

func authenticatedHello(t *testing.T, writer *guestproto.Writer) (guestproto.Hello, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	hostPublic, hostPrivate, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	_, guestPrivate, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	expected := sessionHello()
	signed, err := guestproto.SignHello(expected, hostPrivate)
	require.NoError(t, err)
	require.NoError(t, writer.Write("host_hello", signed))
	return expected, hostPublic, guestPrivate
}

func sessionHello() guestproto.Hello {
	return guestproto.Hello{
		InstallationID: "0123456789abcdef0123456789abcdef",
		LeaseID:        "abcdef0123456789abcdef0123456789",
		WorkerID:       "11111111111111111111111111111111",
		TaskID:         42,
		Nonce:          "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Revision:       "test-revision",
	}
}
