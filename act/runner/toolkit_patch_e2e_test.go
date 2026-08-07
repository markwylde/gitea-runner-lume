// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitea.com/gitea/runner/act/artifactcache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// actionsCacheRef pins the actions/cache release this is verified against. Bump it
// deliberately: a new release is exactly what can stop the patch matching.
const actionsCacheRef = "v6.1.0"

// bundleFromGitHub downloads one entrypoint, keeping it in the user cache dir so repeated runs
// cost nothing. The bundles are megabytes, too large to vendor.
func bundleFromGitHub(t *testing.T, repo, ref, path string) string {
	t.Helper()

	cacheDir, err := os.UserCacheDir()
	require.NoError(t, err)
	dir := filepath.Join(cacheDir, "gitea-runner-test", strings.ReplaceAll(repo, "/", "-")+"-"+ref)
	bundle := filepath.Join(dir, strings.ReplaceAll(path, "/", "-"))
	if _, err := os.Stat(bundle); err == nil {
		return bundle
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))

	url := "https://raw.githubusercontent.com/" + repo + "/" + ref + "/" + path
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("cannot reach %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("GET %s: %s", url, resp.Status)
	}
	file, err := os.Create(bundle)
	require.NoError(t, err)
	_, err = io.Copy(file, resp.Body)
	require.NoError(t, file.Close())
	require.NoError(t, err)
	return bundle
}

// jobEnv is the environment a job gets from this runner, which is what decides where an action's
// toolkit looks for the cache and artifact services.
type jobEnv struct {
	workspace, runnerTemp string
	cacheURL, resultsURL  string
	token                 string
}

// runActionEntrypoint runs one action entrypoint the way a job would. Adding another action to these tests
// means downloading its entrypoint with bundleFromGitHub and calling this with its inputs, whose
// names are the ones the action's own action.yml uses.
func runActionEntrypoint(t *testing.T, script string, env jobEnv, inputs map[string]string) string {
	t.Helper()
	state := filepath.Join(env.runnerTemp, "state")
	output := filepath.Join(env.runnerTemp, "output")
	for _, name := range []string{state, output} {
		require.NoError(t, os.WriteFile(name, nil, 0o600))
	}

	cmd := exec.CommandContext(t.Context(), "node", script)
	cmd.Dir = env.workspace
	cmd.Env = append(os.Environ(),
		"ACTIONS_RUNTIME_TOKEN="+env.token,
		"ACTIONS_CACHE_URL="+env.cacheURL+"/",
		"ACTIONS_RESULTS_URL="+env.resultsURL,
		"ACTIONS_CACHE_SERVICE_V2=true",
		"GITHUB_SERVER_URL=https://gitea.example.com",
		"GITHUB_REPOSITORY=testuser/testrepo",
		"GITHUB_RUN_ID=1",
		"GITHUB_REF=refs/heads/main",
		"GITHUB_EVENT_NAME=push",
		"GITHUB_WORKSPACE="+env.workspace,
		"RUNNER_TEMP="+env.runnerTemp,
		"GITHUB_STATE="+state,
		"GITHUB_OUTPUT="+output,
	)
	for name, value := range inputs {
		cmd.Env = append(cmd.Env, "INPUT_"+strings.ToUpper(name)+"="+value)
	}
	out, err := cmd.CombinedOutput()
	t.Logf("%s:\n%s", filepath.Base(script), out)
	require.NoError(t, err, "%s failed", script)
	return string(out)
}

// patchedAction downloads one entrypoint and patches it exactly as a downloaded action would be,
// keeping the untouched original in the sidecar beside it.
func patchedAction(t *testing.T, repo, ref, entrypoint string) string {
	t.Helper()

	body, err := os.ReadFile(bundleFromGitHub(t, repo, ref, entrypoint))
	require.NoError(t, err)
	dir := tempDirPath(t)
	script := filepath.Join(dir, filepath.Base(entrypoint))
	require.NoError(t, os.WriteFile(script, body, 0o600))
	patchToolkit(t.Context(), dir, []string{script})
	return script
}

// tempDirPath is TempDir with symlinks resolved, because macOS hands out /var paths that resolve
// to /private/var and the client derives archive paths relative to the workspace.
func tempDirPath(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return dir
}

// The whole chain against the pinned release, whose bundles ship unminified: patch them, run the
// real client with an ordinary Gitea server URL and a results URL that goes nowhere, and have it
// save and restore through this runner's cache server. The unreachable results URL is the point,
// it is what proves the cache reaches the runner without the runner fronting Gitea. If a release
// stops matching the patch the client falls back to v1 and this fails on the version line, which
// is the signal to look at the new bundle.
func TestCacheServiceV2EndToEnd(t *testing.T) {
	requireHostTools(t, "node")

	restore := patchedAction(t, "actions/cache", actionsCacheRef, "dist/restore/index.js")
	save := patchedAction(t, "actions/cache", actionsCacheRef, "dist/save/index.js")

	handler, err := artifactcache.StartHandler(filepath.Join(t.TempDir(), "cache"), "127.0.0.1", 0, "", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = handler.Close() })
	const token, repo = "e2e-runtime-token", "testuser/testrepo"
	handler.RegisterJob(token, artifactcache.JobCredential{Repo: repo})

	env := jobEnv{
		workspace:  tempDirPath(t),
		runnerTemp: tempDirPath(t),
		cacheURL:   handler.ExternalURL(),
		// The results service is the cache server's too, which is what the runner advertises.
		resultsURL: handler.ExternalURL(),
		token:      token,
	}
	require.NoError(t, os.MkdirAll(filepath.Join(env.workspace, "to-cache"), 0o755))
	content := []byte("cached through the patched gate")
	require.NoError(t, os.WriteFile(filepath.Join(env.workspace, "to-cache", "data.txt"), content, 0o600))

	const key = "patched-gate-key"
	inputs := map[string]string{"path": "to-cache", "key": key}

	missed := runActionEntrypoint(t, restore, env, inputs)
	require.Contains(t, missed, "Cache service version: v2", "the patch did not take, the client stayed on v1")
	require.Contains(t, missed, "Cache not found for input keys: "+key)

	saved := runActionEntrypoint(t, save, env, inputs)
	require.Contains(t, saved, "Cache saved with key: "+key)

	env.workspace = tempDirPath(t)
	hit := runActionEntrypoint(t, restore, env, inputs)
	require.Contains(t, hit, "Cache restored from key: "+key)

	got, err := os.ReadFile(filepath.Join(env.workspace, "to-cache", "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, content, got)

	// Untouched, the same client takes a Gitea host for GHES and stays on v1, which reaches the
	// cache server on its own address. That is what a runner without a results service of its own
	// leaves its jobs with, so it has to round trip too.
	env.workspace = tempDirPath(t)
	v1 := runActionEntrypoint(t, filepath.Join(sidecarDir(filepath.Dir(restore)), "index.js"), env, inputs)
	require.Contains(t, v1, "Cache service version: v1")
	require.Contains(t, v1, "Cache restored from key: "+key)
}

// The gate and the URL getter are separate functions, and a bundler may put either first: the gap
// between them runs from 159 to 1179 bytes across these actions, which is why neither edit is
// anchored on that distance. One entrypoint from each of the families that bundle the cache
// toolkit, patched but not run, is what keeps a future release from quietly matching only one of
// the two shapes and leaving every cache on v1.
func TestToolkitPatchAcrossActions(t *testing.T) {
	for _, tc := range []struct {
		repo, ref, path string
		wantPatched     bool
	}{
		// The cache toolkit, in each bundler shape and from a spread of ecosystems, including the
		// actions that drive a Go or a Rust cache client of their own.
		{"actions/cache", actionsCacheRef, "dist/restore/index.js", true},
		{"actions/setup-node", "v7.0.0", "dist/cache-save/index.js", true},
		{"actions/setup-python", "v7.0.0", "dist/setup/index.js", true},
		{"actions/setup-go", "v7.0.0", "dist/setup/index.js", true},
		{"actions/setup-java", "v5.7.0", "dist/setup/index.js", true},
		{"ruby/setup-ruby", "v1.321.0", "dist/index.js", true},
		{"pnpm/action-setup", "v6.0.9", "dist/index.js", true},
		{"oven-sh/setup-bun", "v2.2.0", "dist/setup/index.js", true},
		{"Swatinem/rust-cache", "v2.9.1", "dist/restore/index.js", true},
		{"docker/build-push-action", "v7.3.0", "dist/index.cjs", true},
		// The artifact toolkit, where the gate is a refusal and there is nothing to redirect.
		// v4.4.0 is the first release whose gate carries the localhost test this matches; the
		// releases before it refuse in a shape the runner leaves alone.
		{"actions/upload-artifact", "v4.4.0", "dist/upload/index.js", true},
		{"actions/upload-artifact", "v7.0.1", "dist/upload/index.js", true},
		{"actions/download-artifact", "v8.0.1", "dist/index.js", true},
		// Neither toolkit's gate, so these have to come back byte for byte. sccache-action is the
		// one that exports ACTIONS_CACHE_SERVICE_V2 itself, for the Rust client it installs.
		{"actions/checkout", "v7.0.1", "dist/index.js", false},
		{"mozilla-actions/sccache-action", "v0.0.11", "dist/setup/index.js", false},
	} {
		t.Run(tc.repo+"@"+tc.ref, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(bundleFromGitHub(t, tc.repo, tc.ref, tc.path))
			require.NoError(t, err)

			out, patched := patchedBundle(data)
			require.Equal(t, tc.wantPatched, patched)
			if !tc.wantPatched {
				assert.Equal(t, data, out, "an untouched bundle must come back byte for byte")
				return
			}
			assert.NotContains(t, string(out), ".LOCALHOST", "a copy of the gate was missed")

			if !strings.Contains(string(data), CacheServiceV2Env) {
				return // the artifact toolkit: a refusal to open, and no URL to move
			}
			// Only the reads inside getCacheServiceURL are rewritten. The others, such as the
			// feature-availability check, must be left as they are.
			assert.NotZero(t, strings.Count(string(out), "(process.env."+cacheURLEnv+"||process.env"),
				"the cache service URL was not redirected")
			assert.Equal(t, strings.Count(string(data), resultsURLEnv), strings.Count(string(out), resultsURLEnv),
				"a read of the results URL was lost, it must stay as the fallback")
		})
	}
}

// The stock artifact actions refuse on a Gitea host until the gate is opened, and then they talk
// to the results service, which is this runner's cache server forwarding the artifact half on to
// Gitea. Running the real upload-artifact against a stand-in Gitea covers both halves at once:
// the patch, and the forwarding the job's registration set up.
func TestUploadArtifactThroughTheResultsService(t *testing.T) {
	requireHostTools(t, "node")

	var called []string
	var zipped []byte
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := path.Base(r.URL.Path)
		called = append(called, method)
		w.Header().Set("x-ms-request-id", "stub")
		switch method {
		case "CreateArtifact":
			_, _ = io.WriteString(w, `{"ok":true,"signed_upload_url":"http://`+r.Host+
				`/twirp/github.actions.results.api.v1.ArtifactService/UploadArtifact?sig=x"}`)
		case "FinalizeArtifact":
			_, _ = io.WriteString(w, `{"ok":true,"artifact_id":"1"}`)
		case "ListArtifacts":
			_, _ = io.WriteString(w, `{"artifacts":[{"workflow_run_backend_id":"11",`+
				`"workflow_job_run_backend_id":"22","database_id":"1","name":"an-artifact","size":"`+
				strconv.Itoa(len(zipped))+`"}]}`)
		case "GetSignedArtifactURL":
			_, _ = io.WriteString(w, `{"signed_url":"http://`+r.Host+`/download"}`)
		case "download":
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipped)
		default: // the zip on its way up, in the blocks the Azure protocol puts it in
			body, _ := io.ReadAll(r.Body)
			switch r.URL.Query().Get("comp") {
			case "block":
				zipped = append(zipped, body...)
			case "blocklist": // the ordering document, not content
			default:
				zipped = body
			}
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer gitea.Close()

	handler, err := artifactcache.StartHandler(filepath.Join(t.TempDir(), "cache"), "127.0.0.1", 0, "", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = handler.Close() })
	// The artifact client decodes the runtime token for the run ids it puts in its requests, where
	// the cache client only presents it, so this one has to be shaped like Gitea's.
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"scp":"Actions.Results:11:22"}`)) + ".sig"
	defer handler.RegisterJob(token, artifactcache.JobCredential{Repo: "testuser/testrepo", Results: gitea.URL})()

	upload := patchedAction(t, "actions/upload-artifact", "v7.0.1", "dist/upload/index.js")

	env := jobEnv{
		workspace:  tempDirPath(t),
		runnerTemp: tempDirPath(t),
		cacheURL:   handler.ExternalURL(),
		resultsURL: handler.ExternalURL(),
		token:      token,
	}
	uploaded := []byte("through the results service")
	require.NoError(t, os.WriteFile(filepath.Join(env.workspace, "artifact.txt"), uploaded, 0o600))

	out := runActionEntrypoint(t, upload, env, map[string]string{
		"name": "an-artifact", "path": "artifact.txt", "if-no-files-found": "error",
		"retention-days": "0", "compression-level": "6", "overwrite": "false",
		"include-hidden-files": "false", "archive": "true",
	})

	require.Contains(t, out, "has been successfully uploaded")

	// And back down again: listing and downloading go the same way, and the signed URL the
	// artifact service hands out is fetched straight from it.
	download := patchedAction(t, "actions/download-artifact", "v8.0.1", "dist/index.js")
	env.workspace = tempDirPath(t)
	out = runActionEntrypoint(t, download, env, map[string]string{
		"name": "an-artifact", "path": "downloaded", "merge-multiple": "false",
		"skip-decompress": "false", "include-hidden-files": "false", "github-token": "",
	})

	require.Contains(t, out, "Artifact download completed")
	assert.Subset(t, called,
		[]string{"CreateArtifact", "UploadArtifact", "FinalizeArtifact", "ListArtifacts", "GetSignedArtifactURL"},
		"the artifact service was not reached through the cache server")
	got, err := os.ReadFile(filepath.Join(env.workspace, "downloaded", "artifact.txt"))
	require.NoError(t, err)
	assert.Equal(t, uploaded, got)
}

// The setup actions carry the same toolkit and reach the same service, from a key of their own
// making. setup-node is the cheapest of them to run: given a lockfile and no version to install,
// it does the cache lookup and nothing else.
func TestSetupActionFindsTheCacheService(t *testing.T) {
	requireHostTools(t, "node", "npm")

	setup := patchedAction(t, "actions/setup-node", "v7.0.0", "dist/setup/index.js")

	handler, err := artifactcache.StartHandler(filepath.Join(t.TempDir(), "cache"), "127.0.0.1", 0, "", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = handler.Close() })
	const token = "setup-runtime-token"
	defer handler.RegisterJob(token, artifactcache.JobCredential{Repo: "testuser/testrepo"})()

	env := jobEnv{
		workspace:  tempDirPath(t),
		runnerTemp: tempDirPath(t),
		cacheURL:   handler.ExternalURL(),
		resultsURL: handler.ExternalURL(),
		token:      token,
	}
	require.NoError(t, os.WriteFile(filepath.Join(env.workspace, "package-lock.json"),
		[]byte(`{"lockfileVersion":3}`), 0o600))

	out := runActionEntrypoint(t, setup, env, map[string]string{"cache": "npm"})

	require.Contains(t, out, "Cache service version: v2")
	require.Contains(t, out, "npm cache is not found", "the lookup did not reach the cache server")
}
