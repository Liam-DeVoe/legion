import type { Browser, BrowserContext, Page } from "@playwright/test";

import {
  FOOT_HEIGHT_PROPERTY,
  IDENTITY_HEIGHT_PROPERTY,
} from "../web/src/features/issue-page/identity-block";
import { presetPath } from "../web/src/features/issue-page/issue-address";
import { firstLineOf } from "../web/src/features/issue-page/issue-tree";
import { KEY_REPORTS } from "../web/src/features/issue-page/page-structure";
import {
  CONTROL_LABELS,
  DECLARED_WORDS,
  GLYPH_NAMES,
  GLYPH_TOKENS,
  NODE_TYPE_NAMES,
  type NodeType,
  nodeStateWords,
  PRESET_NAMES,
  PRESET_ORDER,
  wordsIn,
} from "../web/src/features/issue-page/page-words";
import { blockNodeId, checkState } from "../web/src/features/issue-page/tree-drafts";
import { DRAWN_LEVELS } from "../web/src/features/issue-page/tree-fold";
import { REF_PREVIEW_OPEN_DELAY_MS } from "../web/src/features/refs/ref-preview-timing";
import {
  answerAsk,
  claimIssue,
  createAsk,
  createComment,
  createIssue,
  createMessage,
  getArtifact,
  getAsk,
  getIssue,
  getIssueCoverage,
  getIssueDecisions,
  getIssueResults,
  listComments,
  listIssueAsks,
  listIssueMessages,
  listMyReplies,
  patchIssue,
} from "./api";
import { countDocumentSockets, cursorLabel } from "./editor";
import { nextFrames } from "./frames";
import {
  seedNoChildren,
  seedReplyNesting,
  seedSpineCases,
  seedUnbrokenChildTitle,
  seedUnbrokenWords,
  seedWholeIssue,
  UNBROKEN_PATH,
  type WholeIssueSeed,
} from "./issue-layout-seed";
import { asPlanner, PROJECT } from "./issue-layout-seed-support";
import {
  OPEN_QUESTION_BLOCK_ID,
  planTaskBlockId,
  requirementBlockId,
  requirementText,
  SPINE_ANSWERED_QUESTION,
  SPINE_CHECK_BLOCK_ID,
  SPINE_REQUIREMENTS,
  SPINE_SPEC,
} from "./issue-layout-seed-text";
import { filterRow, gotoIssue, issuePage, phase, presetToggle } from "./issue-page";
import {
  DESKTOP,
  type DrawnRow,
  drawnRows,
  expectIssuePageDrawn,
  focusRow,
  focusThePage,
  nodeRow,
  nodeRows,
  PHONE,
  parentOfRow,
  rowBody,
  rowLayouts,
  rowOf,
  rowWords,
  tree,
  unfoldEveryBranch,
} from "./issue-page-g";
import {
  branchMoreIndents,
  branchMoreRows,
  bulletBoxes,
  caretInNode,
  childRow,
  childRows,
  childrenSection,
  drawnItems,
  focusedNodeId,
  focusInside,
  gapKinds,
  identityBlockBottom,
  idsOf,
  keysListLines,
  nodeBodies,
  onScreen,
  outlineRows,
  pageStatus,
  pinnedBodies,
  placeInStage,
  rowGaps,
  rowReference,
  rowTags,
  standInControlBelow,
  standInFootBox,
  tabStopRow,
  tooltipTexts,
  treeActiveDescendant,
  treeText,
} from "./issue-page-i";
import { withIssuePage } from "./issue-page-option";
import { expect, test } from "./test";
import { expectLoadSettled, watchApiRequests } from "./traffic";
import { assertPageFits } from "./unclipped";
import { asUser } from "./users";

/**
 * Part I's rows of the new issue page (dispatch://INFRA-3944, part I of dispatch://INFRA-1196):
 * the outline - the conversation folded to the frontier, its children as rows, its keys, and
 * editing in a node - read on the real stack. Each row turns the new page on for its reader with
 * `withIssuePage` (D's `phase` and this file's `openTree`), seeds what it reads with the seeds of
 * `e2e/issue-layout-seed.ts`, and reads the page through D's `e2e/issue-page.ts`, G's
 * `e2e/issue-page-g.ts` and this part's `e2e/issue-page-i.ts`. The few selectors it spells itself
 * are marks one row reads: a key's report at a row, a crumb, a child's facts and key, a row's
 * card and author, the links the hover row rests on and the Inbox's owner link.
 *
 * The rows come from the checkpoint's `e2e/issue-layout.e2e.ts` and `e2e/keyboard-issue.e2e.ts`,
 * each under the title its check names (`verification.md` of dispatch://INFRA-3944, c-outline
 * and c-keys).
 */

interface OpenedTree {
  readonly context: BrowserContext;
  readonly page: Page;
  readonly seeded: WholeIssueSeed;
}

/** The whole issue, opened on the new page by one signed-in reader with the focus on the page
 *  itself, which is where reading mode is. A key pressed before this returns reaches no handler,
 *  because the keymap binds only once sign-in resolves. It asserts only that the page loaded
 *  and that the focus left for the page itself (`gotoIssue`, `focusThePage`): each row below
 *  asserts the key or the tree property it is about first, and `expectTreeDrawn` after it. The
 *  suite's `test` has emptied the database before the row. */
async function openTree(browser: Browser): Promise<OpenedTree> {
  const seeded = await seedWholeIssue();
  const context = await asUser(browser, "alice");
  await withIssuePage(context, "alice");
  const page = await context.newPage();
  await page.setViewportSize({ height: 900, width: 1280 });
  await gotoIssue(page, `/issues/${seeded.key}`);
  await focusThePage(page);
  return { context, page, seeded };
}

/** The redesigned page and its tree are on the screen, which every row asserts after its own
 *  subject: a page that answers a key but draws no tree fails here. It waits for both, so a row
 *  that reads it straight after a load or a reload reads the page the app has drawn. */
async function expectTreeDrawn(page: Page): Promise<void> {
  await expect(
    issuePage(page),
    "the page draws no [data-issue-page] root, so this is not the redesigned issue page"
  ).toHaveCount(1);
  await expect(tree(page), 'the page draws no role="tree" named "Issue tree"').toHaveCount(1);
}

/** The ids of the rows a reader can see, in the order the keys move through them. */
async function visibleIds(page: Page): Promise<string[]> {
  return (await drawnRows(page)).filter((row) => row.visible).map((row) => row.id ?? "<no id>");
}

/** Where the keys have left the reader: the row holding the focus, or the one the tree names with
 *  `aria-activedescendant`. */
async function focusedRow(page: Page): Promise<string | null> {
  return (await focusedNodeId(page)) ?? (await treeActiveDescendant(page));
}

/** The focused row is on screen and clear of the sticky identity block, which R9 requires after
 *  every move and not only at the end. */
async function expectFocusedRowClear(page: Page, after: string): Promise<void> {
  const id = await focusedRow(page);
  expect(id, `${after} left the focus on no row`).not.toBeNull();
  const row = (await drawnRows(page)).find((candidate) => candidate.id === id);
  const box = row?.box ?? null;
  expect(box, `${after} left the focus on a row with no box`).not.toBeNull();
  const header = (await identityBlockBottom(page)) ?? 0;
  expect(
    box?.y ?? -1,
    `after ${after} the focused row sits under the sticky header`
  ).toBeGreaterThanOrEqual(header - 1);
  expect(
    (box?.y ?? 0) + (box?.height ?? 0),
    `after ${after} the focused row is below the viewport`
  ).toBeLessThanOrEqual(900);
}

/** The focused row is whole on screen and above the box at the foot, read with `onScreen`, which
 *  bounds a row by the height the page publishes for that box. */
async function expectFocusedRowAboveFoot(page: Page, after: string): Promise<void> {
  const id = await focusedRow(page);
  expect(id, `${after} left the focus on no row`).not.toBeNull();
  await expect
    .poll(() => onScreen(nodeRow(page, id ?? "").first()), {
      message: `${after} left the focused row under the box at the foot`,
    })
    .toBe(true);
}

/** The slug of the issue's own spec, which is how its blocks are addressed. */
async function primarySlug(key: string): Promise<string> {
  const primary = ((await getIssue(key)).artifacts ?? []).find((artifact) => artifact.primary);
  expect(primary, `${key} carries no primary document`).toBeDefined();
  return primary?.slug ?? "";
}

/** The kinds a row draws no tag for, because they are the conversation's own words rather than
 *  a kind it revealed (R34: "as one tag where the node has a typed kind"). */
const UNTYPED_KINDS: readonly string[] = ["comment", "message"];

/**
 * Every row of a typed kind carries its kind's word and its kind's shape on its one tag (R34),
 * but a child issue, whose tag is its key (the architect's ruling 5); a turn of the
 * conversation carries neither, its author's name and shape saying whose words it is (R9); and
 * every row its state word and its state's shape where the model holds a state, and nothing
 * where it holds none. Every word and token comes from `page-words.ts`, so a type or state the
 * page draws without declaring one fails here. `tags` is each row's tag by its node's id
 * (`rowTags`).
 */
function expectRowVocabulary(rows: readonly DrawnRow[], tags: ReadonlyMap<string, string>): void {
  for (const row of rows) {
    const type = row.type ?? "";
    expect(
      Object.keys(NODE_TYPE_NAMES),
      `row ${row.id} carries data-node-type "${type}", which the model does not name`
    ).toContain(type);
    const typeName = NODE_TYPE_NAMES[type as NodeType];
    const typeGlyph = GLYPH_TOKENS.types[type as NodeType];
    if (UNTYPED_KINDS.includes(type)) {
      expect(
        row.glyphs.map((glyph) => glyph.token),
        `row ${row.id} is a ${typeName}, which carries no tag, and draws its kind's shape`
      ).not.toContain(typeGlyph);
      expect(row.author, `row ${row.id} is a ${typeName} and names no author`).not.toBeNull();
    } else if (type === "child_issue") {
      // G draws the key alone, a link to the child's own page, with no shape beside it: how the
      // child stands is its facts after its title (`NodeRow.tsx`'s `RowMargin`).
      expect(
        tags.get(row.id ?? ""),
        `row ${row.id} is a child issue whose tag is not its key`
      ).toBe(row.id);
    } else {
      expect(row.text.toLowerCase(), `row ${row.id} does not say it is a ${typeName}`).toContain(
        typeName
      );
      expect(
        row.glyphs.map((glyph) => glyph.token),
        `row ${row.id} draws no shape for its type (${typeGlyph})`
      ).toContain(typeGlyph);
    }
    for (const glyph of row.glyphs) {
      expect(glyph.name, `the ${glyph.token} shape on row ${row.id} has no accessible name`).toBe(
        GLYPH_NAMES[glyph.token]
      );
    }

    const states = nodeStateWords(type as NodeType);
    if (states === undefined) {
      expect(
        row.state,
        `row ${row.id} is a ${typeName}, which the model gives no state, and it carries data-node-state "${row.state}"`
      ).toBeNull();
      expect(row.stateWord, `row ${row.id} draws a state word for a ${typeName}`).toBeNull();
      continue;
    }
    const state = row.state ?? "";
    expect(
      Object.keys(states),
      `row ${row.id} is a ${typeName} carrying data-node-state "${state}", which is no state of that type`
    ).toContain(state);
    expect(row.stateWord, `row ${row.id} draws no word for its state ${state}`).toBe(states[state]);
    const stateGlyphs: Readonly<Record<string, Readonly<Record<string, string>>>> =
      GLYPH_TOKENS.states;
    const stateGlyph = stateGlyphs[type]?.[state];
    expect(
      row.glyphs.map((glyph) => glyph.token),
      `row ${row.id} draws no shape for its state (${stateGlyph})`
    ).toContain(stateGlyph);
  }
}

/** The word and the shape each state is drawn with, one entry per state: a state told apart by
 *  colour alone collides with another here. */
function statesDrawn(rows: readonly DrawnRow[]): Map<string, string> {
  const drawn = new Map<string, string>();
  for (const row of rows) {
    if (row.state === null) continue;
    const token = row.glyphs.map((glyph) => glyph.token).join("+");
    drawn.set(`${row.type}:${row.state}`, `${row.stateWord} ${token}`);
  }
  return drawn;
}

/** A first line that ends inside a word: the stored text carries on without a break where the
 *  drawn line stops. */
function cutsAWord(stored: string, drawn: string): boolean {
  const line = drawn.replace(/\s*(?:…|\.\.\.)$/u, "").trimEnd();
  if (!stored.startsWith(line) || stored === line) return false;
  return !/\s/.test(stored.charAt(line.length)) && !/[.!?]$/.test(line);
}

/** The chips of `chips` that name a target whose stored words are `words`: a chip draws its
 *  target's words, cut as a title is, with an ellipsis where they carry on (G's `Chip`). */
function chipsNaming(chips: readonly string[], words: string): string[] {
  return chips.filter((chip) => {
    const drawn = chip.replace(/\s*…$/u, "").trimEnd();
    return drawn !== "" && words.startsWith(drawn);
  });
}

/** The words of the block `id` of `spec`, a document a seed writes as one line of words under
 *  each block's opening line (`SPINE_SPEC`). */
function seededBlockWords(spec: string, id: string): string {
  const at = spec.indexOf(`{#${id}}\n`);
  expect(at, `the seeded spec holds no block ${id}`).not.toBe(-1);
  return spec.slice(at).split("\n")[1] ?? "";
}

/**
 * R1 on one row's one line - a folded branch's - read twice, because the text alone cannot see
 * the cut a box makes: a box that hides the tail of its line leaves every character of it in the
 * line's text. So the line is read for its words - the start of the stored text, ending at a
 * word break, with an ellipsis where the text carries on - and its box for holding every
 * character it was given (`scrollWidth` at its `clientWidth`, a pixel for rounding).
 */
function expectCutAtAWord(row: DrawnRow | undefined, stored: string, where: string): void {
  expect(row, `${where} draws no row`).toBeDefined();
  const drawn = row?.firstLine ?? "";
  const line = drawn.replace(/\s*…$/u, "").trimEnd();
  expect(line, `${where} draws none of its own words`).not.toBe("");
  expect(stored.startsWith(line), `${where} draws "${drawn}", which is not "${stored}"`).toBe(true);
  expect(cutsAWord(stored, drawn), `${where} cuts "${stored}" inside a word: "${drawn}"`).toBe(
    false
  );
  if (line !== stored) {
    expect(drawn.endsWith("…"), `${where} says nothing of the text "${drawn}" stops short of`).toBe(
      true
    );
  }
  const box = row?.firstLineBox;
  expect(
    box?.scroll,
    `${where} draws "${drawn}" as ${box?.scroll} px of text in a ${box?.client} px box, so the ` +
      "box hides its tail where no word boundary decided it"
  ).toBeLessThanOrEqual((box?.client ?? -2) + 1);
  const words = row?.wordsBox;
  expect(
    words?.scroll,
    `${where} lays its words out over ${words?.scroll} px in a ${words?.client} px box, so the ` +
      "row hides a word beside its first line"
  ).toBeLessThanOrEqual((words?.client ?? -2) + 1);
}

/** The kinds of node the issue holds that are no node of its conversation, so no row of the
 *  outline is one (dispatch://INFRA-1196 R1, R3). */
const NOT_CONVERSATION: readonly string[] = ["document", "handoff", "image"];

/** Fails on every row of `rows` that is a document, a file or a handoff of the issue: by its
 *  kind, and by its id, which for a document is its slug. */
function expectNoIssueNodeRows(
  rows: readonly DrawnRow[],
  slugs: ReadonlySet<string>,
  where: string
): void {
  expect(
    rows
      .filter((row) => NOT_CONVERSATION.includes(row.type ?? "") || slugs.has(row.id ?? ""))
      .map((row) => `${row.type} ${row.id}`),
    `${where} draws a row for a document, a file or a handoff of the issue, which are nodes of the issue and not of its conversation`
  ).toEqual([]);
}

/** What of one ask says where the tree draws it: the document block it was indexed from, where
 *  it has one, and its own id. */
interface PlacedAsk {
  readonly artifact_id?: string | null;
  readonly block_artifact?: { readonly slug: string };
  readonly block_id?: string | null;
  readonly id: string;
}

/** The id the tree draws `ask` under: the block the server indexed it from, where it has one,
 *  and the ask's own id otherwise. */
function askNodeId(ask: PlacedAsk, slugOf: ReadonlyMap<string, string>): string {
  const slug = ask.block_artifact?.slug ?? slugOf.get(ask.artifact_id ?? "");
  return slug !== undefined && ask.block_id != null ? blockNodeId(slug, ask.block_id) : ask.id;
}

/** What the outline of `seedWholeIssue` holds, read from the server's own reads rather than from
 *  the page: every node of its conversation by the id the tree draws it under, the slugs of its
 *  documents and files, which no row may be, the summary callout, which the head draws
 *  (dispatch://INFRA-1196 R8, c-three-things), and the frontier - every node open and unanswered,
 *  which the fold opens the path to (R1). */
interface SeededOutline {
  readonly frontier: ReadonlySet<string>;
  readonly nodes: ReadonlySet<string>;
  readonly slugs: ReadonlySet<string>;
  readonly summary: string;
}

async function seededOutline(seeded: WholeIssueSeed): Promise<SeededOutline> {
  const artifacts = (await getIssue(seeded.key)).artifacts ?? [];
  const slugOf = new Map(artifacts.map((artifact) => [artifact.id, artifact.slug]));
  const slug = (id: string) => slugOf.get(id) ?? "";
  // The issue itself is no row: the header is the issue (ruling 5).
  const nodes = new Set<string>(seeded.children.keys);
  const frontier = new Set<string>();
  for (const ask of await listIssueAsks(seeded.key)) {
    if (ask.kind !== "question") continue;
    nodes.add(askNodeId(ask, slugOf));
    if (ask.state === "open") frontier.add(askNodeId(ask, slugOf));
  }
  for (const comment of await listComments(seeded.key)) nodes.add(comment.id);
  for (const thread of await listIssueMessages(seeded.key)) {
    for (const message of [thread.message, ...(thread.replies ?? [])]) nodes.add(message.id);
  }
  for (const requirement of (await getIssueCoverage(seeded.key)).requirements ?? []) {
    const id = blockNodeId(slug(seeded.specArtifactId), requirement.block_id);
    nodes.add(id);
    if (requirement.checks === 0) frontier.add(id);
  }
  for (const decision of (await getIssueDecisions(seeded.key)).decisions ?? []) {
    const id = blockNodeId(slug(seeded.decisionsArtifactId), decision.block_id);
    nodes.add(id);
    if (decision.waiting_review) frontier.add(id);
  }
  for (const check of (await getIssueResults(seeded.key)).checks ?? []) {
    const id = blockNodeId(slug(check.artifact_id), check.block_id);
    nodes.add(id);
    if (checkState(check) !== "passed") frontier.add(id);
    for (const result of check.results ?? []) nodes.add(result.id);
  }
  for (const task of seeded.planTasks) nodes.add(blockNodeId(slug(seeded.planArtifactId), task));
  return {
    frontier,
    nodes,
    slugs: new Set(artifacts.map((artifact) => artifact.slug)),
    summary: blockNodeId(slug(seeded.specArtifactId), seeded.summaryBlockId),
  };
}

/** The ids above a row of `id` in `rows` - its first, or the one at index `at` - outermost
 *  first, as the drawn levels give them: one read of the page for the whole chain rather than
 *  one per level. */
function ancestorsIn(
  rows: readonly DrawnRow[],
  id: string,
  at: number = rows.findIndex((row) => row.id === id)
): string[] {
  const chain: string[] = [];
  let level = at === -1 ? 0 : Number(rows[at]?.level ?? 0);
  for (let index = at - 1; index >= 0 && level > 1; index -= 1) {
    const row = rows[index];
    if (Number(row?.level ?? 0) !== level - 1) continue;
    chain.unshift(row?.id ?? "");
    level -= 1;
  }
  return chain;
}

/** How many rows `rows` draws under the first row of `id`, which with every branch open is how
 *  many nodes that node holds. */
function heldIn(rows: readonly DrawnRow[], id: string): number {
  const at = rows.findIndex((row) => row.id === id);
  const level = Number(rows[at]?.level ?? 0);
  let held = 0;
  for (let index = at + 1; index < rows.length; index += 1) {
    if (Number(rows[index]?.level ?? 0) <= level) break;
    held += 1;
  }
  return held;
}

/** How many rows of each kind `seedWholeIssue` draws with every branch open, from the seed's own
 *  record of what it wrote where it keeps one: the 12 requirements and the one citing a
 *  document, a result for each check but the one recorded with none, the two comment threads
 *  with their replies and the comment anchored to a block, the three messages of the one
 *  conversation, and the children. The rest it writes once each: the open question and the
 *  answered one. The issue itself is no row (ruling 5), the summary's callout is the head's three
 *  sentences and no row (R8), and no document and no file is a row (dispatch://INFRA-1196 R1,
 *  R3). */
function rowsOfTheWholeIssue(seeded: WholeIssueSeed): Record<string, number> {
  const checks = seeded.checks.all.length;
  return {
    ask: 2,
    check: checks,
    child_issue: seeded.children.keys.length,
    comment: seeded.comments.threads.length + seeded.comments.replies.length + 1,
    decision: seeded.decisions.length,
    message: Object.keys(seeded.messages).length,
    plan_task: seeded.planTasks.length,
    requirement: seeded.requirements.length + 1,
    result: checks - 1,
  };
}

/** How many of `rows` there are of each kind. */
function rowsByKind(rows: readonly DrawnRow[]): Record<string, number> {
  const kinds: Record<string, number> = {};
  for (const row of rows) {
    const kind = row.type ?? "<no type>";
    kinds[kind] = (kinds[kind] ?? 0) + 1;
  }
  return kinds;
}

// c-tree. On the issue today's model holds, the one the conversion has not reached
// (dispatch://INFRA-1253), with no filter on, at 1440 px. This row carries no `@phone`: below
// 1024 px the tree is read one node at a time under a breadcrumb, one level below the node it is
// on (R16), and every level, ancestor and fold below would be a different claim; the row under
// this one reads the clauses the width keeps.
//
// What waits on another issue, and is not read here: a criterion, a reading, a lens and a
// finding, which the model cannot hold yet (dispatch://INFRA-1253, INFRA-1150, INFRA-1950); the
// summary callout, which the head draws (R8) and c-three-things reads, and which this row
// leaves out of its reading either way; and which row of a node the edit key opens, which is
// c-editing's and touches nothing this row reads.
// SLOW(>=2s): the whole issue seeded and read back through seven API reads, one load waited out
// until its reads settle, then every branch unfolded press by press and the rows read whole.
test("with no filter the page is the conversation, each node edited once, folded to the frontier", async ({
  browser,
}) => {
  test.setTimeout(180_000);
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    // One more turn of the seed's conversation before the page opens: the planner cites the
    // first child by its address, which the page draws as G's link to an issue, by its key and
    // its title (R4 of the root spec). It answers the seed's message, since the outline draws a
    // thread only once a person wrote in it (`agents-only.ts`).
    const cited = seeded.children.keys[0] ?? "";
    const citing = await createMessage(
      seeded.key,
      {
        body: `The child dispatch://${cited} is where the rows were first drawn.`,
        in_reply_to: seeded.messages.root,
      },
      asPlanner
    );
    const outline = await seededOutline(seeded);
    const specSlug = await primarySlug(seeded.key);
    const settling = watchApiRequests(page);
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectLoadSettled(settling);
    await expectIssuePageDrawn(page);

    // The outline's measure (R1): at most 1000 px wide, the prototype's `#pane`, and centred in
    // the stage, its gaps to the stage's insets on its two sides a pixel apart at most.
    const place = await placeInStage(tree(page));
    expect(place, "the tree stands in no stage").not.toBeNull();
    expect(
      place?.width ?? Number.POSITIVE_INFINITY,
      `the tree is ${place?.width} px wide, wider than the outline's 1000 px`
    ).toBeLessThanOrEqual(1000);
    expect(
      Math.abs((place?.left ?? 0) - (place?.right ?? Number.POSITIVE_INFINITY)),
      `the tree is not centred in the stage: ${place?.left} px at its left, ${place?.right} px at its right`
    ).toBeLessThanOrEqual(1);

    // No document, file or handoff is a row, folded or not (R1, R3): the spec is drawn nowhere
    // in the outline.
    const folded = await drawnRows(page);
    expectNoIssueNodeRows(folded, outline.slugs, "the page as it opens");
    expect(
      folded.filter((row) => row.role !== "treeitem").map((row) => row.id),
      "these rows of the tree are no treeitem"
    ).toEqual([]);
    expectRowVocabulary(folded, await rowTags(page));

    // No row repeats the issue: the first row is the first node under it (row 2), and it starts
    // at most 15 px under the view row's foot - the prototype's 14 px gap under `#tabs`, and 1 px
    // for rounding (part I's R3).
    expect(
      rowOf(folded, seeded.key),
      "the outline draws a row for the issue itself"
    ).toBeUndefined();
    const viewRow = await filterRow(page).boundingBox();
    expect(viewRow, "the page draws no view row").not.toBeNull();
    const firstTop = folded.find((row) => row.visible)?.box?.y ?? Number.POSITIVE_INFINITY;
    expect(
      firstTop - ((viewRow?.y ?? 0) + (viewRow?.height ?? 0)),
      "the outline's first row stands more than 15 px under the view row's foot"
    ).toBeLessThanOrEqual(15);

    // The page opens folded to the frontier: the open ask, every check that has not passed,
    // every decision waiting for review and every requirement no check serves is drawn with
    // its whole path open, and no branch is open that holds none of them - so no plan task
    // holds one open, and the first requirement, which holds a check that passed and a plan
    // task, arrives folded.
    expect(outline.frontier.size, "the seed holds nothing open and unanswered").toBeGreaterThan(0);
    for (const id of outline.frontier) {
      expect(
        rowOf(folded, id),
        `${id}, open and unanswered, draws no row as the page opens`
      ).toBeDefined();
      for (const above of ancestorsIn(folded, id)) {
        expect(
          rowOf(folded, above)?.expanded,
          `${above} is folded over ${id}, which is open and unanswered`
        ).toBe("true");
      }
    }
    const paths = new Set([...outline.frontier].flatMap((id) => ancestorsIn(folded, id)));
    expect(
      folded
        .filter((row) => row.expanded === "true" && !paths.has(row.id ?? ""))
        .map((row) => row.id),
      "a branch opens on load that holds nothing open and unanswered"
    ).toEqual([]);
    expect(
      rowOf(folded, blockNodeId(specSlug, requirementBlockId(1)))?.expanded,
      "the requirement whose check passed opens because a plan task serves it"
    ).toBe("false");

    // With every branch open, the outline draws a row for each node of the conversation the seed
    // wrote - each ask, comment, message, reply, requirement, decision, check, result and plan
    // task, and each child under the issue - read by node id against the server's own reads,
    // and a row for nothing else.
    await unfoldEveryBranch(page);
    const whole = await drawnRows(page);
    expectNoIssueNodeRows(whole, outline.slugs, "the page with every branch open");
    // The memo callout is the head's three sentences and no row of the outline (R8).
    expect(idsOf(whole), "the memo callout is drawn as a row of the outline").not.toContain(
      outline.summary
    );
    expect(
      [...new Set(idsOf(whole))].sort(),
      "the rows of the outline are not the nodes of the issue's conversation"
    ).toEqual([...outline.nodes].sort());
    // The planner's citation of a child is G's link to the child, by its key and its title.
    await expect(
      rowReference(nodeRow(page, citing.id).first(), `dispatch://${cited}`),
      `the message that cites ${cited} does not draw it as its key and its title`
    ).toHaveText(`${cited} ${(await getIssue(cited)).title}`);
    // The children are rows of the outline's top level, under no other row: the issue itself
    // is no row (ruling 5, part I's R6).
    for (const child of seeded.children.keys) {
      expect(ancestorsIn(whole, child), `the child ${child} sits under another row`).toEqual([]);
    }

    // Every branch the page opened folded shows its first line and counts the nodes under it.
    for (const row of folded) {
      if (row.expanded !== "false") continue;
      expect(row.foldedCount, `the folded ${row.id} does not count the nodes under it`).toBe(
        String(heldIn(whole, row.id ?? ""))
      );
    }

    // A node shown under a second parent names its other parents on that row.
    for (const id of new Set(idsOf(whole))) {
      const placed = whole.filter((row) => row.id === id);
      if (placed.length < 2) continue;
      for (const row of placed) {
        expect(row.text, `${id} is drawn twice and this row names no other parent`).toContain(
          CONTROL_LABELS.also_under
        );
      }
    }

    // An ask's answer is drawn in the ask's own row, by who answered and in their words, and is
    // no row of its own.
    const stored = (await getAsk(seeded.answeredAskId)).ask.answer;
    const words = stored?.text ?? "";
    expect(words, "the seed's answered ask holds no answer").not.toBe("");
    const asked = rowOf(whole, seeded.answeredAskId);
    expect(asked?.answerLine ?? "", "the answered ask's row draws no answer").toContain(
      firstLineOf(words).line
    );
    expect(asked?.answerAuthor, "the answered ask's row does not name who answered").toBe(
      stored?.user
    );
    expect(
      whole
        .filter((row) => row.id !== seeded.answeredAskId && (row.words ?? row.firstLine) === words)
        .map((row) => row.id),
      "the answer is drawn as a row of its own"
    ).toEqual([]);

    // A folded branch is where the page draws one line for a node (the architect's ruling 1 on
    // R1): that line ends at a sentence end or at 100 characters, always at a word, and is the
    // node's own text. Every other row draws its words whole, so the focused row holds them.
    const lines = folded.filter((row) => row.firstLine !== null);
    expect(lines.length, "the page as it opens draws no folded branch's line").toBeGreaterThan(0);
    for (const row of lines) {
      const line = (row.firstLine ?? "").replace(/\s*(?:…|\.\.\.)$/u, "").trimEnd();
      expect(
        line.length,
        `row ${row.id} draws a first line of ${line.length} characters`
      ).toBeLessThanOrEqual(100);
      expect(row.text, `row ${row.id}'s first line is not its own text`).toContain(line);
      expect(row.words, `the folded row ${row.id} draws its words whole as well`).toBeNull();
      // A line that ends at a sentence end has no mark after it, and a cut line has one `…`
      // (row 20 drew four dots).
      expect(
        /[.!?]\s*(?:…|\.\.\.)$/u.test(row.firstLine ?? ""),
        `row ${row.id}'s first line "${row.firstLine}" ends with a sentence end and a mark`
      ).toBe(false);
      expect(row.firstLine ?? "", `row ${row.id}'s first line ends with three dots`).not.toMatch(
        /\.\.\.$/u
      );
    }
    const long = rowOf(folded, blockNodeId(specSlug, requirementBlockId(1)));
    expect(long?.firstLine, "the two-sentence requirement draws no folded line").not.toBeNull();
    expect(
      cutsAWord(requirementText(1), long?.firstLine ?? ""),
      `the first line "${long?.firstLine}" cuts the stored text inside a word`
    ).toBe(false);
    const first = nodeRow(page, blockNodeId(specSlug, requirementBlockId(1))).first();
    await first.focus();
    expect(await rowBody(first).count(), "the focused row draws no body").toBe(1);
    expect(
      (await rowWords(first).innerText()).replace(/\s+/g, " "),
      "the focused row does not draw the node's whole stored text"
    ).toContain(requirementText(1));
  });
});

/** The widths a folded branch's line is read at below 1024 px: the floor R18 names, the phone
 *  the `iphone` project runs, the last width the phone's classes draw and the first that draws
 *  the desktop's (decision 72: `max-[761px]:` is `width < 761px`), and the narrowest width above
 *  a phone's (`PHONE_VIEWPORT_QUERY` is under 768 px). */
const ONE_LINE_WIDTHS = [320, 390, 760, 761, 768] as const;

/** How a deeper branch's one line is indented at `width`: per level and past its level's
 *  bullet, the phone's 17 px and 22 px at 760 px and below, and the desktop's 23 px and 26 px
 *  above (`TreeRows.tsx`'s `BranchMore`). */
function branchMoreIndent(width: number): { readonly inset: number; readonly step: number } {
  return width <= 760 ? { inset: 22, step: 17 } : { inset: 26, step: 23 };
}

/**
 * The two gaps at a phone's bullet (part I's R3, decision 60). Between a node's bullet column
 * and its words stands the row's own 6 px (G's `NODE_ROW_LAYOUT.box`) and no inset of the
 * tree's box: at most 7 px, a pixel for rounding, read on each row whose node holds no other,
 * where the bullet is the column itself, and whose words are not the reader's own, which stand
 * inside their ink rule (G's second edge). A node that holds others draws its 24 px fold control
 * reaching past that column on both sides (`NODE_ROW_LAYOUT.foldControl`): it ends before its
 * words, so it covers none of them, and it stays inside the page.
 */
async function expectPhoneBulletGaps(page: Page, where: string): Promise<void> {
  const layouts = await rowLayouts(page);
  const bullets = await bulletBoxes(page);
  expect(
    bullets.map((bullet) => bullet.id),
    `${where}: the bullets were read off other rows than the rows' layouts`
  ).toEqual(layouts.map((row) => row.id));
  let leaves = 0;
  let holders = 0;
  for (const [at, row] of layouts.entries()) {
    const bullet = bullets[at];
    if (row.fold === null) {
      if (row.own) continue;
      leaves += 1;
      const gap = row.wordsLeft - (bullet?.right ?? Number.NEGATIVE_INFINITY);
      expect(
        gap,
        `${where}: the words of ${row.id} start ${gap} px right of its bullet column`
      ).toBeLessThanOrEqual(7);
      continue;
    }
    holders += 1;
    expect(
      bullet?.right ?? Number.POSITIVE_INFINITY,
      `${where}: the fold control of ${row.id} ends at ${bullet?.right} px, over its words, which start at ${row.wordsLeft} px`
    ).toBeLessThanOrEqual(row.wordsLeft);
    expect(
      bullet?.left ?? Number.NEGATIVE_INFINITY,
      `${where}: the fold control of ${row.id} reaches past the page's left edge, to ${bullet?.left} px`
    ).toBeGreaterThanOrEqual(0);
  }
  expect(leaves, `${where} draws no row of a node that holds no other`).toBeGreaterThan(0);
  expect(holders, `${where} draws no row of a node that holds others`).toBeGreaterThan(0);
}

// c-tree, at the widths the tree is read one node at a time at (R16), which is a row of its own:
// the row above reads the whole tree at 1440 px, and every level it names, every ancestor it
// walks and every fold it counts is that tree's. This reads the clauses the narrow tree keeps:
// a drawn row draws its node's words whole (the architect's ruling 1 on R1), and a folded branch
// draws one line - its node's first line, cut at a word by R1's rule - and how many nodes it
// holds, in a box that hides none of it. It carries `@phone-only` beside `@phone` because the
// row above reads the same clauses wherever the tree draws the whole of itself.
//
// Two rows of the issue's own screen: the first requirement, which holds a check that passed
// and a plan task and so arrives folded, its author the page's own words for one nobody
// recorded (R9); and the open question, which the frontier draws two levels down whatever the
// depth (R16), its words whole.
//
// It sets the width itself, as every row of this file does: `phase` starts every page at the
// desktop size in every project, the `iphone` project included.
// SLOW(>=2s): the whole issue seeded, then five cold loads at five widths, each read row by row
// and measured for its fit, and a branch opened at two of them.
test("a folded branch's line is its first line, cut at a word @phone @phone-only", async ({
  browser,
}) => {
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    const specSlug = await primarySlug(seeded.key);
    const nodeId = blockNodeId(specSlug, requirementBlockId(1));
    const questionId = blockNodeId(specSlug, OPEN_QUESTION_BLOCK_ID);
    const question = (await getAsk(seeded.openQuestion.askId)).ask.question;
    for (const width of ONE_LINE_WIDTHS) {
      await page.setViewportSize({ height: PHONE.height, width });
      await gotoIssue(page, `/issues/${seeded.key}`);
      await expectIssuePageDrawn(page);
      await expect(
        nodeRow(page, questionId).first(),
        `the open question draws no row at ${width} px`
      ).toBeVisible();
      await expect(
        nodeRow(page, nodeId).first(),
        `the issue's own screen draws no row for its first requirement at ${width} px`
      ).toBeVisible();
      const rows = await drawnRows(page);

      // The open question: its words whole.
      const asked = rowOf(rows, questionId);
      expect(asked?.firstLine, `the open question is cut to one line at ${width} px`).toBeNull();
      expect(asked?.words, `the open question's row at ${width} px`).toContain(question);

      // The folded requirement: its first line, cut at a word, and its count.
      const required = rowOf(rows, nodeId);
      expect(required?.expanded, `the first requirement is not folded at ${width} px`).toBe(
        "false"
      );
      // Ruling 5: the model holds no author for a requirement, so its row draws none; its
      // opened body says so, which is read below once the row has the focus.
      expect(
        required?.author,
        `the first requirement's row draws an author the model does not hold at ${width} px`
      ).toBeNull();
      expectCutAtAWord(
        required,
        requirementText(1),
        `the two-sentence requirement's line at ${width} px`
      );
      expect(required?.text, `the folded requirement counts nothing at ${width} px`).toContain(
        `${required?.foldedCount} ${CONTROL_LABELS.folded_rows}`
      );
      // No row makes the page scroll sideways at any of these widths (R12).
      await assertPageFits(page);
      if (width === PHONE.width) await expectPhoneBulletGaps(page, `the page at ${width} px`);

      // The phone's form ends at 760 px itself and the desktop's starts at 761 (decision 72).
      // Below 1024 px one level is drawn, so a branch the reader opens off the frontier folds
      // what its nodes hold to one line: the first requirement's check holds its results.
      if (width !== 760 && width !== 761) continue;
      await nodeRow(page, nodeId)
        .first()
        .getByRole("button", { exact: true, name: CONTROL_LABELS.unfold })
        .click();
      await expect
        .poll(() => branchMoreRows(page).count(), {
          message: `opening the first requirement at ${width} px draws no deeper branch's line`,
        })
        .toBeGreaterThan(0);
      const { inset, step } = branchMoreIndent(width);
      for (const line of await branchMoreIndents(page)) {
        expect(
          line.padding,
          `a deeper branch's line at depth ${line.depth} is indented ${line.padding} px at ${width} px`
        ).toBe(line.depth * step + inset);
      }
    }

    // What the line left out is drawn the moment the row has the focus (R6), so the reader
    // loses no word to it.
    const opened = nodeRow(page, nodeId).first();
    await opened.focus();
    await expect
      .poll(async () => (await rowWords(opened).innerText()).replace(/\s+/g, " "), {
        message: "the focused row does not draw the node's whole stored text",
      })
      .toContain(requirementText(1));
    await expect(
      rowBody(opened).locator("[data-node-author]"),
      "the focused requirement's body does not say its author is unknown"
    ).toHaveText(CONTROL_LABELS.unknown_author);
  });
});

// c-spine. Each node under the parent the model stores (R4), read against the server's own
// edges on the two seeds that hold today's: an ask over the decision that answers it, `serves`
// over checks and plan tasks, a check over its results, and the entry a reply answers. On the
// issue the conversion has not reached every other typed block sits under the issue.
//
// What waits on another issue, and is not read here: a criterion under the words that raised
// it, a requirement under its criterion, and the left arrow walking up from a requirement to
// them (dispatch://INFRA-1253); a lens under its node and a finding under its lens
// (INFRA-1950); a reading under its decision (INFRA-1150); and a comment anchored to a node other
// than a statement under that node, an edge the model does not store (INFRA-1149, INFRA-1253), so
// it sits under the issue as any node with no stored parent does. A comment anchored inside a
// statement is read under that statement, which the page finds by the anchor's block (part L,
// dispatch://INFRA-3949 R11).
// SLOW(>=2s): three seeds, each loaded cold with its branches unfolded press by press, and the
// first seed's parents read off the page one whole read at a time.
test("each node sits under the parent the model stores", async ({ browser }) => {
  test.setTimeout(180_000);
  await phase(browser, seedSpineCases, async (page, seeded) => {
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    await unfoldEveryBranch(page);
    const specSlug = (await getArtifact(seeded.specArtifactId)).slug;
    const verificationSlug = (await getArtifact(seeded.verificationArtifactId)).slug;
    const decisionsSlug = (await getArtifact(seeded.decisionsArtifactId)).slug;
    const decision = blockNodeId(decisionsSlug, seeded.decisionBlockId);
    const check = blockNodeId(verificationSlug, SPINE_CHECK_BLOCK_ID);

    // The decision sits under the ask it answers: the question its block names.
    const stored = ((await getIssueDecisions(seeded.key)).decisions ?? []).find(
      (one) => one.block_id === seeded.decisionBlockId
    );
    expect(stored?.asks, "the server holds no ask the decision answers").toEqual([
      seeded.answeredAskId,
    ]);
    expect(
      await parentOfRow(page, decision),
      "the decision is not drawn under the ask it answers"
    ).toBe(seeded.answeredAskId);
    // A node with no stored parent stands at the outline's top level, under no row: the issue
    // itself is no row (ruling 5).
    expect(
      await parentOfRow(page, seeded.answeredAskId),
      "the answered ask is not drawn at the outline's top level"
    ).toBeNull();

    // A requirement whose origin is absent sits under the issue, drawn as a node like any other
    // rather than as an error: the gap it draws is the one that tells it apart, that no check
    // serves it.
    const unexplained = blockNodeId(specSlug, SPINE_REQUIREMENTS.unexplained);
    expect(
      await parentOfRow(page, unexplained),
      "the unexplained requirement is not drawn at the outline's top level"
    ).toBeNull();
    expect(
      await rowGaps(nodeRow(page, unexplained).first()).evaluateAll((drawn) =>
        drawn.map((gap) => gap.getAttribute("data-gap-kind"))
      ),
      "the requirement nothing explains draws a gap other than the one that tells it apart"
    ).toEqual(["no_check"]);
    expect(await gapKinds(page), "a gap the page draws names no kind").not.toContain("");

    // No requirement is drawn under a decision, and each sits under the issue.
    const rows = await drawnRows(page);
    const typeOf = new Map(rows.map((row) => [row.id ?? "", row.type]));
    for (const requirement of Object.values(SPINE_REQUIREMENTS)) {
      const parent = await parentOfRow(page, blockNodeId(specSlug, requirement));
      expect(typeOf.get(parent ?? ""), `${requirement} is drawn under a decision`).not.toBe(
        "decision"
      );
      expect(parent, `${requirement} is not drawn at the outline's top level`).toBeNull();
    }

    // The check that serves two requirements sits under the first its `serves` names and is
    // shown under the other, each row naming its other parent, and both open the same block.
    const placed = rows.filter((row) => row.id === check);
    for (const row of placed) {
      expect(row.text, "a placement of the check does not name its other parent").toContain(
        CONTROL_LABELS.also_under
      );
    }
    expect(
      placed.map((row) => ancestorsIn(rows, check, rows.indexOf(row)).at(-1)),
      "the check is not under each of the two requirements it serves"
    ).toEqual([
      blockNodeId(specSlug, SPINE_REQUIREMENTS.servedTwiceA),
      blockNodeId(specSlug, SPINE_REQUIREMENTS.servedTwiceB),
    ]);
    const firstRow = nodeRow(page, check).nth(0);
    const secondRow = nodeRow(page, check).nth(1);
    await firstRow.focus();
    const fromFirstParent = (await rowBody(firstRow).innerText()).replace(/\s+/g, " ");
    await secondRow.focus();
    const fromSecondParent = (await rowBody(secondRow).innerText()).replace(/\s+/g, " ");
    expect(
      fromSecondParent,
      "the two parents open two copies of the block rather than the same block"
    ).toBe(fromFirstParent);

    // Every edge that is not the spine is a chip naming its target, and the spine is none: the
    // decision's link to a requirement is its chip, once, and the ask it sits under is not one.
    // A folded branch draws one line and no chip (ruling 1 on R1), so the tree is read open.
    // A chip names its target by the target's stored words, cut as a title is (G's `Chip`), so
    // each is found by the words the seed stored.
    await unfoldEveryBranch(page);
    const chips = (await drawnRows(page)).flatMap((row) => row.chips);
    expect(
      chipsNaming(chips, seededBlockWords(SPINE_SPEC, SPINE_REQUIREMENTS.shapedElsewhere)).length,
      `the decision's link is not drawn once as a chip: ${JSON.stringify(chips)}`
    ).toBe(1);
    expect(
      chipsNaming(chips, SPINE_ANSWERED_QUESTION),
      "the ask the decision sits under is drawn as its chip as well"
    ).toEqual([]);
  });

  await phase(browser, seedWholeIssue, async (page, seeded) => {
    const specSlug = (await getArtifact(seeded.specArtifactId)).slug;
    const verificationSlug = (await getArtifact(seeded.verificationArtifactId)).slug;
    const planSlug = (await getArtifact(seeded.planArtifactId)).slug;
    const decisionsSlug = (await getArtifact(seeded.decisionsArtifactId)).slug;
    const slugs = new Set(((await getIssue(seeded.key)).artifacts ?? []).map((one) => one.slug));
    const settling = watchApiRequests(page);
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectLoadSettled(settling);
    await unfoldEveryBranch(page);
    const rows = await drawnRows(page);
    const parentOf = (id: string) => ancestorsIn(rows, id).at(-1);

    // The decision that answers the issue's answered ask sits under that ask.
    expect(
      parentOf(blockNodeId(decisionsSlug, seeded.decisions[1] ?? "")),
      "the decision that answers the ask is not drawn under it"
    ).toBe(seeded.answeredAskId);

    // Each check under the requirement its `serves` names, and each result under its check.
    for (const check of (await getIssueResults(seeded.key)).checks ?? []) {
      const id = blockNodeId(verificationSlug, check.block_id);
      const index = Number(check.block_id.slice(1));
      expect(parentOf(id), `${check.block_id} is not under the requirement it serves`).toBe(
        blockNodeId(specSlug, requirementBlockId(index))
      );
      for (const result of check.results ?? []) {
        expect(parentOf(result.id), `a result of ${check.block_id} is not under it`).toBe(id);
      }
    }
    // Each plan task under the requirement it serves, and the one that serves none under the
    // issue.
    seeded.planTasks.forEach((task, index) => {
      expect(
        parentOf(blockNodeId(planSlug, task)),
        `${task} is not under the requirement it serves`
      ).toBe(
        index === seeded.planTasks.length - 1
          ? undefined
          : blockNodeId(specSlug, requirementBlockId(index + 1))
      );
    });
    // Each reply under the entry it answers.
    expect(parentOf(seeded.messages.reply), "the reply is not under its message").toBe(
      seeded.messages.root
    );
    expect(
      parentOf(seeded.messages.replyToReply),
      "the reply to a reply is not under the reply it answers"
    ).toBe(seeded.messages.reply);
    seeded.comments.replies.forEach((reply, index) => {
      expect(parentOf(reply), "a comment's reply is not under the comment it answers").toBe(
        seeded.comments.threads[index % seeded.comments.threads.length]
      );
    });
    // The comment anchored to the second requirement sits under that requirement (part L's R11).
    expect(
      parentOf(seeded.comments.anchored),
      "the comment anchored to a statement is not drawn under it"
    ).toBe(blockNodeId(specSlug, seeded.comments.anchoredBlock));

    // Every typed block the model gives no parent sits under the issue - the open question's
    // block and every requirement - and no row sits under a document. The summary callout is
    // the head's three sentences and no row (R8).
    expect(
      rows.some((row) => row.id === blockNodeId(specSlug, seeded.summaryBlockId)),
      "the summary callout is drawn as a row of the outline"
    ).toBe(false);
    for (const id of [
      blockNodeId(specSlug, seeded.openQuestion.blockId),
      ...seeded.requirements.map((requirement) => blockNodeId(specSlug, requirement)),
    ]) {
      expect(
        parentOf(id),
        `${id}, which the model gives no parent, is not at the outline's top level`
      ).toBeUndefined();
    }
    expect(
      rows.filter((row) => slugs.has(parentOf(row.id ?? "") ?? "")).map((row) => row.id),
      "a node is drawn under a document"
    ).toEqual([]);
    const typeOfRow = new Map(rows.map((row) => [row.id ?? "", row.type]));
    expect(
      rows
        .filter((row) => row.type === "requirement")
        .filter((row) => typeOfRow.get(parentOf(row.id ?? "") ?? "") === "decision")
        .map((row) => row.id),
      "a requirement is drawn under a decision"
    ).toEqual([]);
  });

  // R4 from 1024 px up: the outline draws three levels below the node it is rooted at, and a
  // branch opened deeper is one line counting what it holds, which opens it in place. The
  // thread of `seedReplyNesting` is four levels deep - its head, the answer to it, the answer to
  // that answer, and a third answer under that - and `phase` opens it at 1440 by 900 px.
  await phase(browser, seedReplyNesting, async (page, seeded) => {
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    await expect(nodeRow(page, seeded.head), "the thread's head draws no row").toHaveCount(1);
    // The thread's folds are opened by their fold controls alone: G's `unfoldEveryBranch` also
    // presses the line that counts a deeper branch, which is what this reading reads shut.
    const folds = nodeRows(page).getByRole("button", { exact: true, name: CONTROL_LABELS.unfold });
    for (let pressed = 0; pressed < 20 && (await folds.count()) > 0; pressed += 1) {
      await folds.first().click();
    }
    await expect(folds, "a fold control of the thread is still shut").toHaveCount(0);
    await focusThePage(page);
    const rows = await drawnRows(page);
    for (const id of [seeded.head, seeded.first, seeded.second]) {
      expect(
        rowOf(rows, id),
        `${id}, of the thread's first three levels, draws no row`
      ).toBeDefined();
    }
    expect(
      rowOf(rows, seeded.third),
      "the thread's fourth level is drawn as a row before the reader opened it"
    ).toBeUndefined();
    await expect(branchMoreRows(page), "the thread's fourth level is not one line").toHaveCount(1);
    const items = await drawnItems(page);
    const second = items.findIndex((item) => item.id === seeded.second);
    expect(
      items[second + 1],
      "the line that counts the fourth level does not stand under its parent, counting it"
    ).toEqual({
      id: null,
      level: String(Number(items[second]?.level) + 1),
      more: `1 ${CONTROL_LABELS.deeper_row}`,
    });

    await branchMoreRows(page).getByRole("button").click();
    await expect(
      nodeRow(page, seeded.third),
      "pressing the line does not draw the fourth level"
    ).toHaveCount(1);
    await expect(branchMoreRows(page), "the line stays once its branch is open").toHaveCount(0);
    const opened = await drawnItems(page);
    const parent = opened.findIndex((item) => item.id === seeded.second);
    expect(opened[parent + 1], "the fourth level does not open in the line's place").toEqual({
      id: seeded.third,
      level: String(Number(opened[parent]?.level) + 1),
      more: null,
    });
  });
});

// c-open-questions (R7). The page opens with the keyboard focus on the first open question that
// waits on the reader, in the outline's order, its row inside the viewport with nothing unfolded
// or scrolled by hand; `q` moves to the next one; and where none waits on the reader the page
// opens with the focus on the outline's first row. The second question is asked after forty
// older nodes, so the scroll is what puts it in the viewport: on a seed whose one question is
// the outline's first row, the row is on the first screen whatever the page does.
// SLOW(>=2s): forty comments and a question written, then ten cold loads at three sizes, of this
// issue and of a second one of one long comment, each waited on until the focus lands.
test("an open question is unfolded and on the first screen @phone", async ({ browser }) => {
  // Ten cold loads at three sizes, eight of them of an issue of some ninety rows.
  test.setTimeout(240_000);
  /** Whether node `id`'s row starts inside what the reader sees: the top of its card under the
   *  band that stays at the top of the screen and above the box at its foot. Only the top is
   *  read, so a row that runs on below the foot still counts. */
  const inView = (page: Page, id: string): Promise<boolean> =>
    nodeRow(page, id)
      .first()
      .evaluate(
        (row, properties) => {
          const style = getComputedStyle(document.documentElement);
          const band = Number.parseFloat(style.getPropertyValue(properties.band)) || 0;
          const foot = Number.parseFloat(style.getPropertyValue(properties.foot)) || 0;
          const card = row.querySelector("[data-node-card]") ?? row;
          const box = card.getBoundingClientRect();
          return box.top >= band - 1 && box.top < window.innerHeight - foot;
        },
        { band: IDENTITY_HEIGHT_PROPERTY, foot: FOOT_HEIGHT_PROPERTY }
      );
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    const first = blockNodeId(await primarySlug(seeded.key), OPEN_QUESTION_BLOCK_ID);
    // Forty nodes a person wrote, each a row of the outline, and then a question asked of the
    // issue itself, which the outline draws after them, by the time it was asked.
    for (let older = 1; older <= 40; older += 1) {
      await createComment(seeded.key, { body: `Older words, number ${older}.` });
    }
    const later = await createAsk(
      seeded.key,
      {
        options: [{ label: "Left" }, { label: "Right" }],
        question: "Which of the two ships first?",
      },
      asPlanner
    );

    // Both wait on the reader: the page opens on the first of them in the outline's order.
    for (const size of [DESKTOP, PHONE]) {
      const at = `${size.width}x${size.height}`;
      await page.setViewportSize({ height: size.height, width: size.width });
      await gotoIssue(page, `/issues/${seeded.key}`);
      await expectIssuePageDrawn(page);
      await expect
        .poll(() => focusedNodeId(page), {
          message: `the page does not open with the focus on the first open question that waits on the reader at ${at}`,
        })
        .toBe(first);
      expect(
        await inView(page, first),
        `the focused question is not in the viewport at ${at}`
      ).toBe(true);
    }

    // The next-question key moves to the next open question and says so, at the row, when there
    // is none; the reader's next move lets that line go.
    await page.setViewportSize({ height: DESKTOP.height, width: DESKTOP.width });
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expect.poll(() => focusedNodeId(page)).toBe(first);
    await page.keyboard.press("q");
    await expect
      .poll(() => focusedNodeId(page), { message: "q did not reach the second open question" })
      .toBe(later.id);
    expect(await inView(page, later.id), "q left the second question off screen").toBe(true);
    await page.keyboard.press("q");
    await expect(
      nodeRow(page, later.id).first().locator("[data-row-report]"),
      "with no further open question the key says nothing at the row it was pressed on"
    ).toHaveCount(1);
    expect(await pageStatus(page).count(), "the key's report is drawn at more than one row").toBe(
      1
    );
    expect(
      wordsIn(await pageStatus(page).first().innerText()).filter(
        (word) => !DECLARED_WORDS.includes(word)
      ),
      "that report uses words the page does not declare"
    ).toEqual([]);
    await page.keyboard.press("k");
    await expect(pageStatus(page), "the report stays after the reader moved on").toHaveCount(0);

    // With the first answered, the one question that waits is under forty older nodes: the page
    // opens on it, and the scroll is what puts it in the viewport.
    await answerAsk(seeded.openQuestion.askId, {
      expected_edited_at: null,
      selected: [],
      text: "1280 px.",
    });
    for (const size of [DESKTOP, PHONE]) {
      const at = `${size.width}x${size.height}`;
      await page.setViewportSize({ height: size.height, width: size.width });
      await gotoIssue(page, `/issues/${seeded.key}`);
      await expectIssuePageDrawn(page);
      await expect
        .poll(() => focusedNodeId(page), {
          message: `the page does not open with the focus on the question under forty older nodes at ${at}`,
        })
        .toBe(later.id);
      await expect
        .poll(() => inView(page, later.id), {
          message: `the question under forty older nodes is not in the viewport at ${at}`,
        })
        .toBe(true);
      expect(
        await page.evaluate(() => window.scrollY),
        `the page did not scroll to the question at ${at}, so the seed put it on the first screen`
      ).toBeGreaterThan(size.height);
    }

    // With neither waiting on the reader, the page opens with the focus on the outline's first
    // row, at the top, and the load scrolls nothing: at 390 by 664 px too, the phone of the
    // browser suite, where a load once scrolled to 249 px and hid the header.
    await answerAsk(later.id, { expected_edited_at: null, selected: ["Left"] });
    for (const size of [DESKTOP, PHONE, { height: 664, width: 390 }]) {
      const at = `${size.width}x${size.height}`;
      await page.setViewportSize({ height: size.height, width: size.width });
      await gotoIssue(page, `/issues/${seeded.key}`);
      await expectIssuePageDrawn(page);
      await expect
        .poll(async () => (await focusedNodeId(page)) !== null, {
          message: `the page opens with the focus on no row at ${at}`,
        })
        .toBe(true);
      const rows = await drawnRows(page);
      expect(
        await focusedNodeId(page),
        `the page does not open with the focus on the outline's first row at ${at}`
      ).toBe(rows[0]?.id ?? "");
      expect(await page.evaluate(() => window.scrollY), `the page opens scrolled at ${at}`).toBe(0);
    }

    // A first row taller than the window opens where it stands too (part I's reading, decision
    // 63). Its body opens with the focus (R7), and the page's one rule for placing a focused row
    // (`bringOnScreen`) puts a row taller than the screen at its top, under the band: on a long
    // answered question a load once opened at 89 px at 1440 by 900 and at 161 px at 390 by 664,
    // and hid the header. The issue's one node is a person's comment of 120 paragraphs.
    const tall = await createIssue({
      force: true,
      project: PROJECT,
      title: "An issue whose first row is taller than the window",
    });
    const long = await createComment(tall.key, {
      body: Array.from(
        { length: 120 },
        (_, paragraph) => `Paragraph ${paragraph + 1} of words taller than any window.`
      ).join("\n\n"),
    });
    for (const size of [DESKTOP, { height: 664, width: 390 }]) {
      const at = `${size.width}x${size.height} on a first row taller than the window`;
      await page.setViewportSize({ height: size.height, width: size.width });
      await gotoIssue(page, `/issues/${tall.key}`);
      await expectIssuePageDrawn(page);
      await expect
        .poll(() => focusedNodeId(page), {
          message: `the page does not open with the focus on the first row at ${at}`,
        })
        .toBe(long.id);
      expect(
        idsOf(await drawnRows(page))[0],
        `the long comment is not the outline's first row at ${at}`
      ).toBe(long.id);
      const row = nodeRow(page, long.id).first();
      await expect(rowBody(row), `the focused first row draws no body at ${at}`).toHaveCount(1);
      expect(
        await row.evaluate((element) => element.getBoundingClientRect().height),
        `the first row is not taller than the window at ${at}`
      ).toBeGreaterThan(size.height);
      expect(await page.evaluate(() => window.scrollY), `the page opens scrolled at ${at}`).toBe(0);
    }
  });
});

// c-children, as the architect's ruling 5 reads R12 (build-rulings-md): no view draws a
// children's section above the outline; the children are one group of rows of the outline, where
// the model places them, which for every child today is under the issue; each child's row holds
// its title, its status, what blocks it and who holds it, with its key as the tag; and
// `/children` and `t h` move the focus to that group. The header's bar and counts are part P's.
// SLOW(>=2s): two seeds and twenty-one cold loads - each view's address at two sizes, each waited
// out until its reads settle - and the children's keys pressed at two sizes.
test("the children are one group of rows of the outline @phone", async ({ browser }) => {
  // Two seeds and twenty-one cold loads of the issue page.
  test.setTimeout(180_000);
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    // No seed claims a child, and the check reads who holds the running one, so this test puts a
    // child in progress and claims it: that is the state the assertion is read against.
    const running = seeded.children.keys[0] ?? "";
    await patchIssue(running, { status: "in_progress" });
    await claimIssue(running, asPlanner);
    const blocked = seeded.children.blocked;

    for (const size of [DESKTOP, PHONE]) {
      await page.setViewportSize({ height: size.height, width: size.width });
      for (const preset of PRESET_ORDER) {
        // A view's body draws rows of its own shape, or nothing until its part fills its slot,
        // so the page is waited for until it has stopped reading rather than for a row.
        const traffic = watchApiRequests(page);
        await gotoIssue(page, presetPath(seeded.key, preset));
        await expectLoadSettled(traffic);
        expect(
          await childrenSection(page).count(),
          `the ${PRESET_NAMES[preset]} view draws a children's section above the outline at ${size.width} px`
        ).toBe(0);
        if (preset !== "conversation") continue;
        await expectIssuePageDrawn(page);

        // With no filter on, each child is a row of the outline, the group of them together,
        // under no other row: the issue itself is no row.
        await expect(
          childRows(page),
          `the outline draws other than one row a child at ${size.width} px`
        ).toHaveCount(seeded.children.keys.length);
        const rows = await drawnRows(page);
        const places = seeded.children.keys.map((key) => idsOf(rows).indexOf(key));
        expect(
          places.map((place, index) => place - (places[0] ?? 0) - index),
          `the children are not one group of rows at ${size.width} px`
        ).toEqual(places.map(() => 0));
        for (const key of seeded.children.keys) {
          expect(
            ancestorsIn(rows, key),
            `the child ${key} sits under another row at ${size.width} px`
          ).toEqual([]);
        }
      }
    }

    // What a child's row holds: its title as its words, how it stands, what blocks it, who
    // holds it, and its key as the tag, which opens the child's own page.
    await page.setViewportSize({ height: DESKTOP.height, width: DESKTOP.width });
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expect(childRows(page)).toHaveCount(seeded.children.keys.length);
    for (const key of seeded.children.keys) {
      const child = await getIssue(key);
      const row = childRow(page, key).first();
      await expect(rowWords(row), `the row for ${key} does not draw its title`).toContainText(
        child.title
      );
      expect(
        await row.locator("[data-child-facts]").getAttribute("data-child-status"),
        `the row for ${key} does not name its status`
      ).toBe(child.status);
      const tag = row.locator("[data-child-key]");
      await expect(tag, `the row for ${key} does not carry its key as the tag`).toHaveText(key);
      expect(await tag.getAttribute("href"), `the tag of ${key} opens another page`).toBe(
        `/issues/${key}`
      );
      // Every colour is the page's own palette: the old application's link blue is gone.
      expect(
        await row
          .locator("a")
          .evaluateAll((links) => links.map((link) => getComputedStyle(link).color)),
        `a link of the row for ${key} is drawn in the old application's blue`
      ).not.toContain("rgb(0, 105, 168)");
    }
    expect(
      (await childRow(page, blocked).first().innerText()).replace(/\s+/g, " "),
      "the blocked child's row does not name the issue that blocks it"
    ).toContain(`Blocked by ${running}`);
    expect(
      (await childRow(page, running).first().innerText()).replace(/\s+/g, " "),
      "the running child's row does not say who holds it"
    ).toMatch(/Claimed by \S/);

    // The children's own address, and `t h`, put the focus on the group's first row, on screen;
    // from there the rows are moved through like any other. `t h` takes the reader there each
    // time it is pressed: again after a move, and from a zoom that holds no child (R6, R9).
    const [firstChild = "", secondChild = ""] = seeded.children.keys;
    for (const size of [DESKTOP, PHONE]) {
      const at = `${size.width} px`;
      const toChildren = async (from: string) => {
        await page.keyboard.press("t");
        await page.keyboard.press("h");
        await expect
          .poll(() => focusedNodeId(page), {
            message: `t h ${from} does not focus the group's first row at ${at}`,
          })
          .toBe(firstChild);
      };
      const toSecondChild = async () => {
        await page.keyboard.press("j");
        await expect
          .poll(() => focusedNodeId(page), {
            message: `j does not move to the next child's row at ${at}`,
          })
          .toBe(secondChild);
      };
      await page.setViewportSize({ height: size.height, width: size.width });
      await gotoIssue(page, `/issues/${seeded.key}/children`);
      await expect
        .poll(() => focusedNodeId(page), {
          message: `/children does not focus the group's first row at ${at}`,
        })
        .toBe(firstChild);
      expect(
        await onScreen(childRow(page, firstChild).first()),
        `/children leaves it off screen at ${at}`
      ).toBe(true);
      await toSecondChild();

      await gotoIssue(page, `/issues/${seeded.key}`);
      await expect(childRows(page)).toHaveCount(seeded.children.keys.length);
      await toChildren("from the issue's address");
      await expect(page).toHaveURL(new RegExp(`/issues/${seeded.key}/children$`));
      await expect
        .poll(() => onScreen(childRow(page, firstChild).first()), {
          message: `t h leaves the first child off screen at ${at}`,
        })
        .toBe(true);
      await toSecondChild();
      await toChildren("a second time, after j");
      await toSecondChild();
      await page.keyboard.press(".");
      await expect(
        childRow(page, firstChild),
        `. on the second child still draws the first at ${at}`
      ).toHaveCount(0);
      await toChildren("from a zoom into the second child");
    }
  });

  await phase(browser, seedNoChildren, async (page, seeded) => {
    for (const preset of PRESET_ORDER) {
      await gotoIssue(page, presetPath(seeded.key, preset));
      if (preset === "conversation") await expectIssuePageDrawn(page);
      expect(
        await childrenSection(page).count(),
        `an issue with no children draws a children section with the ${PRESET_NAMES[preset]} view`
      ).toBe(0);
      expect(
        await childRows(page).count(),
        `an issue with no children draws a child's row with the ${PRESET_NAMES[preset]} view`
      ).toBe(0);
    }

    // With no child, `t h` has nowhere to go: the Keys list greys its line rather than hiding
    // it, and a press moves no focus and reports nothing at the row (R6, R9).
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    await expect(outlineRows(page).first(), "the issue draws no row").toBeVisible();
    await focusThePage(page);
    await filterRow(page).getByRole("button", { exact: true, name: CONTROL_LABELS.keys }).click();
    const keys = page.getByRole("dialog", { name: "Keyboard shortcuts" });
    await expect(keys).toBeVisible();
    expect(
      await keysListLines(page, "t h"),
      "the Keys list does not draw t h as disabled on an issue with no children"
    ).toEqual(["false"]);
    await page.keyboard.press("Escape");
    await expect(keys).toBeHidden();
    await expect(tabStopRow(page)).toBeFocused();
    const before = await focusedNodeId(page);
    await page.keyboard.press("t");
    await page.keyboard.press("h");
    await nextFrames(page);
    expect(await focusedNodeId(page), "t h moved the focus on an issue with no children").toBe(
      before
    );
    expect(
      await pageStatus(page).count(),
      "t h reported something on an issue with no children"
    ).toBe(0);
  });
});

// The stage's choice (part I's R1, R8): a view is drawn in place of the outline, never under or
// beside it, and Conversation draws the outline again, with the focus on its rows. The views'
// own bodies are their parts' (J, V, K, Q); until a part fills its slot, its view draws nothing,
// and the outline is gone all the same.
// SLOW(>=2s): the whole issue seeded and loaded, then four views turned on and off, the outline
// waited for after each, two chords pressed and the list of keys opened.
test("a view replaces the outline, and the outline comes back", async ({ browser }) => {
  test.setTimeout(120_000);
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);

    // Each view's control draws that view in the outline's place, and Conversation's draws the
    // outline back. The outline is read by its own rows rather than by a tree: every view draws
    // a tree of its own (decision 59), and no view's row carries the outline's mark.
    for (const view of ["spec", "verification", "plan", "decisions"] as const) {
      await presetToggle(page, view).click();
      await expect(page).toHaveURL(new RegExp(`${presetPath(seeded.key, view)}$`));
      await expect(
        outlineRows(page),
        `the ${PRESET_NAMES[view]} view draws the outline`
      ).toHaveCount(0);
      await presetToggle(page, "conversation").click();
      await expect
        .poll(() => outlineRows(page).count(), {
          message: `${PRESET_NAMES.conversation} does not draw the outline again after ${PRESET_NAMES[view]}`,
        })
        .toBeGreaterThan(0);
    }

    // The chords: `t s` turns Spec on, and `t c` turns every view off and leaves the focus on a
    // row of the outline.
    await focusThePage(page);
    await page.keyboard.press("t");
    await page.keyboard.press("s");
    await expect(page).toHaveURL(new RegExp(`${presetPath(seeded.key, "spec")}$`));
    await expect(outlineRows(page), "t s left the outline drawn under the Spec view").toHaveCount(
      0
    );
    await page.keyboard.press("t");
    await page.keyboard.press("c");
    await expect
      .poll(() => outlineRows(page).count(), { message: "t c did not draw the outline again" })
      .toBeGreaterThan(0);
    await expect
      .poll(() => focusedNodeId(page), {
        message: "t s then t c did not leave the focus on a row of the outline",
      })
      .not.toBeNull();

    // Keys puts the focus on the outline's tab-stop row before it opens the list of keys, so the
    // list names the outline's keys and closing it gives the reader that row.
    await focusThePage(page);
    await filterRow(page).getByRole("button", { exact: true, name: CONTROL_LABELS.keys }).click();
    const keys = page.getByRole("dialog", { name: "Keyboard shortcuts" });
    await expect(keys).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(keys).toBeHidden();
    await expect(
      tabStopRow(page),
      "Keys did not leave the focus on the outline's tab-stop row"
    ).toBeFocused();
  });
});

// Reading a thread on the issue page marks its Inbox row read, as the panel's list of the
// conversation did on its own address: the row of the thread's newest reply, on screen for a
// second, marks the thread read through that reply (`outline-thread-read.tsx`). Before the
// outline, G's rows marked it read too, with every branch open, so the last reading of this row
// is a guard that passes at the commit before the outline as well. The outline's own reading is
// the one before it: a thread the outline folds is not read, however long the page stays open,
// until the reader opens it.
// SLOW(>=2s): a seeded thread and a reply, the Inbox polled, a load left folded for a fixed two
// seconds, then the thread opened and waited on for the dwell that marks it read.
test("a thread read on the outline is read in the Inbox", async ({ browser }) => {
  await phase(browser, seedReplyNesting, async (page, seeded) => {
    // An agent replies in alice's thread, which makes the thread unread in her Inbox.
    const answer = await createComment(
      seeded.key,
      { body: "An agent's answer alice has not read.", reply_to: seeded.head },
      asPlanner
    );
    // The route's own default is the caller's unread threads alone.
    const unread = async () =>
      ((await listMyReplies({ issue: seeded.key }, { login: "alice" })).rows ?? []).some(
        (row) => row.thread_id === seeded.head
      );
    await expect.poll(unread, { message: "the agent's reply left the thread read" }).toBe(true);

    // The page opens at 1440 by 900 with the thread folded, since it holds no open question.
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    await expect(nodeRow(page, seeded.head), "the thread's head draws no row").toHaveCount(1);
    // A mark's absence is the claim, so the wait is a span: two seconds, twice the dwell that
    // marks a thread read, after which the folded thread is still unread.
    await page.waitForTimeout(2_000);
    expect(await unread(), "the thread was marked read before the reader opened it").toBe(true);
    expect(
      rowOf(await drawnRows(page), seeded.head)?.expanded,
      "the thread is not folded as the page opens"
    ).toBe("false");

    // Opened, with its newest write's row on screen, the thread is read.
    await unfoldEveryBranch(page);
    const newest = nodeRow(page, answer.id).first();
    await newest.scrollIntoViewIfNeeded();
    await expect(newest, "the agent's reply draws no row once the thread is open").toBeVisible();
    await expect.poll(unread, { message: "reading the thread did not mark it read" }).toBe(false);
  });
});

// c-reflow, on words the seeded issue does not carry. R17 binds the page a reader opens, not one
// seed's text: a statement whose words name a file path holds a run of 108 characters with no
// space in it, which is longer than the row that draws it at every width this reads. Found by
// the tester on 2026-11-09 at 39649c0f269d, where the Spec preset's requirement list drew that
// run in a span that neither wrapped it nor gave it its own line, and the whole page scrolled
// sideways: 884 px of page in a 390 px viewport. Read here on the outline, with no view on.
// SLOW(>=2s): a seeded issue loaded cold at three widths, each waited on until its words draw and
// measured for its fit.
test("a statement whose words hold an unbroken run keeps the outline inside its viewport @phone", async ({
  browser,
}) => {
  await phase(browser, seedUnbrokenWords, async (page, seeded) => {
    for (const width of [320, 390, 1440]) {
      await page.setViewportSize({ height: width < 1024 ? PHONE.height : DESKTOP.height, width });
      await gotoIssue(page, `/issues/${seeded.key}`);
      await expectIssuePageDrawn(page);
      // The words are on the page before it is measured, so a fit read before the rows draw
      // cannot pass for the fit of a page that holds them.
      await expect(
        page.getByText(UNBROKEN_PATH, { exact: false }).first(),
        `the outline draws none of the statement's words at ${width} px`
      ).toBeVisible();
      await assertPageFits(page);
    }
  });
});

// c-reflow again, on the other surface the same run reaches: a child's own title, which is the
// words of the child's row of the outline (the architect's ruling 5). Measured at 320 px with
// the same 108 character path on 2026-11-09 at 454f2a3107a6, in the children's section the page
// drew then: 807 px of text in a 288 px box and a page 823 px wide.
// SLOW(>=2s): a seeded issue and its child, loaded cold at three widths, each waited on until the
// child's row draws and measured for its fit.
test("a child whose title holds an unbroken run keeps the page inside its viewport @phone", async ({
  browser,
}) => {
  await phase(browser, seedUnbrokenChildTitle, async (page, seeded) => {
    for (const width of [320, 390, 1440]) {
      await page.setViewportSize({ height: width < 1024 ? PHONE.height : DESKTOP.height, width });
      await gotoIssue(page, `/issues/${seeded.key}`);
      await expectIssuePageDrawn(page);
      // The title is on the page before it is measured, so a fit read before the rows draw
      // cannot pass for the fit of a page that holds them.
      await expect(
        childRow(page, seeded.childKey).first(),
        `the outline draws no row for ${seeded.childKey} at ${width} px`
      ).toBeVisible();
      await expect(
        page.getByText(UNBROKEN_PATH, { exact: false }).first(),
        `the child's row draws none of its title's words at ${width} px`
      ).toBeVisible();
      await assertPageFits(page);
    }
  });
});

// c-tree, the crumb (the architect's ruling 1): where the tree is read one node at a time, a
// crumb is the node's first line by R1's rule, cut at a word, never cut again mid-word by its
// box, and a line that is a whole sentence carries no ellipsis after its own full stop. The
// issue's crumb is its key (ruling 5). The Contents entry's half is part J's.
// SLOW(>=2s): the whole issue seeded and loaded at a phone's size on a reply's address, waited on
// until the reply has the focus, then zoomed into and measured.
test("a crumb draws its node's first line, cut at a word @phone @phone-only", async ({
  browser,
}) => {
  test.setTimeout(120_000);
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    // Zoomed into a reply of a thread, the issue's crumb is its key and the thread's is its
    // stored first line, whole. The thread is the one the server says the reply answers.
    const comments = await listComments(seeded.key);
    const reply = comments.find((one) => one.id === seeded.comments.replies[0]);
    const thread = comments.find((one) => one.id === reply?.reply_to);
    expect(thread, "the server holds no thread the seeded reply answers").toBeDefined();
    await page.setViewportSize({ height: PHONE.height, width: PHONE.width });
    await gotoIssue(page, `/issues/${seeded.key}/comments/${seeded.comments.replies[0] ?? ""}`);
    await expect
      .poll(() => focusedNodeId(page), { message: "the reply's address does not focus its row" })
      .toBe(seeded.comments.replies[0] ?? "");
    await page.keyboard.press(".");
    const crumbs = tree(page).locator("xpath=..").locator("[data-crumb]");
    await expect.poll(() => crumbs.count(), { message: "no crumb is drawn" }).toBeGreaterThan(1);
    const texts = (await crumbs.allInnerTexts()).map((crumb) => crumb.trim());
    expect(texts[0], "the issue's crumb repeats its title").toBe(seeded.key);
    expect(texts[1], "the thread's crumb is not its stored first line").toBe(
      firstLineOf(thread?.body ?? "").line
    );
    for (const crumb of texts.slice(1)) {
      expect(crumb, `the crumb "${crumb}" ends a sentence and then an ellipsis`).not.toMatch(
        /[.!?]…$/
      );
      expect(crumb.length, `the crumb "${crumb}" is longer than a first line`).toBeLessThanOrEqual(
        101
      );
    }
    expect(
      await crumbs.evaluateAll(
        (all) =>
          all.filter(
            (crumb) =>
              getComputedStyle(crumb).textOverflow === "ellipsis" ||
              crumb.scrollWidth > crumb.clientWidth + 1
          ).length
      ),
      "a crumb is cut by its box, which cuts inside a word"
    ).toBe(0);
    // Nothing the zoom draws makes the page scroll sideways (R12).
    await assertPageFits(page);
  });
});

// c-focus-opens.
// SLOW(>=2s): the whole issue seeded and loaded, every row hovered, every link rested on past the
// delay a card opens after, the rows read three times, two controls outside the outline pressed,
// one of them a view's, which the outline is waited for after, and the Inbox loaded.
test("a body opens on focus and nothing opens on hover", async ({ browser }) => {
  // The row rests the pointer on every reference the page draws, each for longer than a card
  // takes to open elsewhere, which is a second or two of absences on top of the row's own walk.
  test.setTimeout(120_000);
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    await gotoIssue(page, `/issues/${seeded.key}`);

    // One body is drawn: the focused node's.
    await focusThePage(page);
    await page.keyboard.press("Tab");
    const focused = await focusedNodeId(page);
    expect(
      focused,
      "Tab put the focus on no row of the tree, so no body can open on focus"
    ).not.toBeNull();
    await expectIssuePageDrawn(page);
    expect(await nodeBodies(page).count(), "more than the focused node's body is drawn").toBe(1);
    expect(
      (await drawnRows(page)).filter((row) => row.body).map((row) => row.id),
      "the body drawn is not the focused node's"
    ).toEqual([focused]);

    // A node pinned open keeps its body when the focus leaves it.
    await page.keyboard.press("o");
    expect(await pinnedBodies(page).count(), "o pinned no body open").toBe(1);
    await page.keyboard.press("j");
    expect(
      await nodeBodies(page).count(),
      "the pinned body and the focused node's are not the two bodies drawn"
    ).toBe(2);
    await page.keyboard.press("k");
    await page.keyboard.press("o");
    expect(await pinnedBodies(page).count(), "o did not close the pinned body").toBe(0);

    // A press on a control of the page outside the outline fires with a row's body open (the
    // architect's decision 105). The press focuses the control as it goes down, and the body
    // closing as the focus left the outline shortened the page under the pointer: the press
    // ended on something else, and the control never fired. The body stays drawn until another
    // row takes the focus or the reader presses Escape.
    //
    // First a control under the focused row, which a body closing above it moves. Nothing of the
    // page's own stands under the outline at this head, so a stand-in does
    // (`standInControlBelow`), in the middle of the screen, under the outline's last row, which
    // End focuses.
    await page.keyboard.press("End");
    const last = tabStopRow(page);
    await expect(rowBody(last), "End opened no body on the outline's last row").toHaveCount(1);
    const below = await standInControlBelow(page);
    await below.control.evaluate((control) => control.scrollIntoView({ block: "center" }));
    await below.control.click();
    expect(
      await below.presses(),
      "a press on a control under the focused row, with the row's body open, did not fire it"
    ).toBe(1);
    await expect(rowBody(last), "the press closed the focused row's body").toHaveCount(1);
    // Escape on the page closes the body the press kept.
    await page.keyboard.press("Escape");
    await expect(nodeBodies(page), "Escape on the page left the kept body drawn").toHaveCount(0);
    await below.remove();

    // Then a view control above the outline: Spec, pressed with a row's body open, draws the
    // Spec view in the outline's place, and Conversation draws the outline again.
    await focusRow(page, focused ?? "");
    await presetToggle(page, "spec").click();
    await expect(
      page,
      "a press on Spec with a row's body open did not open the Spec view"
    ).toHaveURL(new RegExp(`${presetPath(seeded.key, "spec")}$`));
    await expect(outlineRows(page), "the Spec view draws the outline").toHaveCount(0);
    await presetToggle(page, "conversation").click();
    await expect
      .poll(() => outlineRows(page).count(), {
        message: "Conversation does not draw the outline again after Spec",
      })
      .toBeGreaterThan(0);

    // With the keyboard focus on no control nothing is open, and the pointer opens nothing either.
    await focusThePage(page);
    expect(
      await nodeBodies(page).count(),
      "a body is drawn with the keyboard focus on no control"
    ).toBe(0);
    const before = await treeText(page);
    const rows = nodeRows(page);
    const count = await rows.count();
    for (let index = 0; index < count; index += 1) {
      await rows.nth(index).hover();
      expect(
        await nodeBodies(page).count(),
        `the pointer over row ${index} opened a body with the focus elsewhere`
      ).toBe(0);
    }
    expect(await treeText(page), "the pointer crossing every row changed the tree's text").toBe(
      before
    );
    expect(
      await tooltipTexts(page),
      "the page carries tooltips, which a reader on a phone or on the keyboard never sees"
    ).toEqual([]);

    // No link opens a card on hover either, a link in an open body included: the issue page is
    // worked from the keyboard (R6 of the root spec). A decision's words name the requirements
    // it shaped, each as a chip in place and again as a chip at the end of its words, and its
    // row is pinned open, so the pointer rests on every link the page draws - the children's,
    // the panel's and that row's - with the keyboard focus elsewhere. No link of the outline is
    // a preview's trigger at this head: G's links in a node's words and its chips carry no
    // `data-dispatch-ref` or `data-dispatch-href` (`row-references.ts`, `NodeRow.tsx`'s `Chip`),
    // so a focused one opens no card either, and the card a focused link opens is not part I's
    // to read.
    const decisionsSlug = (await getArtifact(seeded.decisionsArtifactId)).slug;
    const decision = nodeRow(page, blockNodeId(decisionsSlug, seeded.decisions[0] ?? "")).first();
    await decision.focus();
    await page.keyboard.press("o");
    await focusThePage(page);
    await expect
      .poll(() => rowWords(decision).locator("a[href]:visible").count(), {
        message: "the pinned decision's words draw no link",
      })
      .toBeGreaterThan(0);
    const links = issuePage(page).locator(
      "a[href]:visible, [data-dispatch-ref]:visible, [data-dispatch-href]:visible"
    );
    const linkCount = await links.count();
    for (let index = 0; index < linkCount; index += 1) {
      await links.nth(index).hover();
      // A pointer resting on the link past the delay a card opens after on every other page:
      // the absence is the claim, so the wait is the span the card would have taken to open.
      await page.waitForTimeout(REF_PREVIEW_OPEN_DELAY_MS + 200);
      expect(
        await page.getByRole("tooltip").count(),
        `the pointer resting on link ${index} of the issue page opened a card`
      ).toBe(0);
    }
    await page.mouse.move(0, 0);
    await decision.focus();
    await page.keyboard.press("o");
    await focusThePage(page);

    // Nothing is carried by colour: each state and type has a word and a shape no other carries,
    // and the reading holds with the colours forced. A greyscale filter is not the test - today's
    // three muted states already differ in luminance and would pass one.
    const drawn = await drawnRows(page);
    expectRowVocabulary(drawn, await rowTags(page));
    const states = statesDrawn(drawn);
    expect(
      [...new Set(states.values())].length,
      `two states are drawn the same way: ${JSON.stringify([...states])}`
    ).toBe(states.size);
    // Each typed kind's tag carries its shape, which is the first shape its row draws; a turn
    // of the conversation draws no tag (R34), and its author's shape says whose words it is.
    const types = new Map(
      drawn
        .filter((row) => !UNTYPED_KINDS.includes(row.type ?? ""))
        .map((row) => [row.type ?? "", row.glyphs[0]?.token ?? ""])
    );
    expect(
      [...new Set(types.values())].length,
      `two types draw the same shape: ${JSON.stringify([...types])}`
    ).toBe(types.size);
    await page.emulateMedia({ forcedColors: "active" });
    const forced = await drawnRows(page);
    expectRowVocabulary(forced, await rowTags(page));
    expect(
      [...statesDrawn(forced)],
      "the states are told apart differently once the colours are forced"
    ).toEqual([...states]);

    // Another page keeps its hover card: this change touches no page but the issue's, and the
    // Inbox's owner link opens a card for a resting pointer as it always has.
    await page.emulateMedia({ forcedColors: "none" });
    await page.goto("/");
    const owner = page.locator("[data-inbox-row] [data-dispatch-ref]").first();
    await expect(owner, "the Inbox lists no row of the seeded issue's open question").toBeVisible();
    await owner.hover();
    await expect(
      page.getByRole("tooltip"),
      "the Inbox's owner link opened no card for a resting pointer"
    ).toBeVisible();
  });
});

// SLOW(>=2s): the whole issue seeded and loaded in a context of its own, then some twenty keys
// pressed with every row read again after most of them, and a caret put in a node.
test("the tree's keys move, fold, zoom and open the focused node", async ({ browser }) => {
  const { context, page, seeded } = await openTree(browser);
  try {
    const order = await visibleIds(page);
    expect(
      order.length,
      "the keys have no rows to move through: the tree draws none"
    ).toBeGreaterThan(3);
    await expectTreeDrawn(page);

    // j, k and the arrows move to the next and previous visible row.
    await page.keyboard.press("Tab");
    expect(await focusedRow(page), "Tab did not put the reader on the first row").toBe(order[0]);
    for (const [index, step] of [
      [1, "j"],
      [2, "ArrowDown"],
      [1, "k"],
      [0, "ArrowUp"],
    ] as const) {
      await page.keyboard.press(step);
      expect(await focusedRow(page), `${step} did not move to row ${index}`).toBe(order[index]);
      await expectFocusedRowClear(page, step);
    }

    // Home and End reach the first and last visible row.
    await page.keyboard.press("End");
    expect(await focusedRow(page), "End did not reach the last visible row").toBe(
      order[order.length - 1]
    );
    await expectFocusedRowClear(page, "End");
    await page.keyboard.press("Home");
    expect(await focusedRow(page), "Home did not reach the first visible row").toBe(order[0]);

    // The right arrow unfolds the focused node and then moves into its first child; the left
    // arrow moves back to the parent and then folds it. The branch is one above the drawn
    // levels, because that is where unfolding draws the children as rows of their own: a
    // branch below them folds to one line the open-deeper key is for (`tree-fold.ts`), which
    // is no child row for an arrow to move into. The page opens folded to the frontier, so
    // there are plenty either way.
    const folded = (await drawnRows(page)).find(
      (row) => row.expanded === "false" && Number(row.level) < DRAWN_LEVELS
    );
    expect(
      folded,
      "no branch above the drawn levels is folded, so the arrows have nothing to unfold"
    ).toBeDefined();
    const branch = folded?.id ?? "";
    await nodeRow(page, branch).first().focus();
    await page.keyboard.press("ArrowRight");
    expect(
      (await drawnRows(page)).find((row) => row.id === branch)?.expanded,
      "the right arrow did not unfold the focused node"
    ).toBe("true");
    expect(
      await focusedRow(page),
      "the right arrow left the focused node before unfolding it"
    ).toBe(branch);
    // The branch's first child is the row after it one level deeper, which is where the second
    // right arrow has to land: "not the branch" would also hold for a focus that was lost or
    // that moved to the branch's own sibling.
    const unfolded = (await drawnRows(page)).filter((row) => row.visible);
    const at = unfolded.findIndex((row) => row.id === branch);
    const firstChild = unfolded[at + 1];
    expect(
      firstChild?.level,
      "the unfolded branch draws no row under it, so it has no first child to move into"
    ).toBe(String(Number(unfolded[at]?.level ?? "0") + 1));
    await page.keyboard.press("ArrowRight");
    expect(
      await focusedRow(page),
      "the second right arrow did not move into the branch's first child"
    ).toBe(firstChild?.id);
    await page.keyboard.press("ArrowLeft");
    expect(await focusedRow(page), "the left arrow did not move to the parent").toBe(branch);
    await page.keyboard.press("ArrowLeft");
    expect(
      (await drawnRows(page)).find((row) => row.id === branch)?.expanded,
      "the second left arrow did not fold the focused node"
    ).toBe("false");

    // o pins the focused node's body open, and again closes it.
    await page.keyboard.press("o");
    expect(await pinnedBodies(page).count(), "o pinned no body open").toBe(1);
    await page.keyboard.press("o");
    expect(await pinnedBodies(page).count(), "o did not close the pinned body").toBe(0);

    // . zooms into the focused node and , comes back out one level.
    const whole = await visibleIds(page);
    await page.keyboard.press(".");
    const zoomed = await visibleIds(page);
    expect(
      zoomed.length,
      "the zoom key drew the same rows, so it zoomed into nothing"
    ).toBeLessThan(whole.length);
    expect(zoomed[0], "the zoom key did not make the focused node the tree's root").toBe(branch);
    await page.keyboard.press(",");
    expect(await visibleIds(page), "the key back out did not restore the level above").toEqual(
      whole
    );

    // q moves to the next open question, whatever its depth.
    await page.keyboard.press("q");
    const question = await focusedRow(page);
    expect(
      (await drawnRows(page)).find((row) => row.id === question)?.type,
      "the next-question key reached a row that is no open question"
    ).toBe("ask");

    // w moves into the decision behind the focused node.
    const specSlug = (await getArtifact(seeded.specArtifactId)).slug;
    const decisionsSlug = (await getArtifact(seeded.decisionsArtifactId)).slug;
    await nodeRow(page, blockNodeId(specSlug, requirementBlockId(1)))
      .first()
      .focus();
    await page.keyboard.press("w");
    expect(await focusedRow(page), "w did not move into the decision behind the node").toBe(
      blockNodeId(decisionsSlug, seeded.decisions[0] ?? "")
    );

    // z cycles the whole tree: first lines, first lines with bodies to depth two, everything.
    await focusThePage(page);
    const firstLines = await visibleIds(page);
    await page.keyboard.press("z");
    expect(
      await nodeBodies(page).count(),
      "the second state of the cycle draws no bodies"
    ).toBeGreaterThan(1);
    await page.keyboard.press("z");
    expect(
      (await visibleIds(page)).length,
      "the third state of the cycle draws no more rows than the first"
    ).toBeGreaterThan(firstLines.length);
    await page.keyboard.press("z");
    expect(await visibleIds(page), "the cycle did not come back to the first lines").toEqual(
      firstLines
    );

    // Enter puts the caret in the focused node; Escape leaves the caret for the row, then the
    // tree. This extends the binding the page already has and adds none.
    const second = blockNodeId(specSlug, requirementBlockId(2));
    await nodeRow(page, second).first().focus();
    await page.keyboard.press("Enter");
    await expect
      .poll(() => caretInNode(page), { message: "Enter did not put the caret in the focused node" })
      .toBe(true);
    await page.keyboard.press("Escape");
    expect(await caretInNode(page), "Escape did not leave the caret").toBe(false);
    expect(await focusedRow(page), "Escape left the row as well as the caret").toBe(second);
    await page.keyboard.press("Escape");
    expect(await focusedRow(page), "the second Escape did not leave the tree").toBeNull();
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the whole issue seeded and loaded in a context of its own, then some thirty keys
// pressed, each followed by a read of where the focus is.
test("Tab enters and leaves the tree at one stop and every move keeps the row on screen", async ({
  browser,
}) => {
  const { context, page } = await openTree(browser);
  try {
    const order = await visibleIds(page);

    // One stop in: Tab reaches the tree, and the next Tab leaves it rather than stepping through
    // its rows, as the WAI-ARIA tree pattern requires.
    await page.keyboard.press("Tab");
    expect(await focusInside(tree(page)), "Tab did not enter the tree").toBe(true);
    await expectTreeDrawn(page);
    await page.keyboard.press("j");
    const inside = await focusedRow(page);
    expect(inside, "j moved to no row inside the tree").toBe(order[1]);
    await page.keyboard.press("Tab");
    expect(
      await focusInside(tree(page)),
      "Tab stepped to another row of the tree instead of leaving it"
    ).toBe(false);
    await page.keyboard.press("Shift+Tab");
    expect(await focusInside(tree(page)), "Shift+Tab did not come back into the tree").toBe(true);
    expect(await focusedRow(page), "Shift+Tab came back to another row than the one left").toBe(
      inside
    );

    // The focused row is inside the viewport and clear of the sticky header after every move,
    // including the ones that scroll the page.
    for (let step = 0; step < Math.min(order.length - 1, 12); step += 1) {
      await page.keyboard.press("j");
      await expectFocusedRowClear(page, `move ${step + 1} with j`);
    }
    await page.keyboard.press("End");
    await expectFocusedRowClear(page, "End");
    await page.keyboard.press("Home");
    await expectFocusedRowClear(page, "Home");

    // The box at the foot of the screen: part L draws one and publishes its height on the
    // document element (`FOOT_HEIGHT_PROPERTY` of `identity-block.ts`), and the page brings the
    // focused row clear of it as it does of the band at the top. A 200 px stand-in for that box:
    // every move keeps the focused row whole above it. Measured at this part's head with only
    // the height published and no box: the moves from the seventh on lift the row to 8 px above
    // it, and End leaves the last row under it, with the page scrolled as far as it goes.
    const takeFootAway = await standInFootBox(page, 200);
    for (let step = 0; step < Math.min(order.length - 1, 12); step += 1) {
      await page.keyboard.press("j");
      await expectFocusedRowAboveFoot(page, `move ${step + 1} with j`);
    }
    await page.keyboard.press("End");
    await expectFocusedRowAboveFoot(page, "End");
    await takeFootAway();
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): the whole issue seeded and loaded in a context of its own, for one read of its rows.
test("the tree answers the WAI-ARIA tree pattern's roles and properties", async ({ browser }) => {
  const { context, page } = await openTree(browser);
  try {
    const rows = await drawnRows(page);
    expect(
      rows.length,
      'the tree pattern has nothing to answer for: no role="treeitem" row is drawn'
    ).toBeGreaterThan(0);
    await expectTreeDrawn(page);
    expect(
      rows.filter((row) => row.role !== "treeitem").map((row) => row.id),
      "these rows of the tree are no treeitem"
    ).toEqual([]);
    expect(
      rows.filter((row) => !/^[1-9]\d*$/.test(row.level ?? "")).map((row) => row.id),
      "these rows carry no aria-level"
    ).toEqual([]);

    // A row that holds children says whether it is open; a row that holds none says nothing.
    const levels = rows.map((row) => Number(row.level ?? "0"));
    const holdsChildren = rows.map(
      (row, index) => (levels[index + 1] ?? 0) > (levels[index] ?? 0) || row.foldedCount !== null
    );
    expect(
      rows
        .filter((row, index) => holdsChildren[index] === true && row.expanded === null)
        .map((row) => row.id),
      "these rows hold children and carry no aria-expanded"
    ).toEqual([]);
    expect(
      rows
        .filter((row, index) => holdsChildren[index] !== true && row.expanded !== null)
        .map((row) => row.id),
      "these rows hold no children and carry aria-expanded all the same"
    ).toEqual([]);

    // The tree is one tab stop: it roves with one tabbable row, or it names the row it is on.
    const tabbable = rows.filter((row) => row.tabIndex === 0);
    const named = await treeActiveDescendant(page);
    expect(
      `${tabbable.length}/${named === null ? "none" : "named"}`,
      `the tree has ${tabbable.length} tabbable rows and ${named === null ? "no" : "an"} aria-activedescendant, so it is not one stop`
    ).toMatch(/^(1\/none|0\/named|1\/named)$/);
  } finally {
    await context.close();
  }
});

// c-chain.
// SLOW(>=2s): the whole issue seeded, loaded until its reads settle and unfolded press by press,
// then its rows read whole five times or more and seven keys pressed.
test("the chain is reachable and only gaps that tell a node apart are drawn", async ({
  browser,
}) => {
  test.setTimeout(120_000);
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    const specSlug = (await getArtifact(seeded.specArtifactId)).slug;
    const decisionsSlug = (await getArtifact(seeded.decisionsArtifactId)).slug;
    const verificationSlug = (await getArtifact(seeded.verificationArtifactId)).slug;
    const planSlug = (await getArtifact(seeded.planArtifactId)).slug;
    const informed = blockNodeId(specSlug, requirementBlockId(1));
    const decision = blockNodeId(decisionsSlug, seeded.decisions[0] ?? "");
    const check = blockNodeId(verificationSlug, seeded.checks.passed);
    const results = (await getIssueResults(seeded.key)).checks ?? [];
    const latest = results.find((one) => one.block_id === seeded.checks.passed)?.results?.[0];
    expect(latest, "the seed recorded no result against the check that serves r1").toBeDefined();

    const settling = watchApiRequests(page);
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectLoadSettled(settling);
    await unfoldEveryBranch(page);

    // The gaps the page draws are those that tell a node apart and name work somebody owes: each
    // requirement no check serves, and the plan task that serves no requirement - and no other
    // node draws any. The check whose latest result records no negative control is one of those
    // others: dispatch://INFRA-1253 R3 refuses such a result, so the page names nothing it lacks.
    const coverage = await getIssueCoverage(seeded.key);
    const wanted = new Map<string, string[]>();
    for (const requirement of coverage.requirements ?? []) {
      if (requirement.checks === 0) {
        wanted.set(blockNodeId(specSlug, requirement.block_id), ["no_check"]);
      }
    }
    wanted.set(blockNodeId(planSlug, planTaskBlockId(seeded.planTasks.length)), ["nobody_asked"]);
    const uncontrolled = (await getIssueResults(seeded.key)).checks?.find(
      (one) => one.block_id === seeded.checks.uncontrolled
    );
    expect(
      uncontrolled?.results?.[0]?.negative_control,
      "the seed's uncontrolled check's latest result records a negative control"
    ).toBe("");
    const drawnGaps = async () => {
      const drawn = new Map<string, string[]>();
      for (const row of await drawnRows(page)) {
        if (row.gapKinds.length > 0) drawn.set(row.id ?? "", [...row.gapKinds]);
      }
      return Object.fromEntries([...drawn].sort());
    };
    await expect
      .poll(drawnGaps, {
        message: "the page draws a gap on a node it does not tell apart, or none on one it does",
      })
      .toEqual(Object.fromEntries([...wanted].sort()));
    await expect
      .poll(async () => rowsByKind(await drawnRows(page)), {
        message: "the unfolded tree does not draw a row for every node the seed wrote",
      })
      .toEqual(rowsOfTheWholeIssue(seeded));

    // From the requirement to its checks and their latest results: each is drawn under it, and
    // the arrow keys walk down to them from the requirement's row. The decision's chip that
    // names the requirement, and following it, are part J's step of the chain.
    expect(await parentOfRow(page, check), "the check that serves r1 is not under it").toBe(
      informed
    );
    expect(
      await parentOfRow(page, latest?.id ?? ""),
      "the check's latest result is not under the check"
    ).toBe(check);
    await nodeRow(page, informed).first().focus();
    const walked: (string | null)[] = [];
    for (let step = 0; step < 6; step += 1) {
      await page.keyboard.press("ArrowDown");
      walked.push(await focusedNodeId(page));
    }
    expect(walked, "the arrows from the requirement do not reach its check").toContain(check);
    expect(walked, "the arrows from the requirement do not reach the check's result").toContain(
      latest?.id ?? ""
    );

    // From the check, `w` reaches back to the decision that informed its requirement.
    await nodeRow(page, check).first().focus();
    await page.keyboard.press("w");
    await expect
      .poll(() => focusedNodeId(page), {
        message: "w on the check does not move the focus to the decision behind its requirement",
      })
      .toBe(decision);

    // No gap that every node of a kind carries is drawn: no requirement and no decision says
    // anything about a source, a read-back or an author. The head's gap and the Spec view's
    // readings are part J's.
    expect(
      (await drawnRows(page))
        .filter((row) => row.type === "requirement" || row.type === "decision")
        .flatMap((row) => row.gapKinds)
        .filter((kind) => kind !== "no_check"),
      "a requirement or a decision draws a gap its whole kind carries"
    ).toEqual([]);
    for (const words of ["No source given", "Not confirmed in Dispatch", "No author in Dispatch"]) {
      expect(await issuePage(page).getByText(words).count(), `the page draws "${words}"`).toBe(0);
    }
  });
});

// The tester's F16: what a key says when it has nowhere to go - `w` on a row no decision stands
// behind - is drawn at the row it was pressed on, once, and the reader's next move lets it go.
// SLOW(>=2s): a seeded thread loaded cold and one of its rows focused, for two keys pressed.
test("a key with nowhere to go says so at its row, and the next move lets it go", async ({
  browser,
}) => {
  test.setTimeout(120_000);
  await phase(browser, seedReplyNesting, async (page, seeded) => {
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    await focusRow(page, seeded.answeredAsk);
    await page.keyboard.press("w");
    await expect(
      nodeRow(page, seeded.answeredAsk).first().locator("[data-row-report]"),
      "the key's report is not drawn at the row it was pressed on"
    ).toHaveCount(1);
    expect(
      await issuePage(page).locator("[data-row-report]").count(),
      "the key's report is drawn at more than one place"
    ).toBe(1);
    expect(
      (await issuePage(page).getByRole("status").allInnerTexts()).filter(
        (words) => words.trim() !== ""
      ).length,
      "the key's report is drawn a second time away from the row"
    ).toBe(1);
    await page.keyboard.press("j");
    await expect(
      issuePage(page).locator("[data-row-report]"),
      "the report stays after the reader moved on"
    ).toHaveCount(0);
  });
});

// Editing in a node (part I's R10). Enter on a row whose node is a block of one of the issue's
// documents puts the caret at the start of its words, in that document's one room: the row takes
// the page's one editor of the document, so the room gets no second connection. A second reader
// who does the same on their own page is in that room too, and the first one's row draws the
// second one's caret. What is typed is stored. Escape gives the same row the focus, and a second
// Escape leaves the outline. A comment's row holds no document's text, and the key says so at
// the row.
// SLOW(>=2s): the whole issue seeded and loaded, a second reader's context loading it too, both
// carets put in the spec's live room, and two reloads or more reading what was stored.
test("Enter puts the caret in a node, and Escape gives the row back", async ({ browser }) => {
  test.setTimeout(120_000);
  const { context, page, seeded } = await openTree(browser);
  try {
    await expectTreeDrawn(page);
    const specSlug = (await getArtifact(seeded.specArtifactId)).slug;
    const statement = blockNodeId(specSlug, requirementBlockId(2));
    const typed = "Caretword ";
    const sockets = countDocumentSockets(page, seeded.specArtifactId);

    // Enter on the statement: the caret is in its words, in the spec's one room.
    await focusRow(page, statement);
    await page.keyboard.press("Enter");
    await expect
      .poll(() => caretInNode(page), { message: "Enter did not put the caret in the row" })
      .toBe(true);
    await expect
      .poll(sockets, { message: "the spec's room took other than one connection" })
      .toBe(1);
    await page.keyboard.type(typed);

    // A second reader, bob, presses Enter on the same statement on his own page: alice's row
    // draws his caret, and neither page's room takes a second connection. A peer's caret on the
    // very spot of a focused editor's own is not drawn (`collab-cursor-plugin.ts`), so he comes
    // in after alice has typed, which moved hers past the start of the words his lands on. Both
    // pages are 1280 px wide, the narrowest at which an editor takes part in the room's carets
    // (`ProofDocument.tsx`).
    const bob = await asUser(browser, "bob");
    try {
      await withIssuePage(bob, "bob");
      const bobPage = await bob.newPage();
      await bobPage.setViewportSize({ height: 900, width: 1280 });
      const bobSockets = countDocumentSockets(bobPage, seeded.specArtifactId);
      await gotoIssue(bobPage, `/issues/${seeded.key}`);
      await expectTreeDrawn(bobPage);
      await focusRow(bobPage, statement);
      await bobPage.keyboard.press("Enter");
      await expect
        .poll(() => caretInNode(bobPage), { message: "Enter did not put bob's caret in the row" })
        .toBe(true);
      await expect(
        nodeRow(page, statement).first().locator(cursorLabel(page, "bob")),
        "alice's row draws no caret of bob's, who is editing the same statement"
      ).toBeVisible();
      await expect
        .poll(bobSockets, { message: "bob's room took other than one connection" })
        .toBe(1);
      expect(sockets(), "alice's room took a second connection when bob came in").toBe(1);
    } finally {
      await bob.close();
    }

    // Escape leaves the caret on that row, and a second Escape leaves the outline.
    await page.keyboard.press("Escape");
    expect(await caretInNode(page), "Escape did not take the caret out").toBe(false);
    expect(await focusedRow(page), "Escape gave the focus to another row").toBe(statement);
    await page.keyboard.press("Escape");
    expect(await focusedRow(page), "the second Escape did not leave the outline").toBeNull();

    // What was typed is stored: a reload reads it, and the same keys remove it.
    const storedWords = async () => {
      await page.reload();
      await expectTreeDrawn(page);
      const row = await focusRow(page, statement);
      return (await rowWords(row).innerText()).replace(/\s+/g, " ");
    };
    expect(await storedWords(), "the word typed in the row was not stored").toContain(typed.trim());
    await page.keyboard.press("Enter");
    await expect
      .poll(() => caretInNode(page), { message: "Enter did not put the caret in the row" })
      .toBe(true);
    for (let at = 0; at < typed.length; at += 1) await page.keyboard.press("Delete");
    await page.keyboard.press("Escape");
    await expect
      .poll(storedWords, { message: "the word typed in the row was not removed" })
      .not.toContain(typed.trim());

    // A comment's row holds no document's text: Enter puts no caret, and the row says so.
    const comment = seeded.comments.threads[0] ?? "";
    await focusRow(page, comment);
    await page.keyboard.press("Enter");
    await expect(
      nodeRow(page, comment).first().locator("[data-row-report]"),
      "Enter on a comment's row did not say it has no text"
    ).toHaveText(KEY_REPORTS.no_text);
    expect(await caretInNode(page), "Enter on a comment put a caret somewhere").toBe(false);
  } finally {
    await context.close();
  }
});
