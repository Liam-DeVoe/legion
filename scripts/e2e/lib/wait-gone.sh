# shellcheck shell=bash
# The one wait for a process a script has sent a signal, before a later step binds its port, takes
# its lock, reads a file it writes, removes its directory or reads the process table: lib/rig.sh,
# lib/box-login.sh, scripts/e2e/stage2-tmux-supervision.sh, verifiers-staging-token.sh and
# envoy-session-delivery.sh; packages/envoy/scripts/airlock-check.sh, callout-check.sh and
# dev-stack.test.sh; packages/pi-envoy/scripts/smoke-registration-stall.sh and smoke-role-takeover.sh;
# packages/dispatch/e2e/acceptance/replace-blocks.sh; and docs/research/temporal-evaluation-2026-11/
# run.sh source it. Sourced, never run: it defines wait_gone, kill_gone, stop_gone and
# process_start, and needs nothing of its caller's but bash 5 (EPOCHSECONDS and EPOCHREALTIME).
#
# A signal ends nothing by itself. A process given SIGTERM goes on until its handler has run, and a
# server's handler drains first. One given SIGKILL ends the next time the kernel runs it, and until
# its exit has finished it keeps its port, its descriptors (an inherited lock among them) and its
# working directory: for a process holding a few GiB of memory that is a measurable time, in which
# its port refuses a bind and /proc still shows it. Each thread of a process exits on its own, and
# the port, the descriptors and the memory are released only when the last one has: /proc/PID/stat
# shows the main thread alone, which can be a zombie (Z) while another thread of the same process
# still holds them all, as a Go program, Bun and any other process with threads can. So a pid is
# gone once /proc holds no process of that pid, or holds it only as a zombie whose task directory
# lists no thread but the main one: that zombie holds nothing but its pid, which its parent reaps.
#
# A pid gone does not make what it held free. A child it forked, or any process that inherited its
# socket or the lock's descriptor, keeps them after the pid is gone; so does any other process that
# holds a reference to the open file, since the file closes when its last reference goes. So a
# caller whose next step binds a port or takes a lock names that port or lock as a target too, and
# the wait reads that fact itself rather than inferring it from a pid: `port:N` is free once no
# socket listens on TCP port N (ss -Hltn, as a stage proof's ports_free reads it), and `lock:FILE`
# once FILE's flock can be taken (or FILE is not there).
#
# Every wait here ends at a DEADLINE, an absolute time in seconds since the epoch, whole as
# EPOCHSECONDS reads it or with up to six decimals as EPOCHREALTIME does: the caller's own, so that
# two waits of one stop (its SIGTERM's and its SIGKILL's) share one bound rather than each starting
# a fresh one. `$((EPOCHSECONDS + 10))` is ten seconds from now, less the part of this second
# already gone.

# wait_gone_proc is the /proc the waits read; a test points it at a tree of its own.
wait_gone_proc=/proc

# wait_gone DEADLINE TARGET... returns 0 once every TARGET is gone, and 1 once the clock has reached
# DEADLINE with one still there; it never gives up before DEADLINE, and it reads the targets once
# more at it. A TARGET is a pid, -PGID for every process of that group, `port:N` or `lock:FILE`. A
# pid is gone as the top of this file says, and the start time the first read finds is held to: a
# pid the kernel hands a new process during the wait is not taken for the one waited on. A pid whose
# /proc entry is there but cannot be read is not gone. For a group, and where /proc cannot be read
# at all, kill -0 answers, a zombie included. A port or a lock is gone once it is free as the top of
# this file says; one that cannot be read (no ss, a lock file that cannot be opened, an flock that
# fails for another reason) is not. It prints nothing: the targets still there are left in the array
# wait_gone_left, for the caller to name in its own terms. A DEADLINE that is no time since the
# epoch (a count of seconds from now, say) is refused with exit 2, naming it. The locals' names are
# ones no caller passes.
wait_gone() {
  local _wg_deadline=$1 _wg_deadline_us _wg_target _wg_start _wg_read _wg_proc=1
  shift
  local -a _wg_targets=() _wg_still=()
  local -A _wg_started=()
  _wait_gone_deadline wait_gone "$_wg_deadline" _wg_deadline_us || return 2
  [ -r "$wait_gone_proc/self/stat" ] || _wg_proc=
  for _wg_target in "$@"; do
    [ -n "$_wg_target" ] || continue
    if [ -n "$_wg_proc" ] && [[ $_wg_target != -* && $_wg_target != port:* && $_wg_target != lock:* ]]; then
      # A pid gone at the first read is gone: a process that takes its pid later is another one.
      _wg_start='' _wg_read=0
      process_start "$_wg_target" _wg_start || _wg_read=$?
      [ "$_wg_read" != 1 ] || continue
      _wg_started[$_wg_target]=$_wg_start
    fi
    _wg_targets+=("$_wg_target")
  done
  while :; do
    _wg_still=()
    for _wg_target in ${_wg_targets[@]+"${_wg_targets[@]}"}; do
      case $_wg_target in
        port:* | lock:*)
          _wait_gone_free "$_wg_target" || _wg_still+=("$_wg_target")
          continue
          ;;
      esac
      if [ "${_wg_target#-}" != "$_wg_target" ] || [ -z "$_wg_proc" ]; then
        # A group, or no /proc: kill -0 answers, a zombie member or pid included.
        ! kill -0 -- "$_wg_target" 2>/dev/null || _wg_still+=("$_wg_target")
        continue
      fi
      _wg_start='' _wg_read=0
      process_start "$_wg_target" _wg_start || _wg_read=$?
      case $_wg_read in
        # Gone: no such process, or a zombie that is its group's last thread.
        1) ;;
        # There but unreadable: not known to be gone.
        2) _wg_still+=("$_wg_target") ;;
        # There: the same process unless its start time changed, which is a new process on a
        # reused pid. One the first read could not read is held to the first start time read.
        *)
          if [ -z "${_wg_started[$_wg_target]:-}" ]; then
            _wg_started[$_wg_target]=$_wg_start
            _wg_still+=("$_wg_target")
          elif [ "$_wg_start" = "${_wg_started[$_wg_target]}" ]; then
            _wg_still+=("$_wg_target")
          fi
          ;;
      esac
    done
    wait_gone_left=(${_wg_still[@]+"${_wg_still[@]}"})
    [ "${#_wg_still[@]}" -gt 0 ] || return 0
    _wg_targets=("${_wg_still[@]}")
    [ "${EPOCHREALTIME//[!0-9]/}" -lt "$_wg_deadline_us" ] || return 1
    sleep 0.1
  done
}

# _wait_gone_free TARGET returns 0 when TARGET, `port:N` or `lock:FILE`, is free as the top of this
# file says, and 1 when it is held or cannot be read. A lock is read by taking it and dropping it at
# once, on a descriptor of its own.
_wait_gone_free() {
  local _wgf_listening _wgf_fd _wgf_status
  case $1 in
    port:*)
      _wgf_listening=$(ss -Hltn "sport = :${1#port:}" 2>/dev/null) || return 1
      [ -z "$_wgf_listening" ]
      ;;
    lock:*)
      [ -e "${1#lock:}" ] || return 0
      { exec {_wgf_fd}<"${1#lock:}"; } 2>/dev/null || return 1
      _wgf_status=0
      flock -n "$_wgf_fd" 2>/dev/null || _wgf_status=1
      exec {_wgf_fd}<&-
      return "$_wgf_status"
      ;;
    *) return 1 ;;
  esac
}

# _wait_gone_deadline WHO DEADLINE VAR assigns VAR the DEADLINE in microseconds since the epoch. It
# fails, naming WHO and DEADLINE on stderr, when DEADLINE is no time in seconds since the epoch at or
# after 2001-09-09 (1000000000), whole or with up to six decimals: a caller that passes a count of
# seconds from now gets this line rather than a wait that ends at once.
_wait_gone_deadline() {
  local _wgd_fraction
  if [[ $2 =~ ^([0-9]+)(\.([0-9]{1,6}))?$ ]] && [ "${BASH_REMATCH[1]}" -ge 1000000000 ]; then
    _wgd_fraction=${BASH_REMATCH[3]}000000
    printf -v "$3" '%s' "$((10#${BASH_REMATCH[1]} * 1000000 + 10#${_wgd_fraction:0:6}))"
    return 0
  fi
  echo "$1: the deadline '$2' is no time in seconds since the epoch (\$((EPOCHSECONDS + 10)) is ten seconds from now)" >&2
  return 1
}

# process_start PID VAR assigns VAR the start time of the process PID names, in clock ticks since
# boot (proc(5), stat field 22), and returns 0 while that process is there; 1 when it is gone (no
# /proc/PID, or a zombie, stat field 3, whose task directory lists no thread but the main one); and
# 2 when /proc/PID is there but its stat could not be read or holds no start time. A caller that
# records a pid with its start time can tell later whether the pid still names that process. The
# command name, field 2, is parenthesised and may hold spaces; the fields after it are not.
process_start() {
  local _wgr_stat
  local -a _wgr_tasks
  if ! { read -r _wgr_stat <"$wait_gone_proc/$1/stat"; } 2>/dev/null; then
    [ -e "$wait_gone_proc/$1" ] && return 2
    return 1
  fi
  read -r -a _wgr_stat <<<"${_wgr_stat##*) }"
  [ -n "${_wgr_stat[19]:-}" ] || return 2
  if [ "${_wgr_stat[0]}" = Z ]; then
    # The main thread has exited. With no other thread listed (or the directory gone, which leaves
    # the pattern itself, one word), the process holds nothing but its pid.
    _wgr_tasks=("$wait_gone_proc/$1/task/"*)
    [ "${#_wgr_tasks[@]}" -gt 1 ] || return 1
  fi
  printf -v "$2" '%s' "${_wgr_stat[19]}"
}

# kill_gone SIGNAL DEADLINE TARGET... sends SIGNAL to every TARGET that is a pid or a process group
# (one already gone is no failure), waits until DEADLINE for every TARGET to be gone (wait_gone),
# the ports and locks among them free, and returns 0 once they are. Each one still there at
# DEADLINE is named on stderr with its command line (or what holds the port), the deadline and the
# time since the signal, and it returns 1; 2 for a DEADLINE wait_gone refuses. A caller whose next
# step needs the targets gone fails when it returns 1; a cleanup that goes on whatever it finds adds
# `|| true` and says why.
kill_gone() {
  local _kg_signal=$1 _kg_deadline=$2 _kg_deadline_us _kg_target _kg_sent _kg_status
  shift 2
  local -a _kg_processes=() _kg_facts=()
  _wait_gone_deadline kill_gone "$_kg_deadline" _kg_deadline_us || return 2
  for _kg_target in "$@"; do
    case $_kg_target in
      '') ;;
      port:* | lock:*) _kg_facts+=("$_kg_target") ;;
      *) _kg_processes+=("$_kg_target") ;;
    esac
  done
  [ "$((${#_kg_processes[@]} + ${#_kg_facts[@]}))" -gt 0 ] || return 0
  _kg_sent=${EPOCHREALTIME//[!0-9]/}
  [ "${#_kg_processes[@]}" = 0 ] || kill -s "$_kg_signal" -- "${_kg_processes[@]}" 2>/dev/null || true
  wait_gone "$_kg_deadline" ${_kg_processes[@]+"${_kg_processes[@]}"} ${_kg_facts[@]+"${_kg_facts[@]}"} && return 0
  _kg_status=$?
  [ "$_kg_status" = 1 ] || return "$_kg_status"
  _wait_gone_name "SIG$_kg_signal" "$_kg_sent"
  return 1
}

# stop_gone GRACE DEADLINE TARGET... is a stop: SIGTERM to every TARGET that is a pid or a process
# group, and a wait of up to GRACE whole seconds from the call, counted to the microsecond and never
# past DEADLINE, for every TARGET to be gone (the ports and locks among them free); then SIGKILL to
# the processes still there and a wait for every TARGET until DEADLINE, so the two waits share the
# caller's one bound. A process in the middle of a write (a pane's token refresh, a server's drain)
# gets the whole grace to finish before anything is killed. Returns 0 once every TARGET is gone, 0
# too when none is given; 1 when one is still there at DEADLINE, named on stderr as kill_gone names
# it; 2 for a DEADLINE wait_gone refuses.
stop_gone() {
  local _sg_grace=$1 _sg_deadline=$2 _sg_deadline_us _sg_term_end _sg_target
  shift 2
  local -a _sg_processes=() _sg_facts=()
  _wait_gone_deadline stop_gone "$_sg_deadline" _sg_deadline_us || return 2
  for _sg_target in "$@"; do
    case $_sg_target in
      '') ;;
      port:* | lock:*) _sg_facts+=("$_sg_target") ;;
      *) _sg_processes+=("$_sg_target") ;;
    esac
  done
  [ "$((${#_sg_processes[@]} + ${#_sg_facts[@]}))" -gt 0 ] || return 0
  _sg_term_end=$((${EPOCHREALTIME//[!0-9]/} + _sg_grace * 1000000))
  [ "$_sg_term_end" -le "$_sg_deadline_us" ] || _sg_term_end=$_sg_deadline_us
  [ "${#_sg_processes[@]}" = 0 ] || kill -s TERM -- "${_sg_processes[@]}" 2>/dev/null || true
  wait_gone "${_sg_term_end:0:-6}.${_sg_term_end: -6}" ${_sg_processes[@]+"${_sg_processes[@]}"} ${_sg_facts[@]+"${_sg_facts[@]}"} && return 0
  kill_gone KILL "$_sg_deadline" "${wait_gone_left[@]}"
}

# _wait_gone_name SIGNAL SENT names, on stderr, each target wait_gone left, with the deadline it
# reached and the seconds since SENT (EPOCHREALTIME's microseconds, its separator taken out) that
# SIGNAL was sent: `pid N (COMMAND) is still in the process table at its deadline, 2.0 s after its
# SIGKILL`, `process group N still has a process ...`, `port N is still listened on (WHAT ss NAMES)
# ...`, `lock FILE is still held ...`, or, for an entry /proc holds but cannot read, `pid N:
# /proc/N/stat could not be read, so it is not known to be gone ...`.
_wait_gone_name() {
  local _wgn_signal=$1 _wgn_sent=$2 _wgn_target _wgn_what _wgn_tenths _wgn_start
  _wgn_tenths=$(((${EPOCHREALTIME//[!0-9]/} - _wgn_sent) / 100000))
  for _wgn_target in ${wait_gone_left[@]+"${wait_gone_left[@]}"}; do
    if [[ $_wgn_target == port:* ]]; then
      _wgn_what=$(ss -Hltnp "sport = :${_wgn_target#port:}" 2>&1 | tr -s ' \n' ' ') || _wgn_what=
      _wgn_what="port ${_wgn_target#port:} is still listened on (${_wgn_what% })"
    elif [[ $_wgn_target == lock:* ]]; then
      _wgn_what="lock ${_wgn_target#lock:} is still held"
    elif [ "${_wgn_target#-}" != "$_wgn_target" ]; then
      _wgn_what="process group ${_wgn_target#-} still has a process"
    elif ! process_start "$_wgn_target" _wgn_start && [ -e "$wait_gone_proc/$_wgn_target" ]; then
      _wgn_what="pid $_wgn_target: $wait_gone_proc/$_wgn_target/stat could not be read, so it is not known to be gone"
    else
      _wgn_what=$(tr '\0' ' ' 2>/dev/null <"$wait_gone_proc/$_wgn_target/cmdline") || _wgn_what=
      _wgn_what="pid $_wgn_target (${_wgn_what% }) is still in the process table"
    fi
    echo "$_wgn_what at its deadline, $((_wgn_tenths / 10)).$((_wgn_tenths % 10)) s after its $_wgn_signal" >&2
  done
}
