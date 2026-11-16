import { devices, expect, type Locator, type Page, test } from "@playwright/test";

import { type FakeSession, getSentMessages, setLiveSessions } from "./agents";
import {
  answerAsk,
  createAsk,
  createComment,
  createIssue,
  createProject,
  getAsk,
  patchIssue,
  resolveAsk,
} from "./api";
import { recordClipboard } from "./clipboard";
import { documentEditor, indexedBlockAsk, marginCard } from "./editor";
import { askCard, inboxPane, inboxRow } from "./inbox-pane";
import { replyRowId } from "./inbox-threads";
import { type KeyedWrite, watchWrites } from "./keyed-writes";
import { resetDatabase } from "./seed";
import { pasteFile } from "./sends";
import { sessionIds } from "./session-ids";
import { asUser } from "./users";

/**
 * INFRA-341: the ask card's answer box and the reply box under an answered ask are one answer
 * area, with the Conversation tab's own field and `@` picker. A picked session is stored as a
 * `session:` mention on the comment Ask back and Reply post, and that session gets it; an answer
 * carries no mentions, so Answer refuses one rather than dropping it. Every send that cannot be
 * made says why in place (`RefusableButton`), and one ask's header reads the same wherever the
 * ask is drawn.
 */

/** The one live session the `@` picker offers. */
const planner: FakeSession = {
  capabilities: ["steer"],
  dir: "/workspaces/planner",
  last_seen: 1_700_000_000_000,
  machine_id: "e2e",
  roles: [],
  session_id: sessionIds.planner,
  title: "Planner",
};

const PLANNER_TARGET = `session:${sessionIds.planner}`;

/** The asker of every ask these rows seed. */
const asker = { actor: { id: sessionIds.planner, kind: "session" as const }, as: "agent" as const };

const MENTION_REFUSAL =
  "An answer cannot mention a session. Remove the mention, or send it with Ask back.";
const EMPTY_ANSWER_REFUSAL = "Pick an option or write an answer.";
const EMPTY_DRAFT_REFUSAL = "Write something first.";

test.beforeEach(async () => {
  await resetDatabase();
  await setLiveSessions([planner]);
});

/** An issue with one open ask on it, asked by the live session. */
async function seedOpenAsk(question = "Which release path?") {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "Answer area" });
  const ask = await createAsk(issue.key, { question }, asker);
  return { ask, issue };
}

/** Types `text` - which must end in the `@` the picker stands over - and picks `option`. */
async function pickMention(
  page: Page,
  field: Locator,
  text: string,
  option: string
): Promise<void> {
  await field.fill(text);
  await page
    .getByRole("listbox", { name: "Mention suggestions" })
    .getByRole("option", { name: option })
    .click();
  await expect(field).toHaveValue(`${text}${option}`);
}

/** The comments the page posts on `issueKey` from here on, as the bodies it sent. */
function postedComments(page: Page, issueKey: string): KeyedWrite<unknown>[] {
  return watchWrites(page, "POST", `/api/v1/issues/${issueKey}/comments`);
}

/** One ask's whole turn in the Conversation: an answered ask's card is the record alone, and the
 *  thread under it - its replies and its Write a reply control - is the turn's, beside it. */
function conversationTurn(page: Page, askId: string): Locator {
  return page
    .getByRole("list", { name: "Conversation turns" })
    .locator(":scope > li")
    .filter({ has: page.getByTestId(`ask-${askId}`) });
}

/** Every `data-ask-*` field the headers inside `place` hold, with the place's own anchor quote
 *  and the state label as the reader sees it: the rendered text (`innerText`) of each label the
 *  browser draws, so a label the place leaves out, or hides, is missing here whatever its
 *  attributes say. A decision block draws two headers - the block's own urgency and state, and
 *  the hosted card's, which adds the provenance, the reference and the label - so the fields are
 *  collected across them. A field two headers of one place disagree about is reported as both
 *  values, which fails the row with the disagreement in it. The labels are not deduplicated: a
 *  place draws one, so two read `Open + Open` and fail a row that expects `Open`. */
async function headerFacts(place: Locator): Promise<Record<string, string | undefined>> {
  return place.evaluate((node) => {
    const names = ["urgency", "asker", "at", "copy", "state"];
    const held: Record<string, string | undefined> = {};
    const headers = [...node.querySelectorAll<HTMLElement>("[data-ask-header]")];
    const joined = (values: readonly string[]) => {
      const distinct = [...new Set(values)];
      return distinct.length === 0 ? undefined : distinct.join(" ≠ ");
    };
    for (const name of names) {
      held[name] = joined(
        headers
          .map((header) => header.dataset[`ask${name[0]?.toUpperCase()}${name.slice(1)}`])
          .filter((value) => value !== undefined)
      );
    }
    const labels = headers
      .flatMap((header) => [...header.querySelectorAll<HTMLElement>("[data-ask-state-label]")])
      .filter((label) => label.checkVisibility({ opacityProperty: true, visibilityProperty: true }))
      .map((label) => label.innerText.trim());
    held.label = labels.length === 0 ? undefined : labels.join(" + ");
    const quote = node.querySelector("blockquote");
    held.anchor = quote?.textContent ?? undefined;
    return held;
  });
}

test("one ask header says the same of an open, an answered and a resolved ask wherever it is drawn @phone", async ({
  browser,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({
    project: "CORE",
    spec: "## Specification\n\nThe mirror replays the ledger on a cold start.\n",
    title: "Header everywhere",
  });
  const quote = "replays the ledger";
  const open = await createAsk(
    issue.key,
    { anchor: { artifact: "spec", quote }, question: "Does a cold start replay the ledger?" },
    asker
  );
  const answeredAsk = await createAsk(
    issue.key,
    { anchor: { artifact: "spec", quote }, question: "Which mirror keeps the budget?" },
    asker
  );
  await answerAsk(answeredAsk.id, {
    expected_edited_at: null,
    selected: [],
    text: "The left one.",
  });
  const resolved = await createAsk(
    issue.key,
    { anchor: { artifact: "spec", quote }, question: "Should the mirror retry?" },
    asker
  );
  await resolveAsk(resolved.id, { kind: "resolved", reason: "Decided elsewhere." });

  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    // The Inbox, which draws the open ask's card: in the reading column at desktop width, in the
    // row itself on a phone.
    await page.goto("/");
    const inboxFacts = await headerFacts(await askCard(page, open.id));
    expect(inboxFacts, "the Inbox card's header").toMatchObject({
      anchor: quote,
      asker: "Planner",
      at: open.created_at,
      copy: `dispatch://${issue.key}/ask/${open.id}`,
      label: "Open",
      state: "open",
      urgency: "med",
    });

    // The Inbox drawer, over whatever page the reader is on, draws the same card.
    await page.goto(`/issues/${issue.key}`);
    await page
      .getByRole("button", { name: /^(Open inbox|Peek at the inbox)/ })
      .first()
      .click();
    const drawer = page.getByRole("dialog", { name: "Inbox" });
    await expect(drawer.getByTestId(`ask-${open.id}`)).toBeVisible();
    expect(
      await headerFacts(drawer.getByTestId(`ask-${open.id}`)),
      "the Inbox drawer card's header"
    ).toMatchObject(inboxFacts);
    await page.keyboard.press("Escape");

    // The Conversation tab, where all three are drawn: the open card, the answered record and the
    // resolved record.
    await page.goto(`/issues/${issue.key}/conversation`);
    const conversation = page.locator("#issue-conversation-panel");
    expect(
      await headerFacts(conversation.getByTestId(`ask-${open.id}`)),
      "the Conversation tab's open card's header"
    ).toMatchObject(inboxFacts);
    const answeredFacts = await headerFacts(conversation.getByTestId(`ask-${answeredAsk.id}`));
    expect(answeredFacts, "the Conversation tab's answered record's header").toMatchObject({
      anchor: quote,
      asker: "Planner",
      at: answeredAsk.created_at,
      copy: `dispatch://${issue.key}/ask/${answeredAsk.id}`,
      label: "Answered",
      state: "answered",
      urgency: "med",
    });
    const resolvedFacts = await headerFacts(conversation.getByTestId(`ask-${resolved.id}`));
    expect(resolvedFacts, "the Conversation tab's resolved record's header").toMatchObject({
      anchor: quote,
      asker: "Planner",
      at: resolved.created_at,
      copy: `dispatch://${issue.key}/ask/${resolved.id}`,
      label: "Resolved",
      state: "resolved",
      urgency: "med",
    });
  } finally {
    await alice.close();
  }
});

test("a decision block draws the same header fields as the ask's other places, without its anchor @phone", async ({
  browser,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({
    project: "CORE",
    spec: 'Intro.\n\n:::ask{#decide urgency="high" multiple="false" state="open"}\nWhich way?\n\n- Left\n- Right\n:::\n\n:::ask{#settle urgency="med" multiple="false" state="open"}\nShip it?\n\n- Ship\n- Hold\n:::\n',
    title: "Block header",
  });
  const blockAsk = await indexedBlockAsk(issue.key, "decide");
  const settled = await indexedBlockAsk(issue.key, "settle");
  await resolveAsk(settled.id, { kind: "resolved", reason: "Settled elsewhere." }, { as: "agent" });
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.goto("/");
    const inboxFacts = await headerFacts(await askCard(page, blockAsk.id));
    expect(inboxFacts, "the Inbox card's header").toMatchObject({
      asker: "alice",
      at: blockAsk.created_at,
      copy: `dispatch://${issue.key}/ask/${blockAsk.id}`,
      label: "Open",
      state: "open",
      urgency: "high",
    });

    // The block is its own anchor, so the block draws no quote; every other field is the same,
    // and the block's two headers hold each field once between them - its label too, for an
    // open ask and a resolved one alike. Each block is its own reported check (`expect.soft`),
    // so a run where both draw a second label says so of both.
    await page.goto(`/issues/${issue.key}`);
    const block = documentEditor(page).locator('[data-dispatch-ask-block="decide"]');
    await expect(block.locator("article[data-testid^=ask-]")).toBeVisible();
    expect.soft(await headerFacts(block), "the open decision block's headers").toEqual({
      ...inboxFacts,
      anchor: undefined,
    });
    const settledBlock = documentEditor(page).locator('[data-dispatch-ask-block="settle"]');
    await expect(settledBlock.locator("article[data-testid^=ask-]")).toBeVisible();
    expect.soft(await headerFacts(settledBlock), "the resolved decision block's headers").toEqual({
      anchor: undefined,
      asker: "alice",
      at: settled.created_at,
      copy: `dispatch://${issue.key}/ask/${settled.id}`,
      label: "Resolved",
      state: "resolved",
      urgency: "med",
    });
  } finally {
    await alice.close();
  }
});

/** A box the page drew, as the numbers a row compares. */
interface Box {
  readonly bottom: number;
  readonly left: number;
  readonly right: number;
  readonly top: number;
}

/** Whether two drawn boxes stand on one line: each starts above where the other ends. */
function sameLine(a: Box, b: Box): boolean {
  return a.top < b.bottom && a.bottom > b.top;
}

/** The boxes `root` and the parts `selectors` name inside it are drawn in, read in one pass. A
 *  part `root` draws none of fails the row, naming the part. */
async function drawnBoxes<Part extends string>(
  root: Locator,
  selectors: Readonly<Record<Part, string>>
): Promise<{ parts: Record<Part, Box>; root: Box }> {
  const drawn = await root.evaluate((node, wanted: Readonly<Record<string, string>>) => {
    const boxOf = (element: Element): Box => {
      const { bottom, left, right, top } = element.getBoundingClientRect();
      return { bottom, left, right, top };
    };
    const parts: Record<string, Box> = {};
    for (const [name, selector] of Object.entries(wanted)) {
      const found = node.querySelector(selector);
      if (found === null) throw new Error(`no ${name} is drawn: nothing matches ${selector}`);
      parts[name] = boxOf(found);
    }
    return { parts, root: boxOf(node) };
  }, selectors);
  return drawn as { parts: Record<Part, Box>; root: Box };
}

/** Where a read-only decision block's header puts its parts: the rows they fall into, whether
 *  who asked shares the label's row or takes one under it, whether "Answered by" follows the
 *  copy control or takes a row under it, and the header's own height. And what is drawn rather
 *  than the boxes around it: a line too narrow for its parts drew the time past the end of who
 *  asked's own box, over the copy control, and pushed the control past the header's edge. */
async function blockHeaderLayout(header: Locator) {
  const { parts, root: head } = await drawnBoxes(header, {
    answered: "[data-dispatch-ask-answered]",
    asked: "[data-dispatch-ask-asked]",
    copy: "[data-copy-ref] button",
    decision: "strong",
    label: "[data-ask-state-label]",
    pill: "[data-dispatch-ask-pill]",
    time: "[data-dispatch-ask-asked] time",
  });
  const { time, ...rowParts } = parts;
  const { answered, asked, copy, label } = rowParts;
  // A part opens a row when it starts below the bottom of every part on the row above it. The
  // time is drawn inside who asked, so it opens none of its own.
  let rows = 0;
  let rowBottom = Number.NEGATIVE_INFINITY;
  for (const part of Object.values(rowParts).sort((a, b) => a.top - b.top)) {
    if (part.top >= rowBottom - 1) {
      rows += 1;
      rowBottom = part.bottom;
    } else {
      rowBottom = Math.max(rowBottom, part.bottom);
    }
  }
  return {
    answeredAfterCopy: sameLine(answered, label)
      ? answered.left >= copy.right
      : answered.top >= Math.max(copy.bottom, label.bottom) - 1,
    answeredUnderAsked: answered.top >= asked.bottom - 1,
    askedOnLabelRow: sameLine(asked, label),
    copyAtRightEnd: Math.abs(head.right - copy.right) <= 1,
    copyInsideHeader: copy.left >= head.left - 1 && copy.right <= head.right + 1,
    copyOnLabelRow: sameLine(copy, label),
    height: Math.round(head.bottom - head.top),
    rows,
    timeClearOfCopy: !sameLine(time, copy) || time.right <= copy.left + 1,
    timeInsideHeader: time.right <= head.right + 1,
  };
}

/** The long-titled session whose name is what pushed a block header's copy control off its
 *  first row. */
const BLOCK_ASKER_TITLE = "Release planner for the ledger mirror";

/** An issue whose spec holds one decision block, asked by the long-titled session and answered,
 *  with the time the server says the answer was given. Each row closes the issue once it has
 *  seeded what it needs on it: a closed issue's answered block draws its own header, since no
 *  card under it carries the provenance. */
async function seedAnsweredBlock() {
  await setLiveSessions([{ ...planner, title: BLOCK_ASKER_TITLE }]);
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue(
    {
      project: "CORE",
      spec: 'Intro.\n\n:::ask{#decide urgency="blocking" multiple="false" state="open"}\nWhich way?\n\n- Left\n- Right\n:::\n',
      title: "Closed block header",
    },
    asker
  );
  const blockAsk = await indexedBlockAsk(issue.key, "decide");
  const answered = await answerAsk(blockAsk.id, {
    expected_edited_at: null,
    selected: ["Left"],
    text: "Left.",
  });
  if (answered.answer === null) throw new Error("the answered ask holds no answer");
  return { answeredAt: answered.answer.at, blockAsk, issue };
}

/** What the block header's two layouts hold wherever they are drawn: the time clear of the
 *  copy control and both inside the header, the control on the label's row. */
const BLOCK_HEADER_DRAWN = {
  copyInsideHeader: true,
  copyOnLabelRow: true,
  timeClearOfCopy: true,
  timeInsideHeader: true,
} as const;

/** From 36rem of header: one line, who asked on the label's row, "Answered by" after the copy
 *  control or under the line. */
const BLOCK_HEADER_LINE = {
  ...BLOCK_HEADER_DRAWN,
  answeredAfterCopy: true,
  askedOnLabelRow: true,
} as const;

/** Below 36rem of header: rows, the copy control at the end of the label's, who asked under it
 *  and "Answered by" under that. */
const BLOCK_HEADER_ROWS = {
  ...BLOCK_HEADER_DRAWN,
  answeredUnderAsked: true,
  askedOnLabelRow: false,
  copyAtRightEnd: true,
} as const;

/**
 * The read-only block's header at each width this row reads it at, for this seed. The heights
 * and rows at 1280, 1024 and 768 px are `main`'s at c69be928, before the header drew a state
 * label: this seed, its page clock pinned as this row pins it, counted as this row counts rows,
 * on a build of that tree served by its own harness. There it was 80 px in three rows in the
 * 491 px column 1280 px leaves beside the margin (the urgency and `Decision`; who asked, when
 * and the copy control; who answered), 44 px in one row in the 939 px column at 1024, and 68
 * px in two in the 683 px column at 768 - below 1280 px every button is a 44 px box
 * (`web/src/styles.css`), the copy control with them. A label that made the header taller
 * there, or pushed "Answered by" to a row of its own at 1024, fails it. A phone's 305 px header
 * is held to its rows alone: `main` wrapped who asked onto two lines there, 112 px to these
 * rows' 92.
 *
 * The time is the one part of this seed whose width moves, so the row pins it: at 1024 the one
 * row leaves 70 px to spare on `main` and 7 px here, and a time as wide as "5 minutes ago"
 * puts "Answered by" on a row of its own on both. The family the text is drawn in is pinned
 * too: the browser image the suite runs in answers `sans-serif` as DejaVu Sans, as the runner
 * did (`.github/docker/browser.Dockerfile`), and so do the machines these figures were read on.
 */
const BLOCK_HEADER_AT: Readonly<Record<number, Record<string, boolean | number>>> = {
  390: { ...BLOCK_HEADER_ROWS, rows: 3 },
  768: { ...BLOCK_HEADER_LINE, height: 68, rows: 2 },
  1024: { ...BLOCK_HEADER_LINE, height: 44, rows: 1 },
  1280: { ...BLOCK_HEADER_ROWS, height: 80, rows: 3 },
};

test("a read-only decision block's header is one wrapping row where its line fits and rows of their own where it does not, as tall as main's from 768 to 1280 px @phone", async ({
  browser,
}) => {
  const { answeredAt, issue } = await seedAnsweredBlock();
  await patchIssue(issue.key, { status: "done" });
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    // The page's `Date` is held at the moment the server says the block was answered, its
    // timers left running, so both of the header's times read "just now" however long the row
    // takes, whatever the page's clock says against the server's: the width of each time is
    // part of what the row measures (`BLOCK_HEADER_AT`).
    await page.clock.setFixedTime(answeredAt);
    await page.goto(`/issues/${issue.key}`);
    const header = documentEditor(page).locator(
      '[data-dispatch-ask-block="decide"] header[data-ask-header]'
    );
    await expect(header.locator("[data-dispatch-ask-answered]")).toBeVisible();
    await expect(header.locator("time")).toHaveText(["just now", "just now"]);
    const viewport = page.viewportSize();
    if (viewport === null) throw new Error("the page has no viewport");
    // The header picks its layout by its own width, not the viewport's. A desktop project reads
    // it from 1280 down to a phone's 390 px, a phone project at its own width.
    const widths = viewport.width < 768 ? [viewport.width] : [1280, 1024, 768, 390];
    for (const width of widths) {
      const expected = BLOCK_HEADER_AT[width];
      if (expected === undefined) throw new Error(`no layout is written down for ${width} px`);
      await page.setViewportSize({ height: viewport.height, width });
      await expect
        .poll(() => blockHeaderLayout(header), {
          message: `the block header's rows at ${width} px`,
        })
        .toMatchObject(expected);
    }
    // A name the row cuts is still there whole, in the tooltip of the part that truncates.
    await expect(header.locator("[data-dispatch-ask-asked] > [title]")).toHaveAttribute(
      "title",
      new RegExp(`^${BLOCK_ASKER_TITLE} · `)
    );
  } finally {
    await alice.close();
  }
});

test("a closed issue's answered block in the narrowest reading pane draws its time clear of its copy control, and the control inside its header", async ({
  browser,
}) => {
  const { blockAsk, issue } = await seedAnsweredBlock();
  // The asker's reply under the reader's answer keeps the ask's thread in the reader's Inbox as
  // a Reply row after the close, and the pane opens a decision block's thread in its document.
  await createComment(issue.key, { ask_id: blockAsk.id, body: "Left it is." }, asker);
  await patchIssue(issue.key, { status: "done" });
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    // The pane shows from 1280 px (`READING_PANE_QUERY`), where its narrowest is narrowest.
    await page.setViewportSize({ height: 900, width: 1280 });
    await page.goto("/");
    const row = page.locator(`[data-inbox-row="${replyRowId(blockAsk.id)}"]`);
    await row.locator("time").first().click();
    await expect(row).toHaveAttribute("data-inbox-selected", "true");
    // The reader drags the line between the list and the pane as far right as it goes, where
    // the pane keeps 30% of the two columns (`PaneDivider`): about 266 px.
    const divider = page.getByTestId("inbox-pane-divider");
    const line = await divider.boundingBox();
    if (line === null) throw new Error("the Inbox draws no pane divider");
    const y = line.y + line.height / 2;
    await page.mouse.move(line.x + line.width / 2, y);
    await page.mouse.down();
    await page.mouse.move(1279, y, { steps: 5 });
    await page.mouse.up();
    await expect(divider).toHaveAttribute("aria-valuenow", "30");
    const header = inboxPane(page).locator(
      '[data-dispatch-ask-block="decide"] header[data-ask-header]'
    );
    await expect(header.locator("[data-dispatch-ask-answered]")).toBeVisible();
    await expect
      .poll(() => blockHeaderLayout(header), {
        message: "the block header in the narrowest reading pane",
      })
      .toMatchObject(BLOCK_HEADER_ROWS);
  } finally {
    await alice.close();
  }
});

/** The long-titled session whose ask the card rows hand back to it: its name is the turn an
 *  agent holds, the one part of the card's line that gives up width. */
const ledger: FakeSession = {
  ...planner,
  dir: "/workspaces/ledger",
  session_id: sessionIds.tester,
  title: "Release planner for the ledger mirror",
};

/** Two anchored asks on one issue's spec: the planner's, waiting on the reader, and the
 *  long-titled session's, whose turn the reader's question handed back to it. */
async function seedTurns() {
  await setLiveSessions([planner, ledger]);
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({
    project: "CORE",
    spec: "## Specification\n\nThe mirror replays the ledger on a cold start.\n",
    title: "Copy control",
  });
  const yours = await createAsk(
    issue.key,
    {
      anchor: { artifact: "spec", quote: "replays the ledger" },
      question: "Does a cold start replay the ledger?",
    },
    asker
  );
  const theirs = await createAsk(
    issue.key,
    {
      anchor: { artifact: "spec", quote: "cold start" },
      question: "Which mirror keeps the budget?",
    },
    { actor: { id: ledger.session_id, kind: "session" }, as: "agent" }
  );
  await createComment(issue.key, { ask_id: theirs.id, body: "Which mirror do you mean?" });
  await expect
    .poll(() => getAsk(theirs.id), { intervals: [50, 50, 100, 200, 500] })
    .toMatchObject({ ask: { waiting_on: "agent" } });
  return {
    issue,
    turns: [
      { ask: yours, asker: planner, turn: "Waiting on you" },
      { ask: theirs, asker: ledger, turn: `Waiting on ${ledger.title}` },
    ],
  };
}

/** An ask card's own line: the width of its `@container` row, the one condition that picks its
 *  layout (`CardAskHeader`: one line from 27.5rem, two lines beside the copy control from 19.5rem,
 *  the phone's lines below that), the viewport's width, which no longer picks it and which a row
 *  reads only to say which reader it stands for, whether the copy control shares the label's
 *  line, and whether who asked and when opens a line of its own under that one. The row and who
 *  asked are read by their own marks, so a row that lost either says which. */
async function copyLine(card: Locator) {
  await expect(card.locator("[data-ask-header] [data-copy-ref] button")).toBeVisible();
  const viewport = card.page().viewportSize();
  if (viewport === null) throw new Error("the page has no viewport");
  const { parts } = await drawnBoxes(card.locator("[data-ask-header]").first(), {
    asked: "[data-ask-card-asked]",
    copy: "[data-copy-ref] button",
    label: "[data-ask-state-label]",
    row: "[data-ask-card-row]",
  });
  const { asked, copy, label, row } = parts;
  return {
    askerOwnLine:
      asked.top >= Math.max(label.bottom, copy.bottom) - 1 && Math.abs(asked.left - row.left) <= 1,
    copyOnLabelLine: sameLine(copy, label),
    row: row.right - row.left,
    viewport: viewport.width,
  };
}

test("the Inbox card's copy control ends the label's one line, whoever's turn it is", async ({
  browser,
}) => {
  const { turns } = await seedTurns();
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.goto("/");
    for (const { ask, turn } of turns) {
      const card = await askCard(page, ask.id);
      await expect(card.getByTestId(`turn-${ask.id}`)).toContainText(turn);
      const line = await copyLine(card);
      // The card's widest layout: a row of 27.5rem or more, where label, turn, asker and copy
      // control are one line.
      expect(line.row, `${turn}: the Inbox card's row is 27.5rem or wider`).toBeGreaterThanOrEqual(
        440
      );
      expect(line.copyOnLabelLine, `${turn}: the copy control is on the label's line`).toBe(true);
    }
  } finally {
    await alice.close();
  }
});

test("the margin card's copy control stands beside its two lines, on the label's, whoever's turn it is", async ({
  browser,
}) => {
  const { issue, turns } = await seedTurns();
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.goto(`/issues/${issue.key}`);
    await page.getByRole("tab", { name: "Spec" }).click();
    for (const { ask, turn } of turns) {
      const card = marginCard(page, ask.id);
      await expect(card.getByTestId(`turn-${ask.id}`)).toContainText(turn);
      const line = await copyLine(card);
      // The margin: a row from 19.5rem to under 27.5rem, which breaks the label and turn from the
      // asker and stands the copy control beside both lines. The viewport check no longer picks
      // that layout; it says this row reads the margin as a desktop reader does, at `md` or wider.
      expect(line.viewport, `${turn}: the viewport is md or wider`).toBeGreaterThanOrEqual(768);
      expect(line.row, `${turn}: the margin card's row is 19.5rem or wider`).toBeGreaterThanOrEqual(
        312
      );
      expect(line.row, `${turn}: the margin card's row is under 27.5rem`).toBeLessThan(440);
      expect(line.copyOnLabelLine, `${turn}: the copy control is on the label's line`).toBe(true);
    }
  } finally {
    await alice.close();
  }
});

test("on a phone the Inbox card's copy control ends the label's line and the asker has the next line whole, whoever's turn it is @phone", async ({
  browser,
}) => {
  const { turns } = await seedTurns();
  const alice = await asUser(browser, "alice", devices["iPhone 13"]);
  try {
    const page = await alice.newPage();
    await page.goto("/");
    for (const { ask, asker: author, turn } of turns) {
      const card = await askCard(page, ask.id);
      await expect(card.getByTestId(`turn-${ask.id}`)).toContainText(turn);
      const line = await copyLine(card);
      // A phone: a row under 19.5rem, where the copy control ends the label's line and the asker
      // has the next line to itself. The viewport check no longer picks that layout; it says
      // this row reads the card as a phone does, under `md`, where the copy control and the live
      // link are 44 px touch boxes.
      expect(line.viewport, `${turn}: the viewport is under md`).toBeLessThan(768);
      expect(line.row, `${turn}: the phone card's row is under 19.5rem`).toBeLessThan(312);
      expect(line.copyOnLabelLine, `${turn}: the copy control is on the label's line`).toBe(true);
      expect(line.askerOwnLine, `${turn}: the asker opens the next line`).toBe(true);
      const name = card.locator(
        `[data-ask-header] [title="${author.title} · ${author.session_id}"]`
      );
      await expect(name).toHaveText(author.title);
      // A name that fits that line is drawn whole. The agent's own title does not fit it at
      // all - 223 px of name beside the live link and the time, which leave it 160 - so it
      // truncates there whatever the copy control does, and its tooltip holds it whole.
      if (author === ledger) continue;
      const fits = await name.evaluate((element) => ({
        client: element.clientWidth,
        scroll: element.scrollWidth,
      }));
      expect(fits.scroll, `${turn}: the asker's name is not cut short`).toBeLessThanOrEqual(
        fits.client
      );
    }
  } finally {
    await alice.close();
  }
});

test("a session picked in an Inbox ask's answer area goes out on Ask back, and no answer carries it @phone", async ({
  browser,
}) => {
  const { ask, issue } = await seedOpenAsk();
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    const comments = postedComments(page, issue.key);
    const answers = watchWrites(page, "POST", `/api/v1/asks/${ask.id}/answer`);
    await page.goto("/");
    const card = await askCard(page, ask.id);
    const field = card.getByLabel("Your answer");
    await pickMention(page, field, "Over to @", "Planner");
    await expect(card.getByRole("list", { name: "Mention targets" })).toContainText(PLANNER_TARGET);

    // An answer carries no mentions, so Answer refuses this draft: in place, with its reason on
    // screen and as its accessible description, and it sends nothing by click or by key.
    const answer = card.getByRole("button", { exact: true, name: "Answer" });
    await answer.focus();
    await expect(answer).toBeDisabled();
    await expect(answer).toHaveAttribute("aria-disabled", "true");
    await expect(answer).toHaveAttribute("title", MENTION_REFUSAL);
    await expect(answer).toHaveAccessibleDescription(MENTION_REFUSAL);
    await expect(card.getByText(MENTION_REFUSAL, { exact: true })).toBeVisible();
    await answer.click({ force: true });
    await expect(answer).toBeFocused();
    await field.press("ControlOrMeta+Enter");
    // Nothing being sent cannot be awaited: half a second is the window the answer would have
    // reached the server in, by the click above or by this key.
    await page.waitForTimeout(500);
    expect(answers).toEqual([]);

    // Ask back posts it as a comment that names the session, and the session gets it.
    await card.getByRole("button", { exact: true, name: "Ask back" }).click();
    await expect
      .poll(() => comments.map((write) => write.input))
      .toEqual([
        { ask_id: ask.id, body: "Over to @Planner", mentions: [{ target: PLANNER_TARGET }] },
      ]);
    await expect
      .poll(() => getSentMessages())
      .toMatchObject([{ target_session: planner.session_id }]);
    await expect(field).toHaveValue("");
  } finally {
    await alice.close();
  }
});

test("a decision block's answer area picks a session the same way and asks back with it @phone", async ({
  browser,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({
    project: "CORE",
    spec: 'Intro.\n\n:::ask{#decide urgency="med" multiple="false" state="open"}\nWhich way?\n\n- Left\n- Right\n:::\n',
    title: "Block answer area",
  });
  const blockAsk = await indexedBlockAsk(issue.key, "decide");
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    const comments = postedComments(page, issue.key);
    await page.goto(`/issues/${issue.key}`);
    const block = documentEditor(page).locator('[data-dispatch-ask-block="decide"]');
    const card = block.locator("article[data-testid^=ask-]");
    await expect(card).toBeVisible();
    await card.getByRole("button", { name: "Add a note or answer in your own words" }).click();
    const field = card.getByLabel("Your answer");
    await pickMention(page, field, "Ask @", "Planner");

    const answer = card.getByRole("button", { exact: true, name: "Answer" });
    await expect(answer).toBeDisabled();
    await expect(answer).toHaveAccessibleDescription(MENTION_REFUSAL);
    await card.getByRole("button", { exact: true, name: "Ask back" }).click();
    await expect
      .poll(() => comments.map((write) => write.input))
      .toEqual([
        { ask_id: blockAsk.id, body: "Ask @Planner", mentions: [{ target: PLANNER_TARGET }] },
      ]);
    await expect
      .poll(() => getSentMessages())
      .toMatchObject([{ target_session: planner.session_id }]);
  } finally {
    await alice.close();
  }
});

test("the Conversation tab's copy of an ask, and the reply box under an answered ask, send a picked session @phone", async ({
  browser,
}) => {
  const { ask, issue } = await seedOpenAsk();
  const decided = await createAsk(issue.key, { question: "Which mirror?" }, asker);
  await answerAsk(decided.id, { expected_edited_at: null, selected: [], text: "The left one." });
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    const comments = postedComments(page, issue.key);
    await page.goto(`/issues/${issue.key}/conversation`);
    const conversation = page.locator("#issue-conversation-panel");

    // The open ask's own card in the timeline: the compact variant, whose answer area is behind
    // its note disclosure.
    const openCard = conversation.getByTestId(`ask-${ask.id}`);
    await openCard.getByRole("button", { name: "Add a note or answer in your own words" }).click();
    await pickMention(page, openCard.getByLabel("Your answer"), "Ask @", "Planner");
    await openCard.getByRole("button", { exact: true, name: "Ask back" }).click();
    await expect
      .poll(() => comments.map((write) => write.input))
      .toEqual([{ ask_id: ask.id, body: "Ask @Planner", mentions: [{ target: PLANNER_TARGET }] }]);

    // The reply box under the answered ask, behind its own Write a reply control. An answered
    // ask's card is its record, and the thread with that control is the turn's, beside it.
    const answeredTurn = conversationTurn(page, decided.id);
    await answeredTurn.getByRole("button", { name: "Write a reply" }).click();
    await pickMention(page, answeredTurn.getByLabel("Reply"), "Over to @", "Planner");
    await answeredTurn.getByRole("button", { exact: true, name: "Reply" }).click();
    await expect.poll(() => comments.map((write) => write.input)).toHaveLength(2);
    expect(comments[1]?.input).toEqual({
      ask_id: decided.id,
      body: "Over to @Planner",
      mentions: [{ target: PLANNER_TARGET }],
    });
    await expect
      .poll(() => getSentMessages())
      .toMatchObject([
        { target_session: planner.session_id },
        { target_session: planner.session_id },
      ]);
  } finally {
    await alice.close();
  }
});

test("an ask draft keeps its picked mention when its box closes and opens again @phone", async ({
  browser,
}) => {
  const { issue } = await seedOpenAsk();
  const decided = await createAsk(issue.key, { question: "Which mirror?" }, asker);
  await answerAsk(decided.id, { expected_edited_at: null, selected: [], text: "The left one." });
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    const comments = postedComments(page, issue.key);
    await page.goto(`/issues/${issue.key}/conversation`);
    const turn = conversationTurn(page, decided.id);
    const open = turn.getByRole("button", { name: "Write a reply" });
    await open.click();
    await pickMention(page, turn.getByLabel("Reply"), "Over to @", "Planner");

    // The box goes with its disclosure and comes back with the draft the ask kept, mentions and
    // all, so the reply still reaches the session that was picked before it closed.
    await open.click();
    await expect(turn.getByLabel("Reply")).toHaveCount(0);
    await open.click();
    await expect(turn.getByLabel("Reply")).toHaveValue("Over to @Planner");
    await expect(turn.getByRole("list", { name: "Mention targets" })).toContainText(PLANNER_TARGET);
    await turn.getByRole("button", { exact: true, name: "Reply" }).click();
    await expect
      .poll(() => comments.map((write) => write.input))
      .toEqual([
        { ask_id: decided.id, body: "Over to @Planner", mentions: [{ target: PLANNER_TARGET }] },
      ]);
  } finally {
    await alice.close();
  }
});

test("Answer, Ask back and Reply refuse an empty ask box in place, sending nothing @phone", async ({
  browser,
}) => {
  const { ask, issue } = await seedOpenAsk();
  const decided = await createAsk(issue.key, { question: "Which mirror?" }, asker);
  await answerAsk(decided.id, { expected_edited_at: null, selected: [], text: "The left one." });
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    const comments = postedComments(page, issue.key);
    const answers = watchWrites(page, "POST", `/api/v1/asks/${ask.id}/answer`);
    await page.goto("/");
    const card = await askCard(page, ask.id);
    const answer = card.getByRole("button", { exact: true, name: "Answer" });
    const askBack = card.getByRole("button", { exact: true, name: "Ask back" });
    for (const [button, reason] of [
      [answer, EMPTY_ANSWER_REFUSAL],
      [askBack, EMPTY_DRAFT_REFUSAL],
    ] as const) {
      // A refused button keeps its place in the tab order, so the reader can reach and hold it;
      // pressing it anyway does nothing. The press is read after it as well as the reach before
      // it: a forced press lands on whatever the page leaves at that point, and the Reply half
      // of this row found its own press reaching another control's refused Send, so each press
      // here says where it landed - a refused button that keeps the focus is the button pressed.
      await button.focus();
      await expect(button).toBeFocused();
      await expect(button).toBeDisabled();
      await expect(button).toHaveAttribute("title", reason);
      await expect(button).toHaveAccessibleDescription(reason);
      await expect(card.getByText(reason, { exact: true })).toBeVisible();
      await button.scrollIntoViewIfNeeded();
      await button.click({ force: true });
      await expect(button).toBeFocused();
    }
    // Nothing being sent cannot be awaited: half a second is the window either press would have
    // reached the server in, and every write this row makes lands well inside it.
    await page.waitForTimeout(500);
    expect(answers).toEqual([]);
    expect(comments).toEqual([]);

    await page.goto(`/issues/${issue.key}/conversation`);
    const turn = conversationTurn(page, decided.id);
    await turn.getByRole("button", { name: "Write a reply" }).click();
    const reply = turn.getByRole("button", { exact: true, name: "Reply" });
    await reply.focus();
    await expect(reply).toBeFocused();
    await expect(reply).toBeDisabled();
    await expect(reply).toHaveAttribute("title", EMPTY_DRAFT_REFUSAL);
    await expect(reply).toHaveAccessibleDescription(EMPTY_DRAFT_REFUSAL);
    await expect(turn.getByText(EMPTY_DRAFT_REFUSAL, { exact: true })).toBeVisible();
    // The press is made where the button is the thing under it. On a phone the conversation's
    // own composer is pinned to the bottom of the screen, and the turn's Reply button lies
    // under it where the page leaves it: a forced press at that point reaches the composer's
    // own refused `Send` - which is where the focus then went, and why nothing below could be
    // read of Reply at all. A refused button takes no unforced press to wait for either
    // (`aria-disabled` is Playwright's "not enabled"), so the scroll is what makes the press
    // the reader's.
    await reply.evaluate((button) => {
      button.scrollIntoView({ block: "center" });
    });
    await reply.click({ force: true });
    // And the press left the focus where it was: what c-a1 asks of a refused Reply is that the
    // reader keeps it with its reason, and a focus read only before the press says nothing
    // about what the press did with it.
    await expect(reply).toBeFocused();
    await turn.getByLabel("Reply").press("ControlOrMeta+Enter");
    // Nothing being sent cannot be awaited: half a second is the window a press that was going
    // to post this reply would have posted it in.
    await page.waitForTimeout(500);
    expect(comments).toEqual([]);
  } finally {
    await alice.close();
  }
});

test("a file pasted into an ask's answer box and into its reply box adds its line, and Ctrl+Enter sends", async ({
  browser,
}) => {
  const { ask, issue } = await seedOpenAsk();
  const decided = await createAsk(issue.key, { question: "Which mirror?" }, asker);
  await answerAsk(decided.id, { expected_edited_at: null, selected: [], text: "The left one." });
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    const comments = postedComments(page, issue.key);
    const answers = watchWrites(page, "POST", `/api/v1/asks/${ask.id}/answer`);
    await page.goto("/");
    const card = await askCard(page, ask.id);
    const field = card.getByLabel("Your answer");
    await field.fill("See");
    await pasteFile(field, "notes.md", "the notes");
    // A file that is no picture joins the draft as its plain reference (`uploadedFileText`).
    const line = `dispatch://${issue.key}/artifact/notes-md`;
    await expect(field).toHaveValue(`See ${line}`);
    await field.press("ControlOrMeta+Enter");
    await expect
      .poll(() => answers.map((write) => write.input))
      .toEqual([{ expected_edited_at: null, selected: [], text: `See ${line}` }]);

    await page.goto(`/issues/${issue.key}/conversation`);
    const turn = conversationTurn(page, decided.id);
    await turn.getByRole("button", { name: "Write a reply" }).click();
    const replyField = turn.getByLabel("Reply");
    await replyField.fill("And");
    await pasteFile(replyField, "notes.md", "the notes");
    const second = `dispatch://${issue.key}/artifact/notes-md`;
    await expect(replyField).toHaveValue(`And ${second}`);
    await replyField.press("ControlOrMeta+Enter");
    await expect
      .poll(() => comments.map((write) => write.input))
      .toEqual([{ ask_id: decided.id, body: `And ${second}` }]);
  } finally {
    await alice.close();
  }
});

test("the open picker owns the arrows and Enter inside an Inbox ask card @phone", async ({
  browser,
}) => {
  const { ask } = await seedOpenAsk();
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.goto("/");
    const card = await askCard(page, ask.id);
    const field = card.getByLabel("Your answer");
    await field.fill("@");
    const option = page
      .getByRole("listbox", { name: "Mention suggestions" })
      .getByRole("option", { name: "Planner" });
    await expect(option).toBeVisible();
    // Nothing is active until a key moves it; the arrows are the picker's, not the Inbox's own
    // j/k twins, and the row the reader is on keeps the focus context either way.
    await expect(option).toHaveAttribute("aria-selected", "false");
    await page.keyboard.press("ArrowDown");
    await expect(option).toHaveAttribute("aria-selected", "true");
    await page.keyboard.press("ArrowUp");
    await expect(option).toHaveAttribute("aria-selected", "true");
    await expect(field).toBeFocused();
    // Enter picks the option rather than moving the Inbox's own focus into the answer box.
    await page.keyboard.press("Enter");
    await expect(field).toHaveValue("@Planner");
    await expect(page.getByRole("listbox", { name: "Mention suggestions" })).toHaveCount(0);
    await expect(field).toBeFocused();
    await expect(inboxRow(page, ask.id)).toBeVisible();
  } finally {
    await alice.close();
  }
});

test("an ask reply in the Inbox copies its own dispatch:// reference @phone", async ({
  browser,
}) => {
  const { ask, issue } = await seedOpenAsk();
  const posted = await createComment(
    issue.key,
    { ask_id: ask.id, body: "Which environment?" },
    asker
  );
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    const copied = await recordClipboard(page);
    await page.goto("/");
    const card = await askCard(page, ask.id);
    const reply = card.getByTestId(`thread-${ask.id}`).locator("li[data-reply]").first();
    await expect(reply).toContainText("Which environment?");
    await reply.getByRole("button", { name: /^Copy reference/ }).click();
    expect(await copied()).toEqual([`dispatch://${issue.key}/comment/${posted.id}`]);
  } finally {
    await alice.close();
  }
});
