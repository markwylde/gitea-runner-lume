// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"testing"

	"gitea.com/gitea/runner/internal/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanAcceptTaskDiskGuard(t *testing.T) {
	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	cfg.Host.WorkdirParent = t.TempDir()
	cfg.HealthCheck.Enabled = true
	r := &Runner{cfg: cfg}

	ready, _ := r.CanAcceptTask(t.Context())
	assert.True(t, ready)

	cfg.HealthCheck.MinFreeDiskSpaceMB = 1 << 40
	ready, reason := r.CanAcceptTask(t.Context())
	assert.False(t, ready)
	assert.Contains(t, reason, "low disk space")
}

func TestDiskCheckDeferredWhileJobRuns(t *testing.T) {
	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	cfg.HealthCheck.Enabled = true
	cfg.HealthCheck.MinFreeDiskSpaceMB = 1
	cfg.Host.WorkdirParent = t.TempDir()
	r := &Runner{cfg: cfg}

	ready, reason := r.CanAcceptTask(t.Context())
	assert.True(t, ready)
	assert.Equal(t, "ok", reason)

	cfg.HealthCheck.MinFreeDiskSpaceMB = 1 << 40
	r.runningCount.Store(1)
	ready, reason = r.CanAcceptTask(t.Context())
	assert.True(t, ready, "the disk check must not run while a job is active")
	assert.Equal(t, "ok", reason)

	r.runningCount.Store(0)
	ready, reason = r.CanAcceptTask(t.Context())
	assert.False(t, ready)
	assert.Contains(t, reason, "low disk space")
}
