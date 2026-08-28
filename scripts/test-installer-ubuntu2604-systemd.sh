#!/bin/sh
set -eu

# Run inside a real Ubuntu 26.04 amd64 VM or systemd-nspawn container. Docker
# without systemd is intentionally rejected: this validation gates platform
# support and must prove that the installed units can be managed by systemd.

repository_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)

grep -Fx 'ID=ubuntu' /etc/os-release
grep -Fx 'VERSION_ID="26.04"' /etc/os-release
[ "$(uname -m)" = x86_64 ] || [ "$(uname -m)" = amd64 ] || {
  printf 'error: Ubuntu 26.04 validation is limited to amd64.\n' >&2
  exit 1
}

command -v systemctl >/dev/null 2>&1
[ -d /run/systemd/system ] || {
  printf 'error: systemd is not PID 1 in this environment.\n' >&2
  exit 1
}
systemctl is-system-running --wait >/dev/null 2>&1 || true

NUBIT_AGENT_REPOSITORY=${NUBIT_AGENT_REPOSITORY:-nubitio/nubit-agent} \
  sh "$repository_dir/scripts/install.sh" --profile web "${@}"

systemctl is-enabled nubit-agent
systemctl is-active --quiet nubit-agent
systemctl is-active --quiet caddy
systemctl is-active --quiet postgresql

for service in php8.3-fpm php8.4-fpm php8.5-fpm ssh; do
  systemctl is-active --quiet "$service"
done

for binary in caddy psql sshd php-fpm8.3 php-fpm8.4 php-fpm8.5; do
  command -v "$binary" >/dev/null
done

test -x /usr/local/bin/nubit-agent
test -d /etc/nubit-agent
test -d /var/lib/nubit-agent
test -f /etc/systemd/system/nubit-agent.service
grep -Fx 'ExecStart=/usr/local/bin/nubit-agent' /etc/systemd/system/nubit-agent.service
grep -Fx 'fpr:::::::::15058500A0235D97F5D10063B188E2B695BD4743:' \
  /tmp/nubit-agent-sury-key-fingerprint 2>/dev/null || \
  gpg --show-keys --with-colons --fingerprint /usr/share/keyrings/debsuryorg-archive-keyring.gpg \
    | grep -Fx 'fpr:::::::::15058500A0235D97F5D10063B188E2B695BD4743:'
grep -Fx 'Pin: origin packages.sury.org' /etc/apt/preferences.d/nubit-sury-php

printf 'Ubuntu 26.04 amd64 systemd installer validation passed.\n'
