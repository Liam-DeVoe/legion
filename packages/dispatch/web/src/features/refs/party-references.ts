import { readWrittenWords, type WordsFormatting } from "@legion/contracts";

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
interface InlineLink {
  readonly open: number;
  readonly wordsEnd: number;
  readonly addressStart: number;
  readonly addressEnd: number;
  readonly close: number;
  readonly picture: boolean;
}

/** The spaces, tabs and line breaks a link may hold between its parts, and a blank line, which
 *  ends the paragraph and every bracket open in it. */
const LINK_SPACE = /[ \t\n]*/y;
const BLANK_LINE_AFTER = /[ \t]*\n/y;

/** `at` moved past the spaces, tabs and line breaks that stand there. */
function pastSpace(body: string, at: number): number {
  LINK_SPACE.lastIndex = at;
  LINK_SPACE.test(body);
  return LINK_SPACE.lastIndex;
}

/** What closes a link's title, by what opens it. */
const TITLE_CLOSERS: Readonly<Record<string, string>> = { '"': '"', "'": "'", "(": ")" };

/** The address and the end of an inline link whose `(` stands just before `from`, read as
 *  CommonMark reads a link's destination and title: an address in angle brackets, or one with no
 *  space whose parentheses balance, then, after a space, a title in double quotes, single quotes
 *  or parentheses, then the `)`. Undefined where no link ends there. */
function linkTail(
  body: string,
  from: number
): Pick<InlineLink, "addressEnd" | "addressStart" | "close"> | undefined {
  let at = pastSpace(body, from);
  const angled = body.charAt(at) === "<";
  const addressStart = angled ? at + 1 : at;
  at = addressStart;
  if (angled) {
    while (at < body.length && body.charAt(at) !== ">") {
      const character = body.charAt(at);
      if (character === "\n" || character === "<") return undefined;
      at += character === "\\" ? 2 : 1;
    }
    if (body.charAt(at) !== ">") return undefined;
  } else {
    let depth = 0;
    for (; at < body.length; at += 1) {
      const character = body.charAt(at);
      if (character <= " " || (character === ")" && depth === 0)) break;
      if (character === "\\") at += 1;
      else if (character === "(") depth += 1;
      else if (character === ")") depth -= 1;
    }
    if (depth !== 0) return undefined;
  }
  const addressEnd = at;
  if (angled) at += 1;
  const spaced = pastSpace(body, at);
  const closer = spaced > at ? TITLE_CLOSERS[body.charAt(spaced)] : undefined;
  at = spaced;
  if (closer !== undefined) {
    at += 1;
    while (at < body.length && body.charAt(at) !== closer) {
      at += body.charAt(at) === "\\" ? 2 : 1;
    }
    if (at >= body.length) return undefined;
    at = pastSpace(body, at + 1);
  }
  return body.charAt(at) === ")" ? { addressEnd, addressStart, close: at + 1 } : undefined;
}

/**
 * Every inline link `body` writes, in the order their words close, read as CommonMark and so
 * the renderer read them: a backslash takes the character after it as a literal one, brackets
 * that balance stand inside a link's words (`[@Planner [2]](…)`), a link holds no link - once one
 * closes, no `[` before it opens one, a picture's caption aside - and a blank line ends every
 * bracket open before it. Code holds no link syntax, so what the text's code contexts hold
 * (`codeRanges`) is passed over.
 */
function inlineLinks(body: string): InlineLink[] {
  const code = codeRanges(body);
  const links: InlineLink[] = [];
  const openers: { at: number; picture: boolean; active: boolean }[] = [];
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
      if (BLANK_LINE_AFTER.test(body)) openers.length = 0;
    } else if (character === "[") {
      const picture = body.charAt(at - 1) === "!" && body.charAt(at - 2) !== "\\";
      openers.push({ active: true, at, picture });
    } else if (character === "]") {
      const opener = openers.pop();
      const tail =
        opener?.active === true && body.charAt(at + 1) === "(" ? linkTail(body, at + 2) : undefined;
      if (opener === undefined || tail === undefined) continue;
      links.push({ ...tail, open: opener.at, picture: opener.picture, wordsEnd: at });
      if (!opener.picture) {
        for (const earlier of openers) {
          if (!earlier.picture) earlier.active = false;
        }
      }
      at = tail.close - 1;
    }
  }
  return links;
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

/** The inline link `body` writes a reference's address in: its words as a reader sees them and
 *  how they are formatted, read out of the characters as a parsed record's are read out of its
 *  marks (`@legion/contracts`'s `readWrittenWords`) - each backslash escape undone, a bracket
 *  `partyLink` escaped among them, and the runs that write formatting gone - and whether it is a
 *  picture's syntax (`![caption](address)`), which a renderer draws as the picture rather than
 *  as a link. */
export interface WrittenLink {
  readonly picture: boolean;
  readonly words: string;
  readonly formatting: WordsFormatting;
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
    : { picture: link.picture, ...readWrittenWords(body.slice(link.open + 1, link.wordsEnd)) };
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
