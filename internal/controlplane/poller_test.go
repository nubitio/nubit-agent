package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
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

// A staged self-update must never land mid-command: the loop finishes the work
// it fetched, and only then exits so the supervisor can start the new binary.
func TestPollStopsOnlyBetweenCommandsWhenAStopIsRequested(t *testing.T) {
	var executing atomic.Bool
	var stoppedWhileExecuting atomic.Bool
	var executed atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if http.MethodGet == request.Method {
			_, _ = writer.Write([]byte(`{"commands":[
				{"id":"1","type":"system.ping","version":1,"idempotencyKey":"a","payload":{}},
				{"id":"2","type":"system.ping","version":1,"idempotencyKey":"b","payload":{}}
			]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	executor := fakeExecutor{execute: func(command.Command) (command.Result, error) {
		executing.Store(true)
		time.Sleep(10 * time.Millisecond)
		executed.Add(1)
		executing.Store(false)
		return command.Result{Status: "succeeded"}, nil
	}}

	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		Poll(
			context.Background(),
			NewClient(server.URL, "token"),
			executor,
			testOutbox(t),
			time.Millisecond,
			WithStopCheck(
				func() bool { return true },
				func() {
					if executing.Load() {
						stoppedWhileExecuting.Store(true)
					}
					close(stopped)
				},
			),
		)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected Poll to return once a stop was requested")
	}

	<-stopped
	if stoppedWhileExecuting.Load() {
		t.Fatal("stopped with a command in flight; the update would interrupt provisioning")
	}
	if got := executed.Load(); got != 2 {
		t.Fatalf("executed %d commands before stopping, want both fetched commands", got)
	}
}

// scriptedOutbox returns errors from a fixed script on Put, while still
// recording what was put so List and Delete can answer. Used to assert that
// executeAndReport decides to continue or abort based on the error class.
type scriptedOutbox struct {
	mu     sync.Mutex
	puts   []error
	idx    int
	stored map[string]PendingResult
}

func newScriptedOutbox(puts ...error) *scriptedOutbox {
	return &scriptedOutbox{puts: puts, stored: make(map[string]PendingResult)}
}

func (outbox *scriptedOutbox) Put(result PendingResult) error {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	if outbox.idx >= len(outbox.puts) {
		outbox.stored[result.CommandID] = result
		return nil
	}
	err := outbox.puts[outbox.idx]
	outbox.idx++
	if err == nil {
		outbox.stored[result.CommandID] = result
	}
	return err
}

func (outbox *scriptedOutbox) List() []PendingResult {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	results := make([]PendingResult, 0, len(outbox.stored))
	for _, pending := range outbox.stored {
		results = append(results, pending)
	}
	return results
}

func (outbox *scriptedOutbox) Delete(commandID string) error {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	delete(outbox.stored, commandID)
	return nil
}

// ErrOutboxFull is treated as recoverable: the dropped result will be
// regenerated by the next poll, so the batch keeps going.
func TestExecuteAndReportContinuesBatchOnOutboxFull(t *testing.T) {
	outbox := newScriptedOutbox(
		fmt.Errorf("synthetic: %w", ErrOutboxFull),
		nil,
	)
	executed := make([]string, 0, 2)
	executor := fakeExecutor{execute: func(cmd command.Command) (command.Result, error) {
		executed = append(executed, cmd.ID)
		return command.Result{CommandID: cmd.ID, Status: "succeeded", Output: json.RawMessage(`{}`)}, nil
	}}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			_, _ = writer.Write([]byte(`{"commands":[
				{"id":"1","type":"system.ping","version":1,"idempotencyKey":"a","payload":{}},
				{"id":"2","type":"system.ping","version":1,"idempotencyKey":"b","payload":{}}
			]}`))
		case http.MethodPost:
			writer.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	pollOnce(context.Background(), NewClient(server.URL, "token"), executor, outbox)

	if len(executed) != 2 || executed[0] != "1" || executed[1] != "2" {
		t.Fatalf("both commands should have been executed, got %v", executed)
	}
}

// ErrOutboxCorrupt is treated as fatal for the batch: a corrupt store cannot
// be trusted to remember the next command's result either.
func TestExecuteAndReportAbortsBatchOnOutboxCorrupt(t *testing.T) {
	outbox := newScriptedOutbox(
		fmt.Errorf("synthetic: %w", ErrOutboxCorrupt),
		nil,
	)
	executed := make([]string, 0, 2)
	executor := fakeExecutor{execute: func(cmd command.Command) (command.Result, error) {
		executed = append(executed, cmd.ID)
		return command.Result{CommandID: cmd.ID, Status: "succeeded", Output: json.RawMessage(`{}`)}, nil
	}}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			_, _ = writer.Write([]byte(`{"commands":[
				{"id":"1","type":"system.ping","version":1,"idempotencyKey":"a","payload":{}},
				{"id":"2","type":"system.ping","version":1,"idempotencyKey":"b","payload":{}}
			]}`))
		}
	}))
	defer server.Close()

	pollOnce(context.Background(), NewClient(server.URL, "token"), executor, outbox)

	if len(executed) != 1 || executed[0] != "1" {
		t.Fatalf("only the first command should have been executed, got %v", executed)
	}
}

// ErrOutboxIO has the same abort-batch behaviour as ErrOutboxCorrupt.
func TestExecuteAndReportAbortsBatchOnOutboxIO(t *testing.T) {
	outbox := newScriptedOutbox(
		fmt.Errorf("synthetic: %w", ErrOutboxIO),
		nil,
	)
	executed := make([]string, 0, 2)
	executor := fakeExecutor{execute: func(cmd command.Command) (command.Result, error) {
		executed = append(executed, cmd.ID)
		return command.Result{CommandID: cmd.ID, Status: "succeeded", Output: json.RawMessage(`{}`)}, nil
	}}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			_, _ = writer.Write([]byte(`{"commands":[
				{"id":"1","type":"system.ping","version":1,"idempotencyKey":"a","payload":{}},
				{"id":"2","type":"system.ping","version":1,"idempotencyKey":"b","payload":{}}
			]}`))
		}
	}))
	defer server.Close()

	pollOnce(context.Background(), NewClient(server.URL, "token"), executor, outbox)

	if len(executed) != 1 || executed[0] != "1" {
		t.Fatalf("only the first command should have been executed, got %v", executed)
	}
}
