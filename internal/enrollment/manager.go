package enrollment

import (
	"bytes"
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
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type Manager struct {
	Directory  string
	ControlURL string
	HTTPClient *http.Client
}

type enrollRequest struct {
	Token string `json:"token"`
	CSR   string `json:"csr"`
}

type enrollResponse struct {
	Certificate   string `json:"certificate"`
	CACertificate string `json:"caCertificate"`
}

type renewRequest struct {
	CSR string `json:"csr"`
}

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
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate agent key: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "nubit-agent"}}, key)
	if err != nil {
		return fmt.Errorf("create enrollment CSR: %w", err)
	}
	requestBody, err := json.Marshal(enrollRequest{Token: token, CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, manager.ControlURL+"/api/agent/enroll", bytes.NewReader(requestBody))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client := manager.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("enroll agent: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("enroll agent: unexpected status %d", response.StatusCode)
	}
	var enrolled enrollResponse
	if err := json.NewDecoder(response.Body).Decode(&enrolled); err != nil {
		return fmt.Errorf("decode enrollment response: %w", err)
	}
	if _, err := tls.X509KeyPair([]byte(enrolled.Certificate), keyPEM); err != nil {
		return fmt.Errorf("validate issued certificate: %w", err)
	}
	if pool := x509.NewCertPool(); !pool.AppendCertsFromPEM([]byte(enrolled.CACertificate)) {
		return errors.New("enrollment response contains an invalid CA certificate")
	}
	if err := os.MkdirAll(manager.Directory, 0o700); err != nil {
		return err
	}
	for _, file := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{{"agent-key.pem", keyPEM, 0o600}, {"agent-cert.pem", []byte(enrolled.Certificate), 0o644}, {"ca-cert.pem", []byte(enrolled.CACertificate), 0o644}} {
		if err := writeAtomic(filepath.Join(manager.Directory, file.name), file.data, file.mode); err != nil {
			return err
		}
	}
	return nil
}

func (manager Manager) TLSConfig() (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(filepath.Join(manager.Directory, "agent-cert.pem"), filepath.Join(manager.Directory, "agent-key.pem"))
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile(filepath.Join(manager.Directory, "ca-cert.pem"))
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
			current, err := tls.LoadX509KeyPair(filepath.Join(manager.Directory, "agent-cert.pem"), filepath.Join(manager.Directory, "agent-key.pem"))
			return &current, err
		},
	}, nil
}

func (manager Manager) CertificateExpiry() (time.Time, error) {
	contents, err := os.ReadFile(filepath.Join(manager.Directory, "agent-cert.pem"))
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

func (manager Manager) NeedsRenewal(now time.Time, before time.Duration) bool {
	expires, err := manager.CertificateExpiry()
	return err != nil || !expires.After(now.Add(before))
}

func (manager Manager) Renew(ctx context.Context) error {
	keyPEM, err := os.ReadFile(filepath.Join(manager.Directory, "agent-key.pem"))
	if err != nil {
		return err
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return errors.New("invalid agent private key")
	}
	keyValue, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return err
	}
	key, ok := keyValue.(*ecdsa.PrivateKey)
	if !ok {
		return errors.New("agent private key is not ECDSA")
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "nubit-agent"}}, key)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(renewRequest{CSR: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))})
	tlsConfig, err := manager.TLSConfig()
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, manager.ControlURL+"/api/agent/certificate/renew", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConfig}}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("renew agent certificate: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("renew agent certificate: unexpected status %d", response.StatusCode)
	}
	var renewed enrollResponse
	if err := json.NewDecoder(response.Body).Decode(&renewed); err != nil {
		return err
	}
	if _, err := tls.X509KeyPair([]byte(renewed.Certificate), keyPEM); err != nil {
		return fmt.Errorf("validate renewed certificate: %w", err)
	}
	if err := writeAtomic(filepath.Join(manager.Directory, "agent-cert.pem"), []byte(renewed.Certificate), 0o644); err != nil {
		return err
	}
	if renewed.CACertificate != "" {
		if err := writeAtomic(filepath.Join(manager.Directory, "ca-cert.pem"), []byte(renewed.CACertificate), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (manager Manager) Enrolled() bool {
	for _, name := range []string{"agent-key.pem", "agent-cert.pem", "ca-cert.pem"} {
		if _, err := os.Stat(filepath.Join(manager.Directory, name)); err != nil {
			return false
		}
	}
	return true
}

func writeAtomic(path string, contents []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".nubit-enrollment-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
