import { SEARCH_OPERATORS, SEARCH_RESULT_KINDS } from "@legion/contracts";
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
