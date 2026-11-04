// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestXcodeInstallRunsWhileAdministratorStillExists(t *testing.T) {
	script := bootstrapScript("1.2.3", "git.internal.example")
	install := strings.Index(script, "bootstrap: installing Xcode")
	require.Positive(t, install)
	require.Less(t, install, strings.Index(script, "dseditgroup -o edit -d lume"))
	for _, required := range []string{
		"codesign --verify --strict -R '" + xcodeCodeRequirement + "'", "CFBundleIdentifier)\" = 'com.apple.dt.Xcode'",
		"chown -R root:wheel '/Applications/Xcode.app'",
		"xcode-select --switch '/Applications/Xcode.app/Contents/Developer'",
		"xcodebuild -license accept", "xcodebuild -runFirstLaunch",
	} {
		require.Contains(t, script, required)
	}
}

func TestXcodeVerificationIsOnlyRequiredWhenRequested(t *testing.T) {
	require.NotContains(t, bootstrapVerificationCommand("1.2.3", "git.internal.example", false), "xcodebuild")
	verification := bootstrapVerificationCommand("1.2.3", "git.internal.example", true)
	require.Contains(t, verification, "xcode-select -p")
	require.Contains(t, verification, "xcodebuild -version")
}

func TestValidateXcodeBundleRefusesNonXcodeBundles(t *testing.T) {
	ctx := context.Background()
	path, err := validateXcodeBundle(ctx, "")
	require.NoError(t, err)
	require.Empty(t, path)

	directory := t.TempDir()
	notApp := filepath.Join(directory, "Xcode")
	require.NoError(t, os.Mkdir(notApp, 0o755))
	_, err = validateXcodeBundle(ctx, notApp)
	require.ErrorContains(t, err, ".app bundle")

	file := filepath.Join(directory, "Xcode.app")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	_, err = validateXcodeBundle(ctx, file)
	require.ErrorContains(t, err, ".app bundle")

}

func TestValidateXcodeBundleRefusesOtherAndUnsignedBundles(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("defaults and codesign are macOS-only")
	}
	other := filepath.Join(t.TempDir(), "Other.app", "Contents")
	require.NoError(t, os.MkdirAll(other, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(other, "Info.plist"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.example.Other</string></dict></plist>`), 0o644))
	_, err := validateXcodeBundle(context.Background(), filepath.Dir(other))
	require.ErrorContains(t, err, "is not com.apple.dt.Xcode")

	contents := filepath.Join(t.TempDir(), "Xcode.app", "Contents")
	require.NoError(t, os.MkdirAll(contents, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>com.apple.dt.Xcode</string></dict></plist>`), 0o644))
	_, err = validateXcodeBundle(context.Background(), filepath.Dir(contents))
	require.ErrorContains(t, err, "not a valid Apple-signed app")
}
