import type { Clock } from "./request-log.ts";

/**
 * The pace of write mode. A named version and a title edit each give one card to each session
 * subscribed to the owner's topic, and a card starts a turn on an idle session. The tool cannot
 * see who those sessions are: Dispatch serves an owner's subscribers only to a credential that
 * holds the `person` scope, and write mode runs with the shared token, which holds none. So the
 * pace bounds what the tool controls, its writes:
 *
 * - at most one write every 10 seconds overall, which the run waits out;
 * - at most one write per owner (an issue, for its title and its documents, or a project
 *   document) in any 15 minutes, so no session subscribed to one owner gets two cards from the
 *   tool in that time;
 * - at most one write per project in any minute, so no session subscribed to a whole project gets
 *   a wave of cards.
 *
 * A write that the owner's or the project's bound holds back waits for a later run, in the state's
 * held-back writes.
 */

export const WRITE_GAP_MS = 10_000;
export const OWNER_WINDOW_MS = 15 * 60_000;
export const PROJECT_WINDOW_MS = 60_000;

/** What the pace knows of a write: its owner's name and its project. */
export interface PacedWrite {
  readonly owner: string;
  readonly project: string;
}

export class Pace {
  readonly #ownerWrote = new Map<string, number>();
  readonly #projectWrote = new Map<string, number>();
  #lastWrite: number | null = null;

  constructor(private readonly clock: Clock) {}

  /** Whether a write keeps the owner's and the project's bounds now. */
  admits(write: PacedWrite): boolean {
    const now = this.clock.now();
    const owner = this.#ownerWrote.get(write.owner);
    const project = this.#projectWrote.get(write.project);
    return (
      (owner === undefined || now - owner >= OWNER_WINDOW_MS) &&
      (project === undefined || now - project >= PROJECT_WINDOW_MS)
    );
  }

  /** Waits until a write keeps the 10-second bound. */
  async gap(): Promise<void> {
    if (this.#lastWrite === null) return;
    const wait = this.#lastWrite + WRITE_GAP_MS - this.clock.now();
    if (wait > 0) await this.clock.sleep(wait);
  }

  /** Records a write made now. */
  wrote(write: PacedWrite): void {
    const now = this.clock.now();
    this.#lastWrite = now;
    this.#ownerWrote.set(write.owner, now);
    this.#projectWrote.set(write.project, now);
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
