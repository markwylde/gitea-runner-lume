# Service and observability

## Outcome

The runner operates unattended as a macOS user service and exposes enough
bounded state to diagnose registration, polling, Lume, guest, and cleanup
failures without exposing workflow or secret data.

## Service

The foreground `daemon` is the primary process. A user LaunchAgent invokes the
same command and config, restarts on unexpected exit with bounded backoff, and
contains no registration credential or secret. Shutdown uses the upstream
poller drain path and downstream worker cleanup path. Reinstallation waits for
launchd to finish unloading the previous job before loading its replacement.

## Status and diagnostics

`status` reports registration identity, Gitea connectivity, online/draining
state, declared labels, capacity, active task IDs, worker phases, and cleanup
failures. `doctor` checks registration-file security, Gitea runner protocol
connectivity, Lume version/storage, base-image manifests, host resources, guest
identity, SSH transport, state permissions, and launchd configuration. The
launchd check parses the installed plist, rejects environment entries, verifies
owner-only permissions and the exact daemon executable/configuration arguments,
and confirms that the user job is loaded. It is read-only and never installs or
starts the service.

`doctor --live-guest-profile NAME` additionally creates an owned disposable
worker through the normal lifecycle, boots it, authenticates the pinned guest
identity and protocol, and then performs ownership-checked cleanup. This
mutating probe is opt-in because it can take several minutes, and it fails
before cloning unless the runner service is stopped so startup reconciliation
cannot race the diagnostic lease. It also holds the runner identity lock for
the probe, excluding a manually started foreground daemon.

Metrics and structured logs use bounded cardinality. They may include task ID,
profile, generated worker ID, phase, duration, and sanitized error class. They
never include workflow text, commands, repository/ref/actor data, task payloads,
headers, tokens, secrets, or guest stdout outside upstream job reporting.

## Acceptance outcomes

- The service appears online in Gitea and survives logout and host restart.
- Readiness becomes false during unreconciled cleanup or dependency failure.
- Diagnostic canaries never appear in logs, metrics, status, crash output, or
  launchd metadata.
- Operators can distinguish Gitea, admission, Lume, guest, job, and cleanup
  failures without enabling unsafe debug output.
