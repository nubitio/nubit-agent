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

1. Treat Ubuntu 26.04 amd64 as supported for the MVP after the operator-external
   real VM systemd validation; keep Ubuntu arm64 unsupported.
2. Keep Ubuntu 26.04 unsupported until a local validation artifact is committed.
3. Treat all Ubuntu 26.04 architectures as supported based on the amd64 run.

## Outcome

**Given choice:** adopt option 1.

Debian 12 remains supported. Ubuntu 26.04 is supported only on amd64 for the
MVP. Ubuntu arm64 is not supported.

## Rationale

The confirmed review decision required a real systemd validation run for Ubuntu
26.04 amd64. The operator externally ran the version-pinned systemd validation
script on a real Ubuntu 26.04 amd64 VM and confirmed it completed OK. No local
artifact is present in this repository, so the evidence must be recorded as
operator-external rather than repository-local.

## Consequences

**Positive**

- MVP documentation can state Debian 12 and Ubuntu 26.04 amd64 support.
- Ubuntu architecture support remains limited to the architecture that was
  externally validated.

**Negative / pending**

- Ubuntu arm64 remains unsupported until separate validation is executed and
  recorded.
- The Ubuntu 26.04 amd64 evidence is operator-external unless a local artifact is
  added later.
