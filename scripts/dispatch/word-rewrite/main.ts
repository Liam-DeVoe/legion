import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { parseArgs } from "node:util";
import {
  type Category,
  ISSUE_KEY,
  readCommandLine,
  runScript,
  UsageError,
} from "../production-check-runtime.ts";
import {
  type Ancestry,
  type DaemonStates,
  daemonAtLeast,
  describeReading,
  dispatchBuild,
  gitAncestry,
  liveDaemonStates,
  readDaemons,
} from "./daemons.ts";
import {
  type DispatchReads,
  type DispatchWrites,
  liveDispatch,
  ownerName,
  Project,
  ReadError,
  Whoami,
} from "./dispatch-api.ts";
import { DEFAULT_MODEL, gatewayModel, type Model } from "./gateway.ts";
import {
  type Context,
  type Examined,
  examine,
  type Item,
  inPool,
  issueItems,
  projectDocumentItem,
  projectItems,
  READ_CONCURRENCY,
  readTree,
} from "./inventory.ts";
import {
  builtInMap,
  type MapEntry,
  MapFileError,
  parseExtraMap,
  readContract,
} from "./name-map.ts";
import { Pace, runTimeMs } from "./pace.ts";
import { Tally } from "./report.ts";
import { type Clock, loggingFetch, RequestRate, requestLog, SYSTEM_CLOCK } from "./request-log.ts";
import {
  emptyState,
  eventsAfter,
  newestEventId,
  parseState,
  parseStateDocument,
  type RunState,
  readStateDocument,
  STATE_BOUND_BYTES,
  type StateDocument,
  StateTooLarge,
  StateWriteError,
  serializeState,
  stateBytes,
  VERDICT_BYTES,
  writeStateDocument,
} from "./state.ts";
import {
  heldWrite,
  type JudgeCalls,
  type WriteContext,
  writeItem,
  writerRefusal,
} from "./write.ts";

/**
 * The Dispatch rewrite tool of INFRA-2017 (`scripts/dispatch/README.md`, "Word rewrite"). Report
 * mode reads every project's open issues and project documents, lists each occurrence of the word
 * with the place that leaves it, and writes nothing but its local `--state` copy. Write mode asks
 * the model about every occurrence no place leaves and writes each rewrite through Dispatch's own
 * edit routes, keeping its state in a project document between runs.
 */

export const USAGE = `bun scripts/dispatch/word-rewrite/main.ts report|write --tree <KEY> --merge-commit <sha> [flags]

  --tree <KEY>                 the root whose issues name the word as the subject of the change
  --merge-commit <sha>         the merge the Dispatch API names wait for (before the merge: the head)
  --project <KEY>              a project to read; repeat; every project when none is named
  --include-closed             read closed issues too, and list them
  --state-document <ref>       dispatch://<PROJECT>/artifact/<slug>: the state kept between runs
  --state <file>               a local copy of the state, written at the end of every run
  --extra-map <file>           more map entries, one JSON object per line
  --request-log <file>         append one "METHOD path status" line per HTTP request
  --daemon-state <KEY>=<url>   a project's daemon state address, where Dispatch names none; repeat
  --no-daemon <KEY>            a project that has no Legion daemon; repeat
  --stop-at <time>             an RFC 3339 time at which the run stops: it starts no new read,
                               judgement or write, saves its state and exits 0
  --stop-after <duration>      the same, a duration after the run starts, such as 90m or 1h30m
  --whole                      read every project again from the start, with the state's verdicts,
                               rather than only the evented, held-back and waiting items; report
                               mode then writes no state, so it takes no --state
write mode only:
  --summary <text>             the summary of each document version the tool writes (required)
  --model <id>                 the model the judge asks (default ${DEFAULT_MODEL})
  --concurrency <n>            model calls at once (default 4)
  --max-writes <n>             stop writing after n writes; the rest wait for the next run
  --pause-before-write <file>  wait before each write until the file exists
  --help                       print this text`;

export interface Options {
  readonly mode: "report" | "write";
  readonly tree: string;
  readonly mergeCommit: string;
  readonly projects: readonly string[];
  readonly includeClosed: boolean;
  readonly stateDocument: StateDocument | undefined;
  readonly stateFile: string | undefined;
  readonly extraMap: string | undefined;
  readonly requestLog: string | undefined;
  readonly stateAddress: ReadonlyMap<string, string>;
  readonly noDaemon: ReadonlySet<string>;
  readonly summary: string | undefined;
  readonly model: string;
  readonly concurrency: number;
  readonly maxWrites: number | undefined;
  readonly pauseBeforeWrite: string | undefined;
  /** When the run stops, as `--stop-at` names it (milliseconds since the epoch). */
  readonly stopAt: number | undefined;
  /** How long after its start the run stops, as `--stop-after` names it. */
  readonly stopAfterMs: number | undefined;
  /** Read every project again from the start (`--whole`). */
  readonly whole: boolean;
}

const FLAGS = {
  tree: { type: "string" },
  "merge-commit": { type: "string" },
  project: { type: "string", multiple: true },
  "include-closed": { type: "boolean" },
  "state-document": { type: "string" },
  state: { type: "string" },
  "extra-map": { type: "string" },
  "request-log": { type: "string" },
  "daemon-state": { type: "string", multiple: true },
  "no-daemon": { type: "string", multiple: true },
  summary: { type: "string" },
  model: { type: "string" },
  concurrency: { type: "string" },
  "max-writes": { type: "string" },
  "pause-before-write": { type: "string" },
  "stop-at": { type: "string" },
  "stop-after": { type: "string" },
  whole: { type: "boolean" },
} as const;

const FLAG_TYPES: Readonly<Record<string, { readonly type: string }>> = FLAGS;
const WRITE_ONLY = ["summary", "model", "concurrency", "max-writes", "pause-before-write"];
const PROJECT_KEY = /^[A-Z][A-Z0-9]*$/;
const COMMIT = /^[0-9a-f]{7,40}$/;

function refuse(reason: string): never {
  throw new UsageError(`${reason}\n${USAGE}`);
}

/** The first argument that names no flag of the tool, so a refusal can name it. */
function unknownFlag(argv: readonly string[]): string | undefined {
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index] ?? "";
    if (!argument.startsWith("-")) continue;
    const name = argument.replace(/^--?/, "").split("=")[0] ?? "";
    if (!argument.startsWith("--") || !Object.hasOwn(FLAG_TYPES, name)) {
      return argument.split("=")[0];
    }
    if (FLAG_TYPES[name]?.type === "string" && !argument.includes("=")) index += 1;
  }
  return undefined;
}

function count(value: string | undefined, flag: string, least: number): number | undefined {
  if (value === undefined) return undefined;
  const number = Number(value);
  if (!/^[0-9]+$/.test(value) || number < least) refuse(`--${flag} takes a whole number`);
  return number;
}

const RFC_3339 = /^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2})$/;
const DURATION = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/;

/** `--stop-at`'s time, in milliseconds since the epoch. */
function stopTime(value: string | undefined): number | undefined {
  if (value === undefined) return undefined;
  const at = Date.parse(value);
  if (!RFC_3339.test(value) || Number.isNaN(at)) refuse("--stop-at takes an RFC 3339 time");
  return at;
}

/** `--stop-after`'s duration (hours, minutes and seconds, such as `1h30m`), in milliseconds. */
function stopDuration(value: string | undefined): number | undefined {
  if (value === undefined) return undefined;
  const parts = DURATION.exec(value);
  const [hours = "0", minutes = "0", seconds = "0"] =
    parts?.slice(1).map((part) => part ?? "0") ?? [];
  const ms = ((Number(hours) * 60 + Number(minutes)) * 60 + Number(seconds)) * 1_000;
  if (parts === null || ms <= 0) refuse("--stop-after takes a duration such as 90m or 1h30m");
  return ms;
}

/** The flags as `parseArgs` reads them, once every argument names a flag of the tool. */
function readFlags(argv: readonly string[]) {
  try {
    return parseArgs({ args: [...argv], options: FLAGS, allowPositionals: true, strict: true });
  } catch {
    // parseArgs's own message is not repeated: every flag it can refuse here is named already.
    return refuse("a flag is missing its value");
  }
}

export function parseArguments(argv: readonly string[]): Options {
  const unknown = unknownFlag(argv);
  if (unknown !== undefined) refuse(`unknown flag ${unknown}`);
  const parsed = readFlags(argv);
  const { values, positionals } = parsed;
  const [mode, ...rest] = positionals;
  if (mode !== "report" && mode !== "write") refuse("name the mode: report or write");
  if (rest.length > 0) refuse(`unexpected argument ${rest[0]}`);
  if (mode === "report") {
    const given = WRITE_ONLY.find((flag) => values[flag as keyof typeof values] !== undefined);
    if (given !== undefined) refuse(`flag --${given} is for write mode`);
  }
  if (values.whole === true && mode === "report" && values.state !== undefined) {
    refuse("report mode's --whole writes no state, so it takes no --state");
  }
  const tree = values.tree;
  if (tree === undefined || !ISSUE_KEY.test(tree)) refuse("--tree takes an issue key");
  const mergeCommit = values["merge-commit"];
  if (mergeCommit === undefined || !COMMIT.test(mergeCommit)) {
    refuse("--merge-commit takes a commit sha");
  }
  const projects = values.project ?? [];
  const badProject = projects.find((project) => !PROJECT_KEY.test(project));
  if (badProject !== undefined) refuse(`--project takes a project key, not ${badProject}`);
  const stateRef = values["state-document"];
  const stateDocument = stateRef === undefined ? undefined : parseStateDocument(stateRef);
  if (stateRef !== undefined && stateDocument === undefined) {
    refuse("--state-document takes dispatch://<PROJECT>/artifact/<slug>");
  }
  const stateAddress = new Map<string, string>();
  for (const pair of values["daemon-state"] ?? []) {
    const [project, url] = [pair.slice(0, pair.indexOf("=")), pair.slice(pair.indexOf("=") + 1)];
    if (!pair.includes("=") || !PROJECT_KEY.test(project) || !/^https?:\/\/[^/]+\/?$/.test(url)) {
      refuse("--daemon-state takes <PROJECT>=<http(s) URL with no path>");
    }
    stateAddress.set(project, url);
  }
  const noDaemon = new Set(values["no-daemon"] ?? []);
  for (const project of noDaemon) {
    if (!PROJECT_KEY.test(project)) refuse(`--no-daemon takes a project key, not ${project}`);
    if (stateAddress.has(project)) refuse(`--no-daemon ${project} and --daemon-state ${project}`);
  }
  const summary = values.summary;
  if (mode === "write") {
    if (summary === undefined || summary.trim() === "") refuse("write mode needs --summary");
    if (stateDocument === undefined) refuse("write mode needs --state-document");
  }
  return {
    mode,
    tree,
    mergeCommit,
    projects,
    includeClosed: values["include-closed"] === true,
    stateDocument,
    stateFile: values.state,
    extraMap: values["extra-map"],
    requestLog: values["request-log"],
    stateAddress,
    noDaemon,
    summary,
    model: values.model ?? DEFAULT_MODEL,
    concurrency: count(values.concurrency, "concurrency", 1) ?? 4,
    maxWrites: count(values["max-writes"], "max-writes", 0),
    pauseBeforeWrite: values["pause-before-write"],
    stopAt: stopTime(values["stop-at"]),
    stopAfterMs: stopDuration(values["stop-after"]),
    whole: values.whole === true,
  };
}

/** What a run calls out to, which a test replaces with fakes. */
export interface Deps {
  readonly reads: DispatchReads;
  readonly writes: DispatchWrites | undefined;
  readonly model: Model | undefined;
  readonly daemonStates: DaemonStates;
  readonly ancestry: Ancestry;
  readonly clock: Clock;
  /** The daemon API contract of the checkout the tool runs from. */
  readonly contract: number;
  readonly extraEntries: readonly MapEntry[];
  readonly out: (line: string) => void;
  /** How many requests the run made so far, for its last lines. */
  readonly requests: () => number;
}

/** How many items are read before their turn of writing, so a project's texts are not all held
 *  at once. */
const CHUNK = 16;

/** Report mode's lines of the pace: each owner whose items wait for the judge, whose subscribers
 *  no credential of the tool can read, and the run time the three bounds give for one write per
 *  waiting item. */
function paceLines(tally: Tally): string[] {
  const lines = tally.owners().map((owner) => `owner ${ownerName(owner)} subscribers=unknown`);
  const writes = tally.waiting.map(({ item }) => ({
    owner: ownerName(item.owner),
    project: item.project,
  }));
  lines.push(`pace writes=${writes.length} run-time-s=${Math.ceil(runTimeMs(writes) / 1_000)}`);
  return lines;
}

/** Reads the state at the run's start: the state document, else the local copy, else none. */
async function startingState(
  options: Options,
  reads: DispatchReads
): Promise<{ state: RunState; token: string | null }> {
  if (options.stateDocument !== undefined) {
    return readStateDocument(reads, options.stateDocument);
  }
  if (options.stateFile !== undefined && existsSync(options.stateFile)) {
    return { state: parseState(readFileSync(options.stateFile, "utf8")), token: null };
  }
  return { state: emptyState(), token: null };
}

/**
 * The items a run reads again after the first whole read: each item an event after the cursor
 * belongs to, in the projects the run reads. Dispatch gives an issue document's events to its
 * issue and sets no top-level `artifact_id` on them (`ownerForArtifact`), so an event of an issue
 * names the issue's title and every document of it; an event of a project document names that
 * document.
 */
async function itemsOfEvents(
  reads: DispatchReads,
  state: RunState,
  projects: ReadonlySet<string>
): Promise<Item[]> {
  const events = await eventsAfter(reads, state.cursor ?? 0);
  const last = events.at(-1);
  if (last !== undefined) state.cursor = last.id;
  const wanted = new Map<string, Promise<Item[]>>();
  for (const event of events) {
    if (!projects.has(event.project)) continue;
    const key = event.issue_key;
    const id = event.artifact_id;
    if (key !== null) {
      if (!wanted.has(key)) wanted.set(key, issueItems(reads, key));
    } else if (id !== null && !wanted.has(id)) {
      wanted.set(id, projectDocumentItem(reads, event.project, id));
    }
  }
  return (await Promise.all(wanted.values())).flat();
}

/** Each item of a list of state entries, read afresh: an issue's title or document, or a project
 *  document. An item that is gone is left out. */
async function readEntries(
  reads: DispatchReads,
  entries: readonly {
    readonly project: string;
    readonly issue: string | null;
    readonly id: string;
  }[]
): Promise<Item[]> {
  const items: Item[] = [];
  const seen = new Set<string>();
  for (const entry of entries) {
    if (seen.has(entry.id)) continue;
    seen.add(entry.id);
    const found =
      entry.issue === null
        ? await projectDocumentItem(reads, entry.project, entry.id)
        : (await issueItems(reads, entry.issue)).filter((item) => item.id === entry.id);
    items.push(...found);
  }
  return items;
}

/** The gate an occurrence's kept name waits for, as the state's `waiting` writes it, or null for
 *  a name no reach of a daemon or of Dispatch's build turns on. */
function gateOf(entry: MapEntry | null): string | null {
  if (entry === null || entry.new === "" || entry.from === "merged") return null;
  return "contract" in entry.from ? `c${entry.from.contract}` : "m";
}

/** The state's `waiting` entries for one item, as its occurrences stand now: each gate of a name
 *  kept as `code-name-not-yet`, once. */
function keepWaiting(state: RunState, examined: Examined): void {
  const item = examined.item;
  state.waiting = state.waiting.filter((entry) => entry.id !== item.id);
  const gates = new Set<string>();
  for (const occurrence of examined.occurrences) {
    if (occurrence.reason !== "code-name-not-yet") continue;
    const gate = gateOf(occurrence.name);
    if (gate !== null) gates.add(gate);
  }
  for (const gate of gates) {
    state.waiting.push({
      project: item.project,
      issue: item.issue?.key ?? null,
      id: item.id,
      gate,
    });
  }
}

/** The state's `refused` entries for one item, as its text stands now: an entry whose key no
 *  occurrence of the item holds any more (its text changed) goes, with its `j` verdict, so the
 *  new text is judged. */
function keepRefused(state: RunState, examined: Examined): void {
  const item = examined.item;
  const keys = new Set(examined.occurrences.map((occurrence) => occurrence.key));
  state.refused = state.refused.filter((entry) => {
    if (entry.id !== item.id || keys.has(entry.key)) return true;
    state.verdicts.delete(entry.key);
    return false;
  });
}

/** What a save compares, so the state is written only when something in it changed. */
function stateMark(state: RunState): string {
  const held = state.held.map((write) => `${write.id}${write.retry ? "!" : ""}`).join(",");
  const waiting = state.waiting.map((entry) => `${entry.id}:${entry.gate}`).join(",");
  const refused = state.refused.map((entry) => entry.key).join(",");
  return `${state.cursor}|${state.whole}|${state.finished.join(",")}|${state.verdicts.size}|${held}|${waiting}|${refused}`;
}

/** Write mode saves its state at most once in this time while it holds something unsaved. */
export const SAVE_INTERVAL_MS = 60_000;

/** When the run stops, where its options name a stop time: the earlier of `--stop-at` and
 *  `--stop-after` counted from the run's start. */
function stopTimeOf(options: Options, started: number): number | undefined {
  const after = options.stopAfterMs === undefined ? undefined : started + options.stopAfterMs;
  if (options.stopAt === undefined) return after;
  return after === undefined ? options.stopAt : Math.min(options.stopAt, after);
}

/** The run's model calls, and the occurrences it judged again for a retried refused write. */
function callsLine(calls: JudgeCalls): string {
  return `model-calls first=${calls.first} second=${calls.second} again=${calls.again} judged-again=${calls.againOccurrences}`;
}

/** One whole run of either mode; answers the exit status. */
export async function runTool(options: Options, deps: Deps): Promise<number> {
  const { reads, out } = deps;
  const started = deps.clock.now();
  const stopTime = stopTimeOf(options, started);
  let halted = false;
  /** Whether the stop time has come: from then on the run starts no read, judgement or write. */
  const stopped = () => {
    if (!halted && stopTime !== undefined && deps.clock.now() >= stopTime) halted = true;
    return halted;
  };
  const calls: JudgeCalls = { first: 0, second: 0, again: 0, againOccurrences: 0 };
  /** The state saved once more when a read fails, where the run has a state to save. */
  let saveOnFailure: (() => Promise<void>) | undefined;
  try {
    const whoami = Whoami.parse(await reads.whoami());
    if (options.mode === "write") {
      const refusal = writerRefusal(whoami);
      if (refusal !== null) {
        out(`refused: ${refusal}`);
        return 2;
      }
    }
    const scope = whoami.kind === "agent" ? whoami.project : null;
    const build = await dispatchBuild(await reads.healthz(), options.mergeCommit, deps.ancestry);
    out(`dispatch commit=${build.commit ?? "null"} holds-merge=${build.holdsMerge ? "yes" : "no"}`);
    const entries = [...builtInMap(deps.contract), ...deps.extraEntries];
    out(`map entries=${entries.length} contract=${deps.contract}`);
    const known = Project.array()
      .parse(await reads.projects())
      .map((project) => project.key);
    const missing = options.projects.find((project) => !known.includes(project));
    if (missing !== undefined) {
      out(`unavailable: project ${missing} is not in Dispatch`);
      return 2;
    }
    const projects = options.projects.length > 0 ? [...options.projects] : known;
    const daemons = await readDaemons(projects, reads, deps.daemonStates, {
      noDaemon: options.noDaemon,
      stateAddress: options.stateAddress,
    });
    for (const [project, reading] of daemons) out(`daemon ${project} ${describeReading(reading)}`);
    const reachOf = (project: string) => ({
      daemonAtLeast: (contract: number) => daemonAtLeast(daemons.get(project), contract),
      dispatchHoldsMerge: build.holdsMerge,
    });
    const tree = await readTree(reads, options.tree);
    let { state, token } = await startingState(options, reads);
    const context: Context = { reads, entries, reachOf, scope, tree };
    const tally = new Tally();
    const seen = new Set<string>();
    const writing = options.mode === "write";
    let writeContext: WriteContext | undefined;
    if (writing) {
      const model = deps.model;
      if (deps.writes === undefined || model === undefined || options.summary === undefined) {
        throw new Error("write mode runs with writes, a model and a summary");
      }
      writeContext = {
        reads,
        writes: deps.writes,
        model,
        pace: new Pace(deps.clock),
        clock: deps.clock,
        state,
        entries,
        reachOf,
        summary: options.summary,
        concurrency: options.concurrency,
        remaining: options.maxWrites ?? Number.POSITIVE_INFINITY,
        pauseFile: options.pauseBeforeWrite,
        events: new Map(),
        retried: new Set(state.held.filter((write) => write.retry).flatMap((write) => write.keys)),
        calls,
      };
    }

    // Write mode saves the state when it holds something unsaved and a minute has passed since
    // its last save (or its start), and once more when the run stops; report mode never does.
    let savedAt = started;
    let savedMark = stateMark(state);
    const save = async (final: boolean) => {
      const writes = writeContext?.writes;
      if (writes === undefined || options.stateDocument === undefined) return;
      const mark = stateMark(state);
      if (mark === savedMark) return;
      const now = deps.clock.now();
      if (!final && now - savedAt < SAVE_INTERVAL_MS) return;
      token = await writeStateDocument({ reads, writes }, options.stateDocument, state, token);
      savedAt = now;
      savedMark = mark;
    };
    saveOnFailure = () => save(true);

    /** Handles each item once; answers the items the stop time left unhandled. */
    const handle = async (items: readonly Item[]): Promise<Item[]> => {
      const fresh = items.filter((item) => !seen.has(item.id));
      for (const item of fresh) seen.add(item.id);
      for (let start = 0; start < fresh.length; start += CHUNK) {
        if (stopped()) return fresh.slice(start);
        const chunk = fresh.slice(start, start + CHUNK);
        const examined: Examined[] = await inPool(chunk, READ_CONCURRENCY, (item) =>
          examine(context, item)
        );
        for (const [index, one] of examined.entries()) {
          if (stopped()) return [...chunk.slice(index), ...fresh.slice(start + CHUNK)];
          keepWaiting(state, one);
          keepRefused(state, one);
          if (writeContext === undefined) {
            tally.add(one, state.verdicts);
            continue;
          }
          const result = await writeItem(writeContext, one);
          tally.add(one, state.verdicts);
          await save(false);
          if (result === "nothing-to-judge") continue;
          const item = one.item;
          out(
            `write kind=${item.kind} project=${item.project} issue=${item.issue?.key ?? "-"} document=${item.artifact?.slug ?? "-"} result=${result}`
          );
        }
      }
      return [];
    };
    /** Write mode keeps for the next run the items a stop time left unhandled. */
    const holdLeft = (left: readonly Item[]) => {
      if (!writing) return;
      for (const item of left) state.held.push(heldWrite(item, []));
    };

    if (options.whole) {
      // The whole read: the held-back writes, then every project from the start, with the
      // state's verdicts. Its cursor and the projects it finished stay as they were, and a project
      // the stop time cuts is left to the next whole read. Write mode saves its state; report mode
      // writes none.
      if (!stopped()) {
        const held = state.held;
        if (writing) state.held = [];
        holdLeft(await handle(await readEntries(reads, held)));
      }
      for (const project of projects) {
        if (stopped()) break;
        await handle(await projectItems(reads, project, options.includeClosed));
      }
      await save(true);
    } else {
      // First the held-back writes, and the items whose kept names the project or Dispatch's
      // build has now reached. Report mode reads them and leaves the held-back list as it is.
      if (!stopped()) {
        const held = state.held;
        if (writing) state.held = [];
        const reached = state.waiting.filter(
          (entry) =>
            projects.includes(entry.project) &&
            (entry.gate === "m"
              ? build.holdsMerge
              : reachOf(entry.project).daemonAtLeast(Number(entry.gate.slice(1))))
        );
        holdLeft(await handle(await readEntries(reads, [...held, ...reached])));
      }
      if (!stopped() && !state.whole) {
        if (state.cursor === null) state.cursor = await newestEventId(reads);
        for (const project of projects) {
          if (state.finished.includes(project)) continue;
          if (stopped()) break;
          // A project the stop time cuts is read whole again by the next run, so its items are
          // not held back one by one.
          const left = await handle(await projectItems(reads, project, options.includeClosed));
          if (left.length > 0) break;
          state.finished.push(project);
          await save(false);
        }
        if (!stopped()) state.whole = true;
      } else if (!stopped()) {
        holdLeft(await handle(await itemsOfEvents(reads, state, new Set(projects))));
      }
      await save(true);
      if (options.stateFile !== undefined) writeFileSync(options.stateFile, serializeState(state));
    }

    tally.addKeptRefused(state.refused);
    for (const line of tally.lines) out(line);
    for (const line of tally.summary()) out(line);
    for (const line of tally.checkSection()) out(line);
    if (options.mode === "report") for (const line of paceLines(tally)) out(line);
    const bytes = stateBytes(state);
    const projected = bytes + tally.notJudged * VERDICT_BYTES;
    out(
      `state verdicts=${state.verdicts.size} held=${state.held.length} waiting=${state.waiting.length} not-judged=${tally.notJudged} bytes=${bytes} projected-bytes=${projected} bound=${STATE_BOUND_BYTES}`
    );
    out(callsLine(calls));
    out(`requests ${deps.requests()}`);
    out(`seconds ${Math.round((deps.clock.now() - started) / 1_000)}`);
    if (halted && stopTime !== undefined) {
      out(`result stopped at its stop time ${new Date(stopTime).toISOString()}`);
    } else {
      out("result done");
    }
    return 0;
  } catch (error) {
    if (error instanceof ReadError) {
      // A read that fails ends the run; what it judged and held back so far is kept.
      try {
        await saveOnFailure?.();
      } catch (saveError) {
        if (saveError instanceof StateTooLarge || saveError instanceof StateWriteError) {
          out(saveError.message);
        } else if (!(saveError instanceof ReadError)) {
          throw saveError;
        }
      }
      out(callsLine(calls));
      out(`unavailable: ${error.route} ${error.category}`);
      return 2;
    }
    if (error instanceof StateTooLarge || error instanceof StateWriteError) {
      out(error.message);
      return 1;
    }
    throw error;
  }
}

async function main(argv: readonly string[]): Promise<number> {
  if (argv.includes("--help") || argv.includes("-h")) {
    console.log(`usage: ${USAGE}`);
    return 0;
  }
  const prelude = readCommandLine(argv, parseArguments, "config" satisfies Category);
  if (!prelude.ready) return prelude.code;
  const { options, config } = prelude;
  const root = resolve(import.meta.dir, "../../..");
  let extraEntries: MapEntry[] = [];
  if (options.extraMap !== undefined) {
    try {
      extraEntries = parseExtraMap(readFileSync(options.extraMap, "utf8"));
    } catch (error) {
      console.error(error instanceof MapFileError ? error.message : "--extra-map cannot be read");
      return 2;
    }
  }
  const log = requestLog(options.requestLog);
  const fetchImpl = loggingFetch(fetch, log, new RequestRate());
  const { reads, writes } = liveDispatch(config, fetchImpl);
  return runTool(options, {
    reads,
    writes: options.mode === "write" ? writes : undefined,
    model:
      options.mode === "write"
        ? gatewayModel(options.model, (detail) => console.error(`model fault: ${detail}`))
        : undefined,
    daemonStates: liveDaemonStates(fetchImpl),
    ancestry: gitAncestry(root),
    clock: SYSTEM_CLOCK,
    contract: readContract(root),
    extraEntries,
    out: (line) => console.log(line),
    requests: () => log.lines().length,
  });
}

if (import.meta.main) await runScript(main);
