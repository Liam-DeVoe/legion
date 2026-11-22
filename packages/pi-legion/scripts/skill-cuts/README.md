# skill-cuts

The runner that tests whether a cut of a skill's text keeps what agents do with it. Run every command
from `packages/pi-legion` as `bun scripts/skill-cuts/cli.ts <command> ...`.

A replayed request is sent as a real session sent it, and nothing of the run that made it may
change what the agent does. Two such things did, and the runner now removes both:

- **The request id.** A body whose `metadata.user_id` (or, on the OpenAI route, `prompt_cache_key`)
  stays the same from run to run moves the pass rate with the batch it is sent in. One body, sent
  interleaved to one gateway deployment, passed 8 of 30 runs with one fixed id, 26 of 30 with
  another, and 18 of 30 with a fresh id for every send. So the runner draws a fresh id for every
  run (the same id in each round of one run) and for every judge call, sends the runs of all the
  arms of a round in one seeded order, never one arm's batch after another's, and checks each
  skill with a false-loss control (`behaviour control`).
- **The harness strings.** A captured body named the capture's own scratch directory, which named
  the skill under test and the cut, in Oh My Pi's date and working-directory reminder and in the
  context files' paths. On one case, with that path the agent read the skill's reference in 12 and
  33 of 40 runs; with a path that named no skill, in 0 and 1 of 40. So every string of a captured
  body that comes from the capture and not from the session is replaced by the session's recorded
  value, or by a neutral one where the store holds none ("The harness strings", below).

Every private value (the owners, the session stores, the gateway's address, the paths left out, the
repositories copies map to, the dev box's Claude Code config directory, the skill sets and the
repository that pins them) comes from the `--config <skill-cuts.json>` file at run time
(`config.ts` says each key). No file here names one. The
gateway key comes from `--key-file`, read inside the process that uses it, never printed, logged
or written. The behaviour test and the load test read the skill texts from directories and the
cases or tasks from a file outside the repository. The repositories a context needs come from
`--repos <dir>`, a tree of `<owner>/<name>/<commit>/` directories, each the repository at that
commit; without `--repos` they are read from GitHub through `gh` (GET calls and GraphQL
queries; a whole repository's tarball only for a plugin's checkout). A skill's directory is read
from its commit's tree and its blobs; where GitHub cuts that tree short (a repository of too many
entries), the directory alone is read, along its path from the root tree, one level at a time
where GitHub cuts its own tree short too, never from the whole repository's tarball. So
`behaviour` and `load` need no GitHub token only with `--repos`. `copies`, `set-count`,
`craft-lines`, `build-cases` and `build-tasks --profiles-out` read GitHub through `gh`, and only
read it: GET calls, and GraphQL queries that read file contents in batches.

## Commands

### `behaviour screen|confirm|control|report`

```
bun scripts/skill-cuts/cli.ts behaviour <screen|confirm|control|report> \
  --config <skill-cuts.json> --key-file <file> --out <dir> \
  --old <skill dir at the cut's base> --new <skill dir in the cut> --cases <case file> \
  [--planted-line <text>] [--budget-usd <n>] [--concurrency <n, default 8>] \
  [--after-case <command>] [--request-log <file>] \
  [--models <models.yml>] [--omp <binary>] [--repos <dir>] [--hint-model <model>] \
  [--omp-config <file>]... [--cache-dir <dir>] [--scratch <dir>] \
  [--reading-models <file>]
```

One skill per run: `--old` and `--new` are that skill's own directories, and `--cases` holds that
skill's cases (`cases.ts`), one JSON object per line.

- `screen`: per case, the three judge controls (the real step must pass; the case's
  `violatingStep`, a step that breaks the line, must fail; "I will continue." must fail; a case
  that fails any is dropped, naming the control), then a capture of each arm (old, new, none), 10
  replays of each, each reply judged blind with all arms shuffled, and runs made again for the runs
  with no verdict, at most 5 more per arm. It stops at 6 informative or 15 screened cases. A case
  whose model the gateway does not serve (none of the configuration's `servedModels` has its id,
  the text after its last `/`) is dropped before any call as `model-not-served`; a case on a
  served model runs as that served `provider/model`, so an Anthropic model behind another
  gateway prefix runs as `anthropic/<id>`. With `--reading-models` (the skill's
  `<cut id>/models/<skill>.json` that `attribute` writes: its reading sessions by model), a skill
  with fewer than 6 reading sessions on models the gateway serves is refused before any call
  (exit 2): it gets no verdict, with the reason `too few sessions on served models` and its served
  and total counts, and the walk of the cut's ranking goes on to the next skill.
- `confirm`: 40 fresh runs of old and new for each flagged case.
- `control`: on the skill's control case, 40 runs each of three arms: old; `minus` (old without
  each line of the case's file that is the case's line, trimmed, or without each line that is
  `--planted-line`; a rule written twice is still the rule, so every copy goes; a link in the old
  directory is copied as the file it names); and `twin` (old again, with a capture and runs of its
  own). Two controls are scored on these runs by the confirm rule (a loss when the other arm
  passes at least 10 fewer of 40 runs than old). The planted-loss control, old against `minus`,
  must be a loss, or the test could not see a loss. The false-loss control, old against `twin`,
  must be no loss, or the test calls a loss where the texts are the same. A skill whose planted-loss
  control is no loss, or whose false-loss control is a loss, gets no verdict, and the report says
  which control failed.
- In every stage, a case one of whose captures needs a directory no read can give (the commit
  holds no such directory, or GitHub cuts a level of its tree short even read alone) is dropped
  as `unreadable-directory`, its drop naming the repository, the commit and the path; a run
  started again skips it, and a dropped case is never informative. A read that fails for another
  reason, after its retries, fails the command (exit 1), and the next run reads it again.
- `report`: each case's passes per arm and stage, the control case's passes of old, `minus` and
  `twin` and what each control says, the skill's verdict and its reason (`keeps` prints as "no
  loss detected by the registered rule"), the drops per judge control and the drops counted as
  `model-not-served` and as `unreadable-directory`, an estimate of the procedure's false-loss
  chance at the cases run (exact binomial under identical old and new texts, the pass rates taken
  from each case's own screen; its assumptions are printed with it),
  a table of the chance it finds a loss of 10 to 40 points in one informative case, the spend
  at list price, the peak memory, and how many captures' system-prompt digests differ from their
  session's. It calls no model, and needs only `--config`, `--out`, `--old` and `--cases`. With
  `--reading-models` it also prints each model of that file with its sessions and whether the
  gateway serves it, the sessions whose file names no model and those in no models query's rows,
  and the served models the screened cases ran on, each with its cases; a verdict then ends with
  the reading sessions it speaks for, on each served model its screened cases ran on (a model
  counted under the served model of its id, so `gateway/claude-sonnet-5-5` under
  `anthropic/claude-sonnet-5-5`), and those it does not speak for, on models the gateway does not
  serve and on served models no screened case ran on. A skill too few of whose reading sessions
  are on served models gets no verdict. Without `--reading-models`, it says the reading sessions
  by model were not given, and the verdict names no models.

`score.ts` holds the rules. Each case's results are one file, `<out>/cases/<case id>.json`,
written whole after each round of runs, so a run started again goes on from what was saved and
skips each complete case. Each run in it keeps its arm, its verdict, the session id it went with
(`userId`) and its place in its round's send order (`sendPlace`), so both controls can be
recounted from it. A round sends every arm's runs in one order, shuffled by the round's recorded
seed (`replay.ts`'s `sendOrder`); the first run of each arm in that order goes alone among its
arm's, so its reply writes the prefix cache the arm's later runs read, and no place of the order
changes. A results file whose stages were run before this (they record no `sends`) is refused:
use a new `--out`. `--after-case <command>` runs `<command> <that file>` when a case's stage
ends. Captures are kept under `<out>/captures/`; a capture that is not there yet is made with Oh My
Pi and needs `--models`: `--omp`, else `$LEGION_OMP_PATH`, else the `omp` on PATH, taken as an
absolute path; a command given `--models` when none of them names one is refused (exit 2), and
`load` reads Oh My Pi the same way. The run stops above 1 GiB of peak resident memory, and no new
model call starts once the replies cost `--budget-usd` at list price.

The gateway serves two routes, and each case runs on its own model's: the Anthropic Messages
route (`/v1/messages`, `anthropic/<id>`) and the OpenAI Responses route (`/v1/responses`,
`openai/<id>`, today `openai/gpt-6.1-sol`). `--models` names both providers as the pod's
`models.yml` does (`openai` is Oh My Pi's built-in provider at the gateway's `/v1`, with its
models under `modelOverrides`); Oh My Pi 18.18.6 sends an OpenAI model's turn as a Responses body
(its system prompt as `instructions`, its history as `input` items; testdata F8), and the capture
keeps it as it does a Messages body. Hints are written by the hint model, which must be on the
Messages route: an OpenAI case needs `--hint-model anthropic/<id>`. `gateway.ts` sends each body
to its API's route (the key as `x-api-key`, or as `authorization: Bearer` on the Responses route),
reads the stream whole, and prices each reply at its model's list price (`PRICES`, from
Anthropic's and OpenAI's pricing pages; gpt-6.1-sol at $2 input, $10 output, $0.10 a cache read
and $2.50 a 30-minute cache write per million tokens, more above 272,000 input tokens), counting a
Responses reply's cached input and reasoning tokens too. A replay answers a reply whose every tool
call reads a skill file from the arm, in the body's API (`function_call_output` items on the
Responses route), at most three rounds. A Messages body gets a cache breakpoint on its last
message; a Responses body gets none, since that route caches the shared prefix by itself. The
judge stays `claude-opus-5-5` on the Messages route and reads a Responses reply's text and tool
calls as it reads a Messages reply's, so its request names no model, route or arm.

### The harness strings

Before a capture is saved, every string of its body that comes from the capture's run and not
from the session is replaced (`capture.ts`, "The harness strings"):

- Oh My Pi's reminder at the start of the first user turn (`Today: <date>; current working
  directory: '<dir>'`) becomes the reminder Oh My Pi journaled for that turn in the session, byte
  for byte. Where the session's head holds none, it is written with the session's working
  directory and the UTC date of the turn it stands for: a case's step, or a load task's first user
  turn.
- The working directory is the session header's `cwd`, else the one its journaled reminder names,
  else `/work`. A context file's `<file path="…">` under the capture's working directory names it,
  and every other mention of the capture's working directory does too. When the profile's
  `workingDirectory` is below the checkout's root, a parent level's context file, and every other
  mention of the capture's checkout, names the session's checkout: that working directory less the
  `workingDirectory`.
- A devbox context's user-level `CLAUDE.md` is shown at `<devboxClaudeConfigDir>/CLAUDE.md`, the
  configuration's value of the dev box's CLAUDE_CONFIG_DIR, else at `/work/.claude/CLAUDE.md`.
- A prompt Oh My Pi builds shows `- OS: linux` in its workstation lines, the same in every arm. A
  subagent's recorded prompt is sent as recorded, its workstation lines and `<file path>` blocks
  the session's own.
- The stand-in daemons answer with that working directory, made-up ids, and the session id each
  request to the Envoy listener names.
- A Claude Code capture (`load`) shows the session's working directory, its memory directory
  under the config directory above, the session's date in `Today's date is`, and `OS Version:
  Linux`.

A body that still holds the case's scratch directory after this is refused. The session's head is
read once from its store, through the configuration's `psqlCommand` (`session-head.sql`: the
file of the path hash, its first 256 Ki characters only), and saved as
`<out>/sessions/<store>-<path hash>.json` (a head an earlier run saved as `<path hash>.json` is
read back too); a read that fails stops that capture, with why. Each capture's record says each
value it shows and where the value came from (`harness`: the store, the configuration, or
neutral, and why), how many strings each rule replaced, and keeps the body as the host sent it
beside the saved one (`<stem>.raw.body.json`). A saved capture whose record holds no `harness`
was made before this rule and is refused.

### `read`

```
bun scripts/skill-cuts/cli.ts read --config <skill-cuts.json> --key-file <file> \
  --prompt <case|restate|audit|map|attack|same-job|sentence|names|tasks|craft|nearest|claim> \
  --in <input.json> --out <answer.json> [--request-log <file>] [--budget-usd <n>] \
  [--concurrency <n>]
```

Runs one reader (`readers.ts`, `prompts/<name>.md`) on one input, a JSON value of that reader's
input schema, and writes `{prompt, settings, answer, refusals, spent, usd}`.

### `control map|attack|same-job|sentence`

```
bun scripts/skill-cuts/cli.ts control <kind> --config <skill-cuts.json> --key-file <file> \
  --in <reader input.json> --out <result.json> [--request-log <file>] [--budget-usd <n>] \
  [--concurrency <n>] \
  map:      --delete-line <a line of the cut text that the base text holds too>
  attack:   [--plant-line <line>]          (default: "Run the tests where needed.")
  same-job: --planted <skill entry.json> --copy-of <id>
  sentence: --into <id> --sentence <text>
```

Plants a known finding in a copy of the reader's input, runs the reader once, and writes whether
its answer names that finding. It exits 0 when the control held, 1 when it did not. The craft
reader's control, `control craft`, takes its own flags (below).

### `count-sql`

```
bun scripts/skill-cuts/cli.ts count-sql --config <skill-cuts.json> --store <db> --day <YYYY-MM-DD> [--hour <0-23>] [--query count|models]
```

Prints `count.sql` filled for one store and one UTC day (or hour), the same as
`bun scripts/skill-cuts/countsql.ts`. `--query models` prints `models.sql` instead: each session
file's model, read from the first 256 Ki characters of the file, as one row per file hash.

### `load`

```
bun scripts/skill-cuts/cli.ts load --config <skill-cuts.json> --key-file <file> \
  --skill <skill dir> [--name <name>] --tasks <tasks.jsonl> --host <omp|claude> \
  [--profile <profile.json>] --out <dir> [--runs <n, default 3>] \
  [--model <model, default anthropic/claude-sonnet-5-5>] \
  [--thinking <level, default off on omp, auto (Claude Code's own) on claude>] \
  [--budget-usd <n>] [--concurrency <n>] [--request-log <file>] \
  [--repos <dir>] [--cache-dir <dir>] [--scratch <dir>] \
  [--models <models.yml>] [--omp <binary>] [--hint-model <model>] [--omp-config <file>]... \
  [--claude <binary>] [--context-session <store>:<path hash>]
```

The load test (`load.ts`): whether the host's agent loads the skill for each task,
given what the host lists for it. `--tasks` holds one task per line, `{id, text, label, source}`
with `label` `need` or `near-miss` and `source` `written`, or `session` with its `store` and
`pathHash`. A profile file is the other skills installed beside the skill (the `Profile` of
`cases.ts`), a `context` (a case's: `devbox`, the default, `legion:<project>:<role>` or
`subagent:<agent>`, whose profile carries the subagent's recorded system prompt and tools), and
for Claude Code a `claude` plugin layout: `{marketplace, plugins: [{name, skills}], skillPlugin}`.
The layout names each skill but the project skills. A project skill is a skill of the profile's
`sessionRepository` (the repository the session's working directory was a checkout of, at the
commit the profile's build read it at) whose path is `<level>/.claude/skills/<name>` or
`<level>/.omp/skills/<name>`, where `<level>` is the working directory's path in the checkout or a
parent of it up to the root. Claude Code lists a `.claude/skills` project skill under its bare
name, not as `<plugin>:<name>`, so `load` writes it at its path in the checkout and in no plugin;
it leaves a `.omp/skills` one out, since Claude Code does not read `.omp/skills`; a layout that
puts a project skill in a plugin is refused. A profile that names no session repository has no
project skill. The capture's record names where each skill went (`placements`).
Each task runs in the context of the session it came from: a task line may name its own `profile`
file (absolute, or relative to the tasks file's directory), and `--profile` is the profile of each
task that names none; a task with neither is refused. `--name` tests the skill under another
name: its frontmatter `name` and its directory's name change in a scratch copy, a subagent's
recorded prompt lists it under that name (its listing line's name changes, its text and every
other line stay), and nothing else that names the skill changes.

Per task, the host's first request is captured once against a recorder, in the context of the
task's own profile (Oh My Pi through `capture.ts`, which lists the skill under the hint Oh My Pi
has a model write, with the plugins' stand-ins, and sends a subagent's recorded prompt with only
the skill's listing line changed; Claude Code with a scratch `CLAUDE_CONFIG_DIR` per profile, into
which a local marketplace installs the profile's skills and the tested skill, each in its plugin,
while a project skill goes in the checkout), then sent `--runs` times. The plugins' stand-ins of
an Oh My Pi capture listen on 127.0.0.1. A Legion role's run gets a stand-in Legion daemon, a
stand-in Envoy listener and a sink for the NATS server the Envoy plugin dials. The run of every
other profile that installs the Envoy plugin, whatever its context (a devbox one included), gets
that listener and that sink, since the plugin defines its tools only when it has a listener to
reach; a subagent's run gets the listener alone. A capture follows the harness strings rule
above: a session task stands for its own session, and a written task for the one
`--context-session` names (the session its profile was rebuilt from; `<path hash>` is the
SHA-256 of that session file's path in hex), or for none, with neutral values, when it is not
given. The runs of a profile's tasks are sent together, once each of its tasks has its capture,
in one seeded order, each run with a fresh session id, the first run of each task in the order
alone among its task's. An `openai/<id>` model runs on Oh My Pi only, on the Responses route,
with an Anthropic `--hint-model`. A run loads the skill
when the first assistant message reads it (Oh My Pi: `read` of `skill://<name>` or its
`SKILL.md`, a `tool_use` block or a Responses `function_call` item; Claude Code: `Skill` naming
`<name>` or `<plugin>:<name>`, or `Read` of its `SKILL.md`), and never goes past that message. It
prints each task's loads and verdict, each profile's send seed, and the skill's verdict
(`score.ts`), and writes `<out>/runs/<task>.jsonl` (one line per run, with its session id, its
send seed and its place in the order), `<out>/captures/`, `<out>/sessions/` and
`<out>/summary.json`; each run line and each task of the summary names its profile file and the
SHA-256 of its bytes. A task whose run file is complete is skipped, so a run started again goes
on from what was saved; a task whose capture or runs were made under a profile of other bytes is
refused, and so is an `--out` whose runs were sent before each had its own session id. A task
whose context needs a directory no read can give (as `behaviour` drops a case for) is dropped:
its line and the summary's `dropped` give the reason (`unreadable-directory: ` and the
repository, the commit and the path), a run started again skips it while its profile file keeps
the same bytes, and the verdict rule counts it as a task with no runs.

### `copies`

```
bun scripts/skill-cuts/cli.ts copies --config <skill-cuts.json> --at <ISO instant> --out <dir> \
  [--only <owner/repo>]... [--max-search-calls <n>] [--cache-dir <dir>]
```

The list of copies and loaders (`copies.ts`): every repository of the configured owners at its
default branch's commit at `--at`, each `SKILL.md` outside the configured `excludePaths`, the
names in two or more repositories, the manifests and installers that install each skill, the
files outside a skill that name one (found by code search, paced under its limit), and the
copy-prefix table the read count is attributed by. Its files are listed in `copies.ts`'s header.
With `--cache-dir`, each owner's listing, each repository's read (its commit, the tree entries it
keeps and every blob it read) and each finished code search is saved there the moment it ends,
under a temporary name renamed into place, so a run stopped at any moment and started again with
the same arguments reads nothing twice; the hit files the searches find are read again. A truncated
tree is walked a level at a time in GraphQL batches of up to 50 subtrees, at most 4 reads in flight
for one repository and 8 in all. It prints one line per repository read (`copies: tree <n>/<total>
<owner/repo>: <entries> entries, <calls> tree reads, <seconds> s, cached|read`) and one per 25
searches, so the log gives the rate.

Three install paths give a skill to Oh My Pi from outside the owners' trees, and each is a unit of
the list, read as Oh My Pi 18.18.6 reads it:

- An Oh My Pi plugin package (a `package.json` whose `omp` or `pi` field is set; `pi` counts only
  when `omp` is absent): the skill directories its manifest's `skills` list declares, each inside
  the package, else its `skills/`, each scanned as Oh My Pi scans a skill directory (an entry
  holding no `SKILL.md` is read one level deeper, a skill with no description is left out, and a
  declared directory lists no skill through a link that leads out of the package). It is
  installed at `node_modules/<name>/`.
- A `github:<owner>/<name>#<ref>` dependency of an Oh My Pi plugins `package.json`: the package
  at the root of that repository, at that ref, installed under the dependency's name, whatever
  the repository's owner.
- An `ensure_vendor <url> <name> <ref>` clone of an installer script, which a `skills-sources.json`
  pool under `vendor/<name>/` feeds to Oh My Pi: the cloned repository at that ref, less each
  entry the installer's `find … -name … -exec rm` removes from the pool.

Each repository a pin names (the dependency's, the clone's) is read at the pin's ref, in place of
its default branch's commit when an owner's listing holds it too; `repositories.tsv`'s `branch`
names the ref. A repository with no pinned ref is read at its default branch. The prefix rows of
these units reach the table only when the configuration's `targets` name their repositories.
`build-cases` and `build-tasks` read a pinned repository at the ref its installer names at the
session's start.

### `attribute`

```
bun scripts/skill-cuts/cli.ts attribute --config <skill-cuts.json> --count <count dir> \
  --copies <copies out dir> --spans <spans.jsonl> --out <dir> [--changed <cut id>=<file>]... \
  [--models <models dir>]
```

The read count per skill copy (`attribute.ts`): it maps each read's copy path to a repository
copy by the prefix table, then by the repository-relative skill directory a checkout's path ends
with, then by the copies the store's context installs; counts each session once across the
count's queries; marks ties and loaded skills; states each store's span; ranks each cut's changed
skills for the behaviour test and writes each ranked skill's reading sessions
(`<out>/<cut id>/sessions/<skill>.jsonl`, file hashes only). With `--models`, it also counts each
copy's reading sessions by model, and writes each ranked skill's
`<out>/<cut id>/models/<skill>.json`: the counts by model of the sessions its sessions file holds
(`sessions`; `byModel`, each `{model, anthropic, served, sessions}`, most first; `noModel`, the
sessions whose file's head names no model; `notQueried`, those no models query held;
`notAnthropic`; `served`, those on a model the gateway serves; `withModel`), which `behaviour
--reading-models` reads. Without `--models` it writes no models file.

### `set-count`

```
bun scripts/skill-cuts/cli.ts set-count --config <skill-cuts.json> --rows <count dir> \
  --spans <spans.jsonl> --out <dir> [--dotfiles-commit <sha>] [--rows-label <text>] \
  [--not-read <name>=<reason>]...
```

The read count of each skill set's skills (`set-count.ts`, with the pins read by `sets.ts`). It
reads no store and calls no model. The configuration must give two keys that only this command
reads: `sets`, `[{id, repo}]`, each skill set measured against our skills (`id` the set's name,
which is also its Claude Code plugin name and the `<id>:` prefix of its skills in a Claude Code
`Skill` call, and may not be a cut's id or another set's; `repo` its upstream repository,
`owner/name`); and `dotfilesRepo`, the `owner/name` whose `omp/plugins/package.json` and
`installers/omp.sh` pin the sets (required when `sets` is given).

Inputs:

- `--rows`: the count's raw output, in `attribute --count`'s layout (`<store>/<day>.txt`, or
  `<store>/<day>T<hour>.txt` for each hour of a split day), read by `attribute.ts`'s own reader.
- `--spans`: each store's earliest record, in the form `attribute --spans` takes.
- `--dotfiles-commit`: the commit of `dotfilesRepo` whose pins are read, 40 hex digits; without
  it, the default branch's commit when the command runs.
- `--rows-label`: words that name the rows (an artifact and its version), written as given.
- `--not-read`: a source of sessions the rows cannot hold, with its reason (the Claude Code
  transcripts), once for each.

The pins. A set is pinned by a dependency `"<package>": "github:<owner>/<repo>#<ref>"` of
`omp/plugins/package.json` (its owner and repository the set's, in any case, whatever the package
key) or by a line `ensure_vendor <url> <dir> <commit>` of `installers/omp.sh` whose url is the
set's repository on GitHub. A set that no line pins, or that two lines pin, fails the command. A
40-hex ref is the commit; a tag resolves to its commit (a lightweight tag's ref names it, an
annotated tag's tag object names it); any other ref fails the command. The set's skills are the
`skills/<name>/SKILL.md` of its repository's tree at that commit, so the place its installer reads
skills from must be `skills/`: for a package, its `omp.skills`, else its `pi.skills`, else (none
declared, or no `package.json`) `skills/`, as Oh My Pi reads a plugin package; for a vendor
checkout, the one `skills-sources.json` pool `vendor/<dir>`. Any other place fails the command.
The set's upstreams are its repository's fork parent and source. The count also names each commit
of `dotfilesRepo` in the window at which a set's pin changed, from GitHub's list of the commits of
each manifest's path, each set against the commit's first parent, with the pin before and after.

The rule. A count row counts as the skill S of a set when S is one of the set's skills at its pin.
Its copy is the set's own copy when it starts with `<set>:` (a Claude Code `Skill` call), holds
the pin's install directory (`/node_modules/<package>/` or `/vendor/<dir>/`), or holds
`/plugins/cache/` and after it a path part that is the set's id (Claude Code's plugin cache). A
copy that is a URL is no install directory: a URL that names the set's repository or an upstream
of it is a read of the upstream by URL, any other URL another copy. A row with no path and no
prefix (empty, a `skill://` path, a bare name) names no copy; any other path or prefix is another
copy. A session is its store and file hash, and counts once for S, in the first of these columns
that one of its `read` rows reaches: `own`, `upstream_url`, `other_copy` (it read only another
copy, such as a Legion fork), `no_copy`. `reference_own` counts the sessions with a `reference`
row of S at the own copy, apart. Only a row's paired reads count, never its unpaired calls.

Before it reads GitHub it refuses (exit 2), naming each gap, rows in which a configured store that
has rows lacks a summary row for a day from the window's start to its end: no output of that day
or of all 24 of its hours, a file that is not one whole count.sql result, or a summary row whose
mtime range is not its query's. A configured store with no rows is named as not read.

Outputs under `--out`:

- `exemplar-count.md`: the window; the rows read (the label as given, and one sha256 over the
  files: of one line `<sha256>  <store>/<file>` per file, sorted by path); each store with its
  earliest record from the spans file; each store not read and each `--not-read` with its reason;
  the dotfiles commit and each pin with the commit it resolves to; the pin changes in the window;
  the rule; the sessions per skill in each column, and per skill and store; the skills with at
  least 20 own-copy sessions; the `count-sql` command line of each store and day the rows hold
  (with its psqlCommand, as it made the file), and the command's own line, with the
  `--dotfiles-commit` it read added when none was given, so it reads the same pins again.
- `exemplar-count.tsv`: one row per skill of each set at its pin (a skill with no read at 0), in
  the sets' order, most own-copy sessions first: `set`, `skill`, `own_sessions`,
  `upstream_url_sessions`, `other_copy_sessions`, `no_copy_sessions`, `reference_own_sessions`,
  then each again per configured store (`<column>:<store>`).
- `sessions/<set>/<skill>.jsonl`: each own-copy session, one line `{"store", "hash"}`, sorted by
  store then hash; an empty file for a skill with none.
- `summary.json`: the numbers of each skill in total and per store, the rows' files with their
  sha256, the stores, those not read, the pins, the pin changes, the skills at 20 or more
  (`atLeast20`) and the commands.

It exits 0 when the count is written, 1 when a GitHub read fails or a pin cannot be read as
above, and 2 when it refuses its arguments, a configuration without sets, a `--not-read` that is
not `<name>=<reason>`, rows or a spans file it cannot read, an `--out` inside `--rows`, or rows
with a gap.

### The craft list

A JSON file written by hand (`craftlist.ts`): the properties of a skill that the craft guides of
both skill sets state. Its shape:

```json
{"schemaVersion": 1,
 "sets": {"<set>": {"repo": "<owner/name>", "commit": "<40 hex>", "guide": ["<path>", "..."]}},
 "properties": [{"id": "C1", "property": "<one line>", "checkedBy": "reader",
   "readerLooksFor": "<text>",
   "lines": [{"set": "<set>", "path": "<path>", "line": 95, "lineEnd": 98}, "..."],
   "example": {"set": "<set>", "path": "<path>", "line": 3}}]}
```

- `sets`: each set by its name, with its repository, the commit its guide is read at, and the
  guide's paths.
- `id`: `C1`, `C2`, … once each.
- `lines`: the line (or the lines `line` to `lineEnd`) of each set's guide that states the
  property, at least one of each set of `sets`. A property that one set's guide states and the
  other's does not is no property of the list.
- `checkedBy`: `reader` (the craft reader checks it on a skill's text) or `behaviour` (only a
  behaviour test can check it).
- `example`: for a reader property, one line of a skill of a set that shows it. `record`: for a
  behaviour property, one `https://` link to a record of the sets' testing.

Every command that reads the list refuses one that breaks a rule above.

### `craft-lines`

```
bun scripts/skill-cuts/cli.ts craft-lines --list <craft-list.json>
```

Prints each set with its repository and commit, then for each property each cited line (or
range) and its example line, with the text of those lines in the set's repository at its commit,
and its record link. It reads each file once from GitHub through `gh` (`sets.ts`, `SetReads.text`)
and calls no model. It exits 1 naming each cited line or example past the end of its file and
each path the commit does not hold, 2 when it refuses its arguments or the list, and 0 otherwise.

### The style text's rule ids

The craft reader judges each rule of a style text (`--style`), by its id (`style.ts`): `S1`, `S2`,
… in the text's order:

1. each body row of the text's first table, the parts of a SKILL.md in order (not its header row
   or its separator row);
2. each top-level list item (`-`, `*`, `+` or `1.`) under a line or a heading named Rules
   (`**Rules:**`, `## Rules`);
3. each top-level list item under `**Check before you commit:**`.

A section runs to the next heading, or to the next line that is only a bold label that ends in a
colon. An item's indented lines (its `- Example:` and `- Source:` sub-items) belong to its text and
get no id; a paragraph of a section gets none. A style text with no rule is refused. So a style
text of 5 table rows and 10 numbered rules gets 15 ids, and one of 7 table rows, 10 items under
Rules and 3 under Check before you commit gets 20, its check item "No paragraph holds more than
one rule" `S19`.

### `craft`

```
bun scripts/skill-cuts/cli.ts craft --config <skill-cuts.json> --key-file <file> \
  --skill <skill dir> [--skill-source <text>] \
  --style <style text> --style-source <text> --craft <craft-list.json> --craft-source <text> \
  [--nearest <skill dir of theirs>]... --out <entry.json> \
  [--request-log <file>] [--budget-usd <n>] [--concurrency <n>]
```

One fresh request of the `craft` reader (`readers.ts`, `prompts/craft.md`), asked again while its
answer is refused, 3 tries in all. It reads every `.md` file of `--skill` (SKILL.md first), the
style text and its rules with their ids, the craft list's properties whose `checkedBy` is `reader`
(never a behaviour property), and every `.md` file of each `--nearest` skill; nothing else. The
`--*-source` flags are words written into the entry as given (for example an artifact and its
version, or a repository at a commit).

The reader judges each style rule and each reader property once, `met`, `gap` or
`not-applicable`, and lists each gap: its file and line (for a missing element, the line after
which it belongs, 0 before line 1), the rule it is against (`S<n>`, `C<n>`, or
`nearest:<skill>:<path>:<line>`, the line of a nearest skill that shows what this skill lacks or
contradicts), its kind (`present` or `missing`) and why. The prompt says that no gap rests on a
word or line count alone: a length the style text gives is a hint to the writer, not a threshold.
An answer is refused when a gap names no file of the skill, a line past its end, or a rule that is
none of these; when it judges a rule twice or not at all; when a rule judged `gap` has no gap; or
when a gap is against a rule not judged `gap`.

It writes the entry to `--out`: `{entry: "craft", skill: {name, dir, source, files: [{path,
sha256}]}, style: {file, source, sha256, rules}, craft: {file, source, sha256, readerProperties},
nearest: [{name, dir, files}], reader: {prompt, settings}, input, answer, refusals, spent, usd}`,
where `answer` is `{judged, gaps}`, or null when every reply was refused. It writes the exact user
message beside it, `<out less .json>.input.md` (`input`). It exits 0 with an answer, 1 when every
reply was refused, and 2 when it refuses its arguments, a style text with no rule, a craft list,
or a skill directory with no SKILL.md.

### `nearest`

```
bun scripts/skill-cuts/cli.ts nearest --config <skill-cuts.json> --key-file <file> \
  --skill <skill dir> [--skill-source <text>] --candidates <source>=<root>... --out <row.json> \
  [--request-log <file>] [--budget-usd <n>] [--concurrency <n>]
```

One fresh request of the `nearest` reader (`prompts/nearest.md`). Its input is the skill's name,
its whole description and its SKILL.md less the frontmatter, and every skill of each `--candidates`
root (each `<root>/<name>/SKILL.md`), with its name, its whole description and the source the flag
gives it; the frontmatter is read as `context.ts`'s `readFrontmatter` reads it. Two candidates of
different sources may have one name, so the answer names each candidate by its name and source:
`{verdict, nearest: [{name, source, verdict, missing, reason}], reason}`, with at most two entries,
each `equivalent` (it does the skill's job, for the same trigger) or `partial` (it does a material
part of it); the answer's `verdict` is `none` exactly when it names none, else the best entry's.
It writes the row to `--out`: `{row: "nearest", skill: {name, description, source, dir},
candidates: [{source, root, skills}], reader, input, answer, nearest, refusals, spent, usd}`, where
`nearest` is each entry with the candidate's whole description, and the exact user message beside
it (`<out less .json>.input.md`). It exits 0 with an answer, 1 when every reply was refused, and 2
when it refuses its arguments, a `--candidates` that is not `<source>=<root>`, a root that holds no
skill, or a skill with no SKILL.md.

### `craft-coverage`

```
bun scripts/skill-cuts/cli.ts craft-coverage --root <dir> [--source <text>] [--skip <name>]... \
  --entries <reader-findings.jsonl>
```

The inventory of the craft entries. It lists each `<name>/SKILL.md` directly under `--root`, less
each `--skip` name, and prints each with its craft entry (the last one, with its gap count, or
`every reply refused`) or `none`. An entry is a line of `--entries` whose `entry` is `craft` (the
other lines, a proposal's entry among them, are passed over), and it matches a skill by its
skill directory's name and, with `--source`, by its `skill.source`. It calls no model and reads
nothing else. It exits 1 naming each skill with no entry, 2 when it refuses its arguments, a root
it cannot read or that holds no skill, or an entries file it cannot read or a line of it that is
no JSON or no craft entry, and 0 otherwise.

### `control craft`

```
bun scripts/skill-cuts/cli.ts control craft \
  (--remove-section <heading> | --join-lines <a>-<b>) \
  --copy-line <a>[-<b>] [--copy-kind <present|missing>]... [--copy-rule <id>]... \
  [--copy-claim <text>] \
  --original-line <a>[-<b>|all] [--original-kind <present|missing>]... [--original-rule <id>]... \
  [--original-claim <text>] \
  <craft's flags: --config, --key-file, --skill, --style, --style-source, --craft, \
   --craft-source, --nearest, --skill-source> --out <dir> \
  [--request-log <file>] [--budget-usd <n>] [--concurrency <n>]
```

The craft reader's control. It writes a changed copy of `--skill` under `<out>/copy/<skill dir
name>/`, with SKILL.md changed: `--remove-section` removes the heading's line and every line up to
the next heading of the same or a higher level; `--join-lines a-b` joins the list items of lines a
to b into one paragraph at line a, their list markers removed. Then it runs `craft` on the copy
and on the unchanged skill, each with the same `--nearest` (`<out>/copy.json` and
`<out>/original.json`, each with its `.input.md`).

Each side has its own criteria. A gap is a candidate of a side when its line is in the side's
range (a line of SKILL.md: the copy's line numbers for the copy, the unchanged skill's for the
original; `all`: any line of any file), and, where given, its kind is one of the side's kinds and
its rule one of the side's rules (no `--*-kind`: any kind; no `--*-rule`: any rule). A rule is an
`S<n>` of the style text, a reader `C<n>` of the craft list, or `nearest:<skill>:<path>:<line>`.
Where a side gives a claim, the `claim` reader (`prompts/claim.md`, one fresh request a candidate)
answers whether the candidate's gap states the claim, with its reason; each claim request's input
and answer go to `<out>/claims/<side>-<n>.json`, with its `.input.md`.

The control holds only when the copy has a candidate that states its claim (any candidate, where
it gives no claim) and the unchanged skill has no candidate that states its claim (no candidate,
where it gives no claim); a candidate of the unchanged skill whose every claim reply was refused
does not let it hold. It writes `<out>/control.json` (each side's criteria, candidates with their
claim answers, whether it holds and why; whether the control held and why; the spend), prints each
side and its candidates, and exits 0 when the control held, 1 when it did not (or a craft read's
every reply was refused), and 2 when it refuses its arguments, a heading SKILL.md does not hold
once, a line of `--join-lines` that is no list item, or an `--out` that holds a copy already.

A control that a removed section's check is missing, for example: `--remove-section '## Step 4:
Verify' --copy-line 31-43 --copy-claim <the claim> --original-line all --original-claim <the
claim>`. A control of the paragraph rule: `--join-lines 37-39 --copy-line 37 --copy-kind present
--copy-rule <the paragraph rule's id> --original-line 38 --original-claim 'line 38 holds more than
one rule'`.

### `build-cases` and `build-tasks`

```
bun scripts/skill-cuts/cli.ts build-cases --config <skill-cuts.json> --key-file <file> --cut <id> \
  --skill <name> --old <skill dir at the cut's base> --sessions <sessions.jsonl> --seed <n> \
  --copies <copies out dir> --count <count dir> --out <cases.jsonl> [--max <n>] [--budget-usd <n>] \
  [--concurrency <n>] [--stop-projected-usd <n>] [--stop-after-sessions <n>] [--stop-zero-case-usd <n>]
bun scripts/skill-cuts/cli.ts build-cases --config <skill-cuts.json> --old <skill dir at the cut's base> \
  --copies <copies out dir> --count <count dir> --reprofile <cases.jsonl> --out <new cases.jsonl>
bun scripts/skill-cuts/cli.ts build-tasks --config <skill-cuts.json> --key-file <file> \
  --skill <skill dir> --store <db>... --out <tasks.jsonl> [--model <model>] \
  [--profiles-out <dir> --copies <copies out dir> --count <count dir> --context-session <store>:<hash>] \
  [--max-candidates <n>] [--max-search-minutes <m>]
bun scripts/skill-cuts/cli.ts build-tasks --config <skill-cuts.json> --copies <copies out dir> \
  --count <count dir> --reprofile <tasks.jsonl> --profiles-out <new dir> \
  [--context-session <store>:<hash>]
bun scripts/skill-cuts/cli.ts build-tasks --first-read --config <skill-cuts.json> --key-file <file> \
  --skill <skill dir> --sessions <sessions.jsonl> --seed <n> [--max <n>] --out <tasks.jsonl> \
  [--model <model>] [--profiles-out <dir> --copies <copies out dir> --count <count dir>]
```

`build.ts`: `build-cases` pulls each candidate session through the config's `psqlCommand`, has
the `case` reader propose the step the skill governs, reading the session from the skill's read
on in windows of at most 40 messages, at most 6 windows a session, and writes the cases
`loadCases` reads, each with the evidence for its rebuilt context; `build-tasks` writes the need
and near-miss tasks of a load test from the first requests of real sessions. `--count` and
`--copies` are the count directory and the copies output that `attribute` read to make the
sessions file: `build-cases` places each session's read of the skill with `attribute.ts`'s own
rules (the prefix table, then a checkout, then the store's context), and keeps a session only
when they place it at the cut repository's copy; a read they place at another copy, or at none, is
`not-cut-copy`, with the rule named. A session is a
candidate only on a model the gateway serves (`servedModels`: its id after the last `/`, so a
`claude-*` model behind any gateway's prefix counts); a session on any other model is counted as
`model-not-served` and never run on another model. The `case` reader is offered only the base
text's lines that the session's own read of that file held (a session whose reads hold none is
`no-common-line`, before any reader call). A case's profile lists what each installing unit
shipped at the session's start: each repository of a unit is read at its last commit at or before
that start (its tree, its `SKILL.md` files and its manifests) and resolved by `copies.ts`'s rules,
so every skill of the profile is in its repository at the commit the case names, and the case's
evidence says which skills the later copies output lists that the unit did not ship then. A read
whose skill none of its units ships at that start, because the later copies output names a unit
as its manifest names it later (a marketplace or a plugin renamed since: the read's Oh My Pi
plugin cache directory, `<marketplace>___<plugin>___<version>`, shows the old name), is placed by
the prefix rows those manifests give at that start, at the unit that installed it then. The
session repository is the repository the session's working directory is a checkout of: for a
Legion pane, the repository of its workspace `/legion/workspaces/<owner>/<name>/<workspace>/…`
(the checkout at `<workspace>`); for a dev-box session, the repository of the copies output's list
named as the last part of its working directory, or of the nearest parent named as any listed
repository, when one repository alone has that name (otherwise none, and the evidence names the
repositories that matched); the checkout's root is that directory. The profile names that
repository and its commit at the session's start (`sessionRepository`), and lists the
repository's project skills at that commit, as Oh My Pi lists them: each
`<level>/.omp/skills/<name>/SKILL.md` (one with a description), then each
`<level>/.claude/skills/<name>/SKILL.md`, where the levels are the working directory's path in the
checkout and each of its parents up to the root, the nearest first (a working directory
`<root>/agents/agentvm` reads `agents/agentvm/`, `agents/` and the root). A session repository
that is no unit's repository is read only where Oh My Pi reads a project, one directory a GitHub
call: each level, its `.omp/`, `.claude/`, `.agent/`, `.agents/`, `.gemini/` and `.github/`, its
`.omp/skills/` and `.claude/skills/`, and each skill directory in those. So a repository whose
whole tree GitHub cuts short (`truncated`) is read too. Where another skill has
the same name, the profile holds the copy Oh My Pi gives the session (unless the session read the
other): a project's `.omp/skills` comes before `~/.omp/agent/skills` and extension packages; its
`.claude/skills` comes after those and after `~/.claude/skills`, and before Claude Code plugins;
of one kind, the nearer level comes first; a pool an extension's `resources_discover` names comes
before them all. The case's evidence cites the Oh My Pi source for each. Every top-level session
in a checkout has its session repository's context files as Oh My Pi reads them: one at each
level from the working directory up to the checkout's root, the one its ranking keeps in that
level's slot (`.omp/AGENTS.md` at the nearest level whose `.omp/` holds anything, priority 100;
the working directory's `.claude/CLAUDE.md`, 80; `.agent/AGENTS.md` and `.agents/AGENTS.md`,
70; the working directory's `.gemini/GEMINI.md`, 60, and `.github/copilot-instructions.md`, 30;
then `AGENTS.md` and `CLAUDE.md`, 10, in that order), listed the farthest level first, each at
its path in the repository, and the profile's `workingDirectory` names the working directory's
path in the checkout when it is below the root. A Legion session also has its project's
instructions file. A top-level devbox session also has the user-level context file Oh My Pi
reads, marked `user: true` in the profile: `CLAUDE.md` in Claude Code's config directory, which
the dev box's `CLAUDE_CONFIG_DIR` names, the `.claude/` of the repository that installs its
units. That is the one repository of the profile's units that holds Claude Code's settings there
(`.claude/settings.json`) at the session's start, and the profile names its `.claude/CLAUDE.md` at
that commit; with none or several such repositories, or no `CLAUDE.md` there, the evidence says it
is left out and why. A capture writes the user-level file as `CLAUDE.md` in a scratch Claude Code
config directory and runs Oh My Pi with `CLAUDE_CONFIG_DIR` there, so Oh My Pi reads it as its
user-level file; it writes the session repository's context files at their paths in a scratch
checkout and runs Oh My Pi in the `workingDirectory` inside it, so each is read at the depth the
session read it, and a file of a parent is shown under the session's checkout (its working
directory less that path). `load` on Claude Code gives the user-level file as user memory,
`CLAUDE.md` in its scratch `CLAUDE_CONFIG_DIR`, and the other files in the same checkout. A
subagent's recorded prompt is sent as recorded.
`--reprofile` builds the profiles of an existing case file again that way, from the cases and
their sessions' heads (no transcript pulled, no reader call), and writes a new file in which only
`profile` and the profile's part of `evidence` differ. A case holds no working directory, so each
case's is the header's `cwd` in its session's head, read from its store with `session-head.sql`
(one query per store, through the configuration's `psqlCommand`; each head is kept under
`<out>.sessions/`). For a case whose session the store no longer holds, the evidence says so
(`no session head`), and the session repository is the one its old evidence names (`session
repository <repo>` or `no session repository`); a case built before that evidence was written is
taken to be in the workspace of the repository whose `AGENTS.md` its old profile holds. A Legion
session's
role comes from the daemon's first message to it, its catch-up notice, or the claim it holds
beside its workspace; a subagent's session (`subagent:<agent>`) keeps the system prompt and tools
its `session_init` line recorded, and its capture sends that prompt with only the skill's
listing line changed per arm. Once a step is found, two more readers decide whether the case is
kept. The `restate` reader reads every message the case keeps before the step, back to its first
and less the skill's own read results, whole (a long message in overlapping parts), in windows of
at most 100,000 characters and at most 10 windows; a restatement of the line in any window drops
the case (`history-restates-line`), and a case whose messages it could not read in full, for too
many windows or a window it could not read, is dropped (`restatement-check-incomplete`), never kept.
The `audit` reader reads the line, the messages before the step as the judge reads them and the
real step, never the criterion, and answers `follows`, `breaks` or `not-governed`; only `follows`
keeps the case (else `audit-breaks`, `audit-not-governed`), and its answer and reason go in the
case's evidence. The summary records, per session, the check's messages read beside those it must
read, its windows, and the audit. `--budget-usd` stops the build once its readers cost that much at
list price (exit 3). The case file and its spool hold real session text, so `build-cases` (both
forms) refuses an `--out` inside a git or jj checkout (exit 2).

The stop rule stops a skill's build (exit 4) as soon as
`--stop-zero-case-usd` dollars (60 unless given) are spent with no case built, or, once at least
`--stop-after-sessions` sessions (40) are decided, when the cost projected to `--max` cases from
the build's own cases and spend passes `--stop-projected-usd` dollars (200): projected = max ×
spent ÷ cases, unbounded with no case, so a build with no case after 40 sessions stops. It is
checked after each session decided, before the build reads another, and the projection needs
`--max`. The summary's `stopped` then holds `{rule, sessions, cases, spentUsd, projectedUsd}`
(`rule` the flag's name, `projectedUsd` null when unbounded); the sessions still open are
`not-needed`, and each drop keeps its reason. A run that goes on checks the rule on the sessions
the earlier runs decided before it reads the store, so a build the rule stopped stops again at
once with the same bounds.

Each session's transcript is pulled into a file of its own beside the output (`<out>.spool/`,
removed once the session is decided, and the directory once the build ends), one row of the
pull's output at a time, and read from there line by line. Only the entries the builder reads
whole are held whole (the header, a `session_init`, model and thinking changes, context resets, a
system prompt's digest, Envoy role claims); every other entry is held light (a message with its
roles, tool calls and `[Skill file: …]` line) and read whole again only where a check or a reader
needs it (the reader's windows, the reads it offers, the case's messages, the compaction before
the step). So a session's memory follows its number of entries and what its case needs, not the
length of its file.

`--concurrency <n>` (4 unless given) lets `build-cases` read up to n sessions at once; the store is
still read one query at a time, and the gateway takes n calls at once. Each session's outcome is
decided and written in the seeded order, a session that finished first waiting for those before
it, so the cases kept are the first `--max` cases of that order, as one session at a time would
keep them. At most n sessions are open (being read, or read and waiting), so at most n spool files
are there at once. When the build ends, each session still open is stopped before its next
reader call and recorded with what it spent, as `not-needed` (or, at the budget, as stopped by
it).

A stopped build goes on where it stopped. The summary records the run's inputs: the cut, the
skill, the seed, and the SHA-256 of `--old`'s files, the sessions file, the copies files read and
the count's query outputs. Run again with the same `--out` and the same inputs, `build-cases` keeps
every session already decided (dropped, or a case the cases file holds), with no pull and no
reader call, and goes on in the same order from the first session not decided, until `--max`
cases in all; the budget counts the dollars already spent, and the summary's spend adds the runs.
A session `not-needed` or stopped by the budget is not decided, so it is read again when it is
needed. It refuses (exit 2, files left as they were) other inputs, naming each difference; a
cases file with no summary; and a summary that records no inputs (one an older `build-cases`
wrote). A kill at any moment leaves both files readable: each case is appended as one whole line,
then the summary is written to a temporary name and renamed; a case line the summary does not
record (a kill between the two) is left out when the build goes on, and its session is read
again. `build-tasks` goes on the same way: its queries, one per store and day, take an hour and
more on the real stores, so the summary records the terms and each query as it ends, and
`<out>.found.jsonl` the messages each found (path hashes and texts; removed once the tasks are
written); a run of the same skill text, stores and window makes no terms call and repeats no
recorded query, and a finished build is left as it is.

The `tasks` reader says of each need task whether the skill governs its first step: whether an
agent doing the task would read the skill at once, before any other act. `build-tasks` leaves out
each need task whose first step the skill does not govern, and its summary counts them
(`droppedNeed`) and keeps each with the reader's reason (`dropped`). The reader must give at least
5 need tasks whose first step the skill governs, and 5 near-miss tasks.

`build-tasks --first-read` writes the need tasks of the load test's positive control. It takes the
sessions file `attribute` wrote for the skill (`{"store", "hash", "path"?}` per line) in the order of
`--seed`, pulls each session in turn, and keeps a session only when its first assistant message
reads the skill (load.ts's rule: an Oh My Pi `read` of `skill://<name>` or of its `SKILL.md`). It
takes that session's first request as a need task as it is, with its secrets taken out, and no
reader chooses among them; a request that an earlier session already made is taken once. The
`tasks` reader then says only whether the skill governs each request's first step, and a request
whose first step it does not govern is left out and counted, with the reader's reason, as above.
The build stops once `--max` requests (5 unless given) are kept, or the sessions run out. The
summary records each session it tried and why it was not taken (`not first`, `same request`, or a
pull that failed). A stopped run goes on as `build-tasks` does: the summary records its inputs
(the SHA-256 of `SKILL.md` and of the sessions file, the seed and `--max`), each session tried
and each request judged, and `<out>.found.jsonl` holds the requests found until the tasks are
written.

`--profiles-out <dir>` (either mode) runs each task in the rebuilt context of the session it came
from. For each task from a session, `build-tasks` pulls the session again and rebuilds its profile
as `build-cases` rebuilds a case's at the session's start: the units its host had and what they
shipped at their commits then (from `--copies` and `--count`), the plugins, the context files, a
subagent's recorded system prompt and tools, and the context (`devbox`, `legion:<project>:<role>`
or `subagent:<agent>`), less the tested skill. It writes that to `<dir>/<task id>.json` (load.ts's
`LoadProfile`), and the task line's `profile` is the file's path. A session whose rebuilt context
does not list the skill (a subagent's recorded listing lacks its line, or another context's
profile does not install it) would test nothing, so it is dropped as `skill-not-listed`: in the
reader's mode each offered message's session is rebuilt first, the drop is counted in
`candidateDrops`, and the next candidate is taken in its place; in `--first-read` the session is
dropped when it is tried, and counted (`skillNotListed`). A written task takes the profile of
`--context-session <store>:<hash>`, the session the reader's mode requires for it (the most-read
session of the context that installs the skill). That profile is rebuilt before any reader call;
the build fails when that session gives none, and fails too (exit 1, since it is found after a
store read) when its rebuilt context does not list the skill. The summary's `profiles` records
the context session, each task's profile with its evidence, and why a task has none. A build
that has already finished gets no profiles from `--profiles-out` afterwards; `--reprofile` builds
them again.

`--reprofile <tasks.jsonl> --profiles-out <new dir>` builds the profile of each task of a tasks
file again, as `--profiles-out` builds one now, with no candidate search and no reader call, so the
tasks a bounded search chose stay as they are. A task from a session takes its session's profile:
the session is found in its store by its path hash (`session-paths.sql`, one query per store) and
pulled as the search pulls it. A written task takes the profile of `--context-session`, else of
the context session the tasks file's summary (`<tasks.jsonl>.summary.json`) names. The command
writes each profile to `<new dir>/<task id>.json`, the tasks again to `<new dir>/tasks.jsonl` with
each `profile` naming its new file, and `<new dir>/tasks.jsonl.summary.json`. The summary says of
each task whether the profile's bytes changed (`unchanged` or `changed`) and lists the evidence
items added and removed against the old summary's. A task whose session its store no longer holds
keeps its old profile (`kept`), and its evidence item `no session in the store` says so. A
session task whose rebuilt context no longer lists the skill is left out and named, and the
command exits 1. A store read, a pull or a rebuild that fails stops the command (exit 1) before it
writes anything to `<new dir>`; `<new dir>` must be new or empty.

`--model <model>` (either mode) keeps only the sessions whose first request ran on that model, as
the session's file names it and `attribute --models` writes it: its last `model_change` before its
first assistant message, a thinking suffix left out (so `<gateway>/openai/gpt-6.1-sol` and
`openai/gpt-6.1-sol` are two models). Every other session is dropped and counted as
`other-model`: in the reader's mode each offered message's session is pulled and the next
candidate taken in its place (`candidateDrops`), and in `--first-read` when the session is tried,
where the summary's `model` (`{name, firstRead, otherModel}`) and the printed line say how many
sessions tried on that model read the skill in their first reply, and say so when they are fewer
than 5. Written tasks are unchanged. The summary names the model, and the inputs hold it, so a run
that goes on with another model is refused.

`--max-candidates <n>` and `--max-search-minutes <m>` bound the reader's mode's candidate search,
which runs with `--model` or `--profiles-out` and pulls each candidate's whole transcript, one at a
time: on the real stores the terms can match tens of thousands of messages, and the search can run
for days. The search ends before its next candidate once the candidates decided (accepted or
dropped) reach n, or its own time reaches m minutes, both summed over the runs of one `--out`; the
time is checked between candidates, so the last one decided can run past it. The `tasks` reader
then gets the messages accepted so far and writes the rest of the tasks (in the
`--context-session` context with `--profiles-out`), as when the candidates run out. Each decision
is appended to `<out>.candidates.jsonl` as it is made, after a first line holding the bounds, and
so is the bound that ends the search. A run that goes on takes each candidate decided there as it
was decided, with no pull, and refuses (exit 2) bounds other than those, naming both values of
each; a file that holds only the bounds, or no file (a run killed during its queries), takes any
bounds. The summary's `search` holds the bounds, what ended the search (`--max-candidates`,
`--max-search-minutes`, `candidates ran out` or `enough offered`), the candidates decided and
accepted, and the search's seconds; `candidateDrops` holds the drops by reason. The file is
removed once the summary is written. With neither bound the search runs to 60 messages offered or
the last candidate, and writes no file. A bound is refused without `--model` or `--profiles-out`
(no search runs) and with `--first-read` (it stops at `--max`). Build a load test's tasks with
`--max-candidates 60 --max-search-minutes 90`.

## The request log

Each model request of `behaviour`, `read`, `control`, `craft`, `nearest` and `load` adds one JSON
line to the request log (`<out>/requests.jsonl` for `behaviour`, `load` and `control craft`,
`<out file>.requests.jsonl` otherwise):
what the call was for; when it was asked for (`at`), when it got its place and was sent
(`sentAt`) and how many calls were in flight then, it among them (`inFlight`); the session id it
went with (`userId`: its `metadata.user_id` or `prompt_cache_key`); the model, the thinking type
and budget, `max_tokens` (a Responses body's `max_output_tokens`), the temperature, the effort (a
Responses body's `reasoning.effort`); and the reply's answering model (`answeredBy`), its message
id, its request id and the provider's request id, the gateway's headers of it (`gateway`:
`x-litellm-model-id`, `x-litellm-model-group`, `x-litellm-attempted-fallbacks`,
`x-litellm-attempted-retries` and the provider's request-id headers, each it came with), its
cache read and write tokens, usage and dollars at list price. It never holds the key or a
message's text.

## Exit status

0 done (a control held); 1 failed (every reply of a reader refused, a control that did not hold, a
call, a load test's task or an `--after-case` command that failed); 2 refused before any model
call or store read (among the refusals, an `--out` inside a git or jj checkout, from `behaviour`'s
stage commands, `load` and `build-cases`); 3 stopped (the budget is spent, or the peak memory
passed 1 GiB, 1.5 GiB for `copies`); 4 stopped by build-cases' stop rule.
