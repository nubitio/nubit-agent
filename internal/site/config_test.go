package site

import (
	"strings"
	"testing"
)

func TestSiteConfigsUseIsolatedPaths(t *testing.T) {
	caddy := CaddyConfig("example.com", "/srv/nubit/sites/example.com/public", "site-example.sock")
	fpm := PHPFPMConfig("site-example", "/srv/nubit/sites/example.com", "site-example.sock")
	if !strings.Contains(caddy, "php_fastcgi unix/site-example.sock") || !strings.Contains(fpm, "user = site-example") { t.Fatal("site config is not isolated") }
}
