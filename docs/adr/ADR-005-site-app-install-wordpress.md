# ADR-005: `site.app.install` — managed WordPress on the shared web profile

- **Status:** Accepted
- **Date:** 2026-09-07
- **Spec:** NUBIT-MVP-001-T01 (WordPress hosting, nubit-control epic #18)
- **Supersedes for this command:** the documentation-only carve-out in
  [ADR-004](ADR-004-mvp-operational-documentation-boundary.md)

## Context

ADR-004 froze the closed command catalogue for T01: new command types are added
"one at a time with payload validation, tests, least privilege, and rollback
behavior", and until now only documentation changes were in scope.

nubit-control wants to sell a `wordpress_hosting` plan. A managed-WordPress
shared host is a normal site (Unix user + PHP-FPM pool + document root + Caddy
vhost + MariaDB) plus one action: install WordPress into the document root. That
action is what this ADR adds to the catalogue.

## Decision

Add exactly one closed, versioned command: **`site.app.install`**.

- **Payload:** `{siteId, app, version, locale, adminUser, adminEmail, siteTitle,
  siteUrl, dbName, dbUser, dbPassword, dbHost}`. `app` is restricted to
  `"wordpress"` and fails closed for any other value — the field exists so a
  second application can be added later without a second command type. The
  database is provisioned separately (`database.create`); its credentials are
  passed in the payload, not discovered by the agent.
- **Execution:** `internal/site.Provisioner.AppInstall` runs **wp-cli as the
  site's own Unix user** (`sudo -u <systemUser> -- wp --path=<documentRoot> …`):
  `core download` → `config create --skip-check` → `core install --skip-email`.
  It never runs as root and never touches another site.
- **Idempotency:** `wp core is-installed` is the first call. A site that already
  carries a WordPress install is left untouched and reported as
  `{installed:false, reason:"already-present"}` — not an error.
- **Admin password:** taken from the payload if present, otherwise generated
  with `crypto/rand`. It is returned in the command result **once** and is never
  logged and never written to site state.
- **Limits:** a 10-minute timeout entry (`SiteAppInstall` in
  `executor_config.go`); the default per-type rate limit applies.
- **Image:** the Docker image gains `wp-cli` (phar pinned to 2.11.0, verified
  against its published `sha512`) and `sudo`.

## Consequences

- The T01 documentation-only rule still governs every **other** command; this
  ADR lifts it for `site.app.install` only.
- **Validation:** unit-tested against a fake `Runner` (payload validation,
  as-the-site-user invocation, the already-present no-op, wp-cli failure
  surfacing). A real-VM / disposable-container run against wp-cli and a real
  MariaDB — the discipline the web profile already applies to `site.create` —
  is the follow-up before this is enabled on a customer node, tracked in
  `docs/roadmap.md`.
- **Not in this ADR:** the WordPress-tuned Caddy template (permalinks, static
  caching, `xmlrpc`/`wp-login` hardening) and managed core/plugin auto-updates
  are separate follow-ups; `site.app.install` only installs.
