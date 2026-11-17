import { describe, expect, test } from "bun:test";
import type { LensChain as ChainRead, LensRead, NodeLenses } from "@legion/contracts";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type RenderResult, render, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http } from "msw";
import { type ReactNode, useState } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { answerRequests } from "../../../__tests__/network";
import type { TreeNode } from "../issue-tree";
import { NodeRow } from "../NodeRow";
import { NoTooltips } from "../NoTooltips";
import {
  authorKind,
  CONTROL_LABELS,
  LENS_ANSWERS,
  LENS_WORDS,
  NODE_TYPE_NAMES,
  type NodeType,
} from "../page-words";
import { LensLines } from "./LensLines";
import { kindWords, type LensFacts, type LensPlace } from "./lens-drafts";

/**
 * What a lens's row draws of its chain, and the lines a lens, a risk, a constrained statement and
 * a constraint draw under their words (dispatch://INFRA-4065 R2, R3, R5, R6, R7).
 *
 * The chain is read through the page's own client (`lens-reads.ts`, `api.getLenses`), answered at
 * `fetch` by the suite's network (`answerRequests`), so a request the row sends is one the test
 * counts. A failed read is not retried, as in the page's other rendering tests.
 */

const KEY = "CORE-1";

const TRADE_OFF = "dispatch://lens/0c6f1d2e-1111-4a6b-9c1d-5e2f3a4b5c61";
const PREMORTEM = "dispatch://lens/0c6f1d2e-2222-4a6b-9c1d-5e2f3a4b5c62";
const FOLLOW_UP = "dispatch://lens/0c6f1d2e-3333-4a6b-9c1d-5e2f3a4b5c63";
const UNNAMED = "dispatch://lens/0c6f1d2e-4444-4a6b-9c1d-5e2f3a4b5c64";
const FURTHER = "dispatch://lens/0c6f1d2e-5555-4a6b-9c1d-5e2f3a4b5c65";
const ELSEWHERE = "dispatch://lens/0c6f1d2e-6666-4a6b-9c1d-5e2f3a4b5c66";
const BEYOND = "dispatch://lens/0c6f1d2e-7777-4a6b-9c1d-5e2f3a4b5c67";

/** The statement both lenses read, and its first line as the tree drew it. */
const STATEMENT = "dispatch://CORE-1/spec#b-r1";
const STATEMENT_LINE = "Store the notices for a day";

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

function facts(partial: Partial<LensFacts>): LensFacts {
  return { constrainedBy: [], preventedBy: [], triggered: [], triggeredBy: [], ...partial };
}

/** Every lens of the tree the focused pre-mortem stands in, by address. */
const PLACES: ReadonlyMap<string, LensPlace> = new Map([
  [TRADE_OFF, { id: "lens/1111", kindWords: "trade-off analysis", onLine: STATEMENT_LINE }],
  [PREMORTEM, { id: "lens/2222", kindWords: "pre-mortem", onLine: STATEMENT_LINE }],
]);

/** The pre-mortem on the statement, triggered by the trade-off analysis. */
function premortem(lenses: ReadonlyMap<string, LensPlace> = PLACES): TreeNode {
  return node({
    id: "lens/2222",
    lens: facts({
      kind: "pre-mortem",
      lenses,
      triggeredBy: [{ address: TRADE_OFF, id: "lens/1111", words: "Trade-off analysis" }],
    }),
    reference: PREMORTEM,
    text: "Ship it behind a flag",
    type: "lens",
  });
}

/** One lens as the lens read answers it, of the kind whose words are `words`. */
function lensRead(address: string, words: string | null): LensRead {
  return {
    address,
    author: { id: "alice", kind: "person" },
    created_at: "2026-11-16T09:00:00Z",
    findings: [],
    kind: words === null ? null : { id: `kind-${address.slice(-2)}`, words },
    on: STATEMENT,
    risks: [],
    state: "live",
    triggered: [],
    triggered_by: [],
    verdict: null,
    visibility: "project",
  };
}

function chain(lenses: readonly LensRead[], stoppedAt: string | null = null): ChainRead {
  return { lenses: [...lenses], stopped_at: stoppedAt };
}

/** The lens read of the pre-mortem, holding `chains`. */
function lensesOf(chains: readonly ChainRead[]): NodeLenses {
  return {
    chains: [...chains],
    lens: lensRead(PREMORTEM, "pre-mortem"),
    lenses: [],
    on: { address: PREMORTEM, kind: "lens", state: "live", visibility: "project" },
  };
}

/** Answers every lens read with what `answer` gives, and keeps the `on` of each request in the
 *  order they came. */
function answerLensReads(answer: () => Response | Promise<Response>): string[] {
  const asked: string[] = [];
  answerRequests(
    http.get("/api/v1/lenses", ({ request }) => {
      asked.push(new URL(request.url).searchParams.get("on") ?? "");
      return answer();
    })
  );
  return asked;
}

/** Who made a node the model holds no author for, as the tree names one. */
const NOBODY_RECORDED = {
  kind: authorKind(undefined),
  name: CONTROL_LABELS.unknown_author,
  reader: false,
};

/** One row the way the tree draws it: unfocused until a focus lands inside it, unless it starts
 *  focused, and then the focused row, with its body open. */
function LensRow({ drawn, focused: start = false }: { drawn: TreeNode; focused?: boolean }) {
  const [focused, setFocused] = useState(start);
  return (
    <NodeRow
      author={NOBODY_RECORDED}
      body={focused}
      context={false}
      focused={focused}
      isClosed={false}
      level={1}
      node={drawn}
      onFocus={() => setFocused(true)}
      onPin={() => {}}
      onToggle={() => {}}
      ownAnswer={false}
      pinned={false}
      rowKey="row"
      tabStop={false}
    />
  );
}

/** A client whose failed read is not retried, as the page's other rendering tests use. */
function testClient(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

/** `page` on this issue's route, so a chip's link takes the route's key. */
function renderOnIssue(page: ReactNode, queryClient = testClient()): RenderResult {
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[`/issues/${KEY}`]}>
        <NoTooltips>
          <Routes>
            <Route element={page} path="/issues/:key/*" />
          </Routes>
        </NoTooltips>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

function showChain(view: RenderResult): HTMLButtonElement | null {
  return view.container.querySelector<HTMLButtonElement>("[data-lens-show-chain]");
}

/** The control, once the row draws it. */
async function drawnControl(view: RenderResult): Promise<HTMLButtonElement> {
  return waitFor(() => {
    const control = showChain(view);
    if (control === null) throw new Error("the focused lens draws no control for its chain");
    return control;
  });
}

/** What the chain's read says, where it says something. */
function chainStatus(view: RenderResult): string | null {
  return view.container.querySelector('[data-lens-chain] [role="status"]')?.textContent ?? null;
}

function chainAddresses(view: RenderResult): (string | null)[] {
  return [...view.container.querySelectorAll("[data-lens-chain-entry]")].map((entry) =>
    entry.getAttribute("data-lens-address")
  );
}

/** What a reader reads of each entry of the chain, and whose words each kind's words are. */
function chainEntries(view: RenderResult) {
  return [...view.container.querySelectorAll("[data-lens-chain-entry]")].map((entry) => {
    const kind = entry.querySelector("[data-lens-chain-kind]");
    return {
      address: entry.getAttribute("data-lens-address"),
      kind: kind?.textContent ?? null,
      line: entry.querySelector("[data-lens-chain-line]")?.textContent ?? null,
      marked: kind?.hasAttribute("data-author-text") ? "author" : "page",
    };
  });
}

/** Lets whatever a press or a focus would send go out and be answered. */
async function settle(): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  setTimeout(resolve, 50);
  await act(() => promise);
}

// R2, R6: the lens read is sent at a press of the control alone, never at the focus, which `j`
// gives every row; once in a load, so a second press, and a press on the row focused again,
// draw the held answer and send nothing.
test("the chain is read at a press and not at the focus", async () => {
  const user = userEvent.setup();
  const out = Promise.withResolvers<void>();
  const asked = answerLensReads(async () => {
    await out.promise;
    return Response.json(lensesOf([chain([lensRead(TRADE_OFF, null), lensRead(PREMORTEM, null)])]));
  });
  const queryClient = testClient();
  const first = renderOnIssue(<LensRow drawn={premortem()} />, queryClient);
  try {
    expect(showChain(first), "an unfocused lens draws the control").toBeNull();
    const row = first.container.querySelector<HTMLElement>("[data-node-row]");
    act(() => row?.focus());
    const control = await drawnControl(first);
    expect(control.textContent).toBe(LENS_WORDS.show_chain);
    expect(control.hasAttribute("data-page-chrome")).toBe(true);
    expect(control.tabIndex, "the control is in the tab order").toBe(-1);
    await settle();
    expect(asked, "the focus sent the lens read").toEqual([]);
    expect(chainStatus(first)).toBeNull();

    await user.click(control);
    expect(chainStatus(first)).toBe(LENS_ANSWERS.loading_chain);
    await waitFor(() => expect(asked).toEqual([PREMORTEM]));
    out.resolve();
    await waitFor(() => expect(chainAddresses(first)).toEqual([TRADE_OFF, PREMORTEM]));
    expect(chainStatus(first)).toBeNull();
    expect(showChain(first), "the control went with the answer").toBe(control);

    await user.click(control);
    await settle();
    expect(asked, "a second press sent the lens read again").toEqual([PREMORTEM]);
    expect(chainAddresses(first)).toEqual([TRADE_OFF, PREMORTEM]);
  } finally {
    out.resolve();
    first.unmount();
  }

  // The row loses the focus and has it again, in the same load.
  const again = renderOnIssue(<LensRow drawn={premortem()} focused />, queryClient);
  try {
    const control = await drawnControl(again);
    expect(chainAddresses(again), "the held answer is drawn before the press").toEqual([]);
    await user.click(control);
    expect(chainStatus(again)).toBeNull();
    expect(chainAddresses(again)).toEqual([TRADE_OFF, PREMORTEM]);
    await settle();
    expect(asked, "a press on the row focused again sent the lens read").toEqual([PREMORTEM]);
  } finally {
    again.unmount();
  }

  // A lens with no trigger chip has no chain to show.
  const alone = node({
    id: "lens/3333",
    lens: facts({ kind: "second opinion", lenses: PLACES }),
    reference: FOLLOW_UP,
    type: "lens",
  });
  const unchained = renderOnIssue(<LensRow drawn={alone} focused />, queryClient);
  try {
    expect(unchained.container.querySelector("[data-node-row]")).not.toBeNull();
    expect(showChain(unchained)).toBeNull();
  } finally {
    unchained.unmount();
  }
});

// R2: the answer holds the chain this lens stands in and the chains of the lenses invoked on
// it, which are its rows; only the first is drawn, in the answer's order, each lens by its
// kind's words as held - the tree's, else the read's, else the page's word - and by the first
// line of the row it stands under where the tree drew it.
test("only the chain that holds the lens is drawn", async () => {
  const user = userEvent.setup();
  const asked = answerLensReads(() =>
    Response.json(
      lensesOf([
        chain([lensRead(ELSEWHERE, "second opinion"), lensRead(BEYOND, "second opinion")]),
        chain([
          lensRead(TRADE_OFF, "trade-off analysis, as read"),
          lensRead(PREMORTEM, "pre-mortem"),
          lensRead(FOLLOW_UP, "second opinion"),
          lensRead(UNNAMED, null),
        ]),
      ])
    )
  );
  const view = renderOnIssue(<LensRow drawn={premortem()} focused />);
  try {
    await user.click(await drawnControl(view));
    await waitFor(() => expect(chainAddresses(view)).toHaveLength(4));
    expect(asked).toEqual([PREMORTEM]);
    expect(chainEntries(view)).toEqual([
      { address: TRADE_OFF, kind: "trade-off analysis", line: STATEMENT_LINE, marked: "author" },
      { address: PREMORTEM, kind: "pre-mortem", line: STATEMENT_LINE, marked: "author" },
      { address: FOLLOW_UP, kind: "second opinion", line: null, marked: "author" },
      { address: UNNAMED, kind: NODE_TYPE_NAMES.lens, line: null, marked: "page" },
    ]);
    expect(
      view.container.querySelector(`[data-lens-address="${ELSEWHERE}"]`),
      "the chain of a lens invoked on this one is drawn"
    ).toBeNull();
    expect(view.container.querySelector("[data-lens-chain-more]")).toBeNull();
  } finally {
    view.unmount();
  }
});

// R2: a chain the read cut at its depth names the lens it stopped at, last, by a chip of its
// kind's words cut as the tag is, linked to its row where the tree drew one.
test("a stopped_at ends the chain with More lenses past this one", async () => {
  const user = userEvent.setup();
  answerLensReads(() =>
    Response.json(
      lensesOf([chain([lensRead(TRADE_OFF, null), lensRead(PREMORTEM, null)], FURTHER)])
    )
  );
  const drawnEnd = async (lenses: ReadonlyMap<string, LensPlace>) => {
    const view = renderOnIssue(<LensRow drawn={premortem(lenses)} focused />);
    try {
      await user.click(await drawnControl(view));
      await waitFor(() => expect(chainAddresses(view)).toEqual([TRADE_OFF, PREMORTEM]));
      const last = view.container.querySelector("[data-lens-chain]")?.lastElementChild;
      const label = last?.firstElementChild;
      return {
        href: last?.querySelector("[data-node-chip] a")?.getAttribute("href") ?? null,
        label: label?.textContent,
        labelMarked: label?.hasAttribute("data-page-chrome"),
        more: last?.hasAttribute("data-lens-chain-more"),
        words: last?.querySelector("[data-node-chip] [data-chip-name]")?.textContent,
      };
    } finally {
      view.unmount();
    }
  };

  const further = new Map(PLACES).set(FURTHER, {
    id: "lens/5555",
    kindWords: "risk review of the notice store's retention",
    onLine: STATEMENT_LINE,
  });
  expect(await drawnEnd(further)).toEqual({
    href: "/issues/CORE-1?node=lens%2F5555",
    label: LENS_WORDS.more_lenses,
    labelMarked: true,
    more: true,
    words: "Risk review of the notice…",
  });
  // A lens the tree drew no row for is named by the page's word for a lens, with no link.
  expect(await drawnEnd(PLACES)).toEqual({
    href: null,
    label: LENS_WORDS.more_lenses,
    labelMarked: true,
    more: true,
    words: kindWords(NODE_TYPE_NAMES.lens),
  });
});

// R2: a read that fails says so in the page's words, and a lens that is no longer there says
// what R4's 404 says. Every attempt is answered.
test("a failed or 404 chain read says so", async () => {
  const user = userEvent.setup();
  const said = async (status: number, error: string) => {
    const asked = answerLensReads(() => Response.json({ code: "X", error }, { status }));
    const view = renderOnIssue(<LensRow drawn={premortem()} focused />);
    try {
      await user.click(await drawnControl(view));
      await waitFor(() => expect(chainStatus(view)).not.toBe(LENS_ANSWERS.loading_chain));
      expect(asked.length).toBeGreaterThan(0);
      expect(chainAddresses(view)).toEqual([]);
      expect(showChain(view), "the control went with the failure").not.toBeNull();
      return {
        marked: view.container
          .querySelector('[data-lens-chain] [role="status"]')
          ?.hasAttribute("data-page-chrome"),
        status: chainStatus(view),
      };
    } finally {
      view.unmount();
    }
  };
  expect(await said(500, "the read failed")).toEqual({
    marked: true,
    status: LENS_ANSWERS.chain_failed,
  });
  expect(await said(404, "no lens at that address")).toEqual({
    marked: true,
    status: LENS_ANSWERS.gone,
  });
});

/**
 * What a reader reads of each line `drawn` draws on this issue's route, with its body open where
 * `body` holds: what the line is, its label in the page's words, the author's words it holds, and
 * each chip's words and where its link goes.
 */
function linesDrawn(drawn: TreeNode, body = false) {
  const view = render(
    <MemoryRouter initialEntries={[`/issues/${KEY}`]}>
      <Routes>
        <Route
          element={<LensLines body={body} node={drawn} tabStops={false} />}
          path="/issues/:key/*"
        />
      </Routes>
    </MemoryRouter>
  );
  try {
    return [...view.container.querySelectorAll("[data-lens-line]")].map((line) => {
      const parts = [...line.children];
      return {
        chips: [...line.querySelectorAll("[data-node-chip]")].map((chip) => ({
          href: chip.querySelector("a")?.getAttribute("href") ?? null,
          words: chip.querySelector("[data-chip-name][data-author-text]")?.textContent ?? null,
        })),
        label: parts.find((part) => part.hasAttribute("data-page-chrome"))?.textContent ?? null,
        line: line.getAttribute("data-lens-line"),
        words: parts
          .filter((part) => part.hasAttribute("data-author-text"))
          .map((part) => part.textContent),
      };
    });
  } finally {
    view.unmount();
  }
}

const CHECK = "dispatch://CORE-1/verification#b-c1";
const ISSUE_CONSTRAINT = "dispatch://constraint/9a1b2c3d-0000-4e5f-8a9b-0c1d2e3f4a50";
const CATALOGUE_CONSTRAINT = "dispatch://lens-kinds/pre-mortem/sources/owasp-asvs";

function risk(partial: Partial<LensFacts>): TreeNode {
  return node({
    id: "lens/2222#b-risk-1",
    lens: facts({ earlyWarning: "Notices pile up past a day", ...partial }),
    text: "The store keeps notices for a week",
    type: "risk",
  });
}

describe("the lines under a row", () => {
  // R2, R7: each chip names its lens, linked to its row where the tree drew one.
  test("a lens draws the lenses that triggered it and the lenses it triggered", () => {
    const drawn = node({
      id: "lens/2222",
      lens: facts({
        kind: "pre-mortem",
        triggered: [{ address: FOLLOW_UP, words: "Second opinion" }],
        triggeredBy: [{ address: TRADE_OFF, id: "lens/1111", words: "Trade-off analysis" }],
      }),
      reference: PREMORTEM,
      type: "lens",
    });
    expect(linesDrawn(drawn)).toEqual([
      {
        chips: [{ href: "/issues/CORE-1?node=lens%2F1111", words: "Trade-off analysis" }],
        label: LENS_WORDS.triggered_by,
        line: "triggered_by",
        words: [],
      },
      {
        chips: [{ href: null, words: "Second opinion" }],
        label: LENS_WORDS.triggered,
        line: "triggered",
        words: [],
      },
    ]);
  });

  // R3: a risk's early warning, then the checks that prevent it, or the one line saying none
  // does, and never both.
  test("a risk names the checks that prevent it, or says that none does", () => {
    const prevented = risk({
      preventedBy: [
        { address: CHECK, id: "verification-md#b-c1", words: "Notices older than a day…" },
      ],
    });
    const warning = {
      chips: [],
      label: LENS_WORDS.early_warning,
      line: "early_warning",
      words: ["Notices pile up past a day"],
    };
    expect(linesDrawn(prevented)).toEqual([
      warning,
      {
        chips: [
          {
            href: "/issues/CORE-1?node=verification-md%23b-c1",
            words: "Notices older than a day…",
          },
        ],
        label: LENS_WORDS.prevented_by,
        line: "prevented_by",
        words: [],
      },
    ]);
    expect(linesDrawn(risk({}))).toEqual([
      warning,
      {
        chips: [],
        label: LENS_WORDS.prevented_by_nothing,
        line: "prevented_by_nothing",
        words: [],
      },
    ]);
  });

  // R5: a statement names each constraint it is held to; one of the issue's is a row and its
  // chip a link, and one of the catalogue has no row, so its chip is words with no link.
  test("a statement names its constraints, the issue's linked and the catalogue's as words", () => {
    const drawn = node({
      id: "spec#b-r5",
      lens: facts({
        constrainedBy: [
          { address: ISSUE_CONSTRAINT, id: "constraint/9a1b", words: "Retention under 24 hours" },
          { address: CATALOGUE_CONSTRAINT, words: "OWASP ASVS 4.0 V8.3" },
        ],
      }),
      reference: "dispatch://CORE-1/spec#b-r5",
      text: "Notices expire",
      type: "requirement",
    });
    expect(linesDrawn(drawn)).toEqual([
      {
        chips: [
          { href: "/issues/CORE-1?node=constraint%2F9a1b", words: "Retention under 24 hours" },
          { href: null, words: "OWASP ASVS 4.0 V8.3" },
        ],
        label: LENS_WORDS.constrained_by,
        line: "constrained_by",
        words: [],
      },
    ]);
  });

  // The plan's decision on a constraint's row: a standard's line holds its reference, and a
  // measurement's says where it was taken.
  test("a standard draws its reference and a measurement where it was taken", () => {
    const standard = node({
      id: "constraint/9a1b",
      lens: facts({ reference: "ISO/IEC 27001 A.8.10" }),
      text: "Information deletion",
      type: "constraint",
    });
    const measurement = node({
      id: "constraint/9a1c",
      lens: facts({ takenWhere: "the staging cluster" }),
      text: "p99 latency: 180 ms",
      type: "constraint",
    });
    expect(linesDrawn(standard)).toEqual([
      { chips: [], label: null, line: "reference", words: ["ISO/IEC 27001 A.8.10"] },
    ]);
    expect(linesDrawn(measurement)).toEqual([
      { chips: [], label: LENS_WORDS.taken_at, line: "taken_at", words: ["the staging cluster"] },
    ]);
  });

  // R1, R3: an open body draws a lens's kind's words whole, which its tag cuts, and a risk's
  // prevention; a closed one draws neither.
  test("an open body adds a lens's kind's words whole and a risk's prevention", () => {
    const lens = node({
      id: "lens/5555",
      lens: facts({ kind: "risk review of the notice store's retention" }),
      reference: FURTHER,
      type: "lens",
    });
    expect(linesDrawn(lens, true)).toEqual([
      {
        chips: [],
        label: null,
        line: "kind",
        words: ["Risk review of the notice store's retention"],
      },
    ]);
    expect(linesDrawn(lens)).toEqual([]);

    const prevents = risk({ prevention: "Expire notices every hour" });
    expect(linesDrawn(prevents, true).map(({ line }) => line)).toEqual([
      "early_warning",
      "prevention",
      "prevented_by_nothing",
    ]);
    expect(linesDrawn(prevents, true)[1]).toEqual({
      chips: [],
      label: LENS_WORDS.prevention,
      line: "prevention",
      words: ["Expire notices every hour"],
    });
    expect(linesDrawn(prevents).map(({ line }) => line)).toEqual([
      "early_warning",
      "prevented_by_nothing",
    ]);
  });
});
