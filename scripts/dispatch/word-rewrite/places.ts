import {
  APPROVAL_SKIP_REASONS,
  type ApprovalFacts,
  type ApprovalSkipReason,
  approvalSkip,
} from "./approval-skips.ts";
import type { Block } from "./dispatch-api.ts";
import type { Span } from "./judge.ts";

/**
 * The places a program can tell: each occurrence the tool leaves, with the reason the report
 * counts it under. An occurrence no reason leaves is "not judged yet" in report mode, and the
 * judge's in write mode. The reasons are checked in the order they are listed, and the first that
 * holds is the one reported, so an occurrence counts once.
 */

export const PLACE_REASONS = [
  /** The credential's project is another (`whoami` → `project`): it writes that project only. */
  "credential-scope",
  /** The issue is the tree the run names (`--tree`) or under it: its text names the word as the
   *  subject of the change. */
  "this-tree",
  /** The issue is done: Dispatch refuses a new title or version (`ISSUE_CLOSED`). */
  "closed-issue",
  /** An open approval request names the document (`approval.state == "awaiting"`). */
  "approval-request",
  /** In the document's name or slug, or in the slug of a `dispatch://…/artifact/<slug>` link. */
  "name-or-slug",
  /** Inside a typed block that is an ask. */
  "ask-block",
  /** Inside a `check` block of the issue's Verification document, whose change would void the
   *  results recorded against it. Dispatch records results only against those checks, so a
   *  `check` block of any other document is judged as its text is. */
  "check-block",
  /** Inside a typed block's opening line, its id or an attribute: a changed id moves the block. */
  "block-attribute",
  /** A person's version made the block or changed its text (`authorship.ts`). */
  "person",
  /** A version that made or changed the block has no authors, a system author, or a person beside
   *  a session: no program can say no person wrote it. */
  "unknown-author",
  /** A name of the map whose `from` the project has not reached. */
  "code-name-not-yet",
] as const;

export type PlaceReason = (typeof PLACE_REASONS)[number];

export type Reason = PlaceReason | ApprovalSkipReason;

/** Every reason the report counts. */
export const REASONS: readonly Reason[] = [...PLACE_REASONS, ...APPROVAL_SKIP_REASONS];

/** Who can have written an occurrence's block, as `authorship.ts` tells it. */
export type Author = "session" | "person" | "unknown";

/** What leaves every occurrence of an item alike: the item's own facts, and what the approval
 *  reasons of `approval-skips.ts` read (a title is no primary document and has no approval and no
 *  blocks). */
export interface ItemFacts extends ApprovalFacts {
  readonly outOfScope: boolean;
  readonly thisTree: boolean;
  readonly closed: boolean;
}

/** What leaves one occurrence. */
export interface OccurrenceFacts {
  readonly inName: boolean;
  /** Every block whose range holds the occurrence. */
  readonly enclosing: readonly Block[];
  readonly inOpeningLine: boolean;
  /** Whether the occurrence's document is its issue's Verification document. */
  readonly verification: boolean;
  readonly author: Author;
  readonly nameNotYet: boolean;
}

/** The reason that leaves a whole item, or null. */
export function itemPlace(item: ItemFacts): Reason | null {
  if (item.outOfScope) return "credential-scope";
  if (item.thisTree) return "this-tree";
  if (item.closed) return "closed-issue";
  return approvalPlace(item);
}

/** The reason a person's approval, or an open request for it, leaves a whole document, or null:
 *  what a write reads again just before it writes. */
export function approvalPlace(
  facts: ApprovalFacts
): "approval-request" | ApprovalSkipReason | null {
  if (facts.approval?.state === "awaiting") return "approval-request";
  return approvalSkip(facts);
}

/** The reason that leaves one occurrence of an item no item reason leaves, or null. */
export function occurrencePlace(facts: OccurrenceFacts): PlaceReason | null {
  if (facts.inName) return "name-or-slug";
  if (facts.enclosing.some((block) => block.type === "ask")) return "ask-block";
  if (facts.verification && facts.enclosing.some((block) => block.type === "check")) {
    return "check-block";
  }
  if (facts.inOpeningLine) return "block-attribute";
  if (facts.author === "person") return "person";
  if (facts.author === "unknown") return "unknown-author";
  if (facts.nameNotYet) return "code-name-not-yet";
  return null;
}

/** A Dispatch link to a document, whose slug no one can change. */
const ARTIFACT_LINK = /dispatch:\/\/[^\s)\]>"'`]*?\/artifact\/([A-Za-z0-9._-]+)/g;

/** The spans of the slugs of a text's document links, read once per text. */
export function artifactSlugSpans(text: string): Span[] {
  return [...text.matchAll(ARTIFACT_LINK)].map((link) => {
    const end = link.index + link[0].length;
    return { start: end - (link[1] ?? "").length, end };
  });
}

/** Whether the text from `start` to `end` lies in one of the slugs `artifactSlugSpans` found. */
export function inArtifactSlug(slugs: readonly Span[], start: number, end: number): boolean {
  return slugs.some((slug) => start >= slug.start && end <= slug.end);
}

const FENCE = Buffer.from(":::");
const NEWLINE = 0x0a;

/** Whether the byte at `byte` lies in the opening line of a typed block that holds it. */
export function inOpeningLine(
  markdown: Buffer,
  enclosing: readonly Block[],
  byte: number
): boolean {
  return enclosing.some((block) => {
    if (!markdown.subarray(block.from, block.from + FENCE.length).equals(FENCE)) return false;
    const lineEnd = markdown.indexOf(NEWLINE, block.from);
    return byte < (lineEnd === -1 ? block.to : lineEnd);
  });
}

/** Every block whose range holds the bytes from `byteStart` to `byteEnd`, innermost first. */
export function enclosingBlocks(
  blocks: readonly Block[],
  byteStart: number,
  byteEnd: number
): Block[] {
  return blocks
    .filter((block) => block.from <= byteStart && byteEnd <= block.to)
    .sort((a, b) => a.to - a.from - (b.to - b.from));
}
