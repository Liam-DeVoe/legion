#!/usr/bin/env bash
# Every file under .github/workflows stays small enough for GitHub to read it, and plain enough for
# GitHub's own parser to accept it, and ci.yaml stays within the bounds R9 sets its layout.
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
#   5. a file calling more than 10 unique reusable workflows, nested calls included. GitHub allows
#      50 from a single workflow file, counting every tree of nested calls; R9 holds ci.yaml far
#      below that, at 10. This check follows only the calls to this repository's own workflows
#      (`./.github/workflows/…`) and counts a call to another repository's workflow as one call;
#   6. ci.yaml routing more than 30 jobs (the keys of its jobs map) besides its carriers: a carrier
#      is a job that only carries the old check-run name of a job split into legs, whose steps are
#      exactly a sparse checkout of .github, one step and the step that reports to ci-ok
#      (.github/actions/ci-ok-report). A carrier with any other step is counted as a job;
#   7. a dorny/paths-filter step whose filter text - its `filters` input inline, or the file that
#      input names - is 10,000 bytes or more. The filters select jobs by package area, one area a
#      job, and a filter that lists files one by one, as the deleted filter file did, is what this
#      bound keeps out.
#
# A file that is not valid YAML is still measured for its bytes, and cannot be read for the other
# faults.
#
# Scope: every file whose parent directory is .github/workflows and whose suffix is .yaml or .yml.
# Any other file of that directory is passed over. Files are discovered by listing that directory,
# not by asking git, so an unsnapshotted change cannot read green locally.
#
# Run from anywhere: .github/scripts/check-workflow-files.sh
# CI runs it in ci.yaml's lint job and in .github/workflows/workflow-files.yaml, and its tests in
# ci.yaml's scripts job.
set -euo pipefail

# The repeated-key test is workflow_paths.repeated_keys, imported from beside this script's own
# file: a test fixture links only this script in, and the link leads back to the module CI runs.
# Resolved before the cd, which would leave a relative $0 naming nothing. PYTHONDONTWRITEBYTECODE
# keeps the import from leaving a __pycache__ in the tree.
scripts=$(dirname "$(readlink -f "$0")")

cd "$(dirname "$0")/../.."

PYTHONDONTWRITEBYTECODE=1 PYTHONPATH="$scripts" python3 <<'PY'
import sys
from pathlib import Path

import yaml
from workflow_paths import repeated_keys

WORKFLOWS = Path(".github/workflows")
SUFFIXES = {".yaml", ".yml"}
# The limit this check holds, and the cap GitHub holds. Every refusal names both: a reader who has
# only crossed the first needs to know what the second does to a run.
LIMIT = 64_000
GITHUB_CAP = 500_000
# Calls counted as fault 5 of the header says: a call to this repository's own workflow is
# followed, and a call to another repository's workflow counts as one.
CALL_LIMIT = 10
GITHUB_CALL_CAP = 50
# Faults 6 and 7: the jobs ci.yaml routes besides its carriers, and the bytes of a filter text.
ROUTER = WORKFLOWS / "ci.yaml"
JOB_LIMIT = 30
FILTER_LIMIT = 10_000
REPORT_ACTION = "./.github/actions/ci-ok-report"
# Where the rule the two size refusals enforce is written down: what a workflow file holds, and
# where the rest goes.
RULE = ".github/ci-notes/README.md"
# What a refusal of size tells the author to move out of the file.
MOVE = (f"Move each longer shell body into a script under .github/scripts/ or a composite action under "
        f".github/actions/, and each derivation into the workflow's notes file, as {RULE} says.")


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
    """A workflow command's message, escaped as ci_layout.py's escape_annotation_message does:
    `%`, CR and LF, and the legacy `##[` command, which the runner recognises anywhere in a line.
    A message can hold YAML text (a repeated key, a `uses:` value), and the runner reads each line
    of a step's output as a command of its own, so a key holding a newline would start one. This
    is a copy rather than an import because this check runs when ci.yaml cannot, and importing the
    layout engine would tie it to the files that engine reads."""
    return (
        value.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A").replace("##[", "%23%23%5B")
    )


def escape_property(value: str) -> str:
    """A property's value, escaped as ci_layout.py's escape_property does: the message
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


def jobs_of(path: Path) -> dict:
    """The jobs map of a workflow, as yaml.safe_load reads it, or none where it cannot be read."""
    try:
        document = yaml.safe_load(path.read_text()) or {}
    except (OSError, UnicodeDecodeError, yaml.YAMLError):
        return {}
    jobs = document.get("jobs") if isinstance(document, dict) else None
    return {str(k): (v if isinstance(v, dict) else {}) for k, v in jobs.items()} if isinstance(jobs, dict) else {}


def filter_steps(path: Path) -> list:
    """Every dorny/paths-filter step of a workflow, as (its job, the step)."""
    return [(job_id, step) for job_id, job in jobs_of(path).items() for step in job.get("steps") or []
            if isinstance(step, dict) and str(step.get("uses", "")).startswith("dorny/paths-filter")]


def is_carrier(job: dict) -> bool:
    """A job that only carries a split job's old check-run name: a sparse checkout of .github, one
    step, and the report to ci-ok, and nothing else."""
    steps = job.get("steps") or []
    if len(steps) != 3 or not all(isinstance(s, dict) for s in steps):
        return False
    checkout, _, report = steps
    sparse = str((checkout.get("with") or {}).get("sparse-checkout", "")).strip()
    return (str(checkout.get("uses", "")).startswith("actions/checkout@") and sparse == ".github"
            and report.get("uses") == REPORT_ACTION)


for path in targets:
    size = path.stat().st_size
    if size > GITHUB_CAP:
        problem(
            path,
            f"{path} is {size:,} bytes, above GitHub's own cap of {GITHUB_CAP:,} bytes (500 KB): "
            f"GitHub starts no run at all for a workflow file this size, and signals it only as a "
            f"run with no jobs in it. It is above this check's limit of {LIMIT:,} bytes as well. "
            f"{MOVE}",
        )
    elif size > LIMIT:
        problem(
            path,
            f"{path} is {size:,} bytes, above this check's limit of {LIMIT:,} bytes. GitHub starts "
            f"no run at all for a workflow file above its own cap of {GITHUB_CAP:,} bytes (500 "
            f"KB), and signals it only as a run with no jobs in it, so this limit keeps every file "
            f"far enough below the cap that no edit crosses it unseen. {MOVE}",
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

    called = unique_callees(path)
    if len(called) > CALL_LIMIT:
        problem(
            path,
            f"{path} calls {len(called)} unique reusable workflows, above this check's limit of "
            f"{CALL_LIMIT}: {', '.join(sorted(called))}. GitHub allows {GITHUB_CALL_CAP} from a "
            f"single workflow file, counting every tree of nested calls, and R9 holds a file to "
            f"{CALL_LIMIT}." + limits(path),
        )

    for job_id, step in filter_steps(path):
        given = str((step.get("with") or {}).get("filters", ""))
        inline = "\n" in given or ":" in given
        source = "its inline filters" if inline else given
        try:
            text = given if inline else Path(given).read_text()
        except OSError as error:
            problem(path, f"{path}: job {job_id}'s paths-filter step reads its filters from {given}, which "
                          f"cannot be read: {one_line(error)}." + limits(path))
            continue
        size = len(text.encode())
        if size >= FILTER_LIMIT:
            problem(
                path,
                f"{path}: job {job_id}'s paths-filter step reads {size:,} bytes of filter text from "
                f"{source}, at or above the limit of {FILTER_LIMIT:,}: a filter names a package area, "
                f"one area a job, and never a list of files." + limits(path),
            )

    if path == ROUTER:
        counted = [job_id for job_id, job in jobs_of(path).items() if not is_carrier(job)]
        if len(counted) > JOB_LIMIT:
            problem(
                path,
                f"{path} routes {len(counted)} jobs besides its carriers, above the limit of {JOB_LIMIT}: "
                f"{', '.join(counted)}. A carrier is a job whose steps are a sparse checkout of "
                f".github, one step and the report to ci-ok, and any other job counts; fold a new "
                f"job's steps into a leg of a job of the same runner and events." + limits(path),
            )

if problems:
    for annotation in problems:
        print(annotation)
    print(
        f"{len(problems)} problem(s) across the {len(targets)} workflow file(s) under {WORKFLOWS}",
        file=sys.stderr,
    )
    sys.exit(1)

print(
    f"all {len(targets)} workflow files under {WORKFLOWS} are within {LIMIT:,} bytes and GitHub's "
    f"{GITHUB_CAP:,}-byte cap, parse as YAML, repeat no mapping key after upper-casing, call at "
    f"most {CALL_LIMIT} unique reusable workflows and read less than {FILTER_LIMIT:,} bytes of "
    f"filter text, and {ROUTER} routes at most {JOB_LIMIT} jobs besides its carriers"
)
PY
