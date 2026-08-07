# Installation and command-line interface

## Outcome

An operator familiar with the official runner can install, register, configure,
and run Gitea Runner Lume without learning a separate Gitea integration model.

## Command model

The executable is `gitea-runner-lume`. It retains upstream `register`, `daemon`,
`exec`, `config`, `cache-server`, and version/help behaviour where applicable.
It adds `image create`, `image adopt`, `image validate`, `doctor`, `status`,
`cleanup`, and macOS `service` commands.

The complete command tree is constructible without executing it so help,
branding, public flags, and bounded argument errors can be regression tested.

`register` is the upstream registration flow. It asks for a Gitea instance URL,
runner registration token, runner name, and labels. Non-interactive automation
uses upstream-compatible flags and supports `--token-file`; documentation
prefers the file form because process arguments are observable. Successful
registration writes the ordinary owner-only runner registration file.

No command asks for a Gitea API token, webhook URL, webhook secret, repository,
or organization. Scope is a property of the registration token.

`config init` starts from the upstream configuration format and adds a `lume`
section. Unknown Lume keys and unsafe combinations fail closed. Existing
upstream configurations remain readable when they do not enable Lume labels.

## Installation

Releases provide a notarized arm64 macOS archive containing one runner binary,
license notices for upstream and downstream code, shell completions, and
checksums. Lume is the only host runtime dependency.

Service installation creates a user-owned LaunchAgent and does not place
registration secrets in the plist. `doctor` validates architecture, macOS,
Lume, storage, base images, registration file permissions, Gitea connectivity,
guest identity, and cleanup recovery before the service is enabled.

## Acceptance outcomes

- Official runner registration instructions work after substituting the binary
  name and choosing a configured `lume://` label.
- Registration immediately appears in the expected Gitea runner page.
- Repeated setup and service commands are idempotent.
- Help and errors contain no obsolete webhook or API-token instructions.
- Uninstalling the service preserves registration, configuration, and images.
