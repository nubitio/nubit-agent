package site

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wpRunner records every sudo/wp invocation and can be told to answer
// `wp core is-installed` (exit 0 = installed) or fail a specific subcommand.
type wpRunner struct {
	calls       [][]string
	isInstalled bool
	failSub     string // wp subcommand to fail, e.g. "core download"
	failCmd     string // any command line containing this substring fails, e.g. "systemctl reload caddy"
}

func (r *wpRunner) Run(name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	line := strings.Join(append([]string{name}, args...), " ")
	if r.failCmd != "" && strings.Contains(line, r.failCmd) {
		return errors.New("injected failure")
	}
	sub := wpSubcommand(args)
	if sub == "core is-installed" {
		if r.isInstalled {
			return nil
		}
		return errors.New("not installed")
	}
	if r.failSub != "" && sub == r.failSub {
		return errors.New("injected failure")
	}
	return nil
}

// wpSubcommand pulls "core download" / "config create" out of the arg list,
// skipping the sudo prefix and the wp --path flag.
func wpSubcommand(args []string) string {
	var rest []string
	seenWP := false
	for _, a := range args {
		if a == "wp" {
			seenWP = true
			continue
		}
		if !seenWP || strings.HasPrefix(a, "--") {
			continue
		}
		rest = append(rest, a)
		if len(rest) == 2 {
			break
		}
	}
	return strings.Join(rest, " ")
}

func newSite(t *testing.T) (Provisioner, *wpRunner) {
	t.Helper()
	runner := &wpRunner{}
	store := NewMemoryStateStore()

	dir := t.TempDir()
	layout := Layout{
		SitesDir:         filepath.Join(dir, "srv"),
		CaddyConfigDir:   filepath.Join(dir, "caddy", "sites-enabled"),
		CaddyDisabledDir: filepath.Join(dir, "caddy", "sites-disabled"),
		PHPConfigRoot:    filepath.Join(dir, "php"),
		StagingDir:       filepath.Join(dir, "staging"),
	}
	if err := os.MkdirAll(layout.CaddyConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const (
		root   = "/srv/nubit/sites/example.com/public"
		socket = "/run/php/site-example.sock"
	)
	vhost := filepath.Join(layout.CaddyConfigDir, "example.com.caddy")
	if err := os.WriteFile(vhost, []byte(CaddyConfig("example.com", root, socket)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(State{
		SiteID: "example.com", Domain: "example.com", SystemUser: "site-example",
		DocumentRoot: root, PHPSocket: socket, Status: "active",
		Domains: []string{"example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	return Provisioner{Runner: runner, Store: store, Layout: layout}, runner
}

// wpCalls returns just the wp-cli (sudo) invocations, dropping the caddy /
// systemctl calls the post-install vhost step makes.
func wpCalls(calls [][]string) [][]string {
	var out [][]string
	for _, c := range calls {
		if len(c) > 0 && c[0] == "sudo" {
			out = append(out, c)
		}
	}
	return out
}

func TestAppInstallRunsWpCliAsTheSiteUserAndReturnsAPassword(t *testing.T) {
	p, runner := newSite(t)

	result, err := p.AppInstall("example.com", AppInstallRequest{
		App: "wordpress", Version: "6.6.2", Locale: "es_ES",
		AdminUser: "admin", AdminEmail: "a@example.com", SiteTitle: "Mi sitio",
		SiteURL: "https://example.com", DBName: "d", DBUser: "u", DBPassword: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Installed || result.AdminPassword == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	wp := wpCalls(runner.calls)
	if len(wp) != 4 { // is-installed, download, config create, install
		t.Fatalf("want 4 wp calls, got %d: %#v", len(wp), wp)
	}
	for _, call := range wp {
		if call[1] != "-u" || call[2] != "site-example" {
			t.Fatalf("wp not run as the site user: %#v", call)
		}
	}
}

func TestAppInstallIsANoOpWhenWordPressIsAlreadyPresent(t *testing.T) {
	p, runner := newSite(t)
	runner.isInstalled = true

	result, err := p.AppInstall("example.com", AppInstallRequest{
		App: "wordpress", AdminUser: "admin", AdminEmail: "a@example.com",
		SiteTitle: "x", SiteURL: "https://example.com", DBName: "d", DBUser: "u",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Installed || result.Reason != "already-present" {
		t.Fatalf("expected no-op, got %#v", result)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected only the is-installed probe, got %#v", runner.calls)
	}
}

func TestAppInstallRejectsANonWordpressApp(t *testing.T) {
	p, _ := newSite(t)
	if _, err := p.AppInstall("example.com", AppInstallRequest{App: "drupal"}); err == nil {
		t.Fatal("expected an error for a non-wordpress app")
	}
}

func TestAppInstallFailsWhenTheSiteIsUnknown(t *testing.T) {
	p, _ := newSite(t)
	if _, err := p.AppInstall("other.com", AppInstallRequest{App: "wordpress"}); err == nil {
		t.Fatal("expected an error for an unknown site")
	}
}

func TestAppInstallSurfacesAWpCliFailure(t *testing.T) {
	p, runner := newSite(t)
	runner.failSub = "core download"
	if _, err := p.AppInstall("example.com", AppInstallRequest{
		App: "wordpress", AdminUser: "admin", AdminEmail: "a@example.com",
		SiteTitle: "x", SiteURL: "https://example.com", DBName: "d", DBUser: "u",
	}); err == nil {
		t.Fatal("expected the wp core download failure to surface")
	}
}

func TestAppInstallHardensTheCaddyVhostAndRecordsTheProfile(t *testing.T) {
	p, runner := newSite(t)

	if _, err := p.AppInstall("example.com", AppInstallRequest{
		App: "wordpress", AdminUser: "admin", AdminEmail: "a@example.com",
		SiteTitle: "Mi sitio", SiteURL: "https://example.com",
		DBName: "d", DBUser: "u", DBPassword: "p",
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(p.Layout.CaddyConfigDir, "example.com.caddy"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/xmlrpc.php", "/wp-config.php", "/wp-content/uploads/*.php", "respond @blocked 403", "Cache-Control"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("vhost was not hardened, missing %q:\n%s", want, data)
		}
	}

	state, _ := p.Store.Get("example.com")
	if state.App != "wordpress" {
		t.Fatalf("state.App = %q, want \"wordpress\"", state.App)
	}

	var validated, reloaded bool
	for _, c := range runner.calls {
		if len(c) >= 2 && c[0] == "caddy" && c[1] == "validate" {
			validated = true
		}
		if len(c) >= 3 && c[0] == "systemctl" && c[1] == "reload" && c[2] == "caddy" {
			reloaded = true
		}
	}
	if !validated || !reloaded {
		t.Fatalf("vhost not validated+reloaded (validated=%v reloaded=%v): %#v", validated, reloaded, runner.calls)
	}
}

func TestAppInstallRollsBackTheVhostWhenCaddyReloadFails(t *testing.T) {
	p, runner := newSite(t)
	runner.failCmd = "systemctl reload caddy"

	original, err := os.ReadFile(filepath.Join(p.Layout.CaddyConfigDir, "example.com.caddy"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := p.AppInstall("example.com", AppInstallRequest{
		App: "wordpress", AdminUser: "admin", AdminEmail: "a@example.com",
		SiteTitle: "x", SiteURL: "https://example.com", DBName: "d", DBUser: "u",
	}); err == nil {
		t.Fatal("expected the caddy reload failure to surface")
	}

	after, err := os.ReadFile(filepath.Join(p.Layout.CaddyConfigDir, "example.com.caddy"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("vhost was not rolled back after the reload failed:\n%s", after)
	}
}
