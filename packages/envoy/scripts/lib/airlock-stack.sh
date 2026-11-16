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

# airlock_stands_start starts cmd/dev-airlock-stands and sets, for the caller:
#   AIRLOCK_STANDS_CONTAINER  the container to stop, "" for a run on this machine
#   AIRLOCK_STANDS_PID        the process to stop, "" for a run in a container
#   AIRLOCK_CA_CERT_FILE      the authority the proxy decrypts with
#   AIRLOCK_CA_KEY_FILE       its key
#   AIRLOCK_S_HOST            the bound host's name, "" for a run that binds none
#   AIRLOCK_S_URL             https://<that name>, "" for a run that binds none
#   AIRLOCK_DIAL_ALLOW        what the proxy may dial although it is not a public address
#   AIRLOCK_RESOLVE           where that name is
#
# $1 is the stands binary, $2 the work directory, $3 the bound host's name, $4 the image a
# container run uses ("" to run on this machine), and $5 a name for the container.
#
# How a bound host gets port 443. On a machine whose ip_unprivileged_port_start is above 443, or
# whose 443 another listener holds, nothing this script runs can bind it: the measurements are in
# the pull request of dispatch://INFRA-3862. So the stands run in an ordinary bridge container
# instead, binding 0.0.0.0:443 inside a network namespace of their own, and the broker on this
# machine dials the container's own address, which a bridge network routes to. The proxy refuses
# every private address it is not told about, so that address is named in both
# BROKER_AIRLOCK_TEST_RESOLVE and BROKER_AIRLOCK_TEST_DIAL_ALLOW, which exist for exactly this.
# The work directory is bind-mounted writable, since the broker on this machine reads the
# certificate authority the stands write into it.
airlock_stands_start() {
  local binary="$1" work_dir="$2" host="$3" image="$4" container="$5"
  local stands_dir="$work_dir/stands" stands_json="$work_dir/stands.json" stands_log="$work_dir/stands.log"
  mkdir -p "$stands_dir"
  : >"$stands_log"
  AIRLOCK_STANDS_CONTAINER=""
  AIRLOCK_STANDS_PID=""
  if [ -n "$image" ]; then
    docker rm -f "$container" >/dev/null 2>&1 || true
    if ! docker run -d --name "$container" \
      -v "$binary:/dev-airlock-stands:ro" -v "$stands_dir:/stands" \
      "$image" /dev-airlock-stands --dir /stands --address "s=0.0.0.0:443" --host "s=$host" \
      >"$work_dir/stands-container" 2>"$stands_log"; then
      echo "airlock-stack: docker refused to start the stands; see $stands_log" >&2
      return 1
    fi
    AIRLOCK_STANDS_CONTAINER="$container"
  else
    "$binary" --dir "$stands_dir" --address "s=0.0.0.0:443" --host "s=$host" \
      >"$stands_json" 2> >(trap '' TERM INT; exec tee -a "$stands_log" >&2) &
    AIRLOCK_STANDS_PID=$!
  fi
  local waited=0 listening=""
  while [ "$waited" -lt 120 ]; do
    if [ -n "$AIRLOCK_STANDS_CONTAINER" ]; then
      # The stands write their JSON to stdout and their lines to stderr; docker keeps the two
      # apart, so each is read on its own and neither is parsed as the other.
      docker logs "$container" >"$stands_json" 2>"$stands_log" || true
    fi
    if grep -q 'stands listening records=' "$stands_log" 2>/dev/null; then
      listening=yes
      break
    fi
    sleep 0.5
    waited=$((waited + 1))
  done
  if [ -z "$listening" ]; then
    echo "airlock-stack: dev-airlock-stands never logged its addresses within 60s; see $stands_log" >&2
    return 1
  fi
  # The stands name each file by the path they wrote it at, which inside a container is the
  # container's. The bind mount makes /stands this run's own directory, so the paths the broker
  # is given are this machine's.
  AIRLOCK_CA_CERT_FILE="$(jq -r .ca_cert_file "$stands_json")"
  AIRLOCK_CA_KEY_FILE="$(jq -r .ca_key_file "$stands_json")"
  if [ -n "$AIRLOCK_STANDS_CONTAINER" ]; then
    AIRLOCK_CA_CERT_FILE="${stands_dir}${AIRLOCK_CA_CERT_FILE#/stands}"
    AIRLOCK_CA_KEY_FILE="${stands_dir}${AIRLOCK_CA_KEY_FILE#/stands}"
    # The stands run as the container's root and write the key readable by its owner alone, so
    # the broker on this machine, which is another user, could not read it. The container's own
    # root is what can change that, so it is asked to.
    docker exec "$container" chmod 0644 /stands/ca.crt /stands/ca.key >/dev/null 2>&1 || true
  fi
  if [ ! -s "$AIRLOCK_CA_CERT_FILE" ] || [ ! -s "$AIRLOCK_CA_KEY_FILE" ]; then
    echo "airlock-stack: the stands wrote no certificate authority this machine can read ($AIRLOCK_CA_CERT_FILE)" >&2
    return 1
  fi
  local address="127.0.0.1"
  if [ -n "$AIRLOCK_STANDS_CONTAINER" ]; then
    address="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$container" 2>/dev/null | head -n1)"
    if [ -z "$address" ]; then
      echo "airlock-stack: the stands container $container has no address of its own" >&2
      return 1
    fi
  fi
  AIRLOCK_S_HOST="$host"
  AIRLOCK_S_URL="https://$host"
  AIRLOCK_DIAL_ALLOW="$host:443"
  AIRLOCK_RESOLVE="$host=$address"
}

# airlock_stands_stop stops whichever of the two a start left behind.
airlock_stands_stop() {
  [ -z "${AIRLOCK_STANDS_CONTAINER:-}" ] || docker rm -f "$AIRLOCK_STANDS_CONTAINER" >/dev/null 2>&1 || true
  if [ -n "${AIRLOCK_STANDS_PID:-}" ] && kill -0 "$AIRLOCK_STANDS_PID" 2>/dev/null; then
    kill -TERM "$AIRLOCK_STANDS_PID" 2>/dev/null || true
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
