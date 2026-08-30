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
	"sync/atomic"
	"testing"
	"time"
)

// renewerFixture stands up a manager whose on-disk material is fully wired
// and whose server signs whatever CSR the renewer sends it. The CA and
// leaf factories are the same as the manager tests so the helper stays in
// one place.
type renewerFixture struct {
	directory      string
	stateDirectory string
	serverURL      string
	server         *httptest.Server
	hits           int32
	caPEM          []byte
}

func newRenewerFixture(t *testing.T, leafLifetime time.Duration) *renewerFixture {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	directory := t.TempDir()
	stateDirectory := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(directory, keyFile), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(directory, caChainFile), caPEM, 0o644); err != nil {
		t.Fatal(err)
	}

	notAfter := time.Now().Add(leafLifetime)
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "agent-test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     notAfter,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caTemplate, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(directory, certificateFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), 0o644); err != nil {
		t.Fatal(err)
	}

	fixture := &renewerFixture{directory: directory, stateDirectory: stateDirectory, caPEM: caPEM}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&fixture.hits, 1)
		var body renewRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		block, _ := pem.Decode([]byte(body.CSR))
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			t.Errorf("parse csr: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := csr.CheckSignature(); err != nil {
			t.Errorf("csr signature: %v", err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		newLeaf := &x509.Certificate{
			SerialNumber: big.NewInt(3),
			Subject:      csr.Subject,
			NotBefore:    time.Now().Add(-time.Minute),
			NotAfter:     time.Now().Add(24 * time.Hour),
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		newLeafDER, err := x509.CreateCertificate(rand.Reader, newLeaf, caTemplate, csr.PublicKey, caKey)
		if err != nil {
			t.Errorf("create cert: %v", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(writer).Encode(EnrollResponse{
			Certificate:   string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: newLeafDER})),
			CACertificate: string(caPEM),
		})
	}))
	t.Cleanup(fixture.server.Close)
	fixture.serverURL = fixture.server.URL
	return fixture
}

func (fixture *renewerFixture) manager() Manager {
	return Manager{Directory: fixture.directory, StateDirectory: fixture.stateDirectory, ControlURL: fixture.serverURL}
}

// TestRenewerDoesNotRenewWhenCertIsFresh asserts that a renewer with a
// long-lived cert does not contact the server. The contract is "renew
// only when about to expire", and the test pins down the negative case
// so an off-by-one in NeedsRenewal cannot silently re-enroll on every
// tick.
func TestRenewerDoesNotRenewWhenCertIsFresh(t *testing.T) {
	fixture := newRenewerFixture(t, 30*24*time.Hour)

	renewer := Renewer{Manager: fixture.manager(), Interval: time.Hour, Before: 7 * 24 * time.Hour}
	renewer.pass(context.Background(), renewer.Before)

	if got := atomic.LoadInt32(&fixture.hits); got != 0 {
		t.Fatalf("expected zero requests against a fresh cert, got %d", got)
	}
	expiry, err := fixture.manager().CertificateExpiry()
	if err != nil {
		t.Fatal(err)
	}
	if !expiry.After(time.Now().Add(20 * 24 * time.Hour)) {
		t.Fatalf("expected the original long-lived cert to be preserved, expiry=%s", expiry)
	}
}

// TestRenewerRenewsWhenCertExpiresSoon asserts that the renewer does call
// the server when the on-disk cert is inside the renewal window, and that
// the resulting cert has the longer lifetime the server issues. The key on
// disk is preserved — the spec says renewal re-signs, not re-keys.
func TestRenewerRenewsWhenCertExpiresSoon(t *testing.T) {
	fixture := newRenewerFixture(t, time.Hour)

	originalKey, err := readKeyFile(fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	originalExpiry, err := fixture.manager().CertificateExpiry()
	if err != nil {
		t.Fatal(err)
	}

	renewer := Renewer{Manager: fixture.manager(), Interval: time.Hour, Before: 7 * 24 * time.Hour}
	renewer.pass(context.Background(), renewer.Before)

	if got := atomic.LoadInt32(&fixture.hits); got == 0 {
		t.Fatalf("expected the renewer to hit the server for an expiring cert, got %d hits", got)
	}
	newExpiry, err := fixture.manager().CertificateExpiry()
	if err != nil {
		t.Fatal(err)
	}
	if !newExpiry.After(originalExpiry.Add(20 * time.Hour)) {
		t.Fatalf("expected new expiry %s to be far past the original %s", newExpiry, originalExpiry)
	}
	currentKey, err := readKeyFile(fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	if !bytesEqual(originalKey, currentKey) {
		t.Fatal("renewer must reuse the on-disk private key, not replace it")
	}
}

// TestRenewerSurvivesFailedRenewal asserts that a renewer whose server is
// broken keeps the existing cert rather than wiping it. Operators expect
// "the cert is wrong, the agent is broken" to degrade to "the cert is
// stale but the agent keeps polling with the old cert", not to a complete
// outage. The test pins the behavior: a failed renewal is logged, the
// loop survives, and the on-disk material is unchanged.
func TestRenewerSurvivesFailedRenewal(t *testing.T) {
	fixture := newRenewerFixture(t, time.Hour)

	// Swap the renewer into a manager pointing at a server that always
	// returns 502 so every renewal attempt fails.
	broken := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer broken.Close()
	manager := fixture.manager()
	manager.ControlURL = broken.URL

	originalCert, err := readCertFile(fixture.directory)
	if err != nil {
		t.Fatal(err)
	}

	renewer := Renewer{Manager: manager, Interval: time.Hour, Before: 7 * 24 * time.Hour}
	renewer.pass(context.Background(), renewer.Before)
	renewer.pass(context.Background(), renewer.Before)
	renewer.pass(context.Background(), renewer.Before)

	currentCert, err := readCertFile(fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	if !bytesEqual(originalCert, currentCert) {
		t.Fatal("failed renewals must not mutate the on-disk certificate")
	}
	if _, err := fixture.manager().VerifyCertificate(); err != nil {
		t.Fatalf("on-disk cert must still verify after failed renewals: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.directory, keyFile)); err != nil {
		t.Fatalf("key file must still be present: %v", err)
	}
}

// readKeyFile returns the on-disk PEM-encoded agent key. Used by the renewer
// tests to assert that the key is preserved across renewals.
func readKeyFile(directory string) ([]byte, error) {
	return os.ReadFile(filepath.Join(directory, keyFile))
}

// readCertFile returns the on-disk PEM-encoded agent certificate.
func readCertFile(directory string) ([]byte, error) {
	return os.ReadFile(filepath.Join(directory, certificateFile))
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
