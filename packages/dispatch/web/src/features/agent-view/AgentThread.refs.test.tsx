import { afterEach, expect, type Mock, spyOn, test } from "bun:test";
import {
  AssistantRuntimeProvider,
  type ThreadMessageLike,
  useExternalStoreRuntime,
} from "@assistant-ui/react";
import { type AgentStreamCommand, dispatchWritingBox } from "@legion/contracts";
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { graphNode } from "../../__tests__/graph-fixture";
import { renderWithQueries as render } from "../../__tests__/query-provider";
import { plannerAgent, searchResponse } from "../../__tests__/search-fixture";
import { api } from "../../api/client";
import type { ArtifactWithTrees, GraphReferences, SearchResult } from "../../api/types";
import { NO_SESSION_SENDS } from "../conversation/useSessionSends";
import { REF_PICKER_INSTRUCTION } from "../refs/ref-picker";
import { AgentThread } from "./AgentThread";

/**
 * The reference picker in an agent's live conversation: the three doors of every other box over
 * every kind it reaches, beside the `/` that lists the session's own commands, in assistant-ui's
 * composer.
 */

// The session this conversation is with: it advertises no delivery mode, which is what a live
// conversation's own box needs none of.
const planner = plannerAgent({ capabilities: [] });

const COMMANDS: AgentStreamCommand[] = [
  { description: "Compact the session's context", name: "compact", source: "builtin" },
];

/** What the search answers for `relay`: an issue, and a message of it, which has an address of
 *  its own like any node of an issue. */
const hitRows: SearchResult[] = [
  {
    href: "/issues/CORE-2",
    id: "CORE-2",
    kind: "issue",
    owner: { key: "CORE-2", kind: "issue", status: "open", title: "Relay drops a reply" },
    rank: 0.1,
    snippet: "The relay drops a reply",
  },
  {
    href: "/issues/CORE-2/messages/message-1",
    id: "message-1",
    kind: "message",
    owner: { key: "CORE-2", kind: "issue", status: "open", title: "Relay drops a reply" },
    rank: 0.09,
    snippet: "I told the <mark>relay</mark> to retry",
  },
];

/** The message's one block, as its `part_of` backlinks answer it. */
const messageBlocks: GraphReferences = {
  edges: [
    {
      created_at: "2026-11-01T00:00:00Z",
      direction: "in",
      excerpt: { text: "Retry the relay once" },
      kind: "part_of",
      node: graphNode({ block_id: "block-1", id: "message-1", kind: "block" }),
      source_seq: null,
    },
  ],
  node: graphNode({ id: "message-1", kind: "message" }),
};

/** The one file the planner's conversation holds. */
const shot: ArtifactWithTrees = {
  created_at: "2026-11-01T00:00:00Z",
  created_by: { id: "alice@theorem.dev", kind: "user" },
  id: "artifact-shot",
  issue_key: null,
  kind: "image",
  name: "shot.png",
  primary: false,
  project: "",
  ref_key: "agent/planner-session/shot-png",
  session_id: "planner-session",
  slug: "shot-png",
  versions: [],
};

interface Pane {
  /** Every message the composer sent, in press order. */
  readonly sent: string[];
  readonly search: Mock<typeof api.search>;
  readonly getReferences: Mock<typeof api.getReferences>;
  readonly listAgentArtifacts: Mock<typeof api.listAgentArtifacts>;
  readonly restore: () => void;
}

let pane: Pane | undefined;

afterEach(() => {
  cleanup();
  pane?.restore();
  pane = undefined;
});

function Harness({
  commands,
  onSend,
}: {
  commands?: AgentStreamCommand[];
  onSend: (body: string) => void;
}): ReactNode {
  const runtime = useExternalStoreRuntime({
    convertMessage: (message: ThreadMessageLike) => message,
    isRunning: false,
    messages: [],
    onNew: async (message) => {
      const [part] = message.content;
      onSend(part?.type === "text" ? part.text : "");
    },
  });
  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <AgentThread
        commands={commands}
        contentIds={[]}
        empty="Nothing yet."
        front
        held={false}
        initialPlace={null}
        onPlace={() => {}}
        ownSendCount={0}
        placeholder="Message the session…"
        replayCount={0}
        resetKey="session-1"
        sends={NO_SESSION_SENDS}
      />
    </AssistantRuntimeProvider>
  );
}

function renderThread(commands?: AgentStreamCommand[]): HTMLTextAreaElement {
  const listAgents = spyOn(api, "listAgents").mockResolvedValue([planner]);
  const listUsers = spyOn(api, "listUsers").mockResolvedValue([{ login: "alice@theorem.dev" }]);
  const search = spyOn(api, "search").mockImplementation(async (query: string) =>
    searchResponse(
      hitRows.filter((hit) => hit.snippet.toLowerCase().includes(query.toLowerCase())),
      { took_ms: 2 }
    )
  );
  const getReferences = spyOn(api, "getReferences").mockResolvedValue(messageBlocks);
  const listAgentArtifacts = spyOn(api, "listAgentArtifacts").mockResolvedValue([shot]);
  const sent: string[] = [];
  render(
    <MemoryRouter>
      <Harness commands={commands} onSend={(body) => sent.push(body)} />
    </MemoryRouter>
  );
  pane = {
    getReferences,
    listAgentArtifacts,
    restore: () => {
      listAgentArtifacts.mockRestore();
      getReferences.mockRestore();
      search.mockRestore();
      listUsers.mockRestore();
      listAgents.mockRestore();
    },
    search,
    sent,
  };
  return within(screen.getByTestId("agent-composer")).getByRole("textbox") as HTMLTextAreaElement;
}

/** Writes `value` into the composer as one edit, as the slash-command rows do: a clear before
 *  the paste would be a delete the trigger popover follows and a list it closed. */
async function type(user: UserEvent, input: HTMLTextAreaElement, value: string): Promise<void> {
  await user.click(input);
  await user.keyboard("{Control>}a{/Control}");
  await user.paste(value);
}

/** The picker's rows in the order they are drawn, each as `<heading>: <words>`: a row's
 *  heading is the nearest element before it that is not a row. */
function drawnRows(): string[] {
  const list = screen.getByRole("listbox", { name: "References" });
  return within(list)
    .getAllByRole("option")
    .map((option) => {
      let heading = "";
      for (let node = option.previousElementSibling; node !== null; ) {
        if (node.getAttribute("role") === "option") {
          node = node.previousElementSibling;
          continue;
        }
        heading = node.textContent ?? "";
        break;
      }
      return `${heading}: ${option.querySelector("span span")?.textContent ?? ""}`;
    });
}

test("each of the three doors opens the picker over the live composer", async () => {
  const user = userEvent.setup();
  const input = renderThread();

  await type(user, input, "@");
  await waitFor(() =>
    expect(drawnRows()).toEqual(["People: alice@theorem.dev", "Agents: Planner", "Roles: planner"])
  );

  // `#` and `[[` reach the same rows; the searched kinds need two characters typed.
  await type(user, input, "#relay");
  await waitFor(() =>
    expect(drawnRows()).toEqual([
      "Issues: Relay drops a reply",
      "Messages: I told the relay to retry",
    ])
  );

  await type(user, input, "[[relay");
  await waitFor(() =>
    expect(drawnRows()).toEqual([
      "Issues: Relay drops a reply",
      "Messages: I told the relay to retry",
    ])
  );
});

test("this box offers an issue's message as every box does, and asks the search for every kind", async () => {
  const user = userEvent.setup();
  const input = renderThread();

  await type(user, input, "#relay");
  await waitFor(() => expect(pane?.search).toHaveBeenCalledTimes(1));
  // The picker's message is a message of an issue, which has an address like any of the issue's
  // nodes: the route is asked for it as for every other searched kind.
  expect(pane?.search.mock.calls[0]).toEqual([
    "relay",
    { kind: ["issue", "document", "ask", "comment", "message"], limit: 8 },
  ]);
  await waitFor(() => expect(drawnRows()).toContain("Messages: I told the relay to retry"));
  await user.click(screen.getByRole("option", { name: /^I told the relay to retry/ }));
  await waitFor(() => expect(input.value).toBe("dispatch://CORE-2/message/message-1 "));
  // The instruction names what the next two letters will add, messages among them.
  await type(user, input, "@");
  await waitFor(() =>
    expect(screen.getByTestId("ref-picker-instruction").textContent).toBe(REF_PICKER_INSTRUCTION)
  );
  expect(REF_PICKER_INSTRUCTION).toContain("messages");
});

test("a row's Blocks control lists its blocks, and a block pick is its address", async () => {
  const user = userEvent.setup();
  const input = renderThread();

  await type(user, input, "#relay");
  await waitFor(() => expect(drawnRows()).toContain("Messages: I told the relay to retry"));
  // An issue holds no blocks of its own, so the message's row alone offers them.
  expect(screen.getAllByRole("button", { name: "Blocks" })).toHaveLength(1);

  await user.click(screen.getByRole("button", { name: "Blocks" }));

  await waitFor(() =>
    expect(pane?.getReferences).toHaveBeenCalledWith("dispatch://CORE-2/message/message-1", {
      kind: "part_of",
    })
  );
  await waitFor(() => expect(drawnRows()).toContain("Message blocks: Retry the relay once"));
  // Opening the parts is not a pick: the trigger the reader typed is still there.
  expect(input.value).toBe("#relay");
  await user.click(screen.getByRole("option", { name: /^Retry the relay once/ }));
  await waitFor(() => expect(input.value).toBe("dispatch://CORE-2/message/message-1#b-block-1 "));
  expect(pane?.sent).toEqual([]);
});

test("ArrowRight opens the highlighted row's parts, and nothing on a row that holds none", async () => {
  const user = userEvent.setup();
  const input = renderThread();

  await type(user, input, "#relay");
  await waitFor(() =>
    expect(drawnRows()).toEqual([
      "Issues: Relay drops a reply",
      "Messages: I told the relay to retry",
    ])
  );
  // The issue is highlighted and holds no blocks: ArrowRight opens nothing.
  await user.keyboard("{ArrowRight}");
  expect(pane?.getReferences).not.toHaveBeenCalled();

  await user.keyboard("{ArrowDown}");
  await waitFor(() =>
    expect(screen.getByRole("option", { selected: true })).toHaveTextContent(
      "I told the relay to retry"
    )
  );
  await user.keyboard("{ArrowRight}");
  await waitFor(() =>
    expect(pane?.getReferences).toHaveBeenCalledWith("dispatch://CORE-2/message/message-1", {
      kind: "part_of",
    })
  );
  await waitFor(() => expect(drawnRows()).toContain("Message blocks: Retry the relay once"));
  await user.keyboard("{ArrowDown}{ArrowDown}");
  await waitFor(() =>
    expect(screen.getByRole("option", { selected: true })).toHaveTextContent("Retry the relay once")
  );
  await user.keyboard("{Enter}");
  await waitFor(() => expect(input.value).toBe("dispatch://CORE-2/message/message-1#b-block-1 "));
  expect(pane?.sent).toEqual([]);
});

test("an agent's row opens its conversation's documents, and a pick is the document's address", async () => {
  const user = userEvent.setup();
  const input = renderThread();

  await type(user, input, "@plan");
  await waitFor(() => expect(drawnRows()).toEqual(["Agents: Planner", "Roles: planner"]));
  await user.click(screen.getByRole("button", { name: "Documents" }));

  await waitFor(() => expect(pane?.listAgentArtifacts).toHaveBeenCalledWith("planner-session"));
  await waitFor(() =>
    expect(drawnRows()).toEqual(["Agents: Planner", "Roles: planner", "Documents: shot.png"])
  );
  await user.click(screen.getByRole("option", { name: /^shot\.png/ }));
  await waitFor(() =>
    expect(input.value).toBe("dispatch://agent/planner-session/artifact/shot-png ")
  );
});

test("the `/` door still lists the session's own commands", async () => {
  const user = userEvent.setup();
  const input = renderThread(COMMANDS);

  await type(user, input, "/");
  await waitFor(() =>
    expect(
      within(screen.getByRole("listbox", { name: "Slash commands" }))
        .getAllByRole("option")
        .map((option) => option.textContent)
    ).toEqual(["/compactCompact the session's context"])
  );
  // The slash list is its own: a command is the characters a terminal reads, not a reference.
  expect(screen.queryByRole("listbox", { name: "References" })).not.toBeInTheDocument();
  await user.keyboard("{Enter}");
  await waitFor(() => expect(input.value).toBe("/compact "));
  expect(pane?.sent).toEqual([]);
});

test("Enter takes the highlighted row while the picker is open, and sends while it is closed", async () => {
  const user = userEvent.setup();
  const input = renderThread();

  await type(user, input, "See #relay");
  await waitFor(() =>
    expect(drawnRows()).toEqual([
      "Issues: Relay drops a reply",
      "Messages: I told the relay to retry",
    ])
  );

  // Enter belongs to the open list: it takes the row and sends nothing.
  await user.keyboard("{Enter}");
  await waitFor(() => expect(input.value).toBe("See dispatch://CORE-2 "));
  expect(pane?.sent).toEqual([]);
  expect(screen.queryByRole("listbox", { name: "References" })).not.toBeInTheDocument();

  // With the list closed Enter is the composer's Send again.
  await user.keyboard("{Enter}");
  // The space the pick left after the address goes out with the message, as a typed one would.
  await waitFor(() => expect(pane?.sent).toEqual(["See dispatch://CORE-2 "]));
});

test("a picked agent is written as the link that names it, and the hint says who is told", async () => {
  const user = userEvent.setup();
  const input = renderThread();

  // The words the list owns, read as the box reads them: a change of them is made in one place.
  expect(screen.getByTestId("agent-message-hint").textContent).toBe(
    dispatchWritingBox("agent-live-composer", "agent-message").hint
  );

  await type(user, input, "@plan");
  await waitFor(() => expect(drawnRows()).toEqual(["Agents: Planner", "Roles: planner"]));
  await user.keyboard("{Enter}");
  await waitFor(() => expect(input.value).toBe("[Planner](dispatch://agent/planner-session) "));
});
