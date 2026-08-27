package controlplane

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type PendingResult struct {
	CommandID string          `json:"commandId"`
	Status    string          `json:"status"`
	Output    json.RawMessage `json:"output,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type Outbox interface {
	Put(result PendingResult) error
	List() []PendingResult
	Delete(commandID string) error
}

type FileOutbox struct {
	mu      sync.RWMutex
	path    string
	pending map[string]PendingResult
}

func NewFileOutbox(path string) (*FileOutbox, error) {
	outbox := &FileOutbox{path: path, pending: make(map[string]PendingResult)}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return outbox, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(contents, &outbox.pending); err != nil {
		return nil, err
	}
	return outbox, nil
}

func (outbox *FileOutbox) Put(result PendingResult) error {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	previous, existed := outbox.pending[result.CommandID]
	outbox.pending[result.CommandID] = result
	if err := outbox.persist(); err != nil {
		if existed {
			outbox.pending[result.CommandID] = previous
		} else {
			delete(outbox.pending, result.CommandID)
		}
		return err
	}
	return nil
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
		return err
	}
	contents, err := json.Marshal(outbox.pending)
	if err != nil {
		return err
	}
	temporary := outbox.path + ".tmp"
	if err := os.WriteFile(temporary, contents, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, outbox.path)
}
