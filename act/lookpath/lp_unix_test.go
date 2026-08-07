// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package lookpath

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

type testEnv map[string]string

func (e testEnv) Getenv(name string) string {
	return e[name]
}

func TestLookPath2SearchesPathAndEmptyElement(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := LookPath2("tool", testEnv{"PATH": string(filepath.ListSeparator) + dir})
	if err != nil {
		t.Fatal(err)
	}
	if got != exe {
		t.Fatalf("LookPath2() = %q, want %q", got, exe)
	}
}

func TestLookPath2DirectPathDoesNotSearchPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "tool")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := LookPath2(exe, testEnv{"PATH": ""})
	if err != nil {
		t.Fatal(err)
	}
	if got != exe {
		t.Fatalf("LookPath2() = %q, want %q", got, exe)
	}
}

func TestLookPath2ReportsPermissionAndNotFound(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(file, []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LookPath2(file, testEnv{"PATH": dir})
	var pathErr *Error
	if !errors.As(err, &pathErr) || !errors.Is(pathErr.Err, fs.ErrPermission) {
		t.Fatalf("LookPath2(non-executable) error = %v, want fs.ErrPermission wrapped in *Error", err)
	}
	if pathErr.Error() != fs.ErrPermission.Error() {
		t.Fatalf("Error() = %q, want %q", pathErr.Error(), fs.ErrPermission.Error())
	}

	_, err = LookPath2("missing", testEnv{"PATH": dir})
	if !errors.As(err, &pathErr) || !errors.Is(pathErr.Err, ErrNotFound) {
		t.Fatalf("LookPath2(missing) error = %v, want ErrNotFound wrapped in *Error", err)
	}
}
