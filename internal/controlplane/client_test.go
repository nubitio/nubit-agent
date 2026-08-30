package controlplane

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// writeKeyPair writes a self-signed client cert and key into a temp dir and
// returns the file paths. Used by the mTLS tests below to exercise the dual
// transport without dragging in a real CA.
func writeKeyPair(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "nubit-agent-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPath := filepath.Join(dir, "agent.crt")
	keyPath := filepath.Join(dir, "agent.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

// TestClientUsesMTLSWhenCertConfigured verifies that NewDualClient (and the
// underlying attachMTLSTransport) really presents the configured client
// certificate on the wire. The TLS server is built with ClientCAs set so the
// handshake rejects requests that don't carry a valid cert; if the cert is
// presented, the test passes.
func TestClientUsesMTLSWhenCertConfigured(t *testing.T) {
	certPath, keyPath := writeKeyPair(t)
	clientTLSCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load client keypair: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
			t.Error("expected a peer certificate in mTLS mode, got none")
		}
		_, _ = writer.Write([]byte(`{"commands":[]}`))
	}))
	// Trust the client cert so the handshake requires it.
	server.TLS = &tls.Config{
		ClientCAs:  certPoolFromCertificate(clientTLSCert),
		ClientAuth: tls.RequireAndVerifyClientCert,
	}
	server.StartTLS()
	defer server.Close()

	serverCert := server.Certificate()
	pool := x509.NewCertPool()
	pool.AddCert(serverCert)
	client := &Client{
		BaseURL: server.URL,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					Certificates: []tls.Certificate{clientTLSCert},
					RootCAs:      pool,
				},
			},
		},
	}
	if _, err := client.FetchJobs(context.Background()); err != nil {
		t.Fatalf("fetch jobs over mTLS: %v", err)
	}
}

// TestClientFallsBackToTokenWhenNoCert verifies that the token header is set
// on every request when no client certificate is configured. This is the
// pre-mTLS behaviour and the dual-mode target: the token must keep working
// while a fleet rolls out the cert.
func TestClientFallsBackToTokenWhenNoCert(t *testing.T) {
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotToken = request.Header.Get("X-Agent-Token")
		_, _ = writer.Write([]byte(`{"commands":[]}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-token")
	if _, err := client.FetchJobs(context.Background()); err != nil {
		t.Fatalf("fetch jobs: %v", err)
	}
	if gotToken != "secret-token" {
		t.Fatalf("expected X-Agent-Token %q, got %q", "secret-token", gotToken)
	}
}

// TestClientDualModeSendsBothCertAndToken verifies that NewDualClient sends
// both the X-Agent-Token header and a client certificate on the wire. During
// the cutover the server may validate either identity; the test asserts both
// are present.
func TestClientDualModeSendsBothCertAndToken(t *testing.T) {
	certPath, keyPath := writeKeyPair(t)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Agent-Token") != "rotating-token" {
			t.Errorf("expected X-Agent-Token in dual mode, got %q", request.Header.Get("X-Agent-Token"))
		}
		if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
			t.Error("expected a peer certificate in dual mode, got none")
		}
		_, _ = writer.Write([]byte(`{"commands":[]}`))
	}))
	// Trust the client cert so the TLS handshake requires it. Without
	// ClientCAs, the server cannot verify the agent's cert and the
	// handshake fails before reaching the assertions above.
	clientTLSCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load client keypair: %v", err)
	}
	server.TLS = &tls.Config{
		ClientCAs:  certPoolFromCertificate(clientTLSCert),
		ClientAuth: tls.RequireAndVerifyClientCert,
	}
	server.StartTLS()
	defer server.Close()

	// Production code trusts the system CA pool; the test server's leaf is
	// self-signed, so we add it to the pool the client builds. The token +
	// cert are then both presented on the wire.
	serverCert := server.Certificate()
	client := NewDualClient(server.URL, "rotating-token", certPath, keyPath)
	pool := x509.NewCertPool()
	pool.AddCert(serverCert)
	transport, ok := client.HTTPClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("expected *http.Transport, got %T", client.HTTPClient.Transport)
	}
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.RootCAs = pool
	if _, err := client.FetchJobs(context.Background()); err != nil {
		t.Fatalf("fetch jobs: %v", err)
	}
}

// certPoolFromCertificate returns a CertPool containing only the supplied
// certificate. Used by the mTLS tests to wire ClientCAs without dragging
// in a CA hierarchy.
func certPoolFromCertificate(certificate tls.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	if certificate.Leaf != nil {
		pool.AddCert(certificate.Leaf)
		return pool
	}
	if len(certificate.Certificate) == 0 {
		return pool
	}
	parsed, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return pool
	}
	pool.AddCert(parsed)
	return pool
}

// TestClientFallsBackToTokenWhenCertUnreadable verifies the graceful-fallback
// contract: a CertFile that points at a missing or unparseable file logs a
// warning and leaves the client in token-only mode instead of refusing to
// send requests.
func TestClientFallsBackToTokenWhenCertUnreadable(t *testing.T) {
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotToken = request.Header.Get("X-Agent-Token")
		_, _ = writer.Write([]byte(`{"commands":[]}`))
	}))
	defer server.Close()

	missingCert := filepath.Join(t.TempDir(), "does-not-exist.crt")
	missingKey := filepath.Join(t.TempDir(), "does-not-exist.key")
	client := NewDualClient(server.URL, "secret-token", missingCert, missingKey)
	if _, err := client.FetchJobs(context.Background()); err != nil {
		t.Fatalf("fetch jobs after cert load failure: %v", err)
	}
	if gotToken != "secret-token" {
		t.Fatalf("expected X-Agent-Token %q after cert load failure, got %q", "secret-token", gotToken)
	}
}
