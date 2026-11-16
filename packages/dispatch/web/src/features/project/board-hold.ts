import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { changedRows, mergeHeld } from "../../components/held-rows";
import type { HeldChanges } from "../../components/ShowChanges";
import { issuePriority } from "../issue/issue-priority";
import type { PriorityLane } from "./board-model";
import { type BoardBy, columnOf } from "./IssueBoard";
import type { Item } from "./issues-table-model";

/** Where the Board draws a card: its column, and its lane where the Board has lanes. */
export interface CardPlace {
  readonly column: string;
  /** Undefined on a Board with no lanes, where a card's priority places it nowhere. */
  readonly lane: PriorityLane | undefined;
}

/** One card as a held Board keeps it: its issue, where it stands, and the row as it was. */
interface Card {
  readonly key: string;
  readonly place: CardPlace;
  readonly item: Item;
}

interface HeldBoard {
  readonly key: string;
  /** Whether the caller held the Board when the cards were taken. */
  readonly holding: boolean;
  readonly cards: readonly Card[];
}

/** What the Board draws while it holds the reader's place. */
export interface BoardHold {
  /** The rows the Board draws, in its order, each as it stands now. */
  readonly rows: readonly Item[];
  /** Where each drawn row stands while the Board holds cards back; undefined while it holds
   *  none, when every row stands where its fields put it. */
  readonly placeOf: ((item: Item) => CardPlace) | undefined;
  /** The cards a change from elsewhere would add, remove or move, and what draws them. */
  readonly held: HeldChanges;
}

const cardKey = (card: Card) => card.key;
const placeName = ({ column, lane }: CardPlace) => `${String(lane)}|${column}`;
const sameCards = (a: readonly Card[], b: readonly Card[]) =>
  a.length === b.length &&
  a.every(
    (card, index) =>
      card.key === b[index]?.key && placeName(card.place) === placeName(b[index].place)
  );

/**
 * How many cards a change would change, by the pager's rule (`changedRows`): the cards it would
 * add, the cards it would take away, the cards it would move to another column or lane, and
 * within each column the cards it would move there, those left out of the longest run that
 * keeps its order.
 */
export function changedCards(held: readonly Card[], live: readonly Card[]): number {
  const now = new Map(live.map((card) => [card.key, card]));
  const heldKeys = new Set(held.map(cardKey));
  const added = live.filter((card) => !heldKeys.has(card.key)).length;
  let removed = 0;
  let moved = 0;
  const staying = new Map<string, string[]>();
  for (const card of held) {
    const current = now.get(card.key);
    const place = placeName(card.place);
    if (current === undefined) removed += 1;
    else if (placeName(current.place) !== place) moved += 1;
    else {
      const keys = staying.get(place);
      if (keys === undefined) staying.set(place, [card.key]);
      else keys.push(card.key);
    }
  }
  const stays = new Set([...staying.values()].flat());
  let reordered = 0;
  for (const [place, keys] of staying)
    reordered += changedRows(
      keys,
      live.filter((card) => stays.has(card.key) && placeName(card.place) === place).map(cardKey)
    );
  return added + removed + moved + reordered;
}

/**
 * The Board's hold of the reader's place: while the reader stays (`holdKey`: the view, its
 * filters, its grouping, the layout and the lanes), a change from elsewhere updates the fields
 * of the cards on screen and adds, removes or moves no card; `held` counts the cards it would
 * change and draws them on the reader's press. The reader's own changes (`own`) are drawn at
 * once. A Board that drew no card, or one drawn while the caller did not hold, is no place to
 * hold: the cards that arrive as it starts holding are drawn. The rule is the table page's
 * (`useDataTable`'s `hold`), so the two count and merge alike.
 */
export function useHeldBoard({
  by,
  holdKey,
  holding,
  lanes,
  own,
  rows,
}: {
  by: BoardBy;
  holdKey: string;
  holding: boolean;
  lanes: boolean;
  own: ReadonlySet<string>;
  /** The view's rows, in its sort, as they stand now. */
  rows: readonly Item[];
}): BoardHold {
  const placeOf = (item: Item): CardPlace => ({
    column: columnOf(item, by),
    lane: lanes ? issuePriority(item.issue.priority) : undefined,
  });
  const live: Card[] = rows.map((item) => ({ key: item.issue.key, place: placeOf(item), item }));
  const [held, setHeld] = useState<HeldBoard>();
  const heldCards =
    holding && held?.holding === true && held.key === holdKey && held.cards.length > 0
      ? held.cards
      : undefined;
  const shown = heldCards === undefined ? live : mergeHeld(heldCards, live, own, cardKey);
  const count = heldCards === undefined ? 0 : changedCards(shown, live);
  if (count === 0) {
    if (
      held === undefined ||
      held.key !== holdKey ||
      held.holding !== holding ||
      !sameCards(held.cards, live)
    )
      setHeld({ key: holdKey, holding, cards: live });
  } else if (held !== undefined && !sameCards(held.cards, shown)) {
    // The reader's own changes join the place held, so they stay once their writes are past.
    setHeld({ ...held, cards: shown });
  }
  const latest = useRef<HeldBoard>({ key: holdKey, holding, cards: live });
  useLayoutEffect(() => {
    latest.current = { key: holdKey, holding, cards: live };
  });
  const show = useCallback(() => setHeld(latest.current), []);
  if (count === 0) return { rows, placeOf: undefined, held: { count, show } };
  const now = new Map(rows.map((item) => [item.issue.key, item]));
  const places = new Map(shown.map((card) => [card.key, card.place]));
  return {
    rows: shown.map((card) => now.get(card.key) ?? card.item),
    placeOf: (item) => places.get(item.issue.key) ?? placeOf(item),
    held: { count, show },
  };
}
