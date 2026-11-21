# shellcheck shell=bash
# lib/subreaper.sh, sourced as a stage proof's first command after its `set` line, with no
# arguments, so "$@" is still the proof's own: it runs the proof again in its own process under
# lib/subreaper.py, with the same pid, arguments and xtrace, so the proof's shell is a child
# subreaper and every process the run starts stays below it for its whole life. The run's walk
# (procs_naming, lib/process-tree.sh) then judges an environment it cannot read by run_descendant,
# which a process another session left under pid 1 during the run does not fail. subreaper.py names
# the shell in LEGION_E2E_SUBREAPER, as `PID START` (its pid and its start, field 22 of its stat),
# or `PID START refused` where the mark could not be set; either way this shell runs on as it is
# the second time. A value of another pid, or of this pid with another start (a pid reused since a
# run that set it), is no subreaper's: the proof is run again. A box whose python3 does not answer
# (none, or a test's stand-in for it) runs the proof on as it is, and the walk judges by
# unread_may_carry.
subreaper_stat=
{ IFS= read -r -d '' subreaper_stat <"/proc/$$/stat"; } 2>/dev/null || :
read -r -a subreaper_fields <<<"${subreaper_stat##*) }"
case ${LEGION_E2E_SUBREAPER:-} in
  "$$ ${subreaper_fields[19]:-}" | "$$ ${subreaper_fields[19]:-} refused") ;;
  *)
    if [ -f "$0" ] && [ "$(python3 -I -c 'print("ok")' 2>/dev/null)" = ok ]; then
      exec python3 -I "${BASH_SOURCE[0]%.sh}.py" "$BASH" "-${-//[^x]/}" "$0" "$@"
    fi
    ;;
esac
unset subreaper_stat subreaper_fields
