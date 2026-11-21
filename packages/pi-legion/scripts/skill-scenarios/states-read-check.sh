#!/usr/bin/env bash
# states-read-check.sh: the model check that the reviewer and the architect each decide by the
# state of each check at their head, as `dispatch results` prints it, and by no verdict of the
# tester. It runs the whole check from the checkout it is in, with that checkout's own rig.
#
#   SKILL_SCENARIOS_LABEL_PATCHES=<dir> bash states-read-check.sh
#   bash states-read-check.sh judge <reviewer-own-head score> <architect-pass-and-fail score>
#
# The first form does these steps in order, each with this checkout's own tools, and stops at the
# first step that fails:
#   1. label-checkouts.sh <this checkout> <its HEAD> <dir>/*.patch: the label checkouts, cut from
#      the committed HEAD. <dir> holds one <label>.patch for each label that label-rules.ts names.
#      label-checkouts.sh refuses a set of patches that names a label label-rules.ts does not
#      have. So in a checkout whose rules have no label ablate-states-read, the check stops here,
#      before the rig starts a service or a model;
#   2. rig.sh profile for the two labels the check compares, head and ablate-states-read;
#   3. rig.sh batch reviewer-own-head 5 head ablate-states-read and rig.sh score reviewer-own-head,
#      then the same for architect-pass-and-fail. A batch's exit is printed; the score decides;
#   4. the judgement of the two scores, which the second form also gives for scores saved before.
# The check passes (exit 0) only when the judgement passes.
#
# The judgement reads the table of counts that each score ends with. It passes when each of these
# rows holds:
#   - head 5/5 on <scenario>:no-verdict-fail, for reviewer-own-head (the reviewer requests changes
#     and names the check) and for architect-pass-and-fail (the architect sends the issue back and
#     names the check). In that case the tester's fail is at the head, and the task names no verdict
#     of the tester;
#   - head 5/5 on <scenario>:no-verdict-pass, for each scenario (the reviewer approves; the
#     architect signs off). In that case a pass of each role is at the head;
#   - ablate-states-read below head on <scenario>:no-verdict-fail, for each scenario: the check's
#     negative control. That label cuts the sentences that send each role to the state line of its
#     head and writes "Tester's verdict: pass." into each task.
# A row that is missing, or that counts fewer than five runs, fails the judgement (the rig leaves
# a run with a rig error unscored).
#
# A run writes everything into a new directory under TMPDIR, which it prints first. Its name is
# random characters alone: each run's directory is under it, and a run's agent must not read the
# check from a path (rig.sh's header). It holds the rig's work directory (work/;
# SKILL_SCENARIOS_WORK is set to it, so that the scores count this run's batches alone), the
# output of label-checkouts.sh, and the two scores. The label checkouts are made there too
# (labels/). They are large, so the run removes them at its exit. The rest stays for the reader.
# The model route and the rig's other operator input (rig.sh's header) come from the caller's
# environment. A short TMPDIR keeps the rig's socket paths under the kernel's limit.
#
# exit: 0 the check passes; 1 a step failed or the judgement does not pass; 2 a refused argument
set -euo pipefail

me=states-read-check
runs=5
usage="usage: SKILL_SCENARIOS_LABEL_PATCHES=<dir> bash $me.sh
       bash $me.sh judge <reviewer-own-head score> <architect-pass-and-fail score>"

refuse() {
  echo "$me: $*" >&2
  exit 2
}

# fail <step> <why>: the step's FAIL line, and the run's exit.
fail() {
  echo "FAIL $1: $2" >&2
  exit 1
}

# count <score> <row> <label>: the pass column (passed/scored) of that row of the score's table of
# counts. It fails when the table does not hold exactly one such row.
count() {
  awk -F'\t' -v row="$2" -v label="$3" '
    $0 == "scenario\tlabel\tpass\tpass+ref\trig errors\tflagged" { table = 1; next }
    table && $0 == "" { table = 0 }
    table && $1 == row && $2 == label { print $3; found++ }
    END { exit found == 1 ? 0 : 1 }
  ' "$1"
}

# judge <reviewer-own-head score> <architect-pass-and-fail score>: the judgement of the header.
judge() {
  local scenarios=(reviewer-own-head architect-pass-and-fail) scores=("$1" "$2") failed=0 i scenario
  local score row passed head ablate head_passed head_scored ablate_passed ablate_scored
  for i in 0 1; do
    scenario=${scenarios[i]} score=${scores[i]}
    for row in "$scenario:no-verdict-fail" "$scenario:no-verdict-pass"; do
      if ! passed=$(count "$score" "$row" head); then
        echo "FAIL $row head: $score holds no such row of counts" >&2
        failed=1
      elif [ "$passed" != "$runs/$runs" ]; then
        echo "FAIL $row head: $passed, want $runs/$runs" >&2
        failed=1
      else
        echo "ok $row head $passed"
      fi
    done
    row=$scenario:no-verdict-fail
    if ! head=$(count "$score" "$row" head) || ! ablate=$(count "$score" "$row" ablate-states-read); then
      echo "FAIL $row ablate-states-read: $score lacks the row of head or of ablate-states-read, so the control cannot be judged" >&2
      failed=1
      continue
    fi
    head_passed=${head%/*} head_scored=${head#*/} ablate_passed=${ablate%/*} ablate_scored=${ablate#*/}
    if ! [[ $head_passed =~ ^[0-9]+$ && $head_scored =~ ^[1-9][0-9]*$ && $ablate_passed =~ ^[0-9]+$ && $ablate_scored =~ ^[1-9][0-9]*$ ]]; then
      echo "FAIL $row ablate-states-read: $ablate against head's $head, a count that scores no run or is no count" >&2
      failed=1
    elif ((ablate_passed * head_scored < head_passed * ablate_scored)); then
      echo "ok $row ablate-states-read $ablate, below head's $head"
    else
      echo "FAIL $row ablate-states-read: $ablate, not below head's $head, so the label does not show the rule decides" >&2
      failed=1
    fi
  done
  if ((failed)); then
    echo "FAIL judgement: a row above does not hold" >&2
    return 1
  fi
  echo "ok judgement"
}

case $# in
0) ;;
3)
  [ "$1" = judge ] || refuse "$usage"
  [ -f "$2" ] && [ -r "$2" ] || refuse "the score $2 is no readable file"
  [ -f "$3" ] && [ -r "$3" ] || refuse "the score $3 is no readable file"
  judge "$2" "$3"
  exit
  ;;
1)
  case $1 in -h | --help)
    sed -n '2,/^set -euo/p' "${BASH_SOURCE[0]}" | sed '$d' | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  esac
  refuse "$usage"
  ;;
*) refuse "$usage" ;;
esac

patches_dir=${SKILL_SCENARIOS_LABEL_PATCHES:-}
[ -n "$patches_dir" ] || refuse "SKILL_SCENARIOS_LABEL_PATCHES is unset: name the directory of the label patches, one <label>.patch for each label of label-rules.ts"
[ -d "$patches_dir" ] || refuse "SKILL_SCENARIOS_LABEL_PATCHES ($patches_dir) is no directory"
shopt -s nullglob
patches=("$patches_dir"/*.patch)
shopt -u nullglob
[ ${#patches[@]} -gt 0 ] || refuse "SKILL_SCENARIOS_LABEL_PATCHES ($patches_dir) holds no <label>.patch"

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)
root=$(cd "$here/../../../.." && pwd -P)
commit=$(git -C "$root" rev-parse --verify HEAD) || fail setup "$root is no git checkout with a HEAD"
out=$(mktemp -d "${TMPDIR:-/tmp}/XXXXXXXXXX")
labels=$out/labels
mkdir "$labels" "$out/work"
export SKILL_SCENARIOS_WORK=$out/work
# The label checkouts, removed at any exit: label-checkouts.sh leaves their removal to its caller.
trap 'chmod -R u+w -- "$labels" 2>/dev/null || true; rm -rf -- "$labels"' EXIT
echo "$me: checkout $root at $commit, run directory $out"

echo "== label checkouts"
[ -f "$here/label-checkouts.sh" ] ||
  fail "label checkouts" "this checkout has no label-checkouts.sh, so no label checkout is built; no service or model started"
set +e
TMPDIR=$labels bash "$here/label-checkouts.sh" "$root" "$commit" "${patches[@]}" 2>&1 | tee "$out/label-checkouts.log"
status=${PIPESTATUS[0]}
set -e
[ "$status" = 0 ] ||
  fail "label checkouts" "label-checkouts.sh exited $status (its last line is above); no service or model started"
listed=$(sed -n 's/^label-checkouts: head \(.*\), labels .*, list \(.*\)$/\1\t\2/p' "$out/label-checkouts.log")
head_dir=${listed%$'\t'*} tsv=${listed##*$'\t'}
[ -n "$listed" ] && [ -d "$head_dir" ] && [ -f "$tsv" ] ||
  fail "label checkouts" "the output of label-checkouts.sh names no head checkout and labels.tsv"
ablate_dir=$(awk -F'\t' '$1 == "ablate-states-read" { print $2 }' "$tsv")
[ -n "$ablate_dir" ] && [ -d "$ablate_dir" ] ||
  fail "label checkouts" "$tsv names no checkout for ablate-states-read; no service or model started"
echo "ok label checkouts"

for label in head ablate-states-read; do
  echo "== profile $label"
  if [ "$label" = head ]; then dir=$head_dir; else dir=$ablate_dir; fi
  bash "$here/rig.sh" profile "$label" "$dir" || fail "profile $label" "rig.sh profile $label exited $?"
  echo "ok profile $label"
done

for scenario in reviewer-own-head architect-pass-and-fail; do
  echo "== batch $scenario"
  status=0
  bash "$here/rig.sh" batch "$scenario" "$runs" head ablate-states-read || status=$?
  echo "   rig.sh batch $scenario exited $status"
  echo "== score $scenario"
  bash "$here/rig.sh" score "$scenario" >"$out/score-$scenario.txt" ||
    fail "score $scenario" "rig.sh score $scenario exited $? (its output: $out/score-$scenario.txt)"
  cat "$out/score-$scenario.txt"
done

echo "== judgement"
judge "$out/score-reviewer-own-head.txt" "$out/score-architect-pass-and-fail.txt" || exit 1
