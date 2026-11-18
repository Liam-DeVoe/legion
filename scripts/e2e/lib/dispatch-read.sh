# shellcheck shell=bash
# The prologue the two read scripts of a deployed issue share, step-records-read.sh and
# plan-ratings-read.sh: how each says it could judge nothing, and the one run of `dispatch` each
# makes and prints. Each script keeps its own usage and its own awk, since the two commands print
# different lines. It calls no command a PATH may lack: a script whose PATH holds no `dispatch`
# still says so. Sourced, never run.

# read_unknown REASON prints `UNKNOWN: REASON` and exits 2: the script could read nothing to judge.
read_unknown() {
  echo "UNKNOWN: $*"
  exit 2
}

# read_require_dispatch is read_unknown unless a `dispatch` command is on PATH, the operator's
# command, which read_dispatch runs. A script calls it before anything else it reads.
read_require_dispatch() {
  command -v dispatch >/dev/null || read_unknown "no dispatch command is on PATH, so nothing was read"
}

# read_dispatch ARGS... runs `dispatch ARGS...` once under the operator's credential, prints what
# it printed and keeps it in read_out for the script's awk. A run that exits other than 0 is
# read_unknown: an empty or failed read is never a pass.
read_dispatch() {
  local code=0
  read_out=$(dispatch "$@" 2>&1) || code=$?
  [ "$code" -eq 0 ] || read_unknown "dispatch $* exited $code, so nothing was read: $read_out"
  printf '%s\n' "$read_out"
}
