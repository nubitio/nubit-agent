# Capacity admission and isolation boundary

The agent now admits site PHP reservations against a measured host envelope for
CPU, memory, PHP workers, and PIDs. Backup scratch is checked immediately
before archive operations; operation pools cover backup, restore, archive,
usage, logs, and database work. Disk, database connection, I/O, and network
budgets are intentionally not advertised because the agent cannot account for
those quantities reliably across provisioners. Existing site reservations are
rebuilt from `sites.json` at startup.
`GET /status` exposes the limit, reserved totals, reservation count, and
in-flight expensive operations. Admission is atomic: a rejected create/update
does not change the existing reservation.

The envelope can be tuned with `NUBIT_AGENT_CAPACITY_*` variables (the names
match the JSON fields in the status response, using `CPU_MILLI`, `MEMORY_BYTES`,
`PHP_WORKERS`, `PIDS`, and `SCRATCH_BYTES`). Expensive operation pools are configured
with `NUBIT_AGENT_OPERATION_CONCURRENCY_backup`, `_restore`, `_archive`,
`_usage`, `_logs`, or `_database`. Invalid and non-positive values are ignored
in favour of safe defaults.

Backup/restore/archive operations also require the configured scratch reserve
to be free and retain the existing per-command timeout and filesystem limits.
When a legacy provisioner outlives its command timeout, its operation admission
and any site reservation remain held until that provisioner returns. This
prevents a retry from overlapping unknown work; a successful late completion
keeps its reservation and a late error rolls it back.

## Deliberate portability boundary

This change does **not** claim hard per-tenant cgroup isolation. cgroup v1/v2
setup, delegation, and container runtime policy differ across supported Debian
and Ubuntu installations and cannot safely be enabled by the portable static
agent without an explicit host contract. The implemented guarantee is safe
admission plus bounded in-process operation concurrency, timeouts, and scratch
preflight. A future host-specific launcher must apply cgroup CPU/memory/PID/I/O
limits before provisioning; until then noisy processes outside the agent (or a
provisioner's child process that ignores its timeout) are not hard-isolated.

The global executor mutex remains intentionally unchanged. No command
parallelism was introduced without a reviewed conflict matrix.
