#!/usr/bin/env bash
# release.yaml's version step for the Go `legion` reads only the paths $LEGION_VERSION_ARGS names
# after its `--`: a commit that changes none of them releases no `legion`, and its release archive
# and the tmux daemon's binary keep the code before it. Those paths are written by hand
# (.github/ci-notes/release.md, "The Go legion's version arguments and the command that prints its
# envoy packages"), and a package the binary comes to link through packages/daemon's `replace` on
# ../envoy is one a hand forgets: packages/envoy/programfile, the writer of every program file the
# daemon runs, was linked for a whole pull request before anyone saw it missing.
#
# So this check holds the list to what the release builds legion from. It keeps no rule of its
# own: each question goes to the program that answers it for the release.
#   * The words of LEGION_VERSION_ARGS: release.yaml's step expands the variable unquoted, in
#     bash, at the repository's root, so bash splits it on the default IFS and expands each word
#     that matches a path of the checkout. This script reads the value with a YAML parser and
#     expands it the same way, in bash, at the root, with IFS and globbing as bash starts them.
#   * Which of those words are the paths: release-push.sh's own parse of `version`'s arguments,
#     which `release-push.sh version-paths` runs and prints.
#   * Which files those paths cover: git, which reads them as pathspecs when release-bump.sh runs
#     `git log ... -- <paths>`. `git ls-files -- <paths>` at the root lists the files they cover,
#     and `git ls-files` the files git tracks.
#   * Which targets legion is built for, with which environment and command:
#     release-build-legion.sh, whose `targets`, `list` and `env` modes run what its build runs.
#   * Which files a target's binary is built from: go. `go list -deps -f` over ./cmd/legion, run
#     through release-build-legion.sh list, names for each package the files it compiles or
#     embeds (GoFiles, CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles, SwigFiles,
#     SwigCXXFiles, SysoFiles and EmbedFiles, in its directory) and its module's go.mod (GoMod),
#     beside which is the module's go.sum. `go env GOWORK`, through release-build-legion.sh env,
#     names the go.work the build reads, beside which is its go.work.sum: in workspace mode its go,
#     godebug, use and replace lines set what the binary builds with.
# A file of those that git does not track is named by no commit, so no path can release a change
# to it, and it is not required.
#
# It fails naming each package directory of this repository with a required file the paths do
# not cover, and each module or workspace file the paths do not cover. It fails, too, when
# release-build-legion.sh cannot read the legion targets or reads none, when go list or go env
# fails for a target, when the go.work the build reads is outside the repository, when
# release.yaml has no LEGION_VERSION_ARGS, when release-push.sh refuses its words, when git
# cannot list the files, and when go list printed no package of this repository: a check that
# compared nothing would read the same as one that found nothing missing.
#
# Run from anywhere: .github/scripts/check-legion-release-paths.sh
# CI runs it in ci.yaml's lint job, which holds no credential, after setup-go and the go-notices
# restore, which it must not run before, when the legion_release_paths filter matches; its tests
# run in ci.yaml's test job.
set -euo pipefail

cd "$(dirname "$0")/../.."

# Every listing goes to python in a file, not in a variable of its environment: the legion binary
# links hundreds of packages, and their module-cache paths overran one variable's limit (128 KiB,
# "Argument list too long") on CI's runner.
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
release=.github/workflows/release.yaml

# The targets the release builds legion for, one <os>/<arch> a line.
if ! .github/scripts/release-build-legion.sh targets >"$work/targets" 2>"$work/errors"; then
  echo "::error file=.github/go-release-targets.json::could not read the legion targets of .github/go-release-targets.json (release-build-legion.sh targets), so the packages the legion binary links are unknown:" >&2
  cat "$work/errors" >&2
  exit 1
fi
if [ ! -s "$work/targets" ]; then
  echo "::error file=.github/go-release-targets.json::.github/go-release-targets.json lists no legion target, so the packages the legion binary links are unknown" >&2
  exit 1
fi

# One line for each package go list prints that is not of the standard library: its directory,
# its module's go.mod, and each file it compiles or embeds, separated by tabs. Every target's
# listing goes to the one file, and the go.work each target's build reads (a path, or empty for
# none found, or off for GOWORK=off) to another.
format='{{if not .Standard}}{{.Dir}}{{"\t"}}{{with .Module}}{{.GoMod}}{{end}}'
for field in GoFiles CgoFiles CFiles CXXFiles MFiles HFiles FFiles SFiles SwigFiles SwigCXXFiles SysoFiles EmbedFiles; do
  format+="{{range .$field}}{{\"\\t\"}}{{.}}{{end}}"
done
format+='{{"\n"}}{{end}}'
: >"$work/listed"
: >"$work/gowork"
while read -r target; do
  if ! .github/scripts/release-build-legion.sh list "$target" -deps -f "$format" </dev/null >>"$work/listed" 2>"$work/errors"; then
    echo "::error file=packages/daemon/cmd/legion::go list -deps ./cmd/legion failed in packages/daemon, so the packages the legion binary links are unknown (target $target):" >&2
    cat "$work/errors" >&2
    exit 1
  fi
  if ! .github/scripts/release-build-legion.sh env "$target" GOWORK </dev/null >>"$work/gowork" 2>"$work/errors"; then
    echo "::error file=packages/daemon/go.mod::go env GOWORK failed in packages/daemon, so the go.work the legion build reads is unknown (target $target):" >&2
    cat "$work/errors" >&2
    exit 1
  fi
done <"$work/targets"

# LEGION_VERSION_ARGS as release.yaml's workflow env holds it.
if ! arguments=$(python3 - "$release" <<'PY'
import sys
from pathlib import Path

import yaml

RELEASE = Path(sys.argv[1])


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
sys.stdout.write(arguments)
PY
); then
  exit 1
fi

# The paths release-push.sh's version reads from the words release.yaml's step gives it: the
# variable unquoted, so split and expanded here as there.
# shellcheck disable=SC2086 # $arguments is the argument list, split and expanded on purpose
if ! .github/scripts/release-push.sh version-paths legion-v $arguments >"$work/paths" 2>"$work/errors"; then
  echo "::error file=$release::release-push.sh refused $release's LEGION_VERSION_ARGS, as the release's version step would, so the paths whose change releases the Go legion are unknown:" >&2
  cat "$work/errors" >&2
  exit 1
fi
mapfile -d '' -t paths <"$work/paths"

# The files the paths cover, and the files git tracks, each ended by a NUL. With no path at all,
# git ls-files lists every file, as git log with none reads every commit.
if ! git ls-files -z -- "${paths[@]}" >"$work/covered" 2>"$work/errors" ||
  ! git ls-files -z >"$work/tracked" 2>>"$work/errors"; then
  echo "::error::git ls-files failed at the repository's root, so the files LEGION_VERSION_ARGS covers are unknown:" >&2
  cat "$work/errors" >&2
  exit 1
fi

python3 - "$work/listed" "$work/gowork" "$work/tracked" "$work/covered" "$(wc -l <"$work/targets")" <<'PY'
import sys
from pathlib import Path, PurePosixPath

LISTED, GOWORK, TRACKED, COVERED = (Path(arg) for arg in sys.argv[1:5])
TARGETS = int(sys.argv[5])
RELEASE = ".github/workflows/release.yaml"
ROOT = Path.cwd().resolve()


def fatal(message: str) -> None:
    print(f"::error file={RELEASE}::{message}", file=sys.stderr)
    raise SystemExit(1)


def text(path: Path) -> str:
    return path.read_bytes().decode("utf-8", "surrogateescape")


def ours(directory: str):
    """directory relative to the repository's root, or None for one outside it."""
    try:
        return Path(directory).resolve().relative_to(ROOT).as_posix()
    except ValueError:
        return None


def file_of(path: str):
    """path, a file, relative to the repository's root, or None for one outside it: its directory
    resolved, its own name kept, as git names it."""
    path = Path(path)
    directory = ours(str(path.parent))
    return None if directory is None else PurePosixPath(directory, path.name).as_posix()


# Each package of this repository, with the files of it the build reads, and the go.mod of each
# module they belong to.
packages, modules = {}, set()
for line in text(LISTED).splitlines():
    if not line.strip():
        continue
    directory, gomod, *files = line.split("\t")
    if (package := ours(directory)) is None:
        continue
    packages.setdefault(package, set()).update(PurePosixPath(package, name).as_posix() for name in files)
    if gomod and (module := file_of(gomod)) is not None:
        modules.add(module)
if not packages:
    fatal("go list -deps ./cmd/legion in packages/daemon printed no package of this repository, so this check compared nothing")

tracked = set(text(TRACKED).split("\0")) - {""}
covered = set(text(COVERED).split("\0")) - {""}


def required(path: str) -> bool:
    return path in tracked


missing = sorted(f"{package}/" for package, files in packages.items()
                 if any(required(path) and path not in covered for path in files))
for gomod in sorted(modules):
    for path in (gomod, PurePosixPath(gomod).with_name("go.sum").as_posix()):
        if required(path) and path not in covered:
            missing.append(path)
# The go.work the build reads binds the modules: in workspace mode its go, godebug, use and
# replace lines set what the binary builds with, and its go.work.sum holds the sums.
workspace = []
for gowork in sorted(set(text(GOWORK).splitlines()) - {"", "off"}):
    if (path := file_of(gowork)) is None:
        fatal(f"the go.work the legion build reads, {gowork}, is outside the repository, so no path of LEGION_VERSION_ARGS "
              "can name it and a change to it alone would release no legion")
    workspace += [name for name in (path, f"{path}.sum") if required(name) and name not in workspace]
missing += [path for path in workspace if path not in covered]
if missing:
    fatal(
        f"{RELEASE}'s LEGION_VERSION_ARGS names no path that covers {len(missing)} of what the legion binary links "
        f"(go list -deps ./cmd/legion in packages/daemon), so a change to it alone would release no legion: "
        + ", ".join(missing)
        + ". Add each after the `--` (.github/ci-notes/release.md, \"The Go legion's version arguments and the command "
        "that prints its envoy packages\")."
    )
bound = f", and {' and '.join(workspace)}, which {'bind' if len(workspace) > 1 else 'binds'} them" if workspace else ""
print(
    f"{RELEASE}'s LEGION_VERSION_ARGS covers every tracked file that the {len(packages)} packages of this repository "
    f"go list -deps ./cmd/legion prints for the {TARGETS} legion targets of .github/go-release-targets.json compile or "
    f"embed, and the go.mod and go.sum of their {len(modules)} modules{bound}"
)
PY
