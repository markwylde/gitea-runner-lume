// Copyright 2026 The Gitea Authors. All rights reserved.
// Copyright 2020 The nektos/act Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package container

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/docker/cli/cli/config"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	log.SetLevel(log.DebugLevel)
}

func TestCleanImage(t *testing.T) {
	tables := []struct {
		imageIn  string
		imageOut string
	}{
		{"myhost.com/foo/bar", "myhost.com/foo/bar"},
		{"localhost:8000/canonical/ubuntu", "localhost:8000/canonical/ubuntu"},
		{"localhost/canonical/ubuntu:latest", "localhost/canonical/ubuntu:latest"},
		{"localhost:8000/canonical/ubuntu:latest", "localhost:8000/canonical/ubuntu:latest"},
		{"ubuntu", "docker.io/library/ubuntu"},
		{"ubuntu:18.04", "docker.io/library/ubuntu:18.04"},
		{"cibuilds/hugo:0.53", "docker.io/cibuilds/hugo:0.53"},
	}

	for _, table := range tables {
		imageOut := cleanImage(context.Background(), table.imageIn)
		assert.Equal(t, table.imageOut, imageOut)
	}
}

func TestGetImagePullOptions(t *testing.T) {
	ctx := context.Background()

	orig := config.Dir()
	t.Cleanup(func() { config.SetDir(orig) })

	config.SetDir("/non-existent/docker")

	options, err := getImagePullOptions(ctx, NewDockerPullExecutorInput{})
	assert.NoError(t, err, "Failed to create ImagePullOptions")                                                 //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, "", options.RegistryAuth, "RegistryAuth should be empty if no username or password is set") //nolint:testifylint // pre-existing issue from nektos/act

	options, err = getImagePullOptions(ctx, NewDockerPullExecutorInput{
		Image:    "",
		Username: "username",
		Password: "password",
	})
	assert.NoError(t, err, "Failed to create ImagePullOptions") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, "eyJ1c2VybmFtZSI6InVzZXJuYW1lIiwicGFzc3dvcmQiOiJwYXNzd29yZCJ9", options.RegistryAuth, "Username and Password should be provided")

	config.SetDir("testdata/docker-pull-options")

	options, err = getImagePullOptions(ctx, NewDockerPullExecutorInput{
		Image: "nektos/act",
	})
	assert.NoError(t, err, "Failed to create ImagePullOptions") //nolint:testifylint // pre-existing issue from nektos/act
	assert.Equal(t, "eyJ1c2VybmFtZSI6InVzZXJuYW1lIiwicGFzc3dvcmQiOiJwYXNzd29yZFxuIiwic2VydmVyYWRkcmVzcyI6Imh0dHBzOi8vaW5kZXguZG9ja2VyLmlvL3YxLyJ9", options.RegistryAuth, "RegistryAuth should be taken from local docker config")
}

// A digest-pinned image is immutable, so its local copy is always current.
func TestIsPinnedImage(t *testing.T) {
	assert.True(t, isPinnedImage("alpine@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b"))
	assert.False(t, isPinnedImage("alpine:latest"))
}

// The pull path reports a failure the daemon sent mid-stream, so it must carry the reason
// whichever of the two shapes the daemon used.
func TestLogDockerResponseError(t *testing.T) {
	logger, _ := test.NewNullLogger()
	streamErr := func(line string) error {
		return logDockerResponse(logger, io.NopCloser(strings.NewReader(line)), false)
	}
	require.EqualError(t, streamErr(`{"error":"toomanyrequests: rate limit exceeded"}`), "toomanyrequests: rate limit exceeded")
	require.EqualError(t, streamErr(`{"errorDetail":{"message":"unexpected EOF"}}`), "unexpected EOF")
	require.NoError(t, streamErr(`{"status":"Downloading"}`))
}
