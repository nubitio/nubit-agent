package command

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type FileStore struct {
	mu      sync.RWMutex
	path    string
	results map[string]Result
}

func NewFileStore(path string) (*FileStore, error) {
	store := &FileStore{
		path:    path,
		results: make(map[string]Result),
	}

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

func (store *FileStore) Get(key string) (Result, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	result, found := store.results[key]
	return result, found
}

func (store *FileStore) Save(key string, result Result) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	store.results[key] = result
	if err := store.persist(); err != nil {
		delete(store.results, key)
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

	temporaryPath := store.path + ".tmp"
	if err := os.WriteFile(temporaryPath, contents, 0o600); err != nil {
		return err
	}

	return os.Rename(temporaryPath, store.path)
}
