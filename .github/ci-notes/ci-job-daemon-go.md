# Notes for the daemon-go job's steps

The derivations of `.github/workflows/ci-job-daemon-go.yaml`, which points into this file. Each
`# why:` line of that workflow names the section below that holds the derivation its value rests
on. A value's own arithmetic stays beside the value.

## The job's limit, held to the installs it runs

The job's limit must leave room for two things: each go test step's own `-timeout`, so a hung test
prints the goroutine dump that names it (the comment above the limit says how that sums to under
4,000 s), and every wait on the network the job makes, so each one fails by name before GitHub's
cancellation, which names nothing.

The slowest green run of this job in the 20 merge-group runs `.github/scripts/network-install.sh`'s
bounds were measured on (44633135919 to 44668680855) took 2,302 s, job 136049107591. Before its
first step the runner pulls the Postgres service image with `docker pull`, which has no bound of
its own and takes none from a workflow, so the job's limit is its one bound: rule 22 of
`.github/scripts/check-ci-filters.sh` counts it at 180 s, what a healthy pull takes (PULL_SECONDS,
in `.github/scripts/ci_network_installs.py`). The installs the job runs through network-install.sh
can take 2,190 s before the last of them fails by name, each at three attempts cut at its kind's
bound, killed 15 s after its SIGTERM, with two 10 s pauses:

- the two apt commands of `.github/scripts/runner-tmux.sh`, 245 s each, which run only on a runner
  image without tmux;
- the three NATS test images `.github/actions/pull-test-images` pulls, 425 s each;
- the Postgres image `scripts/e2e/stage1-skeleton.sh` pulls, 425 s, which runs only where
  `LEGION_E2E_PG_DSN` is unset (a devbox; this job sets it): rule 22 cannot read that condition, so
  it counts the pull.

So the limit is 2,302 s + 180 s + 2,190 s = 4,672 s, rounded up to 78 minutes, which also holds the
tests' timeouts.
