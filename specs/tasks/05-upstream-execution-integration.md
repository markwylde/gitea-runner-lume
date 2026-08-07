# Upstream execution integration

Depends on tasks 02 and 04.

## Checklist

- [x] Introduce a narrow upstream job-environment interface and retain upstream
  host/Docker implementations for upstream tests only.
- [x] Route every `lume://` task through the Lume environment with no fallback.
- [x] Keep upstream poller, declaration, reporter, masks, cache, artifacts,
  cancellation, and final-result code authoritative on the host.
- [x] Keep workflow orchestration in bounded upstream memory and move every
  workflow-created process and required file into the guest without logging or
  persisting the task body.
- [x] Support shell steps, checkout, JavaScript actions, composite actions,
  environment files, masks, outputs, cache, artifacts, post steps, and timeouts.
- [x] Reject Docker actions, job containers, and services before any workflow
  process starts.
- [x] Prove all workflow process IDs and files belong to the worker VM.
- [ ] Run upstream conformance tests against the remote Lume environment and
  compare reports with the standard runner.

## Done

A Gitea task is acquired and reported by the official runner path while its
complete supported Actions execution occurs only inside the disposable guest.
