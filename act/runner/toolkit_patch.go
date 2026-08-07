// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gitea.com/gitea/runner/act/common"
	"gitea.com/gitea/runner/act/common/git"
	"gitea.com/gitea/runner/act/model"
)

// Actions bundle the @actions toolkit into their own JavaScript, and two of its lines keep it
// from working against Gitea. Both are edited out of the bundle the runner downloaded.
//
// isGhes() takes any host that is not github.com, *.ghe.com or *.localhost for GitHub
// Enterprise. @actions/cache then forces the v1 API, and @actions/artifact refuses outright,
// which is why the stock upload-artifact aborts here. The edit empties the last of the three
// hostname tests, so `endsWith('.LOCALHOST')` becomes `endsWith(”)`, which every hostname
// satisfies: one string literal, no call sites to resolve, and the same answer the toolkit's own
// proposed ACTIONS_VENDOR switch would give. Gitea already makes this edit by hand in its fork
// of upload-artifact.
//
// getCacheServiceURL() then resolves the cache service from ACTIONS_RESULTS_URL alone, where v1
// reads ACTIONS_CACHE_URL first. Both reads there are given the same preference, which is what
// keeps the runner out of the artifact path: the results URL still points at Gitea.
//
// Either of these landing upstream makes this file deletable:
//
//	https://github.com/actions/toolkit/pull/2123   — an ACTIONS_VENDOR switch, naming Gitea
//	https://github.com/actions/toolkit/issues/2439 — treat ACTIONS_RESULTS_URL as the signal
const (
	CacheServiceV2Env = "ACTIONS_CACHE_SERVICE_V2"
	cacheURLEnv       = "ACTIONS_CACHE_URL"
	resultsURLEnv     = "ACTIONS_RESULTS_URL"

	// localhostHost is the suffix isGhes accepts; emptying the test is what opens the gate,
	// because every hostname ends with the empty string.
	localhostHost = ".LOCALHOST"

	// artifactRefusal is the only thing the gate guards in @actions/artifact, which is what makes
	// such a bundle safe to open. A bundle carrying neither toolkit uses isGhes for something this
	// runner has not looked at, and is left alone.
	artifactRefusal = "GHESNotSupportedError"

	// sidecarSuffix names the directory of untouched copies, a sibling of the action directory
	// because that directory is copied wholesale into job containers.
	sidecarSuffix = ".toolkit-patch"

	// skipMarker in the sidecar means a patched bundle already failed once here.
	skipMarker = "skip"

	maxBundleSize = 64 << 20
)

var (
	// localhostTest matches the third hostname test of isGhes, in any quoting. The match is case
	// sensitive on purpose, and that is load-bearing: isGhes uppercases the hostname before
	// testing it, while undici, bundled into all of these actions, tests a lowercase ".localhost"
	// in isURLPotentiallyTrustworthy. Opening that one would tell its HTTP client that every URL
	// is trustworthy. Uppercase, the literal occurs nowhere but this test, across 118 bundles
	// covering every major version of sixteen actions.
	localhostTest = regexp.MustCompile(`endsWith\s*\(\s*` + quoted(regexp.QuoteMeta(localhostHost)) + `\s*\)`)

	// serviceURLBranches matches both branches of getCacheServiceURL at once: the v1 branch reads
	// the cache URL and falls back to the results URL, the v2 branch just below reads the results
	// URL alone. That `||` pairing is the only place the two variables are read together, so
	// matching them as one expression is what keeps the edit inside this function rather than
	// anywhere they happen to sit near each other. The branches are 21 bytes apart minified and
	// 63 not, across every bundle measured.
	serviceURLBranches = regexp.MustCompile(`(` + envRead(cacheURLEnv) + `\s*\|\|\s*)(` +
		envRead(resultsURLEnv) + `)((?s).{0,256}?)(` + envRead(resultsURLEnv) + `)`)

	// cacheURLFirst gives both reads the preference the v1 branch already had.
	cacheURLFirst = []byte(`${1}(process.env.` + cacheURLEnv + `||${2})${3}(process.env.` + cacheURLEnv + `||${4})`)
)

func envRead(name string) string {
	return `process\s*\.\s*env\s*(?:\.\s*` + name + `\b|\[\s*` + quoted(name) + `\s*\])`
}

// quoted matches a string literal in any of the three quote characters. RE2 has no
// backreferences, so the pairs are spelled out.
func quoted(pattern string) string {
	return "(?:'" + pattern + "'|\"" + pattern + "\"|`" + pattern + "`)"
}

// actionScriptPaths returns the entrypoints of a node action, the only kind with a bundle. Only
// remote actions get here: a local one lives in the user's checkout, which the runner does not
// rewrite.
func actionScriptPaths(dir string, action *model.Action) []string {
	if action == nil || !action.Runs.Using.IsNode() {
		return nil
	}
	var paths []string
	for _, script := range []string{action.Runs.Pre, action.Runs.Main, action.Runs.Post} {
		if script != "" {
			paths = append(paths, filepath.Join(dir, script))
		}
	}
	return paths
}

// patchToolkit edits the toolkit in an action's bundles, keeping each original beside them. Every
// failure is silent and leaves the bundle as it was, which costs the cache client the v2 API and
// an artifact action nothing at all.
func patchToolkit(ctx context.Context, actionDir string, scripts []string) {
	if len(scripts) == 0 {
		return
	}
	if _, err := os.Stat(filepath.Join(sidecarDir(actionDir), skipMarker)); err == nil {
		return
	}
	defer git.AcquireCloneLock(actionDir)()

	for _, script := range scripts {
		if err := patchBundle(script, originalFor(actionDir, script)); err != nil {
			common.Logger(ctx).Debugf("actions toolkit: %s left unpatched: %v", filepath.Base(script), err)
		}
	}
}

// revertToolkit puts the originals back and stops this action being patched again, so the next job
// runs it exactly as shipped. Called when a step failed with a patched bundle; it does not re-run
// the step, because a step's outputs and env-file writes are already recorded by then.
func revertToolkit(ctx context.Context, actionDir string, scripts []string) {
	if len(scripts) == 0 {
		return
	}
	if _, err := os.Stat(sidecarDir(actionDir)); err != nil {
		return
	}
	defer git.AcquireCloneLock(actionDir)()

	reverted := false
	for _, script := range scripts {
		original := originalFor(actionDir, script)
		if !isPatchOf(original, script) {
			continue
		}
		if err := os.Rename(original, script); err == nil {
			reverted = true
		}
	}
	if reverted {
		_ = os.WriteFile(filepath.Join(sidecarDir(actionDir), skipMarker), nil, 0o600)
		common.Logger(ctx).Warnf("actions toolkit: restored the original %s, it will not be patched again", filepath.Base(actionDir))
	}
}

// sidecarDir holds an action's untouched bundles, and the marker that stops it being patched.
func sidecarDir(actionDir string) string {
	return actionDir + sidecarSuffix
}

// originalFor is where a script's untouched copy lives, or "" for a script the action's own
// `runs` keys placed outside its directory, which is not this runner's to rewrite.
func originalFor(actionDir, script string) string {
	rel, err := filepath.Rel(actionDir, script)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.Join(sidecarDir(actionDir), rel)
}

// patchBundle rewrites one entrypoint in place. The untouched copy kept beside it is what marks
// the bundle as already patched.
func patchBundle(script, original string) error {
	if original == "" {
		return nil
	}
	if _, err := os.Stat(original); err == nil {
		if isPatchOf(original, script) {
			return nil
		}
		// The action's ref moved and git checked the new bundle out over the patched one, so
		// the pair no longer belongs together. Patch afresh rather than keep an original that
		// would restore an older version of the action.
		if err := os.Remove(original); err != nil {
			return err
		}
	}
	info, err := os.Stat(script)
	if err != nil {
		return err
	}
	if info.Size() > maxBundleSize {
		return nil
	}
	data, err := os.ReadFile(script)
	if err != nil {
		return err
	}
	patched, ok := patchedBundle(data)
	if !ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(original), 0o755); err != nil {
		return err
	}
	// The copy is taken before the bundle is replaced, so a write that fails part way can put the
	// action back as it was. A crash needs no handling: the clone executor checks the action out
	// and hard resets it on every prepare, so a half-written bundle never outlives the job.
	if err := os.WriteFile(original, data, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.WriteFile(script, patched, info.Mode().Perm()); err != nil {
		_ = os.Rename(original, script)
		return err
	}
	return nil
}

// isPatchOf reports whether script is exactly what patching original produced. It is what proves
// the two still belong together: an action whose ref moved is checked out over the patched bundle,
// leaving an original that would restore the version before the move.
func isPatchOf(original, script string) bool {
	data, err := os.ReadFile(original)
	if err != nil {
		return false
	}
	current, err := os.ReadFile(script)
	if err != nil {
		return false
	}
	patched, ok := patchedBundle(data)
	return ok && bytes.Equal(patched, current)
}

// patchedBundle opens the GHES gate, and where the cache toolkit is present, points the cache
// service at the cache server. A bundle this runner cannot account for comes back untouched.
func patchedBundle(data []byte) ([]byte, bool) {
	if !localhostTest.Match(data) {
		return data, false
	}
	switch {
	case bytes.Contains(data, []byte(CacheServiceV2Env)):
		// The cache toolkit: both edits or neither, because choosing v2 without redirecting the
		// URL would send the client to a results URL that serves no cache service.
		if !serviceURLBranches.Match(data) {
			return data, false
		}
	case bytes.Contains(data, []byte(artifactRefusal)):
		// The artifact toolkit, where the gate is a plain refusal and there is no URL to move:
		// artifacts already go to Gitea, which implements that service.
	default:
		return data, false
	}

	opened := localhostTest.ReplaceAllFunc(data, func(test []byte) []byte {
		// Drop the hostname from the test rather than rewriting the call, so the bundle's own
		// quoting survives and the result stays valid even inside a string literal.
		return bytes.Replace(test, []byte(localhostHost), nil, 1)
	})
	return serviceURLBranches.ReplaceAll(opened, cacheURLFirst), true
}
