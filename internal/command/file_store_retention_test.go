package command

import (
	"os"
	"path/filepath"
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

func TestFileStoreRejectsCorruptState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileStore(path); err == nil {
		t.Fatal("expected corrupt state error")
	}
}
