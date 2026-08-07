// Copyright 2023 The Gitea Authors. All rights reserved.
// Copyright 2023 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !(WITHOUT_DOCKER || !(linux || darwin || windows || netbsd))

package container

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitea.com/gitea/runner/act/common"

	"github.com/moby/moby/client"
)

const (
	networkCreateAttempts   = 3
	networkCreateRetryDelay = time.Second

	// marks the networks a runner creates for its jobs, so it can tell its own leftovers from
	// those of another runner sharing the daemon
	runnerUUIDLabel = "com.gitea.runner.uuid"
)

// RemoveOrphanNetworks removes the networks this runner created for jobs whose teardown did
// not get to them: the runner died with the job, the teardown timed out, or the network still
// had an endpoint on it at the time. Each one holds a subnet of the daemon's address pool
// until it is removed. Networks created after createdBefore are left alone, so a job starting
// while this runs cannot lose the network it has created but not yet attached a container to.
func RemoveOrphanNetworks(ctx context.Context, runnerUUID string, createdBefore time.Time) error {
	cli, err := GetDockerClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to connect to the docker daemon: %w", err)
	}
	defer cli.Close()

	return removeOrphanNetworks(ctx, cli, runnerUUID, createdBefore)
}

func removeOrphanNetworks(ctx context.Context, cli client.APIClient, runnerUUID string, createdBefore time.Time) error {
	networks, err := cli.NetworkList(ctx, client.NetworkListOptions{
		Filters: make(client.Filters).Add("label", runnerUUIDLabel+"="+runnerUUID),
	})
	if err != nil {
		return err
	}

	var errs []error
	for _, n := range networks.Items {
		result, err := cli.NetworkInspect(ctx, n.ID, client.NetworkInspectOptions{})
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to inspect network %s: %w", n.Name, err))
			continue
		}
		// the emptiness check, not the label, is what keeps a live job of another process
		// sharing this registration safe
		if len(result.Network.Containers) != 0 || result.Network.Created.After(createdBefore) {
			continue
		}
		if _, err := cli.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err != nil {
			errs = append(errs, fmt.Errorf("failed to remove network %s: %w", n.Name, err))
			continue
		}
		common.Logger(ctx).Infof("removed docker network %s left behind by an earlier job", n.Name)
	}
	return errors.Join(errs...)
}

func NewDockerNetworkCreateExecutor(name string, opts NewDockerNetworkCreateExecutorInput) common.Executor {
	return func(ctx context.Context) error {
		cli, err := GetDockerClient(ctx)
		if err != nil {
			return err
		}
		defer cli.Close()

		// Only create the network if it doesn't exist
		networks, err := cli.NetworkList(ctx, client.NetworkListOptions{})
		if err != nil {
			return err
		}
		// For Gitea, reduce log noise
		// common.Logger(ctx).Debugf("%v", networks)
		for _, n := range networks.Items {
			if n.Name == name {
				common.Logger(ctx).Debugf("Network %v exists", name)
				return nil
			}
		}

		for i := range networkCreateAttempts {
			if i > 0 {
				common.Logger(ctx).Infof("Waiting for a free docker address pool to create network %s", name)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(i) * networkCreateRetryDelay):
				}
			}
			if _, err = cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
				Driver:     "bridge",
				Scope:      "local",
				EnableIPv4: opts.EnableIPv4,
				EnableIPv6: opts.EnableIPv6,
				Labels:     runnerLabels(opts.RunnerUUID),
			}); err == nil {
				return nil
			}
			if !isAddressPoolExhausted(err) {
				return err
			}
		}
		return fmt.Errorf("docker has no address pool left for this job's network, lower runner.capacity or widen default-address-pools in the docker daemon config: %w", err)
	}
}

func runnerLabels(runnerUUID string) map[string]string {
	if runnerUUID == "" {
		return nil
	}
	return map[string]string{runnerUUIDLabel: runnerUUID}
}

// The daemon reports this as a plain invalid-parameter error, the same kind it uses for every
// malformed request, so the message is the only discriminator.
func isAddressPoolExhausted(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "all predefined address pools have been fully subnetted") ||
		strings.Contains(msg, "could not find an available, non-overlapping IPv4 address pool among the defaults") // docker 24 and older
}

func NewDockerNetworkRemoveExecutor(name string) common.Executor {
	return func(ctx context.Context) error {
		cli, err := GetDockerClient(ctx)
		if err != nil {
			return err
		}
		defer cli.Close()

		// Make sure that all network of the specified name are removed
		// cli.NetworkRemove refuses to remove a network if there are duplicates
		networks, err := cli.NetworkList(ctx, client.NetworkListOptions{})
		if err != nil {
			return err
		}
		// For Gitea, reduce log noise
		// common.Logger(ctx).Debugf("%v", networks)
		var errs []error
		for _, n := range networks.Items {
			if n.Name == name {
				result, err := cli.NetworkInspect(ctx, n.ID, client.NetworkInspectOptions{})
				if err != nil {
					return err
				}

				// it holds a subnet out of the daemon's pool until something reclaims it
				if len(result.Network.Containers) != 0 {
					common.Logger(ctx).Warnf("Refusing to remove network %s because it still has active endpoints, the idle cleanup reclaims it once they are gone", name)
					continue
				}
				if _, err = cli.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err != nil {
					errs = append(errs, fmt.Errorf("failed to remove network %s: %w", name, err))
				}
			}
		}

		return errors.Join(errs...)
	}
}
