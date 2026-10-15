# ADR-006: `mail.alias.set` — extra addresses for a mailbox

- **Status:** Accepted
- **Date:** 2026-10-15
- **Spec:** nubit-control ADR-012 (decision 3) and issues #94, #95
- **Supersedes for this command:** the documentation-only carve-out in
  [ADR-004](ADR-004-mvp-operational-documentation-boundary.md)

## Context

Mail aliases (an extra address that delivers to an existing mailbox) are a basic function of
shared-hosting mail. The same plan on the ISPConfig backend has them; the agent did not, so a
plan could not move between backends without losing them. nubit-control decided to close this
gap (ADR-012) after verifying the exact behaviour against the pinned Stalwart (`v0.16.21`); the
findings are in nubit-control `docs/architecture/mail-forwarding-stalwart.md`.

## Decision

Add exactly one closed, versioned command: **`mail.alias.set`**.

- **Payload:** `{address, aliases}`. `aliases` is the **complete** set of extra addresses for
  `address`. The field must be present: a missing `aliases` is refused, because reading it as
  "empty" would silently remove every alias. `[]` removes them all, explicitly.
- **Execution:** `internal/mail.Manager.SetAliases` reads nothing it does not need and writes the
  whole list with one `x:Account/set`. The server's alias list is **indexed** and re-indexes on
  removal, so the list is never patched by position; writing the full desired list is also what
  makes a retry a no-op.
- **Validation (before any call to the mail server):** every alias is a valid address; none equals
  the mailbox's own address; none is repeated; at most `mail.MaxAliases` (20); every alias domain
  is already registered on this server. The mailbox must exist.
- **Conflicts:** the server enforces uniqueness over the whole directory. A rejection of type
  `primaryKeyViolation` is returned as the typed `mail.ErrAddressInUse` so the control plane can
  tell "that address belongs to someone else" from a malformed request.
- **Result:** the mailbox with the aliases now in force. No secrets are involved.
- **Inventory:** `mail.inventory` now lists each mailbox's aliases (enabled ones, sorted, as
  addresses) so the control plane can reconcile. A node with no aliases pays no extra lookup.

## Not in this decision

External forwarding (`redirect` to an outside address) is a separate, later command: it has two
open points (envelope sender and reputation, and the one-active-Sieve-script limit) that are not
decided. Spam allow/block lists are not offered.

## Verification

Unit tests assert the exact JMAP the manager sends (indexed, whole list) and every refusal.
The same manager was also run against a real `stalwartlabs/stalwart:v0.16.21`: create, set,
idempotent re-set, removal, a conflicting address, an address equal to a mailbox, an unknown
domain, inventory, and clearing, all with the results above.
