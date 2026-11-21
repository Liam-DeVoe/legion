"""Go's own answers to what `go test -run` makes of a pattern, for go-test-step-expansion.py.

That script learns what a step runs from `go test -list`, which matches each top-level name
against a pattern as written, while `go test -run` (and -skip) first splits the pattern and
rewrites each part; a pattern the two read apart would leave the script wrong about what the step
runs, so it refuses one. This module says when they can: split_disagreement reads the split as
testing/match.go's splitRegexp does, and rewritten_runes asks the step's own Go toolchain which
runes the rewrite changes (REWRITE_PROBE, built with the toolchain go_root finds), since Python's
Unicode tables are older than Go's. Refused and indent, which both files raise and format with,
are here too, so that this module needs nothing of that script's.

go-test-step-expansion.py imports it from the directory both live in, as it imports
ci_filter_coverage: on PYTHONPATH, with no bytecode written.
"""

from __future__ import annotations

import subprocess
import tempfile
from pathlib import Path

from ci_filter_coverage import go_environment


class Refused(Exception):
    """A shape go-test-step-expansion.py will not guess at."""


def indent(text: str) -> str:
    return "".join(f"    | {line}\n" for line in (text or "").strip().splitlines())


# A program that applies testing's rewrite rule to every rune of its standard input, one pattern
# per line, and prints each rune the rule changes as U+XXXX: match.go's rewrite turns a rune its
# isSpace names into `_` and a rune strconv.IsPrint calls unprintable into its escape. isSpace is
# match.go's own list, copied here byte for byte (go-test-step-expansion.test.sh holds the copy to
# the pinned toolchain's match.go); strconv.IsPrint is that toolchain's own table. Python decides
# none of it: its Unicode tables are older than Go's (Python 3.12 has 15.0, Go 1.27 17.0), so its
# isprintable() called U+1C89, a letter Go prints, unprintable.
REWRITE_PROBE = """\
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
)

func isSpace(r rune) bool {
	if r < 0x2000 {
		switch r {
		// Note: not the same as Unicode Z class.
		case '\\t', '\\n', '\\v', '\\f', '\\r', ' ', 0x85, 0xA0, 0x1680:
			return true
		}
	} else {
		if r <= 0x200a {
			return true
		}
		switch r {
		case 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
			return true
		}
	}
	return false
}

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<24)
	for in.Scan() {
		for _, r := range in.Text() {
			if isSpace(r) || !strconv.IsPrint(r) {
				fmt.Printf("%U\\n", r)
			}
		}
	}
}
"""
# What the probe answered, per toolchain GOROOT and per rune, so each rune is asked once a run.
_PROBED: dict[tuple[str, str], bool] = {}


def go_root(directory: Path) -> str:
    """The GOROOT of the toolchain go selects in `directory`, which is the toolchain a step's own
    go test runs there (go.work's go or toolchain line, or go.mod's outside a workspace, may select
    another than the one on PATH).
    It runs under go_environment(), so a caller's GOROOT or GOTOOLCHAIN does not choose it; that
    drops setup-go's GOTOOLCHAIN=local too, so where go.work asks for a newer go than the one
    installed, this command downloads it, in CI as well. go's standard error therefore goes to this
    script's as go writes it, so a download that stalls is named while it waits; standard output,
    the root, is kept."""
    done = subprocess.run(["go", "env", "GOROOT"], cwd=directory, env=go_environment(),
                          stdout=subprocess.PIPE, encoding="utf-8", check=False)
    if done.returncode != 0:
        raise Refused(f"go env GOROOT in {directory} exited {done.returncode}; its standard error is above")
    return done.stdout.strip()


def rewritten_runes(pattern: str, directory: Path) -> list[str]:
    """The characters of `pattern` that testing's matcher rewrites before it matches, decided by
    Go's rule. An ASCII character is decided here, since the rule needs no table for it: of ASCII,
    isSpace names \\t \\n \\v \\f \\r and the space, and strconv.IsPrint calls 0x20-0x7E printable.
    Every other rune is asked of REWRITE_PROBE, built and run once per run with the toolchain go
    selects in `directory`: its own bin/go, under go_environment() (none of the caller's Go
    variables, no go env file) with GOROOT set to that toolchain's root, GOTOOLCHAIN=local so that
    it selects no other, and GOWORK=off for the scratch directory it runs in. A caller's GOROOT of
    another Go - the one a version manager's python3 shim hands this interpreter - would otherwise
    reach that go, which then refuses its own compiler (`compile: version "go1.27.0" does not match
    go tool version "go1.27.3"`)."""
    def ascii_rewritten(c: str) -> bool:
        return c in "\t\n\v\f\r " or not 0x20 <= ord(c) <= 0x7E
    root = None
    unasked = sorted({c for c in pattern if ord(c) > 0x7F})
    if unasked:
        root = go_root(directory)
        unasked = [c for c in unasked if (root, c) not in _PROBED]
    if unasked:
        with tempfile.TemporaryDirectory(prefix="go-test-step-expansion.") as scratch:
            Path(scratch, "probe.go").write_text(REWRITE_PROBE)
            done = subprocess.run([str(Path(root, "bin", "go")), "run", "probe.go"], cwd=scratch,
                                  input="".join(unasked) + "\n", capture_output=True, encoding="utf-8",
                                  env={**go_environment(), "GOROOT": root, "GOTOOLCHAIN": "local",
                                       "GOWORK": "off"},
                                  check=False)
        if done.returncode != 0:
            raise Refused(f"the rewrite probe exited {done.returncode} under {root}\n{indent(done.stderr)}")
        answered = {chr(int(line.removeprefix("U+"), 16)) for line in done.stdout.split()}
        for c in unasked:
            _PROBED[(root, c)] = c in answered
    return sorted({c for c in pattern if (ascii_rewritten(c) if ord(c) <= 0x7F else _PROBED[(root, c)])})


def split_disagreement(pattern: str) -> str | None:
    """Why `go test -list` and `go test -run` (or -skip) can split `pattern` differently, or None
    when they cannot. -list matches each top-level name against the whole pattern; -run first
    splits it (match.go's splitRegexp, mirrored below) at every `/` and every `|` outside brackets
    and parentheses. So a pattern holding neither is matched whole by both. What the run then
    rewrites in it is go-test-step-expansion.py's listed_or_rewritten() to decide, once the
    listing is known."""
    if "/" in pattern:
        return "it holds a `/`, at which go test -run splits a pattern and -list does not"
    brackets = parentheses = 0
    index = 0
    while index < len(pattern):
        character = pattern[index]
        if character == "[":
            brackets += 1
        elif character == "]":
            brackets = max(brackets - 1, 0)  # an unmatched `]` is legal
        elif character == "(" and brackets == 0:
            parentheses += 1
        elif character == ")" and brackets == 0:
            parentheses -= 1
        elif character == "\\":
            index += 1
        elif character == "|" and brackets == 0 and parentheses == 0:
            return ("it holds a `|` outside brackets and parentheses, at which go test -run splits a pattern "
                    "and -list does not")
        index += 1
    return None
