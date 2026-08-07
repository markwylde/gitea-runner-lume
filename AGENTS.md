# AGENTS — Gitea Runner Lume

Gitea Runner Lume is a security-sensitive macOS infrastructure project.
Product behaviour is specified in `specs/`; read the governing feature before
changing code.

- Work feature-first: update `specs/features/` when behaviour, constraints, or
  acceptance expectations change, then implement and test it.
- The physical Mac is a controller only. Never execute workflow-provided code,
  mount host user directories, or expose host credentials to worker VMs.
- Treat Gitea payloads, labels, repository data, guest output, and Lume output
  as untrusted input.
- Only delete a VM after proving it is managed by this installation. A name
  prefix alone is not proof of ownership.
- Keep feature specifications free of implementation progress. Active work
  belongs in `specs/tasks/`; completed plans move to `specs/tasks_completed/`.

