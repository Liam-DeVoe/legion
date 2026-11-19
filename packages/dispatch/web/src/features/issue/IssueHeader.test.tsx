import { expect, type Mock, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
// biome-ignore lint/style/noRestrictedImports: the presses that end a title edit go inside one act, so React commits what they change after them as a browser does; user-event commits inside each event
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { afterRescue, RescuedMain } from "../../__tests__/focus-rescue";
import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { answerIssuePatches } from "../../__tests__/send-answers";
import { api, type WriteBounds } from "../../api/client";
import { issueQuery } from "../../api/queries";
import type { Issue, IssueDetails, UpdateIssueInput } from "../../api/types";
import { MarginProvider } from "../margin/margin-context";
import { idlePriorityWrite, mockHeaderReads } from "./header-fixture";
import { IssueHeader } from "./IssueHeader";
import { stateForIssue } from "./pins";

// The issue title's editor, read with the real header and the page's focus rescue mounted around
// it as the app shell mounts it (`RescuedMain`).

const base: Issue = {
  advances: [],
  advances_revision: 0,
  assignee: null,
  blocked_by: [],
  claim: null,
  closed_at: null,
  components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
  created_at: "2026-09-24T05:00:00Z",
  created_by: { id: "alice", kind: "user" },
  decisions_artifact_id: "",
  external_links: [],
  key: "CORE-1",
  labels: [],
  last_seq: 1,
  number: 1,
  parent: null,
  plan_artifact_id: "",
  primary_artifact_id: "artifact-1",
  priority: null,
  project: "CORE",
  rank: "U",
  repo: null,
  resolved_repo: null,
  route: null,
  status: "in_progress",
  title: "Review the spec",
  updated_at: "2026-09-24T06:00:00Z",
  verification_artifact_id: "",
};

const details: IssueDetails = {
  ...base,
  artifacts: [],
  children: [],
  design_gates: [],
  holder_state: null,
  last_seen: null,
  open_asks: [],
  progress: { tasks: null, children: null },
  referenced_by_count: 0,
  route_holder: null,
  route_status: null,
  route_status_reason: null,
  takeable_at: null,
};

/** The text of the page outside the header, which a reader can click on. */
const PAGE_TEXT = "The issue's description";

type Patches = Mock<(key: string, input: UpdateIssueInput, bounds: WriteBounds) => Promise<Issue>>;

/** The header of the issue the cache holds, drawn again from each answer a write of it merges
 *  there, as `IssuePage` draws it. */
function CachedHeader(): ReactNode {
  const issue = useQuery({ ...issueQuery(base.key), staleTime: Infinity }).data;
  if (issue === undefined) return null;
  return (
    <IssueHeader
      documentArtifact={undefined}
      isClosed={false}
      issue={issue}
      priorityWrite={idlePriorityWrite}
      state={stateForIssue(undefined, issue.key)}
    />
  );
}

/** The header of `details` in the main content, the page's focus rescue mounted on it, with the
 *  page's text after it, and the title writes it makes answered as Dispatch answers them. */
function header(): { patches: Patches; unmount: () => void; user: UserEvent } {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  client.setQueryData(issueQuery(base.key).queryKey, details);
  // The header reads the project's repository mappings, its architecture sources and the issue's
  // subscribers at once; none of them is what these rows are about.
  const restoreReads = mockHeaderReads();
  const subscribers = spyOn(api, "getIssueSubscribers").mockResolvedValue([]);
  const patches = answerIssuePatches().mockImplementation(async (_key, input) => ({
    ...base,
    title: input.title ?? base.title,
    updated_at: "2026-09-24T07:00:00Z",
  }));
  const user = userEvent.setup();
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <MarginProvider>
          <RescuedMain>
            <CachedHeader />
            <p>{PAGE_TEXT}</p>
          </RescuedMain>
        </MarginProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  return {
    patches,
    unmount: () => {
      view.unmount();
      restoreReads();
      subscribers.mockRestore();
    },
    user,
  };
}

/**
 * Sends `press` as a browser has React commit it. A browser commits the update an event makes in
 * a microtask after that event, so the page's focus rescue, whose `focusout` listener on the
 * document runs before React's own, reads the page in a microtask of its own first; user-event
 * commits inside each event it sends, which no browser does. So these presses go out with
 * fireEvent inside one act, which commits what they changed once they have run.
 */
async function pressAsBrowser(press: () => void): Promise<void> {
  await act(async () => {
    press();
  });
}

/** Opens the title's editor with a click on its heading and writes `title` into it. */
async function editTitle(user: UserEvent, title: string): Promise<HTMLElement> {
  await user.click(await screen.findByRole("heading", { level: 1, name: base.title }));
  const editor = await screen.findByRole("textbox", { name: "Issue title" });
  await user.clear(editor);
  await user.keyboard(title);
  return editor;
}

/** The editor closed on the saved `title`, and the reader's next key, `t`, edits no title: no
 *  text box takes it, the heading still reads `title`, and the one write is the save's. */
async function expectNextKeyEditsNoTitle(
  user: UserEvent,
  patches: Patches,
  title: string
): Promise<void> {
  await waitFor(() => expect(patches).toHaveBeenCalledWith("CORE-1", { title }, keyedAttempt));
  expect(screen.queryByRole("textbox", { name: "Issue title" })).not.toBeInTheDocument();
  expect(screen.getByRole("heading", { level: 1, name: title })).toBeInTheDocument();
  await user.keyboard("t");
  await afterRescue();
  expect(screen.queryByRole("textbox", { name: "Issue title" })).not.toBeInTheDocument();
  expect(screen.getByRole("heading", { level: 1, name: title })).toBeInTheDocument();
  expect(patches).toHaveBeenCalledTimes(1);
}

// The reader's Enter saves the title and closes its editor: the editor's own `blur()` is the
// reader leaving it, so nothing reopens it in the heading's place, and the next key they type
// goes into no title.
test("Enter in the title's editor saves it and closes it, and the next key edits no title", async () => {
  const { patches, unmount, user } = header();
  try {
    const editor = await editTitle(user, "Renamed");
    await pressAsBrowser(() => fireEvent.keyDown(editor, { key: "Enter" }));
    await afterRescue();
    await expectNextKeyEditsNoTitle(user, patches, "Renamed");
  } finally {
    unmount();
  }
});

// So does the reader's click on the page's text outside the editor: the press lands on no
// control, the engine moves the focus to the page, and the blur saves and closes the editor.
test("a click on the page's text saves the title and closes its editor, and the next key edits no title", async () => {
  const { patches, unmount, user } = header();
  try {
    const editor = await editTitle(user, "Renamed");
    const text = screen.getByText(PAGE_TEXT);
    await pressAsBrowser(() => {
      fireEvent.pointerDown(text);
      fireEvent.mouseDown(text);
      // The engine's answer to a press on no control, which happy-dom leaves to the page.
      editor.blur();
      fireEvent.pointerUp(text);
      fireEvent.mouseUp(text);
      fireEvent.click(text);
    });
    await afterRescue();
    await expectNextKeyEditsNoTitle(user, patches, "Renamed");
  } finally {
    unmount();
  }
});

// The heading opens its editor when the reader activates it, not when it takes the focus: an
// editor opening on focus changes the page on focus (WCAG 2.2, 3.2.1 On Focus).
test("focusing the title's heading does not open its editor", async () => {
  const { unmount } = header();
  try {
    const heading = await screen.findByRole("heading", { level: 1, name: base.title });
    act(() => heading.focus());
    await afterRescue();
    expect(heading).toHaveFocus();
    expect(screen.queryByRole("textbox", { name: "Issue title" })).not.toBeInTheDocument();
  } finally {
    unmount();
  }
});

// A click on the heading opens its editor, and so does Enter on the focused heading; the editor
// takes the focus either way.
test("a click on the title's heading, and Enter on it, open its editor", async () => {
  const { unmount, user } = header();
  try {
    await user.click(await screen.findByRole("heading", { level: 1, name: base.title }));
    expect(await screen.findByRole("textbox", { name: "Issue title" })).toHaveFocus();
    await user.keyboard("{Escape}");
    await afterRescue();
    const heading = await screen.findByRole("heading", { level: 1, name: base.title });
    expect(screen.queryByRole("textbox", { name: "Issue title" })).not.toBeInTheDocument();

    act(() => heading.focus());
    await user.keyboard("{Enter}");
    expect(await screen.findByRole("textbox", { name: "Issue title" })).toHaveFocus();
  } finally {
    unmount();
  }
});
