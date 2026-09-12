package command

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoreRetainsResultAfterReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "commands.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	err = store.Save("service_1:ping", Result{
		CommandID: "cmd_1",
		Status:    "succeeded",
		Output:    json.RawMessage(`{"receivedAt":"2026-08-25T00:00:00Z"}`),
	})
	if err != nil {
		t.Fatalf("save result: %v", err)
	}

	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	result, found := reopened.Get("service_1:ping")
	if !found {
		t.Fatal("expected persisted result")
	}
	if result.CommandID != "cmd_1" {
		t.Fatalf("expected command id cmd_1, got %q", result.CommandID)
	}
}

func TestFileStoreRetainsExistingResultWhenWriteFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	original := Result{CommandID: "old", Status: "failed"}
	if err := store.Save("key", original); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("key", Result{CommandID: "new"}); err == nil {
		t.Fatal("expected atomic write failure")
	}
	if got, ok := store.Get("key"); !ok || got.CommandID != original.CommandID {
		t.Fatalf("existing result was not retained: %#v, %t", got, ok)
	}
}
