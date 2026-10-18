// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

//go:build darwin

package guestagent

import (
	"os"
	"os/exec"
	"path/filepath"
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

// The agent reads the exit file the moment it exists, so the file must never
// exist without its whole status: a run that exits leaves exactly its status
// there and nothing half-written beside it.
func TestAquaRunnerPublishesTheExitStatusWhole(t *testing.T) {
	directory := t.TempDir()
	workdir := filepath.Join(directory, "work")
	require.NoError(t, os.Mkdir(workdir, 0o700))
	envFile := filepath.Join(directory, "env.json")
	cmdFile := filepath.Join(directory, "cmd.json")
	runFile := filepath.Join(directory, "run.sh")
	exitFile := filepath.Join(directory, "exit-code")
	require.NoError(t, writeAquaEnv(envFile, []string{"PATH=/usr/bin:/bin"}))
	require.NoError(t, writeAquaCommand(cmdFile, "/bin/sh", []string{"-c", "exit 3"}))
	require.NoError(t, writeAquaRunner(runFile, envFile, cmdFile, workdir, exitFile))

	_ = exec.Command("/bin/bash", runFile).Run()

	data, err := os.ReadFile(exitFile)
	require.NoError(t, err)
	require.Equal(t, "3\n", string(data))
	_, err = os.Stat(exitFile + ".partial")
	require.True(t, os.IsNotExist(err), "no partial status is left behind")

	script, err := os.ReadFile(runFile)
	require.NoError(t, err)
	require.Contains(t, string(script), "os.replace(partial_path, exit_path)")
}

// A run that fails before the command starts still publishes a status, and
// publishes it the same way.
func TestAquaRunnerFallbackStatusIsPublishedWhole(t *testing.T) {
	directory := t.TempDir()
	runFile := filepath.Join(directory, "run.sh")
	exitFile := filepath.Join(directory, "exit-code")
	missing := filepath.Join(directory, "missing.json")
	require.NoError(t, writeAquaRunner(runFile, missing, missing, directory, exitFile))

	_ = exec.Command("/bin/bash", runFile).Run()

	data, err := os.ReadFile(exitFile)
	require.NoError(t, err)
	require.Equal(t, "127\n", string(data))
	_, err = os.Stat(exitFile + ".partial")
	require.True(t, os.IsNotExist(err), "no partial status is left behind")
}
