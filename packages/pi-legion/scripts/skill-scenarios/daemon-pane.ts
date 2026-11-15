/**
 * The rig's reading of the Legion daemon: `daemon-pane.go`, run as a main inside a checkout's
 * daemon module, which is how it reaches the daemon's internal packages (a build of it there,
 * `SKILL_SCENARIOS_DAEMON_PANE`, or `go run -overlay`), and `daemon-routes.go`, run with `go run`
 * outside every module, since reading the routes needs only the daemon's files. The rig asks them
 * rather than restating the daemon: a change to the daemon reaches the rig by construction. The
 * one exception is `daemon-pane.go`'s own table of the handoff words, which the program carries
 * because one program runs against both checkouts of a comparison and the daemon's table has not
 * always had the same name; its comment says so. `workerPane` builds a phase worker's whole pane
 * environment over it, and `standinFacts` reads every fact the daemon stand-in answers by.
 *
 * Usage: `bun daemon-pane.ts facts <checkout> <role>[:<phase>]...` reads, through the build
 * `SKILL_SCENARIOS_DAEMON_PANE` names, the facts `standinFacts` reads for a stand-in of each role
 * booted in each phase (none: the role's own) into the facts cache under `SKILL_SCENARIOS_WORK`,
 * so a stand-in's boot after it reads each out of a file (rig.sh's label_facts). It exits 1,
 * naming the fact, when one cannot be read, and 2 on a usage error or with no build or no work
 * directory named.
 */
import { createHash, randomUUID } from "node:crypto";
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
import { NOT_CURRENT_PHASE } from "./completion-answers";

/** This checkout's daemon module, the one the rig reads unless a run names another checkout's. */
export const DAEMON_MODULE = path.resolve(import.meta.dir, "../../../daemon");

const PROGRAM = path.join(import.meta.dir, "daemon-pane.go");
const ROUTES_PROGRAM = path.join(import.meta.dir, "daemon-routes.go");

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

/** daemon-routes.go's printout of the routes the API package at `api` registers. The program
 * imports the standard library alone and runs from its own directory with go.work off, where no
 * go.mod above it makes it part of a module: building it compiles a few standard packages and
 * downloads nothing, so a Go cache that holds nothing yet costs seconds rather than the daemon
 * module's whole build. `api` is resolved against the caller's directory first, as `goSources`
 * reads it, since the program runs in another. */
function runRoutes(api: string): string {
  const dir = path.resolve(api);
  return runProgram(`daemon-routes ${dir}`, ["go", "run", ROUTES_PROGRAM, dir], import.meta.dir, {
    GOWORK: "off",
  });
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
 * directory (`SKILL_SCENARIOS_WORK`, which rig.sh exports), else under `TMPDIR`; never under HOME
 * or a `/tmp` nobody named. With neither set nothing is kept, and every read runs the program. */
function factsCache(): string | undefined {
  const work = process.env.SKILL_SCENARIOS_WORK;
  if (work !== undefined && work !== "") return path.join(work, "daemon-facts");
  const tmp = process.env.TMPDIR;
  if (tmp !== undefined && tmp !== "") return path.join(tmp, "skill-scenarios-daemon-facts");
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
 * agent boots. Its daemon is the one the label's runs stand in for (`labelRoutes`). Throws for a
 * run with no such line: what that run's daemon serves is unknown. */
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

/** Every route the daemon of `checkout` registers, `<METHOD> <path>` each, read out of that
 * checkout's API source by daemon-routes.go (`runRoutes`), which parses the checkout's files and
 * compiles nothing of them or of any daemon module, so a substitute checkout holding the source
 * alone is read the same way. The stand-in answers by this set and health holds every 404 to it,
 * so both read one set by one reading. Throws what the reading refused. */
export function labelRoutes(checkout: string): readonly string[] {
  const daemon = daemonOf(checkout);
  const api = path.join(daemon, "internal", "api");
  return cachedRead(ROUTES_PROGRAM, () => runRoutes(api), ["routes"], daemon, goSources(api))
    .split("\n")
    .filter((line) => line !== "");
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
 * `count-default`, over that checkout's config source. Throws what the reading refused. */
export function countDefault(checkout: string, key: string): number {
  const daemon = daemonOf(checkout);
  const config = path.join(daemon, "internal", "config", "config.go");
  const command = ["count-default", key];
  const printed = cachedRead(
    PROGRAM,
    () => runDaemonPane(DAEMON_MODULE, [...command, config]),
    command,
    daemon,
    [config]
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
  return {
    routes,
    turnOutcomeLimit,
    invalidSecret,
    invalidBootToken,
    staleGeneration,
    rolePhase,
    handoffWord,
    phase: served,
    notCurrentPhase: lateRefusal,
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
 * files, and the pane's own `gh auth token` prints it. A placeholder, never a secret. */
export const RIG_GH_TOKEN = "ghs_rig_token";

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
  const [command, checkout, ...boots] = Bun.argv.slice(2);
  if (command !== "facts" || checkout === undefined || boots.length === 0) {
    console.error("usage: bun daemon-pane.ts facts <checkout> <role>[:<phase>]...");
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
  for (const boot of boots) {
    const [role = "", phase = ""] = boot.split(":");
    if (!isLegionRole(role)) {
      console.error(`daemon-pane facts: ${boot} names no Legion role`);
      process.exit(2);
    }
    try {
      standinFacts(checkout, role, phase);
    } catch (error) {
      console.error(
        `daemon-pane facts: the ${role}'s stand-in${phase === "" ? "" : ` in ${phase}`}: ${error instanceof Error ? error.message : String(error)}`
      );
      process.exit(1);
    }
  }
}
