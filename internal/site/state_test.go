package site

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestFileStateStorePersistsSitesAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sites.json")
	store, err := NewFileStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := State{SiteID: "example.com", Domain: "example.com", SystemUser: "site-example", PHPVersion: "8.4"}
	if err := store.Save(expected); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewFileStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	actual, found := reopened.Get("example.com")
	if !found || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("unexpected persisted state: %#v", actual)
	}
}
