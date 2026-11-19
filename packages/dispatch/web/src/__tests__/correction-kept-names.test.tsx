import { afterEach, beforeEach, expect, setSystemTime, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";

import { api } from "../api/client";
import { useEventFeed } from "../api/event-feed";
import { liveSocketFor } from "../api/live-socket";
import { MIN_REFETCH_INTERVAL_MS } from "../api/query-refresh";
import type {
  AuthenticatedUser,
  Comment,
  DispatchEvent,
  IssueDetails,
  ReferenceTarget,
} from "../api/types";
import {
  ReferenceNamesProvider,
  useKnownEditedReferences,
  useKnownReferenceName,
} from "../features/issue-page/reference-names";
import { useIssueTree } from "../features/issue-page/useIssueTree";
import { type FakeLiveSockets, installFakeLiveSockets } from "./live-socket-harness";

// R2 and R3 of dispatch://INFRA-1523, and the architect's ruling 4 of test round 42: a names read
// (`["reference-targets", <addresses>]`) that a post's edit or delete refreshes keeps the answer
// it held while the refetch is out, and TanStack keeps that answer when the refetch fails. So the
// feed writes the correction's own facts into the answer it keeps (`markCorrectedPost`): after a
// failed refetch the kept answer and the page's published name give a deleted post's deleted
// line, never its old words, and mark an edited post edited.

const KEY = "CORE-1";
const CITING_ID = "aaaaaaaa-0000-4000-8000-000000000001";
const CITED_ID = "11111111-2222-4333-8444-555555555555";
const CITED = `dispatch://${KEY}/comment/${CITED_ID}`;
const OLD_WORDS = "Hold the deploy.";
const ALICE = { id: "alice", kind: "user" } as const;

const issue: IssueDetails = {
  advances: [],
  advances_revision: 0,
  artifacts: [],
  assignee: null,
  blocked_by: [],
  children: [],
  claim: null,
  closed_at: null,
  components: { ids: [], inherited_from: null, mode: "inherit", reason: null, unknown: [] },
  created_at: "2026-11-05T00:00:00Z",
  created_by: ALICE,
  decisions_artifact_id: "",
  design_gates: [],
  external_links: [],
  holder_state: null,
  key: KEY,
  labels: [],
  last_seen: null,
  last_seq: 1,
  number: 1,
  open_asks: [],
  parent: null,
  plan_artifact_id: "",
  primary_artifact_id: "",
  priority: null,
  progress: { children: null, tasks: null },
  project: "CORE",
  rank: "U",
  referenced_by_count: 0,
  repo: null,
  resolved_repo: null,
  route: null,
  route_holder: null,
  route_status: null,
  route_status_reason: null,
  status: "todo",
  takeable_at: null,
  title: "The issue page names what its turns cite",
  updated_at: "2026-11-05T00:00:00Z",
  verification_artifact_id: "",
};

function turn(id: string, body: string): Comment {
  return {
    anchor: null,
    answers: null,
    artifact_id: null,
    ask_id: null,
    author: ALICE,
    body,
    created_at: "2026-11-05T01:00:00Z",
    deleted_at: null,
    deleted_by: null,
    deliveries: [],
    edited_at: null,
    id,
    issue_key: KEY,
    mentions: [],
    reply_block: null,
    reply_to: null,
    resolved: false,
    resolved_at: null,
    resolved_by: null,
    suggestion: null,
    turn: null,
  };
}

/** The page's reads as Dispatch answers them: `comments`, the tree's one names read from what
 *  `names` answers, and every other read empty. */
function stubPageReads(
  comments: () => readonly Comment[],
  names: (refs: readonly string[]) => Promise<{ targets: ReferenceTarget[] }>
) {
  return [
    spyOn(api, "getIssueCoverage").mockResolvedValue({
      gaps: [],
      has_plan: false,
      issue: KEY,
      requirements: [],
      spec_artifact_id: "",
    }),
    spyOn(api, "getIssueDecisions").mockResolvedValue({
      decisions: [],
      document: null,
      open_questions: [],
    }),
    spyOn(api, "getIssueResults").mockResolvedValue({
      checks: [],
      issue: KEY,
      verification_artifact_id: "",
    }),
    spyOn(api, "getIssueReferences").mockResolvedValue({ members: [], truncated: false }),
    spyOn(api, "listIssueAsks").mockResolvedValue([]),
    spyOn(api, "listComments").mockImplementation(async () => [...comments()]),
    spyOn(api, "listIssueMessages").mockResolvedValue([]),
    spyOn(api, "getArtifactBlocks").mockResolvedValue([]),
    spyOn(api, "whoAmI").mockResolvedValue({ event_head: "0", kind: "user", login: "alice" }),
    spyOn(api, "getReferenceTargets").mockImplementation(names),
  ];
}

let sockets: FakeLiveSockets;
const clients: QueryClient[] = [];

beforeEach(() => {
  sockets = installFakeLiveSockets();
});

afterEach(() => {
  for (const client of clients.splice(0)) {
    liveSocketFor(client).close();
    client.clear();
  }
  sockets.restore();
  setSystemTime();
});

/** An event of the feed on the issue, as Dispatch sends it: the payload as its writer wrote it. */
function feedEvent(type: DispatchEvent["type"], payload: Record<string, unknown>): DispatchEvent {
  return {
    actor: ALICE,
    created_at: "2026-11-05T02:00:00Z",
    id: 7,
    issue_key: KEY,
    notify: true,
    payload,
    project: "CORE",
    seq: 7,
    type,
  } as unknown as DispatchEvent;
}

/** The page open on the issue: the event feed, the tree and what it publishes, signed in as
 *  alice, with Dispatch answering `comments` and the names read from `stored`'s words, until
 *  `deliver` makes every names read fail. Returns what each step reads. */
async function openPage(stored: Comment[]) {
  let failing = false;
  let namesReads = 0;
  const stubs = stubPageReads(
    () => stored,
    async (refs) => {
      namesReads += 1;
      if (failing) throw new Error("the names read failed");
      return {
        targets: refs.map(
          (ref): ReferenceTarget => ({
            gone: false,
            kind: "comment",
            name: stored.find((one) => ref.endsWith(one.id))?.body ?? "",
            ref,
          })
        ),
      };
    }
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  clients.push(client);
  const signedIn: AuthenticatedUser = { event_head: "0", kind: "user", login: "alice" };
  client.setQueryData(["whoami"], signedIn);
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ReferenceNamesProvider>{children}</ReferenceNamesProvider>
    </QueryClientProvider>
  );
  const view = renderHook(
    () => {
      useEventFeed();
      useIssueTree(issue);
      return { edited: useKnownEditedReferences(), name: useKnownReferenceName(CITED) };
    },
    { wrapper }
  );
  await waitFor(() => expect(view.result.current.name).toBe(OLD_WORDS));
  await waitFor(() => expect(client.isFetching()).toBe(0));
  const socket = sockets.latest();
  act(() => socket.open());
  const { sub } = socket.subscribed("events");
  act(() => socket.receive({ type: "events_start", sub, since: "0", replay_complete: true }));
  return {
    /** What the names reads the tree holds keep for the cited comment. */
    cached: (): ReferenceTarget[] =>
      client
        .getQueryCache()
        .findAll({ queryKey: ["reference-targets"] })
        .flatMap(
          (query) => (query.state.data as { targets: ReferenceTarget[] } | undefined)?.targets ?? []
        )
        .filter((target) => target.ref === CITED),
    /** Delivers `event` on the feed with every names read failing from now on, and waits until
     *  the refetch it sends has failed. */
    deliver: async (event: DispatchEvent) => {
      failing = true;
      const before = namesReads;
      // A second on, so the scheduler's interval between two fetches of one read
      // (`MIN_REFETCH_INTERVAL_MS`) holds back none of the reads the event sends.
      setSystemTime(Date.now() + MIN_REFETCH_INTERVAL_MS);
      act(() => socket.receive({ type: "event", sub, id: String(event.id), event }));
      await waitFor(() => expect(namesReads).toBeGreaterThan(before));
      await waitFor(() => expect(client.isFetching()).toBe(0));
      const states = client
        .getQueryCache()
        .findAll({ queryKey: ["reference-targets"] })
        .map((query) => query.state.status);
      expect(states, "the names refetch the event sent failed").toContain("error");
    },
    restore: () => {
      view.unmount();
      for (const stub of stubs) stub.mockRestore();
    },
    view,
  };
}

test("a deleted post's names read whose refetch fails names the deleted line, never the old words", async () => {
  const cited = turn(CITED_ID, OLD_WORDS);
  const stored = [turn(CITING_ID, `Read ${CITED} first.`), cited];
  const page = await openPage(stored);
  try {
    const deletion = { deleted_at: "2026-11-05T02:00:00Z", deleted_by: ALICE };
    const { body: _body, ...routing } = cited;
    const deleted = { ...routing, ...deletion };
    stored[1] = { ...deleted, body: "" };
    // A delete's event carries the comment's routing fields and its marks, and no body.
    await page.deliver(feedEvent("comment.deleted", { ...deleted, artifact_name: "" }));
    expect({
      cached: page.cached(),
      published: page.view.result.current.name,
    }).toEqual({
      cached: [{ ...deletion, gone: false, kind: "comment", name: "", ref: CITED }],
      published: "Deleted by alice",
    });
  } finally {
    page.restore();
  }
});

test("an edited post's names read whose refetch fails marks the post edited", async () => {
  const cited = turn(CITED_ID, OLD_WORDS);
  const stored = [turn(CITING_ID, `Read ${CITED} first.`), cited];
  const page = await openPage(stored);
  try {
    const editedAt = "2026-11-05T02:00:00Z";
    const edited = { ...cited, body: "Hold the deploy until Friday.", edited_at: editedAt };
    stored[1] = edited;
    await page.deliver(feedEvent("comment.edited", { ...edited, artifact_name: "" }));
    // The words stay the kept answer's until a read succeeds; the mark is the edit's own.
    expect({
      cached: page.cached(),
      edited: [...page.view.result.current.edited],
    }).toEqual({
      cached: [{ edited_at: editedAt, gone: false, kind: "comment", name: OLD_WORDS, ref: CITED }],
      edited: [CITED],
    });
  } finally {
    page.restore();
  }
});
