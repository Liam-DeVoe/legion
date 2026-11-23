---
title: Replaying a recorded window
description: The developer's tool legion replay, which collects a recorded window of a Legion daemon, replays it under a slot rule you name, and reports what each result measures and assumes.
sidebar:
  order: 4.5
tableOfContents:
  maxHeadingLevel: 2
---

`legion replay` is a tool for a developer who changes how Legion gives out its admission slots.
It replays a recorded window of one project's daemon through the daemon's own admission pass, under a slot rule that you name, on a simulated clock.
It then reports what that rule costs: the issues finished, the started work that waits for a slot, the priority that holders take, and the other delays.
It also reads the same measures from the record of a window, with no replay.

The tool writes nothing to a running daemon, to Dispatch or to GitHub.
`collect` and `collect-merges` read Dispatch and GitHub with `GET` requests alone.
Each replay makes a database of its own on a Postgres server that you name, and drops it at the end.

This page uses the words of [Admission and the `legion` label](/legion/legion/concepts/#admission-and-the-legion-label).
The [CLI reference](/legion/legion/reference/cli/) gives each command and flag as its `--help` prints it.

## The words this page uses

- **Slot**: one of the cap's admission slots.
  An issue that the daemon started holds one in each phase that the slot rule keeps a slot in.

- **Holder**: an issue that holds a slot.

- **The line**: the waiting line, `admission.waiting` in `legion state --json`.
  Its returned part holds the issues whose work came back, and its starting part holds the issues that have not started.

- **Returned issue**: an issue of the returned part of the line.

- **Wait**: a started issue's wait on another issue, an unfinished Dispatch `blocked_by` link or an open child.

- **Slot rule**: the code that decides who holds a slot and how the line gives the slots out.
  It decides which phases keep a slot, which waits give it back, and whether a wait stops the phase under way.
  It also decides the order of the line, what a start may take from the cap, and what a head that conflicts with its base does.

- **Recorded window**: a span of one project's daemon, with the daemon's log lines and the Dispatch events of the project's issues.

- **The record**: what a recorded window shows that the daemon did, under the rule that it ran.

- **Branch**: one value of each knob that the record does not give, such as the time at which an architect takes a link off.

- **Work segment**: one visit of an issue to a phase, from the visit's worker start to the recorded move that ends it.
  The worker start is the later of the move into the phase and the slot that the issue takes for it.

- **Stretch**: one visit of an issue to the phases from testing to merging, in which its head can conflict with its base.

- **Send-back loop**: a move back to an earlier phase, and the work until the issue enters the phase that it left again.

## What a recorded window is

A recorded window is a directory.
`legion replay collect` writes it, and every other command reads it.

| File | What it holds |
| --- | --- |
| `window.json` | The project, the span (`from`, `to`), the reach (`before`), the cap from each change on, the release, commit and rule of each part of the span, the deployment's settings, the kinds of line that the log holds, the collector's commit, and the sha256 of each input file. |
| `log.jsonl` | The daemon's lines in the reach and the span, one JSON object each, as the daemon writes them on standard error: `time`, `level`, `msg` and the attributes. |
| `events/<KEY>.jsonl` | Each issue's Dispatch events, oldest first, as Dispatch's issue event log serves them. |
| `merges/<owner>__<repo>.jsonl` | The first-parent commits of each base branch, which the conflict draw reads. |

The tool reads these kinds of daemon line:

- the `admission:` lines of each slot given out or taken back, and of each wait

- the `workflow:` lines of each phase move, design gate, hold and handoff

- the `supervise:` lines of each launch, and of the start and the end of each turn

- the `pause:` lines of the project's pause and resume

- the lines that a daemon process writes as it starts

From this release, the daemon also writes one `admission: a pass moved slots` line for each admission pass that gives out or takes back a slot.
That line gives the slots in use and the cap.
It also gives each slot given out or taken back, with its issue and reason, and each issue of the line that the pass went past, with its reason.
The tool reads that line where a window holds it, and builds the same slot facts from the other lines where it does not.

The reach is the span from `--from` less `--before` to `--from`.
No reading of a daemon's state exists for a past moment, so the tool rebuilds the state at `--from` from the lines and events of the reach.
Each run gives the number of issues that it could not place at `--from`, and names each one with the reason.

Every command reads a window only if each input file has the sha256 that `window.json` gives for it.
A window that holds an input file that `window.json` does not name is refused too.

## Collect a window

1. Get the daemon's log lines for the reach and the span.

2. Run `legion replay collect`.

3. Run `legion replay collect-merges` once for each base branch.

4. For test data that this repository keeps, run `legion replay neutralize`.

### Get the daemon's log lines

`collect` reads each file that `--log` names, and each file under a directory that `--log` names, gzipped or not.
It reads the three forms below, and you can give it files of more than one form.
It keeps a line that two files hold once.
It keeps only the kinds of line that the tool reads, so you need not filter the lines first.

#### From the daemon's standard error

The daemon logs JSON lines to standard error.
Where you start it yourself, keep that output in a file:

```sh
legion start --config legion.yaml 2>>daemon.log
```

Where a process supervisor runs the daemon, give `collect` the file in which the supervisor keeps the daemon's standard error.

#### From `kubectl logs`

```sh
kubectl -n <namespace> logs <pod> --timestamps > daemon.log
kubectl -n <namespace> logs <pod> --previous --timestamps > daemon-previous.log
```

`collect` removes the time that `--timestamps` writes, and the `[pod/<pod>/<container>]` that `--prefix` writes, before each line.
`kubectl logs` gives only the log file that the node keeps now for the pod that runs now, and `--previous` gives the container that ran before it.
On a busy daemon, that file can hold only the last minutes, so read the time of its first line.
For a window of days, use the log store of your cluster.

#### From a cloud log export

`collect` reads an export of log entries, each with `timestamp` and `jsonPayload`, as JSON lines or as one JSON array.
It reads the daemon's line from `jsonPayload`, and ignores the other members of the entry.
For example, with Google Cloud Logging:

```sh
gcloud logging read '<daemon filter> AND timestamp>="2026-09-28T00:00:00Z" AND timestamp<"2026-10-08T00:00:00Z"' \
  --project=<project> --order=asc --format=json \
  | jq '[.[] | {timestamp, jsonPayload}]' > daemon-export.json
```

`<daemon filter>` selects the daemon's own container, for example `resource.type="k8s_container" AND resource.labels.namespace_name="<namespace>" AND resource.labels.container_name="<container>"`.
The `jq` step keeps only the two members that `collect` reads, so the file holds no name of your cluster.
A log store keeps lines for a limited time, 30 days by default in Cloud Logging, so collect a window before the oldest day of its reach expires.

### Run `collect`

`collect` reads from Dispatch the events of each issue that the lines name.
It also reads the events of each blocker, parent and child that those events name, in any project.
It reads each issue from its first event up to `--to`.
Set `DISPATCH_URL` to your Dispatch server.
The token comes from the file that `--dispatch-token-file` names, else from the file that `DISPATCH_TOKEN_FILE` names, else from `DISPATCH_TOKEN`.
`collect` never prints the token.

```sh
export DISPATCH_URL=https://<host>.internal.example
legion replay collect \
  --project ACME \
  --from 2026-10-05T00:00:00Z --to 2026-10-08T00:00:00Z --before 168h \
  --log daemon-export.json --log daemon.log \
  --dispatch-token-file <token file> \
  --part 2026-10-05T00:00:00Z,2026-10-08T00:00:00Z,<release>,<commit>,main \
  --out windows/acme-2026-10-05
```

It prints the number of daemon lines that it read and that it kept, the lines that it read twice, the lines that were no daemon line, and the issues whose events it read.
It names each issue that Dispatch does not have.

| Flag | What it gives |
| --- | --- |
| `--project <KEY>` | The Dispatch project key of the daemon that wrote the lines (required). |
| `--from <time>`, `--to <time>` | The window's start and end, in RFC 3339 (required). |
| `--before <duration>` | How far before `--from` the inputs reach, so that the tool can rebuild the state at `--from` (required). |
| `--log <file or directory>` | A file, or a directory of files, of the daemon's lines, given once for each (required). |
| `--out <dir>` | The directory that the window goes to, which must be missing or empty (required). |
| `--dispatch-token-file <file>` | The file that holds the Dispatch token. |
| `--concurrency <n>` | How many issues `collect` reads from Dispatch at once (8 by default). |
| `--part <from>,<to>,<release>,<commit>,<rule>` | One part of the span, with the release, the commit and the slot rule that the daemon ran in it, given once for each part. |
| `--collector <commit>` | The commit of cybermonklabs/legion that this `collect` runs from, where the binary carries no commit of its own. |
| `--tick-interval`, `--gate-steps`, `--linger`, `--review-round-cap`, `--stalled-tree-notice`, `--worker-silence`, `--worker-background` | The deployment's `controller_wake_interval_seconds`, `gates.design`, `linger_hours`, `review_round_cap`, `stalled_tree_notice_hours`, `worker_silence_minutes` and `worker_background_hours`, each at the daemon's own default where you give none. |

- Make `--before` as long as the lines that you hold allow.
  If the reach starts while slots are in use and no line names their holders, the tool infers the holders from their phases (assumption 34).

- Give one `--part` for each release that the daemon ran in the window, with the name of its rule as `legion replay rules` prints it.
  `scorecard` reads the record under the rule of each moment.
  `validate` and `forecast` with `--from-runs` read a window's rule from its parts where you name none.

- Give each setting of the deployment that differs from its default, so that a replay decides with the values that the record was made under.

### Add the merges of each base branch

The conflict draw reads how fast each base branch moved.
`collect-merges` reads a base branch's first-parent history from GitHub's commit listing, with `GET` alone.
It writes the history into the window as `merges/<owner>__<name>.jsonl`, and writes the file's new sha256 into `window.json`.

```sh
legion replay collect-merges \
  --repo <owner>/<repo> --branch main \
  --from 2026-09-28T00:00:00Z --to 2026-10-08T00:00:00Z \
  --github-token-env GITHUB_TOKEN \
  --out windows/acme-2026-10-05
```

Run it once for each base branch that the window's pull requests name.
Give it at least the window's span.
A span that starts at the start of the reach also covers each stretch that began before `--from`.
Each commit keeps the pull request that it merged, where the first line of its message ends with GitHub's squash suffix ` (#<n>)`.
The draw reads that number to leave a stretch's own merge out of its count.

| Flag | What it gives |
| --- | --- |
| `--repo <owner/name>` | The repository (required). |
| `--branch <name>` | The base branch (required). |
| `--from <time>`, `--to <time>` | The span, in RFC 3339 (required). |
| `--github-token-env <VAR>` | The environment variable that holds the GitHub token, which `collect-merges` never prints (required). |
| `--github-api <url>` | GitHub's REST API (`https://api.github.com` by default). |
| `--out <dir>` | The directory of the window that `collect` wrote (required). |

### Neutralize a window for test data

This repository is public, so a window that it keeps as test data holds no private name.
`legion replay neutralize` writes a copy of a window with each private name renamed, the same name to the same neutral name in every file.
It renames each issue key, and each claim token that spells one.
It renames each repository but cybermonklabs/legion, each actor's id, each session's id and title, and each person's login and email.
The title of a Legion role's session keeps its role and takes the neutral key of its issue, so the trace reads the same claim from the neutral window.
It renames each host name and machine name, each IP address but a loopback, unspecified or documentation address, and each uuid, link, label and digest.
It also renames the free text of Dispatch's events.
It keeps every member, JSON type and list length, and the daemon's msgs, ranks, statuses and times.
The window's own project becomes `--project`, and each other project becomes `OTHERA`, `OTHERB` and so on.

```sh
legion replay neutralize \
  --in windows/acme-2026-10-05 \
  --out packages/daemon/internal/replay/testdata/<name> \
  --project ACME \
  --map <dir outside the repository>/names.json \
  --deny <organization>
```

`--map` receives the map of every name that `neutralize` replaced.
That file holds every private name, so keep it outside the repository and never commit it.
`neutralize` refuses a `--map` inside `--out`, and one that exists already.
`--deny` names a word that no file may hold in any case, such as an organization's name.
`neutralize` replaces that word wherever it stands, and fails if one is left.
Before you commit the neutral window, search it for each private name that you know.

| Flag | What it gives |
| --- | --- |
| `--in <dir>` | The window (required). |
| `--out <dir>` | The directory that the neutral window goes to, missing or empty (required). |
| `--project <KEY>` | The key that the window's own project becomes (`ACME` by default). |
| `--map <file>` | The file that the map of names goes to, outside `--out` (required). |
| `--deny <word>` | A word that no file of the neutral window may hold, in any case, given once for each word. |

## Run a replay

Build a `legion` binary from this repository:

```sh
go -C packages/daemon build -o <dir>/legion ./cmd/legion
```

`run`, `validate`, `forecast` and `compare` need a Postgres server on which the user of `--pg-dsn` can create a database.
Each run makes a database of its own there, from `template1` with the daemon's migrations, and drops it at the end.
`run --keep-db` keeps the database of its run.
No run writes to the database that `--pg-dsn` names.
Use a test server.
`scorecard` and `rules` need no database.

A comparison of rules takes these steps:

1. Read the scorecard of each window from its record (`scorecard`).

2. Check the replay of each window under the rule that its daemon ran (`validate`).

3. Check a forecast across a recorded change of the rule (`forecast`).

4. Compare the rules on two windows (`compare`).

Keep the window before a change of the rule held out: record the commit of the tool before its first replay of that window.
If the tool fails on that window, fix the tool and check it again on a third window.

### `legion replay rules`

`legion replay rules` prints every slot rule that a replay can run, the daemon's own first, with its commit and each of its choices.
`--json` prints the rules as JSON.
[The slot rules](#the-slot-rules) gives its output.

### `legion replay run`

```sh
legion replay run \
  --window windows/acme-2026-10-05 \
  --rule keep-past-review \
  --pg-dsn 'postgres://<user>@127.0.0.1:5432/postgres?sslmode=disable' \
  --out runs/acme-keep-past-review
```

`run` replays the window through the daemon's own handler chain: the Dispatch mirror, the workflow engine and the admission pass.
It enters each Dispatch event, each recorded phase move, each tick and each boot at its recorded time or offset.
Most Dispatch events that the daemon's own session wrote are its outbox's status writes, which the replay's own outbox makes again, so the replay leaves those events out.
The daemon's API also writes statuses under that session for an agent, and no outbox row writes these: a move to backlog, icebox or triage, a move from one of those or from done to todo that adds no `legion` label, and the controller's todo on a root whose phase is held.
The controller's `set_status`, an architect's `release_children` and `rerun_child` and, in releases that have it, an architect's `park_child` write such moves, and the replay enters each one as recorded.
The replay knows the controller's todo on a held root by the record's release of that hold, whose reason names a todo.
An event that a worker's session wrote inside a work segment of that worker's role on that worker's issue enters at its offset in that segment, so it moves with that work.
The record names that session by an id under the title `Legion <role> · <KEY>`, which names the role and the issue of the claim that the session runs.
Where a window's events name a worker's session by its claim token instead, the replay reads the claim from that token.
A worker's event outside every work segment of its role is part of no segment, so it enters as an event that no worker wrote enters: at its recorded time, or at its offset from the replay's start of the wait that it ended.
A root's architect writes such events between the phases of its root, for example when it files a child while its root is in implementing.
Where the replay does not run the segment of an event, because it passes over that segment or ends its work before the event, the event enters in the same way, at once where its recorded time has passed.
Where `answer` is `offset`, a person's answer to a worker's question enters at its offset from the moment that the question entered the replay, wherever the question entered, so no answer comes before its question.
Where a drawn conflict loop takes the place of the issue's own recorded loop, each event of the issue's own loop enters at its offset in the drawn loop's segment of the same role and phase, or at the end of that segment where the segment is shorter, and an event of a role and phase that the drawn loop does not hold enters as an event that no worker wrote.
So the replay enters each recorded event once.
After each fact, a worker stub runs the outbox rows that the daemon's lanes would take.
Each claim that the replay writes runs the session that the record shows that claim running at that moment.
A claim runs a fresh session after a fresh start of its agent, so at each launch the claim takes the latest session whose first event comes at or before that moment, or else the first session after it.
A claim that the record shows no session for runs a session of the replay's own.
So the daemon's reads that compare a session with the sessions of the claims answer as they did in the record: who holds a waiting root's Dispatch claim, who opened an ask, and who wrote a status.
The start state writes the claim of each turn under way at the window's start.
It also writes the claim of the architect of each root that waits on a person at the start, between that architect's turns, where the record shows the architect's session.
The daemon keeps that claim through the wait, and at each pass it keeps the root only while the session that holds the root's Dispatch claim has a claim in the root's tree.
The start state's copy of Dispatch holds each issue's Dispatch claim as the newest event before the window's start that shows a claim shows it, as the record's daemon held it then.
The replay's copy of Dispatch also takes the claim of each `issue.claimed` and `issue.released` event, so a later boot or tick reads the claim that Dispatch showed.
Each work segment runs its recorded length times `f^((n − m)/42)`, where n is the number of issues in flight in the replay at the segment's start, and m the number in the record.
Each stretch draws, with `--seed`, whether its head conflicts with its base, from a model that the run fits on the stretches that begin and end inside the window.
A stretch that began in the reach is not in the fit, because the reach can be of a release that recorded a conflict as another move back.
Under a rule whose conflicting head moves back, an issue whose stretch draws a conflict runs a recorded conflict loop, drawn with `--seed` among the loops of the window whose conflict left the same phase.
Where the window records no loop from that phase, the replay draws among the loops that left a later phase, and cuts the drawn loop at its first work segment in that phase or a later one, so the issue returns to the phase that its conflict left.
Where the window records only loops that left an earlier phase, the replay draws among those, and after the loop the issue goes back to the phase that its conflict left through copies of its own last work segments in each phase between.
Where the issue's own work holds no segment in the phase that such a loop returns to, the issue runs no drawn loop and goes on with its own recorded work.
The replay draws only among the loops whose work, as the drawn conflict takes it, the record shows ending.
A loop whose issue the record shows still in it at `--to`, or leaving the workflow inside it, has no known length, and an issue given it would stand in its last work segment to the window's end and never run its own work after the loop.
A loop that left a later phase counts where the record shows it reaching the phase that the drawn conflict left.
A stretch whose recorded conflict loop runs to `--to`, with no return to the phase that the conflict left, does not draw: it keeps that conflict, and the issue runs its own loop where the daemon moves it back (assumption 35).
The replay runs the daemon of the commit that it was built from, which answers a conflict past the review as cybermonklabs/legion#4979 has it, and leaves that conflict to the merger (assumption 37).
In accepting and retro, the daemon records the head as conflicting and asks nobody, under every rule, because neither the architect's acceptance nor retro needs a head that merges.
The issue keeps its phase and goes on with its work: past the loop that the record ran, where the record ended the stretch in a conflict, and otherwise by the move that the record shows.
When the issue moves into merging, the daemon asks the merger to merge the base forward, and the replay adds a merge-forward segment of the merger's before the issue's merging work, as long as the implementing round that it drew when the conflict came (assumption 23).
A move back to a phase before accepting ends such a conflict before any merger is asked, and the work goes on from a head of its own, as after a conflict's move back.
In merging, under a rule whose conflicting head keeps its phase, the daemon asks the merger to merge the base forward at once, and the merge-forward segment is the merger's.
A rule whose conflicting head moves back still moves the issue back to implementing from merging, from testing and from reviewing.
In testing and reviewing, under a rule whose conflicting head keeps its phase, the daemon asks the implementer, as before.
`run` asks nothing outside: no Dispatch, no GitHub and no NATS.

The daemon records what each holder of a gated tree's work took of its approved design, and its outbox tells a holder what changed when a person approves a later version.
A window recorded under a release before that record holds none of it, so the start state writes it as the daemon's upgrade to that release writes it: each issue whose work has started took the versions that its tree's gate stands approved at, and so did the architect of each tree whose gate opened.
So the news of a replay is of what a person approves inside the window.
The worker stub runs each reconcile of that news (`design_changes`) as the daemon's outbox runs it, with the replay's copy of Dispatch in place of Dispatch.
That copy holds the versions of each document and the verdicts on them, but not their text.
So it answers which version a person approved before another, and the stub records what changed between two versions as a change that Dispatch could not read, but for a first approval, which names no earlier version.
A merger's start that waits for that record waits on its backoff, as the daemon's does, and starts at its first try after the reconcile, which waits on no read of Dispatch.
The stub finishes each start that the reconcile queues for news alone under assumption 26, once the claim it reaches holds no task, as the daemon's claim takes such a task.
The news that such a start or an entry's start carries counts as taken by its holder at that start, since the replay's workers take each task at their start (assumption 30).
Nothing that moves an issue's work reads what a holder took, but the merger's start, which waits only until the news is recorded.

| Flag | What it gives |
| --- | --- |
| `--window <dir>` | The window (required). |
| `--rule <name>` | The slot rule, as `legion replay rules` names it (required). |
| `--pg-dsn <dsn>` | The Postgres server that the run makes its database on (required). |
| `--out <dir>` | The directory of the run's output (required). |
| `--branch <name>` | The branch, `main` or `main+<knob>=<value>` ([The branches](#the-branches)). |
| `--seed <n>` | The seed of every draw (1 by default). |
| `--slowdown <f>` | The factor f for each 42 issues in flight above the record's number (1 by default). |
| `--keep-db` | Keep the run's database after the run. |
| `--trace-segments` | Write each work segment's issue, phase, start, n, m, recorded length and replayed length to `segments.jsonl`. |

`run` prints the window, the rule, the branch, the seed, f and the database.
It prints each move that the daemon's code refused, with the reason, and counts what the replay could not enter as recorded, the recorded conflicts that it kept for a loop that runs to `--to`, and the conflicts past the review that it left to the merger.
It prints the scenario of the work of an issue that never started, the conflict model with the window that it was fitted on and its intervals, and the widest gap between the run's two scorecards.
Last, it prints each assumption that the run rests on, with the number of outbox rows that it finished under each.

`--out` receives these files:

| File | What it holds |
| --- | --- |
| `log.jsonl` | The daemon's lines on the simulated clock. |
| `scorecard.json` | The measures read from the states that the daemon's own admission passes left, the snapshot timeline. |
| `scorecard-line.json` | The same measures read back from `log.jsonl` and the replay's Dispatch events, as the measures of a record are read, the line timeline. |
| `run.json` | What ran, what it counted, the gap of each measure between the two timelines, and the conflict model. |
| `segments.jsonl` | Each work segment, with `--trace-segments` alone. |

A replay's scorecard names the assumptions from 1 to 25 that its measures rest on.
`run.json` and the output of `run` also name the tool's own assumptions, from 26 on.
The gap between the two timelines is the error of reading a record from its lines, which the scorecard of every record rests on.
The daemon writes the line of an issue's wait for its open children once for each generation of its tree.
So a line timeline reads that wait again, with no line, where the issue's last open child closed and another opened before its own first phase started.
The start state marks each such wait that the record told in its tree's current run before the window's start, so the replay's daemon does not tell it again.
A line timeline reads a design gate from the daemon's lines of its openings and closings, so an approval of another document of the issue opens nothing.
An issue that waits for a slot at the window's start began that wait where the record shows, in both timelines, since the start state is the record's.
The merge queue is GitHub's, so no code that the replay runs writes the line of a pull request that leaves it.
Just before the replay enters a recorded move back out of merging that such a line came before in the record, it writes that line to `log.jsonl` itself, so both timelines name the loop `queue_drop`.
The replay's own pull request record of an issue carries the number that a line of the record names for the issue's pull request, where no other issue's record takes that number in the same repository.
An issue that no line gives such a number takes its key's number where no other issue's record takes it in that repository, and otherwise a number above every other there.
GitHub numbers the pull requests of each repository, and the daemon finds a pull request by its repository, compared without case, and its number, so no two records of a run share a number in one repository.

A run is a function of its window, its rule, its branch, its seed and f.
Two runs of the same window, rule, branch, seed and f write the same `log.jsonl`, the same two scorecards and the same `run.json`, byte for byte.
The one exception is the fields of `run.json` that measure or name the run itself: `wallSeconds`, `meanPassMillis`, `peakMemoryBytes` and `database`.

### `legion replay scorecard`

```sh
legion replay scorecard --window windows/acme-2026-10-05
```

`scorecard` reads every measure of a window from its record alone, by the code that reads a replay's measures, with no replay and no database.
It prints one measure on each line as `<name> <value>`, and then each issue's first slot and done.
`--json` prints the scorecard as JSON.

### `legion replay validate`

```sh
legion replay validate \
  --window windows/acme-2026-10-05 --rule main \
  --pg-dsn '<dsn>' --out runs --parallel 4
```

`validate` replays the window under the rule that its daemon ran, on every branch, each at f = 1, 1.2 and 1.6.
A branch that draws, a conflict or the work of an issue that never started, runs the seeds 1 to 5.
A branch that draws nothing runs the seed 1 alone.
Up to `--parallel` runs run at once, each in a database of its own.
Each run's files go under `--out` in `<window>/<rule>/<branch>@<f>/seed-<n>/`, and a run whose files are there already does not run again.
`validate` deletes each run's log once it has read it, unless `--keep-logs`.

It prints, for each branch and each measure, the recorded value, the replayed value, the gap, the allowance and the test.
The replayed value is the mean of the seeds, with the values of the lowest and the highest seed.
It prints each issue's error in its first slot and in its done, with their median and 90th percentile.
It reads the record and each replay from their line timelines, so that it compares like with like.
It prints whether the window's main branch, `main` at f = 1, passes, and which branches give no comparison.
The allowances are:

- 10% of the issues finished, and of the slots given out on each day

- 0.05 of the priority taken by holders

- 60 minutes at the median of the issues' errors in their first slot

- 30% of every other measure

A branch that misses an allowance gives no comparison.
A window whose main branch misses one gives no comparison at all.
`--from-runs <dir>` reads the validation from the runs that a `validate` or a `compare` wrote, and runs nothing.
`--json` prints the validation as JSON.

### `legion replay forecast`

```sh
legion replay forecast \
  --before windows/acme-2026-09-20 --after windows/acme-2026-10-05 \
  --rule main --pg-dsn '<dsn>' --out runs --parallel 4
```

`forecast` replays the window recorded before a change of the daemon's rule under the rule after the change, on the main branch.
It compares that forecast with the record of the window after the change.
It compares rates per hour, averages over time and shares, never totals, because the two windows differ in length and in work.
Each number passes within twice its allowance, because a forecast carries the model's error twice: once for the window and once for the rule.
A send-back cause that the change of the workflow defines again is scored only inside the total of all send-back loops.
Those causes are red checks or a conflict, a conflict, and a send-back by the architect or the merger.
Each cause that both releases define the same way is scored apart.
The forecast also names the workflow commits between the two windows' daemons, which it carries beside the rule.
With `--from-runs`, `--before` and `--after` name the windows by the names of their directories.
`--json`, `--parallel` and `--keep-logs` work as in `validate`.

### `legion replay compare`

```sh
legion replay compare \
  --window windows/acme-2026-09-20 --window windows/acme-2026-10-05 \
  --pg-dsn '<dsn>' --runs runs --out comparison.md --parallel 8
```

`compare` compares the slot rules on two windows, and writes the result as one Markdown document.
Give the window before the change of the rule first.
It compares each `--rule`, or every rule that the repository keeps where you give none, and adds `main` and each window's own rule where you leave them out.
Under each rule, it replays each window on every branch, and on the main branch at each f from 1 to 2 in steps of 0.1 for the break-even search.

The document gives:

- each window's scorecard, read from its record

- each window's validation under its own rule, with the branches that give no comparison

- the forecast across the change, with the workflow commits that it carries too

- which window measures how a rule changes conflicts

- the value of every measure for every rule, window and branch, with the lowest and the highest seed

- the gap of each measure between the snapshot and the line timelines

- each rule's break-even factor: the f at which its rank against `main` changes, or that no f up to 2 changes it

- the rule that it names best, or that no rule is best, with each rule's trade

- the assumptions and the scenario that each run used

A rule is best when it is no worse than `main` on each of four numbers by more than that number's allowance, and better on at least one.
That must be true in every branch of both windows that passes the validation.
The four numbers are the issues finished, the hours of started work that waits only for a slot, the priority taken by holders, and the hours of the other delays.

To split the work, run each part with `--only-window <name>` and `--only-rule <name>` into one `--runs` directory, with no `--out`.
Then write the document from the parts' directories with `--from-runs <dir>... --out <file>`, which runs nothing.
`validate`, `forecast` and `compare` print on standard error, as each run ends, its facts, passes, seconds and peak memory, so that you can size a job.

## The measures

Every scorecard gives these measures over the window, from `--from` to `--to`.
A replay reads them from its own run, and `legion replay scorecard` reads them from the record, by the same code.
So a difference between the two is a difference of the replay, not of two readings.

An average is the integral of a count over the window, divided by the window's length.
`hours` is that integral in issue-hours, and `max` is the largest count at any moment.
Each name below is the name that `scorecard` prints and that a scorecard's JSON holds.

### Issues finished

- `done`: the issues whose move to done falls in the window, each once.
  `doneAt[<KEY>]` gives the time of each.

- `workLeft`: the issues in flight at `--to`.
  An issue that reached done and then reopened counts in both.

### Started work that waits for a slot

The tool gives two readings side by side, and the issues past their review apart.
Each has an `.average`, a `.max` and `.hours`.

- `waitOnlyForSlot` is reading (a): the returned issues that hold no slot and that no wait holds back.
  It counts an issue from the end of its wait, or from the owed start of an issue that waits on nothing, to its slot.
  `legion state` calls it `wait only for a slot`.

- `startedWithoutSlot` is reading (b): every returned issue that holds no slot, its waits on other issues included.
  It counts an issue whose start is owed, an answered root, and an issue that gave its slot back for a wait, until the pass that gives it a slot.
  Under `cover`, a covered waiter counts too.
  `legion state` calls it `in all`.

- `pastReviewHolding` and `pastReviewNotHolding`: the issues in accepting, retro or merging, or held from one of them, with a slot and without one.

Three more numbers describe the slots given out:

- `slotsGivenByDay[<day>]`: the slots given out on each UTC day, which are each promotion, each child's first phase, each slot that a returned issue takes, and each cover.

- `medianSlotWaitMinutes`: the median, over the slots given out, of the minutes from the moment that the issue which takes a slot began to wait for one to the slot.

- `firstSlot[<KEY>]`: each issue's first slot in the window.

### Priority taken by holders

`priorityTaken` reads, at each moment, every pair of a holder and an issue that waits for a slot.
It is the share of those pairs in which the holder ranks below the waiting issue, as an average over the window.
An issue that waits for a slot is a member of the line, in either part, that holds no slot, that no wait holds back, and that `admission.blocked` and `admission.held` do not name.
The rank is the line's own order: `record.RankLess` over the Dispatch rank of each issue's tree root, and then over the issue's own rank.
At a moment with no pair, the share is 0.

### Other delays

- `loops[<cause>]`: the `.rounds` and `.hours` of the send-back loops of each cause.
  Each move back to an earlier phase is one round.
  Its hours run from the move back to the issue's next entry into the phase that it left, or to `--to`.

- `waits[<word>]`: the `.count` and `.hours` of the waits on other issues, by what ended each.
  A wait runs while an issue in flight, before its review, waits on an unfinished `blocked_by` link, or, at admitted under an open design gate, on an open child.
  A child at admitted that has not started has no wait here: its wait is the line's.
  A wait goes on while the root of its tree lingers.

| Cause | The move back |
| --- | --- |
| `review_changes` | A review that asks for changes, from reviewing back to implementing. |
| `send_back` | A send-back by the architect or the merger, a move back out of accepting, retro or merging that no drop from the merge queue came before. |
| `conflict` | A head that conflicts with its base, moved back to implementing, or kept in its phase and merged forward. |
| `red_checks_or_conflict` | Red checks or a conflict, which the release at 9542c6fb did not tell apart. |
| `test_failed` | A failed test, from testing back to implementing. |
| `queue_drop` | A move back out of merging after the issue's pull request left the merge queue. |
| `other_backward` | A worker's own move back before the review, out of planning, implementing, testing or reviewing. |

| Word | What ended the wait |
| --- | --- |
| `blocker_done` | The last blocker reached done. |
| `link_removed` | Someone took the last unfinished link off. |
| `child_done` | The last open child closed as done or icebox. |
| `child_moved_out` | The last open child moved from under the issue. |
| `gate_closed` | A new spec version closed the design gate. |
| `moved_on` | The waiting issue moved past its review, or out of the phase its open children held. |
| `left_workflow` | The waiting issue left the workflow. |
| `open` | Nothing, because the issue still waits at `--to`. |

### Slots

- `slotHoursInTurn` and `slotHoursNoTurn`: the slot-hours held while a worker of the holder, of any role, is in a turn, and the rest.
  Each slot counts from the pass that gives it to its release, or to `--to`.

- `inFlight`: the issues in flight, with `.average`, `.max` and `.hours`.
  An issue is in flight from the pass that first gives it a slot to its move to done or out of the workflow, and again from a reopened issue's next start.
  An issue that gave its slot back stays in flight.

### What else a scorecard holds

- `unplaced`: the issues that the start state could not place.

- `firstDayApart`: the same measures over the window's first 24 hours alone, for a window longer than a day (assumption 6).

- `rule`, `branch`, `seed` and `slowdown`: what a replay ran.
  A record's scorecard carries none of them.

- `assumptions`: the numbers of the assumptions from 1 to 25 that the measures of a replay rest on.
  A record's scorecard names none, because a record rests on no assumption of a replay.

- `scenario`: the work that the run gave an issue that never started, `none`, `drawn` or `drawn-done`.

## The slot rules

A slot rule is code in the daemon, not a plugin, and a new candidate is a change to the daemon's code.
The daemon runs `main`, and a replay runs the rule that you name through the same admission pass.

| Rule | What it is | Commit |
| --- | --- | --- |
| `main` | The daemon's own rule, with no slot past the review, the slot given back on a link wait and a children wait, returned issues first, and a conflicting head that keeps its phase. | `0d3786da` |
| `legion-v20.42.5` | The rule of releases 20.25.1 and 20.42.5, with the slot choices of `main` and a conflicting head that moves back to implementing. | `958173da` |
| `legion-v19.57.3` | The rule of release 19.57.3, with a slot kept in every phase until the merger's ready mark in merging, no slot given back on a wait, and red checks that move back to implementing. | `9542c6fb` |
| `keep-past-review` | `main` with the slot kept in accepting, retro and merging, so that a send-back past the review finds its issue with its slot. | `0d3786da` |
| `blockers-first` | `main` with each issue that a started issue waits on placed first among the issues that have not started. | `0d3786da` |
| `returned-by-rank` | `main` with a returned issue placed in the line by its board rank, not by the time at which it gave its slot back. | `0d3786da` |
| `cover` | A slot kept until the work is done, and an issue that a waiter waits on started on the waiter's slot, one cover for each holder. | `0d3786da` |
| `wait-grace` | `main` with a wait on another issue that gives the slot back only after five minutes, for the reason that a deployment reported: a started issue whose `blocked_by` link was set and taken off within a minute lost its slot and stood last of 79 in the line's returned part. | `0d3786da` |
| `returned-by-first-slot` | `main` with a returned issue placed in the line by its first slot in its current generation, not by the time at which its work came back, for the reason that a deployment reported: a started issue sent back from merging for one forward merge stood last of 76 in the returned part, and the issue of the one-minute wait last of 79. | `0d3786da` |

The commit of `main` is the commit at which the rule was first written as a value.
A rule built on `main` that changes one choice carries the same commit, the code that its other choices come from.
`legion replay rules` prints:

```text
main (commit 0d3786da536722f2eb26a793ab1b9827b6e90f47)
  keeps a slot in: admitted, awaiting_person, planning, implementing, testing, reviewing, held
  a link wait gives the slot back: true
  a children wait gives the slot back: true
  a wait gives the slot back after: 0s
  a wait stops the phase under way: false
  the line's order: returned_oldest_first
  a start takes: own_slot
  a conflicting head: merge_forward
  the slot goes back at the ready mark: false

legion-v20.42.5 (commit 958173da0ea4cd03290a1964edf9eb92e6685200)
  keeps a slot in: admitted, awaiting_person, planning, implementing, testing, reviewing, held
  a link wait gives the slot back: true
  a children wait gives the slot back: true
  a wait gives the slot back after: 0s
  a wait stops the phase under way: false
  the line's order: returned_oldest_first
  a start takes: own_slot
  a conflicting head: moves_back_conflict
  the slot goes back at the ready mark: false

legion-v19.57.3 (commit 9542c6fbbecd184ccee73523ab3defa39d6cf23a)
  keeps a slot in: admitted, awaiting_person, planning, implementing, testing, reviewing, accepting, retro, merging, held
  a link wait gives the slot back: false
  a children wait gives the slot back: false
  a wait gives the slot back after: 0s
  a wait stops the phase under way: false
  the line's order: returned_oldest_first
  a start takes: own_slot
  a conflicting head: moves_back_checks_red
  the slot goes back at the ready mark: true

keep-past-review (commit 0d3786da536722f2eb26a793ab1b9827b6e90f47)
  keeps a slot in: admitted, awaiting_person, planning, implementing, testing, reviewing, accepting, retro, merging, held
  a link wait gives the slot back: true
  a children wait gives the slot back: true
  a wait gives the slot back after: 0s
  a wait stops the phase under way: false
  the line's order: returned_oldest_first
  a start takes: own_slot
  a conflicting head: merge_forward
  the slot goes back at the ready mark: false

blockers-first (commit 0d3786da536722f2eb26a793ab1b9827b6e90f47)
  keeps a slot in: admitted, awaiting_person, planning, implementing, testing, reviewing, held
  a link wait gives the slot back: true
  a children wait gives the slot back: true
  a wait gives the slot back after: 0s
  a wait stops the phase under way: false
  the line's order: blockers_first
  a start takes: own_slot
  a conflicting head: merge_forward
  the slot goes back at the ready mark: false

returned-by-rank (commit 0d3786da536722f2eb26a793ab1b9827b6e90f47)
  keeps a slot in: admitted, awaiting_person, planning, implementing, testing, reviewing, held
  a link wait gives the slot back: true
  a children wait gives the slot back: true
  a wait gives the slot back after: 0s
  a wait stops the phase under way: false
  the line's order: returned_by_rank
  a start takes: own_slot
  a conflicting head: merge_forward
  the slot goes back at the ready mark: false

cover (commit 0d3786da536722f2eb26a793ab1b9827b6e90f47)
  keeps a slot in: admitted, awaiting_person, planning, implementing, testing, reviewing, accepting, retro, merging, held
  a link wait gives the slot back: false
  a children wait gives the slot back: false
  a wait gives the slot back after: 0s
  a wait stops the phase under way: false
  the line's order: returned_oldest_first
  a start takes: cover
  a conflicting head: merge_forward
  the slot goes back at the ready mark: false

wait-grace (commit 0d3786da536722f2eb26a793ab1b9827b6e90f47)
  keeps a slot in: admitted, awaiting_person, planning, implementing, testing, reviewing, held
  a link wait gives the slot back: true
  a children wait gives the slot back: true
  a wait gives the slot back after: 5m0s
  a wait stops the phase under way: false
  the line's order: returned_oldest_first
  a start takes: own_slot
  a conflicting head: merge_forward
  the slot goes back at the ready mark: false

returned-by-first-slot (commit 0d3786da536722f2eb26a793ab1b9827b6e90f47)
  keeps a slot in: admitted, awaiting_person, planning, implementing, testing, reviewing, held
  a link wait gives the slot back: true
  a children wait gives the slot back: true
  a wait gives the slot back after: 0s
  a wait stops the phase under way: false
  the line's order: returned_by_first_slot
  a start takes: own_slot
  a conflicting head: merge_forward
  the slot goes back at the ready mark: false
```

Each line of a rule gives one choice:

- `keeps a slot in`: the phases in which a started issue keeps its slot.
  A phase that the list leaves out holds no slot, and the start of its worker waits for no slot.

- `a link wait gives the slot back` and `a children wait gives the slot back`: whether a started issue gives its slot back while it waits on an unfinished `blocked_by` link, or on its open children.

- `a wait gives the slot back after`: how long such a wait lasts before it gives the slot back.
  A holder whose wait is younger than this keeps its slot, and the first pass at or after the wait's start plus this time gives the slot back.
  `0s` gives it back at the pass that finds the wait.

- `a wait stops the phase under way`: whether such a wait also stops the worker of the phase.
  Where it does not, the phase goes on while the issue waits, and only its next start waits.

- `the line's order`: `returned_oldest_first`, the returned issues first, oldest first, and then the issues that have not started by rank.
  `returned_by_rank` places a returned issue by its board rank.
  `returned_by_first_slot` places a returned issue by the time of its first slot in its current generation.
  `blockers_first` places first, among the issues that have not started, each issue that a started issue waits on.

- `a start takes`: `own_slot`, a slot of its own while the cap has one free.
  `cover` lets an issue that a waiter waits on start on that waiter's slot, whatever the cap says, one cover for each holder.

- `a conflicting head`: `merge_forward` keeps the phase and asks the implementer, or in merging the merger, to merge the base forward.
  `moves_back_conflict` moves the issue back to implementing by the trigger `conflict`, and `moves_back_checks_red` by the trigger `checks_red`, from testing, reviewing and merging.
  Under every rule, a conflict in accepting or retro keeps its phase and waits for merging, where the merger merges the base forward (assumption 37).

- `the slot goes back at the ready mark`: whether an issue in merging gives its slot back at the merger's ready mark, under a rule that keeps a slot in merging.

### `wait-grace` and its grace

`wait-grace` keeps a holder's slot through a wait on another issue for five minutes.
The admission pass that first finds the holder waiting marks the start of the wait on its slot, as it marks every wait that a holder keeps its slot through.
A wait that ends inside the five minutes frees no slot, and its issue stands in no place of the line.
The first pass at or after the wait's start plus five minutes gives the slot back, as `main` gives it back at once.

No pass is asked for at the end of the grace, because the daemon has no wake that an admission pass can ask for at a time of its choosing.
The slot goes back at the next pass after the grace: the next fact of the project or the next controller tick, at most `controller_wake_interval_seconds` later.
That delay is the rule's own, in the daemon and in a replay alike.

The value comes from window one of the comparison, 94 hours at a cap of 157:

| Reading of the window | Value |
| --- | --- |
| Slots taken again in the pass that freed them | 776 of 819 |
| Median time from a slot's release to the next taking of a slot | 19 microseconds |
| Median time from one pass that frees a slot to the next | 4 minutes 44 seconds, over 760 such passes |
| Waits on another issue that gave a slot back | 114: 61 on a link and 53 on children, 62 still open at the window's end |
| Median length of those waits | 20 hours 24 minutes, an open wait counted to the window's end; 26 hours 22 minutes over the ended waits alone |
| The shortest of those waits | 15 seconds, then 14 minutes, 15 minutes and 25 minutes |
| Median time from a release for a wait to the issue's next slot | 18 hours 18 minutes, over 49 |
| Median time from the end of such a wait to the issue's next slot | 7 minutes 48 seconds, over 49 |

| Grace | Waits shorter than the grace | Slot-hours the grace keeps |
| --- | --- | --- |
| 1 minute | 1 (0.9%) | 1.89 |
| 2 minutes | 1 (0.9%) | 3.77 |
| 5 minutes | 1 (0.9%) | 9.42 |
| 10 minutes | 1 (0.9%) | 18.84 |
| 15 minutes | 2 (1.8%) | 28.24 |
| 30 minutes | 4 (3.5%) | 55.89 |
| 1 hour | 8 (7.0%) | 109.28 |
| 2 hours | 12 (10.5%) | 210.24 |
| 4 hours | 15 (13.2%) | 399.98 |

The median time from a slot's release to its next taking names no grace: the cap was full, and the pass that freed a slot gave it out at once in 776 of 819 releases.
The time from one pass that frees a slot to the next, 4 minutes 44 seconds, is when a slot next comes free, and five minutes is that time rounded up.
Five minutes keeps the slot through the one-minute class, the 15-second wait of the window, and through no other wait of the window, because the next shortest lasted 14 minutes.
Each longer wait holds its slot five minutes more, 9.4 slot-hours of the window's 14,768.
Over the window's waits, the next pass that a replay makes after the grace's end, an event, a recorded move, a slot moved or a tick, came a median 2.6 seconds later, at most 31.5 seconds.

### `returned-by-first-slot` and the first slot

`returned-by-first-slot` places each issue of the returned part by the time of its first slot in its current generation, the earliest first.
An issue that the daemon started early and that gave its slot back stands, when its work comes back, ahead of the returned issues that the daemon started after it.
The daemon records no first slot once a slot goes back, because the slot's row goes with it.
A replay keeps the first slots in a table of its own database: each pass keeps the slot of each holder as the first of its generation where the table holds none, and the start state keeps the first slot that the record shows for each issue in its current run.
A deployment that adopts this rule needs that record in the daemon's own store first.

## The branches

A branch is one value of each knob that the record does not give.
A result that rests on a knob runs under each value of that knob, so that you read the values side by side.

| Knob | Values, the main branch's first | What it decides | Assumption |
| --- | --- | --- | --- |
| `timing` | `offset`, `wall` | When a link's removal, a child's move out, a gate's closing and an outside blocker's close happen, at their recorded offset from the start of the wait that they end, or at their recorded time. | 4 |
| `answer` | `offset`, `wall` | When a person answers an ask or reviews a spec version, at the recorded offset from the question, or at the recorded time. | 5 |
| `afterSendBack` | `recorded`, `less-slot-wait` | How long the first work after a send-back takes, its recorded length, or that length less its recorded wait for a slot. | 13 |
| `firstStarts` | `recorded`, `rank` | The order of first starts, the order in which the record started the issues in the window, or the board's rank. | 36 |
| `neverStarted` | `none`, `drawn`, `drawn-done` | The work of an issue that never started, none, or the work of an issue drawn from those that the record started, or from those that it finished. | 8 |
| `merges` | `fitted`, `zero` | The merges term of the conflict draw, as fitted on the window, or zero. | 3 |

The slowdown factor f is the seventh knob, with the values 1, 1.2 and 1.6 (assumption 1).
The main branch is `offset`, `offset`, `recorded`, `recorded`, `none` and `fitted`, at f = 1.
Each other branch changes one knob, and takes the name `main+<knob>=<value>`, for example `main+timing=wall`.
The tool offers the main branch and the 7 branches that change one knob, each at f = 1, 1.2 and 1.6, which makes 24 branches for each rule and window.
A run's directory names its branch as `<branch>@<f>`, for example `main@1.2`.
Under the rule that made the record, both values of `timing` and of `answer` give the record's times, because every anchor falls at its recorded time.

Under `firstStarts=recorded`, the first start of an issue is the first slot that it took in the window.
A slot that the issue held at the window's start, or a slot of an earlier run, is not a first start.
Each issue that the record started in the window takes a rank in the order of these first starts, ahead of every rank on the board.
The line orders issues by the rank of their tree's root first.
So the root of a tree takes the rank of the earliest recorded first start among the issues of its tree that have not yet taken their first slot in the replay, the root's own first start among them.
Each time one of these issues takes its first slot, the replay moves the root's rank to the next of them.
The move reaches the daemon as a change of the root's rank in the replay's copy of Dispatch, at the moment of that slot, through the daemon's mirror, so the daemon's next pass orders the line by it.
No measure counts that change.
For example, a root that holds its slot at the window's start takes the rank of its first child that starts, and then the rank of its next child.
So the next child of a tree waits where its own recorded start puts it among the work of every tree, and the replay starts the children of different trees in the order in which the record started them (assumption 36).
A tree whose recorded starts are all taken shows its root at its board rank.
Each other issue keeps its board rank, and waits after the ranked issues.
So an issue that the record never started waits behind the recorded starts of its tree that are still to come, and then behind those of every other tree.
A change of a rank in the record does not move these ranks, and the measures read the board's ranks.
One order stays out of reach: while the next recorded start of a tree cannot start yet, its root still ranks by it, so a later issue of that tree that can start goes ahead of another tree's issue that the record started before it.

## The states of a slot holder

`legion state` says what each holder of a slot does now, in one line under its admission line:

```text
slots: 4 of 4 in use: 2 working (oldest 2026-10-05T10:12Z), 0 stopped, 1 person (oldest 2026-10-05T08:20Z), 0 issue, 1 idle (oldest 2026-10-05T09:30Z). Started work without a slot: 1 wait only for a slot (oldest 2026-10-05T09:58Z), 2 in all (oldest 2026-10-05T07:41Z). 2 holders rank below ACME-14, which waits for a slot.
```

The daemon counts each holder in the first of these five states that is true of it:

- `working`: a claim of the holder is in a turn, a start of one of its roles is queued, or a claim of it holds a task that no turn has taken yet.
  Its time is the start of its oldest open turn, else the time at which its oldest start was queued, else the time of its oldest pending task.

- `stopped`: its phase is held, or a claim of it is one that nothing will wake (`claim_stopped` in its `why`).
  Its time is the start of the hold or of the stop.

- `person`: it is a tree root that waits on a person, on an ask or on its design gate.
  Its time is the start of that wait.

- `issue`: it waits on another issue, an unfinished Dispatch blocker or an open child, and keeps its slot through the wait under the rule.
  Its time is the start of the wait.

- `idle`: none of the four is true.
  Its time is the end of its last turn, or the moment at which it took its slot where that is later.

The time after `oldest` is the earliest moment at which a holder of that count entered its state, in UTC to the minute.
The second sentence gives the two readings of started work that waits for a slot.
`wait only for a slot` is reading (a): the returned issues that hold no slot and that `admission.blocked` does not name.
`in all` is reading (b): every returned issue that holds no slot, an issue that still waits on another issue among them.
The last sentence counts the holders that rank below the first issue of the line that waits for a slot, in the line's own order.
It reads `No issue waits for a slot.` when no issue does.

`legion state --json` carries the same numbers as `admission.slots`, and each holder's state and its time on its slot (`state`, `since`).
The controller's tick notice and the `legion` tool's `read_state` summary carry the line too.
A replay measures from the same states, so a reading of production and a replay use one set of words.
`waitOnlyForSlot` is reading (a), `startedWithoutSlot` is reading (b), and `priorityTaken` places holders and waiting issues in the same order as the last sentence.
[Running Legion](/legion/legion/running-legion/#observe) gives the rest of what `legion state` prints.

## The assumptions

A replay under a rule other than the one that made the record rests on assumptions about what the window would show under that rule.
Each run names the assumptions that it used.
Numbers 1 to 18 are the list of the tool's design, 19 to 25 come from its plan, and 26 to 37 are the tool's own.
Each section below gives the assumption as the tool states it, and its verdict.
It then gives the evidence in a recorded window, the direction in which the assumption moves each measure if it is wrong, and the reading of a window that would test it.

Each bullet that follows the label `If it is wrong.` starts with a direction:

- **Up**: the replay gives a value above the true value under that rule.

- **Down**: the replay gives a value below it.

- **None**: the assumption does not move the measure.

- **Unknown**: a window does not show which way the measure moves.

The direction is that of a replay under a rule other than the one that made the record.
Where it depends on the rule, the bullet names the rule for each direction.
Under the rule that made the record, `validate` measures how far a replay stands from the record.

A first replay, a prototype outside this repository, replayed one deployment's record under eight rules.
A review of it by a model of another family named the faults that the tool fixes.
For assumptions 1 to 18, sound, doubtful and wrong are that review's verdicts.
For assumptions 19 to 37, a verdict is the tool's own.
Numbers from the design's record come from that deployment's daemon log over four days, in which every slot was in use for 99.95% of the time.
That record is not in this repository.

### Assumption 1: A work segment takes its recorded time at any start and at any number in flight

Doubtful.
If load slows work, the replay favours a rule with more issues in flight.
The tool takes the slowdown factor f: a segment that starts with n issues in flight in the replay and m in the record runs its recorded length times `f^((n − m)/42)`.
At f = 1, or where n = m, a segment keeps its recorded length.

**Evidence in a recorded window.** _One step of load, read across a change of the rule._
The design's record shows no slowdown from 186 to 228 issues in flight.
That step came with a change of the slot rule, so it does not separate the load from the rule.
f is the slowdown for 42 more issues in flight, which is the size of that step.

**If it is wrong.**
If work slows as more issues are in flight, a replay at f = 1 moves the measures so.

- Issues finished: Up for a rule with more issues in flight than the record, because its segments end sooner in the replay than they would.
  Down for a rule with fewer.

- Started work that waits for a slot: Down for a rule with more issues in flight, because shorter segments bring each holder sooner to the moment at which it gives its slot back.
  Up for a rule with fewer.

- Priority taken by holders: Unknown.
  Segment lengths change which issues hold slots at each moment, but not by their rank.

- Other delays: Down for a rule with more issues in flight, because each round of a send-back loop, and the work of each blocker, keeps its shorter recorded length.
  Up for a rule with fewer.

- Slots: Down for a rule with more issues in flight, because slower work would hold more slot-hours in a turn and keep more issues in flight.
  Up for a rule with fewer.

**The reading that would test it.** _Segment lengths against the issues in flight, under one rule._
Run with `--trace-segments`, and read the phase, m and recorded length of each segment in `segments.jsonl`.
In a span with one rule, a recorded length that grows with m, for the segments of one phase, shows a slowdown.
`compare` gives, for each rule, the f at which its rank against `main` changes, or that no f up to 2 changes it.

### Assumption 2: A stretch past the review keeps its recorded length and its outcome

Doubtful.
If architects and mergers are slower with more issues past the review, the replay favours a rule that keeps slots past the review.
The tool keeps each stretch past the review at its recorded length and outcome, and the comparison says so.

**Evidence in a recorded window.** _Stretches past the review, with what ended each._
The phase lines give each visit to accepting, retro and merging, with its time and the trigger that ended it.
In the design's record, the architect or the merger sent work back within a median of 15 to 83 minutes.
442 of 682 entries into accepting came back after a send-back.

**If it is wrong.**
Suppose that architects and mergers are slower when more issues stand past the review.
Then, for a rule that keeps slots past the review and puts more issues there, the replay moves the measures so.

- Issues finished: Up, because each stretch past the review ends at its recorded length, sooner than it would.

- Started work that waits for a slot: Down, because each holder past the review reaches done, and frees its slot, sooner in the replay.

- Priority taken by holders: Unknown.
  The stretches change how long the holders past the review hold their slots, not their rank.

- Other delays: Down, because an issue that waits on an issue past its review ends its wait sooner in the replay.

- Slots: Down, because longer stretches would hold more slot-hours and keep more issues in flight.

**The reading that would test it.** _Stretch lengths against the issues past the review._
For each stretch past the review in the record, read its length and the number of issues past the review at its start.
A length that grows with that number shows the slowdown.
A window recorded under a rule that keeps slots past the review gives a second reading.

### Assumption 3: Send-backs keep their count and cause

Doubtful for conflicts.
The first replay saw more conflicts after a longer wait for a slot.
If a longer wait brings more conflicts than the fitted rate gives, the replay favours a rule under which branches wait longer.
The tool draws conflicts from a rate that it fits on the stretches inside the window, with a branch that sets the merges term of that rate to zero.
It keeps every other send-back loop at its recorded count and length.

**Evidence in a recorded window.** _A conflict rate fitted on the window, and loops kept as recorded._
The phase lines give each move back with its trigger, and the trace names its cause.
`run` prints the conflict model: the window, and the stretches and conflicts, that it was fitted on, each coefficient with its interval, and the odds ratio for each doubling of the merges.
In the design's record, that odds ratio was 1.21, with a 95% interval of 0.92 to 1.35, so the record does not show the merges term.

**If it is wrong.**
If a longer wait brings more conflicts than the fitted rate gives, the replay moves the measures so for a rule under which branches wait longer.

- Issues finished: Up, because the replay draws fewer conflicts for that rule than it would meet.

- Started work that waits for a slot: Down where a conflict moves its issue back from a phase that keeps no slot, because each missed conflict is a start that would wait for a slot.

- Priority taken by holders: Unknown.
  The missed conflicts change which issues wait, not their rank.

- Other delays: Down, because the replay counts fewer rounds and hours of `conflict` than the rule would bring.

- Slots: Down, because each missed conflict is work, with its turns and its time in flight, that the replay does not run.

**The reading that would test it.** _Conflicts against the wait before each stretch._
For each stretch of the record, read whether it ended in a conflict, the merges into its base during it, and how long its issue waited for a slot before it.
At the same number of merges, a higher rate of conflict after a long wait shows the effect that the rate leaves out.
`validate` and `compare` give the branch `main+merges=zero` beside `main`, which shows how far the merges term moves each measure.

### Assumption 4: A link removal, a child moved out, a gate closing and an outside blocker's close happen at their recorded time, or at their recorded offset from the wait's start

Doubtful, and the direction is unknown.
The tool runs both as branches.
`main` places each such event at its recorded offset from the start of the wait that it ends, and `main+timing=wall` at its recorded time.
Under the rule that made the record, both give the record's times.

**Evidence in a recorded window.** _The events that end each wait, and who made them._
Dispatch's events give each link's removal, each child's move out and close, and each blocker's close.
The admission lines and the design gate lines give the start of each wait.
In the design's record, a Legion architect took the link off in 14 of the 21 link waits that ended, after a median of 43.8 hours.
In the other 7, the blocker reached done, after a median of 36.0 hours.
14 waits on children ended because a new spec version closed the parent's design gate.

**If it is wrong.**
If such an event follows neither its recorded time nor its offset from the wait's start under another rule, the replay moves the measures so.

- Issues finished: Unknown.
  The window does not show whether an event follows the clock or the start of its wait.

- Started work that waits for a slot: Unknown.
  An event that comes sooner than the replay gives makes its issue wait for a slot sooner, and a later one later.

- Priority taken by holders: Unknown.
  The events change which issues wait, not their rank.

- Other delays: Unknown.
  The hours of the waits on other issues move with these events, earlier or later.

- Slots: Unknown.
  A wait that ends at another time changes when its issue holds a slot.

**The reading that would test it.** _The offset of each end from the start of its wait._
For each wait that ended in the window, read its start and the event that ended it.
Ends that keep their offset when the start of the wait moves support `offset`, and ends at times of their own support `wall`.
Two windows recorded under two rules show whether the offsets stay the same when the starts move.
`validate` gives `main` and `main+timing=wall` side by side.

### Assumption 5: A person answers at the recorded time, or at the recorded offset

Doubtful, and the direction is unknown.
The tool runs both as branches.
`main` places each answer at its recorded offset from the question, and `main+answer=wall` at its recorded time.
The question is the ask's opening, or the registration of the spec version that the person reviews.

**Evidence in a recorded window.** _Each question and its answer._
Dispatch's events give each ask's opening and its answer, and each spec version's registration and its review.
The daemon's line `workflow: root waits on a person and keeps its admission slot` gives each root that waits on a person.

**If it is wrong.**
If a person answers at neither the recorded time nor the recorded offset under another rule, the replay moves the measures so.

- Issues finished: Unknown.
  An answer that comes sooner or later than the replay gives moves the root's next start the same way.

- Started work that waits for a slot: Unknown.
  An answered root counts in reading (b) from its answer to its slot, so that count moves with the answer.

- Priority taken by holders: Unknown.
  The answer changes when the root enters the line, not its rank.

- Other delays: Unknown.
  A wait on a person is not one of the other delays, but an issue that waits on the root ends its wait when the root's work ends.

- Slots: Unknown.
  A root that waits on a person holds its slot in the state `person`, so its slot-hours with no worker in a turn move with the answer.

**The reading that would test it.** _The delay of each answer against the time of its question._
For each ask and each spec review in the window, read the delay from the question to the answer, and the hour of the answer.
Delays that stay the same at any hour support `offset`, and answers grouped at certain hours support `wall`.
`validate` gives `main` and `main+answer=wall` side by side.

### Assumption 6: The issues in flight at the window's start enter in their recorded state

Sound for one window, and not for a steady state.
The start state carries the recorded rule's choices into every rule's first day, which favours the recorded rule.
The tool reports the first day apart: each scorecard of a window longer than a day carries `firstDayApart`.

**Evidence in a recorded window.** _The start state and the issues that it could not place._
The tool rebuilds the state at `--from` from the lines and events of the reach.
Each run gives the number of issues that it could not place, and `run.json` names each with its reason.
A scorecard counts them as `unplaced`.

**If it is wrong.**
If the issues in flight at `--from` would stand otherwise under another rule, the replay moves the measures so, most in the first day.

- Issues finished: Down for a rule other than the recorded one, because its first day starts from the recorded rule's choices.

- Started work that waits for a slot: Up for a rule other than the recorded one, for the same reason.

- Priority taken by holders: Up for a rule other than the recorded one, for the same reason.

- Other delays: Up for a rule other than the recorded one, for the same reason.

- Slots: Unknown.
  The start state fixes which issues hold the slots at `--from`, so the first day's slot-hours follow the recorded rule.

**The reading that would test it.** _The first day against the rest of the window._
For each rule, compare each measure of `firstDayApart` with the same measure over the whole window.
A rule whose first day differs from its later days more than the recorded rule's first day does carries the start state.
A later `--from` on the same lines shows whether the difference gets smaller.

### Assumption 7: An issue may start once its todo, its links, its gate, its ancestors and its tree allow it, at the times recorded

Wrong in the first replay, which read only `todo` and links.
A hold that the replay misses starts an issue early, which favours a rule that starts more issues.
The tool reads every hold at its recorded time: a design gate, a link on a child, an ancestor, an accepted ancestor, a tree, a held phase, the project's pause and a boot's catch-up.

**Evidence in a recorded window.** _The holds that the record names._
The daemon's lines give each design gate's opening and closing, each held phase and its release, each pause and resume, each tree that lingers, and each boot's catch-up.
A window of a release that writes no line where a phase is held reads each hold from the failed claim of the phase's worker.
That hold ends at its release line, at the next launch of a claim of the issue in the failed role, at the issue's next move, or at its leave, whichever comes first.
A relaunch or a move with no release line shows the record working the phase again, and the replay releases the hold there as it releases a recorded one.
Dispatch's events give each link, label and status.
The test `TestEveryHoldHasASourceInTheReplay` builds one small window for each hold of the daemon, and fails where a start does not wait from the start of the hold to its end.

**If it is wrong.**
If a hold stands that the replay misses, or at another time than the record gives, the replay moves the measures so.

- Issues finished: Up for a rule that starts more issues, because a missed hold lets an issue start early.

- Started work that waits for a slot: Unknown.
  A missed hold lets an issue take a slot sooner, but the line gives the returned issues their slots first.

- Priority taken by holders: Unknown.
  A held issue does not wait for a slot, so a missed hold adds a waiting issue at its own rank.

- Other delays: None.
  A hold on a first start is not a wait of a started issue, and no send-back loop reads it.

- Slots: Up for a rule that starts more issues, because each issue that starts early is in flight sooner.

**The reading that would test it.** _Each first start against the holds on it._
For each issue that the record started in the window, read the holds that stood before its first slot, and the moment at which the last one ended.
In a replay of the recorded rule, a first slot before that moment shows a missed hold, and `validate` gives each issue's error in its first slot.
A run's report names each issue that it could not place, with its reason.

### Assumption 8: The work of an issue that never started in the window is unknown

A scenario that gives such an issue work that finishes favours a rule that starts more issues.
The scenario that gives it no work disfavours that rule.
The tool names the scenario that each run used: `none`, `drawn` or `drawn-done`.

**Evidence in a recorded window.** _The issues that never start._
A window shows each issue handed to Legion, and whether the record ever gave it a slot.
In the design's record, 394 issues waited to start at one moment, 194 children and 200 roots.
A new root waited a median of 16.7 hours for its first slot.

**If it is wrong.**
If the work that a scenario gives differs from the work that the issue would do, the replay moves the measures so for a rule that starts more issues than the record did.

- Issues finished: Up under `drawn-done` if the real work finishes less often than the drawn work.
  Down under `none` if any of the real work finishes.

- Started work that waits for a slot: Unknown.
  Drawn work brings the send-backs of the issue that it copies, and each can wait for a slot.

- Priority taken by holders: Unknown.
  The scenario changes the work of an issue, not its rank.

- Other delays: Unknown.
  Drawn work brings the loops and the waits of the issue that it copies.

- Slots: Up for the slot-hours with no worker in a turn under `none`, because such an issue holds its slot to the window's end with no turn.

**The reading that would test it.** _The three scenarios side by side, and a later window._
`validate` and `compare` give `main`, `main+neverStarted=drawn` and `main+neverStarted=drawn-done` side by side.
A later window that starts the same issues shows their real work.
Compare it with the work drawn for issues of the same kind, a root for a root and a child for a child.

### Assumption 9: The line orders returned work first and then by rank

Sound.
A replay runs the line's own code, so it orders the line as the daemon does.
A rule that orders the line otherwise does so in its own code.

**Evidence in a recorded window.** _The slots taken, in their order._
The `admission: slot taken` lines give each slot taken, with its issue and its time.
The admission lines that give a slot back to an issue whose work came back, or whose wait ended, name that issue.
In the design's record, each slot that came free went first to the oldest returned issue whose wait had ended, and otherwise to new work.

**If it is wrong.**
If the daemon gave slots out in another order than its code gives, the replay moves the measures so.

- Issues finished: Unknown.
  Another order changes which issue takes each slot, not how many slots come free.

- Started work that waits for a slot: Unknown.
  A returned issue that stands first in the line waits less than one that does not.

- Priority taken by holders: Unknown.
  An order closer to the board's rank takes less priority, and one further from it takes more.

- Other delays: Unknown.
  A wait on another issue ends when the work of its blocker ends, and the line decides when that work starts.

- Slots: Unknown.
  The order chooses which issue takes a free slot, not whether the slot is given.

**The reading that would test it.** _Each slot taken against the first issue of the line._
For each slot taken in the record, compare its issue with the first issue of the line at that moment in a replay of the recorded rule.
`validate` gives each issue's error in its first slot, and an order that differs shows as errors of opposite sign for pairs of issues.

### Assumption 10: A link or a child has the lifetime that its events give it

Wrong in the first replay, which merged them over the window.
A lifetime merged over the window makes each wait on it longer, which favours a rule under which a wait gives its slot back.
The tool keeps the lifetime of each link and of each child.

**Evidence in a recorded window.** _Each link and each child, with its events._
Dispatch's events give each `blocked_by` link's addition and removal, and each child's filing, its moves out and its status.
The fixture windows `link-added-and-removed` and `child-moved-out` replay one of each.

**If it is wrong.**
If a link or a child lasts longer in the replay than it would, the replay moves the measures so.
If it lasts less long, each measure moves the other way.

- Issues finished: Down for every rule, because each waiting issue finishes later.
  It moves down most for a rule that keeps the slot through a wait, such as `cover` and `legion-v19.57.3`.

- Started work that waits for a slot: Up for reading (b) under a rule under which a wait gives its slot back, because the waiting issue counts there for the whole wait.

- Priority taken by holders: Unknown.
  A longer wait changes which issues wait, not their rank.

- Other delays: Up, because the hours of each wait on another issue grow with the lifetime.

- Slots: Up for the slot-hours with no worker in a turn under a rule that keeps the slot through a wait, because the waiting holder keeps its slot longer.

**The reading that would test it.** _Each wait and what ended it._
A scorecard gives the count and the hours of the waits by what ended them, from `blocker_done` to `open`.
`validate` gives the gap of each `waits[<word>].hours` between the record and the replay of the recorded rule.

### Assumption 11: A slot that comes free is given out in the same pass

Sound for the record.
A slower hand-out would make the waits for a slot of every rule longer alike.

**Evidence in a recorded window.** _The time from a slot coming free to its hand-out._
Each `admission: slot released` and `admission: slot taken` line gives its time and the slots in use.
In the design's record, the median time from a slot coming free to its hand-out was 18 microseconds, and the longest was 88 seconds.

**If it is wrong.**
If the daemon gave a free slot out later than the pass that freed it, the replay moves the measures so.

- Issues finished: Up, a little, for every rule alike, because each slot goes to the next issue sooner in the replay.

- Started work that waits for a slot: Down, a little, for every rule alike, for the same reason.

- Priority taken by holders: Unknown.
  A later hand-out changes the number of holders at each moment, not their rank.

- Other delays: Up, a little, for every rule alike, because a blocker that starts later ends the waits on it later.

- Slots: Up, a little, for every rule alike, because a slot that the replay gives out at once is held for longer.

**The reading that would test it.** _Each slot released against the next slot taken._
Pair each slot released with the next slot taken while an issue waited, and read the time between them.
From this release, one `admission: a pass moved slots` line names each slot that one pass gave out and took back.

### Assumption 12: A waiting slot costs no pod after the idle release

Sound after 10 minutes.
A pod kept longer would cost a rule that holds idle slots more than the replay counts.

**Evidence in a recorded window.** _The daemon's release of idle pods._
The daemon releases an issue pod that is idle for 10 minutes.
In the design's record, it released 2,266 idle pods in 25 hours, which gave back 2,247.53 pod-hours.
The median time from a wake to the role's first turn was 160 seconds.

**If it is wrong.**
If an idle slot kept its pod past 10 minutes, the replay moves the measures so.

- Issues finished: None, because the measures count slots and work, not pods.

- Started work that waits for a slot: None, for the same reason.

- Priority taken by holders: None, for the same reason.

- Other delays: None, for the same reason.

- Slots: None, because the measures already count an idle slot as slot-hours with no worker in a turn.
  The cost of a pod kept longer stands outside the measures.

**The reading that would test it.** _The idle pods against the idle slots._
Read from the daemon's log when it released each idle pod, and read from a scorecard the slot-hours with no worker in a turn.
An idle slot whose pod stays up for more than 10 minutes shows a cost that the replay does not count.

### Assumption 13: Work after a send-back takes its recorded time, or that time less its wait for a slot

Doubtful.
The recorded time holds the recorded rule's own wait for a slot, which favours that rule.
The tool runs both as branches.
`main` keeps each recorded length.
`main+afterSendBack=less-slot-wait` takes the visit's recorded wait for a slot, from the move back to its worker start, off the first piece of work after a send-back.
That is the most time that a worker which never went cold could save.

**Evidence in a recorded window.** _The wait before the work after a send-back._
The phase lines give each move back, and the slot lines and the `supervise: launched` lines give the worker start after it.
In the design's record, a send-back past the review waited a median of 16.8 to 18.0 minutes for its slot, and at most 142.4 minutes.
The daemon releases an issue pod that is idle for 10 minutes, so the worker after a longer wait can start in a new pod.

**If it is wrong.**
If the work after a send-back is shorter than its recorded length under a rule that removes the wait, `main` moves the measures so for that rule.
If the work is as long, `less-slot-wait` moves each measure the other way.

- Issues finished: Down for a rule that removes or shortens the wait for a slot after a send-back, such as `keep-past-review`, because its work keeps the length that the wait gave it.

- Started work that waits for a slot: Up for such a rule, because each visit holds its slot for the longer work, and other issues wait longer.

- Priority taken by holders: Unknown.
  The longer visits change which issues hold slots, not their rank.

- Other delays: Up for such a rule, because a round's hours run to the next entry into the phase that it left, and the longer work is inside them.

- Slots: Up for the slot-hours held under such a rule, because each visit holds its slot for the longer work.

**The reading that would test it.** _First segments after a send-back, with and without a wait._
Run with `--trace-segments`, and read from the record which segments are the first after a send-back, and how long each issue waited for a slot before it.
Compare the recorded length of such a segment after a long wait with its length after a short wait.
A length that grows with the wait shows the time that `less-slot-wait` takes off.

### Assumption 14: A cover under spec v4's rule

Wrong in the first replay, which left out some covers and kept others too long.
Either fault misstates the rule with covers.
The tool runs the rule's own code, the rule `cover`, whose start may take a cover.

**Evidence in a recorded window.** _No record holds a cover._
No daemon ran the rule `cover`, so no window shows a cover.
The fixture windows `send-back-at-full-cap` and `two-waiters-one-blocker` replay covers that their authors worked out by hand from the rule.
In a replay under `cover`, each cover counts as a slot given out in `slotsGivenByDay`.

**If it is wrong.**
If the replay leaves out a cover that the rule takes, or keeps one longer than the rule does, the replay of `cover` moves the measures so.

- Issues finished: Unknown.
  A cover left out delays the issue that a waiter waits on, and a cover kept too long keeps a slot from the line.

- Started work that waits for a slot: Unknown.
  A covered waiter counts in reading (b), so each fault moves that count in the direction of the fault.

- Priority taken by holders: Unknown.
  Each fault changes which issue holds a slot, not the ranks.

- Other delays: Unknown.
  The waits on other issues end when the covered issue's work ends, which each fault moves.

- Slots: Unknown.
  Each fault moves the slot-hours of the covers in the direction of the fault.

**The reading that would test it.** _Each cover's start and end against the rule._
In a replay under `cover`, read from `log.jsonl` each slot given out and each `admission gives a slot back to an issue whose cover ended` line.
Each cover must begin when a waiter that holds a slot waits on an issue that has not started.
It must end when that issue is done, or when the waiter no longer waits on it.

### Assumption 15: An issue that stops working at the window's end would not have finished

Wrong: it counts the end of the window as a stop, which disfavours a rule that leaves more work under way at the end.
The tool reports the issues finished (`done`) and the work left (`workLeft`) apart.

**Evidence in a recorded window.** _The work under way at `--to`._
A window shows each segment that the record leaves unended at `--to`.
A replay does not end such a segment before the window does, and the scorecard counts its issue in `workLeft`.

**If it is wrong.**
It is wrong, so a reader who takes `done` alone as the work that a rule finishes reads the measures so.

- Issues finished: Down for a rule that leaves more work under way at `--to`, because some of that work would finish soon after.

- Started work that waits for a slot: None, because the measure reads the window's span alone.

- Priority taken by holders: None, for the same reason.

- Other delays: Down, a little, because a loop or a wait that stands at `--to` counts its hours only up to `--to`.

- Slots: None, because the slot-hours of every rule end at `--to`.

**The reading that would test it.** _Done and work left, and a later end._
Read `done` beside `workLeft` for each rule.
Replay a window that ends later on the same lines, and count how much of the first window's work left reaches done after its end.

### Assumption 16: An issue that reopens keeps its later work

Wrong in the first replay, which dropped it.
Dropping the later work makes the issues finished and the slots held too low under every rule.
The tool keeps the later work.

**Evidence in a recorded window.** _A reopen, and the work after it._
Dispatch's events show an issue that leaves done, and the phase lines show its next start.
The fixture window `reopen-works-again` replays one, in which the issue counts in `done` and again in `workLeft`.

**If it is wrong.**
If the later work would not come under another rule, the replay moves the measures so.
If the rule would bring more later work, each measure moves the other way.

- Issues finished: Up, because the replay counts the done of the later work again.

- Started work that waits for a slot: Up where the later work waits for a slot in the replay.

- Priority taken by holders: Unknown.
  The later work changes which issues hold slots, not their rank.

- Other delays: Up, because the replay counts the loops of the later work.

- Slots: Up, because the replay counts the slot-hours and the time in flight of the later work.

**The reading that would test it.** _The reopens of two windows._
For each reopen in the window, read the event that reopened the issue, and who made it.
The number of reopens for each issue finished, in two windows recorded under two rules, shows whether the rule changes it.

### Assumption 17: The links and children of an issue are the same under every rule

Doubtful, because a planner files children after its parent starts.
Children filed at their recorded times favour a rule that starts parents later.
The tool moves the filing of a child with its parent's start.

**Evidence in a recorded window.** _Children filed after their parent started._
Dispatch's events give each child's filing, and the phase lines give its parent's start.
The fixture window `child-filed-after-parent-start` replays one under `main` and `keep-past-review`.

**If it is wrong.**
If an issue would have other links or children under another rule, or a child's filing does not follow its parent's start, the replay moves the measures so.

- Issues finished: Up for a rule that starts parents earlier, if a child's filing does not follow its parent's start, because the replay files the child too soon.
  Unknown if the links and the children themselves differ.

- Started work that waits for a slot: Unknown.
  The window shows only the links and the children that the recorded rule met.

- Priority taken by holders: Unknown.
  Other children would stand at ranks that the window does not show.

- Other delays: Unknown.
  The waits on other issues are the waits on these links and children.

- Slots: Unknown.
  Other children would hold slots that the window does not show.

**The reading that would test it.** _The offset of each child's filing from its parent's start._
For each child filed after its parent started, read the offset from the start to the filing, and who filed it.
Offsets that stay close for one role support the move with the start.
Two windows under two rules show whether the number of children and links for each parent differs.

### Assumption 18: A conflict under the release that a project runs next behaves as recorded

Wrong for a project that moves to a release with cybermonklabs/legion#4464, where a conflicting head keeps its phase.
Its recorded conflict loops then make the conflicts of that release look too high.
The tool takes what a conflicting head does as part of the rule: `merge_forward`, `moves_back_conflict` or `moves_back_checks_red`.

**Evidence in a recorded window.** _The conflict moves of the record._
A window of a release whose conflicting head moves back holds phase lines with the trigger `conflict`, or `checks_red` in older releases.
Under `main`, a conflict keeps its phase, and the replay adds a merge-forward segment (assumption 23).
In the design's record, 359 conflict rounds took 2,315 hours in four days.

**If it is wrong.**
If a replay kept a recorded conflict loop for a release where a conflicting head keeps its phase, it would move the measures so.

- Issues finished: Down, because each kept loop sends its issue back to implementing.

- Started work that waits for a slot: Up where the loop leaves a phase that keeps no slot, because its issue then waits for a slot to go back to work.

- Priority taken by holders: Unknown.
  The kept loops change which issues wait, not their rank.

- Other delays: Up, because the replay counts the rounds and hours of `conflict` of each kept loop.

- Slots: Up for the slot-hours held and the issues in flight, because each kept loop is more work.

**The reading that would test it.** _A window of the release that the project runs next._
Collect a window recorded under that release, and read its conflicts, each a merge forward that keeps its phase under `main`.
Compare its `loops[conflict]` with a replay of the earlier window under `main`.

### Assumption 19: Under the release at 9542c6fb, whose workflow has the phase awaiting_merge, the time in awaiting_merge is folded into merging

This assumption and the three after it apply only to a window recorded under the release at 9542c6fb, release 19.57.3, from which the rule `legion-v19.57.3` comes.
A slot held in awaiting_merge counts as held in merging.
So a rule that keeps a slot in merging is charged that time, and a rule that keeps none is not.

**Evidence in a recorded window.** _The visits to awaiting_merge._
That release's workflow has the phase `awaiting_merge`, which today's workflow does not have, and it kept no slot there.
Its phase lines give each move into and out of `awaiting_merge`, and the trace reads that time as merging.

**If it is wrong.**
If the time in awaiting_merge should not count as merging, the replay moves the measures so in a window of that release.
It does so for a rule that keeps a slot in merging and does not give it back at the ready mark, such as `keep-past-review` and `cover`.

- Issues finished: Down, because the slot that such a rule holds through that time keeps another issue from starting.

- Started work that waits for a slot: Up, because other issues wait for the slot that it holds.

- Priority taken by holders: Unknown.
  The time changes how long the issue holds its slot, not its rank.

- Other delays: None, because the time in merging is in no send-back loop, and the issue's done does not move.

- Slots: Up for the slot-hours held, because the replay holds the slot through that time.

**The reading that would test it.** _The work in awaiting_merge._
In a window of that release, read the turns of each issue's roles in `awaiting_merge`.
A visit with a merger in a turn is work of merging, and a visit with no turn is a wait for the merge.

### Assumption 20: Under the release at 9542c6fb, the merger's ready move is a mark inside merging, where the rule legion-v19.57.3 gives the slot back

If that release gave the slot back at another moment of merging, the slot-hours past the review under `legion-v19.57.3` move by the time between the two moments.

**Evidence in a recorded window.** _The ready move and the slot line._
That release's phase lines give the merger's `ready` move into `awaiting_merge`, and its slot lines give the slot given back.
The trace reads the move as a mark inside merging, and `legion-v19.57.3` gives the slot back at that mark.

**If it is wrong.**
If that release gave the slot back later than the mark, the replay of `legion-v19.57.3` moves the measures so.
If it gave the slot back earlier, each measure moves the other way.

- Issues finished: Up, because the replay frees the slot sooner, and another issue starts sooner.

- Started work that waits for a slot: Down, because the issues that wait for that slot take it sooner in the replay.

- Priority taken by holders: Unknown.
  The moment changes who holds the slot, not the ranks.

- Other delays: None, because the moment at which the slot goes back moves no send-back loop and no wait on another issue.

- Slots: Down for the slot-hours held past the review, by the time between the mark and the real release.

**The reading that would test it.** _The slot line at each ready move._
In a window of that release, compare the time of each `admission: slot released` line of an issue in merging with its `ready` move.
A release at the same moment supports the mark.

### Assumption 21: Under the release at 9542c6fb, checks_red from any phase is a move back to implementing

A `checks_red` move that did not send the work back would count a send-back loop that never ran, which disfavours that release's rule.

**Evidence in a recorded window.** _The checks_red moves._
That release's phase lines give each move by the trigger `checks_red`, with the phase that it left and the phase that it entered.
The trace reads each as a move back to implementing, with the cause `red_checks_or_conflict`.

**If it is wrong.**
If a `checks_red` move did not send the work back, the replay moves the measures so in a window of that release.

- Issues finished: Down for a rule that keeps no slot in the phase that the move left, because the replay makes the issue wait for a slot before work that never stopped.

- Started work that waits for a slot: Up for such a rule, for the same reason.

- Priority taken by holders: Unknown.
  The move changes which issues wait, not their rank.

- Other delays: Up, because the replay counts a round of `red_checks_or_conflict`, with its hours, that never ran.

- Slots: Up for a rule that keeps no slot in the phase that the move left, because the issue holds a slot in implementing that it would not hold.
  None under `legion-v19.57.3`, which keeps a slot in every phase of the workflow.

**The reading that would test it.** _The work after each checks_red move._
In a window of that release, read the next turn of the issue after each `checks_red` move.
An implementer's turn supports a move back, and a turn of the role of the phase that the move left does not.

### Assumption 22: Under the release at 9542c6fb, red checks and conflicts are one cause of a send-back

A window of that release cannot tell how a rule changes conflicts.
So a forecast across the change scores this cause only inside the total of all send-back loops, and the result says so.

**Evidence in a recorded window.** _One trigger for two causes._
That release moved an issue back by `checks_red` for red checks and for a conflict alike, so its phase lines do not say which.
A window of that release records no `conflict` move, so it fits a chance of conflict of 0 and draws no conflict.
Its loops of `red_checks_or_conflict` stay as recorded under every rule.

**If it is wrong.**
If conflicts grow with the wait while red checks do not, the replay of a window of that release moves the measures so for a rule under which branches wait longer.

- Issues finished: Up, because the replay keeps the record's rounds of the cause where the rule would bring more.

- Started work that waits for a slot: Down where the loops leave a phase that keeps no slot, for the same reason.

- Priority taken by holders: Unknown.
  The missed rounds change which issues wait, not their rank.

- Other delays: Down, because the replay keeps the record's rounds and hours of the cause.

- Slots: Down for the slot-hours held, because the replay leaves out the work of the missed rounds.

**The reading that would test it.** _Each checks_red move against GitHub's record._
For each `checks_red` move in a window of that release, read from GitHub whether the pull request's head conflicted with its base at that moment.
The share of conflicts among the moves, against the wait before each, shows how the cause would change with the rule.

### Assumption 23: Merge-forward work under main lasts as long as the implementing round of a recorded conflict loop

A merge forward shorter than a conflict's implementing round makes the conflict cost of `main` too high, which disfavours `main`.

**Evidence in a recorded window.** _The recorded conflict loops._
A window of a release whose conflicting head moves back holds each conflict loop: the move back by `conflict`, and the implementing round after it.
Under `main`, a drawn conflict keeps its phase, and the replay adds a merge-forward segment, with the worker that the daemon asks in a turn: the implementer in testing and reviewing, and the merger in merging.
A conflict in accepting or retro waits for merging under every rule, and its merge-forward segment, the merger's, runs when the issue moves into merging, with a length that the replay draws when the conflict comes (assumption 37).
The replay draws the length of that segment from the implementing rounds of the recorded conflict loops that left the same phase, among the loops whose work the record shows ending.
Where the window records no loop from that phase, the replay draws from the loops that left a later phase, and where it records none of those either, from the loops that left an earlier phase.
The fixture window `conflict-from-fitted-rate` replays one under `main` and `legion-v20.42.5`.

**If it is wrong.**
If a merge forward is shorter than the implementing round of a conflict loop, the replay moves the measures so for `main` and each rule built on it.

- Issues finished: Down, because each drawn conflict holds its issue longer than a merge forward would.

- Started work that waits for a slot: Up where the conflict comes in a phase that keeps a slot, because its issue holds the slot longer and others wait.

- Priority taken by holders: Unknown.
  The longer segments change how long holders hold their slots, not their rank.

- Other delays: Up, because a drawn conflict's round counts the merge-forward segment as its hours.

- Slots: Up for the slot-hours in a turn, because the worker that the daemon asks is in a turn for the whole merge-forward segment.

**The reading that would test it.** _Merge forwards in a window of `main`._
In a window recorded under a release with #4464, read each merge forward from the conflict to the push that ends it.
Compare those lengths with the implementing rounds of the conflict loops in a window of an earlier release.

### Assumption 24: A daemon down between two boots runs no pass

Sound for the record.
What arrives while the daemon is down waits for the next boot.
A replay that ran a pass then would start issues earlier than any daemon could.

**Evidence in a recorded window.** _The boots and the spans between them._
Each daemon process writes its start lines, such as `legion daemon started`, and writes no line after it ends.
The replay enters each boot and each tick at its recorded time, and no fact while the daemon was down.

**If it is wrong.**
If a pass ran in a span that the record shows as down, for example because the lines of a running process were lost, the replay moves the measures so.

- Issues finished: Down, because the replay starts nothing in that span.

- Started work that waits for a slot: Up, because the issues that waited through the span wait longer in the replay.

- Priority taken by holders: Unknown.
  The span changes when issues take slots, not their rank.

- Other delays: Up, a little, because a wait that ends in the span ends at the next boot in the replay.

- Slots: Unknown.
  The span changes which issues hold slots at the next boot.

**The reading that would test it.** _Spans with no line, against the daemon's own boots._
List each span of the window with no daemon line for longer than a tick interval.
A span between a process's last line and the next start lines is time down.
A span inside one process's life is a gap of the log, which a second source of lines, such as a cloud log export, may fill.

### Assumption 25: An outside blocker's close is seen at the next tick or boot, as the daemon sees it

A close that the daemon saw sooner would end link waits sooner.
So reading the close at the tick makes link waits longer, which disfavours a rule that keeps its slot through a wait.

**Evidence in a recorded window.** _Each outside blocker's close, and the next tick._
The daemon learns the status of a blocker in another project at each tick and each boot.
A tick comes once each `--tick-interval`, the deployment's `controller_wake_interval_seconds`.
Dispatch's events give the blocker's close.
The replay gives each tick the status, at that moment, of each outside issue that a waiter waits on.

**If it is wrong.**
If the daemon saw such a close sooner than the next tick or boot, the replay moves the measures so.

- Issues finished: Down for a rule that keeps its slot through a wait, because its waiters hold their slots longer in the replay.

- Started work that waits for a slot: Up for reading (b) under a rule under which a wait gives its slot back, because the waiter counts there until its wait ends.

- Priority taken by holders: Unknown.
  The later close changes when the waiter leaves its wait, not its rank.

- Other delays: Up, because the hours of the link waits that such a close ended (`blocker_done`) grow by up to one tick interval.

- Slots: Up for the slot-hours with no worker in a turn under a rule that keeps its slot through a wait, because the waiter holds its slot longer.

**The reading that would test it.** _Each close against the end of the wait._
For each link wait that an outside blocker's close ended, read the time of the close from Dispatch.
Read the last `admission waits for what this issue waits on` line that names that blocker for the waiter.
A gap close to the tick interval supports the assumption.

### Assumption 26: A notice, a message or a prompt the daemon sends a worker or posts on Dispatch changes nothing the record's workers did after it: their work is the record's segments

The tool's own.
A notice that changed what a worker did, or how soon, would move that work by the replay's own timing of the notice.
Under the recorded rule, each notice falls where the record's did.
Under another rule, the work that the notice would have moved stays where the record put it, which favours the recorded rule's timing.

**Evidence in a recorded window.** _The rows that the replay finishes without acting._
The replay's worker stub finishes each Dispatch message, notice, controller notice, role message, hook prompt, update ask and merge-forward request without acting, and each start queued for news of the approved design alone that reaches its claim with news to tell.
The daemon gives the worker of such a start a task of news and no work, and launches its claim where an idle pod's release or the daemon's own rule suspended it; the replay leaves the claim as it stands.
`run` prints, beside this assumption, how many rows it finished under it, and `run.json` counts them under `divergence.finished`.

**If it is wrong.**
If a notice moved the work after it, the replay moves the measures so for a rule other than the recorded one.

- Issues finished: Unknown.
  A notice that comes sooner under the rule would move work sooner, and a later notice later.
  The replay moves neither.

- Started work that waits for a slot: Unknown, for the same reason.

- Priority taken by holders: Unknown, for the same reason.

- Other delays: Unknown, for the same reason.

- Slots: Unknown, for the same reason.

**The reading that would test it.** _Turns after a notice._
In the daemon's log, read each notice that the daemon sent a worker, and the start of that worker's next turn.
Turns that start soon after a notice more often than at other moments show that notices move work.
Compare the rows that `run.json` counts under this assumption between rules, because a rule that sends many more notices rests more on it.

### Assumption 27: What the daemon publishes on Dispatch for people and tools to read, the gated steps, the design gates, the role holders and the step and plan records, is read back by no decision a replay runs

The tool's own.
Sound: the daemon decides from its own record and from Dispatch's events.
A person who acted on a publication did so in a recorded event, which the replay applies.

**Evidence in a recorded window.** _The publication rows that the replay finishes without acting._
The replay finishes each publication of the gated steps, the design gates and the role holders, and each step record and plan record, without acting.
`run.json` counts them under `divergence.finished`.
A person's act after a publication is a Dispatch event of the window.

**If it is wrong.**
If a decision of the daemon read a publication back, the replay moves the measures so.

- Issues finished: Unknown.
  Such a decision would read the record's publication, not one at the replay's moment.

- Started work that waits for a slot: Unknown, for the same reason.

- Priority taken by holders: Unknown, for the same reason.

- Other delays: Unknown, for the same reason.

- Slots: Unknown, for the same reason.

**The reading that would test it.** _Decisions after a publication._
In a window, read each decision of the daemon that follows a publication.
A decision with a Dispatch event between the two follows the event, which the replay applies, as it applies an answer (assumption 5).
A decision with no event between the two would show that the daemon reads the publication back.

### Assumption 28: The work of a pod or a workspace, a branch made, a pod stopped, bundled, released or removed, and a role that a pause or an idle pod's release stops, lies inside the recorded work segments and the record's own pause

The tool's own.
A rule that starts more issues at once may wait longer for pods and workspaces than the record did.
The recorded lengths do not carry that wait, which favours such a rule.
The slowdown factor f is the tool's reading of it.

**Evidence in a recorded window.** _The pod and workspace rows that the replay finishes without acting._
The replay finishes each release and each pause stop of a role, and each issue branch, issue suspend, bundle, issue release, workspace removal and claim release, without acting.
`run.json` counts them under `divergence.finished`.
The replay's pause queues its stop rows on the simulated clock, as the daemon queues them on its own, so each pause stop finishes at the moment the record shows the pause.
A segment's recorded length runs from its worker start, so the launch of its pod is inside it.
In the design's record, the median time from a wake to the role's first turn was 160 seconds.

**If it is wrong.**
If pods and workspaces take longer when more issues start at once, the replay moves the measures so for a rule that starts more issues at once.

- Issues finished: Up, because its segments keep their recorded length where they would take longer.

- Started work that waits for a slot: Down, because its holders come sooner to the moment at which they give their slot back.

- Priority taken by holders: Unknown.
  The longer launches change how long holders hold their slots, not their rank.

- Other delays: Down, because each round of a send-back loop keeps its recorded length.

- Slots: Down for the slot-hours with no worker in a turn, because a wait for a pod holds a slot with no worker in a turn.

**The reading that would test it.** _The time to the first turn against the issues in flight._
For each `supervise: launched` line in the record, read the time to the first `supervise: a turn started` line of its claim, and the issues in flight then.
A time that grows with the issues in flight shows a cost that the segments do not carry.
The break-even factor of `compare` says how large f must be to change a rule's rank.

### Assumption 29: GitHub moves a replay only by the moves the record shows: a pull request's opening, reviews, checks, mergeability, merge queue and pushes are read from the recorded moves, and the replay's own pull request record stands in for the issue's

The tool's own.
A rule that changes how long a head waits may change what GitHub reports of it, a conflict above all.
The tool draws conflicts from the fitted rate (assumption 3), and keeps every other report as the record's moves, which favours a rule under which heads wait longer.

**Evidence in a recorded window.** _The GitHub facts that the replay leaves out._
The replay leaves out each fact of a pull request's opening, reviews, checks, closing, merge queue, pushes and mergeability.
It finishes each merge queue read, merge queue removal and conflict confirmation without acting.
The record's phase moves carry what GitHub caused, and the replay's own pull request record stands in for the issue's.

**If it is wrong.**
If GitHub reports more red checks, drops from the merge queue or other faults for a head that waits longer, the replay moves the measures so for a rule under which heads wait longer.

- Issues finished: Up, because the replay misses the work that those reports would cause.

- Started work that waits for a slot: Down where a missed report would send work back from a phase that keeps no slot.

- Priority taken by holders: Unknown.
  The missed reports change which issues wait, not their rank.

- Other delays: Down, because the replay leaves out the loops that those reports would cause.

- Slots: Down for the slot-hours in a turn, because the replay leaves out the work after those reports.

**The reading that would test it.** _GitHub's reports against the wait of each head._
For each pull request of the window, read from GitHub its checks and its merge queue events, and how long its head waited before its merge.
A rate of red checks or drops that grows with the wait shows a report that the replay does not draw.

### Assumption 30: A production check after a merge, its looks and its wakes, changes nothing of a replay: it runs after the issue is done

The tool's own.
Sound for the measures, which end at an issue's done.
A check that reopened its issue made the record's own reopen, which the replay applies.

**Evidence in a recorded window.** _The check rows and turns that the replay leaves out._
The replay leaves out each fact of a production check, its report, its looks, its wakes and its time limit, and finishes its rows without acting.
`run.json` counts the check's turns that the replay left out under `divergence.turnsLeftOut`.

**If it is wrong.**
A production check runs after its issue's done, so a check that changed what came after it would move the measures so.

- Issues finished: None, because a check runs after its issue's done.
  A reopen that the check caused enters as the record's reopen (assumption 16).

- Started work that waits for a slot: None, because a closed issue does not wait for a slot.

- Priority taken by holders: None, because a closed issue neither holds a slot nor waits for one.

- Other delays: None, because each loop and each wait on the issue ends at its done.

- Slots: None, because a closed issue holds no slot and is not in flight.

**The reading that would test it.** _Reopens after a production check._
In the window, read each issue that reopened after a look of its check, and the time of the reopen.
Each must be a Dispatch event of the window, which the replay applies (assumption 16).

### Assumption 31: A worker's claim settles as the record shows it: it launches, is ready and confirms its task at its start, its turns are the record's, and no silence, lost session, memory kill or full disk of supervision's moves the issue

The tool's own.
A supervision fault that the record holds shows as the claim failure, retry or hold that it led to, which the replay enters from the record.
A fault that a rule would bring on more or less often is not drawn, which favours a rule that runs more issues at once.

**Evidence in a recorded window.** _The supervision facts that the replay leaves out._
The replay leaves out the facts of a claim's readiness, of its task's confirmation, of a lost session and of a fault of the session store.
It also leaves out a silent worker, a stalled background job, an entry sweep, a memory kill in an issue's pod or in the controller's, a full volume, a listener's wake and a pod's backstop.
It enters each recorded `supervise: claim failed` line at its offset in the segment of its issue.

**If it is wrong.**
If faults come more often when more issues run at once, the replay moves the measures so for a rule that runs more issues at once.

- Issues finished: Up, because the replay misses the faults that would delay its work.

- Started work that waits for a slot: Down, for the same reason.

- Priority taken by holders: Unknown.
  The missed faults change how long holders hold their slots, not their rank.

- Other delays: Down, a little, because an issue that waits on one with a missed fault ends its wait sooner in the replay.

- Slots: Down for the slot-hours with no worker in a turn, because a fault holds a slot with no worker in a turn.

**The reading that would test it.** _Faults against the issues in flight._
From the daemon's log, count the claim failures, memory kills and full volumes in each hour against the issues in flight.
A rate that grows with the issues in flight shows faults that the replay does not draw.

### Assumption 32: A worker's completion passes the checks a live one passes before its move, its claim's run, its handoff record and its review's head, since the record shows it passed them; the move enters at the end of its segment

The tool's own.
Sound under the recorded rule.
Under another rule, a completion whose check would fail on a later head is entered as passed, which favours a rule under which more completions meet a moved head.

**Evidence in a recorded window.** _The completions that the record shows passed._
The replay leaves out each handoff's completion fact, and enters the recorded move at the end of its segment.
The record shows that each such completion passed its checks: its claim's run, its handoff record and its review's head.

**If it is wrong.**
If a completion would fail on a head that moved under another rule, the replay moves the measures so for a rule under which more completions meet a moved head.

- Issues finished: Up, because the replay misses the extra work that each refused completion would cause.

- Started work that waits for a slot: Unknown.
  A refused completion keeps its issue in its phase, where it holds or waits as before.

- Priority taken by holders: Unknown.
  The extra work changes how long holders hold their slots, not their rank.

- Other delays: Down, a little, because an issue that waits on one with a refused completion ends its wait sooner in the replay.

- Slots: Down for the slot-hours in a turn, because the replay leaves out the work after each refused completion.

**The reading that would test it.** _Refused completions against the merges during the work._
From the daemon's log, count the completions that it refused for a moved head, against the merges into the base while the worker ran.
A count that grows with the merges shows the refusals that a rule with longer work would meet.

### Assumption 33: The repairs a boot or a read of the repository mapping makes, a merger the cutover left released, a design gate's request found again, a repository's mapping changed, change nothing of a window

The tool's own.
Sound for a window recorded under one release and one repository mapping.
A repair that the record made shows as its own move or start, which the replay enters as recorded.

**Evidence in a recorded window.** _The repair facts that the replay leaves out._
The replay leaves out the cutover's repair of a released merger, and a design gate's request found again.
The parts of `window.json` give the release that ran each part of the window.

**If it is wrong.**
If a repair at a boot, or a change of the mapping, gave or took a slot that no recorded move shows, the replay moves the measures so.

- Issues finished: Unknown.
  The repair could free a slot or take one.

- Started work that waits for a slot: Unknown, for the same reason.

- Priority taken by holders: Unknown, for the same reason.

- Other delays: Unknown.
  A repair that changed a design gate would move the waits under it, earlier or later.

- Slots: Unknown, because the repair could free a slot or take one from that boot on.

**The reading that would test it.** _The boots inside the window._
Read each process start in the window, and the release that its part gives.
After a boot that starts a new release, look in the record for a slot or a move that no fact of the window explains.

### Assumption 34: An issue the record first shows in a phase that keeps a slot, whose first slot line gives a slot back or which no slot line names, held a slot from before the window's reach, until that line or, with none, until the rule gives the slot back; and an issue that stands at the window's start in a worker's phase that keeps a slot, with no slot line holding it there and no wait that gives the slot back, whose worker started in that run of phases after its last slot went back, held one from its last entry into that run until the rule gives it back

The tool's own.
Doubtful where the reach begins with slots in use and no line names their holders, because the slot is then read from the phase alone.
Doubtful too where a slot is taken again by a way that writes no slot line, because the slot at the window's start is then read from the worker's start alone.
The daemon starts the worker of a phase that keeps a slot only once the issue holds one, so a worker that started after the issue's last slot went back is the record's sight of a slot that no line names.
An issue whose work came back with no slot and whose worker has not started owes its start, and is given no slot.

**Evidence in a recorded window.** _The slot line at the window's start._
The first `admission:` slot line at or after the window's start gives the slots in use where the start state places them.
A window whose log holds no slot line from its start on is read at the last slot line before it, whose count stands at the start.
The start state reports the difference between that count and the slots that the trace holds right after the line, and the holders that it inferred.
`run.json` carries both, as `divergence.slotCountDifference` and `divergence.inferredSlots`.

**If it is wrong.**
If such an issue held no slot, the replay moves the measures so.
A holder that the inference misses moves each measure the other way.

- Issues finished: Down, because its slot keeps another issue from starting until the slot goes back.

- Started work that waits for a slot: Down, because the tool reads its time as held, not as waiting.

- Priority taken by holders: Up where it ranks below an issue that waits for a slot.

- Other delays: Up, because a link wait that it opens counts from its slot, which makes the waits longer.
  The loops are read from their own lines, and do not move.

- Slots: Up for the slots held, with no worker in a turn above all, by the span of the inferred slot.

**The reading that would test it.** _The slot count against the trace._
Compare the slots in use that the slot line at the window's start gives with the slots that the trace holds right after that line.
A difference of zero, with no inferred holder, means that the window does not rest on this assumption.
A longer `--before` moves the reach back, which can make the difference smaller where the lines exist.

### Assumption 35: A stretch whose recorded conflict loop runs to the window's end keeps that conflict, and its issue runs its own loop

The tool's own.
Doubtful, and the direction is unknown.
A stretch ends in a recorded conflict whose loop runs to `--to` when the record shows the issue moved back by `conflict` and never back in the phase that the conflict left.
The record then holds no work of the issue after the stretch but that loop.
Drawn as any other stretch, a draw of no conflict would take the issue past a loop after which the record shows nothing, and a drawn conflict would give it another loop in place of its own, after which it has nothing either: either way the issue would stand idle, with no worker, to the window's end.
So the replay does not draw such a stretch.
It keeps the conflict and makes it through the daemon's own path, as it makes a drawn one.
Under a rule whose conflicting head moves back, a kept conflict in testing, reviewing or merging moves the issue back, and it then runs its own loop as recorded.
Under `main`, the merge-forward segment of a kept conflict in testing, reviewing or merging lasts as long as that loop's implementing round, and has no end where the round has none.
In accepting or retro, the daemon at the head moves nothing and asks nobody, under every rule, and the record holds no work of the issue after the stretch but the loop, so the issue stands in its phase with no worker to `--to` (assumption 37).
Every other stretch is drawn.

**Evidence in a recorded window.** _Stretches that a recorded conflict loop runs past `--to`._
In a replay of one deployment's recorded window of four days under the rule that its daemon ran, with such stretches drawn as any other, six issues stood with no worker, in accepting, merging or reviewing, from the end of the stretch to the window's end, where the record shows each one working in its loop.
`run` prints the number of conflicts that it kept, and `run.json` counts them as `divergence.keptOpenLoops`.

**If it is wrong.**
If such a stretch would not have conflicted under the rule, the replay moves the measures so.

- Issues finished: Down, because the issue runs its loop to `--to`, where without the conflict it might have finished.

- Started work that waits for a slot: Up where the loop goes back to a phase that keeps a slot, because the issue takes or waits for a slot that it would not have needed.

- Priority taken by holders: Unknown.
  The kept loop changes which issues hold slots, not their rank.

- Other delays: Up, because each kept conflict counts as a round of `conflict`, its hours running to `--to`.

- Slots: Up for the slot-hours in a turn, because the loop's workers are in their turns to `--to`.

**The reading that would test it.** _A later end of the same lines._
Replay a window that ends later on the same lines.
For each conflict that the first window kept, read whether its loop came back, and how long it ran.
Compare the share of those stretches that the later window's model gives a conflict with the share that the first window kept.

### Assumption 36: Under the recorded order of first starts, a tree's root ranks at the earliest recorded first start among the issues of its tree that have not yet taken their first slot

The tool's own.
It is a choice of how the main branch makes its ranks under `firstStarts=recorded`, not a fault of the record.
The line orders the issues whose first phase can start by the rank of their tree's root first, and by their own rank second.
So the rank of a root decides where each child of its tree waits among the work of every other tree.
The replay moves the rank of a root each time an issue of its tree takes its first slot, to the next recorded first start of the tree ([The branches](#the-branches)).
The next child of a tree then takes the place that its own recorded start gives it among the work of every tree.
So the branch starts children in the order in which the record started them, ahead of a root's later work where the record did so.
The branches under `firstStarts=rank` keep the board's ranks, and show how much the choice moves the result.

**Evidence in a recorded window.** _A root that keeps one rank for the whole window._
In a replay of one deployment's recorded window under the rule that its daemon ran, each root kept the rank of its tree's earliest first start for the whole window.
The replay then gave a slot to a child that the record started much later, ahead of the issues of other trees that the record started earlier, which waited.
Children that the record never started, in trees whose roots started early in the window, took slots and held them with no work while issues that the record started waited.
The replay's moves then came far earlier or far later than the record's.

**If it is wrong.**
If the record's daemon started the children of different trees in their order for reasons that the replay does not carry, such as the board's ranks of the moment or a hold, the replay moves the measures so for a rule other than the one that made the record.

- Issues finished: Unknown.
  The order changes which issue takes a slot first, not how many slots there are or how long the work takes.

- Started work that waits for a slot: Unknown.
  The order changes which issues start first, and so which of them wait for a slot later.

- Priority taken by holders: Up, because the line follows the order in which the record started the issues rather than the board's ranks, by which the measure places holders and waiting issues.

- Other delays: Unknown.
  The order changes which issues wait on others, not how long a loop or a wait takes.

- Slots: Down for the slot-hours with no worker in a turn under `neverStarted=none`, because an issue that the record never started waits behind the recorded starts, where a line ordered by the board could start it sooner, to hold its slot with no turn.

**The reading that would test it.** _The two orders side by side, and the board at each start._
`validate` and `compare` give `main` and `main+firstStarts=rank` side by side.
For each child that the record started after a child of another tree, read the ranks that the board gave the roots of the two trees at the moment of each start, and the holds on each child.
A start that those ranks and holds explain is one where the order of the starts and the board agree.
A start that they do not explain is one that only the order of the starts keeps.

### Assumption 37: A conflict past the review is answered as the daemon that the replay runs answers it, which leaves it to the merger

The tool's own.
Wrong, by design, for a window recorded before cybermonklabs/legion#4979, such as a window of release 20.42.5.
The replay runs the daemon of the commit that it was built from, not the daemon that made the record.
Since cybermonklabs/legion#4979, that daemon leaves a conflict past the review to the merger.
In accepting and retro, it records the head as conflicting and asks nobody, under every rule, and it asks the merger to merge the base forward when the issue moves into merging.
In merging, it asks the merger to merge the base forward under a rule whose conflicting head keeps its phase, and it still moves the issue back to implementing under a rule whose conflicting head moves back.
A daemon before cybermonklabs/legion#4979 moved the issue back to implementing from accepting, retro and merging under a rule whose conflicting head moves back, and asked the implementer to merge the base forward in each of them under `main`.
So a replay of such a window under its own rule plays each conflict that it draws or keeps in accepting or retro otherwise than the record: the issue keeps its phase and runs no loop, and its merger merges the base forward when it moves into merging.
Where the record shows no work of the issue after a kept conflict but its loop (assumption 35), the issue stands in its phase with no worker to `--to`.
`validate` measures that difference as the replay's error under the rule that made the record.
A window of release 19.57.3 records no `conflict` move, so it draws no conflict (assumption 22), and this assumption does not move it.

**Evidence in a recorded window.** _The conflict moves past the review, against the run's count._
A window recorded under `legion-v20.42.5` holds a phase line with the trigger `conflict` for each conflict out of accepting, retro or merging.
`run` prints the number of conflicts past the review that it left to the merger, and `run.json` counts them as `divergence.conflictsToMerger`.

**If it is wrong.**
If the project runs a daemon from before cybermonklabs/legion#4979, the replay moves the measures so for a rule whose conflicting head moves back, such as `legion-v20.42.5`.
For `main` and each rule built on it, a merge forward lasts as long whichever worker runs it, so the direction is None.

- Issues finished: Up for a rule whose conflicting head moves back, because an issue whose conflict in accepting or retro waits for the merger finishes without the loop that the older daemon would run.
  None for `main`.

- Started work that waits for a slot: Down for a rule whose conflicting head moves back, because the issue does not go back to implementing, a phase that keeps a slot, so it never waits for one there.
  None for `main`.

- Priority taken by holders: Unknown.
  The loops that the replay does not run change which issues hold slots, not their rank.

- Other delays: Down for a rule whose conflicting head moves back, because each such conflict counts the merger's merge forward as its round, which is shorter than the loop that the older daemon would run.
  None for `main`.

- Slots: Down for a rule whose conflicting head moves back, because the replay runs none of the loop's work, with its slot-hours and its time in flight.
  None for `main`.

**The reading that would test it.** _A window of a release with cybermonklabs/legion#4979._
Collect a window recorded under a release with cybermonklabs/legion#4979, and read each conflict past the review in it.
A conflict in accepting or retro writes the line `workflow: a confirmed conflict needs no merge yet`, and a merge forward in merging writes the line `workflow: merge forward asked`.
Compare the time from each conflict to its merge with the replay of a window of an earlier release under its own rule, and that window's `validate` gap with its `divergence.conflictsToMerger`.
