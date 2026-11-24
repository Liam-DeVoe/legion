# shellcheck shell=bash
# A throwaway kind cluster running Agent Sandbox's controller under gVisor, for a test of Legion's
# Sandbox runtime on an amd64 Linux host: the rig of the kind job of ci.yaml, which
# packages/daemon/internal/kindtest drives from a Go test. Sourced by the caller, never run. The
# caller defines `note MESSAGE` (one indented observation line) and `fail MESSAGE` (ends the run,
# naming its check) before it calls a function here, and calls kind_sandbox_teardown when it ends,
# so the cluster goes on any exit.
#
# Every pin of the rig is here: kind's release, which kind_sandbox_install_kind builds with
# `go install` and kind_sandbox_create holds the binary to; the node image, the local registry,
# busybox, the NFS server and PostgreSQL by digest, each a row of .github/ci-images.yaml, which
# .github/scripts/check-ci-images.sh holds this file to; Agent Sandbox's v1.0.3 manifest, vendored
# beside its CRD in packages/daemon/internal/runtime/sandbox/testdata (the manifest's own header
# says how it differs from the release); and gVisor's dated release by sha512.
#
# The rig's fetches are kind_sandbox_fetch's alone, so a caller that runs it in a set-up step
# leaves the process that runs the rest nothing to fetch: the kind job runs it before its rule,
# and its tests in a cgroup whose outbound connections reach loopback and the kind network's
# bridge alone (.github/scripts/test-fence.sh). The cluster pulls only from the local registry and
# ghcr.io (kind_sandbox_create), and its pods resolve no name outside cluster.local and the rig's
# stand-ins (kind_sandbox_route_stand_ins).
#
# kind_sandbox_install_kind DIR builds the pinned kind into DIR and makes it kind_bin.
# kind_sandbox_fetch DIR fetches what the rig runs: the pinned kind into DIR/kind
# (kind_sandbox_install_kind), gVisor's release into DIR/gvisor.tar.zstd, held to its sha512, and
# the node and registry images into Docker; and it makes the kind network the cluster joins, as
# kind makes it, so a rule naming the network's bridge can stand before the cluster does.
# kind_sandbox_use NAME WORK names the cluster NAME and its files in WORK for the functions below,
# with no cluster made: a caller that runs each function in a shell of its own (internal/kindtest)
# calls it first in each.
# kind_sandbox_create NAME WORK [API_PORT] makes the cluster NAME, on the pinned node image, with
# its kubeconfig in a file of WORK (kind_kubeconfig) and the context kind-NAME (kind_context). Its
# one node (kind_node) is labelled legion.dev/pool=legion, the pool the runtime places every pod
# on, and the cluster has the legion namespace and kind's standard storage class. With API_PORT,
# the API server listens on 127.0.0.1:API_PORT, and its service-account issuer is
# https://127.0.0.1:API_PORT, whose discovery document and keys anyone may read, so a process of
# the host verifies a pod's service-account token as a broker does. Its node's containerd pulls
# from ghcr.io and from no other registry (/etc/containerd/certs.d/_default points every other
# one at kind_no_registry, an address of the node's own where nothing listens), which
# kind_sandbox_start_registry adds the local registry to. kind_k runs kubectl against that file
# and context alone, so the caller's kubeconfig and its current context are never read. This host
# routes the node's pod subnet through the node's address on the kind network (kind_route_pods).
# kind_sandbox_start_registry PORT starts a registry container on the kind network
# (kind_registry), published on the host at 127.0.0.1:PORT, from which the node pulls
# localhost:PORT/<image>: an image pushed there has a registry digest, which
# runtime.kubernetes.image requires.
# kind_sandbox_install_agent_sandbox applies Agent Sandbox's CRD and manifest and waits for its
# controller.
# kind_sandbox_install_gvisor FETCHED WORK installs gVisor's release, which kind_sandbox_fetch
# left in FETCHED, unpacked in WORK, into the node's containerd as the runsc handler with a gvisor
# RuntimeClass, and holds a runtimeClassName: gvisor pod to gVisor's kernel.
# kind_sandbox_load_image IMAGE pulls IMAGE, a reference by digest, into the node's containerd under
# that digest, so a pod's start never waits on a registry.
# kind_sandbox_route_stand_ins ADDRESS GITHUB_PORT SITES_PORT SITE... sends the cluster's pods to
# ADDRESS:GITHUB_PORT for github.com and api.github.com, and to ADDRESS:SITES_PORT for each SITE,
# the public TLS hosts the rig stands in for beside GitHub (internal/kindtest's TLSSites.Hosts:
# the npm registry and the pods' web-search provider): for
# each, a selectorless Service on 443 whose one endpoint is that address, and a CoreDNS answer
# naming its cluster IP (kind_github_address, kind_sites_address). CoreDNS (kind_corefile) answers
# NXDOMAIN for every other name outside cluster.local, with no forward to the runner's resolver,
# and logs each such answer. The rule reaches pods alone: the node, and containerd's pulls,
# resolve through Docker's DNS.
# kind_sandbox_start_nfs_server SEED starts an NFS server in the cluster (NFS-Ganesha, as the
# Kubernetes SIG Storage's nfs-provisioner image ships it), exporting /team, which holds seed.txt
# with SEED, and /scratch, and sets kind_nfs_server to its Service's cluster IP once the node
# itself mounts /team and reads the seed.
# kind_sandbox_start_postgres PASSWORD starts a PostgreSQL server in the cluster (pgvector's image,
# the services: Postgres of CI's other jobs), whose superuser postgres has PASSWORD, on a Service
# the cluster's pods reach at its cluster IP and this host reaches at the node's address on the
# kind network and the Service's node port, once it accepts connections.
# kind_sandbox_teardown STATUS EVIDENCE prints each name CoreDNS answered NXDOMAIN for, saves each
# node's containerd journal to EVIDENCE/containerd-<node>.journal and names each image the nodes
# pulled from it (kind_node_pulls), then, when STATUS is not 0, writes what the cluster held (its
# nodes and pods, the legion namespace's Sandboxes and volume claims, its pods' descriptions and
# container logs and the nfs and postgres namespaces', every event, and the Agent Sandbox
# controller's log) to EVIDENCE, deletes the routes kind_route_pods made, and deletes the cluster
# and the registry. It does nothing before kind_sandbox_use or kind_sandbox_create has named a
# cluster, and deletes nothing once the cluster is gone. It returns 3 when kind_node_pulls fails,
# except for journals with no pull when STATUS is not 0: a rig that failed before its first pull
# leaves those.
#
# LEGION_E2E_KIND names the kind binary when kind_sandbox_install_kind has not run in this shell.

kind_bin=${LEGION_E2E_KIND:-kind}
kind_cluster=
kind_context=
kind_kubeconfig=
kind_node=
kind_registry=
kind_pod_routes=
kind_github_address=
kind_sites_address=
kind_nfs_server=

kind_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)

# The kind release, and the node image its release notes name the default of (Kubernetes
# v1.37.0, kindest/node at this digest).
kind_version=v0.33.0
kind_node_image=ghcr.io/cybermonklabs/legion/ci@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
# The local registry (registry 2.8.3), the busybox of the gVisor check pod (1.37), the NFS server
# (nfs-provisioner v4.0.8), and PostgreSQL with pgvector.
kind_registry_image=ghcr.io/cybermonklabs/legion/ci@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373
kind_busybox_image=docker.io/library/busybox:1.37.0
kind_nfs_image=ghcr.io/cybermonklabs/legion/ci@sha256:c825f3d5e28bde099bd7a3daace28772d412c9157ad47fa752a9ad0baafc118d
kind_postgres_image=ghcr.io/cybermonklabs/legion/ci@sha256:2358fcba361ed2233a5ed81b5fe4ca779ccb304120ce531a3bf51c0ed7e2bc11

kind_agent_sandbox_crd=$kind_root/packages/daemon/internal/runtime/sandbox/testdata/sandboxes.agents.x-k8s.io.yaml
kind_agent_sandbox_manifest=$kind_root/packages/daemon/internal/runtime/sandbox/testdata/agent-sandbox-v1.0.3.yaml
kind_gvisor_url=https://storage.googleapis.com/gvisor/releases/release/20261012.0/x86_64/gvisor.tar.zstd
kind_gvisor_sha512=ce2e1aeb853c104e854dea6644333a1dba0fbc9dd1646af97e1d013fdf810da6d4176dd96f2139d7b055e5382117c0d7c4567ebd64ed9bef47fdd15f80fd67ab
# The kind network, as kind v0.33 makes it when none is there (pkg/cluster/internal/providers/
# docker/network.go): a bridge that masquerades, the default bridge's MTU, and the IPv6 subnet
# kind derives from the name "kind".
kind_network=kind
kind_network_ipv6_subnet=fc00:f853:ccd:e793::/64
# Where the node's containerd sends a pull from a registry the rig does not name: its own loopback,
# on a port nothing listens on, so the pull is refused on the node and no packet leaves it.
kind_no_registry=127.0.0.1:1

kind_k() { kubectl --kubeconfig "$kind_kubeconfig" --context "$kind_context" "$@"; }

kind_sandbox_install_kind() {
  # `go install <module>@<version>` builds the release from its own go.mod, outside any workspace,
  # and reads no file of this repository.
  GOBIN=$1 GOWORK=off GOFLAGS='' go install "sigs.k8s.io/kind@$kind_version"
  kind_bin=$1/kind
  note "$("$kind_bin" version)"
}

kind_sandbox_fetch() {
  local dir=$1 mtu
  mkdir -p "$dir"
  kind_sandbox_install_kind "$dir"
  curl -fsSLo "$dir/gvisor.tar.zstd" "$kind_gvisor_url"
  echo "$kind_gvisor_sha512  $dir/gvisor.tar.zstd" | sha512sum -c -
  docker pull --quiet "$kind_node_image" >/dev/null
  docker pull --quiet "$kind_registry_image" >/dev/null
  note "pulled the node image $kind_node_image and the registry image $kind_registry_image"
  if ! docker network inspect "$kind_network" >/dev/null 2>&1; then
    mtu=$(docker network inspect bridge --format '{{index .Options "com.docker.network.driver.mtu"}}' 2>/dev/null || true)
    local options=(-o com.docker.network.bridge.enable_ip_masquerade=true)
    [[ ! $mtu =~ ^[1-9][0-9]*$ ]] || options+=(-o "com.docker.network.driver.mtu=$mtu")
    docker network create -d=bridge "${options[@]}" --ipv6 --subnet "$kind_network_ipv6_subnet" "$kind_network" >/dev/null
  fi
  note "the $kind_network network: $(docker network inspect "$kind_network" --format '{{range .IPAM.Config}}{{.Subnet}} {{end}}')"
}

kind_sandbox_use() {
  kind_cluster=$1 kind_context=kind-$1 kind_kubeconfig=$2/kubeconfig kind_node=$1-control-plane kind_registry=$1-registry
  kind_pod_routes=$2/pod-routes
}

kind_sandbox_create() {
  kind_sandbox_use "$1" "$2"
  local port=${3:-} config=$2/kind-config.yaml version
  version=$("$kind_bin" version 2>&1) || fail "no kind runs as $kind_bin: $version; build the pinned one with kind_sandbox_install_kind"
  [[ $version == "kind $kind_version "* ]] ||
    fail "the rig is pinned to kind $kind_version, and $kind_bin is $version; build the pinned one with kind_sandbox_install_kind"
  {
    printf 'kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\n'
    [ -z "$port" ] || printf 'networking:\n  apiServerAddress: 127.0.0.1\n  apiServerPort: %s\n' "$port"
    printf 'nodes:\n- role: control-plane\n  image: %s\n' "$kind_node_image"
    # Kubernetes 1.37 takes kubeadm's v1beta4 configuration alone, whose extraArgs is a list of
    # name and value; a patch naming that apiVersion is applied as written, and kubeadm lets an
    # extra argument override its own default (https://kubernetes.default.svc.cluster.local).
    [ -z "$port" ] || cat <<EOF
  kubeadmConfigPatches:
  - |
    apiVersion: kubeadm.k8s.io/v1beta4
    kind: ClusterConfiguration
    apiServer:
      extraArgs:
      - name: service-account-issuer
        value: https://127.0.0.1:$port
      - name: service-account-jwks-uri
        value: https://127.0.0.1:$port/openid/v1/jwks
EOF
  } >"$config"
  "$kind_bin" create cluster --name "$kind_cluster" --config "$config" --kubeconfig "$kind_kubeconfig" --wait 180s
  # containerd reads a registry's hosts.toml under /etc/containerd/certs.d at each pull, and the
  # _default directory's for every registry with no directory of its own: ghcr.io keeps its own
  # server, and every other registry is sent to kind_no_registry, where the pull is refused.
  docker exec "$kind_node" sh -c 'mkdir -p /etc/containerd/certs.d/_default /etc/containerd/certs.d/ghcr.io &&
    printf "server = \"http://%s\"\n" "$1" >/etc/containerd/certs.d/_default/hosts.toml &&
    printf "server = \"https://ghcr.io\"\n" >/etc/containerd/certs.d/ghcr.io/hosts.toml' sh "$kind_no_registry"
  note "the node's containerd pulls from ghcr.io and sends a pull from any other registry to $kind_no_registry"
  kind_k label node "$kind_node" legion.dev/pool=legion
  kind_k create namespace legion
  kind_k get storageclass standard -o name
  if [ -n "$port" ]; then
    kind_k create clusterrolebinding service-account-issuer-discovery-anonymous \
      --clusterrole=system:service-account-issuer-discovery --group=system:unauthenticated
    note "the API server's service-account issuer is https://127.0.0.1:$port; anyone may read its discovery document and keys"
  fi
  note "$("$kind_bin" version), node $(kind_k get node "$kind_node" -o jsonpath='{.status.nodeInfo.kubeletVersion}, allocatable {.status.allocatable.cpu} cpu, {.status.allocatable.memory} memory, {.status.allocatable.ephemeral-storage} ephemeral storage')"
  kind_route_pods
}

# kind_route_pods routes, from this host, the node's pod subnet (its spec.podCIDR, IPv4 as the
# rig's kind config leaves the cluster) through the node's address on the kind network, so a
# process here reaches a pod at the pod's own address, as a daemon outside the cluster does
# (internal/runtime/sandbox's readCacheHealth reads each repository cache's /healthz there): what
# it sends leaves through the kind network's bridge, which the kind job's rule passes. The route
# goes into kind_pod_routes, for kind_sandbox_teardown to delete. Where sudo -n is refused, it says
# that pod addresses are not routed from this host, and goes on.
kind_route_pods() {
  local cidr="" via refused deadline=$((SECONDS + 60))
  if ! refused=$(sudo -n true 2>&1); then
    note "pod addresses are not routed from this host, since sudo -n is refused: $refused"
    return 0
  fi
  # The controller manager gives the node its subnet once the node registers, which --wait
  # covers; the read waits a little for it all the same.
  until [ -n "$cidr" ]; do
    [ "$SECONDS" -lt "$deadline" ] || fail "the node $kind_node has no pod subnet (spec.podCIDR) after 60 s"
    cidr=$(kind_k get node "$kind_node" -o jsonpath='{.spec.podCIDR}')
    [ -n "$cidr" ] || sleep 1
  done
  via=$(docker inspect --format "{{with index .NetworkSettings.Networks \"$kind_network\"}}{{.IPAddress}}{{end}}" "$kind_node")
  [ -n "$via" ] || fail "the node $kind_node has no address on the $kind_network network to route $cidr through"
  sudo -n ip route replace "$cidr" via "$via"
  printf '%s %s\n' "$cidr" "$via" >>"$kind_pod_routes"
  note "this host routes the cluster's pod subnet: $(ip route show "$cidr")"
}

kind_sandbox_start_registry() {
  local port=$1 hosts=/etc/containerd/certs.d/localhost:$1
  docker run -d --name "$kind_registry" --network kind -p "127.0.0.1:${port}:5000" "$kind_registry_image" >/dev/null
  # kind's node images read /etc/containerd/certs.d (config_path) since kind v0.27.0, so this
  # mirror needs no restart of containerd.
  docker exec "$kind_node" mkdir -p "$hosts"
  printf '[host."http://%s:5000"]\n' "$kind_registry" | docker exec -i "$kind_node" cp /dev/stdin "$hosts/hosts.toml"
  kind_k apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata: {name: local-registry-hosting, namespace: kube-public}
data:
  localRegistryHosting.v1: |
    host: "localhost:$port"
EOF
  local _
  for _ in $(seq 1 30); do
    curl -fsS "http://127.0.0.1:${port}/v2/" >/dev/null 2>&1 && break
    sleep 1
  done
  curl -fsS "http://127.0.0.1:${port}/v2/" >/dev/null || fail "the registry $kind_registry does not answer on 127.0.0.1:$port"
  note "registry $kind_registry on the kind network, localhost:$port to the host and the node"
}

kind_sandbox_install_agent_sandbox() {
  kind_k apply -f "$kind_agent_sandbox_crd"
  # `kubectl wait --for=condition=` refuses a CRD whose status holds no conditions yet, which a
  # wait issued right after the create can meet (CI run 44538419665: ".status.conditions accessor
  # error: <nil>"), so this reads the condition until it is True, for at most 60 seconds.
  local established="" deadline=$((SECONDS + 60))
  until [ "$established" = True ]; do
    [ "$SECONDS" -lt "$deadline" ] || fail "the Sandbox CRD is not Established after 60 s"
    sleep 1
    established=$(kind_k get crd/sandboxes.agents.x-k8s.io \
      -o jsonpath='{.status.conditions[?(@.type=="Established")].status}' 2>/dev/null || true)
  done
  kind_k apply -f "$kind_agent_sandbox_manifest"
  kind_k -n agent-sandbox-system rollout status deployment/agent-sandbox-controller --timeout=300s
}

kind_sandbox_install_gvisor() {
  local archive=$1/gvisor.tar.zstd
  echo "$kind_gvisor_sha512  $archive" | sha512sum -c - ||
    fail "$archive is not gVisor's pinned release; fetch it with kind_sandbox_fetch"
  mkdir -p "$2/gvisor"
  tar --zstd -xf "$archive" -C "$2/gvisor"
  local part
  for part in runsc containerd-shim-runsc-v1 gvisor-bin; do
    docker cp "$2/gvisor/$part" "$kind_node:/usr/local/bin/$part"
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
  kind_k run gvisor-check --image="$kind_busybox_image" --restart=Never \
    --overrides='{"spec":{"runtimeClassName":"gvisor"}}' -- dmesg
  kind_k wait --for=jsonpath='{.status.phase}'=Succeeded pod/gvisor-check --timeout=180s
  # The log is read whole before it is matched: `kind_k logs | grep -q` fails under pipefail when
  # grep's early exit stops kubectl with SIGPIPE, as in CI run 44865043944, whose log held the line.
  local logs
  logs=$(kind_k logs gvisor-check)
  [[ $logs == *'Starting gVisor'* ]] || fail "a gvisor pod's kernel is not gVisor's: $logs"
  note "a runtimeClassName: gvisor pod runs under $(docker exec "$kind_node" /usr/local/bin/runsc --version | head -1)"
  kind_k delete pod gvisor-check --wait=false
}

kind_sandbox_load_image() {
  [[ $1 == *@sha256:* ]] || fail "the image to load must be named by digest (<image>@sha256:<64 hex>), got $1"
  docker exec "$kind_node" crictl pull "$1" >/dev/null
  note "the node's containerd holds $1: $(docker exec "$kind_node" crictl inspecti -o go-template --template '{{.status.size}}' "$1") bytes"
}

# kind_stand_in_service NAMESPACE NAME ADDRESS PORT applies a selectorless Service NAME on 443 in
# NAMESPACE whose one endpoint is ADDRESS:PORT, and prints its cluster IP.
kind_stand_in_service() {
  local namespace=$1 name=$2 address=$3 port=$4 ip
  kind_k apply -f - >&2 <<EOF
apiVersion: v1
kind: Namespace
metadata: {name: $namespace}
---
apiVersion: v1
kind: Service
metadata: {name: $name, namespace: $namespace}
spec:
  ports: [{name: https, port: 443, protocol: TCP}]
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: $name
  namespace: $namespace
  labels: {kubernetes.io/service-name: $name}
addressType: IPv4
ports: [{name: https, port: $port, protocol: TCP}]
endpoints: [{addresses: ["$address"]}]
EOF
  ip=$(kind_k -n "$namespace" get service "$name" -o jsonpath='{.spec.clusterIP}')
  [ -n "$ip" ] || fail "the Service $namespace/$name has no cluster IP"
  printf '%s\n' "$ip"
}

# kind_corefile GITHUB_IP SITES_IP SITE... prints the Corefile kind_sandbox_route_stand_ins gives
# CoreDNS. Its first server block is kind's own for cluster.local and the reverse zones, without
# the forward to the node's resolver and without the kubernetes plugin's fallthrough to it, so a
# name there that the cluster does not hold is NXDOMAIN. The second takes every other name:
# github.com and api.github.com are GITHUB_IP and each SITE is SITES_IP, any other record of
# those names is none, and every other name is NXDOMAIN, which its log plugin logs (the
# refusals kind_sandbox_teardown prints). That block is templates alone: CoreDNS runs its
# template plugin before hosts, and hosts answers a name it does not hold SERVFAIL, not NXDOMAIN.
kind_corefile() {
  local github=$1 sites=$2 site pattern=
  shift 2
  for site in "$@"; do pattern+="${pattern:+|}${site//./[.]}"; done
  cat <<EOF
cluster.local:53 in-addr.arpa:53 ip6.arpa:53 {
    errors
    health {
       lameduck 5s
    }
    ready
    kubernetes cluster.local in-addr.arpa ip6.arpa {
       pods insecure
       ttl 30
    }
    prometheus :9153
    cache 30 {
       disable success cluster.local
       disable denial cluster.local
    }
    loop
    reload
    loadbalance
}
.:53 {
    errors
    log . {
       class denial
    }
    template IN A github.com api.github.com {
       match "^(?:api[.])?github[.]com[.]\$"
       answer "{{ .Name }} 30 IN A $github"
       fallthrough
    }
    template IN A $* {
       match "^(?:$pattern)[.]\$"
       answer "{{ .Name }} 30 IN A $sites"
       fallthrough
    }
    template ANY ANY github.com api.github.com $* {
       match "^(?:api[.])?github[.]com[.]\$"
       match "^(?:$pattern)[.]\$"
       rcode NOERROR
       authority "{{ .Zone }} 30 IN SOA ns.kind-rig.invalid. hostmaster.kind-rig.invalid. 1 30 30 30 30"
       fallthrough
    }
    template ANY ANY {
       rcode NXDOMAIN
       authority ". 30 IN SOA ns.kind-rig.invalid. hostmaster.kind-rig.invalid. 1 30 30 30 30"
    }
}
EOF
}

kind_sandbox_route_stand_ins() {
  local address=$1 github_port=$2 sites_port=$3 corefile
  shift 3
  [ "$#" -gt 0 ] || fail "kind_sandbox_route_stand_ins names no TLS site to route"
  kind_github_address=$(kind_stand_in_service fake-github github "$address" "$github_port")
  kind_sites_address=$(kind_stand_in_service tls-sites sites "$address" "$sites_port")
  corefile=$(kind_k -n kube-system get configmap coredns -o jsonpath='{.data.Corefile}')
  [[ $corefile == *".:53 {"* ]] || fail "CoreDNS's Corefile has no .:53 server block to route the stand-ins in: $corefile"
  [[ $corefile != *"template "* ]] || fail "CoreDNS's Corefile holds a template already, and the rig routes the stand-ins once: $corefile"
  corefile=$(kind_corefile "$kind_github_address" "$kind_sites_address" "$@")
  kind_k -n kube-system create configmap coredns --from-literal=Corefile="$corefile" --dry-run=client -o yaml |
    kind_k replace -f -
  # A restart rather than CoreDNS's reload, which waits on the ConfigMap volume's sync and then
  # 30 s and its jitter; the restart also drops the cache's answers for the names.
  kind_k -n kube-system rollout restart deployment/coredns
  kind_k -n kube-system rollout status deployment/coredns --timeout=180s
  note "github.com and api.github.com resolve, for the cluster's pods, to $kind_github_address:443, whose endpoint is $address:$github_port"
  note "$* resolve, for the cluster's pods, to $kind_sites_address:443, whose endpoint is $address:$sites_port"
  note "CoreDNS answers NXDOMAIN for every other name outside cluster.local, and forwards nothing to the node's resolver"
}

kind_sandbox_start_nfs_server() {
  local seed=$1 mounted
  kind_k create namespace nfs
  kind_k apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata: {name: ganesha, namespace: nfs}
data:
  ganesha.conf: |
    NFS_Core_Param { Protocols = 4; }
    NFSV4 { Grace_Period = 0; Lease_Lifetime = 10; Minor_Versions = 0; }
    EXPORT_DEFAULTS { SecType = sys; Squash = no_root_squash; }
    EXPORT { Export_Id = 1; Path = /export/team; Pseudo = /team; Access_Type = RW; FSAL { Name = VFS; } }
    EXPORT { Export_Id = 2; Path = /export/scratch; Pseudo = /scratch; Access_Type = RW; FSAL { Name = VFS; } }
---
apiVersion: v1
kind: Pod
metadata: {name: ganesha, namespace: nfs, labels: {app: ganesha}}
spec:
  containers:
  - name: ganesha
    image: $kind_nfs_image
    command:
    - sh
    - -c
    - >-
      mkdir -p /export/team /export/scratch /run/dbus /usr/local/var/run/ganesha &&
      printf '%s\n' "\$SEED" >/export/team/seed.txt && chmod 0777 /export/team /export/scratch &&
      rpcbind -w && rpc.statd --port 662 && dbus-daemon --system --nopidfile &&
      exec ganesha.nfsd -F -L /dev/stdout -f /etc/ganesha/ganesha.conf
    env: [{name: SEED, value: "$seed"}]
    securityContext: {capabilities: {add: [DAC_READ_SEARCH, SYS_RESOURCE]}}
    readinessProbe: {tcpSocket: {port: 2049}, periodSeconds: 2}
    resources: {requests: {cpu: 100m, memory: 64Mi}, limits: {memory: 512Mi}}
    volumeMounts: [{name: export, mountPath: /export}, {name: conf, mountPath: /etc/ganesha}]
  volumes:
  - {name: export, emptyDir: {}}
  - {name: conf, configMap: {name: ganesha}}
---
apiVersion: v1
kind: Service
metadata: {name: ganesha, namespace: nfs}
spec: {selector: {app: ganesha}, ports: [{name: nfs, port: 2049}]}
EOF
  kind_k -n nfs wait --for=condition=Ready pod/ganesha --timeout=180s
  kind_nfs_server=$(kind_k -n nfs get service ganesha -o jsonpath='{.spec.clusterIP}')
  [ -n "$kind_nfs_server" ] || fail "the NFS server's Service has no cluster IP"
  mounted=$(docker exec "$kind_node" sh -c "mkdir -p /tmp/share-check && mount -t nfs $kind_nfs_server:/team /tmp/share-check && cat /tmp/share-check/seed.txt; umount /tmp/share-check" 2>&1) ||
    fail "the kind node cannot mount $kind_nfs_server:/team: $mounted"
  [ "$mounted" = "$seed" ] || fail "the node read $mounted from $kind_nfs_server:/team, not the seed"
  note "NFS-Ganesha at $kind_nfs_server exports /team (seed.txt: $seed) and /scratch; the node mounts /team and reads the seed"
}

kind_sandbox_start_postgres() {
  local password=$1
  kind_k create namespace postgres
  # The data directory is the pod's own: one level above it is mounted, which is where both the
  # image's PostgreSQL 17 (/var/lib/postgresql/data) and 18 (/var/lib/postgresql/18) keep theirs.
  kind_k apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata: {name: postgres, namespace: postgres, labels: {app: postgres}}
spec:
  containers:
  - name: postgres
    image: $kind_postgres_image
    env: [{name: POSTGRES_PASSWORD, value: "$password"}]
    readinessProbe: {exec: {command: [pg_isready, -q, -h, 127.0.0.1, -U, postgres]}, periodSeconds: 2}
    resources: {requests: {cpu: 100m, memory: 256Mi}, limits: {memory: 1Gi}}
    volumeMounts: [{name: data, mountPath: /var/lib/postgresql}]
  volumes:
  - {name: data, emptyDir: {}}
---
apiVersion: v1
kind: Service
metadata: {name: postgres, namespace: postgres}
spec: {type: NodePort, selector: {app: postgres}, ports: [{name: postgres, port: 5432}]}
EOF
  kind_k -n postgres wait --for=condition=Ready pod/postgres --timeout=180s
  note "PostgreSQL at $(kind_k -n postgres get service postgres -o jsonpath='{.spec.clusterIP}:5432') in the cluster, node port $(kind_k -n postgres get service postgres -o jsonpath='{.spec.ports[0].nodePort}'), accepting connections"
}

# kind_node_pulls JOURNAL... names each image a kind node's containerd pulled, read from JOURNAL,
# the node's containerd journal as `journalctl -u containerd -o cat` prints it, in a file named
# containerd-<node>.journal: the CRI plugin's line for a pull that succeeded (`PullImage "<ref>"
# returns image reference "<id>"`), since images kind load put in the node and the node image's
# own are no pull, and a refused pull returns no reference. It prints `CHECK kind-node-pulls:
# PASS` when each is ghcr.io/cybermonklabs/legion/ci by digest or the rig's registry
# (localhost:<port>); it prints `CHECK kind-node-pulls: FAIL` and returns 3 when one is neither,
# and returns 4 when no journal holds a pull, since every rig that reaches its test pulls the
# gVisor check's busybox and its own image: a journal with no pull is one the reader did not read,
# not a cluster that pulled nothing, unless the rig failed before its first pull.
kind_node_pulls() {
  local journal node pulled ref outside=0 pulls=0
  for journal in "$@"; do
    node=${journal##*/containerd-}
    node=${node%.journal}
    pulled=$(sed -n 's/.*PullImage \\"\([^\\"]*\)\\" returns image reference.*/\1/p' "$journal" 2>/dev/null | sort -u || true)
    while read -r ref; do
      [ -n "$ref" ] || continue
      pulls=$((pulls + 1))
      case "$ref" in
        ghcr.io/cybermonklabs/legion/ci@sha256:*) note "the node $node pulled $ref" ;;
        localhost:[0-9]*/*) note "the node $node pulled $ref from the rig's registry" ;;
        *) note "the node $node pulled $ref, which is neither ghcr.io/cybermonklabs/legion/ci by digest nor the rig's registry"; outside=1 ;;
      esac
    done <<<"$pulled"
  done
  if [ "$outside" -ne 0 ]; then
    note "CHECK kind-node-pulls: FAIL: a node pulled an image from outside ghcr.io/cybermonklabs/legion/ci and the rig's registry, each line above"
    return 3
  fi
  if [ "$pulls" -eq 0 ]; then
    note "CHECK kind-node-pulls: FAIL: no containerd journal of the nodes ($*) holds the line of a pull that succeeded, and every rig pulls at least its own image"
    return 4
  fi
  note "CHECK kind-node-pulls: PASS: each of the $pulls images the nodes pulled is ghcr.io/cybermonklabs/legion/ci by digest or the rig's registry"
}

# kind_sandbox_teardown STATUS EVIDENCE: names each name CoreDNS refused, saves each node's
# containerd journal in EVIDENCE and names each image the nodes pulled (kind_node_pulls), keeps
# the cluster's state in EVIDENCE when STATUS is not 0, then deletes the cluster, the routes
# kind_route_pods made and the registry. It returns 3, after the deletes, when kind_node_pulls
# failed, which the rig reports as the test's failure; a journal with no pull is no failure of its
# own when STATUS is not 0, since a rig that failed before its first pull leaves one.
kind_sandbox_teardown() {
  local status=$1 evidence=$2 namespace pod clusters refused node journals=() verdict=0 cidr via out
  [ -n "$kind_cluster" ] || return 0
  # Read whole, not piped into grep -q, whose early exit is the pipeline's failure under pipefail.
  clusters=$("$kind_bin" get clusters 2>/dev/null || true)
  if grep -qx -- "$kind_cluster" <<<"$clusters"; then
    # Each name CoreDNS's catch-all answered NXDOMAIN for, from the log plugin of the server block
    # that holds every name outside cluster.local (kind_corefile), with how many queries asked it.
    refused=$(kind_k -n kube-system logs -l k8s-app=kube-dns --tail=-1 2>/dev/null |
      sed -n 's/.*"[A-Z0-9]* IN \([^ ]*\) [a-z]* [^"]*" NXDOMAIN .*/\1/p' | sort | uniq -c || true)
    if [ -n "$refused" ]; then
      while read -r count name; do note "CoreDNS refused $name: NXDOMAIN to $count queries"; done <<<"$refused"
    else
      note "CoreDNS refused no name"
    fi
    # Each node's containerd journal, which the kind job uploads with the rest of EVIDENCE, and
    # the images the nodes pulled, read from those very files.
    mkdir -p "$evidence"
    for node in $("$kind_bin" get nodes --name "$kind_cluster" 2>/dev/null); do
      docker exec "$node" journalctl -u containerd --no-pager -o cat >"$evidence/containerd-$node.journal" 2>/dev/null ||
        note "could not read the containerd journal of the node $node"
      journals+=("$evidence/containerd-$node.journal")
    done
    kind_node_pulls "${journals[@]+"${journals[@]}"}" || verdict=$?
    if [ "$verdict" -eq 4 ] && [ "$status" -ne 0 ]; then
      note "the test failed already (status $status), so a journal with no pull is no second failure: the rig may have failed before its first pull"
      verdict=0
    fi
    if [ "$status" -ne 0 ]; then
      {
        kind_k get nodes -o wide
        kind_k get pods -A -o wide
        kind_k -n legion get sandboxes,persistentvolumeclaims -o yaml
        for namespace in legion nfs postgres; do
          kind_k -n "$namespace" describe pods
          for pod in $(kind_k -n "$namespace" get pods -o name 2>/dev/null); do
            kind_k -n "$namespace" logs "$pod" --all-containers --prefix --tail=200
          done
        done
        kind_k get events -A --sort-by=.lastTimestamp
      } >"$evidence/cluster-at-failure.txt" 2>&1 || true
      kind_k -n agent-sandbox-system logs deployment/agent-sandbox-controller --all-containers --tail=2000 \
        >"$evidence/agent-sandbox-controller.log" 2>&1 || true
      echo "the cluster's pods, Sandboxes, volume claims, events and logs at the failure: $evidence/cluster-at-failure.txt and $evidence/agent-sandbox-controller.log" >&2
    fi
    "$kind_bin" delete cluster --name "$kind_cluster" --kubeconfig "$kind_kubeconfig" >/dev/null 2>&1 ||
      echo "could not delete kind cluster $kind_cluster; delete it with: $kind_bin delete cluster --name $kind_cluster" >&2
  fi
  if [ -s "$kind_pod_routes" ]; then
    while read -r cidr via; do
      if out=$(sudo -n ip route del "$cidr" via "$via" 2>&1); then
        note "this host no longer routes the pod subnet $cidr through $via"
      else
        note "could not delete this host's route of $cidr through $via: $out"
      fi
    done <"$kind_pod_routes"
    rm -f "$kind_pod_routes"
  fi
  docker rm -f "$kind_registry" >/dev/null 2>&1 || true
  [ "$verdict" -eq 0 ] || return 3
}
