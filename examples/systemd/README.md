# Running the runner as a systemd service

[`gitea-runner.service`](./gitea-runner.service) is an example unit for running
the runner as a background service on a systemd host.

## Setup

1. Install the `gitea-runner` binary (e.g. to `/usr/local/bin/gitea-runner`).
2. Create a dedicated user and working directory:

   ```bash
   sudo useradd --system --home-dir /var/lib/gitea-runner --create-home gitea-runner
   ```

3. Write a config, hand it to the service user, and register as that user so the
   `.runner` file ends up in the working directory:

   ```bash
   sudo mkdir -p /etc/gitea-runner
   sudo gitea-runner config init --config /etc/gitea-runner/config.yaml
   sudo chown gitea-runner /etc/gitea-runner/config.yaml
   cd /var/lib/gitea-runner
   sudo -u gitea-runner gitea-runner register --config /etc/gitea-runner/config.yaml
   ```

4. Install and enable the unit:

   ```bash
   sudo cp gitea-runner.service /etc/systemd/system/gitea-runner.service
   sudo systemctl daemon-reload
   sudo systemctl enable --now gitea-runner
   ```

Adjust the binary path, config path, working directory and user to match your
installation. If jobs use the host's Docker daemon, uncomment the
`docker.service` dependencies in the unit.
