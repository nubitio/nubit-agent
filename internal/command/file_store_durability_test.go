package command

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreKeepsNewAndReusedKeysDuringEviction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.json")
	store, err := NewFileStoreWithLimits(path, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := store.Save("b", Result{CommandID: "b", CreatedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("c", Result{CommandID: "c", CreatedAt: old.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("a", Result{CommandID: "a"}); !errors.Is(err, ErrResultLimit) {
		t.Fatalf("expected tombstone retention error, got %v", err)
	}
	if _, ok := store.Get("a"); !ok {
		t.Fatal("newly written key was evicted")
	}
	if err := store.Save("b", Result{CommandID: "b"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("b"); !ok {
		t.Fatal("reused key was evicted")
	}
}

func TestFileStoreRejectsNullState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.json")
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path); err == nil {
		t.Fatal("expected null state to be rejected")
	}
}

func TestFileStoreAppliesConfiguredLimitDuringStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.json")
	results := map[string]Result{
		"old": {CommandID: "old", CreatedAt: time.Now().Add(-2 * time.Hour)},
		"mid": {CommandID: "mid", CreatedAt: time.Now().Add(-time.Hour)},
		"new": {CommandID: "new", CreatedAt: time.Now()},
	}
	contents, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStoreWithLimits(path, 2, 1<<20); !errors.Is(err, ErrResultLimit) {
		t.Fatalf("expected startup limit refusal, got %v", err)
	}
}
