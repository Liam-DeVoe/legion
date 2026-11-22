# Notes for the Dispatch browser nightly workflow

The derivations of the `quarantine-check` job of `.github/workflows/dispatch-browser-nightly.yaml`,
which points into this file. Each `# why:` line of that job names the section below that holds
the derivation its job or its step rests on, and the sections stand in the order their comment
blocks stood in the workflow file. A budget's own arithmetic stays beside the budget, in the
workflow file. The workflow's other comments still stand in it: its bodies are not moved yet
(.github/ci-notes/README.md), and this job is the first of it to hold its derivations here.
In the text moved here, "this workflow" refers to the workflow,
`.github/workflows/dispatch-browser-nightly.yaml`.

## The quarantine check, what it reads and when it runs

The quarantine's checks that depend on the date (R4 of dispatch://INFRA-983):
packages/dispatch/e2e/tools/quarantine-check.ts reads the `dispatch-browser flaky row` notices
of the merge jobs of the last 14 days of scheduled runs of this workflow and merge-group runs
of ci.yaml, and fails on a row flaky in two of them that packages/dispatch/e2e/quarantine.ts
does not hold, and on an entry of that file held longer than 14 days, each named in its
summary with the runs and the lines to add or to remove. A pull request's runs are not read,
since a pull request's head can carry its own defect; the tool's head says the rest. It needs
no other job of this run: what it reads is runs that have already ended, this one's not among
them, so a flaky row of tonight's full lane is counted by tomorrow's check.

The 02:17 schedule and a workflow_dispatch alone, not the 13:41 one: a second reading of the
same 14 days half a day later says nothing the first did not. Each reading spends about 34 REST
calls and 13 GraphQL calls of the repository's budgets (the measurement in
.github/ci-notes/dispatch-browser-nightly.md#the-quarantine-checks-measured-steps-and-its-budget).

## Why the quarantine check holds those three scopes, and what it reads of the checkout

`actions: read` is the run listing of the two workflows, `checks: read` the merge jobs' check
runs and their annotations, read through GraphQL, and `contents: read` the checkout. The tool
writes nothing to GitHub. For a row flaky in two runs it reads the row's own lines from a
listing of this checkout and their last change from the file's blame at this commit (one
GraphQL request per file, which the tool sends itself with the job's token), so it needs the
install and the contracts build the listing loads; it reads no git history, so the checkout
stays shallow. It counts a flake only from a run created after that change and after the row's
fix, where e2e/quarantine.ts's `fixes` names one, and it reads each fix's commit against this
one through GitHub's compare.

## The quarantine check's measured steps and its budget

The budget, by the rule .github/ci-notes/dispatch-browser.md states for every job. The check
step's work is measured on the reader it had before its check-run reads moved to GraphQL,
which read each run's check runs and each check run's annotations with a REST call apiece: 1,794
calls for 1,166 runs in job 137061958838 of run 44974729436 on 2026-11-22. Over the
nightly's quarantine-check jobs of 2026-11-21 and 22 that succeeded, 17 of them, the step took
77 to 104 s on the runner. The longest was job 137021330193 of run 44961580846, 23:28:02Z to
23:29:46Z on 2026-11-21. 104 x 1.15 = 120 s. The GraphQL reader makes the same listing calls
and reads the check runs and their annotations of 100 suites a call. On 2026-11-22 at 03:15Z,
from this issue's pod, it read 1,195 runs in 34 REST calls and 13 GraphQL calls costing 132
points, in 22 s, so the REST reader's time is an upper bound on its own. The steps without a
budget, at their maximum over the 12 jobs of this workflow's run 44545596758 that ran them:
Set up job 2 s, checkout 12 s, setup-bun 4 s, `bun install` 11 s, the contracts build 1 s, and
the post and completion steps 3 s: 33 s, and 300 + 33 = 333 s rounds up to 6 minutes.

The step's 5 minutes are its work and a wait for GitHub's rate limit. The check failed on that
limit at 02:14Z on 2026-11-22 (job 137061949842 of run 44974726732), when GitHub refused a
check-runs read `API rate limit exceeded for installation` three times, 10 s and 20 s apart.
The tool now waits out a refusal that names the time GitHub may be asked again: `retry-after`,
or `x-ratelimit-reset` where `x-ratelimit-remaining` is 0, for REST and for GraphQL alike. It
waits for no more than GH_READ_WAIT_CAP_SECONDS, which the step sets. Since a refusal of the
step's first read leaves all of the step's work after the wait, the cap is the step's limit
less all of it: 300 - 120 = 180 s. The step's time grows with the merge queue's runs, which the
listing reads a day at a time and the GraphQL reads 100 to a call, so a busier fortnight moves
it: a step that outran its budget names itself.

## Why the quarantine check builds contracts

The step of this name in .github/workflows/dispatch-browser.yaml's shard job, for the reason
.github/ci-notes/dispatch-browser.md#why-every-job-that-runs-the-suite-builds-contracts-first
gives: the listing loads e2e/playwright.config.ts, which imports `@legion/contracts` out of the
build this step makes.
