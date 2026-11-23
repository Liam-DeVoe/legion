import { afterEach, beforeEach, test as bunTest, expect, spyOn } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

import {
  errorFrame,
  type FakeLiveSocket,
  type FakeLiveSockets,
  installFakeLiveSockets,
  snapshotFrame,
} from "../../__tests__/live-socket-harness";
import { api } from "../../api/client";
import type { EvalKind } from "../../api/queries";
import type {
  DeliveryLiveDelta,
  EvalListItem,
  EvalListResponse,
  LegionSetting,
  LiveServerFrame,
  LiveSubscribe,
} from "../../api/types";
import * as sections from "./EvalsSection";
import { EvalsTab } from "./EvalsTab";

// The state table of a section (dispatch://INFRA-2918/artifact/plan-md, "The state table of a
// section"): each row a test driven by a fake live socket that sends the frames in the order the
// row names. The rows a later task builds are `test.todo`, naming that task.

const DAEMON: LegionSetting = {
  created_by: { id: "alice", kind: "user" },
  project: "ACME",
  state_url: "http://127.0.0.1:9",
  updated_at: "2026-10-01T00:00:00Z",
};

const WINDOW = { days: 29, from: "2026-10-26T00:00:00Z", to: "2026-11-24T00:00:00Z" };
const COMPARE = { days: 29, from: "2026-09-27T00:00:00Z", to: "2026-10-26T00:00:00Z" };

function evalItem(slug: string, kind: EvalKind): EvalListItem {
  const written = { id: "alice", kind: "user" } as const;
  return {
    definition: {
      answer: "",
      created_at: "2026-10-01T00:00:00Z",
      decision: "",
      direction: "",
      doc_block: "",
      eval_id: `ACME/${slug}`,
      failure_modes: [],
      issue_key: "",
      kind,
      links: [],
      name: slug,
      number_rule: "",
      owner_role: "",
      person: "alice",
      project_key: "ACME",
      question: "",
      replaced_by: "",
      slug,
      source: kind === "experiment" ? "logged" : "computed",
      state: "active",
      target: null,
      target_inclusive: false,
      target_judges: [],
      unit: "count",
      version: 1,
      written_by: written,
      written_via: "register",
    },
    id: `ACME/${slug}`,
    owner: { person: "alice", role: "", written_by: written },
    series: [],
    verdict: null,
  };
}

/** An evals view's answer holding `items`, of `total` evals in all. */
function answer(items: EvalListItem[], total = items.length, offset = 0): EvalListResponse {
  return {
    compare: COMPARE,
    freshness: { refresher_age_seconds: null, rows_updated_at: null },
    items,
    limit: 200,
    offset,
    total,
    window: WINDOW,
  };
}

/** One eval of each kind, the answer each view gives a tab with every section drawn. */
const ANSWERS: Record<EvalKind, EvalListResponse> = {
  ci: answer([evalItem("pr-ci", "ci")]),
  delivery: answer([evalItem("deploy-frequency", "delivery")]),
  experiment: answer([evalItem("widget-size", "experiment")]),
  legion: answer([evalItem("legion-work-time", "legion")]),
};

/** A delta of the view `sub` that adds or changes `upsert`. */
function deltaFrame(sub: string, upsert: EvalListItem[], seq = "2"): DeliveryLiveDelta {
  return {
    changed_at: null,
    counts: {},
    items: { items: { remove: [], upsert } },
    maps: {},
    revs: {
      items: { items: Object.fromEntries(upsert.map((item) => [item.id, "00000000000000aa"])) },
    },
    seq,
    set: {},
    sub,
    type: "delta",
  };
}

let sockets: FakeLiveSockets;
const spies: { mockRestore: () => void }[] = [];
beforeEach(() => {
  sockets = installFakeLiveSockets();
});
afterEach(() => {
  sockets.restore();
  for (const spy of spies.splice(0)) spy.mockRestore();
});

/** A row of this file, its page unmounted inside its body rather than in `afterEach`, so no
 *  timer of the live socket fires between the two (DeliveryPage.test.tsx gives the reason). */
const test = (name: string, body: () => Promise<void>): void =>
  bunTest(name, async () => {
    try {
      await body();
    } finally {
      cleanup();
    }
  });

/** The tab at `search`, its project's daemon read answering `daemon` ("pending" never answers). */
function renderTab(daemon: LegionSetting | null | "pending" = DAEMON, search = "?window=28d") {
  const getLegionDaemon = spyOn(api, "getLegionDaemon").mockImplementation(() =>
    daemon === "pending" ? Promise.withResolvers<never>().promise : Promise.resolve(daemon)
  );
  const listIssues = spyOn(api, "listIssues");
  spies.push(getLegionDaemon, listIssues);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <MemoryRouter initialEntries={[`/projects/ACME/evals${search}`]}>
      <QueryClientProvider client={queryClient}>
        <EvalsTab project="ACME" />
      </QueryClientProvider>
    </MemoryRouter>
  );
  return { getLegionDaemon, listIssues };
}

/** The page's live socket, once it has opened one, as Dispatch accepts it. */
async function openSocket(): Promise<FakeLiveSocket> {
  await waitFor(() => expect(sockets.sockets.length).toBeGreaterThan(0));
  const socket = sockets.latest();
  act(() => socket.open());
  return socket;
}

/** The query of an evals view's subscribe frame. */
function queryOf(frame: LiveSubscribe): URLSearchParams {
  return new URLSearchParams(frame.view.kind === "evals" ? frame.view.query : "");
}

/** The evals views `socket` holds now: subscribed and not unsubscribed since. */
function heldViews(socket: FakeLiveSocket): LiveSubscribe[] {
  const dropped = new Set(socket.unsubscribes().map((frame) => frame.sub));
  return socket
    .subscribes()
    .filter((frame) => frame.view.kind === "evals" && !dropped.has(frame.sub));
}

/** The newest subscribe frame of the `kind` view at `offset`, once the page has sent one. */
function subscription(socket: FakeLiveSocket, kind: EvalKind, offset = 0): Promise<LiveSubscribe> {
  return waitFor(() => {
    const frame = socket
      .subscribes()
      .filter(
        (sent) =>
          sent.view.kind === "evals" &&
          queryOf(sent).get("kind") === kind &&
          Number(queryOf(sent).get("offset") ?? 0) === offset
      )
      .at(-1);
    if (frame === undefined) throw new Error(`no ${kind} view at offset ${offset} was subscribed`);
    return frame;
  });
}

/** Dispatch sends `frame` to the page. */
function serve(socket: FakeLiveSocket, frame: LiveServerFrame): void {
  act(() => socket.receive(frame));
}

/** Dispatch answers the `kind` view's first page with `body`. */
async function answerView(
  socket: FakeLiveSocket,
  kind: EvalKind,
  body = ANSWERS[kind],
  seq = "1"
): Promise<string> {
  const { sub } = await subscription(socket, kind);
  serve(socket, snapshotFrame(sub, body, { seq }));
  return sub;
}

function region(name: string): HTMLElement {
  return screen.getByRole("region", { name });
}

/** The evals a section lists, by slug. */
function listed(name: string): string[] {
  return [...region(name).querySelectorAll("[data-eval]")].map(
    (row) => row.getAttribute("data-eval") ?? ""
  );
}

const stateRows: [string, () => Promise<void>][] = [
  [
    "subscribed | own snapshot | painted: the section's numbers",
    async () => {
      renderTab();
      const socket = await openSocket();
      expect(region("CI health")).toHaveAttribute("data-state", "subscribed");
      await answerView(socket, "ci");
      expect(region("CI health")).toHaveAttribute("data-state", "painted");
      expect(listed("CI health")).toEqual(["pr-ci"]);
    },
  ],
  [
    "subscribed | another section's snapshot | subscribed: this section's loading line; the other section paints",
    async () => {
      renderTab();
      const socket = await openSocket();
      await answerView(socket, "ci");
      expect(listed("CI health")).toEqual(["pr-ci"]);
      expect(region("Legion measures")).toHaveAttribute("data-state", "subscribed");
      expect(within(region("Legion measures")).getByRole("status").textContent).toBe(
        "Legion measures: loading…"
      );
      expect(listed("Legion measures")).toEqual([]);
    },
  ],
  [
    "subscribed | own error frame | failed: the section's error line with Retry; others unaffected",
    async () => {
      const user = userEvent.setup();
      renderTab();
      const socket = await openSocket();
      const { sub } = await subscription(socket, "ci");
      serve(socket, errorFrame(sub, "INTERNAL", "the read failed"));
      const failed = region("CI health");
      await waitFor(() => expect(failed).toHaveAttribute("data-state", "failed"));
      expect(within(failed).getByRole("alert").textContent).toContain("CI health: could not load.");
      await answerView(socket, "delivery");
      expect(region("Deploys and lead time")).toHaveAttribute("data-state", "painted");
      expect(listed("Deploys and lead time")).toEqual(["deploy-frequency"]);

      // Retry subscribes the refused view again, and its answer paints the section.
      await user.click(within(failed).getByRole("button", { name: "Retry" }));
      const again = await waitFor(() => {
        const frame = socket.subscribed("evals");
        expect(frame.sub).not.toBe(sub);
        expect(queryOf(frame).get("kind")).toBe("ci");
        return frame;
      });
      serve(socket, snapshotFrame(again.sub, ANSWERS.ci));
      await waitFor(() => expect(region("CI health")).toHaveAttribute("data-state", "painted"));
    },
  ],
  [
    "painted | own delta | painted: only this section re-renders (render count of the others unchanged)",
    async () => {
      const rendered = spyOn(sections, "EvalsSection");
      spies.push(rendered);
      renderTab();
      const socket = await openSocket();
      const subs = {
        ci: await answerView(socket, "ci"),
        delivery: await answerView(socket, "delivery"),
        experiment: await answerView(socket, "experiment"),
        legion: await answerView(socket, "legion"),
      };
      await waitFor(() =>
        expect(
          [...document.querySelectorAll("[data-section]")].map((section) =>
            section.getAttribute("data-state")
          )
        ).toEqual(["painted", "painted", "painted", "painted", "closed", "painted"])
      );
      const counts = () => {
        const byId: Record<string, number> = {};
        for (const [props] of rendered.mock.calls) byId[props.id] = (byId[props.id] ?? 0) + 1;
        return byId;
      };
      const before = counts();
      serve(socket, deltaFrame(subs.ci, [evalItem("staging-e2e", "ci")]));
      await waitFor(() => expect(listed("CI health")).toEqual(["pr-ci", "staging-e2e"]));
      const after = counts();
      expect(after.ci).toBeGreaterThan(before.ci ?? 0);
      expect({ ...after, ci: 0 }).toEqual({ ...before, ci: 0 });
    },
  ],
  [
    "painted | header window change | subscribed: a new view; the old one is unsubscribed",
    async () => {
      const user = userEvent.setup();
      renderTab();
      const socket = await openSocket();
      const old = await answerView(socket, "ci");
      expect(region("CI health")).toHaveAttribute("data-state", "painted");

      await user.selectOptions(screen.getByLabelText("Window"), "7d");
      const next = await waitFor(() => {
        const frame = socket.subscribed("evals");
        expect(frame.view).toMatchObject({ window: { preset: 7 } });
        return frame;
      });
      await waitFor(() => expect(socket.unsubscribes().map((frame) => frame.sub)).toContain(old));
      expect(
        heldViews(socket).every(
          (frame) =>
            frame.view.kind === "evals" &&
            "preset" in frame.view.window &&
            frame.view.window.preset === 7
        )
      ).toBe(true);
      expect(next.sub).not.toBe(old);
      expect(region("CI health")).toHaveAttribute("data-state", "subscribed");
    },
  ],
  [
    "painted | socket reconnect | painted: resume with the view's revisions, no blank",
    async () => {
      renderTab();
      const socket = await openSocket();
      const sub = await answerView(socket, "ci", ANSWERS.ci, "5");
      act(() => socket.restart());
      // Lost, and not yet back: the section keeps what it drew.
      expect(region("CI health")).toHaveAttribute("data-state", "painted");
      expect(listed("CI health")).toEqual(["pr-ci"]);

      await waitFor(() => expect(sockets.sockets.length).toBe(2));
      const next = sockets.latest();
      act(() => next.open());
      const resumed = next.subscribes().find((frame) => frame.sub === sub);
      expect(resumed?.resume).toEqual({
        revs: { counts: {}, fields: {}, items: {}, maps: {} },
        seq: "5",
      });
      expect(listed("CI health")).toEqual(["pr-ci"]);
    },
  ],
  [
    'subscribed | sources answer "no daemon" | hidden (Legion): no Legion section, no Legion card',
    async () => {
      renderTab(null);
      const socket = await openSocket();
      await waitFor(() =>
        expect(screen.queryByRole("region", { name: "Legion measures" })).toBeNull()
      );
      await answerView(socket, "delivery");
      expect(listed("Target cards")).toEqual(["deploy-frequency"]);
      expect(
        heldViews(socket)
          .map((frame) => queryOf(frame).get("kind"))
          .sort()
      ).toEqual(["ci", "delivery", "experiment"]);
    },
  ],
  [
    "painted | the repository filter set | subscribed: the stored views with `repo` (no narrowed view)",
    async () => {
      const user = userEvent.setup();
      renderTab();
      const socket = await openSocket();
      const old = await answerView(socket, "delivery");

      await user.type(screen.getByLabelText("Repository"), "acme/widgets{Enter}");
      await waitFor(() => expect(socket.unsubscribes().map((frame) => frame.sub)).toContain(old));
      const views = heldViews(socket);
      expect(views.map((frame) => queryOf(frame).get("kind")).sort()).toEqual([
        "ci",
        "delivery",
        "experiment",
        "legion",
      ]);
      for (const frame of views) {
        expect(queryOf(frame).getAll("repo")).toEqual(["acme/widgets"]);
        expect([...new Set(queryOf(frame).keys())].sort()).toEqual([
          "compare_from",
          "compare_to",
          "kind",
          "limit",
          "repo",
        ]);
      }
      expect(region("Deploys and lead time")).toHaveAttribute("data-state", "subscribed");
    },
  ],
];

bunTest.each(stateRows)("%s", async (_row, run) => {
  try {
    await run();
  } finally {
    cleanup();
  }
});

/** The rows of the table a later task builds, each with the task that builds it. Their bodies run
 *  only under `bun test --todo`, where each says which task it waits for. */
const laterRows: [string, string][] = [
  [
    "painted | a facet or the search set (header or timeline) | subscribed: the delivery view again with the facets; the narrowed cards and charts, and the window-and-repository-only mark",
    "T30",
  ],
  [
    "painted | the brush set on the timeline | subscribed: the cards' views over the brushed days; the cards name the days",
    "T14",
  ],
  [
    'subscribed | sources answer "delivery off" | hidden (delivery, CI)',
    "T24, the per-project delivery settings read",
  ],
  [
    'subscribed | sources answer "no settings row" | hidden (delivery, CI); the header\'s "Delivery: not set up" line with its link',
    "T24",
  ],
  [
    "opened from an address with a facet | the unnarrowed snapshots land | painted: every section; the narrowed cards and charts show a loading mark over their unnarrowed values",
    "T30",
  ],
  [
    "painted, the narrowed cards and charts loading | the narrowed delivery view's snapshot | painted: the narrowed values and the facets named; the unnarrowed delivery view unsubscribed",
    "T30",
  ],
  [
    "painted, a declared eval or open panel | the evals view's delta carries a new definition.version | re-reading: the detail read again; then the new method, charts and answer (D11a)",
    "T16 and T20",
  ],
  ["closed (timeline) | open | subscribed with limit=200", "T14"],
  [
    "painted (timeline) | scrolled to the end | growing: same sub, limit + 200, a resume with the held revisions; past 500 as below it",
    "T14",
  ],
  ["painted (timeline) | limit reaches total | painted: no further growth", "T14"],
];

for (const [row, task] of laterRows) {
  bunTest.todo(`${row} (waits for ${task})`, () => {
    throw new Error(`${task} builds this row`);
  });
}

test("the four views open at once, with the Legion daemon read in parallel", async () => {
  const { getLegionDaemon } = renderTab("pending");
  const socket = await openSocket();
  await waitFor(() =>
    expect(
      heldViews(socket)
        .map((frame) => queryOf(frame).get("kind"))
        .sort()
    ).toEqual(["ci", "delivery", "experiment", "legion"])
  );
  expect(getLegionDaemon).toHaveBeenCalledWith("ACME");
  for (const frame of heldViews(socket)) {
    expect(frame.view).toMatchObject({ kind: "evals", project: "ACME", window: { preset: 28 } });
    expect(queryOf(frame).get("limit")).toBe("200");
    expect(queryOf(frame).has("offset")).toBe(false);
  }
  // Nothing has answered: every section that reads a view says so, and the timeline is closed.
  expect(
    [...document.querySelectorAll("[data-section]")].map((section) =>
      section.getAttribute("data-state")
    )
  ).toEqual(["subscribed", "subscribed", "subscribed", "subscribed", "closed", "subscribed"]);
});

test("a view whose total exceeds 200 subscribes the next offset as a second view of the same section", async () => {
  renderTab();
  const socket = await openSocket();
  await answerView(
    socket,
    "ci",
    answer([evalItem("pr-ci", "ci"), evalItem("staging-e2e", "ci")], 250)
  );
  const second = await subscription(socket, "ci", 200);
  expect(queryOf(second).get("limit")).toBe("200");
  serve(socket, snapshotFrame(second.sub, answer([evalItem("production-deploy", "ci")], 250, 200)));
  await waitFor(() =>
    expect(listed("CI health")).toEqual(["pr-ci", "staging-e2e", "production-deploy"])
  );
  // 250 evals are two pages: no third view.
  expect(socket.subscribes().filter((frame) => queryOf(frame).get("offset") === "400")).toEqual([]);
});

test("the tab lists no issues", async () => {
  const { listIssues } = renderTab();
  const socket = await openSocket();
  for (const kind of ["delivery", "legion", "ci", "experiment"] as const) {
    await answerView(socket, kind);
  }
  await waitFor(() =>
    expect(
      [...document.querySelectorAll("[data-section]")].map((section) =>
        section.getAttribute("data-state")
      )
    ).toEqual(["painted", "painted", "painted", "painted", "closed", "painted"])
  );
  expect(document.querySelector("[data-issue-row]")).toBeNull();
  expect(screen.queryByRole("table", { name: /issue/i })).toBeNull();
  expect(listIssues).not.toHaveBeenCalled();
});
