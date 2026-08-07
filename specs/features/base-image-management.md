# Base-image management

## Outcome

An operator can create or adopt a stopped macOS Lume VM as a reproducible,
validated source for workers without placing reusable service or project
secrets in the image.

## Image contract

A compatible base image contains:

- a supported macOS guest on Apple Silicon;
- a dedicated unprivileged runner account;
- Remote Login restricted to the controller bootstrap public key;
- a version-matched arm64 Gitea Runner Lume guest agent with recorded checksum;
- generic macOS command-line prerequisites declared by this project; and
- a static guest-agent entrypoint owned and writable only by an administrator.

It contains no Gitea token, runner registration, private SSH key, repository
checkout, signing identity, package-manager login, cloud credential, Actions
secret, or prior runner registration file.

Password-based remote login is disabled before the image is accepted. Default
credentials created by Lume's unattended setup are rotated or removed, and the
controller does not retain a reusable guest administrator password.

`image create` delegates macOS installation to supported Lume commands and
makes long-running progress visible. `image adopt` never modifies a VM before
inspection and confirmation. Both result in a stopped image plus a controller
manifest containing the image identity, expected guest fingerprint, macOS and
guest-agent revisions, validation time, and compatibility schema.

## Validation and update

Validation proves the VM exists in the configured Lume storage, is stopped,
matches the recorded identity, boots without a display, obtains an address,
accepts the expected SSH identity, exposes the expected architecture and
tools, has no runner registration, and can shut down cleanly. Validation never
runs repository code.

An image update operates on a clone and promotes it only after validation. The
prior image remains available until no worker references it and the operator
confirms retirement. Worker provisioning refuses a running, mutated, missing,
or incompatible base image.

## Acceptance outcomes

- Creating and validating a clean image produces a worker-ready stopped VM.
- Seeded secret and registration canaries cause validation to fail.
- A failed update leaves the current image usable.
- Adoption cannot claim or later delete the operator's original VM as a worker.
