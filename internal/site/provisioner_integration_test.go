//go:build integration

package site

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateSiteOnDebian12(t *testing.T) {
	if os.Getenv("NUBIT_DEBIAN_INTEGRATION") != "1" {
		t.Skip("set NUBIT_DEBIAN_INTEGRATION=1 inside the disposable Debian 12 container")
	}

	osRelease, err := os.ReadFile("/etc/os-release")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(osRelease), "VERSION_ID=\"12\"") {
		t.Fatalf("integration test requires Debian 12, got:\n%s", osRelease)
	}
	if os.Geteuid() != 0 {
		t.Fatal("integration test must run as root")
	}

	base := t.TempDir()
	layout := Layout{
		SitesDir:        filepath.Join(base, "sites"),
		CaddyConfigDir:  filepath.Join(base, "caddy"),
		CaddyMainConfig: filepath.Join(base, "Caddyfile"),
		PHPConfigRoot:   filepath.Join(base, "php"),
		StagingDir:      filepath.Join(base, "staging"),
	}
	if err := os.WriteFile(layout.CaddyMainConfig, []byte("import "+layout.CaddyConfigDir+"/*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStateStore()
	for _, version := range []string{"8.3", "8.4", "8.5"} {
		domain := "php" + strings.ReplaceAll(version, ".", "") + ".integration.example.com"
		user := "nubit-int-" + strings.ReplaceAll(version, ".", "")
		result, err := (Provisioner{Runner: OSRunner{}, Layout: layout, Store: store}).Create(domain, user, version)
		if err != nil {
			t.Fatalf("PHP %s: %v", version, err)
		}

		for _, path := range []string{
			result.DocumentRoot,
			filepath.Join(layout.CaddyConfigDir, domain+".caddy"),
			filepath.Join(layout.PHPConfigRoot, version, "fpm", "pool.d", user+".conf"),
		} {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected provisioned resource %s: %v", path, err)
			}
		}
		if !strings.HasPrefix(result.CaddyConfigHash, "sha256:") || !strings.HasPrefix(result.PHPConfigHash, "sha256:") {
			encoded, _ := json.Marshal(result)
			t.Fatalf("expected configuration hashes, got %s", encoded)
		}
	}
	provisioner := Provisioner{Runner: OSRunner{}, Layout: layout, Store: store}
	if _, err := provisioner.SetPHPVersion("php83.integration.example.com", "8.5"); err != nil {
		t.Fatalf("migrate PHP 8.3 to 8.5: %v", err)
	}
	removed, err := provisioner.RemoveRuntime("8.3", true)
	if err != nil {
		t.Fatalf("remove unused PHP 8.3 runtime: %v", err)
	}
	if !removed.Removed {
		t.Fatal("expected PHP 8.3 runtime to be removed")
	}
	inventory, err := provisioner.RuntimeInventory()
	if err != nil {
		t.Fatal(err)
	}
	if inventory[0].Version != "8.3" || inventory[0].Installed || inventory[0].SiteCount != 0 {
		t.Fatalf("unexpected PHP 8.3 inventory after removal: %#v", inventory[0])
	}
	if _, err := provisioner.AddDomain("php84.integration.example.com", "alias.integration.example.com"); err != nil {
		t.Fatalf("add domain: %v", err)
	}
	if _, err := provisioner.Suspend("php84.integration.example.com"); err != nil {
		t.Fatalf("suspend site: %v", err)
	}
	if _, err := provisioner.Resume("php84.integration.example.com"); err != nil {
		t.Fatalf("resume site: %v", err)
	}
	if _, err := provisioner.Suspend("php84.integration.example.com"); err != nil {
		t.Fatalf("suspend site for deletion: %v", err)
	}
	deleted, err := provisioner.Delete("php84.integration.example.com", true)
	if err != nil {
		t.Fatalf("delete site: %v", err)
	}
	if _, err := os.Stat(filepath.Join(deleted.RecoveryDir, "site", "public")); err != nil {
		t.Fatalf("recovery archive missing: %v", err)
	}
}
