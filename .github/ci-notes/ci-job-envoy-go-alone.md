# Notes for the envoy-go-alone job's workflow

The derivations and the history of `.github/workflows/ci-job-envoy-go-alone.yaml`, which points
into this file. Each `# why:` line of that workflow names the section below that holds the
derivation its service or its step rests on, and the sections stand in the order their comment
blocks stand in the workflow file. A value's own arithmetic, the job's timeout-minutes and the
limits' table among it, stays beside its value there, and so does a comment inside a `run:` body,
which is shell and stays with its command: .github/scripts/ci_job_reads.py holds the step's
`run:` text, comments and all, to the copy it keeps (ALONE_JOB_SCRIPT), and check-ci-filters.sh
fails when the two differ. In the text moved here, "this job" is the envoy-go-alone job.

## The shutdown feed test's server of its own

A second instance, for the one untagged test of cmd/dispatch that reads a live event feed
(TestSIGTERMWithOpenStreamsExitsPromptlyAndSettlesTheOwedDocument, shutdown_test.go). It waits
on events_start exactly as the routes package does, and in run 42581214577 it timed out on it
with the server naming the holder: "event horizon held back by an open transaction …
database=dispatch_test_2be55348167c1ee8", a database of another package of envoy-go, then its
job, pending_for 5.14 s. It could not share envoy-go's routes service either, in either
direction: routes' own tests hold instance-wide transaction ids on purpose (whoami_test.go's
pg_current_xact_id hold, its cluster-database holder and its out-of-order insert), and routes'
horizon loop has already been blown once by a sibling's `create database … strategy file_copy`
(run 41868325451), which is what this test's own harness does. So it got a server no other test
touches, on 5434, in envoy-go, and it keeps that server here: the alone call, which runs after
it, runs against the first service (dispatch://INFRA-1075).

## What the job runs, and why it is a job of its own

The tests of envoy-go's package list whose assertion is a bound on how long something takes,
measured from the test's own clock, main's or the branch's, serial or parallel, and whose bound is
under twice their slowest reading in the pair at 8cfd2682 and the dispatch runs swept since (the
architect's rule of 2026-11-21T22:17Z, and the search of the whole package list that applied it,
dispatch://INFRA-1075/artifact/evidence-r10-bounds-md): 40 tests in 13 packages,
.github/scripts/ci_job_reads.py's UNIT_ALONE_TESTS, which a test joins by that rule. Beside
envoy-go's load such a test reads that load rather than the product: two failures in the package
list started the list, TestAnOutboxThatLosesNATSReleasesThePublisher, of
internal/dispatch/outbox, which failed main's bound on how soon the cut process releases the
publisher hold in the dispatch run 44939290502 at 3a4ffe0b (job 136952816413) while 106 tests of
internal/dispatch/pmdoc ran beside it, and TestAStandbyTakesAFreeHoldWithNoNotification, of
internal/dispatch/textversions, which failed main's bound on how soon a standby takes a free hold
in the dispatch run 44953084676 at 4165260d (job 136995819951), where main's envoy-go had passed
that package 24 times. The race lane's tests of that kind run alone in steps of their own; no
race job runs these packages. envoy-go's package list skips each of them by name, and this job
runs them, one package and one test at a time: the shutdown feed test first, in its own call on
its own service, and the rest in the alone call on the first service.

The two calls ran at the end of envoy-go's unit step, once its other calls had ended, through the
dispatch run 44979729824 at 66cba4b8, where the rows-alone call's stream ran 278.2 s from its first
event to its last. Then TestARollingUpdatesSuccessorHandsEachMentionTheOldProcessLeftOnce joined
the list (the architect, 2026-11-22T08:44Z): its bound of 195 s, model.DeliveryClaimLapse +
deliverySweepInterval + sweepMargin, is a product deadline the hand-over must beat, and its
slowest reading, 105.85 s in that run (job 137077480236), is over half of it. The test spends
about 118 s on that product timer whatever the load (117.79 s from the old process's exit in that
run), which at the end of envoy-go's unit step would take envoy-go past envoy-go-race-docs, the
lane's longest job. So the two calls moved into this job of their own, which runs beside envoy-go
(the architect, 2026-11-22T08:45Z): it reuses envoy-go's filter and `changes` output, so it is held
to the merge queue and to `full-ci` exactly as envoy-go is, and envoy-go's go-envoy-test restore,
since it builds a part of what envoy-go's package list builds.
