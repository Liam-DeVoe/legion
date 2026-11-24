#!/usr/bin/env bash
# Tests for `check-workflow-files.sh`: every file under .github/workflows is within 64,000 bytes,
# parses as YAML, repeats no mapping key after upper-casing, calls at most 10 unique reusable
# workflows, nested calls included, reads less than 10,000 bytes of filter text and sets
# JOB_TIMEOUT_MINUTES and STEP_TIMEOUT_MINUTES to the timeout-minutes they stand for, and ci.yaml
# routes at most 30 jobs besides the carriers of a split job's old name; and a refusal of size
# tells the author where to move text, never into a filter file.
#
# Each case builds a miniature repository under a temporary directory and symlinks the real
# script into it, so the script under test is the file CI runs.
#
# Run from anywhere: .github/scripts/check-workflow-files.test.sh
# CI runs it in ci.yaml's scripts job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
check_script="$script_dir/check-workflow-files.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

# fixture <name>: an empty tree with the script linked in. Echoes its root.
fixture() {
  local root="$work/$1"
  if [ -e "$root" ]; then
    echo "  FAIL: the fixture name '$1' is already taken by an earlier case" >&2
    return 1
  fi
  mkdir -p "$root/.github/scripts" "$root/.github/workflows"
  ln -s "$check_script" "$root/.github/scripts/check-workflow-files.sh"
  echo "$root"
}

run_check() {
  local root=$1
  set +e
  out=$(cd "$root" && "./.github/scripts/check-workflow-files.sh" 2>&1)
  status=$?
  set -e
}

# errors <text>: how many annotations the run printed.
errors() { grep -c '^::error file=' <<<"$1" || true; }

# workflow <file>: the smallest workflow every case starts from.
workflow() {
  cat > "$1" <<'YAML'
name: Build
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo build
YAML
}

# reusable <file>: a workflow another one may call.
reusable() {
  cat > "$1" <<'YAML'
name: Reusable
on: workflow_call
jobs:
  work:
    runs-on: ubuntu-latest
    steps:
      - run: echo work
YAML
}

# caller <file> <count> <first>: a workflow whose <count> jobs each call one local reusable
# workflow, numbered from <first>.
caller() {
  local file=$1 count=$2 first=$3 index name
  printf 'name: Caller\non: push\njobs:\n' > "$file"
  for ((index = 0; index < count; index++)); do
    name=$(printf 'w%02d' "$((first + index))")
    printf '  call-%s:\n    uses: ./.github/workflows/%s.yaml\n' "$name" "$name" >> "$file"
  done
}

# callees <dir> <count> <first>: the reusable workflows a caller of those numbers calls.
callees() {
  local dir=$1 count=$2 first=$3 index name
  for ((index = 0; index < count; index++)); do
    name=$(printf 'w%02d' "$((first + index))")
    reusable "$dir/$name.yaml"
  done
}

# pad_to <file> <bytes>: append one comment line so the file is exactly <bytes> long.
pad_to() {
  local file=$1 want=$2 have gap
  have=$(wc -c < "$file")
  gap=$((want - have))
  if [ "$gap" -lt 2 ]; then
    echo "  FAIL: $file is already $have bytes, which is too long to pad to $want" >&2
    return 1
  fi
  printf '#%*s\n' "$((gap - 2))" '' | tr ' ' 'x' >> "$file"
}

echo "case: a workflow file one byte above 64,000"
root=$(fixture over-limit)
workflow "$root/.github/workflows/ci.yaml"
pad_to "$root/.github/workflows/ci.yaml" 64001
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the file and its bytes" \
  "$(contains "$out" '^::error file=.github/workflows/ci.yaml::.github/workflows/ci.yaml is 64,001 bytes, above this check.s limit of 64,000 bytes')"
check "names GitHub's cap too" "$(contains "$out" 'its own cap of 500,000 bytes (500 KB)')"
check "names the file the rule is in" \
  "$(contains "$out" 'into the workflow.s notes file, as \.github/ci-notes/README\.md says\.$')"
check "tells the author to move no filter data anywhere" "$(is "$(contains "$out" 'ci-filters')" false)"
check "counts the problem" \
  "$(contains "$out" '^1 problem(s) across the 1 workflow file(s) under .github/workflows$')"

echo "case: a workflow file one byte above GitHub's own cap of 500 KB"
root=$(fixture over-cap)
workflow "$root/.github/workflows/ci.yaml"
pad_to "$root/.github/workflows/ci.yaml" 500001
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names GitHub's cap and what it does to the run" \
  "$(contains "$out" 'is 500,001 bytes, above GitHub.s own cap of 500,000 bytes (500 KB): GitHub starts no run at all')"
check "says it is above this check's limit as well" \
  "$(contains "$out" "above this check's limit of 64,000 bytes as well")"
check "names the file the rule is in" \
  "$(contains "$out" 'into the workflow.s notes file, as \.github/ci-notes/README\.md says\.$')"
check "the file both refusals name exists" \
  "$(is "$([ -f "$script_dir/../ci-notes/README.md" ] && echo true || echo false)" true)"
check "reports it once, not twice" "$(is "$(errors "$out")" 1)"

echo "case: a workflow file holding one key twice"
root=$(fixture repeated-key)
cat > "$root/.github/workflows/ci.yaml" <<'YAML'
name: Build
on: push
name: Build again
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo build
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names both keys and the line of the second" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=3::.github/workflows/ci.yaml:3: the mapping key 'name' is the key 'name' of line 1 again after upper-casing")"
check "says why GitHub refuses it" \
  "$(contains "$out" 'compares keys without regard to case')"
check "names this check's limit and GitHub's cap, with the file's bytes" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=3::.* The file is [0-9,]* bytes, against this check.s limit of 64,000 bytes and GitHub.s cap of 500,000 bytes (500 KB), above which GitHub starts no run at all\.$")"

echo "case: an env mapping holding http_proxy and HTTP_PROXY"
root=$(fixture folded-env)
cat > "$root/.github/workflows/ci.yaml" <<'YAML'
name: Build
on: push
env:
  http_proxy: http://proxy.invalid:3128
  HTTP_PROXY: http://proxy.invalid:3128
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo build
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names both keys and the line of the second" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=5::.github/workflows/ci.yaml:5: the mapping key 'HTTP_PROXY' is the key 'http_proxy' of line 4 again")"

echo "case: a mapping an alias repeats, holding http_proxy and HTTP_PROXY, is reported once"
root=$(fixture folded-aliased)
cat > "$root/.github/workflows/ci.yaml" <<'YAML'
name: Build
on: push
env: &proxy
  http_proxy: http://proxy.invalid:3128
  HTTP_PROXY: http://proxy.invalid:3128
jobs:
  build:
    runs-on: ubuntu-latest
    env: *proxy
    steps:
      - run: echo build
  test:
    runs-on: ubuntu-latest
    env: *proxy
    steps:
      - run: echo test
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the anchored mapping's second key" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=5::.github/workflows/ci.yaml:5: the mapping key 'HTTP_PROXY' is the key 'http_proxy' of line 4 again")"
check "reports it once, not once per alias" "$(is "$(errors "$out")" 1)"

echo "case: a key holding a newline and a workflow command, and its upper-case twin"
# The message repeats both keys, and the runner reads each line of a step's output as a command
# of its own: unescaped, the newline in each key would start a ::warning the file never asked for.
root=$(fixture forged-command)
cat > "$root/.github/workflows/ci.yaml" <<'YAML'
name: Build
on: push
env:
  "a\n::warning title=forged::injected": one
  "A\n::WARNING TITLE=FORGED::INJECTED": two
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo build
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names both keys and the line of the second, each newline escaped" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=5::.github/workflows/ci.yaml:5: the mapping key 'A%0A::WARNING TITLE=FORGED::INJECTED' is the key 'a%0A::warning title=forged::injected' of line 4 again")"
check "starts no line with ::warning, in either case" "$(is "$(grep -ci '^::warning' <<<"$out" || true)" 0)"
check "reports it once" "$(is "$(errors "$out")" 1)"

echo "case: a file name holding a comma and a colon, and a key holding the legacy ##[ command"
# The runner ends a command's properties at the first `::` and splits them on `,`, so the file
# name is escaped as a property; and it recognises `##[` anywhere in a line, so the message's keys
# are escaped as ci_layout.py escapes an annotation's message.
root=$(fixture escaped-property)
cat > "$root/.github/workflows/a,b:c.yaml" <<'YAML'
name: Build
on: push
env:
  "x##[group]": one
  "X##[GROUP]": two
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo build
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "escapes the file property's comma and colon, and each key's ##[" \
  "$(contains "$out" "^::error file=.github/workflows/a%2Cb%3Ac.yaml,line=5::.github/workflows/a,b:c.yaml:5: the mapping key 'X%23%23%5BGROUP]' is the key 'x%23%23%5Bgroup]' of line 4 again")"
check "leaves no ##[ in its output" "$(is "$(grep -c '##\[' <<<"$out" || true)" 0)"
check "reports it once" "$(is "$(errors "$out")" 1)"

echo "case: a mapping holding an alias of itself ends, and passes"
# The alias makes the composed nodes a cycle, which the key walk would follow for ever if it did
# not visit each node once. The file is valid YAML with no folded key and no call, so it passes.
root=$(fixture recursive-alias)
cat > "$root/.github/workflows/ci.yaml" <<'YAML'
name: Build
on: push
jobs: &jobs
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo build
  again: *jobs
YAML
set +e
out=$(cd "$root" && timeout 60 "./.github/scripts/check-workflow-files.sh" 2>&1)
status=$?
set -e
check "exits 0 within 60 seconds" "$(is "$status" 0)"
check "says what it read" \
  "$(contains "$out" '^all 1 workflow files under .github/workflows are within 64,000 bytes')"

echo "case: a workflow file that is not valid YAML"
root=$(fixture bad-yaml)
printf 'name: "CI\non: push\n' > "$root/.github/workflows/ci.yaml"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the file and the parser's message" \
  "$(contains "$out" '^::error file=.github/workflows/ci.yaml::.github/workflows/ci.yaml is not valid YAML: ')"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
check "names this check's limit and GitHub's cap, with the file's bytes" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml::.* The file is [0-9,]* bytes, against this check.s limit of 64,000 bytes and GitHub.s cap of 500,000 bytes (500 KB), above which GitHub starts no run at all\.$")"

echo "case: two files at fault at once, one of each class"
root=$(fixture two-faults)
workflow "$root/.github/workflows/big.yaml"
pad_to "$root/.github/workflows/big.yaml" 64001
cat > "$root/.github/workflows/folded.yaml" <<'YAML'
name: Build
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    env:
      path: /usr/bin
      PATH: /usr/bin
    steps:
      - run: echo build
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the file above the limit" \
  "$(contains "$out" '^::error file=.github/workflows/big.yaml::.github/workflows/big.yaml is 64,001 bytes')"
check "and the file with the folded keys, in the same run" \
  "$(contains "$out" "^::error file=.github/workflows/folded.yaml,line=8::.github/workflows/folded.yaml:8: the mapping key 'PATH' is the key 'path' of line 7 again")"
check "counts both" \
  "$(contains "$out" '^2 problem(s) across the 2 workflow file(s) under .github/workflows$')"

echo "case: exactly 64,000 bytes and exactly 10 unique reusable workflows passes"
root=$(fixture at-both-limits)
caller "$root/.github/workflows/ci.yaml" 10 1
callees "$root/.github/workflows" 10 1
pad_to "$root/.github/workflows/ci.yaml" 64000
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "says what it read" \
  "$(contains "$out" '^all 11 workflow files under .github/workflows are within 64,000 bytes')"
check "and what it held them to" \
  "$(contains "$out" 'call at most 10 unique reusable workflows, read less than 10,000 bytes of filter text and set JOB_TIMEOUT_MINUTES and STEP_TIMEOUT_MINUTES to the timeout-minutes they stand for, and .github/workflows/ci.yaml routes at most 32 jobs besides its carriers$')"

echo "case: job ids and with: keys that differ only in case"
root=$(fixture folded-elsewhere)
cat > "$root/.github/workflows/jobs.yaml" <<'YAML'
name: Build
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo one
  Build:
    runs-on: ubuntu-latest
    steps:
      - run: echo two
YAML
reusable "$root/.github/workflows/called.yaml"
cat > "$root/.github/workflows/with.yaml" <<'YAML'
name: Caller
on: push
jobs:
  call:
    uses: ./.github/workflows/called.yaml
    with:
      token: one
      Token: two
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the two job ids" \
  "$(contains "$out" "^::error file=.github/workflows/jobs.yaml,line=8::.github/workflows/jobs.yaml:8: the mapping key 'Build' is the key 'build' of line 4 again")"
check "and the two with: keys" \
  "$(contains "$out" "^::error file=.github/workflows/with.yaml,line=8::.github/workflows/with.yaml:8: the mapping key 'Token' is the key 'token' of line 7 again")"

echo "case: a file calling 11 unique reusable workflows"
root=$(fixture over-calls)
caller "$root/.github/workflows/ci.yaml" 11 1
callees "$root/.github/workflows" 11 1
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the count and the limit" \
  "$(contains "$out" '^::error file=.github/workflows/ci.yaml::.github/workflows/ci.yaml calls 11 unique reusable workflows, above this check.s limit of 10:')"
check "lists the callees" "$(contains "$out" '.github/workflows/w11.yaml')"
check "names GitHub's own cap of 50" \
  "$(contains "$out" 'GitHub allows 50 from a single workflow file, counting every tree of nested calls')"
check "names this check's limit and GitHub's cap, with the file's bytes" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml::.* The file is [0-9,]* bytes, against this check.s limit of 64,000 bytes and GitHub.s cap of 500,000 bytes (500 KB), above which GitHub starts no run at all\.$")"

echo "case: 9 calls, one of which calls two more, is a tree of 11"
root=$(fixture nested-calls)
caller "$root/.github/workflows/ci.yaml" 9 1
callees "$root/.github/workflows" 9 1
caller "$root/.github/workflows/w01.yaml" 2 10
callees "$root/.github/workflows" 2 10
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts the whole tree" \
  "$(contains "$out" '^::error file=.github/workflows/ci.yaml::.github/workflows/ci.yaml calls 11 unique reusable workflows')"
check "names a workflow only the nested call reaches" \
  "$(contains "$out" '.github/workflows/w11.yaml')"
check "the nested caller itself, which calls two, passes" \
  "$(is "$(contains "$out" 'file=.github/workflows/w01.yaml')" false)"

echo "case: a .yml file is covered, and a file of that directory that is neither is passed over"
root=$(fixture suffixes)
workflow "$root/.github/workflows/ci.yml"
pad_to "$root/.github/workflows/ci.yml" 64001
printf 'The workflows of this repository live here.\n' > "$root/.github/workflows/README.md"
pad_to "$root/.github/workflows/README.md" 70000
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the .yml file" \
  "$(contains "$out" '^::error file=.github/workflows/ci.yml::.github/workflows/ci.yml is 64,001 bytes')"
check "says nothing of the README beside the workflows" \
  "$(is "$(contains "$out" 'workflows/README\.md')" false)"
check "names .github/ci-notes/README.md, where the rule is" \
  "$(contains "$out" '^::error file=.github/workflows/ci.yml::.* into the workflow.s notes file, as \.github/ci-notes/README\.md says\.$')"
check "read one file" \
  "$(contains "$out" '^1 problem(s) across the 1 workflow file(s) under .github/workflows$')"

# router <file> <jobs> [carriers] [extra]: a ci.yaml of <jobs> jobs that run a step and
# [carriers] carriers - a sparse checkout of .github, one step and the report to ci-ok - the
# first of them with a fourth step when [extra] is given.
router() {
  local file=$1 jobs=$2 carriers=${3:-0} extra=${4:-} index
  printf 'name: CI\non: pull_request\njobs:\n' >"$file"
  for ((index = 1; index <= jobs; index++)); do
    printf '  job%02d:\n    runs-on: ubuntu-24.04\n    steps:\n      - run: echo test\n' "$index" >>"$file"
  done
  for ((index = 1; index <= carriers; index++)); do
    printf '  carrier%02d:\n    runs-on: ubuntu-24.04\n    steps:\n' "$index" >>"$file"
    printf '      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1\n' >>"$file"
    printf '        with: {sparse-checkout: .github, persist-credentials: false}\n' >>"$file"
    printf '      - run: .github/scripts/leg-verdict.sh one two\n' >>"$file"
    if [ "$index" = 1 ] && [ -n "$extra" ]; then printf '      - run: echo a fourth step\n' >>"$file"; fi
    printf '      - {if: always(), uses: ./.github/actions/ci-ok-report}\n' >>"$file"
  done
}

echo "case: 32 jobs and two carriers pass"
root=$(fixture at-job-limit)
router "$root/.github/workflows/ci.yaml" 32 2
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a thirty-third job that runs a test"
root=$(fixture over-job-limit)
router "$root/.github/workflows/ci.yaml" 33
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the count and the limit" \
  "$(contains "$out" '^::error file=.github/workflows/ci.yaml::.github/workflows/ci.yaml routes 33 jobs besides its carriers, above the limit of 32:')"

echo "case: a carrier that runs a step besides its three is a job"
root=$(fixture carrier-extra-step)
router "$root/.github/workflows/ci.yaml" 32 2 extra
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts the carrier with a fourth step" \
  "$(contains "$out" 'routes 33 jobs besides its carriers, above the limit of 32: .*carrier01')"
check "and not the carrier of three steps" "$(is "$(contains "$out" 'carrier02')" false)"

# filters <file> <bytes>: a workflow whose paths-filter step reads .github/areas.yaml, padded to
# <bytes>.
filters() {
  local root=$1 bytes=$2
  printf 'name: CI\non: pull_request\njobs:\n  changes:\n    runs-on: ubuntu-24.04\n    steps:\n' >"$root/.github/workflows/ci.yaml"
  printf '      - uses: dorny/paths-filter@ceb8a2b8f2d89434be7ff52d3de7ec3738c5cc9d\n        with:\n          filters: .github/areas.yaml\n' \
    >>"$root/.github/workflows/ci.yaml"
  printf 'docs:\n  - docs/**\n' >"$root/.github/areas.yaml"
  pad_to "$root/.github/areas.yaml" "$bytes"
}

echo "case: 9,999 bytes of filter text pass"
root=$(fixture filters-under)
filters "$root" 9999
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: 10,000 bytes of filter text"
root=$(fixture filters-at)
filters "$root" 10000
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step, the file and the limit" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml::.github/workflows/ci.yaml: job changes's paths-filter step reads 10,000 bytes of filter text from .github/areas.yaml, at or above the limit of 10,000")"

echo "case: JOB_TIMEOUT_MINUTES that is its job's timeout-minutes passes, at job and at step level"
# gh-read.sh waits for a spent API budget within JOB_TIMEOUT_MINUTES, measured from the job's
# start: a value above the job's timeout-minutes would let the wait outlive the job.
root=$(fixture timeout-equal)
cat > "$root/.github/workflows/ci.yaml" <<'YAML'
name: Build
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    env:
      JOB_TIMEOUT_MINUTES: 10
    steps:
      - run: echo build
        timeout-minutes: 3
        env:
          JOB_TIMEOUT_MINUTES: 10
          STEP_TIMEOUT_MINUTES: 3
  plain:
    runs-on: ubuntu-latest
    env:
      JOB_TIMEOUT_MINUTES: 360
    steps:
      - run: echo plain
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "says it holds the two equal" "$(contains "$out" 'JOB_TIMEOUT_MINUTES and STEP_TIMEOUT_MINUTES')"

echo "case: JOB_TIMEOUT_MINUTES that is not its job's timeout-minutes is refused, by file, line and both values"
root=$(fixture timeout-differs)
cat > "$root/.github/workflows/ci.yaml" <<'YAML'
name: Build
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    env:
      JOB_TIMEOUT_MINUTES: 30
    steps:
      - run: echo build
        env:
          JOB_TIMEOUT_MINUTES: 12
  plain:
    runs-on: ubuntu-latest
    env:
      JOB_TIMEOUT_MINUTES: 30
    steps:
      - run: echo plain
        timeout-minutes: 4
        env:
          STEP_TIMEOUT_MINUTES: 5
      - run: echo bare
        env:
          STEP_TIMEOUT_MINUTES: 2
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the job-level value, its line and the job's limit with its line" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=8::.github/workflows/ci.yaml:8: job build sets JOB_TIMEOUT_MINUTES to 30, but its timeout-minutes is 10 (line 6)")"
check "names a step-level value too" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=12::.github/workflows/ci.yaml:12: job build sets JOB_TIMEOUT_MINUTES to 12, but its timeout-minutes is 10 (line 6)")"
check "reads a job with no timeout-minutes as GitHub's default of 360" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=16::.github/workflows/ci.yaml:16: job plain sets JOB_TIMEOUT_MINUTES to 30, but its timeout-minutes is 360 (GitHub's default, as the job sets none)")"
check "names a step's STEP_TIMEOUT_MINUTES against that step's timeout-minutes" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=21::.github/workflows/ci.yaml:21: step 1 of job plain sets STEP_TIMEOUT_MINUTES to 5, but its timeout-minutes is 4 (line 19)")"
check "and one whose step sets no timeout-minutes at all" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=24::.github/workflows/ci.yaml:24: step 2 of job plain sets STEP_TIMEOUT_MINUTES to 2, but the step sets no timeout-minutes")"
check "says why the two must be one number" "$(contains "$out" "gh-read.sh waits for a spent API budget within")"
check "refuses those five and nothing else" "$(contains "$out" '^5 problem(s) across the 1 workflow file(s) under .github/workflows$')"

echo "case: a misplaced limit name, and a step with its own limit that reaches gh-read.sh without passing it, are refused"
# gh-read.sh, a script that sources it, a script that only names it in a comment, and an action
# that runs the first script: the reach follows a run or a source, never a mention.
root=$(fixture timeout-reach)
printf '%s\n' 'gh_wait_reset() { "$@"; }' > "$root/.github/scripts/gh-read.sh"
printf '%s\n' '#!/usr/bin/env bash' 'source "$(dirname "$0")/gh-read.sh"' 'gh_wait_reset gh api x' \
  > "$root/.github/scripts/reader.sh"
printf '%s\n' '#!/usr/bin/env bash' '# reader.sh, beside this, reads the API; this script does not.' 'echo quiet' \
  > "$root/.github/scripts/mention.sh"
mkdir -p "$root/.github/actions/report"
cat > "$root/.github/actions/report/action.yml" <<'YAML'
name: report
description: Runs .github/scripts/reader.sh, in its description too.
runs:
  using: composite
  steps:
    - shell: bash
      run: .github/scripts/reader.sh
YAML
cat > "$root/.github/workflows/ci.yaml" <<'YAML'
name: Build
on: push
env:
  JOB_TIMEOUT_MINUTES: 10
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    env:
      STEP_TIMEOUT_MINUTES: 3
    steps:
      - run: .github/scripts/reader.sh
        timeout-minutes: 3
      - uses: ./.github/actions/report
        timeout-minutes: 4
      - run: .github/scripts/mention.sh
        timeout-minutes: 5
      - run: echo .github/scripts/reader.sh is a script
        timeout-minutes: 6
      - run: .github/scripts/reader.sh
        timeout-minutes: 7
        env:
          STEP_TIMEOUT_MINUTES: 7
      - run: .github/scripts/reader.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the workflow-level JOB_TIMEOUT_MINUTES" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=4::.github/workflows/ci.yaml:4: the workflow's env sets JOB_TIMEOUT_MINUTES, which stands for no one job's timeout-minutes")"
check "names a job-level STEP_TIMEOUT_MINUTES" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=10::.github/workflows/ci.yaml:10: job build's env sets STEP_TIMEOUT_MINUTES, which stands for no one step's timeout-minutes")"
check "names the step that runs the reader with a limit and no STEP_TIMEOUT_MINUTES" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=13::.github/workflows/ci.yaml:13: step 1 of job build sets timeout-minutes 3 and reaches gh-read.sh (through reader.sh), but sets no STEP_TIMEOUT_MINUTES")"
check "and the step that uses the action that runs it" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=15::.github/workflows/ci.yaml:15: step 2 of job build sets timeout-minutes 4 and reaches gh-read.sh (through ./.github/actions/report), but sets no STEP_TIMEOUT_MINUTES")"
check "not the step whose script only mentions the reader" "$(is "$(contains "$out" 'step 3 of job build')" false)"
check "nor the step that only echoes its path" "$(is "$(contains "$out" 'step 4 of job build')" false)"
check "nor the one that passes its limit, nor the one with no limit of its own" \
  "$(is "$(contains "$out" 'step [56] of job build')" false)"
check "names the router's job that reaches gh-read.sh and sets no JOB_TIMEOUT_MINUTES" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=6::.github/workflows/ci.yaml:6: job build reaches gh-read.sh (through a run: step) and its env sets no JOB_TIMEOUT_MINUTES")"
check "refuses those five and nothing else" "$(contains "$out" '^5 problem(s) across the 1 workflow file(s) under .github/workflows$')"

echo "case: STEP_TIMEOUT_MINUTES in a workflow's env, and steps reaching gh-read.sh through ./base/, bash -c, \${{ github.workspace }} or a bare source, are refused"
root=$(fixture timeout-reach-ways)
printf '%s\n' 'gh_wait_reset() { "$@"; }' > "$root/.github/scripts/gh-read.sh"
printf '%s\n' '#!/usr/bin/env bash' 'source "$(dirname "$0")/gh-read.sh"' 'gh_wait_reset gh api x' \
  > "$root/.github/scripts/reader.sh"
printf '%s\n' '#!/usr/bin/env bash' 'cd "$(dirname "$0")"' 'source gh-read.sh' 'gh_wait_reset gh api x' \
  > "$root/.github/scripts/bare.sh"
mkdir -p "$root/.github/actions/report"
printf '%s\n' 'name: report' 'description: d' 'runs:' '  using: composite' '  steps:' '    - shell: bash' \
  '      run: .github/scripts/reader.sh' > "$root/.github/actions/report/action.yml"
cat > "$root/.github/workflows/ci.yaml" <<'YAML'
name: Build
on: push
env:
  STEP_TIMEOUT_MINUTES: 3
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    steps:
      - uses: ./base/.github/actions/report
        timeout-minutes: 3
      - run: bash -c '.github/scripts/reader.sh'
        timeout-minutes: 4
      - run: '"${{ github.workspace }}/.github/scripts/reader.sh"'
        timeout-minutes: 5
      - run: .github/scripts/bare.sh
        timeout-minutes: 6
      - run: bash -c 'echo .github/scripts/reader.sh'
        timeout-minutes: 7
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the workflow-level STEP_TIMEOUT_MINUTES" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml,line=4::.github/workflows/ci.yaml:4: the workflow's env sets STEP_TIMEOUT_MINUTES, which stands for no one step's timeout-minutes")"
check "names the step that uses the action from ./base/" \
  "$(contains "$out" "line=11::.github/workflows/ci.yaml:11: step 1 of job build sets timeout-minutes 3 and reaches gh-read.sh (through ./base/.github/actions/report)")"
check "names the step that runs the reader through bash -c" \
  "$(contains "$out" "line=13::.github/workflows/ci.yaml:13: step 2 of job build sets timeout-minutes 4 and reaches gh-read.sh (through reader.sh)")"
check "names the step that runs it from \${{ github.workspace }}" \
  "$(contains "$out" "line=15::.github/workflows/ci.yaml:15: step 3 of job build sets timeout-minutes 5 and reaches gh-read.sh (through reader.sh)")"
check "names the step whose script sources gh-read.sh by its bare name" \
  "$(contains "$out" "line=17::.github/workflows/ci.yaml:17: step 4 of job build sets timeout-minutes 6 and reaches gh-read.sh (through bare.sh)")"
check "not the step whose bash -c only echoes the path" "$(is "$(contains "$out" 'step 5 of job build')" false)"
check "names the job that sets no JOB_TIMEOUT_MINUTES" "$(contains "$out" 'job build reaches gh-read.sh (through ./base/.github/actions/report) and its env sets no JOB_TIMEOUT_MINUTES')"
check "refuses those six and nothing else" "$(contains "$out" '^6 problem(s) across the 1 workflow file(s) under .github/workflows$')"

echo "case: a leg's JOB_TIMEOUT_MINUTES is its timeout-minutes expression as written"
root=$(fixture timeout-legs)
printf '%s\n' 'gh_wait_reset() { "$@"; }' > "$root/.github/scripts/gh-read.sh"
printf '%s\n' '#!/usr/bin/env bash' 'source "$(dirname "$0")/gh-read.sh"' 'gh_wait_reset gh api x' \
  > "$root/.github/scripts/reader.sh"
cat > "$root/.github/workflows/ci.yaml" <<'YAML'
name: Build
on: push
jobs:
  legs:
    runs-on: ubuntu-latest
    timeout-minutes: ${{ matrix.timeout }}
    env: {JOB_TIMEOUT_MINUTES: '${{ matrix.timeout }}'}
    strategy: {fail-fast: false, matrix: {include: [{timeout: 10}, {timeout: 20}]}}
    steps:
      - run: .github/scripts/reader.sh
  other:
    runs-on: ubuntu-latest
    timeout-minutes: ${{ matrix.timeout || 10 }}
    env: {JOB_TIMEOUT_MINUTES: '${{ matrix.timeout }}'}
    strategy: {fail-fast: false, matrix: {include: [{timeout: 20}]}}
    steps:
      - run: .github/scripts/reader.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the leg whose value is another expression" \
  "$(contains "$out" "line=14::.github/workflows/ci.yaml:14: job other sets JOB_TIMEOUT_MINUTES to \${{ matrix.timeout }}, but its timeout-minutes is \${{ matrix.timeout || 10 }}")"
check "and not the legs whose value is their expression" "$(is "$(contains "$out" 'job legs')" false)"
check "refuses that one alone" "$(contains "$out" '^1 problem(s) across the 1 workflow file(s) under .github/workflows$')"

echo "case: a run: body inside an action that reaches gh-read.sh under a timeout command passes its bound"
root=$(fixture timeout-action-bodies)
printf '%s\n' 'gh_wait_reset() { "$@"; }' > "$root/.github/scripts/gh-read.sh"
printf '%s\n' '#!/usr/bin/env bash' 'source "$(dirname "$0")/gh-read.sh"' 'gh_wait_reset gh api x' \
  > "$root/.github/scripts/reader.sh"
mkdir -p "$root/.github/actions/timed/inner"
cat > "$root/.github/actions/timed/inner/action.yml" <<'YAML'
name: timed
description: d
runs:
  using: composite
  steps:
    - shell: bash
      env: {STEP_TIMEOUT_MINUTES: 5}
      run: timeout 5m .github/scripts/reader.sh
    - shell: bash
      run: timeout 6m .github/scripts/reader.sh
    - shell: bash
      env: {STEP_TIMEOUT_MINUTES: 8}
      run: timeout 7m .github/scripts/reader.sh
    - shell: bash
      env: {STEP_TIMEOUT_MINUTES: 9}
      run: .github/scripts/reader.sh
    - shell: bash
      run: timeout 4m echo .github/scripts/reader.sh is a script
YAML
printf 'name: Build\non: push\njobs: {}\n' > "$root/.github/workflows/ci.yaml"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the body with no STEP_TIMEOUT_MINUTES" \
  "$(contains "$out" "step 2 reaches gh-read.sh under \`timeout 6m\`, its own bound, and sets STEP_TIMEOUT_MINUTES to nothing")"
check "names the body whose STEP_TIMEOUT_MINUTES is another number" \
  "$(contains "$out" "step 3 reaches gh-read.sh under \`timeout 7m\`, its own bound, and sets STEP_TIMEOUT_MINUTES to 8")"
check "names the body that sets one with no timeout command" \
  "$(contains "$out" "step 4 sets STEP_TIMEOUT_MINUTES to 9, but its run: carries no \`timeout <N>m\` command")"
check "and not the body that passes its bound, nor the one that only echoes the path" \
  "$(is "$(contains "$out" 'step [15] ')" false)"
check "refuses those three and nothing else" "$(contains "$out" '^3 problem(s) across the 1 workflow file(s) under .github/workflows$')"

summary "check-workflow-files.sh"
