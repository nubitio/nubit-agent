// Package telemetry exports traces and metrics to an OTLP collector when
// configured, matching nubit-control's env contract. A missing or broken
// collector must never stop the agent from polling or applying commands.
package telemetry

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/nubitio/nubit-agent"

// Start installs a global TracerProvider and MeterProvider when
// OTEL_EXPORTER_OTLP_ENDPOINT is set and OTEL_SDK_DISABLED is not true.
// The returned shutdown flushes exporters; it is safe to call when Start
// returned a no-op.
func Start(ctx context.Context) (func(context.Context) error, error) {
	if !Enabled() {
		return func(context.Context) error { return nil }, nil
	}

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "nubit-agent"
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(attribute.String("service.name", serviceName)),
	)
	if err != nil {
		return nil, err
	}

	traceExporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)

	metricExporter, err := otlpmetrichttp.New(ctx)
	if err != nil {
		_ = tracerProvider.Shutdown(ctx)
		return nil, err
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(15*time.Second))),
	)
	otel.SetMeterProvider(meterProvider)

	log.Printf("nubit-agent: telemetry exporting to %s as %s", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), serviceName)

	return func(shutdownCtx context.Context) error {
		traceErr := tracerProvider.Shutdown(shutdownCtx)
		metricErr := meterProvider.Shutdown(shutdownCtx)
		if traceErr != nil {
			return traceErr
		}
		return metricErr
	}, nil
}

// Enabled reports whether the operator asked for OTLP export.
func Enabled() bool {
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return false
	}

	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != ""
}

// WrapTransport adds HTTP client spans. With a no-op provider this is cheap
// and keeps one code path for token and mTLS clients.
func WrapTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}

	return otelhttp.NewTransport(base, otelhttp.WithSpanNameFormatter(func(_ string, request *http.Request) string {
		return "agent.controlplane " + request.Method
	}))
}

// Tracer is the process tracer. Safe to call before Start (no-op provider).
func Tracer() trace.Tracer {
	return otel.Tracer(instrumentationName)
}

// RecordCommand counts a finished command by type and status. Cardinality is
// the closed command vocabulary times four statuses — never a domain, payload
// or customer identifier.
func RecordCommand(ctx context.Context, commandType, status string) {
	meter := otel.Meter(instrumentationName)
	counter, err := meter.Int64Counter("nubit.agent.commands")
	if err != nil {
		return
	}
	counter.Add(ctx, 1, metric.WithAttributes(
		attribute.String("nubit.command.type", commandType),
		attribute.String("nubit.command.status", status),
	))
}

// RecordPoll counts one control-plane poll and how many commands it returned.
func RecordPoll(ctx context.Context, commandCount int) {
	meter := otel.Meter(instrumentationName)
	polls, err := meter.Int64Counter("nubit.agent.polls")
	if err != nil {
		return
	}
	polls.Add(ctx, 1)
	fetched, err := meter.Int64Counter("nubit.agent.commands.fetched")
	if err != nil {
		return
	}
	fetched.Add(ctx, int64(commandCount))
}

// MarkError flags the span as failed without attaching the exception
// message — those can contain paths, hosts or provider text.
func MarkError(span trace.Span, err error) {
	if err == nil {
		return
	}
	span.SetAttributes(attribute.String("exception.type", fmt.Sprintf("%T", err)))
	span.SetStatus(codes.Error, "failed")
}
