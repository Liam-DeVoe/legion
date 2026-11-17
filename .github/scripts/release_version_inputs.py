"""The release lines check-legion-version-inputs.sh holds to what go says their builds read, and
what it asks go: one listing per program, target and cgo setting each release builds.
check-legion-version-inputs.sh runs these listings, and ci_job_reads.py turns the same records into
the Runs of the ci-filters gate, so the go commands that check runs and the ones the filter check
accounts for are one plan, kept here.

  legion_listings(targets)     ./cmd/legion in packages/daemon for each os/arch of
                               .github/go-release-targets.json's `legion` list, under
                               CGO_ENABLED=0, as release.yaml's Build legion step compiles it: a
                               file one target's build constraints alone compile can import a
                               package no other target links.
  legion_envoy_listings(arches, client_targets)
                               the archives release-envoy-listener.yaml compiles on the runner, in
                               packages/envoy: envoy-listener for linux and each arch of the
                               `envoy` list under CGO_ENABLED=0 and 1 both, since the build job
                               leaves cgo to the runner; agent-secrets and agent-secrets-helper for
                               the same under CGO_ENABLED=0; and agent-secrets for each os/arch of
                               .github/agent-secrets-client-targets.json under CGO_ENABLED=0.
  VERSION_STEPS                each step that hands a line's paths to release-push.sh, with the
                               variables that hold those paths and the run: it must be, character
                               for character after YAML has read it. Exact text is the rule
                               because shell that reads like the command may not run it: an
                               escaped `\\$`, a `${{ }}` line GitHub renders into an assignment, or
                               any added line can keep the paths from release-push.sh, and a
                               reader of shell finds one more such form each time.
  USES_BEFORE_VERSION_STEPS    the actions a step before one of those may run, each at the pin the
                               workflows use: an action is code this check does not read, and one
                               can write $GITHUB_ENV or $GITHUB_PATH for every later step. A bump
                               of one of these pins in those jobs is an edit here too.

Pure: each caller reads the target lists its own way and hands them in, and keeps its own
annotations and its own handling of a failure.
"""

from __future__ import annotations

from typing import NamedTuple

DAEMON = "packages/daemon"
ENVOY = "packages/envoy"
RELEASE = ".github/workflows/release.yaml"
LISTENER = ".github/workflows/release-envoy-listener.yaml"
LEGION_ARGS = "LEGION_VERSION_ARGS"
LEGION_BLOCK = "LEGION_GO_INPUTS"
ENVOY_ARGS = "LEGION_ENVOY_VERSION_ARGS"
USES_BEFORE_VERSION_STEPS = (
    "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
    "docker/login-action@dbcb813823bdd20940b903addbd779551569679f",
)


class VersionStep(NamedTuple):
    """A step that hands a line's paths to release-push.sh: where it is, the workflow-level
    variables that hold the paths, and its whole run: text."""

    workflow: str
    job: str
    step_id: str
    variables: tuple[str, ...]
    run: str


VERSION_STEPS = (
    VersionStep(RELEASE, "legion_build", "version", (LEGION_ARGS, LEGION_BLOCK), (
        "# shellcheck disable=SC2086 # the two are the argument list, split on purpose\n"
        '.github/scripts/release-push.sh version legion-v $LEGION_VERSION_ARGS $LEGION_GO_INPUTS >> "$GITHUB_OUTPUT"\n'
    )),
    VersionStep(LISTENER, "release_build", "version", (ENVOY_ARGS,), (
        "# shellcheck disable=SC2086 # $LEGION_ENVOY_VERSION_ARGS is the argument list, split on purpose\n"
        '.github/scripts/release-push.sh version legion-envoy-v $LEGION_ENVOY_VERSION_ARGS >> "$GITHUB_OUTPUT"\n'
    )),
    VersionStep(LISTENER, "release_publish", "confirm", (ENVOY_ARGS,), (
        "status=0\n"
        "# shellcheck disable=SC2086 # $LEGION_ENVOY_VERSION_ARGS is the argument list, split on purpose\n"
        '.github/scripts/release-push.sh version legion-envoy-v --expect "$VERSION" $LEGION_ENVOY_VERSION_ARGS'
        " >/dev/null || status=$?\n"
        "case $status in\n"
        '  0) echo "release=true" >> "$GITHUB_OUTPUT" ;;\n'
        "  3) ;;\n"
        '  4) .github/scripts/release-push.sh restart release-envoy-listener.yaml "$LOST_RACES" ;;\n'
        '  *) exit "$status" ;;\n'
        "esac\n"
    )),
)


class Listing(NamedTuple):
    """One `go list -deps`: the module directory it runs in, its packages, the environment it runs
    under and how a message names it."""

    workdir: str
    packages: tuple[str, ...]
    env: dict[str, str]
    label: str


def legion_listings(targets: list[str]) -> list[Listing]:
    out = []
    for target in targets:
        goos, goarch = target.split("/")
        out.append(Listing(DAEMON, ("./cmd/legion",), {"GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0"},
                           f"./cmd/legion for {target}"))
    return out


def legion_envoy_listings(arches: list[str], client_targets: list[str]) -> list[Listing]:
    out = []
    for arch in arches:
        for cgo in ("0", "1"):
            out.append(Listing(ENVOY, ("./cmd/listener",), {"GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": cgo},
                               f"./cmd/listener for linux/{arch} with CGO_ENABLED={cgo}"))
        out.append(Listing(ENVOY, ("./cmd/agent-secrets", "./cmd/agent-secrets-helper"),
                           {"GOOS": "linux", "GOARCH": arch, "CGO_ENABLED": "0"},
                           f"./cmd/agent-secrets and ./cmd/agent-secrets-helper for linux/{arch}"))
    for target in client_targets:
        goos, goarch = target.split("/")
        out.append(Listing(ENVOY, ("./cmd/agent-secrets",), {"GOOS": goos, "GOARCH": goarch, "CGO_ENABLED": "0"},
                           f"./cmd/agent-secrets for {target}"))
    return out
