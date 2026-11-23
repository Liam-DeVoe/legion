#!/usr/bin/env bash
# The checks c-moved-recorded (MODE=record) and c-moved-in-production (MODE=compare): production's
# priorities, runs and plans, recorded before the deploy that moves the priority store and the
# workflow store into the graph store (dispatch://INFRA-1098's R32), and compared after it.
#
# It reads production's Dispatch through its API alone, with the pod's own DISPATCH_URL and
# DISPATCH_TOKEN_FILE (or DISPATCH_TOKEN), and no pod reads production's database.
#
#   MODE=record   Fails where production's /healthz names a commit that carries this head's
#                 commit (git merge-base --is-ancestor HEAD <commit>): the record must come from
#                 the release before. It reads GET /api/v1/priorities, the history of each priority,
#                 the steps of every issue of every project updated since 2026-11-15, the day before
#                 the first run started, keeping each issue that holds a run, and the plans of each
#                 project that holds a run. A read that does not answer 200 fails naming what it
#                 read, and nothing is attached. It attaches the answers, with the commit /healthz
#                 named, as the artifact RECORD_ARTIFACT names on ISSUE (`dispatch artifact`), its
#                 one write, and prints its counts.
#   MODE=compare  Fails where production's /healthz names a commit that does not carry ISSUE's
#                 merge commit: the merge commit of the newest merged pull request of the branch
#                 legion/<ISSUE>, which `gh` answers with the role's own credential, or MERGE_COMMIT
#                 where set. It reads the record from the artifact RECORD_ARTIFACT names on ISSUE,
#                 or from the file RECORD_FILE names where that is set, and reads production again.
#                 Each recorded history entry reads back with the same field, value, issue,
#                 asserted, actor and time; each recorded run reads back in its issue's steps, each
#                 field the record holds set unchanged (a field the record holds null may hold a
#                 value now); each recorded plan reads back unchanged. It reads no
#                 advances_revision, which the move makes greater by design, and compares no write
#                 made after the record. It prints every difference and fails on any.
#
# Each passing section prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` on stderr and the run exits 1; a missing or bad input exits
# 2. It prints no SKIP line. Its reading and its comparison are functions that run when the file
# is sourced without running a step, which stores-moved-production.test.sh runs over captured
# answers.

# shellcheck disable=SC2016 # the single-quoted jq filters name jq variables, not shell ones
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: ISSUE=<the issue> RECORD_ARTIFACT=<the record's artifact slug> MODE=record|compare \
         [RECORD_FILE=<a record to compare in place of the artifact>] [MERGE_COMMIT=<the issue's merge commit>] \
         bash e2e/acceptance/stores-moved-production.sh
       (run from packages/dispatch, with the pod's own DISPATCH_URL and DISPATCH_TOKEN_FILE or
       DISPATCH_TOKEN. MODE=record attaches the record to ISSUE; MODE=compare reads it back)
USAGE
}

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

readonly driver=stores-moved-production.sh
# The day before the first run started (the spec's Design part "How the two stores move"): every
# issue that holds a run was updated since.
readonly runs_since=2026-11-15T00:00:00Z
# The page of the issue list the record reads, its largest (contracts.MaxIssuePageLimit).
readonly page_limit=250

# ---------------------------------------------------------------------------------------------
# Reading and comparing. Each reads answers of production's API, or a record, and none reads a
# global of the run, so stores-moved-production.test.sh runs each over captured answers.
# ---------------------------------------------------------------------------------------------

# healthz_commit FILE: the commit a /healthz answer names, 40 lowercase hex characters, or nothing.
healthz_commit() { jq -r '.commit // empty | select(test("^[0-9a-f]{40}$"))' "$1"; }

# priority_ids FILE: the id of each priority a GET /api/v1/priorities answer lists.
priority_ids() { jq -r '.priorities[].id' "$1"; }

# project_keys FILE: the key of each project a GET /api/v1/projects answer lists.
project_keys() { jq -r '.[].key' "$1"; }

# page_keys FILE: the key of each issue of a page of the issue list; page_total FILE: how many
# issues the list holds in all.
page_keys() { jq -r '.issues[].key' "$1"; }
page_total() { jq -r '.total' "$1"; }

# run_count FILE: how many runs a GET /api/v1/issues/{key}/steps answer holds.
run_count() { jq '[.tasks[]?.steps[]?] | length' "$1"; }

# record_of COMMIT AT DIR: the record, from the answers DIR holds: priorities.json, history/<id>.json,
# steps/<key>.json and plans/<project>.json, each the answer of its read. Each part goes to jq
# through a file of DIR's, since a production's runs outgrow an argument.
record_of() {
  local part
  for part in history steps plans; do
    json_dir "$3/$part" >"$3/$part.all" || return 1
  done
  jq -n --arg commit "$1" --arg at "$2" --arg since "$runs_since" \
    --slurpfile priorities "$3/priorities.json" --slurpfile histories "$3/history.all" \
    --slurpfile steps "$3/steps.all" --slurpfile plans "$3/plans.all" \
    '{commit: $commit, recorded_at: $at, runs_since: $since, priorities: $priorities[0],
      histories: $histories[0], steps: $steps[0], plans: $plans[0]}'
}

# json_dir DIR: an object of each NAME.json under DIR, by NAME, each its JSON.
json_dir() {
  local file
  for file in "$1"/*.json; do
    [[ -e "$file" ]] || continue
    jq -c --arg name "$(basename "$file" .json)" '{($name): .}' "$file"
  done | jq -sc 'add // {}'
}

# record_counts RECORD: what a record holds, in words.
record_counts() {
  jq -r '"\(.priorities.priorities | length) priorities, \([.histories[] | length] | add // 0) history entries, " +
    "\([.steps[] | [.tasks[]?.steps[]?] | length] | add // 0) runs on \(.steps | length) issues, " +
    "\([.plans[] | [.issues[]? | select(.plan != null)] | length] | add // 0) plans of \(.plans | length) projects, " +
    "production at \(.commit)"' "$1"
}

# history_differences RECORD ID FILE: each history entry of the priority ID that RECORD holds and
# the history answer FILE does not, with the same field, value, issue, asserted, actor and time. A
# line names the priority, the field and both values: the recorded one and the one an entry of the
# same field at the same time answers now, or none.
history_differences() {
  jq -r --arg id "$2" --slurpfile now "$3" '
    def entry: {field, value, issue, asserted, actor, at};
    ($now[0] | map(entry)) as $held
    | .histories[$id][] | entry as $e
    | select(any($held[]; . == $e) | not)
    | ([$held[] | select(.field == $e.field and .at == $e.at) | .value] | .[0]) as $other
    | "priority \($id): the \($e.field) entry at \($e.at) recorded \($e.value | tojson), and reads \(if $other == null then "none" else ($other | tojson) end) after the deploy"' "$1"
}

# steps_differences RECORD KEY FILE: each run the record holds in the steps of the issue KEY that
# the steps answer FILE does not hold, and each field the record holds set that reads otherwise.
steps_differences() {
  jq -r --arg key "$2" --slurpfile now "$3" '
    ([$now[0].tasks[]?.steps[]?] | map({(.id): .}) | add // {}) as $held
    | .steps[$key].tasks[]?.steps[]? as $run
    | if $held[$run.id] == null then "issue \($key) run \($run.id): no such run in its steps after the deploy"
      else $run | to_entries[] | select(.value != null) | select($held[$run.id][.key] != .value)
        | "issue \($key) run \($run.id): \(.key) recorded \(.value | tojson), and reads \($held[$run.id][.key] | tojson) after the deploy"
      end' "$1"
}

# plans_differences RECORD PROJECT FILE: each plan the record holds of the project's finished
# issues that the plans answer FILE does not answer unchanged.
plans_differences() {
  jq -r --arg project "$2" --slurpfile now "$3" '
    ([$now[0].issues[]? | {(.issue): .plan}] | add // {}) as $held
    | .plans[$project].issues[]? | select(.plan != null)
    | select($held[.issue] != .plan)
    | "project \($project) issue \(.issue): its plan \(.plan.id) recorded \(.plan | tojson), and reads \($held[.issue] | tojson) after the deploy"' "$1"
}

# recorded_priorities, recorded_issues, recorded_projects RECORD: what the compare reads again.
recorded_priorities() { jq -r '.histories | keys[]' "$1"; }
recorded_issues() { jq -r '.steps | keys[]' "$1"; }
recorded_projects() { jq -r '.plans | keys[]' "$1"; }

# artifact_name SLUG: the name the record is attached under, whose slug is SLUG: a slug ending in
# -json names NAME.json.
artifact_name() {
  case "$1" in
    *-json) printf '%s.json\n' "${1%-json}" ;;
    *) printf '%s\n' "$1" ;;
  esac
}

# ---------------------------------------------------------------------------------------------
# The run.
# ---------------------------------------------------------------------------------------------

# read_to SECTION WHAT PATH FILE: GET PATH from production, which must answer 200, into FILE.
read_to() {
  send GET "$3"
  want_status 200 "$1" "$2"
  cp "$work/body" "$4"
}

# production_commit SECTION: the commit production's /healthz names, in $production.
production_commit() {
  read_to "$1" "read production's /healthz" /healthz "$work/healthz.json"
  production="$(healthz_commit "$work/healthz.json")"
  [[ -n "$production" ]] || fail "$1" "read the commit production's /healthz names" "it names none: $(body)"
  git -C "$checkout" cat-file -e "$production^{commit}" 2>/dev/null ||
    fail "$1" "find production's commit $production in this checkout" "it holds no such commit: fetch origin main"
}

record() {
  local id key project offset total runs runs_total=0 issues=0 name output token_file="$1"
  local dir="$work/record"
  mkdir -p "$dir/history" "$dir/steps" "$dir/plans" "$dir/pages"
  production_commit record
  if git -C "$checkout" merge-base --is-ancestor HEAD "$production" 2>/dev/null; then
    fail record "production runs the release before" \
      "production already runs $production, which carries this head's commit $(git -C "$checkout" rev-parse HEAD)"
  fi
  pass record "production runs $production, which does not carry this head's commit"
  read_to record "read the priorities" /api/v1/priorities "$dir/priorities.json"
  while IFS= read -r id; do
    read_to record "read the history of the priority $id" "/api/v1/priorities/$(q "$id")/history" "$dir/history/$id.json"
  done < <(priority_ids "$dir/priorities.json")
  pass record "read $(priority_ids "$dir/priorities.json" | wc -l) priorities and each one's history"
  read_to record "read the projects" /api/v1/projects "$work/projects.json"
  declare -A ran=()
  while IFS= read -r project; do
    offset=0
    total=1
    while [[ "$offset" -lt "$total" ]]; do
      read_to record "read the issues of $project updated since $runs_since, from $offset" \
        "/api/v1/issues?project=$(q "$project")&updated_since=$(q "$runs_since")&limit=$page_limit&offset=$offset" "$dir/pages/page.json"
      total="$(page_total "$dir/pages/page.json")"
      while IFS= read -r key; do
        issues=$((issues + 1))
        read_to record "read the steps of $key" "/api/v1/issues/$(q "$key")/steps" "$work/steps.json"
        runs="$(run_count "$work/steps.json")"
        if [[ "$runs" -gt 0 ]]; then
          cp "$work/steps.json" "$dir/steps/$key.json"
          runs_total=$((runs_total + runs))
          ran[$project]=1
        fi
      done < <(page_keys "$dir/pages/page.json")
      offset=$((offset + page_limit))
    done
  done < <(project_keys "$work/projects.json")
  pass record "read the steps of $issues issues of every project updated since $runs_since: $runs_total runs"
  for project in "${!ran[@]}"; do
    read_to record "read the plans of $project" "/api/v1/projects/$(q "$project")/plans" "$dir/plans/$project.json"
  done
  pass record "read the plans of the ${#ran[@]} projects that hold a run"
  record_of "$production" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$dir" >"$work/record.json" ||
    fail record "assemble the record" "jq failed"
  pass record "the record holds $(record_counts "$work/record.json")"
  name="$(artifact_name "$RECORD_ARTIFACT")"
  # The record's one write: the artifact, a new version where the issue holds it already.
  if ! output="$(DISPATCH_TOKEN_FILE="$token_file" dispatch artifact --issue "$ISSUE" --name "$name" --path "$work/record.json" --force 2>&1)"; then
    fail record "attach the record as $name on $ISSUE" "$output"
  fi
  pass record "attached the record as $name on $ISSUE"
}

compare() {
  local merge record_file="$work/record.json" id key project found differences=0 count=0
  production_commit compare
  merge="${MERGE_COMMIT:-}"
  if [[ -z "$merge" ]]; then
    merge="$(cd "$checkout" && gh pr list --head "legion/$ISSUE" --state merged --json mergeCommit,mergedAt \
      --jq 'sort_by(.mergedAt) | last | .mergeCommit.oid // empty' 2>"$work/gh")" ||
      fail compare "find the merge commit of $ISSUE" "gh: $(<"$work/gh")"
  fi
  [[ -n "$merge" ]] || fail compare "find the merge commit of $ISSUE" "gh answers no merged pull request of legion/$ISSUE"
  git -C "$checkout" merge-base --is-ancestor "$merge" "$production" 2>/dev/null ||
    fail compare "production runs $ISSUE's merge" "production runs $production, which does not carry $ISSUE's merge commit $merge"
  pass compare "production runs $production, which carries $ISSUE's merge commit $merge"
  if [[ -n "${RECORD_FILE:-}" ]]; then
    [[ -r "$RECORD_FILE" ]] || {
      printf 'RECORD_FILE names %s, which cannot be read\n' "$RECORD_FILE" >&2
      exit 2
    }
    cp "$RECORD_FILE" "$record_file"
  else
    read_to compare "read the record's artifact $RECORD_ARTIFACT on $ISSUE" \
      "/api/v1/issues/$(q "$ISSUE")/artifacts/$(q "$RECORD_ARTIFACT")" "$work/artifact.json"
    found="$(jq -r '[.versions[].number] | max // empty' "$work/artifact.json")"
    [[ -n "$found" ]] || fail compare "read the record's newest version" "the artifact holds no version: $(body)"
    read_to compare "read version $found of the record" \
      "/api/v1/issues/$(q "$ISSUE")/artifacts/$(q "$RECORD_ARTIFACT")/versions/$found" "$record_file"
  fi
  jq -e '.commit and .histories and .steps and .plans' "$record_file" >/dev/null 2>&1 ||
    fail compare "read the record" "it is no record of this driver's"
  pass compare "read the record: $(record_counts "$record_file")"
  : >"$work/differences"
  while IFS= read -r id; do
    read_to compare "read the history of the recorded priority $id" "/api/v1/priorities/$(q "$id")/history" "$work/now.json"
    history_differences "$record_file" "$id" "$work/now.json" >>"$work/differences"
    count=$((count + 1))
  done < <(recorded_priorities "$record_file")
  while IFS= read -r key; do
    read_to compare "read the steps of the recorded issue $key" "/api/v1/issues/$(q "$key")/steps" "$work/now.json"
    steps_differences "$record_file" "$key" "$work/now.json" >>"$work/differences"
  done < <(recorded_issues "$record_file")
  while IFS= read -r project; do
    read_to compare "read the plans of $project" "/api/v1/projects/$(q "$project")/plans" "$work/now.json"
    plans_differences "$record_file" "$project" "$work/now.json" >>"$work/differences"
  done < <(recorded_projects "$record_file")
  differences="$(grep -c . "$work/differences" || true)"
  if [[ "$differences" -gt 0 ]]; then
    cat "$work/differences" >&2
    fail compare "every recorded history entry, run and plan reads back after the deploy" "$differences differences, each above"
  fi
  pass compare "every recorded history entry of $count priorities, every recorded run and every recorded plan reads back after the deploy"
}

main() {
  require_env DISPATCH_URL ISSUE RECORD_ARTIFACT MODE
  require_commands curl git jq
  local token_file="${DISPATCH_TOKEN_FILE:-}"
  case "$MODE" in
    record) require_commands dispatch ;;
    compare) [[ -n "${MERGE_COMMIT:-}" ]] || require_commands gh ;;
    *)
      printf 'MODE is %s, and it is record or compare\n' "$MODE" >&2
      usage
      exit 2
      ;;
  esac
  read_bearer
  checkout="$(git -C "$(dirname -- "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)" || {
    printf 'cannot find the checkout %s is part of\n' "${BASH_SOURCE[0]}" >&2
    exit 2
  }
  base="${DISPATCH_URL%/}"
  work_dir
  trap 'rm -rf "$work"' EXIT
  printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"
  if [[ "$MODE" == record ]]; then
    record "$token_file"
  else
    compare
  fi
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
