// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"gitea.com/gitea/runner/act/runner"
	clientmocks "gitea.com/gitea/runner/internal/pkg/client/mocks"
	"gitea.com/gitea/runner/internal/pkg/config"
	"gitea.com/gitea/runner/internal/pkg/ver"

	"connectrpc.com/connect"
	runnerv1 "gitea.dev/actions-proto-go/runner/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestRunnerCapabilitiesAndDeclare(t *testing.T) {
	require.Equal(t, []string{CapabilityCancelling}, RunnerCapabilities())

	cli := clientmocks.NewClient(t)
	cli.On("Declare", mock.Anything, mock.MatchedBy(func(req *connect.Request[runnerv1.DeclareRequest]) bool {
		return req.Msg.Version == ver.Version() &&
			len(req.Msg.Labels) == 1 &&
			req.Msg.Labels[0] == "ubuntu" &&
			len(req.Msg.Capabilities) == 1 &&
			req.Msg.Capabilities[0] == CapabilityCancelling
	})).Return(connect.NewResponse(&runnerv1.DeclareResponse{}), nil)

	r := &Runner{client: cli}
	_, err := r.Declare(context.Background(), []string{"ubuntu"})
	require.NoError(t, err)
}

func TestRunnerSetCapabilitiesFromDeclare(t *testing.T) {
	r := &Runner{}
	r.SetCapabilitiesFromDeclare(nil)
	require.Empty(t, r.capabilities)

	resp := connect.NewResponse(&runnerv1.DeclareResponse{})
	resp.Header().Set("X-Gitea-Actions-Capabilities", " cancelling,cache-v2 ")
	r.SetCapabilitiesFromDeclare(resp)
	require.Equal(t, "cancelling,cache-v2", r.capabilities)
}

func TestRunnerDefaultActionsURLUsesMirrorOnlyForGithub(t *testing.T) {
	r := &Runner{cfg: &config.Config{}}
	r.cfg.Runner.GithubMirror = "https://mirror.example"

	task := taskWithDefaultActionsURL("https://github.com")
	require.Equal(t, "https://mirror.example", r.getDefaultActionsURL(task))

	task = taskWithDefaultActionsURL("https://gitea.example")
	require.Equal(t, "https://gitea.example", r.getDefaultActionsURL(task))
}

func TestRunnerRunningCountAndNullLogger(t *testing.T) {
	r := &Runner{}
	require.Equal(t, int64(0), r.RunningCount())
	r.runningCount.Add(2)
	require.Equal(t, int64(2), r.RunningCount())

	logger := NullLogger{}.WithJobLogger()
	require.NotNil(t, logger)
	require.NotNil(t, logger.Out)
}

func TestNewRunnerInitializesLabelsAndEnvironment(t *testing.T) {
	cacheEnabled := false
	cfg := &config.Config{}
	cfg.Cache.Enabled = &cacheEnabled
	cfg.Runner.Envs = map[string]string{"EXISTING": "value"}
	reg := &config.Registration{
		Name:   "runner",
		Labels: []string{"ubuntu:host", "", "pool:e57e18d4"},
	}
	cli := clientmocks.NewClient(t)
	cli.On("Address").Return("https://gitea.example/").Maybe()

	r := NewRunner(cfg, reg, cli)

	require.Equal(t, "runner", r.name)
	require.Len(t, r.labels, 2)
	require.Equal(t, []string{"ubuntu", "pool:e57e18d4"}, r.labels.Names())
	require.Equal(t, "value", r.envs["EXISTING"])
	require.Equal(t, "https://gitea.example/api/actions_pipeline/", r.envs["ACTIONS_RUNTIME_URL"])
	require.Equal(t, "https://gitea.example", r.envs["ACTIONS_RESULTS_URL"])
	require.Equal(t, "true", r.envs["GITEA_ACTIONS"])
	require.NotEmpty(t, r.envs["GITEA_ACTIONS_RUNNER_VERSION"])
	require.Nil(t, r.cacheHandler)
	require.Empty(t, r.envs[runner.CacheServiceV2Env], "no cache server, nothing to serve v2 from")
}

// Proxy variables are assembled per task, because a job's service containers have to be
// reached directly and they are only known once the workflow is parsed.
func TestNewRunnerLeavesProxyToTheTask(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("http_proxy", "http://proxy:3128")

	cfg := &config.Config{}
	cfg.Cache.ExternalServer = "http://cache.local:8088/"
	reg := &config.Registration{Name: "runner"}
	cli := clientmocks.NewClient(t)
	cli.On("Address").Return("https://gitea.example/").Maybe()

	r := NewRunner(cfg, reg, cli)

	require.NotContains(t, r.envs, "http_proxy")
	require.NotContains(t, r.envs, "no_proxy")
}

func taskWithDefaultActionsURL(url string) *runnerv1.Task {
	return &runnerv1.Task{
		Context: &structpb.Struct{
			Fields: map[string]*structpb.Value{
				"gitea_default_actions_url": structpb.NewStringValue(url),
			},
		},
	}
}

// The results service is decided per task, because that is where the job's token and its
// instance are both known. NewRunner only leaves the address Gitea serves.
func TestNewRunnerCacheServiceV2(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cache.Dir, cfg.Cache.Host = t.TempDir(), "127.0.0.1"
	cli := clientmocks.NewClient(t)
	cli.On("Address").Return("https://gitea.example/").Maybe()

	r := NewRunner(cfg, &config.Registration{Name: "runner"}, cli)
	t.Cleanup(func() { _ = r.Close() })
	const token = "task-token"

	assert.Equal(t, "https://gitea.example", r.envs["ACTIONS_RESULTS_URL"])
	assert.Empty(t, r.envs[runner.CacheServiceV2Env], "a promise the runner has not made yet")
	assert.True(t, r.patchToolkit())

	// The registration is what makes it true: the cache server takes the results service over,
	// having been told which instance to forward the artifact half to.
	revoke, resultsURL := r.registerCacheForTask(token, "owner/repo", nil)
	defer revoke()
	require.Equal(t, r.cacheHandler.ExternalURL(), resultsURL)

	// And what is advertised has to answer the cache service. A client without the GHES escape
	// hatch, docker buildx among them, posts its cache calls at exactly this URL and nowhere else,
	// so a 404 here is the failure this whole change is about.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		resultsURL+"/twirp/github.actions.results.api.v1.CacheService/GetCacheEntryDownloadURL",
		strings.NewReader(`{"key":"k","version":"v"}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "the advertised results service serves no cache service")
}

// The v1 cache client appends its path to ACTIONS_CACHE_URL without a separator, so a configured
// server that is missing the slash would send it to a URL whose port swallows the path.
func TestNewRunnerNormalizesTheExternalCacheServer(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cache.ExternalServer = "http://cache.local:8088//"
	cli := clientmocks.NewClient(t)
	cli.On("Address").Return("https://gitea.example/").Maybe()

	r := NewRunner(cfg, &config.Registration{Name: "runner"}, cli)

	assert.Equal(t, "http://cache.local:8088/", r.envs["ACTIONS_CACHE_URL"])
	// Nothing to front the results service with, so the variable stays unset, but the bundles are
	// still patched: artifacts v4 need that, and the patch keeps the cache client on the cache URL.
	assert.Equal(t, "https://gitea.example", r.envs["ACTIONS_RESULTS_URL"])
	assert.Empty(t, r.envs[runner.CacheServiceV2Env])
	assert.True(t, r.patchToolkit())
}
