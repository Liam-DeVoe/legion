# shellcheck shell=bash
# The run the two live proofs of the tree's cut-over and rollback (tree-cutover.sh, k31, and
# tree-rollback.sh, k44, of dispatch://INFRA-4730) make around their drivers: both boot the release
# before and this release on one database. Sourced by the entry script, never run. The entry script
# sets `set -euo pipefail` and root, the checkout it sources this file from, and calls tree_proof as
# its last command.
#
# tree_proof LABEL SLUG TEST EVIDENCE runs, after the checks of its own below, lib/live-daemon-proof.sh's
# run of TEST, this checkout's driver, with EVIDENCE as its evidence directory (empty: a fresh one
# under TMPDIR, or /tmp when it is unset). Before that run it:
#   - finds the release before: `git merge-base HEAD origin/main`, read now, so a branch that merged
#     main forward compares itself with that main. A shallow checkout, CI's, fetches its history and
#     origin/main from its own remote, the public repository, first. A merge base whose tree carries
#     this release's conversion (packages/daemon/internal/store/migrations/20261122154645_issue_facts.up.sql)
#     has no release before to convert from, and the run is refused with exit 2;
#   - makes the root of the oldest plugin contract in the fleet (lib/old-plugin-root.sh, with
#     TREE_PROOF_CONTRACT, default 144, the lowest the fleet's daemons served on 2026-11-20), with
#     this checkout's packages/pi-legion/test/daemon-client-driver.ts and gate-holder-driver.ts
#     copied in, and names it in LEGION_E2E_OLD_TOOL_ROOT;
#   - takes the tree of the release before with `git archive` into a directory of the run's own under
#     TMPDIR, lays this checkout's packages/daemon/internal/daemon/tree_release_before_live_test.go
#     over it, as busy-relaunch-delivery.sh's negative control lays its driver, builds that tree's
#     daemon package's `-tags e2e` test binary, and names it in LEGION_E2E_BEFORE_DRIVER: the
#     driver runs it as a child process for each part the release before plays. That driver names
#     only what both releases carry, so it builds there.
# The checkout is named in LEGION_E2E_REPO_ROOT, which the driver builds Dispatch from and reads
# docs/kubernetes.md's way back from. Each step prints `== <step>`, what it observed and
# `ok <step> (<s>s)`, the first that fails `FAIL <step>: <why>` and ends the run non-zero. The
# directory of the release before is removed on any exit; the old plugin's root is kept by commit
# under OLD_PLUGIN_ROOT_CACHE (lib/old-plugin-root.sh). The run's last line is
# lib/live-daemon-proof.sh's `LABEL e2e: PASS in <n>s (evidence: <dir>)`, or its FAIL line.

# shellcheck source-path=SCRIPTDIR source=live-daemon-proof.sh
. "$root/scripts/e2e/lib/live-daemon-proof.sh"

# The tree proof's own state, global because its EXIT trap reads it.
tree_before_work=''

tree_cleanup() {
  [ -z "$tree_before_work" ] || rm -rf "$tree_before_work"
}

tree_proof() {
  local label=$1 slug=$2 test_name=$3 base base_sha old_root status tool
  # The evidence directory is made here, before live_daemon_proof's, so this run's own steps keep
  # their logs in it too.
  evidence=${4:-$(mktemp -d "${TMPDIR:-/tmp}/legion-e2e-$slug-evidence.XXXXXXXX")}
  mkdir -p "$evidence"
  trap tree_cleanup EXIT

  begin 'the release before'
  for tool in git tar bun; do
    command -v "$tool" >/dev/null || fail "$tool is not on PATH"
  done
  [ -d "$root/node_modules" ] || fail "$root/node_modules is missing; run \`bun install\` at the checkout's root first"
  if [ "$(git -C "$root" rev-parse --is-shallow-repository 2>/dev/null)" = true ]; then
    note "the checkout is shallow; fetching its history to find the release before and the oldest contract's commit"
    git -C "$root" fetch --unshallow --quiet || fail "the shallow checkout at $root could not fetch its history (git fetch --unshallow)"
  fi
  if ! git -C "$root" rev-parse --verify --quiet refs/remotes/origin/main >/dev/null; then
    git -C "$root" fetch --quiet origin main:refs/remotes/origin/main ||
      fail "the checkout at $root has no origin/main and could not fetch it"
  fi
  base=$(git -C "$root" merge-base HEAD refs/remotes/origin/main) || fail "HEAD and origin/main share no commit"
  base_sha=$(git -C "$root" rev-parse --short=12 "$base")
  if git -C "$root" cat-file -e "$base:packages/daemon/internal/store/migrations/20261122154645_issue_facts.up.sql" 2>/dev/null; then
    refuse "the merge base $base_sha already stores no tree (it carries this release's conversion), so it is no release before to cut over from"
  fi
  note "the release before is the merge base with origin/main, $base_sha: $(git -C "$root" log -1 --format=%s "$base")"
  tree_before_work=$(mktemp -d "${TMPDIR:-/tmp}/legion-e2e-$slug-before.XXXXXXXX")
  git -C "$root" archive "$base" | tar -x -C "$tree_before_work" || fail "could not take the tree of $base_sha"
  cp "$root/packages/daemon/internal/daemon/tree_release_before_live_test.go" \
    "$tree_before_work/packages/daemon/internal/daemon/tree_release_before_live_test.go" ||
    fail "could not lay tree_release_before_live_test.go over the tree of $base_sha"
  go -C "$tree_before_work/packages/daemon" test -c -tags e2e -o "$tree_before_work/before.test" ./internal/daemon \
    >"$evidence/before-build.log" 2>&1 || fail "the release before's driver did not build in the tree of $base_sha; see $evidence/before-build.log"
  note "built the release before's driver from the tree of $base_sha with this checkout's tree_release_before_live_test.go laid over it: sha256 $(sha256sum "$tree_before_work/before.test" | cut -d' ' -f1)"
  bash "$root/scripts/e2e/lib/built-from.sh" "$root" | while read -r line; do note "this release, $line"; done
  pass

  begin 'the oldest contract'
  old_root=$(bash "$root/scripts/e2e/lib/old-plugin-root.sh" "$root" "${TREE_PROOF_CONTRACT:-144}" \
    packages/pi-legion/test/daemon-client-driver.ts packages/pi-legion/test/gate-holder-driver.ts 2>"$evidence/old-plugin-root.log") ||
    fail "the oldest contract's plugin root could not be made; see $evidence/old-plugin-root.log"
  note "daemon API contract ${TREE_PROOF_CONTRACT:-144}'s plugin root: $old_root"
  pass

  export LEGION_E2E_REPO_ROOT="$root" LEGION_E2E_OLD_TOOL_ROOT="$old_root" LEGION_E2E_BEFORE_DRIVER="$tree_before_work/before.test"
  # live_daemon_proof ends the shell it runs in, and its EXIT trap is that subshell's, so this run's
  # own trap still removes the release before's tree.
  set +e
  (
    set -e
    live_daemon_proof "$label" "$slug" "$test_name" "$evidence"
  )
  status=$?
  set -e
  return "$status"
}
