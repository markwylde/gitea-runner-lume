// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pingv1 "gitea.dev/actions-proto-go/ping/v1"
	"github.com/stretchr/testify/require"
)

func TestGetHTTPClientUsesProxyFromEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.example.com:8080")

	client := getHTTPClient("http://gitea.example.com", false)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)

	req, err := http.NewRequest(http.MethodGet, "http://gitea.example.com/api/actions/ping", nil)
	require.NoError(t, err)

	proxyURL, err := transport.Proxy(req)
	require.NoError(t, err)
	require.NotNil(t, proxyURL)
	require.Equal(t, "http://proxy.example.com:8080", proxyURL.String())
}

func TestGetHTTPClientInsecureTLS(t *testing.T) {
	// insecure only takes effect for https endpoints
	httpsInsecure := getHTTPClient("https://gitea.example.com", true)
	transport, ok := httpsInsecure.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	require.True(t, transport.TLSClientConfig.InsecureSkipVerify)

	for _, tc := range []struct {
		name     string
		endpoint string
		insecure bool
	}{
		{"https secure", "https://gitea.example.com", false},
		{"http insecure ignored", "http://gitea.example.com", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := getHTTPClient(tc.endpoint, tc.insecure)
			tr, ok := c.Transport.(*http.Transport)
			require.True(t, ok)
			require.Nil(t, tr.TLSClientConfig)
		})
	}
}

func TestNewSetsBaseURLAndHeaders(t *testing.T) {
	var gotPath string
	gotHeaders := make(http.Header)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// trailing slash must be trimmed before "/api/actions" is appended
	c := New(server.URL+"/", false, "the-uuid", "the-token")
	// Address returns the endpoint as supplied (untrimmed)
	require.Equal(t, server.URL+"/", c.Address())
	require.False(t, c.Insecure())

	// the call is expected to fail (server returns 500), we only assert what was sent
	_, _ = c.Ping(t.Context(), connect.NewRequest(&pingv1.PingRequest{Data: "hi"}))

	require.True(t, strings.HasPrefix(gotPath, "/api/actions/"), "unexpected path %q", gotPath)
	require.Equal(t, "the-uuid", gotHeaders.Get(UUIDHeader))
	require.Equal(t, "the-token", gotHeaders.Get(TokenHeader))
}

func TestNewOmitsEmptyHeaders(t *testing.T) {
	gotHeaders := make(http.Header)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c := New(server.URL, false, "", "")
	_, _ = c.Ping(t.Context(), connect.NewRequest(&pingv1.PingRequest{Data: "hi"}))

	require.Empty(t, gotHeaders.Get(UUIDHeader))
	require.Empty(t, gotHeaders.Get(TokenHeader))
}
