/**
 * The rig's reading of the Legion daemon: `daemon-pane.go`, run as a main inside a checkout's
 * daemon module, which is how it reaches the daemon's internal packages (a build of it there,
 * `SKILL_SCENARIOS_DAEMON_PANE`, or `go run -overlay`), and `daemon-routes.go`,
 * `daemon-completion.go` and `gh-selectors.go`, run with `go run` outside every module, since
 * reading the routes, how the daemon takes a completion and the selector table of its gh wrapper
 * needs only the daemon's files. The rig asks them rather than restating the daemon: a change to
 * the daemon reaches the rig by construction. The one exception is `daemon-pane.go`'s own table
 * of the handoff words, which the program carries because one program runs against both checkouts
 * of a comparison and the daemon's table has not always had the same name; its comment says so.
 * `workerPane` builds a phase worker's whole pane environment over it, `standinFacts` reads every
 * fact the daemon stand-in answers by, and `labelGhSelectors` what the gh stand-in refuses an
 * owner by.
 *
 * Usage: `bun daemon-pane.ts facts <checkout> <role>[:<phase>]...` reads, through the build
 * `SKILL_SCENARIOS_DAEMON_PANE` names, the facts `standinFacts` reads for a stand-in of each role
 * booted in each phase (none: the role's own), and the checkout's gh selectors
 * (`labelGhSelectors`), into the facts cache under `SKILL_SCENARIOS_WORK`, so a stand-in's boot
 * after it reads each out of a file (rig.sh's label_facts). It exits 1, naming the fact, when one
 * cannot be read, and 2 on a usage error or with no build or no work directory named. `bun
 * daemon-pane.ts gh-selectors <checkout> <file>` writes the checkout's gh selectors to `file`
 * (gh-selectors.ts's RUN_SELECTORS, rig.sh's worker_boot), exiting 1 when they cannot be read.
 * `bun daemon-pane.ts review-round <checkout> <commit> <entry>` prints the two sentences the
 * checkout's daemon ends a reviewer's task in `reviewing` with, for the commit its row holds (""
 * for none) and the entry it stands at (0 for none), and nothing for a daemon that writes none
 * (`reviewRoundSentences`, rig.sh's run_reviewer_text_findings), exiting 1 when its source is of
 * no shape this reads.
 */
import { createHash, randomInt, randomUUID } from "node:crypto";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import os from "node:os";
import path from "node:path";
import { isLegionRole, type LegionRole } from "@legion/contracts";
import { DEFAULT_ENVOY_URL } from "@legion/envoy-client/envoy-settings";
import { sessionConfigFile, sessionEnvoySettings } from "@legion/envoy-client/session-settings";
import {
  LATE_ROLES,
  type LabelDaemon,
  NOT_CURRENT_PHASE,
  RECORD_ROUTES,
} from "./completion-answers";
import { type GhSelectors, type GhTable, selectorsOf, tableOf } from "./gh-selectors";
import { Refused } from "./run-records";

/** This checkout's daemon module, the one the rig reads unless a run names another checkout's. */
export const DAEMON_MODULE = path.resolve(import.meta.dir, "../../../daemon");

const PROGRAM = path.join(import.meta.dir, "daemon-pane.go");
const ROUTES_PROGRAM = path.join(import.meta.dir, "daemon-routes.go");
const COMPLETION_PROGRAM = path.join(import.meta.dir, "daemon-completion.go");
const GH_SELECTORS_PROGRAM = path.join(import.meta.dir, "gh-selectors.go");

/** Runs `command` in `cwd` under the caller's environment and `env` over it, `input` on its stdin
 * (the caller's own stdin when there is none), and returns its stdout. Throws naming `what`, with
 * the program's own stderr, when the run exits other than 0. */
function runProgram(
  what: string,
  command: readonly string[],
  cwd: string,
  env: Record<string, string>,
  input?: string
): string {
  const result = Bun.spawnSync([...command], {
    cwd,
    env: { ...process.env, ...env },
    stdin: input === undefined ? "inherit" : new TextEncoder().encode(input),
    stdout: "pipe",
    stderr: "pipe",
  });
  if (result.exitCode !== 0)
    throw new Error(`${what} exited ${result.exitCode}: ${result.stderr.toString().trim()}`);
  return result.stdout.toString();
}

/** The build of daemon-pane.go a process runs in place of `go run` (`runDaemonPane`): the binary
 * `SKILL_SCENARIOS_DAEMON_PANE` names, or undefined where it names none and every read is a `go
 * run`. rig.sh's daemon_pane_binary builds it in the daemon module of a label's checkout once a
 * batch (services_up), and the rig hands it to each process that reads that label's daemon
 * (label_facts, worker_boot). A `go run` compiles the daemon module first: 6.2-7.4 s a read with
 * a warm Go build cache and 282 s with a cold one (measured on 2026-11-15), where the build's run
 * takes milliseconds. One process reads one label's daemon, so one build serves every read it
 * makes: `phase` and `pane` run that module's own functions, and `refusal`, `count-default` and
 * `claim-refusal` are the program's own parse of the source files they are given, the same in a
 * build of any module. A build named and not there is an error, never a `go run` in its place. */
function daemonPaneBuild(): string | undefined {
  const build = process.env.SKILL_SCENARIOS_DAEMON_PANE;
  if (build === undefined || build === "") return undefined;
  if (!existsSync(build))
    throw new Error(
      `SKILL_SCENARIOS_DAEMON_PANE names ${build}, which is not there: rig.sh's services_up builds it (daemon_pane_binary)`
    );
  return build;
}

/** Runs daemon-pane.go in `module` with `args`, `input` on its stdin (the caller's own stdin when
 * there is none), and returns its stdout: the build `daemonPaneBuild` names, or else `go run
 * -overlay` of the program as `<module>/cmd/daemon-pane/main.go`. */
export function runDaemonPane(module: string, args: readonly string[], input?: string): string {
  const what = `daemon-pane ${args.join(" ")} in ${module}`;
  const build = daemonPaneBuild();
  if (build !== undefined) return runProgram(what, [build, ...args], module, {}, input);
  const scratch = mkdtempSync(path.join(os.tmpdir(), "daemon-pane-"));
  try {
    const overlay = path.join(scratch, "overlay.json");
    const main = path.join(module, "cmd", "daemon-pane", "main.go");
    writeFileSync(overlay, JSON.stringify({ Replace: { [main]: PROGRAM } }));
    return runProgram(
      what,
      ["go", "run", "-overlay", overlay, "./cmd/daemon-pane", ...args],
      module,
      {},
      input
    );
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
}

/** The printout of `program`, daemon-routes.go, daemon-completion.go or gh-selectors.go, over the
 * directory `dir` of a daemon's source. Each imports the standard library alone and runs from its
 * own directory with go.work off, where no go.mod above it makes it part of a module: building it
 * compiles a few standard packages and downloads nothing, so a Go cache that holds nothing yet
 * costs seconds rather than the daemon module's whole build. `dir` is resolved against the
 * caller's directory first, as `goSources` reads it, since the program runs in another. */
function runStandalone(program: string, dir: string): string {
  const resolved = path.resolve(dir);
  return runProgram(
    `${path.basename(program, ".go")} ${resolved}`,
    ["go", "run", program, resolved],
    import.meta.dir,
    { GOWORK: "off" }
  );
}

/** The daemon's refusal of a completion of a phase the completing role does not run: its code
 * and the daemon's own message for it. */
export interface Refusal {
  readonly code: string;
  readonly message: string;
}

/** The refusal daemon-pane.go's `refusal not-current-phase` printed as `printed`, or undefined
 * when it printed anything but what that command prints: the code, then the message, each a
 * non-empty line of its own. Fewer lines, an empty one, or more than two are a refusal half
 * read, which is a sentence the daemon never sends, so the caller reads such a printout as none
 * rather than answering part of it. */
export function refusalIn(printed: string): Refusal | undefined {
  const [code = "", message = "", ...rest] = printed.split("\n");
  if (code === "" || message === "" || rest.length !== 1 || rest[0] !== "") return undefined;
  return { code, message };
}

/** The daemon module of `checkout`. */
function daemonOf(checkout: string): string {
  return path.join(checkout, "packages", "daemon");
}

/** The Go sources of the package at `dir` but its tests, in byte order, each a full path; none for
 * a directory that is not there, whose reading then refuses on its own terms. */
function goSources(dir: string): string[] {
  if (!existsSync(dir)) return [];
  return readdirSync(dir)
    .filter((name) => name.endsWith(".go") && !name.endsWith("_test.go"))
    .sort()
    .map((name) => path.join(dir, name));
}

/** Where daemon-pane.go's answers are kept between processes (`cachedRead`): under the rig's work
 * directory (`SKILL_SCENARIOS_WORK`, which rig.sh exports), else under `TMPDIR`, in a directory of
 * a name that holds no word of the rig's vocabulary (`legion-daemon-cache`: `facts` is an askgate
 * case); never under HOME or a `/tmp` nobody named.
 * With neither set nothing is kept, and every read runs the program. */
function factsCache(): string | undefined {
  const work = process.env.SKILL_SCENARIOS_WORK;
  if (work !== undefined && work !== "") return path.join(work, "daemon-facts");
  const tmp = process.env.TMPDIR;
  if (tmp !== undefined && tmp !== "") return path.join(tmp, "legion-daemon-cache");
  return undefined;
}

/** The answer `run` prints, kept in the facts cache under a key of exactly what decides it: the
 * source of `program`, the Go program `run` runs, `keyArgs` (the command with no path in it),
 * and every source file the read parses, each by its path under `root` and its bytes. A boot
 * whose key is kept reads the answer from the file, with no `go run`; any other runs the program
 * and writes the answer whole (a temporary file renamed into place), so a reader never sees half
 * of one. A read that fails writes nothing, and throws as the program refused. The key names no
 * absolute path, so a second checkout of the same sources is the same answer. */
function cachedRead(
  program: string,
  run: () => string,
  keyArgs: readonly string[],
  root: string,
  sources: readonly string[]
): string {
  const dir = factsCache();
  if (dir === undefined) return run();
  const hash = createHash("sha256").update(readFileSync(program)).update(JSON.stringify(keyArgs));
  for (const source of sources)
    hash.update(`\0${path.relative(root, source)}\0`).update(readFileSync(source));
  const file = path.join(dir, `${hash.digest("hex")}.out`);
  if (existsSync(file)) return readFileSync(file, "utf8");
  const answer = run();
  mkdirSync(dir, { recursive: true });
  const written = `${file}.${process.pid}.${randomUUID()}.tmp`;
  writeFileSync(written, answer);
  renameSync(written, file);
  return answer;
}

/** The daemon's own refusal of `role`'s completion while the issue `issue` stands in `phase`, as
 * the daemon of `checkout` writes it: read out of that checkout's engine and intake sources by
 * daemon-pane.go (`refusalIn`), or undefined when that printout is no refusal. The daemon
 * stand-in answers such a completion with it and its tests read it the same way, so no copy of
 * the sentence and no second reading of it lives here. */
export function notCurrentPhase(
  checkout: string,
  role: string,
  phase: string,
  issue: string
): Refusal | undefined {
  const daemon = daemonOf(checkout);
  const engine = path.join(daemon, "internal", "workflow", "engine.go");
  const intake = path.join(daemon, "internal", "intake");
  const command = ["refusal", "not-current-phase", role, phase, issue];
  return refusalIn(
    cachedRead(
      PROGRAM,
      () => runDaemonPane(DAEMON_MODULE, [...command, engine, intake]),
      command,
      daemon,
      [engine, ...goSources(intake)]
    )
  );
}

/** The phase `role`'s worker is started in by the daemon of `checkout`, the first of its
 * transition table the role works, and the word of the handoff record that phase ends with, ""
 * when it ends with none: daemon-pane.go's `phase`, which runs the daemon's own functions and so
 * runs inside that checkout's module. Its answer is the phase and workflow tables and the claim
 * roles they name, so those three packages are what keeps it (`cachedRead`). */
export function labelPhase(checkout: string, role: string): { phase: string; word: string } {
  const daemon = daemonOf(checkout);
  const internal = path.join(daemon, "internal");
  const [phase = "", word = ""] = cachedRead(
    PROGRAM,
    () => runDaemonPane(daemon, ["phase", role]),
    ["phase", role],
    daemon,
    [
      ...goSources(path.join(internal, "phase")),
      ...goSources(path.join(internal, "workflow")),
      ...goSources(path.join(internal, "claim")),
    ]
  ).split("\n");
  return { phase, word };
}

/** The checkout the run under `runDir` was built from: the `checkout` line of its built-from,
 * which rig.sh's cmd_profile writes for the label and cmd_run copies into the run before any
 * agent boots. Its daemon is the one the label's runs stand in for (`labelRoutes`,
 * `labelDaemon`). Throws for a run with no such line: what that run's daemon serves is unknown. */
export function labelCheckout(runDir: string): string {
  const file = path.join(runDir, "built-from");
  const line = existsSync(file)
    ? readFileSync(file, "utf8")
        .split("\n")
        .find((one) => one.startsWith("checkout "))
    : undefined;
  const checkout = line?.slice("checkout ".length).trim() ?? "";
  if (checkout === "")
    throw new Error(
      `${file} names no checkout (rig.sh's cmd_profile writes a \`checkout <dir>\` line), so the daemon that run stands in for is unknown`
    );
  return checkout;
}

/** The daemon sources a reader reads of a run's label (`labelRoutes`, `labelDaemon`), under the
 * checkout's root. */
export const DAEMON_SOURCES = [
  "packages/daemon/internal/api",
  "packages/daemon/internal/workflow/engine.go",
];
/** The directory each built-from commit's daemon sources were rebuilt into in this process. */
const rebuilt = new Map<string, string>();

/** The daemon sources of the label the run under `runDir` ran, rebuilt from what the run recorded
 * and never read off the checkout its built-from names, which the pod may since have moved or
 * lost: the `commit <sha>` line of its built-from (rig.sh's checkout_commit), read with `git
 * archive` out of the first of `repos` (git directories the caller names, `--repo`) that holds
 * that commit, into a directory of this process's own, removed when it exits. The commit id is
 * the digest git reads every file of it by, so the sources are the ones the run ran. Of
 * DAEMON_SOURCES only those the commit holds are rebuilt: a commit holding none is a label with
 * no daemon there, which `labelRoutes` and `labelDaemon` then say, as they say it of any checkout.
 * Returns that directory, a checkout holding those sources alone, and the commit. Throws a Refused
 * naming the run and the input for a built-from with no commit, a commit of a checkout with
 * changes no commit holds (`dirty`), no repository named, and none that holds the commit. */
export function recordedCheckout(
  runDir: string,
  repos: readonly string[]
): { dir: string; commit: string } {
  const file = path.join(runDir, "built-from");
  const line = existsSync(file)
    ? readFileSync(file, "utf8")
        .split("\n")
        .find((one) => one.startsWith("commit "))
    : undefined;
  const [, commit = "", ...rest] = (line ?? "").trim().split(" ");
  const run = path.basename(runDir);
  if (!/^[0-9a-f]{40}$/.test(commit))
    throw new Refused(
      `${run}: its built-from records no commit (\`commit <sha>\`), so the daemon sources its label ran cannot be rebuilt`
    );
  if (rest.includes("dirty"))
    throw new Refused(
      `${run}: its built-from's commit ${commit} is of a checkout with changes no commit holds (\`dirty\`), so the daemon sources its label ran cannot be rebuilt`
    );
  const known = rebuilt.get(commit);
  if (known !== undefined) return { dir: known, commit };
  if (repos.length === 0)
    throw new Refused(
      `${run}: its label's daemon sources are rebuilt from commit ${commit}, which its built-from records, out of a repository the caller names (--repo <git dir>), and none was named`
    );
  const repo = repos.find(
    (one) =>
      Bun.spawnSync(["git", `--git-dir=${one}`, "cat-file", "-e", `${commit}^{commit}`])
        .exitCode === 0
  );
  if (repo === undefined)
    throw new Refused(
      `${run}: no repository named (${repos.join(", ")}) holds commit ${commit}, which its built-from records`
    );
  const git = (...args: string[]) => {
    const done = Bun.spawnSync(["git", `--git-dir=${repo}`, ...args]);
    if (done.exitCode !== 0)
      throw new Error(`git ${args.join(" ")} of ${repo} failed: ${done.stderr.toString().trim()}`);
    return done.stdout;
  };
  const held = git("ls-tree", "-r", "-z", "--name-only", commit, "--", ...DAEMON_SOURCES)
    .toString()
    .split("\0")
    .filter((name) => name !== "");
  const dir = mkdtempSync(path.join(os.tmpdir(), "label-sources-"));
  process.on("exit", () => rmSync(dir, { recursive: true, force: true }));
  if (held.length > 0) {
    const unpacked = Bun.spawnSync(["tar", "-x", "-C", dir], {
      stdin: git("archive", "--format=tar", commit, "--", ...held),
    });
    if (unpacked.exitCode !== 0)
      throw new Error(`tar could not unpack commit ${commit}'s daemon sources: ${unpacked.stderr}`);
  }
  rebuilt.set(commit, dir);
  return { dir, commit };
}

/** Every route the daemon of `checkout` registers, `<METHOD> <path>` each, read out of that
 * checkout's API source by daemon-routes.go (`runStandalone`), which parses the checkout's files
 * and compiles nothing of them or of any daemon module, so a substitute checkout holding the
 * source alone is read the same way. The stand-in answers by this set and health holds every 404
 * to it, so both read one set by one reading. Throws what the reading refused. */
export function labelRoutes(checkout: string): readonly string[] {
  const daemon = daemonOf(checkout);
  const api = path.join(daemon, "internal", "api");
  return cachedRead(
    ROUTES_PROGRAM,
    () => runStandalone(ROUTES_PROGRAM, api),
    ["routes"],
    daemon,
    goSources(api)
  )
    .split("\n")
    .filter((line) => line !== "");
}

/** The selector table of the Legion gh wrapper of `checkout` (gh-selectors.ts's `GhTable`), read
 * out of that checkout's cmd/legion and internal/ghconfig by gh-selectors.go (`runStandalone`),
 * which parses their files and compiles none of them, and kept in the facts cache keyed by those
 * packages' bytes, as the routes are. So each label's is read from its own checkout, and a
 * selector form its wrapper learns is read with no edit, or refused, naming it, where this rig
 * does not model it (gh-selectors.ts's `tableOf`). Each pattern's file is named under the
 * checkout's daemon module, which the printout, kept for any checkout of the same sources, does
 * not name. Throws what the reading refused. */
export function labelGhTable(checkout: string): GhTable {
  const daemon = daemonOf(checkout);
  return tableOf(
    cachedRead(
      GH_SELECTORS_PROGRAM,
      () => runStandalone(GH_SELECTORS_PROGRAM, daemon),
      ["gh-selectors"],
      daemon,
      [
        ...goSources(path.join(daemon, "cmd", "legion")),
        ...goSources(path.join(daemon, "internal", "ghconfig")),
      ]
    ),
    daemon
  );
}

/** The gh selectors of the daemon of `checkout` (gh-selectors.ts's `GhSelectors`): its gh
 * wrapper's table (`labelGhTable`) joined with the gh help that checkout saves for the wrapper's
 * own tests (cmd/legion/testdata/gh-help), which is what the wrapper's table is held to there.
 * rig.sh's label_facts reads them at services_up and its worker_boot writes them into each run,
 * which the gh stand-in reads them from; health reads the run's label's here. Throws what the
 * reading refused. */
export function labelGhSelectors(checkout: string): GhSelectors {
  const table = labelGhTable(checkout);
  const helps = path.join(daemonOf(checkout), "cmd", "legion", "testdata", "gh-help");
  if (!existsSync(path.join(helps, "reference.txt")))
    throw new Error(
      `${helps}/reference.txt is not there, so which commands the gh wrapper of ${checkout} reads, and how, is unknown`
    );
  const saved = Object.fromEntries(
    readdirSync(helps)
      .filter((name) => name.endsWith(".txt") && name !== "reference.txt")
      .map((name) => [name, readFileSync(path.join(helps, name), "utf8")])
  );
  return selectorsOf(table, saved, readFileSync(path.join(helps, "reference.txt"), "utf8"));
}

/** Each refusal `labelDaemon` met in this process, by the daemon module and the bytes of the two
 * sources it read. The facts cache keeps no failed read (`cachedRead`), so without this a source
 * that cannot be read was run again for each answer that asked for it: health asks once a rule a
 * run, and the tester's fixture of 21 runs, whose built-from names checkouts that are not there,
 * paid 15 `go run`s of daemon-completion.go and timed out at 60 s. Keyed by the bytes too, so a
 * source rewritten in place is read again. */
const daemonRefusals = new Map<string, Error>();

/** How the daemon of `checkout` takes a completion (completion-answers.ts's `LabelDaemon`): its
 * engine's late role and its route's record, read out of that checkout's
 * internal/workflow/engine.go and internal/api/handoff.go by daemon-completion.go
 * (`runStandalone`), which parses the two files and compiles nothing of them, so a substitute
 * checkout holding the two alone is read the same way. A label's contract is so read once per
 * label source: the stand-in reads it at its boot (`standinFacts`) out of the facts cache, which
 * rig.sh's label_facts fills for each label before any run starts, and health and the scorer read
 * the run's own label's through the same cache (rig.sh gives both the work directory), keyed by the
 * two files' bytes (`cachedRead`), so a label whose checkout moves is read anew and never by its
 * name. A source that is not there is refused here, naming it, with no program run: what a
 * checkout lacks needs no compile to say, and a run whose built-from names a checkout that is
 * gone costs health nothing a slow host could stretch. A refusal is kept for the process
 * (`daemonRefusals`). Throws what the reading refused, naming the file and the shapes it looked
 * for, and for a printout that is no late role and record route. */
export function labelDaemon(checkout: string): LabelDaemon {
  const daemon = daemonOf(checkout);
  const sources = [
    path.join(daemon, "internal", "workflow", "engine.go"),
    path.join(daemon, "internal", "api", "handoff.go"),
  ];
  const absent = sources.find((source) => !existsSync(source));
  if (absent !== undefined)
    throw new Error(
      `${absent} is not there, so how the daemon of ${checkout} takes a completion is unknown (daemon-completion.go reads it)`
    );
  const key = [daemon, ...sources.map((source) => readFileSync(source, "utf8"))].join("\0");
  const refused = daemonRefusals.get(key);
  if (refused !== undefined) throw refused;
  try {
    const printed = cachedRead(
      COMPLETION_PROGRAM,
      () => runStandalone(COMPLETION_PROGRAM, daemon),
      ["completion"],
      daemon,
      sources
    );
    const [late, route, ...rest] = printed.split("\n");
    const lateRole = LATE_ROLES.find((shape) => shape === late);
    const recordRoute = RECORD_ROUTES.find((shape) => shape === route);
    if (lateRole === undefined || recordRoute === undefined || rest.join("") !== "")
      throw new Error(
        `daemon-completion ${daemon} printed ${JSON.stringify(printed)}, which is no late role (${LATE_ROLES.join(" or ")}) and record route (${RECORD_ROUTES.join(" or ")})`
      );
    return { lateRole, recordRoute };
  } catch (error) {
    const kept = error instanceof Error ? error : new Error(String(error));
    daemonRefusals.set(key, kept);
    throw kept;
  }
}

/** `judge`'s verdict on the run under `runDir`, `judge` given how that run's label's daemon
 * takes a completion (`labelDaemon` of the sources its built-from's commit holds, rebuilt out of
 * `repos` by `recordedCheckout`) to read where an answer turns on it (completion-answers.ts's
 * `refusedCompletion` and `takenCompletions`), or `unread`'s, with why, where `judge` asked for
 * it and those sources hold no completion the reading knows, the rebuilt directory named by its
 * commit so no path of this process's is in a verdict. health.ts's completion rules and score.ts's
 * `unscored` read a run's daemon so, never by the label's name nor off the pod's checkout; a
 * Refused (`recordedCheckout`) and any other error `judge` throws is thrown on. */
export function judgedOnDaemon<T>(
  runDir: string,
  judge: (daemon: () => LabelDaemon) => T,
  unread: (why: string) => T,
  repos: readonly string[]
): T {
  let why: string | undefined;
  try {
    return judge(() => {
      const { dir, commit } = recordedCheckout(runDir, repos);
      try {
        return labelDaemon(dir);
      } catch (error) {
        why = (error instanceof Error ? error.message : String(error)).replaceAll(
          dir,
          `<commit ${commit}>`
        );
        throw error;
      }
    });
  } catch (error) {
    if (why === undefined) throw error;
    return unread(why);
  }
}

/** Whether `routes` (`labelRoutes`) serves a request of `method` to `asked`; `method` undefined
 * is a request whose method is not recorded, which any route of that path serves. A segment the
 * daemon names `{name}` matches any one non-empty segment, and `{name...}` the rest of the path,
 * as Go's ServeMux matches them. */
export function serves(
  routes: readonly string[],
  method: string | undefined,
  asked: string
): boolean {
  const parts = asked.split("/");
  return routes.some((pattern) => {
    const space = pattern.indexOf(" ");
    if (method !== undefined && pattern.slice(0, space) !== method) return false;
    const segments = pattern.slice(space + 1).split("/");
    for (const [at, segment] of segments.entries()) {
      if (/^\{[^}]*\.\.\.\}$/.test(segment)) return parts.length > at;
      const part = parts[at];
      if (part === undefined) return false;
      if (/^\{[^}]*\}$/.test(segment) ? part === "" : part !== segment) return false;
    }
    return parts.length === segments.length;
  });
}

/** The default the daemon of `checkout` gives the count setting `key` when legion.yaml leaves it
 * out, which is the value the rig's daemon runs with, having no legion.yaml: daemon-pane.go's
 * `count-default`, over that checkout's config package, every source of which the read parses and
 * so keys the answer on (`cachedRead`). Throws what the reading refused. */
export function countDefault(checkout: string, key: string): number {
  const daemon = daemonOf(checkout);
  const config = path.join(daemon, "internal", "config");
  const command = ["count-default", key];
  const printed = cachedRead(
    PROGRAM,
    () => runDaemonPane(DAEMON_MODULE, [...command, config]),
    command,
    daemon,
    goSources(config)
  ).trim();
  const value = Number(printed);
  if (!Number.isSafeInteger(value))
    throw new Error(`daemon-pane count-default ${key} printed ${JSON.stringify(printed)}`);
  return value;
}

/** The claim refusal `name` (claim.InvalidSecret, claim.StaleGeneration, …) of the daemon of
 * `checkout`, its status and its message: daemon-pane.go's `claim-refusal`, over that checkout's
 * claim source, which the stand-in answers a caller it refuses with. Throws what the reading
 * refused. */
export function claimRefusal(checkout: string, name: string): ClaimRefusal {
  const daemon = daemonOf(checkout);
  const claim = path.join(daemon, "internal", "claim");
  const command = ["claim-refusal", name];
  const [status = "", message = ""] = cachedRead(
    PROGRAM,
    () => runDaemonPane(DAEMON_MODULE, [...command, claim]),
    command,
    daemon,
    goSources(claim)
  ).split("\n");
  if (!/^\d+$/.test(status) || message === "")
    throw new Error(`daemon-pane claim-refusal ${name} printed no status and message`);
  return { status: Number(status), message };
}

/** Dispatch's refusal of a message the daemon posts (`postRefusal`): whether it is the refusal of
 * a decision block, rather than of a bound, and Dispatch's own sentence for it. */
export interface PostRefusal {
  readonly decision: boolean;
  readonly sentence: string;
}

/** Dispatch's refusal of a message the daemon of `checkout` posts with `body`, as that daemon
 * decides it before it records a move whose reason it posts, or undefined where Dispatch takes it:
 * daemon-pane.go's `post-check`, which runs that checkout's own reading (the longest post of the
 * body its outbox could make, read by Dispatch's reader) and so runs inside its module, as `phase`
 * does. The daemon stand-in asks it of a backward move's reason at the move, so a run reads it
 * through its label's build (`daemonPaneBuild`) in milliseconds; nothing is kept, since the
 * answer turns on the body and on the whole of Dispatch's reader. Throws what the program
 * refused, and for a printout that is neither nothing nor a kind and a sentence. */
export function postRefusal(checkout: string, body: string): PostRefusal | undefined {
  const printed = runDaemonPane(daemonOf(checkout), ["post-check"], body);
  if (printed === "") return undefined;
  const [kind = "", sentence = "", ...rest] = printed.split("\n");
  if (!["decision", "bound"].includes(kind) || sentence === "" || rest.join("") !== "")
    throw new Error(
      `daemon-pane post-check printed ${JSON.stringify(printed)}, which is no refusal's kind and sentence`
    );
  return { decision: kind === "decision", sentence };
}

/** Stands in for the phase the issue stands in, in the refusal `standinFacts` reads: the
 * stand-in replaces it with the phase it serves when it sends the refusal. */
export const PHASE_SLOT = "PHASESLOTOFTHEISSUE";
/** Stands in for the issue's key in that refusal: the stand-in replaces it with its run's issue.
 * Each run files an issue of its own, so a refusal read with the run's key is a fact no read
 * before the run (rig.sh's label_facts) could have read for it. */
export const ISSUE_SLOT = "ISSUESLOTOFTHERUN";

/** The phases that end with a handoff record (`phase.Handoff` in
 * packages/daemon/internal/phase/phase.go): the stand-in refuses a completion naming a record in
 * any other phase. daemon-pane.go answers only the role's own phase, at a read each, so the rest
 * is written here, and `standinFacts` refuses a daemon whose answer for the role's own phase
 * disagrees with it. */
export const RECORD_PHASES: readonly string[] = [
  "planning",
  "implementing",
  "testing",
  "reviewing",
];

/** A claim refusal of the daemon's (`claimRefusal`): its status and its sentence. */
export interface ClaimRefusal {
  readonly status: number;
  readonly message: string;
}

/** Every fact of a label's daemon the daemon stand-in answers by, read at its boot. */
export interface StandinFacts {
  /** The routes the daemon registers (`labelRoutes`): the one answer to "does this daemon serve
   * that route", which the stand-in asks of every request and health asks of every 404. */
  readonly routes: readonly string[];
  /** How the daemon takes a completion: its engine's answer to a late role and the phase its
   * route holds a completion's record to (`labelDaemon`), which the stand-in answers every
   * completion by. */
  readonly daemon: LabelDaemon;
  /** The run of refused turns at which the daemon fails the claim: the launch failure limit
   * (supervise's `turnOutcome`, `run < m.deps.Limits.LaunchFailures`), which the daemon is given
   * as `launch_failure_limit` and the rig, having no legion.yaml, runs at its default
   * (`countDefault`). Read only for a daemon that serves `claims/turn-outcome`, and 0 for one
   * that serves none. */
  readonly turnOutcomeLimit: number;
  /** The claim refusals the claim routes answer a caller with (packages/daemon/internal/claim's
   * `InvalidSecret`, `InvalidBootToken` and `StaleGeneration`, `claimRefusal`). */
  readonly invalidSecret: ClaimRefusal;
  readonly invalidBootToken: ClaimRefusal;
  readonly staleGeneration: ClaimRefusal;
  /** The phase the role's own worker is started in, the first of the transition table it works,
   * and the word of the handoff record that phase ends with, empty when it ends with none
   * (`labelPhase`). */
  readonly rolePhase: string;
  readonly handoffWord: string;
  /** The phase the issue stands in for the run: the one the stand-in is started in, or the
   * role's own when it is started in none. One that is not the role's is a run of a loop the
   * daemon starts no phase for, where the role's completion is a late one. */
  readonly phase: string;
  /** The daemon's own refusal of a late completion of the role, its code and its message
   * (`notCurrentPhase`), with `PHASE_SLOT` for the issue's phase and `ISSUE_SLOT` for its key:
   * the skill has the worker quote that sentence to its architect. Read only for a run whose
   * role can be late, one started in a phase that is not the role's or in `reviewing`, the one
   * phase the review round moves; undefined for any other. */
  readonly notCurrentPhase: Refusal | undefined;
  /** What the daemon's source says it posts, refuses and publishes (`labelShapes`). */
  readonly shapes: LabelShapes;
}

/** A text a label's daemon source holds, and the file, under the checkout's `packages/`, it is
 * read in. */
interface Marker {
  readonly file: string;
  readonly text: string;
}

/** One shape of a label's daemon source a reader pins: its name, the markers a daemon of that
 * shape holds, each in its file, and what the stand-in answers such a daemon by. */
interface SourceShape<T> {
  readonly name: string;
  readonly markers: readonly Marker[];
  readonly value: T;
}

/** The value of the one shape of `shapes` whose every marker the daemon source of `checkout`
 * holds, for `reader`. A source that holds every marker of no shape, or of two, is refused, naming
 * each marker of each shape it does not hold and the file it was looked for in: a marker this
 * cannot find is a daemon of no shape the stand-in has the answers of, or a marker gone stale, and
 * neither is read as an older daemon that lacks the behaviour (the architect's ruling of
 * 2026-11-18). daemon-pane.go's `addressingOf` reads a checkout the same way: the shapes pinned,
 * any other refused. */
function sourceShape<T>(checkout: string, reader: string, shapes: readonly SourceShape<T>[]): T {
  const texts = new Map<string, string>();
  const holds = ({ file, text }: Marker) => {
    const at = path.join(checkout, "packages", file);
    if (!texts.has(at)) texts.set(at, existsSync(at) ? readFileSync(at, "utf8") : "");
    return texts.get(at)?.includes(text) === true;
  };
  const held = shapes.filter(({ markers }) => markers.every(holds));
  const [one] = held;
  if (held.length === 1 && one !== undefined) return one.value;
  const why =
    held.length === 0
      ? shapes
          .map(
            ({ name, markers }) =>
              `${name} holds ${markers
                .filter((marker) => !holds(marker))
                .map(({ file, text }) => `${JSON.stringify(text)} in packages/${file}`)
                .join(" and ")}, and this source does not`
          )
          .join("; ")
      : `it holds the markers of two shapes at once, ${held.map(({ name }) => name).join(", and ")}`;
  throw new Error(
    `the daemon source of ${checkout} is no shape of ${reader} this rig reads: ${why}`
  );
}

/** What a label's daemon posts on the issue, refuses and publishes, as its source reads
 * (`labelShapes`). */
export interface LabelShapes {
  /** A worker's backward move posts its sentence on the issue (effects.go's `backwardMoveReason`,
   * main's since #4309, 26f27a997a). */
  readonly postsBackward: boolean;
  /** The move of a head that conflicts with its base posts its sentence on the issue (effects.go's
   * `moveReason`, main's since #4291, 34f0d8c801). */
  readonly postsConflict: boolean;
  /** A backward move whose sentence Dispatch would refuse is refused (effects.go's
   * `backwardReasonRefusal`, main's since #4309). */
  readonly refusesReason: boolean;
  /** The daemon publishes the issue's role holders to a Dispatch that serves their route (its
   * client's `PutRoleHolders` and the route's row in Dispatch's table, main's since #4228,
   * 5a2d1f0637). */
  readonly publishesHolders: boolean;
  /** Every task of the reviewer ends with the daemon's two sentences of its review round: the
   * commit the reviewer's newest deciding review read, and the entry of its round
   * (review_round.go's `namingTheReviewRound`, through reviewround.go's `Sentences`, main's since
   * #4328, 480e2dc4ae), which `reviewRoundSentences` writes as that source does. */
  readonly namesReviewRound: boolean;
  /** A review the review App submitted is the reviewer's own, and so decides the round, only
   * where its Legion footer names the session the claims record holds for the claim the daemon
   * starts the reviewer under, which the reviewer's agent records at its registration; while that
   * claim records none, such a review waits (review.go's `byReviewer`, `reviewersClaim` and
   * `RefusedWhileNoSession`, main's since #4328). False for main `ff2a10ef`'s engine, which reads
   * the session off the claim the reviewer's row names, which only the reviewer's completion
   * writes there, and lets the login decide alone while the row names none. */
  readonly decidesBySession: boolean;
  /** A review the review App submitted decides the round only where the head its Legion footer
   * names, 40 lowercase hexadecimal characters, is the pull request's head, compared exactly, and
   * its decision is recorded against that head: a footer that names no head, or another, decides
   * nothing (review.go's `refusesRound` and `reviewHead`, intake's `LegionHead` and
   * `fullCommitSHA`, main's since #2539, a4c0ff994a). Its footer is decoded as a JSON object whose
   * `session` and `head` are strings, beside an `entry` kept raw (intake's `legionFooter`). False
   * for main `ff2a10ef`'s engine, which records a review's decision against its `commit_id` and
   * decodes the footer's `session` alone. */
  readonly decidesByFooterHead: boolean;
}

const EFFECTS = "daemon/internal/workflow/effects.go";
/** The completion refusals every daemon the rig runs gives, each by its code in the file that
 * writes it: this checkout's and main `ff2a10ef`'s (2026-11-07) alike, so a source that names one
 * nowhere is a marker gone stale, which no older shape answers. */
const COMPLETION_REFUSALS = [
  ["HANDOFF_SESSION_MISMATCH", "daemon/internal/api/handoff.go"],
  ["HANDOFF_ALREADY_RECORDED", "daemon/internal/api/handoff.go"],
  ["HANDOFF_NOT_NEW", "daemon/internal/workflow/engine.go"],
] as const;
/** The completion route's refusals of a completion's verdict, in the words every daemon the rig
 * runs gives them (handoff.go's `answerCompletion`, this checkout's and main `ff2a10ef`'s alike):
 * a tester's that is neither `pass` nor `fail`, and one any other role reports. The daemon
 * stand-in answers by them; `labelShapes` holds a label's source to them (`VERDICT_RULE`). */
export const VERDICT_REFUSALS = {
  tester: { code: "TESTER_VERDICT_REQUIRED", message: "tester verdict must be pass or fail" },
  other: { code: "VERDICT_ROLE_FORBIDDEN", message: "only a tester may report a verdict" },
} as const;
/** The route's verdict rule as every daemon the rig runs has it: each condition, then its 400 in
 * the words `VERDICT_REFUSALS` restates. */
const VERDICT_RULE: readonly Marker[] = [
  {
    file: "daemon/internal/api/handoff.go",
    text: `if grant.Role == claim.RoleTester && req.Verdict != "pass" && req.Verdict != "fail" {\n\t\twriteFailure(w, http.StatusBadRequest, "${VERDICT_REFUSALS.tester.code}", "${VERDICT_REFUSALS.tester.message}")`,
  },
  {
    file: "daemon/internal/api/handoff.go",
    text: `if grant.Role != claim.RoleTester && req.Verdict != "" {\n\t\twriteFailure(w, http.StatusBadRequest, "${VERDICT_REFUSALS.other.code}", "${VERDICT_REFUSALS.other.message}")`,
  },
];
/** The workflow's status write as main `ff2a10ef`'s daemon has it, with no message to post: what
 * that older daemon has in the place of each sentence a move posts on the issue, and so of the
 * refusal of a backward move's reason that sentence would carry. */
const STATUS_WITHOUT_MESSAGE: Marker = {
  file: EFFECTS,
  text: "func (e *Engine) status(ctx context.Context, tx pgx.Tx, issue record.Issue, status string) error {",
};
/** The outbox's kinds as main `ff2a10ef`'s daemon has them, ending at the gated steps'
 * publication with no kind of the role holders': what that older daemon has in the place of
 * their publication. */
const OUTBOX_WITHOUT_HOLDERS: Marker = {
  file: "daemon/internal/record/outbox.go",
  text: '\tOutboxKindGatedStepsPublish OutboxKind = "gated_steps_publish"\n)',
};
/** The file that words the two sentences ending a reviewer's task (main's since #4328). */
const ROUND_WORDS = "daemon/internal/reviewround/reviewround.go";
/** Those sentences' words as reviewround.go declares them: the label of the commit, the label of
 * the entry, and the word either names where it has no value. */
const ROUND_COMMIT = "Last reviewed commit";
const ROUND_ENTRY = "Review entry";
const ROUND_NONE = "none";
/** What a daemon that ends every task of the reviewer with those sentences holds: the function
 * that appends them to each task of the reviewer, the three words above as their constants
 * declare them, and the one return of `Sentences` that joins them, so a source that words or
 * joins the sentences otherwise is of no shape this reads. */
const NAMES_ROUND: readonly Marker[] = [
  { file: "daemon/internal/workflow/review_round.go", text: "func namingTheReviewRound(" },
  { file: ROUND_WORDS, text: `const CommitLabel = "${ROUND_COMMIT}"` },
  { file: ROUND_WORDS, text: `const EntryLabel = "${ROUND_ENTRY}"` },
  { file: ROUND_WORDS, text: `const none = "${ROUND_NONE}"` },
  {
    file: ROUND_WORDS,
    text: 'return " " + CommitLabel + ": " + commit + ". " + EntryLabel + ": " + named + "."',
  },
];
/** The queue's row as main `ff2a10ef`'s daemon builds it, from the payload as it came: what that
 * older daemon has in the place of the sentences, which main's since #4328 writes into each task
 * of the reviewer as it queues it (effects.go's `queuedRow`). */
const ROW_AS_QUEUED: Marker = {
  file: EFFECTS,
  text: "payload record.OutboxPayload, at time.Time) error {\n\trow, err := record.NewOutboxRow(issue, payload, at)",
};
/** The file the engine judges who wrote a review in. */
const REVIEW = "daemon/internal/workflow/review.go";
/** Who a review of the review App is the reviewer's own for, as main's engine since #4328 judges
 * it: the session the reviewer's claim records, found by the claim's token, and none while it
 * records none. */
const BY_RECORDED_SESSION: readonly Marker[] = [
  {
    file: REVIEW,
    text: 'return e.byReviewApp(review.Author) && reviewerSession != "" && review.LegionSession() == reviewerSession',
  },
  { file: REVIEW, text: "func (e *Engine) reviewersClaim(" },
];
/** The same judgement as main `ff2a10ef`'s engine makes it: any session while the reviewer's row
 * names no claim, read off the claim that row names (`claimSession`). */
const BY_ROW_SESSION: readonly Marker[] = [
  {
    file: REVIEW,
    text: 'return e.byReviewApp(review.Author) && (reviewerSession == "" || review.LegionSession() == reviewerSession)',
  },
  { file: REVIEW, text: "func (e *Engine) claimSession(" },
];
/** The file the daemon's intake reads a review's Legion footer in. */
const FACTS = "daemon/internal/intake/facts.go";
/** Where every daemon the rig runs finds a review's Legion footer: the JSON object between the
 * last `<!-- legion:` of its body and the first `-->` after it. */
const FOOTER_FOUND: readonly Marker[] = [
  { file: FACTS, text: 'const legionFooterMarker = "<!-- legion:"' },
  { file: FACTS, text: "at := strings.LastIndex(r.Body, legionFooterMarker)" },
  { file: FACTS, text: 'end := strings.Index(rest, "-->")' },
];
/** The head a review of the review App decides on, as main's engine since #2539 reads it: the
 * footer decoded whole into `session` and `head` strings and a raw `entry`, a decoding error
 * costing the whole footer; its head a commit only at 40 lowercase hexadecimal characters; a
 * review App review that names no head, or another than the pull request's, refused; and the
 * decision of a review App review recorded against that head, a person's against its
 * `commit_id`. */
const BY_FOOTER_HEAD: readonly Marker[] = [
  ...FOOTER_FOUND,
  {
    file: FACTS,
    text: 'type legionFooter struct {\n\tSession string          `json:"session"`\n\tHead    string          `json:"head"`\n\tEntry   json.RawMessage `json:"entry"`\n}',
  },
  {
    file: FACTS,
    text: "var footer legionFooter\n\tif err := json.Unmarshal([]byte(rest[:end]), &footer); err != nil {\n\t\treturn legionFooter{}\n\t}",
  },
  {
    file: FACTS,
    text: 'head := r.lastLegionFooter().Head\n\tif !fullCommitSHA(head) {\n\t\treturn ""\n\t}',
  },
  { file: FACTS, text: "\tif len(s) != 40 {\n\t\treturn false\n\t}" },
  { file: FACTS, text: "if (c < '0' || c > '9') && (c < 'a' || c > 'f') {" },
  { file: REVIEW, text: 'switch head := review.LegionHead(); {\n\tcase head == "":' },
  { file: REVIEW, text: "\tcase head != pr.HeadSHA:" },
  {
    file: REVIEW,
    text: "reviewer.Decision = &record.ReviewDecision{State: state, Body: fact.Body, Head: e.reviewHead(fact)}",
  },
  {
    file: REVIEW,
    text: "if e.byReviewApp(review.Author) {\n\t\treturn review.LegionHead()\n\t}\n\treturn review.CommitID",
  },
];
/** The same as main `ff2a10ef`'s engine has it: the footer's `session` decoded alone, and every
 * review's decision recorded against its `commit_id`, which no footer's head refuses. */
const BY_COMMIT_ID: readonly Marker[] = [
  ...FOOTER_FOUND,
  {
    file: FACTS,
    text: 'var footer struct {\n\t\tSession string `json:"session"`\n\t}\n\tif err := json.Unmarshal([]byte(rest[:end]), &footer); err != nil {',
  },
  {
    file: REVIEW,
    text: "reviewer.Decision = &record.ReviewDecision{State: state, Body: fact.Body, Head: fact.CommitID}",
  },
];

/** What the daemon source of `checkout` posts, refuses, publishes, ends a reviewer's task with and
 * takes a review of the review App for, each read as one of two shapes: main's since the change
 * that brought it, by its own marker, or main `ff2a10ef`'s, which predates all six changes, by
 * the code it has in their place (`STATUS_WITHOUT_MESSAGE`, `OUTBOX_WITHOUT_HOLDERS`,
 * `ROW_AS_QUEUED`, `BY_ROW_SESSION`, `BY_COMMIT_ID`). The completion refusals and the route's
 * verdict rule have one shape, every daemon the rig runs holding them. Throws, naming each marker
 * and its file, for a source of no shape or of both (`sourceShape`), which refuses the batch at
 * rig.sh's label_facts and a stand-in's boot. */
export function labelShapes(checkout: string): LabelShapes {
  for (const [code, file] of COMPLETION_REFUSALS)
    sourceShape(checkout, `the completion refusal ${code}`, [
      { name: "every daemon the rig runs", markers: [{ file, text: `"${code}"` }], value: true },
    ]);
  sourceShape(checkout, "the completion route's verdict rule", [
    { name: "every daemon the rig runs", markers: VERDICT_RULE, value: true },
  ]);
  const older = {
    name: "main ff2a10ef's daemon, whose status write posts no message",
    markers: [STATUS_WITHOUT_MESSAGE],
    value: false,
  };
  const posted = (reader: string, since: string, text: string) =>
    sourceShape(checkout, reader, [
      { name: `main's daemon since ${since}`, markers: [{ file: EFFECTS, text }], value: true },
      older,
    ]);
  return {
    postsBackward: posted("a backward move's sentence", "#4309", "func backwardMoveReason("),
    postsConflict: posted("a conflict's move", "#4291", "func moveReason("),
    refusesReason: posted(
      "the refusal of a backward move's reason",
      "#4309",
      '"BACKWARD_REASON_TOO_LONG"'
    ),
    publishesHolders: sourceShape(checkout, "the role holders' publication", [
      {
        name: "main's daemon and Dispatch since #4228",
        markers: [
          { file: "daemon/internal/dispatch/client.go", text: '"/role-holders"' },
          {
            file: "envoy/internal/dispatch/api/routes_table.go",
            text: '"/api/v1/issues/{key}/role-holders"',
          },
        ],
        value: true,
      },
      {
        name: "main ff2a10ef's daemon, whose outbox kinds end at gated_steps_publish",
        markers: [OUTBOX_WITHOUT_HOLDERS],
        value: false,
      },
    ]),
    namesReviewRound: sourceShape(checkout, "the sentences that end a reviewer's task", [
      { name: "main's daemon since #4328", markers: NAMES_ROUND, value: true },
      {
        name: "main ff2a10ef's daemon, whose queue writes a task as it came",
        markers: [ROW_AS_QUEUED],
        value: false,
      },
    ]),
    decidesBySession: sourceShape(checkout, "whose review of the review App decides a round", [
      { name: "main's daemon since #4328", markers: BY_RECORDED_SESSION, value: true },
      {
        name: "main ff2a10ef's daemon, which reads the session off the reviewer's row",
        markers: BY_ROW_SESSION,
        value: false,
      },
    ]),
    decidesByFooterHead: sourceShape(checkout, "the head a review of the review App decides on", [
      { name: "main's daemon since #2539", markers: BY_FOOTER_HEAD, value: true },
      {
        name: "main ff2a10ef's daemon, which records a review's decision against its commit_id",
        markers: BY_COMMIT_ID,
        value: false,
      },
    ]),
  };
}

/** The two sentences the daemon of `checkout` ends a task of the reviewer in `reviewing` with, as
 * reviewround.go's `Sentences` writes them: `commit`, the head its row holds as the one the
 * reviewer's newest deciding review read ("" for none), and `entry`, the entry its row stands at
 * while it holds that entry's task (0 for none), each `none` where it has no value
 * (review_round.go's `namingTheReviewRound`). "" for a daemon of main `ff2a10ef`'s shape, which
 * ends no task with them (`labelShapes`' `namesReviewRound`). Throws for a source of no shape
 * this reads, naming each marker and its file. */
export function reviewRoundSentences(checkout: string, commit: string, entry: number): string {
  if (!labelShapes(checkout).namesReviewRound) return "";
  return ` ${ROUND_COMMIT}: ${commit === "" ? ROUND_NONE : commit}. ${ROUND_ENTRY}: ${entry > 0 ? entry : ROUND_NONE}.`;
}

/** `read()`, or an error naming `fact` of the daemon of `checkout` and why it could not be read. */
function readFact<T>(checkout: string, fact: string, read: () => T): T {
  try {
    return read();
  } catch (error) {
    throw new Error(
      `the daemon fact \`${fact}\` of ${checkout} could not be read: ${error instanceof Error ? error.message : String(error)}`
    );
  }
}

/** Every fact of the daemon of `checkout` a daemon stand-in of `role` started in `phase` (""
 * for the role's own) answers by (`StandinFacts`), each read through the facts cache. The
 * stand-in reads them at its boot and rig.sh's label_facts reads them for every stand-in a batch
 * boots before any of its runs starts, through this one function, so a boot reads each out of a
 * file. Throws naming the fact that could not be read, or the one the daemon answered in a way
 * this rig cannot answer by. */
export function standinFacts(checkout: string, role: string, phase: string): StandinFacts {
  const routes = readFact(checkout, "routes", () => labelRoutes(checkout));
  const daemon = readFact(checkout, "completion", () => labelDaemon(checkout));
  const turnOutcomeLimit = serves(routes, "POST", "/legion/v1/claims/turn-outcome")
    ? readFact(checkout, "count-default launch_failure_limit", () =>
        countDefault(checkout, "launch_failure_limit")
      )
    : 0;
  const refusal = (name: string) =>
    readFact(checkout, `claim-refusal ${name}`, () => claimRefusal(checkout, name));
  const invalidSecret = refusal("InvalidSecret");
  const invalidBootToken = refusal("InvalidBootToken");
  const staleGeneration = refusal("StaleGeneration");
  const { phase: rolePhase, word: handoffWord } = readFact(checkout, `phase ${role}`, () =>
    labelPhase(checkout, role)
  );
  if ((handoffWord !== "") !== RECORD_PHASES.includes(rolePhase))
    throw new Error(
      `the daemon fact \`phase ${role}\` of ${checkout} gives ${rolePhase} the handoff word '${handoffWord}', which RECORD_PHASES does not agree with`
    );
  const served = phase === "" ? rolePhase : phase;
  const late = served !== rolePhase || served === "reviewing";
  const command = `refusal not-current-phase ${role}`;
  const lateRefusal = late
    ? readFact(checkout, command, () => notCurrentPhase(checkout, role, PHASE_SLOT, ISSUE_SLOT))
    : undefined;
  if (late && lateRefusal?.code !== NOT_CURRENT_PHASE)
    throw new Error(
      `the daemon fact \`${command}\` of ${checkout} is no ${NOT_CURRENT_PHASE} code and message for the refusal of ${role}'s late completion`
    );
  const shapes = readFact(checkout, "shapes", () => labelShapes(checkout));
  return {
    routes,
    daemon,
    turnOutcomeLimit,
    invalidSecret,
    invalidBootToken,
    staleGeneration,
    rolePhase,
    handoffWord,
    phase: served,
    notCurrentPhase: lateRefusal,
    shapes,
  };
}

/** The claim a pane is launched for and the rig's stand-ins for what the daemon knows at boot
 * (daemon-pane.go's paneRequest). `repository` and `defaultBranch` are the run's own world's,
 * which the pane's addressing names and whose owner the claim's gh bundle is the directory of.
 * `ghToken` stands in for the claim's GitHub App token: it is
 * rendered into the claim's gh bundle under `<stateDir>/secrets/<claim>-gh`, the pane's
 * `LEGION_GH_OWNERS_DIR`, whose one owner's directory is its `GH_CONFIG_DIR`, and its
 * `LEGION_GH_PATH` is `ghPath` when the rig names one (its recording stand-in), else the real gh as
 * the daemon resolves it at boot, the first `gh` on `path`, refused when that is a script; omitted,
 * the pane gets no gh variable, as from a daemon with no GitHub Apps. */
interface PaneRequest {
  readonly project: string;
  readonly issue: string;
  readonly role: string;
  readonly repository: string;
  readonly defaultBranch: string;
  readonly stateDir: string;
  readonly workspace: string;
  readonly daemonUrl: string;
  readonly envoyUrl: string;
  readonly natsUrls: readonly string[];
  readonly bootTokenFile: string;
  readonly ghToken?: string;
  readonly path: string;
  readonly ghPath?: string;
  readonly systemPrompt?: boolean;
}

/** What the daemon tells the pane: every variable, PATH included, and, for a request that asks for
 * it, the one `--append-system-prompt` argument as shell text. */
export interface DaemonPane {
  readonly env: Record<string, string>;
  readonly systemPromptArgument?: string;
}

/** The stand-in for the claim's GitHub App token: `daemon-pane.go` renders it into the claim's gh
 * files, where production's daemon renders the App's token. Production's `gh auth token` and `gh
 * auth status -t` print that token; the world's gh refuses both, since no stand-in reads a token
 * (gh-standin.ts's `authStatus`), and health's rule 21 names a run that makes either. It has the
 * shape of an installation token, `ghs_` and 36 letters and digits, drawn afresh by each process,
 * so no word of it is the rig's (it was `ghs_rig_token`) and no literal of one is in the source a
 * push's leak check reads. A placeholder, never a secret. */
const TOKEN_ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
export const RIG_GH_TOKEN = `ghs_${Array.from(
  { length: 36 },
  () => TOKEN_ALPHABET[randomInt(TOKEN_ALPHABET.length)]
).join("")}`;

/** Where a worker pane runs: the run directory that stands for the daemon's state directory and
 * the issue's workspace, the stand-in daemon's port, and the Oh My Pi profile. */
export interface WorkerLaunch {
  readonly rig: string;
  readonly port: number;
  readonly profile: string;
}

/** The claim a worker pane is launched for: its project, issue and role, and the repository and
 * default branch of the run's own world, which the pane's addressing names and whose owner the
 * claim's gh bundle is the directory of. The caller reads both out of the run's world
 * (worker-pane.ts); why they are the world's and no name of this rig's own: rig.sh's worker
 * world comment, which holds that story once. */
export interface WorkerClaim {
  readonly project: string;
  readonly issue: string;
  readonly role: LegionRole;
  readonly repository: string;
  readonly defaultBranch: string;
}

/** Where a pane's daemon is read from: a checkout's daemon module (`DAEMON_MODULE` for this one),
 * whose embedded role prompts compose the pane's system prompt when `systemPrompt` is set. */
export interface PaneSource {
  readonly daemonModule: string;
  readonly systemPrompt?: boolean;
  /** The real gh the pane is told, as an operator names one with `LEGION_GH_PATH`: the rig's
   * recording stand-in. Omitted, the daemon's own resolution on the inherited PATH. */
  readonly ghPath?: string;
}

/** The pane the Legion daemon's tmux runtime gives a phase worker for `claim`, pointed at the
 * scratch state directory and the stand-in daemon, over the caller's own environment. Everything
 * the daemon tells the pane comes from the daemon's own functions in `source`'s module
 * (`daemon-pane.go`): the claim's identity, the daemon URL, the state directory and workspace,
 * Envoy (the listener and NATS the pane's own extension reaches from the caller's environment and
 * the profile's envoy.json, `envoySettings`, which reads no Envoy from the workspace's
 * `.envoy/envoy.json`; the default listener and no NATS when that Envoy is off), the claim's gh
 * bundle, which holds `RIG_GH_TOKEN` (`LEGION_GH_OWNERS_DIR`, `GH_CONFIG_DIR` and `LEGION_GH_PATH`,
 * `source.ghPath`, or the first `gh` on the inherited PATH, which must be no script), with
 * `GH_TOKEN`, `GITHUB_TOKEN` and `GH_HOST` set empty,
 * `PI_SHELL_PREFIX`, the four XDG base directories, the boot token pointer
 * (`<state>/secrets/boot`, the file the stand-in checks), and PATH with the launcher directory
 * first; no grant file, since the `legion` tool mints its grants in-process and no agent runs
 * `legion` from bash; and, with `systemPrompt`, the system prompt argument, whose addressing
 * names `claim.repository` and `claim.defaultBranch`, the world's own. An inherited
 * `ANTHROPIC_API_KEY`, every `LEGION_*` and `DISPATCH_*` value and the caller's own GitHub
 * variables are dropped first. The profile's agent directory, and so its envoy.json, is under the
 * inherited `HOME`. Throws `envoy: <why>` when that Envoy is off with an error, or on with inbound
 * messages off (`inbound.off`: no NATS server, or its seed refused), since the pane's Legion claim
 * refuses then. */
export function workerPane(
  launch: WorkerLaunch,
  inherited: NodeJS.ProcessEnv,
  claim: WorkerClaim,
  source: PaneSource
): DaemonPane {
  const env: Record<string, string> = {};
  for (const [key, value] of Object.entries(inherited)) {
    if (value === undefined) continue;
    if (key === "ANTHROPIC_API_KEY" || key.startsWith("LEGION_") || key.startsWith("DISPATCH_"))
      continue;
    if (["GH_CONFIG_DIR", "GH_TOKEN", "GITHUB_TOKEN", "GH_HOST"].includes(key)) continue;
    if (key === "OMP_SESSION_ID" || key === "TMUX" || key === "TMUX_PANE") continue;
    env[key] = value;
  }
  const home = env.HOME ?? os.homedir();
  const stateDir = path.join(launch.rig, "state");
  const workspace = path.join(launch.rig, "ws");
  const profileEnv = {
    ...env,
    OMP_PROFILE: launch.profile,
    PI_CODING_AGENT_DIR: path.join(home, ".omp", "profiles", launch.profile, "agent"),
  };
  const envoy = sessionEnvoySettings(sessionConfigFile("omp", profileEnv), profileEnv, {
    cwd: workspace,
  });
  if (!envoy.enabled && envoy.error !== null) throw new Error(`envoy: ${envoy.error}`);
  if (envoy.enabled && "off" in envoy.inbound) throw new Error(`envoy: ${envoy.inbound.off}`);
  const request: PaneRequest = {
    ...claim,
    stateDir,
    workspace,
    daemonUrl: `http://127.0.0.1:${launch.port}`,
    envoyUrl: envoy.enabled ? envoy.url : DEFAULT_ENVOY_URL,
    natsUrls: envoy.enabled && "natsUrls" in envoy.inbound ? envoy.inbound.natsUrls : [],
    bootTokenFile: path.join(stateDir, "secrets", "boot"),
    ghToken: RIG_GH_TOKEN,
    path: env.PATH ?? "",
    systemPrompt: source.systemPrompt,
    ghPath: source.ghPath,
  };
  const pane = JSON.parse(
    runDaemonPane(source.daemonModule, ["pane"], JSON.stringify(request))
  ) as DaemonPane;
  return {
    ...pane,
    env: {
      ...env,
      OMP_PROFILE: profileEnv.OMP_PROFILE,
      PI_PROFILE: launch.profile,
      PI_CODING_AGENT_DIR: profileEnv.PI_CODING_AGENT_DIR,
      PI_NOTIFICATIONS: "off",
      PI_NO_TITLE: "1",
      ...pane.env,
    },
  };
}

if (import.meta.main) {
  const [command, checkout, ...rest] = Bun.argv.slice(2);
  const message = (error: unknown) => (error instanceof Error ? error.message : String(error));
  if (command === "gh-selectors" && checkout !== undefined && rest.length === 1) {
    try {
      writeFileSync(rest[0] ?? "", `${JSON.stringify(labelGhSelectors(checkout))}\n`);
    } catch (error) {
      console.error(`daemon-pane gh-selectors: ${message(error)}`);
      process.exit(1);
    }
    process.exit(0);
  }
  const [commit = "", entry = ""] = rest;
  if (command === "review-round" && checkout !== undefined && rest.length === 2) {
    if (!/^(?:0|[1-9][0-9]*)$/.test(entry)) {
      console.error(`daemon-pane review-round: entry ${JSON.stringify(entry)} is no entry number`);
      process.exit(2);
    }
    try {
      process.stdout.write(reviewRoundSentences(checkout, commit, Number(entry)));
    } catch (error) {
      console.error(`daemon-pane review-round: ${message(error)}`);
      process.exit(1);
    }
    process.exit(0);
  }
  if (command !== "facts" || checkout === undefined || rest.length === 0) {
    console.error(
      "usage: bun daemon-pane.ts facts <checkout> <role>[:<phase>]... | gh-selectors <checkout> <file> | review-round <checkout> <commit> <entry>"
    );
    process.exit(2);
  }
  // Without a build each fact is a `go run`, which is the cost this read is made to take off a
  // stand-in's boot; without the work directory the facts go to a cache no stand-in reads.
  for (const [variable, what] of [
    ["SKILL_SCENARIOS_DAEMON_PANE", "build of daemon-pane.go to read the daemon's facts with"],
    ["SKILL_SCENARIOS_WORK", "work directory, whose facts cache the label's stand-ins read"],
  ] as const) {
    if ((process.env[variable] ?? "") === "") {
      console.error(`daemon-pane facts: ${variable} names no ${what}`);
      process.exit(2);
    }
  }
  for (const boot of rest) {
    const [role = "", phase = ""] = boot.split(":");
    if (!isLegionRole(role)) {
      console.error(`daemon-pane facts: ${boot} names no Legion role`);
      process.exit(2);
    }
    try {
      standinFacts(checkout, role, phase);
    } catch (error) {
      console.error(
        `daemon-pane facts: the ${role}'s stand-in${phase === "" ? "" : ` in ${phase}`}: ${message(error)}`
      );
      process.exit(1);
    }
  }
  // The gh stand-in of every run of the label refuses an owner by these (worker_boot writes them
  // into the run), so a wrapper whose table holds a form this rig does not model fails the batch
  // here, naming it, before any run starts.
  try {
    labelGhSelectors(checkout);
  } catch (error) {
    console.error(`daemon-pane facts: the gh stand-in: ${message(error)}`);
    process.exit(1);
  }
}
