# skill-cuts

The runner that tests whether a cut of a skill's text keeps what agents do with it. Run every command
from `packages/pi-legion` as `bun scripts/skill-cuts/cli.ts <command> ...`.

Every private value (the owners, the session stores, the gateway's address, the paths left out, the
repositories copies map to) comes from the `--config <skill-cuts.json>` file at run time
(`config.ts` says each key). No file here names one. The behaviour test needs no GitHub token: it
reads the skill texts from two directories, the cases from a file outside the repository, the
repositories a case's context needs from local clones (`--repos`), and the gateway key from
`--key-file`, inside the process that uses it, never printed, logged or written. `copies`,
`build-cases` and `build-tasks` only read GitHub, through `gh`: GET calls, and, in `copies` and
`build-cases`, GraphQL queries that read file contents in batches.

## Commands

### `behaviour screen|confirm|control|report`

```
bun scripts/skill-cuts/cli.ts behaviour <screen|confirm|control|report> \
  --config <skill-cuts.json> --key-file <file> --out <dir> \
  --old <skill dir at the cut's base> --new <skill dir in the cut> --cases <case file> \
  [--planted-line <text>] [--budget-usd <n>] [--concurrency <n, default 8>] \
  [--after-case <command>] [--request-log <file>] \
  [--models <models.yml>] [--omp <binary>] [--repos <dir>] [--hint-model <model>] \
  [--omp-config <file>]... [--cache-dir <dir>] [--scratch <dir>]
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
  gateway prefix runs as `anthropic/<id>`.
- `confirm`: 40 fresh runs of old and new for each flagged case.
- `control`: 40 runs of `minus` (old without the case's line, or without `--planted-line`) and 40
  fresh runs of old, on the skill's control case.
- `report`: each case's passes per arm and stage, the skill's verdict and its reason (`keeps`
  prints as "no loss detected by the registered rule"), the drops per judge control, an estimate
  of the procedure's false-loss chance at the cases run (exact binomial under identical old and
  new texts, the pass rates taken from each case's own screen; its assumptions are printed with
  it), a table of the chance it finds a loss of 10 to 40 points in one informative case, the spend
  at list price, the peak memory, and how many captures' system-prompt digests differ from their
  session's. It calls no model, and needs only `--config`, `--out`, `--old` and `--cases`.

`score.ts` holds the rules. Each case's results are one file, `<out>/cases/<case id>.json`,
written whole after each round of runs, so a run started again goes on from what was saved and
skips each complete case. `--after-case <command>` runs `<command> <that file>` when a case's stage
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

### `read`

```
bun scripts/skill-cuts/cli.ts read --config <skill-cuts.json> --key-file <file> \
  --prompt <case|map|attack|same-job|sentence|names|tasks> --in <input.json> --out <answer.json> \
  [--request-log <file>] [--budget-usd <n>]
```

Runs one reader (`readers.ts`, `prompts/<name>.md`) on one input, a JSON value of that reader's
input schema, and writes `{prompt, settings, answer, refusals, spent, usd}`.

### `control map|attack|same-job|sentence`

```
bun scripts/skill-cuts/cli.ts control <kind> --config <skill-cuts.json> --key-file <file> \
  --in <reader input.json> --out <result.json> [--request-log <file>] [--budget-usd <n>] \
  map:      --delete-line <a line of the cut text that the base text holds too>
  attack:   [--plant-line <line>]          (default: "Run the tests where needed.")
  same-job: --planted <skill entry.json> --copy-of <id>
  sentence: --into <id> --sentence <text>
```

Plants a known finding in a copy of the reader's input, runs the reader once, and writes whether
its answer names that finding. It exits 0 when the control held, 1 when it did not.

### `count-sql`

```
bun scripts/skill-cuts/cli.ts count-sql --config <skill-cuts.json> --store <db> --day <YYYY-MM-DD> [--hour <0-23>] [--query count|models]
```

Prints `count.sql` filled for one store and one UTC day (or hour), the same as
`bun scripts/skill-cuts/countsql.ts`. `--query models` prints `models.sql` instead: each session
file's model, read from the first 256 KiB of the file, as one row per file hash.

### `load`

```
bun scripts/skill-cuts/cli.ts load --config <skill-cuts.json> --key-file <file> \
  --skill <skill dir> [--name <name>] --tasks <tasks.jsonl> --host <omp|claude> \
  --profile <profile.json> --out <dir> [--runs <n, default 3>] \
  [--model <model, default anthropic/claude-sonnet-5-5>] \
  [--thinking <level, default off on omp, auto (Claude Code's own) on claude>] \
  [--budget-usd <n>] [--concurrency <n>] [--request-log <file>] \
  [--repos <dir>] [--cache-dir <dir>] [--scratch <dir>] \
  [--models <models.yml>] [--omp <binary>] [--hint-model <model>] [--omp-config <file>]... \
  [--claude <binary>]
```

The load test of R6 and R7 (`load.ts`): whether the host's agent loads the skill for each task,
given what the host lists for it. `--tasks` holds one task per line, `{id, text, label, source}`
with `label` `need` or `near-miss` and `source` `written`, or `session` with its `store` and
`pathHash`. `--profile` is the other skills installed beside it (the `Profile` of `cases.ts`, a
`context`, and for Claude Code a `claude` plugin layout: `{marketplace, plugins: [{name, skills}],
skillPlugin}`). `--name` tests the skill under another name: its frontmatter `name` and its
directory's name change in a scratch copy, and nothing else that names the skill.

Per task, the host's first request is captured once against a recorder (Oh My Pi through
`capture.ts`, which lists the skill under the hint Oh My Pi has a model write; Claude Code with a
scratch `CLAUDE_CONFIG_DIR` that installs the skills as local plugins), then sent `--runs` times.
An `openai/<id>` model runs on Oh My Pi only, on the Responses route, with an Anthropic
`--hint-model`. A run loads the skill when the first assistant message reads it (Oh My Pi: `read`
of `skill://<name>` or its `SKILL.md`, a `tool_use` block or a Responses `function_call` item;
Claude Code: `Skill` naming `<name>` or `<plugin>:<name>`, or `Read` of its `SKILL.md`), and never
goes past that message. It prints each task's loads and verdict and the skill's verdict
(`score.ts`), and writes `<out>/runs/<task>.jsonl` (one line per run), `<out>/captures/` and
`<out>/summary.json`. A task whose run file is complete is skipped, so a run started again goes on
from what was saved.

### `copies`

```
bun scripts/skill-cuts/cli.ts copies --config <skill-cuts.json> --at <ISO instant> --out <dir> \
  [--only <owner/repo>]... [--max-search-calls <n>]
```

The list of copies and loaders (`copies.ts`): every repository of the configured owners at its
default branch's commit at `--at`, each `SKILL.md` outside the configured `excludePaths`, the
names in two or more repositories, the manifests and installers that install each skill, the
files outside a skill that name one (found by code search, paced under its limit), and the
copy-prefix table the read count is attributed by. Its files are listed in `copies.ts`'s header.

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
copy's reading sessions by model.

### `build-cases` and `build-tasks`

```
bun scripts/skill-cuts/cli.ts build-cases --config <skill-cuts.json> --key-file <file> --cut <id> \
  --skill <name> --old <skill dir at the cut's base> --sessions <sessions.jsonl> --seed <n> \
  --copies <copies out dir> --count <count dir> --out <cases.jsonl> [--max <n>] [--budget-usd <n>]
bun scripts/skill-cuts/cli.ts build-cases --config <skill-cuts.json> --old <skill dir at the cut's base> \
  --copies <copies out dir> --count <count dir> --reprofile <cases.jsonl> --out <new cases.jsonl>
bun scripts/skill-cuts/cli.ts build-tasks --config <skill-cuts.json> --key-file <file> \
  --skill <skill dir> --store <db>... --out <tasks.jsonl>
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
evidence says which skills the later copies output lists that the unit did not ship then.
`--reprofile` builds the profiles of an existing case file again that way, from the cases alone
(no session read, no reader call), and writes a new file in which only `profile` and the
profile's part of `evidence` differ. A Legion session's
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
list price (exit 3). The case file holds real session text, so it stays outside the repository.

## The request log

Each model request of `behaviour`, `read`, `control` and `load` adds one JSON line to the request
log (`<out>/requests.jsonl` for `behaviour` and `load`, `<out file>.requests.jsonl` otherwise):
what the call was for, the model, the thinking type and budget, `max_tokens` (a Responses body's
`max_output_tokens`), the temperature, the effort (a Responses body's `reasoning.effort`), and the
reply's usage and dollars at list price. It never holds the key or a message's text.

## Exit status

0 done (a control held); 1 failed (every reply of a reader refused, a control that did not hold, a
call, a load test's task or an `--after-case` command that failed); 2 refused before any model
call; 3 stopped (the budget is spent, or the peak memory passed 1 GiB).
