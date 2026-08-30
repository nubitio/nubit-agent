package enrollment

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
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// startTLSServer brings up an httptest server over TLS with an in-memory
// client CA pool. When requireCert is true the handshake refuses requests
// that don't present a certificate that chains to the supplied CA — that's
// how the mTLS-only enroll endpoint is supposed to behave in production, and
// how the rejection tests below exercise a real TLS failure (rather than
// faking it through an http.Handler shortcut).
func startTLSServer(t *testing.T, handler http.Handler, ca *x509.Certificate, requireCert bool) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	server.TLS = &tls.Config{
		ClientCAs:  pool,
		ClientAuth: conditionalClientAuth(requireCert),
		MinVersion: tls.VersionTLS12,
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func conditionalClientAuth(require bool) tls.ClientAuthType {
	if require {
		return tls.RequireAndVerifyClientCert
	}
	return tls.NoClientCert
}

// newClientForServer builds an enrollment Client that trusts the test
// server's certificate, mirroring production behavior when an operator pins
// NUBIT_STEPCA_ROOT_CERT_PATH.
func newClientForServer(t *testing.T, server *httptest.Server, token string) *Client {
	t.Helper()
	serverCert := server.Certificate()
	pool := x509.NewCertPool()
	pool.AddCert(serverCert)
	return &Client{
		BaseURL: server.URL,
		Token:   token,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			},
		},
	}
}

func selfSignedCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ca key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "nubit-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create ca cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse ca cert: %v", err)
	}
	return cert, key
}

func issueLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// TestEnrollSendsValidRequest asserts the wire format of an enrollment POST:
// the JSON body carries token, csr, agentId under the names Nubit Control
// expects; the path is /api/agent/enroll; the content-type is JSON. The TLS
// server is configured with ClientCAs that trust the CA, so a self-signed
// server is accepted by the in-memory client.
func TestEnrollSendsValidRequest(t *testing.T) {
	ca, caKey := selfSignedCA(t)
	var received struct {
		Token   string `json:"token"`
		CSR     string `json:"csr"`
		AgentID string `json:"agentId"`
	}
	server := startTLSServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/agent/enroll" {
			t.Errorf("unexpected path %q", request.URL.Path)
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("unexpected content-type %q", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		leaf := issueLeaf(t, ca, caKey, "test-agent")
		caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}))
		_ = json.NewEncoder(writer).Encode(EnrollResponse{Certificate: leaf, CACertificate: caPEM})
	}), ca, false)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, csrPEM, err := BuildCSR(key, "00112233445566778899aabbccddeeff", "test-host")
	if err != nil {
		t.Fatal(err)
	}

	client := newClientForServer(t, server, "one-time-token")
	if _, err := client.Enroll(context.Background(), string(csrPEM), "00112233445566778899aabbccddeeff"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	if received.Token != "one-time-token" {
		t.Errorf("expected token one-time-token, got %q", received.Token)
	}
	if received.CSR != string(csrPEM) {
		t.Errorf("expected CSR to match the one sent")
	}
	if received.AgentID != "00112233445566778899aabbccddeeff" {
		t.Errorf("unexpected agentId %q", received.AgentID)
	}
}

// TestEnrollRejectsUntrustedCA asserts that pinning RootCAPEM (the production
// path through NUBIT_STEPCA_ROOT_CERT_PATH) causes the client to refuse a
// server whose certificate does not chain to that root. The error has to
// surface as a TLS handshake failure so operators see it as "wrong CA",
// not "enrollment endpoint unreachable".
func TestEnrollRejectsUntrustedCA(t *testing.T) {
	ca, _ := selfSignedCA(t)
	untrustedKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(99),
		Subject:      pkix.Name{CommonName: "spoofed-control"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &untrustedKey.PublicKey, untrustedKey)
	if err != nil {
		t.Fatalf("create untrusted server cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse untrusted server cert: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: untrustedKey, Leaf: cert}}}
	server.StartTLS()
	defer server.Close()

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	client := &Client{
		BaseURL: server.URL,
		Token:   "token",
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: mustPool(t, caPEM), MinVersion: tls.VersionTLS12},
			},
		},
	}
	_, err = client.Enroll(context.Background(), "-----BEGIN CERTIFICATE REQUEST-----\n-----END CERTIFICATE REQUEST-----", "00112233445566778899aabbccddeeff")
	if err == nil {
		t.Fatal("expected an error from a server with an untrusted cert")
	}
	if !strings.Contains(err.Error(), "tls:") && !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "x509") {
		t.Fatalf("expected a TLS trust error, got: %v", err)
	}
}

// TestEnrollParsesSuccessResponse confirms the JSON returned from a 200
// enroll call is decoded into the typed EnrollResponse: certificate, CA
// material, serial and expiry come back populated and the PEM blocks parse.
func TestEnrollParsesSuccessResponse(t *testing.T) {
	ca, caKey := selfSignedCA(t)
	serialNumber := big.NewInt(42)
	expiry := time.Now().Add(2 * time.Hour).Truncate(time.Second).UTC()

	var captured []byte
	server := startTLSServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate leaf key: %v", err)
		}
		template := &x509.Certificate{
			SerialNumber: serialNumber,
			Subject:      pkix.Name{CommonName: "test-agent"},
			NotBefore:    time.Now().Add(-time.Minute),
			NotAfter:     expiry,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("create leaf: %v", err)
		}
		captured = der
		caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}))
		leaf := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
		_ = json.NewEncoder(writer).Encode(EnrollResponse{
			Certificate:   leaf,
			CACertificate: caPEM,
			Serial:        "2a",
			ExpiresAt:     expiry,
		})
	}), ca, false)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, csrPEM, err := BuildCSR(key, "00112233445566778899aabbccddeeff", "test-host")
	if err != nil {
		t.Fatal(err)
	}

	client := newClientForServer(t, server, "token")
	response, err := client.Enroll(context.Background(), string(csrPEM), "00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if response.Certificate == "" {
		t.Fatal("missing certificate")
	}
	if response.CACertificate == "" {
		t.Fatal("missing CA certificate")
	}
	if response.ExpiresAt.IsZero() {
		t.Error("ExpiresAt was not populated")
	}
	if !response.ExpiresAt.Equal(expiry) {
		t.Errorf("ExpiresAt = %v, want %v", response.ExpiresAt, expiry)
	}
	block, _ := pem.Decode([]byte(response.Certificate))
	if block == nil {
		t.Fatal("response certificate is not valid PEM")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse response certificate: %v", err)
	}
	if parsed.SerialNumber.Cmp(serialNumber) != 0 {
		t.Errorf("serial = %v, want %v", parsed.SerialNumber, serialNumber)
	}
	if string(captured) != string(block.Bytes) {
		t.Errorf("decoded bytes mismatch: server %x, client %x", captured[:8], block.Bytes[:8])
	}
}

// TestEnrollReturnsErrorOn401 asserts that an unauthorized response from
// Nubit Control surfaces as a non-nil error mentioning the status code, so
// operators see "401" and not a generic "bad response".
func TestEnrollReturnsErrorOn401(t *testing.T) {
	ca, _ := selfSignedCA(t)
	server := startTLSServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":"invalid token"}`))
	}), ca, false)

	client := newClientForServer(t, server, "expired-token")
	_, err := client.Enroll(context.Background(), "-----BEGIN CERTIFICATE REQUEST-----\n-----END CERTIFICATE REQUEST-----", "00112233445566778899aabbccddeeff")
	if err == nil {
		t.Fatal("expected an error on a 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid token") {
		t.Errorf("expected server body excerpt in error, got: %v", err)
	}
}

// TestEnrollReturnsErrorOn502 asserts that an upstream outage (a 502 from a
// proxy in front of Nubit Control) returns an error rather than a
// half-decoded EnrollResponse. A success path with an empty body is the
// failure mode that would otherwise mask the outage.
func TestEnrollReturnsErrorOn502(t *testing.T) {
	ca, _ := selfSignedCA(t)
	server := startTLSServer(t, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte(`upstream timeout`))
	}), ca, false)

	client := newClientForServer(t, server, "token")
	_, err := client.Enroll(context.Background(), "-----BEGIN CERTIFICATE REQUEST-----\n-----END CERTIFICATE REQUEST-----", "00112233445566778899aabbccddeeff")
	if err == nil {
		t.Fatal("expected an error on a 502")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("expected 502 in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "upstream timeout") {
		t.Errorf("expected upstream body excerpt in error, got: %v", err)
	}
}

func mustPool(t *testing.T, pemBytes []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatalf("could not parse PEM into a pool")
	}
	return pool
}
