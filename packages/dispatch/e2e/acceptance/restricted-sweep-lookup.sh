# shellcheck shell=bash
# shellcheck disable=SC2154,SC2034 # restricted-sweep.sh sources this file: it sets what this reads, and reads what this sets
# The restricted sweep's identities and the full member's lookup (restricted-sweep.sh sources this
# file, beside it). It defines functions alone; they read and set the entry script's fills (every
# name of fill_names), `member` and the run's `values`, `missing`, `own` and `seen-actors`.

# names_member FILE: whether the access list FILE holds names the member, by login, in any case.
names_member() {
  [ -n "$(jq -r --arg m "$member" '.people[]? | select(ascii_downcase == ($m | ascii_downcase))' "$1" 2>/dev/null)" ]
}

# member_projects: the projects the member is listed but SWEEP_PROJECT, into the run's `own`, as
# the member's cookie reads them.
member_projects() {
  call restricted /api/v1/projects "$work/body"
  [ "$status" = 200 ] || unknown "GET /api/v1/projects as the member answered $status: $(head_of "$work/body")"
  jq -r --arg p "$project" '.[]? | .key | select(type == "string" and . != $p)' "$work/body" >"$work/own" 2>/dev/null ||
    unknown "GET /api/v1/projects as the member answered no list: $(head_of "$work/body")"
}

# identities: the member must be a restricted member the server signs in (fills mode, which holds
# no member's cookie, takes SWEEP_MEMBER); the full member's token must see the project, whose
# access list must not name the member. A runner's fills checked the full member's half, and its
# member must be the one they were made for.
identities() {
  if [ "$mode" = fills ]; then
    member=$sweep_member
  else
    call restricted /api/v1/whoami "$work/body"
    [ "$status" = 200 ] || unknown "GET /api/v1/whoami as the member answered $status: $(head_of "$work/body")"
    member=$(first_of "$work/body" 'select(.kind == "user" and .member_kind == "restricted") | .login')
    [ -n "$member" ] || unknown "the cookie signs in no restricted member: GET /api/v1/whoami answered $(head_of "$work/body")"
  fi
  if [ "$mode" = runner ]; then
    [ "$(printf '%s' "$member" | tr '[:upper:]' '[:lower:]')" = "$(printf '%s' "$fills_member" | tr '[:upper:]' '[:lower:]')" ] ||
      unknown "the cookie signs in $member, and SWEEP_FILLS_FILE's fills are of $fills_member"
  else
    call full /api/v1/whoami "$work/body"
    [ "$status" = 200 ] || unknown "GET /api/v1/whoami with SWEEP_FULL_TOKEN answered $status: $(head_of "$work/body")"
    call full /api/v1/projects "$work/body"
    [ "$status" = 200 ] || unknown "GET /api/v1/projects with SWEEP_FULL_TOKEN answered $status: $(head_of "$work/body")"
    [ -n "$(first_of "$work/body" ".[] | select(.key == \"$project\") | .key")" ] ||
      unknown "SWEEP_FULL_TOKEN does not see $project: GET /api/v1/projects lists no such project"
    jq -r --arg p "$project" '.[]? | .key | select(type == "string" and . != $p)' "$work/body" >"$work/full-projects" 2>/dev/null
    call full "/api/v1/projects/$project/access" "$work/body"
    [ "$status" = 200 ] || unknown "GET /api/v1/projects/$project/access with SWEEP_FULL_TOKEN answered $status: $(head_of "$work/body")"
    ! names_member "$work/body" ||
      unknown "$project's access list names $member, so the member may read it and this sweep proves nothing"
  fi
  printf 'member  %s, a restricted member %s'"'"'s access list does not name\n' "$member" "$project"
}

# runner_projects: a runner's own projects, which the document socket's control reads too, must
# be the ones the fills were made for: the fills told the project's sessions, people and
# repositories from those of the member's own projects by them. A project the member sees that
# the fills did not know leaves the run UNKNOWN; the project itself among them is judged by the
# routes.
runner_projects() {
  local member_own
  member_projects
  member_own=$(sort -u "$work/own")
  [ "$member_own" = "$fills_own" ] ||
    unproven "the member is listed the projects $(printf '%s' "$member_own" | tr '\n' ' '), but SWEEP_FILLS_FILE was made for $(printf '%s' "$fills_own" | tr '\n' ' ')"
  printf 'fills   of %s for %s, made at %s %s s ago: %s values, %s rows that can fail\n' "$project" "$member" \
    "$fills_origin" "$fills_age" "$(wc -l <"$work/values" | tr -d ' ')" "$(wc -l <"$work/can-fail" | tr -d ' ')"
}

# found LABEL VALUE: a project object the lookup found, which marks a row of the project.
found() {
  printf 'found   %s %s\n' "$1" "$2"
  printf '%s\n' "$2" >>"$work/values"
}

# need LABEL VALUE WHERE: a kind the project owns, found or missing.
need() {
  if [ -n "$2" ]; then found "$1" "$2"; else missing_kind "$1" "$3"; fi
}

# actors_in FILE KINDS: every actor of one of KINDS (a jq array) FILE's objects name, one per line.
actors_in() {
  jq -r --argjson kinds "$2" \
    '.. | objects | select((.kind as $k | any($kinds[]; . == $k)) and (.id | type == "string")) | .id' \
    "$1" 2>/dev/null
}
# authors FILE FILTER: the first person and the first session that FILTER's authors name, and
# every session and person FILE names, into `actors`, its sessions also into `sessions`.
authors() {
  [ -n "$person" ] || person=$(first_of "$1" "$2 | select(.kind == \"user\") | .id")
  [ -n "$session" ] || session=$(first_of "$1" "$2 | select(.kind == \"session\") | .id")
  actors_in "$1" '["user", "session"]' >>"$work/actors"
  actors_in "$1" '["session"]' >>"$work/sessions"
}
# seen_in FILE: every session and person FILE names by kind, and every login it holds.
seen_in() {
  jq -r '(.. | objects | select((.kind == "user" or .kind == "session") and (.id | type == "string")) | .id),
    (.. | strings | select(test("^[^@[:space:]]+@[^@[:space:]]+$")))' "$1" 2>/dev/null
}

# lookup: as the full member, an object of each kind the routes name. A kind the project owns and
# the lookup did not find is written to `missing`, which leaves the run UNKNOWN.
lookup() {
  local key spec spec_slug first_block scanned=0 ref ref_issue other own candidate id
  # A page of the project's issues: given a limit, the list answers {issues: [...]}.
  call full "/api/v1/issues?project=$project&limit=50" "$work/body"
  [ "$status" = 200 ] || unknown "GET /api/v1/issues?project=$project with SWEEP_FULL_TOKEN answered $status: $(head_of "$work/body")"
  jq -c 'if type == "object" then .issues else . end' "$work/body" >"$work/issues" 2>/dev/null
  jq -r '.[]? | .key' "$work/issues" 2>/dev/null >"$work/keys"
  [ -s "$work/keys" ] || unknown "the full member finds no issue of $project, so no route can be filled with its objects"
  : >"$work/actors"
  : >"$work/sessions"
  # Every issue of the page, until an object of each kind is found: the page holds 50.
  while read -r key; do
    scanned=$((scanned + 1))
    if [ -z "$block" ]; then
      call full "/api/v1/issues/$key/summary" "$work/body"
      if [ "$status" = 200 ]; then
        spec=$(first_of "$work/body" '.documents[]? | select(.primary) | .id')
        spec_slug=$(first_of "$work/body" '.documents[]? | select(.primary) | .slug')
        if [ -n "$spec" ] && [ -n "$spec_slug" ]; then
          call full "/api/v1/artifacts/$(uri "$spec")/blocks" "$work/body"
          first_block=$(first_of "$work/body" '.[]? | .id')
          if [ "$status" = 200 ] && [ -n "$first_block" ]; then
            issue=$key doc=$spec doc_slug=$spec_slug block=$first_block
            issue_title=$(jq -r --arg k "$key" 'first(.[]? | select(.key == $k) | .title) // empty' "$work/issues")
          fi
        fi
      fi
    fi
    if [ -z "$comment" ]; then
      call full "/api/v1/issues/$key/comments" "$work/body"
      [ "$status" != 200 ] || { comment=$(first_of "$work/body" '.[]? | .id') && authors "$work/body" '.[]? | .author'; }
    fi
    if [ -z "$ask" ]; then
      call full "/api/v1/issues/$key/asks" "$work/body"
      [ "$status" != 200 ] || { ask=$(first_of "$work/body" '.[]? | .id') && authors "$work/body" '.[]? | .author'; }
    fi
    if [ -z "$message" ]; then
      call full "/api/v1/issues/$key/messages" "$work/body"
      if [ "$status" = 200 ]; then
        message=$(first_of "$work/body" '.[]? | .message.id')
        [ -z "$message" ] || message_issue=$key
        authors "$work/body" '.[]? | .message.author'
      fi
    fi
    if [ -z "$handoff" ]; then
      call full "/api/v1/issues/$key/handoffs" "$work/body"
      if [ "$status" = 200 ]; then
        handoff=$(first_of "$work/body" '.[]? | .id')
        handoff_phase=$(first_of "$work/body" '.[]? | .phase')
        [ -z "$handoff" ] || handoff_issue=$key
        authors "$work/body" '.[]? | .author'
      fi
    fi
    [ -z "$block" ] || [ -z "$comment" ] || [ -z "$ask" ] || [ -z "$message" ] || [ -z "$handoff" ] ||
      [ -z "$person" ] || [ -z "$session" ] || break
  done <"$work/keys"
  call full "/api/v1/projects/$project/artifacts" "$work/body"
  if [ "$status" = 200 ]; then
    pdoc=$(first_of "$work/body" '.[]? | select(.issue_key == null) | .id')
    pdoc_slug=$(first_of "$work/body" '.[]? | select(.issue_key == null) | .slug')
  fi
  call full "/api/v1/near-copy-checks?project=$project" "$work/body"
  [ "$status" != 200 ] || near_copy=$(first_of "$work/body" '.checks[]? | .id')
  # The kinds no project owns, looked up where a route lists them.
  call full /api/v1/priorities "$work/body"
  [ "$status" != 200 ] || priority=$(first_of "$work/body" '.priorities[]? | .id')
  call full /api/v1/broadcasts "$work/body"
  [ "$status" != 200 ] || broadcast=$(first_of "$work/body" '.[]? | .id')
  call full /api/v1/delivery/timeline "$work/body"
  [ "$status" != 200 ] || delivery_run=$(first_of "$work/body" '.runs[]? | .id | tostring')
  # How a reference to an object is spelled, as the server itself writes one: a comment's node
  # names its ref, which starts with the comment's issue key. Never spelled here, so a change of
  # spelling reaches the sweep from the server.
  if [ -n "$comment" ]; then
    call full "/api/v1/comments/$(uri "$comment")/blocks" "$work/body"
    if [ "$status" = 200 ]; then
      ref=$(first_of "$work/body" '.item.ref | select(type == "string")')
      ref_issue=$(first_of "$work/body" '.item.issue_key | select(type == "string")')
      case $ref in ?*"$ref_issue"/*) [ -z "$ref_issue" ] || ref_prefix=${ref%%"$ref_issue"/*} ;; esac
    fi
  fi
  # A question's run belongs to the credential that asked it, and no route lists one, so the full
  # member asks one of the project: the sweep's first write.
  jq -cn --arg p "$project" \
    '{words: "restricted-sweep: which issues does this project hold?", project: $p}' >"$work/question.json" ||
    unknown 'could not write the question'
  call full /api/v1/going/requests "$work/body" "$work/question.json"
  [ "$status" != 201 ] || going_run=$(first_of "$work/body" '.id | select(type == "string")')
  [ -z "$going_run" ] || found going-run "$going_run"
  [ -n "$going_run" ] || going_why="POST /api/v1/going/requests answered $status: $(head_of "$work/body")"

  need issue "$issue" "no issue of the first $scanned holds a spec with a block"
  need document "$doc" "no issue of the first $scanned holds a spec with a block"
  need block "$block" "no issue of the first $scanned holds a spec with a block"
  need comment "$comment" "no issue of the first $scanned holds a comment"
  need ask "$ask" "no issue of the first $scanned holds an ask"
  need message "$message" "no issue of the first $scanned holds a message"
  need handoff "$handoff" "no issue of the first $scanned holds a handoff"
  need project-document "$pdoc" "GET /api/v1/projects/$project/artifacts lists no document of the project itself"
  need near-copy-check "$near_copy" "GET /api/v1/near-copy-checks?project=$project lists none"
  # A person and a session belong to no project, but the routes that name them are filled with
  # ones that worked in the project, which every project of the deployment has.
  [ -n "$person" ] || printf 'person: no comment, ask, message or handoff of the first %s issues names a person\n' "$scanned" >>"$work/missing"
  [ -n "$session" ] || printf 'session: no comment, ask, message or handoff of the first %s issues names a session\n' "$scanned" >>"$work/missing"
  printf 'found   person %s, session %s, priority %s, broadcast %s, delivery run %s\n' \
    "${person:-none}" "${session:-none}" "${priority:-none}" "${broadcast:-none}" "${delivery_run:-none}"
  # The member's own projects, which the document socket's control reads too. Fills mode, with no
  # member's cookie, reads them as the projects whose access lists name the member, which is all a
  # restricted member is listed; a runner checks them against the projects it is listed.
  if [ "$mode" = fills ]; then
    : >"$work/own"
    while read -r other; do
      call full "/api/v1/projects/$(uri "$other")/access" "$work/body"
      if [ "$status" != 200 ]; then
        printf 'own projects: the full member is answered %s for the access list of %s, so the sweep cannot tell whether it names %s\n' \
          "$status" "$other" "$member" >>"$work/missing"
        continue
      fi
      ! names_member "$work/body" || printf '%s\n' "$other" >>"$work/own"
    done <"$work/full-projects"
  else
    member_projects
  fi
  # The sessions and people the member may see: their own login, and every session and person the
  # full member finds in a project the member sees, by kind or as a login anywhere in its answers.
  # A session or a person the lookup read in the project that is none of these is a row of the
  # project: a list of sessions or people that names one gave the member what the project holds.
  printf '%s\n' "$member" >"$work/seen-actors" || unknown 'could not record the member'
  : >"$work/own-repos"
  while read -r own; do
    call full "/api/v1/projects/$own/access" "$work/body"
    [ "$status" != 200 ] || seen_in "$work/body" >>"$work/seen-actors"
    call full "/api/v1/issues?project=$own&limit=50" "$work/own-issues"
    if [ "$status" != 200 ]; then
      printf 'actor: the full member is answered %s for the issues of %s, a project the member sees, so the sweep cannot tell its sessions, people and repositories from %s'"'"'s\n' \
        "$status" "$own" "$project" >>"$work/missing"
      continue
    fi
    seen_in "$work/own-issues" >>"$work/seen-actors"
    jq -r 'if type == "object" then .issues else . end | .[]? | (.repo, .resolved_repo) | select(type == "string")' \
      "$work/own-issues" >>"$work/own-repos" 2>/dev/null
    jq -r 'if type == "object" then .issues else . end | .[]? | .key' "$work/own-issues" >"$work/own-keys" 2>/dev/null
    while read -r key; do
      for part in '' /comments /asks /messages /handoffs; do
        call full "/api/v1/issues/$key$part" "$work/body"
        [ "$status" != 200 ] || seen_in "$work/body" >>"$work/seen-actors"
      done
    done <"$work/own-keys"
  done <"$work/own"
  sort -u "$work/actors" | grep -v -x -F -f "$work/seen-actors" >"$work/project-actors"
  if [ -s "$work/project-actors" ]; then
    while read -r id; do found actor "$id"; done <"$work/project-actors"
  else
    missing_kind actor "every session and person the lookup read in $project also works in a project the member sees, so no list of sessions or people can fail"
  fi
  # A direct message to a session that worked in the project alone, which the full member reads:
  # the member must read it by no route, its session's list of messages included, and the live
  # check's writes send that session another.
  sort -u "$work/sessions" | grep -x -F -f "$work/project-actors" >"$work/project-sessions"
  while read -r candidate; do
    call full "/api/v1/agents/$(uri "$candidate")/messages" "$work/body"
    [ "$status" = 200 ] || continue
    jq -r '.[]? | .message.id | select(type == "string")' "$work/body" >"$work/direct-messages" 2>/dev/null
    [ -s "$work/direct-messages" ] || continue
    dm_session=$candidate dm_message=$(head -n 1 "$work/direct-messages")
    while read -r id; do found direct-message "$id"; done <"$work/direct-messages"
    break
  done <"$work/project-sessions"
  [ -n "$dm_message" ] ||
    missing_kind direct-message "no session the lookup read in $project alone has a direct message the full member reads"
  # A repository only the project's issues resolve to, which every row that names a repository is
  # filled with: a repository a project the member sees resolves to as well is one the member may
  # be answered for.
  proxy_repo=$(jq -r '.[]? | (.repo, .resolved_repo) | select(type == "string" and test("^[^/]+/[^/]+$"))' "$work/issues" 2>/dev/null |
    grep -v -x -F -f "$work/own-repos" -e '' | head -n 1)
  if [ -n "$proxy_repo" ]; then
    found proxy-repository "$proxy_repo"
  else
    missing_kind proxy-repository "every repository $project's issues resolve to is one a project the member sees resolves to as well"
  fi
  # The conversation file must be one the full member reads, of a session the member does not see;
  # any other leaves its two rows unfilled, with the reason.
  if [ -n "$conversation_session" ]; then
    if grep -q -x -F -e "$conversation_session" "$work/seen-actors"; then
      conversation_why="SWEEP_CONVERSATION_FILE's session $conversation_session works in a project the member sees"
      conversation_session='' conversation_slug=''
    else
      call full "/api/v1/agents/$(uri "$conversation_session")/artifacts/$(uri "$conversation_slug")" "$work/body"
      if [ "$status" = 200 ]; then
        found conversation-file "$conversation_file"
      else
        conversation_why="the full member is answered $status for SWEEP_CONVERSATION_FILE's $conversation_file"
        conversation_session='' conversation_slug=''
      fi
    fi
  fi
  [ -z "$issue_title" ] || [ "${#issue_title}" -lt 8 ] || printf '%s\n' "$issue_title" >>"$work/values"
}
