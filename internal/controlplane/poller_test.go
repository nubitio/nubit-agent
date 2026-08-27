package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nubitio/nubit-agent/internal/command"
)

type fakeExecutor struct {
	execute func(command.Command) (command.Result, error)
}

func testOutbox(t *testing.T) *FileOutbox {
	t.Helper()
	outbox, err := NewFileOutbox(filepath.Join(t.TempDir(), "outbox.json"))
	if err != nil {
		t.Fatal(err)
	}
	return outbox
}

func (fake fakeExecutor) Execute(cmd command.Command) (command.Result, error) {
	return fake.execute(cmd)
}

func TestPollOnceExecutesEveryFetchedCommandAndReportsSuccess(t *testing.T) {
	var reported map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case http.MethodGet == request.Method:
			_, _ = writer.Write([]byte(`{"commands":[{"id":"7","type":"system.ping","version":1,"idempotencyKey":"k","payload":{}}]}`))
		case http.MethodPost == request.Method:
			_ = json.NewDecoder(request.Body).Decode(&reported)
			writer.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	executor := fakeExecutor{execute: func(cmd command.Command) (command.Result, error) {
		return command.Result{CommandID: cmd.ID, Status: "succeeded", Output: json.RawMessage(`{}`)}, nil
	}}

	pollOnce(context.Background(), NewClient(server.URL, "token"), executor, testOutbox(t))

	if nil == reported {
		t.Fatal("expected the result to be reported")
	}
	if "succeeded" != reported["status"] {
		t.Fatalf("expected status succeeded, got %#v", reported["status"])
	}
}

func TestPollOnceReportsFailureWhenExecutionErrors(t *testing.T) {
	var reported map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case http.MethodGet == request.Method:
			_, _ = writer.Write([]byte(`{"commands":[{"id":"8","type":"shell.execute","version":1,"idempotencyKey":"k","payload":{}}]}`))
		case http.MethodPost == request.Method:
			_ = json.NewDecoder(request.Body).Decode(&reported)
			writer.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	executor := fakeExecutor{execute: func(command.Command) (command.Result, error) {
		return command.Result{}, errors.New("unsupported command type")
	}}

	pollOnce(context.Background(), NewClient(server.URL, "token"), executor, testOutbox(t))

	if "failed" != reported["status"] || "unsupported command type" != reported["error"] {
		t.Fatalf("unexpected report: %#v", reported)
	}
}

func TestPendingResultIsRetriedFromOutbox(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if attempts.Add(1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	outbox := testOutbox(t)
	if err := outbox.Put(PendingResult{CommandID: "9", Status: "succeeded", Output: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	client := NewClient(server.URL, "token")
	flushOutbox(context.Background(), client, outbox)
	if len(outbox.List()) != 1 {
		t.Fatal("expected failed report to remain pending")
	}
	flushOutbox(context.Background(), client, outbox)
	if attempts.Load() != 2 {
		t.Fatalf("expected two report attempts, got %d", attempts.Load())
	}
	if len(outbox.List()) != 0 {
		t.Fatal("expected accepted report to be deleted")
	}
}

func TestPollStopsWhenContextIsCancelled(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if http.MethodGet == request.Method {
			polls.Add(1)
			_, _ = writer.Write([]byte(`{"commands":[]}`))
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	outbox := testOutbox(t)
	go func() {
		Poll(ctx, NewClient(server.URL, "token"), fakeExecutor{execute: func(cmd command.Command) (command.Result, error) {
			return command.Result{}, nil
		}}, outbox, time.Millisecond)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("expected Poll to return after context cancellation")
	}

	if polls.Load() < 1 {
		t.Fatal("expected at least one poll before cancellation")
	}
}
