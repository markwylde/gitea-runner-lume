// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

//go:build darwin

package guestagent

import (
	"os"
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

func TestAquaEnvPreservesHyphenatedActionInputs(t *testing.T) {
	path := t.TempDir() + "/env.json"
	require.NoError(t, writeAquaEnv(path, []string{
		"PATH=/usr/bin",
		"INPUT_SSH-KNOWN-HOSTS=",
		"CI=true",
	}))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), `"INPUT_SSH-KNOWN-HOSTS":""`)
	require.NotContains(t, string(data), "export ")
}

func TestAquaRunnerLoadsEnvWithPythonNotBashExport(t *testing.T) {
	path := t.TempDir() + "/run.sh"
	require.NoError(t, writeAquaRunner(path, "/tmp/env.json", "/tmp/cmd.json", "/tmp/work", "/tmp/exit"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "/usr/bin/python3 -u")
	require.Contains(t, string(data), "json.load")
	require.NotContains(t, string(data), "source ")
}

func TestAquaControlDirStaysOutsideJobWorkdir(t *testing.T) {
	require.Equal(t, "/private/var/tmp/gitea-runner-lume/worker", aquaControlDirParent("/private/var/tmp/gitea-runner-lume/worker/work"))
	require.Equal(t, os.TempDir(), aquaControlDirParent("work"))
	require.Equal(t, os.TempDir(), aquaControlDirParent("/"))
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
