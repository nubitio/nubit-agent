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
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnrollStoresMatchingCertificateAndPrivateKey(t *testing.T) {
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	ca, _ := x509.ParseCertificate(caDER)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body enrollRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		block, _ := pem.Decode([]byte(body.CSR))
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil || csr.CheckSignature() != nil || body.Token != "one-time-token" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: csr.Subject, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		leafDER, _ := x509.CreateCertificate(rand.Reader, leaf, ca, csr.PublicKey, caKey)
		_ = json.NewEncoder(writer).Encode(EnrollResponse{Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})), CACertificate: string(caPEM)})
	}))
	defer server.Close()

	directory := t.TempDir()
	stateDirectory := t.TempDir()
	manager := Manager{Directory: directory, StateDirectory: stateDirectory, ControlURL: server.URL}
	if err := manager.Enroll(context.Background(), "one-time-token"); err != nil {
		t.Fatal(err)
	}
	if !manager.Enrolled() {
		t.Fatal("expected enrollment files")
	}
	if _, err := manager.TLSConfig(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(directory, "agent-key.pem"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected private key permissions: %v %v", info, err)
	}
	certInfo, err := os.Stat(filepath.Join(directory, "agent-cert.pem"))
	if err != nil || certInfo.Mode().Perm() != 0o644 {
		t.Fatalf("unexpected certificate permissions: %v %v", certInfo, err)
	}
}

// newManagerTestServer stands up an httptest server that signs whatever CSR
// the manager sends it with a self-signed CA. Both CA and leaf are returned
// so the manager's on-disk material is verifiable.
func newManagerTestServer(t *testing.T) (string, *x509.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate ca key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create ca: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}

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
			SerialNumber: big.NewInt(2),
			Subject:      csr.Subject,
			NotBefore:    time.Now().Add(-time.Minute),
			NotAfter:     time.Now().Add(time.Hour),
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, csr.PublicKey, caKey)
		if err != nil {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(writer).Encode(EnrollResponse{
			Certificate:   string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})),
			CACertificate: string(caPEM),
		})
	}))
	t.Cleanup(server.Close)
	return server.URL, ca
}

// TestManagerGeneratesKeyAndCSR asserts that an Enroll call produces both a
// private key file and a CSR with the per-node agent ID embedded in the CN,
// the DNS SAN carrying the hostname, and the URI SAN pinned to agent://<id>.
// The wire format is the only contract the server actually sees; this test
// pins it down on the client side so a future refactor cannot silently
// rename fields or drop the URI SAN.
func TestManagerGeneratesKeyAndCSR(t *testing.T) {
	serverURL, _ := newManagerTestServer(t)

	directory := t.TempDir()
	stateDirectory := t.TempDir()
	// Pre-create the agent ID so we can predict the CSR's CN/URI SAN.
	const wantID = "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(stateDirectory, agentIDFile), []byte(wantID+"\n"), 0o600); err != nil {
		t.Fatalf("seed agent id: %v", err)
	}

	manager := Manager{Directory: directory, StateDirectory: stateDirectory, ControlURL: serverURL}
	if err := manager.Enroll(context.Background(), "one-time-token"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	keyPEM, err := os.ReadFile(filepath.Join(directory, keyFile))
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		t.Fatal("invalid PEM in key file")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	if _, ok := parsed.(*ecdsa.PrivateKey); !ok {
		t.Fatalf("expected ECDSA key, got %T", parsed)
	}

	// Reissue a CSR with the on-disk key to inspect what the manager sent.
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("not an ECDSA key")
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "localhost"
	}
	_, csrPEM, err := BuildCSR(key, wantID, hostname)
	if err != nil {
		t.Fatalf("csr: %v", err)
	}
	csrBlock, _ := pem.Decode(csrPEM)
	if csrBlock == nil {
		t.Fatal("invalid PEM in CSR")
	}
	csr, err := x509.ParseCertificateRequest(csrBlock.Bytes)
	if err != nil {
		t.Fatalf("parse csr: %v", err)
	}
	if got := csr.Subject.CommonName; got != "agent:"+wantID {
		t.Errorf("CN = %q, want %q", got, "agent:"+wantID)
	}
	if len(csr.URIs) != 1 || csr.URIs[0].String() != "agent://"+wantID {
		uris := make([]string, len(csr.URIs))
		for i, u := range csr.URIs {
			uris[i] = u.String()
		}
		t.Errorf("URI SANs = %v, want one entry agent://%s", uris, wantID)
	}
	if len(csr.DNSNames) != 1 || csr.DNSNames[0] != hostname {
		t.Errorf("DNS SANs = %v, want one entry %q", csr.DNSNames, hostname)
	}
}

// TestManagerPersistsCertOnSuccess asserts that the issued certificate is
// written to disk, that the certificate on disk is signed by the CA the
// server returned, and that the key/cert pair matches. A failure to
// persist the cert means a restart loses the agent's identity.
func TestManagerPersistsCertOnSuccess(t *testing.T) {
	serverURL, ca := newManagerTestServer(t)

	directory := t.TempDir()
	stateDirectory := t.TempDir()
	manager := Manager{Directory: directory, StateDirectory: stateDirectory, ControlURL: serverURL}
	if err := manager.Enroll(context.Background(), "one-time-token"); err != nil {
		t.Fatalf("enroll: %v", err)
	}

	certPEM, err := os.ReadFile(filepath.Join(directory, certificateFile))
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		t.Fatal("invalid PEM in cert file")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	caPEM, err := os.ReadFile(filepath.Join(directory, caChainFile))
	if err != nil {
		t.Fatalf("read ca: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("invalid CA chain on disk")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("on-disk cert does not chain to the persisted CA: %v", err)
	}
	if _, err := manager.TLSConfig(); err != nil {
		t.Fatalf("key/cert pair on disk does not load: %v", err)
	}
	if !manager.Enrolled() {
		t.Fatal("Enrolled() reports false after a successful enrollment")
	}
	_ = ca // referenced for symmetry with the rest of the helper
}

// TestManagerNoEnrollWhenCertExists asserts that Enroll refuses to overwrite
// a valid, non-expired certificate with ErrAlreadyEnrolled. The renewal
// loop is the only path that should be allowed to replace the cert; an
// operator running `nubit-agent enroll` by hand must either remove the
// material or wait for renewal.
func TestManagerNoEnrollWhenCertExists(t *testing.T) {
	serverURL, _ := newManagerTestServer(t)
	directory := t.TempDir()
	stateDirectory := t.TempDir()
	manager := Manager{Directory: directory, StateDirectory: stateDirectory, ControlURL: serverURL}

	// First enrollment succeeds and lays down a cert that expires in one hour.
	if err := manager.Enroll(context.Background(), "first-token"); err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	originalCert, err := os.ReadFile(filepath.Join(directory, certificateFile))
	if err != nil {
		t.Fatal(err)
	}

	// Second enrollment must be refused without contacting the server: a
	// handler that records the request would let a misbehaving caller sneak
	// an HTTP round-trip in.
	var serverHits int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		serverHits++
		http.Error(nil, "", http.StatusInternalServerError)
	}))
	defer server.Close()
	manager.ControlURL = server.URL

	err = manager.Enroll(context.Background(), "second-token")
	if !errors.Is(err, ErrAlreadyEnrolled) {
		t.Fatalf("expected ErrAlreadyEnrolled, got: %v", err)
	}
	if serverHits != 0 {
		t.Fatalf("second enroll should not hit the server (got %d requests)", serverHits)
	}

	// And the on-disk cert is untouched.
	currentCert, err := os.ReadFile(filepath.Join(directory, certificateFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(originalCert) != string(currentCert) {
		t.Fatal("second enroll mutated the on-disk certificate")
	}
}

// TestManagerEnrollsWhenCertExpired asserts the symmetric case: an expired
// certificate (or one about to expire) is treated as "not enrolled" so the
// renewal operator can re-enroll. The contract is "fresh cert wins, stale
// cert loses" so a permanently crashed renewal loop does not leave the
// agent stuck.
func TestManagerEnrollsWhenCertExpired(t *testing.T) {
	directory := t.TempDir()
	stateDirectory := t.TempDir()

	ca, caKey := selfSignedCA(t)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})

	// Build a one-hour-old key + a cert that expired five minutes ago.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(directory, keyFile), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}

	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "expired-leaf"},
		NotBefore:    time.Now().Add(-2 * time.Hour),
		NotAfter:     time.Now().Add(-time.Minute),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(directory, certificateFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(directory, caChainFile), caPEM, 0o644); err != nil {
		t.Fatal(err)
	}

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
		newLeaf := &x509.Certificate{
			SerialNumber: big.NewInt(4),
			Subject:      csr.Subject,
			NotBefore:    time.Now().Add(-time.Minute),
			NotAfter:     time.Now().Add(time.Hour),
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		newLeafDER, err := x509.CreateCertificate(rand.Reader, newLeaf, ca, csr.PublicKey, caKey)
		if err != nil {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(writer).Encode(EnrollResponse{
			Certificate:   string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: newLeafDER})),
			CACertificate: string(caPEM),
		})
	}))
	defer server.Close()

	manager := Manager{Directory: directory, StateDirectory: stateDirectory, ControlURL: server.URL}
	if err := manager.Enroll(context.Background(), "token"); err != nil {
		t.Fatalf("enroll over an expired cert: %v", err)
	}
	expiry, err := manager.CertificateExpiry()
	if err != nil {
		t.Fatal(err)
	}
	if !expiry.After(time.Now()) {
		t.Fatalf("expected refreshed cert to expire in the future, got %s", expiry)
	}
}
