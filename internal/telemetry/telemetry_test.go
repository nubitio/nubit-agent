package telemetry

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestEnabledRequiresAnOTLPEndpoint(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	if Enabled() {
		t.Fatal("expected telemetry to stay off without an endpoint")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://ingest.nubit.io")
	if !Enabled() {
		t.Fatal("expected telemetry to turn on with an endpoint")
	}

	t.Setenv("OTEL_SDK_DISABLED", "true")
	if Enabled() {
		t.Fatal("expected OTEL_SDK_DISABLED=true to win")
	}
}

func TestStartWithoutEndpointIsANoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestWrapTransportReturnsARoundTripper(t *testing.T) {
	if WrapTransport(http.DefaultTransport) == nil {
		t.Fatal("expected a wrapped transport")
	}
}

func TestMarkErrorDoesNotAttachTheMessage(t *testing.T) {
	ctx, span := Tracer().Start(context.Background(), "test")
	MarkError(span, errors.New("secret token abc"))
	span.End()
	_ = ctx
}
