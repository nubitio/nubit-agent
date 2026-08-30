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
  restore, prune to 7, no local copy. The Basic/Business/Premium cadence,
  retention and RPO/RTO in
  [`docs/adr/ADR-001-mvp-hosting-platform-baseline.md`](docs/adr/ADR-001-mvp-hosting-platform-baseline.md)
  are **not** yet scheduled/enforced.
- The current Agent credential is temporary; mTLS is a future migration, not an
  MVP transport decision.

DNS/mail/backup implementation status is nubit-control ADR-006. Do not infer
provider topology, the backup *scheduler*, DNS selection policy, certificate
lifecycle, or mailbox restore automation beyond the decision records.

## Documentation-only changes

For NUBIT-MVP-001-T01, keep changes to documentation. Record an unknown as a
pending item rather than adding a command, environment variable, or operational
claim.
