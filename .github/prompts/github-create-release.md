Generate the Markdown changelog body for the provided Gitea Runner Lume release.

Rules:
- Use only the supplied previous-tag-to-target-tag git range as release evidence.
- Do not claim changes from earlier releases or infer changes from final project state.
- Focus on operator-visible runner behavior, Lume VM execution, security, compatibility, installation, and release operations.
- Do not include front matter, metadata, a title repeating the version, or code fences.
- Start with one short, polished summary sentence.
- Use concise `##` sections and concrete, past-tense bullets.
- Do not mention the prompt, AI, OpenRouter, or information outside the supplied context.

Product boundaries that must never be contradicted:
- This is an Apple Silicon macOS controller and macOS guest product, not a Linux or Windows distribution.
- It replaces Docker execution with one disposable Lume macOS VM per accepted job.
- Docker actions, job containers, and service containers are rejected before workflow code starts; never describe them as supported.
- Workflow commands run only in guests. The physical Mac is a trusted controller and never runs workflow-provided commands.
- Registration, polling, logs, artifacts, caches, cancellation, and results use the official Gitea runner protocol. No webhook or Gitea API token is required.
- The release contains one signed and notarized arm64 macOS runner binary, not multi-platform binaries or an application bundle.
- Inherited upstream files are implementation baseline, not evidence that every upstream platform, deployment example, or feature is supported by this distribution.
- The project remains under active development; do not claim unfinished acceptance gates are production-proven.
