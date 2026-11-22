#!/usr/bin/env bash
# Every file under .github/workflows stays small enough for GitHub to read it, and plain enough for
# GitHub's own parser to accept it.
#
# A workflow file above GitHub's cap of 500 KB starts no run at all, and it fails silently: the run
# appears with no jobs in it, no annotation and no error, so the change that crossed the cap reads
# as a change that had nothing to do. .github/workflows/ci.yaml crossed it, which is why this check
# exists (dispatch://INFRA-2507). So the limit held here is 64,000 bytes, far below the cap: a
# workflow file is a list of jobs, and filter data, inline shell and prose belong in a file of their
# own that a job reads, where a reviewer can read them too.
#
# What it refuses, reporting every fault of every file in one run:
#
#   1. a file above 64,000 bytes;
#   2. a file above 500,000 bytes, GitHub's own cap, which is the same fault with the consequence
#      named, because such a file starts no run whatever this check says;
#   3. a file that is not valid YAML, which GitHub reads no further than either;
#   4. two keys of one mapping that are equal after upper-casing. GitHub's parsers refuse both a
#      repeated key and a key differing only in case (actions/languageservices
#      workflow-parser/src/templates/template-reader.ts:212 and :359, actions/runner
#      TemplateReader.cs with StringComparer.OrdinalIgnoreCase), while the YAML layer passes them
#      through on purpose (yaml-object-reader.ts parses with uniqueKeys: false). A load cannot find
#      it — yaml.safe_load keeps the last of a repeated key and reports nothing — so this walks the
#      composed nodes instead;
#   5. a file calling more than 45 unique reusable workflows. GitHub allows 50 from a single
#      workflow file, counting every tree of nested calls. This check follows only the calls to
#      this repository's own workflows (`./.github/workflows/…`) and counts a call to another
#      repository's workflow as one call. ci.yaml calls 43 today, so 45 leaves room for two more
#      calls and none for a surprise;
#   6. a job whose `env:`, or one of whose steps' `env:`, sets JOB_TIMEOUT_MINUTES to anything but
#      the job's timeout-minutes (GitHub's default of 360 where the job sets none), and a step whose
#      `env:` sets STEP_TIMEOUT_MINUTES to anything but that step's own timeout-minutes.
#      .github/scripts/gh-read.sh waits for a spent API budget within those two numbers, measured
#      from the job's start and the step's, and GitHub cancels the job at its timeout-minutes, so a
#      larger number would let the wait outlive the job, and a smaller one would fail by name a wait
#      the job had time for. The values are compared as written, so an expression must be the same
#      expression. Three more ways to give the wait a wrong number are refused with it: either name
#      in the workflow's own `env:`, which stands for no one job's limit; STEP_TIMEOUT_MINUTES in a
#      job's `env:`, which stands for no one step's; and a step with a timeout-minutes of its own
#      that reaches gh-read.sh and sets no STEP_TIMEOUT_MINUTES, whose wait would see only the
#      job's limit. A step reaches gh-read.sh when its `run:` runs or sources it, or a script of
#      .github/scripts that does, followed through the scripts that run or source each other (test
#      files aside; COMMAND_PLACE below says what counts as a run), or when its `uses:` is an
#      action of .github/actions, at the checkout's root or under another checkout (`./base/`),
#      whose steps' `run:` does. A path named in a comment or echoed is no reach;
#   7. in ci.yaml and every workflow of this repository it reaches through its calls (the run a
#      pull request and the merge queue get), a step that runs `go test` (a `go test -c` that
#      compiles only aside, a -c after -args being the binary's), itself or through a repository
#      script it runs, sources or hands a shell (followed through the scripts that run each other),
#      other than through .github/scripts/go_test_events.py; a script that step runs under that
#      writer whose go test neither runs under it nor tees into TEST_EVENTS_FILE on its own line;
#      a step that sets TEST_EVENTS_FILE itself; such a step with no `id:` of its own, which names
#      its events file; and a job holding such a step with no step after it that uploads
#      `${{ runner.temp }}/go-test-events/` as the artifact `go-test-events-${{ job.check_run_id }}`
#      with `if-no-files-found: error` and `if: always()` (or `always() && ` the one condition every
#      such step of the job carries). A go test inside quotes or a heredoc of text runs nothing; a
#      heredoc a shell takes is a script, and an unquoted one's `$(...)` runs. FIXTURES below names
#      the harnesses whose go test runs are their own fixtures. Without -v or -json a go test
#      step's log says only `ok <package> <time>`, so the events that writer keeps, uploaded under
#      the job's id, are the one record of which tests the step ran, passed and skipped; a step
#      without it keeps none, and its tests read as run nowhere. Each refusal names the file, the
#      job, the step's number, its name and its line. It reads shell: a program in another language
#      that runs go test is not followed, and neither are a local action's steps (none runs go
#      test). Every script it reads must be one the ci_filters filter selects, the filter that
#      starts the job this check runs in, or the step that reaches it is refused (FILTERS below):
#      a change to such a script could otherwise merge unread by this rule. The artifacts it holds
#      steps to are public for 90 days, unmasked (docs/testing.md, "Which tests a CI job ran").
#
# A file that is not valid YAML is still measured for its bytes, and cannot be read for the other
# faults.
#
# Scope: every file whose parent directory is .github/workflows and whose suffix is .yaml or .yml.
# Any other file of that directory is passed over. Files are discovered by listing that directory,
# not by asking git, so an unsnapshotted change cannot read green locally.
#
# Run from anywhere: .github/scripts/check-workflow-files.sh
# CI runs it in ci.yaml's ci-filters job and in .github/workflows/workflow-files.yaml, and its tests
# in ci.yaml's test job.
set -euo pipefail

# The repeated-key test is workflow_paths.repeated_keys, the same one the loader every filter
# check reads .github/ci-filters.yaml with runs, imported from beside this script's own file: a
# test fixture links only this script in, and the link leads back to the module CI runs. Resolved
# before the cd, which would leave a relative $0 naming nothing. PYTHONDONTWRITEBYTECODE keeps the
# import from leaving a __pycache__ in the tree.
scripts=$(dirname "$(readlink -f "$0")")

cd "$(dirname "$0")/../.."

PYTHONDONTWRITEBYTECODE=1 PYTHONPATH="$scripts" python3 <<'PY'
import os
import re
import sys
from pathlib import Path

import yaml
from workflow_paths import PathsFilter, repeated_keys

WORKFLOWS = Path(".github/workflows")
SUFFIXES = {".yaml", ".yml"}
# The limit this check holds, and the cap GitHub holds. Every refusal names both: a reader who has
# only crossed the first needs to know what the second does to a run.
LIMIT = 64_000
GITHUB_CAP = 500_000
# Calls counted as fault 5 of the header says: a call to this repository's own workflow is
# followed, and a call to another repository's workflow counts as one.
CALL_LIMIT = 45
GITHUB_CALL_CAP = 50
# Where the rule the two size refusals enforce is written down: what a workflow file holds, and
# where the rest goes.
RULE = ".github/ci-notes/README.md"


def one_line(text: object) -> str:
    """A parser's message on one line: an annotation is one line, and a traceback is noise to the
    reader who has to fix the file."""
    return " ".join(str(text).split())


targets = (
    sorted(path for path in WORKFLOWS.iterdir() if not path.is_dir() and path.suffix in SUFFIXES)
    if WORKFLOWS.is_dir()
    else []
)

problems: list[str] = []
nodes: dict[Path, tuple[object, object]] = {}


def escape_message(value: str) -> str:
    """A workflow command's message, escaped as ci_filter_coverage.py's escape_annotation_message
    does: `%`, CR and LF, and the legacy `##[` command, which the runner recognises anywhere in a
    line. A message can hold YAML text (a repeated key, a `uses:` value), and the runner reads
    each line of a step's output as a command of its own, so a key holding a newline would start
    one. This is a copy rather than an import because this check runs when ci.yaml cannot, and
    importing the filter engine would tie it to the files the filter checks read."""
    return (
        value.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A").replace("##[", "%23%23%5B")
    )


def escape_property(value: str) -> str:
    """A property's value, escaped as ci_filter_coverage.py's escape_property does: the message
    escape above, then `:` and `,`, since the runner ends the properties at `::` and splits them
    on `,`."""
    return escape_message(value).replace(":", "%3A").replace(",", "%2C")


def problem(path: Path, message: str, line: int | None = None) -> None:
    where = escape_property(str(path)) + ("" if line is None else f",line={line}")
    problems.append(f"::error file={where}::{escape_message(message)}")


def limits(path: Path) -> str:
    """The two numbers a refusal for anything but the size names all the same, with the file's own
    bytes against them, so that every refusal says what the limit and the cap are."""
    try:
        size = f"{path.stat().st_size:,} bytes"
    except OSError:
        size = "of a size this check could not read"
    return (
        f" The file is {size}, against this check's limit of {LIMIT:,} bytes and GitHub's cap "
        f"of {GITHUB_CAP:,} bytes (500 KB), above which GitHub starts no run at all."
    )


def composed(path: Path) -> tuple[object, object]:
    """The file's YAML node tree, or the error that stopped it. Cached, since a callee is read once
    however many jobs call it."""
    if path not in nodes:
        try:
            nodes[path] = (yaml.compose(path.read_text()), None)
        except (OSError, UnicodeDecodeError, yaml.YAMLError) as error:
            nodes[path] = (None, error)
    return nodes[path]


def mapping_get(node: object, key: str) -> object:
    if not isinstance(node, yaml.MappingNode):
        return None
    for name, value in node.value:
        if isinstance(name, yaml.ScalarNode) and name.value == key:
            return value
    return None


def check_folded_keys(path: Path, root: object) -> None:
    """Every mapping reachable from the document, sequences included: two keys equal after
    upper-casing are two keys GitHub's parser reads as one, and it refuses the file. Each node is
    visited once, so a mapping an alias repeats is reported once and a recursive alias ends."""
    stack = [root]
    seen: set[int] = set()
    while stack:
        node = stack.pop()
        if id(node) in seen:
            continue
        seen.add(id(node))
        if isinstance(node, yaml.MappingNode):
            for first, name in repeated_keys(node, lambda key: key.value.upper()):
                line = name.start_mark.line + 1
                problem(
                    path,
                    f"{path}:{line}: the mapping key '{name.value}' is the key "
                    f"'{first.value}' of line {first.start_mark.line + 1} again after "
                    f"upper-casing. GitHub's workflow parser compares keys without regard "
                    f"to case and refuses the file, while YAML keeps the last of the two "
                    f"and reports nothing." + limits(path),
                    line=line,
                )
            for name, value in node.value:
                if not isinstance(name, yaml.ScalarNode):
                    stack.append(name)
                stack.append(value)
        elif isinstance(node, yaml.SequenceNode):
            stack.extend(node.value)


def local_callee(uses: str) -> Path | None:
    """A call this repository holds the file for, which is followed; anything else (an
    owner/repo/.github/workflows/x.yml@ref) counts as one call and is not followed."""
    if not uses.startswith("./"):
        return None
    return Path(uses[2:])


def job_uses(node: object) -> list[str]:
    """A job-level `uses:` is a reusable-workflow call. A step-level `uses:` is an action, and
    GitHub counts no action against this limit."""
    found = []
    jobs = mapping_get(node, "jobs")
    if isinstance(jobs, yaml.MappingNode):
        for _, job in jobs.value:
            uses = mapping_get(job, "uses")
            if isinstance(uses, yaml.ScalarNode) and uses.value.strip():
                found.append(uses.value.strip())
    return found


def unique_callees(start: Path) -> list[str]:
    """Every reusable workflow the file reaches, each counted once however many jobs call it and
    however deep the tree that reaches it. The file itself is not one of them."""
    called: dict[str, None] = {}
    queue = [start]
    read = {start}
    while queue:
        node, error = composed(queue.pop(0))
        if error is not None:
            continue
        for uses in job_uses(node):
            callee = local_callee(uses)
            if callee == start:
                continue
            called[uses if callee is None else str(callee)] = None
            if callee is not None and callee not in read:
                read.add(callee)
                queue.append(callee)
    return list(called)


def scalar(node: object) -> str | None:
    return node.value.strip() if isinstance(node, yaml.ScalarNode) else None


HELPER = "gh-read.sh"
# The ways a line of shell runs or sources a script, or keeps its path in a variable to run later:
# the path as a command's first word (a line's start, or after `;`, `&`, `|`, `!`, `$(`, `( `, a
# shell keyword, exec, time, env's assignments, timeout's limit, or bash or sh with their flags,
# `bash -c '<path>'` among them), a `${{ ... }}` expression allowed as its first part
# (`${{ github.workspace }}/.github/scripts/<x>`); `source` or `.` followed, anywhere later on the
# line, by the path or the bare name, however a `$(...)` builds its directory; and a shell
# assignment (no space around the `=`) whose value ends in the path. A path named in a comment, a
# description or a Python string runs nothing and is no reach.
COMMAND_PLACE = (
    r"(?:^|[;&|!]|\$\(|\(\s|\b(?:then|do|else|if|while|until|exec|time)\s|\b(?:bash|sh)(?:\s+-\w+)*\s"
    r"|\btimeout\s+\S+\s|\benv(?:\s+\w+=\S*)*\s)\s*[\"']?(?:\$\{\{[^}]*\}\})?[^\s\"';&|()]*/"
)
SOURCED = r"(?:^|[\s;&|\"'])(?:source|\.)\s+[\"']?(?:.*/)?"
ASSIGNED = r"(?:^|[\s;&|])[A-Za-z_]\w*=.*/"
# A local action, at the root of the checkout or of another checkout inside it (`./base/`).
LOCAL_ACTION = re.compile(r"^\./(?:[^/]+/)*\.github/actions/([^/]+)/?$")


def runs_any(text: str, names: set[str]) -> list[str]:
    """The names among `names` that a line of shell text outside a comment runs or sources, by a
    path ending in that name in one of the places above."""
    found = set()
    for line in text.splitlines():
        if line.lstrip().startswith("#"):
            continue
        for name in names:
            end = re.escape(name) + r"(?![\w.-])"
            if any(re.search(place + end, line) for place in (COMMAND_PLACE, SOURCED, ASSIGNED)):
                found.add(name)
    return sorted(found)


def helper_reachers() -> tuple[set[str], set[str]]:
    """The basenames of the scripts of .github/scripts that reach HELPER, HELPER itself among
    them, and the names of the actions of .github/actions whose steps' `run:` reaches one: a
    script reaches it when a line of it runs or sources HELPER or a script that does
    (runs_any), followed until nothing more joins. Test files and their libraries run no step's
    API call and are left out."""
    texts: dict[str, str] = {}
    for script in Path(".github/scripts").glob("*.sh"):
        if script.name.endswith(".test.sh") or "test-lib" in script.name:
            continue
        try:
            texts[script.name] = script.read_text(errors="replace")
        except OSError:
            continue
    scripts = {HELPER}
    while True:
        joined = {name for name, text in texts.items() if name not in scripts and runs_any(text, scripts)}
        if not joined:
            break
        scripts |= joined
    actions: set[str] = set()
    for action in Path(".github/actions").glob("*/action.y*ml"):
        try:
            doc = yaml.safe_load(action.read_text(errors="replace"))
        except (OSError, yaml.YAMLError):
            continue
        runs = doc.get("runs") if isinstance(doc, dict) else None
        steps = runs.get("steps") if isinstance(runs, dict) else None
        if not isinstance(steps, list):
            continue
        text = "\n".join(str(step.get("run", "")) for step in steps if isinstance(step, dict))
        if runs_any(text, scripts):
            actions.add(action.parent.name)
    return scripts, actions


def check_timeout_variables(path: Path, root: object, reachers: tuple[set[str], set[str]]) -> None:
    """Rule 6 of the header: each JOB_TIMEOUT_MINUTES a job or its steps set is the job's
    timeout-minutes, each STEP_TIMEOUT_MINUTES a step sets is that step's, neither name stands in
    the workflow's env, STEP_TIMEOUT_MINUTES stands in no job's env, and a step with a limit of
    its own that reaches gh-read.sh passes that limit."""
    why = (
        " .github/scripts/gh-read.sh waits for a spent API budget within this number, measured from "
        "the job's start (STEP_TIMEOUT_MINUTES: the step's), and GitHub cancels the job at its "
        "timeout-minutes: a larger number lets the wait outlive the job, and a smaller one fails "
        "by name a wait the job had time for. Write the two as one number."
    )
    scripts, actions = reachers

    def refuse(where: object, who: str, name: str, value: str, limit: str) -> None:
        line = where.start_mark.line + 1
        problem(path, f"{path}:{line}: {who} sets {name} to {value}, but {limit}." + why, line=line)

    def misplaced(where: object, text: str) -> None:
        line = where.start_mark.line + 1
        problem(path, f"{path}:{line}: {text}", line=line)

    for name in ("JOB_TIMEOUT_MINUTES", "STEP_TIMEOUT_MINUTES"):
        value = mapping_get(mapping_get(root, "env"), name)
        if value is not None:
            misplaced(value, f"the workflow's env sets {name}, which stands for no one "
                             f"{'job' if name.startswith('JOB') else 'step'}'s timeout-minutes: set it "
                             f"in the env of each {'job' if name.startswith('JOB') else 'step'} it is "
                             f"that limit of, where this check holds it to that number.")
    jobs = mapping_get(root, "jobs")
    if not isinstance(jobs, yaml.MappingNode):
        return
    for job_name, job in jobs.value:
        if not isinstance(job, yaml.MappingNode):
            continue
        job_id = scalar(job_name)
        timeout = mapping_get(job, "timeout-minutes")
        if timeout is None:
            job_limit, job_said = "360", "its timeout-minutes is 360 (GitHub's default, as the job sets none)"
        else:
            job_limit = scalar(timeout)
            job_said = f"its timeout-minutes is {job_limit} (line {timeout.start_mark.line + 1})"
        value = mapping_get(mapping_get(job, "env"), "JOB_TIMEOUT_MINUTES")
        if value is not None and scalar(value) != job_limit:
            refuse(value, f"job {job_id}", "JOB_TIMEOUT_MINUTES", scalar(value), job_said)
        value = mapping_get(mapping_get(job, "env"), "STEP_TIMEOUT_MINUTES")
        if value is not None:
            misplaced(value, f"job {job_id}'s env sets STEP_TIMEOUT_MINUTES, which stands for no one "
                             f"step's timeout-minutes: set it in the env of the step it is that limit of.")
        steps = mapping_get(job, "steps")
        if not isinstance(steps, yaml.SequenceNode):
            continue
        for index, step in enumerate(steps.value, start=1):
            env = mapping_get(step, "env")
            own = mapping_get(step, "timeout-minutes")
            value = mapping_get(env, "JOB_TIMEOUT_MINUTES")
            if value is not None and scalar(value) != job_limit:
                refuse(value, f"job {job_id}", "JOB_TIMEOUT_MINUTES", scalar(value), job_said)
            value = mapping_get(env, "STEP_TIMEOUT_MINUTES")
            if value is None:
                if own is None:
                    continue
                run = scalar(mapping_get(step, "run")) or ""
                uses = scalar(mapping_get(step, "uses")) or ""
                via = runs_any(run, scripts)
                local = LOCAL_ACTION.match(uses)
                if local and local.group(1) in actions:
                    via.append(uses)
                if via:
                    misplaced(own, f"step {index} of job {job_id} sets timeout-minutes {scalar(own)} and "
                                   f"reaches gh-read.sh (through {', '.join(via)}), but sets no "
                                   f"STEP_TIMEOUT_MINUTES: its wait for a spent API budget would see only "
                                   f"the job's limit and could outlive the step's. Set STEP_TIMEOUT_MINUTES "
                                   f"to {scalar(own)} in the step's env.")
                continue
            if own is None:
                refuse(value, f"step {index} of job {job_id}", "STEP_TIMEOUT_MINUTES", scalar(value),
                       "the step sets no timeout-minutes")
            elif scalar(value) != scalar(own):
                refuse(value, f"step {index} of job {job_id}", "STEP_TIMEOUT_MINUTES", scalar(value),
                       f"its timeout-minutes is {scalar(own)} (line {own.start_mark.line + 1})")


REACHERS = helper_reachers()


# Rule 7 of the header. The writer is named by its file name wherever a step's directory puts it,
# as gh-read.sh is, quoted or not; race_memory.py may stand between it and the go test it samples,
# since it appends what it reads to the file the writer names.
ROUTER = WORKFLOWS / "ci.yaml"
WRITER = ".github/scripts/go_test_events.py"
ENV_FILE = "TEST_EVENTS_FILE"
# The harnesses whose own go test runs are fixtures: the runs each of their cases compares or
# plants, over a module or a narrowed package of the harness's making, and no test of this
# repository a reader asks the result of. A step that runs one is no go test step, and its other
# go test commands are held as any step's are. Each entry says why.
FIXTURES = {
    ".github/scripts/go_test_events.test.sh":
        "the writer's own harness: its plain go test runs over a scratch module are what each case "
        "compares the writer's log with",
    "packages/envoy/scripts/database-skips.test.sh":
        "the skip counter's controls: each runs the counter over a planted or narrowed package to "
        "hold its count, and envoy-go's database-skips step is the counter's own run",
}
UPLOAD_ACTION = "actions/upload-artifact"
UPLOAD_WITH = {
    "name": "go-test-events-${{ job.check_run_id }}",
    "path": "${{ runner.temp }}/go-test-events/",
    "if-no-files-found": "error",
}
# Where a shell word stands as the command it runs: a line's start, after an operator or `$(`,
# after a keyword or a command that runs its arguments, and past a run of assignments (a value
# quoted or not). A word anywhere else - an argument - runs nothing, and neither does one inside
# quotes (an echo's text, a fixture's string) or a heredoc of text, which unquoted() and
# logical_lines() leave out.
# A value holding `$(` or a backquote is no plain word: the command it starts is read at its own
# place (`pattern=$(.github/scripts/go-tagged-tests.sh ...)`), so the assignment does not take it.
EVENTS_PLACE = (r"(?:^|[;&|(`]|\$\(|\b(?:then|do|else|elif|if|while|until|exec|time|xargs|env|nohup|setsid|command)\s+)"
                r"\s*(?:[A-Za-z_]\w*=(?:\"[^\"]*\"|'[^']*'|(?![^\s]*(?:\$\(|`))[^\s\"'`]\S*)?\s+)*")
# A command word, quoted or not: `arg` starts at its opening quote, which stands outside the
# string it opens, so a quoted path at a command's place is read as the command it is.
WORD = r"(?P<arg>[\"']?(?P<word>[^\s\"';&|()]+)[\"']?)"
# The writer, run as a command or by python3, with race_memory.py (which runs the command it is
# handed and appends what it reads to the writer's file) after it or not.
PATH_WORD = r"[\"']?[^\s\"';&|()]*"
SAMPLER = rf"(?:{PATH_WORD}race_memory\.py[\"']?\s+)?"
WRAPPER = rf"(?:python3?\s+)?{PATH_WORD}go_test_events\.py[\"']?\s+{SAMPLER}"
# A go test, the writer before it or not; `cmd` is the command alone, from its first word to the
# end of its flags, and `go` its go, quoted or not, with go's -C before test or not.
GO_TEST = re.compile(EVENTS_PLACE + rf"(?P<cmd>(?P<wrapper>{WRAPPER})?{SAMPLER}(?P<go>[\"']?go[\"']?)\s+"
                     r"(?:-C(?:\s+|=)\S+\s+)?test\b(?P<rest>[^;&|)`]*))")
# A command that runs a file: the file as the command, or the file a shell, python3, source or `.`
# runs; the writer before it or not.
RUNS = re.compile(EVENTS_PLACE + rf"(?P<wrapper>{WRAPPER})?"
                  rf"(?:(?:bash|sh|dash|zsh|ksh|python3?|source|\.)(?:\s+-\w+)*\s+)?{WORD}")
# A go test -json's own line that tees what it writes into the writer's file itself, as the
# counters do: `| tee -a "$TEST_EVENTS_FILE"`, `"${TEST_EVENTS_FILE}"` or `"${TEST_EVENTS_FILE:-/dev/null}"`.
TEES = re.compile(r"^\s*\|\s*tee\s+(?:-a|--append)\s+(?:\"\$\{?TEST_EVENTS_FILE(?::-[^}\"]*)?\}?\""
                  r"|\$\{?TEST_EVENTS_FILE(?::-[^}\s]*)?\}?)(?=\s|$)")
# A step's own assignment of the writer's file, at a command's place.
ASSIGNS = re.compile(r"(?:^|[;&|(]|\b(?:export|env|declare|local|readonly)\s+(?:-\w+\s+)*)\s*(?P<name>TEST_EVENTS_FILE)=")
HEREDOC = re.compile(r"<<-?\s*(?P<quote>[\"']?)(?P<delimiter>[A-Za-z_]\w*)(?P=quote)")
# A heredoc a shell takes as its script (`bash <<EOF`, `bash -s <<'EOF'`, `env bash <<EOF`), and a
# script a shell takes as a string (`bash -c '...'`).
SHELL_AT = r"(?:^|[;&|(`]\s*|\b(?:exec|then|do|else|env|command|nohup|time|setsid|xargs)\s+)(?:\S*/)?(?:bash|sh|dash|zsh|ksh)"
SHELL_HEREDOC = re.compile(SHELL_AT + r"(?:\s+-\S+)*\s*<<$")
SHELL_STRING = re.compile(SHELL_AT + r"(?:\s+-\w+)*\s+-\w*c\w*\s+(?P<quote>['\"])(?P<script>.*?)(?P=quote)")
# The commands an unquoted heredoc's body runs: its `$(...)` and backquotes, a backslash before
# them aside (`\$(...)` is text).
SUBSTITUTION = re.compile(r"(?<!\\)\$\(([^()]*)\)")
BACKQUOTED = re.compile(r"(?<!\\)`([^`]*)(?<!\\)`")
SCRIPT_SUFFIXES = (".sh", ".py")
TRUE = frozenset({"1", "t", "T", "TRUE", "true", "True"})
census = {"commands": 0, "steps": 0, "jobs": 0}


def router_scope() -> set[Path]:
    """ci.yaml and every workflow of this repository it reaches through its calls, or nothing
    when the directory holds no ci.yaml."""
    if not ROUTER.is_file():
        return set()
    return {ROUTER} | {Path(called) for called in unique_callees(ROUTER) if Path(called).is_file()}


def logical_lines(text: str, offset: int = 0) -> list[tuple[int, str]]:
    """A script's lines as the shell reads them: each with the backslash continuations after it
    joined on, comments and blank lines aside, with the number of its first line. A heredoc's body
    is read as the shell reads it: as lines of a script when a shell takes it as one (`bash
    <<EOF`), as the commands its `$(...)` and backquotes run when its delimiter is unquoted, and
    not at all when it is text (`cat <<'EOF'`)."""
    found, pending, first, heredoc, body = [], "", 0, None, []
    for number, raw in enumerate(text.splitlines(), start=offset + 1):
        if heredoc is not None:
            delimiter, mode, start = heredoc
            if raw.strip() != delimiter:
                body.append(raw)
                continue
            if mode == "script":
                found += logical_lines("\n".join(body), start)
            elif mode == "substitutions":
                for at, line in enumerate(body, start=start + 1):
                    found += [(at, inner) for inner in SUBSTITUTION.findall(line) + BACKQUOTED.findall(line)]
            heredoc, body = None, []
            continue
        if not pending:
            first = number
            if not raw.strip() or raw.lstrip().startswith("#"):
                continue
        if raw.rstrip().endswith("\\"):
            pending += raw.rstrip()[:-1] + " "
            continue
        line = uncommented((pending + raw).strip())
        pending = ""
        if not line:
            continue
        found.append((first, line))
        # A script a shell takes as a string is lines of its own, read the same way.
        outside = unquoted(line)
        for string in SHELL_STRING.finditer(line):
            if outside[string.start("quote")]:
                found += [(first, inner) for _, inner in logical_lines(string.group("script"))]
        opened = HEREDOC.search(line.replace("<<<", "   "))
        if opened and unquoted(line)[opened.start()]:
            mode = ("script" if SHELL_HEREDOC.search(line[: opened.start() + 2])
                    else "text" if opened.group("quote") else "substitutions")
            heredoc = (opened.group("delimiter"), mode, number)
    if pending:
        found.append((first, pending.strip()))
    return found


def unquoted(line: str) -> list[bool]:
    """For each character of a line, whether it stands where the shell reads a command: outside
    every quoted string ('...', "..." with its backslash escapes, a backslash-escaped character),
    or inside a `$(...)` or `(...)`, which starts a command of its own even within "...". A
    `case` pattern's lone `)` closes nothing."""
    outside = [True] * (len(line) + 1)
    stack, index = [""], 0
    while index < len(line):
        char, state = line[index], stack[-1]
        if state == "'":
            if char == "'":
                stack.pop()
            else:
                outside[index] = False
            index += 1
            continue
        if char == "\\":
            outside[index:index + 2] = [False] * len(outside[index:index + 2])
            index += 2
            continue
        if state == '"':
            if char == '"':
                stack.pop()
            elif line.startswith("$(", index):
                stack.append("(")
                outside[index:index + 2] = [False, False]
                index += 2
                continue
            elif char == "`":
                stack.append("`")
            else:
                outside[index] = False
            index += 1
            continue
        if char == "`" and state == "`":
            stack.pop()
            index += 1
            continue
        if char in "'\"":
            stack.append(char)
        elif char == "`":
            stack.append("`")
        elif char == "(":
            stack.append("(")
        elif char == ")" and state == "(":
            stack.pop()
        index += 1
    return outside


def uncommented(line: str) -> str:
    """The line less a comment: from a `#` that stands outside every quoted string and starts a
    word (`${#x}` and `$#` hold one that does not)."""
    outside = unquoted(line)
    for at, char in enumerate(line):
        if char == "#" and outside[at] and (at == 0 or line[at - 1] in " \t;"):
            return line[:at].rstrip()
    return line


def commands(pattern: re.Pattern, line: str, group: str) -> list[re.Match]:
    """The matches of `pattern` whose `group` stands outside every quoted string of the line."""
    outside = unquoted(line)
    return [match for match in pattern.finditer(line) if outside[match.start(group)]]


def compile_only(rest: str) -> bool:
    """A go test whose own flags, up to -args, set -c true, the last -c winning as go reads its
    flags: it compiles and runs no test."""
    compiles = False
    for word in rest.split(" -args", 1)[0].split():
        name, eq, value = word.lstrip("-").partition("=")
        if word.startswith("-") and name == "c":
            compiles = not eq or value.strip("\"'") in TRUE
    return compiles


def resolve(word: str, places: list[Path]) -> Path | None:
    """The repository script a command word names, looked for from each of `places`: a file whose
    name ends .sh or .py, or one whose first line names a shell (`#!/usr/bin/env bash`). A word the
    shell builds (`$dir/x.sh`) is looked for by its file name in the first place."""
    if word.endswith("go_test_events.py"):
        return None
    if "$" in word:
        candidates = [places[0] / Path(word).name]
    else:
        candidates = [place / word.removeprefix("./") for place in places] + [Path(word.removeprefix("./"))]
    for candidate in candidates:
        try:
            relative = candidate.resolve().relative_to(Path.cwd().resolve())
        except ValueError:
            continue
        if not relative.is_file():
            continue
        if relative.suffix in SCRIPT_SUFFIXES:
            return relative
        try:
            with open(relative, "rb") as opened:
                first = opened.readline(200)
        except OSError:
            continue
        if first.startswith(b"#!") and re.search(rb"\b(?:ba|da|z|k)?sh\b", first):
            return relative
    return None


# How a go test a script runs keeps its events: "self" when it runs under the writer, "tees" when
# its own line tees what it writes into TEST_EVENTS_FILE (kept only under a writer that sets it),
# and None when it keeps none.
script_reads: dict[Path, list] = {}
# Each script rule 7 reads, with the step that first reached it (its file, line and how a refusal
# names it): the set hold_scripts_to_filter() holds to the filter of the job that runs this check.
reached_by: dict[Path, tuple[Path, int, str]] = {}
reaching: dict[str, tuple[Path, int, str]] = {}


def keeps_of(match: re.Match, line: str) -> str | None:
    if match.group("wrapper"):
        return "self"
    # A tee keeps events only of a go test that writes them: -json among its own flags.
    flags = match.group("rest").split(" -args", 1)[0].split()
    if not any(word in ("-json", "-json=true", "--json") for word in flags):
        return None
    tee = TEES.match(line[match.end():])
    return "tees" if tee and unquoted(line)[match.end() + tee.start()] else None


def script_tests(script: Path, seen: frozenset = frozenset()) -> list[tuple[Path, int, str, str | None]]:
    """Every go test command the script runs, itself or through the scripts it runs, followed
    until nothing more joins: (the file, its line, the command, how it keeps its events, as
    keeps_of says). A compile-only go test -c runs no test, and a fixture harness's runs are its
    own. A script the writer runs keeps a tee's events, since the writer sets their file."""
    if script in seen or str(script) in FIXTURES:
        return []
    if script in script_reads:
        return script_reads[script]
    if "step" in reaching:
        reached_by.setdefault(script, reaching["step"])
    try:
        text = script.read_text(errors="replace")
    except OSError:
        return []
    found = []
    for number, line in logical_lines(text):
        for match in commands(GO_TEST, line, "go"):
            if not compile_only(match.group("rest")):
                found.append((script, number, " ".join(match.group("cmd").split()), keeps_of(match, line)))
        for match in commands(RUNS, line, "arg"):
            inner = resolve(match.group("word"), [script.parent, script.parent.parent, Path(".")])
            if inner is None:
                continue
            reached = script_tests(inner, seen | {script})
            if match.group("wrapper"):
                reached = [(file, at, command, "self" if how else None) for file, at, command, how in reached]
            found += reached
    script_reads[script] = found
    return found


def step_env_names(*holders) -> set[str]:
    names = set()
    for holder in holders:
        env = mapping_get(holder, "env")
        if isinstance(env, yaml.MappingNode):
            names |= {scalar(name) for name, _ in env.value}
    return names


def condition(text: str) -> str:
    """An `if:` as GitHub reads it: the expression inside `${{ }}`, its blanks one."""
    text = " ".join(text.split())
    if text.startswith("${{") and text.endswith("}}"):
        text = text[3:-2].strip()
    return text


def check_go_test_events(path: Path, root: object) -> None:
    """Rule 7: each go test step keeps its events, under an id of its own, and its job uploads
    them after it."""
    jobs = mapping_get(root, "jobs")
    if not isinstance(jobs, yaml.MappingNode):
        return
    workflow_dir = scalar(mapping_get(mapping_get(mapping_get(root, "defaults"), "run"), "working-directory"))
    for job_name, job in jobs.value:
        steps = mapping_get(job, "steps")
        if not isinstance(steps, yaml.SequenceNode):
            continue
        job_id = scalar(job_name)
        default_dir = (scalar(mapping_get(mapping_get(mapping_get(job, "defaults"), "run"), "working-directory"))
                       or workflow_dir or ".")
        last_kept = None
        kept_conditions = set()
        uploads = []
        for index, step in enumerate(steps.value, start=1):
            line = step.start_mark.line + 1
            name = scalar(mapping_get(step, "name")) or scalar(mapping_get(step, "uses")) or "a step with no name"
            where = f"{path}:{line}: step {index} of job {job_id} ({name})"
            reaching["step"] = (path, line, where)
            if (scalar(mapping_get(step, "uses")) or "").split("@")[0] == UPLOAD_ACTION:
                uploads.append((index, step))
            run = scalar(mapping_get(step, "run")) or ""
            workdir = Path(scalar(mapping_get(step, "working-directory")) or default_dir)
            kept, bare, leaky, assigns = 0, [], [], False
            for _, text in logical_lines(run):
                assigns = assigns or bool(commands(ASSIGNS, text, "name"))
                for match in commands(GO_TEST, text, "go"):
                    if compile_only(match.group("rest")):
                        continue
                    if match.group("wrapper"):
                        kept += 1
                    else:
                        bare.append(f"`{' '.join(match.group('cmd').split())}`")
                for match in commands(RUNS, text, "arg"):
                    script = resolve(match.group("word"), [workdir, Path(".")])
                    if script is None or str(script) in FIXTURES:
                        continue
                    reached = script_tests(script)
                    if not reached:
                        continue
                    if match.group("wrapper"):
                        # The writer sets TEST_EVENTS_FILE for the script: a tee keeps its events.
                        kept += 1
                        leaky += [f"`{command}` ({file}:{at})" for file, at, command, how in reached if not how]
                        continue
                    unkept = [(file, at, command) for file, at, command, how in reached if how != "self"]
                    if unkept:
                        file, at, command = unkept[0]
                        bare.append(f"{script}, which runs `{command}` ({file}:{at})")
                    else:
                        kept += 1
            if ENV_FILE in step_env_names(root, job, step) or assigns:
                problem(path, f"{where} sets {ENV_FILE}, which only {WRITER} sets, for a script it runs: a "
                              f"step's own value sends events where the job's upload of "
                              f"$RUNNER_TEMP/go-test-events/ does not look. Take it off the step.", line=line)
            if bare:
                problem(path, f"{where} runs {', '.join(bare)} other than through {WRITER}, so it keeps no "
                              f"record of which tests it ran, passed and skipped: without -v or -json go test "
                              f"prints `ok <package> <time>` alone. Run the command as `<path to {WRITER}> go "
                              f"test ...`, or the script as `<path to {WRITER}> <script> ...`, which keeps every "
                              f"event and prints the log go test prints.", line=line)
                continue
            if leaky:
                problem(path, f"{where} runs a script under {WRITER}, and the script runs {', '.join(leaky)}, "
                              f"which neither runs under that writer nor tees its output into {ENV_FILE} on "
                              f"its own line, so those tests' events are kept nowhere.", line=line)
            if not kept:
                continue
            census["commands"] += kept
            census["steps"] += 1
            last_kept = (index, name, line)
            kept_conditions.add(condition(scalar(mapping_get(step, "if")) or ""))
            step_id = scalar(mapping_get(step, "id")) or ""
            if not step_id or step_id.startswith("__"):
                problem(path, f"{where} runs go test through {WRITER} and has no id: of its own, which names "
                              f"its events file (<id>.jsonl) and which a reader asks for; GitHub's own name for "
                              f"it moves when a step is added above it, and the writer refuses it. Give the "
                              f"step an id:.", line=line)
        if last_kept is None:
            continue
        census["jobs"] += 1
        # The upload runs whenever a go test step ran: always(), or always() and the one condition
        # every such step of the job carries, and nothing else that could hold it back.
        conditions = {"always()"}
        if len(kept_conditions) == 1 and "" not in kept_conditions:
            (only,) = kept_conditions
            conditions |= {f"always() && {only}", f"always() && ({only})"}
        after = [step for index, step in uploads if index > last_kept[0]]
        ok = [step for step in after
              if all(scalar(mapping_get(mapping_get(step, "with"), key)) == value for key, value in UPLOAD_WITH.items())
              and condition(scalar(mapping_get(step, "if")) or "") in conditions]
        if not ok:
            wanted = ", ".join(f"{key}: {value}" for key, value in UPLOAD_WITH.items())
            problem(path, f"{path}:{last_kept[2]}: job {job_id} keeps go test events (its last such step is "
                          f"step {last_kept[0]}, {last_kept[1]}) and no step after it uploads them: add a step "
                          f"`uses: {UPLOAD_ACTION}@<pin>` with `if: always()` (or `always() && ` the one "
                          f"condition its go test steps share) and {wanted}, so a reader fetches them by the "
                          f"job's id, a failed or cancelled run's among them.",
                    line=last_kept[2])


SCOPE = router_scope()

# The filter of the filters file that starts ci.yaml's ci-filters job, the one job that runs this
# check on a whole checkout (workflow-files.yaml checks out .github alone). Every script rule 7
# reads can change its verdict, so a change to one has to start that job: a script the filter does
# not select could gain a bare go test on a pull request this check never ran on, and the next pull
# request that starts the job would fail on a step it never touched. That filter is read as ci.yaml's
# dorny/paths-filter step reads it: an anchor's list flattened in, under some-with-excludes. The
# file is CI_FILTERS_FILE when a job names one (a filters file the changes job wrote), else
# .github/ci-filters.yaml.
FILTERS = Path(os.environ.get("CI_FILTERS_FILE") or ".github/ci-filters.yaml")
CHECK_FILTER = "ci_filters"


def flat(value) -> list[str]:
    if isinstance(value, str):
        return [value]
    if isinstance(value, list):
        return [pattern for item in value for pattern in flat(item)]
    return []


def hold_scripts_to_filter() -> str:
    """Refuse each script rule 7 read that CHECK_FILTER does not select, naming the step that first
    reached it; the summary's phrase for the rest, UNKNOWN when there is no such filter to read."""
    if not reached_by:
        return ""
    try:
        document = yaml.safe_load(FILTERS.read_text()) if FILTERS.is_file() else None
    except (OSError, UnicodeDecodeError, yaml.YAMLError):
        document = None
    patterns = flat(document.get(CHECK_FILTER)) if isinstance(document, dict) else []
    if not patterns:
        return (f", and whether a change to the {len(reached_by)} script(s) rule 7 read starts the "
                f"ci-filters job UNKNOWN: {FILTERS} holds no {CHECK_FILTER} filter")
    selects = PathsFilter(patterns, "some-with-excludes")
    for script, (path, line, where) in sorted(reached_by.items()):
        if not selects.covers(str(script)):
            problem(path, f"{where} runs {script}, itself or through a script it runs, and rule 7 reads it, "
                          f"but the {CHECK_FILTER} filter of {FILTERS} does not select it: a change to that "
                          f"script starts no ci-filters job, the one job that runs this check, so a go test "
                          f"added to it merges unread and fails the next pull request that starts the job. "
                          f"Add a pattern that selects it to that filter, and the read to the "
                          f"{CHECK_FILTER} gate of .github/scripts/ci_job_reads.py.", line=line)
    return (f", and each of the {len(reached_by)} script(s) it read is one the {CHECK_FILTER} filter "
            f"selects")



for path in targets:
    size = path.stat().st_size
    if size > GITHUB_CAP:
        problem(
            path,
            f"{path} is {size:,} bytes, above GitHub's own cap of {GITHUB_CAP:,} bytes (500 KB): "
            f"GitHub starts no run at all for a workflow file this size, and signals it only as a "
            f"run with no jobs in it. It is above this check's limit of {LIMIT:,} bytes as well. "
            f"Move the filter data into .github/ci-filters.yaml, each longer shell body into a script "
            f"under .github/scripts/ and each derivation into the workflow's notes file, as {RULE} says.",
        )
    elif size > LIMIT:
        problem(
            path,
            f"{path} is {size:,} bytes, above this check's limit of {LIMIT:,} bytes. GitHub starts "
            f"no run at all for a workflow file above its own cap of {GITHUB_CAP:,} bytes (500 "
            f"KB), and signals it only as a run with no jobs in it, so this limit keeps every file "
            f"far enough below the cap that no edit crosses it unseen. Move the filter data into "
            f".github/ci-filters.yaml, each longer shell body into a script under .github/scripts/ "
            f"and each derivation into the workflow's notes file, as {RULE} says.",
        )

    node, error = composed(path)
    if error is not None:
        if isinstance(error, yaml.YAMLError):
            problem(
                path,
                f"{path} is not valid YAML: {one_line(error)}. GitHub reads it no further than "
                f"this check does, and neither can read the file's keys or its "
                f"reusable-workflow calls until the syntax is fixed." + limits(path),
            )
        else:
            problem(path, f"{path} could not be read: {one_line(error)}." + limits(path))
        continue
    if node is None:
        continue

    check_folded_keys(path, node)
    check_timeout_variables(path, node, REACHERS)
    if path in SCOPE:
        check_go_test_events(path, node)

    called = unique_callees(path)
    if len(called) > CALL_LIMIT:
        problem(
            path,
            f"{path} calls {len(called)} unique reusable workflows, above this check's limit of "
            f"{CALL_LIMIT}: {', '.join(sorted(called))}. GitHub allows {GITHUB_CALL_CAP} from a "
            f"single workflow file, counting every tree of nested calls, and a file over that cap "
            f"starts no run." + limits(path),
        )

selected = hold_scripts_to_filter() if SCOPE else ""

if problems:
    for annotation in problems:
        print(annotation)
    print(
        f"{len(problems)} problem(s) across the {len(targets)} workflow file(s) under {WORKFLOWS}",
        file=sys.stderr,
    )
    sys.exit(1)

kept = (
    f"keep the events of {census['commands']} go test command(s) in {census['steps']} step(s) of "
    f"{census['jobs']} job(s) of ci.yaml's run{selected}" if SCOPE else
    f"keep go test events UNKNOWN: there is no {ROUTER}, so rule 7 read no step"
)
print(
    f"all {len(targets)} workflow files under {WORKFLOWS} are within {LIMIT:,} bytes and GitHub's "
    f"{GITHUB_CAP:,}-byte cap, parse as YAML, repeat no mapping key after upper-casing, call "
    f"at most {CALL_LIMIT} unique reusable workflows, set JOB_TIMEOUT_MINUTES and "
    f"STEP_TIMEOUT_MINUTES to the timeout-minutes they stand for, and {kept}"
)
PY
