package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/nubitio/nubit-agent/internal/durable"
)

const (
	DefaultOutboxLimit = 10000
	DefaultOutboxBytes = 64 << 20
)

// Flush reports every pending result to Nubit Control and removes the ones
// Control accepted, returning how many were drained. A transport error stops
// the drain and is returned along with the count sent so far; the remainder
// stay queued for the next attempt. The poll loop drains the outbox on every
// tick through the same path — the operator TUI calls this to force it
// between ticks.
func Flush(ctx context.Context, client *Client, outbox Outbox) (int, error) {
	drained := 0
	for _, pending := range outbox.List() {
		if err := client.ReportPending(ctx, pending); err != nil {
			if errors.Is(err, ErrLeaseRejected) {
				// The control plane has fenced this execution. Retrying the same
				// result can never succeed and must not block newer work.
				_ = outbox.Delete(pending.CommandID)
				continue
			}
			return drained, fmt.Errorf("report result for command %s: %w", pending.CommandID, err)
		}
		if err := outbox.Delete(pending.CommandID); err != nil {
			return drained, fmt.Errorf("delete reported result %s from outbox: %w", pending.CommandID, err)
		}
		drained++
	}
	return drained, nil
}

// Outbox put/delete failures are classified so the poller can decide whether a
// single bad write should abort the whole batch or be treated as recoverable.
//
// The current FileOutbox never returns ErrOutboxFull (it accepts unbounded
// entries; bounded eviction is tracked separately), but the sentinel exists
// for forward compatibility with the bounded outbox plan.
var (
	ErrOutboxFull    = errors.New("outbox full, oldest evicted")
	ErrOutboxCorrupt = errors.New("outbox storage corrupt")
	ErrOutboxIO      = errors.New("outbox io error")
)

type PendingResult struct {
	CommandID  string          `json:"commandId"`
	LeaseToken string          `json:"leaseToken,omitempty"`
	Status     string          `json:"status"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	CreatedAt  time.Time       `json:"createdAt,omitempty"`
}

type Outbox interface {
	Put(result PendingResult) error
	List() []PendingResult
	Delete(commandID string) error
}

type FileOutbox struct {
	mu         sync.RWMutex
	path       string
	pending    map[string]PendingResult
	maxEntries int
	maxBytes   int64
}

func NewFileOutbox(path string) (*FileOutbox, error) {
	outbox := &FileOutbox{path: path, pending: make(map[string]PendingResult), maxEntries: DefaultOutboxLimit, maxBytes: DefaultOutboxBytes}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return outbox, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read outbox file: %v", ErrOutboxIO, err)
	}
	if err := json.Unmarshal(contents, &outbox.pending); err != nil {
		return nil, fmt.Errorf("%w: parse outbox file: %v", ErrOutboxCorrupt, err)
	}
	return outbox, nil
}

func NewFileOutboxWithLimits(path string, maxEntries int, maxBytes int64) (*FileOutbox, error) {
	outbox, err := NewFileOutbox(path)
	if err != nil {
		return nil, err
	}
	if maxEntries > 0 {
		outbox.maxEntries = maxEntries
	}
	if maxBytes > 0 {
		outbox.maxBytes = maxBytes
	}
	return outbox, nil
}

func (outbox *FileOutbox) Put(result PendingResult) error {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	previous := clonePending(outbox.pending)
	if result.CreatedAt.IsZero() {
		result.CreatedAt = time.Now().UTC()
	}
	outbox.pending[result.CommandID] = result
	outbox.prune()
	if err := outbox.persist(); err != nil {
		outbox.pending = previous
		return err
	}
	return nil
}

func clonePending(pending map[string]PendingResult) map[string]PendingResult {
	clone := make(map[string]PendingResult, len(pending))
	for key, result := range pending {
		clone[key] = result
	}
	return clone
}

func (outbox *FileOutbox) List() []PendingResult {
	outbox.mu.RLock()
	defer outbox.mu.RUnlock()
	ids := make([]string, 0, len(outbox.pending))
	for id := range outbox.pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	results := make([]PendingResult, 0, len(ids))
	for _, id := range ids {
		results = append(results, outbox.pending[id])
	}
	return results
}

func (outbox *FileOutbox) Reset() error {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	outbox.pending = map[string]PendingResult{}

	return outbox.persist()
}

func (outbox *FileOutbox) Delete(commandID string) error {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	previous, found := outbox.pending[commandID]
	if !found {
		return nil
	}
	delete(outbox.pending, commandID)
	if err := outbox.persist(); err != nil {
		outbox.pending[commandID] = previous
		return err
	}
	return nil
}

func (outbox *FileOutbox) persist() error {
	if err := os.MkdirAll(filepath.Dir(outbox.path), 0o700); err != nil {
		return fmt.Errorf("%w: create outbox directory: %v", ErrOutboxIO, err)
	}
	contents, err := json.Marshal(outbox.pending)
	if err != nil {
		return fmt.Errorf("%w: encode outbox: %v", ErrOutboxCorrupt, err)
	}
	if err := durable.AtomicWrite(outbox.path, contents, 0o600); err != nil {
		return fmt.Errorf("%w: write outbox: %v", ErrOutboxIO, err)
	}
	return nil
}

func (outbox *FileOutbox) prune() {
	for len(outbox.pending) > outbox.maxEntries {
		delete(outbox.pending, oldest(outbox.pending))
	}
	for {
		contents, err := json.Marshal(outbox.pending)
		if err != nil || int64(len(contents)) <= outbox.maxBytes || len(outbox.pending) == 0 {
			return
		}
		delete(outbox.pending, oldest(outbox.pending))
	}
}

func oldest(pending map[string]PendingResult) string {
	var key string
	for candidate, result := range pending {
		if key == "" || result.CreatedAt.Before(pending[key].CreatedAt) || (result.CreatedAt.Equal(pending[key].CreatedAt) && candidate < key) {
			key = candidate
		}
	}
	return key
}
