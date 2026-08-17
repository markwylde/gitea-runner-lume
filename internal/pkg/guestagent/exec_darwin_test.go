// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

//go:build darwin

package guestagent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAquaLaunchPlistIsAquaInteractiveAndEscapesPaths(t *testing.T) {
	plist := string(aquaLaunchPlist(
		"net.gitea.runner-lume.exec.1",
		`/tmp/run & "job".sh`,
		"/tmp/work",
		"/tmp/out",
		"/tmp/err",
	))
	require.Contains(t, plist, "<string>Aqua</string>")
	require.Contains(t, plist, "<string>Interactive</string>")
	require.Contains(t, plist, "<true/>")
	require.Contains(t, plist, "/tmp/run &amp; &quot;job&quot;.sh")
	require.NotContains(t, plist, `run & "job"`)
}

func TestShQuotePreservesSpacesAndQuotes(t *testing.T) {
	require.Equal(t, `'hello'`, shQuote("hello"))
	require.Equal(t, `'it'"'"'s fine'`, shQuote("it's fine"))
}

func TestDarwinAquaProbeUsesLaunchctlManagername(t *testing.T) {
	name, err := launchctlOutput("managername")
	require.NoError(t, err)
	require.NotEmpty(t, strings.TrimSpace(name))
}
