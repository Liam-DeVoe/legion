#!/usr/bin/env bash
# Tests of stores-moved-production.sh's reading and comparison: each runs over the answers under
# testdata/stores-moved-production/, and the comparison over the broken records of the negative
# controls of c-moved-in-production, with a near miss beside each. It sources the driver, which
# runs no step when sourced, and reads no server.
#
#   bash e2e/acceptance/stores-moved-production.test.sh   (from packages/dispatch; prints one line a
#                                                          check and a last line
#                                                          `stores-moved-production.test.sh: N checks
#                                                          passed`, or exits 1 naming the failures)
#
# The answers are production's, read through its API with the pod's own DISPATCH_URL and
# DISPATCH_TOKEN_FILE on 2026-11-23 (`curl -H "Authorization: Bearer …" "$DISPATCH_URL<path>"` of
# /healthz, /api/v1/priorities, /api/v1/priorities/{id}/history, /api/v1/projects,
# /api/v1/issues?updated_since=2026-11-15T00:00:00Z&limit=…&offset=0, /api/v1/issues/{key}/steps and
# /api/v1/projects/{key}/plans), then cut to a few rows each and written with placeholders: every
# key, id, name, text and person is the fixture's own, and every field and its type is the answer's.
# healthz-short-commit.json is healthz.json with a commit cut short, a near miss.

set -euo pipefail
here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR source=stores-moved-production.sh
. "$here/stores-moved-production.sh"
set +e +o pipefail
data="$here/testdata/stores-moved-production"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

checks=0
failures=()
# is NAME GOT WANT: GOT equals WANT.
is() {
  checks=$((checks + 1))
  if [[ "$2" == "$3" ]]; then
    printf 'ok: %s\n' "$1"
  else
    printf 'FAIL: %s: got %q, want %q\n' "$1" "$2" "$3"
    failures+=("$1")
  fi
}

# --- reading the answers ----------------------------------------------------------------------
is "the commit /healthz names" "$(healthz_commit "$data/healthz.json")" 1111111111111111111111111111111111111111
is "near miss: a commit cut short is none" "$(healthz_commit "$data/healthz-short-commit.json")" ""
is "the priorities listed" "$(priority_ids "$data/priorities.json" | tr '\n' ' ')" "priority-a priority-b "
is "the projects listed" "$(project_keys "$data/projects.json" | tr '\n' ' ')" "CORE OTHER "
is "the issues of a page" "$(page_keys "$data/page.json" | tr '\n' ' ')" "CORE-1 CORE-2 "
is "the issues the list holds in all" "$(page_total "$data/page.json")" 2
is "the runs of an issue that ran" "$(run_count "$data/steps-core-1.json")" 2
is "near miss: an issue that never ran holds none" "$(run_count "$data/steps-core-2.json")" 0
is "the record's name" "$(artifact_name stores-before-json)" stores-before.json
is "near miss: a slug of no extension names itself" "$(artifact_name stores-before)" stores-before

# --- the record -------------------------------------------------------------------------------
mkdir -p "$scratch/answers/history" "$scratch/answers/steps" "$scratch/answers/plans"
cp "$data/priorities.json" "$scratch/answers/priorities.json"
cp "$data/history-priority-a.json" "$scratch/answers/history/priority-a.json"
printf '[]\n' >"$scratch/answers/history/priority-b.json"
cp "$data/steps-core-1.json" "$scratch/answers/steps/CORE-1.json"
cp "$data/plans-core.json" "$scratch/answers/plans/CORE.json"
record_of 1111111111111111111111111111111111111111 2026-11-23T22:00:00Z "$scratch/answers" >"$scratch/record.json"
is "the record's counts" "$(record_counts "$scratch/record.json")" \
  "2 priorities, 3 history entries, 2 runs on 1 issues, 1 plans of 1 projects, production at 1111111111111111111111111111111111111111"
is "the record's priorities read again" "$(recorded_priorities "$scratch/record.json" | tr '\n' ' ')" "priority-a priority-b "
is "the record's issues read again" "$(recorded_issues "$scratch/record.json")" CORE-1
is "the record's projects read again" "$(recorded_projects "$scratch/record.json")" CORE

# --- the compare ------------------------------------------------------------------------------
is "every history entry reads back" "$(history_differences "$scratch/record.json" priority-a "$data/history-priority-a.json")" ""
is "every run reads back" "$(steps_differences "$scratch/record.json" CORE-1 "$data/steps-core-1.json")" ""
is "every plan reads back" "$(plans_differences "$scratch/record.json" CORE "$data/plans-core.json")" ""
# Writes after the record add to what it holds and are compared with nothing: a history entry,
# a run, a field the record held null and now set, a plan of an issue finished since, and an
# advances_revision moved by the move.
jq '[{field: "title", value: "Priority A, renamed", issue: null, asserted: true, actor: {kind: "user", id: "bob", proven: true},
  at: "2026-11-24T00:00:00Z"}] + .' "$data/history-priority-a.json" >"$scratch/history-later.json"
is "near miss: a history entry written after the record" "$(history_differences "$scratch/record.json" priority-a "$scratch/history-later.json")" ""
jq '.tasks[0].steps[1] += {ended_at: "2026-11-23T05:00:00Z", result: "passed", rating: 4, rating_reason: "The plan fit the work."}
  | .tasks[0].steps += [.tasks[0].steps[0] | .id = "aaaaaaaa-0000-4000-8000-000000000001" | .attempt = 2]' \
  "$data/steps-core-1.json" >"$scratch/steps-later.json"
is "near miss: a run ended and a run added after the record" "$(steps_differences "$scratch/record.json" CORE-1 "$scratch/steps-later.json")" ""
jq '.issues += [{issue: "CORE-9", plan: {id: "p-9", issue: "CORE-9", generation: 1, source: "default", artifact_id: null, version: null, tasks: []}, ratings: []}]
  | .issues[0].advances_revision = 99999' "$data/plans-core.json" >"$scratch/plans-later.json"
is "near miss: a plan of an issue finished after the record" "$(plans_differences "$scratch/record.json" CORE "$scratch/plans-later.json")" ""

# The first control of c-moved-in-production: a scratch copy of the record with the value of one
# history entry of one priority changed.
jq '.histories["priority-a"][1].value = "B"' "$scratch/record.json" >"$scratch/record-history.json"
is "control: a history entry whose value the record holds otherwise" \
  "$(history_differences "$scratch/record-history.json" priority-a "$data/history-priority-a.json")" \
  'priority priority-a: the rank entry at 2026-11-21T08:00:00.5Z recorded "B", and reads "F" after the deploy'
jq '.histories["priority-a"][1].actor.id = "bob"' "$scratch/record.json" >"$scratch/record-actor.json"
is "control: a history entry whose actor the record holds otherwise" \
  "$(history_differences "$scratch/record-actor.json" priority-a "$data/history-priority-a.json")" \
  'priority priority-a: the rank entry at 2026-11-21T08:00:00.5Z recorded "F", and reads "F" after the deploy'
# The second control: a scratch copy of the record with a run added to the steps of one issue.
jq '.steps["CORE-1"].tasks[0].steps += [.steps["CORE-1"].tasks[0].steps[0] | .id = "bbbbbbbb-0000-4000-8000-000000000002"]' \
  "$scratch/record.json" >"$scratch/record-run.json"
is "control: a run the record holds that the steps do not" \
  "$(steps_differences "$scratch/record-run.json" CORE-1 "$data/steps-core-1.json")" \
  'issue CORE-1 run bbbbbbbb-0000-4000-8000-000000000002: no such run in its steps after the deploy'
jq '.steps["CORE-1"].tasks[0].steps[0].result = "failed"' "$scratch/record.json" >"$scratch/record-result.json"
is "a field of a run the record holds otherwise" \
  "$(steps_differences "$scratch/record-result.json" CORE-1 "$data/steps-core-1.json")" \
  'issue CORE-1 run c2e0436a-12f0-54ef-bc5e-7c0cc4d170dc: result recorded "failed", and reads "passed" after the deploy'
jq '.plans.CORE.issues[0].plan.tasks[0].steps = ["planner"]' "$scratch/record.json" >"$scratch/record-plan.json"
is "a plan the record holds otherwise" \
  "$(plans_differences "$scratch/record-plan.json" CORE "$data/plans-core.json" | cut -d: -f1)" \
  'project CORE issue CORE-1'

if [[ "${#failures[@]}" -gt 0 ]]; then
  printf 'stores-moved-production.test.sh: %d of %d checks failed: %s\n' "${#failures[@]}" "$checks" "${failures[*]}"
  exit 1
fi
printf 'stores-moved-production.test.sh: %d checks passed\n' "$checks"
