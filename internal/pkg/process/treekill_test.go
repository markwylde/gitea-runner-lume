// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package process

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewTreeKillConfiguresCommand(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sleep", "1")
	tk := NewTreeKill(cmd)

	require.NotNil(t, tk)
	require.NotNil(t, cmd.Cancel)
	require.Equal(t, treeKillWaitDelay, cmd.WaitDelay)
	require.NoError(t, cmd.Cancel())
}

func TestTreeKillCaptureStoresKiller(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sleep", "10")
	cmd.SysProcAttr = SysProcAttr("", false)
	tk := NewTreeKill(cmd)
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Wait() }()

	killer, err := tk.Capture(cmd.Process)
	require.NoError(t, err)
	require.NotNil(t, killer)
	require.NoError(t, cmd.Cancel())
	require.NoError(t, killer.Close())
}
