import { inlineLinks } from "./party-references";
import { readTextReferences } from "./text-references";

/** Where one reference stands in a text, `[start, end)` in UTF-16 offsets. */
interface Extent {
  readonly start: number;
  readonly end: number;
}

/** The whitespace a cut that moved back drops from the end of its head, line breaks included. */
const TRAILING_SPACE = /[ \t\r\n]+$/u;

/**
 * `text` cut to at most `limit` characters, `ellipsis` included, at a place no reference crosses:
 * the twin of the server's `text.HeadRefs` (`packages/envoy/internal/dispatch/text/runes.go`), for a
 * head the dashboard cuts itself and shows as its characters - a picker row's words, which the
 * list draws as written until the reader's engine answers and wherever the server sent no tree of
 * them (`useRefPicker.ts`). A cut by length alone would leave half of a reference there: `CORE-12`
 * of `CORE-1234`, or `[@Planner](dispatch://agent/` drawn raw. A text at or under the limit is
 * returned unchanged; one over it is cut to its first `limit` characters less the ellipsis's, and
 * the ellipsis is appended. A character is a code point, as the server counts runes.
 *
 * A reference is what the shared reader finds (`readTextReferences`) - a `dispatch://` address, a
 * dashboard URL, a bare issue key and a `#id` - widened over the inline link whose address it is,
 * a picture's `!` included (`inlineLinks`). Where one starts before the cut and ends after it, the
 * cut moves back to its start: the link's `[` where a link wraps it. The cut moves back the same
 * way where the head, with the ellipsis after it, would read a reference the text does not write:
 * `CORE-12` of `CORE-12x4`, which is no key, or an address the ellipsis joins. A cut that moved
 * drops the whitespace before it. A cut never empties the head: where moving back would leave
 * nothing before the ellipsis - a text that opens with a reference longer than the bound - the
 * cut by length stands, since a head of nothing tells a reader less than half a reference does.
 *
 * The whole text is read, once, and the head again after each move; the texts it cuts are a row's
 * worth - a search hit's snippet and a block's excerpt, both bounded by the server - so it needs
 * none of the window the server reads a paragraph of any length through.
 */
export function headRefs(text: string, limit: number, ellipsis: string): string {
  const characters = Array.from(text);
  if (characters.length <= limit) return text;
  const keep = Math.max(limit - Array.from(ellipsis).length, 0);
  const cut = characters.slice(0, keep).join("").length;
  return `${text.slice(0, headEnd(text, cut, ellipsis))}${ellipsis}`;
}

/** Where the head of `text` cut at `cut` ends (`headRefs`): `cut` moved back past every reference
 *  that crosses it and every one the head would read that the text does not write, less the
 *  whitespace before it; or `cut` itself where that would leave nothing. Each move is back, so it
 *  ends. */
function headEnd(text: string, cut: number, ellipsis: string): number {
  const written = referenceExtents(text);
  let at = cut;
  for (;;) {
    let back = at;
    for (const reference of written) {
      if (reference.start < at && at < reference.end) back = Math.min(back, reference.start);
    }
    for (const reference of referenceExtents(`${text.slice(0, at)}${ellipsis}`)) {
      const writes = written.some(
        (one) => one.start === reference.start && one.end === reference.end
      );
      if (reference.start < at && !writes) back = Math.min(back, reference.start);
    }
    if (back === at) return at === 0 ? cut : at;
    at = text.slice(0, back).replace(TRAILING_SPACE, "").length;
  }
}

/** Every reference of `text` as a reader sees it, none of them resolved, since a cut that splits
 *  a reference changes what the text says whatever the reference names: each span the shared
 *  reader finds, widened over the inline link whose address it is. */
function referenceExtents(text: string): Extent[] {
  const links = new Map(inlineLinks(text).map((link) => [link.addressStart, link]));
  return readTextReferences(text).references.map(({ end, start }): Extent => {
    const link = links.get(start);
    // The address of a link ends where the link's own reading ends it: at the `)` that closes
    // the link, at the space before its title, or at the `>` around it.
    if (link === undefined || link.addressEnd > end) return { end, start };
    return { end: link.close, start: link.picture ? link.open - 1 : link.open };
  });
}
