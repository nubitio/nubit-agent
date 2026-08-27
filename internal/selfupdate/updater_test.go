package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNewerOnlyMovesForward(t *testing.T) {
	cases := []struct {
		current   string
		candidate string
		want      bool
	}{
		{"v1.0.0", "v1.0.1", true},
		{"v1.0.0", "v1.1.0", true},
		{"v1.0.0", "v2.0.0", true},
		{"v1.2.3", "v1.2.3", false},
		{"v1.2.3", "v1.2.2", false},
		{"v2.0.0", "v1.9.9", false},
		// A source build has no ordering against a release, so it never updates.
		{"dev", "v1.0.0", false},
		{"", "v1.0.0", false},
		{"v1.0.0", "", false},
		{"v1.0.0", "not-a-version", false},
		{"v1.0.0-rc1", "v1.0.0", false},
		{"v1.0.0", "v1.0.1-rc1", true},
	}
	for _, testCase := range cases {
		if got := newer(testCase.current, testCase.candidate); got != testCase.want {
			t.Errorf("newer(%q, %q) = %v, want %v", testCase.current, testCase.candidate, got, testCase.want)
		}
	}
}

// releaseServer serves the GitHub endpoints the updater consumes: the latest
// release document, SHA256SUMS, and the platform asset itself.
func releaseServer(t *testing.T, tag string, payload []byte, corruptSum bool) *httptest.Server {
	t.Helper()

	asset := AssetName(runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	if corruptSum {
		digest = hex.EncodeToString(make([]byte, sha256.Size))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/nubitio/nubit-agent/releases/latest", func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(writer, `{"tag_name":%q,"draft":false,"prerelease":false}`, tag)
	})
	mux.HandleFunc("/nubitio/nubit-agent/releases/download/"+tag+"/SHA256SUMS", func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(writer, "%s  %s\n", digest, asset)
	})
	mux.HandleFunc("/nubitio/nubit-agent/releases/download/"+tag+"/"+asset, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(payload)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

func stagedUpdater(t *testing.T, server *httptest.Server, current string) (*Updater, string) {
	t.Helper()

	binary := filepath.Join(t.TempDir(), "nubit-agent")
	if err := os.WriteFile(binary, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("seed the current binary: %v", err)
	}
	updater, err := New(Config{
		CurrentVersion: current,
		Repository:     "nubitio/nubit-agent",
		BinaryPath:     binary,
		APIBaseURL:     server.URL,
		DownloadURL:    server.URL,
		HTTPClient:     server.Client(),
	})
	if err != nil {
		t.Fatalf("build the updater: %v", err)
	}

	return updater, binary
}

func TestStageReplacesTheBinaryAndArmsRestart(t *testing.T) {
	payload := []byte("new binary contents")
	server := releaseServer(t, "v9.9.9", payload, false)
	updater, binary := stagedUpdater(t, server, "v1.0.0")

	staged, err := updater.Stage(context.Background())
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if staged != "v9.9.9" {
		t.Fatalf("staged = %q, want v9.9.9", staged)
	}
	if !updater.RestartPending() {
		t.Fatal("RestartPending = false, want true after staging")
	}

	installed, err := os.ReadFile(binary)
	if err != nil {
		t.Fatalf("read the installed binary: %v", err)
	}
	if string(installed) != string(payload) {
		t.Fatalf("installed binary = %q, want %q", installed, payload)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatalf("stat the installed binary: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode = %v, want 0755", info.Mode().Perm())
	}
}

func TestStageRejectsAChecksumMismatch(t *testing.T) {
	server := releaseServer(t, "v9.9.9", []byte("tampered"), true)
	updater, binary := stagedUpdater(t, server, "v1.0.0")

	if _, err := updater.Stage(context.Background()); err == nil {
		t.Fatal("Stage succeeded on a checksum mismatch, want an error")
	}
	if updater.RestartPending() {
		t.Fatal("RestartPending = true after a rejected update, want false")
	}

	// The running binary must survive a rejected download untouched.
	current, err := os.ReadFile(binary)
	if err != nil {
		t.Fatalf("read the current binary: %v", err)
	}
	if string(current) != "old binary" {
		t.Fatalf("binary was modified by a rejected update: %q", current)
	}
	entries, err := os.ReadDir(filepath.Dir(binary))
	if err != nil {
		t.Fatalf("read the install directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("staging left %d files behind, want only the binary", len(entries))
	}
}

func TestStageIsANoOpWhenAlreadyCurrent(t *testing.T) {
	server := releaseServer(t, "v1.0.0", []byte("same version"), false)
	updater, binary := stagedUpdater(t, server, "v1.0.0")

	staged, err := updater.Stage(context.Background())
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if staged != "" {
		t.Fatalf("staged = %q, want no update", staged)
	}
	if updater.RestartPending() {
		t.Fatal("RestartPending = true without a newer release, want false")
	}

	current, err := os.ReadFile(binary)
	if err != nil {
		t.Fatalf("read the current binary: %v", err)
	}
	if string(current) != "old binary" {
		t.Fatalf("binary was replaced with an equal version: %q", current)
	}
}

func TestStageIgnoresPrereleases(t *testing.T) {
	asset := AssetName(runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/nubitio/nubit-agent/releases/latest", func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `{"tag_name":"v9.9.9","draft":false,"prerelease":true}`)
	})
	mux.HandleFunc("/nubitio/nubit-agent/releases/download/v9.9.9/"+asset, func(http.ResponseWriter, *http.Request) {
		t.Error("a prerelease must never be downloaded")
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	updater, _ := stagedUpdater(t, server, "v1.0.0")
	staged, err := updater.Stage(context.Background())
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if staged != "" || updater.RestartPending() {
		t.Fatalf("staged a prerelease (%q, pending=%v)", staged, updater.RestartPending())
	}
}
