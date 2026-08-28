package command

import (
	"encoding/json"
	"testing"

	"github.com/nubitio/nubit-agent/internal/tls"
)

type fakeInspector struct {
	evidence tls.Evidence
	err      error
}

func (inspector fakeInspector) Inspect(string) (tls.Evidence, error) {
	return inspector.evidence, inspector.err
}

func issuedEvidence() tls.Evidence {
	return tls.Evidence{
		SiteID:      "example.pe",
		Domains:     []string{"example.pe", "www.example.pe"},
		Issuer:      "Test Issuer X1",
		Fingerprint: "sha256:" + "ab12cd34" + "00000000000000000000000000000000000000000000000000000000",
		NotBefore:   "2026-08-01T00:00:00Z",
		ExpiresAt:   "2026-11-01T00:00:00Z",
		Manager:     "caddy_automatic_tls",
		Status:      "active",
	}
}

func TestExecutorReportsCertificateEvidenceWithoutSecrets(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeInspector{evidence: issuedEvidence()})
	result, err := executor.Execute(Command{
		ID: "cmd_tls", Type: TLSLetsEncryptEnable, Version: 1, IdempotencyKey: "tls:1",
		Payload: []byte(`{"siteId":"example.pe","domains":["example.pe"],"challengeType":"http-01"}`),
	})
	if err != nil {
		t.Fatalf("expected TLS command to succeed: %v", err)
	}

	var output map[string]any
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}

	// The control plane allowlists `privateKeyStored`. Reporting it under any
	// other name means the sanitizer drops it and the evidence silently loses
	// the one field that says no key was transferred.
	if output["privateKeyStored"] != false {
		t.Fatalf("expected the output to state no private key was stored: %#v", output)
	}
	for _, key := range []string{"issuer", "fingerprint", "domains", "expiresAt"} {
		if _, present := output[key]; !present {
			t.Fatalf("evidence is missing %q, which the control plane requires: %#v", key, output)
		}
	}
	if output["challengeType"] != "http-01" {
		t.Fatalf("the challenge type was not reported: %#v", output)
	}
}

// A site whose domain has not resolved yet has no certificate. That is the
// normal state right after creation, so it is reported rather than failed —
// a job that failed here would be retried forever.
func TestNoCertificateYetIsReportedAsAState(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeInspector{err: tls.ErrNoCertificate})
	result, err := executor.Execute(Command{
		ID: "cmd_tls_pending", Type: TLSLetsEncryptEnable, Version: 1, IdempotencyKey: "tls:pending",
		Payload: []byte(`{"siteId":"example.pe","domains":["example.pe"],"challengeType":"http-01"}`),
	})
	if err != nil {
		t.Fatalf("a site without a certificate should not fail the command: %v", err)
	}

	var output map[string]any
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output["status"] != "pending_certificate" {
		t.Fatalf("unexpected status: %#v", output)
	}
	if output["privateKeyStored"] != false {
		t.Fatalf("even the pending report must state no key was stored: %#v", output)
	}
}

func TestInspectNamesOnlyTheSite(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeInspector{evidence: issuedEvidence()})
	result, err := executor.Execute(Command{
		ID: "cmd_tls_inspect", Type: TLSCertificateInspect, Version: 1, IdempotencyKey: "tls:inspect",
		Payload: []byte(`{"siteId":"example.pe"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	var output map[string]any
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	// What the certificate covers is read off it, not restated by the caller.
	if _, present := output["challengeType"]; present {
		t.Fatalf("inspecting should not report a challenge type: %#v", output)
	}
	if output["issuer"] != "Test Issuer X1" {
		t.Fatalf("the issuer was not reported: %#v", output)
	}
}

func TestExecutorRejectsUnsupportedTLSChallenge(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeInspector{evidence: issuedEvidence()})
	_, err := executor.Execute(Command{
		ID: "cmd_tls", Type: TLSLetsEncryptEnable, Version: 1, IdempotencyKey: "tls:2",
		Payload: []byte(`{"siteId":"example.pe","domains":["example.pe"],"challengeType":"tls-alpn-01"}`),
	})
	if err == nil {
		t.Fatal("expected unsupported challenge to fail")
	}
}

func TestExecutorRejectsDNS01UntilAcmeMutationIsImplemented(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeInspector{evidence: issuedEvidence()})
	_, err := executor.Execute(Command{
		ID: "cmd_tls_dns", Type: TLSLetsEncryptEnable, Version: 1, IdempotencyKey: "tls:3",
		Payload: []byte(`{"siteId":"example.pe","domains":["example.pe"],"challengeType":"dns-01"}`),
	})
	if err == nil {
		t.Fatal("expected dns-01 challenge to fail until ACME DNS mutation is implemented")
	}
}

func TestTLSCommandsNeedAnInspector(t *testing.T) {
	executor := NewExecutor(NewMemoryStore())
	_, err := executor.Execute(Command{
		ID: "cmd_tls_unwired", Type: TLSCertificateInspect, Version: 1, IdempotencyKey: "tls:4",
		Payload: []byte(`{"siteId":"example.pe"}`),
	})
	if err == nil {
		t.Fatal("an unconfigured inspector reported success")
	}
}
