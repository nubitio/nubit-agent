package database

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
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
	if err := manager.Runner.Execute(dropRoleSQL(role)); err != nil {
		return Result{}, err
	}
	state.Databases = remove(state.Databases, database)
	if err := manager.Sites.Save(state); err != nil {
		return Result{}, err
	}
	return Result{siteID, database, role, "deleted"}, nil
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

func identifier(value string) string { return `"` + value + `"` }
func literal(value string) string    { return `'` + strings.ReplaceAll(value, `'`, `''`) + `'` }
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
