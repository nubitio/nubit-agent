package controlplane

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestFileOutboxPersistsUntilResultIsDeleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.json")
	outbox, err := NewFileOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	pending := PendingResult{CommandID: "cmd-1", Status: "succeeded", Output: json.RawMessage(`{"ok":true}`)}
	if err := outbox.Put(pending); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileOutbox(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.List(); len(got) != 1 || got[0].CommandID != "cmd-1" {
		t.Fatalf("unexpected pending results: %#v", got)
	}
	if err := reopened.Delete("cmd-1"); err != nil {
		t.Fatal(err)
	}
	if got := reopened.List(); len(got) != 0 {
		t.Fatalf("expected empty outbox, got %#v", got)
	}
}
