#!/usr/bin/env bash
# The driver of the check `c-conversions-ran`. Before the pull request of an issue whose children
# convert Dispatch's documents into nodes merges, it reads whether production has finished each
# conversion those children ship, because that issue's own build holds none of the code the
# children kept for one release. It reads the Dispatch that DISPATCH_URL names, this pod's own,
# with this pod's own bearer, and GitHub with the role's own credential, and writes nothing to
# either. No person runs a conversion: two are steps of the server, and the third is a route the
# issue's architect calls. The architect reads this driver's whole output before signing the issue
# off and writes it on the pull request.
#
# It makes six reads, and runs each one whatever the reads before it answered, so that the record
# shows them all:
#
#   0. children   Which children of ISSUE are live. `dispatch issues --project <the part of ISSUE
#                 before its dash> --parent ISSUE --all` lists them. A child is live when it is not
#                 `done`, or when it is `done` and `gh pr list --head legion/<KEY> --state all`
#                 finds a pull request of its branch. A child parked in backlog, icebox or triage
#                 is live on purpose, as unfinished work of the tree. A live child that CHILDREN
#                 does not name fails, and the failure names its key.
#   1. merge, dispatch, pins, image
#                 Production carries the merge commit of each live child: the merge commit of the
#                 newest merged pull request of `legion/<KEY>`. A release carries a commit when
#                 `gh api repos/cybermonklabs/legion/compare/<commit>...<release>` answers `ahead`
#                 or `identical`. Dispatch carries it when the deployment's production check of
#                 Dispatch, run with that commit as production.sh runs it, ends on
#                 `all N assertions passed`. The daemon image and the worker image of each project
#                 carry it when the image's release (below) does.
#   2. healthz    The decisions step finished: /healthz, read with no credential as any caller
#                 reads it, answers `decisions_step_owed` at 0, beside a `commit` that carries the
#                 merge commit of every live child.
#   3. tree       GET /api/v1/issues/<key>/tree answers `converted: true` for ISSUE, and
#                 `converted: false` for DONE_ISSUE, an issue that is `done` and so never converted.
#   4. premortem  GET /api/v1/references?to=dispatch://<ISSUE>/artifact/<PREMORTEM_SLUG> answers a
#                 lens node at `dispatch://lens/<uuid>`, and GET /api/v1/lenses?on=<that address>
#                 answers that lens with kind `pre-mortem` and eleven risks under it.
#   5. spec       The version of ISSUE's spec that the `Approval:` line of
#                 `dispatch read --issue <ISSUE> --artifact spec` names as approved holds a line
#                 that is exactly `:::requirement{#r-stores-moved}`, the line that opens that
#                 block. The spec names that id in its prose too, and the prose is not the block. A
#                 failure prints the version, the person the line names and the block not found.
#
# The check holds the /healthz commit to the merge commit of the one child that ships the decisions
# step. No file of this repository may name a key of the deployment's project, and no input says
# which child that is, so this driver holds the commit to the merge commit of every live child.
# That child's commit is among them, so this read is the stricter one.
#
# The pins come from the directory that DAEMON_PINS_PATH names, which holds one `<nn>-<KEY>.yaml`
# per project. Each file is one mapping with `key`, `image` and `daemonImage`, and an unset
# `daemonImage` means that the project's daemon runs `image`. The release of an image is the
# commit that its `org.opencontainers.image.revision` label names, where its
# `org.opencontainers.image.source` label names Legion's repository, under its present owner or
# the one it moved from (a build cut before the move names that one). That is the `legion-worker`
# image, which packs the plugins from its own checkout, so its revision stands for them too. An
# image that the deployment repository builds on top of `legion-worker` labels that repository's
# own commit instead. For such an image, a worker image or a daemon image alike, the driver reads
# the image's Dockerfile in the deployment repository at that commit. The path is the one the
# image's repository names below the registry's project: an image `<host>/<project>/<dir>/<name>`
# is built from `<dir>/<name>/Dockerfile`. The driver takes the one image that the Dockerfile's
# `ARG LEGION_WORKER=` names, and takes that image's label. An image whose source label names
# neither repository fails.
#
# Each image is read with this pod's own identity and no other. A ghcr.io image is read with the
# anonymous pull token that ghcr.io grants any caller. An Artifact Registry image (a host that ends
# in `.pkg.dev`) is read with the Google access token that the metadata server gives this pod. An
# image of any other registry, or one that its registry refuses, fails, and the failure names it.
#
# The architect writes this output on a public pull request. So the driver names an image by the
# last part of its repository and its digest (`<name>@sha256:<hex>`), never by the registry path
# that names the deployment's project. It names the two deployment paths by their variable names,
# and the deployment repository as "the deployment repository". It prints a registry's refusal as
# its status and its error codes alone, because a registry's message quotes the image's full path,
# and it removes the deployment repository's owner and name from every error that `gh` prints.
# Issue keys, commits and the person that the `Approval:` line names are printed as read, because
# the check asks for each.
#
# Inputs. Each of these six is required, and the driver exits 2 where one is unset or malformed, so
# that no file of this repository holds, as a literal this script needs, the deployment's
# repository, its owner or a key of its project:
#   ISSUE                  the key of the issue whose tree converted;
#   PREMORTEM_SLUG         the slug of that issue's pre-mortem document;
#   PRODUCTION_CHECK_PATH  the GitHub contents path of the deployment's production check of
#                          Dispatch, `repos/<owner>/<repo>/contents/<path>` with no `?ref=`, read
#                          at `?ref=main`;
#   DAEMON_PINS_PATH       the GitHub contents path of the deployment's directory of project files,
#                          in the same form, read at `?ref=main`;
#   DONE_ISSUE             the key of an issue that is `done`;
#   CHILDREN               the keys of the live children, separated by blanks.
# Two are optional, and the check's negative controls set them: MERGE_COMMIT_OVERRIDE,
# `<KEY>=<commit>`, a commit read in place of the merge commit of the child KEY, which CHILDREN
# must name; and SPEC_VERSION_OVERRIDE, a spec version read in place of the approved one.
#
# DISPATCH_URL and the bearer are this pod's own. lib.sh's `read_bearer` takes the bearer from
# DISPATCH_TOKEN or DISPATCH_TOKEN_FILE, unsets both and never prints it. The driver writes it to
# two files of this run under TMPDIR, which only their owner can read: the token file that it names
# as DISPATCH_TOKEN_FILE to the dispatch CLI and to the check script, and the header file that curl
# reads. The cleanup trap removes both. The driver sets no GH_TOKEN and no GITHUB_TOKEN, because a
# Legion role's `gh` refuses every command run under either of them; `gh` is authenticated from
# GH_CONFIG_DIR.
#
# The check script is fetched into a file and run from that file, never `gh api … | bash`. A pipe
# hides a failed fetch, and `bash` that reads the empty program left behind exits 0 having asserted
# nothing. The driver refuses a fetch that failed and a file that came back empty.
#
# `spec_summary` and `spec_text` are the only two places that read the spec, one function each, so
# that a scratch copy of this driver can put an answer of its own in place of either. The check's
# seventh control does that.
#
# Each assertion prints one `PASS: <section> <what>[: <observed>]` or
# `FAIL: <section> <what>: <observed>` line on standard output. Those are the only lines that begin
# with either word, because the check script's own lines are printed indented. There is no SKIP: a
# read that cannot be made fails, and the failure says why. The driver exits 0 when every assertion
# passed, 1 when any failed, and 2 for an input of its own that is missing or malformed.
#
# It runs under `set -uo pipefail` and not under `set -e`, because a read that fails must not stop
# the reads after it. So each command's status is read where the command runs.

set -uo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: ISSUE=<the key of the issue whose tree converted> \
         PREMORTEM_SLUG=<the slug of that issue's pre-mortem document> \
         PRODUCTION_CHECK_PATH=<repos/<owner>/<repo>/contents/<path> of the deployment's production
           check of Dispatch, without ?ref=, which the driver reads at ?ref=main> \
         DAEMON_PINS_PATH=<repos/<owner>/<repo>/contents/<path> of the deployment's directory of
           project files, one <nn>-<KEY>.yaml per project, without ?ref=, read at ?ref=main> \
         DONE_ISSUE=<the key of an issue that is done> \
         CHILDREN='<KEY> <KEY> ...' (the keys of the live children) \
         [MERGE_COMMIT_OVERRIDE=<KEY>=<commit>] [SPEC_VERSION_OVERRIDE=<version>] conversions-ran.sh
       (DISPATCH_URL and the bearer are this pod's own; the bearer is read from DISPATCH_TOKEN or
       DISPATCH_TOKEN_FILE and never printed)
USAGE
}

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_env ISSUE PREMORTEM_SLUG PRODUCTION_CHECK_PATH DAEMON_PINS_PATH DONE_ISSUE CHILDREN DISPATCH_URL
require_commands bash curl date dispatch gh grep jq sed yq

# refuse_input PROBLEM: an input of the wrong shape, which exits 2 as an unset one does.
refuse_input() {
  printf '%s\n' "$1" >&2
  usage
  exit 2
}

readonly key_rule='^[A-Z][A-Z0-9]*-[0-9]+$'
readonly path_rule='^repos/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)/contents/[^?#[:space:]]+$'

# shellcheck disable=SC2153 # ISSUE is the caller's input, checked by name above
readonly issue="$ISSUE"
[[ "$issue" =~ $key_rule ]] || refuse_input "ISSUE is $issue, not an issue key such as ACME-1"
readonly project="${issue%%-*}"
# shellcheck disable=SC2153 # DONE_ISSUE is the caller's input, checked by name above
readonly done_issue="$DONE_ISSUE"
[[ "$done_issue" =~ $key_rule ]] ||
  refuse_input "DONE_ISSUE is $done_issue, not an issue key such as ACME-1"
readonly slug="$PREMORTEM_SLUG"
[[ "$slug" =~ ^[a-z0-9][a-z0-9._-]*$ ]] ||
  refuse_input "PREMORTEM_SLUG is $slug, not a document slug such as premortem-md"

[[ "$PRODUCTION_CHECK_PATH" =~ $path_rule ]] ||
  refuse_input "PRODUCTION_CHECK_PATH must be repos/<owner>/<repo>/contents/<path>, with no ?ref="
readonly check_owner="${BASH_REMATCH[1]}"
readonly check_repo="${BASH_REMATCH[1]}/${BASH_REMATCH[2]}"
[[ "$DAEMON_PINS_PATH" =~ $path_rule ]] ||
  refuse_input "DAEMON_PINS_PATH must be repos/<owner>/<repo>/contents/<path>, with no ?ref="
readonly deploy_owner="${BASH_REMATCH[1]}"
readonly deploy_repo="${BASH_REMATCH[1]}/${BASH_REMATCH[2]}"
readonly pins_dir="${DAEMON_PINS_PATH%/}"

# shellcheck disable=SC2153 # CHILDREN is the caller's input, checked by name above
read -ra named_children <<<"$CHILDREN"
((${#named_children[@]} > 0)) || refuse_input "CHILDREN holds no key"
for key in "${named_children[@]}"; do
  [[ "$key" =~ $key_rule ]] || refuse_input "CHILDREN holds $key, not an issue key such as ACME-1"
done
readonly named_children

# is_named KEY: whether CHILDREN names KEY.
is_named() {
  local key
  for key in "${named_children[@]}"; do
    [[ "$key" == "$1" ]] && return 0
  done
  return 1
}

override_key=""
override_commit=""
if [[ -n "${MERGE_COMMIT_OVERRIDE:-}" ]]; then
  [[ "$MERGE_COMMIT_OVERRIDE" =~ ^([A-Z][A-Z0-9]*-[0-9]+)=([0-9a-fA-F]{7,40})$ ]] ||
    refuse_input "MERGE_COMMIT_OVERRIDE is $MERGE_COMMIT_OVERRIDE, not <KEY>=<commit>"
  override_key="${BASH_REMATCH[1]}"
  override_commit="${BASH_REMATCH[2]}"
  is_named "$override_key" ||
    refuse_input "MERGE_COMMIT_OVERRIDE names $override_key, which CHILDREN does not name"
fi
readonly override_key override_commit
readonly spec_override="${SPEC_VERSION_OVERRIDE:-}"
[[ -z "$spec_override" || "$spec_override" =~ ^[1-9][0-9]*$ ]] ||
  refuse_input "SPEC_VERSION_OVERRIDE is $spec_override, not a version number"

read_bearer
# The check script reads DISPATCH_URL from its environment. read_bearer has unset both bearer
# variables, so the only bearer that reaches the script or the CLI is the file named below.
# shellcheck disable=SC2031 # the DISPATCH_URL `cli` exports is its own; this one is the caller's
export DISPATCH_URL
# shellcheck disable=SC2031 # the DISPATCH_URL `cli` exports is its own; this one is the caller's
readonly base="${DISPATCH_URL%/}"

# Legion's own repository, which every merge commit and every release belongs to.
readonly legion_repo="cybermonklabs/legion"
# The owner Legion's repository had before it moved to the present one on 2026-11-03. The move kept
# the repository and its commits, so an image cut before it names that owner in its source label,
# and its revision is a commit of the same repository, which a compare under the present name reads.
readonly legion_repo_before="sjawhar/legion"

# is_legion_source REPO: whether REPO, as source_repo gives it, is Legion's repository under either
# of its names.
is_legion_source() { [[ "$1" == "$legion_repo" || "$1" == "$legion_repo_before" ]]; }
# Where this pod's own Google credential comes from: the metadata server, and no broker.
readonly metadata_token_url="http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
readonly manifest_types='application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json'
readonly image_rule='^([a-z0-9.-]+(:[0-9]+)?)/([a-z0-9._/-]+)(:[A-Za-z0-9._-]+)?@(sha256:[0-9a-f]{64})$'
readonly legion_worker_arg='^[[:space:]]*[Aa][Rr][Gg][[:space:]]+LEGION_WORKER="?([^"[:space:]]+)'
readonly block_line=':::requirement{#r-stores-moved}'

umask 077
work="$(mktemp -d)"
readonly work
# The bearer is written here and nowhere else: two files of this run's own, which only their owner
# can read, removed by the cleanup trap below.
readonly token_file="$work/dispatch-token"
readonly check_script="$work/dispatch-production-check.sh"

# The EXIT trap below invokes this; shellcheck misses that in a script whose every path exits.
# shellcheck disable=SC2329
cleanup() {
  local code=$?
  rm -f "$token_file" "$work/auth" "$work/google-auth" "$work/registry-auth"
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf '%s\n' "$token" >"$token_file"
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"
token=""

assertions=0
failures=0
# pass SECTION WHAT [OBSERVED] and fail SECTION WHAT OBSERVED: one assertion each. Both print on
# standard output, so the record keeps its order wherever the caller sends it.
pass() {
  assertions=$((assertions + 1))
  if (($# > 2)); then
    printf 'PASS: %s %s: %s\n' "$1" "$2" "$3"
  else
    printf 'PASS: %s %s\n' "$1" "$2"
  fi
}
fail() {
  assertions=$((assertions + 1))
  failures=$((failures + 1))
  printf 'FAIL: %s %s: %s\n' "$1" "$2" "$3"
}

# head_of FILE: the first 300 bytes of FILE on one line, for a failure.
head_of() {
  local text
  text="$(head -c 300 "$1" 2>/dev/null | tr '\n\r\t' '   ')"
  printf '%s' "${text%"${text##*[![:space:]]}"}"
}

# scrub TEXT: TEXT without the deployment repository's owner and name, for an error that a read of
# that repository printed and that may quote its path.
scrub() {
  local text="$1"
  text="${text//"$deploy_repo"/the deployment repository}"
  text="${text//"$check_repo"/the deployment repository}"
  text="${text//"$deploy_owner"/<owner>}"
  text="${text//"$check_owner"/<owner>}"
  printf '%s' "$text"
}

# join_by SEPARATOR ITEM...: the items joined by SEPARATOR.
join_by() {
  local separator="$1" joined="" item
  shift
  for item in "$@"; do
    joined+="${joined:+$separator}$item"
  done
  printf '%s' "$joined"
}

# get PATH OUT: GET PATH of this pod's Dispatch as its agent, the body into OUT and the status into
# $status, or `curl failed: <why>` where no answer came.
get() {
  status="$(curl -sS -m 120 -o "$2" -w '%{http_code}' -H "@$work/auth" "$base$1" 2>"$work/curl.err")" ||
    status="curl failed: $(head_of "$work/curl.err")"
}

# answer_of OUT: what the last request answered, for a failure: its status and the head of its body.
answer_of() {
  if [[ "$status" =~ ^[0-9]{3}$ ]]; then
    printf 'HTTP %s: %s' "$status" "$(head_of "$1")"
  else
    printf '%s' "$status"
  fi
}

# The dispatch CLI on PATH, reading this pod's Dispatch with this run's bearer file, since
# read_bearer has unset the variables the CLI would otherwise read the bearer from.
dispatch_cli() { DISPATCH_TOKEN_FILE="$token_file" dispatch "$@"; }
# The two reads of the spec, one function each, which the seventh control replaces in a copy.
spec_summary() { dispatch_cli read --issue "$issue" --artifact spec; }
spec_text() { dispatch_cli doc-read --issue "$issue" --artifact spec --version "$1"; }

# pull_requests KEY: the pull requests of the branch legion/KEY in Legion's repository, in any
# state, as a JSON list in $work/prs-KEY.json, read once. A failed read returns 1 and leaves gh's
# error in $work/gh.err.
pull_requests() {
  local out="$work/prs-$1.json"
  [[ -s "$out" ]] && return 0
  gh pr list --repo "$legion_repo" --head "legion/$1" --state all --limit 100 \
    --json number,state,mergedAt,mergeCommit,headRefName >"$work/prs.raw" 2>"$work/gh.err" || return 1
  # shellcheck disable=SC2016 # the jq filter names a jq variable, not a shell one
  jq --arg branch "legion/$1" 'map(select(.headRefName == $branch))' "$work/prs.raw" >"$out"
}

declare -A compared=()
# compare COMMIT RELEASE: sets $compare_answer to GitHub's status for COMMIT...RELEASE in Legion's
# repository, or to why there is none. Each pair is asked once.
compare() {
  local pair="$1...$2" answer
  if [[ -z "${compared[$pair]:-}" ]]; then
    if answer="$(gh api "repos/$legion_repo/compare/$pair" --jq .status 2>"$work/gh.err")"; then
      compared[$pair]="${answer:-an empty status}"
    else
      compared[$pair]="no answer from gh api repos/$legion_repo/compare/$pair: $(head_of "$work/gh.err")"
    fi
  fi
  compare_answer="${compared[$pair]}"
}

declare -A commit_of=()
live=()
# carries_all SECTION WHAT RELEASE: one assertion that RELEASE carries the merge commit of every live
# child. It prints GitHub's answer for each child, and fails on a child that has no merge commit.
carries_all() {
  local section="$1" what="$2" release="$3" key commit ok=1 joined
  local -a answers=()
  if ((${#live[@]} == 0)); then
    pass "$section" "$what" "there is no live child"
    return
  fi
  for key in "${live[@]}"; do
    commit="${commit_of[$key]:-}"
    if [[ -z "$commit" ]]; then
      answers+=("$key: no merge commit")
      ok=0
      continue
    fi
    compare "$commit" "$release"
    answers+=("$key (${commit:0:12}): $compare_answer")
    [[ "$compare_answer" == ahead || "$compare_answer" == identical ]] || ok=0
  done
  joined="$(join_by '; ' "${answers[@]}")"
  if ((ok)); then
    pass "$section" "$what" "$joined"
  else
    fail "$section" "$what" "$joined"
  fi
}

# image_name REF: the image REF as the record names it, the last part of its repository and its
# digest, never the registry path, which names the deployment's project.
image_name() {
  local rest="${1#*/}" digest=""
  [[ "$1" == *@* ]] && digest="@${1##*@}"
  rest="${rest%@*}"
  rest="${rest##*/}"
  printf '%s%s' "${rest%%:*}" "$digest"
}

# build_dir REF: the directory of the deployment repository that builds the image REF, which is the
# image's repository below the registry's project: `<host>/<project>/<dir>/<name>` is `<dir>/<name>`.
build_dir() {
  local rest="${1#*/}"
  rest="${rest%@*}"
  rest="${rest%%:*}"
  printf '%s' "${rest#*/}"
}

# source_repo LABEL: the `<owner>/<repo>` of GitHub that an image's source label names, in lower
# case, or the label itself in lower case where it names no repository of GitHub.
source_repo() {
  local source="${1,,}"
  source="${source#https://github.com/}"
  source="${source%/}"
  printf '%s' "${source%.git}"
}

# status_text STATUS: a curl status for a failure, `HTTP <n>` or why no answer came.
status_text() {
  if [[ "$1" =~ ^[0-9]{3}$ ]]; then printf 'HTTP %s' "$1"; else printf '%s' "$1"; fi
}

# google_auth: the header file of this pod's own Google access token, from the metadata server,
# read once. It sets $label_error and returns 1 where none came.
google_auth() {
  [[ -s "$work/google-auth" ]] && return 0
  local answer
  answer="$(curl -sS -m 20 -H 'Metadata-Flavor: Google' -o "$work/metadata" -w '%{http_code}' \
    "$metadata_token_url" 2>"$work/curl.err")" || answer="curl failed: $(head_of "$work/curl.err")"
  if [[ "$answer" != 200 ]] ||
    ! jq -er '.access_token | select(length > 0)' "$work/metadata" >"$work/google-token" 2>/dev/null; then
    rm -f "$work/metadata" "$work/google-token"
    label_error="the metadata server gave this pod no Google access token: $(status_text "$answer")"
    return 1
  fi
  { printf 'Authorization: Bearer '; cat "$work/google-token"; } >"$work/google-auth"
  rm -f "$work/metadata" "$work/google-token"
}

# registry_auth HOST PATH: the header file a read of the image repository PATH on HOST sends, made
# with this pod's own identity for that registry. It sets $label_error and returns 1 where the pod
# holds none, or the registry gave it none.
registry_auth() {
  local answer
  case "$1" in
  ghcr.io)
    answer="$(curl -sS -m 30 -o "$work/registry-token" -w '%{http_code}' \
      "https://ghcr.io/token?scope=repository:$2:pull" 2>"$work/curl.err")" ||
      answer="curl failed: $(head_of "$work/curl.err")"
    if [[ "$answer" != 200 ]] ||
      ! jq -er '.token | select(length > 0)' "$work/registry-token" >"$work/registry-token.value" 2>/dev/null; then
      rm -f "$work/registry-token" "$work/registry-token.value"
      label_error="ghcr.io gave this pod no anonymous pull token: $(status_text "$answer")"
      return 1
    fi
    { printf 'Authorization: Bearer '; cat "$work/registry-token.value"; } >"$work/registry-auth"
    rm -f "$work/registry-token" "$work/registry-token.value"
    ;;
  *.pkg.dev)
    google_auth || return 1
    cp "$work/google-auth" "$work/registry-auth"
    ;;
  *)
    label_error="this pod holds no identity for the registry $1: it reads ghcr.io anonymously and Artifact Registry (*.pkg.dev) with its own Google identity"
    return 1
    ;;
  esac
}

# registry_get URL OUT CURL-ARGS...: GET URL of a registry with the header file registry_auth made,
# the body into OUT. It sets $label_error and returns 1 on anything but 200, naming the status and
# the registry's error codes alone: the registry's message quotes the image's whole path.
registry_get() {
  local url="$1" out="$2" answer codes
  shift 2
  answer="$(curl -sS -m 60 -H "@$work/registry-auth" -o "$out" -w '%{http_code}' "$@" "$url" \
    2>"$work/curl.err")" || {
    label_error="curl failed: $(head_of "$work/curl.err")"
    return 1
  }
  [[ "$answer" == 200 ]] && return 0
  codes="$(jq -r '[.errors[]?.code] | join(", ")' "$out" 2>/dev/null)"
  label_error="the registry answered HTTP $answer, error codes: ${codes:-none}"
  return 1
}

# read_label REF: reads the config of the image REF, pinned by digest, and sets $label_revision and
# $label_source to its two labels, or $label_error to why it could not. An index is read at its
# linux/amd64 image, the platform every pod runs on.
read_label() {
  local ref="$1" host path digest platform config
  label_revision=""
  label_source=""
  label_error=""
  if ! [[ "$ref" =~ $image_rule ]]; then
    label_error="it is not an image reference pinned by digest"
    return
  fi
  host="${BASH_REMATCH[1]}"
  path="${BASH_REMATCH[3]}"
  digest="${BASH_REMATCH[5]}"
  registry_auth "$host" "$path" || return
  registry_get "https://$host/v2/$path/manifests/$digest" "$work/manifest" \
    -H "Accept: $manifest_types" || return
  if jq -e '.manifests | type == "array"' "$work/manifest" >/dev/null 2>&1; then
    platform="$(jq -r 'first(.manifests[] | select(.platform.os == "linux" and .platform.architecture == "amd64") | .digest) // empty' \
      "$work/manifest")"
    if [[ -z "$platform" ]]; then
      label_error="it is an index that holds no linux/amd64 image"
      return
    fi
    registry_get "https://$host/v2/$path/manifests/$platform" "$work/manifest" \
      -H "Accept: $manifest_types" || return
  fi
  config="$(jq -r '.config.digest // empty' "$work/manifest" 2>/dev/null)"
  if ! [[ "$config" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    label_error="its manifest names no config"
    return
  fi
  registry_get "https://$host/v2/$path/blobs/$config" "$work/config" -L || return
  label_revision="$(jq -r '.config.Labels["org.opencontainers.image.revision"] // empty' "$work/config" 2>/dev/null)"
  label_source="$(jq -r '.config.Labels["org.opencontainers.image.source"] // empty' "$work/config" 2>/dev/null)"
  if ! [[ "$label_revision" =~ ^[0-9a-f]{40}$ ]]; then
    label_error="its config carries no org.opencontainers.image.revision label that is a 40-hex commit: it reads \"$label_revision\""
    label_revision=""
  fi
}

declare -A label_revision_of=() label_source_of=() label_error_of=()
# image_label REF: read_label, once for each image, since one legion-worker image is often both a
# daemon image and the base of a worker image. It returns 1 where the label could not be read.
image_label() {
  local ref="$1"
  if [[ -n "${label_error_of[$ref]+set}" ]]; then
    label_revision="${label_revision_of[$ref]}"
    label_source="${label_source_of[$ref]}"
    label_error="${label_error_of[$ref]}"
  else
    read_label "$ref"
    label_revision_of[$ref]="$label_revision"
    label_source_of[$ref]="$label_source"
    label_error_of[$ref]="$label_error"
  fi
  [[ -z "$label_error" ]]
}

declare -A release_of=() release_how=() release_error=()
# resolve_release REF: the release of the image REF into release_of and release_how, or why there
# is none into release_error. The header says what a release is.
resolve_release() {
  local ref="$1" name revision source dockerfile line base_image answer=0
  local -a bases=()
  name="$(image_name "$ref")"
  if ! image_label "$ref"; then
    release_error[$ref]="$name could not be read with this pod's own identity: $label_error"
    return
  fi
  revision="$label_revision"
  source="$(source_repo "$label_source")"
  if is_legion_source "$source"; then
    release_of[$ref]="$revision"
    release_how[$ref]="the revision label of $name, a build of Legion's repository"
    return
  fi
  if [[ "$source" != "${deploy_repo,,}" ]]; then
    release_error[$ref]="the source label of $name reads \"$(scrub "$label_source")\", which is neither Legion's repository nor the deployment repository, so its revision $revision is no commit this driver can follow"
    return
  fi
  dockerfile="$(build_dir "$ref")/Dockerfile"
  gh api "repos/$deploy_repo/contents/$dockerfile?ref=$revision" -H 'Accept: application/vnd.github.raw' \
    >"$work/Dockerfile" 2>"$work/gh.err" || answer=$?
  if ((answer != 0)); then
    release_error[$ref]="$name is a build of the deployment repository at $revision, and its $dockerfile there could not be read: gh api exited $answer: $(scrub "$(head_of "$work/gh.err")")"
    return
  fi
  while IFS= read -r line; do
    if [[ "$line" =~ $legion_worker_arg ]]; then
      bases+=("${BASH_REMATCH[1]}")
    fi
  done <"$work/Dockerfile"
  if ((${#bases[@]} == 0)); then
    release_error[$ref]="$name is a build of the deployment repository at $revision, and its $dockerfile there names no ARG LEGION_WORKER=<image>"
    return
  fi
  for base_image in "${bases[@]}"; do
    if [[ "$base_image" != "${bases[0]}" ]]; then
      release_error[$ref]="$name is a build of the deployment repository at $revision, and its $dockerfile there names ARG LEGION_WORKER with more than one image"
      return
    fi
  done
  base_image="${bases[0]}"
  if ! image_label "$base_image"; then
    release_error[$ref]="the image $(image_name "$base_image") that ARG LEGION_WORKER names in the deployment repository's $dockerfile at $revision, which $name is built from, could not be read with this pod's own identity: $label_error"
    return
  fi
  if ! is_legion_source "$(source_repo "$label_source")"; then
    release_error[$ref]="the image $(image_name "$base_image") that ARG LEGION_WORKER names in the deployment repository's $dockerfile at $revision is no build of Legion's repository: its source label reads \"$(scrub "$label_source")\""
    return
  fi
  release_of[$ref]="$label_revision"
  release_how[$ref]="the revision label of $(image_name "$base_image"), which ARG LEGION_WORKER names in the deployment repository's $dockerfile at $revision, the commit the label of $name names"
}

declare -A image_roles=()
images=()
# add_image REF ROLE: REF among the images to read, and ROLE among the pins that name it.
add_image() {
  if [[ -z "${image_roles[$1]:-}" ]]; then
    images+=("$1")
    image_roles[$1]="$2"
  else
    image_roles[$1]+=", $2"
  fi
}

# read_project NAME: the project file NAME of the pins directory, whose image and daemon image are
# added to the images to read.
read_project() {
  local name="$1" what="project file $1 names its image and daemon image" answer=0 documents key image daemon
  gh api "$pins_dir/$name?ref=main" -H 'Accept: application/vnd.github.raw' \
    >"$work/project.yaml" 2>"$work/gh.err" || answer=$?
  if ((answer != 0)); then
    fail pins "$what" "gh api exited $answer: $(scrub "$(head_of "$work/gh.err")")"
    return
  fi
  if ! yq -o=json -I=0 '.' "$work/project.yaml" >"$work/project.json" 2>"$work/yq.err"; then
    fail pins "$what" "yq could not read it: $(head_of "$work/yq.err")"
    return
  fi
  documents="$(jq -s 'length' "$work/project.json" 2>/dev/null)"
  if [[ "$documents" != 1 ]] || ! jq -e 'type == "object"' "$work/project.json" >/dev/null 2>&1; then
    fail pins "$what" "it holds ${documents:-no} YAML documents, where a project file is one mapping"
    return
  fi
  key="$(jq -r '.key // empty | strings' "$work/project.json")"
  image="$(jq -r '.image // empty | strings' "$work/project.json")"
  daemon="$(jq -r '.daemonImage // empty | strings' "$work/project.json")"
  if ! [[ "$key" =~ ^[A-Z][A-Z0-9]*$ ]]; then
    fail pins "$what" "its key reads \"$key\", which is not a project key"
    return
  fi
  if [[ -z "$image" ]]; then
    fail pins "$what" "project $key sets no image"
    return
  fi
  add_image "$image" "$key image"
  if [[ -n "$daemon" ]]; then
    add_image "$daemon" "$key daemonImage"
    pass pins "$what" "$key: image $(image_name "$image"), daemonImage $(image_name "$daemon")"
  else
    add_image "$image" "$key daemon"
    pass pins "$what" "$key: image $(image_name "$image"), daemonImage unset, so its daemon runs image"
  fi
}

printf 'conversions-ran.sh at %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf 'dispatch: %s\n' "$base"
printf 'issue (ISSUE): %s, in project %s\n' "$issue" "$project"
printf 'pre-mortem (PREMORTEM_SLUG): %s\n' "$slug"
printf 'done issue (DONE_ISSUE): %s\n' "$done_issue"
printf 'named live children (CHILDREN): %s\n' "${named_children[*]}"
printf 'MERGE_COMMIT_OVERRIDE: %s\n' "${MERGE_COMMIT_OVERRIDE:-unset}"
printf 'SPEC_VERSION_OVERRIDE: %s\n' "${spec_override:-unset}"

# 0. The live children.
printf '\n== 0. the live children of %s\n' "$issue"
declare -A live_reason=() child_status=()
children=()
listed_children=0
answer=0
dispatch_cli issues --project "$project" --parent "$issue" --all >"$work/children" 2>"$work/children.err" ||
  answer=$?
if ((answer != 0)); then
  fail children "dispatch issues lists the children of $issue" \
    "it exited $answer: $(head_of "$work/children.err")"
else
  header="$(head -n 1 "$work/children")"
  while IFS= read -r line; do
    if [[ "$line" =~ ^([A-Z][A-Z0-9]*-[0-9]+)\ \[([a-z_]+)\]( |$) ]]; then
      children+=("${BASH_REMATCH[1]}")
      child_status[${BASH_REMATCH[1]}]="${BASH_REMATCH[2]}"
    fi
  done < <(tail -n +2 "$work/children")
  # The CLI prints a count, then one `<KEY> [<status>] <title>` line per issue; it has no
  # machine-readable form. The count holds the parse to the list: a row this driver could not
  # read is a child it would never judge.
  if ! [[ "$header" =~ ^([0-9]+)\ issues?\ in\  ]]; then
    fail children "dispatch issues lists the children of $issue" \
      "its first line reads \"$header\", not \"<n> issues in $project\""
  elif ((BASH_REMATCH[1] != ${#children[@]})); then
    fail children "dispatch issues lists the children of $issue" \
      "it said ${BASH_REMATCH[1]} issues and this driver read ${#children[@]} rows of <KEY> [<status>]: $(head_of "$work/children")"
  else
    listed_children=1
    done_count=0
    for key in "${children[@]}"; do
      [[ "${child_status[$key]}" == "done" ]] && done_count=$((done_count + 1))
    done
    pass children "dispatch issues lists the children of $issue" \
      "${#children[@]} children, $done_count of them done"
    for key in "${children[@]}"; do
      if [[ "${child_status[$key]}" != "done" ]]; then
        live+=("$key")
        live_reason[$key]="it is ${child_status[$key]}"
        continue
      fi
      if ! pull_requests "$key"; then
        # A done child whose pull requests could not be read is taken as live, so every read
        # after this one judges it too, rather than passing it unread.
        fail children "done child $key has no pull request of legion/$key" \
          "gh pr list failed, so it is taken as live: $(head_of "$work/gh.err")"
        live+=("$key")
        live_reason[$key]="it is done, and its pull requests could not be read"
        continue
      fi
      prs="$(jq -r 'map("#\(.number) \(.state)") | join(", ")' "$work/prs-$key.json")"
      if [[ -n "$prs" ]]; then
        live+=("$key")
        live_reason[$key]="it is done, with pull request $prs of legion/$key"
      fi
    done
  fi
fi

if ((listed_children)); then
  for key in "${live[@]}"; do
    printf 'live: %s (%s)\n' "$key" "${live_reason[$key]}"
  done
  unnamed=0
  for key in "${live[@]}"; do
    if ! is_named "$key"; then
      unnamed=$((unnamed + 1))
      fail children "live child $key is named in CHILDREN" \
        "${live_reason[$key]}, and CHILDREN does not name it: name it in CHILDREN, or close, merge or move it out of the tree of $issue"
    fi
  done
  ((unnamed == 0)) && pass children "every live child of $issue is named in CHILDREN" "${live[*]:-none}"
  for key in "${named_children[@]}"; do
    [[ -n "${live_reason[$key]:-}" ]] && continue
    if [[ -n "${child_status[$key]:-}" ]]; then
      printf 'not live, so no read judges it: %s, which CHILDREN names (it is %s with no pull request of legion/%s)\n' \
        "$key" "${child_status[$key]}" "$key"
    else
      printf 'not live, so no read judges it: %s, which CHILDREN names (it is no child of %s)\n' \
        "$key" "$issue"
    fi
  done
else
  # With no list of the children, the reads below judge the children CHILDREN names, so the
  # record still shows each of them.
  live=("${named_children[@]}")
  for key in "${live[@]}"; do
    live_reason[$key]="CHILDREN names it, and the children of $issue could not be listed"
  done
  printf 'the children of %s could not be listed, so the reads below judge those CHILDREN names: %s\n' \
    "$issue" "${live[*]}"
fi

if [[ -n "$override_key" ]]; then
  if [[ -n "${live_reason[$override_key]:-}" ]]; then
    pass merge "MERGE_COMMIT_OVERRIDE replaces the commit of a live child" \
      "$override_commit in place of the commit of $override_key"
  else
    fail merge "MERGE_COMMIT_OVERRIDE replaces the commit of a live child" \
      "$override_key is no live child of $issue, so it replaced no commit"
  fi
fi

# 1. Production carries each live child's merge commit.
printf '\n== 1. production carries the merge commit of every live child\n'
for key in "${live[@]}"; do
  what="legion/$key has a merged pull request"
  if ! pull_requests "$key"; then
    fail merge "$what" "gh pr list failed: $(head_of "$work/gh.err")"
  else
    merged="$(jq -r 'map(select(.state == "MERGED" and .mergeCommit.oid != null)) | sort_by(.mergedAt) | last | if . == null then "" else "\(.number) \(.mergedAt) \(.mergeCommit.oid)" end' \
      "$work/prs-$key.json")"
    if [[ -z "$merged" ]]; then
      fail merge "$what" "$(jq -r 'if length == 0 then "it has no pull request" else "its pull requests are " + (map("#\(.number) \(.state)") | join(", ")) + ", and none merged" end' \
        "$work/prs-$key.json"), so no release can carry it yet"
    else
      read -r number merged_at oid <<<"$merged"
      commit_of[$key]="$oid"
      pass merge "$what" "#$number merged at $merged_at as $oid"
    fi
  fi
  if [[ "$key" == "$override_key" ]]; then
    printf 'MERGE_COMMIT_OVERRIDE: %s in place of %s for %s\n' \
      "$override_commit" "${commit_of[$key]:-no merge commit}" "$key"
    commit_of[$key]="$override_commit"
  fi
done
for key in "${live[@]}"; do
  printf 'commit of %s: %s\n' "$key" "${commit_of[$key]:-none}"
done

printf '\n-- Dispatch\n'
fetch="the production check of Dispatch came from PRODUCTION_CHECK_PATH at ?ref=main"
fetched=0
answer=0
gh api "${PRODUCTION_CHECK_PATH}?ref=main" -H 'Accept: application/vnd.github.raw' \
  >"$check_script" 2>"$work/gh.err" || answer=$?
if ((answer != 0)); then
  fail dispatch "$fetch" "gh api exited $answer: $(scrub "$(head_of "$work/gh.err")")"
elif [[ ! -s "$check_script" ]]; then
  fail dispatch "$fetch" "gh api exited 0 and wrote an empty file, so there is no check to run"
else
  fetched=1
  pass dispatch "$fetch" "$(wc -c <"$check_script") bytes"
fi
for key in "${live[@]}"; do
  commit="${commit_of[$key]:-}"
  what="the running Dispatch carries the merge commit of $key"
  if [[ -z "$commit" ]]; then
    fail dispatch "$what" "$key has no merge commit, so no release can carry it"
    continue
  fi
  if ((fetched == 0)); then
    fail dispatch "$what ($commit)" "the production check of Dispatch was not fetched, so it did not run"
    continue
  fi
  # As production.sh runs it: this issue's page, and the two routes that driver names for it.
  answer=0
  DISPATCH_TOKEN_FILE="$token_file" bash "$check_script" --commit "$commit" --page "/issues/$issue" \
    --route "/api/v1/issues/$issue/decisions" --route /api/v1/inbox >"$work/check.out" 2>&1 ||
    answer=$?
  printf 'the production check of Dispatch with %s, the commit of %s, exited %d:\n' "$commit" "$key" "$answer"
  sed 's/^/    /' "$work/check.out"
  release="$(grep -m 1 '^PASS the running build answers /healthz' "$work/check.out" | grep -oE 'names commit [0-9a-fA-F]{7,40}')"
  release="${release#names commit }"
  printf 'dispatch release: %s\n' "${release:-unknown: the check named no commit /healthz answered}"
  last_line="$(tail -n 1 "$work/check.out")"
  if ((answer == 0)) && [[ "$last_line" =~ ^all\ [0-9]+\ assertions\ passed$ ]]; then
    pass dispatch "$what ($commit)" "the check ended \"$last_line\""
  else
    mapfile -t failed < <(grep '^FAIL ' "$work/check.out" | head -n 3)
    fail dispatch "$what ($commit)" \
      "the check exited $answer and ended \"$last_line\"${failed[0]:+: $(join_by ' | ' "${failed[@]}")}"
  fi
done

printf '\n-- the daemon image and worker image of every project\n'
pins_what="DAEMON_PINS_PATH lists the project files at ?ref=main"
answer=0
gh api "$pins_dir?ref=main" >"$work/pins.json" 2>"$work/gh.err" || answer=$?
if ((answer != 0)); then
  fail pins "$pins_what" "gh api exited $answer: $(scrub "$(head_of "$work/gh.err")")"
elif ! jq -e 'type == "array"' "$work/pins.json" >/dev/null 2>&1; then
  fail pins "$pins_what" \
    "it names a $(jq -r '.type // "thing"' "$work/pins.json" 2>/dev/null) and not a directory: pass the directory that holds one <nn>-<KEY>.yaml per project, whose files carry each project's image and daemonImage"
else
  project_files=()
  mapfile -t entries < <(jq -r '.[] | select(.type == "file") | .name' "$work/pins.json")
  for name in "${entries[@]}"; do
    if [[ "$name" =~ ^[0-9]+-[A-Z][A-Z0-9]*\.yaml$ ]]; then
      project_files+=("$name")
    else
      printf 'not a project file, so not read: %s\n' "$name"
    fi
  done
  if ((${#project_files[@]} == 0)); then
    fail pins "$pins_what" "the directory holds no <nn>-<KEY>.yaml project file"
  else
    pass pins "$pins_what" "${#project_files[@]} project files: ${project_files[*]}"
    for name in "${project_files[@]}"; do
      read_project "$name"
    done
  fi
fi
for ref in "${images[@]}"; do
  name="$(image_name "$ref")"
  resolve_release "$ref"
  if [[ -n "${release_of[$ref]:-}" ]]; then
    printf 'release of %s (%s): %s, %s\n' "$name" "${image_roles[$ref]}" "${release_of[$ref]}" "${release_how[$ref]}"
    pass image "$name (${image_roles[$ref]}) has a release" "${release_of[$ref]}"
    carries_all image "the release of $name (${image_roles[$ref]}), ${release_of[$ref]}, carries the merge commit of every live child" \
      "${release_of[$ref]}"
  else
    fail image "$name (${image_roles[$ref]}) has a release" "${release_error[$ref]}"
  fi
done

# 2. The decisions step finished.
printf '\n== 2. the decisions step finished\n'
status="$(curl -sS -m 30 -o "$work/healthz" -w '%{http_code}' "$base/healthz" 2>"$work/curl.err")" ||
  status="curl failed: $(head_of "$work/curl.err")"
if [[ "$status" != 200 ]] || ! jq -e 'type == "object"' "$work/healthz" >/dev/null 2>&1; then
  printf '/healthz: %s\n' "$(answer_of "$work/healthz")"
  fail healthz "/healthz answers decisions_step_owed at 0" "it answered $(answer_of "$work/healthz")"
  fail healthz "/healthz names a commit that carries the merge commit of every live child" \
    "it answered $(answer_of "$work/healthz")"
else
  owed="$(jq -r 'if has("decisions_step_owed") then (.decisions_step_owed | tojson) else "absent" end' "$work/healthz")"
  healthz_commit="$(jq -r '.commit // empty | strings' "$work/healthz")"
  printf '/healthz: commit %s, decisions_step_owed %s\n' "${healthz_commit:-absent}" "$owed"
  if [[ "$owed" == 0 ]]; then
    pass healthz "/healthz answers decisions_step_owed at 0" "it reads 0"
  elif [[ "$owed" == absent ]]; then
    fail healthz "/healthz answers decisions_step_owed at 0" \
      "the member is absent: no pass of the decisions step has run in the build production serves, or that build does not hold the step"
  else
    fail healthz "/healthz answers decisions_step_owed at 0" \
      "it reads $owed: the last pass of the decisions step found that much still owed"
  fi
  if [[ "$healthz_commit" =~ ^[0-9a-fA-F]{7,40}$ ]]; then
    carries_all healthz "/healthz names commit $healthz_commit, which carries the merge commit of every live child" \
      "$healthz_commit"
  else
    fail healthz "/healthz names a commit that carries the merge commit of every live child" \
      "its commit reads \"$healthz_commit\", which is no commit"
  fi
fi

# 3. This issue reads as converted, and a done issue does not.
printf '\n== 3. %s reads as converted\n' "$issue"
# tree_converted KEY WANT: GET /api/v1/issues/KEY/tree answers converted: WANT.
tree_converted() {
  local key="$1" want="$2" got what="GET /api/v1/issues/$1/tree answers converted: $2"
  get "/api/v1/issues/$(q "$key")/tree" "$work/tree"
  if [[ "$status" != 200 ]]; then
    printf 'tree of %s: %s\n' "$key" "$(answer_of "$work/tree")"
    fail tree "$what" "it answered $(answer_of "$work/tree")"
    return
  fi
  got="$(jq -r 'if has("converted") then (.converted | tostring) else "absent" end' "$work/tree" 2>/dev/null)"
  printf 'tree of %s: converted %s\n' "$key" "${got:-unreadable}"
  if [[ "$got" == "$want" ]]; then
    pass tree "$what"
  else
    fail tree "$what" "it answered converted ${got:-in no JSON this driver can read}"
  fi
}
tree_converted "$issue" true
tree_converted "$done_issue" false

# 4. The pre-mortem's address is a lens.
printf '\n== 4. the address of the pre-mortem of %s is a lens\n' "$issue"
address="dispatch://$issue/artifact/$slug"
lens=""
what="$address answers a lens at dispatch://lens/<uuid>"
get "/api/v1/references?to=$(q "$address")" "$work/references"
if [[ "$status" != 200 ]]; then
  printf 'references of %s: %s\n' "$address" "$(answer_of "$work/references")"
  fail premortem "$what" "the references read answered $(answer_of "$work/references")"
else
  node_kind="$(jq -r '.node.kind // "none"' "$work/references" 2>/dev/null)"
  node_ref="$(jq -r '.node.ref // .node.id // "none"' "$work/references" 2>/dev/null)"
  printf 'references of %s: a node of kind %s at %s\n' "$address" "${node_kind:-none}" "${node_ref:-none}"
  if [[ "$node_kind" == lens && "$node_ref" =~ ^dispatch://lens/[0-9a-f-]{36}$ ]]; then
    lens="$node_ref"
    pass premortem "$what" "$lens"
  else
    fail premortem "$what" \
      "it answers a node of kind ${node_kind:-none} at ${node_ref:-none}: the pre-mortem has not been converted into a lens"
  fi
fi
what="the lens of $address is of kind pre-mortem with eleven risks under it"
if [[ -z "$lens" ]]; then
  fail premortem "$what" "the references read answered no lens, so there is no lens to read"
else
  get "/api/v1/lenses?on=$(q "$lens")" "$work/lens"
  if [[ "$status" != 200 ]]; then
    fail premortem "$what" "GET /api/v1/lenses?on=$lens answered $(answer_of "$work/lens")"
  else
    lens_kind="$(jq -r '.lens.kind.id // "none"' "$work/lens" 2>/dev/null)"
    risks="$(jq -r '.lens.risks // [] | length' "$work/lens" 2>/dev/null)"
    printf 'lens %s: kind %s, %s risks\n' "$lens" "${lens_kind:-none}" "${risks:-no}"
    if [[ "$lens_kind" == pre-mortem && "$risks" == 11 ]]; then
      pass premortem "$what" "$lens"
    else
      fail premortem "$what" "lens $lens is of kind ${lens_kind:-none}, with ${risks:-no} risks"
    fi
  fi
fi

# 5. The owner approved the version of the spec that states the move of the stores.
printf '\n== 5. the approved spec holds %s\n' "$block_line"
approved=""
approver=""
answer=0
spec_summary >"$work/summary" 2>"$work/summary.err" || answer=$?
what="the spec of $issue has an approved version"
if ((answer != 0)); then
  fail spec "$what" "dispatch read exited $answer: $(head_of "$work/summary.err")"
else
  approval="$(grep -m 1 '^Approval:' "$work/summary")"
  printf 'spec approval as read: %s\n' "${approval:-no Approval line}"
  if [[ "$approval" =~ ^Approval:\ approved\ v([0-9]+)\ by\ ([^,]+) ]]; then
    approved="${BASH_REMATCH[1]}"
    approver="${BASH_REMATCH[2]}"
    pass spec "$what" "v$approved, approved by $approver"
  else
    fail spec "$what" "its summary reads \"${approval:-no Approval line}\""
  fi
fi
version="$approved"
read_as="the version approved by $approver"
if [[ -n "$spec_override" ]]; then
  version="$spec_override"
  replaced="no approved version, since the spec has none"
  [[ -n "$approved" ]] && replaced="v$approved, approved by $approver"
  read_as="read by SPEC_VERSION_OVERRIDE in place of $replaced"
  printf 'SPEC_VERSION_OVERRIDE: v%s in place of %s\n' "$version" "$replaced"
fi
printf 'spec version read: %s\n' "${version:-none}"
what="spec v${version:-?} of $issue holds the line $block_line"
if [[ -z "$version" ]]; then
  fail spec "$what" "there is no version to read: the spec has no approved version"
else
  answer=0
  spec_text "$version" >"$work/spec" 2>"$work/spec.err" || answer=$?
  if ((answer != 0)); then
    fail spec "$what" "dispatch doc-read of v$version, $read_as, exited $answer: $(head_of "$work/spec.err")"
  elif grep -qxF "$block_line" "$work/spec"; then
    pass spec "$what" "$read_as"
  else
    mentions="$(grep -c 'r-stores-moved' "$work/spec")"
    if ((mentions > 0)); then
      mentions="the id r-stores-moved appears on $mentions other line(s), which are not the block"
    else
      mentions="the id r-stores-moved appears nowhere in it"
    fi
    fail spec "$what" "v$version, $read_as, holds no line $block_line, so the block was not found; $mentions"
  fi
fi

printf '\n'
if ((failures > 0)); then
  printf '%d of %d assertions failed\n' "$failures" "$assertions"
  exit 1
fi
printf 'all %d assertions passed\n' "$assertions"
exit 0
