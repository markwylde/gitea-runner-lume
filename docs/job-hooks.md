# Job hooks

Job hooks are operator-provided scripts that run **inside the job environment**, before the job's first step and after its last one. They are the equivalent of GitHub's [job hooks](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/run-scripts) and are configured under `runner.hooks` in the runner YAML config (see [config.example.yaml](../internal/pkg/config/config.example.yaml)):

```yaml
runner:
  hooks:
    job_started: /hooks/started.sh
    job_completed: /hooks/completed.sh
```

| Setting | Runs |
| --- | --- |
| `runner.hooks.job_started` | Before the job's first step, before any action is downloaded |
| `runner.hooks.job_completed` | After the job's last post step, while the job environment is still up |

`ACTIONS_RUNNER_HOOK_JOB_STARTED` and `ACTIONS_RUNNER_HOOK_JOB_COMPLETED` are read from the runner's environment (`runner.envs`, `runner.env_file`) when the settings are unset, so a configuration carried over from actions/runner keeps working. The settings take precedence. A workflow cannot point the runner at a different hook: the variables are only read from the runner's own environment, never from the job's.

Both hooks are **synchronous** and block the job while they run, and a non-zero exit from either one fails the job. There is no `continue-on-error` and no per-hook timeout — the job's own `runner.timeout` is the only bound. The operator is responsible for the hook's resilience; run anything long in the background from within the hook.

## Where they run

The hooks run in the same place as the job's steps: inside the job container, or on the host in host mode. The paths are resolved *there*, so the script has to exist in the job image or on the host — a path that only exists on the runner host is not visible to a containerized job. For host-wide cleanup that runs after the job environment is gone, use the [post-task script](post-task-script.md) instead.

> This is a deliberate difference from actions/runner, which runs its job hooks on the host, outside any container the job declares. Running them where the steps run is what lets a hook prepare the environment the steps actually see.

The script is run according to its extension:

| Extension | Command |
| --- | --- |
| `.sh` | `bash -e <path>` |
| `.ps1` | `pwsh -command . '<path>'` |
| anything else | the file itself, which needs its own shebang and executable bit |

As on GitHub, the shell flags applied to `run:` steps are **not** applied to a hook — set `pipefail` or anything else you want inside the script.

### Docker-in-Docker and Docker-out-of-Docker

The hook is executed and its files are exchanged over the Docker API, addressed by container ID, so no path is translated between the runner and the daemon. Both setups work unchanged, but they differ in where the hook file has to be:

- **DinD** — the daemon has its own filesystem. Bake the hook into the job image; a path from the runner's filesystem is not visible to it.
- **DooD** — the job container is created by the host's daemon, so a bind mount in `container.options` is resolved against the **host**, not against the runner container. Either bake the hook into the job image, or mount a host directory and add it to `container.valid_volumes`.

A hook path that does not exist inside the job environment fails the job with `No such file or directory`, naming the path.

## Environment

A hook sees the job's environment: the workflow, job and `container:` `env:`, the runner's `envs`, and the `GITHUB_*` context variables, with the same masking applied to its output as to a step's. The step-specific ones (`GITHUB_ACTION`, `GITHUB_OUTPUT`, `GITHUB_STATE`) are not set — a hook is not a step, so `::save-state::` and `::set-output::` have nowhere to go.

Its stdout is part of the job log, inside a collapsible group, and is scanned for workflow commands. `::add-mask::` registers a value to be masked for the rest of the job, `::set-env::` and `::add-path::` apply to the steps that follow.

`$GITHUB_ENV` and `$GITHUB_PATH` point at files that are read back after the hook exits, so the file-command form works too:

```bash
#!/bin/bash
echo "REGISTRY_TOKEN=$(fetch-token)" >> "$GITHUB_ENV"
echo "/opt/tooling/bin" >> "$GITHUB_PATH"
```

Both files are the hook's own, separate from the per-step ones, so nothing a hook writes is truncated by the first step.

## Recommendations

- Keep hooks **fast** and return the right exit code: they are on the critical path of every job, and nothing bounds them.
- Use **idempotent** operations, and expect `job_completed` to run after success, failure, and cancellation alike.
- Mask anything secret the hook prints or exports with `::add-mask::`.

## See also

- [Post-task script](post-task-script.md) — host-side cleanup after the job environment is torn down.
