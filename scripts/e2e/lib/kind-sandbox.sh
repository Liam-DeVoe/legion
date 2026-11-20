# shellcheck shell=bash
# A throwaway kind cluster running Agent Sandbox's controller under gVisor, for a live proof of
# Legion's Sandbox runtime on an amd64 Linux host. Sourced by the entry script, never run. The entry
# script defines `note MESSAGE` (one indented observation line) and `fail MESSAGE` (ends the run,
# naming its check) before it calls a function here, and calls kind_sandbox_teardown from its EXIT
# trap, so the cluster goes on any exit.
#
# kind_sandbox_create NAME WORK makes the cluster NAME with its kubeconfig in a file of WORK
# (kind_kubeconfig) and the context kind-NAME (kind_context). Its one node (kind_node) is labelled
# legion.dev/pool=legion, the pool the runtime places every pod on, and the cluster has the legion
# namespace and kind's standard storage class. kind_k runs kubectl against that file and context
# alone, so the caller's kubeconfig and its current context are never read. kind picks the API
# server's host port as one free when it looks, and on a busy host another socket can take it
# before docker binds it. Docker refuses that bind with `address already in use` where another
# process holds the port (`failed to bind host port …`, or `Error starting userland proxy: … bind:
# …` in that proxy's words) and `port is already allocated` where another container does (both
# seen from controller-sweep-kind.sh on 2026-11-20); those two phrases are the stable part. A create
# kind fails on either is made again, the cluster deleted by its name first, up to
# kind_sandbox_create_tries tries, each retry named by a note; a create that fails for any other
# reason fails the run, naming kind's `ERROR:` and docker's `docker:` lines, or kind's last line
# where it printed neither (lib/kind-sandbox.test.ts).
# The verdict of each create is kind's own exit (PIPESTATUS), whether or not the caller sets pipefail.
# kind_sandbox_install_agent_sandbox WORK applies Agent Sandbox's release manifest at the version
# internal/runtime/sandbox speaks, pinned by sha256, and waits for its controller.
# kind_sandbox_install_gvisor WORK installs gVisor's dated release, pinned by sha512, into the
# node's containerd as the runsc handler with a gvisor RuntimeClass, and holds a runtimeClassName:
# gvisor pod to gVisor's kernel.
# kind_sandbox_load_image IMAGE pulls IMAGE, a reference by digest, into the node's containerd under
# that digest, so a pod's start never waits on a registry.
# kind_sandbox_internal_kubeconfig FILE writes the kubeconfig a container on the kind network reads,
# whose server is the node's address on that network.
# kind_sandbox_teardown STATUS EVIDENCE writes, when STATUS is not 0, what the cluster held (pods,
# Sandboxes, volume claims, events and the legion namespace's container logs) to
# EVIDENCE/cluster-at-failure.txt, then deletes the cluster. It does nothing before
# kind_sandbox_create has named a cluster, or once the cluster is gone.
#
# LEGION_E2E_KIND names the kind binary when it is not on PATH.

kind_bin=${LEGION_E2E_KIND:-kind}
kind_cluster=
kind_context=
kind_kubeconfig=
kind_node=
kind_sandbox_create_tries=3

kind_agent_sandbox_url=https://github.com/kubernetes-sigs/agent-sandbox/releases/download/v1.0.3/sandbox.yaml
kind_agent_sandbox_sha256=725fafdabe6aac202a89dc57f1cfe0e2e92f3164c8c2bd343fffca52f7039d96
kind_gvisor_url=https://storage.googleapis.com/gvisor/releases/release/20261012.0/x86_64/gvisor.tar.zstd
kind_gvisor_sha512=ce2e1aeb853c104e854dea6644333a1dba0fbc9dd1646af97e1d013fdf810da6d4176dd96f2139d7b055e5382117c0d7c4567ebd64ed9bef47fdd15f80fd67ab

kind_k() { kubectl --kubeconfig "$kind_kubeconfig" --context "$kind_context" "$@"; }

kind_sandbox_create() {
  kind_cluster=$1 kind_context=kind-$1 kind_kubeconfig=$2/kubeconfig kind_node=$1-control-plane
  local try log bind said
  for ((try = 1; ; try++)); do
    log=$2/kind-create-$try.log
    if "$kind_bin" create cluster --name "$kind_cluster" --kubeconfig "$kind_kubeconfig" --wait 180s 2>&1 | tee "$log"; [ "${PIPESTATUS[0]}" -eq 0 ]; then
      break
    fi
    if ! bind=$(grep -m 1 -E 'address already in use|port is already allocated' "$log"); then
      said=$(grep -E '^(ERROR|docker): ' "$log" | paste -sd ' ' -) || true
      fail "kind could not create cluster $kind_cluster: ${said:-$(grep -v '^[[:space:]]*$' "$log" | tail -n 1)}"
    fi
    [ "$try" -lt "$kind_sandbox_create_tries" ] ||
      fail "kind could not create cluster $kind_cluster in $try tries, each failing on a host port bind; the last: $bind"
    note "kind create cluster try $try of $kind_sandbox_create_tries failed on a host port another socket took ($bind); deleting cluster $kind_cluster and trying again"
    "$kind_bin" delete cluster --name "$kind_cluster" --kubeconfig "$kind_kubeconfig"
  done
  kind_k label node "$kind_node" legion.dev/pool=legion
  kind_k create namespace legion
  kind_k get storageclass standard -o name
  note "kind $("$kind_bin" version), node $(kind_k get node "$kind_node" -o jsonpath='{.status.nodeInfo.kubeletVersion} {.status.allocatable.cpu} cpu {.status.allocatable.memory} memory')"
}

kind_sandbox_install_agent_sandbox() {
  curl -fsSLo "$1/sandbox.yaml" "$kind_agent_sandbox_url"
  echo "$kind_agent_sandbox_sha256  $1/sandbox.yaml" | sha256sum -c -
  kind_k apply -f "$1/sandbox.yaml"
  kind_k -n agent-sandbox-system rollout status deployment/agent-sandbox-controller --timeout=300s
}

kind_sandbox_install_gvisor() {
  curl -fsSLo "$1/gvisor.tar.zstd" "$kind_gvisor_url"
  echo "$kind_gvisor_sha512  $1/gvisor.tar.zstd" | sha512sum -c -
  mkdir -p "$1/gvisor"
  tar --zstd -xf "$1/gvisor.tar.zstd" -C "$1/gvisor"
  local part
  for part in runsc containerd-shim-runsc-v1 gvisor-bin; do
    docker cp "$1/gvisor/$part" "$kind_node:/usr/local/bin/$part"
  done
  docker exec "$kind_node" sh -c 'chown -R root:root /usr/local/bin/gvisor-bin &&
    printf "\n[plugins.\"io.containerd.grpc.v1.cri\".containerd.runtimes.runsc]\n  runtime_type = \"io.containerd.runsc.v1\"\n" >>/etc/containerd/config.toml &&
    systemctl restart containerd'
  kind_k wait --for=condition=Ready "node/$kind_node" --timeout=120s
  kind_k apply -f - <<'EOF'
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: gvisor
handler: runsc
EOF
  kind_k run gvisor-check --image=mirror.gcr.io/library/busybox:1.37 --restart=Never \
    --overrides='{"spec":{"runtimeClassName":"gvisor"}}' -- dmesg
  kind_k wait --for=jsonpath='{.status.phase}'=Succeeded pod/gvisor-check --timeout=180s
  kind_k logs gvisor-check | grep -q 'Starting gVisor' || fail "a gvisor pod's kernel is not gVisor's: $(kind_k logs gvisor-check)"
  note "a runtimeClassName: gvisor pod runs under $(docker exec "$kind_node" /usr/local/bin/runsc --version | head -1)"
  kind_k delete pod gvisor-check --wait=false
}

kind_sandbox_load_image() {
  [[ $1 == *@sha256:* ]] || fail "the image to load must be named by digest (<image>@sha256:<64 hex>), got $1"
  docker exec "$kind_node" crictl pull "$1" >/dev/null
  note "the node's containerd holds $1: $(docker exec "$kind_node" crictl inspecti -o go-template --template '{{.status.size}}' "$1") bytes"
}

kind_sandbox_internal_kubeconfig() {
  "$kind_bin" get kubeconfig --internal --name "$kind_cluster" >"$1"
}

kind_sandbox_teardown() {
  local status=$1 evidence=$2
  [ -n "$kind_cluster" ] || return 0
  "$kind_bin" get clusters 2>/dev/null | grep -qx "$kind_cluster" || return 0
  if [ "$status" -ne 0 ]; then
    {
      kind_k get pods -A -o wide
      kind_k -n legion get sandboxes,persistentvolumeclaims -o yaml
      kind_k -n legion describe pods
      kind_k -n legion get events --sort-by=.lastTimestamp
      kind_k -n legion logs --all-containers --prefix --tail=200 -l legion.dev/project 2>&1
    } >"$evidence/cluster-at-failure.txt" 2>&1 || true
    echo "the cluster's pods, Sandboxes, volume claims, events and container logs at the failure: $evidence/cluster-at-failure.txt" >&2
  fi
  "$kind_bin" delete cluster --name "$kind_cluster" --kubeconfig "$kind_kubeconfig" >/dev/null 2>&1 ||
    echo "could not delete kind cluster $kind_cluster; delete it with: $kind_bin delete cluster --name $kind_cluster" >&2
}
