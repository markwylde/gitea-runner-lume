# Installation and command-line interface

## Outcome

An operator familiar with the official runner can install, register, configure,
and run Gitea Runner Lume without learning a separate Gitea integration model.

## Command model

The executable is `gitea-runner-lume`. It retains upstream `register`, `daemon`,
`exec`, `config`, `cache-server`, and version/help behaviour where applicable.
It adds `init`, `image create`, `image adopt`, `image validate`, `doctor`,
`status`, `cleanup`, and macOS `service` commands.

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
When `--config` is omitted, every command uses
`$HOME/.config/gitea-runner-lume/config.yaml`; `config init` creates that file
and its owner-only parent directories. An explicit `--config` always overrides
the default. The generated Lume configuration stores the owner-only runner
registration beside the default configuration rather than in the invoking
working directory.

Top-level `init` is the primary setup command. It creates the Lume-backed
configuration when absent, secure controller and image-signing Ed25519 key
pairs, and the image-manifest directory. Repeated execution preserves valid
configuration and keys. Partial, unsafe, or mismatched key pairs fail closed;
the command never creates the guest-only private key on the controller.
Unless `--no-register` is passed, `init` also performs ordinary Gitea runner
registration. Missing instance, token, and runner-name values are prompted for;
token entry is not echoed on an interactive terminal. Flags and an owner-only
token file support unattended setup. An existing valid registration is kept.

`image create` reports elapsed time while Lume is working. When Lume publishes
structured provisioning operation or download percentage fields, the command
reports those values. Provider output is validated and bounded before display.

## Installation

Releases provide a notarized arm64 macOS archive containing one runner binary,
license notices for upstream and downstream code, shell completions, and
checksums. Lume is the only host runtime dependency.

Operator documentation installs a versioned release archive and verifies its
published SHA-256 checksum before extracting or executing the binary. Cloning
the repository and building with Go is a contributor workflow, not the primary
operator installation path.

The primary operator path is a rerunnable shell installer. It supports only
Apple Silicon macOS, installs without privilege escalation into an explicit or
per-user binary directory, resolves either the latest release or an explicitly
pinned version, and verifies the archive against the release checksum before
extraction. It does not modify shell profiles.

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
