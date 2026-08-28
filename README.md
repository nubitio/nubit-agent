# Nubit Agent

Nubit Agent applies a closed, versioned set of infrastructure commands for one Nubit
server. It is not a remote shell and does not execute arbitrary user input.

The confirmed MVP operating baseline is recorded in
[`docs/adr/ADR-001-mvp-hosting-platform-baseline.md`](docs/adr/ADR-001-mvp-hosting-platform-baseline.md).
For the current MVP, Debian 12 and Ubuntu 26.04 on amd64 are the supported
web-profile platforms. Ubuntu 26.04 amd64 support includes operator-external
evidence: the operator ran `scripts/test-installer-ubuntu2604-systemd.sh
--version <tag>` on a real Ubuntu 26.04 amd64 VM and confirmed OK; no local
artifact is present in this repository. arm64 is outside the MVP and is not
supported. See
[`docs/adr/ADR-002-mvp-platform-validation.md`](docs/adr/ADR-002-mvp-platform-validation.md)
and
[`docs/adr/ADR-003-ubuntu-2604-amd64-operator-validation.md`](docs/adr/ADR-003-ubuntu-2604-amd64-operator-validation.md).
The operational documentation boundary for T01 is in
[`docs/adr/ADR-004-mvp-operational-documentation-boundary.md`](docs/adr/ADR-004-mvp-operational-documentation-boundary.md).

## Current scope

The initial implementation provides a local health endpoint, an idempotent command
executor, and an agent-initiated polling transport to Nubit Control. Completed command
results are persisted at `/var/lib/nubit-agent/commands.json` by default so a retry
after restart does not repeat a provisioning action. Local site state is persisted
separately in `/var/lib/nubit-agent/sites.json`.
Results awaiting acknowledgement from Control are durably queued in
`/var/lib/nubit-agent/outbox.json` and replayed before new jobs are fetched.

Agent-supported command families known in this codebase are:

| Family | Agent-supported command types | MVP / Control readiness |
| --- | --- | --- |
| system | `system.ping`, `system.reconcile` | Supported by Agent. |
| site | `site.create`, `site.inspect`, `site.suspend`, `site.resume`, `site.delete`, `site.add-domain`, `site.remove-domain`, `site.usage` | Core site lifecycle is supported by Agent; Control lifecycle coverage is partial and must be validated per flow. |
| php | `runtime.set-version`, `runtime.inspect`, `runtime.remove` | Supported by Agent; runtime removal remains an explicit operator/lifecycle action. |
| sftp | `sftp.create`, `sftp.update-key`, `sftp.revoke` | Supported by Agent; Control queues create/update in current portal flows. |
| database | `database.create`, `database.rotate-password`, `database.delete` | Supported by Agent; Control queues create and password rotation in current flows. |
| files | `site.files.list`, `site.files.mkdir`, `site.files.write`, `site.files.read`, `site.files.delete`, `site.files.unzip`, `site.files.rename` | Supported by Agent and exposed through portal file operations; validate operational policy before broad enablement. |
| cron | `site.cron.list`, `site.cron.replace` | Supported by Agent; lifecycle/control policy remains pending. |
| logs | `site.logs.read` | Supported by Agent; operational exposure policy remains pending. |
| backup | `site.backup.list`, `site.backup.create`, `site.backup.restore` | Agent currently performs local `.tar.gz` archives with fixed retention. This is not the approved sellable MVP backup product; S3 + tiers/RPO/RTO convergence is pending. |
| mail | `mail.domain.create`, `mail.domain.delete`, `mail.mailbox.create`, `mail.mailbox.set-password`, `mail.mailbox.set-quota`, `mail.mailbox.delete`, `mail.inventory` | Agent capability exists when mail is configured; complete Control lifecycle and mailbox restore automation remain pending. |

## Install

For a supported Debian 12 or Ubuntu 26.04 amd64 server, run as root:

```bash
curl -fsSL https://raw.githubusercontent.com/nubitio/nubit-agent/main/scripts/install.sh | sh
```

That installs the released binary at `/usr/local/bin/nubit-agent`, creates
`/etc/nubit-agent` and `/var/lib/nubit-agent` with strict permissions, and
enables the `nubit-agent` systemd unit. Add `--profile web` to also install the
web-profile packages for the target distribution. For the operational MVP, issue
an opaque server token in Nubit Control with `POST /api/servers/{id}/rotate-token`
and configure it as `NUBIT_AGENT_TOKEN`; the Agent sends it on every poll as
`X-Agent-Token`.

```bash
sh install.sh --profile web \
  --control-url https://control.example.com

sudo install -m 0600 /dev/null /etc/nubit-agent/agent.env
sudo sh -c 'cat > /etc/nubit-agent/agent.env' <<'EOF'
NUBIT_CONTROL_URL=https://control.example.com
NUBIT_AGENT_TOKEN=<token returned once by Control>
EOF
sudo systemctl restart nubit-agent
```

Do **not** use `--enrollment-token` / `NUBIT_AGENT_ENROLLMENT_TOKEN` for the
current MVP. That path is Agent-side partial/future mTLS code. Current Nubit
Control does not expose `POST /api/agent/enroll`; if an enrollment token is set
and no certificate is present, the Agent attempts enrollment and can fail at
startup. mTLS enrollment and renewal remain pending work.

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
interim transport: the MVP continues to use the controlled temporary token in
`X-Agent-Token`. See `docs/roadmap.md` for the pending mTLS migration.
The agent also publishes OS, architecture, memory, disk, IP, relevant package
versions, capabilities, and PHP runtime usage to `POST /api/agent/inventory`
at startup and every five minutes.

When mTLS credentials are absent, the Agent has enrollment-side behavior: it
attempts `POST /api/agent/enroll` with a CSR and the enrollment token, persists
the generated key, issued certificate, and CA material, and provides for
renewal. Nubit Control does not currently expose the `enroll` or `renew`
endpoints. Consequently, the end-to-end enrollment contract and mTLS transport
have not been deployed or validated; configuring an enrollment token against
current Control can fail Agent startup. This is pending/future code, not an
operational MVP capability.

## Security model

- Commands have a version and an idempotency key.
- Unknown command types fail closed.
- The control-plane transport is agent-initiated polling today, authenticated
  per server with `NUBIT_AGENT_TOKEN` in `X-Agent-Token`. The controlled
  temporary-token MVP remains in place while the end-to-end mTLS contract and
  migration remain pending (`docs/roadmap.md`).
- Privileged hosting operations are added one command type at a time with payload
  validation, tests, least privilege, and rollback behavior.

## Debian 12 and Ubuntu 26.04 amd64 web-profile support

The web-profile is supported on Debian 12 amd64 and Ubuntu 26.04 amd64, with Caddy,
PHP-FPM, PostgreSQL, and native SFTP through OpenSSH. Ubuntu 26.04 amd64 has an
operator-external real-VM systemd validation confirmation and no local artifact
in this repository. arm64 is outside the MVP and is rejected by the installer
because it is not supported. Review the installation actions before running them:

```bash
sudo sh scripts/install.sh --dry-run --profile web
sudo sh scripts/install.sh --profile web
```

On Debian and Ubuntu it enables the `packages.sury.org` PHP repository with a
dedicated keyring, verifies the expected archive-key fingerprint, pins Sury as a
PHP-only package source, and installs PHP-FPM 8.3, 8.4, and 8.5 side by side.
New sites should use PHP 8.4 by default; 8.5 is available for applications
validated on the newest branch.
PHP 8.3 is deprecated: existing sites keep running and can migrate away, but
new sites and migrations into 8.3 are rejected. Every `site.create` command
must specify its `phpVersion`, and each site gets a pool and socket owned by
that version's FPM service.

`runtime.inspect` reports installation state, lifecycle status, security
deadline, and site count for every known runtime. `runtime.remove` requires
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

FTP is not installed by the web profile. The Agent has Stalwart/mail capability,
but complete mailbox lifecycle integration is pending; mailbox restoration
remains assisted for the MVP. See the MVP ADR before making mailbox or restore
assumptions.

The current Agent backup commands can create, list, and restore local `.tar.gz`
archives under the Agent state directory and retain the latest seven archives.
That behavior is useful implementation groundwork only. The approved MVP backup
product remains S3-backed storage with the Basic/Business/Premium cadence,
retention, RPO, and RTO in ADR-001; tier enforcement, S3 storage, restore
rehearsals, and sales readiness remain pending.
