// Package audit records a tamper-evident trail of every command the agent
// runs. The control plane reports each result back to Nubit Control, and the
// idempotency store remembers the cached Result, but neither of those answers
// the operator's question "what exact payload did we run two days ago?". The
// audit log fills that gap.
//
// Each entry is one JSON object per line (NDJSON), appended with fsync so a
// crash cannot tear a record. Only the SHA-256 of the payload is written, so
// secrets do not accumulate on disk; an operator who needs the raw payload
// must look it up in the control plane's history.
//
// The log is unbounded and append-only on purpose for the MVP: there is no
// rotation and no external sink. Retention and shipping to Loki/journald are
// tracked as post-MVP work.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event is the row written to the audit log. Fields are stable JSON names so
// downstream tooling can match them without consulting the Go type.
type Event struct {
	Timestamp      string `json:"timestamp"`
	CommandID      string `json:"command_id"`
	CommandType    string `json:"command_type"`
	IdempotencyKey string `json:"idempotency_key"`
	PayloadSHA256  string `json:"payload_sha256"`
	Actor          string `json:"actor"`
	Result         string `json:"result"`
	DurationMs     int64  `json:"duration_ms"`
}

// Logger appends NDJSON lines to a single file under a serialised mutex.
// Concurrent calls are safe; ordering follows the lock, not the call sites.
type Logger struct {
	path string
	mu   sync.Mutex
}

// New opens (or creates) the audit log at path. The parent directory is
// created with 0o700 and the file with 0o600 to match the rest of the
// agent's state. A nil logger is not returned: callers that want optional
// auditing can leave the field unset.
func New(path string) (*Logger, error) {
	if path == "" {
		return nil, errors.New("audit log path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("audit: create directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: open log: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("audit: close log: %w", err)
	}
	return &Logger{path: path}, nil
}

// Record appends event as a single NDJSON line and fsyncs the file. It is
// best-effort from the caller's perspective: errors are returned so the
// executor can log a warning, but they never propagate as a command failure.
//
// The supplied context may cancel the call before the write starts; once the
// write is in flight the append is allowed to finish because partial records
// are worse than a slightly late one.
func (logger *Logger) Record(ctx context.Context, event Event) error {
	if logger == nil {
		return nil
	}
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if event.Actor == "" {
		event.Actor = "control-plane"
	}
	if event.PayloadSHA256 == "" {
		event.PayloadSHA256 = HashPayload(nil)
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("audit: encode event: %w", err)
	}
	payload = append(payload, '\n')

	if err := ctx.Err(); err != nil {
		return err
	}

	logger.mu.Lock()
	defer logger.mu.Unlock()

	file, err := os.OpenFile(logger.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("audit: open log: %w", err)
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("audit: write event: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("audit: fsync event: %w", err)
	}
	return nil
}

// HashPayload returns the hex-encoded SHA-256 of payload, matching the
// PayloadSHA256 field on Event. It is a free function so callers do not need
// to hold a Logger just to compute the hash.
func HashPayload(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
