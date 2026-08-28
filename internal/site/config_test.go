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

func TestSiteConfigsUseIsolatedPaths(t *testing.T) {
	t.Setenv("NUBIT_SITE_LOCALHOST_ALIAS", "")
	caddy := CaddyConfig("example.com", "/srv/nubit/sites/example.com/public", "site-example.sock")
	fpm := PHPFPMConfig("site-example", "/srv/nubit/sites/example.com", "site-example.sock")
	if !strings.Contains(caddy, "php_fastcgi unix/site-example.sock") || !strings.Contains(fpm, "user = site-example") {
		t.Fatal("site config is not isolated")
	}
	if !strings.Contains(fpm, "pm = ondemand") || !strings.Contains(fpm, "pm.max_children = 5") {
		t.Fatal("site config does not define a valid process manager")
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
