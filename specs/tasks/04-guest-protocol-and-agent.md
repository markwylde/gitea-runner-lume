# Guest protocol and agent

Depends on tasks 00 and 03.

## Checklist

- [x] Define a versioned length-delimited protocol for hello/attestation, task,
  event, cancellation, completion, error, and shutdown.
- [x] Authenticate both peers and bind installation, worker, lease, task, nonce,
  image generation, and executable revision into the session.
- [x] Enforce message order, replay rejection, per-message/total limits, timeouts,
  and strict decoding before side effects.
- [x] Start the guest through pinned SSH with a static argument vector and stream
  protocol bytes over standard input/output.
- [x] Build the guest agent as a one-task upstream execution environment with a
  fresh unprivileged workspace/process group.
- [x] Stream command output back through upstream masking, annotation, output,
  state, and completion paths while preserving ordering and backpressure.
- [x] Implement cancellation, timeout, process-tree termination, post steps, and
  one-task-only agent exit.
- [x] Test MITM identity substitution, hostile paths and archives, malformed
  framing, replay, reordering, truncation, disconnect, and process-tree exit.
- [ ] Test sustained backpressure, guest crash, and secret canaries over a real
  SSH/Lume transport.

## Done

One authenticated worker can execute and report one task; protocol input cannot
cause host execution or expose task material outside the guest stream.
