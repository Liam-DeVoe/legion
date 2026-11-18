#!/usr/bin/env bash
# Fetch, in a step before a job's tests, what those tests would otherwise fetch while they run.
# A test that reaches the module proxy or the checksum database fails whenever that service
# fails, on a change that touched nothing it tests: in run 44317642380, envoy-infra-go failed
# because a `go install` of helm inside a test could not read sum.golang.org. Here the fetch runs
# once, tried again on failure, from the module cache the job restored, and the tests then find
# what they need and fetch nothing.
#
#   go-fetch.sh tools FILE
#     `go install` each pin FILE lists, one package path and version per line
#     (`helm.sh/helm/v3/cmd/helm@v3.20.2`; `#` starts a comment), into `go env GOBIN`, where the
#     tests that run the tool look it up and check its build against the pin.
#   go-fetch.sh deps DIR PACKAGE...
#     Download into the module cache every module a `go build` of PACKAGE... in DIR needs
#     (`go list -deps`, which compiles nothing), for tests that build those packages while they
#     run with the module proxy turned off (cmdtest.BuildCommand, testbuild.EnvoyBinary).
#   go-fetch.sh pulumi-consumer SRC PLUGIN
#     Copy the Go program SRC into a directory of its own, run `pulumi package add PLUGIN` there
#     outside any workspace, then `go build ./...`: the resolution host's
#     TestLiveGoConsumersCanUseTheSDK makes, whose `go mod tidy` reads modules no package of this
#     repository loads, so the test can make it again from the module cache with no proxy. Each
#     attempt starts from a fresh copy, since package add writes into its directory.
#
# Each go command is tried up to GO_FETCH_ATTEMPTS times (4), the waits between them starting
# at GO_FETCH_WAIT seconds (15) and doubling, so a fault of up to about two minutes is ridden
# out. It exits 1 when a command fails every attempt, naming it, and 2 on a usage error or a
# FILE line that is no pin, before it runs anything.
set -euo pipefail

attempts=${GO_FETCH_ATTEMPTS:-4}
first_wait=${GO_FETCH_WAIT:-15}

usage() {
  echo "usage: go-fetch.sh tools FILE | go-fetch.sh deps DIR PACKAGE... | go-fetch.sh pulumi-consumer SRC PLUGIN" >&2
  exit 2
}

# retry <command...>: runs the command until it succeeds or has failed `attempts` times. Its own
# lines go to stderr, so a caller may discard the command's output and still see each attempt.
retry() {
  local n=1 wait=$first_wait
  while :; do
    echo "go-fetch: $* (attempt $n of $attempts)" >&2
    if "$@"; then
      return 0
    fi
    if [ "$n" -ge "$attempts" ]; then
      echo "::error::go-fetch: failed $attempts times: $*" >&2
      return 1
    fi
    echo "go-fetch: $* failed; trying again in ${wait}s" >&2
    sleep "$wait"
    n=$((n + 1))
    wait=$((wait * 2))
  done
}

[ $# -ge 1 ] || usage
mode=$1
shift
case "$mode" in
  tools)
    [ $# -eq 1 ] || usage
    file=$1
    [ -f "$file" ] || { echo "go-fetch: no file $file" >&2; exit 2; }
    pins=()
    n=0
    while IFS= read -r line || [ -n "$line" ]; do
      n=$((n + 1))
      pin=${line%%#*}
      pin=${pin#"${pin%%[![:space:]]*}"}
      pin=${pin%"${pin##*[![:space:]]}"}
      [ -n "$pin" ] || continue
      # A package path and a semantic version: a query such as @latest would install whatever
      # the proxy answers that day, which no test's check of the build would then match.
      if ! [[ "$pin" =~ ^[^@[:space:]]+@v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$ ]]; then
        echo "go-fetch: $file:$n: $pin is not a package path and a semantic version, path@vX.Y.Z" >&2
        exit 2
      fi
      pins+=("$pin")
    done < "$file"
    [ "${#pins[@]}" -gt 0 ] || { echo "go-fetch: $file pins no tool" >&2; exit 2; }
    for pin in "${pins[@]}"; do
      retry go install "$pin"
    done
    ;;
  deps)
    [ $# -ge 2 ] || usage
    dir=$1
    shift
    [ -d "$dir" ] || { echo "go-fetch: no directory $dir" >&2; exit 2; }
    cd "$dir"
    retry go list -deps "$@" > /dev/null
    ;;
  pulumi-consumer)
    [ $# -eq 2 ] || usage
    src=$1 plugin=$2
    [ -f "$src/go.mod" ] || { echo "go-fetch: no Go module at $src" >&2; exit 2; }
    [ -x "$plugin" ] || { echo "go-fetch: no plugin binary at $plugin" >&2; exit 2; }
    src=$(cd "$src" && pwd)
    work=$(mktemp -d)
    trap 'rm -rf "$work"' EXIT
    # One attempt: a fresh copy, package add in it, then the build package add's tidy prepared.
    # The CLI runs as pulumitest.NewCLI runs it in the test: a PULUMI_HOME and a file backend of
    # its own, so it reads and writes nothing of the runner's and asks no service who is logged
    # in (with no backend the CLI looks up an account on Pulumi Cloud and, logged out, makes one).
    consume() {
      local dir
      dir=$(mktemp -d "$work/consumer.XXXXXX")
      cp -R "$src/." "$dir"
      mkdir -p "$work/backend" "$work/home"
      (cd "$dir" && GOWORK=off PULUMI_HOME="$work/home" PULUMI_BACKEND_URL="file://$work/backend" \
        PULUMI_CONFIG_PASSPHRASE=test PULUMI_SKIP_UPDATE_CHECK=true pulumi package add "$plugin" &&
        GOWORK=off go build ./...)
    }
    echo "go-fetch: the Pulumi consumer $src, on the plugin $plugin" >&2
    retry consume
    ;;
  *)
    usage
    ;;
esac
