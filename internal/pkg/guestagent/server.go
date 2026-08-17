// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package guestagent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"gitea.com/gitea/runner/act/container"
	"gitea.com/gitea/runner/internal/pkg/guestproto"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,127}$`)

type Server struct {
	root            string
	reader          *guestproto.Reader
	writer          *guestproto.Writer
	mu              sync.Mutex
	expected        guestproto.Hello
	hostPublicKey   ed25519.PublicKey
	guestPrivateKey ed25519.PrivateKey
	tarDestination  string
	tarBuffer       bytes.Buffer
}

func NewServer(root string, input io.Reader, output io.Writer, expected guestproto.Hello, hostPublicKey ed25519.PublicKey, guestPrivateKey ed25519.PrivateKey) (*Server, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, errors.New("guest root must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve guest root: %w", err)
	}
	if len(hostPublicKey) != ed25519.PublicKeySize || len(guestPrivateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("guest session keys are invalid")
	}
	return &Server{
		root: resolved, reader: guestproto.NewReader(input), writer: guestproto.NewWriter(output),
		expected: expected, hostPublicKey: hostPublicKey, guestPrivateKey: guestPrivateKey,
	}, nil
}

func (s *Server) Serve(ctx context.Context) error {
	var hostHello guestproto.Hello
	if err := s.reader.Read("host_hello", &hostHello); err != nil {
		return fmt.Errorf("read host hello: %w", err)
	}
	if err := guestproto.VerifyHello(hostHello, s.hostPublicKey, s.expected); err != nil {
		return fmt.Errorf("authenticate host: %w", err)
	}
	guestHello, err := attestedHello(s.expected)
	if err != nil {
		return fmt.Errorf("collect guest attestation: %w", err)
	}
	guestHello, err = guestproto.SignHello(guestHello, s.guestPrivateKey)
	if err != nil {
		return fmt.Errorf("sign guest hello: %w", err)
	}
	if err := waitForConsoleGUISession(ctx); err != nil {
		return err
	}
	if err := s.write("guest_hello", guestHello); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		message, err := s.reader.ReadMessage()
		if err != nil {
			return err
		}
		switch message.Type {
		case "copy":
			err = s.copy(message)
		case "exec":
			err = s.exec(ctx, message)
		case "archive":
			err = s.archive(message)
		case "copy_tar_begin":
			err = s.copyTarBegin(message)
		case "copy_tar_chunk":
			err = s.copyTarChunk(message)
		case "copy_tar_end":
			err = s.copyTarEnd()
		case "close":
			return s.write("closed", struct{}{})
		default:
			return fmt.Errorf("unsupported guest operation %q", message.Type)
		}
		if err != nil {
			_ = s.write("error", guestproto.Error{Class: "guest_operation", Message: boundedError(err)})
			return err
		}
	}
}

func attestedHello(hello guestproto.Hello) (guestproto.Hello, error) {
	executable, err := os.Executable()
	if err != nil {
		return guestproto.Hello{}, err
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		return guestproto.Hello{}, err
	}
	version := runtime.GOOS
	if runtime.GOOS == "darwin" {
		output, commandErr := exec.Command("/usr/bin/sw_vers", "-productVersion").Output()
		if commandErr != nil {
			return guestproto.Hello{}, commandErr
		}
		version = strings.TrimSpace(string(output))
	}
	digest := sha256.Sum256(binary)
	hello.OS = runtime.GOOS
	hello.Architecture = runtime.GOARCH
	hello.OSVersion = version
	hello.AgentSHA256 = fmt.Sprintf("%x", digest[:])
	hello.UID = os.Geteuid()
	return hello, nil
}

func (s *Server) copyTarBegin(message guestproto.Message) error {
	if s.tarDestination != "" {
		return errors.New("tar copy is already active")
	}
	var request guestproto.CopyTarBegin
	if err := guestproto.DecodeBody(message, &request); err != nil {
		return err
	}
	destination, err := s.safePath(request.Destination)
	if err != nil {
		return err
	}
	s.tarDestination = destination
	s.tarBuffer.Reset()
	return nil
}

func (s *Server) copyTarChunk(message guestproto.Message) error {
	if s.tarDestination == "" {
		return errors.New("tar copy has not started")
	}
	var data guestproto.Data
	if err := guestproto.DecodeBody(message, &data); err != nil {
		return err
	}
	if len(data.Bytes) == 0 || len(data.Bytes) > 64*1024 || s.tarBuffer.Len()+len(data.Bytes) > guestproto.MaxSessionBytes/2 {
		return errors.New("tar copy chunk or total size is invalid")
	}
	_, err := s.tarBuffer.Write(data.Bytes)
	return err
}

func (s *Server) copyTarEnd() error {
	if s.tarDestination == "" {
		return errors.New("tar copy has not started")
	}
	destination := s.tarDestination
	s.tarDestination = ""
	defer s.tarBuffer.Reset()
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	reader := tar.NewReader(bytes.NewReader(s.tarBuffer.Bytes()))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if header.Name == "" || filepath.IsAbs(header.Name) {
			return errors.New("tar entry path is invalid")
		}
		path, err := s.safePath(filepath.Join(destination, header.Name))
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > guestproto.MaxSessionBytes/2 {
				return errors.New("tar entry size is invalid")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode)&0o700)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return errors.New("tar links and special entries are forbidden")
		}
	}
	return s.write("copied", struct{}{})
}

func (s *Server) copy(message guestproto.Message) error {
	var request guestproto.CopyRequest
	if err := guestproto.DecodeBody(message, &request); err != nil {
		return err
	}
	destination, err := s.safePath(request.Destination)
	if err != nil {
		return err
	}
	if len(request.Files) == 0 || len(request.Files) > 1024 {
		return errors.New("copy file count is invalid")
	}
	for _, file := range request.Files {
		if file.Name == "" || filepath.IsAbs(file.Name) || strings.Contains(file.Name, "..") || len(file.Body) > guestproto.MaxMessageBytes {
			return errors.New("copy file entry is invalid")
		}
		path, err := s.safePath(filepath.Join(destination, file.Name))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(file.Mode) & 0o700
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(path, file.Body, mode); err != nil {
			return err
		}
	}
	return s.write("copied", struct{}{})
}

func (s *Server) exec(ctx context.Context, message guestproto.Message) error {
	var request guestproto.ExecRequest
	if err := guestproto.DecodeBody(message, &request); err != nil {
		return err
	}
	if len(request.Command) == 0 || len(request.Command) > 128 {
		return errors.New("guest command argument count is invalid")
	}
	for _, argument := range request.Command {
		if len(argument) > 1024*1024 || strings.IndexByte(argument, 0) >= 0 {
			return errors.New("guest command argument is invalid")
		}
	}
	workdir, err := s.safePath(request.Workdir)
	if err != nil {
		return err
	}
	environment, err := guestProcessEnv(request.Env)
	if err != nil {
		return err
	}
	executable, err := resolveExecutable(request.Command[0], request.Env["PATH"])
	if err != nil {
		return s.write("exec_result", guestproto.ExecResult{ExitCode: -1, Error: boundedError(err)})
	}
	output := &eventWriter{server: s}
	err = guestCommandRunner(ctx, executable, request.Command[1:], workdir, environment, output)
	result := guestproto.ExecResult{}
	if err != nil {
		var coder interface{ ExitCode() int }
		if errors.As(err, &coder) {
			result.ExitCode = coder.ExitCode()
		} else {
			result.ExitCode = -1
			result.Error = boundedError(err)
		}
	}
	return s.write("exec_result", result)
}

var guestCommandRunner = runGuestCommandDirect

func runGuestCommandDirect(ctx context.Context, executable string, args []string, workdir string, environment []string, output io.Writer) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = workdir
	command.Env = environment
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		return <-done
	}
}

func guestProcessEnv(requested map[string]string) ([]string, error) {
	env := make(map[string]string, len(requested)+4)
	for name, value := range requested {
		if !envNamePattern.MatchString(name) || len(value) > 4*1024*1024 || strings.IndexByte(value, 0) >= 0 {
			return nil, errors.New("guest command environment is invalid")
		}
		env[name] = value
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		env["HOME"] = home
	}
	if current, err := user.Current(); err == nil && current.Username != "" {
		env["USER"] = current.Username
		env["LOGNAME"] = current.Username
	}
	if strings.TrimSpace(env["PATH"]) == "" {
		env["PATH"] = container.GuestImagePath
	}
	environment := make([]string, 0, len(env))
	for name, value := range env {
		environment = append(environment, name+"="+value)
	}
	return environment, nil
}

func resolveExecutable(name, pathValue string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		return name, nil
	}
	if pathValue == "" {
		pathValue = container.GuestImagePath
	}
	for _, directory := range filepath.SplitList(pathValue) {
		if !filepath.IsAbs(directory) {
			continue
		}
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("executable %q was not found in the requested PATH", name)
}

func (s *Server) archive(message guestproto.Message) error {
	var request guestproto.ArchiveRequest
	if err := guestproto.DecodeBody(message, &request); err != nil {
		return err
	}
	path, err := s.safePath(request.Path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > guestproto.MaxMessageBytes/2 {
		return errors.New("archive source must be a bounded regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var buffer strings.Builder
	tarWriter := tar.NewWriter(&stringWriter{builder: &buffer})
	if err := tarWriter.WriteHeader(&tar.Header{Name: filepath.Base(path), Mode: int64(info.Mode().Perm()), Size: int64(len(content))}); err != nil {
		return err
	}
	if _, err := tarWriter.Write(content); err != nil {
		return err
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	return s.write("archive_data", guestproto.Data{Bytes: []byte(buffer.String())})
}

func (s *Server) safePath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("guest path must be absolute")
	}
	clean, err := resolveWithMissing(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(s.root, clean)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("guest path escapes the session root")
	}
	return clean, nil
}

func resolveWithMissing(path string) (string, error) {
	current := path
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func (s *Server) write(messageType string, body any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writer.Write(messageType, body)
}

type eventWriter struct{ server *Server }

func (w *eventWriter) Write(content []byte) (int, error) {
	written := len(content)
	for len(content) > 0 {
		size := len(content)
		if size > 64*1024 {
			size = 64 * 1024
		}
		if err := w.server.write("output", guestproto.Data{Bytes: append([]byte(nil), content[:size]...)}); err != nil {
			return 0, err
		}
		content = content[size:]
	}
	return written, nil
}

type stringWriter struct{ builder *strings.Builder }

func (w *stringWriter) Write(content []byte) (int, error) {
	return w.builder.WriteString(string(content))
}

func boundedError(err error) string {
	message := err.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}
