// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitea.com/gitea/runner/internal/pkg/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfiguredHealthCheckCachesAndRecovers(t *testing.T) {
	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	cfg.HealthCheck.Enabled = true
	cfg.HealthCheck.Script = "/health-check"
	cfg.HealthCheck.Interval = time.Minute
	cfg.HealthCheck.Timeout = time.Second

	now := time.Now()
	calls := 0
	fail := true
	r := &Runner{
		cfg:  cfg,
		name: "runner-1",
		now:  func() time.Time { return now },
		runHealthCheck: func(_ context.Context, script string, timeout time.Duration, env map[string]string) error {
			calls++
			assert.Equal(t, "/health-check", script)
			assert.Equal(t, time.Second, timeout)
			assert.Equal(t, "true", env["GITEA_RUNNER_HEALTH_CHECK"])
			assert.Equal(t, "runner-1", env["GITEA_RUNNER_NAME"])
			if fail {
				return errors.New("unhealthy")
			}
			return nil
		},
	}

	ready, reason := r.CanAcceptTask(t.Context())
	assert.False(t, ready)
	assert.Contains(t, reason, "unhealthy")
	assert.Equal(t, 1, calls)

	fail = false
	ready, _ = r.CanAcceptTask(t.Context())
	assert.False(t, ready, "the failed result should remain cached")
	assert.Equal(t, 1, calls)

	now = now.Add(time.Minute)
	ready, reason = r.CanAcceptTask(t.Context())
	assert.True(t, ready)
	assert.Equal(t, "ok", reason)
	assert.Equal(t, 2, calls)
}

func TestConfiguredHealthCheckDisabled(t *testing.T) {
	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	cfg.HealthCheck.MinFreeDiskSpaceMB = 1 << 40
	cfg.HealthCheck.Script = "/must-not-run"
	r := &Runner{
		cfg: cfg,
		runHealthCheck: func(_ context.Context, _ string, _ time.Duration, _ map[string]string) error {
			t.Fatal("disabled health check executed")
			return nil
		},
	}
	ready, reason := r.CanAcceptTask(t.Context())
	assert.True(t, ready)
	assert.Equal(t, "ok", reason)
}

func TestConfiguredHealthCheckDeferredWhileJobRuns(t *testing.T) {
	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	cfg.HealthCheck.Enabled = true
	cfg.HealthCheck.Script = "/health-check"
	cfg.HealthCheck.Interval = time.Minute

	now := time.Now()
	calls := 0
	fail := false
	r := &Runner{
		cfg: cfg,
		now: func() time.Time { return now },
		runHealthCheck: func(_ context.Context, _ string, _ time.Duration, _ map[string]string) error {
			calls++
			if fail {
				return errors.New("unhealthy")
			}
			return nil
		},
	}

	ready, reason := r.CanAcceptTask(t.Context())
	assert.True(t, ready)
	assert.Equal(t, "ok", reason)
	assert.Equal(t, 1, calls)

	now = now.Add(time.Minute)
	fail = true
	r.runningCount.Store(1)
	ready, reason = r.CanAcceptTask(t.Context())
	assert.True(t, ready, "the last result should be reused while a job runs")
	assert.Equal(t, "ok", reason)
	assert.Equal(t, 1, calls)

	r.runningCount.Store(0)
	ready, reason = r.CanAcceptTask(t.Context())
	assert.False(t, ready)
	assert.Contains(t, reason, "unhealthy")
	assert.Equal(t, 2, calls)
}

func TestConfiguredHealthCheckInitialRunDeferredWhileJobRuns(t *testing.T) {
	cfg, err := config.LoadDefault("")
	require.NoError(t, err)
	cfg.HealthCheck.Enabled = true
	cfg.HealthCheck.Script = "/health-check"

	calls := 0
	r := &Runner{
		cfg: cfg,
		runHealthCheck: func(_ context.Context, _ string, _ time.Duration, _ map[string]string) error {
			calls++
			return nil
		},
	}
	r.runningCount.Store(1)

	ready, reason := r.CanAcceptTask(t.Context())
	assert.True(t, ready)
	assert.Contains(t, reason, "deferred")
	assert.Zero(t, calls)

	r.runningCount.Store(0)
	ready, reason = r.CanAcceptTask(t.Context())
	assert.True(t, ready)
	assert.Equal(t, "ok", reason)
	assert.Equal(t, 1, calls)
}
