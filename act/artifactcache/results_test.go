// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package artifactcache

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The artifact half is forwarded under the Host Gitea knows itself by, so the URLs it hands back
// still point at Gitea, and nothing else is proxied.
func TestFrontResultsService(t *testing.T) {
	var gotHost, gotPath, gotProto string
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost, gotPath, gotProto = r.Host, r.URL.Path, r.Header.Get("X-Forwarded-Proto")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer gitea.Close()

	handler, err := StartHandler(t.TempDir(), "127.0.0.1", 0, "", nil)
	require.NoError(t, err)
	defer handler.Close()
	const token = "forward-token"

	client := &http.Client{Transport: &bearerTransport{token: token}}
	post := func(path string) int {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, handler.ExternalURL()+path, nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}

	assert.Equal(t, http.StatusNotFound, post(artifactServicePath+"CreateArtifact"),
		"an unregistered token is forwarded nowhere")

	defer handler.RegisterJob(token, JobCredential{Repo: "owner/repo", Results: gitea.URL})()

	assert.Equal(t, http.StatusOK, post(artifactServicePath+"CreateArtifact"))
	assert.Equal(t, strings.TrimPrefix(gitea.URL, "http://"), gotHost, "Gitea must see the host it mints its URLs from")
	assert.Empty(t, gotProto, "a forwarded scheme would make an https Gitea mint http URLs")
	assert.Equal(t, artifactServicePath+"CreateArtifact", gotPath)

	gotPath = ""
	assert.Equal(t, http.StatusNotFound, post("/twirp/github.actions.results.api.v1.OtherService/Do"))
	assert.Equal(t, http.StatusNotFound, post("/api/v1/repos/owner/repo"))
	assert.Empty(t, gotPath, "only the artifact service is forwarded")
}
