package files

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/nubitio/nubit-agent/internal/site"
)

func TestListWriteReadDeleteStayInsideDocumentRoot(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "public")
	if err := os.Mkdir(public, 0o755); err != nil {
		t.Fatal(err)
	}
	store := site.NewMemoryStateStore()
	if err := store.Save(site.State{SiteID: "example.com", SystemUser: "nobody", DocumentRoot: public}); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Sites: store}

	if err := manager.Write("example.com", "index.html", []byte("<h1>hola</h1>")); err != nil {
		t.Fatal(err)
	}
	if err := manager.Mkdir("example.com", "img"); err != nil {
		t.Fatal(err)
	}
	listed, err := manager.List("example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if 2 != len(listed.Entries) {
		t.Fatalf("entries: %#v", listed.Entries)
	}

	read, err := manager.Read("example.com", "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if "<h1>hola</h1>" != string(read.Content) {
		t.Fatalf("content: %q", read.Content)
	}

	if err := manager.Delete("example.com", "index.html"); err != nil {
		t.Fatal(err)
	}
	listed, err = manager.List("example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if 1 != len(listed.Entries) || "img" != listed.Entries[0].Name {
		t.Fatalf("after delete: %#v", listed.Entries)
	}
}

func TestListReportsOwnerAndMode(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "public")
	if err := os.Mkdir(public, 0o755); err != nil {
		t.Fatal(err)
	}
	store := site.NewMemoryStateStore()
	if err := store.Save(site.State{SiteID: "example.com", SystemUser: "nobody", DocumentRoot: public}); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Sites: store}
	if err := manager.Write("example.com", "page.html", []byte("<h1>hola</h1>")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(public, "page.html"), 0o640); err != nil {
		t.Fatal(err)
	}

	listed, err := manager.List("example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if 1 != len(listed.Entries) {
		t.Fatalf("entries: %#v", listed.Entries)
	}
	entry := listed.Entries[0]
	if entry.Mode != "0640" {
		t.Fatalf("mode: %q", entry.Mode)
	}
	if entry.Owner == "" {
		t.Fatalf("owner was not reported: %#v", entry)
	}
}

func TestUnzipExtractsNextToTheArchive(t *testing.T) {
	root := t.TempDir()
	public := filepath.Join(root, "public")
	if err := os.MkdirAll(filepath.Join(public, "panel-test"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := site.NewMemoryStateStore()
	if err := store.Save(site.State{SiteID: "example.com", SystemUser: "nobody", DocumentRoot: public}); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(public, "panel-test", "pack.zip")
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("desde-zip.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("ok zip\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	if err := (Manager{Sites: store}).Unzip("example.com", "panel-test/pack.zip"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(public, "panel-test", "desde-zip.txt")); err != nil {
		t.Fatalf("expected extract next to zip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(public, "desde-zip.txt")); err == nil {
		t.Fatal("zip must not extract into the site root")
	}
}

func TestRejectsPathsOutsideTheSite(t *testing.T) {
	root := t.TempDir()
	store := site.NewMemoryStateStore()
	if err := store.Save(site.State{SiteID: "example.com", SystemUser: "nobody", DocumentRoot: root}); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Sites: store}

	for _, rel := range []string{"../passwd", "/etc/passwd", "foo/../../passwd"} {
		if err := manager.Write("example.com", rel, []byte("x")); err == nil {
			t.Fatalf("expected reject %q", rel)
		}
	}
	if err := manager.Delete("example.com", ""); err == nil {
		t.Fatal("expected refuse deleting the site root")
	}
}
