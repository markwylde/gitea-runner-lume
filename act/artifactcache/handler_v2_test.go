// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package artifactcache

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// v2Call posts a twirp request to the cache service and returns the decoded response.
// Field names are the proto ones, which is what the toolkit's client sends.
func v2Call(t *testing.T, handler *Handler, client *http.Client, method string, request any) map[string]any {
	t.Helper()

	body, err := json.Marshal(request)
	require.NoError(t, err)

	resp, err := client.Post(handler.ExternalURL()+cacheServiceV2Path+"/"+method, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	got := map[string]any{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	return got
}

// putBlob uploads to a signed URL and returns the status, so a test can assert a refusal.
func putBlob(t *testing.T, url string, content []byte) int {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, url, bytes.NewReader(content))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusCreated {
		// The Azure SDK client dereferences this header without checking, so a blob upload that
		// omits it panics the caller rather than failing it.
		require.NotEmpty(t, resp.Header.Get("x-ms-request-id"))
	}
	return resp.StatusCode
}

func getURL(t *testing.T, url string) []byte {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return body
}

func startTestHandler(t *testing.T) *Handler {
	t.Helper()

	handler, err := StartHandler(filepath.Join(t.TempDir(), "artifactcache"), "127.0.0.1", 0, "", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = handler.Close() })
	handler.RegisterJob(testToken, JobCredential{Repo: testRepo})
	return handler
}

// saveV2 runs the reserve/upload/finalize sequence and returns the finalize response along
// with the upload URL it used.
func saveV2(t *testing.T, handler *Handler, key, version string, content []byte) (finalized map[string]any, uploadURL string) {
	t.Helper()

	created := v2Call(t, handler, testClient, "CreateCacheEntry", map[string]any{"key": key, "version": version})
	require.Equal(t, true, created["ok"])
	uploadURL, _ = created["signed_upload_url"].(string)
	require.NotEmpty(t, uploadURL)
	require.Equal(t, http.StatusCreated, putBlob(t, uploadURL, content))

	return v2Call(t, handler, testClient, "FinalizeCacheEntryUpload", map[string]any{
		"key": key, "version": version,
		"size_bytes": strconv.Itoa(len(content)),
	}), uploadURL
}

// The whole round trip an actions/cache v2 client makes, plus the guarantees on the signed
// URLs it is handed: unsigned requests are refused, an upload URL cannot be replayed to read
// or to replace a finalized entry.
func TestCacheServiceV2RoundTrip(t *testing.T) {
	handler := startTestHandler(t)
	content := []byte("the cached archive")

	unsigned := fmt.Sprintf("%s%s/1", handler.ExternalURL(), blobPath)
	assert.Equal(t, http.StatusUnauthorized, putBlob(t, unsigned, content))

	finalized, uploadURL := saveV2(t, handler, "deps-v1", "abc123", content)
	require.Equal(t, true, finalized["ok"])
	assert.NotEmpty(t, finalized["entry_id"])

	// The upload URL outlives the finalize call, so replaying it must not poison the entry,
	// and it is an upload URL only: nothing reads a blob back through it.
	assert.Equal(t, http.StatusBadRequest, putBlob(t, uploadURL, []byte("poisoned")))
	resp, err := http.Get(uploadURL) //nolint:noctx // the URL is the server under test
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)

	got := v2Call(t, handler, testClient, "GetCacheEntryDownloadURL", map[string]any{"key": "deps-v1", "version": "abc123"})
	require.Equal(t, true, got["ok"])
	assert.Equal(t, "deps-v1", got["matched_key"])
	downloadURL, _ := got["signed_download_url"].(string)
	require.NotEmpty(t, downloadURL)
	assert.Equal(t, content, getURL(t, downloadURL))
}

// A large archive is staged as blocks and only put in order by the final block list, so
// blocks that arrive out of order must still be assembled the way the client asked.
func TestCacheServiceV2BlockUpload(t *testing.T) {
	handler := startTestHandler(t)

	created := v2Call(t, handler, testClient, "CreateCacheEntry", map[string]any{"key": "blocks", "version": "v1"})
	uploadURL, _ := created["signed_upload_url"].(string)
	require.NotEmpty(t, uploadURL)

	blocks := map[string][]byte{}
	var order []string
	for i, part := range []string{"hello ", "world", "!"} {
		blockID := base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "block-%d", i))
		blocks[blockID] = []byte(part)
		order = append(order, blockID)
	}
	// Upload in an order that is not the block list order.
	for _, blockID := range []string{order[2], order[0], order[1]} {
		require.Equal(t, http.StatusCreated, putBlob(t, uploadURL+"&comp=block&blockid="+blockID, blocks[blockID]))
	}

	var list bytes.Buffer
	list.WriteString(`<?xml version="1.0" encoding="utf-8"?><BlockList>`)
	for _, blockID := range order {
		fmt.Fprintf(&list, "<Latest>%s</Latest>", blockID)
	}
	list.WriteString(`</BlockList>`)
	require.Equal(t, http.StatusCreated, putBlob(t, uploadURL+"&comp=blocklist", list.Bytes()))

	finalized := v2Call(t, handler, testClient, "FinalizeCacheEntryUpload", map[string]any{
		"key": "blocks", "version": "v1", "size_bytes": len("hello world!"),
	})
	require.Equal(t, true, finalized["ok"])

	got := v2Call(t, handler, testClient, "GetCacheEntryDownloadURL", map[string]any{"key": "blocks", "version": "v1"})
	require.Equal(t, true, got["ok"])
	assert.Equal(t, "hello world!", string(getURL(t, got["signed_download_url"].(string))))
}

func TestCacheServiceV2Lookups(t *testing.T) {
	handler := startTestHandler(t)
	saved, _ := saveV2(t, handler, "deps-abc", "v1", []byte("x"))
	require.Equal(t, true, saved["ok"])

	t.Run("reports a miss for an unknown key", func(t *testing.T) {
		got := v2Call(t, handler, testClient, "GetCacheEntryDownloadURL", map[string]any{"key": "nothing", "version": "v1"})
		assert.Equal(t, false, got["ok"])
	})

	// The toolkit serialises with the proto field names; the camelCase spellings of the same
	// proto JSON mapping are accepted alongside them.
	for _, field := range []string{"restore_keys", "restoreKeys"} {
		t.Run("restore keys match by prefix, spelled "+field, func(t *testing.T) {
			got := v2Call(t, handler, testClient, "GetCacheEntryDownloadURL", map[string]any{
				"key": "deps-zzz", field: []string{"deps-"}, "version": "v1",
			})
			require.Equal(t, true, got["ok"])
			assert.Equal(t, "deps-abc", got["matched_key"])
		})
	}

	t.Run("an existing entry is not reserved twice", func(t *testing.T) {
		again := v2Call(t, handler, testClient, "CreateCacheEntry", map[string]any{"key": "deps-abc", "version": "v1"})
		assert.Equal(t, false, again["ok"])
	})

	// A key that is only a prefix of an existing one is a different entry, so the
	// reservation check must be exact and not a restore-key prefix match, or the shorter
	// key would be reported as existing and silently never saved.
	t.Run("a prefix of an existing key is still reserved", func(t *testing.T) {
		reserved := v2Call(t, handler, testClient, "CreateCacheEntry", map[string]any{"key": "deps", "version": "v1"})
		require.Equal(t, true, reserved["ok"])
		assert.NotEmpty(t, reserved["signed_upload_url"])
	})

	t.Run("finalizing without a reservation is not ok", func(t *testing.T) {
		got := v2Call(t, handler, testClient, "FinalizeCacheEntryUpload", map[string]any{
			"key": "never-reserved", "version": "v1", "size_bytes": 1,
		})
		assert.Equal(t, false, got["ok"])
	})

	// The size the client declares is what Commit validates the assembled archive against.
	t.Run("finalizing with the wrong size is not ok", func(t *testing.T) {
		created := v2Call(t, handler, testClient, "CreateCacheEntry", map[string]any{"key": "wrong-size", "version": "v1"})
		require.Equal(t, http.StatusCreated, putBlob(t, created["signed_upload_url"].(string), []byte("four")))

		got := v2Call(t, handler, testClient, "FinalizeCacheEntryUpload", map[string]any{
			"key": "wrong-size", "version": "v1", "size_bytes": 99,
		})
		assert.Equal(t, false, got["ok"])
	})

	// Both API versions are served from one store, so an entry written through v2 is a hit for
	// a v1 client asking for the same key and version.
	t.Run("a v1 client sees an entry written through v2", func(t *testing.T) {
		resp, err := testClient.Get(fmt.Sprintf("%s%s/cache?keys=deps-abc&version=v1", handler.ExternalURL(), apiPath))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		got := map[string]any{}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		assert.Equal(t, "deps-abc", got["cacheKey"])
		assert.NotEmpty(t, got["archiveLocation"])
	})

	// The cache of one repository must stay invisible to another, as it does for the v1 API.
	t.Run("another repository sees nothing", func(t *testing.T) {
		handler.RegisterJob("other-runtime-token", JobCredential{Repo: "other/repo"})
		otherClient := &http.Client{Transport: &bearerTransport{token: "other-runtime-token"}}

		got := v2Call(t, handler, otherClient, "GetCacheEntryDownloadURL", map[string]any{"key": "deps-abc", "version": "v1"})
		assert.Equal(t, false, got["ok"])
	})
}
