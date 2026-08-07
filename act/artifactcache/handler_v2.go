// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package artifactcache

import (
	"cmp"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/julienschmidt/httprouter"
)

// The cache service v2 API. A client on this version talks twirp to
// `github.actions.results.api.v1.CacheService` instead of the /_apis/artifactcache
// endpoints, and uploads the archive to the returned URL with the Azure blob protocol.
// Both API versions are served from the same store, so a repository keeps its cache
// when a workflow moves between action versions.
//
// Responses carry the proto field names, which is what Gitea's own results API emits and the only
// spelling the Go clients parse. The JavaScript toolkit accepts either.
const (
	cacheServiceV2Path = "/twirp/github.actions.results.api.v1.CacheService"

	// blobPath authenticates by signature, because the client uploads without an
	// Authorization header. Downloads are handed the v1 artifact URL instead.
	blobPath = apiPath + "/blobs"

	// blobUploadPurpose keeps an upload URL from being replayed to read an entry.
	blobUploadPurpose = "upload:"

	blobUploadURLTTL = time.Hour

	// twirpInternal is the only error code that is not the client's fault.
	twirpInternal = "internal"
)

func (h *Handler) registerV2Routes(router *httprouter.Router) {
	router.POST(cacheServiceV2Path+"/CreateCacheEntry", h.bearerAuth(h.v2CreateCacheEntry))
	router.POST(cacheServiceV2Path+"/FinalizeCacheEntryUpload", h.bearerAuth(h.v2FinalizeCacheEntryUpload))
	router.POST(cacheServiceV2Path+"/GetCacheEntryDownloadURL", h.bearerAuth(h.v2GetCacheEntryDownloadURL))
	router.PUT(blobPath+"/:id", h.signedAuth(blobUploadPurpose, h.v2UploadBlob))
}

// An entry that already exists is reported as not ok, which is how the client learns to skip
// the upload.
func (h *Handler) v2CreateCacheEntry(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	cred := credFromContext(r.Context())
	req, err := decodeTwirpRequest[v2CreateRequest](r)
	if err != nil {
		h.twirpError(w, r, "malformed_request", err)
		return
	}
	if req.Key == "" || req.Version == "" {
		h.twirpError(w, r, "invalid_argument", errors.New("key and version are required"))
		return
	}

	db, err := h.openDB()
	if err != nil {
		h.twirpError(w, r, twirpInternal, err)
		return
	}
	defer db.Close()

	// An exact (key, version) match means the entry is already cached; the client then skips
	// the upload. A prefix match must not count here, or a shorter key would be reported as
	// existing and silently never saved.
	if existing, err := findExactCache(db, cred.Repo, req.Key, req.Version, true); err != nil {
		h.twirpError(w, r, twirpInternal, err)
		return
	} else if existing != nil {
		h.twirpNotOK(w, r)
		return
	}

	now := time.Now().Unix()
	cache := &Cache{
		Repo:      cred.Repo,
		Key:       req.Key,
		Version:   req.Version,
		Size:      -1, // the size is only known at finalize time
		CreatedAt: now,
		UsedAt:    now,
	}
	if err := insertCache(db, cache); err != nil {
		h.twirpError(w, r, twirpInternal, err)
		return
	}

	h.responseJSON(w, r, http.StatusOK, map[string]any{
		"ok":                true,
		"signed_upload_url": h.signedURL(blobPath, blobUploadPurpose, cache.ID, time.Now().Add(blobUploadURLTTL)),
	})
}

func (h *Handler) v2FinalizeCacheEntryUpload(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	cred := credFromContext(r.Context())
	req, err := decodeTwirpRequest[v2FinalizeRequest](r)
	if err != nil {
		h.twirpError(w, r, "malformed_request", err)
		return
	}

	db, err := h.openDB()
	if err != nil {
		h.twirpError(w, r, twirpInternal, err)
		return
	}
	defer db.Close()

	cache, err := findExactCache(db, cred.Repo, req.Key, req.Version, false)
	if err != nil {
		h.twirpError(w, r, twirpInternal, err)
		return
	}
	if cache == nil {
		h.twirpNotOK(w, r)
		return
	}
	db.Close() // commitCache needs the store closed

	cache.Size, _ = cmp.Or(req.SizeBytes, req.SizeBytesCamel).Int64()
	if err := h.commitCache(cache); err != nil {
		h.logger.Errorf("finalize cache %d (%s): %v", cache.ID, cache.Key, err)
		h.twirpNotOK(w, r)
		return
	}

	h.responseJSON(w, r, http.StatusOK, map[string]any{
		"ok": true,
		// int64 fields travel as strings in the proto JSON mapping.
		"entry_id": strconv.FormatUint(cache.ID, 10),
	})
}

func (h *Handler) v2GetCacheEntryDownloadURL(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	cred := credFromContext(r.Context())
	req, err := decodeTwirpRequest[v2DownloadRequest](r)
	if err != nil {
		h.twirpError(w, r, "malformed_request", err)
		return
	}

	db, err := h.openDB()
	if err != nil {
		h.twirpError(w, r, twirpInternal, err)
		return
	}
	defer db.Close()

	cache, err := h.lookupCache(db, cred.Repo, req.keys(), req.Version)
	if err != nil {
		h.twirpError(w, r, twirpInternal, err)
		return
	}
	if cache == nil {
		h.twirpNotOK(w, r)
		return
	}

	h.responseJSON(w, r, http.StatusOK, map[string]any{
		"ok":                  true,
		"signed_download_url": h.signedArtifactURL(cache.ID, time.Now().Add(artifactURLTTL)),
		"matched_key":         cache.Key,
	})
}

// The archive arrives over the subset of the Azure blob API the toolkit uses: a small
// cache is a single PUT, a large one is staged as blocks that a final block list puts
// in order.
func (h *Handler) v2UploadBlob(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
	id, err := strconv.ParseUint(params.ByName("id"), 10, 64)
	if err != nil {
		h.responseJSON(w, r, http.StatusBadRequest, err)
		return
	}

	if err := h.touchCache(id, true); err != nil {
		h.responseJSON(w, r, http.StatusBadRequest, err)
		return
	}

	query := r.URL.Query()
	switch strings.ToLower(query.Get("comp")) {
	case "block":
		blockID := query.Get("blockid")
		if blockID == "" {
			h.responseJSON(w, r, http.StatusBadRequest, errors.New("missing blockid"))
			return
		}
		err = h.storage.WriteBlock(id, blockID, r.Body)
	case "blocklist":
		var list struct{ Latest []string }
		if err := xml.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&list); err != nil {
			h.responseJSON(w, r, http.StatusBadRequest, fmt.Errorf("malformed block list: %w", err))
			return
		}
		err = h.storage.OrderBlocks(id, list.Latest)
	default:
		err = h.storage.Write(id, 0, r.Body)
	}
	if err != nil {
		h.responseJSON(w, r, http.StatusInternalServerError, err)
		return
	}

	// The Azure SDK client dereferences this without checking, so its absence panics the caller.
	w.Header().Set("x-ms-request-id", strconv.FormatInt(time.Now().UnixNano(), 10))
	w.WriteHeader(http.StatusCreated)
}

// twirpNotOK is the negative answer all three endpoints share: no such entry to restore, no
// reservation to finalize, or an entry that already exists and need not be uploaded again.
func (h *Handler) twirpNotOK(w http.ResponseWriter, r *http.Request) {
	h.responseJSON(w, r, http.StatusOK, map[string]any{"ok": false})
}

// twirpError reports in the shape a twirp client expects, so the toolkit surfaces the message
// instead of a parse error.
func (h *Handler) twirpError(w http.ResponseWriter, r *http.Request, code string, err error) {
	h.logger.Debugf("%s %s: %v", r.Method, r.URL.Path, err)
	status := http.StatusBadRequest
	if code == twirpInternal {
		status = http.StatusInternalServerError
	}
	h.responseJSON(w, r, status, map[string]any{"code": code, "msg": err.Error()})
}

// The twirp request bodies. The toolkit's client serialises with useProtoFieldName, so the proto
// names are what arrive; the camelCase spellings of the same mapping are accepted too, as are
// int64s sent as a bare number rather than the string the mapping prescribes.
type (
	v2CreateRequest struct {
		Key     string `json:"key"`
		Version string `json:"version"`
	}

	v2FinalizeRequest struct {
		Key            string      `json:"key"`
		Version        string      `json:"version"`
		SizeBytes      json.Number `json:"size_bytes"`
		SizeBytesCamel json.Number `json:"sizeBytes"`
	}

	v2DownloadRequest struct {
		Key              string   `json:"key"`
		Version          string   `json:"version"`
		RestoreKeys      []string `json:"restore_keys"`
		RestoreKeysCamel []string `json:"restoreKeys"`
	}
)

func (d v2DownloadRequest) keys() []string {
	restoreKeys := d.RestoreKeys
	if len(restoreKeys) == 0 {
		restoreKeys = d.RestoreKeysCamel
	}
	return append([]string{d.Key}, restoreKeys...)
}

func decodeTwirpRequest[T any](r *http.Request) (T, error) {
	var req T
	err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
	return req, err
}
