# Lume Contract

The provider adapter is implemented against the official Lume 0.5.1 CLI and
the source revision below.

- Source: `https://github.com/trycua/cua`
- Source tag: `lume-v0.5.1`
- Installed/contract review date: 2026-08-07
- Reference: `libs/lume/src/Commands` and `libs/lume/src/VM/VMDetails.swift`

The adapter uses `--format json` for `ls` and `get`, and strictly decodes the
documented `VMDetails` schema. Lifecycle calls use `--source-storage` and
`--dest-storage` for clone, `--no-display` for run, and explicit `--storage`
for every operation that supports it. Delete always uses `--force` after local
ownership checks; workflow data never controls a Lume argument.

Configuration lists accepted exact `lume --version` output in
`lume.supported_versions`. Startup fails before runner declaration when the
installed output is not listed. A new Lume revision must be reviewed against
these command and JSON contracts, tested on a disposable storage location, and
added explicitly.

The 0.5.1 `VMDetails` Codable schema and command flags were compared against
the adapter. Real non-empty create, get, list, clone, set, headless run, stop,
delete, and verified-absence paths passed on 2026-08-07. Two observed 0.5.1
constraints are part of the adapter contract:

- `set --disk-size` rejects a value equal to the inherited clone size as a
  shrink, so post-clone configuration changes CPU and memory only; and
- failed unattended finalization can remove the partially created VM while
  leaving a `.NAME.resize.guard`; an operator must prove the VM is absent
  before removing that exact operation-owned guard and retrying.
