package command

import (
	"encoding/json"
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
