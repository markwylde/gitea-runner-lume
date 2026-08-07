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

Upgrades drain work before replacing the daemon. Schema migration is explicit,
backed up, and reversible. A host binary refuses incompatible guest agents and
base-image manifests.

## Acceptance outcomes

- A clean checkout reproduces release checksums in the documented environment.
- The complete upstream unit suite and downstream security suite pass.
- Rebase tooling reports every downstream conflict and patch.
- Upgrade with active work drains safely and preserves recoverable cleanup state.
