import { SEARCH_OPERATORS, SEARCH_RESULT_KINDS } from "@legion/contracts";
import { type SnippetSegment, snippetSegments } from "@legion/contracts/dispatch-snippet";
import type { SearchOperator, SearchOwner, SearchResult, SearchResultKind } from "../../api/types";

/** `value` as a result kind, or `undefined` where this dashboard knows no such kind. Every
 *  surface that needs the kind itself - the palette, the related-items list - narrows here. */
export function searchResultKind(value: string): SearchResultKind | undefined {
  return SEARCH_RESULT_KINDS.find((known) => known === value);
}

/** The operator forms `read` names, as the forms this dashboard knows. An answer's reading of
 *  the query carries each form as the plain string the route table declares, and
 *  `SEARCH_OPERATORS` is the closed list that string is one of; a form this build has no word
 *  for is left out, as is every form when a Dispatch that predates the field answers none. The
 *  one reader asks only whether a form was used, so the answer keeps `SEARCH_OPERATORS`' own
 *  order rather than the reading's. */
export function searchOperators(read: readonly string[] | null | undefined): SearchOperator[] {
  return SEARCH_OPERATORS.filter((known) => read?.includes(known) === true);
}

/** How many hits of one kind the bar lists (design v3, state 1). */
export const HITS_PER_KIND = 6;

/** The heading of each kind's group of hits. */
const KIND_HEADINGS: Readonly<Record<SearchResultKind, string>> = {
  ask: "Asks",
  comment: "Comments",
  document: "Documents",
  issue: "Issues",
  message: "Messages",
};

/** The hits of one kind, under the heading the bar draws above them. */
export interface ResultGroup {
  /** The kind, as the route table names it. */
  readonly kind: string;
  /** "Issues", "Documents", and so on; a kind this build has no word for keeps its own. */
  readonly heading: string;
  readonly results: SearchResult[];
}

/** The hits grouped by their kind, each group in the rank order of its first hit, and at most
 *  `HITS_PER_KIND` hits in a group, in the server's order. */
export function groupResults(results: readonly SearchResult[]): ResultGroup[] {
  const groups = new Map<string, ResultGroup>();

  for (const result of results) {
    const group = groups.get(result.kind);
    if (group === undefined) {
      const known = searchResultKind(result.kind);
      groups.set(result.kind, {
        heading: known === undefined ? result.kind : KIND_HEADINGS[known],
        kind: result.kind,
        results: [result],
      });
    } else if (group.results.length < HITS_PER_KIND) {
      group.results.push(result);
    }
  }

  return [...groups.values()];
}

/** What a hit's owner is called in the bar: an issue by its key and title, a project document by
 *  its project and name. */
export function ownerWords(owner: SearchOwner): { readonly key: string; readonly name: string } {
  return owner.kind === "issue"
    ? { key: owner.key ?? "", name: owner.title ?? "" }
    : { key: owner.project ?? "", name: owner.name ?? "" };
}

/** Whether a hit is the issue itself, whose text is that issue's title: its row names the key and
 *  the title once. A document, a comment, an ask or a message of an issue names its issue. */
export function hitIsItsIssue(result: SearchResult): boolean {
  return result.kind === "issue" && result.owner.kind === "issue";
}

/**
 * The title a hit that is the issue itself shows in place of its snippet, or `undefined` where
 * the snippet is the whole title and shows instead, with the words that matched marked.
 * `snippetText` is the snippet's text without its marks. The server's headline returns a short
 * title whole, but it can start at the first word that matched ("rate limit headers" of "API:
 * rate limit headers"), and the row then shows the whole title, unmarked.
 */
export function issueHitTitle(result: SearchResult, snippetText: string): string | undefined {
  const title = result.owner.title ?? "";
  return snippetText.trim() === title.trim() ? undefined : title;
}

/** Characters of Unicode's private use area that stand for the server's `<mark>` and `</mark>`
 *  while a snippet's markup is taken out (`plainSnippet`), so a match keeps its highlight. */
const MARK_OPEN = "\uE000";
const MARK_CLOSE = "\uE001";

/** The quoted values of a directive's attributes, the one part of them written for a person; a
 *  value the snippet's window cut runs to its end. */
const QUOTED = /"([^"]*)(?:"|$)/gu;

/** Each piece of markup a snippet's text can hold, in the order `plainSnippet` takes them out,
 *  and what each leaves. */
const MARKUP: readonly (readonly [RegExp, string | ((match: string, inner: string) => string)])[] =
  [
    // A directive's fence - ":::plan_task", or a bare ":::" - leaves nothing.
    [/:{3,}[ \t]*[A-Za-z][\w-]*/gu, " "],
    [/:{3,}/gu, " "],
    // An inline directive's name, ":ref" of ":ref[DEMO-1]".
    [/(^|[\s(]):[A-Za-z][\w-]*(?=[[{])/gu, "$1"],
    // A directive's attributes, closed or cut, leave their quoted values.
    [
      /\{([^{}]*)(?:\}|$)/gu,
      (_match, inner) => ` ${[...inner.matchAll(QUOTED)].map((value) => value[1]).join(" ")} `,
    ],
    // An image or a link leaves its words; what a cut link leaves of its address goes, and so
    // does a lone bracket. An address holds no space, so a cut one ends at the first.
    [/!?\[([^\]]*)\]\([^)\s]*\)?/gu, "$1"],
    [/\[([^\]]*)\]\[[^\]]*\]/gu, "$1"],
    [/\]\([^)\s]*\)?/gu, ""],
    [/[[\]]/gu, ""],
    // An autolink leaves its address; any other tag, nothing.
    [/<((?:https?|mailto):[^>\s]*)>/gu, "$1"],
    [/<\/?[A-Za-z][^>]*>/gu, ""],
    // A heading's, a quote's and a list item's mark at a line's start; a table's rule, a rule
    // between sections, and the pipes between a table's cells.
    [/(^|\n)[ \t]*(?:#{1,6}|>|[-*+]|\d+[.)])[ \t]+/gu, "$1"],
    [/(^|\n)[ \t|:-]{3,}(?=\n|$)/gu, "$1"],
    [/\|/gu, " "],
    // Strong, strike-through and code marks, then a single star or underscore that opens or
    // closes emphasis - never one inside a word, as in "snake_case".
    [/\*\*|__|~~|`+/gu, ""],
    [/(^|[\s(])[*_]+(?=\S)/gu, "$1"],
    [/(\S)[*_]+(?=[\s).,;:!?]|$)/gu, "$1"],
    // A backslash escape leaves the character it escapes.
    [/\\([^\w\s])/gu, "$1"],
  ];

/**
 * A hit's snippet as plain words for the second line of its row (design v3, rule 1: no marker
 * text): the server's snippet is a window cut from a document's Markdown and Proof's directives
 * (`:::plan_task{#t1 serves="…"}`), so its fences, attributes, links, tags, list and table marks
 * and emphasis come out, leaving the words a person wrote, on one line. The words the search
 * matched stay marked.
 */
export function plainSnippet(snippet: string): SnippetSegment[] {
  let text = snippetSegments(snippet)
    .map((segment) => (segment.mark ? `${MARK_OPEN}${segment.text}${MARK_CLOSE}` : segment.text))
    .join("");
  for (const [pattern, leaves] of MARKUP) {
    text =
      typeof leaves === "string" ? text.replace(pattern, leaves) : text.replace(pattern, leaves);
  }
  const segments: SnippetSegment[] = [];
  let mark = false;
  for (const part of text
    .replace(/\s+/gu, " ")
    .trim()
    .split(/([\uE000\uE001])/u)) {
    if (part === MARK_OPEN || part === MARK_CLOSE) {
      mark = part === MARK_OPEN;
    } else if (part !== "") {
      segments.push({ mark, text: part });
    }
  }
  return segments;
}

/** Mirrors the server's first positive websearch token for document deep links. */
export function firstHighlightTerm(query: string): string | undefined {
  for (const token of query.trim().split(/\s+/u)) {
    if (token === "" || token.startsWith("-") || token.toLowerCase() === "or") {
      continue;
    }
    const term = token.replace(/^"+|"+$/gu, "");
    if (term !== "") {
      return term;
    }
  }
  return undefined;
}

export function stepActive(index: number, delta: 1 | -1, count: number): number {
  return count === 0 ? 0 : (index + delta + count) % count;
}

export function kindLabel(kind: SearchResultKind): "issue" | "doc" | "comment" | "ask" | "message" {
  return kind === "document" ? "doc" : kind;
}

export function optionId(result: SearchResult): string {
  return `search-option-${result.kind}-${result.id}`;
}
