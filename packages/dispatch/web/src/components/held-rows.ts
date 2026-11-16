/**
 * The reader's place in a list that changes under them (The Spec: content that others add does
 * not move a reader's place while it stays on screen). A list the reader is reading is held: a
 * change that arrives from elsewhere updates what its rows say, and adds, removes or moves none
 * of them until the reader asks for it. The reader's own changes are not held. These are the two
 * rules every held list keeps - the table's page (`useDataTable`'s `hold`) and the Board's
 * cards (`features/project/board-hold.ts`) - so both count and merge the same way.
 */

/**
 * How many of a list's rows a change would change: the rows it would add, the rows it would take
 * away, and the rows it would move - those left out of the longest run of rows that keep their
 * order, so a row moved to the top counts once rather than moving every row it passes.
 */
export function changedRows(held: readonly string[], live: readonly string[]): number {
  const liveAt = new Map(live.map((id, index) => [id, index]));
  const heldSet = new Set(held);
  const added = live.filter((id) => !heldSet.has(id)).length;
  const kept = held.flatMap((id) => {
    const at = liveAt.get(id);
    return at === undefined ? [] : [at];
  });
  return added + (held.length - kept.length) + (kept.length - longestRising(kept));
}

/** The length of the longest strictly rising run in `values`, not necessarily adjacent. */
function longestRising(values: readonly number[]): number {
  const tails: number[] = [];
  for (const value of values) {
    let low = 0;
    let high = tails.length;
    while (low < high) {
      const middle = (low + high) >> 1;
      if ((tails[middle] ?? 0) < value) low = middle + 1;
      else high = middle;
    }
    tails[low] = value;
  }
  return tails.length;
}

/**
 * The held list with the reader's own changes taken from the live one: every entry the reader
 * did not change (`own` names those they did) stays where the held list has it, present or not
 * as it was there; each of the reader's own stands as the live list has it, before the first
 * entry after it there that the held list also shows, and goes where the live list dropped it.
 * A list with groups (`isGroup`) brings an own entry's heading with it where the held list has
 * none, and loses a heading the live list no longer has once nothing stands under it.
 */
export function mergeHeld<T>(
  held: readonly T[],
  live: readonly T[],
  own: ReadonlySet<string>,
  idOf: (entry: T) => string,
  isGroup: (entry: T) => boolean = () => false
): T[] {
  const isOwn = (entry: T) => !isGroup(entry) && own.has(idOf(entry));
  const shown = held.filter((entry) => !isOwn(entry));
  if (own.size === 0) return shown;
  let heading: T | undefined;
  live.forEach((entry, index) => {
    if (isGroup(entry)) {
      heading = entry;
      return;
    }
    if (!own.has(idOf(entry))) return;
    const at = new Map(shown.map((one, place) => [idOf(one), place]));
    let place = shown.length;
    for (const next of live.slice(index + 1)) {
      const found = at.get(idOf(next));
      if (found !== undefined) {
        place = found;
        break;
      }
    }
    const entries = heading !== undefined && !at.has(idOf(heading)) ? [heading, entry] : [entry];
    shown.splice(place, 0, ...entries);
  });
  const liveIds = new Set(live.map(idOf));
  return shown.filter((entry, index) => {
    if (!isGroup(entry) || liveIds.has(idOf(entry))) return true;
    const next = shown[index + 1];
    return next !== undefined && !isGroup(next);
  });
}
