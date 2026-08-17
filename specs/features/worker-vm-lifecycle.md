# Worker VM lifecycle

## Outcome

Every accepted unit of capacity runs in a fresh, bounded macOS VM and every
terminal path removes that VM without affecting unrelated Lume resources.

## Provisioning

The controller generates a worker identifier from its installation identity
and a cryptographically random lease identifier. It records the lease before
calling Lume, then clones only the validated stopped base image into the
configured storage. CPU, memory, disk, and display settings remain within
configured and host-validated limits.

The controller never runs more than two macOS guests concurrently because that
is Lume's documented platform ceiling. A lower configured or resource-derived
limit takes precedence.

Workers run without a host-visible display. Host directory sharing, USB
devices, arbitrary disks, clipboard integration, and interactive display
launch are disabled. Inside the guest the workflow account is
console-logged-in so Aqua exists; the controller never attaches that display.
The guest uses ordinary virtual networking and must reach Gitea and approved
dependency destinations according to the operator's network policy; the
controller does not silently bypass that policy.

Readiness requires Lume state, a discovered guest address, a matching SSH host
identity, and a successful authenticated bootstrap probe. Each phase has a
deadline. Guest addresses and command output are parsed as untrusted data.

## Completion and teardown

The controller begins teardown after guest-agent completion, cancellation,
provisioning failure, readiness timeout, execution timeout, explicit service
drain expiry, or recovery of an abandoned worker. It requests a graceful stop,
uses forced stop only after a deadline, verifies the VM is stopped, deletes it
with the supported Lume interface, and verifies absence.

Worker deletion requires matching durable ownership evidence: controller
installation identifier, lease record, exact storage, and an unpredictable VM
identity. A name prefix is only a search aid. Unowned or ambiguous resources
are quarantined for operator review rather than deleted.

## Acceptance outcomes

- Concurrent provisioning never exceeds configured CPU, memory, disk, or VM
  limits.
- No workflow can see host mounts, host Keychain items, or another worker disk.
- Failure injected after every lifecycle transition ends in verified deletion
  or a visible, retrying cleanup record.
- Unrelated and adversarially named Lume VMs survive all cleanup operations.
