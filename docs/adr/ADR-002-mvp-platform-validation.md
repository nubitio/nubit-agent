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
- Distinguish the reference validation path from supported architectures with
  different validation maturity.
- Support Ubuntu 26.04 arm64 without asserting equivalent real-VM validation.

## Options

1. Support Debian 12 and Ubuntu 26.04 on amd64 and arm64; treat amd64 as the
   reference path after installer and systemd validation is recorded, and
   document arm64's lower field maturity.
2. Support Debian 12 only until all Ubuntu architectures receive validation.
3. Support Debian 12 and Ubuntu 26.04 on all architectures before equivalent
   validation is recorded.

## Outcome

**Given choice:** adopt option 1.

Debian 12 and Ubuntu 26.04 are supported on linux/amd64 and linux/arm64 for the
MVP. amd64 is the reference path with recorded installer and systemd validation.
arm64 is supported by released binaries, installer coverage, and CI, but does
not yet have an equivalent real systemd validation run.

## Rationale

The installer ships and verifies binaries for both architectures, uses the same
Sury repository setup for both, and CI covers cross-builds plus arm64 installer
acceptance. The real systemd validation remains the higher-confidence amd64
reference. The installer also hardens Sury repository setup by verifying the
archive-key fingerprint and pinning packages.sury.org to the PHP package scope.

## Consequences

**Positive**

- MVP support claims match recorded validation and architecture scope.

**Negative / pending**

- Ubuntu 26.04 arm64 still needs equivalent real-systemd validation to reach
  amd64's confidence level.
