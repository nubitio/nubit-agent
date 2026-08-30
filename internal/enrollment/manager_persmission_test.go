package enrollment

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEnrollCreatesConfigDirectoryWithRestrictivePermissions asserts that the
// first enrollment on a host that has no /etc/nubit-agent creates the
// directory with mode 0o750 — restrictive enough to keep the agent's private
// key away from accounts other than root and the nubit-agent group.
func TestEnrollCreatesConfigDirectoryWithRestrictivePermissions(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body enrollRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		block, _ := pem.Decode([]byte(body.CSR))
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil || csr.CheckSignature() != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		leaf := &x509.Certificate{
			SerialNumber: big.NewInt(2), Subject: csr.Subject,
			NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		leafDER, _ := x509.CreateCertificate(rand.Reader, leaf, caTemplate, csr.PublicKey, caKey)
		_ = json.NewEncoder(writer).Encode(EnrollResponse{
			Certificate:   string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})),
			CACertificate: string(caPEM),
		})
	}))
	defer server.Close()

	parent := t.TempDir()
	configDirectory := filepath.Join(parent, "nested", "agent")
	manager := Manager{Directory: configDirectory, StateDirectory: t.TempDir(), ControlURL: server.URL}
	if err := manager.Enroll(context.Background(), "one-time-token"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(configDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("expected config directory mode 0o750, got 0o%o", got)
	}
}

// TestEnrollRejectsHTTPSToNonLoopback guards against a future change that
// would let the manager leak the one-time enrollment token over a cleartext
// transport to a non-loopback host.
func TestEnrollRejectsHTTPSToNonLoopback(t *testing.T) {
	manager := Manager{Directory: t.TempDir(), StateDirectory: t.TempDir(), ControlURL: "http://control.example.com"}
	if err := manager.Enroll(context.Background(), "token"); err == nil {
		t.Fatal("expected an error when enrolling over plain HTTP to a non-loopback host")
	} else if !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("expected an HTTPS-related error, got: %v", err)
	}
}
