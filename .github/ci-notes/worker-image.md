# Notes for the worker image's build

The derivations of `.github/workflows/worker-image.yaml`, which points into this file. Each
`# why:` line of that workflow names the section below that holds the derivation its value rests
on. A value's own arithmetic stays beside the value.

## The build job's limit, held to the build's downloads

Every curl download of `packages/daemon/docker/worker.Dockerfile` goes through
`.github/scripts/fetch.sh`: up to three attempts of its bound, with curl's pauses of 1 s and 2 s,
and no attempt started after twice the bound, so one fetch fails by name inside 3 × its bound + 3 s
(fetch.sh's header says the form). An archive's bound is 300 s, so 903 s at worst; a key's or a
text file's 60 s, so 183 s. The go-licenses fetch runs through `.github/scripts/network-install.sh`'s
bound for go: 425 s (three attempts of 120 s, each killed 15 s after its SIGTERM, with two 10 s
pauses). Each RUN, at its worst:

- tools, mise: one archive, 903 s.
- tools, Oh My Pi, jj, gh and gh-stack: four archives, 3,612 s.
- go-licenses: one go fetch, 425 s.
- toolchain: seven archives (uv, Node, the AWS CLI, Go, kubectl, yq, Chromium), 6,321 s.
- toolchain, the Google Cloud SDK: one archive, 903 s.
- runtime-base, the PostgreSQL key: 183 s.
- notices: seventeen license files and seven checks that an address answers, 24 × 183 s = 4,392 s.

They sum to 16,739 s, but BuildKit builds the stages beside each other, and a RUN waits only for the
RUNs before it in its own stage and for the stages it copies or mounts from. So the build's worst
case is its longest chain of them: tools (mise and the four archives, 4,515 s) and then the notices
stage built on tools (4,392 s), 8,907 s. The other chains are shorter: the toolchain stage's two
RUNs, 7,224 s, which the notices stage's assembly and the runtime stage wait for; the go-licenses
stage, 425 s; runtime-base, 183 s.

The rest of the build is measured. The "Build and push" step took 660 s at its slowest of 27 green
builds read on 2026-11-23, job 137913895923 (release.yaml run 45236058230; the others are the 25
newest green release.yaml builds and the two pull-request builds of this change, 137748816723 and
137784493728), and that time holds every install the build makes outside these two scripts (apt-get,
bun install, go install, go mod download, npm ci and uv), which have no bound of their own but this
limit. The job's other steps took 180 s in that same job, 12 s of them the builder's setup, whose
pull of its BuildKit image is counted as the runner's own pulls are, at 240 s
(`.github/ci-notes/ci.md`, "The runner's own pulls of a job's service and container images"):
180 s - 12 s + 240 s = 408 s.

So the limit is 660 s + 8,907 s + 408 s = 9,975 s, rounded up to 167 minutes. The 30 minutes the job
had would end a build whose downloads stalled in GitHub's cancellation, which names nothing,
before fetch.sh failed one by name.
