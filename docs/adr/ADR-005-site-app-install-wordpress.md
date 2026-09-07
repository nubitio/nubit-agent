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
- **Not in this ADR:** managed core/plugin auto-updates are a separate
  follow-up (nubit-agent epic #18, agent#5).

## Follow-up: WordPress Caddy template (agent#4, 2026-09-07)

`site.app.install` re-renders the site's Caddy vhost after a successful install,
using `CaddyConfigWordPress` instead of the plain PHP-FastCGI `CaddyConfig`:

- `respond @forbidden 403` for `/xmlrpc.php`, `/wp-config.php`,
  `*.sql`/`*.bak`/`*.log`, and `.git`/`.svn`/`.env`.
- a second `respond @uploads_php 403` matched by `path_regexp`
  (`^/wp-content/uploads/.*\.(php|phtml|phar|php[0-9])$`) so PHP dropped into
  the *dated* upload subdirectories WordPress writes to is denied too, not just
  the top level.
- `Cache-Control: public, max-age=604800` (7 days, **not** `immutable`) on
  static assets (`*.css`, `*.js`, images, fonts) so a managed host can still
  bust the cache after a plugin update.
- Permalinks need no extra rule — Caddy's `php_fastcgi` already falls through to
  `index.php`.

The profile is stored as `app: "wordpress"` on site state so `applyDomains`
(domain add/remove) and `Reconcile` (drift check) regenerate the hardened
template rather than resetting the vhost to the plain one. The swap reuses
`applyDomains` (stage → `caddy validate` → activate → reload, with rollback) so
the protocol lives in one place.

**Degradation:** the vhost swap is *not* part of the install's success
condition. `wp core install` consumes the generated admin password, which the
command result carries exactly once; a transient Caddy/reload failure after
that point would otherwise lose it. So the profile is persisted first (a failed
swap then shows as drift) and a swap failure is reported in the result's
`reason` as "hardening deferred" with `installed: true` — a later
`system.reconcile` or a re-run (which hits the already-present path and
re-applies the vhost) converges it.

**Retry safety:** `wp core download` and `wp config create` run with `--force`
so a run that got as far as writing `wp-config.php` before `wp core install`
failed (bad DB credentials, DB unreachable) is not permanently wedged.

Same validation caveat as the install itself: unit-tested against a fake
`Runner`; real-VM/container validation is pending.
