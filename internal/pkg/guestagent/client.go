// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package guestagent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gitea.com/gitea/runner/act/container"
	"gitea.com/gitea/runner/internal/pkg/guestproto"
)

type Client struct {
	reader    *guestproto.Reader
	writer    *guestproto.Writer
	mu        sync.Mutex
	interrupt func()
	diagnose  func(error) error
	peerHello guestproto.Hello
	guestUser string
	guestHome string
}

func (c *Client) SetInterrupt(interrupt func())            { c.interrupt = interrupt }
func (c *Client) SetDiagnostic(diagnose func(error) error) { c.diagnose = diagnose }

// SetGuestIdentity records the VM account used for exec. The current guest
// image replaces the process environment with the request, so HOME must be
// sent explicitly; omitting it is not enough.
func (c *Client) SetGuestIdentity(user, home string) {
	c.guestUser = user
	c.guestHome = home
}

func NewClient(input io.Reader, output io.Writer, expected guestproto.Hello, hostPrivateKey ed25519.PrivateKey, guestPublicKey ed25519.PublicKey) (*Client, error) {
	if len(hostPrivateKey) != ed25519.PrivateKeySize || len(guestPublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("host session keys are invalid")
	}
	client := &Client{reader: guestproto.NewReader(input), writer: guestproto.NewWriter(output)}
	hostHello, err := guestproto.SignHello(expected, hostPrivateKey)
	if err != nil {
		return nil, err
	}
	if err := client.writer.Write("host_hello", hostHello); err != nil {
		return nil, err
	}
	var guestHello guestproto.Hello
	if err := client.reader.Read("guest_hello", &guestHello); err != nil {
		return nil, err
	}
	if err := guestproto.VerifyHello(guestHello, guestPublicKey, expected); err != nil {
		return nil, fmt.Errorf("authenticate guest: %w", err)
	}
	client.peerHello = guestHello
	return client, nil
}

func (c *Client) PeerHello() guestproto.Hello { return c.peerHello }

func (c *Client) CopyFiles(ctx context.Context, destination string, files []*container.FileEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	request := guestproto.CopyRequest{Destination: destination, Files: make([]guestproto.File, 0, len(files))}
	for _, file := range files {
		if file == nil {
			return errors.New("copy contains a nil file")
		}
		request.Files = append(request.Files, guestproto.File{Name: file.Name, Mode: file.Mode, Body: []byte(file.Body)})
	}
	if err := c.writer.Write("copy", request); err != nil {
		return err
	}
	return c.expect(ctx, "copied", nil)
}

func (c *Client) CopyTar(ctx context.Context, destination string, stream io.Reader) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.writer.Write("copy_tar_begin", guestproto.CopyTarBegin{Destination: destination}); err != nil {
		return err
	}
	buffer := make([]byte, 64*1024)
	for {
		count, err := stream.Read(buffer)
		if count > 0 {
			if writeErr := c.writer.Write("copy_tar_chunk", guestproto.Data{Bytes: append([]byte(nil), buffer[:count]...)}); writeErr != nil {
				return writeErr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if err := c.writer.Write("copy_tar_end", struct{}{}); err != nil {
		return err
	}
	return c.expect(ctx, "copied", nil)
}

func (c *Client) CopyDir(ctx context.Context, destination, source string, _ bool) error {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	err := filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("unsupported action-cache entry %q", path)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(writer, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return c.CopyTar(ctx, destination, &buffer)
}

func (c *Client) Archive(ctx context.Context, path string) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.writer.Write("archive", guestproto.ArchiveRequest{Path: path}); err != nil {
		return nil, err
	}
	var data guestproto.Data
	if err := c.expect(ctx, "archive_data", &data); err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data.Bytes)), nil
}

func (c *Client) Exec(ctx context.Context, command []string, env map[string]string, user, workdir string, output io.Writer) error {
	env = maps.Clone(env)
	if env == nil {
		env = make(map[string]string)
	}
	applyGuestIdentity(env, c.guestUser, c.guestHome)
	for name, value := range env {
		if !envNamePattern.MatchString(name) {
			return fmt.Errorf("remote command environment name %q is invalid", name)
		}
		if len(value) > 4*1024*1024 {
			return fmt.Errorf("remote command environment value for %q exceeds the size limit", name)
		}
		if strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("remote command environment value for %q contains NUL", name)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.writer.Write("exec", guestproto.ExecRequest{Command: command, Env: env, User: user, Workdir: workdir}); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		message, err := c.readMessage(ctx)
		if err != nil {
			return err
		}
		switch message.Type {
		case "output":
			var data guestproto.Data
			if err := guestproto.DecodeBody(message, &data); err != nil {
				return err
			}
			if output != nil {
				if _, err := output.Write(data.Bytes); err != nil {
					return err
				}
			}
		case "exec_result":
			var result guestproto.ExecResult
			if err := guestproto.DecodeBody(message, &result); err != nil {
				return err
			}
			if result.ExitCode != 0 {
				if result.Error != "" {
					return fmt.Errorf("remote command failed: %s", result.Error)
				}
				return container.ExitCodeError(result.ExitCode)
			}
			return nil
		case "error":
			return decodeRemoteError(message)
		default:
			return fmt.Errorf("unexpected guest response %q", message.Type)
		}
	}
}

func applyGuestIdentity(env map[string]string, user, home string) {
	stripHostIdentityEnv(env)
	if home != "" {
		env["HOME"] = home
	}
	if user != "" {
		env["USER"] = user
		env["LOGNAME"] = user
	}
	env["PATH"] = guestExecutionPath(env["PATH"])
}

func stripHostIdentityEnv(env map[string]string) {
	for _, name := range []string{"HOME", "USER", "LOGNAME", "TMPDIR", "TMP", "TEMP", "SSH_AUTH_SOCK"} {
		delete(env, name)
	}
}

func guestExecutionPath(requested string) string {
	controllerHome, _ := os.UserHomeDir()
	image := filepath.SplitList(container.GuestImagePath)
	directories := make([]string, 0, len(filepath.SplitList(requested))+len(image))
	seen := make(map[string]struct{}, cap(directories))
	for _, directory := range filepath.SplitList(requested) {
		if directory == "" || !filepath.IsAbs(directory) || pathIsInside(directory, controllerHome) {
			continue
		}
		if _, exists := seen[directory]; exists {
			continue
		}
		seen[directory] = struct{}{}
		directories = append(directories, directory)
	}
	for _, directory := range image {
		if _, exists := seen[directory]; exists {
			continue
		}
		seen[directory] = struct{}{}
		directories = append(directories, directory)
	}
	return strings.Join(directories, string(filepath.ListSeparator))
}

func pathIsInside(path, root string) bool {
	if root == "" {
		return false
	}
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	if cleanPath == cleanRoot {
		return true
	}
	return strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator))
}

func (*Client) Inspect(context.Context) (*container.Info, error) {
	return &container.Info{ID: "lume-worker", State: container.StateRunning, Health: container.HealthHealthy}, nil
}

func (c *Client) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.writer.Write("close", struct{}{}); err != nil {
		return err
	}
	return c.expect(ctx, "closed", nil)
}

func (c *Client) expect(ctx context.Context, expectedType string, destination any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	message, err := c.readMessage(ctx)
	if err != nil {
		return err
	}
	if message.Type == "error" {
		return decodeRemoteError(message)
	}
	if message.Type != expectedType {
		return fmt.Errorf("unexpected guest response %q", message.Type)
	}
	return guestproto.DecodeBody(message, destination)
}

func (c *Client) readMessage(ctx context.Context) (guestproto.Message, error) {
	type outcome struct {
		message guestproto.Message
		err     error
	}
	done := make(chan outcome, 1)
	go func() { message, err := c.reader.ReadMessage(); done <- outcome{message, err} }()
	select {
	case result := <-done:
		if result.err != nil && c.diagnose != nil {
			return guestproto.Message{}, c.diagnose(result.err)
		}
		return result.message, result.err
	case <-ctx.Done():
		if c.interrupt != nil {
			c.interrupt()
		}
		return guestproto.Message{}, ctx.Err()
	}
}

func decodeRemoteError(message guestproto.Message) error {
	var remote guestproto.Error
	if err := guestproto.DecodeBody(message, &remote); err != nil {
		return err
	}
	return fmt.Errorf("guest %s error: %s", remote.Class, remote.Message)
}
