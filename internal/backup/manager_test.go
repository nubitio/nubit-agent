package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nubitio/nubit-agent/internal/objectstore"
	"github.com/nubitio/nubit-agent/internal/site"
)

// memoryBlobs is an in-process BlobStore.
type memoryBlobs struct {
	objects map[string][]byte
}

func newMemoryBlobs() *memoryBlobs { return &memoryBlobs{objects: map[string][]byte{}} }

func (m *memoryBlobs) Put(_ context.Context, key string, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	m.objects[key] = data
	return nil
}

func (m *memoryBlobs) Get(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := m.objects[key]
	if !ok {
		return nil, errors.New("no such key: " + key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memoryBlobs) List(_ context.Context, prefix string) ([]objectstore.Object, error) {
	var out []objectstore.Object
	for key, data := range m.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		out = append(out, objectstore.Object{Key: key, Size: int64(len(data)), LastModified: time.Now()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (m *memoryBlobs) Delete(_ context.Context, key string) error {
	delete(m.objects, key)
	return nil
}

// fakeDB writes a shell script that stands in for mariadb-dump / mariadb.
func fakeDumpBin(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	path := filepath.Join(t.TempDir(), "dump.sh")
	script := "#!/bin/sh\necho \"-- dump $*\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeRestoreBin(t *testing.T, sink string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "restore.sh")
	script := "#!/bin/sh\ncat >> " + sink + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func testManager(t *testing.T, root string, databases ...string) (Manager, *memoryBlobs) {
	t.Helper()
	states := site.NewMemoryStateStore()
	if err := states.Save(site.State{SiteID: "example.pe", DocumentRoot: root, Databases: databases}); err != nil {
		t.Fatal(err)
	}
	blobs := newMemoryBlobs()
	return Manager{
		Sites:      states,
		Blobs:      blobs,
		TempDir:    t.TempDir(),
		DumpBin:    fakeDumpBin(t),
		RestoreBin: fakeRestoreBin(t, filepath.Join(t.TempDir(), "imported.sql")),
	}, blobs
}

func TestCreateStoresFilesAndDumpsThenRestoreReverts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.php"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := filepath.Join(t.TempDir(), "imported.sql")
	manager, blobs := testManager(t, root, "example_db")
	manager.RestoreBin = fakeRestoreBin(t, sink)

	archive, err := manager.Create("example.pe", 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasSuffix(archive.Name, ".tar.gz") {
		t.Fatalf("archive name = %q", archive.Name)
	}

	stored, ok := blobs.objects["example.pe/"+archive.Name]
	if !ok {
		t.Fatalf("archive not uploaded; keys: %v", keysOf(blobs))
	}
	entries := tarEntries(t, stored)
	for _, want := range []string{manifestName, filesPrefix + "index.php", databasePrefix + "example_db.sql"} {
		if _, present := entries[want]; !present {
			t.Fatalf("archive missing %q; has %v", want, keys(entries))
		}
	}
	if !strings.Contains(string(entries[databasePrefix+"example_db.sql"]), "-- dump") {
		t.Fatalf("db entry not the dump output: %q", entries[databasePrefix+"example_db.sql"])
	}

	// Mutate the site, then restore.
	if err := os.WriteFile(filepath.Join(root, "index.php"), []byte("hacked"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manager.Restore("example.pe", archive.Name, true); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "index.php"))
	if string(got) != "original" {
		t.Fatalf("file after restore = %q, want %q", got, "original")
	}
	imported, _ := os.ReadFile(sink)
	if !strings.Contains(string(imported), "-- dump") {
		t.Fatalf("dump not piped to the restore client: %q", imported)
	}
}

func TestListNewestFirst(t *testing.T) {
	manager, blobs := testManager(t, t.TempDir())
	blobs.objects["example.pe/20240101T000000Z.tar.gz"] = []byte("a")
	blobs.objects["example.pe/20240202T000000Z.tar.gz"] = []byte("bb")
	blobs.objects["example.pe/not-a-backup.txt"] = []byte("x")

	list, err := manager.List("example.pe")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "20240202T000000Z.tar.gz" {
		t.Fatalf("List = %+v", list)
	}
}

func TestBackupCommandsRefusedWithoutStore(t *testing.T) {
	states := site.NewMemoryStateStore()
	_ = states.Save(site.State{SiteID: "example.pe", DocumentRoot: t.TempDir()})
	manager := Manager{Sites: states}

	if _, err := manager.Create("example.pe", 0); err == nil {
		t.Fatal("Create without a store should fail")
	}
	if _, err := manager.List("example.pe"); err == nil {
		t.Fatal("List without a store should fail")
	}
	if err := manager.Restore("example.pe", "x.tar.gz", true); err == nil {
		t.Fatal("Restore without a store should fail")
	}
}

func TestCreateRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	manager, _ := testManager(t, root)
	if _, err := manager.Create("example.pe", 0); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Create error = %v, want symlink rejection", err)
	}
}

func TestRestoreRejectsTraversalAndSymlinkParents(t *testing.T) {
	root := t.TempDir()
	manager, blobs := testManager(t, root)

	blobs.objects["example.pe/unsafe.tar.gz"] = tarGz(t, filesPrefix+"../outside.txt", "no")
	if err := manager.Restore("example.pe", "unsafe.tar.gz", true); err == nil {
		t.Fatal("Restore accepted a traversal archive")
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	blobs.objects["example.pe/symlink.tar.gz"] = tarGz(t, filesPrefix+"linked/file.txt", "no")
	if err := manager.Restore("example.pe", "symlink.tar.gz", true); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Restore error = %v, want symlink rejection", err)
	}
}

func TestPruneKeepsEveryArchiveInsideTheRetentionWindow(t *testing.T) {
	manager, blobs := testManager(t, t.TempDir())
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	// One archive every 6 hours for 10 days: 40 archives, denser than the
	// keepArchives floor, so a 7-day window must keep well more than 7.
	for i := 0; i < 40; i++ {
		name := now.Add(-time.Duration(i)*6*time.Hour).Format(archiveLayout) + ".tar.gz"
		blobs.objects["example.pe/"+name] = []byte("x")
	}

	manager.prune("example.pe", 7, now)

	remaining := keysOf(blobs)
	cutoff := now.Add(-7 * 24 * time.Hour)
	if len(remaining) < 20 {
		t.Fatalf("kept only %d archives; a 7-day window at 6h cadence should keep ~28", len(remaining))
	}
	for _, key := range remaining {
		ts := archiveTime(filepath.Base(key))
		if !ts.After(cutoff) {
			// The only pre-window archive allowed to survive is one held by
			// the keepArchives floor — impossible here, there are 28 inside.
			t.Fatalf("kept an archive outside the retention window: %s", key)
		}
	}
}

func TestPruneFallsBackToCountWhenRetentionIsZero(t *testing.T) {
	manager, blobs := testManager(t, t.TempDir())
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for day := 0; day < 12; day++ {
		name := now.AddDate(0, 0, -day).Format(archiveLayout) + ".tar.gz"
		blobs.objects["example.pe/"+name] = []byte("x")
	}

	manager.prune("example.pe", 0, now)

	if got := len(keysOf(blobs)); got != keepArchives {
		t.Fatalf("kept %d archives, want %d (count-only fallback)", got, keepArchives)
	}
}

func TestVerifyPassesOnAHealthyArchive(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.php"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, _ := testManager(t, root, "example_db")

	if _, err := manager.Create("example.pe", 0); err != nil {
		t.Fatalf("Create: %v", err)
	}

	result, err := manager.Verify("example.pe")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !result.Verified {
		t.Fatalf("Verified = false, reason = %q", result.Reason)
	}
	if result.Files < 1 || result.Databases != 1 {
		t.Fatalf("counts: files=%d databases=%d", result.Files, result.Databases)
	}
	if result.DurationSeconds < 0 {
		t.Fatalf("DurationSeconds = %d", result.DurationSeconds)
	}
}

func TestVerifyReportsACorruptArchiveWithoutError(t *testing.T) {
	manager, blobs := testManager(t, t.TempDir())
	blobs.objects["example.pe/20260101T000000Z.tar.gz"] = []byte("not a gzip stream")

	result, err := manager.Verify("example.pe")
	if err != nil {
		t.Fatalf("Verify returned an error for a bad archive: %v", err)
	}
	if result.Verified || result.Reason == "" {
		t.Fatalf("expected Verified:false with a reason, got %+v", result)
	}
}

func TestVerifyErrorsWhenThereIsNothingToVerify(t *testing.T) {
	manager, _ := testManager(t, t.TempDir())
	if _, err := manager.Verify("example.pe"); err == nil {
		t.Fatal("Verify accepted a site with no archives")
	}
}

// --- helpers --------------------------------------------------------------

func tarGz(t *testing.T, name, contents string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarEntries(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out[header.Name] = body
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func keysOf(b *memoryBlobs) []string { return keys(b.objects) }
