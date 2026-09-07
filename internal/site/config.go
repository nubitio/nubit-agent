package site

import (
	"fmt"
	"os"
	"strings"
)

// caddySiteHeader turns the ", "-joined domain list into the site address line
// (adding the "<host>.localhost" aliases when NUBIT_SITE_LOCALHOST_ALIAS is on)
// and the per-site access-log path. Shared by every vhost template.
func caddySiteHeader(domain string) (address, logFile string) {
	parts := strings.Split(domain, ", ")
	hosts := parts
	if v := os.Getenv("NUBIT_SITE_LOCALHOST_ALIAS"); v == "1" || v == "true" {
		hosts = nil
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			hosts = append(hosts, part, "http://"+part+".localhost")
		}
	}
	logFile = "/var/log/nubit/" + strings.ReplaceAll(parts[0], "/", "_") + ".caddy.log"
	return strings.Join(hosts, ", "), logFile
}

func CaddyConfig(domain, root, socket string) string {
	address, logFile := caddySiteHeader(domain)

	return fmt.Sprintf("%s {\n\troot * %s\n\tphp_fastcgi unix/%s\n\tfile_server\n\tlog {\n\t\toutput file %s\n\t}\n}\n", address, root, socket, logFile)
}

// CaddyConfigWordPress is the vhost for a managed-WordPress site: the same
// PHP-FastCGI serving as CaddyConfig plus WordPress-shaped hardening (block
// xmlrpc.php and the config/VCS/dotfiles, deny PHP anywhere under uploads) and
// a cache header on static assets. Caddy's php_fastcgi already does the
// permalink try_files for index.php, so that is not repeated here.
func CaddyConfigWordPress(domain, root, socket string) string {
	address, logFile := caddySiteHeader(domain)

	return fmt.Sprintf(`%s {
	root * %s

	@forbidden path /xmlrpc.php /wp-config.php /.git/* /.svn/* /.env *.sql *.bak *.log
	respond @forbidden 403

	# PHP dropped into the uploads tree (including the dated year/month
	# subdirectories WordPress writes to) is never executed.
	@uploads_php path_regexp uploadsphp ^/wp-content/uploads/.*\.(php|phtml|phar|php[0-9])$
	respond @uploads_php 403

	@static path *.css *.js *.mjs *.png *.jpg *.jpeg *.gif *.svg *.ico *.webp *.woff *.woff2 *.ttf *.eot
	header @static Cache-Control "public, max-age=604800"

	php_fastcgi unix/%s
	file_server
	log {
		output file %s
	}
}
`, address, root, socket, logFile)
}

// WebServerUser is the account Caddy runs as under its Debian package.
//
// It is the only account besides the tenant's own that is given a way into a
// site: it owns the pool socket by group so it can connect, and it is the group
// on the document root so it can read what it serves. Everything else on the
// host, every other tenant included, is left with no access at all.
const WebServerUser = "caddy"

// poolTemplate is the FPM pool for one site.
//
// The socket is owned by the tenant and group-owned by the web server at 0660,
// which is what lets Caddy connect while leaving every other tenant out. Its
// mode is written rather than left to the default, because that default is what
// the two of them can reach each other through.
//
// Memory is bounded here and nowhere else. All the pools of one PHP version are
// children of a single systemd unit, so a cgroup cannot be aimed at an
// individual site; pm.max_children multiplied by memory_limit is the real
// ceiling, and the plan the site was sold on is what sets both.
const poolTemplate = `[%[1]s]
user = %[1]s
group = %[1]s
listen = /run/php/%[2]s
listen.owner = %[1]s
listen.group = %[3]s
listen.mode = 0660

pm = ondemand
pm.max_children = %[5]d
; The worker dies with its opcache, so a short timeout makes a low-traffic site
; recompile on nearly every visit.
pm.process_idle_timeout = 60s
; Without this a hung request holds its worker forever, and five of them take
; the site down for good.
request_terminate_timeout = 60s

chdir = %[4]s
php_admin_value[memory_limit] = %[6]dM
; The tenant's unix user stops it writing outside the site. This stops it
; reading outside, which unix permissions alone would still allow.
php_admin_value[open_basedir] = %[4]s/:/usr/share/php/
; Kept inside the site rather than in a directory shared with every other
; tenant on the host.
php_admin_value[upload_tmp_dir] = %[4]s/tmp
php_admin_value[session.save_path] = %[4]s/tmp
php_admin_value[error_log] = /var/log/nubit/%[1]s.php.log
php_admin_flag[log_errors] = on
`

func PHPFPMConfig(user, root, socket string, resources Resources) string {
	resources = resources.WithDefaults()

	return fmt.Sprintf(
		poolTemplate,
		user, socket, WebServerUser, root,
		resources.Workers, resources.MemoryLimitMB,
	)
}

func DefaultIndexHTML(domain string) string {
	return `<!DOCTYPE html>
<html lang="es">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + domain + `</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; padding: 0 1.5rem; color: #111; }
  h1 { font-size: 1.5rem; }
  p { color: #444; line-height: 1.5; }
</style>
</head>
<body>
<h1>Tu sitio ya está listo</h1>
<p>Este es <strong>` + domain + `</strong>. Sube tus archivos desde tu cuenta (administrador de archivos o SFTP) y reemplaza esta página.</p>
</body>
</html>
`
}
