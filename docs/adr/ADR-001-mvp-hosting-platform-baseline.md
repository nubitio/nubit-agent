# ADR-001: MVP hosting platform baseline

- **Status:** Accepted
- **Spec:** NUBIT-MVP-001-T01
- **Date:** 2026-08-27

## Context

Nubit requires a documented MVP operating baseline shared by Control and Agent.
This record scopes confirmed product and infrastructure choices; it does not add
Agent commands or implementation contracts.

## Drivers

- Serve the initial Peru/PEN hosting offering.
- Keep infrastructure and provider choices open within the confirmed boundaries.
- State recovery commitments and the division between self-service and assisted
  restoration.

## Options

1. Adopt the confirmed shared-hosting, multi-provider baseline in this ADR.
2. Leave the MVP provider, recovery, and transport choices unspecified.

## Outcome

**Given choice:** adopt option 1.

- The MVP is shared hosting for Peru, in PEN, using Culqi.
- VPS infrastructure is multi-provider.
- DNS is selected per service from Nubit-operated PowerDNS, the Cloudflare API,
  or an external DNS provider.
- Certificates may use Let's Encrypt or externally supplied certificates.
- Chatwoot is self-hosted.
- Backups use S3 storage:

  | Plan | Backup cadence | Retention | RPO | RTO |
  | --- | --- | --- | --- |
  | Basic | Daily | 7 days | 24 h | 8 h |
  | Business | Every 6–12 h | 14–30 days | 6–12 h | 4 h |
  | Premium | Every 1–6 h | 30 days | 1–6 h | 1–2 h |

- Web and database restoration is self-service. Stalwart mailbox restoration is
  assisted.
- The Agent uses a temporary token today; migration to mTLS is future work.
- The storefront is the source of truth for plans, prices, and refunds.

## Rationale

The task's closed grilling decisions explicitly confirm each outcome above.
They do not confirm a single VPS provider, a single DNS provider, a backup tool,
or mTLS rollout details.

## Consequences

**Positive**

- Agent-facing work can support the stated DNS, certificate, backup, and mailbox
  boundaries without assuming one provider.
- Recovery expectations are explicit by plan.

**Negative / pending**

- Provider selection rules, credential handling, backup implementation and
  verification, certificate lifecycle, Chatwoot integration, and the mTLS
  migration remain unspecified.
- No Agent command, API, schema, schedule, or automation is introduced by this
  record.
