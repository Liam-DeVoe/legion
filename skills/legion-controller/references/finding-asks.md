# Finding asks

Read this when a clear defect no tree owns, or a finding ask's reference, reaches you on your
role, when one you follow is answered, when you file an issue a person asked for, and at every
start. What a finding ask is, where it goes, and how a role writes a clear defect to the tree that
owns it is `skill://legion-worker/references/findings.md`; what you may file is the skill's
"Findings, and the issues you file" section.

## The `Filed for` line

Every issue you file for a person carries one line in its spec, `Filed for <person> at their
request: <request>`, because triage reads it to tell your filing from anyone else's label
(`skill://legion-controller/references/triage.md`). The request, and the person it names, are:

- an answer to an ask: `dispatch://<REPORT KEY>/ask/<id>`, for the person who answered it;
- a Slack request: `Slack <channel_id> message <ts>`, for the mention's `<@user_id>`. When the
  mention adopts an earlier message's request by asking for it
  (`skill://legion-controller/references/slack-mentions.md`), the request reads
  `Slack <channel_id> message <mention ts>, adopting message <earlier ts>`, and the person it names
  is still the mention's `<@user_id>`;
- a direct message: `direct message <id>`, with the id its frame's `reply_with` names after
  `--in-reply-to`, for the frame's `actor`. A message that arrives as your own user turn shows no
  id: write `direct message to session <your session id>`, for the person by the name they gave,
  else `the person who messaged this session`.

For a clear defect no running tree owns, the line is `Filed for Legion from <role> on <issue>:
<evidence>` ([File a clear defect no tree owns](#file-a-clear-defect-no-tree-owns)).

## File a clear defect no tree owns

A role writes a clear defect to you only when it found no tree that owns its code: a comment
that mentions your role, on the report issue or, when the project had none, on the role's own
issue, whose first line is `Clear defect from <role>`. It holds the defect, the issue it came
from, where the fix belongs and the fix. A clear defect you find yourself goes to its owner as
`findings.md` says ("Write to the owner"), and you forward none: a defect a tree owns goes to
that tree from the role that found it. When no tree owns yours, post its evidence comment on the
report issue, with `controller` as the role, and take it as below.

You file an issue only when nobody owns that code.

1. Check that it is clear: the fix needs no choice a person must make, and it belongs in a
   repository both role GitHub Apps are installed on, the only repositories Legion records an
   issue on (your addressing line names the owners your own App is installed on, and
   `read_state`'s `repositories.refused` names a repository either App is missing from).
   Otherwise reply `Not filed: <reason>` under the evidence, the reason naming the choice a
   person must make or the repository the Apps miss, and open nothing: a finder that reads that
   reply opens its ask itself (`skill://legion-worker/references/findings.md#write-to-the-owner`).
2. Search first (`skill://dispatch-first`). When an open issue tracks it, read whether a running
   tree owns that issue before you post anything, as step 3 reads the live trees: its record's
   `tree` field (`read_state` with `issues`) names its root, and an issue with no record stands
   in no tree. When that root's tree is live and its root records an architect claim, send
   nothing on and file nothing: reply
   `Not filed: tracked by dispatch://<KEY>; write to role:<that tree's architect's token>` under
   the evidence, with the token `issues.<ROOT>.architect.locator.claim` names, so the role writes
   there itself. Only where no running tree owns it, or its root records no architect claim, so
   nobody in the tree can read the write, is the evidence yours to take in: comment it on that
   issue, reply `Filed dispatch://<KEY>` under the evidence, and file nothing.
3. Check that no running tree owns that code: no live tree whose repository and component match.
   Find the live trees as `skill://legion-controller/references/admission.md` says: each
   recorded root is a record whose `tree` field is its own key, and its row there says when its
   tree is live. Read the roots with `read_state` and `issues`, and each candidate with
   `dispatch read`. `admission.active` does not say it: it holds the issues the daemon has
   started, and a tree runs whether or not any issue of it holds a slot
   ([Admission and the `legion` label](https://cybermonklabs.github.io/legion/legion/concepts/#admission-and-the-legion-label)).
   When one matches, send nothing on and file nothing:
   reply `Not filed: dispatch://<ROOT> owns it; write to role:<its architect's token>` under the
   evidence, with the token `issues.<ROOT>.architect.locator.claim` names, so the role writes to
   that tree itself. A second issue would split the work.
4. No running tree owns that code: file it on the repository where the fix belongs, in the
   project that repository maps to, or in your own project when Dispatch maps it to none:

   ```text
   dispatch issue --project <KEY> --repo <owner/name> --title '<the defect, as a title>' --label legion --spec-file -
   ```

   Without `--repo`, Dispatch records the project's default repository, and the daemon admits the
   issue there. The spec states the defect, where the fix belongs and the fix, and ends with the
   `Filed for Legion from` line: the sending role's token (or `controller`), the `dispatch://<KEY>`
   it came from, and the evidence comment's reference. It opens in `triage` with the
   label, and triage admits it (`skill://legion-controller/references/triage.md`).
5. Reply under the evidence comment (`--reply-to '<comment id>'`), its first line exactly
   `Filed dispatch://<NEW KEY>`. A later session reads that reply at triage and at every start.

## Follow it

A finding ask another role opened reaches you as its reference, `dispatch://<KEY>/ask/<id>`, on
the controller topic. Follow it, so that its answer reaches you:

```text
dispatch follow --ask 'dispatch://<KEY>/ask/<id>' --action follow
```

When `dispatch read --ref` shows it answered already, act on its answer now.

## Act on its answer

A park ask (`Park it`, `Leave it admitted`) and a deferral ask (`Defer it`, `Let it run`) take
none of the three finding actions below. Act on the answer to a park ask as
`skill://legion-controller/references/wakes.md#ask-whether-to-park-a-tree` step 4 says, and on
the answer to a deferral ask as
`skill://legion-controller/references/backlog-stewardship.md#a-root-the-plan-defers` step 4 says.

Once it is answered (an ask you follow brings its answer to you), read it with
`dispatch read --ref 'dispatch://<KEY>/ask/<id>'` and act on the newest answer. When its typed
text asks for a different one of the three actions than the option selected, do what the text
asks; when the text asks for something that is none of the three, reply under the ask with your
question and file nothing:

- `Do not file`: file nothing.
- `File an issue`: `dispatch issue --project <PROJECT> --title '<the problem, as a title>'
  --spec-file -` in the project where the fix belongs, as the answer or the ask names it; when
  neither names one, in this project. The spec
  states the problem and where the fix belongs, cites the ask and its evidence by their
  `dispatch://` references, and carries the line `Filed for <the answer's By:> at their request:
  dispatch://<REPORT KEY>/ask/<id>` (above). No `legion` label.
- `File it for Legion`: the same, with `--label legion`. It opens in `triage` with the label, so
  its triage wake brings it back to you (`skill://legion-controller/references/triage.md`).

An ask that re-files an escalated child also takes the steps of
`skill://legion-controller/references/escalation.md`.

Then reply under the ask with what you did, its first line exactly `Filed dispatch://<NEW KEY>`
or `Not filed: <reason>`. The ask's issue is the report issue, or the role's own issue for an ask
it opened there while the report issue was missing or closed:

```text
dispatch comment --issue <the ask's issue> --reply-to-ask '<ask id>' --body-file -
```

Dispatch refuses to resolve an answered ask, so that reply is its record. An open finding ask that
turns moot (an issue now tracks the problem, or the problem is gone) you close with
`dispatch resolve-ask --kind resolved`, naming why.

## Catch up at every start

An answer that arrives while no controller runs never wakes you, a reference published then
reaches nobody, and a new session follows none of the asks an earlier one followed. So at every
start, catch up on the report issue (with none yet, there is nothing to catch up), and on the
finding asks roles opened on their own issues while the report issue was missing or closed:

1. Read it with `dispatch read`. Reopen it when its `Status:` is `done`, and assign it when its
   `Assignee:` says `unassigned` and a person is known, both as
   `skill://legion-controller/references/daily-report.md` "Keep it answerable" says.
2. Take each open ask: `dispatch open-asks --project <PROJECT>` lists every unanswered ask of the
   project, with no cap, each line naming its issue. It leaves out an ask on a closed issue
   (`asks.go`'s `listOpenAsks` takes `i.closed_at is null`), which is why an architect holds its
   issue open for one. Follow those on the report issue. One on another issue whose question opens
   `Finding from ` is a role's own-issue ask: ask it again on the report issue with the same
   question, options and `--ref`, then reply `Moved dispatch://<REPORT KEY>/ask/<new id>` under the
   old ask. Resolve the old ask once it carries a `Moved` or `Filed` reply.
3. List the answered asks still waiting on you, which no `dispatch` command lists, in bash:

   ```text
   asks=$(curl -sS --fail-with-body -H @<(printf 'Authorization: Bearer %s' "$(< "$DISPATCH_TOKEN_FILE")") "$DISPATCH_URL/api/v1/issues/<ISSUE KEY>/asks?state=answered&thread=1") || { echo "Dispatch read failed" >&2; exit 1; }
   printf '%s' "$asks" | jq -cn 'input | if type == "array" then [.[] | select(any(.options[]; .label == "Do not file" or .label == "Admit it" or .label == "Park it" or .label == "Leave it admitted" or .label == "Defer it" or .label == "Let it run")) | .answer.at as $t | select([.thread.replies[]? | select(.author.kind == "session" and .created_at > $t and (.body | test("^(Filed dispatch://|Handed dispatch://|Admitted dispatch://|Not filed:|Not admitted:|Parked dispatch://|Not parked dispatch://|Left admitted dispatch://|Deferred dispatch://|Not deferred dispatch://|Let run dispatch://)")))] | length == 0) | {id, question, answer}] else error("Dispatch did not answer the list") end'
   ```

   Read it for the report issue, then for each issue in `needs_review`, whose architect holds its
   acceptance on a finding ask's answer. Act on each as above, a triage ask as
   `skill://legion-controller/references/triage.md` says. The read lands in `asks` first, because a
   pipeline reports only the status of `jq`. `jq -n 'input …'` fails on an empty body, where a
   plain `jq` would succeed on it, so exit 0 means the list was read: `[]` is nothing waiting.
   Anything else is a failed read and admits nothing.

   Take the `needs_review` issues as one snapshot before you act on any of them:
   `dispatch issues --project <PROJECT> --status needs_review --all`. `--all` reads every match in
   one request. Never page this list: pages by `offset` skip or repeat an issue whose status
   changes between reads, and your reply moves issues out of `needs_review`. `No issues in
   <PROJECT>.` means nothing waits, and a command that fails admits nothing. An issue that enters
   `needs_review` after the snapshot is read at your next start.
4. List the clear defects nobody took yet: each evidence comment on the report issue whose first
   line starts `Clear defect from `, with no session reply under it that starts
   `Filed dispatch://` or `Not filed:` (or `Handed dispatch://` or `Asked dispatch://`, which
   earlier rules wrote), in bash:

   ```text
   notes=$(curl -sS --fail-with-body -H @<(printf 'Authorization: Bearer %s' "$(< "$DISPATCH_TOKEN_FILE")") "$DISPATCH_URL/api/v1/issues/<REPORT KEY>/comments") || { echo "Dispatch read failed" >&2; exit 1; }
   printf '%s' "$notes" | jq -cn 'input | if type == "array" then (. as $all | [.[] | select(.reply_to == null and (.body | startswith("Clear defect from "))) | .id as $id | select([$all[] | select(.reply_to == $id and .author.kind == "session" and (.body | test("^(Handed dispatch://|Filed dispatch://|Not filed:|Asked dispatch://)")))] | length == 0) | {id, body}]) else error("Dispatch did not answer the list") end'
   ```

   It reads as step 3's read does: exit 0 with `[]` is nothing waiting, anything else failed.
   Take each as [File a clear defect no tree owns](#file-a-clear-defect-no-tree-owns) says.

A read that fails, and an assignment Dispatch refuses, are handled as
`skill://legion-controller/SKILL.md#rules` and
`skill://legion-controller/references/daily-report.md` say.
