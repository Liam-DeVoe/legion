/**
 * What a row of a table or list is, for the page's focus rescue (`ROW_SELECTOR`): an element HTML
 * or ARIA defines as an item of a list or a table - an `<li>`, a `<tr>`, an element of role `row`
 * or `listitem`, an element of role `article` inside one of role `feed` - or an element that
 * carries the shared marker below. A list drawn as `<li>`s and a table drawn as `<tr>`s are rows
 * with nothing added: every `<tr>` the shared tables draw (`DataTable`, `TableShell`, and the rows
 * their callers draw), header, spacer, group and empty lines included, the Inbox's and the
 * Answers page's rows, the Conversation's turns, and every other `<ul>`, `<ol>` and table a page
 * draws.
 *
 * The marker is for a row drawn as an element that says nothing of being one: the Agents page's
 * `<article data-agent-row>` and the Architecture page's `<article data-component-row>`, each the
 * element the page's keys move between, inside an `<li>` of its list, and the Board's cards. A
 * page's own row attribute (`data-agent-row`, `data-board-card`, ...) stays the page's, for its
 * own keys and tests; the marker says only that the element is a row.
 *
 * A table in a document's or a comment's prose is a table too, so its `<tr>`s are rows, which
 * harms nothing. In the live Proof editor it changes nothing: the editor's whole text is
 * content-editable, so the rescue passes over every element of it, a link in a cell included, as
 * one that takes typed text before it asks whether that element is in a row, and the focus a loss
 * there leaves is the editor's own, which sits in no prose row. In prose drawn read-only - a
 * comment, a closed issue's document, a past version - a link in a table's cell is no longer a
 * place a loss the reader did not cause lands: the focus goes to the box the table is drawn in, as
 * it does from any other row, only while the reader can see that box and with no scroll, so their
 * next key acts on nothing they did not pick.
 *
 * The rescue reads a row by this one rule (`inRow` in `focus-place.ts`): a loss the reader did
 * not cause lands in no row and in no control inside one, so a row this rule does not match is
 * one the rescue can hand the focus into.
 */
export const ROW_MARKER_ATTRIBUTE = "data-row-marker";

/** The marker as props for a row's element to spread where the row is drawn, on an element that
 *  is no `<li>` or `<tr>`: `<article {...ROW_MARKER}>`. */
export const ROW_MARKER = { [ROW_MARKER_ATTRIBUTE]: "" } as const;

/** Every element that is a row: an item of a list or a table by HTML or ARIA, or one that carries
 *  the marker. */
export const ROW_SELECTOR = [
  "li",
  "tr",
  '[role="row"]',
  '[role="listitem"]',
  '[role="feed"] [role="article"]',
  `[${ROW_MARKER_ATTRIBUTE}]`,
].join(", ");
