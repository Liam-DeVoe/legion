import { codeRanges } from "./code-ranges";
import { type InlineLink, inlineLinks } from "./party-references";
import { referenceSpans } from "./reference-spans";
import { referenceRouteFromHref } from "./routes";
import { readTextReferences } from "./text-references";

/** One reference a text writes, as a cut reads it (the server's `cutReference`): where a reader
 *  sees it, `[start, end)` in UTF-16 offsets - the whole inline link around it, where one is -
 *  and the characters it was read from, which tell two references read at one place apart. */
interface Extent {
  readonly start: number;
  readonly end: number;
  readonly written: string;
}

/** The whitespace a cut that moved back drops from the end of its head, line breaks included. */
const TRAILING_SPACE = /[ \t\r\n]+$/u;

/** The most characters past the bound a cut reads to keep a reference whole: the server's
 *  `longestWholeReferenceRunes` (`packages/envoy/internal/dispatch/text/runes.go`), so the two
 *  copies read the same window of a text and agree on a reference that runs past it. */
const LONGEST_WHOLE_REFERENCE = 1024;

/**
 * `text` cut to at most `limit` characters, `ellipsis` included, at a place no reference crosses:
 * the twin of the server's `text.HeadRefs` (`packages/envoy/internal/dispatch/text/runes.go`),
 * held to the same rows (`testdata/head-refs.json` there), for a head the dashboard cuts itself
 * and shows as its characters - a picker row's words, which the list draws as written until the
 * reader's engine answers and wherever the server sent no tree of them (`useRefPicker.ts`). A
 * cut by length alone would leave half of a reference there: `CORE-12` of `CORE-1234`, or
 * `[@Planner](dispatch://agent/` drawn raw. A text at or under the limit is returned unchanged;
 * one over it is cut to its first `limit` characters less the ellipsis's, and the ellipsis is
 * appended. A character is a code point, as the server counts runes.
 *
 * A reference is what the server's cut reads as one: a `dispatch://` address and a page of the
 * dashboard at `origin` as the shared span reader finds them, a URL of anywhere else left out,
 * and a bare issue key and a `#id` as the shared reader finds them (`readTextReferences`), each
 * widened over the inline link that holds it, a picture's `!` included (`inlineLinks`). A span
 * that is a link's address is that address as the link reads it, since the span would run on
 * over the `)` into whatever follows it: `dispatch://CORE-1)s` of `[CORE-1](dispatch://CORE-1)s`.
 * Where one starts before the cut and ends after it, the cut moves back to its start: the link's
 * `[` where a link wraps it. A code span the cut opens and does not close moves it back to its
 * opening backticks the same way, since the head would leave them as text and read what the span
 * holds - an address included - as prose. The cut moves back to a reference's start too where
 * the head, with the ellipsis after it, does not read the references of the text it holds as the
 * text does: one it reads that the text does not write (`CORE-12` of `CORE-12x4`, which is no
 * key), or one the text writes whole that the ellipsis joins (`dispatch://agent/s1` and `…` read
 * as the session `s1…`). A cut that moved drops the whitespace before it, save where the head
 * then ends after a reference: there it keeps the first whitespace character the text has after
 * that reference, so the ellipsis joins nothing and the walk stops - a line of short addresses
 * separated by spaces is cut after its last whole address and a space, not back past every one
 * of them. A cut never empties the head: where moving back would leave nothing before the
 * ellipsis - a text that opens with a reference or a code span longer than the bound - the cut by
 * length stands, since a head of nothing tells a reader less than half a reference does.
 *
 * It reads the text's first `limit` and `LONGEST_WHOLE_REFERENCE` characters, no more, as the
 * server does: a reference or a code span that runs to the edge of that window, where the text
 * runs on past it, may run further, so it is none the cut keeps whole, and is cut by length.
 */
export function headRefs(
  text: string,
  limit: number,
  ellipsis: string,
  origin: string = window.location.origin
): string {
  const keep = Math.max(limit - Array.from(ellipsis).length, 0);
  let cut = 0;
  let windowEnd = text.length;
  let over = false;
  let count = 0;
  let at = 0;
  for (const character of text) {
    if (count === keep) cut = at;
    if (count === limit) over = true;
    if (count === limit + LONGEST_WHOLE_REFERENCE) {
      windowEnd = at;
      break;
    }
    count += 1;
    at += character.length;
  }
  if (!over) return text;
  const head = headEnd(text.slice(0, windowEnd), windowEnd < text.length, cut, ellipsis, origin);
  return `${text.slice(0, head)}${ellipsis}`;
}

/** Where the head of `windowed`, the text's first characters, cut at `cut` ends (`headRefs`): `cut`
 *  moved back past every reference and code span that crosses it, and every reference the head
 *  reads otherwise than the text does, less the whitespace before it but the one a reference ends
 *  at; or `cut` itself where that would leave nothing. A reference or a span that runs to the
 *  window's edge where the window stops short of the text (`truncated`) may run further, so it is
 *  none the cut keeps whole. Each move is back, so it ends. */
function headEnd(
  windowed: string,
  truncated: boolean,
  cut: number,
  ellipsis: string,
  origin: string
): number {
  const whole = (range: { end: number }) => !truncated || range.end < windowed.length;
  const written = referenceExtents(windowed, origin).filter(whole);
  const spans = windowed.includes("`")
    ? codeRanges(windowed).filter((range) => !range.block && whole(range))
    : [];
  let at = cut;
  while (at > 0) {
    let back = at;
    for (const crossing of [...written, ...spans]) {
      if (crossing.start < at && at < crossing.end) back = Math.min(back, crossing.start);
    }
    const read = referenceExtents(`${windowed.slice(0, at)}${ellipsis}`, origin);
    for (const reference of read) {
      if (reference.start < at && !holds(written, reference)) {
        back = Math.min(back, reference.start);
      }
    }
    for (const reference of written) {
      if (reference.end <= at && !holds(read, reference)) back = Math.min(back, reference.start);
    }
    if (back === at) break;
    at = windowed.slice(0, back).replace(TRAILING_SPACE, "").length;
    // A head that now ends after a reference the text follows with whitespace keeps the first of
    // it, so the ellipsis after the head joins nothing: without it the ellipsis would join that
    // reference and the walk step back past it, and on a line of short addresses back past every
    // one of them.
    if (at < back && written.some((reference) => reference.end === at)) at += 1;
  }
  return at === 0 ? cut : at;
}

/** Whether `extents` holds `reference`: the same characters read at the same place. */
function holds(extents: readonly Extent[], reference: Extent): boolean {
  return extents.some(
    (one) =>
      one.start === reference.start &&
      one.end === reference.end &&
      one.written === reference.written
  );
}

/** Where `link` starts as a reader sees it: at its `[`, or at the `!` of a picture. */
function linkStart(link: InlineLink): number {
  return link.picture ? link.open - 1 : link.open;
}

/** Every reference of `text` as a cut reads it (`headRefs`), none of them resolved, since a cut
 *  that splits a reference changes what the text says whatever the reference names: each span
 *  the shared span reader finds that is an address of Dispatch or of the dashboard at `origin`,
 *  read as the link's address where it starts inside a link that writes it there, and each bare
 *  key and `#id` the shared reader finds, each widened over the inline link that holds it. The
 *  link that holds one is the first to open, as the server's link reader lists them. */
function referenceExtents(text: string, origin: string): Extent[] {
  const links = [...inlineLinks(text)].sort((one, other) => linkStart(one) - linkStart(other));
  const extents: Extent[] = [];
  const add = (start: number, end: number, written: string) => {
    const link = links.find((one) => linkStart(one) <= start && start < one.close);
    extents.push(
      link === undefined
        ? { end, start, written }
        : { end: link.close, start: linkStart(link), written }
    );
  };
  for (const span of referenceSpans(text)) {
    const link = links.find(
      (one) =>
        linkStart(one) < span.start &&
        span.start < one.close &&
        one.addressEnd > one.addressStart &&
        text.startsWith(text.slice(one.addressStart, one.addressEnd), span.start)
    );
    const address =
      link === undefined ? span.value : text.slice(link.addressStart, link.addressEnd);
    if (address !== "" && referenceRouteFromHref(address, origin) !== undefined) {
      add(span.start, span.start + address.length, address);
    }
  }
  for (const reference of readTextReferences(text, { origin }).references) {
    if (reference.form !== "address") add(reference.start, reference.end, reference.text);
  }
  return extents;
}
