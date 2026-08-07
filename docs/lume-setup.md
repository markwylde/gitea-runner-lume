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
valid existing configuration and keys when run again. Edit the profile
resources and paths before continuing. The default Lume storage is named
`home` at `$HOME/.lume`; pass `--storage` and `--storage-path` when using a
different registered storage.

Keep `image-signing.key` offline except while adopting an image. It is never
installed in a guest or service plist.

## 2. Create and Bootstrap the Base VM

Create a vanilla unattended VM:

```sh
gitea-runner-lume image create \
  --profile xcode-16 --ipsw latest --unattended tahoe
```

Before adoption, install the same `gitea-runner-lume` binary at
`/usr/local/bin/gitea-runner-lume` in the VM. Create
`/etc/gitea-runner-lume`, owned by the unprivileged `lume` account with mode
`0700`, and install:

- `host.key.pub` as `/etc/gitea-runner-lume/host.pub`;
- a newly generated guest-only Ed25519 private key as
  `/etc/gitea-runner-lume/guest.key`, mode `0600`; and
- `host.key.pub` in the `lume` account's SSH `authorized_keys`.

Install Apple Command Line Tools and a supported arm64 Node runtime before
adoption. The current tested image uses Command Line Tools 26.6, Apple Git
2.50.1, and Node 24.19.0. Verify downloaded Node archives against the release's
published `SHASUMS256.txt`; do not install an unverified archive.

Finish macOS Setup Assistant before hardening the account. Remote Login on
macOS can be restricted to administrators by default, so add `lume` to the
dedicated `com.apple.access_ssh` group and prove key-only SSH works before
removing it from `admin`:

```sh
sudo dseditgroup -o edit -a lume -t user com.apple.access_ssh
sudo dseditgroup -o edit -d lume -t user admin
```

After the removal, verify a fresh controller SSH connection still succeeds and
that `id` does not report administrator membership. Do not remove administrator
membership first: doing so can lock the operator out before the SSH allow group
is configured.

Lume's Tahoe unattended account can be tokenless, causing both `sysadminctl`
and `dscl -passwd` to fail password updates. In that case, remove its
`ShadowHashData` and `AuthenticationAuthority` records in the same root
hardening transaction, prove `dscl . -authonly lume lume` fails, and only then
drop administrator membership. Password SSH must also be disabled independently.

Export only the guest public key to the controller path configured as
`guest_public_key_file`. Disable SSH password and keyboard-interactive
authentication, remove the default password and autologin configuration, and
stop the VM. Do not put the runner registration file, image-signing private key,
Gitea tokens, project files, or workflow data in the image.

Record the VM's SSH host public key under the configured image name, not its
temporary DHCP address, in `known_hosts`. For the default profile the entry
starts with `grl-xcode-16`. Verify this key through the local VM console before
trusting it; `ssh-keyscan` alone is not authentication.

Private DNS used by Gitea must also resolve inside a Lume NAT guest. Tailscale
split DNS is not inherited automatically on the tested host. When required,
install a scoped `/etc/resolver/DOMAIN` entry using Tailscale's
`100.100.100.100` resolver during root image bootstrap. Prefer a scoped resolver
over pinning a Gitea machine's changing Tailscale IP.

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

Create an instance, organization, or repository registration token in Gitea,
then use the same flow as the official runner:

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
registration token. Run `doctor`, then `daemon` or `service install`.
