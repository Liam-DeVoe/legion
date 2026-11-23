"""The rules about ci.yaml that read the workflow directory: what no `if:` of it, or of an action
only it runs, may say (rule 10), what every job's `if:` must say (rule 12), what ci-caches.yaml
must save for the entries ci.yaml restores, in its own steps or inside the actions they run
(rule 11), which packages the daemon fill job's race build must compile and how a fill reaches a
race script (rule 16), and that no other workflow of this repository runs on a merge group (rule
14). check-ci-layout.sh's header numbers them, and ci_layout.main() runs them over the documents
it loaded: the rerun-guard rule over ci.yaml alone, since the `if:` it reads is the caller job's;
the cache rule over ci.yaml, every workflow its jobs call and every action those run, and over
every other workflow of the directory for the key and paths, since a release build's restore
misses as silently; the push-condition rule over ci.yaml, the called files that are ci.yaml's
alone and the actions no other workflow runs (ci_only_documents and ci_only_actions say why not
the others); the race-fill rule over ci-caches.yaml and the race step ci_pins.py names, whose
packages main() reads; and the merge-group rule over the workflow directory.

It imports ci_layout's names for the two workflow files, the repository root, the checks main's
ruleset requires, the readers of a job's `uses:` and of an action's steps, and the helpers that
read an `if:`. ci_layout.main() imports this module rather than the other way round, so the one
import of the engine here is of a module Python has finished loading by then.
"""

import re
from collections import defaultdict
from functools import cache


from ci_layout import (
    CACHE_WORKFLOW,
    REPO,
    REQUIRED_CHECKS,
    WORKFLOW,
    RepositoryInputError,
    action_steps,
    actions_of_job,
    called_workflows,
    conditions,
    error,
    load_action,
    load_repository_yaml,
    script_lines,
    squash,
    step_label,
    where_if,
)
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
    to main, and docs-site.yaml's cache save step carries `github.event_name == 'push'` for that
    caller; .github/workflows/worker-image.yaml is called by release.yaml twice."""
    called_elsewhere: set = set()
    for path, document in directory_workflows().items():
        if path != WORKFLOW:
            called_elsewhere |= set(called_workflows(document))
    return {path: document for path, document in documents.items()
            if path == WORKFLOW
            or (path not in called_elsewhere and set(events(document)) == {WORKFLOW_CALL})}


def ci_only_actions(documents: dict) -> list:
    """The local composite actions whose every run is a run of ci.yaml: each that a job of
    ci.yaml or of a file ci_only_documents keeps reaches, directly or through another action, and
    that no job of any other workflow reaches. An action ci-caches.yaml also runs - the
    architecture and spec checks, which save their caches on a push to main - is live on a push,
    so a push condition in it is no dead code."""
    ci_files = ci_only_documents(documents)
    elsewhere: set = set()
    for path, document in {**directory_workflows(), **documents}.items():
        if path in ci_files:
            continue
        for job in (document.get("jobs") or {}).values():
            elsewhere |= set(actions_of_job(job or {}))
    found: list = []
    for document in ci_files.values():
        for job in (document.get("jobs") or {}).values():
            found += [a for a in actions_of_job(job or {}) if a not in elsewhere and a not in found]
    return found


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
    for action in ci_only_actions(documents):
        for step in ((load_action(action).get("runs") or {}).get("steps") or []):
            condition = str((step or {}).get("if", ""))
            for found in dict.fromkeys(m.group(0) for m in PUSH_CONDITION.finditer(condition)):
                problems.append(error(action, f"step \"{step_label(step or {})}\" of {action} has `if: "
                                              f"{squash(condition)}`, which tests `{found}`: no workflow but {WORKFLOW} "
                                              f"runs this action, and {WORKFLOW} runs on a pull request, a merge group "
                                              f"and a workflow_dispatch, never on a push to main, so nothing that "
                                              f"condition gates ever runs; {PUSH_INSTEAD}"))


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
# the check a failure; .github/ci-notes/ci.md#why-pr-title-refuses-a-re-run-attempt-in-a-step-of-its-own
# and .github/ci-notes/ci.md#why-a-re-run-attempt-of-a-merge-group-run-is-refused-in-a-step say so.
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


# The key an action's save takes from its own restore step, which cannot drift from it.
PRIMARY_KEY = re.compile(r"\$\{\{\s*steps\.([A-Za-z0-9_-]+)\.outputs\.cache-primary-key\s*\}\}")


def job_cache_steps(job: dict, action: str) -> list:
    """Every actions/cache/<action> step of one job, its own and those of the local actions its
    steps run, as (its family, its with:, the action file it is in or '')."""
    found = []
    for step in (job or {}).get("steps") or []:
        found.append(("", step or {}))
        found += action_steps(str((step or {}).get("uses", "")))
    return [(cache_family(str((step.get("with") or {}).get("key", ""))), step.get("with") or {}, holder)
            for holder, step in found
            if str((step or {}).get("uses", "")).split("@")[0] == f"actions/cache/{action}"]


def cache_steps(document: dict, action: str) -> list:
    """Every actions/cache/<action> step of a workflow, as (its job, its family, its with:, the
    action file it is in or '')."""
    return [(job_id, family, given, holder)
            for job_id, job in (document.get("jobs") or {}).items()
            for family, given, holder in job_cache_steps(job or {}, action)]


def restores_of(documents: dict) -> dict:
    """Every restore of the given workflows, as family -> [(where it is, its with:)]. Given the
    document set the engine loads - ci.yaml, and each local workflow one of its jobs calls:
    dispatch-browser.yaml's binaries, multiprocess, embedder and visual jobs restore go-envoy and
    go-envoy-test for ci.yaml's dispatch-browser job - it is every restore of ci.yaml's run;
    paired against ci.yaml alone, a save that only a called workflow restores would read as a fill
    nobody wants. Given the rest of the directory, it is the restores of the release builds and
    the other workflows. The set holds each file once however many of ci.yaml's jobs name it, so
    each restore is taken once. A restore inside a local action a job runs is that job's, named
    with the action file it is in."""
    restores = defaultdict(list)
    for path, document in documents.items():
        for job_id, family, given, holder in cache_steps(document, "restore"):
            restores[family].append((f"job {job_id} of {path}" + (f" (in {holder})" if holder else ""), given))
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
    jobs call, and from every local action their jobs run, since a job that gives its steps to an
    action takes its restore there with them and the entry it reads is the same one; each refusal
    names the file the restore is in. A save inside an action ci-caches.yaml runs (the
    architecture and spec checks') is held the same way as one of its own steps.

    The key and the paths are held for every other workflow of the directory too: a release
    build (release.yaml's go-daemon, release-envoy-infra.yaml's go-envoy-infra) and this
    workflow's own lookups restore the entries it fills,
    and a restore there that drifts from the save misses as silently as one of ci.yaml's. Only
    the existence check above stays ci.yaml's: a fill is for ci.yaml's run, so a family only
    another workflow restores is still a fill ci.yaml does not read."""
    if not (REPO / CACHE_WORKFLOW).is_file():
        return
    caches = load_repository_yaml(REPO / CACHE_WORKFLOW) or {}
    restores = restores_of(documents)
    elsewhere = restores_of({path: document for path, document in directory_workflows().items()
                             if path not in documents})
    for job_id, family, given, holder in cache_steps(caches, "save"):
        key, paths = str(given.get("key", "")).rstrip(), path_lines(given.get("path", ""))
        mirrored = PRIMARY_KEY.fullmatch(key)
        if holder and mirrored:
            # A save inside an action under the key its own restore computed (the architecture
            # and spec checks): the key cannot drift from that restore, and the paths must not.
            own = [s for s in ((load_action(holder).get("runs") or {}).get("steps") or [])
                   if (s or {}).get("id") == mirrored.group(1)]
            if not own or path_lines((own[0].get("with") or {}).get("path", "")) != paths:
                problems.append(error(holder, f"the save of {holder}, which job {job_id} runs, saves "
                                              f"{', '.join(paths) or '(no path)'} under the key of step "
                                              f"{mirrored.group(1)}, which restores other paths or is not in that "
                                              f"action: the entry then holds other directories than the restore reads"))
            continue
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
    for job_id, family, given, holder in cache_steps(caches, "restore"):
        if holder:
            continue  # an action's restore is that action's read, held to the save above
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
# -race, in the job that fills the daemon's family (ci_pins.DAEMON_RACE_FILL_JOB). A go vet or go
# build line is no such command, and `-racy` is not the flag; a comment is no trigger. The
# packages are the words beginning `./`, which is how a go command names one and how the race
# step's --packages list spells the same list; the `-o /dev/null` of the build is no package. A
# race build of another job fills another family's cache, for another lane, and is not this
# rule's: the envoy-infra job's fills envoy-infra-go's unit step. PINS is the file that names the
# step and the job, which each refusal names.
RACE_TEST = re.compile(r"\bgo\s+(?:-C\s+\S+\s+)?test\b")
RACE_FLAG = re.compile(r"(?<![\w-])-race\b")
PINS = ".github/scripts/ci_pins.py"


def race_builds(document: dict, only_job: str) -> list:
    """Every race build of a workflow's job only_job: (the job, its line, the packages that line
    names)."""
    return [(job_id, line, [word for word in line.split() if word.startswith("./")])
            for job_id, job in (document.get("jobs") or {}).items() if job_id == only_job
            for step in ((job or {}).get("steps") or [])
            for line in script_lines(str((step or {}).get("run", "")))
            if RACE_TEST.search(line) and RACE_FLAG.search(line)]


def check_race_fill_packages(packages: list, step: tuple, job: str, problems: list) -> None:
    """The race build of ci-caches.yaml's job `job`, the one that fills the daemon's family,
    compiles exactly the packages the daemon's race lane runs. The workflow fills on a push to main
    the build cache ci.yaml's race step restores, so the two sides must name one set: a package the
    fill compiles and the lane does not leaves the entry holding a race binary no job ever
    restores, and a package the lane runs that the fill leaves out is linked from nothing in every
    run of that step. Neither fails anything - it is the silence check_cache_round_trip is about
    one level up, where the entry is found and holds the wrong objects - and the command has
    drifted twice for want of this rule: the fill kept ./internal/record/ after #2867 took it out
    of the lane. The packages are held as a set and in order, each spelled as the step spells it
    (./internal/daemon/), since a command that reads in another order than the step is one a
    reader must sort before comparing, and an unreadable pair is where the next drift hides. A job
    with no race build at all fails too, rather than passing quietly: this rule exists to hold that
    one command, so a tree without it is a lane nothing fills, not a rule with nothing to do; and
    so does a workflow with no such job. A race build of any other job fills another family, and
    this rule does not read it. A ci-caches.yaml that is not in the repository fills nothing, and
    so has nothing to hold.

    `packages` are the race step's own --packages, which main() reads off the step `step` (its
    action file and name, ci_pins.DAEMON_RACE_STEP) names, and `job` is the fill job."""
    if not (REPO / CACHE_WORKFLOW).is_file():
        return
    caches = load_repository_yaml(REPO / CACHE_WORKFLOW) or {}
    wanted = list(packages)
    lane = f"the race step \"{step[1]}\" of {step[0]}"
    if job not in (caches.get("jobs") or {}):
        problems.append(error(CACHE_WORKFLOW, f"this workflow has no job {job}, which {PINS} names as the one that "
                                              f"fills the cache {lane} restores, so nothing fills it and that step links "
                                              f"{', '.join(wanted) or '(no package)'} from nothing on every run"))
        return
    builds = race_builds(caches, job)
    if not builds:
        problems.append(error(CACHE_WORKFLOW, f"no step of job {job} runs a `go test` with -race, so nothing fills "
                                              f"the build cache {lane} restores and that step links "
                                              f"{', '.join(wanted) or '(no package)'} from nothing on every run"))
        return
    for job_id, line, named in builds:
        where = f"job {job_id}'s race build"
        extra = [package for package in named if package not in wanted]
        missing = [package for package in wanted if package not in named]
        for package in extra:
            problems.append(error(CACHE_WORKFLOW, f"{where} names {package}, which {lane} does not "
                                                  f"({', '.join(named)} against {', '.join(wanted) or '(no package)'}): "
                                                  f"the fill compiles a race binary no job restores, and nothing fails; "
                                                  f"the fill follows the step"))
        for package in missing:
            problems.append(error(CACHE_WORKFLOW, f"{lane} names {package}, which {where} does not "
                                                  f"({', '.join(wanted)} against {', '.join(named) or '(no package)'}): "
                                                  f"the fill misses a race binary that step links from nothing on every "
                                                  f"run, and nothing fails; the fill follows the step"))
        if named != wanted and not extra and not missing:
            problems.append(error(CACHE_WORKFLOW, f"{where} names {', '.join(named)}, where {lane} has "
                                                  f"{', '.join(wanted)}: the same packages in another order; spell the "
                                                  f"command as the step reads, so the next change to either shows"))


def check_race_fill_scripts(fills: list, problems: list) -> None:
    """Each (fill job, script, its workdir, its compile call) of `fills` is a ci-caches.yaml job
    that fills a family whose race step runs a script: the job's steps in that workdir run the
    script's compile call, exactly as written, and no `go test` with -race of their own. The
    script holds the package list and -race the race step runs, so the fill and the step cannot
    name other packages or flags only while the fill reaches them through that script; a race
    build the fill wrote itself is the copy this rule exists to stop, and nothing fails when it
    drifts. A line that names the script with any other arguments fails too, naming the line, as
    does a fill job that is gone or never calls the script.

    The table is an argument: main() reads ci_pins.RACE_FILL_SCRIPTS, and nothing here imports
    ci_pins."""
    if not (REPO / CACHE_WORKFLOW).is_file():
        return
    jobs = (load_repository_yaml(REPO / CACHE_WORKFLOW) or {}).get("jobs") or {}
    for job_id, script, workdir, call in fills:
        name = script.rsplit("/", 1)[-1]
        if job_id not in jobs:
            problems.append(error(CACHE_WORKFLOW, f"this workflow has no job {job_id}, which {PINS} names as the one "
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
