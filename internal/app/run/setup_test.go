// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gitea.com/gitea/runner/internal/pkg/labels"
	"gitea.com/gitea/runner/internal/pkg/ver"

	runnerv1 "gitea.dev/actions-proto-go/runner/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestSetupLines(t *testing.T) {
	original := osReleasePath
	path := filepath.Join(t.TempDir(), "os-release")
	require.NoError(t, os.WriteFile(path, []byte("PRETTY_NAME=\"Ubuntu 24.04.4 LTS\"\n"), 0o600))
	osReleasePath = path
	defer func() { osReleasePath = original }()

	r := &Runner{
		name: "gitea-com-gitea-0003",
		labels: labels.Labels{
			{Name: "ubuntu-latest", Schema: labels.SchemeDocker, Arg: "//node:20"},
			{Name: "ubuntu-22.04", Schema: labels.SchemeDocker, Arg: "//node:20"},
		},
	}
	taskCtx, err := structpb.NewStruct(map[string]any{
		"job":        "lint",
		"repository": "gitea/runner",
		"event_name": "pull_request",
	})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"gitea-com-gitea-0003(version:" + ver.Version() + ")",
		"::group::Runner Information",
		"Runner labels: ubuntu-latest, ubuntu-22.04",
		"Task: 268506",
		"Job: lint",
		"Repository: gitea/runner",
		"Triggered by event: pull_request",
		"::endgroup::",
		"::group::Operating System",
		"Ubuntu 24.04.4 LTS",
		fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		"::endgroup::",
	}, r.setupLines(&runnerv1.Task{Id: 268506, Context: taskCtx}))
}

func TestPrettyOSName(t *testing.T) {
	tests := map[string]struct {
		osRelease string
		want      string
	}{
		"quoted value": {
			osRelease: "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nPRETTY_NAME=\"Ubuntu 24.04.4 LTS\"\n",
			want:      "Ubuntu 24.04.4 LTS",
		},
		"unquoted value": {
			osRelease: "PRETTY_NAME=Alpine Linux v3.21\n",
			want:      "Alpine Linux v3.21",
		},
		"no pretty name": {
			osRelease: "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\n",
			want:      "",
		},
		// A key that merely ends in PRETTY_NAME must not be mistaken for it.
		"similar key": {
			osRelease: "IMAGE_PRETTY_NAME=\"Ubuntu Core 24\"\n",
			want:      "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "os-release")
			require.NoError(t, os.WriteFile(path, []byte(tt.osRelease), 0o600))

			original := osReleasePath
			osReleasePath = path
			defer func() { osReleasePath = original }()

			assert.Equal(t, tt.want, prettyOSName())
		})
	}

	t.Run("missing file", func(t *testing.T) {
		original := osReleasePath
		osReleasePath = filepath.Join(t.TempDir(), "absent")
		defer func() { osReleasePath = original }()

		assert.Empty(t, prettyOSName())
	})
}
