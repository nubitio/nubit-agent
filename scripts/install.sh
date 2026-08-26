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
run apt-get install -y ca-certificates curl caddy php-fpm postgresql openssh-server
run install -d -m 0750 /var/lib/nubit-agent
printf '%s\n' "Nubit Agent web profile installed. Enrollment is the next required step."
