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

Pure: each caller reads the target lists its own way and hands them in, and keeps its own
annotations and its own handling of a failure.
"""

from __future__ import annotations

from typing import NamedTuple

DAEMON = "packages/daemon"
ENVOY = "packages/envoy"


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
