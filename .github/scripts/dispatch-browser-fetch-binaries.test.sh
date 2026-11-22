#!/usr/bin/env bash
# Tests for `dispatch-browser-fetch-binaries.sh`, the second read of the browser suite's binaries
# artifact. Each case starts a stand-in GitHub on 127.0.0.1: the run's artifact list, the
# artifact's API address, which redirects to a blob address on another host name (localhost),
# and the blob, which serves a fixture zip and honours a byte range. A case's mode makes the blob
# stall partway through its first answer, serve bytes whose digest is not the artifact's, or the
# list hold no artifact. The script runs against it as the step runs it, with the low-speed
# bound shortened to the stand-in's scale.
#
# The stall case has a control: the same stall with a low-speed bound longer than the case's
# limit never completes, which shows the stand-in really stalls and the bound is what ends it.
#
# Run from anywhere: .github/scripts/dispatch-browser-fetch-binaries.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"
fetch="$script_dir/dispatch-browser-fetch-binaries.sh"
work=$(mktemp -d)
server=""
trap '[ -z "$server" ] || kill "$server" 2>/dev/null || true; rm -rf "$work"' EXIT

# The fixture: four files of random bytes, zipped as upload-artifact zips them (deflate).
python3 - "$work" <<'PY'
import os, sys, zipfile
work = sys.argv[1]
os.makedirs(f"{work}/files")
for name, size in (("broker", 400_000), ("dispatch", 300_000), ("dev-oidc-issuer", 90_000), ("requestseed", 80_000)):
    with open(f"{work}/files/{name}", "wb") as f:
        f.write(os.urandom(size))
with zipfile.ZipFile(f"{work}/fixture.zip", "w", zipfile.ZIP_DEFLATED) as z:
    for name in sorted(os.listdir(f"{work}/files")):
        z.write(f"{work}/files/{name}", name)
PY

cat > "$work/server.py" <<'PY'
# The stand-in GitHub: python3 server.py <mode> <zip> <port-file> <log>.
import hashlib, http.server, json, sys, time
mode, zip_path, port_file, log_path = sys.argv[1:5]
blob = open(zip_path, "rb").read()
digest = "sha256:" + hashlib.sha256(blob).hexdigest()
served = blob if mode != "corrupt" else blob[:100] + bytes([blob[100] ^ 0xFF]) + blob[101:]
STALL_AT = 65_536
blob_reads = 0

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def note(self, what):
        with open(log_path, "a") as log:
            auth = "yes" if self.headers.get("Authorization") == "Bearer test-token" else (
                "other" if self.headers.get("Authorization") else "no")
            log.write(f"{what} auth={auth} range={self.headers.get('Range', 'none')}\n")

    def do_GET(self):
        global blob_reads
        port = self.server.server_address[1]
        if self.path.startswith("/repos/o/r/actions/runs/7/artifacts?name=dispatch-e2e-binaries"):
            self.note("list")
            if self.headers.get("Authorization") != "Bearer test-token":
                return self.send_error(401)
            artifacts = [] if mode == "missing" else [{
                "name": "dispatch-e2e-binaries", "expired": False, "size_in_bytes": len(blob),
                "digest": digest, "archive_download_url": f"http://127.0.0.1:{port}/zip"}]
            body = json.dumps({"total_count": len(artifacts), "artifacts": artifacts}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            return self.wfile.write(body)
        if self.path == "/zip":
            self.note("zip")
            if self.headers.get("Authorization") != "Bearer test-token":
                return self.send_error(401)
            self.send_response(302)
            self.send_header("Location", f"http://localhost:{port}/blob")
            self.send_header("Content-Length", "0")
            return self.end_headers()
        if self.path == "/blob":
            self.note("blob")
            blob_reads += 1
            start = 0
            ranged = self.headers.get("Range", "")
            if ranged.startswith("bytes="):
                start = int(ranged[len("bytes="):].split("-")[0])
            body = served[start:]
            self.send_response(206 if start else 200)
            self.send_header("Content-Type", "application/zip")
            self.send_header("Content-Length", str(len(body)))
            if start:
                self.send_header("Content-Range", f"bytes {start}-{len(served) - 1}/{len(served)}")
            self.end_headers()
            if mode == "stall" and blob_reads == 1:
                self.wfile.write(body[:STALL_AT])
                self.wfile.flush()
                time.sleep(30)  # the stall: the connection stays open and sends nothing
                return
            return self.wfile.write(body)
        self.send_error(404)

httpd = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
httpd.daemon_threads = True
with open(port_file + ".tmp", "w") as f:
    f.write(str(httpd.server_address[1]))
import os
os.rename(port_file + ".tmp", port_file)
httpd.serve_forever()
PY

# start <mode>: starts the stand-in for one case and sets $port, $server, $log and $out.
start() {
  local case_dir
  case_dir=$(mktemp -d "$work/case.XXXXXX")
  log="$case_dir/requests.log"
  out="$case_dir/out"
  : > "$log"
  python3 "$work/server.py" "$1" "$work/fixture.zip" "$case_dir/port" "$log" &
  server=$!
  for _ in $(seq 1 100); do
    [ -f "$case_dir/port" ] && break
    sleep 0.05
  done
  port=$(cat "$case_dir/port")
}

stop() {
  kill "$server" 2>/dev/null || true
  wait "$server" 2>/dev/null || true
  server=""
}

# fetch_as_step [timeout] [env...]: runs the script as the step does, against this case's
# stand-in; prints its output and sets $status.
fetch_as_step() {
  local limit=$1
  shift
  status=0
  output=$(env GITHUB_API_URL="http://127.0.0.1:$port" GITHUB_REPOSITORY=o/r GITHUB_RUN_ID=7 \
    GH_TOKEN=test-token DISPATCH_BROWSER_FETCH_SPEED_LIMIT=1000 DISPATCH_BROWSER_FETCH_SPEED_TIME=1 \
    "$@" timeout "$limit" "$fetch" "$out" 2>&1) || status=$?
}

same_files() {
  local name
  for name in broker dispatch dev-oidc-issuer requestseed; do
    cmp -s "$work/files/$name" "$out/$name" || { echo false; return; }
  done
  echo true
}

echo "A read that completes: one attempt, the token kept to the API's host"
start clean
fetch_as_step 30
check "exits 0 ($status)" "$(is "$status" 0)"
check "unzips the artifact's four files, byte for byte" "$(same_files)"
check "reads in one attempt" "$(is "$(grep -c '^attempt ' <<<"$output")" 1)"
check "sends the token to the artifact list and to the artifact's API address" \
  "$(is "$(grep -c '^\(list\|zip\) auth=yes' "$log")" 2)"
check "sends no token to the blob's host" "$(contains "$(grep '^blob' "$log")" '^blob auth=no range=none$')"
stop

echo "A read that stalls partway: the low-speed bound ends it, and the next attempt resumes"
start stall
fetch_as_step 30
check "exits 0 ($status)" "$(is "$status" 0)"
check "unzips the artifact's four files, byte for byte" "$(same_files)"
check "reads in two attempts" "$(is "$(grep -c '^attempt ' <<<"$output")" 2)"
check "the first attempt says where it stopped" "$(contains "$output" '^attempt 1: curl exit 28, HTTP 200, 65536 bytes')"
check "the second attempt asks for the bytes after the stall" \
  "$(is "$(grep '^blob' "$log" | sed -n 2p)" 'blob auth=no range=bytes=65536-')"
check "each attempt names the TCP segments retransmitted during it" "$(contains "$output" 'TCP segments retransmitted')"
stop

echo "Control: the same stall with a low-speed bound past the case's limit never completes"
start stall
fetch_as_step 4 DISPATCH_BROWSER_FETCH_SPEED_TIME=60
check "is ended by the case's limit (status 124, was $status)" "$(is "$status" 124)"
check "reads the blob once" "$(is "$(grep -c '^blob' "$log")" 1)"
stop

echo "A blob whose bytes are not the artifact's digest: refused, and read again from byte 0"
start corrupt
fetch_as_step 30 DISPATCH_BROWSER_FETCH_ATTEMPTS=2
check "exits 1 ($status)" "$(is "$status" 1)"
check "says the sha256 is not the artifact's" "$(contains "$output" 'their sha256 is not the artifact')"
check "says the artifact was not read whole, and that no row failed" \
  "$(contains "$output" 'was not read whole in 2 attempts.*not a failed browser row')"
check "starts the second attempt from byte 0" "$(is "$(grep '^blob' "$log" | sed -n 2p)" 'blob auth=no range=none')"
check "unzips nothing" "$(is "$(find "$out" -type f | wc -l)" 0)"
stop

echo "A run with no such artifact: refused by name"
start missing
fetch_as_step 30
check "exits 1 ($status)" "$(is "$status" 1)"
check "names the artifact it found none of" "$(contains "$output" 'holds 0 unexpired artifacts named dispatch-e2e-binaries')"
stop

echo "Called without its directory: refused"
status=0
output=$("$fetch" 2>&1) || status=$?
check "exits 1 ($status)" "$(is "$status" 1)"
check "says what it takes" "$(contains "$output" 'takes the directory to unzip into')"

summary "dispatch-browser-fetch-binaries.sh"
