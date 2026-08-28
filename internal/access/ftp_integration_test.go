//go:build integration

package access

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nubitio/nubit-agent/internal/site"
)

// These logins are unix accounts reaching into a directory owned by someone
// else through a POSIX ACL. Whether that actually grants anything is decided by
// the filesystem, so it is exercised against a real one rather than asserted
// from the arguments the manager would have passed.
func TestExtraLoginsReachTheSiteWithoutOwningIt(t *testing.T) {
	if os.Getenv("NUBIT_DEBIAN_INTEGRATION") != "1" {
		t.Skip("set NUBIT_DEBIAN_INTEGRATION=1 inside the disposable container")
	}
	if os.Geteuid() != 0 {
		t.Fatal("integration test must run as root")
	}

	manager, state := realSite(t, "example.pe", "site-alpha")

	created, err := manager.CreateFTPUser("example.pe", "agencia", testKey(t), "")
	if err != nil {
		t.Fatalf("create login: %v", err)
	}
	if created.Username != "site-alpha-agencia" {
		t.Fatalf("unexpected unix account: %q", created.Username)
	}

	// A file the site writes must be reachable by the login. Without the default
	// ACL this is exactly what breaks, and only for files created later.
	fresh := filepath.Join(state.DocumentRoot, "index.php")
	writeAs(t, state.SystemUser, fresh, "<?php\n")
	if err := readAs(created.Username, fresh); err != nil {
		t.Fatalf("the login cannot read what the site wrote: %v", err)
	}
	if err := writeableBy(created.Username, filepath.Join(state.DocumentRoot, "uploaded.txt")); err != nil {
		t.Fatalf("the login cannot write into the site: %v", err)
	}

	// It must not become an owner: another site stays closed to it.
	_, other := realSite(t, "beta.pe", "site-beta")
	stranger := filepath.Join(other.DocumentRoot, "secret.txt")
	writeAs(t, "site-beta", stranger, "private\n")
	if err := readAs(created.Username, stranger); err == nil {
		t.Fatal("a login on one site read another site's files")
	}
}

func TestALoginIsScopedToItsDirectory(t *testing.T) {
	if os.Getenv("NUBIT_DEBIAN_INTEGRATION") != "1" {
		t.Skip("set NUBIT_DEBIAN_INTEGRATION=1 inside the disposable container")
	}

	manager, state := realSite(t, "scoped.pe", "site-scoped")

	created, err := manager.CreateFTPUser("scoped.pe", "disenador", testKey(t), "wp-content/themes")
	if err != nil {
		t.Fatalf("create scoped login: %v", err)
	}
	want := filepath.Join(state.DocumentRoot, "wp-content/themes")
	if created.Directory != want {
		t.Fatalf("directory is %q, want %q", created.Directory, want)
	}
	// sshd is told to drop the session straight into it, which is what makes the
	// scope real rather than advisory.
	config := read(t, filepath.Join(manager.ConfigDir, "nubit-"+created.Username+".conf"))
	if !strings.Contains(config, "ForceCommand internal-sftp -d "+want) {
		t.Fatalf("sshd would not confine the session:\n%s", config)
	}

	// And the whole configuration still has to parse, or every login on the host
	// stops working at the next reload.
	if output, err := exec.Command("sshd", "-t").CombinedOutput(); err != nil {
		t.Fatalf("sshd rejected the configuration: %v: %s", err, output)
	}
}

// A directory outside the site is refused rather than quietly resolved.
func TestALoginCannotEscapeTheSite(t *testing.T) {
	if os.Getenv("NUBIT_DEBIAN_INTEGRATION") != "1" {
		t.Skip("set NUBIT_DEBIAN_INTEGRATION=1 inside the disposable container")
	}

	manager, state := realSite(t, "escape.pe", "site-escape")

	// `../public` is missing on purpose: it resolves back to the document root,
	// so it is a strange spelling of a directory inside the site rather than a
	// way out of it. What is refused is what actually lands outside.
	for _, directory := range []string{"../../etc", "public/../../../root", "..", "../../"} {
		if _, err := manager.CreateFTPUser("escape.pe", "fuera", testKey(t), directory); err == nil {
			t.Fatalf("%q was accepted as a directory", directory)
		}
	}

	// A leading slash means the site's own root, which is what the customer's
	// FTP client shows them, not the host's.
	created, err := manager.CreateFTPUser("escape.pe", "raiz", testKey(t), "/uploads")
	if err != nil {
		t.Fatal(err)
	}
	if created.Directory != filepath.Join(state.DocumentRoot, "uploads") {
		t.Fatalf("a leading slash escaped the site: %q", created.Directory)
	}
}

func TestDeletingALoginTakesItsAccessWithIt(t *testing.T) {
	if os.Getenv("NUBIT_DEBIAN_INTEGRATION") != "1" {
		t.Skip("set NUBIT_DEBIAN_INTEGRATION=1 inside the disposable container")
	}

	manager, state := realSite(t, "gone.pe", "site-gone")
	created, err := manager.CreateFTPUser("gone.pe", "temporal", testKey(t), "")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(state.DocumentRoot, "index.php")
	writeAs(t, state.SystemUser, file, "<?php\n")

	if _, err := manager.DeleteFTPUser("gone.pe", "temporal", true); err != nil {
		t.Fatalf("delete login: %v", err)
	}

	if _, err := exec.Command("id", created.Username).CombinedOutput(); err == nil {
		t.Fatal("the unix account outlived the login")
	}
	// An ACL naming a freed uid would grant whoever is given that number next.
	acl, err := exec.Command("getfacl", "-p", state.DocumentRoot).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(acl), created.Username) {
		t.Fatalf("the ACL still names the deleted login:\n%s", acl)
	}
	if _, err := exec.Command("stat", filepath.Join(manager.ConfigDir, "nubit-"+created.Username+".conf")).Output(); err == nil {
		t.Fatal("the sshd block outlived the login")
	}
}

// --- helpers ---------------------------------------------------------------

type osRunner struct{}

func (osRunner) Run(name string, args ...string) error {
	if output, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		return &runError{command: name, output: strings.TrimSpace(string(output)), cause: err}
	}

	return nil
}

type runError struct {
	command string
	output  string
	cause   error
}

func (e *runError) Error() string { return e.command + ": " + e.output }
func (e *runError) Unwrap() error { return e.cause }

type provisioned struct {
	SystemUser   string
	DocumentRoot string
	Layout       site.Layout
}

func realSite(t *testing.T, domain, user string) (Manager, provisioned) {
	t.Helper()

	base := t.TempDir()
	// The whole tree has to be traversable, or the test measures the temporary
	// directory's permissions instead of the site's. t.TempDir puts the run
	// under a 0700 parent of its own, so that one is opened too.
	for _, dir := range []string{filepath.Dir(base), base} {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	layout := site.Layout{
		SitesDir:       filepath.Join(base, "sites"),
		CaddyConfigDir: filepath.Join(base, "caddy"),
		PHPConfigRoot:  filepath.Join(base, "php"),
		StagingDir:     filepath.Join(base, "staging"),
	}
	store := site.NewMemoryStateStore()
	created, err := (site.Provisioner{Runner: site.OSRunner{}, Layout: layout, Store: store}).
		Create(domain, user, "8.4", site.Resources{})
	if err != nil {
		t.Fatalf("provision %s: %v", domain, err)
	}
	for _, dir := range []string{layout.SitesDir, filepath.Dir(created.DocumentRoot)} {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	manager := Manager{
		Runner: osRunner{},
		Sites:  store,
		// The directory sshd is configured to include, so validating and
		// reloading exercise the real configuration rather than a copy.
		ConfigDir: "/etc/ssh/nubit.d",
		KeysDir:   filepath.Join(base, "keys"),
	}

	return manager, provisioned{SystemUser: user, DocumentRoot: created.DocumentRoot, Layout: layout}
}

func testKey(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if output, err := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, output)
	}

	return read(t, path+".pub")
}

func read(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(contents)
}

func writeAs(t *testing.T, user, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("setpriv", "--reuid="+user, "--regid="+user, "--clear-groups",
		"sh", "-c", "cat > "+path)
	command.Stdin = strings.NewReader(contents)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("write as %s: %v: %s", user, err, output)
	}
}

func readAs(user, path string) error {
	return osRunner{}.Run("setpriv", "--reuid="+user, "--regid="+user, "--clear-groups", "cat", path)
}

func writeableBy(user, path string) error {
	return osRunner{}.Run("setpriv", "--reuid="+user, "--regid="+user, "--clear-groups", "touch", path)
}
