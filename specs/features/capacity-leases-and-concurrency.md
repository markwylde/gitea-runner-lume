# Capacity leases and concurrency

## Outcome

The runner accepts no more work than it can isolate in Lume, and accepted tasks
remain recoverable through failure and restart.

## Admission

The upstream runner advertises configured capacity, but each fetched task must
also pass local admission before VM creation. Admission requires a healthy
validated profile, available global and profile concurrency, CPU/memory/disk
budget, free-space reserve, reconciled state, and an accepting service state.

The runner does not fetch beyond locally available capacity. If health changes
after fetch, it reports a clear infrastructure failure rather than running on
the host or waiting without a deadline.

## Lease model

Before creating a VM, an atomic owner-only lease records schema, installation
identity, upstream task ID, profile, unpredictable worker ID, base-image
generation, phase, deadlines, provider ownership evidence, and last failure.
Transitions are monotonic and persisted before and after each side effect.

Lease state contains no workflow, event, repository, token, secret, command, or
log content. Task ID is diagnostic correlation, not authorization to delete.

## Drain and recovery

Graceful drain stops upstream polling and allows active jobs to their deadlines
while cleanup continues. Forced termination leaves enough non-secret lease state
for startup reconciliation. Reconciliation completes cleanup before the runner
declares itself ready or fetches work.

## Acceptance outcomes

- Fetch concurrency and running VMs never exceed configured or platform limits.
- Controller restart at every transition yields one recovered task outcome and
  verified deletion or a visible cleanup failure.
- Low resources make the runner unavailable before another task is fetched.
- Lease corruption blocks polling and cannot authorize deletion.
