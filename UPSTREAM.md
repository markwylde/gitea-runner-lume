# Upstream Gitea Runner

Gitea Runner Lume is a downstream distribution of the official Gitea Runner.

- Source: `https://gitea.com/gitea/runner.git`
- Imported commit: `24c13a1fd0cac7981c0bb6af4aeeedb03f44ff46`
- Commit date: 2026-08-06
- Import date: 2026-08-06
- Go module: `gitea.com/gitea/runner`
- License: MIT, preserved in `LICENSE`

The module path intentionally remains upstream's path. This minimizes patch
noise in internal imports and makes upstream test comparison straightforward.
The distributed executable is branded `gitea-runner-lume`.

## Downstream boundaries

Downstream changes should remain in these areas:

- `internal/pkg/lume`: Lume process, ownership, and lifecycle contracts;
- `internal/pkg/config`: the additional `lume` configuration section;
- `internal/pkg/labels`: the `lume://` execution schema;
- a narrow Actions execution-environment integration point;
- macOS image, guest-agent, recovery, service, and diagnostic commands; and
- product specifications and downstream tests.

Registration, runner protocol, polling, task reporting, workflow semantics,
cache, artifacts, cancellation, and unrelated upstream behaviour should remain
unchanged.

## Updating upstream

1. Fetch the desired official runner revision into a temporary clone.
2. Record its immutable commit, version, Go toolchain, and protocol dependency.
3. Diff the prior and new upstream trees before applying downstream patches.
4. Import the new tree while preserving `AGENTS.md`, `specs/`, this file, and
   downstream-only packages.
5. Resolve conflicts at the narrow integration points and document any expanded
   patch surface here.
6. Run the complete upstream suite unchanged, downstream unit/security tests,
   and real Gitea registration and job acceptance tests.
7. Update the compatibility matrix only after host and guest binaries built
   from the same revision pass the real-environment suite.
