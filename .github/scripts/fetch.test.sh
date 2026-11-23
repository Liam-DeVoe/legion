#!/usr/bin/env bash
# Tests for fetch.sh: a download that a server answers is fetched once; one that fails is tried
# twice more and recovers; one that fails every attempt fails by name with curl's exit status; a
# body that stalls is cut at the bound, and no attempt starts once twice the bound has passed; a
# Retry-After that would wait past that ends the retries; and a call without a stage, a bound and an
# address is refused.
#
# The cases run the real script against a stand-in server on 127.0.0.1, a small Python script in
# this harness's own temporary directory. The path of each request says what the stand-in answers,
# and each case asks a path of its own, so the stand-in's log counts each case's requests apart:
#   /ok/<case>                 200 and the body;
#   /503-once/<case>           503 the first time, then 200 and the body;
#   /503-always/<case>         503 every time;
#   /404/<case>                404 every time;
#   /stall-once/<case>         the first time, 200 with the body's length, five of its bytes and
#                              then nothing; then 200 and the body;
#   /stall-always/<case>       that stall every time;
#   /retry-after-long/<case>   503 with `Retry-After: 100`.
# A case whose server ends each attempt by itself runs with a bound of ENDS (30 s), far above the
# time it takes, so a slow runner cannot cut it; a case whose server stalls runs with STALLS (2 s),
# so it costs seconds. Each call runs under a deadline of its own (DEADLINE): a fetch that never
# ends fails its case by name rather than holding the test job until its limit. How long a case
# took is no assertion; how many requests the stand-in saw is.
#
# Run from anywhere: .github/scripts/fetch.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/fetch.sh"
work=$(mktemp -d)
server_pid=
trap '[ -z "$server_pid" ] || kill "$server_pid" 2> /dev/null || true; rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

ENDS=30
STALLS=2
DEADLINE=60
BODY="the pinned bytes"

cat > "$work/standin.py" <<'PY'
import http.server
import sys
import threading
import time

BODY = b"the pinned bytes\n"
log = open(sys.argv[1], "a", buffering=1)
seen = {}
lock = threading.Lock()


class Handler(http.server.BaseHTTPRequestHandler):
    def answer(self, with_body):
        mode = self.path.split("/")[1]
        with lock:
            earlier = seen.get(self.path, 0)
            seen[self.path] = earlier + 1
        if mode == "ok" or (mode in ("503-once", "stall-once") and earlier):
            status = 200
        elif mode in ("stall-once", "stall-always"):
            status = "stall"
        elif mode == "404":
            status = 404
        else:
            status = 503
        log.write(f"{self.command} {self.path} {status}\n")
        if status == "stall":
            self.send_response(200)
            self.send_header("Content-Length", str(len(BODY)))
            self.end_headers()
            if with_body:
                self.wfile.write(BODY[:5])
                self.wfile.flush()
            time.sleep(120)
            return
        payload = BODY if status == 200 else b"not this time\n"
        self.send_response(status)
        if mode == "retry-after-long":
            self.send_header("Retry-After", "100")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        if with_body:
            self.wfile.write(payload)

    def do_GET(self):
        self.answer(True)

    def do_HEAD(self):
        self.answer(False)

    def log_message(self, *args):
        pass


server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
server.daemon_threads = True
print(server.server_address[1], flush=True)
server.serve_forever()
PY
: > "$work/requests"
python3 "$work/standin.py" "$work/requests" > "$work/port" &
server_pid=$!
for _ in $(seq 100); do [ -s "$work/port" ] && break; sleep 0.1; done
port=$(<"$work/port")
[ -n "$port" ] || { echo "FAIL: the stand-in server did not start"; exit 1; }
base="http://127.0.0.1:$port"
echo "curl: $(curl --version | sed -n 1p)"

# run_fetch <case> <argument…>: the script with these arguments, waited for under DEADLINE; `out`
# is what it printed on both streams and `status` its exit. A script still running at the deadline
# fails the case by name.
run_fetch() {
  local case=$1
  shift
  set +e
  timeout --kill-after=5 "$DEADLINE" "$script" "$@" > "$work/stdout" 2> "$work/stderr"
  status=$?
  set -e
  out=$(cat "$work/stdout" "$work/stderr")
  check "$case: the script ended before the case's ${DEADLINE} s deadline" \
    "$(is "$([ "$status" -ne 124 ] && [ "$status" -ne 137 ] && echo yes)" yes)"
}
# requests <path>: the requests the stand-in saw for that path, one `<method> <status>` a line.
requests() { awk -v path="$1" '$2 == path { print $1, $3 }' "$work/requests" | paste -sd, -; }

echo "case: a download the server answers is fetched once"
run_fetch "answers" test "$ENDS" "$base/ok/answers" -o "$work/answers"
check "exits 0" "$(is "$status" 0)"
check "asked once" "$(is "$(requests /ok/answers)" "GET 200")"
check "wrote the body" "$(is "$(<"$work/answers")" "$BODY")"
check "printed nothing" "$(is "$out" "")"

echo "case: a 503 once is tried again and recovers"
run_fetch "503 once" test "$ENDS" "$base/503-once/download" -o "$work/503-once"
check "exits 0" "$(is "$status" 0)"
check "asked twice" "$(is "$(requests /503-once/download)" "GET 503,GET 200")"
check "wrote the body" "$(is "$(<"$work/503-once")" "$BODY")"

echo "case: a check that the address answers (-I) is tried again and recovers"
run_fetch "503 once, HEAD" test "$ENDS" "$base/503-once/head" -I -o /dev/null
check "exits 0" "$(is "$status" 0)"
check "asked twice, by HEAD" "$(is "$(requests /503-once/head)" "HEAD 503,HEAD 200")"

echo "case: a 503 on every attempt fails by name after three"
run_fetch "503 always" test "$ENDS" "$base/503-always/download" -o "$work/503-always"
check "exits 1" "$(is "$status" 1)"
check "asked three times" "$(is "$(requests /503-always/download)" "GET 503,GET 503,GET 503")"
check "names the stage, the address, the bound and curl's exit" \
  "$(contains "$out" "^test: $base/503-always/download failed after up to 3 attempts of $ENDS s (curl exit 22)$")"

echo "case: a 404 fails by name after three"
run_fetch "404" test "$ENDS" "$base/404/download" -o "$work/404"
check "exits 1" "$(is "$status" 1)"
check "asked three times" "$(is "$(requests /404/download)" "GET 404,GET 404,GET 404")"
check "names the stage, the address, the bound and curl's exit" \
  "$(contains "$out" "^test: $base/404/download failed after up to 3 attempts of $ENDS s (curl exit 22)$")"

echo "case: a body that stalls is cut at the bound, and the next attempt fetches it whole"
run_fetch "stall once" test "$STALLS" "$base/stall-once/download" -o "$work/stall-once"
check "exits 0" "$(is "$status" 0)"
check "asked twice" "$(is "$(requests /stall-once/download)" "GET stall,GET 200")"
check "wrote the whole body and no byte of the cut attempt" "$(is "$(<"$work/stall-once")" "$BODY")"

echo "case: a body that stalls every time is cut at each bound, and no attempt starts after twice the bound"
run_fetch "stall always" test "$STALLS" "$base/stall-always/download" -o "$work/stall-always"
check "exits 1" "$(is "$status" 1)"
# The first attempt is cut at 2 s and the second starts at 3 s, after curl's 1 s pause, and is cut
# at 5 s: past twice the bound, so curl starts no third.
check "asked twice" "$(is "$(requests /stall-always/download)" "GET stall,GET stall")"
check "names the stage, the address, the bound and curl's timeout" \
  "$(contains "$out" "^test: $base/stall-always/download failed after up to 3 attempts of $STALLS s (curl exit 28)$")"

echo "case: a Retry-After that would wait past twice the bound ends the retries"
run_fetch "Retry-After" test "$STALLS" "$base/retry-after-long/download" -o "$work/retry-after"
check "exits 1" "$(is "$status" 1)"
check "asked once" "$(is "$(requests /retry-after-long/download)" "GET 503")"
check "names the stage, the address, the bound and curl's exit" \
  "$(contains "$out" "^test: $base/retry-after-long/download failed after up to 3 attempts of $STALLS s (curl exit 22)$")"

echo "case: a call without a stage, a bound and an address, or with a bound that is no number of seconds"
run_fetch "no address" test "$ENDS"
check "exits 2" "$(is "$status" 2)"
check "says the usage" "$(contains "$out" "usage: fetch.sh <stage> <seconds> <url>")"
run_fetch "no bound" test 1m "$base/ok/no-bound" -o "$work/no-bound"
check "exits 2 for a bound of 1m" "$(is "$status" 2)"
check "names the bound" "$(contains "$out" "the bound '1m' is no whole number of seconds above 0")"
check "asks nothing" "$(is "$(requests /ok/no-bound)" "")"

summary "fetch.sh"
