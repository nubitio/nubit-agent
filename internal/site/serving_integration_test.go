//go:build integration

package site

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestASiteActuallyServes sends a real request through the socket and document
// root the provisioner wrote, with Caddy running as its own unprivileged user.
//
// Every other test stops at `caddy validate` and `php-fpm --test`, which only
// prove the configuration parses. Nothing proved that the user Caddy runs as
// could open the socket or read the files, and those are exactly the two things
// a wrong owner or mode breaks — silently, and only in production.
func TestASiteActuallyServes(t *testing.T) {
	if os.Getenv("NUBIT_DEBIAN_INTEGRATION") != "1" {
		t.Skip("set NUBIT_DEBIAN_INTEGRATION=1 inside the disposable Debian 12 container")
	}
	if os.Geteuid() != 0 {
		t.Fatal("integration test must run as root")
	}

	// A bare hostname block listens on :443. The alias the generator already
	// supports is explicitly http://, which is what puts the site on :80 here.
	t.Setenv("NUBIT_SITE_LOCALHOST_ALIAS", "1")
	// auto_https off keeps the container off ACME: there is no public DNS here,
	// and the certificate path is covered by the TLS inspector's own tests.
	mustWrite(t, "/etc/caddy/Caddyfile", "{\n\tauto_https off\n}\nimport /etc/caddy/sites-enabled/*\n")
	mustRun(t, "install", "-d", "-m", "0755", "/etc/caddy/sites-enabled")

	const domain, user, version = "example.pe", "site-alpha", "8.4"
	const host = domain + ".localhost"
	provisioner := Provisioner{Runner: OSRunner{}, Layout: DefaultLayout(version), Store: NewMemoryStateStore()}
	created, err := provisioner.Create(domain, user, version, Resources{})
	if err != nil {
		t.Fatalf("create site: %v", err)
	}

	// Written the way a tenant's own upload arrives: owned by the site user,
	// with the mode an SFTP client leaves behind.
	script := filepath.Join(created.DocumentRoot, "index.php")
	mustWrite(t, script, "<?php echo 'served-by-fpm:' . PHP_VERSION;\n")
	mustRun(t, "chown", user+":"+user, script)
	mustRun(t, "chmod", "0644", script)

	t.Run("caddy reads the document root", func(t *testing.T) {
		body, status := get(t, host, "/index.html")
		if status != http.StatusOK {
			t.Fatalf("static file returned %d, so Caddy cannot read the document root: %s", status, body)
		}
		if !strings.Contains(body, domain) {
			t.Fatalf("unexpected static body: %s", body)
		}
	})

	t.Run("php-fpm answers on the socket", func(t *testing.T) {
		body, status := get(t, host, "/index.php")
		if status != http.StatusOK {
			t.Fatalf("PHP returned %d, so Caddy cannot reach the pool socket: %s", status, body)
		}
		if !strings.HasPrefix(body, "served-by-fpm:8.4.") {
			t.Fatalf("the response did not come from PHP %s: %s", version, body)
		}
	})

	// The permissions that let Caddy in must not let another tenant in. This is
	// the constraint the fix for the two cases above could most easily break.
	t.Run("another tenant cannot read the document root", func(t *testing.T) {
		mustRun(t, "useradd", "--system", "--shell", "/usr/sbin/nologin", "site-beta")
		out, err := exec.Command(
			"setpriv", "--reuid=site-beta", "--regid=site-beta", "--clear-groups",
			"cat", script,
		).CombinedOutput()
		if err == nil {
			t.Fatalf("site-beta read another tenant's file: %s", out)
		}
	})
}

func get(t *testing.T, host, path string) (string, int) {
	t.Helper()

	var lastErr error
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = host
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err != nil {
			// Caddy was just started by the systemctl stand-in and may not be
			// listening yet. A refused connection is retried; a reply is not.
			lastErr = err
			time.Sleep(250 * time.Millisecond)

			continue
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}

		return strings.TrimSpace(string(body)), response.StatusCode
	}
	t.Fatalf("Caddy never answered on :80: %v", lastErr)

	return "", 0
}

func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRun(t *testing.T, name string, args ...string) {
	t.Helper()
	if output, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, output)
	}
}
