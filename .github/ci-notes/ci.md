# Notes for the CI router and its filters

The derivations and the history of two files that point into this one:
`.github/ci-filters.yaml`, the path-filter data `.github/workflows/ci.yaml`'s `changes` job
selects jobs by, whose sections come first, and `.github/workflows/ci.yaml` itself, the router,
whose sections follow them. Each `# why:` line of either file names the section that holds the
derivation or the record its filter, its pattern, its step, its job or its key rests on, and
within each of the two the sections stand in the order their comment blocks stood. A comment
block of at most two lines stays where it is, beside the pattern or the step it is about; so
does a value's own arithmetic, and a comment inside a `run:` body, which is shell and stays with
its command.

## How a filter of this file is written

Each filter names every file its job reads: the package and what its tests, typecheck
and build read outside it; the composite actions the job installs its tools through;
for Go, the closure of the packages the job compiles (`go list -deps -test`), the
targets of the go commands its tests start, and the module files of the workspace the
build runs in, whose go.mod files move the versions it compiles; and this file, since a
job reads its own definition. The ci-filters job (.github/scripts/check-ci-filters.sh)
recomputes those sets and fails naming each file a filter misses.

The filters are declared in three steps of this job, this one and the two below.
Actions hands each `filters` input to the action as one environment variable, and
Linux caps one of those at 131,072 bytes, which the whole block in one step passed:
the comments above the second and third steps say what that cost and how the sets
were chosen. A group filters of two steps name is written out in both, since a YAML
anchor reaches no further than the string it is in.

dorny/paths-filter v4.0.3's `some-with-excludes` quantifier matches a change to a filter
when one of its patterns without `!` matches and none of its `!` patterns does.
worker_image's drop what the .dockerignore keeps out of the
worker image's build context. envoy_e2e_local and ci_images each end in '!**/*.md': no
step of either job reads a Markdown file, so a guide written under a directory pattern
of theirs (patches/**, packages/broker-client/**, .github/actions/install-crane/**)
starts neither. No Markdown file of the repository matches either filter today, so the
pattern holds no run back now; it keeps the next guide from riding in, which is what a
week of Markdown-only pull requests cost 1,504 runner-minutes (dispatch://INFRA-1413).
Every other filter of this file was tried with that pattern
and refused by rule 1 of .github/scripts/check-ci-filters.sh, which fails a filter that
stops matching a file its gate reads: those jobs read an embedded prompt, a README
packed into a plugin or an image, or a guide a test compares with code - go_notices
among them, whose own Markdown pattern below therefore names packages/envoy/internal
alone, the tree whose Markdown no release target's package directory holds.

The '*_test.go' patterns of the filters below are written by a command rather than by
hand: rule 17 of .github/scripts/check-ci-filters.sh computes, per gate filter, the
`!` patterns that keep it off every Go test file the job it gates does not compile,
and `.github/scripts/check-ci-filters.sh write` puts them in that filter's own block
between a BEGIN and an END comment. The ordinary run of that check refuses a block
that is not what it computes, naming the first pattern that differs, so an edit by
hand is lost. The check prints how many it wrote on every run, so no count is kept
here. That is dispatch://INFRA-1413's R18, the selection half of the rule whose
coverage half is rule 1: a job must not run for a change to a test file of a package
whose tests it never runs, and the pattern that selects such a file (packages/envoy/**
and its kin) matches the job's own sources too, so nothing but an exclusion can say it.
Which test files a job reads is `go list`'s answer for the packages its steps name in
test mode, under the tags and the -race each run carries, so a test it never compiles -
another module's, another package's, a tagged live one, a `//go:build race` one - is
excluded, and a filter whose job compiles every test file its patterns reach carries no
block at all (text_checks, and every filter naming no Go file). A block
belongs to the filter whose list holds it and never to a group an anchor shares, since
two filters that share an anchor need not run the same tests. worker_image is the one
filter keeping such a pattern by hand, because this check computes no read set for it:
its judge is the image check below, and the worker image's context holds
packages/daemon's test files while the .dockerignore drops packages/envoy's. A filter
without a `!` pattern means what it would under the default, `some` (any pattern
matches), where a `!` pattern would match every file outside it.
.github/scripts/check-image-trigger-paths.sh reads the filters
that gate an image build (worker_image, envoy_smoke) under this quantifier, and only as
flat lists of globs, so those two name no anchor standing for a list. The one anchor
they do name, ci_router, is on the glob itself, which leaves their lists flat.
The first keys of this step are no job's gate: each is a group many filters share,
written once and named by every filter that needs it through its anchor, which
dorny/paths-filter flattens into the list that names it.

## Why the filters are one file, and the steps they stood in

The second of the three filter strings. Actions hands a step's input to the action as one
environment variable, and Linux caps one of those at MAX_ARG_STRLEN, 131,072 bytes: the
whole block in one step came to 137,498 bytes once the generated `!` patterns were
written into it, and the job could not start at all ("An error occurred trying to start
process ... Argument list too long", dispatch://INFRA-1413). So the filters are declared
in more than one step, each string its own YAML document; the third step's comment says
why there are three. Which filter is in which step is
grouping, not meaning: the split keeps every filter that shares a group with another in
the step that holds the group, so only the seven cheapest groups are written twice, and
rule 18 of .github/scripts/check-ci-filters.sh refuses a string that comes near the cap
again. dispatch_browser and envoy_infra_go moved here from the first step when main's
own test files grew that string past the margin: they share the Dispatch server with
three filters that stayed, which is the seventh group, and the first step's remaining
filters are the ones whose generated blocks grow with every test file packages/daemon
gains. contracts went the other way in the same pass, to leave the two strings the same
distance from the margin: it carries no generated block, so the bytes it moves are bytes
that stay put. Each output above names the step its filter is in, which rule 19 of that
check holds it to.

The third of the filter strings, for the reason the second's comment gives: the second
came within rule 18's margin again once envoy_go_race carried a generated block of its
own, when search_text_test.go's read narrowed from packages/envoy/** to the Markdown it
opens, and the first had no room for a filter either (dispatch://INFRA-1413). The three
race lane filters are here, and so is envoy_client, which the first step had no room for
once main's new test files grew its generated blocks, and so is private_names_port, whose
generated block names every migration test of the daemon's store and grows with each one
main adds. They share no group with a filter of the other steps but ci_router,
go_workspace, bun_install and legion_command, which this string writes again.

filter_verdicts above is one object over the three steps, the shape .github/scripts/ci-ok.jq
reads. A GitHub expression can build no object from three, so jq merges them here. A
name more than one step holds is a group written out again, which
.github/scripts/check-ci-filters.sh holds to the same patterns in each, so its verdict
is the same in each; rule 19 of that check refuses a filter an output of this job names
in two steps.

Each output names the step its filter is declared in. There are three of them because
Actions hands a step's input to the action as one environment variable and Linux caps
one of those at 131,072 bytes (the comments above the second and third steps say what
that cost here); rule 19 of .github/scripts/check-ci-filters.sh refuses an output naming
a step that does not declare its filter, so an output and its step cannot drift.

ci-ok-report.sh both already have it. The verdicts of the three filter steps are in it,
as one object: the merge is the `verdicts` step below, since a GitHub expression can
build no object out of three. On a dispatched run that step is skipped with the three
it reads and this is `{}`, which is right - the label holds nothing back there, and the
outputs above are all 'true'.

The groups the filters below share. Each is the first step's list pattern for
pattern, written again because a YAML anchor reaches no further than the string it
is in and each step's is a string of its own; the comment above each one there says
what the group is. Rule 18 of .github/scripts/check-ci-filters.sh refuses a pair
that drifts apart. The group dispatch-memory, dispatch-multiprocess and
dispatch-two-builds share moved here whole instead, since no filter of the first
step names it.

The Dispatch server, this step's copy of the first step's group: dispatch_browser
builds it for the browser suite and envoy_infra_go for the permissions live test,
and both filters are in this step, while daemon_go_tests, plugin_pack and docs name
the group in the first one.

The groups the filters below share: the first step's list pattern for pattern,
written again because a YAML anchor reaches no further than the string it is in.

## ci_router: ci.yaml, which every gate filter below names through

ci.yaml, which every gate filter below names through this anchor: every job's `if:`
is in this file, so every job reads it - which is why ci_job_reads.py declares
CI_SELF on every gate. Each gate filter also names the ci-job-<job id>.yaml file
holding its own job's steps, so a change to one of those files selects that job
alone. This entry stays beside them because a change to ci.yaml does reach every
job: dropping it would make each job's `if:` test a second output of changes, which
.github/scripts/ci_filter_coverage.py refuses (a job's `if:` tests exactly one), and
an output written as a disjunction instead would select exactly what this pattern
selects. The anchor is on the glob rather than on the list, since envoy_smoke and
worker_image name it too and the image check above reads those two as flat lists.

## daemon_envoy_deps: The airlock's rules, its decision and its header swap

The airlock's rules, its decision and its header swap. Not the whole tree: lean/
holds the model, which airlock-lean alone reads, and testdata/ the fuzz seeds and
the mutant patches, which the airlock package's own tests read - the daemon
compiles that package without running them.

## daemon_envoy_deps: The daemon reads the Envoy session timings (heartbeat

The daemon reads the Envoy session timings (heartbeat, live window, claim
staleness and the listener call's attempts) through this one public package: it
is a module of its own, so Go's internal rule bars it from internal/contracts,
where the generated values are.

## legion_command: The broker's OAuth wire types: packages/envoy/internal/brokerclient

The broker's OAuth wire types: packages/envoy/internal/brokerclient below
imports them, so `go list -deps ./cmd/legion` lists this package and the anchor
was one package short of its own definition until this line. Every other filter
naming this anchor already named the package through a second pattern of its
own (docs' own broker list, listener_command's, which go_notices takes), so the
line adds the package to no other filter's meaning.

## legion_command: The one public door onto the generated Envoy session

The one public door onto the generated Envoy session timings the daemon's
liveness and notice rules read: it sits in the envoy module because the
daemon, a module of its own, cannot reach internal/contracts, where they are.

## daemon_go_tests: The daemon-go job: what its two Go steps read together

The daemon-go job: what its two Go steps read together — the plain step, which
tests every package of packages/daemon but the three concurrent ones, and the race
step, which tests those three — so the filter covers the whole module's closure,
and what the Stage 1 proof beside them builds and sources. The
packages they compile, the Dispatch server internal/testbuild/testbuild.go:112 builds
for internal/daemon/outbox_scratch_dispatch_test.go:354, and what their tests read
outside the daemon: contracts' fixtures
(internal/api/state_golden_test.go:868, internal/intake/intake_test.go:802), the
agents internal/prompts/roles_test.go:261 stats and the tools skill its :689 holds
roles/mechanics/headless.md to, the GitHub App helper
internal/api/real_github_test.go:115 sources, and the examples and
docs its tests hold to the code (cmd/legion/config_example_test.go:164,
internal/config/controller_test.go:214, internal/config/kubernetes_test.go:1474).
No bun_install and no skills/** - the one page of skills/ this filter names is all
this job reads of it: the tests of this suite that packed the two plugins - and so
resolved the root manifest, the lockfile and the patches - or staged each plugin's
partition of skills/ with the prepack script are the plugin-pack job's now, and
this job installs nothing (dispatch://INFRA-1413, A4).

The list is the one the test job's filter used to name through this anchor while
it gated nothing of its own; it is now the daemon-go job's own filter, so a change
a Go test cannot see no longer runs 1,083 s of them (dispatch://INFRA-1413).

## daemon_go_tests: pi-legion's agents: internal/prompts/roles_test.go:261 stats agents/<name>.md.

pi-legion's agents: internal/prompts/roles_test.go:261 stats agents/<name>.md.
With its manifest, which the Stage 1 proof below reads, they are the files of
pi-legion this suite reads. The pack and prompt probes, which read the rest of
it, are the plugin-pack job's.

## plugin_pack: The plugin-pack job: internal/daemon's three tests of

The plugin-pack job: internal/daemon's three tests of PLUGIN_PACK_TESTS
(.github/scripts/ci_job_reads.py) — the one that packs packages/pi-envoy and
packages/pi-legion with `bun pm pack` and loads the two tarballs, and the two prompt
probes of agentmodels_omp_test.go, one of which lays out the prompts this checkout
ships as the release lays them out. The step runs those tests by name and compiles
the package they are in to do it, so this list is that package's test closure -
packages/daemon, the Dispatch server internal/testbuild builds for
outbox_scratch_dispatch_test.go, and the envoy packages internal/daemon imports -
and what the three read: the two plugin packages, everything their prepack builds
each bundle from, the skills it stages, and the frozen install that prepack builds
against, the lockfile included. Those reads are why the job exists: while these
tests were daemon-go's, bun.lock and skills/ were on that filter and a change to
either ran the daemon's whole Go suite (dispatch://INFRA-1413).
No Stage 1 script, no uv and no NATS images: those are daemon-go's steps, not this
one's.

## claude_envoy_build: What packages/claude-envoy/scripts/build.ts and scripts/verify-build.sh

What packages/claude-envoy/scripts/build.ts and scripts/verify-build.sh read, which
is every file the published plugin is built from: the claude-envoy job runs them,
and so do two steps of the test job - its verify-build step, which runs the same
command (packages/claude-envoy/scripts/verify-build.sh), as release.yaml's
claude_envoy_build does, and its build-input-list step, which runs
packages/claude-envoy/tests/build-inputs.test.ts. What only the whole claude-envoy
job reads is in the claude_envoy filter below rather than here: the test job runs
that job's lint and typecheck in no step, and no other test of the package.

## claude_envoy_build: packages/claude-envoy/tests/build-inputs.test.ts:146-153

packages/claude-envoy/tests/build-inputs.test.ts:146-153 holds release.yaml's
$CLAUDE_ENVOY_VERSION_ARGS equal to scripts/build-inputs.txt, so a change to either
must run both jobs.

## claude_envoy_build: Its src/broker-credential.ts:21-22 imports @legion/broker-client

Its src/broker-credential.ts:21-22 imports @legion/broker-client, whose exports
map every entry to ./src/<module>.ts, so a build compiles that package's seven
sources with envoy-client's. check-ci-filters.test.sh holds a hand-written list of
such pairs to this pattern, and ci_job_reads.py's envoy_client_dep is the one
place that declares the pair, which check-ci-filters.sh then holds to the filter.

## claude_envoy: tests/envoy-channel-server.test.ts holds the marketplace

tests/envoy-channel-server.test.ts holds the marketplace entry to this package's
own name, to no `version` of its own, and to a source range that leaves out the
0.0.0 placeholder and admits the first release. No step of the test job runs that
test, so the entry is this job's alone.

## contracts: The Go copies its tests hold the TypeScript to: src/dispatch-href.test.ts:49-53

The Go copies its tests hold the TypeScript to: src/dispatch-href.test.ts:49-53 and
src/dispatch-text-references.test.ts:10-14 read the Go reader's JSON tables,
src/dispatch-block-ref.test.ts reads pmdoc's block pointer and typed block id
tables, and scripts/gen-go.test.ts checks every file scripts/gen-go.ts writes - the
Go contract, pmdoc's generated block schema and contracts' own generated TypeScript
- is current. Not pmdoc/schema, the block schema those generated types are the
shape of: this job generates that Go from packages/contracts/schemas, and reads no
file of pmdoc/schema itself.

## contracts: `bun run check:api` renders schemas/dispatch-openapi.json into

`bun run check:api` renders schemas/dispatch-openapi.json into
src/dispatch-api.generated.ts and compares, and reads no Go at all
(scripts/gen-api.ts:6), so the Go that document is emitted from is not named here:
a change to it is judged by envoy-go-checks' `openapi --check`, whose filter
(envoy_go_checks) selects both that Go and this document, so either one alone
starts the job.
The five guides scripts/semantic-line-breaks.guides.test.ts reads, the checker it
spawns and the test itself: the guide-layout step's filter below is them, and the
test job's filter names them too, since a job's `if:` tests one changes output and
that step runs in no job the test filter did not select. No other step of the job
reads one of them, so the step's own gate is what keeps it off every other change.

## release_push_test: One filter per `*.test.sh` step of the test job: that

One filter per `*.test.sh` step of the test job: that harness and the script it
tests, so the step runs when either changes and skips when neither does. Each is
anchored and named again by the `test` filter below, which is what selects the job
the steps run in: a job's `if:` may test one changes output only, so the job's own
filter has to match whatever any of its steps' gates match, and the step's gate
then decides which of them run. Every one of them names this file and
ci-job-test.yaml, as every gate does, and the harnesses under .github/scripts name
test-lib.sh, which each of them sources for `check`, `is` and `summary`.
Before these the job's filter carried one pattern, '.github/scripts/*', for all of
them: a change to any one script ran every harness, and 28 of the 66 commits that
touched that directory since 2026-10-01 changed no Go file (dispatch://INFRA-1413).

## test: The test job: the Claude Code plugin build, the changelog

The test job: the Claude Code plugin build, the changelog fold, the script
harnesses above and the Bun suites of scripts/. The daemon's Go tests are the
daemon-go job's now, and daemon_go_tests, which this filter used to name through
its anchor, is that job's own filter.

It names every harness group above and the guide-layout step's files, because a
job's `if:` tests one changes output and nothing runs in a job that is not
selected: the groups decide which steps run, this filter decides whether the job
they are in runs at all.

## test: The workspace of the one go command left here - the listener

The workspace of the one go command left here - the listener build
scripts/e2e/lib/held-port.test.ts starts - and of the setup-go that installs the
Go for it (packages/daemon/go.mod). The `legion` build of the private-name port
harness, which put that binary's whole closure on this filter, is the
private-names-port job's now, with the filter below.

## test: Each harness step's own gate, named here so the job runs

Each harness step's own gate, named here so the job runs for the change that
makes one of those gates true. Each of those lists names this file and
ci-job-test.yaml, as every gate filter does, so the flattened list repeats the
two patterns already above; a repeated pattern selects what it selected.

## guide_layout: The guides' layout step of the test job (ci-job-test.yaml's

The guides' layout step of the test job (ci-job-test.yaml's
"Check the guides' semantic line breaks"), gated on exactly the files that step
reads, which the group above holds and the job's own filter names too.

## docs: docs/site's typecheck, screenshots and build, with the sources its reference

docs/site's typecheck, screenshots and build, with the sources its reference
generators and its screenshot harness read. docs-site.yaml holds those steps, and
both the docs job here and docs-pages.yaml's build job call it. docs-pages.yaml
builds and publishes main's site on the pushes this same list selects: its
on.push.paths, which check-ci-filters.sh holds equal to this filter, so the list
names all three workflow files.

## go_notices: lint's notices step: the Go packages it loads for every

lint's notices step: the Go packages it loads for every release target (`legion`,
the listener, the agent-secrets binaries and client, every envoy-infra command), the
workspaces they build in, the target lists, the script holding the loops and the
notices script it runs per target. Every Go and module file of the repository is in
it because scripts/go-third-party-notices.sh reads each target's `go list -deps`: a
new import changes the notices with no module file changed.

## go_notices: The step loads each release target's packages and writes

The step loads each release target's packages and writes their notices; it runs no
test, and no package directory it loads under packages/envoy/internal holds a
Markdown file. The 230 that ride in today are the pmdoc parser's corpus
(internal/dispatch/pmdoc/testdata), which only the jobs that run pmdoc's tests
read, so a guide or a corpus case written under packages/envoy/internal starts no
notices build (dispatch://INFRA-1413's A2).

## sessions_script: The session message-wait suite's own job: the suite

The session message-wait suite's own job: the suite, the script it spawns, and the
Bun that runs both. No install: the two import node: builtins and bun alone, so no
lockfile and no workspace manifest is an input of this job. The lint and the
typecheck of the same directory stay in the test job, which the `test` filter below
names scripts/sessions for.

## pi_legion: src/skills-guard.test.ts:24,275 spawns packages/claude-envoy/scripts/build.ts

src/skills-guard.test.ts:24,275 spawns packages/claude-envoy/scripts/build.ts and
asserts an exact file list over the tree it writes, so this job reads that whole
package and the one entry of its BUNDLE_ENTRYPOINTS (scripts/build.ts:41-46) that
lives outside it. A spawn carries no import, so no derivation of this repository
sees the reach: both patterns are hand-kept, with their declared reads in
ci_job_reads.py.

## pi_legion: The Dispatch e2e client scripts/skill-scenarios/seed.ts:96

The Dispatch e2e client scripts/skill-scenarios/seed.ts:96 imports, which the
typecheck compiles: that program holds six modules of this directory (api.ts,
harness-ports.ts, harness-broker.ts, harness-processes.ts, harness-real-broker.ts
and harness-signin.ts), so the directory is named rather than the two it reached
when this entry was written.

## dispatch_ui: The dashboard's and the proof editor's fast checks (dispatch-ui)

The dashboard's and the proof editor's fast checks (dispatch-ui): what their lint,
typecheck, unit tests and the dashboard's build and asset checks read. No Go and no
Dispatch server: the job runs neither.

## dispatch_ui: The one markdown fixture table, read at web/src/__tests__/pmdoc-fixtures.ts:48

The one markdown fixture table, read at web/src/__tests__/pmdoc-fixtures.ts:48,
whose path is the new URL at :44, by the three suites this job runs over it:
features/refs/fixture-table-readings.test.ts,
markdown-engine.test.ts and markdown-surfaces.test.tsx. The dispatch-browser job
declares it too, for the browser rows that import the same module, but that job
runs no unit test.

## dispatch_ui: The dashboard's tsc program compiles @legion/envoy-client's

The dashboard's tsc program compiles @legion/envoy-client's sources, and its e2e
helpers' test/fake-dispatch with them, through the workspace's "bun" export
condition, so check-ts-filters.sh holds this filter to both. The two suites that
import it, e2e/__tests__/delivery-live-probe.test.ts:6-9 and
local-instance-support.test.ts:2-5, run in this job's test step and in no other,
so that read is this gate's (ci_job_reads.py's dispatch_ui).

## dispatch_browser: The browser suite (dispatch-browser), which .github/workflows/dispatch-browser.yaml

The browser suite (dispatch-browser), which .github/workflows/dispatch-browser.yaml
holds: the shards, the three `-alone` projects, the two-process job, the
fake-embedder job, the visual review and the merge job. Every entry is something a
browser row reads: the dashboard and its contracts, the Dispatch server its harness
builds, and the two workflow files that hold the steps. The retired `dispatch` job's
filter also held three fixture sets whose only readers are the dashboard's and the
proof editor's unit tests, and they are not here: dispatch-ui's filter names all
three, and with this output ungated on a pull request they would start four shard
machines and the merge job for a fixture-only change. The dispatch_browser gate in
.github/scripts/ci_job_reads.py says what this suite reads, and check-ci-filters.sh
holds this filter to that gate - it refuses a filter that misses a read, never one
that is too wide, so a path is in this list because a browser row reads it and for
no other reason.

## dispatch_browser: dispatch_ui's set, which the SPA and the e2e helpers load,

dispatch_ui's set, which the SPA and the e2e helpers load, with the bundle of the
dispatch command its rows spawn (e2e/dispatch-cli.ts:8,39), dispatch_two_builds_go's
set (ci_ts_programs.py, LISTED, says why). Its jobs' contracts build
(`build:dispatch`) compiles nothing outside the first: not legion-api.ts.

## dispatch_browser: The workflow that holds every browser step, which this job

The workflow that holds every browser step, which this job reads as its own
definition - the place every other job of this file gives to its own
ci-job-<id>.yaml, so a change to it selects this job alone. An unlabelled pull
request runs the pull-request lane, the two projects a change is held to; a pull
request carrying `full-ci`, the merge group and a dispatch run the full lane,
every project and the two-process job, which is the lane the nightly
(.github/workflows/dispatch-browser-nightly.yaml) calls. So a worker can put the
whole suite on a pull request with the label, and the group is where a change to
that file meets it before main anyway. The nightly itself is not in this list:
no step of this suite reads it, which rule 9's second direction refuses, since
four shard machines and the merge job would start over a change no row sees. It
is a read of the ci_filters gate instead, so a change to it re-runs this check,
which holds this filter and that gate together.

## dispatch_browser: The memory and CPU sampler the shard and alone jobs'

The memory and CPU sampler the shard and alone jobs' browser steps start and read
back, the reading of the lanes' PostgreSQL those two jobs run before the suite,
the script the merge job runs to take each leg's newest attempt, and the script
that makes the merge job's two reads of its own run, which reads this run's own
attempt's jobs and says a refused blob download was a refused read. Beside them,
the twelve bodies the shard, the alone, the visual and the merge jobs carried
inline until this file grew too large for GitHub to start it
(dispatch://INFRA-2507): the shard job's wait for the file store it starts, its
browser step and the record of what that run left behind, the alone job's
browser step and its own record step, whose text differs from the shard job's
because no file store runs on that machine, the visual job's run against the
baselines, and the merge job's merge of the legs' blob reports, its four
readings - the rows, the flake rate, the leg exits and the row durations - and
the verdict that fails the job on a red leg or a failed row. The Playwright
install both jobs ran stood here too, with the pinned apt list it installed, the
key their apt archive cache was read under and the check that the list had not
fallen behind Playwright: those two jobs run in the image this repository builds
now, which carries the browsers and their host packages, so all four files are
gone (dispatch://INFRA-1443).

## dispatch_browser: The container image the shard and alone jobs run in and

The container image the shard and alone jobs run in and everything that decides
which bytes it is: the row that pins its digest, the Dockerfile it is built from
and the workflow that builds it. A change to any of them changes what those jobs
run inside, so they run (dispatch://INFRA-1443). The row is named as the whole
file because a paths filter matches files and not rows, so any row change
re-runs the browser jobs; that is correct and cheap. The two-process and merge
jobs are on this same filter and run in no container, so an image change runs
them too, which costs a five-minute Chromium job and keeps one list rather than
two.

## dispatch_browser: The one markdown fixture table: the browser row each

The one markdown fixture table: the browser row each of the seven parity families
whose shapes the corpus holds keeps reads it at
web/src/__tests__/pmdoc-fixtures.ts:48, whose path is the new URL at :44. This
entry declares that read explicitly and selects nothing on its own: the
`*dispatch_server` anchor this filter takes above already matches it through its
own 'packages/envoy/internal/dispatch/pmdoc/**', as it does the block schema
e2e/engine-reading.ts:13 reads. Measured with ci_filter_coverage.changes_filters
and workflow_paths.PathsFilter under both quantifiers: this filter covers the
table with the entry and without it, where dispatch-ui's own entry for this same
path covers it only with, that filter naming no anchor that reaches the table.
Those three are named rather than cited by line: a citation into this file moves
with every comment added above it, and one entry's line number took four values in
two rounds that way.

## dispatch_browser: What stood here was the file store's own image declaration

What stood here was the file store's own image declaration and the
pull-test-images action: no job of this suite pulls an image or runs a docker
command now, since a job inside a container can do neither. The shard job starts
the `weed` binary its own image carries instead, so neither file reaches it.
The real secrets broker e2e/broker-approval.e2e.ts approves a request on, the dev
OIDC issuer it trusts, and the seeding step that logs its service in and enrols its
pods: the packages `go list -deps ./cmd/broker ./cmd/dev-oidc-issuer
./internal/broker/brokertest/requestseed` lists in packages/envoy beyond the
Dispatch server's, which e2e/run-broker.sh builds in one `go build`.
agentloop: the model loop cmd/broker's judge runs a model and its tools through.

## envoy_go: internal/dispatch/text/whitespace_fixture_test.go holds unicode.IsSpace to this

internal/dispatch/text/whitespace_fixture_test.go holds unicode.IsSpace to this
list, which packages/envoy-client holds goBlankPattern to: an edit of the list
alone must start both jobs, or the side it disagrees with stays green.

## envoy_go: The three jobs whose tag steps lean on this job's untagged

The three jobs whose tag steps lean on this job's untagged run of ./cmd/dispatch
and ./cmd/broker: each runs only what its tag adds, so a change that starts one
must start this job too (check-ci-filters.sh's rule 15).

## envoy_go_checks: envoy-go's fast half (envoy-go-checks): the format, generated-contract and pmdoc

envoy-go's fast half (envoy-go-checks): the format, generated-contract and pmdoc
checks, the seven shell-script tests, and the build and pack of the envoy commands,
with the tests of internal/dispatch/architecture/notes and cmd/architecture-check,
whose one run this is (envoy-go's unit step leaves them out), and the
architecture-note check over this repository's own notes through the composite
action .github/actions/architecture-check. gofmt -l . walks every .go file under
packages/envoy, those tests and the scripts they drive live there, and `go build
./cmd/dispatch ./cmd/listener` compiles packages of that module alone, as the
action's build of cmd/architecture-check does.

## text_checks: The text checks of the three Go modules, the Dispatch

The text checks of the three Go modules, the Dispatch API's eight static source
checks, and the two pure pins of packages/daemon's supervise table: the tests whose
subject is a file's words. Their job runs packages/envoy/internal/textcheck,
packages/envoy/infra/internal/textcheck and packages/daemon/internal/textcheck,
which the three Go lanes leave out of their own runs, those checks' fifteen tests
by name, and those two pins by name, which the daemon-go plain step skips.
packages/envoy stays a wide
root: file_copy_test.go walks every .go file under it (infra included), the API's
guards and one_expression_guard_test.go walk every non-test .go file of the
module, and the guard step compiles that package's whole test closure, which
reaches testdata and goldens across the module.
Of packages/daemon the job reads the .go files its own file_copy_test.go walks,
the .go files the supervise pins type-check from source - internal/supervise and
every package of this module it imports - the prompt below and the committed
copies its text checks hold to the texts they copy, so the module's other files -
its .sql, .json and .md - are no input of this job and no pattern here matches them.

## text_checks: The six copies of shipped texts that readExtract (waiting_line_texts_test.go:380)

The six copies of shipped texts that readExtract (waiting_line_texts_test.go:380)
reads to hold them to the texts themselves: the waiting line's four, the way
back's (way_back_doc_test.go:45) and the troubleshooting page's refusal table's
(troubleshooting_refusals_doc_test.go:36), each committed beside the package whose
own check reads it. They are .txt files, which the module's `**/*.go` above does
not match.

## dispatch_memory: `go test -tags memory ./cmd/dispatch`. memory_profile_test.go

`go test -tags memory ./cmd/dispatch`. memory_profile_test.go reads four guides only
when DISPATCH_EDIT_PROFILE_OUT is set (its line 126), which CI never sets, so they are
not here. cmd/dispatch/nats_unreachable_test.go, which carries no tag, starts the
in-process NATS (its line 47).

## dispatch_two_builds_go: The two-build job's Go step alone, behind a gate of

The two-build job's Go step alone, behind a gate of its own (dispatch_two_builds_go):
`go test -tags twobuild ./cmd/dispatch`, the same closure as dispatch_multiprocess,
the script its -run comes from, the script that builds the previous build it runs
against, the sizing of its Postgres, and the current build's CLI
twobuild_cli_test.go runs (`bun packages/envoy-client/bin/dispatch.ts`, with the
sources it loads, after a frozen Bun install). TAG_RUNS pairs the step with envoy_go,
whose filter rule 15 holds to all of this, the listed block's files among them, and
to nothing of the browser half below.

## dispatch_two_builds: The two-build job: its Go step's reads above, named again

The two-build job: its Go step's reads above, named again so the job runs for the
change that starts the step, and the browser half,
`bun run e2e:multiprocess -- cross-build.multiprocess` in packages/dispatch: the
harness and spec under e2e/, the two SPA builds (web/, the editor package and the
patches it takes, the notices script the build runs), the Dispatch server
e2e/run-server.sh builds (dispatch_command_tests holds its packages) and the script
that holds each server's socket (run-server.sh:37, e2e/harness-start.ts). Of
packages/contracts it reads the manifest whose build:dispatch its contracts build runs
(*bun_install holds it) and the modules of the listed block below.

## listener_server_change: The server-change soak (listener-server-change): `go

The server-change soak (listener-server-change): `go test -tags serverchange
./cmd/listener`. The listener's own packages, and what its test binary compiles
beyond them - the in-process NATS and Postgres helpers, the builder of the binary it
starts, and the Dispatch packages cmd/listener's tests drive. A dependency's own
testdata is no input of this job: the tests that read it are that package's, and
this job runs cmd/listener's alone.

## listener_server_change: What cmd/listener's test binary compiles that `go list

What cmd/listener's test binary compiles that `go list -deps ./cmd/listener` does
not, so listener_command, which is that build closure, leaves them to the jobs
that run the tests: the chat client, the redirect table and the router.

## airlock_lean: The Lean model of the airlock and the Go code it covers

The Lean model of the airlock and the Go code it covers (airlock-lean): `lake build`
and the axiom print read the model's own project, and the two fuzz runs compile the
airlock package and compare it against the model's program, so a change to either
side runs the job. Both sides live under internal/broker/airlock, which one glob
names; the axiom check is a script of its own, which check k15 runs too; the Lean
toolchain comes from the elan the job's action installs, and that action reads the
`elan` row of the tool manifest through the fetch helper every install goes through.

## envoy_smoke: Every file packages/envoy/docker/Dockerfile builds from

Every file packages/envoy/docker/Dockerfile builds from, so envoy-smoke runs whenever
the image it smokes changes, every file the held-port image built on top of it reads
(packages/envoy/infra/host/heldportwrapper/Dockerfile and its .dockerignore), and what
the job's tests compile: TestSmoke in the root workspace, and envoy-infra's host live
tests and plugin in the infra one.
.github/scripts/check-image-trigger-paths.sh fails the build when this list misses a
file the image build reads; it reads this filter only as a flat list, so the one
anchor it names is ci_router, which stands for a single glob and leaves the list
flat. check-ci-filters.sh holds it to the rest: the job's go commands and its other
steps' reads.

## envoy_smoke: The job's steps, which build the image and smoke it

The job's steps, which build the image and smoke it: check-image-trigger-paths.sh
counts the workflow file holding a build step among the image's inputs, so a
change to it must start the build.

## envoy_smoke: envoy-infra's host live tests and its plugin build, in

envoy-infra's host live tests and its plugin build, in packages/envoy/infra's own
workspace; the live tests compile internal/pulumitest, which holds the version
they pin the docker provider at, under infra/internal. The plugin build's step is
pinned whole in check-ci-filters.sh's table (an exact entry), so what it compiles
is here, not the infra module.

## envoy_e2e_local: And @legion/broker-client with it: the tsc program of

And @legion/broker-client with it: the tsc program of envoy-client holds all seven
of its modules, and at runtime, which is what this job's bun scripts do,
transport.ts's own closure holds two of them (delivery.ts's holds none), so the
pattern is needed through transport.ts (ci_job_reads.py's envoy_client_dep).

## envoy_e2e_local: The job reads no Markdown file: the listener, the renderer

The job reads no Markdown file: the listener, the renderer and the contracts
between them are its whole input, so a guide written under one of the directory
patterns above would start it for nothing.

## worker_image: Every file packages/daemon/docker/worker.Dockerfile builds

Every file packages/daemon/docker/worker.Dockerfile builds from, for the worker-image
job (.github/workflows/worker-image.yaml, which the build reads too): the root
manifest, lockfile, patches and every workspace manifest the plugin stage's frozen
install copies; the packages it copies whole (the daemon with the Dockerfile, the two
plugins and the workspace packages they inline, the Claude Code plugin, whose source
the plugin stage builds into the image's dist/ and legion/ with the .bun-version that
build is pinned to, the skills and the prepack that stages them); the Go workspace; packages/envoy, copied whole for its agent-secrets
binary but for packages/envoy/infra, which the .dockerignore drops, so each of its
top-level directories but infra is named; the scripts that write the image's
third-party notices; the Oh My Pi pin; and the context's .dockerignore. Then a `!`
pattern for each .dockerignore line that drops part of those packages, of
packages/envoy or of skills/, which the step's some-with-excludes reads as an
exclusion, so a pull request touching only files the build does not read builds no
image.
.github/scripts/check-image-trigger-paths.sh fails the build when this list misses a
file the image build reads, including a new top-level directory of packages/envoy, or
matches a file it does not read other than the two workflow files; it reads this
filter only as a flat list, so the one anchor it names is ci_router, which stands
for a single glob and leaves the list flat.

## worker_image: Changelog fragments, which the packages above match and

Changelog fragments, which the packages above match and no build reads: .dockerignore
leaves them out of the context, so a pull request that only writes a changelog entry
builds no worker image, and a fragment stays where it was written, so no pattern
here matches nothing.

## browser_image: Every file the browser image's build reads, for the browser-image

Every file the browser image's build reads, for the browser-image job
(.github/workflows/browser-image.yaml, which the build reads too): that build's
context is .github/docker, so every file under it reaches the build, and the
Dockerfile it builds is the only file there today.
.github/ci-images.yaml is deliberately not named. The build does read that file:
its `row` step reads the registry and the digest of the row built from that
Dockerfile out of it at run time, and its publish decision is that digest and the
label the image at it carries. But .github/scripts/check-image-trigger-paths.sh
reads what a build reads from its context and its Dockerfile alone, and it
refuses a path matched by a filter that decides nothing but an image build and
that no build reads by that reading: "browser_image matches
.github/ci-images.yaml, which no image build reads, so a pull request touching
only that builds an image for nothing; narrow the filter". worker_image above
leaves .github/scripts/repo-paths.sh out for that same reason, though its build
job runs that script. That refusal does not reach this filter today: the steps of
browser-image.yaml's build job are in no BUILD_PLUMBING_STEPS entry of that check
and its `attest` job is in none either, so the check reads the browser-image job
as doing more than build an image and leaves this filter's breadth unjudged
("not checked for files no image build reads"). The file stays out all the same:
the argument below is why, and a filter narrowed by hand needs no refusal to
keep it narrow.
Nothing is lost by leaving it out. A change to the row alone builds nothing, and
the digest a row names is one the package already holds: the ci-images job, whose
filter does name that file, is what reads whether it does
(.github/scripts/check-ci-images.sh package), and the push that writes the row
needs no build of its own: the image that row names was built and pushed by the
earlier push, the one that changed the Dockerfile under .github/docker, which
this filter does name. The `container:` jobs that run the image are in
.github/workflows/dispatch-browser.yaml, and this file declares no `container:`.
That check also fails the build when this list misses a file the image build reads;
it reads this filter only as a flat list, so the one anchor it names is ci_router,
which stands for a single glob and leaves the list flat.
.github/scripts/ci_images.py is named, and is the one file here the build does
not read as a file: the build's `labels:` is
legion.browser-image.source-sha256=<the row step's source output>, and that step
computes the output with that file's recipe_sha256, so the file's own bytes
decide a label of the image this build pushes. Without it a change to the rule
that defines the hash selected the ci-images job, whose rule 10 then refused the
published image for carrying a hash this tree no longer computes, while no build
ran and no row could move. check-image-trigger-paths.sh reads it as an input of
this build for that reason, from the COMPUTED_BUILD_INPUTS entry that writes the
argument down, and that entry is what refuses this filter if it stops naming it.

## ci_filters: The directory rather than the four workflows the check

The directory rather than the four workflows the check names one by one (this
file, docs-pages.yaml, docs-site.yaml and ci-caches.yaml): its rules read every
workflow of it - ci_workflow_rules.check_one_merge_group_workflow refuses any but
this one that runs on merge_group - and a rule that refuses a new workflow has to
run on the change that adds one, which a list of the names already there cannot
select.

## tool_fallback: ci.yaml's tool-fallback and tool-fallback-arm jobs, the

ci.yaml's tool-fallback and tool-fallback-arm jobs, the live proof of the second
source every tool install reads when the first source does not serve the bytes:
the manifest that names both sources of each row, the fetch helper every install
goes through, the filler and the gate beside it, the rig and the fault proxy it
drives, the eight actions it installs through, and the two files a row's version is
pinned in that the job itself reads (.bun-version for the bun rows and the
worker image's Dockerfile for mise, jj and uv).
packages/envoy/infra/internal/pulumitest/pulumitest.go, whose
`const DockerPluginVersion` the gate holds the pulumi-docker row's version to,
is not here and no step of either job reads it: a raise of that constant must
move the row, and .github/ci-tools.yaml is in this list.
One filter for the two jobs, as `envoy_go_race_docs` is for its two: the arm64
job drives the same action over the same manifest through the same helper, so a
change that selects one of them must select both, and one list cannot drift from
another that does not exist. Each job carries a gate of its own in
.github/scripts/ci_job_reads.py for what it reads.
Each job holds its steps in a called file of its own, which this list names beside
ci.yaml, where both jobs keep their gate.

## ci_images: ci.yaml's ci-images job: the image copy (.github/scripts/mirror-ci-images.sh

ci.yaml's ci-images job: the image copy (.github/scripts/mirror-ci-images.sh,
through the crane .github/actions/install-crane installs), the package mode of
.github/scripts/check-ci-images.sh, which reads ghcr.io, and the tool copy
(.github/scripts/mirror-ci-tools.sh), which fills the second source of every tool
install. These files are what decides what those three steps do, the built row's
whole build recipe among them: the package mode hashes the recipe - the browser
Dockerfile's own path, every file of .github/docker and
.github/workflows/browser-image.yaml, folded by the recipe_sha256 of
.github/scripts/ci_images.py - and holds the built row's published image to that
hash. The gate's other half reads the whole tree and is a step of lint, which
runs on every change.
It names what this job reads and nothing else, the tool_fallback filter above
included: rule 9's second direction refuses a pattern of a gate's filter that
matches no file its job reads, so this list cannot name that filter whole to be a
superset of it by construction. The tool-fallback job below therefore carries
`!cancelled()` rather than leaning on that: its `needs: ci-images` is for order,
since a run that selects both must fill the copy before the proof reads it, and
the one file that can bring a new row in, .github/ci-tools.yaml, is in both
filters, so every change that adds a row runs the fill first.

## ci_images: The job reads no Markdown file: its steps read the image

The job reads no Markdown file: its steps read the image rows, the two scripts
that copy and check them, the tool rows and the filler beside them, and the
crane the action installs, so a guide written under one of those directories
would start it for nothing.

## proof_sdk_offline: The proof-sdk-offline job: a frozen install behind a

The proof-sdk-offline job: a frozen install behind a proxy that answers 504
for every host the proof editor's old `github:sjawhar/proof-sdk#<sha>` route used.
What can send that install to one of those hosts again is every file the install
reads, which is the shared group above, so the filter names it whole rather than
the four sites of it this job is really about: bun.lock, which a reintroduced
remote dependency necessarily moves, package.json, the root manifest the frozen
install reads, packages/proof-editor/package.json, the manifest that names the
tarball, and packages/proof-editor/proof-sdk-*.tgz, the file itself. Beside it:
.github/scripts/ci-fault-proxy.py, the program that drives the install, and the
two files that hold the job: ci.yaml, which carries its `if:`, and the called
file its steps live in.

## envoy_client: The Envoy client's job (envoy-client). It moved from the second

The Envoy client's job (envoy-client). It moved from the second step to the first
when the listed blocks of dispatch_ui and dispatch_browser grew that string past
rule 18's margin, and from the first to this one when main's new test files grew the
first step's generated blocks past it in turn: it names only groups this step writes
out.

## envoy_client: src/__tests__/dispatch-execute.test.ts holds goBlankPattern to this list, which

src/__tests__/dispatch-execute.test.ts holds goBlankPattern to this list, which
the Go test of packages/envoy/internal/dispatch/text holds unicode.IsSpace to:
an edit of the list alone must start both jobs, or the side it disagrees with
stays green.

## envoy_client: src/__tests__/dispatch-who-waits.test.ts's `no text states the old rule` reads

src/__tests__/dispatch-who-waits.test.ts's `no text states the old rule` reads
every text file git lists under these roots (OLD_RULE_ROOTS) and every AGENTS.md,
the root's and the nested ones among them, so a text there that states the
replaced rule must start this job. The recordings under packages/dispatch/e2e
start it too, though the row leaves them out: this filter takes no `!` pattern.

## envoy_go_race: The shared race job: the closure of `go test -race`

The shared race job: the closure of `go test -race` over delivery, the Dispatch
store, the bus, the listener, the registry store, the session cache and kvwatch (`go
list -deps -test -race` in packages/envoy), and every .md file under packages/envoy,
which internal/dispatch/store's search_text_test.go:104-120 walks (the RULES row for
that file in .github/scripts/ci_job_reads.py declares packages/envoy/**/*.md). The
filter names the wide root rather than that package list and that glob, and its
generated block keeps it off the Go test files the job does not compile, and
listener_server_change's tagged soak test, which rule 15 holds this filter to, is
not among them. No bun_install anchor: no test of these packages runs bun.

## envoy_go_race: listener-server-change takes its -run from this script

listener-server-change takes its -run from this script, and this is the job that
runs cmd/listener's untagged tests, so it must run whenever the script changes -
and whenever that job's own definition does, for the same reason
(check-ci-filters.sh's rule 15).

## envoy_go_race_api: The API race job. Not packages/envoy/**, as envoy_go_race has: that

The API race job. Not packages/envoy/**, as envoy_go_race has: that root was here
because internal/dispatch/api's lock_level_test.go, event_insert_guard_test.go and
review_write_guard_test.go walk every non-test .go file of the module, and those
seven source guards now carry `//go:build !race`, so the race build compiles none of
them and the text-check job runs their tests instead. one_expression_guard_test.go
walks the module the same way and is on that side of the tag for the same reason,
so no module-wide walk is left in this job's build (dispatch://INFRA-1413). What is
left is the closure of `go test -race ./internal/dispatch/api/` (`go list -deps
-test -race` in packages/envoy), one pattern per directory, with what those
packages embed and the testdata of the package whose tests this job runs: a
dependency's testdata is read by that package's own tests, which run in a job of
their own.

## envoy_go_race_api: The API's CI-reconcile passes route reads cireconcile's

The API's CI-reconcile passes route reads cireconcile's own record, so the race
build of ./internal/dispatch/api compiles that package too. Its testdata is not
here, for the reason this filter's header gives: a dependency's testdata is read
by that package's own tests, which run in a job of their own.

## envoy_go_race_docs: The document service's own race job. Not packages/envoy/**, as envoy_go_race

The document service's own race job. Not packages/envoy/**, as envoy_go_race has:
a race job of twenty minutes or more must not run on every change under that tree
(INFRA-490). This is the closure of ./internal/dispatch/docs/ (`go list -deps -test
-race`), one pattern per directory, with what those packages embed and the testdata
its own tests read - this package's, and pmdoc's, which one of them names through
`..`. A dependency's testdata no test of this job reads is not an input.

## envoy_go_race_docs: Two jobs run this package under the detector and share

Two jobs run this package under the detector and share this filter, so it names
both of their files (ci_job_reads.py gates envoy-go-race-docs and
envoy-go-docs-finalizers on it).

## private_names_port: The private-name port harness, in a job of its own: it

The private-name port harness, in a job of its own: it builds the `legion` binary
of this head (.github/scripts/check-private-names-port.test.sh:70) and holds the
rules that binary carries to the frozen shell script's verdicts, case for case.
That build is why this list is as long as it is - legion_command is what the
binary is made of, the rule file packages/daemon/internal/leakguard embeds among
it - and why the step has a job: while it was the test job's, the whole closure
sat on the `test` filter, so a change to one package of the daemon ran thirty-odd
shell harnesses and the Bun suites of scripts/ with it (dispatch://INFRA-1413).

## private_names_port: test-lib.sh, which the harness sources for check/is/summary

test-lib.sh, which the harness sources for check/is/summary (:63), and the
frozen script it runs every case through beside the binary, symlinked into each
fixture (:203,411,457,546). That script is the subject of
check_private_names_test above, which gates another job's step: this job reads
it as the engine it compares against.
