#!/bin/sh
set -eu

grep -Fx 'ID=ubuntu' /etc/os-release
grep -Fx 'VERSION_ID="26.04"' /etc/os-release

if NUBIT_AGENT_REPOSITORY=integration/nubit-agent \
  NUBIT_AGENT_TOKEN=conflict-agent-token \
  NUBIT_AGENT_ENROLLMENT_TOKEN=conflict-enrollment-token \
  sh scripts/install.sh --version v0.0.0-integration \
    > /tmp/nubit-agent-installer-conflict.log 2>&1; then
  printf 'installer accepted both agent and enrollment tokens on initial creation\n' >&2
  exit 1
fi
grep -Fq 'Use only NUBIT_AGENT_TOKEN/--agent-token for MVP installs' /tmp/nubit-agent-installer-conflict.log
if grep -Eq 'conflict-(agent|enrollment)-token' /tmp/nubit-agent-installer-conflict.log; then
  printf 'conflict validation leaked token fixture in installer output\n' >&2
  exit 1
fi
test ! -f /etc/nubit-agent/agent.env

NUBIT_AGENT_REPOSITORY=integration/nubit-agent \
  sh scripts/install.sh --version v0.0.0-integration --profile web \
    --control-url https://control.example.test --agent-token integration-agent-token

test -x /usr/local/bin/nubit-agent
/usr/local/bin/nubit-agent --version | grep -qx dev
for binary in caddy psql sshd php-fpm8.3 php-fpm8.4 php-fpm8.5; do
  command -v "$binary" >/dev/null
done

test -d /etc/nubit-agent
grep -Fxq 'NUBIT_CONTROL_URL=https://control.example.test' /etc/nubit-agent/agent.env
grep -Fxq 'NUBIT_AGENT_TOKEN=integration-agent-token' /etc/nubit-agent/agent.env
printf 'CUSTOM_SETTING=keep-me\n' >> /etc/nubit-agent/agent.env
printf 'NUBIT_AGENT_ENROLLMENT_TOKEN=old-enrollment-token\n' >> /etc/nubit-agent/agent.env
NUBIT_AGENT_REPOSITORY=integration/nubit-agent NUBIT_AGENT_TOKEN=rotated-agent-token \
  sh scripts/install.sh --version v0.0.0-integration > /tmp/nubit-agent-installer-rerun.log 2>&1
grep -Fxq 'NUBIT_CONTROL_URL=https://control.example.test' /etc/nubit-agent/agent.env
grep -Fxq 'NUBIT_AGENT_TOKEN=rotated-agent-token' /etc/nubit-agent/agent.env
test "$(grep -Fc 'NUBIT_AGENT_TOKEN=' /etc/nubit-agent/agent.env)" = 1
grep -Fxq 'CUSTOM_SETTING=keep-me' /etc/nubit-agent/agent.env
grep -Fxq '# NUBIT_AGENT_ENROLLMENT_TOKEN removed by installer: current Nubit Control only supports NUBIT_AGENT_TOKEN; enrollment mTLS is future work' /etc/nubit-agent/agent.env
if grep -Fq 'old-enrollment-token' /etc/nubit-agent/agent.env /tmp/nubit-agent-installer-rerun.log; then
  printf 'rerun leaked or preserved old enrollment token\n' >&2
  exit 1
fi
grep -Fxq 'Updated NUBIT_AGENT_TOKEN in /etc/nubit-agent/agent.env (secret not shown).' /tmp/nubit-agent-installer-rerun.log
grep -Fq 'warning: deactivated existing NUBIT_AGENT_ENROLLMENT_TOKEN in /etc/nubit-agent/agent.env because the agent prioritizes enrollment tokens, and current Nubit Control only supports NUBIT_AGENT_TOKEN.' /tmp/nubit-agent-installer-rerun.log
if grep -Fq 'rotated-agent-token' /tmp/nubit-agent-installer-rerun.log; then
  printf 'rerun leaked agent token in installer output\n' >&2
  exit 1
fi
NUBIT_AGENT_REPOSITORY=integration/nubit-agent \
  sh scripts/install.sh --version v0.0.0-integration \
    --agent-token cli-rotated-agent-token > /tmp/nubit-agent-installer-cli-rerun.log 2>&1
grep -Fxq 'NUBIT_AGENT_TOKEN=cli-rotated-agent-token' /etc/nubit-agent/agent.env
test "$(grep -Fc 'NUBIT_AGENT_TOKEN=' /etc/nubit-agent/agent.env)" = 1
grep -Fq 'warning: --agent-token can expose the token through process argv or shell history; prefer NUBIT_AGENT_TOKEN in the environment.' /tmp/nubit-agent-installer-cli-rerun.log
grep -Fxq 'Updated NUBIT_AGENT_TOKEN in /etc/nubit-agent/agent.env (secret not shown).' /tmp/nubit-agent-installer-cli-rerun.log
if grep -Fq 'cli-rotated-agent-token' /tmp/nubit-agent-installer-cli-rerun.log; then
  printf 'CLI rerun leaked agent token in installer output\n' >&2
  exit 1
fi
if grep -q '^NUBIT_AGENT_ENROLLMENT_TOKEN=' /etc/nubit-agent/agent.env; then
  printf 'unexpected enrollment token in agent environment\n' >&2
  exit 1
fi
printf 'NUBIT_AGENT_ENROLLMENT_TOKEN=stale-enrollment-token\n' >> /etc/nubit-agent/agent.env
NUBIT_AGENT_REPOSITORY=integration/nubit-agent \
  sh scripts/install.sh --version v0.0.0-integration > /tmp/nubit-agent-installer-no-token-rerun.log 2>&1
grep -Fxq 'NUBIT_AGENT_TOKEN=cli-rotated-agent-token' /etc/nubit-agent/agent.env
if grep -Fq 'stale-enrollment-token' /etc/nubit-agent/agent.env /tmp/nubit-agent-installer-no-token-rerun.log; then
  printf 'no-token rerun leaked or preserved enrollment token\n' >&2
  exit 1
fi
grep -Fq 'warning: deactivated existing NUBIT_AGENT_ENROLLMENT_TOKEN in /etc/nubit-agent/agent.env because NUBIT_AGENT_TOKEN is already active' /tmp/nubit-agent-installer-no-token-rerun.log
test -d /var/lib/nubit-agent
test "$(stat -c '%a' /etc/nubit-agent /var/lib/nubit-agent)" = '750
750'
test -f /etc/systemd/system/nubit-agent.service
grep -Fx 'ExecStart=/usr/local/bin/nubit-agent' /etc/systemd/system/nubit-agent.service
test "$(gpg --show-keys --with-colons --fingerprint /usr/share/keyrings/debsuryorg-archive-keyring.gpg \
  | awk -F: '$1 == "fpr" { print $10 }')" = '15058500A0235D97F5D10063B188E2B695BD4743
45BEA3E529112086C622F8A4B214EAC28059B8AC'
grep -Fx 'Pin: origin packages.sury.org' /etc/apt/preferences.d/nubit-sury-php
grep -Fx 'daemon-reload' /tmp/nubit-agent-systemctl.log
grep -Fx 'enable --now nubit-agent' /tmp/nubit-agent-systemctl.log
grep -Fx 'restart nubit-agent' /tmp/nubit-agent-systemctl.log
