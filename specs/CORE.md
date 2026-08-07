# Gitea Runner Lume core product specification

## Product

Gitea Runner Lume is a downstream distribution of the official Gitea Runner
that executes macOS Actions jobs in disposable Lume virtual machines instead
of Docker containers or on the physical Mac.

To Gitea and to an operator it behaves like the official runner: the operator
registers it with a Gitea instance, organization, or repository runner token,
it appears in the corresponding Actions runner page, its daemon polls Gitea
for compatible jobs, and it reports logs, artifacts, cancellation, and results
through the official runner protocol. It does not create or require webhooks.

## Core model

- The **runner daemon** is the registered official-runner-compatible process on
  the Apple Silicon controller Mac. It performs only trusted runner control
  operations and never executes workflow-provided commands.
- The **runner registration** is the ordinary Gitea registration produced by
  `register`. Its scope is determined entirely by the token issued by Gitea;
  configuration does not hard-code a repository or organization.
- A **Lume label** maps a Gitea `runs-on` label to a validated base image and
  bounded VM resources. Only labels configured by the operator are declared.
- A **base image** is a stopped, validated macOS VM containing the trusted guest
  agent and generic Actions prerequisites, but no Gitea credentials, runner
  registration, host credentials, project checkout, or Actions secrets.
- A **worker VM** is a fresh clone used for exactly one accepted job and deleted
  after every terminal outcome.
- The **guest agent** is a narrowly scoped component of this distribution. It
  receives one authenticated job execution stream, runs the upstream Actions
  engine inside the guest, and returns protocol events to the host daemon.
- A **managed lease** records the accepted Gitea task, unpredictable worker
  identity, verified Lume ownership, deadlines, and cleanup state.

## Product goals

1. Preserve the official Gitea Runner registration, daemon, configuration,
   polling, job reporting, labels, and service UX wherever Lume configuration
   does not require an additional option.
2. Reuse the upstream runner protocol client, workflow engine, log reporter,
   cache, artifact, cancellation, and compatibility behaviour rather than
   reimplementing them.
3. Replace host/Docker job execution for configured macOS labels with one fresh
   Lume VM per accepted job.
4. Keep the physical Mac outside the workflow execution and credential boundary.
5. Destroy every worker after success, failure, timeout, cancellation, daemon
   interruption, or host restart, while never deleting an unproven VM.
6. Track a pinned upstream runner revision and keep the downstream patch small,
   reviewable, tested, and routinely rebaseable.

## Supported platform

The initial product supports Apple Silicon macOS 14 or newer, a compatible
Lume CLI, macOS guests using Apple's Virtualization framework, and Gitea
versions supported by the pinned upstream runner. Concurrency is bounded by
configuration and Lume's effective macOS guest limit.

The downstream runner declares labels using `label:lume://IMAGE`. `IMAGE` is a
configuration key, not an arbitrary VM name supplied by workflow content.
Docker and direct host labels are disabled by default in this distribution.

## Operator experience

Normal setup mirrors the official runner:

```text
gitea-runner-lume register
gitea-runner-lume config init
gitea-runner-lume image create|bootstrap|adopt|validate
gitea-runner-lume daemon
```

`register` accepts the same instance URL, registration token, runner name, and
labels as upstream, including interactive and non-interactive operation. The
registration token may be read from a file. Successful registration creates
the ordinary runner registration file and immediately makes the runner visible
in Gitea. No API token, webhook URL, webhook secret, or repository owner/name is
requested.

## Gitea boundary

The pinned upstream runner remains authoritative for registration, declaration,
long polling, task acquisition, task heartbeats, cancellation, workflow model,
expressions, action downloads, secrets, logs, cache, artifacts, and final task
results. Downstream code must not introduce a second Gitea API or protocol
implementation where an upstream path exists.

## Host and guest boundary

After a task is accepted, the official upstream engine parses and orchestrates
the workflow in bounded host memory. Workflow-created commands and environments
are delivered over an authenticated encrypted channel to the trusted guest
agent. Task values are never materialized in host process arguments,
environment, logs, shell input, or general-purpose host files.

The guest runs all workflow-created processes behind the upstream execution
environment interface. The host does not provide a fallback executor. If guest
creation, attestation, transport, or execution fails, the task fails closed and
cleanup begins.

## Worker lifecycle

For each accepted task the runner:

1. atomically records a lease before creating capacity;
2. resolves the declared Lume label to an operator-owned image profile;
3. clones the exact validated base image with an unpredictable managed identity;
4. starts the VM and verifies Lume state, guest identity, and agent attestation;
5. opens one mutually authenticated, size-bounded execution stream;
6. runs every upstream Actions process through the guest while the host-side
   engine and reporter communicate with Gitea;
7. propagates cancellation and deadlines to the guest;
8. stops and deletes the VM, then verifies absence; and
9. closes the lease only after cleanup succeeds, otherwise retaining a visible
   retryable cleanup record.

## Security invariants

- Workflow-controlled data never becomes a host command, path, environment
  variable, Lume argument, VM ownership claim, or deletion target.
- No host home directory, project directory, Keychain, SSH agent, Unix socket,
  clipboard, USB device, or arbitrary disk is mounted into a worker.
- Runner registration credentials remain on the host. A guest receives only
  per-task credentials already present in the accepted upstream task and only
  after authenticated attestation.
- The base image contains no reusable Gitea or project credential.
- VM deletion requires durable installation identity, lease identity, exact
  storage identity, and provider-observed ownership metadata. A name prefix is
  never sufficient.
- Secrets and workflow output use the upstream masking/reporting path and are
  not copied into controller diagnostics.
- Unsupported host or Docker execution for a Lume label fails closed.

## Reliability invariants

- The upstream poller acquires each task once; downstream code does not create
  speculative capacity from webhooks.
- Capacity and resource admission occur before VM creation and remain bounded
  across restart.
- Every create, boot, attestation, execution, stop, and delete phase has a
  deadline and persisted transition.
- Startup reconciliation recovers leases and owned VMs before accepting work.
- Graceful shutdown stops polling, drains active jobs to configured deadlines,
  and completes or records cleanup.

## Deliberate non-goals

The initial product does not implement a webhook listener, require a Gitea API
token, reimplement the Actions protocol, run jobs on the controller, support
arbitrary existing VMs, expose a hosted control plane, or support non-macOS
guests. Docker actions and service containers inside macOS guests are unsupported
until a separately specified guest-local container runtime is provided.

## Specification map

Feature specifications in `features/` govern upstream compatibility and
registration, Lume configuration and images, guest transport and execution,
worker lifecycle and ownership, service operation, and release maintenance.
Active implementation plans and proof gates live in `tasks/`.
