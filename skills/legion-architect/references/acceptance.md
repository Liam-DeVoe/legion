# Acceptance, the merge and the close

Read the section for your step of `skill://legion-architect`, sections 4 to 7. Where it says to
ask a person, a child's own architect sends the question to the architect above it.

## Retro's result

Retro posts one `dispatch comment` of learnings and one `dispatch message`, brings the pull
request body up to date, and commits nothing. When it reports that it cannot compute, read or
write that body, it is still in its phase: answer it on its role topic from the deployment
instructions, or open a `dispatch ask` naming the missing instruction.

## Accepting

Read, against the goal, every check and every requirement:

1. the tester's handoff: `dispatch handoff-read --issue <issue> --phase test`;
2. `dispatch results --issue <issue>`: whether a pass stands for each check at the approved head;
3. the reviewer's approval of the head its Legion footer names, never its `commit_id`
   (`skill://legion-worker/references/merge-gate.md`);
4. each claim of the reviewer's prose, against the code at that head;
5. `gh pr view <n> --json mergeable,mergeStateStatus`: a `CONFLICTING` head gets no CI
   (*A conflicting head*, below).

Accept an issue only when each requirement block has a serving check, in its own Verification
document or one of an issue under it on the `child_of` chain (another tree's root included), whose
latest result passed and is current. Read coverage with `dispatch read --issue <KEY>` (read again
on `Coverage: unavailable`), list the issues under it level by level with
`dispatch issues --project <PROJECT> --parent <KEY> --all`, and read `dispatch results` for each.
Count a check of an issue under it only once that issue is `done`, with the result at its final
head (after the merge for `after_merge`); while such an issue is not done, hold the acceptance.
Name in the acceptance comment each requirement with the issue and the check that proved it.
Accept a view, or answer `SIGNOFF_RESTING_ROOT_IN_WORKFLOW`, as "A root under an approved ancestor"
in `skill://legion-architect/references/design-gate.md` says.

- `sign_off` works only in `accepting`, where the Dispatch status reads `needs_review`; your
  `handoff_complete` there is refused (`ACCEPTANCE_IS_SIGN_OFF`).
- A `catch-up` whose `reason` names an issue waiting for your acceptance is its wake: the
  `phase-finished` may never have reached you.
- `SIGNOFF_CHILD_IN_WORKFLOW` names each issue beneath still in the workflow: run each to done,
  and sign off again yourself only where the issue's pull request has merged.
- `SIGNOFF_CHILD_OPEN`: run each open child to done, or close or icebox it under the rule, and wait
  for it as for an issue beneath. Before `close_root`, also close or icebox each child that was
  never released.
- Until the person answers about an unreleased child you moved to no parent, sign off neither its
  parent nor any issue above it.
- On an issue with children, the fix of a code defect, a red check's included, goes to a new child
  that you file and release, never to the issue's implementer.
- A finding ask holds the acceptance: one a worker sent you, or one `dispatch read` lists under
  `Open asks:` whose question opens `Finding from ` or `Clear defect from `. Follow it
  (`dispatch follow --ask <id> --action follow`) and publish its reference to the controller topic.
  Accept only once it is resolved, or carries the controller's reply
  (`Filed dispatch://`, `Handed dispatch://` or `Not filed:`): a closed issue hides its open
  questions from the controller.
- No `after_merge` result is asked here; the production check records it after the close.

## A parent's head

Accept a parent on its head, which need not carry its children's merges, once each check a role
runs has a pass at that head whose observed line names a base commit that carries the merge of
every child the check needs (`skill://legion-worker/references/pr-body.md`, "The rules every
phase's evidence follows"). Read a `ci_check` check, an `after_merge` check and a check in another
repository as for any issue: one in another repository needs a pass standing since the close of
each child it needs, at a commit on the branch that issue's pull request merged into or on its
default branch; a `default_branch` check, on the default branch. A criterion proven on a deployed
system is proven after that child's deploy, as the repository's deploy skill says. A child's
result proves no check of the parent: a child carrying part of a parent's requirement proves it
with a check of its own whose `serves` names it. Nothing builds on the parent's merge, which comes
last. Before a child goes to the backlog, move its criterion to a released, open child or a new
child; never downgrade it.

Send the tester no message for the children's merges, wait in `accepting` for no child, and move
nothing back for a child's merge, a park, the last close or a conflict during the wait. Move a
parent whose pass names an older base commit, or none, back to `testing` with
`request_backward_move`, naming the children whose merges that base lacks, and ask for no forward
merge.

If the parent's pull request merged before its children's, move nothing back: once the children
close, have a fresh `task` subagent run each check with no `ci_check` on the default branch and
record each result, read each `ci_check` result in `dispatch results`, post them in the acceptance
comment, then `sign_off`.

## After your sign-off

Retro runs, then the merger merges the approved head under the repository's own rules; you start
none of it. A person's request after your sign-off is a requirement of this issue: the role that
holds the phase makes the `request_backward_move`. When the merger reports:

- a failed verification: tell it to move the issue back with `request_backward_move`, naming what
  failed;
- a merge GitHub refuses for a repository setting or an App permission: open a `dispatch ask`
  naming the pull request, the head and GitHub's refusal, and never bypass a rule;
- a squash into a base other than the default branch: tell the implementer of each open pull
  request of the tree built on that branch whose issue is yours and still in `implementing` to
  merge that base forward in its implementing round. Past `implementing`, the daemon asks for that
  merge itself (*A conflicting head*, below).

## A conflicting head

A head that conflicts with its base keeps the issue in its phase. The daemon asks the implementer
to merge the base forward only when the next step needs a mergeable head (*The daemon's ask* in
`skill://legion-worker/references/conflicts-and-rewrites.md`). A conflict it does not act on yet
is recorded: the tester and the reviewer go on at that head, an approval of it ends the review
round, and the daemon acts when the issue enters `accepting`. When it asks outside
`implementing`, it tells you once by `checks-red`, naming the phase the issue stays in, the head,
the base and why; in `implementing` no notice comes. You start nothing for the merge.

Past review, the daemon also asks the reviewer to read the merge and approve the new head by its
SHA, or request changes. In `accepting`, wait for that approval of the new head before
`sign_off`, and send the issue back with `request_backward_move` when the reviewer requests
changes.

## A review thread GitHub will not resolve

Only the pull request author's App resolves a thread, so the implementer resolves them all. When
GitHub refuses its `resolveReviewThread`, open a `dispatch ask` with the thread's URL and GitHub's
message, for a person to resolve it, with the options resolved and could not. The merger does not
complete while it is open.

## The close

The merge closes the issue (`done`). The production check after the close is not yours. A root's
close lingers the tree for `linger_hours`.

## Hand-over at a root's linger

Ask the person whether a child's work must end with the root. Your role prompt
says what a `handed_over` catch-up asks of you.
