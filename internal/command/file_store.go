package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nubitio/nubit-agent/internal/durable"
)

var ErrResultTooLarge = errors.New("command result exceeds configured size limit")
var ErrResultLimit = errors.New("command result retention limit reached")

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
	if store.results == nil {
		return nil, durable.Invalid(path, errors.New("expected an object, got null"))
	}
	if err := store.enforceLimits(""); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(store.results)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(contents, normalized) {
		if err := store.persist(); err != nil {
			return nil, err
		}
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
	before, err := json.Marshal(store.results)
	if err != nil {
		return nil, err
	}
	if err := store.enforceLimits(""); err != nil {
		return nil, err
	}
	after, err := json.Marshal(store.results)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(before, after) {
		if err := store.persist(); err != nil {
			return nil, err
		}
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
	if result.CreatedAt.IsZero() {
		result.CreatedAt = time.Now().UTC()
	}
	store.results[key] = result
	if contents, err := json.Marshal(map[string]Result{key: result}); err != nil {
		store.results = previous
		return err
	} else if int64(len(contents)) > store.maxBytes {
		store.results = previous
		return fmt.Errorf("%w: %d bytes exceeds limit %d", ErrResultTooLarge, len(contents), store.maxBytes)
	}
	if liveResultCount(store.results) > store.maxEntries {
		return store.saveTombstone(key, previous, ErrResultLimit)
	}
	if contents, err := json.Marshal(store.results); err != nil {
		store.results = previous
		return err
	} else if int64(len(contents)) > store.maxBytes {
		return store.saveTombstone(key, previous, ErrResultTooLarge)
	}
	if err := store.persist(); err != nil {
		if !durable.IsCommitted(err) {
			store.results = previous
		}
		return err
	}

	return nil
}

func (store *FileStore) saveTombstone(key string, previous map[string]Result, cause error) error {
	tombstone := Result{CommandID: key, Status: "failed", Tombstone: true, CreatedAt: store.results[key].CreatedAt,
		Output: json.RawMessage(fmt.Sprintf(`{"error":%q}`, cause.Error()))}
	store.results[key] = tombstone
	if err := store.persist(); err != nil {
		if !durable.IsCommitted(err) {
			store.results = previous
		}
		return err
	}
	return cause
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

func (store *FileStore) enforceLimits(protected string) error {
	if store.results == nil {
		return durable.Invalid(store.path, errors.New("expected an object, got null"))
	}
	contents, err := json.Marshal(store.results)
	if err != nil {
		return err
	}
	if liveResultCount(store.results) > store.maxEntries {
		return fmt.Errorf("%w: %d live entries exceeds limit %d", ErrResultLimit, liveResultCount(store.results), store.maxEntries)
	}
	if int64(len(contents)) > store.maxBytes {
		return fmt.Errorf("%w: serialized results are %d bytes, limit is %d", ErrResultTooLarge, len(contents), store.maxBytes)
	}
	return nil
}

func liveResultCount(results map[string]Result) int {
	count := 0
	for _, result := range results {
		if !result.Tombstone {
			count++
		}
	}
	return count
}
