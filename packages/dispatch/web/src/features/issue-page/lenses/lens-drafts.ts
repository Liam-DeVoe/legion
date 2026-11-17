import { sentenceCase } from "../page-words";

/**
 * What a row of a lens, a finding, a risk or a constraint carries beyond a `TreeNode`'s own
 * fields, and what a statement a constraint names carries (dispatch://INFRA-4065 R1 to R5).
 * Every fact comes from part T's tree read; nothing here sends a request.
 */

/** One chip that names another node of the tree: a lens, a check or a constraint. */
export interface LensChip {
  /** The target's `dispatch://` address. */
  readonly address: string;
  /** The target's words, cut by `chipWords`. */
  readonly words: string;
  /** The target's id in the tree read, only where a step drafted it: the link's `?node=`. */
  readonly id?: string;
  /** The target's state and visibility, as the tree read gives them (part X). */
  readonly state?: string;
  readonly visibility?: string;
}

/** One lens of the tree, as the chain of a focused lens draws it. */
export interface LensPlace {
  readonly kindWords: string;
  /** The first line of the row it stands under. */
  readonly onLine?: string;
  /** Its id in the tree read, where a step drafted it. */
  readonly id?: string;
}

/** The lens facts of one node. A list with nothing in it draws no line. */
export interface LensFacts {
  /** A lens: its kind's words, as the `lens_kind` node holds them. */
  readonly kind?: string;
  readonly triggeredBy: readonly LensChip[];
  readonly triggered: readonly LensChip[];
  readonly earlyWarning?: string;
  readonly prevention?: string;
  readonly preventedBy: readonly LensChip[];
  readonly constrainedBy: readonly LensChip[];
  /** A standard's reference. */
  readonly reference?: string;
  /** Where a measurement was taken. */
  readonly takenWhere?: string;
  /** A lens: every lens of the tree, by its address. */
  readonly lenses?: ReadonlyMap<string, LensPlace>;
}

/** A kind's words with their first letter in capitals, as a lens's tag and a chip that names a
 *  lens draw them (R1, R2). A chain entry and a menu item draw the words as the node holds them. */
export function kindWords(words: string): string {
  return sentenceCase(words);
}
