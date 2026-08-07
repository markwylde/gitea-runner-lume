// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitea.com/gitea/runner/act/model"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The three shapes real bundlers emit, reduced to the bytes that matter: the version gate, and
// the URL getter that follows it. tsc keeps the names, webpack prefixes them, esbuild mangles
// them, writes ternaries in place of the switch, and records the real name in the export
// assignment. Each carries both reads of the results URL, as the real getter does.
const (
	urlTSC     = `function getCacheServiceURL() {` + "\n" + `    switch (getCacheServiceVersion()) {` + "\n" + `        case 'v1':` + "\n" + `            return (process.env['ACTIONS_CACHE_URL'] || process.env['ACTIONS_RESULTS_URL'] || '');` + "\n" + `        case 'v2':` + "\n" + `            return process.env['ACTIONS_RESULTS_URL'] || '';` + "\n" + `    }` + "\n" + `}`
	urlEsbuild = `function YK(){let e=XK();return e==="v1"?process.env.ACTIONS_CACHE_URL||process.env.ACTIONS_RESULTS_URL||"":e==="v2"?process.env.ACTIONS_RESULTS_URL||"":""}`

	isGhesTSC   = `function isGhes(){const h=new URL(process.env['GITHUB_SERVER_URL']||'https://github.com').hostname.toUpperCase();return h!=='GITHUB.COM'&&!h.endsWith('.GHE.COM')&&!h.endsWith('.LOCALHOST')}`
	gateTSC     = isGhesTSC + "\n" + `function getCacheServiceVersion() {` + "\n" + `    if (isGhes())` + "\n" + `        return 'v1';` + "\n" + `    return process.env['ACTIONS_CACHE_SERVICE_V2'] ? 'v2' : 'v1';` + "\n" + `}` + "\n" + urlTSC
	gateWebpack = `function config_isGhes(){const h=new URL(process.env['GITHUB_SERVER_URL']||'https://github.com').hostname.toUpperCase();return h!=='GITHUB.COM'&&!h.endsWith('.GHE.COM')&&!h.endsWith('.LOCALHOST')}` + "\n" + `function config_getCacheServiceVersion() {` + "\n" + `    if (config_isGhes())` + "\n" + `        return 'v1';` + "\n" + `    return process.env['ACTIONS_CACHE_SERVICE_V2'] ? 'v2' : 'v1';` + "\n" + `}` + "\n" + urlTSC
	gateEsbuild = `vu.isGhes=$K;vu.getCacheServiceVersion=XK;function $K(){let e=new URL(process.env.GITHUB_SERVER_URL||"https://github.com").hostname.toUpperCase(),r=e==="GITHUB.COM",n=e.endsWith(".GHE.COM"),i=e.endsWith(".LOCALHOST");return!r&&!n&&!i}function XK(){return $K()?"v1":process.env.ACTIONS_CACHE_SERVICE_V2?"v2":"v1"}` + urlEsbuild
)

func TestPatchedBundle(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		wantPatched bool
	}{
		{"tsc keeps the names", gateTSC, true},
		{"webpack prefixes them", gateWebpack, true},
		{"esbuild mangles and minifies them", gateEsbuild, true},
		// A bundler picks its own quoting; gateTSC is single-quoted already.
		{"double-quoted", requoted(`"`), true},
		{"backtick-quoted", requoted("`"), true},
		// sccache-action sets the variable itself; there is no gate to open.
		{"mentions the variable without the gate", `core.exportVariable("ACTIONS_CACHE_SERVICE_V2","on")`, false},
		// Both edits or neither: a gate patched without the URL would send the client to a
		// results URL that serves no cache service.
		{"gate without a recognisable url getter", strings.TrimSuffix(gateTSC, "\n"+urlTSC), false},
		// And the other way round: an action that reads both variables but has no gate to open.
		{"url getter without a gate", urlTSC, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, patched := patchedBundle([]byte(tc.body))
			assert.Equal(t, tc.wantPatched, patched)
			if !tc.wantPatched {
				assert.Equal(t, tc.body, string(out), "an unpatched bundle must come back byte for byte")
				return
			}
			assert.True(t, gateOpened(string(out)))
			// The other two hostname tests are left alone, so a host that really is GitHub or
			// GHES is still recognised as such.
			assert.NotContains(t, string(out), ".LOCALHOST", "the localhost test is the one that opens")
			assert.Contains(t, string(out), ".GHE.COM")

			// Every read of the results URL now prefers the cache URL, and none was lost: the
			// results URL stays the fallback, so a runner not serving the cache still works.
			assert.Equal(t, strings.Count(tc.body, "ACTIONS_RESULTS_URL"), strings.Count(string(out), "ACTIONS_RESULTS_URL"))
			assert.Equal(t, strings.Count(tc.body, "ACTIONS_RESULTS_URL"),
				strings.Count(string(out), "(process.env.ACTIONS_CACHE_URL||process.env"))
		})
	}
}

// undici, bundled into every one of these actions, decides whether to trust a URL with a
// lowercase test that reads almost the same. Opening it would tell the HTTP client that every URL
// is trustworthy, so the uppercase the toolkit produces is what separates them.
func TestPatchedBundleLeavesTrustworthyURLCheckAlone(t *testing.T) {
	const undici = `if(n.hostname==="localhost"||n.hostname.includes("localhost.")||n.hostname.endsWith(".localhost")){return true}`

	out, patched := patchedBundle([]byte(undici + gateTSC))
	require.True(t, patched)
	assert.Contains(t, string(out), undici, "the trustworthy-URL check must survive byte for byte")
	assert.True(t, gateOpened(string(out)))
}

// The artifact toolkit puts the same gate in front of a plain refusal, with no URL to move, so
// opening it is what lets the stock upload-artifact work against Gitea instead of aborting.
func TestPatchedBundleOpensTheArtifactRefusal(t *testing.T) {
	const artifact = isGhesTSC + "\n" + `uploadArtifact(){if(isGhes()){throw new GHESNotSupportedError()}}`

	out, patched := patchedBundle([]byte(artifact))
	assert.True(t, patched)
	assert.True(t, gateOpened(string(out)))
	assert.Contains(t, string(out), "GHESNotSupportedError", "the refusal itself is left in place, it just stops firing")

	// A bundle using the gate for something this runner has not accounted for is not touched.
	unknown := strings.Replace(artifact, "GHESNotSupportedError", "SomeOtherError", 1)
	out, patched = patchedBundle([]byte(unknown))
	assert.False(t, patched)
	assert.Equal(t, unknown, string(out))
}

// requoted respells gateTSC's string literals with another quote character.
func requoted(quote string) string {
	gate := strings.ReplaceAll(gateTSC, `'.LOCALHOST'`, quote+".LOCALHOST"+quote)
	gate = strings.ReplaceAll(gate, `['ACTIONS_RESULTS_URL']`, "["+quote+"ACTIONS_RESULTS_URL"+quote+"]")
	return strings.ReplaceAll(gate, `['ACTIONS_CACHE_URL']`, "["+quote+"ACTIONS_CACHE_URL"+quote+"]")
}

// gateOpened reports whether the hostname test was emptied, in whatever quoting the bundle used.
func gateOpened(body string) bool {
	return strings.Contains(body, "endsWith(") && !strings.Contains(body, ".LOCALHOST")
}

// The patched bundle must still be JavaScript, and must resolve the way the runner needs: v2 for
// an ordinary Gitea host, the cache server for the service URL, and the results URL when there is
// no cache server. Unpatched, the same bundle must still choose v1, or the patch proves nothing.
func TestPatchedBundleBehavesInNode(t *testing.T) {
	requireHostTools(t, "node")

	eval := func(t *testing.T, bundle, prelude, cacheURL string) string {
		t.Helper()

		script := prelude + bundle + "\nprocess.stdout.write(getCacheServiceVersion()+' '+getCacheServiceURL())"
		cmd := exec.CommandContext(t.Context(), "node", "-e", script)
		cmd.Env = append(os.Environ(),
			"ACTIONS_CACHE_SERVICE_V2=true",
			"ACTIONS_CACHE_URL="+cacheURL,
			"ACTIONS_RESULTS_URL=https://gitea.example",
			"GITHUB_SERVER_URL=https://gitea.example",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return string(out)
	}

	for _, tc := range []struct{ name, bundle, prelude string }{
		{"tsc", gateTSC, ""},
		{"webpack", gateWebpack, "const getCacheServiceVersion=()=>config_getCacheServiceVersion();"},
		{"esbuild", gateEsbuild, "var vu={};const getCacheServiceVersion=()=>XK(),getCacheServiceURL=()=>YK();"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Unpatched, a Gitea host is taken for GHES: v1, whose branch already reads the
			// cache URL. The patch has to move the version without moving that.
			assert.Equal(t, "v1 http://cache:8088/", eval(t, tc.bundle, tc.prelude, "http://cache:8088/"))

			patched, ok := patchedBundle([]byte(tc.bundle))
			require.True(t, ok)

			assert.Equal(t, "v2 http://cache:8088/", eval(t, string(patched), tc.prelude, "http://cache:8088/"))
			assert.Equal(t, "v2 https://gitea.example", eval(t, string(patched), tc.prelude, ""),
				"with no cache server the results URL is still the fallback")
		})
	}
}

// A bundler that embeds module sources as strings, such as webpack with devtool: eval, carries
// the gate inside a double-quoted literal. Rewriting the call rather than emptying its argument
// would end that string early and leave the bundle unparseable.
func TestPatchedBundleSurvivesInsideAStringLiteral(t *testing.T) {
	requireHostTools(t, "node")

	escaped := strings.ReplaceAll(gateTSC, `"`, `\"`)
	embedded := `eval("` + strings.ReplaceAll(escaped, "\n", `\n`) + `");`
	out, patched := patchedBundle([]byte(embedded))
	require.True(t, patched)

	file := filepath.Join(t.TempDir(), "bundle.js")
	require.NoError(t, os.WriteFile(file, out, 0o600))
	checked, err := exec.CommandContext(t.Context(), "node", "--check", file).CombinedOutput()
	require.NoError(t, err, "%s", checked)
}

func TestPatchBundleKeepsTheOriginal(t *testing.T) {
	dir, script := bundleFile(t, gateTSC)
	original := originalFor(dir, script)

	require.NoError(t, patchBundle(script, original))
	patched, err := os.ReadFile(script)
	require.NoError(t, err)
	assert.True(t, gateOpened(string(patched)))

	kept, err := os.ReadFile(original)
	require.NoError(t, err)
	assert.Equal(t, gateTSC, string(kept), "the untouched bundle is kept outside the action tree")
	assert.NotContains(t, original, dir+string(filepath.Separator), "originals must not ship into job containers")

	// Patching again must not stack, and must not overwrite the kept original.
	require.NoError(t, patchBundle(script, original))
	again, err := os.ReadFile(script)
	require.NoError(t, err)
	assert.Equal(t, string(patched), string(again))
	kept, err = os.ReadFile(original)
	require.NoError(t, err)
	assert.Equal(t, gateTSC, string(kept))
}

// A bundle with nothing to patch is left exactly as it was, with no original kept beside it.
func TestPatchBundleLeavesOtherActionsAlone(t *testing.T) {
	dir, script := bundleFile(t, `console.log("checkout")`)
	original := originalFor(dir, script)

	require.NoError(t, patchBundle(script, original))
	body, err := os.ReadFile(script)
	require.NoError(t, err)
	assert.Equal(t, `console.log("checkout")`, string(body))
	_, err = os.Stat(original)
	assert.True(t, os.IsNotExist(err), "no original is kept for a bundle that was not patched")
}

// bundleFile writes one entrypoint into a fresh action directory.
func bundleFile(t *testing.T, body string) (dir, script string) {
	t.Helper()

	dir = t.TempDir()
	script = filepath.Join(dir, "index.js")
	require.NoError(t, os.WriteFile(script, []byte(body), 0o600))
	return dir, script
}

func TestActionScriptPaths(t *testing.T) {
	node := &model.Action{Runs: model.ActionRuns{Using: "node20", Main: "dist/restore/index.js", Post: "dist/save/index.js"}}
	assert.Equal(t, []string{"/a/dist/restore/index.js", "/a/dist/save/index.js"}, actionScriptPaths("/a", node))

	// Only a node action has a bundle to patch.
	assert.Nil(t, actionScriptPaths("/a", &model.Action{Runs: model.ActionRuns{Using: "docker", Image: "alpine"}}))
	assert.Nil(t, actionScriptPaths("/a", nil))
}

// A step that fails with a patched bundle gets the untouched bundle back, and the action is not
// patched again, so later jobs run it exactly as its author shipped it.
func TestRevertToolkit(t *testing.T) {
	dir, script := bundleFile(t, gateTSC)
	scripts := []string{script}

	patchToolkit(t.Context(), dir, scripts)
	body, err := os.ReadFile(script)
	require.NoError(t, err)
	require.True(t, gateOpened(string(body)), "precondition: the bundle is patched")

	revertToolkit(t.Context(), dir, scripts)
	body, err = os.ReadFile(script)
	require.NoError(t, err)
	assert.Equal(t, gateTSC, string(body), "the original bundle is back")

	// The skip marker survives, so the action stays unpatched from now on.
	patchToolkit(t.Context(), dir, scripts)
	body, err = os.ReadFile(script)
	require.NoError(t, err)
	assert.Equal(t, gateTSC, string(body), "a reverted action stays unpatched")
}

// An action whose ref moves is checked out over the patched bundle. The kept original then
// belongs to the version before the move, and must not be restored over the new one.
func TestPatchBundleAfterTheActionMoved(t *testing.T) {
	dir, script := bundleFile(t, gateTSC)
	original := originalFor(dir, script)
	scripts := []string{script}

	patchToolkit(t.Context(), dir, scripts)
	require.NoError(t, os.WriteFile(script, []byte(gateWebpack), 0o600)) // the new version lands

	// Reverting must not roll the action back to the version the original came from.
	revertToolkit(t.Context(), dir, scripts)
	body, err := os.ReadFile(script)
	require.NoError(t, err)
	assert.Equal(t, gateWebpack, string(body))

	// Nothing was reverted, so the action is not marked off either: the new version is patched
	// in its own right, and keeps its own original.
	require.NoFileExists(t, filepath.Join(sidecarDir(dir), skipMarker))
	require.NoError(t, patchBundle(script, original))
	body, err = os.ReadFile(script)
	require.NoError(t, err)
	assert.True(t, gateOpened(string(body)))
	kept, err := os.ReadFile(original)
	require.NoError(t, err)
	assert.Equal(t, gateWebpack, string(kept))
}

// The wiring: a step patches its own bundles only when the runner serves the v2 API, and a step
// that fails gets them back. The action's path inside its repository is part of where they live.
func TestStepActionRemoteToolkitPatch(t *testing.T) {
	newStep := func(t *testing.T, patch bool) (*stepActionRemote, string) {
		t.Helper()

		sar := &stepActionRemote{
			Step:         &model.Step{Uses: "owner/repo/sub@v1"},
			remoteAction: &remoteAction{Org: "owner", Repo: "repo", Path: "sub", Ref: "v1"},
			action:       &model.Action{Runs: model.ActionRuns{Using: "node20", Main: "index.js"}},
			RunContext: &RunContext{
				Config: &Config{ActionCacheDir: t.TempDir(), PatchToolkit: patch},
			},
		}
		script := filepath.Join(sar.actionDir(), "sub", "index.js")
		require.NoError(t, os.MkdirAll(filepath.Dir(script), 0o755))
		require.NoError(t, os.WriteFile(script, []byte(gateTSC), 0o600))
		return sar, script
	}

	t.Run("left alone when the runner does not patch", func(t *testing.T) {
		sar, script := newStep(t, false)
		require.NoError(t, sar.patchActionToolkit(t.Context()))

		body, err := os.ReadFile(script)
		require.NoError(t, err)
		assert.Equal(t, gateTSC, string(body))
	})

	t.Run("patched, and put back when the step fails", func(t *testing.T) {
		sar, script := newStep(t, true)
		require.NoError(t, sar.patchActionToolkit(t.Context()))

		body, err := os.ReadFile(script)
		require.NoError(t, err)
		require.True(t, gateOpened(string(body)))

		failed := errors.New("the step failed")
		require.ErrorIs(t, sar.revertToolkitOnFailure(func(context.Context) error { return failed })(t.Context()), failed)

		body, err = os.ReadFile(script)
		require.NoError(t, err)
		assert.Equal(t, gateTSC, string(body))
	})
}
