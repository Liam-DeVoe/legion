# Acceptance, retro, the merge and the close

Part of `skill://legion-architect`, sections 3, 5, 6 and 7, and its escalation judgment. Read the
section for the step you are at. Where it tells you to ask a person, a child's own architect sends
the question to the architect above it instead (`skill://legion-architect`, "Every turn").

## Retro's result

Retro output is one `dispatch comment` on the issue with what the work taught, the PR body content
the repository's instructions derive from the pull request's changed paths at that head, and one
`dispatch message` on the issue. Retro commits nothing and pushes nothing, since every handoff is
a record on the Dispatch issue and `docs/solutions/` is history no role writes to now. Retro
writes no handoff and never rewrites the reviewer-approved head.

When the implementer reports that it cannot compute that body content or do the work a line of it
affirms, that its read of the body failed or came back empty, or that GitHub refused the body, it
has pushed nothing and is still in its phase. Answer it with `envoy_publish` to its role topic,
saying how from the deployment instructions; when they do not say, open a `dispatch ask` naming the
missing instruction.

## Accepting

Sign off an issue with no children once its evidence holds for its goal and every requirement of
its spec, none unmet; a parent only after its last round (below), because
before it the approved head does not carry its children's merges.

```text
dispatch comment --issue LEGION-40 --body-file - <<'EOF'
<acceptance: scope, integration evidence, the tester's handoff, each check with its result, the approved head>
EOF
legion({ op: "sign_off", issue: "LEGION-40" })
```

- The issue's Dispatch status stays `needs_review` in `accepting`; `sign_off` moves it to `retro`.
- A `catch-up` whose `reason` names an issue as waiting for your acceptance is the wake for it: it
  is in `accepting`, and the `phase-finished` that moved it there may never have reached you.
  While a root's architect has failed, its notices end undelivered. While a child's own architect
  does not hold its role, the child's notices go to the architect above it. Either way, a retried
  architect learns of the wait from its catch-up alone. Accept it as this section says, a parent
  only after its last round (below).
- The daemon refuses `sign_off` while an issue beneath the one you accept is still in the workflow
  (`SIGNOFF_CHILD_IN_WORKFLOW`, naming each one): run each one to done, and sign off again.
  Park one with `park_child` only when a person asked for that move, or when the work is done, folded into another issue or no longer needed,
  and say in a comment which of those it is. Ask the person about a child that can reach neither
  `done` nor a park under that rule, and hold the acceptance until they answer. Until the person
  answers the ask on the root about an unreleased child you moved, sign off neither the issue you
  moved it under nor any issue above it that you accept (`skill://legion-architect`, section 3):
  release the child when they say to run it; when they say to stop it, close it, or move it to
  `icebox` under the rule when they say its work should wait, citing their answer. A parent: sign
  off only after its last round (below). The daemon then closes a parent that is not the root at
  its own merge, which under this rule comes after its children's. One issue beneath never holds
  it: one the daemon itself holds at `admitted` under an ancestor it has already accepted, which
  starts when that ancestor closes.
- `request_backward_move` names the issue, because your grant serves the tree; the daemon starts
  that phase's role with your reason in its task.
- `handoff_complete` from you in `accepting` is refused `ACCEPTANCE_IS_SIGN_OFF`. CI red at the head
  in `accepting` returns the issue to `implementing` by the daemon's table.
- No `after_merge` result is asked here: the production check records it after the close, where
  the project's `production_check` opens one.
- A finding ask on the issue holds the acceptance: one a worker sent you, or one `dispatch read`
  lists under `Open asks:` whose question opens `Finding from `
  (`skill://legion-worker/references/findings.md`, "Where it goes"). Follow each
  (`dispatch follow --ask <id> --action follow`) and, once answered, publish its reference to the
  controller topic. Accept only once each is resolved or carries the controller's
  `Filed dispatch://` or `Not filed:` reply, because a closed issue hides
  its asks from the controller.
- A merge hold on the repository of the issue's pull request holds the acceptance: read the
  project's merge holds before every `sign_off` ([A merge hold](#a-merge-hold)).

## A merge hold

The controller writes each merge hold of the project as one line of the project's document
`merge-holds-md`, and sends a hold to no tree. Before every `sign_off`, read it:

```bash
dispatch doc-read --project <PROJECT> --artifact merge-holds-md
```

`<PROJECT>` is the project of the issue's key (`LEGION` for `LEGION-41`). A line under the
document's `Holds` heading reads `- <repositories> | <reason> | <end>`. It holds the sign-off when
it names the repository of the issue's pull request (`owner/name`), or `all`, and its end is not
past: its end is `until the controller lifts it`, or an RFC 3339 time later than
`date -u +%Y-%m-%dT%H:%M:%SZ` prints. No line means no hold. A read that answers
`document "merge-holds-md" not found by slug` means the project has no such document, and no hold.
Any other failed read decides nothing: sign off nothing on it.

While a line holds the sign-off:

1. End the turn with a line that starts with WAITING and names the hold: its repositories, its
   reason and its end. Subscribe to nothing for the hold.
2. At each change of the document, the daemon sends a `merge-holds-changed` notice to the
   architect of each issue in `accepting`. The notice wakes you also when the daemon released your
   pod, which a Dispatch document event does not do. Its `reason` names the document.
3. On that notice, read the document again. If a line still holds the sign-off, end the turn
   again as step 1 says. If no line holds it, accept as [Accepting](#accepting) says.
4. Another wake tells you nothing of the hold: a notice of another kind, a task, a message on your
   role topic, or a mention. Read the document again before you sign off on any wake.

The passing of an end time sends no event. The controller deletes a line whose end is past on its
next tick, and that deletion sends the notice. The document is the whole hold, so send the
controller no message about it.

## The token the secrets broker lends

These GitHub writes of your tree's work take the implement App token that the secrets broker
lends every enrolled pod (`skill://agent-secrets`): a re-run of your tree's CI run, the enqueue of
a pull request that no merger merges, and any other write that your own `gh` cannot make, such as
a comment on a pull request in a repository that your `gh` does not reach. Take the token
narrowed to the one repository, in this form:

```sh
agent-secrets LEGION_IMPLEMENT_APP --reason '<KEY>: <the write, and why>' --github-owner <owner> --github-repo <repo> --github-token -- sh -c 'printf "Authorization: Bearer %s\n" "$LEGION_IMPLEMENT_APP" | curl -fsS -X POST -H @- <the rest of one curl call>'
```

- `--github-owner` and `--github-repo` narrow the token to the one repository. `--github-token`
  gives the token alone. Without it, the name holds the JSON object of installations, and GitHub
  answers `401`.
- Make the call with `curl` to `https://api.github.com`, never with `gh`: the `gh` wrapper refuses
  a set `GH_TOKEN`. Keep the call in single quotes, so that the child shell reads
  `$LEGION_IMPLEMENT_APP`, and give `curl` the header on standard input (`-H @-`), so that the
  token is in no command's arguments (`skill://agent-secrets`). Never print the token.
- Name in `--reason` the write, why you make it, and your issue's key.
- After the grant, `agent-secrets` writes one line, `agent-secrets: LEGION_IMPLEMENT_APP from
  <source>`, and the exit status is then `curl`'s. Without that line the status is the client's
  own, and 77 is a denial. A denial is final: ask no other session to make the write.

A comment on the pull request `<n>`, with `<the comment>` its text:

```sh
agent-secrets LEGION_IMPLEMENT_APP --reason '<KEY>: comment on <owner>/<repo>#<n>, which my gh does not reach' --github-owner <owner> --github-repo <repo> --github-token -- sh -c 'printf "Authorization: Bearer %s\n" "$LEGION_IMPLEMENT_APP" | curl -fsS -X POST -H @- --data-binary "$1" https://api.github.com/repos/<owner>/<repo>/issues/<n>/comments' sh "$(jq -nc --arg body '<the comment>' '{body: $body}')"
```

It worked when `curl` exits 0: GitHub answered `201`, and the answer holds the comment's
`html_url`.

## A pull request no merger merges

An issue's merger merges the pull request that its issue records, and no other. A pull request of
your tree that no issue records is yours to put in its base's merge queue, or to arm with
auto-merge where the base has no merge queue. Ask no other session, the controller included, to
merge or queue it. The controller holds no enqueue: it writes its own request, or a person's in
the person's words, to you by a mention, and tells an agent that asks it to write to you itself.

**Which pull request.** All three of these hold:

- It is a pull request of your tree: its head branch begins with your tree's root key in lower
  case and a slash (`acme-41/…` under the root `ACME-41`), or it is `legion/<KEY>` of an issue of
  your tree.
- No issue of your tree records it: it is the `pullRequest` of no issue's `read_record`.
- A review approves its head (step 2).

**The steps.** `<head sha>` is the one head that every step names.

1. Read the pull request:

   ```sh
   gh pr view <n> -R <owner>/<repo> --json id,number,title,state,headRefName,baseRefName,headRefOid,mergeStateStatus,autoMergeRequest
   ```

   `headRefOid` is `<head sha>`, and `id` is `<pull request id>`. Check `headRefName` against the
   first rule above. A `state` other than `OPEN` ends the procedure. A non-null `autoMergeRequest`
   is auto-merge that is armed already, at a head that you did not read: turn it off first
   (step 7), then begin again at step 1.
2. Read the approvals:

   ```sh
   gh api --paginate repos/<owner>/<repo>/pulls/<n>/reviews --jq '.[] | select(.state == "APPROVED") | [.user.login, .commit_id, ((.body // "") | capture("\"head\" *: *\"(?<head>[0-9a-f]{40})\"").head // "-")] | @tsv'
   ```

   Each line is one approval: the reviewer, the review's `commit_id`, and the head that its Legion
   footer names, or `-` where it has no footer. The head is approved when a line names
   `<head sha>` in its footer, or, with no footer, as its `commit_id`. When no line does, enqueue
   nothing.
3. Read the merge holds, as [A merge hold](#a-merge-hold) reads them before a sign-off. A line
   that names `<owner>/<repo>` or `all`, and whose end is not past, holds the enqueue: enqueue
   nothing, and name the hold in your summary. The `merge-holds-changed` notice reaches you only
   for an issue in `accepting`, so read the document again on each wake before you enqueue.
4. Read the base:

   ```bash
   base=$(gh pr view <n> -R <owner>/<repo> --json baseRefName -q .baseRefName) && \
     base_path=$(jq -rn --arg b "$base" '$b | split("/") | map(@uri) | join("/")') && \
     gh api "repos/<owner>/<repo>" --jq '[.squash_merge_commit_title, .squash_merge_commit_message] | @tsv' && \
     gh api --paginate "repos/<owner>/<repo>/rules/branches/$base_path" --jq '.[].type'
   ```

   A `merge_queue` line means a merge-queue base. `enqueuePullRequest` takes no message, so on
   such a base a `squash_merge_commit_message` of `COMMIT_MESSAGES` puts the branch's commit
   messages on the default branch. Then enqueue nothing. Open a `dispatch ask` on your root that
   names the pull request, the head and the setting, because only a person can change the setting
   to `PR_BODY` or `BLANK`.
5. On a merge-queue base, enqueue the pull request at that head:

   ```sh
   agent-secrets LEGION_IMPLEMENT_APP --reason '<KEY>: enqueue <owner>/<repo>#<n> at <head sha>, which a review approved and no merger merges' --github-owner <owner> --github-repo <repo> --github-token -- sh -c 'printf "Authorization: Bearer %s\n" "$LEGION_IMPLEMENT_APP" | curl -fsS -X POST -H @- --data-binary "$1" https://api.github.com/graphql' sh "$(jq -nc --arg pr '<pull request id>' --arg head '<head sha>' '{query: "mutation($pr:ID!,$head:GitObjectID!){enqueuePullRequest(input:{pullRequestId:$pr,expectedHeadOid:$head}){mergeQueueEntry{state position}}}", variables: {pr: $pr, head: $head}}')"
   ```

   On any other base, arm auto-merge at that head, with the merger's subject and an empty body:

   ```sh
   agent-secrets LEGION_IMPLEMENT_APP --reason '<KEY>: arm auto-merge of <owner>/<repo>#<n> at <head sha>, which a review approved and no merger merges' --github-owner <owner> --github-repo <repo> --github-token -- sh -c 'printf "Authorization: Bearer %s\n" "$LEGION_IMPLEMENT_APP" | curl -fsS -X POST -H @- --data-binary "$1" https://api.github.com/graphql' sh "$(jq -nc --arg pr '<pull request id>' --arg head '<head sha>' --arg title '<pull request title> (#<n>)' '{query: "mutation($pr:ID!,$head:GitObjectID!,$title:String!){enablePullRequestAutoMerge(input:{pullRequestId:$pr,expectedHeadOid:$head,mergeMethod:SQUASH,commitHeadline:$title,commitBody:\"\"}){pullRequest{autoMergeRequest{enabledAt mergeMethod}}}}", variables: {pr: $pr, head: $head, title: $title}}')"
   ```

   GitHub answers in JSON, and `curl` exits 0 on a refusal too, so read the answer. The call
   worked when the answer's `mergeQueueEntry` or `autoMergeRequest` is an object. An `errors`
   array is GitHub's refusal: quote its `message`. A refusal for a repository setting or an App
   permission is a person's to change: open a `dispatch ask` on your root.
6. Read the pull request again:

   ```sh
   gh api graphql -F number=<n> -f owner=<owner> -f name=<repo> -f query='query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){state headRefOid isInMergeQueue mergeQueueEntry{state position} autoMergeRequest{enabledAt mergeMethod}}}}'
   ```

   It worked when `headRefOid` is still `<head sha>`, and `mergeQueueEntry` on a merge-queue base,
   or `autoMergeRequest` on any other base, is not null. A `state` of `MERGED` means that GitHub
   merged it. Read it again on each later wake while it is open.
7. A head that moved after step 5 is a head that no review approved, and GitHub checks the head
   of an auto-merge only when it arms it, never when it merges. So when a read of step 6 shows
   another `headRefOid` than `<head sha>`, take the pull request out of the queue, or turn its
   auto-merge off, at once. Then begin again at step 1. Out of the queue:

   ```sh
   agent-secrets LEGION_IMPLEMENT_APP --reason '<KEY>: take <owner>/<repo>#<n> out of the merge queue: its head moved from <head sha>' --github-owner <owner> --github-repo <repo> --github-token -- sh -c 'printf "Authorization: Bearer %s\n" "$LEGION_IMPLEMENT_APP" | curl -fsS -X POST -H @- --data-binary "$1" https://api.github.com/graphql' sh "$(jq -nc --arg pr '<pull request id>' '{query: "mutation($pr:ID!){dequeuePullRequest(input:{id:$pr}){mergeQueueEntry{position state}}}", variables: {pr: $pr}}')"
   ```

   Auto-merge off:

   ```sh
   agent-secrets LEGION_IMPLEMENT_APP --reason '<KEY>: turn off auto-merge of <owner>/<repo>#<n>: its head moved from <head sha>' --github-owner <owner> --github-repo <repo> --github-token -- sh -c 'printf "Authorization: Bearer %s\n" "$LEGION_IMPLEMENT_APP" | curl -fsS -X POST -H @- --data-binary "$1" https://api.github.com/graphql' sh "$(jq -nc --arg pr '<pull request id>' '{query: "mutation($pr:ID!){disablePullRequestAutoMerge(input:{pullRequestId:$pr}){pullRequest{autoMergeRequest{enabledAt}}}}", variables: {pr: $pr}}')"
   ```

   The read of step 6 then shows `mergeQueueEntry` and `autoMergeRequest` null.

## A re-run of your tree's CI run

A CI run of your tree is a run on the head of a pull request of your tree
([A pull request no merger merges](#a-pull-request-no-merger-merges) says which pull requests are
your tree's). Its re-run is yours: ask no other session, the controller included, to re-run it.
Re-run only its failed jobs, and only for an infrastructure failure that the failed job's log
shows. Use the rules, the command and the reads of
`skill://legion-controller/references/operations.md#a-re-run-of-a-ci-run`: the rules are the
`developing-ci` skill's section 7, and the command takes the token as
[The token the secrets broker lends](#the-token-the-secrets-broker-lends) says. That section is
written for the controller. Follow it for your own tree's runs alone, and where it names the day's
report, name the denial in your summary.

## The last round

A parent's own pull request proves each of its checks, so sign off a parent with children only
on an approved head carrying the merges of every child those checks need. Each needs a passing
result at that head, or, for one in another repository, at a commit on the branch that
issue's pull request merged into or on that repository's default branch — that default branch
alone where Dispatch knows of no merge of the issue — and, for a `default_branch` check, on that
default branch whatever branch the pull request merged into. A child records no result that
proves a parent's check: such a result was recorded at the child's head and not at the parent's,
and a child carrying part of a parent's
requirement proves it with a check on its own Verification document whose `serves` names that
requirement. Nothing in the tree builds on the parent's own merge, which comes last: a child
that waits for it waits forever, so work another child builds on is a child of its own.
While the approved head does not carry those
merges, or a check in another repository has no pass standing since the close of a child it
needs, leave the issue in `accepting` until each has closed (`child-closed`) or a park has
already taken it out (`child-status`). That wait is no reason to park a child. Move a parked
child's criterion to the parent's own pull request or a released, open child first; never
downgrade it. The parent's requirement stays the parent's, pending
until this round, where its head must meet it.
Then, in one turn:

1. `envoy_publish` to the tester's role topic: "The next round of <KEY> is its last round: a full
   test round of every check of the issue with no `ci_check`, not the forward-merge re-check." The
   backward move's reason reaches only the implementer.
2. `request_backward_move` to `implementing`, with the reason "Last round: merge the default
   branch your addressing line names into the branch, push, end your forward-merge comment with
   `parent's last round: full test round`, and complete." Never use a hard-coded `main`. The move
   counts a review round, or a conflict round, which `review_round_cap` leaves out, where the
   record holds the head conflicting with its base.

While the parent waits in `accepting`, a conflict on its head or a child's merge needs no move of
its own: the last round's forward merge resolves them all, so spend no backward move per child.
A move for a child's merge counts toward `review_round_cap`, since the record holds no conflict on
the head to leave it out; a move for a conflict the record does hold is left out, and still costs
the implementer a forward merge the last round makes anyway.

A criterion proven on a deployed system is proven after that child's deploy, as the repository's
deploy skill says.

If the pull request merged before its last round, it can carry no new head, so move nothing back.
Once they have closed, have a fresh `task` subagent run each check with no `ci_check` on the
default branch and record each result. Read each `ci_check` check's derived result in
`dispatch results`, which no role records. Post them in the acceptance comment, then `sign_off`.

## The order after your acceptance

The daemon keeps this order from its fixed table; you start none of its steps:

1. The tester's last result is green and the reviewer approves the head.
2. The reviewer's approval of the head by SHA moves the issue to `accepting`; your `sign_off` moves
   it to `retro`.
3. Retro records what the work taught as a `dispatch comment` on the issue and brings the PR body
   up to date. It commits nothing, so the approved head is the head the merger merges. No merge
   carries a handoff onto the default branch, since every handoff is a record on the Dispatch
   issue and no commit carries one.
4. The merger reads the pull request and merges that head itself, as the implement App and with the
   head it read as the head GitHub must match
   (into the default branch
   `gh pr merge <n> --squash --match-head-commit <head> --subject '<pull request title> (#<n>)' --body ''`,
   whose explicit message keeps GitHub's default squash message from joining the branch's commit
   messages onto the base; into another base, a merge commit (`--merge` in place of `--squash`)
   where the repository allows one, because a squash there gives each pull request built on that
   branch a conflict of history alone; or `expectedHeadOid` on a merge-queue base, where it
   enqueues the pull request); it
   never turns auto-merge on, and the daemon submits no merge and changes no merge setting. The
   repository's required reviews and
   checks decide when it lands; Legion neither reads nor writes those rules, and a review only a
   person can give is waited for, never bypassed. Nothing compares that head with the approved one:
   a repository that wants a later push reviewed says so in its own rulesets. The merger's
   completion records its phase row and moves nothing, and the daemon posts no packet.
5. The merge lands under the repository's rules, the daemon reads it and closes the issue once
   every issue beneath it has left the workflow (for a root, every other issue of its tree).

A person's request after your sign-off is a requirement of this issue, filed nowhere else: in
`retro` or `merging` the role holding the phase makes the `request_backward_move`.
A merge the merger cannot make arrives while the merger holds `merging`, so the merger makes the
move and you tell it which; from `accepting` you send an issue back only to `implementing` or
`testing`.

- A failed verification the merger reports: handle it as the `pr-blocked` row of
  `skill://legion-architect/references/wake-routing.md` says, telling the merger to move the
  issue back with `request_backward_move`, naming what failed; never bypass.
- A merge GitHub refuses for a repository setting or an App permission — a merge-queue base whose
  `squash_merge_commit_message` joins the branch's commit messages among them: the merger tells
  you, and only a person can change it. Ask one with `dispatch ask`, naming the pull request, the
  head and GitHub's refusal; never bypass a rule.
- A squash into a base other than the default branch, where the repository allows only squash or
  the base's rules require a linear history: the merger tells the architect its addressing line
  names which open pull requests of the tree build on the merged branch. Each one whose issue
  stands past `implementing` and that GitHub then reports `CONFLICTING` comes back to
  `implementing` (*A conflicting head*, below), and the architect that owns its issue hears of it
  by `checks-red`. For each pull request on that list whose issue is yours, tell its implementer
  (`envoy_publish` to its role topic) to merge that base forward in its implementing round. One
  whose issue is still in `implementing` gets no move, no task and no `checks-red` from the daemon.

## A conflicting head

What returns the tree to review is a changed diff: a conflict-resolution merge whose
fingerprint (the unchanged-diff check,
`skill://legion-worker/references/conflicts-and-rewrites.md`) differs from the approved head's.
A conflict merge whose fingerprint is unchanged does not, and no Legion check compares the head
that merges with the approved one: a repository that wants a later push reviewed says so in its
own rulesets.

Make a conflict-resolution merge only when GitHub reports `CONFLICTING`
(`gh pr view <n> --json mergeable,mergeStateStatus`). From `testing`, `reviewing`, `accepting`,
`retro` and `merging` alike the daemon acts on it itself: it moves the issue back to
`implementing`, starts the implementer, and tells you with a `checks-red` notice naming the phase
the issue was taken out of and whose reason reads
`the head conflicts with <base>: GitHub runs no checks on it; merge <base> forward`. You start
nothing for it.

The daemon then runs the phases from `implementing`: the implementer merges the bookmark forward
with the destination and pushes it (the procedure in
`skill://legion-worker/references/conflicts-and-rewrites.md`), posting the before and after
fingerprints; the tester re-runs the bare gates only and the class search of every earlier
round's findings at the new head; the reviewer approves the new head by SHA, or continues its
round if it had not approved; the daemon carries the issue through your acceptance and retro to
the merger's merge.

## A review thread GitHub will not resolve

GitHub lets only the pull request author's App resolve a thread, so the implementer resolves every
thread with plain `gh`: the ones it answers and the bot threads the reviewer accepted and named to
it. The reviewer resolves none. If the implementer reports that GitHub refused its
`resolveReviewThread`, open a `dispatch ask` naming the thread's id or URL and GitHub's message, for
a human to resolve it by hand, with options for resolved / could not. The merger does not complete
while that ask is open; the merge itself checks no thread state
(`skill://legion-worker/references/merge-gate.md`).

## The close

The merge closes the issue: the daemon reads the merged pull request — from GitHub's webhook or
from its own read of the issue — and writes `done`. An issue that reaches `merging` on a pull
request GitHub has already merged closes at once.

- An issue whose merge lands while an issue beneath it is still in the workflow (for a root,
  another issue of its tree) stays in `merging`, its workers kept and live, until the last of
  them is done or parked. One
  `pr-merged` notice names what it waits for and none follows as they depart, so read the pull
  request on GitHub.
- A child's close reaches you as `child-closed`. Where the project's `production_check` names the
  role that works the check — the tester, unless the setting is `off` — the daemon retires five
  of its six claims and leaves that role running, because the issue still owes the production
  check; where the setting is `off` no check opens, and the close retires all six and releases
  the pod. When the issue's
  pod and Sandbox go is in `skill://legion-worker/references/merge-gate.md`, *The production check
  after the close*.
- A root's close writes `done`, releases any admission slot the tree's issues still held, and lingers the tree for
  `linger_hours` with every member suspended. The linger's end, the tree's last step, waits until
  every member's open production check has closed.

Nothing of the production check is yours. Its record is the pull request and the result of the
issue's `after_merge` check, whether or not the issue is closed. A defect it finds is never an
issue the checking role files: a clear defect goes to the architect of the tree whose code it is,
or to the controller to file when no tree owns that code, and only a defect that needs a decision
is a `dispatch ask`. `read_record` shows a closed issue's open check (`check`: the pull request, the
merge commit, the role the check records as the one that works it, the looks, what woke the last
one, and `nextWakeAt`, when the next look is due — the time the checking agent named for itself on
a `pending`, the retry of a look nobody reported, or the check's own bound where neither stands).

## Hand-over at a root's linger

When a person closes a root and it stays `done` through its linger, each child still in the
workflow is handed to Legion as a root of its own: `todo` with the `legion` label, its phase, gate,
phase records and pull request carried into a new generation, and that phase's role started. A
child that had its own architect keeps that architect's session: the root's architect start
resumes the same claim, and its next launch gives it the root architect's prompt. A child that ran
without one gets a new architect session. A child that never started is handed over too,
carrying no run, whatever held it at `admitted`: it waits in the line for a slot and for any
unfinished blocker, and its architect starts as a fresh root and registers its own spec. Only a
child that finished, was parked or was never released stays. The child stays its own tree;
reopening the old root does not take it back. The hand-over runs by itself and needs no action
from you.
Park a child with `park_child` before then only when a person asked for that move, or when the work is done, folded into another issue or no longer needed, and cite that reason in a comment on the child.
Ask the person whether a child's work must end with the root. A root that is parked
rather than closed hands over only a child in `merging` whose pull request is still open,
since the daemon re-admits no parked root and only that child's own merger, started again under
its own tree, merges that head. Ask the person whether that child's merge must wait for the root.

If you are that child's architect:

- You may be the child's own architect from before the hand-over. Your session is the same, and
  your role prompt is now the root architect's. Follow that prompt from your next `catch-up`,
  which says `handed_over`.
- Your catch-up says `handed_over` and `carried`: resume from the phase, and never register a spec
  or ask for approval again.
- It says `handed_over` alone (no gate to carry): your workers already run their phases, so
  decompose nothing, and read the root's phase from the catch-up (for a root `held`, the phase its
  `held` notice names). Before the merge, register your spec with `register_gate`: the tree's
  work starts on its approval.
  That registration's approval request waits with the person like any other, but it starts no wait of the root's own: a root your catch-up calls `handed_over` stands at a phase it carried, never `admitted`, and only a root at `admitted` waits on a person.
  The section below is for a root of your own at `admitted`, not for this one.
  When that `design-approved` arrives, release and start
  nothing: the workers are already running. A child carried in `accepting` is yours to accept. In
  `merging` the work is already through every gate, so register none.

## Your slot while a person has your spec

This is for a root of your own standing at `admitted`, the only phase from which a root waits on a person.
A registration that leaves the approval request with the person moves your root to `awaiting_person`, and what brings the root back is the answer, not the registration.
Your root keeps its admission slot throughout: a root in `awaiting_person` is one of the roots that keep their slot through a wait ([Admission and the `legion` label](https://cybermonklabs.github.io/legion/legion/concepts/#admission-and-the-legion-label)).
So your resume takes no slot and comes however full the cap is, whether or not the gate has ever opened in this generation.
The first phase that resume starts waits in the line among the issues whose work came back, oldest first; whether it needs a slot again depends on whether your root waits on another issue, an open child under it or a `blocked_by` link to an issue that is not `done` ([Admission and the `legion` label](https://cybermonklabs.github.io/legion/legion/concepts/#admission-and-the-legion-label)).
A changes request, or a newer version of the document that hands the request back to you, leaves the gate closed, so it holds the tree's work until a new approval.
While your answered root stands in the line, your record carries `waitingLine`: your place in the line and its length, your `part` (`returned`) and `kind` (`answered`), your `partPlace` of `partLength` among the issues whose work came back, `holdsSlot` true because your root has held its slot since its start, `since`, the moment your work came back, and `reason`, the daemon's own sentence for that place.
An issue that has not started carries `rootRank`, `ownRank` and `rootKey` in place of `since`, the two Dispatch ranks that placed it and the key of its tree root.
`why` beside it says why any issue of your tree is not working now, in one word and one sentence: `claim_stopped`, a claim nothing will wake; `waiting`, this line; `blocked`, a Dispatch link or an open child below it, though its reason can be the sentence of the issue's `admission.held` word (the sentence table of `packages/daemon/internal/projection/why.go` says when); `unlisted`, an issue the record holds in the workflow that no admission list places.
Nothing is sent to you about that wait, and your resume task says what answered it.
Act on neither your place nor your wait: the daemon starts you again on the pass that reads the answer.
