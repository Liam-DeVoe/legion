#!/usr/bin/env bash
# Tests for the reading of a step's script that `go-test-step-expansion.py` and
# `check-test-reach.sh` share (`go_test_steps.py`): which command a line runs, and which shapes it
# refuses rather than guesses at; that it reads every `go test` step of this head's workflows;
# and that each script AUDITED_SCRIPTS reads still has the SHA-256 its entry pins.
#
# Only the reader and the pins. What the expansion makes of a command - the packages a
# `go test -list` names, the pairs two steps share, the exceptions - needs all three Go modules'
# test binaries built, which is minutes of compile and why no CI job runs that script (its own
# header says so). This harness needs no Go at all: every case is text, read as the script reads
# it, or a file's hash.
#
# The case that named this harness: a step whose line set a variable before its command, or ran
# it under the memory sampler, was refused, and the refusal stopped the whole run at exit 2 with
# no pair list at all - so the rule answered nothing about any pair while
# `RACE_MEM_LIMIT_KB=… race_memory.py go test …` stood in the race-docs job's file and
# `GODEBUG=checkfinalizers=1 go test …` in the finalizers job's. Both are .github/scripts/go-test.sh
# commands in the go-envoy-held legs' actions now: `RACE_MEM_LIMIT_KB=… go-test.sh --race-memory …`,
# read as the gotestsum command it runs, and `go-test.sh … -exec 'env GODEBUG=checkfinalizers=1'`.
#
# The modules are imported from the directory they live in, the way check-ci-filters.sh imports
# its own (PYTHONPATH, no bytecode), so the code under test is the file a person and CI run. It
# needs a python3 with PyYAML, which the modules import.
#
# Run from anywhere: .github/scripts/go-test-step-expansion.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$script_dir/../.." && pwd)

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

# py <statements> [argument ...]: the statements with the shared reader as `steps` and the
# arguments in sys.argv[1:]. go-test-step-expansion.py is loaded too, by path, since its name holds
# dashes, which no import statement can spell: a reader it no longer imports cleanly fails every
# case here. PYTHONPATH names the directory beside them so their own imports resolve as they do
# for the scripts.
py() {
  local statements=$1
  shift
  PYTHONDONTWRITEBYTECODE=1 PYTHONPATH="$script_dir" python3 -c "import importlib.util, sys
spec = importlib.util.spec_from_file_location('expansion', '$script_dir/go-test-step-expansion.py')
expansion = importlib.util.module_from_spec(spec)
# @dataclass reads sys.modules for the class's own module, so register it before it executes.
sys.modules['expansion'] = expansion
spec.loader.exec_module(expansion)
import go_test_steps as steps
$statements" "$@"
}

# command_of <line>: the words of the go command the line runs, space-separated.
command_of() { py 'print(" ".join(steps.go_command(sys.argv[1])[0]))' "$1"; }

# text_of <line>: the command's text, which keeps the line's own quoting.
text_of() { py 'print(steps.go_command(sys.argv[1])[1])' "$1"; }

echo "case: a plain go test line is itself"
line='go test -count=1 ./internal/dispatch/docs/'
check "its words are the command's" "$(is "$(command_of "$line")" "go test -count=1 ./internal/dispatch/docs/")"
check "its text is the line" "$(is "$(text_of "$line")" "$line")"

echo "case: a variable set before the command (the finalizers lane)"
line='GODEBUG=checkfinalizers=1 go test -v -count=1 -timeout 29m ./internal/dispatch/docs/'
check "the variable is dropped" "$(is "$(command_of "$line")" "go test -v -count=1 -timeout 29m ./internal/dispatch/docs/")"
check "the text begins at go" "$(is "$(text_of "$line")" "go test -v -count=1 -timeout 29m ./internal/dispatch/docs/")"

echo "case: a variable and a go-test.sh command (the race-docs lane)"
line='RACE_MEM_LIMIT_KB=8388608 ../../.github/scripts/go-test.sh --race-memory --jsonfile envoy-go-race-docs-race.json --packages ./internal/dispatch/docs/ -- -race -count=1 -timeout 60m'
check "the variable is dropped and the go-test.sh command is left" \
  "$(is "$(command_of "$line")" "../../.github/scripts/go-test.sh --race-memory --jsonfile envoy-go-race-docs-race.json --packages ./internal/dispatch/docs/ -- -race -count=1 -timeout 60m")"

echo "case: more than one variable before the command"
check "each is dropped" "$(is "$(command_of 'A=1 B=2 go test ./pkg')" "go test ./pkg")"

echo "case: a go-test.sh command is read as the go test gotestsum runs, whatever path names it"
check "its --packages, its go test flags after --, and the variable before it" \
  "$(is "$(py 'step = steps.Step(workflow="(a test case)", job="(a job)", name="(a step)",
                  workdir="packages/envoy", script=sys.argv[1])
command, = steps.commands_of(step, lambda step, text: "(not-run)")
print(" ".join(command.packages), command.race, command.run, command.env)' \
    "RACE_MEM_LIMIT_KB=1 ../../../.github/scripts/go-test.sh --race-memory --rerun-fails-max-failures 3 --jsonfile a.json --packages './a/ ./b/' -- -count=1 -race -run '^(TestOne)\$'")" \
    "./a/ ./b/ True ^(TestOne)\$ {'RACE_MEM_LIMIT_KB': '1'}")"

echo "case: a -run pattern keeps its quoting"
line="GODEBUG=checkfinalizers=1 go test -run '^(TestOne|TestTwo)\$' ./pkg"
check "the text keeps the quotes the line wrote" \
  "$(is "$(text_of "$line")" "go test -run '^(TestOne|TestTwo)\$' ./pkg")"

echo "case: what it leaves for the caller to refuse"
check "a value needing the shell is left in place" \
  "$(is "$(command_of 'GOFLAGS=$EXTRA go test ./pkg')" 'GOFLAGS=$EXTRA go test ./pkg')"
check "a command the reader does not read is left in place" \
  "$(is "$(command_of 'timeout 60 go test ./pkg')" "timeout 60 go test ./pkg")"
check "the memory sampler written before a go command is no wrapper of it: it is left in place" \
  "$(is "$(command_of '../../.github/scripts/race_memory.py go test ./pkg')" "../../.github/scripts/race_memory.py go test ./pkg")"
check "a bare assignment is left in place" "$(is "$(command_of 'NAME=value')" "NAME=value")"

echo "case: the two steps whose lines this harness exists for are read as their actions write them"
for file in .github/actions/go-envoy-held/envoy-go-race-docs/action.yml .github/actions/go-envoy-held/envoy-go-docs-finalizers/action.yml; do
  found=$(py 'import pathlib, yaml
document = yaml.safe_load(pathlib.Path(steps.ROOT / sys.argv[1]).read_text())
lines = [line.strip() for step in document["runs"]["steps"] if "run" in step
         for line in step["run"].strip().splitlines()]
print(sum(1 for line in lines
          for words, _ in [steps.go_command(line)]
          if (len(words) > 1 and words[0] == "go" and words[1] == "test")
          or (words and words[0].rsplit("/", 1)[-1] in steps.GOTESTSUM_COMMANDS)))' "$file")
  check "$file holds a go test or go-test.sh line this reader reads" "$(is "$found" "1")"
done

# Four steps of this repository carry a `-skip` (daemon-go's two, envoy-go's unit step and
# dispatch-multiprocess's), each in the space form below, so that form is reachable from a real
# step and the equals form from none, which is why this case holds both. The parse is what these
# cases hold: measured at
# b9e4127d0d78, `-skip '<pattern>'` left the pattern in the command's packages, where `go list`
# refuses it as a malformed import path and the whole run stops at exit 2 naming a `go list`
# failure rather than the flag; `-skip=<pattern>` was dropped whole, and the step was reported as
# running every test of its packages, the skipped ones included, which is the shape that makes the
# exactly-once rule wrong: a test that runs nowhere reads as covered, and one the step skips reads
# as a duplicate.
echo "case: a -skip is read rather than dropped, in either form a step may write it"
pattern='^(TestTheShippedWordsNameTheWaitingLineBand)$'
for form in "-skip '$pattern'" "-skip=$pattern"; do
  read -r in_packages retained <<<"$(py 'step = steps.Step(workflow="(a test case)", job="(a job)",
               name="(a step)", workdir="packages/envoy", script="")
text = f"go test -count=1 {sys.argv[1]} ./internal/textcheck/"
try:
    command = steps.parse_go_test(step, text, [])
except steps.Refused:
    # A refusal is no reading: it leaves the rule with nothing to say about the pattern, which is
    # the hole the two checks below are here to find. A refusal must therefore fail this case, so
    # print what neither check accepts rather than an answer one of them would take.
    print("refused refused")
else:
    # `text` is the line itself and holds the pattern whatever the parse made of it, so the
    # question is whether any field the parse writes holds it.
    parsed = {name: value for name, value in vars(command).items()
              if name not in ("step", "text", "packages", "resolved")}
    print("yes" if sys.argv[2] in command.packages else "no",
          "yes" if any(sys.argv[2] in str(value) for value in parsed.values()) else "no")' \
    "$form" "$pattern")"
  check "$form does not leave its pattern among the packages" "$(is "$in_packages" "no")"
  check "$form is kept, so the tests it skips can be subtracted" "$(is "$retained" "yes")"
done

# Two steps run database-skips.sh, each with the argument list its entry reads: the envoy-go legs'
# count, which runs the broker's packages less the race lane's, and its command, and the
# envoy-go-race-api legs' race step, which runs the API package; each passes its leg's share as the
# split's --run. Each step is read out of the file that holds it - ci.yaml's go-envoy-held job, and
# the race-api legs' action - in the directory GitHub runs it from, so the case holds the entry to
# the steps as written; an argument list the entry does not list is refused, since what the script
# runs then is a reading nobody made.
echo "case: a step that runs an audited script is read through its entry, by its argument list"
read_audited() { py 'import ci_filter_coverage, pathlib, yaml
document = yaml.safe_load(pathlib.Path(steps.ROOT / sys.argv[1]).read_text())
if "runs" in document:
    found = [(None, step) for step in document["runs"]["steps"] if step.get("name") == sys.argv[2]]
else:
    found = [(job, step) for job in document["jobs"].values() for step in job.get("steps", [])
             if step.get("name") == sys.argv[2]]
job, step = found[0]
workdir = (steps.action_workdir(step, {}) if job is None
           else ci_filter_coverage.step_workdir(document, job, step))
try:
    read = steps.audited_step(sys.argv[1], "(a job)", sys.argv[2], workdir, step["run"] + sys.argv[3])
except steps.Refused:
    print("refused")
else:
    print(read.workdir, read.script)' "$@"; }
check "the envoy-go legs' count runs the broker's packages less the race lane's, and its command, less the backend count's test, split between the legs" \
  "$(is "$(read_audited .github/workflows/ci.yaml "Count the database broker suites' skips" "")" \
    "packages/envoy go list ./internal/cipher/... ./cmd/cipherd/ | grep -Ev '/internal/cipher/natscallout$' | xargs go test -json -count=1 -skip '^(TestSettingsNameTheGoogleExchangeAudienceOnEitherStore)\$' -run \"\$(../../.github/scripts/go-test-split.sh)\" -timeout 15m")"
check "the envoy-go-race-api legs' race step runs the API package under -race through the counter, split between the legs" \
  "$(is "$(read_audited .github/actions/go-envoy-held/envoy-go-race-api/action.yml "Race-test the Dispatch API" "")" \
    "packages/envoy go test -json -count=1 -race -run \"\$(../../.github/scripts/go-test-split.sh)\" -timeout 26m ./internal/dispatch/api/")"
check "the race step with one more argument, which the entry does not list, is refused" \
  "$(is "$(read_audited .github/actions/go-envoy-held/envoy-go-race-api/action.yml "Race-test the Dispatch API" " --race")" "refused")"

# The envoy-go legs' "Count the store-form rows on each backend" runs backend-count.sh from the
# repository root and holds no `go test` of its own, so until AUDITED_SCRIPTS read that script
# the reader skipped the step, and the second run of its five tests (both packages also run whole
# in other steps of the job) was seen by no reader: O15 of dispatch://INFRA-1408. The step is
# read here as both readers read it, out of every step of this head: a step of the envoy-go legs'
# action, keyed as ci.yaml's step that uses it. The count runs them in the same job, env and build
# as the steps that run their packages whole, so those steps skip them and the count is their one
# run: the second check holds each to the one step that skips it - the unit step, the envoy-go
# legs' split command, and ci.yaml's database-skips step - read with a stand-in answer for each
# step's lister, so no command runs.
echo "case: envoy-go's backend count is read as the go test its five tests run in"
check "the step runs its five tests in its two packages, from packages/envoy" \
  "$(is "$(py 'found = [command for step in steps.steps_of(steps.documents())
         if step.key == (".github/workflows/ci.yaml", "envoy-go")
         for command in steps.commands_of(step, lambda step, text: "(not-run)")
         if command.packages == ["./cmd/cipher/", "./internal/cipher/api/"]]
if not found:
    print("not read")
else:
    command = found[0]
    print(command.step.workdir, " ".join(command.packages), command.run)')" \
    "packages/envoy ./cmd/cipher/ ./internal/cipher/api/ ^(TestEverySecretFormNamesTheSourceItUses|TestNoSecretFormAsksTheBrokerForACredential|TestUseCommandOutsideASessionReachesTheMachineSource|TestTheChainSkipsAbsenceAndStopsOnABrokenSource|TestSettingsNameTheGoogleExchangeAudienceOnEitherStore)\$")"
check "the unit step skips the four of cmd/cipher and the database-skips step the one of internal/cipher/api" \
  "$(is "$(py 'found = steps.steps_of(steps.documents())
def commands(name):
    return [command for step in found if step.key == (".github/workflows/ci.yaml", name)
            for command in steps.commands_of(step, lambda step, text: "(not-run)")]
sources = {"Unit test envoy Go package": [command for command in commands(sys.argv[1]) if command.split],
           sys.argv[2]: commands(sys.argv[2])}
skipping = {name: {test for command in found_commands for test in steps.skip_names(command.skip) or ()}
            for name, found_commands in sources.items()}
print(" ".join(test + ":" + ",".join(name for name, tests in skipping.items() if test in tests)
               for test in steps.BACKEND_COUNT_TESTS))' "envoy-go" "Count the database broker suites' skips")" \
    "TestEverySecretFormNamesTheSourceItUses:Unit test envoy Go package TestNoSecretFormAsksTheBrokerForACredential:Unit test envoy Go package TestUseCommandOutsideASessionReachesTheMachineSource:Unit test envoy Go package TestTheChainSkipsAbsenceAndStopsOnABrokenSource:Unit test envoy Go package TestSettingsNameTheGoogleExchangeAudienceOnEitherStore:Count the database broker suites' skips")"

# The expansion stops at exit 2 on the first step it cannot read, and it runs by hand, so nothing
# told the pull request that made a step unreadable: #4319 moved the skips step behind
# database-skips.sh, #2856 edited that script past its pin, and each time the next by-hand run
# ended with no verdict. This case reads every go test step of this head's ci.yaml and the
# workflows its jobs call, as both readers do, and fails naming each step the reader refuses and
# why. It runs no command: the reader asks its answer for what a step's `go list … | xargs`
# lister and its go-tagged-tests.sh call print, and this case answers both with a stand-in word,
# since what those print is the go command's answer and not the step's text. Two fixtures, each a
# step the reader must refuse, show the case can fail. The last case holds each workflow the
# reader reads to this gate's declared reads, so the pull request that edits one runs this case.
echo "case: the step reader refuses no go test step of this head"
# reader_refusals <root>: each refusal of the reader over the workflows under root, one per line.
reader_refusals() { py 'import pathlib
steps.ROOT = pathlib.Path(sys.argv[1])
try:
    found = steps.steps_of(steps.documents())
except steps.Refused as refusal:
    print(refusal)
    raise SystemExit
if not found:
    print("no go test step")
for step in found:
    try:
        steps.commands_of(step, lambda step, text: "(not-run)")
    except steps.Refused as refusal:
        print(refusal)' "$1"; }
refusals=$(reader_refusals "$repo")
check "every go test step is read${refusals:+; refused: $refusals}" "$(is "$refusals" "")"
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/.github/workflows" "$fixture/packages/envoy/scripts"
printf 'jobs:\n  envoy-go:\n    uses: ./.github/workflows/ci-job-envoy-go.yaml\n' >"$fixture/.github/workflows/ci.yaml"
cat >"$fixture/.github/workflows/ci-job-envoy-go.yaml" <<'EOF'
jobs:
  envoy-go:
    steps:
      - name: Count the database broker suites' skips
        working-directory: packages/envoy
        run: scripts/database-skips.sh --timeout 15m --skip '^(TestSettingsNameTheGoogleExchangeAudienceOnEitherStore)$' --run "$(../../.github/scripts/go-test-split.sh "$LEG" 3 ./internal/cipher/... ./cmd/cipherd/)" --jsonfile "go-envoy-held-envoy-go-$LEG-database-skips.json"
EOF
skips=packages/envoy/scripts/database-skips.sh
{ cat "$repo/$skips"; echo '# an edit after the reading its pin is of'; } >"$fixture/$skips"
check "a step whose script has moved past its pin is refused, by step and reason" \
  "$(contains "$(reader_refusals "$fixture")" "^\.github/workflows/ci-job-envoy-go\.yaml's envoy-go / Count the database broker suites' skips: $skips has SHA-256 ")"
cat >"$fixture/.github/workflows/ci-job-envoy-go.yaml" <<'EOF'
jobs:
  envoy-go:
    steps:
      - name: Time-box the tests
        run: timeout 60 go test ./internal/bus/
        working-directory: packages/envoy
EOF
check "a line the reader cannot read is refused, by step and line" \
  "$(is "$(reader_refusals "$fixture")" ".github/workflows/ci-job-envoy-go.yaml's envoy-go / Time-box the tests: this reader cannot read \`timeout 60 go test ./internal/bus/\`")"

# The forms a go test takes once ci.yaml's jobs are matrix jobs whose steps live in local
# composite actions and run their Go tests through gotestsum: a matrix value a step reads, a step
# of a composite action, a script a step runs, a gotestsum command, a package go-test-split.sh
# splits between the legs, and an input the job that calls the workflow gives it; and the
# variables a command's line, or its `-exec 'env …'`, hands the test binary. One fixture tree holds
# a step of each form, and each check reads the go test commands the reader makes of that step:
# its directory, packages, -tags, -race, -run, -skip, whether it is a split and its environment.
echo "case: the reader reads a go test in each form a step of the new layout writes it"
forms="$fixture/forms"
mkdir -p "$forms/.github/workflows" "$forms/.github/actions/unit-tests" "$forms/scripts" "$forms/packages/envoy"
cat >"$forms/.github/workflows/ci.yaml" <<'EOF'
jobs:
  forms:
    uses: ./.github/workflows/ci-job-forms.yaml
    with:
      packages: ./internal/input/
EOF
cat >"$forms/.github/workflows/ci-job-forms.yaml" <<'EOF'
on:
  workflow_call:
    inputs:
      packages:
        type: string
        required: true
jobs:
  forms:
    runs-on: ubuntu-24.04
    strategy:
      fail-fast: false
      matrix:
        leg: [1, 2]
        include:
          - leg: 1
            packages: ./internal/a/
          - leg: 2
            packages: ./internal/b/
    steps:
      - name: A go test in a matrix value
        run: go test -count=1 ${{ matrix.packages }}
        working-directory: packages/envoy
      - name: A go test in a composite action
        uses: ./.github/actions/unit-tests
        with:
          packages: ./internal/action/
      - name: A go test in a script
        run: scripts/unit.sh
      - name: A gotestsum command
        run: gotestsum --format standard-verbose --jsonfile forms.json --packages "./internal/c/ ./internal/d/" -- -count=1 -race -tags e2e -run '^(TestOne)$' -skip '^(TestTwo)$'
        working-directory: packages/envoy
      - name: A go-test.sh command
        run: RACE_MEM_LIMIT_KB=8388608 ../../.github/scripts/go-test.sh --race-memory --rerun-fails-max-failures 3 --jsonfile forms-e.json --packages "./internal/e/ ./internal/f/" -- -count=1 -race -run '^(TestThree)$'
        working-directory: packages/envoy
      - name: A package split between the legs
        run: |
          pattern=$(../../.github/scripts/go-test-split.sh ${{ matrix.leg }} 2 ./internal/split/)
          gotestsum --format standard-verbose --jsonfile split-${{ matrix.leg }}.json --packages ./internal/split/ -- -count=1 -run "$pattern"
        working-directory: packages/envoy
      - name: A split of the tests a pattern selects
        run: |
          pattern=$(../../.github/scripts/go-test-split.sh ${{ matrix.leg }} 2 -tags=memory "-run=^(TestOne|TestTwo)$" ./internal/split/)
          gotestsum --format standard-verbose --jsonfile narrowed-${{ matrix.leg }}.json --packages ./internal/split/ -- -count=1 -tags memory -run "$pattern"
        working-directory: packages/envoy
      - name: A variable set before the command
        run: GODEBUG=checkfinalizers=1 go test -count=1 ./internal/docs/
        working-directory: packages/envoy
      - name: A variable the test binary gets through -exec
        run: go test -count=1 -exec 'env GODEBUG=checkfinalizers=1' ./internal/docs/
        working-directory: packages/envoy
      - name: The caller's input
        run: go test -count=1 ${{ inputs.packages }}
        working-directory: packages/envoy
EOF
cat >"$forms/.github/actions/unit-tests/action.yml" <<'EOF'
name: Unit tests
description: Runs the packages its user names.
inputs:
  packages:
    description: The packages.
    required: true
runs:
  using: composite
  steps:
    - name: The packages' tests
      shell: bash
      run: go test -count=1 ${{ inputs.packages }}
      working-directory: packages/envoy
EOF
cat >"$forms/scripts/unit.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../packages/envoy"
go test -count=1 ./internal/script/
EOF
# read_forms <root>: one line per go test command of each step the reader reads under root: the
# step's file and name, its directory, the packages, and what the command reads them under.
read_forms() { py 'import pathlib
steps.ROOT = pathlib.Path(sys.argv[1])
for step in steps.steps_of(steps.documents()):
    for command in steps.commands_of(step, lambda step, text: "(not-run)"):
        env = " ".join(f"{name}={value}" for name, value in sorted(command.env.items()))
        print(f"{step.workflow} / {step.name} | {step.workdir} | {chr(32).join(command.packages)} | tags={command.tags} "
              f"race={command.race} run={command.run} skip={command.skip} split={command.split} env={env}")' "$1"; }
# has <text> <string>: true/false for check, a fixed-string match: these lines hold ^, $ and |.
has() { grep -Fq -- "$2" <<<"$1" && echo true || echo false; }
read=$(read_forms "$forms")
job=.github/workflows/ci-job-forms.yaml
check "a matrix value is read into the step that reads it, once for each leg${read:+ (read: $read)}" \
  "$(has "$read" "$job / A go test in a matrix value | packages/envoy | ./internal/a/ |")"
check "and the other leg's value too" \
  "$(has "$read" "$job / A go test in a matrix value | packages/envoy | ./internal/b/ |")"
check "a step that uses a composite action is read through the action's step, under the input it gives, and keyed as the step" \
  "$(has "$read" "$job / A go test in a composite action | packages/envoy | ./internal/action/ |")"
check "a script a step runs is read through its lines, in the directory it moves to" \
  "$(has "$read" "$job / A go test in a script | packages/envoy | ./internal/script/ |")"
check "a gotestsum command is read as the go test it wraps: its --packages, and the flags after its --" \
  "$(has "$read" "$job / A gotestsum command | packages/envoy | ./internal/c/ ./internal/d/ | tags=e2e race=True run=^(TestOne)\$ skip=^(TestTwo)\$ split=False env=")"
check "a go-test.sh command is read as the gotestsum command it runs, the variable before it in its environment" \
  "$(has "$read" "$job / A go-test.sh command | packages/envoy | ./internal/e/ ./internal/f/ | tags= race=True run=^(TestThree)\$ skip= split=False env=RACE_MEM_LIMIT_KB=8388608")"
check "a package go-test-split.sh splits between the legs is read as run whole, and marked a split" \
  "$(has "$read" "$job / A package split between the legs | packages/envoy | ./internal/split/ | tags= race=False run= skip= split=True env=")"
check "a split given -run=<pattern> is read as running what the pattern selects, and marked a split" \
  "$(has "$read" "$job / A split of the tests a pattern selects | packages/envoy | ./internal/split/ | tags=memory race=False run=^(TestOne|TestTwo)\$ skip= split=True env=")"
check "a variable set before the command is part of the command's environment" \
  "$(has "$read" "$job / A variable set before the command | packages/envoy | ./internal/docs/ | tags= race=False run= skip= split=False env=GODEBUG=checkfinalizers=1")"
check "a variable an -exec 'env …' hands the test binary is part of the command's environment" \
  "$(has "$read" "$job / A variable the test binary gets through -exec | packages/envoy | ./internal/docs/ | tags= race=False run= skip= split=False env=GODEBUG=checkfinalizers=1")"
check "an input the job that calls the workflow gives is read into the step" \
  "$(has "$read" "$job / The caller's input | packages/envoy | ./internal/input/ |")"

# AUDITED_SCRIPTS holds a person's reading of each script, pinned by its SHA-256, and the script
# refuses a step whose script's text has moved on. The script runs by hand, so nothing else tells
# the pull request that edits an audited script: #2856 changed database-skips.sh's
# EXPECTED_SKIPS, the pin went stale, and the next by-hand run ended at exit 2 with no verdict.
# ci.yaml's go_test_step_expansion_test filter names each audited script, so this case runs on
# that pull request. It also holds each audited script, and each workflow the step reader reads,
# to the reads ci_job_reads.py declares for this harness's gate, which ci-filters holds to that
# filter, so a new AUDITED_SCRIPTS entry or called workflow fails here until its file selects this
# harness. Both lists are read into variables first: a failed read then stops the harness under
# `set -e`, where a process substitution's failure would leave the loop with nothing to check.
echo "case: each script AUDITED_SCRIPTS reads still has the text its entry was read from"
pins=$(py 'for path, audited in steps.AUDITED_SCRIPTS.items(): print(path + "\t" + audited.sha256)')
check "AUDITED_SCRIPTS names at least one script" "$(is "$([ -n "$pins" ] && echo yes || echo no)" yes)"
declared=$(cd "$repo" && py 'import ci_job_reads
print("\n".join(read.glob for gate in ci_job_reads.SCRIPT_TEST_GATES
                if gate.filter == "go_test_step_expansion_test" for read in gate.reads))')
while IFS=$'\t' read -r path pinned; do
  actual=$(sha256sum "$repo/$path" | cut -d' ' -f1)
  check "$path has its entry's SHA-256 (read the script again, then update its AuditedScript entry in go_test_steps.py)" \
    "$(is "$actual" "$pinned")"
  check "$path is among the reads ci_job_reads.py declares for the go_test_step_expansion_test gate (add it there and to that ci.yaml filter)" \
    "$(is "$(grep -Fxq -- "$path" <<<"$declared" && echo yes || echo no)" yes)"
done <<<"$pins"
unread=$(py 'import workflow_paths
globs = sys.argv[1].splitlines()
print(" ".join(path for path in steps.documents()
               if not any(workflow_paths.glob_regex(glob).fullmatch(path) for glob in globs)))' "$declared")
check "each workflow the step reader reads is among those reads${unread:+ (add it there and to that ci.yaml filter): $unread}" \
  "$(is "$unread" "")"

summary "go_test_steps.py reads a go command behind the variables a step writes"
