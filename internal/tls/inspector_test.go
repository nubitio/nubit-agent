package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// caddyStorage lays out a directory the way Caddy does and drops a real
// certificate in it. Generating one rather than checking in a fixture keeps the
// test honest about expiry, which is half of what the evidence is for.
func caddyStorage(t *testing.T, domain string, names []string, notBefore, notAfter time.Time) string {
	t.Helper()

	// Signed by a separate CA rather than self-signed: x509 takes the issuer
	// from the parent's subject, so a self-signed certificate would name the
	// site as its own issuer and the assertion would prove nothing.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Issuer X1"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, &ca, &ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     names,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, issuer, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	// The issuer directory varies with the CA and the ACME endpoint, which is
	// why the inspector walks rather than assuming a path.
	dir := filepath.Join(root, "acme-v02.api.letsencrypt.org-directory", domain)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, domain+".crt"), pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	// A key sits beside it on a real host. Writing one here proves the
	// inspector walks past it rather than opening it.
	if err := os.WriteFile(filepath.Join(dir, domain+".key"), []byte("-----BEGIN PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return root
}

func TestInspectReportsWhatTheControlPlaneAudits(t *testing.T) {
	notBefore := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	notAfter := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	root := caddyStorage(t, "example.pe", []string{"example.pe", "www.example.pe"}, notBefore, notAfter)

	inspector := Inspector{StorageDir: root, Now: func() time.Time { return notBefore.AddDate(0, 1, 0) }}
	evidence, err := inspector.Inspect("example.pe")
	if err != nil {
		t.Fatal(err)
	}

	if evidence.Issuer != "Test Issuer X1" {
		t.Fatalf("wrong issuer: %q", evidence.Issuer)
	}
	// The control plane refuses a fingerprint that is not exactly this shape.
	if !strings.HasPrefix(evidence.Fingerprint, "sha256:") || len(evidence.Fingerprint) != len("sha256:")+64 {
		t.Fatalf("fingerprint is not sha256:<64 hex>: %q", evidence.Fingerprint)
	}
	if evidence.ExpiresAt != "2026-11-01T00:00:00Z" {
		t.Fatalf("wrong expiry: %q", evidence.ExpiresAt)
	}
	if evidence.Status != "active" {
		t.Fatalf("a valid certificate was not reported active: %q", evidence.Status)
	}
	if evidence.PrivateKeyStored {
		t.Fatal("the agent must never report a stored private key")
	}
	want := []string{"example.pe", "www.example.pe"}
	if len(evidence.Domains) != len(want) {
		t.Fatalf("wrong domains: %#v", evidence.Domains)
	}
	for i, name := range want {
		if evidence.Domains[i] != name {
			t.Fatalf("wrong domains: %#v", evidence.Domains)
		}
	}
}

// The common name repeats in the SANs on almost every real certificate.
func TestDomainsAreDeduplicatedAndSorted(t *testing.T) {
	root := caddyStorage(
		t,
		"example.pe",
		[]string{"WWW.example.pe", "example.pe"},
		time.Now().Add(-time.Hour),
		time.Now().Add(time.Hour),
	)

	evidence, err := (Inspector{StorageDir: root}).Inspect("example.pe")
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Domains) != 2 || evidence.Domains[0] != "example.pe" || evidence.Domains[1] != "www.example.pe" {
		t.Fatalf("names were not normalised: %#v", evidence.Domains)
	}
}

// A certificate that has run out is a state to report, not an error to hide:
// the control plane can only act on it if it can see it.
func TestAnExpiredCertificateIsReportedNotRefused(t *testing.T) {
	notAfter := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	root := caddyStorage(t, "example.pe", []string{"example.pe"}, notAfter.AddDate(0, -3, 0), notAfter)

	evidence, err := (Inspector{
		StorageDir: root,
		Now:        func() time.Time { return notAfter.AddDate(0, 1, 0) },
	}).Inspect("example.pe")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Status != "expired" {
		t.Fatalf("an expired certificate was reported as %q", evidence.Status)
	}
}

// Before the domain resolves publicly there is nothing to report, and that is
// the normal state of a site that was just created.
func TestNoCertificateYetIsItsOwnAnswer(t *testing.T) {
	_, err := (Inspector{StorageDir: t.TempDir()}).Inspect("example.pe")
	if !errors.Is(err, ErrNoCertificate) {
		t.Fatalf("expected ErrNoCertificate, got %v", err)
	}

	_, err = (Inspector{StorageDir: filepath.Join(t.TempDir(), "absent")}).Inspect("example.pe")
	if !errors.Is(err, ErrNoCertificate) {
		t.Fatalf("a missing storage directory should read as no certificate, got %v", err)
	}
}

func TestASiteIsRequired(t *testing.T) {
	if _, err := (Inspector{StorageDir: t.TempDir()}).Inspect("  "); err == nil {
		t.Fatal("an empty site id was accepted")
	}
}
