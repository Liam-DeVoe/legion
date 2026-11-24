import { z } from "zod";
import {
  type DispatchReads,
  type DispatchWrites,
  Event,
  Text,
  type WriteOutcome,
} from "./dispatch-api.ts";
import type { Examined } from "./inventory.ts";
import type { MapEntry, Reach } from "./name-map.ts";
import { VERDICT_CODES, VERDICT_KEY, type VerdictCode } from "./verdicts.ts";

/**
 * The small state the tool keeps between runs (INFRA-2017's plan, P6), as one fenced `json`
 * block of an unlinked project document (`--state-document dispatch://<P>/artifact/<slug>`):
 *
 * - `cursor`: the newest id of Dispatch's event log at the start of the first whole read, moved
 *   by each later run to the newest event it read before its first read of an item;
 * - `write_cursor`: the cursor of the last write-mode run, from which a report lists apart the
 *   occurrences changed since (`changes.ts`);
 * - `whole`: whether that read has finished, and `finished`, the projects it has finished;
 * - `verdicts`: each occurrence the judge or the settle command left, by its hashed key, as a
 *   one-letter code: its place by what it is, or `j` where both its answers failed;
 * - `held`: the writes the pace held back, or a stop time left unread, by item id and the keys of
 *   their occurrences, which the next run takes first;
 * - `waiting`: the items that hold a name of the map the tool kept because its project's daemon
 *   reads below the name's contract (`c<n>`) or Dispatch's build does not hold the merge (`m`), by
 *   item id and that gate, which a run reads again once the gate is reached, though no event
 *   names them;
 * - `refused`: the occurrences whose two answers failed, by item id and key, which every report
 *   lists until their text changes or the settle command settles them, and no run asks about
 *   again;
 * - `sessions`: the ids of the sessions that ran write mode or the settle command, whose edits a
 *   report sets aside (`changes.ts`).
 *
 * It holds only hashes, numbers (counts and the cursor), true or false, item, issue, event and
 * session ids, project keys and one-word codes: no text of any item. Dispatch caps a document at
 * 1 MiB, so a state past 900 KiB is refused (`state-too-large`), naming the size of each part,
 * rather than cut.
 */

/** A held-back write: its item, and the keys of the occurrences it judged. A title's `id` is its
 *  issue's key, the same as `issue`; a document's `id` is its artifact id. */
export interface HeldWrite {
  readonly project: string;
  /** The issue of a title or of an issue document; null for a project document. */
  readonly issue: string | null;
  /** The issue key of a title, the artifact id of a document. */
  readonly id: string;
  readonly keys: readonly string[];
  /** A write Dispatch refused, whose judged occurrences the next run judges again. */
  readonly retry: boolean;
}

/** What a kept name waits for: a daemon contract (`c<n>`) or Dispatch's build holding the merge
 *  (`m`). */
export type Gate = `c${number}` | "m";

const GATE = /^(c[1-9][0-9]*|m)$/;

/** The gate an occurrence's kept name waits for, or null for a name no reach of a daemon or of
 *  Dispatch's build turns on (a row that applies from the merge, or a Go test's name that no row
 *  names, which stands in with `from: "merged"`). */
export function gateOf(entry: MapEntry | null): Gate | null {
  if (entry === null || entry.from === "merged") return null;
  return "contract" in entry.from ? `c${entry.from.contract}` : "m";
}

/** Whether a project's reach has passed a gate. */
export function gateReached(gate: Gate, reach: Reach): boolean {
  return gate === "m" ? reach.dispatchHoldsMerge : reach.daemonAtLeast(Number(gate.slice(1)));
}

/** The state's `waiting` entries for one item, as its occurrences stand now: each gate of a name
 *  kept as `code-name-not-yet`, once. */
export function keepWaiting(state: RunState, examined: Examined): void {
  const item = examined.item;
  state.waiting = state.waiting.filter((entry) => entry.id !== item.id);
  const gates = new Set<Gate>();
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
export function keepRefused(state: RunState, examined: Examined): void {
  const item = examined.item;
  const keys = new Set(examined.occurrences.map((occurrence) => occurrence.key));
  state.refused = state.refused.filter((entry) => {
    if (entry.id !== item.id || keys.has(entry.key)) return true;
    state.verdicts.delete(entry.key);
    return false;
  });
}

/** An item that holds a name kept until its gate. */
export interface WaitingItem {
  readonly project: string;
  /** The issue of a title or of an issue document; null for a project document. */
  readonly issue: string | null;
  readonly id: string;
  readonly gate: Gate;
}

/** An occurrence whose two answers failed: its item and its verdict key. */
export interface RefusedOccurrence {
  readonly project: string;
  /** The issue of a title or of an issue document; null for a project document. */
  readonly issue: string | null;
  readonly id: string;
  readonly key: string;
}

export interface RunState {
  cursor: number | null;
  /** The cursor of the last write-mode run, or null before the first. */
  writeCursor: number | null;
  whole: boolean;
  finished: string[];
  readonly verdicts: Map<string, VerdictCode>;
  held: HeldWrite[];
  waiting: WaitingItem[];
  refused: RefusedOccurrence[];
  /** The sessions that ran write mode or the settle command. */
  sessions: string[];
}

export function emptyState(): RunState {
  return {
    cursor: null,
    writeCursor: null,
    whole: false,
    finished: [],
    verdicts: new Map(),
    held: [],
    waiting: [],
    refused: [],
    sessions: [],
  };
}

/** The largest state the tool writes: 900 KiB, under Dispatch's 1 MiB cap on a document. */
export const STATE_BOUND_BYTES = 900 * 1024;

/** The most a verdict costs the state as JSON, which a report's projection multiplies. */
export const VERDICT_BYTES = 48;

export class StateTooLarge extends Error {
  override readonly name = "StateTooLarge";

  constructor(
    readonly bytes: number,
    readonly verdicts: number,
    readonly held: number,
    readonly waiting: number,
    readonly refused: number,
    readonly projects: number,
    readonly sessions: number
  ) {
    super(
      `state-too-large bytes=${bytes} bound=${STATE_BOUND_BYTES} verdicts=${verdicts} held=${held} waiting=${waiting} refused=${refused} projects=${projects} sessions=${sessions}`
    );
  }
}

/** A project key, as the state, the flags and the seed take one. */
export const PROJECT_KEY = /^[A-Z][A-Z0-9]*$/;
/** An item's, an issue's or a session's id, as the state keeps one. */
const ITEM_ID = /^[A-Za-z0-9-]{1,64}$/;
const CODE = z.enum(Object.values(VERDICT_CODES) as [VerdictCode, ...VerdictCode[]]);

const Stored = z.strictObject({
  v: z.literal(1),
  cursor: z.int().min(0).nullable(),
  write_cursor: z.int().min(0).nullable(),
  whole: z.boolean(),
  finished: z.array(z.string().regex(PROJECT_KEY)),
  verdicts: z.record(z.string().regex(VERDICT_KEY), CODE),
  held: z.array(
    z.strictObject({
      p: z.string().regex(PROJECT_KEY),
      i: z.string().regex(ITEM_ID).nullable(),
      id: z.string().regex(ITEM_ID),
      keys: z.array(z.string().regex(VERDICT_KEY)),
      r: z.boolean(),
    })
  ),
  waiting: z.array(
    z.strictObject({
      p: z.string().regex(PROJECT_KEY),
      i: z.string().regex(ITEM_ID).nullable(),
      id: z.string().regex(ITEM_ID),
      g: z.custom<Gate>((value) => typeof value === "string" && GATE.test(value)),
    })
  ),
  refused: z.array(
    z.strictObject({
      p: z.string().regex(PROJECT_KEY),
      i: z.string().regex(ITEM_ID).nullable(),
      id: z.string().regex(ITEM_ID),
      k: z.string().regex(VERDICT_KEY),
    })
  ),
  sessions: z.array(z.string().regex(ITEM_ID)),
});

const FENCE = /```json\n([\s\S]*?)\n```/;

/** The state as the document's markdown, whatever its size. */
function stateMarkdown(state: RunState): string {
  const stored: z.input<typeof Stored> = {
    v: 1,
    cursor: state.cursor,
    write_cursor: state.writeCursor,
    whole: state.whole,
    finished: state.finished,
    verdicts: Object.fromEntries(state.verdicts),
    held: state.held.map((write) => ({
      p: write.project,
      i: write.issue,
      id: write.id,
      keys: [...write.keys],
      r: write.retry,
    })),
    waiting: state.waiting.map((item) => ({
      p: item.project,
      i: item.issue,
      id: item.id,
      g: item.gate,
    })),
    refused: state.refused.map((occurrence) => ({
      p: occurrence.project,
      i: occurrence.issue,
      id: occurrence.id,
      k: occurrence.key,
    })),
    sessions: state.sessions,
  };
  return `\`\`\`json\n${JSON.stringify(stored)}\n\`\`\`\n`;
}

/** How many bytes the state takes as the document's markdown. */
export function stateBytes(state: RunState): number {
  return Buffer.byteLength(stateMarkdown(state), "utf8");
}

/** The state as the document's markdown: one fenced `json` block, refused past the bound. */
export function serializeState(state: RunState): string {
  const markdown = stateMarkdown(state);
  const bytes = Buffer.byteLength(markdown, "utf8");
  if (bytes > STATE_BOUND_BYTES) {
    throw new StateTooLarge(
      bytes,
      state.verdicts.size,
      state.held.length,
      state.waiting.length,
      state.refused.length,
      state.finished.length,
      state.sessions.length
    );
  }
  return markdown;
}

/** The state a document's markdown holds. A document that holds no state block, or one that is
 *  not the state's shape, is refused rather than taken for an empty state. */
export function parseState(markdown: string): RunState {
  const block = FENCE.exec(markdown)?.[1];
  if (block === undefined) throw new Error("the state document holds no json block");
  const stored = Stored.parse(JSON.parse(block));
  return {
    cursor: stored.cursor,
    writeCursor: stored.write_cursor,
    whole: stored.whole,
    finished: stored.finished,
    verdicts: new Map(Object.entries(stored.verdicts)),
    held: stored.held.map((write) => ({
      project: write.p,
      issue: write.i,
      id: write.id,
      keys: write.keys,
      retry: write.r,
    })),
    waiting: stored.waiting.map((item) => ({
      project: item.p,
      issue: item.i,
      id: item.id,
      gate: item.g,
    })),
    refused: stored.refused.map((occurrence) => ({
      project: occurrence.p,
      issue: occurrence.i,
      id: occurrence.id,
      key: occurrence.k,
    })),
    sessions: stored.sessions,
  };
}

/** Where the state is kept: a project and a document slug. */
export interface StateDocument {
  readonly project: string;
  readonly slug: string;
}

const DOCUMENT_REF = /^dispatch:\/\/([A-Z][A-Z0-9]*)\/artifact\/([a-z0-9][a-z0-9-]*)$/;

/** `dispatch://<P>/artifact/<slug>`, or undefined for anything else. */
export function parseStateDocument(ref: string): StateDocument | undefined {
  const parsed = DOCUMENT_REF.exec(ref);
  if (parsed === null) return undefined;
  const [, project, slug] = parsed;
  return project === undefined || slug === undefined ? undefined : { project, slug };
}

/** The name the state document is uploaded under, from its slug: `rewrite-state-md` is
 *  `rewrite-state.md`, as Dispatch makes a slug of a name. */
export function stateDocumentName(slug: string): string {
  return slug.endsWith("-md") ? `${slug.slice(0, -3)}.md` : slug;
}

/** The state the document holds and the token it was read at; an absent document is an empty
 *  state with no token. */
export async function readStateDocument(
  reads: Pick<DispatchReads, "stateText">,
  document: StateDocument
): Promise<{ state: RunState; token: string | null }> {
  const answer = await reads.stateText(document.project, document.slug);
  if (answer === null) return { state: emptyState(), token: null };
  const text = Text.parse(answer);
  return { state: parseState(text.markdown), token: text.token };
}

/** A state write Dispatch refused: another run wrote it since this one read it, or worse. */
export class StateWriteError extends Error {
  override readonly name = "StateWriteError";

  constructor(readonly outcome: Extract<WriteOutcome, { ok: false }>) {
    super(`state write refused: ${outcome.status} ${outcome.code}`);
  }
}

/** Writes the state over the token it was read at, and answers the token it stands at now. */
export async function writeStateDocument(
  io: { reads: Pick<DispatchReads, "stateText">; writes: Pick<DispatchWrites, "uploadState"> },
  document: StateDocument,
  state: RunState,
  token: string | null
): Promise<string | null> {
  const content = serializeState(state);
  const outcome = await io.writes.uploadState(
    document.project,
    stateDocumentName(document.slug),
    content,
    token
  );
  if (!outcome.ok) throw new StateWriteError(outcome);
  return (await readStateDocument(io.reads, document)).token;
}

/** The first event of the log after `after`, read one at a time. */
async function firstAfter(
  reads: Pick<DispatchReads, "eventLog">,
  after: number
): Promise<number | undefined> {
  return z.array(Event).parse(await reads.eventLog(after, 1))[0]?.id;
}

/**
 * The newest id of the event log, which answers only `after` and `limit`, oldest first: each read
 * of one event after `x` answers the first id above `x`. The search doubles its step from the
 * newest id it knows until a read answers nothing, then halves the gap in which the newest id must
 * stand. About 41 reads over a million ids; 0 for an empty log.
 */
export async function newestEventId(reads: Pick<DispatchReads, "eventLog">): Promise<number> {
  const first = await firstAfter(reads, 0);
  if (first === undefined) return 0;
  let known = first;
  let step = 1;
  let above: number;
  while (true) {
    const probe = known + step;
    const found = await firstAfter(reads, probe);
    if (found === undefined) {
      above = probe;
      break;
    }
    known = found;
    step *= 2;
  }
  // No id stands above `above`, and `known` is one. The newest is in [known, above].
  while (above > known) {
    const middle = known + Math.floor((above - known) / 2);
    const found = await firstAfter(reads, middle);
    if (found === undefined) above = middle;
    else known = found;
  }
  return known;
}

/** The page size of the log's reads after a cursor. */
const LOG_PAGE = 1000;

/** Each page of the log after `cursor`, oldest first, given to `visit` as it is read, so that no
 *  caller holds more of an event than it needs. */
export async function readLog(
  reads: Pick<DispatchReads, "eventLog">,
  cursor: number,
  visit: (page: readonly Event[]) => void
): Promise<void> {
  let after = cursor;
  while (true) {
    const page = z.array(Event).parse(await reads.eventLog(after, LOG_PAGE));
    visit(page);
    const last = page.at(-1);
    if (page.length < LOG_PAGE || last === undefined || last.id <= after) return;
    after = last.id;
  }
}

/** What a run keeps of an event after its cursor: what names the event's item. */
export type LoggedEvent = Pick<Event, "id" | "project" | "issue_key" | "artifact_id">;

/** Every event of the log after `cursor`, oldest first, as `LoggedEvent`; `visit` also sees
 *  each whole page, for a reader that needs more of an event. */
export async function eventsAfter(
  reads: Pick<DispatchReads, "eventLog">,
  cursor: number,
  visit?: (page: readonly Event[]) => void
): Promise<LoggedEvent[]> {
  const events: LoggedEvent[] = [];
  await readLog(reads, cursor, (page) => {
    visit?.(page);
    for (const { id, project, issue_key, artifact_id } of page) {
      events.push({ id, project, issue_key, artifact_id });
    }
  });
  return events;
}
