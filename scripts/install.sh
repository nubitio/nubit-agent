#!/bin/sh
# Install or upgrade Nubit Agent.
#
#   curl -fsSL https://raw.githubusercontent.com/nubitio/nubit-agent/main/scripts/install.sh | sh
#
# Installs the released binary, the systemd unit, and the state directories.
# Pass --profile web to also install the Debian 12 web profile packages (Caddy,
# PHP-FPM, PostgreSQL, OpenSSH). Re-running upgrades in place.
set -eu

dry_run=false
profile=
version=latest
repository=${NUBIT_AGENT_REPOSITORY:-nubitio/nubit-agent}
control_url=${NUBIT_CONTROL_URL:-}
enrollment_token=${NUBIT_AGENT_ENROLLMENT_TOKEN:-}
bin_dir=/usr/local/bin
config_dir=/etc/nubit-agent
state_dir=/var/lib/nubit-agent

usage() {
  cat <<'USAGE'
Usage: install.sh [options]

  --version <tag>        Install a specific release (default: latest)
  --profile web          Also install the Debian 12 web profile packages
  --control-url <url>    Nubit Control base URL to write into the environment file
  --enrollment-token <t> One-time enrollment token to write into the environment file
  --dry-run              Print the actions instead of performing them
  --help                 Show this message
USAGE
}

run() {
  if "$dry_run"; then
    printf '+ %s\n' "$*"
  else
    "$@"
  fi
}

fail() {
  printf 'error: %s\n' "$1" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) dry_run=true ;;
    --profile) shift; profile=${1:-} ;;
    --version) shift; version=${1:-} ;;
    --control-url) shift; control_url=${1:-} ;;
    --enrollment-token) shift; enrollment_token=${1:-} ;;
    --help) usage; exit 0 ;;
    *) usage >&2; exit 64 ;;
  esac
  shift
done

[ "$(id -u)" = 0 ] || fail 'Run as root.'
command -v curl >/dev/null 2>&1 || fail 'curl is required.'
command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required.'

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "Unsupported architecture: $(uname -m). Nubit Agent ships linux/amd64 and linux/arm64." ;;
esac
[ "$(uname -s)" = Linux ] || fail 'Nubit Agent runs on Linux only.'

asset="nubit-agent_linux_${arch}"

if [ "$version" = latest ]; then
  tag=$(curl -fsSL "https://api.github.com/repos/${repository}/releases/latest" \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
    | head -n 1)
  [ -n "$tag" ] || fail "Could not resolve the latest release of ${repository}."
else
  tag=$version
fi

base="https://github.com/${repository}/releases/download/${tag}"
printf 'Installing Nubit Agent %s (linux/%s)\n' "$tag" "$arch"

# ---------------------------------------------------------------------------
# Download and verify before touching anything already installed.
# ---------------------------------------------------------------------------

if "$dry_run"; then
  printf '+ download %s/%s and verify against SHA256SUMS\n' "$base" "$asset"
else
  work=$(mktemp -d)
  # shellcheck disable=SC2064 # expand work now: it must be removed on any exit
  trap "rm -rf '$work'" EXIT INT TERM

  curl -fsSL -o "$work/$asset" "$base/$asset" || fail "Could not download $base/$asset"
  curl -fsSL -o "$work/SHA256SUMS" "$base/SHA256SUMS" || fail "Could not download $base/SHA256SUMS"

  # Verify only the asset for this platform; the file lists every published one.
  ( cd "$work" && grep " \*\{0,1\}${asset}\$" SHA256SUMS | sha256sum -c - ) \
    >/dev/null 2>&1 || fail "Checksum verification failed for $asset."
  printf 'Checksum verified.\n'
fi

# ---------------------------------------------------------------------------
# Optional Debian 12 web profile.
# ---------------------------------------------------------------------------

if [ -n "$profile" ]; then
  [ "$profile" = web ] || fail 'Only the web profile is supported.'
  [ -r /etc/os-release ] || fail 'Cannot identify the operating system.'
  # shellcheck source=/dev/null # provided by the host, not by this repository
  . /etc/os-release
  if [ "${ID:-}" != debian ] || [ "${VERSION_ID:-}" != 12 ]; then
    fail 'The web profile currently supports Debian 12 only.'
  fi

  run apt-get update
  run apt-get install -y ca-certificates curl caddy lsb-release postgresql openssh-server
  run curl -fsSLo /tmp/debsuryorg-archive-keyring.deb https://packages.sury.org/debsuryorg-archive-keyring.deb
  run dpkg -i /tmp/debsuryorg-archive-keyring.deb
  run sh -c 'printf "%s\n" "deb [signed-by=/usr/share/keyrings/debsuryorg-archive-keyring.gpg] https://packages.sury.org/php/ bookworm main" > /etc/apt/sources.list.d/php.list'
  run apt-get update
  run apt-get install -y php8.3-fpm php8.4-fpm php8.5-fpm
  run install -d -m 0755 /etc/caddy/sites-enabled
  if ! grep -Fqx 'import /etc/caddy/sites-enabled/*' /etc/caddy/Caddyfile 2>/dev/null; then
    run sh -c 'printf "\n%s\n" "import /etc/caddy/sites-enabled/*" >> /etc/caddy/Caddyfile'
  fi
  run caddy validate --adapter caddyfile --config /etc/caddy/Caddyfile
fi

# ---------------------------------------------------------------------------
# Install the binary, directories and unit.
# ---------------------------------------------------------------------------

run install -d -m 0750 "$state_dir"
run install -d -m 0750 "$config_dir"

if "$dry_run"; then
  printf '+ install -m 0755 <verified binary> %s/nubit-agent\n' "$bin_dir"
else
  # install(1) replaces via a new inode, so a running agent keeps its open image
  # until systemd restarts it below.
  install -m 0755 "$work/$asset" "$bin_dir/nubit-agent"
fi

env_file="$config_dir/agent.env"
if [ ! -f "$env_file" ]; then
  if "$dry_run"; then
    printf '+ write %s\n' "$env_file"
  else
    umask 077
    {
      printf '# Nubit Agent environment. Restart the service after editing:\n'
      printf '#   systemctl restart nubit-agent\n'
      printf 'NUBIT_CONTROL_URL=%s\n' "$control_url"
      if [ -n "$enrollment_token" ]; then
        printf 'NUBIT_AGENT_ENROLLMENT_TOKEN=%s\n' "$enrollment_token"
      fi
      printf '# Set to off to pin this server to the installed version.\n'
      printf '#NUBIT_AGENT_UPDATE=off\n'
    } > "$env_file"
    chmod 0600 "$env_file"
  fi
elif [ -n "$control_url" ] || [ -n "$enrollment_token" ]; then
  printf 'Keeping the existing %s; edit it by hand to change enrollment.\n' "$env_file"
fi

unit=/etc/systemd/system/nubit-agent.service
if "$dry_run"; then
  printf '+ write %s and enable the service\n' "$unit"
else
  cat > "$unit" <<'UNIT'
[Unit]
Description=Nubit Agent
Documentation=https://github.com/nubitio/nubit-agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/nubit-agent
EnvironmentFile=-/etc/nubit-agent/agent.env
Restart=always
RestartSec=5
User=root
KillMode=mixed
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
UNIT
  chmod 0644 "$unit"
  systemctl daemon-reload
  systemctl enable --now nubit-agent
  systemctl restart nubit-agent
fi

printf 'Nubit Agent %s installed.\n' "$tag"
if [ -z "$control_url" ]; then
  printf 'Next: set NUBIT_CONTROL_URL and NUBIT_AGENT_ENROLLMENT_TOKEN in %s, then restart the service.\n' "$env_file"
fi
