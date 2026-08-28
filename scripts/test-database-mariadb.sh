#!/bin/sh
set -eu

repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

docker build \
  --file "$repository_dir/tests/integration/Dockerfile.database" \
  --tag nubit-agent-database-integration \
  "$repository_dir"
docker run --rm \
  --env NUBIT_DEBIAN_INTEGRATION=1 \
  --env NUBIT_DATABASE_ENGINE=mariadb \
  nubit-agent-database-integration
