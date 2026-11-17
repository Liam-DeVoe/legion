import { expect, test } from "bun:test";
import { act, render, waitFor } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { type ReactNode, useEffect, useState } from "react";
import { MemoryRouter, useLocation } from "react-router-dom";

import { KeymapProvider } from "../../shell/KeymapProvider";
import { presetPath } from "../issue-address";
import { CONTROL_LABELS } from "../page-words";
import { TAB_STOP_ROW } from "../tree-focus";
import { useIssueKeymap } from "../useIssueKeymap";
import { FailedReadLine } from "./failed-read-line";
import { ViewStage } from "./stage";

// A failed read's line, as each view drawn in the stage is to draw it. The views here are
// stand-ins that draw the lines over a tree the way a view is to: each view's own slot is to be
// filled by that view's own code.

/** Stands in for Chromium's focus fixup, which moves the focus of a button it disables to the
 *  page's body where happy-dom keeps it on the button; the returned function ends it. happy-dom
 *  refuses `blur()` on a disabled control, so the body is given the focus instead. */
function blurWhenDisabled(): () => void {
  const fixup = new MutationObserver(() => {
    const focused = document.activeElement;
    if (focused instanceof HTMLButtonElement && focused.disabled) document.body.focus();
  });
  fixup.observe(document.body, { attributeFilter: ["disabled"], attributes: true, subtree: true });
  return () => fixup.disconnect();
}

/** Lets `count` frames pass. */
async function frames(count: number): Promise<void> {
  for (let left = count; left > 0; left -= 1) {
    const { promise, resolve } = Promise.withResolvers<void>();
    requestAnimationFrame(() => resolve());
    await act(() => promise);
  }
}

/** A view with one read refused, which keeps that read's line drawn while its Retry runs the
 *  read again. */
function RunningAgainView({ asked }: { asked: () => void }): ReactNode {
  const [running, setRunning] = useState(false);
  return (
    <>
      <FailedReadLine
        message="Couldn't read the statements."
        onRetry={() => {
          asked();
          setRunning(true);
        }}
        retrying={running}
      />
      <div aria-label={CONTROL_LABELS.tree} role="tree" tabIndex={-1} />
    </>
  );
}

test("a failed read's Retry stays enabled while its read runs again, and a press then asks nothing more", async () => {
  const user = userEvent.setup();
  const fixup = blurWhenDisabled();
  let asks = 0;
  const view = render(
    <ViewStage view="spec">
      <RunningAgainView
        asked={() => {
          asks += 1;
        }}
      />
    </ViewStage>
  );
  try {
    const retry = view.getByRole("button", { name: "Retry" }) as HTMLButtonElement;
    await act(async () => retry.focus());
    await user.keyboard("{Enter}");
    expect(asks, "Enter on Retry did not ask the read again").toBe(1);
    // The fixup's observer runs once React has drawn the read running.
    await act(async () => {});
    expect(document.activeElement, "the read running again took the focus off its Retry").toBe(
      retry
    );
    expect(retry.disabled, "the Retry is disabled while its read runs again").toBe(false);
    await user.keyboard("{Enter}");
    expect(asks, "a second press while the read runs asked it again").toBe(1);
  } finally {
    fixup();
    view.unmount();
  }
});

/** How a read of `TwoReadsView` stands. */
type Stands = "failed" | "out" | "answered";

/** A view drawn from two reads: a failed read's line for each read that failed, in the order of
 *  the reads, over a tree that holds the view's rows once both have answered, its first row
 *  holding the tab stop. */
function TwoReadsView({ results, statements }: { results: Stands; statements: Stands }): ReactNode {
  const answered = statements === "answered" && results === "answered";
  return (
    <>
      {statements === "failed" ? (
        <FailedReadLine
          message="Couldn't read the statements."
          onRetry={() => undefined}
          retrying={false}
        />
      ) : null}
      {results === "failed" ? (
        <FailedReadLine
          message="Couldn't read the results."
          onRetry={() => undefined}
          retrying={false}
        />
      ) : null}
      <div aria-label={CONTROL_LABELS.tree} role="tree" tabIndex={-1}>
        {answered ? (
          <>
            <div aria-level={1} role="treeitem" tabIndex={0}>
              The first statement.
            </div>
            <div aria-level={1} role="treeitem" tabIndex={-1}>
              The second statement.
            </div>
          </>
        ) : null}
      </div>
    </>
  );
}

test("a failed read's line that leaves with the focus in it gives the focus to what replaces it: the next failed read's Retry, or the view's first row", async () => {
  const drawn = (statements: Stands, results: Stands) => (
    <ViewStage view="spec">
      <TwoReadsView results={results} statements={statements} />
    </ViewStage>
  );

  (document.activeElement as HTMLElement | null)?.blur();
  const view = render(drawn("failed", "failed"));
  try {
    const [statements, results] = view.getAllByRole("button", { name: "Retry" });
    await waitFor(() => expect(document.activeElement).toBe(statements));
    view.rerender(drawn("answered", "failed"));
    await waitFor(() =>
      expect(
        document.activeElement,
        "the statements answered and their line left the focus elsewhere than the results' Retry"
      ).toBe(results)
    );
    view.rerender(drawn("answered", "answered"));
    await waitFor(() =>
      expect(
        document.activeElement,
        "the results answered and their line left the focus elsewhere than the view's first row"
      ).toBe(view.container.querySelector(TAB_STOP_ROW))
    );
  } finally {
    view.unmount();
  }

  // A read asked again that holds no answer is out again, so its line leaves before the rows
  // come: the tree itself holds the focus meanwhile, and the first row takes it once drawn.
  (document.activeElement as HTMLElement | null)?.blur();
  const again = render(drawn("answered", "failed"));
  try {
    const retry = again.getByRole("button", { name: "Retry" });
    await waitFor(() => expect(document.activeElement).toBe(retry));
    again.rerender(drawn("answered", "out"));
    const tree = again.getByRole("tree", { name: CONTROL_LABELS.tree });
    await waitFor(() =>
      expect(
        document.activeElement,
        "the results' read out again left the focus elsewhere than the tree"
      ).toBe(tree)
    );
    again.rerender(drawn("answered", "answered"));
    await waitFor(() =>
      expect(
        document.activeElement,
        "the rows that came left the focus elsewhere than the view's first row"
      ).toBe(again.container.querySelector(TAB_STOP_ROW))
    );
  } finally {
    again.unmount();
  }
});

const ISSUE = "CORE-1";

/** A view whose read is refused from its first load, which draws its tree at once and with no
 *  row, as a view does while a read is out. The refused read's line over it is drawn at once, or
 *  where `late` a moment after the view mounts, once the chord has reached the tree. */
function RefusedTreeView({ late }: { late: boolean }): ReactNode {
  const [refused, setRefused] = useState(!late);
  useEffect(() => {
    if (!late) return;
    const failed = window.setTimeout(() => setRefused(true), 40);
    return () => window.clearTimeout(failed);
  }, [late]);
  return (
    <>
      {refused ? (
        <FailedReadLine
          message="Couldn't read the checks."
          onRetry={() => undefined}
          retrying={false}
        />
      ) : null}
      <div aria-label={CONTROL_LABELS.tree} role="tree" tabIndex={-1} />
    </>
  );
}

/** The issue page's chords (`useIssueKeymap`), a control elsewhere on the page, and a stage that
 *  draws the Verification view at that view's address. */
function ChordPage({ late }: { late: boolean }): ReactNode {
  useIssueKeymap(ISSUE);
  const { pathname } = useLocation();
  return (
    <>
      <button type="button">Elsewhere</button>
      {pathname === presetPath(ISSUE, "verification") ? (
        <ViewStage view="verification">
          <RefusedTreeView late={late} />
        </ViewStage>
      ) : null}
    </>
  );
}

test("the chord that opens a view with no row reaches its failed read's Retry from anywhere on the page", async () => {
  const user = userEvent.setup();
  for (const from of ["a control elsewhere", "the page's body"] as const) {
    for (const late of [false, true]) {
      const page = render(
        <MemoryRouter initialEntries={[presetPath(ISSUE, "conversation")]}>
          <KeymapProvider>
            <ChordPage late={late} />
          </KeymapProvider>
        </MemoryRouter>
      );
      try {
        if (from === "the page's body") (document.activeElement as HTMLElement | null)?.blur();
        else await act(async () => page.getByRole("button", { name: "Elsewhere" }).focus());
        await user.keyboard("tk");
        const retry = await page.findByRole("button", { name: "Retry" });
        const said = `t k from ${from}, with the read refused ${late ? "after" : "before"} the chord reached the tree, left the focus elsewhere than the Retry`;
        await waitFor(() => expect(document.activeElement, said).toBe(retry));
        // The chord looks for the tree a frame after it opens the view and focuses it there, so
        // the focus is read again once those frames have passed.
        await frames(3);
        expect(document.activeElement, said).toBe(retry);
      } finally {
        page.unmount();
      }
    }
  }
});

/** A control elsewhere on the page and, once `drawn`, the Verification view with its read
 *  refused: a case puts the focus where it wants it before the stage mounts. */
function RefusedViewPage({ drawn, late }: { drawn: boolean; late: boolean }): ReactNode {
  return (
    <>
      <button type="button">Elsewhere</button>
      {drawn ? (
        <ViewStage view="verification">
          <RefusedTreeView late={late} />
        </ViewStage>
      ) : null}
    </>
  );
}

/** The ways a reader moves the focus off the Retry onto the view's tree: a press on the tree, or
 *  a screen reader's cursor, which focuses what it reaches as a script does. */
const MOVES = ["a press on the tree", "a screen reader's cursor"] as const;

/** Moves the focus onto `tree` the way `move` names. */
async function moveOnto(
  tree: HTMLElement,
  move: (typeof MOVES)[number],
  user: UserEvent
): Promise<void> {
  if (move === "a press on the tree") await user.click(tree);
  else await act(async () => tree.focus());
}

// A focus that moves onto the view's tree from inside the stage is the reader's, and rests there:
// the stage gives a focus on the tree to the Retry only where it comes from outside the view, as
// the chord's does.
test("a focus the reader moves off a failed read's Retry onto the view's tree stays on the tree", async () => {
  const user = userEvent.setup();
  for (const move of MOVES) {
    const page = render(<RefusedViewPage drawn={false} late={false} />);
    try {
      const elsewhere = page.getByRole("button", { name: "Elsewhere" });
      await act(async () => elsewhere.focus());
      // The control elsewhere holds the focus, so the stage takes none, and the reader Tabs from
      // it to the Retry.
      page.rerender(<RefusedViewPage drawn late={false} />);
      const retry = page.getByRole("button", { name: "Retry" });
      const tree = page.getByRole("tree", { name: CONTROL_LABELS.tree });
      await user.tab();
      expect(document.activeElement, "Tab from the control elsewhere left the Retry").toBe(retry);
      await moveOnto(tree, move, user);
      await frames(3);
      expect(
        document.activeElement,
        `${move} moved the focus off the Retry onto the tree, and the stage took it back`
      ).toBe(tree);
      // A focus on the tree from a control outside the view is still taken as the chord's.
      await act(async () => elsewhere.focus());
      await act(async () => tree.focus());
      await waitFor(() =>
        expect(
          document.activeElement,
          "a focus on the tree from the control elsewhere left the focus elsewhere than the Retry"
        ).toBe(retry)
      );
    } finally {
      page.unmount();
    }
  }
});

// The chord's focus on the tree comes in the frame after the stage gave the Retry the focus
// (`reachPart`), and is taken again; a reader's move comes later, and a focus moved off that same
// Retry onto the tree once a frame has passed rests there.
test("a focus the reader moves onto the view's tree, a frame after the stage gave its Retry the focus, stays on the tree", async () => {
  const user = userEvent.setup();
  for (const move of MOVES) {
    (document.activeElement as HTMLElement | null)?.blur();
    const page = render(<RefusedViewPage drawn late={false} />);
    try {
      const retry = page.getByRole("button", { name: "Retry" });
      await waitFor(() => expect(document.activeElement).toBe(retry));
      await frames(1);
      const tree = page.getByRole("tree", { name: CONTROL_LABELS.tree });
      await moveOnto(tree, move, user);
      await frames(3);
      expect(
        document.activeElement,
        `${move} a frame after the stage gave the Retry the focus moved it onto the tree, and the stage took it back`
      ).toBe(tree);
    } finally {
      page.unmount();
    }
  }
});

// The app focuses its main element at each page change but the first (`app.tsx`), so a view's
// address opened from another page starts with the focus on what holds the stage. Nobody put it
// there, as nobody puts it on the page itself, and a refused read's Retry takes it. The case's
// container stands for the app's main element.
test("a view opened from another page gives its failed read's Retry the focus the app put on what holds the view", async () => {
  for (const late of [false, true]) {
    const page = render(<RefusedViewPage drawn={false} late={late} />);
    try {
      const { container } = page;
      container.tabIndex = -1;
      await act(async () => container.focus());
      expect(document.activeElement).toBe(container);
      page.rerender(<RefusedViewPage drawn late={late} />);
      const retry = await page.findByRole("button", { name: "Retry" });
      await waitFor(() =>
        expect(
          document.activeElement,
          `the view, with its read refused ${late ? "after" : "as"} it mounted, left the focus on what holds it`
        ).toBe(retry)
      );
    } finally {
      page.unmount();
    }
  }
});
