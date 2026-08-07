// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// clearProxyEnv starts a test from a runner that has no proxy, whatever the developer's own
// environment looks like.
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"http_proxy", "HTTP_PROXY", "https_proxy", "HTTPS_PROXY", "no_proxy", "NO_PROXY"} {
		t.Setenv(name, "")
	}
}

func TestJobProxyEnv(t *testing.T) {
	const loopback = "localhost,127.0.0.1,::1"
	const proxy = "http://proxy:3128"

	for _, tc := range []struct {
		name     string
		runner   map[string]string // the runner process environment
		envs     map[string]string // what runner.envs already put in the job
		cacheURL string
		services []string
		want     map[string]string
	}{
		{
			// The guarantee that makes this safe to ship: a runner without a proxy gives its
			// jobs nothing at all.
			name:     "runner has no proxy",
			cacheURL: "http://cache.local:8088/",
		},
		{
			name:   "a lone no_proxy is not a proxy",
			runner: map[string]string{"no_proxy": "example.com"},
		},
		{
			name:   "both spellings of every variable",
			runner: map[string]string{"http_proxy": proxy, "https_proxy": proxy},
			want: map[string]string{
				"http_proxy": proxy, "HTTP_PROXY": proxy,
				"https_proxy": proxy, "HTTPS_PROXY": proxy,
				"no_proxy": loopback, "NO_PROXY": loopback,
			},
		},
		{
			name:   "a variable the runner does not have is omitted",
			runner: map[string]string{"https_proxy": proxy},
			want: map[string]string{
				"https_proxy": proxy, "HTTPS_PROXY": proxy,
				"no_proxy": loopback, "NO_PROXY": loopback,
			},
		},
		{
			// The cache server and the service containers are on the local network; Gitea is
			// not added and keeps being reached through the proxy.
			name:     "hosts the job must reach directly",
			runner:   map[string]string{"http_proxy": proxy, "no_proxy": "internal.example"},
			cacheURL: "http://192.168.1.10:34567/",
			services: []string{"postgres", "redis"},
			want: map[string]string{
				"http_proxy": proxy, "HTTP_PROXY": proxy,
				"no_proxy": "internal.example," + loopback + ",postgres,redis,192.168.1.10",
				"NO_PROXY": "internal.example," + loopback + ",postgres,redis,192.168.1.10",
			},
		},
		{
			name:   "runner.envs wins, for both spellings",
			runner: map[string]string{"http_proxy": "http://from-env:3128"},
			envs:   map[string]string{"http_proxy": "http://from-config:3128"},
			want: map[string]string{
				"http_proxy": "http://from-config:3128", "HTTP_PROXY": "http://from-config:3128",
				"no_proxy": loopback, "NO_PROXY": loopback,
			},
		},
		{
			name:   "runner.envs wins through the upper case spelling too",
			runner: map[string]string{"http_proxy": "http://from-env:3128"},
			envs:   map[string]string{"HTTP_PROXY": "http://from-config:3128"},
			want: map[string]string{
				"http_proxy": "http://from-config:3128", "HTTP_PROXY": "http://from-config:3128",
				"no_proxy": loopback, "NO_PROXY": loopback,
			},
		},
		{
			// A runner.envs no_proxy adds to the hosts that must stay direct rather than
			// replacing them, which would send cache traffic through the proxy.
			name:     "runner.envs no_proxy is merged, not substituted",
			runner:   map[string]string{"http_proxy": proxy},
			envs:     map[string]string{"no_proxy": "operator.example"},
			cacheURL: "http://192.168.1.10:34567/",
			want: map[string]string{
				"http_proxy": proxy, "HTTP_PROXY": proxy,
				"no_proxy": "operator.example," + loopback + ",192.168.1.10",
				"NO_PROXY": "operator.example," + loopback + ",192.168.1.10",
			},
		},
		{
			name:     "runner.envs NO_PROXY is merged through the upper case spelling too",
			runner:   map[string]string{"http_proxy": proxy},
			envs:     map[string]string{"NO_PROXY": "operator.example"},
			cacheURL: "http://192.168.1.10:34567/",
			want: map[string]string{
				"http_proxy": proxy, "HTTP_PROXY": proxy,
				"no_proxy": "operator.example," + loopback + ",192.168.1.10",
				"NO_PROXY": "operator.example," + loopback + ",192.168.1.10",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearProxyEnv(t)
			for name, value := range tc.runner {
				t.Setenv(name, value)
			}
			assert.Equal(t, tc.want, JobProxyEnv(tc.envs, tc.cacheURL, tc.services))
		})
	}
}

// docker-in-docker over tcp: the docker client would otherwise send API calls to a proxy
// that cannot route to the daemon.
func TestBypassProxyForDockerHost(t *testing.T) {
	for _, tc := range []struct {
		name       string
		httpProxy  string
		dockerHost string
		want       string
	}{
		{
			name:       "tcp daemon is added",
			httpProxy:  "http://proxy:3128",
			dockerHost: "tcp://docker:2375",
			want:       "internal.example,docker",
		},
		{
			name:       "unix socket is left alone",
			httpProxy:  "http://proxy:3128",
			dockerHost: "unix:///var/run/docker.sock",
			want:       "internal.example",
		},
		{
			name:       "nothing happens without a proxy",
			dockerHost: "tcp://docker:2375",
			want:       "internal.example",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearProxyEnv(t)
			t.Setenv("no_proxy", "internal.example")
			if tc.httpProxy != "" {
				t.Setenv("http_proxy", tc.httpProxy)
			}

			BypassProxyForDockerHost(tc.dockerHost)

			assert.Equal(t, tc.want, os.Getenv("no_proxy"))
		})
	}
}

func TestProxyPasswords(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("http_proxy", "http://user:hunter2@proxy:3128")
	t.Setenv("https_proxy", "http://user:s3cret@proxy:3128")
	assert.Equal(t, []string{"hunter2", "s3cret"}, proxyPasswords())

	t.Setenv("https_proxy", "http://proxy:3128")
	assert.Equal(t, []string{"hunter2"}, proxyPasswords())
}

func TestAppendNoProxy(t *testing.T) {
	assert.Equal(t, "a.example,b.example", appendNoProxy(" a.example , b.example "))
	// a host already listed is not repeated
	assert.Equal(t, "cache.local", appendNoProxy("cache.local", "cache.local"))
	assert.Empty(t, appendNoProxy("", "", ""))
}

func TestHostOf(t *testing.T) {
	assert.Equal(t, "cache.local", hostOf("http://cache.local:8088/"))
	assert.Equal(t, "192.168.1.10", hostOf("http://192.168.1.10:34567"))
	// nothing to bypass for a socket path or an unparseable URL
	assert.Empty(t, hostOf("unix:///var/run/docker.sock"))
	assert.Empty(t, hostOf("cache.local:8088"))
	assert.Empty(t, hostOf("://nope"))
}
