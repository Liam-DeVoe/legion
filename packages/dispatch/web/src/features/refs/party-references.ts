import { MENTION_SIGN, partyLink } from "./ref-picker";
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
 * sees (`isPartyMention`).
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

/** Whether a link whose words are `words` and whose address names `route` is a party's mention
 *  (`partyLink`): its words start with the mention sign, and it reads as that sign and the
 *  party's current name rather than as its words, wherever it is shown - a hand-written one as
 *  much as a picked one (R4). Every other worded link keeps its author's words (R8). */
export function isPartyMention(words: string, route: DispatchReferenceRoute | undefined): boolean {
  return words.startsWith(MENTION_SIGN) && route !== undefined && isPartyRoute(route);
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

/** Where the words of the inline link whose address is `body.slice(start, end)` open - the `[`
 *  before them - or undefined where no link wraps that address. */
function linkOpening(body: string, start: number, end: number): number | undefined {
  if (body.charAt(end) !== ")" || body.slice(start - 2, start) !== "](") return undefined;
  // The words run back to the `[` that opens them. A bracket among them is escaped
  // (`partyLink`), and a line break ends the search: no link's words hold one.
  for (let at = start - 3; at >= 0; at -= 1) {
    const character = body.charAt(at);
    if (character === "\n") break;
    if (character === "[" && body.charAt(at - 1) !== "\\") return at;
  }
  return undefined;
}

/** `[start, end)` widened over the inline link that wraps it, where one does. A mention's
 *  address is only half of what a reader sees, so a caller taking one out of the text must take
 *  its words with it rather than leave `[@Planner](` behind. */
function linkAround(body: string, start: number, end: number): { end: number; start: number } {
  const opening = linkOpening(body, start, end);
  return opening === undefined ? { end, start } : { end: end + 1, start: opening };
}

/** The inline link `body` writes a reference's address in: its words as a reader sees them -
 *  each bracket `partyLink` escaped is a bracket again - and whether it is a picture's syntax
 *  (`![caption](address)`), which a renderer draws as the picture rather than as a link. */
export interface WrittenLink {
  readonly picture: boolean;
  readonly words: string;
}

/** The inline link whose address `reference` is (`WrittenLink`), or undefined where the
 *  reference stands on its own: what a draft's pill reads, as the rendered record reads the
 *  same text. */
export function linkOf(
  body: string,
  reference: Pick<TextReference, "end" | "start">
): WrittenLink | undefined {
  const opening = linkOpening(body, reference.start, reference.end);
  return opening === undefined
    ? undefined
    : {
        picture: body.charAt(opening - 1) === "!",
        words: body.slice(opening + 1, reference.start - 2).replace(/\\([[\]])/gu, "$1"),
      };
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
