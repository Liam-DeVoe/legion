# shellcheck shell=bash
# test-lib.sh: what the load test's suites (vm/vm.test.sh, cluster/cluster.test.sh, common.test.sh
# and restart-step.test.sh) share about a process a case started and has to see end or hold still.
# They source it before they put a fake sleep first on PATH. Not executable, and no part of what
# the scripts it tests run.
#
# process_ended PID: the process PID names has ended: its /proc entry is gone, or its main thread
# is a zombie and its task directory lists no thread but the main one. A zombie main thread with
# another thread listed is a process still ending, whose threads still hold its files, its ports
# and its locks, so it has not ended. An entry there that cannot be read has not ended either, as
# far as a case can tell. It reads /proc with builtins alone, so a case can ask it at the moment a
# stop returns.
#
# wait_gone PID [SECONDS]: waited until PID has ended (process_ended), for SECONDS (10 by default)
# on the clock at most. It returns 1 when it has not, and the caller says so. A case's stand-in that
# outlives the shell that started it is reaped by no wait of the case's, so it is waited for in the
# table rather than by wait.
#
# end_process PID [SECONDS]: PID sent SIGKILL, and waited until it has ended (wait_gone).
#
# stop_process PID [SECONDS]: PID sent SIGSTOP, and waited until its state reads stopped (T), for
# SECONDS (10 by default) at most; it returns 1 when it has not. A signal is delivered when the
# kernel next runs its process, so a parent sent SIGSTOP that is not run before its child is
# killed reaps that child at once, from the wait it is in, and only then stops: a case that holds
# a dead child in the table under a stopped parent waits for the stop first.
#
# process_start PID: the process's start time (field 22 of /proc/PID/stat), or nothing when its
# entry cannot be read, so a pid the kernel gave another process since is told apart.
#
# record_pid FILE PID: "PID START" appended to FILE, the record of a process a suite started that
# may outlive the case that started it -- a fake that runs in the background, a stand-in, a step
# started with & -- for end_recorded. A pid that has already gone is not recorded.
#
# end_recorded FILE: each process FILE records that is still the process recorded (its start the
# same) ended with every process below it (end_tree). A suite's exit ends what its cases left
# this way, by the pids they recorded, never by a pattern of command lines, which would reach
# another run's processes as well. It returns 1, naming each process that has not ended.
#
# end_tree PID [SECONDS]: PID and every process below it ended: each sent SIGSTOP before its
# children are read, so that none starts one the reading misses, then all of them SIGKILL, and
# each waited until it has ended (wait_gone, SECONDS each, 10 by default). It returns 1 when one
# has not; a PID already gone is nothing to end.

TEST_LIB_SLEEP=$(command -v sleep)

process_ended() {
  local stat task
  [ -e "/proc/$1" ] || return 0
  if ! { read -r stat <"/proc/$1/stat"; } 2>/dev/null; then
    [ -e "/proc/$1" ] && return 1
    return 0
  fi
  stat=${stat##*) }
  [ "${stat%% *}" = Z ] || return 1
  for task in "/proc/$1"/task/*; do
    if [ "$task" = "/proc/$1/task/*" ]; then
      [ -e "/proc/$1" ] && return 1
      return 0
    fi
    [ "${task##*/}" = "$1" ] || return 1
  done
  return 0
}

wait_gone() {
  local pid=$1 bound=${2:-10} t0=$EPOCHSECONDS
  until process_ended "$pid"; do
    ((EPOCHSECONDS - t0 <= bound)) || return 1
    "$TEST_LIB_SLEEP" 0.1
  done
}

end_process() {
  local pid=$1
  kill -KILL "$pid" 2>/dev/null || true
  wait_gone "$pid" "${2:-10}" || return 1
}

stop_process() {
  local pid=$1 bound=${2:-10} t0=$EPOCHSECONDS stat
  kill -STOP "$pid" 2>/dev/null || return 1
  while :; do
    stat=""
    { read -r stat <"/proc/$pid/stat"; } 2>/dev/null || return 1
    stat=${stat##*) }
    [ "${stat%% *}" != T ] || return 0
    ((EPOCHSECONDS - t0 <= bound)) || return 1
    "$TEST_LIB_SLEEP" 0.05
  done
}

process_start() {
  local stat fields
  { read -r stat <"/proc/$1/stat"; } 2>/dev/null || return 0
  read -r -a fields <<<"${stat##*) }"
  printf '%s\n' "${fields[19]:-}"
}

record_pid() {
  local start
  start=$(process_start "$2")
  [ -z "$start" ] || printf '%s %s\n' "$2" "$start" >>"$1"
}

end_recorded() {
  local pid start now rc=0
  [ -f "$1" ] || return 0
  while read -r pid start; do
    now=$(process_start "$pid")
    [ -n "$now" ] && [ "$now" = "$start" ] || continue
    end_tree "$pid" || {
      printf 'end_recorded: pid %s, which %s records, or a process below it, still runs 10 s after its SIGKILL\n' "$pid" "$1" >&2
      rc=1
    }
  done <"$1"
  return "$rc"
}

end_tree() {
  local walked=("$1") i=0 file kid found=()
  kill -STOP "$1" 2>/dev/null || return 0
  while ((i < ${#walked[@]})); do
    for file in "/proc/${walked[i]}"/task/*/children; do
      found=()
      { read -r -a found <"$file"; } 2>/dev/null || true
      for kid in "${found[@]}"; do
        kill -STOP "$kid" 2>/dev/null || true
        walked+=("$kid")
      done
    done
    i=$((i + 1))
  done
  # no-wait: each of walked[@] is waited for just below, one at a time, until it has ended
  # (wait_gone).
  kill -KILL "${walked[@]}" 2>/dev/null || true
  for i in "${!walked[@]}"; do
    wait_gone "${walked[i]}" "${2:-10}" || return 1
  done
}
