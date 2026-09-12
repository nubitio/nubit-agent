package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/nubitio/nubit-agent/internal/durable"
)

var ErrResultTooLarge = errors.New("command result exceeds configured size limit")

const (
	DefaultResultLimit = 10000
	DefaultResultBytes = 64 << 20
)

type FileStore struct {
	mu         sync.RWMutex
	path       string
	results    map[string]Result
	maxEntries int
	maxBytes   int64
}

func NewFileStore(path string) (*FileStore, error) {
	store := &FileStore{path: path, results: make(map[string]Result), maxEntries: DefaultResultLimit, maxBytes: DefaultResultBytes}

	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(contents, &store.results); err != nil {
		return nil, err
	}

	return store, nil
}

func NewFileStoreWithLimits(path string, maxEntries int, maxBytes int64) (*FileStore, error) {
	store, err := NewFileStore(path)
	if err != nil {
		return nil, err
	}
	if maxEntries > 0 {
		store.maxEntries = maxEntries
	}
	if maxBytes > 0 {
		store.maxBytes = maxBytes
	}
	return store, nil
}

func (store *FileStore) Get(key string) (Result, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	result, found := store.results[key]
	return result, found
}

func (store *FileStore) Save(key string, result Result) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	previous := cloneResults(store.results)
	store.results[key] = result
	if contents, err := json.Marshal(map[string]Result{key: result}); err != nil {
		store.results = previous
		return err
	} else if int64(len(contents)) > store.maxBytes {
		store.results = previous
		return fmt.Errorf("%w: %d bytes exceeds limit %d", ErrResultTooLarge, len(contents), store.maxBytes)
	}
	store.prune()
	if err := store.persist(); err != nil {
		if !durable.IsCommitted(err) {
			store.results = previous
		}
		return err
	}

	return nil
}

func cloneResults(results map[string]Result) map[string]Result {
	clone := make(map[string]Result, len(results))
	for key, result := range results {
		clone[key] = result
	}
	return clone
}

func (store *FileStore) Reset() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	previous := store.results
	store.results = map[string]Result{}

	if err := store.persist(); err != nil {
		if !durable.IsCommitted(err) {
			store.results = previous
		}
		return err
	}
	return nil
}

func (store *FileStore) persist() error {
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return err
	}

	contents, err := json.Marshal(store.results)
	if err != nil {
		return err
	}

	return durable.AtomicWrite(store.path, contents, 0o600)
}

func (store *FileStore) prune() {
	for len(store.results) > store.maxEntries {
		delete(store.results, oldestKey(store.results))
	}
	for {
		contents, err := json.Marshal(store.results)
		if err != nil || int64(len(contents)) <= store.maxBytes || len(store.results) == 0 {
			return
		}
		delete(store.results, oldestKey(store.results))
	}
}

func oldestKey(results map[string]Result) string {
	keys := make([]string, 0, len(results))
	for key := range results {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys[0]
}
