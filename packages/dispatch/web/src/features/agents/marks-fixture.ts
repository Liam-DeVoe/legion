import type { Marks, MarkV2 } from "../../api/types";

/** What one mark holds beyond the object it is on: the fields a test names. */
export type MarkFields = Omit<MarkV2, "id" | "kind">;

/** One of the person's marks as Dispatch answers it: the fields named, on `id`, of `kind`. */
export function markOf(kind: MarkV2["kind"], id: string, fields: MarkFields = {}): MarkV2 {
  return { ...fields, id, kind };
}

/** The person's marks of one kind by the id each is on, as the page holds them (`Marks`): what
 *  `GET /api/v2/me/marks?kind=` answers, keyed, each mark carrying its own id and kind. */
export function marksOf(kind: MarkV2["kind"], fields: Readonly<Record<string, MarkFields>>): Marks {
  return Object.fromEntries(
    Object.entries(fields).map(([id, mark]) => [id, markOf(kind, id, mark)])
  );
}
