// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

//go:build darwin

package guestagent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func init() {
	guestCommandRunner = runGuestCommandDarwin
}

func waitForConsoleGUISession(ctx context.Context) error {
	if darwinInAquaSession() {
		return nil
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for console GUI session: %w", err)
		}
		if darwinHasGUIDomain() {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for the guest console GUI session")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for console GUI session: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func runGuestCommandDarwin(ctx context.Context, executable string, args []string, workdir string, environment []string, output io.Writer) error {
	if darwinInAquaSession() {
		return runGuestCommandDirect(ctx, executable, args, workdir, environment, output)
	}
	if !darwinHasGUIDomain() {
		return errors.New("guest console GUI session is unavailable")
	}
	return runGuestCommandInAqua(ctx, executable, args, workdir, environment, output)
}

func darwinInAquaSession() bool {
	name, err := launchctlOutput("managername")
	return err == nil && strings.TrimSpace(name) == "Aqua"
}

func darwinHasGUIDomain() bool {
	uid := os.Getuid()
	if uid < 1 {
		return false
	}
	return exec.Command("/bin/launchctl", "print", fmt.Sprintf("gui/%d", uid)).Run() == nil
}

func launchctlOutput(args ...string) (string, error) {
	command := exec.Command("/bin/launchctl", args...)
	output, err := command.Output()
	return string(output), err
}

var aquaExecSequence atomic.Uint64

func runGuestCommandInAqua(ctx context.Context, executable string, args []string, workdir string, environment []string, output io.Writer) error {
	uid := os.Getuid()
	id := aquaExecSequence.Add(1)
	directory, err := os.MkdirTemp(workdir, "grl-aqua-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}

	label := fmt.Sprintf("net.gitea.runner-lume.exec.%d.%d", os.Getpid(), id)
	envFile := filepath.Join(directory, "env.sh")
	runFile := filepath.Join(directory, "run.sh")
	plistFile := filepath.Join(directory, label+".plist")
	exitFile := filepath.Join(directory, "exit-code")
	stdoutFile := filepath.Join(directory, "stdout.log")
	stderrFile := filepath.Join(directory, "stderr.log")
	domain := fmt.Sprintf("gui/%d", uid)

	if err := writeAquaEnv(envFile, environment); err != nil {
		return err
	}
	if err := writeAquaRunner(runFile, envFile, workdir, executable, args, exitFile); err != nil {
		return err
	}
	if err := os.WriteFile(stdoutFile, nil, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(stderrFile, nil, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(plistFile, aquaLaunchPlist(label, runFile, workdir, stdoutFile, stderrFile), 0o600); err != nil {
		return err
	}

	_ = exec.Command("/bin/launchctl", "bootout", domain+"/"+label).Run()
	if err := exec.Command("/bin/launchctl", "bootstrap", domain, plistFile).Run(); err != nil {
		return fmt.Errorf("start Aqua job: %w", err)
	}
	defer func() {
		_ = exec.Command("/bin/launchctl", "bootout", domain+"/"+label).Run()
	}()

	stopTail := make(chan struct{})
	tailDone := make(chan struct{})
	go func() {
		defer close(tailDone)
		_ = followFiles(ctx, stopTail, output, stdoutFile, stderrFile)
	}()
	defer func() {
		close(stopTail)
		<-tailDone
	}()

	for {
		if data, readErr := os.ReadFile(exitFile); readErr == nil {
			status, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if convErr != nil {
				return convErr
			}
			if status != 0 {
				return &exitStatusError{code: status}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			_ = exec.Command("/bin/launchctl", "bootout", domain+"/"+label).Run()
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

type exitStatusError struct{ code int }

func (e *exitStatusError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e *exitStatusError) ExitCode() int { return e.code }

func writeAquaEnv(path string, environment []string) error {
	var body strings.Builder
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		body.WriteString("export ")
		body.WriteString(name)
		body.WriteString("=")
		body.WriteString(shQuote(value))
		body.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(body.String()), 0o600)
}

func writeAquaRunner(path, envFile, workdir, executable string, args []string, exitFile string) error {
	var body strings.Builder
	body.WriteString("#!/bin/bash\nset -euo pipefail\nset -a\n")
	body.WriteString("source ")
	body.WriteString(shQuote(envFile))
	body.WriteString("\nset +a\ncd ")
	body.WriteString(shQuote(workdir))
	body.WriteString("\nset +e\n")
	body.WriteString(shQuote(executable))
	for _, argument := range args {
		body.WriteByte(' ')
		body.WriteString(shQuote(argument))
	}
	body.WriteString("\nstatus=$?\nset -e\nprintf '%s\\n' \"$status\" > ")
	body.WriteString(shQuote(exitFile))
	body.WriteString("\nexit \"$status\"\n")
	if err := os.WriteFile(path, []byte(body.String()), 0o700); err != nil {
		return err
	}
	return nil
}

func aquaLaunchPlist(label, runFile, workdir, stdoutFile, stderrFile string) []byte {
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>`)
	body.WriteString(xmlEscape(label))
	body.WriteString(`</string>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>RunAtLoad</key>
	<true/>
	<key>WorkingDirectory</key>
	<string>`)
	body.WriteString(xmlEscape(workdir))
	body.WriteString(`</string>
	<key>ProgramArguments</key>
	<array>
		<string>/bin/bash</string>
		<string>`)
	body.WriteString(xmlEscape(runFile))
	body.WriteString(`</string>
	</array>
	<key>StandardOutPath</key>
	<string>`)
	body.WriteString(xmlEscape(stdoutFile))
	body.WriteString(`</string>
	<key>StandardErrorPath</key>
	<string>`)
	body.WriteString(xmlEscape(stderrFile))
	body.WriteString(`</string>
</dict>
</plist>
`)
	return []byte(body.String())
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return replacer.Replace(value)
}

func shQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func followFiles(ctx context.Context, stop <-chan struct{}, output io.Writer, paths ...string) error {
	offsets := make([]int64, len(paths))
	for {
		for i, path := range paths {
			file, err := os.Open(path)
			if err != nil {
				continue
			}
			if _, err := file.Seek(offsets[i], io.SeekStart); err != nil {
				_ = file.Close()
				continue
			}
			copied, err := io.Copy(output, bufio.NewReader(file))
			offsets[i] += copied
			_ = file.Close()
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-stop:
			return nil
		case <-time.After(50 * time.Millisecond):
		}
	}
}
