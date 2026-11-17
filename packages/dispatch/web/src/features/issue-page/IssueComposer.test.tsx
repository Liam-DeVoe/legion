import { afterEach, beforeAll, beforeEach, expect, jest, type Mock, spyOn, test } from "bun:test";
import { dispatchWritingBox } from "@legion/contracts";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
// biome-ignore lint/style/noRestrictedImports: a paste of a file into the box, which user-event's paste cannot carry
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { commentDeliveryFields } from "../../__tests__/comment-fixture";
import { displayTree } from "../../__tests__/display-trees";
import { jump, realClock } from "../../__tests__/fake-clock";
import { graphNode } from "../../__tests__/graph-fixture";
import { keyedAttempt } from "../../__tests__/keyed-attempt";
import {
  architectureTree,
  issueCollaborators,
  plannerAgent,
  searchResponse,
} from "../../__tests__/search-fixture";
import { userOnFakeClock } from "../../__tests__/user";
import { ApiError, api } from "../../api/client";
import type { Actor, CommentWithTrees, IssueAskRow, SearchResult } from "../../api/types";
import { pageFailedText, pageMutedText } from "../../theme/classes";
import type { UploadResult } from "../artifacts/ArtifactUpload";
import { heldSends } from "../conversation/held-sends";
import { KEYED_SEND_LATE_TEXT } from "../conversation/send-request";
import { MarginProvider } from "../margin/margin-context";
import { warmMarkdownRenderer } from "../refs/markdown-engine";
import { nodeRouteFromHref, shortForm } from "../refs/routes";
import { KeymapProvider } from "../shell/KeymapProvider";
import { useKeymapScope } from "../shell/keymap";
import { SEND_DEADLINE_MS } from "../writes/keyed-write";
import { IssueComposer } from "./IssueComposer";
import { IssueTree } from "./IssueTree";
import { FOOT_HEIGHT_PROPERTY } from "./identity-block";
import type { TreeNode } from "./issue-tree";
import { NoTooltips } from "./NoTooltips";
import {
  type OutlineWriter,
  OutlineWriterProvider,
  type ReplyItem,
  useOutlineWriter,
} from "./outline-writer";
import { ANSWER_LINES, CONTROL_LABELS, type NodeType } from "./page-words";

/**
 * The new issue page's one box to write in (dispatch://INFRA-3949 R1 to R7 and R11), drawn as
 * the page draws it: under the page's writer and its words (`slots/providers.tsx`), at the foot of
 * the outline the body slot draws (`IssueTree`), or of a view's rows where a view is on. Each
 * case presses what a reader presses - a row's `r` and `a`, the box's own controls and keys - and
 * reads what the box draws and what it sends. Every read and write a case makes is answered by the
 * case (`packages/dispatch/AGENTS.md`): the reads below from `reads`, the writes by each case.
 */

const KEY = "CORE-1";

/** Who reads the page, and so who writes from the box. */
const READER = "alice";
const ALICE: Actor = { id: READER, kind: "user" };
const BOB: Actor = { id: "bob", kind: "user" };

/** When the reader answered ask-2, the answer a change of it is measured against. */
const ANSWERED_AT = "2026-11-06T00:00:00Z";

/** A statement of two paragraphs, one word of it marked, as the spec stores it; and its whole
 *  words as the server matches a quote against them (`wordsOf`). */
const STATEMENT_WORDS = "The page draws **one** box.\n\nIt sits at the foot.";
const STATEMENT_QUOTE = "The page draws one box. It sits at the foot.";

/** The record a statement's words are a block of: the issue's spec (`tree-drafts.ts`). */
const SPEC_SOURCE = { id: "artifact-spec", issue: KEY, kind: "artifact" } as const;

/** A mention of the session the registry lists as Planner, which a plain line reads by that
 *  name (`plannerAgent`). */
const PLANNER_MENTION = "[@Old](dispatch://agent/planner-session)";

/** The reads the box and the writer draw from, under the keys the outline reads them by: each
 *  case's own, and what a stub below answers when a write makes the page read one again. */
interface Reads {
  asks: IssueAskRow[];
  comments: CommentWithTrees[];
}

let reads: Reads;

/** Question `id` as the issue's asks read sends it. */
function askRecord(id: string, fields: Partial<IssueAskRow> = {}): IssueAskRow {
  return {
    anchor: null,
    answer: null,
    artifact_id: null,
    asked_of: null,
    asked_of_at: null,
    asked_of_by: null,
    author: { id: "carol", kind: "user" },
    block_id: null,
    created_at: "2026-11-05T00:00:00Z",
    edited_at: null,
    id,
    issue_key: KEY,
    kind: "question",
    last_reply: null,
    multiple: false,
    opened_event_id: 1,
    options: [{ label: "Yes" }, { label: "No" }],
    question: `Question ${id}?`,
    state: "open",
    urgency: "med",
    ...fields,
  };
}

/** ask-2 as the reads hold it once `user` has answered it at `at` with `text`. */
function answeredAsk(at: string, text: string, user = READER): IssueAskRow {
  return askRecord("ask-2", { answer: { at, selected: ["No"], text, user }, state: "answered" });
}

function commentRecord(
  id: string,
  author: Actor,
  body: string,
  fields: Partial<CommentWithTrees> = {}
): CommentWithTrees {
  const tree = displayTree(body);
  return {
    anchor: null,
    ask_id: null,
    author,
    body,
    ...(tree === undefined ? {} : { body_tree: tree }),
    created_at: "2026-11-05T00:00:00Z",
    edited_at: null,
    id,
    issue_key: KEY,
    reply_to: null,
    resolved: false,
    resolved_at: null,
    resolved_by: null,
    suggestion: null,
    turn: null,
    ...commentDeliveryFields(),
    ...fields,
  };
}

function node(partial: Partial<TreeNode> & { id: string; type: NodeType }): TreeNode {
  return {
    alsoUnder: [],
    children: [],
    chips: [],
    facts: [],
    gaps: [],
    held: 0,
    question: false,
    replaced: false,
    text: partial.id,
    transcluded: false,
    waiting: false,
    ...partial,
  };
}

/** The row of question `id` in the outline. */
function askNode(id: string, state: "answered" | "open"): TreeNode {
  return node({
    author: { id: "carol", kind: "user" },
    id,
    options: [{ label: "Yes" }, { label: "No" }],
    question: state === "open",
    reference: `dispatch://${KEY}/ask/${id}`,
    state,
    text: `Question ${id}?`,
    type: "ask",
  });
}

function commentNode(id: string, author: Actor, text: string): TreeNode {
  return node({
    author,
    id,
    reference: `dispatch://${KEY}/comment/${id}`,
    text,
    type: "comment",
  });
}

function statementNode(): TreeNode {
  const tree = displayTree(STATEMENT_WORDS);
  if (tree === undefined) throw new Error("the engine read no statement");
  return node({
    id: "spec#b-r1",
    reference: `dispatch://${KEY}/spec#b-r1`,
    source: SPEC_SOURCE,
    text: STATEMENT_WORDS,
    tree,
    type: "requirement",
  });
}

/** The outline the body slot draws: bob's comment, the reader's own, an open question, the
 *  question the reader answered, and a statement of the spec. */
function outline(): TreeNode {
  return node({
    children: [
      commentNode("comment-1", BOB, "Earlier words"),
      commentNode("comment-2", ALICE, "My own words"),
      askNode("ask-1", "open"),
      askNode("ask-2", "answered"),
      statementNode(),
    ],
    id: KEY,
    type: "issue",
  });
}

/** bob's comment as a row's Reply hands it to the writer. */
const BOBS_COMMENT: ReplyItem = {
  author: BOB,
  id: "comment-1",
  kind: "comment",
  text: "Earlier words",
};

/** Where the box stands, as the foot slot reads it off the address (`slots/foot.tsx`). */
interface Place {
  readonly closed?: boolean;
  readonly documentOpen?: boolean;
  readonly viewOn?: boolean;
}

/** The page's own `issue` scope, which every one of the tree's keys is registered in. */
function IssueScope(): ReactNode {
  useKeymapScope("issue");
  return null;
}

/** A view's rows as the stage draws them in place of the outline (`slots/stage.tsx`): one tree,
 *  its row the tab stop, with the Reply and the Answer a view's row hands the writer. */
function ViewRows(): ReactNode {
  const writer = useOutlineWriter();
  return (
    <div aria-label="Spec" role="tree">
      <div aria-selected="true" role="treeitem" tabIndex={0}>
        <button onClick={() => writer?.replyTo(BOBS_COMMENT)} type="button">
          Reply
        </button>
        <button onClick={() => writer?.answerNode(askNode("ask-1", "open"))} type="button">
          Answer
        </button>
      </div>
    </div>
  );
}

/** The body and the foot of the page: the outline, or a view's rows while a view is on, and the
 *  box after them, as `IssueOutlinePage.tsx` draws the stage and the foot slot. */
function Page({
  held,
  place,
  root,
}: {
  held: { writer?: OutlineWriter };
  place: Place;
  root: TreeNode;
}): ReactNode {
  held.writer = useOutlineWriter();
  return (
    <div data-issue-page="">
      {place.viewOn === true ? (
        <ViewRows />
      ) : (
        <IssueTree
          artifacts={new Map()}
          context={new Set()}
          emptyLine="Nothing here."
          filtering={false}
          focusReveal={false}
          holdsEscape={false}
          isClosed={place.closed === true}
          onEditing={() => {}}
          reveal={undefined}
          root={root}
          rootRow
        />
      )}
      <IssueComposer
        documentOpen={place.documentOpen === true}
        isClosed={place.closed === true}
        issueKey={KEY}
        viewOn={place.viewOn === true}
      />
    </div>
  );
}

/** The page signed in as the reader, every read the writer and the box make answered from
 *  `reads` before it mounts. `redraw` moves the box to another place on the same mount: a view
 *  turned on or off, a document opened, the issue closed. A page drawn with an earlier page's
 *  `client` is that page mounted again in the same tab, its held sends with it. */
function renderPage(
  place: Place = {},
  root: TreeNode = outline(),
  client = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false, staleTime: Number.POSITIVE_INFINITY },
    },
  })
) {
  client.setQueryData(["whoami"], { kind: "user", login: READER });
  client.setQueryData(["asks", KEY], reads.asks);
  client.setQueryData(["comments", KEY], reads.comments);
  client.setQueryData(["issue-messages", KEY], []);
  client.setQueryData(["agents"], [plannerAgent()]);
  const held: { writer?: OutlineWriter } = {};
  const draw = (at: Place) => (
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <KeymapProvider>
          <MarginProvider>
            <NoTooltips>
              <OutlineWriterProvider issueKey={KEY}>
                <IssueScope />
                <Page held={held} place={at} root={root} />
              </OutlineWriterProvider>
            </NoTooltips>
          </MarginProvider>
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  const view = render(draw(place));
  const writer = (): OutlineWriter => {
    if (held.writer === undefined) throw new Error("the page drew no writer");
    return held.writer;
  };
  /** Focuses the row of `nodeId` and presses `key` there, as the reader does. */
  const press = async (user: UserEvent, nodeId: string, key: string) => {
    const row = rowOf(nodeId);
    await act(async () => row.focus());
    await user.keyboard(key);
  };
  return { client, press, redraw: (at: Place) => view.rerender(draw(at)), view, writer };
}

/** The box's section. */
function composerBox(): HTMLElement {
  const found = document.querySelector<HTMLElement>('[data-testid="issue-composer"]');
  if (found === null) throw new Error("the page drew no box");
  return found;
}

/** The field of the line that writes on the issue, a reply and a statement's comment: drawn
 *  whether or not the line is hidden, so a case reads the draft it keeps. */
function writeField(): HTMLTextAreaElement {
  const found = composerBox().querySelector<HTMLTextAreaElement>("[data-composer-write] textarea");
  if (found === null) throw new Error("the box drew no write line");
  return found;
}

/** The answer line's form, once the box answers a question. */
function answerForm(): HTMLFormElement {
  const found = composerBox().querySelector<HTMLFormElement>("form[data-answering]");
  if (found === null) throw new Error("the box drew no answer line");
  return found;
}

function answerField(): HTMLTextAreaElement {
  return within(answerForm()).getByRole<HTMLTextAreaElement>("textbox", { name: "Answer" });
}

/** The words of the target line the box draws, the × beside them left out; null with none. */
function targetWords(): string | null {
  const line = [...composerBox().querySelectorAll<HTMLElement>("[data-composer-target]")].find(
    (one) => one.closest("[hidden]") === null
  );
  return line?.firstElementChild?.textContent ?? null;
}

/** The kinds the drawn line shows as chips, in their order, each with whether it is pressed: the
 *  write line's own stay in the page, hidden, while the box answers or edits. */
function kinds(): { kind: string | undefined; name: string | null; pressed: string | null }[] {
  return [...composerBox().querySelectorAll<HTMLElement>("[data-composer-kind]")]
    .filter((chip) => chip.closest("[hidden]") === null)
    .map((chip) => ({
      kind: chip.dataset.composerKind,
      name: chip.querySelector("[data-page-chrome]")?.textContent ?? null,
      pressed: chip.getAttribute("aria-pressed"),
    }));
}

function rowOf(nodeId: string): HTMLElement {
  const row = document.querySelector<HTMLElement>(`[data-node-row][data-node-id="${nodeId}"]`);
  if (row === null) throw new Error(`the outline drew no row for ${nodeId}`);
  return row;
}

/** The node id of the outline's row that holds the focus, if one does. */
function focusedRowId(): string | undefined {
  return (
    document.activeElement?.closest<HTMLElement>("[data-node-row]")?.dataset.nodeId ?? undefined
  );
}

function footHeight(): string {
  return document.documentElement.style.getPropertyValue(FOOT_HEIGHT_PROPERTY);
}

/** Every spy a case makes, restored after it. */
const restores: (() => void)[] = [];
function stub<T extends { mockRestore: () => void }>(spy: T): T {
  restores.push(() => spy.mockRestore());
  return spy;
}

/** The issue's comments read, which a saved edit reads again; and the search the reference
 *  picker's `#` door reads. */
let commentsRead: Mock<typeof api.listComments>;
let searchRead: Mock<typeof api.search>;

beforeAll(async () => {
  await warmMarkdownRenderer();
});

beforeEach(() => {
  reads = {
    asks: [askRecord("ask-1"), answeredAsk(ANSWERED_AT, "Old reason")],
    comments: [
      commentRecord("comment-1", BOB, "Earlier words"),
      commentRecord("comment-2", ALICE, "My own words"),
    ],
  };
  // What a write makes the page read again, answered from the case's own reads.
  stub(spyOn(api, "listIssueAsks").mockImplementation(async () => reads.asks));
  commentsRead = stub(spyOn(api, "listComments").mockImplementation(async () => reads.comments));
  stub(spyOn(api, "listIssueMessages").mockResolvedValue([]));
  stub(spyOn(api, "listAgents").mockResolvedValue([plannerAgent()]));
  // The reads behind the reference picker, and behind an edit's blocks, which the box hosts.
  stub(spyOn(api, "getIssueCollaborators").mockResolvedValue(issueCollaborators()));
  stub(spyOn(api, "getArchitecture").mockResolvedValue(architectureTree()));
  stub(spyOn(api, "getKnownSessions").mockResolvedValue({ sessions: [] }));
  stub(
    spyOn(api, "commentBlocks").mockImplementation(async (id) => ({
      blocks: [],
      indexed: false,
      item: graphNode({ id, kind: "comment" }),
    }))
  );
  searchRead = stub(spyOn(api, "search").mockResolvedValue(searchResponse([])));
});

afterEach(() => {
  cleanup();
  for (const restore of restores.splice(0)) restore();
});

// R2: with no target the box writes one of two kinds, Comment on at first and Question, drawn
// as chips before Send; no chip with a reply's or a statement's target, and none on a closed
// issue. A Question pressed while the issue was open sends a comment once it is closed, rather
// than a question the issue refuses (IC:153).
test("the box draws two kinds, and none with a target or on a closed issue", async () => {
  const user = userEvent.setup();
  const createComment = stub(
    spyOn(api, "createComment").mockResolvedValue(
      commentRecord("comment-9", ALICE, "Is the box done?")
    )
  );
  // A question the closed issue would refuse, as Dispatch refuses it; the box must send none.
  const createAsk = stub(
    spyOn(api, "createAsk").mockRejectedValue(
      new ApiError(409, { code: "ISSUE_CLOSED", error: "This issue is closed." })
    )
  );
  const page = renderPage();

  expect(kinds()).toEqual([
    { kind: "comment", name: CONTROL_LABELS.composer_comment, pressed: "true" },
    { kind: "ask", name: CONTROL_LABELS.composer_question, pressed: "false" },
  ]);
  const send = within(composerBox()).getByRole("button", { name: CONTROL_LABELS.send });
  for (const chip of composerBox().querySelectorAll("[data-composer-kind]")) {
    expect(chip.compareDocumentPosition(send) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  }

  const writeOnIssue = async () => {
    await user.click(
      within(composerBox()).getByRole("button", { name: CONTROL_LABELS.write_on_issue })
    );
    await waitFor(() => expect(targetWords()).toBeNull());
  };
  // A reply's target, then a statement's: no chip while either is held.
  await page.press(user, "comment-1", "r");
  await waitFor(() => expect(targetWords()).toContain("Earlier words"));
  expect(kinds()).toEqual([]);
  await writeOnIssue();
  expect(kinds()).toHaveLength(2);
  await page.press(user, "spec#b-r1", "r");
  await waitFor(() => expect(targetWords()).toContain("The page draws one box."));
  expect(kinds()).toEqual([]);
  await writeOnIssue();
  expect(kinds()).toHaveLength(2);

  await user.click(
    within(composerBox()).getByRole("button", { name: CONTROL_LABELS.composer_question })
  );
  expect(kinds().map((chip) => chip.pressed)).toEqual(["false", "true"]);
  await user.click(writeField());
  await user.keyboard("Is the box done?");
  page.redraw({ closed: true });
  expect(kinds()).toEqual([]);
  await user.click(writeField());
  await user.keyboard("{Enter}");
  await waitFor(() =>
    expect(createComment).toHaveBeenCalledWith(KEY, { body: "Is the box done?" }, keyedAttempt)
  );
  expect(createAsk).not.toHaveBeenCalled();
});

// R1: while a view is on or a document is open the box is hidden, never unmounted, so what the
// reader typed is there when it is drawn again; it is drawn there once the reader asks to write -
// a Reply or an Edit, which put the caret in its field (IC:83) - or while a send of the line's
// own is out.
test("under a view or a document the box is hidden until the reader asks to write, and its draft is kept", async () => {
  const user = userEvent.setup();
  const page = renderPage();
  expect(composerBox()).not.toHaveAttribute("hidden");
  await user.click(writeField());
  await user.keyboard("Half a thought");

  page.redraw({ viewOn: true });
  expect(composerBox()).toHaveAttribute("hidden");
  expect(writeField().value).toBe("Half a thought");
  page.redraw({ documentOpen: true });
  expect(composerBox()).toHaveAttribute("hidden");
  expect(writeField().value).toBe("Half a thought");
  page.redraw({});
  expect(composerBox()).not.toHaveAttribute("hidden");
  expect(writeField().value).toBe("Half a thought");

  // With a document open, Edit and Reply each draw the box with the caret in their field, and
  // ending them hides it again.
  page.redraw({ documentOpen: true });
  act(() => page.writer().edit("comment-2", "My own words"));
  await waitFor(() =>
    expect(within(composerBox()).getByRole("textbox", { name: "Edit comment" })).toHaveFocus()
  );
  expect(composerBox()).not.toHaveAttribute("hidden");
  await user.keyboard("{Escape}");
  await waitFor(() => expect(composerBox()).toHaveAttribute("hidden"));
  await page.press(user, "comment-1", "r");
  await waitFor(() => expect(writeField()).toHaveFocus());
  expect(composerBox()).not.toHaveAttribute("hidden");
  // The Reply keeps the words the box held: it is the page's one draft.
  expect(writeField().value).toBe("Half a thought");
  await user.keyboard("{Escape}");
  await waitFor(() => expect(composerBox()).toHaveAttribute("hidden"));

  // A send made at rest is still out when a view opens: the box stays drawn over it until
  // Dispatch answers, and is hidden once it has.
  const out = Promise.withResolvers<CommentWithTrees>();
  stub(spyOn(api, "createComment").mockReturnValue(out.promise));
  page.redraw({});
  await user.click(writeField());
  await user.keyboard("{Control>}a{/Control}Sent at rest{Enter}");
  await waitFor(() => expect(api.createComment).toHaveBeenCalledTimes(1));
  page.redraw({ viewOn: true });
  expect(composerBox()).not.toHaveAttribute("hidden");
  await act(async () => out.resolve(commentRecord("comment-9", ALICE, "Sent at rest")));
  await waitFor(() => expect(composerBox()).toHaveAttribute("hidden"));
});

// The reviewer's finding 4593511272: the box is the page's one draft at every width, so a Reply
// keeps what the reader has typed in it: here a comment half-typed at rest, under the target the
// Reply names.
test("a Reply keeps a comment half-typed in the box at rest", async () => {
  const user = userEvent.setup();
  const page = renderPage();
  await user.click(writeField());
  await user.keyboard("Half a thought");
  await page.press(user, "comment-1", "r");
  await waitFor(() => expect(targetWords()).toContain("Earlier words"));
  await waitFor(() => expect(writeField()).toHaveFocus());
  expect(writeField().value).toBe("Half a thought");
});

// The same finding: a reply typed to one row is still the box's draft when the reader turns the
// box to another row with its Reply, or with `r` after a statement, each named above the words.
test("a Reply on another row keeps the reply typed in the box", async () => {
  const user = userEvent.setup();
  const page = renderPage();
  await page.press(user, "comment-1", "r");
  await waitFor(() => expect(targetWords()).toContain("Earlier words"));
  await waitFor(() => expect(writeField()).toHaveFocus());
  await user.keyboard("My reply to bob");

  // The Reply of the question the reader answered, pressed.
  await act(async () => rowOf("ask-2").focus());
  const reply = rowOf("ask-2").querySelector<HTMLElement>('[data-row-act="reply"]');
  if (reply === null) throw new Error("the question's row drew no Reply");
  await user.click(reply);
  await waitFor(() => expect(targetWords()).toContain("Question ask-2?"));
  await waitFor(() => expect(writeField()).toHaveFocus());
  expect(page.writer().reply?.id).toBe("ask-2");
  expect(writeField().value).toBe("My reply to bob");

  // `r` on a statement, and back to bob's comment.
  await page.press(user, "spec#b-r1", "r");
  await waitFor(() => expect(targetWords()).toContain("The page draws one box."));
  expect(writeField().value).toBe("My reply to bob");
  await page.press(user, "comment-1", "r");
  await waitFor(() => expect(targetWords()).toContain("Earlier words"));
  expect(writeField().value).toBe("My reply to bob");
});

// R5: an answer sends the options picked and the reader's words, and a change of the reader's own
// answer carries the time of the answer it replaces (`expected_answer_at`).
test("an answer sends the options and the words, and a change carries expected_answer_at", async () => {
  const user = userEvent.setup();
  const answerAsk = stub(
    spyOn(api, "answerAsk").mockImplementation(async (id, input) => {
      const answer = {
        at: "2026-11-07T00:00:00Z",
        selected: input.selected ?? null,
        text: input.text ?? "",
        user: READER,
      };
      return askRecord(id, { answer, state: "answered" });
    })
  );
  const page = renderPage();

  await page.press(user, "ask-1", "a");
  await waitFor(() => expect(answerField()).toHaveFocus());
  expect(targetWords()).toContain("Question ask-1?");
  await user.click(within(answerForm()).getByRole("button", { name: "Yes" }));
  expect(within(answerForm()).getByRole("button", { name: "Yes" })).toHaveAttribute(
    "aria-pressed",
    "true"
  );
  await user.click(answerField());
  await user.keyboard("Because it ships{Enter}");
  await waitFor(() =>
    expect(answerAsk).toHaveBeenCalledWith(
      "ask-1",
      { expected_edited_at: null, selected: ["Yes"], text: "Because it ships" },
      keyedAttempt
    )
  );
  expect(answerAsk.mock.calls[0]?.[1]).not.toHaveProperty("expected_answer_at");
  await waitFor(() => expect(page.writer().answer).toBeUndefined());

  // The reader's own answer to ask-2, changed: the line opens on its words and its pick.
  await act(async () => page.writer().changeAnswer(askNode("ask-2", "answered")));
  await waitFor(() => expect(answerField().value).toBe("Old reason"));
  expect(within(answerForm()).getByRole("button", { name: "No" })).toHaveAttribute(
    "aria-pressed",
    "true"
  );
  await user.click(answerField());
  await user.keyboard("{Control>}a{/Control}New reason{Enter}");
  await waitFor(() =>
    expect(answerAsk).toHaveBeenLastCalledWith(
      "ask-2",
      {
        expected_answer_at: ANSWERED_AT,
        expected_edited_at: null,
        selected: ["No"],
        text: "New reason",
      },
      keyedAttempt
    )
  );
});

// R5: the writer holding the reader's own comment (part M's Edit), the box holds its words, and
// Save writes the words the reader left there; the edit then ends on the comment's row.
test("an edit saves the comment's words", async () => {
  const user = userEvent.setup();
  const editComment = stub(
    spyOn(api, "editComment").mockImplementation(async (id, input) =>
      commentRecord(id, ALICE, input.body ?? "")
    )
  );
  const page = renderPage();

  await act(async () => page.writer().edit("comment-2", "My own words"));
  const field = within(composerBox()).getByRole<HTMLTextAreaElement>("textbox", {
    name: "Edit comment",
  });
  await waitFor(() => expect(field).toHaveFocus());
  expect(field.value).toBe("My own words");
  expect(targetWords()).toBe(`${CONTROL_LABELS.editing} My own words`);
  expect(kinds()).toEqual([]);
  expect(within(composerBox()).getByRole("button", { name: CONTROL_LABELS.save })).toBeTruthy();

  const readsBefore = commentsRead.mock.calls.length;
  await user.keyboard("{Control>}a{/Control}My better words{Enter}");
  await waitFor(() =>
    expect(editComment).toHaveBeenCalledWith("comment-2", { body: "My better words" }, keyedAttempt)
  );
  await waitFor(() => expect(page.writer().editing).toBeUndefined());
  // The outline draws the comment again from the read that answers after the save.
  expect(commentsRead.mock.calls.length).toBeGreaterThan(readsBefore);
  await waitFor(() => expect(focusedRowId()).toBe("comment-2"));
});

// R7: with the look, the hint under the field and the list of mention targets are off screen and
// still read out - the hint as the field's description, the list by its name - and a row of the
// reference picker carries no `title`, the page drawing no tooltips (`NoTooltips`).
test("the look keeps the hint and the targets off screen, and a picker row has no title", async () => {
  const user = userEvent.setup();
  const hit: SearchResult = {
    href: "/issues/CORE-2/comments/comment-7",
    id: "comment-7",
    kind: "comment",
    owner: { key: "CORE-2", kind: "issue", status: "open", title: "Relay drops a reply" },
    rank: 0.1,
    snippet: "The relay drops a reply",
  };
  searchRead.mockResolvedValue(searchResponse([hit]));
  renderPage();
  const field = writeField();

  const described = (field.getAttribute("aria-describedby") ?? "").split(/\s+/).flatMap((id) => {
    const line = id === "" ? null : document.getElementById(id);
    return line === null ? [] : [line];
  });
  const hint = described.find(
    (line) => line.textContent === dispatchWritingBox("issue-page-composer", "comment").hint
  );
  expect(hint).toBeDefined();
  expect(hint).toHaveClass("sr-only");
  expect(composerBox().contains(hint ?? null)).toBe(true);

  await user.click(field);
  await user.paste(`${PLANNER_MENTION} `);
  const targets = within(composerBox()).getByRole("list", { name: "Mention targets" });
  expect(targets).toHaveClass("sr-only");
  expect(targets).toHaveTextContent("session:planner-session");

  await user.keyboard("#relay");
  const list = await within(composerBox()).findByRole("listbox", { name: "References" });
  const row = await within(list).findByRole("option", { name: /The relay drops a reply/ });
  const route = nodeRouteFromHref(hit.href);
  if (route === undefined) throw new Error("the hit names no address");
  // The row draws its detail line, and nothing in the list carries a title for it.
  expect(within(row).getByText(shortForm(route))).toBeInTheDocument();
  expect(list.querySelectorAll("[title]")).toHaveLength(0);
});

// The Design's "focused row": the box publishes how far up from the screen's foot it reaches
// (`FOOT_HEIGHT_PROPERTY`) while it is drawn, keeps it as the box changes size, and takes it
// away while it is hidden, so a row brought on screen is brought clear of it.
test("the box sets the foot height while drawn and removes it while hidden", async () => {
  const page = renderPage({ viewOn: true });
  const section = composerBox();
  expect(section).toHaveAttribute("hidden");
  expect(footHeight()).toBe("");

  // happy-dom lays nothing out, so the section answers with the place the page gives it: its top
  // `reach` px above the screen's foot.
  let reach = 58;
  section.getBoundingClientRect = () => new DOMRect(0, window.innerHeight - reach, 1000, reach);
  page.redraw({});
  expect(section).not.toHaveAttribute("hidden");
  expect(footHeight()).toBe("58px");
  // A window resized: user-event has no form for it, so the window is told as a browser tells it.
  reach = 96;
  act(() => {
    window.dispatchEvent(new Event("resize"));
  });
  expect(footHeight()).toBe("96px");

  page.redraw({ viewOn: true });
  expect(section).toHaveAttribute("hidden");
  expect(footHeight()).toBe("");
  await act(async () => page.writer().replyTo(BOBS_COMMENT));
  await waitFor(() => expect(section).not.toHaveAttribute("hidden"));
  expect(footHeight()).toBe("96px");
  await act(async () => page.writer().clear());
  expect(section).toHaveAttribute("hidden");
  expect(footHeight()).toBe("");
});

// IC:92: the line that writes on the issue stays mounted while the box answers a question or
// edits a comment, so what the reader was typing there is still there when that ends.
test("a comment typed in the box is still there after an answer or an edit ends", async () => {
  const user = userEvent.setup();
  stub(
    spyOn(api, "answerAsk").mockResolvedValue(
      askRecord("ask-1", {
        answer: { at: "2026-11-07T00:00:00Z", selected: ["Yes"], text: "", user: READER },
        state: "answered",
      })
    )
  );
  const page = renderPage();
  await user.click(writeField());
  await user.keyboard("Half a thought");

  // An answer, sent.
  await page.press(user, "ask-1", "a");
  await waitFor(() => expect(answerField()).toHaveFocus());
  expect(writeField().closest("[hidden]")).not.toBeNull();
  expect(writeField().value).toBe("Half a thought");
  await user.click(within(answerForm()).getByRole("button", { name: "Yes" }));
  await user.click(answerField());
  await user.keyboard("{Enter}");
  await waitFor(() => expect(page.writer().answer).toBeUndefined());
  expect(writeField().closest("[hidden]")).toBeNull();
  expect(writeField().value).toBe("Half a thought");

  // An edit, left with Escape.
  await act(async () => page.writer().edit("comment-2", "My own words"));
  const edit = within(composerBox()).getByRole("textbox", { name: "Edit comment" });
  await waitFor(() => expect(edit).toHaveFocus());
  expect(writeField().value).toBe("Half a thought");
  await user.keyboard("{Escape}");
  await waitFor(() => expect(page.writer().editing).toBeUndefined());
  expect(writeField().closest("[hidden]")).toBeNull();
  expect(writeField().value).toBe("Half a thought");
});

// IC:219: a change refused because the answer changed under it takes the current answer, its
// words in the field and its time, so the next send is measured against that answer.
test("a change refused because the answer changed sends the current answer's time next", async () => {
  const user = userEvent.setup();
  const newer = "2026-11-08T00:00:00Z";
  const answerAsk = stub(
    spyOn(api, "answerAsk")
      .mockImplementationOnce(async () => {
        // The answer moved on another surface: the read that answers next holds it.
        reads.asks = [askRecord("ask-1"), answeredAsk(newer, "Their newer words")];
        throw new ApiError(409, {
          code: "ASK_ANSWER_CHANGED",
          error: "The answer changed since you opened it.",
        });
      })
      .mockImplementation(async () => answeredAsk("2026-11-09T00:00:00Z", "Their newer words"))
  );
  const page = renderPage();

  await act(async () => page.writer().changeAnswer(askNode("ask-2", "answered")));
  await waitFor(() => expect(answerField().value).toBe("Old reason"));
  await user.click(answerField());
  await user.keyboard("{Control>}a{/Control}My change{Enter}");
  await waitFor(() => expect(answerAsk).toHaveBeenCalledTimes(1));
  expect(answerAsk.mock.calls[0]?.[1]).toMatchObject({ expected_answer_at: ANSWERED_AT });

  await waitFor(() => expect(answerField().value).toBe("Their newer words"));
  expect(answerForm()).toHaveTextContent(
    "The answer changed since you opened it; here is the current one."
  );
  await user.click(answerField());
  await user.keyboard("{Enter}");
  await waitFor(() => expect(answerAsk).toHaveBeenCalledTimes(2));
  expect(answerAsk.mock.calls[1]?.[1]).toMatchObject({
    expected_answer_at: newer,
    text: "Their newer words",
  });
});

// IC:219: a change Dispatch refuses because another person answered ends the change - nothing of
// it can be sent again - and the line says why.
test("a change refused to another answerer ends the change and says why", async () => {
  const user = userEvent.setup();
  const why = "Only the person who answered can change this answer.";
  reads.asks = [askRecord("ask-1"), answeredAsk(ANSWERED_AT, "Bob's reason", "bob")];
  const answerAsk = stub(
    spyOn(api, "answerAsk").mockRejectedValue(
      new ApiError(403, { code: "NOT_ANSWERER", error: "only the answerer may change it" })
    )
  );
  const page = renderPage();

  await act(async () => page.writer().changeAnswer(askNode("ask-2", "answered")));
  await waitFor(() => expect(answerField().value).toBe("Bob's reason"));
  await user.click(answerField());
  await user.keyboard(" and mine{Enter}");
  await waitFor(() => expect(answerAsk).toHaveBeenCalledTimes(1));

  await waitFor(() => expect(answerForm()).toHaveTextContent(why));
  const send = within(answerForm()).getByRole("button", { name: CONTROL_LABELS.send_answer });
  expect(send).toHaveAttribute("aria-disabled", "true");
  // Send says why it is refused, on the line its description names (`RefusableButton`).
  const reason = send.getAttribute("aria-describedby") ?? "";
  expect(document.getElementById(reason)?.textContent).toBe(why);
  await user.click(send);
  await user.click(answerField());
  await user.keyboard("{Enter}");
  expect(answerAsk).toHaveBeenCalledTimes(1);
});

// IC:232: a question answered elsewhere while the line holds it is not answered here: the line
// goes back to writing on the issue, taking the reader to the question where the line held
// their focus, and leaving the focus where it is otherwise.
test("the answer line closes when its question is answered elsewhere", async () => {
  const user = userEvent.setup();
  const page = renderPage();
  const answeredByBob = askRecord("ask-1", {
    answer: { at: "2026-11-07T00:00:00Z", selected: ["No"], text: "", user: "bob" },
    state: "answered",
  });

  await page.press(user, "ask-1", "a");
  await waitFor(() => expect(answerField()).toHaveFocus());
  act(() => {
    page.client.setQueryData(["asks", KEY], [answeredByBob, reads.asks[1]]);
  });
  await waitFor(() => expect(page.writer().answer).toBeUndefined());
  expect(composerBox().querySelector("form[data-answering]")).toBeNull();
  await waitFor(() => expect(focusedRowId()).toBe("ask-1"));

  // The reader's focus elsewhere: the line closes and the focus stays where they put it.
  act(() => {
    page.client.setQueryData(["asks", KEY], reads.asks);
  });
  await page.press(user, "ask-1", "a");
  await waitFor(() => expect(answerField()).toHaveFocus());
  await act(async () => rowOf("comment-1").focus());
  act(() => {
    page.client.setQueryData(["asks", KEY], [answeredByBob, reads.asks[1]]);
  });
  await waitFor(() => expect(page.writer().answer).toBeUndefined());
  expect(page.writer().landing).toBeUndefined();
  expect(focusedRowId()).toBe("comment-1");
});

// IC:239, IC:160: Escape from any control of the answer line - its field, each option, Decline
// and Send answer - and its × each end the answer and give the focus back to the question's row.
test("Escape from any control of the answer line, and its close control, give the question's row back", async () => {
  const user = userEvent.setup();
  const page = renderPage();
  const controls: readonly { readonly name: string; readonly take: () => HTMLElement }[] = [
    { name: "the field", take: answerField },
    { name: "Yes", take: () => within(answerForm()).getByRole("button", { name: "Yes" }) },
    { name: "No", take: () => within(answerForm()).getByRole("button", { name: "No" }) },
    {
      name: "Decline to answer",
      take: () => within(answerForm()).getByRole("button", { name: "Decline to answer" }),
    },
    {
      name: "Send answer",
      take: () => within(answerForm()).getByRole("button", { name: CONTROL_LABELS.send_answer }),
    },
  ];
  for (const { name, take } of controls) {
    await page.press(user, "ask-1", "a");
    await waitFor(() => expect(answerField()).toHaveFocus());
    await act(async () => take().focus());
    await user.keyboard("{Escape}");
    await waitFor(() => expect(page.writer().answer, name).toBeUndefined());
    await waitFor(() => expect(focusedRowId(), name).toBe("ask-1"));
  }

  await page.press(user, "ask-1", "a");
  await waitFor(() => expect(answerField()).toHaveFocus());
  await user.click(
    within(answerForm()).getByRole("button", { name: CONTROL_LABELS.write_on_issue })
  );
  await waitFor(() => expect(page.writer().answer).toBeUndefined());
  await waitFor(() => expect(focusedRowId()).toBe("ask-1"));
});

// IC:355: the decline dialog is drawn outside the answer's form, so Enter, Escape and
// Ctrl/Cmd+Enter typed in its note are the dialog's: a new line, its own close, and a decline -
// never the answer sent, and never the answer line ended.
test("keys typed in the decline note stay in the decline dialog", async () => {
  const user = userEvent.setup();
  // An answer the box must not send, refused as Dispatch would refuse a stale one.
  const answerAsk = stub(
    spyOn(api, "answerAsk").mockRejectedValue(
      new ApiError(409, { code: "ASK_CLOSED", error: "This ask was already answered." })
    )
  );
  const declineAsk = stub(
    spyOn(api, "declineAsk")
      .mockRejectedValueOnce(
        new ApiError(409, { code: "ASK_NOT_DECLINABLE", error: "This question is already closed." })
      )
      .mockResolvedValue(
        askRecord("ask-1", {
          resolution: {
            actor: ALICE,
            at: "2026-11-07T00:00:00Z",
            dispatch_words: "",
            kind: "declined",
            reason: "Not mine to answer",
          },
          state: "resolved",
        })
      )
  );
  const page = renderPage();
  await page.press(user, "ask-1", "a");
  await waitFor(() => expect(answerField()).toHaveFocus());
  // Words in the answer, so a key that reached its form would send them.
  await user.keyboard("My answer");

  const openNote = async () => {
    await user.click(within(answerForm()).getByRole("button", { name: "Decline to answer" }));
    const dialog = await screen.findByRole("dialog", { name: "Decline to answer" });
    const note = within(dialog).getByLabelText<HTMLTextAreaElement>("Note (optional)");
    await waitFor(() => expect(note).toHaveFocus());
    return { dialog, note };
  };

  // Enter is a new line of the note.
  const first = await openNote();
  await user.keyboard("Not mine{Enter}");
  expect(first.note.value).toBe("Not mine\n");
  expect(screen.getByRole("dialog", { name: "Decline to answer" })).toBeInTheDocument();
  // Escape closes the dialog alone.
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(page.writer().answer?.askId).toBe("ask-1");
  expect(answerField().value).toBe("My answer");

  // Ctrl+Enter, then Cmd+Enter, decline.
  const second = await openNote();
  await user.keyboard("Not mine to answer");
  await user.keyboard("{Control>}{Enter}{/Control}");
  await waitFor(() => expect(declineAsk).toHaveBeenCalledTimes(1));
  expect(await within(second.dialog).findByRole("alert")).toHaveTextContent(
    "This question is already closed."
  );
  expect(page.writer().answer?.askId).toBe("ask-1");
  await user.click(second.note);
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await waitFor(() => expect(declineAsk).toHaveBeenCalledTimes(2));
  expect(declineAsk).toHaveBeenLastCalledWith(
    "ask-1",
    { note: "Not mine to answer" },
    keyedAttempt
  );
  await waitFor(() => expect(page.writer().answer).toBeUndefined());
  expect(answerAsk).not.toHaveBeenCalled();
});

// Departure 1 of the plan: under a view no outline is drawn to consume a landing, so a write sent
// there leaves none, and the focus goes back to the control the reader came to the box from.
test("under a view a sent write leaves no landing and gives the focus back where it came from", async () => {
  const user = userEvent.setup();
  const createComment = stub(
    spyOn(api, "createComment").mockResolvedValue(
      commentRecord("comment-9", ALICE, "Under the view", { reply_to: "comment-1" })
    )
  );
  const answerAsk = stub(
    spyOn(api, "answerAsk").mockResolvedValue(
      askRecord("ask-1", {
        answer: { at: "2026-11-07T00:00:00Z", selected: [], text: "Yes please", user: READER },
        state: "answered",
      })
    )
  );
  const page = renderPage({ viewOn: true });
  expect(composerBox()).toHaveAttribute("hidden");

  const reply = screen.getByRole("button", { name: "Reply" });
  await user.click(reply);
  await waitFor(() => expect(writeField()).toHaveFocus());
  expect(composerBox()).not.toHaveAttribute("hidden");
  await user.keyboard("Under the view{Enter}");
  await waitFor(() =>
    expect(createComment).toHaveBeenCalledWith(
      KEY,
      { body: "Under the view", reply_to: "comment-1" },
      keyedAttempt
    )
  );
  await waitFor(() => expect(reply).toHaveFocus());
  expect(page.writer().landing).toBeUndefined();
  expect(page.writer().reply).toBeNull();
  expect(composerBox()).toHaveAttribute("hidden");

  const answer = screen.getByRole("button", { name: "Answer" });
  await user.click(answer);
  await waitFor(() => expect(answerField()).toHaveFocus());
  await user.keyboard("Yes please{Enter}");
  await waitFor(() => expect(answerAsk).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(answer).toHaveFocus());
  expect(page.writer().landing).toBeUndefined();
  expect(page.writer().answer).toBeUndefined();
  expect(composerBox()).toHaveAttribute("hidden");
});

// R11: `r` on a statement aims the box at it: the target line names the statement by its first
// line in plain words, and the comment sent carries the statement's whole words as its anchor's
// quote, with no occurrence, so words the document holds twice are refused rather than anchored
// to the wrong block. The comment lands under the statement.
test("a statement's target line names its first line, and its send carries its words as the quote", async () => {
  const user = userEvent.setup();
  const createComment = stub(
    spyOn(api, "createComment").mockResolvedValue(commentRecord("comment-9", ALICE, "Agreed"))
  );
  const page = renderPage();

  await page.press(user, "spec#b-r1", "r");
  await waitFor(() => expect(writeField()).toHaveFocus());
  await waitFor(() =>
    expect(targetWords()).toBe(`${CONTROL_LABELS.replying_to} The page draws one box.`)
  );
  expect(writeField()).toHaveAttribute("placeholder", CONTROL_LABELS.reply_placeholder);
  expect(kinds()).toEqual([]);

  await user.keyboard("Agreed{Enter}");
  await waitFor(() => expect(createComment).toHaveBeenCalledTimes(1));
  const [key, input, bounds] = createComment.mock.calls[0] ?? [];
  expect(key).toBe(KEY);
  expect(bounds).toEqual(keyedAttempt);
  expect(input).toEqual({ anchor: { artifact: "spec", quote: STATEMENT_QUOTE }, body: "Agreed" });
  expect(Object.keys(input?.anchor ?? {}).sort()).toEqual(["artifact", "quote"]);
  await waitFor(() => expect(page.writer().statement).toBeUndefined());
  expect(page.writer().landing).toEqual({
    to: "newest",
    under: { id: "spec#b-r1", kind: "statement" },
  });
});

// The reviewer's finding 4593511319: a statement's row holds the coverage read's words, which
// leave each of its references out (`blockWords` in Go's `api/results.go`), beside the reading
// of its whole words, which keeps them. The target line reads a reference that reading holds as
// the name the row draws for it - the issue's key and title - and never as its address.
test("a statement's target line names the issue its words cite, never its address", async () => {
  const user = userEvent.setup();
  const tree = displayTree(
    "A comment on this statement is drawn under it, as dispatch://CORE-2 asks."
  );
  if (tree === undefined) throw new Error("the engine read no statement");
  const summary = stub(
    spyOn(api, "getIssueSummary").mockImplementation(async (key) => ({
      documents: [],
      key,
      project: "CORE",
      status: "in_progress",
      title: "The issue a statement cites",
    }))
  );
  const root = node({
    children: [
      node({
        id: "spec#b-r2",
        reference: `dispatch://${KEY}/spec#b-r2`,
        source: SPEC_SOURCE,
        // The coverage read's words, the address left out, as the page's row holds them.
        text: "A comment on this statement is drawn under it, as asks.",
        tree,
        type: "requirement",
      }),
    ],
    id: KEY,
    type: "issue",
  });
  const page = renderPage({}, root);

  await page.press(user, "spec#b-r2", "r");
  await waitFor(() => expect(writeField()).toHaveFocus());
  await waitFor(() =>
    expect(targetWords()).toBe(
      `${CONTROL_LABELS.replying_to} A comment on this statement is drawn under it, as CORE-2 The issue a statement cites asks.`
    )
  );
  expect(summary).toHaveBeenCalledWith("CORE-2");
});

// The reviewer's finding 4593511280: a comment on a statement Dispatch refused holds the box on
// the statement - a Reply on another row, or an Answer, puts the caret back in it on that
// statement, its refused words kept - and a box that mounts again over the refused send, as one
// does when the reader leaves the issue and comes back, names the statement from the send and
// sends the words to it again. The send keeps the statement's document and words alone, so the
// line names it by those words, and a write to it lands on no row.
test("a refused comment on a statement keeps its words and its statement, through a Reply elsewhere and a new mount", async () => {
  const user = userEvent.setup();
  const createComment = stub(
    spyOn(api, "createComment")
      .mockRejectedValueOnce(
        new ApiError(400, { code: "INVALID_INPUT", error: "The comment was refused." })
      )
      .mockResolvedValue(commentRecord("comment-9", ALICE, "Agreed"))
  );
  const first = renderPage();
  await first.press(user, "spec#b-r1", "r");
  await waitFor(() => expect(writeField()).toHaveFocus());
  await user.keyboard("Agreed{Enter}");
  await waitFor(() =>
    expect(within(composerBox()).getByRole("alert")).toHaveTextContent("The comment was refused.")
  );

  // `r` on bob's comment, and `a` on the open question.
  const named = `${CONTROL_LABELS.replying_to} The page draws one box.`;
  for (const [row, key] of [
    ["comment-1", "r"],
    ["ask-1", "a"],
  ] as const) {
    await first.press(user, row, key);
    await waitFor(() => expect(writeField()).toHaveFocus());
    expect(targetWords(), `${key} on ${row}`).toBe(named);
    expect(writeField().value, `${key} on ${row}`).toBe("Agreed");
    expect(first.writer().reply, `${key} on ${row}`).toBeNull();
    expect(first.writer().answer, `${key} on ${row}`).toBeUndefined();
  }

  // The box mounted again in the same tab, over the send it refused: the statement named by the
  // first line of its words, as its row names it.
  first.view.unmount();
  const again = renderPage({}, outline(), first.client);
  await waitFor(() => expect(targetWords()).toBe(named));
  expect(again.writer().statement).toEqual({ artifact: "spec", quote: STATEMENT_QUOTE });
  expect(writeField().value).toBe("Agreed");
  await user.click(within(composerBox()).getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(createComment).toHaveBeenCalledTimes(2));
  expect(createComment.mock.calls[1]?.[1]).toEqual({
    anchor: { artifact: "spec", quote: STATEMENT_QUOTE },
    body: "Agreed",
  });
  await waitFor(() => expect(again.writer().statement).toBeUndefined());
  expect(again.writer().landing).toBeUndefined();
});

// The reviewer's finding 4593511306: a statement whose words its document holds twice is
// refused with `TARGET_AMBIGUOUS`, whose own words ask for an occurrence the box has no control
// for: the box says in the page's words why the comment cannot be placed, draws no Retry, which
// would send the same words to the same refusal, and keeps the draft and its statement.
test("a statement held twice is refused in the page's words, with no Retry and the draft kept", async () => {
  const user = userEvent.setup();
  const createComment = stub(
    spyOn(api, "createComment").mockRejectedValue(
      new ApiError(409, {
        code: "TARGET_AMBIGUOUS",
        error: `"${STATEMENT_QUOTE}" matches 2 places; pass a zero-based occurrence, or quote more of the surrounding text`,
      })
    )
  );
  const page = renderPage();
  await page.press(user, "spec#b-r1", "r");
  await waitFor(() => expect(writeField()).toHaveFocus());
  await user.keyboard("Agreed{Enter}");
  await waitFor(() => expect(createComment).toHaveBeenCalledTimes(1));
  const alert = await within(composerBox()).findByRole("alert");
  expect(alert).toHaveTextContent(ANSWER_LINES.statement_twice);
  expect(alert.textContent).not.toContain("occurrence");
  expect(within(composerBox()).queryByRole("button", { name: "Retry" })).toBeNull();
  expect(writeField().value).toBe("Agreed");
  expect(targetWords()).toBe(`${CONTROL_LABELS.replying_to} The page draws one box.`);
});

// The reviewer's finding PRRT_kwDORFy7ds64Sg45: a comment on a statement refused with
// `TARGET_AMBIGUOUS` holds the box on its statement only while the held send carries it. The
// target line's control takes the statement off the held send, its refusal and its words kept,
// so `r` on another row then aims the box at that row, the refused words kept, and `a` on an
// open question draws the answer line.
test("a refused statement let go with the target line's control frees a Reply and an Answer", async () => {
  const user = userEvent.setup();
  const createComment = stub(
    spyOn(api, "createComment").mockRejectedValue(
      new ApiError(409, {
        code: "TARGET_AMBIGUOUS",
        error: `"${STATEMENT_QUOTE}" matches 2 places; pass a zero-based occurrence, or quote more of the surrounding text`,
      })
    )
  );
  const page = renderPage();
  await page.press(user, "spec#b-r1", "r");
  await waitFor(() => expect(writeField()).toHaveFocus());
  await user.keyboard("Agreed{Enter}");
  await waitFor(() => expect(createComment).toHaveBeenCalledTimes(1));
  expect(await within(composerBox()).findByRole("alert")).toHaveTextContent(
    ANSWER_LINES.statement_twice
  );

  // The target line's control: the box writes on the issue.
  await user.click(
    within(composerBox()).getByRole("button", { name: CONTROL_LABELS.write_on_issue })
  );
  await waitFor(() => expect(targetWords()).toBeNull());
  expect(page.writer().statement).toBeUndefined();

  // `r` on bob's comment: a reply to it, the refused words still the box's draft.
  await page.press(user, "comment-1", "r");
  await waitFor(() =>
    expect(targetWords()).toBe(`${CONTROL_LABELS.replying_to} bob Earlier words`)
  );
  await waitFor(() => expect(writeField()).toHaveFocus());
  expect(page.writer().reply?.id).toBe("comment-1");
  expect(page.writer().statement).toBeUndefined();
  expect(writeField().value).toBe("Agreed");

  // `a` on the open question: the answer line.
  await page.press(user, "ask-1", "a");
  await waitFor(() => expect(answerField()).toHaveFocus());
  expect(targetWords()).toBe(`${CONTROL_LABELS.answering} Question ask-1?`);
  expect(page.writer().answer?.askId).toBe("ask-1");
  expect(page.writer().statement).toBeUndefined();

  // The refusal is still held, with no statement left on it.
  const held = heldSends(page.client).get(["issue-composer", KEY]);
  expect(held?.status).toBe("refused");
  expect(held?.request.statement).toBeUndefined();
});

// The reviewer's finding PRRT_kwDORFy7ds64Sg4-: a reply Dispatch refused keeps the box on the
// comment it answers when the reader presses `r` on a statement, as a Reply on another row does
// (`redirectedReplyTarget`), so Enter sends the words again as that reply and never as a comment
// anchored to the statement.
test("r on a statement keeps the box on a refused reply, and the send carries the reply", async () => {
  const user = userEvent.setup();
  const createComment = stub(
    spyOn(api, "createComment")
      .mockRejectedValueOnce(
        new ApiError(400, { code: "INVALID_INPUT", error: "The reply was refused." })
      )
      .mockResolvedValue(
        commentRecord("comment-9", ALICE, "My reply to bob", { reply_to: "comment-1" })
      )
  );
  const page = renderPage();
  await page.press(user, "comment-1", "r");
  await waitFor(() => expect(writeField()).toHaveFocus());
  await user.keyboard("My reply to bob{Enter}");
  await waitFor(() =>
    expect(within(composerBox()).getByRole("alert")).toHaveTextContent("The reply was refused.")
  );

  // `r` on the statement: the box stays on bob's comment, the refused words in it.
  await page.press(user, "spec#b-r1", "r");
  await waitFor(() => expect(writeField()).toHaveFocus());
  expect(targetWords()).toBe(`${CONTROL_LABELS.replying_to} bob Earlier words`);
  expect(page.writer().reply?.id).toBe("comment-1");
  expect(page.writer().statement).toBeUndefined();
  expect(writeField().value).toBe("My reply to bob");

  // Enter sends the words again as the reply, with no statement's anchor.
  await user.keyboard("{Enter}");
  await waitFor(() => expect(createComment).toHaveBeenCalledTimes(2));
  expect(createComment.mock.calls[1]?.[1]).toEqual({
    body: "My reply to bob",
    reply_to: "comment-1",
  });
});

// The reading list's `issue-page-composer-target`: the target line and the answer's options draw
// a stored text as its plain words - formatting gone, a reference read as the name it reads as -
// never as the characters it is stored as.
test("the target line reads the entry, the question and its options as their plain words", async () => {
  const user = userEvent.setup();
  const entry = `I saw **the relay** drop ${PLANNER_MENTION}`;
  const question = `Ship **which** path for ${PLANNER_MENTION}?`;
  const option = `**Fast** for ${PLANNER_MENTION}`;
  const questionTree = displayTree(question);
  const optionTree = displayTree(option);
  if (questionTree === undefined || optionTree === undefined) {
    throw new Error("the engine read no question");
  }
  reads.comments = [...reads.comments, commentRecord("comment-3", BOB, entry)];
  reads.asks = [
    ...reads.asks,
    askRecord("ask-3", {
      options: [{ label: option, label_tree: optionTree }, { label: "Slow" }],
      question,
      question_tree: questionTree,
    }),
  ];
  const page = renderPage();

  // The entry a reply answers.
  await act(async () =>
    page.writer().replyTo({ author: BOB, id: "comment-3", kind: "comment", text: entry })
  );
  await waitFor(() =>
    expect(targetWords()).toBe(`${CONTROL_LABELS.replying_to} bob I saw the relay drop @Planner`)
  );

  // The question an answer answers, its options, and the one the reader chose.
  await act(async () => page.writer().answerNode({ ...askNode("ask-3", "open"), text: question }));
  await waitFor(() =>
    expect(targetWords()).toBe(`${CONTROL_LABELS.answering} Ship which path for @Planner?`)
  );
  const chip = within(answerForm()).getByRole("button", { name: "Fast for @Planner" });
  expect(chip.textContent).toBe("Fast for @Planner");
  await user.click(chip);
  await waitFor(() =>
    expect(targetWords()).toBe(
      `${CONTROL_LABELS.answering} Ship which path for @Planner? · ${CONTROL_LABELS.answer_chose} “Fast for @Planner”`
    )
  );
});

/** The classes of `element` and of everything inside it that are a colour of the dashboard's
 *  palette (`theme/classes.ts`'s slate greys and rose and red refusals), which nothing inside the
 *  issue page is drawn in (decision 56 of dispatch://INFRA-1196). */
function dashboardColours(element: Element): string[] {
  return [element, ...element.querySelectorAll("*")]
    .flatMap((one) => [...one.classList])
    .filter((name) => /(?:slate|rose|red)-\d/u.test(name));
}

// The reviewer's finding PRRT_kwDORFy7ds64NIrQ: what the box says of a file pasted into its
// draft - the upload still out, an upload Dispatch refused with its Retry, one Dispatch stored
// and lost its answer for - and of a send past its deadline, with Stop trying, is drawn in the
// page's colours and words, as the box's refusal is, and never in the dashboard's.
test("the box draws an upload's lines and a late send's in the page's colours", async () => {
  jest.useFakeTimers();
  const user = userOnFakeClock();
  // The first upload stays out until the case refuses it, from inside the request it made, so
  // the refusal reaches a write that is waiting for it.
  let refuseUpload: ((refusal: ApiError) => void) | undefined;
  stub(
    spyOn(api, "uploadArtifact")
      .mockImplementationOnce(() => {
        const { promise, reject } = Promise.withResolvers<UploadResult>();
        refuseUpload = reject;
        return promise;
      })
      .mockRejectedValueOnce(new ApiError(409, { code: "KEY_COMMITTED", error: "already made" }))
  );
  const answer = Promise.withResolvers<CommentWithTrees>();
  const createComment = stub(spyOn(api, "createComment").mockReturnValueOnce(answer.promise));
  try {
    renderPage();
    await user.click(writeField());
    const file = new File(["# Notes"], "notes.md", { type: "text/markdown" });
    // user-event's paste carries text, never files.
    fireEvent.paste(writeField(), { clipboardData: { files: [file] } });

    // The upload out: the page's busy word, in its muted grey.
    const uploading = await within(composerBox()).findByText(ANSWER_LINES.uploading, {
      selector: '[role="status"]',
    });
    expect(dashboardColours(uploading)).toEqual([]);
    expect(uploading).toHaveClass(pageMutedText);

    // Refused: the server's reason in the page's red, with a Retry whose press leaves the caret
    // in the field.
    await waitFor(() => expect(refuseUpload).toBeDefined());
    await act(async () =>
      refuseUpload?.(
        new ApiError(400, { code: "INVALID_INPUT", error: "The file is not allowed." })
      )
    );
    const refused = (await within(composerBox()).findByText("The file is not allowed.")).closest(
      '[role="alert"]'
    );
    if (refused === null) throw new Error("the box drew the upload's refusal as no alert");
    expect(dashboardColours(refused)).toEqual([]);
    expect(refused).toHaveClass(pageFailedText);
    await user.click(within(refused as HTMLElement).getByRole("button", { name: "Retry" }));
    expect(writeField()).toHaveFocus();

    // Stored with no answer: the page's own sentence, in its muted grey.
    const unanswered = await within(composerBox()).findByTestId("landed-without-answer");
    expect(unanswered.textContent).toBe(ANSWER_LINES.uploaded_unanswered);
    expect(dashboardColours(unanswered)).toEqual([]);
    expect(unanswered).toHaveClass(pageMutedText);

    // A send past its deadline: still being tried, in the page's muted grey, beside a Stop
    // trying drawn in the page's look.
    await user.keyboard("Agreed{Enter}");
    await waitFor(() => expect(createComment).toHaveBeenCalledTimes(1));
    await jump(SEND_DEADLINE_MS);
    const late = within(composerBox()).getByText(KEYED_SEND_LATE_TEXT);
    expect(late).toHaveClass(pageMutedText);
    const lateLine = late.parentElement;
    if (lateLine === null) throw new Error("the box drew the late line alone");
    expect(dashboardColours(lateLine)).toEqual([]);
    expect(within(lateLine).getByRole("button", { name: "Stop trying" })).toHaveClass(
      pageMutedText
    );

    await act(async () => answer.resolve(commentRecord("comment-9", ALICE, "Agreed")));
    await waitFor(() => expect(writeField().value).toBe(""));
  } finally {
    realClock();
  }
});
