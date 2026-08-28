# ADR-004: MVP operational documentation boundary

- **Status:** Accepted
- **Spec:** NUBIT-MVP-001-T01
- **Date:** 2026-08-28
- **Supersedes in part:** ADR-001 MVP hosting platform baseline; ADR-003 Ubuntu 26.04 amd64 operator validation

## Context

The Agent repository contains implemented command handlers beyond the approved
sellable MVP boundary, and it also contains Agent-side enrollment/mTLS code that
depends on Control endpoints that are not present today. Documentation must close
T01 without presenting partial or future capabilities as operational.

## Drivers

- Keep MVP support claims limited to confirmed platforms.
- Document the operational token-based Agent transport that works with current
  Control.
- Separate implemented local/partial backup behavior from the approved S3
  backup commitment.
- Present Agent command support without implying Control, lifecycle, or operator
  readiness for every command family.

## Options

1. Document the current MVP as Debian 12 and Ubuntu 26.04 amd64 only, using
   `NUBIT_AGENT_TOKEN` in `X-Agent-Token`; mark enrollment/mTLS and S3 backup
   convergence as pending.
2. Present enrollment/mTLS as the MVP installation path.
3. Present all Agent command handlers, including local backups, as sellable MVP
   features.

## Outcome

**Given choice:** adopt option 1.

Debian 12 and Ubuntu 26.04 are supported only on amd64 for the MVP web profile;
arm64 is outside the MVP. The operational MVP transport uses
`NUBIT_AGENT_TOKEN` sent as `X-Agent-Token`. `--enrollment-token`,
`NUBIT_AGENT_ENROLLMENT_TOKEN`, and mTLS are partial/future work and are not
usable against current Control because `POST /api/agent/enroll` is missing; using
that path can fail Agent startup.

The Agent may expose local backup operations that create/list/restore local
`.tar.gz` archives with fixed retention. The approved MVP backup target remains
S3 with plan tiers, RPO, and RTO; convergence from local archives to that target
is pending and must not be sold as complete.

## Rationale

The confirmed grilling decisions explicitly limit the MVP to amd64 on Debian 12
and Ubuntu 26.04, confirm the temporary token transport, reject arm64 from the
MVP, and require backup documentation to distinguish local implementation from
the approved S3/tiered recovery commitment.

## Consequences

**Positive**

- Operators have a documented installation path that works with current Control.
- MVP claims avoid arm64, mTLS enrollment, and backup-sales overstatement.
- Command documentation can expose known Agent handlers while retaining Control
  and lifecycle readiness boundaries.

**Negative / pending**

- Enrollment and mTLS remain pending until Control implements and validates the
  required endpoints.
- S3 backup storage, tier enforcement, restore rehearsals, and RPO/RTO evidence
  remain pending.
- Control/portal/operator workflows may lag behind implemented Agent command
  handlers.
