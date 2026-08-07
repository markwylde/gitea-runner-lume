# Upstream baseline and contract proofs

## Goal

Establish the official runner as the maintained base and prove every external
contract that can invalidate the Lume execution design.

## Checklist

- [x] Import a pinned `gitea.com/gitea/runner` commit with license, history
  metadata, module locks, tests, and an `UPSTREAM.md` rebase procedure.
- [ ] Build and run the unmodified upstream test suite on Apple Silicon macOS.
- [x] Record minimum/tested macOS, Gitea, Lume, Go, and guest versions.
- [ ] Capture runner registration, declaration, fetch, heartbeat, cancellation,
  log, artifact, cache, and result fixtures against a disposable real Gitea.
- [x] Prove `--token-file` precedence/security and owner-only, symlink-safe
  registration-file format and persistence locally.
- [x] Prove scope-by-token, runner visibility, online state, labels, and capacity
  match upstream UX against real Gitea.
- [x] Map the narrow upstream integration points for task execution, reporting,
  cancellation, health admission, configuration, and CLI additions.
- [x] Capture structured Lume version/list/get/clone/run/stop/delete contracts,
  storage selection, exit codes, timeouts, and the macOS VM concurrency ceiling.
- [x] Prove a guest transport that can carry upstream execution-environment
  operations and live output without host persistence or command-line secrets.
- [x] Decide and document support behaviour for JavaScript/composite actions,
  cache/artifacts, Docker actions, job containers, and service containers.

## Done

The baseline is reproducible and no unproven Gitea, upstream-runner, Lume, or
guest boundary can force a redesign of registration or execution.
