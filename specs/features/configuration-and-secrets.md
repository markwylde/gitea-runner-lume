# Configuration and secrets

## Outcome

Runner and Lume configuration is explicit and reviewable while registration,
task, and guest-authentication secrets remain outside logs and observable
process arguments.

## Configuration model

The upstream runner configuration remains authoritative for logging, polling,
capacity, task timeouts, cache, metrics, and registration-file location. The
downstream `lume` section records:

- the absolute Lume executable and exact storage identity;
- an installation identifier and owner-only state directory;
- one or more named profiles mapping labels to validated base-image manifests;
- per-profile CPU, memory, disk, boot, execution, shutdown, and cleanup bounds;
- global VM concurrency, never exceeding the supported VM limit;
- guest-agent executable version and pinned public identity; and
- retention and reconciliation bounds.

Profiles and generated resource names use a strict alphabet. Workflow data
cannot override any profile field. Unknown keys, duplicate labels, host labels,
Docker labels, unvalidated images, relative privileged paths, and resource
limits outside proven bounds fail closed.

## Secret model

The ordinary upstream `.runner` file contains the registered runner credential
and is owner-only. The Gitea registration token is used only by upstream
registration and is not retained afterward.

An owner-only host file stores the SSH and guest-session private identity; its
path never appears in guest-visible data. Base images contain only its public
counterpart and their own pinned guest identity. Each worker session derives a
unique nonce. Gitea task tokens and Actions secrets pass only through the
authenticated in-memory guest stream after attestation; they are not stored in
controller state.

There is no Gitea API token or webhook secret in the product.

## Acceptance outcomes

- Secret canaries do not appear in configuration, state, arguments, environment,
  plist, diagnostics, or retained host files.
- Registration and state files reject insecure permissions and symlinks.
- Unknown, corrupt, downgraded, or partially written state blocks polling.
- Inaccessible host identity and guest identity mismatch fail before task delivery.
