package site

import (
	"fmt"
	"os"
	"strings"
)

func CaddyConfig(domain, root, socket string) string {
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
	logFile := "/var/log/nubit/" + strings.ReplaceAll(parts[0], "/", "_") + ".caddy.log"

	return fmt.Sprintf("%s {\n\troot * %s\n\tphp_fastcgi unix/%s\n\tfile_server\n\tlog {\n\t\toutput file %s\n\t}\n}\n", strings.Join(hosts, ", "), root, socket, logFile)
}

func PHPFPMConfig(user, root, socket string) string {
	return fmt.Sprintf("[%s]\nuser = %s\ngroup = %s\nlisten = /run/php/%s\nlisten.owner = %s\nlisten.group = %s\npm = ondemand\npm.max_children = 5\npm.process_idle_timeout = 10s\nchdir = %s\nphp_admin_value[error_log] = /var/log/nubit/%s.php.log\nphp_admin_flag[log_errors] = on\n", user, user, user, socket, user, user, root, user)
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
