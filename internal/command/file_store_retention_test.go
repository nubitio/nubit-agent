package command

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStoreEvictsResultsAtConfiguredLimit(t *testing.T) {
	store, err := NewFileStoreWithLimits(filepath.Join(t.TempDir(), "commands.json"), 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b", "c"} {
		if err := store.Save(key, Result{CommandID: key}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := store.Get("a"); ok {
		t.Fatal("oldest result was not evicted")
	}
	if got := len(store.results); got != 2 {
		t.Fatalf("got %d results", got)
	}
}

func TestFileStoreRejectsIndividualOversizedResult(t *testing.T) {
	store, err := NewFileStoreWithLimits(filepath.Join(t.TempDir(), "commands.json"), 10, 128)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Save("large", Result{CommandID: "large", Output: []byte(`"` + strings.Repeat("x", 256) + `"`)})
	if !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("expected ErrResultTooLarge, got %v", err)
	}
}

func TestFileStoreRejectsCorruptState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path); err == nil {
		t.Fatal("expected corrupt state error")
	}
}
