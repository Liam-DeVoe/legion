import { expect, test } from "bun:test";
import type { IssueSummary } from "../../api/types";
import {
  buildItems,
  clearPicked,
  columnFilterSpec,
  compareGroups,
  DURATION_BUCKETS,
  defaultView,
  durationBucket,
  facetCounts,
  isQueued,
  matchesFilters,
  matchesSearch,
  queuedInOrder,
  readSelection,
  readUrl,
  selectIssues,
  startNextWrites,
  UNCLAIMED_FACET,
  writeUrl,
} from "./issues-table-model";
import { embeddedGrammar } from "./issues-table-view";
import { currentAddress, legacyOpenLayout } from "./issues-views";

function issue(key: string, overrides: Partial<IssueSummary> = {}): IssueSummary {
  return {
    key,
    advances: [],
    advances_revision: 0,
    title: key,
    status: "todo",
    priority: null,
    rank: key,
    parent: null,
    blocked_by: [],
    assignee: null,
    claim: null,
    components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
    route: null,
    repo: null,
    resolved_repo: null,
    route_status: null,
    route_holder: null,
    route_status_reason: null,
    labels: [],
    open_asks: 0,
    progress: { tasks: null, children: null },
    last_seq: 1,
    primary_artifact_id: "spec",
    created_by: null,
    created_at: "2026-09-10T00:00:00Z",
    updated_at: "2026-09-10T00:00:00Z",
    closed_at: overrides.status === "done" ? "2026-09-10T00:00:00Z" : null,
    ...overrides,
  };
}
const items = buildItems(
  [
    issue("CORE-1", {
      title: "Implement searchable filters",
      labels: ["FrontEnd", "backend", "legion"],
    }),
    issue("CORE-2", { title: "Search backend", status: "testing", labels: ["backend"] }),
    issue("CORE-3", { title: "Keyboard navigation", labels: ["frontend"] }),
  ],
  (actor) => actor.id
);

test("labels require every picked value case-insensitively, alongside a Title filter", () => {
  const filters = [
    { id: "labels", value: ["frontend", "backend"] },
    { id: "title", value: "SEARCH" },
  ];
  expect(
    items.filter((item) => matchesFilters(item, filters)).map((item) => item.issue.key)
  ).toEqual(["CORE-1"]);
});

test("fuzzy search requires every word, tolerates one edit only for long words", () => {
  const searchable = items[0];
  if (!searchable) throw new Error("Search fixture has no rows");
  expect(matchesSearch(searchable, "filters implement")).toBe(true);
  expect(matchesSearch(searchable, "searchble filters")).toBe(true);
  expect(matchesSearch(searchable, "searchxble filters")).toBe(true);
  expect(matchesSearch(searchable, "searchxxble filters")).toBe(false);
  expect(matchesSearch(searchable, "filters missing")).toBe(false);
  expect(matchesSearch(searchable, "CORF")).toBe(false);
});

test("facet counts apply search and every other filter, not their own", () => {
  const filters = [
    { id: "status", value: ["todo"] },
    { id: "labels", value: ["backend"] },
  ];
  // The project's own spelling of a label is the facet, in the table as on the Board.
  expect([...facetCounts(items, "labels", filters, "search")]).toEqual([
    ["FrontEnd", 1],
    ["backend", 1],
    ["legion", 1],
  ]);
  expect([...facetCounts(items, "status", filters, "search")]).toEqual([
    ["todo", 1],
    ["testing", 1],
  ]);
  // A URL keeps the casing it carries — one spelling for the table and the Board — and the
  // matching is what is case-insensitive.
  expect(
    readUrl(new URLSearchParams("label=FrontEnd")).filters.find(({ id }) => id === "labels")?.value
  ).toEqual(["FrontEnd"]);
});

test("URL round trip preserves Board status and label params plus every table control", () => {
  const state = {
    ...defaultView,
    filters: [
      { id: "status", value: ["todo", "testing"] },
      { id: "labels", value: ["frontend", "backend"] },
      { id: "title", value: "a & b" },
    ],
    sorting: [
      { id: "priority", desc: true },
      { id: "key", desc: false },
    ],
    grouping: ["assignee"],
    search: "two words",
    page: 3,
    pageSize: 25,
  };
  const params = writeUrl(new URLSearchParams("unread=1"), state);
  expect(params.getAll("status")).toEqual(["todo", "testing"]);
  expect(params.getAll("label")).toEqual(["frontend", "backend"]);
  expect(params.get("unread")).toBe("1");
  expect(params.has("f.status")).toBe(false);
  expect(readUrl(params)).toEqual(state);
  expect(readUrl(new URLSearchParams("page=NaN&size=-1&sort=bogus&group=bogus&f.bogus=x"))).toEqual(
    defaultView
  );
  expect(readUrl(writeUrl(new URLSearchParams(), { ...defaultView, filters: [] })).filters).toEqual(
    []
  );
});

test("Start next ranks the selected rows in queue order and skips the ones already ahead", () => {
  const rows = (keys: string[]) =>
    buildItems(
      keys.map((key, index) => issue(key, { rank: String(index), labels: ["legion"] })),
      (actor) => actor.id
    );
  const queue = rows(["A", "B", "C", "D"]);
  expect(queuedInOrder(queue, ["C", "B"])).toEqual(["B", "C"]);
  expect(startNextWrites(queue, queuedInOrder(queue, ["C", "B"]))).toEqual([
    { key: "C", input: { rank: { before: "A" } } },
    { key: "B", input: { rank: { before: "C" } } },
  ]);
  expect(startNextWrites(queue, ["A", "B", "C", "D"])).toEqual([]);
  // The same function picks up an action a reload cut short: only the ranks still behind.
  expect(startNextWrites(rows(["C", "A", "B", "D"]), ["B", "C"])).toEqual([
    { key: "B", input: { rank: { before: "C" } } },
  ]);
  expect(startNextWrites(rows(["B", "C", "A", "D"]), ["B", "C"])).toEqual([]);
  expect(startNextWrites(rows(["C", "B"]), ["B", "C"])).toEqual([
    { key: "B", input: { rank: { before: "C" } } },
  ]);
  expect(startNextWrites(rows(["A", "C"]), ["B", "C"])).toEqual([
    { key: "C", input: { rank: { before: "A" } } },
  ]);
});

// The server matches a label on `lower(label)`, so an issue labelled `Legion` is queued exactly
// as one labelled `legion` is.
test("the legion label is matched case-insensitively", () => {
  expect(isQueued(issue("A", { labels: ["Legion"] }))).toBe(true);
  expect(isQueued(issue("A", { labels: ["LEGION"], status: "testing" }))).toBe(false);
  const rows = buildItems([issue("B", { labels: ["Legion"] })], (actor) => actor.id);
  expect(rows.find((row) => row.issue.key === "B")?.queue).toBe(1);
});

test("Nobody or lapsed picks unheld and takeable claims, and ?unclaimed=1 still sets it", () => {
  const held = issue("A", {
    claim: { actor: { kind: "session", id: "one" }, at: "2026-09-10T00:00:00Z" },
  });
  const rows = buildItems(
    [held, issue("B")],
    (actor) => actor.id,
    (row) => row.key === "B"
  );
  const picked = [{ id: "claim", value: [UNCLAIMED_FACET] }];
  expect(rows.filter((row) => matchesFilters(row, picked)).map((row) => row.issue.key)).toEqual([
    "B",
  ]);
  expect([...facetCounts(rows, "claim", [], "")]).toEqual([
    ["one", 1],
    ["Nobody", 1],
    [UNCLAIMED_FACET, 1],
  ]);
  const fromLink = readUrl(new URLSearchParams("unclaimed=1"));
  expect(fromLink.filters.find(({ id }) => id === "claim")?.value).toEqual([UNCLAIMED_FACET]);
  // And a view written back from it keeps the parameter the Board's own chip reads.
  expect(writeUrl(new URLSearchParams(), fromLink).get("unclaimed")).toBe("1");
});

// B1: a `?status=` this bundle does not know is still the reader's filter, under its own text.
test("an unknown status keeps its own text and its place in the picks", () => {
  const show = columnFilterSpec("status");
  if (show?.kind !== "picks" || show.show === undefined) throw new Error("the status filter");
  expect(show.show("bogus")).toBe("bogus");
  expect(show.show("Done")).toBe("Done");
  expect(show.show("done")).toBe("Done");
  expect(readUrl(new URLSearchParams("status=bogus")).filters).toEqual([
    { id: "status", value: ["bogus"] },
  ]);
});

// O1: the view the table opens with says nothing in a link, and comes back when read.
test("a shared link leaves out the default status set and reads it back", () => {
  const params = writeUrl(new URLSearchParams(), { ...defaultView, search: "rank" });
  expect(params.get("status")).toBeNull();
  expect(params.get("nofilter")).toBeNull();
  expect(readUrl(params).filters).toEqual(defaultView.filters);
  // An empty selection is still expressible, and still means every status.
  expect(writeUrl(new URLSearchParams(), { ...defaultView, filters: [] }).get("nofilter")).toBe(
    "1"
  );
  expect(readUrl(new URLSearchParams("nofilter=1")).filters).toEqual([]);
});

// O2: Key filters by what the reader types, not by a list of every issue.
test("the Key column filters by typed text", () => {
  const key = columnFilterSpec("key");
  expect(key?.kind).toBe("text");
  const item = buildItems([issue("CORE-12")], () => "")[0];
  if (item === undefined) throw new Error("no item");
  expect(matchesFilters(item, [{ id: "key", value: "re-1" }])).toBe(true);
  expect(matchesFilters(item, [{ id: "key", value: "CORE-9" }])).toBe(false);
});

// I3: a key reads as a number where it is one, and the empty group reads last.
test("groups order by number inside a name, with the empty group last", () => {
  expect(compareGroups("author", "agent-2", "agent-10")).toBeLessThan(0);
  expect(compareGroups("claim", "Nobody", "zoe")).toBeGreaterThan(0);
  expect(compareGroups("assignee", "Unassigned", "alice")).toBeGreaterThan(0);
});

// F8: nothing typed matches every row, before any of the work a query needs.
test("a blank search matches every row", () => {
  const item = buildItems([issue("CORE-1")], () => "")[0];
  if (item === undefined) throw new Error("no item");
  expect(matchesSearch(item, "")).toBe(true);
  expect(matchesSearch(item, "   ")).toBe(true);
});

test("a narrowing names its issues in the URL, lifts the status filter, and is read back", () => {
  const picked = selectIssues(
    new URLSearchParams("status=todo&page=4&q=rank"),
    ["CORE-1", "CORE-2"],
    "Finished last week"
  );
  expect(picked.get("only")).toBe("CORE-1,CORE-2");
  expect(picked.get("onlyLabel")).toBe("Finished last week");
  // Finished work shows: the status filter goes, `nofilter=1` keeps the empty pick, and the
  // reader lands on the first page. Every other control stays as it was.
  expect(picked.has("status")).toBe(false);
  expect(picked.get("nofilter")).toBe("1");
  expect(picked.has("page")).toBe(false);
  expect(picked.get("q")).toBe("rank");
  const selection = readSelection(picked);
  expect([...(selection?.keys ?? [])]).toEqual(["CORE-1", "CORE-2"]);
  expect(selection?.label).toBe("Finished last week");
  expect(readSelection(new URLSearchParams("only=CORE-1"))?.label).toBe("Selected issues");
  expect(readSelection(new URLSearchParams("only="))?.keys.size).toBe(0);
  expect(readSelection(new URLSearchParams("q=rank"))).toBeNull();
});

test("removing the chip gives the whole table back and leaves the other controls alone", () => {
  const cleared = clearPicked(new URLSearchParams("only=CORE-1&onlyLabel=Two&q=rank&page=4"));
  expect(cleared.has("only")).toBe(false);
  expect(cleared.has("onlyLabel")).toBe(false);
  expect(cleared.has("page")).toBe(false);
  expect(cleared.get("q")).toBe("rank");
  expect(readSelection(cleared)).toBeNull();
});

test("a write of the table keeps the narrowing the address carries", () => {
  const address = new URLSearchParams("only=CORE-1,CORE-2&onlyLabel=Finished&nofilter=1");
  // A control the reader moves while the table is narrowed keeps the narrowing.
  const written = writeUrl(address, { ...readUrl(address), search: "rank" });
  expect(written.get("only")).toBe("CORE-1,CORE-2");
  expect(written.get("onlyLabel")).toBe("Finished");
  expect(written.get("q")).toBe("rank");
  expect(readSelection(written)?.label).toBe("Finished");
});

test("an embedded table's prefix holds its own controls and leaves the bare ones alone", () => {
  const state = {
    ...defaultView,
    sorting: [{ id: "key", desc: true }],
    grouping: ["status"],
    search: "rank",
    page: 2,
    pageSize: 25,
    filters: [{ id: "labels", value: ["frontend"] }],
  };
  const grammar = embeddedGrammar("advances");
  const scoped = grammar.write(new URLSearchParams("sort=-priority&q=other"), state);
  expect(scoped.get("advances.sort")).toBe("-key");
  expect(scoped.get("advances.group")).toBe("status");
  expect(scoped.get("advances.q")).toBe("rank");
  expect(scoped.get("advances.page")).toBe("3");
  expect(scoped.get("advances.size")).toBe("25");
  expect(scoped.get("advances.nofilter")).toBe("1");
  expect(scoped.get("advances.label")).toBe("frontend");
  // It writes no view and no layout.
  expect(scoped.has("view")).toBe(false);
  expect(scoped.has("advances.view")).toBe(false);
  // The page's other keys are left as they were, and the Issues tab's grammar reads none of
  // the prefixed ones.
  expect(scoped.get("sort")).toBe("-priority");
  expect(scoped.get("q")).toBe("other");
  expect(grammar.read(scoped)).toEqual(state);
  expect(readUrl(scoped).sorting).toEqual([{ id: "priority", desc: true }]);
  expect(readUrl(scoped).search).toBe("other");
  expect(readUrl(scoped).filters).toEqual(defaultView.filters);
  // An embedded table left as it opens writes nothing at all.
  expect([...grammar.write(new URLSearchParams(), defaultView).keys()]).toEqual([]);
});

test("a link to the Legion queue's old tab opens Open narrowed to the queue", () => {
  for (const id of ["queue", "waiting"]) {
    const old = new URLSearchParams(`view=${id}&q=rank`);
    expect(currentAddress(old)?.toString()).toBe("q=rank&f.queue=Queued");
    const view = readUrl(old);
    expect(view.sorting).toEqual([{ id: "queue", desc: false }]);
    expect(view.filters).toContainEqual({ id: "queue", value: ["Queued"] });
    expect(view.filters).toContainEqual(defaultView.filters[0]);
  }
  // Today's addresses are left as they are.
  expect(currentAddress(new URLSearchParams("view=done"))).toBeUndefined();
  expect(currentAddress(new URLSearchParams("f.queue=Queued"))).toBeUndefined();
});

test("a duration falls in one bucket, each taking its lower bound and not its upper", () => {
  const hour = 3_600_000;
  expect(DURATION_BUCKETS).toEqual(["under-1h", "1h-1d", "1d-7d", "over-7d", "none"]);
  expect(durationBucket(undefined)).toBe("none");
  expect(durationBucket(hour - 1)).toBe("under-1h");
  expect(durationBucket(hour)).toBe("1h-1d");
  expect(durationBucket(24 * hour)).toBe("1d-7d");
  expect(durationBucket(7 * 24 * hour)).toBe("over-7d");
});

test("Open asks, Review rounds, Workers and Legion tree filter by what their cells hold", () => {
  const card = (roles: string[]) => ({
    card: { key: "CORE-1", roles } as never,
    column: "implementing" as const,
  });
  const [asked, quiet] = buildItems(
    [issue("CORE-1", { open_asks: 3 }), issue("CORE-2")],
    (actor) => actor.id
  ).map((item, index) =>
    index === 0
      ? {
          ...item,
          card: card(["implementer", "tester"]),
          facts: { review_rounds: 4, tree_depth: 0 } as never,
        }
      : { ...item, facts: { review_rounds: 1, tree_depth: 2 } as never }
  );
  if (asked === undefined || quiet === undefined) throw new Error("no items");
  const picks = (id: string, value: string[]) => (item: typeof asked) =>
    matchesFilters(item, [{ id, value }]);
  expect([asked, quiet].map(picks("asks", ["any"]))).toEqual([true, false]);
  expect([asked, quiet].map(picks("asks", ["none"]))).toEqual([false, true]);
  expect([asked, quiet].map(picks("review_rounds", ["3-or-more"]))).toEqual([true, false]);
  expect([asked, quiet].map(picks("review_rounds", ["1"]))).toEqual([false, true]);
  expect([asked, quiet].map(picks("roles", ["tester"]))).toEqual([true, false]);
  expect([asked, quiet].map(picks("roles", ["none"]))).toEqual([false, true]);
  expect([asked, quiet].map(picks("legion", ["root"]))).toEqual([true, false]);
  expect([asked, quiet].map(picks("legion", ["child"]))).toEqual([false, true]);
  // A pick of None survives the address: it is `none`, not an empty value.
  const written = writeUrl(new URLSearchParams(), {
    ...defaultView,
    filters: [...defaultView.filters, { id: "activity", value: ["none"] }],
  });
  expect(written.get("f.activity")).toBe("none");
  expect(readUrl(written).filters).toContainEqual({ id: "activity", value: ["none"] });
  const show = (id: string, value: string) => {
    const spec = columnFilterSpec(id);
    return spec?.kind === "picks" ? spec.show?.(value) : undefined;
  };
  expect(show("asks", "any")).toBe("With open asks");
  expect(show("asks", "none")).toBe("No open asks");
  expect(show("review_rounds", "3-or-more")).toBe("3 or more");
  expect(show("roles", "implementer")).toBe("Implementer");
  expect(show("waited", "1h-1d")).toBe("1 hour to 1 day");
  expect(show("activity", "held_children")).toBe("Waiting on child issues");
  expect(show("activity", "held_idle")).toBe("Idle");
  // Waiting on names what the card waits on, inside the queue or out of it.
  expect(
    ["ready", "slot", "blocked", "person", "none"].map((value) => show("waiting_reason", value))
  ).toEqual(["Ready to start", "Capacity", "A blocker", "A person", "None"]);
});

test("a link that picked open asks by count keeps its rows", () => {
  const [asked, quiet] = buildItems(
    [issue("CORE-1", { open_asks: 3 }), issue("CORE-2")],
    (actor) => actor.id
  );
  if (asked === undefined || quiet === undefined) throw new Error("no items");
  const asksOf = (query: string) =>
    readUrl(new URLSearchParams(query)).filters.find((filter) => filter.id === "asks")?.value;
  expect(asksOf("f.asks=0")).toEqual(["none"]);
  expect(asksOf("f.asks=1")).toEqual(["any"]);
  expect(asksOf("f.asks=12")).toEqual(["any"]);
  // Today's values are read as they are.
  expect(asksOf("f.asks=any")).toEqual(["any"]);
  expect(asksOf("f.asks=none")).toEqual(["none"]);
  const listed = (query: string) => {
    const value = asksOf(query);
    return [asked, quiet]
      .filter((item) => matchesFilters(item, [{ id: "asks", value }]))
      .map((item) => item.issue.key);
  };
  expect(listed("f.asks=0")).toEqual(["CORE-2"]);
  expect(listed("f.asks=3")).toEqual(["CORE-1"]);
});

test("the kept issue-view choice opens Open's table, its Board, or its Board by phase", () => {
  expect(legacyOpenLayout("list")).toEqual({ layout: "table" });
  expect(legacyOpenLayout("board")).toEqual({ layout: "board" });
  // The Legion view the Issues tab no longer has: its Board, grouped as that view was.
  expect(legacyOpenLayout("legion")).toEqual({ layout: "board", group: "legion_phase" });
  // A value this bundle does not know opens the table.
  expect(legacyOpenLayout("")).toEqual({ layout: "table" });
  expect(legacyOpenLayout("timeline")).toEqual({ layout: "table" });
});
