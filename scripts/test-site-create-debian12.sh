#!/bin/sh
set -eu

repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

docker build \
  --file "$repository_dir/tests/integration/Dockerfile.site-create" \
  --tag nubit-agent-site-create-integration \
  "$repository_dir"
docker run --rm \
  --env NUBIT_DEBIAN_INTEGRATION=1 \
  nubit-agent-site-create-integration
