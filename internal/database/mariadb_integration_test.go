//go:build integration

package database

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/nubitio/nubit-agent/internal/site"
)

// The MariaDB statements were only ever compared as strings. Nothing had run
// one against a server, which is the half that decides whether a customer's
// database works at all.
func TestMariaDBLifecycleAgainstARealServer(t *testing.T) {
	manager, store := realManager(t)
	store.Save(site.State{SiteID: "example.pe", Domain: "example.pe"})

	if _, err := manager.Create("example.pe", "shop_db", "shop_user", "s3cret-pw"); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The point of the credentials is that they open the database. Asserting the
	// SQL was well formed would not have caught a missing grant.
	if err := connect("shop_user", "s3cret-pw", "shop_db"); err != nil {
		t.Fatalf("the site cannot reach its own database: %v", err)
	}

	// Creating the same database twice is how a retried command arrives.
	if _, err := manager.Create("example.pe", "shop_db", "shop_user", "s3cret-pw"); err != nil {
		t.Fatalf("a repeated create was not idempotent: %v", err)
	}

	if _, err := manager.RotatePassword("example.pe", "shop_db", "shop_user", "rotated-pw"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if err := connect("shop_user", "rotated-pw", "shop_db"); err != nil {
		t.Fatalf("the rotated password does not work: %v", err)
	}
	if err := connect("shop_user", "s3cret-pw", "shop_db"); err == nil {
		t.Fatal("the old password still opens the database after a rotation")
	}

	if _, err := manager.Delete("example.pe", "shop_db", "shop_user", true); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := connect("shop_user", "rotated-pw", "shop_db"); err == nil {
		t.Fatal("the credentials still work after the database was deleted")
	}
}

// One tenant's credentials must not open another tenant's data. On a shared
// host this is the whole of the isolation between two customers' databases.
func TestMariaDBTenantsCannotReachEachOther(t *testing.T) {
	manager, store := realManager(t)
	store.Save(site.State{SiteID: "alpha.pe", Domain: "alpha.pe"})
	store.Save(site.State{SiteID: "beta.pe", Domain: "beta.pe"})

	if _, err := manager.Create("alpha.pe", "alpha_db", "alpha_user", "alpha-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create("beta.pe", "beta_db", "beta_user", "beta-pw"); err != nil {
		t.Fatal(err)
	}

	if err := connect("beta_user", "beta-pw", "alpha_db"); err == nil {
		t.Fatal("beta opened alpha's database")
	}
	if err := connect("beta_user", "beta-pw", "beta_db"); err != nil {
		t.Fatalf("beta cannot reach its own database: %v", err)
	}
}

// MySQL treats a backslash as an escape character unless NO_BACKSLASH_ESCAPES
// is set, so doubling quotes alone does not close a string literal: `\'` is a
// quote, and the `”` meant to escape it ends the literal one character early.
// Everything after that is statement, not password.
func TestMariaDBPasswordCannotEscapeItsLiteral(t *testing.T) {
	manager, store := realManager(t)
	store.Save(site.State{SiteID: "example.pe", Domain: "example.pe"})

	hostile := `a\'; DROP DATABASE canary; -- `
	if _, err := manager.Create("example.pe", "canary", "canary_user", hostile); err != nil {
		t.Fatalf("create with a hostile password: %v", err)
	}
	if err := connect("canary_user", hostile, "canary"); err != nil {
		t.Fatalf("the password was not stored as written: %v", err)
	}

	if _, err := manager.RotatePassword("example.pe", "canary", "canary_user", `b\'; DROP DATABASE canary; -- `); err != nil {
		t.Fatalf("rotate with a hostile password: %v", err)
	}
	if err := connect("canary_user", `b\'; DROP DATABASE canary; -- `, "canary"); err != nil {
		t.Fatalf("the database did not survive the rotation: %v", err)
	}
}

func realManager(t *testing.T) (Manager, site.StateStore) {
	t.Helper()
	if os.Getenv("NUBIT_DEBIAN_INTEGRATION") != "1" {
		t.Skip("set NUBIT_DEBIAN_INTEGRATION=1 inside the disposable MariaDB container")
	}
	if !mariadb() {
		t.Fatal("set NUBIT_DATABASE_ENGINE=mariadb")
	}
	store := site.NewMemoryStateStore()

	return Manager{Runner: OSRunner{}, Sites: store}, store
}

func connect(user, password, database string) error {
	command := exec.Command("mysql", "--protocol=socket", "-u"+user, "-p"+password, database, "-e", "SELECT 1")
	output, err := command.CombinedOutput()
	if err != nil {
		return errorWith(string(output), err)
	}

	return nil
}

func errorWith(output string, err error) error {
	if trimmed := strings.TrimSpace(output); trimmed != "" {
		return &connectError{message: trimmed, cause: err}
	}

	return err
}

type connectError struct {
	message string
	cause   error
}

func (e *connectError) Error() string { return e.message }
func (e *connectError) Unwrap() error { return e.cause }
