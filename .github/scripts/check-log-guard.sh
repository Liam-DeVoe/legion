#!/usr/bin/env bash
# Every network client a workflow step or a composite action runs at a command position, itself or
# in a script it runs, goes through .github/scripts/ci-log-guard.sh, or is on
# .github/log-guard-allow.tsv with the reason it needs nothing.
#
# A client that copies a server's reply into the job log lets the server write workflow commands
# there (`\r::warning::…`, `\t::error::…`, `… ##[warning]…`), and only a stop-commands block around
# that output stops them (ci-log-guard.sh says how the runner reads the log). So a new step or a new
# script line that runs go, gh, curl, git fetch, docker pull or another client of NETWORK below,
# unguarded, fails here by name, rather than reading green until a server's text acts in a log.
#
# What it reads: each `run:` of every file of .github/workflows and of each composite action under
# .github/actions, and each shell script of this repository such a step runs, split into simple
# commands by .github/scripts/shell_commands.py's scanner (quote aware, a `$(…)` its own command,
# a here-document's body no command, `${{ … }}` read as a word). A command is routed when its first
# word, past `VAR=value` assignments and keywords, is one of WRAPPERS, or when its line is the
# guarded pipeline group `{ …; } 2>&1 | ci_log_guard cat`. A command that is not routed is a
# finding when its command word (or, through `sh -c`, `bash -c` and `eval`, the word it runs) is a
# client of NETWORK running a subcommand that reaches a server.
#
# A script a step runs is the command word of a command it does not route (through a runner, an
# interpreter such as `bash x.sh` or `. x.sh`, or eval) that names a file of this repository ending
# in .sh or starting with a `#!` line naming sh, bash or dash: `$GITHUB_WORKSPACE/` (or
# `${{ github.workspace }}/`) at its start names the repository root, `$GITHUB_ACTION_PATH/` (or
# `${{ github.action_path }}/`) the composite action's own directory, and any other relative path
# is read from the step's working directory. The script is read as a step is, and so is each script
# it runs, its relative paths read from the repository root, where the steps run. A script's
# finding is the script's own: a row names it as (the script, `-`, `-`, client), whichever steps run
# it.
#
# It refuses, naming the file, the job, the step and the command (for a script, the script and the
# first step that runs it):
#   1. a finding no row of the allow list names (file, job, step, client), and
#   2. a row of the allow list that names no finding, or that gives no reason: a row outliving its
#      step would let the next step of that name through unread.
#
# It does not read: a step under a shell other than bash or sh, a script named through a variable
# or a `$(…)` (`"$(dirname "$0")/x.sh"`, `"$here/x.sh"`), a program in another language, a command a
# variable names, a client run under a wrapper this check does not name, and a here-document's
# body, which runs no command unless a shell reads it on its standard input.
#
#   .github/scripts/check-log-guard.sh          check
#   .github/scripts/check-log-guard.sh list     print every finding as an allow-list row with no reason
#   .github/scripts/check-log-guard.sh scripts  print every script it reads, one path a line
#
# CI runs it in ci.yaml's ci-filters job and in .github/workflows/workflow-files.yaml; its tests,
# check-log-guard.test.sh, in ci.yaml's test job.
set -euo pipefail

scripts=$(dirname "$(readlink -f "$0")")
cd "$(dirname "$0")/../.."

PYTHONDONTWRITEBYTECODE=1 PYTHONPATH="$scripts" python3 - "${1:-check}" <<'PY'
import os
import re
import sys
from pathlib import Path

import yaml
from shell_commands import (ASSIGNMENT, EXPRESSION, INTERPRETERS, SKIP_WORDS, Scanner, command_of,
                            quoted_line_starts, without_heredocs)

MODE = sys.argv[1]
ALLOW = Path(".github/log-guard-allow.tsv")
WRAPPERS = ("ci_log_guard", "ci_log_guard_stderr")
# env and time are runners here (RUNNERS), read with their options, not keywords shed before the
# command.
KEYWORDS = SKIP_WORDS - set(WRAPPERS) - {"env", "time"}
GROUP = re.compile(r"\{[ \t]*([^{}]*);[ \t]*\}[ \t]*2>&1[ \t]*\|[ \t]*ci_log_guard[ \t]+cat")
# A call of ci_log_guard_stderr. Its stdout is no part of the log when the call stands inside a
# `$(…)`, a backtick pair or a `<(…)` still open at the call, or when its own command, up to the
# first unquoted `;`, `&&`, `||`, `|`, `&` or newline, redirects its stdout (stdout_safe).
STDERR_CALL = re.compile(r"(?<![\w-])ci_log_guard_stderr[ \t]+")
SHELLS = {None, "bash", "sh"}
# The first line of a script this check reads that does not end in .sh.
SHEBANG = re.compile(r"#![^\n]*\b(?:ba|da)?sh\b")
# The two expressions a step names the repository root and a composite action's own directory
# with, read as the variables they equal before every other expression is elided.
DIRECTORY_EXPRESSIONS = ((re.compile(r"\$\{\{\s*github\.workspace\s*\}\}"), "$GITHUB_WORKSPACE"),
                         (re.compile(r"\$\{\{\s*github\.action_path\s*\}\}"), "$GITHUB_ACTION_PATH"))

# client -> the subcommands that reach a server, or None for every use of the client. bun and bunx
# are not here: the audit's probe of bun 1.4.4 (`bun install`, `bunx`) against a server answering
# 504 with a planted body printed the status code and never the body. A bunx that runs Playwright's
# own installer is read as the client `playwright` (network_client), whose downloader nobody probed.
NETWORK = {
    "curl": None, "wget": None, "gh": None, "npx": None,
    "playwright": {"install"},
    "go": {"build", "test", "vet", "run", "list", "mod", "install", "get", "generate", "work", "tool"},
    "git": {"fetch", "pull", "push", "clone", "ls-remote", "submodule", "remote"},
    "docker": {"pull", "push", "run", "build", "buildx", "compose", "login", "manifest", "image", "create"},
    "npm": {"install", "i", "ci", "view", "info", "show", "publish", "update", "exec", "dist-tag", "unpublish", "add"},
    "crane": {"copy", "cp", "pull", "push", "digest", "manifest", "ls", "catalog", "config", "export", "tag",
              "delete", "append", "mutate", "auth", "index", "flatten", "rebase", "blob", "validate", "edit"},
    "apt-get": {"install", "update", "upgrade", "dist-upgrade", "download", "source", "build-dep"},
    "apt": {"install", "update", "upgrade", "full-upgrade", "download", "source"},
    "pip": {"install", "download", "index"}, "pip3": {"install", "download", "index"},
    "uv": {"pip", "sync", "add", "tool", "run", "lock", "python", "venv", "self"},
    "mise": {"install", "i", "use", "u", "exec", "x", "upgrade", "up", "plugins", "p", "self-update", "run", "r",
             "latest", "ls-remote"},
    "elan": {"toolchain", "default", "self", "override", "run", "update"},
    "lake": {"build", "update", "exe", "test", "run", "upload", "unpack"},
}
# The options before a subcommand that take a value: `go -C dir`, `git -C dir`, `git -c k=v`,
# `docker --config d`.
VALUED = {"go": {"-C"}, "git": {"-C", "-c", "--git-dir", "--work-tree"},
          "docker": {"--config", "-c", "--context", "-H", "--host", "-l", "--log-level"}}


class RawScanner(Scanner):
    """shell_commands' scanner, keeping each simple command's words as written."""

    def end_command(self) -> None:
        self.end_word()
        if self.words:
            self.commands.append(self.words)
        self.words = []
        self.redirect_target = False


def commands(run: str) -> list:
    """(routed, words) for each simple command of a run: script."""
    out, rest = [], []
    for line in EXPRESSION.sub("EXPR", run).split("\n"):
        group = GROUP.fullmatch(line.strip(" \t"))
        if group:
            out += [(True, words) for words in Scanner().scan(group.group(1))]
            rest.append("")
        else:
            rest.append(line)
    for words in RawScanner().scan("\n".join(rest)):
        words = list(words)
        while words and (ASSIGNMENT.match(words[0]) or words[0] in KEYWORDS):
            words.pop(0)
        if words and words[0] in WRAPPERS:
            out.append((True, words[1:]))
        elif words and words[0].rsplit("/", 1)[-1] in RUNNERS:
            out.append((False, words))
        elif words:
            out += [(False, w) for w in command_of(words)]
    return out


def _word_start(line: str, i: int) -> bool:
    return i == 0 or line[i - 1] in " \t;&|(){}"


def _scan(line: str, upto: int):
    """The state of the line at `upto`: the frames still open, outermost first, each (kind, the
    index it opened at, the quote it opened inside), the quote open in the innermost frame, and
    whether the character at `upto` is escaped. A frame is a `capture` (`$(…)`, `<(…)`), a
    backtick pair, a `procout` (`>(…)`), a `subshell` (`(…)`) or a `brace` group (`{ …; }`). Each
    frame starts unquoted and keeps the quote it opened inside: a `$(` inside double quotes opens
    a capture whose `)` closes it, and the outer quote resumes there, so `v="$(date)"; …` closes
    its capture before the `;`."""
    stack, quote, i = [], None, 0
    while i < upto:
        ch, nxt = line[i], line[i + 1:i + 2]
        if quote == "'":
            if ch == "'":
                quote = None
        elif ch == "\\":
            i += 1
        elif ch == "`":
            if stack and stack[-1][0] == "`":
                quote = stack.pop()[2]
            else:
                stack.append(("`", i, quote))
                quote = None
        elif ch == "$" and nxt == "(":
            stack.append(("capture", i, quote))
            quote = None
            i += 1
        elif quote == '"':
            if ch == '"':
                quote = None
        elif ch in "'\"":
            quote = ch
        elif ch in "<>" and nxt == "(":
            stack.append(("capture" if ch == "<" else "procout", i, None))
            i += 1
        elif ch == "(":
            stack.append(("subshell", i, None))
        elif ch == ")" and stack and stack[-1][0] in ("capture", "procout", "subshell"):
            quote = stack.pop()[2]
        elif ch == "{" and _word_start(line, i) and nxt in (" ", "\t", ""):
            stack.append(("brace", i, None))
        elif ch == "}" and _word_start(line, i) and stack and stack[-1][0] == "brace":
            stack.pop()
        i += 1
    return stack, quote, i > upto


def frames_at(line: str, upto: int) -> list:
    """The frames still open at `upto` (_scan), each as (kind, the index it opened at)."""
    return [(kind, pos) for kind, pos, _ in _scan(line, upto)[0]]


def _close_of(line: str, frame: tuple) -> int:
    """The index just past the character that closes `frame`, or -1 when the line leaves it open."""
    for j in range(frame[1] + 1, len(line) + 1):
        if frame not in frames_at(line, j):
            return j
    return -1


# Where a redirection sends stdout that is still the log after a pipe: the stage writes it after
# the guard's block has closed.
LOG_TARGETS = ("/dev/stdout", "/dev/stderr", "/dev/fd/1", "/dev/fd/2", "/proc/self/fd/1", "/proc/self/fd/2")


def _target(line: str, i: int) -> str:
    """The target of the redirection whose `>` (or `&>`'s `>`) is at `i`: `&<fd>` for a dup, `(`
    for a `>(…)`, else the target word, with its quotes taken off."""
    rest = line[i + 1:].lstrip(">|")
    if rest.startswith("&"):
        return "&" + re.match(r"&(\S*)", rest).group(1)
    rest = rest.lstrip(" \t")
    if rest.startswith("(") or rest.startswith(">("):
        return "("
    word = re.match(r"""[^\s;&|<>()]*""", rest).group(0)
    return word.strip("'\"") or "("


def stdout_safe(line: str, start: int, frames: list) -> bool:
    """Whether the command that starts at `start`, inside `frames`, keeps its stdout out of the
    log. The last redirection of stdout in the command's last stage decides, as the shell applies
    redirections left to right (`> out.json >&2` sends stdout to stderr). Before any pipe, a
    redirection does (`>`, `1>`, `>>`, `>|`, `>&…`, `&>`, but not `2>`, not `>&1` or
    `/dev/stdout`, and not into a `>(…)`): the command's stdout then goes where it goes while the
    block is open. After a pipe, the last stage writes after the block has closed, so only a
    redirection to a file does, not to a file descriptor, `/dev/stdout`, `/dev/stderr` or a
    `>(…)`; a pipe starts the decision afresh, any stage naming one of LOG_TARGETS refuses the call
    (`| tee /dev/stderr > f`), and a pipe into `ci_log_guard` is refused too, since its block and
    this one interleave under one stop state. The command ends at an unquoted `;`, `&&`, `||`, a
    lone `&`, a newline, a `#` at a word's start, or its frame's close; a command with no pipe and
    no redirection of its own inside a `{ …; }` or `( … )` group is then read by the group's
    redirection the same way (a piped one is refused: the group's redirection would apply after the
    block has closed). Each character is read in the state _scan gives it, so a quote, a nested
    capture or an escape hides it."""
    piped, verdict = False, None
    for i in range(start, len(line)):
        stack, quote, escaped = _scan(line, i)
        if len(stack) < len(frames):
            break
        if escaped or len(stack) > len(frames):
            continue
        if piped and _word_start(line, i) and any(line.startswith(t, i + (line[i] in "'\"")) for t in LOG_TARGETS):
            return False
        if quote:
            continue
        ch, nxt = line[i], line[i + 1:i + 2]
        if ch == "#" and _word_start(line, i):
            break
        if ch in ";)`" or (ch == "}" and _word_start(line, i)):
            break
        if ch == "|":
            if nxt == "|" or line[i - 1:i] == "|":
                break
            if line[i - 1:i] == ">":
                continue
            if line[i + 1:].lstrip(" \t").startswith("ci_log_guard"):
                return False
            piped, verdict = True, None
        elif ch == "&":
            if nxt == ">":
                continue
            if line[i - 1:i] in (">", "&") or nxt == "&":
                if nxt == "&":
                    break
                continue
            break
        elif ch == ">":
            before = line[i - 1:i]
            if before == ">":
                continue
            if before.isdigit() and before != "1" and (i - 1 == start or line[i - 2:i - 1] in " \t"):
                continue
            target = _target(line, i)
            if target == "(":
                verdict = False
            elif target.startswith("&"):
                verdict = not (piped or target == "&1")
            elif target in LOG_TARGETS:
                verdict = not (piped or target in ("/dev/stdout", "/dev/fd/1", "/proc/self/fd/1"))
            else:
                verdict = True
    if verdict is not None:
        return verdict
    if piped:
        return False
    groups = [f for f in frames if f[0] in ("brace", "subshell")]
    if not groups or frames[-1][0] not in ("brace", "subshell"):
        return False
    group = groups[-1]
    end = _close_of(line, group)
    return end > 0 and stdout_safe(line, end, frames[:frames.index(group)])


def loose_stderr_guards(run: str) -> list:
    """Each ci_log_guard_stderr call whose stdout is neither captured nor kept out of the log:
    that command's stdout reaches the log outside the block. Read by the line, a
    backslash-continued line joined to the next first, each line from the quote it starts inside
    (a string a line before opened), and each call by its own command alone. A call inside a quoted
    string is text: a harness's expected output, or a script string that `sh -c` or eval runs,
    whose client the finding rule reads as not routed."""
    loose = []
    text = EXPRESSION.sub("EXPR", run).replace("\\\n", " ")
    starts = quoted_line_starts(text)
    for number, line in enumerate(text.split("\n")):
        opened = starts.get(number, "").lstrip("$")  # _scan reads `$'` as `'`
        if not opened and line.lstrip(" \t").startswith("#"):
            continue
        read = opened + line
        for call in STDERR_CALL.finditer(read):
            stack, quote, escaped = _scan(read, call.start())
            if quote or escaped:
                continue
            frames = [(kind, pos) for kind, pos, _ in stack]
            if any(kind in ("capture", "`") for kind, _ in frames):
                continue
            if not stdout_safe(read, call.end(), frames):
                loose.append(line.strip(" \t"))
    return loose


# Commands that run the words after them, each with its options that take a value (their value is
# skipped with them) and the number of plain words it takes before the command (timeout's
# duration). env also takes `NAME=value` words; `command -v` and `command -V` look a name up rather
# than run it.
RUNNERS = {
    "sudo": ({"-u", "-g", "-C", "-h", "-p", "-U", "-r", "-t", "-D", "-R", "--user", "--group", "--chdir",
              "--host", "--prompt", "--other-user", "--role", "--type", "--close-from"}, 0),
    "timeout": ({"-s", "--signal", "-k", "--kill-after"}, 1),
    "nice": ({"-n", "--adjustment"}, 0),
    "nohup": (set(), 0),
    "stdbuf": ({"-i", "-o", "-e", "--input", "--output", "--error"}, 0),
    "xargs": ({"-n", "-L", "-P", "-I", "-s", "-d", "-E", "-a", "--max-args", "--max-lines", "--max-procs",
               "--replace", "--delimiter", "--eof", "--arg-file", "--max-chars"}, 0),
    "env": ({"-u", "--unset", "-C", "--chdir", "-S", "--split-string"}, 0),
    "command": (set(), 0),
    "time": (set(), 0),
    # .github/scripts/gh-read.sh's two: each runs the command after it, and prints the stderr of
    # every run it ends on itself (unless --stderr names a file), outside any block, so a step that
    # calls one is read as calling the client.
    "gh_wait_reset": ({"--stderr"}, 0),
    "gh_read": ({"--stderr"}, 0),
}
# find runs the words after each of these, up to `;` or `+`.
FIND_EXEC = ("-exec", "-execdir", "-ok", "-okdir")


def run_commands(words: list) -> list:
    """The commands the words run, each as its words: past each of RUNNERS with its options, through
    a shell's `-c` script or the script it is handed and eval's words (shell_commands' command_of
    reads through the interpreter and its flags), and behind find's -exec."""
    words = list(words)
    while words and words[0].rsplit("/", 1)[-1] in RUNNERS:
        runner = words.pop(0).rsplit("/", 1)[-1]
        valued, plain = RUNNERS[runner]
        if runner == "command" and words and words[0] in ("-v", "-V"):
            return []
        while words and words[0].startswith("-") and words[0] != "-":
            flag = words.pop(0)
            if flag in valued and words:
                words.pop(0)
        while runner == "env" and words and ASSIGNMENT.match(words[0]):
            words.pop(0)
        del words[:plain]
    if not words:
        return []
    if words[0].rsplit("/", 1)[-1] in INTERPRETERS or words[0] == "eval":
        return [run for inner in command_of(list(words)) if inner != words for run in run_commands(inner)]
    if words[0].rsplit("/", 1)[-1] == "find":
        return [run for i, word in enumerate(words) if word in FIND_EXEC for run in run_commands(words[i + 1:])]
    return [words]


def network_client(words: list) -> str:
    """The client of NETWORK the words run against a server, or ''."""
    for run in run_commands(words):
        if run[0].rsplit("/", 1)[-1] in ("bunx", "npx") and len(run) > 1 and run[1].startswith("playwright"):
            run = ["playwright"] + run[2:]
        client = run[0].rsplit("/", 1)[-1]
        if client not in NETWORK:
            continue
        subcommands = NETWORK[client]
        if subcommands is None:
            return client
        rest = run[1:]
        while rest and rest[0].startswith("-"):
            flag = rest.pop(0)
            if flag in VALUED.get(client, ()) and rest:
                rest.pop(0)
        if rest and rest[0] in subcommands:
            return client
    return ""


def shell_of(document: dict, holder: dict, step: dict):
    shell = step.get("shell") or ((holder.get("defaults") or {}).get("run") or {}).get("shell") \
        or ((document.get("defaults") or {}).get("run") or {}).get("shell")
    return str(shell).split()[0].rsplit("/", 1)[-1] if shell else None


def workdir_of(document: dict, holder: dict, step: dict):
    """The directory a step runs in, from the repository root, or None where an expression names it."""
    given = str(step.get("working-directory") or ((holder.get("defaults") or {}).get("run") or {}).get(
        "working-directory") or ((document.get("defaults") or {}).get("run") or {}).get("working-directory") or ".")
    return None if "${{" in given else given


def script_path(word: str, workdir, action) -> str:
    """The shell script of this repository a command's first word names, as a path from the
    repository root, or '': `$GITHUB_WORKSPACE/` names the root and `$GITHUB_ACTION_PATH/` the
    composite action's directory (`action`, None outside one); any other relative path is read from
    `workdir` (None: not known), and a word holding any other variable, a substitution or a glob
    names nothing this check can read."""
    for prefix, base in (("$GITHUB_WORKSPACE/", "."), ("${GITHUB_WORKSPACE}/", "."),
                         ("$GITHUB_ACTION_PATH/", action), ("${GITHUB_ACTION_PATH}/", action)):
        if word.startswith(prefix):
            word, workdir = word[len(prefix):], base
            break
    if workdir is None or not word or any(c in word for c in "$`*?[~") or word.startswith("/"):
        return ""
    if "/" not in word and not word.endswith(".sh"):
        return ""  # a bare name is looked up on PATH
    path = os.path.normpath(os.path.join(workdir, word))
    if path.startswith("..") or os.path.isabs(path) or not os.path.isfile(path):
        return ""
    if path.endswith(".sh"):
        return path
    with open(path, errors="replace") as script:
        return path if SHEBANG.match(script.readline()) else ""


def subject(file: str, job: str, label: str) -> str:
    """How a problem names where its command is: a step, or a script and the first step that runs it."""
    if (job, label) != ("-", "-"):
        return f"job {job} step \"{label}\""
    by_file, by_job, by_label = scripts[file]
    return f"the script {file} (job {by_job} step \"{by_label}\" of {by_file} runs it)"


def read(text: str, key: tuple, workdir, action, runner: tuple) -> None:
    """Notes the findings of a step's run: or a script's text, under `key` (file, job, step), and
    reads each script it runs that this check has not read: from the repository root, with the
    composite action's directory `action` and `runner`, the step that ran the first of them."""
    text = without_heredocs(EXPRESSION.sub("EXPR", text))
    for routed, words in commands(text):
        if routed:
            continue
        client = network_client(words)
        if client:
            findings.setdefault(key + (client,), []).append(" ".join(words))
        for run in run_commands(words):
            script = script_path(run[0], workdir, action)
            if script and script not in scripts:
                scripts[script] = runner
                with open(script, errors="replace") as handle:
                    read(handle.read(), (script, "-", "-"), ".", action, runner)
    loose.extend(key + (line,) for line in loose_stderr_guards(text))


files = sorted(p for p in Path(".github/workflows").iterdir() if p.suffix in (".yaml", ".yml"))
files += sorted(p for p in Path(".github/actions").glob("*/action.y*ml"))
findings = {}  # (file, job, step, client) -> [command text]; a script's job and step are `-`
loose = []  # (file, job, step, line): a ci_log_guard_stderr whose stdout reaches the log
scripts = {}  # each script read -> (file, job, step) of the first step that runs it
unread = 0
for path in files:
    document = yaml.safe_load(path.read_text()) or {}
    holders = [(job_id, job) for job_id, job in (document.get("jobs") or {}).items() if isinstance(job, dict)]
    action = None
    if isinstance(document.get("runs"), dict):
        holders.append(("-", document["runs"]))
        action = str(path.parent)
    for job_id, holder in holders:
        for index, step in enumerate(holder.get("steps") or []):
            if not isinstance(step, dict) or not isinstance(step.get("run"), str):
                continue
            if shell_of(document, holder, step) not in SHELLS:
                unread += 1
                continue
            label = str(step.get("name") or step.get("id") or f"step {index + 1}")
            run = step["run"]
            for expression, variable in DIRECTORY_EXPRESSIONS:
                run = expression.sub(lambda _: variable, run)
            key = (str(path), str(job_id), label)
            read(run, key, workdir_of(document, holder, step), action, key)

if not files:
    print("::error::check-log-guard: read no workflow file and no composite action, so it checked nothing")
    sys.exit(1)

if MODE == "list":
    for key in sorted(findings):
        print("\t".join(key) + "\t")
    sys.exit(0)
if MODE == "scripts":
    print("\n".join(sorted(scripts)))
    sys.exit(0)

rows = {}
problems = [f"::error file={file}::{subject(file, job, label)} runs `{line}`: ci_log_guard_stderr blocks only "
            f"stderr, and this command's stdout is neither captured in $(…) nor redirected, so it reaches the "
            f"log outside the block; run it as `ci_log_guard …`, or capture or redirect its stdout"
            for file, job, label, line in loose]
for number, line in enumerate(ALLOW.read_text().split("\n"), 1):
    if not line.strip() or line.startswith("#"):
        continue
    fields = line.split("\t")
    if len(fields) != 5 or not fields[4].strip():
        problems.append(f"::error file={ALLOW},line={number}::row {number} is not file, job, step, client and a "
                        f"reason, tab-separated, with the reason given: a row says why its step needs no guard")
        continue
    rows[tuple(fields[:4])] = number

for key in sorted(findings):
    if key not in rows:
        file, job, label, client = key
        problems += [f"::error file={file}::{subject(file, job, label)} runs `{command}`, a {client} "
                     f"command that can copy a server's reply into the log, outside "
                     f".github/scripts/ci-log-guard.sh: run it as `ci_log_guard …` (output to the log) or "
                     f"`ci_log_guard_stderr …` (stdout captured), or add a row to {ALLOW} saying why it needs "
                     f"no guard" for command in findings[key]]
for key, number in sorted(rows.items(), key=lambda item: item[1]):
    if key not in findings:
        file, job, label, client = key
        problems.append(f"::error file={ALLOW},line={number}::row {number} names "
                        + (f"the script {file} running {client} unguarded, and no script a step runs does"
                           if (job, label) == ("-", "-") else
                           f"job {job} step \"{label}\" of {file} running {client} unguarded, and no such step "
                           f"does") + ": remove the row")

for problem in problems:
    print(problem)
print(f"check-log-guard: {len(files)} files and {len(scripts)} scripts, {len(findings)} unguarded network commands, "
      f"every one on {ALLOW}; {unread} steps under another shell not read" if not problems
      else f"::error::check-log-guard: {len(problems)} problem(s)")
sys.exit(1 if problems else 0)
PY
