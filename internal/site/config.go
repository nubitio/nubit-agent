package site

import "fmt"

func CaddyConfig(domain, root, socket string) string {
	return fmt.Sprintf("%s {\n\troot * %s\n\tphp_fastcgi unix/%s\n\tfile_server\n}\n", domain, root, socket)
}

func PHPFPMConfig(user, root, socket string) string {
	return fmt.Sprintf("[%s]\nuser = %s\ngroup = %s\nlisten = /run/php/%s\nlisten.owner = %s\nlisten.group = %s\nchdir = %s\n", user, user, user, socket, user, user, root)
}
