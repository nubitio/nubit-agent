package database

import (
	"strings"
	"testing"

	"github.com/nubitio/nubit-agent/internal/site"
)

type fakeRunner struct{ queries []string }

func (runner *fakeRunner) Execute(query string) error {
	runner.queries = append(runner.queries, query)
	return nil
}

func TestDatabaseLifecycleIsScopedToSite(t *testing.T) {
	sites := site.NewMemoryStateStore()
	if err := sites.Save(site.State{SiteID: "example.com"}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	manager := Manager{Runner: runner, Sites: sites}
	if _, err := manager.Create("example.com", "example_db", "example_role", "secret'password"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.queries[0], "secret''password") {
		t.Fatal("expected password to be escaped as a SQL literal")
	}
	if _, err := manager.RotatePassword("example.com", "example_db", "example_role", "new-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Delete("example.com", "example_db", "example_role", true); err != nil {
		t.Fatal(err)
	}
	state, _ := sites.Get("example.com")
	if len(state.Databases) != 0 {
		t.Fatalf("database still belongs to site: %#v", state.Databases)
	}
}

func TestMariaDBCreateSQLUsesMysqlUsers(t *testing.T) {
	t.Setenv("NUBIT_DATABASE_ENGINE", "mariadb")
	sites := site.NewMemoryStateStore()
	if err := sites.Save(site.State{SiteID: "example.com"}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	if _, err := (Manager{Runner: runner, Sites: sites}).Create("example.com", "example_db", "example_role", "secret"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.queries[0], "CREATE DATABASE IF NOT EXISTS `example_db`") {
		t.Fatalf("expected mariadb create SQL, got %q", runner.queries[0])
	}
	if strings.Contains(runner.queries[0], "CREATE ROLE") {
		t.Fatal("mariadb must not emit postgres CREATE ROLE")
	}
}

func TestDatabaseDeletionRequiresConfirmation(t *testing.T) {
	sites := site.NewMemoryStateStore()
	_ = sites.Save(site.State{SiteID: "example.com", Databases: []string{"example_db"}})
	_, err := (Manager{Runner: &fakeRunner{}, Sites: sites}).Delete("example.com", "example_db", "example_role", false)
	if err == nil {
		t.Fatal("expected confirmation error")
	}
}
