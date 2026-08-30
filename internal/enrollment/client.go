package enrollment

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// EnrollResponse is the JSON payload Nubit Control returns from a successful
// enrollment or renewal. Certificate, CACertificate and CAChain are all PEM
// encoded; CAChain, when present, supersedes CACertificate for trust-store
// reconstruction (e.g. an intermediate + root bundle from step-ca).
type EnrollResponse struct {
	Certificate   string    `json:"certificate"`
	CACertificate string    `json:"ca_chain"`
	CAChain       []string  `json:"ca_chain_entries,omitempty"`
	Serial        string    `json:"serial,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
}

type enrollRequest struct {
	Token   string `json:"enrollment_token"`
	CSR     string `json:"csr"`
	AgentID string `json:"agent_id"`
}

type renewRequest struct {
	CSR     string `json:"csr"`
	AgentID string `json:"agent_id"`
}

// Client posts CSRs to Nubit Control's enroll and renew endpoints and parses
// the response. It does not persist anything; the caller decides where the
// issued material lands.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	// RootCAPEM, when set, pins the control-plane enrollment endpoint to a
	// specific trust root. The HTTP transport will refuse to complete the
	// handshake unless the server presents a certificate that chains to one
	// of these roots — so a token leak cannot be redeemed against a spoofed
	// control plane. The env var NUBIT_STEPCA_ROOT_CERT_PATH is the operator
	// hook for this knob.
	RootCAPEM []byte
}

// NewClient returns a Client that pins the enrollment endpoint to the PEM
// bundle at rootCAPath when that path is readable. A missing or unreadable
// file is not fatal: the system roots (or the issuer returned by the response
// after the first renewal) are the fallback. Production deployments should
// always set the path explicitly.
func NewClient(baseURL, token, rootCAPath string) (*Client, error) {
	client := &Client{
		BaseURL: baseURL,
		Token:   token,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
	if rootCAPath != "" {
		contents, err := os.ReadFile(rootCAPath)
		if err != nil {
			return nil, fmt.Errorf("read step-ca root certificate %s: %w", rootCAPath, err)
		}
		client.RootCAPEM = contents
	}
	if err := client.configureTransport(); err != nil {
		return nil, err
	}
	return client, nil
}

func (client *Client) configureTransport() error {
	if len(client.RootCAPEM) == 0 {
		return nil
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(client.RootCAPEM) {
		return errors.New("step-ca root certificate bundle is invalid")
	}
	client.HTTPClient.Transport = &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
	}
	return nil
}

func (client *Client) validateEndpoint() error {
	parsed, err := url.Parse(client.BaseURL)
	if err != nil || parsed.Host == "" {
		return errors.New("control-plane URL is invalid")
	}
	host := parsed.Hostname()
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return errors.New("enrollment requires HTTPS except on loopback")
	}
	return nil
}

// Enroll posts the supplied CSR to POST /api/agent/enroll together with the
// one-time token and this agent's identity. On a 200 response the issued
// certificate and CA material are returned to the caller for persistence.
func (client *Client) Enroll(ctx context.Context, csrPEM, agentID string) (*EnrollResponse, error) {
	if client.Token == "" {
		return nil, errors.New("enrollment token is required")
	}
	if csrPEM == "" {
		return nil, errors.New("csr is required")
	}
	if err := client.validateEndpoint(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(enrollRequest{
		Token:   client.Token,
		CSR:     csrPEM,
		AgentID: agentID,
	})
	if err != nil {
		return nil, fmt.Errorf("encode enroll request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.BaseURL+"/api/agent/enroll", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build enroll request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	return client.doEnroll(ctx, request)
}

// Renew posts the supplied CSR to POST /api/agent/certificate/renew. The
// request is authenticated with the existing mTLS identity; the supplied
// *tls.Config must be the one currently in use by the control-plane client.
func (client *Client) Renew(ctx context.Context, csrPEM, agentID string, identity *tls.Config) (*EnrollResponse, error) {
	if csrPEM == "" {
		return nil, errors.New("csr is required")
	}
	if err := client.validateEndpoint(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(renewRequest{
		CSR:     csrPEM,
		AgentID: agentID,
	})
	if err != nil {
		return nil, fmt.Errorf("encode renew request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.BaseURL+"/api/agent/certificate/renew", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build renew request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	transport := &http.Transport{
		TLSClientConfig: identity,
	}
	httpClient := &http.Client{Timeout: 30 * time.Second, Transport: transport}
	return doJSON[EnrollResponse](httpClient, request)
}

func (client *Client) doEnroll(ctx context.Context, request *http.Request) (*EnrollResponse, error) {
	return doJSON[EnrollResponse](client.HTTPClient, request)
}

func doJSON[T any](httpClient *http.Client, request *http.Request) (*T, error) {
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", request.Method, request.URL.Path, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s: unexpected status %d: %s", request.Method, request.URL.Path, response.StatusCode, readErrorBody(response.Body))
	}
	var body T
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode response for %s %s: %w", request.Method, request.URL.Path, err)
	}
	return &body, nil
}

// readErrorBody pulls up to 1 KiB from the response body so error messages
// carry the server's complaint without exposing arbitrarily large payloads
// (a 500 page with a stack trace, a debug dump, etc.).
func readErrorBody(body io.Reader) string {
	const limit = 1024
	limited := &io.LimitedReader{R: body, N: limit}
	contents, err := io.ReadAll(limited)
	if err != nil || len(contents) == 0 {
		return ""
	}
	contents = bytes.TrimSpace(contents)
	if limited.N == 0 {
		return string(contents) + "...(truncated)"
	}
	return string(contents)
}
