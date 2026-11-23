# Notes for the daemon-go job's steps

The derivations and the history of `.github/workflows/ci-job-daemon-go.yaml`, which points into
this file. Each `# why:` line of that workflow names the section below that holds the derivation
or the record its job or step rests on. A value's own arithmetic stays beside the value.

## Why the race step is a job of its own

The workflow holds two jobs, which run at the same time: daemon-go-race runs the race step, and
daemon-go runs the plain step, the race-impossible step and the Stage 1 proof. The plain step's
package list is every package of the module less the race step's five (and the text checks), as
`.github/scripts/ci_job_reads.py` builds both from one list, so the two steps share no package and
nothing orders them.

In one job their times added up. Over the 158 green daemon-go jobs of ci.yaml runs on 2026-11-22
from 13:46Z to 18:10Z (jobs API; the sample is the job list read at 18:57Z), the race step took
26.8 min at the median (36.3 at most) and the plain step 12.0 (15.0), after 1.4 min of setup, for
a job of 41.0 min. At the same time, the race job alone sets the wall time, and the second runner
costs the setup it repeats, about 1.4 min.

The split makes no test do less work. The race step's time is one package's: internal/daemon took
1,352 s of the step's 1,422 s in run 45048924209, and the four other packages of the step ended
inside it. That package's tests are where the rest of the time is.

## Each job's limit, held to the installs it runs

Each job's limit is held, beside what its comment derives from its go test steps, to every wait on
the network the job makes, so each one fails by name before GitHub's cancellation, which names
nothing: up to each step that installs, the bounds of every step before it (its own limit, or its
installs and its go tests' own `-timeout` where it sets none), the runner's pulls and the step's own
installs. Both jobs make the same waits but for the NATS images:

- 180 s: before the first step the runner pulls the Postgres service image with `docker pull`,
  which has no bound of its own and takes none from a workflow, so the job's limit is its one
  bound; it counts at what a healthy pull takes (`.github/ci-notes/ci.md`, "The runner's own pulls
  of a job's service and container images").
- 490 s: the two apt commands of `.github/scripts/runner-tmux.sh`, 245 s each (three attempts of
  apt's 60 s, each killed 15 s after its SIGTERM, with two 10 s pauses). They run only on a runner
  image without tmux, which a later image can be, so they count.
- 1,207 s: `.github/actions/install-omp`'s download of the pinned Oh My Pi, `curl --retry 3
  --retry-all-errors --max-time 300`: four attempts of 300 s, with curl's own pauses of 1, 2 and
  4 s between them, before the action fails by name.
- 425 s for each NATS test image `.github/actions/pull-test-images` pulls: one in daemon-go
  (testnats.Image), three in daemon-go-race (Image, SkippingImage and Envoy's HubImage).

daemon-go: 180 s + 490 s + 1,207 s + 425 s = 2,302 s, which its 60 minutes (3,600 s) hold.
daemon-go-race: 180 s + 490 s + 1,207 s + 1,275 s = 3,152 s, which its 70 minutes (4,200 s) hold.

The Postgres image `scripts/e2e/stage1-skeleton.sh` pulls (425 s) is left out: the pull stands in
that script's `if [ -z "${LEGION_E2E_PG_DSN:-}" ]` branch, the devbox's path, and daemon-go's env
sets LEGION_E2E_PG_DSN, so in that job the branch never runs. A job that dropped the variable
would count the pull again.
