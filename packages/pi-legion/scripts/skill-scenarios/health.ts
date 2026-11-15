#!/usr/bin/env bun
/**
 * `rig.sh health <run dir>...`: whether the world of each run could take what its agents did, and
 * whether the run stayed inside its own world. It does not score the run, it judges no agent, and
 * it writes nothing. A run is healthy when every rule of RULES holds:
 *   1. finished: its out.txt ends with an `exit=` line (completion-answers.ts's `finished`);
 *   2. completions: every completion line names a role the scenario boots, and no `handoff/complete`
 *      was refused 4xx with an answer its world's entry does not allow for the run's label and that
 *      role. The scorer's refused-completion rule reads this through the same function
 *      (`refusedCompletion`, which score.ts's `unscored` calls);
 *   3. taken: a scenario whose world must take a role's completion (`ANSWERS`' `takes`) took it at
 *      least once, with an answer the entry allows. reviewer-text-findings requires one of the
 *      reviewer's; threads-closed requires none;
 *   4. routes: no request the daemon stand-in answered 404 is of a route the run's label's daemon
 *      registers (`unservedRoute`): the routes read out of the source of the checkout the run's
 *      built-from names (daemon-pane.ts's `labelRoutes`), which the stand-in itself answers by
 *      (daemon-standin.ts's `ROUTES`). A 404 of a route that daemon does not register is that
 *      daemon's own answer, as the base label's to this checkout's `claims/turn-outcome` is. A
 *      run with a 404 whose daemon's routes cannot be read is unhealthy, since which it was is
 *      unknown;
 *   5. failed claim: no turn outcome the daemon stand-in answered failed the claim (its line's
 *      `failed`, `failedClaim`): the daemon fails a claim whose provider refused turns up to its
 *      launch failure limit, stops its process and takes its secret, so what the run did after
 *      is a world no daemon serves;
 *   6. push guard: no tool result of any agent of the run, and no out*.txt, holds a line the public
 *      leak check or Legion's git and jj wrappers print when they refuse a push or cannot run
 *      (GUARD_REFUSAL). No world of this rig plants a secret, so a refusal is the guard refusing
 *      what a production pod would let through, or a guard that could not start;
 *   7. gh: no `gh` call the gh stand-in refused (calls.jsonl, exit not 0) with a refusal the
 *      scenario does not declare for the calling role (GH_DECLARED, `ghRefusal`);
 *   8. another run: no tool call of the run names a path under another run's directory
 *      (`otherRun`): a sibling under the same runs directory, an archived run, or a run of another
 *      batch, wherever it sits;
 *   9. another run's write: no tool call of the run reads a path another run of those given wrote
 *      last before the read (`otherRunWrite`). A run's own writes outside its directory, HOME and
 *      TMPDIR are recorded (`writesOf`) and are no verdict on it;
 *  10. outside its world: no tool call names a path its world hides — the scratch root, the issue
 *      workspaces, the work directory, every label's checkout — outside its own directory, its
 *      /tmp and every path its world shows again (`outsideWorld`). The world is rig.sh's one
 *      declaration of it (world_paths, `rig.sh world`), the one the run's namespace mounts;
 *  11. another process: no tool call reads a /proc/<n>/environ or cmdline but /proc/self's
 *      (`otherProcess`);
 *  12. credentials: no tool call names a path under /var/run/legion or /var/run/secrets, by
 *      either name (`credentialReach`);
 *  13. off-world network: no call reaches a host other than the loopback, the hosts its pane's URLs
 *      name and the model gateway its profile's models.yml names (`offWorld`): no `web_search`
 *      call; no host a `bash` command reaches (`bashHosts`), a here-document an interpreter runs
 *      included; no host an `eval` cell reaches (`evalHosts`): each `tool.<name>(…)` call of it by
 *      that tool's own reading, and every network URL in the rest of it, in any language; no
 *      http(s) URL a `read`, `fetch` or `grep` is given as its path; no network URL in a file the
 *      run wrote and then ran (`fileRuns`); and no network URL in any string argument of a tool
 *      TEXT_TOOLS does not name, a tool nobody named included. A URL in text a call writes (a
 *      review body, a reply, a commit message, a file never run) is a link, not a reach.
 * Rules 6 and 8 to 13 read every transcript of the run (`<run>/sessions/**\/*.jsonl`: each agent's
 * and each subagent's) through score.ts's readers. The run's own directory is the one its pane
 * file names (`SKILL_SCENARIO_RUN`, which the transcripts' paths are written under) and the
 * directory health was given, which differ for a run read from an archive or a copy. Rule 9 reads
 * the runs it is given together, so a batch's runs are judged in one call: given one run, it
 * sees no other run's writes and flags nothing, and rule 10 sees no other label's checkout but
 * the work directory's profiles name, while the other rules read each run alone.
 *
 * Prints one line per run, `<run>  healthy` or `<run>  unhealthy: <reason>`, naming the first rule
 * the run fails with the evidence it failed on; then, for each rule, `health: <rule>: <n> of <m>
 * runs`, the runs that rule alone flags; then how many runs wrote outside their own directory,
 * which is no verdict; then `health: <n> of <m> runs unhealthy`. Exits 0 when every run is healthy,
 * 1 otherwise, and 2, printing no run, for no argument or for a path that is not a run directory
 * of a scenario `ANSWERS` names and of a label `LABEL_DAEMONS` names.
 */
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import { dispatchToolSpecs } from "@legion/contracts/dispatch-tools";
import { z } from "zod";
import {
  answerOf,
  daemonLines,
  finished,
  labelOf,
  refusedCompletion,
  scenarioOf,
  takenCompletions,
} from "./completion-answers";
import { labelCheckout, labelRoutes, serves } from "./daemon-pane";
import { isPost, methodOf, readWorld, reviewsRoute, submitsReview } from "./gh-call";
import { CALLS } from "./run-records";
import {
  ABSOLUTE_PATH,
  outputs,
  SHELL_COMMAND,
  transcriptEntries,
  transcriptFiles,
  withoutBodies,
} from "./score";

const USAGE = "usage: rig.sh health [--tmp-snapshot <file>] <run dir>...";

/** A tool call's arguments, as a transcript records them: an object of named values. */
const ToolArgs = z.record(z.string(), z.unknown());
type ToolArgs = z.infer<typeof ToolArgs>;

/** A path a tool call of the run reached, when, and where the call is: a read, or a write
 * outside the run's own directory, HOME and TMPDIR. */
interface Access {
  readonly path: string;
  /** Epoch milliseconds: the transcript entry's time for a tool call, the file's modification
   * time for an entry rig.sh's scratch_clean recorded. */
  readonly at: number;
  /** The transcript the call is in, or the record that holds it. */
  readonly file: string;
  readonly tool: string;
}

/** One run as the rules read it. */
interface Run {
  readonly dir: string;
  readonly scenario: string;
  readonly label: string;
  /** The run's own directory: the one its pane file names and the one health was given. */
  readonly own: readonly string[];
  /** Every tool call of every transcript of the run, with the transcript it is in and when. */
  readonly calls: readonly { file: string; tool: string; args: ToolArgs; at: number }[];
  /** Every tool result's text of every transcript of the run, with the transcript it is in. */
  readonly results: readonly { file: string; text: string }[];
  /** What the run wrote outside its own directory, HOME and TMPDIR (`writesOf`). */
  readonly writes: readonly Access[];
  /** Every path a tool call of the run reads (`readsOf`). */
  readonly reads: readonly Access[];
  /** The entries at the top of the shared scratch when the run started, from
   * <run>/scratch-before.txt (rig.sh's scratch_before); undefined for a run with no record. */
  readonly before: readonly string[] | undefined;
}

/** The run directory `dir` names, with its scenario and label, or why it is none. */
function runOf(dir: string): { scenario: string; label: string } | string {
  if (!existsSync(dir) || !statSync(dir).isDirectory()) return `${dir} is not a directory`;
  const scenario = scenarioOf(dir);
  if (scenario === undefined)
    return `${dir} is no run of a scenario health reads (completion-answers.ts's ANSWERS)`;
  try {
    return { scenario, label: labelOf(dir) };
  } catch (error) {
    return error instanceof Error ? error.message : String(error);
  }
}

/** The run's pane file (rig.sh's base_env and worker_boot write it), as its variables; empty for
 * a run that has none. */
function paneOf(dir: string): Map<string, string> {
  const file = path.join(dir, "pane.env");
  const vars = new Map<string, string>();
  if (!existsSync(file)) return vars;
  for (const line of readFileSync(file, "utf8").split("\n")) {
    const equals = line.indexOf("=");
    if (equals > 0) vars.set(line.slice(0, equals), line.slice(equals + 1));
  }
  return vars;
}

function loadRun(dir: string, scenario: string, label: string): Run {
  const pane = paneOf(dir);
  const own = [path.resolve(dir), pane.get("SKILL_SCENARIO_RUN")]
    .filter((one): one is string => one !== undefined && one !== "")
    .map((one) => path.posix.normalize(one));
  const writable = [
    ...own,
    ...[pane.get("HOME"), pane.get("TMPDIR")].filter(
      (one): one is string => one?.startsWith("/") === true
    ),
  ];
  const calls: { file: string; tool: string; args: ToolArgs; at: number }[] = [];
  const results: { file: string; text: string }[] = [];
  for (const full of transcriptFiles(dir)) {
    const file = path.relative(dir, full);
    for (const entry of transcriptEntries(full)) {
      const message = entry.message;
      if (message?.role === "assistant")
        for (const part of message.content ?? [])
          if (part.type === "toolCall")
            calls.push({
              file,
              tool: part.name ?? "tool",
              args: ToolArgs.safeParse(part.arguments).data ?? {},
              at: Date.parse(entry.timestamp ?? ""),
            });
      if (message?.role === "toolResult")
        results.push({
          file,
          text: (message.content ?? []).map((part) => part.text ?? "").join(""),
        });
    }
  }
  return {
    dir,
    scenario,
    label,
    own,
    calls,
    results,
    writes: writesOf(dir, calls, writable),
    reads: calls.flatMap((call) =>
      readsOf(call.tool, call.args).map((read) => ({ ...call, path: read }))
    ),
    before: existsSync(path.join(dir, "scratch-before.txt"))
      ? readFileSync(path.join(dir, "scratch-before.txt"), "utf8")
          .split("\n")
          .filter((line) => line !== "")
      : undefined,
  };
}

/** Whether `target` is `dir` or a path under it. */
function inside(target: string, dir: string): boolean {
  return target === dir || target.startsWith(`${dir}/`);
}

/** A line the push guard prints when it refuses a push or cannot run, at the start of a line of
 * output, under any `<name>: ` prefix a caller puts before a child's line (jj prints the git it
 * runs as `git: …`): the shared check's `LEAK CHECK: refusing …`, `LEAK CHECK ERROR: …`,
 * `LEAK CHECK SELF-TEST FAILED: …` and `LEAK CHECK: cannot read the visibility …`, and the
 * `LEAK CHECK SETUP ERROR: …` of Legion's guard and its jj wrapper (packages/daemon/internal/
 * leakguard/errors.go, /opt/legion/bin/jj). A clean push prints `LEAK CHECK:` lines too — `clean`,
 * `read <n> commit(s)`, `self-test` — and they are none of these. A line of the check's own source
 * a tool printed (`echo "LEAK CHECK ERROR: …"`, a quoted Python string) does not start so. */
const GUARD_REFUSAL =
  /^[ \t]*(?:[\w.-]+: )*LEAK CHECK(?: SETUP ERROR\b| ERROR\b| SELF-TEST FAILED\b|: refusing\b|: cannot read the visibility\b).*$/m;

/** Rule 4: the first push-guard refusal an agent of the run was shown, in a tool result of any
 * transcript or in its own output. */
function guardRefusal(run: Run): string | undefined {
  const texts = [...run.results, ...outputs(run.dir).map(([file, text]) => ({ file, text }))];
  for (const { file, text } of texts) {
    const found = GUARD_REFUSAL.exec(text);
    if (found !== null)
      return `push guard: ${file} holds the guard's refusal "${found[0].trim().slice(0, 240)}"`;
  }
  return undefined;
}

/** GitHub's answer to the protection of a branch that has none, which the world's protection
 * route gives for every unprotected base (rig.sh's fixture_write). */
const PROTECTION_404 = "gh: Branch not protected (HTTP 404)";
/** The gh stand-in's 422s, each GitHub's own answer to that call (gh-standin.ts): the create of a
 * review with an event GitHub does not take, a REQUEST_CHANGES or COMMENT with no body, or a
 * pending review while one is pending (`reviewCreate`); and the submit of a review that is not
 * pending, or with no event GitHub takes (`reviewSubmission`). */
const CREATE_422 = "review create 422";
const SUBMIT_422 = "review submit 422";
/** The gh stand-in's own fallback for a call no route of the world answers, `gh: Not Found (HTTP
 * 404)`: the world is missing an answer GitHub would give, so it is never declared. */
const UNROUTED = "gh: Not Found (HTTP 404) (no route of the world answers it)";
/** The world holds no workflow run (rig.sh's fixture_write: its checks are check runs of no
 * Actions workflow), so three of its routes give the refusal production gives for a run that is
 * not there: GitHub's 404 for the jobs of a run (`…/actions/runs/<id>/jobs`, read back from the
 * call by `JOBS`, since another route's 404 is another refusal), gh's own for `gh run view` or
 * `gh run watch <id>` of such a run, and gh's for `gh run view` or `gh run watch` naming no run
 * where no terminal can pick one. */
const JOBS_404 = "gh: Not Found (HTTP 404) (the jobs of a workflow run the world holds none of)";
const RUN_404 = "failed to get run: HTTP 404: Not Found";
const VIEW_ID_REQUIRED = "run or job ID required when not running interactively";
const WATCH_ID_REQUIRED = "run ID required when not running interactively";
const NO_RUN = [JOBS_404, RUN_404, VIEW_ID_REQUIRED, WATCH_ID_REQUIRED];
/** `repos/<owner>/<repo>/actions/runs/<id>/jobs`, with the leading slash and the query string
 * `gh api` takes. */
const JOBS = /^\/?repos\/[^/]+\/[^/]+\/actions\/runs\/\d+\/jobs(?:\?.*)?$/;

/** The gh refusals each scenario provokes on purpose, or that a correct agent meets in it, by the
 * role that meets them; every other refusal is unhealthy (rule 7). Both labels' worlds are one
 * GitHub, so the table names no label; calls.jsonl records no phase, so it names none. Declared:
 *   - the reviewer's PROTECTION_404, in both scenarios: the merge gate has the reviewer read the
 *     base branch's protection (skills/legion-worker/references/merge-gate.md), every world's
 *     base is unprotected, and GitHub answers that read 404 "Branch not protected";
 *   - the reviewer's CREATE_422 and SUBMIT_422, in both scenarios: the create and the submit of a
 *     review are the reviewer's own step in both worlds, and the stand-in refuses one exactly
 *     where GitHub refuses it, so a reviewer that met one met GitHub;
 *   - NO_RUN, for every role of both scenarios: the merge gate has the reviewer find a check's
 *     run and its jobs, and pr-body.md has a role watch CI with `gh run watch` and `gh run view`;
 *     in a world that holds no workflow run those reads get production's own refusals, so a role
 *     that met one met production.
 * Not declared: the implementer's PROTECTION_404 and 422s, since it reads no protection and makes
 * no review in threads-closed; any UNROUTED, which is the world missing an answer; and any other
 * refusal of a route the worlds add later, until a row here says why a correct agent meets it. */
const GH_DECLARED: Readonly<Record<string, Readonly<Record<string, readonly string[]>>>> = {
  "reviewer-text-findings": { reviewer: [PROTECTION_404, CREATE_422, SUBMIT_422, ...NO_RUN] },
  "threads-closed": {
    reviewer: [PROTECTION_404, CREATE_422, SUBMIT_422, ...NO_RUN],
    implementer: NO_RUN,
  },
};

/** A call of calls.jsonl as the gh stand-in records it (gh-standin.ts's `exit`); the record's
 * opening line parses as one with no `as`. */
const RecordedCall = z.looseObject({
  as: z.string().optional(),
  by: z.string().optional(),
  argv: z.array(z.string()).optional(),
  route: z.string().nullable().optional(),
  review: z.unknown().optional(),
  exit: z.number().optional(),
});
type RecordedCall = z.infer<typeof RecordedCall>;
/** A route of the world's fixtures.json, as rig.sh's fixture_write writes it and the gh stand-in
 * matches it; the stand-in rewrites the file on a body edit and keeps these three. */
const Route = z.looseObject({
  match: z.string(),
  exit: z.number().optional(),
  stderr: z.string().optional(),
});
type Route = z.infer<typeof Route>;
const Fixtures = z.looseObject({ routes: z.array(Route) });
/** A comparison of two refs, and the read of one review, the gh stand-in answers itself (its
 * `comparison` and `reviewRead`). */
const OWN_READ = /\/compare\/[^?]+\.\.\.|\/pulls\/\d+\/reviews\/\d+(?:\?|$)/;

/** The refusal the gh stand-in gave `call`, which exited with a status other than 0, or undefined
 * when that status is no refusal of the world's. The record keeps the status and not the text, so
 * the refusal is read back from the call the way the stand-in decided it:
 *   - the submit of a review (`reviewSubmission`) that left no review on the record was refused
 *     422 (SUBMIT_422); one that left a review was taken, and its status is its own `--jq`'s;
 *   - the create of a review (`reviewCreate`) a route matched that left no review was refused 422
 *     (CREATE_422); one that left a review was taken, as above;
 *   - a call a route matched is the route's refusal where the route has a status of its own (its
 *     stderr's first line; JOBS_404 for the jobs of a run, read off the call), and otherwise was
 *     answered, its status being its `--jq`'s or `--json`'s over that answer, gh's exit on the
 *     errors of a GraphQL answer, or the refusal production gives where the stand-in reads the
 *     route's answer from the run and finds nothing there (a review, a ref or a commit the world
 *     has not got, gh-standin.ts's `fromRun`), which is GitHub's answer and is not judged;
 *   - a call no route matched is the stand-in's fallback 404 (UNROUTED) unless the stand-in
 *     answers it itself: `gh pr checks`, which exits 1 or 8 on the checks it printed as gh does,
 *     `gh pr diff`, a comparison and the read of one review (OWN_READ). Their non-zero statuses
 *     are not judged: each is a `--jq` over an answer, gh's own report of a check, or, for a
 *     comparison or a read of refs or a review the run has not got, the 404 GitHub gives too.
 * What this cannot see: the text an agent was shown, which calls.jsonl does not keep; and a jq
 * failure over an answer the world gave without a field GitHub's carries, which is the world's
 * incompleteness but no refusal. */
function ghRefusal(
  call: RecordedCall,
  routes: readonly Route[],
  repo: string | undefined
): string | undefined {
  const argv = call.argv ?? [];
  if (repo !== undefined && argv[0] === "api" && isPost(argv)) {
    if (argv.some((arg) => submitsReview(arg, repo) !== undefined))
      return call.review === undefined ? SUBMIT_422 : undefined;
    if (call.route !== null && argv.some((arg) => reviewsRoute(arg, repo)))
      return call.review === undefined ? CREATE_422 : undefined;
  }
  if (call.route !== null && call.route !== undefined) {
    const route = routes.find((one) => one.match === call.route);
    if (route?.exit === undefined || route.exit === 0) return undefined;
    const refusal = (route.stderr ?? `exit ${route.exit}`).split("\n")[0];
    return refusal === "gh: Not Found (HTTP 404)" &&
      argv[0] === "api" &&
      argv.some((arg) => JOBS.test(arg))
      ? JOBS_404
      : refusal;
  }
  if (argv[0] === "pr" && (argv[1] === "checks" || argv[1] === "diff")) return undefined;
  if (argv[0] === "api" && methodOf(argv) === "GET" && argv.some((arg) => OWN_READ.test(arg)))
    return undefined;
  return UNROUTED;
}

/** Rule 7: the first `gh` call of calls.jsonl the stand-in refused with a refusal GH_DECLARED does
 * not declare for the scenario and the calling role. */
function ghUndeclared(run: Run): string | undefined {
  const file = path.join(run.dir, CALLS);
  if (!existsSync(file)) return undefined;
  const fixtures = path.join(run.dir, "fixtures.json");
  const routes = existsSync(fixtures)
    ? Fixtures.parse(JSON.parse(readFileSync(fixtures, "utf8"))).routes
    : [];
  const read = readWorld(run.dir);
  const repo = "world" in read ? read.world.repo : undefined;
  for (const [index, line] of readFileSync(file, "utf8").split("\n").entries()) {
    if (line.trim() === "") continue;
    const call = RecordedCall.parse(JSON.parse(line));
    if (call.as !== "gh" || call.exit === undefined || call.exit === 0) continue;
    const refusal = ghRefusal(call, routes, repo);
    if (refusal === undefined) continue;
    const declared = GH_DECLARED[run.scenario]?.[call.by ?? ""] ?? [];
    if (declared.includes(refusal)) continue;
    return `gh refusal: ${CALLS}:${index + 1}, \`gh ${(call.argv ?? []).join(" ").slice(0, 160)}\` of the ${call.by ?? "unnamed role"}, exited ${call.exit}: ${refusal}, which ${run.scenario} does not declare for that role`;
  }
  return undefined;
}

/** A string argument of a tool call, or undefined. */
function stringArg(args: ToolArgs, key: string): string | undefined {
  const value = args[key];
  return typeof value === "string" ? value : undefined;
}

/** The path each `[PATH#TAG]` header of an `edit` call's input edits. */
const EDIT_HEADER = /^\[([^\]\n]+?)#[0-9A-Fa-f]{4}\]/gm;

/** Every absolute path a tool call names as a path: a `read`, `grep`, `glob`, `write` or `edit`
 * call's `path`, each file an `edit` call's input edits, and every absolute path in the text a
 * `bash` call runs (here-document bodies left out: they are data) or an `eval` cell holds. A path
 * the text goes on to build with an expansion (`/tmp/out.$$`, `/tmp/${name}`) is passed over: its
 * name is not in the text, and its leading part names no file. */
function namedPaths(tool: string, args: ToolArgs): string[] {
  const named: string[] = [];
  const pathArg = stringArg(args, "path");
  if (["read", "grep", "glob", "write", "edit"].includes(tool) && pathArg !== undefined)
    named.push(pathArg);
  if (tool === "edit")
    for (const hit of (stringArg(args, "input") ?? "").matchAll(EDIT_HEADER))
      named.push(hit[1] ?? "");
  const text =
    tool === "bash"
      ? withoutBodies(stringArg(args, "command") ?? "").code
      : tool === "eval"
        ? (stringArg(args, "code") ?? "")
        : "";
  for (const hit of text.matchAll(ABSOLUTE_PATH))
    if (!/^[$`{*?]/.test(text.slice((hit.index ?? 0) + hit[0].length))) named.push(hit[0]);
  return named.filter((one) => one.startsWith("/")).map((one) => path.posix.normalize(one));
}

/** A run directory's name as rig.sh's cmd_run gives it, `<scenario>[-<case>]-<label>-<n>`, for
 * each scenario cmd_run runs. */
const RUN_NAME =
  /^(?:ask-on-message|measure-before-ask|tester-proof|reviewer-text-findings|threads-closed|brainstorm|askgate|spec-writing|spec-rewrite)-[a-z0-9][a-z0-9-]*-\d+$/;

/** The run directory `target` lies in or names, when that is a run other than this one: the
 * first component of the path named as a run is (RUN_NAME), wherever it sits — beside this run
 * in the runs directory, in the archive rig.sh moves a finished run to, or under another batch's
 * work directory. */
function otherRun(target: string, run: Run): string | undefined {
  if (run.own.some((dir) => inside(target, dir))) return undefined;
  const parts = target.split("/");
  const at = parts.findIndex((part) => RUN_NAME.test(part));
  return at < 0 ? undefined : parts.slice(0, at + 1).join("/");
}

/** Rule 6: the first tool call of the run that names a path under another run's directory. What
 * it cannot see is a reach that names no such path: a `find` or a glob from a directory above
 * the runs, a path built in a variable, or a command that reads the runs directory itself and
 * acts on what it listed. */
function otherRunReach(run: Run): string | undefined {
  for (const call of run.calls)
    for (const target of namedPaths(call.tool, call.args)) {
      const other = otherRun(target, run);
      if (other !== undefined)
        return `another run: a ${call.tool} call in ${call.file} names ${target}, under ${other}`;
    }
  return undefined;
}

/** A run's namespace as rig.sh declares it (world_paths, read through `rig.sh world`, the one
 * declaration its namespace mounts too): what the namespace hides behind an empty tmpfs
 * (`cover`), the credentials it hides the same way (`credentials`), and what it shows again
 * (`rw`, `ro`). It is not gh-call.ts's `World`, the run's world.json. */
interface RunNamespace {
  readonly covers: readonly string[];
  readonly credentials: readonly string[];
  readonly shown: readonly string[];
}

const namespaces = new Map<string, RunNamespace>();

/** The run's namespace, from `rig.sh world <label> <run dir>`, read once per run. */
function namespaceOf(run: Run): RunNamespace {
  const known = namespaces.get(run.dir);
  if (known !== undefined) return known;
  const made = Bun.spawnSync(
    ["bash", path.join(import.meta.dir, "rig.sh"), "world", run.label, run.dir],
    { stdout: "pipe", stderr: "pipe" }
  );
  if (made.exitCode !== 0)
    throw new Error(
      `rig.sh world ${run.label} ${run.dir} exited ${made.exitCode}: ${made.stderr.toString().trim()}`
    );
  const world = { covers: [] as string[], credentials: [] as string[], shown: [] as string[] };
  for (const line of made.stdout.toString().split("\n")) {
    const at = line.indexOf(" ");
    const where = path.posix.normalize(line.slice(at + 1));
    const kind = line.slice(0, at);
    if (kind === "cover") world.covers.push(where);
    else if (kind === "credentials") world.credentials.push(where);
    else if (kind === "rw" || kind === "ro") world.shown.push(where);
  }
  namespaces.set(run.dir, world);
  return world;
}

/** Rule 10: the first path a tool call of the run names under what its world hides
 * — the scratch root, the issue workspaces, the work directory and every label's checkout (those
 * of the runs given too, by their built-from) — outside the run's own directory, its /tmp (the
 * namespace's $R/tmp) and every path its world shows again. The world is the one its namespace
 * mounted, so the allowlist is that declaration, and a path under the other label's checkout is
 * outside it. A run from before the namespace is held to the same world. What it cannot see is
 * what rule 8 cannot: a reach that names no path. */
function outsideWorld(run: Run, runs: readonly Run[]): string | undefined {
  const world = namespaceOf(run);
  const hidden = [
    ...world.covers,
    ...runs.flatMap((one) => {
      try {
        return [path.posix.normalize(labelCheckout(one.dir))];
      } catch {
        return [];
      }
    }),
  ];
  const allowed = [...run.own, "/tmp", ...world.shown];
  for (const call of run.calls)
    for (const target of namedPaths(call.tool, call.args)) {
      if (allowed.some((dir) => inside(target, dir))) continue;
      const zone = hidden.find((dir) => inside(target, dir));
      if (zone !== undefined)
        return `outside its world: a ${call.tool} call in ${call.file} names ${target}, under ${zone}, which the run's world hides and does not show again (rig.sh's world_paths)`;
    }
  return undefined;
}

/** The text a call runs or reads by: a `bash` command whole, an `eval` cell, and a `read`, `grep`
 * or `glob` path. */
function callText(tool: string, args: ToolArgs): string {
  if (tool === "bash") return stringArg(args, "command") ?? "";
  if (tool === "eval") return stringArg(args, "code") ?? "";
  return ["read", "grep", "glob"].includes(tool) ? (stringArg(args, "path") ?? "") : "";
}

/** The environment or the command line of a process other than the caller's own: any
 * `/proc/<n>/environ` or `/proc/<n>/cmdline` but `/proc/self/…` and `/proc/thread-self/…`, a glob
 * or a variable in place of `<n>` included. Inside a run's namespace PID 1 is the run's own, but
 * a run from before it shared the pod's processes, and a transcript does not say which it was. */
const OTHER_PROCESS =
  /\/proc\/(?!self\/|thread-self\/)[^/\s'"`;|&<>()]+\/(?:environ|cmdline)(?![\w.-])/;

/** Rule 11: the first tool call of the run that reads another process's environment
 * or command line (OTHER_PROCESS), here-document bodies included: the 2026-11-14 probe read the
 * caller's Dispatch variables out of /proc/1/environ. */
function otherProcess(run: Run): string | undefined {
  for (const call of run.calls) {
    const found = OTHER_PROCESS.exec(callText(call.tool, call.args));
    if (found !== null)
      return `another process: a ${call.tool} call in ${call.file} reads ${found[0]}, a process the run does not own`;
  }
  return undefined;
}

/** Rule 12: the first path a tool call of the run names under a credentials path of
 * its world (/var/run/legion, /var/run/secrets, by either name), whatever the run then did with
 * it. */
function credentialReach(run: Run): string | undefined {
  const { credentials } = namespaceOf(run);
  for (const call of run.calls)
    for (const target of namedPaths(call.tool, call.args)) {
      const under = credentials.find((dir) => inside(target, dir));
      if (under !== undefined)
        return `credentials: a ${call.tool} call in ${call.file} names ${target}, under ${under}`;
    }
  return undefined;
}

/** A URL that reaches a host, whole, with its host: the schemes of the network, not Oh My Pi's
 * own references (skill://, dispatch://, local://), which reach none. */
const NETWORK_URL =
  /\b(?:https?|wss?|ssh|git|git\+ssh|ftp|sftp|rsync):\/\/(?:[^\s/@'"`]*@)?(\[[0-9A-Fa-f:.]+\]|[^\s/:?#'"`()<>;|&]+)[^\s'"`()<>;|&]*/gi;
/** Any URL, with its host: what the pane names (nats://, postgres://) counts as the world's. */
const ANY_URL =
  /\b[a-z][a-z0-9+.-]*:\/\/(?:[^\s/@'"`]*@)?(\[[0-9A-Fa-f:.]+\]|[^\s/:?#'"`()<>;|&]+)/gi;
/** A git remote in scp form, `<user>@<host>:<path>`. */
const SCP_REMOTE = /^[\w.-]+@([\w.-]+):/;
/** A host a curl or wget operand names without a scheme: a name with a dot, an address or
 * localhost, followed by a port or a path. */
const BARE_HOST =
  /^(localhost|\d{1,3}(?:\.\d{1,3}){3}|(?:[a-z0-9-]+\.)+[a-z]{2,})(?::\d+)?(?:[/:]|$)/i;
/** The curl and wget options whose value is the next word, which is no URL. */
const VALUE_FLAGS = new Set(
  "-o -O -H -d -X -u -T -F -A -e -b -c -w -x -P --output --output-document --header --data --data-raw --data-binary --data-urlencode --request --user --upload-file --form --user-agent --referer --cookie --cookie-jar --write-out --proxy --directory-prefix".split(
    " "
  )
);

/** A host as the world compares it: lower case, an IPv6 address out of its brackets. */
function hostName(host: string): string {
  return host.toLowerCase().replace(/^\[(.*)\]$/, "$1");
}

/** A flag whose value is text the command writes (a comment's body, a commit's message, a
 * title), with that value: a URL in it is a link the agent wrote, not a host it reached. */
const TEXT_FLAG =
  /(^|\s)(--body|-b|--title|-t|-m|--message|--subject)(=|[ \t]+)(?:'[^']*'|"[^"]*"|[^\s'"])+/g;
/** A `gh api` field flag with its `key=value` word; its value is written text when the key is
 * `body`, `title` or `message`. */
const FIELD_FLAG = /(^|\s)(-f|-F|--field|--raw-field)(=|[ \t]+)((?:'[^']*'|"[^"]*"|[^\s'"])+)/g;

/** A host a call reaches, with the text that names it and, for a URL a program holds, where that
 * program is (` in a here-document python3 runs`). */
interface Reach {
  readonly host: string;
  readonly named: string;
  readonly via?: string;
}

/** The interpreters whose program health reads: the shells and the languages an agent writes a
 * script in. `jq` is none: a jq program reaches no host, so a body it reads is data. */
const INTERPRETER = /^(?:python[\d.]*|node|bun|deno|sh|bash|zsh|dash|ksh|perl|ruby|php)$/;
/** The shells, whose program health reads as a `bash` command's (`bashHosts`). */
const SHELL = /^(?:sh|bash|zsh|dash|ksh)$/;
/** A script's first line naming a shell as its interpreter. */
const SHELL_SHEBANG = /^#![^\n]*\b(?:ba|z|da|k)?sh\b/;
/** A redirection with its target, which a command's words leave out. */
const REDIRECTION = /(?:\d+|&)?[<>]{1,2}\|?[ \t]*\S*/g;
/** A redirection of a command's output, `>` or `>>`, with its target, relative or not. */
const OUTPUT = /(?:^|[^<>&\d])1?(>>?)\|?[ \t]*['"]?([^\s'";&|()<>`]+)/g;

/** Each command of shell text by SHELL_COMMAND: its first word, that word's base name, the rest of
 * its text, and where the command lies in the text. */
function commandsOf(code: string) {
  return [...code.matchAll(SHELL_COMMAND)].map((hit) => ({
    word: hit[1] ?? "",
    name: path.posix.basename(hit[1] ?? ""),
    rest: hit[2] ?? "",
    at: hit.index ?? 0,
    end: (hit.index ?? 0) + hit[0].length,
  }));
}

/** A command's words after its first, unquoted, with its redirections left out. */
function wordsOf(rest: string): string[] {
  return [...rest.replace(REDIRECTION, " ").matchAll(WORD)].map((word) =>
    word[0].replace(/^(['"])(.*)\1$/, "$2")
  );
}

/** The program an interpreter command runs: its first operand, a script's path (`python3 f.py`,
 * `bun run f.ts`) or a program given inline (`python3 -c '…'`, which names no file the run
 * wrote); `-` for a program it reads on stdin (`python3 -`, `node`, `bash -s arg`); or undefined
 * for a command that is no interpreter. */
function programOf(name: string, words: readonly string[]): string | undefined {
  if (!INTERPRETER.test(name)) return undefined;
  if (SHELL.test(name) && words.includes("-s")) return "-";
  const operands = words.filter((word) => word === "-" || !word.startsWith("-"));
  if ((name === "bun" || name === "deno") && operands[0] === "run") operands.shift();
  return operands[0] ?? "-";
}

/** The interpreter that runs the here-document whose `<<` lies at `at` in `code` as its program:
 * an interpreter that reads its program on stdin (`python3 - <<EOF`), or one a bare `cat` pipes
 * the body into (`cat <<EOF | node`). Undefined for every other command, whose body is text it
 * writes or data it reads: `cat > f`, `tee`, `gh … --body-file -`, `git commit -F -`, `jq`. */
function bodyRunner(code: string, at: number): string | undefined {
  const commands = commandsOf(code);
  const index = commands.findIndex((one) => one.at <= at && at < one.end);
  const owner = commands[index];
  if (owner === undefined) return undefined;
  const words = wordsOf(owner.rest);
  if (programOf(owner.name, words) === "-") return owner.name;
  const next = commands[index + 1];
  return owner.name === "cat" &&
    words.length === 0 &&
    next !== undefined &&
    code[next.at] === "|" &&
    code.slice(owner.end, next.at).trim() === "" &&
    programOf(next.name, wordsOf(next.rest)) === "-"
    ? next.name
    : undefined;
}

/** The host of every network URL in `text`. */
function urlHosts(text: string): Reach[] {
  return [...text.matchAll(NETWORK_URL)].map((hit) => ({
    host: hostName(hit[1] ?? ""),
    named: hit[0],
  }));
}

/** Every host a program reaches: a shell script's as a `bash` command's (`bashHosts`), and the
 * host of every network URL any other language's program holds, whatever it does with it. */
function programHosts(text: string, shell: boolean): Reach[] {
  return shell ? bashHosts(text) : urlHosts(text);
}

/** Every host a `bash` command reaches, with the text that names it: the host of every network
 * URL in it but those in text it writes (the values of TEXT_FLAG and of a `body`, `title` or
 * `message` FIELD_FLAG, and here-document bodies but those an interpreter runs, `bodyRunner`), the
 * scp-form remote of a `git` or `jj` command, a curl or wget operand that names a host without a
 * scheme, and the host a `gh api --hostname` names. */
function bashHosts(command: string): Reach[] {
  const { code, bodies } = withoutBodies(command);
  const reached = code
    .replace(TEXT_FLAG, (_all, lead, flag, separator) => `${lead}${flag}${separator}''`)
    .replace(FIELD_FLAG, (all, lead, flag, separator, value) =>
      /^['"]?(?:body|title|message)=/.test(value) ? `${lead}${flag}${separator}''` : all
    );
  const hosts = urlHosts(reached);
  for (const body of bodies) {
    const runner = bodyRunner(code, body.at);
    if (runner !== undefined)
      for (const one of programHosts(body.text, SHELL.test(runner)))
        hosts.push({ ...one, via: one.via ?? ` in a here-document ${runner} runs` });
  }
  for (const hit of code.matchAll(SHELL_COMMAND)) {
    const name = path.posix.basename(hit[1] ?? "");
    const words = [...(hit[2] ?? "").matchAll(WORD)].map((word) =>
      word[0].replace(/^(['"])(.*)\1$/, "$2")
    );
    words.forEach((word, at) => {
      const before = words[at - 1] ?? "";
      const scp = SCP_REMOTE.exec(word);
      if ((name === "git" || name === "jj") && scp !== null && !word.includes("://"))
        hosts.push({ host: hostName(scp[1] ?? ""), named: word });
      const bare = BARE_HOST.exec(word);
      if (
        (name === "curl" || name === "wget") &&
        bare !== null &&
        !word.includes("://") &&
        !VALUE_FLAGS.has(before)
      )
        hosts.push({ host: hostName(bare[1] ?? ""), named: word });
      if (name === "gh" && before === "--hostname")
        hosts.push({ host: hostName(word), named: word });
      if (name === "gh" && word.startsWith("--hostname="))
        hosts.push({ host: hostName(word.slice("--hostname=".length)), named: word });
    });
  }
  return hosts;
}

/** The files a `bash` command writes a here-document's body into, with the body: `cat > f`,
 * `cat >> f` (`append`), `tee f`, `tee -a f`. */
function bashScripts(command: string): { path: string; text: string; append: boolean }[] {
  const { code, bodies } = withoutBodies(command);
  const commands = commandsOf(code);
  return bodies.flatMap((body) => {
    const owner = commands.find((one) => one.at <= body.at && body.at < one.end);
    if (owner?.name === "tee") {
      const words = wordsOf(owner.rest);
      const append = words.includes("-a") || words.includes("--append");
      return words
        .filter((word) => !word.startsWith("-"))
        .map((file) => ({ path: file, text: body.text, append }));
    }
    if (owner?.name !== "cat") return [];
    return [...owner.rest.matchAll(OUTPUT)].map((hit) => ({
      path: hit[2] ?? "",
      text: body.text,
      append: hit[1] === ">>",
    }));
  });
}

/** A shell's quoted string. */
const SHELL_QUOTED = /'[^']*'|"(?:\\.|[^"\\])*"/g;
/** The program a shell is given inline: `sh -c '<program>'`, `bash -lc "<program>"`. */
const SHELL_INLINE =
  /\b(?:ba|z|da|k)?sh[ \t]+(?:-[a-z]*c|-\w+[ \t]+-c)[ \t]+('[^']*'|"(?:\\.|[^"\\])*")/g;

/** The files a `bash` command runs as a program, with what runs each: an interpreter's script
 * (`python3 f.py`, `bash f.sh`, `bun run f.ts`), a file `source` or `.` reads, a command named by
 * its path (`./f`), and each of those in a program a shell is given inline (`sh -c '…'`). A
 * quoted string of more than one word or line is text the command is given (a commit message, a
 * `--run` note), whose commands run nothing. A command substitution starts a command of its own,
 * so `body=$(cat f)` runs `cat`, not `f`: SHELL_COMMAND reads `body=$(cat` as one assignment. */
function bashRuns(command: string): { path: string; runner: string }[] {
  const code = withoutBodies(command).code;
  const inline = [...code.matchAll(SHELL_INLINE)].flatMap((hit) =>
    bashRuns((hit[1] ?? "").slice(1, -1))
  );
  const spoken = code
    .replace(SHELL_QUOTED, (quoted) => (/[\s;&|`]/.test(quoted) ? "''" : quoted))
    .replace(/\$\(|`/g, "$& ");
  return commandsOf(spoken)
    .flatMap((one) => {
      const words = wordsOf(one.rest);
      const program = programOf(one.name, words);
      if (program !== undefined && program !== "-") return [{ path: program, runner: one.name }];
      if ((one.name === "source" || one.name === ".") && words[0] !== undefined)
        return [{ path: words[0], runner: "source" }];
      return one.word.includes("/") ? [{ path: one.word, runner: one.word }] : [];
    })
    .concat(inline);
}

/** A profile's models.yml, as far as its providers' addresses. */
const ModelsFile = z.looseObject({
  providers: z.record(z.string(), z.looseObject({ baseUrl: z.string().optional() })).optional(),
});

/** The hosts of the run's world: the loopback, every host a URL of its pane file names (its
 * stand-ins', its Dispatch's, its Envoy's and its NATS's), and the host of every provider's
 * baseUrl in the models.yml of each HOME the run's agents ran under (`home`, `home-<role>`): the
 * model gateway its profile names, read from the profile's own configuration. */
function worldHosts(run: Run): Set<string> {
  const hosts = new Set(["127.0.0.1", "localhost", "::1"]);
  const pane = path.join(run.dir, "pane.env");
  if (existsSync(pane))
    for (const hit of readFileSync(pane, "utf8").matchAll(ANY_URL))
      hosts.add(hostName(hit[1] ?? ""));
  const homes = readdirSync(run.dir).filter((name) => /^home(?:-[\w-]+)?$/.test(name));
  for (const home of homes) {
    const profiles = path.join(run.dir, home, ".omp", "profiles");
    if (!existsSync(profiles)) continue;
    for (const profile of readdirSync(profiles)) {
      const models = path.join(profiles, profile, "agent", "models.yml");
      if (!existsSync(models)) continue;
      const parsed = ModelsFile.safeParse(Bun.YAML.parse(readFileSync(models, "utf8")));
      for (const provider of Object.values(parsed.data?.providers ?? {}))
        for (const hit of (provider.baseUrl ?? "").matchAll(ANY_URL))
          hosts.add(hostName(hit[1] ?? ""));
    }
  }
  return hosts;
}

/** A tool call as a transcript records it, with the transcript it is in and when. */
type Call = Run["calls"][number];

/** The tools whose arguments rule 13 reads as text a call writes or sends, never as a host it
 * reaches, each with why. A tool neither here nor read by a door of its own (`reachesOf`) has
 * every string argument read for a network URL, so a tool nobody named is read too. */
const TEXT_TOOLS: Readonly<Record<string, string>> = {
  write:
    "its content is a file's text; a file the run goes on to run is read where it runs (`fileRuns`)",
  edit: "its input is a file's text; a file the run goes on to run is read where it runs (`fileRuns`)",
  task: "its prompts are a subagent's instructions; the subagent's own calls are transcripts of the run, read as any",
  todo: "its items are the agent's own list of steps",
  envoy_publish: "its message is text a session of the run's Envoy reads",
  envoy_send: "its message is text a session of the run's Envoy reads",
  legion: "its summary and decision are text the run's daemon, a host of its world, records",
  report_issue: "its content is a report on a tool, which the harness records",
  ...Object.fromEntries(
    dispatchToolSpecs.map((spec) => [
      spec.name,
      "a Dispatch tool: its arguments are text and refs the run's Dispatch, a host of its world, records or reads",
    ])
  ),
};
/** The tools that read the `path` (or `url`) they are given: an http(s) URL there is fetched. A
 * `grep`'s pattern is text it searches for. */
const PATH_READERS = new Set(["read", "fetch", "web_fetch", "grep"]);

/** The call a tool call is: a `write` to an `xd://<tool>` device is a call of that tool with the
 * arguments its content holds (a content that is no JSON object is its one `content`). */
function deviceCall(call: Call): Call & { shown: string } {
  const target = call.tool === "write" ? (stringArg(call.args, "path") ?? "") : "";
  if (!target.startsWith("xd://")) return { ...call, shown: call.tool };
  const content = stringArg(call.args, "content") ?? "";
  let args: ToolArgs = { content };
  try {
    args = ToolArgs.safeParse(JSON.parse(content)).data ?? args;
  } catch {}
  return { ...call, tool: target.slice("xd://".length), args, shown: `write ${target}` };
}

/** Every string a value holds, in its arrays and objects too. */
function stringsOf(value: unknown): string[] {
  if (typeof value === "string") return [value];
  if (Array.isArray(value)) return value.flatMap(stringsOf);
  return typeof value === "object" && value !== null ? Object.values(value).flatMap(stringsOf) : [];
}

/** A file the run wrote, as far as its text, and the call that wrote it last. */
interface Written {
  readonly path: string;
  readonly text: string;
  readonly tool: string;
  readonly file: string;
}

/** Whether two paths the run named are one file: equal once normalized, or the one relative and
 * the other ending in it (`fetch.py`, `<ws>/fetch.py`), since a transcript does not say the cwd. */
function sameFile(one: string, other: string): boolean {
  const [a, b] = [path.posix.normalize(one), path.posix.normalize(other)];
  return (
    a === b ||
    (!b.startsWith("/") && a.endsWith(`/${b}`)) ||
    (!a.startsWith("/") && b.endsWith(`/${a}`))
  );
}

/** The files a call writes and the text it writes into each: a `write` call's content, an `edit`
 * call's input appended to each file it edits, a here-document a `bash` command writes to a file
 * (`bashScripts`), and each of those an `eval` cell's session tool calls write (`evalToolCalls`). */
function writtenText(call: Call): { path: string; text: string; append: boolean }[] {
  if (call.tool === "eval")
    return (evalToolCalls(stringArg(call.args, "code") ?? "") ?? []).flatMap((one) =>
      writtenText({ ...call, tool: one.tool, args: one.args })
    );
  if (call.tool === "write")
    return [
      {
        path: stringArg(call.args, "path") ?? "",
        text: stringArg(call.args, "content") ?? "",
        append: false,
      },
    ];
  if (call.tool === "edit") {
    const input = stringArg(call.args, "input") ?? "";
    const edited = [...input.matchAll(EDIT_HEADER)].map((hit) => hit[1] ?? "");
    const named = stringArg(call.args, "path");
    return [...edited, ...(named === undefined ? [] : [named])].map((file) => ({
      path: file,
      text: input,
      append: true,
    }));
  }
  return call.tool === "bash" ? bashScripts(stringArg(call.args, "command") ?? "") : [];
}

/** A Python or JavaScript string literal on one line, with its text. */
const STRING_LITERAL = /(['"`])((?:\\.|(?!\1)[^\\\n])*)\1/g;
/** The command a subprocess call of an eval cell runs, as its first argument: a string literal
 * or a list of them (`execSync("bun f.ts")`, `subprocess.run(["python3", "f.py"])`,
 * `os.system(…)`), or Bun's shell (`` $`bun f.ts` ``). */
const SUBPROCESS =
  /\b(?:execSync|execFileSync|execFile|exec|spawnSync|spawn|system|popen|Popen|run|call|check_call|check_output|getoutput|getstatusoutput)\s*\(\s*(\[[^\]\n]*\]|(['"`])(?:\\.|(?!\2)[^\\\n])*\2)|\$(`[^`]*`)/g;
/** A file an eval cell loads as code by its path: Python's `runpy.run_path`, `execfile` and
 * `exec(open(…))`, JavaScript's `require`, `import(…)` and `import … from`; and a module Python
 * imports by its name (`import fetch`, `from fetch import …`). */
const EVAL_LOAD =
  /\b(?:run_path|execfile|require|import)\s*\(\s*(['"`])([^'"`\n]+)\1|\bexec\s*\(\s*open\s*\(\s*(['"])([^'"\n]+)\3|\bfrom\s+(['"])([^'"\n]+)\5|^[ \t]*(?:from[ \t]+([\w.]+)[ \t]+import|import[ \t]+([\w.]+))/gm;

/** The files an `eval` cell runs: each it loads as code (EVAL_LOAD), and each the command of a
 * subprocess call of it runs (SUBPROCESS, read by `bashRuns`). A file a string of the cell names
 * otherwise (`open("f")`, a message quoting `` `bun f.ts` ``) it does not run. */
function evalRuns(code: string): { path: string; runner: string }[] {
  const commands = [...code.matchAll(SUBPROCESS)].map((hit) =>
    [...(hit[1] ?? hit[3] ?? "").matchAll(STRING_LITERAL)].map((word) => word[2] ?? "").join(" ")
  );
  return [
    ...commands.flatMap(bashRuns),
    ...[...code.matchAll(EVAL_LOAD)].map((hit) => ({
      path: hit[2] ?? hit[4] ?? hit[6] ?? `${(hit[7] ?? hit[8] ?? "").replace(/\./g, "/")}.py`,
      runner: "eval",
    })),
  ];
}

/** The files the run wrote (`written`) that a call runs, with whether each is read as a shell
 * script: a file a `bash` command runs (`bashRuns`), the files its own here-documents write
 * (`bashScripts`, `cat > f <<EOF … EOF; python3 f`) among them, or an `eval` cell runs
 * (`evalRuns`), by its path or, for a module a cell loads with no extension, by that path with
 * one. */
function fileRuns(call: Call, written: readonly Written[]): { file: Written; shell: boolean }[] {
  const command = call.tool === "bash" ? (stringArg(call.args, "command") ?? "") : "";
  const runs =
    call.tool === "bash"
      ? bashRuns(command)
      : call.tool === "eval"
        ? evalRuns(stringArg(call.args, "code") ?? "")
        : [];
  const files = [...written, ...record(bashScripts(command), written, call.tool, call.file)];
  return runs.flatMap((run) => {
    const file = files.findLast(
      (one) =>
        sameFile(one.path, run.path) ||
        (run.runner === "eval" && sameFile(one.path.replace(/\.[cm]?[jt]s$/, ""), run.path))
    );
    if (file === undefined) return [];
    const shell =
      SHELL.test(run.runner) ||
      run.runner === "source" ||
      (!INTERPRETER.test(run.runner) &&
        (/\.(?:sh|bash)$/.test(file.path) || SHELL_SHEBANG.test(file.text)));
    return [{ file, shell }];
  });
}

/** The end (one past it) of the Python or JavaScript string literal whose quote is at `at`: a
 * triple-quoted Python string, a JavaScript template with the code of each `${…}` skipped as code,
 * or a one-line string; -1 for one that does not end. */
function literalEnd(code: string, at: number): number {
  const quote = code[at] ?? "";
  if (quote === "`") {
    for (let i = at + 1; i < code.length; i++) {
      if (code[i] === "\\") i++;
      else if (code[i] === "`") return i + 1;
      else if (code.startsWith("${", i)) {
        i = closer(code, i + 2, "}");
        if (i < 0) return -1;
      }
    }
    return -1;
  }
  const fence = code.startsWith(quote.repeat(3), at) ? quote.repeat(3) : quote;
  for (let i = at + fence.length; i < code.length; i++) {
    if (code[i] === "\\") i++;
    else if (code.startsWith(fence, i)) return i + fence.length;
    else if (code[i] === "\n" && fence.length === 1) return -1;
  }
  return -1;
}

/** The brackets of Python and JavaScript, by their opener. */
const CLOSERS: Readonly<Record<string, string>> = { "(": ")", "[": "]", "{": "}" };

/** Where the `close` lies that ends the code from `at`, past nested brackets and string literals
 * (`literalEnd`); -1 for code it does not end in, or that closes another bracket first. */
function closer(code: string, at: number, close: string): number {
  const open: string[] = [];
  for (let i = at; i < code.length; i++) {
    const char = code[i] ?? "";
    if (char === "'" || char === '"' || char === "`") {
      const end = literalEnd(code, i);
      if (end < 0) return -1;
      i = end - 1;
    } else if (CLOSERS[char] !== undefined) open.push(CLOSERS[char]);
    else if (char === ")" || char === "]" || char === "}") {
      if (open.length === 0) return char === close ? i : -1;
      if (open.pop() !== char) return -1;
    }
  }
  return -1;
}

/** The name a string literal is given by the text before it: `key:`, `"key":` or `key=`, a
 * Python string's prefix (`f`, `r`, `b`) between. */
const KEYED = /(?:(\w+)|(["'])(\w+)\2)\s*[:=]\s*[rbfuRBFU]{0,2}$/;

/** A Python f-string's prefix (`f`, `rf`, `Fr`) right before its quote. */
const F_STRING = /[rRbBuU]?[fF][rRbBuU]?$/;

/** The arguments a session tool call's argument text gives, as far as its string literals: each
 * by its key (`{path: "…"}`, `path="…"`, `{"path": "…"}`), the rest by place (`_0`, `_1`, a
 * quoted key among them); and the code of each `${…}` in a JavaScript template and each `{…}` in
 * a Python f-string among them (`{{` is a brace of the text). Undefined for text whose literals
 * or expressions do not end. */
function literalArgs(text: string): { args: ToolArgs; code: string[] } | undefined {
  const args: ToolArgs = {};
  const code: string[] = [];
  let place = 0;
  for (let at = 0; at < text.length; at++) {
    const quote = text[at] ?? "";
    if (quote !== "'" && quote !== '"' && quote !== "`") continue;
    const end = literalEnd(text, at);
    if (end < 0) return undefined;
    const fence = quote !== "`" && text.startsWith(quote.repeat(3), at) ? 3 : 1;
    const value = text.slice(at + fence, end - fence);
    const opener = quote === "`" ? "${" : F_STRING.test(text.slice(0, at)) ? "{" : undefined;
    for (let i = opener === undefined ? -1 : value.indexOf(opener); i >= 0; ) {
      if (opener === "{" && value[i + 1] === "{") {
        i = value.indexOf(opener, i + 2);
        continue;
      }
      const close = closer(value, i + (opener?.length ?? 0), "}");
      if (close < 0) return undefined;
      code.push(value.slice(i + (opener?.length ?? 0), close));
      i = value.indexOf(opener ?? "", close + 1);
    }
    const key = KEYED.exec(text.slice(0, at));
    args[key?.[1] ?? key?.[3] ?? `_${place++}`] = value;
    at = end - 1;
  }
  return { args, code };
}

/** A session tool an eval cell calls by name, `tool.<name>(`, in Python or JavaScript. */
const TOOL_CALL = /\btool\.(\w+)\s*\(/g;

/** Each session tool call of an eval cell (TOOL_CALL), in the cell's order: its tool, the
 * arguments its string literals give (`literalArgs`), the code of the expressions among them, and
 * where its argument text lies. Undefined for a cell with a call whose brackets or literals do
 * not end. */
function evalToolCalls(code: string) {
  const calls: { tool: string; args: ToolArgs; code: string[]; open: number; close: number }[] = [];
  for (const hit of code.matchAll(TOOL_CALL)) {
    const open = (hit.index ?? 0) + hit[0].length;
    const close = closer(code, open, ")");
    const given = close < 0 ? undefined : literalArgs(code.slice(open, close));
    if (given === undefined) return undefined;
    calls.push({ tool: hit[1] ?? "", ...given, open, close });
  }
  return calls;
}

/** Every host an `eval` cell reaches: each session tool call of it (`evalToolCalls`) by the
 * reading its tool gets as a call of its own (`reachesOf`: none for a tool of TEXT_TOOLS,
 * `bashHosts` for `bash`, the URL a `read` is given, the class default for a tool nobody named),
 * given the files the run wrote, those the cell's calls before it wrote included, and the code of
 * each expression among its literals; then every network URL in the rest of the cell, those
 * calls blanked. A cell with a call whose brackets or literals do not end is read whole as code. */
function evalHosts(call: Call, written: readonly Written[]): Reach[] {
  const code = stringArg(call.args, "code") ?? "";
  const calls = evalToolCalls(code);
  if (calls === undefined) return urlHosts(code);
  const rest = code.split("");
  const reached: Reach[] = [];
  const seen = [...written];
  for (const one of calls) {
    const inner = { ...call, tool: one.tool, args: one.args };
    for (const hit of [
      ...reachesOf(inner, seen),
      ...one.code.flatMap((expression) => evalHosts({ ...call, args: { code: expression } }, seen)),
    ])
      reached.push({ ...hit, via: hit.via ?? ` in the cell's tool.${one.tool} call` });
    seen.push(...record(writtenText(inner), seen, call.tool, call.file));
    rest.fill(" ", one.open, one.close);
  }
  return [...reached, ...urlHosts(rest.join(""))];
}

/** Every host a call reaches, by its tool: a `bash` command's (`bashHosts`); an `eval` cell's
 * (`evalHosts`), in whatever language; an http(s) URL a `read`, `fetch` or `grep` is given as
 * its path; and for a file the call runs that the run wrote (`fileRuns`), every host its
 * program reaches. A tool of TEXT_TOOLS reaches none by its arguments; any other tool reaches the
 * host of every network URL in any string argument but `i`, the intent every call names. */
function reachesOf(call: Call, written: readonly Written[]): Reach[] {
  const ran = fileRuns(call, written).flatMap(({ file, shell }) =>
    programHosts(file.text, shell).map((one) => ({
      ...one,
      via: ` in ${file.path}, which a ${file.tool} call in ${file.file} wrote and it runs`,
    }))
  );
  if (call.tool === "bash") return [...bashHosts(stringArg(call.args, "command") ?? ""), ...ran];
  if (call.tool === "eval") return [...evalHosts(call, written), ...ran];
  if (PATH_READERS.has(call.tool))
    return (stringArg(call.args, "url") ?? stringArg(call.args, "path") ?? "")
      .split(";")
      .flatMap((one) =>
        /^https?:\/\//i.test(one) ? urlHosts(one).map((hit) => ({ ...hit, named: one })) : []
      );
  if (TEXT_TOOLS[call.tool] !== undefined) return [];
  return Object.entries(call.args)
    .filter(([key]) => key !== "i")
    .flatMap(([, value]) => stringsOf(value).flatMap(urlHosts));
}

/** The files `writes` leaves, given the files the run had `written` before it: a write that
 * appends keeps the text the file held, by a write earlier in `writes` too. */
function record(
  writes: readonly { path: string; text: string; append: boolean }[],
  written: readonly Written[],
  tool: string,
  file: string
): Written[] {
  const made: Written[] = [];
  for (const one of writes) {
    const before = one.append
      ? [...written, ...made].findLast((held) => sameFile(held.path, one.path))
      : undefined;
    made.push({ path: one.path, text: `${before?.text ?? ""}${one.text}`, tool, file });
  }
  return made;
}

/** The first call of `calls` that reaches a host not in `hosts`, as a reason, read in the order the
 * calls were made (a transcript's own order where two calls share a time): every `web_search` call,
 * which always leaves the world, and the hosts each call reaches (`reachesOf`), given the files the
 * calls before it wrote. */
export function offWorldReach(
  calls: readonly Call[],
  hosts: ReadonlySet<string>
): string | undefined {
  const allowed = [...hosts].join(", ");
  const written: Written[] = [];
  const ordered = calls
    .map((call, index) => ({ call, index }))
    .sort((a, b) => a.call.at - b.call.at || a.index - b.index);
  for (const { call: made } of ordered) {
    const call = deviceCall(made);
    if (call.tool === "web_search")
      return `off-world network: a ${call.shown} call in ${call.file} searches the web, which no host of the run's world serves`;
    const off = reachesOf(call, written).find((one) => !hosts.has(one.host));
    if (off !== undefined)
      return `off-world network: a ${call.shown} call in ${call.file} reaches ${off.host} (${off.named})${off.via ?? ""}, no host of the run's world (${allowed})`;
    written.push(...record(writtenText(call), written, call.shown, call.file));
  }
  return undefined;
}

/** Rule 13: the first call of the run that reaches a host off its world (`worldHosts`,
 * `offWorldReach`). No tool is taken out of a profile for this: a profile without a tool is a
 * world that differs from production. */
function offWorld(run: Run): string | undefined {
  return offWorldReach(run.calls, worldHosts(run));
}

/** Commands whose operands are files they write: every operand, or the last, which is the
 * destination (`cp`, `mv`, `ln` and `install`, unless `-t <dir>` names it; `sed -i`, whose
 * last operand is the file and whose first may be a script that starts with `/`). */
const WRITERS: Readonly<Record<string, "all" | "last">> = {
  tee: "all",
  touch: "all",
  mkdir: "all",
  rm: "all",
  rmdir: "all",
  truncate: "all",
  cp: "last",
  mv: "last",
  ln: "last",
  install: "last",
};
/** A redirection of a command's output to an absolute path: `>`, `>>`, `>|`, `2>`, `&>`. */
const REDIRECT = /(?:^|[^<>&\d])(?:\d+|&)?>>?\|?[ \t]*(['"]?)(\/[^\s'";&|()<>`]*)\1/gm;
/** A quoted string, with the redirection it is the target of where it is one. A `>` inside a
 * quoted argument (`sed 's/=x/=<set>/'`) redirects nothing, so REDIRECT reads the text with
 * every quoted string that is no redirection's target emptied. */
const QUOTED = /(>>?\|?[ \t]*)?('[^']*'|"[^"]*")/g;
/** A shell word: quoted parts and bare characters up to the next blank. */
const WORD = /(?:'[^']*'|"[^"]*"|[^\s'"])+/g;

/** The absolute paths a `bash` command writes, read from its text: each redirection's target,
 * and the file operands of the WRITERS, `sed -i` and `dd of=`. A word that holds an expansion
 * (`$`, a backquote, a glob) is passed over, since its path is not in the text. */
function bashWrites(command: string): string[] {
  const code = withoutBodies(command).code;
  const written = [
    ...code
      .replace(QUOTED, (all, target) => (target === undefined ? "''" : all))
      .matchAll(REDIRECT),
  ].map((hit) => hit[2] ?? "");
  for (const hit of code.matchAll(SHELL_COMMAND)) {
    const name = path.posix.basename(hit[1] ?? "");
    const words = [...(hit[2] ?? "").replace(REDIRECTION, " ").matchAll(WORD)].map((word) =>
      word[0].replace(/^(['"])(.*)\1$/, "$2")
    );
    const operands = words.filter((word) => !word.startsWith("-"));
    if (name === "dd")
      written.push(...words.filter((word) => word.startsWith("of=")).map((word) => word.slice(3)));
    const inPlace = words.some((word) => /^(?:-i|--in-place)/.test(word));
    const kind = name === "sed" ? (inPlace ? "last" : undefined) : WRITERS[name];
    if (kind === undefined) continue;
    const target = words.indexOf("-t");
    if (kind === "last" && target >= 0) written.push(words[target + 1] ?? "");
    else if (kind === "last") written.push(operands.at(-1) ?? "");
    else written.push(...operands);
  }
  return written
    .filter((one) => one.startsWith("/") && !/[$`*?]/.test(one))
    .map((one) => path.posix.normalize(one));
}

/** Where a write leaves nothing behind: the null device and the process's own streams. */
const SINKS = /^\/(?:dev\/(?:null|stdout|stderr|tty|fd\/\d+)|proc\/self\/fd\/\d+)$/;

/** The paths a tool call writes: a `write` or `edit` call's file, and the paths `bashWrites` reads
 * out of a `bash` call. A path is normalized, so `<run>/ws/../../../tmp/x` is /tmp/x. What a
 * `bash` command can hide from this: a path held in a variable or built by a substitution; a
 * relative path after a `cd` out of the run; a program that writes a path its own arguments do not
 * name (a script, `git -C <dir>`, `jj -R <dir>`, `find -delete`, `xargs`, `python -c`, a
 * configuration home); and an `eval` cell, whose writes this does not read. rig.sh's
 * scratch_clean records what a serial run left at the top of /tmp whatever wrote it, which is the
 * record of what these hide there. */
function writtenBy(tool: string, args: ToolArgs): string[] {
  if (tool === "bash") return bashWrites(stringArg(args, "command") ?? "");
  return tool === "write" || tool === "edit" ? namedPaths(tool, args) : [];
}

/** What the run wrote outside its own directory, its HOME and its TMPDIR (`writable`): every path
 * a tool call writes there (`writtenBy`), and every entry rig.sh's scratch_clean found the run
 * had left in the machine's shared scratch (<run>/scratch-created.txt, `<epoch seconds> <path>`).
 * A run's own writes are no verdict on it; another run that reads one is (`otherRunWrite`). */
function writesOf(
  dir: string,
  calls: readonly { file: string; tool: string; args: ToolArgs; at: number }[],
  writable: readonly string[]
): Access[] {
  const created = path.join(dir, "scratch-created.txt");
  return [
    ...calls.flatMap((call) =>
      writtenBy(call.tool, call.args).map((written) => ({ ...call, path: written }))
    ),
    ...(existsSync(created) ? readFileSync(created, "utf8").split("\n") : []).flatMap((line) => {
      const at = line.indexOf(" ");
      return at < 0
        ? []
        : [
            {
              path: path.posix.normalize(line.slice(at + 1)),
              at: Number(line.slice(0, at)) * 1000,
              file: "scratch-created.txt",
              tool: "scratch_clean",
            },
          ];
    }),
  ].filter(
    (write) => !SINKS.test(write.path) && !writable.some((place) => inside(write.path, place))
  );
}

/** The paths a tool call reads: every path it names (`namedPaths`) but the ones it only writes
 * (`writtenBy`); an `edit` call reads the file it edits. */
function readsOf(tool: string, args: ToolArgs): string[] {
  const written = tool === "edit" ? [] : writtenBy(tool, args);
  return namedPaths(tool, args).filter((named) => !written.includes(named));
}

/** The directories every run on the machine shares, as rig.sh's `shared_scratch` names them: what
 * a run finds there it did not make itself is another process's, a run's of this batch or older. */
const SHARED_SCRATCH = ["/tmp"];

/** Rule 7: the first read of the run that met a world another run, or something before the run,
 * had changed:
 *   - another run's write: the latest write of the path read, or of a directory above it, across
 *     every run health was given (`runs`), before the read's time, is another run's;
 *   - a shared scratch entry from before the run: the path is under SHARED_SCRATCH, no run given
 *     wrote it before the read, and it was there when the run started: its entry at the top of
 *     the directory is in <run>/scratch-before.txt (rig.sh's scratch_before) and is none the run
 *     made itself (scratch-created.txt). A run with no such record (one from before the rig kept
 *     it) is held to its transcripts alone: a shared scratch path it reads, that no run given
 *     wrote before the read, counts as there before it, which also counts a read of a path that
 *     was never there.
 * A run that reads only what it wrote itself is clean, and so is the run whose write another
 * read: it is the reader that met a world another run had changed. The rule sees only the runs it
 * is given, so `rig.sh health` reads a batch's runs together; given one run, it sees no other
 * run's write, and every shared scratch path that run reads before writing it counts as there
 * before it. It sees a write only where `writesOf` records one, and a read only where a tool call
 * names its path (`namedPaths`). */
function otherRunWrite(run: Run, runs: readonly Run[]): string | undefined {
  for (const read of run.reads) {
    let last: { write: Access; by: Run } | undefined;
    for (const by of runs)
      for (const write of by.writes)
        if (
          inside(read.path, write.path) &&
          write.at < read.at &&
          (last === undefined || write.at > last.write.at)
        )
          last = { write, by };
    const when = new Date(read.at).toISOString();
    if (last !== undefined && last.by !== run)
      return `another run's write: a ${read.tool} call in ${read.file} at ${when} reads ${read.path}, which ${path.basename(last.by.dir)} wrote at ${new Date(last.write.at).toISOString()} (${last.write.tool} in ${last.write.file})`;
    if (last !== undefined) continue;
    const shared = SHARED_SCRATCH.find((dir) => read.path.startsWith(`${dir}/`));
    if (shared === undefined) continue;
    const entry = path.posix.join(shared, read.path.slice(shared.length + 1).split("/")[0] ?? "");
    const made = run.writes.some((write) => write.tool === "scratch_clean" && write.path === entry);
    if (made || (run.before !== undefined && !run.before.includes(entry))) continue;
    return `shared scratch from before the run: a ${read.tool} call in ${read.file} at ${when} reads ${read.path}, which no run given wrote before it${run.before === undefined ? "" : ` and ${entry} was in ${shared} when the run started (scratch-before.txt)`}`;
  }
  return undefined;
}

/** Rule 4: the first request the daemon stand-in answered 404 whose route the run's label's
 * daemon registers, as a reason; or why the routes of that daemon cannot be read, for a run that
 * has a 404 at all. A line naming no method, which the stand-in wrote before it logged one, is
 * held to every route of its path. */
function unservedRoute(run: Run): string | undefined {
  const missing = daemonLines(run.dir).filter((line) => line.status === 404);
  if (missing.length === 0) return undefined;
  let checkout: string;
  let routes: readonly string[];
  try {
    checkout = labelCheckout(run.dir);
    routes = labelRoutes(checkout);
  } catch (error) {
    const why = error instanceof Error ? error.message : String(error);
    return `routes unknown: the stand-in answered ${missing[0]?.path} 404, and which routes the ${run.label} label's daemon registers cannot be read: ${why}`;
  }
  const line = missing.find((one) => serves(routes, one.method, one.path));
  if (line === undefined) return undefined;
  return `unserved route: the stand-in answered ${line.method ?? "a request of"} ${line.path} 404, a route the ${run.label} label's daemon registers (${checkout})`;
}

/** Rule 5: the first turn outcome the daemon stand-in answered with `failed` (its line's
 * `failed`), as a reason. */
function failedClaim(run: Run): string | undefined {
  const failed = daemonLines(run.dir).find((line) => line.failed === true);
  return failed === undefined
    ? undefined
    : `failed claim: the stand-in answered the ${failed.role}'s turn outcome at ${failed.at} with failed, so the daemon would have failed that claim`;
}

/** Every rule, in the order a run's line names the first it fails. A rule is given the run and
 * every run health was given, which only the cross-run rule reads. */
const RULES: readonly {
  readonly name: string;
  readonly judge: (run: Run, runs: readonly Run[]) => string | undefined;
}[] = [
  {
    name: "finished",
    judge: (run) => (finished(run.dir) ? undefined : "unfinished: out.txt has no exit= line"),
  },
  {
    name: "completions",
    judge: (run) => {
      const refused = refusedCompletion(daemonLines(run.dir), run.scenario, run.label);
      if (refused === undefined) return undefined;
      if ("unkeyed" in refused)
        return `unkeyable completion: ${refused.unkeyed} (POST ${refused.line.path} answered ${answerOf(refused.line)}), so whose completion it was is unknown`;
      return `refused completion: POST ${refused.line.path} of the ${refused.role} answered ${answerOf(refused.line)}, which the ${run.label} label's daemon does not answer it with in this world`;
    },
  },
  {
    name: "taken",
    judge: (run) => {
      const taken = takenCompletions(daemonLines(run.dir), run.scenario, run.label);
      return taken !== undefined && taken.count === 0
        ? `no completion taken: no completion of the ${taken.role} was answered as the ${run.label} label's daemon answers one it takes`
        : undefined;
    },
  },
  { name: "routes", judge: unservedRoute },
  { name: "failed claim", judge: failedClaim },
  { name: "push guard", judge: guardRefusal },
  { name: "gh", judge: ghUndeclared },
  { name: "another run", judge: otherRunReach },
  { name: "another run's write", judge: otherRunWrite },
  { name: "outside its world", judge: outsideWorld },
  { name: "another process", judge: otherProcess },
  { name: "credentials", judge: credentialReach },
  { name: "off-world network", judge: offWorld },
];

if (import.meta.main) {
  const argv = Bun.argv.slice(2);
  // The shared scratch's entries before the batch's first run, one path a line: what a run with
  // no record of its own (scratch-before.txt) is held to, and added to the record of one that has.
  let snapshot: string[] | undefined;
  if (argv[0] === "--tmp-snapshot") {
    const file = argv[1];
    if (file === undefined || !existsSync(file)) {
      console.error(`health: --tmp-snapshot names no file${file === undefined ? "" : `: ${file}`}`);
      console.error(USAGE);
      process.exit(2);
    }
    snapshot = readFileSync(file, "utf8")
      .split("\n")
      .map((line) => line.trim())
      .filter((line) => line !== "");
    argv.splice(0, 2);
  }
  if (argv.length === 0) {
    console.error(USAGE);
    process.exit(2);
  }
  const named = argv.map((dir) => ({ dir, run: runOf(dir) }));
  const bad = named.find((one) => typeof one.run === "string");
  if (bad !== undefined) {
    console.error(`health: ${String(bad.run)}`);
    console.error(USAGE);
    process.exit(2);
  }
  const runs = named.flatMap(({ dir, run }) => {
    if (typeof run === "string") return [];
    const loaded = loadRun(dir, run.scenario, run.label);
    return [
      snapshot === undefined
        ? loaded
        : { ...loaded, before: [...(loaded.before ?? []), ...snapshot] },
    ];
  });
  let failed = 0;
  const flagged = new Map(RULES.map((rule) => [rule.name, 0]));
  for (const run of runs) {
    const reasons = RULES.flatMap((rule) => {
      // A record of the run that does not parse (a daemon.log line that is no JSON, say) is a run
      // whose world cannot be read, which is no healthy one.
      let reason: string | undefined;
      try {
        reason = rule.judge(run, runs);
      } catch (error) {
        reason = `unreadable record: ${error instanceof Error ? error.message : String(error)}`;
      }
      if (reason === undefined) return [];
      flagged.set(rule.name, (flagged.get(rule.name) ?? 0) + 1);
      return [reason];
    });
    if (reasons.length > 0) failed += 1;
    console.log(
      `${path.basename(run.dir)}  ${reasons[0] === undefined ? "healthy" : `unhealthy: ${reasons[0]}`}`
    );
  }
  for (const [name, count] of flagged)
    console.log(`health: ${name}: ${count} of ${runs.length} runs`);
  // A run's own writes outside its directory are no verdict on it: recorded here, and judged
  // where another run read them (`otherRunWrite`).
  console.log(
    `health: wrote outside its own directory, HOME and TMPDIR (no verdict): ${runs.filter((run) => run.writes.length > 0).length} of ${runs.length} runs`
  );
  console.log(`health: ${failed} of ${runs.length} runs unhealthy`);
  process.exit(failed === 0 ? 0 : 1);
}
