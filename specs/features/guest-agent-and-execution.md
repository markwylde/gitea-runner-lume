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
On macOS the workspace uses canonical `/private/var/tmp` paths so Git
credential `includeIf` rules cannot diverge through the `/var/tmp` symlink.
Structured process environments accept bounded action-input names containing
hyphens, while rejecting empty names, `=`, NUL, and oversized names or values.
Every guest process retains workflow-provided `PATH` entries and also receives
the root-owned macOS tool directories provisioned by the base image, including
`/usr/local/bin`, so JavaScript actions can resolve the verified Node runtime
when the controller daemon was started with launchd's restricted environment.
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
- A canary scan finds task secrets only in expected guest process memory/files
  during the job and nowhere in retained host state.
- Logs, masks, outputs, cache, artifacts, step failure, timeout, and cancellation
  match upstream runner behaviour.
- A private Gitea hostname resolvable through controller-only split DNS remains
  reachable from disposable guests created from the bootstrapped image.
- Malformed, replayed, reordered, oversized, or unauthenticated agent messages
  cannot execute code or forge task completion.
- No worker can execute a second task.
