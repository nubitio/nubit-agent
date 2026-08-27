#!/bin/sh
set -eu
real="/usr/sbin/$(basename "$0")"
if [ "${1:-}" = "--test" ] && [ "${2:-}" = "--fpm-config" ]; then
	tmp=$(mktemp)
	printf '[global]\nerror_log = /dev/null\n' >"$tmp"
	cat "$3" >>"$tmp"
	"$real" --test --fpm-config "$tmp"
	status=$?
	rm -f "$tmp"
	exit "$status"
fi
exec "$real" "$@"
