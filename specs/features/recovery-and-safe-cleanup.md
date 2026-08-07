# Recovery and safe cleanup

## Outcome

A crash, reboot, killed process, network partition, or partial dependency
failure cannot permanently leak managed workers or cause unrelated VMs to be
deleted.

## Durable state

Lease and ownership state is stored as atomic, owner-only versioned records.
Durable transition is written before each external
side effect where recovery would otherwise be ambiguous. The database stores
no workflow output or secret value.

The controller maintains a stable random installation identity. Each worker
has independent ownership evidence in controller state and a guest/VM metadata
manifest where supported. Ownership checks bind installation, lease, base
generation, storage, and VM identity.

## Startup reconciliation

Before accepting demand, startup:

1. obtains the single-controller lock;
2. loads and validates durable leases;
3. lists Lume resources in the configured storage;
4. classifies accepted task leases using their persisted phase and the upstream
   runner's active task set;
5. classifies each lease as resumable, terminal, stale, ambiguous, or already
   clean;
6. resumes monitoring or cleanup; and
7. reports ambiguous resources without deleting them.

Periodic reconciliation repeats bounded subsets of the same checks. Cleanup is
idempotent and retryable with capped exponential backoff. Permanent failure is
prominent in status and logs and blocks capacity if resource bounds would be
violated.

## Manual cleanup

`cleanup` defaults to a read-only plan. Applying it requires confirmation and
uses the same ownership proof as automatic cleanup. State loss does not
authorize prefix-based deletion; recovery instead offers an explicit adoption
or manual Lume remediation path.

## Acceptance outcomes

- Killing the controller after every external side effect and restarting it
  converges to the correct active or deleted state.
- Reconciliation is safe with corrupt state, unavailable Gitea, unavailable
  Lume, reused IP addresses, and adversarial VM names.
- Two controller processes cannot manage the same installation concurrently.
- Cleanup retries remain bounded and observable without blocking status or
  graceful service control.
