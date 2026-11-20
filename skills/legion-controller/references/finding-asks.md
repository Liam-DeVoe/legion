# Finding asks

Read this when a clear defect or a finding ask's reference reaches you, when an ask you follow is answered, when you file or link an issue, when a session tells you of an issue under a root on the backlog, and at every start. What a finding ask is: `skill://legion-worker/references/findings.md`.

## The `Filed for` line

Every issue you file carries one line in its spec, which triage reads to tell your filing from another's label:

- for a person: `Filed for <person> at their request: <request>`. The request is `dispatch://<REPORT KEY>/ask/<id>` for who answered it; `Slack <channel_id> message <ts>` for the mention's `<@user_id>` (`…, adopting message <earlier ts>` when the mention asks for an earlier message's request); or `direct message <id>`, the id its frame's `reply_with` names after `--in-reply-to` (for a message that came as your own user turn, `direct message to session <your session id>`).
- for a clear defect no running tree owns: `Filed for Legion from <role> on <issue>: <evidence reference>`.

## Hand over a clear defect

A role sends a clear defect with its role token, the defect, its issue, where the fix belongs, the fix, and its evidence comment on the report issue, whose first line is `Clear defect from <role>`. When the message carries the evidence itself, create or reopen the report issue (`skill://legion-controller/references/daily-report.md`) and post it there with that first line. One you find yourself gets the same comment, with `controller` as the role. A clear-defect ask needs no answer: take it as below, and resolve it (`dispatch resolve-ask --kind resolved`) only once it carries your reply.

1. Check it is clear: the fix needs no choice a person must make, in a repository the project maps (`GET $DISPATCH_URL/api/v1/settings/repo-projects`). Otherwise open a decision ask as `findings.md` says, and reply `Moved dispatch://<REPORT KEY>/ask/<new id>`.
2. Search first (`skill://dispatch-first`). When an open issue tracks it, comment the evidence there and reply `Filed dispatch://<that KEY>`. When that issue belongs to an approved root and has no `child_of` link to it, link it ([An issue's approved root](#an-issues-approved-root)).
3. When a root in `admission.trees` matches its repository and component, or its approved class or unfinished work holds the fix, `envoy_publish` the defect, its evidence reference and the fix to `notifications.role.<issues.<KEY>.architect.locator.claim>`. Reply `Handed dispatch://<that KEY>` only when a live holder took the publish; when Envoy kept it, or the root has no locator, reply nothing and publish again at your next wake. File nothing while a tree owns the code.
4. When no running tree owns it, file it where the fix belongs, under its approved root with `--parent <ROOT>` and no label where [An issue's approved root](#an-issues-approved-root) puts it there, else:
   `dispatch issue --project <KEY> --repo <owner/name> --title '<the defect>' --label legion --spec-file -`
   The spec states the defect, where the fix belongs and the fix, and ends with the `Filed for Legion from` line.
5. Reply under the evidence comment (`--reply-to '<comment id>'`) or the ask (`--reply-to-ask '<ask id>'`), its first line exactly `Filed dispatch://<NEW KEY>` or `Handed dispatch://<KEY>`.

## An issue's approved root

Before you file an issue or give one to a tree, find its approved root: a class root whose class holds it, or the root of a running tree whose unfinished work holds it. A mention of a class in prose is no link: only a Dispatch `child_of` link (`--parent`) is one, and a root counts only once a person approved its spec.

- The root's tree runs: give the issue to the tree (step 3 above).
- It is a Legion root whose tree does not run: file the issue with `--parent <ROOT>` and no `legion` label.
- A fix of what a `done` issue delivered is new work: file it with no parent, and it asks for its own approval.
- An issue that belongs under any other unfinished issue is filed as a root of its own with no parent, and it asks for its own approval.
- Link an issue already filed that belongs to an approved root's class or unfinished work: `dispatch issue-update --issue <KEY> --parent <ROOT>`. After linking a root of its own, `envoy_publish` to its architect's role topic the ancestor's key and that "A root under an approved ancestor" in `skill://legion-architect/references/design-gate.md` now applies to it.
- Where you report a link, and in your message to the linked issue's architect, name all three parts of the link's cost (an open child holds the parent's first phase, holds each later phase start, and stops a phase under way at the next admission pass), and say that a root whose branch carries code puts that code in a child.

A class root that must take new members is a Legion root with no code of its own, never an issue that holds only a design. File it with `--label legion` and a spec line that says it has no code of its own, also when a request asks for a design-only issue (say why); if the request says to start nothing yet, move it to `backlog` at once.

### A member under a root on the backlog

A member filed under a root on the backlog moves nothing: the session that files it tells you the member and the root, even when the person who moved the root works with that session. A root's architect also tells you when its root rests on an ancestor on the backlog. Decide whether that root runs under "Free slots" (`skill://legion-controller/SKILL.md#free-slots`): with no request naming it, follow the deployment's instructions, leave it on the backlog where they let no session ask, and otherwise ask a person. That ask is one `dispatch ask` per root on the report issue, with the options `Run it now` and `Leave it on the backlog` and a recommendation, followed; a later member goes under the open ask.

On `Run it now`, set the root to `todo` and cite the answer in a comment on it; on `Leave it on the backlog`, move nothing; then reply `Started dispatch://<ROOT>` or `Left on the backlog dispatch://<ROOT>`. Until the root runs, its member waits; a fix that must run now is filed with no parent, asks for its own approval, and its spec names the class root and why it is not linked.

## Follow it

A finding ask another role opened reaches you as `dispatch://<KEY>/ask/<id>`. Follow it so that its answer reaches you: `dispatch follow --ask 'dispatch://<KEY>/ask/<id>' --action follow`. When `dispatch read --ref` shows it answered already, act on its answer now.

## Act on its answer

A park ask's answer goes to `skill://legion-controller/references/park-asks.md` step 4, a deferral ask's to `skill://legion-controller/references/backlog-stewardship.md#a-root-the-plan-defers` step 4, and a run ask's to [A member under a root on the backlog](#a-member-under-a-root-on-the-backlog). For any other, `dispatch read --ref` it and act on the newest answer; when its typed text asks for another of these four than the option selected, do what the text asks, and when it asks for none of them, reply with your question and file nothing:

- `Hand it to the owning tree`: [Hand over a clear defect](#hand-over-a-clear-defect) from step 3.
- `Do not file`: file nothing.
- `File an issue`: `dispatch issue --project <PROJECT> --title '<the problem>' --spec-file -` in the project where the fix belongs (this one when the answer names none). The spec states the problem and where the fix belongs, cites the ask and its evidence, and carries `Filed for <the answer's By:> at their request: dispatch://<REPORT KEY>/ask/<id>`.
- `File it for Legion`: the same, with `--label legion` (under its approved root, `--parent <ROOT>` and no label); its triage wake brings it back.

An ask that re-files an escalated child also takes the steps of `skill://legion-controller/references/escalation.md#a-child-that-looks-independent`.

Then reply under the ask, first line exactly `Filed dispatch://<NEW KEY>`, `Handed dispatch://<KEY>` or `Not filed: <reason>`: `dispatch comment --issue <the ask's issue> --reply-to-ask '<ask id>' --body-file -`. Dispatch cannot resolve an answered ask, so that reply is its record. Close an open finding ask that turned moot with `dispatch resolve-ask --kind resolved`, naming why.

## Catch up at every start

An answer that arrives while no controller runs never wakes you, a reference published then reaches nobody, and a new session follows none of the asks an earlier one followed. So at every start:

1. `dispatch read` the report issue (none yet: nothing to catch up). Reopen and assign it as `skill://legion-controller/references/daily-report.md` "Keep it answerable" says.
2. `dispatch open-asks --project <PROJECT>` lists every open ask; an ask on a closed issue is not listed. Follow those on the report issue, and take each whose question opens `Clear defect from ` as above. On another issue, a question opening `Finding from ` or `Clear defect from ` is a role's own-issue ask: take a clear defect the same way; move a finding to the report issue with the same question, options and `--ref`, reply `Moved dispatch://<REPORT KEY>/ask/<new id>` under the old one, and resolve it.
3. List the answered asks still waiting on you, for the report issue and then each issue of one snapshot of `dispatch issues --project <PROJECT> --status needs_review --all` (never paged):

   ```text
   asks=$(printf 'Authorization: Bearer %s' "$(< "$DISPATCH_TOKEN_FILE")" | curl -sS --fail-with-body -H @- "$DISPATCH_URL/api/v1/issues/<ISSUE KEY>/asks?state=answered&thread=1") || { echo "Dispatch read failed" >&2; exit 1; }
   printf '%s' "$asks" | jq -cn 'input | if type == "array" then [.[] | select(any(.options[]; .label == "Do not file" or .label == "Admit it" or .label == "Park it" or .label == "Leave it admitted" or .label == "Defer it" or .label == "Let it run" or .label == "Run it now" or .label == "Leave it on the backlog")) | .answer.at as $t | select([.thread.replies[]? | select(.author.kind == "session" and .created_at > $t and (.body | test("^(Filed dispatch://|Handed dispatch://|Admitted dispatch://|Not filed:|Not admitted:|Parked dispatch://|Not parked dispatch://|Left admitted dispatch://|Deferred dispatch://|Not deferred dispatch://|Let run dispatch://|Started dispatch://|Left on the backlog dispatch://)")))] | length == 0) | {id, question, answer}] else error("Dispatch did not answer the list") end'
   ```

   Exit 0 with `[]` is nothing waiting; anything else is a failed read. Act on each as above, a triage ask as `skill://legion-controller/references/triage.md` says.
4. List the clear defects nobody took yet:

   ```text
   notes=$(printf 'Authorization: Bearer %s' "$(< "$DISPATCH_TOKEN_FILE")" | curl -sS --fail-with-body -H @- "$DISPATCH_URL/api/v1/issues/<REPORT KEY>/comments") || { echo "Dispatch read failed" >&2; exit 1; }
   printf '%s' "$notes" | jq -cn 'input | if type == "array" then (. as $all | [.[] | select(.reply_to == null and (.body | startswith("Clear defect from "))) | .id as $id | select([$all[] | select(.reply_to == $id and .author.kind == "session" and (.body | test("^(Handed dispatch://|Filed dispatch://|Not filed:|Asked dispatch://)")))] | length == 0) | {id, body}]) else error("Dispatch did not answer the list") end'
   ```

   Take each as [Hand over a clear defect](#hand-over-a-clear-defect) says; reply `Not filed: <reason>` under one that is not clear, and open an ask for it instead.
