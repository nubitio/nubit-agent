package database

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/nubitio/nubit-agent/internal/site"
)

var safeName = regexp.MustCompile(`^[a-z][a-z0-9_]{2,62}$`)

type Runner interface {
	Execute(query string) error
}

type OSRunner struct{}

func (OSRunner) Execute(query string) error {
	if mariadb() {
		command := exec.Command("mysql", "-uroot")
		command.Stdin = strings.NewReader(query)
		if err := command.Run(); err != nil {
			return fmt.Errorf("mariadb command failed: %w", err)
		}
		return nil
	}
	command := exec.Command("runuser", "-u", "postgres", "--", "psql", "--no-psqlrc", "--set", "ON_ERROR_STOP=1")
	command.Stdin = strings.NewReader(query)
	if err := command.Run(); err != nil {
		return fmt.Errorf("postgres command failed: %w", err)
	}
	return nil
}

type Manager struct {
	Runner Runner
	Sites  site.StateStore
}

type Result struct {
	SiteID   string `json:"siteId"`
	Database string `json:"database"`
	Role     string `json:"role"`
	Status   string `json:"status"`
}

func (manager Manager) Create(siteID, database, role, password string) (result Result, err error) {
	if err := manager.validate(siteID, database, role, password); err != nil {
		return result, err
	}
	state, _ := manager.Sites.Get(siteID)
	if contains(state.Databases, database) {
		return Result{siteID, database, role, "active"}, nil
	}
	if err := manager.Runner.Execute(createSQL(database, role, password)); err != nil {
		return result, err
	}
	roleCreated := true
	defer func() {
		if err != nil && roleCreated {
			_ = manager.Runner.Execute(dropRoleSQL(role))
		}
	}()
	if !mariadb() {
		if err = manager.Runner.Execute(fmt.Sprintf("CREATE DATABASE %s OWNER %s;\n", identifier(database), identifier(role))); err != nil {
			return result, err
		}
	}
	state.Databases = append(state.Databases, database)
	// The role Create makes is a database user like any other. Recording it here
	// is what lets it be granted on a second database later, and what stops a
	// deletion elsewhere from dropping it while this database still needs it.
	state.DatabaseUsers = appendUnique(state.DatabaseUsers, role)
	state.DatabaseGrants = grant(state.DatabaseGrants, database, role)
	if err = manager.Sites.Save(state); err != nil {
		_ = manager.Runner.Execute(dropDatabaseSQL(database))
		return result, err
	}
	roleCreated = false
	return Result{siteID, database, role, "active"}, nil
}

func (manager Manager) RotatePassword(siteID, database, role, password string) (Result, error) {
	if err := manager.validate(siteID, database, role, password); err != nil {
		return Result{}, err
	}
	state, _ := manager.Sites.Get(siteID)
	if !contains(state.Databases, database) {
		return Result{}, errors.New("database does not belong to site")
	}
	if err := manager.Runner.Execute(rotateSQL(role, password)); err != nil {
		return Result{}, err
	}
	return Result{siteID, database, role, "active"}, nil
}

func (manager Manager) Delete(siteID, database, role string, confirmed bool) (Result, error) {
	if !confirmed {
		return Result{}, errors.New("database deletion requires explicit confirmation")
	}
	if err := manager.validate(siteID, database, role, "placeholder"); err != nil {
		return Result{}, err
	}
	state, _ := manager.Sites.Get(siteID)
	if !contains(state.Databases, database) {
		return Result{siteID, database, role, "absent"}, nil
	}
	if err := manager.Runner.Execute(dropDatabaseSQL(database)); err != nil {
		return Result{}, err
	}
	state.Databases = remove(state.Databases, database)
	delete(state.DatabaseGrants, database)
	// The role goes only when nothing else is reachable through it. Dropping a
	// user that still holds another database would cut off an application that
	// has nothing to do with this deletion.
	if !granted(state.DatabaseGrants, role) {
		if err := manager.Runner.Execute(dropRoleSQL(role)); err != nil {
			return Result{}, err
		}
		state.DatabaseUsers = remove(state.DatabaseUsers, role)
	}
	if err := manager.Sites.Save(state); err != nil {
		return Result{}, err
	}
	return Result{siteID, database, role, "deleted"}, nil
}

// UserResult reports a database user apart from any database.
type UserResult struct {
	SiteID string   `json:"siteId"`
	Role   string   `json:"role"`
	Grants []string `json:"grants"`
	Status string   `json:"status"`
}

// CreateUser adds a database user that holds nothing yet.
//
// Separate from Create because a hosting account has both: the user its
// application connects as, and the extra ones a customer makes for a second
// application or a read-only tool.
func (manager Manager) CreateUser(siteID, role, password string) (UserResult, error) {
	state, err := manager.userSite(siteID, role, password)
	if err != nil {
		return UserResult{}, err
	}
	if contains(state.DatabaseUsers, role) {
		// A retried command, or a user someone made twice. Rotating the password
		// here would lock out whatever is already connecting as it.
		return UserResult{siteID, role, grantsFor(state.DatabaseGrants, role), "active"}, nil
	}
	if err := manager.Runner.Execute(createUserSQL(role, password)); err != nil {
		return UserResult{}, err
	}
	state.DatabaseUsers = appendUnique(state.DatabaseUsers, role)
	if err := manager.Sites.Save(state); err != nil {
		_ = manager.Runner.Execute(dropRoleSQL(role))

		return UserResult{}, fmt.Errorf("persist site state: %w", err)
	}

	return UserResult{siteID, role, []string{}, "active"}, nil
}

// DeleteUser removes a database user and every grant it held.
func (manager Manager) DeleteUser(siteID, role string, confirmed bool) (UserResult, error) {
	if !confirmed {
		return UserResult{}, errors.New("deleting a database user requires explicit confirmation")
	}
	state, err := manager.userSite(siteID, role, "placeholder")
	if err != nil {
		return UserResult{}, err
	}
	if !contains(state.DatabaseUsers, role) {
		return UserResult{siteID, role, []string{}, "absent"}, nil
	}
	if err := manager.Runner.Execute(dropRoleSQL(role)); err != nil {
		return UserResult{}, err
	}
	state.DatabaseUsers = remove(state.DatabaseUsers, role)
	for database := range state.DatabaseGrants {
		state.DatabaseGrants[database] = remove(state.DatabaseGrants[database], role)
	}
	if err := manager.Sites.Save(state); err != nil {
		return UserResult{}, fmt.Errorf("persist site state: %w", err)
	}

	return UserResult{siteID, role, []string{}, "deleted"}, nil
}

// Grant lets an existing user open an existing database of the same site.
func (manager Manager) Grant(siteID, database, role string) (UserResult, error) {
	state, err := manager.pair(siteID, database, role)
	if err != nil {
		return UserResult{}, err
	}
	if !contains(state.DatabaseGrants[database], role) {
		if err := manager.Runner.Execute(grantSQL(database, role)); err != nil {
			return UserResult{}, err
		}
		state.DatabaseGrants = grant(state.DatabaseGrants, database, role)
		if err := manager.Sites.Save(state); err != nil {
			return UserResult{}, fmt.Errorf("persist site state: %w", err)
		}
	}

	return UserResult{siteID, role, grantsFor(state.DatabaseGrants, role), "active"}, nil
}

// Revoke takes that access away again.
func (manager Manager) Revoke(siteID, database, role string) (UserResult, error) {
	state, err := manager.pair(siteID, database, role)
	if err != nil {
		return UserResult{}, err
	}
	if contains(state.DatabaseGrants[database], role) {
		if err := manager.Runner.Execute(revokeSQL(database, role)); err != nil {
			return UserResult{}, err
		}
		state.DatabaseGrants[database] = remove(state.DatabaseGrants[database], role)
		if err := manager.Sites.Save(state); err != nil {
			return UserResult{}, fmt.Errorf("persist site state: %w", err)
		}
	}

	return UserResult{siteID, role, grantsFor(state.DatabaseGrants, role), "active"}, nil
}

// userSite validates a user-only command and returns the site it belongs to.
func (manager Manager) userSite(siteID, role, password string) (site.State, error) {
	if manager.Runner == nil || manager.Sites == nil {
		return site.State{}, errors.New("database manager is not configured")
	}
	state, found := manager.Sites.Get(siteID)
	if !found {
		return site.State{}, errors.New("site not found")
	}
	if !safeName.MatchString(role) {
		return site.State{}, errors.New("database role name is invalid")
	}
	if password == "" {
		return site.State{}, errors.New("database password is required")
	}

	return state, nil
}

// pair validates that both the database and the user belong to this site, which
// is what keeps one customer's user off another customer's database.
func (manager Manager) pair(siteID, database, role string) (site.State, error) {
	state, err := manager.userSite(siteID, role, "placeholder")
	if err != nil {
		return site.State{}, err
	}
	if !safeName.MatchString(database) {
		return site.State{}, errors.New("database name is invalid")
	}
	if !contains(state.Databases, database) {
		return site.State{}, errors.New("database does not belong to site")
	}
	if !contains(state.DatabaseUsers, role) {
		return site.State{}, errors.New("database user does not belong to site")
	}

	return state, nil
}

func (manager Manager) validate(siteID, database, role, password string) error {
	if manager.Runner == nil || manager.Sites == nil {
		return errors.New("database manager is not configured")
	}
	if _, found := manager.Sites.Get(siteID); !found {
		return errors.New("site not found")
	}
	if !safeName.MatchString(database) || !safeName.MatchString(role) {
		return errors.New("database or role name is invalid")
	}
	if password == "" {
		return errors.New("database password is required")
	}
	return nil
}

func mariadb() bool {
	engine := os.Getenv("NUBIT_DATABASE_ENGINE")
	return engine == "mariadb" || engine == "mysql"
}

func createSQL(database, role, password string) string {
	if mariadb() {
		return fmt.Sprintf(
			"CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY %s; CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY %s; CREATE USER IF NOT EXISTS '%s'@'127.0.0.1' IDENTIFIED BY %s; CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci; GRANT ALL ON `%s`.* TO '%s'@'%%'; GRANT ALL ON `%s`.* TO '%s'@'localhost'; GRANT ALL ON `%s`.* TO '%s'@'127.0.0.1'; FLUSH PRIVILEGES;\n",
			role, literal(password), role, literal(password), role, literal(password), database, database, role, database, role, database, role,
		)
	}
	return fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD %s;\n", identifier(role), literal(password))
}

func rotateSQL(role, password string) string {
	if mariadb() {
		return fmt.Sprintf(
			"CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY %s; CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY %s; CREATE USER IF NOT EXISTS '%s'@'127.0.0.1' IDENTIFIED BY %s; ALTER USER '%s'@'%%' IDENTIFIED BY %s; ALTER USER '%s'@'localhost' IDENTIFIED BY %s; ALTER USER '%s'@'127.0.0.1' IDENTIFIED BY %s;\n",
			role, literal(password), role, literal(password), role, literal(password), role, literal(password), role, literal(password), role, literal(password),
		)
	}
	return fmt.Sprintf("ALTER ROLE %s PASSWORD %s;\n", identifier(role), literal(password))
}

func dropDatabaseSQL(database string) string {
	if mariadb() {
		return fmt.Sprintf("DROP DATABASE IF EXISTS `%s`;\n", database)
	}
	return fmt.Sprintf("DROP DATABASE IF EXISTS %s;\n", identifier(database))
}

func dropRoleSQL(role string) string {
	if mariadb() {
		return fmt.Sprintf("DROP USER IF EXISTS '%s'@'%%'; DROP USER IF EXISTS '%s'@'localhost'; DROP USER IF EXISTS '%s'@'127.0.0.1';\n", role, role, role)
	}
	return fmt.Sprintf("DROP ROLE IF EXISTS %s;\n", identifier(role))
}

func createUserSQL(role, password string) string {
	if mariadb() {
		// One user per host the site can reach the server from. MariaDB treats
		// 'role'@'localhost' and 'role'@'127.0.0.1' as different accounts, and a
		// connection over TCP to the loopback address matches the second.
		return fmt.Sprintf(
			"CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY %s; CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY %s; CREATE USER IF NOT EXISTS '%s'@'127.0.0.1' IDENTIFIED BY %s;\n",
			role, literal(password), role, literal(password), role, literal(password),
		)
	}

	return fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD %s;\n", identifier(role), literal(password))
}

func grantSQL(database, role string) string {
	if mariadb() {
		return fmt.Sprintf(
			"GRANT ALL ON `%s`.* TO '%s'@'%%'; GRANT ALL ON `%s`.* TO '%s'@'localhost'; GRANT ALL ON `%s`.* TO '%s'@'127.0.0.1'; FLUSH PRIVILEGES;\n",
			database, role, database, role, database, role,
		)
	}

	return fmt.Sprintf(
		"GRANT CONNECT ON DATABASE %s TO %s; GRANT ALL ON SCHEMA public TO %s;\n",
		identifier(database), identifier(role), identifier(role),
	)
}

func revokeSQL(database, role string) string {
	if mariadb() {
		return fmt.Sprintf(
			"REVOKE ALL ON `%s`.* FROM '%s'@'%%'; REVOKE ALL ON `%s`.* FROM '%s'@'localhost'; REVOKE ALL ON `%s`.* FROM '%s'@'127.0.0.1'; FLUSH PRIVILEGES;\n",
			database, role, database, role, database, role,
		)
	}

	return fmt.Sprintf("REVOKE ALL ON DATABASE %s FROM %s;\n", identifier(database), identifier(role))
}

func appendUnique(values []string, value string) []string {
	if contains(values, value) {
		return values
	}

	return append(values, value)
}

func grant(grants map[string][]string, database, role string) map[string][]string {
	if grants == nil {
		grants = map[string][]string{}
	}
	grants[database] = appendUnique(grants[database], role)

	return grants
}

// granted reports whether any database is still reachable through this user.
func granted(grants map[string][]string, role string) bool {
	for _, roles := range grants {
		if contains(roles, role) {
			return true
		}
	}

	return false
}

func grantsFor(grants map[string][]string, role string) []string {
	databases := []string{}
	for database, roles := range grants {
		if contains(roles, role) {
			databases = append(databases, database)
		}
	}
	sort.Strings(databases)

	return databases
}

func identifier(value string) string { return `"` + value + `"` }

// literal quotes a value for the engine in use.
//
// Doubling the quote is the whole of it for PostgreSQL, where
// standard_conforming_strings leaves a backslash meaning a backslash. MySQL and
// MariaDB read one as an escape unless NO_BACKSLASH_ESCAPES is set, so there
// `\'` is a quote and the doubling meant to neutralise it closes the literal a
// character early — with everything after it read as statement rather than
// password.
func literal(value string) string {
	if mariadb() {
		value = strings.ReplaceAll(value, `\`, `\\`)
	}

	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func remove(values []string, target string) []string {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}
