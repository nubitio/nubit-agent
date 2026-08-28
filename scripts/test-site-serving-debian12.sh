#!/bin/sh
set -eu

repository_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

docker build \
  --file "$repository_dir/tests/integration/Dockerfile.serving" \
  --tag nubit-agent-serving-integration \
  "$repository_dir"
docker run --rm \
  --env NUBIT_DEBIAN_INTEGRATION=1 \
  --env NUBIT_TEST_SUPERVISE=1 \
  nubit-agent-serving-integration
