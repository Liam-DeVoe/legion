import { z } from "zod";
import { eventMoved } from "./authorship.ts";
import { type DispatchReads, Event } from "./dispatch-api.ts";
import type { Clock } from "./request-log.ts";
import { newestEventId, readLog } from "./state.ts";

/**
 * The pace of write mode and of the settle command. A named version and a title edit each give
 * one card to each session subscribed to the owner's topic, and a card starts a turn on an idle
 * session. The tool cannot see who those sessions are: Dispatch serves an owner's subscribers only
 * to a credential that holds the `person` scope, and write mode runs with the shared token, which
 * holds none. So the pace bounds what the tool controls, its writes:
 *
 * - at most one write every 10 seconds overall, which the run waits out;
 * - at most one write per owner (an issue, for its title and its documents, or a project
 *   document) in any 15 minutes, so no session subscribed to one owner gets two cards from the
 *   tool in that time;
 * - at most one write per project in any minute, so no session subscribed to a whole project gets
 *   a wave of cards.
 *
 * It counts the writes of every run and settle, whichever process made them (spec version 43):
 * the state keeps no time, so before each write the tool reads, from Dispatch's events, the title
 * edits and named versions of the sessions its state lists (`PaceLog`). A write that the owner's
 * or the project's bound holds back waits for a later run, in the state's held-back writes; the
 * settle command writes nothing and prints when it may write.
 */

export const WRITE_GAP_MS = 10_000;
export const OWNER_WINDOW_MS = 15 * 60_000;
export const PROJECT_WINDOW_MS = 60_000;

/** What the pace knows of a write: its owner (an issue's key, or a project document's id) and
 *  its project. */
export interface PacedWrite {
  readonly owner: string;
  readonly project: string;
}

export class Pace {
  readonly #ownerWrote = new Map<string, number>();
  readonly #projectWrote = new Map<string, number>();
  #lastWrite: number | null = null;

  constructor(private readonly clock: Clock) {}

  /** When the owner's and the project's bounds let the write be made. */
  allowedAt(write: PacedWrite): number {
    const owner = this.#ownerWrote.get(write.owner) ?? Number.NEGATIVE_INFINITY;
    const project = this.#projectWrote.get(write.project) ?? Number.NEGATIVE_INFINITY;
    return Math.max(owner + OWNER_WINDOW_MS, project + PROJECT_WINDOW_MS);
  }

  /** Whether a write keeps the owner's and the project's bounds now. */
  admits(write: PacedWrite): boolean {
    return this.clock.now() >= this.allowedAt(write);
  }

  /** Waits until a write keeps the 10-second bound. */
  async gap(): Promise<void> {
    if (this.#lastWrite === null) return;
    const wait = this.#lastWrite + WRITE_GAP_MS - this.clock.now();
    if (wait > 0) await this.clock.sleep(wait);
  }

  /** Records a write made now. */
  wrote(write: PacedWrite): void {
    this.saw(write, this.clock.now());
  }

  /** Records a write made at `at`, by this process or another. */
  saw(write: PacedWrite, at: number): void {
    this.#lastWrite = Math.max(this.#lastWrite ?? at, at);
    this.#ownerWrote.set(write.owner, Math.max(this.#ownerWrote.get(write.owner) ?? at, at));
    this.#projectWrote.set(
      write.project,
      Math.max(this.#projectWrote.get(write.project) ?? at, at)
    );
  }
}

/** A version's summary: the tool's edits are named versions, its state saves unnamed ones. */
const NamedVersion = z.looseObject({
  version: z.looseObject({ summary: z.string().nullable().optional() }).nullable().optional(),
});

/**
 * The tool's writes as Dispatch's events record them, read into a `Pace`: each title edit
 * (`issue.updated` that changed the title) and each named version (`artifact.version` with a
 * summary) of a session the state lists. The first `refresh` reads every event of the last
 * quarter-hour, whatever any cursor of the state says: the log answers only `after` and `limit`,
 * oldest first, so it steps back from the newest id by a gap that doubles until the event after
 * the gap is older than the window, and reads forward from there. Each later `refresh` reads the
 * log after the newest event seen, so each write sees what any process wrote since. An issue's
 * own events, which a run reads once for its status moves, show its older writes too (`saw`).
 */
export class PaceLog {
  #after: number | undefined;

  constructor(
    private readonly reads: Pick<DispatchReads, "eventLog">,
    private readonly pace: Pace,
    private readonly sessions: ReadonlySet<string>,
    private readonly clock: Clock
  ) {}

  /** Takes the tool's writes among `events` into the pace; a page of the log also moves the
   *  point after which `refresh` reads. */
  saw(events: readonly Event[], fromLog = false): void {
    for (const event of events) {
      if (fromLog) this.#after = Math.max(this.#after ?? 0, event.id);
      const actor = event.actor;
      if (actor.kind !== "session" || actor.id === undefined || !this.sessions.has(actor.id)) {
        continue;
      }
      const named =
        event.type === "artifact.version" &&
        typeof NamedVersion.safeParse(event.payload).data?.version?.summary === "string";
      if (!named && !(event.type === "issue.updated" && eventMoved(event, "title"))) continue;
      const owner = event.issue_key ?? event.artifact_id;
      if (owner === null) continue;
      this.pace.saw({ owner, project: event.project }, Date.parse(event.created_at));
    }
  }

  /** An id after which the log holds every event of the last quarter-hour. */
  async #windowStart(): Promise<number> {
    const since = this.clock.now() - OWNER_WINDOW_MS;
    const newest = await newestEventId(this.reads);
    for (let back = 1_000; ; back *= 2) {
      const from = Math.max(0, newest - back);
      if (from === 0) return 0;
      const [first] = z.array(Event).parse(await this.reads.eventLog(from, 1));
      if (first === undefined || Date.parse(first.created_at) < since) return from;
    }
  }

  /** Reads the log after the newest event seen, or the whole window the first time. */
  async refresh(): Promise<void> {
    this.#after ??= await this.#windowStart();
    await readLog(this.reads, this.#after, (page) => this.saw(page, true));
  }
}

/**
 * The run time the three bounds give for these writes: when the last one can start, writing as
 * soon as the bounds allow and taking first the owner with the most writes left. Report mode
 * prints it for the items that wait for the judge, one write each.
 */
export function runTimeMs(writes: readonly PacedWrite[]): number {
  const owners = new Map<string, { project: string; left: number; free: number }>();
  for (const write of writes) {
    const owner = owners.get(write.owner) ?? { project: write.project, left: 0, free: 0 };
    owner.left += 1;
    owners.set(write.owner, owner);
  }
  const projectFree = new Map<string, number>();
  let now = 0;
  let last = 0;
  for (let remaining = writes.length; remaining > 0; ) {
    let pick: { project: string; left: number; free: number } | undefined;
    let soonest = Number.POSITIVE_INFINITY;
    for (const owner of owners.values()) {
      if (owner.left === 0) continue;
      const ready = Math.max(owner.free, projectFree.get(owner.project) ?? 0);
      if (ready > now) soonest = Math.min(soonest, ready);
      else if (pick === undefined || owner.left > pick.left) pick = owner;
    }
    if (pick === undefined) {
      now = soonest;
      continue;
    }
    last = now;
    pick.left -= 1;
    remaining -= 1;
    pick.free = now + OWNER_WINDOW_MS;
    projectFree.set(pick.project, now + PROJECT_WINDOW_MS);
    now += WRITE_GAP_MS;
  }
  return last;
}
