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
# stop_process PID [SECONDS]: PID sent SIGSTOP, and waited until every thread of it reads stopped
# (T or t) or ended (Z or X: a thread that has ended forks nothing), for SECONDS (10 by default)
# at most; it returns 1 when one still runs then, or when its entry cannot be read. A signal is
# delivered when the kernel next runs its process, so a parent sent SIGSTOP that is not run before
# its child is killed reaps that child at once, from the wait it is in, and only then stops: a case
# that holds a dead child in the table under a stopped parent waits for the stop first. A child that
# ends after its parent is stopped stays a zombie, which never reads T, so a zombie counts as
# stopped.
#
# process_start PID: the process's start time (field 22 of /proc/PID/stat), or nothing when its
# entry cannot be read, so a pid the kernel gave another process since is told apart.
#
# process_state PID: the state letter of its main thread (field 3 of /proc/PID/stat: R, S, T, Z
# and so on), or nothing when its entry cannot be read.
#
# record_pid FILE PID: "PID START" appended to FILE, the record of a process a suite started that
# may outlive the case that started it -- a fake that runs in the background, a stand-in, a step
# started with & -- for end_recorded. A pid that has already gone is not recorded.
#
# end_recorded FILE: each process FILE records that is still the process recorded (its start the
# same) ended with every process below it (end_tree). A suite's exit ends what its cases left
# this way, by the pids they recorded, never by a pattern of command lines, which would reach
# another run's processes as well. It returns 1 when end_tree did, whose lines name each process.
#
# end_tree PID [SECONDS]: PID and every process below it ended: each sent SIGSTOP and seen stopped
# (stop_process, SECONDS each) before its children are read, so that none starts one the reading
# misses, then all of them SIGKILL, and each waited until it has ended (wait_gone, SECONDS each).
# SECONDS is 10 by default. Every process is walked: one that is not seen stopped is named, still
# gets the SIGKILL, and its children are still read. It returns 1, naming each, when a process was
# not seen stopped or has not ended, or when a children file held a token that is no pid; a PID
# already gone is nothing to end.

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
  local pid=$1 bound=${2:-10} t0=$EPOCHSECONDS task stat running
  kill -STOP "$pid" 2>/dev/null || return 1
  while :; do
    running=unread
    for task in "/proc/$pid"/task/*; do
      [ "$task" != "/proc/$pid/task/*" ] || break
      [ "$running" = yes ] || running=no
      if ! { read -r stat <"$task/stat"; } 2>/dev/null; then
        [ ! -e "$task" ] || running=yes
        continue
      fi
      stat=${stat##*) }
      case ${stat%% *} in
      T | t | Z | X) ;;
      *) running=yes ;;
      esac
    done
    [ "$running" != no ] || return 0
    [ -e "/proc/$pid" ] || return 1
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

process_state() {
  local stat
  { read -r stat <"/proc/$1/stat"; } 2>/dev/null || return 0
  stat=${stat##*) }
  printf '%s\n' "${stat%% *}"
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
      printf 'end_recorded: pid %s, which %s records, was not shown ended with every process below it (the lines above say which)\n' "$pid" "$1" >&2
      rc=1
    }
  done <"$1"
  return "$rc"
}

end_tree() {
  local pid=$1 bound=${2:-10} walked seen i=0 file kid found=() rc=0
  [[ $pid =~ ^[1-9][0-9]*$ ]] || {
    printf 'end_tree: refusing invalid process id %q\n' "$pid" >&2
    return 1
  }
  walked=("$pid") seen=" $pid "
  if ! stop_process "$pid" "$bound"; then
    [ -e "/proc/$pid" ] || return 0
    printf 'end_tree: pid %s was not seen stopped within %s s of its SIGSTOP; it is sent the SIGKILL with the rest, and its children are read\n' "$pid" "$bound" >&2
    rc=1
  fi
  while ((i < ${#walked[@]})); do
    for file in "/proc/${walked[i]}"/task/*/children; do
      found=()
      { read -r -a found <"$file"; } 2>/dev/null || true
      for kid in "${found[@]}"; do
        [[ $seen != *" $kid "* ]] || continue
        seen+="$kid "
        [[ $kid =~ ^[1-9][0-9]*$ ]] || {
          printf 'end_tree: refusing invalid process id %q below pid %s\n' "$kid" "${walked[i]}" >&2
          rc=1
          continue
        }
        if ! stop_process "$kid" "$bound"; then
          [ -e "/proc/$kid" ] || continue
          printf 'end_tree: pid %s, below pid %s, was not seen stopped within %s s of its SIGSTOP; it is sent the SIGKILL with the rest, and its children are read\n' "$kid" "${walked[i]}" "$bound" >&2
          rc=1
        fi
        walked+=("$kid")
      done
    done
    i=$((i + 1))
  done
  # no-wait: each of walked[@] is waited for just below, one at a time, until it has ended
  # (wait_gone).
  kill -KILL "${walked[@]}" 2>/dev/null || true
  for i in "${!walked[@]}"; do
    wait_gone "${walked[i]}" "$bound" || {
      printf 'end_tree: pid %s has not ended %s s after its SIGKILL\n' "${walked[i]}" "$bound" >&2
      rc=1
    }
  done
  return "$rc"
}
