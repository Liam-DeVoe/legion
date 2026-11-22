#!/usr/bin/env bash
# Tests for `check-workflow-files.sh`: every file under .github/workflows is within 64,000 bytes,
# parses as YAML, repeats no mapping key after upper-casing, calls at most 45 unique reusable
# workflows, and sets JOB_TIMEOUT_MINUTES and STEP_TIMEOUT_MINUTES to the timeout-minutes they
# stand for.
#
# Each case builds a miniature repository under a temporary directory and symlinks the real
# script into it, so the script under test is the file CI runs.
#
# Run from anywhere: .github/scripts/check-workflow-files.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
check_script="$script_dir/check-workflow-files.sh"
repo=$(cd "$script_dir/../.." && pwd)
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
# are escaped as ci_filter_coverage.py escapes an annotation's message.
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

echo "case: exactly 64,000 bytes and exactly 45 unique reusable workflows passes"
root=$(fixture at-both-limits)
caller "$root/.github/workflows/ci.yaml" 45 1
callees "$root/.github/workflows" 45 1
pad_to "$root/.github/workflows/ci.yaml" 64000
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "says what it read" \
  "$(contains "$out" '^all 46 workflow files under .github/workflows are within 64,000 bytes')"
check "and what it held them to" \
  "$(contains "$out" 'call at most 45 unique reusable workflows, set JOB_TIMEOUT_MINUTES and STEP_TIMEOUT_MINUTES to the timeout-minutes they stand for, and keep the events of 0 go test command(s) in 0 step(s) of 0 job(s) of ci.yaml.s run$')"

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

echo "case: a file calling 46 unique reusable workflows"
root=$(fixture over-calls)
caller "$root/.github/workflows/ci.yaml" 46 1
callees "$root/.github/workflows" 46 1
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the count and the limit" \
  "$(contains "$out" '^::error file=.github/workflows/ci.yaml::.github/workflows/ci.yaml calls 46 unique reusable workflows, above this check.s limit of 45:')"
check "lists the callees" "$(contains "$out" '.github/workflows/w46.yaml')"
check "names GitHub's own cap of 50" \
  "$(contains "$out" 'GitHub allows 50 from a single workflow file, counting every tree of nested calls')"
check "names this check's limit and GitHub's cap, with the file's bytes" \
  "$(contains "$out" "^::error file=.github/workflows/ci.yaml::.* The file is [0-9,]* bytes, against this check.s limit of 64,000 bytes and GitHub.s cap of 500,000 bytes (500 KB), above which GitHub starts no run at all\.$")"

echo "case: 44 calls, one of which calls two more, is a tree of 46"
root=$(fixture nested-calls)
caller "$root/.github/workflows/ci.yaml" 44 1
callees "$root/.github/workflows" 44 1
caller "$root/.github/workflows/w01.yaml" 2 45
callees "$root/.github/workflows" 2 45
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts the whole tree" \
  "$(contains "$out" '^::error file=.github/workflows/ci.yaml::.github/workflows/ci.yaml calls 46 unique reusable workflows')"
check "names a workflow only the nested call reaches" \
  "$(contains "$out" '.github/workflows/w46.yaml')"
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
check "refuses those four and nothing else" "$(contains "$out" '^4 problem(s) across the 1 workflow file(s) under .github/workflows$')"

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
check "refuses those five and nothing else" "$(contains "$out" '^5 problem(s) across the 1 workflow file(s) under .github/workflows$')"

# events_router <root>: a ci.yaml whose one job calls ci-job-unit.yaml, the file each case of rule
# 7 writes its job into.
events_router() {
  cat > "$1/.github/workflows/ci.yaml" <<'YAML'
name: CI
on: pull_request
jobs:
  unit:
    uses: ./.github/workflows/ci-job-unit.yaml
YAML
}

# the upload step rule 7 asks of a job that keeps go test events.
upload_step='      - name: Upload the go test events
        if: always()
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: go-test-events-${{ job.check_run_id }}
          path: ${{ runner.temp }}/go-test-events/
          if-no-files-found: error'

# events_scripts <root>: the scripts the rule 7 cases run, in the fixture's tree: a counter that
# appends its own -json report to TEST_EVENTS_FILE, a script that runs go test bare and one that
# runs that one, a script that runs go test under the writer and one that runs that one, a script
# that runs go test only in a quoted string and a heredoc, and a fixture harness of FIXTURES' list,
# whose go test runs are its own.
events_scripts() {
  mkdir -p "$1/packages/envoy/scripts" "$1/packages/x/scripts" "$1/.github/scripts"
  cat > "$1/packages/envoy/scripts/database-skips.sh" <<'SH'
#!/bin/sh
go test -json ./... | tee -a "$TEST_EVENTS_FILE"
SH
  cat > "$1/packages/x/scripts/unit.sh" <<'SH'
#!/bin/sh
cd "$(dirname "$0")/.." && go test ./pkg
SH
  cat > "$1/packages/x/scripts/outer.sh" <<'SH'
#!/bin/sh
dir=$(dirname "$0")
bash "$dir/unit.sh"
SH
  cat > "$1/packages/x/scripts/kept.sh" <<'SH'
#!/bin/sh
exec ../../.github/scripts/go_test_events.py go test ./pkg
SH
  cat > "$1/packages/x/scripts/kept-outer.sh" <<'SH'
#!/bin/sh
dir=$(dirname "$0")
sh "$dir/kept.sh"
SH
  cat > "$1/packages/x/scripts/words.sh" <<'SH'
#!/bin/sh
echo "run go test ./pkg to see"
cat <<EOF
go test ./pkg
EOF
SH
  cat > "$1/.github/scripts/go_test_events.test.sh" <<'SH'
#!/bin/sh
go test ./fixture-module/...
SH
}

echo "case: go test steps of ci.yaml's run that keep their events, under ids, uploaded after them, pass and are counted"
root=$(fixture events-kept)
events_router "$root"
events_scripts "$root"
cat > "$root/.github/workflows/ci-job-unit.yaml" <<YAML
name: CI job unit
on: workflow_call
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - name: Unit tests
        id: unit
        run: |
          set -o pipefail
          go list ./... | grep -v /live | xargs ../../.github/scripts/go_test_events.py go test -count=1
          ../../.github/scripts/go_test_events.py go test -count=1 -v -run "\$pattern" ./cmd/x
          "../../.github/scripts/go_test_events.py" go -C mod test ./pkg
          ../../.github/scripts/go_test_events.py \\
            go test ./continued/
          echo "go test ./pkg is how to run it by hand"
          scripts/words.sh
        working-directory: packages/x
      - name: Race tests under the sampler
        id: race
        run: RACE_MEM_LIMIT_KB=1 ../../.github/scripts/go_test_events.py ../../.github/scripts/race_memory.py go test -json -race ./y/
      - name: The skip counter
        id: skips
        run: .github/scripts/go_test_events.py packages/envoy/scripts/database-skips.sh --timeout 20m
      - name: A script two levels down, under the writer as a whole
        id: outer
        if: inputs.unit == 'true'
        run: ../../.github/scripts/go_test_events.py scripts/kept-outer.sh
        working-directory: packages/x
      - name: Test the writer, a fixture harness
        run: .github/scripts/go_test_events.test.sh
      - name: Fill the build cache
        run: go test -c -o /dev/null ./...
$upload_step
YAML
# A workflow ci.yaml does not reach is not ci.yaml's run, whatever its go test steps do.
cat > "$root/.github/workflows/release.yaml" <<'YAML'
name: Release
on: push
jobs:
  smoke:
    runs-on: ubuntu-latest
    steps:
      - run: go test ./smoke/
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the seven commands, four steps and one job it holds to the rule" \
  "$(contains "$out" 'keep the events of 7 go test command(s) in 4 step(s) of 1 job(s) of ci.yaml.s run, ')"
check "and with no ci_filters filter to read says whether a change to its scripts starts the job is UNKNOWN" \
  "$(contains "$out" 'run, and whether a change to the [0-9]* script(s) rule 7 read starts the ci-filters job UNKNOWN: .github/ci-filters.yaml holds no ci_filters filter$')"

echo "case: control: a go test step that keeps no events is refused by name, however it spells or reaches go test"
root=$(fixture events-bare)
events_router "$root"
events_scripts "$root"
cat > "$root/.github/workflows/ci-job-unit.yaml" <<YAML
name: CI job unit
on: workflow_call
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - name: Unit tests
        id: unit
        run: go test -count=1 ./...
      - name: Integration tests
        id: integration
        run: |
          ../../.github/scripts/go_test_events.py go test ./a/
          go list ./... | xargs go test -count=1
      - name: The skip counter
        id: skips
        run: packages/envoy/scripts/database-skips.sh --timeout 20m
      - name: Race tests under the sampler alone
        id: race
        run: ../../.github/scripts/race_memory.py go test -json -race ./y/
      - name: A script that runs go test
        id: script
        run: scripts/unit.sh
        working-directory: packages/x
      - name: A script that runs that script
        id: outer
        run: bash scripts/outer.sh
        working-directory: packages/x
      - name: Go with a directory
        id: go-c
        run: go -C mod test ./pkg
      - name: A continued line
        id: continued
        run: |
          go \\
            test ./pkg
      - name: A test binary's own -c
        id: args-c
        run: go test ./argsflag -args -c
$upload_step
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the bare go test step, its file, job, number and line" \
  "$(contains "$out" '^::error file=.github/workflows/ci-job-unit.yaml,line=7::.github/workflows/ci-job-unit.yaml:7: step 1 of job unit (Unit tests) runs `go test -count=1 ./...` other than through .github/scripts/go_test_events.py')"
check "names the bare line of a step whose other line keeps its events" \
  "$(contains "$out" 'step 2 of job unit (Integration tests) runs `go test -count=1` other than through')"
check "names the skip counter run bare" \
  "$(contains "$out" 'step 3 of job unit (The skip counter) runs packages/envoy/scripts/database-skips.sh, which runs `go test -json ./...` (packages/envoy/scripts/database-skips.sh:2) other than through')"
check "names the sampler run outside the writer" \
  "$(contains "$out" 'step 4 of job unit (Race tests under the sampler alone) runs `../../.github/scripts/race_memory.py go test -json -race ./y/` other than through')"
check "names a script of no list, by the go test line it runs" \
  "$(contains "$out" 'step 5 of job unit (A script that runs go test) runs packages/x/scripts/unit.sh, which runs `go test ./pkg` (packages/x/scripts/unit.sh:2) other than through')"
check "follows a script into the script it runs" \
  "$(contains "$out" 'step 6 of job unit (A script that runs that script) runs packages/x/scripts/outer.sh, which runs `go test ./pkg` (packages/x/scripts/unit.sh:2) other than through')"
check "reads go -C <dir> test" "$(contains "$out" 'step 7 of job unit (Go with a directory) runs `go -C mod test ./pkg` other than through')"
check "reads a go test continued over two lines" "$(contains "$out" 'step 8 of job unit (A continued line) runs `go test ./pkg` other than through')"
check "reads a -c after -args as the binary's, not go test's compile-only flag" \
  "$(contains "$out" 'step 9 of job unit (A test binary.s own -c) runs `go test ./argsflag -args -c` other than through')"
check "says what the log keeps without it" "$(contains "$out" 'go test prints `ok <package> <time>` alone')"
check "refuses those nine and nothing else" "$(contains "$out" '^9 problem(s) across the 2 workflow file(s) under .github/workflows$')"

echo "case: a kept go test step with no id, a job whose upload could not run, a script whose events go nowhere and a step's own TEST_EVENTS_FILE are refused"
root=$(fixture events-unnamed)
events_router "$root"
events_scripts "$root"
cat > "$root/.github/workflows/ci-job-unit.yaml" <<YAML
name: CI job unit
on: workflow_call
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
$upload_step
      - name: Unit tests
        run: ../../.github/scripts/go_test_events.py go test -count=1 ./...
  other:
    runs-on: ubuntu-latest
    steps:
      - name: Other tests
        id: other
        run: ../../.github/scripts/go_test_events.py go test -count=1 ./other/
      - name: Upload on success alone
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: go-test-events-\${{ job.check_run_id }}
          path: \${{ runner.temp }}/go-test-events/
          if-no-files-found: error
  never:
    runs-on: ubuntu-latest
    steps:
      - name: Never-uploaded tests
        id: never
        run: ../../.github/scripts/go_test_events.py go test -count=1 ./never/
      - name: An upload whose condition is false
        if: always() && false
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: go-test-events-\${{ job.check_run_id }}
          path: \${{ runner.temp }}/go-test-events/
          if-no-files-found: error
  leaky:
    runs-on: ubuntu-latest
    steps:
      - name: A bare script under the writer
        id: leaky
        run: ../../.github/scripts/go_test_events.py scripts/unit.sh
        working-directory: packages/x
      - name: Elsewhere
        id: elsewhere
        env:
          TEST_EVENTS_FILE: elsewhere/events.jsonl
        run: ../../.github/scripts/go_test_events.py go test ./pkg
$upload_step
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step with no id" \
  "$(contains "$out" 'line=14::.github/workflows/ci-job-unit.yaml:14: step 2 of job unit (Unit tests) runs go test through .github/scripts/go_test_events.py and has no id: of its own')"
check "names the job whose upload comes before its go test step" \
  "$(contains "$out" 'job unit keeps go test events (its last such step is step 2, Unit tests) and no step after it uploads them')"
check "names the job whose upload is not if: always()" \
  "$(contains "$out" 'job other keeps go test events (its last such step is step 1, Other tests) and no step after it uploads them')"
check "names the job whose upload's condition holds more than always()" \
  "$(contains "$out" 'job never keeps go test events (its last such step is step 1, Never-uploaded tests) and no step after it uploads them')"
check "names the script under the writer whose go test neither runs under it nor tees into TEST_EVENTS_FILE" \
  "$(contains "$out" 'step 1 of job leaky (A bare script under the writer) runs a script under .github/scripts/go_test_events.py, and the script runs `go test ./pkg` (packages/x/scripts/unit.sh:2), which neither runs under that writer nor tees its output into TEST_EVENTS_FILE on its own line')"
check "names the step that sets TEST_EVENTS_FILE itself" \
  "$(contains "$out" 'step 2 of job leaky (Elsewhere) sets TEST_EVENTS_FILE, which only .github/scripts/go_test_events.py sets')"
check "refuses those six and nothing else" "$(contains "$out" '^6 problem(s) across the 2 workflow file(s) under .github/workflows$')"

# events_more_scripts <root>: the scripts of the second pair of rule 7 cases: one a script runs by
# `source`, one with no suffix and a shell's #! line, one that runs go test under the writer
# itself, one whose comment names TEST_EVENTS_FILE beside a go test that keeps nothing.
events_more_scripts() {
  mkdir -p "$1/packages/x/scripts"
  printf '#!/bin/sh\ngo test ./sourced\n' > "$1/packages/x/scripts/sourced.sh"
  printf '#!/usr/bin/env bash\ngo test ./nosuffix\n' > "$1/packages/x/scripts/unit"
  printf '#!/bin/sh\nexec ../../.github/scripts/go_test_events.py go test ./pkg\n' > "$1/packages/x/scripts/kept.sh"
  printf '#!/bin/sh\n# TEST_EVENTS_FILE is inherited.\n../../.github/scripts/go_test_events.py go test ./one\ngo test ./two\n' \
    > "$1/packages/x/scripts/mixed.sh"
  # Three go tests whose tees keep no event: one that writes no -json, one whose tee is a
  # comment, one that tees into another file.
  printf '%s\n' '#!/bin/sh' \
    'go test -count=1 ./plain | tee -a "$TEST_EVENTS_FILE" >/dev/null' \
    'go test -json -count=1 ./comment # | tee -a "$TEST_EVENTS_FILE"' \
    'go test -json -count=1 ./suffix | tee -a "${TEST_EVENTS_FILE}.other" >/dev/null' \
    > "$1/packages/x/scripts/tees.sh"
}

echo "case: a script that keeps its own events, an \${{ }} condition, an echoed TEST_EVENTS_FILE= and -c=true pass"
root=$(fixture events-kept-more)
events_router "$root"
events_more_scripts "$root"
cat > "$root/.github/workflows/ci-job-unit.yaml" <<YAML
name: CI job unit
on: workflow_call
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - name: A script that runs go test under the writer itself
        id: inside
        if: \${{ inputs.unit == 'true' }}
        run: |
          echo "TEST_EVENTS_FILE=example.jsonl is what the writer names"
          scripts/kept.sh
          go test -c=true -o /dev/null ./pkg
        working-directory: packages/x
      - name: Upload the go test events
        if: \${{ always() && inputs.unit == 'true' }}
        uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: go-test-events-\${{ job.check_run_id }}
          path: \${{ runner.temp }}/go-test-events/
          if-no-files-found: error
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the one script, its step and its job" \
  "$(contains "$out" 'keep the events of 1 go test command(s) in 1 step(s) of 1 job(s) of ci.yaml.s run, ')"

echo "case: control: go test run by a shell's heredoc, quoted, by source, with no suffix, from the workflow's directory, with -c=false, or with no upload, is refused"
root=$(fixture events-bare-more)
events_router "$root"
events_more_scripts "$root"
cat > "$root/.github/workflows/ci-job-unit.yaml" <<YAML
name: CI job unit
on: workflow_call
defaults:
  run:
    working-directory: packages/x
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - name: A heredoc bash runs
        id: heredoc-bash
        run: |
          bash <<'SCRIPT'
          go test ./a
          SCRIPT
      - name: A heredoc's substitution
        id: heredoc-substitution
        run: |
          cat <<SCRIPT
          \$(go test ./b)
          SCRIPT
      - name: Quoted go and a quoted assignment
        id: quoted
        run: |
          "go" test ./c
          GODEBUG="checkfinalizers=1" go test ./d
      - name: A sourced script
        id: sourced
        run: source scripts/sourced.sh
      - name: A script with no suffix, from the workflow's default directory
        id: nosuffix
        run: bash scripts/unit
      - name: -c=false runs the tests
        id: compile-false
        run: go test -c=false ./e
      - name: A text heredoc runs nothing
        id: text
        run: |
          cat <<'TEXT'
          go test ./f
          TEXT
      - name: A string bash -c runs
        id: bash-c
        run: bash -c 'go test -count=1 ./h'
      - name: A heredoc env bash runs
        id: env-heredoc
        run: |
          env bash <<'SCRIPT'
          go test -count=1 ./i
          SCRIPT
      - name: A backquoted go test
        id: backquoted
        run: result=\`go test -count=1 ./j\`
$upload_step
  tees:
    runs-on: ubuntu-latest
    steps:
      - name: Tees that keep nothing
        id: tees
        run: ../../.github/scripts/go_test_events.py scripts/tees.sh
$upload_step
  mixed:
    runs-on: ubuntu-latest
    steps:
      - name: A script whose comment names the file and whose go test keeps nothing
        id: mixed
        run: ../../.github/scripts/go_test_events.py scripts/mixed.sh
$upload_step
  python-writer:
    runs-on: ubuntu-latest
    steps:
      - name: The writer run by python3, no upload
        id: python-writer
        run: python3 ../../.github/scripts/go_test_events.py go test ./g
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "reads a heredoc bash runs" "$(contains "$out" 'step 1 of job unit (A heredoc bash runs) runs `go test ./a` other than through')"
check "reads a substitution in an unquoted heredoc" "$(contains "$out" 'step 2 of job unit (A heredoc.s substitution) runs `go test ./b` other than through')"
check "reads a quoted go and a quoted assignment's go test" \
  "$(contains "$out" 'step 3 of job unit (Quoted go and a quoted assignment) runs `"go" test ./c`, `go test ./d` other than through')"
check "follows source" \
  "$(contains "$out" 'step 4 of job unit (A sourced script) runs packages/x/scripts/sourced.sh, which runs `go test ./sourced`')"
check "follows a script with no suffix from the workflow's directory" \
  "$(contains "$out" 'step 5 of job unit (A script with no suffix, from the workflow.s default directory) runs packages/x/scripts/unit, which runs `go test ./nosuffix`')"
check "reads -c=false as a run" "$(contains "$out" 'step 6 of job unit (-c=false runs the tests) runs `go test -c=false ./e` other than through')"
check "leaves a text heredoc alone" "$(is "$(contains "$out" 'A text heredoc runs nothing')" false)"
check "reads the string bash -c runs" "$(contains "$out" 'step 8 of job unit (A string bash -c runs) runs `go test -count=1 ./h` other than through')"
check "reads a heredoc env bash runs" "$(contains "$out" 'step 9 of job unit (A heredoc env bash runs) runs `go test -count=1 ./i` other than through')"
check "reads a backquoted go test" "$(contains "$out" 'step 10 of job unit (A backquoted go test) runs `go test -count=1 ./j` other than through')"
check "holds a tee to go test -json, to the file itself and to the line outside its comment" \
  "$(contains "$out" 'step 1 of job tees (Tees that keep nothing) runs a script under .github/scripts/go_test_events.py, and the script runs `go test -count=1 ./plain` (packages/x/scripts/tees.sh:2), `go test -json -count=1 ./comment` (packages/x/scripts/tees.sh:3), `go test -json -count=1 ./suffix` (packages/x/scripts/tees.sh:4), which neither')"
check "holds a script's go test to its own line, not a comment naming the file" \
  "$(contains "$out" 'step 1 of job mixed (A script whose comment names the file and whose go test keeps nothing) runs a script under .github/scripts/go_test_events.py, and the script runs `go test ./two` (packages/x/scripts/mixed.sh:4)')"
check "counts the writer python3 runs, and asks its job for the upload" \
  "$(contains "$out" 'job python-writer keeps go test events (its last such step is step 1, The writer run by python3, no upload) and no step after it uploads them')"
check "refuses those twelve and nothing else" "$(contains "$out" '^12 problem(s) across the 2 workflow file(s) under .github/workflows$')"

# filters_file <root> <pattern>...: a .github/ci-filters.yaml whose ci_filters filter holds the
# patterns, one level of them inside an anchor as the real file writes its lists.
filters_file() {
  local root=$1
  shift
  {
    echo "shared: &shared"
    echo "  - '.github/workflows/**'"
    echo "ci_filters:"
    echo "  - *shared"
    for pattern in "$@"; do echo "  - '$pattern'"; done
  } > "$root/.github/ci-filters.yaml"
}

echo "case: control: a script rule 7 reads that the ci_filters filter does not select is refused, naming the step that reaches it"
root=$(fixture events-filter-misses)
events_router "$root"
events_scripts "$root"
filters_file "$root" 'packages/envoy/**'
cat > "$root/.github/workflows/ci-job-unit.yaml" <<YAML
name: CI job unit
on: workflow_call
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - name: A script two levels down, under the writer as a whole
        id: outer
        run: ../../.github/scripts/go_test_events.py scripts/kept-outer.sh
        working-directory: packages/x
      - name: The skip counter
        id: skips
        run: .github/scripts/go_test_events.py packages/envoy/scripts/database-skips.sh --timeout 20m
$upload_step
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the script the step runs" \
  "$(contains "$out" '^::error file=.github/workflows/ci-job-unit.yaml,line=7::.github/workflows/ci-job-unit.yaml:7: step 1 of job unit (A script two levels down, under the writer as a whole) runs packages/x/scripts/kept-outer.sh, itself or through a script it runs, and rule 7 reads it, but the ci_filters filter of .github/ci-filters.yaml does not select it')"
check "and the script that script runs, by the same step" \
  "$(contains "$out" 'step 1 of job unit (A script two levels down, under the writer as a whole) runs packages/x/scripts/kept.sh, itself or through a script it runs')"
check "says what that lets merge" "$(contains "$out" 'a go test added to it merges unread and fails the next pull request that starts the job')"
check "passes the script the filter selects" "$(is "$(contains "$out" 'database-skips.sh, itself')" false)"
check "refuses those two and nothing else" "$(contains "$out" '^2 problem(s) across the 2 workflow file(s) under .github/workflows$')"
filters_file "$root" 'packages/envoy/**' 'packages/*/scripts/**'
run_check "$root"
check "with a pattern that selects them, exits 0" "$(is "$status" 0)"
check "and says each script it read is one the filter selects" \
  "$(contains "$out" 'run, and each of the 3 script(s) it read is one the ci_filters filter selects$')"

# The repository's own filter, read as ci.yaml's dorny/paths-filter step reads it: a change to a
# script alone, which a step of ci.yaml's run reaches, starts the ci-filters job. The control is
# that filter less the patterns that name the scripts' directories, as it stood before rule 7
# read them.
echo "case: a change to a script alone that a step reaches starts the ci-filters job, under the repository's own filter"
selects() {
  (cd "$repo" && PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=.github/scripts python3 - "$@" <<'PY'
import sys
import yaml
from workflow_paths import PathsFilter

def flat(value):
    return [value] if isinstance(value, str) else [p for item in value for p in flat(item)] if isinstance(value, list) else []

drop = set(sys.argv[1].split()) if sys.argv[1] != "-" else set()
patterns = [p for p in flat(yaml.safe_load(open(".github/ci-filters.yaml"))["ci_filters"]) if p not in drop]
chosen = PathsFilter(patterns, "some-with-excludes")
print(" ".join("yes" if chosen.covers(path) else "no" for path in sys.argv[2:]))
PY
  )
}
scripts_dirs=".github/scripts/** scripts/** packages/*/scripts/** packages/dispatch/e2e/visual-run.sh"
reached="scripts/e2e/stage1-skeleton.sh .github/scripts/go-tagged-tests.sh packages/pi-envoy/scripts/local-stack.sh packages/dispatch/e2e/visual-run.sh"
# shellcheck disable=SC2086 # the lists are words
check "scripts/e2e/stage1-skeleton.sh, go-tagged-tests.sh, a package's script and visual-run.sh each start it" \
  "$(is "$(selects - $reached)" "yes yes yes yes")"
# shellcheck disable=SC2086
check "control: without the scripts' patterns, none of the four does" \
  "$(is "$(selects "$scripts_dirs" $reached)" "no no no no")"

echo "case: with no ci.yaml, rule 7 says it read nothing rather than that nothing failed it"
root=$(fixture events-no-router)
workflow "$root/.github/workflows/build.yaml"
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "says the count is UNKNOWN and why" \
  "$(contains "$out" 'keep go test events UNKNOWN: there is no .github/workflows/ci.yaml, so rule 7 read no step$')"

summary "check-workflow-files.sh"
