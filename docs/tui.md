# Operator TUI (`nubit-agent tui`)

A full-screen cockpit for **one node**, run over SSH on the box. It is a
separate short-lived process — like `nubit-agent enroll` — not part of the
daemon. It reads:

- the running daemon's `GET /status` (see below), and
- the shared state files under the state directory
  (`commands.json` is not parsed; `audit.log`, `outbox.json`, `sites.json` are).

It never opens a shell, never runs an arbitrary command, and never mutates
Nubit Control. The only writes it can make are the confirmed node-local
actions in the **Actions** panel, and those are gated on the daemon being
stopped so two processes never race the same JSON file.

```bash
nubit-agent tui                       # talks to 127.0.0.1:9090 by default
nubit-agent tui --addr 127.0.0.1:9090 --state-dir /var/lib/nubit-agent
nubit-agent tui --refresh 5s
nubit-agent tui --control-admin-url https://control.example.com \
                --control-admin-token <ROLE_ADMIN bearer>   # enables the Control panel
```

Flags fall back to `NUBIT_AGENT_LISTEN_ADDR`, `NUBIT_AGENT_STATE_DIR`,
`NUBIT_CONTROL_URL` and `NUBIT_CONTROL_ADMIN_TOKEN`.

## Keys

| Key | Action |
| --- | --- |
| `tab` / `shift+tab`, `1`–`5` | switch panel |
| `r` | refresh now |
| `↑`/`↓` (`k`/`j`) | move selection (Sites, Actions) |
| `enter` | activate the selected action / open its confirm field |
| `esc` | cancel an open confirm field |
| `q` / `ctrl+c` | quit |

## Panels

### 1 · Overview — from `GET /status`

Version, uptime, listen address, state dir; the control transport
(`token` / `mtls` / `offline`) and, when enrolled, the client-cert expiry;
poll health (last poll time, last error, ok/fail counts, jobs fetched and
executed); outbox depth; local site count; whether a self-update is staged.

The poll line goes amber when the last poll is older than three minutes and
red when the last poll failed.

If `/status` is unreachable the panel says so in red — the daemon is down or
on another address — and the other panels keep working from the files.

### 2 · Jobs — from `audit.log` + `outbox.json`

Recent commands newest-first (time, type, result, duration) from the audit
NDJSON, then the **pending outbox**: results the agent executed but Control
has not yet acknowledged. An empty outbox means Control is caught up.

### 3 · Sites — from `sites.json`

One row per site (id, system user, PHP version, status) and a detail block
for the selected row (domains, document root, PHP socket, databases, database
users, SFTP, worker/memory limits). This is the same data `site.inspect`
returns.

### 4 · Actions

| Action | Safe while daemon runs? | Effect |
| --- | --- | --- |
| Reconcile (drift report) | yes — read-only | runs `Provisioner.Reconcile`, lists any file/config drift |
| Flush outbox to Control | no | drains `outbox.json` to Control now (needs `NUBIT_CONTROL_URL` + `NUBIT_AGENT_TOKEN`) |
| Enroll (mTLS) | yes — one-time, idempotent | prompts for a token, runs the same path as `nubit-agent enroll`; restart the daemon after |
| Reset node | no | force-removes every site this node created and clears `commands.json` / `outbox.json` — the same effect as Control's `system.reset`. Requires typing `RESET`. |

"Flush outbox" and "Reset node" refuse while `/status` answers. Stop the unit
first (`systemctl stop nubit-agent`) or drive the change from Control with
`app:agent:dispatch`.

### 5 · Control — read-only, only with `--control-admin-url`

Lists `GET /api/servers` from Nubit Control so you can correlate a node's
local Jobs view with the queued/running/succeeded jobs Control sees. It
cannot queue anything — that is `app:agent:dispatch` on Control.

## `GET /status`

The daemon serves this next to `/healthz` on `NUBIT_AGENT_LISTEN_ADDR`
(loopback by default). It is read-only and carries no secrets, so it is also
useful as `curl -s 127.0.0.1:9090/status | jq` in a script or an alert probe.

```json
{
  "version": "v1.4.0",
  "startedAt": "2026-09-04T11:14:41Z",
  "uptimeSeconds": 3720,
  "listenAddr": "127.0.0.1:9090",
  "stateDir": "/var/lib/nubit-agent",
  "controlUrl": "https://control.example.com",
  "transport": "token",
  "enrolled": false,
  "polling": true,
  "pollInterval": "15s",
  "lastPollAt": "2026-09-04T12:16:31Z",
  "lastPollOk": true,
  "pollsOk": 244,
  "pollsFailed": 1,
  "jobsFetched": 12,
  "jobsExecuted": 12,
  "outboxDepth": 0,
  "siteCount": 3,
  "selfUpdatePending": false
}
```

The counters describe this process's session, not the node's history — a
restart resets them. `outboxDepth` and `siteCount` are read live from the
outbox and site-state stores on every request.
