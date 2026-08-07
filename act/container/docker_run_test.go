// Copyright 2022 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitea.com/gitea/runner/act/common"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestDocker(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	client, err := GetDockerClient(ctx)
	require.NoError(t, err)
	defer client.Close()

	dockerBuild := NewDockerBuildExecutor(NewDockerBuildExecutorInput{
		ContextDir: "testdata",
		ImageTag:   "envmergetest",
	})

	err = dockerBuild(ctx)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act

	cr := &containerReference{
		cli: client,
		input: &NewContainerInput{
			Image: "envmergetest",
		},
	}
	env := map[string]string{
		"PATH":         "/usr/local/bin:/usr/bin:/usr/sbin:/bin:/sbin",
		"RANDOM_VAR":   "WITH_VALUE",
		"ANOTHER_VAR":  "",
		"CONFLICT_VAR": "I_EXIST_IN_MULTIPLE_PLACES",
	}

	envExecutor := cr.extractFromImageEnv(&env)
	err = envExecutor(ctx)
	assert.NoError(t, err) //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, map[string]string{
		"PATH":            "/usr/local/bin:/usr/bin:/usr/sbin:/bin:/sbin:/this/path/does/not/exists/anywhere:/this/either",
		"RANDOM_VAR":      "WITH_VALUE",
		"ANOTHER_VAR":     "",
		"SOME_RANDOM_VAR": "",
		"ANOTHER_ONE":     "BUT_I_HAVE_VALUE",
		"CONFLICT_VAR":    "I_EXIST_IN_MULTIPLE_PLACES",
	}, env)
}

type mockDockerClient struct {
	mobyclient.APIClient
	mock.Mock
}

func (m *mockDockerClient) ExecCreate(ctx context.Context, id string, opts mobyclient.ExecCreateOptions) (mobyclient.ExecCreateResult, error) {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(mobyclient.ExecCreateResult), args.Error(1)
}

func (m *mockDockerClient) ExecAttach(ctx context.Context, id string, opts mobyclient.ExecAttachOptions) (mobyclient.ExecAttachResult, error) {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(mobyclient.ExecAttachResult), args.Error(1)
}

func (m *mockDockerClient) ExecInspect(ctx context.Context, execID string, opts mobyclient.ExecInspectOptions) (mobyclient.ExecInspectResult, error) {
	args := m.Called(ctx, execID, opts)
	return args.Get(0).(mobyclient.ExecInspectResult), args.Error(1)
}

func (m *mockDockerClient) ContainerStatPath(ctx context.Context, containerID string, opts mobyclient.ContainerStatPathOptions) (mobyclient.ContainerStatPathResult, error) {
	args := m.Called(ctx, containerID, opts)
	return args.Get(0).(mobyclient.ContainerStatPathResult), args.Error(1)
}

func (m *mockDockerClient) ContainerAttach(ctx context.Context, containerID string, opts mobyclient.ContainerAttachOptions) (mobyclient.ContainerAttachResult, error) {
	args := m.Called(ctx, containerID, opts)
	return args.Get(0).(mobyclient.ContainerAttachResult), args.Error(1)
}

func (m *mockDockerClient) ContainerWait(ctx context.Context, containerID string, opts mobyclient.ContainerWaitOptions) mobyclient.ContainerWaitResult {
	args := m.Called(ctx, containerID, opts)
	return args.Get(0).(mobyclient.ContainerWaitResult)
}

func (m *mockDockerClient) CopyToContainer(ctx context.Context, id string, options mobyclient.CopyToContainerOptions) (mobyclient.CopyToContainerResult, error) {
	args := m.Called(ctx, id, options)
	return args.Get(0).(mobyclient.CopyToContainerResult), args.Error(1)
}

func (m *mockDockerClient) ContainerInspect(ctx context.Context, id string, opts mobyclient.ContainerInspectOptions) (mobyclient.ContainerInspectResult, error) {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(mobyclient.ContainerInspectResult), args.Error(1)
}

func (m *mockDockerClient) ContainerList(ctx context.Context, opts mobyclient.ContainerListOptions) (mobyclient.ContainerListResult, error) {
	args := m.Called(ctx, opts)
	return args.Get(0).(mobyclient.ContainerListResult), args.Error(1)
}

func (m *mockDockerClient) ContainerRemove(ctx context.Context, id string, opts mobyclient.ContainerRemoveOptions) (mobyclient.ContainerRemoveResult, error) {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(mobyclient.ContainerRemoveResult), args.Error(1)
}

func (m *mockDockerClient) ContainerKill(ctx context.Context, id string, opts mobyclient.ContainerKillOptions) (mobyclient.ContainerKillResult, error) {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(mobyclient.ContainerKillResult), args.Error(1)
}

func (m *mockDockerClient) NetworkList(ctx context.Context, opts mobyclient.NetworkListOptions) (mobyclient.NetworkListResult, error) {
	args := m.Called(ctx, opts)
	return args.Get(0).(mobyclient.NetworkListResult), args.Error(1)
}

func (m *mockDockerClient) NetworkInspect(ctx context.Context, id string, opts mobyclient.NetworkInspectOptions) (mobyclient.NetworkInspectResult, error) {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(mobyclient.NetworkInspectResult), args.Error(1)
}

func (m *mockDockerClient) NetworkRemove(ctx context.Context, id string, opts mobyclient.NetworkRemoveOptions) (mobyclient.NetworkRemoveResult, error) {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(mobyclient.NetworkRemoveResult), args.Error(1)
}

type endlessReader struct {
	io.Reader
}

func (r endlessReader) Read(_ []byte) (n int, err error) {
	return 1, nil
}

type mockConn struct {
	net.Conn
	mock.Mock
}

func (m *mockConn) Write(b []byte) (n int, err error) {
	args := m.Called(b)
	return args.Int(0), args.Error(1)
}

func (m *mockConn) Close() (err error) {
	return nil
}

func TestDockerExecAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	conn := &mockConn{}
	conn.On("Write", mock.AnythingOfType("[]uint8")).Return(1, nil)

	client := &mockDockerClient{}
	client.On("ExecCreate", ctx, "123", mock.AnythingOfType("client.ExecCreateOptions")).Return(mobyclient.ExecCreateResult{ID: "id"}, nil)
	client.On("ExecAttach", ctx, "id", mock.AnythingOfType("client.ExecAttachOptions")).Return(mobyclient.ExecAttachResult{
		HijackedResponse: mobyclient.HijackedResponse{
			Conn:   conn,
			Reader: bufio.NewReader(endlessReader{}),
		},
	}, nil)

	cr := &containerReference{
		id:  "123",
		cli: client,
		input: &NewContainerInput{
			Image: "image",
		},
	}

	channel := make(chan error)

	go func() {
		channel <- cr.exec([]string{""}, map[string]string{}, "user", "workdir")(ctx)
	}()

	time.Sleep(500 * time.Millisecond)

	cancel()

	err := <-channel
	assert.ErrorIs(t, err, context.Canceled) //nolint:testifylint // pre-existing issue from nektos/act

	conn.AssertExpectations(t)
	client.AssertExpectations(t)
}

func TestDockerExecFailure(t *testing.T) {
	ctx := context.Background()

	conn := &mockConn{}

	client := &mockDockerClient{}
	client.On("ExecCreate", ctx, "123", mock.AnythingOfType("client.ExecCreateOptions")).Return(mobyclient.ExecCreateResult{ID: "id"}, nil)
	client.On("ExecAttach", ctx, "id", mock.AnythingOfType("client.ExecAttachOptions")).Return(mobyclient.ExecAttachResult{
		HijackedResponse: mobyclient.HijackedResponse{
			Conn:   conn,
			Reader: bufio.NewReader(strings.NewReader("output")),
		},
	}, nil)
	client.On("ExecInspect", ctx, "id", mobyclient.ExecInspectOptions{}).Return(mobyclient.ExecInspectResult{
		ExitCode: 1,
	}, nil)

	cr := &containerReference{
		id:  "123",
		cli: client,
		input: &NewContainerInput{
			Image: "image",
		},
	}

	err := cr.exec([]string{""}, map[string]string{}, "user", "workdir")(ctx)
	var exitErr ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, ExitCodeError(1), exitErr)
	assert.Equal(t, "Process completed with exit code 1.", err.Error())

	conn.AssertExpectations(t)
	client.AssertExpectations(t)
}

// stdcopyFrame wraps payload in a single Docker multiplexed-stream frame, the
// format StdCopy expects: an 8-byte header (stream type + 4-byte big-endian
// length) followed by the payload.
func stdcopyFrame(stream stdcopy.StdType, payload string) []byte {
	b := make([]byte, 8+len(payload))
	b[0] = byte(stream)
	binary.BigEndian.PutUint32(b[4:8], uint32(len(payload)))
	copy(b[8:], payload)
	return b
}

// TestDockerAttachFlushesTrailingLine verifies that wait() blocks until the
// attach() streaming goroutine has drained and flushed the container's output,
// so a final line without a trailing newline is not lost.
func TestDockerAttachFlushesTrailingLine(t *testing.T) {
	ctx := context.Background()

	framed := bytes.NewBuffer(stdcopyFrame(stdcopy.Stdout, "line one\nlast line without newline"))

	var lines []string
	logWriter := common.NewLineWriter(func(s string) bool {
		lines = append(lines, s)
		return true
	})

	client := &mockDockerClient{}
	client.On("ContainerAttach", ctx, "123", mock.AnythingOfType("client.ContainerAttachOptions")).
		Return(mobyclient.ContainerAttachResult{
			HijackedResponse: mobyclient.HijackedResponse{
				Conn:   &mockConn{},
				Reader: bufio.NewReader(framed),
			},
		}, nil)

	statusCh := make(chan container.WaitResponse, 1)
	statusCh <- container.WaitResponse{StatusCode: 0}
	errCh := make(chan error, 1)
	client.On("ContainerWait", ctx, "123", mobyclient.ContainerWaitOptions{Condition: container.WaitConditionNotRunning}).
		Return(mobyclient.ContainerWaitResult{
			Result: (<-chan container.WaitResponse)(statusCh),
			Error:  (<-chan error)(errCh),
		})

	cr := &containerReference{
		id:  "123",
		cli: client,
		input: &NewContainerInput{
			Image:  "image",
			Stdout: logWriter,
			Stderr: logWriter,
		},
	}

	require.NoError(t, cr.attach()(ctx))
	require.NoError(t, cr.wait()(ctx))

	// wait() must have blocked until the goroutine drained AND flushed; the
	// trailing, non-newline-terminated line must therefore be present. Reading
	// lines here is race-free because wait() synchronizes on attachDone, which
	// the goroutine closes after the final append.
	assert.Equal(t, []string{"line one\n", "last line without newline"}, lines)

	client.AssertExpectations(t)
}

func TestDockerWaitFailure(t *testing.T) {
	ctx := context.Background()

	statusCh := make(chan container.WaitResponse, 1)
	statusCh <- container.WaitResponse{StatusCode: 2}
	errCh := make(chan error, 1)

	client := &mockDockerClient{}
	client.On("ContainerWait", ctx, "123", mobyclient.ContainerWaitOptions{Condition: container.WaitConditionNotRunning}).
		Return(mobyclient.ContainerWaitResult{
			Result: (<-chan container.WaitResponse)(statusCh),
			Error:  (<-chan error)(errCh),
		})

	cr := &containerReference{
		id:  "123",
		cli: client,
		input: &NewContainerInput{
			Image: "image",
		},
	}

	err := cr.wait()(ctx)
	var exitErr ExitCodeError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, ExitCodeError(2), exitErr)
	assert.Equal(t, "Process completed with exit code 2.", err.Error())

	client.AssertExpectations(t)
}

// stubStatPath answers path resolution: the given paths exist, mapped to their target
// when they are a symlink, everything else does not exist.
func stubStatPath(client *mockDockerClient, existing map[string]string) {
	for containerPath, target := range existing {
		client.On("ContainerStatPath", mock.Anything, "123", mobyclient.ContainerStatPathOptions{Path: containerPath}).
			Return(mobyclient.ContainerStatPathResult{Stat: container.PathStat{LinkTarget: target}}, nil).Maybe()
	}
	client.On("ContainerStatPath", mock.Anything, "123", mock.Anything).
		Return(mobyclient.ContainerStatPathResult{}, cerrdefs.ErrNotFound).Maybe()
}

// The mkdir tarball is extracted at the deepest existing ancestor, with entries relative
// to it that never traverse the "/var/run" symlink, see moby/moby#53258.
func TestDockerCopyTarStream(t *testing.T) {
	ctx := context.Background()

	var mkdirNames []string
	client := &mockDockerClient{}
	stubStatPath(client, map[string]string{"/var": "", "/var/run": "/run", "/run": ""})
	client.On("CopyToContainer", ctx, "123", mock.MatchedBy(func(opts mobyclient.CopyToContainerOptions) bool {
		if opts.DestinationPath != "/run" || opts.Content == nil {
			return false
		}
		tr := tar.NewReader(opts.Content)
		for hdr, err := tr.Next(); err == nil; hdr, err = tr.Next() {
			mkdirNames = append(mkdirNames, hdr.Name)
		}
		return true
	})).Return(mobyclient.CopyToContainerResult{}, nil)
	client.On("CopyToContainer", ctx, "123", mock.MatchedBy(func(opts mobyclient.CopyToContainerOptions) bool {
		return opts.DestinationPath == "/run/act" && opts.Content != nil
	})).Return(mobyclient.CopyToContainerResult{}, nil)
	cr := &containerReference{
		id:  "123",
		cli: client,
		input: &NewContainerInput{
			Image: "image",
		},
	}

	require.NoError(t, cr.CopyTarStream(ctx, "/var/run/act", &bytes.Buffer{}))
	assert.Equal(t, []string{"act"}, mkdirNames)

	client.AssertExpectations(t)
}

func TestDockerCopyTarStreamErrors(t *testing.T) {
	merr := errors.New("Failure")
	for _, testCase := range []struct {
		name     string
		mkdirErr error
		copyErr  error
	}{
		{"mkdir", merr, nil},
		{"copy content", nil, merr},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()

			client := &mockDockerClient{}
			stubStatPath(client, map[string]string{"/var": "", "/var/run": ""})
			client.On("CopyToContainer", ctx, "123", mock.MatchedBy(func(opts mobyclient.CopyToContainerOptions) bool {
				return opts.DestinationPath == "/var/run" && opts.Content != nil
			})).Return(mobyclient.CopyToContainerResult{}, testCase.mkdirErr)
			client.On("CopyToContainer", ctx, "123", mock.MatchedBy(func(opts mobyclient.CopyToContainerOptions) bool {
				return opts.DestinationPath == "/var/run/act" && opts.Content != nil
			})).Return(mobyclient.CopyToContainerResult{}, testCase.copyErr).Maybe()
			cr := &containerReference{
				id:  "123",
				cli: client,
				input: &NewContainerInput{
					Image: "image",
				},
			}

			require.ErrorIs(t, cr.CopyTarStream(ctx, "/var/run/act", &bytes.Buffer{}), merr)

			client.AssertExpectations(t)
		})
	}
}

// A remove that raced the daemon's AutoRemove teardown is not a failure and must not
// be logged as one.
func TestRemoveIgnoresAutoRemoveRace(t *testing.T) {
	removeOpts := mobyclient.ContainerRemoveOptions{RemoveVolumes: true, Force: true}
	killOpts := mobyclient.ContainerKillOptions{Signal: "SIGKILL"}
	for _, tc := range []struct {
		name        string
		err         error
		wantWait    bool
		wantFailure bool
	}{
		{name: "removal in progress", err: cerrdefs.ErrConflict.WithMessage("removal of container abc is already in progress"), wantWait: true},
		{name: "already removed", err: cerrdefs.ErrNotFound.WithMessage("No such container: abc")},
		{name: "removed cleanly", err: nil},
		{name: "real failure", err: errors.New("driver failed to remove root filesystem"), wantFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger, hook := test.NewNullLogger()
			ctx := common.WithLogger(context.Background(), logger)
			client := &mockDockerClient{}
			client.On("ContainerKill", ctx, "abc", killOpts).Return(mobyclient.ContainerKillResult{}, nil)
			client.On("ContainerRemove", ctx, "abc", removeOpts).Return(mobyclient.ContainerRemoveResult{}, tc.err)
			if tc.wantWait {
				removed := make(chan container.WaitResponse, 1)
				removed <- container.WaitResponse{}
				client.On("ContainerWait", mock.Anything, "abc", mobyclient.ContainerWaitOptions{Condition: container.WaitConditionRemoved}).
					Return(mobyclient.ContainerWaitResult{Result: removed})
			}
			cr := &containerReference{id: "abc", cli: client}

			require.NoError(t, cr.remove()(ctx))
			// a failure keeps the id, so a later Remove() can retry it
			if tc.wantFailure {
				assert.Equal(t, "abc", cr.id)
				assert.Len(t, hook.AllEntries(), 1)
			} else {
				assert.Empty(t, cr.id)
				assert.Empty(t, hook.AllEntries())
			}
			client.AssertExpectations(t)
		})
	}
}

// A container whose id was never learned, because find() could not reach the daemon or
// create() lost its reply, must still be removed rather than leaking with its network. It
// was never started here, so it is not worth a kill of its own.
func TestRemoveWithoutIDUsesName(t *testing.T) {
	ctx := context.Background()
	client := &mockDockerClient{}
	client.On("ContainerRemove", ctx, "job-1", mobyclient.ContainerRemoveOptions{RemoveVolumes: true, Force: true}).
		Return(mobyclient.ContainerRemoveResult{}, nil)
	cr := &containerReference{cli: client, input: &NewContainerInput{Name: "job-1"}}

	require.NoError(t, cr.remove()(ctx))
	client.AssertExpectations(t)
}

// find() must drop a stale cached id so later Copy/Exec don't hit the
// daemon with a torn-down container.
func TestFindRevalidatesStaleID(t *testing.T) {
	ctx := context.Background()
	notFound := cerrdefs.ErrNotFound.WithMessage("No such container")
	boom := errors.New("daemon unreachable")
	newCR := func(id string) (*containerReference, *mockDockerClient) {
		client := &mockDockerClient{}
		return &containerReference{id: id, cli: client, input: &NewContainerInput{Name: "job-1"}}, client
	}
	listOpts := mobyclient.ContainerListOptions{All: true}
	inspectOpts := mobyclient.ContainerInspectOptions{}

	t.Run("stale id cleared, name lookup empty", func(t *testing.T) {
		cr, client := newCR("stale")
		client.On("ContainerInspect", ctx, "stale", inspectOpts).Return(mobyclient.ContainerInspectResult{}, notFound)
		client.On("ContainerList", ctx, listOpts).Return(mobyclient.ContainerListResult{}, nil)
		require.NoError(t, cr.find()(ctx))
		assert.Empty(t, cr.id)
		client.AssertExpectations(t)
	})

	t.Run("stale id cleared, name lookup repopulates", func(t *testing.T) {
		cr, client := newCR("stale")
		client.On("ContainerInspect", ctx, "stale", inspectOpts).Return(mobyclient.ContainerInspectResult{}, notFound)
		client.On("ContainerList", ctx, listOpts).Return(mobyclient.ContainerListResult{Items: []container.Summary{
			{ID: "other", Names: []string{"/somebody-else"}},
			{ID: "fresh", Names: []string{"/job-1"}},
		}}, nil)
		require.NoError(t, cr.find()(ctx))
		assert.Equal(t, "fresh", cr.id)
		client.AssertExpectations(t)
	})

	t.Run("live id kept", func(t *testing.T) {
		cr, client := newCR("live")
		client.On("ContainerInspect", ctx, "live", inspectOpts).Return(mobyclient.ContainerInspectResult{}, nil)
		require.NoError(t, cr.find()(ctx))
		assert.Equal(t, "live", cr.id)
		client.AssertExpectations(t)
	})

	t.Run("transient inspect error trusts cache", func(t *testing.T) {
		cr, client := newCR("live")
		client.On("ContainerInspect", ctx, "live", inspectOpts).Return(mobyclient.ContainerInspectResult{}, boom)
		require.NoError(t, cr.find()(ctx))
		assert.Equal(t, "live", cr.id)
		client.AssertExpectations(t)
	})

	t.Run("list error propagates", func(t *testing.T) {
		cr, client := newCR("")
		client.On("ContainerList", ctx, listOpts).Return(mobyclient.ContainerListResult{}, boom)
		require.ErrorIs(t, cr.find()(ctx), boom)
		client.AssertExpectations(t)
	})
}

// Every daemon entry point fails fast with a clear, container-named
// error when no live cr.id is known.
func TestRejectsMissingContainer(t *testing.T) {
	ctx := context.Background()
	client := &mockDockerClient{}
	client.On("ContainerList", ctx, mobyclient.ContainerListOptions{All: true}).Return(mobyclient.ContainerListResult{}, nil)
	cr := &containerReference{cli: client, input: &NewContainerInput{Name: "job-1"}}
	check := func(op string, err error) {
		t.Helper()
		require.ErrorIs(t, err, ErrContainerNotFound, op)
		assert.Contains(t, err.Error(), `container "job-1" does not exist`, op)
	}
	check("copyContent", cr.copyContent("/var/run/act", &FileEntry{Name: "x", Mode: 0o644})(ctx))
	check("copyDir", cr.copyDir("/var/run/act", "/src", false)(ctx))
	check("CopyTarStream", cr.CopyTarStream(ctx, "/var/run/act", &bytes.Buffer{}))
	check("exec", cr.exec([]string{"echo"}, nil, "", "")(ctx))
	_, err := cr.GetContainerArchive(ctx, "/var/run/act/x")
	check("GetContainerArchive", err)
	_, err = cr.Inspect(ctx)
	check("Inspect", err)

	// a known id the daemon has since dropped
	client.On("ContainerInspect", ctx, "gone", mobyclient.ContainerInspectOptions{}).
		Return(mobyclient.ContainerInspectResult{}, cerrdefs.ErrNotFound)
	removed := &containerReference{id: "gone", cli: client, input: &NewContainerInput{Name: "job-1"}}
	_, err = removed.Inspect(ctx)
	check("Inspect after removal", err)
}

// End-to-end: a stale cr.id is cleared, repopulated from name lookup,
// and the Copy completes against the fresh id.
func TestPublicCopyPipelineHandlesStaleID(t *testing.T) {
	ctx := context.Background()
	client := &mockDockerClient{}
	client.On("ContainerInspect", ctx, "stale", mobyclient.ContainerInspectOptions{}).
		Return(mobyclient.ContainerInspectResult{}, cerrdefs.ErrNotFound.WithMessage("gone"))
	client.On("ContainerList", ctx, mobyclient.ContainerListOptions{All: true}).
		Return(mobyclient.ContainerListResult{Items: []container.Summary{
			{ID: "fresh", Names: []string{"/job-1"}},
		}}, nil)
	client.On("CopyToContainer", ctx, "fresh", mock.MatchedBy(func(opts mobyclient.CopyToContainerOptions) bool {
		return opts.DestinationPath == "/var/run/act"
	})).Return(mobyclient.CopyToContainerResult{}, nil)

	cr := &containerReference{id: "stale", cli: client, input: &NewContainerInput{Name: "job-1"}}
	require.NoError(t, cr.Copy("/var/run/act", &FileEntry{Name: "x", Mode: 0o644})(ctx))
	assert.Equal(t, "fresh", cr.id)
	client.AssertExpectations(t)
}

// TestDockerCopyToSymlinkPath is a regression test for gitea/runner#981. Most base images
// symlink /var/run to /run, so copying into /var/run/act traverses that symlink. The broken
// docker 29.5.1 daemon fails the extraction with "mkdirat var/run: file exists" (fixed in
// 29.5.2). Running against the daemon shipped in the dind image, this catches a bad bump.
func TestDockerCopyToSymlinkPath(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	rc := NewContainer(&NewContainerInput{
		Image:      "alpine:latest",
		Entrypoint: []string{"sleep", "30"},
		Name:       "act-test-symlink-" + time.Now().Format("20060102150405.000000"),
		AutoRemove: true,
	})
	require.NoError(t, rc.Pull(false)(ctx))
	require.NoError(t, rc.Create(nil, nil)(ctx))
	require.NoError(t, rc.Start(false)(ctx))
	t.Cleanup(func() {
		_ = rc.Remove()(ctx)
		_ = rc.Close()(ctx)
	})

	// CopyTarStream resolves the var/run symlink and creates act below its target, the
	// exact step that fails on a broken daemon.
	err := rc.CopyTarStream(ctx, "/var/run/act", &bytes.Buffer{})
	require.NoError(t, err)
}

// Type assert containerReference implements ExecutionsEnvironment
var _ ExecutionsEnvironment = &containerReference{}

func TestCheckVolumes(t *testing.T) {
	testCases := []struct {
		desc          string
		validVolumes  []string
		binds         []string
		expectedBinds []string
	}{
		{
			desc:         "match all volumes",
			validVolumes: []string{"**"},
			binds: []string{
				"shared_volume:/shared_volume",
				"/home/test/data:/test_data",
				"/etc/conf.d/base.json:/config/base.json",
				"sql_data:/sql_data",
				"/secrets/keys:/keys",
			},
			expectedBinds: []string{
				"shared_volume:/shared_volume",
				"/home/test/data:/test_data",
				"/etc/conf.d/base.json:/config/base.json",
				"sql_data:/sql_data",
				"/secrets/keys:/keys",
			},
		},
		{
			desc:         "no volumes can be matched",
			validVolumes: []string{},
			binds: []string{
				"shared_volume:/shared_volume",
				"/home/test/data:/test_data",
				"/etc/conf.d/base.json:/config/base.json",
				"sql_data:/sql_data",
				"/secrets/keys:/keys",
			},
			expectedBinds: []string{},
		},
		{
			desc: "only allowed volumes can be matched",
			validVolumes: []string{
				"shared_volume",
				"/home/test/data",
				"/etc/conf.d/*.json",
			},
			binds: []string{
				"shared_volume:/shared_volume",
				"/home/test/data:/test_data",
				"/etc/conf.d/base.json:/config/base.json",
				"sql_data:/sql_data",
				"/secrets/keys:/keys",
			},
			expectedBinds: []string{
				"shared_volume:/shared_volume",
				"/home/test/data:/test_data",
				"/etc/conf.d/base.json:/config/base.json",
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			logger, _ := test.NewNullLogger()
			ctx := common.WithLogger(context.Background(), logger)
			cr := &containerReference{
				input: &NewContainerInput{
					ValidVolumes: tc.validVolumes,
				},
			}
			_, hostConf := cr.sanitizeConfig(ctx, &container.Config{}, &container.HostConfig{Binds: tc.binds})
			assert.Equal(t, tc.expectedBinds, hostConf.Binds)
		})
	}
}

func TestSanitizeOptionsHostConfig(t *testing.T) {
	logger, _ := test.NewNullLogger()

	dangerous := func() *container.HostConfig {
		return &container.HostConfig{
			PidMode:      "host",
			IpcMode:      "host",
			UTSMode:      "host",
			CgroupnsMode: "host",
			UsernsMode:   "host",
			CapAdd:       []string{"ALL"},
			SecurityOpt:  []string{"seccomp=unconfined", "apparmor=unconfined"},
			VolumesFrom:  []string{"other"},
			Runtime:      "runc",
			Resources: container.Resources{
				CgroupParent:      "/custom",
				Devices:           []container.DeviceMapping{{PathOnHost: "/dev/sda", PathInContainer: "/dev/sda", CgroupPermissions: "rwm"}},
				DeviceCgroupRules: []string{"a *:* rwm"},
			},
			Sysctls: map[string]string{"net.ipv4.ip_forward": "1"},
		}
	}

	hostConfig := dangerous()
	sanitizeOptionsHostConfig(logger, hostConfig)

	assert.Empty(t, string(hostConfig.PidMode))
	assert.Empty(t, string(hostConfig.IpcMode))
	assert.Empty(t, string(hostConfig.UTSMode))
	assert.Empty(t, string(hostConfig.CgroupnsMode))
	assert.Empty(t, string(hostConfig.UsernsMode))
	assert.Empty(t, hostConfig.CapAdd)
	assert.Empty(t, hostConfig.SecurityOpt)
	assert.Empty(t, hostConfig.Devices)
	assert.Empty(t, hostConfig.DeviceCgroupRules)
	assert.Empty(t, hostConfig.VolumesFrom)
	assert.Empty(t, hostConfig.Runtime)
	assert.Empty(t, hostConfig.CgroupParent)
	assert.Empty(t, hostConfig.Sysctls)
}

func TestMergeContainerConfigsStripsDangerousOptionsWhenUnprivileged(t *testing.T) {
	// OS-independent options only: --device parsing requires a linux/windows
	// server OS, which is not guaranteed for the test host.
	const dangerousOptions = "--pid=host --ipc=host --uts=host --cgroupns=host " +
		"--userns=host --cap-add=ALL --security-opt seccomp=unconfined " +
		"--security-opt apparmor=unconfined --volumes-from other " +
		"--runtime runc --cgroup-parent /custom --sysctl net.ipv4.ip_forward=1"

	t.Run("unprivileged strips host-escape options", func(t *testing.T) {
		logger, _ := test.NewNullLogger()
		ctx := common.WithLogger(context.Background(), logger)
		cr := &containerReference{
			input: &NewContainerInput{
				Options:     dangerousOptions,
				NetworkMode: "bridge",
				UsernsMode:  "private",
			},
		}

		_, hostConfig, err := cr.mergeContainerConfigs(ctx, &container.Config{}, &container.HostConfig{
			Privileged:  false,
			UsernsMode:  container.UsernsMode("private"),
			NetworkMode: container.NetworkMode("bridge"),
		})
		require.NoError(t, err)

		assert.False(t, hostConfig.Privileged)
		assert.Empty(t, string(hostConfig.PidMode))
		assert.Empty(t, string(hostConfig.IpcMode))
		assert.Empty(t, string(hostConfig.UTSMode))
		assert.Empty(t, string(hostConfig.CgroupnsMode))
		// UsernsMode must keep the runner-controlled value, not the one from options.
		assert.Equal(t, "private", string(hostConfig.UsernsMode))
		assert.Empty(t, hostConfig.CapAdd)
		assert.Empty(t, hostConfig.SecurityOpt)
		assert.Empty(t, hostConfig.VolumesFrom)
		assert.Empty(t, hostConfig.Runtime)
		assert.Empty(t, hostConfig.CgroupParent)
		assert.Empty(t, hostConfig.Sysctls)
	})

	t.Run("privileged preserves options", func(t *testing.T) {
		logger, _ := test.NewNullLogger()
		ctx := common.WithLogger(context.Background(), logger)
		cr := &containerReference{
			input: &NewContainerInput{
				Options:     "--pid=host --cap-add=ALL --security-opt seccomp=unconfined",
				NetworkMode: "bridge",
			},
		}

		_, hostConfig, err := cr.mergeContainerConfigs(ctx, &container.Config{}, &container.HostConfig{
			Privileged:  true,
			NetworkMode: container.NetworkMode("bridge"),
		})
		require.NoError(t, err)

		assert.Equal(t, "host", string(hostConfig.PidMode))
		assert.Equal(t, []string{"ALL"}, hostConfig.CapAdd)
		assert.Equal(t, []string{"seccomp=unconfined"}, hostConfig.SecurityOpt)
	})
}

func TestCheckVolumesRejectsEscapingHostPaths(t *testing.T) {
	logger, _ := test.NewNullLogger()
	ctx := common.WithLogger(context.Background(), logger)

	base := t.TempDir()
	allowed := filepath.Join(base, "allowed")
	denied := filepath.Join(base, "denied")
	require.NoError(t, os.MkdirAll(allowed, 0o700))
	require.NoError(t, os.MkdirAll(denied, 0o700))

	cr := &containerReference{
		input: &NewContainerInput{
			ValidVolumes: []string{filepath.Join(allowed, "**")},
		},
	}

	escapingPath := allowed + string(filepath.Separator) + ".." + string(filepath.Separator) + "denied"
	_, hostConf := cr.sanitizeConfig(ctx, &container.Config{}, &container.HostConfig{
		Binds: []string{escapingPath + ":/mnt"},
	})
	assert.Empty(t, hostConf.Binds)

	linkPath := filepath.Join(allowed, "link")
	if err := os.Symlink(denied, linkPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	_, hostConf = cr.sanitizeConfig(ctx, &container.Config{}, &container.HostConfig{
		Binds: []string{linkPath + ":/mnt"},
	})
	assert.Empty(t, hostConf.Binds)

	_, hostConf = cr.sanitizeConfig(ctx, &container.Config{}, &container.HostConfig{
		Binds: []string{filepath.Join(linkPath, "missing") + ":/mnt"},
	})
	assert.Empty(t, hostConf.Binds)
}

func TestContainerInfoFromInspect(t *testing.T) {
	t.Run("reports no healthcheck when the image declares none", func(t *testing.T) {
		info := containerInfoFromInspect(container.InspectResponse{
			ID:    "abc123",
			State: &container.State{Status: "running", Running: true},
		})

		assert.Equal(t, "abc123", info.ID)
		assert.Equal(t, "running", info.State)
		assert.Equal(t, HealthNone, info.Health)
		assert.Empty(t, info.Ports)
	})

	t.Run("reports the health status and the last probe output", func(t *testing.T) {
		info := containerInfoFromInspect(container.InspectResponse{
			State: &container.State{
				Status: "running",
				Health: &container.Health{
					Status: container.Unhealthy,
					Log: []*container.HealthcheckResult{
						{Output: "first\n"},
						{Output: "connection refused\n"},
					},
				},
			},
		})

		assert.Equal(t, HealthUnhealthy, info.Health)
		assert.Equal(t, "connection refused", info.HealthOutput)
	})

	t.Run("reports the published ports", func(t *testing.T) {
		info := containerInfoFromInspect(container.InspectResponse{
			State: &container.State{Status: "running"},
			NetworkSettings: &container.NetworkSettings{
				Ports: network.PortMap{
					network.MustParsePort("5432/tcp"): []network.PortBinding{{HostPort: "49153"}},
					network.MustParsePort("6379/tcp"): nil,
				},
			},
		})

		assert.Equal(t, map[string]string{"5432": "49153"}, info.Ports)
	})

	t.Run("tolerates a container without state", func(t *testing.T) {
		info := containerInfoFromInspect(container.InspectResponse{ID: "abc123"})

		assert.Equal(t, "abc123", info.ID)
		assert.Equal(t, HealthNone, info.Health)
	})
}

func TestMergeContainerConfigsVolumesReplaceRunnerMounts(t *testing.T) {
	logger, _ := test.NewNullLogger()
	ctx := common.WithLogger(context.Background(), logger)
	cr := &containerReference{
		input: &NewContainerInput{
			NetworkMode: "bridge",
			Options:     "--volume /host/tools:/opt/hostedtoolcache",
		},
	}

	_, hostConf, err := cr.mergeContainerConfigs(ctx, &container.Config{}, &container.HostConfig{
		Binds:  []string{"/var/run/docker.sock:/var/run/docker.sock"},
		Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "act-toolcache", Target: "/opt/hostedtoolcache"}},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"/var/run/docker.sock:/var/run/docker.sock", "/host/tools:/opt/hostedtoolcache"}, hostConf.Binds)
	assert.Empty(t, hostConf.Mounts)
}
