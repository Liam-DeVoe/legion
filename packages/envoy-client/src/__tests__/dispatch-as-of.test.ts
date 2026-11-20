import { afterAll, beforeAll, describe, expect, setSystemTime, test } from "bun:test";
import { absentFieldWords, LEFT_OUT } from "../dispatch-commands/as-of";
import {
  ASK_OPENED,
  callsAtCutProblems,
  HOLDER_TITLE,
  leftOutParts,
  OWNER,
  READ_FORMS,
  type Run,
  routesCalled,
  startAsOfStandIn,
} from "./as-of-stand-in";
import { AT } from "./dispatch-command-surface-responses";

// Each read command of spec R3 run with and without `--as-of` against a stand-in Dispatch that
// records every request (dispatch://INFRA-4620, plan task t14). At the cut a command sends the cut
// as `as_of` to every route of kind "answers" it calls, calls no route that holds no history,
// names in its `Left out at <cut>:` line each part it did not read and each field an answer named
// absent, and prints a field left out as left out, never as a value. The test's clock stands
// years after the cut, so an age or a window that a command counts from its own clock at a cut
// prints differently from one counted from the cut.

/** The cut each read at a past time sends. */
const CUT = "2026-11-10T12:00:00Z";
/** The stand-in clock: years after the cut. */
const CLOCK = "2031-06-01T00:00:00Z";

const standIn = startAsOfStandIn("dispatch-as-of");

beforeAll(() => {
  setSystemTime(new Date(CLOCK));
});

afterAll(() => {
  setSystemTime();
  standIn.stop();
});

/** A search at a cut is sent to the other route of the same row of the plan's table. */
function sameRow(routes: readonly string[]): string[] {
  return routes.map((route) => route.replace("/api/v1/search/results", "/api/v1/search")).sort();
}

/** The run's output and any failure line, on one text. */
function said(run: Run): string {
  return `exit ${run.code}: ${run.stdout}${run.stderr}`;
}

/**
 * Runs `argv` without `--as-of`, then with it, and checks what every read at a cut must do: the
 * calls of the plan's table, each route of kind "answers" the present read calls called at the
 * cut too, the routes that hold no history called at the present alone (`noHistory`), and a `Left
 * out at` line that names each part left out and each field an answer named absent, or
 * `nothing`, which the present read does not print. A field left out never prints as
 * `undefined`, nor as the words for a Dispatch older than the field.
 */
async function readBothWays(
  argv: readonly string[],
  expected: {
    readonly noHistory: readonly string[];
    readonly parts: readonly string[];
    readonly absent: readonly string[];
  }
): Promise<{ readonly present: Run; readonly past: Run }> {
  const present = await standIn.run(argv);
  const past = await standIn.run([...argv, "--as-of", CUT]);
  const named = [...new Set([...expected.parts, ...expected.absent.map(absentFieldWords)])].sort();
  expect({
    present: [
      ...(present.code === 0 ? [] : [said(present)]),
      ...present.calls.filter((call) => call.asOf.length > 0).map((call) => call.text),
      ...present.stdout.split("\n").filter((line) => line.startsWith("Left out at")),
    ],
    past: [
      ...(past.code === 0 ? [] : [said(past)]),
      ...callsAtCutProblems(past, CUT),
      ...["undefined", "does not say"].filter((words) => past.stdout.includes(words)),
    ],
    answersAtTheCut: sameRow(routesCalled(past, "answers")),
    noHistoryAtThePresent: routesCalled(present, "no history"),
    leftOut: leftOutParts(past.stdout, CUT),
  }).toEqual({
    present: [],
    past: [],
    answersAtTheCut: sameRow(routesCalled(present, "answers")),
    noHistoryAtThePresent: [...expected.noHistory].sort(),
    leftOut: named.length === 0 ? ["nothing"] : named,
  });
  return { present, past };
}

/** The lines of `text` after the `Left out at` line. */
function body(text: string): string[] {
  const lines = text.trimEnd().split("\n");
  return lines[0]?.startsWith("Left out at") ? lines.slice(1) : lines;
}

/** `age` as `dispatch open-asks` prints it: days and hours past a day. */
function daysAndHours(fromMs: number, toMs: number): string {
  const hours = Math.floor((toMs - fromMs) / 3_600_000);
  return `${Math.floor(hours / 24)}d ${hours % 24}h`;
}

/** The fields the issue read leaves out at the cut: its route, its holder's liveness, its
 *  components and its place in the reference graph. */
const ISSUE_ABSENT = [
  "/route_status",
  "/route_holder",
  "/route_status_reason",
  "/holder_state",
  "/last_seen",
  "/takeable_at",
  "/design_gates",
  "/referenced_by_count",
  "/components/ids",
  "/components/unknown",
];

describe("each read command with and without --as-of", () => {
  test("read of an issue leaves out the graph, the coverage and liveness, and says nothing present of its route or claim", async () => {
    const { present, past } = await readBothWays(READ_FORMS.issue, {
      noHistory: [
        "GET /api/v1/agents",
        "GET /api/v1/issues/{key}/coverage",
        "GET /api/v1/issues/{key}/references",
        "GET /api/v1/references",
      ],
      parts: [LEFT_OUT.graph, LEFT_OUT.issueReferences, LEFT_OUT.coverage, LEFT_OUT.liveness],
      absent: ISSUE_ABSENT,
    });
    expect(body(present.stdout)).toEqual(
      expect.arrayContaining([
        `Claimed by: ${HOLDER_TITLE} since ${AT}`,
        `Route: role:implementer (held by ${HOLDER_TITLE})`,
        "Components: cli",
      ])
    );
    const pastLines = body(past.stdout);
    const claimed = pastLines.find((line) => line.startsWith("Claimed by: "));
    expect(claimed?.endsWith(` since ${AT} · liveness left out at ${CUT}`)).toBe(true);
    expect(pastLines).toEqual(
      expect.arrayContaining([
        `Route: role:implementer (whether it reaches anyone is left out at ${CUT})`,
        `Components: left out at ${CUT}`,
        `Coverage: left out at ${CUT}`,
      ])
    );
    for (const present of [
      "liveness unknown",
      "not running",
      "held by",
      "right now",
      "none live",
    ]) {
      expect(past.stdout).not.toContain(present);
    }
  });

  test("read of a comment prints its body and whether a block was removed as left out", async () => {
    const { present, past } = await readBothWays(READ_FORMS.comment, {
      noHistory: ["GET /api/v1/references"],
      parts: [LEFT_OUT.graph],
      absent: ["/comment/body", "/replies/*/reply_block/removed"],
    });
    expect(body(present.stdout)).toEqual(
      expect.arrayContaining([
        "Body: The words before the long edit.",
        "Re: block b1: The quoted words.",
        "Body: A reply.",
      ])
    );
    expect(body(past.stdout)).toEqual(
      expect.arrayContaining([
        `Body: left out at ${CUT}`,
        `Re: block b1 (whether it was removed is left out at ${CUT}): The quoted words.`,
        "Body: A reply.",
      ])
    );
  });

  test("doc-read --blocks prints a token and an id left out as left out, never as none", async () => {
    // The document is found through the issue read, whose answer names its own fields absent.
    const { present, past } = await readBothWays(READ_FORMS.blocks, {
      noHistory: [],
      parts: [],
      absent: [...ISSUE_ABSENT, "/token", "/blocks/*/token", "/blocks/*/embedding", "/blocks/*/id"],
    });
    expect(body(present.stdout).slice(1)).toEqual([
      "intro heading (sha256:intro; 0 comments, 0 asks): # Spec",
      "p-1 paragraph (sha256:p-1; 0 comments, 0 asks): The workspace opens natively.",
      "",
      "Document token: sha256:spec-token",
      "Document version: 1",
    ]);
    expect(body(past.stdout).slice(1)).toEqual([
      `intro heading (token left out at ${CUT}; 0 comments, 0 asks): # Spec`,
      `(id left out at ${CUT}) paragraph (token left out at ${CUT}; 0 comments, 0 asks): The workspace opens natively.`,
      "",
      "Document version: 1",
    ]);
  });

  test("search at a cut names the broadened query left out", async () => {
    await readBothWays(READ_FORMS.search, {
      noHistory: [],
      parts: [],
      absent: ["/query/broadened"],
    });
  });

  test("answers counts its 72 hours back from the cut, and says so", async () => {
    const { present, past } = await readBothWays(READ_FORMS.answers, {
      noHistory: [],
      parts: [],
      absent: [],
    });
    expect(body(present.stdout)[0]).toBe(
      "1 item people wrote to this session in the last 72 hours, newest first, word for word:"
    );
    expect(body(past.stdout)[0]).toBe(
      `1 item people wrote to this session in the 72 hours before ${CUT}, newest first, word for word:`
    );
    for (const run of [present, past]) {
      expect(run.stdout).toContain("The words of an hour before.");
      expect(run.stdout).not.toContain("The words of 80 hours before.");
    }
  });

  test("open-asks prints each age at the cut and a person the records cannot give as left out", async () => {
    const { present, past } = await readBothWays(READ_FORMS.openAsks, {
      noHistory: [],
      parts: [],
      absent: ["/asks/*/waiting_on_person"],
    });
    const opened = Date.parse(ASK_OPENED);
    const rows = (run: Run) => body(run.stdout).filter((line) => line.startsWith("- "));
    expect(rows(present).map((row) => row.split(" · ").slice(0, 4))).toEqual([
      [
        `- ${daysAndHours(opened, Date.parse(CLOCK))}`,
        "P1",
        "DSP-1: Native workspace",
        `asked of ${OWNER}`,
      ],
      [
        `- ${daysAndHours(opened, Date.parse(CLOCK))}`,
        "P1",
        "DSP-1: Native workspace",
        `asked of ${OWNER}`,
      ],
    ]);
    expect(rows(past).map((row) => row.split(" · ").slice(0, 4))).toEqual([
      [
        `- ${daysAndHours(opened, Date.parse(CUT))}`,
        "P1",
        "DSP-1: Native workspace",
        `asked of ${OWNER}`,
      ],
      [
        `- ${daysAndHours(opened, Date.parse(CUT))}`,
        "P1",
        "DSP-1: Native workspace",
        `asked of (left out at ${CUT})`,
      ],
    ]);
  });

  test("inbox at a cut lists each ask under whose turn it is, and leaves out Later, threads and credential requests", async () => {
    const { present, past } = await readBothWays(READ_FORMS.inbox, {
      noHistory: ["GET /api/v1/credential-requests", "GET /api/v1/me/replies"],
      parts: [LEFT_OUT.unreadThreads, LEFT_OUT.credentialRequests, LEFT_OUT.snoozes],
      absent: ["/*/snoozed_until"],
    });
    const ask = (key: string, id: string) =>
      `- ${key} · P1 · question · Question of ${key}? · dispatch://${key}/ask/${id}`;
    const human = ask("DSP-11", "11111111-1111-4111-8111-111111111111");
    const deferred = ask("DSP-12", "22222222-2222-4222-8222-222222222222");
    const deferralEnded = ask("DSP-13", "33333333-3333-4333-8333-333333333333");
    const agent = ask("DSP-14", "44444444-4444-4444-8444-444444444444");
    expect(body(present.stdout)).toEqual([
      "Needs you 2",
      human,
      deferralEnded,
      "",
      "Waiting on agents 1",
      agent,
      "",
      "Later 1",
      deferred,
      "",
      "Unread threads 1",
      "- DSP-16 · comment · The staging box lost its disk again · user bob@example.com · 5m · /issues/DSP-16/comments/66666666-6666-4666-8666-666666666666",
      "",
      "Credential requests 0",
    ]);
    expect(body(past.stdout)).toEqual([
      "Needs you 3",
      human,
      deferred,
      deferralEnded,
      "",
      "Waiting on agents 1",
      agent,
      "",
      `Later left out at ${CUT}`,
    ]);
  });

  test("results at a cut prints what GitHub says now, and a check version no index holds, as left out", async () => {
    const { present, past } = await readBothWays(READ_FORMS.results, {
      noHistory: [],
      parts: [],
      absent: [
        "/checks/*/compared_with",
        "/checks/*/compared_nothing",
        "/checks/*/pass_stands",
        "/checks/*/ci",
        "/checks/*/results/*/commit_stands",
        "/checks/*/results/*/currency",
        "/checks/*/version",
        "/checks/*/sha256",
        "/checks/*/results/*/check_words_moved",
      ],
    });
    const presentText = body(present.stdout).join("\n");
    expect(presentText).toContain("A pass stands: yes, from implementer.");
    expect(presentText).toContain(
      "The check's words did not move after this result (result version 1, check version 1)."
    );
    const pastText = body(past.stdout).join("\n");
    expect(pastText).toContain(
      `- k1 (before_merge). What it is compared with is left out at ${CUT}. Whether a pass stands is left out at ${CUT}.`
    );
    expect(pastText).toContain(`Where that commit stands is left out at ${CUT}.`);
    expect(pastText).toContain(
      `Whether the check's words moved is left out at ${CUT} (result version 1, check version left out).`
    );
  });
});
