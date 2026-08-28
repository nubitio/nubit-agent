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
- Chatwoot is self-hosted. Mailboxes use Stalwart; their restoration is assisted.
- Backups use S3 storage. The retention and recovery targets are
  recorded in [`docs/adr/ADR-001-mvp-hosting-platform-baseline.md`](docs/adr/ADR-001-mvp-hosting-platform-baseline.md).
- The current Agent credential is temporary; mTLS is a future migration, not an
  MVP transport decision.

Do not infer provider topology, backup implementation, DNS selection policy,
certificate lifecycle, or mailbox restore automation beyond that decision record.

## Documentation-only changes

For NUBIT-MVP-001-T01, keep changes to documentation. Record an unknown as a
pending item rather than adding a command, environment variable, or operational
claim.
