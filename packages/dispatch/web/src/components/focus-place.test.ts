import { expect, jest, spyOn, test } from "bun:test";
import { renderHook, waitFor } from "@testing-library/react";

import { SEND_DEADLINE_MS } from "../features/writes/keyed-write";
import { CONTROL } from "./control-names";
import { useFocusRescue } from "./focus-place";

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
// leaving it: nothing moves, so a heading that opens the editor when it takes the focus is not
// handed the focus and the editor stays closed.
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

/** A button labelled `label`, appended to `parent`. */
function buttonIn(parent: Element, label: string): HTMLButtonElement {
  const button = document.createElement("button");
  button.type = "button";
  button.textContent = label;
  parent.append(button);
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

// (u) So does the Inbox drawer, a modal dialog of rows: its last two rows answered together, the
// focus stays in the drawer, on its first control (the real drawer's is its Close button).
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
    expect(rows[0]).toHaveFocus();
    expect(pageSearch).not.toHaveFocus();
  });
});

// (v) The main content with no control left at the place or before it hands the focus to its
// search box, else its filter trigger, else its own first control, else to itself - and never to
// a control outside it, the sidebar's here.
test("the main content with nothing left at the place goes to its search box, its trigger, its first control, then itself", async () => {
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
      expect(search).toHaveFocus();
      search.remove();
      await loseThree();
      expect(trigger).toHaveFocus();
      trigger.remove();
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
