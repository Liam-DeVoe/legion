#!/usr/bin/env bash
# Tests for `ci-fault-proxy.py`: each of its three modes over plain HTTP and over CONNECT, a
# host outside --hosts passed on in every one of them, --bind, and the window of its own that
# --per-host-window gives each host under --mode recover; and the counts by host that
# download-retry-proof.sh's `report` makes of the requests the proxy logged as passed on and as
# faulted.
#
# Nothing here touches the real internet. The host that is passed on is an origin this test
# starts, a `python3 -m http.server` on a loopback port, and the host that is faulted is either
# a name under .invalid, which RFC 2606 reserves to resolve nowhere, or that same origin under
# another of its names — `localhost` and `127.0.0.1` reach the same server and the proxy matches
# on the name the request carries, so the recover case can fault a host that still answers once
# the window closes.
#
# CONNECT is driven with `curl --proxytunnel`, which makes curl tunnel a plain-HTTP request
# through a CONNECT rather than speak TLS: it exercises the proxy's CONNECT path, which is what
# is under test, against an origin that needs no certificate.
#
# Every curl here clears no_proxy and NO_PROXY. A value of `*` in either makes curl ignore -x,
# and the test would then pass by never reaching the proxy at all. A GitHub runner sets neither;
# the pods this repository's agents run in set both.
#
# Every process this script starts is killed by its own pid in the EXIT trap, and everything it
# writes goes under $TMPDIR (mktemp -d honours it), never /tmp by name.
#
# Run from anywhere: .github/scripts/ci-fault-proxy.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
proxy_script="$script_dir/ci-fault-proxy.py"

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

work=$(mktemp -d)
pids=()
cleanup() {
  local pid
  for pid in ${pids+"${pids[@]}"}; do
    kill "$pid" 2> /dev/null || true
  done
  rm -rf "$work"
}
trap cleanup EXIT

# The origin: one file, served on a loopback port the kernel picks. The port is written to a
# file rather than read out of what the server logs, as the proxy's own --port-file is: the
# banner `python3 -m http.server` prints reached its log only when the server exited on the
# runner, so this harness waited ten seconds and then said the origin reported no port (run
# 42353947192 of this repository, the `test` job, whose origin log was empty). A port written
# by the process that bound it cannot be buffered away and cannot be taken by a second process
# between the choice and the bind.
mkdir "$work/origin"
printf 'the origin answered\n' > "$work/origin/hello.txt"
cat > "$work/origin.py" <<'ORIGIN'
import functools, http.server, os, sys

directory, port_file = sys.argv[1], sys.argv[2]
handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=directory)
server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
temporary = port_file + ".part"
with open(temporary, "w", encoding="utf-8") as handle:
    handle.write(f"{server.server_address[1]}\n")
os.replace(temporary, port_file)
server.serve_forever()
ORIGIN
python3 "$work/origin.py" "$work/origin" "$work/origin.port" > "$work/origin.log" 2>&1 &
pids+=("$!")
origin_port=""
for _ in $(seq 200); do
  if [ -s "$work/origin.port" ]; then
    origin_port=$(cat "$work/origin.port")
    break
  fi
  sleep 0.05
done
if [ -z "$origin_port" ]; then
  echo "  FAIL: the origin did not report a port" >&2
  cat "$work/origin.log" >&2
  exit 1
fi
echo "measured: the origin listens on 127.0.0.1:$origin_port"

# start_proxy <name> <arguments...>: starts the proxy on a free port, waits for its port file and
# sets $proxy and $proxy_port. Its pid joins the list the trap kills.
start_proxy() {
  local name=$1
  shift
  local port_file="$work/$name.port" pid port=""
  python3 "$proxy_script" --port 0 --port-file "$port_file" "$@" > "$work/$name.log" 2>&1 &
  pid=$!
  pids+=("$pid")
  for _ in $(seq 200); do
    if [ -s "$port_file" ]; then
      port=$(cat "$port_file")
      break
    fi
    kill -0 "$pid" 2> /dev/null || break
    sleep 0.05
  done
  if [ -z "$port" ]; then
    echo "  FAIL: the $name proxy did not report a port" >&2
    cat "$work/$name.log" >&2
    exit 1
  fi
  proxy_port=$port
  proxy="http://127.0.0.1:$port"
}

# run_curl <arguments...>: curl with this machine's no_proxy and NO_PROXY cleared, leaving its
# merged output in $out, its exit status in $status and its wall time in whole seconds in
# $seconds.
run_curl() {
  local start
  start=$(date +%s)
  set +e
  out=$(env no_proxy= NO_PROXY= curl --silent --show-error "$@" 2>&1)
  status=$?
  set -e
  seconds=$(($(date +%s) - start))
}

echo "case: --mode fail answers 504 to a faulted host, over plain HTTP and over CONNECT"
start_proxy fail --mode fail --hosts fetch.invalid
run_curl -x "$proxy" -o /dev/null -w '%{http_code}' http://fetch.invalid/tool.tar.gz
echo "  measured: plain HTTP answered $out, curl exit $status"
check "a plain-HTTP request for a faulted host answers 504" "$(is "$out" 504)"
run_curl -x "$proxy" --proxytunnel http://fetch.invalid/tool.tar.gz
echo "  measured: CONNECT gave curl exit $status: $out"
check "a CONNECT for a faulted host is refused with 504, which curl reports as exit 56" "$(is "$status" 56)"
check "and curl's message names the 504 it got from the proxy" "$(contains "$out" '504')"
run_curl -x "$proxy" "http://localhost:$origin_port/hello.txt"
check "a host outside --hosts reaches the origin over plain HTTP" "$(is "$out" 'the origin answered')"
run_curl -x "$proxy" --proxytunnel "http://localhost:$origin_port/hello.txt"
check "and through CONNECT" "$(is "$out" 'the origin answered')"

echo "case: --mode recover answers 504 inside its window and passes the request on after it"
# A 10 s window, not the 2 s a reader might write: three loopback requests have to finish inside
# it, and a runner that stalls for a second between them would otherwise read the recovered
# answer and report the window as broken. The case costs 11 s and cannot race.
start_proxy recover --mode recover --recover-after 10 --hosts localhost
window_start=$(date +%s)
run_curl -x "$proxy" -o /dev/null -w '%{http_code}' "http://localhost:$origin_port/hello.txt"
inside_plain=$out
run_curl -x "$proxy" --proxytunnel "http://localhost:$origin_port/hello.txt"
inside_tunnel_status=$status
echo "  measured: $(($(date +%s) - window_start))s into the 10 s window, plain HTTP answered $inside_plain and CONNECT gave curl exit $inside_tunnel_status"
check "a plain-HTTP request inside the window answers 504" "$(is "$inside_plain" 504)"
check "a CONNECT inside the window is refused" "$(is "$inside_tunnel_status" 56)"
run_curl -x "$proxy" "http://127.0.0.1:$origin_port/hello.txt"
check "a host outside --hosts reaches the origin inside the window" "$(is "$out" 'the origin answered')"
sleep 11
run_curl -x "$proxy" "http://localhost:$origin_port/hello.txt"
echo "  measured: $(($(date +%s) - window_start))s after the window opened, plain HTTP answered '$out' (curl exit $status)"
check "the same plain-HTTP request after the window reaches the origin" "$(is "$out" 'the origin answered')"
run_curl -x "$proxy" --proxytunnel "http://localhost:$origin_port/hello.txt"
check "and the same CONNECT reaches it too" "$(is "$out" 'the origin answered')"

# The recover case above holds no window to the proxy's start rather than to a host's first
# request: it asks for the faulted host at once, so its first request and the proxy's start fall
# in the same second, and a window of either kind answers it the same way. This case holds it,
# by asking for the host only after one window from the start has closed.
echo "case: without --per-host-window, --mode recover keeps one window from the start, which a host first reached after it never meets"
start_proxy oneshot --mode recover --recover-after 2 --hosts localhost
sleep 3
run_curl -x "$proxy" "http://localhost:$origin_port/hello.txt"
echo "  measured: 3 s after the proxy started, the faulted host's first request answered '$out' (curl exit $status)"
check "a faulted host first reached after the one window is passed on at its first request" \
  "$(is "$out" 'the origin answered')"
check "and the proxy logged no window of its own for it" \
  "$(is "$(contains "$(cat "$work/oneshot.log")" 'ci-fault-proxy: window ')" false)"

echo "case: --per-host-window gives a host first reached late a full window of its own, and never a second one"
# A 4 s window, and each reading at least a second clear of its edge: the host is first asked for
# 5 s after the proxy started, a second after one window from the start would have closed; then
# 2.5 s into its own window, 1.5 s before that window closes; then a second after it closed; and
# then once more after as long again as a window, where a second window would answer it.
start_proxy late --mode recover --recover-after 4 --per-host-window --hosts localhost
sleep 5
run_curl -x "$proxy" -o /dev/null -w '%{http_code}' "http://localhost:$origin_port/hello.txt"
late_first=$out
sleep 2.5
run_curl -x "$proxy" --proxytunnel "http://localhost:$origin_port/hello.txt"
late_inside_status=$status
late_opened=$(sed -n 's/^ci-fault-proxy: window localhost opened \([0-9.]*\)s after the proxy started$/\1/p' "$work/late.log")
echo "  measured: the first request, 5 s after the proxy started, answered $late_first; 2.5 s later a CONNECT gave curl exit $late_inside_status; the proxy logged the window opening at ${late_opened:-no second at all}s"
check "a host first asked for after one window from the start has closed answers 504 at its first request" \
  "$(is "$late_first" 504)"
check "and a CONNECT 2.5 s into its own 4 s window is refused too" "$(is "$late_inside_status" 56)"
check "and the proxy logged its window opening at that first request, more than 4 s after it started" \
  "$(awk -v s="${late_opened:-0}" 'BEGIN { print (s > 4 ? "true" : "false") }')"
sleep 2.5
run_curl -x "$proxy" "http://localhost:$origin_port/hello.txt"
echo "  measured: 5 s into the host's window, plain HTTP answered '$out' (curl exit $status)"
check "the same request after its window closed reaches the origin" "$(is "$out" 'the origin answered')"
run_curl -x "$proxy" "http://localhost:$origin_port/hello.txt"
late_again=$out
sleep 4.5
run_curl -x "$proxy" --proxytunnel "http://localhost:$origin_port/hello.txt"
echo "  measured: the next request answered '$late_again', and a CONNECT 4.5 s later '$out' (curl exit $status)"
check "and the request after that reaches it too: a closed window opens no second one" \
  "$(is "$late_again" 'the origin answered')"
check "and so does a CONNECT as long as a window after that" "$(is "$out" 'the origin answered')"
check "the proxy logged one window for the host, and only one" \
  "$(is "$(grep -c '^ci-fault-proxy: window ' "$work/late.log" || true)" 1)"
check "and its first line says it runs a window per host" \
  "$(contains "$(cat "$work/late.log")" '^ci-fault-proxy: mode=recover window=per-host bind=127\.0\.0\.1 ')"

echo "case: --per-host-window keeps each host's window its own, two hosts under one --hosts entry among them"
# Both hosts are names under fetch.invalid, the one entry, so the windows are told apart by the
# host a request names and not by the entry it matched. A name under .invalid resolves nowhere,
# so a request the proxy passes on is answered by the proxy itself with 502, as the rig's probe
# reads its unfaulted host: 504 is the fault, and 502 is the request passed on. b is first asked
# for 3 s into a's 4 s window, and the pair is read again 2 s later, a second after a's window
# closed and 2 s into b's own, b first since a's 502 waits on a resolver; and b once more 3 s
# after that, a second after its own window closed.
start_proxy hosts --mode recover --recover-after 4 --per-host-window --hosts fetch.invalid
run_curl -x "$proxy" -o /dev/null -w '%{http_code}' http://a.fetch.invalid/tool.tar.gz
a_first=$out
sleep 3
run_curl -x "$proxy" -o /dev/null -w '%{http_code}' http://b.fetch.invalid/tool.tar.gz
b_first=$out
sleep 2
run_curl -x "$proxy" -o /dev/null -w '%{http_code}' http://b.fetch.invalid/tool.tar.gz
b_inside=$out
run_curl -x "$proxy" -o /dev/null -w '%{http_code}' http://a.fetch.invalid/tool.tar.gz
a_after=$out
sleep 3
run_curl -x "$proxy" -o /dev/null -w '%{http_code}' http://b.fetch.invalid/tool.tar.gz
b_after=$out
echo "  measured: a answered $a_first, then b 3 s later $b_first; 2 s on, b $b_inside and a $a_after; 3 s on again, b $b_after"
check "the first host's first request answers 504" "$(is "$a_first" 504)"
check "and the second host, first asked for while the first host's window is closing, answers 504" "$(is "$b_first" 504)"
check "while the second host, in its own window, still answers 504" "$(is "$b_inside" 504)"
check "and once the first host's window has closed, the first host is passed on" "$(is "$a_after" 502)"
check "and the second host is passed on once its own window has closed" "$(is "$b_after" 502)"
# Bounded, so a proxy that took the flag and served would end here rather than hold the test.
set +e
timeout 10 python3 "$proxy_script" --mode fail --per-host-window --hosts fetch.invalid > "$work/refused.out" 2>&1
refused=$?
set -e
check "--per-host-window is refused with a mode that has no window, as a usage error" \
  "$(is "$refused:$(contains "$(cat "$work/refused.out")" 'has no window')" 2:true)"

echo "case: --mode stall sends nothing, so each attempt ends at the client's own bound"
start_proxy stall --mode stall --hosts fetch.invalid
# The body file is created here, not left to curl: a curl that receives nothing writes no file
# at all, and a check that a file which was never created is empty proves nothing.
: > "$work/stall.body"
run_curl --max-time 2 -o "$work/stall.body" -x "$proxy" http://fetch.invalid/tool.tar.gz
echo "  measured: plain HTTP ended after ${seconds}s with curl exit $status, $(wc -c < "$work/stall.body") bytes from the proxy: $out"
check "a stalled plain-HTTP request ends at curl's --max-time, which curl reports as exit 28" "$(is "$status" 28)"
check "and it took about the 2 s bound" "$([ "$seconds" -ge 2 ] && [ "$seconds" -le 5 ] && echo true || echo false)"
check "and the proxy sent nothing at all" "$([ ! -s "$work/stall.body" ] && echo true || echo false)"
run_curl --max-time 2 -x "$proxy" --proxytunnel http://fetch.invalid/tool.tar.gz
echo "  measured: CONNECT ended after ${seconds}s with curl exit $status: $out"
check "a stalled CONNECT ends at the same bound" "$(is "$status" 28)"
check "and it took about the 2 s bound too" "$([ "$seconds" -ge 2 ] && [ "$seconds" -le 5 ] && echo true || echo false)"
run_curl --max-time 10 -x "$proxy" "http://localhost:$origin_port/hello.txt"
check "a host outside --hosts reaches the origin over plain HTTP" "$(is "$out" 'the origin answered')"
run_curl --max-time 10 -x "$proxy" --proxytunnel "http://localhost:$origin_port/hello.txt"
check "and through CONNECT" "$(is "$out" 'the origin answered')"

echo "case: --bind listens on another address, which is how a docker build reaches the proxy"
# 127.0.0.2 stands in for the docker bridge gateway: a second address of this machine that the
# default 127.0.0.1 does not answer on, so the case proves both halves of the option — the proxy
# serves where --bind says, and nowhere else. Every Linux kernel routes the whole 127.0.0.0/8 to
# loopback, so this needs no interface of its own.
start_proxy bind --mode fail --hosts fetch.invalid --bind 127.0.0.2
run_curl -x "http://127.0.0.2:$proxy_port" -o /dev/null -w '%{http_code}' http://fetch.invalid/tool.tar.gz
echo "  measured: the proxy on 127.0.0.2:$proxy_port answered $out, curl exit $status"
check "a request to the bound address is served" "$(is "$out" 504)"
run_curl --max-time 5 -x "http://127.0.0.1:$proxy_port" -o /dev/null -w '%{http_code}' http://fetch.invalid/tool.tar.gz
echo "  measured: the same port on 127.0.0.1 gave curl exit $status: $out"
check "and 127.0.0.1 is not listening, which curl reports as exit 7" "$(is "$status" 7)"
check "the proxy's first line names the address it bound" \
  "$(contains "$(cat "$work/bind.log")" "^ci-fault-proxy: mode=fail bind=127\.0\.0\.2 port=$proxy_port ")"

echo "case: the proxy logs what it did, and fetched nothing of its own"
fail_log=$(cat "$work/fail.log")
indent "$fail_log"
check "it logged the 504 it answered to the plain-HTTP request" \
  "$(contains "$fail_log" '^ci-fault-proxy: 504 GET http://fetch.invalid/tool.tar.gz$')"
check "and the 504 it answered to the CONNECT" \
  "$(contains "$fail_log" '^ci-fault-proxy: 504 CONNECT fetch.invalid:80$')"
check "and the request it passed on" \
  "$(contains "$fail_log" "^ci-fault-proxy: pass GET http://localhost:$origin_port/hello.txt\$")"
check "no handler failed" "$(is "$(contains "$fail_log" 'handler failed')" false)"

echo "case: download-retry-proof.sh's report counts the requests the proxy passed on by host"
# The rig the download-retry jobs run, driven whole over the origin above: `start` runs the proxy
# behind its stamping loop, `probe` reads its pair before the site, and `report` reads the log
# back. The site is the origin under two of its names, 127.0.0.1 twice over plain HTTP and
# localhost once through CONNECT, so the log holds passes to two hosts in both of the line shapes
# the proxy writes. --mode fail, faulting a host the site never asks for, so the report's own
# check of its mode passes and this case reads the count. RUNNER_TEMP is the rig's state
# directory, under $work, and GITHUB_OUTPUT is emptied, so a run on a runner writes nothing into
# its own step's outputs; no_proxy and NO_PROXY are cleared for the reason run_curl gives.
rig() { env RUNNER_TEMP="$work/rig" GITHUB_OUTPUT= no_proxy= NO_PROXY= "$script_dir/download-retry-proof.sh" "$@"; }
rig_state="$work/rig/download-retry/tally.state"
set +e
rig start tally --mode fail --hosts fetch.invalid > "$work/rig-start.out" 2>&1
rig_start=$?
set -e
[ ! -f "$rig_state" ] || pids+=("$(sed -n 's/^pid=//p' "$rig_state")")
indent "$(cat "$work/rig-start.out")"
check "the rig's start exits 0" "$(is "$rig_start" 0)"
set +e
rig probe tally > "$work/rig-probe.out" 2>&1
rig_probe=$?
set -e
indent "$(cat "$work/rig-probe.out")"
check "and its probe reads the pair as it must" "$(is "$rig_probe" 0)"
rig_proxy=$(sed -n 's/^proxy=//p' "$rig_state" 2> /dev/null || true)
run_curl -x "$rig_proxy" "http://127.0.0.1:$origin_port/hello.txt"
run_curl -x "$rig_proxy" "http://127.0.0.1:$origin_port/hello.txt"
run_curl -x "$rig_proxy" --proxytunnel "http://localhost:$origin_port/hello.txt"
# The stamping loop writes a line a moment after the answer it belongs to, so the three passes
# are waited for rather than raced, as the probe waits for its own.
for _ in $(seq 100); do
  [ "$(grep -c 'ci-fault-proxy: pass ' "$work/rig/download-retry/tally.log" 2> /dev/null || true)" -lt 3 ] || break
  sleep 0.05
done
set +e
rig_out=$(rig report tally --what "the by-host case" --outcome success --attempts 1 --floor 0 --budget 600 2>&1)
rig_report=$?
set -e
indent "$rig_out"
check "the report exits 0" "$(is "$rig_report" 0)"
check "it counts the requests the proxy passed on, and says a count by host follows" \
  "$(contains "$rig_out" '^  requests the proxy passed on: 3, by host:$')"
check "and the two lines after it name both hosts with their own counts, the CONNECT's port taken off" \
  "$(is "$(sed -n '/^  requests the proxy passed on: 3, by host:$/{n;p;n;p;}' <<<"$rig_out")" \
    "$(printf '    2 127.0.0.1\n    1 localhost')")"

echo "case: download-retry-proof.sh passes --per-host-window on, and its report counts the requests the proxy faulted by host, with each host's first request"
# The rig again, under --mode recover with a window per host, the 60 s `start` gives by default,
# which nothing here outlasts. The site asks for two faulted hosts, a.fetch.invalid twice, over
# plain HTTP and through CONNECT, and localhost once, and for 127.0.0.1, off the list, once: the
# one pass the report's recover check asks for.
rig_state="$work/rig/download-retry/perhost.state"
set +e
rig start perhost --mode recover --per-host-window --hosts fetch.invalid,localhost > "$work/rig-perhost-start.out" 2>&1
rig_start=$?
set -e
[ ! -f "$rig_state" ] || pids+=("$(sed -n 's/^pid=//p' "$rig_state")")
indent "$(cat "$work/rig-perhost-start.out")"
check "the rig's start exits 0, and says it passed the flag on" \
  "$(is "$rig_start:$(contains "$(cat "$work/rig-perhost-start.out")" '(--mode recover --per-host-window, faulting fetch.invalid,localhost)$')" 0:true)"
check "and the proxy it started runs a window per host" \
  "$(contains "$(cat "$work/rig/download-retry/perhost.log")" ' ci-fault-proxy: mode=recover window=per-host ')"
set +e
rig probe perhost > "$work/rig-perhost-probe.out" 2>&1
rig_probe=$?
set -e
indent "$(cat "$work/rig-perhost-probe.out")"
check "and its probe reads the pair as it must" "$(is "$rig_probe" 0)"
check "and prints the 504 as the proxy's line for its faulted request, not the line of the window that request opened" \
  "$(contains "$(cat "$work/rig-perhost-probe.out")" "^  the proxy's own line for it: [0-9]* ci-fault-proxy: 504 GET http://fetch\.invalid/\$")"
rig_proxy=$(sed -n 's/^proxy=//p' "$rig_state" 2> /dev/null || true)
run_curl -x "$rig_proxy" http://a.fetch.invalid/tool.tar.gz
run_curl -x "$rig_proxy" --proxytunnel http://a.fetch.invalid/tool.tar.gz
run_curl -x "$rig_proxy" "http://localhost:$origin_port/hello.txt"
run_curl -x "$rig_proxy" "http://127.0.0.1:$origin_port/hello.txt"
# The probe's own refusal and the site's three, and the site's one pass, waited for as above.
for _ in $(seq 100); do
  [ "$(grep -c 'ci-fault-proxy: 504 ' "$work/rig/download-retry/perhost.log" 2> /dev/null || true)" -lt 4 ] ||
    [ "$(grep -c 'ci-fault-proxy: pass ' "$work/rig/download-retry/perhost.log" 2> /dev/null || true)" -lt 1 ] || break
  sleep 0.05
done
set +e
rig_out=$(rig report perhost --what "the faulted-by-host case" --outcome success --attempts 1 --floor 0 --budget 600 2>&1)
rig_report=$?
set -e
indent "$rig_out"
check "the report exits 0" "$(is "$rig_report" 0)"
faulted_header='^  requests the proxy faulted: 3, by host, each with its first request in seconds after the proxy started:$'
check "it counts the requests the proxy faulted, and says a count by host with each first request follows" \
  "$(contains "$rig_out" "$faulted_header")"
check "and the first line after it names a.fetch.invalid, faulted twice, the CONNECT's port taken off, with the second its window opened" \
  "$(contains "$(sed -n "/$faulted_header/{n;p;}" <<<"$rig_out")" '^    2 a\.fetch\.invalid, first request at [0-9][0-9]*\.[0-9] s$')"
check "and the second names localhost, faulted once, with its own" \
  "$(contains "$(sed -n "/$faulted_header/{n;n;p;}" <<<"$rig_out")" '^    1 localhost, first request at [0-9][0-9]*\.[0-9] s$')"

summary "ci-fault-proxy.py and download-retry-proof.sh's counts by host"
