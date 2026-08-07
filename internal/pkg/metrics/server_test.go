// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReadyEndpoint(t *testing.T) {
	tests := []struct {
		name   string
		ready  bool
		reason string
		status int
	}{
		{"ready", true, "ok", http.StatusOK},
		{"not ready", false, "low disk space", http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHTTPHandler(func() (bool, string) { return test.ready, test.reason })
			request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assert.Equal(t, test.status, response.Code)
			assert.Equal(t, test.reason, response.Body.String())
		})
	}
}

func TestHealthEndpointStaysLiveWhenNotReady(t *testing.T) {
	handler := NewHTTPHandler(func() (bool, string) { return false, "low disk space" })
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, "ok", response.Body.String())
}
