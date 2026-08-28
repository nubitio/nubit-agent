package site

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type State struct {
	SiteID       string   `json:"siteId"`
	Domain       string   `json:"domain"`
	SystemUser   string   `json:"systemUser"`
	PHPVersion   string   `json:"phpVersion"`
	DocumentRoot string   `json:"documentRoot"`
	PHPSocket    string   `json:"phpSocket"`
	Status       string   `json:"status"`
	Domains      []string `json:"domains"`
	Databases    []string `json:"databases,omitempty"`
	// Database users exist apart from the databases they can open, the way a
	// hosting panel presents them: one user may hold several databases, and a
	// database may be reachable by several users.
	DatabaseUsers []string `json:"databaseUsers,omitempty"`
	// Grants maps a database to the users that may open it. Kept so deleting a
	// database can tell whether a user it was paired with is still needed
	// elsewhere, which is the difference between tidying up and cutting off
	// another database.
	DatabaseGrants map[string][]string `json:"databaseGrants,omitempty"`
	SFTPEnabled    bool                `json:"sftpEnabled"`
	// Kept with the site so every path that regenerates the pool — a version
	// change, a drift check — rebuilds it with the limits the site was sold,
	// instead of quietly resetting it to the default tier.
	Resources Resources `json:"resources"`
}

type StateStore interface {
	Get(siteID string) (State, bool)
	List() []State
	Save(state State) error
	Delete(siteID string) error
}

type MemoryStateStore struct {
	mu     sync.RWMutex
	states map[string]State
}

func NewMemoryStateStore() *MemoryStateStore {
	return &MemoryStateStore{states: make(map[string]State)}
}

func (store *MemoryStateStore) Get(siteID string) (State, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	state, found := store.states[siteID]
	return state, found
}

func (store *MemoryStateStore) Save(state State) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.states[state.SiteID] = state
	return nil
}

func (store *MemoryStateStore) List() []State {
	store.mu.RLock()
	defer store.mu.RUnlock()
	states := make([]State, 0, len(store.states))
	for _, state := range store.states {
		states = append(states, state)
	}
	return states
}

func (store *MemoryStateStore) Delete(siteID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.states, siteID)
	return nil
}

type FileStateStore struct {
	mu     sync.RWMutex
	path   string
	states map[string]State
}

func NewFileStateStore(path string) (*FileStateStore, error) {
	store := &FileStateStore{path: path, states: make(map[string]State)}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(contents, &store.states); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *FileStateStore) Get(siteID string) (State, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	state, found := store.states[siteID]
	return state, found
}

func (store *FileStateStore) Save(state State) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	previous, existed := store.states[state.SiteID]
	store.states[state.SiteID] = state
	if err := store.persist(); err != nil {
		if existed {
			store.states[state.SiteID] = previous
		} else {
			delete(store.states, state.SiteID)
		}
		return err
	}
	return nil
}

func (store *FileStateStore) List() []State {
	store.mu.RLock()
	defer store.mu.RUnlock()
	states := make([]State, 0, len(store.states))
	for _, state := range store.states {
		states = append(states, state)
	}
	return states
}

func (store *FileStateStore) Delete(siteID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	previous, found := store.states[siteID]
	if !found {
		return nil
	}
	delete(store.states, siteID)
	if err := store.persist(); err != nil {
		store.states[siteID] = previous
		return err
	}
	return nil
}

func (store *FileStateStore) persist() error {
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return err
	}
	contents, err := json.Marshal(store.states)
	if err != nil {
		return err
	}
	temporary := store.path + ".tmp"
	if err := os.WriteFile(temporary, contents, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, store.path)
}
