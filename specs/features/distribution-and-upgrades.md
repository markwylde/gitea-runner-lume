# Distribution and upgrades

## Outcome

Operators install reproducible macOS releases whose Gitea compatibility is
traceable to an exact upstream runner revision.

## Upstream maintenance

The repository is a downstream of `gitea.com/gitea/runner`. `UPSTREAM.md`
records the source URL, immutable commit, released version, import date, local
patch inventory, and rebase procedure. Upstream license and notices are
preserved.

Each update imports upstream tests unchanged where possible, reviews runner
protocol and workflow-engine changes, rebuilds guest and host from the same
revision, and runs registration plus real-job compatibility tests. Downstream
Lume code resides in dedicated packages and narrow integration points.

## Release

Releases are Go-built arm64 macOS binaries for host and guest use, notarized and
published with checksums, SBOM, provenance, license notices, configuration
migration notes, supported Gitea/Lume/macOS matrix, and pinned base-image agent
version. Builds use a pinned Go toolchain and locked modules.

An operator starts a release from the GitHub Actions `Trigger Release` workflow.
The workflow derives the next semantic version from conventional commits since
the latest version tag, runs release checks against the exact `main` commit, and
then creates the version tag. It builds, signs, notarizes, verifies, and
publishes only that tagged commit. A directly pushed tag does not publish a
release.

When an OpenRouter credential is configured, the workflow generates concise
release notes using only commits and changed-file evidence from the released git
range. AI note generation is advisory: failure falls back to GitHub-generated
notes and never bypasses verification or blocks a verified release.

Upgrades drain work before replacing the daemon. Schema migration is explicit,
backed up, and reversible. A host binary refuses incompatible guest agents and
base-image manifests.

## Acceptance outcomes

- A clean checkout reproduces release checksums in the documented environment.
- The complete upstream unit suite and downstream security suite pass.
- Rebase tooling reports every downstream conflict and patch.
- Upgrade with active work drains safely and preserves recoverable cleanup state.
