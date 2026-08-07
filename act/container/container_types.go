// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2023 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"context"
	"errors"
	"fmt"
	"io"

	"gitea.com/gitea/runner/act/common"

	"github.com/docker/go-connections/nat"
	"github.com/moby/moby/api/types/container"
)

// ExitCodeError reports a non-zero process exit code from a container command.
type ExitCodeError int

func (e ExitCodeError) Error() string {
	return fmt.Sprintf("Process completed with exit code %d.", int(e))
}

// NewContainerInput the input for the New function
type NewContainerInput struct {
	Image          string
	Username       string
	Password       string
	Entrypoint     []string
	Cmd            []string
	WorkingDir     string
	Env            []string
	Binds          []string
	Mounts         map[string]string
	Name           string
	Stdout         io.Writer
	Stderr         io.Writer
	NetworkMode    string
	Privileged     bool
	UsernsMode     string
	Platform       string
	Options        string
	NetworkAliases []string
	ExposedPorts   nat.PortSet
	PortBindings   nat.PortMap

	// Gitea specific
	AutoRemove   bool
	ValidVolumes []string
	AllocatePTY  bool // allocate a pseudo-TTY for the container's exec processes
}

// FileEntry is a file to copy to a container
type FileEntry struct {
	Name string
	Mode int64
	Body string
}

// Container and healthcheck states, as plain strings so a caller of Info needs no docker
// SDK of its own.
const (
	StateRunning = string(container.StateRunning)

	HealthNone      = string(container.NoHealthcheck)
	HealthStarting  = string(container.Starting)
	HealthHealthy   = string(container.Healthy)
	HealthUnhealthy = string(container.Unhealthy)
)

// ErrContainerNotFound reports a container the daemon no longer knows. Its text is a
// fragment, missingContainerError composes it into the message every operation shares.
var ErrContainerNotFound = errors.New("does not exist")

// Info is a snapshot of a container, as of one inspect.
type Info struct {
	ID       string
	State    string // the docker container state: "created", "running", "exited", ...
	ExitCode int
	Health   string // one of the Health* constants
	// HealthOutput is the last healthcheck probe's output.
	HealthOutput string
	Ports        map[string]string // container port ("5432") to the host port it is published on
}

// Container for managing docker run containers
type Container interface {
	Create(capAdd, capDrop []string) common.Executor
	ConnectToNetwork(name string) common.Executor
	Copy(destPath string, files ...*FileEntry) common.Executor
	CopyTarStream(ctx context.Context, destPath string, tarStream io.Reader) error
	CopyDir(destPath, srcPath string, useGitIgnore bool) common.Executor
	GetContainerArchive(ctx context.Context, srcPath string) (io.ReadCloser, error)
	Inspect(ctx context.Context) (*Info, error)
	DumpLogs(ctx context.Context) error
	Pull(forcePull bool) common.Executor
	Start(attach bool) common.Executor
	Exec(command []string, env map[string]string, user, workdir string) common.Executor
	UpdateFromEnv(srcPath string, env *map[string]string) common.Executor
	UpdateFromImageEnv(env *map[string]string) common.Executor
	Remove() common.Executor
	Close() common.Executor
	ReplaceLogWriter(io.Writer, io.Writer) (io.Writer, io.Writer)
}

// NewDockerBuildExecutorInput the input for the NewDockerBuildExecutor function
type NewDockerBuildExecutorInput struct {
	ContextDir   string
	Dockerfile   string
	BuildContext io.Reader
	ImageTag     string
	Platform     string
	BuildArgs    map[string]*string
}

// NewDockerNetworkCreateExecutorInput the input for the NewDockerNetworkCreateExecutor function
type NewDockerNetworkCreateExecutorInput struct {
	EnableIPv4 *bool
	EnableIPv6 *bool
	RunnerUUID string
}

// NewDockerPullExecutorInput the input for the NewDockerPullExecutor function
type NewDockerPullExecutorInput struct {
	Image     string
	ForcePull bool
	Platform  string
	Username  string
	Password  string
}
