import { afterEach, expect, jest, setSystemTime, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, waitFor } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { type ReactNode, useEffect, useRef, useState } from "react";
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
import { DocumentStage, DocumentStages } from "./document-stage";
import { type FocusedNode, FocusedNodeProvider, useFocusedNode } from "./focused-node";
import { IssueTree, LANDING_WAIT_MS, ZOOMED_TREE_QUERY } from "./IssueTree";
import { CLEARANCE, FOOT_HEIGHT_PROPERTY, IDENTITY_HEIGHT_PROPERTY } from "./identity-block";
import type { TreeNode } from "./issue-tree";
import { EDITOR_BOX_ATTRIBUTE } from "./node-caret";
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
import { EditorBox } from "./StagedDocument";
import { BodySlot } from "./slots/body";
import { ProvidersSlot } from "./slots/providers";
import { addressesAsShortForms } from "./tree-drafts";

/**
 * What a reader does to the tree with the keyboard alone.
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

/** The page's one editor of the document a row of the tree is editing in, as `IssuePresetPanels`
 *  draws it for the row to take onto its stage, in the box it scrolls in there, with `editor`
 *  standing in for the document's own editor. `held.set` is how the tree's `onEditing` reaches
 *  it. */
function EditedDocument({
  editor,
  held,
}: {
  editor: ReactNode;
  held: { set?: (artifactId: string | undefined) => void };
}): ReactNode {
  const [artifactId, setArtifactId] = useState<string | undefined>(undefined);
  held.set = setArtifactId;
  return artifactId === undefined ? null : (
    <DocumentStage artifactId={artifactId}>
      <EditorBox>{editor}</EditorBox>
    </DocumentStage>
  );
}

/** The tree alone, under the page's keymap and the focused node it sets. `reveal` is the chain
 *  down to the node an address names, which the tree is then to put the focus on
 *  (`slots/body.tsx`); `editor` stands in for the editor of the document a row is editing in,
 *  which the page draws on that row's stage; `emptyLine` is what the tree says where the issue
 *  holds nothing, none where it is not given; `readsHeld` is whether the reads the tree is built
 *  from hold their answers, until `holdReads` draws the same tree with them held; `redraw` draws
 *  the same tree again, as a read answering does. */
function renderTree(
  root: TreeNode,
  {
    artifacts = [],
    decisions,
    editor,
    emptyLine,
    readsHeld = true,
    reveal,
  }: {
    artifacts?: Artifact[];
    decisions?: IssueDecisions;
    editor?: ReactNode;
    emptyLine?: string;
    readsHeld?: boolean;
    reveal?: readonly TreeNode[];
  } = {}
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  if (decisions !== undefined) client.setQueryData(["decisions", KEY], decisions);
  const edited: (string | undefined)[] = [];
  const reads = { held: readsHeld };
  const editedDocument: { set?: (artifactId: string | undefined) => void } = {};
  const seen: { path?: string } = {};
  const focused: { node?: FocusedNode } = {};
  const draw = () => (
    <MemoryRouter>
      <AddressProbe seen={seen} />
      <QueryClientProvider client={client}>
        <KeymapProvider>
          <MarginProvider>
            <FocusedNodeProvider>
              <DocumentStages>
                <IssueScope />
                <FocusedNodeProbe seen={focused} />
                <IssueTree
                  artifacts={new Map(artifacts.map((artifact) => [artifact.slug, artifact]))}
                  context={new Set()}
                  emptyLine={emptyLine}
                  filtering={false}
                  focusReveal={reveal !== undefined}
                  holdsEscape={false}
                  isClosed={false}
                  onEditing={(artifactId) => {
                    edited.push(artifactId);
                    editedDocument.set?.(artifactId);
                  }}
                  readsHeld={reads.held}
                  reveal={reveal}
                  root={root}
                  rootRow
                />
                {editor === undefined ? null : (
                  <EditedDocument editor={editor} held={editedDocument} />
                )}
              </DocumentStages>
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
    holdReads: () => {
      reads.held = true;
      view.rerender(draw());
    },
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

/** Every row drawn for `nodeId`, in the order the tree drew them: one per placement. */
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

/** Lets the next frame come, and React draw what it changed. */
async function nextFrame(): Promise<void> {
  const frame = Promise.withResolvers<void>();
  requestAnimationFrame(() => frame.resolve());
  await act(async () => frame.promise);
}

/** Holds every frame asked for from here on until `run` runs them - the ones named, or every one
 *  still asked for - as the next frame would; a frame cancelled first is not run. `asked` names
 *  the frames asked for and not run yet, and `restore` gives the frames back to happy-dom. */
function framesByHand(): {
  readonly asked: () => number[];
  readonly restore: () => void;
  readonly run: (frames?: readonly number[]) => Promise<void>;
} {
  const asked = new Map<number, FrameRequestCallback>();
  let last = 0;
  const request = spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
    last += 1;
    asked.set(last, callback);
    return last;
  });
  const cancel = spyOn(window, "cancelAnimationFrame").mockImplementation((frame) => {
    asked.delete(frame);
  });
  return {
    asked: () => [...asked.keys()],
    restore: () => {
      request.mockRestore();
      cancel.mockRestore();
    },
    run: (frames = [...asked.keys()]) =>
      act(async () => {
        for (const frame of frames) {
          const callback = asked.get(frame);
          asked.delete(frame);
          callback?.(performance.now());
        }
      }),
  };
}

/** Stands in for the browser's size observers, since happy-dom lays nothing out and observes no
 *  size: each observer still observing is held with what it observes, and `resized` calls by hand
 *  the ones observing `target`, or every one where it names none, as a change of size would.
 *  `restore` puts the real one back. */
function sizesByHand(): {
  readonly resized: (target?: Element) => Promise<void>;
  readonly restore: () => void;
} {
  const observing = new Map<
    object,
    { readonly callback: ResizeObserverCallback; readonly targets: Set<Element> }
  >();
  const RealResizeObserver = globalThis.ResizeObserver;
  globalThis.ResizeObserver = class {
    readonly #callback: ResizeObserverCallback;
    constructor(callback: ResizeObserverCallback) {
      this.#callback = callback;
    }
    observe(target: Element) {
      const held = observing.get(this) ?? { callback: this.#callback, targets: new Set() };
      held.targets.add(target);
      observing.set(this, held);
    }
    unobserve(target: Element) {
      observing.get(this)?.targets.delete(target);
    }
    disconnect() {
      observing.delete(this);
    }
  } as unknown as typeof ResizeObserver;
  return {
    resized: (target) =>
      act(async () => {
        for (const [observer, { callback, targets }] of observing) {
          if (target === undefined || targets.has(target)) callback([], observer as ResizeObserver);
        }
      }),
    restore: () => {
      globalThis.ResizeObserver = RealResizeObserver;
    },
  };
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
    // row included, so Tab enters the tree once and leaves it again.
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

test("Enter sends the change the reader typed, instead of putting a caret in the row", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const sent = spyOn(api, "createComment").mockResolvedValue({ id: "comment-1" } as never);
  const read = readDecisionsText();
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
    read.mockRestore();
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

/** Answers the decisions document's text, which a Change reads as it is sent to count its
 *  quote's places over (`DecisionActions.tsx`). */
function readDecisionsText() {
  return spyOn(api, "getArtifactText").mockResolvedValue({
    markdown: decision.text,
    token: "sha256:decisions-text",
    version: null,
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
  const read = readDecisionsText();
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
    read.mockRestore();
  }
});

// The outline holds `ChangeDraftsProvider` (`DecisionActions.tsx`) above its rows: a decision's
// body is drawn while its row has the focus, so a reader who opened Change, typed and moved on
// closes the body, and Keep and Change go with it. The open form and its words come back with
// the body when the reader comes back to the row, rather than being dropped with no word said.
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

// A press focuses what it lands on as it goes down, and the row taking the
// focus opens its body and draws its acts. Drawn while the press is held, that grows the row
// under the pointer and lets Chromium's scroll anchoring move the page with it, and the press
// ends on something else: a picture in a comment, pressed near the foot of the page, rose 46 px
// before the mouseup and never opened (c-row-layout, `issue-page-g.e2e.ts`). happy-dom lays
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

// The architect's ruling on a press, as amended: a press on a control outside the outline - a
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

// Tab is no press, so the focus leaving the outline by it lets go of the row at once - its body,
// its focused look and what a key reported at it, which lasts until the next key, and Tab is one
// (the reviewer's finding on e3136741, where both stayed drawn).
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

// Escape anywhere on the page closes at once the body a press outside the outline keeps, with
// the press still held; and the outline's own Escape leaves the outline from its row with
// nothing open after the focus has been out to a control and back.
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

// WCAG 2.1.1: every control of a row is out of the tab order, so `f` steps through the focused
// row's chips, then the links its words hold, then each control of the row that no key of the
// keymap presses - Change answer, Copy reference, a command's Copy - in the row's order, and the
// last stop comes back round to the first. Enter on the one it stopped on follows it or presses
// it.
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

// Home on the first row moves the focus to no other row, and from a chip `f` stopped on, the row
// already holds the focus through the chip: the row's own placing, which runs as a row takes the
// focus, does not run. A reader who scrolled the row off the screen from there is brought back
// to it by the key all the same (`moveToRow`), as by a key that moves the focus to another row.
test("Home from a chip of the first row brings the row back on screen after the reader scrolled it off", async () => {
  setViewport(1440);
  const user = userEvent.setup();
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
        id: "comment-1",
        text: "The first words.",
        type: "comment",
      }),
      node({ id: "comment-2", text: "The second words.", type: "comment" }),
    ],
    id: KEY,
    type: "issue",
  });
  // happy-dom lays nothing out, so the first row's box is given once the reader has scrolled it
  // off: 400 px above the window's top. Every other box, and that one before, is empty.
  let scrolledOff = false;
  const off = { bottom: -360, top: -400 };
  const boxes = spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement
  ) {
    return scrolledOff && this.matches('[data-node-row][data-node-id="comment-1"]')
      ? new DOMRect(0, off.top, 800, off.bottom - off.top)
      : new DOMRect();
  });
  const scrolled = spyOn(window, "scrollBy").mockImplementation(() => {});
  try {
    const { container } = renderTree(root);
    await waitFor(() => expect(focusedRow(container)).toBe("comment-1"));
    await user.keyboard("f");
    expect(document.activeElement?.textContent).toBe("The spec block");
    expect(focusedRow(container)).toBe("comment-1");

    scrolledOff = true;
    await user.keyboard("{Home}");
    expect(document.activeElement).toBe(rowsFor(container, "comment-1")[0]);
    expect(scrolled, "Home left the row the reader scrolled off the screen").toHaveBeenCalledTimes(
      1
    );
    // Scrolled by that much, the row stands whole inside the window.
    const by = (scrolled.mock.calls[0]?.[0] as ScrollToOptions | undefined)?.top ?? 0;
    expect(off.top - by).toBeGreaterThanOrEqual(0);
    expect(off.bottom - by).toBeLessThanOrEqual(window.innerHeight);
  } finally {
    boxes.mockRestore();
    scrolled.mockRestore();
  }
});

test("Escape leaves the caret for the placement it was taken from, not the node's first row", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const spec: Artifact = { ...decisionsDocument, id: "artifact-spec", name: "Spec", slug: "spec" };
  // A check of the Verification document draws a state, and a requirement of the spec is a
  // statement (`isCheck`, `isStatement`): with neither, the outline folds the branch away.
  const shared = {
    id: "spec#b-c1",
    state: "no-result",
    text: "One check, under two requirements.",
    type: "check",
  };
  const root = node({
    children: [
      node({
        children: [node(shared as never)],
        id: "spec#b-r1",
        statement: true,
        type: "requirement",
      }),
      node({
        children: [node(shared as never)],
        id: "spec#b-r2",
        statement: true,
        type: "requirement",
      }),
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

// The tester's red row on c-keys (`the caret Enter puts in a statement deep in its document
// stays on screen while the reader writes`): the row the reader is editing in grows as they
// write, and put back at its top at each change of its size (`NodeRow.tsx`'s
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
  const sizes = sizesByHand();
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
    await sizes.resized();
    expect(
      scrolled,
      "the row being edited was put back on screen as it grew"
    ).not.toHaveBeenCalled();
  } finally {
    sizes.restore();
    boxes.mockRestore();
    scrolled.mockRestore();
  }
});

// The tester's red row on c-keys (`a row that edits a statement is no taller than the window,
// and the page around it stays put`): the editor a row takes holds the whole
// document, and drawn whole in the row it made the row as tall as the document. Its box reaches
// from where it stands down to the box at the foot of the window, and is scrolled - the box, not
// the page - to the node's block, with the caret there; a row so low that less than half the
// room is left under it gets half the room, and the page is scrolled just far enough to show it.
// The editor is made editable before it has read its document, and holds no block for the caret
// or the box until then, so nothing is placed until it has drawn the document's blocks.
// happy-dom lays nothing out, so the box, and the block 1,500 px down the document it scrolls,
// are given, under a band of 60 px at the top and over a box of 100 px at the foot.
test("the row's editor opens at the node's block, in a box that ends at the foot of the window", async () => {
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
  const documentHeight = 3000;
  let stands = 200;
  const boxes = spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement
  ) {
    const box = this.closest<HTMLElement>(`[${EDITOR_BOX_ATTRIBUTE}]`);
    if (box === null) return new DOMRect();
    if (box === this) {
      const cap = Number.parseFloat(box.style.maxHeight);
      return new DOMRect(0, stands, 800, Number.isNaN(cap) ? documentHeight : cap);
    }
    return this.dataset.blockId === "r1"
      ? new DOMRect(0, stands + 1500 - box.scrollTop, 800, 24)
      : new DOMRect();
  });
  // The second look after the drawing above the block settles watches the editor's size.
  const sizes = sizesByHand();
  const scrolled = spyOn(window, "scrollBy").mockImplementation(() => {});
  const published = document.documentElement.style;
  published.setProperty(IDENTITY_HEIGHT_PROPERTY, "60px");
  published.setProperty(FOOT_HEIGHT_PROPERTY, "100px");
  const floor = window.innerHeight - 100;
  const room = floor - 60;
  try {
    const view = renderTree(root, {
      artifacts: [spec],
      editor: <div contentEditable suppressContentEditableWarning />,
    });
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
    await user.keyboard("j");
    await waitFor(() => expect(focusedRow(view.container)).toBe("spec#b-r1"));
    // The row's editor, and then the document's text in it, once the editor has read it.
    const drawText = async (): Promise<void> => {
      let editable: HTMLElement | null | undefined;
      await waitFor(() => {
        editable = rowsFor(view.container, "spec#b-r1")[0]?.querySelector<HTMLElement>(
          '[contenteditable="true"]'
        );
        expect(editable, "the row took no editor").toBeTruthy();
      });
      if (editable === null || editable === undefined) throw new Error("the row took no editor");
      await nextFrame();
      expect(
        editable.closest<HTMLElement>(`[${EDITOR_BOX_ATTRIBUTE}]`)?.style.maxHeight,
        "the editor was opened before it had drawn the document's text"
      ).toBe("");
      await drawStatementBlocks(editable);
    };
    const editorBox = async (): Promise<HTMLElement> => {
      let box: HTMLElement | null | undefined;
      await waitFor(() => {
        box = rowsFor(view.container, "spec#b-r1")[0]?.querySelector<HTMLElement>(
          `[${EDITOR_BOX_ATTRIBUTE}]`
        );
        expect(box?.style.maxHeight, "the row's editor was not held to the window").not.toBe("");
      });
      if (box === null || box === undefined) throw new Error("the row holds no editor");
      return box;
    };

    scrolled.mockClear();
    await user.keyboard("{Enter}");
    await drawText();
    const box = await editorBox();
    expect(box.style.maxHeight, "the box does not end at the foot of the window").toBe(
      `${floor - stands}px`
    );
    expect(box.scrollTop, "the box was not scrolled to the statement's block").toBe(1500);
    expect(scrolled, "the page around the row moved").not.toHaveBeenCalled();
    expect(caretBlock(), "the caret is not in the statement's block").toBe("r1");

    // Low on the screen, with less than half the room under it.
    await user.keyboard("{Escape}");
    await waitFor(() => expect(view.edited.at(-1)).toBeUndefined());
    stands = floor - 40;
    await user.keyboard("{Enter}");
    await drawText();
    const low = await editorBox();
    const cap = Math.floor(room / 2);
    expect(low.style.maxHeight, "the box of a row low on the screen is not half the room").toBe(
      `${cap}px`
    );
    expect(scrolled, "the page did not bring the low row's box on screen").toHaveBeenCalledTimes(1);
    expect(
      (scrolled.mock.calls[0]?.[0] as ScrollToOptions | undefined)?.top ?? 0,
      "the page did not bring the low row's box over the foot"
    ).toBeGreaterThanOrEqual(stands + cap - floor);
  } finally {
    sizes.restore();
    boxes.mockRestore();
    scrolled.mockRestore();
    published.removeProperty(IDENTITY_HEIGHT_PROPERTY);
    published.removeProperty(FOOT_HEIGHT_PROPERTY);
  }
});

/** Blocks of the statement's document as its editor draws them, each by its `data-block-id` and
 *  its words: by default the words above the statement (`r0`), then the statement (`r1`). */
type StatementBlocks = readonly (readonly [id: string, words: string])[];

const STATEMENT_BLOCKS: StatementBlocks = [
  ["r0", "The words above the statement."],
  ["r1", "A statement of the spec."],
];

/** A comment and then a statement of the spec, with Enter pressed on the statement's row, once
 *  the row's editor - `editor`, a plain editable unless given - has drawn `blocks`, and Enter's
 *  caret is in the statement's block (`r1`). Answers the editor's editable. */
async function editStatement(
  user: UserEvent,
  editor: ReactNode = <div contentEditable suppressContentEditableWarning />,
  blocks: StatementBlocks = STATEMENT_BLOCKS
): Promise<HTMLElement> {
  const spec: Artifact = { ...decisionsDocument, id: "artifact-spec", name: "Spec", slug: "spec" };
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({ id: "spec#b-r1", text: "A statement of the spec.", type: "requirement" }),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root, { artifacts: [spec], editor });
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  await user.keyboard("j");
  await waitFor(() => expect(focusedRow(view.container)).toBe("spec#b-r1"));
  await user.keyboard("{Enter}");
  let found: HTMLElement | null | undefined;
  await waitFor(() => {
    found = rowsFor(view.container, "spec#b-r1")[0]?.querySelector<HTMLElement>(
      '[contenteditable="true"]'
    );
    expect(found, "the row took no editor").toBeTruthy();
  });
  const editable = found as HTMLElement;
  await drawStatementBlocks(editable, blocks);
  await waitFor(() =>
    expect(
      document.activeElement === editable && caretBlock() === "r1",
      "Enter did not put the caret in the statement"
    ).toBe(true)
  );
  return editable;
}

/** Draws `blocks` of the statement's document in the row's `editable`, as its editor does once it
 *  has read it. */
function drawStatementBlocks(
  editable: HTMLElement,
  blocks: StatementBlocks = STATEMENT_BLOCKS
): Promise<void> {
  return act(async () => {
    for (const [id, words] of blocks) {
      const block = document.createElement("p");
      block.dataset.blockId = id;
      block.textContent = words;
      editable.append(block);
    }
  });
}

/** The block of the editor the caret is in, by its `data-block-id`: the block of the text it is
 *  in, or the block it is in itself where that block holds no text. */
function caretBlock(): string | null | undefined {
  const anchor = window.getSelection()?.anchorNode;
  const element = anchor instanceof Element ? anchor : anchor?.parentElement;
  return element?.closest("[data-block-id]")?.getAttribute("data-block-id");
}

/**
 * Milkdown's view made again, as a change of the editor's plugins makes it: its wrapper put
 * back in the editable's place, and a new one around it. Chromium sends the editable a focusout
 * that names nothing as it leaves the page, drops the focus to the page's body, and moves the
 * caret out to where the editable stood, as the DOM moves every range in a node taken out of
 * it; happy-dom drops the focus alone, so the focusout and the caret's move are made here.
 */
function wrapAgain(editable: HTMLElement): Promise<void> {
  return act(async () => {
    const wrapper = document.createElement("div");
    editable.dispatchEvent(new FocusEvent("focusout", { bubbles: true, relatedTarget: null }));
    editable.parentElement?.replaceChild(wrapper, editable);
    window.getSelection()?.collapse(wrapper, 0);
    wrapper.append(editable);
  });
}

/** An editor that reads its own selection as ProseMirror does: from the document's
 *  `selectionchange`, while it holds the focus, and from nothing else. `own.block` is the block
 *  of that selection, which is what a key the editor handles itself acts on, and `own.offset`
 *  its anchor's offset. */
function ReportReadingEditor({
  own,
}: {
  own: { block?: string | null; offset?: number };
}): ReactNode {
  const editable = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const read = () => {
      if (document.activeElement !== editable.current) return;
      own.block = caretBlock();
      own.offset = window.getSelection()?.anchorOffset;
    };
    document.addEventListener("selectionchange", read);
    return () => document.removeEventListener("selectionchange", read);
  }, [own]);
  return <div contentEditable ref={editable} suppressContentEditableWarning />;
}

/** Drops the `selectionchange` happy-dom reports inside the call that moves the selection: a
 *  browser reports a move a task after it, so no listener hears it in the task that made it. A
 *  report dispatched apart from such a call is heard. Answers what restores happy-dom's own. */
function dropSelectionReports(): () => void {
  const selection = window.getSelection();
  if (selection === null) throw new Error("happy-dom draws no selection");
  let moving = false;
  const move = (change: () => void) => {
    moving = true;
    try {
      change();
    } finally {
      moving = false;
    }
  };
  const collapse = selection.collapse.bind(selection);
  const setBaseAndExtent = selection.setBaseAndExtent.bind(selection);
  const spies = [
    spyOn(selection, "collapse").mockImplementation((node, offset) =>
      move(() => collapse(node, offset))
    ),
    spyOn(selection, "setBaseAndExtent").mockImplementation(
      (anchor, anchorOffset, focus, focusOffset) =>
        move(() => setBaseAndExtent(anchor, anchorOffset, focus, focusOffset))
    ),
  ];
  const drop = (event: Event) => {
    if (moving) event.stopImmediatePropagation();
  };
  document.addEventListener("selectionchange", drop, { capture: true });
  return () => {
    document.removeEventListener("selectionchange", drop, { capture: true });
    for (const spy of spies) spy.mockRestore();
  };
}

// The editor reads a caret put in it only once the browser reports the move - ProseMirror's
// observer listens for the document's `selectionchange` - which a browser does a task later, and
// until then a key the editor handles itself acts on the editor's own selection: measured in
// Chromium on a busy page, the editor held the focus with its selection at the top of the
// document for 0.27 to 0.84 s after Enter, and ten Deletes pressed then removed the spec's first
// heading. The editor takes Enter's caret as its own in the task that puts it there. happy-dom
// reports a move inside the call that makes it, so that report is dropped here, as no listener
// hears a browser's in that task.
test("the editor takes Enter's caret in a statement as its own in the task that puts it there", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const restore = dropSelectionReports();
  const own: { block?: string | null } = {};
  try {
    await editStatement(user, <ReportReadingEditor own={own} />);
    expect(own.block, "the editor's own selection is not in the statement's block").toBe("r1");
  } finally {
    restore();
  }
});

// A statement whose block holds no text - an empty paragraph - has no text to put Enter's caret
// in, and an editor given the focus with no caret put in it takes the next key wherever the
// browser puts its own caret, outside the node's block. The caret goes in the block itself, which
// the editor reads as a place in that block, in the task that puts it there.
test("Enter on a statement whose block holds no text puts the caret in that block, as the editor's own", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const restore = dropSelectionReports();
  const own: { block?: string | null } = {};
  try {
    await editStatement(user, <ReportReadingEditor own={own} />, [
      ["r0", "The words above the statement."],
      ["r1", ""],
    ]);
    expect(own.block, "the editor's own selection is not in the empty statement's block").toBe(
      "r1"
    );
  } finally {
    restore();
  }
});

/** A comment, a statement of the spec and a second comment, with Enter pressed on the
 *  statement's row and the frame after it run, while the row's editor - a plain editable - has
 *  drawn none of the document yet. Answers the tree and that editable. */
async function enterBeforeDrawn(user: UserEvent) {
  const spec: Artifact = { ...decisionsDocument, id: "artifact-spec", name: "Spec", slug: "spec" };
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({ id: "spec#b-r1", text: "A statement of the spec.", type: "requirement" }),
      node({ id: "comment-2", text: "The last words.", type: "comment" }),
    ],
    id: KEY,
    type: "issue",
  });
  const view = renderTree(root, {
    artifacts: [spec],
    editor: <div contentEditable suppressContentEditableWarning />,
  });
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  await user.keyboard("j");
  await waitFor(() => expect(focusedRow(view.container)).toBe("spec#b-r1"));
  await user.keyboard("{Enter}");
  let found: HTMLElement | null | undefined;
  await waitFor(() => {
    found = rowsFor(view.container, "spec#b-r1")[0]?.querySelector<HTMLElement>(
      '[contenteditable="true"]'
    );
    expect(found, "the row took no editor").toBeTruthy();
  });
  await nextFrame();
  return { editable: found as HTMLElement, view };
}

// A key the reader presses after Enter and before the row's editor has drawn the document, which
// takes the focus out of the row - `j` to the next row - is them moving on. The caret Enter put
// in the statement once the editor had drawn it took the focus back from the row `j` moved to,
// and the letters they typed next went into the statement, which is saved to the shared
// document. The first look for the caret after that ends the editing instead, and the focus stays
// on the row `j` moved it to.
test("a key that takes the focus out of the row before its editor has drawn the document ends the editing, and the focus stays where the key put it", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const { editable, view } = await enterBeforeDrawn(user);
  await user.keyboard("j");
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-2"));
  // The editor draws the document a frame later.
  await nextFrame();
  await drawStatementBlocks(editable);
  await nextFrame();
  expect(focusedRow(view.container), "the caret took the focus back from the row j moved to").toBe(
    "comment-2"
  );
  expect(
    document.activeElement?.closest('[contenteditable="true"]') ?? null,
    "an editor holds the focus"
  ).toBeNull();
  expect(view.edited.at(-1), "the editing did not end").toBeUndefined();
  expect(
    rowsFor(view.container, "spec#b-r1")[0]?.querySelector('[contenteditable="true"]') ?? null,
    "the statement's row still holds the editor"
  ).toBeNull();
});

// A key that leaves the focus on the row - Delete pressed the moment Enter is, as the browser row
// on a slowed page presses it - is one for the statement: the caret goes into the node's block
// once the editor has drawn the document, all the same.
test("a key that leaves the focus on the row before its editor has drawn the document still has the caret put in the node's block", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const { editable, view } = await enterBeforeDrawn(user);
  await user.keyboard("{Delete}");
  expect(focusedRow(view.container), "Delete moved the focus off the row").toBe("spec#b-r1");
  // The editor draws the document a frame later.
  await nextFrame();
  await drawStatementBlocks(editable);
  await waitFor(() =>
    expect(
      document.activeElement === editable && caretBlock() === "r1",
      "a Delete pressed before the editor drew the document kept the caret out of the statement"
    ).toBe(true)
  );
  expect(view.edited.at(-1), "the editing ended").toBe("artifact-spec");
});

// The editor draws the document's blocks once it has read it, and may draw others before the
// node's. A caret put in it before it has drawn the node's block goes to the document's first
// text, and the keys the reader presses next edit the top of the document, which is saved to the
// shared document. No caret goes into the editor, and no focus, until it has drawn the node's
// block: the focus stays on the row, where no key edits the document, and the caret goes into
// the node's block once it is drawn.
test("Enter puts no caret in the row's editor until it has drawn the node's own block, and then puts it there", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const { editable, view } = await enterBeforeDrawn(user);
  const statement = rowsFor(view.container, "spec#b-r1")[0];
  // The editor draws the words above the statement first.
  await drawStatementBlocks(editable, [["r0", "The words above the statement."]]);
  await nextFrame();
  expect(
    document.activeElement,
    "the focus left the row before the editor drew the statement's block"
  ).toBe(statement);
  expect(
    editable.contains(window.getSelection()?.anchorNode ?? null),
    "a caret was put in the editor before it drew the statement's block"
  ).toBe(false);
  // And the statement a frame later.
  await drawStatementBlocks(editable, [["r1", "A statement of the spec."]]);
  await waitFor(() =>
    expect(
      document.activeElement === editable && caretBlock() === "r1",
      "the caret did not go into the statement once the editor drew its block"
    ).toBe(true)
  );
  expect(view.edited.at(-1), "the editing ended").toBe("artifact-spec");
});

// A press that takes the focus out of the outline before the row's editor has drawn the
// document ends the editing (above). The tree lets go of a row as the focus leaves it, except
// while that row is being edited, when the editor moving drops the focus, so the statement's row
// kept its focused look, its open body and its facts in the panel, with the focus on the control
// pressed. The editing ending with the focus out of the tree lets go of the row, as the focus
// leaving it does.
test("a press out of the outline before the row's editor has drawn the document lets go of the row as the editing ends", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const { editable, view } = await enterBeforeDrawn(user);
  const { control } = controlOutside();
  const statement = rowsFor(view.container, "spec#b-r1")[0];
  if (statement === undefined) throw new Error("the statement's row is not drawn");
  expect(
    statement.querySelector("[data-node-body]"),
    "the statement's row drew no body as it was edited"
  ).not.toBeNull();
  await user.click(control);
  expect(document.activeElement, "the press did not focus the control").toBe(control);
  // The editor draws the document a frame later.
  await nextFrame();
  await drawStatementBlocks(editable);
  await waitFor(() => expect(view.edited.at(-1), "the editing did not end").toBeUndefined());
  expect(document.activeElement, "the focus left the control pressed").toBe(control);
  await waitFor(() =>
    expect(
      statement.querySelector("[data-node-body]"),
      "the statement's row still draws its body"
    ).toBeNull()
  );
  expect(
    statement.querySelector("[data-node-card]")?.className,
    "the statement's row still looks focused"
  ).not.toContain(pageFocusedRow);
  expect(view.focusedNode(), "the panel still holds the statement's facts").toBeUndefined();
});

// The same where the editor never draws the document at all after the press: nothing more is
// drawn to look again, and the editing ended all the same, a task after the reader's act.
test("a press out of the outline ends the editing of a row whose editor never draws the node's block", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const { view } = await enterBeforeDrawn(user);
  const { control } = controlOutside();
  const statement = rowsFor(view.container, "spec#b-r1")[0];
  if (statement === undefined) throw new Error("the statement's row is not drawn");
  await user.click(control);
  expect(document.activeElement, "the press did not focus the control").toBe(control);
  await waitFor(() =>
    expect(view.edited.at(-1), "the editing went on with nothing drawn to end it").toBeUndefined()
  );
  expect(document.activeElement, "the focus left the control pressed").toBe(control);
  await waitFor(() =>
    expect(
      statement.querySelector("[data-node-body]"),
      "the statement's row still draws its body"
    ).toBeNull()
  );
});

// A press away from the row before its editor has drawn the node's block ends the editing once
// the press is over, a task after its click, as the tree lets go of a row a press left
// (`useReleaseFocus`). Until the caret is placed the editor's box stands in the row as tall as
// what the editor has drawn, and the editing ending a task after the press went down took that
// box out of the row with the pointer still down: the control below the row rose under the
// pointer, and the click landed on something else. happy-dom lays nothing out, so what is under
// the pointer as the press ends is given: the control while the row holds the editor's box, and
// the words below the control once the box has left the row.
test("a press on a control below the row before its editor has drawn the node's block lands on that control, and ends the editing once it is over", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const { editable, view } = await enterBeforeDrawn(user);
  const outside = controlOutside();
  const words = "Words below the control";
  const below = render(<p>{words}</p>).getByText(words);
  const statement = rowsFor(view.container, "spec#b-r1")[0];
  if (statement === undefined) throw new Error("the statement's row is not drawn");
  // The editor draws the words above the statement, and not the statement.
  await drawStatementBlocks(editable, [["r0", "The words above the statement."]]);
  await nextFrame();
  const underPointer = () =>
    statement.querySelector(`[${EDITOR_BOX_ATTRIBUTE}]`) === null ? below : outside.control;

  await user.pointer({ keys: "[MouseLeft>]", target: outside.control });
  expect(document.activeElement, "the press did not focus the control").toBe(outside.control);
  // The press is held over a few frames, as a reader's is.
  await nextFrame();
  await nextFrame();
  await user.pointer({ keys: "[/MouseLeft]", target: underPointer() });
  expect(
    outside.presses(),
    "the editing ended while the press was held, and its click landed below the control"
  ).toBe(1);

  await waitFor(() => expect(view.edited.at(-1), "the editing did not end").toBeUndefined());
  expect(document.activeElement, "the focus left the control pressed").toBe(outside.control);
  await waitFor(() =>
    expect(
      statement.querySelector("[data-node-body]"),
      "the statement's row still draws its body"
    ).toBeNull()
  );
});

// The row is looked at after each focus move off it, and not only after the reader's first act,
// until the caret is placed: a first act may leave the focus on the row - Shift, Delete - and a
// later one take it out of the outline before the editor has drawn the node's block. Looked at
// after the first act alone, the row of an editor that never draws that block kept the editing
// on, and the tree kept the row - its body, its focused look and its facts in the panel - with
// the focus on the control Tab moved it to. Tab from the row stops in its editor first, where
// the focus is still the row's, and the Tab after that leaves the outline.
test("a later act that takes the focus out of the outline ends the editing of a row whose editor never draws the node's block, and lets go of the row", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const { editable, view } = await enterBeforeDrawn(user);
  const { control } = controlOutside();
  const statement = rowsFor(view.container, "spec#b-r1")[0];
  if (statement === undefined) throw new Error("the statement's row is not drawn");
  await user.keyboard("{Shift}");
  expect(document.activeElement, "Shift moved the focus off the row").toBe(statement);
  await user.tab();
  expect(document.activeElement, "Tab from the row did not stop in its editor").toBe(editable);
  await user.tab();
  expect(document.activeElement, "Tab did not leave the outline").toBe(control);
  await waitFor(() => expect(view.edited.at(-1), "the editing went on").toBeUndefined());
  expect(document.activeElement, "the focus left the control Tab moved it to").toBe(control);
  await waitFor(() =>
    expect(
      statement.querySelector("[data-node-body]"),
      "the statement's row still draws its body"
    ).toBeNull()
  );
  expect(
    statement.querySelector("[data-node-card]")?.className,
    "the statement's row still looks focused"
  ).not.toContain(pageFocusedRow);
  expect(view.focusedNode(), "the panel still holds the statement's facts").toBeUndefined();
});

// A control inside the row is somewhere the reader moved to as well: `Shift+C` focuses a
// change's own box, and `f` steps to the row's links and controls. The caret placed once the
// editor drew took the focus from it, and the words written there went into the shared
// document. The case stands the row's own Keep open for such a control: a key of the reader's,
// then the focus on it.
test("a key that takes the focus to a control inside the row before its editor draws ends the editing there", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const { editable, view } = await enterBeforeDrawn(user);
  const statement = rowsFor(view.container, "spec#b-r1")[0];
  const inner = statement?.querySelector<HTMLElement>("button");
  if (inner === null || inner === undefined)
    throw new Error("the statement's row draws no control");
  await user.keyboard("{Shift}");
  await act(async () => inner.focus());
  expect(document.activeElement, "the control inside the row did not take the focus").toBe(inner);
  await nextFrame();
  await drawStatementBlocks(editable);
  await waitFor(() => expect(view.edited.at(-1), "the editing did not end").toBeUndefined());
  expect(document.activeElement, "the caret took the focus from the row's control").toBe(inner);
});

// The box the row's editor is drawn in is the row's own, unlike a control inside the row: a
// press of the reader's in it before the editor has drawn the node's block is no move off the
// row, and the caret goes into that block once it is drawn, with the editing on - a key may edit
// the node's own block alone.
test("a press inside the row's editor before it has drawn the node's block keeps the editing on, and the caret goes into that block once it is drawn", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const { editable, view } = await enterBeforeDrawn(user);
  await user.click(editable);
  expect(view.edited.at(-1), "the press inside the editor ended the editing").toBe("artifact-spec");
  expect(document.activeElement, "the press did not focus the editor").toBe(editable);
  // The editor draws the document a frame later.
  await nextFrame();
  await drawStatementBlocks(editable);
  await waitFor(() =>
    expect(
      document.activeElement === editable && caretBlock() === "r1",
      "the caret did not go into the statement once the editor drew its block"
    ).toBe(true)
  );
  expect(view.edited.at(-1), "the editing ended").toBe("artifact-spec");
});

// Only an act of the reader's ends the page's own placing of the caret. A focus a script gives a
// control out of the row before the row's editor has drawn the document is no reader moving on:
// the editing goes on, and the caret goes into the node's block once the editor has drawn it.
test("a focus a script moves out of the row before its editor has drawn the document leaves the editing on, and the caret goes into the node's block", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const { editable, view } = await enterBeforeDrawn(user);
  const { control } = controlOutside();
  await act(async () => control.focus());
  expect(document.activeElement, "the script did not focus the control").toBe(control);
  // The editor draws the document a frame later.
  await nextFrame();
  await drawStatementBlocks(editable);
  await waitFor(() =>
    expect(
      document.activeElement === editable && caretBlock() === "r1",
      "a focus a script moved before the editor drew the document kept the caret out of the statement"
    ).toBe(true)
  );
  expect(view.edited.at(-1), "the editing ended").toBe("artifact-spec");
});

// Two things the editor does to the caret Enter put in a node before the reader has done
// anything. It changes its plugins after it has drawn the document's text - Dispatch's block
// views as it binds, its collaborators' cursors once the room has synced - and ProseMirror then
// makes Milkdown's view again, which takes the editable out of the box it wraps it in and wraps
// it in a new one: the focus falls to the page's body as the editable leaves the page (measured
// in Chromium, 45 ms after Enter had put the caret in a statement). And it wrote its own
// selection back over a caret it had not read yet: measured in Chromium, the caret was in the
// spec's summary 60 ms after it was put in a statement, and the words typed went there. It reads
// the caret in the task that puts it there now (the test above), and a caret moved out of the
// block all the same goes back: until the reader acts the row puts the caret back at the start
// of the node's block; from their first act the caret is where the editor leaves it inside that
// block - a move out of it stops at the block's nearer end (below) - and a focus the editor
// drops comes back at that caret, not at the block's start (the next row).
test("a caret the editor drops or moves goes back to the node's block until the reader acts", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const editable = await editStatement(user);
  const caretInStatement = () => document.activeElement === editable && caretBlock() === "r1";
  // The editor's own selection, from before the caret went into the statement, written back,
  // and the browser reporting it.
  const selectAbove = () =>
    act(async () => {
      const words = editable.querySelector('[data-block-id="r0"]')?.firstChild;
      if (words === null || words === undefined) throw new Error("the editor draws no r0");
      window.getSelection()?.collapse(words, 0);
      document.dispatchEvent(new Event("selectionchange"));
    });

  await wrapAgain(editable);
  expect(caretInStatement(), "the caret the editor dropped was not put back").toBe(true);
  await selectAbove();
  expect(caretInStatement(), "the caret the editor moved was not put back").toBe(true);

  // The reader presses a key: the caret is where the editor leaves it inside the node's block
  // from then on, five characters into the statement, and not put back at the block's start.
  await user.keyboard("{Shift}");
  const statement = editable.querySelector('[data-block-id="r1"]')?.firstChild;
  if (statement === null || statement === undefined) throw new Error("the editor draws no r1");
  await act(async () => {
    window.getSelection()?.collapse(statement, 5);
    document.dispatchEvent(new Event("selectionchange"));
  });
  expect(
    window.getSelection()?.anchorOffset,
    "the caret was moved back after the reader acted"
  ).toBe(5);
  await wrapAgain(editable);
  await waitFor(() => expect(document.activeElement).toBe(editable));
  expect(
    window.getSelection()?.anchorOffset,
    "the caret was put back at the block's start after the reader acted"
  ).toBe(5);
});

// The editor goes on changing its plugins after the reader's first act - its collaborators'
// cursors go in once the room's mapping is ready, which it tries for up to 120 frames
// (`@legion/proof-editor`'s `lib.ts`) - and each change makes Milkdown's view again, which drops
// the focus to the page's body while the reader writes: the keys they press next went nowhere.
// A focus the editable loses to nothing, with no press of the reader's under it, comes back on
// the next frame at the caret it held. A press on blank space is the reader leaving the editor,
// and the focus stays where that press left it.
test("a focus the editor drops while the reader writes comes back at their caret, and a press elsewhere leaves it", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const editable = await editStatement(user);
  // Something outside the outline that takes no focus.
  const outside = "Words outside the outline";
  const plain = render(<p>{outside}</p>).getByText(outside);
  const statement = editable.querySelector('[data-block-id="r1"]')?.firstChild;
  if (statement === null || statement === undefined) throw new Error("the editor draws no r1");

  // The reader presses a key and moves the caret five characters into the statement.
  await user.keyboard("{Shift}");
  await act(async () => {
    window.getSelection()?.collapse(statement, 5);
    document.dispatchEvent(new Event("selectionchange"));
  });
  await wrapAgain(editable);
  await waitFor(() =>
    expect(document.activeElement, "the focus the editor dropped did not come back").toBe(editable)
  );
  expect(window.getSelection()?.anchorNode, "the caret came back elsewhere").toBe(statement);
  expect(window.getSelection()?.anchorOffset, "the caret came back elsewhere").toBe(5);

  // A press on blank space takes the focus off the editable to nothing, and it stays there.
  await user.pointer({ keys: "[MouseLeft>]", target: plain });
  expect(document.activeElement, "the press did not take the focus off the editor").toBe(
    document.body
  );
  await user.pointer({ keys: "[/MouseLeft]" });
  await nextFrame();
  expect(document.activeElement, "the focus a press took off the editor came back").toBe(
    document.body
  );
});

// The editor can redraw its text while the editable has no focus - the words the reader's caret
// was in drawn anew - and the caret's points are gone then. The focus came back with no caret put
// in the editor, and the next key went where the browser puts the caret in an editable it focuses
// (in Chromium, the editable's start, outside the node's block). It comes back at the start of
// the node's block instead, as the editor's own in the task that puts it there. The frames are
// held, so the words are drawn anew before the frame that gives the focus back.
test("a focus the editor drops after it redrew the words of the reader's caret comes back in the node's block, as the editor's own", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const restore = dropSelectionReports();
  const own: { block?: string | null; offset?: number } = {};
  try {
    const editable = await editStatement(user, <ReportReadingEditor own={own} />);
    const statement = editable.querySelector<HTMLElement>('[data-block-id="r1"]');
    const words = statement?.firstChild;
    if (statement === null || words === null || words === undefined) {
      throw new Error("the editor draws no r1");
    }
    // The reader presses a key and moves the caret five characters into the statement, which
    // the editor reads.
    await user.keyboard("{Shift}");
    await act(async () => {
      window.getSelection()?.collapse(words, 5);
      document.dispatchEvent(new Event("selectionchange"));
    });
    expect(own.offset, "the editor did not read the reader's move").toBe(5);
    const frames = framesByHand();
    try {
      await wrapAgain(editable);
      // The editor draws the statement's words anew while it has no focus.
      await act(async () => {
        statement.textContent = "A statement of the spec.";
      });
      await frames.run();
    } finally {
      frames.restore();
    }
    expect(document.activeElement, "the focus the editor dropped did not come back").toBe(editable);
    expect(selected(), "the caret did not come back at the start of the node's block").toEqual([
      "r1:0",
      "r1:0",
    ]);
    expect(
      [own.block, own.offset],
      "the editor's own selection is not at the start of the node's block"
    ).toEqual(["r1", 0]);
  } finally {
    restore();
  }
});

// Where the editor has redrawn the words of the reader's caret and taken the node's block out as
// well, a focus it drops has no place in the node's text to come back at. An editable given the
// focus with no caret put in it takes the next key where the browser puts its caret, and a caret
// put at the editor's start edits the top of the document, so the focus stays where it fell. The
// frames are held, so the editor redraws before the frame that would give the focus back.
test("a focus the editor drops after it redrew the reader's caret and took the node's block out stays where it fell", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const editable = await editStatement(user);
  const words = editable.querySelector('[data-block-id="r1"]')?.firstChild;
  if (words === null || words === undefined) throw new Error("the editor draws no r1");
  // The reader presses a key and moves the caret five characters into the statement.
  await user.keyboard("{Shift}");
  await act(async () => {
    window.getSelection()?.collapse(words, 5);
    document.dispatchEvent(new Event("selectionchange"));
  });
  const frames = framesByHand();
  try {
    await wrapAgain(editable);
    // The editor draws the statement's block no more while it has no focus.
    await act(async () => {
      editable.querySelector('[data-block-id="r1"]')?.remove();
    });
    await frames.run();
  } finally {
    frames.restore();
  }
  expect(
    document.activeElement,
    "the focus came back to an editor that no longer draws the node's block"
  ).toBe(document.body);
  expect(
    editable.contains(window.getSelection()?.anchorNode ?? null),
    "a caret was put in the editor outside the node's block"
  ).toBe(false);
});

// The caret the focus comes back at is the editor's own in the task that puts it there, as
// Enter's is (above): the editor writes its own selection back over a caret it has not read.
// Here the reader's last move is one the editor never read - the editable dropped the focus
// before the browser reported it - so a caret put back and not reported would leave the editor's
// own selection at the statement's start, under the reader's caret five characters into it.
test("the caret a dropped focus comes back at is the editor's own in the task that puts it there", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const restore = dropSelectionReports();
  const own: { block?: string | null; offset?: number } = {};
  try {
    const editable = await editStatement(user, <ReportReadingEditor own={own} />);
    const words = editable.querySelector('[data-block-id="r1"]')?.firstChild;
    if (words === null || words === undefined) throw new Error("the editor draws no r1");
    // The reader presses a key and moves the caret five characters into the statement, and the
    // focus drops before the browser reports the move.
    await user.keyboard("{Shift}");
    await act(async () => {
      window.getSelection()?.collapse(words, 5);
    });
    expect(own.offset, "the editor read a move the browser had not reported").toBe(0);
    await wrapAgain(editable);
    await waitFor(() =>
      expect(document.activeElement, "the focus the editor dropped did not come back").toBe(
        editable
      )
    );
    expect(selected(), "the caret came back elsewhere").toEqual(["r1:5", "r1:5"]);
    expect(own.offset, "the editor's own selection is not the caret the focus came back at").toBe(
      5
    );
  } finally {
    restore();
  }
});

/** The statement's document with words above the statement and words below it. */
const BLOCKS_AROUND: StatementBlocks = [
  ["r0", "The words above the statement."],
  ["r1", "A statement of the spec."],
  ["r2", "The words below the statement."],
];

/** The words of block `id` the row's `editable` draws. */
function wordsOf(editable: HTMLElement, id: string): Text {
  const words = editable.querySelector(`[data-block-id="${id}"]`)?.firstChild;
  if (!(words instanceof Text)) throw new Error(`the editor draws no ${id}`);
  return words;
}

/** A move of the selection from `anchor` to `focus`, the browser reporting it. */
function select(
  anchor: Node,
  anchorOffset: number,
  focus: Node = anchor,
  focusOffset: number = anchorOffset
): Promise<void> {
  return act(async () => {
    window.getSelection()?.setBaseAndExtent(anchor, anchorOffset, focus, focusOffset);
    document.dispatchEvent(new Event("selectionchange"));
  });
}

/** Where the selection's anchor and its focus stand, each as its block's id and its offset:
 *  `r1:5` is five characters into r1's words, and an end on an element rather than in words reads
 *  `r1(element):0`. A comparison that fails prints what it compared, and a happy-dom node printed
 *  is the whole page it is in, which ran the test process out of memory: so no node is compared,
 *  only where it stands. */
function selected(): [anchor: string, focus: string] {
  const selection = window.getSelection();
  const placeOf = (node: Node | null | undefined, offset: number | undefined) => {
    const element = node instanceof Element ? node : node?.parentElement;
    const block = element?.closest("[data-block-id]")?.getAttribute("data-block-id") ?? "-";
    return `${block}${node instanceof Text ? "" : "(element)"}:${offset}`;
  };
  return [
    placeOf(selection?.anchorNode, selection?.anchorOffset),
    placeOf(selection?.focusNode, selection?.focusOffset),
  ];
}

/** Presses `key` in the row's `editable`, and answers whether it is left to the editor and the
 *  browser: false where the row refused it, or made the move it names itself. */
function pressIn(editable: HTMLElement, key: string, modifiers: KeyboardEventInit = {}): boolean {
  return editable.dispatchEvent(
    new KeyboardEvent("keydown", { bubbles: true, cancelable: true, key, ...modifiers })
  );
}

/** Sends the row's `editable` the `beforeinput` a browser sends before it edits the page, with
 *  `ranges` as the ranges the edit replaces, and answers whether the edit is left to happen.
 *  happy-dom's input events carry no target ranges. */
function inputIn(editable: HTMLElement, inputType: string, ranges: readonly Range[] = []): boolean {
  const event = new InputEvent("beforeinput", { bubbles: true, cancelable: true, inputType });
  Object.defineProperty(event, "getTargetRanges", { value: () => ranges });
  return editable.dispatchEvent(event);
}

/** A range from `start` at `startOffset` to `end` at `endOffset`. */
function rangeOf(start: Node, startOffset: number, end: Node, endOffset: number): Range {
  const range = document.createRange();
  range.setStart(start, startOffset);
  range.setEnd(end, endOffset);
  return range;
}

// The row's editor is the document's whole editor, and Ctrl+Home and Ctrl+End moved its caret to
// the top and the foot of the whole document: measured on a preview, Ctrl+Home put it before the
// spec's `## Summary`, and a `Y` typed then made it `## YSummary`, out of the box above the row.
// They move the caret to the start and the end of the node's own block, and so do Meta's and a
// Mac's Meta+Up and Meta+Down, as the editor's own in the same task; with Shift held they extend
// the selection there. happy-dom reports a move inside the call that makes it, and that report is
// dropped, as no listener hears a browser's in that task.
test("Ctrl+Home and Ctrl+End in a row's editor stop at the node's block's start and end, as the editor's own", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const restore = dropSelectionReports();
  const own: { block?: string | null; offset?: number } = {};
  try {
    const editable = await editStatement(user, <ReportReadingEditor own={own} />, BLOCKS_AROUND);
    const statement = wordsOf(editable, "r1");
    const end = statement.length;
    await select(statement, 5);
    for (const [key, modifiers, offset] of [
      ["End", { ctrlKey: true }, end],
      ["Home", { ctrlKey: true }, 0],
      ["End", { metaKey: true }, end],
      ["Home", { metaKey: true }, 0],
      ["ArrowDown", { metaKey: true }, end],
      ["ArrowUp", { metaKey: true }, 0],
    ] as const) {
      const name = `${Object.keys(modifiers)[0]}+${key}`;
      expect(pressIn(editable, key, modifiers), `${name} was left to the editor`).toBe(false);
      expect(selected(), `${name} put the caret elsewhere`).toEqual([
        `r1:${offset}`,
        `r1:${offset}`,
      ]);
      expect([own.block, own.offset], `the editor's own selection is not ${name}'s`).toEqual([
        "r1",
        offset,
      ]);
    }
    await select(statement, 5);
    expect(pressIn(editable, "End", { ctrlKey: true, shiftKey: true })).toBe(false);
    expect(selected(), "Shift+Ctrl+End did not extend the selection to the block's end").toEqual([
      "r1:5",
      `r1:${end}`,
    ]);
    expect(pressIn(editable, "Home", { ctrlKey: true, shiftKey: true })).toBe(false);
    expect(selected(), "Shift+Ctrl+Home did not extend the selection to the block's start").toEqual(
      ["r1:5", "r1:0"]
    );
  } finally {
    restore();
  }
});

// Ctrl+A selected the whole document, and a letter typed then replaced it. It selects the node's
// block's text alone, and so does Meta+A, a Mac's.
test("Ctrl+A in a row's editor selects the node's block alone", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const editable = await editStatement(user, undefined, BLOCKS_AROUND);
  const statement = wordsOf(editable, "r1");
  for (const modifiers of [{ ctrlKey: true }, { metaKey: true }]) {
    await select(statement, 5);
    expect(pressIn(editable, "a", modifiers), "select-all was left to the editor").toBe(false);
    expect(selected(), "select-all selected other than the block's text").toEqual([
      "r1:0",
      `r1:${statement.length}`,
    ]);
  }
});

/** The statement's words laid out on two lines, as a browser draws a caret's box at each place
 *  in them: the first ten characters on the line from 100 to 120 px down, the rest on the line
 *  from 120 to 140 px. happy-dom lays nothing out. Answers what restores happy-dom's own. */
function statementOnTwoLines(statement: Text): () => void {
  const boxes = spyOn(Range.prototype, "getClientRects").mockImplementation(function (this: Range) {
    const top = this.startContainer === statement && this.startOffset > 10 ? 120 : 100;
    return [new DOMRect(0, top, 0, 20)] as unknown as DOMRectList;
  });
  return () => boxes.mockRestore();
}

// Up from the first line of the node's block and Down from its last, Left at its start and Right
// at its end took the row's caret into the blocks beside it - measured on a preview, Up after Left
// put it before a heading above the statement - and PageUp and PageDown a page away; Backspace at
// the block's start and Delete at its end join it to the block beside it. Each is refused there,
// and the caret stays where it was; inside the block each is the editor's. With Shift held, Up on
// the first line and Down on the last extend the selection to the block's end they stop at.
test("the keys that move past the node's block's edges, or join it to the block beside it, stop at those edges", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const editable = await editStatement(user, undefined, BLOCKS_AROUND);
  const statement = wordsOf(editable, "r1");
  const end = statement.length;
  const lines = statementOnTwoLines(statement);
  try {
    for (const [at, key, modifiers, refused] of [
      [0, "ArrowUp", {}, true],
      [5, "ArrowUp", {}, true],
      [15, "ArrowUp", {}, false],
      [15, "ArrowDown", {}, true],
      [5, "ArrowDown", {}, false],
      [0, "ArrowLeft", {}, true],
      [0, "ArrowLeft", { ctrlKey: true }, true],
      [5, "ArrowLeft", {}, false],
      [end, "ArrowRight", {}, true],
      [5, "ArrowRight", {}, false],
      [5, "PageUp", {}, true],
      [5, "PageDown", {}, true],
      [0, "Backspace", {}, true],
      [0, "Backspace", { ctrlKey: true }, true],
      [5, "Backspace", {}, false],
      [end, "Delete", {}, true],
      [5, "Delete", {}, false],
    ] as const) {
      await select(statement, at);
      const name = `${Object.keys(modifiers).join("+")} ${key} at ${at}`;
      expect(!pressIn(editable, key, modifiers), `${name} refused: ${!refused}`).toBe(refused);
      expect(selected(), `${name} moved the caret`).toEqual([`r1:${at}`, `r1:${at}`]);
    }
    await select(statement, 5);
    expect(pressIn(editable, "ArrowUp", { shiftKey: true })).toBe(false);
    expect(selected(), "Shift+Up on the first line did not extend to the block's start").toEqual([
      "r1:5",
      "r1:0",
    ]);
    await select(statement, 15);
    expect(pressIn(editable, "ArrowDown", { shiftKey: true })).toBe(false);
    expect(selected(), "Shift+Down on the last line did not extend to the block's end").toEqual([
      "r1:15",
      `r1:${end}`,
    ]);
  } finally {
    lines();
  }
});

// Enter splits the block the caret is in, and in a block that is its own paragraph - a
// decision's heading - the second half is a new block after the node's, with the caret in it. It
// is refused there. In a typed block, which holds its paragraphs and whose sides no split
// crosses, it is the editor's, and so is Shift+Enter, a line break inside the block.
test("Enter in a row's editor is refused where it would split the node's block in two", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const editable = await editStatement(user, undefined, BLOCKS_AROUND);
  await select(wordsOf(editable, "r1"), 5);
  expect(pressIn(editable, "Enter"), "Enter splitting the block was left to the editor").toBe(
    false
  );
  expect(pressIn(editable, "Enter", { shiftKey: true }), "Shift+Enter was refused").toBe(true);
  editable
    .querySelector('[data-block-id="r1"]')
    ?.setAttribute("data-proof-block-type", "requirement");
  expect(pressIn(editable, "Enter"), "Enter in a typed block was refused").toBe(true);
});

// A selection moved out of the node's block once the reader has acted - a press on another block
// of the editor, a drag across blocks, a platform's own key for a move - comes back to the
// block's nearer end, as the editor's own in the same task: an end above the block to its start,
// an end below it to its end, and an end inside it stays.
test("a selection moved out of the node's block comes back to the block's nearer end, as the editor's own", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const restore = dropSelectionReports();
  const own: { block?: string | null; offset?: number } = {};
  try {
    const editable = await editStatement(user, <ReportReadingEditor own={own} />, BLOCKS_AROUND);
    const above = wordsOf(editable, "r0");
    const statement = wordsOf(editable, "r1");
    const below = wordsOf(editable, "r2");
    const end = statement.length;
    await user.keyboard("{Shift}");
    await select(above, 5);
    expect(selected(), "a caret above the block did not come to its start").toEqual([
      "r1:0",
      "r1:0",
    ]);
    expect([own.block, own.offset], "the editor's own caret is not at the block's start").toEqual([
      "r1",
      0,
    ]);
    await select(below, 5);
    expect(selected(), "a caret below the block did not come to its end").toEqual([
      `r1:${end}`,
      `r1:${end}`,
    ]);
    expect([own.block, own.offset], "the editor's own caret is not at the block's end").toEqual([
      "r1",
      end,
    ]);
    await select(above, 3, below, 4);
    expect(selected(), "a selection across the block is not the block's text").toEqual([
      "r1:0",
      `r1:${end}`,
    ]);
    await select(statement, 5, below, 4);
    expect(selected(), "a selection from inside the block did not stop at its end").toEqual([
      "r1:5",
      `r1:${end}`,
    ]);
  } finally {
    restore();
  }
});

// Until the node's block is the selection - before the caret is placed in it, with the focus
// Tabbed into the editable before the editor has drawn that block - the editor's own selection is
// wherever it was, and a key the editor answers, or an edit the browser makes, lands there. No key
// edits the document then: every key but those that move the focus is refused, and every edit.
test("no key edits the document while the node's block of the row's editor is not the selection", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const { editable } = await enterBeforeDrawn(user);
  // The editor draws the words above the statement, and not the statement.
  await drawStatementBlocks(editable, [["r0", "The words above the statement."]]);
  await user.tab();
  expect(document.activeElement, "Tab from the row did not stop in its editor").toBe(editable);
  const above = wordsOf(editable, "r0");
  await select(above, 5);
  for (const key of ["x", " ", "Backspace", "Delete", "Enter", "ArrowUp", "Home"]) {
    expect(pressIn(editable, key), `${key} was left to the editor`).toBe(false);
  }
  expect(pressIn(editable, "a", { ctrlKey: true }), "Ctrl+A was left to the editor").toBe(false);
  expect(
    inputIn(editable, "insertText", [rangeOf(above, 5, above, 5)]),
    "a letter typed was left to the browser"
  ).toBe(false);
  expect(pressIn(editable, "Shift"), "Shift was refused").toBe(true);
  expect(pressIn(editable, "Tab"), "Tab was refused").toBe(true);
});

// The editor leaves typed text, and a key its keymap does not answer, to the browser, which edits
// the page and has the editor read the change after. An edit whose target ranges reach past the
// node's block - a platform's own delete at the block's edge - is refused before it is made, and
// one inside the block is left to the browser.
test("an edit the browser is about to make in the row's editor past the node's block is refused", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const editable = await editStatement(user, undefined, BLOCKS_AROUND);
  const above = wordsOf(editable, "r0");
  const statement = wordsOf(editable, "r1");
  await select(statement, 5);
  expect(
    inputIn(editable, "insertText", [rangeOf(statement, 5, statement, 5)]),
    "a letter typed in the statement was refused"
  ).toBe(true);
  expect(
    inputIn(editable, "deleteContentBackward", [rangeOf(above, above.length, statement, 0)]),
    "a delete reaching into the words above was left to the browser"
  ).toBe(false);
});

/** Pastes `text` into the row's `editable` as a browser sends a paste, and answers whether the
 *  paste is left to the editor. */
function pasteIn(editable: HTMLElement, text: string): boolean {
  const clipboardData = new DataTransfer();
  clipboardData.setData("text/plain", text);
  return editable.dispatchEvent(
    new ClipboardEvent("paste", { bubbles: true, cancelable: true, clipboardData })
  );
}

// The editor reads each line of a pasted text as a paragraph of its own, and in a block that
// holds no paragraphs - a paragraph, a decision's heading - those after the first go after the
// node's block, or into a heading as a line break: measured in Chromium, two lines pasted at a
// `## ` heading's start stored it as a heading of two lines. Such a paste is refused there. In a
// typed block, which holds the paragraphs a paste makes in it, it is the editor's, and so is a
// paste of one line anywhere in the block.
test("a paste of more than one line into the row's editor is refused where it would change more than the block's words", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const editable = await editStatement(user, undefined, BLOCKS_AROUND);
  await select(wordsOf(editable, "r1"), 5);
  expect(pasteIn(editable, "Two\nlines"), "a paste of two lines was left to the editor").toBe(
    false
  );
  expect(pasteIn(editable, "Two\r\nlines"), "a paste of two lines was left to the editor").toBe(
    false
  );
  expect(pasteIn(editable, "One line"), "a paste of one line was refused").toBe(true);
  editable
    .querySelector('[data-block-id="r1"]')
    ?.setAttribute("data-proof-block-type", "requirement");
  expect(pasteIn(editable, "Two\nlines"), "a paste into a typed block was refused").toBe(true);
});

// A drop edits the document where it lands, in whichever block that is, and text dragged out of
// the node's block is moved wherever the editor takes it. While a row is edited, a drag that
// starts in its editor and a drop into it are refused; a drag elsewhere on the page is no
// business of the row's.
test("a drag out of the row's editor and a drop into it are refused", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const { control } = controlOutside();
  const editable = await editStatement(user, undefined, BLOCKS_AROUND);
  const dragged = (target: Element, type: string) =>
    target.dispatchEvent(new Event(type, { bubbles: true, cancelable: true }));
  const statement = editable.querySelector('[data-block-id="r1"]');
  const above = editable.querySelector('[data-block-id="r0"]');
  if (statement === null || above === null) throw new Error("the editor draws no r0 or r1");
  expect(dragged(statement, "dragstart"), "a drag out of the editor was left to it").toBe(false);
  expect(dragged(above, "drop"), "a drop into the editor was left to it").toBe(false);
  expect(dragged(control, "dragstart"), "a drag outside the outline was refused").toBe(true);
});

// On a Mac the editor takes Ctrl+H for Backspace, and Ctrl+D and Alt+D for Delete
// (`prosemirror-commands`' `macBaseKeymap`), so each joined the node's block to the block beside
// it at the block's start and its end. On a Mac they keep Backspace's and Delete's rules; elsewhere they are no edit of
// the editor's, and the row leaves them alone.
test("on a Mac, Ctrl+H, Ctrl+D and Alt+D in a row's editor stop at the node's block's start and end", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const editable = await editStatement(user, undefined, BLOCKS_AROUND);
  const statement = wordsOf(editable, "r1");
  const end = statement.length;
  const pressAt = async (at: number, key: string) => {
    await select(statement, at);
    return !pressIn(editable, key, { ctrlKey: true });
  };
  Object.defineProperty(navigator, "platform", { configurable: true, value: "MacIntel" });
  try {
    expect(await pressAt(0, "h"), "Ctrl+H at the block's start was left to the editor").toBe(true);
    expect(await pressAt(5, "h"), "Ctrl+H inside the block was refused").toBe(false);
    expect(await pressAt(end, "d"), "Ctrl+D at the block's end was left to the editor").toBe(true);
    expect(await pressAt(5, "d"), "Ctrl+D inside the block was refused").toBe(false);
    // Alt+D types a letter of its own on a Mac, and the editor reads it by its code.
    await select(statement, end);
    expect(
      !pressIn(editable, "∂", { altKey: true, code: "KeyD" }),
      "Alt+D at the block's end was left to the editor"
    ).toBe(true);
  } finally {
    Reflect.deleteProperty(navigator, "platform");
  }
  expect(await pressAt(0, "h"), "Ctrl+H off a Mac was refused").toBe(false);
  expect(await pressAt(end, "d"), "Ctrl+D off a Mac was refused").toBe(false);
});

// Escape gives the row the focus at once, and again in the next frame, once the editor has left
// the row and dropped the focus to the page: the frame gives it back to the row. A key the reader
// presses before that frame - one that moves the focus, or a second Escape, which leaves the
// outline - a press on blank space, or a focus a script gives a control, is where the focus
// stays: the frame took the focus back to the row all the same. The frames are held, so each
// comes before the frame.
test("the frame after Escape gives the row back a focus the editor dropped, and leaves it where the reader or a script put it", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const { control } = controlOutside();
  // Something outside the outline that takes no focus.
  const outside = "Words outside the outline";
  const plain = render(<p>{outside}</p>).getByText(outside);
  const editable = await editStatement(user);
  const statement = editable.closest<HTMLElement>("[data-node-row]");
  const tree = statement?.closest<HTMLElement>('[role="tree"]');
  if (statement === null || tree === null || tree === undefined) {
    throw new Error("the editor is in no row of a tree");
  }
  // Enter on the statement's row, then Escape from its editor.
  const editThenEscape = async () => {
    await user.keyboard("{Enter}");
    await waitFor(() =>
      expect(
        statement.querySelector('[contenteditable="true"]'),
        "Enter put no editor in the row"
      ).toBeTruthy()
    );
    await user.keyboard("{Escape}");
    expect(document.activeElement, "Escape left the focus off the row").toBe(statement);
  };
  const frames = framesByHand();
  try {
    // A key: `k` moves the focus to the row above.
    await user.keyboard("{Escape}");
    expect(document.activeElement, "Escape left the focus off the row").toBe(statement);
    await user.keyboard("k");
    expect(focusedRow(tree)).toBe("comment-1");
    await frames.run();
    expect(
      focusedRow(tree),
      "the frame after Escape took the focus back from the row k moved it to"
    ).toBe("comment-1");

    // A script's focus on a control, with no key pressed.
    await user.keyboard("j");
    await editThenEscape();
    await act(async () => control.focus());
    await frames.run();
    expect(
      document.activeElement,
      "the frame after Escape took the focus back from the control a script focused"
    ).toBe(control);

    // A second Escape, which leaves the outline from the row: the focus is on the page.
    await act(async () => statement.focus());
    await editThenEscape();
    await user.keyboard("{Escape}");
    expect(document.activeElement, "the second Escape did not leave the outline").toBe(
      document.body
    );
    await frames.run();
    expect(
      document.activeElement,
      "the frame after Escape took the focus back from where a second Escape left it"
    ).toBe(document.body);

    // A press on blank space, which takes the focus to the page.
    await act(async () => statement.focus());
    await editThenEscape();
    await user.pointer({ keys: "[MouseLeft>]", target: plain });
    await user.pointer({ keys: "[/MouseLeft]" });
    expect(document.activeElement, "the press did not take the focus off the row").toBe(
      document.body
    );
    await frames.run();
    expect(
      document.activeElement,
      "the frame after Escape took the focus back from where a press on blank space left it"
    ).toBe(document.body);

    // The editor leaving the row drops the focus to the page, which is no act of the reader's.
    await act(async () => statement.focus());
    await editThenEscape();
    await act(async () => statement.blur());
    await frames.run();
    expect(
      document.activeElement,
      "the frame after Escape left the focus the editor dropped on the page"
    ).toBe(statement);
  } finally {
    frames.restore();
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

/** An issue of two comments, the first with a reply under it and the second with none. */
function twoCommentsTree(): TreeNode {
  return node({
    children: [
      node({
        children: [node({ id: "comment-1a", text: "Under the first.", type: "comment" })],
        id: "comment-1",
        text: "The first words.",
        type: "comment",
      }),
      node({ id: "comment-2", text: "The second words.", type: "comment" }),
    ],
    id: KEY,
    type: "issue",
  });
}

// At 390 by 664 on a real issue, a zoom into a row taller than the window left the crumb line it
// draws under the band that stays at the top: the line was placed, then the row was placed by
// the page's one rule, which puts a row taller than the room at its top just under the band, and
// again by the row's own as it grew. The line is placed, and the row, its first line on screen
// under it, is placed by nothing - not as it grows either - until the reader's next act, a key
// that moves no focus among them. The same holds for the row `,` takes the focus to from a row
// under the zoom's root: that root, which the zoom out a level draws under its line. happy-dom
// lays nothing out, so the boxes are given, moved by each scroll, under a band of 60 px; each
// size observer still observing is called by hand once the row grows.
test("a zoom into a row taller than the window places its crumb line, and the row keeps its top under it until the reader acts", async () => {
  setViewport(390);
  const user = userEvent.setup();
  const band = 60;
  let scrolledTo = 0;
  let tall = 2 * window.innerHeight;
  // The row given the tall box: the row `.` zooms into, then the row `,` takes the focus to.
  let tallRow = "comment-1";
  const boxes = spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement
  ) {
    if (this.matches("p") && this.querySelector("[data-crumb]") !== null) {
      return new DOMRect(0, 10 - scrolledTo, 390, 20);
    }
    const row = this.closest<HTMLElement>("[data-node-row]");
    if (row?.dataset.nodeId !== tallRow) return new DOMRect();
    return this === row
      ? new DOMRect(0, 40 - scrolledTo, 390, tall)
      : this.matches("[data-node-bullet]")
        ? new DOMRect(0, 42 - scrolledTo, 18, 24)
        : new DOMRect();
  });
  const scrolled = spyOn(window, "scrollBy").mockImplementation(((options: ScrollToOptions) => {
    scrolledTo += options.top ?? 0;
  }) as typeof window.scrollBy);
  const sizes = sizesByHand();
  const published = document.documentElement.style;
  published.setProperty(IDENTITY_HEIGHT_PROPERTY, `${band}px`);
  // Where the line and the row stand now: the line's top below the band, and the row's top on
  // screen, which is under the line, the row being drawn under it.
  const placed = () => ({
    line: 10 - scrolledTo >= band,
    row: 40 - scrolledTo >= band && 40 - scrolledTo < window.innerHeight,
  });
  // Two comments, the first with a reply, and a reply under that reply.
  const root = node({
    children: [
      node({
        children: [
          node({
            children: [node({ id: "comment-1b", text: "Under the reply.", type: "comment" })],
            id: "comment-1a",
            text: "Under the first.",
            type: "comment",
          }),
        ],
        id: "comment-1",
        text: "The first words.",
        type: "comment",
      }),
      node({ id: "comment-2", text: "The second words.", type: "comment" }),
    ],
    id: KEY,
    type: "issue",
  });
  try {
    const view = renderTree(root);
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
    await user.keyboard(".");
    await waitFor(() =>
      expect(view.container.querySelector(`[data-crumb="${KEY}"]`)).not.toBeNull()
    );
    await waitFor(() => expect(scrolled).toHaveBeenCalled());
    expect(placed(), "the zoom left the line under the band or the row off the screen").toEqual({
      line: true,
      row: true,
    });
    const after = scrolledTo;

    tall = 3 * window.innerHeight;
    await sizes.resized();
    expect(scrolledTo, "the zoomed row was put back at its top as it grew").toBe(after);

    // The reader presses a key that moves no focus: grown again, the row is placed by the page's
    // one rule, its top just under the band.
    await user.keyboard("{Shift}");
    tall = 4 * window.innerHeight;
    await sizes.resized();
    expect(
      40 - scrolledTo,
      "the zoomed row was not placed as it grew once the reader had acted"
    ).toBe(band + CLEARANCE);

    // `,` from the reply under the zoom's root zooms out to the first comment and takes the focus
    // to that root, the first reply.
    await user.keyboard("j");
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1a"));
    await user.keyboard(".");
    await waitFor(() =>
      expect(view.container.querySelector('[data-crumb="comment-1"]')).not.toBeNull()
    );
    await user.keyboard("j");
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1b"));
    tallRow = "comment-1a";
    tall = 2 * window.innerHeight;
    await user.keyboard(",");
    await waitFor(() =>
      expect(view.container.querySelector('[data-crumb="comment-1"]')).toBeNull()
    );
    expect(focusedRow(view.container)).toBe("comment-1a");
    expect(
      placed(),
      "the row , took the focus to placed itself, and left the line under the band"
    ).toEqual({ line: true, row: true });
    const out = scrolledTo;
    tall = 3 * window.innerHeight;
    await sizes.resized();
    expect(scrolledTo, "the row , took the focus to was put back at its top as it grew").toBe(out);
  } finally {
    sizes.restore();
    boxes.mockRestore();
    scrolled.mockRestore();
    published.removeProperty(IDENTITY_HEIGHT_PROPERTY);
  }
});

// The cycle key holds the focused row on screen while the bodies it draws above the row grow
// (`holdOnScreen`), and a focus `f` left on a chip of the row is the row's own: the hold ended as
// soon as the page grew with the focus on the chip rather than on the row itself, and the row was
// left where the growth pushed it. happy-dom lays nothing out, so the row's box is given, and the
// size observers watching the page are called by hand once the page grows.
test("z holds the focused row on screen as the page grows, with the focus on a chip of the row", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({
        chips: [
          {
            answered: true,
            gone: false,
            name: "The spec block",
            ref: `dispatch://${KEY}/spec#b-r1`,
          },
        ],
        id: "comment-2",
        text: "The second words.",
        type: "comment",
      }),
    ],
    id: KEY,
    type: "issue",
  });
  // The second row 300 px down and whole on screen, until the bodies drawn above it push it below
  // the window's foot.
  let top = 300;
  const boxes = spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement
  ) {
    return this.matches("[data-node-row]") && this.dataset.nodeId === "comment-2"
      ? new DOMRect(0, top, 800, 40)
      : new DOMRect();
  });
  const sizes = sizesByHand();
  const scrolled = spyOn(window, "scrollBy").mockImplementation(() => {});
  try {
    const view = renderTree(root);
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
    await user.keyboard("j");
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-2"));
    await user.keyboard("f");
    expect(document.activeElement?.textContent).toBe("The spec block");
    await user.keyboard("z");
    expect(document.activeElement?.textContent, "z took the focus off the chip").toBe(
      "The spec block"
    );
    scrolled.mockClear();

    // What the bodies drew above it put its 40 px row 400 px below the screen's foot.
    top = window.innerHeight + 400;
    await sizes.resized(document.body);
    // Its foot is brought 8 px clear of the screen's (`identity-block.ts`'s `bringOnScreen`).
    expect(
      scrolled,
      "the row the focus is in was not brought back on screen as the page grew"
    ).toHaveBeenCalledWith({ top: 448 });
  } finally {
    sizes.restore();
    boxes.mockRestore();
    scrolled.mockRestore();
  }
});

// At 390 by 664 Right on the row the tree is zoomed to - the view's own root, drawn open over
// what it holds - zoomed into the row it was on, which moved nothing and said nothing. It steps
// into the row as into any open row, and where nothing is under it says so at the row.
test("Right on the row the tree is zoomed to moves to its first child, or says nothing is under it", async () => {
  setViewport(390);
  const user = userEvent.setup();
  const view = renderTree(twoCommentsTree());
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  await user.keyboard(".");
  await waitFor(() => expect(view.container.querySelector(`[data-crumb="${KEY}"]`)).not.toBeNull());
  expect(focusedRow(view.container)).toBe("comment-1");
  await user.keyboard("{ArrowRight}");
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1a"));
  expect(rowReport(view.container)).toBeUndefined();

  await user.keyboard(",");
  await waitFor(() => expect(view.container.querySelector("[data-crumb]")).toBeNull());
  await user.keyboard("{End}");
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-2"));
  await user.keyboard(".");
  await waitFor(() => expect(view.container.querySelector(`[data-crumb="${KEY}"]`)).not.toBeNull());
  await user.keyboard("{ArrowRight}");
  await waitFor(() => expect(rowReport(view.container)).toBe(KEY_REPORTS.nothing_under));
  expect(focusedRow(view.container)).toBe("comment-2");
});

// At 390 by 664 Left on a child of the row the tree is zoomed to asked to zoom to that row,
// which is the zoom already: the focus stayed and nothing was said. It says so at the row - the
// issue itself where the tree is zoomed to it, the zoom's own row above where it is zoomed to a
// row - and on the zoom's own row Left zooms back out one level.
test("Left on a child of the row the tree is zoomed to says the tree is zoomed to its parent", async () => {
  setViewport(390);
  const user = userEvent.setup();
  const view = renderTree(twoCommentsTree());
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  await user.keyboard("{ArrowLeft}");
  await waitFor(() => expect(rowReport(view.container)).toBe(KEY_REPORTS.whole_tree));
  expect(focusedRow(view.container)).toBe("comment-1");

  await user.keyboard(".");
  await waitFor(() => expect(view.container.querySelector(`[data-crumb="${KEY}"]`)).not.toBeNull());
  await user.keyboard("{ArrowRight}");
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1a"));
  await user.keyboard("{ArrowLeft}");
  await waitFor(() => expect(rowReport(view.container)).toBe(KEY_REPORTS.parent_zoomed));
  expect(focusedRow(view.container)).toBe("comment-1a");
  expect(view.container.querySelector(`[data-crumb="${KEY}"]`)).not.toBeNull();

  await user.keyboard("{ArrowUp}");
  await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
  await user.keyboard("{ArrowLeft}");
  await waitFor(() => expect(view.container.querySelector("[data-crumb]")).toBeNull());
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

// `t h` opens the children's own address and puts the focus on the group's first row
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

// `.` zooms into the focused node, and the key then takes the reader to that node so its
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

// Below 1024 px the outline draws one level under the node it is read from, and a deeper
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

// A row's key is the path from the issue down to it, and it holds the pin, the open body
// and the tree's one tab stop. The whole tree drawn from a zoomed node keyed its rows from
// that node, so a zoom re-keyed every row below it: a pin made before the zoom closed as the
// reader zoomed in, and one made while zoomed was lost on the way out.
test("a row keeps its key, and its pin, through a zoom in and back out", async () => {
  setViewport(1440);
  const user = userEvent.setup();
  const root = node({
    children: [
      node({
        children: [node({ id: "verification#b-c1", state: "no-result", type: "check" })],
        id: "spec#b-r1",
        statement: true,
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

// The header is the issue, so the outline draws no row that repeats its key and title, and its
// first row is the first node under the issue; the page opens with the focus on that row where
// no open question waits on the reader.
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

// The focused node is named in the panel by its own first line, and a node with no words - a
// plan task whose block holds none - by the page's word for its kind, never by its id, which is
// no name a reader reads (the rule a parent line already follows).
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

// The page opens with the focus on the first open question that waits on the reader, in the
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

// The question the page opens on takes the focus while the outline's other reads are still out,
// and each that answers after draws rows above it, which push its row below the screen without
// changing the row's own size, so the row's own placing never sees it move: the page holds it on
// screen as the page grows, until the reader acts. happy-dom lays nothing out, so the row's box
// is given, and the size observers watching the page are called by hand.
test("the open question the page opens on is held on screen as the page grows, until the reader acts", async () => {
  setViewport(1440);
  const sizes = sizesByHand();
  const scrolled = spyOn(window, "scrollBy").mockImplementation(() => {});
  try {
    const root = node({
      children: [
        node({ id: "comment-1", text: "The first words.", type: "comment" }),
        node({ id: "ask-mine", question: true, text: "Yours?", type: "ask", waiting: true }),
      ],
      id: KEY,
      type: "issue",
    });
    const { container } = renderTree(root);
    await waitFor(() => expect(focusedRow(container)).toBe("ask-mine"));
    const [question] = rowsFor(container, "ask-mine");
    if (question === undefined) throw new Error("the tree draws no row for ask-mine");
    // What landed above it put its 28 px row 400 px below the screen's foot.
    spyOn(question, "getBoundingClientRect").mockReturnValue(
      new DOMRect(0, window.innerHeight + 400, 600, 28)
    );
    scrolled.mockClear();
    await sizes.resized(document.body);
    // Its foot is brought 8 px clear of the screen's (`identity-block.ts`'s `bringOnScreen`).
    expect(scrolled).toHaveBeenCalledWith({ top: 436 });

    // A wheel is the reader taking over: the page grows on and moves them no more.
    await act(async () => window.dispatchEvent(new Event("wheel")));
    scrolled.mockClear();
    await sizes.resized(document.body);
    expect(scrolled).not.toHaveBeenCalled();
  } finally {
    sizes.restore();
    scrolled.mockRestore();
  }
});

// The page's reads answer one after another, and a node whose parent's read has not answered
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

// The same where the read draws the row the opening focused again, under its new parent, and a
// row above it: the parent holds an open question that waits on somebody else, so its branch is
// drawn. The row's old element leaves the page with the focus, and the opening gives it to the
// outline's first row as it now is.
test("a focus a redraw drops while the page opens goes back to the outline's first row", async () => {
  setViewport(1440);
  const theirs = node({ id: "ask-theirs", question: true, text: "Not yours?", type: "ask" });
  const later = node({ id: "comment-9", text: "Later words.", type: "comment" });
  const tree = renderPageTree(node({ children: [theirs, later], id: KEY, type: "issue" }));
  const { container } = tree.view;
  await waitFor(() => expect(focusedRow(container)).toBe("ask-theirs"));
  const theirsKey = rowsFor(container, "ask-theirs")[0]?.dataset.rowKey;

  // A later read places the question under a node that has arrived since.
  tree.redraw(
    node({
      children: [
        node({
          children: [theirs],
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
  const drawnAgain = rowsFor(container, "ask-theirs").map((row) => row.dataset.rowKey);
  expect(drawnAgain).toHaveLength(1);
  expect(drawnAgain).not.toContain(theirsKey);
  await waitFor(() =>
    expect(focusedRow(container), "the row drawn again lost the focus").toBe("comment-1")
  );
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

// The same for the question the page opened on, which is where the page stays until the reader
// acts: a later read places the question under a node that has arrived since, which gives its
// row another key, and the browser drops the focus of the element that leaves the page. The
// opening gives the focus back to the question's new row. An answer arriving for the question
// then moves nothing: the question is open no more, so the thread the frontier opened for it
// alone folds, and its row leaves the page with the focus, which went to the page's body. The
// opening unfolds the thread again where the outline stands, and the focus stays on the
// question, not on the outline's first row or on the next question that waits on the reader.
// At 390 px, where the outline is read one node at a time, it went to the question as a key
// goes to a node, which zooms to the node's parent there: the answer drew the question's thread
// alone, under a crumb line.
test("the question the page opened on takes back a focus a redraw drops, and keeps it", async () => {
  const question = (open: boolean) =>
    node({ id: "ask-mine", question: open, text: "Yours?", type: "ask", waiting: open });
  const later = node({
    id: "ask-later",
    question: true,
    text: "Also yours?",
    type: "ask",
    waiting: true,
  });
  // A later read places the question under a node that has arrived since, whose thread the
  // frontier opens because the question in it is open.
  const placedUnder = (open: boolean) =>
    node({
      children: [
        node({
          children: [question(open)],
          held: 1,
          id: "comment-1",
          text: "The first words.",
          type: "comment",
        }),
        later,
      ],
      id: KEY,
      type: "issue",
    });
  for (const width of [1440, 390]) {
    setViewport(width);
    const tree = renderPageTree(
      node({ children: [question(true), later], id: KEY, type: "issue" })
    );
    const { container } = tree.view;
    await waitFor(() => expect(focusedRow(container), `at ${width} px`).toBe("ask-mine"));
    const questionKey = rowsFor(container, "ask-mine")[0]?.dataset.rowKey;

    tree.redraw(placedUnder(true));
    const drawnAgain = rowsFor(container, "ask-mine").map((row) => row.dataset.rowKey);
    expect(drawnAgain).toHaveLength(1);
    expect(drawnAgain).not.toContain(questionKey);
    await waitFor(() =>
      expect(
        focusedRow(container),
        `at ${width} px the question's row drawn again lost the focus`
      ).toBe("ask-mine")
    );

    // The answer arrives: the question is open no more, and waits on the reader no more.
    tree.redraw(placedUnder(false));
    await nextFrame();
    expect(
      focusedRow(container),
      `at ${width} px the answer moved the focus off the question`
    ).toBe("ask-mine");
    expect(
      container.querySelector("[data-crumb]"),
      `at ${width} px the answer zoomed the outline into the question's thread`
    ).toBeNull();
    tree.view.unmount();
  }
});

// From the answer on, the question's row the opening unfolded the path to again is the row the
// opening focused (`unfoldTo`), as the row its landing focused was before. It is held on screen
// as the page grows, as that row was until the answer took it out of the page, which ended its
// hold. A later read that takes it out of the page - here one that places the question's thread
// under a node that has arrived since, which nothing opens - has the opening unfold the path to
// the question again. Read as nobody's row, the focus the answer's unfolding gave the question
// ended the opening in the same render, and the later read left the focus on the page. happy-dom
// lays nothing out, so the row's box is given, and the size observers watching the page are
// called by hand.
test("the question the opening unfolded again after its answer keeps the focus through a later read", async () => {
  setViewport(1440);
  const sizes = sizesByHand();
  const scrolled = spyOn(window, "scrollBy").mockImplementation(() => {});
  try {
    const thread = (open: boolean) =>
      node({
        children: [
          node({ id: "ask-mine", question: open, text: "Yours?", type: "ask", waiting: open }),
        ],
        held: 1,
        id: "comment-1",
        text: "The first words.",
        type: "comment",
      });
    const tree = renderPageTree(node({ children: [thread(true)], id: KEY, type: "issue" }));
    const { container } = tree.view;
    await waitFor(() => expect(focusedRow(container)).toBe("ask-mine"));

    // The answer folds the thread the frontier alone opened, and the opening unfolds it again.
    tree.redraw(node({ children: [thread(false)], id: KEY, type: "issue" }));
    await nextFrame();
    expect(focusedRow(container), "the answer moved the focus off the question").toBe("ask-mine");
    const unfolded = rowsFor(container, "ask-mine")[0];
    if (unfolded === undefined) throw new Error("the answer's unfolding drew no row for ask-mine");
    // What landed above it put its 28 px row 400 px below the screen's foot.
    spyOn(unfolded, "getBoundingClientRect").mockReturnValue(
      new DOMRect(0, window.innerHeight + 400, 600, 28)
    );
    scrolled.mockClear();
    await sizes.resized(document.body);
    // Its foot is brought 8 px clear of the screen's (`identity-block.ts`'s `bringOnScreen`).
    expect(
      scrolled,
      "the question's row the answer unfolded again was not held on screen"
    ).toHaveBeenCalledWith({ top: 436 });

    tree.redraw(
      node({
        children: [
          node({
            children: [thread(false)],
            held: 2,
            id: "comment-0",
            text: "Older words.",
            type: "comment",
          }),
        ],
        id: KEY,
        type: "issue",
      })
    );
    await nextFrame();
    expect(
      rowsFor(container, "ask-mine"),
      "the later read left the question folded away: the answer's unfolding ended the opening"
    ).toHaveLength(1);
    expect(rowsFor(container, "ask-mine")[0], "the later read kept the question's row").not.toBe(
      unfolded
    );
    expect(
      focusedRow(container),
      "the later read left the focus off the question: the answer's unfolding ended the opening"
    ).toBe("ask-mine");
  } finally {
    sizes.restore();
    scrolled.mockRestore();
  }
});

// The opening unfolds the path to the question it opened on once for the row it focused there
// (`unfoldTo`). A question that leaves the tree - a read that answers holds it no more - leaves
// no row to go back to, and unfolding the path to it at each render drew the tree again from
// its own effect, without end: React reports such a loop as an error and goes on with it, so
// the report is thrown here, which ends it and fails the case.
test("a question the page opened on that leaves the tree is gone back to once, not at every render", async () => {
  setViewport(1440);
  const first = node({ id: "comment-1", text: "The first words.", type: "comment" });
  const question = node({
    id: "ask-1",
    question: true,
    text: "Which flag?",
    type: "ask",
    waiting: true,
  });
  const reported = console.error;
  const loop = spyOn(console, "error").mockImplementation((...said: unknown[]) => {
    if (String(said[0]).startsWith("Maximum update depth exceeded")) {
      throw new Error("the outline drew itself again from its own effect without end");
    }
    reported.apply(console, said);
  });
  try {
    const tree = renderPageTree(node({ children: [first, question], id: KEY, type: "issue" }));
    const { container } = tree.view;
    await waitFor(() => expect(focusedRow(container)).toBe("ask-1"));

    // The question leaves the tree, and the reads that answer after it draw the tree again.
    for (let read = 0; read < 3; read += 1) {
      tree.redraw(node({ children: [first], id: KEY, type: "issue" }));
      await nextFrame();
    }
    expect(rowsFor(container, "ask-1")).toHaveLength(0);
    expect(rowsFor(container, "comment-1")).toHaveLength(1);
  } finally {
    loop.mockRestore();
  }
});

// An address that names a node puts the focus on it, and takes it back from the page itself
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

// A read that answers before the frame an address's landing waits for draws the tree again, and
// the landing waits on for that same frame: asked again at each render, it was put off by a frame
// each time, for as long as reads kept drawing the tree.
test("a tree drawn again before an address's landing frame leaves the landing to that frame", async () => {
  setViewport(1440);
  const named = node({ id: "comment-2", text: "The second words.", type: "comment" });
  const root = node({
    children: [node({ id: "comment-1", text: "The first words.", type: "comment" }), named],
    id: KEY,
    type: "issue",
  });
  const frames = framesByHand();
  try {
    const view = renderTree(root, { reveal: [root, named] });
    // The frames the render that drew the address's row asked for, the landing's among them.
    const first = frames.asked();
    view.redraw();
    view.redraw();
    await frames.run(first);
    expect(
      focusedRow(view.container),
      "the address's row took no focus in the frame its landing first asked for"
    ).toBe("comment-2");
  } finally {
    frames.restore();
  }
});

// The same for the frames the page's opening asks: the first row's where no question waits on
// the reader, and the landing on the question where one does.
test("a tree drawn again before the opening's first-row frame leaves the focus to that frame", async () => {
  setViewport(1440);
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({ id: "comment-2", text: "The second words.", type: "comment" }),
    ],
    id: KEY,
    type: "issue",
  });
  const frames = framesByHand();
  try {
    const view = renderTree(root);
    // The frames the first render asked for, the first row's among them.
    const first = frames.asked();
    view.redraw();
    view.redraw();
    await frames.run(first);
    expect(
      focusedRow(view.container),
      "the first row took no focus in the frame the opening first asked for"
    ).toBe("comment-1");
  } finally {
    frames.restore();
  }
});

test("a tree drawn again before the landing on the question the page opens on leaves the landing to its frame", async () => {
  setViewport(1440);
  const root = node({
    children: [
      node({ id: "comment-1", text: "The first words.", type: "comment" }),
      node({ id: "ask-1", question: true, text: "Which flag?", type: "ask", waiting: true }),
    ],
    id: KEY,
    type: "issue",
  });
  const frames = framesByHand();
  try {
    const view = renderTree(root);
    // The frames the first render asked for, the question's landing among them.
    const first = frames.asked();
    view.redraw();
    view.redraw();
    await frames.run(first);
    expect(
      focusedRow(view.container),
      "the question took no focus in the frame its landing first asked for"
    ).toBe("ask-1");
  } finally {
    frames.restore();
  }
});

// The asks' read can answer after the first row was drawn and before the frame that is to give
// it the focus: the question takes the focus in the first row's place, and the first row's frame
// asks nothing of the opening, which goes on - here through a later read that draws the
// question's row again under another key.
test("a question whose read answers before the first row's frame takes the focus, and the opening goes on", async () => {
  setViewport(1440);
  const first = node({ id: "comment-1", text: "The first words.", type: "comment" });
  const question = node({
    id: "ask-1",
    question: true,
    text: "Which flag?",
    type: "ask",
    waiting: true,
  });
  const frames = framesByHand();
  try {
    const tree = renderPageTree(node({ children: [first], id: KEY, type: "issue" }));
    const { container } = tree.view;
    tree.redraw(node({ children: [first, question], id: KEY, type: "issue" }));
    await frames.run();
    expect(focusedRow(container), "the question whose read answered took no focus").toBe("ask-1");

    // A later read places the question under a node that has arrived since.
    tree.redraw(
      node({
        children: [
          first,
          node({
            children: [question],
            held: 1,
            id: "comment-2",
            text: "A thread.",
            type: "comment",
          }),
        ],
        id: KEY,
        type: "issue",
      })
    );
    await frames.run();
    expect(
      focusedRow(container),
      "the question's row drawn again lost the focus: the first row's frame ended the opening"
    ).toBe("ask-1");
  } finally {
    frames.restore();
  }
});

// A script's focus on a control before the question's landing frame is no act of the reader's,
// and the landing leaves the focus there (`landOnRow`). A read that draws the question's row
// again under another key before that frame ends the opening too, rather than land on the new
// row from the control.
test("a focus a script puts on a control before the question's landing frame stays there, through a redraw of the question's row", async () => {
  setViewport(1440);
  const { control } = controlOutside();
  const question = node({
    id: "ask-1",
    question: true,
    text: "Which flag?",
    type: "ask",
    waiting: true,
  });
  const frames = framesByHand();
  try {
    const tree = renderPageTree(node({ children: [question], id: KEY, type: "issue" }));
    const { container } = tree.view;
    const asked = rowsFor(container, "ask-1")[0];
    await act(async () => control.focus());
    // A later read places the question under a node that has arrived since.
    tree.redraw(
      node({
        children: [
          node({
            children: [question],
            held: 1,
            id: "comment-1",
            text: "The first words.",
            type: "comment",
          }),
        ],
        id: KEY,
        type: "issue",
      })
    );
    expect(rowsFor(container, "ask-1"), "the read drew the question's row nowhere").toHaveLength(1);
    expect(rowsFor(container, "ask-1")[0], "the read kept the question's row").not.toBe(asked);
    await frames.run();
    expect(
      document.activeElement,
      "the question's row drawn again took the focus from the control a script focused"
    ).toBe(control);
  } finally {
    frames.restore();
  }
});

// While the question's landing waits for its frame, the first row the opening focused before
// keeps the focus, which is still the opening's: reads that draw the tree again meanwhile end
// nothing, and the opening goes on - here through a later read that draws the question's row
// again under another key.
test("the first row the opening focused keeps the opening's focus while the question's landing waits", async () => {
  setViewport(1440);
  const first = node({ id: "comment-1", text: "The first words.", type: "comment" });
  const question = node({
    id: "ask-1",
    question: true,
    text: "Which flag?",
    type: "ask",
    waiting: true,
  });
  const frames = framesByHand();
  try {
    const tree = renderPageTree(node({ children: [first], id: KEY, type: "issue" }));
    const { container } = tree.view;
    await frames.run();
    expect(focusedRow(container)).toBe("comment-1");
    // The asks' read answers, and other reads draw the tree again before the landing's frame.
    const asking = node({ children: [first, question], id: KEY, type: "issue" });
    tree.redraw(asking);
    tree.redraw(asking);
    await frames.run();
    expect(focusedRow(container), "the question whose read answered took no focus").toBe("ask-1");

    // A later read places the question under a node that has arrived since.
    tree.redraw(
      node({
        children: [
          first,
          node({
            children: [question],
            held: 1,
            id: "comment-2",
            text: "A thread.",
            type: "comment",
          }),
        ],
        id: KEY,
        type: "issue",
      })
    );
    await frames.run();
    expect(
      focusedRow(container),
      "the question's row drawn again lost the focus: a redraw while its landing waited ended the opening"
    ).toBe("ask-1");
  } finally {
    frames.restore();
  }
});

// The same where a read draws the first row the opening focused again under another key before
// the question's landing frame: the row's old element leaves the page with the focus, which the
// browser drops to the body, where nobody holds it, and the opening goes on. Read as somebody
// else's focus - on neither the row the landing found it on nor the question's - it ended the
// opening: the frame still landed on the question, and the next read that drew the question's
// row again under another key left the focus on the page.
test("a focus a redraw drops from the first row while the question's landing waits is still the opening's", async () => {
  setViewport(1440);
  const theirs = node({ id: "ask-theirs", question: true, text: "Not yours?", type: "ask" });
  const question = node({
    id: "ask-1",
    question: true,
    text: "Which flag?",
    type: "ask",
    waiting: true,
  });
  // A node that has arrived since, holding `one`: a thread the frontier opens, since what it
  // holds is an open question.
  const thread = (id: string, one: TreeNode) =>
    node({ children: [one], held: 1, id, text: "A thread.", type: "comment" });
  const frames = framesByHand();
  try {
    const tree = renderPageTree(node({ children: [theirs], id: KEY, type: "issue" }));
    const { container } = tree.view;
    await frames.run();
    expect(focusedRow(container)).toBe("ask-theirs");
    const firstRow = rowsFor(container, "ask-theirs")[0];

    // The asks' read answers, and before the landing's frame a later read places the first row
    // under a node that has arrived since.
    tree.redraw(node({ children: [theirs, question], id: KEY, type: "issue" }));
    tree.redraw(
      node({ children: [thread("comment-1", theirs), question], id: KEY, type: "issue" })
    );
    expect(rowsFor(container, "ask-theirs"), "the read drew the first row nowhere").toHaveLength(1);
    expect(rowsFor(container, "ask-theirs")[0], "the read kept the first row").not.toBe(firstRow);
    expect(document.activeElement).toBe(document.body);
    await frames.run();
    expect(focusedRow(container), "the question's landing took no focus").toBe("ask-1");

    // A later read places the question under a node that has arrived since.
    tree.redraw(
      node({
        children: [thread("comment-1", theirs), thread("comment-2", question)],
        id: KEY,
        type: "issue",
      })
    );
    await frames.run();
    expect(
      focusedRow(container),
      "the question's row drawn again lost the focus: the first row's redraw while the landing waited ended the opening"
    ).toBe("ask-1");
  } finally {
    frames.restore();
  }
});

// The same where the focus is on the question's own row before its landing's frame - a script's
// focus, which is no act of the reader's, put where the landing is to put it: the focus is the
// opening's still, and reads that draw the tree meanwhile end nothing. Read as somebody else's
// focus - neither nobody's nor where the landing found it - it ended the opening: a later read
// that drew the question's row again under another key left the focus on the page, and the
// frame asked for the row that had left it landed nowhere.
test("a focus on the question's own row while its landing waits is still the opening's", async () => {
  setViewport(1440);
  const question = node({
    id: "ask-1",
    question: true,
    text: "Which flag?",
    type: "ask",
    waiting: true,
  });
  const frames = framesByHand();
  try {
    const tree = renderPageTree(node({ children: [question], id: KEY, type: "issue" }));
    const { container } = tree.view;
    const asked = rowsFor(container, "ask-1")[0];
    await act(async () => asked?.focus());
    expect(focusedRow(container), "the script's focus is not on the question's row").toBe("ask-1");

    // Another read draws the tree again, and a later one places the question under a node that
    // has arrived since, all before the landing's frame.
    tree.redraw(node({ children: [question], id: KEY, type: "issue" }));
    tree.redraw(
      node({
        children: [
          node({
            children: [question],
            held: 1,
            id: "comment-1",
            text: "A thread.",
            type: "comment",
          }),
        ],
        id: KEY,
        type: "issue",
      })
    );
    expect(rowsFor(container, "ask-1"), "the read drew the question's row nowhere").toHaveLength(1);
    expect(rowsFor(container, "ask-1")[0], "the read kept the question's row").not.toBe(asked);
    await frames.run();
    expect(
      focusedRow(container),
      "the question's row drawn again lost the focus: the focus on it while its landing waited ended the opening"
    ).toBe("ask-1");
  } finally {
    frames.restore();
  }
});

// A row the page takes the reader to of its own accord - the node an address names, the open
// question the page opens on, or the outline's first row where no question waits - takes the
// focus in the frame after the commit that draws it, not in that commit: a focus given there
// made the browser lay the whole new page out inside the reader's press, 831 ms of a 3.9 s press
// that changed the view on a large issue. It is focused once, without a scroll; the page's one
// rule places the node and the question after, and the first row stays where it stands. A read
// that draws a row above the first before the frame comes gives the focus to that row, and the
// row the frame was first asked for takes none. Before the frame, a key the reader presses keeps
// the focus where it is, even a key that moves none, and so does a script's focus on a control,
// which is no act of the reader's.
test("a row the page lands on takes the focus in the next frame, without a scroll, unless a key or a script's focus came first", async () => {
  setViewport(1440);
  const first = node({ id: "comment-1", text: "The first words.", type: "comment" });
  const named = node({ id: "comment-2", text: "The second words.", type: "comment" });
  const question = node({
    id: "ask-1",
    question: true,
    text: "Which flag?",
    type: "ask",
    waiting: true,
  });
  const addressed = node({ children: [first, named], id: KEY, type: "issue" });
  const asking = node({ children: [first, question], id: KEY, type: "issue" });
  const landings = [
    ["the node an address names", addressed, [addressed, named], "comment-2"],
    ["the open question the page opens on", asking, undefined, "ask-1"],
    // The same issue with no address: no question waits on the reader, so the page opens on its
    // first row.
    ["the first row the page opens on", addressed, undefined, "comment-1"],
  ] as const;
  const realFocus = HTMLElement.prototype.focus;
  const focuses: { readonly element: HTMLElement; readonly options: FocusOptions | undefined }[] =
    [];
  const focus = spyOn(HTMLElement.prototype, "focus").mockImplementation(function (
    this: HTMLElement,
    options?: FocusOptions
  ) {
    focuses.push({ element: this, options });
    realFocus.call(this, options);
  });
  const focusesOf = (row: HTMLElement | undefined) =>
    focuses.filter(({ element }) => element === row).map(({ options }) => options);
  try {
    for (const [what, root, reveal, landsOn] of landings) {
      focuses.length = 0;
      const view = renderTree(root, reveal === undefined ? {} : { reveal });
      const row = rowsFor(view.container, landsOn)[0];
      expect(row, `${what} draws no row`).toBeDefined();
      expect(focusesOf(row), `${what} took the focus in the commit that drew it`).toEqual([]);
      expect(document.activeElement).toBe(document.body);

      await nextFrame();
      expect(focusedRow(view.container), `${what} has no focus after the next frame`).toBe(landsOn);
      expect(focusesOf(row), `${what} was focused with a scroll, or more than once`).toEqual([
        { preventScroll: true },
      ]);
      view.unmount();
    }

    // A read draws a row above the first before the frame: the frame focuses the row that is
    // first when it comes, and the row it was first asked for, still on the page, takes no focus.
    focuses.length = 0;
    const tree = renderPageTree(node({ children: [first], id: KEY, type: "issue" }));
    const { container } = tree.view;
    const asked = rowsFor(container, "comment-1")[0];
    tree.redraw(
      node({
        children: [node({ id: "comment-0", text: "Older words.", type: "comment" }), first],
        id: KEY,
        type: "issue",
      })
    );
    expect(asked?.isConnected, "the read took the first row it was asked for off the page").toBe(
      true
    );
    await nextFrame();
    expect(focusedRow(container), "the row first when the frame came has no focus").toBe(
      "comment-0"
    );
    expect(focusesOf(asked), "the row the frame was first asked for took the focus").toEqual([]);
    expect(focusesOf(rowsFor(container, "comment-0")[0])).toEqual([{ preventScroll: true }]);
    tree.view.unmount();

    // Before the frame, a key the reader presses with the focus on the page - Shift alone, which
    // moves no focus - or a script's focus on a control with no key pressed: each landing leaves
    // the focus where that left it.
    const { control } = controlOutside();
    const pressShift = () => {
      document.body.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Shift" }));
    };
    for (const [what, root, reveal, landsOn] of landings) {
      for (const [came, run, stays] of [
        ["a key that moves no focus", pressShift, document.body],
        ["a script's focus on a control", () => control.focus(), control],
      ] as const) {
        focuses.length = 0;
        const view = renderTree(root, reveal === undefined ? {} : { reveal });
        expect(document.activeElement).toBe(document.body);
        await act(async () => run());
        await nextFrame();
        expect(document.activeElement, `${what} took the focus from where ${came} left it`).toBe(
          stays
        );
        expect(
          focusesOf(rowsFor(view.container, landsOn)[0]),
          `${what} was focused after ${came}`
        ).toEqual([]);
        view.unmount();
        await act(async () => control.blur());
      }
    }
  } finally {
    focus.mockRestore();
  }
});

// Where no question waits on the reader the page opens on the outline's first row, whose body
// opens with the focus, and scrolls nothing - not even for a first row taller than the window,
// which the page's one rule (`identity-block.ts`'s `bringOnScreen`) would put at the top under
// the band, the page opening scrolled past its header (89 px at 1440 by 900 on a real issue of
// some 160 rows). The reader's first move places the row it moves to as every move does.
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

// The tester's finding: what a key reports when it has nowhere to go is drawn at the row it was
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

// `q` and `w` each work out where they go from the tree as it stands, so pressed before the
// reads the tree is built from have answered, the absence they say may be false - the question
// may be in the asks' read still out. Until those reads hold an answer (`slots/body.tsx`) the key
// says nothing, and once they do it says there is none.
test("q and w say there is nowhere to go only once the reads the tree is built from are held", async () => {
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
  for (const [key, report, says] of [
    ["q", KEY_REPORTS.no_next_question, "there is no next question"],
    ["w", KEY_REPORTS.no_decision, "no decision is behind the row"],
  ] as const) {
    const view = renderTree(root, { readsHeld: false });
    await waitFor(() => expect(focusedRow(view.container)).toBe("comment-1"));
    // `user.keyboard` answers once the render its keys made has been drawn.
    await user.keyboard(key);
    expect(focusedRow(view.container), `${key} moved with nowhere to go`).toBe("comment-1");
    expect(
      rowReport(view.container),
      `${key} said ${says} before the tree's reads held an answer`
    ).toBeUndefined();

    view.holdReads();
    await user.keyboard(key);
    await waitFor(() => expect(rowReport(view.container)).toBe(report));
    view.unmount();
  }
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

// The Contents list asks the outline through the page's provider, and each call is a request of
// its own - to a node in the whole outline, or, with Focus branch on, to that node's branch
// alone.
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

// Focus branch, the Contents pane's toggle, is the page's, held above the outline by
// `outline-request.tsx`'s provider, and it outlives a view drawn in the outline's place, where
// the outline's zoom did not: the outline drawn again as the view is turned off is a new one,
// and the request that zoomed the one before was acted on already, so it drew every branch while
// the toggle read on. Drawn again while Focus branch is on, the outline draws the last request's
// branch alone, as the outline before it did, and takes the reader nowhere: it is no new
// request, and the focus stays where the view left it. Focus branch turned off, as the Contents
// list turns it, draws the whole outline again.
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
// no page opening on what waits on the reader, so it does not go to a question outside the
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
  // The page opens on the question that waits on the reader.
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

// The fold's half of a finding of the tester's: a reply the reader wrote under a folded branch
// is not left behind the fold. The writer's landing names what the write was made under; once
// the read that holds the new node draws it, the branch unfolds and the reader's new row takes
// the focus.
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

// `r` sets the line at the foot to reply to the focused row, and `a` sets it to answer the
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
  // The page opens on the question that waits on the reader.
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

// The architect's ruling on a node's one line: a crumb draws one line for a node, so it is the
// node's first line (`firstLineOf`), cut at a word, never cut again mid-word by its box; the
// issue's crumb is its key.
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

// No node is named by its id. A crumb of a node with no words of its own, a plan task drawn
// with no text, names it by its kind's word.
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

// The focused node's title in the panel and a crumb each draw one line for a node, cut from
// `lineWords`, so a comment whose words are only a bare address is named by that address's short
// form, as a link to it draws it, and never by the address as written.
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

// The line that says the issue holds nothing is the page's to give (`slots/body.tsx`), and it
// is drawn after the tree, where the issue holds nothing and the reads are held. A tree given no
// line draws none there: no empty paragraph under the rows.
test("a tree given no empty line draws none for an issue that holds nothing", () => {
  setViewport(1440);
  const given = renderTree(node({ id: KEY, type: "issue" }), { emptyLine: "Nothing here." });
  expect(given.getByRole("tree").nextElementSibling?.textContent).toBe("Nothing here.");
  given.unmount();

  const none = renderTree(node({ id: KEY, type: "issue" }));
  expect(
    none.getByRole("tree").nextElementSibling,
    "a tree given no empty line drew one with no words"
  ).toBeNull();
});

// The page's stage mounts the outline's slot again on every press of a view, and under the app's
// `staleTime` a read is stale 30 s after it answered (`main.tsx`). The reads the outline waits on
// before it says the issue holds nothing are kept fresh by events, as the tree's own reads are
// (`useIssueTree.ts`): the slot mounted again more than 30 s on reads none of them again.
test("the outline mounted again past 30 s reads none of the reads it waits on again", async () => {
  setViewport(1440);
  const issue: IssueDetails = { ...issueDetails(), artifacts: [] };
  const route = { key: KEY, kind: "issue" } as const;
  // The app's client's `staleTime` (`main.tsx`), which a read that sets none of its own takes.
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 30_000 } },
  });
  client.setQueryData(["whoami"], { kind: "user", login: "alice" });
  client.setQueryData(issueCoverageQuery(KEY).queryKey, { requirements: [] } as never);
  client.setQueryData(issueResultsQuery(KEY).queryKey, { checks: [] } as never);
  client.setQueryData(["decisions", KEY], { decisions: [], document: null, open_questions: [] });
  client.setQueryData(["asks", KEY], []);
  client.setQueryData(["comments", KEY], []);
  client.setQueryData(["issue-messages", KEY], []);
  const reads = [
    spyOn(api, "getIssueCoverage").mockResolvedValue({ requirements: [] } as never),
    spyOn(api, "getIssueDecisions").mockResolvedValue({
      decisions: [],
      document: null,
      open_questions: [],
    } as never),
    spyOn(api, "getIssueResults").mockResolvedValue({ checks: [] } as never),
    spyOn(api, "listIssueAsks").mockResolvedValue([] as never),
    spyOn(api, "listComments").mockResolvedValue([] as never),
    spyOn(api, "listIssueMessages").mockResolvedValue([] as never),
  ];
  const sent = () => reads.reduce((all, read) => all + read.mock.calls.length, 0);
  // The reader, whom the rows read under the app's `staleTime`, and the names, answered so that
  // neither reaches the network.
  const others = [
    spyOn(api, "whoAmI").mockResolvedValue({ kind: "user", login: "alice" } as never),
    spyOn(api, "getReferenceTargets").mockResolvedValue({ targets: [] } as never),
  ];
  const draw = (shown: boolean) => (
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <KeymapProvider>
          <MarginProvider>
            <ProvidersSlot issue={issue} route={route}>
              {shown ? <BodySlot issue={issue} route={route} /> : null}
            </ProvidersSlot>
          </MarginProvider>
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  try {
    const view = render(draw(true));
    await waitFor(() =>
      expect(view.container.textContent).toContain(EMPTY_STATE_LINES.conversation)
    );
    await waitFor(() => expect(client.isFetching()).toBe(0));
    const loaded = sent();
    view.rerender(draw(false));

    // A press of another view, more than 30 s after the load, mounts the slot again.
    setSystemTime(Date.now() + 31_000);
    view.rerender(draw(true));
    // Long enough for a read the mount started to have been sent.
    await act(async () => {
      const waited = Promise.withResolvers<void>();
      setTimeout(waited.resolve, 50);
      await waited.promise;
    });
    expect(sent(), "the outline mounted again read again what it waits on").toBe(loaded);
  } finally {
    setSystemTime();
    for (const spy of [...reads, ...others]) spy.mockRestore();
  }
});
