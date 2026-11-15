import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { parseArgs } from "node:util";
import type { CaseKind, CaseRow } from "./build-corpus";
import {
  answeredTime,
  CannotRunError,
  cutTime,
  leadDocumentOf,
  listOf,
  newSeed,
  outDirectory,
  pool,
  randomStream,
  readCorpus,
  recordOf,
  stringOf,
  UsageError,
} from "./corpus-io";
import {
  type DispatchConfig,
  DispatchRefusedError,
  dispatchConfig,
  readJson,
} from "./dispatch-read";
import {
  type BacklogRoots,
  type DrawnSlices,
  type DrawRecord,
  drawPath,
  type IssueTrees,
  rootFactsOf,
  rootReadingOf,
  SLICES,
  type SliceName,
  slicesPath,
} from "./draw-io";
import {
  DEFAULT_NEVER_DECIDE_MODEL,
  type IssueFacts,
  type NeverDecideAnswer,
  type NeverDecideKind,
  type NeverDecideOptions,
  neverDecide,
  neverDecideDefinition,
} from "./never-decide";
import { type RenderGuard, renderChecked, renderDocument } from "./render";
import { replayCase } from "./replay";

/**
 * Draws the post-cut cases of a corpus into the four slices the pre-analysis plan's table names:
 * fit, report, anchor and reserve (dispatch://INFRA-2897/artifact/plan-of-analysis-md@v2, "The
 * slices").
 *
 * A case's group is its issue tree: the root of its issue's chain of parents, as Dispatch
 * answers it when the draw runs. A whole group goes into one slice, so two decisions of one
 * subject never land on opposite sides of a split: a document belongs to one issue, and a child
 * issue's design is read beside its parent's, so this grouping holds every case of a document,
 * of an issue and of a tree together (dispatch://INFRA-3189, "Data sets": "an issue tree" is
 * one of the things that can share information). A group whose size would take a slice past its
 * target goes to the next slice, which is why the targets are reached to within one group. The
 * draw is at random under a seed it prints, so the same seed over the same corpus and the same
 * trees draws the same slices. `draw.json` records the key the groups were taken under, how many
 * groups there were, and every parent it read, since a later reading of the slices turns on them.
 *
 * A group stands on one side of the cut whole, and the side is its first case's. A tree with a
 * case answered before the cut stands on the training side, so its cases answered after the cut
 * go to no slice: they are left out under `group-before-cut`, counted and named, because a case
 * in a scored slice whose own issue or tree has decisions on the training side is measured on a
 * subject the training already holds ("a group that spans the time cut goes whole to the set of
 * its first case, so grouping wins", the same section). A case whose tree Dispatch cannot answer
 * is left out the same way, under `tree-unknown`. Nothing here moves a training case: the side of a
 * case answered before the cut is the training side already.
 *
 * Four more kinds of case never reach the draw. A question offering fewer than two options
 * cannot be judged as a choice, and a question whose answer names no option cannot be scored
 * against what the person chose, since set equality with an empty answer is nothing an arm could
 * agree with.
 * A case the never-decide rule holds is one the shipped skill would refuse, and a coverage
 * number counting it would be larger than anything the skill can deliver; the counts and the
 * kinds held are printed and written. And a case whose answer from the rule could not be read
 * is left out as well, because a rule about what must not be decided may not decide by
 * accident; those are counted and named too.
 *
 * An approval case whose answer names no option stops the whole draw instead: every approval
 * case of this corpus carries the option the person chose, so one that does not is the corpus
 * read wrongly and not a case to pass over.
 *
 * What is pinned and what is live. The cases are the corpus's, and each document is read at the
 * version the corpus pins, so those are the same bytes on every run. Two readings are live, and
 * Dispatch answers each as it stands now. One is the list of roots carrying the `legion` label
 * that stand on the backlog or in icebox, which the never-decide rule needs: `draw.json` writes
 * that list, with the time it was read, beside its count. The other is the parent of every
 * issue the corpus names and of every issue above one, which the groups are taken over:
 * `draw.json` writes those whole, with the time they were read. `--backlog-roots <file>` and
 * `--issue-trees <file>` read each from a file in the shape `draw.json` writes instead of from
 * Dispatch, so a re-run that must reproduce an earlier draw passes `--seed`, `--backlog-roots
 * <that draw's draw.json>` and `--issue-trees <that draw's draw.json>` and then turns on the
 * corpus and the seed alone.
 *
 * The rule itself is a model call per case. `--cache <dir>` keeps those answers between runs,
 * keyed on the prose of the definition among other things, so a redraw of a frozen definition
 * pays for none of them again and a changed definition is asked afresh by construction.
 *
 * The anchor slice is written with a descriptor per case - the ask, the first document of the
 * request that could be read, the version it was asked about and the SHA-256 of that one
 * document's body as `renderDocument` renders it, the rest of the rendered case being outside
 * the digest; a case with no readable document is digested over its whole rendering. Nothing
 * here reads `anchor-descriptors.json` back: it is written for a later round, whose own digest
 * of a case differs as soon as the bytes it covers do.
 */

const USAGE = `usage: bun scripts/approval-eval/draw.ts --corpus <dir> --cut <date> --out <dir> \\
  [--seed <n>] [--backlog-roots <file>] [--issue-trees <file>] [--cache <dir>]

Draws the post-cut cases of a corpus into the fit, report, anchor and reserve slices.

Options:
  --corpus <dir>          Required. A directory holding corpus.jsonl and snapshot.json.
  --cut <date>            Required. An ISO 8601 time, for example 2026-11-05T00:00:00Z. A case
                          answered at or after it is drawn, unless its issue tree holds a case
                          answered before it; a case answered before it is training, and a
                          case of a tree with one is drawn into no slice.
  --out <dir>             Required. Where slices.json, anchor-descriptors.json and draw.json
                          are written. An absolute path outside this repository's workspace.
  --seed <n>              The seed of the draw. With none, one is made and printed.
  --backlog-roots <file>  The roots carrying the legion label that stand on the backlog or in
                          icebox, read from this file rather than from Dispatch. The shape is
                          the one draw.json writes, so an earlier draw's draw.json names its
                          own list. With --seed, this makes a re-run reproduce that draw from
                          the corpus alone.
  --issue-trees <file>    The parent of each issue the corpus names, read from this file rather
                          than from Dispatch. The shape is the one draw.json writes, so an
                          earlier draw's draw.json names its own trees. Every issue the corpus
                          names must be in it.
  --cache <dir>           Where the never-decide rule's answers are kept between runs. Without
                          it every case is asked of the model again.

Exit status: 0 when the draw was written, 2 when it could not run.`;

/** How many cases of each kind each slice is drawn for. The rest is the reserve. */
export interface SliceTargets {
  fit: number;
  report: number;
  anchor: number;
}

/** The targets of the plan's table, by kind of case. */
export const TARGETS: Readonly<Record<CaseKind, SliceTargets>> = {
  approval: { fit: 80, report: 250, anchor: 48 },
  question: { fit: 20, report: 50, anchor: 12 },
};

/** The kinds of case, in the order every count of them is held and printed. */
export const KINDS: readonly CaseKind[] = ["approval", "question"];

/** What a case is grouped by, which `draw.json` records and a later reading of it reads. */
export const GROUP_KEY = "issue tree";

/** How many option a question must offer to be judged as a choice at all. */
const LEAST_OPTIONS = 2;

/**
 * How many cases are read from Dispatch and put to the never-decide rule at once. The rule is
 * one model call per case, so this is what the whole draw's wall time turns on.
 */
const RENDER_CONCURRENCY = 16;

/** How many issues one page of the issue list asks for, which is that route's own maximum. */
const ISSUE_PAGE = 250;

/** How many issues are read at once when their parents are read, one issue per call. */
const TREE_CONCURRENCY = 16;

/** How many parents a chain is walked through before it is taken for a cycle. */
const TREE_DEPTH = 64;

/** How many case ids of each slice the command prints. */
const IDS_SHOWN = 10;

/**
 * Whether this draw holds the text it puts to the rule to `assertNoLeak`. One value for the
 * whole draw, read by the rendering that says what was done to its text and by the record that
 * writes the bound below, so a draw cannot carry a sentence saying the guard ran and a bound
 * asking for it. `NOT_HELD_TO_GUARD` in `render.ts` gives the reason this is `"none"`.
 */
const DRAW_GUARD: RenderGuard = "none";

/**
 * The bound this draw holds its own reading of the never-decide rule's answers under, which is
 * the bound of an unguarded draw. No branch can derive it, so it is written here and joined to
 * the sentence the rendering wrote of its text.
 */
const RENDERING_BOUND =
  "That holds while the question does: a reading that asks this rule what the person decided, " +
  "or that puts its answer into the agreement measurement, needs the text held to " +
  "`assertNoLeak` in full, both of its rules, first.";

/**
 * What `rule.renderingCheck` records: what this draw's renderings said was done to them, under
 * the bound above where no guard was run over them. The fact is the renderings' own, so a
 * change to what the draw does to the text it puts to the rule changes the record with it. A
 * draw that rendered nothing has no such fact and says that, rather than leaving the bound
 * standing over nothing, and a draw whose renderings were held to the guard has met that bound
 * already, so its sentences stand without it.
 */
export function renderingCheckOf(checks: readonly string[], guard: RenderGuard): string {
  if (checks.length === 0) return "No case was rendered in this draw, so no text was checked.";
  const said = checks.join(" ");
  return guard === "none" ? `${said} ${RENDERING_BOUND}` : said;
}

interface Flags {
  corpus: string;
  cut: string;
  out: string;
  seed: number | undefined;
  backlogRoots: string | undefined;
  issueTrees: string | undefined;
  cache: string | undefined;
}

/** One post-cut case as the draw was given it: the row, the rule's answer and its document. */
export interface DrawCase {
  row: CaseRow;
  /** What the never-decide rule said over this case's rendered evidence. */
  hold: NeverDecideAnswer;
  /** The first document of the approval request that could be read, and null where none was. */
  documentId: string | null;
  /** The version that document was asked about. */
  requestedVersion: number | null;
  /** The SHA-256 of that document as rendered, or of the rendered case where there is none. */
  sha256: string;
}

/** What a later round reads to prove it scored the same case over the same bytes. */
export interface AnchorDescriptor {
  caseId: string;
  askId: string;
  kind: CaseKind;
  documentId: string | null;
  requestedVersion: number | null;
  sha256: string;
}

/** Why a case the rule did not hold could still not be drawn. */
export type UndrawableKind =
  | "group-before-cut"
  | "tree-unknown"
  | "too-few-options"
  | "no-option-chosen"
  | "rule-unreadable";

/** The undrawable kinds, in the order they are counted and printed. */
const UNDRAWABLE_KINDS: readonly UndrawableKind[] = [
  "group-before-cut",
  "tree-unknown",
  "too-few-options",
  "no-option-chosen",
  "rule-unreadable",
];

/** What each undrawable kind is called where a count of it is printed. */
const UNDRAWABLE_NAMES: Readonly<Record<UndrawableKind, string>> = {
  "group-before-cut": "cases of trees from before the cut",
  "tree-unknown": "cases on trees Dispatch lacks",
  "too-few-options": "questions with too few options",
  "no-option-chosen": "questions answered with no option",
  "rule-unreadable": "unreadable never-decide answers",
};

/**
 * What the draw was given is not what it takes it for, so no draw over it would mean anything:
 * a corpus read wrongly, an issue list answering a root that is no root, or a chain of parents
 * that comes back to an issue it already passed.
 */
export class DrawError extends CannotRunError {
  override readonly name = "DrawError";
}

/** One case left out before the draw, and why. */
export interface ExcludedCase {
  caseId: string;
  kind: CaseKind;
  reason: string;
  /** The kind of hold, for a case the never-decide rule held. */
  heldKind?: NeverDecideKind;
  /** Which rule left it out, for a case no arm could be scored on. */
  undrawableKind?: UndrawableKind;
}

/** What one draw did: the slices, the counts they reached and what never reached them. */
export interface Draw {
  seed: number;
  targets: Readonly<Record<CaseKind, SliceTargets>>;
  slices: DrawnSlices;
  counts: Record<SliceName, Record<CaseKind, number>>;
  /** What the cases were grouped by, a whole group going into one slice. */
  groupKey: string;
  /** How many groups there were under that key. */
  groupCount: number;
  /** How many of those groups hold a case of each kind, a group being able to hold both. */
  groups: Record<CaseKind, number>;
  held: ExcludedCase[];
  heldByKind: Record<string, number>;
  undrawable: ExcludedCase[];
  undrawableByKind: Record<UndrawableKind, number>;
  anchors: AnchorDescriptor[];
}

/** One group of cases, which goes into one slice whole. */
interface Group {
  key: string;
  /** How many cases of each kind the group holds, which is what a slice must have room for. */
  counts: Record<CaseKind, number>;
  cases: DrawCase[];
}

/** `items` in a random order under `seed`, by a Fisher-Yates walk of that one stream. */
function shuffled<T>(items: readonly T[], seed: number): T[] {
  const order = [...items];
  const random = randomStream(seed);
  for (let index = order.length - 1; index > 0; index -= 1) {
    const swap = Math.floor(random() * (index + 1));
    const here = order[index];
    const there = order[swap];
    if (here === undefined || there === undefined) continue;
    order[index] = there;
    order[swap] = here;
  }
  return order;
}

/** Each issue's parent by key, null for a root: what the groups are taken over. */
export type IssueParents = Readonly<Record<string, string | null>>;

/** What `splitAtCut` reads of a tree reading: the parents, and the issues Dispatch lacked. */
export type TreeReading = Pick<IssueTrees, "parents" | "missing">;

/**
 * The issues from `issueKey` up to the root of its tree, in that order: the issue, its parent,
 * that issue's parent, and so on. An issue `parents` does not name ends the chain; the command
 * reads every issue the corpus names and every issue above one, so that is only ever an issue
 * Dispatch answered 404 for, which `splitAtCut` keeps out of the scored slices. A chain that
 * comes back to an issue it passed, or runs past `TREE_DEPTH` parents, stops the draw: no group
 * taken over it would mean anything.
 */
export function chainOf(issueKey: string, parents: IssueParents): string[] {
  const chain = [issueKey];
  for (;;) {
    const at = chain[chain.length - 1] as string;
    const parent = Object.hasOwn(parents, at) ? (parents[at] ?? null) : null;
    if (parent === null) return chain;
    if (chain.includes(parent) || chain.length > TREE_DEPTH) {
      throw new DrawError(
        `the chain of parents of ${issueKey} comes back to ${parent} or runs past ` +
          `${TREE_DEPTH} issues, so its tree has no root`
      );
    }
    chain.push(parent);
  }
}

/** The root of an issue's tree: the last issue of its chain of parents. */
export function treeRootOf(issueKey: string, parents: IssueParents): string {
  return chainOf(issueKey, parents).at(-1) as string;
}

/**
 * The group a case belongs to: its issue tree, whether it is an approval request or a question.
 * A document belongs to one issue and an issue to one tree, so this key holds together every
 * case one document or one issue held together, and the cases of a parent and its children
 * besides. An approval case naming no issue is grouped by its lead document, so the versions of
 * one document stay together; any other case naming no issue is its own group, since nothing
 * else is known to share it.
 */
export function groupKeyOf(row: CaseRow, parents: IssueParents): string {
  if (row.issueKey === null) {
    const lead = leadDocumentOf(row);
    return lead === null ? `ask:${row.askId}` : `document:${lead.artifactId}`;
  }
  return `${GROUP_KEY}:${treeRootOf(row.issueKey, parents)}`;
}

/** Which of two cases was answered first, by time and then by case id, so a tie has one answer. */
function answeredBefore(one: CaseRow, other: CaseRow): boolean {
  const [oneAt, otherAt] = [answeredTime(one), answeredTime(other)];
  return oneAt < otherAt || (oneAt === otherAt && one.caseId < other.caseId);
}

/**
 * The cases answered at or after the cut that the draw may take, and the ones it leaves out.
 * `rows` is the whole corpus, both sides of the cut, because which side a group stands on is its
 * first case's.
 *
 * - A case whose group holds a case answered before the cut stands with its group on the training
 *   side, so it reaches no slice; it is named, with the group's earliest case, as
 *   `group-before-cut`. It joins no training input either: retrieval and distillation read only
 *   cases answered before the cut (`evidenceFor` in `split.ts`), and reading such a case there is
 *   a change to every arm's recipe, which is a round's to decide and not the draw's.
 * - A case whose chain of parents passes an issue Dispatch answered 404 for has a tree nobody can
 *   read, so it may share a group with a training case and reaches no slice either, as
 *   `tree-unknown`.
 *
 * An approval case answered after the cut that names no option stops the split, as it stops
 * `drawSlices`: leaving it out under a tree would pass over a corpus read wrongly.
 */
export function splitAtCut(
  rows: readonly CaseRow[],
  cut: number,
  trees: TreeReading
): { post: CaseRow[]; leftOut: ExcludedCase[] } {
  const missing = new Set(trees.missing);
  const firstBefore = new Map<string, CaseRow>();
  for (const row of rows) {
    if (answeredTime(row) >= cut) continue;
    const key = groupKeyOf(row, trees.parents);
    const known = firstBefore.get(key);
    if (known === undefined || answeredBefore(row, known)) firstBefore.set(key, row);
  }
  const post: CaseRow[] = [];
  const leftOut: ExcludedCase[] = [];
  for (const row of rows) {
    if (answeredTime(row) < cut) continue;
    if (row.kind === "approval" && row.selected.length === 0) {
      throw new DrawError(
        `the approval case ${row.caseId} names no option the person chose, which the corpus ` +
          "build cannot produce: the draw stops rather than passing over it"
      );
    }
    const lacked =
      row.issueKey === null
        ? undefined
        : chainOf(row.issueKey, trees.parents).find((key) => missing.has(key));
    if (lacked !== undefined) {
      leftOut.push({
        caseId: row.caseId,
        kind: row.kind,
        reason:
          `Dispatch answered 404 for ${lacked}, on the chain of parents of ${row.issueKey}, so ` +
          "its tree cannot be read and it may share one with a training case",
        undrawableKind: "tree-unknown",
      });
      continue;
    }
    const key = groupKeyOf(row, trees.parents);
    const first = firstBefore.get(key);
    if (first === undefined) {
      post.push(row);
      continue;
    }
    leftOut.push({
      caseId: row.caseId,
      kind: row.kind,
      reason:
        `its ${key} holds the case ${first.caseId}, answered at ${first.answeredAt}, before ` +
        "the cut, so the group stands on the training side",
      undrawableKind: "group-before-cut",
    });
  }
  return { post, leftOut };
}

/** The slices and the counts each starts at. */
function emptyDraw(seed: number): Draw {
  const counts = {} as Record<SliceName, Record<CaseKind, number>>;
  const slices = {} as DrawnSlices;
  for (const slice of SLICES) {
    counts[slice] = { approval: 0, question: 0 };
    slices[slice] = [];
  }
  return {
    seed,
    targets: TARGETS,
    slices,
    counts,
    groupKey: GROUP_KEY,
    groupCount: 0,
    groups: { approval: 0, question: 0 },
    held: [],
    heldByKind: {},
    undrawable: [],
    undrawableByKind: {
      "group-before-cut": 0,
      "tree-unknown": 0,
      "too-few-options": 0,
      "no-option-chosen": 0,
      "rule-unreadable": 0,
    },
    anchors: [],
  };
}

/**
 * The draw itself: the cases the rule holds and the questions no arm could be scored on are left
 * out, the rest are grouped by their issue tree under `parents`, the groups are shuffled under
 * `seed`, and each group goes into the first slice its whole size still fits. `cases` are the
 * ones `splitAtCut` let through, so no group here has a case on the training side.
 *
 * It throws `DrawError` over an approval case whose answer names no option, which is the corpus
 * read wrongly rather than a case to leave out.
 */
export function drawSlices(cases: readonly DrawCase[], seed: number, parents: IssueParents): Draw {
  const draw = emptyDraw(seed);
  const drawable: DrawCase[] = [];
  const leaveOut = (row: CaseRow, undrawableKind: UndrawableKind, reason: string): void => {
    draw.undrawable.push({ caseId: row.caseId, kind: row.kind, reason, undrawableKind });
    draw.undrawableByKind[undrawableKind] += 1;
  };
  for (const drawCase of cases) {
    const row = drawCase.row;
    if (row.kind === "approval" && row.selected.length === 0) {
      throw new DrawError(
        `the approval case ${row.caseId} names no option the person chose, which the corpus ` +
          "build cannot produce: the draw stops rather than passing over it"
      );
    }
    if (drawCase.hold.held) {
      const heldKind = drawCase.hold.kind;
      draw.held.push({
        caseId: row.caseId,
        kind: row.kind,
        reason: drawCase.hold.reason,
        ...(heldKind === null ? {} : { heldKind }),
      });
      const name = heldKind ?? "unnamed";
      draw.heldByKind[name] = (draw.heldByKind[name] ?? 0) + 1;
      continue;
    }
    if (drawCase.hold.fault !== null) {
      // Neither held nor decided: a rule about what must not be decided may not decide by
      // accident, so the case leaves the draw and is named with what came back.
      leaveOut(
        row,
        "rule-unreadable",
        `the never-decide answer could not be read: ${drawCase.hold.fault}`
      );
      continue;
    }
    if (row.kind === "question" && row.options.length < LEAST_OPTIONS) {
      leaveOut(
        row,
        "too-few-options",
        `a question offering ${row.options.length} options cannot be judged as a choice`
      );
      continue;
    }
    if (row.kind === "question" && row.selected.length === 0) {
      leaveOut(
        row,
        "no-option-chosen",
        "a question the person answered with no option cannot be scored by set equality with " +
          "what they chose"
      );
      continue;
    }
    drawable.push(drawCase);
  }
  const groups = new Map<string, Group>();
  // Sorted by case id, then grouped by sorted key: the draw turns on the seed alone and not on
  // the order `corpus.jsonl` happens to hold its cases in.
  for (const drawCase of [...drawable].sort((one, other) =>
    one.row.caseId < other.row.caseId ? -1 : 1
  )) {
    const key = groupKeyOf(drawCase.row, parents);
    const group = groups.get(key);
    if (group === undefined) {
      groups.set(key, {
        key,
        counts: { approval: 0, question: 0, [drawCase.row.kind]: 1 },
        cases: [drawCase],
      });
    } else {
      group.counts[drawCase.row.kind] += 1;
      group.cases.push(drawCase);
    }
  }
  const ordered = [...groups.values()].sort((one, other) => (one.key < other.key ? -1 : 1));
  for (const group of shuffled(ordered, seed)) {
    draw.groupCount += 1;
    for (const kind of KINDS) if (group.counts[kind] > 0) draw.groups[kind] += 1;
    let taken: SliceName = "reserve";
    for (const slice of SLICES) {
      if (slice === "reserve") break;
      // A group holding both kinds goes where both still fit: one group, one slice, whole.
      const fits = KINDS.every(
        (kind) => group.counts[kind] <= TARGETS[kind][slice] - draw.counts[slice][kind]
      );
      if (fits) {
        taken = slice;
        break;
      }
    }
    for (const kind of KINDS) draw.counts[taken][kind] += group.counts[kind];
    for (const drawCase of group.cases) {
      draw.slices[taken].push(drawCase.row.caseId);
      if (taken !== "anchor") continue;
      draw.anchors.push({
        caseId: drawCase.row.caseId,
        askId: drawCase.row.askId,
        kind: drawCase.row.kind,
        documentId: drawCase.documentId,
        requestedVersion: drawCase.requestedVersion,
        sha256: drawCase.sha256,
      });
    }
  }
  return draw;
}

/**
 * Every issue carrying the `legion` label that stands on the backlog or in icebox, read now.
 * That is the fact the never-decide rule's waiting-root kind needs and the rendered case does
 * not carry; it is read here, in the command, and handed to the rule as a value. Dispatch's own
 * list answers it in one page per status, which is why no case is read for on its own. The list
 * is sorted by key, so what `draw.json` writes of one reading is the same bytes however the
 * pages came back.
 *
 * An entry of a page that is no root stops the draw, as every reading of this shape does
 * (`rootReadingOf` in `draw-io.ts`). This loop used to pass over an entry whose key or status
 * it could not read, which is the drop that policy repairs: such a root is one the never-decide
 * rule is never told about, and the draw would record a list shorter than the one Dispatch
 * answered without saying so.
 */
async function backlogLegionRoots(config: DispatchConfig): Promise<BacklogRoots> {
  const issues: Array<{ key: string } & IssueFacts> = [];
  const seen = new Set<string>();
  for (const status of ["backlog", "icebox"]) {
    for (let offset = 0; ; offset += ISSUE_PAGE) {
      const path = `/api/v1/issues?label=legion&status=${status}&limit=${ISSUE_PAGE}&offset=${offset}`;
      const answer = recordOf(await readJson<unknown>(path, config));
      const page = listOf(answer?.issues);
      for (const [at, value] of page.entries()) {
        const read = rootReadingOf(value);
        if (read.root === null) {
          throw new DrawError(
            `the ${status} issue list root ${read.key ?? offset + at + 1} ${read.fault}`
          );
        }
        if (seen.has(read.root.key)) continue;
        seen.add(read.root.key);
        issues.push(read.root);
      }
      const total = answer?.total;
      if (page.length < ISSUE_PAGE || typeof total !== "number" || offset + ISSUE_PAGE >= total) {
        break;
      }
    }
  }
  issues.sort((one, other) => (one.key < other.key ? -1 : 1));
  return { readAt: new Date().toISOString(), source: "dispatch", issues };
}

/**
 * The same list, pinned: read from a file in the shape `draw.json` writes, which is either that
 * whole file or the `backlogLegionRoots` object inside it. The reading time the file carries is
 * kept, because it is when the list was true; the source becomes the file.
 *
 * The roots themselves are read by `rootReadingOf`, the one reader of that shape, which checks
 * every field and refuses an entry holding one that is no such field. What this adds is its own
 * words for that refusal, and it keeps them rather than throwing the reader's: what it reads is
 * not a corpus's own draw but a file a person named on the command line, so every way it can
 * fail is a usage error about `--backlog-roots` and says what that flag takes. Answering a
 * corpus error over a flag would be a refusal that has lost its words.
 */
export async function pinnedBacklogRoots(path: string): Promise<BacklogRoots> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(await readFile(path, "utf8"));
  } catch {
    throw new UsageError(`--backlog-roots ${path} could not be read as JSON`);
  }
  const record = recordOf(parsed);
  const held = recordOf(record?.backlogLegionRoots) ?? record;
  const listed = listOf(held?.issues);
  if (held === null || listed.length === 0) {
    throw new UsageError(
      `--backlog-roots ${path} names no such roots: it takes the shape draw.json writes, ` +
        "which is an object with `issues` or a whole draw.json holding `backlogLegionRoots`"
    );
  }
  const issues: Array<{ key: string } & IssueFacts> = [];
  for (const [at, value] of listed.entries()) {
    const read = rootReadingOf(value);
    if (read.root === null) {
      throw new UsageError(
        `--backlog-roots ${path} root ${read.key ?? at + 1} ${read.fault} (it takes the shape ` +
          "draw.json writes, whose every root names a key and a status that are strings, and " +
          "labels that are a list of strings)"
      );
    }
    issues.push(read.root);
  }
  issues.sort((one, other) => (one.key < other.key ? -1 : 1));
  return { readAt: stringOf(held.readAt) ?? "unknown", source: path, issues };
}

/**
 * The parents in key order, so what `draw.json` writes of one reading is the same bytes. Built
 * by `Object.fromEntries`, which defines each key as the record's own, `__proto__` included.
 */
function sortedParents(parents: ReadonlyMap<string, string | null>): Record<string, string | null> {
  return Object.fromEntries([...parents].sort(([one], [other]) => (one < other ? -1 : 1)));
}

/**
 * The parent of every issue in `keys` and of every issue above one, read now, one issue per call
 * of `GET /api/v1/issues/<key>`, `TREE_CONCURRENCY` at a time, a level of parents per pass until
 * every chain reaches a root. An issue Dispatch answers 404 for is no longer there to say whose
 * child it was. Its tree cannot be read, so it is listed under `missing`, where `splitAtCut`
 * keeps every case whose chain passes it out of the slices; its own entry is null so the chain
 * ends there. Any other refusal, and an answer naming another issue or a parent that is no key,
 * stops the draw.
 */
async function readIssueTrees(
  keys: readonly string[],
  config: DispatchConfig
): Promise<IssueTrees> {
  const parents = new Map<string, string | null>();
  const missing: string[] = [];
  let wanted = [...new Set(keys)];
  while (wanted.length > 0) {
    await pool(wanted, TREE_CONCURRENCY, async (key) => {
      let answer: unknown;
      try {
        answer = await readJson<unknown>(`/api/v1/issues/${encodeURIComponent(key)}`, config);
      } catch (error) {
        if (!(error instanceof DispatchRefusedError) || error.status !== 404) throw error;
        parents.set(key, null);
        missing.push(key);
        return;
      }
      const issue = recordOf(answer);
      const parent = issue?.parent ?? null;
      if (
        issue?.key !== key ||
        (parent !== null && (typeof parent !== "string" || parent === ""))
      ) {
        throw new DrawError(
          `Dispatch answered the issue ${key} with the key ${String(issue?.key)} and the parent ` +
            `${JSON.stringify(parent)}, which is no issue and its parent's key`
        );
      }
      parents.set(key, parent);
    });
    wanted = [
      ...new Set(
        [...parents.values()].filter(
          (parent): parent is string => parent !== null && !parents.has(parent)
        )
      ),
    ];
  }
  return {
    readAt: new Date().toISOString(),
    source: "dispatch",
    parents: sortedParents(parents),
    missing: missing.sort(),
  };
}

/**
 * The same trees, pinned: read from a file in the shape `draw.json` writes, which is either that
 * whole file or the `issueTrees` object inside it. The reading time the file carries is kept,
 * because it is when the parents were true; the source becomes the file. Every parent it names
 * must be an issue it names, so every chain ends inside it; every issue in `keys` must be there,
 * since an issue it leaves out would be drawn as a tree of its own whatever its parent is; and
 * every issue it lists as `missing` must be one it names with no parent. Each way it can fail is
 * a usage error about `--issue-trees`, as with `--backlog-roots`.
 */
export async function pinnedIssueTrees(path: string, keys: readonly string[]): Promise<IssueTrees> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(await readFile(path, "utf8"));
  } catch {
    throw new UsageError(`--issue-trees ${path} could not be read as JSON`);
  }
  const record = recordOf(parsed);
  const held = recordOf(record?.issueTrees) ?? record;
  const listed = recordOf(held?.parents);
  if (held === null || listed === null) {
    throw new UsageError(
      `--issue-trees ${path} names no trees: it takes the shape draw.json writes, which is an ` +
        "object with `parents` or a whole draw.json holding `issueTrees`"
    );
  }
  const parents = new Map<string, string | null>();
  for (const [key, parent] of Object.entries(listed)) {
    if (parent !== null && (typeof parent !== "string" || parent === "")) {
      throw new UsageError(
        `--issue-trees ${path} gives ${key} the parent ${JSON.stringify(parent)}, which is ` +
          "neither an issue's key nor null"
      );
    }
    parents.set(key, parent);
  }
  for (const [key, parent] of parents) {
    if (parent !== null && !parents.has(parent)) {
      throw new UsageError(
        `--issue-trees ${path} names ${parent} as the parent of ${key} and gives no parent of ` +
          `${parent}, so the tree of ${key} does not end inside it`
      );
    }
  }
  const absent = keys.filter((key) => !parents.has(key));
  if (absent.length > 0) {
    throw new UsageError(
      `--issue-trees ${path} names no parent for ${absent.length} issue(s) the corpus names, ` +
        `the first ${absent[0]}`
    );
  }
  const missing: string[] = [];
  for (const key of listOf(held.missing)) {
    if (typeof key !== "string" || parents.get(key) !== null) {
      throw new UsageError(
        `--issue-trees ${path} lists ${JSON.stringify(key)} as missing, which is no issue it ` +
          "names with no parent"
      );
    }
    missing.push(key);
  }
  return {
    readAt: stringOf(held.readAt) ?? "unknown",
    source: path,
    parents: sortedParents(parents),
    missing: missing.sort(),
  };
}

/** One post-cut case rendered and put to the rule, which is what the draw is given. */
async function drawCaseOf(
  row: CaseRow,
  issues: ReadonlyMap<string, IssueFacts>,
  config: DispatchConfig,
  options: NeverDecideOptions
): Promise<{ drawCase: DrawCase; unread: number; check: string }> {
  const replayed = row.kind === "approval" ? await replayCase(row, config) : null;
  const documents = replayed?.documents ?? [];
  // `DRAW_GUARD` is this draw's one guard choice, a decision and not an oversight:
  // `NOT_HELD_TO_GUARD` in `render.ts` holds the reason, rule by rule, and `check` is the
  // sentence the rendering wrote of what was done to its text.
  const { text, check } = renderChecked(row, documents, DRAW_GUARD);
  // The descriptor's digest is over the first document of the request that could be read, as
  // `renderDocument` renders it, which is that document's own body inside the case rendered
  // above and not its section: the heading above it, the head of the rendering and every
  // other document are outside the digest, so a case whose rendering changes only there digests
  // the same. A case with no readable document is digested whole, over the rendered case. It is
  // the first readable document and not `leadDocumentOf`, which is the first the request names
  // an artifact and a version for: a digest of a document that could not be read is no record
  // of what was drawn, so an unread document is passed over.
  const first = documents.find((document) => document.markdown !== null) ?? null;
  const digested =
    first === null || first.markdown === null
      ? text
      : renderDocument(first.markdown, row.answeredAt);
  const sha256 = createHash("sha256").update(digested, "utf8").digest("hex");
  const issue = row.issueKey === null ? null : (issues.get(row.issueKey) ?? null);
  return {
    drawCase: {
      row,
      hold: await neverDecide(
        {
          text,
          row,
          issue,
          documentId: first?.artifactId ?? null,
          documentVersion: first?.requestedVersion ?? null,
        },
        options
      ),
      documentId: first?.artifactId ?? null,
      requestedVersion: first?.requestedVersion ?? null,
      sha256,
    },
    unread: documents.filter((document) => document.refusal !== null).length,
    check,
  };
}

/** The post-cut cases, rendered and put to the rule, `RENDER_CONCURRENCY` of them at a time. */
async function drawCasesOf(
  rows: readonly CaseRow[],
  issues: ReadonlyMap<string, IssueFacts>,
  config: DispatchConfig,
  options: NeverDecideOptions
): Promise<{ cases: DrawCase[]; unread: number; checks: string[] }> {
  const cases = new Array<DrawCase>(rows.length);
  // What the renderings said was done to them, each sentence once and in no worker's order, so
  // the record a draw writes does not turn on which worker got there first.
  const checks = new Set<string>();
  let unread = 0;
  // Over the indices, so each case is written back where it was read from and the order of
  // `cases` is the order of `rows`, whichever worker got there first.
  await pool([...rows.keys()], RENDER_CONCURRENCY, async (index) => {
    const row = rows[index] as CaseRow;
    const one = await drawCaseOf(row, issues, config, options);
    cases[index] = one.drawCase;
    checks.add(one.check);
    unread += one.unread;
  });
  return { cases, unread, checks: [...checks].sort() };
}

function readFlags(argv: readonly string[]): Flags {
  let values: {
    corpus?: string;
    cut?: string;
    out?: string;
    seed?: string;
    "backlog-roots"?: string;
    "issue-trees"?: string;
    cache?: string;
  };
  try {
    ({ values } = parseArgs({
      args: [...argv],
      options: {
        corpus: { type: "string" },
        cut: { type: "string" },
        out: { type: "string" },
        seed: { type: "string" },
        "backlog-roots": { type: "string" },
        "issue-trees": { type: "string" },
        cache: { type: "string" },
      },
      strict: true,
      allowPositionals: false,
    }));
  } catch {
    // parseArgs names what it refused, so its message is never repeated.
    throw new UsageError(USAGE);
  }
  const corpus = values.corpus?.trim();
  const cut = values.cut?.trim();
  const out = values.out?.trim();
  if (!corpus || !cut || !out) throw new UsageError(USAGE);
  let seed: number | undefined;
  if (values.seed !== undefined) {
    seed = Number(values.seed.trim());
    if (!Number.isSafeInteger(seed) || seed < 0) {
      throw new UsageError(`--seed takes a whole number, zero or above\n\n${USAGE}`);
    }
  }
  return {
    corpus,
    cut,
    out,
    seed,
    backlogRoots: values["backlog-roots"]?.trim() || undefined,
    issueTrees: values["issue-trees"]?.trim() || undefined,
    cache: values.cache?.trim() || undefined,
  };
}

function countsTable(draw: Draw): string {
  const lines = [`  ${"slice".padEnd(10)}${"approval".padStart(10)}${"question".padStart(10)}`];
  for (const slice of SLICES) {
    const counts = draw.counts[slice];
    lines.push(
      `  ${slice.padEnd(10)}${String(counts.approval).padStart(10)}` +
        `${String(counts.question).padStart(10)}`
    );
  }
  for (const kind of ["approval", "question"] as const) {
    const targets = TARGETS[kind];
    lines.push(
      `  target ${kind.padEnd(10)}fit ${targets.fit}, report ${targets.report}, ` +
        `anchor ${targets.anchor}, reserve the rest`
    );
  }
  return lines.join("\n");
}

async function draw(flags: Flags): Promise<number> {
  const corpus = await readCorpus(flags.corpus);
  const directory = await outDirectory(flags.out);
  const cut = cutTime(flags.cut);
  const config = await dispatchConfig();
  const post = corpus.rows.filter((row) => answeredTime(row) >= cut);
  const seed = flags.seed ?? newSeed();
  console.log(`corpus            ${corpus.directory}`);
  console.log(`cut               ${new Date(cut).toISOString()}`);
  console.log(`post-cut cases    ${post.length}`);
  console.log(`seed              ${seed} (--seed ${seed} draws these slices again)`);
  const roots =
    flags.backlogRoots === undefined
      ? await backlogLegionRoots(config)
      : await pinnedBacklogRoots(flags.backlogRoots);
  const issues = rootFactsOf(roots.issues);
  console.log(`legion roots on the backlog or in icebox  ${roots.issues.length}`);
  console.log(
    roots.source === "dispatch"
      ? `                  from Dispatch at ${roots.readAt}`
      : `                  pinned from ${roots.source}, read at ${roots.readAt}`
  );
  const issueKeys = [
    ...new Set(corpus.rows.flatMap((row) => (row.issueKey === null ? [] : [row.issueKey]))),
  ].sort();
  const trees =
    flags.issueTrees === undefined
      ? await readIssueTrees(issueKeys, config)
      : await pinnedIssueTrees(flags.issueTrees, issueKeys);
  const treeCount = new Set(issueKeys.map((key) => treeRootOf(key, trees.parents))).size;
  console.log(
    `issue trees       ${treeCount} over the ${issueKeys.length} issues the corpus names, ` +
      `${Object.keys(trees.parents).length} issues read, ${trees.missing.length} not found`
  );
  console.log(
    trees.source === "dispatch"
      ? `                  from Dispatch at ${trees.readAt}`
      : `                  pinned from ${trees.source}, read at ${trees.readAt}`
  );
  const split = splitAtCut(corpus.rows, cut, trees);
  const leftOut = (kind: UndrawableKind): number =>
    split.leftOut.filter((one) => one.undrawableKind === kind).length;
  console.log(`of a tree with a case before the cut, so training  ${leftOut("group-before-cut")}`);
  console.log(`on a tree Dispatch could not answer                ${leftOut("tree-unknown")}`);
  const definition = await neverDecideDefinition();
  const ruleOptions: NeverDecideOptions = {
    model: DEFAULT_NEVER_DECIDE_MODEL,
    cacheDirectory: flags.cache ?? null,
    definition,
  };
  console.log(`the rule          ${DEFAULT_NEVER_DECIDE_MODEL}, one call per case`);
  console.log(
    `cache             ${flags.cache ?? "none, so every case is asked of the model again"}`
  );
  const rendered = await drawCasesOf(split.post, issues, config, ruleOptions);
  const drawn = drawSlices(rendered.cases, seed, trees.parents);
  // The cases `splitAtCut` left out were never rendered or put to the rule, so they join the
  // undrawable ones here, first, under their own kinds.
  drawn.undrawable = [...split.leftOut, ...drawn.undrawable];
  drawn.undrawableByKind["group-before-cut"] = leftOut("group-before-cut");
  drawn.undrawableByKind["tree-unknown"] = leftOut("tree-unknown");
  console.log("");
  console.log(countsTable(drawn));
  console.log("");
  console.log(
    `groups            ${drawn.groupCount} under the key ${drawn.groupKey}, of which ` +
      `${drawn.groups.approval} hold an approval case and ${drawn.groups.question} a question`
  );
  console.log(`held by the never-decide rule  ${drawn.held.length}`);
  for (const [kind, count] of Object.entries(drawn.heldByKind).sort()) {
    console.log(`  ${kind.padEnd(22)}${count}`);
  }
  console.log(`undrawable                     ${drawn.undrawable.length}`);
  for (const kind of UNDRAWABLE_KINDS) {
    console.log(`  ${UNDRAWABLE_NAMES[kind].padEnd(35)}${drawn.undrawableByKind[kind]}`);
  }
  const holds = rendered.cases.map((one) => one.hold);
  const cached = holds.filter((one) => one.source === "cache").length;
  const asked = holds.filter((one) => one.source === "model").length;
  console.log(
    `the rule's calls  ${asked} asked, ${cached} from the cache, ` +
      `${holds.length - asked - cached} settled by the issue's facts`
  );
  console.log(
    `the rule's tokens ${holds.reduce((total, one) => total + (one.inputTokens ?? 0), 0)} read, ` +
      `${holds.reduce((total, one) => total + (one.outputTokens ?? 0), 0)} written`
  );
  console.log(`documents that could not be read ${rendered.unread}`);
  console.log("");
  for (const slice of SLICES) {
    const ids = drawn.slices[slice].slice(0, IDS_SHOWN);
    console.log(`first ${ids.length} of ${slice}`);
    for (const id of ids) console.log(`  ${id}`);
  }
  await mkdir(directory, { recursive: true });
  const write = async (path: string, value: unknown): Promise<void> => {
    await writeFile(path, `${JSON.stringify(value, null, 2)}\n`, "utf8");
  };
  const anchors = join(directory, "anchor-descriptors.json");
  await write(slicesPath(directory), drawn.slices);
  await write(anchors, drawn.anchors);
  const record: DrawRecord = {
    corpus: corpus.directory,
    cut: new Date(cut).toISOString(),
    seed,
    targets: drawn.targets,
    counts: drawn.counts,
    groupKey: drawn.groupKey,
    groupCount: drawn.groupCount,
    groups: drawn.groups,
    postCutCases: post.length,
    documentsUnread: rendered.unread,
    // The two inputs of the draw that are not the corpus. The labelled roots on the backlog or
    // in icebox, which the never-decide rule reads, and the parent of every issue the corpus
    // names, which the groups are taken over. Each is written whole, with the time it was read,
    // so `--backlog-roots <this file> --issue-trees <this file>` draws these slices again
    // whatever Dispatch shows later.
    backlogLegionRootsRead: roots.issues.length,
    backlogLegionRoots: roots,
    issueTreesRead: Object.keys(trees.parents).length,
    issueTrees: trees,
    // What the rule was, for this draw: the prose it applied, what applying it cost, and what
    // was checked of the text it was applied to. A change of one byte of that prose changes every
    // cache key, so a later draw over the same corpus and seed with another definition is a
    // different reading and says so here.
    rule: {
      model: DEFAULT_NEVER_DECIDE_MODEL,
      definitionSha256: createHash("sha256").update(definition, "utf8").digest("hex"),
      cache: flags.cache ?? null,
      asked,
      cached,
      inputTokens: holds.reduce((total, one) => total + (one.inputTokens ?? 0), 0),
      outputTokens: holds.reduce((total, one) => total + (one.outputTokens ?? 0), 0),
      renderingCheck: renderingCheckOf(rendered.checks, DRAW_GUARD),
    },
    excluded: {
      held: { total: drawn.held.length, byKind: drawn.heldByKind, cases: drawn.held },
      undrawable: {
        total: drawn.undrawable.length,
        byKind: drawn.undrawableByKind,
        cases: drawn.undrawable,
      },
    },
  };
  await write(drawPath(directory), record);
  console.log("");
  console.log(`written           ${slicesPath(directory)}`);
  console.log(`                  ${anchors}`);
  console.log(`                  ${drawPath(directory)}`);
  return 0;
}

async function main(argv: readonly string[]): Promise<number> {
  let flags: Flags;
  try {
    flags = readFlags(argv);
  } catch (error) {
    if (!(error instanceof UsageError)) throw error;
    console.error(error.message);
    return 2;
  }
  try {
    return await draw(flags);
  } catch (error) {
    if (error instanceof CannotRunError) {
      console.error(`${error.name}: ${error.message}`);
      return 2;
    }
    throw error;
  }
}

// Only when run: a test that imports `drawSlices` must not run a command.
if (import.meta.main) process.exit(await main(Bun.argv.slice(2)));
