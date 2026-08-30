package enrollment

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Manager is the on-disk orchestrator for the agent's mTLS identity. It
// generates keys, builds CSRs, talks to Nubit Control through a Client, and
// persists the issued certificate, key and CA material to disk. It does not
// run the renewal loop on its own; see renewer.go for that.
type Manager struct {
	// Directory is the location of mTLS material: agent.key, agent.crt, ca-chain.crt.
	// The directory is created with 0o750 if it does not exist.
	Directory string
	// StateDirectory is the location of the persistent per-node identity
	// (agent.id). It is distinct from Directory so reinstalls that wipe
	// /etc/nubit-agent keep the same identity — and so the mTLS cert stays
	// bound to the same node across material rotations.
	StateDirectory string
	// ControlURL is the Nubit Control base URL (https://control.example.com).
	ControlURL string
	// RootCAPath, when set, pins the enrollment endpoint to a specific
	// trust root. Operator hook: NUBIT_STEPCA_ROOT_CERT_PATH.
	RootCAPath string
	// HTTPClient is the *http.Client used for the enrollment POST. When nil
	// the manager builds a Client with a sensible default. Tests inject
	// their own transport; production code does not.
	HTTPClient *http.Client
}

const (
	keyFile         = "agent-key.pem"
	certificateFile = "agent-cert.pem"
	caChainFile     = "ca-cert.pem"
)

// ErrNotEnrolled is returned by operations that require an existing
// certificate when none has been written yet.
var ErrNotEnrolled = errors.New("agent is not enrolled")

// ErrAlreadyEnrolled is returned by Enroll when a valid, non-expired
// certificate is already persisted. The caller can wipe the on-disk material
// (or wait for the renewal loop to handle it) and call again.
var ErrAlreadyEnrolled = errors.New("agent is already enrolled")

// Enroll generates a fresh keypair, builds a CSR, calls POST /api/agent/enroll
// and persists the response. The path is safe to call repeatedly: it refuses
// to overwrite an existing enrollment with ErrAlreadyEnrolled when a valid,
// non-expired certificate is already on disk. The caller can wipe the
// material (or wait for the renewal loop to handle it) and retry.
func (manager Manager) Enroll(ctx context.Context, token string) error {
	if token == "" {
		return errors.New("enrollment token is required")
	}
	controlURL, err := url.Parse(manager.ControlURL)
	if err != nil || controlURL.Host == "" {
		return errors.New("control-plane URL is invalid")
	}
	host := controlURL.Hostname()
	if controlURL.Scheme != "https" && !(controlURL.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1")) {
		return errors.New("enrollment requires HTTPS except on loopback")
	}
	if manager.alreadyEnrolled(time.Now().UTC()) {
		return ErrAlreadyEnrolled
	}
	agentID, err := LoadOrCreateAgentID(manager.StateDirectory)
	if err != nil {
		return err
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "localhost"
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate agent key: %w", err)
	}
	_, csrPEM, err := BuildCSR(key, agentID, hostname)
	if err != nil {
		return err
	}
	client, err := manager.newClient(token)
	if err != nil {
		return err
	}
	response, err := client.Enroll(ctx, string(csrPEM), agentID)
	if err != nil {
		return fmt.Errorf("enroll agent: %w", err)
	}
	if err := validateIssuedCertificate(response, csrPEM, key); err != nil {
		return err
	}
	keyPEM := marshalPrivateKey(key)
	if err := manager.persistMaterial(keyPEM, response); err != nil {
		return fmt.Errorf("persist enrollment: %w", err)
	}
	return nil
}

// alreadyEnrolled reports whether the on-disk material is complete AND the
// certificate has not yet expired. A parse failure on the cert leaves the
// caller free to re-enroll, so the answer is false in that case.
func (manager Manager) alreadyEnrolled(now time.Time) bool {
	if !manager.Enrolled() {
		return false
	}
	expiry, err := manager.CertificateExpiry()
	if err != nil || !expiry.After(now) {
		return false
	}
	return true
}

// Renew reuses the existing key, builds a fresh CSR and asks Nubit Control to
// reissue the certificate. The renewal is signed with the existing mTLS
// identity, so the caller must have already enrolled.
func (manager Manager) Renew(ctx context.Context) error {
	if !manager.Enrolled() {
		return ErrNotEnrolled
	}
	key, err := manager.loadPrivateKey()
	if err != nil {
		return err
	}
	agentID, err := LoadOrCreateAgentID(manager.StateDirectory)
	if err != nil {
		return err
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "localhost"
	}
	_, csrPEM, err := BuildCSR(key, agentID, hostname)
	if err != nil {
		return err
	}
	tlsConfig, err := manager.TLSConfig()
	if err != nil {
		return err
	}
	client, err := manager.newClient("")
	if err != nil {
		return err
	}
	response, err := client.Renew(ctx, string(csrPEM), agentID, tlsConfig)
	if err != nil {
		return fmt.Errorf("renew agent certificate: %w", err)
	}
	if err := validateIssuedCertificate(response, csrPEM, key); err != nil {
		return err
	}
	if err := manager.persistCertificate(response); err != nil {
		return err
	}
	return nil
}

func (manager Manager) newClient(token string) (*Client, error) {
	if manager.HTTPClient != nil {
		return &Client{BaseURL: manager.ControlURL, Token: token, HTTPClient: manager.HTTPClient, RootCAPEM: nil}, nil
	}
	client, err := NewClient(manager.ControlURL, token, manager.RootCAPath)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func validateIssuedCertificate(response *EnrollResponse, csrPEM []byte, key *ecdsa.PrivateKey) error {
	if response == nil || response.Certificate == "" {
		return errors.New("enrollment response is missing a certificate")
	}
	keyPEM := marshalPrivateKey(key)
	if _, err := tls.X509KeyPair([]byte(response.Certificate), keyPEM); err != nil {
		return fmt.Errorf("validate issued certificate: %w", err)
	}
	if _, err := parseCertificatePEM(response.Certificate); err != nil {
		return fmt.Errorf("parse issued certificate: %w", err)
	}
	ca, err := loadTrustAnchors(response)
	if err != nil {
		return err
	}
	if err := verifyChainAgainst(response.Certificate, ca); err != nil {
		return fmt.Errorf("issued certificate is not signed by the advertised CA: %w", err)
	}
	if len(csrPEM) > 0 {
		if err := verifyCSRSignature(csrPEM, key); err != nil {
			return fmt.Errorf("csr signature does not match the agent key: %w", err)
		}
	}
	return nil
}

func loadTrustAnchors(response *EnrollResponse) (*x509.CertPool, error) {
	bundle := response.CACertificate
	for _, extra := range response.CAChain {
		if strings.TrimSpace(extra) == "" {
			continue
		}
		if bundle != "" {
			bundle += "\n"
		}
		bundle += extra
	}
	if bundle == "" {
		return nil, errors.New("enrollment response is missing CA material")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(bundle)) {
		return nil, errors.New("enrollment response contains an invalid CA certificate bundle")
	}
	return pool, nil
}

func verifyChainAgainst(certificatePEM string, roots *x509.CertPool) error {
	certificate, err := parseCertificatePEM(certificatePEM)
	if err != nil {
		return err
	}
	_, err = certificate.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	return err
}

func verifyCSRSignature(csrPEM []byte, key *ecdsa.PrivateKey) error {
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return errors.New("invalid csr pem")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return err
	}
	return csr.CheckSignature()
}

func parseCertificatePEM(pemEncoded string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pemEncoded))
	if block == nil {
		return nil, errors.New("invalid certificate pem")
	}
	return x509.ParseCertificate(block.Bytes)
}

func marshalPrivateKey(key *ecdsa.PrivateKey) []byte {
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		// MarshalPKCS8PrivateKey only fails for an unsupported key type; P-256
		// is supported, so this is genuinely unreachable for an agent key.
		panic("enrollment: marshal agent private key: " + err.Error())
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func (manager Manager) persistMaterial(keyPEM []byte, response *EnrollResponse) error {
	if err := os.MkdirAll(manager.Directory, 0o750); err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{keyFile, keyPEM, 0o600},
		{certificateFile, []byte(response.Certificate), 0o644},
		{caChainFile, []byte(caBundleFor(response)), 0o644},
	}
	for _, file := range files {
		if err := writeAtomic(filepath.Join(manager.Directory, file.name), file.data, file.mode); err != nil {
			return err
		}
	}
	return nil
}

func (manager Manager) persistCertificate(response *EnrollResponse) error {
	files := []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{certificateFile, []byte(response.Certificate), 0o644},
		{caChainFile, []byte(caBundleFor(response)), 0o644},
	}
	for _, file := range files {
		if err := writeAtomic(filepath.Join(manager.Directory, file.name), file.data, file.mode); err != nil {
			return err
		}
	}
	return nil
}

func caBundleFor(response *EnrollResponse) string {
	if len(response.CAChain) > 0 {
		return strings.Join(response.CAChain, "\n")
	}
	return response.CACertificate
}

func (manager Manager) loadPrivateKey() (*ecdsa.PrivateKey, error) {
	keyPEM, err := os.ReadFile(filepath.Join(manager.Directory, keyFile))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("invalid agent private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("agent private key is not ECDSA")
	}
	return key, nil
}

// TLSConfig returns the *tls.Config the control-plane client should use to
// authenticate against Nubit Control. It loads the cert + key on every call
// so a renewed certificate is picked up without restarting the agent.
func (manager Manager) TLSConfig() (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(filepath.Join(manager.Directory, certificateFile), filepath.Join(manager.Directory, keyFile))
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(filepath.Join(manager.Directory, caChainFile))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("invalid control-plane CA certificate")
	}
	_ = certificate
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    roots,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			current, err := tls.LoadX509KeyPair(filepath.Join(manager.Directory, certificateFile), filepath.Join(manager.Directory, keyFile))
			return &current, err
		},
	}, nil
}

// VerifyCertificate checks that the on-disk certificate is well-formed,
// signed by the persisted CA bundle, and unexpired. It returns the parsed
// certificate so the caller can log expiry information without re-parsing.
func (manager Manager) VerifyCertificate() (*x509.Certificate, error) {
	certPEM, err := os.ReadFile(filepath.Join(manager.Directory, certificateFile))
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(filepath.Join(manager.Directory, caChainFile))
	if err != nil {
		return nil, err
	}
	certificate, err := parseCertificatePEM(string(certPEM))
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("invalid control-plane CA certificate")
	}
	if _, err := certificate.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return nil, fmt.Errorf("certificate is not signed by the persisted CA: %w", err)
	}
	return certificate, nil
}

// CertificateExpiry returns the NotAfter of the persisted agent certificate.
func (manager Manager) CertificateExpiry() (time.Time, error) {
	contents, err := os.ReadFile(filepath.Join(manager.Directory, certificateFile))
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(contents)
	if block == nil {
		return time.Time{}, errors.New("invalid agent certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return certificate.NotAfter, nil
}

// NeedsRenewal reports whether the on-disk certificate will expire within the
// given duration from now, or whether it cannot be parsed at all.
func (manager Manager) NeedsRenewal(now time.Time, before time.Duration) bool {
	expires, err := manager.CertificateExpiry()
	return err != nil || !expires.After(now.Add(before))
}

// Enrolled reports whether the persisted material is complete.
func (manager Manager) Enrolled() bool {
	for _, name := range []string{keyFile, certificateFile, caChainFile} {
		if _, err := os.Stat(filepath.Join(manager.Directory, name)); err != nil {
			return false
		}
	}
	return true
}
