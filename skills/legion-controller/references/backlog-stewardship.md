# Backlog stewardship

The controller keeps the Dispatch backlog's facts aligned with the deployment's current weekly
plan. The plan is the source of the work order and its one-owner map; Dispatch board rank is the
executable form of that order. The deployment instructions name the weekly-planning issue and its
`plan-week-*` artifact.

## Controller routing address

Claim no Envoy role other than `legion-<project>-controller`, your startup role, because changing
roles ends the controller's wake delivery. Scope questions, routing requests, finding-ask
references and clear defects no tree owns reach you on
`notifications.role.legion-<project>-controller`.

## The prioritization pass

The pass keeps the order of the project's work aligned with the project's priorities. No timer
starts it: a wake does. Run it at most once a turn, after the wake's work, as the last step
before your summary.

**When.**

- On every turn that a `tick`, `slot-free` or `todo` wake starts.
- On your first turn of each UTC day, whatever woke you, and after every directive from a person,
  in its daily form: [Reconcile the weekly plan](#reconcile-the-weekly-plan).
- On every other turn, when all three of these hold: the wake's work is done, nothing else waits
  for you, and the board changed since your last pass. The board changed when this read lists an
  issue:

  ```bash
  dispatch issues --project <PROJECT> --updated-since <the time of your newest pass line> --all
  ```

  Every change to an issue updates it: its status, its rank, its priority, its labels or its
  assignee. When your context holds no pass line, run the pass. A new session, a restarted
  session and a compacted session can hold none.

**What to read.**

1. `read_state`'s `admission`: the line (`waiting`, in the line's own order), `active` and
   `blocked`. The `waiting` and `active` lines of the `read_state` summary give the same.
2. The weekly plan: the newest `plan-week-*` artifact of the weekly-planning issue, which step 1
   of [Reconcile the weekly plan](#reconcile-the-weekly-plan) selects.
3. The open P0 and P1 issues: `dispatch issues --project <PROJECT> --priority 0 --priority 1 --all`.
   Make [the owner audit](#the-owner-audit) on that read.

**What to change.** Change each of these where it no longer matches the project's priorities,
and change nothing else:

- **A rank.** Make the board order match the plan's order, as step 2 of
  [Reconcile the weekly plan](#reconcile-the-weekly-plan) says:
  `dispatch issue-update --issue <KEY> --before <OTHER>`, or `--after <OTHER>`.
- **An admission.** Admit an issue only as the intake your deployment instructions give and the
  moves to `todo` of [Free slots](skill://legion-controller/SKILL.md#free-slots) allow. A free slot
  is no reason to admit an issue. Park a root that the plan defers only as
  [A root the plan defers](#a-root-the-plan-defers) says.
- **The plan.** Record in the plan what it no longer matches: a person's directive that it does
  not hold yet, or an owner that changed. Edit the line as step 4 of
  [Reconcile the weekly plan](#reconcile-the-weekly-plan) says.

**How it ends.** End the pass with exactly one line in your summary:

```text
Prioritization pass at <RFC3339>: <what it changed, or none>
```

- `<RFC3339>` is what `date -u +%FT%TZ` prints after the pass's last change. So the changes you
  made in the pass are older than the line, and your next turn does not read them as a change of
  the board.
- `<what it changed, or none>` names each change, with the issue and the move, such as
  `ACME-41 before ACME-40` or `ACME-43 admitted`. Write `none` when the pass changed nothing.
- Write the line once a turn, and only on a turn that ran the pass. The line is no note between
  turns ([Rules](skill://legion-controller/SKILL.md#rules)): your next turn reads it in its own
  context, and a session that has no such line runs the pass.

## Reconcile the weekly plan

This is the daily form of [the prioritization pass](#the-prioritization-pass). Run it on your
first turn of each UTC day, whatever woke you, and after every directive from a person. It reads
what the pass reads and makes every change the pass makes. It also reconciles the plan's
one-owner map (step 3). It ends as the pass ends, with the pass's one line. Its steps:

1. Read the weekly-planning issue and select its newest `plan-week-*` artifact by its latest
   `artifact.version` event. Read that artifact before changing any issue.
2. For every plan line whose issue key carries this project's prefix (`daemon.project` in
   `read_state`), make the project's board order match the line order exactly, because board rank
   is the order Legion admits by: an issue whose first phase can start takes the rank of its
   tree's root, and an issue whose work came back goes ahead of all of them. Set the priority the
   plan line states, and leave the priority where it states none; priority changes no admission
   order. When one plan line lists
   several issues, keep them in that order. Take the order from the plan alone, never from a
   title or a route. Reorder with
   `dispatch issue-update --issue <KEY> --before <OTHER>` or `--after <OTHER>`.
3. Reconcile the plan's one-owner map. `envoy_sessions` gives each session a `state` (`live`,
   `unresponsive` or `gone`) and a `last_seen`; read the state, never the presence or absence of
   the row. A `live` or `unresponsive` owner still owns its work: change nothing for it. Only an
   owner that reads `gone`, or that the registry has no record of, is without a session. For
   unowned work, find an owner whose `envoy_sessions` `dir` is a
   checkout of the issue's repository, route the issue to it
   (`dispatch issue-update --issue <KEY> --route session:<session id>`) and `envoy_send` it
   `You own <KEY>: <title>.`. Routing starts nothing on Legion and sets no label. Never route an
   issue that carries the `legion` label, because Legion's own roles work it. Do not change the
   human `Assignee:` field: it decides who receives asks, not who works the issue.
4. Edit that issue's line in the plan artifact (its owner column) with `dispatch doc-edit`,
   guarded by the document token from the current `dispatch doc-read`. Replace superseded text
   instead of keeping a change log in it, because the artifact states the current rule. A comment
   or message does not update the one-owner map.

A directive from a person is a direct Dispatch message to this controller. Answer it first, carry out
its directive on scope or priority, then record the resulting current rule in the affected weekly-plan
row (or its directives section when it has no one line). Link the message when Dispatch gave it a
reference.
A directive runs this daily form even when it appears to affect only one issue.

## Answering controller deliveries

Read every delivery to `notifications.role.legion-<project>-controller` against live Dispatch and
daemon state. You decide labels, the statuses `todo`, `backlog` and `icebox`, and board order.
Answer a scope question from the weekly plan and the deployment instructions. Answer a routing
request with the owner the plan's one-owner map names for that issue, and that owner's address,
so the asker writes to it. Send nothing on: you forward no message to another session.

Never merge or enqueue a pull request from the controller, whatever credential you hold. The
merger merges its issue's pull request itself, at the head it read and under the repository's own
rules. A tree's architect enqueues a pull request of its tree that no merger merges
(`skill://legion-architect/references/acceptance.md#a-pull-request-no-merger-merges`). Write your
own request to merge or queue a pull request, or a person's in the person's words, to that
architect by a mention, and tell an agent that asks you for one to write to that architect itself
(`skill://legion-controller/references/operations.md#each-kind-of-request-the-controller-sent`).

## The owner audit

At each prioritization pass, read `dispatch issues --project <KEY> --priority 0 --priority 1 --all`:

- An unclaimed row in `triage`, `icebox`, `backlog` or `todo` is a decision: someone builds it, or
  it closes. A claimed or in-flight row already has an owner.
- A close that says a defect cannot happen cites the code that prevents it; without that, the issue
  is unread, not foreclosed.
- A plan or measurement nobody builds needs an owner.
- A merged pull request names an issue not yet in `testing` or `done`: check it live, then move it.
- Someone repeats the issue's work by hand: fix its status.
- A route nobody holds (`--route-status no_holder`, any priority), after the two reads that
  "Recovering stopped-session work" names: staff the role, route the issue to a live holder, or
  clear the route and assign it.

## Recovering stopped-session work

Treat a session route as stopped only after two reads of
`dispatch issues --project <KEY> --route-status no_holder --all` at least ten minutes apart
both list it, as `dispatch issues --help` says for `route_status`, because a
restarting session is absent for minutes. Do not poll to create the second read; use the next
controller wake. Then read the owner's state with `envoy_sessions` (the session the route names,
or one that took it over). A `live` owner still holds the work. So does an `unresponsive` one,
which is past the live window but inside the gone horizon: it keeps its claims, and a message you
send it waits for it. Recover the work only from an owner that reads `gone`, or that reads
`unresponsive` with a `last_seen` more than 20 minutes old, which is as long as an issue claim is
protected.

For work that a person's directive, recorded in the plan with its link (the directives section
above), says moves to Legion when its session stops, preserve its existing labels, add `legion`,
then set it `todo` with the `legion` tool's `set_status`. A plan row you wrote with no such directive
behind it moves nothing. The label comes first: it is what
lets the daemon admit the root. Do not apply that recovery to an issue already in a live Legion
tree (an issue whose record's `tree` names a root whose tree is live, as
`skill://legion-controller/references/admission.md` reads a root); its architect and daemon own its lifecycle. Update the weekly-plan owner row after the move.

## A root the plan defers

A person's `legion` label outranks a plan's deferral. The daemon queues the root at once: a
person's label on an unrecorded root in `triage` is the root's `todo`, written by the daemon. A
labelled root that the daemon has queued or runs, and that the weekly plan says to defer, is
parked by you at your next prioritization pass, not before, and only when the plan's line names a
person's request to defer it: `set_status` it to `backlog`, or to `icebox` for a longer deferral,
and cite that request in a comment on the issue.
Park a root only when a person asked for that move, or when the work is done, folded into another issue or no longer needed.
A plan row you wrote with no such request behind it moves nothing. Ask the person whether to
defer that root instead:

1. Look for your earlier ask for that row. Read the root's comments with the command of
   `skill://legion-controller/references/wakes.md#ask-whether-to-park-a-tree` step 1, with
   `Asked whether to defer: ` in place of `Asked whether to park: `, and take the newest whose
   second line is exactly that plan row. Compare that line alone. A comment or a reply from any
   session counts as yours. When its ask is open, open nothing and post nothing. When it is
   resolved with no answer, open nothing and post nothing for that row. The day's report names
   the root and that ask, as `skill://legion-controller/references/daily-report.md` says. When it
   is answered, and no reply of yours under it starts `Deferred dispatch://`,
   `Not deferred dispatch://` or `Let run dispatch://`, act on its answer now (step 4). When your
   reply under it starts `Deferred dispatch://` or `Let run dispatch://`, the person decided
   already: open nothing and post nothing. When your reply under it starts
   `Not deferred dispatch://`, that ask is spent, because its `Defer it` came when the check of
   step 2 failed. Go on to step 2.
2. Otherwise, and when step 1 sends you here, check first that Legion runs the root: it carries
   the `legion` label, it stands in `admission.active` or `admission.waiting` of `read_state` (the
   `active` and `waiting` lines of its summary), and
   `issues.<root>.phase` is not `done`. When one of these fails, open nothing and post nothing,
   because there is nothing to defer. When all of them hold, open a `dispatch ask` on the report
   issue that names the root and the plan row and
   asks the person whether to defer that root, with the options `Defer it` and `Let it run`.
   Follow it (`dispatch follow --ask '<ref>' --action follow`). Post one comment on the root
   (`dispatch comment --issue <root> --body-file -`). Make its first line
   `Asked whether to defer: dispatch://<REPORT KEY>/ask/<id>`. Make its second line the plan row,
   exactly as the plan gives it: that line is the key that step 1 compares.
3. Name the row and that ask in the day's report while the ask is open.
4. On `Defer it`, when the check of step 2 still holds, park the root as
   above, and cite the answer in a comment on the issue. On `Defer it` in any other case, move
   nothing, and say in your reply what you checked. On `Let it run`, move nothing, and take the
   deferral off that plan row with `dispatch doc-edit`, as step 4 of
   [Reconcile the weekly plan](#reconcile-the-weekly-plan) edits a row, so that the next
   prioritization pass asks nothing. In each case, reply under the ask with what you did, its
   first line exactly `Deferred dispatch://<root>`, `Not deferred dispatch://<root>` or
   `Let run dispatch://<root>`:
   `dispatch comment --issue <REPORT KEY> --reply-to-ask '<ask id>' --body-file -`.

The cost is the architect's work between the daemon's `todo` and your park.
