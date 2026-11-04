// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gitea.com/gitea/runner/internal/pkg/guestbootstrap"
)

const (
	xcodeBundleIdentifier = "com.apple.dt.Xcode"
	// Only Apple-signed bundles are accepted, on the controller and again in
	// the guest after transfer.
	xcodeCodeRequirement = "=anchor apple"
	xcodeStagingDir      = "/tmp/gitea-runner-lume.xcode"
	xcodeInstallPath     = "/Applications/Xcode.app"
)

// xcodeCommand lets tests replace the controller's macOS tools.
var xcodeCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// validateXcodeBundle checks an operator-supplied Xcode.app on the controller
// before anything is sent to the guest. It returns the cleaned bundle path.
func validateXcodeBundle(ctx context.Context, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	cleaned, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(cleaned)
	if err != nil {
		return "", fmt.Errorf("inspect Xcode bundle: %w", err)
	}
	if !info.IsDir() || filepath.Ext(cleaned) != ".app" {
		return "", errors.New("--xcode must name an Xcode .app bundle directory")
	}
	identifier, err := xcodeCommand(ctx, "/usr/bin/defaults", "read", filepath.Join(cleaned, "Contents", "Info"), "CFBundleIdentifier").Output()
	if err != nil {
		return "", fmt.Errorf("read Xcode bundle identifier: %w", err)
	}
	if strings.TrimSpace(string(identifier)) != xcodeBundleIdentifier {
		return "", fmt.Errorf("--xcode bundle is not %s", xcodeBundleIdentifier)
	}
	if output, err := xcodeCommand(ctx, "/usr/bin/codesign", "--verify", "--strict", "-R="+xcodeCodeRequirement, cleaned).CombinedOutput(); err != nil {
		return "", fmt.Errorf("Xcode bundle is not a valid Apple-signed app: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return cleaned, nil
}

// uploadXcodeBundle streams the bundle as a tar archive into the guest's
// staging directory. The root bootstrap script re-verifies and installs it.
func uploadXcodeBundle(ctx context.Context, client *guestbootstrap.Client, bundle string) error {
	archive := xcodeCommand(ctx, "/usr/bin/tar", "-C", filepath.Dir(bundle), "-cf", "-", filepath.Base(bundle))
	stdout, err := archive.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	archive.Stderr = &stderr
	if err := archive.Start(); err != nil {
		return fmt.Errorf("archive Xcode bundle: %w", err)
	}
	extract := "set -eu; rm -rf '" + xcodeStagingDir + "'; mkdir -m 0700 '" + xcodeStagingDir + "'; " +
		"tar -xpf - -C '" + xcodeStagingDir + "'; " +
		"test \"$(ls '" + xcodeStagingDir + "' | wc -l | tr -d ' ')\" = 1"
	_, uploadErr := client.RunStream(ctx, extract, stdout)
	if uploadErr != nil {
		_, _ = io.Copy(io.Discard, stdout)
	}
	archiveErr := archive.Wait()
	if uploadErr != nil {
		return fmt.Errorf("upload Xcode bundle: %w", uploadErr)
	}
	if archiveErr != nil {
		return fmt.Errorf("archive Xcode bundle: %w: %s", archiveErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// xcodeInstallScript runs as root inside bootstrapScript, while the guest
// still has an administrator, and only when an Xcode bundle was staged.
func xcodeInstallScript() string {
	return `
if [ -d '` + xcodeStagingDir + `' ]; then
  echo 'bootstrap: installing Xcode'
  staged="$(find '` + xcodeStagingDir + `' -mindepth 1 -maxdepth 1 -name '*.app' -type d)"
  test -n "$staged"
  test "$(defaults read "$staged/Contents/Info" CFBundleIdentifier)" = '` + xcodeBundleIdentifier + `'
  codesign --verify --strict '-R=` + xcodeCodeRequirement + `' "$staged"
  rm -rf '` + xcodeInstallPath + `'
  mv "$staged" '` + xcodeInstallPath + `'
  rm -rf '` + xcodeStagingDir + `'
  chown -R root:wheel '` + xcodeInstallPath + `'
  xcode-select --switch '` + xcodeInstallPath + `/Contents/Developer'
  xcodebuild -license accept
  xcodebuild -runFirstLaunch
fi
`
}

// xcodeVerificationCommand proves, as the workflow user, that full Xcode is
// the active developer directory.
func xcodeVerificationCommand(required bool) string {
	if !required {
		return "true"
	}
	return "test \"$(xcode-select -p)\" = '" + xcodeInstallPath + "/Contents/Developer'; xcodebuild -version >/dev/null"
}
