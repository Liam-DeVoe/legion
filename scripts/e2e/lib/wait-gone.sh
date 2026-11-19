# shellcheck shell=bash
# The one wait for a process a script has sent a signal, before a later step binds its port, takes
# its lock, reads a file it writes, removes its directory or reads the process table: lib/rig.sh,
# lib/box-login.sh, scripts/e2e/stage2-tmux-supervision.sh, scripts/e2e/verifiers-staging-token.sh,
# packages/envoy/scripts/airlock-check.sh, packages/envoy/scripts/callout-check.sh and
# packages/envoy/scripts/dev-stack.test.sh source it. Sourced, never run: it defines wait_gone and
# kill_gone, and needs nothing of its caller's.
#
# A signal ends nothing by itself. A process given SIGTERM goes on until its handler has run, and a
# server's handler drains first. One given SIGKILL ends the next time the kernel runs it, and until
# its exit has finished it keeps its port, its descriptors (an inherited lock among them) and its
# working directory: for a process holding a few GiB of memory that is a measurable time, in which
# its port refuses a bind and /proc still shows it. Once its exit has finished it is a zombie, which
# holds none of these: only its pid, until its parent reaps it. Gone is dead: reaped, or a zombie.

# wait_gone SECONDS TARGET... returns 0 once every TARGET is gone, and 1 once the clock has passed
# SECONDS whole seconds since the call with one still there, so it never gives up sooner than
# SECONDS. A TARGET is a pid, or -PGID for every process of that group. A pid counts as gone once
# /proc holds no process of that pid that started when the first read found it, or holds it only as
# a zombie: a zombie is its parent's to reap, and a parent that is itself waiting on the caller (a
# test runner's synchronous spawn) would never reap it while this waited. A pid the kernel hands a
# new process during the wait is not taken for the one waited on. Where /proc cannot be read, and
# for a group, kill -0 answers, a zombie included. It prints nothing: the targets still there are
# left in the array wait_gone_left, for the caller to name in its own terms. The locals' names are
# ones no caller passes.
wait_gone() {
  local _wg_seconds=$1 _wg_end _wg_now _wg_target _wg_start _wg_proc=1
  shift
  local -a _wg_targets=() _wg_still=()
  local -A _wg_started=()
  [ -r /proc/self/stat ] || _wg_proc=
  for _wg_target in "$@"; do
    [ -n "$_wg_target" ] || continue
    _wg_targets+=("$_wg_target")
    if [ -n "$_wg_proc" ] && [ "${_wg_target#-}" = "$_wg_target" ] &&
      _wait_gone_started "$_wg_target" _wg_start; then
      _wg_started[$_wg_target]=$_wg_start
    fi
  done
  printf -v _wg_end '%(%s)T' -1
  _wg_end=$((_wg_end + _wg_seconds))
  while :; do
    _wg_still=()
    for _wg_target in ${_wg_targets[@]+"${_wg_targets[@]}"}; do
      if [ "${_wg_target#-}" != "$_wg_target" ] || [ -z "$_wg_proc" ]; then
        # A group, or no /proc: kill -0 answers, a zombie member or pid included.
        ! kill -0 -- "$_wg_target" 2>/dev/null || _wg_still+=("$_wg_target")
      elif [ -n "${_wg_started[$_wg_target]:-}" ] && _wait_gone_started "$_wg_target" _wg_start &&
        [ "$_wg_start" = "${_wg_started[$_wg_target]}" ]; then
        _wg_still+=("$_wg_target")
      fi
    done
    wait_gone_left=(${_wg_still[@]+"${_wg_still[@]}"})
    [ "${#_wg_still[@]}" -gt 0 ] || return 0
    _wg_targets=("${_wg_still[@]}")
    printf -v _wg_now '%(%s)T' -1
    [ "$_wg_now" -le "$_wg_end" ] || return 1
    sleep 0.1
  done
}

# _wait_gone_started PID VAR assigns VAR the start time of the process PID names, in clock ticks
# since boot (proc(5), stat field 22), and fails when /proc holds no such process, or holds it only
# as a zombie (stat field 3, Z). The command name, field 2, is parenthesised and may hold spaces;
# the fields after it are not.
_wait_gone_started() {
  local _wgs_stat
  { read -r _wgs_stat <"/proc/$1/stat"; } 2>/dev/null || return 1
  read -r -a _wgs_stat <<<"${_wgs_stat##*) }"
  [ "${_wgs_stat[0]:-}" != Z ] && [ -n "${_wgs_stat[19]:-}" ] || return 1
  printf -v "$2" '%s' "${_wgs_stat[19]}"
}

# kill_gone SIGNAL SECONDS TARGET... sends SIGNAL to every TARGET (one already gone is no failure),
# waits up to SECONDS for all of them to leave the process table (wait_gone), and returns 0 once they
# have. Each one still there then is named on stderr with its command line, and it returns 1; a
# cleanup that must go on whatever it finds adds `|| true`, and the names are printed either way.
kill_gone() {
  local _kg_signal=$1 _kg_seconds=$2 _kg_target _kg_command
  shift 2
  local -a _kg_targets=()
  for _kg_target in "$@"; do
    [ -z "$_kg_target" ] || _kg_targets+=("$_kg_target")
  done
  [ "${#_kg_targets[@]}" -gt 0 ] || return 0
  kill -s "$_kg_signal" -- "${_kg_targets[@]}" 2>/dev/null || true
  wait_gone "$_kg_seconds" "${_kg_targets[@]}" && return 0
  for _kg_target in ${wait_gone_left[@]+"${wait_gone_left[@]}"}; do
    if [ "${_kg_target#-}" != "$_kg_target" ]; then
      _kg_command="process group ${_kg_target#-} still has a process"
    else
      _kg_command=$(tr '\0' ' ' 2>/dev/null <"/proc/$_kg_target/cmdline") || _kg_command=
      _kg_command="pid $_kg_target (${_kg_command% }) is still"
    fi
    echo "$_kg_command in the process table ${_kg_seconds} s after its SIG${_kg_signal}" >&2
  done
  return 1
}
