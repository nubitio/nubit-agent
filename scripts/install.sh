#!/bin/sh
# Install or upgrade Nubit Agent.
#
#   curl -fsSL https://raw.githubusercontent.com/nubitio/nubit-agent/main/scripts/install.sh | sh
#
# Installs the released binary, the systemd unit, and the state directories.
# Pass --profile web to also install the Debian 12 or Ubuntu 26.04 web profile
# packages (Caddy, PHP-FPM, a database server, OpenSSH). Re-running upgrades in
# place.
set -eu

dry_run=false
profile=
# MariaDB is what shared PHP hosting means in practice, and what the sites this
# node will run expect to find.
database_engine=${NUBIT_DATABASE_ENGINE:-mariadb}
agent_token_from_cli=false
mail_relay=${NUBIT_MAIL_RELAY:-}
version=latest
repository=${NUBIT_AGENT_REPOSITORY:-nubitio/nubit-agent}
control_url=${NUBIT_CONTROL_URL:-}
agent_token=${NUBIT_AGENT_TOKEN:-}
enrollment_token=${NUBIT_AGENT_ENROLLMENT_TOKEN:-}
bin_dir=/usr/local/bin
config_dir=/etc/nubit-agent
env_file=$config_dir/agent.env
state_dir=/var/lib/nubit-agent

usage() {
  cat <<'USAGE'
Usage: install.sh [options]

  --version <tag>        Install a specific release (default: latest)
  --profile web          Also install the Debian 12 or Ubuntu 26.04 web profile packages
  --profile web,mail     Also install Stalwart for shared-hosting mailboxes
  --database <engine>    Database server the web profile installs and the agent
                         provisions on: mariadb (default) or postgres
  --mail-relay <host>    Smart host outbound mail is relayed through
  --control-url <url>    Nubit Control base URL to write into the environment file
  --agent-token <t>      Agent token to write as NUBIT_AGENT_TOKEN for Control polling.
                         Prefer NUBIT_AGENT_TOKEN=... in the environment;
                         CLI secrets can be exposed through argv or shell history.
  --enrollment-token <t> Experimental future mTLS enrollment token; not usable with current Control
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

update_env_assignment() {
  file=$1
  name=$2
  value=$3

  tmp=$(mktemp "${file}.tmp.XXXXXX") || fail "Could not create a temporary file for $file."
  chmod 0600 "$tmp"
  awk -v name="$name" -v value="$value" '
    index($0, name "=") == 1 { print name "=" value; found = 1; next }
    { print }
    END { if (!found) print name "=" value }
  ' "$file" > "$tmp" || {
    rm -f "$tmp"
    fail "Could not update $name in $file."
  }
  mv "$tmp" "$file"
  chmod 0600 "$file"
}

deactivate_env_assignment() {
  file=$1
  name=$2
  note=$3

  tmp=$(mktemp "${file}.tmp.XXXXXX") || fail "Could not create a temporary file for $file."
  chmod 0600 "$tmp"
  status=0
  awk -v name="$name" -v note="$note" '
    index($0, name "=") == 1 {
      if (!noted) print "# " name " removed by installer: " note
      found = 1
      noted = 1
      next
    }
    { print }
    END { if (found) exit 0; exit 1 }
  ' "$file" > "$tmp" || status=$?
  if [ "$status" -gt 1 ]; then
    rm -f "$tmp"
    fail "Could not deactivate $name in $file."
  fi
  if [ "$status" -eq 1 ]; then
    rm -f "$tmp"
    return 1
  fi
  mv "$tmp" "$file"
  chmod 0600 "$file"
  return 0
}

has_active_env_assignment() {
  file=$1
  name=$2

  grep -q "^${name}=" "$file" 2>/dev/null
}

install_sury_php_repository() {
  suite=$1

  run apt-get install -y ca-certificates curl gnupg
  run curl -fsSLo /tmp/debsuryorg-archive-keyring.deb https://packages.sury.org/debsuryorg-archive-keyring.deb
  run dpkg -i /tmp/debsuryorg-archive-keyring.deb
  if "$dry_run"; then
    printf '+ verify packages.sury.org keyring fingerprints 15058500A0235D97F5D10063B188E2B695BD4743 and 45BEA3E529112086C622F8A4B214EAC28059B8AC\n'
  else
    keyring_fingerprints=$(gpg --show-keys --with-colons --fingerprint /usr/share/keyrings/debsuryorg-archive-keyring.gpg \
      | awk -F: '$1 == "fpr" { print $10 }')
    [ "$keyring_fingerprints" = '15058500A0235D97F5D10063B188E2B695BD4743
45BEA3E529112086C622F8A4B214EAC28059B8AC' ] \
      || fail 'Unexpected packages.sury.org archive key fingerprint set.'
  fi
  run sh -c 'printf "%s\n" "deb [signed-by=/usr/share/keyrings/debsuryorg-archive-keyring.gpg] https://packages.sury.org/php/ '"$suite"' main" > /etc/apt/sources.list.d/php.list'
  run sh -c 'cat > /etc/apt/preferences.d/nubit-sury-php <<"PREF"
Package: php* libapache2-mod-php*
Pin: origin packages.sury.org
Pin-Priority: 500

Package: *
Pin: origin packages.sury.org
Pin-Priority: 100
PREF'
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) dry_run=true ;;
    --profile) shift; profile=${1:-} ;;
    --database) shift; database_engine=${1:-} ;;
    --mail-relay) shift; mail_relay=${1:-} ;;
    --version) shift; version=${1:-} ;;
    --control-url) shift; control_url=${1:-} ;;
    --agent-token) shift; agent_token=${1:-}; agent_token_from_cli=true ;;
    --enrollment-token) shift; enrollment_token=${1:-} ;;
    --help) usage; exit 0 ;;
    *) usage >&2; exit 64 ;;
  esac
  shift
done

if "$agent_token_from_cli"; then
  printf 'warning: --agent-token can expose the token through process argv or shell history; prefer NUBIT_AGENT_TOKEN in the environment. The token will not be printed.\n' >&2
fi

case "$database_engine" in
  mariadb | postgres) ;;
  *) fail 'Supported database engines are "mariadb" and "postgres".' ;;
esac

[ "$(id -u)" = 0 ] || fail 'Run as root.'
command -v curl >/dev/null 2>&1 || fail 'curl is required.'
command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required.'

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "Unsupported architecture: $(uname -m). Nubit Agent ships linux/amd64 and linux/arm64." ;;
esac
[ "$(uname -s)" = Linux ] || fail 'Nubit Agent runs on Linux only.'

host_os=
host_version=
if [ -r /etc/os-release ]; then
  # shellcheck source=/dev/null # provided by the host, not by this repository
  . /etc/os-release
  host_os=${ID:-}
  host_version=${VERSION_ID:-}
fi

case "${host_os}:${host_version}:${arch}" in
  debian:12:amd64 | debian:12:)
    ;;
  debian:12:*)
    fail 'Debian 12 support is validated for linux/amd64 only; this Debian architecture is not supported.'
    ;;
  ubuntu:26.04:amd64 | ubuntu:26.04:)
    ;;
  ubuntu:26.04:*)
    fail 'Ubuntu 26.04 support is validated for linux/amd64 only; this Ubuntu architecture is not supported.'
    ;;
esac

asset="nubit-agent_linux_${arch}"

if [ ! -f "$env_file" ] && [ -n "$agent_token" ] && [ -n "$enrollment_token" ]; then
  fail 'Use only NUBIT_AGENT_TOKEN/--agent-token for MVP installs; NUBIT_AGENT_ENROLLMENT_TOKEN/--enrollment-token is experimental future mTLS work and must not be configured together.'
fi

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

# The agent reads the engine from its environment and speaks a different
# dialect for each, so an installation that put MariaDB on the host and left
# this saying postgres would fail every database command on a node that looks
# correctly installed.
if [ -f "$env_file" ]; then
  if "$dry_run"; then
    printf '+ set NUBIT_DATABASE_ENGINE=%s in %s\n' "$database_engine" "$env_file"
  else
    update_env_assignment "$env_file" NUBIT_DATABASE_ENGINE "$database_engine"
  fi
fi

# ---------------------------------------------------------------------------
# Optional web profile. Debian 12 and Ubuntu 26.04 use packages.sury.org,
# which exposes versioned PHP-FPM pools under /etc/php/<version>/fpm/pool.d,
# the contract the agent uses. The agent may ship arm64 binaries, but the MVP
# Debian/Ubuntu web profile is validated for amd64 only.
# ---------------------------------------------------------------------------

if [ -n "$profile" ]; then
  if [ "$database_engine" = mariadb ]; then
    database_package=mariadb-server
  else
    database_package=postgresql
  fi
  case "$profile" in
    web | web,mail) ;;
    *) fail 'Supported profiles are "web" and "web,mail".' ;;
  esac
  [ -r /etc/os-release ] || fail 'Cannot identify the operating system.'
  # shellcheck source=/dev/null # provided by the host, not by this repository
  . /etc/os-release
  case "${ID:-}:${VERSION_ID:-}" in
    debian:12)
      [ "$arch" = amd64 ] || fail 'Debian 12 web profile is validated for linux/amd64 only.'
      run apt-get update
      run apt-get install -y acl ca-certificates curl caddy lsb-release "$database_package" openssh-server
      install_sury_php_repository bookworm
      ;;
    ubuntu:26.04)
      [ "$arch" = amd64 ] || fail 'Ubuntu 26.04 web profile is validated for linux/amd64 only.'
      run apt-get update
      run apt-get install -y acl ca-certificates curl caddy lsb-release "$database_package" openssh-server
      install_sury_php_repository resolute
      ;;
    *)
      fail 'The web profile supports Debian 12 and Ubuntu 26.04 only.'
      ;;
  esac

  run apt-get update
  run apt-get install -y php8.3-fpm php8.4-fpm php8.5-fpm
  run install -d -m 0755 /etc/caddy/sites-enabled
  if ! grep -Fqx 'import /etc/caddy/sites-enabled/*' /etc/caddy/Caddyfile 2>/dev/null; then
    run sh -c 'printf "\n%s\n" "import /etc/caddy/sites-enabled/*" >> /etc/caddy/Caddyfile'
  fi
  run caddy validate --adapter caddyfile --config /etc/caddy/Caddyfile
fi

# ---------------------------------------------------------------------------
# Optional mail profile. Stalwart is one binary that speaks SMTP, IMAP and JMAP
# and carries its own spam filter, which is why it stands in for
# Postfix+Dovecot+Rspamd here and on the central server both — one
# implementation, one administration API, one thing to learn when it breaks.
#
# It costs roughly 115 MiB more at rest than that trio, which is noise on the
# 4-8 GB nodes this runs on.
# ---------------------------------------------------------------------------

case "$profile" in
  *mail*)
    [ -n "$mail_relay" ] || printf 'warning: no --mail-relay given; this node will send mail from its own IP.\n' >&2

    run install -d -m 0750 /etc/stalwart
    run install -d -m 0750 /var/lib/stalwart

    # Only the store location goes in the file. Everything else — domains,
    # mailboxes, routing — lives in the store and is administered over JMAP by
    # the agent, so there is no second copy of the configuration to drift.
    if [ ! -f /etc/stalwart/config.json ]; then
      if "$dry_run"; then
        printf '+ write /etc/stalwart/config.json\n'
      else
        printf '{\n  "@type": "RocksDb",\n  "path": "/var/lib/stalwart/data"\n}\n' > /etc/stalwart/config.json
        chmod 0640 /etc/stalwart/config.json
      fi
    fi

    # Stalwart publishes GNU triples, not Go's arch names. A plain
    # `[ … ] && assign` would abort the script under `set -e` on the branch
    # whose test is false.
    case "$arch" in
      amd64) stalwart_arch=x86_64 ;;
      arm64) stalwart_arch=aarch64 ;;
      *) fail "No Stalwart build for $arch." ;;
    esac
    # Downloaded over TLS but not signature-checked: Stalwart publishes
    # sigstore bundles rather than a checksum file, and verifying those needs
    # cosign on the host. The agent's own binary is checksum-verified above;
    # this one is not, and that gap is deliberate rather than overlooked.
    run curl -fsSL -o /tmp/stalwart.tar.gz \
      "https://github.com/stalwartlabs/stalwart/releases/latest/download/stalwart-${stalwart_arch}-unknown-linux-gnu.tar.gz"
    run tar -xzf /tmp/stalwart.tar.gz -C /usr/local/bin stalwart
    run chmod 0755 /usr/local/bin/stalwart

    # Outbound goes through the relay when one is named. A shared node that
    # sends directly puts the reputation of every site on it behind one
    # compromised WordPress, which is the failure this exists to avoid.
    if [ -n "$mail_relay" ]; then
      printf 'Outbound mail will be relayed through %s.\n' "$mail_relay"
      printf 'Configure the smart host after enrollment: the agent writes it through the mail API.\n'
    fi
    ;;
esac

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

if [ ! -f "$env_file" ]; then
  if "$dry_run"; then
    printf '+ write %s\n' "$env_file"
  else
    umask 077
    {
      printf '# Nubit Agent environment. Restart the service after editing:\n'
      printf '#   systemctl restart nubit-agent\n'
      printf 'NUBIT_CONTROL_URL=%s\n' "$control_url"
      if [ -n "$agent_token" ]; then
        printf 'NUBIT_AGENT_TOKEN=%s\n' "$agent_token"
      fi
      if [ -n "$enrollment_token" ]; then
        printf '# Experimental future mTLS enrollment; not usable with current Nubit Control.\n'
        printf 'NUBIT_AGENT_ENROLLMENT_TOKEN=%s\n' "$enrollment_token"
      fi
      printf '# Mail. Set the API secret to let this node administer mailboxes;\n'
      printf '# leaving it empty makes the agent refuse mail commands outright.\n'
      printf '#NUBIT_MAIL_API_USER=nubit-agent\n'
      printf '#NUBIT_MAIL_API_SECRET=\n'
      printf '#NUBIT_MAIL_BASE_URL=https://127.0.0.1\n'
      printf '# Database server this node provisions site databases on.\n'
      printf 'NUBIT_DATABASE_ENGINE=%s\n' "$database_engine"
      printf '# Set to off to pin this server to the installed version.\n'
      printf '#NUBIT_AGENT_UPDATE=off\n'
    } > "$env_file"
    chmod 0600 "$env_file"
  fi
elif [ -n "$agent_token" ]; then
  if "$dry_run"; then
    printf '+ update NUBIT_AGENT_TOKEN in %s (secret not shown)\n' "$env_file"
    if has_active_env_assignment "$env_file" NUBIT_AGENT_ENROLLMENT_TOKEN; then
      printf '+ deactivate NUBIT_AGENT_ENROLLMENT_TOKEN in %s (secret not shown)\n' "$env_file"
      printf 'warning: existing NUBIT_AGENT_ENROLLMENT_TOKEN in %s will be deactivated because the agent prioritizes enrollment tokens, and current Nubit Control only supports NUBIT_AGENT_TOKEN. The enrollment token will not be printed.\n' "$env_file" >&2
    fi
  else
    update_env_assignment "$env_file" NUBIT_AGENT_TOKEN "$agent_token"
    printf 'Updated NUBIT_AGENT_TOKEN in %s (secret not shown).\n' "$env_file"
    if deactivate_env_assignment "$env_file" NUBIT_AGENT_ENROLLMENT_TOKEN 'current Nubit Control only supports NUBIT_AGENT_TOKEN; enrollment mTLS is future work'; then
      printf 'warning: deactivated existing NUBIT_AGENT_ENROLLMENT_TOKEN in %s because the agent prioritizes enrollment tokens, and current Nubit Control only supports NUBIT_AGENT_TOKEN. The enrollment token was not printed.\n' "$env_file" >&2
    fi
  fi
  if [ -n "$control_url" ] || [ -n "$enrollment_token" ]; then
    printf 'warning: existing %s preserved; only NUBIT_AGENT_TOKEN was updated. Edit the file by hand to change other Control settings.\n' "$env_file" >&2
  fi
elif has_active_env_assignment "$env_file" NUBIT_AGENT_TOKEN; then
  if "$dry_run"; then
    if has_active_env_assignment "$env_file" NUBIT_AGENT_ENROLLMENT_TOKEN; then
      printf '+ deactivate NUBIT_AGENT_ENROLLMENT_TOKEN in %s (secret not shown)\n' "$env_file"
      printf 'warning: existing NUBIT_AGENT_ENROLLMENT_TOKEN in %s will be deactivated because NUBIT_AGENT_TOKEN is already active, and current Nubit Control only supports NUBIT_AGENT_TOKEN. The enrollment token will not be printed.\n' "$env_file" >&2
    fi
  else
    if deactivate_env_assignment "$env_file" NUBIT_AGENT_ENROLLMENT_TOKEN 'current Nubit Control only supports NUBIT_AGENT_TOKEN; enrollment mTLS is future work'; then
      printf 'warning: deactivated existing NUBIT_AGENT_ENROLLMENT_TOKEN in %s because NUBIT_AGENT_TOKEN is already active, and current Nubit Control only supports NUBIT_AGENT_TOKEN. The enrollment token was not printed.\n' "$env_file" >&2
    fi
  fi
  if [ -n "$control_url" ] || [ -n "$enrollment_token" ]; then
    printf 'Keeping the existing %s; edit it by hand to change Control settings.\n' "$env_file"
  fi
elif [ -n "$control_url" ] || [ -n "$enrollment_token" ]; then
  printf 'Keeping the existing %s; edit it by hand to change Control settings.\n' "$env_file"
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
  printf 'Next: set NUBIT_CONTROL_URL and NUBIT_AGENT_TOKEN in %s, then restart the service.\n' "$env_file"
elif [ -z "$agent_token" ]; then
  printf 'Next: set NUBIT_AGENT_TOKEN in %s, then restart the service.\n' "$env_file"
fi
