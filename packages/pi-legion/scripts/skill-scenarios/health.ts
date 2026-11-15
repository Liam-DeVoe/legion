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
 *  13. off-world network: no `web_search` call, and no `bash`, `read` or `fetch` call that reaches
 *      a host other than the loopback, the hosts its pane's URLs name and the model gateway its
 *      profile's models.yml names (`offWorld`).
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

/** The status line of the world's one refusing route (rig.sh's fixture_write): GitHub's answer to
 * the protection of a branch that has none. */
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

/** The gh refusals each scenario provokes on purpose, or that a correct agent meets in it, by the
 * role that meets them; every other refusal is unhealthy (rule 5). Both labels' worlds are one
 * GitHub, so the table names no label; calls.jsonl records no phase, so it names none. Declared:
 *   - the reviewer's PROTECTION_404, in both scenarios: the merge gate has the reviewer read the
 *     base branch's protection (skills/legion-worker/references/merge-gate.md), every world's
 *     base is unprotected, and GitHub answers that read 404 "Branch not protected";
 *   - the reviewer's CREATE_422 and SUBMIT_422, in both scenarios: the create and the submit of a
 *     review are the reviewer's own step in both worlds, and the stand-in refuses one exactly
 *     where GitHub refuses it, so a reviewer that met one met GitHub.
 * Not declared: any refusal of the implementer, which reads no protection and makes no review in
 * threads-closed; any UNROUTED, which is the world missing an answer; and any refusal of a route
 * the worlds add later, until a row here says why a correct agent meets it. */
const GH_DECLARED: Readonly<Record<string, Readonly<Record<string, readonly string[]>>>> = {
  "reviewer-text-findings": { reviewer: [PROTECTION_404, CREATE_422, SUBMIT_422] },
  "threads-closed": { reviewer: [PROTECTION_404, CREATE_422, SUBMIT_422] },
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
 *     stderr's first line), and otherwise was answered, its status being its `--jq`'s or
 *     `--json`'s over that answer;
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
    return (route.stderr ?? `exit ${route.exit}`).split("\n")[0];
  }
  if (argv[0] === "pr" && (argv[1] === "checks" || argv[1] === "diff")) return undefined;
  if (argv[0] === "api" && methodOf(argv) === "GET" && argv.some((arg) => OWN_READ.test(arg)))
    return undefined;
  return UNROUTED;
}

/** Rule 5: the first `gh` call of calls.jsonl the stand-in refused with a refusal GH_DECLARED does
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

/** Every host a `bash` command reaches, with the text that names it: the host of every network
 * URL in it but those in text it writes (here-document bodies, and the values of TEXT_FLAG and of
 * a `body`, `title` or `message` FIELD_FLAG), the scp-form remote of a `git` or `jj` command, a
 * curl or wget operand that names a host without a scheme, and the host a `gh api --hostname`
 * names. */
function bashHosts(command: string): { host: string; named: string }[] {
  const code = withoutBodies(command).code;
  const reached = code
    .replace(TEXT_FLAG, (_all, lead, flag, separator) => `${lead}${flag}${separator}''`)
    .replace(FIELD_FLAG, (all, lead, flag, separator, value) =>
      /^['"]?(?:body|title|message)=/.test(value) ? `${lead}${flag}${separator}''` : all
    );
  const hosts = [...reached.matchAll(NETWORK_URL)].map((hit) => ({
    host: hostName(hit[1] ?? ""),
    named: hit[0],
  }));
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

/** Rule 13: the first call of the run that reaches a host off its world (`worldHosts`):
 * a `bash` command's (`bashHosts`), a `read` or `fetch` of an http(s) URL, and every `web_search`
 * call, which always leaves it. No tool is taken out of a profile for this: a profile without a
 * tool is a world that differs from production. */
function offWorld(run: Run): string | undefined {
  const hosts = worldHosts(run);
  const allowed = [...hosts].join(", ");
  for (const call of run.calls) {
    if (call.tool === "web_search")
      return `off-world network: a web_search call in ${call.file} searches the web, which no host of the run's world serves`;
    const reached =
      call.tool === "bash"
        ? bashHosts(stringArg(call.args, "command") ?? "")
        : ["read", "fetch", "web_fetch"].includes(call.tool)
          ? [stringArg(call.args, "url") ?? stringArg(call.args, "path") ?? ""].flatMap((one) =>
              /^https?:\/\//i.test(one)
                ? [...one.matchAll(NETWORK_URL)].map((hit) => ({
                    host: hostName(hit[1] ?? ""),
                    named: one,
                  }))
                : []
            )
          : [];
    const off = reached.find((one) => !hosts.has(one.host));
    if (off !== undefined)
      return `off-world network: a ${call.tool} call in ${call.file} reaches ${off.host} (${off.named}), no host of the run's world (${allowed})`;
  }
  return undefined;
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
    const words = [
      ...(hit[2] ?? "").replace(/(?:\d+|&)?[<>]{1,2}\|?[ \t]*\S*/g, " ").matchAll(WORD),
    ].map((word) => word[0].replace(/^(['"])(.*)\1$/, "$2"));
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
