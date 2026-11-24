#!/usr/bin/env bash
# .github/scripts/test-fence.sh
#
# Fences a CI step's test process: the process, and every process it starts, runs in a cgroup v2
# of its own, legion-test, and an iptables and an ip6tables rule matched on that cgroup
# (`-m cgroup --path legion-test`) let its outbound connections reach loopback and the bridges of
# the docker networks the job names alone, so a test that reaches for a host outside the runner
# is refused and the job's log names the connection. The process keeps the runner's own user, so
# it reads and writes the checkout, the runner's temporary directory and the caches the set-up
# steps filled as they did. The kind job's tests run so, after set-up steps fetched what the rig
# runs, and so do airlock-arm's two scripts, against a stack a set-up step started. The runner's
# own processes, its link to GitHub among them, are in no such cgroup and keep every connection
# they make.
#
#   test-fence.sh fence [--network NAME]... [--container ID]...
#   test-fence.sh run SCRIPT
#   test-fence.sh report [--docker-events]
#
# fence, run once by the runner's user, which may sudo: makes the cgroup (legion-test under
# /sys/fs/cgroup) and puts an iptables and an ip6tables chain, LEGION-TEST, before every outbound
# packet of a socket of that cgroup, in which an established connection's packets, loopback and
# what leaves through the bridge of each named docker network (or of each network a named
# container is on) pass, and every other packet is logged with the prefix "legion-test refused: ",
# then rejected. No rule names a host or an address: a host's addresses move, and the rule's
# purpose is that the test process reaches none outside the runner. It installs
# /usr/local/bin/legion-test-shell, the step shell (`shell: legion-test-shell {0}`) that runs a
# step's body through `run`, and records when the rule began, for report.
#
# run SCRIPT moves its own process into the cgroup (its pid into the cgroup's cgroup.procs, through
# sudo), fails unless the cgroup then holds it, and runs SCRIPT with bash -e -o pipefail as the
# runner's user, in the step's environment with GOPROXY=off and GOTOOLCHAIN=local, so a Go build
# reads only the modules and the toolchain already on the runner and names a module it lacks
# rather than failing at a refused connection.
#
# report prints, for each family, the rule's chain, the OUTPUT rows that match the cgroup and the
# counters of both (`-L -v -x -n`), and `CHECK test-fence-match: PASS` when both families hold the
# match, else FAIL; every line the rule logged and `CHECK test-fence-refusals: PASS` when there is
# none, else FAIL; and with --docker-events Docker's image events since the rule began, and
# `CHECK test-fence-image-pulls: FAIL` where one is a pull, since Docker fetches as root, outside
# the rule. It exits 1 after a FAIL.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
cgname=legion-test
chain=LEGION-TEST
prefix="legion-test refused: "
state=${RUNNER_TEMP:-${TMPDIR:-/tmp}}/legion-test
# Where the cgroup v2 hierarchy is and where fence installs the step shell; the harness
# (test-fence.test.sh) moves both into a directory of its own.
cgroot=${LEGION_TEST_CGROUP_ROOT:-/sys/fs/cgroup}
cgroup=$cgroot/$cgname
shell=${LEGION_TEST_SHELL:-/usr/local/bin/legion-test-shell}

usage() {
  echo "test-fence.sh: $1 (usage: test-fence.sh fence [--network NAME]... [--container ID]..., run SCRIPT, or report [--docker-events])" >&2
  exit 2
}

# bridge NETWORK: the interface on this host of the docker bridge network NETWORK, through which
# what reaches its containers leaves: the name its options give, else Docker's br-<its id's first
# twelve characters>.
bridge() {
  local inspected driver id name
  inspected=$(docker network inspect "$1" --format '{{.Driver}} {{.Id}} {{index .Options "com.docker.network.bridge.name"}}' 2>&1) ||
    { echo "test-fence.sh: docker cannot inspect the network $1: $inspected" >&2; exit 1; }
  read -r driver id name <<<"$inspected"
  [ "$driver" = bridge ] ||
    { echo "test-fence.sh: the docker network $1 is a $driver network, and the fence passes a bridge's interface alone" >&2; exit 1; }
  case "$name" in "" | "<no value>") name=br-${id:0:12} ;; esac
  printf '%s\n' "$name"
}

fence() {
  local networks=() containers=() bridges=() container network family named
  while [ $# -gt 0 ]; do
    case "$1" in
      --network) [ $# -ge 2 ] || usage "--network needs a docker network"; networks+=("$2"); shift 2 ;;
      --container) [ $# -ge 2 ] || usage "--container needs a container"; containers+=("$2"); shift 2 ;;
      *) usage "unknown argument $1" ;;
    esac
  done
  [ -f "$cgroot/cgroup.controllers" ] ||
    { echo "test-fence.sh: $cgroot holds no cgroup v2 hierarchy (no cgroup.controllers), whose path the rule matches" >&2; exit 1; }
  for container in "${containers[@]+"${containers[@]}"}"; do
    named=$(docker inspect "$container" --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{"\n"}}{{end}}' 2>&1) ||
      { echo "test-fence.sh: docker cannot inspect the container $container: $named" >&2; exit 1; }
    mapfile -t -O "${#networks[@]}" networks < <(sed '/^$/d' <<<"$named")
  done
  for network in "${networks[@]+"${networks[@]}"}"; do
    network=$(bridge "$network")
    bridges+=("$network")
  done
  mapfile -t bridges < <(printf '%s\n' "${bridges[@]+"${bridges[@]}"}" | sed '/^$/d' | sort -u)

  # The cgroup is made before the rules: the kernel resolves the match's path when the rule goes in.
  sudo mkdir -p "$cgroup"
  for family in iptables ip6tables; do
    sudo "$family" -N "$chain"
    sudo "$family" -A "$chain" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
    sudo "$family" -A "$chain" -o lo -j RETURN
    for network in "${bridges[@]+"${bridges[@]}"}"; do
      sudo "$family" -A "$chain" -o "$network" -j RETURN
    done
    sudo "$family" -A "$chain" -j LOG --log-prefix "$prefix"
    sudo "$family" -A "$chain" -j REJECT
    sudo "$family" -I OUTPUT 1 -m cgroup --path "$cgname" -j "$chain"
    echo "test-fence.sh: $family rules for the cgroup $cgname:"
    sudo "$family" -S "$chain" | sed 's/^/  /'
    { sudo "$family" -S OUTPUT | grep -F -- "$cgname" || true; } | sed 's/^/  /'
  done

  mkdir -p "$state"
  printf '%s\n' GOPROXY=off GOTOOLCHAIN=local >"$state/env"
  date +%s >"$state/fenced-at"
  printf '#!/usr/bin/env bash\nexec bash %q run "$@"\n' "$here/test-fence.sh" | sudo tee "$shell" >/dev/null
  sudo chmod 0755 "$shell"
  echo "test-fence.sh: a step with shell: $(basename "$shell") {0} runs in the cgroup $cgname, as $(id -un)"
}

run() {
  [ $# -eq 1 ] || usage "run takes the step's script"
  [ -f "$state/env" ] || { echo "::error::test-fence.sh: no fence has run in this job ($state/env is missing); run test-fence.sh fence first" >&2; exit 1; }
  local env=()
  mapfile -t env <"$state/env"
  printf '%s\n' "$$" | sudo -n tee "$cgroup/cgroup.procs" >/dev/null
  grep -qx -- "$$" "$cgroup/cgroup.procs" ||
    { echo "::error::test-fence.sh: the step's shell, pid $$, is not in the cgroup $cgname after the move; it holds $(tr '\n' ' ' <"$cgroup/cgroup.procs")" >&2; exit 1; }
  exec env "${env[@]}" bash --noprofile --norc -eo pipefail "$1"
}

report() {
  local events=false failed=0 missing=() family rows kernel refused since lines pulls
  case "${1:-}" in
    "") ;;
    --docker-events) events=true ;;
    *) usage "unknown argument $1" ;;
  esac
  [ -f "$state/fenced-at" ] || { echo "CHECK test-fence-match: FAIL: no fence has run in this job ($state/fenced-at is missing)"; exit 1; }
  since=$(cat "$state/fenced-at")
  for family in iptables ip6tables; do
    echo "test-fence.sh: the $family rule:"
    sudo "$family" -S "$chain" | sed 's/^/  /'
    echo "test-fence.sh: the $family OUTPUT rows that match the cgroup $cgname:"
    rows=$(sudo "$family" -S OUTPUT | grep -F -- "$cgname" || true)
    if [ -n "$rows" ]; then sed 's/^/  /' <<<"$rows"; else echo "  none"; missing+=("$family"); fi
    echo "test-fence.sh: the $family counters:"
    sudo "$family" -L OUTPUT -v -x -n | sed 's/^/  /'
    sudo "$family" -L "$chain" -v -x -n | sed 's/^/  /'
  done
  if [ "${#missing[@]}" -eq 0 ]; then
    echo "CHECK test-fence-match: PASS: the OUTPUT chains of iptables and ip6tables send the cgroup $cgname's packets through $chain, with the counters above"
  else
    echo "CHECK test-fence-match: FAIL: no OUTPUT row of ${missing[*]} matches the cgroup $cgname"
    failed=1
  fi
  if ! kernel=$(sudo dmesg 2>&1); then
    echo "CHECK test-fence-refusals: FAIL: the kernel's log, which holds the rule's lines, cannot be read: $kernel"
    failed=1
  else
    refused=$(grep -F -- "$prefix" <<<"$kernel" || true)
    if [ -z "$refused" ]; then
      echo "CHECK test-fence-refusals: PASS: the rule refused no connection of the cgroup $cgname"
    else
      printf '%s\n' "$refused"
      echo "CHECK test-fence-refusals: FAIL: the rule refused $(wc -l <<<"$refused") packets of the cgroup $cgname, each line above"
      failed=1
    fi
  fi
  if [ "$events" = true ]; then
    lines=$(docker events --since "$since" --until "$(date +%s)" --filter type=image \
      --format '{{.Time}} {{.Action}} {{.Actor.ID}} {{with .Actor.Attributes}}{{.name}}{{end}}' 2>&1) ||
      { echo "CHECK test-fence-image-pulls: FAIL: docker events: $lines"; exit 1; }
    echo "Docker's image events since the rule began ($(date -u -d "@$since" +%FT%TZ)):"
    if [ -n "$lines" ]; then sed 's/^/  /' <<<"$lines"; else echo "  none"; fi
    pulls=$(awk '$2 == "pull"' <<<"$lines")
    if [ -z "$pulls" ]; then
      echo "CHECK test-fence-image-pulls: PASS: Docker pulled no image while the rule stood"
    else
      echo "CHECK test-fence-image-pulls: FAIL: Docker pulled $(wc -l <<<"$pulls") images while the rule stood, a fetch outside the rule"
      failed=1
    fi
  fi
  exit "$failed"
}

[ $# -gt 0 ] || usage "name fence, run or report"
command=$1
shift
case "$command" in
  fence) fence "$@" ;;
  run) run "$@" ;;
  report) report "$@" ;;
  *) usage "unknown command $command" ;;
esac
