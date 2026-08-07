// Copyright 2026 The Gitea Runner Lume Authors
// SPDX-License-Identifier: MIT

package cmd

import (
	"errors"
	"testing"
	"time"

	"gitea.com/gitea/runner/internal/pkg/lume"

	"github.com/stretchr/testify/require"
)

func TestFormatImageCreateProgress(t *testing.T) {
	progress := 42.5
	tests := []struct {
		name string
		vm   lume.VM
		err  error
		want string
	}{
		{"download", lume.VM{DownloadProgress: &progress}, nil, "creating base VM base: downloading 42% (1m0s elapsed)"},
		{"operation", lume.VM{ProvisioningOperation: "installing macOS"}, nil, "creating base VM base: installing macOS (1m0s elapsed)"},
		{"state", lume.VM{State: lume.StateProvisioning}, nil, "creating base VM base: provisioning (1m0s elapsed)"},
		{"unavailable", lume.VM{}, errors.New("not ready"), "creating base VM base (1m0s elapsed; waiting for Lume progress)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, formatImageCreateProgress("base", time.Minute, test.vm, test.err))
		})
	}
}
