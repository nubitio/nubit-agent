# Nubit Agent Roadmap

## Mission

Nubit Agent is the execution plane for Nubit Control. It is a replacement for
the hosting-management portion of ISPConfig/CloudPanel, not a remote shell.
Control decides and audits; the Agent applies a closed, versioned command set.

## Supported baseline

The first supported server profile is Debian 12 with Caddy, PHP-FPM,
PostgreSQL, OpenSSH/SFTP, ACME TLS, and Restic backups. Mail, FTP, and DNS
management are separate later profiles.

## Non-negotiable security rules

- Never execute arbitrary shell from a control-plane payload.
- Every command has a version and idempotency key.
- Every site has its own Unix system user, PHP-FPM pool, document root, and
  socket.
- The transport will be agent-initiated mTLS.
- Secrets are never included in logs or persisted command output.
- Destructive commands need an explicit lifecycle and rollback policy.

## Completed

- Local health endpoint.
- Versioned command contract and persistent idempotent executor.
- `system.ping` command.
- Debian 12 web-profile installer with `--dry-run`.
- Validated `site.create` payload contract: domain, Unix user, and PHP 8.3,
  8.4, or 8.5 (8.4 recommended for new sites).
- Isolated-site provisioner for a system user and document root.
- Deterministic Caddy and PHP-FPM configuration renderers.
- Atomic, validated Caddy and PHP-FPM configuration activation with rollback.
- `site.create` executor integration with structured paths and configuration hashes.
- Integration test on a disposable Debian 12 container, validated against real
  Caddy and PHP-FPM 8.3/8.4/8.5 binaries (`scripts/test-site-create-debian12.sh`).
- Agent-initiated polling transport: `GET /api/agent/jobs` /
  `POST /api/agent/jobs/{id}/result` against Nubit Control, authenticated with
  a per-server bearer token (`X-Agent-Token`), not the mTLS enrollment flow
  below yet — see "Agent enrollment".
- Persistent local site inventory exposed through `site.inspect`.
- PHP runtime lifecycle catalog and rollback-safe `php.set-version`; deprecated
  runtimes continue serving existing sites but cannot receive new sites.
- Runtime inventory and explicitly confirmed `php.runtime.remove`, guarded by
  lifecycle status and a zero-site usage check.
- Persistent result outbox replayed before fetching new work, preventing a
  temporary Control outage or agent restart from losing command outcomes.
- Periodic server inventory publication with OS, resources, IP addresses,
  relevant packages, capabilities, and PHP runtime usage.
- ECDSA enrollment, locally held private key, TLS 1.3 client identity, and
  automatic certificate renewal before expiry.
- Drift inspection through `system.reconcile`.
- CI on every push and pull request: gofmt, vet, race-enabled tests, cross
  builds for linux/amd64 and linux/arm64, and shellcheck plus a dry run of the
  installer.
- Tagged releases publishing verified `linux/amd64` and `linux/arm64` binaries
  with `SHA256SUMS`, gated on a green suite.
- `scripts/install.sh` installing the binary, systemd unit and state
  directories, verifying checksums before writing anything.
- Checksum-verified self-update that restarts only between polls, never with a
  command in flight.
- Site suspension, resumption, alias domains, and recoverable confirmed deletion.
- Public-key SFTP lifecycle with restricted OpenSSH configuration.
- PostgreSQL create, password rotation, and confirmed delete operations scoped
  to persistent site ownership.

## Release integrity

Release assets are verified against `SHA256SUMS` published alongside them. That
proves the download is intact; it does not prove the release is authentic, since
the checksum file shares a trust root with the binary. Signing release artifacts
and verifying the signature in both the installer and the self-updater is the
next step for supply-chain integrity, and should land before the agent runs on
servers Nubit does not operate.

## Immediate slice: control-plane transport hardening

1. Replace the bearer-token transport with agent-initiated mTLS per "Agent
   enrollment" below.
2. Attach the implemented inventory publication to the mTLS server identity.

## Agent enrollment

1. Installer creates `/etc/nubit-agent` and `/var/lib/nubit-agent` with strict
   ownership and permissions.
2. Agent generates a keypair locally.
3. Administrator provides a one-time enrollment token.
4. Agent initiates TLS to Control, exchanges the token for a short-lived
   certificate, and renews before expiry.
5. Agent publishes server inventory: OS, packages, PHP versions, disk, memory,
   IP addresses, and supported capabilities.

## Web hosting commands

1. `site.create` (implemented)
2. `site.suspend` (implemented)
3. `site.resume` (implemented)
4. `site.delete` (implemented)
5. `site.add-domain` (implemented)
6. `site.remove-domain` (implemented)
7. `php.set-version` (implemented)
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
4. `tls.issue`
5. `tls.renew`
6. `tls.revoke`

Databases and certificates belong to a site and cannot be addressed by raw
server paths or arbitrary SQL.

## Backups and mail

1. Restic repository enrollment and `backup.create`.
2. Restore rehearsals and `backup.restore` with confirmation.
3. Mail profile: Postfix, Dovecot, Rspamd, DKIM, mailbox commands, aliases,
   quotas, suspension, and deletion.

Mail is intentionally after web/SFTP/TLS/backups because it has the highest
deliverability and abuse-management burden.

## Control-plane contract

Control owns customers, orders, payments, domains, pricing, and provisioning
jobs. The Agent owns local resource state and reports structured results. A
paid order creates a ProvisioningJob; the Agent never determines whether an
order is paid or registers a domain.

## Verification required for every slice

```bash
docker run --rm -v "$PWD:/app" -w /app golang:1.25 go test ./...
sh -n scripts/install.sh
```

Run integration tests in an isolated Debian 12 container before enabling a
command on a real server.
