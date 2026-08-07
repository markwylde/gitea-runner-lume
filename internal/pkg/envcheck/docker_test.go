// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package envcheck

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckIfDockerRunningReturnsPingError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := CheckIfDockerRunning(ctx, "unix:///definitely/missing/docker.sock")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot ping the docker daemon")
}
