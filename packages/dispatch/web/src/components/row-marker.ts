/**
 * The one marker every row of a table or list carries, set on the row's element where it is
 * drawn: every `<tr>` the shared tables draw (`DataTable`, `TableShell`, and the rows their
 * callers draw), header, spacer, group and empty lines included, and each list's own row - the
 * Inbox's, the Agents page's, the Answers page's, the Board's cards, the Architecture page's. A
 * page's own row attribute (`data-inbox-row`, `data-agent-row`, `data-board-card`, ...) stays
 * the page's, for its own keys and tests; this marker says only that the element is a row. A
 * table in a document's or a comment's prose, whose `<tr>`s the Proof editor's schema draws, is
 * no list the reader picks rows of, and its rows carry no marker.
 *
 * The page's focus rescue reads a row by this marker alone (`inRow` in `focus-place.ts`): a loss
 * the reader did not cause lands in no row and in no control inside one, so a row drawn without
 * the marker is one the rescue can hand the focus into.
 */
export const ROW_MARKER_ATTRIBUTE = "data-row-marker";

/** The marker as props for a row's element to spread where the row is drawn:
 *  `<tr {...ROW_MARKER}>`. */
export const ROW_MARKER = { [ROW_MARKER_ATTRIBUTE]: "" } as const;

/** Every element that carries the marker: what a row is. */
export const ROW_MARKER_SELECTOR = `[${ROW_MARKER_ATTRIBUTE}]`;
