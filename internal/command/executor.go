package command

import (
	"encoding/json"
	"errors"
	"sync"
	"time"
)

type Result struct {
	CommandID string          `json:"commandId"`
	Status    string          `json:"status"`
	Output    json.RawMessage `json:"output"`
}

type Store interface {
	Get(key string) (Result, bool)
	Save(key string, result Result) error
}

type MemoryStore struct {
	mu      sync.RWMutex
	results map[string]Result
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{results: make(map[string]Result)}
}

func (store *MemoryStore) Get(key string) (Result, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	result, found := store.results[key]
	return result, found
}

func (store *MemoryStore) Save(key string, result Result) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	store.results[key] = result

	return nil
}

type Executor struct {
	mu    sync.Mutex
	store Store
}

func NewExecutor(store Store) *Executor {
	return &Executor{store: store}
}

func (executor *Executor) Execute(command Command) (Result, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()

	if err := command.Validate(); err != nil {
		return Result{}, err
	}
	if result, found := executor.store.Get(command.IdempotencyKey); found {
		return result, nil
	}

	var output []byte
	var err error
	switch command.Type {
	case SystemPing:
		output, err = json.Marshal(map[string]string{"receivedAt": time.Now().UTC().Format(time.RFC3339)})
	case SiteCreate:
		site, parseErr := parseSiteCreate(command.Payload)
		if parseErr != nil { return Result{}, parseErr }
		output, err = json.Marshal(map[string]string{"domain": site.Domain, "systemUser": site.SystemUser, "documentRoot": "/srv/nubit/sites/" + site.Domain + "/public", "status": "planned"})
	default:
		return Result{}, errors.New("unsupported command type")
	}
	if err != nil {
		return Result{}, err
	}

	result := Result{
		CommandID: command.ID,
		Status:    "succeeded",
		Output:    output,
	}
	if err := executor.store.Save(command.IdempotencyKey, result); err != nil {
		return Result{}, err
	}

	return result, nil
}
