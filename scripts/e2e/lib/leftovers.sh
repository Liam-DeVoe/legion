#!/usr/bin/env bash
# A stage driver's check that it is not starting beside what an earlier run of the same stage left
# running. A driver that dies with no chance to clean up (SIGKILL, a crashed host) leaves its daemon,
# its listener, its agents' processes and its containers behind, and a later run would otherwise
# start beside them without a word. Everything a run starts carries the pid of the driver that
# started it: each container is named <prefix>-<role>-<pid>, and every process of the run holds its
# work directory, /tmp/<prefix>.<pid>.<random>, in its argv, its working directory or its
# environment (an agent only as its working directory, a bridge only through the state directories
# it is handed). A run of the same stage that is still alive owns its own, and two lanes may run a
# stage at once, so only what a dead driver started is a leftover.
#
# A Postgres is the exception, and no judge of this file's: every shell stack's Postgres container
# carries the mark every test stack's Postgres carries (packages/dispatch/e2e/
# harness-postgres-marks.ts), and the run's start removes, before it starts its own, each Postgres
# whose mark names a process that is gone, the browser pool's among them, by the one removal each
# start of a stack runs (packages/dispatch/e2e/postgres-mark.ts), the same way on a dev box and in
# CI. Each needs bun, and a start with no bun on PATH fails with a line that names it:
#   remove_leftover_postgres     removes each Postgres of a stack that is gone, leaves each other,
#                                and names on stderr each it leaves, one line per kind.
#   postgres_mark RUN CONTAINER  prints the --label value that marks CONTAINER as the Postgres of
#                                the run whose work directory is RUN, owned by this shell ($$).
# refuse_leftovers leaves a marked container to that removal.

_postgres_mark_cli=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../.." && pwd)/packages/dispatch/e2e/postgres-mark.ts

# _postgres_mark ARGS... runs the command with ARGS, or fails, naming bun, where there is no bun.
_postgres_mark() {
  command -v bun >/dev/null || {
    echo "lib/leftovers.sh: bun is required: the mark and the removal of a stack's Postgres run through bun $_postgres_mark_cli" >&2
    return 1
  }
  bun "$_postgres_mark_cli" "$@"
}

remove_leftover_postgres() {
  _postgres_mark sweep
}

postgres_mark() {
  _postgres_mark label "$$" "$1" "$2"
}

# leftover_started PID prints the process's start time, in clock ticks since boot (proc(5), stat
# field 22), and fails for a pid with no process or a zombie.
leftover_started() {
  local stat
  stat=$(cat "/proc/$1/stat" 2>/dev/null) || return 1
  # The command name, field 2, is parenthesised and may hold spaces; the fields after it are not.
  read -r -a stat <<<"${stat##*) }"
  [ "${stat[0]}" != Z ] || return 1
  printf '%s\n' "${stat[19]}"
}

# leftover_owner OWNER SINCE: OWNER is a live process that started no later than SINCE (clock ticks
# since boot), so it is the driver that started what SINCE dates, not a later process given its pid.
leftover_owner() {
  local start
  start=$(leftover_started "$1") || return 1
  [ "$start" -le "$2" ]
}

# leftover_now_us VAR sets VAR to the wall clock in microseconds since the epoch: from bash's own
# EPOCHREALTIME, with no process started, so two reads are microseconds apart, or from date.
leftover_now_us() {
  if [ -n "${EPOCHREALTIME:-}" ]; then printf -v "$1" '%s' "${EPOCHREALTIME//[^0-9]/}"; else printf -v "$1" '%s' "$(date +%s%6N)"; fi
}

# leftover_boot_ns prints the earliest moment the machine can have booted, in nanoseconds since
# the epoch, and it is that moment to within a millisecond. /proc/uptime counts the time since the
# boot in hundredths of a second, and the clock of a process's start counts it in ticks; btime of
# /proc/stat is a whole second. So the wall clock is read at a step of the count, when the time
# since the boot is exactly what the count says: the step to U fell between the start of the read
# that still said less and the end of the read that says U, and the boot is U before that start.
# A step that the scheduler stretches past a millisecond is read again, up to 50 times; past that
# the boot is the earliest of the last, wider, window.
leftover_boot_ns() {
  local up last='' before last_before='' after tries=0
  while :; do
    leftover_now_us before
    read -r up _ </proc/uptime
    leftover_now_us after
    up=${up//[^0-9]/}
    if [ -n "$last" ] && [ "$up" != "$last" ]; then
      if [ $((10#$after - 10#$last_before)) -le 1000 ] || [ "$tries" -ge 50 ]; then
        printf '%s\n' "$(((10#$last_before - 10#$up * 10000) * 1000))"
        return 0
      fi
      tries=$((tries + 1))
    fi
    last=$up last_before=$before
  done
}

# refuse_leftovers PREFIX fails, naming each one and the command that removes it, when a container
# or a process of an earlier PREFIX run outlived its driver. The caller's `fail` reports it. A
# container's name ends in its driver's pid, or in that pid and the run's 16 random hex digits
# (<prefix>-<role>-<pid>-<random>, unique where two PID namespaces share a pid).
#
# A container's owner is the live process of its pid that started no later than the container was
# made, compared at the precision both clocks give: the owner's start in clock ticks since the
# boot, and Docker's creation in nanoseconds, turned into ticks since the boot (leftover_boot_ns).
# Where the two fall in one tick the owner counts as alive: a driver that made its container a few
# milliseconds after it started is never a leftover, and a later process given a dead driver's pid
# is taken for it only where it started in the tick (10 ms at 100 a second) its container was made.
refuse_leftovers() {
  local prefix=$1 name mark owner created p text since boot tick
  local -a leftover_list=()
  boot=$(leftover_boot_ns)
  tick=$((1000000000 / $(getconf CLK_TCK)))
  while IFS='|' read -r name mark; do
    [ -z "$mark" ] || continue
    [[ $name =~ -([0-9]+)(-[0-9a-f]{16})?$ ]] || continue
    owner=${BASH_REMATCH[1]}
    created=$(docker inspect -f '{{.Created}}' "$name" 2>/dev/null) || continue
    created=$(date -d "$created" +%s%N) || continue
    # A container made before this boot has no owner among the processes of this boot.
    since=-1
    [ "$created" -lt "$boot" ] || since=$(((created - boot) / tick))
    leftover_owner "$owner" "$since" || leftover_list+=("container $name (docker rm -f -v $name)")
  done < <(docker ps -a --filter "name=^$prefix-" --format '{{.Names}}|{{.Label "dev.legion.dispatch-e2e.postgres-mark"}}')
  for p in /proc/[0-9]*; do
    # A pid is its namespace's own: a process in another pid namespace (an agent box) names a
    # driver pid that means nothing here, and one of another user's reads as nothing.
    [ "$p/ns/pid" -ef /proc/self/ns/pid ] || continue
    # A process that ends between the listing and the reads has nothing left to read; it is no
    # leftover. Each read is guarded, so the caller's ERR trap never sees one fail.
    text="$(tr '\0' ' ' 2>/dev/null <"$p/cmdline" || true) $(readlink "$p/cwd" 2>/dev/null || true)/ $(tr '\0' ' ' 2>/dev/null <"$p/environ" || true)"
    [[ $text =~ /tmp/$prefix\.([0-9]+)\. ]] || continue
    owner=${BASH_REMATCH[1]}
    since=$(leftover_started "${p#/proc/}") || continue
    leftover_owner "$owner" "$since" && continue
    text=$(tr '\0' ' ' 2>/dev/null <"$p/cmdline" || true)
    text=${text% }
    leftover_list+=("process ${p#/proc/}, ${text:0:100} (kill ${p#/proc/})")
  done
  [ "${#leftover_list[@]}" -eq 0 ] && return 0
  fail "an earlier $prefix run whose driver is gone left these running: $(printf '%s; ' "${leftover_list[@]}")remove them and run again"
}
