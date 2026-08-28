package mail

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func stalwart(t *testing.T, handler func(request *http.Request) (int, string)) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/jmap/session" {
			_, _ = io.WriteString(writer, `{"primaryAccounts":{"urn:stalwart:jmap":"d333333"}}`)
			return
		}
		status, body := handler(request)
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, body)
	}))
	t.Cleanup(server.Close)

	return server
}

func TestAccountIDComesFromTheSessionAndIsCachedOnce(t *testing.T) {
	var sessions int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sessions++
		_, _ = io.WriteString(writer, `{"primaryAccounts":{"urn:stalwart:jmap":"d333333"}}`)
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL, Username: "api", Secret: "key"}
	for range 3 {
		id, err := client.AccountID()
		if err != nil {
			t.Fatal(err)
		}
		// The login name is not the account id. Passing it addresses the wrong
		// account, which the server accepts rather than refuses.
		if id != "d333333" {
			t.Fatalf("wrong account id: %q", id)
		}
	}
	if sessions != 1 {
		t.Fatalf("the session was fetched %d times; it should be cached", sessions)
	}
}

func TestInvokeDeclaresTheStalwartCapabilityAndTheAccount(t *testing.T) {
	var sent jmapRequest
	server := stalwart(t, func(request *http.Request) (int, string) {
		body, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(body, &sent)
		return http.StatusOK, `{"methodResponses":[["x:Domain/get",{"list":[]},"c0"]]}`
	})

	client := &Client{BaseURL: server.URL, Username: "api", Secret: "key"}
	if _, err := client.Invoke("x:Domain/get", map[string]any{"ids": nil}); err != nil {
		t.Fatal(err)
	}

	// Omitting urn:stalwart:jmap makes the server reject the call as a
	// malformed request, which reads like a client bug rather than a missing
	// capability.
	var declared bool
	for _, capability := range sent.Using {
		if capability == "urn:stalwart:jmap" {
			declared = true
		}
	}
	if !declared {
		t.Fatalf("the Stalwart capability was not declared: %#v", sent.Using)
	}

	arguments := sent.MethodCalls[0][1].(map[string]any)
	if arguments["accountId"] != "d333333" {
		t.Fatalf("the call did not carry the account id: %#v", arguments)
	}
}

func TestAMethodErrorBecomesAGoError(t *testing.T) {
	server := stalwart(t, func(*http.Request) (int, string) {
		return http.StatusOK, `{"methodResponses":[["error",{"type":"unknownMethod","description":"x:Nope/get"},"c0"]]}`
	})

	client := &Client{BaseURL: server.URL, Username: "api", Secret: "key"}
	_, err := client.Invoke("x:Nope/get", nil)
	if err == nil {
		t.Fatal("a JMAP error was reported as success")
	}
	if !strings.Contains(err.Error(), "unknownMethod") {
		t.Fatalf("the server's reason was lost: %v", err)
	}
}

// The server echoes the request in some failures. For a password change that
// would put the secret into an error that is logged and persisted.
func TestAFailedRequestNeverEchoesTheBody(t *testing.T) {
	server := stalwart(t, func(*http.Request) (int, string) {
		return http.StatusBadRequest, `{"detail":"{\"secret\":\"Tr3men.da-Clave_2026!x\"}"}`
	})

	client := &Client{BaseURL: server.URL, Username: "api", Secret: "key"}
	_, err := client.Invoke("x:Account/set", map[string]any{"update": "…"})
	if err == nil {
		t.Fatal("a 400 was reported as success")
	}
	if strings.Contains(err.Error(), "Tr3men.da-Clave_2026!x") {
		t.Fatalf("the error leaked the request body: %v", err)
	}
}

func TestTheRequestAuthenticatesOverBasic(t *testing.T) {
	var user, secret string
	var ok bool
	server := stalwart(t, func(request *http.Request) (int, string) {
		user, secret, ok = request.BasicAuth()
		return http.StatusOK, `{"methodResponses":[["x:Domain/get",{"list":[]},"c0"]]}`
	})

	client := &Client{BaseURL: server.URL, Username: "api-key", Secret: "s3cret"}
	if _, err := client.Invoke("x:Domain/get", nil); err != nil {
		t.Fatal(err)
	}
	if !ok || user != "api-key" || secret != "s3cret" {
		t.Fatalf("basic auth was not sent: %q %q %v", user, secret, ok)
	}
}
