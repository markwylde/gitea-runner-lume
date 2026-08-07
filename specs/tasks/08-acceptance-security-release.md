# Acceptance, security, and release

Depends on all prior tasks.

## Checklist

- [x] Run macOS CI with `go vet`, selected race-enabled downstream suites,
  upstream engine unit suites, and an arm64 build.
- [ ] Complete the dependency/license audit, secret scan, fuzzers, full upstream
  and downstream suites, and reproducible arm64 build proof.
- [x] Register against a clean real Gitea using only instance URL, registration
  token, name, and Lume labels; prove correct scope and visible online runner.
- [ ] Execute real success, step failure, timeout, cancellation, checkout,
  JavaScript, composite, cache, artifact, and secret-masking workflows.
- [ ] Prove Docker/service workflows fail before code execution and host canaries
  remain unreadable.
- [ ] Inject duplicate fetch response, Gitea loss, guest crash, daemon kill at
  every phase, host reboot, Lume failure, low disk, and delete failure.
- [ ] Verify every terminal path deletes the owned VM and adversarial/unrelated
  VMs survive cleanup.
- [ ] Scan host arguments, environment, files, state, plist, logs, metrics, crash
  output, base image, and retained disks for secret canaries.
- [x] Configure macOS-arm64-only archives, checksums, SBOM generation, a
  compatibility matrix, install runbook, security model, and rebase guide.
- [x] Sign and notarize releases with production credentials and publish their
  checksums, SBOMs, and notarization records.
- [ ] Generate release provenance, publish complete license notices, and
  reproduce a release checksum in the pinned environment.

## Done

An operator can install and register the product like the official runner, run
native macOS Actions jobs exclusively in disposable Lume VMs, and independently
verify reporting, isolation, recovery, and cleanup.
