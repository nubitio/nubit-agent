package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchJobsSendsTheAgentTokenAndParsesCommands(t *testing.T) {
	var gotToken, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotToken = request.Header.Get("X-Agent-Token")
		gotPath = request.URL.Path
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"commands":[{"id":"1","type":"system.ping","version":1,"idempotencyKey":"k1","payload":{}}]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-token")
	commands, err := client.FetchJobs(context.Background())
	if err != nil {
		t.Fatalf("fetch jobs: %v", err)
	}

	if "secret-token" != gotToken {
		t.Fatalf("expected X-Agent-Token %q, got %q", "secret-token", gotToken)
	}
	if "/api/agent/jobs" != gotPath {
		t.Fatalf("expected path /api/agent/jobs, got %q", gotPath)
	}
	if 1 != len(commands) || "system.ping" != commands[0].Type {
		t.Fatalf("unexpected commands: %#v", commands)
	}
}

func TestFetchJobsFailsOnNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewClient(server.URL, "wrong-token")
	if _, err := client.FetchJobs(context.Background()); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
}

func TestFetchJobsRejectsPlainHTTPOutsideLoopback(t *testing.T) {
	t.Setenv("NUBIT_CONTROL_ALLOW_HTTP", "")
	client := NewClient("http://control.example.com", "secret-token")
	if _, err := client.FetchJobs(context.Background()); err == nil {
		t.Fatal("expected insecure control-plane URL to be rejected")
	}
}

func TestValidateEndpointAllowsComposeHTTPWhenOptedIn(t *testing.T) {
	client := NewClient("http://app", "secret-token")
	t.Setenv("NUBIT_CONTROL_ALLOW_HTTP", "")
	if err := client.validateEndpoint(); err == nil {
		t.Fatal("expected http://app to be rejected without NUBIT_CONTROL_ALLOW_HTTP")
	}
	t.Setenv("NUBIT_CONTROL_ALLOW_HTTP", "1")
	if err := client.validateEndpoint(); err != nil {
		t.Fatalf("expected http://app when opted in: %v", err)
	}
}

func TestReportResultSendsStatusOutputAndError(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotPath = request.URL.Path
		_ = json.NewDecoder(request.Body).Decode(&gotBody)
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	execErr := errors.New("execution failed")
	client := NewClient(server.URL, "secret-token")
	err := client.ReportResult(context.Background(), "42", "failed", nil, execErr)
	if err != nil {
		t.Fatalf("report result: %v", err)
	}

	if "/api/agent/jobs/42/result" != gotPath {
		t.Fatalf("expected path /api/agent/jobs/42/result, got %q", gotPath)
	}
	if "failed" != gotBody["status"] || execErr.Error() != gotBody["error"] {
		t.Fatalf("unexpected body: %#v", gotBody)
	}
}
