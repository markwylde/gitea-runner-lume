// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package container

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"gitea.com/gitea/runner/act/common"
)

// RemoteEnvironmentClient is implemented by the authenticated guest session.
// Its arguments are structured protocol fields, never a host shell command.
type RemoteEnvironmentClient interface {
	CopyFiles(context.Context, string, []*FileEntry) error
	CopyTar(context.Context, string, io.Reader) error
	CopyDir(context.Context, string, string, bool) error
	Archive(context.Context, string) (io.ReadCloser, error)
	Exec(context.Context, []string, map[string]string, string, string, io.Writer) error
	Inspect(context.Context) (*Info, error)
	Close(context.Context) error
}

type RemoteEnvironment struct {
	Client    RemoteEnvironmentClient
	Path      string
	TmpDir    string
	ToolCache string
	Workdir   string
	ActPath   string
	Stdout    io.Writer
}

// GuestImagePath is the macOS tool PATH provisioned on Lume base images.
const GuestImagePath = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

var _ ExecutionsEnvironment = (*RemoteEnvironment)(nil)

func (e *RemoteEnvironment) validate() error {
	if e.Client == nil || e.Path == "" || e.TmpDir == "" || e.ToolCache == "" || e.Workdir == "" || e.ActPath == "" {
		return errors.New("remote execution environment is incomplete")
	}
	for _, path := range []string{e.Path, e.TmpDir, e.ToolCache, e.Workdir, e.ActPath} {
		if !filepath.IsAbs(path) {
			return errors.New("remote execution paths must be absolute")
		}
	}
	return nil
}

func (e *RemoteEnvironment) Create(_, _ []string) common.Executor {
	return func(context.Context) error { return e.validate() }
}

func (e *RemoteEnvironment) ConnectToNetwork(string) common.Executor {
	return func(context.Context) error { return nil }
}

func (e *RemoteEnvironment) Copy(destPath string, files ...*FileEntry) common.Executor {
	return func(ctx context.Context) error {
		if err := e.validate(); err != nil {
			return err
		}
		return e.Client.CopyFiles(ctx, destPath, files)
	}
}

func (e *RemoteEnvironment) CopyTarStream(ctx context.Context, destPath string, stream io.Reader) error {
	if err := e.validate(); err != nil {
		return err
	}
	return e.Client.CopyTar(ctx, destPath, stream)
}

func (e *RemoteEnvironment) CopyDir(destPath, srcPath string, useGitIgnore bool) common.Executor {
	return func(ctx context.Context) error {
		if err := e.validate(); err != nil {
			return err
		}
		return e.Client.CopyDir(ctx, destPath, srcPath, useGitIgnore)
	}
}

func (e *RemoteEnvironment) GetContainerArchive(ctx context.Context, srcPath string) (io.ReadCloser, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	return e.Client.Archive(ctx, srcPath)
}

func (e *RemoteEnvironment) Inspect(ctx context.Context) (*Info, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	return e.Client.Inspect(ctx)
}

func (*RemoteEnvironment) DumpLogs(context.Context) error { return nil }
func (*RemoteEnvironment) Pull(bool) common.Executor {
	return func(context.Context) error { return nil }
}
func (*RemoteEnvironment) Start(bool) common.Executor {
	return func(context.Context) error { return nil }
}

func (e *RemoteEnvironment) Exec(command []string, env map[string]string, user, workdir string) common.Executor {
	return func(ctx context.Context) error {
		if err := e.validate(); err != nil {
			return err
		}
		if len(command) == 0 {
			return errors.New("remote command is empty")
		}
		remoteWorkdir := e.Path
		if workdir != "" {
			if filepath.IsAbs(workdir) {
				remoteWorkdir = e.ToContainerPath(workdir)
			} else {
				remoteWorkdir = filepath.Join(e.Path, workdir)
			}
		}
		return e.Client.Exec(ctx, command, env, user, remoteWorkdir, e.Stdout)
	}
}

func (e *RemoteEnvironment) UpdateFromEnv(srcPath string, env *map[string]string) common.Executor {
	return parseEnvFile(e, srcPath, env)
}

func (*RemoteEnvironment) UpdateFromImageEnv(*map[string]string) common.Executor {
	return func(context.Context) error { return nil }
}

func (e *RemoteEnvironment) Remove() common.Executor { return e.Close() }

func (e *RemoteEnvironment) Close() common.Executor {
	return func(ctx context.Context) error {
		if e.Client == nil {
			return nil
		}
		return e.Client.Close(ctx)
	}
}

func (e *RemoteEnvironment) ReplaceLogWriter(stdout, _ io.Writer) (io.Writer, io.Writer) {
	prior := e.Stdout
	e.Stdout = stdout
	return prior, prior
}

func (e *RemoteEnvironment) ToContainerPath(path string) string {
	if relative, err := filepath.Rel(e.Workdir, path); err == nil && relative != "." && !strings.HasPrefix(relative, "..") {
		return filepath.Join(e.Path, relative)
	}
	if filepath.Clean(path) == filepath.Clean(e.Workdir) {
		return e.Path
	}
	return path
}

func (e *RemoteEnvironment) GetActPath() string        { return e.ActPath }
func (*RemoteEnvironment) GetPathVariableName() string { return "PATH" }
func (*RemoteEnvironment) DefaultPathVariable() string {
	return GuestImagePath
}
func (*RemoteEnvironment) JoinPathVariable(paths ...string) string { return strings.Join(paths, ":") }
func (e *RemoteEnvironment) GetRunnerContext(context.Context) map[string]any {
	return map[string]any{"os": "macOS", "arch": "ARM64", "temp": e.TmpDir, "tool_cache": e.ToolCache}
}
func (*RemoteEnvironment) IsEnvironmentCaseInsensitive() bool { return false }
