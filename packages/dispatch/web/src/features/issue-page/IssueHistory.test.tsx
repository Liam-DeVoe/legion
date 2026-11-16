import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { api } from "../../api/client";
import { prependEventToLog } from "../../api/event-feed";
import type { Artifact, DispatchEvent, IssueDetails, MessageRead } from "../../api/types";
import { linkText, pageInkText } from "../../theme/classes";
import { dateKey } from "../activity/activity-model";
import { IssueHistory } from "./IssueHistory";
import { type OutlineWriter, OutlineWriterProvider, useOutlineWriter } from "./outline-writer";
import { CONTROL_LABELS } from "./page-words";

const KEY = "CORE-1";

/** A claim of the issue, which the history draws as one line (`history-model.ts`), made
 *  `20 - seq` seconds ago, so a higher sequence is newer and every one is today's, its day open. */
function claim(seq: number): DispatchEvent {
  const at = new Date(Date.now() - (20 - seq) * 1000).toISOString();
  return {
    actor: { id: "planner", kind: "session" },
    created_at: at,
    id: seq,
    issue_key: KEY,
    notify: false,
    payload: {},
    seq,
    type: "issue.claimed",
  } as DispatchEvent;
}

/** The reads the history makes (`IssueHistory.tsx`), stubbed: the log answers `events` and the
 *  issue's messages read `messages`, with no comment and no ask. Returns what restores them. */
function stubHistoryReads(
  events: readonly DispatchEvent[],
  messages: readonly MessageRead[] = []
): () => void {
  const reads = [
    spyOn(api, "getIssueEvents").mockResolvedValue([...events]),
    spyOn(api, "listComments").mockResolvedValue([]),
    spyOn(api, "listIssueMessages").mockResolvedValue([...messages]),
    spyOn(api, "listIssueAsks").mockResolvedValue([]),
  ];
  return () => {
    for (const read of reads) read.mockRestore();
  };
}

const ROW_HEIGHT = 40;
const LIST_TOP = 100;

function setScrollY(value: number): void {
  Object.defineProperty(window, "scrollY", { configurable: true, value, writable: true });
}

/** The geometry `ViewportAnchor` reads, faked as `ViewportAnchor.test.tsx` fakes it: every entry
 *  is `ROW_HEIGHT` tall, stacked from `LIST_TOP` in DOM order, and the page scrolls through them
 *  with `window.scrollY`. Anything that is not an entry measures as the list's own box. */
function installRowLayout(): { restore: () => void } {
  const rows = () => [...document.querySelectorAll<HTMLElement>("[data-event-seq]")];
  const rect = spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
    this: Element
  ) {
    const all = rows();
    const index = all.indexOf(this as HTMLElement);
    const top = LIST_TOP + (index < 0 ? 0 : index * ROW_HEIGHT) - window.scrollY;
    const height = index < 0 ? all.length * ROW_HEIGHT : ROW_HEIGHT;
    return {
      bottom: top + height,
      height,
      left: 0,
      right: 100,
      toJSON: () => ({}),
      top,
      width: 100,
      x: 0,
      y: top,
    } as DOMRect;
  });
  const point = spyOn(document, "elementFromPoint").mockImplementation(
    (_x, y) => rows()[Math.floor((window.scrollY + y - LIST_TOP) / ROW_HEIGHT)] ?? null
  );
  const originalScrollBy = window.scrollBy;
  window.scrollBy = ((x: number | ScrollToOptions, y?: number) => {
    const top = typeof x === "number" ? y : x.top;
    if (top !== undefined) setScrollY(window.scrollY + top);
  }) as typeof window.scrollBy;
  return {
    restore: () => {
      rect.mockRestore();
      point.mockRestore();
      window.scrollBy = originalScrollBy;
    },
  };
}

// The history reads `["events", <key>]`, the query the live feed prepends every published event
// into at the front of page 0 (`prependEventToLog`), and draws it newest first: each arrival
// pushes a reader who is down in the history one entry further down unless something holds
// their place, as the old Activity tab's `ViewportAnchor` did. Each entry carries
// `data-history-entry`, its own id, the identity the anchor follows.
test("an event arriving while the reader is in the history leaves their entry where it was", async () => {
  const scrollY = Object.getOwnPropertyDescriptor(window, "scrollY");
  const restoreReads = stubHistoryReads(
    Array.from({ length: 12 }, (_, index) => claim(12 - index))
  );
  const layout = installRowLayout();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  const issue = { artifacts: [], key: KEY } as unknown as IssueDetails;
  let unmount: (() => void) | undefined;

  try {
    unmount = render(
      <MemoryRouter>
        <QueryClientProvider client={queryClient}>
          <IssueHistory
            activityAddress={undefined}
            issue={issue}
            item={undefined}
            state={{ [KEY]: { dismissed: [], last_read_seq: 99, pinned: false, seq: 0 } }}
            visitMark={99}
          />
        </QueryClientProvider>
      </MemoryRouter>
    ).unmount;
    await screen.findByRole("list", { name: CONTROL_LABELS.history_list });
    await waitFor(() => expect(document.querySelectorAll("[data-event-seq]")).toHaveLength(12));
    setScrollY(300);
    const reading = document.querySelectorAll<HTMLElement>("[data-event-seq]")[8];
    const seq = reading?.getAttribute("data-event-seq");
    const before = reading?.getBoundingClientRect().top;

    act(() => {
      prependEventToLog(queryClient, claim(13));
    });
    await waitFor(() => expect(document.querySelectorAll("[data-event-seq]")).toHaveLength(13));

    expect(document.querySelector(`[data-event-seq="${seq}"]`)?.getBoundingClientRect().top).toBe(
      before
    );
    // The entry it held onto moved down one place in the list, so the page scrolled by one entry.
    expect(window.scrollY).toBe(300 + ROW_HEIGHT);
  } finally {
    unmount?.();
    layout.restore();
    restoreReads();
    if (scrollY === undefined) {
      Reflect.deleteProperty(window, "scrollY");
    } else {
      Object.defineProperty(window, "scrollY", scrollY);
    }
  }
});

/** A message an agent wrote outside every thread a person wrote in, `ago` seconds ago: a thread
 *  of the history whose first turn's event no page of the log read here holds. */
function agentMessage(id: string, ago: number): MessageRead {
  return {
    message: {
      author: { id: "planner", kind: "session" },
      body: `Agent note ${id}.`,
      broadcast_id: null,
      created_at: new Date(Date.now() - ago * 1000).toISOString(),
      deliveries: [],
      id,
      in_reply_to: null,
      issue_key: KEY,
      reply_block: null,
      target: null,
    },
    replies: [],
  };
}

// A thread whose first turn's event is on a page of the log not read yet carries seq 0, as every
// other such thread does, so two of them share one `data-event-seq`: the anchor follows each
// entry's own id instead, or it holds the first of them while the reader is on the second.
test("a reader on one of two threads whose events are unread keeps their entry when an event arrives", async () => {
  const scrollY = Object.getOwnPropertyDescriptor(window, "scrollY");
  const restoreReads = stubHistoryReads(
    Array.from({ length: 6 }, (_, index) => claim(6 - index)),
    [agentMessage("m-older", 200), agentMessage("m-newer", 100)]
  );
  const layout = installRowLayout();
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  const issue = { artifacts: [], key: KEY } as unknown as IssueDetails;
  let unmount: (() => void) | undefined;

  try {
    unmount = render(
      <MemoryRouter>
        <QueryClientProvider client={queryClient}>
          <IssueHistory
            activityAddress={undefined}
            issue={issue}
            item={undefined}
            state={{ [KEY]: { dismissed: [], last_read_seq: 99, pinned: false, seq: 0 } }}
            visitMark={99}
          />
        </QueryClientProvider>
      </MemoryRouter>
    ).unmount;
    await screen.findByRole("list", { name: CONTROL_LABELS.history_list });
    await waitFor(() => expect(document.querySelectorAll("[data-event-seq]")).toHaveLength(8));
    const entries = [...document.querySelectorAll<HTMLElement>("[data-event-seq]")];
    expect(entries.slice(6).map((entry) => entry.getAttribute("data-event-seq"))).toEqual([
      "0",
      "0",
    ]);
    // The reader is on the last entry, the older of the two threads: 360 px down puts it at the
    // centre of what the list shows of itself, where the anchor probes.
    setScrollY(360);
    const reading = entries[7];
    const before = reading?.getBoundingClientRect().top;

    act(() => {
      prependEventToLog(queryClient, claim(7));
    });
    await waitFor(() => expect(document.querySelectorAll("[data-event-seq]")).toHaveLength(9));

    expect(reading?.getBoundingClientRect().top).toBe(before);
  } finally {
    unmount?.();
    layout.restore();
    restoreReads();
    if (scrollY === undefined) {
      Reflect.deleteProperty(window, "scrollY");
    } else {
      Object.defineProperty(window, "scrollY", scrollY);
    }
  }
});

// R7 of dispatch://INFRA-3948: a turn's Reply is drawn only under the page's writer, and sets it
// to reply to that turn; the box at the foot of the outline that sends it is part L's.
test("a turn's Reply sets the writer to reply to that turn", async () => {
  const restoreReads = stubHistoryReads([], [agentMessage("m-turn", 60)]);
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  queryClient.setQueryData(["agents"], []);
  const issue = { artifacts: [], key: KEY } as unknown as IssueDetails;
  let writer: OutlineWriter | undefined;
  function Probe(): ReactNode {
    writer = useOutlineWriter();
    return null;
  }
  const history = (
    <IssueHistory
      activityAddress={undefined}
      issue={issue}
      item={undefined}
      state={{ [KEY]: { dismissed: [], last_read_seq: 99, pinned: false, seq: 0 } }}
      visitMark={99}
    />
  );
  let unmount: (() => void) | undefined;

  try {
    // With no writer above it, a turn draws no Reply.
    const alone = render(
      <MemoryRouter>
        <QueryClientProvider client={queryClient}>{history}</QueryClientProvider>
      </MemoryRouter>
    );
    try {
      await waitFor(() =>
        expect(document.querySelector('[data-history-item="m-turn"]')).not.toBeNull()
      );
      expect(document.querySelectorAll("[data-history-reply]")).toHaveLength(0);
    } finally {
      alone.unmount();
    }

    unmount = render(
      <MemoryRouter>
        <QueryClientProvider client={queryClient}>
          <OutlineWriterProvider issueKey={KEY}>
            <Probe />
            {history}
          </OutlineWriterProvider>
        </QueryClientProvider>
      </MemoryRouter>
    ).unmount;
    const reply = await screen.findByRole("button", { name: CONTROL_LABELS.reply });
    expect(reply.getAttribute("data-history-reply")).toBe("m-turn");
    expect(writer?.reply, "the writer replied to something before Reply").toBeNull();

    await userEvent.click(reply);

    await waitFor(() =>
      expect(writer?.reply).toMatchObject({ id: "m-turn", parentKind: "message" })
    );
    expect(writer?.editing).toBeUndefined();
  } finally {
    unmount?.();
    restoreReads();
  }
});

// R4 of dispatch://INFRA-3948: the address of a turn in a day before today opens that day with
// the turn marked, and the day's line is then the reader's like any other: a press on it closes
// the day the address opened.
test("a day the address opened closes when the reader presses its line", async () => {
  const earlier = agentMessage("m-earlier", 2 * 24 * 60 * 60);
  // Today's lines beside it: a document added, then renamed, each a link to the document.
  const plan: Artifact = {
    created_at: new Date().toISOString(),
    created_by: { id: "planner", kind: "session" },
    id: "plan",
    issue_key: KEY,
    kind: "doc",
    name: "plan.md",
    primary: false,
    project: "CORE",
    ref_key: "artifact:plan",
    session_id: null,
    slug: "plan-md",
    versions: [],
  };
  const restoreReads = stubHistoryReads(
    [
      {
        ...claim(3),
        payload: { artifact: plan, previous_name: "draft.md" },
        type: "artifact.renamed",
      } as DispatchEvent,
      {
        ...claim(2),
        payload: { artifact: { ...plan, name: "draft.md" } },
        type: "artifact.created",
      } as DispatchEvent,
      claim(1),
    ],
    [earlier]
  );
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  queryClient.setQueryData(["agents"], []);
  const issue = { artifacts: [], key: KEY } as unknown as IssueDetails;
  const dayLine = () =>
    document.querySelector<HTMLElement>(
      `[data-history-day="${dateKey(earlier.message.created_at)}"] > button`
    );
  let unmount: (() => void) | undefined;

  try {
    unmount = render(
      <MemoryRouter>
        <QueryClientProvider client={queryClient}>
          <IssueHistory
            activityAddress={undefined}
            issue={issue}
            item="m-earlier"
            state={{ [KEY]: { dismissed: [], last_read_seq: 99, pinned: false, seq: 0 } }}
            visitMark={99}
          />
        </QueryClientProvider>
      </MemoryRouter>
    ).unmount;
    await waitFor(() => expect(dayLine()?.getAttribute("aria-expanded")).toBe("true"));
    expect(
      document.querySelector('[data-history-item="m-earlier"]')?.getAttribute("aria-current")
    ).toBe("true");

    const line = dayLine();
    if (line === null) throw new Error("the earlier day draws no line");
    await userEvent.click(line);

    expect(dayLine()?.getAttribute("aria-expanded")).toBe("false");
    expect(document.querySelector('[data-history-item="m-earlier"]')).toBeNull();

    // A document's lines link to it in the page's ink, as an upload's do, never in the
    // application's link blue (decision 56).
    for (const name of ["draft.md", "plan.md"]) {
      const link = screen.getByRole("link", { name });
      expect(link.getAttribute("href")).toBe(`/issues/${KEY}/artifacts/plan-md`);
      expect(link, `${name}'s link is not in the page's ink`).toHaveClass(pageInkText);
      for (const token of linkText.split(" ")) {
        expect(link, `${name}'s link is in the application's link blue`).not.toHaveClass(token);
      }
    }
  } finally {
    unmount?.();
    restoreReads();
  }
});
