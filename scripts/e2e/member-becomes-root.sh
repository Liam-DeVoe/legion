#!/usr/bin/env bash
# The live proof that a member whose own architect runs, and that becomes a root by a change of
# parent, has that architect relaunched in its session under its own gate root, and that both the
# legion tool of this checkout and the tool of the oldest plugin contract in the fleet then
# register the member's own gate (k32 of dispatch://INFRA-4730, decision 13; R22 of
# dispatch://INFRA-3320). The driver
# (packages/daemon/internal/daemon/member_root_live_test.go, which this script builds with
# `-tags e2e`) builds and starts the real Dispatch server from this checkout's packages/envoy on a
# database of its own, with its events on the run's nats-server, and boots the daemon in its own
# process reading them (the rig of person_handover_scratch_dispatch_test.go). Each tool is
# packages/pi-legion's, run under bun by packages/pi-legion/test/gate-holder-driver.ts, holding the
# claim as the plugin's registration answered it. The steps:
#   - the world: P, a labelled root, is admitted and its architect registered; M, filed under P, is
#     released by P's architect, and its first phase start launches M's own architect with
#     LEGION_TREE=P, which its registration answers too; each tool refuses to register M's gate,
#     naming P, with no request sent to the daemon;
#   - the change of parent: the person takes M out from under P; the daemon relaunches M's
#     architect in its session with LEGION_TREE=M, its registration answers M, and each tool's
#     register_gate of M's own spec is taken, the daemon recording the gate.
#
# Run it from a checkout on a Linux host with go, git, bun (after `bun install` at the checkout's
# root), awk and python3, as
#   LEGION_TEST_PG_DSN=postgres://<user>@127.0.0.1:<port>/<database>?sslmode=disable \
#     bash scripts/e2e/member-becomes-root.sh
# LEGION_TEST_PG_DSN is required and names the PostgreSQL the daemon migrates and runs on, as
# register-gate-basis.sh says, and lib/live-daemon-proof.sh refuses one that is not this machine's.
# The checkout's history must hold the oldest contract's commit: MEMBER_BECOMES_ROOT_CONTRACT
# (default 144, the lowest plugin contract the fleet's daemons served on 2026-11-20) names it, and
# lib/old-plugin-root.sh makes its root from a `git archive` of the last commit whose
# packages/daemon/internal/api/version.go declares it, `bun install`ed and kept by commit under
# OLD_PLUGIN_ROOT_CACHE (default TMPDIR); a shallow checkout, CI's, fetches its history first.
# MEMBER_BECOMES_ROOT_EVIDENCE_DIR (default a fresh directory under TMPDIR, or /tmp when it is
# unset, kept and printed) keeps the transcript, the nats-server's log, the daemon's, Dispatch's and
# the driver's output. Nothing reaches github.com or a deployed Dispatch; `bun install` of the old
# root reaches the package registry the checkout's lockfile names, once per commit. The run around
# the driver is lib/live-daemon-proof.sh's.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
command -v bun >/dev/null || {
  echo "FAIL prerequisites: bun is not on PATH; the legion tools the proof drives run under it" >&2
  exit 1
}
[ -d "$root/node_modules" ] || {
  echo "FAIL prerequisites: $root/node_modules is missing; run \`bun install\` at the checkout's root first" >&2
  exit 1
}
# CI's checkout is shallow, and the oldest contract's commit is found in the history: a shallow
# checkout fetches it from its own remote, the public repository, which needs no credential.
if [ "$(git -C "$root" rev-parse --is-shallow-repository 2>/dev/null)" = true ]; then
  echo "member becomes root: the checkout is shallow; fetching its history to find the oldest contract's commit" >&2
  git -C "$root" fetch --unshallow --quiet || {
    echo "FAIL prerequisites: the shallow checkout at $root could not fetch its history (git fetch --unshallow)" >&2
    exit 1
  }
fi
old_root=$(bash "$root/scripts/e2e/lib/old-plugin-root.sh" "$root" "${MEMBER_BECOMES_ROOT_CONTRACT:-144}" \
  packages/pi-legion/test/gate-holder-driver.ts) || {
  echo "FAIL prerequisites: the oldest contract's plugin root could not be made (scripts/e2e/lib/old-plugin-root.sh)" >&2
  exit 1
}
# The driver binary runs from this directory, not the package's, so it is told the checkout it
# builds Dispatch from and the old tool's root.
export LEGION_E2E_REPO_ROOT="$root"
export LEGION_E2E_OLD_TOOL_ROOT="$old_root"
# shellcheck source-path=SCRIPTDIR source=lib/live-daemon-proof.sh
. "$root/scripts/e2e/lib/live-daemon-proof.sh"
live_daemon_proof 'member becomes root' member-becomes-root \
  'TestAMemberThatBecomesARootIsRelaunchedAndRegisters|TestAGateHolderUnderAParentRegistersItsOwnGate' \
  "${MEMBER_BECOMES_ROOT_EVIDENCE_DIR:-}"
