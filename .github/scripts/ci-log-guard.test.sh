#!/usr/bin/env bash
# Tests for ci-log-guard.sh: a server's text that a guarded command copies to the log reaches the
# runner as no workflow command, in any of the three forms, from either stream, on a failed
# attempt and on a successful one; the step's own commands around the block still work; the
# command's exit status and a captured standard output come back unchanged.
#
# The log is read by a parser that applies the runner's rules (actions/runner v2.338.2):
#   - a line ends at `\n`, at `\r\n` and at a lone `\r` (StreamReader.ReadLine,
#     src/Runner.Sdk/ProcessInvoker.cs:513);
#   - a line is a command when, after the white space .NET's TrimStart removes, it starts with
#     `::<registered command>…::`, or when its first `##[` starts `##[<registered command>…]`
#     (src/Runner.Common/ActionCommand.cs:51-189), the names compared without case;
#   - `::stop-commands::<token>` stops every command until a line that parses as the command
#     `<token>` (src/Runner.Worker/ActionCommandManager.cs:86-124).
# The runner keeps one stop state for the step and handles the two streams in an order nobody
# promises, so the parser reads each log in three orders, and a case passes only when all three
# agree: each stream with a stop state of its own ("per-stream", where a block on one stream
# covers nothing on the other), all of standard error and then all of standard output under one
# state ("stderr-first", what one batch of ProcessInvoker.cs:417-440 does), and the reverse
# ("stdout-first").
#
# The controls: the same stand-in client run with no guard shows every planted command in every
# order, so the parser can see each form; and a stand-in guard that writes its markers on standard
# output, around a client writing on standard error, shows the client's commands, so the parser
# can see a block on the wrong stream.
#
# Run from anywhere: .github/scripts/ci-log-guard.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

cat > "$work/parse.py" <<'PY'
"""Reads one step's standard output and standard error as the runner does.

Prints one line for each workflow command the runner would act on, as
`<order>\t<stream>\t<command>\t<data>`, and `<order>\t<stream>\tSTOPPED-AT-END\t<token>` for a
stop that no line of that order resumed.
"""
import sys
import unicodedata

# Every name the runner registers (src/Runner.Worker/ActionCommandManager.cs: each extension's
# Command, and stop-commands itself).
REGISTERED = {
    "internal-set-repo-path", "set-env", "set-output", "save-state", "add-mask", "add-path",
    "add-matcher", "remove-matcher", "debug", "warning", "error", "notice", "group", "endgroup",
    "echo", "stop-commands",
}


def is_white(c):
    # .NET Char.IsWhiteSpace, which String.TrimStart() with no argument removes.
    return c in "\t\n\v\f\r\x85\xa0" or unicodedata.category(c) in ("Zs", "Zl", "Zp")


def lines_of(data):
    text = data.decode("utf-8", errors="replace")
    out, cur, i, n = [], [], 0, len(text)
    while i < n:
        c = text[i]
        if c == "\n":
            out.append("".join(cur))
            cur = []
        elif c == "\r":
            out.append("".join(cur))
            cur = []
            if i + 1 < n and text[i + 1] == "\n":
                i += 1
        else:
            cur.append(c)
        i += 1
    if cur:
        out.append("".join(cur))
    return out


def parse(line, registered):
    s = line
    while s and is_white(s[0]):
        s = s[1:]
    if s.startswith("::"):
        end = s.find("::", 2)
        if end >= 0:
            name = s[2:end].split(" ", 1)[0]
            if name.lower() in registered:
                return name.lower(), s[end + 2:]
    p = line.find("##[")
    if p >= 0:
        rb = line.find("]", p)
        if rb >= 0:
            name = line[p + 3:rb].split(" ", 1)[0]
            if name.lower() in registered:
                return name.lower(), line[rb + 1:]
    return None


class State:
    def __init__(self):
        self.token = None

    def feed(self, order, stream, line, out):
        registered = REGISTERED | ({self.token} if self.token else set())
        cmd = parse(line, registered)
        if cmd is None:
            return
        name, data = cmd
        if self.token is not None:
            if name == self.token:
                self.token = None
            return
        if name == "stop-commands":
            if not data or data.lower() in REGISTERED:
                out.append(f"{order}\t{stream}\tREFUSED-STOP\t{data}")
                return
            self.token = data.lower()
            return
        out.append(f"{order}\t{stream}\t{name}\t{data}")

    def end(self, order, stream, out):
        if self.token is not None:
            out.append(f"{order}\t{stream}\tSTOPPED-AT-END\t{self.token}")


streams = {}
for stream, path in (("stdout", sys.argv[1]), ("stderr", sys.argv[2])):
    with open(path, "rb") as fh:
        streams[stream] = lines_of(fh.read())

out = []
for stream in ("stdout", "stderr"):
    st = State()
    for line in streams[stream]:
        st.feed("per-stream", stream, line, out)
    st.end("per-stream", stream, out)
for order, first, second in (("stderr-first", "stderr", "stdout"), ("stdout-first", "stdout", "stderr")):
    st = State()
    for stream in (first, second):
        for line in streams[stream]:
            st.feed(order, stream, line, out)
    st.end(order, first + "+" + second, out)
print("\n".join(out))
PY

# parse <stdout file> <stderr file>: the commands the runner would act on, one per line.
parse() { python3 "$work/parse.py" "$1" "$2"; }

# count <text> <regex>: how many lines of the parse match.
count() { grep -c -- "$2" <<<"$1" || true; }

# The stand-in client: what go, gh or crane print when a server answers 504 with a body that
# plants each form, on both streams, and then exits with $1. The last write of each stream has no
# newline, as a body cut short would.
cat > "$work/client" <<'SH'
#!/usr/bin/env bash
for s in out err; do
  fd=1
  [ "$s" = err ] && fd=2
  {
    printf 'GET https://registry.example/pkg: 504 upstream timed out\r::warning::planted-%s-cr\n' "$s"
    printf '\t::warning::planted-%s-tab\n' "$s"
    printf '   ::warning::planted-%s-space\n' "$s"
    printf 'upstream said ##[warning]planted-%s-legacy\n' "$s"
    printf 'body cut short'
  } >&"$fd"
done
exit "$1"
SH
chmod +x "$work/client"

lib="$script_dir/ci-log-guard.sh"

# step <name> <shell body>: runs the body as a run: step does (bash -e, the library sourced), and
# keeps its two streams as the runner receives them, and its exit status.
step() {
  local name=$1 body=$2 status=0
  bash -e -c ". '$lib'; $body" >"$work/$name.out" 2>"$work/$name.err" || status=$?
  echo "$status" >"$work/$name.status"
}

echo "Controls: the parser sees what the runner would act on"

for attempt in 0 7; do
  step "bare-$attempt" "'$work/client' $attempt"
  got=$(parse "$work/bare-$attempt.out" "$work/bare-$attempt.err")
  for order in per-stream stderr-first stdout-first; do
    for s in out err; do
      for form in cr tab space legacy; do
        check "no guard, exit $attempt, $order: the parser sees planted-$s-$form" \
          "$(is "$(count "$got" "^$order	std$s	warning	planted-$s-$form\$")" 1)"
      done
    done
  done
done

# The stand-in guard on the wrong stream: its markers on standard output, the client's text on
# standard error, where the runner does not keep them in order with the markers.
step wrong-stream "
  t=\$(ci_log_token)
  printf '::stop-commands::%s\n' \"\$t\"
  '$work/client' 0 >/dev/null
  printf '\n::%s::\n' \"\$t\""
got=$(parse "$work/wrong-stream.out" "$work/wrong-stream.err")
for form in cr tab space legacy; do
  check "markers on stdout, text on stderr, per-stream: planted-err-$form leaks" \
    "$(is "$(count "$got" "^per-stream	stderr	warning	planted-err-$form\$")" 1)"
  check "markers on stdout, text on stderr, stderr-first: planted-err-$form leaks" \
    "$(is "$(count "$got" "^stderr-first	stderr	warning	planted-err-$form\$")" 1)"
done

echo
echo "ci_log_guard: the server's text is stopped on both streams"

for attempt in 0 7; do
  step "guard-$attempt" "
    echo '::warning::own-before' >&2
    ci_log_guard '$work/client' $attempt && rc=0 || rc=\$?
    echo '::warning::own-after' >&2
    exit \$rc"
  got=$(parse "$work/guard-$attempt.out" "$work/guard-$attempt.err")
  check "guard, exit $attempt: the parser sees no planted command in any order" \
    "$(is "$(count "$got" planted)" 0)"
  check "guard, exit $attempt: no block is left stopped" \
    "$(is "$(count "$got" STOPPED-AT-END)" 0)"
  for order in per-stream stderr-first stdout-first; do
    check "guard, exit $attempt, $order: the step's own warning before the block works" \
      "$(is "$(count "$got" "^$order	stderr	warning	own-before\$")" 1)"
    check "guard, exit $attempt, $order: the step's own warning after the block works" \
      "$(is "$(count "$got" "^$order	stderr	warning	own-after\$")" 1)"
  done
  check "guard, exit $attempt: the command's exit status comes back" \
    "$(is "$(cat "$work/guard-$attempt.status")" "$attempt")"
  check "guard, exit $attempt: nothing reaches stdout, so no block can be on it" \
    "$(is "$(wc -c <"$work/guard-$attempt.out")" 0)"
  check "guard, exit $attempt: the client's stdout text is in the log (on stderr)" \
    "$(contains "$(cat "$work/guard-$attempt.err")" 'planted-out-legacy')"
done

# Under bash -e a failing command would end the shell before the close if the guard ran it as a
# plain command; the step's last line runs only if the shell is still there.
step guard-errexit "
  ci_log_guard '$work/client' 9 || true
  ci_log_guard '$work/client' 9
  echo '::warning::never-reached' >&2"
got=$(parse "$work/guard-errexit.out" "$work/guard-errexit.err")
check "guard under bash -e: a failed command still closes its block" \
  "$(is "$(count "$got" STOPPED-AT-END)" 0)"
check "guard under bash -e: the step fails with the command's status" \
  "$(is "$(cat "$work/guard-errexit.status")" 9)"
check "guard under bash -e: the step stops at the failed command" \
  "$(is "$(count "$got" never-reached)" 0)"

# Two broken copies of the library, so the cases above can fail: one whose close does not start a
# line of its own, after a body with no newline at its end, and one that leaves the command's
# stdout on stdout.
real_lib=$lib
lib="$work/close-no-newline.sh"
# shellcheck disable=SC2016,SC2028 # the function's text, written into the copy unexpanded
{ cat "$real_lib"; echo 'ci_log_block_close() { printf "::%s::\n" "$1" >&2; }'; } >"$lib"
step mutant-close "
  ci_log_guard '$work/client' 0
  echo '::warning::own-after' >&2"
got=$(parse "$work/mutant-close.out" "$work/mutant-close.err")
check "control: a close glued to the body's last line leaves the step stopped" \
  "$(is "$(count "$got" "^per-stream	stderr	STOPPED-AT-END")" 1)"
check "control: a close glued to the body's last line drops the step's own warning after it" \
  "$(is "$(count "$got" own-after)" 0)"
lib="$work/stdout-left.sh"
{ cat "$real_lib"; echo 'ci_log_guard() { ci_log_guard_stderr "$@"; }'; } >"$lib"
step mutant-stdout "ci_log_guard '$work/client' 0"
got=$(parse "$work/mutant-stdout.out" "$work/mutant-stdout.err")
for order in per-stream stderr-first stdout-first; do
  check "control: a guard that leaves stdout alone lets planted-out-legacy through, $order" \
    "$(is "$(count "$got" "^$order	stdout	warning	planted-out-legacy\$")" 1)"
done
lib=$real_lib

echo
echo "ci_log_guard_stderr: a captured stdout is a value with no marker in it"

for attempt in 0 7; do
  step "value-$attempt" "
    v=\$(ci_log_guard_stderr '$work/client' $attempt) && rc=0 || rc=\$?
    printf '%s' \"\$v\" >'$work/value-$attempt.value'
    echo '::warning::own-after' >&2
    exit \$rc"
  got=$(parse "$work/value-$attempt.out" "$work/value-$attempt.err")
  check "stderr guard, exit $attempt: the parser sees no planted command in any order" \
    "$(is "$(count "$got" planted)" 0)"
  check "stderr guard, exit $attempt: the step's own warning after the block works" \
    "$(is "$(count "$got" "^per-stream	stderr	warning	own-after\$")" 1)"
  check "stderr guard, exit $attempt: the command's exit status comes back" \
    "$(is "$(cat "$work/value-$attempt.status")" "$attempt")"
  "$work/client" "$attempt" 2>/dev/null >"$work/value-$attempt.expected" || true
  check "stderr guard, exit $attempt: the captured value is the command's stdout, byte for byte" \
    "$(is "$(cmp -s "$work/value-$attempt.value" "$work/value-$attempt.expected" && echo same)" same)"
done

# The stderr guard leaves stdout where the caller sends it: here a file, as retry_fetch sends it.
step value-file "ci_log_guard_stderr '$work/client' 0 >'$work/value-file.value'"
check "stderr guard to a file: the file holds no marker" \
  "$(is "$(grep -c -e 'stop-commands' -e '^::[0-9a-f]\{32\}::$' "$work/value-file.value" || true)" 0)"

echo
echo "The pieces"

t1=$(bash -c ". '$lib'; ci_log_token")
t2=$(bash -c ". '$lib'; ci_log_token")
check "ci_log_token prints 32 lowercase hex characters" \
  "$(contains "$t1" '^[0-9a-f]\{32\}$')"
check "ci_log_token prints a fresh token each time" "$(is "$([ "$t1" != "$t2" ] && echo fresh)" fresh)"

step two-blocks "ci_log_guard true; ci_log_guard true"
mapfile -t opens < <(grep -o '^::stop-commands::[0-9a-f]*' "$work/two-blocks.err" || true)
check "each guarded command opens a block with a token of its own" \
  "$(is "$([ "${#opens[@]}" -eq 2 ] && [ "${opens[0]}" != "${opens[1]}" ] && echo two)" two)"

step bad-open "ci_log_block_open warning"
check "ci_log_block_open refuses a token that is a command name" \
  "$(is "$(cat "$work/bad-open.status")" 2)"
check "ci_log_block_open writes no marker for a refused token" \
  "$(is "$(grep -c '^::' "$work/bad-open.err" || true)" 0)"
step bad-close "ci_log_block_close ''"
check "ci_log_block_close refuses an empty token" "$(is "$(cat "$work/bad-close.status")" 2)"
step no-command "ci_log_guard"
check "ci_log_guard refuses to run no command" "$(is "$(cat "$work/no-command.status")" 2)"
step no-command-stderr "ci_log_guard_stderr"
check "ci_log_guard_stderr refuses to run no command" "$(is "$(cat "$work/no-command-stderr.status")" 2)"

# Sourcing and calling leave the caller's shell as it was: no option set, no variable left. The
# two controls are copies of the library that leave a variable and that set an option.
cat >"$work/state.sh" <<'SH'
before= after=
snap() { set -o; shopt; compgen -v | grep -v -x -e _ -e BASH_COMMAND -e BASH_LINENO \
  -e BASH_SOURCE -e FUNCNAME -e LINENO -e PIPESTATUS -e SECONDS -e RANDOM -e SRANDOM \
  -e EPOCHSECONDS -e EPOCHREALTIME -e BASH_ARGC -e BASH_ARGV -e BASHPID -e COLUMNS -e LINES; }
before=$(snap)
. "$1"
ci_log_guard true 2>/dev/null
ci_log_guard_stderr true 2>/dev/null
ci_log_block_open "$(ci_log_token)" 2>/dev/null
after=$(snap)
if [ "$before" = "$after" ]; then echo same; else echo changed; fi
SH
{ cat "$lib"; echo 'ci_log_guard() { leaked=1; }'; } >"$work/leaks-variable.sh"
{ cat "$lib"; echo 'set -o pipefail'; } >"$work/sets-option.sh"
check "control: the shell-state check sees a variable left behind" \
  "$(is "$(bash "$work/state.sh" "$work/leaks-variable.sh")" changed)"
check "control: the shell-state check sees an option set" \
  "$(is "$(bash "$work/state.sh" "$work/sets-option.sh")" changed)"
check "sourcing and calling set no option and leave no variable" \
  "$(is "$(bash "$work/state.sh" "$lib")" same)"

summary "ci-log-guard.sh"
