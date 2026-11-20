# skill-cuts

The runner that tests whether a cut of a skill's text keeps what agents do with it. Run every command
from `packages/pi-legion` as `bun scripts/skill-cuts/cli.ts <command> ...`.

Every private value (the owners, the session stores, the gateway's address, the paths left out, the
repositories copies map to) comes from the `--config <skill-cuts.json>` file at run time
(`config.ts` says each key). No file here names one. The behaviour test needs no GitHub token: it
reads the skill texts from two directories, the cases from a file outside the repository, the
repositories a case's context needs from local clones (`--repos`), and the gateway key from
`--key-file`, inside the process that uses it, never printed, logged or written. `copies`,
`build-cases` and `build-tasks` read GitHub through `gh`, with GET calls only.

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

- `screen`: per case, the two judge controls (the real step must pass, "I will continue." must
  fail; a case that fails either is dropped), then a capture of each arm (old, new, none), 10
  replays of each, each reply judged blind with all arms shuffled, and runs made again for the runs
  with no verdict, at most 5 more per arm. It stops at 6 informative or 15 screened cases.
- `confirm`: 40 fresh runs of old and new for each flagged case.
- `control`: 40 runs of `minus` (old without the case's line, or without `--planted-line`) and 40
  fresh runs of old, on the skill's control case.
- `report`: each case's passes per arm and stage, the skill's verdict and its reason, the
  false-loss rate at the cases run, the spend at list price, the peak memory, and how many
  captures' system-prompt digests differ from their session's. It calls no model, and needs only
  `--config`, `--out`, `--old` and `--cases`.

`score.ts` holds the rules. Each case's results are one file, `<out>/cases/<case id>.json`,
written whole after each round of runs, so a run started again goes on from what was saved and
skips each complete case. `--after-case <command>` runs `<command> <that file>` when a case's stage
ends. Captures are kept under `<out>/captures/`; a capture that is not there yet is made with Oh My
Pi (`--omp`) and needs `--models`. The run stops above 1 GiB of peak resident memory, and no new
model call starts once the replies cost `--budget-usd` at list price.

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
bun scripts/skill-cuts/cli.ts count-sql --config <skill-cuts.json> --store <db> --day <YYYY-MM-DD> [--hour <0-23>]
```

Prints `count.sql` filled for one store and one UTC day (or hour), the same as
`bun scripts/skill-cuts/countsql.ts`.

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
A run loads the skill when the first assistant message reads it (Oh My Pi: `read` of
`skill://<name>` or its `SKILL.md`; Claude Code: `Skill` naming `<name>` or `<plugin>:<name>`, or
`Read` of its `SKILL.md`), and never goes past that message. It prints each task's loads and
verdict and the skill's verdict (`score.ts`), and writes `<out>/runs/<task>.jsonl` (one line per
run), `<out>/captures/` and `<out>/summary.json`. A task whose run file is complete is skipped, so
a run started again goes on from what was saved.

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
  --copies <copies out dir> --spans <spans.jsonl> --out <dir> [--changed <cut id>=<file>]...
```

The read count per skill copy (`attribute.ts`): it maps each read's copy path to a repository
copy by the prefix table, counts each session once across the count's queries, marks ties and
loaded skills, states each store's span, and ranks each cut's changed skills for the behaviour
test.

### `build-cases` and `build-tasks`

```
bun scripts/skill-cuts/cli.ts build-cases --config <skill-cuts.json> --key-file <file> --cut <id> \
  --skill <name> --old <skill dir at the cut's base> --sessions <sessions.jsonl> --seed <n> \
  --copies <copies out dir> --out <cases.jsonl> [--max <n>]
bun scripts/skill-cuts/cli.ts build-tasks --config <skill-cuts.json> --key-file <file> \
  --skill <skill dir> --store <db>... --out <tasks.jsonl>
```

`build.ts`: `build-cases` pulls each candidate session through the config's `psqlCommand`, has
the `case` reader propose the step the skill governs, and writes the cases `loadCases` reads,
each with the evidence for its rebuilt context; `build-tasks` writes the need and near-miss tasks
of a load test from the first requests of real sessions. The case file holds real session text,
so it stays outside the repository.

## The request log

Each model request of `behaviour`, `read`, `control` and `load` adds one JSON line to the request
log (`<out>/requests.jsonl` for `behaviour` and `load`, `<out file>.requests.jsonl` otherwise):
what the call was for, the model, the thinking type and budget, `max_tokens`, the temperature, the
effort, and the reply's usage and dollars at list price. It never holds the key or a message's
text.

## Exit status

0 done (a control held); 1 failed (every reply of a reader refused, a control that did not hold, a
call, a load test's task or an `--after-case` command that failed); 2 refused before any model
call; 3 stopped (the budget is spent, or the peak memory passed 1 GiB).
