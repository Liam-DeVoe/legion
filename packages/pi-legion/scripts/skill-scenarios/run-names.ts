// What the rig calls a run, kept where no agent of the run can read it, and the words a run's
// world and its agents' own words are held to. A run's directory is named by an opaque id
// (rig.sh's cmd_run), so its path, and every path, variable and process built on it, names
// nothing of the measurement; the run's scenario, case, label and number are in its record,
// <work>/runs/<id>.json beside the directory (`recordFile`), which the run's namespace covers with
// the rest of the work directory. Every reader of a run's scenario, case or label reads it here
// (`runIdentity`): rig.sh, the readers (health.ts, boot-refusal.ts, completion-answers.ts), the
// scorer and judge.ts. It imports nothing of the scorer or of health: the daemon stand-in loads it
// at every boot, through completion-answers.ts.
//
//   run-names.ts case <scenario>            prints the case the scenario's own variable names
//                                           (SCENARIOS), nothing for a scenario that takes none;
//                                           exits 1, naming the variable and every case, for a
//                                           variable that names none, and 2 for no scenario of
//                                           SCENARIOS
//   run-names.ts guard <run dir> <sessions dir> <work dir>
//                                           exits 0 when nothing the run's agent is about to read
//                                           names a word of the vocabulary (`worldNames`), and 1,
//                                           printing the first word and where it is, when
//                                           something does: rig.sh's launch asks before every
//                                           agent starts, and starts none of a world that names
//                                           one
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import { z } from "zod";

/** Every scenario rig.sh runs, by the name `rig.sh batch` and `rig.sh run` take: the variable
 * that names its case (rig.sh's scenario_case reads it through `caseOf`), the cases it takes, in
 * the order a refusal lists them, and the case an unset variable gives, where unset is a case.
 * A scenario with no cases takes none. It is the rig's one scenario table: the cases a run may be
 * built for, the cases score.ts and health read back, and the words the vocabulary holds
 * (`vocabulary`) are all this table. */
export const SCENARIOS: Readonly<
  Record<
    string,
    { readonly variable?: string; readonly cases: readonly string[]; readonly unset?: string }
  >
> = {
  "ask-on-message": { cases: [] },
  "measure-before-ask": { cases: [] },
  "tester-proof": { cases: [] },
  "reviewer-text-findings": {
    variable: "SKILL_SCENARIOS_REVIEWER_CASE",
    cases: ["text-only", "defect"],
  },
  // Unset is text-fixes, the scenario's measured arm, which the check that runs it names no
  // variable for.
  "threads-closed": {
    variable: "SKILL_SCENARIOS_THREADS_CASE",
    cases: ["text-fixes", "code-defect"],
    unset: "text-fixes",
  },
  brainstorm: { cases: [] },
  askgate: {
    variable: "SKILL_SCENARIOS_ASKGATE_CASE",
    cases: [
      "facts",
      "link",
      "upload",
      "nochange",
      "nochange-rebuttal",
      "nochange-block",
      "untried",
      "tried",
    ],
  },
  "spec-writing": { cases: [] },
  "spec-rewrite": { cases: [] },
};

/** The cases of `scenario`, none for a scenario that takes none or that SCENARIOS does not name. */
export function casesOf(scenario: string): readonly string[] {
  return SCENARIOS[scenario]?.cases ?? [];
}

/** The case `env` names for `scenario` through the scenario's own variable, "" for a scenario
 * that takes none. Throws, naming the variable, the value and every case (`a, b or c`), for a
 * value that is no case of the scenario's, and for no scenario of SCENARIOS. */
export function caseOf(
  scenario: string,
  env: Readonly<Record<string, string | undefined>>
): string {
  const entry = SCENARIOS[scenario];
  if (entry === undefined) throw new Error(`unknown scenario ${scenario}`);
  if (entry.variable === undefined) return "";
  const given = env[entry.variable] ?? "";
  const value = given === "" ? (entry.unset ?? "") : given;
  if (!entry.cases.includes(value))
    throw new Error(
      `${entry.variable} is '${given}', not ${entry.cases.slice(0, -1).join(", ")} or ${entry.cases.at(-1)}`
    );
  return value;
}

/** The shape of a run's id, its directory's own name (rig.sh's cmd_run): twelve hex digits, which
 * name nothing. */
export const RUN_ID = /^[0-9a-f]{12}$/;

/** A run's record: what the rig calls the run, which nothing the run's agents can read names.
 * rig.sh's cmd_run writes it, before anything of the run starts, to <work>/runs/<id>.json beside
 * the run's directory, and archive_run moves it with the directory. The fields:
 *   run       the run's id, its directory's own name;
 *   scenario  a scenario of SCENARIOS;
 *   case      its case, "" for a scenario that takes none;
 *   label     the label whose checkout's skills the run's agents load;
 *   n         the run's number among the runs of its scenario, case and label in the work
 *             directory (rig.sh's next_run);
 *   name      `<scenario>[-<case>]-<label>-<n>`, the name a run's directory had before the names
 *             left the world, for a person reading the record;
 *   batch     the batch the run was started in: the work directory's tag and the batch's start;
 *   ran       the run's directory as it ran, the path its transcripts and pane name, which differs
 *             from the directory read once the run is archived or copied;
 *   mode      askgate's SKILL_SCENARIOS_ASKGATE_MODE where one was set. */
export const RunRecord = z.strictObject({
  run: z.string().regex(RUN_ID),
  scenario: z.string(),
  case: z.string(),
  label: z.string().regex(/^[a-z0-9][a-z0-9-]*$/),
  n: z.number().int().positive(),
  name: z.string(),
  batch: z.string(),
  ran: z.string().startsWith("/"),
  mode: z.enum(["warn", "block"]).optional(),
});
export type RunRecord = z.infer<typeof RunRecord>;

/** The record of the run whose directory is `runDir`: beside it, `<run dir>.json`. */
export function recordFile(runDir: string): string {
  return `${path.resolve(runDir)}.json`;
}

/** What a run is: its scenario, case, label and number, the name a person reads it by, and where
 * its record says it ran (undefined for a run read from its name, whose pane names that). */
export interface RunIdentity {
  readonly scenario: string;
  readonly case: string;
  readonly label: string;
  readonly n: number;
  readonly name: string;
  readonly ran: string | undefined;
  /** Whether the identity was read from the run's record (`recordFile`), or, for a run recorded
   * before the names left the world, from its directory's name. */
  readonly recorded: boolean;
  readonly mode: string | undefined;
}

/** The scenario, case, label and number a name `<scenario>[-<case>]-<label>-<n>` gives: the
 * longest scenario of SCENARIOS it starts with, then, for a cased scenario, the longest of its
 * cases (`nochange-block` before `nochange`), and the rest before the number is the label. Why
 * it gives none, for a name of no such shape. */
function identityOfName(name: string): Omit<RunIdentity, "ran" | "recorded" | "mode"> | string {
  const scenario = Object.keys(SCENARIOS)
    .filter((one) => name.startsWith(`${one}-`))
    .sort((a, b) => b.length - a.length)[0];
  if (scenario === undefined) return `${name} names no scenario rig.sh runs`;
  const number = /-(\d+)$/.exec(name);
  if (number === null) return `${name} ends with no run number`;
  const middle = name.slice(scenario.length + 1, number.index);
  const cases = casesOf(scenario);
  const runCase =
    cases.length === 0
      ? ""
      : [...cases].sort((a, b) => b.length - a.length).find((one) => middle.startsWith(`${one}-`));
  if (runCase === undefined)
    return `${name} names no case of ${scenario}: expected ${scenario}-<case>-<label>-<n>`;
  const label = runCase === "" ? middle : middle.slice(runCase.length + 1);
  if (label === "") return `${name} names no label`;
  return { scenario, case: runCase, label, n: Number(number[1]), name };
}

/** The identity of the run whose directory is `runDir`, or why it has none. A run with a record
 * (`recordFile`) is read from the record alone, whose run must be the directory's own name and
 * whose scenario and case SCENARIOS must hold. A run recorded before the names left the world
 * has no record, and its directory's name is the only place its scenario, case and label were
 * ever kept: it is read from that name (`identityOfName`). A directory named by a run id with no
 * record is no run anyone can read. */
export function runIdentity(runDir: string): RunIdentity | string {
  const base = path.basename(path.resolve(runDir));
  const file = recordFile(runDir);
  if (existsSync(file)) {
    const parsed = RunRecord.safeParse(JSON.parse(readFileSync(file, "utf8")));
    if (!parsed.success) return `${file} is no run record: ${z.prettifyError(parsed.error)}`;
    const record = parsed.data;
    if (record.run !== base) return `${file} is the record of run ${record.run}, not of ${base}`;
    const cases = casesOf(record.scenario);
    if (SCENARIOS[record.scenario] === undefined)
      return `${file} names the scenario ${record.scenario}, which rig.sh does not run`;
    if (cases.length === 0 ? record.case !== "" : !cases.includes(record.case))
      return `${file} names the case '${record.case}', which is no case of ${record.scenario}`;
    return {
      scenario: record.scenario,
      case: record.case,
      label: record.label,
      n: record.n,
      name: record.name,
      ran: record.ran,
      recorded: true,
      mode: record.mode,
    };
  }
  if (RUN_ID.test(base)) return `${runDir} is named by a run id and has no record ${file}`;
  // A run recorded before the names left the world: its name is its record.
  const named = identityOfName(base);
  return typeof named === "string"
    ? named
    : { ...named, ran: undefined, recorded: false, mode: undefined };
}

/** `runIdentity`, or a throw naming why the run has none. */
export function identityOf(runDir: string): RunIdentity {
  const identity = runIdentity(runDir);
  if (typeof identity === "string") throw new Error(identity);
  return identity;
}

/** The names the architect's ruling of 2026-11-16T09:0xZ took out of everything an agent can
 * read, each traced to the sentences of the batch at c273084d that called the world a test: the
 * seeded Dispatch sessions (`rig-<role>`), the work directory's own name (`skill-scenarios`, its
 * stem, so SKILL_SCENARIO_RUN is one too), the Dispatch project key (`lweval`), the seeding
 * session (`e2e-seed`) and the repository (`example/widgets`). Each is matched where a word
 * starts, whatever follows it (`lweval-1`, `lweval1`, `rig-tester`). */
export const REMOVED_NAMES = ["rig-", "skill-scenario", "lweval", "e2e-seed", "example/widgets"];

/** The Dispatch project every run's own issue is filed in (seed.ts), the same for every label: a
 * key of the shape a team's project has, for the widgets repository the world is on. It was
 * `LWEVAL`. */
export const WORLD_PROJECT = "WID";

/** The words a run's world is held to, derived from SCENARIOS and the labels given: every
 * scenario, every case and every label. A word with a character that is no letter or digit
 * (`text-only`, `threads-closed`) names the measurement wherever it stands; a plain word (`head`,
 * `base`, `defect`, `askgate`) is also an ordinary word, and names it only as a part of a name or
 * a path, or beside another word of the measurement (`proseNames`). The removed names
 * (REMOVED_NAMES) are distinctive. */
export interface Vocabulary {
  readonly scenarios: readonly string[];
  readonly cases: readonly string[];
  readonly labels: readonly string[];
  readonly distinctive: readonly string[];
  readonly ordinary: readonly string[];
}

export function vocabulary(labels: readonly string[]): Vocabulary {
  const scenarios = Object.keys(SCENARIOS);
  const cases = [...new Set(scenarios.flatMap((scenario) => casesOf(scenario)))];
  const all = [...new Set([...scenarios, ...cases, ...labels].map((word) => word.toLowerCase()))];
  return {
    scenarios,
    cases,
    labels: [...new Set(labels)],
    distinctive: all.filter((word) => /[^a-z0-9]/.test(word)),
    ordinary: all.filter((word) => !/[^a-z0-9]/.test(word)),
  };
}

const literal = (word: string) => word.replace(/[.*+?^${}()|[\]\\/]/g, "\\$&");
/** A text as the words are matched in it: lower case, an underscore read as a hyphen. */
const folded = (text: string) => text.toLowerCase().replaceAll("_", "-");

/** The first distinctive word of `vocab` in `text`, or of REMOVED_NAMES when `removed` holds:
 * a vocabulary word standing alone (no letter or digit on either side), a removed name where a
 * word starts. */
function distinctiveIn(text: string, vocab: Vocabulary, removed: boolean): string | undefined {
  const lower = folded(text);
  for (const word of vocab.distinctive)
    if (new RegExp(`(?<![a-z0-9])${literal(word)}(?![a-z0-9])`).test(lower)) return word;
  if (removed)
    for (const word of REMOVED_NAMES)
      if (new RegExp(`(?<![a-z0-9])${literal(word)}`).test(lower)) return word;
  return undefined;
}

/** The first word of `vocab` in `value`, a name or a path an agent can read (a path, an
 * environment variable's name, a profile, a project, an issue key, a topic): any distinctive word
 * or removed name, and any plain word that is a whole part of it, the parts being the runs of
 * letters and digits (`skills-head` holds `head`; `refs/heads` does not). */
export function nameWords(value: string, vocab: Vocabulary): string | undefined {
  const found = distinctiveIn(value, vocab, true);
  if (found !== undefined) return found;
  const parts = new Set(value.toLowerCase().split(/[^a-z0-9]+/));
  return vocab.ordinary.find((word) => parts.has(word));
}

/** The first distinctive word or removed name in `text`, free text an agent can read (a prompt,
 * a variable's value that is no path, a file of the world): a plain word there is the work's own
 * word as often as not, so it is read only in a name (`nameWords`). */
export function textWords(text: string, vocab: Vocabulary): string | undefined {
  return distinctiveIn(text, vocab, true);
}

/** The first word of the measurement an agent's own sentence says, or undefined: a distinctive
 * scenario, case or label word (`text-only`, `threads-closed`); a run's name in rig.sh's old shape
 * (`askgate-facts-on-1`); or a case word and a label word side by side, as a run's name puts them
 * (`defect head`, `base text-only`). A plain word alone is the work's own (`head SHA`, `base
 * branch`, `not a defect`), and the removed names are the world's, read where the world is
 * (`worldNames`), not in what an agent says. */
export function proseNames(sentence: string, vocab: Vocabulary): string | undefined {
  const lower = folded(sentence);
  for (const word of vocab.distinctive)
    if (new RegExp(`(?<![a-z0-9-])${literal(word)}(?![a-z0-9-])`).test(lower)) return word;
  const alt = (words: readonly string[]) =>
    [...words]
      .map((word) => literal(word.toLowerCase()))
      .sort((a, b) => b.length - a.length)
      .join("|");
  if (vocab.labels.length === 0) return undefined;
  const scenarios = alt(vocab.scenarios);
  const cases = alt(vocab.cases);
  const labels = alt(vocab.labels);
  const run = new RegExp(
    `(?<![a-z0-9-])(?:${scenarios})(?:-(?:${cases}))?-(?:${labels})-\\d+(?![a-z0-9-])`
  ).exec(lower);
  if (run !== null) return run[0];
  const pair = new RegExp(
    `(?<![a-z0-9-])(?:(?:${cases})[ -](?:${labels})|(?:${labels})[ -](?:${cases}))(?![a-z0-9-])`
  ).exec(lower);
  return pair?.[0];
}

/** The labels the work directory `work` holds a profile for (`rig.sh profile`), by the names
 * under <work>/profiles. */
export function workLabels(work: string): string[] {
  const profiles = path.join(work, "profiles");
  return existsSync(profiles)
    ? readdirSync(profiles).filter((label) => existsSync(path.join(profiles, label, "checkout")))
    : [];
}

/** Where `rig.sh profile` installed `label`'s profile, its HOME and its plugins, which every run of
 * the label reads its skills from: <at>/installs/<id>, the id held in <work>/profiles/<label>/install
 * (rig.sh's cmd_profile), so no path a run's agents read names the label; <at>/profiles/<label>
 * itself for a label built before the names left the world, which installed there. `at` is the
 * work directory the run had when it ran, `work` the one read, which differ for a run read from a
 * copy. */
export function labelInstall(work: string, label: string, at = work): string {
  const id = path.join(work, "profiles", label, "install");
  return existsSync(id)
    ? path.join(at, "installs", readFileSync(id, "utf8").trim())
    : path.join(at, "profiles", label);
}

/** The variables of a pane file, KEY=value a line, as `env -i` takes them, the last of two. */
function paneVars(file: string): Map<string, string> {
  const vars = new Map<string, string>();
  if (!existsSync(file)) return vars;
  for (const line of readFileSync(file, "utf8").split("\n")) {
    const equals = line.indexOf("=");
    if (equals > 0) vars.set(line.slice(0, equals), line.slice(equals + 1));
  }
  return vars;
}

/** The variables whose value is a name a reader of the world takes for a name rather than text:
 * the profile, the Legion project, issue and tree, the role, and the repository. A value with a
 * `/` in it is a path or a list of paths, and is read as a name too. */
const NAMED_VARS: Record<string, true> = {
  OMP_PROFILE: true,
  PI_PROFILE: true,
  LEGION_PROJECT: true,
  LEGION_ISSUE: true,
  LEGION_TREE: true,
  LEGION_ROLE: true,
  GH_REPO: true,
};

/** Where a run's world names a word, and the word: one place of everything the agent about to
 * start can read, or undefined for a world that names none. */
export interface Named {
  readonly place: string;
  readonly word: string;
  readonly value: string;
}

/** The paths the run's namespace mounts, as private_world mounts them from <run>/namespace.txt
 * (rig.sh's world_paths): each cover and credentials path that is a directory and lies under no
 * cover mounted before it (one that does is gone once that cover is mounted, and is skipped),
 * each rw and ro path that is a directory, at its own path and from its own path, and the run's
 * tmp, which is bound onto /tmp. Each is a mount point the agent's /proc/self/mountinfo lists,
 * and each bound path is also the source that line names. */
export function mountedPaths(runDir: string): string[] {
  const file = path.join(runDir, "namespace.txt");
  if (!existsSync(file)) return [];
  const isDir = (one: string) => existsSync(one) && statSync(one).isDirectory();
  const covered: string[] = [];
  const mounted: string[] = [];
  const lines = readFileSync(file, "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => {
      const space = line.indexOf(" ");
      return { kind: line.slice(0, space), at: line.slice(space + 1) };
    });
  for (const { kind, at } of lines)
    if ((kind === "cover" || kind === "credentials") && isDir(at)) {
      if (covered.some((one) => at === one || at.startsWith(`${one}/`))) continue;
      covered.push(at);
      mounted.push(at);
    }
  for (const { kind, at } of lines)
    if ((kind === "rw" || kind === "ro") && isDir(at)) mounted.push(at);
  mounted.push(path.join(runDir, "tmp"));
  return mounted;
}

/** The largest file the guard reads: a file an agent reads at a glance is far smaller, and a
 * larger one in a run's world is a repository's pack or a build. */
const FILE_BOUND = 1 << 20;

/** The text of `file` where it is text an agent reads: at most FILE_BOUND bytes, with no NUL byte
 * in its first 8 KiB (a binary file or a git object is passed over). */
function textOf(file: string): string | undefined {
  if (statSync(file).size > FILE_BOUND) return undefined;
  const bytes = readFileSync(file);
  return bytes.subarray(0, 8192).includes(0) ? undefined : bytes.toString("utf8");
}

/** The rig's records of a run that are files at the top of its directory: they name the rig's
 * own paths (built-from, services-from), the world's layout (namespace.txt, launch.sh, the pane
 * and its prompts) or the batch's (scratch-before.txt, dispatch.json, the seed's fixture). No
 * process of the run reads them, and a gVisor pod refuses the mount onto a file that would hide
 * them (rig.sh's world_paths), so an agent can read them; `shownFiles` passes them over, since
 * services-from names the rig's own directory in every run. The pane and its prompts are read
 * as the agent's environment and command line instead (`worldNames`). */
export const RECORDS = [
  "built-from",
  "services-from",
  "namespace.txt",
  "launch.sh",
  "scratch-before.txt",
  "scratch-created.txt",
  "system-args",
  "prompt.txt",
  "pane.env",
  "worker.env",
  "dispatch.json",
  "fixture.json",
  "fixture.log",
  "booted",
];

/** Every regular file the run's world shows under the work directory `work`, which is the rig's
 * own: the run's directory, its label's install and the batch's tokens (<run>/namespace.txt's rw
 * and ro paths there), less each path the world hides there (`hide`, rig.sh's world_paths) and the
 * rig's records (RECORDS), walked without following a link. The tool directories the world shows
 * are the operator's own, and are not read. */
export function shownFiles(runDir: string, work: string): string[] {
  const file = path.join(runDir, "namespace.txt");
  if (!existsSync(file)) return [];
  const lines = readFileSync(file, "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => ({
      kind: line.slice(0, line.indexOf(" ")),
      at: line.slice(line.indexOf(" ") + 1),
    }));
  const hidden = new Set([
    ...lines.filter(({ kind }) => kind === "hide").map(({ at }) => at),
    ...RECORDS.map((record) => path.join(runDir, record)),
  ]);
  const seen = new Set<string>();
  const found: string[] = [];
  const walk = (dir: string) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const at = path.join(dir, entry.name);
      if (hidden.has(at) || seen.has(at)) continue;
      seen.add(at);
      if (entry.isDirectory()) walk(at);
      else if (entry.isFile()) found.push(at);
    }
  };
  for (const { kind, at } of lines)
    if (
      (kind === "rw" || kind === "ro") &&
      (at === work || at.startsWith(`${work}/`)) &&
      !hidden.has(at) &&
      existsSync(at) &&
      statSync(at).isDirectory()
    )
      walk(at);
  return found;
}

/** The first word of the vocabulary (`vocabulary` of `labels`) in what the agent about to start
 * in `runDir` can read, as rig.sh's launch has written it: every variable of its pane file, its
 * name and its value (a value with a `/`, and a NAMED_VARS value, read as a name: `nameWords`;
 * any other as text: `textWords`), which holds its profile, its Legion project, issue and tree
 * (so the Envoy topics they make), its HOME and TMPDIR; every path its namespace mounts
 * (`mountedPaths`); its working directory and session directory; its prompt and its system
 * prompt, which its command line, /proc/1's, carries; and every file its world shows under the
 * work directory `work` (`shownFiles`), its stand-ins in <run>/bin and the gh bundle in <run>/lib
 * among them, which a `cat` shows, each named with the text around the word. */
export function worldNames(
  runDir: string,
  sessionsDir: string,
  labels: readonly string[],
  work: string
): Named | undefined {
  const vocab = vocabulary(labels);
  const checks: { place: string; value: string; name: boolean }[] = [];
  for (const [key, value] of paneVars(path.join(runDir, "pane.env"))) {
    checks.push({ place: `the variable name ${key}`, value: key, name: true });
    checks.push({
      place: `the variable ${key}`,
      value,
      name: NAMED_VARS[key] === true || value.includes("/"),
    });
  }
  for (const mounted of mountedPaths(runDir))
    checks.push({ place: "a mount of its namespace", value: mounted, name: true });
  checks.push({ place: "its working directory", value: path.join(runDir, "cwd"), name: true });
  checks.push({ place: "its session directory", value: sessionsDir, name: true });
  for (const file of ["prompt.txt", "system-args"]) {
    const at = path.join(runDir, file);
    if (existsSync(at))
      checks.push({ place: `<run>/${file}`, value: readFileSync(at, "utf8"), name: false });
  }
  for (const { place, value, name } of checks) {
    const word = name ? nameWords(value, vocab) : textWords(value, vocab);
    if (word !== undefined) return { place, word, value };
  }
  for (const file of shownFiles(runDir, work)) {
    const text = textOf(file);
    const word = text === undefined ? undefined : textWords(text, vocab);
    if (text === undefined || word === undefined) continue;
    const at = Math.max(0, folded(text).search(literal(word)));
    const place = file.startsWith(`${runDir}/`) ? `<run>/${path.relative(runDir, file)}` : file;
    return { place, word, value: text.slice(Math.max(0, at - 60), at + 140) };
  }
  return undefined;
}

if (import.meta.main) {
  const [command, ...rest] = Bun.argv.slice(2);
  if (command === "case" && rest.length === 1) {
    const scenario = rest[0] ?? "";
    if (SCENARIOS[scenario] === undefined) {
      console.error(`run-names.ts: unknown scenario ${scenario}`);
      process.exit(2);
    }
    try {
      const found = caseOf(scenario, process.env);
      if (found !== "") console.log(found);
    } catch (error) {
      console.error(error instanceof Error ? error.message : String(error));
      process.exit(1);
    }
  } else if (command === "guard" && rest.length === 3) {
    const [runDir = "", sessionsDir = "", work = ""] = rest;
    const named = worldNames(runDir, sessionsDir, workLabels(work), work);
    if (named !== undefined) {
      console.error(
        `the world of ${path.basename(runDir)} names "${named.word}" in ${named.place}: ${named.value.slice(0, 200)}`
      );
      process.exit(1);
    }
  } else {
    console.error(
      "usage: run-names.ts case <scenario> | guard <run dir> <sessions dir> <work dir>"
    );
    process.exit(2);
  }
}
