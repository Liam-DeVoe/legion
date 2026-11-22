#!/usr/bin/env bash
# The live proof of the tree's cut-over (k31 of dispatch://INFRA-4730; R21 and R22 of
# dispatch://INFRA-3320): the release before runs on a database and stops, and this release boots
# on the same database, is stopped after each step of its first boot's conversion and booted again,
# and is booted once more. It first runs scripts/e2e/member-becomes-root.sh, whose two drivers,
# TestAMemberThatBecomesARootIsRelaunchedAndRegisters and TestAGateHolderUnderAParentRegistersItsOwnGate,
# have this proof's CI lane as their home (dispatch://INFRA-4730's t7), and fails when it fails.
#
# The driver (packages/daemon/internal/daemon/tree_cutover_live_test.go, which this script builds
# with `-tags e2e`) builds and starts the real Dispatch server from this checkout's packages/envoy on
# a database of its own, with its events on the run's nats-server and its parent-status rule off,
# since production's cut-over runs before DISPATCH_PARENT_STATUS_RULE is turned on (k33), and the
# release before's lingering trees hand over children of a closed root, which the rule refuses;
# makes the daemon's one database; and runs the release
# before's driver (tree_release_before_live_test.go, built from the merge base's tree,
# lib/tree-proof.sh) as a child process, which boots that release's daemon and drives it: a root at
# work with its gate open and three children (one in planning whose planner the oldest plugin
# contract in the fleet registered and on which a person's decision is kept, one in planning whose
# parent the person took off, one the person closed, whose claims retired with their session names
# kept and whose production check is open), a root at work set under that root, a root waiting on
# an issue nobody hands to Legion, a root whose tree the operator holds, and a root the person closed
# whose tree lingers with its deadline two days ahead and its cleanup reserved, one child of it
# closed and two handed over, the hand-over write of one landed in Dispatch and of the other not.
# What that release cannot leave in the minutes a run has (the linger, its cleanup and hand-overs,
# the operator's hold, the production check, the kept decision) its driver writes with the daemon
# stopped, as that release writes it. Then this release boots in the driver's own process. Each step
# prints `== <name>`, what it observed and `ok <name> (<s>s)`; the first that fails ends the run
# non-zero, naming it. It finds every run, claim, session, session-name reservation, slot and
# production check kept; the linger deadline the retention deadline of each issue it covered, with
# its retention_end row; the reserved cleanup and the operator's hold on each issue they covered;
# the landed hand-over a root of its own carrying its run, and that write's later event read as the
# daemon's own; each issue in Legion's work in one list of the state; R18's notice of the root under
# a parent at work told to its own architect and the one above it, once; the kept decision named by
# the next catch-up; the oldest contract's registration, ready, activity, exit, credential, handoff
# and phase completion answered; Dispatch's Legion board grouping each issue by its gate root
# (Dispatch's metrics have no route, and place an issue in a tree by the board's one rule);
# daemon.convertedAt set by the first finished conversion and unchanged by the later boots; and the
# last boot changing nothing (each table the conversion writes, read before and after it, equal). Its
# last line is the driver's `tree cutover: PASS` and then the run's.
#
# Run it from a checkout on a Linux host with go, git, tar, bun (after `bun install` at the
# checkout's root), awk and python3, as
#   LEGION_TEST_PG_DSN=postgres://<user>@127.0.0.1:<port>/<database>?sslmode=disable \
#     bash scripts/e2e/tree-cutover.sh
# LEGION_TEST_PG_DSN is required and names the PostgreSQL both daemons migrate and run on; the
# driver also creates Dispatch's database and the daemon's on the same server (its role must be
# allowed to create and drop databases, and the server must offer the pgvector extension, which
# Dispatch's migrations create) and drops them on exit. That server must be this machine's:
# lib/live-daemon-proof.sh refuses a host that is not 127.0.0.1, localhost or ::1 with exit 2. The
# release before is `git merge-base HEAD origin/main`, read at run time (lib/tree-proof.sh): a
# shallow checkout, CI's, fetches its history first, and the oldest contract's commit with it. The
# broker is the run's own, a nats-server from LEGION_E2E_NATS_BIN or the pinned one installed with
# `go install`. TREE_CUTOVER_EVIDENCE_DIR (default a fresh directory under TMPDIR, or /tmp when it is
# unset, kept and printed) keeps the transcript, the nats-server's log, both daemons' logs
# (daemon.log), Dispatch's (dispatch.log), the build of the release before's driver, the driver's
# output, and member-becomes-root.sh's evidence. Nothing reaches github.com or a deployed Dispatch;
# `bun install` of the old plugin root reaches the package registry the checkout's lockfile names,
# once per commit. A run takes five to fifteen minutes: three daemon builds, Dispatch's, and the old
# root's install on a cold cache.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
# The run's own name for its evidence directory: lib/live-daemon-proof.sh, which lib/tree-proof.sh
# sources, starts its own `evidence` empty.
cutover_evidence=${TREE_CUTOVER_EVIDENCE_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/legion-e2e-tree-cutover-evidence.XXXXXXXX")}
mkdir -p "$cutover_evidence"
echo "== member-becomes-root.sh, the two drivers this lane is the home of"
MEMBER_BECOMES_ROOT_EVIDENCE_DIR="$cutover_evidence/member-becomes-root" bash "$root/scripts/e2e/member-becomes-root.sh" || {
  echo "FAIL member-becomes-root.sh: see $cutover_evidence/member-becomes-root" >&2
  exit 1
}
# shellcheck source-path=SCRIPTDIR source=lib/tree-proof.sh
. "$root/scripts/e2e/lib/tree-proof.sh"
tree_proof 'tree cutover' tree-cutover TestTheTreeCutoverLive "$cutover_evidence"
