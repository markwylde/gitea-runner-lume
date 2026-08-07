// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package run

import (
	"context"
	"net/url"
	"os"
	"slices"
	"strings"

	"gitea.com/gitea/runner/act/container"

	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http/httpproxy"
)

// proxyFromEnv returns the runner's own proxy configuration, or nil when it has none.
func proxyFromEnv() *httpproxy.Config {
	cfg := httpproxy.FromEnvironment()
	if cfg.HTTPProxy == "" && cfg.HTTPSProxy == "" {
		return nil
	}
	return cfg
}

// JobProxyEnv returns the proxy variables a job runs with, given what runner.envs already
// put in jobEnvs. Gitea is deliberately not made direct, so it stays reachable however the
// runner reaches it.
func JobProxyEnv(jobEnvs map[string]string, cacheURL string, serviceNames []string) map[string]string {
	cfg := proxyFromEnv()
	if cfg == nil {
		return nil
	}

	// Go bypasses loopback on its own, curl and most other tools in a job do not.
	direct := append([]string{"localhost", "127.0.0.1", "::1"}, serviceNames...)
	direct = append(direct, hostOf(cacheURL))

	proxyEnv := map[string]string{}
	setPair := func(lower, upper, value string) {
		if value == "" {
			return
		}
		// Either spelling in runner.envs takes over both, so the pair cannot disagree.
		if existing, ok := jobEnvs[lower]; ok {
			value = existing
		} else if existing, ok := jobEnvs[upper]; ok {
			value = existing
		}
		proxyEnv[lower], proxyEnv[upper] = value, value
	}
	setPair("http_proxy", "HTTP_PROXY", cfg.HTTPProxy)
	setPair("https_proxy", "HTTPS_PROXY", cfg.HTTPSProxy)

	// no_proxy is merged rather than replaced: the hosts above are structural, and an
	// operator cannot list the cache server's startup-assigned address in advance.
	noProxy := appendNoProxy(cfg.NoProxy, direct...)
	for _, name := range []string{"no_proxy", "NO_PROXY"} {
		if existing, ok := jobEnvs[name]; ok {
			noProxy = appendNoProxy(existing, strings.Split(noProxy, ",")...)
			break
		}
	}
	proxyEnv["no_proxy"], proxyEnv["NO_PROXY"] = noProxy, noProxy
	return proxyEnv
}

// BypassProxyForDockerHost keeps the runner's Docker API traffic off the proxy: the docker
// client proxies every transport that is not a unix socket or a named pipe, so a tcp://
// daemon would be reached through a proxy that cannot route to it.
//
// It must run before the first Docker API call, because the standard library resolves the
// proxy environment once.
func BypassProxyForDockerHost(dockerHost string) {
	cfg := proxyFromEnv()
	if cfg == nil {
		return
	}
	host := hostOf(dockerHost)
	if host == "" {
		// A unix socket or named pipe is never proxied.
		return
	}

	noProxy := appendNoProxy(cfg.NoProxy, host)
	for _, name := range []string{"no_proxy", "NO_PROXY"} {
		if err := os.Setenv(name, noProxy); err != nil {
			log.Warnf("cannot set %s for the runner process: %v", name, err)
		}
	}
	log.Debugf("docker host %s is reached directly, no_proxy is now %q", host, noProxy)
}

// WarnIfDaemonHasNoProxy points at the one part the runner cannot set: the docker daemon
// pulls the images, and a daemon in its own container needs its own proxy.
func WarnIfDaemonHasNoProxy(ctx context.Context) {
	if proxyFromEnv() == nil {
		return
	}
	info, err := container.GetHostInfo(ctx)
	if err != nil {
		log.Debugf("cannot read the docker daemon's proxy configuration: %v", err)
		return
	}
	if info.HTTPProxy == "" && info.HTTPSProxy == "" {
		log.Warn("the runner has a proxy but the docker daemon reports none, so image pulls will not use it: https://docs.docker.com/engine/daemon/proxy/")
	}
}

// proxyPasswords returns the passwords embedded in the runner's proxy URLs, to mask before
// a job echoes its environment.
func proxyPasswords() []string {
	cfg := proxyFromEnv()
	if cfg == nil {
		return nil
	}

	var passwords []string
	for _, raw := range []string{cfg.HTTPProxy, cfg.HTTPSProxy} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.User == nil {
			continue
		}
		if password, ok := parsed.User.Password(); ok && password != "" && !slices.Contains(passwords, password) {
			passwords = append(passwords, password)
		}
	}
	return passwords
}

// appendNoProxy adds hosts to a no_proxy list, keeping the operator's entries and adding
// none twice.
func appendNoProxy(noProxy string, hosts ...string) string {
	entries := []string{}
	for entry := range strings.SplitSeq(noProxy, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			entries = append(entries, entry)
		}
	}

	for _, host := range hosts {
		if host == "" || slices.Contains(entries, host) {
			continue
		}
		entries = append(entries, host)
	}
	return strings.Join(entries, ",")
}

// hostOf returns the host of a URL without its port, the form a no_proxy entry takes. It is
// empty for anything without a network host, such as a unix socket.
func hostOf(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(strings.TrimSuffix(raw, "/"))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	return parsed.Hostname()
}
