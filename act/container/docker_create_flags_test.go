// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateFlagsFromOptions(t *testing.T) {
	for _, tc := range []struct {
		options  string
		platform string
		pull     string
	}{
		{"", "", pullPolicyMissing},
		{"-v /a:/b --platform=linux/arm64 --pull always", "linux/arm64", pullPolicyAlways},
		{"--platform linux/arm/v7 --pull never", "linux/arm/v7", pullPolicyNever},
		{`--platform "linux/amd64`, "", pullPolicyMissing}, // malformed, defaults kept
	} {
		t.Run(tc.options, func(t *testing.T) {
			cf := createFlagsFromOptions(tc.options)
			assert.Equal(t, tc.platform, cf.platform)
			assert.Equal(t, tc.pull, cf.pull)
		})
	}
}

func TestCreateFlagsValidate(t *testing.T) {
	for _, tc := range []struct {
		options string
		wantErr string
	}{
		{"--quiet --disable-content-trust --name mine", ""},
		{"--pull sometimes", `invalid --pull option "sometimes"`},
		{"--use-api-socket", "--use-api-socket is not supported"},
	} {
		t.Run(tc.options, func(t *testing.T) {
			err := createFlagsFromOptions(tc.options).validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestNewContainerAppliesCreateFlags(t *testing.T) {
	input := &NewContainerInput{Platform: "linux/amd64", Options: "--platform linux/arm64 --pull never"}
	cr, ok := NewContainer(input).(*containerReference)
	require.True(t, ok)
	assert.Equal(t, "linux/arm64", input.Platform)
	assert.Equal(t, pullPolicyNever, cr.pullPolicy)

	kept := &NewContainerInput{Platform: "linux/amd64", Options: "--privileged"}
	NewContainer(kept)
	assert.Equal(t, "linux/amd64", kept.Platform)
}
