#!/usr/bin/env bash
# The shared clone live proof on a throwaway kind cluster: an issue pod the daemon's own code
# launches under gVisor provisions its clone with `git clone --shared` from the read-only feed, so
# the clone reads the feed's objects through `.git/objects/info/alternates` on `/legion/feed` and
# the pod's disk holds the repository once. The pod is built by Legion's own code: the daemon's
# loader reads the legion.yaml, its sandboxOptions builds the Agent Sandbox runtime, which launches
# the issue's root through Agent Sandbox v1.0.3's controller (internal/daemon's
# shared_clone_live_test.go), on a public repository of some tens of megabytes whose feed the pod's
# workspace-fetch fills from GitHub. The checks read the pod as its roles container sees it: both
# init containers ended 0, the container's kernel is gVisor's, the clone's alternates name the
# feed's objects and nothing else, `du -s` of the clone's `.git/objects` is under a tenth of the
# feed's objects (a plain local clone of the same feed, made beside it in the container as the
# release before made the pod's, copies them, and its `du -s` is printed as the control), the feed
# is read-only there, `git fsck --connectivity-only` passes in the clone, and `git log` and
# `jj log` of the default branch succeed in the clone as the launched architect's agent runs them.
# Each check prints `== <name>`, what it observed and `ok <name> (<s>s)`; the first that fails ends
# the run non-zero, naming it.
#
# Run it from a checkout on an amd64 (x86_64) Linux host — the gVisor release and the static
# binaries it builds are amd64 only — as
#   bash scripts/e2e/shared-clone-kind.sh
# with docker, go, kubectl, curl, jq, zstd, a tar that supports --zstd, and kind (LEGION_E2E_KIND
# names the binary when it is not on PATH) installed. The pod runs the worker image of the
# checkout's own commit: LEGION_E2E_IMAGE names it by digest, and where it is unset the run reads
# the digest of `ghcr.io/cybermonklabs/legion-worker:sha-<the first 12 hex of HEAD>`, which
# `.github/workflows/worker-image.yaml` publishes for a pull request's head, and fails naming the
# tag when the registry has none. The image's own `legion` runs the pod's workspace-fetch and
# workspace-init, so the image must be built from the commit under proof. The cluster is
# lib/kind-sandbox.sh's, the run's own, and every kubectl call names its kubeconfig and context, so
# the caller's kubeconfig is never read. The cluster is deleted on any exit;
# SHARED_CLONE_EVIDENCE_DIR (default a fresh /tmp directory, kept and printed) keeps the
# transcript, the legion.yaml and the issue pod's manifest as the API server holds it.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
evidence=${SHARED_CLONE_EVIDENCE_DIR:-$(mktemp -d /tmp/legion-e2e-shared-clone-evidence.XXXXXXXX)}
mkdir -p "$evidence"
# shellcheck source-path=SCRIPTDIR source=lib/transcript.sh
. "$root/scripts/e2e/lib/transcript.sh"
transcript_to "$evidence/transcript.log"
# shellcheck source-path=SCRIPTDIR source=lib/kind-sandbox.sh
. "$root/scripts/e2e/lib/kind-sandbox.sh"
# shellcheck source-path=SCRIPTDIR source=lib/role-namespaces.sh
. "$root/scripts/e2e/lib/role-namespaces.sh"
# shellcheck source-path=SCRIPTDIR source=lib/run-verdict.sh
. "$root/scripts/e2e/lib/run-verdict.sh"

cluster=legion-shared-clone-$$
work=$(mktemp -d "/tmp/legion-e2e-shared-clone.$$.XXXXXXXX")
check=setup
started=$SECONDS
check_started=$SECONDS

begin() { check=$1; check_started=$SECONDS; echo "== $check"; }
note() { echo "   $*"; }
pass() { echo "ok $check ($((SECONDS - check_started))s)"; }
fail() { echo "FAIL $check: $*" >&2; exit 1; }

# cleanup tears the run down and prints its last line through lib/run-verdict.sh: PASS only for a
# run that reached run_end, the script's last statement.
cleanup() {
  local status=$? ended
  ended=$(run_status "$status")
  docker rm -f "$cluster-driver" >/dev/null 2>&1 || true
  kind_sandbox_teardown "$ended" "$evidence"
  rm -rf "$work"
  run_verdict "$status" \
    "shared clone kind e2e: PASS in $((SECONDS - started))s (evidence: $evidence)" \
    "shared clone kind e2e: FAIL at $check after $((SECONDS - started))s (evidence: $evidence)"
}
trap cleanup EXIT

# on_kind NAME COMMAND… runs a static binary of the work directory in a container on the kind
# network beside the node, as share-volumes-kind.sh's does: the pods reach the kind network's
# containers, while a host firewall can drop what they send the host at the network's gateway.
on_kind() {
  local name=$1
  shift
  docker run --rm --name "$cluster-$name" --cpus 2 --memory 2g --network kind --user "$(id -u):$(id -g)" \
    -v "$work:$work" -e HOME=/tmp "$@"
}

# worker_image is the image the run's pod runs: LEGION_E2E_IMAGE, or the digest the registry gives
# the checkout's own commit's sha- tag.
worker_image() {
  if [ -n "${LEGION_E2E_IMAGE:-}" ]; then
    echo "$LEGION_E2E_IMAGE"
    return
  fi
  local tag token digest
  tag=sha-$(git -C "$root" rev-parse HEAD | cut -c1-12)
  token=$(curl -fsS "https://ghcr.io/token?scope=repository:cybermonklabs/legion-worker:pull" | jq -er .token) ||
    fail "no anonymous pull token for ghcr.io/cybermonklabs/legion-worker"
  # The pull token goes to curl on standard input, never as an argument every process can read.
  digest=$(printf 'Authorization: Bearer %s\n' "$token" | curl -fsSI -H @- \
    -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json' \
    "https://ghcr.io/v2/cybermonklabs/legion-worker/manifests/$tag" | tr -d '\r' | awk 'tolower($1) == "docker-content-digest:" {print $2}') || true
  [[ $digest == sha256:* ]] ||
    fail "ghcr.io/cybermonklabs/legion-worker has no $tag; set LEGION_E2E_IMAGE to the worker image of $(git -C "$root" rev-parse HEAD), by digest, or run worker-image.yaml on it"
  echo "ghcr.io/cybermonklabs/legion-worker@$digest"
}

begin prerequisites
[ "$(uname -m)" = x86_64 ] || fail "the proof runs on amd64 (x86_64) only, since the gVisor release it installs and the binaries it builds are amd64; this host is $(uname -m)"
for tool in docker go kubectl curl jq zstd tar sha256sum sha512sum "$kind_bin"; do
  command -v "$tool" >/dev/null || fail "$tool is not on PATH"
done
[[ $(tar --help 2>/dev/null) == *--zstd* ]] || fail "this tar has no --zstd; install GNU tar 1.31 or later"
image=$(worker_image)
[[ $image == *@sha256:* ]] || fail "LEGION_E2E_IMAGE must name the worker image by digest (<image>@sha256:<64 hex>)"
note "worker image $image, for $(git -C "$root" rev-parse HEAD)"
pass

begin binaries
CGO_ENABLED=0 go -C "$root/packages/daemon" test -c -tags e2e -o "$work/shared-clone.test" ./internal/daemon
pass

begin cluster
kind_sandbox_create "$cluster" "$work"
pass

begin agent-sandbox
kind_sandbox_install_agent_sandbox "$work"
pass

begin gvisor
kind_sandbox_install_gvisor "$work"
pass

begin images
kind_sandbox_load_image "$image"
kind_sandbox_internal_kubeconfig "$work/kubeconfig-internal"
pass

begin launch
result=$work/result
# shellcheck disable=SC2016 # expanded by the driver container's shell
on_kind driver \
  -e LEGION_E2E_KUBECONFIG="$work/kubeconfig-internal" -e LEGION_E2E_KUBE_CONTEXT="$kind_context" \
  -e LEGION_E2E_IMAGE="$image" -e LEGION_E2E_WORK="$work" -e LEGION_E2E_RESULT="$result" \
  mirror.gcr.io/library/busybox:1.37 sh -c 'LEGION_E2E_STREAM_HOST=$(ip -4 -o addr show eth0 | awk "{print \$4}" | cut -d/ -f1) exec "$0" -test.count=1 -test.timeout=30m -test.v -test.run "^TestASharedCloneReadsTheFeedInAnIssuePodLive\$"' "$work/shared-clone.test" ||
  fail "the launch driver failed"
read -r pod repo < "$result" || true
[ -n "${pod:-}" ] && [ -n "${repo:-}" ] || fail "the launch driver recorded no pod"
pod=${pod#*/}
cp "$work/shared-clone/legion.yaml" "$evidence/legion.yaml"
kind_k -n legion get pod "$pod" -o yaml >"$evidence/pod.yaml"
note "pod $pod on $repo"
pass

container=roles
in_pod() { kind_k -n legion exec "$pod" -c "$container" -- "$@"; }
clone=/legion/repos/github.com/$repo
feed=/legion/feed/$repo.git

begin provision
# shellcheck disable=SC2016 # a kubectl JSONPath, not the shell's
ends=$(kind_k -n legion get pod "$pod" -o jsonpath='{range .status.initContainerStatuses[*]}{.name}={.state.terminated.exitCode} {end}')
[ "$ends" = "workspace-fetch=0 workspace-init=0 " ] || fail "the pod's init containers ended $ends, want workspace-fetch=0 workspace-init=0"
note "init containers: $ends"
pass

begin gvisor-kernel
kernel=$(in_pod sh -c 'dmesg 2>/dev/null | head -1')
[[ $kernel == *gVisor* ]] || fail "container $container's kernel is not gVisor's: $kernel"
note "container $container: $kernel"
pass

begin alternates
alternates=$(in_pod cat "$clone/.git/objects/info/alternates") || fail "the clone at $clone has no .git/objects/info/alternates"
[ "$alternates" = "$feed/objects" ] || fail "the clone's alternates name $alternates, want the feed's objects $feed/objects alone"
note "cat $clone/.git/objects/info/alternates → $alternates"
packs=$(in_pod sh -c "ls $clone/.git/objects/pack | grep -c '\\.pack\$' || true")
note "packs in the clone's own objects/pack: $packs"
[ "$packs" = 0 ] || fail "the clone's own objects/pack holds $packs packs, want none of the feed's"
pass

begin disk
clone_kib=$(in_pod du -sk "$clone/.git/objects" | awk '{print $1}')
feed_kib=$(in_pod du -sk "$feed/objects" | awk '{print $1}')
note "du -sk $clone/.git/objects → $clone_kib; du -sk $feed/objects → $feed_kib"
[ $((clone_kib * 10)) -lt "$feed_kib" ] || fail "the clone's objects take $clone_kib KiB, not under a tenth of the feed's $feed_kib KiB"
# The control: a plain local clone of the same read-only feed, as the release before made the
# pod's (`git clone --no-checkout`), copies every object across the two mounts.
control=$(in_pod sh -c "d=\$(mktemp -d) && git clone --quiet --no-checkout $feed \$d/plain >/dev/null && du -sk \$d/plain/.git/objects | cut -f1; rm -rf \$d") ||
  fail "the control's plain clone of $feed failed: $control"
note "control: du -sk of a plain git clone --no-checkout of the feed's objects → $control"
[ $((control * 10)) -ge "$feed_kib" ] || fail "the control's plain clone takes $control KiB, under a tenth of the feed's $feed_kib: the control shows nothing"
pass

begin feed-read-only
if refused=$(in_pod sh -c "touch $feed/written-by-roles" 2>&1); then
  fail "container $container wrote to $feed, which it mounts read-only"
fi
[[ $refused == *"Read-only file system"* ]] || fail "the write to $feed failed for another reason: $refused"
note "touch $feed/written-by-roles → refused: $(tr '\n' ' ' <<<"$refused")"
pass

begin fsck
fsck=$(in_pod git --git-dir="$clone/.git" fsck --connectivity-only --no-dangling --no-progress 2>&1) ||
  fail "git fsck --connectivity-only of the clone: $fsck"
note "git fsck --connectivity-only of $clone: ok $(tr '\n' ' ' <<<"$fsck")"
pass

begin logs
# As the launched architect's agent runs them (role_enter_sh): inside its role-init's namespaces,
# as its shim's user. The launch driver's worker stream is gone once it exits, and the shim redials
# it without end, so the role's namespaces stay up for the checks.
role=architect
role_exec() { kind_k -n legion exec "$pod" -c "$container" -- sh -c "$role_enter_sh" role-enter "$role" "$@"; }
for _ in $(seq 1 90); do
  role_exec true >/dev/null 2>&1 && break
  sleep 2
done
git_log=$(role_exec git --git-dir="$clone/.git" log -1 --format='%H %s' refs/remotes/origin/master 2>&1) ||
  fail "git log of origin/master in the clone, as role $role: $git_log"
note "git log -1 origin/master → $git_log"
# shellcheck disable=SC2016 # a jj template, not the shell's
jj_log=$(role_exec sh -c "cd $clone && jj log --ignore-working-copy --no-graph --color=never -r 'master@origin' -T 'commit_id ++ \" \" ++ description.first_line() ++ \"\\n\"'" 2>&1) ||
  fail "jj log of master@origin in the clone, as role $role: $jj_log"
note "jj log -r master@origin → $jj_log"
[ "${git_log%% *}" = "${jj_log%% *}" ] || fail "git and jj read origin's master as ${git_log%% *} and ${jj_log%% *}"
pass
run_end
