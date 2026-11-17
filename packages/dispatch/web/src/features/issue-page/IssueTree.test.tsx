import { afterEach, expect, jest, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useState } from "react";
import { MemoryRouter, useLocation } from "react-router-dom";

import { jump, realClock } from "../../__tests__/fake-clock";
import { userOnFakeClock } from "../../__tests__/user";
import { api } from "../../api/client";
import { issueCoverageQuery, issueResultsQuery } from "../../api/queries";
import type { Artifact, Decision, IssueDecisions, IssueDetails } from "../../api/types";
import { pageFocusedRow } from "../../theme/classes";
import { MarginProvider } from "../margin/margin-context";
import { buildDispatchReference, buildIssuePath } from "../refs/routes";
import { KeymapProvider } from "../shell/KeymapProvider";
import { useKeymapScope } from "../shell/keymap";
import { issueDetails } from "./decisions-filter/decisions-fixtures";
import { type FocusedNode, FocusedNodeProvider, useFocusedNode } from "./focused-node";
import { IssueTree, LANDING_WAIT_MS, ZOOMED_TREE_QUERY } from "./IssueTree";
import type { TreeNode } from "./issue-tree";
import {
  OutlineRequestProvider,
  useAskOutline,
  useFocusBranch,
  useOutlineRequest,
} from "./outline-request";
import {
  type Landing,
  type OutlineWriter,
  OutlineWriterProvider,
  useOutlineWriter,
} from "./outline-writer";
import { KEY_REPORTS } from "./page-structure";
import { CONTROL_LABELS, EMPTY_STATE_LINES, NODE_TYPE_NAMES, type NodeType } from "./page-words";
import { BodySlot } from "./slots/body";
import { ProvidersSlot } from "./slots/providers";
import { addressesAsShortForms } from "./tree-drafts";

/**
 * What a reader does to the tree with the keyboard alone (dispatch://INFRA-1196 R6, R9, R23).
 *
 * The tree is rendered here rather than through `IssuePage`, because what these rows are about
 * is where the focus goes and which control a key presses, and that needs a tree whose shape -
 * one node under two parents, two open questions in different branches - the page's own reads
 * would have to be fabricated backwards to produce.
 */

const KEY = "CORE-1";

const decisionsDocument: Artifact = {
  created_at: "2026-11-05T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  id: "artifact-decisions",
  issue_key: KEY,
  kind: "doc",
  name: "Decisions",
  primary: false,
  project: "CORE",
  ref_key: "artifact:artifact-decisions",
  session_id: null,
  slug: "decisions",
  versions: [],
};

const decision: Decision = {
  asks: [],
  block_id: "d1",
  changed_at: null,
  changed_version: 1,
  heading: "Ship behind a flag",
  kept: null,
  kind: "agent",
  links: [],
  ref: `dispatch://${KEY}/decisions#b-d1`,
  replaced: false,
  text: "## Ship behind a flag\n\nWe ship it behind a flag.",
  text_sha256: "sha256:one",
  unsure: false,
  waiting_review: true,
};

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

/** happy-dom answers every media query against its own 1024 px window, where the tree draws
 *  three levels (`ZOOMED_TREE_QUERY` is below 1024 px). A test sets the width it reads: a
 *  `max-width` in px, a `min-width` in rem, and `not all and` either. */
function setViewport(width: number): void {
  window.matchMedia = ((query: string) => {
    const max = /max-width:\s*([\d.]+)px/.exec(query)?.[1];
    const min = /min-width:\s*([\d.]+)rem/.exec(query)?.[1];
    const matched =
      max !== undefined ? width <= Number(max) : min !== undefined && width >= Number(min) * 16;
    return {
      addEventListener: () => {},
      addListener: () => {},
      dispatchEvent: () => true,
      matches: query.startsWith("not all and ") ? !matched : matched,
      media: query,
      onchange: null,
      removeEventListener: () => {},
      removeListener: () => {},
    } as MediaQueryList;
  }) as typeof window.matchMedia;
}

const originalMatchMedia = window.matchMedia;
afterEach(() => {
  window.matchMedia = originalMatchMedia;
});

/** The page's own `issue` scope, which every one of the tree's keys is registered in. */
function IssueScope(): ReactNode {
  useKeymapScope("issue");
  return null;
}

/** Where the page's address stands, which the tree's own navigation moves (`t h`). */
function AddressProbe({ seen }: { seen: { path?: string } }): ReactNode {
  seen.path = useLocation().pathname;
  return null;
}

/** What the page's panel would draw the facts of: the node the tree last set as focused. */
function FocusedNodeProbe({ seen }: { seen: { node?: FocusedNode } }): ReactNode {
  seen.node = useFocusedNode();
  return null;
}

/** The tree alone, under the page's keymap and the focused node it sets. `reveal` is the chain
 *  down to the node an address names, which the tree is then to put the focus on
 *  (`slots/body.tsx`); `redraw` draws the same tree again, as a read answering does. */
function renderTree(
  root: TreeNode,
  {
    artifacts = [],
    decisions,
    reveal,
  }: { artifacts?: Artifact[]; decisions?: IssueDecisions; reveal?: readonly TreeNode[] } = {}
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  if (decisions !== undefined) client.setQueryData(["decisions", KEY], decisions);
  const edited: (string | undefined)[] = [];
  const seen: { path?: string } = {};
  const focused: { node?: FocusedNode } = {};
  const draw = () => (
    <MemoryRouter>
      <AddressProbe seen={seen} />
      <QueryClientProvider client={client}>
        <KeymapProvider>
          <MarginProvider>
            <FocusedNodeProvider>
              <IssueScope />
              <FocusedNodeProbe seen={focused} />
              <IssueTree
                artifacts={new Map(artifacts.map((artifact) => [artifact.slug, artifact]))}
                context={new Set()}
                emptyLine="Nothing here."
                filtering={false}
                focusReveal={reveal !== undefined}
                holdsEscape={false}
                isClosed={false}
                onEditing={(artifactId) => edited.push(artifactId)}
                reveal={reveal}
                root={root}
                rootRow
              />
            </FocusedNodeProvider>
          </MarginProvider>
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  const view = render(draw());
  return {
    address: () => seen.path,
    edited,
    focusedNode: () => focused.node,
    redraw: () => view.rerender(draw()),
    ...view,
  };
}

/** What the page-body harness hands back from inside the providers: the Contents list's ask and
 *  its Focus branch's setter, the row writer, and the switch that takes the tree off the page and
 *  draws it again. */
interface PageHold {
  ask?: (id: string | undefined, focusBranch: boolean) => void;
  focusBranch?: (next: boolean) => void;
  show?: (shown: boolean) => void;
  writer?: OutlineWriter;
}

/** The tree as `slots/body.tsx` draws it: from the last request of the page's
 *  `OutlineRequestProvider`, under the page's row writer, while it is shown. */
function PageBody({ drawn, held }: { drawn: TreeNode; held: PageHold }): ReactNode {
  held.ask = useAskOutline();
  held.focusBranch = useFocusBranch()[1];
  held.writer = useOutlineWriter();
  const request = useOutlineRequest();
  const [shown, setShown] = useState(true);
  held.show = setShown;
  return shown ? (
    <IssueTree
      artifacts={new Map()}
      context={new Set()}
      emptyLine="Nothing here."
      filtering={false}
      focusReveal={false}
      holdsEscape={false}
      isClosed={false}
      onEditing={() => {}}
      {...(request === undefined ? {} : { request })}
      reveal={undefined}
      root={drawn}
      rootRow
    />
  ) : null;
}

/**
 * The tree under the providers the page's `slots/providers.tsx` draws around its body - the row
 * writer and the Contents list's requests - with `reader` signed in. It hands back the list's
 * ask and its Focus branch toggle, the writer's landing, a switch that takes the tree off the
 * page and draws it again (a view drawn in its place and turned off), and a redraw over another
 * tree (a read answering).
 */
function renderPageTree(root: TreeNode, reader = "alice") {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  client.setQueryData(["whoami"], { kind: "user", login: reader });
  // The writer's own reads, answered empty, so it asks the network for nothing.
  for (const read of ["asks", "comments", "issue-messages"]) client.setQueryData([read, KEY], []);
  const held: PageHold = {};
  const draw = (drawn: TreeNode) => (
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <KeymapProvider>
          <MarginProvider>
            <OutlineWriterProvider issueKey={KEY}>
              <OutlineRequestProvider>
                <IssueScope />
                <PageBody drawn={drawn} held={held} />
              </OutlineRequestProvider>
            </OutlineWriterProvider>
          </MarginProvider>
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  const view = render(draw(root));
  return {
    ask: (id: string | undefined, focusBranch: boolean) =>
      act(async () => held.ask?.(id, focusBranch)),
    focusBranch: (next: boolean) => act(async () => held.focusBranch?.(next)),
    land: (under: string, to: Landing["to"] = "newest") =>
      act(async () => held.writer?.land({ to, under: { id: under, kind: "comment" } })),
    redraw: (drawn: TreeNode) => view.rerender(draw(drawn)),
    show: (shown: boolean) => act(async () => held.show?.(shown)),
    view,
    writer: () => held.writer,
  };
}

/** What a key reported at the row it was pressed on, or undefined where no row draws one. */
function rowReport(container: HTMLElement): string | undefined {
  return container.querySelector("[data-row-report]")?.textContent ?? undefined;
}

/** Every row drawn for `nodeId`, in the order the tree drew them: one per placement (R23). */
function rowsFor(container: HTMLElement, nodeId: string): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>(`[data-node-row][data-node-id="${nodeId}"]`)];
}

function focusedRow(container: HTMLElement): string | undefined {
  const active = document.activeElement;
  return [...container.querySelectorAll<HTMLElement>("[data-node-row]")]
    .find((row) => row.contains(active))
    ?.getAttribute("data-node-id") as string | undefined;
}

/** A comment with its own address, so its row, focused, draws its acts (Copy reference). */
function addressedComment(id: string, text: string): TreeNode {
  return node({
    id,
    reference: buildDispatchReference({ id, key: KEY, kind: "comment" }),
    text,
    type: "comment",
  });
}

/** A control of the page outside the outline - a view control, a control of Details, one under
 *  the outline - drawn after the tree, and how many times a press fired it. */
function controlOutside(): { readonly control: HTMLElement; readonly presses: () => number } {
  let presses = 0;
  const name = "A control outside the outline";
  const drawn = render(
    <button
      onClick={() => {
        presses += 1;
      }}
      type="button"
    >
      {name}
    </button>
  );
  return { control: drawn.getByRole("button", { name }), presses: () => presses };
}

test("the whole tree is one tab stop, and the focused row's keys work its controls", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const kept = spyOn(api, "keepDecision").mockResolvedValue({
    ...decision,
    waiting_review: false,
  } as never);
  const root = node({
    children: [
      node({
        chips: [
          {
            answered: true,
            gone: false,
            name: "The spec block",
            ref: `dispatch://${KEY}/spec#b-r1`,
          },
        ],
        id: "decisions#b-d1",
        reference: decision.ref,
        type: "decision",
        waiting: true,
      }),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root, {
    artifacts: [decisionsDocument],
    decisions: { decisions: [decision], document: null, open_questions: [] },
  });
  try {
    const [row] = rowsFor(view.container, "decisions#b-d1");
    if (row === undefined) throw new Error("the decision has no row");

    // The tree offers one tab stop and nothing inside a row offers another, standing on the
    // row included, so Tab enters the tree once and leaves it again (R9).
    await act(async () => row.focus());
    await waitFor(() => expect(view.getByRole("button", { name: "Keep" })).toBeTruthy());
    const stops = [...view.container.querySelectorAll('[tabindex="0"]')];
    expect(stops.length).toBe(1);
    expect(stops[0]?.getAttribute("data-node-row")).toBe("");

    // So each control of the row takes a key instead, and the keys reach what no Tab does
    // (WCAG 2.1.1): `f` steps to the chip's link, Escape gives the row back, Shift+K presses
    // Keep and Shift+C presses Change and leaves the caret in the form it opens.
    await user.keyboard("f");
    expect((document.activeElement as HTMLElement).textContent).toBe("The spec block");
    await user.keyboard("{Escape}");
    expect(document.activeElement).toBe(row);

    await user.keyboard("{Shift>}K{/Shift}");
    await waitFor(() => expect(kept).toHaveBeenCalled());
    expect(kept.mock.calls[0]?.[1]).toBe(decision.block_id);

    await user.keyboard("{Shift>}C{/Shift}");
    const field = await view.findByRole("textbox", { name: "Your change" });
    await waitFor(() => expect(document.activeElement).toBe(field));
  } finally {
    kept.mockRestore();
  }
});

/** The decisions document's text read, which a Change reads at its send (`changeAnchor`): with
 *  no reading of its own, the anchor names the quote and the server finds its one place. */
const DECISIONS_TEXT = { markdown: "", token: "sha256:decisions", version: null };

test("Enter sends the change the reader typed, instead of putting a caret in the row", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const sent = spyOn(api, "createComment").mockResolvedValue({ id: "comment-1" } as never);
  // The Change reads the decisions document's text at its send, to count its quote's place.
  const text = spyOn(api, "getArtifactText").mockResolvedValue(DECISIONS_TEXT);
  const root = node({
    children: [
      node({
        id: "decisions#b-d1",
        reference: decision.ref,
        type: "decision",
        waiting: true,
      }),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root, {
    artifacts: [decisionsDocument],
    decisions: { decisions: [decision], document: null, open_questions: [] },
  });
  try {
    const [row] = rowsFor(view.container, "decisions#b-d1");
    await act(async () => row?.focus());
    await user.click(await view.findByRole("button", { name: "Change" }));
    await user.click(view.getByRole("textbox", { name: "Your change" }));
    await user.keyboard("Say more about the flag.");

    // Enter on the form's own control sends it: the row's Enter fires from the row itself and
    // takes nothing from a control the reader reached inside it.
    await act(async () => view.getByRole("button", { name: "Send change" }).focus());
    await user.keyboard("{Enter}");
    await waitFor(() => expect(sent).toHaveBeenCalled());
    expect(sent.mock.calls[0]?.[0]).toBe(KEY);
    // Nothing went into an editor: the row's Enter never fired.
    expect(view.edited.filter((artifactId) => artifactId !== undefined)).toEqual([]);
  } finally {
    sent.mockRestore();
    text.mockRestore();
  }
});

/** The one decision's tree the two rows below press its controls in. */
function decisionTree(): TreeNode {
  return node({
    children: [
      node({ id: "decisions#b-d1", reference: decision.ref, type: "decision", waiting: true }),
    ],
    id: KEY,
    type: "issue",
  });
}

// A control that disables itself while its write is out drops the focus to the document body
// if it holds it: a keyboard reader loses their place, and the tree closes the body the focus
// held, taking Change with it (measured in Chromium: a `focusout` from Keep with no related
// target 45 ms after its write went out, then the kept decision's body removed). So Keep moves
// the focus onto its decision's own row first, and the row's body stays open throughout.
test("Keep keeps the focus on its decision's row while its write is out, and after", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const answer = Promise.withResolvers<never>();
  const kept = spyOn(api, "keepDecision").mockReturnValue(answer.promise);
  const view = renderTree(decisionTree(), {
    artifacts: [decisionsDocument],
    decisions: { decisions: [decision], document: null, open_questions: [] },
  });
  try {
    const [row] = rowsFor(view.container, "decisions#b-d1");
    if (row === undefined) throw new Error("the decision has no row");
    await act(async () => row.focus());
    const keep = await view.findByRole("button", { name: "Keep" });
    await user.click(keep);
    await waitFor(() => expect(kept).toHaveBeenCalled());
    await waitFor(() => expect(keep).toHaveProperty("disabled", true));
    expect(document.activeElement, "the focus stayed on Keep, which its write disables").toBe(row);
    expect(row.querySelector("[data-node-body]"), "the decision's body closed").not.toBeNull();

    await act(async () => answer.resolve({ ...decision, waiting_review: false } as never));
    await waitFor(() => expect(view.queryByRole("button", { name: "Keep" })).toBeNull());
    expect(document.activeElement).toBe(row);
    expect(view.getByRole("button", { name: "Change" })).toBeTruthy();
  } finally {
    kept.mockRestore();
  }
});

// The same for Change's own Send, which is disabled while the comment is out and goes with the
// form once it lands.
test("Send change keeps the focus on its decision's row while the comment is out, and after", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const answer = Promise.withResolvers<never>();
  const sent = spyOn(api, "createComment").mockReturnValue(answer.promise);
  const text = spyOn(api, "getArtifactText").mockResolvedValue(DECISIONS_TEXT);
  const view = renderTree(decisionTree(), {
    artifacts: [decisionsDocument],
    decisions: { decisions: [decision], document: null, open_questions: [] },
  });
  try {
    const [row] = rowsFor(view.container, "decisions#b-d1");
    if (row === undefined) throw new Error("the decision has no row");
    await act(async () => row.focus());
    await user.click(await view.findByRole("button", { name: "Change" }));
    await user.keyboard("Say more about the flag.");
    const send = view.getByRole("button", { name: "Send change" });
    await user.click(send);
    await waitFor(() => expect(sent).toHaveBeenCalled());
    await waitFor(() => expect(send).toHaveProperty("disabled", true));
    expect(
      document.activeElement,
      "the focus stayed on Send change, which its write disables"
    ).toBe(row);
    expect(row.querySelector("[data-node-body]"), "the decision's body closed").not.toBeNull();

    await act(async () => answer.resolve({ id: "comment-1" } as never));
    await waitFor(() => expect(view.queryByRole("textbox", { name: "Your change" })).toBeNull());
    expect(document.activeElement).toBe(row);
    expect(view.getByRole("button", { name: "Change" })).toBeTruthy();
  } finally {
    sent.mockRestore();
    text.mockRestore();
  }
});

// The outline holds G's `ChangeDraftsProvider` above its rows: a decision's body is drawn while
// its row has the focus, so a reader who opened Change, typed and moved on closes the body, and
// Keep and Change go with it. The open form and its words come back with the body when the
// reader comes back to the row, rather than being dropped with no word said.
test("a Change the reader typed in comes back with its decision's body", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const root = node({
    children: [
      node({ id: "decisions#b-d1", reference: decision.ref, type: "decision", waiting: true }),
      node({ id: "comment-1", text: "The next words.", type: "comment" }),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root, {
    artifacts: [decisionsDocument],
    decisions: { decisions: [decision], document: null, open_questions: [] },
  });
  const [row] = rowsFor(view.container, "decisions#b-d1");
  if (row === undefined) throw new Error("the decision has no row");
  await act(async () => row.focus());
  await view.findByRole("button", { name: "Change" });
  await user.keyboard("{Shift>}C{/Shift}");
  const field = await view.findByRole("textbox", { name: "Your change" });
  await waitFor(() => expect(document.activeElement).toBe(field));
  await user.keyboard("Say more about the flag.");
  // Out of the field by the keyboard alone: Tab to the form's Send change, Escape to the row.
  await user.keyboard("{Tab}{Escape}");
  expect(document.activeElement).toBe(row);

  // The next row takes the focus and the decision's body closes; the row above opens it again.
  await user.keyboard("j");
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  expect(view.queryByRole("textbox", { name: "Your change" })).toBeNull();
  await user.keyboard("k");
  await waitFor(() => expect(focusedRow(view.container)).toBe("decisions#b-d1"));
  const back = await view.findByRole("textbox", { name: "Your change" });
  expect((back as HTMLTextAreaElement).value).toBe("Say more about the flag.");
});

// R7 under a press: a press focuses what it lands on as it goes down, and the row taking the
// focus opens its body and draws its acts. Drawn while the press is held, that grows the row
// under the pointer and lets Chromium's scroll anchoring move the page with it, and the press
// ends on something else: part G's picture in a comment, pressed near the foot of the page, rose
// 46 px before the mouseup and never opened (c-row-layout, `issue-page-g.e2e.ts`). happy-dom lays
// nothing out and anchors nothing, so the browser row reads the picture; this reads the rule
// that keeps the page still: the tree draws nothing new until the press is over, the body the
// focus left included, and then the pressed row opens.
test("a press on a row draws nothing new until it ends, and then the row opens", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const root = node({
    children: [
      addressedComment("comment-1", "The first words."),
      addressedComment("comment-2", "The words pressed on."),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root);
  // No question waits, so the page opens on the first row, its body open.
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  const [first] = rowsFor(view.container, "comment-1");
  const [pressed] = rowsFor(view.container, "comment-2");
  const words = pressed?.querySelector<HTMLElement>("[data-node-words]");
  if (first === undefined || pressed === undefined || words == null) {
    throw new Error("the rows are not drawn");
  }
  await waitFor(() => expect(first.querySelector("[data-node-body]")).not.toBeNull());

  await user.pointer({ keys: "[MouseLeft>]", target: words });
  expect(focusedRow(view.container), "the press did not focus the row it went down on").toBe(
    "comment-2"
  );
  expect(pressed.querySelector("[data-node-body]"), "the held press opened a body").toBeNull();
  expect(pressed.querySelector("[data-row-acts]"), "the held press drew the row's acts").toBeNull();
  expect(first.querySelector("[data-node-body]"), "the held press closed a body").not.toBeNull();

  await user.pointer({ keys: "[/MouseLeft]" });
  await waitFor(() => expect(pressed.querySelector("[data-node-body]")).not.toBeNull());
  expect(pressed.querySelector("[data-row-acts]")).not.toBeNull();
  expect(first.querySelector("[data-node-body]")).toBeNull();
});

// The architect's decision 105 on R7, as amended: a press on a control outside the outline - a
// view control, a control of Details, one under the outline - focuses that control as it goes
// down, and the focused row's body closing then shortened the page under the pointer: the press
// ended on something else, and the control never fired. The body and the row's acts stay drawn
// while the press is held and through its click, and the row is let go one task after the press
// ends. happy-dom lays nothing out, so the browser row reads the presses firing (c-focus-opens,
// `a body opens on focus and nothing opens on hover`); this reads the rule that keeps the page
// still, and that lets the row go once nothing is under the pointer any more.
test("a press on a control outside the outline keeps the focused row's body through its click, and lets the row go once it ends", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const view = renderTree(
    node({
      children: [
        addressedComment("comment-1", "The first words."),
        addressedComment("comment-2", "The second words."),
      ],
      id: KEY,
      type: "issue",
    })
  );
  const outside = controlOutside();
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  const [first] = rowsFor(view.container, "comment-1");
  if (first === undefined) throw new Error("the first row is not drawn");
  await waitFor(() => expect(first.querySelector("[data-node-body]")).not.toBeNull());
  // Whether the row's body was drawn as each click of the control landed.
  const drawnAtClick: boolean[] = [];
  outside.control.addEventListener("click", () =>
    drawnAtClick.push(first.querySelector("[data-node-body]") !== null)
  );

  await user.pointer({ keys: "[MouseLeft>]", target: outside.control });
  expect(document.activeElement, "the press did not focus the control it went down on").toBe(
    outside.control
  );
  expect(
    first.querySelector("[data-node-body]"),
    "the press on a control outside the outline closed the focused row's body"
  ).not.toBeNull();
  expect(
    first.querySelector("[data-row-acts]"),
    "the press on a control outside the outline took the focused row's acts away"
  ).not.toBeNull();
  await user.pointer({ keys: "[/MouseLeft]" });
  expect(outside.presses(), "the press did not fire the control").toBe(1);
  expect(drawnAtClick, "the body closed before the press's click landed").toEqual([true]);
  // The panel goes back to the issue's own facts, as for any focus that leaves the outline for
  // anything but Details.
  expect(view.focusedNode()).toBeUndefined();

  // The press over, the row is let go: its body, its acts and its focused look.
  await waitFor(() =>
    expect(
      first.querySelector("[data-node-body]"),
      "the body stayed drawn after the press on a control outside the outline ended"
    ).toBeNull()
  );
  expect(first.querySelector("[data-row-acts]")).toBeNull();
  expect(first.querySelector("[data-node-card]")?.className).not.toContain(pageFocusedRow);
});

// Decision 105 as amended: Tab is no press, so the focus leaving the outline by it lets go of the
// row at once - its body, its focused look and what a key reported at it, which lasts until the
// next key (R9), and Tab is one (the reviewer's finding on e3136741, where both stayed drawn).
test("Tab out of the outline lets go of the row at once: no body, no focused look and no report", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const view = renderTree(
    node({
      children: [
        addressedComment("comment-1", "The first words."),
        addressedComment("comment-2", "The second words."),
      ],
      id: KEY,
      type: "issue",
    })
  );
  const outside = controlOutside();
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  const [first] = rowsFor(view.container, "comment-1");
  if (first === undefined) throw new Error("the first row is not drawn");
  await user.keyboard("w");
  await waitFor(() => expect(rowReport(view.container)).toBe(KEY_REPORTS.no_decision));
  expect(first.querySelector("[data-node-card]")?.className).toContain(pageFocusedRow);

  await user.tab();
  expect(document.activeElement).toBe(outside.control);
  await waitFor(() =>
    expect(rowReport(view.container), "Tab out of the outline left the key's report drawn").toBe(
      undefined
    )
  );
  expect(
    first.querySelector("[data-node-card]")?.className,
    "Tab out of the outline left the row it left looking focused"
  ).not.toContain(pageFocusedRow);
  expect(first.querySelector("[data-node-body]")).toBeNull();
  expect(first.querySelector("[data-row-acts]")).toBeNull();
});

// Decision 105's Escape: Escape anywhere on the page closes at once the body a press outside the
// outline keeps, with the press still held; and the outline's own Escape leaves the outline from
// its row with nothing open after the focus has been out to a control and back.
test("Escape closes the body a press outside the outline keeps, from the page or from the outline", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const view = renderTree(
    node({
      children: [
        addressedComment("comment-1", "The first words."),
        addressedComment("comment-2", "The second words."),
      ],
      id: KEY,
      type: "issue",
    })
  );
  const outside = controlOutside();
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  const [first] = rowsFor(view.container, "comment-1");
  if (first === undefined) throw new Error("the first row is not drawn");
  await waitFor(() => expect(first.querySelector("[data-node-body]")).not.toBeNull());

  await user.pointer({ keys: "[MouseLeft>]", target: outside.control });
  expect(document.activeElement).toBe(outside.control);
  expect(
    first.querySelector("[data-node-body]"),
    "the press held on a control outside the outline closed the focused row's body"
  ).not.toBeNull();
  await user.keyboard("{Escape}");
  await waitFor(() => expect(first.querySelector("[data-node-body]")).toBeNull());
  expect(first.querySelector("[data-row-acts]")).toBeNull();
  await user.pointer({ keys: "[/MouseLeft]" });

  // Back by Shift+Tab, to the row the reader left, whose body opens again; on it, the outline's
  // own Escape leaves the outline and closes the body.
  await user.tab({ shift: true });
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  await waitFor(() => expect(first.querySelector("[data-node-body]")).not.toBeNull());
  await user.keyboard("{Escape}");
  expect(document.activeElement).toBe(document.body);
  await waitFor(() => expect(first.querySelector("[data-node-body]")).toBeNull());
});

// Safari focuses no button a press lands on, so a press on a control outside the outline takes
// the focus from the row to no control at all, and the blur names no element. A blur naming no
// element that a press caused keeps the body through the press's click, as any press's does, and
// lets the row go one task after the press ends; a blur with no press - a script's, as the
// browser rows' `focusThePage` makes - lets it go at once.
test("a press outside the outline that leaves the focus on no control keeps the body through its click, and a blur with no press lets it go at once", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const view = renderTree(
    node({
      children: [
        addressedComment("comment-1", "The first words."),
        addressedComment("comment-2", "The second words."),
      ],
      id: KEY,
      type: "issue",
    })
  );
  // Something outside the outline that takes no focus, as a button does not in Safari.
  const words = "Words outside the outline";
  const plain = render(<p>{words}</p>).getByText(words);
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  const [first] = rowsFor(view.container, "comment-1");
  if (first === undefined) throw new Error("the first row is not drawn");
  await waitFor(() => expect(first.querySelector("[data-node-body]")).not.toBeNull());
  const blurredTo: (EventTarget | null)[] = [];
  first.addEventListener("focusout", (event) => blurredTo.push(event.relatedTarget));
  // Whether the row's body was drawn as each click on the words landed.
  const drawnAtClick: boolean[] = [];
  plain.addEventListener("click", () =>
    drawnAtClick.push(first.querySelector("[data-node-body]") !== null)
  );

  // A blur with no press closes the body.
  await act(async () => first.blur());
  expect(blurredTo).toEqual([null]);
  await waitFor(() => expect(first.querySelector("[data-node-body]")).toBeNull());

  // A press outside the tree, on what takes no focus, blurs the row to no element and keeps it
  // while it is held and through its click.
  await act(async () => first.focus());
  await waitFor(() => expect(first.querySelector("[data-node-body]")).not.toBeNull());
  await user.pointer({ keys: "[MouseLeft>]", target: plain });
  expect(blurredTo, "the press did not blur the row to no element").toEqual([null, null]);
  expect(document.activeElement).toBe(document.body);
  expect(
    first.querySelector("[data-node-body]"),
    "the press outside the outline that left the focus on no control closed the row's body"
  ).not.toBeNull();
  await user.pointer({ keys: "[/MouseLeft]" });
  expect(drawnAtClick, "the body closed before the press's click landed").toEqual([true]);
  await waitFor(() =>
    expect(
      first.querySelector("[data-node-body]"),
      "the body stayed drawn after the press that left the focus on no control ended"
    ).toBeNull()
  );
  expect(first.querySelector("[data-row-acts]")).toBeNull();
  expect(first.querySelector("[data-node-card]")?.className).not.toContain(pageFocusedRow);
});

// R9, WCAG 2.1.1, decision 69: every control of a row is out of the tab order, so `f` steps
// through the focused row's chips, then the links its words hold, then each control of the row
// that no key of the keymap presses - Change answer, Copy reference, a command's Copy - in the
// row's order, and the last stop comes back round to the first. Enter on the one it stopped on
// follows it or presses it.
test("f steps through a row's chips, links and then the controls no key presses", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const writeText = spyOn({ writeText: async (_text: string) => undefined }, "writeText");
  const original = navigator.clipboard;
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
  const reference = buildDispatchReference({ id: "ask-1", key: KEY, kind: "ask" });
  // A question the reader answered, so its row draws Change answer before its Reply, which `r`
  // presses, and Copy reference; its words link an issue, and it names a block of the spec.
  const root = node({
    children: [
      node({
        answer: {
          at: "2026-11-16T09:00:00Z",
          by: { id: "alice", kind: "user" },
          selected: [],
          text: "Behind a flag.",
        },
        author: { id: "bob", kind: "user" },
        chips: [
          {
            answered: true,
            gone: false,
            name: "The spec block",
            ref: `dispatch://${KEY}/spec#b-r1`,
          },
        ],
        id: "ask-1",
        question: true,
        reference,
        state: "answered",
        text: "Do we ship it as dispatch://CORE-7 says?",
        type: "ask",
      }),
    ],
    id: KEY,
    type: "issue",
  });
  /** Presses `f` and reads what it left the focus on. */
  const stop = async (): Promise<string | null | undefined> => {
    await user.keyboard("f");
    return document.activeElement?.textContent;
  };
  try {
    const { container } = renderPageTree(root, "alice").view;
    await waitFor(() => expect(focusedRow(container)).toBe("ask-1"));
    expect(await stop()).toBe("The spec block");
    expect(await stop()).toBe("CORE-7");
    expect(await stop()).toBe(CONTROL_LABELS.change_answer);
    expect(await stop()).toBe(CONTROL_LABELS.copy_reference);
    await user.keyboard("{Enter}");
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(reference));
    expect(await stop()).toBe("The spec block");
  } finally {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: original });
  }
});

test("Escape leaves the caret for the placement it was taken from, not the node's first row", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const spec: Artifact = { ...decisionsDocument, id: "artifact-spec", name: "Spec", slug: "spec" };
  const shared = { id: "spec#b-c1", text: "One check, under two requirements.", type: "check" };
  const root = node({
    children: [
      node({ children: [node(shared as never)], id: "spec#b-r1", type: "requirement" }),
      node({ children: [node(shared as never)], id: "spec#b-r2", type: "requirement" }),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root, { artifacts: [spec] });
  const placements = rowsFor(view.container, "spec#b-c1");
  expect(placements.length).toBe(2);
  const second = placements[1];
  if (second === undefined) throw new Error("the check has one placement only");

  await act(async () => second.focus());
  await user.keyboard("{Enter}");
  await waitFor(() => expect(view.edited.at(-1)).toBe("artifact-spec"));
  await user.keyboard("{Escape}");
  await waitFor(() => expect(view.edited.at(-1)).toBeUndefined());
  expect(document.activeElement).toBe(second);
});

// Decision 123, the tester's red row on c-keys (`the caret Enter puts in a statement deep in its
// document stays on screen while the reader writes`): the row the reader is editing in grows as
// they write, and put back at its top at each change of its size (`NodeRow.tsx`'s
// `useRowOnScreen`) it took the caret off the screen. The editor keeps its caret on screen, and
// the row places nothing of its own. happy-dom lays nothing out and observes no size, so the
// row's box is given, and each size observer still observing is called by hand once it grows.
test("the row the reader is editing in places nothing of its own as it grows", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const spec: Artifact = { ...decisionsDocument, id: "artifact-spec", name: "Spec", slug: "spec" };
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({ id: "spec#b-r1", text: "A statement of the spec.", type: "requirement" }),
    ],
    id: KEY,
    type: "issue",
  });
  // The statement's row 100 px down and 40 px tall, whole on the screen, until it grows to three
  // windows' height, its foot far below the window's.
  let grown = false;
  const boxes = spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement
  ) {
    return this.matches("[data-node-row]") && this.dataset.nodeId === "spec#b-r1"
      ? new DOMRect(0, 100, 800, grown ? 3 * window.innerHeight : 40)
      : new DOMRect();
  });
  const observing = new Set<{ readonly callback: ResizeObserverCallback; observer: unknown }>();
  const RealResizeObserver = globalThis.ResizeObserver;
  globalThis.ResizeObserver = class {
    readonly #held: { readonly callback: ResizeObserverCallback; observer: unknown };
    constructor(callback: ResizeObserverCallback) {
      this.#held = { callback, observer: this };
    }
    observe() {
      observing.add(this.#held);
    }
    unobserve() {}
    disconnect() {
      observing.delete(this.#held);
    }
  } as unknown as typeof ResizeObserver;
  const scrolled = spyOn(window, "scrollBy").mockImplementation(() => {});
  try {
    const view = renderTree(root, { artifacts: [spec] });
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
    await user.keyboard("j");
    await waitFor(() => expect(focusedRow(view.container)).toBe("spec#b-r1"));
    await user.keyboard("{Enter}");
    await waitFor(() => expect(view.edited.at(-1)).toBe("artifact-spec"));
    scrolled.mockClear();

    grown = true;
    await act(async () => {
      for (const { callback, observer } of observing) {
        callback([], observer as ResizeObserver);
      }
    });
    expect(
      scrolled,
      "the row being edited was put back on screen as it grew"
    ).not.toHaveBeenCalled();
  } finally {
    globalThis.ResizeObserver = RealResizeObserver;
    boxes.mockRestore();
    scrolled.mockRestore();
  }
});

test("q reaches every open question while the tree is read one node at a time", async () => {
  setViewport(1000);
  expect(window.matchMedia(ZOOMED_TREE_QUERY).matches).toBe(true);
  const user = userEvent.setup();
  const root = node({
    children: [
      node({
        children: [node({ id: "ask-1", question: true, text: "Which flag?", type: "ask" })],
        id: "spec",
        type: "document",
      }),
      node({
        children: [node({ id: "ask-2", question: true, text: "Who ships it?", type: "ask" })],
        id: "CORE-2",
        type: "child_issue",
      }),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root);

  await user.keyboard("q");
  await waitFor(() => expect(focusedRow(view.container)).toBe("ask-1"));
  await user.keyboard("q");
  await waitFor(() => expect(focusedRow(view.container)).toBe("ask-2"));
  expect(rowReport(view.container)).toBeUndefined();
});

/** An issue whose outline holds a comment and then the group of its two children. */
function childrenTree(): TreeNode {
  return node({
    children: [
      node({
        children: [node({ id: "comment-1a", text: "Under the first.", type: "comment" })],
        held: 1,
        id: "comment-1",
        text: "The first words.",
        type: "comment",
      }),
      node({ id: "CORE-2", text: "The first child", type: "child_issue" }),
      node({ id: "CORE-3", text: "The second child", type: "child_issue" }),
    ],
    id: KEY,
    type: "issue",
  });
}

// R6, R9: `t h` opens the children's own address and puts the focus on the group's first row
// each time it is pressed. The address alone moves the focus once per address, so a second
// `t h` after the reader had moved on moved nothing.
test("t h puts the focus on the first child's row, and again after the reader moved on", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const view = renderTree(childrenTree());
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));

  await user.keyboard("th");
  await waitFor(() => expect(focusedRow(view.container)).toBe("CORE-2"));
  expect(view.address()).toBe(buildIssuePath({ key: KEY, kind: "children" }));
  await user.keyboard("j");
  await waitFor(() => expect(focusedRow(view.container)).toBe("CORE-3"));
  await user.keyboard("th");
  await waitFor(() => expect(focusedRow(view.container)).toBe("CORE-2"));
});

// A zoomed tree is rooted at the node the reader zoomed into, which holds no child, so the
// children are no rows of it: `t h` leaves the zoom for the whole outline, as every key that
// takes the reader to a node does.
test("t h from a zoom into a node puts the focus on the first child's row", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const view = renderTree(childrenTree());
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  await user.keyboard(".");
  await waitFor(() => expect(view.container.querySelector(`[data-crumb="${KEY}"]`)).not.toBeNull());
  expect(rowsFor(view.container, "CORE-2")).toHaveLength(0);

  await user.keyboard("th");
  await waitFor(() => expect(focusedRow(view.container)).toBe("CORE-2"));
  expect(view.container.querySelector("[data-crumb]")).toBeNull();
});

// `t h` is offered only where the outline holds a child: on an issue with none it opens no
// address, moves no focus and draws no line.
test("t h on an issue with no children changes nothing", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({ id: "comment-2", text: "The second words.", type: "comment" }),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root);
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  const before = view.address();

  await user.keyboard("th");
  expect(view.address()).toBe(before);
  expect(focusedRow(view.container)).toBe("comment-1");
  expect(rowReport(view.container)).toBeUndefined();
});

// R6: `.` zooms into the focused node, and the key then takes the reader to that node so its
// row keeps the focus (`useTreeKeymap.ts`'s `tree-zoom-in`). Where the tree is read one node
// at a time, taking a reader to a node zooms to that node's parent, which undid the zoom the
// key had just made: `.` drew the rows it started from, and on the zoomed node's own row it
// walked back out one level instead.
test(". zooms into the focused node while the tree is read one node at a time", async () => {
  setViewport(1000);
  const user = userEvent.setup();
  const root = node({
    children: [
      node({
        children: [node({ id: "spec#b-r1", type: "requirement" })],
        id: "spec",
        type: "document",
      }),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root);
  const [row] = rowsFor(view.container, "spec");
  if (row === undefined) throw new Error("the document has no row");

  await act(async () => row.focus());
  await user.keyboard(".");
  // The tree is on the document now: its own row and what it holds, with the issue left
  // behind in the breadcrumb above the rows.
  await waitFor(() => expect(rowsFor(view.container, "spec#b-r1").length).toBe(1));
  expect(rowsFor(view.container, KEY).length).toBe(0);
  expect(view.getByRole("button", { name: KEY })).toBeTruthy();

  // On the zoomed node's own row - which is where the key left the focus - `.` holds the zoom
  // where it is rather than taking the reader back out of it.
  await user.keyboard(".");
  expect(rowsFor(view.container, "spec#b-r1").length).toBe(1);
  expect(rowsFor(view.container, KEY).length).toBe(0);
});

// R16: below 1024 px the outline draws one level under the node it is read from, and a deeper
// branch is one line saying how many nodes it holds, which opens them in place. Unfolding a
// branch on a phone draws that line under it, beside the rows above, and pressing the line
// draws the nodes it holds there - it does not carry the reader off to another root.
test("on a phone a branch's counted line opens its nodes in place", async () => {
  setViewport(390);
  const user = userEvent.setup();
  const turn = (id: string, children: TreeNode[] = []) =>
    node({
      children,
      held: children.reduce((total, child) => total + child.held + 1, 0),
      id,
      reference: buildDispatchReference({ id, key: KEY, kind: "comment" }),
      text: `Turn ${id}.`,
      type: "comment",
    });
  const root = node({
    children: [turn("head", [turn("first", [turn("second", [turn("third")])])])],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root);
  const head = rowsFor(view.container, "head")[0]?.querySelector<HTMLButtonElement>(
    'button[aria-label="Unfold"]'
  );
  if (head === null || head === undefined) throw new Error("the head has no Unfold");

  await user.click(head);
  // The head is open, and what it holds is one line counting its three nodes, under it.
  const line = await waitFor(() => {
    const found = view.container.querySelector<HTMLElement>("[data-branch-more]");
    if (found === null) throw new Error("no branch is one line counting its nodes");
    return found;
  });
  expect(line.textContent).toContain("3");
  expect(rowsFor(view.container, "head")).toHaveLength(1);
  expect(rowsFor(view.container, "first")).toHaveLength(0);

  const open = line.querySelector("button");
  if (open === null) throw new Error("the counted line opens nothing");
  await user.click(open);
  // Pressing the line draws what it holds in its place, under the head the reader was on: the
  // outline is still read from the issue, with no crumb above it.
  await waitFor(() => expect(rowsFor(view.container, "first")).toHaveLength(1));
  expect(rowsFor(view.container, "head")).toHaveLength(1);
  expect(view.container.querySelector("[data-crumb]")).toBeNull();
});

// R9: a row's key is the path from the issue down to it, and it holds the pin, the open body
// and the tree's one tab stop. The whole tree drawn from a zoomed node keyed its rows from
// that node, so a zoom re-keyed every row below it: a pin made before the zoom closed as the
// reader zoomed in, and one made while zoomed was lost on the way out.
test("a row keeps its key, and its pin, through a zoom in and back out", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const root = node({
    children: [
      node({
        children: [node({ id: "verification#b-c1", type: "check" })],
        id: "spec#b-r1",
        type: "requirement",
      }),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root);
  const [check] = rowsFor(view.container, "verification#b-c1");
  const [requirement] = rowsFor(view.container, "spec#b-r1");
  if (check === undefined || requirement === undefined) throw new Error("the tree drew no rows");
  const keyOfCheck = check.getAttribute("data-row-key");

  await act(async () => check.focus());
  await user.keyboard("o");
  await waitFor(() => expect(check.querySelector("[data-pinned]")).toBeTruthy());

  // Zoomed into the requirement, the check is the same row under the same key, so the body
  // the reader pinned open is still open.
  await act(async () => requirement.focus());
  await user.keyboard(".");
  await waitFor(() => expect(view.container.querySelector(`[data-crumb="${KEY}"]`)).not.toBeNull());
  const [zoomedCheck] = rowsFor(view.container, "verification#b-c1");
  expect(zoomedCheck?.getAttribute("data-row-key")).toBe(keyOfCheck);
  expect(zoomedCheck?.querySelector("[data-pinned]")).toBeTruthy();

  // A pin made while zoomed is on the same row once the reader is back out of the zoom.
  await user.keyboard("o");
  await waitFor(() =>
    expect(rowsFor(view.container, "spec#b-r1")[0]?.querySelector("[data-pinned]")).toBeTruthy()
  );
  await user.keyboard(",");
  await waitFor(() => expect(view.container.querySelector("[data-crumb]")).toBeNull());
  expect(rowsFor(view.container, "spec#b-r1")[0]?.querySelector("[data-pinned]")).toBeTruthy();
  expect(
    rowsFor(view.container, "verification#b-c1")[0]?.querySelector("[data-pinned]")
  ).toBeTruthy();
});

// The architect's ruling 5: the header is the issue, so the outline draws no row that repeats its
// key and title, and its first row is the first node under the issue. R7: the page opens with
// the focus on that row where no open question waits on the reader.
test("the outline draws no row of the issue, and opens with the focus on its first row", async () => {
  setViewport(1440);
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({ id: "ask-1", question: true, text: "Which flag?", type: "ask" }),
    ],
    id: KEY,
    text: `${KEY} The issue's own title`,
    type: "issue",
  });
  const view = renderTree(root);
  expect(rowsFor(view.container, KEY)).toHaveLength(0);
  const rows = [...view.container.querySelectorAll("[data-node-row]")];
  expect(rows.map((row) => row.getAttribute("data-node-id"))).toEqual(["comment-1", "ask-1"]);
  expect(rows.map((row) => row.getAttribute("aria-level"))).toEqual(["1", "1"]);
  // The open question waits on somebody else, so the first row is where the page opens.
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
});

// R7, and R12's panel: the focused node is named by its own first line, and a node with no words
// - a plan task whose block holds none - by the page's word for its kind, never by its id, which
// is no name a reader reads (part G's decision 66 on the same rule for a parent line).
test("a focused row whose node has no words names it by its kind, not its id", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words. And more.", type: "comment" }),
      node({ id: "plan#b-t1", text: "", type: "plan_task" }),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root);
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  expect(view.focusedNode()?.title).toBe("The first words.");

  await user.keyboard("j");
  await waitFor(() => expect(focusedRow(view.container)).toBe("plan#b-t1"));
  expect(view.focusedNode()?.id).toBe("plan#b-t1");
  expect(view.focusedNode()?.title).toBe("plan task");
});

// R7: the page opens with the focus on the first open question that waits on the reader, in the
// outline's own order, its path unfolded.
test("the page opens with the focus on the first open question that waits on the reader", async () => {
  setViewport(1440);
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({
        children: [
          node({ id: "ask-theirs", question: true, text: "Not yours?", type: "ask" }),
          node({ id: "ask-mine", question: true, text: "Yours?", type: "ask", waiting: true }),
        ],
        held: 2,
        id: "comment-2",
        text: "A thread.",
        type: "comment",
      }),
      node({ id: "ask-later", question: true, text: "Also yours?", type: "ask", waiting: true }),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root);
  await waitFor(() => expect(focusedRow(view.container)).toBe("ask-mine"));
});

// R8: the page's reads answer one after another, and a node whose parent's read has not answered
// yet sits under the issue until it does. A read that then draws the first row under its parent
// gives that row another key, and its old element leaves the page with the focus, which the
// browser drops to the body. The page opened with the focus on no row (the browser row `an open
// question is unfolded and on the first screen`, at 1440 by 900 and at 390 by 664): the opening
// read the body as the focus somebody else had placed. It takes it again, on the first row.
test("the page opens on its first row when a later read draws the row it focused under another key", async () => {
  setViewport(1440);
  const reply = node({ id: "comment-2", text: "A reply.", type: "comment" });
  const later = node({ id: "comment-9", text: "Later words.", type: "comment" });
  const tree = renderPageTree(node({ children: [reply, later], id: KEY, type: "issue" }));
  const { container } = tree.view;
  await waitFor(() => expect(focusedRow(container)).toBe("comment-2"));

  // The comments read places the reply under the comment it answers.
  tree.redraw(
    node({
      children: [
        node({
          children: [reply],
          held: 1,
          id: "comment-1",
          text: "The first words.",
          type: "comment",
        }),
        later,
      ],
      id: KEY,
      type: "issue",
    })
  );
  expect(rowsFor(container, "comment-1")).toHaveLength(1);
  await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));
});

// The opening takes the focus back only from a row that has left the page. A row still on it
// that lost the focus lost it to something else - here a script's blur, as the browser rows'
// `focusThePage` makes before they press Tab - and the focus stays where that left it, through
// the reads that answer after.
test("a focus taken off the opening's row while the row stays on the page ends the opening", async () => {
  setViewport(1440);
  const first = node({ id: "comment-1", text: "The first words.", type: "comment" });
  const tree = renderPageTree(node({ children: [first], id: KEY, type: "issue" }));
  const { container } = tree.view;
  await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));

  await act(async () => (document.activeElement as HTMLElement | null)?.blur());
  tree.redraw(
    node({
      children: [node({ id: "comment-0", text: "Older words.", type: "comment" }), first],
      id: KEY,
      type: "issue",
    })
  );
  expect(rowsFor(container, "comment-0")).toHaveLength(1);
  expect(document.activeElement).toBe(document.body);
});

// R19: an address that names a node puts the focus on it, and takes it back from the page itself
// - where a document mounting beside the tree drops it - until the reader's first act. After
// that the focus is the reader's: a second Escape or a press on blank space leaves it on the
// page, and no tree drawn after takes it back (the efficiency reviewer's finding on `/children`
// and `#b-` addresses, where it did on every render).
test("an address's node takes the focus back until the reader acts, and not after", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const named = node({ id: "comment-2", text: "The second words.", type: "comment" });
  const root = node({
    children: [node({ id: "comment-1", text: "The first words.", type: "comment" }), named],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root, { reveal: [root, named] });
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-2"));

  // Before the reader acts: the focus dropped to the page is taken back.
  await act(async () => (document.activeElement as HTMLElement | null)?.blur());
  view.redraw();
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-2"));

  // A key the reader presses - Shift alone, which moves nothing - and then the focus on the page.
  await user.keyboard("{Shift}");
  expect(focusedRow(view.container)).toBe("comment-2");
  await act(async () => (document.activeElement as HTMLElement | null)?.blur());
  view.redraw();
  expect(document.activeElement, "a tree drawn after the reader acted took the focus back").toBe(
    document.body
  );
});

// The tester's red row on c-outline (`an issue opened by a link opens with the focus on its
// question, or on its first row where none waits`): the app focuses its main element at each
// page change but the first (`app.tsx`), and that element holds the tree, whose reads have drawn
// no row yet. Read as somebody's focus, that ended the opening, and the page opened with the
// focus on no row. It is nobody's, as the page itself is nobody's, and the opening places the
// focus once the rows are drawn. The case's container stands for the app's main element.
test("the page opens on its question, or its first row, with the focus on what holds the tree", async () => {
  setViewport(1440);
  const first = node({ id: "comment-1", text: "The first words.", type: "comment" });
  const question = node({
    id: "ask-1",
    question: true,
    text: "Which flag?",
    type: "ask",
    waiting: true,
  });
  for (const [children, opensOn] of [
    [[first], "comment-1"],
    [[first, question], "ask-1"],
  ] as const) {
    const tree = renderPageTree(node({ id: KEY, type: "issue" }));
    const { container } = tree.view;
    container.tabIndex = -1;
    await act(async () => container.focus());
    expect(document.activeElement).toBe(container);
    tree.redraw(node({ children: [...children], id: KEY, type: "issue" }));
    await waitFor(() =>
      expect(
        focusedRow(container),
        `the page opened with the focus on no row, not on ${opensOn}, with the focus on what holds the tree`
      ).toBe(opensOn)
    );
    tree.view.unmount();
  }
});

// The same focus meets an address that names a node, opened by a link: the row the address took
// the focus to is given it back from what holds the tree, as from the page itself, until the
// reader acts.
test("an address's node takes the focus back from what holds the tree", async () => {
  setViewport(1440);
  const named = node({ id: "comment-2", text: "The second words.", type: "comment" });
  const root = node({
    children: [node({ id: "comment-1", text: "The first words.", type: "comment" }), named],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root, { reveal: [root, named] });
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-2"));

  view.container.tabIndex = -1;
  await act(async () => view.container.focus());
  view.redraw();
  await waitFor(() =>
    expect(
      focusedRow(view.container),
      "the address's node left the focus on what holds the tree"
    ).toBe("comment-2")
  );
});

// Decision 63, R8: where no question waits on the reader the page opens on the outline's first
// row, whose body opens with the focus (R7), and scrolls nothing - not even for a first row
// taller than the window, which the page's one rule (`identity-block.ts`'s `bringOnScreen`)
// would put at the top under the band, the page opening scrolled past its header (89 px at 1440
// by 900 on the real INFRA-1196). The reader's first move places the row it moves to as every
// move does.
test("the first row the page opens on scrolls nothing, and the first j places the next row", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({ id: "comment-2", text: "The second words.", type: "comment" }),
    ],
    id: KEY,
    type: "issue",
  });
  // happy-dom lays nothing out, so the rows' boxes are given: the first row 100 px down and
  // twice the window's height tall, the second under it, below the window's foot. Every other
  // box is empty.
  const tall = 2 * window.innerHeight;
  const second = { bottom: 100 + tall + 40, top: 100 + tall };
  const boxes = spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement
  ) {
    const id = this.matches("[data-node-row]") ? this.dataset.nodeId : undefined;
    if (id === "comment-1") return new DOMRect(0, 100, 800, tall);
    if (id === "comment-2") return new DOMRect(0, second.top, 800, second.bottom - second.top);
    return new DOMRect();
  });
  const scrolled = spyOn(window, "scrollBy").mockImplementation(() => {});
  try {
    const { container } = renderTree(root);
    await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));
    expect(scrolled, "the first row's focus at load scrolled the page").not.toHaveBeenCalled();

    await user.keyboard("j");
    await waitFor(() => expect(focusedRow(container)).toBe("comment-2"));
    expect(scrolled).toHaveBeenCalledTimes(1);
    // Scrolled by that much, the second row stands whole inside the window.
    const by = (scrolled.mock.calls[0]?.[0] as ScrollToOptions | undefined)?.top ?? 0;
    expect(second.top - by).toBeGreaterThanOrEqual(0);
    expect(second.bottom - by).toBeLessThanOrEqual(window.innerHeight);
  } finally {
    boxes.mockRestore();
    scrolled.mockRestore();
  }
});

// The tester's F16: what a key reports when it has nowhere to go is drawn at the row it was
// pressed on, and the reader's next move lets it go.
test("a key with nowhere to go says so at its row, until the next move", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({ id: "comment-2", text: "The second words.", type: "comment" }),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root);
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  await user.keyboard("w");
  await waitFor(() => expect(rowReport(view.container)).toBe(KEY_REPORTS.no_decision));
  const [first] = rowsFor(view.container, "comment-1");
  expect(first?.querySelector("[data-row-report]")).not.toBeNull();
  await user.keyboard("j");
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-2"));
  expect(rowReport(view.container)).toBeUndefined();
});

// The reviewer's finding on the Contents pane's asks: an ask is acted on once, whatever draws
// the tree again in between - a view drawn in its place and turned off mounts a new tree with
// the page's last ask still in hand (`outline-request.tsx`), and that ask must not move the
// reader a second time.
test("an ask of the Contents pane is acted on once, across the tree being drawn again", async () => {
  setViewport(1440);
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({ id: "comment-2", text: "The second words.", type: "comment" }),
    ],
    id: KEY,
    type: "issue",
  });
  const tree = renderPageTree(root);
  const { container } = tree.view;
  await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));
  await tree.ask("comment-2", false);
  await waitFor(() => expect(focusedRow(container)).toBe("comment-2"));
  await tree.show(false);
  await tree.show(true);
  await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));
});

// R14 of the root spec, decision 29: the Contents list asks the outline through the page's
// provider, and each call is a request of its own - to a node in the whole outline, or, with
// Focus branch on, to that node's branch alone.
test("a request sent with useAskOutline takes the outline to a node, or to its branch alone", async () => {
  setViewport(1440);
  const root = node({
    children: [
      node({
        children: [node({ id: "comment-1a", text: "Under the first.", type: "comment" })],
        held: 1,
        id: "comment-1",
        text: "The first words.",
        type: "comment",
      }),
      node({ id: "comment-2", text: "The second words.", type: "comment" }),
    ],
    id: KEY,
    type: "issue",
  });
  const tree = renderPageTree(root);
  const { container } = tree.view;
  await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));

  await tree.ask("comment-2", false);
  await waitFor(() => expect(focusedRow(container)).toBe("comment-2"));

  // Its branch alone: the outline is rooted at the node, its own row first, and the rest of
  // the issue is the breadcrumb above it.
  await tree.ask("comment-1", true);
  await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));
  expect(rowsFor(container, "comment-2")).toHaveLength(0);
  expect(container.querySelector(`[data-crumb="${KEY}"]`)).not.toBeNull();

  // And back to the whole outline.
  await tree.ask("comment-2", false);
  await waitFor(() => expect(focusedRow(container)).toBe("comment-2"));
  expect(container.querySelector("[data-crumb]")).toBeNull();
});

// Part J's planner's finding: Focus branch is the page's, held above the outline by
// `outline-request.tsx`'s provider, and it outlives a view drawn in the outline's place, where
// the outline's zoom did not: the outline drawn again as the view is turned off is a new one,
// and the request that zoomed the one before was acted on already, so it drew every branch while
// the toggle read on. Drawn again while Focus branch is on, the outline draws the last request's
// branch alone, as the outline before it did, and takes the reader nowhere: it is no new
// request, and the focus stays where the view left it. Focus branch turned off, as the Contents
// list turns it (part J's R2), draws the whole outline again.
test("an outline drawn again while Focus branch is on draws the last request's branch alone", async () => {
  setViewport(1440);
  const root = node({
    children: [
      node({
        children: [node({ id: "comment-1a", text: "Under the first.", type: "comment" })],
        held: 1,
        id: "comment-1",
        text: "The first words.",
        type: "comment",
      }),
      node({ id: "comment-2", text: "The second words.", type: "comment" }),
    ],
    id: KEY,
    type: "issue",
  });
  const tree = renderPageTree(root);
  const { container } = tree.view;
  await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));

  // The Contents list's toggle: Focus branch on, and the outline sent to the entry's branch.
  await tree.focusBranch(true);
  await tree.ask("comment-1", true);
  await waitFor(() => expect(rowsFor(container, "comment-2")).toHaveLength(0));
  const branch = [...container.querySelectorAll<HTMLElement>("[data-node-row]")].map(
    (row) => row.dataset.nodeId
  );

  // A view drawn in the outline's place, and turned off from a control outside the outline.
  const control = document.createElement("button");
  document.body.append(control);
  try {
    await tree.show(false);
    await act(async () => control.focus());
    await tree.show(true);
    expect(
      [...container.querySelectorAll<HTMLElement>("[data-node-row]")].map(
        (row) => row.dataset.nodeId
      )
    ).toEqual(branch);
    expect(rowsFor(container, "comment-2")).toHaveLength(0);
    expect(container.querySelector(`[data-crumb="${KEY}"]`)).not.toBeNull();
    expect(document.activeElement, "drawing the outline again moved the focus").toBe(control);

    await tree.focusBranch(false);
    await tree.ask("comment-1", false);
    await waitFor(() => expect(rowsFor(container, "comment-2")).toHaveLength(1));
    expect(container.querySelector("[data-crumb]")).toBeNull();
  } finally {
    control.remove();
  }
});

// The same outline drawn again where nothing holds the focus: the opening places it, but this is
// no page opening on what waits on the reader (R7), so it does not go to a question outside the
// branch, which would leave the zoom and draw every branch while the toggle read on. It puts the
// focus on the branch's first row, the node Focus branch is on, where it stands.
test("an outline drawn again on Focus branch's node opens on that node, not on a question outside it", async () => {
  setViewport(1440);
  const root = node({
    children: [
      node({
        children: [node({ id: "comment-1a", text: "Under the first.", type: "comment" })],
        held: 1,
        id: "comment-1",
        text: "The first words.",
        type: "comment",
      }),
      node({ id: "ask-1", question: true, text: "Which flag?", type: "ask", waiting: true }),
    ],
    id: KEY,
    type: "issue",
  });
  const tree = renderPageTree(root);
  const { container } = tree.view;
  // The page opens on the question that waits on the reader (R7).
  await waitFor(() => expect(focusedRow(container)).toBe("ask-1"));
  await tree.focusBranch(true);
  await tree.ask("comment-1", true);
  await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));
  expect(rowsFor(container, "ask-1")).toHaveLength(0);

  await tree.show(false);
  await act(async () => (document.activeElement as HTMLElement | null)?.blur());
  expect(document.activeElement).toBe(document.body);
  await tree.show(true);
  await waitFor(() =>
    expect(focusedRow(container), "the outline drawn again opened on another row").toBe("comment-1")
  );
  expect(
    [...container.querySelectorAll<HTMLElement>("[data-node-row]")].map((row) => row.dataset.nodeId)
  ).toEqual(["comment-1", "comment-1a"]);
  expect(rowsFor(container, "ask-1")).toHaveLength(0);
  expect(container.querySelector(`[data-crumb="${KEY}"]`)).not.toBeNull();
});

// The fold's half of the tester's F1: a reply the reader wrote under a folded branch is not left
// behind the fold. The writer's landing names what the write was made under; once the read that
// holds the new node draws it, the branch unfolds and the reader's new row takes the focus.
test("a write that lands under a folded branch unfolds it and gives the reader's new row the focus", async () => {
  setViewport(1440);
  const thread = (replies: TreeNode[]) =>
    node({
      children: [
        node({
          children: replies,
          held: replies.length,
          id: "comment-1",
          text: "A thread.",
          type: "comment",
        }),
        node({ id: "comment-9", text: "After the thread.", type: "comment" }),
      ],
      id: KEY,
      type: "issue",
    });
  const theirs = node({
    author: { id: "bob", kind: "user" },
    id: "comment-2",
    text: "Bob's reply.",
    type: "comment",
  });
  const mine = node({
    at: "2026-11-16T09:00:00Z",
    author: { id: "alice", kind: "user" },
    id: "comment-3",
    text: "My reply.",
    type: "comment",
  });
  const tree = renderPageTree(thread([theirs]), "alice");
  const { container } = tree.view;
  await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));
  // The thread is folded: nothing under it is open and unanswered.
  expect(rowsFor(container, "comment-2")).toHaveLength(0);

  await tree.land("comment-1");
  tree.redraw(thread([theirs, mine]));
  await waitFor(() => expect(focusedRow(container)).toBe("comment-3"));
  expect(rowsFor(container, "comment-2")).toHaveLength(1);
  expect(tree.writer()?.landing).toBeUndefined();
});

// The reviewer's finding on the landing: one the writer already holds as the tree mounts was
// asked for while no outline was drawn - under a view drawn in the tree's place, or before a
// reload - and the reader has been elsewhere since. Drawing the outline ends it and takes them
// nowhere: not to the row an answer or an edit was made on, which a landing reaches at once,
// and not to their newest node once the late fallback's wait is out.
test("a landing made while the outline was not drawn moves no focus when the outline is drawn", async () => {
  setViewport(1440);
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({
        children: [
          node({
            at: "2026-11-16T09:00:00Z",
            author: { id: "alice", kind: "user" },
            id: "comment-3",
            text: "My reply.",
            type: "comment",
          }),
        ],
        held: 1,
        id: "comment-2",
        text: "A thread.",
        type: "comment",
      }),
    ],
    id: KEY,
    type: "issue",
  });
  const tree = renderPageTree(root, "alice");
  const { container } = tree.view;
  await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));

  // An answer or an edit, which lands on the row it was made on.
  await tree.show(false);
  await tree.land("comment-2", "self");
  await tree.show(true);
  expect(focusedRow(container)).not.toBe("comment-2");
  expect(tree.writer()?.landing).toBeUndefined();
  await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));

  // A reply, which lands on the reader's newest node under what it answered: here the late
  // fallback's, since no read draws a new one.
  await tree.show(false);
  await tree.land("comment-2");
  jest.useFakeTimers();
  try {
    await tree.show(true);
    expect(tree.writer()?.landing).toBeUndefined();
    await jump(LANDING_WAIT_MS);
    expect(focusedRow(container)).toBe("comment-1");
    expect(rowsFor(container, "comment-3")).toHaveLength(0);
  } finally {
    realClock();
  }
});

// The late fallback takes the reader to their newest node where no read draws the new one, and
// only while the focus is still the page's to place: a key, a press, a wheel or a touch after
// the landing is the reader going somewhere of their own, and the fallback leaves them there.
test("a landing's late fallback leaves the focus where the reader moved it", async () => {
  setViewport(1440);
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({ id: "comment-2", text: "The second words.", type: "comment" }),
    ],
    id: KEY,
    type: "issue",
  });
  const tree = renderPageTree(root, "alice");
  const { container } = tree.view;
  await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));
  jest.useFakeTimers();
  try {
    const user = userOnFakeClock();
    // A reply under the first comment, whose node no read draws.
    await tree.land("comment-1");
    await user.keyboard("j");
    expect(focusedRow(container)).toBe("comment-2");
    await jump(LANDING_WAIT_MS);
    expect(focusedRow(container)).toBe("comment-2");
    expect(tree.writer()?.landing).toBeUndefined();
  } finally {
    realClock();
  }
});

// R9: `r` sets the line at the foot to reply to the focused row, and `a` sets it to answer the
// focused open question (`useTreeKeymap.ts`'s `tree-reply` and `tree-answer`). A comment takes
// no answer, so `a` on one leaves the line as it was and says nothing at the row.
test("r and a on the focused row set the page's writer", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const bob = { id: "bob", kind: "user" } as const;
  const root = node({
    children: [
      node({
        author: bob,
        id: "comment-1",
        reference: buildDispatchReference({ id: "comment-1", key: KEY, kind: "comment" }),
        text: "The first words.",
        type: "comment",
      }),
      node({
        author: bob,
        id: "ask-1",
        question: true,
        reference: buildDispatchReference({ id: "ask-1", key: KEY, kind: "ask" }),
        state: "open",
        text: "Which flag?",
        type: "ask",
        waiting: true,
      }),
    ],
    id: KEY,
    type: "issue",
  });
  const tree = renderPageTree(root, "alice");
  const { container } = tree.view;
  // The page opens on the question that waits on the reader (R7).
  await waitFor(() => expect(focusedRow(container)).toBe("ask-1"));

  await user.keyboard("k");
  await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));
  await user.keyboard("r");
  await waitFor(() =>
    expect(tree.writer()?.reply).toMatchObject({
      actor: bob,
      excerpt: "The first words.",
      id: "comment-1",
      parentKind: "comment",
      to: buildIssuePath({ id: "comment-1", key: KEY, kind: "comment" }),
    })
  );
  expect(tree.writer()?.answer).toBeUndefined();

  const reply = tree.writer()?.reply;
  await user.keyboard("a");
  expect(tree.writer()?.reply).toBe(reply);
  expect(tree.writer()?.answer).toBeUndefined();
  expect(rowReport(container)).toBeUndefined();

  await user.keyboard("j");
  await waitFor(() => expect(focusedRow(container)).toBe("ask-1"));
  await user.keyboard("a");
  await waitFor(() =>
    expect(tree.writer()?.answer).toEqual({
      askId: "ask-1",
      multiple: false,
      nodeId: "ask-1",
      selected: [],
    })
  );
  expect(tree.writer()?.reply).toBeNull();
});

// The architect's ruling 1: a crumb draws one line for a node, so it is R1's first line, cut at a
// word, never cut again mid-word by its box; the issue's crumb is its key.
test("a crumb is its node's first line cut at a word, and the issue's is its key", async () => {
  setViewport(1000);
  const user = userEvent.setup();
  const long =
    "A first sentence that runs on well past the hundred characters a first line may hold so it is cut at a word boundary";
  const root = node({
    children: [
      node({
        children: [node({ id: "comment-2", text: "Under it.", type: "comment" })],
        held: 1,
        id: "comment-1",
        text: long,
        type: "comment",
      }),
    ],
    id: KEY,
    text: `${KEY} The issue's own title`,
    type: "issue",
  });
  const view = renderTree(root);
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  await user.keyboard(".");
  await waitFor(() => expect(rowsFor(view.container, "comment-2")).toHaveLength(1));
  await user.keyboard("j");
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-2"));
  await user.keyboard(".");
  const crumbs = await waitFor(() => {
    const found = [...view.container.querySelectorAll<HTMLElement>("[data-crumb]")];
    if (found.length < 2) throw new Error("the breadcrumb holds no crumb of the comment");
    return found;
  });
  expect(crumbs[0]?.textContent).toBe(KEY);
  const crumb = crumbs[1]?.textContent ?? "";
  expect(crumb.endsWith("…")).toBe(true);
  expect(long.startsWith(crumb.slice(0, -1))).toBe(true);
  expect(long.charAt(crumb.length - 1)).toBe(" ");
  expect(crumbs[1]?.className).not.toContain("truncate");
});

// The root spec's R7: no node is named by its id. A crumb of a node with no words of its own, a
// plan task drawn with no text, names it by its kind's word.
test("a crumb of a node with no words is its kind's word, not its id", async () => {
  setViewport(1000);
  const user = userEvent.setup();
  const root = node({
    children: [
      node({
        children: [node({ id: "comment-2", text: "Under it.", type: "comment" })],
        held: 1,
        id: "plan#b-t1",
        text: "",
        type: "plan_task",
      }),
    ],
    id: KEY,
    text: `${KEY} The issue's own title`,
    type: "issue",
  });
  const view = renderTree(root);
  await waitFor(() => expect(focusedRow(view.container)).toBe("plan#b-t1"));
  await user.keyboard(".");
  await waitFor(() => expect(rowsFor(view.container, "comment-2")).toHaveLength(1));
  await user.keyboard("j");
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-2"));
  await user.keyboard(".");
  const crumbs = await waitFor(() => {
    const found = [...view.container.querySelectorAll<HTMLElement>("[data-crumb]")];
    if (found.length < 2) throw new Error("the breadcrumb holds no crumb of the plan task");
    return found;
  });
  expect(crumbs[1]?.textContent).toBe(NODE_TYPE_NAMES.plan_task);
});

// Decision 95, with part F's 6e7522e2: the focused node's title in the panel and a crumb each
// draw one line for a node, cut from `lineWords`, so a comment whose words are only a bare
// address is named by that address's short form, as a link to it draws it, and never by the
// address as written (R22).
test("a node whose words are only a bare address is named by its short form, in the panel and in its crumb", async () => {
  setViewport(1000);
  const user = userEvent.setup();
  const address = `dispatch://${KEY}/spec#b-r1`;
  const short = addressesAsShortForms(address);
  expect(short, "the address has no short form to name the node by").not.toContain("dispatch://");
  const root = node({
    children: [
      node({
        children: [node({ id: "comment-2", text: "Under it.", type: "comment" })],
        held: 1,
        id: "comment-1",
        text: address,
        type: "comment",
      }),
    ],
    id: KEY,
    text: `${KEY} The issue's own title`,
    type: "issue",
  });
  const view = renderTree(root);
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  expect(view.focusedNode()?.title, "the panel names the node by its address as written").toBe(
    short
  );
  await user.keyboard(".");
  await waitFor(() => expect(rowsFor(view.container, "comment-2")).toHaveLength(1));
  await user.keyboard("j");
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-2"));
  await user.keyboard(".");
  const crumbs = await waitFor(() => {
    const found = [...view.container.querySelectorAll<HTMLElement>("[data-crumb]")];
    if (found.length < 2) throw new Error("the breadcrumb holds no crumb of the comment");
    return found;
  });
  expect(crumbs[1]?.textContent, "the crumb names the node by its address as written").toBe(short);
});

// The reviewer's finding on the outline's empty line: a tree with no node is also one whose reads
// are still out - the asks, the comments, the messages, the decisions, the coverage, the results
// and the documents' blocks each draw nodes once they answer, and only the children come with the
// issue - so the line that the issue holds nothing was drawn on every cold load of an issue with
// no children, and rows then drawn in its place. The body draws it only once those reads have
// answered, and nothing in its place before. Here every read has answered but the comments'.
test("the outline says the issue holds nothing only once the reads its nodes come from have answered", async () => {
  setViewport(1440);
  const issue: IssueDetails = { ...issueDetails(), artifacts: [] };
  const route = { key: KEY, kind: "issue" } as const;
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  client.setQueryData(["whoami"], { kind: "user", login: "alice" });
  client.setQueryData(issueCoverageQuery(KEY).queryKey, { requirements: [] } as never);
  client.setQueryData(issueResultsQuery(KEY).queryKey, { checks: [] } as never);
  client.setQueryData(["decisions", KEY], { decisions: [], document: null, open_questions: [] });
  client.setQueryData(["asks", KEY], []);
  client.setQueryData(["issue-messages", KEY], []);
  // The comments' read, held out until the case answers it.
  const held = Promise.withResolvers<never[]>();
  const comments = spyOn(api, "listComments").mockReturnValue(held.promise as never);
  const names = spyOn(api, "getReferenceTargets").mockResolvedValue({ targets: [] } as never);
  try {
    const view = render(
      <MemoryRouter>
        <QueryClientProvider client={client}>
          <KeymapProvider>
            <MarginProvider>
              <ProvidersSlot issue={issue} route={route}>
                <BodySlot issue={issue} route={route} />
              </ProvidersSlot>
            </MarginProvider>
          </KeymapProvider>
        </QueryClientProvider>
      </MemoryRouter>
    );
    await waitFor(() => expect(comments).toHaveBeenCalled());
    expect(
      view.container.textContent,
      "the outline said the issue holds nothing with its comments' read still out"
    ).not.toContain(EMPTY_STATE_LINES.conversation);

    await act(async () => held.resolve([]));
    await waitFor(() =>
      expect(
        view.container.textContent,
        "the outline does not say the issue holds nothing once every read has answered empty"
      ).toContain(EMPTY_STATE_LINES.conversation)
    );
  } finally {
    comments.mockRestore();
    names.mockRestore();
  }
});
