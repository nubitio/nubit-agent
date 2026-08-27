package access

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nubitio/nubit-agent/internal/site"
)

type fakeRunner struct{ calls [][]string }

func (runner *fakeRunner) Run(name string, args ...string) error {
	runner.calls = append(runner.calls, append([]string{name}, args...))
	return nil
}

func TestCreateUpdateAndRevokeSFTPAccess(t *testing.T) {
	base := t.TempDir()
	sites := site.NewMemoryStateStore()
	if err := sites.Save(site.State{SiteID: "example.com", SystemUser: "site-example", DocumentRoot: "/srv/example/public"}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	manager := Manager{Runner: runner, Sites: sites, ConfigDir: filepath.Join(base, "sshd"), KeysDir: filepath.Join(base, "keys")}
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKeyMaterial example"
	if _, err := manager.Create("example.com", key); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(manager.KeysDir, "site-example")
	if info, err := os.Stat(keyPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected authorized key: %v %v", info, err)
	}
	if _, err := manager.UpdateKey("example.com", key+"-updated"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Revoke("example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("authorized key was not removed: %v", err)
	}
}
