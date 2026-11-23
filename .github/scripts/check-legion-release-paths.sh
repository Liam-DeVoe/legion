#!/usr/bin/env bash
# release.yaml's version step for the Go `legion` reads only the paths $LEGION_VERSION_ARGS names
# after its `--`: a commit that changes none of them releases no `legion`, and its release archive
# and the tmux daemon's binary keep the code before it. Those paths are written by hand
# (.github/ci-notes/release.md, "The Go legion's version arguments and the command that prints its
# envoy packages"), and a package the binary comes to link through packages/daemon's `replace` on
# ../envoy is one a hand forgets: packages/envoy/programfile, the writer of every program file the
# daemon runs, was linked for a whole pull request before anyone saw it missing.
#
# So this check runs that note's own command, `go list -deps ./cmd/legion` in packages/daemon, and
# fails naming each package of this repository it prints whose directory no path of the list
# covers, and each module of this repository it reaches whose go.mod or go.sum no path covers. A
# path covers what git's pathspec would: itself, and everything below it when it names a
# directory. `:(exclude)<path>` takes that path back out; any other pathspec magic is refused,
# since the check would not read it as release-push.sh's git does.
#
# It fails, too, when `go list` fails, when release.yaml has no LEGION_VERSION_ARGS or no `--` in
# it, and when `go list` printed no package of this repository: a check that compared nothing
# would read the same as one that found nothing missing.
#
# Run from anywhere: .github/scripts/check-legion-release-paths.sh
# CI runs it in ci.yaml's lint job (after setup-go), which holds no credential, and its tests in
# ci.yaml's test job.
set -euo pipefail

cd "$(dirname "$0")/../.."

errors=$(mktemp)
trap 'rm -f "$errors"' EXIT

# Each package go list prints that is not of the standard library: its directory, a tab, and the
# root directory of the module it belongs to.
format='{{if not .Standard}}{{.Dir}}{{"\t"}}{{with .Module}}{{.Dir}}{{end}}{{"\n"}}{{end}}'
if ! listed=$(cd packages/daemon && go list -deps -f "$format" ./cmd/legion 2>"$errors"); then
  echo "::error file=packages/daemon/cmd/legion::go list -deps ./cmd/legion failed in packages/daemon, so the packages the legion binary links are unknown:" >&2
  cat "$errors" >&2
  exit 1
fi

LISTED=$listed python3 <<'PY'
import os
import sys
from pathlib import Path

import yaml

RELEASE = Path(".github/workflows/release.yaml")
ROOT = Path.cwd().resolve()


def fatal(message: str) -> None:
    print(f"::error file={RELEASE}::{message}", file=sys.stderr)
    raise SystemExit(1)


try:
    document = yaml.safe_load(RELEASE.read_text(encoding="utf-8"))
except FileNotFoundError:
    fatal(f"{RELEASE} is missing")
arguments = ((document or {}).get("env") or {}).get("LEGION_VERSION_ARGS")
if not isinstance(arguments, str):
    fatal(f"{RELEASE} has no env LEGION_VERSION_ARGS, the paths whose change releases the Go legion")
words = arguments.split()
if "--" not in words:
    fatal(f"{RELEASE}'s LEGION_VERSION_ARGS has no `--`, after which release-push.sh's version reads the paths")
specs = words[words.index("--") + 1:]

covers, excludes = [], []
for spec in specs:
    if spec.startswith(":(exclude)"):
        excludes.append(spec[len(":(exclude)"):])
    elif spec.startswith(":"):
        fatal(f"{RELEASE}'s LEGION_VERSION_ARGS names {spec}, a pathspec magic this check does not read")
    else:
        covers.append(spec)


def under(path: str, spec: str) -> bool:
    spec = spec.rstrip("/")
    return path == spec or path.startswith(spec + "/")


def covered(path: str) -> bool:
    return any(under(path, spec) for spec in covers) and not any(under(path, spec) for spec in excludes)


def ours(directory: str):
    """directory relative to the repository's root, or None for one outside it."""
    try:
        return Path(directory).resolve().relative_to(ROOT).as_posix()
    except ValueError:
        return None


packages, modules = set(), set()
for line in os.environ["LISTED"].splitlines():
    if not line.strip():
        continue
    directory, _, module = line.partition("\t")
    if (package := ours(directory)) is not None:
        packages.add(package)
        if module and (root := ours(module)) is not None:
            modules.add(root)
if not packages:
    fatal("go list -deps ./cmd/legion in packages/daemon printed no package of this repository, so this check compared nothing")

missing = sorted(f"{package}/" for package in packages if not covered(package))
for module in sorted(modules):
    for name in ("go.mod", "go.sum"):
        path = f"{module}/{name}"
        if (ROOT / path).exists() and not covered(path):
            missing.append(path)
if missing:
    fatal(
        f"{RELEASE}'s LEGION_VERSION_ARGS names no path that covers {len(missing)} of what the legion binary links "
        f"(go list -deps ./cmd/legion in packages/daemon), so a change to it alone would release no legion: "
        + ", ".join(missing)
        + ". Add each after the `--` (.github/ci-notes/release.md, \"The Go legion's version arguments and the command "
        "that prints its envoy packages\")."
    )
print(
    f"{RELEASE}'s LEGION_VERSION_ARGS covers all {len(packages)} packages of this repository that "
    f"go list -deps ./cmd/legion prints, and the go.mod and go.sum of their {len(modules)} modules"
)
PY
