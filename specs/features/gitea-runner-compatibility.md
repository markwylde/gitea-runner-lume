# Gitea Runner compatibility

## Outcome

An operator registers and operates Gitea Runner Lume exactly as an official
Gitea Runner, and Gitea displays and schedules it as an ordinary runner.

## Upstream baseline

Each release records one immutable upstream Gitea Runner commit and version.
Upstream registration, configuration loading, runner declaration, task polling,
heartbeats, cancellation, workflow parsing, expressions, action resolution,
secret masking, cache, artifacts, logs, and result reporting are retained.

Downstream changes are isolated behind explicit interfaces and carry tests that
run both the upstream backend suite and Lume-specific behaviour. Updating the
baseline requires a reviewed upstream diff, successful compatibility suite, and
real Gitea registration and job execution proof.

## Registration and labels

`register` uses the upstream command and protocol. Interactive prompts and the
upstream flags `--instance`, `--token-file`, `--name`, `--labels`, and
`--no-interactive` retain their meaning. The Gitea-issued token determines
whether the runner is instance-, organization-, or repository-scoped.

Lume labels use `label:lume://PROFILE`. Registration validates that every
profile exists in configuration. Workflow labels select only among declared
profiles; workflow content cannot choose a base image, storage, executable,
resource size, SSH identity, or Lume option.

The daemon declares its configured labels and capacity through the upstream
protocol, polls through the upstream client, and appears online in Gitea while
it can safely accept work. Local health or cleanup failure makes it unavailable
through the existing runner health/admission mechanism.

## Task execution boundary

The upstream poller and reporter remain on the host. For a Lume label, the
upstream workflow execution call is delegated to the authenticated guest agent.
Host and Docker execution are never selected as fallback for that task.

Unsupported workflow features fail with a clear job error. The initial support
set includes shell steps, JavaScript actions supported on arm64 macOS, composite
actions, checkout, environment files, outputs, masks, cache, artifacts,
timeouts, and cancellation. Docker actions, job containers, and service
containers are rejected before workflow code starts.

## Acceptance outcomes

- Registration creates one runner in the correct Gitea scope without any
  webhook or API token.
- Starting `daemon` changes that runner to online/idle and makes its exact Lume
  labels available to workflows.
- An upstream protocol fixture produces byte-equivalent declarations and task
  reports apart from the product name/version capability.
- A compatible job reports normal live logs and results while all step process
  identifiers belong to the guest.
- A Docker/service-container workflow fails before any step executes.
