// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package cmd

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRootHelpExposesCompleteSafeCommandSurface(t *testing.T) {
	command := NewRootCommand(context.Background())
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"--help"})
	require.NoError(t, command.Execute())

	help := output.String()
	require.Contains(t, help, "Gitea Runner with disposable Lume macOS workers")
	for _, name := range []string{
		"bug-report", "cache-server", "cleanup", "config", "daemon", "doctor",
		"exec", "image", "init", "register", "service",
		"status", "version",
	} {
		require.Contains(t, help, "  "+name+" ", name)
	}
	for _, hidden := range []string{"generate-config", "guest"} {
		item, _, err := command.Find([]string{hidden})
		require.NoError(t, err)
		require.Equal(t, hidden, item.Name())
		require.NotContains(t, help, "  "+hidden+" ")
	}
	for _, forbidden := range []string{"webhook", "api-token", "repository owner", "repository name"} {
		require.NotContains(t, strings.ToLower(help), forbidden)
	}
}

func TestRootPublicFlagsAndSubcommands(t *testing.T) {
	command := NewRootCommand(context.Background())
	tests := map[string][]string{
		"init":            {"image", "instance", "name", "no-register", "profile", "storage", "storage-path", "token", "token-file"},
		"register":        {"ephemeral", "instance", "labels", "name", "no-interactive", "token", "token-file"},
		"daemon":          {"labels", "once"},
		"status":          {"json"},
		"doctor":          {"live-guest-profile"},
		"cleanup":         {"apply"},
		"image create":    {"ipsw", "profile", "unattended"},
		"image bootstrap": {"agent", "password", "password-file", "profile"},
		"image adopt":     {"profile", "signing-key-file"},
		"image validate":  {"profile"},
	}
	for path, flags := range tests {
		t.Run(path, func(t *testing.T) {
			item, _, err := command.Find(strings.Fields(path))
			require.NoError(t, err)
			for _, flag := range flags {
				require.NotNil(t, item.Flags().Lookup(flag), flag)
			}
		})
	}
	service, _, err := command.Find([]string{"service"})
	require.NoError(t, err)
	if runtime.GOOS != "darwin" {
		require.Empty(t, service.Commands())
		return
	}
	for _, child := range []string{"install", "start", "status", "stop", "uninstall"} {
		item, _, err := service.Find([]string{child})
		require.NoError(t, err)
		require.Equal(t, child, item.Name())
	}
}

func TestRootArgumentErrorIsBoundedAndDoesNotPrintUsage(t *testing.T) {
	command := NewRootCommand(context.Background())
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"doctor", "unexpected"})
	err := command.Execute()
	require.EqualError(t, err, "unknown command \"unexpected\" for \"gitea-runner-lume doctor\"")
	require.NotContains(t, output.String(), "Usage:")
	require.Less(t, output.Len(), 512)
}
