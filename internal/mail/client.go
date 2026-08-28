// Package mail administers the Stalwart mail server that runs alongside the
// web stack on a shared hosting node.
//
// Stalwart exposes almost all of its administration over JMAP rather than a
// REST API: the endpoints under /api are helpers for login, discovery and
// telemetry, and everything that creates a domain or a mailbox is a JMAP method
// call against the `urn:stalwart:jmap` capability. The object model those calls
// operate on is published by the server itself at GET /api/schema.
//
// The same transport serves both hosting tiers. A shared node talks to the
// Stalwart on localhost; the control plane talks to the central Stalwart for
// business mailboxes. Only the base URL differs, which is the reason the same
// mail server was chosen for both.
package mail

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Capabilities every administrative call has to declare. Stalwart rejects a
// request whose `using` array omits its own capability, and it rejects it as a
// malformed request rather than as a permission error, which makes the failure
// look like a client bug.
var capabilities = []string{"urn:ietf:params:jmap:core", "urn:stalwart:jmap"}

// Client is a JMAP client for one Stalwart server.
type Client struct {
	// BaseURL is the server root, e.g. https://127.0.0.1 for a local node.
	BaseURL string
	// Username and Secret authenticate over HTTP Basic. On a shared node this
	// is an x:ApiKey credential scoped to administration, never an operator's
	// own password.
	Username string
	Secret   string
	// HTTP is optional; a client with a sane timeout is used when it is nil.
	HTTP *http.Client
	// InsecureTLS skips certificate verification. A node talks to the Stalwart
	// on its own loopback, where the certificate is self-signed and the
	// transport never leaves the machine.
	InsecureTLS bool

	mu        sync.Mutex
	accountID string
	once      sync.Once
	built     *http.Client
}

type jmapRequest struct {
	Using       []string `json:"using"`
	MethodCalls [][]any  `json:"methodCalls"`
}

type jmapResponse struct {
	MethodResponses [][]json.RawMessage `json:"methodResponses"`
}

type methodError struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

func (client *Client) httpClient() *http.Client {
	if client.HTTP != nil {
		return client.HTTP
	}

	client.once.Do(func() {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		if client.InsecureTLS {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // loopback only
		}
		client.built = &http.Client{Timeout: 30 * time.Second, Transport: transport}
	})

	return client.built
}

// AccountID resolves the administrative account the calls are made against.
//
// It is read from the JMAP session rather than assumed: Stalwart account ids
// are opaque strings the server assigns, and passing the login name instead
// silently addresses the wrong account.
func (client *Client) AccountID() (string, error) {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.accountID != "" {
		return client.accountID, nil
	}

	body, err := client.do(http.MethodGet, "/jmap/session", nil)
	if err != nil {
		return "", err
	}

	var session struct {
		PrimaryAccounts map[string]string `json:"primaryAccounts"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		return "", fmt.Errorf("mail: session is not valid JSON: %w", err)
	}

	id := session.PrimaryAccounts["urn:stalwart:jmap"]
	if id == "" {
		// Every account the server owns is listed under each capability it
		// holds, so any entry identifies the same principal.
		for _, candidate := range session.PrimaryAccounts {
			id = candidate
			break
		}
	}
	if id == "" {
		return "", errors.New("mail: the JMAP session names no account")
	}

	client.accountID = id

	return id, nil
}

// Invoke performs one JMAP method call and returns its raw response arguments.
func (client *Client) Invoke(method string, arguments map[string]any) (json.RawMessage, error) {
	accountID, err := client.AccountID()
	if err != nil {
		return nil, err
	}

	call := map[string]any{"accountId": accountID}
	for key, value := range arguments {
		call[key] = value
	}

	payload, err := json.Marshal(jmapRequest{
		Using:       capabilities,
		MethodCalls: [][]any{{method, call, "c0"}},
	})
	if err != nil {
		return nil, err
	}

	body, err := client.do(http.MethodPost, "/jmap/", payload)
	if err != nil {
		return nil, err
	}

	return firstResponse(method, body)
}

// firstResponse unwraps the single method response, turning a JMAP-level error
// into a Go error so callers never have to inspect the envelope.
func firstResponse(method string, body []byte) (json.RawMessage, error) {
	var response jmapResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("mail: %s returned invalid JSON: %w", method, err)
	}
	if len(response.MethodResponses) == 0 || len(response.MethodResponses[0]) < 2 {
		return nil, fmt.Errorf("mail: %s returned no method response", method)
	}

	var name string
	if err := json.Unmarshal(response.MethodResponses[0][0], &name); err != nil {
		return nil, fmt.Errorf("mail: %s returned an unnamed response: %w", method, err)
	}

	arguments := response.MethodResponses[0][1]
	if name == "error" {
		var failure methodError
		if err := json.Unmarshal(arguments, &failure); err != nil {
			return nil, fmt.Errorf("mail: %s failed", method)
		}

		return nil, fmt.Errorf("mail: %s failed: %s (%s)", method, failure.Description, failure.Type)
	}

	return arguments, nil
}

func (client *Client) do(method, path string, body []byte) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	request, err := http.NewRequest(method, strings.TrimSuffix(client.BaseURL, "/")+path, reader)
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(client.Username, client.Secret)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.httpClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("mail: %s %s: %w", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()

	// Bounded so a misconfigured endpoint answering with something large cannot
	// exhaust the agent.
	payload, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// The body can echo the request, which for a password change would put
		// the secret in the error, so only the status is reported.
		return nil, fmt.Errorf("mail: %s %s returned %d", method, path, response.StatusCode)
	}

	return payload, nil
}
