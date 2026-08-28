package audit

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLoggerAppendsEvent(t *testing.T) {
	dir := t.TempDir()
	logger, err := New(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		err := logger.Record(context.Background(), Event{
			CommandID:      "cmd-" + string(rune('a'+i)),
			CommandType:    "system.ping",
			IdempotencyKey: "k-" + string(rune('a'+i)),
			PayloadSHA256:  HashPayload([]byte(`{}`)),
			Result:         "ok",
			DurationMs:     int64(i + 1),
		})
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	file, err := os.Open(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lines := 0
	for scanner.Scan() {
		lines++
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("line %d is not valid NDJSON: %v", lines, err)
		}
		if event.Timestamp == "" {
			t.Fatalf("line %d missing timestamp", lines)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if lines != 3 {
		t.Fatalf("expected 3 lines, got %d", lines)
	}
}

// The audit log is the only record of what was run, so torn writes would
// silently lose events. The serializer must hold the lock for the full
// write+fsync window.
func TestLoggerSurvivesConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	logger, err := New(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 10
	const perGoroutine = 100
	var started sync.WaitGroup
	var release sync.WaitGroup
	started.Add(goroutines)
	release.Add(1)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			started.Done()
			release.Wait()
			for i := 0; i < perGoroutine; i++ {
				err := logger.Record(context.Background(), Event{
					CommandID:      "g",
					CommandType:    "system.ping",
					IdempotencyKey: "k",
					PayloadSHA256:  HashPayload([]byte(`{}`)),
					Result:         "ok",
				})
				if err != nil {
					t.Errorf("goroutine %d record %d: %v", gid, i, err)
					return
				}
			}
		}(g)
	}
	started.Wait()
	release.Done()
	wg.Wait()

	lines := countLines(t, filepath.Join(dir, "audit.log"))
	if lines != goroutines*perGoroutine {
		t.Fatalf("expected %d lines, got %d", goroutines*perGoroutine, lines)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	lines := 0
	for scanner.Scan() {
		// A torn line would be empty or not parse as JSON; either is a
		// failure of the contract that Record never writes half an event.
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Errorf("torn line: %q", scanner.Text())
			continue
		}
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// A broken log path must return an error, not panic. Callers (the executor)
// log a warning and continue; an audit failure never blocks a command.
func TestLoggerFailsSoftOnDiskFull(t *testing.T) {
	// First scenario: the constructor cannot open the log because the
	// parent directory is read-only. The call must return an error and a
	// nil logger; it must not panic.
	readOnly := t.TempDir()
	if err := os.Chmod(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

	logger, err := New(filepath.Join(readOnly, "audit.log"))
	if err == nil {
		t.Fatal("expected an error when the log path is inside a read-only directory")
	}
	if logger != nil {
		t.Fatal("expected a nil logger when construction fails")
	}

	// Second scenario: a logger whose file path is replaced by a directory
	// between construction and the first Record. Record must return an error
	// rather than panic — the executor relies on that to fail soft.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "audit.log")
	logger, err = New(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(logPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := logger.Record(context.Background(), Event{CommandID: "x", CommandType: "system.ping", IdempotencyKey: "x", PayloadSHA256: HashPayload(nil), Result: "ok"}); err == nil {
		t.Fatal("expected an error when the log file path is now a directory")
	}
}

// The hash is a 64-character lowercase hex string. Anything else breaks the
// shape of the field on which downstream tooling will match.
func TestEventSHA256IsHex64(t *testing.T) {
	got := HashPayload([]byte(`{"siteId":"example.pe"}`))
	if len(got) != 64 {
		t.Fatalf("expected 64 hex chars, got %d (%q)", len(got), got)
	}
	if _, err := hex.DecodeString(got); err != nil {
		t.Fatalf("not valid hex: %v (%q)", err, got)
	}
	if got != strings.ToLower(got) {
		t.Fatalf("hash must be lowercase: %q", got)
	}
}

// context cancellation before a write starts must surface as an error; the
// half-written case is the auditor's worst nightmare.
func TestLoggerHonoursContextCancellation(t *testing.T) {
	dir := t.TempDir()
	logger, err := New(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = logger.Record(ctx, Event{CommandID: "x", CommandType: "system.ping", IdempotencyKey: "x", PayloadSHA256: HashPayload(nil), Result: "ok"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// Nothing was written, so the file must still be empty.
	if size, statErr := os.Stat(filepath.Join(dir, "audit.log")); statErr == nil {
		if size.Size() != 0 {
			t.Fatalf("expected an empty audit log, found %d bytes", size.Size())
		}
	}
}
