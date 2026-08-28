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

// A hosting account has more than one database user: the one its application
// connects as, and the extra ones a customer makes for a second application or
// a read-only tool. None of this SQL had ever been run against a server.
func TestMariaDBUsersAreSeparateFromDatabases(t *testing.T) {
	manager, store := realManager(t)
	store.Save(site.State{SiteID: "example.pe", Domain: "example.pe"})

	if _, err := manager.Create("example.pe", "shop_db", "shop_user", "shop-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateUser("example.pe", "reader", "reader-pw"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// A user with no grant yet must not already be able to open the database.
	if err := connect("reader", "reader-pw", "shop_db"); err == nil {
		t.Fatal("a user with no grant opened the database")
	}

	if _, err := manager.Grant("example.pe", "shop_db", "reader"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := connect("reader", "reader-pw", "shop_db"); err != nil {
		t.Fatalf("the granted user cannot open the database: %v", err)
	}

	if _, err := manager.Revoke("example.pe", "shop_db", "reader"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := connect("reader", "reader-pw", "shop_db"); err == nil {
		t.Fatal("the database is still open after the grant was revoked")
	}

	if _, err := manager.DeleteUser("example.pe", "reader", true); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	if err := connect("reader", "reader-pw", "shop_db"); err == nil {
		t.Fatal("a deleted user can still authenticate")
	}
}

// Deleting one database must not cut off another that the same user holds.
func TestMariaDBDeletingADatabaseKeepsAUserAnotherStillNeeds(t *testing.T) {
	manager, store := realManager(t)
	store.Save(site.State{SiteID: "example.pe", Domain: "example.pe"})

	if _, err := manager.Create("example.pe", "first_db", "shared_user", "shared-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create("example.pe", "second_db", "second_owner", "second-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Grant("example.pe", "second_db", "shared_user"); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Delete("example.pe", "first_db", "shared_user", true); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := connect("shared_user", "shared-pw", "second_db"); err != nil {
		t.Fatalf("deleting one database cut off another the user still holds: %v", err)
	}
}

// And when nothing else is reachable through it, the user goes with it: an
// account left behind is a credential nobody is watching.
func TestMariaDBDeletingTheLastDatabaseTakesItsUser(t *testing.T) {
	manager, store := realManager(t)
	store.Save(site.State{SiteID: "example.pe", Domain: "example.pe"})

	if _, err := manager.Create("example.pe", "only_db", "only_user", "only-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Delete("example.pe", "only_db", "only_user", true); err != nil {
		t.Fatal(err)
	}

	if err := connect("only_user", "only-pw", "mysql"); err == nil {
		t.Fatal("the user outlived the only database it held")
	}
}

// One customer's user must not be usable against another customer's database,
// which on a shared host is the whole of the isolation between them.
func TestMariaDBGrantsCannotCrossSites(t *testing.T) {
	manager, store := realManager(t)
	store.Save(site.State{SiteID: "alpha.pe", Domain: "alpha.pe"})
	store.Save(site.State{SiteID: "beta.pe", Domain: "beta.pe"})

	if _, err := manager.Create("alpha.pe", "alpha_db", "alpha_user", "alpha-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateUser("beta.pe", "beta_reader", "beta-pw"); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Grant("alpha.pe", "alpha_db", "beta_reader"); err == nil {
		t.Fatal("another site's user was granted on this site's database")
	}
	if _, err := manager.Grant("beta.pe", "alpha_db", "beta_reader"); err == nil {
		t.Fatal("a database from another site was granted")
	}
}
