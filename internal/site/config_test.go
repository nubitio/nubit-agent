package site

import (
	"strings"
	"testing"
)

func TestLocalhostAliasIsOptional(t *testing.T) {
	t.Setenv("NUBIT_SITE_LOCALHOST_ALIAS", "1")
	caddy := CaddyConfig("example.com", "/srv/nubit/sites/example.com/public", "site-example.sock")
	if !strings.Contains(caddy, "http://example.com.localhost") {
		t.Fatalf("expected localhost alias: %s", caddy)
	}
}

func TestWordPressVhostIsHardenedAndCachesStatics(t *testing.T) {
	t.Setenv("NUBIT_SITE_LOCALHOST_ALIAS", "")
	caddy := CaddyConfigWordPress("example.com", "/srv/nubit/sites/example.com/public", "site-example.sock")

	// still a PHP-FastCGI site on the pool socket
	if !strings.Contains(caddy, "php_fastcgi unix/site-example.sock") {
		t.Fatalf("wordpress vhost lost its FastCGI upstream:\n%s", caddy)
	}
	// the sensitive paths a WordPress install exposes are answered with 403
	for _, blocked := range []string{"/xmlrpc.php", "/wp-config.php", "/.git/*", "*.sql"} {
		if !strings.Contains(caddy, blocked) {
			t.Fatalf("wordpress vhost does not block %q:\n%s", blocked, caddy)
		}
	}
	if !strings.Contains(caddy, "respond @forbidden 403") {
		t.Fatalf("forbidden paths are not denied:\n%s", caddy)
	}
	// PHP under uploads is denied via a regex so the dated year/month
	// subdirectories WordPress writes to are covered, not just the top level.
	if !strings.Contains(caddy, `path_regexp uploadsphp ^/wp-content/uploads/.*\.(php`) || !strings.Contains(caddy, "respond @uploads_php 403") {
		t.Fatalf("PHP under uploads is not denied recursively:\n%s", caddy)
	}
	// static assets get a cache header, but not an immutable one — a managed
	// host has to be able to bust it after a plugin update.
	if !strings.Contains(caddy, `header @static Cache-Control "public, max-age=604800"`) {
		t.Fatalf("static assets are not cached as expected:\n%s", caddy)
	}
	if strings.Contains(caddy, "immutable") {
		t.Fatalf("static cache header must not be immutable:\n%s", caddy)
	}
}

func TestWordPressVhostKeepsTheLocalhostAlias(t *testing.T) {
	t.Setenv("NUBIT_SITE_LOCALHOST_ALIAS", "1")
	caddy := CaddyConfigWordPress("example.com", "/srv/nubit/sites/example.com/public", "site-example.sock")
	if !strings.Contains(caddy, "http://example.com.localhost") {
		t.Fatalf("expected localhost alias: %s", caddy)
	}
}

func TestSiteConfigsUseIsolatedPaths(t *testing.T) {
	t.Setenv("NUBIT_SITE_LOCALHOST_ALIAS", "")
	caddy := CaddyConfig("example.com", "/srv/nubit/sites/example.com/public", "site-example.sock")
	fpm := PHPFPMConfig("site-example", "/srv/nubit/sites/example.com", "site-example.sock", Resources{})
	if !strings.Contains(caddy, "php_fastcgi unix/site-example.sock") || !strings.Contains(fpm, "user = site-example") {
		t.Fatal("site config is not isolated")
	}
	if !strings.Contains(fpm, "pm = ondemand") || !strings.Contains(fpm, "pm.max_children = 5") {
		t.Fatal("site config does not define a valid process manager")
	}
	if !strings.Contains(fpm, "php_admin_value[memory_limit] = 128M") {
		t.Fatalf("an unset plan did not fall back to the shared tier:\n%s", fpm)
	}
	// Caddy runs as its own user and connects over this socket. Owned by the
	// tenant and group-owned by the web server at 0660 is what lets it in
	// without letting any other tenant in.
	for _, line := range []string{
		"listen.owner = site-example",
		"listen.group = " + WebServerUser,
		"listen.mode = 0660",
	} {
		if !strings.Contains(fpm, line) {
			t.Fatalf("the pool socket is unreachable by the web server, missing %q:\n%s", line, fpm)
		}
	}
	// max_children multiplied by memory_limit is the only ceiling a site has:
	// pools share one systemd unit, so a cgroup cannot be aimed at one of them.
	if !strings.Contains(fpm, "php_admin_value[memory_limit]") || !strings.Contains(fpm, "request_terminate_timeout") {
		t.Fatalf("the pool is unbounded in memory or in time:\n%s", fpm)
	}
	if !strings.Contains(fpm, "php_admin_value[open_basedir]") {
		t.Fatalf("the pool can read outside the site:\n%s", fpm)
	}
}

// The limits a plan buys have to reach the pool, and a site that only raises
// one of the two must keep the default for the other.
func TestPoolLimitsComeFromThePlan(t *testing.T) {
	fpm := PHPFPMConfig("site-example", "/srv/nubit/sites/example.com", "site-example.sock", Resources{Workers: 20, MemoryLimitMB: 512})
	if !strings.Contains(fpm, "pm.max_children = 20") || !strings.Contains(fpm, "php_admin_value[memory_limit] = 512M") {
		t.Fatalf("the plan's limits did not reach the pool:\n%s", fpm)
	}

	half := PHPFPMConfig("site-example", "/srv/nubit/sites/example.com", "site-example.sock", Resources{Workers: 12})
	if !strings.Contains(half, "pm.max_children = 12") || !strings.Contains(half, "php_admin_value[memory_limit] = 128M") {
		t.Fatalf("setting one limit dropped the other:\n%s", half)
	}
}
