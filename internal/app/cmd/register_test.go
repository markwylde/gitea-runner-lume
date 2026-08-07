// Copyright 2025 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"gitea.com/gitea/runner/internal/pkg/config"

	"github.com/stretchr/testify/require"
	"gotest.tools/v3/assert"
)

func TestRegisterNonInteractiveReturnsLabelValidationError(t *testing.T) {
	err := registerNoInteractive(t.Context(), "", &registerArgs{
		Labels:       "ubuntu:host,,broken",
		Token:        "token",
		InstanceAddr: "http://localhost:3000",
	})
	assert.ErrorContains(t, err, "empty label")
}

func TestRegisterInputsValidate(t *testing.T) {
	tests := []struct {
		name    string
		inputs  registerInputs
		wantErr string
	}{
		{
			name:    "empty instance address",
			inputs:  registerInputs{Token: "token"},
			wantErr: "instance address is empty",
		},
		{
			name:    "empty token",
			inputs:  registerInputs{InstanceAddr: "http://localhost:3000"},
			wantErr: "token is empty",
		},
		{
			name:    "invalid label",
			inputs:  registerInputs{InstanceAddr: "http://localhost:3000", Token: "token", Labels: []string{""}},
			wantErr: "empty label",
		},
		{
			name:   "valid",
			inputs: registerInputs{InstanceAddr: "http://localhost:3000", Token: "token", Labels: []string{"ubuntu:host"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.inputs.validate()
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateLabels(t *testing.T) {
	require.NoError(t, validateLabels([]string{"ubuntu:host", "ubuntu:docker://node:18"}))
	// a colon that is not a supported schema is part of the label name
	require.NoError(t, validateLabels([]string{"pool:e57e18d4-10d4-406f-93bf-60f127221bdd"}))
	require.Error(t, validateLabels([]string{"ubuntu:host", ""}))
}

func TestRegisterInputsStageValue(t *testing.T) {
	inputs := &registerInputs{
		InstanceAddr: "http://localhost:3000",
		Token:        "token",
		RunnerName:   "runner",
		Labels:       []string{"ubuntu:host", "ubuntu:docker://node:18"},
	}
	require.Equal(t, "http://localhost:3000", inputs.stageValue(StageInputInstance))
	require.Equal(t, "token", inputs.stageValue(StageInputToken))
	require.Equal(t, "runner", inputs.stageValue(StageInputRunnerName))
	require.Equal(t, "ubuntu:host,ubuntu:docker://node:18", inputs.stageValue(StageInputLabels))
	require.Empty(t, (&registerInputs{}).stageValue(StageInputLabels))
	require.Empty(t, inputs.stageValue(StageWaitingForRegistration))
}

func TestRegisterInputsAssignToNext(t *testing.T) {
	emptyCfg := &config.Config{}

	t.Run("instance and token stay on empty value", func(t *testing.T) {
		inputs := &registerInputs{}
		require.Equal(t, StageInputInstance, inputs.assignToNext(StageInputInstance, "", emptyCfg))
		require.Equal(t, StageInputToken, inputs.assignToNext(StageInputToken, "", emptyCfg))
	})

	t.Run("instance then token then runner name", func(t *testing.T) {
		inputs := &registerInputs{}
		require.Equal(t, StageInputToken, inputs.assignToNext(StageInputInstance, "http://localhost:3000", emptyCfg))
		require.Equal(t, "http://localhost:3000", inputs.InstanceAddr)
		require.Equal(t, StageInputRunnerName, inputs.assignToNext(StageInputToken, "token", emptyCfg))
		require.Equal(t, "token", inputs.Token)
	})

	t.Run("empty runner name falls back to hostname", func(t *testing.T) {
		inputs := &registerInputs{}
		require.Equal(t, StageInputLabels, inputs.assignToNext(StageInputRunnerName, "", emptyCfg))
		hostname, _ := os.Hostname()
		require.Equal(t, hostname, inputs.RunnerName)
	})

	t.Run("labels from config skip the labels stage", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Runner.Labels = []string{"ubuntu:host", "", "pool:e57e18d4"}
		inputs := &registerInputs{}
		require.Equal(t, StageWaitingForRegistration, inputs.assignToNext(StageInputRunnerName, "runner", cfg))
		require.Equal(t, []string{"ubuntu:host", "pool:e57e18d4"}, inputs.Labels)
	})

	t.Run("blank labels input uses defaults", func(t *testing.T) {
		inputs := &registerInputs{}
		require.Equal(t, StageWaitingForRegistration, inputs.assignToNext(StageInputLabels, "", emptyCfg))
		require.Equal(t, defaultLabels, inputs.Labels)
	})

	t.Run("invalid labels input loops back", func(t *testing.T) {
		inputs := &registerInputs{}
		require.Equal(t, StageInputLabels, inputs.assignToNext(StageInputLabels, "ubuntu:host,,bad", emptyCfg))
		require.Nil(t, inputs.Labels)
	})

	t.Run("labels containing a colon are accepted", func(t *testing.T) {
		inputs := &registerInputs{}
		require.Equal(t, StageWaitingForRegistration, inputs.assignToNext(StageInputLabels, "pool:e57e18d4,ubuntu:host", emptyCfg))
		require.Equal(t, []string{"pool:e57e18d4", "ubuntu:host"}, inputs.Labels)
	})

	t.Run("overwrite local config", func(t *testing.T) {
		inputs := &registerInputs{}
		require.Equal(t, StageInputInstance, inputs.assignToNext(StageOverwriteLocalConfig, "Y", emptyCfg))
		require.Equal(t, StageInputInstance, inputs.assignToNext(StageOverwriteLocalConfig, "y", emptyCfg))
		require.Equal(t, StageExit, inputs.assignToNext(StageOverwriteLocalConfig, "n", emptyCfg))
	})

	t.Run("unknown stage", func(t *testing.T) {
		inputs := &registerInputs{}
		require.Equal(t, StageUnknown, inputs.assignToNext(StageWaitingForRegistration, "x", emptyCfg))
	})
}

func TestInitInputs(t *testing.T) {
	t.Run("missing token", func(t *testing.T) {
		_, err := initInputs(&registerArgs{
			InstanceAddr: "http://localhost:3000",
			RunnerName:   "runner",
			Ephemeral:    true,
			Labels:       " ubuntu:host , ubuntu:docker://node:18 ",
		})
		require.EqualError(t, err, "missing token, token-file argument, or GITEA_RUNNER_REGISTRATION_TOKEN environment variable")
	})

	t.Run("empty token", func(t *testing.T) {
		t.Setenv(registerTokenEnvVar, "")
		_, err := initInputs(&registerArgs{
			InstanceAddr: "http://localhost:3000",
			Token:        "",
			TokenFile:    "",
			RunnerName:   "runner",
			Ephemeral:    true,
			Labels:       " ubuntu:host , ubuntu:docker://node:18 ",
		})
		require.EqualError(t, err, "missing token, token-file argument, or GITEA_RUNNER_REGISTRATION_TOKEN environment variable")
	})

	t.Run("invalid token file", func(t *testing.T) {
		t.Setenv(registerTokenEnvVar, "from-env")
		_, err := initInputs(&registerArgs{
			InstanceAddr: "http://localhost:3000",
			TokenFile:    "/tmp/nonexistent",
			RunnerName:   "runner",
			Ephemeral:    true,
			Labels:       " ubuntu:host , ubuntu:docker://node:18 ",
		})
		require.ErrorContains(t, err, "cannot read the token file: /tmp/nonexistent")
	})

	t.Run("valid token", func(t *testing.T) {
		t.Setenv(registerTokenEnvVar, "from-env")
		inputs, err := initInputs(&registerArgs{
			InstanceAddr: " http://localhost:3000 ",
			Token:        "from-plain-arg\n",
			RunnerName:   "runner\n",
			Ephemeral:    true,
			Labels:       " ubuntu:host , ubuntu:docker://node:18 ",
		})
		require.NoError(t, err)
		require.Equal(t, "http://localhost:3000", inputs.InstanceAddr)
		require.Equal(t, "from-plain-arg", inputs.Token)
		require.Equal(t, "runner", inputs.RunnerName)
		require.True(t, inputs.Ephemeral)
		require.Equal(t, []string{"ubuntu:host ", " ubuntu:docker://node:18"}, inputs.Labels)
	})

	t.Run("valid token file", func(t *testing.T) {
		t.Setenv(registerTokenEnvVar, "from-env")
		tokenFile, createErr := os.CreateTemp(t.TempDir(), "from-file")
		require.NoError(t, createErr)
		require.NoError(t, tokenFile.Chmod(0o600))
		defer tokenFile.Close()
		_, writeErr := tokenFile.WriteString("from-file\n")
		require.NoError(t, writeErr)
		_ = tokenFile.Sync()

		inputs, err := initInputs(&registerArgs{
			InstanceAddr: "http://localhost:3000",
			TokenFile:    tokenFile.Name(),
			RunnerName:   "runner",
			Ephemeral:    true,
			Labels:       " ubuntu:host , ubuntu:docker://node:18 ",
		})
		require.NoError(t, err)
		require.Equal(t, "http://localhost:3000", inputs.InstanceAddr)
		require.Equal(t, "from-file", inputs.Token)
		require.Equal(t, "runner", inputs.RunnerName)
		require.True(t, inputs.Ephemeral)
		require.Equal(t, []string{"ubuntu:host ", " ubuntu:docker://node:18"}, inputs.Labels)
	})

	t.Run("insecure token file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "token")
		require.NoError(t, os.WriteFile(path, []byte("secret"), 0o644))
		_, err := initInputs(&registerArgs{TokenFile: path})
		require.ErrorContains(t, err, "regular owner-only file")
	})

	t.Run("symlink token file", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target")
		path := filepath.Join(directory, "token")
		require.NoError(t, os.WriteFile(target, []byte("secret"), 0o600))
		require.NoError(t, os.Symlink(target, path))
		_, err := initInputs(&registerArgs{TokenFile: path})
		require.ErrorContains(t, err, "regular owner-only file")
	})

	t.Run("token from environment variable", func(t *testing.T) {
		t.Setenv(registerTokenEnvVar, "from-env")
		inputs, err := initInputs(&registerArgs{
			InstanceAddr: "http://localhost:3000",
			RunnerName:   "runner",
			Ephemeral:    true,
			Labels:       " ubuntu:host , ubuntu:docker://node:18 ",
		})
		require.NoError(t, err)
		require.Equal(t, "http://localhost:3000", inputs.InstanceAddr)
		require.Equal(t, "from-env", inputs.Token)
		require.Equal(t, "runner", inputs.RunnerName)
		require.True(t, inputs.Ephemeral)
		require.Equal(t, []string{"ubuntu:host ", " ubuntu:docker://node:18"}, inputs.Labels)
	})

	t.Run("empty labels", func(t *testing.T) {
		inputs, _ := initInputs(&registerArgs{
			Token:  "from-plain-arg",
			Labels: "  ",
		})
		require.Nil(t, inputs.Labels)
	})
}
