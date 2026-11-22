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
  --old <skill dir at the cut's base> [--new <skill dir in the cut> | --ours <skill dir of ours>] \
  --cases <case file> [--case <id>] \
  [--planted-line <text>] [--budget-usd <n>] [--concurrency <n, default 8>] \
  [--after-case <command>] [--request-log <file>] \
  [--models <models.yml>] [--omp <binary>] [--repos <dir>] [--hint-model <model>] \
  [--omp-config <file>]... [--cache-dir <dir>] [--scratch <dir>] \
  [--reading-models <file>] [--limits <file>]
```

One skill per run: `--old` is that skill's own directory (their skill), and `--cases` holds that
skill's cases (`cases.ts`), one JSON object per line. The old text is compared with one arm, the
compared arm: `--new` (the skill's directory in the cut), `--ours` (a skill of ours, "The arm of
ours" below), or neither, which compares the old text with no skill. `--new` and `--ours` exclude
each other (exit 2). Each stage records its compared arm (`compared`; a stage that records none
was run with `--new` before it was recorded), and a command whose `--out` holds runs of another
compared arm is refused (exit 2). A case is informative when old passes at least 2 more of its
10 screen runs than no skill; it is flagged when it is informative and old passes at least 2 more
than the compared arm (with no skill compared, that is informative); and a confirmed case is a
loss when the compared arm passes at least 10 fewer of 40 fresh runs than old.

- `screen`: per case, the three judge controls (the real step must pass; the case's
  `violatingStep`, a step that breaks the line, must fail; "I will continue." must fail; a case
  that fails any is dropped, naming the control), then a capture of each arm (old, the compared
  arm unless it is no skill, and none), 10
  replays of each, each reply judged blind with all arms shuffled, and runs made again for the runs
  with no verdict, at most 5 more per arm. The session cases (`source.kind` `session`, or none)
  are screened first, in the file's order, and the written cases (`build-cases --written`) after
  them: at most 15 screened cases of each kind, until the skill has 6 informative cases in all. A
  case whose model the gateway does not serve (none of the configuration's `servedModels` has its
  id, the text after its last `/`) is dropped before any call as `model-not-served`; a case on a
  served model runs as that served `provider/model`, so an Anthropic model behind another
  gateway prefix runs as `anthropic/<id>`. With `--reading-models` (the skill's
  `<cut or set id>/models/<skill>.json` that `attribute` writes: its reading sessions by model),
  a skill with fewer than 6 reading sessions on models the gateway serves gets no session case
  screened: with no written case it is refused before any call (exit 2), it gets no verdict, with
  the reason `too few sessions on served models` and its served and total counts, and the walk of
  the cut's ranking goes on to the next skill; with written cases, the screen says the session
  cases are not screened and screens the written ones, and the skill's verdict, or its reason for
  none, is theirs, never that refusal.
- `confirm`: 40 fresh runs of old and the compared arm for each flagged case. `confirm --case
  <id>` makes 40 fresh runs of old and the compared arm on that one case, with no flag gate and
  no gate of 6 informative cases, and no screen or judge controls: give it its own `--out`. It
  refuses an id the case file does not hold, and a case whose model the gateway does not serve
  (exit 2).
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
- `report`: the compared arm, each case's passes and runs per arm and stage, the cases of each
  kind (`source.kind` `session` or `written`; a case with none is a session case) and the
  informative cases of each, the control case's passes of old, `minus` and
  `twin` and what each control says, each model of the cases with its cases, the skill's verdict
  and its reason, the drops per judge control and the drops counted as
  `model-not-served` and as `unreadable-directory`, an estimate of the procedure's false-loss
  chance at the cases run (exact binomial under identical old and compared texts, the pass rates
  taken from each case's own screen; its assumptions are printed with it),
  a table of the chance it finds a loss of 10 to 40 points in one informative case, each screened
  case's smallest drop ("The smallest drop" below), the spend
  at list price, the peak memory, how many captures' system-prompt digests differ from their
  session's, and the text of `--limits <file>` as it is (the limits a result states, so no limit
  is written into the runner), or that none was given. Beside the verdict, as descriptions that
  change no rule: each kind's screened cases (their ids), how many are informative, and each
  screen arm's passes summed over them; the mean over the screened cases of old's screen pass
  rate less the compared arm's, over all of them and over each kind's, with a 95% interval
  clustered by case (each case's difference is one observation, so the interval is Student's t
  over the cases; one case has none, and a kind with no screened case has no mean); and how many
  screened cases tie (old and the compared arm pass alike), floor (0 of 10 in both), ceil (10 of
  10 in both) and reverse (the compared arm passes more than old), a floor or a ceiling being a
  tie too. It names the judge: the SHA-256 of `prompts/judge.md` and the settings its header pins,
  and how many stages of the results record that judge, another one, or none. Each stage records
  the judge it was made with (`judge`) before its first judgement; a results file made before
  that records none and still loads. A skill whose planted-loss control is no loss gets the reason
  "deleting the case's line showed no detectable change (planted-loss control not a loss)" (with
  `--planted-line`, the planted line in place of the case's), and one whose control's runs are
  unscreened "planted-loss control not a loss: its runs are unscreened". It calls no model, and
  needs only `--config`, `--out`, `--old` and `--cases`; its compared arm is the one its results
  record (a `--new` or `--ours` it is given must be that one). With the cut compared, `keeps`
  prints as "no loss detected by the registered rule" and a loss names the cases where the cut's
  text passes at least 10 fewer of 40 fresh runs than the old text. With no skill compared, the
  report says once
  that the arm with no skill informs and clears nothing; `loss` prints as "no skill loses against
  their skill on <n> cases" and `keeps` as "no loss of no skill detected by the registered rule".
  With ours compared, it calls the arm of ours a bundle where its text replaced a skill of our name
  that a case's profile held ("their skill hidden, ours at the given text (it replaced the
  profile's own <name>)"), names on each case's line the history reads the arm of ours removed,
  and gives ours against old twice: over all screened cases, and over the screened cases with no
  removed read (each arm's screen passes, and the confirm passes and the count of losses where
  confirm runs were made). With
  `--reading-models` it also prints each model of that file with its sessions and whether the
  gateway serves it, the sessions whose file names no model and those in no models query's rows,
  and the served models the screened session cases ran on, each with its cases; a verdict then
  ends with the reading sessions it speaks for, on each served model its screened session cases
  ran on (a model counted under the served model of its id, so `gateway/claude-sonnet-5-5` under
  `anthropic/claude-sonnet-5-5`), and those it does not speak for, on models the gateway does not
  serve and on served models no screened session case ran on. A written case is a written task in
  a recorded session's context, never a reading session: the report counts the screened written
  cases on a line of their own by the model each ran on (`written tasks: <n> on <model>, …`), and
  a skill whose screened cases are all written speaks for no reading sessions, which its verdict
  says. A skill too few of whose reading sessions are on served models, and with no written case,
  gets no verdict. Without `--reading-models`, it says the reading sessions by model were not
  given, and the verdict names no models.

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

#### The arm of ours

`--ours <dir>` is a skill of ours, whose frontmatter `name` (and directory name) may differ from
the case's skill (`arms.ts`). Its profile is the case's less every copy of their skill's source
(the repository and path of the copy listed under their name, at any commit, under any
namespace), as the arm with no skill leaves them out, with ours under our own name and
description in place of the copy listed under theirs; a copy of another source that shares their
skill's name stays as the profile has it. Where the profile already lists a skill of another
source under our name, it keeps that entry, replaced by the `--ours` text, and adds no copy of
theirs, so no name is installed twice (the context refuses a name installed twice), and the stage
records that it replaced it (`ours.replacedOwn`). Each read of a file of their skill
in the history becomes a read of `skill://<ours>/<the same path inside the skill>`, answered from
our directory at our installed directory (theirs, with our name as its last part); where ours
has no such file, the read call and its result are removed as the arm with no skill removes them,
and the stage records the read's path (`ours.removedReads`). So a renamed unchanged copy of their
skill gives old's texts but for the name. In a reply, a read of `skill://<ours>/…` is answered
from our directory, and a read of their skill gets Oh My Pi's `Unknown skill: <theirs>` text; both
are answered, not judged. The capture lists our skill under its own name and description, and a
subagent's recorded listing names ours in their skill's line. Its captures are kept under
`<out>/captures/ours-<digest of our directory>/`.

#### The judge reads no skill name

In a run with an arm of ours (`--ours`), before the judge reads a case's criterion (pass and
fail), its line, its history and a step, in every arm, the case's skill name and ours become
`the-skill` (`blind.ts`): `skill://<name>` reads `skill://the-skill`, a path part `/<name>/`
reads `/the-skill/`, and the name as a whole word reads `the-skill`. A longer name that holds the
name as part of a word (`alphabet`, `alpha-extra` for `alpha`) stays. So the judge cannot tell
the arm of their skill from the arm of ours by a name, and the criterion uses the step's words. A
run with `--new` (both arms hold the case's skill) or with no skill compared (the judge reads the
case's own history in every arm) blinds nothing: its judge reads the case's text as it is.

#### The smallest drop

For each screened case, the report gives the smallest drop, in 5-point steps below the case's
screened pass rate of old, that the rule (the screen's flag, then the confirm's loss: `score.ts`'s
`lossChance` with the compared arm) catches with a chance of at least 80%, with that chance. With
the cut or ours compared, the compared arm passes the drop below old and no skill at the case's
screened rate; with no skill compared, no skill is the arm that drops, and the screen's gap is
counted once (informative and flagged are one condition). Where no drop down to a rate of 0
reaches 80%, the figure says so and gives the chance at the largest drop. The report gives the
median over the screened cases and over the informative cases ("none" when there are none; a
case no drop reaches counts above every other). At old 0.9 and no skill 0.1 the figure is 40
points at 87.52%, compared with the cut or with no skill.

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
  --prompt <case|restate|audit|map|attack|same-job|sentence|names|tasks|craft|nearest|claim|written-case> \
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

Each count row also names, for each file, the keys of the reads that put it in the row (`calls`,
and `unpaired_calls` for calls with no result). A key is md5 of the call's id, a space and the
call line's UTC timestamp to the microsecond, cut to 16 hex characters. Inside one query, a key
that two files hold counts in the file whose path hash sorts first, and `shared_call_ids` counts
such keys.

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

Six install paths give a skill to Oh My Pi from outside the owners' trees, and each is a unit of
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
  entry the installer's `find … -name … -exec rm` removes from the pool. A clone made as one entry
  of a pool's skills directory (`ensure_vendor <url> <pool>/skills/<name> <ref>`, a repository that
  is one skill, its `SKILL.md` at its root) gives that repository's root skill under `<name>`.
- A plugin an installer script installs from a marketplace into Oh My Pi's own plugin tree
  (`omp plugin install <plugin>@<marketplace>`, or a helper given the marketplace's repository and
  `<plugin>@<marketplace>`): the marketplace's repository is read at its default branch, and the
  plugin's source at the commit its marketplace entry pins (`sha`, else `ref`). Oh My Pi lists
  such a plugin's skills from its manifest's `skills` entries, else its `skills/`
  (discovery/claude-plugins.ts, loadSkills, lines 226-275; an install of Oh My Pi's own is not
  Claude Code's registry, so no setting gates it: lines 44-53). A line's host is read from its
  words less its `<plugin>@<marketplace>` and `<owner>/<name>` words, so a marketplace named
  `claude-plugins-official` names no host.
- A `gh skill install <repo> <skill> … --pin <ref>` of an installer script (each `$NAME` the
  script sets read so): the repository's `skills/<skill>` at that ref, installed at
  `~/.agents/skills/<skill>`, which Oh My Pi's agents provider lists (discovery/agents.ts, lines
  123-127 and 158-170).
- A Legion deployment's files: its role setup script (`legion/*setup.sh`) fetches a catalog
  repository (`repo=<owner>/<name>`, `omp plugin marketplace add`) and installs plugins of it
  (`omp plugin install`); its program (`legion/main.go`) names the plugins every role installs
  (`everyRolePlugins`); a project file's `verificationSkillsLeaveOut` names the catalog plugins
  its roles leave out; and a worker image's Dockerfile (`legion/worker*/Dockerfile`) installs each
  skill of a GitHub release as an Oh My Pi plugin under `/opt/<dir>/` (an `image:<dir>` unit, at
  the release's tag). A marketplace entry whose source is `git-subdir` is the plugin directory
  `path` of that repository at its pin.

Each repository a pin names (the dependency's, the clone's, the marketplace's, a marketplace
plugin's source, a `gh skill install`'s) is read at the pin's ref, in place of its default
branch's commit when an owner's listing holds it too; `repositories.tsv`'s `branch` names the
ref. A repository with no pinned ref is read at its default branch. A marketplace read at its pin
pins its plugins' sources in turn, so the pins are read again until no new one appears. The prefix
rows of these units reach the table only when the configuration's `targets` name their
repositories. `build-cases` and `build-tasks` read a pinned repository at the ref its installer
names at the session's start; where `repositories.tsv` names a ref (a pin's tag) made after the
session's start, they read the repository's default branch then.

### `attribute`

```
bun scripts/skill-cuts/cli.ts attribute --config <skill-cuts.json> --count <count dir> \
  --copies <copies out dir> --spans <spans.jsonl> --out <dir> \
  [--changed <cut or set id>=<file>]... [--models <models dir>]
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
`--changed` takes a skill set of the configuration (`sets`) as it takes a cut: `--changed
<set id>=<file>`, the file one skill directory of the set's repository per line (the set's skills
to rank), ranks them by the sessions the rules above place at that repository's copies, and
writes `<out>/<set id>/ranking.json`, `<out>/<set id>/sessions/<skill>.jsonl` and, with
`--models`, `<out>/<set id>/models/<skill>.json`. A read of a set's skill at
`node_modules/<package>/skills/<name>/` goes to the set's copy by the prefix row `copies` gives
the set's package (when `targets` names the set's repository), not to a skill of ours of the same
name.
A path inside a copy of another repository that an installer writes (`<dir>/vendor/<name>/`, a
package under `node_modules/`, a plugin under `plugins/cache/`) is a checkout only of a repository
the copy's own directories name, and a read inside an `ensure_vendor` clone that no prefix row
maps goes to no copy by the context rule either: it is unattributed.
A read that two files of a store hold (the same call id and timestamp: a forked session copies
its parent's lines) counts in one of them, the file whose path hash sorts first, whichever queries
hold the two, since a fork written on another mtime day than its parent is in another query. A
file stays a session of a skill when one of its reads of it counts in it. `build-cases` reads the
count by the same rule. A count made by an earlier `count.sql`, whose rows have no `calls`, is
refused. The report's store table gives the call ids two files of one query hold, the call ids
files of two queries hold, and the pairs of files of two queries that hold one.

### `set-count`

```
bun scripts/skill-cuts/cli.ts set-count --config <skill-cuts.json> --rows <count dir> \
  --spans <spans.jsonl> --out <dir> [--dotfiles-commit <sha>] [--rows-label <text>] \
  [--not-read <name>=<reason>]...
```

The read count of each skill set's skills (`set-count.ts`, with the pins read by `sets.ts`). It
reads no store and calls no model. The configuration must give two keys: `sets`, `[{id, repo}]`,
each skill set measured against our skills (`id` the set's name, which is also its Claude Code
plugin name and the `<id>:` prefix of its skills in a Claude Code `Skill` call, and may not be a
cut's id or another set's; `repo` its upstream repository, `owner/name`), which `attribute
--changed`, `build-cases --set` and `build-cases --reprofile` also take where they take a cut; and
`dotfilesRepo`, the `owner/name` whose `omp/plugins/package.json` and `installers/omp.sh` pin the
sets (required when `sets` is given), which only this command reads.

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
row of S at the own copy, apart. Only a row's paired reads count, never its unpaired calls. A read
that two files of a store hold (a forked session copies its parent's lines) counts in one of them,
the file whose path hash sorts first, whichever queries hold the two, as `attribute` counts it
(`attribute.ts`'s `countOnce`): a file stays a session of S in a row when one of its reads there
counts in it. Rows made by an earlier `count.sql`, with no `calls` and `unpaired_calls`, are
refused.

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

### `craft-merge`

```
bun scripts/skill-cuts/cli.ts craft-merge --entry <entry.json> --entry <entry.json> \
  [--entry <entry.json>]... --out <merged.json>
```

The merge of fresh `craft` readings of one skill (`craftmerge.ts`). The craft reader's answer
varies between fresh requests of the same inputs (on one skill text it listed 6, 5, 0 and 3 style
gaps), so a skill's findings come from several readings of the same inputs. Each `--entry` is an
entry that `craft` wrote. It calls no model and reads nothing but the entries.

It refuses (exit 2), naming each difference, an entry that is not a reading of the first entry's
inputs: another name of the skill's directory, another set of skill files or another sha256 of
one, another sha256 of the style text or of the craft list, or another set of nearest skills (by
name) or of their files and sha256. It refuses, naming it, an entry whose `answer` is null (every
reply refused), and it refuses fewer than two `--entry` and an entry it cannot read, that is no
JSON or that is no craft entry. It refuses, naming both, one reading given twice: two `--entry`
of one file (the same path once every link and `..` is resolved) or of two files with the same
content (the same sha256), since that reading's gaps would look found by more readings than found
them.

Two gaps are one gap when they have the same `file`, `line` and `rule` (`S<n>`, `C<n>`, or the
same `nearest:<skill>:<path>:<line>`); a different kind does not split them. It writes `--out`:
`{entry: "craft-merged", skill, style, craft, nearest, reader, readings, entries: [{path,
sha256}], gaps: [{file, line, rule, readings, of, kinds, why}], judged: [{rule, gap, met,
notApplicable}], noGap, usd}`, where:

- `skill`, `style`, `craft`, `nearest` and `reader` are the first entry's, as it wrote them;
- `readings` is the number of entries, and `entries` each entry's path and the sha256 of its file,
  in the order given;
- `gaps` holds each gap that any reading lists: `readings` is the count of readings that list it,
  of `of`; `kinds` and `why` are the kind and the why of each listing, in reading order, each why
  with the prefix `reading <i>: `. They are sorted by file, then line, then rule (as text, so
  `S10` comes before `S2`);
- `judged` holds each rule that any reading judged, with the count of readings that judged it
  `gap`, `met` and `not-applicable`, sorted by the rule's letter and then its number;
- `noGap` is true exactly when no reading lists a gap (`gaps` is empty);
- `usd` is the sum of the entries' `usd`.

It prints one line: the skill, the number of merged gaps and how many of them are in each count
of readings (`in 3 of 3: 8; 2 of 3: 6; 1 of 3: 3` for three entries), or `no gap in <n> readings`,
and the spend. It exits 0 when it wrote the merged entry, and 2 when it refuses.

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

### `proposals`

```
bun scripts/skill-cuts/cli.ts proposals --config <skill-cuts.json> --key-file <file> \
  --body <pull request body> --body-source <text> --section <heading> --list <text>... \
  --pr-diff <id prefix>=<gh pr diff output>... --skills-root <repo>=<dir>... \
  --repo-root <repo>=<repository tree> \
  --style <style text> --style-source <text> --craft <craft-list.json> --craft-source <text> \
  --findings <reader-findings.jsonl> --out <dir> \
  [--request-log <file>] [--budget-usd <n>] [--concurrency <n>]
bun scripts/skill-cuts/cli.ts proposals --dry-run --body <pull request body> --section <heading> \
  --list <text>...
```

Reads each open proposal of closed pull requests that a later pull request's body lists, with two
readers (`readers.ts`), and writes one entry per item. `--body` is the body as text (`gh pr view
<n> --json body --jq .body`), and `--body-source` the words the entries give it (the pull request
at the head read).

**The items.** `--section` names a heading of the body, of any level, by its whole text; its section
runs to the next heading of its level or a higher one. Each `--list` names the one line of the
section that starts with its text (`Lines whose target is gone` names `Lines whose target is gone
from this repository (this pull request cannot carry them):`). The list is the `- ` items after
that line, up to the next line after a blank line that is no item, or the next heading; a line
indented under an item goes on with its text. An item is `` - `<id>` (<state>): <text> ``,
whatever the words in the parentheses (`open`, `partly on main`, `open; no ruling found`, `target
gone`, `done in its new home`, or any other). A list no `--list` names is never read. The command
exits 1 naming each line of a named list that is not of that form, gives an id with a character
other than a letter, a digit, `.`, `_` and `-`, or gives an id twice, or that follows an item, is
not indented and is no item. It exits 2 when the section or a list's line is not found once, or no
item follows a list's line. `--dry-run` prints the items with each list's count and reads nothing
else.

**The diffs and the trees.** Each item's id names its pull request by its prefix, the text before
its first `-` (`41-9.12` → `41`): `--pr-diff 41=<file>` gives that pull request's `gh pr diff`
output, which `proposals.ts`'s `splitDiff` splits into files (each path before and after the
change, so a renamed file has both) and hunks (each hunk's `@@` line and its lines, as many as the
line counts). Each `--skills-root <repo>=<dir>` gives a directory of skills (`<dir>/<name>/SKILL.md`)
and the words of its repository at the commit read, which must be the `skill.source` the
repository's `craft` entries give. `--repo-root <repo>=<dir>` gives the whole tree of the repository
the pull requests changed, at the commit read, for a target that is a file of it.

**Two readers per item.** `proposal-target` (`prompts/proposal-target.md`) gets the item's pull
request's whole diff, with each file's hunks by their indexes, first in the request and marked for
the prompt cache (`cache_control: {"type": "ephemeral"}`), so each item of one pull request reads it
from the cache; then the repository of `--repo-root`, the item (id, state and text) and every skill
of the skills roots (repository and name). It answers the hunks that make the item (each a file of
the diff, by either of its paths, and a hunk's index in it, from 0) and its targets: a `dotfiles`
file of the repository by its path, a `skill` of the list by its repository and name, or `none`,
each with why. An answer is refused that names a file the diff does not hold, a hunk past its
file's last, a skill not in the list, or a target twice.

When every target is `none`, no second call is made: the entry says no target was found and gives
the body's own words of the item. Else `proposal` (`prompts/proposal.md`) gets the item, its hunks,
each target's text with its lines numbered (a file read from `--repo-root`, or that the tree holds
no such file; a skill's `.md` files from its skills root, named `<skill>/<path>`), the gaps of each
target skill's `craft` entry of `--findings` (the last entry whose source is the skill's root's
repository and whose directory's name is the skill's; numbered across the targets), the style
rules with their ids, and the craft list's reader properties, as `craft` gets them. It answers the
gaps the change fixes (each a finding by its number, or a style rule or property with its file and
line), the gaps it makes (each a rule, with its file and line where it gives them), and a one-line
summary. An answer is refused that names a finding, a rule or a file the request does not hold, a
fix with both a finding and a rule or neither, a line with no file, or a line past its file's end.

**The entries.** Each item's entry is `<out>/<id>.json`: `{entry: "proposal", id, state, text,
list, line, body: {file, source, sha256}, diff: {pr, file, sha256}, style, craft, findings: {file,
sha256}, input, target: {reader, answer, refusals, spent, usd}, hunks: [{file, hunk, header}],
targets, noTarget, reader, answer, refusals, spent, usd}`. `targets` gives each target read: a file
with its repository, path, file and SHA-256 (null when the tree holds no such file), or a skill
with its repository, name, directory, each file's SHA-256 and its findings entry (`{line, gaps}`,
`gaps` null when every reply of that entry was refused; null with no entry). `noTarget` is
`{bodyWords, why}` for an item with no target found, and then `targets` is empty and `reader` and
`answer` are null; `answer` is the `proposal` reader's answer, `usd` the dollars of both calls. The
user messages sent go to `<out>/<id>.input.md`. A reader whose every reply was refused leaves its
item no entry, and its refusals go to `<out>/<id>.refused.json`.

The first item of each pull request is read alone, then the rest, as many at once as
`--concurrency` lets. An item whose entry exists is not read again, so a run started again reads
only the items with none; an entry of another state or text of its item than the body's is refused
(exit 2), so a changed item is read again only once its old entry is moved away. It exits 0 when
every item has an entry, 1 when an item has none at the end, 2 when it refuses its arguments, a
diff, a root with no skill, a style text, a craft list or a findings file, and 3 when the budget is
spent.

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
bun scripts/skill-cuts/cli.ts build-cases --config <skill-cuts.json> --key-file <file> \
  (--cut <id> | --set <id>) --skill <name> --old <skill dir at the cut's base> \
  --sessions <sessions.jsonl> --seed <n> --copies <copies out dir> --count <count dir> \
  --out <cases.jsonl> [--max <n>] [--budget-usd <n>] [--concurrency <n>] \
  [--stop-projected-usd <n>] [--stop-after-sessions <n>] [--stop-zero-case-usd <n>]
bun scripts/skill-cuts/cli.ts build-cases --config <skill-cuts.json> --key-file <file> \
  (--cut <id> | --set <id>) --skill <name> --old <skill dir> --copies <copies out dir> \
  --count <count dir> --written <n> \
  (--context-session <store>:<hash> | --sessions <sessions.jsonl> --seed <n>) --out <cases.jsonl> \
  [--session-cases <cases.jsonl>] [--reading-models <file>] [--model-pool <file>]... \
  [--budget-usd <n>]
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
`not-cut-copy`, with the rule named. `--set <id>` takes a skill set of the configuration (`sets`)
in place of `--cut <id>`; one of the two is required, and both are refused (exit 2). The set's
`repo` is then the cut repository: a session is kept only when its read is placed at the set's
copy (a read of our skill of the same name, a Legion fork's among them, is `not-cut-copy`), the
profile must install the set's copy, and each case's `cut` holds the set's id; `--reprofile`
finds each case's cut or set by its `cut`. A session is a
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
the same name, the profile holds as the name's copy the one Oh My Pi gives the session (unless the
session read the other): a project's `.omp/skills` comes before `~/.omp/agent/skills` and
extension packages; its `.claude/skills` comes after those and after `~/.claude/skills`, and
before Claude Code plugins; of one kind, the nearer level comes first; a pool an extension's
`resources_discover` names comes before them all; of units of one kind, the first unit's. Oh My
Pi lists a later copy whose `SKILL.md` differs as `<namespace>/<name>` and drops one whose
`SKILL.md` is the same (extensibility/skills.ts, skillNamespace, lines 96-116, and
resolveCollision, lines 164-223), so the profile installs such a copy with its `namespace`, and a
capture lays it out under `namespaced/<namespace>/skills/<name>`, a skills directory it gives Oh
My Pi after its own, where Oh My Pi gives it that name. The namespace is the plugin's name for a
package (its `node_modules` directory's name), a Claude Code plugin or a worker image plugin;
else the directory that holds its `skills/`: a pool's directory, `agent` for `~/.omp/agent/skills`;
for a dotted directory, its provider's id: `native` for a project's `.omp/skills`, `claude` for
`.claude/skills`. A copy of another unit is left out, with why. The case's evidence cites the Oh
My Pi source for each. Every top-level session
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

A dev-box profile installs, beside the units its session's reads come from, every unit the dev
box's own installers install into Oh My Pi at the session's start: the one repository of the
copies output that holds both an installer script (`installers/*.sh`) and Claude Code's settings
(`.claude/settings.json`) is read at its commit of the session's start, with each repository its
installers pin (a repository the copies output no longer names is read at the pinned ref, or its
default branch when the installer names none), and each unit its installers then install into
Oh My Pi (plugin lists, skill pools, plugin installs, linked user skills) is installed. With no
such repository, or several, the evidence says so and only the read units are installed. A skill
whose name a pattern of that repository's Oh My Pi config `skills.ignoredSkills` matches is left
out (Oh My Pi 18.18.6 loads no such skill: extensibility/skills.ts, lines 386-389 and 401-406). A
skill whose `SKILL.md` frontmatter sets `hide: true` or `disable-model-invocation: true` is
installed, so `skill://` reads it, and the evidence marks it `not listed`: Oh My Pi loads it and
leaves it out of the prompt's listing (extensibility/skills.ts, line 494; system-prompt.ts,
lines 961-983). A Legion profile installs what the deployment gives each role at its start: the
deployment is the repository whose project file (`legion/projects/*.yaml`) names the session's
project, read at its commit of the session's start with the catalog its role setup fetches; the
profile installs each plugin of the catalog its roles install (the every-role plugins, and, when
the project file sets `verificationSkillsLeaveOut`, an empty list too, every other plugin but those
it names) and each skill its worker image installs from a release. It installs the plugin lists
(`omp-plugins` manifests) of the repositories its worker image's packages come from (those of the
prefix rows under `/opt/legion/`) and no other: a dev box's own plugin list is no pod's, and the
evidence names each list left out. A daemon-launched controller installs the every-role plugins
alone, which a profile does not tell apart.

The arms and the load test tell the copies of the tested skill apart by their source, not their
name (cases.ts's `testedCopy`: the repository and path of the copy listed under the skill's own
name, at any commit, under any namespace). Each arm installs its own text in every copy of that
source, and `none` leaves out every one, so no arm keeps the old text readable at another path; a
copy of another source that shares the name (listed as `<namespace>/<name>`) stays as the profile
has it in every arm. A load task's profile leaves out every copy of the tested source, which the
load test installs once under `--name`, and keeps the other sources' copies; load reads a
namespaced skill as `<namespace>/<name>` in every name check, and refuses one on Claude Code,
whose layout places skills by their bare name. A profile holds each copy once, since it reads a
repository at one commit: where the session's host had the tested source at a second install path
too (Oh My Pi lists that one as `<namespace>/<name>` when its text there differs), the task's
evidence says so (`held once`), and the listing measure counts such a recorded name missing.

Each case and each load task's profile carries a `listing measure` evidence item, whose source
is one JSON object (`ListingMeasure` in build.ts): how far the listing its rebuilt profile gives
(its skills less those Oh My Pi hides, the tested skill included) is from the listing its
session saw. A session that recorded its system prompt (a subagent's) is measured against that
prompt's `<skills>` lines (`session listing`). A top-level session records only its prompt's
digest, and the build makes no prompt, so `rebuiltDigest` is always null; it is measured against
the recorded listing of a subagent it started (`subagent listing`: the earliest one-level child
of its directory whose `session_init` holds a listing, read with `subagents.sql`, one query a
store, since Oh My Pi gives a subagent its parent's skills: task/structured-subagent.ts, lines
811-818 and 900; task/executor.ts, lines 662-666 and 5256), else it is `not measurable`. The
item names the recorded names the profile does not list (`missing`), those it lists that the
recording lacks (`added`), and `missingShare`. Only names are compared: Oh My Pi lists under each
name a model's hint for the skill or a preview of its description (system-prompt.ts, lines
981-983; extensibility/skill-descriptions.ts, lines 448-476), never the frontmatter's text. The
summaries of `build-cases`, `build-cases --reprofile`, `build-tasks --profiles-out` and
`build-tasks --reprofile` count the measures of each kind and sum the missing and added names
(`listing`); a task that keeps its old profile is `not measurable`, with why. Nothing is dropped
by the measure: a report applies its rules.

`--reprofile` builds the profiles of an existing case file again that way, from the cases and
their sessions' heads (no transcript pulled, no reader call), and writes a new file in which only
`profile` and the profile's part of `evidence` differ: each case's `source`, its `kind` among it,
stays. A written case's profile is built again from the reads of its context session its evidence
names (`context session read: …`), which its messages do not hold, and its written case's
evidence stays. A case holds no working directory, so each
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

`build-cases --written <n>` writes up to n written cases of one skill, for a skill whose session
cases give fewer than 6 informative cases in the screen:

```
bun scripts/skill-cuts/cli.ts build-cases --config <skill-cuts.json> --key-file <file> \
  (--cut <id> | --set <id>) --skill <name> --old <skill dir> --copies <copies out dir> \
  --count <count dir> --written <n> \
  (--context-session <store>:<hash> | --sessions <sessions.jsonl> --seed <n>) --out <cases.jsonl> \
  [--session-cases <cases.jsonl>] [--reading-models <file>] [--model-pool <file>]... \
  [--budget-usd <n>]
```

A written case is a written task in a recorded session's context, not that session run again.
The context session is the one `--context-session` names (`<store>:<file hash>`, the hash the
sessions file names a file by), else the first of `--sessions` in the order `--seed` gives that is
on a model the gateway serves and whose rebuilt profile installs the cut repository's (or the
set's) copy of the skill; when no such session is on a served model, the first whose profile
installs it, on any model. A session gives a context as a session gives a case: its first
successful read of the skill must be placed at the cut repository's copy by `attribute.ts`'s
rules, the model and thinking level are those in effect at that read, its context (`devbox`,
its Legion role, or a subagent's recorded prompt and tools) is rebuilt as build-cases rebuilds a
case's, and its profile is built at its start from its reads up to and including that read, as a
session case's is from the reads before its step. The summary's `contextSession` names it, how it
was chosen, and each session passed over before it with why (a drop reason and its detail, or
`model-not-served` for a session on a model the gateway does not serve). A case's `model` is the
context session's when the gateway serves it; else the served model with the most reading
sessions of the skill in `--reading-models` (the skill's `<cut or set id>/models/<skill>.json`);
else the served model with the most reading sessions summed over the `--model-pool` files (each a
models file of a skill of the set: give the flag once per file); with none, the build fails
(exit 1). The `written-case` reader (`prompts/written-case.md`) is called once: it reads the base
text (`--old`, each line numbered), each session case of `--session-cases` (its real step and its
criterion; none when the flag is not given) and the context session's first user message, and
writes at most n tasks in which a line of the skill decides the agent's first step after it reads
the skill, each with the task (the user's message), the line (file, number and text; its schema
refuses one that is not that line of the base text), the criterion, a following step and a
violating step. Each task gives a case whose `messages` are the task as the user's message, an
assistant call `read skill://<skill>` and its result: the base text's `SKILL.md`, answered as the
arms answer a history read, at the path the context session's first read of the skill showed
(`skillReads` points at it). Its `realStep` is the following step, its `violatingStep` the
violating one, its `source` the context session's store and path hash with `kind` `written`, and
its profile, working directory and system context the context session's; its evidence names each
read of the context session its profile is built from (`context session read: …`), the task, the
context session and the model. Secrets are taken out as from a session case. The `restate` reader
reads the task and the read's call (never the skill's text): a restatement of the line drops the
task (`written-restates`). The `audit` reader reads the line, the messages as the judge reads
them and the following step: anything but `follows` drops the task (`written-not-followed`). The
screen's three judge controls run on a written case as on a session case. The summary
(`<out>.summary.json`) records the inputs, the context session, the model, each task's outcome
(its case, or its drop reason and detail, with its restatement check and audit), the drops by
reason and the readers' spend. The build does not go on from an earlier run: an `--out` that is
there is refused (exit 2). `--budget-usd` stops it once its readers cost that much (exit 3), with
the cases built so far written. Put the written cases after the skill's session cases in one file
for `behaviour` (`cat cases.jsonl written.jsonl`), which screens the session cases first.

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

### `results-index`

```
bun scripts/skill-cuts/cli.ts results-index --count <set-count out dir> \
  --nearest <dir of nearest rows> --cases <dir of case files> --results <dir of results>
```

The inventory of the results of the skills of the two sets (`results-index.ts`). It lists each
skill that `set-count`'s `summary.json` (in `--count`) gives at 20 or more own-copy sessions
(`atLeast20`), in its order, beside its nearest row (a `nearest` row JSON file at any depth under
`--nearest` of the skill's name; a row whose every reply was refused names no nearest skill, so
it is none), its cases file (`cases-<skill>.jsonl` or `cases-<skill>.jsonl.gz` in `--cases`) and
its result (`results-<skill>/` or `results-<skill>.tar.gz` in `--results`, holding the output of
`behaviour report` for the skill as `report.txt`, whose `verdict:` line it prints; a result with
no report, or a report with no verdict line, is none). It calls no model and reads nothing else.
It exits 1 naming each skill that lacks a nearest row, a cases file or a result, and 2 when it
refuses its arguments, a `summary.json` that is not set-count's, a directory it cannot read, or a
`.json` file under `--nearest` that is no JSON or a nearest row of another shape.

## The request log

Each model request of `behaviour`, `read`, `control`, `craft`, `nearest`, `proposals` and `load`
adds one JSON line to the request log (`<out>/requests.jsonl` for `behaviour`, `load`,
`control craft` and `proposals`, `<out file>.requests.jsonl` otherwise):
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
call, a load test's task or an `--after-case` command that failed, an item line of `proposals` that
does not parse, an item it leaves with no entry, or a skill `results-index` finds with no nearest
row, cases file or result); 2 refused before any model
call or store read (among the refusals, an `--out` inside a git or jj checkout, from `behaviour`'s
stage commands, `load` and `build-cases`); 3 stopped (the budget is spent, or the peak memory
passed 1 GiB, 1.5 GiB for `copies`); 4 stopped by build-cases' stop rule.
