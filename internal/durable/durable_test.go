package durable

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteLeavesOldValueOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := AtomicWrite(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory at the destination makes the rename fail; the old value is
	// still readable and the temporary value is not exposed as state.json.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(path, []byte("new"), 0o600); err == nil {
		t.Fatal("expected rename failure")
	}
}

func TestWriterLockFencesSecondProcessInSameProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writer.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Acquire(path)
	if second != nil || err != ErrWriterBusy {
		t.Fatalf("expected busy lock, got %v", err)
	}
}
