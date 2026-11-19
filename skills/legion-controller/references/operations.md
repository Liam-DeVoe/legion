# The operations of your own work

Read this before you change a claim's state, replace an issue's pod, restart your project's
daemon or re-run a CI run, and before you ask anyone to do one of them. Each operation here is
yours. Do it yourself, and ask no other session to do it. Each section says when to use the
operation, the exact command, and what to read to know that it worked.

The placeholders mean the same in every command:

- `<KEY>`: the issue key, such as `ACME-41`.
- `<owner>` and `<repo>`: the issue's repository on GitHub.
- `<pod>`: the name of a pod in `<namespace>`.
- `<namespace>`: the Kubernetes namespace your project's pods run in, which `legion.yaml` sets as
  `runtime.kubernetes.namespace` and which has no default. Read it from `read_state` with
  `settings: true`, at `settings.runtime.kubernetes.namespace`.
- `<daemon label>`: the label of your project's daemon pod, as `key=value`, which your deployment
  instructions give ([A restart of your project's daemon](#a-restart-of-your-projects-daemon)).
- `<id>`: the id of a GitHub Actions run, the number after `/actions/runs/` in its URL.
- `<role>`: the role of a claim: `architect`, `planner`, `implementer`, `tester`, `reviewer` or
  `merger`.
- `<REPORT KEY>`: the key of your report issue
  (`skill://legion-controller/references/daily-report.md#find-the-report-issue`).

## Before any `kubectl` operation

Make this read first, each time:

```sh
kubectl auth can-i delete pods -n <namespace>
```

It prints `yes` or `no`. Only a controller whose pod runs as its own ServiceAccount
(`runtime.kubernetes.pod.controller_service_account`) holds this right. A controller pod that
runs as the account of the issue pods does not.

Where the read prints `no`, or `kubectl` itself fails:

- Run no operation that needs that right. The restart of the daemon needs `delete pods`.
- Name the operation you lack in the day's report, with the read and its answer
  (`skill://legion-controller/references/daily-report.md`).
- Ask no other session to do it.

The claim operations, the replacement of an issue's pod and the re-run of a CI run need no
`kubectl`.

## A claim's state

**When.** A claim of your project needs action: a worker that stalled or went silent, a worker
that holds its task and does not run it, a session that a provider keeps refusing, a claim that a
wake gives you (`provider-errors`, an escalated hold, `worker-died`), or a claim that a person asks
you to act on. A tree's architect holds the same six operations for the claims of the issues it
owns, but not for its own claim (`skill://legion-architect/SKILL.md#escalation-judgment`): a
sub-architect's operation on its tree's root or on a sibling's subtree is refused
`403 ISSUE_NOT_OWNED`. A tree's architect claim is yours.

**The command.** `legion({ op: "<op>", issue: "<KEY>", role: "<role>" })`, with `<op>` one of
the six below. Each refuses your own claim (role `controller`).

| `op` | What it does |
| --- | --- |
| `suspend` | Stops the claim's agent, and keeps its session and the task it owes. A claim in a turn keeps that turn until the turn ends, or until the stop timeout cuts it off. No rule of the daemon's starts the claim again until your `resume` or the operator's. The tree's architect cannot lift your suspension: its operations on that claim are refused `SUSPENDED_BY_CONTROLLER`. |
| `resume` | Starts a suspended claim again in the session it recorded, and sends it the task it owes. It also lifts an architect's suspension. It is refused for a claim the operator's suspension holds, landed or held for a turn (`SUSPENDED_BY_OPERATOR`), and for a claim stored working (`CLAIM_IN_A_TURN`). Its start waits its turn in the daemon's wake pacing (`worker_wakes_per_minute`). |
| `stop` | Ends the claim. The daemon stops its agent at once, also in a turn, which loses that turn. It retires the claim and keeps its session on record. Only `retry`, or a start of its issue's phase, runs the claim again. It is refused for a tree's root architect, which ends only with its tree: suspend that claim instead. |
| `retry` | Starts a claim that failed, or that a `stop` retired, again in the session it recorded, with new launch budgets. A phase worker starts through its issue's current phase, so it gets that phase's work. It is refused for a claim the operator's suspension holds, landed or held for a turn (`SUSPENDED_BY_OPERATOR`), for a claim stored working (`CLAIM_IN_A_TURN`) and for a suspended claim: `resume` that claim. Its start waits its turn in the daemon's wake pacing. |
| `compact` | Has Oh My Pi write a summary of the session. The claim keeps its session, its workspace and what it knows of its issue, and gets the task it owes again. Use it first for a session that a provider keeps refusing. It is refused for a claim that runs no agent (`CLAIM_NOT_RUNNING`): use `fresh_session` there. |
| `fresh_session` | Leaves the claim's session behind and starts the claim in a new session, which loses everything the old session held. Use it for a claim that no process stands behind, and for a claim that a provider still refuses after a `compact`. Its start waits its turn in the daemon's wake pacing. |

`skill://legion-controller/references/wakes.md`, in its `provider-errors` section, says more about
`compact` and `fresh_session`.

**What to read.** The answer of `stop`, `retry`, `suspend` and `resume` is the claim's state after
the operation, with who holds a suspension over it: `suspendedBy` while the claim is suspended,
and `suspensionHeld` with `suspensionHeldBy` while a suspension waits for a turn to end. The
answer of `compact` says whether the session was compacted, and why not when it was not. The claim
is back either way. The answer of `fresh_session` is empty when the claim came back, and
`phaseNotRestarted` says why nothing restarted the claim's phase. Then read the claim in
`read_state`.

**A refusal** changes nothing of the claim, but for the two of `fresh_session` named below. It says
what the claim does now and which operation reaches each state. It names no time to come back:
ask again later, and read the claim in `read_state`. Under the operator's own suspension, landed
or held for a turn, `stop`, `retry`, `suspend`, `resume` and `fresh_session` are refused
`409 SUSPENDED_BY_OPERATOR` before anything else of the claim is read. `compact` is refused there
because the claim runs no agent (`CLAIM_NOT_RUNNING`), or because a suspension is held over it
(`CLAIM_IN_A_TURN` or `CLAIM_UNSETTLED`). A `fresh_session` can also meet the operator's
suspension after its own suspension of the claim ran. Its `SUSPENDED_BY_OPERATOR` refusal then
says that it `went no further than its own suspension of the claim, which that suspension then
took over`: the claim is then suspended under the operator's suspension, with the session it
recorded. A `fresh_session` of a claim it found suspended can also meet the operator's suspension
at its resume, after it left the recorded session behind. Its refusal then says that it `went no
further than leaving behind the session the claim recorded`: the claim is then suspended under the
operator's suspension with no session to resume, and starts in a new session once it is started.
[A claim the operator holds](#a-claim-the-operator-holds) says what to do then.

**The wake pacing.** The start that your `resume`, `retry` or `fresh_session` raises waits its
turn under `worker_wakes_per_minute`, as the start an architect asks for does, so a controller
that lifts many holds after a deploy does not start every claim in the same minute. Two kinds of
start skip the pacing. One is the operator's own: its resume, its retry, its spawn of a claim, and
the start of a suspended claim that a task it hands that claim raises (`exemptWake` in
`packages/daemon/internal/supervise/wake.go`, `deliverResuming` in `table.go`). The other is
every start of your own claim, which the pacing lets through whoever raises it (`reserveWake`).
So while a wave runs, a claim you started can take minutes to come up: ask for nothing again, and
read the claim in `read_state` at a later turn.

## A restart of your project's daemon

**When.** A person asks for it, your deployment instructions name the case, or a change that your
work waits on reaches the daemon only at its boot, such as a Secret or a configuration file that
an apply changed. A restart starts a wave of wakes, so never restart the daemon to reach one
claim: use [the claim operations](#a-claims-state). Restart nothing while a wave runs already.
Your `read_state` shows a wave in two reads, though it gives no list of the starts that wait
(`stateSummary` and `readState` in `packages/pi-legion/src/tools.ts`). In its summary, the claims
of several issues read `queued` or `launching`: the starts of a restart or of a refresh, which wait
their turn. In the full records (`read_state` with `phase` or `issues`), the claims of several
issues carry an `updateRound`: a wave of pod-update rounds. The summary's `project` line also
names when the daemon last started (`started`, which is `daemon.startedAt`).

**The commands.** `<daemon label>` is the label of your project's daemon pod, as `key=value`,
that your deployment instructions give. cybermonklabs/legion ships no daemon Deployment, so no
label is the same in every deployment. When your instructions give none, delete nothing, and name
the operation you lack in the day's report.

1. Read the daemon's pod:

   ```sh
   kubectl -n <namespace> get pods -l '<daemon label>' -o wide
   ```

   It lists one pod. Write down its name. When it lists no pod, or more than one, delete nothing,
   and name that in the day's report.

2. Delete the pod:

   ```sh
   kubectl -n <namespace> delete pod -l '<daemon label>'
   ```

   It prints `pod "<pod>" deleted`. The daemon's Deployment then creates a new pod. The new pod
   waits until the old pod has gone, because the daemon's state volume attaches to one pod at a
   time.

3. Make the read of step 1 again one minute after the delete, and again until five minutes have
   passed or a new pod reads `Running`.

**What to read.** The restart worked when the read lists one pod whose name is not the name you
wrote down, whose `STATUS` is `Running`, and whose `READY` shows each container ready (`n/n`).
Then `read_state` answers with a `daemon.startedAt` after the delete. When no new pod reads
`Running` five minutes after the delete, open a finding ask on the report issue
(`skill://legion-worker/references/findings.md`) that names the pod and the status that the read
shows, because a daemon that does not start is the operator's to fix.

Publish to no role topic while the restart runs. A message starts a turn of each session it
reaches (`triggerTurn` in `packages/pi-envoy/extensions/envoy.ts`), and the daemon's wake pacing
counts no such turn, so it adds to the wave that the pacing holds under the provider's per-minute
cap (`worker_wakes_per_minute`).

## A replacement of an issue's pod

**When.** An issue pod of your project must go, and the daemon does not replace it by itself: a
person asks for it, or your deployment instructions name the case. Never replace a pod to reach
one claim: use [the claim operations](#a-claims-state).

The pod's disk goes with the pod, and so does every commit and edit of the issue's workspace that
no push carried. So you delete no issue pod or Sandbox with `kubectl`. You have no exec, attach or
port-forward into a pod, because inside a pod you would reach what its launcher alone holds. The
daemon's update round is the one way you remove an issue pod: it asks each role in the pod to
push its work and to answer `ready_for_update`, as it does when a pod drifts from its
configuration.

**The command.** `legion({ op: "replace_pod", issue: "<KEY>" })`. The daemon opens the update
round on that issue's pod. Where a round is open on that pod already, the answer names that
round, and the daemon opens no second one. An answer that names a round means a claim of the
issue records it. A refusal opens nothing, and its sentence says what to do:

- `NO_CLAIM`: the issue is not one of your project's, or the daemon runs no claim of it. There
  is no pod of it to replace.
- `NO_POD`: no role of the issue runs in a pod now. Ask again once one does, if the pod must
  still go.
- `POD_CHANGING` or `POD_UPDATE_LIMITED`: a replacement is in flight, or a limit of the project
  holds the round back, and the sentence names the limit. Ask again at a later wake.
- `POD_UPDATE_NOT_RECORDED`: no claim recorded the round within the wait, and its post may still
  land. Read `updateRound` on the issue's claims before you ask again.

Ask no other session to remove the pod.

**What to read.** The answer names the round, its reasons and whether this call opened it. Then
`read_state` with `issues: ["<KEY>"]` shows the round as `updateRound` on each claim of the pod,
and, once a role has answered, the head it pushed as that claim's `updateHead`, which is the
role's word and nothing verifies. The daemon replaces the pod once every role has answered and
no turn runs, or at the round's bound, `pod_update_grace_minutes` after the round opened,
whatever the roles answered. Work that a role did not push by then is lost with the disk, as in
any update round. The round is done when no claim of the issue shows `updateRound`. A round that
does not end at its bound is told as an `update-stalled` wake: to the architect that owns a claim
an architect's suspension holds, and to you for every other hold
(`skill://legion-controller/references/wakes.md`).

## A re-run of a CI run

**When.** Re-run only the failed jobs of a run, never a whole run. Re-run them only for an
infrastructure failure that the failed job's log shows: a lost runner, or a network error. Never
re-run to test a theory. Never start a run for any other reason on a head that a run tested
already with the same inputs: that verdict exists, so read it and its logs. These rules are the
`developing-ci` skill's, section 7. Read the failed job's log first:

```sh
gh run view <id> --log-failed --repo <owner>/<repo>
```

A test that failed and passed on the same code is flaky. That is no reason for a re-run: it goes
to the tree whose code it is, by a mention
(`skill://legion-worker/references/findings.md#write-to-the-owner`), to be fixed at its cause.
A tree's architect holds the same command for its own tree's runs
(`skill://legion-architect/references/acceptance.md#a-re-run-of-your-trees-ci-run`).

**The command.** One run, with the implement App token that the secrets broker lends, narrowed to
the repository:

```sh
agent-secrets LEGION_IMPLEMENT_APP --reason '<the run and why>' --github-owner <owner> --github-repo <repo> --github-token -- sh -c 'printf "Authorization: Bearer %s\n" "$LEGION_IMPLEMENT_APP" | curl -fsS -X POST -H @- https://api.github.com/repos/<owner>/<repo>/actions/runs/<id>/rerun-failed-jobs'
```

Name in `--reason` the run, the infrastructure failure that its log shows, and the issue key it is
for. The flags are necessary. `--github-owner` and `--github-repo` narrow the token to the one
repository. `--github-token` gives the token alone: without it, the name holds the JSON object of
installations, and GitHub answers `401`. Without the flags, the name held 1,440 characters that
started with `{`. With them, it held an installation token of 390 characters. Measured on
2026-11-15: 401 without the flags at 14:02:51Z, 201 with them at 14:02:40Z on run 43695263178 of
cybermonklabs/legion. Your own `gh` holds a read token only, so it cannot re-run a run. The single
quotes keep `$LEGION_IMPLEMENT_APP` for the child shell, and `-H @-` gives `curl` the header on
standard input, so the token is in no command's arguments, as `skill://agent-secrets` says.

**What to read.** After the grant, `agent-secrets` writes one line,
`agent-secrets: LEGION_IMPLEMENT_APP from <source>`, and the exit status is then `curl`'s. `curl`
exits 0 when GitHub answers `201`. Any other answer exits 22, with
`curl: (22) The requested URL returned error: <code>`. Then read the run:
`gh run view <id> --repo <owner>/<repo> --json attempt,status`. The re-run started when `attempt`
is one more than before and `status` is `queued` or `in_progress`. Without the grant line, the
status is the client's own, and 77 is a denial. Name a denial in the day's report, and ask no
other session to re-run the run.

## A claim the operator holds

**When.** The daemon refuses an operation of yours on the claim with `409 SUSPENDED_BY_OPERATOR`.
Its message begins `<claim> is held by the operator's own suspension, which only the operator
lifts`. That refusal is the one read that names the operator as the holder. `read_state` names no
holder: every suspended claim reads `suspended` there, whoever suspended it. And the answer of a
`stop`, `retry`, `suspend` or `resume` that the daemon takes never names the operator, because the
daemon refuses each of them on a claim the operator's suspension holds
(`ClaimOperationResponse` and `claimDoor` in `packages/daemon/internal/api/`). Your `stop`,
`retry`, `suspend`, `resume` and `fresh_session` are refused there. Only the operator lifts the
operator's suspension (`legion claims resume`).

**The command.** Open a `dispatch ask` on the report issue. It names the claim, quotes the
refusal, and says why the claim needs your operation, `<op>`:

```bash
dispatch ask --issue '<REPORT KEY>' --question-file - \
  --option 'Resume it: the operator runs legion claims resume on the claim, and I then run <op> on it' \
  --option 'Keep it held: <what stays as it is>' <<'EOF'
The operator's suspension holds the <role> claim of <KEY>
The daemon refused my <op> of <KEY>'s <role> claim with 409 SUSPENDED_BY_OPERATOR: "<the refusal's message>". <Why the claim needs the operation, with the numbers.> Only the operator lifts this hold. I recommend <option>, because <reason>.
EOF
```

Send no Envoy message to the session that holds the operator's token, and ask no other session to
lift the hold. The ask reaches the person who answers for the project. You follow each ask you
open, so its answer reaches you.

**What to read.** `dispatch ask` prints `Asked <ask id> on <REPORT KEY>`. Name the ask's
reference, `dispatch://<REPORT KEY>/ask/<ask id>`, in your summary, and in the day's report while
the ask is open. When the answer is `Resume it`, run `<op>` again on the turn it reaches you: the
daemon's answer is then the read that shows whether the hold is gone. A second
`409 SUSPENDED_BY_OPERATOR` means the operator has not resumed the claim yet: reply under the ask
with that refusal, the text on stdin
(`dispatch comment --issue '<REPORT KEY>' --reply-to-ask '<ask id>' --body-file -`), and run
`<op>` again at the next reply. When the answer is `Keep it held`, run nothing.

## Each kind of request the controller sent

On 2026-11-13, the controllers of one deployment sent other sessions requests of thirteen kinds,
as a labeller of that day's transcripts counted them. Each row says what you do now in place of
such a request.
An operation is one of the sections above. A decision that only a person may make is a
`dispatch ask`. Every other request goes to the session whose work it is, as a Dispatch comment
that mentions its role (`skill://legion-worker/references/findings.md#write-to-the-owner`). Send
it to no other session.

| Kind | What you do now |
| --- | --- |
| `change a claim or a session` | [A claim's state](#a-claims-state): the six `legion` tool operations. Under the operator's suspension: [A claim the operator holds](#a-claim-the-operator-holds). An issue pod that must go: [A replacement of an issue's pod](#a-replacement-of-an-issues-pod). |
| `change a Dispatch record` | Your own `dispatch` commands (a comment, an ask, an artifact, the fields of an issue other than its status) and `set_status`. A lifecycle status stays the daemon's. |
| `merge or queue a pull request` | Not yours: you merge and enqueue nothing. The architect of the tree that owns the pull request enqueues it, after it reads `merge-holds-md` (`skill://legion-architect/references/acceptance.md#a-pull-request-no-merger-merges`). Write to that architect by a mention. A merge that only a person may schedule is a `dispatch ask`. |
| `act on the cluster, cloud or CI` | [A restart of your project's daemon](#a-restart-of-your-projects-daemon), [A re-run of a CI run](#a-re-run-of-a-ci-run), [A replacement of an issue's pod](#a-replacement-of-an-issues-pod), and the reads of your project's pods and Sandboxes in `<namespace>`. Any other act, such as a quota, a pin or a Pulumi value, is a change of code. It goes by a mention to the architect of the tree whose code it is, or it is a Legion root you file when no tree owns that code (`skill://legion-controller/references/finding-asks.md#file-a-clear-defect-no-tree-owns`). An apply window that only a person may set is a `dispatch ask`. |
| `do or change work` | No operation: it is the recipient's own work. Write to its owner by a mention. |
| `investigate and report` | No operation: it is the recipient's own work. Read and measure what you can reach yourself. Write the rest to its owner by a mention. |
| `review or verify` | No operation: it is the recipient's own work. Write to its owner by a mention. |
| `decide or answer` | No operation: it is the recipient's own answer. Write to its owner by a mention. A decision that only a person may make is a `dispatch ask`. |
| `tell when or report status` | No operation: it is the recipient's own answer. Read what `read_state` or Dispatch holds yourself. Write the rest to its owner by a mention. |
| `hold or stop` | No operation. A merge hold is a line of `merge-holds-md` (`skill://legion-controller/SKILL.md#merge-holds`), and you send a hold to no tree. Any other hold is the recipient's own work. |
| `passes on what another agent sent` | No operation: send no relay. The sender writes to the owner itself. |
| `a copy only so that it knows` | No operation: send no copy. |
| `not a request` | Nothing is owed. |
