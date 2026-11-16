#!/usr/bin/env bash
# Tests for `check-download-retry.sh`: every network fetch of this repository's CI retries at
# R1's floor, a curl whose own options would put its last attempt inside that floor is refused,
# an `apt-get update` fails rather than warns when an index read fails so that the retry has
# something to retry, a `mise x` carries no retry at all because it exits 0 when it cannot
# resolve the tool, a fetch left of a `|` in a step whose shell leaves pipefail off is refused
# because the pipeline's status is its last command's, a step's budget holds every pass of a
# loop around a retried call, one rule holds that retry, and the exceptions R2 lists are
# the only sites left bare (dispatch://INFRA-1545, R2).
#
# Each case builds a miniature repository under a temporary directory and symlinks the real
# script into it, so the script under test is the file CI runs. Every fixture starts from the
# same baseline: every exception that still has a site, each at the file, job and step name the
# check's exception
# list holds, the loops its DECLARED_LOOPS list declares the arithmetic of, each at the file,
# step and verb that list holds, and the three Dockerfiles it reads. Both lists fail the check
# when an entry matches nothing, which is why the baseline carries one site for each. A case
# that proves a refusal adds a workflow, an action or a Dockerfile of its own beside that
# baseline, so a refusal is the case's own and not a hole in the baseline, and the baseline's
# own case proves it passes. The last of rule 8's cases is the one that builds no miniature: it
# copies this repository's own tree and lowers its real keys, each below the sum of the block the
# gate's --jobs says prices it, through .github/scripts/download_retry_real_tree.py.
#
# Run from anywhere: .github/scripts/check-download-retry.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
check_script="$script_dir/check-download-retry.sh"
work=$(mktemp -d "${TMPDIR:-/tmp}/check-download-retry.XXXXXX")
trap 'rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

PIN=fbc6f3992d24b796d5a048ff273f7fcc4a7b6c09
DIGEST=sha256:5498ba57b9471840be3d15b033a4eec554d1c02fa6c2cc0ca2d888637f6c6e2f

# The canonical inline retry every network RUN of the three Dockerfiles opens with. A fixture
# that means to pass writes this text; a fixture that means to be refused writes another.
read -r -d '' CANONICAL <<'TEXT' || true
RUN retry() { b=$1; what=$2; shift 2; i=1; while :; do timeout "$b" "$@" && return 0; s=$?; \
  [ "$i" -ge 5 ] && { echo "$what failed after $i attempts (last exit $s)" >&2; return "$s"; }; \
  sleep "$(( 10 << (i - 1) ))"; i=$((i + 1)); done; } \
TEXT

# fixture <name>: a tree holding the baseline below, with the script linked in. Echoes its root.
#
# The baseline is R2's exceptions that still have a site, which every fixture carries for two
# reasons: the
# check refuses an exception that matches no fetch, so a tree without them fails for a reason no
# case is about; and a case that then adds one bad site proves that site alone.
fixture() {
  local root="$work/$1"
  if [ -e "$root" ]; then
    echo "  FAIL: the fixture name '$1' is already taken by an earlier case" >&2
    return 1
  fi
  mkdir -p "$root/.github/scripts" "$root/.github/workflows" \
    "$root/.github/actions/pull-test-images" \
    "$root/packages/daemon/docker" "$root/packages/envoy/docker" \
    "$root/packages/envoy/infra/host/heldportwrapper"
  ln -s "$check_script" "$root/.github/scripts/check-download-retry.sh"
  ln -s "$script_dir/workflow_paths.py" "$root/.github/scripts/workflow_paths.py"
  for dockerfile in \
    "$root/packages/daemon/docker/worker.Dockerfile" \
    "$root/packages/envoy/docker/Dockerfile" \
    "$root/packages/envoy/infra/host/heldportwrapper/Dockerfile"; do
    printf 'FROM scratch\nCOPY . /src\n' > "$dockerfile"
  done

  # Exception (a), first site: the ghcr.io pull of a package this repository publishes, read from
  # a Go const and pulled in a loop.
  cat > "$root/.github/actions/pull-test-images/action.yml" <<'YAML'
name: Pull the images tests start
description: Pulls each image a job's tests start, from the package this repository publishes.
runs:
  using: composite
  steps:
    - name: Pull the images tests start
      shell: bash
      run: |
        for image in $IMAGES; do
          docker pull "$image"
        done
YAML

  # Exceptions (a) again and (b), and a browser download, which is a site like any other and
  # retries like one: exception (c)'s script held the apt phase beside it and went with that
  # script (cybermonklabs/legion#2938). The jobs the exceptions name sit in the files main's
  # #2945 moved them to, .github/workflows/ci-job-<id>.yaml, which is where the exceptions
  # name them.
  cat > "$root/.github/workflows/ci-job-envoy-client.yaml" <<YAML
name: CI job envoy-client
on:
  workflow_call:
jobs:
  envoy-client:
    runs-on: ubuntu-24.04
    steps:
      - name: Pre-pull the NATS image the credential tests run
        run: docker pull ghcr.io/example/legion/ci@$DIGEST
YAML
  cat > "$root/.github/workflows/ci-job-download-retry-images.yaml" <<YAML
name: CI job download-retry-images
on:
  workflow_call:
jobs:
  download-retry-images:
    runs-on: ubuntu-24.04
    steps:
      - name: Pull every base image the three builds read
        run: |
          for image in \$images; do docker pull "\$image"; done
YAML
  cat > "$root/.github/workflows/ci-job-airlock-lean.yaml" <<YAML
name: CI job airlock-lean
on:
  workflow_call:
jobs:
  airlock-lean:
    runs-on: ubuntu-24.04
    steps:
      # Rule 11's exemption names this job, and its guard refuses an entry matching no job that
      # compiles Go here, so every fixture carries the job its entry names, as each fixture
      # carries the site an EXEMPTIONS entry names. The real job builds a package importing the
      # standard library alone; a fixture needs only the verb.
      - name: Compare the Go airlock with the model
        run: go test ./internal/broker/airlock/
YAML
  cat > "$root/.github/workflows/ci.yaml" <<YAML
name: CI
on: [push]
jobs:
  dispatch:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@$PIN
      - name: Install Playwright Chromium
        # The browser download's 5 x 300 + 150 = 1650 s. Rule 8 asks every step holding a
        # retried call for a budget above its arithmetic, so the baseline's own steps carry
        # one: a fixture without them fails for a reason no case is about.
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "Playwright's Chromium" 300 -- bunx playwright install chromium
YAML

  # Exception (a)'s fourth and fifth sites: the browser lane's visual job pulls the pinned
  # Playwright copy and its stack's Postgres copy from the same package. The entries naming them
  # are in EXEMPTIONS, and an entry matching nothing fails this check, so every fixture carries
  # the sites its entries name.
  cat > "$root/.github/workflows/dispatch-browser.yaml" <<YAML
name: Dispatch browser
on:
  workflow_call:
jobs:
  visual:
    runs-on: ubuntu-24.04
    steps:
      - name: Pull the pinned Playwright copy
        run: docker pull ghcr.io/example/legion/ci@$DIGEST
      - name: Pull the visual stack's Postgres copy
        run: docker pull ghcr.io/example/legion/ci@$DIGEST
YAML

  # Exception (d): a credentialed publish job, whose pinned command set no wrapper may change.
  cat > "$root/.github/workflows/release.yaml" <<'YAML'
name: Release
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install dependencies
        # 5 x 60 + 150 = 450 s.
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "the workspace's dependencies" 60 -- bun install --frozen-lockfile
  publish:
    runs-on: ubuntu-24.04
    permissions:
      contents: write
    steps:
      - name: Determine latest
        run: gh api "repos/$GITHUB_REPOSITORY/releases/latest"
YAML

  # The loops DECLARED_LOOPS declares the arithmetic of, one call of each at the file, step
  # and verb that list holds. Every fixture carries them for the reason it carries the four
  # exceptions: a declaration matching no such loop fails the check, so a tree without them
  # fails for a reason no case is about. Each is the shape that list is for — a loop over the
  # work the run found, whose pass count the check cannot read off the loop — and each passes
  # every other rule, so a case that adds one bad site still proves that site alone. Each
  # carries the budget rule 8 asks of a step holding a retried call, 6 minutes above the 300 s
  # its declaration writes, so none of them is a budget a case has to reckon with.
  cat >> "$root/.github/workflows/ci.yaml" <<'YAML'
  cancel-started:
    runs-on: ubuntu-24.04
    steps:
      - name: Force-cancel the runs of the groups the queue removed
        timeout-minutes: 6
        run: |
          . .github/scripts/ci-retry.sh
          while read -r act id; do
            retry_fetch "the force-cancel of run $id" 30 -- \
              gh api --silent --method POST "repos/$GITHUB_REPOSITORY/actions/runs/$id/force-cancel"
          done <<<"$acts"
YAML
  cat > "$root/.github/workflows/dequeued.yaml" <<'YAML'
name: CI (dequeued pull request)
on: [pull_request]
jobs:
  cancel-dequeued:
    runs-on: ubuntu-24.04
    steps:
      - name: Cancel the removed group's ci.yaml run
        timeout-minutes: 6
        run: |
          . .github/scripts/ci-retry.sh
          while read -r id; do
            retry_fetch "the force-cancel of run $id" 30 -- \
              gh api --silent --method POST "repos/$GITHUB_REPOSITORY/actions/runs/$id/force-cancel"
          done <<<"$doomed"
YAML
  cat > "$root/.github/workflows/main-bypass-check.yaml" <<'YAML'
name: CI (main bypass check)
on: [push]
jobs:
  bypass-check:
    runs-on: ubuntu-24.04
    steps:
      - name: Start ci.yaml when this push's head was never tested
        timeout-minutes: 6
        run: |
          . .github/scripts/ci-retry.sh
          while read -r act sha; do
            retry_fetch "the ci.yaml dispatch" 30 -- \
              gh workflow run ci.yaml --repo "$GITHUB_REPOSITORY" --ref "$GITHUB_REF_NAME"
          done <<<"$tokens"
YAML
  cat > "$root/.github/scripts/check-pr-text.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
while :; do
  retry_fetch "page $page of the $route route" 30 -- \
    gh api "repos/$repository/$route?per_page=100&page=$page" > "$work/answer"
  [ "$(jq length "$work/answer")" = 100 ] || break
  page=$((page + 1))
done
SH
  cat > "$root/.github/scripts/ci-ok-reconcile.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
while read -r id status was marker; do
  retry_fetch "the PATCH of ci-ok check run $id" 30 -- \
    gh api --silent --method PATCH "repos/$GITHUB_REPOSITORY/check-runs/$id" --input "$body"
done <<<"$runs"
SH
  # The annotations loop of runner-shutdown.sh, which one deadline bounds rather than a count of
  # passes (main's #2945 wrote the loop): its DECLARED_LOOPS entry matches nothing in a tree
  # without the file, so every fixture carries it, sourced as the repository sources it.
  cat > "$root/.github/scripts/runner-shutdown.sh" <<'SH'
#!/usr/bin/env bash
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-retry.sh"
runner_shutdown_kills() {
  local job url
  for job in "$@"; do
    while read -r url; do
      retry_fetch "the annotations of $job" 30 -- gh api --paginate "$url/annotations?per_page=100"
    done <<<"$urls"
  done
}
SH
  # The last of them is read through two functions: the call sits in image_go(), which
  # check_go() calls, and the loop runs check_go. A declaration covers it for the loop that
  # runs those calls, which is the loop the entry names.
  cat > "$root/.github/scripts/mirror-ci-images.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
image_go() {
  retry_fetch "the config of $1" 60 -- crane config --platform linux/amd64 "$1"
}
check_go() {
  local carried_go
  carried_go=$(image_go "$1")
  [ "$carried_go" = "$2" ] || exit 1
}
while IFS=$'\t' read -r registry tag digest source claimed_go; do
  check_go "$registry@$digest" "$claimed_go"
  retry_fetch "the copy of $source" 300 -- crane copy "$source" "$registry:$tag"
  have=$(retry_fetch "the digest of $registry:$tag" 60 -- crane digest "$registry:$tag")
done <<<"$rows"
SH
  # The entry of that table which is no loop: the one curl of .github/scripts/ci-tools.sh,
  # whose `--max-time` is the row's own field and so is a bound the check cannot read. One
  # call of it has no figure, so the declaration writes what the site contributes whole, and
  # an entry matching nothing fails the check — which is why every fixture carries this file
  # too. No step here runs it, so the seconds that entry writes sit in no budget a case has
  # to reckon with. It is sourced and not executed in the repository, and it is not
  # executable here either.
  cat > "$root/.github/scripts/ci-tools.sh" <<'SH'
#!/usr/bin/env bash
ci_tools_curl() {
  local file=$1 max_time=$2 url=$3
  shift 3
  curl -fsSL --retry 7 --retry-all-errors --retry-max-time 300 --max-time "$max_time" -o "$file" "$@" "$url"
}
SH
  chmod +x "$root/.github/scripts/check-pr-text.sh" "$root/.github/scripts/ci-ok-reconcile.sh" \
    "$root/.github/scripts/mirror-ci-images.sh"
  echo "$root"
}

# GATE_BOUND: how long one call of the gate may take before this harness calls it hung and
# stops, naming the case that ran it. Measured on 2026-11-14 on an INFRA issue pod (2 CPUs,
# gVisor): an unmutated harness run made 147 gate calls, median 1.5 s, 99th percentile 1.87 s,
# slowest 1.94 s (`declaration-two-loops-below`). run_gate does not read a clock: it polls the
# call GATE_BOUND x 10 times, 600 polls of `sleep 0.1`, which is at least 60 s and longer under
# load, since each poll's sleep stretches with the load that slows the gate. That is at least
# thirty times the slowest call, so it cannot fail a correct run on a slow runner, and a gate
# that never returns fails here a minute or so in, under its case's name, rather than at the test
# job's own key with GitHub's bare timeout line. Round 32's mutation pass found two mutants of
# rule 8 that hang the gate (dispatch://INFRA-1545), and before this bound nothing below the job
# key ended them.
GATE_BOUND=60

# run_gate <stderr: out|null> <root> <args…>: runs the gate in a session of its own with its
# output in a file, never through a pipe: a gate that hangs is bash waiting on its Python, and
# killing the bash alone leaves the Python holding a pipe open, which blocks a `$(…)` capture
# for ever. At the bound the whole session is killed, the hang is one FAIL under the current
# case, and the harness ends there, since every later case would hang the same way.
run_gate() {
  local stderr=$1 root=$2 log="$work/gate.out" pid tenths=0
  shift 2
  if [ "$stderr" = out ]; then
    ( cd "$root" && exec setsid ./.github/scripts/check-download-retry.sh "$@" ) > "$log" 2>&1 &
  else
    ( cd "$root" && exec setsid ./.github/scripts/check-download-retry.sh "$@" ) > "$log" 2>/dev/null &
  fi
  pid=$!
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$tenths" -ge $((GATE_BOUND * 10)) ]; then
      kill -KILL -- "-$pid" 2>/dev/null
      wait "$pid" 2>/dev/null
      echo "  FAIL: the gate did not return within $((GATE_BOUND * 10)) polls of \`sleep 0.1\` after this case started it, which is at least ${GATE_BOUND} s and longer under load"
      FAIL=$((FAIL + 1))
      summary "check-download-retry.sh"
    fi
    sleep 0.1
    tenths=$((tenths + 1))
  done
  wait "$pid"
  status=$?
  out=$(cat "$log")
}

run_check() {
  local root=$1
  shift
  set +e
  run_gate out "$root" "$@"
  set -e
}

# declare_entry <root> <one DECLARED_LOOPS entry, in Python>: replaces the check this fixture
# links with a copy of it carrying that entry at the end of its DECLARED_LOOPS table. A
# declaration is proved this way because the tree's own table holds only the entries the tree
# needs, and none of them covers a loop a fixture writes. The copy is the same file otherwise,
# so every other rule reads the fixture as it always does.
declare_entry() {
  local root=$1 entry=$2 copy="$root/.github/scripts/check-download-retry.sh"
  rm "$copy"
  awk -v entry="$entry" '
    /^DECLARED_LOOPS/ { inside = 1 }
    inside && $0 == "]" { print entry; inside = 0 }
    { print }
  ' "$check_script" > "$copy"
  chmod +x "$copy"
}

# declare_in <root> <table> <one entry, in Python>: as declare_entry, for any table of
# declarations the gate holds — DECLARED_LOOPS, DECLARED_LIBRARIES or DECLARED_CALLS — and onto
# the copy an earlier call made, so one fixture can carry entries of more than one table
# (dispatch://INFRA-1545, round 37).
declare_in() {
  local root=$1 table=$2 entry=$3 copy="$root/.github/scripts/check-download-retry.sh"
  local source=$check_script
  if [ ! -L "$copy" ]; then
    source="$work/declare-in.sh"
    cp "$copy" "$source"
  fi
  rm "$copy"
  awk -v table="$table" -v entry="$entry" '
    index($0, table ":") == 1 { inside = 1 }
    inside && $0 == "]" { print entry; inside = 0 }
    { print }
  ' "$source" > "$copy"
  chmod +x "$copy"
}

list_check() {
  local root=$1
  set +e
  run_gate null "$root" --list
  set -e
}

echo "case: a tree holding each exception R2 still has a site for passes"
root=$(fixture exceptions)
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "says what it read" "$(contains "$out" 'every network fetch in CI retries at the floor')"
# Six sites, not the seven this case counted while exception (c) had a script: that script
# went with the browser suite's move into an image this repository builds
# (cybermonklabs/legion#2938), and its two exempted apt sites went with it; the visual job's
# pull of its stack's Postgres copy is the one added since (dispatch://INFRA-1545, round 37).
check "counts the sites the exceptions name" "$(contains "$out" '6 site(s) the exceptions name')"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"

echo "case: a workflow with a bare curl of a release asset"
root=$(fixture bare-curl)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install changie
        run: |
          curl -fsSL https://github.com/miniscruff/changie/releases/download/v1.21.1/changie.tar.gz \
            | tar -xz -C /usr/local/bin changie
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the file, the line and the step" \
  "$(contains "$out" 'file=.github/workflows/case.yaml,line=8::.curl. in step .Install changie. fetches without the floor')"
check "names each option it lacks" "$(contains "$out" 'it passes none')"
check "names --retry-all-errors" "$(contains "$out" 'retry-all-errors')"
check "names --max-time" "$(contains "$out" 'max-time')"
check "counts the refusal" "$(contains "$out" '1 download(s) outside the one retry rule')"

echo "case: a workflow whose curl carries a retry below the floor"
root=$(fixture short-retry)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install crane
        run: |
          curl -fsSL --retry 3 --retry-all-errors --max-time 30 -o crane.tar.gz \
            https://github.com/google/go-containerregistry/releases/download/v0.21.1/crane.tar.gz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the count it passes" "$(contains "$out" 'it passes .--retry 3.')"
check "names the floor" "$(contains "$out" 'only 7 reaches 127 s')"

echo "case: a curl that retries but bounds no attempt"
root=$(fixture unbounded-curl)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install uv
        run: curl -fsSL --retry 7 --retry-all-errors -o uv.tar.gz https://example.com/uv.tar.gz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the missing bound" "$(contains "$out" 'which curl resets for each attempt')"

echo "case: a curl that retries and never fails on a persistent 504"
root=$(fixture no-fail)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install uv
        run: curl -sSL --retry 7 --retry-all-errors --max-time 30 -o uv.tar.gz https://example.com/uv.tar.gz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names --fail and what it costs" "$(contains "$out" 'writes the error body to the output')"

echo "case: a curl that reads the status itself answers for --fail"
root=$(fixture status-read)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read the release
        # 8 x 30 + 127 = 367 s.
        timeout-minutes: 7
        run: |
          code=$(curl -sSL --retry 7 --retry-all-errors --max-time 30 -o out.json -w '%{http_code}' https://example.com/r)
          [ "$code" = 200 ]
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# Two of curl's options rewrite the waits between its own attempts, and each can put the last
# attempt inside R1's floor of 120 s while every other test of that floor passes. The rule is
# curl's own, read at curl 8.14.1: the first wait is one second and doubles for each of the
# rest, `--retry-delay <n>` replaces that with a fixed n, and `--retry-max-time <n>` lets a
# retry start only while the timer reset at the first attempt has not reached n.
echo "case: a curl whose --retry-delay puts the last attempt seconds after the first"
root=$(fixture retry-delay)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install uv
        run: curl -fsSL --retry 7 --retry-all-errors --retry-delay 1 --max-time 60 -o uv.tar.gz https://example.com/uv.tar.gz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the ladder it computed" \
  "$(contains "$out" 'its last one starts 7 s after the first, inside R1.s floor of 120 s')"
check "names the option that made it so" "$(contains "$out" '.--retry-delay 1., which replaces')"

echo "case: a curl whose --retry-max-time stops the ladder inside the floor"
root=$(fixture retry-max-time)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install uv
        run: curl -fsSL --retry 7 --retry-all-errors --retry-max-time 5 --max-time 60 -o uv.tar.gz https://example.com/uv.tar.gz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the ladder the limit allows" \
  "$(contains "$out" 'its last one starts 7 s after the first')"
check "names the option that made it so" \
  "$(contains "$out" '.--retry-max-time 5., which stops the ladder')"

echo "case: a curl whose --retry-delay reaches the floor passes"
root=$(fixture retry-delay-wide)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install uv
        # 8 x 60 + 7 x 20 = 620 s.
        timeout-minutes: 11
        run: curl -fsSL --retry 7 --retry-all-errors --retry-delay 20 --max-time 60 -o uv.tar.gz https://example.com/uv.tar.gz
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a curl whose retry wait is not a number this check can read is refused"
root=$(fixture retry-delay-unreadable)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install uv
        run: curl -fsSL --retry 7 --retry-all-errors --retry-delay "$delay" --max-time 60 -o uv.tar.gz https://example.com/uv.tar.gz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "says it cannot read the wait" \
  "$(contains "$out" 'with a value this check cannot read')"

echo "case: a composite action holding a second copy of the retry helper"
root=$(fixture copied-helper)
mkdir -p "$root/.github/actions/install-thing"
cat > "$root/.github/actions/install-thing/action.yml" <<'YAML'
name: Install the thing
description: Installs the thing, with a retry loop of its own.
runs:
  using: composite
  steps:
    - name: Install the thing
      shell: bash
      run: |
        attempt=1
        while [ "$attempt" -le 5 ]; do
          curl -fsSL --retry 7 --retry-all-errors --max-time 30 -o thing.tar.gz https://example.com/thing && break
          sleep $((10 * attempt))
          attempt=$((attempt + 1))
        done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the loop as a copy" \
  "$(contains "$out" 'file=.github/actions/install-thing/action.yml,line=9::this loop retries .curl. itself, which makes it a second copy')"
check "names the one rule" "$(contains "$out" 'One rule holds the attempts')"

echo "case: a loop that hands a fetch to a retry function of its own is a copy too"
root=$(fixture copied-helper-indirect)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install the packages
        run: |
          attempts() {
            local left=$1 what=$2 status
            shift 2
            while :; do
              "$@" && return 0
              status=$?
              left=$((left - 1))
              [ "$left" -gt 0 ] || return 1
            done
          }
          attempts 2 "apt-get update" sudo timeout 90 apt-get update
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the loop the call reaches" \
  "$(contains "$out" 'file=.github/workflows/case.yaml,line=11::this loop retries .apt-get update. itself')"

echo "case: a loop whose fetch is a retry_fetch call is no copy"
root=$(fixture loop-with-helper)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Copy each row
        # Three passes of 5 x 300 + 150 = 1650 s. The loop writes its passes so that this case
        # proves rule 2 alone: an unreadable one would be refused for want of a figure.
        timeout-minutes: 83
        run: |
          . .github/scripts/ci-retry.sh
          for row in one two three; do
            retry_fetch "the copy of $row" 300 -- crane copy "$row" "ghcr.io/example/ci:$row"
          done
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a retry_fetch call written without a per-attempt bound"
root=$(fixture unbounded-helper)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install the workspace's dependencies
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "the workspace's dependencies" -- bun install --frozen-lockfile
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the missing bound" \
  "$(contains "$out" 'is retried and no attempt of it is bounded in time. Give .retry_fetch. a per-attempt bound')"

echo "case: a Dockerfile with an unretried apt-get"
root=$(fixture bare-apt)
cat > "$root/packages/envoy/docker/Dockerfile" <<'DOCKER'
FROM debian:trixie-slim AS build
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
DOCKER
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the Dockerfile, the line and the verb" \
  "$(contains "$out" 'file=packages/envoy/docker/Dockerfile,line=2::.apt-get update. fetches inside a Dockerfile RUN with no retry')"
check "names the inline loop to use" "$(contains "$out" 'the canonical inline loop')"

echo "case: a Dockerfile RUN that opens with the canonical retry and wraps each fetch passes"
root=$(fixture canonical-retry)
{
  echo 'FROM debian:trixie-slim AS build'
  printf '%s\n' "$CANONICAL"
  cat <<'DOCKER'
    && retry 120 "apt-get update" apt-get update -o APT::Update::Error-Mode=any \
    && retry 300 "apt-get install" apt-get install -y --no-install-recommends curl \
    && rm -rf /var/lib/apt/lists/*
DOCKER
} > "$root/packages/envoy/docker/Dockerfile"
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a Dockerfile's retried apt-get update that keeps apt's warning on a failed index"
root=$(fixture dockerfile-apt-error-mode)
{
  echo 'FROM debian:trixie-slim AS build'
  printf '%s\n' "$CANONICAL"
  cat <<'DOCKER'
    && retry 120 "apt-get update" apt-get update \
    && retry 300 "apt-get install" apt-get install -y --no-install-recommends curl \
    && rm -rf /var/lib/apt/lists/*
DOCKER
} > "$root/packages/envoy/docker/Dockerfile"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the Dockerfile, the line and the option" \
  "$(contains "$out" 'file=packages/envoy/docker/Dockerfile,line=5::.apt-get update. passes no .-o APT::Update::Error-Mode=any.')"
check "says what goes wrong, not only what is missing" \
  "$(contains "$out" 'the retry around it stops after one attempt having read no list')"
check "names the install the unread list breaks" \
  "$(contains "$out" 'has no installation candidate')"

echo "case: a Dockerfile RUN with a retry loop of its own text"
root=$(fixture other-retry)
cat > "$root/packages/envoy/docker/Dockerfile" <<'DOCKER'
FROM debian:trixie-slim AS build
RUN retry() { for i in 1 2 3; do "$@" && return 0; sleep 5; done; return 1; } \
    && retry apt-get update \
    && rm -rf /var/lib/apt/lists/*
DOCKER
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the canonical text as the one definition" \
  "$(contains "$out" 'this RUN defines a retry loop of its own text')"

echo "case: a Dockerfile RUN whose one fetch is a curl carrying curl's own options passes"
root=$(fixture dockerfile-curl)
cat > "$root/packages/daemon/docker/worker.Dockerfile" <<'DOCKER'
FROM debian:trixie-slim AS build
RUN set -eu; \
    curl -fsSL --retry 7 --retry-all-errors --max-time 120 -o /tmp/mise.tar.gz \
      "https://example.com/mise.tar.gz"; \
    tar -xzf /tmp/mise.tar.gz -C /tmp
DOCKER
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a workflow that wraps a mise x in a retry"
root=$(fixture retried-mise-x)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install the worker image's jj
        run: |
          . .github/scripts/ci-retry.sh
          jj_path=$(retry_fetch "the worker image's jj" 60 -- mise x "$jj_tool" -- sh -c 'command -v jj')
          install -m 0755 "$jj_path" "$HOME/.local/bin/jj"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the file, the line, the step and the verb" \
  "$(contains "$out" "file=.github/workflows/case.yaml,line=9::.mise x. in step .Install the worker image.s jj. is retried, and that retry protects nothing")"
check "says what goes wrong, not only what to write instead" \
  "$(contains "$out" 'runs whatever binary of that name is already on PATH')"
check "names the cure" "$(contains "$out" 'The cure is .mise install <tool>. under the retry')"
check "names the resolution that follows it" \
  "$(contains "$out" 'with the resolution after it')"

echo "case: a Dockerfile RUN that wraps a mise x in the canonical retry"
root=$(fixture retried-mise-x-dockerfile)
{
  echo 'FROM debian:trixie-slim AS build'
  printf '%s\n' "$CANONICAL"
  cat <<'DOCKER'
    && install -m 0755 "$(retry 300 "jj download" mise x "$JJ_TOOL" -- sh -c 'command -v jj')" /opt/tools/jj
DOCKER
} > "$root/packages/daemon/docker/worker.Dockerfile"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the Dockerfile, the line and the verb" \
  "$(contains "$out" 'file=packages/daemon/docker/worker.Dockerfile,line=5::.mise x. is retried, and that retry protects nothing')"
check "names the cure" "$(contains "$out" 'The cure is .mise install <tool>. under the retry')"
check "counts the one refusal" \
  "$(contains "$out" '1 download(s) outside the one retry rule')"

echo "case: a workflow's retried mise install with the resolution after it passes"
root=$(fixture mise-install-cured)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install the worker image's jj
        # 5 x 60 + 150 = 450 s.
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "the worker image's jj" 60 -- mise install "$jj_tool"
          jj_path=$(mise x "$jj_tool" -- sh -c 'command -v jj')
          install -m 0755 "$jj_path" "$HOME/.local/bin/jj"
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
list_check "$root"
check "the install is on the list" "$(contains "$out" 'case.yaml	11	mise install	')"
check "the resolution after it is on no list" \
  "$(is "$(contains "$out" 'mise x')" false)"

echo "case: a Dockerfile's retried mise install with the resolution after it passes"
root=$(fixture mise-install-cured-dockerfile)
{
  echo 'FROM debian:trixie-slim AS build'
  printf '%s\n' "$CANONICAL"
  cat <<'DOCKER'
    && retry 300 "jj download" mise install "$JJ_TOOL" \
    && install -m 0755 "$(mise x "$JJ_TOOL" -- sh -c 'command -v jj')" /opt/tools/jj
DOCKER
} > "$root/packages/daemon/docker/worker.Dockerfile"
run_check "$root"
check "exits 0" "$(is "$status" 0)"
list_check "$root"
check "the install is on the list" \
  "$(contains "$out" '^packages/daemon/docker/worker.Dockerfile	5	mise install	-$')"
check "the resolution after it is on no list" \
  "$(is "$(contains "$out" 'mise x')" false)"

echo "case: a readiness poll of the loopback is no download"
root=$(fixture loopback)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Wait for the S3-compatible server
        run: |
          for attempt in $(seq 1 60); do
            curl -sf -o /dev/null http://127.0.0.1:18333/ && break
            sleep 1
          done
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
list_check "$root"
check "the poll is on no list" "$(is "$(contains "$out" 'case.yaml')" false)"

echo "case: a probe whose failure is the answer is no download"
root=$(fixture probe)
cat > "$root/.github/scripts/mirror.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
have=$(crane digest "ghcr.io/example/ci:$tag" 2>/dev/null || true)
[ "$have" = "$digest" ] || echo copy
SH
chmod +x "$root/.github/scripts/mirror.sh"
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: the same read with its failure a fault is a download"
root=$(fixture probe-not)
cat > "$root/.github/scripts/mirror.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
have=$(crane digest "ghcr.io/example/ci:$tag")
echo "$have"
SH
chmod +x "$root/.github/scripts/mirror.sh"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the read" "$(contains "$out" 'file=.github/scripts/mirror.sh,line=3::.crane digest. fetches with no retry')"

# The same scoping in a workflow step, which is where ci.yaml's proof-sdk-offline job reads its
# fault proxy: two requests through the proxy before the installs, one to a faulted host that
# must be refused and one to a control that must be answered. Each reads whether the proxy is in
# the path and brings nothing into the job, so each is written the way
# .github/scripts/download-retry-proof.sh's through_proxy writes one — error silenced, status
# consumed into the reading — and this check reads it as the probe it is. Written as a plain
# fetch the same two requests are two downloads, and the pair below proves both halves: a step
# that takes the scoping out of the shape is refused by rule 1 for each curl, and by rule 8 for
# the budget the ladders it would then make need.
echo "case: a workflow step's curl whose status is the reading is no download"
root=$(fixture probe-step)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  offline:
    runs-on: ubuntu-24.04
    steps:
      - name: Probe the proxy before the install
        run: |
          faulted=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 30 https://api.example.com/meta 2>/dev/null || true)
          [ "${faulted:-000}" = 000 ]
          passed=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 60 https://registry.example.org/ 2>/dev/null || true)
          [ "${passed:-000}" != 000 ]
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
list_check "$root"
check "neither request is on the list" "$(is "$(contains "$out" 'case.yaml')" false)"

echo "case: the same two requests written as plain fetches are two downloads"
root=$(fixture probe-step-not)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  offline:
    runs-on: ubuntu-24.04
    steps:
      - name: Probe the proxy before the install
        run: |
          if curl -sS -o /dev/null --max-time 30 https://api.example.com/meta; then
            exit 1
          fi
          curl -sS -o /dev/null --max-time 60 https://registry.example.org/
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the first request" \
  "$(contains "$out" 'file=.github/workflows/case.yaml,line=8::.curl. in step .Probe the proxy before the install. fetches without the floor')"
check "names the second request" \
  "$(contains "$out" 'file=.github/workflows/case.yaml,line=11::.curl. in step .Probe the proxy before the install. fetches without the floor')"
check "asks the step for the budget those ladders would need" \
  "$(contains "$out" "holds 974 s of retries and carries no \`timeout-minutes\`")"

echo "case: a Python read that goes straight to urllib"
root=$(fixture python-read)
cat > "$root/.github/scripts/read.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
python3 <<'PY'
import urllib.request

with urllib.request.urlopen("https://ghcr.io/token", timeout=30) as answer:
    print(answer.read())
PY
SH
chmod +x "$root/.github/scripts/read.sh"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the file and the one reader" \
  "$(contains "$out" 'file=.github/scripts/read.sh,line=6::this read goes straight to urllib.request')"
check "names the floor it carries" "$(contains "$out" 'waits of 10, 20, 40 and 80')"

echo "case: the same read inside another file's read_retrying is still refused"
root=$(fixture python-copy)
cat > "$root/.github/scripts/read.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
python3 <<'PY'
import urllib.request


def read_retrying(request):
    for wait in (10, 20, 40, 80, None):
        try:
            return urllib.request.urlopen(request, timeout=30)
        except Exception:
            if wait is None:
                raise
PY
SH
chmod +x "$root/.github/scripts/read.sh"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the copy" "$(contains "$out" 'this read goes straight to urllib.request')"

echo "case: an apt unpack after a retried download takes no network"
root=$(fixture apt-unpack)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install this proof's packages
        # 5 x 95 + 150 = 625 s and 5 x 125 + 150 = 775 s; the unpack after them takes no
        # network and is no term.
        timeout-minutes: 24
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "apt's package lists" 95 -- \
            sudo timeout 90 apt-get update -o APT::Update::Error-Mode=any
          retry_fetch "the proof's apt packages" 125 -- \
            sudo timeout 120 apt-get install -y --no-install-recommends -d $PACKAGES
          sudo apt-get install -y --no-install-recommends $PACKAGES
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a workflow's retried apt-get update that keeps apt's warning on a failed index"
root=$(fixture apt-error-mode)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install the packages
        # 5 x 95 + 150 = 625 s and 5 x 125 + 150 = 775 s.
        timeout-minutes: 24
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "apt's package lists" 95 -- sudo timeout 90 apt-get update
          retry_fetch "the packages" 125 -- \
            sudo timeout 120 apt-get install -y --no-install-recommends -d $PACKAGES
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the file, the line, the step and the option" \
  "$(contains "$out" "file=.github/workflows/case.yaml,line=11::.apt-get update. in step .Install the packages. passes no .-o APT::Update::Error-Mode=any.")"
check "says what goes wrong, not only what is missing" \
  "$(contains "$out" 'the retry around it stops after one attempt having read no list')"
check "the retried install beside it is no problem of this rule" \
  "$(contains "$out" '1 download(s) outside the one retry rule')"

echo "case: a command that only answers about itself is no fetch"
root=$(fixture versions)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read the toolchain
        run: curl --version; wget --version; bunx biome check .
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: --list prints every site with its step name"
root=$(fixture listing)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Download the Go modules this job builds
        # 5 x 300 + 150 = 1650 s.
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "packages/envoy's Go modules" 300 -- go -C packages/envoy mod download
YAML
cat > "$root/.github/scripts/report.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "the run's artifact list" 60 -- gh api "$route" > "$out"
SH
chmod +x "$root/.github/scripts/report.sh"
list_check "$root"
check "exits 0" "$(is "$status" 0)"
check "a workflow site carries its step name" \
  "$(contains "$out" '^.github/workflows/case.yaml	11	go mod download	Download the Go modules this job builds$')"
check "a script site carries a dash for the step" \
  "$(contains "$out" '^.github/scripts/report.sh	4	gh api	-$')"
check "the baseline's own retried site is on it" \
  "$(contains "$out" 'playwright install	Install Playwright Chromium$')"
check "an excepted site is not on it" "$(is "$(contains "$out" 'docker pull')" false)"
check "the count goes to standard error, not into the rows" \
  "$(is "$(contains "$out" 'site\(s\)')" false)"

echo "case: --list over a tree with a problem refuses instead of listing"
root=$(fixture listing-refused)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install changie
        run: curl -fsSL -o changie.tar.gz https://example.com/changie.tar.gz
YAML
run_check "$root" --list
check "exits 1" "$(is "$status" 1)"
check "names the problem" "$(contains "$out" 'fetches without the floor')"

echo "case: an exception that matches no fetch fails"
root=$(fixture stale-exception)
rm "$root/.github/actions/pull-test-images/action.yml"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the exception and what it no longer covers" \
  "$(contains "$out" 'the exception for .github/actions/pull-test-images/action.yml matches no fetch')"

echo "case: one of the three Dockerfiles missing fails"
root=$(fixture missing-dockerfile)
rm "$root/packages/daemon/docker/worker.Dockerfile"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the file" \
  "$(contains "$out" 'packages/daemon/docker/worker.Dockerfile is one of the three Dockerfiles')"

echo "case: a workflow that cannot be parsed as YAML"
root=$(fixture bad-yaml)
printf 'name: Case\non: [push\n' > "$root/.github/workflows/case.yaml"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the file" "$(contains "$out" 'this file is not valid YAML')"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"

# A step's own budget must be larger than the attempts it allows, not equal to them. Measured in
# run 42386541065's download-retry job, where `Read the run's artifact list under the stalling
# fault` carries `timeout-minutes: 5` against its own arithmetic of 5 x 30 + 150 = 300 s: the
# fifth attempt ended at 300.03 s and the runner killed the step in the same tenth of a second
# (`the run's artifact list: attempt 5 of 5 reached its 30s bound` at 17:22:16.7444966Z, then
# `The action 'Read the run's artifact list under the stalling fault' has timed out after 5
# minutes.` at 17:22:16.7483230Z). The helper's own `::error::` line won that race by four
# milliseconds; a run whose attempts drift later loses it, and the job then reports a timeout
# rather than the download that failed, which is the diagnosis this change exists to give.
echo "case: a step whose own timeout-minutes is no larger than the attempts it allows"
root=$(fixture budget-no-headroom)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read the run's artifact list
        timeout-minutes: 5
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "the run's artifact list" 30 -- gh api "repos/$GITHUB_REPOSITORY/actions/runs/1/artifacts"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step" "$(contains "$out" "Read the run's artifact list")"
check "names the budget" "$(contains "$out" 'timeout-minutes')"
# The figures a refusal prints are what its reader writes next, so each is pinned exactly: a
# `cost // 60 + 1` turned into `/` prints `6.0`, which a check for `timeout-minutes: 6` alone
# also matches (dispatch://INFRA-1545, round 33).
check "says the budget in seconds" "$(contains "$out" 'carries .timeout-minutes: 5., 300 s, and its own retries take 300 s')"
check "says exactly what to write" "$(contains "$out" 'so write .timeout-minutes: 6. or more\.')"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"

echo "case: the same step with a budget above its attempts passes"
root=$(fixture budget-headroom)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read the run's artifact list
        timeout-minutes: 6
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "the run's artifact list" 30 -- gh api "repos/$GITHUB_REPOSITORY/actions/runs/1/artifacts"
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# A call inside a loop is one call per pass of that loop, and the step waits for all of them.
# dequeued.yaml's timeline read is the site these cases close: one `retry_fetch` call of
# 5 x 30 + 150 = 300 s written inside a seven-pass poll with a 10 s sleep between the passes,
# 7 x 300 + 6 x 10 = 2160 s, which the check read as 300 s before this change and passed under
# a budget of 16 minutes.
echo "case: a counted loop multiplies the call in it, and a budget written for one call fails"
root=$(fixture loop-counted)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Poll the pull request's timeline
        timeout-minutes: 6
        run: |
          . .github/scripts/ci-retry.sh
          for attempt in 1 2 3 4 5 6 7; do
            removal=$(retry_fetch "the timeline" 30 -- gh api graphql -f query="$query") && break
            sleep 10
          done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step" "$(contains "$out" "Poll the pull request's timeline")"
check "multiplies the call by the passes and adds the loop's own sleeps" \
  "$(contains "$out" 'retries take 2160 s to exhaust')"
check "names the budget that would hold it" "$(contains "$out" 'timeout-minutes: 37')"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"

echo "case: the same counted loop under a budget above the multiplied figure passes"
root=$(fixture loop-counted-headroom)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Poll the pull request's timeline
        timeout-minutes: 37
        run: |
          . .github/scripts/ci-retry.sh
          for attempt in 1 2 3 4 5 6 7; do
            removal=$(retry_fetch "the timeline" 30 -- gh api graphql -f query="$query") && break
            sleep 10
          done
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a loop counted with seq multiplies the same way"
root=$(fixture loop-seq)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read each page of the route
        timeout-minutes: 15
        run: |
          . .github/scripts/ci-retry.sh
          for page in $(seq 1 3); do
            retry_fetch "page $page" 30 -- gh api "repos/$GITHUB_REPOSITORY/issues?page=$page"
          done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts the three passes and no sleep" "$(contains "$out" 'retries take 900 s to exhaust')"

echo "case: a step whose budget reaches a loop with no readable count is refused by name"
root=$(fixture loop-unreadable)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Cancel every run the decision named
        timeout-minutes: 30
        run: |
          . .github/scripts/ci-retry.sh
          while read -r id; do
            retry_fetch "the cancel of run $id" 30 -- \
              gh api --silent --method POST "repos/$GITHUB_REPOSITORY/actions/runs/$id/cancel"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step and the call it cannot count" \
  "$(contains "$out" "step .Cancel every run the decision named. carries .timeout-minutes: 30. and this check can put no figure on it")"
check "names the loop's file and line" \
  "$(contains "$out" '.gh api. at .github/workflows/case.yaml:11, inside the loop at line 10')"
check "says what one call of it costs" "$(contains "$out" 'One call there takes 300 s')"
check "says where a count can be written or declared" "$(contains "$out" 'DECLARED_LOOPS')"
check "does not judge the budget against a figure it is missing" \
  "$(is "$(contains "$out" 'does not strictly exceed')" false)"
check "does not ask for a bound written as a number, the bound being one" \
  "$(is "$(contains "$out" '[Ww]rite that bound as a number')" false)"

# Two uncounted loops, one inside the other's script: the step's loop cannot be counted, and
# neither can the loop inside the script it runs, so the figure for one run of that script is
# one this check cannot make either. It printed `0 s` here while the read's None was taken for
# a zero, which is the gate stating a cost it never computed - rule 8's own defect, one layer
# in (dispatch://INFRA-1545, round 4).
echo "case: a script whose own loop is uncounted is refused with no figure at all"
root=$(fixture script-loop-unreadable)
cat > "$root/.github/scripts/c.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
while read -r id; do
  retry_fetch "the run $id" 30 -- gh api "repos/$GITHUB_REPOSITORY/actions/runs/$id"
done <<<"$ids"
SH
chmod +x "$root/.github/scripts/c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read every run the decision named
        timeout-minutes: 30
        run: |
          while read -r id; do
            .github/scripts/c.sh "$id"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step and the call it cannot count" \
  "$(contains "$out" "step .Read every run the decision named. carries .timeout-minutes: 30. and this check can put no figure on it")"
check "names the script call and the loop around it" \
  "$(contains "$out" '..github/scripts/c.sh. at .github/workflows/case.yaml:10, inside the loop at line 9')"
check "says one run of that script reaches an uncounted loop" \
  "$(contains "$out" 'One run of ..github/scripts/c.sh. reaches a loop, its own or one in a script it calls, whose passes this check cannot read either')"
check "prints no figure for a cost it cannot compute" \
  "$(is "$(contains "$out" 'takes 0 s')" false)"
check "still says what to do instead" \
  "$(contains "$out" 'take the call out of the loop')"

# The loop can be two scripts in: the step loops over a script that calls another outside any
# loop, and that second one holds the `while read`. The figure for one run of the first is
# still one this check cannot make, because the run reaches the uncounted loop either way, but
# the loop is not the first script's own — the sentence said it was while the site list named
# the second script, so the two halves of one refusal pointed a reader at different files
# (dispatch://INFRA-1545, round 5).
echo "case: a loop in a script the called script calls is refused without naming the wrong file"
root=$(fixture script-loop-two-deep)
cat > "$root/.github/scripts/c2.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/d.sh "$1"
SH
cat > "$root/.github/scripts/d.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
while read -r id; do
  retry_fetch "the run $id" 30 -- gh api "repos/$GITHUB_REPOSITORY/actions/runs/$id"
done <<<"$ids"
SH
chmod +x "$root/.github/scripts/c2.sh" "$root/.github/scripts/d.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read every run the decision named
        timeout-minutes: 30
        run: |
          while read -r id; do
            .github/scripts/c2.sh "$id"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the script the step runs and the loop around that call" \
  "$(contains "$out" '..github/scripts/c2.sh. at .github/workflows/case.yaml:10, inside the loop at line 9')"
check "names the site in the second script that stopped the reading" \
  "$(contains "$out" '.gh api. at .github/scripts/d.sh:5, inside the loop at line 4')"
check "says the loop one run reaches may sit in a script that script calls" \
  "$(contains "$out" 'One run of ..github/scripts/c2.sh. reaches a loop, its own or one in a script it calls')"
check "does not call the uncounted loop the first script's own" \
  "$(is "$(contains "$out" 'holds a loop of its own')" false)"

# A loop this check cannot count around a script that retries nothing is no loop rule 8 has
# anything to say about: nobody waits for an attempt under it, so there is no sum for a budget
# to exceed and no pass count to ask the step's author for. The gate refused this shape and
# priced the run it was refusing at `0 s`, because blocked_calls lists every script call in an
# uncounted loop and the script's own arithmetic of 0 is not the None the no-figure sentence
# reads (dispatch://INFRA-1545, round 5).
echo "case: a loop around a script that fetches nothing passes"
root=$(fixture script-no-fetch-in-loop)
cat > "$root/.github/scripts/zz-e.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "$1"
SH
chmod +x "$root/.github/scripts/zz-e.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Echo every id the decision named
        timeout-minutes: 30
        run: |
          printf 'a\nb\n' | while read -r id; do .github/scripts/zz-e.sh "$id"; done
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "asks the step for no pass count" \
  "$(is "$(contains "$out" 'can put no figure on it')" false)"
check "prices no run of that script at nothing" \
  "$(is "$(contains "$out" 'takes 0 s')" false)"

# One layer deeper: the loop sits in a script the step runs, around a call of the costless
# one, so the item blocked_calls lists is that inner call rather than the step's own. The one
# reading drops it there too, because the step's list and the lists of every script and
# action it reaches take the same filter (review round 5 of dispatch://INFRA-1545). What leaves the step
# with a figure rather than none is shell_arithmetic(), which charges nothing for a call inside
# an uncounted loop where the script it reaches, with what that script calls, costs 0 s; the case
# below holding a priced script beside such a loop is the one that pins that answer.
echo "case: a loop around a script that calls a script that fetches nothing passes"
root=$(fixture script-no-fetch-two-deep)
cat > "$root/.github/scripts/zz-g.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "$1"
SH
cat > "$root/.github/scripts/zz-h.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
while read -r id; do
  .github/scripts/zz-g.sh "$id"
done <<<"$ids"
SH
chmod +x "$root/.github/scripts/zz-g.sh" "$root/.github/scripts/zz-h.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Echo every id the decision named
        timeout-minutes: 30
        run: .github/scripts/zz-h.sh
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "asks the step for no pass count" \
  "$(is "$(contains "$out" 'can put no figure on it')" false)"

# A script this check cannot price is not a script that retries nothing. A curl making its own
# attempts passes rule 1 on the options it writes, and `--max-time "$bound"` is one of them:
# the flag is there, so each attempt is bounded, and the seconds are not, so one_call reads no
# figure off the call. Answering 0 for that made the script read as one holding no retry at
# all, and the filter above dropped the loop around it, where the fetch is real and the step
# waits for the whole of it once per pass (review round 6 of dispatch://INFRA-1545).
echo "case: a loop around a script whose curl writes its bound as a variable is refused"
root=$(fixture script-unread-bound-in-loop)
cat > "$root/.github/scripts/zz-i.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
bound=60
curl -fsSL --retry 7 --retry-all-errors --max-time "$bound" -o run.json \
  "https://api.github.com/repos/$GITHUB_REPOSITORY/actions/runs/$1"
SH
chmod +x "$root/.github/scripts/zz-i.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read every run the decision named
        timeout-minutes: 1
        run: |
          printf 'a\nb\n' | while read -r id; do .github/scripts/zz-i.sh "$id"; done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the script call and the loop around it" \
  "$(contains "$out" '..github/scripts/zz-i.sh. at .github/workflows/case.yaml:9, inside the loop at line 9')"
check "says the bound is written as a variable rather than as a number" \
  "$(contains "$out" 'makes attempts whose bound is written as a variable rather than as a number')"
check "prices no run of that script at nothing" \
  "$(is "$(contains "$out" 'takes 0 s')" false)"

# The third member of that family, and the one the two cases above leave open: the site whose
# bound this check cannot read, written in the body's own loop rather than in a script the
# loop runs. One call of it has no figure — `--max-time "$bound"` bounds the attempt where the
# seconds are not written — and the loop around it has no pass count, so both halves of what
# the step waits for are unread. uncounted_of dropped it with the sites that cost nothing,
# because `one is None` was read as "this item is a script call" and a site with no figure had
# no sentence of its own to be refused with, so rule 8 priced the step's whole wait at 0 and
# refused nobody for it (review round 8 of dispatch://INFRA-1545).
#
# The fetch is written in a function the loop calls, because rule 2 refuses any fetch that is
# not a `retry_fetch` call written in a loop's own body, and a fetch in a function the loop
# runs sits in no loop of its own. That is rule 8's second reading of a loop — times_run folds
# the loops around the calls of the function holding the call — so the case is about rule 8
# alone, and the fixture passes every other rule.
echo "case: a loop around the step's own curl whose bound is a variable is refused"
root=$(fixture site-unread-bound-in-loop)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read every run the decision named
        timeout-minutes: 30
        run: |
          read_run() {
            curl -fsSL --retry 7 --retry-all-errors --max-time "$bound" \
              -o "run-$1.json" "https://api.github.com/repos/$GITHUB_REPOSITORY/actions/runs/$1"
          }
          while read -r id; do
            read_run "$id"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step and the site it cannot count" \
  "$(contains "$out" "step .Read every run the decision named. carries .timeout-minutes: 30. and this check can put no figure on it")"
check "names the site's line and the loop around it" \
  "$(contains "$out" '.curl. at .github/workflows/case.yaml:10, inside the loop at line 13')"
check "says what one call of it takes is a figure it cannot make" \
  "$(contains "$out" 'What one call of .curl. there takes is a figure this check cannot make, because its bound is written as a variable rather than as a number')"
check "says the passes of the loop are unread too" \
  "$(contains "$out" 'the step waits for that once per pass of a loop whose passes this check cannot read either')"
check "prints no figure for a call it cannot price" \
  "$(is "$(contains "$out" 'One call there takes')" false)"
check "prices the site at nothing nowhere" \
  "$(is "$(contains "$out" 'takes 0 s')" false)"
check "gives the site's advice and not a script call's" \
  "$(contains "$out" 'Write that bound as a number')"
# With the count advice left out, the advice is a sentence of its own and starts with a capital:
# it read `… it exists to allow. write that bound …` (dispatch://INFRA-1545, round 37).
check "starts the advice that follows the count's absence with a capital letter" \
  "$(contains "$out" 'cuts in the middle of the retry it exists to allow\. Write that bound as a number')"
check "starts no sentence of the refusal in lowercase" \
  "$(is "$(contains "$out" 'it exists to allow\. [a-z]')" false)"
check "says a declaration answers an unread bound" \
  "$(contains "$out" 'or declare that site in DECLARED_LOOPS in .github/scripts/check-download-retry.sh. An entry there writes the seconds the site contributes to this budget, whole')"
check "says why that entry writes the whole of it" \
  "$(contains "$out" 'neither half of what the step waits for — what one call takes, or how many calls the loop makes — is a figure this check can make')"
check "names what such an entry matches a site by" \
  "$(contains "$out" 'It matches a site by its file, the verb it retries and the step where the site is written in a workflow, a site inside a script being read with no step at all')"
check "no longer says no entry of DECLARED_LOOPS answers this one" \
  "$(is "$(contains "$out" 'No entry of DECLARED_LOOPS answers this one')" false)"
check "offers no pass count as a way out of this one" \
  "$(is "$(contains "$out" 'Write the count where this check reads it')" false)"
check "does not tell the author to take a call out of a loop" \
  "$(is "$(contains "$out" 'take the call out of the loop')" false)"

# A site with no bound at all, in that same loop, is not a site whose bound is a variable. Rule 8
# kept it, rightly, since the loop is its own fault and rule 1 says nothing of a loop, but it
# told the author the bound was written as a variable and to write that bound as a number,
# when the curl writes no `--max-time` at all (dispatch://INFRA-1545, round 37). It now says it
# finds no bound, asks for one as a number, and then for the count, since a bound alone leaves
# the loop unread. The second step is the control: a `retry_fetch` whose seconds are `"$T"`
# around a curl whose `--max-time` is `"$T"` writes its bound as a variable, and is told so.
echo "case: a loop around the step's own curl with no bound names no variable bound"
root=$(fixture site-no-bound-in-loop)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read every run the decision named
        timeout-minutes: 30
        run: |
          read_run() {
            curl -fsSL --retry 7 --retry-all-errors \
              -o "run-$1.json" "https://api.github.com/repos/$GITHUB_REPOSITORY/actions/runs/$1"
          }
          while read -r id; do
            read_run "$id"
          done <<<"$ids"
      - name: Fetch every row at the caller's bound
        timeout-minutes: 5
        run: |
          . .github/scripts/ci-retry.sh
          while read -r url; do
            retry_fetch "a row" "$T" -- curl -fsSL --max-time "$T" -o f "$url"
          done < urls.txt
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "leaves rule 1 refusing the curl on its own line" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=10::.curl. in step 'Read every run the decision named' fetches without the floor: it lacks .--max-time <seconds>.")"
check "refuses the step for the loop it cannot count" \
  "$(contains "$out" "step .Read every run the decision named. carries .timeout-minutes: 30. and this check can put no figure on it: it runs a retried call inside a loop whose passes it cannot read — .curl. at .github/workflows/case.yaml:10, inside the loop at line 13")"
check "says it finds no bound on one attempt of the call" \
  "$(contains "$out" "step .Read every run the decision named. .*What one call of .curl. there takes is a figure this check cannot make, because this check finds no bound on one attempt of it, and the step waits for that once per pass")"
check "does not say that call's bound is written as a variable" \
  "$(is "$(contains "$out" "step .Read every run the decision named. .*written as a variable")" false)"
check "asks for a bound written as a number, as a sentence of its own" \
  "$(contains "$out" "step .Read every run the decision named. .*it exists to allow\. Bound each attempt with a number of seconds — .--max-time <seconds>. on a curl, or the seconds .retry_fetch. takes before its .--. — which is the figure this check multiplies by the passes it reads\. Write the count where this check reads it")"
check "does not ask it to write as a number a bound it does not write" \
  "$(is "$(contains "$out" "step .Read every run the decision named. .*[Ww]rite that bound as a number")" false)"
check "tells the control its bound is written as a variable" \
  "$(contains "$out" "step .Fetch every row at the caller's bound. .*because its bound is written as a variable rather than as a number, .*it exists to allow\. Write that bound as a number")"

# The same false premise one script in: a script whose curl writes no bound, run in a loop the
# check cannot count, was said to make 'attempts whose bound is written as a variable'
# (dispatch://INFRA-1545, round 37). The second script holds one call of each kind, and is told
# of both.
echo "case: a loop around a script whose curl has no bound names no variable bound"
root=$(fixture script-no-bound-in-loop)
cat > "$root/.github/scripts/zz-nobound.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
curl -fsSL --retry 7 --retry-all-errors -o run.json \
  "https://api.github.com/repos/$GITHUB_REPOSITORY/actions/runs/$1"
SH
cat > "$root/.github/scripts/zz-mixed.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
bound=60
curl -fsSL --retry 7 --retry-all-errors --max-time "$bound" -o run.json \
  "https://api.github.com/repos/$GITHUB_REPOSITORY/actions/runs/$1"
curl -fsSL --retry 7 --retry-all-errors -o jobs.json \
  "https://api.github.com/repos/$GITHUB_REPOSITORY/actions/runs/$1/jobs"
SH
chmod +x "$root/.github/scripts/zz-nobound.sh" "$root/.github/scripts/zz-mixed.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read every run the decision named
        timeout-minutes: 1
        run: |
          printf 'a\nb\n' | while read -r id; do .github/scripts/zz-nobound.sh "$id"; done
      - name: Read every run and its jobs
        timeout-minutes: 1
        run: |
          printf 'a\nb\n' | while read -r id; do .github/scripts/zz-mixed.sh "$id"; done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "leaves rule 1 refusing the script's curl on its own line" \
  "$(contains "$out" "::error file=.github/scripts/zz-nobound.sh,line=3::.curl. fetches without the floor: it lacks .--max-time <seconds>.")"
check "names the script call and the loop around it" \
  "$(contains "$out" '..github/scripts/zz-nobound.sh. at .github/workflows/case.yaml:9, inside the loop at line 9')"
check "says it finds no bound on that script's attempts" \
  "$(contains "$out" 'One run of ..github/scripts/zz-nobound.sh. makes attempts on which this check finds no bound, there or in a script it calls')"
check "does not say that script's bound is written as a variable" \
  "$(is "$(contains "$out" "step .Read every run the decision named. .*written as a variable")" false)"
check "names both kinds in the script holding both" \
  "$(contains "$out" 'One run of ..github/scripts/zz-mixed.sh. makes attempts whose bound is written as a variable rather than as a number and attempts on which this check finds no bound, there or in a script it calls')"

# The fourth member of that family, and the one with no loop anywhere in it. A curl whose
# `--max-time` is written as a variable is bounded, so rule 4 passes it, and the seconds are
# the caller's, so one_call answers None: the passes here are plainly one, and one times a
# figure nobody can read is still no figure. Round 8 answered 0 for the site and the step
# passed under any budget at all, which is how every tool install fetching through
# .github/scripts/ci-tools.sh read green at a minute (review round 9 of dispatch://INFRA-1545).
echo "case: a step whose curl writes its bound as a variable, in no loop, is refused"
root=$(fixture site-unread-bound-no-loop)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install the tool this job runs
        timeout-minutes: 1
        run: |
          bound=60
          curl -fsSL --retry 7 --retry-all-errors --max-time "$bound" \
            -o tool.tar.gz https://example.com/tool.tar.gz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step and the site whose bound it cannot read" \
  "$(contains "$out" "step .Install the tool this job runs. carries .timeout-minutes: 1. and this check can put no figure on it: it runs a retried call whose attempts are bounded by a variable rather than by a number — .curl. at .github/workflows/case.yaml:10")"
check "says a budget cannot be held above a wait nobody can price" \
  "$(contains "$out" 'a budget cannot be held above a wait nobody can price')"
check "offers the bound written as a number" \
  "$(contains "$out" 'Write that bound as a number')"
check "offers the declaration beside it" \
  "$(contains "$out" 'declare that site in DECLARED_LOOPS')"
check "names no loop, there being none" \
  "$(is "$(contains "$out" 'inside the loop at line')" false)"
check "prices the step at nothing nowhere" \
  "$(is "$(contains "$out" 'holds 0 s of retries')" false)"

# And the declaration answers it, with no loop for the entry to name: the figure is the whole
# of what the site contributes, since there is no per-call number for a pass count to
# multiply. The entry is counted where it is read, so this check refuses it for matching
# nothing no more than it refuses the entries that answer a loop.
echo "case: a declaration answers that site, and is counted"
root=$(fixture site-unread-bound-declared)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install the tool this job runs
        timeout-minutes: 31
        run: |
          bound=60
          curl -fsSL --retry 7 --retry-all-errors --max-time "$bound" \
            -o tool.tar.gz https://example.com/tool.tar.gz
YAML
declare_entry "$root" '    DeclaredArithmetic(".github/workflows/case.yaml", "Install the tool this job runs", "curl", 1821, "the three fetches one failing install makes, at 8 x 60 + 127 = 607 s each: 3 x 607 = 1821 s"),'
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "asks the step for no figure of its own" \
  "$(is "$(contains "$out" 'can put no figure on it')" false)"
check "refuses the entry for matching no call nowhere" \
  "$(is "$(contains "$out" 'matches no retried call')" false)"

# The entry's seconds are the site's contribution and not a note beside it: the budget is held
# above that figure, and a budget under it is refused with it.
echo "case: the declared seconds are what that step's budget is held above"
root=$(fixture site-unread-bound-declared-under)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install the tool this job runs
        timeout-minutes: 30
        run: |
          bound=60
          curl -fsSL --retry 7 --retry-all-errors --max-time "$bound" \
            -o tool.tar.gz https://example.com/tool.tar.gz
YAML
declare_entry "$root" '    DeclaredArithmetic(".github/workflows/case.yaml", "Install the tool this job runs", "curl", 1821, "the three fetches one failing install makes, at 8 x 60 + 127 = 607 s each: 3 x 607 = 1821 s"),'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "holds the budget above the declared figure" \
  "$(contains "$out" 'carries .timeout-minutes: 30., 1800 s, and its own retries take 1821 s to exhaust')"

# The floor an entry is held to is one call of the site it covers, and this site has none:
# one_call answered None, which is the absence of the very figure that comparison reads. An
# entry below one call of a site that has one is still refused, which the case for that floor
# elsewhere proves; here there is nothing to compare and the entry stands on its own `why`.
echo "case: no floor is compared with an entry answering a bound nobody can read"
root=$(fixture site-unread-bound-declared-small)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install the tool this job runs
        timeout-minutes: 2
        run: |
          bound=60
          curl -fsSL --retry 7 --retry-all-errors --max-time "$bound" \
            -o tool.tar.gz https://example.com/tool.tar.gz
YAML
declare_entry "$root" '    DeclaredArithmetic(".github/workflows/case.yaml", "Install the tool this job runs", "curl", 60, "one attempt of one fetch, which is all this fixture claims"),'
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "compares the entry with no floor it cannot read" \
  "$(is "$(contains "$out" 'A site costs at least one call of itself')" false)"

# The other half of that reading, which the case above must not take with it: a site one_call
# prices at 0 is still dropped. The loop the exempt action writes is as unreadable as the one
# above, and the `docker pull` under it is a verb this check puts no attempt behind, so there
# is nothing under that loop for the step's budget to exceed and no pass count to ask the
# action's author for. Keeping every site with no figure, rather than only the ones whose
# bound is unread, refuses this step — and the baseline's own two image loops with it.
echo "case: a step whose action loops, uncounted, over a verb that waits for nothing passes"
root=$(fixture site-costless-in-loop)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Pull the images tests start
        timeout-minutes: 30
        uses: ./.github/actions/pull-test-images
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "asks the step for no pass count" \
  "$(is "$(contains "$out" 'can put no figure on it')" false)"
check "prices no call of that loop at nothing" \
  "$(is "$(contains "$out" 'takes 0 s')" false)"

# The filter's question asked one layer in: the step's loop runs a script whose own work is a
# loop this check cannot count over a script that fetches nothing. No attempt waits under
# either loop, so neither is a loop rule 8 has a pass count to ask the step's author for.
# Reading the question as "the script holds no uncounted item of its own" refused this with
# the `0 s` sentence, because the inner call is an item of the script the step runs
# (review round 6 of dispatch://INFRA-1545).
echo "case: a loop around a script that loops over a script that fetches nothing passes"
root=$(fixture script-no-fetch-in-loop-two-deep)
cat > "$root/.github/scripts/zz-j.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "$1"
SH
cat > "$root/.github/scripts/zz-k.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
while read -r id; do
  .github/scripts/zz-j.sh "$id"
done <<<"$ids"
SH
chmod +x "$root/.github/scripts/zz-j.sh" "$root/.github/scripts/zz-k.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Echo every id the decision named
        timeout-minutes: 30
        run: |
          printf 'a\nb\n' | while read -r id; do .github/scripts/zz-k.sh "$id"; done
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "asks the step for no pass count" \
  "$(is "$(contains "$out" 'can put no figure on it')" false)"
check "prices no run of either script at nothing" \
  "$(is "$(contains "$out" 'takes 0 s')" false)"

# A script this check does price, holding beside that ladder a loop it cannot count around a
# costless script. The ladder is 450 s the step waits for and the costless loop takes nothing
# off it, so the budget is held to the 450 s. shell_arithmetic() charging nothing for an
# uncounted call of a script that costs 0 s is what keeps that figure readable: without it the
# costless call answers None, the whole script's arithmetic answers None with it, and the step is
# compared with nothing and passes in silence (review round 6 of dispatch://INFRA-1545).
echo "case: a priced script that also loops, uncounted, over a costless one keeps its figure"
root=$(fixture script-priced-beside-costless-loop)
cat > "$root/.github/scripts/zz-l.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "$1"
SH
cat > "$root/.github/scripts/zz-m.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "the run" 60 -- gh api "repos/$GITHUB_REPOSITORY/actions/runs/$1"
while read -r id; do
  .github/scripts/zz-l.sh "$id"
done <<<"$ids"
SH
chmod +x "$root/.github/scripts/zz-l.sh" "$root/.github/scripts/zz-m.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read the run the decision named
        timeout-minutes: 1
        run: .github/scripts/zz-m.sh a
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "holds the step to the ladder the priced script makes" \
  "$(contains "$out" 'its own retries take 450 s to exhaust')"
check "asks the step for no pass count" \
  "$(is "$(contains "$out" 'can put no figure on it')" false)"

# The same loop around a script that does fetch is the shape rule 8 is for: the step waits for
# that script's whole ladder once per pass, and the passes are unread.
echo "case: the same loop around a script that does fetch is refused"
root=$(fixture script-fetch-in-loop)
cat > "$root/.github/scripts/zz-f.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "the run" 60 -- gh api "repos/$GITHUB_REPOSITORY/actions/runs/$1"
SH
chmod +x "$root/.github/scripts/zz-f.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read every run the decision named
        timeout-minutes: 30
        run: |
          printf 'a\nb\n' | while read -r id; do .github/scripts/zz-f.sh "$id"; done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the script call and the loop around it" \
  "$(contains "$out" '..github/scripts/zz-f.sh. at .github/workflows/case.yaml:9, inside the loop at line 9')"
check "says what one run of that script takes" \
  "$(contains "$out" 'One run of that script takes 450 s to exhaust its attempts')"

# A step with no budget of its own is no longer a step nothing judges: rule 8 asks a budget of
# every step holding a retried call, so the loop this check cannot read is refused here too,
# and the refusal names the loop rather than the missing budget — a reader cannot write the
# budget until the site has a figure.
echo "case: the same loop in a step with no budget is refused and named all the same"
root=$(fixture loop-unreadable-unbudgeted)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Cancel every run the decision named
        run: |
          . .github/scripts/ci-retry.sh
          while read -r id; do
            retry_fetch "the cancel of run $id" 30 -- \
              gh api --silent --method POST "repos/$GITHUB_REPOSITORY/actions/runs/$id/cancel"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "says the step writes no budget" \
  "$(contains "$out" "step .Cancel every run the decision named. carries no .timeout-minutes. and this check can put no figure on it")"
check "names the loop's file and line" \
  "$(contains "$out" '.gh api. at .github/workflows/case.yaml:10, inside the loop at line 9')"

# A loop whose passes are as many as the run finds work has its own arithmetic declared in the
# check's own DECLARED_LOOPS, and the budget is held to that figure. The baseline's
# ci-ok-reconcile.sh is one of them, declared at the 900 s three PATCHes take: a 15-minute
# budget does not strictly exceed them.
echo "case: a declared loop is counted at the seconds the declaration writes"
root=$(fixture loop-declared)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Set every other ci-ok on this head to this job's conclusion
        timeout-minutes: 15
        run: .github/scripts/ci-ok-reconcile.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts the 900 s the declaration writes" \
  "$(contains "$out" 'retries take 900 s to exhaust')"
check "names the script the step runs" "$(contains "$out" 'ci-ok-reconcile.sh')"

echo "case: the same declared loop under a budget above its figure passes"
root=$(fixture loop-declared-headroom)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Set every other ci-ok on this head to this job's conclusion
        timeout-minutes: 16
        run: .github/scripts/ci-ok-reconcile.sh
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a declared loop that matches no loop in the tree fails"
root=$(fixture stale-declaration)
rm "$root/.github/scripts/mirror-ci-images.sh"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the declaration and what it no longer covers" \
  "$(contains "$out" 'the declared arithmetic for .github/scripts/mirror-ci-images.sh .crane copy. matches no retried call this check could not price of itself')"
check "says what the declaration was for" "$(contains "$out" 'written for 1650 s')"

# A site whose passes this check cannot resolve is answered in seconds, not in passes, and the
# figure stands for the whole of what that site contributes to its step.
echo "case: a declared arithmetic is the figure the budget is held to"
root=$(fixture declared-arithmetic)
declare_entry "$root" '    DeclaredArithmetic(".github/workflows/case.yaml",
                       "Cancel every run the decision named", "gh api", 1200,
                       "four cancels at 5 x 30 + 150 = 300 s each: 4 x 300 = 1200 s"),'
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Cancel every run the decision named
        timeout-minutes: 20
        run: |
          . .github/scripts/ci-retry.sh
          while read -r id; do
            retry_fetch "the cancel of run $id" 30 -- \
              gh api --silent --method POST "repos/$GITHUB_REPOSITORY/actions/runs/$id/cancel"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "uses the declared seconds and not one call of the site" \
  "$(contains "$out" 'retries take 1200 s to exhaust')"
check "no longer refuses the site for want of a figure" \
  "$(is "$(contains "$out" 'can put no figure on it')" false)"

echo "case: a declared arithmetic below one call of its site is refused"
root=$(fixture declared-arithmetic-floor)
declare_entry "$root" '    DeclaredArithmetic(".github/workflows/case.yaml",
                       "Cancel every run the decision named", "gh api", 200,
                       "a figure nobody derived"),'
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Cancel every run the decision named
        timeout-minutes: 20
        run: |
          . .github/scripts/ci-retry.sh
          while read -r id; do
            retry_fetch "the cancel of run $id" 30 -- \
              gh api --silent --method POST "repos/$GITHUB_REPOSITORY/actions/runs/$id/cancel"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the site and the floor one call of it sets" \
  "$(contains "$out" 'writes 200 s, and one call of that site at .github/workflows/case.yaml:11 takes 300 s')"
check "says a site costs one call of itself" \
  "$(contains "$out" 'A site costs at least one call of itself')"

# The shape that reached no comparison at all: a step whose whole arithmetic is a composite
# action's, carrying no budget of its own. ci.yaml's `airlock-lean` held install-elan's
# 8 x 60 + 127 = 607 s ladder that way, under a job budget with no term for it, and this check
# built a budget only for a step that wrote one.
echo "case: a step running a composite action's ladder with no budget of its own is refused"
root=$(fixture action-unbudgeted)
mkdir -p "$root/.github/actions/install-elan"
cat > "$root/.github/actions/install-elan/action.yml" <<'YAML'
name: Install elan
description: Installs elan, the Lean toolchain manager.
runs:
  using: composite
  steps:
    - name: Install elan
      shell: bash
      run: |
        curl -fsSL --retry 7 --retry-all-errors --max-time 60 -o elan.tar.gz \
          https://example.com/elan.tar.gz
YAML
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install elan
        uses: ./.github/actions/install-elan
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step, its arithmetic and the budget it writes none of" \
  "$(contains "$out" "step .Install elan. holds 607 s of retries and carries no .timeout-minutes. of its own")"
check "names the measured shape it comes from" "$(contains "$out" 'install-elan')"
check "says exactly what to write" "$(contains "$out" 'write .timeout-minutes: 11. or more on this step')"

echo "case: the same step carrying that budget passes"
root=$(fixture action-budgeted)
mkdir -p "$root/.github/actions/install-elan"
cat > "$root/.github/actions/install-elan/action.yml" <<'YAML'
name: Install elan
description: Installs elan, the Lean toolchain manager.
runs:
  using: composite
  steps:
    - name: Install elan
      shell: bash
      run: |
        curl -fsSL --retry 7 --retry-all-errors --max-time 60 -o elan.tar.gz \
          https://example.com/elan.tar.gz
YAML
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install elan
        uses: ./.github/actions/install-elan
        timeout-minutes: 11
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# A job ceiling under one of its own steps' arithmetic cuts that step before the step's own
# budget can name the site, so the step's margin buys nothing.
echo "case: a job whose budget is under one of its steps' retries is refused"
root=$(fixture job-budget)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 5
    steps:
      - name: Install changie
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "the changie tarball" 60 -- \
            curl -fsSL --max-time 60 -o changie.tar.gz https://example.com/changie.tar.gz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the job, its budget and the step's arithmetic" \
  "$(contains "$out" "job .build. carries .timeout-minutes: 5., 300 s, and its step .Install changie. holds retries taking 450 s to exhaust")"
check "says what to write on the job" "$(contains "$out" 'timeout-minutes: 8. or more on this job')"

echo "case: the same job with a budget above that step passes"
root=$(fixture job-budget-headroom)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 0 s measured, plus its slowest retried step alone, the changie tarball's
    # 5 x 60 + 150 = 450 s -> 9 min.
    timeout-minutes: 9
    steps:
      - name: Install changie
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "the changie tarball" 60 -- \
            curl -fsSL --max-time 60 -o changie.tar.gz https://example.com/changie.tar.gz
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# Rule 8's `<=`, at the job ceiling against a step's arithmetic: a job whose budget EQUALS that
# arithmetic is refused. Every other fixture of this clause leaves a gap of seconds between the
# two, where `<` and `<=` answer alike, so this one is what refuses a revert of the comparison
# (dispatch://INFRA-1545, round 31). 5 x 90 + 150 = 600 s, which is 10 minutes exactly.
echo "case: a job whose budget equals one of its steps' retries to the second is refused"
root=$(fixture job-budget-equal)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 10
    steps:
      - name: Install changie
        timeout-minutes: 11
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "the changie tarball" 90 -- \
            curl -fsSL --max-time 90 -o changie.tar.gz https://example.com/changie.tar.gz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the job at 600 s against a step whose retries take 600 s" \
  "$(contains "$out" "job .build. carries .timeout-minutes: 10., 600 s, and its step .Install changie. holds retries taking 600 s to exhaust, which that budget does not strictly exceed")"
check "says what to write on the job" "$(contains "$out" 'timeout-minutes: 11. or more on this job')"

# The job half of the same rule, which the clause above cannot reach: a step whose retries fit
# inside its own budget is still cut by a job ceiling that is not above that budget, because the
# job holds the step AND everything else it runs. Twenty-two job keys of this repository stood
# below their own documented derivation while this check was green (dispatch://INFRA-1545,
# round 27), every one of them past the clause above.
echo "case: a job whose budget equals its slowest step's own budget is refused"
root=$(fixture job-holds-step)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 8
    steps:
      - name: Install changie
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "the changie tarball" 60 -- \
            curl -fsSL --max-time 60 -o changie.tar.gz https://example.com/changie.tar.gz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the job's budget and the step's own" \
  "$(contains "$out" "and its step .Install changie. carries .timeout-minutes: 8.")"
check "says the job budget does not strictly exceed it" \
  "$(contains "$out" "which that job budget does not strictly exceed")"
check "says what to write on the job" "$(contains "$out" 'timeout-minutes: 9. or more on this job')"
check "does not also report the step's own retries against this job" \
  "$(is "$(printf '%s\n' "$out" | grep -c 'holds retries taking')" 0)"

# The job-derivation clause, which reads a job key against THE SUM ITS OWN COMMENT STATES and
# recomputes the second term from this check's own step arithmetic. Its fixtures are the shapes
# that earned it: a comment naming a step that stopped being the slowest (eight jobs of this
# repository, dispatch://INFRA-1545 round 28), a key under the sum its own comment states
# (`download-retry`, 221 minutes against its own 14322 s), and the job it must NOT refuse — one
# whose comment derives its key by a construction rather than by a sum, which `tool-fallback`
# does and which a clause that assumed first-plus-slowest would have refused.
echo "case: a job whose comment names a step that is no longer its slowest is refused"
root=$(fixture job-derivation)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus its slowest retried step alone — the small fetch,
    # 450 s. 1200 + 450 = 1650 s -> 28 min.
    timeout-minutes: 28
    steps:
      - name: The small fetch
        timeout-minutes: 9
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the sum the comment states" \
  "$(contains "$out" "its own comment derives 1200 + 450 = 1650 s")"
check "names the step that is really the slowest, with this check's own figure" \
  "$(contains "$out" "slowest retried step of this job is .The large fetch., whose retries take 1050 s")"
check "says the sum this check computes" "$(contains "$out" "1200 + 1050 = 2250 s")"
check "says what the key must be" "$(contains "$out" 'timeout-minutes: 38. or more')"

echo "case: the same job with the derivation and the key corrected passes"
root=$(fixture job-derivation-fixed)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus its slowest retried step alone — the large fetch,
    # 1050 s. 1200 + 1050 = 2250 s -> 38 min.
    timeout-minutes: 38
    steps:
      - name: The small fetch
        timeout-minutes: 9
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a job whose key is under the sum its own comment states is refused"
root=$(fixture job-derivation-half-a)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus its slowest retried step alone — the large fetch,
    # 1050 s. 1200 + 1050 = 2250 s -> 30 min.
    timeout-minutes: 30
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the sum and says the key does not strictly exceed it" \
  "$(contains "$out" "its own comment derives 1200 + 1050 = 2250 s, which that key does not strictly exceed")"
check "says what to write" "$(contains "$out" 'timeout-minutes: 38. or more')"

# Rule 8's `<=` at the derivation clause's two halves: a key EXACTLY at the sum its comment
# states, and a key exactly at the sum this check recomputes. The fixtures above use
# 1200 + 1050 = 2250 s, which is 37.5 minutes, where `<` and `<=` answer alike, so a revert of
# either comparison left every case green (dispatch://INFRA-1545, round 31). 1350 + 1050 =
# 2400 s is 40 minutes exactly, the shape of the eleven keys round 29 raised.
echo "case: a job whose key equals the whole-minute sum its own comment states is refused"
root=$(fixture job-derivation-equal)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1350 s measured, plus its slowest retried step alone — the large fetch,
    # 1050 s. 1350 + 1050 = 2400 s -> 40 min.
    timeout-minutes: 40
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the sum and says the key does not strictly exceed it" \
  "$(contains "$out" "its own comment derives 1350 + 1050 = 2400 s, which that key does not strictly exceed")"
check "says what to write" "$(contains "$out" 'timeout-minutes: 41. or more')"

echo "case: a job whose key equals the whole-minute sum this check recomputes is refused"
root=$(fixture job-derivation-recomputed-equal)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1350 s measured, plus its slowest retried step alone — the small fetch,
    # 450 s. 1350 + 450 = 1800 s -> 40 min.
    timeout-minutes: 40
    steps:
      - name: The small fetch
        timeout-minutes: 9
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the recomputed sum and says the key does not strictly exceed it" \
  "$(contains "$out" "1350 + 1050 = 2400 s, which this key does not strictly exceed")"
check "says what the key must be" "$(contains "$out" 'timeout-minutes: 41. or more')"

# The derivation clause reads the sum whose arrow names the job's own key, and no other sum of
# its block. A block may derive the job and a step together, and a reviewer made the clause take
# the block's last sum whatever its arrow names (`if True:` in place of the arrow's test against
# the key): it read the step's 1663 s, under the job's 2280 s, and the job's own 2300 s, which
# 38 minutes does not exceed, went unread (dispatch://INFRA-1545, review round 28).
echo "case: of two derivations in a block, the one whose arrow names the key is read"
root=$(fixture job-derivation-arrow-names-key)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 650 + 1650 = 2300 s -> 38 min. The step: 13 + 1650 = 1663 s -> 28 min.
    timeout-minutes: 38
    steps:
      - name: The browser download
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a browser" 300 -- curl -fsSL --max-time 300 -o c https://example.com/c
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "reads the sum whose arrow is the job's key" \
  "$(contains "$out" "job 'build' carries .timeout-minutes: 38., 2280 s, and its own comment derives 650 + 1650 = 2300 s, which that key does not strictly exceed")"
check "says what to write" "$(contains "$out" 'timeout-minutes: 39. or more')"

# Of two sums whose arrows both name the key, the clause reads the last, the one written nearest
# the key. A `break` after the first match made it read the first, 2250 s, and the job went
# through under its second, 2350 s (dispatch://INFRA-1545, review round 28).
echo "case: of two derivations naming the key, the one nearest the key is read"
root=$(fixture job-derivation-last-of-two)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget, as first measured: 600 + 1650 = 2250 s -> 38 min.
    # As measured since the checkout slowed: 700 + 1650 = 2350 s -> 38 min.
    timeout-minutes: 38
    steps:
      - name: The browser download
        # The browser download's 5 x 300 + 150 = 1650 s -> 28 min.
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a browser" 300 -- curl -fsSL --max-time 300 -o c https://example.com/c
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "reads the second sum" \
  "$(contains "$out" "its own comment derives 700 + 1650 = 2350 s, which that key does not strictly exceed")"
check "says what to write" "$(contains "$out" 'timeout-minutes: 40. or more')"

# The clause reads its block upwards from the key and joins it back in reading order, so a sum
# split over two lines reads as one. Joined in the order the walk collects it, the block below
# reads `1750 s -> 28 min. 700 + 1050 =`, which is no derivation, and a key under its own sum
# went through (dispatch://INFRA-1545, review round 28, the mutation that dropped `reversed`).
echo "case: a derivation split over two comment lines is read in order"
root=$(fixture job-derivation-split)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 700 s measured, plus the large fetch's 1050 s:
    # 700 + 1050 =
    # 1750 s -> 28 min.
    timeout-minutes: 28
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "reads the sum the two lines state" \
  "$(contains "$out" "its own comment derives 700 + 1050 = 1750 s, which that key does not strictly exceed")"
check "says what to write" "$(contains "$out" 'timeout-minutes: 30. or more')"

# The stand-aside, which is the clause's whole design: `tool-fallback` of this repository states
# `114 is the 104 this construction gives with ten minutes added`, which .github/ci-budgets.md
# holds for that job, and carries a position rule,
# so no `<a> + <b> = <c> s -> <key> min` ties a sum to its key, and a clause that refused it
# would be a correct job refused — which is how a check gets silenced.
# The arrow rule, which is this issue's own control moved into the check: a key lowered while
# its comment is left alone. Before it, each of the nine keys dispatch://INFRA-1545 raised could
# be put back with its sentence untouched and this check stayed green, because the derivation
# clause below finds no sum once the arrow and the key disagree, and stands aside.
echo "case: a key lowered while its comment is left alone is refused"
root=$(fixture arrow-against-key)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus its slowest retried step alone — the large fetch,
    # 1050 s. 1200 + 1050 = 2250 s -> 38 min.
    timeout-minutes: 30
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the figure the comment prices and the key beneath it" \
  "$(contains "$out" "this comment prices what it sits above at 38 min, and the .timeout-minutes: 30. below it, the key of job 'build', carries 30")"
check "points at the comment's first line, where a reader rewrites the sentence" \
  "$(contains "$out" "file=.github/workflows/case.yaml,line=6::this comment prices")"

echo "case: a comment block pricing two steps keeps both keys"
root=$(fixture arrow-two-steps)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  # Budget: 1350 s measured, plus the large fetch's 5 x 180 + 150 = 1050 s -> 40 min.
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 40
    steps:
      # Two steps of this job, at the bounds their own arithmetic gives: the large fetch's
      # 5 x 180 + 150 = 1050 s -> 18 min, and the small fetch's 5 x 60 + 150 = 450 s -> 8 min.
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
      - name: The small fetch
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# KEY_LINE reads a key line to its end. `timeout-minutes: 28 junk` is no key: YAML reads its value
# as the string `28 junk`, which written_minutes takes as no key at all, so the job below writes
# none and no clause holding a job's key reads it. With the `$` dropped from the pattern, the line
# read as key 28, and the block above it was refused for pricing 30 against a key YAML never
# reads (dispatch://INFRA-1545, review round 28).
echo "case: a timeout-minutes line with words after its number is no key a block prices"
root=$(fixture arrow-over-string-key)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: the large fetch's 1050 s and the steps beside it -> 30 min.
    timeout-minutes: 28 junk
    steps:
      - name: The large fetch
        # The large fetch's 5 x 180 + 150 = 1050 s -> 18 min.
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "pairs no key 28 with the block" "$(is "$(contains "$out" 'this comment prices')" false)"

# A key line may carry a YAML comment after its number, and its key is that number, as YAML reads
# it. Without the pattern's `\s+#.*` alternative such a line read as no key, so the job below
# priced nothing and was refused (dispatch://INFRA-1545, review round 28).
echo "case: a key carrying a trailing comment is the key its block prices"
root=$(fixture arrow-key-trailing-comment)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus the large fetch's 1050 s: 1200 + 1050 = 2250 s -> 38 min.
    timeout-minutes: 38  # the sum above
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# The same key lowered to 37 with its arrow, the trailing comment kept: the block still prices the
# key, and the derivation clause reads the 37 YAML reads and refuses it under its 2250 s.
echo "case: the same key lowered under its derivation, its trailing comment kept, is refused"
root=$(fixture arrow-key-trailing-comment-lowered)
sed 's/-> 38 min/-> 37 min/; s/^    timeout-minutes: 38  # the sum above$/    timeout-minutes: 37  # the sum above/' \
  "$work/arrow-key-trailing-comment/.github/workflows/case.yaml" > "$root/.github/workflows/case.yaml"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "the derivation clause refuses the key it reads" \
  "$(contains "$out" "job 'build' carries .timeout-minutes: 37., 2220 s, and its own comment derives 1200 + 1050 = 2250 s, which that key does not strictly exceed")"
check "and the block still prices that key" \
  "$(is "$(contains "$out" 'no comment block prices that key')" false)"

# Each line of a block loses its indentation before its `#`, so an arrow split over two indented
# lines joins as `-> 38 min`. With the `#` stripped alone, the indented second line kept its `#`
# and joined as `-> # 38 min`, which prices nothing, and the job was refused
# (dispatch://INFRA-1545, review round 28).
echo "case: an arrow split over two indented comment lines prices the key below"
root=$(fixture arrow-split-indented)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus the large fetch's 1050 s: 1200 + 1050 = 2250 s ->
    # 38 min.
    timeout-minutes: 38
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# A block's keys are read down to the next comment line, so a block above `runs-on:` never reaches
# past the job's own priced block to its key. Without that stop the first block below took 38 as
# its own key and refused it against its 30 (dispatch://INFRA-1545, review round 28).
echo "case: a block's reach ends at the next comment block"
root=$(fixture arrow-ends-at-next-block)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    # The runner queue's own wait, priced apart from this job -> 30 min.
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus the large fetch's 1050 s: 1200 + 1050 = 2250 s -> 38 min.
    timeout-minutes: 38
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# EVERY CLAIM THIS RULE'S HEADER MAKES ABOUT WHAT IT REFUSES HAS A CASE HERE THAT FAILS WHEN
# THAT BEHAVIOUR IS REMOVED. The claims were written from memory of the change three rounds
# running and were wrong each time: a reviewer turned `<=` back into `<` and the gate and all
# 299 cases stayed green, so the eleven keys that operator raised were guarded by nothing
# (dispatch://INFRA-1545, review round 26). A claim with no case behind it is a sentence.
echo "case: a job key equal to the sum its own comment states is refused"
root=$(fixture job-key-equals-sum)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1170 s measured, plus its slowest retried step alone — the large fetch,
    # 1050 s. 1170 + 1050 = 2220 s -> 37 min.
    timeout-minutes: 37
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "says the key does not strictly exceed the sum" \
  "$(contains "$out" "its own comment derives 1170 + 1050 = 2220 s, which that key does not strictly exceed")"
# 37 minutes is 2220 s, the sum to the second, and 38 minutes is 2280 s, a minute above it. The
# case above fails when `allowed <= total` becomes `allowed < total`: the key is then refused by
# the recomputed sum's sentence and not by the stated sum's, which it pins. The case below fails
# when the refusal reaches a key above the sum. This case stood at 1200 + 1050 = 2250 s against
# 37 minutes, under the sum and not at it, so it stayed green under the first of those mutations
# (dispatch://INFRA-1545, review round 28).
echo "case: one minute above that sum passes"
root=$(fixture job-key-above-sum)
sed 's/-> 37 min/-> 38 min/; s/^    timeout-minutes: 37$/    timeout-minutes: 38/' \
  "$work/job-key-equals-sum/.github/workflows/case.yaml" > "$root/.github/workflows/case.yaml"
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a job holding a retried step whose comments price nothing is refused"
root=$(fixture job-prices-nothing)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus the large fetch's 1050 s, which comes to 2250 s and takes
    # the whole minute above it: 38.
    timeout-minutes: 38
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the retried step it holds" "$(contains "$out" "holds a retried step")"
check "says no comment block prices that key" \
  "$(contains "$out" "no comment block prices that key")"
check "names the two clauses that read a price, by what they are and where" \
  "$(contains "$out" "rule 8's priced-comment and job-derivation clauses in .github/scripts/check-download-retry.sh")"
check "says what to write" "$(contains "$out" 'as .-> N min. in a comment block inside this job')"

# PRICED reads an arrow only where `min` follows its number: `-> 38.` names no unit, and the
# pricing clause asks for `-> N min`. With `min` dropped from the pattern, the job below went
# through (dispatch://INFRA-1545, review round 28).
echo "case: an arrow with no unit prices nothing"
root=$(fixture arrow-without-min)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus the large fetch's 1050 s: 1200 + 1050 = 2250 s -> 38.
    timeout-minutes: 38
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the key" "$(contains "$out" "job 'build' carries .timeout-minutes: 38. and holds a retried step")"
check "says no comment block prices that key" \
  "$(contains "$out" "no comment block prices that key")"

echo "case: the same job priced as -> N min passes, wherever the block sits"
root=$(fixture job-priced-above-its-id)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  # Budget: 1200 s measured, plus the large fetch's 1050 s: 1200 + 1050 = 2250 s -> 38 min.
  # This block sits above the job's id rather than above its key, which is where two jobs of
  # this repository state their sums.
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 38
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# The pricing refusal's advice is the pairing rule in words: a block above the job's id prices
# the key only with no comment line between the two. A note above the key ends the block's reach,
# so the job below is refused, and the refusal says what stands in the way; the same job without
# the note passes (dispatch://INFRA-1545, review round 28).
echo "case: a note between a job's priced block and its key is refused, and told why"
root=$(fixture job-priced-note-between)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  # Budget: 600 s measured, plus the browser download's 1650 s: 600 + 1650 = 2250 s -> 38 min.
  build:
    runs-on: ubuntu-24.04
    # A note about the runner.
    timeout-minutes: 38
    steps:
      - name: The browser download
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a browser" 300 -- curl -fsSL --max-time 300 -o c https://example.com/c
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the key" "$(contains "$out" "job 'build' carries .timeout-minutes: 38. and holds a retried step")"
check "says no comment line may stand between the block and the key" \
  "$(contains "$out" "with no comment line and no other .timeout-minutes. between that block and this key")"

echo "case: the same job with the note removed passes"
root=$(fixture job-priced-note-removed)
sed '/# A note about the runner\./d' \
  "$work/job-priced-note-between/.github/workflows/case.yaml" > "$root/.github/workflows/case.yaml"
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# What "a job prices itself" means is what the priced-comment clause pairs: a block's arrow goes
# to the keys below it, one per figure, so an arrow belongs to the key it sits above and never to
# a job further down. A 40-line window stood here before, and a reviewer lowered `typecheck` to
# 20 minutes — 1200 s under its own 1650 s — with its derivation written in words: the gate
# stayed green, because `tool-fallback-arm`'s step comment 28 lines up carried `-> 22 min`
# (dispatch://INFRA-1545, review round 27). This case is that shape.
echo "case: a job priced only by a neighbouring job's arrow is refused"
root=$(fixture job-priced-by-neighbour)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  arm:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus the small fetch's 450 s: 1200 + 450 = 1650 s -> 28 min.
    timeout-minutes: 28
    steps:
      # The small fetch's 5 x 60 + 150 = 450 s, the whole minute above it (ci-budgets.md's
      # Budgets note) -> 8 min.
      - name: The small fetch
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
  typecheck:
    runs-on: ubuntu-24.04
    # Budget: 600 s measured, plus the large fetch's 1050 s: 600 + 1050 = 1650 s, so the key
    # takes the whole minute above it: 28.
    timeout-minutes: 20
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses typecheck's key, which no block prices" \
  "$(contains "$out" "line=20::job 'typecheck' carries .timeout-minutes: 20. and holds a retried step")"
check "does not refuse the job whose own step comment it is" \
  "$(is "$(printf '%s\n' "$out" | grep -c "job 'arm'")" 0)"

# The walk stops at another job's id. Job `a`'s step comment prices a step that writes no key, so
# with that stop removed it reached job `b`'s key, priced it, and `b` went through with no comment
# of its own (dispatch://INFRA-1545, review round 28).
echo "case: one job's step comment does not price the next job's key"
root=$(fixture job-id-ends-step-comment)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  a:
    runs-on: ubuntu-24.04
    steps:
      # Prose for this step, priced -> 29 min.
      - name: Say hello
        run: echo hello
  b:
    runs-on: ubuntu-24.04
    timeout-minutes: 29
    steps:
      - name: The browser download
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a browser" 300 -- curl -fsSL --max-time 300 -o c https://example.com/c
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses b, which no block of its own prices" \
  "$(contains "$out" "line=12::job 'b' carries .timeout-minutes: 29. and holds a retried step")"

# The same stop where a step comment's second figure finds no key in its own job: `arm`'s step
# comment prices its step at 8 and a slow mirror's floor at 12, and the walk looking for a key
# for that 12 ends at `typecheck`'s id rather than taking `typecheck`'s 12 (dispatch://INFRA-1545,
# review round 28, the job-id stop removed).
echo "case: a step comment's unpaired figure does not price the next job's key"
root=$(fixture job-id-ends-second-figure)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  arm:
    runs-on: ubuntu-24.04
    steps:
      # The small fetch's 5 x 60 + 150 = 450 s -> 8 min, and the floor a slow mirror takes,
      # 720 s -> 12 min.
      - name: The small fetch
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
  typecheck:
    runs-on: ubuntu-24.04
    timeout-minutes: 12
    steps:
      - name: The other small fetch
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "another small asset" 60 -- curl -fsSL --max-time 60 -o d https://example.com/d
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses typecheck, which no block of its own prices" \
  "$(contains "$out" "line=16::job 'typecheck' carries .timeout-minutes: 12. and holds a retried step")"
check "does not refuse arm, whose step its comment prices" \
  "$(is "$(printf '%s\n' "$out" | grep -c "job 'arm'")" 0)"

# The one id a block passes is its own job's, written directly below a block indented no deeper
# than that id. A comment at step depth at the end of job `a` is `a`'s, though it sits directly
# above `b:`; with the indentation test dropped, it priced `b`'s key (dispatch://INFRA-1545,
# review round 28).
echo "case: a step-depth comment directly above the next job's id does not price that job"
root=$(fixture job-id-deeper-comment)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  a:
    runs-on: ubuntu-24.04
    steps:
      - name: Say hello
        run: echo hello
      # Priced for the job below -> 29 min.
  b:
    runs-on: ubuntu-24.04
    timeout-minutes: 29
    steps:
      - name: The browser download
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a browser" 300 -- curl -fsSL --max-time 300 -o c https://example.com/c
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses b, which no block of its own prices" \
  "$(contains "$out" "line=12::job 'b' carries .timeout-minutes: 29. and holds a retried step")"

# The one id a block passes is written on the line directly below the block's last line. A file's
# header comment sits at column 0, no deeper than the first job's id, so with `below == number`
# dropped from the job-id stop the header's `-> 30 min` priced the first job's key, though `name:`,
# `on:` and `jobs:` stand between the two. Review round 29 of dispatch://INFRA-1545 found that
# mutant, and in round 36 the harness still passed 469 of 469 checks under it.
echo "case: a file's header comment does not price the first job's key"
root=$(fixture job-id-file-header)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
# The nightly run -> 30 min.
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 30
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses build, which no block of its own prices" \
  "$(contains "$out" "line=7::job 'build' carries .timeout-minutes: 30. and holds a retried step")"
check "says no comment block prices that key" \
  "$(contains "$out" "no comment block prices that key")"

# A key is paired by its file and its line. The two files below put their job keys on the same
# line, and only the first prices its own; compared by the line alone, the pairing the first file
# made passed the second (dispatch://INFRA-1545, review round 28).
echo "case: a key priced in one file does not price the key on the same line of another"
root=$(fixture paired-by-file)
cat > "$root/.github/workflows/priced.yaml" <<'YAML'
name: Priced
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus the large fetch's 1050 s: 1200 + 1050 = 2250 s -> 38 min.
    timeout-minutes: 38
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
cat > "$root/.github/workflows/unpriced.yaml" <<'YAML'
name: Unpriced
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus the large fetch's 1050 s, 2250 s, the minute above: 38.
    timeout-minutes: 38
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the unpriced file's key" \
  "$(contains "$out" "file=.github/workflows/unpriced.yaml,line=7::job 'build' carries .timeout-minutes: 38. and holds a retried step")"
check "and nothing in the priced file" \
  "$(is "$(contains "$out" 'file=.github/workflows/priced.yaml')" false)"

echo "case: a job priced by its own arrow more than forty lines above its key passes"
root=$(fixture job-priced-far-above)
{
  printf 'name: Case\non: [push]\njobs:\n  build:\n    runs-on: ubuntu-24.04\n'
  printf '    # Budget: 1200 s measured, plus the large fetch, 1200 + 1050 = 2250 s -> 38 min.\n'
  for line in $(seq 1 45); do
    printf '    # Line %s of the reasoning behind that sum, which this check never reads.\n' "$line"
  done
  cat <<'YAML'
    timeout-minutes: 38
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
} > "$root/.github/workflows/case.yaml"
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# The same distance made of the job's own YAML rather than of its comment: a block's keys are
# read however many lines below it they sit. A 40-line reach on the walk let the 45 `env:` lines
# below hide the key from its own block, and the case above never measured that distance, since
# its 45 lines belong to the block and the walk starts below them (dispatch://INFRA-1545, review
# round 28).
echo "case: a job priced by its own arrow above forty-five lines of env passes"
root=$(fixture job-priced-far-above-env)
{
  printf 'name: Case\non: [push]\njobs:\n  build:\n    runs-on: ubuntu-24.04\n'
  printf '    # Budget: 1200 s measured, plus the large fetch, 1200 + 1050 = 2250 s -> 38 min.\n'
  printf '    env:\n'
  for line in $(seq 1 45); do
    printf '      SETTING_%s: "%s"\n' "$line" "$line"
  done
  cat <<'YAML'
    timeout-minutes: 38
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
} > "$root/.github/workflows/case.yaml"
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# The pricing clause's guard is `cost <= 0`: only a job whose retried steps cost nothing is passed
# over. A reviewer widened it to `<= 1000` and `download-retry-images`, its arrow written in words
# and its key put back to 95 minutes, went through (review round 27), so a retried job costing a
# few minutes is the case that holds that guard where it is.
echo "case: an unpriced job whose slowest retried step takes 450 s is refused"
root=$(fixture job-prices-nothing-small)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 300 s measured, plus the small fetch's 450 s, which takes the whole minute
    # above 750 s: 13.
    timeout-minutes: 13
    steps:
      - name: The small fetch
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the 450 s step" "$(contains "$out" "whose retries take 450 s to exhaust")"
check "says no comment block prices that key" \
  "$(contains "$out" "no comment block prices that key")"

# The pricing refusal names the job's slowest retried step by the step's own name, which is what
# a reader searches the workflow for. With the job's name put in its place, the refusal below
# named a step 'fetch-assets' that the workflow does not hold (dispatch://INFRA-1545, review
# round 28).
echo "case: an unpriced job's refusal names its slowest step by the step's own name"
root=$(fixture job-prices-nothing-step-name)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  fetch-assets:
    runs-on: ubuntu-24.04
    timeout-minutes: 13
    steps:
      - name: The small fetch
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step, not the job, as the slowest" \
  "$(contains "$out" "job 'fetch-assets' carries .timeout-minutes: 13. and holds a retried step — 'The small fetch', whose retries take 450 s")"

# The priced-comment clause holds a key to BEING ONE OF its block's figures. A reviewer weakened
# `key in figures` to `key >= min(figures)` and `re-record`, whose block prices both a step at 9
# and the job at 69, went through at 50 (review round 27): a key between two figures of its block
# names neither.
echo "case: a key between the two figures its block prices is refused"
root=$(fixture arrow-between-figures)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: the small fetch's 5 x 60 + 150 = 450 s -> 9 min, and the job's 3000 s measured
    # plus that, 3000 + 450 = 3450 s -> 69 min.
    timeout-minutes: 50
    steps:
      - name: The small fetch
        timeout-minutes: 9
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names both figures and the key between them" \
  "$(contains "$out" "this comment prices what it sits above at 9 min and 69 min, and the .timeout-minutes: 50. below it, the key of job 'build', carries 50")"

# The same rule from above: a key over every figure its block prices names none of them. A
# reviewer let any key above the largest figure through (`key > max(figures)`), and a key raised
# while its sentence was left alone, a sentence as stale as one under a lowered key, went through
# (dispatch://INFRA-1545, review round 28).
echo "case: a key above every figure its block prices is refused"
root=$(fixture arrow-under-key)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: the large fetch's 1050 s and the steps beside it -> 28 min.
    timeout-minutes: 40
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the figure and the key above it" \
  "$(contains "$out" "this comment prices what it sits above at 28 min, and the .timeout-minutes: 40. below it, the key of job 'build', carries 40")"

# A block's walk reads one key for each figure it states, so a block pricing two figures holds
# the second key below it too. Stopped after the first key, the walk never read the step's 25
# below a block naming 28 and 18 (dispatch://INFRA-1545, review round 28).
echo "case: a block pricing two figures holds its second key too"
root=$(fixture arrow-second-key)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: the job's measured run and its large fetch -> 28 min, and the fetch's own -> 18 min.
    timeout-minutes: 28
    steps:
      - name: The large fetch
        timeout-minutes: 25
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names both figures and the step's key" \
  "$(contains "$out" "this comment prices what it sits above at 28 min and 18 min, and the .timeout-minutes: 25. below it, the key of step 'The large fetch' of job 'build', carries 25")"

echo "case: a job whose comment derives its key another way is passed over"
root=$(fixture job-derivation-construction)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: NOT first-plus-slowest. The large fetch's own 18 minutes have to finish before
    # this ceiling, and the steps beside it measure 2 minutes, so the limit is those two with
    # room on top: 18 + 2 = 20, written as 30 for the ten minutes this job has always carried
    # -> 30 min.
    timeout-minutes: 30
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: one minute above that step passes, and the step's retries never reach its own budget"
root=$(fixture job-holds-step-headroom)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 0 s measured, plus the changie tarball's 5 x 60 + 150 = 450 s -> 9 min.
    timeout-minutes: 9
    steps:
      - name: Install changie
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "the changie tarball" 60 -- \
            curl -fsSL --max-time 60 -o changie.tar.gz https://example.com/changie.tar.gz
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# A step waits for every pass of every loop around a call, for every call of the function that
# holds it, and for every pass of the loops around those calls — and it waits for all of that
# again in each script it runs inside a loop of its own. Each shape below was a budget this
# check read at a fraction of what the step allows: a loop inside a loop counted once, a
# function called three times counted once, and a script in a three-pass loop counted once.
# The live instance was the required `pr-title` check: check-pr-text.sh makes five `gh api`
# ladders and this check counted three, because the third sits in read_list's page loop and
# the `for ((i = 0; i < ${#lists[@]}; i += 2))` under it calls read_list once for each of the
# three lists.
echo "case: a loop inside a loop multiplies the call by both"
root=$(fixture loop-nested)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read each page of each list
        timeout-minutes: 30
        run: |
          . .github/scripts/ci-retry.sh
          for list in comments reviews; do
            for page in 1 2 3; do
              retry_fetch "page $page of $list" 30 -- \
                gh api "repos/$GITHUB_REPOSITORY/issues/1/$list?page=$page"
            done
          done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts both loops and not the innermost alone" \
  "$(contains "$out" 'retries take 1800 s to exhaust')"
check "names the budget that would hold it" "$(contains "$out" 'timeout-minutes: 31')"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"

echo "case: the same nest under a budget above the figure both loops make passes"
root=$(fixture loop-nested-headroom)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read each page of each list
        timeout-minutes: 31
        run: |
          . .github/scripts/ci-retry.sh
          for list in comments reviews; do
            for page in 1 2 3; do
              retry_fetch "page $page of $list" 30 -- \
                gh api "repos/$GITHUB_REPOSITORY/issues/1/$list?page=$page"
            done
          done
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a function holding a retried call is counted once for each call of it"
root=$(fixture function-called-thrice)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read the three lists
        timeout-minutes: 15
        run: |
          . .github/scripts/ci-retry.sh
          read_list() {
            retry_fetch "the $1 list" 30 -- gh api "repos/$GITHUB_REPOSITORY/issues/1/$1"
          }
          read_list comments
          read_list reactions
          read_list events
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts the call once for each of the three" \
  "$(contains "$out" 'retries take 900 s to exhaust')"
check "names the budget that would hold it" "$(contains "$out" 'timeout-minutes: 16')"

echo "case: the same function under a budget above its three calls passes"
root=$(fixture function-called-thrice-headroom)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read the three lists
        timeout-minutes: 16
        run: |
          . .github/scripts/ci-retry.sh
          read_list() {
            retry_fetch "the $1 list" 30 -- gh api "repos/$GITHUB_REPOSITORY/issues/1/$1"
          }
          read_list comments
          read_list reactions
          read_list events
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# The baseline's ci-ok-reconcile.sh is 3 x (5 x 30 + 150) = 900 s of its own, so a step that
# runs it three times round a loop waits 2700 s for it.
echo "case: a script the step runs inside its own loop is counted once for each pass"
root=$(fixture script-in-loop)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Reconcile the ci-oks of each head the queue left
        timeout-minutes: 45
        run: |
          for head in 1 2 3; do
            .github/scripts/ci-ok-reconcile.sh "$head"
          done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts the script once for each pass of the step's loop" \
  "$(contains "$out" 'retries take 2700 s to exhaust')"
check "names the script the step runs" "$(contains "$out" 'ci-ok-reconcile.sh')"
check "names the budget that would hold it" "$(contains "$out" 'timeout-minutes: 46')"

echo "case: the same script in the same loop under a budget above the three runs passes"
root=$(fixture script-in-loop-headroom)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Reconcile the ci-oks of each head the queue left
        timeout-minutes: 46
        run: |
          for head in 1 2 3; do
            .github/scripts/ci-ok-reconcile.sh "$head"
          done
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# A heredoc body belongs to the command consuming it. A shell runs what it reads on its standard
# input, so the calls a `bash <<EOF` body writes are the step's own and their attempts are the
# step's cost; `cat`, a redirect into a file and a `python3 <<'PY'` take their body as data,
# where a path is a path something prints rather than runs. The baseline's ci-ok-reconcile.sh is
# 900 s of its own, which is the figure every case below turns on.
echo "case: a script called inside a bash heredoc is the step's own arithmetic"
root=$(fixture heredoc-bash)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Reconcile the ci-oks the queue left
        timeout-minutes: 15
        run: |
          bash <<'EOF'
          .github/scripts/ci-ok-reconcile.sh
          EOF
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts the call the body makes" "$(contains "$out" 'retries take 900 s to exhaust')"
check "names the script that body runs" \
  "$(contains "$out" 'that of .github/scripts/ci-ok-reconcile.sh, which it runs')"
check "names the budget that would hold it" "$(contains "$out" 'timeout-minutes: 16')"

echo "case: the same body under a budget above the call it makes passes"
root=$(fixture heredoc-bash-headroom)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Reconcile the ci-oks the queue left
        timeout-minutes: 16
        run: |
          bash <<'EOF'
          .github/scripts/ci-ok-reconcile.sh
          EOF
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: the same call inside a cat heredoc is data and costs the step nothing"
root=$(fixture heredoc-cat)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Write the note that names the script a reader runs
        run: |
          cat > note.txt <<'EOF'
          Run .github/scripts/ci-ok-reconcile.sh once the queue has settled.
          EOF
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "the step holding no call is asked for no budget" \
  "$(is "$(contains "$out" '900 s')" false)"

echo "case: the same call inside a python3 heredoc is data and costs the step nothing"
root=$(fixture heredoc-python)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Print the script a reader runs
        run: |
          python3 <<'PY'
          print("run .github/scripts/ci-ok-reconcile.sh once the queue has settled")
          PY
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "the step holding no call is asked for no budget" \
  "$(is "$(contains "$out" '900 s')" false)"

# However the shell is written, the body is the commands it runs: a bare word, an absolute path,
# options of its own, a `-s` that says the script is standard input, and a `-` written for the
# script argument, which is standard input spelled out.
echo "case: every spelling of a shell reads its body as the step's commands"
root=$(fixture heredoc-interpreters)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: A shell written as an absolute path
        timeout-minutes: 15
        run: |
          /bin/bash <<'EOF'
          .github/scripts/ci-ok-reconcile.sh
          EOF
      - name: A shell carrying options of its own
        timeout-minutes: 15
        run: |
          sudo bash -euo pipefail <<'EOF'
          .github/scripts/ci-ok-reconcile.sh
          EOF
      - name: A shell told to read its script from standard input
        timeout-minutes: 15
        run: |
          bash -s stage1 <<'EOF'
          .github/scripts/ci-ok-reconcile.sh
          EOF
      - name: A dash written for the script argument
        timeout-minutes: 15
        run: |
          sh - <<'EOF'
          .github/scripts/ci-ok-reconcile.sh
          EOF
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "the absolute path runs its body" \
  "$(contains "$out" "step .A shell written as an absolute path. carries .timeout-minutes: 15")"
check "the shell carrying options runs its body" \
  "$(contains "$out" "step .A shell carrying options of its own. carries .timeout-minutes: 15")"
check "the shell reading standard input runs its body" \
  "$(contains "$out" "step .A shell told to read its script from standard input. carries")"
check "the dash for the script argument runs its body" \
  "$(contains "$out" "step .A dash written for the script argument. carries")"
check "each of the four is one refusal" \
  "$(contains "$out" '4 download(s) outside the one retry rule')"

# An interpreter whose script is somewhere else reads its body as that script's input, which is
# the shell's own rule: the word behind `-c` is the script, and so is a file named on the line.
echo "case: an interpreter whose body is another script's input is data"
root=$(fixture heredoc-interpreter-input)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: A script behind -c, whose body is that script's input
        run: |
          bash -c 'cat > note.txt' <<'EOF'
          .github/scripts/ci-ok-reconcile.sh
          EOF
      - name: A script file, whose body is that file's input
        run: |
          bash tools/note.sh <<'EOF'
          .github/scripts/ci-ok-reconcile.sh
          EOF
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "neither step is asked for a budget" "$(is "$(contains "$out" '900 s')" false)"

echo "case: a data heredoc inside a shell's heredoc is still data"
root=$(fixture heredoc-nested)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Reconcile the ci-oks and leave a note naming the other script
        timeout-minutes: 15
        run: |
          bash <<'OUTER'
          cat > note.txt <<'INNER'
          Read .github/scripts/check-pr-text.sh for the page this reconcile skipped.
          INNER
          .github/scripts/ci-ok-reconcile.sh
          OUTER
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "the outer body is commands and the inner one is not" \
  "$(contains "$out" 'retries take 900 s to exhaust')"
check "the script the inner body names is no call of this step" \
  "$(is "$(contains "$out" 'check-pr-text.sh')" false)"

# A declaration writes one number, so it answers for one loop. A second loop of the same file,
# step and verb would take that number without anybody writing it one, which is the undercount
# the declaration exists to stop.
echo "case: a declaration that would cover two loops fails"
root=$(fixture declaration-two-loops)
cat > "$root/.github/scripts/ci-ok-reconcile.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
while read -r id status was marker; do
  retry_fetch "the PATCH of ci-ok check run $id" 30 -- \
    gh api --silent --method PATCH "repos/$GITHUB_REPOSITORY/check-runs/$id" --input "$body"
done <<<"$runs"
while read -r id; do
  retry_fetch "the ci-ok of run $id" 30 -- \
    gh api "repos/$GITHUB_REPOSITORY/check-runs/$id"
done <<<"$others"
SH
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the entry and the two loops it covers" \
  "$(contains "$out" 'the declared loop for .github/scripts/ci-ok-reconcile.sh .gh api. covers the loops at lines 4, 8')"
check "says what the entry was written for" "$(contains "$out" 'This entry said 900 s')"

# Rule 8 reads a curl's own attempts off the ladder rule 1 holds it to, not off a fixed eight:
# `--retry 10` is eleven attempts at `--max-time` with 1, 2, 4, …, 512 s between them, so
# 11 x 30 + 1023 = 1353 s.
echo "case: a curl's own attempts are counted at the ladder its options write"
root=$(fixture curl-ladder-budget)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install crane
        timeout-minutes: 22
        run: |
          curl -fsSL --retry 10 --retry-all-errors --max-time 30 -o crane.tar.gz \
            https://example.com/crane.tar.gz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts the eleven attempts and curl's own waits" \
  "$(contains "$out" 'retries take 1353 s to exhaust')"
check "names the budget that would hold it" "$(contains "$out" 'timeout-minutes: 23')"

echo "case: the same curl under a budget above its ladder passes"
root=$(fixture curl-ladder-budget-headroom)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install crane
        timeout-minutes: 23
        run: |
          curl -fsSL --retry 10 --retry-all-errors --max-time 30 -o crane.tar.gz \
            https://example.com/crane.tar.gz
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: an argument this check does not take"
root=$(fixture bad-argument)
run_check "$root" --everything
check "exits 2" "$(is "$status" 2)"
check "says what it takes" "$(contains "$out" 'this check takes no argument')"

# Rule 9. A workflow `run:` that names no shell runs under GitHub's `/usr/bin/bash -e {0}`:
# errexit on, pipefail off, so a pipeline's status is its last command's and a fetch left of a
# `|` can fail with the step exiting 0. The retry around such a pipeline sees that 0 and never
# retries, which is the hole these cases close.
echo "case: a retried fetch left of a pipe in a workflow run"
root=$(fixture piped-retry)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install changie
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a tarball" 60 -- curl -fsSL https://example.com/changie.tar.gz | tar -xz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the file, the line and the step" \
  "$(contains "$out" 'file=.github/workflows/case.yaml,line=10::.curl. in step .Install changie. is not the last command of its pipeline')"
check "says whose status the step reads" \
  "$(contains "$out" "a pipeline's status is its last command's alone")"
check "says the retry around it never retries" "$(contains "$out" 'never retries')"
check "names the cure" "$(contains "$out" 'write the pipeline as two commands')"
check "counts the one refusal" "$(contains "$out" '1 download(s) outside the one retry rule')"

echo "case: a bare fetch left of a pipe is refused for the pipeline as well as the floor"
root=$(fixture piped-bare-curl)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install changie
        run: |
          curl -fsSL https://example.com/changie.tar.gz | tar -xz -C /usr/local/bin changie
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the missing floor" \
  "$(contains "$out" 'file=.github/workflows/case.yaml,line=8::.curl. in step .Install changie. fetches without the floor')"
check "names the pipeline too, not only the missing retry" \
  "$(contains "$out" 'is not the last command of its pipeline')"
check "the two faults of one site are one annotation" \
  "$(contains "$out" '1 download(s) outside the one retry rule')"

echo "case: the same piped fetch in a composite action passes, since its shell sets pipefail"
root=$(fixture piped-action)
mkdir -p "$root/.github/actions/install-thing"
cat > "$root/.github/actions/install-thing/action.yml" <<'YAML'
name: Install the thing
description: Installs the thing through a shell that carries the fetch's failure.
runs:
  using: composite
  steps:
    - name: Install the thing
      shell: bash
      run: |
        . "$GITHUB_ACTION_PATH/../../scripts/ci-retry.sh"
        retry_fetch "a tarball" 60 -- curl -fsSL https://example.com/thing.tar.gz | tar -xz
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a workflow's gh api with a logical OR after it is no pipeline"
root=$(fixture logical-or)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Cancel the runs this push started
        timeout-minutes: 6
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "the force-cancel" 30 -- gh api --silent --method POST "repos/$GITHUB_REPOSITORY/actions/runs/1/force-cancel" ||
            echo "failed"
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a fetch that is the last command of its pipeline passes"
root=$(fixture piped-last)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read what this job downloads
        # Two curls of 8 x 60 + 127 = 607 s.
        timeout-minutes: 21
        run: |
          echo x | xargs curl -fsSL --retry 7 --retry-all-errors --max-time 60 -o out
          cat urls.txt | curl -fsSL --retry 7 --retry-all-errors --max-time 60 -K - -o out2
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
list_check "$root"
check "the curl at the end of its pipeline is a site like any other" \
  "$(contains "$out" '^.github/workflows/case.yaml	11	curl	Read what this job downloads$')"

echo "case: a workflow step that names shell bash carries the fetch's failure and passes"
root=$(fixture piped-step-shell)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install changie
        timeout-minutes: 8
        shell: bash
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a tarball" 60 -- curl -fsSL https://example.com/changie.tar.gz | tar -xz
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a workflow whose defaults.run.shell names bash passes"
root=$(fixture piped-workflow-defaults)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
defaults:
  run:
    shell: bash
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install changie
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a tarball" 60 -- curl -fsSL https://example.com/changie.tar.gz | tar -xz
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: a job whose defaults.run.shell names sh is refused, since sh sets no pipefail"
root=$(fixture piped-job-defaults)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    defaults:
      run:
        shell: sh
    steps:
      - name: Install changie
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a tarball" 60 -- curl -fsSL https://example.com/changie.tar.gz | tar -xz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the file, the line and the step" \
  "$(contains "$out" 'file=.github/workflows/case.yaml,line=13::.curl. in step .Install changie. is not the last command of its pipeline')"

echo "case: a retried fetch left of a pipe that carries standard error too is refused"
root=$(fixture piped-stderr)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Install changie
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a tarball" 60 -- curl -fsSL https://example.com/changie.tar.gz |& tar -xz
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the file, the line and the step" \
  "$(contains "$out" 'file=.github/workflows/case.yaml,line=10::.curl. in step .Install changie. is not the last command of its pipeline')"
check "counts the one refusal" "$(contains "$out" '1 download(s) outside the one retry rule')"

echo "case: a job that compiles Go with no retried module download of its own"
# Rule 11's own fixture, driven both ways: a go command fetches this module's dependencies when
# the cache misses, and that fetch is in none of THE VERBS, so every other rule of this check
# passes the job. Two real jobs stood like this - the browser suite's embedder and the nightly
# re-record - each found by a person reading the job rather than by this check.
root=$(fixture go-build-no-download)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [pull_request]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/cache/restore@55cc8345863c7cc4c66a329aec7e433d2d1c52a9 # v6.1.0
      - name: Build the server
        run: go build -o ./server ./cmd/server
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the job, the step and the file" \
  "$(contains "$out" "file=.github/workflows/case.yaml,line=9::job .build. compiles Go in step .Build the server. and holds no retried")"
check "names the step to write" "$(contains "$out" 'mod download')"
# The same job with the download step the browser suite's jobs carry.
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [pull_request]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/cache/restore@55cc8345863c7cc4c66a329aec7e433d2d1c52a9 # v6.1.0
      - name: Download the Go modules this job builds
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a module's Go modules" 180 -- go -C packages/envoy mod download
      - name: Build the server
        run: go build -o ./server ./cmd/server
YAML
run_check "$root"
check "passes once the job downloads its modules in a step of its own" "$(is "$status" 0)"
# And a `go test` is read as readily as a `go build`, since both fetch on a cache miss.
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [pull_request]
jobs:
  unit:
    runs-on: ubuntu-24.04
    steps:
      - run: go test ./...
YAML
run_check "$root"
check "reads a go test too" \
  "$(contains "$out" "job .unit. compiles Go in step")"

echo "case: rule 11's exemption holds a job that compiles Go and stops holding a renamed one"
# The exemption path and its staleness guard, which the rule's own fixture above does not reach:
# the fixture's ci-job-airlock-lean.yaml carries `airlock-lean`, so a tree whose GO_BUILD_EXEMPT names it passes
# with that job compiling Go and no download, and the same tree with the job renamed is refused
# twice - once for the renamed job, which no entry covers, and once for the entry, which now
# matches no job compiling Go. That second refusal is what a guard that asked only whether the job
# still existed never made: it left a renamed job's exception live for the next job of that name.
root=$(fixture go-exempt)
run_check "$root"
check "the exempted job passes" "$(is "$status" 0)"
check "and no refusal names it" "$(is "$(contains "$out" 'airlock-lean')" false)"

root=$(fixture go-exempt-stale)
python3 - "$root/.github/workflows/ci-job-airlock-lean.yaml" <<'PY_EDIT'
import sys
p = sys.argv[1]
t = open(p).read()
open(p, "w").write(t.replace("  airlock-lean:", "  airlock-lean-renamed:", 1))
PY_EDIT
run_check "$root"
check "a renamed job is refused" "$(is "$status" 1)"
check "the renamed job is named" \
  "$(contains "$out" "job 'airlock-lean-renamed' compiles Go in step")"
check "and its stale exemption is named too" \
  "$(contains "$out" "GO_BUILD_EXEMPT holds 'airlock-lean' of this file")"

echo "case: a workflow whose job writes one key twice"
# GitHub refuses such a file and runs nothing of it, where PyYAML takes the last of the two, so
# this check reads it before any rule below can pass a file CI will not start. Driven both ways:
# the duplicate is refused by name and line, and the same tree with one of them deleted passes.
root=$(fixture duplicate-key)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 22
    timeout-minutes: 75
    steps:
      - run: echo hi
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the key and the second line" \
  "$(contains "$out" 'file=.github/workflows/case.yaml,line=7::`timeout-minutes` is written twice')"
check "says GitHub runs nothing of it" "$(contains "$out" 'runs nothing of it')"

python3 - "$root/.github/workflows/case.yaml" <<'PY_EDIT'
import sys
p = sys.argv[1]
open(p, "w").write(open(p).read().replace("    timeout-minutes: 22\n", "", 1))
PY_EDIT
run_check "$root"
check "and the same tree with one of them gone passes" "$(is "$status" 0)"

# ---------------------------------------------------------------- rule 8's own mutants
# Every case below answers one mutant round 32's enumeration of rule 8 made and the harness
# missed: the code changed, the gate's every refusal on the tree stayed the same, and all 326
# cases passed (dispatch://INFRA-1545, round 32). Each names the behaviour its mutant removed.

# The four walks rule 8 recurses through each stop at a path already read, so a cycle ends and a
# file two calls reach is read once. A cycle is what no case wrote, so the stop could go and every
# case stayed green; without it the check recurses until Python gives up. Since round 37 a script
# that reaches itself and fetches is refused by name as well, each of the pair at its call of the
# other, since the run a path stops at is charged nothing (dispatch://INFRA-1545, round 37); the
# check still ends, with those two refusals and no other.
echo "case: two scripts that call each other are read once each, and the check ends"
root=$(fixture script-cycle)
cat > "$root/.github/scripts/cycle-a.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
.github/scripts/cycle-b.sh
SH
cat > "$root/.github/scripts/cycle-b.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/cycle-a.sh
SH
chmod +x "$root/.github/scripts/cycle-a.sh" "$root/.github/scripts/cycle-b.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch through the pair
        timeout-minutes: 8
        run: .github/scripts/cycle-a.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
for refusal in \
  ".github/scripts/cycle-a.sh	5	.github/scripts/cycle-b.sh" \
  ".github/scripts/cycle-b.sh	3	.github/scripts/cycle-a.sh"; do
  IFS=$'\t' read -r script line target <<<"$refusal"
  check "refuses $script, which reaches itself, at its call of $target" \
    "$(contains "$out" "::error file=$script,line=$line::$script runs or sources $target, and that call reaches $script again")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 2)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"

echo "case: a loop around two scripts that call each other and fetch nothing passes"
root=$(fixture script-cycle-costless)
cat > "$root/.github/scripts/quiet-a.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "$1"
.github/scripts/quiet-b.sh "$1"
SH
cat > "$root/.github/scripts/quiet-b.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/quiet-a.sh "$1"
SH
chmod +x "$root/.github/scripts/quiet-a.sh" "$root/.github/scripts/quiet-b.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Print every name
        timeout-minutes: 5
        run: |
          while read -r name; do
            .github/scripts/quiet-a.sh "$name"
          done <<<"$names"
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"

echo "case: a loop around two scripts that call each other and fetch is refused, and the check ends"
root=$(fixture script-cycle-uncounted)
cat > "$root/.github/scripts/loud-a.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "the asset $1" 60 -- curl -fsSL --max-time 60 -o a "https://example.com/$1"
.github/scripts/loud-b.sh "$1"
SH
cat > "$root/.github/scripts/loud-b.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/loud-a.sh "$1"
SH
chmod +x "$root/.github/scripts/loud-a.sh" "$root/.github/scripts/loud-b.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch every name
        timeout-minutes: 30
        run: |
          while read -r name; do
            .github/scripts/loud-a.sh "$name"
          done <<<"$names"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the loop around the first script" \
  "$(contains "$out" '..github/scripts/loud-a.sh. at .github/workflows/case.yaml:10, inside the loop at line 9')"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"

echo "case: two composite actions that use each other are read once each, and the check ends"
root=$(fixture action-cycle)
mkdir -p "$root/.github/actions/cycle-x" "$root/.github/actions/cycle-y"
cat > "$root/.github/actions/cycle-x/action.yml" <<'YAML'
name: Cycle x
description: Fetches an asset and uses cycle-y.
runs:
  using: composite
  steps:
    - name: Fetch the asset
      shell: bash
      run: |
        curl -fsSL --retry 7 --retry-all-errors --max-time 60 -o x.tar.gz \
          https://example.com/x.tar.gz
    - uses: ./.github/actions/cycle-y
YAML
cat > "$root/.github/actions/cycle-y/action.yml" <<'YAML'
name: Cycle y
description: Uses cycle-x.
runs:
  using: composite
  steps:
    - uses: ./.github/actions/cycle-x
YAML
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Run the pair
        uses: ./.github/actions/cycle-x
        timeout-minutes: 11
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"

# step_name: a `uses:` step with no `name:` reads as '-' in the workflow, so every step refusal
# names the action it runs instead (review round 26). No case wrote such a step until this one.
echo "case: a step with no name is named in a refusal by the action it runs"
root=$(fixture action-unnamed)
mkdir -p "$root/.github/actions/install-elan"
cat > "$root/.github/actions/install-elan/action.yml" <<'YAML'
name: Install elan
description: Installs elan, the Lean toolchain manager.
runs:
  using: composite
  steps:
    - name: Install elan
      shell: bash
      run: |
        curl -fsSL --retry 7 --retry-all-errors --max-time 60 -o elan.tar.gz \
          https://example.com/elan.tar.gz
YAML
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - uses: ./.github/actions/install-elan
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step by the action it runs" \
  "$(contains "$out" "step 'the step running .github/actions/install-elan/action.yml' holds 607 s of retries")"
check "never names it '-'" "$(is "$(contains "$out" "step '-'")" false)"

# The pricing refusal names a job's slowest step as step_name does: a nameless `uses:` step by the
# action it runs. With the bare `budget.step` kept in JOB_SLOWEST in place of step_name, the
# refusal below named a step '-' (dispatch://INFRA-1545, review round 28).
echo "case: an unpriced job's nameless action step is named by the action it runs"
root=$(fixture job-prices-nothing-action-step)
mkdir -p "$root/.github/actions/fetch-asset"
cat > "$root/.github/actions/fetch-asset/action.yml" <<'YAML'
name: Fetch an asset
description: Fetches the asset a job's tests read.
runs:
  using: composite
  steps:
    - name: Fetch the asset
      shell: bash
      run: |
        . .github/scripts/ci-retry.sh
        retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
YAML
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 13
    steps:
      - uses: ./.github/actions/fetch-asset
        timeout-minutes: 8
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the slowest step by the action it runs" \
  "$(contains "$out" "holds a retried step — 'the step running .github/actions/fetch-asset/action.yml', whose retries take 450 s")"

# step_name names a step by an action only where the step runs one. A nameless `run:` step runs
# none, and with `and budget.uses` dropped the refusal below named it 'the step running None'. It
# names the step '-', as the workflow reads it (dispatch://INFRA-1545, review round 28).
echo "case: an unpriced job's nameless run step is named '-', never by an action"
root=$(fixture job-prices-nothing-nameless-run)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 13
    steps:
      - timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step '-'" "$(contains "$out" "holds a retried step — '-', whose retries take 450 s")"
check "never names an action it does not run" "$(is "$(contains "$out" 'None')" false)"

# The slowest step is the first one with the largest arithmetic: a later step that only ties it
# does not take its place, so the refusal names the step written first.
echo "case: of two steps that tie for the slowest, a refusal names the first"
root=$(fixture job-slowest-tie)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus its slowest retried step alone — the small fetch, 450 s.
    # 1200 + 450 = 1650 s -> 30 min.
    timeout-minutes: 30
    steps:
      - name: The first large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
      - name: The second large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "another large asset" 180 -- curl -fsSL --max-time 180 -o c https://example.com/c
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the first of the two" \
  "$(contains "$out" "slowest retried step of this job is 'The first large fetch'")"

# A step holding two loops this check cannot count is told about the first of them, by its figure
# and its name: the refusal's sentence is written for blocking[0], and the list beside it names
# the rest.
echo "case: a step with two uncounted loops is priced at the first loop's call"
root=$(fixture two-uncounted-first-figure)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch every name twice over
        timeout-minutes: 30
        run: |
          . .github/scripts/ci-retry.sh
          while read -r id; do
            retry_fetch "the small $id" 60 -- curl -fsSL --max-time 60 -o a "https://example.com/$id"
          done <<<"$ids"
          while read -r id; do
            retry_fetch "the large $id" 180 -- curl -fsSL --max-time 180 -o b "https://example.com/$id"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "prices one call of the first loop's site" "$(contains "$out" 'One call there takes 450 s')"

echo "case: a step with two uncounted loops whose bounds are unread names the first site"
root=$(fixture two-uncounted-first-unread)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch every name twice over
        timeout-minutes: 30
        run: |
          while read -r id; do
            curl -fsSL --retry 7 --retry-all-errors --max-time "$T" -o a "https://example.com/$id"
          done <<<"$ids"
          . .github/scripts/ci-retry.sh
          while read -r id; do
            retry_fetch "the run $id" "$T" -- gh api "repos/$GITHUB_REPOSITORY/actions/runs/$id"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the first site's verb in the sentence" "$(contains "$out" 'What one call of .curl. there takes')"

# The two loops in the other order: the first loop's call has a bound the check reads and the
# second's is unread. Every blocked-item case above has the unread one first, or both alike,
# so reading `blocking[-1].one` where the gate reads `blocking[0].one` passed the whole harness
# and told the reader that the first call's bound is a variable when it is `--max-time 60`
# (dispatch://INFRA-1545, the tester's mutation pass at 663a6c05a083, mutant T045).
echo "case: a step with two uncounted loops, the first with a read bound, prices the first"
root=$(fixture two-uncounted-first-read)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch every name twice over
        timeout-minutes: 30
        run: |
          while read -r id; do
            curl -fsSL --retry 7 --retry-all-errors --max-time 60 -o a "https://example.com/$id"
          done <<<"$ids"
          . .github/scripts/ci-retry.sh
          while read -r id; do
            retry_fetch "the run $id" "$T" -- gh api "repos/$GITHUB_REPOSITORY/actions/runs/$id"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "prices the first loop's call at its read bound" "$(contains "$out" 'One call there takes 607 s')"
check "does not call the first call's bound a variable" \
  "$(is "$(contains "$out" 'What one call of .curl. there takes is a figure this check cannot make')" false)"

echo "case: a step looping over two scripts, the first with an unread bound, names the first"
root=$(fixture two-uncounted-scripts)
cat > "$root/.github/scripts/early-unread.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
curl -fsSL --retry 7 --retry-all-errors --max-time "$T" -o a "https://example.com/$1"
SH
cat > "$root/.github/scripts/later-numbered.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
curl -fsSL --retry 7 --retry-all-errors --max-time 60 -o a "https://example.com/$1"
SH
chmod +x "$root/.github/scripts/early-unread.sh" "$root/.github/scripts/later-numbered.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch every name through both
        timeout-minutes: 30
        run: |
          while read -r id; do
            .github/scripts/early-unread.sh "$id"
          done <<<"$ids"
          while read -r id; do
            .github/scripts/later-numbered.sh "$id"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the first script and its unread bound" \
  "$(contains "$out" 'One run of ..github/scripts/early-unread.sh. makes attempts whose bound is written as a variable')"

# One refusal per step: a step told its retries sit in a loop nobody can count is told that alone,
# and not also about a second site of it whose bound is unread.
echo "case: a step with an uncounted loop and an unread bound is refused once"
root=$(fixture uncounted-and-unread)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch every name, then one more
        timeout-minutes: 30
        run: |
          . .github/scripts/ci-retry.sh
          while read -r id; do
            retry_fetch "the small $id" 60 -- curl -fsSL --max-time 60 -o a "https://example.com/$id"
          done <<<"$ids"
          curl -fsSL --retry 7 --retry-all-errors --max-time "$T" -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the step for its loop" "$(contains "$out" 'inside the loop at line 10')"
check "and only once" \
  "$(is "$(printf '%s\n' "$out" | grep -c 'this check can put no figure on it')" 1)"

# A step whose own budget is under its retries is told about its own budget first, and not also
# about the job's: the step's budget is the one its author writes next.
echo "case: a step under its own retries is told about its budget, not also its job's"
root=$(fixture step-and-job-under)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: the small fetch's 5 x 60 + 150 = 450 s alone -> 5 min.
    timeout-minutes: 5
    steps:
      - name: The small fetch
        timeout-minutes: 7
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the step's own budget" \
  "$(contains "$out" "step 'The small fetch' carries .timeout-minutes: 7.")"
check "and not the job's for the same retries" \
  "$(is "$(printf '%s\n' "$out" | grep -c "job 'build' carries .timeout-minutes: 5.")" 0)"

echo "case: a job under a step's retries is told about that, not also about the step's budget"
root=$(fixture job-under-step-retries)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: the small fetch's 5 x 60 + 150 = 450 s alone -> 7 min.
    timeout-minutes: 7
    steps:
      - name: The small fetch
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the job against the step's retries" \
  "$(contains "$out" "holds retries taking 450 s to exhaust, which that budget does not strictly exceed")"
check "and not also against the step's own budget" \
  "$(is "$(printf '%s\n' "$out" | grep -c "which that job budget does not strictly exceed")" 0)"

echo "case: a job that writes no timeout-minutes is told GitHub gives it 360 minutes"
root=$(fixture job-default-under-step)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: The small fetch
        timeout-minutes: 400
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "says the job writes none and what GitHub gives it" \
  "$(contains "$out" "job 'build' writes no .timeout-minutes., so GitHub gives it 360 minutes, and its step 'The small fetch' carries .timeout-minutes: 400.")"

# The same default, under a step's retries this time: the refusal prints GitHub's 360 minutes in
# seconds as well, a figure its reader compares with the step's own, so it is pinned exactly.
echo "case: a job that writes no timeout-minutes, under a step's retries, is told the 21600 s it gets"
root=$(fixture job-default-under-retries)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: The large fetch, thirty times
        timeout-minutes: 600
        run: |
          . .github/scripts/ci-retry.sh
          for x in $(seq 1 30); do
            retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
          done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "says what GitHub gives the job, in minutes and in seconds, against the step's retries" \
  "$(contains "$out" "job 'build' writes no .timeout-minutes., so GitHub gives it 360 minutes, 21600 s, and its step 'The large fetch, thirty times' holds retries taking 31500 s to exhaust")"
check "says exactly what to write on the job" "$(contains "$out" 'Write .timeout-minutes: 526. or more on this job\.')"

# The priced-comment clause reads a block to the last line of the file, and a file whose last line
# is a comment with no newline after it ends the block there.
echo "case: a workflow whose last line is a comment with no newline after it passes"
root=$(fixture comment-at-end-of-file)
{
  cat <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus the small fetch's 450 s: 1200 + 450 = 1650 s -> 28 min.
    timeout-minutes: 28
    steps:
      - name: The small fetch
        timeout-minutes: 8
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
YAML
  printf '    # The last line of this file, with no newline after it.'
} > "$root/.github/workflows/case.yaml"
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"

# The derivation clause's header says a sum that does not add up is passed over rather than
# refused, since a clause recomputing a total it cannot trust would be guessing. This is that sum.
echo "case: a derivation whose sum does not add up is passed over"
root=$(fixture job-derivation-bad-sum)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus the small fetch's 450 s. 1200 + 450 = 2500 s -> 40 min.
    timeout-minutes: 40
    steps:
      - name: The small fetch
        timeout-minutes: 9
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# One refusal per job: a key under the sum its own comment states is told that, and not also the
# sum this check recomputes, which the corrected derivation will state anyway.
echo "case: a key under both its stated and its recomputed sum is refused once"
root=$(fixture job-derivation-both-under)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus its slowest retried step alone — the small fetch, 450 s.
    # 1200 + 450 = 1650 s -> 27 min.
    timeout-minutes: 27
    steps:
      - name: The small fetch
        timeout-minutes: 9
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a small asset" 60 -- curl -fsSL --max-time 60 -o a https://example.com/a
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the key under the sum its comment states" \
  "$(contains "$out" "own comment derives 1200 + 450 = 1650 s, which that key does not strictly exceed")"
check "and only once" "$(is "$(printf '%s\n' "$out" | grep -c 'own comment derives')" 1)"

# The pricing clause passes over a job whose steps retry nothing: there is no arithmetic under its
# key for a comment to price, and the tree holds such jobs.
echo "case: a job whose steps retry nothing needs no price"
root=$(fixture job-retries-nothing)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 5
    steps:
      - name: Say hello
        timeout-minutes: 2
        run: echo hello
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# A declaration two loops match is refused for that, once: its figure is wrong for one of the two
# loops whatever it is, so its floor is not told about as well.
echo "case: a declaration covering two loops and under a call's floor is refused once"
root=$(fixture declaration-two-loops-below)
declare_entry "$root" '    DeclaredArithmetic(".github/workflows/case.yaml",
                       "Cancel every run, then read every run", "gh api", 200,
                       "a figure nobody derived"),'
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Cancel every run, then read every run
        timeout-minutes: 20
        run: |
          . .github/scripts/ci-retry.sh
          while read -r id; do
            retry_fetch "the cancel of run $id" 30 -- \
              gh api --silent --method POST "repos/$GITHUB_REPOSITORY/actions/runs/$id/cancel"
          done <<<"$ids"
          while read -r id; do
            retry_fetch "the run $id" 30 -- gh api "repos/$GITHUB_REPOSITORY/actions/runs/$id"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the entry for its two loops" "$(contains "$out" 'covers the loops at lines')"
check "and not also for its floor" "$(is "$(contains "$out" 'writes 200 s, and one call')" false)"

echo "case: a declaration under the floor of two of its calls names the first of them"
root=$(fixture declared-arithmetic-two-floors)
declare_entry "$root" '    DeclaredArithmetic(".github/workflows/case.yaml",
                       "Cancel and read every run the decision named", "gh api", 200,
                       "a figure nobody derived"),'
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Cancel and read every run the decision named
        timeout-minutes: 20
        run: |
          . .github/scripts/ci-retry.sh
          while read -r id; do
            retry_fetch "the cancel of run $id" 30 -- \
              gh api --silent --method POST "repos/$GITHUB_REPOSITORY/actions/runs/$id/cancel"
            retry_fetch "the run $id" 60 -- gh api "repos/$GITHUB_REPOSITORY/actions/runs/$id"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the first call under the floor and its floor" \
  "$(contains "$out" 'one call of that site at .github/workflows/case.yaml:11 takes 300 s')"

# The derivation clause reads the block the priced-comment clause pairs with the key, and a block
# directly above the job's id is one: this job's key, 30 minutes, 1800 s, sits under the 2250 s
# its own derivation states there, and is refused for it. Until round 35 the clause read only the
# comment block directly above the key, this case pinned that limit by passing, and five jobs of
# this repository could be lowered under their sums with their arrows moved and no refusal
# naming them (dispatch://INFRA-1545, round 35).
echo "case: a derivation written above the job's id is read by the derivation clause"
root=$(fixture job-derivation-above-id)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  # Budget: 1200 s measured, plus the large fetch's 1050 s. 1200 + 1050 = 2250 s -> 30 min.
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 30
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the key under the sum its above-id derivation states" \
  "$(contains "$out" "job 'build' carries .timeout-minutes: 30., 1800 s, and its own comment derives 1200 + 1050 = 2250 s, which that key does not strictly exceed")"

# Another of the derivation clause's stated limits, pinned so that reading it starts or stops in
# the harness: a sum with words between it and its arrow. The key sits under the sum it states,
# and passes, because this clause does not read it as a derivation.
echo "case: a derivation with words between its sum and its arrow is not read as one"
root=$(fixture job-derivation-words-between)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus the large fetch's 1050 s. 1200 + 1050 = 2250 s, of which
    # this key carries the measured part -> 30 min.
    timeout-minutes: 30
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# The pricing clause passes over a job only where its retried steps cost nothing at all: a
# declaration answering a site whose bound is unread writes that site's seconds outright, so a
# retried step can cost one second, and its job is still a retried job that must price itself.
echo "case: an unpriced job whose retries cost one declared second is refused"
root=$(fixture job-prices-nothing-one-second)
declare_entry "$root" '    DeclaredArithmetic(".github/workflows/case.yaml",
                       "Read the run", "gh api", 1,
                       "a figure that is the smallest a declaration can write"),'
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 5
    steps:
      - name: Read the run
        timeout-minutes: 1
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "the run" "$T" -- gh api "repos/$GITHUB_REPOSITORY/actions/runs/1"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the job for its one second of retries" \
  "$(contains "$out" "whose retries take 1 s to exhaust — and no comment block prices that key")"

# Each case below answers a mutant round 35's mutation pass of rule 8 made and the harness
# missed: the code changed and all of its cases stayed green (dispatch://INFRA-1545, round 35).
#
# The first four answer a `break` written after the sum in one of the four loops rule 8 adds a
# step's arithmetic up over, so the loop counts its first item alone. On this repository's own
# tree each of them lowered a real step's cost — ci-filters' from 600 to 300 s, test's from 3012
# to 2700 s, pr-title's from 1800 to 1500 s — so a key could sit under its true sum, and nothing
# refused it, because no fixture held such a loop over two items that each cost something. In
# each case below the key is above either item's ladder alone and below the two together, so a
# sum of one item passes it whichever item that is. The scripts are 5 x 30 + 150 = 300 s and
# 5 x 60 + 150 = 450 s, 750 s together, and the actions' curls 8 x 30 + 127 = 367 s and
# 8 x 60 + 127 = 607 s, 974 s together.
echo "case: a step running two retried scripts is held to both of their ladders"
root=$(fixture two-scripts-in-a-step)
cat > "$root/.github/scripts/fetch-small.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a small asset" 30 -- curl -fsSL --max-time 30 -o a https://example.com/a
SH
cat > "$root/.github/scripts/fetch-large.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 60 -- curl -fsSL --max-time 60 -o b https://example.com/b
SH
chmod +x "$root/.github/scripts/fetch-small.sh" "$root/.github/scripts/fetch-large.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch both assets
        timeout-minutes: 12
        run: |
          .github/scripts/fetch-small.sh
          .github/scripts/fetch-large.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts both scripts' ladders" "$(contains "$out" 'its own retries take 750 s to exhaust')"
check "names the budget that would hold them" "$(contains "$out" 'timeout-minutes: 13')"

echo "case: a script running two retried scripts carries both of their ladders"
root=$(fixture two-scripts-in-a-script)
cat > "$root/.github/scripts/fetch-small.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a small asset" 30 -- curl -fsSL --max-time 30 -o a https://example.com/a
SH
cat > "$root/.github/scripts/fetch-large.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 60 -- curl -fsSL --max-time 60 -o b https://example.com/b
SH
cat > "$root/.github/scripts/fetch-both.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/fetch-small.sh
.github/scripts/fetch-large.sh
SH
chmod +x "$root/.github/scripts/fetch-small.sh" "$root/.github/scripts/fetch-large.sh" \
  "$root/.github/scripts/fetch-both.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch both assets
        timeout-minutes: 12
        run: .github/scripts/fetch-both.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts both scripts' ladders" "$(contains "$out" 'its own retries take 750 s to exhaust')"
check "names the budget that would hold them" "$(contains "$out" 'timeout-minutes: 13')"

echo "case: a composite action running two retried scripts carries both of their ladders"
root=$(fixture two-scripts-in-an-action)
cat > "$root/.github/scripts/fetch-small.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a small asset" 30 -- curl -fsSL --max-time 30 -o a https://example.com/a
SH
cat > "$root/.github/scripts/fetch-large.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 60 -- curl -fsSL --max-time 60 -o b https://example.com/b
SH
chmod +x "$root/.github/scripts/fetch-small.sh" "$root/.github/scripts/fetch-large.sh"
mkdir -p "$root/.github/actions/fetch-both"
cat > "$root/.github/actions/fetch-both/action.yml" <<'YAML'
name: Fetch both
description: Fetches both assets through their scripts.
runs:
  using: composite
  steps:
    - name: Fetch both assets
      shell: bash
      run: |
        .github/scripts/fetch-small.sh
        .github/scripts/fetch-large.sh
YAML
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch both assets
        uses: ./.github/actions/fetch-both
        timeout-minutes: 12
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts both scripts' ladders" "$(contains "$out" 'its own retries take 750 s to exhaust')"
check "names the budget that would hold them" "$(contains "$out" 'timeout-minutes: 13')"

echo "case: a composite action using two retried actions carries both of their ladders"
root=$(fixture two-actions-in-an-action)
mkdir -p "$root/.github/actions/fetch-small" "$root/.github/actions/fetch-large" \
  "$root/.github/actions/fetch-both"
cat > "$root/.github/actions/fetch-small/action.yml" <<'YAML'
name: Fetch small
description: Fetches the small asset.
runs:
  using: composite
  steps:
    - name: Fetch the small asset
      shell: bash
      run: curl -fsSL --retry 7 --retry-all-errors --max-time 30 -o a https://example.com/a
YAML
cat > "$root/.github/actions/fetch-large/action.yml" <<'YAML'
name: Fetch large
description: Fetches the large asset.
runs:
  using: composite
  steps:
    - name: Fetch the large asset
      shell: bash
      run: curl -fsSL --retry 7 --retry-all-errors --max-time 60 -o b https://example.com/b
YAML
cat > "$root/.github/actions/fetch-both/action.yml" <<'YAML'
name: Fetch both
description: Fetches both assets through their actions.
runs:
  using: composite
  steps:
    - uses: ./.github/actions/fetch-small
    - uses: ./.github/actions/fetch-large
YAML
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch both assets
        uses: ./.github/actions/fetch-both
        timeout-minutes: 16
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "counts both actions' ladders" "$(contains "$out" 'its own retries take 974 s to exhaust')"
check "names the budget that would hold them" "$(contains "$out" 'timeout-minutes: 17')"

# DERIVATION and PRICED read a sum and an arrow whatever space is written inside them, and every
# derivation and arrow of this tree is written with spaces, so each of the two patterns' `\s*`
# could become `\s+` with every case green (round 35's pass, mutants M265-M273 of DERIVATION and
# M183-M184 of PRICED). A sum written with no space in it is a derivation all the same, and its
# key at that sum is refused by the derivation clause; an arrow written with no space in it
# prices the key below it all the same, and the job it prices passes.
echo "case: a derivation written with no space in it is read, and its key at its sum refused"
root=$(fixture job-derivation-tight)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1170 s measured, plus its slowest retried step alone — the large fetch,
    # 1050 s. 1170+1050=2220s->37min.
    timeout-minutes: 37
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "says the key does not strictly exceed the sum" \
  "$(contains "$out" "its own comment derives 1170 + 1050 = 2220 s, which that key does not strictly exceed")"

echo "case: an arrow written with no space in it prices the key below it"
root=$(fixture arrow-tight)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    # Budget: 1200 s measured, plus the large fetch's 1050 s, which comes to 2250 s and takes
    # the whole minute above it ->38min.
    timeout-minutes: 38
    steps:
      - name: The large fetch
        timeout-minutes: 18
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"

# A script two of a step's runs reach is charged once for every run, whatever the files are
# named. Review round 29 of dispatch://INFRA-1545 drove this input: the step runs zz-a.sh three
# times in a loop and zz-b.sh once, both run zz-c.sh, whose 180 s retry is 5 x 180 + 150 =
# 1050 s, and the step's key is 20 minutes, 1200 s. The step runs zz-c.sh four times, three
# through zz-a.sh and once through zz-b.sh: 4 x 1050 = 4200 s. A walk charging zz-c.sh through
# the first path it visited charged 3150 s here, and with zz-b.sh renamed zz-0.sh, which sorts
# before zz-a.sh, it charged 1050 s and passed the step; the walk after it charged each file
# once at its most expensive path, 3150 s under either name, which was the undercount round 37
# of dispatch://INFRA-1545 corrected. Both names are refused with the same 4200 s.
echo "case: a script two of a step's runs reach is charged once for every run"
root=$(fixture shared-script-most-expensive)
cat > "$root/.github/scripts/zz-c.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
cat > "$root/.github/scripts/zz-a.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/zz-c.sh
SH
cp "$root/.github/scripts/zz-a.sh" "$root/.github/scripts/zz-b.sh"
chmod +x "$root/.github/scripts/zz-a.sh" "$root/.github/scripts/zz-b.sh" \
  "$root/.github/scripts/zz-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch through both scripts
        timeout-minutes: 20
        run: |
          for pass in 1 2 3; do
            .github/scripts/zz-a.sh
          done
          .github/scripts/zz-b.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "charges zz-c.sh once for each of its four runs" \
  "$(contains "$out" 'its own retries take 4200 s to exhaust')"
check "names the budget that would hold them" "$(contains "$out" 'timeout-minutes: 71')"

echo "case: the same step with zz-b.sh renamed zz-0.sh, which sorts first, is charged the same"
root=$(fixture shared-script-renamed)
cp "$work/shared-script-most-expensive/.github/scripts/zz-a.sh" \
  "$work/shared-script-most-expensive/.github/scripts/zz-c.sh" "$root/.github/scripts/"
cp "$work/shared-script-most-expensive/.github/scripts/zz-b.sh" "$root/.github/scripts/zz-0.sh"
chmod +x "$root/.github/scripts/zz-a.sh" "$root/.github/scripts/zz-0.sh" \
  "$root/.github/scripts/zz-c.sh"
sed 's/zz-b\.sh/zz-0.sh/' "$work/shared-script-most-expensive/.github/workflows/case.yaml" \
  > "$root/.github/workflows/case.yaml"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "charges zz-c.sh once for each of its four runs" \
  "$(contains "$out" 'its own retries take 4200 s to exhaust')"
check "names the budget that would hold them" "$(contains "$out" 'timeout-minutes: 71')"

# The loop waits of the path a file is charged through are charged at every call of that path,
# each multiplied by the passes above it, and not at its last call alone. The step runs l-a.sh
# twice with `sleep 120` between the passes, l-a.sh runs l-b.sh three times with `sleep 20`
# between them, and l-b.sh retries at 180 s, 5 x 180 + 150 = 1050 s. The step waits
# 6 x 1050 = 6300 s for the attempts, 120 s in its own loop and 2 x 40 = 80 s in l-a.sh's,
# 6500 s, which `timeout-minutes: 108`, 6480 s, does not exceed. A walk charging the waits of the
# outer call alone (6420 s), of the inner call alone (6380 s), of neither (6300 s), or taking
# them away (6100 s) passed it; round 36's mutation pass of dispatch://INFRA-1545 found all four
# standing.
echo "case: the loop waits along a nested path are charged at every call of it"
root=$(fixture path-waits-nested)
cat > "$root/.github/scripts/l-b.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
cat > "$root/.github/scripts/l-a.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
for pass in 1 2 3; do
  .github/scripts/l-b.sh
  sleep 20
done
SH
chmod +x "$root/.github/scripts/l-a.sh" "$root/.github/scripts/l-b.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch through a nest of loops
        timeout-minutes: 108
        run: |
          for pass in 1 2; do
            .github/scripts/l-a.sh
            sleep 120
          done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "charges both loops' waits along the path, once each" \
  "$(contains "$out" 'its own retries take 6500 s to exhaust')"
check "names the budget that would hold them" "$(contains "$out" 'timeout-minutes: 109')"

# Two paths reaching one file with the same product of loop factors are both charged, each run
# with the waits of the loops around it (dispatch://INFRA-1545, round 37; until then the file
# was charged once, through the path whose loops waited longer). The step runs pa.sh and pz.sh
# ten times and pm.sh twenty times, in loops that do not sleep. pa.sh runs pc1.sh twice with
# `sleep 9`, 2 x 300 + 9 = 609 s, and pm.sh runs it once; pz.sh runs pz2.sh twice with
# `sleep 9` and pz2.sh runs pc2.sh once, 609 s again, while pm.sh reaches pc2.sh through pm2.sh.
# Each fetch retries at 30 s (300 s), so pm.sh is 600 s and the step waits
# 10 x (609 + 609) + 20 x 600 = 24180 s, which `timeout-minutes: 203`, 12180 s, does not exceed.
# Round 36's reading charged each file once, 2 x 20 x 300 + 2 x 90 = 12180 s, half of the runs
# this step makes.
echo "case: of two paths of one product, every run along both is charged"
root=$(fixture path-waits-equal-product)
cat > "$root/.github/scripts/pc1.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a small asset" 30 -- curl -fsSL --max-time 30 -o a https://example.com/a
SH
cp "$root/.github/scripts/pc1.sh" "$root/.github/scripts/pc2.sh"
cat > "$root/.github/scripts/pa.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
for pass in 1 2; do
  .github/scripts/pc1.sh
  sleep 9
done
SH
cat > "$root/.github/scripts/pz.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
for pass in 1 2; do
  .github/scripts/pz2.sh
  sleep 9
done
SH
cat > "$root/.github/scripts/pz2.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/pc2.sh
SH
cat > "$root/.github/scripts/pm.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/pc1.sh
.github/scripts/pm2.sh
SH
cat > "$root/.github/scripts/pm2.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/pc2.sh
SH
chmod +x "$root/.github/scripts/pc1.sh" "$root/.github/scripts/pc2.sh" \
  "$root/.github/scripts/pa.sh" "$root/.github/scripts/pz.sh" "$root/.github/scripts/pz2.sh" \
  "$root/.github/scripts/pm.sh" "$root/.github/scripts/pm2.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch along paths of one product
        timeout-minutes: 203
        run: |
          for pass in $(seq 1 10); do
            .github/scripts/pa.sh
            .github/scripts/pz.sh
          done
          for pass in $(seq 1 20); do
            .github/scripts/pm.sh
          done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "charges every run of each file along both paths, with their waits" \
  "$(contains "$out" 'its own retries take 24180 s to exhaust')"
check "names the budget that would hold them" "$(contains "$out" 'timeout-minutes: 404')"

# Two paths of the same product and the same waits are both charged, and each loop's waits once
# for its own runs (dispatch://INFRA-1545, round 37; until then one of the two was charged, the
# first by name). The step runs t-a.sh twice with `sleep 100`, then t-b.sh twice with
# `sleep 100`. Both run t-c.sh, and t-b.sh runs t-x.sh too, each retrying at 30 s (300 s). So
# the first loop waits 2 x 300 + 100 = 700 s and the second 2 x (300 + 300) + 100 = 1300 s,
# 2000 s, which `timeout-minutes: 23`, 1380 s, does not exceed. Round 36's reading charged t-c.sh
# once, through t-a.sh, 4 x 300 + 200 = 1400 s.
echo "case: of two paths of one product and one wait, each path's runs are charged"
root=$(fixture path-tie-scripts)
cat > "$root/.github/scripts/t-c.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a small asset" 30 -- curl -fsSL --max-time 30 -o a https://example.com/a
SH
cp "$root/.github/scripts/t-c.sh" "$root/.github/scripts/t-x.sh"
cat > "$root/.github/scripts/t-a.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/t-c.sh
SH
cat > "$root/.github/scripts/t-b.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/t-c.sh
.github/scripts/t-x.sh
SH
chmod +x "$root/.github/scripts/t-a.sh" "$root/.github/scripts/t-b.sh" \
  "$root/.github/scripts/t-c.sh" "$root/.github/scripts/t-x.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch along two equal paths
        timeout-minutes: 23
        run: |
          for pass in 1 2; do
            .github/scripts/t-a.sh
            sleep 100
          done
          for pass in 1 2; do
            .github/scripts/t-b.sh
            sleep 100
          done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "charges t-c.sh for its runs along both paths" \
  "$(contains "$out" 'its own retries take 2000 s to exhaust')"
check "names the budget that would hold them" "$(contains "$out" 'timeout-minutes: 34')"

# The same through a composite action, whose steps are shells of their own and whose `uses:`
# is a run of the action it names (dispatch://INFRA-1545, round 37; until then one of the two
# paths was charged, the first by name). The step uses v-top, which runs v-s.sh twice with
# `sleep 100` and then uses v-a. v-s.sh runs v-t.sh and v-x.sh once; v-a runs v-t.sh twice with
# `sleep 100`. So v-top's first step waits 2 x (300 + 300) + 100 = 1300 s and v-a's one step
# 2 x 300 + 100 = 700 s, 2000 s, which `timeout-minutes: 23` does not exceed. Round 36's reading
# charged v-t.sh once, through v-a, 4 x 300 + 200 = 1400 s.
echo "case: of two equal paths through a composite action, each path's runs are charged"
root=$(fixture path-tie-action)
cat > "$root/.github/scripts/v-t.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a small asset" 30 -- curl -fsSL --max-time 30 -o a https://example.com/a
SH
cp "$root/.github/scripts/v-t.sh" "$root/.github/scripts/v-x.sh"
cat > "$root/.github/scripts/v-s.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/v-t.sh
.github/scripts/v-x.sh
SH
chmod +x "$root/.github/scripts/v-s.sh" "$root/.github/scripts/v-t.sh" \
  "$root/.github/scripts/v-x.sh"
mkdir -p "$root/.github/actions/v-top" "$root/.github/actions/v-a"
cat > "$root/.github/actions/v-top/action.yml" <<'YAML'
name: V top
description: Runs v-s.sh in a loop, then uses v-a.
runs:
  using: composite
  steps:
    - name: Run v-s.sh twice
      shell: bash
      run: |
        for pass in 1 2; do
          .github/scripts/v-s.sh
          sleep 100
        done
    - uses: ./.github/actions/v-a
YAML
cat > "$root/.github/actions/v-a/action.yml" <<'YAML'
name: V a
description: Runs v-t.sh in a loop.
runs:
  using: composite
  steps:
    - name: Run v-t.sh twice
      shell: bash
      run: |
        for pass in 1 2; do
          .github/scripts/v-t.sh
          sleep 100
        done
YAML
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch along two equal paths
        uses: ./.github/actions/v-top
        timeout-minutes: 23
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "charges v-t.sh for its runs along both paths" \
  "$(contains "$out" 'its own retries take 2000 s to exhaust')"
check "names the budget that would hold them" "$(contains "$out" 'timeout-minutes: 34')"

# And a path's waits are its own: each loop's waits are charged with that loop's runs and with
# no other's (dispatch://INFRA-1545, round 37; until then one of the two paths was charged, the
# one waiting longer). w-top runs w-s.sh, which runs w-t.sh twice with `sleep 100`, and uses
# w-a, which runs w-t.sh twice with no sleep. The step waits 2 x 300 + 100 = 700 s through
# w-s.sh and 2 x 300 = 600 s through w-a, 1300 s, which `timeout-minutes: 11`, 660 s, does not
# exceed. Round 36's reading charged w-t.sh once, through w-s.sh, 700 s.
echo "case: a path waiting less and a path waiting more are each charged their own waits"
root=$(fixture path-name-not-wait)
cat > "$root/.github/scripts/w-t.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a small asset" 30 -- curl -fsSL --max-time 30 -o a https://example.com/a
SH
cat > "$root/.github/scripts/w-s.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
for pass in 1 2; do
  .github/scripts/w-t.sh
  sleep 100
done
SH
chmod +x "$root/.github/scripts/w-s.sh" "$root/.github/scripts/w-t.sh"
mkdir -p "$root/.github/actions/w-top" "$root/.github/actions/w-a"
cat > "$root/.github/actions/w-top/action.yml" <<'YAML'
name: W top
description: Runs w-s.sh, then uses w-a.
runs:
  using: composite
  steps:
    - name: Run w-s.sh
      shell: bash
      run: .github/scripts/w-s.sh
    - uses: ./.github/actions/w-a
YAML
cat > "$root/.github/actions/w-a/action.yml" <<'YAML'
name: W a
description: Runs w-t.sh in a loop that does not sleep.
runs:
  using: composite
  steps:
    - name: Run w-t.sh twice
      shell: bash
      run: |
        for pass in 1 2; do
          .github/scripts/w-t.sh
        done
YAML
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch along a waiting path and a named one
        uses: ./.github/actions/w-top
        timeout-minutes: 11
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "charges w-t.sh's runs along both paths, with w-s.sh's waits alone" \
  "$(contains "$out" 'its own retries take 1300 s to exhaust')"
check "names the budget that would hold them" "$(contains "$out" 'timeout-minutes: 22')"

# The waits charged are those of the paths charging a file whose own arithmetic is above 0. A
# loop sleeping 100 s between its three passes around q-quiet.sh, which retries nothing, sits
# beside q-fetch.sh's 1050 s, and the step's figure is 1050 s, which `timeout-minutes: 18`,
# 1080 s, exceeds. A walk charging the waits of q-quiet.sh's path as well refused it at 1250 s
# (dispatch://INFRA-1545, round 36).
echo "case: the waits of a loop around a script that retries nothing are not charged"
root=$(fixture path-waits-costless)
cat > "$root/.github/scripts/q-fetch.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
cat > "$root/.github/scripts/q-quiet.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "nothing to fetch"
SH
chmod +x "$root/.github/scripts/q-fetch.sh" "$root/.github/scripts/q-quiet.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Wait around nothing, then fetch
        timeout-minutes: 18
        run: |
          for pass in 1 2 3; do
            .github/scripts/q-quiet.sh
            sleep 100
          done
          .github/scripts/q-fetch.sh
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "does not judge the budget against the quiet loop's waits" \
  "$(is "$(contains "$out" 'does not strictly exceed')" false)"

# A call of a priced script inside a loop whose passes this check cannot read leaves the walk
# with no figure, and the walk still reaches every file beside that call. The step loops,
# uncounted, over r-a.sh; r-a.sh loops, uncounted, over r-b.sh, which retries at 30 s, and then
# runs r-c.sh, which loops, uncounted, over r-b.sh too. One run of r-a.sh has no figure, and the
# refusal says so where a walk going on past the unread call printed `takes 0 s`, the figure of
# what is left; and it names r-c.sh's loop, which a walk stopping at the first unread call never
# reached (dispatch://INFRA-1545, round 36).
echo "case: a script reaching a priced script through an unread loop has no figure, and its other calls are read"
root=$(fixture path-unread)
cat > "$root/.github/scripts/r-b.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a small asset" 30 -- curl -fsSL --max-time 30 -o a https://example.com/a
SH
cat > "$root/.github/scripts/r-a.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
while read -r id; do
  .github/scripts/r-b.sh "$id"
done <<<"$ids"
.github/scripts/r-c.sh
SH
cat > "$root/.github/scripts/r-c.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
while read -r id; do
  .github/scripts/r-b.sh "$id"
done <<<"$more"
SH
chmod +x "$root/.github/scripts/r-a.sh" "$root/.github/scripts/r-b.sh" \
  "$root/.github/scripts/r-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read every run the decision named
        timeout-minutes: 30
        run: |
          while read -r id; do
            .github/scripts/r-a.sh "$id"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "is refused once" \
  "$(is "$(printf '%s\n' "$out" | grep -c 'this check can put no figure on it')" 1)"
check "says one run of r-a.sh reaches an uncounted loop" \
  "$(contains "$out" 'One run of ..github/scripts/r-a.sh. reaches a loop, its own or one in a script it calls')"
check "prints no figure for a cost it cannot compute" \
  "$(is "$(contains "$out" 'takes 0 s')" false)"
check "names r-a.sh's own loop" \
  "$(contains "$out" '..github/scripts/r-b.sh. at .github/scripts/r-a.sh:4, inside the loop at line 3')"
check "names r-c.sh's loop, beside the unread call" \
  "$(contains "$out" '..github/scripts/r-b.sh. at .github/scripts/r-c.sh:4, inside the loop at line 3')"

# A job writing its own `timeout-minutes` around a step with no figure: the step is refused for
# its uncounted loop, and the job's slowest retried step is read from the steps that have a
# figure, of which there is none here. Comparing that step's None with a figure ended the check
# in a TypeError (dispatch://INFRA-1545, round 36).
echo "case: a job whose only retried step has no figure is refused for that step alone"
root=$(fixture job-key-unread-step)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    timeout-minutes: 40
    steps:
      - name: Cancel every run the decision named
        timeout-minutes: 30
        run: |
          . .github/scripts/ci-retry.sh
          while read -r id; do
            retry_fetch "the cancel of run $id" 30 -- \
              gh api --silent --method POST "repos/$GITHUB_REPOSITORY/actions/runs/$id/cancel"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
check "is refused once, for the loop it cannot count" \
  "$(is "$(printf '%s\n' "$out" | grep -c 'this check can put no figure on it')" 1)"
check "names the loop's file and line" \
  "$(contains "$out" '.gh api. at .github/workflows/case.yaml:12, inside the loop at line 11')"

# The tester's input of round 37 (test handoff v51 of dispatch://INFRA-1545, round 37). zz-c.sh
# retries at 180 s, 5 x 180 + 150 = 1050 s; zz-a.sh and zz-b.sh each run it once, and the step
# runs zz-a.sh and then zz-b.sh, so it runs zz-c.sh twice and waits 2 x 1050 = 2100 s. Under
# `timeout-minutes: 30`, 1800 s, the step is refused for those 2100 s; under 36, 2160 s, it
# passes. A walk charging zz-c.sh once, at its most expensive path, read 1050 s and passed both.
echo "case: a step running two scripts that each run one retried script waits for both runs"
root=$(fixture runs-twice-under-30)
cat > "$root/.github/scripts/zz-c.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
cat > "$root/.github/scripts/zz-a.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/zz-c.sh
SH
cp "$root/.github/scripts/zz-a.sh" "$root/.github/scripts/zz-b.sh"
chmod +x "$root/.github/scripts/zz-a.sh" "$root/.github/scripts/zz-b.sh" \
  "$root/.github/scripts/zz-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch through both scripts
        timeout-minutes: 30
        run: |
          .github/scripts/zz-a.sh
          .github/scripts/zz-b.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "charges zz-c.sh once for each of its two runs" \
  "$(contains "$out" "step 'Fetch through both scripts' carries .timeout-minutes: 30., 1800 s, and its own retries take 2100 s to exhaust")"
check "names the budget that would hold them" "$(contains "$out" 'timeout-minutes: 36. or more')"

echo "case: the same step under timeout-minutes: 36 passes"
root=$(fixture runs-twice-under-36)
cp "$work/runs-twice-under-30/.github/scripts/zz-a.sh" \
  "$work/runs-twice-under-30/.github/scripts/zz-b.sh" \
  "$work/runs-twice-under-30/.github/scripts/zz-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/zz-a.sh" "$root/.github/scripts/zz-b.sh" \
  "$root/.github/scripts/zz-c.sh"
sed 's/timeout-minutes: 30/timeout-minutes: 36/' "$work/runs-twice-under-30/.github/workflows/case.yaml" \
  > "$root/.github/workflows/case.yaml"
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "refuses nothing" "$(is "$(contains "$out" '::error')" false)"

# Each spelling a run is written in is a run, charged once for every time the step makes it
# (dispatch://INFRA-1545, round 37): the path after `bash`, after `sh` and an option of its own,
# with a `./` in front of it, as the whole command of a `$(…)`, and behind `timeout 600`. zz-c.sh
# retries at 180 s, 1050 s a run, so the step running it through both shells reads 2100 s and
# each other step 1050 s. Every key is one minute, so each step is refused with its own figure.
echo "case: a run is charged in each spelling it is written in"
root=$(fixture run-spellings)
cp "$work/runs-twice-under-30/.github/scripts/zz-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/zz-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Run through two shells
        timeout-minutes: 1
        run: |
          bash .github/scripts/zz-c.sh
          sh -e .github/scripts/zz-c.sh
      - name: Run by its dotted path
        timeout-minutes: 1
        run: ./.github/scripts/zz-c.sh
      - name: Run inside a command substitution
        timeout-minutes: 1
        run: out=$(.github/scripts/zz-c.sh)
      - name: Run behind timeout
        timeout-minutes: 1
        run: timeout 600 .github/scripts/zz-c.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "charges a run after bash and one after sh -e, 2100 s" \
  "$(contains "$out" "step 'Run through two shells' carries .timeout-minutes: 1., 60 s, and its own retries take 2100 s to exhaust")"
check "charges a run by ./.github/scripts/zz-c.sh, 1050 s" \
  "$(contains "$out" "step 'Run by its dotted path' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
check "charges a run inside \$(…), 1050 s" \
  "$(contains "$out" "step 'Run inside a command substitution' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
check "charges a run behind timeout 600, 1050 s" \
  "$(contains "$out" "step 'Run behind timeout' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
check "refuses none of them as a place it does not read" \
  "$(is "$(contains "$out" 'a place this check does not read as a call')" false)"

# A library is charged once for each shell that sources it, however often that shell reaches it
# (dispatch://INFRA-1545, round 37). zz-lib.sh's own site retries at 180 s, 1050 s; zz-user.sh
# sources it. The first step sources it and then runs zz-user.sh, which sources it again: one
# shell, 1050 s and not 2100 s. zz-one's one step does the same, 1050 s. zz-two sources it in
# each of its two steps, which are two shells, so the step using zz-two reads 2100 s.
echo "case: a library sourced twice in one shell is charged once, and once in each of two shells"
root=$(fixture library-sourced-twice)
cat > "$root/.github/scripts/zz-lib.sh" <<'SH'
#!/usr/bin/env bash
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
cat > "$root/.github/scripts/zz-user.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/zz-lib.sh
SH
chmod +x "$root/.github/scripts/zz-user.sh"
mkdir -p "$root/.github/actions/zz-one" "$root/.github/actions/zz-two"
cat > "$root/.github/actions/zz-one/action.yml" <<'YAML'
name: ZZ one
description: Sources zz-lib.sh and runs a script that sources it again, in one step.
runs:
  using: composite
  steps:
    - name: Source the library and run its user
      shell: bash
      run: |
        . .github/scripts/zz-lib.sh
        .github/scripts/zz-user.sh
YAML
cat > "$root/.github/actions/zz-two/action.yml" <<'YAML'
name: ZZ two
description: Sources zz-lib.sh in each of two steps.
runs:
  using: composite
  steps:
    - name: Source the library
      shell: bash
      run: . .github/scripts/zz-lib.sh
    - name: Source the library again
      shell: bash
      run: . .github/scripts/zz-lib.sh
YAML
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Source the library and run its user
        timeout-minutes: 1
        run: |
          . .github/scripts/zz-lib.sh
          .github/scripts/zz-user.sh
      - name: Use the action sourcing it in one step
        uses: ./.github/actions/zz-one
        timeout-minutes: 1
      - name: Use the action sourcing it in two steps
        uses: ./.github/actions/zz-two
        timeout-minutes: 1
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "charges the library once in a step that sources it and runs a script sourcing it" \
  "$(contains "$out" "step 'Source the library and run its user' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
check "charges it once in an action step doing the same" \
  "$(contains "$out" "step 'Use the action sourcing it in one step' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
check "charges it once in each of an action's two steps" \
  "$(contains "$out" "step 'Use the action sourcing it in two steps' carries .timeout-minutes: 1., 60 s, and its own retries take 2100 s to exhaust")"

# A path a step only prints, searches for or writes in a comment is no call, and is charged
# nothing and refused nothing (dispatch://INFRA-1545, round 37). These are the shapes round 37's
# tester found in this tree, written over zz-c.sh, whose 1050 s would refuse a one-minute key:
# browser-image.yaml's summary line naming a script inside `{ … } >> "$GITHUB_STEP_SUMMARY"`,
# ci.yaml tool-fallback's `::error` line, and ci.yaml's `grep` of a script's name, with a
# `printf`, a comment line and a comment after a command beside them.
echo "case: a script's path in echo, printf, grep or a comment is no call"
root=$(fixture non-calls)
cp "$work/runs-twice-under-30/.github/scripts/zz-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/zz-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Print and search the script's name
        timeout-minutes: 1
        run: |
          {
            echo "Pin a row of .github/ci-images.yaml; .github/scripts/zz-c.sh refuses anything else."
          } >> "$GITHUB_STEP_SUMMARY"
          echo "::error::.github/scripts/zz-c.sh answered nothing, so this step has no bound" >&2
          printf '%s\n' .github/scripts/zz-c.sh
          grep -q .github/scripts/zz-c.sh notes.txt || true
          # .github/scripts/zz-c.sh fetches the asset; this step only names it.
          true # and so does .github/scripts/zz-c.sh
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "neither charges nor refuses the step" "$(is "$(contains "$out" 'case.yaml')" false)"
check "names zz-c.sh nowhere" "$(is "$(contains "$out" 'zz-c.sh')" false)"

# Every other place a path is written in is unread, and naming a script that fetches it is
# refused by name: the file and the line, the step, the script, the form and the rewrite
# (dispatch://INFRA-1545, round 37). One step for each form, each naming zz-c.sh, which retries
# at 180 s. The process substitution is written twice: a run inside one, and the tester's
# `bash <(cat …)`, where the path is an argument of `cat` inside it and is named that way. A run
# inside a substitution has its path as the command word already, so it is told to take the run
# out of the substitution instead, which is advice it can follow. An `echo` or a `printf`
# piped into a shell is a pipe into a shell like `cat`'s, though the same `echo` with no pipe is
# a non-call (the case above): it was read as a non-call, and the script the shell runs was
# neither charged nor refused.
echo "case: each place rule 8 does not read is refused by name where the script fetches"
root=$(fixture unread-forms)
cp "$work/runs-twice-under-30/.github/scripts/zz-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/zz-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Run it through a variable
        timeout-minutes: 1
        run: |
          p=.github/scripts/zz-c.sh; "$p"
      - name: List it in an array
        timeout-minutes: 1
        run: |
          a=( .github/scripts/zz-c.sh )
          "${a[@]}"
      - name: Run it through eval
        timeout-minutes: 1
        run: |
          eval .github/scripts/zz-c.sh
      - name: Run it in bash -c
        timeout-minutes: 1
        run: |
          bash -c '.github/scripts/zz-c.sh'
      - name: Pipe it into a shell
        timeout-minutes: 1
        run: |
          cat .github/scripts/zz-c.sh | bash
      - name: Run it in a process substitution
        timeout-minutes: 1
        run: |
          diff <(.github/scripts/zz-c.sh) expected.txt
      - name: Feed it to bash through a process substitution
        timeout-minutes: 1
        run: |
          bash <(cat .github/scripts/zz-c.sh)
      - name: Run it in a function
        timeout-minutes: 1
        run: |
          f() { .github/scripts/zz-c.sh; }; f
      - name: Name it to a helper
        timeout-minutes: 1
        run: |
          die() { echo "$*" >&2; exit 1; }
          die "see .github/scripts/zz-c.sh"
      - name: Echo it into a shell
        timeout-minutes: 1
        run: |
          echo .github/scripts/zz-c.sh | bash
      - name: Print it into a shell
        timeout-minutes: 1
        run: |
          printf '%s\n' .github/scripts/zz-c.sh | sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
command_word='Write the path as the command word'
substitution='Take the run out of the substitution: run the script as a command of its own, with its output written to a file, and name that file where the substitution stood'
for refusal in \
  "10	Run it through a variable	a variable assignment	$command_word" \
  "14	List it in an array	an array	$command_word" \
  "19	Run it through eval	.eval.	$command_word" \
  "23	Run it in bash -c	.bash -c. text	$command_word" \
  "27	Pipe it into a shell	a pipe into a shell	$command_word" \
  "31	Run it in a process substitution	a process substitution	$substitution" \
  "35	Feed it to bash through a process substitution	an argument of .cat.	$command_word" \
  "39	Run it in a function	a function body	Call the script outside the function" \
  "44	Name it to a helper	an argument of .die.	$command_word" \
  "48	Echo it into a shell	a pipe into a shell	$command_word" \
  "52	Print it into a shell	a pipe into a shell	$command_word"; do
  IFS=$'\t' read -r line step form cure <<<"$refusal"
  check "refuses '$step' by its file and line, the step, the script and the form" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' names .github/scripts/zz-c.sh in $form, a place this check does not read as a call, and that script fetches")"
  check "gives '$step' its rewrite" \
    "$(contains "$out" "step '$step' of job 'build' names .github/scripts/zz-c.sh in $form, .*$cure")"
done
check "does not tell a run inside a substitution to write its path as the command word" \
  "$(is "$(contains "$out" "step 'Run it in a process substitution' of job 'build' .*$command_word")" false)"
check "refuses nothing else" \
  "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 11)"
check "charges none of them" "$(is "$(contains "$out" 'its own retries take')" false)"

# The same places naming a script that fetches nothing are neither charged nor refused
# (dispatch://INFRA-1545, round 37): zz-quiet.sh holds no retried call, so whatever runs it
# waits for no attempt, and there is no figure to ask for.
echo "case: a place rule 8 does not read, naming a script that fetches nothing, passes"
root=$(fixture unread-quiet)
cat > "$root/.github/scripts/zz-quiet.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "nothing to fetch"
SH
chmod +x "$root/.github/scripts/zz-quiet.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Run it through a variable
        timeout-minutes: 1
        run: |
          p=.github/scripts/zz-quiet.sh; "$p"
      - name: Run it in a function
        timeout-minutes: 1
        run: |
          f() { .github/scripts/zz-quiet.sh; }; f
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "names zz-quiet.sh nowhere" "$(is "$(contains "$out" 'zz-quiet.sh')" false)"

# A script is read as a step is: a place it does not read, naming a script that fetches, is
# refused at the script's own file and line, and the script is named where a step would be
# (dispatch://INFRA-1545, round 37). zz-wrap.sh runs zz-c.sh, 1050 s, from a function body and
# through a variable, and the step running zz-wrap.sh is charged neither.
echo "case: a place rule 8 does not read, inside a script the step runs, is refused at that script's line"
root=$(fixture unread-in-script)
cp "$work/runs-twice-under-30/.github/scripts/zz-c.sh" "$root/.github/scripts/"
cat > "$root/.github/scripts/zz-wrap.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
fetch() {
  .github/scripts/zz-c.sh
}
fetch
p=.github/scripts/zz-c.sh
"$p"
SH
chmod +x "$root/.github/scripts/zz-c.sh" "$root/.github/scripts/zz-wrap.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Run the wrapper
        timeout-minutes: 1
        run: .github/scripts/zz-wrap.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the function body at zz-wrap.sh:4" \
  "$(contains "$out" "::error file=.github/scripts/zz-wrap.sh,line=4::.github/scripts/zz-wrap.sh names .github/scripts/zz-c.sh in a function body, a place this check does not read as a call, and that script fetches")"
check "refuses the variable at zz-wrap.sh:7" \
  "$(contains "$out" "::error file=.github/scripts/zz-wrap.sh,line=7::.github/scripts/zz-wrap.sh names .github/scripts/zz-c.sh in a variable assignment, a place this check does not read as a call, and that script fetches")"
check "charges the step nothing for them" "$(is "$(contains "$out" 'case.yaml')" false)"

# release.yaml's shape after round 37's rewrite (dispatch://INFRA-1545, round 37): a function
# computes a value, and each run is written where that value is used, as the whole command of a
# `$(…)` whose argument is the function's own `$(…)`. Two runs of zz-c.sh, 2 x 1050 = 2100 s,
# and neither is read as a function body.
echo "case: a run written at the call site of a function computing its argument is charged"
root=$(fixture release-shape)
cp "$work/runs-twice-under-30/.github/scripts/zz-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/zz-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Resolve both versions
        timeout-minutes: 1
        run: |
          released_of() {
            if [ "$(sed -n 's/^skip=//p' <<<"$1")" != true ]; then
              sed -n 's/^version=//p' <<<"$1"
            fi
          }
          envoy_version=$(.github/scripts/zz-c.sh pair-version pi-envoy-v "$(released_of "$envoy")")
          legion_version=$(.github/scripts/zz-c.sh pair-version pi-legion-v "$(released_of "$legion")")
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "charges both runs, 2100 s" \
  "$(contains "$out" "step 'Resolve both versions' carries .timeout-minutes: 1., 60 s, and its own retries take 2100 s to exhaust")"
check "reads neither as a place it does not read" \
  "$(is "$(contains "$out" 'a place this check does not read as a call')" false)"

# The shapes the call scanner reads a script's path in, each a step whose verdict is that reading
# (dispatch://INFRA-1545, round 37). Round 37's mutation pass of rule 8 left the scanner's handling
# of backticks, subshells, quotes, escapes, comments and redirects standing, because no case wrote
# a path in those shapes. sa-c.sh retries at 180 s, 5 x 180 + 150 = 1050 s, and every step's key is
# one minute, so a step the scanner reads a run in is refused with 1050 s. These steps are runs:
# the whole command of a backtick substitution, one inside an array's parentheses, a run beside a
# backtick substitution that holds a case pattern's `)`, a run beside a `$(…)` holding a subshell, a
# run inside a subshell, a run on the line after a process substitution, after a comment that ends
# the command before it, and after a `${…#…}` expansion on its line, a run through `\bash`, a run
# after an escaped quote, a run through a quoted `"ba\<newline>sh"` (the shell joins the two lines
# inside the quotes, so the command is `bash`), and a run whose output is redirected before its
# path. A reading that loses the backtick, the subshell, the comment's newline or the escape reads
# the path as an argument, a variable or nothing and refuses it as unread, or crashes.
echo "case: a run written inside backticks, subshells, quotes and escapes is charged"
root=$(fixture sa-scanner-runs)
cat > "$root/.github/scripts/sa-c.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
chmod +x "$root/.github/scripts/sa-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Run inside backticks
        timeout-minutes: 1
        run: |
          out=`.github/scripts/sa-c.sh`
      - name: Run inside backticks inside an array
        timeout-minutes: 1
        run: |
          files=( `.github/scripts/sa-c.sh` )
      - name: Run beside backticks holding a case pattern
        timeout-minutes: 1
        run: |
          bash .github/scripts/sa-c.sh "`case $1 in a) echo a;; esac`"
      - name: Run beside a subshell inside a substitution
        timeout-minutes: 1
        run: |
          .github/scripts/sa-c.sh "$( (cd sub && pwd) )"
      - name: Run inside a subshell
        timeout-minutes: 1
        run: |
          ( cd "$GITHUB_WORKSPACE" && .github/scripts/sa-c.sh )
      - name: Run after a process substitution
        timeout-minutes: 1
        run: |
          sort <(printf 'b\na\n')
          .github/scripts/sa-c.sh
      - name: Run after a comment that ends a command
        timeout-minutes: 1
        run: |
          cd "$GITHUB_WORKSPACE" # the scripts are read from the root
          .github/scripts/sa-c.sh
      - name: Run after an expansion holding a hash
        timeout-minutes: 1
        run: |
          tag=${GITHUB_REF#refs/tags/}; .github/scripts/sa-c.sh "$tag"
      - name: Run through a backslashed bash
        timeout-minutes: 1
        run: |
          \bash .github/scripts/sa-c.sh
      - name: Run after an escaped quote
        timeout-minutes: 1
        run: |
          echo it\'s done; .github/scripts/sa-c.sh
      - name: Run through a quoted bash broken over two lines
        timeout-minutes: 1
        run: |
          "ba\
          sh" .github/scripts/sa-c.sh
      - name: Run with its output redirected before its path
        timeout-minutes: 1
        run: |
          bash >run.log .github/scripts/sa-c.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for step in \
  "Run inside backticks" \
  "Run inside backticks inside an array" \
  "Run beside backticks holding a case pattern" \
  "Run beside a subshell inside a substitution" \
  "Run inside a subshell" \
  "Run after a process substitution" \
  "Run after a comment that ends a command" \
  "Run after an expansion holding a hash" \
  "Run through a backslashed bash" \
  "Run after an escaped quote" \
  "Run through a quoted bash broken over two lines" \
  "Run with its output redirected before its path"; do
  check "charges '$step' one run of sa-c.sh, 1050 s" \
    "$(contains "$out" "step '$step' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 12)"
check "reads none of them as a place it does not read" \
  "$(is "$(contains "$out" 'a place this check does not read as a call')" false)"

# The same scanner's other readings (dispatch://INFRA-1545, round 37). Each step below writes
# sa-c.sh's path in a place rule 8 does not read, and is refused there by its file, its line and
# the form, which is what the scanner read: `bash < path` is a redirect; an `echo` piped with its
# errors, `2>&1 |`, into bash is a pipe into a shell (the `&` of `2>&1` belongs to the redirect and
# ends no command); a quoted command name holding a backslash keeps it (`"ba\sh"` is not bash, nor
# is `"bas\xh"`); a quoted argument whose path is followed by another character before the quote
# closes on the next line is more than the path; `"$(command -v bash)"` is a command word the check
# does not name as bash; a `for` after a function's spaced parentheses lists the path in a `for`'s
# words; a process substitution assigned to a name is a process substitution, not an array; a
# path on the second line of a backslash continuation is refused on that line; and a path the run
# text ends in with a backslash after it is more than the path. Five steps are no call and are
# refused nothing: a path in a comment after an empty redirect target, an `echo` on the line
# before a `bash` run (a newline pipes nothing), a `grep` before `|| bash` (an OR pipes nothing),
# an `echo` of a script that sources sa-c.sh (a printed path is not walked), and an `echo` of a
# message quoting a command in escaped backticks, which open no substitution.
echo "case: a path the scanner reads in a redirect, a quoted word or a substitution is refused by that form"
root=$(fixture sa-scanner-forms)
cp "$work/sa-scanner-runs/.github/scripts/sa-c.sh" "$root/.github/scripts/"
cat > "$root/.github/scripts/sa-q.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "nothing to fetch"
SH
cat > "$root/.github/scripts/sa-user.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/sa-c.sh
SH
chmod +x "$root/.github/scripts/sa-c.sh" "$root/.github/scripts/sa-q.sh" \
  "$root/.github/scripts/sa-user.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Feed it to bash on standard input
        timeout-minutes: 1
        run: |
          bash < .github/scripts/sa-c.sh
      - name: Echo it into bash with its errors
        timeout-minutes: 1
        run: |
          echo .github/scripts/sa-c.sh 2>&1 | bash
      - name: Run it through a quoted name holding a backslash
        timeout-minutes: 1
        run: |
          "ba\sh" .github/scripts/sa-c.sh
      - name: Run it through a quoted name holding another backslash
        timeout-minutes: 1
        run: |
          "bas\xh" .github/scripts/sa-c.sh
      - name: Pass it to bash in quotes closing on the next line
        timeout-minutes: 1
        run: |
          bash ".github/scripts/sa-c.sh;
          "
      - name: Run it through a substitution naming bash
        timeout-minutes: 1
        run: |
          "$(command -v bash)" .github/scripts/sa-c.sh
      - name: List it after a function's spaced parentheses
        timeout-minutes: 1
        run: |
          sa_f () for x in .github/scripts/sa-c.sh; do "$x"; done
      - name: Read it from a process substitution assigned to a name
        timeout-minutes: 1
        run: |
          out=<(.github/scripts/sa-c.sh)
      - name: Run it through eval over two lines
        timeout-minutes: 1
        run: |
          eval \
            .github/scripts/sa-c.sh
      - name: Pass it to bash before a trailing backslash
        timeout-minutes: 1
        run: bash .github/scripts/sa-c.sh\
      - name: Name it in a comment after an empty redirect
        timeout-minutes: 1
        run: |
          : > ""; true # see .github/scripts/sa-c.sh
      - name: Print it before a run of bash
        timeout-minutes: 1
        run: |
          echo "see .github/scripts/sa-c.sh"
          bash .github/scripts/sa-q.sh
      - name: Search for it before a run of bash
        timeout-minutes: 1
        run: |
          grep -q .github/scripts/sa-c.sh list.txt || bash .github/scripts/sa-q.sh
      - name: Print the name of a script that sources it
        timeout-minutes: 1
        run: |
          echo "see .github/scripts/sa-user.sh"
      - name: Print a command quoted in escaped backticks
        timeout-minutes: 1
        run: |
          echo "run \`.github/scripts/sa-c.sh\` to refresh the asset"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for refusal in \
  "10	Feed it to bash on standard input	a redirect" \
  "14	Echo it into bash with its errors	a pipe into a shell" \
  "18	Run it through a quoted name holding a backslash	an argument of .ba\\\\sh." \
  "22	Run it through a quoted name holding another backslash	an argument of .bas\\\\xh." \
  "26	Pass it to bash in quotes closing on the next line	an argument of .bash." \
  "31	Run it through a substitution naming bash	an argument of .\$(command -v bash)." \
  "35	List it after a function's spaced parentheses	the words of a .for." \
  "39	Read it from a process substitution assigned to a name	a process substitution" \
  "44	Run it through eval over two lines	.eval." \
  "47	Pass it to bash before a trailing backslash	an argument of .bash."; do
  IFS=$'\t' read -r line step form <<<"$refusal"
  check "refuses '$step' at line $line as $form" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' names .github/scripts/sa-c.sh in $form, a place this check does not read as a call, and that script fetches")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 10)"
check "charges none of them" "$(is "$(contains "$out" 'its own retries take')" false)"
for step in \
  "Name it in a comment after an empty redirect" \
  "Print it before a run of bash" \
  "Search for it before a run of bash" \
  "Print the name of a script that sources it" \
  "Print a command quoted in escaped backticks"; do
  check "neither charges nor refuses '$step'" "$(is "$(contains "$out" "step '$step'")" false)"
done

# A library one shell reaches through a looped run is charged once, at that path's product, and
# the run's own loop waits are charged with the run (dispatch://INFRA-1545, round 37). sa-lib.sh
# and sa-lib2.sh each retry at 180 s, 1050 s; sa-b.sh sources sa-lib.sh and nothing else, so a
# loop of two runs of it sleeping 61 s reaches sa-lib.sh with a product of 2,
# 2 x 1050 + 61 = 2161 s. One more step sources sa-lib.sh and then sa-lib2.sh, two libraries,
# 2100 s; a walk that stopped at the first library charged it 1050 s. The steps that sourced
# sa-lib.sh inside a loop beside such a run, which pinned how a source's loop waits were charged,
# are gone with that charge: a source inside a loop is refused by name, which the
# r37-library-order case below proves.
echo "case: a library reached through a looped run is charged at that run's product, and two libraries are both charged"
root=$(fixture sa-library-paths)
cat > "$root/.github/scripts/sa-lib.sh" <<'SH'
#!/usr/bin/env bash
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
cp "$root/.github/scripts/sa-lib.sh" "$root/.github/scripts/sa-lib2.sh"
cat > "$root/.github/scripts/sa-b.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/sa-lib.sh
SH
chmod +x "$root/.github/scripts/sa-b.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: The library reached through a looped run
        timeout-minutes: 1
        run: |
          for pass in 1 2; do
            .github/scripts/sa-b.sh
            sleep 61
          done
      - name: Two libraries
        timeout-minutes: 1
        run: |
          . .github/scripts/sa-lib.sh
          . .github/scripts/sa-lib2.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
for charge in \
  "The library reached through a looped run	2161" \
  "Two libraries	2100"; do
  IFS=$'\t' read -r step seconds <<<"$charge"
  check "charges '$step' $seconds s" \
    "$(contains "$out" "step '$step' carries .timeout-minutes: 1., 60 s, and its own retries take $seconds s to exhaust")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 2)"

# A script whose loops the check cannot count is a run with no figure as soon as one of them runs
# a script that fetches, whatever its later loops run (dispatch://INFRA-1545, round 37). sa-wrap.sh
# loops, uncounted, over sa-c.sh, which retries at 180 s, and then, uncounted again, over sa-q.sh,
# which fetches nothing; the step loops, uncounted, over sa-wrap.sh. The refusal says one run of
# sa-wrap.sh reaches such a loop; a walk letting the last loop's answer stand for all of them read
# the quiet loop alone and printed `takes 0 s`.
echo "case: an uncounted loop over a fetching script leaves its script with no figure, though a later loop runs a quiet one"
root=$(fixture sa-unread-then-quiet)
cp "$work/sa-scanner-runs/.github/scripts/sa-c.sh" "$work/sa-scanner-forms/.github/scripts/sa-q.sh" \
  "$root/.github/scripts/"
cat > "$root/.github/scripts/sa-wrap.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
while read -r id; do
  .github/scripts/sa-c.sh "$id"
done <<<"$ids"
while read -r id; do
  .github/scripts/sa-q.sh "$id"
done <<<"$ids"
SH
chmod +x "$root/.github/scripts/sa-c.sh" "$root/.github/scripts/sa-q.sh" \
  "$root/.github/scripts/sa-wrap.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Run the wrapper for every id
        timeout-minutes: 30
        run: |
          while read -r id; do
            .github/scripts/sa-wrap.sh "$id"
          done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "is refused once" \
  "$(is "$(printf '%s\n' "$out" | grep -c 'this check can put no figure on it')" 1)"
check "says one run of sa-wrap.sh reaches an uncounted loop" \
  "$(contains "$out" 'One run of ..github/scripts/sa-wrap.sh. reaches a loop, its own or one in a script it calls')"
check "prints no figure for a run it cannot read" "$(is "$(contains "$out" 'takes 0 s')" false)"

# Group B of round 37's mutation pass over call_of (dispatch://INFRA-1545, round 37): each place
# a path can be written in, read the way call_of reads it. zz-c.sh retries at 180 s, 5 x 180 +
# 150 = 1050 s, the script the cases above write; these fixtures write it again, so the block
# reads alone. Every key is one minute, so a run the gate charges is refused with its figure.
echo "case: a run is charged after /bin/bash, inside an array's \$(…) and after bash's own options"
root=$(fixture sb-run-spellings)
cat > "$root/.github/scripts/zz-c.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
chmod +x "$root/.github/scripts/zz-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Run it through /bin/bash
        timeout-minutes: 1
        run: /bin/bash .github/scripts/zz-c.sh
      - name: Run it inside an array's substitution
        timeout-minutes: 1
        run: files=( $(.github/scripts/zz-c.sh) )
      - name: Run it after bash --
        timeout-minutes: 1
        run: bash -- .github/scripts/zz-c.sh
      - name: Run it after bash --norc
        timeout-minutes: 1
        run: bash --norc .github/scripts/zz-c.sh
      - name: Run it after bash -eo pipefail
        timeout-minutes: 1
        run: bash -eo pipefail .github/scripts/zz-c.sh
      - name: Run it after bash -e -u
        timeout-minutes: 1
        run: bash -e -u .github/scripts/zz-c.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
for step in "Run it through /bin/bash" "Run it inside an array's substitution" \
  "Run it after bash --" "Run it after bash --norc" "Run it after bash -eo pipefail" \
  "Run it after bash -e -u"; do
  check "charges '$step' one run, 1050 s" \
    "$(contains "$out" "step '$step' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
done
check "refuses none of them as a place it does not read" \
  "$(is "$(contains "$out" 'a place this check does not read as a call')" false)"

# A path that is only a comment or text a command prints or searches is no call, whatever sits
# around it (dispatch://INFRA-1545, round 37): a comment on a body's first line, a comment that
# opens the line after a continuation, a word that starts at the path's slash, an `echo` whose
# next command is a shell it does not pipe into, a `printf` piped into a command that is no
# shell, and an `echo` piped into a `{ … }` group.
echo "case: a comment, and echo, printf or grep output that no shell runs, name a script and pass"
root=$(fixture sb-non-calls)
cp "$work/sb-run-spellings/.github/scripts/zz-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/zz-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Name it in a comment on the body's first line
        timeout-minutes: 1
        run: |
          true # .github/scripts/zz-c.sh fetches the asset; this step only names it
          true
      - name: Name it in a comment after a continuation
        timeout-minutes: 1
        run: |
          true \
          # .github/scripts/zz-c.sh fetches the asset; this step only names it
      - name: Search for its bare name
        timeout-minutes: 1
        run: grep -q /zz-c.sh notes.txt || true
      - name: Echo it before a shell it does not pipe into
        timeout-minutes: 1
        run: echo "fetching with .github/scripts/zz-c.sh" && bash -c true
      - name: Print it into grep
        timeout-minutes: 1
        run: printf '%s\n' .github/scripts/zz-c.sh | grep -c zz-c
      - name: Echo it into a group
        timeout-minutes: 1
        run: echo .github/scripts/zz-c.sh | { read -r p; echo "$p"; }
YAML
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
check "neither charges nor refuses a step" "$(is "$(contains "$out" 'case.yaml')" false)"
check "names zz-c.sh nowhere" "$(is "$(contains "$out" 'zz-c.sh')" false)"

# The places call_of reads as no call, past the ones the case above with the same forms proves,
# each refused by name, form and rewrite (dispatch://INFRA-1545, round 37): a redirect target, a
# `case` word, the text of a retry_fetch, a pipe into a shell written as a path and one after
# another command, the words of a `bash` that are no script (behind `-- -c`, an option holding
# the path, an empty word, another script, a longer word), a command word holding more than the
# path, a `.` given a second word and a `source` given a longer one, and the arguments of
# `export` and `make`. None is charged.
echo "case: each further place call_of does not read is refused by name, and none is charged"
root=$(fixture sb-unread-forms)
cp "$work/sb-run-spellings/.github/scripts/zz-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/zz-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Write it through a redirect
        timeout-minutes: 1
        run: echo done > .github/scripts/zz-c.sh
      - name: Write its bare name through a redirect
        timeout-minutes: 1
        run: echo done > /zz-c.sh
      - name: Match it in a case
        timeout-minutes: 1
        run: case .github/scripts/zz-c.sh in *) true ;; esac
      - name: Name it in a retry's text
        timeout-minutes: 6
        run: |
          . .github/scripts/ci-retry.sh
          retry_fetch "the asset .github/scripts/zz-c.sh names" 30 -- curl -fsSL --max-time 30 -o a https://example.com/a
      - name: Echo it into a shell written as a path
        timeout-minutes: 1
        run: echo .github/scripts/zz-c.sh | /bin/bash
      - name: Echo it into a shell after another command
        timeout-minutes: 1
        run: |
          cd .
          echo .github/scripts/zz-c.sh | bash
      - name: Give it to bash behind -- -c
        timeout-minutes: 1
        run: bash -- -c .github/scripts/zz-c.sh
      - name: Give it to bash in an option
        timeout-minutes: 1
        run: bash --rcfile=.github/scripts/zz-c.sh -i
      - name: Give it to bash after an empty word
        timeout-minutes: 1
        run: bash "" .github/scripts/zz-c.sh
      - name: Give it to another script bash runs
        timeout-minutes: 1
        run: bash ./run.sh .github/scripts/zz-c.sh
      - name: Give bash a longer word
        timeout-minutes: 1
        run: bash .github/scripts/zz-c.sh.orig
      - name: Run a longer command word
        timeout-minutes: 1
        run: .github/scripts/zz-c.sh.orig
      - name: Run a command word that goes on past it
        timeout-minutes: 1
        run: .github/scripts/zz-c.sh/grep x
      - name: Give it to . as a second word
        timeout-minutes: 1
        run: . ./lib.sh .github/scripts/zz-c.sh
      - name: Source a longer word
        timeout-minutes: 1
        run: source .github/scripts/zz-c.sh.orig
      - name: Export it
        timeout-minutes: 1
        run: export P=.github/scripts/zz-c.sh
      - name: Export it with no name
        timeout-minutes: 1
        run: export -n .github/scripts/zz-c.sh
      - name: Give it to make
        timeout-minutes: 1
        run: make SCRIPT=.github/scripts/zz-c.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
command_word='Write the path as the command word'
for refusal in \
  "9	Write it through a redirect	a redirect" \
  "12	Write its bare name through a redirect	a redirect" \
  "15	Match it in a case	the words of a .case." \
  "20	Name it in a retry's text	an argument of .retry_fetch." \
  "23	Echo it into a shell written as a path	a pipe into a shell" \
  "28	Echo it into a shell after another command	a pipe into a shell" \
  "31	Give it to bash behind -- -c	an argument of .bash." \
  "34	Give it to bash in an option	an argument of .bash." \
  "37	Give it to bash after an empty word	an argument of .bash." \
  "40	Give it to another script bash runs	an argument of .bash." \
  "43	Give bash a longer word	an argument of .bash." \
  "46	Run a longer command word	a command word that holds more than the script's path" \
  "49	Run a command word that goes on past it	a command word that holds more than the script's path" \
  "52	Give it to . as a second word	an argument of ..." \
  "55	Source a longer word	an argument of .source." \
  "58	Export it	a variable assignment" \
  "61	Export it with no name	an argument of .export." \
  "64	Give it to make	an argument of .make."; do
  IFS=$'\t' read -r line step form <<<"$refusal"
  check "refuses '$step' by its file and line, the step, the script and the form" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' names .github/scripts/zz-c.sh in $form, a place this check does not read as a call, and that script fetches")"
  check "gives '$step' its rewrite" \
    "$(contains "$out" "step '$step' of job 'build' names .github/scripts/zz-c.sh in $form, .*$command_word")"
done
check "refuses nothing else" \
  "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 18)"
check "charges none of them" "$(is "$(contains "$out" 'its own retries take')" false)"

# The function forms rule 8 reads and refuses (dispatch://INFRA-1545, round 37). A run written in
# the body of `name() { … }`, its `{` on the name's line or on the next one, is refused as a
# function body, because the function runs it once for every call, from wherever it is called.
# A function written with the `function` keyword, with or without `()`, is no form rule 8 reads:
# its body reads as commands of the step, so the run is charged once, 1050 s, and the definition
# is refused by name, with the form to write instead, since the run would be charged once for
# any number of calls. sc-c.sh retries at 180 s, 5 x 180 + 150 = 1050 s.
echo "case: a run in a function body is refused as one, and a function with the function keyword is refused by name"
root=$(fixture sc-function-forms)
cat > "$root/.github/scripts/sc-c.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
chmod +x "$root/.github/scripts/sc-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Brace on the next line
        timeout-minutes: 1
        run: |
          fetch()
          {
            .github/scripts/sc-c.sh
          }
          fetch
      - name: The function keyword
        timeout-minutes: 1
        run: |
          function fetch { .github/scripts/sc-c.sh; }
          fetch
      - name: The function keyword with parentheses
        timeout-minutes: 1
        run: |
          function fetch() { .github/scripts/sc-c.sh; }
          fetch
      - name: The function keyword, brace on the next line
        timeout-minutes: 1
        run: |
          function fetch
          {
            .github/scripts/sc-c.sh
          }
          fetch
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the brace on the next line as a function body at its line" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=12::step 'Brace on the next line' of job 'build' names .github/scripts/sc-c.sh in a function body, a place this check does not read as a call")"
for refusal in \
  "18	The function keyword" \
  "23	The function keyword with parentheses" \
  "28	The function keyword, brace on the next line"; do
  IFS=$'\t' read -r line step <<<"$refusal"
  check "refuses '$step' by name at its definition" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' defines a function with the .function. keyword, a form this check does not read as a function, and a fetch, or a call of a script that fetches, is written after it")"
  check "tells '$step' the form to write" \
    "$(contains "$out" "step '$step' of job 'build' defines a function with the .function. keyword, .*this check counts those calls only for a function written .name() { …; }.*Write the function in that form")"
  check "charges '$step' its run once, 1050 s" \
    "$(contains "$out" "step '$step' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 7)"

# What is not a function body is read as the run it is (dispatch://INFRA-1545, round 37): a brace
# group at the top level, after a command of two words, and one in a `case` arm whose pattern is
# the word `function`. Each runs sc-c.sh once, 1050 s, which `timeout-minutes: 1` does not hold.
# Read as a function body, each was refused as one and charged nothing.
echo "case: a brace group that is no function body is charged as a run"
root=$(fixture sc-brace-groups)
cp "$work/sc-function-forms/.github/scripts/sc-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/sc-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Log the fetch
        timeout-minutes: 1
        run: |
          echo "fetching"
          { .github/scripts/sc-c.sh; } > fetch.log
      - name: Fetch for one kind
        timeout-minutes: 1
        run: |
          case "$kind" in
            function) { .github/scripts/sc-c.sh; } ;;
          esac
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "charges the brace group's run, 1050 s" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=8::step 'Log the fetch' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
check "charges the case arm's run, 1050 s" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=13::step 'Fetch for one kind' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
check "reads neither as a function body" "$(is "$(contains "$out" 'a function body')" false)"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 2)"

# A function whose body is not a `{ … }` group — a `( … )` subshell, or an `if` — is no form
# rule 8 reads (dispatch://INFRA-1545, round 37): its body reads as commands of the step, so its
# run is charged once, 1050 s, and the definition is refused by name, since the run would be
# charged once for the two calls each step makes. Round 37 read both bodies for a while, the
# subshell refused as a function body and the `if` charged once for each call, 2100 s; no step
# of this tree writes either.
echo "case: a function whose body is a subshell or an if is refused by name"
root=$(fixture sc-function-bodies)
cp "$work/sc-function-forms/.github/scripts/sc-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/sc-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: A subshell body
        timeout-minutes: 1
        run: |
          fetch() (
            .github/scripts/sc-c.sh
          )
          fetch
          fetch
      - name: An if body
        timeout-minutes: 1
        run: |
          fetch() if [ -n "$GITHUB_TOKEN" ]; then {
            .github/scripts/sc-c.sh
          }; fi
          fetch
          fetch
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
for refusal in \
  "10	A subshell body" \
  "18	An if body"; do
  IFS=$'\t' read -r line step <<<"$refusal"
  check "refuses '$step' by name at its definition" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' defines a function with a body that is not a .{ … }. group, such as a .( … ). subshell, an .if., a loop or a .case., a form this check does not read as a function")"
  check "charges '$step' its run once, 1050 s" \
    "$(contains "$out" "step '$step' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 4)"

# The lines occurrences_of joins and the comments it leaves out, which are bash's
# (dispatch://INFRA-1545, round 37). A run whose continuation line holds only a commented-out
# flag is a run, since the comment starts at the `#` and not at the run's line; a run after a
# comment line is a run, wherever the comment sits; a run in a `run: |-` body whose last line
# ends in a backslash is a run, with no line after it to join; a word of one character followed
# by a backslash and a line starting with `#` is one word, `x#y`, to which the path is an
# argument, refused by name as any argument of a command this check does not know is; and a
# line starting with `#` inside a string is part of the string, so a string echoed into a shell
# holds the path whatever that shell makes of the line, and is refused as a pipe into a shell,
# as `echo <path> | bash` is. Each of the first three steps runs sc-c.sh, 1050 s.
echo "case: continued lines and comment lines are read as the shell reads them"
root=$(fixture sc-continued-lines)
cp "$work/sc-function-forms/.github/scripts/sc-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/sc-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: A flag commented out
        timeout-minutes: 1
        run: |
          .github/scripts/sc-c.sh --all \
            #--verbose
      - name: After a comment
        timeout-minutes: 1
        run: |
          echo start
          # fetch the asset
          .github/scripts/sc-c.sh
      - name: A stray backslash at the end
        timeout-minutes: 1
        run: |-
          .github/scripts/sc-c.sh \
      - name: One word over two lines
        timeout-minutes: 1
        run: |
          x\
          #y .github/scripts/sc-c.sh
      - name: A comment piped into a shell
        timeout-minutes: 1
        run: |
          echo "set -eu
            # .github/scripts/sc-c.sh is the next job's, and this shell only prepares
          mkdir -p out" | bash
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
for refusal in \
  "8	A flag commented out" \
  "13	After a comment" \
  "19	A stray backslash at the end"; do
  IFS=$'\t' read -r line step <<<"$refusal"
  check "charges '$step' its run, 1050 s" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
done
check "refuses the argument of the joined word by name" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=26::step 'One word over two lines' of job 'build' names .github/scripts/sc-c.sh in an argument of .x#y., a place this check does not read as a call")"
check "refuses the string echoed into a shell at the path's line" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=31::step 'A comment piped into a shell' of job 'build' names .github/scripts/sc-c.sh in a pipe into a shell, a place this check does not read as a call")"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 5)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"

# A continuation onto the last line of a body names the call by the line its logical line starts
# on (dispatch://INFRA-1545, round 37): the `run: |-` body ends with no newline, so the line the
# call is written on is the body's last, and it is joined to the `do \` above it all the same.
echo "case: a call continued onto a body's last line is named at its logical line"
root=$(fixture sc-last-line)
cp "$work/sc-function-forms/.github/scripts/sc-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/sc-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch every row
        timeout-minutes: 1
        run: |-
          while read -r row; do \
            .github/scripts/sc-c.sh "$row"; done <<<"$rows"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the call at the line its logical line starts on" \
  "$(contains "$out" "it runs a retried call inside a loop whose passes it cannot read — ..github/scripts/sc-c.sh. at .github/workflows/case.yaml:9, inside the loop at line 9\.")"

# Exception (d)'s closure reads the paths a credentialed job could run, and no other
# (dispatch://INFRA-1545, round 37): a path its step prints is a recognised non-call, so the
# script it names is held to the floor as any other script is, and so is one a credentialed
# script only prints; a script a credentialed script runs is the job's, and carries no retry. A
# path naming no script of the tree, here one an earlier step would write, is read as nothing.
# sc-bare.sh, sc-echoed.sh and sc-pub-inner.sh each fetch with a bare curl, which rule 1 refuses
# outside a credentialed job.
echo "case: exception (d) follows a credentialed job's runs and not the paths it prints"
root=$(fixture sc-credentialed)
for name in sc-bare sc-echoed sc-pub-inner; do
  cat > "$root/.github/scripts/$name.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
curl -fsSL -o asset https://example.com/asset
SH
done
cat > "$root/.github/scripts/sc-pub.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/sc-pub-inner.sh
echo "the next job runs .github/scripts/sc-echoed.sh"
SH
chmod +x "$root/.github/scripts/sc-bare.sh" "$root/.github/scripts/sc-echoed.sh" \
  "$root/.github/scripts/sc-pub-inner.sh" "$root/.github/scripts/sc-pub.sh"
cat >> "$root/.github/workflows/release.yaml" <<'YAML'
      - name: Run the script the build wrote
        run: .github/scripts/sc-built.sh
      - name: Say what runs next
        run: echo "the next job runs .github/scripts/sc-bare.sh"
      - name: Publish
        run: .github/scripts/sc-pub.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "holds the script a credentialed step prints to the floor" \
  "$(contains "$out" "::error file=.github/scripts/sc-bare.sh,line=3::.curl. fetches without the floor")"
check "holds the script a credentialed script prints to the floor" \
  "$(contains "$out" "::error file=.github/scripts/sc-echoed.sh,line=3::.curl. fetches without the floor")"
check "exempts the script a credentialed script runs" \
  "$(is "$(contains "$out" 'sc-pub-inner.sh')" false)"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 2)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"

# A step running each of two scripts in a loop whose passes this check cannot read names each
# script once, at its first such call, and in the order of their paths (dispatch://INFRA-1545,
# round 37): sc-b.sh is run at body lines 1 and 3 and sc-a.sh at line 2, so sc-a.sh is named
# first, and sc-b.sh at its first call. The figure is the first one's, 1050 s.
echo "case: scripts in uncounted loops are named once each, at the first call, by path"
root=$(fixture sc-blocked-order)
cp "$work/sc-function-forms/.github/scripts/sc-c.sh" "$root/.github/scripts/sc-a.sh"
cp "$work/sc-function-forms/.github/scripts/sc-c.sh" "$root/.github/scripts/sc-b.sh"
chmod +x "$root/.github/scripts/sc-a.sh" "$root/.github/scripts/sc-b.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch every row twice
        timeout-minutes: 1
        run: |
          while read -r row; do .github/scripts/sc-b.sh "$row"; done <<<"$rows"
          while read -r row; do .github/scripts/sc-a.sh "$row"; done <<<"$rows"
          while read -r row; do .github/scripts/sc-b.sh "$row"; done <<<"$rows"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names sc-a.sh, then sc-b.sh at its first call" \
  "$(contains "$out" "inside a loop whose passes it cannot read — ..github/scripts/sc-a.sh. at .github/workflows/case.yaml:10, inside the loop at line 10; ..github/scripts/sc-b.sh. at .github/workflows/case.yaml:9, inside the loop at line 9\. One run of that script takes 1050 s")"

# The places rule 8 does not read are refused in the order they are written
# (dispatch://INFRA-1545, round 37), so the annotations read down the file as the step does.
echo "case: unread places are refused in the order they are written"
root=$(fixture sc-unread-order)
cp "$work/sc-function-forms/.github/scripts/sc-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/sc-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Run it two ways
        timeout-minutes: 1
        run: |
          p=.github/scripts/sc-c.sh; "$p"
          eval .github/scripts/sc-c.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the variable at line 10, then eval at line 11" \
  "$(is "$(printf '%s\n' "$out" | grep '^::error' | cut -d: -f3 | tr '\n' ' ')" "error file=.github/workflows/case.yaml,line=10 error file=.github/workflows/case.yaml,line=11 ")"

# The bound rule 8 names for an item it cannot price is the first item's (dispatch://INFRA-1545,
# round 37): the step's first uncounted site writes its bound as a variable and its second
# writes none, and the refusal speaks of the first. A site whose verb is no curl reads no bound
# off a `-m` among its words: `wget -m` mirrors, and the `"$T"` this site hands `retry_fetch` is
# no number, so it is told this check finds no bound on it.
echo "case: the bound an uncounted site is told of is its own"
root=$(fixture sc-bound-advice)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read every run and its jobs
        timeout-minutes: 30
        run: |
          read_run() {
            curl -fsSL --retry 7 --retry-all-errors --max-time "$bound" \
              -o "run-$1.json" "https://api.github.com/repos/$GITHUB_REPOSITORY/actions/runs/$1"
          }
          read_jobs() {
            curl -fsSL --retry 7 --retry-all-errors \
              -o "jobs-$1.json" "https://api.github.com/repos/$GITHUB_REPOSITORY/actions/runs/$1/jobs"
          }
          while read -r id; do
            read_run "$id"
            read_jobs "$id"
          done <<<"$ids"
      - name: Mirror every site
        timeout-minutes: 30
        run: |
          . .github/scripts/ci-retry.sh
          while read -r site; do
            retry_fetch "the mirror of $site" "$T" -- wget -m -np "$site"
          done < sites.txt
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "tells the first step its first site's bound is a variable" \
  "$(contains "$out" "step 'Read every run and its jobs' .*What one call of .curl. there takes is a figure this check cannot make, because its bound is written as a variable rather than as a number")"
check "gives the first step the variable bound's advice" \
  "$(contains "$out" "step 'Read every run and its jobs' .*it exists to allow\. Write that bound as a number")"
check "tells the wget step it finds no bound" \
  "$(contains "$out" "step 'Mirror every site' .*What one call of .wget. there takes is a figure this check cannot make, because this check finds no bound on one attempt of it")"
check "does not tell the wget step its bound is written as a variable" \
  "$(is "$(contains "$out" "step 'Mirror every site' .*written as a variable")" false)"

# What a script in an uncounted loop is told of is every script it runs, not the first
# (dispatch://INFRA-1545, round 37): sc-outer.sh runs sc-quiet.sh, which fetches nothing, and
# then sc-var.sh, whose curl writes its bound as a variable.
echo "case: a script's unread bound is read through every script it runs"
root=$(fixture sc-unpriced-reach)
cat > "$root/.github/scripts/sc-quiet.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
echo "nothing to fetch"
SH
cat > "$root/.github/scripts/sc-var.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
bound=60
curl -fsSL --retry 7 --retry-all-errors --max-time "$bound" -o run.json \
  "https://api.github.com/repos/$GITHUB_REPOSITORY/actions/runs/$1"
SH
cat > "$root/.github/scripts/sc-outer.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
.github/scripts/sc-quiet.sh
.github/scripts/sc-var.sh "$1"
SH
chmod +x "$root/.github/scripts/sc-quiet.sh" "$root/.github/scripts/sc-var.sh" \
  "$root/.github/scripts/sc-outer.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read every run the decision named
        timeout-minutes: 1
        run: |
          printf 'a\nb\n' | while read -r id; do .github/scripts/sc-outer.sh "$id"; done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "says the second script it runs bounds its attempts with a variable" \
  "$(contains "$out" 'One run of ..github/scripts/sc-outer.sh. makes attempts whose bound is written as a variable rather than as a number, there or in a script it calls')"

# A library sourced inside a loop is refused by name, whether the loop writes its passes or not
# (dispatch://INFRA-1545, round 37): it is no call rule 8 charges, so neither step is charged,
# and each is told to source the library once, before the loop. Round 37 charged the counted
# loop for a while, three passes of 1050 s and two sleeps of 10 s, 3170 s, through a model of
# every path to a library that no step of this tree needs; the uncounted loop was refused as one
# whose passes this check cannot read.
echo "case: a library sourced inside a loop is refused by name, counted loop or not"
root=$(fixture sc-library-waits)
cp "$work/sc-function-forms/.github/scripts/sc-c.sh" "$root/.github/scripts/sc-lib.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Source it three times
        timeout-minutes: 1
        run: |
          for pass in 1 2 3; do
            . .github/scripts/sc-lib.sh
            sleep 10
          done
      - name: Source it on every row read
        timeout-minutes: 1
        run: |
          while read -r row; do
            . .github/scripts/sc-lib.sh
          done < rows.txt
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
for refusal in \
  "11	Source it three times" \
  "18	Source it on every row read"; do
  IFS=$'\t' read -r line step <<<"$refusal"
  check "refuses '$step' by name at the source" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' names .github/scripts/sc-lib.sh in a source inside a loop, a place this check does not read as a call, and that script fetches")"
  check "tells '$step' to source the library once, before the loop" \
    "$(contains "$out" "step '$step' of job 'build' names .github/scripts/sc-lib.sh in a source inside a loop, .*Source the library once, before the loop")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 2)"
check "charges neither" "$(is "$(contains "$out" 'its own retries take')" false)"

# A step using a composite action whose own step loops uncounted is refused for that loop
# (dispatch://INFRA-1545, round 37): the action's arithmetic is None, and the step running it is
# refused by name rather than priced.
echo "case: a uses: step whose action loops uncounted is refused for that loop"
root=$(fixture sc-action-loop)
mkdir -p "$root/.github/actions/sc-rows"
cat > "$root/.github/actions/sc-rows/action.yml" <<'YAML'
name: Rows
description: Reads every row it is handed.
runs:
  using: composite
  steps:
    - name: Read every row
      shell: bash
      run: |
        . .github/scripts/ci-retry.sh
        while read -r url; do
          retry_fetch "a row" 30 -- curl -fsSL --max-time 30 -o row "$url"
        done < urls.txt
YAML
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Read the rows
        timeout-minutes: 5
        uses: ./.github/actions/sc-rows
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the step for the action's loop" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=8::step 'Read the rows' carries .timeout-minutes: 5. and this check can put no figure on it: it runs a retried call inside a loop whose passes it cannot read — .curl. at .github/actions/sc-rows/action.yml:10, inside the loop at line 9\. One call there takes 300 s")"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"

# A path in text the gate cannot read as any command is refused by name as such
# (dispatch://INFRA-1545, round 37): a `$(` that never closes leaves the words before it in no
# command, and a comment on the line above and a `#` inside a word before the path do not make
# it a comment. zz-c.sh retries at 180 s, 1050 s.
echo "case: a path in text the gate reads no command in is refused as such, not read as a comment"
root=$(fixture sb-no-command)
cat > "$root/.github/scripts/zz-c.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
chmod +x "$root/.github/scripts/zz-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Name it before a substitution that never closes
        timeout-minutes: 1
        run: |
          true # a comment that names no script
          echo a#b .github/scripts/zz-c.sh $(
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
check "refuses the path at its line as a place it reads no command at" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=11::step 'Name it before a substitution that never closes' of job 'build' names .github/scripts/zz-c.sh in a place this check reads no command at, a place this check does not read as a call, and that script fetches")"

# A library sourced in a loop and again outside it is refused by name at the loop's source,
# whichever of the two is written first (dispatch://INFRA-1545, round 37). r37-lib.sh retries at
# 180 s, 5 x 180 + 150 = 1050 s; three passes sourcing it and sleeping 60 s are
# 3 x 1050 + 2 x 60 = 3270 s, and each step's key of 54 minutes, 3240 s, is under that. The gate
# before round 37's model, at 179c3dc9, read a path to a library as its names, so the two sources
# were one path and the wait of the source read later stood for both: it charged the step writing
# the loop first 3150 s and passed that key. A source inside a loop is now no call rule 8 charges,
# so neither loop is charged and each is refused by name; the source outside the loop is charged,
# 1050 s, which the key holds.
echo "case: a library sourced in a loop and again outside it is refused by name in either order"
root=$(fixture r37-library-order)
cat > "$root/.github/scripts/r37-lib.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: The loop written first
        timeout-minutes: 54
        run: |
          for pass in 1 2 3; do
            . .github/scripts/r37-lib.sh
            sleep 60
          done
          . .github/scripts/r37-lib.sh
      - name: The loop written last
        timeout-minutes: 54
        run: |
          . .github/scripts/r37-lib.sh
          for pass in 1 2 3; do
            . .github/scripts/r37-lib.sh
            sleep 60
          done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
for refusal in \
  "11	The loop written first" \
  "20	The loop written last"; do
  IFS=$'\t' read -r line step <<<"$refusal"
  check "refuses '$step' by name at the loop's source" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' names .github/scripts/r37-lib.sh in a source inside a loop, a place this check does not read as a call, and that script fetches")"
  check "tells '$step' to source the library once, before the loop" \
    "$(contains "$out" "step '$step' of job 'build' names .github/scripts/r37-lib.sh in a source inside a loop, .*Source the library once, before the loop")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 2)"

# A comment is where bash reads one, from a `#` that starts a word outside quotes to the end of
# its physical line (dispatch://INFRA-1545, round 37). `true;#` and `(true)#` each open one, so
# the path after them is no call and is neither charged nor refused; a comment line ending in a
# backslash ends at its newline all the same, so the run on the next line is charged, 1050 s;
# and a line starting with `#` inside a string spanning lines is part of the string, so the path
# on it is an argument of `die`, refused at that line. These were decided by the line's text:
# the first two were refused as a place no command is read at, the backslash joined the run to
# the comment and charged it nothing, and the string's line was left out as a comment.
echo "case: a comment is read where bash reads one, and nowhere else"
root=$(fixture r37-comments)
cp "$work/r37-library-order/.github/scripts/r37-lib.sh" "$root/.github/scripts/r37-c.sh"
chmod +x "$root/.github/scripts/r37-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Name it in a comment after a semicolon
        timeout-minutes: 1
        run: true;# .github/scripts/r37-c.sh fetches the asset; this step only names it
      - name: Name it in a comment after a subshell
        timeout-minutes: 1
        run: (true)# .github/scripts/r37-c.sh fetches the asset
      - name: Run it after a comment ending in a backslash
        timeout-minutes: 1
        run: |
          # the next line fetches \
          .github/scripts/r37-c.sh
      - name: Name it in a string whose second line starts with a hash
        timeout-minutes: 1
        run: |
          die() { echo "$*" >&2; exit 1; }
          die "see
            # .github/scripts/r37-c.sh"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for step in "Name it in a comment after a semicolon" "Name it in a comment after a subshell"; do
  check "neither charges nor refuses '$step'" "$(is "$(contains "$out" "step '$step'")" false)"
done
check "charges the run after the comment, 1050 s" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=14::step 'Run it after a comment ending in a backslash' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
check "refuses the path in the string as an argument of die, at its line" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=23::step 'Name it in a string whose second line starts with a hash' of job 'build' names .github/scripts/r37-c.sh in an argument of .die., a place this check does not read as a call, and that script fetches")"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 2)"

# bash's own options as rule 8 reads them (dispatch://INFRA-1545, round 37): `-o` and `+o` take
# the word after them as their value, so the script is the word after that, and the run is
# charged, 1050 s. A lone `-`, and the values `-O`, `+O`, `--rcfile` and `--init-file` take, are
# not modelled: each reads as the script, so the path after it is an argument of the shell,
# refused by name where its script fetches. That refusal is one this check accepts on a shape no
# step of this tree writes; round 37 read them as bash does for a while, and charged each run.
# Behind `-O extglob`, a `-c` is read the same way, the value of `-O` standing as the script.
echo "case: a run after bash -o is charged, and one after an option rule 8 does not model is refused as an argument"
root=$(fixture r37-bash-options)
cp "$work/r37-comments/.github/scripts/r37-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/r37-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Run it after bash -
        timeout-minutes: 1
        run: bash - .github/scripts/r37-c.sh
      - name: Run it after sh -
        timeout-minutes: 1
        run: sh - .github/scripts/r37-c.sh
      - name: Run it after bash -O extglob
        timeout-minutes: 1
        run: bash -O extglob .github/scripts/r37-c.sh
      - name: Run it after bash +O extglob
        timeout-minutes: 1
        run: bash +O extglob .github/scripts/r37-c.sh
      - name: Run it after bash -o pipefail
        timeout-minutes: 1
        run: bash -o pipefail .github/scripts/r37-c.sh
      - name: Run it after bash +o posix
        timeout-minutes: 1
        run: bash +o posix .github/scripts/r37-c.sh
      - name: Run it after bash --rcfile
        timeout-minutes: 1
        run: bash --rcfile /dev/null .github/scripts/r37-c.sh
      - name: Run it after bash --init-file
        timeout-minutes: 1
        run: bash --init-file /dev/null .github/scripts/r37-c.sh
      - name: Give it to bash -c after -O
        timeout-minutes: 1
        run: bash -O extglob -c .github/scripts/r37-c.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
for refusal in \
  "9	Run it after bash -	bash" \
  "12	Run it after sh -	sh" \
  "15	Run it after bash -O extglob	bash" \
  "18	Run it after bash +O extglob	bash" \
  "27	Run it after bash --rcfile	bash" \
  "30	Run it after bash --init-file	bash" \
  "33	Give it to bash -c after -O	bash"; do
  IFS=$'\t' read -r line step shell <<<"$refusal"
  check "refuses '$step' as an argument of $shell" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' names .github/scripts/r37-c.sh in an argument of .$shell., a place this check does not read as a call, and that script fetches")"
done
for step in "Run it after bash -o pipefail" "Run it after bash +o posix"; do
  check "charges '$step' one run, 1050 s" \
    "$(contains "$out" "step '$step' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 9)"

# A word of a `[[ … ]]`, an extglob pattern and a `case` pattern are not modelled
# (dispatch://INFRA-1545, round 37): their `(`, `)`, `|`, `<` and `>` read as the shell's own, so
# a path written there is a run charged in full or an argument refused where its script fetches,
# never nothing. In an extglob pattern, a parenthesised pattern, a pattern on its own line and a
# later pattern, the path reads as a run and is charged 1050 s, which the step does not wait for:
# an overcharge this check accepts on a shape no step of this tree writes. After `[[ -x` it is an
# argument of `-x`, refused by name. A run in a case arm, after a `[[ … ]]` and inside a `$(…)` a
# `[[ … ]]` tests is a run, 1050 s each. Round 37 read the patterns and the tests for a while, and
# refused each path written in one.
echo "case: a path in a [[ … ]] or a case pattern is charged as a run or refused, and a run beside one is charged"
root=$(fixture r37-tested-words)
cp "$work/r37-comments/.github/scripts/r37-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/r37-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Match it in an extglob pattern
        timeout-minutes: 1
        run: |
          [[ "$f" == @(.github/scripts/r37-c.sh|other) ]] && echo yes
      - name: Test it in an if
        timeout-minutes: 1
        run: |
          if [[ -x .github/scripts/r37-c.sh && ( -n "$f" || -z "$g" ) ]]; then echo yes; fi
      - name: Match it in a parenthesised case pattern
        timeout-minutes: 1
        run: |
          case "$f" in (.github/scripts/r37-c.sh) echo yes;; esac
      - name: Match it in a case pattern on its own line
        timeout-minutes: 1
        run: |
          case "$f" in
            other | .github/scripts/r37-c.sh) echo yes ;;
          esac
      - name: Match it in a later pattern
        timeout-minutes: 1
        run: |
          case "$f" in a) true;; (b|.github/scripts/r37-c.sh) true;; esac
      - name: Run it in a case arm
        timeout-minutes: 1
        run: |
          case "$kind" in (fetch) .github/scripts/r37-c.sh;; esac
      - name: Run it after a test
        timeout-minutes: 1
        run: |
          [[ -n "$f" ]] && .github/scripts/r37-c.sh
      - name: Run it in a substitution a test reads
        timeout-minutes: 1
        run: |
          [[ "$(.github/scripts/r37-c.sh)" == ok ]]
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
check "refuses the path after [[ -x as an argument of -x" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=14::step 'Test it in an if' of job 'build' names .github/scripts/r37-c.sh in an argument of .-x., a place this check does not read as a call, and that script fetches")"
for step in "Match it in an extglob pattern" "Match it in a parenthesised case pattern" \
  "Match it in a case pattern on its own line" "Match it in a later pattern" \
  "Run it in a case arm" "Run it after a test" "Run it in a substitution a test reads"; do
  check "charges '$step' one run, 1050 s" \
    "$(contains "$out" "step '$step' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 8)"

# A function spelled any way but `name() { … }` is refused by name where a call of a script that
# fetches follows it (dispatch://INFRA-1545, round 37): a blank before the `()`, with or without
# the `function` keyword, a body that is a `( … )` subshell, on the name's line or on the next,
# with or without the keyword, and a definition after a reserved word on its line. Read as the
# commands of the step, its run is charged once, 1050 s, which each step's key of 18 minutes,
# 1080 s, holds; the three steps calling their function twice wait 2100 s. The gate at 179c3dc9
# charged those three 1050 s and passed them: the blank and the reserved word hid the function,
# and the subshell's calls sat on its line. Round 37 then read the blank and the subshell as a
# function body, which no step of this tree writes. The subshell on the name's line is charged
# for both calls, which sit on the line of the function, 2100 s, and refused for that too.
echo "case: a function spelled with a blank, the function keyword, a subshell body or after a reserved word is refused by name"
root=$(fixture r37-function-spellings)
cp "$work/r37-comments/.github/scripts/r37-c.sh" "$root/.github/scripts/"
chmod +x "$root/.github/scripts/r37-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: A blank before the parentheses
        timeout-minutes: 18
        run: |
          fetch () { .github/scripts/r37-c.sh; }
          fetch
          fetch
      - name: The function keyword and a blank before the parentheses
        timeout-minutes: 18
        run: |
          function fetch () { .github/scripts/r37-c.sh; }
          fetch
      - name: A subshell body on the name's line
        timeout-minutes: 18
        run: |
          fetch() ( .github/scripts/r37-c.sh ); fetch; fetch
      - name: A subshell body on the next line
        timeout-minutes: 18
        run: |
          fetch()
          (
            .github/scripts/r37-c.sh
          )
          fetch
      - name: The function keyword and a subshell body
        timeout-minutes: 18
        run: |
          function fetch ( .github/scripts/r37-c.sh )
          fetch
      - name: A definition after a reserved word
        timeout-minutes: 18
        run: |
          if true; then fetch() { .github/scripts/r37-c.sh; }; fi
          fetch
          fetch
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
for refusal in \
  "10	A blank before the parentheses	with a blank before or inside its .()." \
  "16	The function keyword and a blank before the parentheses	with the .function. keyword" \
  "21	A subshell body on the name's line	with a body that is not a .{ … }. group" \
  "25	A subshell body on the next line	with a body that is not a .{ … }. group" \
  "33	The function keyword and a subshell body	with the .function. keyword" \
  "38	A definition after a reserved word	after a reserved word on its line"; do
  IFS=$'\t' read -r line step shape <<<"$refusal"
  check "refuses '$step' by name at its definition" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' defines a function $shape.*, a form this check does not read as a function, and a fetch, or a call of a script that fetches, is written after it")"
  check "tells '$step' the form to write" \
    "$(contains "$out" "step '$step' of job 'build' defines a function .*only for a function written .name() { …; }.*Write the function in that form")"
done
check "charges the subshell on the name's line for both calls, 2100 s" \
  "$(contains "$out" "step 'A subshell body on the name's line' carries .timeout-minutes: 18., 1080 s, and its own retries take 2100 s to exhaust")"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 7)"

# A fetch inside a function spelled any way but `name() { … }` is refused by name
# (dispatch://INFRA-1545, round 37): a blank before the `()`, the `function` keyword, a subshell
# body on the name's line or over several, an `if` body, and a function defined inside another
# function's body. Each step calls the function holding one `retry_fetch` at 300 s,
# 5 x 300 + 150 = 1650 s, twice, so it waits 2 x 1650 = 3300 s, and its key of 28 minutes,
# 1680 s, is under that. The gate at 179c3dc9 charged every one of them but the `if` body 1650 s
# and passed that key; round 37 then read each spelling but the nested one as a function, which
# no step of this tree writes. Read as commands of the step, each fetch is charged once, 1650 s,
# except where the calls sit on the line of the function, 3300 s, which the key does not hold.
echo "case: a fetch inside a function spelled any way but name() { … } is refused by name"
root=$(fixture r37-site-functions)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: A blank before the parentheses
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          fetch () { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }
          fetch
          fetch
      - name: The function keyword
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          function fetch { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }
          fetch
          fetch
      - name: A subshell body on the name's line
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          fetch() ( retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a ); fetch; fetch
      - name: A fetch below a subshell body's first command
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          fetch() (
            cd "$RUNNER_TEMP"
            retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a
          )
          fetch
          fetch
      - name: An if body
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          fetch() if true; then retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; fi
          fetch
          fetch
      - name: A function defined inside another
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          outer() {
            inner() { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }
          }
          outer
          inner
          inner
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for refusal in \
  "11	A blank before the parentheses	with a blank before or inside its .()." \
  "18	The function keyword	with the .function. keyword" \
  "25	A subshell body on the name's line	with a body that is not a .{ … }. group" \
  "30	A fetch below a subshell body's first command	with a body that is not a .{ … }. group" \
  "40	An if body	with a body that is not a .{ … }. group" \
  "48	A function defined inside another	inside another function's body"; do
  IFS=$'\t' read -r line step shape <<<"$refusal"
  check "refuses '$step' by name at its definition" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' defines a function $shape.*, a form this check does not read as a function, and a fetch, or a call of a script that fetches, is written after it")"
done
for step in "A subshell body on the name's line" "An if body"; do
  check "charges '$step' both calls, which sit on its line, 3300 s" \
    "$(contains "$out" "step '$step' carries .timeout-minutes: 28., 1680 s, and its own retries take 3300 s to exhaust")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 8)"

# A heredoc fed to a shell given `-O`, `+O`, `--rcfile` or `--init-file` is refused by name where
# its body holds a fetch or names a script that fetches (dispatch://INFRA-1545, round 37): this
# check reads that option's value as the shell's script and the body as data, where the shell
# runs the body, so the run of r37-c.sh in it, 1050 s, would be charged nothing under a key of
# 1 minute; the gate at 179c3dc9 passed each of those steps. A body holding a retried curl is
# refused the same way, and one that fetches nothing is not. Round 37 read those options as bash
# does for a while, and charged each body; no step of this tree writes one. `-e`, a lone `-` and
# `sh -eu` run their body, which is charged; a body fed to `bash -c` is data, since the `-c` text
# is the script; and a body fed to `bash - <script>` is read as shell, its `-` standing as the
# script, which charges a run the step does not make: an overcharge this check accepts.
echo "case: a heredoc fed to a shell given an option rule 8 does not read is refused by name where it fetches"
root=$(fixture r37-heredoc-options)
cat > "$root/.github/scripts/r37-c.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
cat > "$root/.github/scripts/r37-quiet.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
cat
SH
chmod +x "$root/.github/scripts/r37-c.sh" "$root/.github/scripts/r37-quiet.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Feed it to bash -O extglob
        timeout-minutes: 1
        run: |
          bash -O extglob <<'EOF'
          .github/scripts/r37-c.sh
          EOF
      - name: Feed it to bash +O extglob
        timeout-minutes: 1
        run: |
          bash +O extglob <<'EOF'
          .github/scripts/r37-c.sh
          EOF
      - name: Feed it to bash --rcfile
        timeout-minutes: 1
        run: |
          bash --rcfile /dev/null <<'EOF'
          .github/scripts/r37-c.sh
          EOF
      - name: Feed it to bash --init-file
        timeout-minutes: 1
        run: |
          bash --init-file /dev/null <<'EOF'
          .github/scripts/r37-c.sh
          EOF
      - name: Feed it to bash -e
        timeout-minutes: 1
        run: |
          bash -e <<'EOF'
          .github/scripts/r37-c.sh
          EOF
      - name: Feed it to bash -
        timeout-minutes: 1
        run: |
          bash - <<'EOF'
          .github/scripts/r37-c.sh
          EOF
      - name: Feed it to sh -eu
        timeout-minutes: 1
        run: |
          sh -eu <<'EOF'
          .github/scripts/r37-c.sh
          EOF
      - name: Feed it to bash -c
        timeout-minutes: 1
        run: |
          bash -c 'cat' <<'EOF'
          .github/scripts/r37-c.sh
          EOF
      - name: Feed it to a script bash runs after -
        timeout-minutes: 1
        run: |
          bash - .github/scripts/r37-quiet.sh <<'EOF'
          .github/scripts/r37-c.sh
          EOF
      - name: Feed a fetch to bash -O extglob
        timeout-minutes: 1
        run: |
          bash -O extglob <<'EOF'
          retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
          EOF
      - name: Feed nothing that fetches to bash -O extglob
        timeout-minutes: 1
        run: |
          bash -O extglob <<'EOF'
          echo "nothing to fetch"
          EOF
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
for refusal in \
  "10	Feed it to bash -O extglob" \
  "16	Feed it to bash +O extglob" \
  "22	Feed it to bash --rcfile" \
  "28	Feed it to bash --init-file" \
  "64	Feed a fetch to bash -O extglob"; do
  IFS=$'\t' read -r line step <<<"$refusal"
  check "refuses '$step' by name at its operator" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' feeds a heredoc to a shell given .-O., .+O., .--rcfile. or .--init-file., and the body holds a fetch or names a script that fetches")"
  check "tells '$step' to write the option in the body" \
    "$(contains "$out" "step '$step' of job 'build' feeds a heredoc .*Write the option in the body instead, .shopt -s <name>. for .-O <name>.")"
done
for step in "Feed it to bash -e" "Feed it to bash -" "Feed it to sh -eu" \
  "Feed it to a script bash runs after -"; do
  check "charges '$step' the run in its body, 1050 s" \
    "$(contains "$out" "step '$step' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
done
for step in "Feed it to bash -c" "Feed nothing that fetches to bash -O extglob"; do
  check "neither charges nor refuses '$step'" "$(is "$(contains "$out" "step '$step'")" false)"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 9)"

# A fetch in the text of a `bash -c` or an `eval` runs once for each pass of the loops around the
# command, and a loop or a function inside that text is refused by name where the text fetches
# (dispatch://INFRA-1545, round 37). Each fetch is one `retry_fetch` at 300 s,
# 5 x 300 + 150 = 1650 s, run twice, 3300 s, under a key of 28 minutes, 1680 s; the gate at
# 179c3dc9 charged each of the first five steps 1650 s and passed it. The text's commands took no
# loop around the command, and a loop or a function in the text was not read. Text whose loop
# fetches nothing is neither charged nor refused.
echo "case: a fetch in bash -c or eval text is charged for the loops around it, and a loop or a function in the text is refused by name"
root=$(fixture r37-shell-text)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: A fetch in bash -c text inside a loop
        timeout-minutes: 28
        run: |
          for pass in 1 2; do
            bash -c 'retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a'
          done
      - name: A fetch in eval text inside a loop
        timeout-minutes: 28
        run: |
          for pass in 1 2; do
            eval 'retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a'
          done
      - name: A loop in bash -c text
        timeout-minutes: 28
        run: |
          bash -c 'for pass in 1 2; do retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; done'
      - name: A function in bash -c text
        timeout-minutes: 28
        run: |
          bash -c 'fetch() { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }; fetch; fetch'
      - name: A loop in eval text
        timeout-minutes: 28
        run: |
          eval 'for pass in 1 2; do retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; done'
      - name: A loop in bash -c text that fetches nothing
        timeout-minutes: 28
        run: |
          bash -c 'for pass in 1 2; do echo "$pass"; done'
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for step in "A fetch in bash -c text inside a loop" "A fetch in eval text inside a loop"; do
  check "charges '$step' both passes, 3300 s" \
    "$(contains "$out" "step '$step' carries .timeout-minutes: 28., 1680 s, and its own retries take 3300 s to exhaust")"
done
for refusal in \
  "22	A loop in bash -c text" \
  "26	A function in bash -c text" \
  "30	A loop in eval text"; do
  IFS=$'\t' read -r line step <<<"$refusal"
  check "refuses '$step' by name at its line" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' runs .bash -c., .sh -c. or .eval. text that holds a loop or a function, and a fetch")"
  check "tells '$step' where to write the loop or the function" \
    "$(contains "$out" "step '$step' of job 'build' runs .bash -c.*Write the loop or the function in the step itself, or in a script under .github/scripts/ that the step runs")"
done
check "neither charges nor refuses the text that fetches nothing" \
  "$(is "$(contains "$out" "step 'A loop in bash -c text that fetches nothing'")" false)"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 5)"

# A library one shell reaches along two paths of different products is charged at the larger,
# whichever of the two is written first (dispatch://INFRA-1545, round 37). ka-lib.sh defines a
# function retrying at 180 s and calls it nowhere, so it costs 1050 s once; ka-run.sh sources it
# and calls that function. Each step sources ka-lib.sh itself, a product of 1, and runs ka-run.sh
# three times, which reaches ka-lib.sh with a product of 3: the shell waits for three calls of
# the function, 3 x 1050 = 3150 s, and sourcing the library defines it and fetches nothing. A
# walk keeping the path found first charged the step sourcing first 1050 s; one keeping the path
# found last, or the path whose names sort first whatever its product, charged the other 1050 s.
echo "case: a library reached at two products is charged at the larger, in either order"
root=$(fixture ka-library-products)
cat > "$root/.github/scripts/ka-lib.sh" <<'SH'
#!/usr/bin/env bash
. .github/scripts/ci-retry.sh
ka_fetch() {
  retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
}
SH
cat > "$root/.github/scripts/ka-run.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ka-lib.sh
ka_fetch
SH
chmod +x "$root/.github/scripts/ka-run.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Source first, then the looped run
        timeout-minutes: 1
        run: |
          . .github/scripts/ka-lib.sh
          for pass in 1 2 3; do .github/scripts/ka-run.sh; done
      - name: The looped run, then source
        timeout-minutes: 1
        run: |
          for pass in 1 2 3; do .github/scripts/ka-run.sh; done
          . .github/scripts/ka-lib.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
for charge in \
  "8	Source first, then the looped run" \
  "13	The looped run, then source"; do
  IFS=$'\t' read -r line step <<<"$charge"
  check "charges '$step' the looped run's product, 3150 s" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' carries .timeout-minutes: 1., 60 s, and its own retries take 3150 s to exhaust")"
  # The step sources the library and runs a script that sources it again, two paths, so it is
  # refused by name as well where no DECLARED_LIBRARIES entry covers it, though here the larger
  # product is the step's whole wait (dispatch://INFRA-1545, round 37).
  check "refuses '$step' by name for the library it reaches along two paths" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' reaches .github/scripts/ka-lib.sh, a library that fetches, along more than one path")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 4)"

# A library reached along two paths of one product is charged through the path whose names sort
# first, whichever of the two is written first (dispatch://INFRA-1545, round 37). ka-z.sh retries
# at 180 s, 1050 s, and sources ka-lib.sh, which runs ka-z.sh once a job, the first time any
# shell sources it: the marker file it touches is seen by every later shell. Each step runs
# ka-z.sh and sources ka-lib.sh, so ka-lib.sh is reached at a product of 1 both from the step
# and through ka-z.sh. The shell runs ka-z.sh twice in either order, 2 x 1050 = 2100 s: the
# step's run, and ka-lib.sh's one run, from whichever shell sourced it first. The path the step
# sources it on, (ka-lib.sh), sorts before (ka-z.sh, ka-lib.sh), and through it ka-lib.sh's run
# of ka-z.sh is a run to charge, where through ka-z.sh it re-enters a file the path is inside and
# is charged nothing. A walk keeping the path it found first charged the step running ka-z.sh
# first 1050 s; one keeping the path it found last, the step sourcing first. Each script reaches
# itself through the other, and each fetches, so each is refused by name at its call of the
# other as well: the same shell under other names was charged 1050 s, and a guard each process
# sets for itself runs ka-z.sh three times, which no walk charges (dispatch://INFRA-1545, round
# 37).
echo "case: a library reached along two paths of one product is charged through the path that sorts first, in either order"
root=$(fixture ka-library-tie)
cat > "$root/.github/scripts/ka-lib.sh" <<'SH'
#!/usr/bin/env bash
[ -e "$RUNNER_TEMP/ka-z.done" ] || { touch "$RUNNER_TEMP/ka-z.done"; .github/scripts/ka-z.sh; }
SH
cat > "$root/.github/scripts/ka-z.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
. .github/scripts/ka-lib.sh
SH
chmod +x "$root/.github/scripts/ka-z.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Run first, then source
        timeout-minutes: 1
        run: |
          .github/scripts/ka-z.sh
          . .github/scripts/ka-lib.sh
      - name: Source first, then run
        timeout-minutes: 1
        run: |
          . .github/scripts/ka-lib.sh
          .github/scripts/ka-z.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
for charge in \
  "8	Run first, then source" \
  "13	Source first, then run"; do
  IFS=$'\t' read -r line step <<<"$charge"
  check "charges '$step' both runs of ka-z.sh, 2100 s" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' carries .timeout-minutes: 1., 60 s, and its own retries take 2100 s to exhaust")"
  check "refuses '$step' by name for the library it reaches along two paths" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' reaches .github/scripts/ka-lib.sh, a library that fetches, along more than one path")"
done
for refusal in \
  ".github/scripts/ka-z.sh	5	.github/scripts/ka-lib.sh" \
  ".github/scripts/ka-lib.sh	2	.github/scripts/ka-z.sh"; do
  IFS=$'\t' read -r script line target <<<"$refusal"
  check "refuses $script, which reaches itself, at its call of $target" \
    "$(contains "$out" "::error file=$script,line=$line::$script runs or sources $target, and that call reaches $script again")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 6)"

# Where a heredoc's consumer is read, and which line its refusal names (dispatch://INFRA-1545,
# round 37). A body fed to `bash -e -` is the shell's script, since the `-` after the options is
# standard input spelled out: its run of kb-c.sh, 1050 s, is the step's own and is charged against
# the key of 1 minute, 60 s. A body fed to a shell given `-O` is refused by name where it fetches,
# and the shell is the LAST command in front of the operator, whatever runs before it on the line
# (`cd … && bash -O extglob <<'EOF'`), and a shell written by its path (`/bin/bash`) is the same
# shell. The refusal names the operator's own line, which here is the run's second. Round 37's
# mutation pass found each of these read another way and the step then passed, its run charged
# nothing: the `-` looked up at the wrong word, the first command of the line taken as the
# consumer, `/bin/bash` read as the directory, and the line counted back from the `run:` key.
echo "case: a heredoc's shell is read after bash's options, as the line's last command and by its path, and refused at its operator"
root=$(fixture kb-heredoc-consumers)
cat > "$root/.github/scripts/kb-c.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
chmod +x "$root/.github/scripts/kb-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Feed it to bash -e -
        timeout-minutes: 1
        run: |
          bash -e - <<'EOF'
          .github/scripts/kb-c.sh
          EOF
      - name: Feed it to bash -O extglob after a cd
        timeout-minutes: 1
        run: |
          cd "$RUNNER_TEMP" && bash -O extglob <<'EOF'
          .github/scripts/kb-c.sh
          EOF
      - name: Feed it to /bin/bash -O extglob
        timeout-minutes: 1
        run: |
          /bin/bash -O extglob <<'EOF'
          .github/scripts/kb-c.sh
          EOF
      - name: Feed it to bash -O extglob below a line
        timeout-minutes: 1
        run: |
          echo start
          bash -O extglob <<'EOF'
          .github/scripts/kb-c.sh
          EOF
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
check "charges 'Feed it to bash -e -' the run in its body, 1050 s" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=8::step 'Feed it to bash -e -' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
for refusal in \
  "16	Feed it to bash -O extglob after a cd" \
  "22	Feed it to /bin/bash -O extglob" \
  "29	Feed it to bash -O extglob below a line"; do
  IFS=$'\t' read -r line step <<<"$refusal"
  check "refuses '$step' by name at its operator, line $line" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' feeds a heredoc to a shell given .-O., .+O., .--rcfile. or .--init-file., and the body holds a fetch or names a script that fetches")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 4)"

# A function rule 8 does not read is refused where a fetch is written on the last line its reach
# holds, that line included (dispatch://INFRA-1545, round 37). Each step calls its function twice,
# and each call makes the one `retry_fetch` at 300 s, 5 x 300 + 150 = 1650 s, so the step waits
# 3300 s; read as commands of the step, the fetch is charged once, 1650 s, which the key of
# 28 minutes, 1680 s, holds, so the refusal is all that stands between the step and a pass. The
# first function is spelled with a blank before its `()`, whose reach runs to the end of the text,
# and it is written on the last line of a `|-` block, which ends with no newline. The second is
# defined inside another function's body, whose reach ends at that body's `}`, on the same line as
# the fetch. Round 37's mutation pass found the reach ending a line short and both steps passing.
echo "case: a function rule 8 does not read is refused for a fetch on the last line of its reach"
root=$(fixture kb-function-reach)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: A blank function on the last line of a stripped block
        timeout-minutes: 28
        run: |-
          . .github/scripts/ci-retry.sh
          fetch () { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }; fetch; fetch
      - name: A nested function whose fetch is on the outer closing line
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          outer() {
            inner() { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }; }
          outer
          inner
          inner
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for refusal in \
  "11	A blank function on the last line of a stripped block	with a blank before or inside its .()." \
  "17	A nested function whose fetch is on the outer closing line	inside another function's body"; do
  IFS=$'\t' read -r line step shape <<<"$refusal"
  check "refuses '$step' by name at its definition" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' defines a function $shape, a form this check does not read as a function, and a fetch, or a call of a script that fetches, is written after it")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 2)"

# A shape rule 8 refuses by name is refused for a fetch under `timeout 0`, which is no bound, and
# not for a fetch that waits for nothing (dispatch://INFRA-1545, round 37). The gate at d766bd4b
# read `timeout 0` as a bound of 0 s, so a `curl --retry 7 --retry-all-errors --retry-max-time 1`
# under it was charged 8 x 0 + 1 = 1 s a call, and round 37's mutation pass found each shape
# asking whether a call costs 1 s rather than 0 s. `timeout 0` sets no limit, so the curl's
# attempts are unbounded and have no figure: fed to `bash -O extglob`, the body is data to every
# other rule, so the refusal by name is the only thing that stops the step; in the credentialed
# publish job the curl is exempted from the other rules (exception (d)), so a function with the
# `function` keyword and a loop in `bash -c` text are each refused by name, and the curl in each
# is refused as an excepted site making unbounded attempts of its own. A bare `gh api` in a
# `bash -c` loop of the same job waits for nothing, so it is refused by nobody: a shape asking
# whether a call costs other than 1 s refused it.
echo "case: a shape refused by name is refused for a fetch under timeout 0, and not for one that waits for nothing"
root=$(fixture kb-one-second-fetch)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Feed a one-second curl to bash -O extglob
        timeout-minutes: 1
        run: |
          bash -O extglob <<'EOF'
          timeout 0 curl -fsSL --retry 7 --retry-all-errors --retry-max-time 1 -o b https://example.com/b
          EOF
YAML
cat >> "$root/.github/workflows/release.yaml" <<'YAML'
      - name: Read it in a function with the keyword
        timeout-minutes: 1
        run: |
          function latest { timeout 0 curl -fsSL --retry 7 --retry-all-errors --retry-max-time 1 -o l "https://api.github.com/repos/$GITHUB_REPOSITORY/releases/latest"; }
          latest
          latest
      - name: Read it in a bash -c loop
        timeout-minutes: 1
        run: |
          bash -c 'for pass in 1 2; do timeout 0 curl -fsSL --retry 7 --retry-all-errors --retry-max-time 1 -o l "https://api.github.com/repos/$GITHUB_REPOSITORY/releases/latest"; done'
      - name: Read it bare in a bash -c loop
        run: |
          bash -c 'for pass in 1 2; do gh api "repos/$GITHUB_REPOSITORY/releases/latest"; done'
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
check "refuses the heredoc fed to bash -O extglob by name at its operator" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=10::step 'Feed a one-second curl to bash -O extglob' of job 'build' feeds a heredoc to a shell given .-O., .+O., .--rcfile. or .--init-file., and the body holds a fetch or names a script that fetches")"
check "refuses the function with the keyword by name at its definition" \
  "$(contains "$out" "::error file=.github/workflows/release.yaml,line=23::step 'Read it in a function with the keyword' of job 'publish' defines a function with the .function. keyword, a form this check does not read as a function, and a fetch, or a call of a script that fetches, is written after it")"
check "refuses the loop in bash -c text by name at its line" \
  "$(contains "$out" "::error file=.github/workflows/release.yaml,line=29::step 'Read it in a bash -c loop' of job 'publish' runs .bash -c., .sh -c. or .eval. text that holds a loop or a function, and a fetch")"
for refusal in "22	Read it in a function with the keyword" "28	Read it in a bash -c loop"; do
  IFS=$'\t' read -r line step <<<"$refusal"
  check "refuses the curl of '$step' as an excepted site making unbounded attempts" \
    "$(contains "$out" "::error file=.github/workflows/release.yaml,line=$line::.curl. in step '$step' is a site an exception above leaves unretried, and it makes attempts of its own with no bound this check finds")"
done
check "refuses nothing at the bare read in a bash -c loop" \
  "$(is "$(contains "$out" "Read it bare in a bash -c loop")" false)"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 5)"

# Five readings of a function that round 37's mutation pass found no case holding
# (dispatch://INFRA-1545, round 37). Each step fetches through one `retry_fetch` at 300 s,
# 5 x 300 + 150 = 1650 s, which runs twice, 3300 s, under a key of 28 minutes, 1680 s, so a
# reading that charges the fetch once passes the key and only a refusal holds the step:
#   - a function defined inside another over several lines is refused at its definition, and
#     its reach runs to the outer function's closing brace, not to its own line alone;
#   - a function whose body is an `if` is refused by name though its test ends in a `{` word,
#     which is an argument of `[` and opens no group;
#   - a quoted command word that ends in `()`, `'quiet()' fetchzz`, is a command bash cannot find
#     and defines nothing, and the next word of that command names no function either: the
#     `fetch` defined before it is still the one its two calls run;
#   - of two functions written on one line, the first holds the fetch and is the one called, and
#     the step is charged both calls; the second definition starts on the line the first body
#     ends on, which round 37 refuses by name too, since with the fetch in the second the line
#     went to the first and the fetch was charged once (dispatch://INFRA-1545, round 37);
#   - `bash -c` text defining a function with the `function` keyword, which writes no `name()`,
#     is refused as text holding a function.
echo "case: five readings of a function the gate refuses or charges for both calls"
root=$(fixture kc-function-readings)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: A nested function over several lines
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          outer() {
            inner() {
              retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a
            }
          }
          outer
          inner
          inner
      - name: An if body whose test ends in a brace
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          fetch()
          if [ -n { ]; then retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; fi
          fetch
          fetch
      - name: A command word quoted to end in parentheses
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          fetch() {
            retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a
          }
          fetch
          fetch
          'quiet()' fetchzz || true
      - name: Two functions on one line, the fetch in the first
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          fetch() { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }; quiet() { :; }
          fetch
          fetch
      - name: A function with the keyword in bash -c text
        timeout-minutes: 28
        run: |
          bash -c 'function fetch { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }; fetch; fetch'
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
check "refuses the nested function at its definition, for the fetch on the line below it" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=12::step 'A nested function over several lines' of job 'build' defines a function inside another function's body, a form this check does not read as a function, and a fetch, or a call of a script that fetches, is written after it")"
check "refuses the if body at its name though its test ends in a brace" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=23::step 'An if body whose test ends in a brace' of job 'build' defines a function with a body that is not a .{ … }. group")"
for step in "A command word quoted to end in parentheses" "Two functions on one line, the fetch in the first"; do
  check "charges '$step' both calls of the function holding the fetch, 3300 s" \
    "$(contains "$out" "step '$step' carries .timeout-minutes: 28., 1680 s, and its own retries take 3300 s to exhaust")"
done
check "refuses the bash -c text defining a function with the keyword" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=47::step 'A function with the keyword in bash -c text' of job 'build' runs .bash -c., .sh -c. or .eval. text that holds a loop or a function, and a fetch")"
check "refuses the second definition on the first one's closing line by name" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=41::step 'Two functions on one line, the fetch in the first' of job 'build' defines a function on the line another function's body ends on")"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 6)"

# A library function that calls itself inside a loop of no pass is called from outside the
# library alone (dispatch://INFRA-1545, round 37): the loop around its own call, `for ((i = 0;
# i < 0; i++))`, runs no pass, so the step that sources the library and calls the function once
# waits for one `retry_fetch` at 300 s, 1650 s, which its key of 27 minutes, 1620 s, does not
# exceed. A call inside the function's own body is no call of it: counted as one, its no passes
# multiply the library's fetch by 0, and the step is charged nothing. Round 37 refuses both
# readings of the library by name as well, since each can hide passes where it is written
# otherwise: the function that calls itself, at its definition, and the loop of no pass, at its
# line (dispatch://INFRA-1545, round 37).
echo "case: a library function's call of itself is no call of it"
root=$(fixture kc-library-recursion)
cat > "$root/.github/scripts/kc-lib.sh" <<'SH'
#!/usr/bin/env bash
. .github/scripts/ci-retry.sh
fetch_asset() {
  retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a
  for ((i = 0; i < 0; i++)); do
    fetch_asset
  done
}
SH
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Source the library and fetch once
        timeout-minutes: 27
        run: |
          . .github/scripts/kc-lib.sh
          fetch_asset
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "charges the library's fetch once, 1650 s" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=8::step 'Source the library and fetch once' carries .timeout-minutes: 27., 1620 s, and its own retries take 1650 s to exhaust")"
for refusal in \
  "3	defines a function that calls itself, directly or through another function" \
  "5	writes a loop whose written count is no pass"; do
  IFS=$'\t' read -r line shape <<<"$refusal"
  check "refuses kc-lib.sh at line $line by name" \
    "$(contains "$out" "::error file=.github/scripts/kc-lib.sh,line=$line::.github/scripts/kc-lib.sh $shape")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 3)"

# A step written as a flow mapping holds its `run:` on the key's own line, so a refusal inside it
# names that line, even where the step's name before the key holds the text `run: >`
# (dispatch://INFRA-1545, round 37): only a `|` or a `>` with nothing after it but an indentation
# figure, a chomping sign and a comment opens a block scalar, whose text starts a line below. The
# step defines a function with a blank before its `()` and calls it, which rule 8 refuses by name
# at the definition.
echo "case: a refusal in a flow-mapping step names the key's line, whatever the name before it holds"
root=$(fixture kc-flow-step)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - {name: "A run: > in a name", timeout-minutes: 28, run: ". .github/scripts/ci-retry.sh; fetch () { retry_fetch \"the asset\" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }; fetch"}
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "refuses the definition on the step's own line" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=7::step 'A run: > in a name' of job 'build' defines a function with a blank before or inside its .().")"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 1)"

# A function holding a loop runs what it is handed in the first loop its body writes, which is
# the loop rule 2 and rule 8 name (dispatch://INFRA-1545, round 37): here the `for` at line 10
# makes three passes of the curl the call hands it, and the `while` after it runs `:` alone. The
# curl's `--max-time` is `"$T"`, so rule 8 names the variable bound, and the passes of the `for`
# are read; a reading taking the function's last loop named the `while` at line 11 as the loop
# the curl runs in, in both refusals.
echo "case: a function's words run in the first loop its body writes"
root=$(fixture kc-handed-loops)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Hand the curl to the first of two loops
        timeout-minutes: 28
        run: |
          each() {
            for pass in 1 2 3; do "$@"; done
            while read -r tidy; do :; done < tidy.txt
          }
          each curl -fsSL --retry 7 --retry-all-errors --max-time "$T" -o a https://example.com/a
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the for loop as the copy of the retry" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=10::this loop retries .curl. itself")"
check "names the call's bound as a variable" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=8::step 'Hand the curl to the first of two loops' carries .timeout-minutes: 28. and this check can put no figure on it: it runs a retried call whose attempts are bounded by a variable rather than by a number — .curl. at .github/workflows/case.yaml:13\.")"
check "refuses nothing at the while loop's line" "$(is "$(contains "$out" 'line=11::')" false)"
check "does not name the while loop as the loop the curl runs in" "$(is "$(contains "$out" 'loop at line 11')" false)"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 2)"

# A call of a function written after a reserved word or a wrapper on its line is a call of it
# (dispatch://INFRA-1545, round 37): `do fetch`, `then fetch`, `GH_HOST=x fetch`, `! fetch` and
# `time fetch` each run the function's one `retry_fetch` at 300 s, 5 x 300 + 150 = 1650 s, so
# each step below waits 3300 s and its key of 28 minutes, 1680 s, is under that. The gate at
# d766bd4b read a call only where the function's name was its command's first word, charged
# each step 1650 s and passed it. This tree writes such calls (`if fn`, `! fn`, `elif ! fn`), so
# they are read, with the wrappers reduce_words strips from any command.
echo "case: a call of a function after a reserved word or a wrapper is a call of it"
root=$(fixture u37-calls-after-word)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: A call after do in a one-line loop
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          fetch() { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }
          for pass in 1 2; do fetch; done
      - name: A call after then, and one more
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          fetch() { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }
          if true; then fetch; fi; fetch
      - name: A call behind an assignment and one after a bang
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          fetch() { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }
          GH_HOST=example.com fetch
          ! fetch
      - name: A call after time and one in a brace group
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          fetch() { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }
          time fetch
          { fetch; }
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for charge in \
  "8	A call after do in a one-line loop" \
  "14	A call after then, and one more" \
  "20	A call behind an assignment and one after a bang" \
  "27	A call after time and one in a brace group"; do
  IFS=$'\t' read -r line step <<<"$charge"
  check "charges '$step' both calls, 3300 s" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' carries .timeout-minutes: 28., 1680 s, and its own retries take 3300 s to exhaust")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 4)"

# Four readings of a function that hide a call of its fetch are refused by name, none of them
# written in this tree (dispatch://INFRA-1545, round 37). Each step's fetch is one `retry_fetch`
# at 300 s, 1650 s, which runs twice, 3300 s, and each is charged 1650 s, under its key of 28
# minutes, 1680 s, so the refusal is what holds it; the gate at d766bd4b passed all four. A name
# defined again after its calls kept the second body only, so the first body's two calls were
# charged once; a definition on the line another function's body ends on gave its line, and so
# its fetch, to that other function; a function calling itself was charged for its outside
# call alone; and a function this check does not read, here one with the `function` keyword,
# was refused for a fetch from its own line on, which missed the fetch in the function it
# calls, written above it.
echo "case: a function defined again, defined on a closing line, calling itself or called from a function this check does not read is refused by name"
root=$(fixture u37-function-readings)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: A function defined again after its calls
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          fetch() { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }
          fetch
          fetch
          fetch() { :; }
          fetch
      - name: A definition on the line another body ends on
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          quiet() { :; }; fetch() { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }
          fetch
          fetch
      - name: A function that calls itself
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          fetch() { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; [ -e done ] || { touch done; fetch; }; }
          fetch
      - name: A function with the keyword calling one written above it
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          get() { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }
          function twice { get; }
          twice
          twice
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for refusal in \
  "11	A function defined again after its calls	that the same text defines again under the same name.*Give each definition a name of its own" \
  "20	A definition on the line another body ends on	on the line another function's body ends on.*Start each definition on a line of its own" \
  "27	A function that calls itself	that calls itself, directly or through another function.*Write the repetition as a loop" \
  "34	A function with the keyword calling one written above it	with the .function. keyword"; do
  IFS=$'\t' read -r line step shape <<<"$refusal"
  check "refuses '$step' by name" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' defines a function $shape")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 4)"

# Three loops this check reads by line are refused by name where the text fetches, none of them
# written in this tree (dispatch://INFRA-1545, round 37). Two loops on one line: the second's
# three passes priced the first's five, 3 x 1650 = 4950 s where the step waits 8250 s, under a
# key of 120 minutes, 7200 s. A loop after `then`: read as no loop, its two passes were charged
# once, 1650 s where the step waits 3300 s, under 28 minutes. A loop of no pass beside a fetch on
# its line: the fetch, which runs once, was multiplied by those no passes and charged nothing.
# The gate at d766bd4b passed all three.
echo "case: two loops on one line, a loop after a reserved word and a loop of no pass are refused by name"
root=$(fixture u37-loop-readings)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Two loops on one line
        timeout-minutes: 120
        run: |
          . .github/scripts/ci-retry.sh
          for a in 1 2 3 4 5; do retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; done; for b in 1 2 3; do :; done
      - name: A loop after then
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          if true; then for a in 1 2; do retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; done; fi
      - name: A loop of no pass beside a fetch
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          for ((i = 0; i < 0; i++)); do :; done; retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for refusal in \
  "11	Two loops on one line	two loops on one line.*Write each loop on lines of its own" \
  "16	A loop after then	a loop after a reserved word on its line.*Open the loop on a line of its own" \
  "21	A loop of no pass beside a fetch	a loop whose written count is no pass.*Delete the loop"; do
  IFS=$'\t' read -r line step shape <<<"$refusal"
  check "refuses '$step' by name" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' writes $shape")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 3)"

# An array whose words this check cannot count makes the loop over its length a loop whose
# passes it cannot read, which is refused (dispatch://INFRA-1545, round 37). `($names)` splits
# into as many words as the run finds, `lists[2]=c` adds a third word to two, and `mapfile` fills
# an array written empty. The gate at d766bd4b counted one, two and no passes, so each step's
# fetch at 1650 s a pass was charged 1650 s, 3300 s and nothing, each passing its key.
echo "case: a loop over an array whose words are not all written out is refused for its passes"
root=$(fixture u37-array-counts)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: An array of an unquoted expansion
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          lists=($names)
          for ((i = 0; i < ${#lists[@]}; i++)); do
            retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a
          done
      - name: An array given an element after its words
        timeout-minutes: 56
        run: |
          . .github/scripts/ci-retry.sh
          lists=(a b)
          lists[2]=c
          for ((i = 0; i < ${#lists[@]}; i++)); do
            retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a
          done
      - name: An array mapfile fills
        timeout-minutes: 28
        run: |
          . .github/scripts/ci-retry.sh
          lists=()
          mapfile -t lists < lists.txt
          for ((i = 0; i < ${#lists[@]}; i++)); do
            retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a
          done
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for refusal in \
  "8	An array of an unquoted expansion	12	11" \
  "16	An array given an element after its words	21	20" \
  "25	An array mapfile fills	30	29"; do
  IFS=$'\t' read -r line step site loop <<<"$refusal"
  check "refuses '$step' for the loop's unread passes" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' carries .timeout-minutes: [0-9]*. and this check can put no figure on it: it runs a retried call inside a loop whose passes it cannot read — .curl. at .github/workflows/case.yaml:$site, inside the loop at line $loop\.")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 3)"

# A `timeout` whose duration this check cannot read as seconds above 0, and a curl `--max-time
# 0`, bound nothing, and the fetch behind an option of `timeout` stays a fetch
# (dispatch://INFRA-1545, round 37). The gate at d766bd4b read `timeout 1.5`'s duration, `-k`'s
# value and `-s`'s signal as the command, so each bare curl behind them was no fetch at all and
# passed; read `--max-time 0` as a bound written as a variable; and read `timeout 0` as a bound
# of 0 s, so a curl in a credentialed publish job, which no other rule reads, retrying itself
# under it was charged 1 s and passed a key of 1 minute. The same job's bare read in a function
# with the `function` keyword waits for nothing, and is refused by nobody. The curls behind `-k`
# and `-s` are bounded at 60 s and read at curl's floor, 8 x 60 + 127 = 607 s, which their keys
# of 11 minutes hold, so rule 1 is all that refuses them.
echo "case: a timeout or a max-time this check cannot read as seconds above 0 bounds nothing, and hides no fetch"
root=$(fixture u37-timeout-bounds)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: A curl under timeout 1.5
        run: timeout 1.5 curl -fsSL -o a https://example.com/a
      - name: A curl under timeout -k 5 60
        timeout-minutes: 11
        run: timeout -k 5 60 curl -fsSL -o a https://example.com/a
      - name: A curl under timeout -s KILL 60
        timeout-minutes: 11
        run: timeout -s KILL 60 curl -fsSL -o a https://example.com/a
      - name: A curl whose max-time is 0
        timeout-minutes: 28
        run: curl -fsSL --retry 7 --retry-all-errors --max-time 0 -o a https://example.com/a
YAML
cat >> "$root/.github/workflows/release.yaml" <<'YAML'
      - name: Read it under timeout 0
        timeout-minutes: 1
        run: timeout 0 curl -fsSL --retry 7 --retry-all-errors --retry-max-time 1 -o l "https://api.github.com/repos/$GITHUB_REPOSITORY/releases/latest"
      - name: Read it bare in a function with the keyword
        run: |
          function latest { gh api "repos/$GITHUB_REPOSITORY/releases/latest"; }
          latest
          latest
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for line in 8 11 14; do
  check "reads the curl at line $line as a fetch, and refuses it for the floor" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::.curl. in step .* fetches without the floor: it lacks .--retry 7.")"
done
check "refuses the curl whose max-time is 0 for a bound above 0" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=17::.curl. in step 'A curl whose max-time is 0' fetches without the floor: it lacks .--max-time <seconds>., which curl resets for each attempt, above 0, since .--max-time 0. sets no limit")"
check "says no bound of it is written as a variable" "$(is "$(contains "$out" 'written as a variable')" false)"
check "refuses the curl under timeout 0 in the publish job as unbounded" \
  "$(contains "$out" "::error file=.github/workflows/release.yaml,line=22::.curl. in step 'Read it under timeout 0' is a site an exception above leaves unretried, and it makes attempts of its own with no bound this check finds")"
check "refuses nothing at the bare read in a function with the keyword" \
  "$(is "$(contains "$out" 'line=2[3-7]::')" false)"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 5)"

# A heredoc fed to a shell given `-O`, `+O`, `--rcfile` or `--init-file` is refused by name for
# any fetch in its body, a bare one or a retried `mise x` included (dispatch://INFRA-1545, round
# 37): no other rule reads that body, so the rules that refuse a bare `wget`, a bare `docker
# pull` and a retried `mise x` written in a step see none of them there. The gate at d766bd4b
# refused such a body only where one call of a fetch in it cost more than nothing, and passed
# all three; the body holding a curl under `timeout 0`, which is no bound, is refused as well.
echo "case: a heredoc fed to a shell given an option rule 8 does not read is refused for any fetch in its body"
root=$(fixture u37-heredoc-any-fetch)
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: A bare wget fed to bash -O extglob
        run: |
          bash -O extglob <<'EOF'
          wget -q -O a https://example.com/a
          EOF
      - name: A bare docker pull fed to bash +O extglob
        run: |
          bash +O extglob <<'EOF'
          docker pull example.com/image:1
          EOF
      - name: A retried mise x fed to bash --rcfile
        run: |
          bash --rcfile /dev/null <<'EOF'
          . .github/scripts/ci-retry.sh
          retry_fetch "the tool" 60 -- mise x node@22 -- node --version
          EOF
      - name: A curl under timeout 0 fed to bash -O extglob
        timeout-minutes: 1
        run: |
          bash -O extglob <<'EOF'
          timeout 0 curl -fsSL --retry 7 --retry-all-errors --retry-max-time 1 -o b https://example.com/b
          EOF
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for refusal in \
  "9	A bare wget fed to bash -O extglob" \
  "14	A bare docker pull fed to bash +O extglob" \
  "19	A retried mise x fed to bash --rcfile" \
  "26	A curl under timeout 0 fed to bash -O extglob"; do
  IFS=$'\t' read -r line step <<<"$refusal"
  check "refuses '$step' by name at its operator" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' feeds a heredoc to a shell given .-O., .+O., .--rcfile. or .--init-file., and the body holds a fetch or names a script that fetches")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 4)"

# A run on the line after a backslash continuation is read against the loops of the line its
# command starts on (dispatch://INFRA-1545, round 37). u37-c.sh retries at 180 s, 1050 s. After
# `echo start \`, and after a double-quoted string the backslash continues, a three-pass loop
# opens on the next line and runs the script three times, 3150 s, over a key of 30 minutes,
# 1800 s; a `while read` opening there has no pass count. The gate at d766bd4b keyed each run
# to the line its logical line starts on, the line before the loop: it charged the first two
# 1050 s and refused nothing in the third, passing all three.
echo "case: a run after a continued line is read against the loops of the line its command starts on"
root=$(fixture u37-continued-lines)
cat > "$root/.github/scripts/u37-c.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
chmod +x "$root/.github/scripts/u37-c.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Bare continuation before a loop
        timeout-minutes: 30
        run: |
          echo start \
          && for pass in 1 2 3; do .github/scripts/u37-c.sh; done
      - name: Quoted continuation before a loop
        timeout-minutes: 30
        run: |
          echo "start\
          "; for pass in 1 2 3; do .github/scripts/u37-c.sh; done
      - name: Bare continuation before an uncounted loop
        timeout-minutes: 30
        run: |
          echo start \
          && while read -r id; do .github/scripts/u37-c.sh "$id"; done <<<"$ids"
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for charge in \
  "8	Bare continuation before a loop" \
  "13	Quoted continuation before a loop"; do
  IFS=$'\t' read -r line step <<<"$charge"
  check "charges '$step' three runs, 3150 s" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' carries .timeout-minutes: 30., 1800 s, and its own retries take 3150 s to exhaust")"
done
check "refuses the uncounted loop on the continued line" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=18::step 'Bare continuation before an uncounted loop' carries .timeout-minutes: 30. and this check can put no figure on it: it runs a retried call inside a loop whose passes it cannot read — .\.github/scripts/u37-c\.sh. at .github/workflows/case.yaml:20, inside the loop at line 20\.")"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 3)"

# A script that reaches itself and fetches is refused by name at the call that leads back
# (dispatch://INFRA-1545, round 37): a path stops at a script it is already inside, so the run
# the shell makes on entering it again was charged nothing. u37-self.sh retries at 180 s,
# 1050 s, and runs itself once more behind a guard, 2100 s; u37-a.sh retries the same and runs
# u37-b.sh, which runs u37-a.sh once a job. The gate at d766bd4b charged each step 1050 s,
# under its key of 30 minutes, and refused nothing.
echo "case: a script that reaches itself and fetches is refused by name"
root=$(fixture u37-self-reach)
cat > "$root/.github/scripts/u37-self.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
[ -n "${U37_INNER:-}" ] || U37_INNER=1 .github/scripts/u37-self.sh
SH
cat > "$root/.github/scripts/u37-a.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
.github/scripts/u37-b.sh
SH
cat > "$root/.github/scripts/u37-b.sh" <<'SH'
#!/usr/bin/env bash
[ -e "$RUNNER_TEMP/u37.done" ] || { touch "$RUNNER_TEMP/u37.done"; .github/scripts/u37-a.sh; }
SH
chmod +x "$root/.github/scripts/u37-self.sh" "$root/.github/scripts/u37-a.sh" \
  "$root/.github/scripts/u37-b.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Run the script that runs itself
        timeout-minutes: 30
        run: .github/scripts/u37-self.sh
      - name: Run the script that another runs again
        timeout-minutes: 30
        run: .github/scripts/u37-a.sh
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for refusal in \
  ".github/scripts/u37-self.sh	5	.github/scripts/u37-self.sh" \
  ".github/scripts/u37-a.sh	5	.github/scripts/u37-b.sh" \
  ".github/scripts/u37-b.sh	2	.github/scripts/u37-a.sh"; do
  IFS=$'\t' read -r script line target <<<"$refusal"
  check "refuses $script at its call of $target" \
    "$(contains "$out" "::error file=$script,line=$line::$script runs or sources $target, and that call reaches $script again")"
done
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 3)"

# A call of a function through a variable, in a text that defines a function that fetches, is
# refused by name unless declared, and a declaration's figure enters the step's arithmetic
# (dispatch://INFRA-1545, round 37). The step calls `fetch`, one `retry_fetch` at 300 s,
# 5 x 300 + 150 = 1650 s, twice through `"$fn"`, 3300 s, which its key of 55 minutes, 3300 s,
# does not exceed; the site is charged once as written, 1650 s, and the gate at d766bd4b passed
# the step. A DECLARED_CALLS entry writing the second call's 1650 s puts the step at 3300 s,
# which that key is refused for and a key of 56 minutes passes; an entry matching no call fails.
echo "case: a call of a function through a variable is refused unless declared, and its declaration is charged"
root=$(fixture u37-variable-call)
write_variable_case() {
  cat > "$root/.github/workflows/case.yaml" <<YAML
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch through a variable
        timeout-minutes: $1
        run: |
          . .github/scripts/ci-retry.sh
          fetch() { retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a; }
          fn=fetch
          "\$fn"
          "\$fn"
YAML
}
write_variable_case 55
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
check "refuses the call through the variable by name, naming the fetch it can reach" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=13::step 'Fetch through a variable' of job 'build' calls a function through a variable, .\\\$fn., and a function this text defines or sources fetches — .curl. at .github/workflows/case.yaml:10\.")"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 1)"
declare_in "$root" DECLARED_CALLS '    DeclaredCall(".github/workflows/case.yaml", "Fetch through a variable", "$fn", 1650, "the second of two calls of fetch, the first being the site this check counts: 1 x (5 x 300 + 150) = 1650 s"),'
declare_in "$root" DECLARED_CALLS '    DeclaredCall(".github/workflows/case.yaml", "No such step", "$fn", 0, "a step this fixture does not write"),'
run_check "$root"
check "with the call declared, charges the step the declared figure beside the site, 3300 s" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=8::step 'Fetch through a variable' carries .timeout-minutes: 55., 3300 s, and its own retries take 3300 s to exhaust")"
check "refuses the declaration that matches no call" \
  "$(contains "$out" "::error file=.github/scripts/check-download-retry.sh,line=1::the declared call figure for .\\\$fn. in .github/workflows/case.yaml step 'No such step' matches no call through that variable")"
check "with the call declared, refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 2)"
ln -sf "$check_script" "$root/.github/scripts/check-download-retry.sh"
declare_in "$root" DECLARED_CALLS '    DeclaredCall(".github/workflows/case.yaml", "Fetch through a variable", "$fn", 1650, "the second of two calls of fetch, the first being the site this check counts: 1 x (5 x 300 + 150) = 1650 s"),'
write_variable_case 56
run_check "$root"
check "with the call declared and a key above 3300 s, passes" "$(is "$status" 0)"

# A library a step reaches along more than one path is refused by name unless declared
# (dispatch://INFRA-1545, round 37). u37-lib.sh defines u37_fetch, one `retry_fetch` at 300 s,
# 1650 s, and the step sources it and calls the function, and runs u37-run.sh, which sources it
# and calls the function again: 3300 s, which a key of 55 minutes does not exceed. The library
# is charged once, at its most expensive path, 1650 s, and the gate at d766bd4b passed the step.
# A DECLARED_LIBRARIES entry writing 3300 s replaces that charge, so the key of 55 minutes is
# refused and one of 56 passes; an entry matching no step fails.
echo "case: a library a step reaches along more than one path is refused unless declared, and its declaration is charged"
root=$(fixture u37-shared-library)
cat > "$root/.github/scripts/u37-lib.sh" <<'SH'
#!/usr/bin/env bash
. .github/scripts/ci-retry.sh
u37_fetch() {
  retry_fetch "the asset" 300 -- curl -fsSL --max-time 300 -o a https://example.com/a
}
SH
cat > "$root/.github/scripts/u37-run.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/u37-lib.sh
u37_fetch
SH
chmod +x "$root/.github/scripts/u37-run.sh"
write_shared_case() {
  cat > "$root/.github/workflows/case.yaml" <<YAML
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Source the library and run a script that sources it too
        timeout-minutes: $1
        run: |
          . .github/scripts/u37-lib.sh
          u37_fetch
          .github/scripts/u37-run.sh
YAML
}
write_shared_case 55
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
check "refuses the step by name for the library it reaches twice" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=8::step 'Source the library and run a script that sources it too' reaches .github/scripts/u37-lib.sh, a library that fetches, along more than one path")"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 1)"
library_entry='    DeclaredLibrary(".github/workflows/case.yaml", "Source the library and run a script that sources it too", ".github/scripts/u37-lib.sh", 3300, "one call of u37_fetch from the step and one from u37-run.sh: 2 x (5 x 300 + 150) = 3300 s"),'
declare_in "$root" DECLARED_LIBRARIES "$library_entry"
declare_in "$root" DECLARED_LIBRARIES '    DeclaredLibrary(".github/workflows/case.yaml", "No such step", ".github/scripts/u37-lib.sh", 1650, "a step this fixture does not write"),'
run_check "$root"
check "with the library declared, charges the step the declared 3300 s" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=8::step 'Source the library and run a script that sources it too' carries .timeout-minutes: 55., 3300 s, and its own retries take 3300 s to exhaust")"
check "refuses the declaration that matches no step" \
  "$(contains "$out" "::error file=.github/scripts/check-download-retry.sh,line=1::the declared library figure for .github/workflows/case.yaml step 'No such step' .github/scripts/u37-lib.sh matches no step")"
check "with the library declared, refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 2)"
ln -sf "$check_script" "$root/.github/scripts/check-download-retry.sh"
declare_in "$root" DECLARED_LIBRARIES "$library_entry"
write_shared_case 56
run_check "$root"
check "with the library declared and a key above 3300 s, passes" "$(is "$status" 0)"

# A library's fetching function called more than once after sourcing it is refused by name
# unless declared, for the step that calls it or for the script whose calls they are
# (dispatch://INFRA-1545, round 37). The step calls u37_fetch in a two-pass loop, and
# u37-twice.sh, which a second step runs, calls it twice: each waits 3300 s under a key of 55
# minutes, and each is charged the library once, 1650 s, which the gate at d766bd4b passed. A
# DECLARED_LIBRARIES entry for the step, and one for the script with no step, each writing
# 3300 s, replace that charge, so each key of 55 minutes is refused and keys of 56 pass.
echo "case: a library's fetching function called more than once is refused unless declared, for the step or for the script"
root=$(fixture u37-library-calls)
cp "$work/u37-shared-library/.github/scripts/u37-lib.sh" "$root/.github/scripts/"
cat > "$root/.github/scripts/u37-twice.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/u37-lib.sh
u37_fetch
u37_fetch
SH
chmod +x "$root/.github/scripts/u37-twice.sh"
write_calls_case() {
  cat > "$root/.github/workflows/case.yaml" <<YAML
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Fetch twice through the library
        timeout-minutes: $1
        run: |
          . .github/scripts/u37-lib.sh
          for pass in 1 2; do u37_fetch; done
      - name: Run the script that fetches twice
        timeout-minutes: $1
        run: .github/scripts/u37-twice.sh
YAML
}
write_calls_case 55
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
check "refuses the step's loop of calls by name" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=11::step 'Fetch twice through the library' of job 'build' calls .u37_fetch., which .github/scripts/u37-lib.sh defines and which fetch, more than once")"
check "refuses the script's two calls by name, naming the step that runs it" \
  "$(contains "$out" "::error file=.github/scripts/u37-twice.sh,line=4::.github/scripts/u37-twice.sh calls .u37_fetch., which .github/scripts/u37-lib.sh defines and which fetch, more than once.*step 'Run the script that fetches twice' of .github/workflows/case.yaml reaches it")"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 2)"
declare_in "$root" DECLARED_LIBRARIES '    DeclaredLibrary(".github/workflows/case.yaml", "Fetch twice through the library", ".github/scripts/u37-lib.sh", 3300, "two passes of one call of u37_fetch: 2 x (5 x 300 + 150) = 3300 s"),'
declare_in "$root" DECLARED_LIBRARIES '    DeclaredLibrary(".github/scripts/u37-twice.sh", None, ".github/scripts/u37-lib.sh", 3300, "two calls of u37_fetch: 2 x (5 x 300 + 150) = 3300 s"),'
run_check "$root"
for step in "Fetch twice through the library" "Run the script that fetches twice"; do
  check "with the calls declared, charges '$step' the declared 3300 s" \
    "$(contains "$out" "step '$step' carries .timeout-minutes: 55., 3300 s, and its own retries take 3300 s to exhaust")"
done
check "with the calls declared, refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 2)"
write_calls_case 56
run_check "$root"
check "with the calls declared and keys above 3300 s, passes" "$(is "$status" 0)"

# A heredoc whose shell the command in front of its operator does not name as this check reads
# one is refused by name where its body fetches, and a `-c` after `bash -o pipefail` is read as
# the text it runs (dispatch://INFRA-1545, round 37). u37-hc.sh retries at 180 s, 1050 s, and
# each step runs it, or the same retried curl, under a key of 1 minute. The gate at d766bd4b read
# each body as data and the `-c` text as no text, so it charged every step nothing and passed
# all six: a shell named by a variable, `ssh` and `/usr/bin/env` naming the shell among their
# words, a `cat` piped into `bash`, an unquoted body whose command substitution the step's own
# shell runs, and the `-c` text written after an option's value.
echo "case: a heredoc whose shell this check cannot name is refused for a fetch in its body, and -c text after -o is read"
root=$(fixture u37-heredoc-consumers)
cat > "$root/.github/scripts/u37-hc.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
. .github/scripts/ci-retry.sh
retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b
SH
chmod +x "$root/.github/scripts/u37-hc.sh"
cat > "$root/.github/workflows/case.yaml" <<'YAML'
name: Case
on: [push]
jobs:
  build:
    runs-on: ubuntu-24.04
    steps:
      - name: Feed it to a shell named by a variable
        timeout-minutes: 1
        run: |
          "$SHELL" <<'EOF'
          .github/scripts/u37-hc.sh
          EOF
      - name: Feed it to the shell ssh runs
        timeout-minutes: 1
        run: |
          ssh build-host bash <<'EOF'
          .github/scripts/u37-hc.sh
          EOF
      - name: Feed it to bash through env by its path
        timeout-minutes: 1
        run: |
          /usr/bin/env bash <<'EOF'
          .github/scripts/u37-hc.sh
          EOF
      - name: Pipe a printed copy into bash
        timeout-minutes: 1
        run: |
          cat <<'EOF' | bash
          .github/scripts/u37-hc.sh
          EOF
      - name: Substitute a run in an unquoted body
        timeout-minutes: 1
        run: |
          cat > out.txt <<EOF
          $(.github/scripts/u37-hc.sh)
          EOF
      - name: Run -c text after an option's value
        timeout-minutes: 1
        run: |
          . .github/scripts/ci-retry.sh
          bash -o pipefail -c 'retry_fetch "a large asset" 180 -- curl -fsSL --max-time 180 -o b https://example.com/b'
YAML
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "no traceback" "$(is "$(contains "$out" 'Traceback')" false)"
for refusal in \
  "10	Feed it to a shell named by a variable	a shell named by a variable, .\\\$SHELL." \
  "16	Feed it to the shell ssh runs	.ssh., which names a shell among its words" \
  "22	Feed it to bash through env by its path	./usr/bin/env., which names a shell among its words" \
  "28	Pipe a printed copy into bash	a command whose output a pipe on the operator's line hands to a shell" \
  "34	Substitute a run in an unquoted body	a command that reads it as data, unquoted and holding a command substitution, which the step's own shell runs before that command reads the body"; do
  IFS=$'\t' read -r line step consumer <<<"$refusal"
  check "refuses '$step' by name at its operator" \
    "$(contains "$out" "::error file=.github/workflows/case.yaml,line=$line::step '$step' of job 'build' feeds a heredoc to $consumer, and the body holds a fetch or names a script that fetches")"
done
check "charges the -c text after -o pipefail its retried curl, 1050 s" \
  "$(contains "$out" "::error file=.github/workflows/case.yaml,line=38::step 'Run -c text after an option's value' carries .timeout-minutes: 1., 60 s, and its own retries take 1050 s to exhaust")"
check "refuses nothing else" "$(is "$(printf '%s\n' "$out" | grep -c '^::error')" 6)"

# The promise rule 8 exists to keep: no retried job whose key is below its own sum reaches main.
# Every case above proves one shape on a fixture written for it, which leaves open whether a shape
# this repository's own tree holds hides a key, so this case drives the promise on a copy of the
# tree itself. For each job --jobs lists, it lowers the key to the whole minutes of the job's own
# sum, which is at or under that sum, and asks the gate to refuse that job's key by name. The sum
# is the gate's: the largest of the slowest retried step's cost and, where the block that prices
# the key holds a derivation whose arrow names the key and adds up, the last such derivation's
# total and its measured term plus that cost. That block is the one --jobs names in its sixth and
# seventh columns: the case reads the gate's pairing and walks to no block of its own, since review
# round 29 of dispatch://INFRA-1545 found it re-implementing the gate's pairing walk. Its program,
# .github/scripts/download_retry_real_tree.py, reads the derivation inside that block, which is
# the case's oracle for the sum. A mutant of the gate is then judged by whether it can hide a real
# key, not by whether some fixture happened to hold the shape. The shapes are seven ways a lowered
# key can be written:
#   key           the key alone, its block left naming the old figure;
#   arrows        the key, and every arrow of its block naming the old key moved to the new one;
#   decoy-after   arrows, and a derivation whose arrow names another key appended to the block;
#   decoy-before  arrows, and a 2 s derivation naming the new key as the block's first line;
#   split         arrows, and the derivation the gate reads broken over two lines after its `=`;
#   neighbour     the job's own block deleted, and a step-depth comment naming the new key written
#                 directly above its id, which prices no job (every job but its file's first);
#   more-figures  the job's own block deleted, and the previous job's last comment block given
#                 arrows naming the new key, more than the keys left below it before this job's
#                 id, which ends its reach (only where nothing but YAML lies between that block
#                 and this job's key, since a comment line there ends the reach anyway).
# A refusal of one of the job's STEP keys does not count: the promise is about the job's key. Each
# shape prints how many jobs it lowered. `key`, `arrows` and both decoys lower every job --jobs
# lists and must lower exactly that many, and the other three must lower one at least, so a shape
# that silently lowers fewer is seen; and the list the case walks is first held to the tree, so it
# is not narrower than it: every workflow job that writes its own whole-number `timeout-minutes`
# and holds a site --list prints must be in --jobs. A key written as an expression is outside, as
# the gate's header says under A CEILING WRITTEN AS AN EXPRESSION (e2e.yaml's `proof`).
# THIS CASE FOUND A GAP THE FIXTURES DID NOT: at the head before round 35, `arrows`, both decoys
# and `split` left `download-retry`, `download-retry-images`, `re-record`, `embedder` and
# `visual` unnamed, because the derivation clause read only the comment block directly above the
# key and those five write their derivations above the job's id (dispatch://INFRA-1545, round 35).
# It is eight gate calls over the tree and one more for --list.
echo "case: every retried job of this repository's own tree, its key lowered below its own sum, is refused by name"
repository=$(cd "$script_dir/../.." && pwd)
tree="$work/real-tree"
mkdir -p "$tree/packages/daemon/docker" "$tree/packages/envoy/docker" \
  "$tree/packages/envoy/infra/host/heldportwrapper"
cp -R "$repository/.github" "$tree/.github"
for dockerfile in packages/daemon/docker/worker.Dockerfile packages/envoy/docker/Dockerfile \
  packages/envoy/infra/host/heldportwrapper/Dockerfile; do
  cp "$repository/$dockerfile" "$tree/$dockerfile"
done
real_tree="$script_dir/download_retry_real_tree.py"
set +e
run_gate null "$tree" --jobs
set -e
jobs_status=$status
check "--jobs over the tree exits 0" "$(is "$jobs_status" 0)"
if [ -n "$out" ]; then printf '%s\n' "$out"; fi > "$work/real-tree.jobs"
listed=$(python3 "$real_tree" rows "$work/real-tree.jobs")
check "--jobs lists at least one job (it lists $listed)" "$([ "$listed" -ge 1 ] && echo true || echo false)"
list_check "$tree"
check "--list over the tree exits 0" "$(is "$status" 0)"
if [ -n "$out" ]; then printf '%s\n' "$out"; fi > "$work/real-tree.sites"
omitted=$(python3 "$real_tree" cover "$tree" "$work/real-tree.jobs" "$work/real-tree.sites")
check "--jobs omits no workflow job that writes its own whole-number key and holds a site --list prints${omitted:+; it omits $omitted}" \
  "$(is "$omitted" "")"
# The shapes lower the jobs --jobs lists, so where it refused the tree or listed none there is
# nothing for them to lower, and the case says so in a check of its own rather than seven shapes
# that each lower nothing.
if [ "$jobs_status" -ne 0 ] || [ "$listed" -lt 1 ]; then
  check "the seven shapes are skipped: --jobs over the tree exited $jobs_status and listed $listed job(s), so there is no job for them to lower" false
else
  for shape in key arrows decoy-after decoy-before split neighbour more-figures; do
    copy="$work/real-tree-$shape"
    cp -R "$tree" "$copy"
    python3 "$real_tree" shape "$tree" "$work/real-tree.jobs" "$shape" "$copy" > "$copy.done"
    lowered=$(grep -c '^lowered' "$copy.done" || true)
    if [ "$shape" = split ]; then
      echo "  $shape: lowered $lowered job(s), split $(grep -c '^split' "$copy.done" || true) derivation(s)"
    else
      echo "  $shape: lowered $lowered job(s)"
    fi
    # These four shapes lower every job --jobs lists, so each is held to that number and cannot
    # shrink in silence; the other three lower only the jobs their shape fits, and are held to
    # one at least.
    case "$shape" in
      key | arrows | decoy-after | decoy-before)
        check "$shape: lowers every job --jobs lists ($lowered of $listed)" "$(is "$lowered" "$listed")"
        ;;
      *)
        check "$shape: lowers at least one job" "$([ "$lowered" -ge 1 ] && echo true || echo false)"
        ;;
    esac
    run_check "$copy"
    check "$shape: exits 1" "$(is "$status" 1)"
    printf '%s\n' "$out" > "$copy.out"
    unnamed=$(python3 "$real_tree" unnamed "$copy.done" "$copy.out")
    check "$shape: a refusal of its own key names every job it lowered${unnamed:+; none names $unnamed}" \
      "$(is "$unnamed" "")"
  done
fi

summary "check-download-retry.sh"
