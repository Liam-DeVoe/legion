#!/usr/bin/env python3
"""Split one Go package's tests into shards by name, and prove that the shards' `-run` patterns
select every test of the package exactly once.

It exists for envoy-go-race-api (dispatch://INFRA-3904). `go test -race ./internal/dispatch/api/`
took 913 s to 1,527 s of its 30-minute `-timeout` on one day's runners, so whether the job passed
depended on how fast the runner was. The job is now a matrix, and each leg runs one shard of the
package. A shard is a set of top-level test names, written as the anchored alternation
`go test -run` takes, `^(TestA|TestB|…)$`.

Usage:

    go-test-shards.py pattern <index> <total> [<build flag>...] <package>
    go-test-shards.py check [<build flag>...] <package> -- <pattern>...

`pattern` prints the pattern of shard <index> (0 to <total> - 1) and nothing else on standard
output, after it has run `check` over all <total> patterns. A leg's step assigns it on a line of
its own and passes it to `go test -run`, so under `bash -e` a refusal fails the step and never
runs `-run ""`, which matches every test:

    pattern=$(../../.github/scripts/go-test-shards.py pattern ${{ strategy.job-index }} ${{ strategy.job-total }} -race ./internal/dispatch/api/)
    go test -count=1 -race -timeout 30m -run "$pattern" ./internal/dispatch/api/

The leg's index and the leg count are the matrix's own (`strategy.job-index`,
`strategy.job-total`), so a leg added to the matrix changes the denominator with it, and no leg
can be left out of the split.

`check` proves a set of patterns, given one per shard, select each test exactly once. A test no
pattern selects runs in no leg, and a test two patterns select runs twice; it names each one.
`pattern` runs it over the patterns it made, in every leg, before the leg runs its tests.

A pattern may hold no `/`. `-test.list` matches each top-level name against the whole pattern,
but `go test -run` splits the pattern at every `/` outside a bracket expression, matches the
first part against the top-level name and starts a test whose name matches it partly, for the
subtests the rest may select. So `^TestAlpha$` and `^TestBeta$|^TestAlpha$/child` list TestAlpha
once and run it in both legs. `check` fails such a pattern, naming it, before it lists anything.
The patterns `pattern` makes are alternations of top-level names, which hold no `/`.

Where the names come from. Both commands build the package's test binary with
`go test -c` under the build flags given (`-race`, `-tags T`), which are the flags the leg's own
`go test` uses, then ask the binary itself: `-test.list .` for every name, and `-test.list
<pattern>` for the names one pattern selects. The binary is the truth about which tests exist in
that build: the race build of internal/dispatch/api leaves out the files that carry
`//go:build !race`, and a reading of the source would have to agree with the go tool about every
constraint to know that. The patterns are matched by Go's own regexp in the binary, so `check`
proves what `go test -run` will do, not what a second engine thinks it would do. The build is the
one the leg's `go test` makes next, so that `go test` takes every compiled package from the build
cache and links once more; nothing is compiled twice.

Which shard a test is in. A test is in shard `crc32(name) mod total`, the IEEE CRC-32 that
Python's zlib and Go's hash/crc32 both compute. The assignment needs no list kept by hand: a new
test lands in exactly one shard the first time a leg runs, and a test stays in its shard until it
is renamed or the leg count changes. A list of measured durations would balance the shards more
closely, but it goes stale as tests are added and needs regenerating; the hash needs nothing.
The legs' balance is measured, not assumed (the comment on the matrix in
.github/workflows/ci-job-envoy-go-race-api.yaml gives the numbers).

The names in the split are every name `-test.list .` prints but benchmarks: `Test…`, `Fuzz…` and
`Example…`. `-test.list` prints an example only when it has an output comment, which is when
`go test` runs it, and `-run` never starts a benchmark. TestMain is never printed.

Refusals, each on standard error with nothing on standard output: a usage error, an index outside
the leg count, a build or listing that fails, a package with no test, a shard with no test (more
legs than the package needs), and a pattern longer than 131,071 bytes, which Linux refuses as one
argument (MAX_ARG_STRLEN), so `go test` could not start the binary with it; add a leg.

Exit status: 0 when every test is in exactly one shard, 1 when a test is in no shard or in two,
or a pattern holds a `/`, 2 for every refusal above.

Run from the directory the package path is relative to, as `go test` is: the leg runs it in
packages/envoy. Needs python3 and go on PATH; `-race` needs cgo and a C compiler, as it does for
`go test`. Its tests are in go-test-shards.test.sh, which ci.yaml's test job runs.
"""

from __future__ import annotations

import contextlib
import re
import subprocess
import sys
import tempfile
import zlib
from pathlib import Path

USAGE = """\
usage: go-test-shards.py pattern <index> <total> [<build flag>...] <package>
       go-test-shards.py check [<build flag>...] <package> -- <pattern>...
A <pattern> may hold no `/`: go test -run splits it there, which -test.list does not."""

# A name `-test.list` prints that `-run` can start: a top-level test, fuzz target or example.
# Benchmarks, which it also prints, are left out (`-run` never starts one), and a line with a space
# in it (a TestMain that prints a sentence) names no test. After the prefix the name is any run of
# non-space characters, not a character class: the binary printed it, so Go has already decided it
# is a test, and Python's own Unicode tables are older than Go's (Python 3.12 has Unicode 15.0, Go
# 1.27 17.0), so `TestᲉ` (U+1C89, Unicode 16.0), which go test runs, failed `\w` and was in no
# shard while the proof, reading the same listing, passed. A TestMain that prints one word starting
# with Test is read as a test too; it then lists under every pattern, and the proof fails it by
# name as a test in every shard.
SPLIT = re.compile(r"^(Test|Fuzz|Example)\S*$")
# Linux's MAX_ARG_STRLEN, 32 pages of 4 KiB: the longest single argument exec accepts, terminator
# included. The pattern is one argument to go test and one to the test binary.
MAX_ARGUMENT = 131072


class Refused(Exception):
    """A refusal: exit 2, its message on standard error."""


class Uncovered(Exception):
    """Tests in no shard or in two: exit 1, one line per test on standard error."""


def shard_of(name: str, total: int) -> int:
    """The shard, 0 to total - 1, that a test of this name is in."""
    return zlib.crc32(name.encode()) % total


def alternation(names) -> str:
    """The -run pattern that selects exactly these names, sorted."""
    return "^(" + "|".join(sorted(names)) + ")$"


def build(flags: list[str], package: str, directory: Path) -> Path:
    """The package's test binary, built under the flags into directory."""
    binary = directory / "package.test"
    command = ["go", "test", "-c", "-o", str(binary), *flags, package]
    done = subprocess.run(command, capture_output=True, encoding="utf-8", check=False)
    if done.returncode != 0:
        raise Refused(f"`{' '.join(command)}` exited {done.returncode}:\n{done.stderr}{done.stdout}")
    if not binary.is_file():
        raise Refused(f"`{' '.join(command)}` wrote no test binary: {package} has no test files")
    return binary


def listed(binary: Path, pattern: str) -> list[str]:
    """The names of the split the binary lists for one pattern."""
    if len(pattern.encode()) >= MAX_ARGUMENT:
        raise Refused(f"a pattern of {len(pattern.encode())} bytes is longer than one argument may be "
                      f"({MAX_ARGUMENT - 1} bytes, MAX_ARG_STRLEN), so go test could not pass it to the "
                      f"test binary; add a leg to the matrix, which shortens every shard's pattern")
    # UTF-8 whatever the locale says, since a test's name may hold any letter Go allows.
    done = subprocess.run([str(binary), f"-test.list={pattern}"], capture_output=True, encoding="utf-8",
                          check=False)
    if done.returncode != 0:
        raise Refused(f"`{binary.name} -test.list` exited {done.returncode}:\n{done.stderr}{done.stdout}")
    return [line for line in done.stdout.splitlines() if SPLIT.match(line)]


@contextlib.contextmanager
def package_tests(flags: list[str], package: str, purpose: str):
    """The package's test binary, built under the flags, and every name of the split it lists,
    for as long as the block runs; refused when it lists none."""
    with tempfile.TemporaryDirectory(prefix="go-test-shards.") as directory:
        binary = build(flags, package, Path(directory))
        everything = listed(binary, ".")
        if not everything:
            raise Refused(f"{package} lists no test, so there is nothing to {purpose}")
        yield binary, everything


def prove(binary: Path, everything: list[str], patterns: list[str]) -> None:
    """Raise Uncovered naming each test of `everything` that no pattern, or more than one,
    selects in the binary; or, before it lists anything, each pattern that holds a `/`, whose
    selection -test.list cannot show (the module's head says why)."""
    total = len(patterns)
    slashed = [f"pattern {number} of {total}, `{pattern}`, holds a `/`: go test -run splits it there and "
               f"starts every test whose name matches the part before it, which -test.list does not show, "
               f"so no listing can prove what it runs; a shard's pattern may hold no `/`"
               for number, pattern in enumerate(patterns, start=1) if "/" in pattern]
    if slashed:
        raise Uncovered("\n".join(slashed))
    shards_of: dict[str, list[int]] = {name: [] for name in everything}
    for number, pattern in enumerate(patterns, start=1):
        for name in listed(binary, pattern):
            shards_of.setdefault(name, []).append(number)
    problems = []
    for name in sorted(shards_of):
        shards = shards_of[name]
        if not shards:
            problems.append(f"{name} is in no shard: none of the {total} patterns selects it, so no leg runs it")
        elif len(shards) > 1:
            numbers = " and ".join(str(n) for n in shards)
            problems.append(f"{name} is in shards {numbers} of {total}: each of those legs runs it")
    if problems:
        raise Uncovered("\n".join(problems))


def split_package(arguments: list[str]) -> tuple[list[str], str]:
    """The build flags and the package: the last argument is the package."""
    if not arguments:
        raise Refused(f"no package given\n{USAGE}")
    package = arguments[-1]
    if package.startswith("-"):
        raise Refused(f"the last argument, {package}, is a flag, not a package\n{USAGE}")
    return arguments[:-1], package


def pattern_command(arguments: list[str]) -> str:
    if len(arguments) < 3:
        raise Refused(USAGE)
    try:
        index, total = int(arguments[0]), int(arguments[1])
    except ValueError:
        raise Refused(f"the index and the total must be whole numbers, got {arguments[0]!r} and {arguments[1]!r}\n{USAGE}")
    if total < 1 or not 0 <= index < total:
        raise Refused(f"index {index} is not a shard of {total}: an index runs from 0 to {total - 1}")
    flags, package = split_package(arguments[2:])
    with package_tests(flags, package, "split") as (binary, everything):
        shards: list[list[str]] = [[] for _ in range(total)]
        for name in everything:
            shards[shard_of(name, total)].append(name)
        empty = [str(number + 1) for number, names in enumerate(shards) if not names]
        if empty:
            raise Refused(f"shard(s) {', '.join(empty)} of {total} hold no test of the {len(everything)} "
                          f"{package} lists: the matrix has more legs than this package needs")
        patterns = [alternation(names) for names in shards]
        prove(binary, everything, patterns)
    print(f"go-test-shards.py: shard {index + 1} of {total} holds {len(shards[index])} of the "
          f"{len(everything)} tests of {package}; every test is in exactly one of the {total} shards",
          file=sys.stderr)
    return patterns[index]


def check_command(arguments: list[str]) -> None:
    if "--" not in arguments:
        raise Refused(f"no `--` before the patterns\n{USAGE}")
    cut = arguments.index("--")
    flags, package = split_package(arguments[:cut])
    patterns = arguments[cut + 1:]
    if not patterns:
        raise Refused(f"no pattern after `--`\n{USAGE}")
    with package_tests(flags, package, "check") as (binary, everything):
        prove(binary, everything, patterns)
    print(f"go-test-shards.py: every one of the {len(everything)} tests of {package} is in exactly one "
          f"of the {len(patterns)} shards", file=sys.stderr)


def main(arguments: list[str]) -> int:
    try:
        if arguments[:1] == ["pattern"]:
            print(pattern_command(arguments[1:]))
        elif arguments[:1] == ["check"]:
            check_command(arguments[1:])
        else:
            raise Refused(USAGE)
    except Refused as refusal:
        print(f"go-test-shards.py: {refusal}", file=sys.stderr)
        return 2
    except Uncovered as uncovered:
        for line in str(uncovered).splitlines():
            print(f"go-test-shards.py: {line}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
