#!/usr/bin/env bash
# Every network client a workflow step or a composite action runs at a command position goes
# through .github/scripts/ci-log-guard.sh, or is on .github/log-guard-allow.tsv with the reason it
# needs nothing.
#
# A client that copies a server's reply into the job log lets the server write workflow commands
# there (`\r::warning::…`, `\t::error::…`, `… ##[warning]…`), and only a stop-commands block around
# that output stops them (ci-log-guard.sh says how the runner reads the log). So a new step that
# runs go, gh, curl, git fetch, docker pull or another client of NETWORK below, unguarded, fails
# here by name, rather than reading green until a server's text acts in a log.
#
# What it reads: each `run:` of every file of .github/workflows and of each composite action under
# .github/actions, split into simple commands by .github/scripts/shell_commands.py's scanner (quote
# aware, a `$(…)` its own command, `${{ … }}` read as a word). A command is routed when its first
# word, past `VAR=value` assignments and keywords, is one of WRAPPERS, or when its line is the
# guarded pipeline group `{ …; } 2>&1 | ci_log_guard cat`. A command that is not routed is a
# finding when its command word (or, through `sh -c`, `bash -c` and `eval`, the word it runs) is a
# client of NETWORK running a subcommand that reaches a server.
#
# It refuses, naming the file, the job, the step and the command:
#   1. a finding no row of the allow list names (file, job, step, client), and
#   2. a row of the allow list that names no finding, or that gives no reason: a row outliving its
#      step would let the next step of that name through unread.
#
# It does not read: a step under a shell other than bash or sh, a script a step runs (a script that
# runs a client routes it itself, as pg-connection-ceiling.sh does), a command a variable names, and
# a client run under a wrapper this check does not name.
#
#   .github/scripts/check-log-guard.sh          check
#   .github/scripts/check-log-guard.sh list     print every finding as an allow-list row with no reason
#
# CI runs it in ci.yaml's ci-filters job and in .github/workflows/workflow-files.yaml; its tests,
# check-log-guard.test.sh, in ci.yaml's test job.
set -euo pipefail

scripts=$(dirname "$(readlink -f "$0")")
cd "$(dirname "$0")/../.."

PYTHONDONTWRITEBYTECODE=1 PYTHONPATH="$scripts" python3 - "${1:-check}" <<'PY'
import re
import sys
from pathlib import Path

import yaml
from shell_commands import ASSIGNMENT, EXPRESSION, INTERPRETERS, SKIP_WORDS, Scanner, command_of

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


def stdout_safe(line: str, start: int, frames: list) -> bool:
    """Whether the command that starts at `start`, inside `frames`, keeps its stdout out of the
    log: it redirects stdout (`>`, `1>`, `>>`, `>|`, `>&…`, `&>`, but not `2>` and not into a
    `>(…)`), it pipes into `ci_log_guard`, or it ends at an unquoted `;`, `&&`, `||`, a lone `&`,
    a newline or its frame's close inside a group (`{ …; }`, `( … )`) whose own stdout is kept
    so. A `#` at a word's start ends the line's code. Each character is read in the state _scan
    gives it, so a quote, a nested capture or an escape hides it."""
    for i in range(start, len(line)):
        stack, quote, escaped = _scan(line, i)
        if len(stack) < len(frames):
            break
        if quote or escaped or len(stack) > len(frames):
            continue
        ch, nxt = line[i], line[i + 1:i + 2]
        if ch == "#" and _word_start(line, i):
            break
        if ch in ";)`" or (ch == "}" and _word_start(line, i)):
            break
        if ch == "|":
            if nxt == "|" or line[i - 1:i] == "|":
                break
            if line[i + 1:].lstrip(" \t").startswith("ci_log_guard "):
                return True
        elif ch == "&":
            if nxt == ">":
                return True
            if line[i - 1:i] not in (">", "&") and nxt != "&":
                break
            if nxt == "&":
                break
        elif ch == ">":
            before = line[i - 1:i]
            if before in (">", "&"):
                continue
            if before.isdigit() and before != "1" and (i - 1 == start or line[i - 2:i - 1] in " \t"):
                continue
            if line[i + 1:].lstrip(" \t>|").startswith("("):
                return False
            return True
    groups = [f for f in frames if f[0] in ("brace", "subshell")]
    if not groups or frames[-1][0] not in ("brace", "subshell"):
        return False
    group = groups[-1]
    end = _close_of(line, group)
    return end > 0 and stdout_safe(line, end, frames[:frames.index(group)])


def loose_stderr_guards(run: str) -> list:
    """Each ci_log_guard_stderr call whose stdout is neither captured nor kept out of the log:
    that command's stdout reaches the log outside the block. Read by the line, a
    backslash-continued line joined to the next first, and each call by its own command alone."""
    loose = []
    for line in EXPRESSION.sub("EXPR", run).replace("\\\n", " ").split("\n"):
        if line.lstrip(" \t").startswith("#"):
            continue
        for call in STDERR_CALL.finditer(line):
            frames = frames_at(line, call.start())
            if any(kind in ("capture", "`") for kind, _ in frames):
                continue
            if not stdout_safe(line, call.end(), frames):
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


def network_client(words: list) -> str:
    """The client of NETWORK the words run against a server, or ''."""
    words = list(words)
    while words and words[0].rsplit("/", 1)[-1] in RUNNERS:
        runner = words.pop(0).rsplit("/", 1)[-1]
        valued, plain = RUNNERS[runner]
        if runner == "command" and words and words[0] in ("-v", "-V"):
            return ""
        while words and words[0].startswith("-") and words[0] != "-":
            flag = words.pop(0)
            if flag in valued and words:
                words.pop(0)
        while runner == "env" and words and ASSIGNMENT.match(words[0]):
            words.pop(0)
        del words[:plain]
    if not words:
        return ""
    # A shell's `-c` script, or eval's words, after a runner: each of its commands is read the same
    # way (shell_commands' command_of reads through the interpreter and its flags).
    if words[0].rsplit("/", 1)[-1] in INTERPRETERS or words[0] == "eval":
        for inner in command_of(list(words)):
            if inner != words:
                client = network_client(inner)
                if client:
                    return client
        return ""
    if words[0].rsplit("/", 1)[-1] == "find":
        for i, word in enumerate(words):
            if word in FIND_EXEC:
                client = network_client(words[i + 1:])
                if client:
                    return client
        return ""
    if words[0].rsplit("/", 1)[-1] in ("bunx", "npx") and len(words) > 1 and words[1].startswith("playwright"):
        words = ["playwright"] + words[2:]
    client = words[0].rsplit("/", 1)[-1]
    if client not in NETWORK:
        return ""
    subcommands = NETWORK[client]
    if subcommands is None:
        return client
    rest = words[1:]
    while rest and rest[0].startswith("-"):
        flag = rest.pop(0)
        if flag in VALUED.get(client, ()) and rest:
            rest.pop(0)
    return client if rest and rest[0] in subcommands else ""


def shell_of(document: dict, holder: dict, step: dict):
    shell = step.get("shell") or ((holder.get("defaults") or {}).get("run") or {}).get("shell") \
        or ((document.get("defaults") or {}).get("run") or {}).get("shell")
    return str(shell).split()[0].rsplit("/", 1)[-1] if shell else None


files = sorted(p for p in Path(".github/workflows").iterdir() if p.suffix in (".yaml", ".yml"))
files += sorted(p for p in Path(".github/actions").glob("*/action.y*ml"))
findings = {}  # (file, job, step, client) -> [command text]
loose = []  # (file, job, step, line): a ci_log_guard_stderr whose stdout reaches the log
unread = 0
for path in files:
    document = yaml.safe_load(path.read_text()) or {}
    holders = [(job_id, job) for job_id, job in (document.get("jobs") or {}).items() if isinstance(job, dict)]
    if isinstance(document.get("runs"), dict):
        holders.append(("-", document["runs"]))
    for job_id, holder in holders:
        for index, step in enumerate(holder.get("steps") or []):
            if not isinstance(step, dict) or not isinstance(step.get("run"), str):
                continue
            if shell_of(document, holder, step) not in SHELLS:
                unread += 1
                continue
            label = str(step.get("name") or step.get("id") or f"step {index + 1}")
            for routed, words in commands(step["run"]):
                client = "" if routed else network_client(words)
                if client:
                    findings.setdefault((str(path), str(job_id), label, client), []).append(" ".join(words))
            loose += [(str(path), str(job_id), label, line) for line in loose_stderr_guards(step["run"])]

if not files:
    print("::error::check-log-guard: read no workflow file and no composite action, so it checked nothing")
    sys.exit(1)

if MODE == "list":
    for key in sorted(findings):
        print("\t".join(key) + "\t")
    sys.exit(0)

rows = {}
problems = [f"::error file={file}::job {job} step \"{label}\" runs `{line}`: ci_log_guard_stderr blocks only "
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
        problems += [f"::error file={file}::job {job} step \"{label}\" runs `{command}`, a {client} "
                     f"command that can copy a server's reply into the log, outside "
                     f".github/scripts/ci-log-guard.sh: run it as `ci_log_guard …` (output to the log) or "
                     f"`ci_log_guard_stderr …` (stdout captured), or add a row to {ALLOW} saying why it needs "
                     f"no guard" for command in findings[key]]
for key, number in sorted(rows.items(), key=lambda item: item[1]):
    if key not in findings:
        file, job, label, client = key
        problems.append(f"::error file={ALLOW},line={number}::row {number} names job {job} step \"{label}\" of "
                        f"{file} running {client} unguarded, and no such step does: remove the row")

for problem in problems:
    print(problem)
print(f"check-log-guard: {len(files)} files, {len(findings)} unguarded network commands, every one on {ALLOW}; "
      f"{unread} steps under another shell not read" if not problems
      else f"::error::check-log-guard: {len(problems)} problem(s)")
sys.exit(1 if problems else 0)
PY
