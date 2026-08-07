# Active implementation plan

This plan builds Gitea Runner Lume as a maintained downstream of the official
Gitea Runner. Checklist marks represent only the current Go implementation;
the abandoned Swift/webhook prototype contributes no completed items.

| Order | Task | Outcome |
| --- | --- | --- |
| 00 | [Upstream baseline and contract proofs](./00-upstream-and-contract-proofs.md) | Reproducible fork baseline and proven external contracts. |
| 01 | [Lume provider and ownership](./01-lume-provider-and-ownership.md) | Safe structured VM lifecycle boundary. |
| 03 | [Base images and guest identity](./03-base-images-and-guest-identity.md) | Reproducible trusted macOS image. |
| 04 | [Guest protocol and agent](./04-guest-protocol-and-agent.md) | Authenticated one-job remote execution channel. |
| 05 | [Upstream execution integration](./05-upstream-execution-integration.md) | Official runner jobs execute in the guest. |
| 06 | [Lifecycle, recovery, and cleanup](./06-lifecycle-recovery-cleanup.md) | Durable bounded workers and safe restart. |
| 07 | [Service and diagnostics](./07-service-and-diagnostics.md) | Unattended macOS operation. |
| 08 | [Acceptance, security, and release](./08-acceptance-security-release.md) | Real Gitea proof and reproducible release. |

Completed delivery plans are retained in [`../tasks_completed/`](../tasks_completed/).

Dependencies are gates. A dependent task may be prototyped for discovery, but
its checklist is not ticked while an unresolved contract could invalidate it.

The product is complete only when a clean Mac can register it using the normal
runner flow, see it online in Gitea, execute a native macOS job entirely in a
fresh Lume VM, observe normal logs/results, and verify deletion afterward.
