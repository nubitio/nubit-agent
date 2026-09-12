package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunNodeResetPropagatesCommandStoreResetError(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(stateDir, "commands.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := runNodeReset(stateDir, false)
	if err == nil || !strings.Contains(err.Error(), "load command results for reset") {
		t.Fatalf("expected command reset error, got %v", err)
	}
}
