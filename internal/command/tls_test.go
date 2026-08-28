package command

import (
	"encoding/json"
	"strings"
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

// A site whose domain has not resolved yet has no certificate. When the
// caller is inspecting, that is the normal state right after creation, so it
// is reported rather than failed — a job that failed here would be retried
// forever. When the caller is enabling, the absence is a failure: the agent
// does not drive ACME itself and Caddy has not produced one yet, so the
// command has to be marked failed with an explicit message instead of left
// in a silent "pending" state.
func TestNoCertificateYetIsReportedAsAStateForInspect(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeInspector{err: tls.ErrNoCertificate})
	result, err := executor.Execute(Command{
		ID: "cmd_tls_pending", Type: TLSCertificateInspect, Version: 1, IdempotencyKey: "tls:pending",
		Payload: []byte(`{"siteId":"example.pe"}`),
	})
	if err != nil {
		t.Fatalf("inspecting without a certificate should not fail the command: %v", err)
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

// Enabling Let's Encrypt is the agent telling Caddy "go get a certificate."
// The agent itself does not run ACME; if Caddy has not produced one yet the
// command must say so explicitly rather than report a silent pending state
// that the control plane would treat as still running.
func TestExecuteTLSLetsEncryptEnableWithoutCertReturnsExplicitError(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeInspector{err: tls.ErrNoCertificate})
	_, err := executor.Execute(Command{
		ID: "cmd_tls_enable_nocert", Type: TLSLetsEncryptEnable, Version: 1, IdempotencyKey: "tls:enable:nocert",
		Payload: []byte(`{"siteId":"example.pe","domains":["example.pe"],"challengeType":"http-01"}`),
	})
	if err == nil {
		t.Fatal("enabling with no certificate should fail with an explicit error")
	}
	message := err.Error()
	for _, want := range []string{
		"tls.letsencrypt.enable is not implemented",
		"No certificate found for example.pe",
		"ACME HTTP-01 is reachable",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("error message is missing %q: %s", want, message)
		}
	}
}

func TestExecuteTLSLetsEncryptEnableWithCertReturnsSuccess(t *testing.T) {
	executor := NewExecutor(NewMemoryStore(), fakeInspector{evidence: issuedEvidence()})
	result, err := executor.Execute(Command{
		ID: "cmd_tls_enable_ok", Type: TLSLetsEncryptEnable, Version: 1, IdempotencyKey: "tls:enable:ok",
		Payload: []byte(`{"siteId":"example.pe","domains":["example.pe"],"challengeType":"http-01"}`),
	})
	if err != nil {
		t.Fatalf("enabling with a valid certificate should succeed: %v", err)
	}
	if result.Status != "succeeded" {
		t.Fatalf("unexpected status: %q", result.Status)
	}
	var output map[string]any
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output["issuer"] != "Test Issuer X1" {
		t.Fatalf("the issuer was not reported: %v", output)
	}
	if output["challengeType"] != "http-01" {
		t.Fatalf("the requested challenge type was not echoed: %v", output)
	}
}

// The control plane cares which challenge was asked for. dns-01 is the most
// likely to be requested; the error must name the value and explain that only
// http-01 is wired up today.
func TestParseTLSLetsEncryptEnablePayloadRejectsDNS01WithMessage(t *testing.T) {
	_, err := parseTLSLetsEncrypt([]byte(`{"siteId":"example.pe","domains":["example.pe"],"challengeType":"dns-01"}`))
	if err == nil {
		t.Fatal("dns-01 should fail to parse")
	}
	message := err.Error()
	for _, want := range []string{
		"\"dns-01\"",
		"only http-01 is currently implemented",
		"ACME challenge mutation",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("error message is missing %q: %s", want, message)
		}
	}
}

func TestParseTLSLetsEncryptEnablePayloadRejectsTLSALPN01WithMessage(t *testing.T) {
	_, err := parseTLSLetsEncrypt([]byte(`{"siteId":"example.pe","domains":["example.pe"],"challengeType":"tls-alpn-01"}`))
	if err == nil {
		t.Fatal("tls-alpn-01 should fail to parse")
	}
	message := err.Error()
	for _, want := range []string{
		"\"tls-alpn-01\"",
		"only http-01 is currently implemented",
		"ACME challenge mutation",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("error message is missing %q: %s", want, message)
		}
	}
}
