# Gitea Runner Lume specifications

This folder is the product source of truth. Delivery follows a simple loop:
specify the behaviour, plan an implementation slice, implement and verify it,
then preserve the completed plan as history.

## Map

- [CORE.md](./CORE.md) — purpose, supported product, boundaries, and feature
  map.
- [features/](./features/) — canonical behaviour and acceptance contracts.
- [tasks/](./tasks/) — active implementation plans and checklists.
- [tasks_completed/](./tasks_completed/) — completed implementation history.

## Working agreement

Update the governing feature specification before changing product behaviour.
Feature documents describe the required product in present tense and remain
independent of delivery status. Gaps, sequencing, and checklists belong in
`tasks/`.

## Feature catalogue

| Area | Canonical specification |
| --- | --- |
| Installation and CLI | [installation-and-cli](./features/installation-and-cli.md) |
| Configuration and secrets | [configuration-and-secrets](./features/configuration-and-secrets.md) |
| Gitea compatibility | [gitea-runner-compatibility](./features/gitea-runner-compatibility.md) |
| Base images | [base-image-management](./features/base-image-management.md) |
| Worker VMs | [worker-vm-lifecycle](./features/worker-vm-lifecycle.md) |
| Guest execution | [guest-agent-and-execution](./features/guest-agent-and-execution.md) |
| Scheduling | [capacity-leases-and-concurrency](./features/capacity-leases-and-concurrency.md) |
| Recovery and cleanup | [recovery-and-safe-cleanup](./features/recovery-and-safe-cleanup.md) |
| Service operation | [service-and-observability](./features/service-and-observability.md) |
| Distribution and compatibility | [distribution-and-upgrades](./features/distribution-and-upgrades.md) |
