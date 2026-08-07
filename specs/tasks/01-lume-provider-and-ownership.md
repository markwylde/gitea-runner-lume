# Lume provider and ownership

Depends on task 00 contract fixtures.

## Checklist

- [x] Add a structured, cancellable Lume provider with argument arrays and no
  shell interpolation.
- [x] Parse only proven JSON/version output with strict size and schema bounds.
- [x] Validate executable path, storage identity, VM names, profile resources,
  and all untrusted provider output.
- [x] Implement inspect, clone, configure, headless start, IP discovery, stop,
  forced stop, delete, and verified absence with independent deadlines.
- [x] Define cryptographically unpredictable worker identity and durable
  ownership evidence that survives restart.
- [x] Require installation ID, lease ID, storage, image generation, and observed
  provider metadata before stop/delete; quarantine ambiguity.
- [x] Add adversarial tests for output, names, symlinks, storage mismatch,
  command injection, timeout, and unrelated VMs.
- [ ] Exercise real Lume partial side effects and interruption at every command.

## Done

Every VM operation is bounded and no cleanup path can affect a VM whose
ownership is not positively proven.
