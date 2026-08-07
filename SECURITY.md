# Security

Do not use this development build for untrusted workflows until every item in
`specs/tasks/08-acceptance-security-release.md` is complete.

Report vulnerabilities privately to the repository owner. Do not include
registration tokens, Actions secrets, workflow payloads, VM disks, private
keys, or unredacted runner logs in an issue.

The controller is not an execution target. Workflow processes and files belong
inside disposable Lume guests. Cleanup may stop or delete a VM only when its
durable installation, lease, storage, generation, nonce, and provider identity
all match. Ambiguous state is quarantined for operator review.
