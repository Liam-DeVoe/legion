/**
 * The skill-cuts runner's command line (README.md, "Commands"). Every command that calls a model
 * takes `--config <skill-cuts.json>` (config.ts: the gateway's address), `--key-file <file>` (the
 * gateway key, read inside gateway.ts and never printed, logged or written), and `--out` (where
 * it writes; `behaviour`'s stage commands, `load` and `build-cases` write session text there, so
 * they refuse an `--out` inside a git or jj checkout, exit 2).
 * `behaviour` and `load` read skill texts from directories, cases or tasks from a file, and the
 * key from a file; the repositories a context needs come from `--repos <dir>` (a tree of
 * `<owner>/<name>/<commit>/` directories, context.ts's `localSource`), or else from GitHub
 * through `gh` (context.ts's `ghSource`). So they read GitHub, with its token, only to make a
 * capture with no `--repos`: a command whose captures are all saved reads no repository.
 *
 *   bun scripts/skill-cuts/cli.ts behaviour <screen|confirm|control|report> --config <file>
 *       --key-file <file> --out <dir> --old <dir> [--new <dir> | --ours <dir>] --cases <file>
 *       [--case <id>] [--planted-line <text>] [--budget-usd <n>] [--concurrency <n>]
 *       [--after-case <command>] [--models <models.yml>] [--omp <binary>] [--repos <dir>]
 *       [--hint-model <model>] [--omp-config <file>]... [--cache-dir <dir>] [--scratch <dir>]
 *       [--request-log <file>] [--reading-models <file>] [--limits <file>]
 *       [--again <case id>]... (confirm only)
 *   bun scripts/skill-cuts/cli.ts read --config <file> --key-file <file> --prompt <name>
 *       --in <input.json> --out <answer.json> [--request-log <file>] [--budget-usd <n>]
 *       [--concurrency <n>]
 *   bun scripts/skill-cuts/cli.ts control <map|attack|same-job|sentence> --config <file>
 *       --key-file <file> --in <input.json> --out <result.json> [what to plant, below]
 *       [--request-log <file>] [--budget-usd <n>] [--concurrency <n>]
 *   bun scripts/skill-cuts/cli.ts control craft (--remove-section <heading> | --join-lines <a>-<b>)
 *       --copy-line <a>[-<b>] [--copy-kind <present|missing>]... [--copy-rule <id>]...
 *       [--copy-claim <text>] --original-line <a>[-<b>|all] [--original-kind <k>]...
 *       [--original-rule <id>]... [--original-claim <text>] <craft's flags> --out <dir>
 *   bun scripts/skill-cuts/cli.ts craft-lines --list <craft-list.json>
 *   bun scripts/skill-cuts/cli.ts craft --config <file> --key-file <file> --skill <dir>
 *       --style <file> --style-source <text> --craft <craft-list.json> --craft-source <text>
 *       [--nearest <dir>]... [--skill-source <text>] --out <entry.json> [--request-log <file>]
 *       [--budget-usd <n>] [--concurrency <n>]
 *   bun scripts/skill-cuts/cli.ts craft-merge --entry <entry.json> --entry <entry.json>
 *       [--entry <entry.json>]... --out <merged.json>
 *   bun scripts/skill-cuts/cli.ts nearest --config <file> --key-file <file> --skill <dir>
 *       [--skill-source <text>] --candidates <source>=<root>... --out <row.json>
 *       [--request-log <file>] [--budget-usd <n>] [--concurrency <n>]
 *   bun scripts/skill-cuts/cli.ts craft-coverage --root <dir> [--source <text>]
 *       [--skip <name>]... --entries <reader-findings.jsonl>
 *   bun scripts/skill-cuts/cli.ts proposals --config <file> --key-file <file> --body <file>
 *       --body-source <text> --section <heading> --list <text>... --pr-diff <prefix>=<file>...
 *       --skills-root <repo>=<dir>... --repo-root <repo>=<dir> --style <file>
 *       --style-source <text> --craft <craft-list.json> --craft-source <text>
 *       --findings <reader-findings.jsonl> --out <dir> [--request-log <file>] [--budget-usd <n>]
 *       [--concurrency <n>]
 *   bun scripts/skill-cuts/cli.ts proposals --dry-run --body <file> --section <heading>
 *       --list <text>...
 *   bun scripts/skill-cuts/cli.ts count-sql --config <file> --store <db> --day <YYYY-MM-DD>
 *       [--hour <0-23>] [--query count|models]
 *   bun scripts/skill-cuts/cli.ts load --config <file> --key-file <file> --skill <dir>
 *       [--name <name>] --tasks <file> --host <omp|claude> [--profile <file>] --out <dir>
 *       [--runs <n>] [--model <model>] [--thinking <level>] [--budget-usd <n>]
 *       [--concurrency <n>] [--request-log <file>] [--repos <dir>] [--cache-dir <dir>]
 *       [--scratch <dir>] [--models <models.yml>] [--omp <binary>] [--hint-model <model>]
 *       [--omp-config <file>]... [--claude <binary>] [--context-session <store>:<path hash>]
 *   bun scripts/skill-cuts/cli.ts copies --config <file> --at <ISO instant> --out <dir>
 *       [--only <owner/repo>]... [--max-search-calls <n>] [--cache-dir <dir>]
 *   bun scripts/skill-cuts/cli.ts attribute --config <file> --count <dir> --copies <dir>
 *       --spans <file> --out <dir> [--changed <cut or set id>=<file>]... [--models <dir>]
 *   bun scripts/skill-cuts/cli.ts set-count --config <file> --rows <dir> --spans <file>
 *       --out <dir> [--dotfiles-commit <sha>] [--rows-label <text>] [--not-read <name>=<reason>]...
 *   bun scripts/skill-cuts/cli.ts build-cases --config <file> --key-file <file>
 *       (--cut <id> | --set <id>) --skill <name> --old <dir> --sessions <file> --seed <n>
 *       --copies <dir> --count <dir> --out <cases.jsonl> [--max <n>] [--budget-usd <n>]
 *       [--concurrency <n>] [--stop-projected-usd <n>] [--stop-after-sessions <n>]
 *       [--stop-zero-case-usd <n>]
 *   bun scripts/skill-cuts/cli.ts build-cases --config <file> --key-file <file>
 *       (--cut <id> | --set <id>) --skill <name> --old <dir> --copies <dir> --count <dir>
 *       --written <n> (--context-session <store>:<hash> | --sessions <file> --seed <n>)
 *       --out <cases.jsonl> [--session-cases <cases.jsonl>] [--reading-models <file>]
 *       [--model-pool <file>]... [--budget-usd <n>]
 *   bun scripts/skill-cuts/cli.ts build-cases --config <file> --old <dir> --copies <dir>
 *       --count <dir> --reprofile <cases.jsonl> --out <new cases.jsonl>
 *   bun scripts/skill-cuts/cli.ts build-tasks --config <file> --key-file <file> --skill <dir>
 *       --store <db>... --out <tasks.jsonl> [--model <model>] [--profiles-out <dir>
 *       --copies <dir> --count <dir> --context-session <store>:<hash>] [--max-candidates <n>]
 *       [--max-search-minutes <m>]
 *   bun scripts/skill-cuts/cli.ts build-tasks --config <file> --copies <dir> --count <dir>
 *       --reprofile <tasks.jsonl> --profiles-out <new dir> [--context-session <store>:<hash>]
 *   bun scripts/skill-cuts/cli.ts build-tasks --first-read --config <file> --key-file <file>
 *       --skill <dir> --sessions <file> --seed <n> [--max <n>] --out <tasks.jsonl>
 *       [--model <model>] [--profiles-out <dir> --copies <dir> --count <dir>]
 *   bun scripts/skill-cuts/cli.ts results-index --count <set-count out dir> --nearest <dir>
 *       --cases <dir> --results <dir>
 *
 * `count-sql`, `copies`, `attribute`, `set-count`, `craft-lines`, `craft-merge`, `build-cases`,
 * `build-tasks` and `results-index` are the commands of countsql.ts, copies.ts, attribute.ts,
 * set-count.ts, craftlist.ts, craftmerge.ts, build.ts and results-index.ts, given their arguments
 * whole.
 *
 * **`behaviour`** runs the behaviour test of one skill: `--old` is the skill's own directory (the
 * old text: the skill at the cut's base, or a set's skill), and `--cases` a case file of that
 * skill (cases.ts, checked against `--old`). The old text is compared with one arm (score.ts's
 * Compared): `--new`, the skill's directory in the cut; `--ours`, a skill of ours whose name may
 * differ (arms.ts's `ours`); or, with neither, no skill. `--new` and `--ours` exclude each other.
 * Each stage records its
 * compared arm, and an `--out` whose results record another than the command's is refused.
 * Each case's results are one file, `<out>/cases/<id>.json` (CaseResult),
 * written whole (a fresh file renamed over it) after each round of runs, so a run started again
 * goes on from what was saved and skips each case whose stage is complete. `--after-case
 * <command>` runs `<command> <that file>` through `sh` when a case's stage ends. In a run with an
 * arm of ours, every judgement reads the case's skill name and ours as `the-skill` (blind.ts);
 * with `--new` or no skill compared, the judge reads the case's text as it is.
 *
 * - `screen`: the session cases first, in the file's order, then the written ones (cases.ts's
 *   `caseKind`; score.ts's `screenOrder`), at most MAX_SCREENED_CASES screened cases of each kind,
 *   until the skill has MIN_INFORMATIVE_CASES informative cases in all (`screeningDone`): a case
 *   whose model the gateway does not serve
 *   (none of the configuration's `servedModels` has its id, its text after the last `/`) is
 *   dropped as `model-not-served`, and a case on a served model runs as that served
 *   `provider/model`, on the gateway route of its capture's body (gateway.ts); a case whose
 *   listing measure misses more than MAX_LISTING_MISSING_PERCENT of its session's recorded names
 *   (score.ts's `listingStanding`) is dropped as `listing-misses`, before any call too; the three
 *   judge controls (judge.ts's CONTROLS) run, and a case that fails any is dropped, its drop
 *   naming each control it failed; then a capture
 *   per arm (old, the compared arm unless it is none, and none; capture.ts, read back when saved
 *   already), SCREEN_RUNS replays of
 *   each, every reply of the round judged blind (all arms shuffled under a recorded seed), and
 *   rounds of extra runs for the runs with no verdict, as score.ts's `stageOutcome` says, at most
 *   MAX_EXTRA_RUNS per arm. With `--reading-models <file>` (attribute.ts's
 *   `<cut or set id>/models/<skill>.json` of the skill: its reading sessions by model), a skill
 *   whose reading sessions on a model the gateway serves are fewer than MIN_READING_SESSIONS gets
 *   no session case screened (score.ts's `servedScreeningRefusal`: `too few sessions on served
 *   models`, with the served and total counts): with no written case it is refused before any
 *   model call, gets no verdict, and the walk of the cut's ranking goes on to its next skill;
 *   with written cases, they are screened, and the refusal is never its reason for no verdict. A
 *   session case whose screen began before the refusal applied is passed over too: its runs count
 *   for nothing, and confirm, control and report take the screen as finished without it.
 * - `confirm`: FRESH_RUNS fresh runs of old and the compared arm for each flagged case that is not
 *   dropped. A confirm records the runner that made it (`runnerDigest`). Where a case's last two
 *   confirms at the same texts on one runner disagree, one a loss and one not, `confirm` runs a
 *   third, and score.ts pools the three (`confirmScore`). `--again <case id>` asks for one more
 *   confirm of a flagged case whose confirm is finished; it is refused (exit 2), naming the case,
 *   where the case already has POOLED_CONFIRMS confirms at these texts on this runner (no fourth
 *   runs), or two that agree (a third runs only where two disagree). With `--case <id>`,
 *   FRESH_RUNS fresh runs of old and the compared arm on that case alone, with no flag gate and no
 *   gate of MIN_INFORMATIVE_CASES informative cases, into its own `--out`; an id the case file
 *   does not hold, a case whose model the gateway does not serve, and `--again` beside it are
 *   refused.
 * - `control`: on the skill's control case, FRESH_RUNS runs each of old, `minus` (old less every
 *   line of the case's file that is the case's line, trimmed, or less every line that is
 *   `--planted-line`: a rule written twice is still the rule, so each copy goes; arms.ts) and
 *   `twin` (old again, its own capture: the false-loss control, which must be no loss). Old
 *   against `minus` is the planted-loss control, which must be a loss.
 *   confirm and control need the screen finished, and make no run when the skill can get no verdict
 *   (fewer than MIN_INFORMATIVE_CASES informative cases, a planted-loss control that is no loss,
 *   or a false-loss control that is a loss).
 *   In any stage, a case one of whose captures needs a directory no read can give (context.ts's
 *   UnreadableDirectory: the commit holds no such directory, or GitHub cuts a level of its tree
 *   short even read alone) is dropped as `unreadable-directory`, its drop naming the repository,
 *   the commit and the path, and a run started again skips it. Each drop records its stage
 *   (`droppedAt`). A case dropped at screen is never informative; one dropped at confirm or
 *   control keeps its screen's passes in the score, so a flagged one that cannot be confirmed
 *   gives its skill no verdict unless another case is a confirmed loss, and a dropped control
 *   case gives its place to the next case of the control rule's order (score.ts's `scoreSkill`).
 *   A read that failed for another reason, after its retries, fails the command, and
 *   the next run reads it again. A case that holds a block of its skill its arms cannot each give
 *   their own text is dropped at screen, before any model call, as `injection-unrebuildable`, its
 *   reason naming each fault's kind (arms.ts's `injectionFaults`: `user-invoked`, a `/skill:`
 *   prompt the user typed; `other-shape`, a block with arguments or in another form;
 *   `plugin-block-misplaced`, a dispatch-first block away from where the plugin puts it), and the
 *   rest of the file runs; so is, at the stage whose capture finds it, a case whose subagent's
 *   agent autoloads the skill and whose messages hold no block of it (capture.ts's
 *   InjectionRefused, `autoload-no-block`).
 * - `report` (no model call) prints the compared arm (no skill informs and clears nothing; ours is
 *   a bundle where it replaced a skill of our name that a profile held), each case's passes per
 *   arm and stage (and the history reads the ours arm removed), and for a dropped case the stage
 *   that dropped it, why, and the passes of each stage it ran; each confirm of a case with more
 *   than one, and the pooled passes of a case whose three confirms are pooled; the cases of each
 *   kind and the informative cases of each; the drops by each judge control, by
 *   `model-not-served`, by `unreadable-directory`, by `listing-misses` and by
 *   `injection-unrebuildable` (with the cases of each of its kinds); the control case, and each
 *   dropped case it replaced; with ours compared, ours against old over all screened cases and
 *   over those with no removed read; each model of the cases; the skill's verdict and its reason
 *   (score.ts; `keeps` is printed "no loss detected by the registered rule", or "no loss of no
 *   skill …" with no skill compared), with how many of its screened cases have a listing that
 *   could not be measured and how many rest on the assumption that their session had Dispatch;
 *   the rule's sensitivity (`lossChance` at an old pass rate of 0.9 and 0.7 and drops of 0.1 to
 *   0.4), each screened case's smallest drop and their medians (score.ts's `dropFigures`), the
 *   estimated false-loss rate at the cases run (`falseLossChance`), the spend, the peak memory,
 *   how many captures' system-prompt digests differ from their session's, how many cases carry
 *   the Envoy plugin's block of the skill, by where the answer that their session had Dispatch
 *   comes from (arms.ts's `dispatchOf`: a Legion role, the transcript, or the assumption; each
 *   capture that carries the block records it in its stage's `captures`), and the `--limits` text
 *   as it is. Beside the verdict, as descriptions: each kind's screened cases with each arm's
 *   screen passes and the informative ones, the mean of old's screen pass rate less the compared
 *   arm's over the screened cases, all and each kind's, with its 95% t interval over the cases
 *   (score.ts's `meanDifference`), the screened cases that tie, floor, ceil and reverse
 *   (`screenShapes`), and the judge's identity (judge.ts's `judgeIdentity`) with how many stages
 *   record it. Its compared arm is the one its results record. With `--reading-models`, it prints
 *   each model of the file with its sessions and
 *   whether the gateway serves it (the file's `served`), the sessions with no model and not
 *   queried, and the served models the screened session cases ran on, each with its cases; a
 *   verdict's line then ends with the reading sessions it speaks for, on each served model its
 *   screened session cases ran on (a model of the file counted under the served model of its id),
 *   and the ones it does not: on models the gateway does not serve, and on served models no
 *   screened session case ran on. A written case is a written task, never a reading session: the
 *   screened written cases are counted by model on a line of their own (`writtenTasksLine`), and a
 *   skill whose screened cases are all written speaks for no reading sessions. A skill the
 *   screen's refusal holds for gets no verdict. Without it, `report` says the
 *   reading sessions by model were not given, and its verdict names no models. A drop at confirm
 *   of a case its screen did not flag cannot happen, since only flagged cases are confirmed: a
 *   result that shows one fails `report`, and every stage command, naming the case (exit 1).
 *
 * Every round of a stage sends the runs of all its arms in one order, seeded by the round's
 * recorded seed (replay.ts's `replayInterleaved`), each run with a session id of its own, and its
 * runs record that id and their place in the order. A capture made for a stage shows the case's
 * session (capture.ts, "The harness strings"), whose head the capture reads once from its store
 * through the configuration's `psqlCommand` and saves under `<out>/sessions/`.
 *
 * Captures are saved under `<out>/captures/<arm>-<digest of the arm's skill directory>/`, so a new
 * text, or another planted line, is captured again. Making one runs Oh My Pi (`--omp`, else
 * `$LEGION_OMP_PATH`, else the `omp` on PATH, as an absolute path: `ompBinary`; a command given
 * `--models` when none of the three names one is refused) with `--models` (a `models.yml` whose
 * providers the capture points at its recorder), the context's repositories read with `gh` or
 * from `--repos <dir>` (context.ts's `localSource`), and the caches under `--cache-dir`; without
 * `--models` a missing capture fails the run. The peak resident memory (`VmHWM`, or the largest
 * `VmRSS` read each second on a kernel that keeps no `VmHWM`, as gVisor) is recorded at the end
 * of each case, and the run stops above 1 GiB.
 * `--budget-usd` stops new model calls once the replies' dollars at list price reach it; the runs
 * of the round so far are saved.
 *
 * **`read`** runs one reader of readers.ts (`runReader`) on the input `--in` holds, one JSON
 * value of
 * that reader's input schema, and writes `--out` whole: `{prompt, settings, answer, refusals,
 * spent, usd}`, `answer` null when every reply was refused.
 *
 * **`control`** runs one positive control of readers.ts (`runControl`) on the reader's input `--in`
 * holds, with what it plants: `map --delete-line <a line of the cut text the base text holds too>`,
 * `attack [--plant-line <line>]` (readers.ts's ATTACK_PLANTED_LINE by default), `same-job --planted
 * <file holding one skill entry {id, repo, name, description}> --copy-of <id>`, `sentence --into
 * <id> --sentence <text>`. It writes `--out` whole: `{control, held, why, answer, refusals, spent,
 * usd}`.
 *
 * **`craft`** runs the craft reader once (a fresh request, READER_TRIES tries) on one skill of
 * ours: every `.md` file of `--skill`, the style text's rules with their ids (style.ts), the craft
 * list's reader properties (craftlist.ts), and every `.md` file of each `--nearest` skill of
 * theirs; it reads nothing else. It writes the entry to `--out` (`craftRead`) and the exact user
 * message beside it, `<out less .json>.input.md`.
 *
 * **`nearest`** runs the nearest reader once on one skill (its name, whole description and
 * SKILL.md body) and every `<root>/<name>/SKILL.md` of each `--candidates <source>=<root>`, and
 * writes the row: the skill, the answer, and each named candidate with its whole description and
 * source.
 *
 * **`craft-coverage`** (no model call) lists each `<name>/SKILL.md` under `--root` less the
 * `--skip` names beside its craft entry in `--entries`, or none, and exits 1 naming each skill
 * with no entry.
 *
 * **`proposals`** reads each item of the `--list` lists of the `--section` section of a pull
 * request's body with the proposal-target and proposal readers (proposals.ts): one entry per item,
 * `<out>/<id>.json`, and the user messages sent, `<out>/<id>.input.md`. An item with an entry is
 * not read again; it exits 1 naming an item line it cannot parse, and when an item has no entry
 * at the end. `--dry-run` parses the body and prints the items, and reads nothing else.
 *
 * **`control craft`** writes a copy of the directory `--skill` names (the one a link points at, so
 * the skill itself is never written) with a section removed or list items joined, runs
 * `craft` on the copy and on the unchanged skill, takes each side's candidates (gaps in its line
 * range, of its kinds and rules), asks the claim reader once a candidate where the side gives a
 * claim, and holds only when the copy has a candidate that states its claim (any, with none) and
 * the unchanged skill has none that does (none at all, with none). It writes every input and
 * answer under `--out`.
 *
 * **`count-sql`** prints count.sql (or, with `--query models`, models.sql) filled for one store
 * and day.
 *
 * **`load`** is load.ts's load test: per task, the host's first request captured once
 * and sent `--runs` times (LOAD_RUNS by default), the runs of a profile's tasks in one seeded
 * order, each with a session id of its own; each run's first assistant message read for a read of
 * the skill, the loads and verdicts printed and saved under `--out`, which a run started again
 * goes on from. The model is LOAD_MODEL and the thinking level LOAD_THINKING unless `--model` and
 * `--thinking` say otherwise. `--context-session <store>:<path hash>` names the session a written
 * task stands for in its capture.
 *
 * Each model request goes through the request log (`loggedGateway`): one JSON line per request,
 * appended to `--request-log` (by default `<out>/requests.jsonl` for `behaviour`, `load`,
 * `control craft` and `proposals`, and the `--out` file's path with `.requests.jsonl` added for
 * `read`, `control`, `craft` and `nearest`), holding what the call was for, when it was asked for
 * and sent and the calls in flight then, the session id it went with, the model, the thinking type
 * and budget, max_tokens, the temperature, the effort, and the reply's answering model, ids,
 * gateway headers, cache read and write tokens, usage and dollars at list price; never the key and
 * never a message's text.
 *
 * Exit status: 0 done (a control held); 1 failed (a reader's every reply refused, a control that
 * did not hold, a call, a load test's task or an `--after-case` command that failed, a proposals
 * item line that does not parse or an item left with no entry, a skill `craft-coverage` finds with
 * no entry, or behaviour results that show a drop no stage makes); 2 refused before any model
 * call; 3 stopped (the budget is spent, the peak memory passed 1 GiB, 1.5 GiB for `copies`, or the
 * listing rule dropped most of a load test's tasks); 4 stopped by build-cases' stop rule. The
 * commands of the other files return their own status, which `main` returns as it is.
 */
import { createHash } from "node:crypto";
import {
  appendFileSync,
  cpSync,
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  realpathSync,
  statSync,
} from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { messageFor } from "@legion/envoy-client/errors";
import { z } from "zod";
import { MessagesApiError } from "../skill-scenarios/messages-api";
import { outRefusal } from "../skill-scenarios/production-dispatch";
import {
  type Arm,
  type ArmDirs,
  type ArmName,
  buildArm,
  InjectionRefused,
  injectionFaults,
} from "./arms";
import { main as attributeMain, type ModelCount, modelCountOf, modelId } from "./attribute";
import { main as buildMain } from "./build";
import {
  type Capture,
  captureBody,
  capturePaths,
  gatewayHints,
  type SessionFacts,
  savedCapture,
  sessionFacts,
  sessionHeads,
  userTurnKey,
} from "./capture";
import { type Case, caseKind, loadCases, Thinking, textLines } from "./cases";
import {
  configFrom,
  fileDigests,
  json,
  OneLine,
  parse,
  Refused,
  required,
  Stopped,
  schemaFaults,
  sha256,
  writeWhole,
} from "./common";
import { type SkillCutsConfig, storeNamed } from "./config";
import {
  frontmatterBlock,
  ghSource,
  localSource,
  type RepoSource,
  readFrontmatter,
  UNREADABLE_DIRECTORY,
  UnreadableDirectory,
} from "./context";
import { main as copiesMain, spawnGh } from "./copies";
import { main as countSqlMain } from "./countsql";
import { main as craftLinesMain, readCraftList } from "./craftlist";
import { main as craftMergeMain } from "./craftmerge";
import {
  BudgetSpent,
  bodyIdentity,
  createGateway,
  type Gateway,
  PRICE_SOURCE,
  PRICES,
  type RequestBody,
  type Sent,
  SpentRecord,
  usdOf,
} from "./gateway";
import {
  type ArmReply,
  CONTROLS,
  ControlName,
  type Judged,
  JudgeIdentity,
  judgeBlind,
  judgeControls,
  judgeIdentity,
  newSeed,
} from "./judge";
import { Host, LOAD_MODEL, LOAD_THINKING, runLoad, skillName, skillNameOf } from "./load";
import { mib, procPeakRssBytes, RUN_MEMORY_LIMIT_BYTES, startPeakRssSampling } from "./memory";
import {
  type DiffFile,
  FindingsEntry,
  fencedLines,
  ItemFaults,
  idPrefix,
  type ProposalItem,
  type ProposalsRun,
  type PullDiff,
  parseProposalItems,
  pendingItems,
  rulesRecords,
  runProposals,
  splitDiff,
} from "./proposals";
import {
  ATTACK_PLANTED_LINE,
  type ClaimAnswer,
  ClaimInput,
  type Control,
  type CraftAnswer,
  CraftGap,
  type CraftInput,
  craftInput,
  loadPrompt,
  NEAREST_RULE,
  type NearestAnswer,
  plantControl,
  READER_NAMES,
  READERS,
  ReaderFailed,
  type ReaderName,
  runControl,
  runReader,
  SameJobInput,
  skillFiles,
} from "./readers";
import { type Replayed, replayInterleaved } from "./replay";
import { main as resultsIndexMain } from "./results-index";
import {
  type CaseFacts,
  CaseKind,
  type CaseScore,
  Compared,
  DROP_CHANCE,
  DROP_STEP_POINTS,
  dropFigures,
  FRESH_RUNS,
  isControlLoss,
  isFalseLoss,
  isFlagged,
  isInformative,
  isLoss,
  LISTING_MISSES,
  LOAD_RUNS,
  LOSS_GAP,
  listingStanding,
  lossChance,
  MAX_EXTRA_RUNS,
  MAX_SCREENED_CASES,
  type MadeConfirm,
  MIN_INFORMATIVE_CASES,
  meanDifference,
  NO_CONTROL_LOSS,
  type NoVerdict,
  noControlLoss,
  type Passes,
  POOLED_CONFIRMS,
  RunRecord,
  SCREEN_RUNS,
  type SkillScore,
  type SmallestDrop,
  STAGE_RUNS,
  Stage,
  type StageOutcome,
  StageState,
  scoreSkill,
  screenCounts,
  screeningDone,
  screenOrder,
  screenShapes,
  servedScreeningRefusal,
  skillVerdict,
  stageArms,
  stageOutcome,
} from "./score";
import { main as setCountMain } from "./set-count";
import { type StyleRule, styleRules } from "./style";

/** The longest one try of a model call may take: a reply with a long thinking budget is slow. */
const CALL_TIMEOUT_MS = 600_000;
/** The model calls in flight at once, unless `--concurrency` says otherwise. */
const DEFAULT_CONCURRENCY = 8;
/** The request log's name: `<out>/requests.jsonl` for `behaviour`, `load`, `control craft` and
 *  `proposals`, and the `--out` file's path with `.requests.jsonl` added for `read`, `control`,
 *  `craft` and `nearest`, unless `--request-log` names one. */
const REQUEST_LOG = "requests.jsonl";
/** The directories under the system's temporary directory that `--scratch` and `--cache-dir`
 *  name unless given. */
const SCRATCH_DIR = "skill-cuts-scratch";
const CACHE_DIR = "skill-cuts-cache";

/** Exit statuses. */
const EXIT = { ok: 0, failed: 1, refused: 2, stopped: 3 } as const;

/** What the commands write to, and the process facts they read. */
export interface CliIo {
  /** Writes `text` to standard output as it is. */
  out(text: string): void;
  /** Writes one line to standard error. */
  err(line: string): void;
  /** The process's peak resident memory so far, in bytes (`procPeakRssBytes`). */
  peakRssBytes(): number;
}

/** The io of a real run. Where the kernel keeps no `VmHWM`, the resident memory is read every
 *  second from now on (memory.ts), so that the peak misses little between the ends of cases. */
function processIo(): CliIo {
  startPeakRssSampling();
  return {
    out: (text) => process.stdout.write(text),
    err: (line) => process.stderr.write(`${line}\n`),
    peakRssBytes: procPeakRssBytes,
  };
}

/** The JSON value the file `file` holds. Throws a refusal naming the file. */
function readJson(file: string, what: string): unknown {
  const text = readBytes(file, what).toString("utf8");
  try {
    return JSON.parse(text);
  } catch (error) {
    throw new Refused(`${what} ${file} is not JSON: ${messageFor(error)}`);
  }
}

// The request log.

/** A field of an object, or undefined. */
function field(value: unknown, key: string): unknown {
  return typeof value === "object" && value !== null
    ? (value as Record<string, unknown>)[key]
    : undefined;
}

/** The settings of a request body that the request log records: its model, thinking type and
 *  budget, max_tokens (a Responses body's `max_output_tokens`), temperature, effort (a Responses
 *  body's `reasoning.effort`), and whether it streams. Nothing else of the body. */
function requestSettings(body: RequestBody): Record<string, unknown> {
  return {
    model: body.model,
    thinking: field(body.thinking, "type") ?? null,
    thinking_budget: field(body.thinking, "budget_tokens") ?? null,
    max_tokens: body.max_tokens ?? body.max_output_tokens ?? null,
    temperature: body.temperature ?? null,
    effort: field(body.output_config, "effort") ?? field(body.reasoning, "effort") ?? null,
    stream: body.stream === true,
  };
}

/** `gateway`, with each request it sends appended to `logFile` as one JSON line: when it was
 *  asked for (`at`), when it got its place and was sent (`sentAt`) and how many calls were in
 *  flight then, it among them (`inFlight`), `kind` (what the call is for), the body's settings
 *  (`requestSettings`) and the session id it went with (`userId`: its `metadata.user_id` or
 *  `prompt_cache_key`), and the reply's model (`answeredBy`), message id, request id, provider
 *  request id, the gateway's headers of it (`gateway`: gateway.ts's GATEWAY_HEADERS it came
 *  with, the deployment and model group among them), duration, stop reason, cache read and write
 *  tokens, usage and dollars; or, for a call that failed, the error's name and HTTP status. Never
 *  the key, and never a message's text. */
function loggedGateway(gateway: Gateway, logFile: string, kind: string): Gateway {
  mkdirSync(path.dirname(logFile), { recursive: true });
  const log = (line: Record<string, unknown>) =>
    appendFileSync(logFile, `${JSON.stringify(line)}\n`);
  return {
    async send(body, options) {
      const at = new Date().toISOString();
      const settings = requestSettings(body);
      const userId = bodyIdentity(body);
      let sent: Sent | undefined;
      const onSent = (each: Sent): void => {
        sent = each;
        options?.onSent?.(each);
      };
      try {
        const reply = await gateway.send(body, { ...options, onSent });
        const id: unknown = reply.message.id;
        log({
          at,
          sentAt: reply.sentAt,
          inFlight: reply.inFlight,
          kind,
          ...settings,
          userId,
          answeredBy: reply.message.model,
          messageId: typeof id === "string" ? id : null,
          requestId: reply.requestId ?? null,
          providerRequestId: reply.providerRequestId,
          gateway: reply.gatewayHeaders,
          durationMs: reply.durationMs,
          stopReason: reply.message.stop_reason,
          cacheRead: reply.spent.cacheRead,
          cacheWrite: reply.spent.cacheWrite,
          usage: reply.message.usage,
          usd: reply.spent.usd,
        });
        return reply;
      } catch (error) {
        log({
          at,
          sentAt: sent?.at ?? null,
          inFlight: sent?.inFlight ?? null,
          kind,
          ...settings,
          userId,
          error: error instanceof Error ? error.name : "error",
          status: error instanceof MessagesApiError ? (error.status ?? null) : null,
        });
        throw error;
      }
    },
    spend: () => gateway.spend(),
  };
}

// Options every model command reads.

/** The options of a command that calls a model. */
const GATEWAY_OPTIONS = {
  config: { type: "string" },
  "key-file": { type: "string" },
  "budget-usd": { type: "string" },
  concurrency: { type: "string" },
  "request-log": { type: "string" },
} as const;

/** The gateway a command's options name: the configuration's address, the key file, the budget
 *  and the concurrency. Throws a refusal on an option out of range. */
function openGateway(values: {
  config: SkillCutsConfig;
  keyFile: string;
  budgetUsd?: string;
  concurrency?: string;
}): Gateway {
  const concurrency =
    values.concurrency === undefined ? DEFAULT_CONCURRENCY : Number(values.concurrency);
  if (!Number.isInteger(concurrency) || concurrency < 1) {
    throw new Refused(`--concurrency ${values.concurrency} is not a whole number of 1 or more`);
  }
  let budgetUsd: number | undefined;
  if (values.budgetUsd !== undefined) {
    budgetUsd = Number(values.budgetUsd);
    if (values.budgetUsd.trim() === "" || !Number.isFinite(budgetUsd) || budgetUsd < 0) {
      throw new Refused(`--budget-usd ${values.budgetUsd} is not a number of dollars of 0 or more`);
    }
  }
  return createGateway({
    baseUrl: values.config.gatewayBaseUrl,
    keyFile: values.keyFile,
    concurrency,
    timeoutMs: CALL_TIMEOUT_MS,
    ...(budgetUsd === undefined ? {} : { budgetUsd }),
  });
}

/** `openGateway` of a model command's parsed options: the configuration `config`, the key file
 *  `keyFile`, and `values`' `--budget-usd` and `--concurrency`. */
function gatewayOf(
  config: SkillCutsConfig,
  keyFile: string,
  values: { readonly "budget-usd"?: string; readonly concurrency?: string }
): Gateway {
  return openGateway({
    config,
    keyFile,
    budgetUsd: values["budget-usd"],
    concurrency: values.concurrency,
  });
}

/** The Oh My Pi binary a capture runs, as an absolute path: `given` (`--omp`), else
 *  `env.LEGION_OMP_PATH`, else `omp`; a name with no `/` is looked up on `env.PATH`, since a
 *  capture opens the binary as a file (its natives are linked from it). Undefined when the one
 *  that applies names no file. */
export function ompBinary(
  given: string | undefined,
  env: Readonly<Record<string, string | undefined>> = process.env
): string | undefined {
  const name =
    given ??
    (env.LEGION_OMP_PATH === undefined || env.LEGION_OMP_PATH === "" ? "omp" : env.LEGION_OMP_PATH);
  if (!name.includes("/")) return Bun.which(name, { PATH: env.PATH ?? "" }) ?? undefined;
  const full = path.resolve(name);
  return statSync(full, { throwIfNoEntry: false })?.isFile() ? full : undefined;
}

/** `ompBinary(given)`, or a refusal naming where it looked. */
function requiredOmp(given: string | undefined): string {
  const found = ompBinary(given);
  if (found !== undefined) return found;
  const where =
    given !== undefined
      ? `--omp ${given} names no file`
      : process.env.LEGION_OMP_PATH
        ? `LEGION_OMP_PATH ${process.env.LEGION_OMP_PATH} names no file`
        : "no --omp is given, LEGION_OMP_PATH is unset, and no omp is on PATH";
  throw new Refused(
    `--models makes captures, which run Oh My Pi, and none is found: ${where}. Give --omp <absolute path>, or set LEGION_OMP_PATH`
  );
}

// `read`.

const READ_USAGE =
  "usage: cli.ts read --config <skill-cuts.json> --key-file <file> --prompt <name> --in <input.json> --out <answer.json> [--request-log <file>] [--budget-usd <n>] [--concurrency <n>]";

/** `skill-cuts read`. */
async function readCommand(argv: readonly string[], io: CliIo): Promise<number> {
  const values = parse(
    argv,
    {
      ...GATEWAY_OPTIONS,
      prompt: { type: "string" },
      in: { type: "string" },
      out: { type: "string" },
    },
    READ_USAGE
  );
  const given = required(values, ["config", "key-file", "prompt", "in", "out"], READ_USAGE);
  if (!(READER_NAMES as readonly string[]).includes(given.prompt)) {
    throw new Refused(
      `--prompt ${given.prompt} is no reader; the readers are ${READER_NAMES.join(", ")}`
    );
  }
  const name = given.prompt as ReaderName;
  const checked = READERS[name].input.safeParse(readJson(given.in, "the input"));
  if (!checked.success) {
    throw new Refused(
      `the ${name} reader's input ${given.in} is refused: ${schemaFaults(checked.error)}`
    );
  }
  const config = configFrom(given.config);
  const settings = loadPrompt(name).settings;
  const gateway = openGateway({
    config,
    keyFile: given["key-file"],
    budgetUsd: values["budget-usd"],
    concurrency: values.concurrency,
  });
  const logged = loggedGateway(
    gateway,
    values["request-log"] ?? `${given.out}.${REQUEST_LOG}`,
    `reader:${name}`
  );
  try {
    const result = await runReader(name, checked.data, logged);
    const usd = usdOf(result.spent);
    writeWhole(given.out, json({ prompt: name, settings, ...result, usd }));
    io.out(
      `read: the ${name} reader answered after ${result.refusals.length} refused replies, $${usd.toFixed(4)} at list price; written to ${given.out}\n`
    );
    return EXIT.ok;
  } catch (error) {
    if (!(error instanceof ReaderFailed)) throw error;
    const usd = usdOf(error.spent);
    writeWhole(
      given.out,
      json({
        prompt: name,
        settings,
        answer: null,
        refusals: error.refusals,
        spent: error.spent,
        usd,
      })
    );
    io.err(`read: ${error.message}; written to ${given.out}`);
    return EXIT.failed;
  }
}

// `control`.

const CONTROL_USAGE = [
  "usage: cli.ts control <kind> --config <skill-cuts.json> --key-file <file> --in <input.json> --out <result.json> [--request-log <file>] [--budget-usd <n>] [--concurrency <n>], where <kind> and what it plants is one of:",
  "  map --delete-line <a line of the cut text that the base text holds too>",
  "  attack [--plant-line <line>]",
  "  same-job --planted <file holding one skill entry {id, repo, name, description}> --copy-of <id>",
  "  sentence --into <id> --sentence <text>",
  "or the craft reader's control, which takes its own flags: control craft (--remove-section <heading> | --join-lines <a>-<b>) ...",
].join("\n");

const CONTROL_KINDS = ["map", "attack", "same-job", "sentence"] as const;
type ControlKind = (typeof CONTROL_KINDS)[number];

/** The control `kind` on the reader input `input`, with what `values` plant. Throws a refusal. */
function controlOf(
  kind: ControlKind,
  input: unknown,
  values: Record<string, string | undefined>
): Control {
  const checked = READERS[kind].input.safeParse(input);
  if (!checked.success) {
    throw new Refused(`the ${kind} reader's input is refused: ${schemaFaults(checked.error)}`);
  }
  switch (kind) {
    case "map": {
      const { "delete-line": deleteLine } = required(values, ["delete-line"], CONTROL_USAGE);
      return { reader: "map", input: READERS.map.input.parse(input), deleteLine };
    }
    case "attack":
      return {
        reader: "attack",
        input: READERS.attack.input.parse(input),
        plantLine: values["plant-line"] ?? ATTACK_PLANTED_LINE,
      };
    case "same-job": {
      const given = required(values, ["planted", "copy-of"], CONTROL_USAGE);
      const planted = SameJobInput.shape.skills.element.safeParse(
        readJson(given.planted, "the planted skill")
      );
      if (!planted.success) {
        throw new Refused(
          `the planted skill ${given.planted} is not one skill entry {id, repo, name, description}`
        );
      }
      return {
        reader: "same-job",
        input: READERS["same-job"].input.parse(input),
        planted: planted.data,
        copyOf: given["copy-of"],
      };
    }
    case "sentence": {
      const given = required(values, ["into", "sentence"], CONTROL_USAGE);
      return {
        reader: "sentence",
        input: READERS.sentence.input.parse(input),
        into: given.into,
        sentence: given.sentence,
      };
    }
  }
}

/** `skill-cuts control <kind>`; `control craft` is `controlCraftCommand`. */
async function controlCommand(argv: readonly string[], io: CliIo): Promise<number> {
  const [kind, ...rest] = argv;
  if (kind === "craft") return await controlCraftCommand(rest, io);
  if (!(CONTROL_KINDS as readonly (string | undefined)[]).includes(kind)) {
    throw new Refused(`control ${kind ?? ""}: no such control. ${CONTROL_USAGE}`);
  }
  const values = parse(
    rest,
    {
      ...GATEWAY_OPTIONS,
      in: { type: "string" },
      out: { type: "string" },
      "delete-line": { type: "string" },
      "plant-line": { type: "string" },
      planted: { type: "string" },
      "copy-of": { type: "string" },
      into: { type: "string" },
      sentence: { type: "string" },
    },
    CONTROL_USAGE
  );
  const given = required(values, ["config", "key-file", "in", "out"], CONTROL_USAGE);
  const control = controlOf(kind as ControlKind, readJson(given.in, "the input"), values);
  try {
    plantControl(control);
  } catch (error) {
    throw new Refused(`the ${kind} control cannot be planted: ${messageFor(error)}`);
  }
  const config = configFrom(given.config);
  const gateway = openGateway({
    config,
    keyFile: given["key-file"],
    budgetUsd: values["budget-usd"],
    concurrency: values.concurrency,
  });
  const logged = loggedGateway(
    gateway,
    values["request-log"] ?? `${given.out}.${REQUEST_LOG}`,
    `control:${kind}`
  );
  try {
    const ran = await runControl(control, logged);
    const usd = usdOf(ran.result.spent);
    writeWhole(
      given.out,
      json({ control: kind, held: ran.held, why: ran.why, ...ran.result, usd })
    );
    const line = `control ${kind}: ${ran.held ? "held" : "did not hold"}: ${ran.why}; $${usd.toFixed(4)} at list price; written to ${given.out}`;
    if (ran.held) io.out(`${line}\n`);
    else io.err(line);
    return ran.held ? EXIT.ok : EXIT.failed;
  } catch (error) {
    if (!(error instanceof ReaderFailed)) throw error;
    const usd = usdOf(error.spent);
    writeWhole(
      given.out,
      json({
        control: kind,
        held: false,
        why: error.message,
        answer: null,
        refusals: error.refusals,
        spent: error.spent,
        usd,
      })
    );
    io.err(`control ${kind}: did not hold: ${error.message}; written to ${given.out}`);
    return EXIT.failed;
  }
}

// `craft`, `nearest`, `craft-coverage` and `control craft`: the readers of our skills.

/** Whether `file` is a file; false where it, or a directory on its path, is none. */
function isFile(file: string): boolean {
  try {
    return statSync(file).isFile();
  } catch {
    return false;
  }
}

/** `value` when it is a string that is not empty, else `otherwise`: a frontmatter field. */
function textOr(value: unknown, otherwise: string): string {
  return typeof value === "string" && value !== "" ? value : otherwise;
}

/** A skill directory as the readers read it: its name (load.ts's `skillNameOf`: its frontmatter
 *  `name`, trimmed, else the directory's) and every `.md` file under it (`skillFiles`), SKILL.md
 *  first. */
interface ReadSkill {
  readonly dir: string;
  readonly name: string;
  readonly files: readonly { readonly path: string; readonly text: string }[];
}

/** The skill in `dir`, which the flag `flag` names. Throws a refusal when `dir` cannot be read or
 *  holds no SKILL.md. */
function readSkill(dir: string, flag: string): ReadSkill {
  let files: ReadSkill["files"];
  try {
    files = skillFiles(dir);
  } catch (error) {
    throw new Refused(`${flag} ${dir} cannot be read: ${messageFor(error)}`);
  }
  const skill = files.find((file) => file.path === "SKILL.md");
  if (skill === undefined) throw new Refused(`${flag} ${dir} has no SKILL.md`);
  return { dir, name: skillNameOf(readFrontmatter(skill.text), dir), files };
}

/** The skills directly under `root`: each `<root>/<name>/SKILL.md`, by name. Throws a refusal
 *  when `root` cannot be read or holds none. */
function skillDirs(root: string): string[] {
  let names: string[];
  try {
    names = readdirSync(root);
  } catch (error) {
    throw new Refused(`cannot read the root ${root}: ${messageFor(error)}`);
  }
  const skills = names.filter((name) => isFile(path.join(root, name, "SKILL.md"))).sort();
  if (skills.length === 0) throw new Refused(`the root ${root} holds no <name>/SKILL.md`);
  return skills;
}

/** The file a reader's exact user message is written to: `out` less `.json`, and `.input.md`. */
function inputFile(out: string): string {
  return `${out.replace(/\.json$/, "")}.input.md`;
}

/** The bytes of `file`, which is `what`. Throws a refusal naming it. */
function readBytes(file: string, what: string): Buffer {
  try {
    return readFileSync(file);
  } catch (error) {
    throw new Refused(`cannot read ${what} ${file}: ${messageFor(error)}`);
  }
}

/** The two sides of `value`, the value of `flag` in the shape `shape` (`<a>=<b>`): the text before
 *  its first `=`, trimmed, and the rest. Throws a refusal when either side is empty. */
function pairOf(flag: string, value: string, shape: string): [string, string] {
  const at = value.indexOf("=");
  const left = value.slice(0, Math.max(at, 0)).trim();
  const right = value.slice(at + 1);
  if (at < 0 || left === "" || right === "") throw new Refused(`${flag} ${value} is not ${shape}`);
  return [left, right];
}

const CRAFT_OPTIONS = {
  ...GATEWAY_OPTIONS,
  skill: { type: "string" },
  "skill-source": { type: "string" },
  style: { type: "string" },
  "style-source": { type: "string" },
  craft: { type: "string" },
  "craft-source": { type: "string" },
  nearest: { type: "string", multiple: true },
  out: { type: "string" },
} as const;
const CRAFT_REQUIRED = [
  ...["config", "key-file", "skill", "style", "style-source", "craft", "craft-source"],
  "out",
] as const;
const CRAFT_FLAGS =
  "--config <skill-cuts.json> --key-file <file> --skill <dir> --style <file> --style-source <text> --craft <craft-list.json> --craft-source <text> [--nearest <dir>]... [--skill-source <text>]";
const CRAFT_USAGE = `usage: cli.ts craft ${CRAFT_FLAGS} --out <entry.json> [--request-log <file>] [--budget-usd <n>] [--concurrency <n>]`;

/** The style text and the craft list a reader reads (`--style` and `--craft`), each with its file,
 *  the words its `--*-source` flag gives it, and its SHA-256: `craft`'s and `proposals`'. */
type RulesReads = Pick<ProposalsRun, "style" | "craft">;

/** What a craft read reads, all of it before any call: the skill, the style text and its rules,
 *  the craft list, and each nearest skill of theirs, each with its source where it has one. */
interface CraftReads extends RulesReads {
  readonly skill: ReadSkill;
  readonly skillSource: string | null;
  readonly nearest: readonly ReadSkill[];
}

/** The reads of `--style` and `--craft`. Throws a refusal: a file it cannot read, a style text with
 *  no rule, a craft list the shape refuses. */
function rulesReads(
  given: Record<"style" | "style-source" | "craft" | "craft-source", string>
): RulesReads {
  const styleBytes = readBytes(given.style, "the style text");
  let rules: StyleRule[];
  try {
    rules = styleRules(styleBytes.toString("utf8"));
  } catch (error) {
    throw new Refused(`the style text ${given.style} is refused: ${messageFor(error)}`);
  }
  const craftList = readCraftList(given.craft);
  return {
    style: { file: given.style, source: given["style-source"], sha256: sha256(styleBytes), rules },
    craft: {
      file: given.craft,
      source: given["craft-source"],
      sha256: sha256(craftList.bytes),
      list: craftList.list,
    },
  };
}

/** The reads of `craft`'s flags. Throws a refusal: a file it cannot read, a style text with no
 *  rule, a craft list the shape refuses, a skill directory with no SKILL.md. */
function craftReads(
  given: Record<"skill" | "style" | "style-source" | "craft" | "craft-source", string>,
  values: { readonly "skill-source"?: string; readonly nearest?: readonly string[] }
): CraftReads {
  return {
    ...rulesReads(given),
    skill: readSkill(given.skill, "--skill"),
    skillSource: values["skill-source"] ?? null,
    nearest: (values.nearest ?? []).map((dir) => readSkill(dir, "--nearest")),
  };
}

/** The craft reader's input of `reads`, with `skill` in place of its skill when given. Throws a
 *  refusal when the reader's input schema refuses it. */
function craftReaderInput(reads: CraftReads, skill: ReadSkill = reads.skill): CraftInput {
  const checked = READERS.craft.input.safeParse(
    craftInput({
      skill: skill.name,
      files: skill.files,
      style: reads.style.rules,
      list: reads.craft.list,
      nearest: reads.nearest,
    })
  );
  if (!checked.success) {
    throw new Refused(`the craft reader's input is refused: ${schemaFaults(checked.error)}`);
  }
  return checked.data;
}

/** One fresh request of the reader `name` on `input`, its entry written whole to `out`: `entry`,
 *  then the answer (null when every reply was refused), what `after` gives for that answer, the
 *  refusals, the spend and its dollars at list price. Returns the answer and its dollars, or the
 *  ReaderFailed of a reader whose every reply was refused; any other failure throws. */
async function readToFile<A>(
  name: ReaderName,
  input: unknown,
  gateway: Gateway,
  out: string,
  entry: Record<string, unknown>,
  after: (answer: A | null) => Record<string, unknown> = () => ({})
): Promise<{ answer: A; usd: number } | { answer: undefined; failed: ReaderFailed }> {
  try {
    const result = await runReader(name, input, gateway);
    // runReader parsed it with the reader's answer schema.
    const answer = result.answer as A;
    const usd = usdOf(result.spent);
    writeWhole(
      out,
      json({
        ...entry,
        answer,
        ...after(answer),
        refusals: result.refusals,
        spent: result.spent,
        usd,
      })
    );
    return { answer, usd };
  } catch (error) {
    if (!(error instanceof ReaderFailed)) throw error;
    const { refusals, spent } = error;
    writeWhole(
      out,
      json({ ...entry, answer: null, ...after(null), refusals, spent, usd: usdOf(spent) })
    );
    return { answer: undefined, failed: error };
  }
}

/** One fresh request of the craft reader on `input`, the input of `skill`: writes its exact user
 *  message to `inputFile(out)` and the entry to `out`, whole. The entry names the skill (its
 *  directory, source and each file with its SHA-256), the style text (file, source, SHA-256 and
 *  how many rules), the craft list (file, source, SHA-256 and the reader properties sent), each
 *  nearest skill, the reader's prompt and settings, the answer (null when every reply was
 *  refused), the refusals and the spend. */
async function craftRead(
  reads: CraftReads,
  skill: ReadSkill,
  input: CraftInput,
  out: string,
  gateway: Gateway
): Promise<{ answer: CraftAnswer; usd: number } | { answer: undefined; failed: ReaderFailed }> {
  const message = inputFile(out);
  writeWhole(message, READERS.craft.render(input));
  const entry = {
    entry: "craft",
    skill: {
      name: skill.name,
      dir: skill.dir,
      source: reads.skillSource,
      files: fileDigests(skill.files),
    },
    ...rulesRecords(reads, input.properties),
    nearest: reads.nearest.map((each) => ({
      name: each.name,
      dir: each.dir,
      files: fileDigests(each.files),
    })),
    reader: { prompt: "craft", settings: loadPrompt("craft").settings },
    input: message,
  };
  return await readToFile<CraftAnswer>("craft", input, gateway, out, entry);
}

/** The gaps of `answer` in a few words: `no gap`, or their count and their rules. */
function gapWords(answer: CraftAnswer): string {
  if (answer.gaps.length === 0) return "no gap";
  const rules = [...new Set(answer.gaps.map((gap) => gap.rule))];
  return `${counted(answer.gaps.length, "gap")} (${rules.join(", ")})`;
}

/** `skill-cuts craft`. */
async function craftCommand(argv: readonly string[], io: CliIo): Promise<number> {
  const values = parse(argv, CRAFT_OPTIONS, CRAFT_USAGE);
  const given = required(values, CRAFT_REQUIRED, CRAFT_USAGE);
  const reads = craftReads(given, values);
  const input = craftReaderInput(reads);
  const gateway = gatewayOf(configFrom(given.config), given["key-file"], values);
  const logged = loggedGateway(
    gateway,
    values["request-log"] ?? `${given.out}.${REQUEST_LOG}`,
    "reader:craft"
  );
  const read = await craftRead(reads, reads.skill, input, given.out, logged);
  if (read.answer === undefined) {
    io.err(`craft: ${read.failed.message}; written to ${given.out}`);
    return EXIT.failed;
  }
  io.out(
    `craft: ${reads.skill.name}: ${gapWords(read.answer)}, $${read.usd.toFixed(4)} at list price; written to ${given.out}\n`
  );
  return EXIT.ok;
}

const NEAREST_FLAGS =
  "--config <skill-cuts.json> --key-file <file> --skill <dir> [--skill-source <text>] --candidates <source>=<root>... --out <row.json>";
const NEAREST_USAGE = `usage: cli.ts nearest ${NEAREST_FLAGS} [--request-log <file>] [--budget-usd <n>] [--concurrency <n>]`;

/** The text of a SKILL.md less its frontmatter (context.ts's `frontmatterBlock`: the block
 *  readFrontmatter reads, after any leading HTML comments), with the blank lines before it and the
 *  white space after it trimmed. */
function skillBody(text: string): string {
  const body = frontmatterBlock(text)?.rest ?? text.replace(/\r\n?/g, "\n");
  return body.replace(/^\n+/, "").trimEnd();
}

/** `skill-cuts nearest`: one fresh request of the nearest reader, on one skill and every skill of
 *  each `--candidates` root. */
async function nearestCommand(argv: readonly string[], io: CliIo): Promise<number> {
  const values = parse(
    argv,
    {
      ...GATEWAY_OPTIONS,
      skill: { type: "string" },
      "skill-source": { type: "string" },
      candidates: { type: "string", multiple: true },
      out: { type: "string" },
    },
    NEAREST_USAGE
  );
  const given = required(values, ["config", "key-file", "skill", "out"], NEAREST_USAGE);
  const roots = (values.candidates ?? []).map((flag) => {
    const [source, root] = pairOf("--candidates", flag, "<source>=<root>");
    return { source, root, names: skillDirs(root) };
  });
  if (roots.length === 0) throw new Refused(`--candidates required. ${NEAREST_USAGE}`);
  const skillMd = path.join(given.skill, "SKILL.md");
  if (!isFile(skillMd)) throw new Refused(`--skill ${given.skill} has no SKILL.md`);
  const text = readFileSync(skillMd, "utf8");
  const frontmatter = readFrontmatter(text);
  const skill = {
    name: skillNameOf(frontmatter, given.skill),
    description: textOr(frontmatter.description, ""),
    body: skillBody(text),
  };
  const candidates = roots.flatMap(({ source, root, names }) =>
    names.map((name) => {
      const read = readFrontmatter(readFileSync(path.join(root, name, "SKILL.md"), "utf8"));
      return { name: skillNameOf(read, name), source, description: textOr(read.description, "") };
    })
  );
  const checked = READERS.nearest.input.safeParse({ skill, candidates });
  if (!checked.success) {
    throw new Refused(`the nearest reader's input is refused: ${schemaFaults(checked.error)}`);
  }
  const gateway = gatewayOf(configFrom(given.config), given["key-file"], values);
  const logged = loggedGateway(
    gateway,
    values["request-log"] ?? `${given.out}.${REQUEST_LOG}`,
    "reader:nearest"
  );
  const message = inputFile(given.out);
  writeWhole(message, READERS.nearest.render(checked.data));
  const row = {
    row: "nearest",
    skill: {
      name: skill.name,
      description: skill.description,
      source: values["skill-source"] ?? null,
      dir: given.skill,
    },
    candidates: roots.map(({ source, root, names }) => ({ source, root, skills: names.length })),
    reader: { prompt: "nearest", settings: loadPrompt("nearest").settings },
    input: message,
  };
  const read = await readToFile<NearestAnswer>(
    "nearest",
    checked.data,
    logged,
    given.out,
    row,
    (answer) => ({
      nearest:
        answer === null
          ? []
          : answer.nearest.map((entry) => ({
              ...entry,
              description:
                candidates.find((each) => each.name === entry.name && each.source === entry.source)
                  ?.description ?? "",
            })),
    })
  );
  if (read.answer === undefined) {
    io.err(`nearest: ${read.failed.message}; written to ${given.out}`);
    return EXIT.failed;
  }
  const named = read.answer.nearest.map((entry) => `${entry.name} (${entry.source})`).join(", ");
  io.out(
    `nearest: ${skill.name}: ${read.answer.verdict}${named === "" ? "" : `: ${named}`}, $${read.usd.toFixed(4)} at list price; written to ${given.out}\n`
  );
  return EXIT.ok;
}

const COVERAGE_USAGE =
  "usage: cli.ts craft-coverage --root <dir> [--source <text>] [--skip <name>]... --entries <reader-findings.jsonl>";

/** What craft-coverage reads of a craft entry (`craftRead`): its skill's directory and source, and
 *  whether it has an answer, with its gaps: proposals.ts's FindingsEntry, each gap of any shape. */
const CoverageEntry = FindingsEntry.extend({
  answer: z.looseObject({ gaps: z.array(z.unknown()) }).nullable(),
});
type CoverageEntry = z.infer<typeof CoverageEntry>;

/** Each craft entry of the entries file `file` (a `reader-findings.jsonl`) with its line, as
 *  `schema` reads it, in the file's order, and the file's bytes; its other lines (a proposal's
 *  entry) are passed over. Throws a refusal naming the file when it cannot be read, or naming a
 *  line that is no JSON or that `schema` refuses. */
function craftEntries<T>(
  file: string,
  schema: z.ZodType<T>
): { bytes: Buffer; entries: { line: number; entry: T }[] } {
  const bytes = readBytes(file, "the entries");
  const text = bytes.toString("utf8");
  const entries = text.split("\n").flatMap((line, index) => {
    if (line.trim() === "") return [];
    const where = `line ${index + 1} of the entries ${file}`;
    let value: unknown;
    try {
      value = JSON.parse(line);
    } catch {
      throw new Refused(`${where} is not JSON`);
    }
    if (field(value, "entry") !== "craft") return [];
    const entry = schema.safeParse(value);
    if (!entry.success) {
      throw new Refused(`${where} is no craft entry: ${schemaFaults(entry.error)}`);
    }
    return [{ line: index + 1, entry: entry.data }];
  });
  return { bytes, entries };
}

/** `skill-cuts craft-coverage`: each skill of a root beside its craft entry, or none. */
function craftCoverageCommand(argv: readonly string[], io: CliIo): number {
  const values = parse(
    argv,
    {
      root: { type: "string" },
      source: { type: "string" },
      skip: { type: "string", multiple: true },
      entries: { type: "string" },
    },
    COVERAGE_USAGE
  );
  const given = required(values, ["root", "entries"], COVERAGE_USAGE);
  const skip = new Set(values.skip ?? []);
  const all = skillDirs(given.root);
  const names = all.filter((name) => !skip.has(name));
  const skipped = all.filter((name) => skip.has(name));
  // Each skill's last craft entry of the source, by its directory's name, and its count of them.
  const entries = new Map<string, { last: CoverageEntry; count: number }>();
  for (const { entry } of craftEntries(given.entries, CoverageEntry).entries) {
    if (values.source !== undefined && entry.skill.source !== values.source) continue;
    const name = path.basename(entry.skill.dir);
    entries.set(name, { last: entry, count: (entries.get(name)?.count ?? 0) + 1 });
  }
  const missing = names.filter((name) => !entries.has(name));
  const lines = names.map((name) => {
    const found = entries.get(name);
    if (found === undefined) return `${name}: none`;
    const { last, count } = found;
    const what =
      last.answer === null
        ? "every reply refused"
        : last.answer.gaps.length === 0
          ? "no gap"
          : counted(last.answer.gaps.length, "gap");
    return `${name}: entry: ${what}${count > 1 ? ` (the last of ${count} entries)` : ""}`;
  });
  const source = values.source === undefined ? "" : `, source ${values.source}`;
  const skippedWords = skipped.length === 0 ? "" : ` (skipped: ${skipped.join(", ")})`;
  io.out(
    `${[
      `craft-coverage: ${given.root}${source}: ${counted(names.length, "skill")}${skippedWords}, ${names.length - missing.length} with an entry`,
      ...lines,
    ].join("\n")}\n`
  );
  if (missing.length === 0) return EXIT.ok;
  io.err(
    `craft-coverage: ${counted(missing.length, "skill")} under ${given.root} ${missing.length === 1 ? "has" : "have"} no entry: ${missing.join(", ")}`
  );
  return EXIT.failed;
}

// `proposals`: the open proposals of closed pull requests (proposals.ts).

const PROPOSALS_FLAGS =
  "--config <skill-cuts.json> --key-file <file> --body <file> --body-source <text> --section <heading> --list <text>... --pr-diff <id prefix>=<file>... --skills-root <repo>=<dir>... --repo-root <repo>=<dir> --style <file> --style-source <text> --craft <craft-list.json> --craft-source <text> --findings <reader-findings.jsonl> --out <dir>";
const PROPOSALS_DRY_RUN = "--dry-run --body <file> --section <heading> --list <text>...";
const PROPOSALS_USAGE = [
  `usage: cli.ts proposals ${PROPOSALS_FLAGS} [--request-log <file>] [--budget-usd <n>] [--concurrency <n>]`,
  `       cli.ts proposals ${PROPOSALS_DRY_RUN}`,
].join("\n");

/** `skill-cuts proposals`: each item of the named lists of a pull request's body, read with the
 *  proposal-target and proposal readers (proposals.ts); with `--dry-run`, the items only. */
async function proposalsCommand(argv: readonly string[], io: CliIo): Promise<number> {
  const values = parse(
    argv,
    {
      ...GATEWAY_OPTIONS,
      "dry-run": { type: "boolean" },
      body: { type: "string" },
      "body-source": { type: "string" },
      section: { type: "string" },
      list: { type: "string", multiple: true },
      "pr-diff": { type: "string", multiple: true },
      "skills-root": { type: "string", multiple: true },
      "repo-root": { type: "string" },
      style: { type: "string" },
      "style-source": { type: "string" },
      craft: { type: "string" },
      "craft-source": { type: "string" },
      findings: { type: "string" },
      out: { type: "string" },
    },
    PROPOSALS_USAGE
  );
  const lists = values.list ?? [];
  if (lists.length === 0) throw new Refused(`--list required. ${PROPOSALS_USAGE}`);
  const { body, section } = required(values, ["body", "section"], PROPOSALS_USAGE);
  const bodyBytes = readBytes(body, "the body");
  let items: ProposalItem[];
  try {
    items = parseProposalItems(bodyBytes.toString("utf8"), section, lists);
  } catch (error) {
    if (!(error instanceof ItemFaults)) throw error;
    io.err(`proposals: ${body}: ${error.message}`);
    return EXIT.failed;
  }
  if (values["dry-run"] === true) {
    const counts = lists.map(
      (list) => `${items.filter((item) => item.list === list).length} in ${JSON.stringify(list)}`
    );
    io.out(
      `${[
        `proposals: ${body}: ${counted(items.length, "item")}: ${counts.join(", ")}`,
        ...items.map((item) => `${item.id} (${item.state}): line ${item.line}`),
      ].join("\n")}\n`
    );
    return EXIT.ok;
  }
  const given = required(
    values,
    [
      ...["config", "key-file", "body-source", "repo-root", "style", "style-source", "craft"],
      ...["craft-source", "findings", "out"],
    ] as const,
    PROPOSALS_USAGE
  );
  const diffs = new Map<string, PullDiff>();
  for (const flag of values["pr-diff"] ?? []) {
    const [prefix, file] = pairOf("--pr-diff", flag, "<id prefix>=<file>");
    if (diffs.has(prefix)) throw new Refused(`--pr-diff gives the prefix ${prefix} twice`);
    const bytes = readBytes(file, "the diff");
    const text = bytes.toString("utf8");
    let files: DiffFile[];
    try {
      files = splitDiff(text);
    } catch (error) {
      throw new Refused(`the diff ${file} of --pr-diff ${prefix} is refused: ${messageFor(error)}`);
    }
    diffs.set(prefix, { pr: prefix, file, sha256: sha256(bytes), text, files });
  }
  const undiffed = [...new Set(items.map((item) => idPrefix(item.id)))].filter(
    (prefix) => !diffs.has(prefix)
  );
  if (undiffed.length > 0) {
    const named = undiffed.map((prefix) => {
      const ids = items.filter((item) => idPrefix(item.id) === prefix).map((item) => item.id);
      return `${prefix} (${ids.join(", ")})`;
    });
    throw new Refused(`no --pr-diff gives the diff of ${named.join("; ")}`);
  }
  const skillsRoots: { repo: string; dir: string; names: string[] }[] = [];
  for (const flag of values["skills-root"] ?? []) {
    const [repo, dir] = pairOf("--skills-root", flag, "<repo>=<dir>");
    if (skillsRoots.some((root) => root.repo === repo)) {
      throw new Refused(`--skills-root gives the repository ${repo} twice`);
    }
    skillsRoots.push({ repo, dir, names: skillDirs(dir) });
  }
  if (skillsRoots.length === 0) throw new Refused(`--skills-root required. ${PROPOSALS_USAGE}`);
  const [repo, dir] = pairOf("--repo-root", given["repo-root"], "<repo>=<dir>");
  if (!statSync(dir, { throwIfNoEntry: false })?.isDirectory()) {
    throw new Refused(`--repo-root ${given["repo-root"]}: ${dir} is no directory`);
  }
  const rules = rulesReads(given);
  const findings = craftEntries(given.findings, FindingsEntry);
  const run: ProposalsRun = {
    items,
    body: { file: body, source: given["body-source"], sha256: sha256(bodyBytes) },
    diffs,
    skillsRoots,
    repoRoot: { repo, dir },
    style: rules.style,
    craft: rules.craft,
    findings: { file: given.findings, sha256: sha256(findings.bytes), entries: findings.entries },
    out: given.out,
  };
  const pending = pendingItems(items, given.out);
  const gateway = gatewayOf(configFrom(given.config), given["key-file"], values);
  const log = values["request-log"] ?? path.join(given.out, REQUEST_LOG);
  io.out(
    `proposals: ${counted(items.length, "item")}, ${pending.length} to read; the others have an entry under ${given.out}\n`
  );
  const missing = await runProposals(
    run,
    pending,
    {
      target: loggedGateway(gateway, log, "reader:proposal-target"),
      proposal: loggedGateway(gateway, log, "reader:proposal"),
    },
    io
  );
  const spent = `$${gateway.spend().usd.toFixed(4)} at list price`;
  if (missing.length > 0) {
    io.err(
      `proposals: ${counted(missing.length, "item")} of ${items.length} ${missing.length === 1 ? "has" : "have"} no entry: ${missing.join(", ")}; ${spent}`
    );
    return EXIT.failed;
  }
  io.out(`proposals: each of ${counted(items.length, "item")} has an entry; ${spent}\n`);
  return EXIT.ok;
}

const CONTROL_CRAFT_USAGE = `usage: cli.ts control craft (--remove-section <heading> | --join-lines <a>-<b>) --copy-line <a>[-<b>] [--copy-kind <present|missing>]... [--copy-rule <id>]... [--copy-claim <text>] --original-line <a>[-<b>|all] [--original-kind <present|missing>]... [--original-rule <id>]... [--original-claim <text>] ${CRAFT_FLAGS} --out <dir> [--request-log <file>] [--budget-usd <n>] [--concurrency <n>]`;

const HEADING_LINE = /^(#{1,6})\s/;
const LIST_ITEM = /^\s*(?:[-*+]|\d+[.)])\s+/;

/** `lines` as the text of a file, with a final newline where `text` had one. */
function withLines(text: string, lines: readonly string[]): string {
  return `${lines.join("\n")}${text.endsWith("\n") ? "\n" : ""}`;
}

/** `text`, a SKILL.md, with the section `heading` removed: the heading's line and every line up
 *  to the next heading of its level or a higher one. A line in a fenced block is no heading
 *  (proposals.ts's `fencedLines`). Throws a refusal when no line, or more than one, is that
 *  heading. */
function removeSection(text: string, heading: string): string {
  const lines = textLines(text);
  const inFence = fencedLines(lines);
  const at = lines.flatMap((line, index) =>
    !inFence[index] && line.trim() === heading.trim() ? [index] : []
  );
  if (at.length !== 1) {
    throw new Refused(
      at.length === 0
        ? `no line of SKILL.md is the heading ${heading}`
        : `lines ${at.map((index) => index + 1).join(", ")} of SKILL.md are each the heading ${heading}`
    );
  }
  const start = at[0] ?? 0;
  const level = HEADING_LINE.exec(lines[start] ?? "")?.[1]?.length;
  if (level === undefined) throw new Refused(`--remove-section ${heading} is no markdown heading`);
  let end = start + 1;
  for (; end < lines.length; end++) {
    const depth = inFence[end] ? undefined : HEADING_LINE.exec(lines[end] ?? "")?.[1]?.length;
    if (depth !== undefined && depth <= level) break;
  }
  return withLines(text, [...lines.slice(0, start), ...lines.slice(end)]);
}

/** `text`, a SKILL.md, with the list items of lines `from` to `to` joined into one paragraph at
 *  line `from`, their list markers removed. Throws a refusal when a line of them is no list item
 *  or the range runs past the file's end. */
function joinLines(text: string, from: number, to: number): string {
  const lines = textLines(text);
  if (to > lines.length) {
    throw new Refused(
      `--join-lines ${from}-${to} runs past the last line, ${lines.length}, of SKILL.md`
    );
  }
  for (let line = from; line <= to; line++) {
    if (!LIST_ITEM.test(lines[line - 1] ?? ""))
      throw new Refused(`line ${line} of SKILL.md is no list item`);
  }
  const joined = lines
    .slice(from - 1, to)
    .map((line) => line.replace(LIST_ITEM, "").trim())
    .join(" ");
  return withLines(text, [...lines.slice(0, from - 1), joined, ...lines.slice(to)]);
}

/** One side of a control craft, the copy or the unchanged skill, and its criteria: a gap is a
 *  candidate when its line is in `lines` (of SKILL.md; `all`: any line of any file) and, where
 *  given, its kind is one of `kinds` and its rule one of `rules`. With a `claim`, the claim reader
 *  answers whether each candidate states it. */
interface Side {
  readonly name: "copy" | "original";
  readonly lines: { readonly from: number; readonly to: number } | "all";
  readonly kinds: readonly CraftGap["kind"][];
  readonly rules: readonly string[];
  readonly claim: string | undefined;
}

/** Each side of a control craft, as the report's words name it. */
const SIDE_WORDS: Readonly<Record<Side["name"], string>> = {
  copy: "the copy",
  original: "the unchanged skill",
};

/** A candidate of a side: its gap, and the claim reader's answer (null where the side gives no
 *  claim; `states` null where every reply was refused), with the file it is written to. */
interface Candidate {
  readonly gap: CraftGap;
  readonly claim: { states: boolean | null; reason: string; file: string } | null;
}

/** The side `name` of a control craft, as its flags give it (`--<name>-line`, `-kind`, `-rule` and
 *  `-claim`). Throws a refusal on a line range, a kind or a rule it cannot read, and on a claim of
 *  more than one line. */
function sideOf(
  name: Side["name"],
  given: {
    readonly line: string;
    readonly kinds: readonly string[] | undefined;
    readonly rules: readonly string[] | undefined;
    readonly claim: string | undefined;
  },
  reads: CraftReads,
  input: CraftInput
): Side {
  const flag = `--${name}`;
  const range = given.line;
  const parts = /^(\d+)(?:-(\d+))?$/.exec(range);
  const from = Number(parts?.[1]);
  const to = Number(parts?.[2] ?? parts?.[1]);
  if (name === "copy" ? parts === null : parts === null && range !== "all") {
    throw new Refused(`${flag}-line ${range} is not <a>[-<b>]${name === "copy" ? "" : " or all"}`);
  }
  if (parts !== null && (from < 1 || to < from)) {
    throw new Refused(`${flag}-line ${range} is not a range of lines from 1`);
  }
  const kinds = (given.kinds ?? []).map((kind) => {
    const parsed = CraftGap.shape.kind.safeParse(kind);
    if (!parsed.success) throw new Refused(`${flag}-kind ${kind} is not present or missing`);
    return parsed.data;
  });
  const rules = given.rules ?? [];
  const ids = new Set([...input.style, ...input.properties].map((rule) => rule.id));
  for (const rule of rules) {
    if (!ids.has(rule) && !NEAREST_RULE.test(rule)) {
      throw new Refused(
        `${flag}-rule ${rule} is no rule of the style text ${reads.style.file}, no reader property of the craft list and no nearest:<skill>:<path>:<line>`
      );
    }
  }
  const claim = given.claim;
  if (claim !== undefined && !OneLine.safeParse(claim).success) {
    throw new Refused(`${flag}-claim is not one line`);
  }
  return { name, lines: parts === null ? "all" : { from, to }, kinds, rules, claim };
}

/** `side`'s criteria in words. */
function sideWords(side: Side): string {
  const lines =
    side.lines === "all"
      ? "any line"
      : side.lines.from === side.lines.to
        ? `line ${side.lines.from}`
        : `lines ${side.lines.from}-${side.lines.to}`;
  const kinds = side.kinds.length === 0 ? "any kind" : side.kinds.join(" or ");
  const rules = side.rules.length === 0 ? "any rule" : side.rules.join(" or ");
  const claim = side.claim === undefined ? "no claim" : `claim: ${side.claim}`;
  return `${lines}, ${kinds}, ${rules}, ${claim}`;
}

/** The candidates of `side` among the gaps of `answer` (the craft answer on `skill`), each with
 *  the claim reader's answer where the side gives a claim (one fresh request a candidate, its
 *  input and answer written under `<out>/claims/`); whether the side holds, and why. The copy
 *  holds when a candidate states its claim (any candidate, with no claim); the unchanged skill
 *  when none does (no candidate, with no claim), and a candidate whose every claim reply was
 *  refused does not let it hold. */
async function judgeSide(
  side: Side,
  answer: CraftAnswer,
  skill: ReadSkill,
  out: string,
  gateway: Gateway
): Promise<{ candidates: Candidate[]; holds: boolean; why: string }> {
  const gaps = answer.gaps.filter(
    (gap) =>
      (side.lines === "all" ||
        (gap.file === "SKILL.md" && gap.line >= side.lines.from && gap.line <= side.lines.to)) &&
      (side.kinds.length === 0 || side.kinds.includes(gap.kind)) &&
      (side.rules.length === 0 || side.rules.includes(gap.rule))
  );
  const claim = side.claim;
  const candidates = await Promise.all(
    gaps.map(async (gap, index): Promise<Candidate> => {
      if (claim === undefined) return { gap, claim: null };
      const file = path.join(out, "claims", `${side.name}-${index + 1}.json`);
      const input = ClaimInput.parse({ claim, gap, files: skill.files });
      writeWhole(inputFile(file), READERS.claim.render(input));
      const read = await readToFile<ClaimAnswer>("claim", input, gateway, file, {
        side: side.name,
        candidate: index + 1,
        input,
        prompt: "claim",
        settings: loadPrompt("claim").settings,
      });
      return read.answer === undefined
        ? { gap, claim: { states: null, reason: read.failed.message, file } }
        : { gap, claim: { ...read.answer, file } };
    })
  );
  const places = (some: readonly Candidate[]) =>
    some.map((each) => `${each.gap.file}:${each.gap.line}`).join(", ");
  const words = SIDE_WORDS[side.name];
  if (candidates.length === 0) {
    return { candidates, holds: side.name === "original", why: `${words} has no candidate` };
  }
  if (claim === undefined) {
    return {
      candidates,
      holds: side.name === "copy",
      why: `${words} has a candidate: ${places(candidates)}`,
    };
  }
  const stating = candidates.filter((each) => each.claim?.states === true);
  const unknown = candidates.filter((each) => each.claim?.states === null);
  if (stating.length > 0) {
    return {
      candidates,
      holds: side.name === "copy",
      why: `a candidate of ${words} states the claim: ${places(stating)}`,
    };
  }
  if (side.name === "original" && unknown.length > 0) {
    return {
      candidates,
      holds: false,
      why: `the claim reader's every reply was refused for ${places(unknown)} of ${words}`,
    };
  }
  return {
    candidates,
    holds: side.name === "original",
    why: `no candidate of ${words} states the claim (${counted(candidates.length, "candidate")})`,
  };
}

/** A candidate as a line of the report: its place, kind, rule and why, and its claim answer. */
function candidateLine(candidate: Candidate): string {
  const { gap, claim } = candidate;
  const said =
    claim === null
      ? ""
      : claim.states === null
        ? ` (${claim.reason})`
        : ` (${claim.states ? "states" : "does not state"} the claim: ${claim.reason})`;
  return `  ${gap.file}:${gap.line} ${gap.kind} ${gap.rule}: ${gap.why}${said}`;
}

/** `skill-cuts control craft`: the craft reader's control (README.md, "control craft"). */
async function controlCraftCommand(argv: readonly string[], io: CliIo): Promise<number> {
  const values = parse(
    argv,
    {
      ...CRAFT_OPTIONS,
      "remove-section": { type: "string" },
      "join-lines": { type: "string" },
      "copy-line": { type: "string" },
      "copy-kind": { type: "string", multiple: true },
      "copy-rule": { type: "string", multiple: true },
      "copy-claim": { type: "string" },
      "original-line": { type: "string" },
      "original-kind": { type: "string", multiple: true },
      "original-rule": { type: "string", multiple: true },
      "original-claim": { type: "string" },
    },
    CONTROL_CRAFT_USAGE
  );
  const given = required(
    values,
    [...CRAFT_REQUIRED, "copy-line", "original-line"],
    CONTROL_CRAFT_USAGE
  );
  const removed = values["remove-section"];
  const joined = values["join-lines"];
  if ((removed === undefined) === (joined === undefined)) {
    throw new Refused(`give one of --remove-section and --join-lines. ${CONTROL_CRAFT_USAGE}`);
  }
  const reads = craftReads(given, values);
  const originalInput = craftReaderInput(reads);
  const copySide = sideOf(
    "copy",
    {
      line: given["copy-line"],
      kinds: values["copy-kind"],
      rules: values["copy-rule"],
      claim: values["copy-claim"],
    },
    reads,
    originalInput
  );
  const originalSide = sideOf(
    "original",
    {
      line: given["original-line"],
      kinds: values["original-kind"],
      rules: values["original-rule"],
      claim: values["original-claim"],
    },
    reads,
    originalInput
  );
  const skillText = reads.skill.files[0]?.text ?? "";
  let changed: string;
  if (removed !== undefined) changed = removeSection(skillText, removed);
  else {
    const range = /^(\d+)-(\d+)$/.exec(joined ?? "");
    const [from, to] = [Number(range?.[1]), Number(range?.[2])];
    if (range === null || from < 1 || to <= from) {
      throw new Refused(`--join-lines ${joined} is not <a>-<b>, two lines from 1 with a before b`);
    }
    changed = joinLines(skillText, from, to);
  }
  const copies = path.join(given.out, "copy");
  if (existsSync(copies)) {
    throw new Refused(`--out ${given.out} holds a copy already: give a fresh --out`);
  }
  const gateway = gatewayOf(configFrom(given.config), given["key-file"], values);
  const log = values["request-log"] ?? path.join(given.out, REQUEST_LOG);
  const copyDir = path.join(copies, path.basename(path.resolve(given.skill)));
  // The directory a link names, so that the change is never written through it into the skill.
  cpSync(realpathSync(given.skill), copyDir, { recursive: true });
  writeWhole(path.join(copyDir, "SKILL.md"), changed);
  const copy = readSkill(copyDir, "the copy");
  const craftGateway = loggedGateway(gateway, log, "reader:craft");
  const files = {
    copy: path.join(given.out, "copy.json"),
    original: path.join(given.out, "original.json"),
  };
  const [copyRead, originalRead] = await Promise.all([
    craftRead(reads, copy, craftReaderInput(reads, copy), files.copy, craftGateway),
    craftRead(reads, reads.skill, originalInput, files.original, craftGateway),
  ]);
  const change = removed !== undefined ? { removeSection: removed } : { joinLines: joined ?? "" };
  const record = (side: Side, skill: ReadSkill, entry: string) => ({
    skill: skill.dir,
    entry,
    lines: side.lines === "all" ? "all" : `${side.lines.from}-${side.lines.to}`,
    kinds: side.kinds,
    rules: side.rules,
    claim: side.claim ?? null,
  });
  const report = [
    `control craft: ${reads.skill.name}: ${removed !== undefined ? `--remove-section ${removed}` : `--join-lines ${joined}`}; the copy is ${copyDir}`,
  ];
  let control: Record<string, unknown>;
  let held: boolean;
  let why: string;
  if (copyRead.answer === undefined || originalRead.answer === undefined) {
    held = false;
    why = [
      ...(copyRead.answer === undefined
        ? [`on ${SIDE_WORDS.copy}, ${copyRead.failed.message}`]
        : []),
      ...(originalRead.answer === undefined
        ? [`on ${SIDE_WORDS.original}, ${originalRead.failed.message}`]
        : []),
    ].join("; ");
    control = {
      copy: record(copySide, copy, files.copy),
      original: record(originalSide, reads.skill, files.original),
    };
  } else {
    const claimGateway = loggedGateway(gateway, log, "reader:claim");
    const [copyJudged, originalJudged] = await Promise.all([
      judgeSide(copySide, copyRead.answer, copy, given.out, claimGateway),
      judgeSide(originalSide, originalRead.answer, reads.skill, given.out, claimGateway),
    ]);
    held = copyJudged.holds && originalJudged.holds;
    why = `${copyJudged.why}; ${originalJudged.why}`;
    control = {
      copy: { ...record(copySide, copy, files.copy), ...copyJudged },
      original: { ...record(originalSide, reads.skill, files.original), ...originalJudged },
    };
    for (const [side, judged] of [
      [copySide, copyJudged],
      [originalSide, originalJudged],
    ] as const) {
      report.push(
        `${side.name} (${sideWords(side)}): ${counted(judged.candidates.length, "candidate")}; ${judged.holds ? "holds" : "does not hold"}: ${judged.why}`,
        ...judged.candidates.map(candidateLine)
      );
    }
  }
  const usd = gateway.spend().usd;
  writeWhole(
    path.join(given.out, "control.json"),
    json({ control: "craft", skill: given.skill, change, ...control, held, why, usd })
  );
  io.out(`${report.join("\n")}\n`);
  const line = `control craft: ${held ? "held" : "not held"}: ${why}; $${usd.toFixed(4)} at list price; written to ${given.out}`;
  if (held) io.out(`${line}\n`);
  else io.err(line);
  return held ? EXIT.ok : EXIT.failed;
}

// `behaviour`: the results of a case.

/** One run of one arm of a case in one stage: score.ts's RunRecord, and what the run did. */
const Run = z.strictObject({
  ...RunRecord.shape,
  /** The run's number in its arm and stage, from 1. */
  run: z.int().positive(),
  /** The round of the stage that started it, from 1; its blind order's seed, and its send
   *  order's (replay.ts's `sendOrder`), is `seeds[round - 1]`. */
  round: z.int().positive(),
  /** Its place in that round's send order, from 0. */
  sendPlace: z.int().nonnegative(),
  /** The session id every request of the run went with (its `metadata.user_id` or
   *  `prompt_cache_key`), its own, or null for a body that names none. */
  userId: z.string().nullable(),
  /** Its reply's opaque id in that round's blind order, or null when the replay failed. */
  id: z.string().nullable(),
  /** The rounds of skill reads the replay answered before the step, or null when it failed. */
  skillRounds: z.int().nonnegative().nullable(),
  /** The step's content, or null when the replay failed. */
  reply: z.array(z.looseObject({ type: z.string() })).nullable(),
  replaySpent: z.array(SpentRecord),
  judgeSpent: z.array(SpentRecord),
});
type Run = z.infer<typeof Run>;

/** How a case's session is taken to have had Dispatch, which the Envoy plugin's block rests on
 *  (arms.ts's `dispatchOf`): from a Legion role, from its transcript, or on the assumption. */
const DispatchFrom = z.enum(["legion-role", "transcript", "assumed"]);
type DispatchFrom = z.infer<typeof DispatchFrom>;

/** The capture an arm's runs replay: its body file, below `--out`, and the two digests; and, for
 *  a capture that carries the Envoy plugin's block of the skill, where the answer that the
 *  session had Dispatch comes from (absent otherwise, and in a stage made before this field). */
const CaptureNote = z.strictObject({
  bodyFile: z.string(),
  captureDigest: z.string().nullable(),
  sessionDigest: z.string().nullable(),
  dispatch: DispatchFrom.optional(),
});

/** How a stage's runs were sent, as each stage result records it: each run with a session id of
 *  its own, and every arm's runs of a round in one seeded order. A stage result that does not
 *  record it was made before, when an arm's runs shared its capture's one id and went as one
 *  batch, so its runs are no runs of this procedure. */
const SENDS = "each run its own session id; a round's arms interleaved in one seeded order";

/** What a stage whose arms hold the arm of ours records of it: our skill's name; whether our text
 *  replaced a skill of our name that the case's profile held, which makes the arm a bundle (their
 *  skill hidden, ours at the given text); and the path inside the skill of each history read of
 *  theirs it removed because ours has no file there (arms.ts's `Arm.ours`). */
const OursNote = z.strictObject({
  name: z.string().min(1),
  replacedOwn: z.boolean(),
  removedReads: z.array(z.string()),
});
type OursNote = z.infer<typeof OursNote>;

/** The digests (`textDigest`) of the skill directories a run reads: `--old`, and `--new` or
 *  `--ours` where given; each stage records those it ran with. */
const Texts = z.strictObject({
  old: z.string(),
  new: z.string().optional(),
  ours: z.string().optional(),
});
type Texts = z.infer<typeof Texts>;

/** One stage of a case: `running` until score.ts's `stageOutcome` says `counted` or `unscreened`.
 *  `texts` are the digests of `--old` and of `--new` or `--ours` it ran with, `compared` the arm
 *  the old text was compared with (a stage that records none was run with `--new`, before it was
 *  recorded), `ours` the arm of ours' note where its arms hold it, and `plantedLine` the line
 *  `minus` left out in place of the case's (control only). `judge` is the judge's identity
 *  (judge.ts's JudgeIdentity) when the stage was made, before its first judgement; a stage made
 *  before it was recorded has none. `sends` is SENDS. `usd` is what its calls cost at list price.
 *  `runner` is the runner that started it (`runnerDigest`), absent in a stage started before the
 *  runner was recorded. */
const StageResult = z.strictObject({
  state: StageState,
  sends: z.literal(SENDS),
  texts: Texts,
  compared: Compared.optional(),
  ours: OursNote.optional(),
  plantedLine: z.string().nullable(),
  judge: JudgeIdentity.optional(),
  captures: z.record(z.string(), CaptureNote),
  seeds: z.array(z.string()),
  runs: z.array(Run),
  usd: z.number(),
  runner: z.string().optional(),
});
type StageResult = z.infer<typeof StageResult>;

/** The compared arm `stage` records, `new` for a stage of the cut's runner that records none. */
function recordedArm(stage: StageResult): Compared {
  return stage.compared ?? "new";
}

const Judgement = z.strictObject({
  verdict: z.enum(["pass", "fail"]).nullable(),
  reason: z.string(),
});

/** How the drop of a case whose block of its skill the arms cannot each rebuild begins. */
const INJECTION_UNREBUILDABLE = "injection-unrebuildable";
/** The kinds of such a block: arms.ts's `injectionFaults` (a `/skill:` prompt the user typed, a
 *  block with arguments or in another form, a dispatch-first block away from where the plugin
 *  puts it), and capture.ts's InjectionRefused (a subagent whose agent autoloads the skill and
 *  whose messages hold no block of it). */
const InjectionKind = z.enum([
  "user-invoked",
  "other-shape",
  "plugin-block-misplaced",
  "autoload-no-block",
]);
type InjectionKind = z.infer<typeof InjectionKind>;

/** Everything the runner keeps of one case: why it was dropped, if it was, and the stage that
 *  dropped it (`droppedAt`: `screen` for a drop by the judge controls, the model, the listing or a
 *  block its arms cannot rebuild, else the stage whose capture needed a directory no read gives
 *  or found such a block; absent when it was not dropped, and in a result written before the
 *  stage was recorded, `dropStage`); for an `injection-unrebuildable` drop, the kind of each fault
 *  (`injectionKinds`); its judge controls; each stage it ran, `confirm` its latest confirm and
 *  `earlierConfirms` the finished ones before it, oldest first; and the peak resident memory at
 *  the end of its last stage. */
export const CaseResult = z.strictObject({
  caseId: z.string().min(1),
  skill: z.string().min(1),
  cut: z.string().min(1),
  dropped: z.string().nullable(),
  droppedAt: Stage.optional(),
  controls: z
    .strictObject({
      realStep: Judgement,
      violating: Judgement,
      nothing: Judgement,
      failed: z.array(ControlName),
      hold: z.boolean(),
      usd: z.number(),
    })
    .nullable(),
  stages: z.strictObject({
    screen: StageResult.optional(),
    confirm: StageResult.optional(),
    control: StageResult.optional(),
  }),
  earlierConfirms: z.array(StageResult).optional(),
  injectionKinds: z.array(InjectionKind).optional(),
  peakRssBytes: z.number().nullable(),
});
export type CaseResult = z.infer<typeof CaseResult>;

/** The file of case `caseId`'s results under `out`. */
export function caseResultFile(out: string, caseId: string): string {
  return path.join(out, "cases", `${encodeURIComponent(caseId)}.json`);
}

/** A digest of the directory `dir`: each file's path inside it and its bytes, in path order, of
 *  the files `keep` keeps (every file by default). */
export function textDigest(dir: string, keep: (file: string) => boolean = () => true): string {
  const hash = createHash("sha256");
  const files = readdirSync(dir, { recursive: true, encoding: "utf8" })
    .filter((file) => keep(file) && statSync(path.join(dir, file)).isFile())
    .sort();
  for (const file of files) {
    hash
      .update(file)
      .update("\0")
      .update(readFileSync(path.join(dir, file)))
      .update("\0");
  }
  return hash.digest("hex").slice(0, 16);
}

/** `texts` as score.ts's MadeConfirm holds them: the old text's digest and the compared arm's in
 *  one string, `none` for no skill. */
function textsKey(texts: Texts): string {
  return `${texts.old} ${texts.new ?? texts.ours ?? "none"}`;
}

/** The runner's own code, as each stage it starts records it: textDigest of this directory less
 *  its tests (`*.test.ts`), its test data and README.md. Two confirms of one case are pooled only
 *  when one runner made both (score.ts's `confirmScore`), so a confirm made before a fix of the
 *  runner is not pooled with one made after it. */
export function runnerDigest(): string {
  return textDigest(
    import.meta.dir,
    (file) =>
      !file.endsWith(".test.ts") && file !== "README.md" && file.split(path.sep)[0] !== "testdata"
  );
}

/** Where the captures of `arm` are saved under `out`: one directory per arm and text. `texts` are
 *  the digests of `--old`, `--new` and `--ours` (`textDigest`), so only `minus`'s copy is hashed
 *  here; `twin` holds the old text. */
export function armCaptureDir(out: string, arm: Arm, texts: Texts): string {
  const dir = (text: string) => path.join(out, "captures", `${arm.name}-${text}`);
  if (arm.skillDir === undefined) return dir("none");
  if (arm.name === "old" || arm.name === "new" || arm.name === "ours") {
    return dir(texts[arm.name] ?? textDigest(arm.skillDir));
  }
  if (arm.name === "twin") return dir(texts.old);
  return dir(textDigest(arm.skillDir));
}

/** An estimate of the chance that the registered rule calls a loss in one informative case when
 *  the compared arm's text is the old text: score.ts's `lossChance` with both texts passing at p =
 *  (old + compared + 1) / (2 × SCREEN_RUNS + 2), the case's old and compared arm's screen passes
 *  pooled with a Laplace term, and no skill at q = (none + 1) / (SCREEN_RUNS + 2); with no skill
 *  compared, no skill is the compared arm, at p. */
export function falseLossChance(
  passes: { old: number; compared: number; none: number },
  compared: Compared = "new"
): number {
  const p = (passes.old + passes.compared + 1) / (2 * SCREEN_RUNS + 2);
  const q = (passes.none + 1) / (SCREEN_RUNS + 2);
  return lossChance({ old: p, compared: p, none: compared === "none" ? p : q }, compared);
}

/** The report's sensitivity table: the old text's pass rates, the drops of the cut's text from
 *  them, and the no-skill pass rate. */
const SENSITIVITY = { old: [0.9, 0.7], drops: [0.1, 0.2, 0.3, 0.4], none: 0.1 } as const;

// `behaviour`: running the stages.

/** A behaviour command's cases and where it writes. `compared` is the arm the old text is
 *  compared with (`report`'s: the one its results record). `servedModels` are the configuration's,
 *  `readingModels` the skill's reading sessions by model that `--reading-models` names, with its
 *  file, and `limits` the text `--limits` names, with its file (each undefined without its flag). */
interface Behaviour {
  readonly io: CliIo;
  readonly command: string;
  readonly out: string;
  readonly casesFile: string;
  readonly cases: readonly Case[];
  readonly compared: Compared;
  /** Whether a result made with another compared arm than `compared` is refused (`readResult`):
   *  for a stage command, and for a report given `--new` or `--ours`; a report given neither
   *  takes the arm its results record. */
  readonly checkCompared: boolean;
  readonly servedModels: readonly string[];
  readonly readingModels: { readonly file: string; readonly count: ModelCount } | undefined;
  readonly limits: { readonly file: string; readonly text: string } | undefined;
}

/** What a stage command runs with. `gateway` counts the spend; `replays`, `judge` and the capture
 *  send through it, each logged. `source` reads the context's repositories, and counts its GitHub
 *  reads. `runner` is this runner's code (`runnerDigest`), which each stage it starts records;
 *  `again` the cases `--again` names (confirm only). */
interface Runner extends Behaviour {
  readonly dirs: ArmDirs;
  readonly texts: Texts;
  readonly scratch: string;
  readonly gateway: Gateway;
  readonly replays: Gateway;
  readonly judge: Gateway;
  readonly source: RepoSource;
  capture(c: Case, arm: Arm): Promise<Capture>;
  readonly afterCase: string | undefined;
  readonly afterCaseFailures: string[];
  readonly runner: string;
  readonly again: readonly string[];
}

/** The skill names the judge reads as `the-skill` in every judgement of case `c` (blind.ts): in a
 *  run with an arm of ours, the case's skill and ours, so the judge cannot tell old from ours by
 *  a name; none in a run with `--new` (both arms hold the case's skill) or with no skill compared
 *  (the judge reads the case's own history in every arm), whose judge reads the case's text as
 *  it is. */
function blinded(r: Runner, c: Case): string[] {
  return r.dirs.ours === undefined ? [] : [c.skill, r.dirs.ours.name];
}

/** The milliseconds of the phases of one case's stage, from `startedAt` (`performance.now()`), for
 *  its line (`phaseWords`): its judge controls, each arm's capture, the context builds within them
 *  and the GitHub reads made in those (how many, and the milliseconds one ran), its replays, its
 *  judgements, and its result writes. */
interface Phases {
  readonly startedAt: number;
  controlsMs: number;
  readonly captureMs: Partial<Record<ArmName, number>>;
  contextMs: number;
  github: { readonly calls: number; readonly ms: number };
  replaysMs: number;
  judgeMs: number;
  writeMs: number;
}

/** The phases of a case's stage that starts now, none of them run yet. */
function newPhases(): Phases {
  return {
    startedAt: performance.now(),
    controlsMs: 0,
    captureMs: {},
    contextMs: 0,
    github: { calls: 0, ms: 0 },
    replaysMs: 0,
    judgeMs: 0,
    writeMs: 0,
  };
}

/** The seconds of `p`, as a case's line ends them: each phase, the rest, and the total so far. */
function phaseWords(p: Phases): string {
  const s = (ms: number) => (ms / 1000).toFixed(1);
  const total = performance.now() - p.startedAt;
  const arms = Object.entries(p.captureMs);
  const captures = arms.reduce((sum, [, ms]) => sum + ms, 0);
  const named = p.controlsMs + captures + p.replaysMs + p.judgeMs + p.writeMs;
  const each = arms.map(([arm, ms]) => `${arm} ${s(ms)}`).join(", ") || "none";
  return `seconds: judge controls ${s(p.controlsMs)}, captures ${s(captures)} (${each}; context ${s(p.contextMs)}; ${p.github.calls} GitHub reads, ${s(p.github.ms)}), replays ${s(p.replaysMs)}, judge ${s(p.judgeMs)}, write ${s(p.writeMs)}, other ${s(Math.max(0, total - named))}, total ${s(total)}`;
}

/** Writes one line of the command's progress. */
function say(b: Behaviour, line: string): void {
  b.io.out(`${b.command}: ${line}\n`);
}

/** Case `caseId`'s results under `b.out`, or undefined when it has none. Refuses a result one of
 *  whose stages was run before each run had its own session id and the arms were interleaved
 *  (its stage records no `sends`, SENDS), and, where `b.checkCompared`, one of whose stages was
 *  run with another compared arm than `b.compared`. */
function readResult(b: Behaviour, caseId: string): CaseResult | undefined {
  const file = caseResultFile(b.out, caseId);
  if (!existsSync(file)) return undefined;
  const raw = readJson(file, "the case result");
  const stages: unknown = typeof raw === "object" && raw !== null ? Reflect.get(raw, "stages") : {};
  const before = Stage.options.filter((stage) => {
    const state: unknown =
      typeof stages === "object" && stages !== null ? Reflect.get(stages, stage) : undefined;
    return typeof state === "object" && state !== null && !Object.hasOwn(state, "sends");
  });
  if (before.length > 0) {
    throw new Refused(
      `${file} holds ${before.join(" and ")} runs sent before each run had its own session id and a round's arms were interleaved, so they are no runs of this procedure: use a new --out`
    );
  }
  const parsed = CaseResult.safeParse(raw);
  if (!parsed.success) {
    throw new Refused(`${file} is not a case result: ${schemaFaults(parsed.error)}`);
  }
  for (const stage of b.checkCompared ? Stage.options : []) {
    const state = parsed.data.stages[stage];
    if (state !== undefined && recordedArm(state) !== b.compared) {
      throw new Refused(
        `${file} holds runs made with the compared arm ${recordedArm(state)}, not ${b.compared}, this command's: give the --new or --ours they were made with (neither for none), or another --out`
      );
    }
  }
  return parsed.data;
}

/** The results of each case of `b` that has some, by case id. */
function readResults(b: Behaviour): Map<string, CaseResult> {
  const results = new Map<string, CaseResult>();
  for (const c of b.cases) {
    const result = readResult(b, c.id);
    if (result !== undefined) results.set(c.id, result);
  }
  return results;
}

/** Writes `result` whole, its time counted in `phases` when given. Returns its file. */
function writeResult(b: Behaviour, result: CaseResult, phases?: Phases): string {
  const started = performance.now();
  const file = caseResultFile(b.out, result.caseId);
  writeWhole(file, json(result));
  if (phases !== undefined) phases.writeMs += performance.now() - started;
  return file;
}

/** The screen and control run records of `results`, as score.ts reads them, a dropped case's
 *  included: score.ts reads its drop from its facts (`caseFacts`), and each confirm from there. */
function runRecords(results: Iterable<CaseResult>): RunRecord[] {
  const records: RunRecord[] = [];
  for (const result of results) {
    for (const stage of ["screen", "control"] as const) {
      for (const { caseId, arm, verdict, reason } of result.stages[stage]?.runs ?? []) {
        records.push({ caseId, arm, stage, verdict, reason });
      }
    }
  }
  return records;
}

/** The served-sessions refusal of `b`'s skill (score.ts's `servedScreeningRefusal`, with
 *  `--reading-models`), which stops its session cases only: a case runs only on a served model, and
 *  a written case is a written task on a served model, not a session. Undefined without
 *  `--reading-models` or with enough reading sessions on served models. */
function sessionRefusal(b: Behaviour): NoVerdict | undefined {
  return b.readingModels === undefined ? undefined : servedScreeningRefusal(b.readingModels.count);
}

/** The served-sessions refusal (`sessionRefusal`) as the whole skill's: a skill with written cases
 *  is screened on them, so only one with none gets no verdict by it. */
function skillRefusal(b: Behaviour): NoVerdict | undefined {
  return b.cases.some((c) => caseKind(c) === "written") ? undefined : sessionRefusal(b);
}

/** The kind of each case of `b` by its id (cases.ts's `caseKind`). */
function kindsOf(b: Behaviour): (caseId: string) => CaseKind {
  const kinds = new Map(b.cases.map((c) => [c.id, caseKind(c)]));
  return (caseId) => kinds.get(caseId) ?? "session";
}

/** The run records of `b`'s `results` that its score counts (`runRecords`): while the
 *  served-sessions refusal applies (`sessionRefusal`), none of a session case whose screen is not
 *  counted or unscreened, since the screen takes no session case then (screenCommand) and a stage
 *  still running would keep the verdict waiting for ever. */
function scoredRecords(b: Behaviour, results: ReadonlyMap<string, CaseResult>): RunRecord[] {
  if (sessionRefusal(b) === undefined) return runRecords(results.values());
  const kindOf = kindsOf(b);
  return runRecords(
    [...results.values()].filter((result) => {
      const state = result.stages.screen?.state;
      return kindOf(result.caseId) !== "session" || state === "counted" || state === "unscreened";
    })
  );
}

/** The stage that dropped `result`: its `droppedAt`, or for a result written before that was
 *  recorded, the stage its runs show: screen when its screen runs are not counted, else confirm
 *  when its screen flagged it (by the compared arm the screen records) and it has no confirm runs,
 *  else control. */
function dropStage(result: CaseResult): Stage {
  if (result.droppedAt !== undefined) return result.droppedAt;
  const screen = result.stages.screen;
  const compared = screen === undefined ? "new" : recordedArm(screen);
  const outcome =
    screen === undefined ? undefined : stageOutcome(screen.runs, result.caseId, "screen", compared);
  if (outcome?.state !== "counted") return "screen";
  return isFlagged(outcome.passes, compared) && result.stages.confirm === undefined
    ? "confirm"
    : "control";
}

/** Each confirm of `result`, oldest first: its earlier ones, then its latest. */
function confirmStages(result: CaseResult): StageResult[] {
  const latest = result.stages.confirm;
  return [...(result.earlierConfirms ?? []), ...(latest === undefined ? [] : [latest])];
}

/** The drop at screen of case `c` for each block of its skill its arms cannot each rebuild
 *  (arms.ts's `injectionFaults`), or undefined when it holds none: its reason names each fault's
 *  kind, then each fault. */
function injectionDrop(c: Case): { reason: string; kinds: InjectionKind[] } | undefined {
  const faults = injectionFaults(c);
  if (faults.length === 0) return undefined;
  const kinds = [...new Set(faults.map((fault) => fault.kind))];
  const texts = faults.map((fault) => fault.text).join("; ");
  return { reason: `${INJECTION_UNREBUILDABLE}: ${kinds.join(", ")}: ${texts}`, kinds };
}

/** What score.ts reads of `b`'s cases beside their run records (CaseFacts): the drop of each case
 *  that has one, the drop its result records (`dropStage`), else its listing measure's
 *  (score.ts's `listingStanding`), else its block its arms cannot rebuild (`injectionDrop`), at
 *  screen; and each case's confirms with the runner and texts of each, old against `b.compared`. */
function caseFacts(b: Behaviour, results: ReadonlyMap<string, CaseResult>): Map<string, CaseFacts> {
  const facts = new Map<string, CaseFacts>();
  for (const c of b.cases) {
    const result = results.get(c.id);
    const listing = listingStanding(c.evidence);
    const injection = injectionDrop(c);
    const drop =
      result !== undefined && result.dropped !== null
        ? { stage: dropStage(result), reason: result.dropped }
        : listing.state === "dropped"
          ? { stage: "screen" as const, reason: listing.reason }
          : injection !== undefined
            ? { stage: "screen" as const, reason: injection.reason }
            : undefined;
    const confirms: MadeConfirm[] = (result === undefined ? [] : confirmStages(result)).map(
      (stage) => ({
        runner: stage.runner ?? null,
        texts: textsKey(stage.texts),
        outcome: stageOutcome(stage.runs, c.id, "confirm", b.compared),
      })
    );
    facts.set(c.id, { ...(drop === undefined ? {} : { drop }), confirms });
  }
  return facts;
}

/** The score of `b`'s skill from `results`, the old text compared with `b.compared` (score.ts's
 *  `scoreSkill`, of `scoredRecords` with `caseFacts`). */
function scored(b: Behaviour, results: ReadonlyMap<string, CaseResult>): SkillScore {
  return scoreSkill(scoredRecords(b, results), caseFacts(b, results), b.compared);
}

/** Why the screen of `b`'s skill is not finished, or undefined when it is: each case is dropped
 *  or has its screen counted or unscreened, or the screen takes it no more: a session case the
 *  served-sessions refusal stops (`sessionRefusal`), whether or not its screen began, as
 *  screenCommand passes it over; or a case of a kind whose screening stopped (score.ts's
 *  `screeningDone`) before it. */
function screenUnfinished(
  b: Behaviour,
  results: ReadonlyMap<string, CaseResult>,
  score: SkillScore
): string | undefined {
  const counts = screenCounts(score, kindsOf(b));
  const refused = sessionRefusal(b) !== undefined;
  const dropped = new Set(
    score.cases.flatMap((each) => (each.drop === undefined ? [] : [each.caseId]))
  );
  for (const c of screenOrder(b.cases, caseKind)) {
    if (dropped.has(c.id)) continue;
    const result = results.get(c.id);
    const state = result?.stages.screen?.state;
    if (state === "counted" || state === "unscreened") continue;
    const kind = caseKind(c);
    if ((kind === "session" && refused) || (result === undefined && screeningDone(counts, kind)))
      continue;
    return `case ${c.id} is not screened yet`;
  }
  return undefined;
}

/** The results of `b`'s cases that `keep` names of the skill's score, and the score, once its
 *  screen is finished: the other cases' results are read for the score only. Throws a refusal
 *  before that. */
function screened(
  b: Behaviour,
  keep: (score: SkillScore) => readonly (string | undefined)[]
): { results: Map<string, CaseResult>; score: SkillScore } {
  const results = readResults(b);
  const score = scored(b, results);
  const unfinished = screenUnfinished(b, results, score);
  if (unfinished !== undefined) {
    throw new Refused(`the screen is not finished: ${unfinished}; run behaviour screen first`);
  }
  const kept = new Set(keep(score));
  for (const id of results.keys()) if (!kept.has(id)) results.delete(id);
  return { results, score };
}

/** Why a skill whose screen is finished can get no verdict, whatever its confirm runs show, or
 *  undefined: too few informative cases; no informative case left to be the control case, each
 *  one dropped; or a control whose runs are made and whose planted-loss control is no loss or
 *  whose false-loss control is a loss (score.ts's `skillVerdict`, and its reason). Exported for
 *  cli.test.ts. */
export function noVerdict(score: SkillScore): string | undefined {
  const control = score.control;
  const counted = control?.state === "counted" ? control.passes : undefined;
  const controlLoss =
    control === undefined ||
    control.state === "running" ||
    (counted !== undefined && isControlLoss(counted));
  const falseLoss = counted !== undefined && isFalseLoss(counted);
  const outcome = skillVerdict({
    informative: score.informative,
    controlLoss,
    falseLoss,
    losses: 0,
  });
  if (outcome.verdict !== null) return score.noControl;
  return reasonWords(outcome.reason, control, null);
}

/** A reason of score.ts's (`skillVerdict`) as the runner says it. Where the planted-loss
 *  control's runs are unscreened, its reason (NO_CONTROL_LOSS) says so, since no deletion was
 *  measured; else it names the line `minus` left out: `plantedLine` where one was given. */
function reasonWords(
  reason: string,
  control: StageOutcome<"control"> | undefined,
  plantedLine: string | null
): string {
  if (control?.state === "unscreened") {
    return reason.replace(
      NO_CONTROL_LOSS,
      "planted-loss control not a loss: its runs are unscreened"
    );
  }
  return plantedLine === null
    ? reason
    : reason.replace(
        NO_CONTROL_LOSS,
        noControlLoss(`the planted line ${JSON.stringify(plantedLine)}`)
      );
}

/** The drop of a case whose model the gateway does not serve, as its drop begins and the report
 *  counts it. */
const MODEL_NOT_SERVED = "model-not-served";

/** The model of `servedModels` (the configuration's, each `provider/model`) that serves `model`:
 *  the one whose id, its text after its last `/`, is `model`'s; or undefined when none is. */
function servedModel(model: string, servedModels: readonly string[]): string | undefined {
  return servedModels.find((served) => modelId(served) === modelId(model));
}

/** Why the gateway runs nothing on the case model `model`: no model of `servedModels` (the
 *  configuration's) serves it. */
function notServedWords(model: string, servedModels: readonly string[]): string {
  return `its model ${model} is none the gateway serves (the configuration's servedModels: ${servedModels.join(", ") || "none"})`;
}

/** The directory under `--out` that keeps each session head a capture read (capture.ts's
 *  `sessionHeads`), one file per store and path hash. */
const SESSIONS_DIR = "sessions";

/**
 * What a capture of case `c` shows of the session it stands for (capture.ts's `sessionFacts`):
 * the session's head, read from its store (`c.source`) through the configuration's
 * `psqlCommand` once and saved under `<out>/sessions/`; the reminder Oh My Pi journaled for the
 * case's first message that goes to the model as a user turn (any but an assistant message or a
 * tool result), the turn a capture's reminder goes on, else one rendered for the UTC date of the
 * case's step (of its session's start when the step has no time). Throws when the
 * configuration names no such store or the head cannot be read: no capture of a session the
 * store holds is made with neutral values in place of a read that failed.
 */
async function caseSession(config: SkillCutsConfig, out: string, c: Case): Promise<SessionFacts> {
  const store = storeNamed(config, c.source.store).name;
  const heads = await sessionHeads(
    config.psqlCommand,
    store,
    [c.source.pathHash],
    path.join(out, SESSIONS_DIR)
  );
  const first = c.messages.find(
    (message) => message.role !== "assistant" && message.role !== "toolResult"
  );
  const turnAt: unknown = first?.timestamp;
  const attribution: unknown = first?.attribution;
  const stepAt: unknown = c.realStep.timestamp;
  return sessionFacts(heads.get(c.source.pathHash), {
    turn:
      typeof turnAt === "number"
        ? userTurnKey({
            timestamp: turnAt,
            attribution: typeof attribution === "string" ? attribution : undefined,
          })
        : undefined,
    date:
      typeof stepAt === "number"
        ? {
            value: new Date(stepAt).toISOString().slice(0, 10),
            from: "the UTC date of the case's step in the session",
          }
        : {
            value: new Date(c.sessionStart).toISOString().slice(0, 10),
            from: "the UTC date of the session's start (the case's step has no time)",
          },
    devbox: c.context === "devbox",
    devboxClaudeConfigDir: config.devboxClaudeConfigDir,
  });
}

/** A fresh result of case `c`. */
function newResult(c: Case): CaseResult {
  return {
    caseId: c.id,
    skill: c.skill,
    cut: c.cut,
    dropped: null,
    controls: null,
    stages: {},
    peakRssBytes: null,
  };
}

/** The texts of `texts` in words: `old <digest>`, then `new <digest>` or `ours <digest>`. */
function textWords(texts: Texts): string {
  return [
    `old ${texts.old}`,
    ...(texts.new === undefined ? [] : [`new ${texts.new}`]),
    ...(texts.ours === undefined ? [] : [`ours ${texts.ours}`]),
  ].join(", ");
}

/** Refuses a stage of case `caseId` that was run with other skill texts or another planted line
 *  than this run's: its runs and this run's would be of different arms. */
function checkSameTexts(r: Runner, caseId: string, stage: Stage, existing: StageResult): void {
  const plantedLine = stage === "control" ? (r.dirs.plantedLine ?? null) : null;
  if (
    existing.texts.old === r.texts.old &&
    existing.texts.new === r.texts.new &&
    existing.texts.ours === r.texts.ours &&
    existing.plantedLine === plantedLine
  ) {
    return;
  }
  throw new Refused(
    `case ${caseId}'s ${stage} runs in ${caseResultFile(r.out, caseId)} were made with other skill texts (${textWords(existing.texts)}, planted line ${JSON.stringify(existing.plantedLine)}) than this run's (${textWords(r.texts)}, planted line ${JSON.stringify(plantedLine)}): use the same --old, --new or --ours and --planted-line, or another --out`
  );
}

/** Whether case `result`'s `stage` is complete, refusing one made with other texts. */
function stageDone(r: Runner, result: CaseResult | undefined, stage: Stage): boolean {
  const existing = result?.stages[stage];
  if (result === undefined || existing === undefined) return false;
  checkSameTexts(r, result.caseId, stage, existing);
  return existing.state !== "running";
}

/** The judge controls of case `c`, kept in `result`, which is dropped when they do not hold: its
 *  drop names each control it failed. */
async function judgeTheControls(r: Runner, c: Case, result: CaseResult): Promise<CaseResult> {
  const before = r.gateway.spend().usd;
  const controls = await judgeControls(c, r.judge, blinded(r, c));
  const judged = (j: Judged) => ({ verdict: j.verdict, reason: j.reason });
  const next: CaseResult = {
    ...result,
    controls: {
      realStep: judged(controls.realStep),
      violating: judged(controls.violating),
      nothing: judged(controls.nothing),
      failed: controls.failed,
      hold: controls.hold,
      usd: r.gateway.spend().usd - before,
    },
  };
  if (controls.hold) return next;
  const failures = controls.failed.map(
    (name) =>
      `${CONTROLS[name].step} is judged ${controls[name].verdict ?? "not at all"} (${CONTROLS[name].wanted} wanted)`
  );
  return {
    ...next,
    dropped: `the judge controls do not hold: ${failures.join("; ")}`,
    droppedAt: "screen",
  };
}

/** Round `round` of a stage: `toStart` replays of each arm, every arm's sent in one order under
 *  `seed` (replay.ts's `replayInterleaved`; the gateway bounds the calls in flight), each run with
 *  a session id of its own; then every reply judged blind under `seed`. A replay that fails is a
 *  run with no verdict; one the budget refused, or whose judgement the budget refused, is no run.
 *  The runs are numbered, within their arm, in the order they were sent. */
async function runRound(
  r: Runner,
  c: Case,
  stage: Stage,
  arms: ReadonlyMap<ArmName, { arm: Arm; capture: Capture }>,
  toStart: Partial<Record<ArmName, number>>,
  made: readonly Run[],
  round: number,
  seed: string,
  phases: Phases
): Promise<{ runs: Run[]; budgetSpent: boolean }> {
  const replaysStarted = performance.now();
  const sent = await replayInterleaved(
    [...arms].map(([name, { arm, capture }]) => ({
      arm,
      body: capture.body,
      headers: capture.record.requestHeaders,
      runs: toStart[name] ?? 0,
    })),
    r.replays,
    seed
  );
  // Each reply is judged under its arm and its place among its arm's replays of this round.
  const replies: ArmReply[] = sent.flatMap(({ arm, run, outcome }) =>
    outcome.status === "fulfilled"
      ? [{ arm, run, content: outcome.value.step.message.content }]
      : []
  );
  const judgeStarted = performance.now();
  phases.replaysMs += judgeStarted - replaysStarted;
  const names = blinded(r, c);
  const judged =
    replies.length === 0 ? [] : (await judgeBlind(c, replies, r.judge, seed, names)).judged;
  phases.judgeMs += performance.now() - judgeStarted;
  const judgements = new Map(judged.map((j) => [`${j.arm}:${j.run}`, j]));
  let budgetSpent = false;
  const runs: Run[] = [];
  const numbered = new Map<ArmName, number>();
  for (const { arm, run: inArm, place: sendPlace, userId, outcome } of sent) {
    const base = { caseId: c.id, arm, stage, round, sendPlace, userId };
    const next = () => {
      const run = (numbered.get(arm) ?? made.filter((each) => each.arm === arm).length) + 1;
      numbered.set(arm, run);
      return run;
    };
    if (outcome.status === "rejected") {
      if (outcome.reason instanceof BudgetSpent) {
        budgetSpent = true;
        continue;
      }
      runs.push({
        ...base,
        run: next(),
        verdict: null,
        reason: `the replay failed: ${messageFor(outcome.reason)}`,
        id: null,
        skillRounds: null,
        reply: null,
        replaySpent: [],
        judgeSpent: [],
      });
      continue;
    }
    const judgement = judgements.get(`${arm}:${inArm}`);
    if (judgement === undefined || judgement.budgetRefused) {
      budgetSpent = true;
      continue;
    }
    const step: Replayed = outcome.value;
    runs.push({
      ...base,
      run: next(),
      verdict: judgement.verdict,
      reason: judgement.reason,
      id: judgement.id,
      skillRounds: step.rounds,
      reply: step.step.message.content,
      replaySpent: step.usage,
      judgeSpent: [...judgement.spent],
    });
  }
  return { runs, budgetSpent };
}

/**
 * Runs `stage` of case `c` until score.ts's `stageOutcome` says its runs are counted or
 * unscreened, round by round, from the runs `result` holds already (which each caller's
 * `stageDone` has checked were made with this run's texts). Each round's runs are saved when it
 * ends; the stage's last state is returned unsaved, for `finishCase` to write. Each phase's time
 * is counted in `phases`. A case whose context holds a directory no read can give (context.ts's
 * UnreadableDirectory: the commit holds no such directory, or GitHub cuts a level of it short
 * even read alone) is returned dropped, with that reason and `stage`, before any run, so a run
 * started again skips it. A stage it starts records this runner (`r.runner`). Throws Stopped,
 * after saving, when the budget refused a call.
 */
async function runStage(
  r: Runner,
  c: Case,
  stage: Stage,
  result: CaseResult,
  phases: Phases
): Promise<CaseResult> {
  const existing = result.stages[stage];
  let state: StageResult = existing ?? {
    state: "running",
    sends: SENDS,
    texts: r.texts,
    compared: r.compared,
    plantedLine: stage === "control" ? (r.dirs.plantedLine ?? null) : null,
    judge: judgeIdentity(),
    captures: {},
    seeds: [],
    runs: [],
    usd: 0,
    runner: r.runner,
  };
  let spentAt = r.gateway.spend().usd;
  let current = result;
  const update = (next: StageResult): void => {
    const now = r.gateway.spend().usd;
    state = { ...next, usd: next.usd + now - spentAt };
    spentAt = now;
    current = { ...current, stages: { ...current.stages, [stage]: state } };
  };
  const save = (next: StageResult): string => {
    update(next);
    return writeResult(r, current, phases);
  };

  const scratch = path.join(
    r.scratch,
    "arms",
    createHash("sha256").update(c.id).digest("hex").slice(0, 16)
  );
  const arms = new Map<ArmName, { arm: Arm; capture: Capture }>();
  const captures = { ...state.captures };
  let ours: OursNote | undefined;
  const readsBefore = r.source.reads?.() ?? { calls: 0, ms: 0 };
  // The GitHub reads the captures made, counted into `phases`.
  const countReads = (): void => {
    const readsAfter = r.source.reads?.() ?? { calls: 0, ms: 0 };
    phases.github = {
      calls: readsAfter.calls - readsBefore.calls,
      ms: readsAfter.ms - readsBefore.ms,
    };
  };
  for (const name of stageArms(stage, r.compared)) {
    const started = performance.now();
    const arm = buildArm(c, name, r.dirs, scratch);
    if (arm.ours !== undefined) {
      const { replacedOwn, removedReads } = arm.ours;
      ours = { name: arm.skill, replacedOwn, removedReads: [...removedReads] };
    }
    let capture: Capture;
    try {
      capture = await r.capture(c, arm);
    } catch (error) {
      // An InjectionRefused with no kind is buildArm's, whose faults screen drops before any
      // capture (`injectionDrop`): it fails the command as any other error.
      const dropped =
        error instanceof InjectionRefused && error.kind !== undefined
          ? {
              dropped: `${INJECTION_UNREBUILDABLE}: ${error.kind}: ${error.message}`,
              injectionKinds: [error.kind],
            }
          : error instanceof UnreadableDirectory
            ? { dropped: `${UNREADABLE_DIRECTORY}: ${error.message}` }
            : undefined;
      if (dropped === undefined) throw error;
      phases.captureMs[name] = performance.now() - started;
      countReads();
      return { ...current, ...dropped, droppedAt: stage };
    }
    phases.captureMs[name] = performance.now() - started;
    if (capture.made) phases.contextMs += capture.record.timings.contextMs;
    // Only the capture reads the arm's messages: the replays and the judge need none.
    arms.set(name, { arm: { ...arm, messages: [] }, capture });
    captures[name] = {
      bodyFile: path.relative(r.out, capture.bodyFile),
      captureDigest: capture.record.captureDigest ?? null,
      sessionDigest: c.sessionDigest,
      // A capture saved before the record kept its blocks holds no `injections`.
      ...((capture.record.injections ?? []).some((each) => each.by === "plugin") &&
      capture.record.dispatch
        ? { dispatch: capture.record.dispatch.from }
        : {}),
    };
  }
  countReads();
  save({ ...state, captures, ...(ours === undefined ? {} : { ours }) });

  for (;;) {
    const outcome = stageOutcome(state.runs, c.id, stage, r.compared);
    if (outcome.state !== "running") {
      update({ ...state, state: outcome.state });
      return current;
    }
    const seed = newSeed();
    const round = await runRound(
      r,
      c,
      stage,
      arms,
      outcome.toStart,
      state.runs,
      state.seeds.length + 1,
      seed,
      phases
    );
    const file = save({
      ...state,
      seeds: [...state.seeds, seed],
      runs: [...state.runs, ...round.runs],
    });
    if (round.budgetSpent) {
      throw new Stopped(
        `the budget is spent: the replies so far cost $${r.gateway.spend().usd.toFixed(4)} at list price, so no new model call starts; case ${c.id}'s ${stage} runs so far are saved in ${file}`
      );
    }
  }
}

/** Ends case `result`'s stage: records the peak memory, writes the result, says the case's line
 *  (its stage, the peak and the seconds of each phase of `phases`), runs `--after-case`, and stops
 *  the run when the peak is above RUN_MEMORY_LIMIT_BYTES. Returns the result as written. */
async function finishCase(
  r: Runner,
  result: CaseResult,
  stage: Stage,
  phases: Phases
): Promise<CaseResult> {
  const peak = Math.max(r.io.peakRssBytes(), result.peakRssBytes ?? 0);
  const written = { ...result, peakRssBytes: peak };
  const file = writeResult(r, written, phases);
  const what =
    result.dropped === null
      ? stageLine(result.caseId, stage, result.stages[stage])
      : `dropped: ${result.dropped}`;
  say(
    r,
    `case ${result.caseId}: ${what}; peak memory ${mib(peak)} MiB; ${phaseWords(phases)}; written to ${file}`
  );
  if (r.afterCase !== undefined) {
    const child = Bun.spawn(["sh", "-c", `${r.afterCase} "$1"`, "after-case", file], {
      stdin: "ignore",
      stdout: "inherit",
      stderr: "inherit",
    });
    const code = await child.exited;
    if (code !== 0) {
      r.io.err(`${r.command}: the --after-case command exited ${code} for ${file}`);
      r.afterCaseFailures.push(file);
    }
  }
  if (peak > RUN_MEMORY_LIMIT_BYTES) {
    throw new Stopped(
      `the runner's peak resident memory, ${mib(peak)} MiB, is above 1 GiB, so the run stops after case ${result.caseId}, whose results are written to ${file}`
    );
  }
  return written;
}

/** `behaviour screen`. */
async function screenCommand(r: Runner): Promise<void> {
  const compared =
    r.compared === "none" ? "compared with no skill" : `${r.compared} text ${r.texts[r.compared]}`;
  say(
    r,
    `${r.cases.length} cases of the skill ${r.cases[0]?.skill} in ${r.casesFile}; old text ${r.texts.old}, ${compared}`
  );
  // Each case's result, read once here and kept as each case ends (only this process writes
  // <out>/cases/), for the score that says when screening stops.
  const results = readResults(r);
  // The score, computed again only once a case has ended: a case passed over changes nothing.
  let scoreSoFar: SkillScore | undefined;
  const finished = async (result: CaseResult, phases: Phases): Promise<void> => {
    results.set(result.caseId, await finishCase(r, result, "screen", phases));
    scoreSoFar = undefined;
  };
  // The served-sessions refusal stops the session cases only; the setup refused a skill with no
  // written case before any call.
  const refused = sessionRefusal(r);
  if (refused !== undefined) {
    say(
      r,
      `session cases are not screened: ${refused.reason} (--reading-models ${r.readingModels?.file}); the written cases are`
    );
  }
  const kindOf = kindsOf(r);
  const stopped = new Set<CaseKind>();
  // Session cases first, then written ones: up to MAX_SCREENED_CASES screened cases of each kind,
  // until the skill has MIN_INFORMATIVE_CASES informative cases in all.
  for (const c of screenOrder(r.cases, caseKind)) {
    const kind = caseKind(c);
    if (kind === "session" && refused !== undefined) {
      stopped.add(kind);
      continue;
    }
    const phases = newPhases();
    let result = readResult(r, c.id);
    if (result !== undefined && result.dropped !== null) {
      say(r, `case ${c.id}: dropped before, skipped: ${result.dropped}`);
      continue;
    }
    if (stageDone(r, result, "screen")) {
      say(r, `case ${c.id}: screened before, skipped`);
      continue;
    }
    if (result === undefined) {
      scoreSoFar ??= scored(r, results);
      const score = scoreSoFar;
      const counts = screenCounts(score, kindOf);
      if (counts.informative >= MIN_INFORMATIVE_CASES) {
        say(
          r,
          `screening stops before case ${c.id}: ${score.informative} informative and ${score.screened} screened cases (it stops at ${MIN_INFORMATIVE_CASES} informative in all, or at ${MAX_SCREENED_CASES} screened cases of each kind)`
        );
        return;
      }
      if (screeningDone(counts, kind)) {
        if (!stopped.has(kind)) {
          say(
            r,
            `screening of ${kind} cases stops before case ${c.id}: ${counts.screened[kind]} ${kind} cases screened, ${score.informative} informative in all (it stops at ${MAX_SCREENED_CASES} screened cases of each kind)`
          );
        }
        stopped.add(kind);
        continue;
      }
      result = newResult(c);
      if (servedModel(c.model, r.servedModels) === undefined) {
        const dropped = `${MODEL_NOT_SERVED}: ${notServedWords(c.model, r.servedModels)}`;
        await finished({ ...result, dropped, droppedAt: "screen" }, phases);
        continue;
      }
    }
    const listing = listingStanding(c.evidence);
    if (listing.state === "dropped") {
      await finished({ ...result, dropped: listing.reason, droppedAt: "screen" }, phases);
      continue;
    }
    const injection = injectionDrop(c);
    if (injection !== undefined) {
      const { reason: dropped, kinds: injectionKinds } = injection;
      await finished({ ...result, dropped, droppedAt: "screen", injectionKinds }, phases);
      continue;
    }
    if (result.controls === null) {
      const controlsStarted = performance.now();
      result = await judgeTheControls(r, c, result);
      phases.controlsMs = performance.now() - controlsStarted;
      if (result.dropped !== null) {
        await finished(result, phases);
        continue;
      }
      writeResult(r, result, phases);
    }
    await finished(await runStage(r, c, "screen", result, phases), phases);
  }
  say(
    r,
    stopped.size === 0
      ? "every case of the file is screened"
      : `the screen is done: it takes no more ${[...stopped].join(" or ")} cases`
  );
}

/** Why `--again <id>` is refused, or undefined: the file holds no such case, or it is dropped, is
 *  not flagged, or has no finished confirm; or it has POOLED_CONFIRMS confirms at this run's
 *  texts on this runner, which the rule pools and after which no confirm runs, or two there that
 *  agree, where the rule runs no third. */
function againRefusal(
  r: Runner,
  score: SkillScore,
  result: CaseResult | undefined,
  id: string
): string | undefined {
  if (!r.cases.some((c) => c.id === id)) {
    return `--again ${id}: ${r.casesFile} holds no case ${id}`;
  }
  const each = score.cases.find((c) => c.caseId === id);
  if (each?.drop !== undefined) {
    return `--again ${id}: case ${id} is dropped at ${each.drop.stage} (${each.drop.reason}), so no confirm of it runs`;
  }
  if (each === undefined || !each.flagged || result === undefined) {
    return `--again ${id}: case ${id} is not flagged by its screen, and only flagged cases are confirmed`;
  }
  const latest = result.stages.confirm;
  if (latest === undefined || latest.state === "running") {
    return `--again ${id}: case ${id} has no finished confirm yet: run behaviour confirm without --again first`;
  }
  const texts = textsKey(r.texts);
  const losses = confirmStages(result).flatMap((stage) => {
    if (stage.runner !== r.runner || textsKey(stage.texts) !== texts) return [];
    const outcome = stageOutcome(stage.runs, id, "confirm", r.compared);
    return outcome.state === "counted" ? [isLoss(outcome.passes, r.compared)] : [];
  });
  if (losses.length >= POOLED_CONFIRMS) {
    return `--again ${id}: case ${id} has ${losses.length} confirms at these texts on this runner, which the rule pools, and no fourth confirm runs`;
  }
  if (losses.length === 2 && losses[0] === losses[1]) {
    return `--again ${id}: case ${id}'s two confirms at these texts on this runner agree (${losses[0] ? "both a loss" : "both no loss"}), and a third confirm runs only where two disagree`;
  }
  return undefined;
}

/** `behaviour confirm`: a confirm of each flagged case that is not dropped and has none; a third
 *  of each whose last two confirms at its texts on one runner disagree (score.ts's
 *  `confirmScore`); and one more of each case `--again` names, where `againRefusal` allows it. */
async function confirmCommand(r: Runner): Promise<void> {
  const { results, score } = screened(r, (s) =>
    s.cases.filter((each) => each.flagged).map((each) => each.caseId)
  );
  const refused = r.again.flatMap((id) => againRefusal(r, score, results.get(id), id) ?? []);
  if (refused.length > 0) throw new Refused(refused.join("; "));
  const why = noVerdict(score);
  if (why !== undefined) {
    say(r, `no confirm runs: the skill gets no verdict: ${why}`);
    return;
  }
  const flagged = score.cases.filter((each) => each.flagged);
  const ids = flagged.map((each) => each.caseId);
  say(r, `${ids.length} flagged cases${ids.length === 0 ? "" : `: ${ids.join(", ")}`}`);
  for (const { caseId: id, drop, confirm } of flagged) {
    if (drop !== undefined) {
      say(r, `case ${id}: dropped at ${drop.stage}, so no confirm of it runs: ${drop.reason}`);
      continue;
    }
    const phases = newPhases();
    const c = r.cases.find((each) => each.id === id);
    let result = results.get(id);
    if (c === undefined || result === undefined) throw new Error(`no case ${id} in ${r.casesFile}`);
    if (stageDone(r, result, "confirm")) {
      const third = confirm?.state === "running" && confirm.third;
      if (!third && !r.again.includes(id)) {
        say(r, `case ${id}: confirmed before, skipped`);
        continue;
      }
      const { confirm: _latest, ...stages } = result.stages;
      result = { ...result, stages, earlierConfirms: confirmStages(result) };
      say(
        r,
        third
          ? `case ${id}: its last two confirms at these texts on one runner disagree, one a loss and one not, so a third confirm runs, and the three are pooled`
          : `case ${id}: one more confirm runs, as --again asks`
      );
    }
    await finishCase(r, await runStage(r, c, "confirm", result, phases), "confirm", phases);
  }
}

/** `behaviour confirm --case <id>`: FRESH_RUNS fresh runs of old and of the compared arm on case
 *  `c`, with no flag gate and no gate of MIN_INFORMATIVE_CASES informative cases, scored by the
 *  confirm rule; no screen and no judge controls run. */
async function confirmCaseCommand(r: Runner, c: Case): Promise<void> {
  say(
    r,
    `case ${c.id}: ${FRESH_RUNS} fresh runs of old and of ${r.compared}, with no flag gate and no gate of ${MIN_INFORMATIVE_CASES} informative cases`
  );
  const phases = newPhases();
  const result = readResult(r, c.id) ?? newResult(c);
  if (result.dropped !== null) {
    say(r, `case ${c.id}: dropped before, skipped: ${result.dropped}`);
    return;
  }
  if (stageDone(r, result, "confirm")) {
    say(r, `case ${c.id}: confirmed before, skipped`);
    return;
  }
  await finishCase(r, await runStage(r, c, "confirm", result, phases), "confirm", phases);
}

/** `behaviour control`. */
async function controlStageCommand(r: Runner): Promise<void> {
  const { results, score } = screened(r, (s) => [s.controlCase]);
  const tooFew = skillVerdict({
    informative: score.informative,
    controlLoss: true,
    falseLoss: false,
    losses: 0,
  });
  if (tooFew.verdict === null) {
    say(r, `no control runs: the skill gets no verdict: ${tooFew.reason}`);
    return;
  }
  if (score.noControl !== undefined) {
    say(r, `no control runs: the skill gets no verdict: ${score.noControl}`);
    return;
  }
  // The control case's own work starts here: reading and scoring every case's result is no part
  // of its stage.
  const phases = newPhases();
  const id = score.controlCase;
  const c = r.cases.find((each) => each.id === id);
  const result = id === undefined ? undefined : results.get(id);
  if (c === undefined || result === undefined) throw new Error(`no control case ${id}`);
  const planted = r.dirs.plantedLine;
  say(
    r,
    `the control case is ${c.id}${replacedWords(score)}; minus leaves out ${planted === undefined ? "the case's line" : `the planted line ${JSON.stringify(planted)}`}, and twin is the old text again (the false-loss control); old, minus and twin are sent in one order each round`
  );
  if (stageDone(r, result, "control")) {
    say(r, `case ${c.id}: control runs made before, skipped`);
    return;
  }
  await finishCase(r, await runStage(r, c, "control", result, phases), "control", phases);
}

// `behaviour report`.

/** What a stage's counted passes say, the old text compared with `compared`. */
function stageVerdict(stage: Stage, passes: Passes<Stage>, compared: Compared): string {
  switch (stage) {
    case "screen":
      if (!isInformative(passes)) return "not informative";
      return isFlagged(passes, compared) ? "informative, flagged" : "informative, not flagged";
    case "confirm":
      return isLoss(passes, compared) ? "a loss" : "no loss";
    case "control":
      return `the planted-loss control (old against minus) is ${isControlLoss(passes) ? "a loss" : "no loss"}, the false-loss control (old against twin) is ${isFalseLoss(passes) ? "a loss" : "no loss"}`;
  }
}

/** Case `caseId`'s stage `state` of `stage` as one line, under `label`: each arm's passes and what
 *  they say, by the compared arm the stage records. */
function stageLine(
  caseId: string,
  stage: Stage,
  state: StageResult | undefined,
  label: string = stage
): string {
  if (state === undefined) return `${label}: not run`;
  const compared = recordedArm(state);
  const outcome = stageOutcome(state.runs, caseId, stage, compared);
  const unjudged = state.runs.filter((run) => run.verdict === null).length;
  const extra = unjudged === 0 ? "" : ` (${unjudged} runs with no verdict, run again)`;
  switch (outcome.state) {
    case "counted": {
      const passes = Object.entries(outcome.passes)
        .map(([arm, count]) => `${arm} ${count}/${STAGE_RUNS[stage]}`)
        .join(", ");
      return `${label}: ${passes}: ${stageVerdict(stage, outcome.passes, compared)}${extra}`;
    }
    case "unscreened":
      return `${label}: unscreened: an arm lacks its ${STAGE_RUNS[stage]} judged runs after ${MAX_EXTRA_RUNS} extra runs${extra}`;
    case "running": {
      const toStart = Object.entries(outcome.toStart)
        .map(([arm, count]) => `${arm} ${count}`)
        .join(", ");
      return `${label}: not finished, still to start: ${toStart}`;
    }
  }
}

/** A case's pooled confirms (score.ts's `confirmScore`, counted with `pooled`) in words: the
 *  pooled passes of old and of the compared arm `compared`, the gap, and whether it is a loss. */
function pooledWords(pooled: Passes<"confirm">, loss: boolean, compared: Compared): string {
  const runs = FRESH_RUNS * POOLED_CONFIRMS;
  const other = pooled[compared] ?? 0;
  return `old ${pooled.old}/${runs}, ${compared} ${other}/${runs}, ${pooled.old - other} fewer: ${loss ? "a loss" : "no loss"}`;
}

/** Case `result`'s stages as its report line gives them (`stageLine`), each stage it ran; a case
 *  with more than one confirm gives each numbered, those on another runner or at other texts than
 *  its last marked as not counted, then their pooled passes, or the third confirm they call for
 *  (`score`, score.ts's `confirmScore`). */
function caseStageLines(result: CaseResult, score: CaseScore | undefined): string[] {
  const lines: string[] = [];
  const confirms = confirmStages(result);
  const last = confirms.at(-1);
  for (const stage of Stage.options) {
    if (stage !== "confirm" || confirms.length < 2 || last === undefined) {
      const state = stage === "confirm" ? last : result.stages[stage];
      if (state !== undefined) lines.push(stageLine(result.caseId, stage, state));
      continue;
    }
    for (const [n, each] of confirms.entries()) {
      const note =
        each.runner !== last.runner
          ? " (another runner's, not counted)"
          : textsKey(each.texts) !== textsKey(last.texts)
            ? " (at other texts, not counted)"
            : "";
      lines.push(stageLine(result.caseId, stage, each, `confirm ${n + 1}${note}`));
    }
    const confirm = score?.confirm;
    if (confirm?.state === "counted" && confirm.pooled !== undefined) {
      lines.push(`pooled: ${pooledWords(confirm.pooled, confirm.loss, recordedArm(last))}`);
    } else if (confirm?.state === "running" && confirm.third) {
      lines.push("a third confirm is due, since two on one runner disagree (behaviour confirm)");
    }
  }
  return lines;
}

/** The words after a control case that name each dropped case it took the place of, which the
 *  control rule chose before it, or none. */
function replacedWords(score: SkillScore): string {
  if (score.replacedControls.length === 0) return "";
  const named = score.replacedControls.map(
    (id) => `${id} (dropped at ${score.cases.find((each) => each.caseId === id)?.drop?.stage})`
  );
  return `, in place of ${named.join(", ")}, which the control rule chose before it`;
}

/** `count` with `word`, plural unless the count is 1: `1 session`, `2 sessions`. */
const counted = (count: number, word: string) => `${count} ${word}${count === 1 ? "" : "s"}`;

/** The served models the screened cases of `kind` of `score` ran on (counted, and not dropped at
 *  screen), each case on the served model of its model's id (`servedModel`), with its screened
 *  cases, most first, a tie by model. */
function screenedModels(
  b: Behaviour,
  score: SkillScore,
  kind: CaseKind
): { model: string; cases: number }[] {
  const cases = new Map<string, number>();
  for (const each of score.cases) {
    const c = b.cases.find((candidate) => candidate.id === each.caseId);
    if (c === undefined || each.screen.state !== "counted" || each.drop?.stage === "screen")
      continue;
    if (caseKind(c) !== kind) continue;
    const model = servedModel(c.model, b.servedModels) ?? c.model;
    cases.set(model, (cases.get(model) ?? 0) + 1);
  }
  return [...cases]
    .map(([model, count]) => ({ model, cases: count }))
    .sort((x, y) => y.cases - x.cases || x.model.localeCompare(y.model));
}

/** The report's line of the screened written cases by the served model each ran on: written
 *  tasks in a recorded session's context, counted apart and never as reading sessions. */
function writtenTasksLine(b: Behaviour, score: SkillScore): string {
  const ran = screenedModels(b, score, "written");
  if (ran.length === 0) return "written tasks: none screened";
  return `written tasks: ${ran.map((item) => `${item.cases} on ${item.model}`).join(", ")} (screened written cases: each a written task in a recorded session's context, never a reading session)`;
}

/** The report's lines on the skill's reading sessions by model (`--reading-models`): each model
 *  of the file with its sessions and whether the gateway serves it, the sessions with no model and
 *  not queried, and the served models the screened session cases ran on; or one line saying they
 *  were not
 *  given. */
function modelLines(b: Behaviour, score: SkillScore): string[] {
  if (b.readingModels === undefined) {
    return [
      "reading sessions by model: not given (no --reading-models), so the verdict names no models",
    ];
  }
  const { file, count } = b.readingModels;
  const ran = screenedModels(b, score, "session")
    .map((item) => `${item.model} ${counted(item.cases, "case")}`)
    .join(", ");
  return [
    `reading sessions by model (--reading-models ${file}): ${counted(count.sessions, "session")}`,
    ...count.byModel.map(
      (item) =>
        `  ${item.model}: ${counted(item.sessions, "session")}, ${item.served ? "served" : "not served"} by the gateway`
    ),
    `  with no model (its file's head names none): ${count.noModel}; not queried (in no models query's rows): ${count.notQueried}`,
    `screened session cases by the served model they ran on: ${ran || "none"}`,
  ];
}

/** The end of a verdict's line with `--reading-models`: the reading sessions the verdict speaks
 *  for, on each served model its screened session cases ran on (a served model of the file counted
 *  under the one of its id), and those it does not: the sessions on models the gateway does not
 *  serve, and any on served models no screened session case ran on. A written case is a written
 *  task, never a reading session, so a skill whose screened cases are all written speaks for no
 *  reading sessions. Empty without `--reading-models`. */
function speaksFor(b: Behaviour, score: SkillScore): string {
  const count = b.readingModels?.count;
  if (count === undefined) return "";
  const spoken = screenedModels(b, score, "session").map(({ model }) => ({
    model,
    id: modelId(model),
    sessions: 0,
  }));
  if (spoken.length === 0) {
    return "; it speaks for no reading sessions: its screened cases are all written tasks, never reading sessions";
  }
  let unserved = 0;
  let otherServed = 0;
  for (const item of count.byModel) {
    const ran = item.served ? spoken.find((each) => each.id === modelId(item.model)) : undefined;
    if (ran !== undefined) ran.sessions += item.sessions;
    else if (item.served) otherServed += item.sessions;
    else unserved += item.sessions;
  }
  const models = spoken
    .map((each) => `${each.model} (${counted(each.sessions, "session")})`)
    .join(", ");
  return (
    `; it speaks for the reading sessions on ${models}; not for the ${counted(unserved, "session")} on models the gateway does not serve` +
    (otherServed === 0
      ? ""
      : `; nor for the ${counted(otherServed, "session")} on served models no screened session case ran on`)
  );
}

/** Where the answer that each case's session had Dispatch comes from, by case id, for each case
 *  one of whose captures carries the Envoy plugin's block of the skill (CaptureNote's `dispatch`,
 *  of any stage). */
function dispatchAnswers(results: ReadonlyMap<string, CaseResult>): Map<string, DispatchFrom> {
  const answers = new Map<string, DispatchFrom>();
  for (const result of results.values()) {
    const stages = [...Object.values(result.stages), ...(result.earlierConfirms ?? [])];
    for (const stage of stages) {
      for (const note of Object.values(stage?.captures ?? {})) {
        if (note.dispatch !== undefined) answers.set(result.caseId, note.dispatch);
      }
    }
  }
  return answers;
}

/** The end of a verdict's line: how many of the skill's screened cases have a listing measure
 *  that says their listing could not be measured (score.ts's `listingStanding`), and how many of
 *  them carry the Envoy plugin's block on the assumption that their session had Dispatch
 *  (`dispatchAnswers`), so a reader sees both rests of the verdict at once. */
function listingWords(
  b: Behaviour,
  results: ReadonlyMap<string, CaseResult>,
  score: SkillScore
): string {
  const screened = score.cases.filter(
    (each) => each.screen.state === "counted" && each.drop?.stage !== "screen"
  );
  const unmeasured = screened.filter((each) => {
    const c = b.cases.find((candidate) => candidate.id === each.caseId);
    return listingStanding(c?.evidence).state === "not measurable";
  }).length;
  const answers = dispatchAnswers(results);
  const assumed = screened.filter((each) => answers.get(each.caseId) === "assumed").length;
  return `; the listing of ${unmeasured} of its ${screened.length} screened cases could not be measured, and ${assumed} of the ${screened.length} rest on the assumption that their session had Dispatch`;
}

/** The skill's verdict as one line: the verdict and its reason, or what it waits for; a verdict
 *  ends with the reading sessions it speaks for (`speaksFor`), and a verdict or its reason with
 *  how many screened cases' listings could not be measured (`listingWords`). With
 *  `--reading-models`, a skill too few of whose reading sessions are on served models
 *  (`servedScreeningRefusal`) gets none when it has no written case; one with written cases is
 *  screened on them, and its reason is theirs. */
function verdictLine(
  b: Behaviour,
  results: ReadonlyMap<string, CaseResult>,
  score: SkillScore
): string {
  const refusal = skillRefusal(b);
  if (refusal !== undefined) return `none: ${refusal.reason}`;
  const unfinished = screenUnfinished(b, results, score);
  if (unfinished !== undefined) return `not yet: ${unfinished} (behaviour screen)`;
  const outcome = score.outcome;
  if (outcome === undefined) {
    const third = score.cases.flatMap((each) =>
      each.confirm?.state === "running" && each.confirm.third ? [each.caseId] : []
    );
    return score.control?.state === "running"
      ? "not yet: the control runs are not all made (behaviour control)"
      : third.length > 0
        ? `not yet: two confirms on one runner disagree in ${third.join(", ")}, so a third confirm of each is due (behaviour confirm)`
        : "not yet: the confirm runs of a flagged case are not all made (behaviour confirm)";
  }
  const listing = listingWords(b, results, score);
  if (outcome.verdict === null) {
    const controlResult =
      score.controlCase === undefined ? undefined : results.get(score.controlCase);
    const planted = controlResult?.stages.control?.plantedLine ?? null;
    return `none: ${reasonWords(outcome.reason, score.control, planted)}${listing}`;
  }
  const flagged = score.cases.filter((each) => each.flagged).length;
  const control = `the planted-loss control (case ${score.controlCase}) is a loss and its false-loss control is no loss`;
  if (outcome.verdict === "keeps") {
    const kept =
      b.compared === "none"
        ? "no loss of no skill detected by the registered rule"
        : "no loss detected by the registered rule";
    return `${kept}: no confirmed case is a loss (${flagged} flagged of ${score.informative} informative), and ${control}${speaksFor(b, score)}${listing}`;
  }
  // Each case a loss by one confirm, or by POOLED_CONFIRMS pooled ones (score.ts's confirmScore).
  const losses = score.cases.filter((each) => each.loss).map((each) => each.caseId);
  const pooled = score.cases.flatMap((each) =>
    each.loss && each.confirm?.state === "counted" && each.confirm.pooled !== undefined
      ? [each.caseId]
      : []
  );
  const single = losses.filter((id) => !pooled.includes(id));
  const byPool =
    pooled.length === 0
      ? []
      : [
          `in ${pooled.join(", ")}, over ${POOLED_CONFIRMS} pooled confirms, at least ${LOSS_GAP * POOLED_CONFIRMS} fewer of ${FRESH_RUNS * POOLED_CONFIRMS}`,
        ];
  if (b.compared === "none") {
    const where = [
      ...(single.length === 0
        ? []
        : [
            `in ${single.length === losses.length ? "each" : single.join(", ")}, no skill passes at least ${LOSS_GAP} fewer of ${FRESH_RUNS} fresh runs than their skill`,
          ]),
      ...byPool,
    ];
    return `loss: no skill loses against their skill on ${counted(losses.length, "case")} (${losses.join(", ")}): ${where.join("; ")}, and ${control}${speaksFor(b, score)}${listing}`;
  }
  const where = [
    ...(single.length === 0
      ? []
      : [
          `in ${single.join(", ")} ${COMPARED_TEXT[b.compared]} passes at least ${LOSS_GAP} fewer of ${FRESH_RUNS} fresh runs than the old text`,
        ]),
    ...byPool,
  ];
  return `loss: ${where.join("; ")}, and ${control}${speaksFor(b, score)}${listing}`;
}

/** The compared arm as the report's sentences name it. */
const COMPARED_TEXT: Readonly<Record<Compared, string>> = {
  new: "the cut's text",
  ours: "our skill's text",
  none: "no skill",
};

/** The ours arm's notes of `result` (OursNote), one a stage that ran it. */
function oursNotes(result: CaseResult): OursNote[] {
  return Stage.options.flatMap((stage) => result.stages[stage]?.ours ?? []);
}

/** The paths inside the skill of the history reads the ours arm removed from case `result`, each
 *  once, in order. */
function removedReads(result: CaseResult): string[] {
  return [...new Set(oursNotes(result).flatMap((note) => note.removedReads))];
}

/** The report's line on the compared arm: the cut's text; a skill of ours, a bundle where its text
 *  replaced a skill of our name that a case's profile held; or no skill, which informs and clears
 *  nothing. */
function comparedLine(b: Behaviour, results: ReadonlyMap<string, CaseResult>): string {
  if (b.compared === "new") return "compared arm: new (the cut's text)";
  if (b.compared === "none") {
    return "compared arm: none (no skill): the arm with no skill informs and clears nothing";
  }
  const noted = [...results.values()].flatMap((result) =>
    oursNotes(result).map((note) => ({ caseId: result.caseId, note }))
  );
  const name = noted[0]?.note.name;
  const ours = name === undefined ? "ours" : `ours (${name})`;
  const bundled = [...new Set(noted.filter((each) => each.note.replacedOwn).map((e) => e.caseId))];
  if (bundled.length === 0) {
    return `compared arm: ${ours}: their skill hidden, ours in its place under its own name`;
  }
  const cases = new Set(noted.map((each) => each.caseId));
  const some =
    bundled.length === cases.size
      ? ""
      : `, in ${bundled.join(", ")}; in the other cases, ours in their skill's place under its own name`;
  return `compared arm: ${ours}, a bundle: their skill hidden, ours at the given text (it replaced the profile's own ${name})${some}`;
}

/** The report's line of the cases of each kind, and the informative cases of each. */
function kindLine(b: Behaviour, score: SkillScore): string {
  const informative = new Set(score.cases.filter((c) => c.informative).map((c) => c.caseId));
  const words = CaseKind.options.map((kind) => {
    const cases = b.cases.filter((c) => caseKind(c) === kind);
    const of = cases.filter((c) => informative.has(c.id)).length;
    return `${cases.length} ${kind} (${of} informative)`;
  });
  return `cases by kind: ${words.join(", ")}`;
}

/** The screened cases of `score` (counted, and not dropped at screen) with their screen passes, the
 *  old text's and the compared arm's among them (`compared`: no skill's, with no skill compared),
 *  and each case's kind. */
function screenedCases(
  b: Behaviour,
  score: SkillScore
): {
  caseId: string;
  kind: CaseKind;
  passes: Partial<Record<ArmName, number>>;
  informative: boolean;
  old: number;
  compared: number;
}[] {
  const kindOf = kindsOf(b);
  return score.cases.flatMap((each) => {
    if (each.screen.state !== "counted" || each.drop?.stage === "screen") return [];
    const passes: Partial<Record<ArmName, number>> = each.screen.passes;
    return [
      {
        caseId: each.caseId,
        kind: kindOf(each.caseId),
        passes,
        informative: each.informative,
        old: passes.old ?? 0,
        compared: passes[b.compared] ?? 0,
      },
    ];
  });
}

/** The report's lines of each kind's screened cases, a description beside the registered rule:
 *  their ids, how many are informative, and each screen arm's passes summed over them. */
function screenedKindLines(b: Behaviour, score: SkillScore): string[] {
  const screened = screenedCases(b, score);
  return CaseKind.options.map((kind) => {
    const cases = screened.filter((each) => each.kind === kind);
    if (cases.length === 0) return `screened ${kind} cases: none`;
    const passes = stageArms("screen", b.compared)
      .map(
        (arm) =>
          `${arm} ${cases.reduce((sum, each) => sum + (each.passes[arm] ?? 0), 0)}/${cases.length * SCREEN_RUNS}`
      )
      .join(", ");
    const informative = cases.filter((each) => each.informative).length;
    return `screened ${kind} cases: ${cases.length} (${cases.map((each) => each.caseId).join(", ")}), ${informative} informative; screen passes ${passes}`;
  });
}

/** The report's line of the mean over the screened cases of old's screen pass rate less the
 *  compared arm's, each case one observation, with its 95% interval clustered by case (score.ts's
 *  `meanDifference`): over all of them and over each kind's. A description beside the registered
 *  rule, which it does not change. */
function meanLine(b: Behaviour, score: SkillScore): string {
  const screened = screenedCases(b, score);
  const words = (kind: CaseKind | undefined): string => {
    const mean = meanDifference(
      screened
        .filter((each) => kind === undefined || each.kind === kind)
        .map((each) => (each.old - each.compared) / SCREEN_RUNS)
    );
    if (mean === undefined) return "none";
    const head = `${counted(mean.cases, "case")} ${mean.mean.toFixed(2)}`;
    return mean.interval === undefined
      ? `${head} (one case, so no interval)`
      : `${head} (95% interval ${mean.interval.low.toFixed(2)} to ${mean.interval.high.toFixed(2)}, t over the cases)`;
  };
  return `mean of old less ${b.compared} in screen pass rate, each screened case one observation (a description beside the registered rule): all ${words(undefined)}; ${CaseKind.options.map((kind) => `${kind} ${words(kind)}`).join("; ")}`;
}

/** The report's line of the screened cases that tie, floor, ceil and reverse, old against the
 *  compared arm in the screen (score.ts's `screenShapes`). */
function shapesLine(b: Behaviour, score: SkillScore): string {
  const shapes = screenShapes(screenedCases(b, score));
  return `screened cases, old against ${b.compared} in the screen: ${counted(shapes.ties, "tie")}, ${counted(shapes.floors, "floor")} (0 of ${SCREEN_RUNS} in both), ${counted(shapes.ceilings, "ceiling")} (${SCREEN_RUNS} of ${SCREEN_RUNS} in both), ${counted(shapes.reverse, "reverse gap")} (${b.compared} above old); a floor or a ceiling is a tie too`;
}

/** The report's line of the judge's identity (judge.ts's `judgeIdentity`): the SHA-256 of
 *  `prompts/judge.md` and its pinned settings, and how many stages of the results record it,
 *  record another, and record none (a stage made before the identity was recorded). */
function judgeLine(results: ReadonlyMap<string, CaseResult>): string {
  const now = judgeIdentity();
  const settings = (identity: JudgeIdentity) =>
    Object.entries(identity.settings)
      .map(([key, value]) => `${key} ${value}`)
      .join(", ");
  let stages = 0;
  let same = 0;
  let none = 0;
  const others = new Map<string, number>();
  for (const result of results.values()) {
    for (const stage of Stage.options) {
      const state = result.stages[stage];
      if (state === undefined) continue;
      stages += 1;
      const judge = state.judge;
      if (judge === undefined) none += 1;
      else if (judge.sha256 === now.sha256 && settings(judge) === settings(now)) same += 1;
      else {
        const other = `sha256 ${judge.sha256} (${settings(judge)})`;
        others.set(other, (others.get(other) ?? 0) + 1);
      }
    }
  }
  const other = stages - same - none;
  const which =
    others.size === 0
      ? ""
      : ` (${[...others].map(([each, count]) => `${each} by ${count}`).join("; ")})`;
  return `judge: prompts/judge.md sha256 ${now.sha256}; ${settings(now)}; recorded by ${same} of the ${stages} stages of the results, another judge by ${other}${which}, none by ${none}${none === 0 ? "" : " (judged before the judge's identity was recorded)"}`;
}

/** The report's line of each model of the cases, with its cases, most first, a tie by model. */
function caseModelsLine(b: Behaviour): string {
  const models = new Map<string, number>();
  for (const c of b.cases) models.set(c.model, (models.get(c.model) ?? 0) + 1);
  const words = [...models]
    .sort(([x, a], [y, z]) => z - a || x.localeCompare(y))
    .map(([model, cases]) => `${model} ${counted(cases, "case")}`);
  return `models of the cases: ${words.join(", ") || "none"}`;
}

/** The report's two lines of ours against old (their skill): each arm's screen passes, and the
 *  confirm passes and losses where confirm runs are counted, summed over the screened cases, then
 *  over those from whose history the ours arm removed no read. */
function oursAgainstOldLines(
  results: ReadonlyMap<string, CaseResult>,
  score: SkillScore
): string[] {
  const screened = score.cases.filter(
    (each) => each.screen.state === "counted" && each.drop?.stage !== "screen"
  );
  const clean = screened.filter((each) => {
    const result = results.get(each.caseId);
    return result === undefined || removedReads(result).length === 0;
  });
  const sums = (cases: readonly CaseScore[]): string => {
    /** The passes of `arm` summed over the counted stages of `stages`. */
    const passes = (arm: ArmName, stages: readonly (Partial<Record<ArmName, number>> | null)[]) =>
      stages.reduce((sum, each) => sum + (each?.[arm] ?? 0), 0);
    const screens = cases.flatMap(({ screen }) =>
      screen.state === "counted" ? [screen.passes] : []
    );
    const screen = stageArms("screen", "ours")
      .map((arm) => `${arm} ${passes(arm, screens)}/${cases.length * SCREEN_RUNS}`)
      .join(", ");
    // Each counted confirm's passes and runs: its POOLED_CONFIRMS pooled ones', or its last one's.
    const confirms = cases.flatMap(({ confirm }) =>
      confirm?.state !== "counted"
        ? []
        : confirm.pooled === undefined
          ? [{ passes: confirm.counted.at(-1) ?? null, runs: FRESH_RUNS }]
          : [{ passes: confirm.pooled, runs: FRESH_RUNS * POOLED_CONFIRMS }]
    );
    const runs = confirms.reduce((sum, each) => sum + each.runs, 0);
    const losses = cases.filter((each) => each.loss).length;
    const confirm =
      confirms.length === 0
        ? "no confirm runs"
        : `confirm on ${counted(confirms.length, "case")}: ${stageArms("confirm", "ours")
            .map(
              (arm) =>
                `${arm} ${passes(
                  arm,
                  confirms.map((each) => each.passes)
                )}/${runs}`
            )
            .join(", ")}, ${losses} a loss, ${confirms.length - losses} no loss`;
    return `screen ${screen}; ${confirm}`;
  };
  const ids = clean.length === 0 ? "" : ` (${clean.map((each) => each.caseId).join(", ")})`;
  return [
    `ours against old (their skill), over all ${counted(screened.length, "screened case")}: ${sums(screened)}`,
    `ours against old (their skill), over the ${counted(clean.length, "screened case")} with no removed read${ids}: ${sums(clean)}`,
  ];
}

/** A smallest drop's figure, as the report prints it. */
function dropWords(drop: SmallestDrop): string {
  const percent = `${(100 * drop.chance).toFixed(2)}%`;
  if (drop.reaches) return `${drop.points} points (${percent})`;
  if (drop.points === 0) return "no drop, since old passes none of its screen runs";
  return `no drop down to 0 reaches ${100 * DROP_CHANCE}% (${percent} at ${drop.points} points)`;
}

/** The report's lines of the smallest drop (score.ts's `dropFigures`): each screened case's, and
 *  the median over the screened cases and over the informative ones. */
function dropLines(b: Behaviour, score: SkillScore): string[] {
  const figures = dropFigures(score, b.compared);
  const median = (points: number | undefined) =>
    points === undefined
      ? "none"
      : points === Number.POSITIVE_INFINITY
        ? `no drop down to 0 reaches ${100 * DROP_CHANCE}%`
        : `${points} points`;
  const informative = figures.cases.filter((each) => each.informative).length;
  return [
    `smallest drop the rule catches with a chance of at least ${100 * DROP_CHANCE}%, by screened case (${DROP_STEP_POINTS}-point steps below the case's screened pass rate of old; the rule's chance with the compared arm ${b.compared}):`,
    ...figures.cases.map(
      ({ caseId, drop }) => `  ${caseId} old ${(drop.old / 100).toFixed(1)}: ${dropWords(drop)}`
    ),
    `median smallest drop: ${median(figures.screened)} over the ${counted(figures.cases.length, "screened case")}; ${median(figures.informative)} over the ${counted(informative, "informative case")}`,
  ];
}

/** `behaviour report`: prints each case's passes, the skill's verdict and the run's figures. A
 *  dropped case's line gives the stage that dropped it, why, and each stage it ran. */
function reportCommand(given: Behaviour): void {
  const results = readResults(given);
  const b = { ...given, compared: recordedCompared(given.out, results) ?? given.compared };
  const score = scored(b, results);
  const byCase = new Map(score.cases.map((each) => [each.caseId, each]));
  const lines = [`skill ${b.cases[0]?.skill}: ${b.cases.length} cases in ${b.casesFile}`];
  lines.push(comparedLine(b, results));
  for (const c of b.cases) {
    const result = results.get(c.id);
    const drop = byCase.get(c.id)?.drop;
    const ran = result === undefined ? [] : caseStageLines(result, byCase.get(c.id));
    let line =
      drop !== undefined
        ? `dropped at ${drop.stage}: ${drop.reason}; ${ran.length === 0 ? "screen: not run" : ran.join("; ")}`
        : ran.length === 0
          ? "not run"
          : ran.join("; ");
    const removed = result === undefined ? [] : removedReads(result);
    if (removed.length > 0) {
      line += `; ours left out the history's reads of ${removed.join(", ")} (our skill has no file at that path)`;
    }
    lines.push(`case ${c.id}: ${line}`);
  }

  const dropped = [...results.values()].filter((result) => result.dropped !== null);
  const drops = score.cases.flatMap((each) => (each.drop === undefined ? [] : [each.drop.reason]));
  // A case dropped after its judge controls held (its context could not be read) is no drop by
  // them.
  const byControls = dropped.filter((result) => result.controls?.hold === false).length;
  const droppedAs = (kind: string) =>
    `${drops.filter((reason) => reason.startsWith(kind)).length} ${kind}`;
  const unscreened = score.cases.filter(
    (each) => each.drop === undefined && each.screen.state === "unscreened"
  ).length;
  const flagged = score.cases.filter((each) => each.flagged).length;
  const losses = score.cases.filter((each) => each.loss).length;
  const notRun = b.cases.filter(
    (c) => !results.has(c.id) && byCase.get(c.id)?.drop === undefined
  ).length;
  // Each case dropped for a block its arms cannot rebuild, with the kind of each of its faults.
  const injected = b.cases.flatMap((c) =>
    byCase.get(c.id)?.drop?.reason.startsWith(INJECTION_UNREBUILDABLE) === true
      ? [results.get(c.id)?.injectionKinds ?? injectionDrop(c)?.kinds ?? []]
      : []
  );
  const byKind = InjectionKind.options
    .map((kind) => `${kind} ${injected.filter((kinds) => kinds.includes(kind)).length}`)
    .join(", ");
  lines.push(
    `cases: ${score.screened} screened, ${score.informative} informative, ${flagged} flagged, ${losses} confirmed losses; ${drops.length} dropped (${byControls} by the judge controls, ${droppedAs(MODEL_NOT_SERVED)}, ${droppedAs(UNREADABLE_DIRECTORY)}, ${droppedAs(LISTING_MISSES)}, ${injected.length} ${INJECTION_UNREBUILDABLE} (${byKind})), ${unscreened} unscreened, ${notRun} not run`
  );
  const pooled = score.cases.flatMap(({ caseId, confirm }) =>
    confirm?.state === "counted" && confirm.pooled !== undefined
      ? [
          `${caseId} (confirms of old ${confirm.counted.map((each) => each.old).join(", ")} and ${b.compared} ${confirm.counted.map((each) => each[b.compared] ?? 0).join(", ")} of ${FRESH_RUNS} each; pooled ${pooledWords(confirm.pooled, confirm.loss, b.compared)})`,
        ]
      : []
  );
  if (pooled.length > 0) {
    lines.push(
      `pooled confirms (two confirms at one case's texts on one runner disagreed, so a third ran, and the case is a loss when ${COMPARED_TEXT[b.compared]} passes at least ${LOSS_GAP * POOLED_CONFIRMS} fewer of the ${FRESH_RUNS * POOLED_CONFIRMS} pooled runs than the old text): ${pooled.join("; ")}`
    );
  }
  lines.push(kindLine(b, score));
  lines.push(...screenedKindLines(b, score), meanLine(b, score), shapesLine(b, score));
  const byControl = ControlName.options.map((name) => {
    const count = dropped.filter((result) => result.controls?.failed.includes(name)).length;
    return `${CONTROLS[name].step} not judged ${CONTROLS[name].wanted} ${count}`;
  });
  lines.push(
    `judge controls failed, by control (a case can fail more than one): ${byControl.join(", ")}`
  );
  lines.push(
    score.controlCase !== undefined
      ? `control case: ${score.controlCase}${replacedWords(score)}`
      : score.noControl !== undefined
        ? `control case: none: ${score.noControl}`
        : "control case: none, since no case is informative"
  );
  const control = score.control;
  if (control?.state === "counted") {
    const { old, minus, twin } = control.passes;
    lines.push(
      `controls, scored by the confirm rule (a loss when the other arm passes at least ${LOSS_GAP} fewer of ${FRESH_RUNS} fresh runs than old): old ${old}/${FRESH_RUNS}, minus ${minus}/${FRESH_RUNS}, twin ${twin}/${FRESH_RUNS}; the planted-loss control (old less minus, ${old - minus}) is ${isControlLoss(control.passes) ? "a loss, as it must be" : "no loss, so the skill gets no verdict"}; the false-loss control (old less twin, the same text twice, ${old - twin}) is ${isFalseLoss(control.passes) ? "a loss, so the skill gets no verdict" : "no loss, as it must be"}`
    );
  } else if (control !== undefined) {
    lines.push(
      `controls: the planted-loss and false-loss controls are ${control.state === "running" ? "not all run (behaviour control)" : "unscreened: an arm lacks its judged runs"}`
    );
  }
  if (b.compared === "ours") lines.push(...oursAgainstOldLines(results, score));
  lines.push(caseModelsLine(b));
  lines.push(...modelLines(b, score), writtenTasksLine(b, score));
  lines.push(`verdict: ${verdictLine(b, results, score)}`);

  const percent = (x: number) => `${(100 * x).toFixed(2)}%`;
  lines.push(
    b.compared === "none"
      ? "sensitivity of the registered rule: the chance that it calls a loss in one case, by the old text's and no skill's pass rates (exact binomial; with no skill compared, the screen's gap counts once)"
      : `sensitivity of the registered rule: the chance that it calls a loss in one informative case whose no-skill arm passes ${SENSITIVITY.none} of its runs, by the old text's and ${b.compared === "new" ? "the cut's" : "our skill's"} pass rates (exact binomial)`
  );
  for (const old of SENSITIVITY.old) {
    const row = SENSITIVITY.drops.map((drop) => {
      const cut = old - drop;
      const none = b.compared === "none" ? cut : SENSITIVITY.none;
      return `${b.compared} ${cut.toFixed(1)} ${percent(lossChance({ old, compared: cut, none }, b.compared))}`;
    });
    lines.push(`  old ${old.toFixed(1)}: ${row.join(", ")}`);
  }
  lines.push(...dropLines(b, score));

  const chances = score.cases.flatMap((each) => {
    if (!each.informative || each.screen.state !== "counted") return [];
    const { old, none, [b.compared]: other = 0 } = each.screen.passes;
    return [
      { id: each.caseId, chance: falseLossChance({ old, compared: other, none }, b.compared) },
    ];
  });
  const rate = 1 - chances.reduce((none, { chance }) => none * (1 - chance), 1);
  const laplace = `${2 * SCREEN_RUNS + 2}`;
  const how = {
    new: `when the cut's text is the old text; each case's chance is exact binomial given that it is informative, with the old and the cut's texts both passing at (old + new + 1)/${laplace} of their runs and no skill at (none + 1)/${SCREEN_RUNS + 2}`,
    ours: `when our skill's text is the old text; each case's chance is exact binomial given that it is informative, with the old text and ours both passing at (old + ours + 1)/${laplace} of their runs and no skill at (none + 1)/${SCREEN_RUNS + 2}`,
    none: `when no skill passes as the old text does; each case's chance is exact binomial, the screen's gap counted once, with the old text and no skill both passing at (old + none + 1)/${laplace} of their runs`,
  }[b.compared];
  lines.push(
    `false-loss rate at the cases run (an estimate): ${percent(rate)}, the chance that the registered rule calls a loss in one of the ${chances.length} informative cases ${how}, from its screen passes, and the cases are combined as independent, 1 − Π(1 − chance)${chances.length === 0 ? "" : ` (${chances.map(({ id, chance }) => `${id} ${percent(chance)}`).join(", ")})`}`
  );

  const notes = new Map<string, z.infer<typeof CaptureNote>>();
  let controlsUsd = 0;
  const stageUsd: Record<Stage, number> = { screen: 0, confirm: 0, control: 0 };
  let peak = 0;
  for (const result of results.values()) {
    controlsUsd += result.controls?.usd ?? 0;
    peak = Math.max(peak, result.peakRssBytes ?? 0);
    const stages: [Stage, StageResult | undefined][] = [
      ...Stage.options.map((stage): [Stage, StageResult | undefined] => [
        stage,
        result.stages[stage],
      ]),
      ...(result.earlierConfirms ?? []).map((state): [Stage, StageResult] => ["confirm", state]),
    ];
    for (const [stage, state] of stages) {
      stageUsd[stage] += state?.usd ?? 0;
      for (const note of Object.values(state?.captures ?? {})) notes.set(note.bodyFile, note);
    }
  }
  const differ = [...notes.values()].filter((note) => note.captureDigest !== note.sessionDigest);
  lines.push(
    `digests: ${differ.length} of ${notes.size} captures' system-prompt digests differ from their session's`
  );
  lines.push(judgeLine(results));
  const answers = [...dispatchAnswers(results).values()];
  const from = (answer: DispatchFrom) => answers.filter((each) => each === answer).length;
  lines.push(
    `dispatch: ${answers.length} cases carry the Envoy plugin's block of the skill: ${from("assumed")} rest on the assumption that their session had Dispatch, ${from("legion-role")} take it from a Legion role, ${from("transcript")} from their transcript (arms.ts's dispatchOf)`
  );
  const total = controlsUsd + stageUsd.screen + stageUsd.confirm + stageUsd.control;
  lines.push(
    `spend at list price: $${total.toFixed(4)} (judge controls $${controlsUsd.toFixed(4)}, screen $${stageUsd.screen.toFixed(4)}, confirm $${stageUsd.confirm.toFixed(4)}, control $${stageUsd.control.toFixed(4)})`
  );
  lines.push(`peak resident memory: ${mib(peak)} MiB, the most at the end of a case`);
  lines.push(
    b.limits === undefined
      ? "limits: not given (no --limits)"
      : `limits (--limits ${b.limits.file}):\n${b.limits.text.replace(/\n$/, "")}`
  );
  b.io.out(`${lines.join("\n")}\n`);
}

// `behaviour`: the command.

const BEHAVIOUR_COMMANDS = ["screen", "confirm", "control", "report"] as const;

const BEHAVIOUR_USAGE = [
  "usage: cli.ts behaviour <screen|confirm|control|report> --config <skill-cuts.json> --key-file <file> --out <dir> --old <dir> [--new <dir> | --ours <dir>] --cases <file>",
  "  [--planted-line <text>] [--budget-usd <n>] [--concurrency <n>] [--after-case <command>] [--request-log <file>]",
  "  [--models <models.yml>] [--omp <binary>] [--repos <dir>] [--hint-model <model>] [--omp-config <file>]... [--cache-dir <dir>] [--scratch <dir>]",
  "  [--reading-models <file>] (the skill's <cut or set id>/models/<skill>.json, written by attribute)",
  `  confirm [--case <id>] (${FRESH_RUNS} fresh runs of old and the compared arm on that case alone, into its own --out)`,
  "  [--again <case id>]... (confirm only, not with --case: one more confirm of that flagged case)",
  "  report needs only --config, --out, --old and --cases; [--limits <file>] (printed as it is)",
].join("\n");

/** The compared arm the stages of `results` record (a stage that records none was run with
 *  `--new`); undefined when no stage was run. Throws a refusal naming two files under `out` that
 *  record different arms. */
function recordedCompared(
  out: string,
  results: ReadonlyMap<string, CaseResult>
): Compared | undefined {
  let found: { compared: Compared; file: string } | undefined;
  for (const result of results.values()) {
    for (const stage of Stage.options) {
      const state = result.stages[stage];
      if (state === undefined) continue;
      const each = { compared: recordedArm(state), file: caseResultFile(out, result.caseId) };
      found ??= each;
      if (found.compared !== each.compared) {
        throw new Refused(
          `${found.file} holds runs made with the compared arm ${found.compared}, and ${each.file} with ${each.compared}: one --out holds the runs of one compared arm`
        );
      }
    }
  }
  return found?.compared;
}

/** `skill-cuts behaviour <command>`. */
async function behaviourCommand(argv: readonly string[], io: CliIo): Promise<number> {
  const [command, ...rest] = argv;
  if (!(BEHAVIOUR_COMMANDS as readonly (string | undefined)[]).includes(command)) {
    throw new Refused(`behaviour ${command ?? ""}: no such command. ${BEHAVIOUR_USAGE}`);
  }
  const values = parse(
    rest,
    {
      ...GATEWAY_OPTIONS,
      out: { type: "string" },
      old: { type: "string" },
      new: { type: "string" },
      ours: { type: "string" },
      cases: { type: "string" },
      case: { type: "string" },
      "planted-line": { type: "string" },
      "after-case": { type: "string" },
      models: { type: "string" },
      omp: { type: "string" },
      repos: { type: "string" },
      "hint-model": { type: "string" },
      "omp-config": { type: "string", multiple: true },
      "cache-dir": { type: "string" },
      scratch: { type: "string" },
      "reading-models": { type: "string" },
      limits: { type: "string" },
      again: { type: "string", multiple: true },
    },
    BEHAVIOUR_USAGE
  );
  if (values.again !== undefined && command !== "confirm") {
    throw new Refused(`--again goes with behaviour confirm only. ${BEHAVIOUR_USAGE}`);
  }
  const report = command === "report";
  const given = required(
    values,
    report ? ["config", "out", "old", "cases"] : ["config", "key-file", "out", "old", "cases"],
    BEHAVIOUR_USAGE
  );
  if (values.new !== undefined && values.ours !== undefined) {
    throw new Refused(
      `--new and --ours exclude each other: give one, or neither to compare the old text with no skill. ${BEHAVIOUR_USAGE}`
    );
  }
  if (values.case !== undefined && command !== "confirm") {
    throw new Refused("--case is for behaviour confirm only");
  }
  if (values.case !== undefined && values.again !== undefined) {
    throw new Refused(
      "--again and --case exclude each other: --case runs one confirm of its case alone, into its own --out"
    );
  }
  if (values.limits !== undefined && !report) {
    throw new Refused("--limits is for behaviour report only");
  }
  const named: Compared =
    values.new !== undefined ? "new" : values.ours !== undefined ? "ours" : "none";
  // A stage command writes the cases' session text under --out; `report` writes nothing.
  const inCheckout = report ? undefined : outRefusal(given.out);
  if (inCheckout !== undefined) throw new Refused(inCheckout);
  const config = configFrom(given.config);
  let cases: Case[];
  try {
    cases = loadCases(given.cases, given.old);
  } catch (error) {
    throw new Refused(messageFor(error));
  }
  if (cases.length === 0) throw new Refused(`${given.cases} holds no case`);
  const one = values.case === undefined ? undefined : cases.find((c) => c.id === values.case);
  if (values.case !== undefined && one === undefined) {
    throw new Refused(`--case ${values.case}: ${given.cases} holds no case ${values.case}`);
  }
  if (one !== undefined && servedModel(one.model, config.servedModels) === undefined) {
    throw new Refused(`--case ${one.id}: ${notServedWords(one.model, config.servedModels)}`);
  }
  const readingModels = values["reading-models"];
  const limits = values.limits;
  const behaviour: Behaviour = {
    io,
    command: `behaviour ${command}`,
    out: given.out,
    casesFile: given.cases,
    cases,
    compared: named,
    checkCompared: !report || values.new !== undefined || values.ours !== undefined,
    servedModels: config.servedModels,
    readingModels:
      readingModels === undefined
        ? undefined
        : {
            file: readingModels,
            count: modelCountOf(
              readJson(readingModels, "the reading sessions by model"),
              readingModels,
              "--reading-models"
            ),
          },
    limits:
      limits === undefined
        ? undefined
        : { file: limits, text: readBytes(limits, "the limits text").toString("utf8") },
  };
  if (report) {
    reportCommand(behaviour);
    return EXIT.ok;
  }
  // The served-sessions refusal stops the session cases only: a skill with written cases is
  // screened on them (screenCommand).
  const refusal = skillRefusal(behaviour);
  if (command === "screen" && refusal !== undefined) {
    throw new Refused(
      `the skill ${cases[0]?.skill} gets no verdict, and no case of it is screened: ${refusal.reason} (--reading-models ${readingModels}); the walk of the cut's ranking goes on to its next skill`
    );
  }

  const comparedDir = values.new ?? values.ours;
  if (
    comparedDir !== undefined &&
    !statSync(comparedDir, { throwIfNoEntry: false })?.isDirectory()
  ) {
    throw new Refused(`--${named} ${comparedDir} is not a directory`);
  }
  if (values.ours !== undefined && !isFile(path.join(values.ours, "SKILL.md"))) {
    throw new Refused(`--ours ${values.ours} holds no SKILL.md`);
  }
  const unpriced = [
    ...new Set(
      cases.flatMap((c) => {
        const served = servedModel(c.model, config.servedModels);
        return served !== undefined && PRICES[modelId(served)] === undefined
          ? [modelId(served)]
          : [];
      })
    ),
  ];
  if (unpriced.length > 0) {
    throw new Refused(
      `gateway.ts has no list price for the cases' models ${unpriced.join(", ")}, so their dollars could not be counted: add each to PRICES from ${PRICE_SOURCE}`
    );
  }
  // Only a command given --models can make a capture, so only it needs Oh My Pi; without it every
  // capture is read back, and Oh My Pi does not run.
  const models = values.models;
  const omp = models === undefined ? undefined : requiredOmp(values.omp);
  const gateway = openGateway({
    config,
    keyFile: given["key-file"],
    budgetUsd: values["budget-usd"],
    concurrency: values.concurrency,
  });
  const log = values["request-log"] ?? path.join(given.out, REQUEST_LOG);
  const scratch = values.scratch ?? path.join(tmpdir(), SCRATCH_DIR);
  const plantedLine = values["planted-line"];
  const source = values.repos === undefined ? ghSource() : localSource(values.repos);
  const hints = gatewayHints(loggedGateway(gateway, log, "hint"));
  const texts: Texts = {
    old: textDigest(given.old),
    ...(values.new === undefined ? {} : { new: textDigest(values.new) }),
    ...(values.ours === undefined ? {} : { ours: textDigest(values.ours) }),
  };
  const runner: Runner = {
    ...behaviour,
    dirs: {
      old: given.old,
      ...(values.new === undefined ? {} : { new: values.new }),
      ...(values.ours === undefined
        ? {}
        : { ours: { dir: values.ours, name: skillName(values.ours) } }),
      ...(plantedLine === undefined ? {} : { plantedLine }),
    },
    texts,
    scratch,
    gateway,
    replays: loggedGateway(gateway, log, "replay"),
    judge: loggedGateway(gateway, log, "judge"),
    source,
    capture: async (c, arm) => {
      const outDir = armCaptureDir(given.out, arm, texts);
      const saved = savedCapture(capturePaths(outDir, c.id, arm.name), arm);
      if (saved !== undefined) return saved;
      if (models === undefined || omp === undefined) {
        throw new Error(
          `case ${c.id} has no capture of its ${arm.name} arm in ${outDir}, and no --models is given to make one`
        );
      }
      // A case runs on the served model that serves it: an Anthropic model behind another
      // gateway prefix as the served `anthropic/<id>`, so the capture finds its provider.
      const model = servedModel(c.model, config.servedModels) ?? c.model;
      return captureBody({ ...c, model }, arm, {
        omp,
        outDir,
        scratchDir: path.join(scratch, "captures"),
        cacheDir: values["cache-dir"] ?? path.join(tmpdir(), CACHE_DIR),
        source,
        models,
        hints,
        ...(values["hint-model"] === undefined ? {} : { hintModel: values["hint-model"] }),
        configFiles: values["omp-config"] ?? [],
        session: await caseSession(config, given.out, c),
      });
    },
    afterCase: values["after-case"],
    afterCaseFailures: [],
    runner: runnerDigest(),
    again: values.again ?? [],
  };
  if (command === "screen") await screenCommand(runner);
  else if (command === "confirm" && one !== undefined) await confirmCaseCommand(runner, one);
  else if (command === "confirm") await confirmCommand(runner);
  else await controlStageCommand(runner);
  say(
    runner,
    `done; the calls of this run cost $${gateway.spend().usd.toFixed(4)} at list price; request log ${log}`
  );
  if (runner.afterCaseFailures.length > 0) {
    io.err(
      `${runner.command}: the --after-case command failed for ${runner.afterCaseFailures.join(", ")}`
    );
    return EXIT.failed;
  }
  return EXIT.ok;
}

// `load`.

const LOAD_USAGE = [
  "usage: cli.ts load --config <skill-cuts.json> --key-file <file> --skill <dir> [--name <name>] --tasks <file> --host <omp|claude> [--profile <file>] --out <dir>",
  "  --profile is the profile of each task whose line names no profile of its own",
  `  [--runs <n, default ${LOAD_RUNS}>] [--model <model, default ${LOAD_MODEL}>] [--thinking <level, default ${LOAD_THINKING.omp} on omp, ${LOAD_THINKING.claude} on claude>]`,
  "  [--budget-usd <n>] [--concurrency <n>] [--request-log <file>] [--repos <dir>] [--cache-dir <dir>] [--scratch <dir>]",
  "  [--context-session <store>:<path hash>] (the session a written task stands for: the one its profile was rebuilt from)",
  "  omp: [--models <models.yml>] [--omp <binary>] [--hint-model <model>] [--omp-config <file>]...",
  "  claude: [--claude <binary>]",
].join("\n");

/** `skill-cuts load`: the load test of load.ts. */
async function loadCommand(argv: readonly string[], io: CliIo): Promise<number> {
  const values = parse(
    argv,
    {
      ...GATEWAY_OPTIONS,
      skill: { type: "string" },
      name: { type: "string" },
      tasks: { type: "string" },
      host: { type: "string" },
      profile: { type: "string" },
      out: { type: "string" },
      runs: { type: "string" },
      model: { type: "string" },
      thinking: { type: "string" },
      models: { type: "string" },
      omp: { type: "string" },
      "hint-model": { type: "string" },
      "omp-config": { type: "string", multiple: true },
      claude: { type: "string" },
      repos: { type: "string" },
      "cache-dir": { type: "string" },
      scratch: { type: "string" },
      "context-session": { type: "string" },
    },
    LOAD_USAGE
  );
  const given = required(
    values,
    ["config", "key-file", "skill", "tasks", "host", "out"],
    LOAD_USAGE
  );
  // The load test writes the tasks' session text under --out.
  const inCheckout = outRefusal(given.out);
  if (inCheckout !== undefined) throw new Refused(inCheckout);
  const host = Host.safeParse(given.host);
  if (!host.success) throw new Refused(`--host ${given.host} is no host: omp or claude`);
  const thinking = Thinking.safeParse(values.thinking ?? LOAD_THINKING[host.data]);
  if (!thinking.success) {
    throw new Refused(
      `--thinking ${values.thinking} is no thinking level: ${Thinking.options.join(", ")}`
    );
  }
  const omp =
    host.data === "omp" && values.models !== undefined ? requiredOmp(values.omp) : undefined;
  if (values.runs !== undefined && !/^[1-9][0-9]*$/.test(values.runs)) {
    throw new Refused(`--runs ${values.runs} is not a whole number of 1 or more`);
  }
  if (!statSync(given.skill, { throwIfNoEntry: false })?.isDirectory()) {
    throw new Refused(`--skill ${given.skill} is not a directory`);
  }
  const config = configFrom(given.config);
  const named = values["context-session"];
  let contextSession: { store: string; pathHash: string } | undefined;
  if (named !== undefined) {
    const match = /^([^:]+):([0-9a-f]{64})$/.exec(named);
    if (match?.[1] === undefined || match[2] === undefined) {
      throw new Refused(
        `--context-session ${named} is not <store>:<path hash>, the hash the SHA-256 of the session file's path in hex`
      );
    }
    if (!config.stores.some((store) => store.name === match[1])) {
      throw new Refused(`--context-session ${named}: the configuration names no store ${match[1]}`);
    }
    contextSession = { store: match[1], pathHash: match[2] };
  }
  const gateway = openGateway({
    config,
    keyFile: given["key-file"],
    budgetUsd: values["budget-usd"],
    concurrency: values.concurrency,
  });
  const log = values["request-log"] ?? path.join(given.out, REQUEST_LOG);
  const result = await runLoad({
    io,
    host: host.data,
    skillDir: given.skill,
    skillDigest: textDigest(given.skill),
    ...(values.name === undefined ? {} : { name: values.name }),
    tasksFile: given.tasks,
    ...(values.profile === undefined ? {} : { profileFile: values.profile }),
    runs: values.runs === undefined ? LOAD_RUNS : Number(values.runs),
    out: given.out,
    model: values.model ?? LOAD_MODEL,
    thinking: thinking.data,
    gateway: loggedGateway(gateway, log, "load"),
    hints: gatewayHints(loggedGateway(gateway, log, "hint")),
    source: values.repos === undefined ? ghSource() : localSource(values.repos),
    scratch: values.scratch ?? path.join(tmpdir(), SCRATCH_DIR),
    cacheDir: values["cache-dir"] ?? path.join(tmpdir(), CACHE_DIR),
    ...(omp === undefined ? {} : { omp }),
    ...(values.models === undefined ? {} : { models: values.models }),
    ...(values["hint-model"] === undefined ? {} : { hintModel: values["hint-model"] }),
    configFiles: values["omp-config"] ?? [],
    claude: values.claude ?? "claude",
    requestLog: log,
    memoryLimitBytes: RUN_MEMORY_LIMIT_BYTES,
    psqlCommand: config.psqlCommand,
    stores: config.stores.map((store) => store.name),
    ...(config.devboxClaudeConfigDir === undefined
      ? {}
      : { devboxClaudeConfigDir: config.devboxClaudeConfigDir }),
    ...(contextSession === undefined ? {} : { contextSession }),
  });
  io.out(
    `load: done; the calls of this command cost $${gateway.spend().usd.toFixed(4)} at list price; request log ${log}\n`
  );
  if (result.failures.length === 0) return EXIT.ok;
  io.err(
    `load: ${result.failures.length} tasks or runs failed; run the command again to make what is missing`
  );
  return EXIT.failed;
}

// The command line.

const USAGE = [
  "usage: cli.ts <command> ...",
  "  behaviour <screen|confirm|control|report> --config <skill-cuts.json> --key-file <file> --out <dir> --old <dir> [--new <dir> | --ours <dir>] --cases <file> ...",
  "  read --prompt <name> --in <input.json> --out <answer.json> ...",
  "  control <map|attack|same-job|sentence> --in <input.json> --out <result.json> ...",
  "  control craft (--remove-section <heading> | --join-lines <a>-<b>) --copy-line <a>[-<b>] ... --original-line <a>[-<b>|all] ... <craft's flags> --out <dir>",
  "  craft-lines --list <craft-list.json>",
  `  craft ${CRAFT_FLAGS} --out <entry.json> ...`,
  "  craft-merge --entry <entry.json> --entry <entry.json> [--entry <entry.json>]... --out <merged.json>",
  `  nearest ${NEAREST_FLAGS} ...`,
  "  craft-coverage --root <dir> [--source <text>] [--skip <name>]... --entries <reader-findings.jsonl>",
  `  proposals ${PROPOSALS_FLAGS} ...`,
  `  proposals ${PROPOSALS_DRY_RUN}`,
  "  count-sql --config <skill-cuts.json> --store <db> --day <YYYY-MM-DD> [--hour <0-23>] [--query count|models]",
  "  load --config <skill-cuts.json> --key-file <file> --skill <dir> [--name <name>] --tasks <file> --host <omp|claude> [--profile <file>] --out <dir> ...",
  "  copies --config <skill-cuts.json> --at <ISO instant> --out <dir> [--only <owner/repo>]... [--max-search-calls <n>] [--cache-dir <dir>]",
  "  attribute --config <skill-cuts.json> --count <dir> --copies <dir> --spans <file> --out <dir> [--changed <cut or set id>=<file>]... [--models <dir>]",
  "  set-count --config <skill-cuts.json> --rows <dir> --spans <file> --out <dir> [--dotfiles-commit <sha>] [--rows-label <text>] [--not-read <name>=<reason>]...",
  "  build-cases --config <skill-cuts.json> --key-file <file> (--cut <id> | --set <id>) --skill <name> --old <dir> --sessions <file> --seed <n> --copies <dir> --count <dir> --out <cases.jsonl> [--max <n>] [--budget-usd <n>] [--concurrency <n>] [--stop-projected-usd <n>] [--stop-after-sessions <n>] [--stop-zero-case-usd <n>]",
  "  build-cases --config <skill-cuts.json> --key-file <file> (--cut <id> | --set <id>) --skill <name> --old <dir> --copies <dir> --count <dir> --written <n> (--context-session <store>:<hash> | --sessions <file> --seed <n>) --out <cases.jsonl> [--session-cases <cases.jsonl>] [--reading-models <file>] [--model-pool <file>]... [--budget-usd <n>]",
  "  build-cases --config <skill-cuts.json> --old <dir> --copies <dir> --count <dir> --reprofile <cases.jsonl> --out <new cases.jsonl>",
  "  build-tasks --config <skill-cuts.json> --key-file <file> --skill <dir> --store <db>... --out <tasks.jsonl> [--model <model>] [--profiles-out <dir> --copies <dir> --count <dir> --context-session <store>:<hash>] [--max-candidates <n>] [--max-search-minutes <m>]",
  "  build-tasks --config <skill-cuts.json> --copies <dir> --count <dir> --reprofile <tasks.jsonl> --profiles-out <new dir> [--context-session <store>:<hash>]",
  "  build-tasks --first-read --config <skill-cuts.json> --key-file <file> --skill <dir> --sessions <sessions.jsonl> --seed <n> [--max <n>] --out <tasks.jsonl> [--model <model>] [--profiles-out <dir> --copies <dir> --count <dir>]",
  "  results-index --count <set-count out dir> --nearest <dir> --cases <dir> --results <dir>",
].join("\n");

/** The command line. Returns the exit status (EXIT). */
export async function main(argv: readonly string[], io: CliIo): Promise<number> {
  const [command, ...rest] = argv;
  try {
    switch (command) {
      case "behaviour":
        return await behaviourCommand(rest, io);
      case "read":
        return await readCommand(rest, io);
      case "control":
        return await controlCommand(rest, io);
      case "count-sql":
        return countSqlMain(rest, io);
      case "load":
        return await loadCommand(rest, io);
      case "copies":
        return await copiesMain(rest, {
          gh: spawnGh,
          sleep: (ms) => Bun.sleep(ms),
          now: () => Date.now(),
          out: io.out,
          err: io.err,
        });
      case "attribute":
        return attributeMain(rest, io);
      case "set-count":
        return await setCountMain(rest, { out: io.out, err: io.err, gh: spawnGh });
      case "build-cases":
      case "build-tasks":
        return await buildMain(argv, {
          out: io.out,
          err: io.err,
          gh: spawnGh,
          now: () => performance.now(),
        });
      case "craft-lines":
        return await craftLinesMain(rest, { out: io.out, err: io.err, gh: spawnGh });
      case "craft":
        return await craftCommand(rest, io);
      case "craft-merge":
        return craftMergeMain(rest, io);
      case "nearest":
        return await nearestCommand(rest, io);
      case "craft-coverage":
        return craftCoverageCommand(rest, io);
      case "proposals":
        return await proposalsCommand(rest, io);
      case "results-index":
        return resultsIndexMain(rest, io);
      default:
        throw new Refused(`no command ${JSON.stringify(command ?? "")}. ${USAGE}`);
    }
  } catch (error) {
    if (error instanceof Refused) {
      io.err(`skill-cuts: ${error.message}`);
      return EXIT.refused;
    }
    if (error instanceof Stopped || error instanceof BudgetSpent) {
      io.err(`skill-cuts: stopped: ${error.message}`);
      return EXIT.stopped;
    }
    io.err(`skill-cuts: ${command}: ${messageFor(error)}`);
    return EXIT.failed;
  }
}

if (import.meta.main) {
  process.exit(await main(Bun.argv.slice(2), processIo()));
}
