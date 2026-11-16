import { codeRanges } from "./code-ranges";
import { partyLink } from "./ref-picker";
import {
  buildDispatchReference,
  type DispatchReferenceRoute,
  deliveryAddressOf,
  type PartyRoute,
  partyOfAddress,
} from "./routes";
import { readTextReferences, type TextReference } from "./text-references";

/**
 * Who a draft addresses, read out of its own words. A mention is a reference (INFRA-1702 R4):
 * the party is written into the text as a link whose words are the mention sign and its name and
 * whose address names it, and nothing at all is kept beside the text. So every surface that asks
 * who a draft names - the delivery chips under a box, the composer's delivery notices, whether a
 * typed `/btw` is a command, and the request Send makes - asks here, and this asks the one reader
 * of what a text cites (`readTextReferences`), the twin of the server's own. What the writer is
 * told and what the server will read can therefore never be two readings of the same draft. The
 * server reads the address alone, never a link's words, so the words decide only what a reader
 * sees (`@legion/contracts`'s `referenceReading`).
 *
 * A party reference a code context holds - a fenced code block, an indented code block or a code
 * span (`TextReference.inCode`) - names nobody, as the server reads it
 * (`api.commentPartiesOf`): a draft writing one is showing the address rather than addressing
 * its party, which is how every renderer of the record draws it. So it is no target, it makes no
 * draft one that names somebody, and the composer leaves a typed `/btw` in front of it the
 * reader's own first word. The same party named outside code is named by that occurrence, and a
 * worded link's address (`[@Planner](dispatch://agent/...)`) is no code context at all.
 */

/** Whether a reference names a party: an agent's session, a role, or a person. */
export function isPartyRoute(route: DispatchReferenceRoute): route is PartyRoute {
  return route.kind === "agent-session" || route.kind === "role" || route.kind === "person";
}

/** The mention of the party at `target`, written as the link that names it: the mention sign
 *  and `title` for the words, or the sign and the address itself where the party has no name
 *  that fits on one line. Undefined for an address that names no party, which is then mentioned
 *  by nothing. */
export function partyMention(target: string, title: string): string | undefined {
  const route = partyOfAddress(target);
  if (route === undefined) return undefined;
  const named = title.trim();
  const words = named === "" || /[\r\n]/.test(named) ? target : named;
  return partyLink(words, buildDispatchReference(route));
}

/** What a draft's text says about who it addresses. */
export interface DraftParties {
  /** Every delivering party the text names, in the order it names them and each once: who the
   *  write is delivered to, and what the delivery notices are drawn from. */
  readonly targets: readonly string[];
  /** Whether the text names a party at all, a person among them. Nothing is delivered to a
   *  person, but naming one is still addressing somebody: it is what makes a typed `/btw` the
   *  command it looks like rather than the reader's own first word. */
  readonly named: boolean;
}

/** The parties `body` names, each as the reference naming it stands in the text: a party
 *  written in a code context names nobody and is neither. */
export function draftParties(body: string): DraftParties {
  const targets: string[] = [];
  const seen = new Set<string>();
  let named = false;
  for (const found of readTextReferences(body).references) {
    const route = found.route;
    if (found.inCode || route === undefined || !isPartyRoute(route)) continue;
    named = true;
    const target = deliveryAddressOf(route);
    if (target === undefined || seen.has(target)) continue;
    seen.add(target);
    targets.push(target);
  }
  return { named, targets };
}

/** One inline link a text writes - `[words](address "title")` - as offsets into it: its `[`, the
 *  `]` its words end at, its address without the angle brackets it may stand in, and one past its
 *  `)`; `picture` where a `!` before the `[` makes it a picture's syntax (`![caption](address)`),
 *  which a renderer draws as the picture rather than as a link. */
export interface InlineLink {
  readonly open: number;
  readonly wordsEnd: number;
  readonly addressStart: number;
  readonly addressEnd: number;
  readonly close: number;
  readonly picture: boolean;
}

/** What follows a line break where the next line is blank, which ends the paragraph and every
 *  bracket open in it. */
const BLANK_LINE_AFTER = /[ \t]*\n/y;

/**
 * The most characters a link's tail - its address and title, after the `(` - is read across: as
 * many as the server reads past a cut to keep a reference whole (`longestWholeReferenceRunes` in
 * `packages/envoy/internal/dispatch/text/runes.go`), far more than any address or title a person
 * writes. A tail that runs further reads as no link. Every `](` of a text is read across no more
 * than this, so a draft holding thousands of `](` that open no link - parentheses that never
 * balance, a title that never closes - is read in time that grows with its length, not with its
 * square.
 */
const LONGEST_LINK_TAIL = 1024;

/** `at` moved past the spaces, tabs and one line break that stand there, short of `limit`;
 *  undefined at a second line break, since a link's parts are never more than a line apart and a
 *  blank line ends the paragraph, and so the link. */
function pastSpace(body: string, at: number, limit: number): number | undefined {
  let broken = false;
  for (; at < limit; at += 1) {
    const character = body.charAt(at);
    if (character === "\n") {
      if (broken) return undefined;
      broken = true;
    } else if (character !== " " && character !== "\t") {
      break;
    }
  }
  return at;
}

/** What closes a link's title, by what opens it. */
const TITLE_CLOSERS: Readonly<Record<string, string>> = { '"': '"', "'": "'", "(": ")" };

/** The address and the end of an inline link whose `(` stands just before `from`, read as
 *  CommonMark reads a link's destination and title: an address in angle brackets, or one with no
 *  space whose parentheses balance, then, after a space, a title in double quotes, single quotes
 *  or parentheses, then the `)`, all within the paragraph and `LONGEST_LINK_TAIL` characters.
 *  Undefined where no link ends there. */
function linkTail(
  body: string,
  from: number
): Pick<InlineLink, "addressEnd" | "addressStart" | "close"> | undefined {
  const limit = Math.min(body.length, from + LONGEST_LINK_TAIL);
  let at = pastSpace(body, from, limit);
  if (at === undefined) return undefined;
  const angled = body.charAt(at) === "<";
  const addressStart = angled ? at + 1 : at;
  at = addressStart;
  if (angled) {
    while (at < limit && body.charAt(at) !== ">") {
      const character = body.charAt(at);
      if (character === "\n" || character === "<") return undefined;
      at += character === "\\" ? 2 : 1;
    }
    if (at >= limit) return undefined;
  } else {
    let depth = 0;
    for (; at < limit; at += 1) {
      const character = body.charAt(at);
      if (character <= " " || (character === ")" && depth === 0)) break;
      if (character === "\\") at += 1;
      else if (character === "(") depth += 1;
      else if (character === ")") depth -= 1;
    }
    if (depth !== 0 || at >= limit) return undefined;
  }
  const addressEnd = at;
  if (angled) at += 1;
  const spaced = pastSpace(body, at, limit);
  if (spaced === undefined) return undefined;
  const closer = spaced > at ? TITLE_CLOSERS[body.charAt(spaced)] : undefined;
  at = spaced;
  if (closer !== undefined) {
    at += 1;
    // A title may run over a line break, never over a blank line.
    let blank = false;
    while (at < limit && body.charAt(at) !== closer) {
      const character = body.charAt(at);
      if (character === "\n") {
        if (blank) return undefined;
        blank = true;
      } else if (character !== " " && character !== "\t") {
        blank = false;
      }
      at += character === "\\" ? 2 : 1;
    }
    if (at >= limit) return undefined;
    const closed = pastSpace(body, at + 1, limit);
    if (closed === undefined) return undefined;
    at = closed;
  }
  return at < limit && body.charAt(at) === ")"
    ? { addressEnd, addressStart, close: at + 1 }
    : undefined;
}

/**
 * Every inline link `body` writes, in the order their words close, read as CommonMark and so
 * the renderer read them: a backslash takes the character after it as a literal one, brackets
 * that balance stand inside a link's words (`[@Planner [2]](…)`), a link holds no link - once one
 * closes, no `[` before it opens one, a picture's caption aside - and a blank line ends every
 * bracket open before it. Code holds no link syntax, so what the text's code contexts hold
 * (`codeRanges`) is passed over.
 */
function readInlineLinks(body: string): readonly InlineLink[] {
  const code = codeRanges(body);
  const links: InlineLink[] = [];
  const openers: { at: number; picture: boolean }[] = [];
  // Every opener below this index that is not a picture's was open when a link closed, and so
  // opens no link: one mark for all of them, rather than a walk over them at every link.
  let closedBelow = 0;
  let nextCode = 0;
  for (let at = 0; at < body.length; at += 1) {
    while (nextCode < code.length && (code[nextCode]?.end ?? 0) <= at) nextCode += 1;
    const range = code[nextCode];
    if (range !== undefined && range.start <= at) {
      at = range.end - 1;
      continue;
    }
    const character = body.charAt(at);
    if (character === "\\") {
      at += 1;
    } else if (character === "\n") {
      BLANK_LINE_AFTER.lastIndex = at + 1;
      if (BLANK_LINE_AFTER.test(body)) {
        openers.length = 0;
        closedBelow = 0;
      }
    } else if (character === "[") {
      const picture = body.charAt(at - 1) === "!" && body.charAt(at - 2) !== "\\";
      closedBelow = Math.min(closedBelow, openers.length);
      openers.push({ at, picture });
    } else if (character === "]") {
      const opener = openers.pop();
      const opens = opener !== undefined && (opener.picture || openers.length >= closedBelow);
      const tail = opens && body.charAt(at + 1) === "(" ? linkTail(body, at + 2) : undefined;
      if (opener === undefined || tail === undefined) continue;
      links.push({ ...tail, open: opener.at, picture: opener.picture, wordsEnd: at });
      if (!opener.picture) closedBelow = openers.length;
      at = tail.close - 1;
    }
  }
  return links;
}

/** The last text `inlineLinks` read, and the links it found there. */
let lastRead: { readonly body: string; readonly links: readonly InlineLink[] } | undefined;

/** Every inline link `body` writes (`readInlineLinks`). A draft's pills ask about each reference
 *  of one text in turn (`linkOf`, `inLinkWords`), so the text last read is read once, not once
 *  for every reference it holds. */
export function inlineLinks(body: string): readonly InlineLink[] {
  if (lastRead === undefined || lastRead.body !== body) {
    lastRead = { body, links: readInlineLinks(body) };
  }
  return lastRead.links;
}

/** The inline link whose address is `body.slice(start, end)`, or undefined where none is. */
function linkWithAddress(body: string, start: number, end: number): InlineLink | undefined {
  return inlineLinks(body).find((link) => link.addressStart === start && link.addressEnd === end);
}

/** `[start, end)` widened over the inline link that wraps it, where one does. A mention's
 *  address is only half of what a reader sees, so a caller taking one out of the text must take
 *  its words with it rather than leave `[@Planner](` behind. */
function linkAround(body: string, start: number, end: number): { end: number; start: number } {
  const link = linkWithAddress(body, start, end);
  return link === undefined ? { end, start } : { end: link.close, start: link.open };
}

/** The inline link `body` writes a reference's address in: the link's own Markdown, from the
 *  `[` that opens its words to the `)` that closes its address, which a reader of its words
 *  parses (`parsed-references.ts`'s `parsedLinkWords`) rather than reads out of the characters,
 *  and whether it is a picture's syntax (`![caption](address)`), which a renderer draws as the
 *  picture rather than as a link. */
export interface WrittenLink {
  readonly picture: boolean;
  readonly markdown: string;
}

/** The inline link whose address `reference` is (`WrittenLink`), or undefined where the
 *  reference stands on its own: what a draft's pill reads, as the rendered record reads the
 *  same text. */
export function linkOf(
  body: string,
  reference: Pick<TextReference, "end" | "start">
): WrittenLink | undefined {
  const link = linkWithAddress(body, reference.start, reference.end);
  return link === undefined
    ? undefined
    : { markdown: body.slice(link.open, link.close), picture: link.picture };
}

/** Whether `reference` stands in an inline link's words rather than on its own or as a link's
 *  address: `CORE-1` in `[see CORE-1 here](https://x)`, and in `[CORE-1](dispatch://CORE-2)`,
 *  whose address `CORE-2` is not in its words. The renderer links no reference there - its link
 *  pass reads no text a link, a code span or a code block holds (`RefLink.tsx`'s
 *  `excludedRefAncestorTags` and `linkPassReads`), and a link's words are text its `<a>` holds -
 *  so what reads a draft as the record will be drawn, a draft's pills among them, links none
 *  there either. */
export function inLinkWords(
  body: string,
  reference: Pick<TextReference, "end" | "start">
): boolean {
  return inlineLinks(body).some(
    (link) => link.open < reference.start && reference.end <= link.wordsEnd
  );
}

/** Where `body` first mentions the party at `target`, taken whole - the link's words with its
 *  address - or undefined where it never does. A reference a code context holds mentions
 *  nobody, so none is found there: the one caller takes the mention it finds out of the draft
 *  (`seededDraft`), and the text inside a fence is the reader's own, not an address this
 *  channel owes. */
export function partyMentionSpan(
  body: string,
  target: string
): { end: number; start: number } | undefined {
  for (const found of readTextReferences(body).references) {
    const route = found.route;
    if (
      found.inCode ||
      route === undefined ||
      !isPartyRoute(route) ||
      deliveryAddressOf(route) !== target
    ) {
      continue;
    }
    return linkAround(body, found.start, found.end);
  }
  return undefined;
}
