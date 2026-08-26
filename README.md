# Nubit Agent

Nubit Agent applies a closed, versioned set of infrastructure commands for one Nubit
server. It is not a remote shell and does not execute arbitrary user input.

## Current scope

The initial implementation provides a local health endpoint and an idempotent command
executor. Completed command results are persisted at
`/var/lib/nubit-agent/commands.json` by default so a retry after restart does not repeat
a provisioning action. The only supported command is `system.ping`; it establishes the
command contract before privileged hosting operations are added.

```bash
go test ./...
go run ./cmd/nubit-agent
curl http://127.0.0.1:9090/healthz
```

The health endpoint binds to `127.0.0.1:9090` by default. Configure another address
only through `NUBIT_AGENT_LISTEN_ADDR`; configure persistent state with
`NUBIT_AGENT_STATE_DIR`.

## Security model

- Commands have a version and an idempotency key.
- Unknown command types fail closed.
- The future control-plane transport uses mTLS and an agent-initiated connection.
- Privileged hosting operations are added one command type at a time with payload
  validation, tests, least privilege, and rollback behavior.

## Debian 12 web profile

The first supported profile is Debian 12 with Caddy, PHP-FPM, PostgreSQL, and native
SFTP through OpenSSH. Review its actions before running it:

```bash
sudo sh scripts/install.sh --dry-run --profile web
sudo sh scripts/install.sh --profile web
```

It does not yet enroll the server, configure sites, or install mail/FTP services.
