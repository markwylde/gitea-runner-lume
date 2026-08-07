// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package container

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeRemoteClient struct {
	commands [][]string
	envs     []map[string]string
	closed   bool
}

func (*fakeRemoteClient) CopyFiles(context.Context, string, []*FileEntry) error { return nil }
func (*fakeRemoteClient) CopyTar(context.Context, string, io.Reader) error      { return nil }
func (*fakeRemoteClient) CopyDir(context.Context, string, string, bool) error   { return nil }
func (*fakeRemoteClient) Archive(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (f *fakeRemoteClient) Exec(_ context.Context, command []string, env map[string]string, _, _ string, _ io.Writer) error {
	f.commands = append(f.commands, append([]string(nil), command...))
	f.envs = append(f.envs, env)
	return nil
}
func (*fakeRemoteClient) Inspect(context.Context) (*Info, error) {
	return &Info{ID: "worker", State: StateRunning}, nil
}
func (f *fakeRemoteClient) Close(context.Context) error { f.closed = true; return nil }

func TestRemoteEnvironmentDelegatesStructuredExecution(t *testing.T) {
	client := &fakeRemoteClient{}
	environment := &RemoteEnvironment{
		Client: client, Path: "/runner/work/repo", Workdir: "/workspace/owner/repo",
		ActPath: "/runner/act", TmpDir: "/runner/tmp", ToolCache: "/runner/toolcache",
		Stdout: io.Discard,
	}
	command := []string{"/bin/bash", "-e", "/runner/work/repo/script.sh"}
	env := map[string]string{"CANARY": "value with spaces; $(touch nope)"}
	require.NoError(t, environment.Exec(command, env, "runner", environment.Path)(t.Context()))
	require.Equal(t, command, client.commands[0])
	require.Equal(t, env, client.envs[0])
	require.NoError(t, environment.Exec(command, env, "runner", "")(t.Context()))
	require.Equal(t, "/runner/work/repo/sub", environment.ToContainerPath("/workspace/owner/repo/sub"))
	require.NoError(t, environment.Remove()(t.Context()))
	require.True(t, client.closed)
}

func TestRemoteEnvironmentRejectsIncompleteConfiguration(t *testing.T) {
	environment := &RemoteEnvironment{Client: &fakeRemoteClient{}}
	require.Error(t, environment.Exec([]string{"echo"}, nil, "", "")(t.Context()))
}
