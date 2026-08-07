# Compatibility Matrix

This matrix distinguishes reviewed contracts from combinations proven in a
real environment. A row is supported only after the acceptance task is signed
off; source review alone is not a production claim.

| Component | Reviewed or pinned | Real acceptance |
| --- | --- | --- |
| Official Gitea Runner | commit `24c13a1fd0cac7981c0bb6af4aeeedb03f44ff46` | registration, polling, reporting, cache, and artifacts passed |
| Gitea | runner declaration API requires 1.21 or later | 1.27.0 passed against a private instance |
| Lume | 0.5.1 CLI, tag `lume-v0.5.1` | create, get, clone, set, run, stop, delete, and interruption recovery passed |
| Controller | Apple Silicon macOS | macOS 26.5.2 arm64 passed |
| Guest | Apple Silicon macOS, exact host/guest runner revision | macOS 26.6.1 arm64, Node 24.19.0, Apple Git 2.50.1 passed |
| Go | `go1.26.5` toolchain from `go.mod` | local unit/race/vet build passed |

Configuration allows only exact `lume --version` strings listed by the
operator. Signed image manifests bind the guest OS, architecture, OS version,
agent revision, agent binary SHA-256, guest public key, and non-root UID.
