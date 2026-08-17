# Base images and guest identity

Depends on tasks 01 and 02.

## Checklist

- [x] Define a signed/versioned image manifest binding exact VM identity,
  storage, macOS, architecture, runner revision, agent revision, SSH identity,
  generation, and validation time.
- [x] Bind the authenticated guest's macOS version, non-root UID, architecture,
  and running agent-binary SHA-256 into the signed manifest and every session.
- [x] Implement `image create`, `adopt`, and `validate`.
- [x] Implement a supported, idempotent `image bootstrap` command that performs
  every mandatory guest setup and controller public-key export step.
- [ ] Implement and validate a non-destructive update-on-clone workflow against
  a Lume version with a proven atomic image replacement contract.
- [x] Provision an unprivileged guest account, static agent entrypoint, pinned
  SSH host key, controller public key, and generic Actions prerequisites.
- [x] Disable SSH password login, default credentials, shared folders, clipboard,
  host-visible display, devices, and stale runner registration. Enable console
  autologin for the unprivileged workflow account.
- [ ] Scan for registration, API, webhook, project, signing, and canary secrets.
- [x] Validate boot, address, host identity, attestation, tools, clean workspace,
  shutdown, mutation evidence, and stopped state.
- [ ] Test failed adoption/update, tampered manifests, changed host keys, secret
  canaries, stale state, and preservation of operator-owned source VMs.

## Done

A validated stopped image can create a trusted worker and contains no reusable
Gitea, host, or project credential.
