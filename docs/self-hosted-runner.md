# Self-hosted GitHub Actions runner

By default every CI job in `.github/workflows/ci.yml` runs on a GitHub-hosted
runner (`ubuntu-latest`). This guide moves those jobs to a machine you control,
such as a server or a laptop, using the label **`komputer-hi`** as the example.

An example workflow is in [`deploy/self-hosted-runner/ci.yml`](../deploy/self-hosted-runner/ci.yml).

## Before you start

- You need admin rights on the repo (or the `HumanInitiative` organisation).
- **Use this only for private repos, or public repos that do not run fork PRs
  on it.** A self-hosted runner executes whatever the workflow says, directly on
  your machine. A malicious PR from a fork could run code there.
- A **laptop** only runs jobs while it is on, online and awake. A server is
  better for steady CI.
- The CI jobs need on the machine: `git`, `make`, `curl`, **Docker** (the
  `build` job runs `docker build`), and a C toolchain if tests need cgo.
  Go itself is installed by `actions/setup-go`.

## 1. Register the runner

1. Open **Settings → Actions → Runners → New self-hosted runner** in the repo.
2. Choose the OS and architecture of the target machine (for example
   macOS ARM64 for an Apple Silicon laptop, Linux x64 for a server).
3. Run the commands that page shows. They look like this:

   ```bash
   mkdir actions-runner && cd actions-runner
   curl -o runner.tar.gz -L <download URL from the page>
   tar xzf runner.tar.gz

   ./config.sh \
     --url https://github.com/HumanInitiative/agent-harness-go \
     --token <REGISTRATION TOKEN> \
     --name komputer-hi \
     --labels komputer-hi \
     --work _work
   ```

   - The token is valid for about an hour. Copy it from the page right before use.
   - `--labels komputer-hi` adds the label the workflow targets. The runner
     also gets the automatic labels `self-hosted`, the OS (`Linux`/`macOS`) and
     the architecture (`X64`/`ARM64`).
4. Start it once in the foreground to check that it connects:

   ```bash
   ./run.sh
   ```

   The runner should appear as **Idle** on the Runners page.

## 2. Keep it running as a service

So it survives closing the terminal and rebooting:

```bash
# Linux (systemd)
sudo ./svc.sh install
sudo ./svc.sh start
sudo ./svc.sh status

# macOS (launchd, runs as the logged-in user)
./svc.sh install
./svc.sh start
./svc.sh status
```

On a laptop, also stop it from sleeping while plugged in, or jobs will queue
until it wakes up.

## 3. Point the workflow at the runner

Copy the example over the real workflow:

```bash
cp deploy/self-hosted-runner/ci.yml .github/workflows/ci.yml
```

It differs from the current file only in `runs-on`:

```yaml
runs-on: ${{ fromJSON(vars.CI_RUNNER || '["ubuntu-latest"]') }}
```

Then choose the runner with a repo variable, no commit needed:

1. **Settings → Secrets and variables → Actions → Variables → New repository variable**
2. Name `CI_RUNNER`, value `["self-hosted","komputer-hi"]`.

| `CI_RUNNER`                          | Jobs run on                      |
| ------------------------------------ | -------------------------------- |
| not set                              | GitHub-hosted `ubuntu-latest`    |
| `["self-hosted","komputer-hi"]`      | your runner labelled `komputer-hi` |

To go back to GitHub's runners, delete the variable. If you prefer to hard-code
it, use `runs-on: [self-hosted, komputer-hi]` in each job instead.

Keep the job names (`conventions`, `lint`, `test`, `swagger`, `build`) as they
are: they are the required status checks set by `scripts/github-setup.sh`.

## 4. Verify

1. Push a branch and open a PR, or re-run the latest workflow.
2. In the run, each job's header should say it ran on `komputer-hi`.
3. On the machine, `_work/` holds the checkout and `_diag/` the runner logs.

## Moving the runner to another machine

A runner is bound to one machine. To move it:

```bash
# on the old machine
sudo ./svc.sh stop && sudo ./svc.sh uninstall
./config.sh remove --token <REMOVAL TOKEN>   # token from the Runners page
```

Then repeat steps 1 and 2 on the new machine with the same name and label. No
workflow change is needed, because jobs find the runner by label.

To have two machines share the load, register both with the label
`komputer-hi`. GitHub sends each job to whichever is idle.

## Troubleshooting

| Symptom | Likely cause |
| ------- | ------------ |
| Jobs stay "Queued" | Runner offline, or no runner has all the labels in `runs-on`. |
| `docker: command not found` / permission denied | Install Docker and add the runner's user to the `docker` group, then restart the service. |
| Stale files break a job | Runners reuse `_work/`. Clean it, or register with `--ephemeral` so each job gets a fresh runner. |
| Runner disappeared after a long offline period | GitHub removes runners offline for 14+ days. Re-register it. |
| Registration token rejected | It expired. Generate a new one on the Runners page. |

## Security checklist

- Do not run the runner as root. Use a dedicated user.
- Do not store personal credentials or SSH keys on the machine.
- Keep the runner application and the OS up to date. The runner updates itself
  by default.
- For a server, consider running it inside a container or VM, so a bad job
  cannot reach the rest of the host.
