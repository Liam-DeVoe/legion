"""The rules about ci.yaml that stand on their own: what no `if:` of it may say, what every job's
`if:` must say, what ci-caches.yaml must save
for the entries ci.yaml restores and which packages the daemon fill job's race build must compile, which workspaces
ci_job_reads.py's WORKSPACES must name, and
that no other workflow of this repository runs on a merge group.
None of them
reads a filter, a gate or the closure of a go command, so none belongs in the filter-coverage
engine beside them; check-ci-filters.sh's header numbers them 10, 12, 11, 13, 14 and 16, and
ci_filter_coverage.main()
is what runs them, over the documents it already loaded where the rule reads only those - the
rerun-guard rule over ci.yaml alone, since the `if:` it reads is the caller job's; the cache
rule over ci.yaml and every workflow its jobs call, since a job's restore moves with its steps,
and over every other workflow of the directory, which it reads itself, for the key and paths,
since a release build's restore misses as silently; the push-condition rule over
ci.yaml and the called files that are ci.yaml's alone (ci_only_documents says why not the
others, and reads the directory to find them) - the workspace rule over the
root manifest and the WORKSPACES table main() hands it, and the race-fill rule over ci-caches.yaml
and the DAEMON_RACE_PACKAGES list and DAEMON_RACE_FILL_JOB job main() hands it the same way, since what this module imports is
the engine's names, never the data's, and the merge-group rule over the workflow directory, which
it reads itself.

It sits beside that engine (and workflow_paths.py, the path model) in the repository it checks,
and imports the engine's names for the two workflow files, the repository root, the checks
main's ruleset requires, the reader of a job's `uses:` and the three
helpers that read an `if:`, and the path model's reader of a workflow's `on:`. main()
imports this module rather than the other way round, so the one import of the engine here is of
a module Python has finished loading by then.
"""

import json
import os
import re
import subprocess
from collections import defaultdict
from dataclasses import dataclass
from functools import cache


from ci_filter_coverage import (
    CACHE_WORKFLOW,
    REPO,
    REQUIRED_CHECKS,
    WORKFLOW,
    RepositoryInputError,
    called_workflows,
    conditions,
    error,
    load_repository_yaml,
    read_repository_text,
    script_lines,
    squash,
    step_label,
    where_if,
)
from shell_commands import STDIN_SHELLS, WRAPPERS, command_of, pipelines_of
from workflow_paths import events


# ---------------------------------------------------------------- the conditions ci.yaml cannot hold
# ci.yaml runs on a pull request, a merge group and a workflow_dispatch, and on no push, so an if:
# testing for a push event, or comparing github.ref with a branch ref, holds its job or step back
# on every run there is. Either way round, as a GitHub expression may be written; its strings are
# single-quoted, a double-quoted one being no expression at all. github.ref_name, and github.ref
# in anything but such a comparison - startsWith against refs/heads/gh-readonly-queue/, the branch
# a merge_group run does carry, among them - are left alone.
def comparison(left: str, right: str) -> str:
    """The regex of a GitHub expression comparing `left` with `right`, either operand first."""
    return rf"{left}\s*[=!]=\s*{right}|{right}\s*[=!]=\s*{left}"


PUSH_CONDITION = re.compile("|".join((comparison(r"github\.event_name", "'push'"),
                                      comparison(r"github\.ref", r"'refs/heads/[^']*'"))))
# Where the work such a condition used to gate lives now.
PUSH_INSTEAD = (f"a cache that must be filled on a push to main belongs in {CACHE_WORKFLOW}, and a job that must "
                f"run at main's tip is started by the workflow_dispatch "
                f".github/workflows/main-bypass-check.yaml sends")
# The one `on:` a workflow this rule reads beyond ci.yaml may declare, and the directory and the
# two suffixes GitHub reads it in, which this rule and the merge-group rule at the foot of this
# module both walk.
WORKFLOW_CALL = "workflow_call"
WORKFLOW_DIR = ".github/workflows"
WORKFLOW_SUFFIXES = (".yaml", ".yml")


@cache
def directory_workflows() -> dict:
    """Every workflow of this repository, as {its repository-relative path: the parsed document},
    in path order. The three rules that walk the directory - the push-condition rule (through
    ci_only_documents), the cache rule and the merge-group rule - read this one answer: parsing
    each file for each of them is a second read of every workflow in the repository."""
    try:
        paths = sorted(p for p in (REPO / WORKFLOW_DIR).iterdir() if p.suffix in WORKFLOW_SUFFIXES)
    except Exception as exc:
        raise RepositoryInputError(WORKFLOW_DIR, exc) from exc
    return {f"{WORKFLOW_DIR}/{path.name}": load_repository_yaml(path) or {} for path in paths}


def ci_only_documents(documents: dict) -> dict:
    """Of the document set, the files whose every run is a run of ci.yaml: ci.yaml itself, and
    each workflow it calls that no other workflow of this repository calls and whose `on:`
    declares workflow_call and nothing else.

    The rule below is about conditions that are dead because the workflow they are in has no
    push trigger, and that reason carries to a called file only when ci.yaml is the only way
    into it. A reusable workflow runs inside its caller's run and under its caller's event, so a
    file two workflows call is live under each caller's events, and a file with a trigger of its
    own has runs no caller starts at all. Both cases are in this repository today:
    .github/workflows/docs-site.yaml is called by docs-pages.yaml as well, which runs on a push
    to main, and docs-site.yaml:189 carries `github.event_name == 'push'` for exactly that
    caller; .github/workflows/worker-image.yaml is called by release.yaml twice. The
    .github/workflows/ci-job-*.yaml files, which hold the steps of one ci.yaml job each, are the
    other case: nothing else calls them and they run on workflow_call alone, so a push condition
    in one of them is as dead as it would be in ci.yaml."""
    called_elsewhere: set = set()
    for path, document in directory_workflows().items():
        if path != WORKFLOW:
            called_elsewhere |= set(called_workflows(document))
    return {path: document for path, document in documents.items()
            if path == WORKFLOW
            or (path not in called_elsewhere and set(events(document)) == {WORKFLOW_CALL})}


def check_push_conditions(documents: dict, problems: list) -> None:
    """A job or a step of ci.yaml, or of a workflow only ci.yaml calls, gated on a push event,
    or on github.ref being a branch ref, is
    dead: the workflow has no push trigger, and on its three events github.ref is a pull request's
    merge ref or the merge queue's own branch, never the ref such a condition names. Dead code
    that reads as live is worse than none - a step held back by `if: github.event_name ==
    'push'`, or a job by `if: github.ref == 'refs/heads/main'`, runs in no run of this workflow
    while its text says it runs on main - so a later pull
    request may not bring one back. Read off the if: text, one comparison at a time: a committer
    who writes the same test as a function call is the review's to catch. Which files it reads,
    and why it is not every file ci.yaml calls, is ci_only_documents above; each refusal names
    the file the condition is in."""
    for path, document in ci_only_documents(documents).items():
        for job_id, job in (document.get("jobs") or {}).items():
            job = job or {}
            refs = conditions(job)
            for step, condition in refs:
                for found in dict.fromkeys(m.group(0) for m in PUSH_CONDITION.finditer(condition)):
                    where = where_if(job_id, step)
                    runs = (f"{WORKFLOW} runs" if path == WORKFLOW
                            else f"{path} runs only as part of {WORKFLOW}, which runs")
                    problems.append(error(path, f"{where} has `if: {squash(condition)}`, which tests "
                                                f"`{found}`: {runs} on a pull request, a merge group and a "
                                                f"workflow_dispatch, never on a push to main, so nothing that condition "
                                                f"gates ever runs; {PUSH_INSTEAD}"))


# ------------------------------------------------------- the re-run of a merge-group run
# A group is tested once, so every job of a re-run attempt of a merge-group run must skip. The
# term that does it is a job's own: `gh run rerun --failed` leaves out a `changes` that
# succeeded and hands the attempt that job's outputs again, so an `if:` testing only
# `needs.changes.outputs.<x>` runs the job in full on an attempt the group has no use for, and
# its report step posts a ci-ok on the dead group's head. A job that runs on merge_group alone
# (`cancel-started`) needs only the attempt half, so each job is asked for one term and never
# for both.
#
# A job named for a required check (REQUIRED_CHECKS) is asked for neither, and refused one,
# because a skip is no safe outcome for it: a job an `if:` holds back is skipped, and a skipped
# job's check run is a passing required check, so the term that saves every other job from a dead
# group would hand that group the check it merges on, concluded by nothing. Each of those jobs
# runs on every attempt and refuses an attempt above the first in a step, which fails and makes
# the check a failure; the comment above each one's `if:` in ci.yaml says so.
RERUN_GUARD = ("(github.event_name != 'merge_group' || github.run_attempt == '1')",
               re.compile(r"\(\s*github\.event_name\s*!=\s*'merge_group'\s*\|\|"
                          r"\s*github\.run_attempt\s*==\s*'1'\s*\)"))
MERGE_GROUP_ONLY = re.compile(r"github\.event_name\s*==\s*'merge_group'")
ATTEMPT_ONE = re.compile(r"github\.run_attempt\s*==\s*'1'")


def check_rerun_guard(document: dict, problems: list) -> None:
    """Every job of ci.yaml but the REQUIRED_CHECKS ones must hold, in its own `if:`, the term
    that skips it on a re-run attempt of a merge-group run: the disjunction RERUN_GUARD names,
    or, for a job whose `if:` admits merge_group alone, `github.run_attempt == '1'` by itself. A
    job named for a required check must hold neither, for the reason above: its skip is a passing
    required check. Read off the if: text, as check_push_conditions is: the same test written
    another way is the review's to catch.

    It reads ci.yaml alone, and no workflow ci.yaml calls: the `if:` this rule is about is the
    caller job's, which stays in ci.yaml whatever file holds that job's steps, and a called
    file's own jobs run or skip with the caller that started them."""
    wanted, found = RERUN_GUARD
    for job_id, job in (document.get("jobs") or {}).items():
        condition = str((job or {}).get("if", ""))
        if job_id in REQUIRED_CHECKS:
            if found.search(condition) or ATTEMPT_ONE.search(condition):
                problems.append(error(WORKFLOW, f"job {job_id} is named for a check main's ruleset "
                                                f"requires and its if: holds a term that skips it on a re-run attempt of a "
                                                f"merge-group run: a skipped job's check run is a passing required check, so that "
                                                f"attempt would merge on {job_id} concluded by nothing; drop the term and refuse "
                                                f"the attempt in a step of the job instead"))
            continue
        if found.search(condition):
            continue
        if MERGE_GROUP_ONLY.search(condition):
            if not ATTEMPT_ONE.search(condition):
                problems.append(error(WORKFLOW, f"job {job_id} runs on merge_group and its if: has no "
                                                f"`github.run_attempt == '1'`, so a re-run attempt of a merge-group run runs it "
                                                f"again on a group that is tested already"))
            continue
        problems.append(error(WORKFLOW, f"job {job_id}'s if: has no `{wanted}`, so a re-run attempt of a "
                                        f"merge-group run runs it again on a group that is tested already; `gh run rerun --failed` "
                                        f"does not re-run the `changes` that succeeded, so no job may lean on that job's skip for "
                                        f"this"))


# ---------------------------------------------------------------- the cache round trip
def cache_family(key: str) -> str:
    """The family a cache key names: its text up to the first expression - the Go version the
    entry is keyed on - without the separator that joins the two."""
    return key.split("${{")[0].strip().rstrip("-")


def path_lines(value) -> list:
    """A cache step's path:, as the ordered list of directories it names."""
    return [line.strip() for line in str(value).splitlines() if line.strip()]


def job_cache_steps(job: dict, action: str) -> list:
    """Every actions/cache/<action> step of one job, as (its family, its with:)."""
    return [(cache_family(str((step.get("with") or {}).get("key", ""))), step.get("with") or {})
            for step in ((job or {}).get("steps") or [])
            if str((step or {}).get("uses", "")).split("@")[0] == f"actions/cache/{action}"]


def cache_steps(document: dict, action: str) -> list:
    """Every actions/cache/<action> step of a workflow, as (its job, its family, its with:)."""
    return [(job_id, family, given)
            for job_id, job in (document.get("jobs") or {}).items()
            for family, given in job_cache_steps(job or {}, action)]


def restores_of(documents: dict) -> dict:
    """Every restore of the given workflows, as family -> [(where it is, its with:)]. Given the
    document set the engine loads - ci.yaml, and each local workflow one of its jobs calls:
    dispatch-browser.yaml's binaries, multiprocess, embedder and visual jobs restore go-envoy and
    go-envoy-test for ci.yaml's dispatch-browser job - it is every restore of ci.yaml's run;
    paired against ci.yaml alone, a save that only a called workflow restores would read as a fill
    nobody wants. Given the rest of the directory, it is the restores of the release builds and
    the other workflows. The set holds each file once however many of ci.yaml's jobs name it, so
    each restore is taken once."""
    restores = defaultdict(list)
    for path, document in documents.items():
        for job_id, family, given in cache_steps(document, "restore"):
            restores[family].append((f"job {job_id} of {path}", given))
    return restores


def check_cache_round_trip(documents: dict, problems: list) -> None:
    """ci-caches.yaml fills on a push to main the entries ci.yaml's run restores - its own jobs'
    restores, and those of the local workflows they call - so each family's save must carry
    exactly the key and the paths its restores name: a key or a path that drifts apart makes
    every restore miss, and nothing fails - each job just rebuilds from nothing what the fill
    already built. A family is the key's text up to its first expression, and it is what
    pairs a save with the restores to hold it to; the keys are compared as text and the paths as
    the ordered lists of lines they name. A family ci.yaml restores that this workflow does not
    save is no fault: docs-pages.yaml saves go-docs. The other way round is one, a family nothing
    restores being a fill nobody reads. Each restore here is a lookup, which must be
    `lookup-only: true` with no restore-keys: the question it asks is whether that exact key is
    already there, the entry it would download is read by nothing in this workflow, and a prefix
    hit would answer for an older key and skip the build that fills this one. A ci-caches.yaml
    that is not in the repository saves nothing, and so has nothing to hold.

    The restores are collected from every document of the set, ci.yaml and each workflow its
    jobs call, since a job that gives its steps to a called file takes its restore there with
    them and the entry it reads is the same one; each refusal names the file the restore is in.

    The key and the paths are held for every other workflow of the directory too: a release
    build (release.yaml's go-daemon, release-envoy-infra.yaml's go-envoy-infra), the nightly
    browser re-record (go-envoy) and this workflow's own lookups restore the entries it fills,
    and a restore there that drifts from the save misses as silently as one of ci.yaml's. Only
    the existence check above stays ci.yaml's: a fill is for ci.yaml's run, so a family only
    another workflow restores is still a fill ci.yaml does not read."""
    if not (REPO / CACHE_WORKFLOW).is_file():
        return
    caches = load_repository_yaml(REPO / CACHE_WORKFLOW) or {}
    restores = restores_of(documents)
    elsewhere = restores_of({path: document for path, document in directory_workflows().items()
                             if path not in documents})
    for job_id, family, given in cache_steps(caches, "save"):
        key, paths = str(given.get("key", "")).rstrip(), path_lines(given.get("path", ""))
        if family not in restores:
            problems.append(error(CACHE_WORKFLOW, f"job {job_id} saves cache family {family}, which no "
                                                  f"actions/cache/restore of {WORKFLOW} or of a workflow it calls "
                                                  f"reads; this workflow exists to fill what {WORKFLOW} restores, so a "
                                                  f"family nothing restores is a fill nobody reads: restore it there, "
                                                  f"or drop the save"))
            continue
        for restore_where, restored in restores[family] + elsewhere.get(family, []):
            restored_key = str(restored.get("key", "")).rstrip()
            restored_paths = path_lines(restored.get("path", ""))
            if key != restored_key:
                problems.append(error(CACHE_WORKFLOW, f"job {job_id} saves family {family} under key "
                                                      f"`{key}`, but {restore_where} restores it under "
                                                      f"`{restored_key}`: a restore whose key is not the save's "
                                                      f"misses every time, and nothing fails - the job rebuilds from nothing instead"))
            if paths != restored_paths:
                problems.append(error(CACHE_WORKFLOW, f"job {job_id} saves family {family} from "
                                                      f"{', '.join(paths) or '(no path)'}, but {restore_where} restores "
                                                      f"it into {', '.join(restored_paths) or '(no path)'}: the "
                                                      f"entry then holds other directories than the job reads, and the job rebuilds what "
                                                      f"the fill already built"))
    for job_id, family, given in cache_steps(caches, "restore"):
        if str(given.get("lookup-only", "")).strip().lower() != "true":
            problems.append(error(CACHE_WORKFLOW, f"job {job_id}'s actions/cache/restore of family "
                                                  f"{family} is not `lookup-only: true`: it asks only whether that exact key is already "
                                                  f"there, and the entry a restore downloads is read by no step of this workflow"))
        if given.get("restore-keys") is not None:
            problems.append(error(CACHE_WORKFLOW, f"job {job_id}'s actions/cache/restore of family "
                                                  f"{family} has restore-keys: a prefix hit would answer for an older key, and the build "
                                                  f"that fills this one would be skipped, leaving the key empty"))


# ------------------------------------------- the race build that fills the race lane's cache
# What makes a line of ci-caches.yaml the race build this rule holds: a `go test` running with
# -race, in the job that fills the daemon's family (RACE_JOB, the job id ci_job_reads.py's
# DAEMON_RACE_FILL_JOB names). Spelled as the engine's tag-run triggers are, so `-racy` is not the
# flag and a go vet or go build line is no such command, and read through script_lines, so a
# comment is no trigger. The packages are the words beginning `./`, which is how a go command
# names one and how ci_job_reads.py's DAEMON_RACE_PKGS spells that same list; the `-o /dev/null`
# of the build is no package. TABLE, the file the list is in, is the workspaces section's constant
# below: both rules name that one file, so there is one spelling of it here. A race build of
# another job fills another family's cache, for another lane, and is not this rule's: the
# envoy-infra job's fills envoy-infra-go's unit step.
RACE_TEST = re.compile(r"\bgo\s+(?:-C\s+\S+\s+)?test\b")
RACE_FLAG = re.compile(r"(?<![\w-])-race\b")
RACE_LIST = "DAEMON_RACE_PACKAGES"
RACE_JOB = "DAEMON_RACE_FILL_JOB"


def race_builds(document: dict, only_job: str) -> list:
    """Every race build of a workflow's job only_job: (the job, its line, the packages that line
    names)."""
    return [(job_id, line, [word for word in line.split() if word.startswith("./")])
            for job_id, job in (document.get("jobs") or {}).items() if job_id == only_job
            for step in ((job or {}).get("steps") or [])
            for line in script_lines(str((step or {}).get("run", "")))
            if RACE_TEST.search(line) and RACE_FLAG.search(line)]


def check_race_fill_packages(packages: list, job: str, problems: list) -> None:
    """The race build of ci-caches.yaml's job `job`, the one that fills the daemon's family,
    compiles exactly the packages the daemon's race lane runs. The workflow fills on a push to main
    the build cache ci.yaml's race step restores, so the two sides must name one set: a package the
    fill compiles and the lane does not leaves the entry holding a race binary no job ever
    restores, and a package the lane runs that the fill leaves out is linked from nothing in every
    run of that step. Neither fails anything - it is the silence check_cache_round_trip is about
    one level up, where the entry is found and holds the wrong objects - and the command has
    drifted twice for want of this rule: the fill kept ./internal/record/ after #2867 took it out
    of the lane. The packages are held as a set and in order, each spelled as a command spells it
    (./internal/daemon/), since a command that reads in another order than the list is one a
    reader must sort before comparing, and an unreadable pair is where the next drift hides. A job
    with no race build at all fails too, rather than passing quietly: this rule exists to hold that
    one command, so a tree without it is a lane nothing fills, not a rule with nothing to do; and
    so does a workflow with no such job. A race build of any other job fills another family, and
    this rule does not read it. A ci-caches.yaml that is not in the repository fills nothing, and
    so has nothing to hold.

    The list and the job are arguments, as check_workspace_table's table is: main() holds
    ci_job_reads.py already, and nothing here imports it - the engine's names are all this module
    takes."""
    if not (REPO / CACHE_WORKFLOW).is_file():
        return
    caches = load_repository_yaml(REPO / CACHE_WORKFLOW) or {}
    wanted = [f"./{package}/" for package in packages]
    if job not in (caches.get("jobs") or {}):
        problems.append(error(CACHE_WORKFLOW, f"this workflow has no job {job}, which {TABLE}'s {RACE_JOB} names "
                                              f"as the one that fills the cache {WORKFLOW}'s race step restores, so nothing "
                                              f"fills it and that step links {', '.join(wanted) or '(no package)'} from nothing "
                                              f"on every run; name the fill job there, or drop this rule with the lane it holds"))
        return
    builds = race_builds(caches, job)
    if not builds:
        problems.append(error(CACHE_WORKFLOW, f"no step of job {job} runs a `go test` with -race, so "
                                              f"nothing fills the build cache {WORKFLOW}'s race step restores and that step links "
                                              f"{', '.join(wanted) or '(no package)'} from nothing on every run; put the race build "
                                              f"back, or drop this rule with the lane it holds"))
        return
    for job_id, line, named in builds:
        where = f"job {job_id}'s race build"
        extra = [package for package in named if package not in wanted]
        missing = [package for package in wanted if package not in named]
        for package in extra:
            problems.append(error(CACHE_WORKFLOW, f"{where} names {package}, which {TABLE}'s {RACE_LIST} "
                                                  f"does not ({', '.join(named)} against {', '.join(wanted) or '(no package)'}): this "
                                                  f"build fills the cache {WORKFLOW}'s race step restores, so it compiles a race binary "
                                                  f"no job of {WORKFLOW} restores, and nothing fails; either the command follows the "
                                                  f"list, or the package joins {RACE_LIST}, the one place the lane is named - "
                                                  f"{WORKFLOW}'s two race steps follow it already"))
        for package in missing:
            problems.append(error(CACHE_WORKFLOW, f"{TABLE}'s {RACE_LIST} names {package}, which {where} "
                                                  f"does not ({', '.join(wanted)} against {', '.join(named) or '(no package)'}): the "
                                                  f"fill then misses a race binary {WORKFLOW}'s race step links from nothing on every "
                                                  f"run, and nothing fails; either the command follows the list, or the package leaves "
                                                  f"{RACE_LIST}, the one place the lane is named - {WORKFLOW}'s two race steps follow "
                                                  f"it already"))
        if named != wanted and not extra and not missing:
            problems.append(error(CACHE_WORKFLOW, f"{where} names {', '.join(named)}, where {TABLE}'s "
                                                  f"{RACE_LIST} has {', '.join(wanted)}: the same packages in another order, so the "
                                                  f"fill is right and only a reader who sorts both lists can see that it is; spell the "
                                                  f"command as the list reads, so the next change to either shows as a difference"))


def check_race_fill_scripts(fills: list, problems: list) -> None:
    """Each (fill job, script, its workdir, its compile call) of `fills` is a ci-caches.yaml job
    that fills a family whose race step runs a script: the job's steps in that workdir run the
    script's compile call, exactly as written, and no `go test` with -race of their own. The
    script holds the package list and -race the race step runs, so the fill and the step cannot
    name other packages or flags only while the fill reaches them through that script; a race
    build the fill wrote itself is the copy this rule exists to stop, and nothing fails when it
    drifts. A line that names the script with any other arguments fails too, naming the line, as
    does a fill job that is gone or never calls the script.

    The table is an argument, as check_race_fill_packages' list is: main() holds ci_job_reads.py,
    and nothing here imports it."""
    if not (REPO / CACHE_WORKFLOW).is_file():
        return
    jobs = (load_repository_yaml(REPO / CACHE_WORKFLOW) or {}).get("jobs") or {}
    for job_id, script, workdir, call in fills:
        name = script.rsplit("/", 1)[-1]
        if job_id not in jobs:
            problems.append(error(CACHE_WORKFLOW, f"this workflow has no job {job_id}, which {TABLE} names as the one "
                                                  f"that fills the race objects {script} compiles; name the fill job there, "
                                                  f"or drop the entry with the step it serves"))
            continue
        calls = 0
        for step in (jobs[job_id] or {}).get("steps") or []:
            if (step or {}).get("working-directory", "") != workdir:
                continue
            for line in script_lines(str((step or {}).get("run", ""))):
                if name in line:
                    if line == call:
                        calls += 1
                    else:
                        problems.append(error(CACHE_WORKFLOW, f"job {job_id} runs `{line}`, where the fill runs "
                                                              f"`{call}` and nothing else of {script}: the step that "
                                                              f"restores this cache runs the script's other mode, so any "
                                                              f"other call compiles objects the step does not link"))
                elif RACE_TEST.search(line) and RACE_FLAG.search(line):
                    problems.append(error(CACHE_WORKFLOW, f"job {job_id} runs `{line}`, a race build of its own: "
                                                          f"its race objects come from `{call}`, which takes the "
                                                          f"packages and flags from {script}, so a copy of them here "
                                                          f"can drift from the step and nothing fails"))
        if calls != 1:
            problems.append(error(CACHE_WORKFLOW, f"job {job_id} runs `{call}` {calls} times in {workdir}, where it "
                                                  f"fills the race objects {script} compiles once: with none the step "
                                                  f"links them from nothing on every run"))


# ---------------------------------------------------------------- the workspaces and the table
# The root manifest, whose workspaces ci_job_reads.py's WORKSPACES must name one for one, and the
# file that table is in. Both are named in the errors below, each in the one it is about.
MANIFEST = "package.json"
TABLE = ".github/scripts/ci_job_reads.py"


def check_workspace_table(workspaces: list, problems: list) -> None:
    """The root manifest's workspaces and ci_job_reads.py's WORKSPACES name the same packages.
    A frozen `bun install` reads every workspace manifest, so bun_job() declares each of
    WORKSPACES as a read of every Bun job, and the engine holds every filter gating such a job to
    all of them. A workspace the table leaves out is therefore a manifest no filter has to select,
    and a change to that package starts no job at all - which is how packages/broker-client came
    to have its typecheck and its tests run nowhere. The other way round is a read every Bun job
    declares of a manifest no install reads. The table is an argument because main() holds it
    already, and because nothing here imports ci_job_reads: the engine's names are all this
    module takes.

    The manifest is read as .github/scripts/check-bun-workspace-manifests.sh:101-109 reads it,
    at every shape, since the two checks are about the same list and a list one of them cannot
    read is not a list the other may pass over: a root manifest that is missing or is not valid
    JSON, and a `workspaces` that is not a list or holds anything but non-empty strings, are
    named rather than taken for an empty list, which would hold the table to nothing - and would
    do it exactly where every read WORKSPACES feeds is wrong anyway, since a tree with no root
    manifest runs no `bun install` the table could describe. An empty array is accepted, as that
    script accepts it: a root that declares no workspace is consistent with a table that names
    none, and a table that names one is caught below as an entry the manifest does not list. A
    trailing slash is stripped on both sides, since `packages/x/` and `packages/x` are one
    workspace to bun, and a rule that stripped it on one side alone would report the table as
    missing a workspace it names in the other spelling."""
    if not (REPO / MANIFEST).is_file():
        problems.append(error(MANIFEST, f"the root manifest is missing, so {TABLE}'s WORKSPACES is held to "
                                        f"nothing while every read it gives each Bun job names a manifest this tree does not have"))
        return
    try:
        manifest = json.loads(read_repository_text(REPO / MANIFEST))
    except json.JSONDecodeError as json_error:
        problems.append(error(MANIFEST, f"the root manifest is not valid JSON ({json_error.msg} at line "
                                        f"{json_error.lineno}), so its workspaces cannot be held to {TABLE}'s WORKSPACES"))
        return
    found = manifest.get("workspaces") if isinstance(manifest, dict) else None
    if not isinstance(found, list) or not all(isinstance(w, str) and w for w in found):
        problems.append(error(MANIFEST, "workspaces must be an array of workspace paths for "
                                        f"{TABLE}'s WORKSPACES to be held to it"))
        return
    declared = [workspace.removesuffix("/") for workspace in found]
    table = [workspace.removesuffix("/") for workspace in workspaces]
    for workspace in declared:
        if workspace in table:
            continue
        problems.append(error(MANIFEST, f"workspaces names {workspace}, which {TABLE}'s WORKSPACES does "
                                        f"not: a frozen `bun install` reads that manifest in every Bun job, so until the table "
                                        f"names it no filter has to select it, and a change to the package starts no job at all"))
    for workspace in table:
        if workspace in declared:
            continue
        problems.append(error(TABLE, f"WORKSPACES names {workspace}, which the root manifest's workspaces "
                                     f"does not: every Bun job then declares a read of {workspace}/package.json that no install "
                                     f"of this repository reads"))


# ------------------------------------------------- the one workflow a merge group runs
MERGE_GROUP = "merge_group"


def check_one_merge_group_workflow(problems: list) -> None:
    """ci.yaml is the only workflow of this repository that runs on a merge group.

    What holds it there is ci-ok-report.sh: it POSTs the failing ci-ok of a merge-group run the
    reporting job's own result decides, rather than waiting for the ci-ok job, so the queue
    learns the group is doomed while the rest of the run is still going. A POSTed check run
    joins the Actions App's first check suite at that commit rather than the posting run's
    (measured on pull request #2860 at 57182d42), and a ci-ok in a suite the queue does not read
    is a ci-ok the queue does not have: #2847 was refused with `Required status check "ci-ok" is
    expected` while a green ci-ok sat in PR Title's suite. That POST therefore lands where the
    queue reads it only because ci.yaml is
    the one workflow a merge group starts, which makes its suite the first one. A second
    merge_group workflow puts the early POST back in another suite, and nothing in ci.yaml would
    say so: the workflow that broke it is the new file. So the rule is about every other
    workflow, and names the file it found."""
    for name, document in directory_workflows().items():
        if name == WORKFLOW or MERGE_GROUP not in events(document):
            continue
        problems.append(error(name, f"{name} runs on {MERGE_GROUP}, which only {WORKFLOW} may: "
                                    f"ci-ok-report.sh POSTs the failing ci-ok of a merge-group run the reporting job's own "
                                    f"result decides, and a POSTed check run joins the Actions App's first check suite at that "
                                    f"commit rather than the posting run's, so a second {MERGE_GROUP} workflow can hold that "
                                    f"first suite and the queue never reads the POST - measured on pull request #2847, which "
                                    f"the queue refused to enqueue with `Required status check \"ci-ok\" is expected` while a "
                                    f"green ci-ok sat in another workflow's check suite. Run this work on pull_request, or "
                                    f"move it into {WORKFLOW}"))


# ------------------------------------------------- the network installs a required job runs
# The one way a job of ci.yaml, or of a workflow it calls, may run an install that waits on the
# network: through this script, which bounds each attempt, tries it a fixed number of times and
# fails by name (its header says how each kind's bound was measured). `--bounds` prints its table.
NETWORK_INSTALL = ".github/scripts/network-install.sh"
ACTIONS_PREFIX = "./.github/actions/"
# The variables network-install.sh reads in place of its bounds for its own tests alone; this rule
# asks it for the real ones with them unset.
NETWORK_INSTALL_SEAMS = ("NETWORK_INSTALL_BOUND_SECONDS", "NETWORK_INSTALL_KILL_AFTER_SECONDS",
                         "NETWORK_INSTALL_PAUSE_SECONDS")
# GitHub's limit for a job or a step that sets no timeout-minutes.
DEFAULT_MINUTES = 360
# apt's own verbs that read a mirror, and the options of apt and apt-get that take a value.
APT = {"apt", "apt-get", "aptitude"}
APT_VERBS = {"update", "install", "reinstall", "upgrade", "dist-upgrade", "full-upgrade", "build-dep", "source",
             "download"}
APT_VALUE_OPTIONS = {"-o", "-c", "-t", "--option", "--config-file", "--target-release"}
# Every other installer, by its command name: the verbs of it that install from the network, and
# the options of it that take a value of their own and so may stand before the verb (`npm --prefix
# dir ci`, `bun --cwd dir install`); a `+toolchain` word (`cargo +nightly install`) is an option.
PIP_VALUE_OPTIONS = {"--proxy", "--cert", "--client-cert", "--log", "--timeout", "--retries", "--python",
                     "--cache-dir", "--exists-action", "--trusted-host"}
INSTALLERS = {
    "pip": ({"install", "download"}, PIP_VALUE_OPTIONS),
    "pip3": ({"install", "download"}, PIP_VALUE_OPTIONS),
    "pipx": ({"install"}, set()),
    "npm": ({"install", "i", "ci", "add"}, {"--prefix", "-C", "--workspace", "-w", "--registry", "--cache",
                                            "--userconfig", "--loglevel"}),
    "pnpm": ({"install", "i", "add"}, {"-C", "--dir", "--filter", "-F", "--reporter", "--loglevel"}),
    "yarn": ({"install", "add"}, {"--cwd", "--cache-folder", "--modules-folder", "--registry"}),
    "bun": ({"install", "i", "add"}, {"--cwd", "-c", "--config", "--env-file", "-r", "--preload"}),
    "gem": ({"install"}, {"--config-file"}),
    "cargo": ({"install"}, {"-C", "--config", "-Z", "--color"}),
    "snap": ({"install"}, set()),
    "brew": ({"install"}, set()),
}
UV_VALUE_OPTIONS = {"--directory", "--project", "--cache-dir", "--python", "-p", "--config-file", "--color"}
DOCKER_VALUE_OPTIONS = {"--config", "-c", "--context", "-H", "--host", "-l", "--log-level", "--tlscacert",
                        "--tlscert", "--tlskey"}
# The runners of a package's own command, with their options that take a value, and the packages
# whose `install` downloads Playwright's browsers.
PACKAGE_RUNNERS = {"bunx": {"-p", "--package"}, "npx": {"-p", "--package"}, "pnpx": set()}
PLAYWRIGHT_PACKAGES = {"playwright", "@playwright/test", "playwright-core"}


def _from_verb(rest: list, value_options: set) -> list:
    """The words from the first that is no option on, each option's own value taken with it."""
    index = 0
    while index < len(rest):
        if rest[index] in value_options:
            index += 2
        elif rest[index].startswith(("-", "+")):
            index += 1
        else:
            return rest[index:]
    return []


def _playwright_install(rest: list, value_options: set) -> bool:
    """Whether a package runner's words (`playwright install chromium`, `-p @playwright/test
    playwright install`) run Playwright's browser download."""
    words = _from_verb(rest, value_options)
    package = words[0].rsplit("@", 1)[0] if words[:1] and "@" in words[0][1:] else (words[:1] or [""])[0]
    return package in PLAYWRIGHT_PACKAGES and _from_verb(words[1:], set())[:1] == ["install"]


def network_install(words: list) -> tuple[str, str] | None:
    """The install a command makes from the network, as the words that say so (`apt-get update`,
    `bun install`) and its kind, network-install.sh's word for it (`apt`, `bun`, `docker`,
    `playwright`; an install that script has no kind for is of a kind named after its program), or
    None where the command makes none."""
    name = words[0].rsplit("/", 1)[-1]
    rest = words[1:]
    if name in APT:
        verb = (_from_verb(rest, APT_VALUE_OPTIONS) or [""])[0]
        return (f"{name} {verb}", "apt") if verb in APT_VERBS else None
    if name == "wget":
        return ("wget", "wget")
    if name == "docker":
        command = _from_verb(rest, DOCKER_VALUE_OPTIONS)
        return ("docker pull", "docker") if command[:1] == ["pull"] or command[:2] == ["image", "pull"] else None
    if name in PACKAGE_RUNNERS and _playwright_install(rest, PACKAGE_RUNNERS[name]):
        return ("playwright install", "playwright")
    if name == "bun" and _from_verb(rest, INSTALLERS["bun"][1])[:1] == ["x"] and \
            _playwright_install(_from_verb(rest, INSTALLERS["bun"][1])[1:], PACKAGE_RUNNERS["bunx"]):
        return ("playwright install", "playwright")
    if name == "uv":
        command = _from_verb(rest, UV_VALUE_OPTIONS)
        return (f"uv {command[0]} install", "uv") if command[:2] in (["pip", "install"], ["tool", "install"]) else None
    if name.startswith("python") and rest[:2] == ["-m", "pip"] and "install" in rest[2:]:
        return (f"{name} -m pip install", "pip")
    if name == "go":
        fetch = go_fetch(rest)
        return (fetch, "go") if fetch else None
    if name in INSTALLERS:
        verbs, value_options = INSTALLERS[name]
        verb = (_from_verb(rest, value_options) or [""])[0]
        return (f"{name} {verb}", name) if verb in verbs else None
    return None


def go_fetch(rest: list) -> str:
    """The fetch a go command names, as words (`go mod download`, `go install <module>@<version>`),
    or "" where it names none. A step that names a fetch is an install; the modules a `go build`,
    `go test`, `go vet` or `go run` of a package of this repository downloads by itself are not,
    since no step names them (pull request #4434 moves those into one fetch step per job, which
    this rule then reads)."""
    if rest[:1] == ["-C"]:
        rest = rest[2:]
    words = [word for word in rest if not word.startswith("-")]
    if words[:2] == ["mod", "download"]:
        return "go mod download"
    if words[:1] == ["get"]:
        return "go get"
    if words[:1] in (["install"], ["run"]) and any("@" in word for word in words[1:]):
        return f"go {words[0]} <module>@<version>"
    return ""


@cache
def network_install_bounds() -> dict | str:
    """network-install.sh's table, read by running it with `--bounds` and its test seams unset:
    {kind: (bound s, attempts, s before SIGKILL, pause s)}, or why it could not be read."""
    script = REPO / NETWORK_INSTALL
    if not script.is_file():
        return f"{NETWORK_INSTALL} is not in the repository"
    env = {name: value for name, value in os.environ.items() if name not in NETWORK_INSTALL_SEAMS}
    result = subprocess.run(["bash", str(script), "--bounds"], capture_output=True, text=True, env=env,
                            timeout=60, check=False)
    try:
        if result.returncode != 0:
            raise ValueError(f"exit {result.returncode}")
        return {kind: tuple(int(field) for field in fields)
                for kind, *fields in (line.split() for line in result.stdout.splitlines())
                if len(fields) == 4}
    except ValueError as exc:
        return f"`{NETWORK_INSTALL} --bounds` did not print its table ({exc}): {squash(result.stderr)}"


def worst_case(bound: int, attempts: int, kill_after: int, pause: int) -> int:
    """The seconds a wrapped install can take before network-install.sh fails it by name: every
    attempt cut at its bound and killed after the wait for SIGTERM, with the pauses between them."""
    return attempts * (bound + kill_after) + (attempts - 1) * pause


def _minutes(holder: dict) -> int | None:
    """A job's or a step's timeout-minutes, GitHub's 360 where it sets none, or None where it is an
    expression this rule cannot read."""
    value = holder.get("timeout-minutes", DEFAULT_MINUTES)
    return value if isinstance(value, (int, float)) else None


@dataclass
class Site:
    """Where a command runs: the job and its workflow, the job's step it runs in, the file that
    holds the command (the workflow, a local action's action.yml or a repository script), the
    words that say how that file is reached from the job's step, the label of the step in that
    file, the directory a relative path is read from, and a local action's directory, which
    $GITHUB_ACTION_PATH names."""
    path: str
    job_id: str
    step: str
    where: str
    via: str = ""
    label: str = ""
    base: str = ""
    action: str = ""


def _action_steps(uses: str, seen: set) -> list:
    """The steps of the local composite action a `uses:` names, with each one's file, and those of
    the local actions they use in turn, each action once. An action this repository does not hold
    is check_table's to refuse, so it yields no steps here."""
    path = uses[2:].rstrip("/")
    if path in seen:
        return []
    seen.add(path)
    for name in ("action.yml", "action.yaml"):
        if (REPO / path / name).is_file():
            document = load_repository_yaml(REPO / path / name) or {}
            steps = []
            for step in (document.get("runs") or {}).get("steps") or []:
                steps.append((f"{path}/{name}", step))
                inner = str(step.get("uses", ""))
                if inner.startswith(ACTIONS_PREFIX):
                    steps += _action_steps(inner, seen)
            return steps
    return []


def _resolved(word: str, site: Site) -> str | None:
    """The repository-relative path a command word names, read from the site's directory: a path
    under $GITHUB_WORKSPACE from the root, and in a local action one under $GITHUB_ACTION_PATH from
    the action's directory. None for a word that holds any other variable or an expression, for an
    absolute path, and for one outside the repository."""
    base = site.base
    for variable in ("$GITHUB_ACTION_PATH", "${GITHUB_ACTION_PATH}"):
        if word.startswith(variable + "/") and site.action:
            word, base = word[len(variable) + 1:], site.action
    for variable in ("$GITHUB_WORKSPACE", "${GITHUB_WORKSPACE}"):
        if word.startswith(variable + "/"):
            word, base = word[len(variable) + 1:], ""
    if "$" in word or "EXPR" in word or word.startswith("/"):
        return None
    path = os.path.normpath(os.path.join(base, word))
    return None if path.startswith("..") else path


def _repository_script(word: str, site: Site) -> str | None:
    """The repository-relative path of the shell script a command word runs, where it names one
    this repository holds (a `.sh` file, or a file whose `#!` line names bash, sh, dash or zsh), read
    from the site's directory or else from the root; a bare name with no `/` is a program on PATH
    unless it ends in `.sh`."""
    if "/" not in word and not word.endswith(".sh"):
        return None
    for candidate in (_resolved(word, site), _resolved(word, Site("", "", "", "", action=site.action))):
        if candidate and (REPO / candidate).is_file():
            if candidate.endswith(".sh"):
                return candidate
            first = read_repository_text(REPO / candidate).split("\n", 1)[0]
            interpreter = first[2:].split() if first.startswith("#!") else []
            if interpreter[:1] and interpreter[0].endswith("/env"):
                interpreter = interpreter[1:]
            if interpreter and interpreter[0].rsplit("/", 1)[-1] in STDIN_SHELLS:
                return candidate
    return None


def _is_wrapper(word: str, site: Site) -> bool:
    """Whether a command word names this repository's NETWORK_INSTALL: resolved to that path, or,
    where a variable stands for the root (`"$root/.github/scripts/network-install.sh"`), ending
    in it."""
    resolved = _resolved(word, site)
    return resolved == NETWORK_INSTALL if resolved is not None else word.endswith("/" + NETWORK_INSTALL)


class NetworkInstalls:
    """Rule 21's reading of one job: each install its steps run, in the steps' own `run:`, in the
    local actions they use and in the repository scripts those run, refused unless wrapped, and
    each wrapped install's worst case held to the limits of its step and its job."""

    def __init__(self, path: str, job_id: str, job: dict, problems: list) -> None:
        self.path, self.job_id, self.job, self.problems = path, job_id, job, problems
        self.limit = _minutes(job)
        self.wrapped: list = []  # each wrapped install of the steps read so far, with its worst case

    def refuse(self, site: Site, message: str) -> None:
        self.problems.append(error(site.where, f"job {self.job_id} of {self.path}{site.via} {message}"))

    def read_step(self, step: dict) -> None:
        label = step_label(step)
        directory = step.get("working-directory") or (((self.job.get("defaults") or {}).get("run") or {})
                                                       .get("working-directory")) or ""
        directory = "" if "${{" in str(directory) else str(directory)
        own = Site(self.path, self.job_id, label, self.path, label=label, base=directory)
        located = [(own, step)]
        uses = str(step.get("uses", ""))
        if uses.startswith(ACTIONS_PREFIX):
            for where, inner in _action_steps(uses, set()):
                located.append((Site(self.path, self.job_id, label, where,
                                     via=f", in {where}, which step \"{label}\" uses,", label=step_label(inner),
                                     action=where.rsplit("/", 1)[0]), inner))
        wrapped: list = []
        for site, inner in located:
            if isinstance(inner.get("run"), str):
                self.read_run(site, inner["run"], wrapped, set())
        self.hold_to_limits(step, label, wrapped)

    def read_run(self, site: Site, run: str, wrapped: list, seen: set) -> None:
        """Each pipeline of a `run:` or a script (shell_commands.pipelines_of, looked through
        WRAPPERS): its installs refused, its wrapped installs kept in `wrapped`, and each
        repository script it runs read the same way, once."""
        found: list = []
        for pipeline in pipelines_of(run, WRAPPERS):
            for index, words in enumerate(pipeline):
                name = words[0].rsplit("/", 1)[-1]
                if name == NETWORK_INSTALL.rsplit("/", 1)[-1]:
                    self.read_wrapped(site, words, wrapped)
                    continue
                install = network_install(words)
                if install:
                    found.append(install[0])
                if name in ("curl", "wget") and any(later == [shell] for later in pipeline[index + 1:]
                                                    for shell in STDIN_SHELLS):
                    found.append(f"{name} piped into a shell")
                script = _repository_script(words[0], site)
                if script and script not in seen:
                    seen.add(script)
                    parent = f"step \"{site.label}\"" if site.where == site.path else site.where
                    self.read_run(Site(site.path, site.job_id, site.step, script,
                                       via=f"{site.via}, in {script}, which {parent} runs,".replace(",,", ","),
                                       label=site.label, base=site.base),
                                  read_repository_text(REPO / script), wrapped, seen)
        for install in dict.fromkeys(found):
            self.refuse(site, f"runs `{install}` in step \"{site.label}\": an install that waits on the network "
                              f"with no bound of its own, so a mirror or a registry that stops answering holds the "
                              f"job until its limit and fails it for a cause outside the change under test. Run "
                              f"it as `{NETWORK_INSTALL} <kind> -- <command>` (from a composite action, "
                              f"\"$GITHUB_ACTION_PATH/../../scripts/network-install.sh\"), which bounds each "
                              f"attempt by that kind's measured bound, tries it three times and fails by name; a "
                              f"kind it lacks is a line of its table, with a bound measured from that install's "
                              f"own runs. A tool every run needs belongs in the runner image or in a cached, "
                              f"pinned download instead")

    def read_wrapped(self, site: Site, words: list, wrapped: list) -> None:
        """A call of a script named network-install.sh: the repository's own, as `<kind> --
        <command>`, with a kind its table bounds, wrapping an install of that kind."""
        call = " ".join(words)
        if not _is_wrapper(words[0], site):
            self.refuse(site, f"runs `{call}` in step \"{site.label}\": a network-install.sh that is not "
                              f"this repository's {NETWORK_INSTALL}, whose bounds are the measured ones")
            return
        if len(words) < 4 or words[2] != "--":
            self.refuse(site, f"runs `{call}` in step \"{site.label}\": {NETWORK_INSTALL} takes "
                              f"`<kind> -- <command>`, and refuses any other call when it runs")
            return
        kind, bounds = words[1], network_install_bounds()
        if isinstance(bounds, str):
            self.refuse(site, f"runs `{call}` in step \"{site.label}\", and rule 21 cannot read the bounds that "
                              f"call waits under: {bounds}")
            return
        if kind not in bounds:
            self.refuse(site, f"runs `{call}` in step \"{site.label}\": {NETWORK_INSTALL} bounds no kind "
                              f"'{kind}' (its kinds: {', '.join(sorted(bounds))}), and refuses it when it runs. A "
                              f"new kind is a line of its table, with a bound measured from that install's own runs")
            return
        for pipeline in command_of(list(words[3:]), WRAPPERS):
            for command in pipeline:
                install = network_install(command)
                if install and install[1] != kind:
                    self.refuse(site, f"runs `{install[0]}` through {NETWORK_INSTALL}'s kind '{kind}' in step "
                                      f"\"{site.label}\": that bound was measured on {kind}'s installs, and "
                                      f"`{install[0]}` is of kind '{install[1]}'")
        wrapped.append((" ".join(words[3:]), kind, worst_case(*bounds[kind])))

    def hold_to_limits(self, step: dict, label: str, wrapped: list) -> None:
        """Every wrapped install of the step must be able to fail by name inside the step's own
        limit, and, counted with those of the steps before it, inside the job's: a limit that ends
        first ends the job in GitHub's cancellation, which names nothing."""
        if not wrapped:
            return
        self.wrapped += wrapped

        def why(installs: list) -> str:
            each = "; ".join(f"`{command}` ({kind}) {seconds} s" for command, kind, seconds in installs)
            return (f"the wrapped installs can take before the last of them fails by name, each through "
                    f"{NETWORK_INSTALL}'s attempts, each cut at its bound and killed after the wait for SIGTERM, "
                    f"with the pauses between them ({each}). A registry that stalls would end it in GitHub's "
                    f"cancellation, which names nothing, before {NETWORK_INSTALL} names the install. Give it a "
                    f"limit derived from what it runs, or fewer tries, with the reason in a comment")

        took = sum(seconds for _, _, seconds in wrapped)
        step_limit = _minutes(step) if "timeout-minutes" in step else None
        if step_limit is not None and took > step_limit * 60:
            self.problems.append(error(self.path, f"job {self.job_id} of {self.path}: step \"{label}\" has "
                                                  f"timeout-minutes {step_limit}, {step_limit * 60} s, and {took} s is "
                                                  f"what {why(wrapped)}"))
        spent = sum(seconds for _, _, seconds in self.wrapped)
        if self.limit is not None and spent > self.limit * 60:
            self.problems.append(error(self.path, f"job {self.job_id} of {self.path} has timeout-minutes "
                                                  f"{self.limit}, {self.limit * 60} s, and {spent} s is what, up "
                                                  f"to step \"{label}\", {why(self.wrapped)}"))
            self.limit = None  # the job is named once


def check_network_installs(documents: dict, problems: list) -> None:
    """No job of ci.yaml, nor of a workflow it calls, nor a local composite action one of them uses,
    nor a shell script of this repository one of those runs, runs an install that waits on the
    network except through NETWORK_INSTALL, and every install it runs that way can fail by name
    inside the limits of its step and its job. Every such job is on the path of ci-ok, the check
    main's ruleset requires, or is pr-title, the other one, so an install there that waits without a
    bound fails a pull request for a cause outside its change: on 2026-11-20 run 44665181416's
    daemon-go job waited in `sudo apt-get update` from 03:05:59Z until the job's 70-minute limit, on
    a mirror that had sent an answer's headers and then all but stopped, which apt's own timeout, a
    wait for the next byte, never ends.

    What it reads: each pipeline of each step's `run:` (shell_commands.pipelines_of, looked through
    its WRAPPERS: sudo, timeout, nice, ionice, nohup, setsid, stdbuf, xargs and command, and the
    `NAME=value` words, env, interpreters and `-c` strings each of them may put before the command,
    until none is left); the steps of each local action a step uses, and of the actions those use;
    and each shell script of this repository one of those commands runs (`bash scripts/x.sh`,
    `./scripts/x.sh`, `. "$GITHUB_ACTION_PATH/../../scripts/x.sh"`), and the scripts those run, each
    once. A command word that holds any other variable or an expression (`"$root/x.sh"`), and a
    program that is no shell script (a Python, TypeScript or Go program), are out of its reach.

    What it refuses: apt, apt-get and aptitude with a verb that reads a mirror; pip, pip3, pipx,
    `uv pip`, `uv tool` and `python -m pip` installing; npm, pnpm, yarn and bun installing or
    adding; gem, cargo, snap and brew installing; `docker pull`; Playwright's `install`, run by
    bunx, npx, pnpx or `bun x`; a go command that names a fetch (`go mod download`, `go get`, `go
    install` or `go run` of a <module>@<version>); any wget; and a curl or a wget piped into a shell.
    timeout(1) alone is refused too: it bounds the wait, but neither tries it again nor names what
    failed. A call of NETWORK_INSTALL passes when it is the repository's own script, names a kind
    its table bounds (`--bounds`) and wraps no install of another kind, whose bound was not measured
    on it.

    What it leaves alone, each for a reason: a `uses:` of an action this repository does not hold
    (actions/setup-go, actions/cache), whose fetches are that action's own; `mise install`, whose
    client ends a stalled wait by itself: mise 2026.8.12 bounds each request, its body included, by
    `http_timeout`'s 30 s, and against a server that sent the answer's headers and then one byte
    every 5 s, `mise install` of the jj install-jj installs ended by itself after four attempts, in
    125 s; and curl outside a pipe into a shell, which a pinned download makes through
    .github/scripts/ci-tools.sh's ci_tools_curl, bounded by --max-time and --retry-max-time. The
    modules a `go build`, `go test`, `go vet` or `go run` of this repository's own packages
    downloads by itself are no step's install: pull request #4434 moves them into one fetch step per
    job, which this rule then reads, so whichever of that pull request and this rule lands second
    wraps that step in NETWORK_INSTALL."""
    for path, document in documents.items():
        for job_id, job in ((document or {}).get("jobs") or {}).items():
            reader = NetworkInstalls(path, job_id, job or {}, problems)
            for step in (job or {}).get("steps") or []:
                reader.read_step(step)
