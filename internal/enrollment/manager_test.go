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
		_ = json.NewEncoder(writer).Encode(enrollResponse{Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})), CACertificate: string(caPEM)})
	}))
	defer server.Close()

	directory := t.TempDir()
	manager := Manager{Directory: directory, ControlURL: server.URL}
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
}
