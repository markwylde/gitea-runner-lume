// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
)

// StartServer starts an HTTP server that serves Prometheus metrics on /metrics
// and health checks. The server shuts down when ctx is cancelled.
// Call Init() before StartServer to register metrics with the Registry.
func StartServer(ctx context.Context, addr string, readiness func() (bool, string)) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           NewHTTPHandler(readiness),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Infof("metrics server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.WithError(err).Error("metrics server failed")
		}
	}()

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.WithError(err).Warn("metrics server shutdown error")
		}
	}()
}

// NewHTTPHandler returns the metrics and health endpoints used by StartServer.
func NewHTTPHandler(readiness func() (bool, string)) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		ready, reason := true, "ok"
		if readiness != nil {
			ready, reason = readiness()
		}
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = w.Write([]byte(reason))
	})

	return mux
}
