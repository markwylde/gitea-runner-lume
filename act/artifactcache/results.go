// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package artifactcache

import (
	"crypto/tls"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// The results service is one origin serving every github.actions.results.api.v1 service, and
// Gitea implements only the artifact half of it. Forwarding that half from here makes this origin
// the whole service, so ACTIONS_RESULTS_URL can point at it truthfully, which is what the clients
// this runner cannot patch need, docker buildx among them.
//
// The instance to forward to travels with the job registration rather than with configuration, so
// a cache server shared between runners serves each of their instances.
const artifactServicePath = "/twirp/github.actions.results.api.v1.ArtifactService/"

// forwardOrNotFound is the router's fallback: the artifact service of the instance the job
// registered with, and the 404 the router would have written otherwise.
func (h *Handler) forwardOrNotFound(w http.ResponseWriter, r *http.Request) {
	cred, ok := h.lookupCredential(bearerToken(r))
	if !ok || cred.Results == "" || !strings.HasPrefix(r.URL.Path, artifactServicePath) {
		http.NotFound(w, r)
		return
	}
	target, err := url.Parse(strings.TrimSuffix(cred.Results, "/"))
	if err != nil {
		h.logger.Errorf("artifact service forward to %q: %v", cred.Results, err)
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	h.logger.Debugf("%s %s: forwarding to %s", r.Method, r.URL.Path, target)

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			// Gitea builds the URLs it hands back from this Host, and their scheme from the
			// connection unless a forwarded header overrides it, so artifact bodies go to Gitea
			// directly and never through here.
			r.Out.Host = target.Host
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			h.logger.Warnf("artifact service forward to %s: %v", target, err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	if cred.InsecureTLS {
		proxy.Transport = insecureTransport
	}
	proxy.ServeHTTP(w, r)
}

// insecureTransport is shared, because a transport per request would pool no connections.
var insecureTransport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // the runner reaches its instance on the operator's say-so
