# Gitea Runner Lume

Gitea Runner Lume is a fork of the official Gitea Actions runner. It uses fresh
[Lume](https://cua.ai/docs/lume) macOS virtual machines instead of Docker
containers. Registration, polling, logs, artifacts, caches, and job results use
the normal Gitea runner protocol. No webhook or Gitea API token is required.

> This project is under active development. Use it only on a dedicated Apple
> Silicon controller until the remaining gates in `specs/tasks/` are complete.

## Requirements

- Apple Silicon Mac running macOS 14 or later
- [Lume 0.5.1](https://cua.ai/docs/lume)
- Gitea 1.21 or later with Actions enabled
- macOS IPSW and enough disk/RAM for the configured VM

The physical Mac is a controller. Workflow code runs only inside disposable
VMs.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/markwylde/gitea-runner-lume/main/install.sh | bash
```

The installer downloads the latest Apple Silicon release, verifies its
published checksum, and installs it to `~/.local/bin`. Run the same command to
upgrade. See [install.sh](install.sh) for version pinning and custom locations.

Ensure `$HOME/.local/bin` is in `PATH`, then install Lume:

```sh
/bin/bash -c "$(curl -fsSL https://cua.ai/lume/install.sh)"
lume --version
```

## Configure

```sh
gitea-runner-lume init
```

This creates the default configuration, controller keys, and image-signing
keys under `~/.config/gitea-runner-lume`, then prompts for the Gitea instance,
runner registration token, and runner name. Pass `--config PATH` to use another
configuration file or `--no-register` to initialize local files only.

## Create the base VM

```sh
gitea-runner-lume image create \
  --profile xcode-16 --ipsw latest --unattended tahoe
```

Inside the VM:

1. Create a non-admin `lume` user with key-only SSH access.
2. Install the same runner binary at `/usr/local/bin/gitea-runner-lume`.
3. Install Apple Command Line Tools and an arm64 Node runtime.
4. Install the controller public key and a separate guest signing key as
   described in [docs/lume-setup.md](docs/lume-setup.md).
5. Disable password SSH, autologin, and the account password.
6. Stop the VM and pin its SSH host key in the configured `known_hosts` file.

Adopt and validate the image:

```sh
gitea-runner-lume image adopt \
  --profile xcode-16 \
  --signing-key-file "$HOME/.config/gitea-runner-lume/image-signing.key"
gitea-runner-lume image validate --profile xcode-16
```

## Start the runner

After image adoption and validation succeed, verify the complete installation
and start the runner service:

```sh
gitea-runner-lume doctor
gitea-runner-lume service install
```

The runner remains offline in Gitea until the service starts. If `init` was run
with `--no-register`, run `gitea-runner-lume register` before these commands.

Use the label in a Gitea workflow:

```yaml
jobs:
  test:
    runs-on: xcode-16
    steps:
      - uses: actions/checkout@v4
      - run: swift test
```

Each job clones the trusted base image, runs in the disposable VM, reports the
result to Gitea, and deletes the worker VM.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md) for source builds and tests.

## Releases

Run the GitHub Actions `Trigger Release` workflow from `main` to build a signed
and notarized Apple Silicon archive, checksum, SBOM, and GitHub Release. The
workflow derives the next semantic version from conventional commits and
creates the tag after its release checks pass; directly pushing a tag does not
publish a release.

```sh
gh workflow run release.yml \
  --repo markwylde/gitea-runner-lume \
  --ref main
```

The repository must define `APPLE_ID` and `APPLE_TEAM_ID` variables plus
`MACOS_CERTIFICATE_P12`, `MACOS_CERTIFICATE_PASSWORD`, and
`APPLE_APP_SPECIFIC_PASSWORD` secrets. The workflow fails before publication
when any credential, signature, notarization, or artifact check fails.

Set the optional `OPENROUTER_API_KEY` secret for AI-generated release notes.
Without it, releases use GitHub-generated notes.

See [SECURITY.md](SECURITY.md), [UPSTREAM.md](UPSTREAM.md), and
[docs/lume-setup.md](docs/lume-setup.md) for the security model, fork baseline,
and full image-hardening procedure.

## License

MIT. See [LICENSE](LICENSE).
