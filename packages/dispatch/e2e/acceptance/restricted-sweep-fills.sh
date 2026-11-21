# shellcheck shell=bash
# shellcheck disable=SC2154,SC2034 # restricted-sweep.sh sources this file: it sets what this reads, and reads what this sets
# The fills file of the restricted sweep (restricted-sweep.sh sources this file, beside it): what
# fills mode writes of the full member's half and a runner or a writer reads. It defines
# functions alone, over the entry script's `work`, `project`, `origin`, `member`, `fills_file` and
# every name `fill_names` lists.

# The format's version: a runner refuses fills of another.
readonly fills_version=2
# The fills a whole file holds, each a variable of the sweep by the same name, which the lookup
# sets: the objects of the project it found, and the values a runner fills its rows with. Fills
# mode writes no file where one is empty, and a runner or a writer refuses fills that lack one.
readonly fill_names='issue issue_title doc doc_slug block pdoc pdoc_slug comment ask message
message_issue handoff handoff_issue handoff_phase near_copy session person proxy_repo priority
broadcast delivery_run going_run ref_prefix dm_session dm_message conversation_session
conversation_slug'

# read_fills: the fills SWEEP_FILLS_FILE names, every one, read before the first request, so fills
# the run cannot read stop it before it reaches the deployment. It sets every name of fill_names,
# fills_member, fills_origin, fills_age and fills_own, and writes the found values, the rows that
# can fail and each row's full member's status to the run's `values`, `can-fail` and
# `full-status`.
read_fills() {
  local first name value lacking made_at='' fills_project=''
  [ -f "$fills_file" ] && [ -r "$fills_file" ] ||
    unknown "SWEEP_FILLS_FILE names no file this run can read: $fills_file"
  [ -s "$fills_file" ] || unknown "SWEEP_FILLS_FILE is empty: $fills_file"
  IFS= read -r first <"$fills_file" || unknown "SWEEP_FILLS_FILE holds no line: $fills_file"
  [ "$first" = "restricted-sweep-fills	$fills_version" ] ||
    unknown "SWEEP_FILLS_FILE holds no fills of this sweep's version $fills_version: its first line is $(printf '%s' "$first" | cut -c1-60)"
  : >"$work/fills-own"
  fills_member='' fills_origin=''
  while IFS='	' read -r name value || [ -n "${name:-}" ]; do
    case $name in
      restricted-sweep-fills) ;;
      project) fills_project=$value ;;
      origin) fills_origin=$value ;;
      made_at) made_at=$value ;;
      member) fills_member=$value ;;
      own) printf '%s\n' "$value" >>"$work/fills-own" ;;
      value) printf '%s\n' "$value" >>"$work/values" ;;
      can_fail) printf '%s\n' "$value" >>"$work/can-fail" ;;
      full_status) printf '%s\n' "$value" >>"$work/full-status" ;;
      *)
        # shellcheck disable=SC2086 # the list is words on purpose
        case " $(printf '%s ' $fill_names)" in
          *" $name "*) printf -v "$name" '%s' "$value" ;;
          *) unknown "SWEEP_FILLS_FILE holds a fill this sweep does not read: $(printf '%s' "$name" | cut -c1-60)" ;;
        esac
        ;;
    esac
  done <"$fills_file"
  [ "$fills_project" = "$project" ] ||
    unknown "SWEEP_FILLS_FILE holds the fills of project ${fills_project:-none}, not of $project"
  is_count "$made_at" || unknown "SWEEP_FILLS_FILE says no time it was made: ${made_at:-none}"
  fills_age=$(($(date -u +%s) - made_at))
  [ "$fills_age" -ge -300 ] || unknown "SWEEP_FILLS_FILE was made $((-fills_age)) s in the future"
  [ "$fills_age" -le 3300 ] ||
    unknown "SWEEP_FILLS_FILE was made $((fills_age / 60)) minutes ago, past 55: its question's run is kept an hour, read only by its asker"
  [ -n "$fills_member" ] || unknown 'SWEEP_FILLS_FILE names no member'
  [ -n "$fills_origin" ] || unknown 'SWEEP_FILLS_FILE names no origin it was made at'
  lacking=''
  for name in $fill_names; do [ -n "${!name}" ] || lacking="$lacking $name"; done
  [ -z "$lacking" ] || unknown "SWEEP_FILLS_FILE lacks fills that whole fills hold:$lacking"
  fills_own=$(sort -u "$work/fills-own")
}

# write_fills: fills mode's end, the full member's half of every row whole in SWEEP_FILLS_FILE:
# the header, every fill of fill_names, the found values, then each row's lines, which the rows
# wrote to the run's `fills` as they were judged. A fill left empty writes no file.
write_fills() {
  local name fill_value own value fills_bytes partial
  mv "$work/fills" "$work/fills-rows" 2>/dev/null || : >"$work/fills-rows"
  : >"$work/fills"
  put restricted-sweep-fills "$fills_version"
  put project "$project"
  put origin "$origin"
  put made_at "$(date -u +%s)"
  put member "$member"
  sort -u "$work/own" >"$work/own-sorted"
  while read -r own; do [ -z "$own" ] || put own "$own"; done <"$work/own-sorted"
  for name in $fill_names; do
    fill_value=${!name}
    [ -n "$fill_value" ] || unknown "the lookup left the fill $name empty, so no fills were written"
    put "$name" "$fill_value"
  done
  sort -u "$work/values" >"$work/values-sorted"
  while IFS= read -r value; do [ -z "$value" ] || put value "$value"; done <"$work/values-sorted"
  cat "$work/fills-rows" >>"$work/fills" || unknown 'could not gather the fills'
  fills_bytes=$(wc -c <"$work/fills" | tr -d ' ')
  [ "$fills_bytes" -le 40960 ] ||
    unknown "the fills hold $fills_bytes bytes, above the 40 KiB one workflow input can carry; no fills were written"
  partial="$fills_file.partial.$$"
  if ! cp "$work/fills" "$partial" || ! mv -f "$partial" "$fills_file"; then
    rm -f "$partial"
    unknown "could not write SWEEP_FILLS_FILE: $fills_file"
  fi
  printf 'FILLS: %s fills of %s, %s bytes, %s rows that can fail, written to %s; a runner may read them for 55 minutes\n' \
    "$(wc -l <"$work/fills" | tr -d ' ')" "$project" "$fills_bytes" "$live" "$fills_file"
}
