# Notes for the repeated Dispatch unit suite

The derivations and the history of `.github/workflows/dispatch-unit-repeat.yaml`, which points
into this file. Each `# why:` line of that workflow names the section below that holds the
derivation its step, its job or its key rests on. A value's own arithmetic stays beside the value,
in the matrix's `include` rows.
In the text here, "the workflow" is `.github/workflows/dispatch-unit-repeat.yaml`, and "the tool"
is `packages/dispatch/e2e/tools/unit-repeat.ts`, which `bun run test:repeat` runs.

## The budgets measured from CI's own runs

Each lane's budgets are a hang guard and nothing more: a leg's verdict is what its rows print, and
a budget only ends a leg that will not end by itself. So each budget is set from the slowest leg
measured on main's suite, with a margin of twice that, and the matrix row of each lane writes the
measurement beside the budgets:

- `slowest_seconds`: the slowest test step of the lane's measured legs;
- `other_steps_seconds`: the slowest the job's other steps took together in one leg (the job's
  time minus its test step's), the checkout, Bun, the install, and for the harness lane zsh's
  install and PostgreSQL's start;
- `deadline_seconds`: twice `slowest_seconds`, up to the next whole minute, the time at which the
  step's `timeout` stops the tool (the next section);
- `step_minutes`: the deadline, the 60 s `timeout --kill-after` leaves the tool to report, and
  60 s of slack for the step's own start around `timeout`;
- `job_minutes`: `step_minutes`, and twice `other_steps_seconds` up to the next whole minute.

`packages/dispatch/e2e/__tests__/unit-repeat.test.ts`, which the dashboard job of `ci.yaml` runs
on every change to the workflow, holds each lane's three budgets to those rules, and holds the job
and the step to the matrix's values.

### What was measured, on 2026-11-20

The step times of every leg of 15 runs of the workflow, read from
`gh api repos/cybermonklabs/legion/actions/runs/<id>/jobs` and from the legs' artifacts, 45 legs a
lane:

- the ten runs before 11:00Z that day (runs 44681726917 to 44712651976, pull requests and
  hand-started runs on five branches, each a merge of its head with main or that head);
- run 44726566359, started by hand at main `1c7c825367`;
- four runs at the heads of the pull request that set these budgets, on main `5570d0acd2`: runs
  44751162563 (started by hand) and 44751677485 (the pull request's own) at its first head, and
  runs 44759854373 and 44759866208 at its second.

A leg stopped before Bun ended has no time of its own, and no report. Its time is estimated from
its `bun.log`, which holds a result line for each row that ended: the step's time, times the
results a whole leg of the same lane of that run wrote, over the results the stopped leg's log
holds.

| Lane | Legs whose Bun ended by itself | Legs stopped before Bun ended | Slowest | Old step budget |
| --- | --- | --- | --- | --- |
| web | 29, 726 to 1,954 s | 16, by the budget at 972 or 973 s, estimated at most 1,133 s | 1,954 s, run 44759854373, round 1 | 16 minutes |
| web-cold | 43, 94 to 158 s; 9 of them past the budget, so their steps failed with no row failed | 2, by the budget at 132 and 133 s | 158 s, run 44751677485, round 1 | 2 minutes |
| harness | 44, 1,075 to 2,301 s | 1, by a cancel of its run at 2,274 s, estimated 2,283 s | 1,496 s, run 44759866208, round 2 | 30 minutes |

The web lane grew within the day: main's `a8067fcd51` (11:41Z) added
`web/src/features/issue/useIssueDrafts.test.ts`, whose rows took 1,168 s together over a leg's 20
runs, and its rows a run went from 6,296 (run 44726566359) to 6,462 (run 44751162563). So the
four runs on main `5570d0acd2` hold the slowest legs of every lane. The same suite's web legs took
1,202 to 1,954 s across those four runs: one runner can take 1.6 times another's time, which is
the spread the margin of 2 is there for.

The harness lane's slowest leg is read over the 36 legs of the branches that change no harness
row. Pull request #4236 adds harness rows whose real-time waits take minutes, and its 9 legs took
1,651 to 2,301 s; it sets its own harness budget, and 2,301 s is inside the deadline set here.

The other steps took at most 37 s in a web leg (run 44751162563, round 3), 28 s in a web-cold leg
(run 44712085178, round 2) and 229 s in a harness leg (run 44709413418, round 1, most of it zsh's
install).

The largest job budget, 70 minutes for a web leg, is far inside GitHub's own limit for a job on a
GitHub-hosted runner. GitHub's "Actions limits" page
(https://docs.github.com/en/actions/reference/limits, read on 2026-11-20) says: "Each job in a
workflow can run for up to 6 hours of execution time." Each step's budget is inside its job's,
and each deadline inside its step's, so `timeout` fires first, then the step budget, then the job
budget, and GitHub's limit never decides.

### Why the old budgets stopped legs that were passing

The old budgets were the slowest leg plus 15 per cent, measured over the workflow's runs of
2026-11-17 to 2026-11-19, when a web run read 4,885 to 5,189 rows; main's suite then grew to
6,462 rows a run. So in the first 11 runs above, the budget stopped 16 web and 2 web-cold legs
before Bun ended, and failed 9 more web-cold legs after Bun had ended. Three of those nine are the
web-cold failures of the hour of 09:00Z on 2026-11-20 (jobs 136196998929, 136166391921 and
136207986486): in each, Bun had ended by itself with 6,214 rows passed and none failed, after
125.0, 131.3 and 125.4 s, and the tool had written a clean report, when the step's two-minute
budget failed the step. No row was running long; the lane was.

### What a run costs now

A run that passes takes what its rows take: the runs read cost 115 to 164 billed minutes, and $0,
since standard runners are free for a public repository. The budgets bound only a leg that hangs:
at most 70 minutes for a web leg, 9 for a web-cold leg and 60 for a harness leg.

### When to measure again

When a leg's report says it was stopped, and the file it names passes in the legs that ended by
themselves, the suite has outgrown the deadline: read the legs again as above, add them to the
window, and set the matrix's measurements and budgets from the new reading by the same rule. A
leg that ends by itself above a lane's `slowest_seconds` is read the same way: it moves the
slowest, and the budgets follow it, so a budget never rests on a margin its own window breaks.

## A leg's deadline, and what a stopped leg names

A step that GitHub ends at its `timeout-minutes` is ended from outside, and says only that the
action "has timed out": the tool never learns of it, so no report names a row, and the 18 legs the
old budgets stopped before Bun ended, in the reading above, wrote no report at all. So the step's
own deadline comes first, inside its budget, and the tool is told of it:

- The step runs `timeout --foreground --signal=TERM --kill-after=60 <deadline_seconds> bun run
  test:repeat ...`. At the deadline `timeout` sends SIGTERM to `bun run` alone, and not to its
  process group (`--foreground`), so Bun's workers are left for the tool to stop. `bun run` passes
  the SIGTERM on to the tool, through the script's `&&` too (measured on Bun 1.4.4: a script
  `true && bun <file>` under `timeout` got the signal in that file's process).
- The tool adds `packages/dispatch/e2e/tools/unit-repeat-progress.ts` to the lane's preloads. That
  preload appends a line to `<out>/progress.jsonl` as each worker loads a test file, and as each
  run of the file and each row of the run begins and ends.
- On the SIGTERM the tool reads that file first, and which of its workers are alive. Then it passes
  the signal on to Bun, and kills Bun 10 s later if Bun has not ended (Bun 1.4.4 ended 0.08 s after
  a SIGTERM with a worker spinning in a row that never yields).
- Once Bun has ended, for each file a live worker was in, the tool runs that file alone under the
  leg's seed with the preload failing every row before its body, which prints the file's rows by
  name in the order the seed runs them (measured against the order the bodies ran in, under five
  seeds). That run is killed at 20 s.
- The report, the step summary and one `::error` for each such file name the file, the run of it
  and the row of that run by its place and its name, and give the command that runs the file again
  in that order. The tool exits 1, and `<out>/progress.jsonl` stays in the leg's artifact.

A stopped leg's error then reads, for a planted row that spins for ever in a copy of the package:

```text
::error file=packages/dispatch/e2e/__tests__/planted.test.ts,title=Running when the leg was stopped::e2e/__tests__/planted.test.ts: in row 3, "planted spins", of run 1 of 2, 9.6 s since it began, when SIGTERM stopped the leg after 9.8 s (seed 2743885038). Run it again from packages/dispatch: bun run test:harness e2e/__tests__/planted.test.ts --seed=2743885038
```

That reading is the step's own `run:` text with the harness leg's values, two reruns and a 10 s
deadline, over that copy, under a 40 s `timeout` that stands in for GitHub's step budget: the leg
ended after 11 s and named the row. The workflow before this deadline, at main `1c7c825367`, ran
the same copy to the whole 40 s and printed no error and no report. `unit-repeat.test.ts` drives
the same stop through the tool on every change to it.

What a stop cannot name. A file whose run had not begun is named as being in its own code, which
Bun runs again for each run of it; a file between two rows is named with the row that ended last;
and where the run that reads a file's names fails or reaches its 20 s, the row is named by its
place alone and the report says why its name was not read. A file of a worker that was gone at
the stop is not named as running. Bun's junit file gives a `(worker crashed)` testcase to each
file whose results it had not taken in when the stop came: the file in hand, and also, measured
on 2026-11-20, a file whose worker had ended its rows and exited a moment before. That testcase
cannot tell a worker that crashed before the stop from one the stop ended, so after a stop the
report lists each such file as one whose results Bun did not report, in the stop's own failure,
and makes no failing row of it.
