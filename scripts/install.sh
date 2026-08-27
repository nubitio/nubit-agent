#!/bin/sh
set -eu

dry_run=false
profile=web

usage() {
  printf '%s\n' 'Usage: install.sh [--dry-run] [--profile web]'
}

run() {
  if "$dry_run"; then
    printf '+ %s\n' "$*"
  else
    "$@"
  fi
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) dry_run=true ;;
    --profile) shift; profile=${1:-} ;;
    --help) usage; exit 0 ;;
    *) usage; exit 64 ;;
  esac
  shift
done

[ "$profile" = web ] || { printf '%s\n' 'Only the web profile is supported.' >&2; exit 64; }
[ "$(id -u)" = 0 ] || { printf '%s\n' 'Run as root.' >&2; exit 1; }
[ -r /etc/os-release ] || { printf '%s\n' 'Cannot identify the operating system.' >&2; exit 1; }
. /etc/os-release
[ "$ID" = debian ] && [ "$VERSION_ID" = 12 ] || { printf '%s\n' 'Nubit Agent currently supports Debian 12 only.' >&2; exit 1; }

run apt-get update
run apt-get install -y ca-certificates curl caddy lsb-release postgresql openssh-server
run curl -fsSLo /tmp/debsuryorg-archive-keyring.deb https://packages.sury.org/debsuryorg-archive-keyring.deb
run dpkg -i /tmp/debsuryorg-archive-keyring.deb
run sh -c 'printf "%s\n" "deb [signed-by=/usr/share/keyrings/debsuryorg-archive-keyring.gpg] https://packages.sury.org/php/ bookworm main" > /etc/apt/sources.list.d/php.list'
run apt-get update
run apt-get install -y php8.3-fpm php8.4-fpm php8.5-fpm
run install -d -m 0750 /var/lib/nubit-agent
run install -d -m 0755 /etc/caddy/sites-enabled
if ! grep -Fqx 'import /etc/caddy/sites-enabled/*' /etc/caddy/Caddyfile; then
  run sh -c 'printf "\n%s\n" "import /etc/caddy/sites-enabled/*" >> /etc/caddy/Caddyfile'
fi
run caddy validate --adapter caddyfile --config /etc/caddy/Caddyfile
printf '%s\n' "Nubit Agent web profile installed. Enrollment is the next required step."
