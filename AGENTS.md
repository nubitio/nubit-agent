# Nubit Agent — contributor context

Nubit Agent is the execution plane: it applies only closed, versioned
infrastructure commands received from Nubit Control. It is never a remote shell.

## MVP baseline (NUBIT-MVP-001-T01)

- The initial offering is shared hosting for Peru, priced and charged in PEN, with
  Culqi for payments.
- Deployments use a multi-provider VPS model.
- DNS may be provided by PowerDNS operated by Nubit, the Cloudflare API, or an
  external DNS provider.
- TLS supports Let's Encrypt and externally supplied certificates.
- Chatwoot is self-hosted. Mailboxes use Stalwart — on the Docker image the
  Agent bundles and boots it when `NUBIT_MAIL_API_SECRET` is set and administers
  it over JMAP (`internal/mail`). Mailbox restoration is still assisted.
- Backups are **S3 object storage** (`internal/objectstore` + `internal/backup`,
  `NUBIT_BACKUP_S3_*`): document root + `mariadb-dump` per database, real
  restore, no local copy. `site.backup.create` prunes on the plan's
  `retentionDays` (window + a seven-newest floor); `site.backup.verify` does a
  scratch-dir restore rehearsal. The Basic/Business/Premium **cadence** and the
  RPO/RTO scheduler in
  [`docs/adr/ADR-001-mvp-hosting-platform-baseline.md`](docs/adr/ADR-001-mvp-hosting-platform-baseline.md)
  live in nubit-control, not here.
- The current Agent credential is temporary; mTLS is a future migration, not an
  MVP transport decision.

DNS/mail/backup implementation status is nubit-control ADR-006. Do not infer
provider topology, the backup *scheduler*, DNS selection policy, certificate
lifecycle, or mailbox restore automation beyond the decision records.

## Documentation-only changes

For NUBIT-MVP-001-T01, keep changes to documentation. Record an unknown as a
pending item rather than adding a command, environment variable, or operational
claim.

**Carve-out (operator observability & tooling).** Local, read-only
observability and the operator TUI are allowed to be code: the `GET /status`
endpoint (`internal/status`) and `nubit-agent tui` (`internal/tui`). They do
not add a privileged command type, do not change the Agent↔Control contract,
and hold no secrets. The closed command set, the control-plane transport, and
every operational claim about them stay under the rule above.
