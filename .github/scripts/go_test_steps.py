"""The `go test` commands of ci.yaml's router, read off the workflow text: the module both
go-test-step-expansion.py and check-test-reach.sh import.

It loads `.github/workflows/ci.yaml` and every workflow a job of it calls (`documents`), picks
each step whose `run:` holds a `go test` (or a gotestsum command, or a `.github/scripts/go-test.sh`
command) in one of the Go modules (`steps_of`, one step at a time `step_of`), and reads each such
step line by line into its `go test` commands (`commands_of`, `read_block`): packages, `-tags`,
`-race`, `-run` and `-skip`, through the variables a line sets before its command, `for` loops
over packages, and the scripts AUDITED_SCRIPTS reads.

A step is read in each form the workflows write it in. A matrix job's step is read once for each
leg of its matrix (`matrix_legs`), with each `${{ matrix.<name> }}` it writes replaced by the
leg's value, and a called workflow's step once for each job that calls it, with each
`${{ inputs.<name> }}` replaced by what that job's `with:` gives (`call_inputs`, `substitute`). A
step that `uses:` a local composite action is read through the action's own `run:` steps, under
the inputs its `with:` gives them (`local_action_steps`); such a step keeps the name of the step
that uses the action, so a reader keys it as it keys any step of the file. A step whose run: is
one command that runs a shell script of the repository is read through the script's own lines
(`script_step`), where the reader can read every line of it. A gotestsum command is read as the
`go test` it wraps: its `--packages` list and the `go test` flags after its `--`
(`parse_gotestsum`), and so is a `.github/scripts/go-test.sh` command, which takes gotestsum's
`--jsonfile`, `--packages` and `--` and runs gotestsum with them. A `-run` that
`.github/scripts/go-test-split.sh` prints is the leg's share of
its packages, which the legs of the job run between them, so the command is read as running its
packages whole, or the tests the `-run=<pattern>` the call passes the script selects, and is
marked `split`. The variables a line sets before its command, and those a
`-exec 'env NAME=value'` hands the test binary, are the command's own environment (`GoTest.env`).

Two shapes of a line need an answer from outside the line: a `-run "$pattern"` assigned from a
`$(…)` call of `.github/scripts/go-tagged-tests.sh`, and the package list `go list … | grep -Ev
'…'` gives a command, piped into `xargs go test …` or assigned to a variable first. `read_block`
asks its `answer` for both, with the step and the text of the call or the lister.
go-test-step-expansion.py answers by running the text in the step's directory (`shell_answer`);
check-test-reach.sh, which runs no `go` command, answers by reading the source the way those two
commands read it. A line that is neither, and holds no command this module reads, is refused
rather than skipped: a `go test` silently passed over would leave both readers wrong about what a
step runs.

It needs PyYAML, and the helpers of ci_layout.py beside it (`called_workflows`, `script_lines`,
`step_workdir`), so the readers and check-ci-layout.sh agree on which files a job's steps live in,
which lines a script holds and which directory a step runs in.
"""

from __future__ import annotations

import hashlib
import os
import re
import shlex
import subprocess
from collections.abc import Callable
from dataclasses import dataclass, field
from pathlib import Path

import yaml
from ci_layout import called_workflows, script_lines, step_workdir

# The repository holding this file: its own path, not resolved through a symlink, so a fixture
# tree that links this file in is the repository it reads.
ROOT = Path(__file__).absolute().parents[2]
# Repository-relative, as the documents below are keyed and as every message names them.
WORKFLOW = ".github/workflows/ci.yaml"
# The three Go modules whose steps are read. packages/daemon joined them with the first pair of
# steps in this repository that share a package through a `-run` and a `-skip` naming the same
# tests (plugin-pack's step and go-daemon's race step): the pairing is what makes
# each of them run exactly once, and a reader that cannot see both steps cannot see it.
MODULE_DIRS = ("packages/envoy", "packages/envoy/infra", "packages/daemon")

# A test function's name, as `go test -list` prints it and as a `-skip` alternation names it.
NAME = re.compile(r"^(Test|Fuzz|Example|Benchmark)[A-Za-z0-9_]*$")
# A shell variable, as the steps write it: $pkg or ${pkg}.
SHELL_VAR = re.compile(r"\$(?:([A-Za-z_][A-Za-z0-9_]*)|\{([A-Za-z_][A-Za-z0-9_]*)\})")
FOR_LINE = re.compile(r"^for\s+([A-Za-z_][A-Za-z0-9_]*)\s+in\s+(.+?)\s*;\s*do$")
ASSIGNMENT = re.compile(r'^([A-Za-z_][A-Za-z0-9_]*)="?\$\((.+)\)"?$')
XARGS = re.compile(r"^(?P<lister>.+?)\s*\|\s*xargs\s+(?P<test>go\s+test\b.*)$")
TAGGED_TESTS = ".github/scripts/go-tagged-tests.sh"
# The script that splits one package's tests between the legs of a job at run time: given
# `<leg> <legs> <packages…>`, it prints the `-run` alternation of the tests that land in that leg
# by their names' hash, so the legs between them run each test of the packages once. A reader of
# what the job runs reads the command whose `-run` it prints as running its packages whole, or,
# where the call passes the script `-run=<pattern>`, as running what that pattern selects.
SPLIT = ".github/scripts/go-test-split.sh"
# What read_block assigns the variable a `$(…)` call of SPLIT sets, followed by the call's own
# `-run=` pattern, so the `-run` that expands it is known as the split's: no pattern a step writes
# starts with this text.
SPLIT_RUN = "(the -run go-test-split.sh prints)"
# The `-run=<pattern>` a call of SPLIT narrows its split to, one word before its packages.
SPLIT_PATTERN = re.compile(r"""(?:^|\s)["']?-run=([^"'\s]+)""")
# A run: that starts a go test, a gotestsum or a go-test.sh, by its comment-free lines.
GO_TEST_TEXT = re.compile(r"\b(?:go\s+test|gotestsum|go-test\.sh)\b")
# The commands read as gotestsum (parse_gotestsum), by the last part of the word that names them:
# gotestsum itself, and .github/scripts/go-test.sh, through which every Go step of a job ci-ok
# needs runs it, whose own flags before `--` (--jsonfile, --packages, --rerun-fails-max-failures,
# --race-memory) are gotestsum's or a switch.
GOTESTSUM_COMMANDS = ("gotestsum", "go-test.sh")
# gotestsum's own flags that take a value, as `--flag value` or `--flag=value`; every other flag
# before its `--` is a switch. `--raw-command` makes the words after `--` a whole command and
# `--watch` runs no fixed set, so a command with either is refused rather than read.
GOTESTSUM_VALUE_FLAGS = ("--format", "-f", "--jsonfile", "--junitfile", "--packages",
                         "--rerun-fails-report", "--rerun-fails-max-failures", "--format-icons",
                         "--junitfile-testsuite-name", "--junitfile-testcase-classname",
                         "--junitfile-project-name", "--post-run-command", "--jsonfile-timing-events",
                         "--hide-summary")
GOTESTSUM_REFUSED = ("--raw-command", "--watch")
# The expressions a step's text is read through before its lines are: a matrix value, and an input
# the job that calls the workflow, or the step that uses the composite action, gives. Any other
# expression is left as written.
CONTEXT_EXPRESSION = re.compile(r"\$\{\{\s*(matrix|inputs)\.([A-Za-z0-9_-]+)\s*\}\}")
# A composite action's step that names the checkout's root as its directory.
WORKSPACE_DIRECTORY = re.compile(r"^\$\{\{\s*github\.workspace\s*\}\}/?")
# The one move a script may make before its commands that script_step reads: into a directory
# named from the script's own, `cd "$(dirname "$0")/<path>"`.
SCRIPT_CD = re.compile(r'^cd\s+"\$\(dirname\s+"(?:\$0|\$\{BASH_SOURCE\[0\]\})"\)(?:/(?P<path>[^"]*))?"$')
# A positional parameter, which a script read without the arguments a step passes cannot expand.
POSITIONAL = re.compile(r"\$(?:[1-9@*#]|\{[1-9@*#])")
# The harnesses of the repository's own scripts, which run `go test` over fixture modules they
# write, not over the tree.
HARNESS_NAME = re.compile(r"[._]test\.(?:sh|bash)$")
# What script_step reads `$GITHUB_WORKSPACE/` as before it splits a run: into words, the checkout's
# root a step names a script by from another directory: a text no path holds.
WORKSPACE_MARK = "\0workspace\0/"


@dataclass(frozen=True)
class AuditedScript:
    """A script a step runs, read by a person: the module it moves into before its tests, the one
    `go test` it runs there for each argument list a step passes it (the run:'s text after the
    script's path, "" for none), and the SHA-256 of the text that reading was of."""

    module: str
    commands: dict[str, str]
    sha256: str


# The tests envoy-go's "Count the store-form rows on each backend" names, in the order its run:
# passes them to backend-count.sh; the unit and database-skips steps' -skip lists name the same
# tests by package.
BACKEND_COUNT_TESTS = (
    "TestEverySecretFormNamesTheSourceItUses",
    "TestNoSecretFormAsksTheBrokerForACredential",
    "TestUseCommandOutsideASessionReachesTheMachineSource",
    "TestTheChainSkipsAbsenceAndStopsOnABrokenSource",
    "TestSettingsNameTheGoogleExchangeAudienceOnEitherStore",
)

# Each script a step of ci.yaml's router runs as its whole run: line, read by a person, with the
# one `go test` it runs for each argument list a step passes it. A step that runs any other script
# is read through the script's own lines where the reader can read each of them (script_step); a
# script it cannot read whole - one that lists its packages into an array, moves directory inside
# a function, or reads its arguments - is passed over, as every script a step ran was before
# script_step, which is how envoy-go's "Count the store-form rows on each backend"
# (backend-count.sh) was read as running nothing until its entry below, and its five tests'
# second run was seen by no reader. An entry here is how such a script is read.
AUDITED_SCRIPTS = {
    # It moves into packages/envoy (`cd "$(dirname "$0")/.."`) and runs one
    # .github/scripts/go-test.sh over its packages, which runs gotestsum, whose first run is one
    # `go test -json -count=1` of them (a rerun in a merge group runs a failed test again and no
    # other), then counts the skips in the jsonfile. With no package argument it
    # runs its DEFAULT_PACKAGES, ./internal/cipher/... and ./cmd/cipherd/, less the import paths its
    # DEFAULT_EXCEPT's `grep -Ev '/internal/cipher/natscallout$'` takes out of `go list` of them (the
    # race lane's packages under the tree), with no -tags,
    # -race, -run or -skip, under -timeout "${GO_TEST_TIMEOUT:-15m}". A package argument replaces
    # those packages, `--race` adds -race, `--skip P` adds -skip P, `--run P` adds -run P,
    # `--timeout D` sets the -timeout, and `--var NAME` and `--jsonfile NAME` decide only whether it
    # runs at all and where its jsonfile goes. With `--report FILE` it reads a report and runs no
    # test. ci.yaml's go-envoy-held job runs it in two kinds of leg, each passing its own share of
    # the packages' tests as --run, the `$(…)` call of .github/scripts/go-test-split.sh that prints
    # it, with the leg's number in $LEG: the envoy-go legs' "Count the database broker suites'
    # skips" step passes the first argument list below, whose command is that run in the listing
    # form this module reads, and the envoy-go-race-api legs' race step the second. Each command's
    # -run is the split's, so the legs between them run the packages whole. The first's --skip
    # leaves out the backend count's test of ./internal/cipher/api/.
    "packages/envoy/scripts/database-skips.sh": AuditedScript(
        module="packages/envoy",
        commands={
            "--timeout 15m --skip '^(TestSettingsNameTheGoogleExchangeAudienceOnEitherStore)$' "
            "--run \"$(../../.github/scripts/go-test-split.sh \"$LEG\" 3 ./internal/cipher/... ./cmd/cipherd/)\" "
            "--jsonfile \"go-envoy-held-envoy-go-$LEG-database-skips.json\"":
                "go list ./internal/cipher/... ./cmd/cipherd/ | grep -Ev '/internal/cipher/natscallout$' | "
                "xargs go test -json -count=1 -skip '^(TestSettingsNameTheGoogleExchangeAudienceOnEitherStore)$' "
                "-run \"$(../../.github/scripts/go-test-split.sh)\" -timeout 15m",
            "--var DISPATCH_TEST_DATABASE_URL --var BROKER_TEST_DATABASE_URL --race --timeout 26m "
            "--run \"$(../../.github/scripts/go-test-split.sh \"$LEG\" \"$LEGS\" -race ./internal/dispatch/api/)\" "
            "--jsonfile \"go-envoy-held-envoy-go-race-api-$LEG-race.json\" ./internal/dispatch/api/":
                "go test -json -count=1 -race -run \"$(../../.github/scripts/go-test-split.sh)\" -timeout 26m "
                "./internal/dispatch/api/",
        },
        sha256="8de239fb2624ed2464f8be90c2bafbd8e0e14fc973eb3abcbc4b4fad76acc496",
    ),
    # It moves into packages/envoy (`cd "$(dirname "$0")/.."`) and runs one
    # .github/scripts/go-test.sh over the packages before its `--`, which runs gotestsum, whose
    # first run is one `go test -json -count=1 -timeout "${GO_TEST_TIMEOUT:-5m}" -run '^(TEST|…)$'`
    # of them, the -run built from the test names after it, then counts each test's `aws` and `gcp`
    # passes in the jsonfile. GO_TEST_SKIP adds a -skip, which no step sets, and envoy-go's job sets
    # neither variable. envoy-go's count step passes the argument list below.
    "packages/envoy/scripts/backend-count.sh": AuditedScript(
        module="packages/envoy",
        commands={
            "./cmd/cipher/ ./internal/cipher/api/ -- " + " ".join(BACKEND_COUNT_TESTS):
                f"go test -json -count=1 -timeout 5m -run '^({'|'.join(BACKEND_COUNT_TESTS)})$' "
                "./cmd/cipher/ ./internal/cipher/api/",
        },
        sha256="ebe385e698039bb01af54bec30ab758019a8e86492711ed5e42adae7b2bc5af7",
    ),
    # It moves into packages/envoy/infra (`cd "$(dirname "$0")/.."`) and, given `run`, runs one
    # .github/scripts/go-test.sh over the import paths `go list ./...` lists less those its
    # EXCLUDE, `/internal/textcheck$`, matches (the text checks, whose tests the text-check job
    # runs), which runs gotestsum, whose first run is one `go test -json -count=1 -race` of them.
    # Given `compile`, it builds those race test binaries with `-c` and runs none, which
    # ci-caches.yaml's envoy-infra fill alone does, outside ci.yaml. envoy-infra-go's unit step
    # passes `run`.
    "packages/envoy/infra/scripts/race-unit.sh": AuditedScript(
        module="packages/envoy/infra",
        commands={
            "run": "go list ./... | grep -Ev '/internal/textcheck$' | xargs go test -json -count=1 -race",
        },
        sha256="dfdc0e381fdc4b737010127fa1d115178b12e88bd5eb49e25ec0c7afdff21dc5",
    ),
}
# A variable a step sets before its command, as the steps write it: GODEBUG=checkfinalizers=1.
# A value holding `$` is left for the caller to refuse, since what it expands to is the shell's.
ENV_PREFIX = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*=[^$]*$")
# go commands that run no test, so a step may hold one and the reader need not read it.
NO_TEST_GO = ("build", "vet", "list", "install", "mod", "work", "env", "generate", "tool", "run")
# The commands a line that names no `go` may still start one through, or move the directory a
# later `go test` reads its packages from with: such a line is refused like any other this module
# cannot read. Any other line that holds no `go` word - a `docker exec … createdb` that makes the
# tests' database, an `echo` - starts no go command the reader could see, and is passed over.
NOT_PASSED_OVER = ("cd", "pushd", "popd", "source", ".", "eval", "exec", "bash", "sh", "env", "make",
                   "timeout", "xargs", "nohup", "sudo")
GO_WORD = re.compile(r"(?<![\w./-])go(?![\w.-])")
# go test flags whose value is the next argument.
VALUE_FLAGS = ("-run", "-skip", "-tags", "-timeout", "-count", "-parallel", "-cpu", "-bench",
               "-o", "-fuzz", "-fuzztime", "-shuffle", "-exec", "-coverprofile", "-covermode",
               "-p")
# The one `-skip` shape this module reads: the anchored alternation of whole test names
# every step of this repository that carries a -skip writes, `^(Name|Name|…)$`.
# Its names are read off the pattern's own text, and every other pattern is refused by name,
# because `go test` runs the real `-skip` through Go's regexp: a Python `re` matched against a
# listing would be a second engine, and two engines that agree on every pattern anyone wrote
# down disagree on the one nobody tried, leaving a reader wrong about what a step runs.
SKIP_ALTERNATION = re.compile(r"^\^\(([^()|]+(?:\|[^()|]+)*)\)\$$")


class Refused(Exception):
    """A shape this module will not guess at."""


@dataclass
class Step:
    workflow: str  # repository-relative: the file that holds this step
    job: str
    name: str
    workdir: str  # repository-relative, as the file writes it
    script: str
    # The variables the workflow's, the job's and the step's `env:` set (an action's step's own
    # too), by name, each value as written there with the step's matrix values and inputs read in.
    env: dict[str, str] = field(default_factory=dict)

    @property
    def label(self) -> str:
        return f"{self.workflow}'s {self.job} / {self.name}"

    # The file and the step's name, which is what an exception of go-test-step-expansion.py
    # declares: a step name is unique within the file that holds it, while a job id is ci.yaml's
    # and the name of the step may be another file's too. The legs of a matrix job and the steps
    # of the composite action a step uses share their step's key.
    @property
    def key(self) -> tuple[str, str]:
        return (self.workflow, self.name)

    @property
    def directory(self) -> Path:
        return ROOT / self.workdir


@dataclass
class GoTest:
    """One `go test` command of one step."""

    step: Step
    text: str
    packages: list[str]
    tags: str = ""
    race: bool = False
    run: str = ""
    skip: str = ""  # the pattern as the step writes it; skip_names reads the names out of it
    resolved: list[str] = field(default_factory=list)  # import paths
    # The variables the command's own line sets before it, and those a `-exec 'env NAME=value'`
    # hands the test binary: GODEBUG=checkfinalizers=1 either way.
    env: dict[str, str] = field(default_factory=dict)
    # Its `-run` is the share of its packages go-test-split.sh gives one leg of its job, so the
    # legs between them run the packages whole, and `run` is "", or the `-run=<pattern>` the call
    # passes the script, whose tests the legs run between them.
    split: bool = False


# How a reader answers the two lines read_block cannot read off their own text: given the step
# and the text of the `$(…)` call or the `go list …` lister, what it prints.
Answer = Callable[[Step, str], str]


def indent(text: str) -> str:
    return "".join(f"    | {line}\n" for line in (text or "").strip().splitlines())


def shell_answer(step: Step, script: str) -> str:
    """A line of a step's own script, run in the step's own directory."""
    done = subprocess.run(["bash", "-c", f"set -euo pipefail\n{script}"],
                          cwd=step.directory, capture_output=True, text=True)
    if done.returncode != 0:
        raise Refused(f"{step.label}: {script} exited {done.returncode}\n"
                      f"    $ {script}\n{indent(done.stderr or done.stdout)}")
    return done.stdout


def expand(text: str, variables: dict[str, str]) -> str:
    def one(match: re.Match[str]) -> str:
        name = match.group(1) or match.group(2)
        if name not in variables:
            raise Refused(f"the shell variable ${name} is set outside the step, or not at all")
        return variables[name]

    return SHELL_VAR.sub(one, text)


def documents() -> dict:
    """ci.yaml and every workflow a job of it calls, keyed by repository-relative path: a job of
    ci.yaml holds its steps itself, through the composite actions they use, or in the workflow its
    `uses:` names (dispatch-browser.yaml, docs-site.yaml, the image builds). The set is the one
    check-ci-layout.sh reads, derived with that check's own `called_workflows`; it is loaded here
    rather than through `ci_layout`'s `workflow_documents`, which roots itself at the current
    working directory and appends to the checker's problem list, because this module roots itself
    at its own path and refuses instead."""
    found = {WORKFLOW: yaml.safe_load((ROOT / WORKFLOW).read_text()) or {}}
    for path, job_id in called_workflows(found[WORKFLOW]).items():
        if not (ROOT / path).is_file():
            raise Refused(f"job {job_id} of {WORKFLOW} calls {path}, which is not in the "
                          f"repository, so the steps it runs are read by nothing here")
        found[path] = yaml.safe_load((ROOT / path).read_text()) or {}
    return found


def steps_of(found: dict) -> list[Step]:
    """Every step of those documents whose run: holds a `go test` or a gotestsum command and whose
    directory is one of the three modules, and every step that runs an AUDITED_SCRIPTS script or
    a script script_step reads: once for each leg of its job's matrix and each job that calls its
    workflow, through the local composite action it uses. The directory and the comment-free
    lines are read with check-ci-layout.sh's own helpers (ci_layout.step_workdir, script_lines),
    so the readers and the layout check agree on what a step runs and where. Two legs, two
    callers or two action steps that read the same are one step."""
    steps: list[Step] = []
    inputs = call_inputs(found)
    for path, document in found.items():
        for job, body in (document.get("jobs") or {}).items():
            body = body or {}
            if body.get("uses"):
                continue  # a job that calls a workflow holds no step; its workflow's are read
            for given in inputs.get(path, [{}]):
                for leg in matrix_legs(body):
                    contexts = {"matrix": leg, "inputs": given}
                    for index, raw in enumerate(body.get("steps", []) or []):
                        name = raw.get("name") or f"step {index + 1}"
                        for script, workdir, env in run_texts(document, body, raw, contexts):
                            step = step_of(path, job, name, workdir, script)
                            if step is None:
                                continue
                            step.env = env
                            if step not in steps:
                                steps.append(step)
    return steps


def step_of(workflow: str, job: str, name: str, workdir: str, script: str,
            modules: tuple[str, ...] = MODULE_DIRS) -> Step | None:
    """The step one run: makes, as steps_of reads it, or None where it starts no `go test` this
    module reads: an AUDITED_SCRIPTS script's entry; the lines of a script script_step reads; or
    the run: itself, where it holds a `go test` or a gotestsum command and runs in one of
    `modules` (MODULE_DIRS, or check-test-reach.sh's every module of the tree)."""
    audited = audited_step(workflow, job, name, workdir, script)
    if audited is not None:
        return audited
    followed = script_step(Step(workflow=workflow, job=job, name=name, workdir=workdir, script=script), modules)
    if followed is not None:
        return followed
    if workdir not in modules or not GO_TEST_TEXT.search("\n".join(script_lines(script))):
        return None
    return Step(workflow=workflow, job=job, name=name, workdir=workdir, script=script)


def context_text(value) -> str:
    """A matrix value, an input or an env value as the expression that reads it writes it into
    the text: a YAML boolean as `true` or `false`, nothing as the empty string."""
    if isinstance(value, bool):
        return "true" if value else "false"
    return "" if value is None else str(value)


def substitute(text: str, contexts: dict[str, dict[str, str]]) -> str:
    """The text with each `${{ matrix.<name> }}` and `${{ inputs.<name> }}` the contexts hold
    replaced by its value; any other expression, and one naming a value they lack, as written."""
    def one(match: re.Match[str]) -> str:
        values = contexts.get(match.group(1)) or {}
        return values.get(match.group(2), match.group(0))

    return CONTEXT_EXPRESSION.sub(one, text)


def matrix_legs(job: dict) -> list[dict[str, str]]:
    """The legs a job's `strategy.matrix` makes, each as the values its `${{ matrix.<name> }}`
    reads, expanded as GitHub expands a matrix: every combination of its lists, less each
    `exclude` entry matches, then each `include` entry added to every combination whose original
    values it does not change, or made a leg of its own where it changes those of each. A job with
    no matrix, or with one written as an expression, is one leg with no value, whose
    `${{ matrix.<name> }}` is then left as written."""
    matrix = ((job or {}).get("strategy") or {}).get("matrix")
    if not isinstance(matrix, dict):
        return [{}]
    keys = [key for key in matrix if key not in ("include", "exclude")]
    legs: list[dict] = [{}] if keys else []
    for key in keys:
        values = matrix[key]
        if not isinstance(values, list):
            return [{}]
        legs = [{**leg, key: value} for leg in legs for value in values]
    for excluded in matrix.get("exclude") or []:
        if isinstance(excluded, dict):
            legs = [leg for leg in legs if any(leg.get(k) != v for k, v in excluded.items())]
    originals = len(legs)
    for included in matrix.get("include") or []:
        if not isinstance(included, dict):
            continue
        added = False
        for leg in legs[:originals]:
            if all(leg.get(k) == v for k, v in included.items() if k in keys):
                leg.update({k: v for k, v in included.items() if k not in keys})
                added = True
        if not added:
            legs.append(dict(included))
    return [{str(k): context_text(v) for k, v in leg.items()} for leg in legs] or [{}]


def input_defaults(document: dict) -> dict[str, str]:
    """The default of each input a reusable workflow's `on.workflow_call` declares one for."""
    on = document.get("on", document.get(True)) or {}
    call = on.get("workflow_call") if isinstance(on, dict) else None
    declared = (call or {}).get("inputs") if isinstance(call, dict) else None
    return {str(name): context_text(spec.get("default")) for name, spec in (declared or {}).items()
            if isinstance(spec, dict) and "default" in spec}


def call_inputs(found: dict) -> dict[str, list[dict[str, str]]]:
    """The inputs each workflow of `found` is called with: for each job of a document of `found`
    that calls it with `uses:`, its `with:` over the defaults the workflow declares, as text. A
    workflow no document of `found` calls is read with no input."""
    calls: dict[str, list[dict[str, str]]] = {}
    for document in found.values():
        for body in (document.get("jobs") or {}).values():
            uses = str((body or {}).get("uses") or "")
            path = uses.removeprefix("./")
            if not uses.startswith("./") or path not in found:
                continue
            given = {str(k): context_text(v) for k, v in ((body or {}).get("with") or {}).items()}
            calls.setdefault(path, []).append({**input_defaults(found[path]), **given})
    return calls


def env_text(block, contexts: dict[str, dict[str, str]]) -> dict[str, str]:
    """An `env:` block's variables, each value as text with the contexts read in."""
    return {str(k): substitute(context_text(v), contexts) for k, v in (block or {}).items()} if isinstance(block, dict) else {}


def local_action_steps(uses: str, given: dict[str, str],
                       seen: frozenset = frozenset()) -> list[tuple[str, dict, dict[str, str]]]:
    """The run: steps of the local composite action a step's `uses: ./<directory>` names, with
    those of each local action it uses in turn, as (its action file, the step, the inputs that
    step reads: the `with:` its user gives over the defaults its action declares). An action that
    is no local directory with an action.yml or action.yaml has none here."""
    if not uses.startswith("./") or uses in seen:
        return []
    base = uses.removeprefix("./").rstrip("/")
    path = next((f"{base}/{name}" for name in ("action.yml", "action.yaml") if (ROOT / base / name).is_file()), None)
    if path is None:
        return []
    action = yaml.safe_load((ROOT / path).read_text()) or {}
    declared = action.get("inputs") or {}
    inputs = {**{str(k): context_text(v.get("default")) for k, v in declared.items()
                 if isinstance(v, dict) and "default" in v}, **given}
    found = []
    for step in ((action.get("runs") or {}).get("steps") or []):
        if step.get("run"):
            found.append((path, step, inputs))
        elif step.get("uses"):
            inner = {str(k): substitute(context_text(v), {"inputs": inputs}) for k, v in (step.get("with") or {}).items()}
            found += local_action_steps(str(step["uses"]), inner, seen | {uses})
    return found


def action_workdir(step: dict, inputs: dict[str, str]) -> str:
    """The directory a composite action's run: step runs in: its own working-directory, the
    checkout's root where that names `${{ github.workspace }}` or is not given."""
    given = substitute(context_text(step.get("working-directory")), {"inputs": inputs})
    return WORKSPACE_DIRECTORY.sub("", given).strip() or "."


def run_texts(document: dict, job: dict, step: dict,
              contexts: dict[str, dict[str, str]]) -> list[tuple[str, str, dict[str, str]]]:
    """What one step of a job runs, as (its run: text, the directory it runs in, its env: blocks'
    variables), with the leg's matrix values and the caller's inputs read in: the step's own run:,
    or the run: of each step of the local composite action it uses, under the inputs its `with:`
    gives."""
    env = {**env_text(document.get("env"), contexts), **env_text(job.get("env"), contexts),
           **env_text(step.get("env"), contexts)}
    if step.get("run"):
        return [(substitute(str(step["run"]), contexts), substitute(step_workdir(document, job, step), contexts), env)]
    given = {str(k): substitute(context_text(v), contexts) for k, v in (step.get("with") or {}).items()}
    found = []
    for _, inner, inputs in local_action_steps(str(step.get("uses") or ""), given):
        inner_contexts = {"inputs": inputs}
        found.append((substitute(str(inner["run"]), inner_contexts), action_workdir(inner, inputs),
                      {**env, **env_text(inner.get("env"), inner_contexts)}))
    return found


def script_step(step: Step, modules: tuple[str, ...] = MODULE_DIRS) -> Step | None:
    """The step a run: that is one command running a shell script of the repository makes: the
    script's own lines, read in the directory it runs them in - the step's, or the one its first
    command moves to from its own (`cd "$(dirname "$0")/<path>"`) - where that is one of
    `modules`. None where the run: is no such command, the script is one of the harnesses beside
    the scripts they test (`*.test.sh`), its lines hold no `go test` or gotestsum, it reads the
    arguments a step passes it, or the reader cannot read each of its lines: such a script is
    passed over, as every script was before this reading, and an AUDITED_SCRIPTS entry is how a
    person's reading of it joins the readers."""
    lines = script_lines(step.script)
    if len(lines) != 1:
        return None
    line = lines[0]
    for prefix in ("$GITHUB_WORKSPACE/", "${GITHUB_WORKSPACE}/"):
        line = line.replace(f'"{prefix}', f'"{WORKSPACE_MARK}').replace(prefix, WORKSPACE_MARK)
    if any(c in line for c in "|&;<>`$("):
        return None
    try:
        words = shlex.split(line)
    except ValueError:
        return None
    while words and ENV_PREFIX.match(words[0]):
        words = words[1:]
    if words[:1] in (["bash"], ["sh"]):
        words = words[1:]
    if not words or "/" not in words[0]:
        return None
    if words[0].startswith(WORKSPACE_MARK):
        relative = os.path.normpath(words[0][len(WORKSPACE_MARK):])
    else:
        relative = os.path.normpath(os.path.join(step.workdir, words[0]))
    if (relative.startswith("..") or os.path.isabs(relative) or not relative.endswith((".sh", ".bash"))
            or HARNESS_NAME.search(relative) or not (ROOT / relative).is_file()):
        return None
    text = (ROOT / relative).read_text(encoding="utf-8", errors="replace")
    body = [line for line in script_lines(text) if not line.startswith(("set ", "shopt "))]
    if not GO_TEST_TEXT.search("\n".join(body)) or (len(words) > 1 and POSITIONAL.search(text)):
        return None
    workdir = step.workdir
    moved = SCRIPT_CD.match(body[0]) if body else None
    if moved:
        workdir = os.path.normpath(os.path.join(os.path.dirname(relative), moved.group("path") or "."))
        body = body[1:]
    if workdir not in modules:
        return None
    followed = Step(workflow=step.workflow, job=step.job, name=step.name, workdir=workdir,
                    script="\n".join(body), env=dict(step.env))
    try:
        commands_of(followed, lambda _step, _text: "(read by the reader's own answer)")
    except Refused:
        return None
    return followed


def spellings(path: str, workdir: str) -> set[str]:
    """The ways a run: in workdir names path, a repository-relative file: its path from workdir
    behind `./`, and bare where that path holds a `/` (a bare name without one is looked up on
    PATH by the shell, not in the directory)."""
    if workdir in ("", "."):
        relative = path
    elif path.startswith(f"{workdir}/"):
        relative = path[len(workdir) + 1:]
    else:
        return set()
    return {f"./{relative}"} | ({relative} if "/" in relative else set())


def audited_step(workflow: str, job: str, name: str, workdir: str, script: str) -> Step | None:
    """The step a run: that is the path of an AUDITED_SCRIPTS script makes, alone or followed by
    an argument list its entry reads, read as that entry's command for those arguments in its
    module, or None where the run's text holds no such script's file name. A run whose text holds
    one anywhere else - an operator, another command, a `$(…)`, a comment, another line, an
    argument list the entry does not read - is refused rather than read or skipped: the test is
    on the raw text, before any shell reading, so no quoting or substitution hides the script
    from it. So is a script whose text no longer has its entry's SHA-256."""
    label = f"{workflow}'s {job} / {name}"
    named = [path for path in AUDITED_SCRIPTS if Path(path).name in script]
    if not named:
        return None
    # Only the shell's own blanks and the line's end: Python's strip() also takes Unicode spaces,
    # which bash reads as part of the name.
    alone = script.strip(" \t\n")
    # The path is the text up to the first space, and the arguments the text after it, compared
    # with each listed argument list as written: one space between words, nothing else.
    spelled, _, arguments = alone.partition(" ")
    # The literal spelling, compared with no path normalization: `path/` and `path/.` resolve to
    # the file in Python and name nothing the shell can run.
    matches = [path for path in named if spelled in spellings(path, workdir)]
    if (len(named) != 1 or len(matches) != 1 or any(c.isspace() for c in spelled)
            or arguments not in AUDITED_SCRIPTS[matches[0]].commands):
        raise Refused(f"{label}: names {', '.join(named)} in a form no AUDITED_SCRIPTS entry "
                      "reads: an entry reads a run: that is that one path, alone or followed by "
                      "an argument list the entry lists")
    path = matches[0]
    entry = AUDITED_SCRIPTS[path]
    digest = hashlib.sha256((ROOT / path).read_bytes()).hexdigest()
    if digest != entry.sha256:
        raise Refused(f"{label}: {path} has SHA-256 {digest}, and its AUDITED_SCRIPTS entry was "
                      f"read from {entry.sha256}; read the script again and update the entry's "
                      "commands and digest in .github/scripts/go_test_steps.py")
    return Step(workflow=workflow, job=job, name=name, workdir=entry.module, script=entry.commands[arguments])


def commands_of(step: Step, answer: Answer) -> list[GoTest]:
    """Every `go test` a step's script starts, each with its packages, tags, -race and -run. A
    line a `\\` continues is read as one line with the next, as bash reads it."""
    found: list[GoTest] = []
    read_block(step, step.script.replace("\\\n", " ").splitlines(), {}, found, answer)
    if not found:
        raise Refused(f"{step.label}: its run: holds `go test` and no command this reader can read")
    return found


def read_block(step: Step, lines: list[str], variables: dict[str, str],
               found: list[GoTest], answer: Answer) -> None:
    index = 0
    while index < len(lines):
        line = lines[index].strip()
        index += 1
        if not line or line.startswith("#") or line.startswith("set "):
            continue

        loop = FOR_LINE.match(line)
        if loop:
            body, index = loop_body(step, lines, index)
            for value in shlex.split(expand(loop.group(2), variables)):
                read_block(step, body, {**variables, loop.group(1): value}, found, answer)
            continue

        assignment = ASSIGNMENT.match(line)
        if assignment:
            name, inner = assignment.group(1), expand(assignment.group(2), variables)
            if SPLIT in inner:
                variables[name] = SPLIT_RUN + split_pattern(inner)
            elif TAGGED_TESTS in inner or re.match(r"go\s+list\b", inner):
                variables[name] = answer(step, inner).strip()
            else:
                raise Refused(f"{step.label}: this reader reads only a $(…) call of {TAGGED_TESTS} "
                              f"or {SPLIT}, or a `go list … | grep …` lister, not `{inner}`")
            continue

        piped = XARGS.match(line)
        if piped:
            lister = expand(piped.group("lister"), variables)
            packages = answer(step, lister).split()
            if not packages:
                raise Refused(f"{step.label}: `{lister}` printed no package")
            command = parse_go_test(step, expand(piped.group("test"), variables), packages)
            found.append(command)
            continue

        words, command_text = go_command(line)
        if len(words) > 1 and words[0] == "go" and words[1] in NO_TEST_GO:
            continue
        if len(words) > 1 and words[0] == "go" and words[1] == "test":
            command = parse_go_test(step, expand(command_text, variables), [])
            command.env = {**line_env(line), **command.env}
            found.append(command)
            continue
        if words and words[0].rsplit("/", 1)[-1] in GOTESTSUM_COMMANDS:
            command = parse_gotestsum(step, expand(command_text, variables))
            command.env = {**line_env(line), **command.env}
            found.append(command)
            continue
        if (words and not GO_WORD.search(line) and words[0] not in NOT_PASSED_OVER
                and "/" not in words[0] and not words[0].endswith((".sh", ".py"))):
            continue
        raise Refused(f"{step.label}: this reader cannot read `{line}`")


def go_command(line: str) -> tuple[list[str], str]:
    """The command a line runs, with the variables it sets before it dropped: its words, and its
    text with those words removed.

    A step writes `GODEBUG=checkfinalizers=1 go test …` and
    `RACE_MEM_LIMIT_KB=8388608 ../../.github/scripts/go-test.sh --race-memory …`. The variables
    change which test functions the command runs in no way, which is all the readers read, so
    they are dropped and what is left is the go command, or the gotestsum or go-test.sh command
    that runs it. A value holding `$` is left where it is, so the caller refuses the line rather
    than guessing. The text is cut word by word off the line itself, so the quoting of what is
    left - a `-run` pattern above all - is the line's own. The variables themselves are
    line_env's.
    """
    rest = line.strip()
    while True:
        head = rest.split(None, 1)
        if len(head) == 2 and ENV_PREFIX.match(head[0]):
            rest = head[1]
            continue
        break
    return rest.split(), rest


def line_env(line: str) -> dict[str, str]:
    """The variables a line sets before its command, `NAME=value command`, which go_command
    drops: the command's environment, its test binary's included."""
    found = {}
    for word in line.strip().split():
        if not ENV_PREFIX.match(word):
            break
        name, _, value = word.partition("=")
        found[name] = value.strip("'\"")
    return found


def parse_gotestsum(step: Step, text: str) -> GoTest:
    """The `go test` a gotestsum command runs: the packages its `--packages` lists and the `go
    test` flags (and any package) after its `--`, read as parse_go_test reads a `go test`, with
    the command's text kept as the line writes it. gotestsum's own flags - `--format`,
    `--jsonfile`, the `--rerun-fails` family - choose how it reports and whether it runs a
    failed test again, never which tests the first run runs."""
    words = shlex.split(text)
    position, packages = 1, []
    while position < len(words):
        word = words[position]
        position += 1
        if word == "--":
            break
        flag, equals, value = word.partition("=")
        if not word.startswith("-"):
            raise Refused(f"{step.label}: `{text}` gives gotestsum `{word}` before its `--`, and "
                          "this reader reads packages from --packages and go test flags after `--`")
        if flag in GOTESTSUM_REFUSED:
            raise Refused(f"{step.label}: `{text}` runs gotestsum with {flag}, whose run this "
                          "reader cannot read off the command")
        if flag in GOTESTSUM_VALUE_FLAGS and not equals:
            if position >= len(words):
                raise Refused(f"{step.label}: `{text}` gives gotestsum's {flag} no value")
            value = words[position]
            position += 1
        if flag == "--packages":
            packages += value.split()
    command = parse_go_test(step, shlex.join(["go", "test", *words[position:]]), packages)
    command.text = text
    return command


def loop_body(step: Step, lines: list[str], index: int) -> tuple[list[str], int]:
    body, depth = [], 1
    while index < len(lines):
        line = lines[index].strip()
        index += 1
        if FOR_LINE.match(line) or line.startswith("while ") or line.startswith("until "):
            depth += 1
        elif line == "done":
            depth -= 1
            if depth == 0:
                return body, index
        body.append(line)
    raise Refused(f"{step.label}: a `for` loop with no `done`")


def skip_names(pattern: str) -> list[str] | None:
    """The test names an anchored alternation leaves out, or None for a pattern of any other
    shape, which the caller refuses by name rather than guessing at."""
    match = SKIP_ALTERNATION.match(pattern)
    if not match:
        return None
    names = match.group(1).split("|")
    return names if all(NAME.match(name) for name in names) else None


def split_pattern(text: str) -> str:
    """The `-run=<pattern>` a call of SPLIT in text passes it, or "" where it splits every test of
    its packages."""
    match = SPLIT_PATTERN.search(text[text.index(SPLIT):])
    return match.group(1) if match else ""


def parse_go_test(step: Step, text: str, packages: list[str]) -> GoTest:
    """The packages, -tags, -race, -run and -skip of one `go test` command, and the variables a
    `-exec 'env NAME=value'` hands its test binary. A `-run` go-test-split.sh prints is read as
    the `-run=<pattern>` its call passes the script, or none, the command marked `split`. The
    words after `-args` are the test binary's own, so the reading stops there."""
    command = GoTest(step=step, text=text, packages=list(packages))
    words = shlex.split(text)
    position = words.index("test") + 1
    while position < len(words):
        word = words[position]
        position += 1
        if word == "-args":
            break
        if word == "-race":
            command.race = True
        elif word.startswith("-"):
            flag, _, value = word.partition("=")
            if not value and flag in VALUE_FLAGS:
                value = words[position]
                position += 1
            if flag == "-run" and (value.startswith(SPLIT_RUN) or SPLIT in value):
                command.split = True
                command.run = value.removeprefix(SPLIT_RUN) if value.startswith(SPLIT_RUN) else split_pattern(value)
            elif flag == "-run":
                command.run = value
            elif flag == "-exec":
                exec_words = shlex.split(value)
                if exec_words[:1] == ["env"]:
                    command.env.update(line_env(" ".join(exec_words[1:]) + " -"))
            elif flag == "-skip":
                if skip_names(value) is None:
                    raise Refused(f"{step.label}: `{text}` skips by `{value}`, and this reader "
                                  "reads only the anchored alternation of whole test names, "
                                  "`^(Name|Name|…)$`, that every -skip of this repository writes")
                command.skip = value
            elif flag == "-tags":
                command.tags = value
            elif flag == "-bench":
                raise Refused(f"{step.label}: `{text}` runs benchmarks, which this reader "
                              "does not expand")
        else:
            command.packages.append(word)
    if not command.packages:
        raise Refused(f"{step.label}: `{text}` names no package")
    return command
