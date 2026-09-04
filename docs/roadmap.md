# Nubit Agent Roadmap

## Mission

Nubit Agent is the execution plane for Nubit Control. It is a replacement for
the hosting-management portion of ISPConfig/CloudPanel, not a remote shell.
Control decides and audits; the Agent applies a closed, versioned command set.

## Supported baseline

Debian 12 and Ubuntu 26.04 on amd64 and arm64 are supported platforms for the
current MVP web profile: Caddy, PHP-FPM, MariaDB (PostgreSQL on request),
OpenSSH/SFTP, and ACME TLS. Ubuntu 26.04 amd64 is the reference path and has
operator-external evidence: the operator ran
`scripts/test-installer-ubuntu2604-systemd.sh --version <tag>` on a real Ubuntu
26.04 amd64 VM and confirmed OK; no local artifact is present in this
repository. linux/arm64 on the same distros is supported with a mileage warning
(same sury.org repo, arm64 binary) but has not had the
equivalent operator-external real-VM systemd validation. The MVP platform choices, including DNS, certificate, S3
backup storage, and Stalwart mailbox boundaries, are recorded in
[`adr/ADR-001-mvp-hosting-platform-baseline.md`](adr/ADR-001-mvp-hosting-platform-baseline.md)
and the platform-support clarification in
[`adr/ADR-002-mvp-platform-validation.md`](adr/ADR-002-mvp-platform-validation.md)
and
[`adr/ADR-003-ubuntu-2604-amd64-operator-validation.md`](adr/ADR-003-ubuntu-2604-amd64-operator-validation.md).
The concrete sellable backup implementation is pending; local `.tar.gz` Agent
backup commands do not satisfy the approved S3 + tier/RPO/RTO commitment.

## Non-negotiable security rules

- Never execute arbitrary shell from a control-plane payload.
- Every command has a version and idempotency key.
- Every site has its own Unix system user, PHP-FPM pool, document root, and
  socket.
- The current transport uses the controlled temporary `NUBIT_AGENT_TOKEN` in
  `X-Agent-Token`; the end-to-end mTLS contract and migration are pending.
- Secrets are never included in logs or persisted command output.
- Destructive commands need an explicit lifecycle and rollback policy.

## Completed

- Local health endpoint.
- Versioned command contract and persistent idempotent executor.
- `system.ping` command.
- Debian 12 web-profile installer with `--dry-run`.
- Ubuntu 26.04 amd64 web-profile installer path with Sury keyring fingerprint
  verification and PHP-source apt pinning; Debian/Ubuntu arm64 is accepted with
  a mileage warning, short of amd64's operator-external real-VM validation.
- Validated `site.create` payload contract: domain, Unix user, and PHP 8.3,
  8.4, or 8.5 (8.4 recommended for new sites).
- Isolated-site provisioner for a system user and document root.
- Deterministic Caddy and PHP-FPM configuration renderers.
- Atomic, validated Caddy and PHP-FPM configuration activation with rollback.
- `site.create` executor integration with structured paths and configuration hashes.
- Integration test on a disposable Debian 12 container, validated against real
  Caddy and PHP-FPM 8.3/8.4/8.5 binaries (`scripts/test-site-create-debian12.sh`).
- Ubuntu 26.04 amd64 installer validation includes a disposable container smoke
  test (`scripts/test-installer-ubuntu2604-docker.sh`) and operator-external
  confirmation that `scripts/test-installer-ubuntu2604-systemd.sh --version
  <tag>` completed OK on a real Ubuntu 26.04 amd64 VM. No local artifact for the
  VM run is present in this repository.
- Agent-initiated polling transport: `GET /api/agent/jobs` /
  `POST /api/agent/jobs/{id}/result` against Nubit Control, authenticated with
  the controlled temporary `NUBIT_AGENT_TOKEN` in `X-Agent-Token`.
- Persistent local site inventory exposed through `site.inspect`.
- PHP runtime lifecycle catalog and rollback-safe `runtime.set-version`; deprecated
  runtimes continue serving existing sites but cannot receive new sites.
- Runtime inventory and explicitly confirmed `runtime.remove`, guarded by
  lifecycle status and a zero-site usage check.
- Persistent result outbox replayed before fetching new work, preventing a
  temporary Control outage or agent restart from losing command outcomes.
- Periodic server inventory publication with OS, resources, IP addresses,
  relevant packages, capabilities, and PHP runtime usage.
- Drift inspection through `system.reconcile`.
- CI on every push and pull request: gofmt, vet, race-enabled tests, cross
  builds for linux/amd64 and linux/arm64, and shellcheck plus a dry run of the
  installer on both amd64 and mocked-arm64 hosts.
- Tagged releases publishing verified `linux/amd64` and `linux/arm64` binaries
  with `SHA256SUMS`, gated on a green suite. The installer runs the Debian 12 /
  Ubuntu 26.04 web profile on both; amd64 additionally has operator-external
  real-VM systemd validation.
- `scripts/install.sh` installing the binary, systemd unit and state
  directories, verifying checksums before writing anything.
- Checksum-verified self-update that restarts only between polls, never with a
  command in flight.
- Site suspension, resumption, alias domains, and recoverable confirmed deletion.
- Public-key SFTP lifecycle with restricted OpenSSH configuration.
- MariaDB and PostgreSQL create, password rotation, and confirmed delete scoped
  to persistent site ownership.
- Site file manager commands: `site.files.list`, `site.files.mkdir`,
  `site.files.write`, `site.files.read`, `site.files.delete`,
  `site.files.unzip`, and `site.files.rename`.
- Site usage, log, and cron commands: `site.usage`, `site.logs.read`,
  `site.cron.list`, and `site.cron.replace`.
- Local backup groundwork: `site.backup.list`, `site.backup.create`, and
  `site.backup.restore` create/list/restore local `.tar.gz` archives with fixed
  retention. This is not the approved S3-backed MVP backup product.
- Mail command capability when mail is configured: `mail.domain.create`,
  `mail.domain.delete`, `mail.mailbox.create`, `mail.mailbox.set-password`,
  `mail.mailbox.set-quota`, `mail.mailbox.delete`, and `mail.inventory`.
- HTTP-01 TLS enablement through `tls.letsencrypt.enable`: Caddy issues and
  renews; the command reports the resulting certificate (issuer, SAN list,
  fingerprint, expiry) without ever reading a key. On a node whose Caddy is
  pointed at a reachable ACME CA (`NUBIT_TLS_ACME_CA`, e.g. a private step-ca —
  set `NUBIT_TLS_ACME_CA_ROOT` to the CA root and `NUBIT_TLS_ISSUE_WAIT` to a
  bound like `150s`) the command waits out an in-flight order before reporting
  evidence; with no reachable CA it fails fast with an explicit message.
  `tls.issue` / `tls.renew` / `tls.revoke` — an audited lifecycle contract that
  holds keys — remain future work.

## Release integrity

Release assets are verified against `SHA256SUMS` published alongside them. That
proves the download is intact; it does not prove the release is authentic, since
the checksum file shares a trust root with the binary. Signing release artifacts
and verifying the signature in both the installer and the self-updater is the
next step for supply-chain integrity, and should land before the agent runs on
servers Nubit does not operate.

## Immediate slice: control-plane transport hardening

The MVP operates with the controlled, per-server temporary token described
above. The operational installer configuration is `NUBIT_CONTROL_URL` plus
`NUBIT_AGENT_TOKEN`; Control authenticates polls using the `X-Agent-Token`
header. The Agent persists `NUBIT_AGENT_ENROLLMENT_TOKEN` when the installer is
given `--enrollment-token`, but that path is partial/future work. If mTLS
credentials are absent and the enrollment token is set, it attempts
`POST /api/agent/enroll` with a CSR, persists its key, certificate, and CA
material, and provides for renewal. Nubit Control does not currently expose
`enroll` or `renew` endpoints. Therefore the end-to-end enrollment and renewal
contract, identity issuance, mTLS transport, and migration are neither deployed
nor validated, can fail Agent startup when configured against current Control,
and must not be represented as operational capabilities.

1. Define, deploy, and validate the end-to-end `enroll` and `renew` contract
   between Agent and Control.
2. Migrate the controlled temporary-token transport to agent-initiated mTLS.
3. Attach the implemented inventory publication to the validated mTLS server
   identity.

## Agent enrollment

The Agent-side enrollment flow exists, but its Control contract and the mTLS
transport migration are pending; neither is deployed or validated end to end:

1. Installer creates `/etc/nubit-agent` and `/var/lib/nubit-agent` with strict
   ownership and permissions.
2. Agent generates a keypair locally.
3. Administrator provides a one-time enrollment token.
4. Agent sends a CSR to `POST /api/agent/enroll`, receives a short-lived
   certificate and CA material, persists them with its key, and renews before
   expiry through the Control contract.
5. Agent publishes server inventory: OS, packages, PHP versions, disk, memory,
   IP addresses, and supported capabilities.

## Web hosting commands

1. `site.create` (implemented)
2. `site.suspend` (implemented)
3. `site.resume` (implemented)
4. `site.delete` (implemented)
5. `site.add-domain` (implemented)
6. `site.remove-domain` (implemented)
7. `runtime.set-version` (implemented)
8. `site.inspect` (implemented)

Each command must define validation, idempotency identity, expected files,
service reload behavior, and rollback behavior before implementation.

## Access commands

1. `sftp.create` (implemented)
2. `sftp.update-key` (implemented)
3. `sftp.revoke` (implemented)

SFTP uses OpenSSH native restrictions. FTP is not part of the first profile;
add it only for demonstrated compatibility demand.

## Data and TLS commands

1. `database.create` (implemented)
2. `database.rotate-password` (implemented)
3. `database.delete` (implemented)
4. `tls.letsencrypt.enable` (implemented as preparatory HTTP-01 stub)
5. `tls.issue` (future audited certificate issuance contract)
6. `tls.renew` (future audited renewal contract)
7. `tls.revoke` (future audited revocation contract)

Databases and certificates belong to a site and cannot be addressed by raw
server paths or arbitrary SQL. The current TLS command contract only includes
`tls.letsencrypt.enable`; `tls.issue`, `tls.renew`, and `tls.revoke` remain
future commands and must not be presented as available Agent operations.

## Backups and mail

1. **Done:** backups converged onto S3 object storage (`NUBIT_BACKUP_S3_*`,
   MinIO/Wasabi). Archives carry the document root + a `mariadb-dump` per site
   database; `Restore` rewrites files and re-imports the dumps.
   `site.backup.create` now honours the plan's `retentionDays` (window + a
   seven-newest floor), and `site.backup.verify` performs a scratch-dir restore
   rehearsal reporting `{verified, durationSeconds, files, databases}`.
   **Still pending before sale:** the Basic/Business/Premium cadence + RPO/RTO
   scheduler — that is nubit-control's (`app:backups:run-due` /
   `rehearse-restore` / `audit`), which drives both commands.
2. **Done (Docker image):** the Agent bundles Stalwart and boots it unattended
   when `NUBIT_MAIL_API_SECRET` is set; portal covers domain/mailbox create +
   password/quota/delete. **Pending:** bare-metal `install.sh --profile web,mail`
   systemd wiring; mailbox restoration remains assisted under the MVP baseline.

Mail is intentionally after web/SFTP/TLS/backups because it has the highest
deliverability and abuse-management burden.

## Control-plane contract

The storefront is the source of truth for plans, prices, and refunds. Control
owns customers, orders, payments, domains, and provisioning jobs. The Agent
owns local resource state and reports structured results. A paid order creates
a ProvisioningJob; the Agent never determines whether an order is paid or
registers a domain.

## Verification required for every slice

```bash
docker run --rm -v "$PWD:/app" -w /app golang:1.25 go test ./...
sh -n scripts/install.sh
```

Run integration tests in an isolated Debian 12 container before enabling a
command on a real server. For Ubuntu 26.04, amd64 is the reference path and
requires both the container installer smoke test and the real systemd-based
validation script to pass; the current amd64 systemd evidence is
operator-external with no local artifact. Debian/Ubuntu arm64 is supported with
a mileage warning but must not be represented as having equivalent
operator-external real-VM validation until such a run is recorded.
