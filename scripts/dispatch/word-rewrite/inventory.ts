import { z } from "zod";
import { Authorship, titleAuthor } from "./authorship.ts";
import {
  type Actor,
  Artifact,
  type Block,
  type DispatchReads,
  Event,
  IssueDetails,
  IssueRow,
  type Owner,
  Text,
} from "./dispatch-api.ts";
import { sentenceAt } from "./judge.ts";
import {
  applies,
  type MapEntry,
  nameAt,
  type Reach,
  renamedNamesIn,
  testNameAt,
} from "./name-map.ts";
import { indexOfByte, matches } from "./pattern.ts";
import {
  type Author,
  enclosingBlocks,
  type ItemFacts,
  inArtifactSlug,
  inOpeningLine,
  itemPlace,
  occurrencePlace,
  type Reason,
} from "./places.ts";
import { verdictKey } from "./verdicts.ts";

/**
 * What the tool reads: the items (each issue's title, each issue document, each project
 * document), their text, and each occurrence of the word in them with the place that leaves it,
 * when a program can tell one.
 */

export type ItemKind = "title" | "issue-document" | "project-document";

export const ITEM_KINDS: readonly ItemKind[] = ["title", "issue-document", "project-document"];

export interface IssueFacts {
  readonly key: string;
  readonly project: string;
  readonly title: string;
  readonly status: string;
  readonly closed: boolean;
  readonly lastSeq: number;
  readonly createdBy: Actor | null;
}

export interface Item {
  readonly kind: ItemKind;
  readonly project: string;
  /** The issue key of a title, the artifact id of a document. */
  readonly id: string;
  readonly owner: Owner;
  readonly issue: IssueFacts | null;
  readonly artifact: Artifact | null;
}

/** The paragraph an occurrence stands in: the innermost block that holds it, or its line. */
export interface Paragraph {
  readonly start: number;
  readonly end: number;
  readonly text: string;
  readonly blockId: string | null;
}

export interface Occurrence {
  /** Its place in the item's text, or in the document's name for `inName`. */
  readonly start: number;
  readonly end: number;
  readonly byteStart: number;
  readonly inName: boolean;
  readonly paragraph: Paragraph | null;
  /** Its index among the occurrences of its paragraph. */
  readonly indexInParagraph: number;
  /** The program-told place that leaves it, or null. */
  readonly reason: Reason | null;
  /** The map name that covers it, or null. */
  readonly name: MapEntry | null;
  /** The key its verdict is kept under; null for an occurrence in a name. */
  readonly key: string | null;
}

/** A check block that names an old name of the map (the report's own section). */
export interface CheckName {
  readonly block: string;
  readonly old: string;
}

export interface Examined {
  readonly item: Item;
  /** The title, or the document's markdown. */
  readonly text: string;
  /** The document's token as read; null for a title. */
  readonly token: string | null;
  /** The document's version as read; 0 for a title. */
  readonly version: number;
  /** The document's blocks as read; none for a title. */
  readonly blocks: readonly Block[];
  readonly occurrences: readonly Occurrence[];
  readonly checkNames: readonly CheckName[];
  /** The issue's events, read for a title that holds the word. */
  readonly events: readonly Event[];
}

/** What examining an item needs beside the item. */
export interface Context {
  readonly reads: DispatchReads;
  readonly entries: readonly MapEntry[];
  reachOf(project: string): Reach;
  /** The project the credential is limited to, or null. */
  readonly scope: string | null;
  /** The keys of the tree the run names and of every issue under it. */
  readonly tree: ReadonlySet<string>;
}

/** How many issues are read at once: the request rate bounds the run, this only keeps some
 *  requests in flight while others wait their turn. */
export const READ_CONCURRENCY = 4;

/** `work` over every value, `limit` at a time, answers in the order of `values`. */
export async function inPool<T, R>(
  values: readonly T[],
  limit: number,
  work: (value: T) => Promise<R>
): Promise<R[]> {
  const results: R[] = new Array(values.length);
  let next = 0;
  const workers = Array.from({ length: Math.min(limit, values.length) }, async () => {
    while (next < values.length) {
      const index = next;
      next += 1;
      const value = values[index] as T;
      results[index] = await work(value);
    }
  });
  await Promise.all(workers);
  return results;
}

/** The keys of the tree that `root` names: the root and every issue under it. */
export async function readTree(reads: DispatchReads, root: string): Promise<Set<string>> {
  const tree = new Set<string>([root]);
  const queue = [root];
  for (let key = queue.shift(); key !== undefined; key = queue.shift()) {
    for (const child of z.array(IssueRow).parse(await reads.children(key))) {
      if (tree.has(child.key)) continue;
      tree.add(child.key);
      queue.push(child.key);
    }
  }
  return tree;
}

function issueFacts(row: IssueRow, project: string): IssueFacts {
  return {
    key: row.key,
    project,
    title: row.title,
    status: row.status,
    closed: row.closed_at !== null || row.status === "done",
    lastSeq: row.last_seq,
    createdBy: row.created_by ?? null,
  };
}

function titleItem(issue: IssueFacts): Item {
  return {
    kind: "title",
    project: issue.project,
    id: issue.key,
    owner: { kind: "issue", key: issue.key },
    issue,
    artifact: null,
  };
}

function documentItems(artifacts: readonly Artifact[], issue: IssueFacts | null): Item[] {
  return artifacts
    .filter((artifact) => artifact.kind === "doc")
    .map((artifact) => ({
      kind: issue === null ? "project-document" : "issue-document",
      project: artifact.project,
      id: artifact.id,
      owner:
        issue === null
          ? { kind: "document", project: artifact.project, slug: artifact.slug, id: artifact.id }
          : { kind: "issue", key: issue.key },
      issue,
      artifact,
    }));
}

/** Every item of one issue, from its row: its title and its documents. */
async function rowItems(reads: DispatchReads, issue: IssueFacts): Promise<Item[]> {
  const artifacts = z.array(Artifact).parse(await reads.issueArtifacts(issue.key));
  return [titleItem(issue), ...documentItems(artifacts, issue)];
}

/** Every item of a project: each listed issue's title and documents, then its own documents. */
export async function projectItems(
  reads: DispatchReads,
  project: string,
  includeClosed: boolean
): Promise<Item[]> {
  const rows = z.array(IssueRow).parse(await reads.issues(project, includeClosed));
  const issues = rows.map((row) => issueFacts(row, project));
  const perIssue = await inPool(issues, READ_CONCURRENCY, (issue) => rowItems(reads, issue));
  const documents = z.array(Artifact).parse(await reads.projectDocuments(project));
  return [...perIssue.flat(), ...documentItems(documents, null)];
}

/** One issue's items read afresh: `GET /api/v1/issues/{key}` gives its row and its documents. */
export async function issueItems(reads: DispatchReads, key: string): Promise<Item[]> {
  const details = IssueDetails.parse(await reads.issue(key));
  const issue = issueFacts(details, details.project);
  return [titleItem(issue), ...documentItems(details.artifacts ?? [], issue)];
}

/** One project document read afresh, or none when it is gone. */
export async function projectDocumentItem(
  reads: DispatchReads,
  project: string,
  id: string
): Promise<Item[]> {
  const documents = z.array(Artifact).parse(await reads.projectDocuments(project));
  return documentItems(
    documents.filter((document) => document.id === id),
    null
  );
}

/** Every event of an issue, oldest first, read a page of 200 at a time. */
export async function issueEvents(reads: DispatchReads, key: string): Promise<Event[]> {
  const events: Event[] = [];
  let after = 0;
  while (true) {
    const page = z.array(Event).parse(await reads.issueEvents(key, after, 200));
    events.push(...page);
    const last = page.at(-1);
    if (page.length < 200 || last === undefined || last.seq <= after) return events;
    after = last.seq;
  }
}

function itemFacts(context: Context, item: Item, blocks: readonly Block[]): ItemFacts {
  return {
    outOfScope: context.scope !== null && context.scope !== item.project,
    thisTree: item.issue !== null && context.tree.has(item.issue.key),
    closed: item.issue?.closed ?? false,
    primary: item.artifact?.primary === true,
    approval: item.artifact?.approval,
    blocks,
  };
}

/** The occurrences of a name: a document's name, or its slug where the name holds none. */
function nameOccurrences(item: Item, reason: Reason | null): Occurrence[] {
  const artifact = item.artifact;
  if (artifact === null) return [];
  const inName = matches(artifact.name);
  const found = inName.length > 0 ? inName : matches(artifact.slug);
  return found.map((match) => ({
    start: match.start,
    end: match.end,
    byteStart: match.byteStart,
    inName: true,
    paragraph: null,
    indexInParagraph: 0,
    reason: reason ?? "name-or-slug",
    name: null,
    key: null,
  }));
}

/** Each check block of a text that names an old name of the map, whatever its gate (the report's
 *  own section, spec version 19). */
export function checkNames(
  markdown: Buffer,
  blocks: readonly Block[],
  entries: readonly MapEntry[]
): CheckName[] {
  return blocks
    .filter((block) => block.type === "check")
    .flatMap((block) =>
      renamedNamesIn(markdown.subarray(block.from, block.to).toString("utf8"), entries).map(
        (old) => ({ block: block.id, old })
      )
    );
}

/** The map name that covers an occurrence, and whether the item's project has not reached it. A
 *  Go test's name the map has no row for stands in as a name with no new name, which no project
 *  has reached. */
function nameGate(
  context: Context,
  project: string,
  text: string,
  start: number,
  end: number
): { readonly name: MapEntry | null; readonly notYet: boolean } {
  const mapped = nameAt(text, start, end, context.entries);
  const test = mapped === null ? testNameAt(text, start, end) : null;
  const name: MapEntry | null =
    mapped ?? (test === null ? null : { old: test, new: "", codeName: true, from: "merged" });
  const notYet = name !== null && (name.new === "" || !applies(name, context.reachOf(project)));
  return { name, notYet };
}

async function examineTitle(context: Context, item: Item, issue: IssueFacts): Promise<Examined> {
  const found = matches(issue.title);
  const empty = { item, text: issue.title, token: null, version: 0, blocks: [], checkNames: [] };
  if (found.length === 0) return { ...empty, occurrences: [], events: [] };
  const held = itemPlace(itemFacts(context, item, []));
  const events = held === null ? await issueEvents(context.reads, issue.key) : [];
  const author: Author = held === null ? titleAuthor(events, issue.createdBy) : "session";
  const paragraph: Paragraph = {
    start: 0,
    end: issue.title.length,
    text: issue.title,
    blockId: null,
  };
  const occurrences = found.map((match, index): Occurrence => {
    const { name, notYet } = nameGate(context, item.project, issue.title, match.start, match.end);
    return {
      start: match.start,
      end: match.end,
      byteStart: match.byteStart,
      inName: false,
      paragraph,
      indexInParagraph: index,
      reason:
        held ??
        occurrencePlace({
          inName: false,
          enclosing: [],
          inOpeningLine: false,
          author,
          nameNotYet: notYet,
        }),
      name,
      key: verdictKey(item.id, issue.title, index),
    };
  });
  return { ...empty, occurrences, events };
}

/** The paragraph of an occurrence: its innermost block, or its line when no block holds it. */
function paragraphOf(text: string, bytes: Buffer, enclosing: readonly Block[], start: number) {
  const innermost = enclosing[0];
  if (innermost !== undefined) {
    const from = indexOfByte(text, innermost.from);
    const paragraphText = bytes.subarray(innermost.from, innermost.to).toString("utf8");
    return {
      start: from,
      end: from + paragraphText.length,
      text: paragraphText,
      blockId: innermost.id,
    };
  }
  const lineStart = text.lastIndexOf("\n", start - 1) + 1;
  const lineEnd = text.indexOf("\n", start);
  const end = lineEnd === -1 ? text.length : lineEnd;
  return { start: lineStart, end, text: text.slice(lineStart, end), blockId: null };
}

async function examineDocument(
  context: Context,
  item: Item,
  artifact: Artifact
): Promise<Examined> {
  const read = Text.parse(await context.reads.text(artifact.id));
  const blocks = read.blocks ?? [];
  const latest = Math.max(0, ...(artifact.versions ?? []).map((version) => version.number));
  const version = read.version ?? latest;
  const text = read.markdown;
  const bytes = Buffer.from(text, "utf8");
  const held = itemPlace(itemFacts(context, item, blocks));
  const found = matches(text);
  const named =
    item.issue !== null && !item.issue.closed ? checkNames(bytes, blocks, context.entries) : [];
  const base = { item, text, token: read.token, version, blocks, checkNames: named, events: [] };
  const authorship = new Authorship(artifact.id, artifact.versions, context.reads);
  const perParagraph = new Map<number, number>();
  const occurrences: Occurrence[] = [];
  for (const match of found) {
    const enclosing = enclosingBlocks(blocks, match.byteStart, match.byteEnd);
    const paragraph = paragraphOf(text, bytes, enclosing, match.start);
    const indexInParagraph = perParagraph.get(paragraph.start) ?? 0;
    perParagraph.set(paragraph.start, indexInParagraph + 1);
    const { name, notYet } = nameGate(context, item.project, text, match.start, match.end);
    const facts = {
      inName: inArtifactSlug(text, match.start, match.end),
      enclosing,
      inOpeningLine: inOpeningLine(bytes, enclosing, match.byteStart),
      author: "session" as Author,
      nameNotYet: notYet,
    };
    // Who wrote the block is read only for an occurrence no cheaper place leaves, and before the
    // name's gate, which comes after it in the order of the places.
    let reason: Reason | null = held ?? occurrencePlace({ ...facts, nameNotYet: false });
    if (reason === null) {
      let author: Author = "session";
      if (authorship.suspect) {
        const sentence = sentenceAt(paragraph.text, match.start - paragraph.start);
        author = await authorship.authorOf({
          id: paragraph.blockId,
          text: paragraph.text,
          sentence: paragraph.text.slice(sentence.start, sentence.end),
        });
      }
      reason = occurrencePlace({ ...facts, author });
    }
    occurrences.push({
      start: match.start,
      end: match.end,
      byteStart: match.byteStart,
      inName: false,
      paragraph,
      indexInParagraph,
      reason,
      name,
      key: verdictKey(item.id, paragraph.text, indexInParagraph),
    });
  }
  return { ...base, occurrences: [...nameOccurrences(item, held), ...occurrences] };
}

/** One item read and its occurrences placed. */
export function examine(context: Context, item: Item): Promise<Examined> {
  if (item.artifact !== null) return examineDocument(context, item, item.artifact);
  if (item.issue === null) throw new Error("an item is a title or a document");
  return examineTitle(context, item, item.issue);
}
