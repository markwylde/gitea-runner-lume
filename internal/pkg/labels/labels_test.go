// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package labels

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gotest.tools/v3/assert"
)

func TestParse(t *testing.T) {
	tests := []struct {
		args    string
		want    *Label
		wantErr bool
	}{
		{
			args: "ubuntu:docker://node:18",
			want: &Label{
				Name:   "ubuntu",
				Schema: "docker",
				Arg:    "//node:18",
			},
			wantErr: false,
		},
		{
			args: "macos-arm64:lume://xcode-16",
			want: &Label{
				Name:   "macos-arm64",
				Schema: "lume",
				Arg:    "//xcode-16",
			},
			wantErr: false,
		},
		{
			args: "ubuntu:host",
			want: &Label{
				Name:   "ubuntu",
				Schema: "host",
				Arg:    "",
			},
			wantErr: false,
		},
		{
			args: "ubuntu",
			want: &Label{
				Name:   "ubuntu",
				Schema: "host",
				Arg:    "",
			},
			wantErr: false,
		},
		{
			args: "pool:e57e18d4-10d4-406f-93bf-60f127221bdd",
			want: &Label{
				Name:   "pool:e57e18d4-10d4-406f-93bf-60f127221bdd",
				Schema: "host",
				Arg:    "",
				Opaque: true,
			},
			wantErr: false,
		},
		{
			args: "ubuntu:vm:ubuntu-18.04",
			want: &Label{
				Name:   "ubuntu:vm:ubuntu-18.04",
				Schema: "host",
				Arg:    "",
				Opaque: true,
			},
			wantErr: false,
		},
		{
			args:    "",
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.args, func(t *testing.T) {
			got, err := Parse(tt.args)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.DeepEqual(t, got, tt.want)
		})
	}
}

// mustParse parses the given label strings, failing the test on any error.
func mustParse(t *testing.T, strs ...string) Labels {
	t.Helper()
	ls := make(Labels, 0, len(strs))
	for _, s := range strs {
		l, err := Parse(s)
		require.NoError(t, err)
		ls = append(ls, l)
	}
	return ls
}

func TestRequireDocker(t *testing.T) {
	tests := []struct {
		name string
		strs []string
		want bool
	}{
		{"empty", nil, false},
		{"only host", []string{"ubuntu:host", "self-hosted"}, false},
		{"has docker", []string{"ubuntu:host", "ubuntu:docker://node:18"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, mustParse(t, tt.strs...).RequireDocker())
		})
	}
}

func TestLumeLabels(t *testing.T) {
	ls := mustParse(t, "macos-arm64:lume://xcode-16", "linux:host")
	require.True(t, ls.RequireLume())
	profile, ok := ls[0].LumeProfile()
	require.True(t, ok)
	require.Equal(t, "xcode-16", profile)
	require.Equal(t, "lume://xcode-16", ls.PickPlatform([]string{"macos-arm64"}))
	require.Equal(t, []string{"macos-arm64:lume://xcode-16", "linux:host"}, ls.ToStrings())
}

func TestPickPlatform(t *testing.T) {
	ls := mustParse(t,
		"ubuntu:docker://node:18",
		"self-hosted:host",
	)

	tests := []struct {
		name   string
		runsOn []string
		want   string
	}{
		{"docker strips leading slashes", []string{"ubuntu"}, "node:18"},
		{"host maps to self-hosted marker", []string{"self-hosted"}, "-self-hosted"},
		{"first match wins", []string{"self-hosted", "ubuntu"}, "-self-hosted"},
		{"unknown falls back to default", []string{"windows"}, "docker.gitea.com/runner-images:ubuntu-latest"},
		{"no runsOn falls back to default", nil, "docker.gitea.com/runner-images:ubuntu-latest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ls.PickPlatform(tt.runsOn))
		})
	}
}

func TestNames(t *testing.T) {
	ls := mustParse(t, "ubuntu:docker://node:18", "self-hosted:host", "pool:e57e18d4")
	require.Equal(t, []string{"ubuntu", "self-hosted", "pool:e57e18d4"}, ls.Names())
	require.Empty(t, Labels{}.Names())
}

func TestToStrings(t *testing.T) {
	ls := mustParse(t,
		"ubuntu:docker://node:18",
		"self-hosted:host",
		"bare",
		"pool:e57e18d4",
	)
	require.Equal(t, []string{
		"ubuntu:docker://node:18",
		"self-hosted:host",
		"bare:host",
		"pool:e57e18d4",
	}, ls.ToStrings())
}

// a colon-containing name must survive a write to and read back from the .runner file
func TestOpaqueLabelRoundTrip(t *testing.T) {
	const raw = "pool:e57e18d4-10d4-406f-93bf-60f127221bdd"

	ls := mustParse(t, raw)
	require.Equal(t, []string{raw}, ls.ToStrings())

	again := mustParse(t, ls.ToStrings()...)
	require.Equal(t, ls, again)
	require.Equal(t, []string{raw}, again.Names())
	require.False(t, again.RequireDocker())
	require.Equal(t, "-self-hosted", again.PickPlatform([]string{raw}))
}
