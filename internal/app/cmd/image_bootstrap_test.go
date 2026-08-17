// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestBootstrapPasswordUsesDocumentedDefaultAndSecureFile(t *testing.T) {
	value, err := bootstrapPassword(imageBootstrapOptions{})
	require.NoError(t, err)
	require.Equal(t, "lume", value)

	path := filepath.Join(t.TempDir(), "password")
	require.NoError(t, os.WriteFile(path, []byte(" temporary-password\n"), 0o600))
	value, err = bootstrapPassword(imageBootstrapOptions{passwordFile: path})
	require.NoError(t, err)
	require.Equal(t, "temporary-password", value)

	require.NoError(t, os.Chmod(path, 0o644))
	_, err = bootstrapPassword(imageBootstrapOptions{passwordFile: path})
	require.ErrorContains(t, err, "owner-only")
	_, err = bootstrapPassword(imageBootstrapOptions{password: "one", passwordFile: path})
	require.ErrorContains(t, err, "either")
}

func TestBootstrapScriptContainsRequiredSecurityTransitions(t *testing.T) {
	script := bootstrapScript("1.2.3", "git.internal.example")
	for _, required := range []string{
		"gitea-runner-lume 1.2.3", bootstrapNodeVersion, bootstrapNodeSHA256,
		"shasum -a 256 -c", "PasswordAuthentication no", "KbdInteractiveAuthentication no",
		"no valid Command Line Tools update was found",
		"dseditgroup -o edit -d lume -t user admin", "autoLoginUser lume", "/etc/kcpassword",
		"sysadminctl -resetPasswordFor lume",
		"/etc/gitea-runner-lume/guest.key", "/etc/gitea-runner-lume/host.pub",
		"GRL_ROOT_BOOTSTRAP_OK", "grl-maintenance", "/sbin/shutdown -h now",
		"/tmp/gitea-runner-lume.hosts", "git.internal.example",
	} {
		require.Contains(t, script, required)
	}
	for _, forbidden := range []string{
		"image-signing.key", ".runner", "GITEA_RUNNER_REGISTRATION_TOKEN", "shared-dir",
		"defaults delete /Library/Preferences/com.apple.loginwindow autoLoginUser",
		"dscl . -delete /Users/lume dsAttrTypeNative:ShadowHashData",
	} {
		require.NotContains(t, script, forbidden)
	}
	require.Less(t, strings.Index(script, "test \"$(node --version)\""), strings.Index(script, "dseditgroup -o edit -d lume"))
	require.Contains(t, bootstrapVerificationCommand("1.2.3", "git.internal.example"), "autoLoginUser")
	require.Contains(t, bootstrapVerificationCommand("1.2.3", "git.internal.example"), "launchctl print gui/")
}

func TestRegisteredGiteaHostnameAcceptsOnlySafeHostnamesAndIPs(t *testing.T) {
	hostname, isIP, err := registeredGiteaHostname("https://Git.Internal.Example./owner/repo")
	require.NoError(t, err)
	require.Equal(t, "git.internal.example", hostname)
	require.False(t, isIP)

	hostname, isIP, err = registeredGiteaHostname("https://100.85.243.16:3000")
	require.NoError(t, err)
	require.Equal(t, "100.85.243.16", hostname)
	require.True(t, isIP)

	_, _, err = registeredGiteaHostname("https://bad'host.example")
	require.ErrorContains(t, err, "invalid DNS hostname")
}

func TestParseBootstrapVerificationValidatesGuestPublicKey(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshKey, err := ssh.NewPublicKey(publicKey)
	require.NoError(t, err)
	encoded := ssh.MarshalAuthorizedKey(sshKey)
	output := append([]byte("GRL_BOOTSTRAP_OK\n"), encoded...)
	output = append(output, []byte("v24.19.0\ngit version 2.50.1\n")...)

	parsed, err := parseBootstrapVerification(output)
	require.NoError(t, err)
	require.Equal(t, encoded, parsed)
	_, err = parseBootstrapVerification([]byte("not trusted\n"))
	require.Error(t, err)
}

func TestWritePublicIdentityIsAtomicIdempotentAndRefusesMismatch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "guest.pub")
	require.NoError(t, writePublicIdentity(path, []byte("identity\n")))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
	require.NoError(t, writePublicIdentity(path, []byte("identity\n")))
	require.ErrorContains(t, writePublicIdentity(path, []byte("changed\n")), "does not match")
}

func TestLoadPinnedHostKeyFindsOnlyConfiguredImage(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshKey, err := ssh.NewPublicKey(publicKey)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(path, []byte("base "+strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshKey)))+"\n"), 0o644))

	loaded, err := loadPinnedHostKey(path, "base")
	require.NoError(t, err)
	require.Equal(t, sshKey.Marshal(), loaded.Marshal())
	_, err = loadPinnedHostKey(path, "other")
	require.ErrorContains(t, err, "no entry")
}
