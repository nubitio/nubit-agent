FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -o /nubit-agent ./cmd/nubit-agent

FROM debian:bookworm-slim
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates caddy curl gnupg lsb-release \
	&& curl -fsSLo /tmp/debsuryorg-archive-keyring.deb https://packages.sury.org/debsuryorg-archive-keyring.deb \
	&& dpkg -i /tmp/debsuryorg-archive-keyring.deb \
	&& echo "deb [signed-by=/usr/share/keyrings/debsuryorg-archive-keyring.gpg] https://packages.sury.org/php/ bookworm main" >/etc/apt/sources.list.d/php.list \
	&& apt-get update \
	&& apt-get install -y --no-install-recommends \
		php8.4-fpm php8.4-cli php8.4-mysql php8.4-xml php8.4-mbstring php8.4-curl php8.4-zip \
		php8.5-fpm php8.5-cli php8.5-mysql php8.5-xml php8.5-mbstring php8.5-curl php8.5-zip \
		openssh-server mariadb-server cron xz-utils \
	&& rm -rf /var/lib/apt/lists/* /tmp/debsuryorg-archive-keyring.deb

# Stalwart (mail) + its CLI, statically linked (musl) so they run regardless of
# the base image's glibc. The agent administers this Stalwart over JMAP when
# NUBIT_MAIL_API_SECRET is set; a web-only node never starts it.
RUN set -eux; \
	case "$(dpkg --print-architecture)" in \
		amd64) rustarch=x86_64 ;; \
		arm64) rustarch=aarch64 ;; \
		*) echo "unsupported arch for stalwart"; exit 1 ;; \
	esac; \
	curl -fsSL -o /tmp/stalwart.tar.gz \
		"https://github.com/stalwartlabs/stalwart/releases/latest/download/stalwart-${rustarch}-unknown-linux-musl.tar.gz"; \
	tar -xzf /tmp/stalwart.tar.gz -C /usr/local/bin stalwart; \
	curl -fsSL -o /tmp/stalwart-cli.tar.xz \
		"https://github.com/stalwartlabs/cli/releases/latest/download/stalwart-cli-${rustarch}-unknown-linux-musl.tar.xz"; \
	mkdir -p /tmp/swcli && tar -xJf /tmp/stalwart-cli.tar.xz -C /tmp/swcli; \
	install -m 0755 "$(find /tmp/swcli -type f -name stalwart-cli | head -n1)" /usr/local/bin/stalwart-cli; \
	chmod 0755 /usr/local/bin/stalwart; \
	rm -rf /tmp/stalwart.tar.gz /tmp/stalwart-cli.tar.xz /tmp/swcli

COPY --from=build /nubit-agent /usr/local/bin/nubit-agent
COPY docker/systemctl /usr/local/bin/systemctl
COPY docker/php-fpm-wrapper.sh /usr/local/bin/php-fpm8.4
COPY docker/php-fpm-wrapper.sh /usr/local/bin/php-fpm8.5
COPY docker/Caddyfile /etc/caddy/Caddyfile
COPY docker/sites-enabled/00-ready.caddy /etc/caddy/sites-enabled/00-ready.caddy
COPY docker/entrypoint.sh /usr/local/bin/nubit-agent-entrypoint
RUN chmod 0755 /usr/local/bin/systemctl /usr/local/bin/php-fpm8.4 /usr/local/bin/php-fpm8.5 /usr/local/bin/nubit-agent /usr/local/bin/nubit-agent-entrypoint \
	&& mkdir -p /var/lib/nubit-agent /srv/nubit/sites /run/php /run/sshd /etc/ssh/sshd_config.d /var/log/nubit /var/run/mysqld /etc/stalwart /var/lib/stalwart \
	&& chown mysql:mysql /var/run/mysqld

ENV NUBIT_AGENT_LISTEN_ADDR=127.0.0.1:9090 \
	NUBIT_AGENT_STATE_DIR=/var/lib/nubit-agent \
	NUBIT_AGENT_POLL_INTERVAL=5s \
	NUBIT_DATABASE_ENGINE=mariadb

EXPOSE 80 22 3306
ENTRYPOINT ["/usr/local/bin/nubit-agent-entrypoint"]
