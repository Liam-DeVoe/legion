#!/bin/sh
# One collection for every object a SeaweedFS server stores, grown before anything is written,
# and the checks that say it held. This is the one implementation of that rule, and both places
# that start this store run it: the shard job of .github/workflows/dispatch-browser.yaml, for the
# browser suite's file store, and tests3.Start (seaweedfs.go beside this file, which embeds it),
# inside the Go tests' SeaweedFS container. So the order of its steps, its waits and its checks
# cannot drift apart between the two.
#
#   one-collection.sh prepare <collection> <server log>   once the S3 port answers, before any write
#   one-collection.sh check <collection> <server log>     after writes
#
# It drives the `weed server` on this machine's default ports (master 127.0.0.1:9333, filer
# 127.0.0.1:8888) with `weed shell` and `wget`, which both images carry (busybox's in the
# SeaweedFS image), and reads that server's log from the file named. It stays POSIX: the
# SeaweedFS image's sh is busybox's, and the shard job runs its steps with `sh -e`. On a failure
# it prints a `::error::` line naming what failed, what it read, and the end of the server's log,
# and exits 1.
#
# WHY. SeaweedFS keeps a bucket's objects in a collection of the bucket's own name and makes that
# collection's seven volumes when the bucket's first object is written. That growth races the
# volume server's heartbeat: the master can read a heartbeat collected before the collection's
# first volume existed, take the volume back and drop the collection's layout while the write
# waits on it. The write then fails after 10 s. When the filer gives up on it before the master
# answers, every later write to the store fails at once ("Connection wait stopped: context
# canceled" in the log) until the store is restarted; when the master answers first, a retry of
# the write can succeed. Run 44292015929 met the first outcome in shard 2: the first object of
# dispatch-e2e-files-worker-2 was a row's upload, Dispatch answered it 502
# FILE_STORE_UNAVAILABLE, and the row and its retry each waited 45 s for a Download link that
# could not come. The race is rare, not absent: 1 of the 1,066 growths of the 265 shard legs of
# 200 ci.yaml runs, read from their record artifacts on 2026-11-18 (10:02 to 13:06Z). A growth
# that no write waits on survives it, since the next heartbeat lists the volume again. So no
# write may grow a collection.
#
# The log lines this reads are SeaweedFS 3.97's, which .github/ci-images.yaml pins by digest for
# both images; whoever moves that row reads them again.
set -eu

usage() {
  echo "::error::usage: one-collection.sh prepare|check <collection> <server log>; it was given: $*" >&2
  exit 1
}
[ $# -eq 3 ] || usage "$@"
step=$1
collection=$2
log=$3
shell="weed shell -master=127.0.0.1:9333 -filer=127.0.0.1:8888"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# fail <message> [file...]: the message as an error, each file read, the end of the server's log.
fail() {
  echo "::error::$1"
  shift
  for file in "$@"; do
    echo "--- $(basename "$file")"
    cat "$file"
  done
  echo "--- the last 200 lines of $log"
  tail -n 200 "$log" 2>/dev/null || echo "(no log at $log)"
  exit 1
}

# 1. The collection's seven volumes, made by `/vol/grow` before anything is written. It runs
# first, before the rule of step 2 exists, for two reasons. The filer writes a file of its own log
# about a minute after it starts, and once the rule is in force that file goes into the collection
# too, so the collection must hold its volumes before the rule can send any write to it. And a
# `weed shell` that connects before the volume server has registered with the master waits for
# ever: the master sends a new client one message for each volume server it knows
# (master_grpc_server.go's KeepConnected), so with none it sends nothing and the shell prints dots
# until it is killed. The grow is retried because the master answers before the volume server
# has registered with it, and once it succeeds the volume server is there for every shell below.
# It names the collection itself, so it needs no rule.
grow() {
  attempt=1
  until wget -q -O /dev/null "http://127.0.0.1:9333/vol/grow?collection=$collection&count=7"; do
    [ "$attempt" -lt 60 ] || fail "the master grew no volumes for $collection"
    attempt=$((attempt + 1))
    sleep 1
  done
}

# 2. The rule: every path into the collection, so each bucket and the filer's own files share
# it. Then a directory under /etc/seaweedfs, made after the rule, which step 4 waits for.
configure() {
  marker="rule-in-force-$$"
  printf 'fs.configure -locationPrefix=/ -collection=%s -apply\nfs.mkdir /etc/seaweedfs/%s\n' "$collection" "$marker" |
    $shell > "$work/rule.txt" 2>&1 || fail "weed shell could not run" "$work/rule.txt"
}

# 3. The rule, read back. `weed shell` exits 0 when a command inside it fails, and fs.configure
# returns success on a flag it cannot parse, so neither says whether the rule was stored. The
# filer's configuration as fs.configure prints it must hold the rule instead.
read_back() {
  echo fs.configure | $shell > "$work/configuration.txt" 2>&1 || fail "weed shell could not run" "$work/configuration.txt"
  tr -d ' \t\n' < "$work/configuration.txt" | grep -qF "{\"locationPrefix\":\"/\",\"collection\":\"$collection\"," ||
    fail "the filer holds no rule putting every path into $collection" "$work/rule.txt" "$work/configuration.txt"
}

# 4. The rule in force. fs.configure stores the rule and returns; the filer puts it in force only
# when it processes that change, in order with every change under /etc/seaweedfs, and it logs
# each one (filer_on_meta_event.go's "procesing", spelt so). Measured on a dev box, that came
# 0.2 s after a readiness write that had started at once, so the write grew a collection of its
# bucket's name. Once the marker's line is logged, the rule before it is in force.
wait_in_force() {
  attempt=1
  until grep -q "procesing.*$marker" "$log"; do
    [ "$attempt" -lt 60 ] || fail "the filer did not put the rule for $collection in force within a minute" "$work/rule.txt"
    attempt=$((attempt + 1))
    sleep 1
  done
}

# collection.list into collections.txt: one line per collection, `collection:"<name>"` and then
# its counts, after the shell's prompt.
list() {
  echo collection.list | $shell > "$work/collections.txt" 2>&1 || fail "weed shell could not run" "$work/collections.txt"
}

# alone <pattern>: collections.txt lists one collection, and its line matches pattern.
alone() {
  [ "$(grep -c 'collection:"' "$work/collections.txt")" = 1 ] && grep -q "$1" "$work/collections.txt"
}

# 5. The collection alone, with its volumes, before any write. Before anything is written a
# collection can exist only because step 1 made it, so a grow that is missing, aimed at another
# collection or made nothing fails here, on the plain store, and not only on the rare run where
# the race comes.
alone_before_writes() {
  list
  alone "collection:\"$collection\"[[:space:]]*volumeCount:[1-9]" ||
    fail "before the first write the store holds no volumes of $collection alone" "$work/collections.txt"
}

# After writes: no write grew storage, and the collection is still the store's only one. The
# master logs `volume grow &{...}` (master_grpc_server_volume.go) only for a growth that an
# assign asked for because the layout had no writable volume, which is a write waiting on storage
# being made; the grow of step 1 logs `volumeGrowHandler received` instead. A log that is missing
# or empty is a failure, not a pass: nothing could be read from it.
check() {
  [ -s "$log" ] || fail "the server's log $log is missing or empty, so no growth can be read from it"
  if grep 'volume grow &' "$log" > "$work/growths.txt"; then
    fail "a write grew the store's storage: the master grew a collection for a write that found no writable volume" "$work/growths.txt"
  fi
  list
  alone "collection:\"$collection\"" ||
    fail "the store holds a collection other than $collection, so a write grew one" "$work/collections.txt"
}

prepare() {
  grow
  configure
  read_back
  wait_in_force
  alone_before_writes
}

case $step in
  prepare) prepare ;;
  check) check ;;
  *) usage "$@" ;;
esac
