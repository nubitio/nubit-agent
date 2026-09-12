package controlplane

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileOutboxRejectsNewResultAtConfiguredLimit(t *testing.T) {
	outbox, err := NewFileOutboxWithLimits(filepath.Join(t.TempDir(), "outbox.json"), 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		err := outbox.Put(PendingResult{CommandID: id})
		if id != "c" && err != nil {
			t.Fatal(err)
		}
		if id == "c" && !errors.Is(err, ErrOutboxFull) {
			t.Fatalf("expected ErrOutboxFull, got %v", err)
		}
	}
	if got := outbox.List(); len(got) != 2 || got[0].CommandID != "a" || got[1].CommandID != "b" {
		t.Fatalf("unexpected retained results: %#v", got)
	}
}

func TestFileOutboxRejectsIndividualOversizedResult(t *testing.T) {
	outbox, err := NewFileOutboxWithLimits(filepath.Join(t.TempDir(), "outbox.json"), 10, 128)
	if err != nil {
		t.Fatal(err)
	}
	err = outbox.Put(PendingResult{CommandID: "large", Output: json.RawMessage(`"` + strings.Repeat("x", 256) + `"`)})
	if !errors.Is(err, ErrOutboxFull) {
		t.Fatalf("expected ErrOutboxFull, got %v", err)
	}
	if got := outbox.List(); len(got) != 0 {
		t.Fatalf("oversized result was retained: %#v", got)
	}
}
