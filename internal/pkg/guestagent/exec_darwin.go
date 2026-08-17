// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

//go:build darwin

package guestagent

import (
	"bufio"
	"context"
	"encoding/json"
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
	directory, err := os.MkdirTemp(aquaControlDirParent(workdir), "grl-aqua-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}

	label := fmt.Sprintf("net.gitea.runner-lume.exec.%d.%d", os.Getpid(), id)
	envFile := filepath.Join(directory, "env.json")
	cmdFile := filepath.Join(directory, "cmd.json")
	runFile := filepath.Join(directory, "run.sh")
	plistFile := filepath.Join(directory, label+".plist")
	exitFile := filepath.Join(directory, "exit-code")
	stdoutFile := filepath.Join(directory, "stdout.log")
	stderrFile := filepath.Join(directory, "stderr.log")
	domain := fmt.Sprintf("gui/%d", uid)

	if err := writeAquaEnv(envFile, environment); err != nil {
		return err
	}
	if err := writeAquaCommand(cmdFile, executable, args); err != nil {
		return err
	}
	if err := writeAquaRunner(runFile, envFile, cmdFile, workdir, exitFile); err != nil {
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
		if aquaJobExited(domain, label) {
			for i := 0; i < 20; i++ {
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
				time.Sleep(50 * time.Millisecond)
			}
			return errors.New("Aqua job exited without writing a status")
		}
		select {
		case <-ctx.Done():
			_ = exec.Command("/bin/launchctl", "bootout", domain+"/"+label).Run()
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func aquaControlDirParent(workdir string) string {
	parent := filepath.Dir(workdir)
	if !filepath.IsAbs(parent) || parent == string(filepath.Separator) {
		return os.TempDir()
	}
	return parent
}

func aquaJobExited(domain, label string) bool {
	output, err := exec.Command("/bin/launchctl", "print", domain+"/"+label).CombinedOutput()
	if err != nil {
		return false
	}
	text := string(output)
	return strings.Contains(text, "state = not running") && strings.Contains(text, "last exit code")
}

type exitStatusError struct{ code int }

func (e *exitStatusError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e *exitStatusError) ExitCode() int { return e.code }

func writeAquaEnv(path string, environment []string) error {
	env := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		env[name] = value
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func writeAquaCommand(path, executable string, args []string) error {
	command := append([]string{executable}, args...)
	data, err := json.Marshal(command)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func writeAquaRunner(path, envFile, cmdFile, workdir, exitFile string) error {
	quotedExit := shQuote(exitFile)
	script := fmt.Sprintf(`#!/bin/bash
trap 'if [ ! -f %s ]; then printf "127\n" > %s; fi' EXIT
/usr/bin/python3 -u - %s %s %s %s <<'PY'
import json, os, subprocess, sys
env_path, cmd_path, workdir, exit_path = sys.argv[1:5]
with open(env_path, encoding="utf-8") as handle:
    env = {str(name): str(value) for name, value in json.load(handle).items()}
with open(cmd_path, encoding="utf-8") as handle:
    command = json.load(handle)
os.chdir(workdir)
result = subprocess.run(command, env=env)
with open(exit_path, "w", encoding="utf-8") as handle:
    handle.write("%%d\n" %% result.returncode)
raise SystemExit(result.returncode)
PY
`, quotedExit, quotedExit, shQuote(envFile), shQuote(cmdFile), shQuote(workdir), quotedExit)
	return os.WriteFile(path, []byte(script), 0o700)
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
