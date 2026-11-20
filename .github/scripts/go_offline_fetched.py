"""Rule 7 of check-go-offline.sh: each go command a step of a required Go job runs in its own run
text loads only packages a fetch before it, in the same job, names.

A required Go job runs with GOPROXY=off (rule 4), so its go steps read the module cache its restore
and its fetch step filled, and nothing else. The fetch step runs the job's fetch file
(.github/go-fetch/<job>.txt), and the fill of the job's cache family runs the same file (rule 22 of
check-ci-filters.sh). A go step whose packages no line of that file covers reads modules only an
older entry restored by its key's prefix happens to hold: it passes there, and fails, naming the
module, on the run that restores the entry its own fill saved, which may be another change's. This
holds each such step to its fetch before CI runs it.

What a go command needs: the packages it names (a pattern ending in /... is every package under its
directory in the same Go module, as go reads it; no package is the directory itself, and a .go file
is its directory), in the directory it runs in (the step's working-directory, else the job's or the
workflow's defaults.run, then each `cd` of the step before it, then its -C), with its tests when it
is go test or go vet, under its -tags and its GOOS, GOARCH, CGO_ENABLED and GOWORK (set on its own
line, or in the step's, the job's or the workflow's env). The subcommands that load packages:
build, install, run (its first package alone), list, test and vet.

What a fetch names: each `[GOOS=..] [GOARCH=..] [CGO_ENABLED=..] [GOWORK=off] deps DIR [-test]
[-tags T] PACKAGE...` line of each `go-fetch.sh file FILE` a command before it in the job runs (DIR
and FILE read from the repository's root, as go-fetch.sh reads them), and each `go-fetch.sh deps DIR
...` such a command runs (DIR read from its own directory).

A command is held when one such line names a pattern holding each of its packages, with -test where
the command needs its tests, the same set of tags and the same four settings. A command no line
holds is refused at its workflow, naming the job, the step, the command and the deps line that would
fetch it, unless it says why it needs none: a `go-offline: <why>` comment on its own line of the run
text or within the 5 lines above it, or one its step carries as rule 2 reads it (in its run text,
or in a comment within the 5 lines above its name). A command whose packages, tags, directory or
settings are not literal ($pkg, "$flag", GOARCH=$arch, cd "$dir"), or that names an import path
rather than a path from its directory, or is go list -m (which reads the module graph, not
packages), is refused too, unless such a comment (on its line, above it, or above its step's name)
says which line of the fetch file covers it: no fetch can be held to what this cannot
read. A command whose own GOPROXY (its line's, its step's) is not off goes online on purpose, which
rule 4 holds; it is not read here.

Its limit: a go command inside a script or a function the step runs is not read (rule 2 holds an
offline build there, and rule 1 each go command of the script).

Imported with PYTHONPATH naming the directory this file lives in. Its test is
check-go-offline.test.sh, with the cases it sources.
"""
import os
import posixpath
from pathlib import Path
from typing import NamedTuple

import yaml

import source_scan as scan
from go_offline_sites import REASON, go_subcommand, goish_text
from go_offline_walk import FLAGS_WITH_VALUES, step_reasoned
from source_shell import shell_command_word, shell_script

LOADS = {"build", "install", "run", "list", "test", "vet"}
SETTINGS = ("GOOS", "GOARCH", "CGO_ENABLED", "GOWORK")
UNREAD = "\x00"


class Load(NamedTuple):
    """What a go command loads, or a deps line fetches: its packages as repository paths (a pattern
    ends in /...), whether with their tests, its tags, and its four settings (None where unset)."""
    packages: tuple
    tests: bool
    tags: frozenset
    settings: tuple


def module_root(directory):
    """The nearest directory at or above `directory` that holds a go.mod, or None."""
    here = directory
    while True:
        if os.path.isfile(os.path.join(here or ".", "go.mod")):
            return here
        if here in ("", "."):
            return None
        here = posixpath.dirname(here)


def place(directory, arg):
    """A package argument as a repository path: '.' for the root; a /... pattern kept as one."""
    wild = arg == "..." or arg.endswith("/...")
    base = arg[:-3] if wild else arg
    if base.endswith(".go"):
        base = posixpath.dirname(base) or "."
    path = posixpath.normpath(posixpath.join(directory, base or "."))
    return f"{path}/..." if wild else path


def holds(pattern, package):
    """Whether a fetched pattern holds a needed package (or package pattern): the same path, or a
    path under a /... pattern's directory in the same Go module, since go's /... ends at a nested
    module."""
    if pattern == package:
        return True
    if not pattern.endswith("/..."):
        return False
    top = pattern[:-4]
    path = package[:-4] if package.endswith("/...") else package
    if not (path == top or top == "." or path.startswith(top + "/")):
        return False
    return module_root(path) == module_root(top)


def relative(pattern, module):
    """A repository path (or /... pattern) as a deps line names it, from its module's directory."""
    wild = pattern.endswith("/...")
    rel = posixpath.relpath(pattern.removesuffix("/..."), module)
    shown = "." if rel == "." else "./" + rel
    return f"{shown}/..." if wild else shown


def covered(need, lines):
    return any(line.tests >= need.tests and line.tags == need.tags and line.settings == need.settings
               and all(any(holds(p, n) for p in line.packages) for n in need.packages)
               for line in lines)


def tag_set(value):
    return frozenset(t for t in value.replace(" ", ",").split(",") if t)


def settings_of(env, assigns):
    """The four settings, from the step's, job's and workflow's env (merged into env) and the
    command's own NAME=value prefix (assigns: name -> value, None where not literal)."""
    out = []
    for name in SETTINGS:
        value = assigns.get(name, env.get(name))
        out.append(UNREAD if value is not None and UNREAD in str(value) else value)
    return tuple(None if v is None else str(v) for v in out)


def deps_line(directory, words, settings):
    """A Load of a deps line's words after `deps DIR`, or None when one is not literal."""
    tests, tags, packages, skip = False, frozenset(), [], False
    for k, word in enumerate(words):
        if UNREAD in word:
            return None
        if skip:
            skip = False
        elif word == "-test":
            tests = True
        elif word == "-tags":
            tags, skip = tag_set(words[k + 1]) if k + 1 < len(words) else frozenset(), True
        elif word.startswith("-tags="):
            tags = tag_set(word[len("-tags="):])
        elif word.startswith("-"):
            skip = word in FLAGS_WITH_VALUES
        else:
            packages.append(place(directory, word))
    return Load(tuple(packages), tests, tags, settings)


def fetch_file(path):
    """The Loads of each deps line of a fetch file (go-fetch.sh's file mode), [] when it is absent."""
    if not os.path.isfile(path):
        return []
    lines = []
    for raw in Path(path).read_text().splitlines():
        words = raw.split("#", 1)[0].split()
        given = {}
        while words and "=" in words[0]:
            name, _, value = words.pop(0).partition("=")
            given[name] = value
        if len(words) >= 3 and words[0] == "deps":
            load = deps_line(posixpath.normpath(words[1]), words[2:], settings_of({}, given))
            if load is not None:
                lines.append(load)
    return lines


def need_of(values, k, directory, env, assigns):
    """(the Load a go command at word k needs, or None for a subcommand that loads nothing; or
    UNREAD when what it loads is not literal)."""
    rest = values[k + 1:]
    if rest[:1] == ["-C"] and len(rest) > 1:
        directory, rest = posixpath.normpath(posixpath.join(directory, rest[1])), rest[2:]
    elif rest and rest[0].startswith("-C="):
        directory, rest = posixpath.normpath(posixpath.join(directory, rest[0][3:])), rest[1:]
    sub, args = rest[0], rest[1:]
    if sub not in LOADS:
        return None
    if UNREAD in directory:
        return UNREAD
    tags, packages, skip = frozenset(), [], False
    for k2, arg in enumerate(args):
        if skip:
            skip = False
            if args[k2 - 1] == "-tags":
                if UNREAD in arg:
                    return UNREAD
                tags = tag_set(arg)
            continue
        if UNREAD in arg:
            return UNREAD
        if arg == "-args":
            break
        if arg in ("-m", "-mod=mod") and sub == "list":
            return UNREAD
        if arg.startswith("-tags="):
            tags = tag_set(arg[len("-tags="):])
        elif arg.startswith("-"):
            skip = arg in FLAGS_WITH_VALUES and "=" not in arg
        elif arg.startswith((".", "/")) or arg.endswith(".go"):
            packages.append(place(directory, arg))
            if sub == "run":
                break
        else:
            return UNREAD
    settings = settings_of(env, assigns)
    if UNREAD in [s for s in settings if s is not None]:
        return UNREAD
    return Load(tuple(packages) or (directory,), sub in ("test", "vet"), tags, settings)


def go_env(document, job, step):
    env = {}
    for scope in (document, job, step):
        env.update({str(k): v for k, v in ((scope or {}).get("env") or {}).items()})
    return env


def step_directory(document, job, step):
    default = ((document.get("defaults") or {}).get("run") or {}).get("working-directory", ".")
    job_dir = ((job.get("defaults") or {}).get("run") or {}).get("working-directory", default)
    return posixpath.normpath(str(step.get("working-directory", job_dir)))


def check(workflows, errors):
    """Rule 7 over each required workflow (path -> text): each offline go command a step of a job
    whose env sets GOPROXY=off runs, that no fetch before it holds or whose packages, tags,
    directory or settings it cannot read, and that says no reason, adds an error. Returns
    (commands held, commands that say why)."""
    held = reasoned = 0
    for workflow_path in sorted(workflows):
        text = Path(workflow_path).read_text()
        document = yaml.safe_load(text) or {}
        raw = text.split("\n")
        for job_id, job in (document.get("jobs") or {}).items():
            if not isinstance(job, dict) or str(go_env(document, job, {}).get("GOPROXY")) != "off":
                continue
            fetched = []
            for step in job.get("steps") or []:
                run = step.get("run")
                if not isinstance(run, str):
                    continue
                env = go_env(document, job, step)
                directory = step_directory(document, job, step)
                name = step.get("name") or run.strip().splitlines()[0]
                where = f"job {job_id}, step \"{name}\""
                lines = run.split("\n")
                step_reason = step_reasoned(raw, step, run)
                missing = []
                for command in shell_script(run):
                    values = command.values()
                    start = shell_command_word(command)
                    if start is None:
                        continue
                    if values[start] == "cd" and not command.forked:
                        target = values[start + 1] if start + 1 < len(values) else ""
                        directory = UNREAD if UNREAD in target or not target or target.startswith(("/", "~")) \
                            else posixpath.normpath(posixpath.join(directory, target))
                        continue
                    assigns = {n: v.value() if v.static else UNREAD for n, v in command.assigns}
                    for k, value in enumerate(values):
                        if value.endswith("go-fetch.sh") and k + 2 < len(values):
                            if values[k + 1] == "file" and UNREAD not in values[k + 2] + directory:
                                fetched += fetch_file(posixpath.normpath(posixpath.join(directory, values[k + 2])))
                            elif values[k + 1] == "deps" and UNREAD not in values[k + 2] + directory:
                                load = deps_line(posixpath.normpath(posixpath.join(directory, values[k + 2])),
                                                 values[k + 3:], settings_of(env, assigns))
                                fetched += [load] if load is not None else []
                    # A step that sends its go online on purpose (its own GOPROXY) is rule 4's to hold.
                    if str(assigns.get("GOPROXY", env.get("GOPROXY"))) != "off":
                        continue
                    for k in range(len(values)):
                        if not (goish_text(values[k]) and go_subcommand(values, k)):
                            continue
                        need = need_of(values, k, directory, env, assigns)
                        if need is None:
                            continue
                        shown = " ".join(w.source() for w in command.words[k:])[:200]
                        if need != UNREAD and covered(need, fetched):
                            held += 1
                        elif step_reason or scan.reasoned(lines, "sh", REASON, [command.line]):
                            reasoned += 1
                        elif need == UNREAD:
                            errors.append((workflow_path, 1, f"{where}, runs `{shown}`, whose packages, tags, directory "
                                           f"or GOOS, GOARCH, CGO_ENABLED or GOWORK this rule cannot read, so no fetch "
                                           f"can be held to it: write them literally, or say in a `# go-offline: <why>` "
                                           f"line above it, or above its step's name, which deps line of the job's "
                                           f"fetch file covers it"))
                        else:
                            missing.append((need, shown))
                for need, shown in missing:
                    settings = " ".join(f"{n}={v}" for n, v in zip(SETTINGS, need.settings) if v is not None)
                    module = module_root(need.packages[0].removesuffix("/...")) or "."
                    wanted = " ".join(relative(p, module) for p in need.packages)
                    flags = (" -test" if need.tests else "") + (f" -tags {','.join(sorted(need.tags))}"
                                                                 if need.tags else "")
                    errors.append((workflow_path, 1, f"{where}, runs `{shown}`, which loads "
                                   f"{' '.join(need.packages)}, and no fetch before it in the job names them: add "
                                   f"`{(settings + ' ') if settings else ''}deps {module}{flags} {wanted}` to the "
                                   f"job's fetch file under .github/go-fetch/, or say in a `# go-offline: <why>` line "
                                   f"above it why it needs none"))
    return held, reasoned
