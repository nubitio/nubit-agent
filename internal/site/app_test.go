package site

import (
	"errors"
	"strings"
	"testing"
)

// wpRunner records every sudo/wp invocation and can be told to answer
// `wp core is-installed` (exit 0 = installed) or fail a specific subcommand.
type wpRunner struct {
	calls       [][]string
	isInstalled bool
	failSub     string // e.g. "core download"
}

func (r *wpRunner) Run(name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	sub := wpSubcommand(args)
	if sub == "core is-installed" {
		if r.isInstalled {
			return nil
		}
		return errors.New("not installed")
	}
	if sub == r.failSub {
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
	if err := store.Save(State{SiteID: "example.com", SystemUser: "site-example", DocumentRoot: "/srv/nubit/sites/example.com/public"}); err != nil {
		t.Fatal(err)
	}
	return Provisioner{Runner: runner, Store: store}, runner
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
	if len(runner.calls) != 4 { // is-installed, download, config create, install
		t.Fatalf("want 4 wp calls, got %d: %#v", len(runner.calls), runner.calls)
	}
	for _, call := range runner.calls {
		if call[0] != "sudo" || call[1] != "-u" || call[2] != "site-example" {
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
