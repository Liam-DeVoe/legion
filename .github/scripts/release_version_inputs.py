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
                               variables that hold those paths, the run: it must be, character for
                               character after YAML has read it, its env:, key for key, its if:,
                               its job's runs-on:, and the job outputs: that hand its decision on.
                               Exact text is the rule because shell that reads like the command may
                               not run it: an escaped `\\$`, a `${{ }}` line GitHub renders into an
                               assignment, or any added line can keep the paths from
                               release-push.sh, and a reader of shell finds one more such form
                               each time. The runner is pinned because a self-hosted one runs its
                               own hooks and environment before the job's first step. The if: and
                               the outputs are pinned because the decision is the step's only
                               product: an `if: false`, or a job output `skip: "true"` in place of
                               the step's, throws away what the step computed, and the release
                               downstream is skipped all the same.
  STEPS_BEFORE_VERSION_STEPS   every step that may run before one of those in its job: an action
                               at its pin with its with:, or a run: text, and in each case its
                               env:, all exactly. Any other step is code this check does not read,
                               and code can write $GITHUB_ENV or $GITHUB_PATH for every later step
                               however it is spelled, or through a helper it calls; so the jobs
                               are pinned up to and including the version step, and an edit of one
                               of these steps in those jobs is an edit here too.
  WORKFLOW_ENV_KEYS            the variables each workflow's env: may set: every one reaches the
                               version step, and one that a shell, the dynamic loader, git or gh
                               reads (BASH_ENV, PATH, LD_PRELOAD, GIT_DIR, GH_REPO and more) can
                               change what the step hands release-push.sh or what that script
                               reads. A list of what may be set closes that class; a list of what
                               may not would name its members one review at a time.
  JOB_KEYS                     the keys a job holding one of VERSION_STEPS may set: those the three
                               jobs set today. Another key can run code or set the environment
                               before the step - services: (a container with a writable host
                               mount and a health-check command can replace host tools such as
                               git), container:, env:, defaults:, a reusable workflow's uses: -
                               so the class is closed with a list of what may be set, as
                               WORKFLOW_ENV_KEYS closes the variables'.

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
CHECKOUT = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
LOGIN = "docker/login-action@dbcb813823bdd20940b903addbd779551569679f"

WORKFLOW_ENV_KEYS = {
    RELEASE: ("CLAUDE_ENVOY_VERSION_ARGS", "GOPROXY", LEGION_BLOCK, LEGION_ARGS, "PI_ENVOY_VERSION_ARGS",
              "PI_LEGION_VERSION_ARGS"),
    LISTENER: ("GOPROXY", ENVOY_ARGS),
}

JOB_KEYS = ("cache-mode", "concurrency", "if", "needs", "outputs", "permissions", "runs-on", "steps",
            "timeout-minutes")


# The GitHub-hosted runner each checked job runs on: its image is GitHub's, with no hook or
# environment of anyone else's before the job's first step.
RUNNER = "ubuntu-24.04"


# The job outputs a build job hands its version step's decision on with: what the publish steps
# read as needs.<job>.outputs.version and .skip.
DECISION = {
    "version": "${{ steps.version.outputs.version }}",
    "skip": "${{ steps.version.outputs.skip }}",
}


class VersionStep(NamedTuple):
    """A step that hands a line's paths to release-push.sh: where it is, the workflow-level
    variables that hold the paths, its whole run: text, its env: and its if: (None for a step with
    none), its job's runs-on:, and the job outputs that must carry its decision as written here
    (None for a job whose steps read the decision themselves)."""

    workflow: str
    job: str
    step_id: str
    variables: tuple[str, ...]
    run: str
    env: dict[str, str] | None = None
    runs_on: str = RUNNER
    if_: str | None = None
    outputs: dict[str, str] | None = None


VERSION_STEPS = (
    VersionStep(RELEASE, "legion_build", "version", (LEGION_ARGS, LEGION_BLOCK), (
        "# shellcheck disable=SC2086 # the two are the argument list, split on purpose\n"
        '.github/scripts/release-push.sh version legion-v $LEGION_VERSION_ARGS $LEGION_GO_INPUTS >> "$GITHUB_OUTPUT"\n'
    ), outputs=DECISION),
    VersionStep(LISTENER, "release_build", "version", (ENVOY_ARGS,), (
        "# shellcheck disable=SC2086 # $LEGION_ENVOY_VERSION_ARGS is the argument list, split on purpose\n"
        '.github/scripts/release-push.sh version legion-envoy-v $LEGION_ENVOY_VERSION_ARGS >> "$GITHUB_OUTPUT"\n'
    ), outputs=DECISION),
    # The confirm step's decision, release=true, stays in its own job, whose later steps read it.
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
    ), {
        "GH_TOKEN": "${{ github.token }}",
        "LOST_RACES": "${{ inputs.lost_races || 0 }}",
        "VERSION": "${{ needs.release_build.outputs.version }}",
    }, if_="needs.release_build.result == 'success' && needs.release_build.outputs.skip != 'true'"),
)


class ApprovedStep(NamedTuple):
    """A step that may run before one of VERSION_STEPS, by a name messages use: the action it runs
    and that action's with:, or its run: text, and its env:. None where the step has no such key."""

    name: str
    uses: str | None = None
    with_: dict | None = None
    run: str | None = None
    env: dict[str, str] | None = None


# release_publish's "Determine latest" and "Advance latest image" steps, which run before its
# confirm step: raw strings, so each holds the shell exactly as the workflow's block does.
DETERMINE_LATEST = r"""git fetch origin main --tags

image_digest() {
  local sha=$1
  local error_file
  error_file=$(mktemp)
  if IMAGE_DIGEST=$(docker buildx imagetools inspect --format '{{.Manifest.Digest}}' "$IMAGE:$sha" 2>"$error_file"); then
    rm -f "$error_file"
    return 0
  fi
  if grep -Eqi 'manifest unknown|name unknown|not found' "$error_file"; then
    rm -f "$error_file"
    return 1
  fi
  cat "$error_file" >&2
  rm -f "$error_file"
  exit 1
}

if ! git merge-base --is-ancestor "$GITHUB_SHA" origin/main; then
  echo "should_tag_latest=false" >> "$GITHUB_OUTPUT"
  echo "::notice title=Latest promotion skipped::The event SHA is no longer on main."
else
  latest_exists=false
  if image_digest latest; then
    latest_exists=true
    latest_digest="$IMAGE_DIGEST"
    if ! latest_manifest=$(docker buildx imagetools inspect --raw "$IMAGE:latest"); then
      echo "::error title=Cannot establish latest provenance::Unable to read latest's manifest."
      exit 1
    fi
    latest_has_revision=$(printf '%s\n' "$latest_manifest" | jq -r '(.annotations // {}) | has("org.opencontainers.image.revision")')
    latest_sha=$(printf '%s\n' "$latest_manifest" | jq -r '(.annotations // {})["org.opencontainers.image.revision"] // empty')

    # Resolve the one legacy unannotated latest tag through its GHCR package-version
    # metadata. The package is the image without its registry and owner
    # (ghcr.io/<owner>/ -> <name>/envoy), whose remaining slash is percent-encoded
    # because the package name is one segment of the API's path; taken from $IMAGE so
    # that this lookup moves with the repository as the tag above does.
    if [ "$latest_has_revision" = false ]; then
      package=${IMAGE#"ghcr.io/$GITHUB_REPOSITORY_OWNER/"}
      package=${package//\//%2F}
      if ! legacy_source=$(gh api "users/$GITHUB_REPOSITORY_OWNER/packages/container/$package/versions?per_page=100" --paginate --jq '.[] | select(.metadata.container.tags | index("latest")) | [.name, (.metadata.container.tags[] | select(test("^[0-9a-f]{40}$")))] | @tsv'); then
        echo "::error title=Cannot establish latest provenance::Unable to resolve legacy latest through GHCR."
        exit 1
      fi
      case "$legacy_source" in
        *$'\n'*)
          echo "::error title=Cannot establish latest provenance::GHCR returned multiple latest sources."
          exit 1
          ;;
      esac
      IFS=$'\t' read -r legacy_digest latest_sha <<< "$legacy_source"
      if [ -z "$legacy_digest" ] || [ -z "$latest_sha" ] || [ "$legacy_digest" != "$latest_digest" ] ||
        ! git cat-file -e "${latest_sha}^{commit}" 2>/dev/null ||
        ! git merge-base --is-ancestor "$latest_sha" origin/main; then
        echo "::error title=Cannot establish latest provenance::Legacy latest has no matching main SHA image."
        exit 1
      fi
      echo "Resolved legacy latest to $latest_sha"
    elif [ -z "$latest_sha" ] || ! git cat-file -e "${latest_sha}^{commit}" 2>/dev/null ||
      ! git merge-base --is-ancestor "$latest_sha" origin/main; then
      echo "::error title=Cannot establish latest provenance::latest has an unknown or non-main revision annotation."
      exit 1
    fi
  fi

  # Prefer the newest already-published descendant, even if its release_publish job was
  # cancelled.
  promote_sha="$GITHUB_SHA"
  promote_digest="$OWN_DIGEST"
  if ! candidate_shas=$(git rev-list --first-parent "$GITHUB_SHA..origin/main"); then
    echo "::error title=Cannot establish latest provenance::Unable to enumerate newer main commits."
    exit 1
  fi
  if [ -n "$candidate_shas" ]; then
    while IFS= read -r candidate; do
      if image_digest "$candidate"; then
        promote_sha="$candidate"
        promote_digest="$IMAGE_DIGEST"
        break
      fi
    done <<< "$candidate_shas"
  fi
  test -n "$promote_digest"

  if [ "$latest_exists" = false ] ||
    git merge-base --is-ancestor "$latest_sha" "$promote_sha"; then
    if [ "$latest_exists" = true ] && [ "$latest_sha" = "$promote_sha" ]; then
      echo "should_tag_latest=false" >> "$GITHUB_OUTPUT"
      echo "latest already names $promote_sha"
    else
      {
        echo "should_tag_latest=true"
        echo "latest_sha=$promote_sha"
        echo "latest_digest=$promote_digest"
      } >> "$GITHUB_OUTPUT"
      echo "Advancing latest to $promote_sha"
    fi
  else
    echo "should_tag_latest=false" >> "$GITHUB_OUTPUT"
    echo "latest already names descendant $latest_sha"
  fi
fi
"""

ADVANCE_LATEST = r"""test -n "$DIGEST"
docker buildx imagetools create \
  --annotation "index:org.opencontainers.image.revision=$SHA" \
  --tag "$IMAGE:latest" \
  "$IMAGE@$DIGEST"
"""

STEPS_BEFORE_VERSION_STEPS = (
    ApprovedStep("checkout", uses=CHECKOUT, with_={"fetch-depth": 0, "persist-credentials": False}),
    ApprovedStep("checkout of the event's commit", uses=CHECKOUT,
                 with_={"fetch-depth": 0, "persist-credentials": False, "ref": "${{ github.sha }}"}),
    ApprovedStep("Determine latest", run=DETERMINE_LATEST, env={
        "GH_TOKEN": "${{ github.token }}",
        "IMAGE": "${{ needs.targets.outputs.envoy_image }}",
        "OWN_DIGEST": "${{ needs.image_index.outputs.digest }}",
    }),
    ApprovedStep("log in to GHCR", uses=LOGIN, with_={
        "password": "${{ secrets.GITHUB_TOKEN }}", "registry": "ghcr.io", "username": "${{ github.actor }}",
    }),
    ApprovedStep("Advance latest image", run=ADVANCE_LATEST, env={
        "DIGEST": "${{ steps.latest.outputs.latest_digest }}",
        "IMAGE": "${{ needs.targets.outputs.envoy_image }}",
        "SHA": "${{ steps.latest.outputs.latest_sha }}",
    }),
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
