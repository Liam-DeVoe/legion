# Notes for the download-retry lanes' steps

The derivations and the history of `.github/workflows/ci-job-download-retry.yaml`, which points
into this file with `# why:` lines. A value's own arithmetic stays beside the value. What the
jobs' rigs are and how they work is in `.github/ci-budgets.md`, "The download-retry job's rig" and
"The download-retry-images job's rig"; the second rig is the download-retry-images job's and the
download-retry-images-worker job's both.

## Each image build's budget: the gate's price for that build

The `--budget` each report step of the download-retry-images and download-retry-images-worker
jobs holds its build to has three terms.

The first is the price the gate itself puts on that build: its measured build time plus the
longest single ladder of the Dockerfile it builds, the two terms the price comment of the key
holding that build carries where the image is built for real
(`.github/scripts/check-download-retry.sh`, rule 10). A build that runs past the price its own key
puts on it still fails.

The second is the probe's 20 s. None of those keys holds the probe, which is this rig's own:
`report` measures a build from the moment its proxy started, and the probe's two requests are
made between the two, each bounded at 10 s by the rig's PROBE_BOUND. Every site of the
download-retry job carries the same 20 s.

The third is 70 s for each listed host the build reaches (dispatch://INFRA-1545, round 63). Each
arm's proxy runs `--per-host-window`, so each host its `--hosts` lists answers 504 for 60 s from
that host's own first request, wherever in the build that request comes, and is passed on after.
Until round 63 one window ran from the proxy's start, and a host the build first reached after its
first minute met no fault at all: the worker image's seven model-catalog hosts, which the final
RUN's `legion probe-image` reaches about 529 s in, were listed and never faulted. What a window
per host costs a build, and why the count of hosts bounds it:

- docker build stops at the first RUN that fails, so at most one ladder runs out, and the build
  has failed by then. The longest single ladder in the first term pays for that.
- A RUN that passes waits out at most one window for each listed host it is the first to reach.
  A 504 answers at once, so the attempts inside a window cost about nothing, and the inline
  retry sleeps 10, 20 and 40 s, so its fourth attempt starts 70 s after its first, the first
  attempt after a 60 s window. So one window costs a RUN 70 s.
- Hosts are counted as the proxy keys its windows, by the host a request names, lower-cased, not
  by the `--hosts` entry it matched. The hosts one RUN first reaches together share one 70 s
  wait, so the count is an upper bound.

The hosts counted are the hosts each arm's `--hosts` lists that a RUN line of its Dockerfile
reaches, from round 61 and 62's table of every host the four builds fetch from, with the hosts
images job 138228770096 (at f3e50c3f) showed each proxy passing:

- The worker image, 63 hosts: deb.debian.org; registry.npmjs.org; github.com,
  objects.githubusercontent.com, release-assets.githubusercontent.com and
  raw.githubusercontent.com; mise.jdx.dev; nodejs.org; awscli.amazonaws.com; go.dev and
  dl.google.com; dl.k8s.io and cdn.dl.k8s.io; storage.googleapis.com; releases.astral.sh; pypi.org
  and files.pythonhosted.org; www.postgresql.org and apt.postgresql.org; cdn.playwright.dev and
  playwright.download.prss.microsoft.com; azcliprod.blob.core.windows.net; proxy.golang.org,
  sum.golang.org, golang.org and google.golang.org; go.googlesource.com; cloud.google.com;
  dl-cdn.alpinelinux.org; registry.yarnpkg.com; the go-get hosts authelia.com and
  www.authelia.com, cel.dev, codeberg.org, dario.cat, dmitri.shuralyov.com, filippo.io,
  git.sr.ht, go.etcd.io, go.opencensus.io and contrib.go.opencensus.io, go.opentelemetry.io,
  go.uber.org, go.yaml.in, go4.org, gonum.org and www.gonum.org, gopkg.in, gotest.tools,
  honnef.co, k8s.io and sigs.k8s.io, modernc.org, mvdan.cc and rsc.io; gitlab.com; and Oh My
  Pi's model catalog, catalog.stencil.so, api.commandcode.ai, api.kilo.ai, api.venice.ai,
  coding-intl.dashscope.aliyuncs.com, hyper.charm.land and zenmux.ai. www.authelia.com is where
  authelia.com's go-get page sends the go command (the Envoy arm's proxy passed both), and
  www.gonum.org is where gonum.org's does.
- The Envoy image, 24 hosts: registry.npmjs.org; deb.debian.org; proxy.golang.org,
  sum.golang.org, golang.org and google.golang.org; storage.googleapis.com; github.com, which its
  proxy passed ten requests to in job 138228770096; and the go-get hosts authelia.com and
  www.authelia.com, cloud.google.com, dario.cat, dmitri.shuralyov.com, go.opencensus.io,
  go.opentelemetry.io, go.uber.org, go.yaml.in, gonum.org and www.gonum.org, gopkg.in,
  gotest.tools, honnef.co, k8s.io and rsc.io.
- The held-port image, 29 hosts: proxy.golang.org, sum.golang.org, golang.org and
  google.golang.org; storage.googleapis.com; and the go-get hosts authelia.com and
  www.authelia.com, cel.dev, charm.land, cloud.google.com, codeberg.org, dario.cat, git.sr.ht,
  go.mongodb.org, go.opencensus.io, go.opentelemetry.io, go.uber.org, go.yaml.in, gocloud.dev,
  gonum.org and www.gonum.org, gopkg.in, gotest.tools, honnef.co, lukechampine.com, modernc.org,
  mvdan.cc, pgregory.net and rsc.io. Its golang image carries no git, so it clones nothing.
- The browser image, 5 hosts: www.postgresql.org and apt.postgresql.org;
  azure.archive.ubuntu.com and security.ubuntu.com; deb.nodesource.com.

The four budgets:

- The worker image: the key of `.github/workflows/worker-image.yaml`'s `build` job, which holds
  that build and no other fetch but the ci-ok report: 1800 s, the 30 minutes the job carried,
  which holds its build, plus the `go` stage's `go mod download` of
  packages/daemon/docker/worker.Dockerfile, 5 x 900 + 150 = 4650 s. That key names no measured
  build of its own, so its 1800 s is the measured term here too.
  1800 + 4650 + 20 + 63 x 70 = 10880 s.
- The Envoy image: the key of `.github/workflows/ci-job-envoy-smoke.yaml`'s job, whose comment
  names this build's own 203 s (job 133743044741), plus the `bun install` and the `go mod
  download` of packages/envoy/docker/Dockerfile at 5 x 600 + 150 = 3150 s each.
  203 + 3150 + 20 + 24 x 70 = 5053 s.
- The held-port image: the same key, whose comment names this build's 11 s in the same job, plus
  the wrapper stage's `go mod download` of packages/envoy/infra/host/heldportwrapper/Dockerfile,
  5 x 600 + 150 = 3150 s. 11 + 3150 + 20 + 29 x 70 = 5211 s.
- The browser image: the key of `.github/workflows/browser-image.yaml`'s "Build and push" step:
  66 s, the slowest of the image's 33 cold builds measured in that job (job 128991520632 of run
  42565616054), plus the package set's `apt-get install` of .github/docker/browser.Dockerfile,
  5 x 300 + 150 = 1650 s, above the signing key's and the index read's 750 s each.
  66 + 1650 + 20 + 5 x 70 = 2086 s.

The four builds no longer fit one job. With its 300 s of base image pulls, the 900 s smoke build
and about 200 s of checkout, bypass clearing and rig steps, the one job came to
1400 + 10880 + 5053 + 5211 + 2086 = 24630 s, and with the single slowest retried step that the
Budgets note adds, the worker image's 4650 s ladder, to 29280 s, above the 360 minutes GitHub
gives a job on a hosted runner. So the worker image's build runs in a job of its own,
download-retry-images-worker, at 200 + 10880 = 11080 s and 11080 + 4650 = 15730 s, 263 minutes;
the other three stay in download-retry-images, at 1400 + 5053 + 5211 + 2086 = 13750 s and
13750 + 3150 = 16900 s, 282 minutes, where 3150 s is the longest single ladder of the three
Dockerfiles that job builds. Each key is the whole minute strictly above its sum, and each job's
comment derives it.

The Envoy and held-port arms take each build's own measured time, which envoy-smoke's key names,
rather than the 1200 s that key carries for its whole job: that term holds both builds and the
job's tests together, so charging it to each arm would count it twice.

Until job 136575882603 of the run at 95587c62 failed, these arms carried 2720, 920, 500 and 384 s,
each set by hand from one measured build and a margin. The browser's was 100 + 20 + 264 = 384 s:
the recovering fault lets the first attempt through no later than 100 s after the proxy starts,
the probe's 20 s, and four times the 66 s build, about the margin the Envoy image's 920 s kept
over its 239 s faulted build in job 133724372176. In job 136575882603 the browser arm took 915 s:
the injected fault on the PostgreSQL signing key recovered on its fourth attempt, then
azure.archive.ubuntu.com, outside the injected fault, served at 121 kB/s, so two `apt-get install`
attempts reached their 300 s bound and the third succeeded. The retry held; the budget had assumed
that every fetch after the fault meets a healthy host. That job's four arms took 540, 272, 130 and
915 s, each under the budget above.

From then until round 63 they carried the first two terms alone, 6470, 3373, 3181 and 1736 s, and
one job held the four builds at 347 minutes: 16160 + 4650 = 20810 s. In images job 138228770096
(at f3e50c3f) the four took 607, 264, 132 and 99 s under one window from each proxy's start.

## The sites lane's arms with a window per host

Three recover arms of the download-retry job run their proxy with `--per-host-window`
(dispatch://INFRA-1545, round 63), because each first reaches a host its fault lists only once
another listed host has passed, after one window from the proxy's start had closed. Job
138228769946 of run 45329962650 (at f3e50c3f) showed it:

- recover-changie and recover-omp fault github.com and githubusercontent.com. Their release
  asset's URL on github.com redirects to release-assets.githubusercontent.com, which a client can
  reach only once github.com answers: at +68 s and +75 s in that job, after the window, so that
  host was listed and never faulted.
- recover-go faults every host. proxy.golang.org sends a module zip to storage.googleapis.com, and
  the go command reads sum.golang.org after a download, so both are reached only once
  proxy.golang.org answers: at +80 s in that job, and neither was faulted.

With a window per host each of those hosts answers 504 for 60 s from its own first request. Each
arm's budget takes one window for each listed host it first reaches late, at 70 s each, the
derivation the images arms' budgets use: a 504 answers at once, and the helper's sleeps of 10, 20
and 40 s put an attempt 70 s after the one that met the window. On top of its stall twin's figure,
the ladder its own retry guarantees (.github/ci-budgets.md, "The download-retry job's rig"):

- recover-changie: 710 + 1 x 70 = 780 s, for release-assets.githubusercontent.com.
- recover-omp: 530 + 1 x 70 = 600 s, for release-assets.githubusercontent.com.
- recover-go: 1130 + 2 x 70 = 1270 s, for storage.googleapis.com and sum.golang.org.

The ladders and the steps' own keys are unchanged, and a step cannot outlast its own key:
install-omp's 8 minutes and the Go download's 18, with the probe's 20 s, are 500 s and 1100 s,
under the budgets above. The two arms of github.com list githubusercontent.com first, because the
probe asks for the first entry by its bare name and opens that host's window
(.github/scripts/download-retry-proof.sh, `probe`): no site fetches githubusercontent.com bare,
and each fetches github.com. The job's key moves with the recover half's sum, from
6740 s to 7020 s: .github/workflows/ci-job-download-retry.yaml's comment above the job's id
derives 14710 + 3416 = 18126 s and 303 minutes, where it read 298.
