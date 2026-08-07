# Service and diagnostics

Depends on tasks 02 and 06.

## Checklist

- [x] Implement foreground daemon lifecycle using upstream polling and
  downstream reconciliation/drain hooks.
- [x] Implement owner-only LaunchAgent install/start/stop/status/uninstall with
  no secrets in plist or arguments.
- [x] Extend upstream metrics/readiness with Lume admission, phase duration, and
  cleanup failure using bounded labels.
- [x] Add live guest-health readiness from a booted worker.
- [x] Implement status and doctor checks for registration, Gitea protocol,
  config, Lume, images, state, resources, and guest identity.
- [x] Add doctor probes for a real guest transport and installed service files.
- [ ] Add bounded structured logging and audit every field for workflow/secret
  leakage and cardinality.
- [ ] Test logout, restart, drain, signals, crash loops, inaccessible identities,
  bad service files, dependency partitions, and log rotation.

## Done

The runner operates unattended, accurately advertises readiness, drains safely,
and provides actionable diagnostics without retaining workflow or secret data.
