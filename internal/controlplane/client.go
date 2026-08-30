// Package controlplane implements the agent-initiated transport to Nubit
// Control: polling for queued ProvisioningJobs and reporting results back.
// Authenticated with a per-server bearer token in X-Agent-Token — deliberately
// not the Authorization header, which Nubit Control's user-facing JWT
// authenticator also inspects. When mTLS material is available on disk
// (CertFile + KeyFile), the client also presents it on every request; both
// authenticators can be active at once while a fleet rolls out, so the token
// header is never suppressed in dual mode.
package controlplane

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nubitio/nubit-agent/internal/command"
)

type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	// CertFile and KeyFile, when both set and readable, are loaded as a
	// tls.Certificate and presented on every TLS handshake. A failure to load
	// them is logged and the client falls back to the token-only transport —
	// a missing or unreadable cert must not prevent the agent from running.
	CertFile string
	KeyFile  string
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		Token:      token,
		HTTPClient: &http.Client{Timeout: 180 * time.Second},
	}
}

func NewMTLSClient(baseURL string, tlsConfig *tls.Config) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 180 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConfig}},
	}
}

// NewDualClient returns a client that, on top of the X-Agent-Token header,
// also presents a client certificate loaded from certFile/keyFile. The token
// header is still set on every request so the server can validate either
// identity during the cutover. A failure to load the keypair is logged and
// the returned client falls back to the token-only transport.
func NewDualClient(baseURL, token, certFile, keyFile string) *Client {
	client := NewClient(baseURL, token)
	client.CertFile = certFile
	client.KeyFile = keyFile
	client.attachMTLSTransport()
	return client
}

// attachMTLSTransport upgrades the client's HTTP transport with a client
// certificate. A failure to load the keypair is logged and the transport is
// left untouched, so callers always retain the token-only fallback.
func (client *Client) attachMTLSTransport() {
	if client.CertFile == "" || client.KeyFile == "" {
		return
	}
	certificate, err := tls.LoadX509KeyPair(client.CertFile, client.KeyFile)
	if err != nil {
		log.Printf("nubit-agent: load mTLS keypair (%s, %s): %v; continuing without client certificate", client.CertFile, client.KeyFile, err)
		return
	}
	transport, ok := client.HTTPClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		transport = &http.Transport{}
	}
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	transport.TLSClientConfig.Certificates = []tls.Certificate{certificate}
	client.HTTPClient.Transport = transport
}

func (client *Client) authenticate(request *http.Request) {
	if client.Token != "" {
		request.Header.Set("X-Agent-Token", client.Token)
	}
}

func (client *Client) validateEndpoint() error {
	parsed, err := url.Parse(client.BaseURL)
	if err != nil || parsed.Host == "" {
		return errors.New("control-plane URL is invalid")
	}
	host := parsed.Hostname()
	allowHTTP := os.Getenv("NUBIT_CONTROL_ALLOW_HTTP") == "1" || os.Getenv("NUBIT_CONTROL_ALLOW_HTTP") == "true"
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (loopback || allowHTTP)) {
		return errors.New("control-plane URL must use HTTPS except on loopback")
	}
	if parsed.User != nil {
		return errors.New("control-plane URL must not contain credentials")
	}
	return nil
}

type jobsResponse struct {
	Commands []command.Command `json:"commands"`
}

// FetchJobs asks Nubit Control for this server's queued work. The server
// marks every returned job "running" and updates the server's heartbeat as a
// side effect of the request.
func (client *Client) FetchJobs(ctx context.Context) ([]command.Command, error) {
	if err := client.validateEndpoint(); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.BaseURL+"/api/agent/jobs?wait=20", nil)
	if err != nil {
		return nil, fmt.Errorf("build jobs request: %w", err)
	}
	client.authenticate(request)

	response, err := client.HTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch jobs: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if http.StatusOK != response.StatusCode {
		return nil, fmt.Errorf("fetch jobs: unexpected status %d", response.StatusCode)
	}

	var body jobsResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode jobs response: %w", err)
	}

	return body.Commands, nil
}

type resultReport struct {
	Status string          `json:"status"`
	Output json.RawMessage `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// ReportResult tells Nubit Control how a command turned out. execErr, when
// non-nil, is the reason a command that never produced a Result failed —
// its message becomes the job's lastError.
func (client *Client) ReportResult(ctx context.Context, commandID, status string, output json.RawMessage, execErr error) error {
	pending := PendingResult{CommandID: commandID, Status: status, Output: output}
	if execErr != nil {
		pending.Error = execErr.Error()
	}
	return client.ReportPending(ctx, pending)
}

func (client *Client) ReportPending(ctx context.Context, pending PendingResult) error {
	if err := client.validateEndpoint(); err != nil {
		return err
	}
	report := resultReport{Status: pending.Status, Output: pending.Output, Error: pending.Error}

	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode result report: %w", err)
	}

	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, client.BaseURL+"/api/agent/jobs/"+pending.CommandID+"/result", bytes.NewReader(payload),
	)
	if err != nil {
		return fmt.Errorf("build result request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	client.authenticate(request)

	response, err := client.HTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("report result: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if http.StatusOK != response.StatusCode {
		return fmt.Errorf("report result: unexpected status %d", response.StatusCode)
	}

	return nil
}

func (client *Client) PublishInventory(ctx context.Context, inventory any) error {
	if err := client.validateEndpoint(); err != nil {
		return err
	}
	payload, err := json.Marshal(inventory)
	if err != nil {
		return fmt.Errorf("encode inventory: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.BaseURL+"/api/agent/inventory", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build inventory request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	client.authenticate(request)
	response, err := client.HTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("publish inventory: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("publish inventory: unexpected status %d", response.StatusCode)
	}
	return nil
}
