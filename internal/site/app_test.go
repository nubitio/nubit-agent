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

func TestAppInstallDoesNotReinstallWhenWordPressIsAlreadyPresent(t *testing.T) {
	p, runner := newSite(t)
	runner.isInstalled = true

	result, err := p.AppInstall("example.com", AppInstallRequest{
		App: "wordpress", AdminUser: "admin", AdminEmail: "a@example.com",
		SiteTitle: "x", SiteURL: "https://example.com", DBName: "d", DBUser: "u",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Installed {
		t.Fatalf("expected no reinstall, got %#v", result)
	}
	if !strings.HasPrefix(result.Reason, "already-present") {
		t.Fatalf("reason = %q, want an already-present reason", result.Reason)
	}
	// The only wp-cli call is the is-installed probe: no download/config/install.
	if wp := wpCalls(runner.calls); len(wp) != 1 {
		t.Fatalf("expected only the is-installed probe, got %#v", wp)
	}
}

// A WordPress site whose earlier hardening step failed (or that predates this
// command) still gets the hardened vhost on the next call, even though the
// install itself is a no-op.
func TestAppInstallHardensAnAlreadyPresentSiteThatIsNotYetMarked(t *testing.T) {
	p, _ := newSite(t)

	runner := &wpRunner{isInstalled: true}
	p.Runner = runner

	result, err := p.AppInstall("example.com", AppInstallRequest{
		App: "wordpress", AdminUser: "admin", AdminEmail: "a@example.com",
		SiteTitle: "x", SiteURL: "https://example.com", DBName: "d", DBUser: "u",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != "already-present; caddy hardening applied" {
		t.Fatalf("reason = %q", result.Reason)
	}
	state, _ := p.Store.Get("example.com")
	if state.App != "wordpress" {
		t.Fatalf("state.App = %q, want wordpress", state.App)
	}
	data, _ := os.ReadFile(filepath.Join(p.Layout.CaddyConfigDir, "example.com.caddy"))
	if !strings.Contains(string(data), "respond @forbidden 403") {
		t.Fatalf("vhost was not hardened:\n%s", data)
	}
}

// A site already marked app="wordpress" does no vhost work on a repeat call.
func TestAppInstallDoesNoVhostWorkWhenTheProfileIsAlreadyRecorded(t *testing.T) {
	p, runner := newSite(t)
	runner.isInstalled = true
	state, _ := p.Store.Get("example.com")
	state.App = "wordpress"
	if err := p.Store.Save(state); err != nil {
		t.Fatal(err)
	}

	if _, err := p.AppInstall("example.com", AppInstallRequest{
		App: "wordpress", AdminUser: "admin", AdminEmail: "a@example.com",
		SiteTitle: "x", SiteURL: "https://example.com", DBName: "d", DBUser: "u",
	}); err != nil {
		t.Fatal(err)
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
	for _, want := range []string{"/xmlrpc.php", "/wp-config.php", "respond @forbidden 403", "wp-content/uploads/", "Cache-Control"} {
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

// A Caddy reload failure after `wp core install` succeeded must not lose the
// generated password nor report the install as failed: the hardening is
// deferred (drift + a retry converge it) and the previous vhost is restored.
func TestAppInstallDefersHardeningButKeepsThePasswordWhenCaddyReloadFails(t *testing.T) {
	p, runner := newSite(t)
	runner.failCmd = "systemctl reload caddy"

	original, err := os.ReadFile(filepath.Join(p.Layout.CaddyConfigDir, "example.com.caddy"))
	if err != nil {
		t.Fatal(err)
	}

	result, err := p.AppInstall("example.com", AppInstallRequest{
		App: "wordpress", AdminUser: "admin", AdminEmail: "a@example.com",
		SiteTitle: "x", SiteURL: "https://example.com", DBName: "d", DBUser: "u",
	})
	if err != nil {
		t.Fatalf("install must not fail on a deferred vhost swap: %v", err)
	}
	if !result.Installed || result.AdminPassword == "" {
		t.Fatalf("password/installed lost on a deferred swap: %#v", result)
	}
	if !strings.Contains(result.Reason, "deferred") {
		t.Fatalf("reason = %q, want a 'deferred' note", result.Reason)
	}
	// The profile is still recorded so Reconcile sees the drift.
	if state, _ := p.Store.Get("example.com"); state.App != "wordpress" {
		t.Fatalf("state.App = %q, want wordpress", state.App)
	}

	after, err := os.ReadFile(filepath.Join(p.Layout.CaddyConfigDir, "example.com.caddy"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("vhost was not rolled back after the reload failed:\n%s", after)
	}
}

func TestAppInstallForcesWpCliSoARetryIsNotWedged(t *testing.T) {
	p, runner := newSite(t)

	if _, err := p.AppInstall("example.com", AppInstallRequest{
		App: "wordpress", AdminUser: "admin", AdminEmail: "a@example.com",
		SiteTitle: "x", SiteURL: "https://example.com", DBName: "d", DBUser: "u",
	}); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"core download", "config create"} {
		var forced bool
		for _, c := range wpCalls(runner.calls) {
			if wpSubcommand(c[1:]) == want && contains(c, "--force") {
				forced = true
			}
		}
		if !forced {
			t.Fatalf("%q did not run with --force: %#v", want, runner.calls)
		}
	}
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
