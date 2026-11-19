#!/usr/bin/env bash
# Keeps the devbox Dispatch on the newest main commit with a published image.
set -euo pipefail

POLL="${POLL:-300}"
REPO="${REPO:-/home/ubuntu/.worktrees/legion/dn-an}"
COMPOSE_PROJECT="${COMPOSE_PROJECT:-envoy-dispatch}"
COMPOSE_DIR="${COMPOSE_DIR:-$REPO/packages/envoy/deploy/compose}"
BACKUPS="${BACKUPS:-/home/ubuntu/tmp/dispatch-backups}"
IMAGE="${IMAGE:-ghcr.io/cybermonklabs/legion/envoy}"
COMPOSE_FILE="$COMPOSE_DIR/dispatch.compose.yml"
COMPOSE_ENV="$COMPOSE_DIR/.env"
# pg_dump N dumps servers <= N; the production database is Aurora PostgreSQL 16.
PG_DUMP_IMAGE="${PG_DUMP_IMAGE:-postgres:16.11-alpine}"
# SCHEMA_READ_DEADLINE is how long schema_applied polls the migration advisory key before it
# gives up on reading the applied set. It bounds the whole read and not merely the start of its
# last try: each try's psql is given the time this deadline leaves, capped at a minute.
SCHEMA_READ_DEADLINE="${SCHEMA_READ_DEADLINE:-60}"
# PASS_DEADLINE is how long one pass may take, in whole seconds, counted from its start. It is the
# pass's one deadline: every call of the pass that waits on the registry, Docker or the database
# is given the time it leaves (pass_bound), so a call that answers slowly is waited for, and only
# one that has not answered when the pass's time is spent is stopped, named as having run out the
# pass's deadline. Three steps of a deploy keep a bound of their own, each cut to what the pass
# leaves: PULL_BOUND, UP_BOUND and the health check's HEALTH_BOUND. The deadline is the pass's and
# not the poll's: a deploy's rollback comes after those three, so a deadline as short as POLL would
# leave every slow deploy's rollback no time, and the rollback would be refused.
PASS_DEADLINE="${PASS_DEADLINE:-3600}"
# PULL_BOUND is the pull's own bound. A pull that runs it out changes nothing: the previous image
# still serves, and the next pass pulls again.
PULL_BOUND=900
# UP_BOUND is the deploy's limit for the new image's start, `docker compose up -d`: a start that
# has not finished within it is rolled back, and the previous image serves again.
UP_BOUND=300
# HEALTH_TRIES is how many times wait_for_health asks /healthz, 3 s apart, each request bounded at
# 3 s, so HEALTH_BOUND is the most one health check takes.
HEALTH_TRIES=30
HEALTH_BOUND=$((HEALTH_TRIES * 6))

case "$#" in
  0)
    once=false
    ;;
  1)
    if [[ "$1" != "--once" ]]; then
      printf 'usage: %s [--once]\n' "$0" >&2
      exit 2
    fi
    once=true
    ;;
  *)
    printf 'usage: %s [--once]\n' "$0" >&2
    exit 2
    ;;
esac

# Each setting of seconds is a whole number above 0, or the deployer refuses to start: bash reads
# `300s` as an arithmetic error, which a walk that reads it fails on and takes for no image at all.
for setting in POLL PASS_DEADLINE SCHEMA_READ_DEADLINE; do
  if [[ ! "${!setting}" =~ ^[1-9][0-9]{0,8}$ ]]; then
    printf '%s is %q, which is no whole number of seconds from 1 to 999999999 (write 300, not 300s or 5m); not starting\n' \
      "$setting" "${!setting}" >&2
    exit 2
  fi
done
# The pass's deadline leaves a rollback its time: a pass whose deploy steps each run out their own
# bound still has its rollback's longest bounded step left, the health check of the previous image
# or the schema read after the stop, whichever is the longer. The rollback's other steps have no
# bound of their own and take what is left.
rollback_longest=$((SCHEMA_READ_DEADLINE > HEALTH_BOUND ? SCHEMA_READ_DEADLINE : HEALTH_BOUND))
PASS_DEADLINE_MINIMUM=$((PULL_BOUND + UP_BOUND + HEALTH_BOUND + rollback_longest))
if ((PASS_DEADLINE < PASS_DEADLINE_MINIMUM)); then
  printf 'PASS_DEADLINE is %ss, below the %ss a pass needs: %ss for its deploy steps (the pull %ss, the up %ss, the health check %ss) and %ss for its rollback'"'"'s longest step; not starting\n' \
    "$PASS_DEADLINE" "$PASS_DEADLINE_MINIMUM" "$((PULL_BOUND + UP_BOUND + HEALTH_BOUND))" "$PULL_BOUND" "$UP_BOUND" "$HEALTH_BOUND" "$rollback_longest" >&2
  exit 2
fi

mkdir -p "$BACKUPS"

log() {
  printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"
}

# clock is the time the pass's deadline is read against, in whole seconds since the epoch.
clock() {
  date +%s
}

# pass_bound <call> [own] prints the seconds <call> may wait: what the pass's deadline leaves, or
# own, the call's own bound, where that is less. Once the deadline has passed it logs, on stderr,
# that <call> could not start, naming the deadline, and fails, printing nothing: no call is ever
# handed 0, which `timeout` and pg_client read as no bound at all. <call> says what is not done.
pass_bound() {
  local left=$((pass_deadline - $(clock)))
  if ((left < 1)); then
    log "the pass's deadline (PASS_DEADLINE=${PASS_DEADLINE}s) passed before $1" >&2
    return 1
  fi
  if [[ -n "${2:-}" ]] && ((left > $2)); then
    left=$2
  fi
  printf '%s\n' "$left"
}

# bound_note <status> [own] prints, for a call `timeout` stopped (status 124), the words that say
# which bound stopped it: the pass's deadline, where that has passed, or else the call's own bound
# of own seconds. It prints nothing for any other status.
bound_note() {
  (($1 == 124)) || return 0
  if (($(clock) >= pass_deadline)); then
    printf " (it had not answered when the pass's deadline, PASS_DEADLINE=%ss, passed)" "$PASS_DEADLINE"
  elif [[ -n "${2:-}" ]]; then
    printf ' (it ran out its own %ss)' "$2"
  fi
}

compose() {
  local duration="$1"
  shift
  timeout "$duration" docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" "$@"
}

compose_with_tag() {
  local tag="$1"
  local duration="$2"
  shift 2
  env ENVOY_IMAGE_TAG="$tag" timeout "$duration" docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" "$@"
}

env_value() {
  sed -nE "s/^$1=(.*)$/\1/p" "$COMPOSE_ENV"
}

deployed_tag() {
  env_value ENVOY_IMAGE_TAG
}

set_env_value() {
  if grep -q "^$1=" "$COMPOSE_ENV"; then
    sed -i -E "s|^$1=.*|$1=$2|" "$COMPOSE_ENV"
  else
    printf '%s=%s\n' "$1" "$2" >>"$COMPOSE_ENV"
  fi
}

# database_url_split reads DATABASE_URL into db_url, the URL without its password, and db_password,
# that password percent-decoded. The database clients below get db_url as their argument and the
# password as PGPASSWORD, an environment variable, so no process's arguments carry a credential:
# a container's processes are the host's, and /proc/<pid>/cmdline is world-readable on a shared
# host. It accepts exactly one shape and fails on every other, printing nothing:
#
#   postgres[ql]://<user>[:<password>]@<host>[:<port>]/<database>[?<key>=<value>[&<key>=<value>]…]
#
#   - <user>, <database> and each <value>: ASCII letters, digits, `.`, `_` and `-`.
#   - <password>: ASCII letters, digits, `.`, `_`, `~` and `-`, and %XX escapes other than %00,
#     whose decoded bytes are valid UTF-8. Any other character (@ : / ? # [ ], %, a space,
#     non-ASCII) has to be percent-encoded.
#   - <host>: dot-separated labels of ASCII letters, digits and hyphens, none empty and none
#     starting or ending with a hyphen. That is a host name or an IPv4 address; no brackets, no
#     second host, no socket path.
#   - <port>: 1 to 65535.
#   - <key>: sslmode, connect_timeout or application_name.
#
# In that shape every reading of the URL, the deployer's, libpq's and pgx's, puts the password in
# the same place, and nowhere else. LC_ALL=C makes the ranges ASCII whatever the caller's locale:
# under en_US.UTF-8, [a-z] and [0-9] also match letters and digits outside ASCII, and under a
# double-byte locale such as GBK one character can hide an @.
database_url_split() {
  local LC_ALL=C
  local value='[A-Za-z0-9._-]+'
  local key='(sslmode|connect_timeout|application_name)'
  local escape='%(0[1-9A-Fa-f]|[1-9A-Fa-f][0-9A-Fa-f])'
  local label='[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?'
  local port='([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])'
  local grammar="^(postgres|postgresql)://$value(:([A-Za-z0-9._~-]|$escape)+)?@$label(\\.$label)*(:$port)?/$value(\\?$key=$value(&$key=$value)*)?\$"
  local userinfo
  local password=""
  if [[ ! "$1" =~ $grammar ]]; then
    return 1
  fi
  # An accepted URL holds exactly one @, so the user and password are before it, and the rest of
  # the URL, the clients' part, is after it.
  userinfo="${1#*://}"
  userinfo="${userinfo%%@*}"
  if [[ "$userinfo" == *:* ]]; then
    password="${userinfo#*:}"
  fi
  printf -v db_password '%b' "${password//%/\\x}"
  # Docker rewrites an environment value that is not valid UTF-8, so such a password would reach
  # the clients as different bytes than libpq or pgx read from the URL. The check reads it from a
  # pipe, never an argument.
  if ! printf '%s' "$db_password" | iconv -f UTF-8 -t UTF-8 >/dev/null 2>&1; then
    return 1
  fi
  db_url="${1%%://*}://${userinfo%%:*}@${1#*@}"
}

# pg_client runs shell command $2 in the pg_dump image, bounded at $1 seconds, with the connection
# in its environment: DATABASE_URL (db_url) and PGPASSWORD (db_password), each passed to docker by
# name, so docker reads the value from this process's environment. Every caller takes $1 from
# pass_bound, which never gives 0, the value `timeout` reads as no bound. --init runs the command
# under an init that passes on the signal docker forwards when `timeout` fires; as PID 1 the shell
# would ignore it, and the container would outlive the bound.
pg_client() {
  PGPASSWORD="$db_password" DATABASE_URL="$db_url" timeout "$1" \
    docker run --init --rm --network host -e PGPASSWORD -e DATABASE_URL "$PG_DUMP_IMAGE" sh -c "$2"
}

# schema_applied prints a value standing for the set of migrations the database has applied:
#
#   <max>-<count>-<sha256 of the versions, ascending, one per line>
#
# Not max(version) alone, the `schema_version` Dispatch's /healthz reports: a migration whose
# version is below one already applied, a numbered migration merged after a timestamped one
# deployed, leaves max where it was, and a guard reading max alone would start the previous image
# on a schema it never ran against. The max and the count are there for a reader of a log line;
# the digest is what changes whenever the set changes.
#
# It reads under the advisory lock Dispatch's migration runner holds for each migration
# (internal/dispatch/store/store.go, migrationLockKey): one psql session runs a script that tries
# for the key and then, only once the try was granted, reads the versions. A migration
# transaction still in flight, one a stopped image sent its commit for included, is waited out
# only as far as the deadline below reaches: one that ends inside it ends before the read takes
# its snapshot, and one still holding the key when the deadline passes is not waited out at all:
# that read fails, and the caller refuses to deploy rather than read behind a held key. The
# versions a granted try reads come back ordered by the database, and the key is still held as
# they do (measured on PostgreSQL 18.6: while the read runs, the reading backend is the one
# session holding the key in that database). The session's end releases it. One statement
# could not take the key and read behind it: a statement's snapshot is taken when it starts,
# before its own one-time filter calls pg_try_advisory_lock, so its read would still be of the
# schema as it stood before the migration committed (measured too).
#
# It polls the key rather than waiting for it, because any blocking wait on this key deadlocks
# against the runner that holds it. A backend blocked on a lock keeps the snapshot its statement
# took, and CREATE INDEX CONCURRENTLY waits for every snapshot older than its own, so a holder
# building an index concurrently and a waiter on the key wait for each other (PostgreSQL 18.6,
# SQLSTATE 40P01). The runner polls for the same reason (store.go, lockMigrations). So each try is
# a psql session of its own, with no statement open between two tries: a try refused the key
# answers f, takes nothing and reads nothing at all, and the next try follows a second later as a
# fresh session holding nothing. The \if is what keeps the read out of a refused try, and it is
# no nicety. A read that ran behind a held key can fail (a first boot holds the key before
# schema_migrations is committed, and the runner's one-time widening ALTER TABLE makes the read
# wait out its lock_timeout), and under ON_ERROR_STOP=1 that failure ends psql non-zero, which
# would end this read in its caller rather than try the key again. Once the deadline passes with
# the key still held the read fails, and the caller refuses to deploy rather than wait for ever.
# The deadline bounds the database's answers, not Docker's: each try's psql is given the time the
# deadline leaves, capped at the minute a try is given where the deadline leaves more, inside the
# container, so a try that starts a second before the deadline ends at it rather than a minute past
# it. With SCHEMA_READ_DEADLINE where it stands, a first try against a free key still gets the
# whole minute. The container's start is Docker's: it waits as long as the pass's deadline leaves
# (pass_bound), since a Docker that creates a container slowly still answers, and a start refused
# at a bound of its own would refuse a rollback and leave Dispatch for a person.
#
# psql's stderr is dropped, since its diagnostics can quote the connection; a failure prints
# nothing and returns psql's status, or 1 for an answer that is no list of versions.
schema_applied() {
  local sql answer versions line count max digest deadline try left bound
  # The script reaches psql on its standard input, since \gset and \if are psql's own and -c
  # takes SQL alone. The here-document is the container shell's, so the script travels as part of
  # sh -c's argument (pg_client) and nothing of it reaches psql's own arguments; it carries no
  # credential, only the connection's name in the environment.
  sql="psql -X -q -At -v ON_ERROR_STOP=1 -f - \"\$DATABASE_URL\" <<'PSQL'
set lock_timeout = 30000;
select pg_try_advisory_lock(8150001) as got \\gset
\\echo :got
\\if :got
select version from schema_migrations order by version;
\\endif
PSQL
"
  deadline=$((SECONDS + SCHEMA_READ_DEADLINE))
  try=0
  while true; do
    # Two bounds on the poll, whichever comes first, both read before a try is sent rather than
    # after one is refused: the deadline, and the count of tries a deadline of seconds can hold
    # with a second between two of them. The count is what bounds the poll where a sleep returns
    # early (the test suite's own sleep returns at once, but for the cases that ask it for a real
    # one); without it such a run would reach for the key as fast as it can until the deadline
    # passes. The deadline is tested here and nowhere else, so a poll that lost this test would
    # run its 60 tries out past it, which is what the suite's three-second case reads.
    ((SECONDS < deadline && try++ < 60)) || return 1
    # What this try is given: the time the deadline leaves, capped at a minute. That is how the
    # read ends inside the deadline rather than a timeout past it, where a try starting a second
    # before the deadline would otherwise get a minute of its own. The test above has left at
    # least a second, so the container's `timeout` is never given 0, which it reads as no bound.
    left=$((deadline - SECONDS))
    ((left > 60)) && left=60
    bound=$(pass_bound "the schema read's container could start") || return 1
    answer=$(pg_client "$bound" "timeout $left $sql" 2>/dev/null) ||
      return
    [[ "$answer" == t || "$answer" == t$'\n'* ]] && break
    # f: another session holds the key. Any other answer is none of this read's.
    [[ "$answer" == f || "$answer" == f$'\n'* ]] || return 1
    sleep 1
  done
  versions="${answer#t}"
  versions="${versions#$'\n'}"
  count=0
  max=0
  digest=""
  if [[ -n "$versions" ]]; then
    while IFS= read -r line; do
      [[ "$line" =~ ^[0-9]+$ ]] || return 1
      digest+="$line"$'\n'
      count=$((count + 1))
      max="$line"
    done <<<"$versions"
  fi
  digest=$(printf '%s' "$digest" | sha256sum) || return 1
  printf '%s-%s-%s\n' "$max" "$count" "${digest%% *}"
}

# schema_changed answers whether the applied set moved from a recorded baseline to the set the
# database holds now: 0 when it moved, 1 when it did not. A baseline in the form above holds the
# whole set, so any difference is a move; a bare max(version), which the deployer before this one
# recorded, holds only the highest version, so all it can show is a version applied above it.
schema_changed() {
  local baseline="$1"
  local now="$2"
  if [[ "$baseline" == *-* ]]; then
    [[ "$now" != "$baseline" ]]
    return
  fi
  ((${now%%-*} > baseline))
}

# schema_label describes a value for a log line: the highest version applied, and, for a value
# that holds the set, how many versions that set holds.
schema_label() {
  local rest
  if [[ "$1" == *-* ]]; then
    rest="${1#*-}"
    printf '%s (%s applied)\n' "${1%%-*}" "${rest%%-*}"
  else
    printf '%s\n' "$1"
  fi
}

# newest_published_main prints the newest of main's last 30 commits whose image the registry holds,
# and returns 1 when the registry answers that it holds none of them. It passes over a commit only
# when the registry answered that it holds no such manifest. Silence and every other answer (a
# refused credential, a rate limit, a server error) prove nothing about the image, and a walk that
# passed over such a commit would pick an older one: where that commit is the one deployed, the
# pass would start an image older than its database's schema. So such a read is logged, naming the
# image it asked for, and returns 2, which fails the pass with nothing deployed.
#
# A read has no bound of its own, and the walk none either. Each read waits as long as the pass's
# deadline leaves (pass_bound), since a registry that answers slowly still answers, and only a
# read that has not answered when the pass's time is spent is stopped, named as having run out
# the pass's deadline. `docker manifest inspect` asks the registry and makes nothing on this host,
# so a stopped read leaves nothing to remove. The log lines go to stderr, because the caller reads
# this function's stdout as the commit.
newest_published_main() {
  local sha answer status bound
  jj -R "$REPO" git fetch >/dev/null 2>&1
  while IFS= read -r sha; do
    bound=$(pass_bound "docker manifest inspect $IMAGE:$sha could start; not deploying") || return 2
    if answer=$(timeout "$bound" docker manifest inspect "$IMAGE:$sha" 2>&1 >/dev/null); then
      printf '%s\n' "$sha"
      return 0
    else
      status=$?
    fi
    if ((status == 124)); then
      log "docker manifest inspect $IMAGE:$sha had not answered when the pass's deadline (PASS_DEADLINE=${PASS_DEADLINE}s) passed; not deploying" >&2
      return 2
    fi
    case "$answer" in
      *"manifest unknown"* | *"no such manifest"*) continue ;;
    esac
    log "docker manifest inspect $IMAGE:$sha failed with status $status, an answer that does not say the image is missing: ${answer%%$'\n'*}; not deploying" >&2
    return 2
  done < <(jj -R "$REPO" log -r 'ancestors(main@origin, 30)' --no-graph -T 'commit_id ++ "\n"')
  return 1
}

prune_backups() {
  local -a dumps=()
  local dump
  local i
  while IFS= read -r -d '' dump; do
    dumps+=("$dump")
  done < <(find "$BACKUPS" -maxdepth 1 -type f -name 'pre-*.dump' -printf '%T@:%p\0' | sort -znr)
  for ((i = 10; i < ${#dumps[@]}; i++)); do
    rm -f -- "${dumps[i]#*:}"
  done
}

oauth_redirect_uri() {
  local origin="$1"
  local port="$2"
  local host="${origin#http://}"
  host="${host#https://}"
  curl -s -o /dev/null -w '%{redirect_url}' -H "Host: $host" "http://127.0.0.1:$port/auth/start" |
    python3 -c 'import sys, urllib.parse as u; query = u.parse_qs(u.urlparse(sys.stdin.read()).query); print(query.get("redirect_uri", [""])[0])'
}

# wait_for_health asks /healthz on port $1, up to HEALTH_TRIES times 3 s apart and each request
# bounded at 3 s (HEALTH_BOUND, 180 s, at most), to answer 200 with NATS connected, or turned off
# (`nats` null). It is the deploy's health limit, and it asks Dispatch, not Docker. Dispatch serves
# while NATS is down or refuses its credential, so a 200 alone would keep an image whose NATS
# connection fails; reading `nats` rolls such an image back, as when Dispatch exited at boot without
# NATS. A NATS outage fails the previous image's check too.
wait_for_health() {
  local port="$1"
  local attempt
  for ((attempt = 0; attempt < HEALTH_TRIES; attempt++)); do
    sleep 3
    if curl -sf --max-time 3 "http://127.0.0.1:$port/healthz" |
      python3 -c 'import json, sys; sys.exit(json.load(sys.stdin).get("nats") is False)' 2>/dev/null; then
      return 0
    fi
  done
  return 1
}

# rollback starts $previous again. Its `up` has no bound of its own and waits as long as the pass's
# deadline leaves: Dispatch is down until it returns, and a Docker that starts a container slowly
# still answers, so a bound of its own would only turn a slow rollback into one a person finishes.
rollback() {
  local previous="$1"
  local dispatchPort="$2"
  local bound
  local status=0
  sed -i -E "s|^ENVOY_IMAGE_TAG=.*|ENVOY_IMAGE_TAG=$previous|" "$COMPOSE_ENV"
  bound=$(pass_bound "the rollback's docker compose up of $previous could start") || return 1
  compose "$bound" up -d --no-deps dispatch || status=$?
  if ((status != 0)); then
    log "rollback compose failed for $previous$(bound_note "$status")"
    return 1
  fi
  if ! wait_for_health "$dispatchPort"; then
    log "rollback to $previous did not become healthy in $HEALTH_TRIES tries 3 s apart"
    return 1
  fi
}

# roll_back_or_stop answers a deploy of $sha that failed. It restores $previous only while the
# schema is still the one $previous runs against. A failed image may have migrated before it failed,
# and an image older than its database's schema can write what the newer release fails to read
# (packages/envoy/cmd/dispatch/README.md, "Database migrations"). So it stops $sha first and
# confirms the stop, then reads the schema, so nothing $sha runs can move it after the read. Once the
# set of applied migrations has changed, or the stop or the read fails, it starts nothing and fails,
# for an operator to recover forward. It compares the whole set, not max(version): a migration
# applied below one already applied leaves the max where it was.
#
# A $baseline the deployer before this one recorded is a bare max(version), with no set behind it.
# While the tag that wrote it is still deployed, it is compared the old way, so a retry in flight
# across the upgrade keeps the safety it started with; the first deploy under a new tag records the
# set.
#
# Its stop, its confirmation and its schema read's container have no bound of their own: each
# waits as long as the pass's deadline leaves. Docker answers each of them, slowly on a loaded
# host, and a bound of its own that fired would refuse the rollback and leave Dispatch stopped for
# a person; only a call that has not answered when the pass's time is spent fails, named.
roll_back_or_stop() {
  local sha="$1"
  local previous="$2"
  local baseline="$3"
  local dispatchPort="$4"
  local running
  local now
  local status=0
  local refusing
  local bound
  refusing="so refusing to roll back to $previous, which runs against schema $(schema_label "$baseline"), and forward recovery is required"
  bound=$(pass_bound "docker compose stop of $sha could start, $refusing") || return 1
  compose "$bound" stop dispatch || status=$?
  if ((status != 0)); then
    log "could not stop $sha$(bound_note "$status"), $refusing"
    return 1
  fi
  bound=$(pass_bound "docker compose ps could confirm $sha stopped, $refusing") || return 1
  running=$(compose "$bound" ps --status running -q dispatch) || status=$?
  if ((status != 0)) || [[ -n "$running" ]]; then
    log "could not confirm $sha stopped$(bound_note "$status"), $refusing"
    return 1
  fi
  now=$(schema_applied) || {
    status=$?
    log "could not read the schema version after stopping $sha (status $status$(bound_note "$status")), $refusing"
    return 1
  }
  if schema_changed "$baseline" "$now"; then
    log "refusing to roll back to $previous: it runs against schema $(schema_label "$baseline"), and $sha changed the applied migrations to $(schema_label "$now") before it failed. $sha is stopped, and forward recovery is required: deploy an image that fixes it, or start $sha by hand once its failure is understood. Never start $previous against schema $(schema_label "$now")"
    return 1
  fi
  log "rolling back to $previous (schema $(schema_label "$now"))"
  rollback "$previous" "$dispatchPort"
}

deploy() {
  local sha="$1"
  local previous
  local databaseUrl
  local db_url
  local db_password
  local dispatchPort
  local status=0
  local before
  local recorded
  local baseline
  local dump
  local origin
  local redirect
  local expected
  local bound

  previous=$(deployed_tag)
  origin=$(env_value DISPATCH_SERVER_URL)
  if [[ -z "$origin" ]]; then
    log "DISPATCH_SERVER_URL is missing from $COMPOSE_ENV; not deploying $sha"
    return 1
  fi
  databaseUrl=$(env_value DATABASE_URL)
  if [[ -z "$databaseUrl" ]]; then
    log "DATABASE_URL is missing from $COMPOSE_ENV; not deploying $sha"
    return 1
  fi
  if ! database_url_split "$databaseUrl"; then
    log "DATABASE_URL in $COMPOSE_ENV is not in the one form the deployer accepts, the only one in which it can keep the password off its database clients' process arguments; see packages/envoy/deploy/README.md, \"Auto-deployer\", for the form; not deploying $sha"
    return 1
  fi
  dispatchPort=$(env_value DISPATCH_PORT)
  dispatchPort="${dispatchPort:-8766}"
  before=$(schema_applied) || {
    status=$?
    log "could not read the schema version (status $status$(bound_note "$status")); not deploying $sha"
    return 1
  }
  # ENVOY_IMAGE_SCHEMA is <tag>:<value>, where <value> stands for the set of migrations the
  # deployed tag was running against when the deployer first tried to replace it (schema_applied,
  # above). Kept while that tag stays deployed, it is what a rollback to it needs: a retry after a
  # failed deploy that migrated starts from the moved schema. A value the deployer before this one
  # wrote is a bare max(version), which roll_back_or_stop compares the old way.
  recorded=$(env_value ENVOY_IMAGE_SCHEMA)
  if [[ -n "$recorded" && "${recorded%:*}" == "$previous" ]]; then
    baseline="${recorded##*:}"
    if [[ ! "$baseline" =~ ^[0-9]+-[0-9]+-[0-9a-f]{64}$ && ! "$baseline" =~ ^[0-9]+$ ]]; then
      log "ENVOY_IMAGE_SCHEMA in $COMPOSE_ENV is '$recorded', not <tag>:<max>-<count>-<digest>; not deploying $sha"
      return 1
    fi
  else
    baseline="$before"
    if ! set_env_value ENVOY_IMAGE_SCHEMA "$previous:$baseline"; then
      log "could not record the schema version $previous runs against; not deploying $sha"
      return 1
    fi
  fi
  dump="$BACKUPS/pre-${sha:0:12}-$(date -u +%Y%m%dT%H%M%SZ).dump"
  bound=$(pass_bound "the backup could start; not deploying $sha") || return 1
  # shellcheck disable=SC2016 # $DATABASE_URL expands in the container's shell, from its environment
  pg_client "$bound" 'pg_dump -Fc "$DATABASE_URL"' >"$dump" || status=$?
  if ((status != 0)); then
    log "backup failed$(bound_note "$status"); not deploying $sha"
    rm -f "$dump"
    return 1
  fi
  prune_backups
  bound=$(pass_bound "the pull of $sha could start; $previous still serves" "$PULL_BOUND") || return 1
  compose_with_tag "$sha" "$bound" pull -q dispatch || status=$?
  if ((status != 0)); then
    log "could not pull $sha$(bound_note "$status" "$bound"); $previous still serves"
    return 1
  fi
  bound=$(pass_bound "the up of $sha could start; $previous still serves" "$UP_BOUND") || return 1
  compose_with_tag "$sha" "$bound" up -d --no-deps dispatch || status=$?
  if ((status != 0)); then
    log "compose up failed for $sha$(bound_note "$status" "$bound")"
    roll_back_or_stop "$sha" "$previous" "$baseline" "$dispatchPort"
    return 1
  fi
  if ! wait_for_health "$dispatchPort"; then
    log "deployed $sha but /healthz did not answer 200 with NATS connected in $HEALTH_TRIES tries 3 s apart"
    roll_back_or_stop "$sha" "$previous" "$baseline" "$dispatchPort"
    return 1
  fi
  redirect=$(oauth_redirect_uri "$origin" "$dispatchPort")
  expected="$origin/auth/callback"
  if [[ "$redirect" != "$expected" ]]; then
    log "deployed $sha but the OAuth redirect_uri is '$redirect', expected '$expected' - login would break"
    roll_back_or_stop "$sha" "$previous" "$baseline" "$dispatchPort"
    return 1
  fi
  if ! sed -i -E "s|^ENVOY_IMAGE_TAG=.*|ENVOY_IMAGE_TAG=$sha|" "$COMPOSE_ENV"; then
    log "could not persist deployed tag $sha"
    roll_back_or_stop "$sha" "$previous" "$baseline" "$dispatchPort"
    return 1
  fi
  log "deployed $sha ($(jj -R "$REPO" log -r "$sha" --no-graph -T 'description.first_line()' | cut -c1-90)); login origin $origin verified; backup $dump"
}

# commit_order <sha> <deployed> prints `older` when <sha> is an ancestor of the deployed commit,
# and `newer` when it is not, read from the history of the repository the walk reads. A deployed
# tag that names no commit the repository knows (`local`, a release tag set by hand) has no history
# for <sha> to be older than, so the answer is `newer`: the newest published main replaces it, as
# it always has. Where jj answers neither, it prints nothing.
commit_order() {
  jj -R "$REPO" log --no-graph -r "present(\"$1\")" \
    -T "if(self.contained_in(\"::present(\\\"$2\\\")\"), \"older\", \"newer\") ++ \"\\n\""
}

# run_once is one pass, against the pass's deadline (PASS_DEADLINE, counted from here). It never
# deploys a commit older than the deployed one: the walk passes over a commit the registry says it
# holds no image of, and where that is the deployed commit itself (its image was removed, or IMAGE
# names another repository), the commit it reaches next is older. So the newest published commit
# is deployed only when the repository's history says it is not an ancestor of the deployed one;
# an ancestor is named on one line with the deployed commit and nothing is deployed, and an order
# jj cannot read fails the pass, since a deploy needs the answer `newer`, not the absence of
# `older`.
run_once() {
  local sha status deployed order
  pass_deadline=$(($(clock) + PASS_DEADLINE))
  if sha=$(newest_published_main); then
    deployed=$(deployed_tag)
    [[ "$sha" != "$deployed" ]] || return 0
    order=$(commit_order "$sha" "$deployed") || order=
    case "$order" in
      newer)
        log "main has a newer published image: $sha (deployed $deployed)"
        deploy "$sha"
        ;;
      older)
        log "the newest published main commit, $sha, is an ancestor of the deployed $deployed; deploying nothing older over it"
        ;;
      *)
        log "could not read from $REPO whether $sha is older than the deployed $deployed; not deploying"
        return 1
        ;;
    esac
  else
    status=$?
    # 2: a read that proves nothing, which newest_published_main has logged.
    ((status == 1)) || return 1
    log "no published image among the last 30 main commits"
  fi
}

if [[ "$once" == true ]]; then
  run_once
  exit 0
fi

# The continuous loop runs each pass as a process of its own, a --once run, so a pass ends under
# set -e as one run does, and a failed pass ends that process alone: it has logged its one named
# line, the loop logs that it failed, and the next pass tries again after POLL. A `run_once ||`
# here would turn set -e off inside every pass.
log "autodeploy started; deployed=$(deployed_tag)"
while true; do
  "$BASH" "$0" --once || log "the pass failed (exit $?); the next pass starts in ${POLL}s"
  sleep "$POLL"
done
