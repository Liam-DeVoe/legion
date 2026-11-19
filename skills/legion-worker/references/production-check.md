# The production check after the close

Part of `skill://legion-worker`. Read it when you work a look of a closed issue's production
check. The merge gate before it is `skill://legion-worker/references/merge-gate.md`. Every path
it cites is in cybermonklabs/legion.

- **The merge closes the issue; the production check is the work of the role the project names,
  after the close.** `projects.<KEY>.production_check` in the project's `legion.yaml` takes `off`
  or `tester`, and a project that names no key gets `tester`, so by default the tester works the
  check; the loader refuses every other value, naming the key and the value it read. Where the
  setting names the tester, the daemon reads that merge, writes `done` and hands the check task
  to the tester's live session. Where it is `off` nothing is opened at all — no record, no
  bound and no look — and the merged close is like any other done: every claim of the issue is
  retired and its pod released with the disk it ran on. A merged root still lingers its tree with
  every claim suspended, as before. On a root whose tree still runs, the merge is recorded and
  the root stays in `merging` on its merged pull request until the last other issue of its tree
  is done or parked, the check opening with that close. Its first line, which tells it from a
  phase task in a resumed session, is `Production check of <KEY>: <title>.`, naming the
  repository, the pull request, the merge commit, the look's number, its wake (`the merge`, for
  the first) and whether it is the last.
- **The check's record holds the role that works it, written when the check opened.** Changing the
  setting moves no check already open: a check keeps the role it opened with, and a check open
  before this release carries the implementer. Only that role may report on it, so a report from
  any other role is refused (`CHECK_ROLE_MISMATCH`). The check carries no phase and no handoff: a
  look ends with `handoff_complete` carrying a `verdict` and a `summary` and no commit, and the
  daemon refuses a report with no open check (`CHECK_NOT_OPEN`), on an ended look
  (`CHECK_LOOK_NOT_OPEN`) or with no verdict
  (`CHECK_VERDICT_REQUIRED`).
  One refusal is about the turn rather than the report: a completion sent from a turn that is not
  a look's own task is refused `CHECK_LOOK_NOT_SERVED` and records nothing, so report again in the
  turn of the look's task.
- **Each look does three things.** Do what the deployment instructions say to reach the deploy:
  when they name an agent or role for it, `envoy_publish` the merge commit to that role's topic
  with `expects_reply: false`, and await no reply. Read whether production carries the merge, and
  with it every requirement only the deploy can show, each as the requirement names it, since
  observing something else is never that requirement observed. Then
  report one of three outcomes:
  - `pass`, once you drove the changed path and those requirements in production through the
    user's own access path and
    recorded the result: `dispatch result` against the issue's `after_merge` check, at the merge
    commit, its `observed` ending `woken by <event>`, the printed line in the PR body's `Checks`
    block and one pull-request comment with the Legion footer. A closed issue takes a result for
    an `after_merge` or `default_branch` check, a comment and a message, and refuses every other
    write. A staging pass is not this: staging does not run every resource production does.
  - `pending`, when production does not carry the merge yet (a shared profile still holding the
    previous release, a daemon still running the previous commit, a deploy nobody has run), or a
    requirement's observation has not happened: the summary names what is missing and what you
    will look for next, `wake_at` names your next look (below), nothing is recorded, and the
    check's line in the body stays at
    `- Check <block id> (after_merge): not yet run`.
  - `fail`, for a requirement observed as not met, one still unobserved at the last look, or a
    defect: record the failing result with `dispatch result --verdict fail`, file no issue, and
    take the path `skill://legion-worker/references/findings.md` gives it — a clear fix to the
    controller topic, with its evidence comment on the controller's report issue; one needing a
    decision as a `dispatch ask` there, whose reference you publish to that topic. Either names
    this issue, the pull request, the merge commit, what you drove and what failed, and the
    result's `observed` and the summary name it (`dispatch://<REPORT KEY>/ask/<id>`).
- **Numbers the daemon holds are read with the `legion` tool.** Where the verdict rests on what a
  deployment's idle release has done, `read_release_records` answers a window of the daemon's own
  records of its releases, resumes and holds: its one argument `since` is the RFC 3339 instant
  the window starts at, and the tool's own description says what the answer carries. It reads
  them with this session's own claim, so there is no credential to fetch and none on disk.
- **You name your own next look, and nothing else wakes the check.** A `pending` on any look but
  the check's last carries `wake_at`, an RFC 3339 time: when you want to look again. Name a time
  you have a reason for — when the deploy you asked for should have run, when the next release
  lands — and say in the summary what you are waiting for, since that summary is what the next
  look's task tells you woke it. A `pending` with no `wake_at` on a look that is not the last is
  refused (`CHECK_WAKE_REQUIRED`): nothing is recorded, the look stays open, and the same report
  with a time is taken. A `wake_at` that no look follows is refused (`CHECK_WAKE_UNEXPECTED`): on
  a `pass`, on a `fail`, and on a `pending` on the last look, each of which ends the check. A time
  less than 60 seconds ahead becomes 60 seconds ahead, and a time at or after the check's own
  bound starts no look of its own, since the look at that bound already stands. No deploy run, no
  Dispatch `issue.deployed`, no Dispatch build and no daemon boot wakes a check: the daemon reads
  no deployment's production workflows at all, and your `wake_at` is the only thing that starts
  the look after this one.
- **Nothing waits in the session for a deploy.** A `pending` ends the look: the daemon stops the
  checking role's process, keeps its session and releases the issue's pod, whose Sandbox goes with
  its disk, and at the time you named a new pod comes back and your session resumes with the next
  look's task. So end the turn with the report: never poll, never wait on a reply, never ask for
  approval to deploy. Open a `dispatch ask` only where the instructions name nobody: the
  production gap and why it matters, then the required install or restart step, its risk, and
  outcome-named options; the look still ends `pending`.
- **The bounds.** A look is bounded at 2 hours, and a look nobody reported — its 2 hours, an
  operator's hold, or a claim that failed to start — starts the next look two hours later; on the
  last look it closes the check as not run. The check is bounded by the project's
  `production_check_hours` (336, 14 days, by default) from the close; the look that bound starts
  is the last, so told in its task
  (`the bound: production did not carry the merge within <N days>`). A `pass` or `fail` there
  closes the check as any look's would, and a requirement still unobserved there is that `fail`;
  only production not carrying the merge ends it `pending`, which carries no `wake_at` and
  closes the check as not run, written first on the pull request — its check line and a comment
  — because production did not carry the merge within the bound.
- **The record lives on the pull request and in the result.** Dispatch takes a comment, a message
  and a result for an `after_merge` or `default_branch` check on a closed issue, and refuses an
  ask there (`ISSUE_CLOSED`), so the deployment instructions' "on the pull request and the issue"
  is the pull request and that result, and a defect reaches Dispatch by the `fail` verdict's two
  paths alone — the message to the controller, which gives it to the tree that owns that code,
  and the ask on the controller's report issue. The architect accepted before the
  merge and is asked nothing after. `pass` or
  `fail` closes the check: your claim is retired and the issue's Sandbox goes with its pod and
  disk, for a root as for a child. Neither ending retires or suspends anything of an issue back in
  the workflow (a root reopened inside its linger): its claim and pod are the new run's.
