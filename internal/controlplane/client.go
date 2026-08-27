// Package controlplane implements the agent-initiated transport to Nubit
// Control: polling for queued ProvisioningJobs and reporting results back.
// Authenticated with a per-server bearer token in X-Agent-Token — deliberately
// not the Authorization header, which Nubit Control's user-facing JWT
// authenticator also inspects. This is an interim transport; docs/roadmap.md
// tracks replacing it with agent-initiated mTLS.
package controlplane

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
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
