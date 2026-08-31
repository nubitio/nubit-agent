#!/bin/sh
#
# Points git at the versioned hooks in .githooks/. Run once per clone.

set -e
cd "$(git rev-parse --show-toplevel)"
git config core.hooksPath .githooks
chmod +x .githooks/* 2>/dev/null || true
echo "hooks enabled: core.hooksPath -> .githooks"
