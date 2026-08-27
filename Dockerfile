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
		openssh-server mariadb-server cron \
	&& rm -rf /var/lib/apt/lists/* /tmp/debsuryorg-archive-keyring.deb

COPY --from=build /nubit-agent /usr/local/bin/nubit-agent
COPY docker/systemctl /usr/local/bin/systemctl
COPY docker/php-fpm-wrapper.sh /usr/local/bin/php-fpm8.4
COPY docker/php-fpm-wrapper.sh /usr/local/bin/php-fpm8.5
COPY docker/Caddyfile /etc/caddy/Caddyfile
COPY docker/sites-enabled/00-ready.caddy /etc/caddy/sites-enabled/00-ready.caddy
COPY docker/entrypoint.sh /usr/local/bin/nubit-agent-entrypoint
RUN chmod 0755 /usr/local/bin/systemctl /usr/local/bin/php-fpm8.4 /usr/local/bin/php-fpm8.5 /usr/local/bin/nubit-agent /usr/local/bin/nubit-agent-entrypoint \
	&& mkdir -p /var/lib/nubit-agent /srv/nubit/sites /run/php /run/sshd /etc/ssh/sshd_config.d /var/log/nubit /var/run/mysqld \
	&& chown mysql:mysql /var/run/mysqld

ENV NUBIT_AGENT_LISTEN_ADDR=127.0.0.1:9090 \
	NUBIT_AGENT_STATE_DIR=/var/lib/nubit-agent \
	NUBIT_AGENT_POLL_INTERVAL=5s \
	NUBIT_DATABASE_ENGINE=mariadb

EXPOSE 80 22 3306
ENTRYPOINT ["/usr/local/bin/nubit-agent-entrypoint"]
