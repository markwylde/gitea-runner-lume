// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !(WITHOUT_DOCKER || !(linux || darwin || windows || netbsd))

package container

import (
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/kballard/go-shellquote"
	"github.com/spf13/pflag"
)

const (
	pullPolicyAlways  = "always"
	pullPolicyMissing = "missing"
	pullPolicyNever   = "never"
)

var pullPolicies = []string{pullPolicyAlways, pullPolicyMissing, pullPolicyNever}

// createFlags are the flags docker/cli registers on the `create` and `run` commands
// instead of in addFlags, so they are not part of containerOptions.
type createFlags struct {
	platform     string
	pull         string
	name         string
	useAPISocket bool
}

func registerCreateFlags(flags *pflag.FlagSet) *createFlags {
	cf := new(createFlags)
	flags.StringVar(&cf.platform, "platform", "", "Set platform if server is multi-platform capable")
	flags.StringVar(&cf.pull, "pull", pullPolicyMissing, `Pull image before creating ("always", "missing", "never")`)
	flags.StringVar(&cf.name, "name", "", "Assign a name to the container")
	flags.BoolVar(&cf.useAPISocket, "use-api-socket", false, "Bind mount Docker API socket and required auth")
	// Accepted without effect: pull progress is only logged at debug level, and docker
	// no longer implements content trust.
	flags.BoolP("quiet", "q", false, "Suppress the pull output")
	flags.Bool("disable-content-trust", true, "Skip image verification (deprecated)")
	return cf
}

// parseContainerOptions parses a container options string. The flags are returned even
// on error, holding whatever was read before the failure.
func parseContainerOptions(options string) (*pflag.FlagSet, *containerOptions, *createFlags, error) {
	flags := pflag.NewFlagSet("container_flags", pflag.ContinueOnError)
	flags.SetOutput(io.Discard)
	copts := addFlags(flags)
	cf := registerCreateFlags(flags)

	args, err := shellquote.Split(options)
	if err != nil {
		return flags, copts, cf, fmt.Errorf("Cannot split container options: '%s': '%w'", options, err)
	}

	if err := flags.Parse(args); err != nil {
		return flags, copts, cf, fmt.Errorf("Cannot parse container options: '%s': '%w'", options, err)
	}

	return flags, copts, cf, nil
}

// createFlagsFromOptions reads the create-level flags that have to be known before the
// container is created. Malformed options keep the defaults here and are reported by
// mergeContainerConfigs at create time.
func createFlagsFromOptions(options string) *createFlags {
	_, _, cf, _ := parseContainerOptions(options)
	return cf
}

func (cf *createFlags) validate() error {
	if !slices.Contains(pullPolicies, cf.pull) {
		return fmt.Errorf("invalid --pull option %q: must be one of %q", cf.pull, pullPolicies)
	}

	if cf.useAPISocket {
		return errors.New("--use-api-socket is not supported, use the runner's container.docker_host setting to expose a docker socket")
	}

	return nil
}
