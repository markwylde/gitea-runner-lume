// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"context"
	"errors"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsAddressPoolExhausted(t *testing.T) {
	assert.True(t, isAddressPoolExhausted(cerrdefs.ErrInvalidArgument.WithMessage("Error response from daemon: all predefined address pools have been fully subnetted")))
	assert.True(t, isAddressPoolExhausted(errors.New("could not find an available, non-overlapping IPv4 address pool among the defaults to assign to the network")))
	assert.False(t, isAddressPoolExhausted(cerrdefs.ErrInvalidArgument.WithMessage("invalid subnet 10.0.0.0/8: it overlaps with an existing network")))
}

// Of this runner's networks, only the ones nothing is attached to and old enough to predate
// any job now starting are the runner's to reclaim. An unexpected NetworkRemove fails the
// test on its own, since testify has no expectation to match it against.
func TestRemoveOrphanNetworks(t *testing.T) {
	ctx := context.Background()
	cutoff := time.Date(2026, time.April, 29, 20, 0, 0, 0, time.UTC)
	client := &mockDockerClient{}
	client.On("NetworkList", ctx, mobyclient.NetworkListOptions{
		Filters: make(mobyclient.Filters).Add("label", runnerUUIDLabel+"=runner-1"),
	}).Return(mobyclient.NetworkListResult{Items: []network.Summary{
		{Network: network.Network{ID: "orphan"}},
		{Network: network.Network{ID: "busy"}},
		{Network: network.Network{ID: "starting"}},
	}}, nil)
	client.On("NetworkInspect", ctx, "orphan", mobyclient.NetworkInspectOptions{}).
		Return(mobyclient.NetworkInspectResult{}, nil)
	client.On("NetworkInspect", ctx, "busy", mobyclient.NetworkInspectOptions{}).
		Return(mobyclient.NetworkInspectResult{Network: network.Inspect{Containers: map[string]network.EndpointResource{"c": {}}}}, nil)
	client.On("NetworkInspect", ctx, "starting", mobyclient.NetworkInspectOptions{}).
		Return(mobyclient.NetworkInspectResult{Network: network.Inspect{Network: network.Network{Created: cutoff.Add(time.Second)}}}, nil)
	client.On("NetworkRemove", ctx, "orphan", mobyclient.NetworkRemoveOptions{}).
		Return(mobyclient.NetworkRemoveResult{}, nil)

	require.NoError(t, removeOrphanNetworks(ctx, client, "runner-1", cutoff))
	client.AssertExpectations(t)
}
