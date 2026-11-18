# The operations of your own work

Read this before you change a claim's state, restart your project's daemon, delete a pod or
re-run a CI run, and before you ask anyone to do one of them. Each operation here is yours. Do it
yourself, and ask no other session to do it. Each section says when to use the operation, the
exact command, and what to read to know that it worked.

The placeholders mean the same in every command:

- `<KEY>`: the issue key, such as `ACME-41`. `<key>` is the same key in lower case.
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

Make these two reads first, each time:

```sh
kubectl auth can-i create pods/exec -n <namespace>
kubectl auth can-i delete pods -n <namespace>
```

Each prints `yes` or `no`. Only a controller whose pod runs as its own ServiceAccount
(`runtime.kubernetes.pod.controller_service_account`) holds these rights. A controller pod that
runs as the account of the issue pods holds neither.

Where either read prints `no`, or `kubectl` itself fails:

- Run no operation that needs that right. The restart of the daemon and the pod delete need
  `delete pods`. The pod delete also needs `create pods/exec`.
- Name the operation you lack in the day's report, with the read and its answer
  (`skill://legion-controller/references/daily-report.md`).
- Ask no other session to do it.

The claim operations and the re-run of a CI run need no `kubectl`.

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

## A pod delete with nothing lost

**When.** An issue pod of your project must go, and the daemon does not replace it by itself: a
person asks for it, or your deployment instructions name the case. The pod's disk goes with the
pod, and so does every commit and edit of the issue's workspace that no push carried. So carry
that work to the issue branch first, then delete the pod. Never delete a pod to reach one claim:
use [the claim operations](#a-claims-state).

The procedure is the route of `docs/kubernetes.md` in cybermonklabs/legion, "Carrying unpushed
work out of an issue's pod", with one change: step 2 runs `carry-out.sh` in your own pod, in a
namespace of its own, where the docs run the same script in a throwaway container. Your pod has
no Docker. Run no `git`, `jj` or `omp` in the issue pod, and bring no credential into it: a
`kubectl exec -c roles` shell runs as user 1000 with every role's credentials in reach, and the
clone's configuration is every role's to write, so a program it names would run there.

Find the pod. `<pod>` is the name this read lists:

```sh
kubectl -n <namespace> get pods -l legion.dev/issue=<KEY> --field-selector=status.phase=Running
```

The issue's agents run while you copy. Work they write after step 1 goes with the pod.

1. **Copy the clone and the workspace out of the pod with `tar` alone.** The first archive holds
   the clone's refs, worktree records and objects. The second holds the workspace's files, with no
   `.git` and no `.jj`. Each command refuses a directory reached through a symlink. An exit of 1
   after `file changed as we read it` is no failure, because the agents still write:

   ```sh
   kubectl -n <namespace> exec <pod> -c roles -- sh -c 'set -e; cd -P "$1/.git"
     test "$(pwd -P)" = "$1/.git"
     tar --hard-dereference -cf - refs worktrees $(test -f packed-refs && echo packed-refs) objects
     ' sh /legion/repos/github.com/<owner>/<repo> > "$TMPDIR/<KEY>-git.tar"
   kubectl -n <namespace> exec <pod> -c roles -- sh -c 'set -e; cd -P "$1"; test "$(pwd -P)" = "$1"
     tar --hard-dereference --ignore-case --exclude=.git --exclude=.jj --exclude=./.codegraph \
     -cf - .' sh /legion/workspaces/<owner>/<repo>/<key> > "$TMPDIR/<KEY>-tree.tar"
   ```

2. **Rebuild the work into a bundle in your own pod.** `carry-out.sh` runs itself in a user,
   network, mount and PID namespace of its own, with a tmpfs over `/run` and over `$HOME`, and
   refuses to run outside it. It checks the type of each entry of both archives. It refuses an
   entry of the tree through a `.git` or `.jj` directory, at any depth and in any case, and an
   entry under a symlink of the tree. It refuses a symlink whose relative target, walked from the
   link's own directory and on through each link of the tree it meets, climbs above the tree's
   root, loops, or passes through more than 40 links. An in-tree link that climbs and comes back
   down, as a Bun workspace's `node_modules` holds, passes.

   An absolute target, such as a virtualenv's `bin/python -> /usr/bin/python3`, passes, and ends
   the walk of any link that reaches it. It carries no write out of the tree: no entry under a
   link is unpacked, tar replaces a name it meets again and does not write through it, and
   `git add` stores a link as a link and does not follow it.

   The tree archive holds ignored files too. Step 1 leaves out only `.git`, `.jj` and
   `.codegraph`, so `node_modules` and a `.venv` are in it, and the script checks them. The
   repository's `.gitignore` keeps them out of the rebuilt commit. So a link that git never adds
   can still refuse a pod, but only as an entry under a link or by the walk of its relative target.

   It then rebuilds the work under an empty environment, and writes a bundle of the branch
   `rescue-<KEY>`: the commits origin lacks, and the edits no commit holds as one more commit. In a
   pod of the worker image, the plugin holds the script at
   `/opt/legion/pi-legion/dist/skills/legion-controller/scripts/carry-out.sh`, the same file as
   `skill://legion-controller/scripts/carry-out.sh`:

   ```sh
   sh /opt/legion/pi-legion/dist/skills/legion-controller/scripts/carry-out.sh <KEY> /legion/workspaces/<owner>/<repo>/<key> "$TMPDIR/<KEY>-git.tar" "$TMPDIR/<KEY>-tree.tar" "$TMPDIR/<KEY>.bundle"
   ```

   Read its output:

   - It exits 0: the bundle is written. The lines of `git status --short` are the edits that the
     last commit of `rescue-<KEY>` now holds.
   - Each commit under `unpushed commits of other branches, not in the bundle:` is work that no
     push of the issue branch carries. Delete nothing, and tell the issue's architect by a mention
     that names those commits.
   - git's `Refusing to create empty bundle`, with no edit and no such commit: nothing below the
     working copy is unpushed. Go to step 4.
   - A `refused:` line that names `unshare` or the namespace: your pod cannot run the rebuild.
     Delete nothing, and name the operation you lack in the day's report.
   - Any other `refused:` line: the agents may have written while tar read. Make step 1 again,
     then step 2 again. A second refusal means delete nothing, and tell the issue's architect,
     naming the line.
   - Any other exit that is not 0, with no `refused:` line: a `git` step of the rebuild failed,
     such as an `update-ref`, a `fetch` or a `reset` that exits 128 on an object the agents were
     writing while tar read it. Make step 1 again, then step 2 again. A second such exit means
     delete nothing, and tell the issue's architect, naming the exit status and git's last line.

3. **Push the issue branch, outside the namespace.** Run it through the worker image's `git`, the
   wrapper first on your `PATH`, so Legion's leak guard checks the push. Take the implement App
   token as [A re-run of a CI run](#a-re-run-of-a-ci-run) takes it, narrowed to the repository.
   First fetch the issue branch and the default branch from GitHub into a fresh clone, then the
   bundle, and read what the bundle adds:

   ```sh
   s=$(mktemp -d -p "$TMPDIR") && echo "$s" && git -C "$s" init -q
   agent-secrets LEGION_IMPLEMENT_APP --reason '<KEY>: fetch legion/<KEY> to carry the unpushed work of its pod before the delete' --github-owner <owner> --github-repo <repo> --github-token -- sh -c 'GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=http.extraheader GIT_CONFIG_VALUE_0="Authorization: basic $(printf "x-access-token:%s" "$LEGION_IMPLEMENT_APP" | base64 -w0)" git -C "$1" fetch -q https://github.com/<owner>/<repo>.git "refs/heads/legion/<KEY>:refs/remotes/origin/legion/<KEY>" "HEAD:refs/remotes/origin/default"' sh "$s"
   git -C "$s" -c fetch.fsckObjects=true fetch -q "$TMPDIR/<KEY>.bundle" "rescue-<KEY>:refs/remotes/rescue/<KEY>"
   git -C "$s" log --stat --oneline "origin/legion/<KEY>..rescue/<KEY>"
   ```

   The fetch from GitHub worked when it exits 0 after the
   `agent-secrets: LEGION_IMPLEMENT_APP from <source>` line. Any other exit leaves nothing to read
   the bundle against, so delete nothing. GitHub's
   `fatal: couldn't find remote ref refs/heads/legion/<KEY>` is an issue branch no role ever
   pushed, and another line is a refused token or a read that did not answer: tell the issue's
   architect by a mention that names git's first line. Without the grant line, the status is the
   client's own, and 77 is a denial: name it in the day's report, and ask no other session to make
   the fetch.

   `fetch.fsckObjects` has git check each object of the bundle before it stores any, as
   `transfer.fsckObjects` checks a fetch from a remote. A malformed object refuses the fetch, with
   `fatal: fsck error in packed object`. With git 2.47.3 on 2026-11-18, a bundle that held a commit
   with a bad time zone fetched with no refusal without the setting, and with it was refused
   `badTimezone: invalid author/committer line - bad time zone`.

   The log lists the commits the push adds. Then push them, with no force:

   ```sh
   agent-secrets LEGION_IMPLEMENT_APP --reason '<KEY>: push the unpushed work of its pod to legion/<KEY> before the delete' --github-owner <owner> --github-repo <repo> --github-token -- sh -c 'GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=http.extraheader GIT_CONFIG_VALUE_0="Authorization: basic $(printf "x-access-token:%s" "$LEGION_IMPLEMENT_APP" | base64 -w0)" git -C "$1" push https://github.com/<owner>/<repo>.git "refs/remotes/rescue/<KEY>:refs/heads/legion/<KEY>"' sh "$s"
   ```

   The token reaches git in its environment, never in its arguments. The push worked when it
   prints `<old>..<new>  rescue/<KEY> -> legion/<KEY>` and exits 0, and when
   `gh api repos/<owner>/<repo>/git/ref/heads/legion/<KEY> --jq .object.sha` prints the commit
   that `git -C "$s" rev-parse refs/remotes/rescue/<KEY>` prints. A refused push means delete
   nothing, and tell the issue's architect by a mention that names the refusal's first line and
   the commits of the log. GitHub refuses anything but a fast-forward of the branch, and the App's
   push of a change under `.github/workflows/` unless the App holds `workflows: write`. A fetch of
   the bundle that names a missing commit, such as one force-pushed away on GitHub, or that fsck
   refuses, means the same. Never push with force, and never push any branch but `legion/<KEY>`.

4. **Only then, delete the pod**, and remove the scratch directory and the three files by their
   exact paths (`$s` is the path step 3 printed):

   ```sh
   kubectl -n <namespace> delete pod <pod>
   rm -rf -- "$s" "$TMPDIR/<KEY>-git.tar" "$TMPDIR/<KEY>-tree.tar" "$TMPDIR/<KEY>.bundle"
   ```

   The delete prints `pod "<pod>" deleted`. The pod read above lists `<pod>` no more.

**The residual risk.** The rebuild runs in your own pod, not in a throwaway container. A crafted
object in the archives that exploits a bug of git or tar runs inside the namespace in your pod.
There it has no network, and it sees neither `/run`, which holds the credential files under
`/var/run/legion` and `/var/run/secrets`, nor `$HOME`. It sees `$TMPDIR`. Step 3 runs outside the
namespace: it fetches the bundle, whose objects came from the pod, and pushes them, with the
network and your pod's credentials in reach. Its `fetch.fsckObjects` check is the one check of
those objects there, so a crafted object that exploits a bug of git's that this check does not
catch runs with your pod's rights.

The bundle can also carry what a role's own push would carry. An absolute link that `git add -A`
takes, tracked or new and named by no `.gitignore`, can reach the bundle. So can a file that a
`.gitignore` would have ignored when that `.gitignore` is itself a symlink, because git does not
read a `.gitignore` that is a symlink. Read the log's `--stat` in step 3 before you push. With
GNU tar 1.35 and git 2.47.3, tar replaced a repeated name and did not write through the link,
`git add -A` staged an absolute link as mode 120000, and git did not read a symlinked
`.gitignore`.

**Proved** on 2026-11-15 on cybermonklabs/legion#4065's own branch, from that issue's own pod
on its own workspace, so step 1 ran with `sh -c` in place of `kubectl exec`. Step 1 wrote a git
archive of 525,516,800 bytes and a tree archive of 1,590,466,560 bytes, holding 3,884 links,
between 15:40:38Z and 15:42:47Z. Step 2's `carry-out.sh` exited 0 at 15:48:37Z with a bundle of
25,895 bytes, no edit left out of a commit and no other branch's commit. Step 3, not yet with
`fetch.fsckObjects`, fetched, logged the two commits `1e4bff5d` and `b6ba8a07`, and pushed
`c0bdcc984..b6ba8a07d  rescue/<KEY> -> legion/<KEY>` at 15:51:03Z. GitHub's
branch then named `b6ba8a07`, the bundle's tip. The first run of step 2 at 15:02Z
had refused the same workspace on its Bun links, under an earlier symlink rule that this
section no longer has. No run has yet made step 1 through `kubectl exec` from a controller's own
pod.

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
as the request-kind labeller counted them in dispatch://INFRA-2709/artifact/request-kinds-md. Each
row says what you do now in place of such a request.
An operation is one of the sections above. A decision that only a person may make is a
`dispatch ask`. Every other request goes to the session whose work it is, as a Dispatch comment
that mentions its role (`skill://legion-worker/references/findings.md#write-to-the-owner`). Send
it to no other session.

| Kind | What you do now |
| --- | --- |
| `change a claim or a session` | [A claim's state](#a-claims-state): the six `legion` tool operations. Under the operator's suspension: [A claim the operator holds](#a-claim-the-operator-holds). A pod that must go: [A pod delete with nothing lost](#a-pod-delete-with-nothing-lost). |
| `change a Dispatch record` | Your own `dispatch` commands (a comment, an ask, an artifact, the fields of an issue other than its status) and `set_status`. A lifecycle status stays the daemon's. |
| `merge or queue a pull request` | Not yours: you merge and enqueue nothing. The architect of the tree that owns the pull request enqueues it, after it reads `merge-holds-md` (`skill://legion-architect/references/acceptance.md#a-pull-request-no-merger-merges`). Write to that architect by a mention. A merge that only a person may schedule is a `dispatch ask`. |
| `act on the cluster, cloud or CI` | [A restart of your project's daemon](#a-restart-of-your-projects-daemon), [A re-run of a CI run](#a-re-run-of-a-ci-run), [A pod delete with nothing lost](#a-pod-delete-with-nothing-lost), and the reads of your project's pods and Sandboxes in `legion`. Any other act, such as a quota, a pin or a Pulumi value, is a change of code. It goes by a mention to the architect of the tree whose code it is, or it is a Legion root you file when no tree owns that code (`skill://legion-controller/references/finding-asks.md#file-a-clear-defect-no-tree-owns`). An apply window that only a person may set is a `dispatch ask`. |
| `do or change work` | No operation: it is the recipient's own work. Write to its owner by a mention. |
| `investigate and report` | No operation: it is the recipient's own work. Read and measure what you can reach yourself. Write the rest to its owner by a mention. |
| `review or verify` | No operation: it is the recipient's own work. Write to its owner by a mention. |
| `decide or answer` | No operation: it is the recipient's own answer. Write to its owner by a mention. A decision that only a person may make is a `dispatch ask`. |
| `tell when or report status` | No operation: it is the recipient's own answer. Read what `read_state` or Dispatch holds yourself. Write the rest to its owner by a mention. |
| `hold or stop` | No operation. A merge hold is a line of `merge-holds-md` (`skill://legion-controller/SKILL.md#merge-holds`), and you send a hold to no tree. Any other hold is the recipient's own work. |
| `passes on what another agent sent` | No operation: send no relay. The sender writes to the owner itself. |
| `a copy only so that it knows` | No operation: send no copy. |
| `not a request` | Nothing is owed. |
