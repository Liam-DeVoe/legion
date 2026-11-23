# Notes for the worker image's build

The derivations of `.github/workflows/worker-image.yaml`, and of the bounds of the downloads of
`packages/daemon/docker/worker.Dockerfile`, which point into this file. Each `# why:` line names
the section below that holds the derivation its value rests on. A value's own arithmetic stays
beside the value.

## The bound of each download

Every curl download of the Dockerfile goes through `.github/scripts/fetch.sh`, whose bound is the
one network-install.sh's kinds follow: twice the slowest green fetch of that download, rounded up
to the minute. The fetches were read off BuildKit's own clock (the seconds since a RUN started
that it prints before each line of the RUN's output) in the 27 green builds of 2026-11-23: the 25
newest green worker_image builds of release.yaml and the two pull-request builds of this change,
jobs 137748816723 and 137784493728. Each RUN prints a line right after its downloads end, so:

- mise (tools stage): `mise.tar.gz: OK`, at most 0.98 s, job 137826539543 (run 45212669243).
- Oh My Pi, jj, gh and gh-stack (tools stage): the `tools: <tool>: <url>, sha256 ...` line each
  fetch prints after its check, less the line before it: Oh My Pi at most 3.10 s, job
  137784493728 (run 45201056167); jj 0.49 s, job 137826539543; gh 0.54 s, job 137784493728;
  gh-stack 0.66 s, job 137787948171 (run 45197933390), in the 6 builds that had it.
- uv, Node, the AWS CLI, Go, kubectl, yq and Chromium (toolchain stage): the first `: OK` of the
  sha256sum after all seven, at most 6.69 s for the seven together, job 137646499135 (run
  45158739984).
- The Google Cloud SDK: `sdk.tar.gz: OK`, at most 2.29 s, job 137749053475 (run 45190750789).
- The seventeen license files and seven checks of the notices stage print nothing until the RUN
  ends: the whole RUN took at most 8.1 s, job 137787948171.
- The PostgreSQL key (runtime-base stage) is fetched in the RUN that installs the image's Debian
  packages, whose output does not set it apart; the browser image fetches the same key from the
  same address, at most 0.71 s (`.github/ci-notes/browser-image.md`).

Twice each of those is under a minute, so every call's bound is 60 s. A fetch then fails by name
inside 3 × 60 s + 3 s = 183 s (fetch.sh's header says why). The go-licenses fetch runs through
`.github/scripts/network-install.sh`'s bound for go: 425 s.

## The build job's limit, held to the build's downloads

BuildKit builds the stages beside each other, and a RUN waits only for the RUNs before it in its
own stage and for the stages it copies or mounts from. That is measured in the builds above, read
from each RUN's `DONE <s>s` line and the time the log took it: in job 137784493728 (run
45201056167) the tools stage's archive RUN ran 10.4 s, every second of it inside the toolchain
RUN's 30.0 s (both from about 15:05:32Z), and the notices stage's fetch RUN ran its 6.2 s inside
the Google Cloud SDK RUN; job 137748816723 (run 45190825753) shows the same two overlaps, 6.9 s and
3.7 s. So the build's worst case is its longest chain of downloads, not their sum:

- tools: mise and the four archives, 5 × 183 s = 915 s, and then the notices stage, built on the
  tools stage: 24 × 183 s = 4,392 s. 5,307 s.
- toolchain: its seven archives, then the Google Cloud SDK, 8 × 183 s = 1,464 s.
- go-licenses, 425 s; runtime-base, 183 s.

The rest of the build is measured. The "Build and push" step took 660 s at its slowest of those 27
builds, job 137913895923 (release.yaml run 45236058230), and that time holds every install the
build makes outside these two scripts (apt-get, bun install, go install, go mod download, npm ci
and uv), which have no bound of their own but this limit. The job's other steps took 180 s in that
same job, 12 s of them the builder's setup, whose pull of its BuildKit image is counted as the
runner's own pulls are, at 240 s (`.github/ci-notes/ci.md`, "The runner's own pulls of a job's
service and container images"): 180 s - 12 s + 240 s = 408 s.

So the limit is 660 s + 5,307 s + 408 s = 6,375 s, rounded up to 107 minutes. The 30 minutes main
gives the job would end a build whose downloads stalled in GitHub's cancellation, which names
nothing, before fetch.sh failed one by name.
