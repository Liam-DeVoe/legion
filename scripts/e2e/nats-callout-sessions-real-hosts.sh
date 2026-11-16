#!/usr/bin/env bash
# scripts/e2e/nats-callout-sessions-real-hosts.sh — nats-callout-sessions.sh with the real hosts:
# the Oh My Pi binary and the pinned Claude Code, each running this checkout's plugin, rather than
# each plugin under a stand-in host. It is that script's `real` mode and nothing else; its header
# holds the checks, the negative control and every input. This entry point exists because a script
# is in one list of .github/e2e-scripts.json, and this mode needs a model route and the omp binary,
# which a GitHub-hosted runner has not got, so it is listed under `elsewhere`.
#
#   LEGION_TEST_PG_DSN=postgres://<user>@127.0.0.1:<port>/<database>?sslmode=disable \
#   LEGION_E2E_MODEL_BASE_URL=<endpoint> LEGION_E2E_MODEL_KEY_FILE=<key file> \
#   LEGION_E2E_MODEL_ID=anthropic/<id> LEGION_E2E_CLAUDE_MODEL=<id> \
#     bash scripts/e2e/nats-callout-sessions-real-hosts.sh [--binaries <dir>]
set -euo pipefail
NATS_CALLOUT_SESSIONS_HOSTS=real exec bash "$(dirname "${BASH_SOURCE[0]}")/nats-callout-sessions.sh" "$@"
