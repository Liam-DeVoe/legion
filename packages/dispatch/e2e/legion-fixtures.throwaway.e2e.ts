// THROWAWAY (dispatch://INFRA-3221, plan task T14): one row that holds the Legion fixtures this
// pull request ports from #3578's head 02a7b3c67e to the board as the server serves it now. It
// seeds ACME (`seedLegionFixture`, `seedLegionMetricsCorpus`) and WIDGETS
// (`seedLegionFactsFixture`), reads each board through the route, and asserts every card field,
// the header and the stored facts the spec names, then reads an items row of a closed issue and
// writes through the API to issues the seeds wrote. It is deleted before the stage's round ends,
// with its entries in `e2e/stub/real-stack-reasons.ts` and `e2e/row-map.md`.
import type { LegionBoardResponse as Board, LegionBoardCard as Card } from "../web/src/api/types";
import {
  createIssue,
  getIssue,
  getLegionBoard,
  getLegionWorkTimeItems,
  patchIssue,
  waitForLegionWorkTimeEval,
} from "./api";
import {
  issueFactsWait,
  LEGION_FACTS_PROJECT,
  seedLegionFactsFixture,
  waitForIssueFacts,
  writeRoleEvent,
} from "./legion-facts-seed";
import {
  LEGION_PROJECT,
  pushLegionObservation,
  seedLegionFixture,
  seedLegionMetricsCorpus,
  settledCounters,
  startFakeLegionDaemon,
} from "./legion-seed";
import { sql } from "./psql";
import { expect, test } from "./test";
import { asUser } from "./users";

/** The board's eleven columns, in the order the server sends them, with their labels. */
const COLUMNS = [
  ["queue", "Queue"],
  ["design", "Design"],
  ["planning", "Planning"],
  ["implementing", "Implementing"],
  ["testing", "Testing"],
  ["reviewing", "Reviewing"],
  ["accepting", "Accepting"],
  ["retro", "Retro"],
  ["merging", "Merging"],
  ["awaiting_person", "Waiting on a person"],
  ["held", "Held"],
];

/** Every card of the board by key, with the column that holds it. */
function cardsOf(board: Board): Map<string, { column: string; card: Card }> {
  return new Map(
    (board.columns ?? []).flatMap((column) =>
      (column.cards ?? []).map((card) => [card.key, { column: column.id, card }] as const)
    )
  );
}

/** The keys of one column's cards, in the board's order. */
function columnKeys(board: Board, id: string): string[] {
  return (board.columns?.find((column) => column.id === id)?.cards ?? []).map((card) => card.key);
}

/** `at` (an RFC 3339 time) lies within a minute of `expected` (milliseconds since the epoch). */
function near(at: string | null | undefined, expected: number): boolean {
  return at != null && Math.abs(Date.parse(at) - expected) < 60_000;
}

/** The project's newest `legion_reads` row as `ok|error|read_at`, which the board's header reads. */
function newestRead(project: string): Promise<string> {
  return sql(
    `SELECT ok::text || '|' || coalesce(error, '') || '|' || to_char(read_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
       FROM legion_reads WHERE project = '${project}' ORDER BY read_at DESC, id DESC LIMIT 1`
  );
}

/** The board, read between two reads of the newest `legion_reads` row that agree, so the header's
 *  read state can be held to that row although the sampler writes a failed read each minute. */
async function boardWithItsRead(project: string): Promise<{ board: Board; read: string }> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    const before = await newestRead(project);
    const board = await getLegionBoard(project);
    if ((await newestRead(project)) === before) return { board, read: before };
  }
  throw new Error(`a sampler pass wrote a read of ${project} during each of five board reads`);
}

/** The header's read state is the newest read: when it was made, whether it succeeded, and why it
 *  did not. */
function expectReadState(board: Board, read: string): void {
  const [ok, error, at] = read.split("|");
  expect(board.header.read_ok).toBe(ok === "true");
  expect(board.header.read_error).toBe(ok === "true" ? null : error);
  expect(Date.parse(board.header.read_at ?? "")).toBe(Date.parse(at));
}

// SLOW(>=2s): the row waits for the facts consumer's rows of five issues and for the eval
// refresher's pass that writes ACME's catalogue, each up to a minute, beside two seeded boards.
test("the ported Legion fixtures seed ACME's and WIDGETS' boards with every card field", async ({
  browser,
}) => {
  test.setTimeout(240_000);
  const daemon = await startFakeLegionDaemon();
  const context = await asUser(browser, "alice");
  try {
    const seededAt = Date.now();
    await seedLegionFixture(daemon.url);
    await seedLegionMetricsCorpus();
    // A child of ACME-1 that its tree has not started: `admitted`, no slot, in no line. The board
    // counts it on ACME-1's card and draws no card for it.
    await sql(
      `INSERT INTO issues (key, project_key, number, title, status, priority, parent_key, created_by, rank)
         VALUES ('ACME-8', '${LEGION_PROJECT}', 8, 'A child not started', 'todo', 2, 'ACME-1', '{"kind":"user","id":"alice"}', 'Zb')`,
      ...settledCounters(LEGION_PROJECT)
    );
    await pushLegionObservation("ACME-8", "phase", '"admitted"');
    const fixture = await seedLegionFactsFixture(daemon.url);
    // Role events on three open WIDGETS issues: a root, a child of the root WIDGETS-4, and a card
    // in review, so the facts consumer writes each a row.
    await writeRoleEvent("WIDGETS-1", "implementer", "70 minutes");
    await writeRoleEvent("WIDGETS-5", "implementer", "50 minutes");
    await writeRoleEvent("WIDGETS-14", "reviewer", "60 minutes");

    // ACME's board, as the fixture comment says it is.
    const acmeRead = await boardWithItsRead(LEGION_PROJECT);
    const acme = acmeRead.board;
    expect((acme.columns ?? []).map((column) => [column.id, column.label])).toEqual(COLUMNS);
    expect(columnKeys(acme, "queue")).toEqual(["ACME-4", "ACME-3", "ACME-5"]);
    expect(columnKeys(acme, "implementing")).toEqual(["ACME-1"]);
    expect(columnKeys(acme, "awaiting_person")).toEqual(["ACME-2"]);
    const acmeCards = cardsOf(acme);
    expect([...acmeCards.keys()].sort()).toEqual([
      "ACME-1",
      "ACME-2",
      "ACME-3",
      "ACME-4",
      "ACME-5",
    ]);
    expect(acme.header.slots_in_use).toBe(2);
    expect(acme.header.slot_cap).toBe(4);
    expect(acme.header.trees).toBe(2);
    expect(acme.header.queue_length).toBe(3);
    expectReadState(acme, acmeRead.read);

    const card = (key: string): Card => {
      const found = acmeCards.get(key)?.card;
      if (found === undefined) throw new Error(`ACME's board has no card ${key}`);
      return found;
    };
    const implementing = card("ACME-1");
    expect(implementing).toMatchObject({
      blocked_by: [],
      children: 1,
      phase: "implementing",
      priority: 0,
      roles: ["implementer"],
      slot: 0,
      slot_use: "working",
      title: "Ship the widgets",
      tree: "ACME-1",
      waiting: null,
      waiting_on: null,
      waiting_place: null,
    });
    expect(near(implementing.phase_since, seededAt - 2 * 3_600_000)).toBe(true);

    const person = card("ACME-2");
    expect(person).toMatchObject({
      blocked_by: [],
      children: 0,
      phase: "awaiting_person",
      roles: [],
      slot: 1,
      slot_use: "held_person",
      waiting: { blocked_by: [], reason: "person" },
      waiting_on: { answered_at: null, kind: "ask" },
      waiting_place: null,
    });
    expect(Date.parse(person.waiting?.since ?? "")).toBe(Date.parse("2026-11-03T04:19:00Z"));
    expect(Date.parse(person.waiting_on?.since ?? "")).toBe(Date.parse("2026-11-03T04:19:00Z"));
    expect(near(person.phase_since, seededAt - 45 * 60_000)).toBe(true);

    for (const [key, place, ago] of [
      ["ACME-4", 1, 80],
      ["ACME-3", 2, 90],
    ] as const) {
      const queued = card(key);
      expect(queued).toMatchObject({
        blocked_by: [],
        children: 0,
        phase: "admitted",
        roles: [],
        slot: null,
        slot_use: null,
        waiting: { blocked_by: [], reason: "ready" },
        waiting_on: null,
        waiting_place: place,
      });
      expect(near(queued.phase_since, seededAt - ago * 60_000)).toBe(true);
      expect(queued.waiting?.since).toBe(queued.phase_since);
    }

    const blocked = card("ACME-5");
    expect(blocked).toMatchObject({
      blocked_by: ["ACME-1"],
      children: 0,
      phase: "admitted",
      slot_use: null,
      waiting: { blocked_by: ["ACME-1"], reason: "blocked" },
      waiting_on: null,
      waiting_place: null,
    });
    expect(blocked.waiting?.since).toBe(blocked.phase_since);

    // WIDGETS' board: every column holds a card, and each card the state the fixture declares.
    const widgetsRead = await boardWithItsRead(LEGION_FACTS_PROJECT);
    const widgets = widgetsRead.board;
    expect((widgets.columns ?? []).map((column) => [column.id, column.label])).toEqual(COLUMNS);
    for (const column of widgets.columns ?? []) {
      expect(column.cards?.length ?? 0, `WIDGETS' ${column.id} column`).toBeGreaterThan(0);
    }
    const widgetsCards = cardsOf(widgets);
    expect([...widgetsCards.keys()].sort()).toEqual([...fixture.keys].sort());
    expect(widgets.header.slots_in_use).toBe(7);
    expect(widgets.header.slot_cap).toBe(8);
    expect(widgets.header.trees).toBe(7);
    expect(widgets.header.queue_length).toBe(4);
    expectReadState(widgets, widgetsRead.read);
    expect(columnKeys(widgets, "queue")).toEqual([
      "WIDGETS-9",
      "WIDGETS-8",
      "WIDGETS-23",
      "WIDGETS-11",
    ]);

    const uses = new Map<string, string>();
    for (const [use, keys] of Object.entries(fixture.slotUse)) {
      expect(keys.length, `the cards the fixture declares ${use}`).toBeGreaterThan(0);
      for (const key of keys) uses.set(key, use);
    }
    const reasons = new Map<string, string>();
    for (const [reason, keys] of Object.entries(fixture.waiting)) {
      expect(keys.length, `the cards the fixture declares waiting for ${reason}`).toBeGreaterThan(
        0
      );
      for (const key of keys) reasons.set(key, reason);
    }
    const places: Record<string, number> = {
      "WIDGETS-9": 1,
      "WIDGETS-8": 2,
      "WIDGETS-23": 3,
      "WIDGETS-10": 4,
      "WIDGETS-24": 5,
    };
    for (const [key, { card: one }] of widgetsCards) {
      expect(one.slot_use, `the use of ${key}'s own slot`).toBe(uses.get(key) ?? null);
      expect(one.waiting?.reason ?? null, `why ${key} waits`).toBe(reasons.get(key) ?? null);
      expect(one.waiting_place, `${key}'s place in the line`).toBe(places[key] ?? null);
      expect(one.phase_since, `${key}'s phase entry`).not.toBeNull();
      expect(one.children, `${key}'s children not started`).toBe(0);
    }
    expect(widgetsCards.get("WIDGETS-1")?.card.roles).toEqual(["implementer"]);
    expect(widgetsCards.get("WIDGETS-22")?.card.roles).toEqual(["reviewer"]);
    expect(widgetsCards.get("WIDGETS-2")?.card.roles).toEqual([]);
    expect(widgetsCards.get("WIDGETS-5")?.column).toBe("implementing");
    expect(widgetsCards.get("WIDGETS-5")?.card.tree).toBe("WIDGETS-4");
    expect(widgetsCards.get("WIDGETS-7")?.card.tree).toBe("WIDGETS-6");
    expect(widgetsCards.get("WIDGETS-11")?.card).toMatchObject({
      blocked_by: ["WIDGETS-1"],
      waiting: { blocked_by: ["WIDGETS-1"], reason: "blocked" },
    });
    for (const [key, kind] of [
      ["WIDGETS-3", "ask"],
      ["WIDGETS-12", "gate"],
    ] as const) {
      const waiting = widgetsCards.get(key)?.card;
      expect(waiting?.waiting_on).toMatchObject({ answered_at: null, kind });
      expect(Date.parse(waiting?.waiting?.since ?? "")).toBe(Date.parse("2026-11-09T08:00:00Z"));
    }

    // The stored facts: the consumer's rows of the three WIDGETS issues and ACME's two closed
    // ones, then the board's `issues` map, which carries the open issues with Legion work.
    await waitForIssueFacts(
      ["WIDGETS-1", "WIDGETS-5", "WIDGETS-14", "ACME-6", "ACME-7"],
      issueFactsWait
    );
    const facts = (await getLegionBoard(LEGION_FACTS_PROJECT)).issues ?? {};
    expect(Object.keys(facts).sort()).toEqual(["WIDGETS-1", "WIDGETS-14", "WIDGETS-5"]);
    const root = facts["WIDGETS-1"];
    expect(root.hours_by_phase?.implementing ?? 0).toBeGreaterThan(0);
    expect(root.work_hours).toBeGreaterThan(0);
    expect(root.review_rounds).toBe(0);
    expect(root.root_key).toBe("WIDGETS-1");
    expect(root.tree_depth).toBe(0);
    expect(root.closed_at).toBeNull();
    expect(root.work_first_at).not.toBeNull();
    expect(Date.parse(root.work_first_at ?? "")).toBeLessThanOrEqual(Date.now());
    expect(Date.parse(root.work_first_at ?? "")).toBeGreaterThan(seededAt - 3 * 3_600_000);
    expect(facts["WIDGETS-5"]).toMatchObject({
      closed_at: null,
      root_key: "WIDGETS-4",
      tree_depth: 1,
    });
    expect(facts["WIDGETS-5"].work_hours).toBeGreaterThan(0);
    expect(facts["WIDGETS-14"].review_rounds).toBeGreaterThanOrEqual(1);
    expect(facts["WIDGETS-14"].hours_by_phase?.reviewing ?? 0).toBeGreaterThan(0);
    // ACME's closed issues are in no tree a card stands in, so the map carries neither.
    const acmeFacts = (await getLegionBoard(LEGION_PROJECT)).issues ?? {};
    expect(acmeFacts["ACME-6"]).toBeUndefined();
    expect(acmeFacts["ACME-7"]).toBeUndefined();

    // The items route serves them, once the refresher has written ACME's catalogue.
    await waitForLegionWorkTimeEval(LEGION_PROJECT);
    const items = await getLegionWorkTimeItems(LEGION_PROJECT, { row_kind: "issue" });
    const child = (items.items ?? []).find((row) => row.row_key === "ACME-7");
    expect(child, "the items row of the closed ACME-7").toBeDefined();
    expect(child?.row_kind).toBe("issue");
    expect(child?.cols?.closed_at).not.toBeNull();
    expect(Date.parse(String(child?.cols?.closed_at))).toBe(Date.parse(child?.measured_at ?? ""));
    expect(near(String(child?.cols?.closed_at), seededAt - 24 * 3_600_000)).toBe(true);
    expect(child?.cols).toMatchObject({ review_rounds: 1, root_key: "ACME-6", tree_depth: 1 });
    expect(Number(child?.cols?.work_hours)).toBeGreaterThan(0);
    expect(child?.cols?.work_first_at).not.toBeNull();
    expect((items.items ?? []).some((row) => row.row_key === "ACME-6")).toBe(true);

    // Every seeded issue's last_seq is its newest event's seq, so the API's next write appends
    // after it, and ACME's next issue takes the number after ACME-8.
    for (const key of [
      ...["ACME-1", "ACME-2", "ACME-3", "ACME-4", "ACME-5", "ACME-6", "ACME-7", "ACME-8"],
      ...fixture.keys,
    ]) {
      const newest = Number(
        await sql(`SELECT coalesce(max(seq), 0) FROM events WHERE issue_key = '${key}'`)
      );
      expect((await getIssue(key)).last_seq, `${key}'s last_seq`).toBe(newest);
    }
    expect((await getIssue("ACME-7")).last_seq).toBe(5);
    expect((await getIssue("WIDGETS-1")).last_seq).toBe(1);
    // A closed issue takes a reopening status and no priority (issue_patch.go), so ACME-7 is
    // reopened; WIDGETS-1 is open and takes a priority.
    await patchIssue("ACME-7", { status: "todo" });
    expect((await getIssue("ACME-7")).last_seq).toBeGreaterThan(5);
    await patchIssue("WIDGETS-1", { priority: 3 });
    expect((await getIssue("WIDGETS-1")).last_seq).toBeGreaterThan(1);
    expect(
      (await createIssue({ project: LEGION_PROJECT, title: "Created after the seed" })).key
    ).toBe("ACME-9");
    expect(
      (await createIssue({ project: LEGION_FACTS_PROJECT, title: "Created after the seed" })).key
    ).toBe("WIDGETS-25");

    // The Issues tab draws ACME's seeded issues.
    const page = await context.newPage();
    await page.goto(`/projects/${LEGION_PROJECT}/issues`);
    await expect(page.locator('[data-issue-row="ACME-1"]')).toBeVisible();
    await expect(page.locator('[data-issue-row="ACME-5"]')).toBeVisible();
  } finally {
    await context.close();
    await daemon.close();
  }
});
