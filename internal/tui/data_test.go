package tui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFetchStatusDecodesSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"v9.9.9","transport":"token","polling":true,"outboxDepth":4,"siteCount":2}`))
	}))
	defer srv.Close()

	snap, err := fetchStatus(srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Version != "v9.9.9" || snap.Transport != "token" || !snap.Polling {
		t.Fatalf("decoded wrong: %+v", snap)
	}
	if snap.OutboxDepth != 4 || snap.SiteCount != 2 {
		t.Fatalf("counters wrong: %+v", snap)
	}
}

func TestFetchStatusErrorsWhenDown(t *testing.T) {
	if _, err := fetchStatus(http.DefaultClient, "http://127.0.0.1:1"); err == nil {
		t.Fatal("expected an error against a dead address")
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadJobsParsesAuditNDJSONNewestFirst(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, auditFile, `{"timestamp":"2026-09-04T10:00:00Z","command_type":"site.create","result":"ok","duration_ms":1200,"idempotency_key":"a"}
{"timestamp":"2026-09-04T11:00:00Z","command_type":"site.backup.create","result":"failed","duration_ms":50,"idempotency_key":"b"}
not-json
{"timestamp":"2026-09-04T09:00:00Z","command_type":"system.ping","result":"ok","duration_ms":3,"idempotency_key":"c"}
`)
	rows, err := loadJobs(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (bad line skipped)", len(rows))
	}
	if rows[0].Type != "site.backup.create" || rows[0].Result != "failed" {
		t.Fatalf("newest row wrong: %+v", rows[0])
	}
	if rows[2].Type != "system.ping" {
		t.Fatalf("oldest row wrong: %+v", rows[2])
	}
	if rows[0].Duration.Milliseconds() != 50 {
		t.Fatalf("duration not mapped: %v", rows[0].Duration)
	}
}

func TestLoadJobsMissingFileIsEmpty(t *testing.T) {
	rows, err := loadJobs(t.TempDir(), 10)
	if err != nil || rows != nil {
		t.Fatalf("want nil,nil for missing audit.log; got %v,%v", rows, err)
	}
}

func TestLoadOutboxSortsByCommandID(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, outboxFile, `{"z1":{"commandId":"z1","status":"failed","error":"boom"},"a1":{"commandId":"a1","status":"ok"}}`)
	got, err := loadOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].CommandID != "a1" || got[1].CommandID != "z1" {
		t.Fatalf("unsorted or wrong: %+v", got)
	}
	if got[1].Error != "boom" {
		t.Fatalf("error field lost: %+v", got[1])
	}
}

func TestLoadSitesFromMapKeyedByID(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, sitesFile, `{"b.pe":{"siteId":"b.pe","systemUser":"ub","phpVersion":"8.4","status":"active"},"a.pe":{"siteId":"a.pe","systemUser":"ua","phpVersion":"8.5","status":"suspended"}}`)
	got, err := loadSites(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].SiteID != "a.pe" || got[1].SiteID != "b.pe" {
		t.Fatalf("sites not sorted by id: %+v", got)
	}
}
