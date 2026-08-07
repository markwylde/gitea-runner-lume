# Lifecycle, recovery, and cleanup

Depends on tasks 01, 03, 04, and 05.

## Checklist

- [x] Implement atomic owner-only leases and monotonic phases around every side
  effect from accepted task through verified deletion.
- [x] Gate upstream fetching by global capacity, Lume limit, resources,
  image health, reconciled state, and drain state.
- [x] Implement provision, boot, attest, execute, cancel, stop, process kill,
  ownership-checked delete, verified absence, and bounded cleanup deadlines.
- [x] Validate cleanup retry timing and Lume stop behavior on real VMs.
- [x] Reconcile leases and provider inventory before declaration/polling on
  startup; quarantine corrupt or ambiguous state.
- [x] Make graceful shutdown stop fetches and continue bounded job cleanup.
- [x] Handle restart reconciliation, transport cancellation, Lume command
  failure, low resources, and deletion failure with durable cleanup state.
- [ ] Inject host kill/reboot, runner crash, guest crash, Gitea loss, and real
  Lume interruption.
- [x] Add a deterministic exhaustive lease state-machine transition test.
- [ ] Add real-provider fault injection at every persisted transition.

## Done

Every accepted task reaches a reported terminal state and verified worker
absence, or leaves a visible retrying cleanup record that cannot harm unowned VMs.
