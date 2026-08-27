#!/bin/sh
set -eu

mkdir -p /etc/caddy/sites-enabled /run/php /run/sshd /var/lib/nubit-agent /srv/nubit/sites /var/log/nubit /etc/ssh/sshd_config.d /var/run/mysqld
chown mysql:mysql /var/run/mysqld 2>/dev/null || true

if [ ! -f /etc/caddy/sites-enabled/00-ready.caddy ]; then
	printf 'http://127.0.0.1 {\n\trespond "nubit-agent ready" 200\n}\n' >/etc/caddy/sites-enabled/00-ready.caddy
fi

write_www_pool() {
	version=$1
	conf="/etc/php/${version}/fpm/pool.d/www.conf"
	mkdir -p "/etc/php/${version}/fpm/pool.d"
	if [ ! -f "$conf" ]; then
		cat >"$conf" <<EOF
[www]
user = www-data
group = www-data
listen = /run/php/php${version}-fpm.sock
listen.owner = www-data
listen.group = www-data
pm = ondemand
pm.max_children = 2
EOF
	fi
}

# Recreate Unix users that persisted PHP-FPM pools still reference after a
# container recreate (pool.d is a volume; /etc/passwd is not).
recover_php_pools() {
	for conf in /etc/php/*/fpm/pool.d/*.conf; do
		[ -f "$conf" ] || continue
		pool=$(basename "$conf")
		[ "$pool" = "www.conf" ] && continue
		user=$(awk -F= '/^[[:space:]]*user[[:space:]]*=/{gsub(/[[:space:]]/,"",$2); print $2; exit}' "$conf")
		chdir=$(awk -F= '/^[[:space:]]*chdir[[:space:]]*=/{gsub(/[[:space:]]/,"",$2); print $2; exit}' "$conf")
		if [ -z "$user" ]; then
			continue
		fi
		if ! getent passwd "$user" >/dev/null 2>&1; then
			useradd --system --no-create-home --home-dir "${chdir:-/nonexistent}" --shell /usr/sbin/nologin "$user" || true
			passwd -d "$user" || true
		fi
		if [ -n "$chdir" ] && [ -d "$chdir" ]; then
			chown -R "$user:$user" "$chdir" || true
		fi
		if [ -f "/var/lib/nubit-agent/authorized_keys/$user" ]; then
			chown "$user:$user" "/var/lib/nubit-agent/authorized_keys/$user" || true
		fi
	done
}

start_php_fpm() {
	version=$1
	binary="/usr/sbin/php-fpm${version}"
	pidfile="/run/php/php${version}-fpm.pid"
	[ -x "$binary" ] || return 0
	if [ -f "$pidfile" ] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
		return 0
	fi
	"$binary" --daemonize --pid "$pidfile" || true
}

write_www_pool 8.4
write_www_pool 8.5
recover_php_pools

if [ ! -f /etc/ssh/ssh_host_ed25519_key ]; then
	ssh-keygen -A
fi
sed -i \
	-e 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' \
	-e 's/^#\?KbdInteractiveAuthentication.*/KbdInteractiveAuthentication no/' \
	-e 's/^#\?UsePAM.*/UsePAM no/' \
	-e 's/^#\?PermitRootLogin.*/PermitRootLogin no/' \
	/etc/ssh/sshd_config
/usr/sbin/sshd

if command -v mariadbd >/dev/null 2>&1; then
	if [ ! -d /var/lib/mysql/mysql ]; then
		mysql_install_db --user=mysql --datadir=/var/lib/mysql >/dev/null 2>&1 \
			|| mariadb-install-db --user=mysql --datadir=/var/lib/mysql >/dev/null 2>&1 || true
	fi
	mariadbd --user=mysql --bind-address=0.0.0.0 --datadir=/var/lib/mysql >/var/log/nubit/mariadb.log 2>&1 &
	for _ in 1 2 3 4 5 6 7 8 9 10; do
		mysqladmin ping >/dev/null 2>&1 && break
		sleep 1
	done
fi
if command -v cron >/dev/null 2>&1; then
	cron
fi

start_php_fpm 8.4
start_php_fpm 8.5
caddy start --config /etc/caddy/Caddyfile --adapter caddyfile
exec /usr/local/bin/nubit-agent
