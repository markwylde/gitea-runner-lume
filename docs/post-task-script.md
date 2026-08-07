# Post-task script

The post-task script is an optional host hook that runs **once after every task**, after the runner has already finished its normal per-task cleanup. Typical uses include pruning Docker images, vacuuming ephemeral disks, or resetting VM state between jobs.

It is configured under `runner.post_task_script` in the runner YAML config (see [config.example.yaml](../internal/pkg/config/config.example.yaml)).

## When it runs

For each task, execution order is:

1. Workflow runs (steps, actions, containers).
2. In-job cleanup (action `post:` steps, container stop/remove).
3. Job outputs are reported to Gitea.
4. Bind-workdir workspace removal, when `container.bind_workdir` is enabled.
5. **Post-task script** (this hook).
6. Final task acknowledgement to Gitea (`reporter.Close()`).

The script is **additive**: it does not replace any built-in cleanup. When `container.bind_workdir` is enabled, the task workspace directory has usually already been deleted before the script starts. `GITEA_WORKSPACE` is still set to the path the job used, for reference.

## Runner stays offline until the script finishes

This is the most important operational detail.

When the post-task script starts, the runner **stops sending task heartbeats** to Gitea (the same mechanism used during cancel/cleanup). From Gitea's perspective, the runner is **not available for new work** until:

1. The script exits (success or failure), **and**
2. The runner sends the final task flush to Gitea.

While the script runs:

- **Gitea will not assign another task** to this runner for the current job slot (heartbeats are stopped).
- **The runner capacity slot stays occupied** locally — with `capacity: 1`, the poller will not start another task until the script completes.
- **Runner shutdown** (`shutdown_timeout`) counts this phase as part of the in-flight task; a long or stuck script delays graceful shutdown.

If the script **never exits**, the runner remains in this state until `runner.post_task_script_timeout` elapses (default **5 minutes** when a script is configured). The runner then kills the script process and proceeds to the final acknowledgement. Until that timeout fires, **the runner effectively stays offline**.

Set `post_task_script_timeout` to a value that matches how long your housekeeping is allowed to take — not how long you wish it could take. Prefer short, bounded scripts.

### Recommendations

- Keep scripts **fast and bounded** (seconds, not minutes).
- Avoid interactive prompts, blocking network calls without timeouts, or waiting on user input.
- Use **idempotent** operations (the script may run after success, failure, or cancellation).
- Test failure modes: hung script, non-zero exit, missing executable.
- Watch the **runner process log** for script output (it is not written to the Gitea job log).
- On shutdown, ensure scripts respond to process termination within `post_task_script_timeout`.

## Configuration

```yaml
runner:
  # Path to an executable on the host. Empty or omitted disables the hook.
  post_task_script: /usr/local/bin/gitea-post-task.sh

  # Hard limit on script runtime. Default when post_task_script is set: 5m.
  # If the script exceeds this, it is killed and the runner continues.
  post_task_script_timeout: 2m
```

| Option | Default | Description |
| --- | --- | --- |
| `runner.post_task_script` | *(disabled)* | Host path to the script or binary. Relative paths are resolved from the runner process working directory. |
| `runner.post_task_script_timeout` | `5m` (only when script is set) | Maximum time the script may run before the runner kills it and moves on. |

The script must be **executable** on the host (shebang on Linux/macOS, or a native `.exe` / `.bat` / `.cmd` on Windows). **PowerShell (`.ps1`) is not supported yet** as the value of `post_task_script`; the runner executes the configured path directly and does not invoke `powershell.exe` for you.

`gitea-runner exec` does **not** load runner YAML and will not run this hook.

## Environment variables

The script receives `runner.envs` / `runner.env_file` values plus:

| Variable | Description |
| --- | --- |
| `GITEA_TASK_ID` | Numeric task ID. |
| `GITEA_RUN_ID` | Workflow run ID, when provided by the server. |
| `GITEA_REPOSITORY` | Repository slug (`owner/name`). |
| `GITEA_WORKSPACE` | Workspace path used for the job (may already be deleted). |
| `GITEA_JOB_RESULT` | `success`, `failure`, `cancelled`, `skipped`, or `unknown`. |

The script environment is **not** a full copy of the job container environment. System variables such as `PATH` are only present if you define them in `runner.envs` or `runner.env_file`.

## Output and errors

- **Stdout/stderr** are written to the **runner process log** (logrus), prefixed with `post-task script stdout:` / `post-task script stderr:`.
- **Non-zero exit codes** are logged as warnings only. They do **not** change the job result already reported to Gitea.
- **Timeouts and start failures** are logged as warnings; the runner still completes the task acknowledgement.

## Interaction with other timeouts

| Timeout | Effect on post-task script |
| --- | --- |
| `runner.post_task_script_timeout` | Kills the script if it runs too long. This is the **only** timeout that bounds the script. |
| `runner.timeout` | Caps the task **up to** the script. The script detaches from the task deadline, so a job near the runner timeout limit does **not** cut the script short — it still gets its full `post_task_script_timeout`. |
| `runner.shutdown_timeout` | On SIGINT/SIGTERM, bounds how long the runner waits for the **task** to finish. The post-task script detaches from cancellation, so it is **not** interrupted by this window and may extend shutdown until its own `post_task_script_timeout` elapses. |

## Examples

### Linux — prune dangling Docker resources

`/usr/local/bin/gitea-post-task.sh`:

```sh
#!/bin/sh
set -eu
docker image prune -f
docker builder prune -f --filter 'until=24h'
```

`config.yaml`:

```yaml
runner:
  post_task_script: /usr/local/bin/gitea-post-task.sh
  post_task_script_timeout: 3m
```

### Windows — batch file (`.cmd`)

Use a `.cmd` or `.bat` file. PowerShell scripts are **not supported yet** as `post_task_script`; call PowerShell from a batch wrapper if needed:

`C:\gitea-runner\scripts\post-task.cmd`:

```bat
@echo off
docker image prune -f
```

```yaml
runner:
  post_task_script: C:\gitea-runner\scripts\post-task.cmd
  post_task_script_timeout: 3m
```

PowerShell workaround until native `.ps1` support exists:

`C:\gitea-runner\scripts\post-task.cmd`:

```bat
@echo off
powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "%~dp0post-task.ps1"
```

## Windows notes

- Supported as `post_task_script`: `.exe`, `.bat`, `.cmd`.
- **Not supported yet:** `.ps1` as the configured path (use a `.cmd` wrapper; see above).
- `.sh` files require a Unix shell on the PATH unless you point `post_task_script` at the interpreter.
- Use backslashes or forward slashes in YAML paths; both work in Go on Windows.

## See also

- [Job hooks](job-hooks.md) — scripts running inside the job environment, around its steps
- [Configuration](../README.md#configuration) — generating and loading `config.yaml`
- [config.example.yaml](../internal/pkg/config/config.example.yaml) — all runner options
- Bind-workdir idle cleanup (`runner.workdir_cleanup_age`) — separate from this hook; runs only when the runner is idle
