import type { ReactNode } from "react";

import { Chip } from "./Chip";
import type { ControlSize } from "./control-names";

/** One narrowing that is on, as a page's control row draws it. */
export interface FilterChip {
  /** Tells two chips apart; the label, where a page never draws one label twice. */
  readonly key?: string;
  /** The chip's own text: the facet and its value as the page writes them - `Label: docs`,
   *  `Author alice`, `Needs you`. */
  readonly label: string;
  /** Takes this narrowing off, and no other. */
  readonly remove: () => void;
}

/**
 * The chips of the narrowings that are on, for a `ControlRow`, on every page that has a row: one
 * removable chip per narrowing, whose press takes that narrowing off. Each is named for what the
 * press does, `Remove <label> filter`, the same words on every page, so a screen reader hears
 * one name for one act and a browser row finds a chip the same way everywhere.
 *
 * A chip here stands for a narrowing that is on, so it takes the selected fill; a resting chip
 * would read as a narrowing the reader could still switch on. Its text is cut where the row is
 * too narrow, with the whole of it as its `title`, since a search can be 1,000 characters long.
 *
 * Answers undefined for no narrowing, which is what the row's `chips` takes for no chips.
 */
export function filterChips(
  chips: readonly FilterChip[],
  size?: ControlSize
): ReactNode[] | undefined {
  if (chips.length === 0) return undefined;
  return chips.map(({ key, label, remove }) => (
    <Chip
      aria-label={`Remove ${label} filter`}
      key={key ?? label}
      onClick={remove}
      removable
      selected
      size={size}
      title={label}
    >
      {label}
    </Chip>
  ));
}
