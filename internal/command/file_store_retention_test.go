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
		err := store.Save(key, Result{CommandID: key})
		if key != "c" && err != nil {
			t.Fatal(err)
		}
		if key == "c" && !errors.Is(err, ErrResultLimit) {
			t.Fatalf("expected retention limit, got %v", err)
		}
	}
	if _, ok := store.Get("a"); !ok {
		t.Fatal("idempotency key was evicted")
	}
	if got := len(store.results); got != 3 {
		t.Fatalf("got %d retained keys", got)
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
