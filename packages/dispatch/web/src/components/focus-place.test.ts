import { expect, jest, spyOn, test } from "bun:test";
import { renderHook, waitFor } from "@testing-library/react";

import { KeymapProvider } from "../features/shell/KeymapProvider";
import { PANEL_ATTRIBUTE, takesText, useKeymap } from "../features/shell/keymap";
import { SEND_DEADLINE_MS } from "../features/writes/keyed-write";
import { CONTROL } from "./control-names";
import { inRow, useFocusRescue } from "./focus-place";
import { ROW_MARKER_ATTRIBUTE } from "./row-marker";

// The page's focus rescue, read in happy-dom with the page built by hand. A focused element taken
// away fires a `focusout` in Chromium while it is still in the page, and none at all in Firefox
// (measured, INFRA-3135 round 7) nor in happy-dom: `lose` fires it as Chromium does before the
// element goes, and a bare `remove()` is the Firefox case, which the rescue sees by watching the
// page. A `blur()` fires its own.

/** The main content the shell hands the rescue, as `app.tsx` hands it its `mainRef`. */
const mainRef: { current: HTMLElement | null } = { current: null };

/** The page's main content holding one button per label, in the document. */
function mainOf(...labels: string[]): { main: HTMLElement; buttons: HTMLButtonElement[] } {
  const main = document.createElement("main");
  main.id = "main-content";
  const buttons = labels.map((label) => buttonIn(main, label));
  document.body.append(main);
  mainRef.current = main;
  return { buttons, main };
}

/** Takes `element` away while it holds the focus, firing the `focusout` Chromium fires. */
function lose(element: HTMLElement, leave: () => void = () => element.remove()): void {
  element.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
  leave();
}

/** One animation frame. happy-dom runs it on the real clock, under a fake one too. */
async function frame(): Promise<void> {
  const done = Promise.withResolvers<void>();
  requestAnimationFrame(() => done.resolve());
  await done.promise;
}

/** Lets the rescue's check run: it runs a frame and then a task after the loss, so a frame and
 *  two tasks here come after it. The tasks are zero-delay turns of the event loop that order this
 *  after the check, not a wait for time to pass. */
async function settled(): Promise<void> {
  await frame();
  for (const _ of [0, 1]) {
    const task = Promise.withResolvers<void>();
    setTimeout(task.resolve, 0);
    await task.promise;
  }
}

/** Runs `body` with the rescue mounted, and takes the page and the rescue away after it. */
async function withRescue(body: () => Promise<void>): Promise<void> {
  const rescue = renderHook(() => useFocusRescue(mainRef));
  try {
    await body();
  } finally {
    rescue.unmount();
    mainRef.current?.remove();
    mainRef.current = null;
    for (const dialog of document.querySelectorAll('[role="dialog"]')) dialog.remove();
    window.history.replaceState(null, "", "/");
  }
}

// (a) A control taken away hands the focus to the control now at its place.
test("a control taken away hands the focus to the control now at its place", async () => {
  await withRescue(async () => {
    const [first, second, third] = mainOf("First", "Second", "Third").buttons;
    second?.focus();
    lose(second as HTMLElement);
    expect(document.activeElement).toBe(document.body);
    await waitFor(() => expect(third).toHaveFocus());
    expect(first).not.toHaveFocus();
  });
});

// (b) With no control after it, the focus goes to the one before it.
test("the last control taken away hands the focus to the one before it", async () => {
  await withRescue(async () => {
    const [, second, third] = mainOf("First", "Second", "Third").buttons;
    third?.focus();
    lose(third as HTMLElement);
    await waitFor(() => expect(second).toHaveFocus());
  });
});

// (c) A control its own press disabled gets the focus back once it is enabled again.
test("a control that disabled itself gets the focus back when it is enabled again", async () => {
  await withRescue(async () => {
    const [, saving, after] = mainOf("Before", "Save", "After").buttons;
    const button = saving as HTMLButtonElement;
    button.focus();
    // Chromium fires the `focusout` just after the disable; happy-dom blurs no disabled button,
    // so here it comes just before it. The check reads the page only later either way.
    button.blur();
    button.disabled = true;
    await settled();
    // While the write is out the rescue waits on it, rather than sending the focus elsewhere.
    expect(document.activeElement).toBe(document.body);
    expect(after).not.toHaveFocus();
    button.disabled = false;
    await waitFor(() => expect(button).toHaveFocus());
  });
});

// (h) A component that watches its own disabled control, as the Agents composer watches its
// fieldset (`refocusComposer`), starts its watcher after the rescue's wait has begun, and hands
// the focus to its own target once the control is enabled again: the focus ends on that target.
// The rescue settles only after the components have had their turn with that same change.
test("a component's own watcher, started after the rescue's wait, hands the focus on first", async () => {
  await withRescue(async () => {
    const [box, sending] = mainOf("Message", "Send", "After").buttons;
    const send = sending as HTMLButtonElement;
    send.focus();
    send.blur();
    send.disabled = true;
    await settled();
    expect(document.activeElement).toBe(document.body);
    const watcher = new MutationObserver(() => {
      if (send.matches(":disabled")) return;
      watcher.disconnect();
      if (document.activeElement === document.body) box?.focus();
    });
    watcher.observe(send, { attributeFilter: ["disabled"] });
    send.disabled = false;
    await settled();
    expect(box).toHaveFocus();
    expect(send).not.toHaveFocus();
  });
});

// (k) A control the press disabled and enabled again before the check ran is no control the
// reader moved off: it was taken from them, so it gets the focus back.
test("a control enabled again before the check runs gets the focus back", async () => {
  await withRescue(async () => {
    const [, saving, after] = mainOf("Before", "Save", "After").buttons;
    const button = saving as HTMLButtonElement;
    button.focus();
    button.blur();
    button.disabled = true;
    // The `focusout` Chromium fires once the control is disabled, which happy-dom does not.
    button.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    button.disabled = false;
    await settled();
    expect(button).toHaveFocus();
    expect(after).not.toHaveFocus();
  });
});

// (l) The wait outlasts the app's write deadline, so a slow write's control, enabled again after
// the 10 s the wait used to give it, still gets the focus back rather than losing it elsewhere.
test("a control disabled for longer than 10 s still gets the focus back when it is enabled", async () => {
  jest.useFakeTimers();
  try {
    await withRescue(async () => {
      const [, saving, after] = mainOf("Before", "Save", "After").buttons;
      const button = saving as HTMLButtonElement;
      button.focus();
      button.blur();
      button.disabled = true;
      await frame();
      jest.advanceTimersByTime(0);
      jest.advanceTimersByTime(SEND_DEADLINE_MS - 1_000);
      await frame();
      jest.advanceTimersByTime(0);
      expect(document.activeElement).toBe(document.body);
      expect(after).not.toHaveFocus();
      button.disabled = false;
      await frame();
      await frame();
      jest.advanceTimersByTime(0);
      expect(button).toHaveFocus();
    });
  } finally {
    jest.useRealTimers();
  }
});

// (d) A dialog that closes over an opener its own press disabled hands the focus to the
// opener's place in the main content, the region under the dialog.
test("a control in a dialog that closes hands the focus to the opener's place under it", async () => {
  await withRescue(async () => {
    const [, opener, after] = mainOf("Before", "Decline", "After").buttons;
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    dialog.setAttribute("aria-modal", "true");
    const confirm = document.createElement("button");
    confirm.type = "button";
    confirm.textContent = "Confirm";
    dialog.append(confirm);
    document.body.append(dialog);
    opener?.focus();
    confirm.focus();
    // The confirm disables the opener while its write is out and closes the dialog, whose
    // return of the focus to that opener takes nothing.
    (opener as HTMLButtonElement).disabled = true;
    lose(confirm, () => dialog.remove());
    opener?.focus();
    expect(document.activeElement).toBe(document.body);
    await waitFor(() => expect(after).toHaveFocus());
  });
});

// (m) A panel that is no modal - a menu's, drawn outside the main content - closed by a press on
// the page outside it, hands the focus back to the trigger that opened it, not to the last
// control of the document.
test("a panel that is no modal, closed by a press outside the main content, hands the focus to its trigger", async () => {
  await withRescue(async () => {
    const [trigger] = mainOf("Filters", "Other", "Last").buttons;
    const panel = document.createElement("div");
    panel.setAttribute("role", "dialog");
    const box = document.createElement("input");
    panel.append(box);
    document.body.append(panel);
    trigger?.focus();
    box.focus();
    box.blur();
    panel.remove();
    await waitFor(() => expect(trigger).toHaveFocus());
  });
});

/** A `MultiSelect` list's panel, its text box and one of its options. */
interface ListPanel {
  box: HTMLInputElement;
  option: HTMLButtonElement;
  panel: HTMLElement;
}

/** A list's panel portalled to the end of the body as `AnchoredPanel` portals it: no role, the
 *  keymap's panel attribute, its text box and one option. */
function listPanel(): ListPanel {
  const panel = document.createElement("div");
  panel.setAttribute(PANEL_ATTRIBUTE, "");
  panel.tabIndex = -1;
  const box = document.createElement("input");
  box.type = "search";
  panel.append(box);
  const option = buttonIn(panel, "Done");
  option.setAttribute("role", "option");
  document.body.append(panel);
  return { box, option, panel };
}

/** A list opened from `trigger`: the trigger focused, then the list's text box, as the list's own
 *  open puts the focus there, so the rescue notes the trigger as the panel's opener. */
function openListFrom(trigger: HTMLElement): ListPanel {
  trigger.focus();
  const list = listPanel();
  list.box.focus();
  return list;
}

/** A pointer press on `element`: its `pointerdown`, then its `pointerup`. */
function pointerPress(element: Element): void {
  element.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
  element.dispatchEvent(new PointerEvent("pointerup", { bubbles: true }));
}

/** A pointer pick on the list's option, which keeps the focus in its text box (the option's
 *  `mousedown` is prevented), and the pick's commit taking the list's panel away, with the
 *  `focusout` Chromium fires for the box or with none, as Firefox and WebKit do. */
function pickAway(list: ListPanel, withFocusout: boolean): void {
  pointerPress(list.option);
  if (withFocusout) lose(list.box, () => list.panel.remove());
  else list.panel.remove();
}

// (at) A list's panel - an `AnchoredPanel` with no role, portalled outside the main content and
// marked with the keymap's panel attribute - is a region of its own, opened from its trigger. A
// pointer pick on one of its options keeps the focus in the list's text box, and the pick's
// commit takes the panel away. The press was inside the panel, so the loss is the reader's, and
// the focus goes back to the trigger in the main content, never to the sidebar's link before it.
// The same with a `focusout` (Chromium) or none (Firefox and WebKit).
test("a list's panel that a pointer pick takes away hands the focus back to its trigger", async () => {
  const aside = document.createElement("aside");
  const link = document.createElement("a");
  link.href = "/";
  link.textContent = "Inbox";
  aside.append(link);
  document.body.prepend(aside);
  try {
    for (const withFocusout of [true, false]) {
      await withRescue(async () => {
        const [trigger, after] = mainOf("Filter Status", "After").buttons;
        const list = openListFrom(trigger as HTMLButtonElement);
        pickAway(list, withFocusout);
        await settled();
        expect(trigger).toHaveFocus();
        expect(link).not.toHaveFocus();
        expect(after).not.toHaveFocus();
      });
    }
  } finally {
    aside.remove();
  }
});

// (au) So for a list opened from inside a filter panel - a `PanelTrigger` panel, a dialog that
// carries the panel attribute too - and portalled outside it: the pick that takes the list away
// hands the focus back to the list's trigger inside the filter panel, which stays open, and to
// none of that panel's other controls nor the page behind it.
test("a list opened from inside a filter panel, taken away by a pick, hands the focus back to its trigger there", async () => {
  for (const withFocusout of [true, false]) {
    await withRescue(async () => {
      const [filters] = mainOf("Filters", "After").buttons;
      const filterPanel = document.createElement("div");
      filterPanel.setAttribute("role", "dialog");
      filterPanel.setAttribute(PANEL_ATTRIBUTE, "");
      const [first, trigger, last] = ["Assignee", "Status", "Labels"].map((label) =>
        buttonIn(filterPanel, label)
      );
      document.body.append(filterPanel);
      filters?.focus();
      const list = openListFrom(trigger as HTMLButtonElement);
      pickAway(list, withFocusout);
      await settled();
      expect(trigger).toHaveFocus();
      expect(first).not.toHaveFocus();
      expect(last).not.toHaveFocus();
      expect(filters).not.toHaveFocus();
    });
  }
});

// (r) WebKit can hold a page's animation frames back for seconds while nothing is drawn: the
// check goes on without its frame rather than leaving the reader on the page that long.
test("a loss is answered even while the page's animation frames are held back", async () => {
  const frames = spyOn(window, "requestAnimationFrame").mockImplementation(() => 0);
  try {
    await withRescue(async () => {
      const [, second, third] = mainOf("First", "Second", "Third").buttons;
      second?.focus();
      lose(second as HTMLElement);
      await waitFor(() => expect(third).toHaveFocus());
    });
  } finally {
    frames.mockRestore();
  }
});

// (q) A control the reader's own Escape takes away - an editor Escape closes - is the reader
// leaving it: nothing moves, so the heading after it is not handed the focus, and the focus stays
// where Escape left it.
test("a control taken away by the reader's Escape moves nothing", async () => {
  await withRescue(async () => {
    const [, editor, heading] = mainOf("Before", "Editor", "Heading").buttons;
    editor?.focus();
    document.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Escape" }));
    lose(editor as HTMLElement);
    await settled();
    expect(document.activeElement).toBe(document.body);
    expect(heading).not.toHaveFocus();
  });
});

// (p) A press on the page while the rescue waits on a disabled control is the reader moving on:
// the control enabled again later does not pull the focus back from the page.
test("a pointer press on the page during the wait leaves the focus there when the control is enabled", async () => {
  await withRescue(async () => {
    const [, saving] = mainOf("Message", "Send").buttons;
    const send = saving as HTMLButtonElement;
    send.focus();
    send.blur();
    send.disabled = true;
    await settled();
    document.body.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    send.disabled = false;
    await settled();
    expect(document.activeElement).toBe(document.body);
  });
});

// (o) A dialog the reader opened with the focus on the page itself - a single-key shortcut with
// nothing focused - gives the focus back to the page when it closes. That is where the reader
// was, so it is no loss: nothing moves, the page's search box least of all, which would take the
// reader's next key as text.
test("a modal dialog opened with the focus on the page closes and leaves the focus on the page", async () => {
  await withRescue(async () => {
    const { main } = mainOf("First", "Second");
    const search = document.createElement("input");
    search.setAttribute("data-control", "search-box");
    main.prepend(search);
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    dialog.setAttribute("aria-modal", "true");
    const box = document.createElement("input");
    dialog.append(box);
    document.body.append(dialog);
    expect(document.activeElement).toBe(document.body);
    box.focus();
    lose(box, () => dialog.remove());
    await settled();
    expect(document.activeElement).toBe(document.body);
    expect(search).not.toHaveFocus();
  });
});

// (i) Firefox and WebKit fire no `focusout` for a focused control taken away: the rescue sees it
// go by watching the page, and hands the focus on all the same.
test("a control taken away with no focusout, as Firefox does, hands the focus to the control now at its place", async () => {
  await withRescue(async () => {
    const [, second, third] = mainOf("First", "Second", "Third").buttons;
    second?.focus();
    second?.remove();
    await waitFor(() => expect(third).toHaveFocus());
  });
});

// (j) So for a control whose parent is taken away: its place is where the parent stood.
test("a control whose parent is taken away with no focusout hands the focus to the control after the parent", async () => {
  await withRescue(async () => {
    const { buttons, main } = mainOf("First", "Last");
    const [first, last] = buttons;
    const group = document.createElement("div");
    const inner = ["Inner one", "Inner two"].map((label) => {
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = label;
      group.append(button);
      return button;
    });
    main.insertBefore(group, last ?? null);
    inner[1]?.focus();
    group.remove();
    await waitFor(() => expect(last).toHaveFocus());
    expect(first).not.toHaveFocus();
  });
});

// (e) A focus the reader put on the page stays there; the same control taken away would have
// been rescued.
test("a deliberate blur onto the page moves nothing", async () => {
  await withRescue(async () => {
    const [first, second] = mainOf("First", "Second").buttons;
    first?.focus();
    first?.blur();
    await settled();
    expect(document.activeElement).toBe(document.body);

    second?.focus();
    lose(second as HTMLElement);
    await waitFor(() => expect(first).toHaveFocus());
  });
});

// (f) A loss on a page the reader has since left moves nothing: the new page owns its focus. The
// same loss on the page it happened on is rescued.
test("a loss followed by a change of page moves nothing", async () => {
  await withRescue(async () => {
    const [first, second, third] = mainOf("First", "Second", "Third").buttons;
    second?.focus();
    lose(second as HTMLElement);
    window.history.pushState(null, "", "/elsewhere");
    await settled();
    expect(document.activeElement).toBe(document.body);

    first?.focus();
    lose(first as HTMLElement);
    await waitFor(() => expect(third).toHaveFocus());
  });
});

// (n) A navigation writes the new address before the old page goes, so the loss is read against
// the address the control was focused on: the change comes first here, then the control goes.
test("a control taken away after the address changed moves nothing", async () => {
  await withRescue(async () => {
    const [, second, third] = mainOf("First", "Second", "Third").buttons;
    second?.focus();
    window.history.pushState(null, "", "/elsewhere");
    lose(second as HTMLElement);
    await settled();
    expect(document.activeElement).toBe(document.body);
    expect(third).not.toHaveFocus();
  });
});

// (g) A component's own hand-off runs first - here in the frame after the loss, moving the focus
// only while it is still on the page - and the rescue then leaves it where it landed. Without a
// hand-off the same loss is rescued.
test("a component that already moved the focus is left alone", async () => {
  await withRescue(async () => {
    const [first, second, third] = mainOf("First", "Second", "Third").buttons;
    first?.focus();
    lose(first as HTMLElement);
    requestAnimationFrame(() => {
      if (document.activeElement === document.body) third?.focus();
    });
    await settled();
    expect(third).toHaveFocus();
    expect(second).not.toHaveFocus();

    lose(third as HTMLElement);
    await waitFor(() => expect(second).toHaveFocus());
  });
});

// (s) A loss inside a dialog that stays open is answered inside that dialog. The reader is in a
// modal the page behind it cannot be reached from, so a place the dialog no longer draws - two
// of its controls went with the press, as the search bar's Retry takes its own failure block
// away - hands the focus to the dialog's own last control, and never to the page's search box
// behind it, whose next keystroke would be typed into it. Measured in Firefox, INFRA-3135 round
// 7: the search bar's Retry put the focus outside the open palette in 2 of 12 runs.
test("a loss in a dialog that stays open hands the focus inside it, never to the page behind", async () => {
  await withRescue(async () => {
    const { main } = mainOf("Before");
    const pageSearch = document.createElement("input");
    pageSearch.type = "search";
    pageSearch.setAttribute("aria-label", "Search issues");
    pageSearch.setAttribute("data-control", CONTROL.searchBox);
    main.append(pageSearch);
    const dialog = document.createElement("div");
    dialog.setAttribute("aria-modal", "true");
    dialog.setAttribute("role", "dialog");
    const kept = document.createElement("input");
    kept.type = "search";
    kept.setAttribute("aria-label", "Search");
    const filter = document.createElement("button");
    filter.type = "button";
    filter.textContent = "Filter";
    const retry = document.createElement("button");
    retry.type = "button";
    retry.textContent = "Retry";
    dialog.append(kept, filter, retry);
    document.body.append(dialog);
    retry.focus();
    // The retry's answer takes the failure block away, and with it the control counted before
    // it, so neither the place nor the one under it is a control the dialog still draws.
    lose(retry, () => {
      retry.remove();
      filter.remove();
    });
    expect(document.activeElement).toBe(document.body);
    await settled();
    expect(dialog.contains(document.activeElement)).toBe(true);
    expect(pageSearch).not.toHaveFocus();
  });
});

/** A button labelled `label`, put in `parent` before `before`, else at its end. */
function buttonIn(parent: Element, label: string, before: Node | null = null): HTMLButtonElement {
  const button = document.createElement("button");
  button.type = "button";
  button.textContent = label;
  parent.insertBefore(button, before);
  return button;
}

/** The page's own search box, at the end of `main`, where a fallback behind a dialog would land. */
function pageSearchIn(main: HTMLElement): HTMLInputElement {
  const search = document.createElement("input");
  search.setAttribute("aria-label", "Search issues");
  search.setAttribute("data-control", CONTROL.searchBox);
  main.append(search);
  return search;
}

// (t) A panel that is no modal - a menu's, drawn outside the main content - whose last two
// controls go with the press keeps the focus inside it: its own first control, never the page's
// search box.
test("a panel that is no modal, losing its last controls, keeps the focus inside it", async () => {
  await withRescue(async () => {
    const pageSearch = pageSearchIn(mainOf("Before").main);
    const panel = document.createElement("div");
    panel.setAttribute("role", "dialog");
    document.body.append(panel);
    const [first, second, third] = ["Todo", "Done", "Clear"].map((label) => buttonIn(panel, label));
    third?.focus();
    lose(third as HTMLElement, () => {
      third?.remove();
      second?.remove();
    });
    await settled();
    expect(first).toHaveFocus();
    expect(pageSearch).not.toHaveFocus();
  });
});

// (u) So does the Inbox drawer, a modal dialog of rows: its last two rows taken together by an
// update, with no press of the reader's, the focus stays in the drawer. Each `<li>` is a row by its
// element alone (`row-marker.ts`'s `ROW_SELECTOR`), so the first row's control, the nearest one
// before them, is no landing for a loss the reader did not cause (ag): the box the rows are drawn
// in takes the focus instead, which here, with no box inside it that clips, is the drawer itself.
test("the Inbox drawer, losing its last rows, keeps the focus inside it", async () => {
  await withRescue(async () => {
    const pageSearch = pageSearchIn(mainOf("Before").main);
    const drawer = document.createElement("div");
    drawer.setAttribute("role", "dialog");
    drawer.setAttribute("aria-modal", "true");
    const list = document.createElement("ul");
    drawer.append(list);
    document.body.append(drawer);
    const rows = ["First", "Second", "Third"].map((label) => {
      const row = document.createElement("li");
      list.append(row);
      return buttonIn(row, `Answer ${label}`);
    });
    rows[2]?.focus();
    lose(rows[2] as HTMLElement, () => {
      rows[2]?.closest("li")?.remove();
      rows[1]?.closest("li")?.remove();
    });
    await settled();
    expect(drawer).toHaveFocus();
    expect(rows[0]).not.toHaveFocus();
    expect(pageSearch).not.toHaveFocus();
  });
});

// (v) The main content, losing a block of controls at its end, hands the focus to the nearest
// control before the block, however many went together - here its filter trigger, then, past its
// search box, which takes typed text and so is no landing for a loss the reader did not cause, its
// first control as each is taken in turn - and to itself once it draws none, never to a control
// outside it, the sidebar's here. Changed by the tree's architect in round 9
// (2026-11-15T08:03Z): this case once expected the search box first, which only Chromium's path
// gave; the nearest earlier control is the one landing every engine agrees on, and the one a
// keyboard reader expects, where the block was rather than at the top of the page.
test("the main content losing a block at its end goes to the nearest control before it, then itself", async () => {
  const aside = document.createElement("aside");
  const sidebar = buttonIn(aside, "Sidebar");
  document.body.prepend(aside);
  try {
    await withRescue(async () => {
      const [first] = mainOf("First").buttons;
      const main = mainRef.current as HTMLElement;
      const search = pageSearchIn(main);
      const trigger = buttonIn(main, "Filters");
      trigger.setAttribute("data-control", CONTROL.panelTrigger);
      /** Three controls at the end of the main content, the last focused and all three taken. */
      const loseThree = async () => {
        const three = ["One", "Two", "Three"].map((label) => buttonIn(main, label));
        three[2]?.focus();
        lose(three[2] as HTMLElement, () => {
          for (const each of three) each.remove();
        });
        await settled();
      };
      await loseThree();
      expect(trigger).toHaveFocus();
      trigger.remove();
      await loseThree();
      expect(first).toHaveFocus();
      expect(search).not.toHaveFocus();
      search.remove();
      await loseThree();
      expect(first).toHaveFocus();
      first?.remove();
      const last = buttonIn(main, "Last");
      last.focus();
      lose(last);
      await settled();
      expect(main).toHaveFocus();
      expect(sidebar).not.toHaveFocus();
    });
  } finally {
    aside.remove();
  }
});

// (be) The edge of (v): a loss the reader did not cause, where every control before the lost
// place takes typed text - the page's search box and a note field - lands on none of them, nor
// on the body: the main content itself takes the focus, as its tabindex="-1" stop, and nothing
// scrolls, so the page's single keys answer from there. With Chromium's `focusout` and without.
test("an uncaused loss whose every earlier control takes text lands on the main content itself", async () => {
  for (const withFocusout of [true, false]) {
    const scrolls = spyOn(Element.prototype, "scrollIntoView");
    try {
      await withRescue(async () => {
        const { main } = mainOf();
        const search = pageSearchIn(main);
        const note = document.createElement("textarea");
        note.setAttribute("aria-label", "Note");
        main.append(note);
        const gone = buttonIn(main, "Gone");
        gone.focus();
        if (withFocusout) lose(gone);
        else gone.remove();
        await settled();
        expect(main).toHaveFocus();
        expect(search).not.toHaveFocus();
        expect(note).not.toHaveFocus();
        expect(document.activeElement).not.toBe(document.body);
      });
      expect(scrolls).not.toHaveBeenCalled();
    } finally {
      scrolls.mockRestore();
    }
  }
});

// (w) A control outside the main content - the sidebar's - whose place has gone hands the focus
// to the sidebar's control before it, and never into the main content or onto the body.
test("a control outside the main content whose place has gone stays outside it, on the control before it", async () => {
  const aside = document.createElement("aside");
  const [, middle, last] = ["Inbox", "Projects", "Agents"].map((label) => buttonIn(aside, label));
  document.body.prepend(aside);
  try {
    await withRescue(async () => {
      const [inMain] = mainOf("In the main content").buttons;
      last?.focus();
      lose(last as HTMLElement);
      await settled();
      expect(middle).toHaveFocus();
      expect(inMain).not.toHaveFocus();
    });
  } finally {
    aside.remove();
  }
});

// (x) A modal dialog that loses every control takes the focus itself, given a Tab stop of none
// for it when it has none of its own.
test("a modal dialog that loses every control takes the focus itself", async () => {
  await withRescue(async () => {
    const pageSearch = pageSearchIn(mainOf("Before").main);
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    dialog.setAttribute("aria-modal", "true");
    document.body.append(dialog);
    const only = buttonIn(dialog, "Retry");
    only.focus();
    lose(only);
    await settled();
    expect(dialog).toHaveFocus();
    expect(dialog).toHaveAttribute("tabindex", "-1");
    expect(pageSearch).not.toHaveFocus();
  });
});

// (y) A dialog hidden while it stays mounted - the phone thread kept for a reply - has gone as
// much as one taken away: a loss in it goes back to the control that opened it, still in the
// page, rather than to the hidden dialog itself.
test("a dialog hidden while it stays mounted hands the focus back to its opener", async () => {
  await withRescue(async () => {
    const [, opener] = mainOf("Before", "Open thread", "After").buttons;
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    dialog.setAttribute("aria-modal", "true");
    document.body.append(dialog);
    const reply = buttonIn(dialog, "Reply");
    opener?.focus();
    reply.focus();
    // Back hides the dialog under the focus, and its `focusout` comes once it is hidden.
    dialog.hidden = true;
    reply.blur();
    await settled();
    expect(opener).toHaveFocus();
  });
});

// (z) The same loss lands on the same control in every engine. Two controls at the end of a
// region go in one commit, the last holding the focus. Chromium fires a `focusout` while both
// are still counted, Firefox and WebKit fire none and the rescue reads the removal; the reader
// pressed the same thing either way. Measured by hand on dispatch:local at c3373441, INFRA-3135
// round 8: with the project List's last two pager controls taken away, Firefox and WebKit land
// on "‹ Previous", the control before them, and Chromium on the page's search box; in the Inbox
// drawer, Firefox and WebKit on "Decline to answer" and Chromium on Close at the drawer's top.
test("two controls taken away together at a region's end land the focus on one control, focusout or none", async () => {
  /** Builds the region, takes its last two controls away while the last holds the focus, and
   *  answers the label of the control the rescue landed on. */
  const landing = async (
    region: () => HTMLElement,
    withFocusout: boolean
  ): Promise<string | null | undefined> => {
    let label: string | null | undefined;
    await withRescue(async () => {
      const inside = region();
      const controls = ["One", "Two", "Three", "Four", "Five"].map((name) =>
        buttonIn(inside, name)
      );
      const [, , , fourth, fifth] = controls;
      fifth?.focus();
      const leave = () => {
        fifth?.remove();
        fourth?.remove();
      };
      if (withFocusout) lose(fifth as HTMLElement, leave);
      else leave();
      await settled();
      label = document.activeElement?.textContent;
    });
    return label;
  };
  const inMain = () => mainOf().main;
  const inDialog = () => {
    mainOf("Before");
    const dialog = document.createElement("div");
    dialog.setAttribute("aria-modal", "true");
    dialog.setAttribute("role", "dialog");
    document.body.append(dialog);
    return dialog;
  };
  for (const region of [inMain, inDialog]) {
    const chromium = await landing(region, true);
    const firefox = await landing(region, false);
    expect(chromium).toBe(firefox);
  }
});

/** Lays `element` out `top` px down the window and `height` px tall: where a scroll left it,
 *  which happy-dom, laying nothing out, does not say. */
function placeAt(element: Element, top: number, height = 40): void {
  element.getBoundingClientRect = () => new DOMRect(0, top, 200, height);
}

/** A box that scrolls by itself, laid out from 100 px to 400 px down the window, holding one
 *  button per label, appended to `parent`. */
function scrollBoxIn(
  parent: Element,
  ...labels: string[]
): { box: HTMLElement; buttons: HTMLButtonElement[] } {
  const box = document.createElement("div");
  parent.append(box);
  placeAt(box, 100, 300);
  return { box, buttons: labels.map((label) => buttonIn(box, label)) };
}

// (aa) A loss the reader can no longer see moves nothing and scrolls nothing. The reader scrolled
// the focused element out of their sight - out of the window by the page's scroll, or out of the
// box it scrolls in while that box stays on screen - and then it went: a row a virtual list
// stopped drawing, or a row a live update took away. The focus stays on the page rather than the
// list or the page being pulled back to that place and a row the reader did not pick taking the
// focus, whether the engine fired a `focusout` for it (Chromium) or not (Firefox and WebKit).
// Asked for in INFRA-3135's review round 1 (thread PRRT_kwDORFy7ds63p6y_): with a row's checkbox
// focused, wheel steps over a virtual table pulled its box back from 690 px to 24 px, and a
// focused row a live update took away pulled the page back from 3,000 px to its top.
test("a loss of an element scrolled out of sight moves nothing and scrolls nothing", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    for (const withFocusout of [true, false]) {
      const goes = (element: HTMLElement) => {
        if (withFocusout) lose(element);
        else element.remove();
      };
      // Out of the window: the page scrolled it past the window's bottom edge.
      await withRescue(async () => {
        const [, second, third] = mainOf("First", "Second", "Third").buttons;
        second?.focus();
        placeAt(second as HTMLElement, 5_000);
        document.dispatchEvent(new Event("scroll"));
        goes(second as HTMLElement);
        await settled();
        expect(document.activeElement).toBe(document.body);
        expect(third).not.toHaveFocus();
      });
      // Out of its box: the box scrolled it above its top edge, where the window still holds it.
      await withRescue(async () => {
        const { box, buttons } = scrollBoxIn(mainOf("Before").main, "One", "Two", "Three");
        const [, second, third] = buttons;
        second?.focus();
        placeAt(second as HTMLElement, 40);
        box.dispatchEvent(new Event("scroll"));
        goes(second as HTMLElement);
        await settled();
        expect(document.activeElement).toBe(document.body);
        expect(third).not.toHaveFocus();
      });
    }
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
});

// (ab) A loss the reader can still see is answered as before: an element its box scrolled but
// kept in sight, and one scrolled out of sight and back into it before it went, hand the focus
// to the control now at their place. Only the scroll that leaves nothing of it in sight counts.
test("a loss of an element still in sight is answered, however it was scrolled", async () => {
  await withRescue(async () => {
    const { box, buttons } = scrollBoxIn(mainOf("Before").main, "One", "Two", "Three");
    const [, second, third] = buttons;
    second?.focus();
    // Half of it above the box's top edge: the reader still sees the other half.
    placeAt(second as HTMLElement, 80);
    box.dispatchEvent(new Event("scroll"));
    lose(second as HTMLElement);
    await waitFor(() => expect(third).toHaveFocus());
  });
  await withRescue(async () => {
    const [, second, third] = mainOf("First", "Second", "Third").buttons;
    second?.focus();
    placeAt(second as HTMLElement, 5_000);
    document.dispatchEvent(new Event("scroll"));
    placeAt(second as HTMLElement, 300);
    document.dispatchEvent(new Event("scroll"));
    second?.remove();
    await waitFor(() => expect(third).toHaveFocus());
  });
});

// (ac) In a dialog the same loss still keeps the focus inside it, so the page behind a modal one
// is never reached, and still scrolls nothing and focuses no row the reader did not pick: the
// Inbox drawer, whose list the reader scrolled away from the focused row before a live update
// took that row away, takes the focus itself.
test("a loss scrolled out of sight in a modal dialog leaves the focus on the dialog itself, scrolling nothing", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    await withRescue(async () => {
      const pageSearch = pageSearchIn(mainOf("Before").main);
      const drawer = document.createElement("div");
      drawer.setAttribute("role", "dialog");
      drawer.setAttribute("aria-modal", "true");
      document.body.append(drawer);
      const { box, buttons } = scrollBoxIn(drawer, "Answer one", "Answer two", "Answer three");
      const [first, second] = buttons;
      first?.focus();
      placeAt(first as HTMLElement, 20);
      box.dispatchEvent(new Event("scroll"));
      first?.remove();
      await settled();
      expect(drawer).toHaveFocus();
      expect(drawer).toHaveAttribute("tabindex", "-1");
      expect(second).not.toHaveFocus();
      expect(pageSearch).not.toHaveFocus();
    });
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
});

// (ad) The wait is held to the same rule: a control the reader's press disabled, and that they
// scrolled out of their sight while its write was out, does not get the focus back once it is
// enabled again, which would pull the page back to it. Left in sight it gets it back (c).
test("a control scrolled out of sight while its write is out is left alone when it is enabled again", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    await withRescue(async () => {
      const [, saving] = mainOf("Before", "Save", "After").buttons;
      const button = saving as HTMLButtonElement;
      button.focus();
      button.blur();
      button.disabled = true;
      await settled();
      placeAt(button, 5_000);
      document.dispatchEvent(new Event("scroll"));
      button.disabled = false;
      await settled();
      await settled();
      expect(document.activeElement).toBe(document.body);
    });
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
});

// (ae) A resize can put the focused element out of the reader's sight with nothing scrolled: a
// shorter window, or a zoom, shrinks the box a table is sized to under its focused row, and the
// virtual list may then stop drawing that row. The resize reads the sight again, against every
// box around the element that clips what overflows it - this box has never scrolled - so the
// row's removal after it moves nothing and scrolls nothing. A resize that leaves the row in its
// box changes nothing: its removal is answered as before. INFRA-3135's review round 1, R10.
test("a resize that puts the focused row out of its box, then the row's removal, moves nothing", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    await withRescue(async () => {
      const { box, buttons } = scrollBoxIn(mainOf("Before").main, "One", "Two", "Three");
      box.style.overflowY = "auto";
      const [, second, third] = buttons;
      second?.focus();
      // Under the box's bottom edge at 400 px, where the window still holds it.
      placeAt(second as HTMLElement, 450);
      window.dispatchEvent(new Event("resize"));
      second?.remove();
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(third).not.toHaveFocus();
    });
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
  await withRescue(async () => {
    const { box, buttons } = scrollBoxIn(mainOf("Before").main, "One", "Two", "Three");
    box.style.overflowY = "auto";
    const [, second, third] = buttons;
    second?.focus();
    placeAt(second as HTMLElement, 300);
    window.dispatchEvent(new Event("resize"));
    second?.remove();
    await waitFor(() => expect(third).toHaveFocus());
  });
});

// (af) An element the page no longer draws is no longer read: a hidden one's box is the
// viewport's top-left corner, which a read would count as in sight. A control the reader's press
// disabled, scrolled out of their sight while its write was out, and then hidden, stays out of
// their sight however many scrolls land before the wait settles, so the focus is handed to no
// control and nothing scrolls. INFRA-3135's review round 1, R10.
test("a control hidden after the reader scrolled it out of sight stays out of sight at the scrolls after", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    await withRescue(async () => {
      const [, saving, after] = mainOf("Before", "Save", "After").buttons;
      const button = saving as HTMLButtonElement;
      button.focus();
      button.blur();
      button.disabled = true;
      await settled();
      placeAt(button, 5_000);
      document.dispatchEvent(new Event("scroll"));
      // Hidden: the box a browser gives an element under `[hidden]`, and one more page scroll
      // before the wait settles.
      button.hidden = true;
      button.getBoundingClientRect = () => new DOMRect(0, 0, 0, 0);
      document.dispatchEvent(new Event("scroll"));
      await settled();
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(after).not.toHaveFocus();
    });
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
});

/** A box that scrolls by itself, laid out from 100 px to 400 px down the window, holding one row
 *  per label - an Inbox row, a `div` carrying the row marker where the Inbox draws an `<li>`, a
 *  row by the same rule (`ROW_SELECTOR`), and the one checkbox a row marks itself by - appended
 *  to `parent`. */
function rowBoxIn(
  parent: Element,
  ...labels: string[]
): { box: HTMLElement; rows: { mark: HTMLInputElement; row: HTMLElement }[] } {
  const box = document.createElement("div");
  box.style.overflowY = "auto";
  parent.append(box);
  placeAt(box, 100, 300);
  const rows = labels.map((label) => {
    const row = document.createElement("div");
    row.setAttribute("data-inbox-row", label);
    row.setAttribute(ROW_MARKER_ATTRIBUTE, "");
    row.tabIndex = -1;
    const mark = document.createElement("input");
    mark.type = "checkbox";
    mark.setAttribute("aria-label", `Select ${label}`);
    row.append(mark);
    box.append(row);
    return { mark, row };
  });
  return { box, rows };
}

// (ag) A row a live update takes away while the reader can see it, with no press of theirs before
// it, is a loss they did not cause: the focus goes into no other row and nothing scrolls. It lands
// on the box the rows are drawn in instead, given a Tab stop for it, so the reader's next key acts
// on no row they did not pick. INFRA-3135's review round 1, follow-up F4.
test("a row removed by a live update with no press hands the focus to the box, in no row, scrolling nothing", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    await withRescue(async () => {
      const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
      const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
      second.mark.focus();
      // In sight: the mark sits inside the box, which the window draws.
      placeAt(second.mark, 200);
      box.dispatchEvent(new Event("scroll"));
      second.row.remove();
      await settled();
      expect(box).toHaveFocus();
      expect(box).toHaveAttribute("tabindex", "-1");
      expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
      expect(rows[2]?.mark).not.toHaveFocus();
    });
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
});

// (ah) The same removal, right after the reader's own press on that row, is a loss they caused:
// the hand-off it always had stands, to the control the table now draws at the lost row's place.
test("the same row removed right after the reader's press on it keeps today's hand-off", async () => {
  await withRescue(async () => {
    const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
    const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
    const third = rows[2] as { mark: HTMLInputElement; row: HTMLElement };
    second.mark.focus();
    placeAt(second.mark, 200);
    box.dispatchEvent(new Event("scroll"));
    second.mark.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    second.row.remove();
    await waitFor(() => expect(third.mark).toHaveFocus());
  });
});

// (aq) A press is the cause only of the update that answers it. The same removal as (ah), once the
// reader's press on that row has been answered - a frame and a task after it, its control held by
// no write - is a live update's, not theirs: the focus goes into no other row and nothing scrolls,
// as with no press at all (ag). The reader's next Space or `x` then marks no row they did not pick.
test("a row removed by a live update after the reader's press on it was answered hands the focus to no row", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    await withRescue(async () => {
      const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
      const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
      second.mark.focus();
      placeAt(second.mark, 200);
      box.dispatchEvent(new Event("scroll"));
      pointerPress(second.mark);
      await settled();
      second.row.remove();
      await settled();
      expect(box).toHaveFocus();
      expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
      expect(rows[2]?.mark).not.toHaveFocus();
    });
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
});

/** A press's write holding `control` while it is out, as `heldPress` holds it: the click's commit
 *  setting `aria-disabled`, then several frames of the write being out. */
async function holdFor(control: HTMLElement): Promise<void> {
  control.setAttribute("aria-disabled", "true");
  for (const _ of [0, 1, 2]) await settled();
}

// (ar) A press on a control its write holds (`aria-disabled`) is the cause of the update that
// ends the hold, however many frames the write is out: the row taken away in the commit that ends
// it - Dismiss answered - keeps the hand-off of (ah), to the control now at the row's place.
test("a row removed in the commit that ends its pressed control's hold keeps today's hand-off", async () => {
  await withRescue(async () => {
    const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
    const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
    const third = rows[2] as { mark: HTMLInputElement; row: HTMLElement };
    const dismiss = buttonIn(second.row, "Dismiss");
    dismiss.focus();
    placeAt(dismiss, 200);
    box.dispatchEvent(new Event("scroll"));
    pointerPress(dismiss);
    await holdFor(dismiss);
    expect(dismiss).toHaveFocus();
    second.row.remove();
    await waitFor(() => expect(third.mark).toHaveFocus());
  });
});

// (as) Once that hold ends with the row still there - the write answered and the row kept - the
// press has been answered a frame and a task later, and a live update taking the row away after
// that is no doing of the reader's: the focus goes into no other row and nothing scrolls (ag).
test("a row removed after its pressed control's hold ended hands the focus to no row", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    await withRescue(async () => {
      const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
      const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
      const dismiss = buttonIn(second.row, "Dismiss");
      dismiss.focus();
      placeAt(dismiss, 200);
      box.dispatchEvent(new Event("scroll"));
      pointerPress(dismiss);
      await holdFor(dismiss);
      dismiss.removeAttribute("aria-disabled");
      await settled();
      second.row.remove();
      await settled();
      expect(box).toHaveFocus();
      expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
      expect(rows[2]?.mark).not.toHaveFocus();
    });
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
});

// (ax) The hold ends at the write deadline (`RESCUE_WAIT_MS`), as the rescue's own wait does: a
// control its press's write holds that stays held past it - a write that never answers - keeps
// its press live no longer, so a live update taking its row away after that is no doing of the
// reader's: the focus goes into no other row and nothing scrolls (ag).
test("a row removed after its pressed control stayed held past the write deadline hands the focus to no row", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  jest.useFakeTimers();
  try {
    await withRescue(async () => {
      const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
      const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
      const dismiss = buttonIn(second.row, "Dismiss");
      dismiss.focus();
      placeAt(dismiss, 200);
      box.dispatchEvent(new Event("scroll"));
      pointerPress(dismiss);
      dismiss.setAttribute("aria-disabled", "true");
      // A frame and a task after the release the press finds its control held, and watches it.
      await frame();
      jest.advanceTimersByTime(0);
      // The deadline passes with the control still held; the press stops a frame and a task on.
      jest.advanceTimersByTime(SEND_DEADLINE_MS);
      await frame();
      jest.advanceTimersByTime(0);
      expect(dismiss).toHaveFocus();
      second.row.remove();
      // The rescue's check, a frame and a task after the removal its watcher reads.
      await frame();
      await frame();
      jest.advanceTimersByTime(0);
      expect(box).toHaveFocus();
      expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
      expect(rows[2]?.mark).not.toHaveFocus();
    });
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    jest.useRealTimers();
    scrolls.mockRestore();
  }
});

// (ay) A press on a control already held when it is pressed - the pager's Next on its last page, a
// select or a trigger held while an earlier write is out - is refused and leads to no write, so
// that hold is no doing of the press: the press is live a frame and a task after it and its
// release, as a press on a control held by nothing is (aq), and no longer. A live update taking
// the row away after that is no doing of the reader's: the focus goes into no other row and
// nothing scrolls (ag).
test("a row removed after a press on its control held before the press hands the focus to no row", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    await withRescue(async () => {
      const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
      const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
      const dismiss = buttonIn(second.row, "Dismiss");
      dismiss.setAttribute("aria-disabled", "true");
      dismiss.focus();
      placeAt(dismiss, 200);
      box.dispatchEvent(new Event("scroll"));
      pointerPress(dismiss);
      // Two frames and their tasks on, the control still held as it was before the press.
      await frame();
      await settled();
      expect(dismiss).toHaveFocus();
      second.row.remove();
      await settled();
      expect(box).toHaveFocus();
      expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
      expect(rows[2]?.mark).not.toHaveFocus();
    });
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
});

// (az) A second press on a control its first press holds - a double click, a repeated Enter - is
// refused by the control and leaves the first press the cause: the row taken away in the commit
// that ends the hold keeps the hand-off of (ar), to the control now at the row's place.
test("a second press on a control its first press holds keeps the first press the cause of the row's going", async () => {
  await withRescue(async () => {
    const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
    const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
    const third = rows[2] as { mark: HTMLInputElement; row: HTMLElement };
    const dismiss = buttonIn(second.row, "Dismiss");
    dismiss.focus();
    placeAt(dismiss, 200);
    box.dispatchEvent(new Event("scroll"));
    pointerPress(dismiss);
    await holdFor(dismiss);
    pointerPress(dismiss);
    await holdFor(dismiss);
    expect(dismiss).toHaveFocus();
    second.row.remove();
    await waitFor(() => expect(third.mark).toHaveFocus());
  });
});

// (ba) A second press on a control its first press holds, and that press's release, leave the
// first press's write deadline where it was: counted from the first press's release, as in (ax),
// so the reader pressing the held control again 20 s on keeps the first press live no longer. A
// live update taking the row away a little over 30 s after the first release is no doing of the
// reader's: the focus goes into no other row and nothing scrolls (ag).
test("a second press on a control its first press holds leaves the write deadline counted from the first release", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  jest.useFakeTimers();
  try {
    await withRescue(async () => {
      const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
      const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
      const dismiss = buttonIn(second.row, "Dismiss");
      dismiss.focus();
      placeAt(dismiss, 200);
      box.dispatchEvent(new Event("scroll"));
      pointerPress(dismiss);
      dismiss.setAttribute("aria-disabled", "true");
      // A frame and a task after the release the press finds its control held, and watches it.
      await frame();
      jest.advanceTimersByTime(0);
      // 20 s on, the reader presses the held control again and lets go, a frame and a task before
      // the clock moves on.
      jest.advanceTimersByTime(20_000);
      pointerPress(dismiss);
      await frame();
      jest.advanceTimersByTime(0);
      // The deadline from the first release passes with the control still held; the press stops a
      // frame and a task on.
      jest.advanceTimersByTime(SEND_DEADLINE_MS - 20_000);
      await frame();
      jest.advanceTimersByTime(0);
      expect(dismiss).toHaveFocus();
      second.row.remove();
      // The rescue's check, a frame and a task after the removal its watcher reads.
      await frame();
      await frame();
      jest.advanceTimersByTime(0);
      expect(box).toHaveFocus();
      expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
      expect(rows[2]?.mark).not.toHaveFocus();
    });
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    jest.useRealTimers();
    scrolls.mockRestore();
  }
});

// (bb) A second press on an element inside the control its first press holds - an icon drawn in
// the button - is a press on that control, which takes it without a write: the first press stays
// the cause, and the row taken away in the commit that ends the hold keeps the hand-off of (az),
// to the control now at the row's place.
test("a second press inside a control its first press holds keeps the first press the cause of the row's going", async () => {
  await withRescue(async () => {
    const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
    const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
    const third = rows[2] as { mark: HTMLInputElement; row: HTMLElement };
    const dismiss = buttonIn(second.row, "Dismiss");
    const icon = document.createElement("span");
    icon.setAttribute("aria-hidden", "true");
    dismiss.append(icon);
    dismiss.focus();
    placeAt(dismiss, 200);
    box.dispatchEvent(new Event("scroll"));
    pointerPress(dismiss);
    await holdFor(dismiss);
    pointerPress(icon);
    await holdFor(dismiss);
    expect(dismiss).toHaveFocus();
    second.row.remove();
    await waitFor(() => expect(third.mark).toHaveFocus());
  });
});

// (bf) A press in a menu or a list counts as one on the trigger that names it (`openerOf`) - the
// selection bar's Snooze, whose menu the focus entered from the row the bar's key was pressed on.
// A trigger already held when the press is made - an earlier write of the bar's still out - is no
// doing of that press, as in (ay): the press is live a frame and a task after it and its release,
// and no longer. A live update that takes the menu away after that is no doing of the reader's,
// so the focus goes back to the row it entered the menu from by no hand-off into that row: to the
// box the row is drawn in, scrolling nothing (ag).
test("a press in a menu whose trigger was held before the press is no cause of the menu's going after it", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    await withRescue(async () => {
      const { main } = mainOf("Before");
      const { box, rows } = rowBoxIn(main, "One", "Two", "Three");
      const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
      const snooze = buttonIn(main, "Snooze");
      snooze.setAttribute("aria-disabled", "true");
      second.mark.focus();
      placeAt(second.mark, 200);
      box.dispatchEvent(new Event("scroll"));
      const menu = listPanel();
      menu.panel.id = "bar-snooze-menu";
      snooze.setAttribute("aria-controls", menu.panel.id);
      menu.box.focus();
      pointerPress(menu.box);
      // Two frames and their tasks on, the trigger still held as it was before the press.
      await frame();
      await settled();
      expect(menu.box).toHaveFocus();
      lose(menu.box, () => menu.panel.remove());
      await settled();
      expect(box).toHaveFocus();
      expect(second.mark).not.toHaveFocus();
    });
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
});

/** A row's list - an Inbox row's `Hand this question to` - opened from its trigger in the row
 *  (`openListFrom`), the trigger laid out in the row box's sight. */
function rowList(row: HTMLElement): { list: ListPanel; trigger: HTMLButtonElement } {
  const trigger = buttonIn(row, "Hand this question to");
  placeAt(trigger, 200);
  return { list: openListFrom(trigger), trigger };
}

/** A pointer pick on the list's option, as a single pick makes it (`MultiSelect`'s `choose`): the
 *  focus put back on the trigger, the list's panel taken away, and the pick's write holding the
 *  trigger (`aria-disabled`) for several frames while it is out. */
async function pickAndHold(list: ListPanel, trigger: HTMLButtonElement): Promise<void> {
  pointerPress(list.option);
  trigger.focus();
  list.panel.remove();
  await holdFor(trigger);
}

// (av) A pick in a list's panel that has gone counts as a press on the trigger the list was opened
// from: the pick's write holds that trigger, and the row around it taken away in the commit that
// ends the hold - the hand-on answered, the row let go - is the reader's doing, so it keeps the
// hand-off of (ar), to the control now at the row's place.
test("a row its list's pick holds, taken away in the commit that ends the hold, keeps today's hand-off", async () => {
  await withRescue(async () => {
    const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
    const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
    const third = rows[2] as { mark: HTMLInputElement; row: HTMLElement };
    const { list, trigger } = rowList(second.row);
    await pickAndHold(list, trigger);
    box.dispatchEvent(new Event("scroll"));
    expect(trigger).toHaveFocus();
    second.row.remove();
    await waitFor(() => expect(third.mark).toHaveFocus());
  });
});

// (aw) So for (as): once the trigger's hold ends with the row still there, the pick has been
// answered a frame and a task later, and a live update taking the row away after that is no doing
// of the reader's: the focus goes into no other row and nothing scrolls (ag).
test("a row its list's pick held, taken away after the hold ended, hands the focus to no row", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    await withRescue(async () => {
      const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
      const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
      const { list, trigger } = rowList(second.row);
      await pickAndHold(list, trigger);
      box.dispatchEvent(new Event("scroll"));
      trigger.removeAttribute("aria-disabled");
      await settled();
      second.row.remove();
      await settled();
      expect(box).toHaveFocus();
      expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
      expect(rows[2]?.mark).not.toHaveFocus();
    });
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
});

// (ai) A control focused below the fold is one the engine is still to scroll into view - WebKit
// does so a frame after the focus - and a scroll the page makes first can leave it there. No read
// has then found it in the reader's sight since it took the focus, so no scroll took it out of
// their sight, and the loss their Enter on it makes is answered as one they can see. Once a read
// has found it in sight, a scroll that takes it out counts as before (aa). INFRA-3135 round 11,
// measured in WebKit at 390x664: the Inbox's list keeping its rows in place scrolled the page
// 203 px before the engine's own scroll, which had not come by the press, and in 9 of 40 runs
// that scroll was read before Enter on a thread's Dismiss, which then left the focus on the body.
test("a control focused below the fold, left there by a scroll the page made, is answered after the reader's press", async () => {
  for (const withFocusout of [true, false]) {
    await withRescue(async () => {
      const [, second, third] = mainOf("First", "Second", "Third").buttons;
      const dismiss = second as HTMLElement;
      // Below the fold where it takes the focus, and still there after the page's own scroll.
      placeAt(dismiss, 5_000);
      dismiss.focus();
      document.dispatchEvent(new Event("scroll"));
      dismiss.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }));
      if (withFocusout) lose(dismiss);
      else dismiss.remove();
      await waitFor(() => expect(third).toHaveFocus());
    });
  }
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    await withRescue(async () => {
      const [, second, third] = mainOf("First", "Second", "Third").buttons;
      const dismiss = second as HTMLElement;
      // Scrolled into the reader's sight by the engine, then out of it by the reader.
      placeAt(dismiss, 5_000);
      dismiss.focus();
      placeAt(dismiss, 300);
      document.dispatchEvent(new Event("scroll"));
      placeAt(dismiss, 5_000);
      document.dispatchEvent(new Event("scroll"));
      dismiss.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }));
      dismiss.remove();
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(third).not.toHaveFocus();
    });
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
});

/** What a press takes away and what the commit draws in its place, in `main` before `after`: the
 *  pressed control, the element taken away with it, what is drawn where that stood, and the
 *  control the focus belongs on there. */
interface Replacement {
  readonly drawn: HTMLElement;
  readonly first: HTMLElement;
  readonly pressed: HTMLButtonElement;
  readonly taken: HTMLElement;
}

/** Change answer: the answered card goes, and the card drawn where it stood repeats the ask's
 *  header, with controls of its own, above the answer form, whose first control is the first
 *  choice. */
function changeAnswer(main: HTMLElement, after: HTMLElement): Replacement {
  const taken = document.createElement("article");
  buttonIn(taken, "Copy");
  const pressed = buttonIn(taken, "Change answer");
  main.insertBefore(taken, after);
  const drawn = document.createElement("article");
  const header = document.createElement("header");
  for (const label of ["Session", "Pin"]) buttonIn(header, label);
  const form = document.createElement("form");
  const first = document.createElement("input");
  first.type = "radio";
  first.name = "choice";
  form.append(first);
  buttonIn(form, "Send");
  drawn.append(header, form);
  return { drawn, first, pressed, taken };
}

/** Add a priority: the `+` goes, and the form drawn where it stood has the list's trigger first. */
function addPriority(main: HTMLElement, after: HTMLElement): Replacement {
  const pressed = buttonIn(main, "+", after);
  const drawn = document.createElement("form");
  const first = buttonIn(drawn, "Priority to advance");
  buttonIn(drawn, "Add");
  return { drawn, first, pressed, taken: pressed };
}

// (aj) A press that draws something else where its control stood lands the focus on what it drew:
// on the first control of the form there, focused with no scroll. Change answer's card repeats
// the ask's header above its form, and the header is not the place: the walk by count lands on a
// header control, or past the card, and scrolls to it, where the reader asked for the form. Add
// a priority's form stands where its `+` stood. The same with a `focusout` (Chromium) or none
// (Firefox and WebKit).
test("a control replaced by content holding a form hands the focus to the form's first control, scrolling nothing", async () => {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    for (const replacement of [changeAnswer, addPriority]) {
      for (const withFocusout of [true, false]) {
        await withRescue(async () => {
          const { buttons, main } = mainOf("Before", "After");
          const after = buttons[1] as HTMLButtonElement;
          const { drawn, first, pressed, taken } = replacement(main, after);
          const focus = spyOn(first, "focus");
          pressed.focus();
          pressed.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
          // React takes the pressed element away, then puts what it drew where that stood.
          const replace = () => {
            taken.remove();
            main.insertBefore(drawn, after);
          };
          if (withFocusout) lose(pressed, replace);
          else replace();
          await settled();
          expect(first).toHaveFocus();
          expect(focus).toHaveBeenCalledWith({ preventScroll: true });
        });
      }
    }
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
});

// (ak) A press that takes its control away with nothing drawn in its place still walks back to
// the nearest control before it, however many went together: Dismiss on a thread's last row,
// whose row goes with the row before it. The reply form standing before them was there before the
// press, though the row the pressed one stood after has gone too, so it is nothing new in the
// place: the focus lands on the form's last control, the nearest before the place, and not on its
// first. An Undo the same commit adds in a parent of its own stands at no place of the pressed
// control's either.
test("a control taken away with nothing new in its place walks back to the nearest control before it", async () => {
  for (const withFocusout of [true, false]) {
    await withRescue(async () => {
      const { main } = mainOf();
      const notices = document.createElement("div");
      main.append(notices);
      const reply = document.createElement("form");
      const box = document.createElement("textarea");
      reply.append(box);
      const send = buttonIn(reply, "Send");
      main.append(reply);
      const rows = ["One", "Two"].map((label) => {
        const row = document.createElement("div");
        main.append(row);
        return { dismiss: buttonIn(row, `Dismiss ${label}`), row };
      });
      const dismiss = rows[1]?.dismiss as HTMLButtonElement;
      dismiss.focus();
      dismiss.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }));
      // The pressed row goes first, then the row it stood after, and the commit adds an Undo.
      const leave = () => {
        for (const { row } of [...rows].reverse()) row.remove();
        buttonIn(notices, "Undo");
      };
      if (withFocusout) lose(dismiss, leave);
      else leave();
      await settled();
      expect(send).toHaveFocus();
    });
  }
});

// (al) A sibling the same commit moves into the place of the control a press took away stood in
// the page before the press, so it is nothing new there: the focus walks back by count, as for a
// control taken away with nothing in its place, to the control now at the pressed one's place,
// and not onto the moved sibling. React moves a keyed node with `insertBefore`, which the page
// reports as its removal and then its addition in the batch that takes the pressed control away.
test("a sibling moved into a pressed control's place is nothing new there, so the focus walks back", async () => {
  for (const withFocusout of [true, false]) {
    await withRescue(async () => {
      const { buttons, main } = mainOf("Moved", "Before", "Pressed", "After");
      const [moved, , pressed, after] = buttons as [
        HTMLButtonElement,
        HTMLButtonElement,
        HTMLButtonElement,
        HTMLButtonElement,
      ];
      pressed.focus();
      pressed.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }));
      // React takes the pressed control away, then moves the first sibling into its place.
      const leave = () => {
        pressed.remove();
        main.insertBefore(moved, after);
      };
      if (withFocusout) lose(pressed, leave);
      else leave();
      await settled();
      expect(after).toHaveFocus();
    });
  }
});

/** The sidebar's pinned agents (`PinnedAgentRow`), in a list at the end of `parent`: one `<li>`
 *  per name, a row by its element alone, holding a link named for the agent and then its
 *  `Unpin <name> from the sidebar` button, save for the names in `withoutUnpin`, whose rows hold
 *  the link alone. */
function pinnedAgentsIn(
  parent: Element,
  names: readonly string[],
  withoutUnpin: readonly string[]
): { row: HTMLLIElement; unpin: HTMLButtonElement | null }[] {
  const list = document.createElement("ul");
  parent.append(list);
  return names.map((name) => {
    const row = document.createElement("li");
    const link = document.createElement("a");
    link.href = `/agents/${name}`;
    link.textContent = name;
    row.append(link);
    list.append(row);
    const unpin = withoutUnpin.includes(name)
      ? null
      : buttonIn(row, `Unpin ${name} from the sidebar`);
    return { row, unpin };
  });
}

// (bn) A press of the reader's that takes its own row away lands on the same control in the row
// that moves up into its place, so the reader can go on, unpinning as Dismiss goes on to the next
// item. Unpin on the second of the sidebar's three pinned agents takes that `<li>` away at once,
// and the third moves up, the same element, so nothing new stands there (al). The walk by count
// lands Chromium, which counts the place while the row is still in the page, on the third row's
// Unpin, and Firefox and WebKit, which count it once the row has gone, on its link; the row rule
// lands every engine on its Unpin. Where the row that moves up has no Unpin, its first control,
// its link, takes the focus. Where the row taken away was the last, no row moves up, and the
// focus walks back as before, to the Unpin of the row before it. An Unpin its press disabled
// before the row went, the disable's `focusout` first, keeps its place in its row all the same.
// By a pointer press and by Enter, with Chromium's `focusout` and without.
test("a row the reader's press takes away hands the focus to the same control in the row that moves up", async () => {
  const presses: Record<string, (control: HTMLElement) => void> = {
    Enter: (control) => {
      control.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }));
    },
    pointer: pointerPress,
  };
  const cases: {
    disable: boolean;
    landing: string;
    name: string;
    pressed: number;
    withoutUnpin: string[];
  }[] = [
    {
      disable: false,
      landing: "Unpin Gamma from the sidebar",
      name: "Unpin Beta",
      pressed: 1,
      withoutUnpin: [],
    },
    {
      disable: false,
      landing: "Gamma",
      name: "Unpin Beta, Gamma with no Unpin",
      pressed: 1,
      withoutUnpin: ["Gamma"],
    },
    {
      disable: false,
      landing: "Unpin Beta from the sidebar",
      name: "Unpin Gamma, the last row",
      pressed: 2,
      withoutUnpin: [],
    },
    {
      disable: true,
      landing: "Unpin Gamma from the sidebar",
      name: "Unpin Beta, disabled by its press",
      pressed: 1,
      withoutUnpin: [],
    },
  ];
  // Where each press landed, and where it belongs.
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  for (const { disable, landing, name, pressed, withoutUnpin } of cases) {
    for (const [by, press] of Object.entries(presses)) {
      // A disable fires its `focusout` in every engine, so that case is read with it alone.
      for (const withFocusout of disable ? [true] : [true, false]) {
        const named = `${name}, by ${by}, ${withFocusout ? "with" : "without"} a focusout`;
        await withRescue(async () => {
          const rows = pinnedAgentsIn(mainOf().main, ["Alpha", "Beta", "Gamma"], withoutUnpin);
          const { row, unpin } = rows[pressed] as { row: HTMLLIElement; unpin: HTMLButtonElement };
          unpin.focus();
          press(unpin);
          // The press's commit takes its row away at once, and the row after it moves up.
          const leave = () => row.remove();
          if (disable) unpin.disabled = true;
          if (withFocusout) lose(unpin, leave);
          else leave();
          await settled();
          landed[named] = focusedName();
        });
        belongs[named] = landing;
      }
    }
  }
  expect(landed).toEqual(belongs);
});

// (am) One batch of the page's records can hold a commit that moves the pressed control - a live
// update reordering what it stands among - and the press's own commit that takes it away and
// draws a form where it stood. With no `focusout` (Firefox and WebKit), the place is read off the
// last record that took the control, where it was taken from, and not off the move's removal,
// where it no longer stood: the focus lands on the first control of the form drawn there.
test("a control moved and then taken away in one batch hands the focus to the form drawn where it was taken from", async () => {
  await withRescue(async () => {
    const { buttons, main } = mainOf("Before", "After");
    const [before, after] = buttons as [HTMLButtonElement, HTMLButtonElement];
    const { drawn, first, pressed } = addPriority(main, before);
    pressed.focus();
    pressed.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }));
    // A live update moves the `+` after Before; the press's commit then takes it away and puts
    // its form where it stood.
    main.insertBefore(pressed, after);
    pressed.remove();
    main.insertBefore(drawn, after);
    await settled();
    expect(first).toHaveFocus();
  });
});

/** The issue title's editor, put in `main` before `after`: its text box, the heading with a Tab
 *  stop that a save draws in the box's place, and that save's commit (`replace`), which takes the
 *  box away and draws the heading there. */
function titleEditorIn(
  main: HTMLElement,
  after: HTMLElement
): { field: HTMLInputElement; heading: HTMLHeadingElement; replace: () => void } {
  const field = document.createElement("input");
  field.setAttribute("aria-label", "Issue title");
  main.insertBefore(field, after);
  const heading = document.createElement("h1");
  heading.tabIndex = 0;
  heading.textContent = "Title";
  const replace = () => {
    field.remove();
    main.insertBefore(heading, after);
  };
  return { field, heading, replace };
}

/** Saves `field` on its blur, the save's commit (`replace`) a microtask later, as React commits
 *  the update an event makes. The save listens for the field's `focusout`, as React's `onBlur`
 *  does at its root, so the rescue's own listener on the document's capture hears the blur first. */
function saveOnBlur(field: HTMLInputElement, replace: () => void): void {
  field.addEventListener("focusout", () => queueMicrotask(replace));
}

// (an) The reader's Enter that commits a field with the page's own `blur()` - the issue title's
// editor saving - is the reader leaving the field. The blur sends the focus to the page while the
// field still stands, and the save's commit takes the field away a microtask later, drawing the
// heading in its place: nothing moves, so the heading is not handed the focus and the reader's
// next key acts on the page.
test("a field the page's own blur commits leaves the focus on the page when its commit takes it away", async () => {
  await withRescue(async () => {
    const { buttons, main } = mainOf("Before", "After");
    const { field, heading, replace } = titleEditorIn(main, buttons[1] as HTMLButtonElement);
    saveOnBlur(field, replace);
    field.addEventListener("keydown", (event) => {
      if (event.key === "Enter") field.blur();
    });
    field.focus();
    field.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }));
    await settled();
    expect(field.isConnected).toBe(false);
    expect(heading.isConnected).toBe(true);
    expect(document.activeElement).toBe(document.body);
    expect(heading).not.toHaveFocus();
  });
});

// (ao) So is the reader's click on the page's text while a field holds the focus: the press lands
// on no control, so the engine moves the focus to the page, and the save that blur makes takes
// the field away a microtask later. Nothing moves.
test("a click on the page's text that ends a field's edit leaves the focus on the page", async () => {
  await withRescue(async () => {
    const { buttons, main } = mainOf("Before", "After");
    const { field, heading, replace } = titleEditorIn(main, buttons[1] as HTMLButtonElement);
    saveOnBlur(field, replace);
    const text = document.createElement("p");
    text.textContent = "The issue's description";
    main.append(text);
    field.focus();
    text.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    // The engine's answer to a press on no control, which happy-dom leaves to the page.
    field.blur();
    await settled();
    expect(field.isConnected).toBe(false);
    expect(heading.isConnected).toBe(true);
    expect(document.activeElement).toBe(document.body);
    expect(heading).not.toHaveFocus();
  });
});

// (ap) The same field taken away while it holds the focus is still a loss to answer, however late
// its commit comes: Chromium fires the `focusout` during the removal itself, so the field has gone
// by the rescue's microtask, and Firefox and WebKit fire none. The commit is the reader's Enter's,
// as the issue title's is, so the loss is theirs, and the focus lands on the heading drawn in the
// field's place (a loss the reader did not cause lands on nothing drawn in its place: (bd)).
test("the same field taken away while focused, a microtask after the event, hands the focus to the heading", async () => {
  for (const withFocusout of [true, false]) {
    await withRescue(async () => {
      const { buttons, main } = mainOf("Before", "After");
      const { field, heading, replace } = titleEditorIn(main, buttons[1] as HTMLButtonElement);
      field.focus();
      field.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }));
      queueMicrotask(() => (withFocusout ? lose(field, replace) : replace()));
      await settled();
      expect(heading).toHaveFocus();
    });
  }
});

/** An Inbox drawer - a modal dialog portalled to the end of the body - holding a box of rows, as
 *  the drawer draws its threads, with the trigger of `Hand this question to` in its first row and
 *  that trigger's list opened. */
function drawerRows(): {
  box: HTMLElement;
  list: ListPanel;
  rows: { mark: HTMLInputElement; row: HTMLElement }[];
  trigger: HTMLButtonElement;
} {
  const drawer = document.createElement("div");
  drawer.setAttribute("role", "dialog");
  drawer.setAttribute("aria-modal", "true");
  document.body.append(drawer);
  const { box, rows } = rowBoxIn(drawer, "One", "Two", "Three");
  const { list, trigger } = rowList((rows[0] as { row: HTMLElement }).row);
  return { box, list, rows, trigger };
}

// (bc) INFRA-3135 round 16, the tester's, from the class search of review round 2's second
// finding: Tab pressed on the trigger a pick's write holds is no press the held control takes
// without a write - it moves the focus off the trigger, to the next row's mark. A live update
// that takes that row away while the write is still out is no doing of the reader's: the pick's
// answer is its own row's, not another row's going. So, as in (aw) once the hold has ended, the
// focus goes into no other row and nothing scrolls, by Chromium's `focusout` as by none. At
// 90fa8211 the Tab is passed over (`notePress`, `onHeldControl`), the pick stays the cause, and a
// press's place in a dialog is the whole dialog (`pressPlaces`): on Chromium's path the focus is
// walked into row Three and scrolled to it; with no `focusout` the detached mark reaches no
// dialog, so it lands on the box, and the two engines disagree.
test("Tab off a control a pick's write holds ends the pick as the cause of another row's going", async () => {
  for (const withFocusout of [true, false]) {
    const scrolls = spyOn(Element.prototype, "scrollIntoView");
    try {
      await withRescue(async () => {
        const { box, list, rows, trigger } = drawerRows();
        const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
        await pickAndHold(list, trigger);
        expect(trigger).toHaveFocus();
        // The reader's Tab on the held trigger, and the engine's move of the focus to the next
        // control, the second row's mark.
        trigger.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Tab" }));
        second.mark.focus();
        placeAt(second.mark, 260);
        box.dispatchEvent(new Event("scroll"));
        second.mark.dispatchEvent(new KeyboardEvent("keyup", { bubbles: true, key: "Tab" }));
        await settled();
        expect(trigger).toHaveAttribute("aria-disabled", "true");
        if (withFocusout) lose(second.mark, () => second.row.remove());
        else second.row.remove();
        await settled();
        expect(box).toHaveFocus();
        expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
        expect(rows[2]?.mark).not.toHaveFocus();
      });
      expect(scrolls).not.toHaveBeenCalled();
    } finally {
      scrolls.mockRestore();
    }
  }
});

// (bd) A loss the reader did not cause takes the walk back alone, whatever the page drew in the
// lost control's place. A live update withdraws the question whose answer form holds the focus and
// draws a Reply form where that card stood, its text box first. The focus lands on no control of
// that form - not its text box, where the reader's next key would be typed and a Reply could be
// sent, nor its Send - but on the control before the place, where the walk lands, in no row and
// with no scroll, by Chromium's `focusout` as by none. The page's keys answer there: `j` runs the
// page's binding.
test("an uncaused loss whose place now holds a form leaves the focus out of its text box", async () => {
  for (const withFocusout of [true, false]) {
    const scrolls = spyOn(Element.prototype, "scrollIntoView");
    try {
      await withRescue(async () => {
        const walked: string[] = [];
        const next = { id: "next", keys: "j", label: "Next row", run: () => walked.push("j") };
        const keys = renderHook(() => useKeymap("global", [next]), { wrapper: KeymapProvider });
        try {
          const { buttons, main } = mainOf("Pin");
          const pin = buttons[0] as HTMLButtonElement;
          const asked = document.createElement("article");
          const answer = document.createElement("form");
          const choice = document.createElement("input");
          choice.type = "radio";
          choice.name = "choice";
          answer.append(choice);
          buttonIn(answer, "Send answer");
          asked.append(answer);
          main.append(asked);
          const withdrawn = document.createElement("article");
          const reply = document.createElement("form");
          const box = document.createElement("textarea");
          box.setAttribute("aria-label", "Reply");
          reply.append(box);
          buttonIn(reply, "Send");
          withdrawn.append(reply);
          choice.focus();
          // The update takes the asked card away and draws the withdrawn one where it stood.
          const replace = () => {
            asked.remove();
            main.append(withdrawn);
          };
          if (withFocusout) lose(choice, replace);
          else replace();
          await settled();
          expect(box).not.toHaveFocus();
          expect(reply.contains(document.activeElement)).toBe(false);
          expect(pin).toHaveFocus();
          const landed = document.activeElement as HTMLElement;
          expect(takesText(landed)).toBe(false);
          expect(inRow(landed)).toBe(false);
          landed.dispatchEvent(
            new KeyboardEvent("keydown", { bubbles: true, cancelable: true, key: "j" })
          );
          expect(walked).toEqual(["j"]);
        } finally {
          keys.unmount();
        }
      });
      expect(scrolls).not.toHaveBeenCalled();
    } finally {
      scrolls.mockRestore();
    }
  }
});

// (bf) INFRA-3135 round 17, the tester's: the Agents page draws each agent as an
// `<article data-agent-row>` holding its broadcast mark and its name's toggle, a row of that list
// as much as an Inbox row is of the Inbox. A session that ends takes its row away with no press of
// the reader's, a loss they did not cause, and the rule of (ag) holds for it: the focus goes into
// no other agent's row and nothing scrolls, so the reader's next Space marks no agent they did not
// pick. Measured in Chromium at e4d8b3b1 (builds Job infra-3135-r17h2): with the focus on an
// agent's mark or name when its session ended, the focus landed on the next agent's mark or name,
// and a Space then marked that agent for broadcast; at the merge base bacd5d7e it stayed on the
// body and Space marked nothing. With Chromium's `focusout` and without.
test("an agent's row taken away by its session's end hands the focus to no other agent's row", async () => {
  for (const withFocusout of [true, false]) {
    const scrolls = spyOn(Element.prototype, "scrollIntoView");
    try {
      await withRescue(async () => {
        const { main } = mainOf("Before");
        const agents = ["One", "Two", "Three"].map((label) => {
          const row = document.createElement("article");
          row.setAttribute("data-agent-row", label);
          row.setAttribute(ROW_MARKER_ATTRIBUTE, "");
          row.tabIndex = -1;
          const mark = document.createElement("input");
          mark.type = "checkbox";
          mark.setAttribute("aria-label", `Select Agent ${label} for broadcast`);
          row.append(mark);
          buttonIn(row, `Agent ${label}`);
          main.append(row);
          return { mark, row };
        });
        const second = agents[1] as { mark: HTMLInputElement; row: HTMLElement };
        second.mark.focus();
        placeAt(second.mark, 200);
        if (withFocusout) lose(second.mark, () => second.row.remove());
        else second.row.remove();
        await settled();
        expect(document.activeElement?.closest("[data-agent-row]") ?? null).toBeNull();
        expect(agents[2]?.mark).not.toHaveFocus();
      });
      expect(scrolls).not.toHaveBeenCalled();
    } finally {
      scrolls.mockRestore();
    }
  }
});

// (bi) A keystroke in a field that takes typed text edits the field and is no press of the
// reader's (`isPress`), and nor is the `change` the field fires as it goes, its commit on blur,
// which Chromium fires as an update takes the focused field away. So a live update that takes a
// field away from under the reader's typing, in the task of that `change`, is a loss they did not
// cause (ag): the focus lands in no row and on no field that takes typed text - not on the Reply
// text box the update draws in the row's place - and nothing scrolls. Measured in Chromium with
// the `change` read as the reader's pick: the loss read as caused, and the focus landed on that
// Reply text box, where the reader's next key was typed. A `<textarea>` with a character key and
// then Enter, which inserts a line there, and an `<input>` with a character key, each by
// Chromium's `focusout` and by none. The issue title's Enter, which commits its `<input>`, is
// still a press (ap).
test("a keystroke in a text field, or the change its removal fires, makes no press, so an uncaused removal of the field takes the uncaused path", async () => {
  const fields = [
    {
      keys: [
        { code: "KeyA", key: "a" },
        { code: "Enter", key: "Enter" },
      ],
      make: () => document.createElement("textarea"),
    },
    { keys: [{ code: "KeyA", key: "a" }], make: () => document.createElement("input") },
  ];
  for (const { keys, make } of fields) {
    for (const withFocusout of [true, false]) {
      const scrolls = spyOn(Element.prototype, "scrollIntoView");
      try {
        await withRescue(async () => {
          const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
          const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
          const third = rows[2] as { mark: HTMLInputElement; row: HTMLElement };
          const field = make();
          field.setAttribute("aria-label", "Note");
          second.row.append(field);
          field.focus();
          placeAt(field, 200);
          box.dispatchEvent(new Event("scroll"));
          for (const { code, key } of keys) {
            field.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, code, key }));
            field.dispatchEvent(new KeyboardEvent("keyup", { bubbles: true, code, key }));
          }
          // The update draws the row again in its place, holding a Reply form, its text box first.
          const redrawn = document.createElement("div");
          redrawn.setAttribute("data-inbox-row", "Two");
          redrawn.setAttribute(ROW_MARKER_ATTRIBUTE, "");
          redrawn.tabIndex = -1;
          const reply = document.createElement("form");
          const replyBox = document.createElement("textarea");
          replyBox.setAttribute("aria-label", "Reply");
          reply.append(replyBox);
          buttonIn(reply, "Send");
          redrawn.append(reply);
          const replace = () => {
            second.row.remove();
            box.insertBefore(redrawn, third.row);
          };
          // The field's commit on blur, which Chromium fires as the update takes it away.
          field.dispatchEvent(new Event("change", { bubbles: true }));
          if (withFocusout) lose(field, replace);
          else replace();
          await settled();
          const landed = document.activeElement as HTMLElement;
          expect(box).toHaveFocus();
          expect(inRow(landed)).toBe(false);
          expect(takesText(landed)).toBe(false);
          expect(replyBox).not.toHaveFocus();
        });
        expect(scrolls).not.toHaveBeenCalled();
      } finally {
        scrolls.mockRestore();
      }
    }
  }
});

// (bj) A character typed with a modifier in a field that takes typed text is no press either
// (`isPress`): AltGr, which Windows reports as Ctrl and Alt together with `AltGraph` held, types
// `@` on a German layout, and macOS's Option, Alt held alone, types `@` on many layouts. So the
// update of (bi), after either character in a `<textarea>` or an `<input>`, is a loss the reader
// did not cause: the focus lands on the row box, in no row and on no field that takes typed text,
// and nothing scrolls. Each keydown answers `getModifierState` from the modifiers it holds, as a
// browser does, since happy-dom reads `AltGraph` off `altKey` and would take Option for AltGr.
test("a character typed with AltGr or Option in a text field makes no press, so an uncaused removal of the field takes the uncaused path", async () => {
  const characters = [
    { code: "KeyQ", held: ["AltGraph", "Control", "Alt"] },
    { code: "KeyL", held: ["Alt"] },
  ];
  for (const { code, held } of characters) {
    for (const tag of ["textarea", "input"] as const) {
      for (const withFocusout of [true, false]) {
        const scrolls = spyOn(Element.prototype, "scrollIntoView");
        try {
          await withRescue(async () => {
            const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
            const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
            const third = rows[2] as { mark: HTMLInputElement; row: HTMLElement };
            const field = document.createElement(tag);
            field.setAttribute("aria-label", "Note");
            second.row.append(field);
            field.focus();
            placeAt(field, 200);
            box.dispatchEvent(new Event("scroll"));
            const typed = new KeyboardEvent("keydown", {
              altKey: held.includes("Alt"),
              bubbles: true,
              code,
              ctrlKey: held.includes("Control"),
              key: "@",
            });
            Object.defineProperty(typed, "getModifierState", {
              value: (modifier: string) => held.includes(modifier),
            });
            field.dispatchEvent(typed);
            field.dispatchEvent(new KeyboardEvent("keyup", { bubbles: true, code, key: "@" }));
            // The update of (bi): the row drawn again in its place, holding a Reply form.
            const redrawn = document.createElement("div");
            redrawn.setAttribute("data-inbox-row", "Two");
            redrawn.setAttribute(ROW_MARKER_ATTRIBUTE, "");
            redrawn.tabIndex = -1;
            const reply = document.createElement("form");
            const replyBox = document.createElement("textarea");
            replyBox.setAttribute("aria-label", "Reply");
            reply.append(replyBox);
            buttonIn(reply, "Send");
            redrawn.append(reply);
            const replace = () => {
              second.row.remove();
              box.insertBefore(redrawn, third.row);
            };
            field.dispatchEvent(new Event("change", { bubbles: true }));
            if (withFocusout) lose(field, replace);
            else replace();
            await settled();
            const landed = document.activeElement as HTMLElement;
            expect(box).toHaveFocus();
            expect(inRow(landed)).toBe(false);
            expect(takesText(landed)).toBe(false);
            expect(replyBox).not.toHaveFocus();
          });
          expect(scrolls).not.toHaveBeenCalled();
        } finally {
          scrolls.mockRestore();
        }
      }
    }
  }
});

// (bk) A date box fires its `change` at the reader's pick - in its picker, or with the digit that
// completes the date - and not on blur, as a text field does, so that change is a press of theirs
// (`isPress`). INFRA-3135, round 18 (the tester's): the search bar's Filter panel draws its
// Updated since box only while no date is set (`SearchPalette.tsx`), so the pick takes the
// focused box away. At e9ed2743 the rescue reads that as a loss the reader did not cause, passes
// over the Author box before it and lands on the Status trigger, in Chromium, Firefox and WebKit
// at 1440 and 390 px (builds Job infra-3135-r18xh); at 90fa8211 the focus goes to the Author box,
// the nearest control before the date box, as a loss the reader caused does in (u).
test("a date picked in a date box is the reader's press, so the box's removal walks back to the control before it", async () => {
  for (const typed of [false, true]) {
    for (const withFocusout of [true, false]) {
      await withRescue(async () => {
        mainOf("Search");
        const panel = document.createElement("div");
        panel.setAttribute("role", "dialog");
        panel.setAttribute(PANEL_ATTRIBUTE, "");
        const status = buttonIn(panel, "Status");
        const author = document.createElement("input");
        author.setAttribute("aria-label", "Author");
        panel.append(author);
        const since = document.createElement("input");
        since.type = "date";
        since.setAttribute("aria-label", "Updated since");
        panel.append(since);
        document.body.append(panel);
        since.focus();
        // The digit that completes the date, or no key at all: a pick in the picker presses none.
        if (typed) {
          for (const type of ["keydown", "keyup"]) {
            since.dispatchEvent(
              new KeyboardEvent(type, { bubbles: true, code: "Digit9", key: "9" })
            );
          }
        }
        since.dispatchEvent(new Event("input", { bubbles: true }));
        since.dispatchEvent(new Event("change", { bubbles: true }));
        if (withFocusout) lose(since);
        else since.remove();
        await settled();
        expect(author).toHaveFocus();
        expect(status).not.toHaveFocus();
      });
    }
  }
});

// (bo) The page drops the date box at its `input`, which React's onChange answers, before any
// `change` reaches it. INFRA-3135, round 19 (the tester's, builds Job infra-3135-r19dt at
// 89072a56, a trace of the events in each engine): Firefox fires `input` alone as the digit that
// completes the date is typed, and Playwright's fill() - a value set without keys, as assistive
// technology sets it - fires `input`, then `change` on the box the page has already taken away,
// which no listener of the page hears. The date is the reader's all the same, so the box's
// removal walks back to the control before it, as in (bk); at 89072a56 the rescue reads it as a
// loss the reader did not cause and lands on Status.
test("a date box dropped at its input, before any change, is the reader's press all the same", async () => {
  for (const typed of [false, true]) {
    for (const withFocusout of [true, false]) {
      await withRescue(async () => {
        mainOf("Search");
        const panel = document.createElement("div");
        panel.setAttribute("role", "dialog");
        panel.setAttribute(PANEL_ATTRIBUTE, "");
        const status = buttonIn(panel, "Status");
        const author = document.createElement("input");
        author.setAttribute("aria-label", "Author");
        panel.append(author);
        const since = document.createElement("input");
        since.type = "date";
        since.setAttribute("aria-label", "Updated since");
        panel.append(since);
        document.body.append(panel);
        since.focus();
        // The digit that completes the date as Firefox reports it: its keydown, then `input`.
        if (typed) {
          since.dispatchEvent(
            new KeyboardEvent("keydown", { bubbles: true, code: "Digit9", key: "9" })
          );
        }
        since.dispatchEvent(new Event("input", { bubbles: true }));
        if (withFocusout) lose(since);
        else since.remove();
        // The `change` that follows reaches a box no longer in the page.
        since.dispatchEvent(new Event("change", { bubbles: true }));
        await settled();
        expect(author).toHaveFocus();
        expect(status).not.toHaveFocus();
      });
    }
  }
});

/** A control of `kind`: a `<select>`, a `<textarea>`, a content-editable `<div>`, else an
 *  `<input>` of that type. */
function controlOf(kind: string): HTMLElement {
  if (kind === "select" || kind === "textarea") return document.createElement(kind);
  if (kind === "content-editable") {
    const editable = document.createElement("div");
    editable.contentEditable = "true";
    return editable;
  }
  const input = document.createElement("input");
  input.type = kind;
  return input;
}

/** Puts `control` last in the Filter panel of (bk), where the date box stands, after its Status
 *  trigger and its Author box, beside the page's main content, and gives it the focus. */
function focusInFilterPanel(control: HTMLElement): void {
  mainOf("Search");
  const panel = document.createElement("div");
  panel.setAttribute("role", "dialog");
  panel.setAttribute(PANEL_ATTRIBUTE, "");
  buttonIn(panel, "Status");
  const author = document.createElement("input");
  author.setAttribute("aria-label", "Author");
  panel.append(author);
  control.setAttribute("aria-label", "Value");
  panel.append(control);
  document.body.append(panel);
  control.focus();
}

/** The name of the element holding the focus: its `aria-label`, else its text. */
function focusedName(): string | null {
  const focused = document.activeElement;
  return focused?.getAttribute("aria-label") ?? focused?.textContent ?? null;
}

// (bl) Every control that fires its `change` at the reader's pick, as the date box of (bk) does,
// makes that change a press of theirs (`isPress`): a `<select>`, a checkbox, a radio, a file,
// range or color input, and each date and time box, which takes typed text all the same. So in
// the Filter panel of (bk), the control's removal in the task of its `change` is a loss the reader
// caused: the focus walks back to the Author box before it, as in (bk), and does not pass over it
// to the Status trigger.
test("a change on a control that fires it at the reader's pick is their press, so the control's removal walks back to the control before it", async () => {
  const kinds = [
    "select",
    "checkbox",
    "radio",
    "file",
    "range",
    "color",
    "date",
    "time",
    "datetime-local",
    "month",
    "week",
  ];
  // Where the focus went for each kind, with Chromium's `focusout` and with none.
  const landed: Record<string, string | null> = {};
  const walkedBack: Record<string, string> = {};
  for (const kind of kinds) {
    for (const withFocusout of [true, false]) {
      const named = `${kind}, ${withFocusout ? "with" : "without"} a focusout`;
      await withRescue(async () => {
        const control = controlOf(kind);
        focusInFilterPanel(control);
        control.dispatchEvent(new Event("input", { bubbles: true }));
        control.dispatchEvent(new Event("change", { bubbles: true }));
        if (withFocusout) lose(control);
        else control.remove();
        await settled();
        landed[named] = focusedName();
      });
      walkedBack[named] = "Author";
    }
  }
  expect(landed).toEqual(walkedBack);
});

// (bm) A `change` on a field whose value is typed text alone - an `<input>` of type text, search,
// email, url, tel, password or number, a `<textarea>`, a content-editable element - is the
// field's commit on blur, which Chromium fires as an update takes the focused field away, and no
// press of the reader's (`isPress`), as in (bi). So in the Filter panel of (bk), the field's
// removal in the task of that `change` is a loss the reader did not cause: the focus passes over
// the Author box, which takes typed text, and lands on the Status trigger, in no row, and nothing
// scrolls.
test("a change on a field whose value is typed text is no press, so the field's removal takes the uncaused path", async () => {
  const kinds = [
    "text",
    "search",
    "email",
    "url",
    "tel",
    "password",
    "number",
    "textarea",
    "content-editable",
  ];
  // Where the loss landed for each kind, with Chromium's `focusout` and with none: the element
  // holding the focus, whether it is in a row or takes typed text, and whether anything scrolled.
  const landed: Record<string, object> = {};
  const quiet: Record<string, object> = {};
  for (const kind of kinds) {
    for (const withFocusout of [true, false]) {
      const named = `${kind}, ${withFocusout ? "with" : "without"} a focusout`;
      const scrolls = spyOn(Element.prototype, "scrollIntoView");
      try {
        await withRescue(async () => {
          const field = controlOf(kind);
          focusInFilterPanel(field);
          field.dispatchEvent(new Event("input", { bubbles: true }));
          // The field's commit on blur, which Chromium fires as the update takes it away.
          field.dispatchEvent(new Event("change", { bubbles: true }));
          if (withFocusout) lose(field);
          else field.remove();
          await settled();
          const focused = document.activeElement as HTMLElement;
          landed[named] = {
            focus: focusedName(),
            inRow: inRow(focused),
            scrolled: scrolls.mock.calls.length > 0,
            takesText: takesText(focused),
          };
        });
      } finally {
        scrolls.mockRestore();
      }
      quiet[named] = { focus: "Status", inRow: false, scrolled: false, takesText: false };
    }
  }
  expect(landed).toEqual(quiet);
});
