# Local state reliability

The daemon now takes an exclusive `writer.lock` in `NUBIT_AGENT_STATE_DIR`.
The daemon and mutating TUI actions therefore fail closed rather than racing
over local state. State, command results, and outbox updates write a synced
temporary file and atomically rename it; audit records are fsynced and rotate
at bounded size. Command results and outbox entries are bounded by count and
64 MiB by default (the oldest outbox entries, and the lowest stable keys in
the legacy timestamp-free result map, are evicted). Rotated audit files are
bounded to three files plus the active log. A result or outbox entry that is
evicted is safe to regenerate from Control because command execution remains
idempotent by its existing key.

Corrupt JSON is never silently discarded: startup refuses to proceed and
leaves the original file untouched for operator recovery. A power loss during
an individual write can expose either the old complete file or the new
complete file, never the temporary file.

## Explicit remaining criteria for #13

This is the safest vertical slice without changing the Control protocol:

* The four files are still separate stores, so a single transaction spanning a
  host mutation, site state, command result, and outbox enqueue is not yet
  implemented. Recovery remains reconciliation/idempotent retry, not atomic
  cross-file commit.
* Retention is local count/byte bounding; there is no age policy for command
  results or outbox entries because their public records previously carried no
  creation timestamp. New outbox records do carry one.
* Disk pressure is detected by propagating write/fsync errors and bounded
  stores, but there is no portable free-space reservation or preflight
  threshold yet.
* Crash injection around every filesystem syscall and large-state benchmarks
  remain CI follow-ups; deterministic unit tests cover atomic replacement,
  corruption refusal, retention, and writer fencing.
