# Nubit Agent

Nubit Agent applies a closed, versioned set of infrastructure commands for one Nubit
server. It is not a remote shell and does not execute arbitrary user input.

## Current scope

The initial implementation provides a local health endpoint, an idempotent command
executor, and an agent-initiated polling transport to Nubit Control. Completed command
results are persisted at `/var/lib/nubit-agent/commands.json` by default so a retry
after restart does not repeat a provisioning action. The executor supports
`system.ping`, `system.reconcile`, the complete site lifecycle (`site.create`,
`site.inspect`, `site.suspend`, `site.resume`, `site.delete`, domain aliases), `php.set-version`,
`php.runtime.inspect`, and `php.runtime.remove`. Local site state is persisted
separately in `/var/lib/nubit-agent/sites.json`.
Results awaiting acknowledgement from Control are durably queued in
`/var/lib/nubit-agent/outbox.json` and replayed before new jobs are fetched.

## Install

On a Debian 12 server, as root:

```bash
curl -fsSL https://raw.githubusercontent.com/nubitio/nubit-agent/main/scripts/install.sh | sh
```

That installs the released binary at `/usr/local/bin/nubit-agent`, creates
`/etc/nubit-agent` and `/var/lib/nubit-agent` with strict permissions, and
enables the `nubit-agent` systemd unit. Add `--profile web` to also install the
Debian 12 web profile packages, and `--control-url` / `--enrollment-token` to
write enrollment straight into `/etc/nubit-agent/agent.env`:

```bash
sh install.sh --profile web \
  --control-url https://control.example.com \
  --enrollment-token <one-time token>
```

`--dry-run` prints every action without touching the machine, and `--version
<tag>` pins a specific release. Re-running the installer upgrades in place.

Every download is verified against the `SHA256SUMS` published with the release
before anything is written.

## Self-update

A released agent checks `nubitio/nubit-agent` for a newer stable release every
six hours. When it finds one it downloads the binary for its platform, verifies
the checksum, swaps it in atomically — and then waits. **The restart only
happens between polls, never with a command in flight**, so an update can never
land halfway through provisioning a site. Exiting is what applies it: systemd's
`Restart=always` starts the replacement.

| Variable | Effect |
| --- | --- |
| `NUBIT_AGENT_UPDATE=off` | Pin the server to the installed version |
| `NUBIT_AGENT_UPDATE_INTERVAL` | Check interval (Go duration, default `6h`) |
| `NUBIT_AGENT_UPDATE_REPOSITORY` | Release source, for staging a fork |

Source builds report version `dev` and never self-update: an untagged binary has
no ordering against a release, so replacing it would be a guess. Draft and
pre-release tags are ignored.

Checksums establish that the download is intact, not that the release is
authentic — they share a trust root with the binary. Artifact signing is tracked
in [`docs/roadmap.md`](docs/roadmap.md).

## Local development

```bash
go test ./...
go run ./cmd/nubit-agent
nubit-agent --version
curl http://127.0.0.1:9090/healthz
```

The health endpoint binds to `127.0.0.1:9090` by default. Configure another address
only through `NUBIT_AGENT_LISTEN_ADDR`; configure persistent state with
`NUBIT_AGENT_STATE_DIR`.

## Control-plane polling

Set `NUBIT_CONTROL_URL` (e.g. `https://control.example.com`) and `NUBIT_AGENT_TOKEN`
(issued by `POST /api/servers/{id}/rotate-token` on Nubit Control, ROLE_ADMIN, shown
once) to start polling. Left unset, the agent still runs — useful for local dev — it
just never picks up ProvisioningJobs. `NUBIT_AGENT_POLL_INTERVAL` (a Go duration, e.g.
`30s`) overrides the 15s default.

Every poll (`GET /api/agent/jobs`, `X-Agent-Token` header — deliberately not
`Authorization: Bearer`, so these requests never reach Nubit Control's user-facing JWT
authenticator) also doubles as a heartbeat: Nubit Control marks the server `online` with
a fresh `lastSeenAt`. Results go back via `POST /api/agent/jobs/{id}/result`. This is an
interim transport — see `docs/roadmap.md` for the planned move to agent-initiated mTLS.
The agent also publishes OS, architecture, memory, disk, IP, relevant package
versions, capabilities, and PHP runtime usage to `POST /api/agent/inventory`
at startup and every five minutes.

## Security model

- Commands have a version and an idempotency key.
- Unknown command types fail closed.
- The control-plane transport is agent-initiated polling today, bearer-token
  authenticated per server; a future revision moves it to agent-initiated mTLS
  (`docs/roadmap.md`).
- Privileged hosting operations are added one command type at a time with payload
  validation, tests, least privilege, and rollback behavior.

## Debian 12 web profile

The first supported profile is Debian 12 with Caddy, PHP-FPM, PostgreSQL, and native
SFTP through OpenSSH. Review its actions before running it:

```bash
sudo sh scripts/install.sh --dry-run --profile web
sudo sh scripts/install.sh --profile web
```

The web profile enables the `packages.sury.org` PHP repository and installs
PHP-FPM 8.3, 8.4, and 8.5 side by side. New sites should use PHP 8.4 by
default; 8.5 is available for applications validated on the newest branch.
PHP 8.3 is deprecated: existing sites keep running and can migrate away, but
new sites and migrations into 8.3 are rejected. Every `site.create` command
must specify its `phpVersion`, and each site gets a pool and socket owned by
that version's FPM service.

`php.runtime.inspect` reports installation state, lifecycle status, security
deadline, and site count for every known runtime. `php.runtime.remove` requires
`{"phpVersion":"8.3","confirm":true}` and refuses supported versions or any
runtime still referenced by a site.

Site deletion requires a suspended site and explicit confirmation. Its document
root and configuration copies are retained under `/srv/nubit/sites/.trash` and
the returned `recoveryDir` identifies the recovery location.

SFTP access uses public keys, OpenSSH `internal-sftp`, a forced initial document
root, disabled forwarding, and no password authentication. PostgreSQL database
commands use fixed operations and send passwords through stdin; secrets are not
returned in command results. Site deletion is blocked until SFTP access and
owned databases have been removed.

It does not yet install mail or FTP services.
