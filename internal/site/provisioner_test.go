package site

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls  [][]string
	failAt string
}

func (r *fakeRunner) Run(name string, args ...string) error {
	r.calls = append(r.calls, append([]string{name}, args...))
	if name == r.failAt {
		return errors.New("injected failure")
	}
	if name == "install" {
		return os.MkdirAll(args[len(args)-1], 0o750)
	}
	return nil
}

func TestProvisionerCreatesIsolatedUserAndDocumentRoot(t *testing.T) {
	runner := &fakeRunner{}
	base := t.TempDir()
	result, err := (Provisioner{Runner: runner, Store: NewMemoryStateStore(), Layout: Layout{
		SitesDir: filepath.Join(base, "sites"), CaddyConfigDir: filepath.Join(base, "caddy"),
		PHPConfigDir: filepath.Join(base, "php"), StagingDir: filepath.Join(base, "staging"),
	}}).Create("example.com", "site-example", "8.4", Resources{})
	if err != nil {
		t.Fatal(err)
	}
	if result.SiteID != "example.com" || result.DocumentRoot != filepath.Join(base, "sites", "example.com", "public") {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(runner.calls) != 9 {
		t.Fatalf("got %d commands: %#v", len(runner.calls), runner.calls)
	}
	// The web server has to be able to read what it serves. A document root
	// group-owned by the tenant answers every static request with a 403, and
	// nothing below this layer would notice.
	if !containsCall(runner.calls, []string{"install", "-d", "-o", "site-example", "-g", WebServerUser, "-m", "0750", result.DocumentRoot}) {
		t.Fatalf("the document root is not readable by the web server: %#v", runner.calls)
	}
	if _, err := os.Stat(result.DocumentRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(result.DocumentRoot, "index.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "caddy", "example.com.caddy")); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionerRejectsUnsafeDomain(t *testing.T) {
	_, err := (Provisioner{Runner: &fakeRunner{}, Store: NewMemoryStateStore()}).Create("../escape", "site-example", "8.4", Resources{})
	if err == nil {
		t.Fatal("expected invalid domain")
	}
}

func TestProvisionerRollsBackUserAndSiteDirectoryAfterValidationFailure(t *testing.T) {
	runner := &fakeRunner{failAt: "php-fpm8.4"}
	base := t.TempDir()
	layout := Layout{
		SitesDir: filepath.Join(base, "sites"), CaddyConfigDir: filepath.Join(base, "caddy"),
		PHPConfigDir: filepath.Join(base, "php"), StagingDir: filepath.Join(base, "staging"),
	}
	_, err := (Provisioner{Runner: runner, Store: NewMemoryStateStore(), Layout: layout}).Create("example.com", "site-example", "8.4", Resources{})
	if err == nil {
		t.Fatal("expected validation failure")
	}
	if _, statErr := os.Stat(filepath.Join(layout.SitesDir, "example.com")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("site directory was not removed: %v", statErr)
	}
	last := runner.calls[len(runner.calls)-1]
	if last[0] != "userdel" || last[2] != "site-example" {
		t.Fatalf("user was not rolled back: %#v", runner.calls)
	}
}

func TestProvisionerRemovesActivatedConfigsAfterReloadFailure(t *testing.T) {
	runner := &fakeRunner{failAt: "systemctl"}
	base := t.TempDir()
	layout := Layout{
		SitesDir: filepath.Join(base, "sites"), CaddyConfigDir: filepath.Join(base, "caddy"),
		PHPConfigDir: filepath.Join(base, "php"), StagingDir: filepath.Join(base, "staging"),
	}
	_, err := (Provisioner{Runner: runner, Store: NewMemoryStateStore(), Layout: layout}).Create("example.com", "site-example", "8.4", Resources{})
	if err == nil {
		t.Fatal("expected reload failure")
	}
	for _, path := range []string{filepath.Join(layout.CaddyConfigDir, "example.com.caddy"), filepath.Join(layout.PHPConfigDir, "site-example.conf")} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("active config was not removed: %s", path)
		}
	}
}

func TestProvisionerChangesPHPVersionAndPersistsState(t *testing.T) {
	runner := &fakeRunner{}
	store := NewMemoryStateStore()
	base := t.TempDir()
	layout := Layout{
		SitesDir: filepath.Join(base, "sites"), CaddyConfigDir: filepath.Join(base, "caddy"),
		PHPConfigRoot: filepath.Join(base, "php"), StagingDir: filepath.Join(base, "staging"),
	}
	provisioner := Provisioner{Runner: runner, Store: store, Layout: layout}
	if _, err := provisioner.Create("example.com", "site-example", "8.4", Resources{}); err != nil {
		t.Fatal(err)
	}
	changed, err := provisioner.SetPHPVersion("example.com", "8.5")
	if err != nil {
		t.Fatal(err)
	}
	if changed.PreviousVersion != "8.4" || changed.PHPVersion != "8.5" {
		t.Fatalf("unexpected change result: %#v", changed)
	}
	state, _ := store.Get("example.com")
	if state.PHPVersion != "8.5" {
		t.Fatalf("expected persisted PHP 8.5, got %q", state.PHPVersion)
	}
	oldPath := filepath.Join(layout.PHPConfigRoot, "8.4", "fpm", "pool.d", "site-example.conf")
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old pool still exists: %v", err)
	}
}

func TestProvisionerRollsBackPHPVersionWhenReloadFails(t *testing.T) {
	runner := &fakeRunner{}
	store := NewMemoryStateStore()
	base := t.TempDir()
	layout := Layout{
		SitesDir: filepath.Join(base, "sites"), CaddyConfigDir: filepath.Join(base, "caddy"),
		PHPConfigRoot: filepath.Join(base, "php"), StagingDir: filepath.Join(base, "staging"),
	}
	provisioner := Provisioner{Runner: runner, Store: store, Layout: layout}
	if _, err := provisioner.Create("example.com", "site-example", "8.4", Resources{}); err != nil {
		t.Fatal(err)
	}
	runner.failAt = "systemctl"
	if _, err := provisioner.SetPHPVersion("example.com", "8.5"); err == nil {
		t.Fatal("expected reload failure")
	}
	state, _ := store.Get("example.com")
	if state.PHPVersion != "8.4" {
		t.Fatalf("state was not rolled back: %#v", state)
	}
	oldPath := filepath.Join(layout.PHPConfigRoot, "8.4", "fpm", "pool.d", "site-example.conf")
	newPath := filepath.Join(layout.PHPConfigRoot, "8.5", "fpm", "pool.d", "site-example.conf")
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("old pool was not restored: %v", err)
	}
	if _, err := os.Stat(newPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new pool was not removed: %v", err)
	}
}

func TestRuntimeInventoryReportsInstallationAndSiteUsage(t *testing.T) {
	base := t.TempDir()
	store := NewMemoryStateStore()
	if err := store.Save(State{SiteID: "legacy.example.com", PHPVersion: "8.3"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "php-fpm8.3"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	provisioner := Provisioner{Store: store, Layout: Layout{PHPBinaryDir: base}}
	inventory, err := provisioner.RuntimeInventory()
	if err != nil {
		t.Fatal(err)
	}
	if inventory[0].Version != "8.3" || !inventory[0].Installed || inventory[0].SiteCount != 1 {
		t.Fatalf("unexpected inventory: %#v", inventory)
	}
}

func TestRemoveRuntimeRefusesVersionUsedBySite(t *testing.T) {
	store := NewMemoryStateStore()
	if err := store.Save(State{SiteID: "legacy.example.com", PHPVersion: "8.3"}); err != nil {
		t.Fatal(err)
	}
	_, err := (Provisioner{Runner: &fakeRunner{}, Store: store}).RemoveRuntime("8.3", true)
	if err == nil {
		t.Fatal("expected runtime removal to be rejected")
	}
}

func TestRemoveRuntimeUninstallsUnusedDeprecatedVersion(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "php-fpm8.3"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	result, err := (Provisioner{Runner: runner, Store: NewMemoryStateStore(), Layout: Layout{PHPBinaryDir: base}}).RemoveRuntime("8.3", true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Removed {
		t.Fatal("expected runtime to be removed")
	}
	last := runner.calls[len(runner.calls)-1]
	if last[0] != "apt-get" || last[3] != "php8.3-fpm" {
		t.Fatalf("unexpected removal command: %#v", last)
	}
}

func TestRemoveRuntimeRefusesSupportedVersion(t *testing.T) {
	_, err := (Provisioner{Runner: &fakeRunner{}, Store: NewMemoryStateStore()}).RemoveRuntime("8.4", true)
	if err == nil {
		t.Fatal("expected supported runtime removal to be rejected")
	}
}

func TestReconcileDetectsModifiedPHPConfiguration(t *testing.T) {
	base := t.TempDir()
	layout := Layout{
		SitesDir: filepath.Join(base, "sites"), CaddyConfigDir: filepath.Join(base, "caddy"),
		PHPConfigRoot: filepath.Join(base, "php"), PHPBinaryDir: filepath.Join(base, "bin"), StagingDir: filepath.Join(base, "staging"),
	}
	if err := os.MkdirAll(layout.PHPBinaryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layout.PHPBinaryDir, "php-fpm8.4"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStateStore()
	provisioner := Provisioner{Runner: &fakeRunner{}, Store: store, Layout: layout}
	if _, err := provisioner.Create("example.com", "site-example", "8.4", Resources{}); err != nil {
		t.Fatal(err)
	}
	phpPath := filepath.Join(layout.PHPConfigRoot, "8.4", "fpm", "pool.d", "site-example.conf")
	if err := os.WriteFile(phpPath, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	drifts, err := provisioner.Reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if len(drifts) != 1 || drifts[0].Resource != "phpConfig" {
		t.Fatalf("unexpected drift: %#v", drifts)
	}
}

func TestSuspendAndResumeMoveCaddyConfiguration(t *testing.T) {
	base := t.TempDir()
	layout := Layout{
		SitesDir: filepath.Join(base, "sites"), CaddyConfigDir: filepath.Join(base, "enabled"),
		CaddyDisabledDir: filepath.Join(base, "disabled"), PHPConfigRoot: filepath.Join(base, "php"),
		StagingDir: filepath.Join(base, "staging"),
	}
	store := NewMemoryStateStore()
	provisioner := Provisioner{Runner: &fakeRunner{}, Store: store, Layout: layout}
	if _, err := provisioner.Create("example.com", "site-example", "8.4", Resources{}); err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.Suspend("example.com"); err != nil {
		t.Fatal(err)
	}
	disabled := filepath.Join(layout.CaddyDisabledDir, "example.com.caddy")
	if _, err := os.Stat(disabled); err != nil {
		t.Fatalf("disabled config missing: %v", err)
	}
	state, _ := store.Get("example.com")
	if state.Status != "suspended" {
		t.Fatalf("unexpected site status: %q", state.Status)
	}
	if _, err := provisioner.Resume("example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(layout.CaddyConfigDir, "example.com.caddy")); err != nil {
		t.Fatalf("active config missing: %v", err)
	}
}

func TestAddAndRemoveDomainUpdatesCaddyAndState(t *testing.T) {
	base := t.TempDir()
	layout := Layout{SitesDir: filepath.Join(base, "sites"), CaddyConfigDir: filepath.Join(base, "caddy"), PHPConfigRoot: filepath.Join(base, "php"), StagingDir: filepath.Join(base, "staging")}
	store := NewMemoryStateStore()
	provisioner := Provisioner{Runner: &fakeRunner{}, Store: store, Layout: layout}
	if _, err := provisioner.Create("example.com", "site-example", "8.4", Resources{}); err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.AddDomain("example.com", "www.example.com"); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(layout.CaddyConfigDir, "example.com.caddy"))
	if err != nil || !strings.Contains(string(contents), "example.com, www.example.com") {
		t.Fatalf("alias missing from Caddy config: %s %v", contents, err)
	}
	if _, err := provisioner.RemoveDomain("example.com", "www.example.com"); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Get("example.com")
	if len(state.Domains) != 1 || state.Domains[0] != "example.com" {
		t.Fatalf("unexpected domains: %#v", state.Domains)
	}
}

func TestDeleteRequiresSuspensionAndArchivesSite(t *testing.T) {
	base := t.TempDir()
	layout := Layout{
		SitesDir: filepath.Join(base, "sites"), CaddyConfigDir: filepath.Join(base, "enabled"),
		CaddyDisabledDir: filepath.Join(base, "disabled"), PHPConfigRoot: filepath.Join(base, "php"),
		StagingDir: filepath.Join(base, "staging"),
	}
	store := NewMemoryStateStore()
	provisioner := Provisioner{Runner: &fakeRunner{}, Store: store, Layout: layout}
	if _, err := provisioner.Create("example.com", "site-example", "8.4", Resources{}); err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.Delete("example.com", true); err == nil {
		t.Fatal("expected active site deletion to be rejected")
	}
	if _, err := provisioner.Suspend("example.com"); err != nil {
		t.Fatal(err)
	}
	deleted, err := provisioner.Delete("example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Status != "deleted" {
		t.Fatalf("unexpected result: %#v", deleted)
	}
	if _, found := store.Get("example.com"); found {
		t.Fatal("site state was not deleted")
	}
	if _, err := os.Stat(filepath.Join(deleted.RecoveryDir, "site", "public")); err != nil {
		t.Fatalf("archived site missing: %v", err)
	}
}

func containsCall(calls [][]string, want []string) bool {
	for _, call := range calls {
		if len(call) != len(want) {
			continue
		}
		match := true
		for i := range call {
			if call[i] != want[i] {
				match = false

				break
			}
		}
		if match {
			return true
		}
	}

	return false
}

func TestSetResourcesRewritesThePoolAndRemembersIt(t *testing.T) {
	runner := &fakeRunner{}
	base := t.TempDir()
	layout := Layout{
		SitesDir: filepath.Join(base, "sites"), CaddyConfigDir: filepath.Join(base, "caddy"),
		PHPConfigRoot: filepath.Join(base, "php"), StagingDir: filepath.Join(base, "staging"),
	}
	store := NewMemoryStateStore()
	provisioner := Provisioner{Runner: runner, Store: store, Layout: layout}
	if _, err := provisioner.Create("example.com", "site-example", "8.4", Resources{}); err != nil {
		t.Fatal(err)
	}

	applied, err := provisioner.SetResources("example.com", Resources{Workers: 20, MemoryLimitMB: 512})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Previous != DefaultResources() {
		t.Fatalf("the previous tier was not reported: %#v", applied.Previous)
	}

	pool, err := os.ReadFile(filepath.Join(base, "php", "8.4", "fpm", "pool.d", "site-example.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pool), "pm.max_children = 20") {
		t.Fatalf("the new limits were not written:\n%s", pool)
	}

	// Remembered on the site, so a later version change or drift check rebuilds
	// the pool with what the site was sold rather than the default tier.
	state, _ := store.Get("example.com")
	if state.Resources.Workers != 20 || state.Resources.MemoryLimitMB != 512 {
		t.Fatalf("the limits were not persisted: %#v", state.Resources)
	}
	if _, err := provisioner.SetPHPVersion("example.com", "8.5"); err != nil {
		t.Fatal(err)
	}
	migrated, err := os.ReadFile(filepath.Join(base, "php", "8.5", "fpm", "pool.d", "site-example.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(migrated), "pm.max_children = 20") {
		t.Fatalf("a version change reset the site to the default tier:\n%s", migrated)
	}
}

func TestSetResourcesRefusesLimitsThatCouldSinkTheHost(t *testing.T) {
	base := t.TempDir()
	provisioner := Provisioner{Runner: &fakeRunner{}, Store: NewMemoryStateStore(), Layout: Layout{
		SitesDir: filepath.Join(base, "sites"), CaddyConfigDir: filepath.Join(base, "caddy"),
		PHPConfigRoot: filepath.Join(base, "php"), StagingDir: filepath.Join(base, "staging"),
	}}
	if _, err := provisioner.Create("example.com", "site-example", "8.4", Resources{}); err != nil {
		t.Fatal(err)
	}
	if _, err := provisioner.SetResources("example.com", Resources{Workers: 5000, MemoryLimitMB: 128}); err == nil {
		t.Fatal("a worker count far outside the bounds was accepted")
	}
}
