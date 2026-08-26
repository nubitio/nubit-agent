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
- Validated `site.create` payload contract: domain, Unix user, PHP 8.3.
- Isolated-site provisioner for a system user and document root.
- Deterministic Caddy and PHP-FPM configuration renderers.

## Immediate slice: real site.create

1. Write Caddy and PHP-FPM configuration files atomically into staging paths.
2. Validate Caddy and PHP-FPM configuration before replacing active files.
3. Move valid files into active paths and reload services.
4. Roll back files and user directories when validation or reload fails.
5. Connect the provisioner to the `site.create` command executor.
6. Return site ID, document root, PHP socket, and applied configuration hashes.
7. Add integration tests using a disposable Debian 12 container.

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

1. `site.create`
2. `site.suspend`
3. `site.resume`
4. `site.delete`
5. `site.add-domain`
6. `site.remove-domain`
7. `php.set-version`
8. `site.inspect`

Each command must define validation, idempotency identity, expected files,
service reload behavior, and rollback behavior before implementation.

## Access commands

1. `sftp.create`
2. `sftp.rotate-password` or SSH-key update
3. `sftp.revoke`

SFTP uses OpenSSH native restrictions. FTP is not part of the first profile;
add it only for demonstrated compatibility demand.

## Data and TLS commands

1. `database.create`
2. `database.rotate-password`
3. `database.delete`
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
