package controlplane

import (
	"path/filepath"
	"testing"
)

func TestFileOutboxEvictsOldestAtConfiguredLimit(t *testing.T) {
	outbox, err := NewFileOutboxWithLimits(filepath.Join(t.TempDir(), "outbox.json"), 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := outbox.Put(PendingResult{CommandID: id}); err != nil {
			t.Fatal(err)
		}
	}
	if got := outbox.List(); len(got) != 2 || got[0].CommandID != "b" || got[1].CommandID != "c" {
		t.Fatalf("unexpected retained results: %#v", got)
	}
}
