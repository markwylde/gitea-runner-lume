# Configuration, registration, and CLI

Depends on tasks 00 and 01.

## Checklist

- [x] Preserve upstream registration and daemon flags, prompts, environment
  behaviour, registration file, protocol client, and config compatibility.
- [x] Brand the binary `gitea-runner-lume` without changing Gitea protocol
  identity semantics.
- [x] Add strict `lume` configuration for executable, storage, state, profiles,
  images, resources, concurrency, deadlines, and guest identity.
- [x] Parse `label:lume://PROFILE`; reject duplicate, unknown, Docker, and host
  execution labels in secure default mode.
- [x] Make `register` validate Lume labels and then call the upstream registration
  path; support interactive token entry and `--token-file`.
- [x] Add migration/error handling that identifies and rejects the abandoned
  webhook-controller JSON configuration.
- [x] Add `doctor`, `status`, `image`, `cleanup`, and `service` command surfaces
  without shadowing upstream commands.
- [x] Test token secrecy, owner-only and symlink-safe registration persistence,
  corrupt configuration, unknown keys, and service/config idempotency.
- [x] Test complete help and JSON/human error snapshots plus runner visibility
  against real Gitea.

## Done

The official registration workflow creates the expected visible runner and no
operator-facing path mentions webhooks, API tokens, or repository hard-coding.
