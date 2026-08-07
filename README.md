# Gitea Runner Lume

Gitea Runner Lume is a fork of the official Gitea Actions runner. It uses fresh
[Lume](https://cua.ai/docs/lume) macOS virtual machines instead of Docker
containers. Registration, polling, logs, artifacts, caches, and job results use
the normal Gitea runner protocol. No webhook or Gitea API token is required.

> This project is under active development. Use it only on a dedicated Apple
> Silicon controller until the remaining gates in `specs/tasks/` are complete.

## Requirements

- Apple Silicon Mac running macOS 14 or later
- [Go](https://go.dev/dl/) version specified by `go.mod`
- [Lume 0.5.1](https://cua.ai/docs/lume)
- Gitea 1.21 or later with Actions enabled
- macOS IPSW and enough disk/RAM for the configured VM

The physical Mac is a controller. Workflow code runs only inside disposable
VMs.

## Install

```sh
git clone https://github.com/markwylde/gitea-runner-lume.git
cd gitea-runner-lume
go build -o "$HOME/.local/bin/gitea-runner-lume" .
```

Ensure `$HOME/.local/bin` is in `PATH`, then install Lume:

```sh
/bin/bash -c "$(curl -fsSL https://cua.ai/lume/install.sh)"
lume --version
```

## Configure

```sh
CONFIG="$HOME/.config/gitea-runner-lume/config.yaml"
gitea-runner-lume config init --lume --config "$CONFIG"
```

Create the controller and image-signing keys:

```sh
install -d -m 700 "$HOME/.config/gitea-runner-lume/images"
ssh-keygen -q -t ed25519 -N '' -f "$HOME/.config/gitea-runner-lume/host.key"
ssh-keygen -q -t ed25519 -N '' -f "$HOME/.config/gitea-runner-lume/image-signing.key"
cp "$HOME/.config/gitea-runner-lume/image-signing.key.pub" \
  "$HOME/.config/gitea-runner-lume/image-signing.pub"
chmod 600 "$HOME/.config/gitea-runner-lume/host.key" \
  "$HOME/.config/gitea-runner-lume/image-signing.key"
```

## Create the base VM

```sh
gitea-runner-lume --config "$CONFIG" image create \
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
gitea-runner-lume --config "$CONFIG" image adopt \
  --profile xcode-16 \
  --signing-key-file "$HOME/.config/gitea-runner-lume/image-signing.key"
gitea-runner-lume --config "$CONFIG" image validate --profile xcode-16
```

## Register and run

Create a runner registration token in Gitea at instance, organization, or
repository scope:

```sh
printf '%s' 'REGISTRATION_TOKEN' > /tmp/gitea-runner-token
chmod 600 /tmp/gitea-runner-token

gitea-runner-lume --config "$CONFIG" register \
  --no-interactive \
  --instance https://gitea.example.com \
  --token-file /tmp/gitea-runner-token \
  --name "$(scutil --get LocalHostName)-lume" \
  --labels xcode-16:lume://xcode-16

gitea-runner-lume --config "$CONFIG" doctor
gitea-runner-lume --config "$CONFIG" service install
```

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

```sh
go test ./internal/...
go test -race ./internal/pkg/lume ./internal/pkg/guestagent ./internal/app/run
go vet ./...
```

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
