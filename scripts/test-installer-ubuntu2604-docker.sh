#!/bin/sh
set -eu

repository_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)

docker build \
  --platform linux/amd64 \
  --file "$repository_dir/tests/integration/Dockerfile.installer-ubuntu2604" \
  --tag nubit-agent-installer-ubuntu2604-integration \
  "$repository_dir"
docker run --rm --platform linux/amd64 nubit-agent-installer-ubuntu2604-integration
