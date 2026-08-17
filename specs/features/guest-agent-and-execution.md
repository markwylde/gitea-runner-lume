# Guest agent and job execution

## Outcome

The upstream Actions engine orchestrates each accepted job from the controller
through a fresh macOS guest execution environment. Every workflow-created
process runs in that guest; workflow code and secrets never enter the physical
Mac's process environment.

## Agent contract

The base image contains a version-matched `gitea-runner-lume guest` executable
running as an unprivileged account. It exposes no listening network service at
rest. The host starts one session through pinned SSH transport and launches the
agent with a static command and a random nonce.

The session uses a versioned, length-delimited protocol over standard input and
output. Mutual identity, protocol version, task identity, message order, size,
and total byte limits are verified before task material is accepted. Messages
carry structured file transfer, command execution, output, cancellation, and
shutdown operations. There is no arbitrary host-command message.

## Execution

The official upstream workflow engine remains authoritative on the controller,
as it is when orchestrating Docker. Its execution-environment interface maps
file and process operations to the authenticated guest agent instead of a
container. The guest creates a fresh workspace and process group for the task.
The host still starts the agent over pinned SSH; that transport is not the
job's GUI session. On macOS the agent waits until the guest account has a
console Aqua session (`gui/$UID`) and then starts every workflow process in
that domain, so GUI programs such as Electron can open windows. Aqua control
files (plist, logs, exit status) live beside the job workspace, not inside it,
because `actions/checkout` deletes workdir contents. SSH remains
key-only; the console session exists so job processes are not children of
`sshd`.
On macOS the workspace uses canonical `/private/var/tmp` paths so Git
credential `includeIf` rules cannot diverge through the `/var/tmp` symlink.
Structured process environments accept bounded action-input names containing
hyphens, while rejecting empty names, `=`, NUL, and oversized names or values.
Guest `PATH` is an image contract, not a copy of the controller process
environment. Every guest process receives the root-owned macOS tool directories
provisioned by the base image (`/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin`)
and prepends workflow `PATH` or `$GITHUB_PATH` extras so JavaScript actions can
resolve the verified Node runtime. Entries under the controller home directory
are dropped. The controller does not copy `os.Environ()` into a Lume guest.
Guest processes never inherit the controller's `HOME`, `USER`, `LOGNAME`,
`TMPDIR`, or `SSH_AUTH_SOCK`. The controller sets `HOME`, `USER`, and
`LOGNAME` to the VM account (`/Users/lume`) on each exec so the guest agent
in the current image, which replaces the process environment with the request,
still presents a writable home. Tools such as npm then write under
`/Users/lume`, not the physical Mac user's home.
Shell, JavaScript, and composite actions available on arm64 macOS retain the
upstream expression, environment-file, masking, cache, artifact, and reporting
contracts.

The host reporter remains connected to Gitea and translates bounded guest
events into upstream reporter calls. Cancellation closes admission, signals the
guest job process tree, permits bounded post-step cleanup, and then destroys
the VM. Lost transport, malformed events, agent crash, or report failure fails
the task and begins cleanup.

Docker actions, job containers, service containers, interactive desktop access,
host mounts, and workflow-requested devices are rejected before execution.

## Secret boundary

The complete accepted task contains Gitea task credentials and Actions secrets.
It exists on the host only in upstream's bounded in-memory objects. Values
needed by guest processes cross only the authenticated encrypted SSH stream;
the transport redacts and never logs message bodies.
The guest erases its session envelope and workspace before shutdown where
possible; VM deletion is the final confidentiality boundary.

## Acceptance outcomes

- Process tracing proves every workflow step and action process runs in the VM.
- On macOS, workflow processes run in the guest account's Aqua session, not as
  children of the SSH agent.
- A canary scan finds task secrets only in expected guest process memory/files
  during the job and nowhere in retained host state.
- Logs, masks, outputs, cache, artifacts, step failure, timeout, and cancellation
  match upstream runner behaviour.
- A private Gitea hostname resolvable through controller-only split DNS remains
  reachable from disposable guests created from the bootstrapped image.
- Malformed, replayed, reordered, oversized, or unauthenticated agent messages
  cannot execute code or forge task completion.
- No worker can execute a second task.
