# ADR-002: MVP platform validation

- **Status:** Accepted
- **Spec:** NUBIT-MVP-001-T01
- **Date:** 2026-08-27
- **Supersedes in part:** ADR-001 MVP platform-support scope

## Context

ADR-001 records the MVP hosting baseline. The current platform-support boundary
must distinguish completed integration validation from unvalidated architecture
or distribution targets.

## Drivers

- Make the supported MVP platform unambiguous.
- Avoid presenting an unvalidated platform or architecture as supported.
- Preserve Ubuntu 26.04 arm64 as a future target without asserting readiness.

## Options

1. Support Debian 12 and Ubuntu 26.04 amd64 after equivalent installer and
   systemd validation is recorded; keep Ubuntu 26.04 arm64 unsupported.
2. Support Debian 12 only until all Ubuntu architectures receive validation.
3. Support Debian 12 and Ubuntu 26.04 on all architectures before equivalent
   validation is recorded.

## Outcome

**Given choice:** adopt option 1.

Debian 12 remains supported. Ubuntu 26.04 support is limited to linux/amd64 for
the MVP. Ubuntu 26.04 arm64 is not supported by the installer until a separate
real validation run is recorded.

## Rationale

The confirmed review decision authorizes Ubuntu 26.04 support only for amd64
and requires real validation in an environment with systemd before declaring
that support. The installer also hardens Sury repository setup by verifying the
archive-key fingerprint and pinning packages.sury.org to the PHP package scope.

## Consequences

**Positive**

- MVP support claims match recorded validation and architecture scope.

**Negative / pending**

- Ubuntu 26.04 arm64 integration validation must be executed and recorded before
  it can become supported.
