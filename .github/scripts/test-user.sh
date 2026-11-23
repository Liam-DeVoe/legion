#!/usr/bin/env bash
# .github/scripts/test-user.sh
#
# Runs a CI step's test process as a user of its own, legion-test, whose outbound connections
# reach loopback and the docker networks the job names alone, so a test that reaches for a host
# outside the runner is refused and the job's log names the connection. The kind job's tests run
# so, after set-up steps fetched what the rig runs as the runner's user, and so do airlock-arm's
# two scripts, against a stack the runner's user started. The runner's own processes, its link
# to GitHub among them, keep every connection they make.
#
#   test-user.sh fence [--docker] [--signal-as USER] [--network NAME]... [--container ID]...
#                      [--grant PATH]... [--read PATH]...
#   test-user.sh grant [--read] PATH...
#   test-user.sh run SCRIPT
#   test-user.sh report [--docker-events]
#
# fence, run once by the runner's user, which may sudo: makes the user (in the docker group with
# --docker: the kind rig drives Docker); with --signal-as, lets it run kill and read a process's
# environ as USER through sudo, for a check that stops a broker of the stack USER started; puts an
# iptables and an ip6tables chain, LEGION-TEST, before every outbound packet of its uid, in which
# an established connection's packets, loopback and each subnet of each named docker network (or
# of each network a named container is on) pass, and every other packet is logged with the
# prefix "legion-test refused: " and its uid, then rejected; and grants it each --grant path to
# read and write and each --read path to read, as grant does. No rule names a host: a host's
# addresses move, and the rule's purpose is that the test process reaches none outside the
# runner. It installs /usr/local/bin/legion-test-shell, the step shell (`shell: legion-test-shell
# {0}`) that runs a step's body through `run`, and records when the rule began, for report.
#
# grant gives the user read and write (with --read, read alone) of each PATH and everything under
# it, with the same entry as the default of each directory there, and the right to pass through
# each directory above it, through ACLs: the runner's files keep their owner and mode.
#
# run SCRIPT runs SCRIPT with bash -e -o pipefail as the user, in the step's environment but
# PATH, HOME and the user's own names, with GOCACHE in its home, GOMODCACHE the runner's module
# cache that a set-up step filled, GOPROXY=off and GOTOOLCHAIN=local, so a Go build reads only
# the modules and the toolchain already on the runner.
#
# report prints every line the rule logged and `CHECK test-user-refusals: PASS` when there is
# none, else FAIL; with --docker-events it prints Docker's image events since the rule began,
# and `CHECK test-user-image-pulls: FAIL` where one is a pull, since Docker fetches as root,
# outside the user's rule. It exits 1 after a FAIL.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
user=legion-test
chain=LEGION-TEST
prefix="legion-test refused: "
state=${RUNNER_TEMP:-${TMPDIR:-/tmp}}/legion-test
# Where fence installs the step shell and the sudoers entry; the harness (test-user.test.sh)
# moves both into a directory of its own.
shell=${LEGION_TEST_SHELL:-/usr/local/bin/legion-test-shell}
sudoers=${LEGION_TEST_SUDOERS:-/etc/sudoers.d/legion-test}

usage() {
  echo "test-user.sh: $1 (usage: test-user.sh fence [--docker] [--signal-as USER] [--network NAME]... [--container ID]... [--grant PATH]... [--read PATH]..., grant [--read] PATH..., run SCRIPT, or report [--docker-events])" >&2
  exit 2
}

# grant_path MODE PATH: MODE (rwX or rX) on PATH and below it, as each directory's default too,
# and passage through each directory above it.
grant_path() {
  local mode=$1 path dir
  [ -e "$2" ] || { echo "test-user.sh: there is no $2 to grant $user" >&2; exit 1; }
  path=$(realpath "$2")
  dir=$(dirname "$path")
  while [ "$dir" != / ]; do
    sudo setfacl -m "u:$user:x" "$dir"
    dir=$(dirname "$dir")
  done
  sudo setfacl -R -m "u:$user:$mode" "$path"
  sudo find "$path" -type d -exec setfacl -m "d:u:$user:$mode" {} +
  echo "test-user.sh: $user may $([ "$mode" = rX ] && echo read || echo 'read and write') $path"
}

# subnets NETWORK: each subnet of a docker network, one a line.
subnets() {
  docker network inspect "$1" --format '{{range .IPAM.Config}}{{.Subnet}}{{"\n"}}{{end}}' | sed '/^$/d'
}

fence() {
  local docker=false signal_as="" networks=() containers=() grants=() reads=() uid family subnet all=() container
  while [ $# -gt 0 ]; do
    case "$1" in
      --docker) docker=true; shift ;;
      --signal-as) [ $# -ge 2 ] || usage "--signal-as needs a user"; signal_as=$2; shift 2 ;;
      --network) [ $# -ge 2 ] || usage "--network needs a docker network"; networks+=("$2"); shift 2 ;;
      --container) [ $# -ge 2 ] || usage "--container needs a container"; containers+=("$2"); shift 2 ;;
      --grant) [ $# -ge 2 ] || usage "--grant needs a path"; grants+=("$2"); shift 2 ;;
      --read) [ $# -ge 2 ] || usage "--read needs a path"; reads+=("$2"); shift 2 ;;
      *) usage "unknown argument $1" ;;
    esac
  done
  for container in "${containers[@]+"${containers[@]}"}"; do
    mapfile -t -O "${#networks[@]}" networks < <(docker inspect "$container" --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{"\n"}}{{end}}' | sed '/^$/d')
  done
  for subnet in "${networks[@]+"${networks[@]}"}"; do
    mapfile -t -O "${#all[@]}" all < <(subnets "$subnet")
  done
  [ "${#networks[@]}" -eq 0 ] || [ "${#all[@]}" -gt 0 ] || { echo "test-user.sh: the networks ${networks[*]} have no subnet" >&2; exit 1; }

  command -v setfacl >/dev/null 2>&1 ||
    { sudo apt-get update -q >/dev/null && sudo apt-get install -y -q --no-install-recommends acl >/dev/null; }
  id "$user" >/dev/null 2>&1 || sudo useradd --create-home --shell /bin/bash "$user"
  [ "$docker" = false ] || sudo usermod -aG docker "$user"
  uid=$(id -u "$user")
  if [ -n "$signal_as" ]; then
    printf '%s ALL=(%s) NOPASSWD: /usr/bin/kill, /usr/bin/cat /proc/[0-9]*/environ\n' "$user" "$signal_as" |
      sudo tee "$sudoers" >/dev/null
    sudo chmod 0440 "$sudoers"
    sudo visudo -cf "$sudoers" >/dev/null
    echo "test-user.sh: $user may run kill and read a process's environ as $signal_as"
  fi

  for family in iptables ip6tables; do
    sudo "$family" -N "$chain"
    sudo "$family" -A "$chain" -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
    sudo "$family" -A "$chain" -o lo -j RETURN
    for subnet in "${all[@]+"${all[@]}"}"; do
      case "$family:$subnet" in
        iptables:*:* | ip6tables:*.*) ;;
        *) sudo "$family" -A "$chain" -d "$subnet" -j RETURN ;;
      esac
    done
    sudo "$family" -A "$chain" -j LOG --log-prefix "$prefix" --log-uid
    sudo "$family" -A "$chain" -j REJECT
    sudo "$family" -I OUTPUT 1 -m owner --uid-owner "$uid" -j "$chain"
    echo "test-user.sh: $family rules for $user (uid $uid):"
    sudo "$family" -S "$chain" | sed 's/^/  /'
  done

  for path in "${grants[@]+"${grants[@]}"}"; do grant_path rwX "$path"; done
  for path in "${reads[@]+"${reads[@]}"}"; do grant_path rX "$path"; done
  # Go stamps a build of a main package with the checkout's commit, and git refuses a repository
  # another user owns unless that user names it safe.
  [ -z "${GITHUB_WORKSPACE:-}" ] || sudo -n -u "$user" -H git config --global --add safe.directory "$GITHUB_WORKSPACE"

  mkdir -p "$state"
  {
    echo "GOCACHE=/home/$user/.cache/go-build"
    echo GOPROXY=off
    echo GOTOOLCHAIN=local
    if command -v go >/dev/null 2>&1; then echo "GOMODCACHE=$(go env GOMODCACHE)"; fi
  } >"$state/env"
  date +%s >"$state/fenced-at"
  printf '#!/usr/bin/env bash\nexec bash %q run "$@"\n' "$here/test-user.sh" | sudo tee "$shell" >/dev/null
  sudo chmod 0755 "$shell"
  echo "test-user.sh: a step with shell: $(basename "$shell") {0} runs as $user"
}

grant() {
  local mode=rwX
  if [ "${1:-}" = --read ]; then mode=rX; shift; fi
  [ $# -gt 0 ] || usage "grant needs a path"
  local path
  for path in "$@"; do grant_path "$mode" "$path"; done
}

run() {
  [ $# -eq 1 ] || usage "run takes the step's script"
  [ -f "$state/env" ] || { echo "::error::test-user.sh: no fence has run in this job ($state/env is missing); run test-user.sh fence first" >&2; exit 1; }
  local env=()
  mapfile -t env <"$state/env"
  exec sudo -n --preserve-env -u "$user" -- env -u XDG_RUNTIME_DIR -u XDG_CONFIG_HOME -u XDG_CACHE_HOME -u XDG_DATA_HOME -u GOPATH \
    PATH="$PATH" HOME="/home/$user" USER="$user" LOGNAME="$user" "${env[@]}" bash --noprofile --norc -eo pipefail "$1"
}

report() {
  local events=false failed=0 kernel refused since lines pulls
  case "${1:-}" in
    "") ;;
    --docker-events) events=true ;;
    *) usage "unknown argument $1" ;;
  esac
  [ -f "$state/fenced-at" ] || { echo "CHECK test-user-refusals: FAIL: no fence has run in this job ($state/fenced-at is missing)"; exit 1; }
  since=$(cat "$state/fenced-at")
  if ! kernel=$(sudo dmesg 2>&1); then
    echo "CHECK test-user-refusals: FAIL: the kernel's log, which holds the rule's lines, cannot be read: $kernel"
    failed=1
  else
    refused=$(grep -F -- "$prefix" <<<"$kernel" || true)
    if [ -z "$refused" ]; then
      echo "CHECK test-user-refusals: PASS: the rule refused no connection of $user"
    else
      printf '%s\n' "$refused"
      echo "CHECK test-user-refusals: FAIL: the rule refused $(wc -l <<<"$refused") packets of $user, each line above"
      failed=1
    fi
  fi
  if [ "$events" = true ]; then
    lines=$(docker events --since "$since" --until "$(date +%s)" --filter type=image \
      --format '{{.Time}} {{.Action}} {{.Actor.ID}} {{with .Actor.Attributes}}{{.name}}{{end}}' 2>&1) ||
      { echo "CHECK test-user-image-pulls: FAIL: docker events: $lines"; exit 1; }
    echo "Docker's image events since the rule began ($(date -u -d "@$since" +%FT%TZ)):"
    if [ -n "$lines" ]; then sed 's/^/  /' <<<"$lines"; else echo "  none"; fi
    pulls=$(awk '$2 == "pull"' <<<"$lines")
    if [ -z "$pulls" ]; then
      echo "CHECK test-user-image-pulls: PASS: Docker pulled no image while the rule stood"
    else
      echo "CHECK test-user-image-pulls: FAIL: Docker pulled $(wc -l <<<"$pulls") images while the rule stood, a fetch outside $user's rule"
      failed=1
    fi
  fi
  exit "$failed"
}

[ $# -gt 0 ] || usage "name fence, grant, run or report"
command=$1
shift
case "$command" in
  fence) fence "$@" ;;
  grant) grant "$@" ;;
  run) run "$@" ;;
  report) report "$@" ;;
  *) usage "unknown command $command" ;;
esac
