# Observability

The agent exports traces and metrics over OTLP/HTTP when
`OTEL_EXPORTER_OTLP_ENDPOINT` is set. Local and unconfigured deployments keep
the SDK off; a collector outage never stops polling or command execution.

## What is exported

- Spans: `agent.poll`, `agent.command`, and (when export is on)
  `agent.controlplane GET|POST` for the hop to Nubit Control. Exception
  messages are not attached; only `exception.type`.
- Metrics: `nubit.agent.polls`, `nubit.agent.commands.fetched`,
  `nubit.agent.commands` (attributes: `nubit.command.type`,
  `nubit.command.status`).
- Logs stay on stderr (journald / Portainer). Command payloads, tokens, and
  private keys are not attached to telemetry.

## Configuration

The same env contract as nubit-control:

```dotenv
OTEL_SDK_DISABLED=false
OTEL_SERVICE_NAME=nubit-agent
OTEL_TRACES_EXPORTER=otlp
OTEL_METRICS_EXPORTER=otlp
OTEL_EXPORTER_OTLP_ENDPOINT=https://ingest.nubit.io
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=production,service.namespace=nubit
```

Leave `OTEL_EXPORTER_OTLP_ENDPOINT` empty (the default) to keep export off.
Set `OTEL_SDK_DISABLED=true` to force it off even when an endpoint is present.

Do not put tokens, Authorization headers, domains, or command payloads in
resource attributes.
