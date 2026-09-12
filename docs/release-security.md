# Release security

## Release inputs

The release workflow accepts only stable tags in the form `vMAJOR.MINOR.PATCH`.
It resolves the tag to a commit before checkout, so a mutable branch or an
untrusted dispatch input cannot choose the source that is signed. Existing
GitHub releases are immutable: publishing the same tag twice fails.

The agent binaries are signed with the Ed25519 private key stored in the
GitHub Actions secret `NUBIT_RELEASE_SIGNING_PRIVATE_KEY`. The corresponding
public key is embedded in the updater, copied into the installer, and retained
under `packaging/`. The installer and self-updater require both the detached
signature and `SHA256SUMS` to verify before replacing a binary.

The release contains an SPDX JSON SBOM and GitHub build-provenance attestations.
Operators should retain the release tag, commit, asset checksums, signatures,
SBOM, and attestation with the deployment record.

## Key rotation

1. Generate a new Ed25519 keypair offline.
2. Review and merge a change replacing every checked-in public-key copy.
3. Update `NUBIT_RELEASE_SIGNING_PRIVATE_KEY` only after that change is merged.
4. Publish a new release and verify both assets before rollout.
5. Retain the previous private key in restricted offline recovery storage until
   every supported node has crossed the rotation boundary.

If the private key is compromised, revoke access to the GitHub secret, stop
automatic rollout, merge a new public key, publish a new release, and reinstall
or manually update nodes from the retained known-good release.

## Rollout and rollback

Releases are applied first to a disposable or canary node. A failed CI,
signature check, startup, or health check aborts rollout. The previous release
asset must be retained so operators can reinstall it with its exact tag.
The self-updater swaps binaries atomically and only exits between polls; an
operator rollback is performed by installing the previous exact tag and
restarting the systemd unit. Full fleet canary orchestration and automatic
health-gated rollback remain deployment-controller responsibilities.
