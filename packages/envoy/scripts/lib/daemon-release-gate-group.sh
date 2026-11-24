#!/usr/bin/env bash
# packages/envoy/scripts/lib/daemon-release-gate-group.sh <group> <tally file>
#
# One scenario group of daemon-release-gate.test.sh, in a process of its own: it sources the
# harness, which gives this process its own temporary directory, its own fakes and its own case
# roots, then the client fixtures, then lib/daemon-release-gate-cases-<group>.sh, and writes the
# number of checks that failed to <tally file>. It exits 0 when that number is 0 and 1 otherwise,
# so the test that runs the groups reads both the tally and the exit, and a group that ends any
# other way (a harness that could not start, a syntax error, a kill) fails that test by name.
#
# daemon-release-gate.test.sh runs the groups; run this file by hand to read one group alone:
#   bash lib/daemon-release-gate-group.sh developer-box /dev/null
set -euo pipefail

[ "$#" -eq 2 ] || {
  echo "usage: daemon-release-gate-group.sh <group> <tally file>" >&2
  exit 2
}
group=$1
tally=$2
project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
scripts="${project_root}/packages/envoy/scripts"
# Each group's scenario file by its name, so a reader of what CI runs (check-test-reach.sh, which
# reads a script as run where a text CI runs names it) finds each of them run here.
case "$group" in
  daemons) cases=daemon-release-gate-cases-daemons.sh ;;
  listeners) cases=daemon-release-gate-cases-listeners.sh ;;
  vm-hub) cases=daemon-release-gate-cases-vm-hub.sh ;;
  image-clients) cases=daemon-release-gate-cases-image-clients.sh ;;
  developer-box) cases=daemon-release-gate-cases-developer-box.sh ;;
  versions) cases=daemon-release-gate-cases-versions.sh ;;
  project-fields) cases=daemon-release-gate-cases-project-fields.sh ;;
  short-reads) cases=daemon-release-gate-cases-short-reads.sh ;;
  *)
    echo "daemon-release-gate-group.sh: no scenario file for the group ${group}" >&2
    exit 2
    ;;
esac
cases="${scripts}/lib/${cases}"
[ -f "$cases" ] || {
  echo "daemon-release-gate-group.sh: no scenario file for the group ${group}: ${cases}" >&2
  exit 2
}
# shellcheck disable=SC2034 # the harness reads script, the gate under test, as it is sourced
readonly script="${scripts}/daemon-release-gate.sh"
# Each directive below names its file as `shellcheck -x` run in packages/envoy/scripts reads it.
# shellcheck source=lib/daemon-release-gate-harness.sh
source "${scripts}/lib/daemon-release-gate-harness.sh"
# The --clients fixtures every scenario file may use; they define and run nothing.
# shellcheck source=lib/daemon-release-gate-fixtures-clients.sh
source "${scripts}/lib/daemon-release-gate-fixtures-clients.sh"
# shellcheck source=/dev/null # one of lib/daemon-release-gate-cases-*.sh, named by the group
source "$cases"

printf '%s\n' "$failures" >"$tally"
[ "$failures" -eq 0 ]
