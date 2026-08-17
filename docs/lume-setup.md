# Lume Runner Setup

This is the development setup path. It uses ordinary Gitea runner registration:
an instance URL, a runner registration token, a name, and labels. It does not
use an API token, webhook, repository, or organization setting.

## 1. Initialize the Controller

Install the reviewed Lume 0.5.1 release on an Apple Silicon Mac, then run:

```sh
gitea-runner-lume init
```

The command discovers the exact `lume --version`, generates a random
installation identity, creates one `xcode-16:lume://xcode-16` profile, and
creates separate controller SSH and image-signing identities. It preserves
valid existing configuration, keys, and runner registration when run again.
It prompts for the Gitea instance, registration token, and runner name; token
input is hidden. Edit the profile resources and paths before continuing. The
default Lume storage is named `home` at `$HOME/.lume`; pass `--storage` and
`--storage-path` when using a different registered storage, or `--no-register`
to defer registration.

Keep `image-signing.key` offline except while adopting an image. It is never
installed in a guest or service plist.

## 2. Create and Bootstrap the Base VM

Create a vanilla unattended VM:

```sh
gitea-runner-lume image create \
  --profile xcode-16 --ipsw latest --unattended tahoe
```

Bootstrap and harden it:

```sh
gitea-runner-lume image bootstrap --profile xcode-16
```

The command streams the exact controller binary into the VM, installs Apple
Command Line Tools and checksum-verified Node 24, generates the guest identity,
installs the controller public key, disables SSH password login, enables
console autologin for the workflow account, removes that account from
`admin`, exports only public guest identities,
and shuts down cleanly. It then boots once more to prove that key-only access
and the hardened state persisted. Rerunning the command is safe.

macOS requires an administrator account, so bootstrap creates a hidden
`grl-maintenance` administrator with a random password that is discarded. SSH
is restricted to the non-administrator `lume` user; the controller retains no
administrator credential.

Lume's documented unattended password is used only during initial bootstrap.
For a custom unattended password, place it in an owner-only file and pass
`--password-file PATH`. Do not use `--password` in automation because process
arguments are observable.

Bootstrap verifies the registered Gitea hostname inside the guest. When Lume
NAT does not inherit host-only split DNS, it installs a validated mapping for
that hostname automatically.

## 3. Attest and Adopt

```sh
gitea-runner-lume image adopt --profile xcode-16 \
  --signing-key-file "$HOME/.config/gitea-runner-lume/image-signing.key"

gitea-runner-lume image validate --profile xcode-16
```

Both commands boot the image and require pinned SSH plus mutual protocol
authentication. Adoption writes a signed manifest only after that check. The
manifest records the attested macOS version, arm64 architecture, non-root guest
UID, exact agent revision, and SHA-256 of the running guest binary; validation
and worker startup require the same values.

## 4. Register Normally

This step is already complete unless `init --no-register` was used. To register
later, create an instance, organization, or repository registration token in
Gitea, then use the same flow as the official runner:

```sh
printf '%s' 'REGISTRATION_TOKEN' > /tmp/gitea-runner-token
chmod 600 /tmp/gitea-runner-token

gitea-runner-lume register \
  --no-interactive \
  --instance https://gitea.example.com \
  --token-file /tmp/gitea-runner-token \
  --name "$(scutil --get LocalHostName)-lume" \
  --labels xcode-16:lume://xcode-16
```

The runner should appear immediately in the Gitea scope selected by that
registration token. After image validation, run `service install`, then
`doctor`.
