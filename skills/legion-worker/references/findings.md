# A finding outside your issue

Read this when you find a problem the issue you work does not cover: a defect in another
component, repository or tool, a gap in Legion itself, or a `fail` on a look of the production
check the project's `production_check` setting gives the tester. It holds for
every Legion role (each phase worker, each architect, the controller) and for each of their
subagents, and it replaces the filing step of `skill://dispatch-first` and `skill://dispatch`. The
controller edits no repository, so a defect it finds goes to its owner, as [Write to the
owner](#write-to-the-owner) says.

## Fix it yourself, else write to its owner, and ask only for a decision

A finding outside your issue never becomes a `dispatch issue` you file. Take the first of these
three steps that fits:

1. **Fix it yourself.** A defect with a clear fix is yours to fix, in whichever repository it is,
   with judgment: in your own pull request, or in a separate pull request of your tree. The fix
   needs no choice a person must make, and the evidence shows the defect. A phase worker whose own
   pull request cannot carry the fix sends it to its tree's architect, which adds it to an open
   pull request of the tree or files a child for it. Say in your handoff and in the pull request
   body what you fixed beside the issue's own work. A fix deferred to another issue is a fix
   nobody makes.
2. **Write to its owner.** A defect goes to another tree's architect only when your fix would
   collide with that tree's open pull request. A defect in a repository your token does not reach
   goes to the architect of the tree whose code it is. When no tree owns that code, it goes to the
   controller of that repository's project, which files it. After the merge, a production check
   has no pull request to fix in, so the tester writes each clear defect it finds to its owner.
   [Write to the owner](#write-to-the-owner) says how.
3. **Ask.** A defect goes to a person only when it needs a person's decision, a deploy window, or
   work much larger than your own. Any other finding that needs a person's authority, taste or
   risk appetite goes to a person too: whether the work is wanted at all, which of several
   designs, who owns it, or a change of scope, cost or risk. It is a `dispatch ask` with one
   recommendation, as [Where it goes](#where-it-goes) says.

A fix you are not sure of is not step 1: take step 2 when it holds, else step 3.

- A problem inside your issue is not such a finding: fix it in your change, since nothing needed
  for correctness is deferred.
- A problem inside your tree is work its root spec's acceptance needs. A phase worker's such
  problem outside its own issue goes to the tree's architect (`skill://legion-worker`, *Stop
  rules*), which adds it to an open pull request of the tree, or files a child when it is a
  separately deliverable change. Anything else, in the same
  repository or not, is a finding.
- A fault of Legion's own machinery that holds your phase is a finding too, never a request to
  another session to change your claim or your session: a reminder that comes after your phase
  ended, an update round that does not end, or a completion that `handoff_complete` cannot make.
  Its fix is in Legion's code, so take the three steps above. Tell your own tree's architect too
  where a stop rule of `skill://legion-worker` says so.
- Search first (`skill://dispatch-first`): when an open issue already tracks the problem, comment
  the evidence on that issue and send or ask nothing. When an open ask on the report issue already
  asks about it (`dispatch open-asks --project <PROJECT>` lists them), add your evidence under that
  ask (`dispatch comment --issue <REPORT KEY> --reply-to-ask '<ask id>' --body-file -`, the
  evidence on stdin) and open no second one.

These issues are filed:

- an architect's children of its own tree (`dispatch issue --parent`, `skill://legion-architect`)
- a Legion root the controller files for a clear defect no running tree owns
- an issue a person asked the controller to file, in a direct message, in a Slack request, or in
  their answer to an ask
- the report issue itself, which the controller creates once when the project has none

`skill://legion-controller`, "Findings, and the issues you file", and
`skill://legion-controller/references/daily-report.md` say how.

## Write to the owner

Write to the owner on Dispatch: a comment on the issue that owns the subject, which mentions the
owner's role. Dispatch delivers the mention and retries it, so you check no liveness first.

1. Find the issue that owns the subject: the issue of an open pull request on a `legion/<KEY>`
   branch that changes that code, or the open issue `dispatch search` finds for it.
2. Walk its `child_of` links up to the tree's root: `dispatch read --issue <KEY>` names each
   parent under `Links:`.
3. Post the evidence on the owning issue, and mention the root's architect:

   ```bash
   dispatch comment --issue '<owning KEY>' --mention 'role:legion-<project>-<root key, lower case>-architect' --body-file - <<'EOF'
   Clear defect from <your role token>
   <what went wrong, where and to whom, with the numbers; the issue it came from (dispatch://<KEY>); where the fix belongs, the repository and the component; the fix; why you do not make it yourself>
   EOF
   ```

   It prints `Posted comment <id>`. The role is the token `roleToken` builds
   (`packages/contracts/src/legion-roles.ts`): `<project>` is the Legion project that runs the
   tree, which for your own project is the word after `legion-` in your role token. So project
   `acme` and root `LEGION-41` give `role:legion-acme-legion-41-architect`.

When no tree owns that code, write to the controller of that repository's project instead. Post
the same comment on that project's report issue ([Where it goes](#where-it-goes)), with
`--mention 'role:legion-<project>-controller'`; your own project's controller is the one your
`Legion addressing` line names. A closed report issue takes a comment too. When the project has no
report issue, post the comment on your own issue. The controller files the defect for Legion and
replies under your comment with the issue it filed.

You file nothing, and you wait for nothing. An architect and its own tree's workers keep their
Envoy messages: a phase worker sends a fix its pull request cannot carry to its own tree's
architect with `envoy_publish`, as `skill://legion-worker` says. A production check's recorded
result names your comment, `dispatch://<KEY>/comment/<id>`, and the `fail` that records it closes
the check, so that look names no next look of its own.

## Where it goes

An ask goes on the controller's report issue whenever it is open: the issue the deployment
instructions name for Legion's reports, else the project's `Legion daily report`, which
`dispatch search --query '"Legion daily report"' --project <PROJECT>` finds. It stays open in
`icebox`, so the ask stays answerable whatever happens to the issue it came from; once an issue
closes, its close withdraws its open asks (a reopen brings them back), and Dispatch refuses their
answers (`409 ISSUE_CLOSED`). The question names the issue the finding came from by its
`dispatch://` reference.

When the project has no report issue yet, or a person closed it (`dispatch read` shows `Status:
done`, and Dispatch refuses the ask with `409 ISSUE_CLOSED`), open the ask yourself, on the first
of these that holds:

1. **Your own issue is open:** ask on your own issue, the evidence posted there as a comment
   first. Then publish the ask's reference to your tree's architect (`skill://legion-worker`,
   *Stop rules*). An architect holds its own. The architect holds your issue's
   acceptance until a controller has taken the ask (`skill://legion-architect`, "Acceptance and the
   merge"). That hold is what keeps the ask readable: an open ask on a closed issue drops out of
   the controller's own listing (`asks.go`'s `listOpenAsks` takes `i.closed_at is null`), so an
   issue that closed over one hides it for good.
2. **Your own issue is closed too** (Dispatch refuses the ask `409 ISSUE_CLOSED`, as on a
   production check): put the report issue back in reach yourself, with the `dispatch` commands
   below, and ask there. Never `legion({op: "set_status"})`, which the controller's report steps
   (`skill://legion-controller/references/daily-report.md`) use: that op is the controller's
   alone, and your session is refused `set_status is not
   available to a phase-worker session` (`packages/pi-legion/src/tools.ts`'s `OPERATIONS`). No
   other issue can hold an answerable ask, so this is the one case a role writes the report issue.
   A report issue holds no work, which is why it sits in `icebox` and why neither write below is
   the move the rule limits:
   move an issue to `backlog`, `icebox` or `triage` only when a person asked for that move, or when the work is done, folded into another issue or no longer needed, and cite that reason in a comment on the issue.
   - It exists and is closed: `dispatch issue-update --issue <REPORT KEY> --status icebox`. Send
     the status by itself. A closed issue takes a reopening status alone, and refuses it beside
     an assignee or any other field (`api/issue_patch.go`, the `before.ClosedAt` branch). This is
     the one lifecycle status your own prompt lets you write. The report issue is in no tree, so
     the daemon re-asserts nothing on it: `workflow`'s `sessionStatusWrite` acts only for a
     session that claims the issue's tree, or for an admitted root.
   - A search finds none: create it as this section names it, `dispatch issue --project
     <PROJECT> --title 'Legion daily report' --spec-file -`, with the Summary
     `skill://legion-controller/references/daily-report.md` gives, then park it with
     `dispatch issue-update --issue <NEW KEY> --status icebox`. It carries no `legion` label.
   - Then post the evidence there and ask there, as [What the ask says](#what-the-ask-says)
     spells it. Say in your summary that you reopened or created it.

Name the ask in your summary, and in the result a production check records. A look that must wait
for the answer reports `pending` with a `wake_at` of its own, the time you want to look again:
nothing else starts the next look.

## What the ask says

The question stands alone, since the ask view shows no comment. It holds at most 2,000 characters,
the `Ref: <reference>` line `--ref` appends included (about 75 characters for a comment's
reference). Its first line names who asks: `Finding from <your role token>`. The architect's
acceptance and the controller's catch-up find a finding ask by that line. Then it names the
concrete case before any general statement of it:

- what went wrong, where and to whom: the issue it came from (`dispatch://<KEY>`), the machine,
  file, command or session, and the numbers;
- where the fix would belong: the repository and component, and a plain statement that it is not
  Legion's when it is not. A problem is not Legion's because you work in Legion: name the
  component that misbehaved;
- the decision a person must make, what happens to whom under each option, and your
  recommendation and its reason.

Evidence the decision does not need (full command output, logs, every file and line, the pull
request) goes in a comment. Post it first as a `dispatch comment` on the issue the ask goes on,
which prints `Posted comment <id>`, and cite that comment with `--ref`, the one reference an ask
takes. That issue,
`<ASK KEY>` below, is the report issue, except as [Where it goes](#where-it-goes) says:

```bash
dispatch comment --issue '<ASK KEY>' --body-file - <<'EOF'
<the evidence: commands, output with its time, file:line, links>
EOF
# Keep `File it for Legion` only when your Legion addressing line lists the repository where the
# fix belongs.
dispatch ask --issue '<ASK KEY>' --ref 'dispatch://<ASK KEY>/comment/<id>' --question-file - \
  --option 'File an issue: <what it would track, and who would own it>' \
  --option 'File it for Legion: Legion works it once it is filed' \
  --option 'Do not file: <what stays as it is>' <<'EOF'
Finding from <your role token>
<what went wrong, where and to whom, with dispatch://<KEY> and the numbers; where the fix belongs, and that it is not Legion's when it is not; what each option does to whom; your recommendation and why>
EOF
```

Legion refuses to record an issue on a repository the project does not map, hence that condition.

## Tell the controller about the ask

The person answers in Dispatch, and the answer reaches only the ask's followers. So once the ask is
open, `envoy_publish` its reference, `dispatch://<REPORT KEY>/ask/<id>` (the id from the
`Asked <id> on …` line), to the controller topic your `Legion addressing` line names, with
`expects_reply: false`. The controller follows the ask and files what the person's answer asks for;
you file nothing, whatever the answer says. The controller itself skips this step, since it follows
each ask it opens. When the listener kept that publish rather than a holder taking it, do nothing
more for an ask on the report issue: the controller's catch-up follows it at its next start.

An ask on the report issue blocks nothing of your own issue: its work goes on. One on your own
issue holds its acceptance, as step 1 of [Where it goes](#where-it-goes) says.
