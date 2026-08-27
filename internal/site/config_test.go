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
}
