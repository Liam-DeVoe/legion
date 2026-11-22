import { useRef } from "react";

/** One read of a failed-read line, as `useLineRefusals` counts it in one render. */
export interface LineRead {
  /** How many times its query has been refused (TanStack Query's `errorUpdateCount`). */
  readonly errorUpdateCount: number;
  /** Whether it is failing: while a read of a line is failing, it may stand the line, and its
   *  refusals count. */
  readonly failing: boolean;
  /** Its key, hashed (`hashKey`): a read under another key is a query of its own. */
  readonly hash: string;
}

/** What `useLineRefusals` says of one line at a draw. */
export interface LineRefusals {
  /** How many refusals of its reads the line has counted: it never falls. */
  readonly refusals: number;
  /** Whether the line speaks: its `FailedReadLine`'s `speaks`, by the hook's rule. */
  readonly speaks: boolean;
  /** Whether the line stands. */
  readonly stands: boolean;
}

/** What `useLineRefusals` keeps for as long as the component that calls it lives. */
interface Kept {
  /** Each line as it was at the last draw, in the lines' order. */
  lines: readonly LineRefusals[];
  /** The `errorUpdateCount` last seen under each key the hook has read, by the key's hash. */
  readonly seen: Map<string, number>;
}

/**
 * The refusals each failed-read line has counted, which key the line's alert (`FailedReadLine`'s
 * `failure`), whether it stands, and whether it speaks, so a screen reader says the failure again
 * exactly for a refusal new to the line.
 *
 * The rule: the hook keeps, for as long as the component that calls it lives, the
 * `errorUpdateCount` last seen under each key it has read, written for every read at every draw,
 * failing or not. At each draw, each failing read raises its line's count by what its
 * `errorUpdateCount` rose by since the count last seen under its key, or by the whole of it where
 * no count was seen under that key or the count is below the one seen (the query was reset or
 * made afresh, which drops its answer too). An answer leaves `errorUpdateCount` as it is: TanStack
 * sets it to 0 only for a query made afresh or reset. So each refusal is counted once under its
 * key, and a line's count never falls. A line stands while a read of it is failing and either the
 * line stood at the last draw or a failing read holds a refusal under its key (`errorUpdateCount`
 * above 0). So a line does not stand for a read that is merely out with no refusal, it stays
 * through a run again, and it stands again, with its count unchanged, where a read is back on a
 * key whose refusal the line has already counted.
 *
 * A line speaks while it stands and either its count rose at this draw or it stood and spoke at
 * the last draw. A screen reader says an alert as it is drawn, so a line that stands again with
 * no new refusal draws without speaking: it said those refusals when it counted them. So a line
 * that begins standing with a new refusal speaks, one that stands again with none does not, and
 * one that stands quietly speaks from its next new refusal for as long as it stands.
 *
 * `lines` are the same lines, in the same order, on every render. The ref is written during
 * render with that render's state, so a second render of the same state counts nothing more.
 */
export function useLineRefusals(lines: readonly (readonly LineRead[])[]): readonly LineRefusals[] {
  const ref = useRef<Kept | undefined>(undefined);
  ref.current ??= { lines: [], seen: new Map() };
  const kept = ref.current;
  const counted = lines.map((reads, at): LineRefusals => {
    const was = kept.lines[at];
    let refusals = was?.refusals ?? 0;
    let failing = false;
    let refused = false;
    for (const read of reads) {
      if (!read.failing) continue;
      failing = true;
      refused ||= read.errorUpdateCount > 0;
      const seen = kept.seen.get(read.hash);
      refusals +=
        seen === undefined || read.errorUpdateCount < seen
          ? read.errorUpdateCount
          : read.errorUpdateCount - seen;
    }
    const stands = failing && (refused || (was?.stands ?? false));
    const speaks = stands && (refusals > (was?.refusals ?? 0) || (was?.speaks ?? false));
    return { refusals, speaks, stands };
  });
  // Every line is counted against the counts seen before this draw, so two lines that read the
  // same key each count its new refusals.
  for (const reads of lines) {
    for (const read of reads) kept.seen.set(read.hash, read.errorUpdateCount);
  }
  kept.lines = counted;
  return counted;
}
