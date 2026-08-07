// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLaunchAgentContainsOnlyBinaryConfigAndLogPaths(t *testing.T) {
	var output bytes.Buffer
	err := launchAgentTemplate.Execute(&output, map[string]string{
		"Label": serviceLabel, "Binary": "/Applications/Runner & Tools/runner",
		"Config": "/Users/runner/config.yaml", "WorkingDirectory": "/Users/runner/Runner & State", "Stdout": "/tmp/out", "Stderr": "/tmp/err",
	})
	require.NoError(t, err)
	text := output.String()
	require.True(t, strings.HasPrefix(text, "<!DOCTYPE plist"))
	require.NotContains(t, text, "&lt;?")
	require.Contains(t, text, "/Applications/Runner &amp; Tools/runner")
	require.Contains(t, text, "/Users/runner/Runner &amp; State")
	for _, forbidden := range []string{"token", "secret", "webhook", "GITEA_RUNNER_REGISTRATION_TOKEN"} {
		require.NotContains(t, strings.ToLower(text), strings.ToLower(forbidden))
	}
}

func TestWaitForServiceUnload(t *testing.T) {
	calls := 0
	require.NoError(t, waitForServiceUnload(time.Second, func() error {
		calls++
		if calls < 3 {
			return nil
		}
		return assert.AnError
	}))
	require.Equal(t, 3, calls)
	require.ErrorContains(t, waitForServiceUnload(0, func() error { return nil }), "timed out")
}

func TestValidateLaunchAgentDocument(t *testing.T) {
	document := launchAgentDocument{
		Label:             serviceLabel,
		ProgramArguments:  []string{"/usr/local/bin/runner", "--config", "/tmp/config.yaml", "daemon"},
		WorkingDirectory:  "/tmp/runner",
		StandardOutPath:   "/tmp/out.log",
		StandardErrorPath: "/tmp/err.log",
	}
	require.NoError(t, validateLaunchAgentDocument(document, "/usr/local/bin/runner", "/tmp/config.yaml"))

	tests := map[string]func(*launchAgentDocument){
		"wrong label":          func(item *launchAgentDocument) { item.Label = "attacker" },
		"changed arguments":    func(item *launchAgentDocument) { item.ProgramArguments = append(item.ProgramArguments, "--debug") },
		"environment injected": func(item *launchAgentDocument) { item.EnvironmentVariables = map[string]string{"TOKEN": "secret"} },
		"relative working dir": func(item *launchAgentDocument) { item.WorkingDirectory = "runner" },
		"relative stdout":      func(item *launchAgentDocument) { item.StandardOutPath = "out.log" },
		"relative stderr":      func(item *launchAgentDocument) { item.StandardErrorPath = "err.log" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			copy := document
			copy.ProgramArguments = append([]string(nil), document.ProgramArguments...)
			mutate(&copy)
			require.Error(t, validateLaunchAgentDocument(copy, "/usr/local/bin/runner", "/tmp/config.yaml"))
		})
	}
}
