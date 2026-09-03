# ADR-003: Ubuntu 26.04 amd64 operator validation

- **Status:** Accepted
- **Spec:** NUBIT-MVP-001-T01
- **Date:** 2026-08-28
- **Supersedes in part:** ADR-002 MVP platform validation

## Context

ADR-002 required real systemd validation before Ubuntu 26.04 amd64 could be
represented as supported for the MVP web profile. The repository has no local
artifact for this run, but the operator externally executed and confirmed OK:

```bash
scripts/test-installer-ubuntu2604-systemd.sh --version <tag>
```

The environment was a real Ubuntu 26.04 amd64 VM.

## Drivers

- Keep support claims aligned with confirmed validation.
- Preserve the MVP architecture boundary.
- Distinguish operator-external evidence from local repository artifacts.

## Options

1. Treat Ubuntu 26.04 amd64 as the supported reference path after the
   operator-external real VM systemd validation; support arm64 with a clear
   lower-maturity caveat.
2. Keep Ubuntu 26.04 unsupported until a local validation artifact is committed.
3. Treat all Ubuntu 26.04 architectures as supported based on the amd64 run.

## Outcome

**Given choice:** adopt option 1.

Debian 12 and Ubuntu 26.04 are supported on amd64 and arm64 for the MVP. amd64
is the reference path because it has operator-external real-VM systemd evidence.
arm64 is supported by the released binary and installer, but lacks equivalent
real-VM evidence and is therefore lower maturity.

## Rationale

The confirmed review decision required a real systemd validation run for Ubuntu
26.04 amd64. The operator externally ran the version-pinned systemd validation
script on a real Ubuntu 26.04 amd64 VM and confirmed it completed OK. No local
artifact is present in this repository, so the evidence must be recorded as
operator-external rather than repository-local.

## Consequences

**Positive**

- MVP documentation can state Debian 12 and Ubuntu 26.04 support on amd64 and
  arm64 while identifying amd64 as the validated reference path.

**Negative / pending**

- Ubuntu arm64 needs separate real-VM validation to reach amd64's confidence
  level.
- The Ubuntu 26.04 amd64 evidence is operator-external unless a local artifact is
  added later.
