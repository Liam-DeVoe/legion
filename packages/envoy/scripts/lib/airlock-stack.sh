# packages/envoy/scripts/lib/airlock-stack.sh
# The airlock's own half of a local stack, in one place: the stand-ins that mint the certificate
# authority a proxy decrypts with, the rules file that says which secret may go to which host, and
# the BROKER_AIRLOCK_* settings a broker is started with. It is sourced, never run.
#
# scripts/dev-stack.sh calls it under --project-access, so one stack can drive a proxied request
# beside everything else it drives. scripts/dev-airlock.sh builds the same stack by hand, with far
# more of it: two brokers behind two fronts, six kinds of secret, the model judge and the stands
# for GitHub, Google, Microsoft and AWS. Only what both need is here.
#
# A bound host listens on port 443 of a loopback address of its own. That is not a choice: the
# proxy compares a request's Host header against its CONNECT target, and
# airlockproxy.hostHeaderOf trims ":443" alone, so a bound host on any other port is refused
# "host header mismatch" before anything else it could prove. A machine whose
# /proc/sys/net/ipv4/ip_unprivileged_port_start is above 443 therefore cannot serve a bound host
# without privilege; airlock_stands_start says so and answers 1, and a caller that only needs the
# proxy's own answers (a CONNECT refused, or not) goes on with no bound host at all.

# airlock_stands_start starts cmd/dev-airlock-stands in work_dir and sets, for the caller:
#   AIRLOCK_STANDS_PID        the process, to stop
#   AIRLOCK_CA_CERT_FILE      the authority the proxy decrypts with
#   AIRLOCK_CA_KEY_FILE       its key
#   AIRLOCK_RECORDS_URL       the stands' recording routes
#   AIRLOCK_S_HOST            the one bound host's name, when address is not empty
#   AIRLOCK_S_URL             https://<that name>, when address is not empty
#   AIRLOCK_DIAL_ALLOW        what the proxy may dial although it resolves to loopback
#   AIRLOCK_RESOLVE           where that name is
# $1 is the stands binary, $2 the work directory, $3 the bound host's name and $4 its address
# (host:port, port 443) or "" for a run that binds no upstream.
airlock_stands_start() {
  local binary="$1" work_dir="$2" host="$3" address="$4"
  local stands_json="$work_dir/stands.json" stands_log="$work_dir/stands.log"
  local -a bound=()
  if [ -n "$address" ]; then
    local port="${address##*:}"
    if [ "$port" != 443 ]; then
      echo "airlock-stack: a bound host listens on 443, not $port: the proxy refuses any other port as a host header mismatch" >&2
      return 1
    fi
    if ! python3 -c "import socket,sys; s=socket.socket(); s.bind(('${address%:*}', 443)); s.close()" 2>/dev/null; then
      echo "airlock-stack: this machine does not let ${address} be bound: a bound host needs port 443, which needs privilege here (/proc/sys/net/ipv4/ip_unprivileged_port_start)" >&2
      return 1
    fi
    bound=(--address "s=$address" --host "s=$host")
  fi
  : >"$stands_log"
  "$binary" --dir "$work_dir/stands" "${bound[@]}" \
    >"$stands_json" 2> >(trap '' TERM INT; exec tee -a "$stands_log" >&2) &
  AIRLOCK_STANDS_PID=$!
  local waited=0
  while [ "$waited" -lt 120 ]; do
    grep -q 'stands listening records=' "$stands_log" 2>/dev/null && break
    kill -0 "$AIRLOCK_STANDS_PID" 2>/dev/null || {
      echo "airlock-stack: dev-airlock-stands exited during startup; see $stands_log" >&2
      return 1
    }
    sleep 0.5
    waited=$((waited + 1))
  done
  if ! grep -q 'stands listening records=' "$stands_log" 2>/dev/null; then
    echo "airlock-stack: dev-airlock-stands never logged its addresses within 60s; see $stands_log" >&2
    return 1
  fi
  AIRLOCK_CA_CERT_FILE="$(jq -r .ca_cert_file "$stands_json")"
  AIRLOCK_CA_KEY_FILE="$(jq -r .ca_key_file "$stands_json")"
  AIRLOCK_RECORDS_URL="$(jq -r .records_url "$stands_json")"
  AIRLOCK_S_HOST=""
  AIRLOCK_S_URL=""
  AIRLOCK_DIAL_ALLOW=""
  AIRLOCK_RESOLVE=""
  if [ -n "$address" ]; then
    AIRLOCK_S_HOST="$host"
    AIRLOCK_S_URL="https://$host"
    AIRLOCK_DIAL_ALLOW="$host:443"
    AIRLOCK_RESOLVE="$host=${address%:*}"
  fi
}

# airlock_rules_write writes the rules file at $1 that binds each name after it to the host $2,
# one entry per name and nothing else: the value of such a secret leaves the broker only through
# the proxy, and only to that host.
airlock_rules_write() {
  local file="$1" host="$2"
  shift 2
  local entries="" name
  for name in "$@"; do
    [ -z "$entries" ] || entries="$entries,"
    entries="$entries
  {\"name\": \"$name\", \"hosts\": [\"$host\"]}"
  done
  printf '{"secrets": [%s\n]}\n' "$entries" >"$file"
  chmod 600 "$file"
}

# airlock_broker_env prints, one per line, the BROKER_AIRLOCK_* settings a broker reads to serve
# the airlock: the rules file $1, a proxy port the system picks, the proxy URL $2 a release hands
# to a session, the authority read above, and where the proxy may dial the bound host. A caller
# reads them into an array (mapfile) and passes them to the broker it starts.
airlock_broker_env() {
  local rules_file="$1" proxy_url="$2"
  printf '%s\n' \
    "BROKER_AIRLOCK_RULES_FILE=$rules_file" \
    "BROKER_AIRLOCK_LISTEN_ADDR=127.0.0.1:0" \
    "BROKER_AIRLOCK_PROXY_URL=$proxy_url" \
    "BROKER_AIRLOCK_CA_CERT_FILE=$AIRLOCK_CA_CERT_FILE" \
    "BROKER_AIRLOCK_CA_KEY_FILE=$AIRLOCK_CA_KEY_FILE" \
    "BROKER_AIRLOCK_TEST_UPSTREAM_CA_FILE=$AIRLOCK_CA_CERT_FILE" \
    "BROKER_AIRLOCK_TEST_DIAL_ALLOW=$AIRLOCK_DIAL_ALLOW" \
    "BROKER_AIRLOCK_TEST_RESOLVE=$AIRLOCK_RESOLVE"
}
