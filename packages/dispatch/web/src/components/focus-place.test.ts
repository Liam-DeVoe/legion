import { expect, jest, spyOn, test } from "bun:test";
import { onlineManager } from "@tanstack/react-query";
// biome-ignore lint/style/noRestrictedImports: an armed press is live only until a frame and a task after it, and user-event waits a task after every press it makes, so the click the armed cases make and the commit after it must come in the same task, which user-event has no form for.
import { act, fireEvent, render, renderHook, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement, type ReactNode, useRef } from "react";

import { realClock } from "../__tests__/fake-clock";
import { NETWORK_ERROR_MESSAGE, REQUEST_DEADLINE_MS } from "../api/client";
import { useReadBound } from "../api/read-bound";
import { signedIn } from "../features/conversation/held-sends-fixture";
import { KeymapProvider } from "../features/shell/KeymapProvider";
import { PANEL_ATTRIBUTE, useKeymap } from "../features/shell/keymap";
import { SEND_DEADLINE_MS } from "../features/writes/keyed-write";
import { plainWrite, type Writes, writesOf } from "../features/writes/write-store";
import { CONTROL } from "./control-names";
import {
  armOnPress,
  beginAnswerHold,
  causedByReader,
  FRAME_WAIT_MS,
  SHOWN_ITEM_ATTRIBUTE,
  useFocusPlace,
  useFocusRescue,
} from "./focus-place";
import { ROW_MARKER_ATTRIBUTE } from "./row-marker";

// The page's focus rescue, read in happy-dom with the page built by hand. A focused element taken
// away fires a `focusout` in Chromium while it is still in the page, and none at all in Firefox
// (measured, INFRA-3135 round 7) nor in happy-dom: `lose` fires it as Chromium does before the
// element goes, and a bare `remove()` is the Firefox case, which the rescue sees by watching the
// page. A `blur()` fires its own.
// The rescue is handed no writes unless a case hands it the page's (`withRescue`'s `writes`), so a
// press there starts no write, and it stays live while a control it reads as is held by a hold
// that began after it, or while a hold on what answers it is on (`beginAnswerHold`): a case that
// holds a control by `aria-disabled` while its write is out reads the first rule. The cases that
// hand the rescue writes read a press's action (`signedInWrites`).
// A component's own hand-off (`useFocusPlace`) is read here too, in a line of buttons React draws
// (`HandOffLine`).

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

/** Runs `body` with the rescue mounted, handed `writes` as the page's writes where a case gives
 *  them, and takes the page and the rescue away after it. */
async function withRescue(body: () => Promise<void>, writes?: Writes): Promise<void> {
  const rescue = renderHook(() => useFocusRescue(mainRef, writes));
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

/** Runs `body` with every `scrollIntoView` watched, and expects none of them to have run. */
async function scrollingNothing(body: () => Promise<void>): Promise<void> {
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    await body();
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
}

/** A modal dialog at the end of the body, as `useDialog`'s dialogs are portalled, holding one
 *  button per label. */
function modalOf(...labels: string[]): { buttons: HTMLButtonElement[]; dialog: HTMLElement } {
  const dialog = document.createElement("div");
  dialog.setAttribute("role", "dialog");
  dialog.setAttribute("aria-modal", "true");
  document.body.append(dialog);
  return { buttons: labels.map((label) => buttonIn(dialog, label)), dialog };
}

// (a) A control an update takes away, with no press of the reader's, leaves the focus on the page,
// as it did before the page had a rescue: the control now at its place is the page's choice, not
// the reader's, and nothing scrolls.
test("a control taken away with no press leaves the focus on the page", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      const [first, second, third] = mainOf("First", "Second", "Third").buttons;
      second?.focus();
      lose(second as HTMLElement);
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(third).not.toHaveFocus();
      expect(first).not.toHaveFocus();
    });
  });
});

// (b) So for the last control taken away with no press: the one before it is no more the reader's
// pick than one after it would be, and the focus stays on the page.
test("the last control taken away with no press leaves the focus on the page", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      const [, second, third] = mainOf("First", "Second", "Third").buttons;
      third?.focus();
      lose(third as HTMLElement);
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(second).not.toHaveFocus();
    });
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

// (bq) A control an update disabled, with no press of the reader's, gets the focus back when it is
// enabled again within the wait (c) - a control that comes back, which a loss the reader did not
// cause gives the focus back to outside a modal (`leaveUncaused`) - but only while the focus is
// still on the page. A reader who has moved the focus on before the control comes back - here their
// Tab from the page to the control before it - keeps it where they put it.
test("a control an update disabled gets no focus back once the reader has moved the focus on", async () => {
  await withRescue(async () => {
    const [before, saving] = mainOf("Before", "Save", "After").buttons;
    const button = saving as HTMLButtonElement;
    button.focus();
    button.blur();
    button.disabled = true;
    await settled();
    expect(document.activeElement).toBe(document.body);
    document.body.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Tab" }));
    before?.focus();
    button.disabled = false;
    await settled();
    await settled();
    expect(before).toHaveFocus();
    expect(button).not.toHaveFocus();
  });
});

// (br) The rescue's wait keeps no deadline of its own: it lasts as long as the control's hold, which
// a read held after the write can stretch past the write deadline. So a control disabled under the
// focus for twice that deadline gets the focus back once it is enabled again, as one enabled within
// it does (l). That holds after a loss the reader caused, by their Enter on the control, and after
// one an update made.
test("a control disabled under the focus for longer than the write deadline gets the focus back when it is enabled again", async () => {
  jest.useFakeTimers();
  try {
    // Where the focus went after each loss, and where it belongs.
    const landed: Record<string, string | null> = {};
    const belongs: Record<string, string> = {};
    for (const caused of [true, false]) {
      const named = caused ? "after the reader's Enter" : "after an update";
      await withRescue(async () => {
        const button = mainOf("Before", "Save", "After").buttons[1] as HTMLButtonElement;
        button.focus();
        if (caused) enterPress(button);
        button.blur();
        button.disabled = true;
        // The check, a frame and a task after the loss, begins the wait.
        await frame();
        jest.advanceTimersByTime(0);
        // Twice the write deadline passes with the control still disabled.
        jest.advanceTimersByTime(2 * SEND_DEADLINE_MS);
        await frame();
        jest.advanceTimersByTime(0);
        expect(document.activeElement).toBe(document.body);
        button.disabled = false;
        await frame();
        await frame();
        jest.advanceTimersByTime(0);
        landed[named] = document.activeElement === document.body ? "the page" : focusedName();
      });
      belongs[named] = "Save";
    }
    expect(landed).toEqual(belongs);
  } finally {
    jest.useRealTimers();
  }
});

/** The main content holding Before, an answer box - a `<textarea>` named Answer - and After. */
function answerBox(): HTMLTextAreaElement {
  const { main } = mainOf("Before");
  const answer = document.createElement("textarea");
  answer.setAttribute("aria-label", "Answer");
  main.append(answer);
  buttonIn(main, "After");
  return answer;
}

// (bu) The control an update disabled, with no press of the reader's, gets the focus back only
// while no press of theirs has come since the loss. A key the reader presses on the page is one,
// and ends the rescue's wait as a pointer press does (p): after `j`, `k` and `j` on the page, the
// answer box enabled again within the wait takes nothing back. The focus stays on the page, the
// reader's next key is typed into nothing, and nothing scrolls.
test("a control an update disabled gets no focus back once the reader has pressed keys on the page", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      const answer = answerBox();
      answer.focus();
      // Chromium fires the `focusout` just after the disable; happy-dom blurs no disabled control,
      // so here it comes just before it.
      answer.blur();
      answer.disabled = true;
      await settled();
      expect(document.activeElement).toBe(document.body);
      for (const key of ["j", "k", "j"]) keyOnPage(key);
      answer.disabled = false;
      await settled();
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(answer).not.toHaveFocus();
      await userEvent.setup().keyboard("x");
      expect(answer.value).toBe("");
    });
  });
});

// (bv) A control an update hides, with no press of the reader's, and shows again before the
// rescue's check is the control that held the focus coming back while the focus is still on the
// page: it gets the focus back, focused with no scroll.
test("a control an update hides and shows again before the rescue's check gets the focus back, scrolling nothing", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      const answer = answerBox();
      answer.focus();
      const focus = spyOn(answer, "focus");
      // The update hides the box under the focus, and its `focusout` comes once it is hidden.
      answer.hidden = true;
      answer.blur();
      answer.hidden = false;
      await settled();
      expect(answer).toHaveFocus();
      expect(focus).toHaveBeenLastCalledWith({ preventScroll: true });
    });
  });
});

// (bw) So for (bv) once the reader has pressed a key between: `j` on the page while the box is
// hidden is a press of theirs, which ends the rescue's check, and the box shown again takes
// nothing back. The focus stays on the page, and nothing scrolls.
test("a control an update hides and shows again gets no focus back once the reader has pressed a key between", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      const answer = answerBox();
      answer.focus();
      answer.hidden = true;
      answer.blur();
      keyOnPage("j");
      answer.hidden = false;
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(answer).not.toHaveFocus();
    });
  });
});

// (d) A modal dialog an update closes, with no press of the reader's, over an opener held disabled
// leaves the focus on the page: neither the opener's place in the main content under the dialog
// nor any control there is the reader's pick, and no other modal is open to take it.
test("a control in a dialog that closes with no press leaves the focus on the page", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      const [, opener, after] = mainOf("Before", "Decline", "After").buttons;
      const { buttons, dialog } = modalOf("Confirm");
      const confirm = buttons[0] as HTMLButtonElement;
      opener?.focus();
      confirm.focus();
      // The update disables the opener and closes the dialog, whose return of the focus to that
      // opener takes nothing.
      (opener as HTMLButtonElement).disabled = true;
      lose(confirm, () => dialog.remove());
      opener?.focus();
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(after).not.toHaveFocus();
    });
  });
});

// (m) A panel that is no modal - a menu's, drawn outside the main content - taken away with no
// press of the reader's inside it leaves the focus on the page: a loss the reader did not cause
// goes back to no trigger, and only an open modal dialog would take it.
test("a panel that is no modal, closed with no press inside it, leaves the focus on the page", async () => {
  await scrollingNothing(async () => {
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
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(trigger).not.toHaveFocus();
    });
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

/** The reader's Enter on `element`: its `keydown`, by which the rescue notes the press. */
function enterPress(element: Element): void {
  element.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }));
}

/** The reader's `key` pressed on the page itself, the focus on the body: its `keydown`, by which
 *  the rescue notes a press, and its `keyup`. */
function keyOnPage(key: string): void {
  for (const type of ["keydown", "keyup"]) {
    document.body.dispatchEvent(new KeyboardEvent(type, { bubbles: true, key }));
  }
}

/** Each way a case presses a control, by its name: Enter, and a pointer press. */
const PRESSES: Readonly<Record<string, (element: Element) => void>> = {
  Enter: enterPress,
  pointer: pointerPress,
};

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
// check goes on without its frame rather than leaving the reader on the page that long. A control
// an update takes from a modal dialog, with no press of the reader's, is answered at the dialog's
// own stop, with no scroll.
test("a loss is answered even while the page's animation frames are held back", async () => {
  const frames = spyOn(window, "requestAnimationFrame").mockImplementation(() => 0);
  try {
    await scrollingNothing(async () => {
      await withRescue(async () => {
        mainOf("Before");
        const { buttons, dialog } = modalOf("First", "Second", "Third");
        const [, second, third] = buttons;
        second?.focus();
        lose(second as HTMLElement);
        await waitFor(() => expect(dialog).toHaveFocus());
        expect(third).not.toHaveFocus();
      });
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

// (ch) A press of the reader's ends the rescue's wait whoever caused the loss, as it does after a
// loss an update made (p, bu): once the reader acts, the focus is theirs. Their Enter on Save
// disables it under the press. Then a key on the page, or a pointer press there, comes before Save
// is enabled again, and Save takes nothing back: the focus stays on the page.
test("a key or a pointer press on the page ends the wait of a control the reader's own press disabled", async () => {
  const moves: Readonly<Record<string, () => void>> = {
    "j on the page": () => keyOnPage("j"),
    "a pointer press on the page": () =>
      document.body.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })),
  };
  // Where the focus went after each press, and where it belongs.
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  for (const [name, move] of Object.entries(moves)) {
    await withRescue(async () => {
      const save = mainOf("Before", "Save", "After").buttons[1] as HTMLButtonElement;
      save.focus();
      enterPress(save);
      save.blur();
      save.disabled = true;
      await settled();
      move();
      save.disabled = false;
      await settled();
      await settled();
      landed[name] = document.activeElement === document.body ? "the page" : focusedName();
    });
    belongs[name] = "the page";
  }
  expect(landed).toEqual(belongs);
});

// (ci) Once the reader acts, their key's own action decides where the focus goes, and the rescue
// hands nothing on after it. The reader's Enter on Reply, in a reading column that shows thread
// one, disables Reply under the press. Then `j`, which the page binds to the next thread, draws
// thread two in the column, and the disabled Reply goes with thread one. `j` moves no focus, so the
// focus stays on the page. No walk-back runs once Reply has gone, onto thread two's "Release bot"
// link or onto the control before the column, and nothing scrolls. In WebKit the wait ran on
// through the key and walked back onto that link.
test("a key the page acts on, pressed while the rescue waits on a control the reader's press disabled, leaves the focus where the key's action put it", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      const { main } = mainOf("Threads");
      const column = document.createElement("section");
      column.setAttribute(SHOWN_ITEM_ATTRIBUTE, "one");
      const reply = buttonIn(column, "Reply");
      main.append(column);
      const nextThread = () => {
        const link = document.createElement("a");
        link.href = "/agents/release-bot";
        link.textContent = "Release bot";
        column.setAttribute(SHOWN_ITEM_ATTRIBUTE, "two");
        column.replaceChildren(link);
        buttonIn(column, "Reply");
      };
      const next = { id: "next", keys: "j", label: "Next thread", run: nextThread };
      const keys = renderHook(() => useKeymap("global", [next]), { wrapper: KeymapProvider });
      try {
        reply.focus();
        enterPress(reply);
        reply.blur();
        reply.disabled = true;
        await settled();
        expect(document.activeElement).toBe(document.body);
        keyOnPage("j");
        expect(reply.isConnected).toBe(false);
        await settled();
        await settled();
        expect(document.activeElement === document.body ? "the page" : focusedName()).toBe(
          "the page"
        );
      } finally {
        keys.unmount();
      }
    });
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
// go by watching the page. With no press of the reader's, the focus stays on the page there too.
test("a control taken away with no focusout, as Firefox does, and no press leaves the focus on the page", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      const [, second, third] = mainOf("First", "Second", "Third").buttons;
      second?.focus();
      second?.remove();
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(third).not.toHaveFocus();
    });
  });
});

// (j) So for a control whose parent is taken away with no press: neither the control after the
// parent nor the one before it takes the focus.
test("a control whose parent is taken away with no focusout and no press leaves the focus on the page", async () => {
  await scrollingNothing(async () => {
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
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(last).not.toHaveFocus();
      expect(first).not.toHaveFocus();
    });
  });
});

// (e) A focus the reader put on the page stays there, even out of a modal dialog; the same
// dialog's control taken away by an update, with no press of the reader's, is answered at the
// dialog's own stop.
test("a deliberate blur onto the page moves nothing", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      mainOf("Before");
      const { buttons, dialog } = modalOf("First", "Second");
      const [first, second] = buttons;
      first?.focus();
      first?.blur();
      await settled();
      expect(document.activeElement).toBe(document.body);

      second?.focus();
      lose(second as HTMLElement);
      await settled();
      expect(dialog).toHaveFocus();
      expect(first).not.toHaveFocus();
    });
  });
});

// (f) A loss on a page the reader has since left moves nothing: the new page owns its focus. The
// same loss on the page it happened on, an update's in a modal dialog with no press of the
// reader's, is answered at the dialog's own stop.
test("a loss followed by a change of page moves nothing", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      mainOf("Before");
      const { buttons, dialog } = modalOf("First", "Second", "Third");
      const [first, second, third] = buttons;
      second?.focus();
      lose(second as HTMLElement);
      window.history.pushState(null, "", "/elsewhere");
      await settled();
      expect(document.activeElement).toBe(document.body);

      first?.focus();
      lose(first as HTMLElement);
      await settled();
      expect(dialog).toHaveFocus();
      expect(third).not.toHaveFocus();
    });
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
// hand-off the same loss, an update's in a modal dialog with no press of the reader's, is
// answered at the dialog's own stop.
test("a component that already moved the focus is left alone", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      mainOf("Before");
      const { buttons, dialog } = modalOf("First", "Second", "Third");
      const [first, second, third] = buttons;
      first?.focus();
      lose(first as HTMLElement);
      requestAnimationFrame(() => {
        if (document.activeElement === document.body) third?.focus();
      });
      await settled();
      expect(third).toHaveFocus();
      expect(second).not.toHaveFocus();

      lose(third as HTMLElement);
      await settled();
      expect(dialog).toHaveFocus();
      expect(second).not.toHaveFocus();
    });
  });
});

// (s) A loss inside a modal dialog that stays open is answered inside that dialog. The reader is
// in a modal the page behind it cannot be reached from, so a place the dialog no longer draws -
// two of its controls taken away together, as the search bar's Retry takes its own failure block
// away, here with no press of the reader's - leaves the focus on the dialog's own stop, and never
// on the page's search box behind it, whose next keystroke would be typed into it. Measured in
// Firefox, INFRA-3135 round 7: the search bar's Retry put the focus outside the open palette in 2
// of 12 runs.
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
// controls an update takes away, with no press of the reader's, leaves the focus on the page:
// neither its own first control nor the page's search box takes it, since only an open modal
// dialog keeps a loss the reader did not cause.
test("a panel that is no modal, losing its last controls with no press, leaves the focus on the page", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      const pageSearch = pageSearchIn(mainOf("Before").main);
      const panel = document.createElement("div");
      panel.setAttribute("role", "dialog");
      document.body.append(panel);
      const [first, second, third] = ["Todo", "Done", "Clear"].map((label) =>
        buttonIn(panel, label)
      );
      third?.focus();
      lose(third as HTMLElement, () => {
        third?.remove();
        second?.remove();
      });
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(first).not.toHaveFocus();
      expect(panel).not.toHaveFocus();
      expect(pageSearch).not.toHaveFocus();
    });
  });
});

// (u) The Inbox drawer, a modal dialog of rows, keeps the focus: its last two rows taken together
// by an update, with no press of the reader's, the drawer takes the focus itself, as its own stop
// - not the first row's control, the nearest one before them, nor the page's search box behind
// it - since a focus that leaves an open modal reaches the page behind it.
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

// (v) The main content losing a block of controls at its end, with no press of the reader's,
// leaves the focus on the page however many went together: neither the nearest control before
// the block - its filter trigger here - nor its search box nor its first control takes it, nor a
// control outside it, the sidebar's here, and nothing scrolls. Once the main content draws no
// other control, the last one taken away leaves the focus on the page too, not on the main
// content itself.
test("the main content losing a block at its end with no press leaves the focus on the page", async () => {
  const aside = document.createElement("aside");
  const sidebar = buttonIn(aside, "Sidebar");
  document.body.prepend(aside);
  try {
    await scrollingNothing(async () => {
      await withRescue(async () => {
        const [first] = mainOf("First").buttons;
        const main = mainRef.current as HTMLElement;
        const search = pageSearchIn(main);
        const trigger = buttonIn(main, "Filters");
        trigger.setAttribute("data-control", CONTROL.panelTrigger);
        const three = ["One", "Two", "Three"].map((label) => buttonIn(main, label));
        three[2]?.focus();
        lose(three[2] as HTMLElement, () => {
          for (const each of three) each.remove();
        });
        await settled();
        expect(document.activeElement).toBe(document.body);
        for (const control of [trigger, search, first, sidebar]) {
          expect(control).not.toHaveFocus();
        }
        for (const each of [trigger, search, first]) each?.remove();
        const last = buttonIn(main, "Last");
        last.focus();
        lose(last);
        await settled();
        expect(document.activeElement).toBe(document.body);
        expect(main).not.toHaveFocus();
        expect(sidebar).not.toHaveFocus();
      });
    });
  } finally {
    aside.remove();
  }
});

// (be) The edge of (v): a loss the reader did not cause, where every control before the lost
// place takes typed text - the page's search box and a note field - leaves the focus on the page,
// as every such loss does: not on either field, nor on the main content itself, and nothing
// scrolls, so the page's single keys answer from the page. With Chromium's `focusout` and without.
test("an uncaused loss whose every earlier control takes text leaves the focus on the page", async () => {
  for (const withFocusout of [true, false]) {
    await scrollingNothing(async () => {
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
        expect(document.activeElement).toBe(document.body);
        expect(main).not.toHaveFocus();
        expect(search).not.toHaveFocus();
        expect(note).not.toHaveFocus();
      });
    });
  }
});

// (w) A control outside the main content - the sidebar's - whose place an update takes away, with
// no press of the reader's, leaves the focus on the page: neither the sidebar's control before it
// nor one in the main content takes it.
test("a control outside the main content whose place has gone with no press leaves the focus on the page", async () => {
  const aside = document.createElement("aside");
  const [, middle, last] = ["Inbox", "Projects", "Agents"].map((label) => buttonIn(aside, label));
  document.body.prepend(aside);
  try {
    await scrollingNothing(async () => {
      await withRescue(async () => {
        const [inMain] = mainOf("In the main content").buttons;
        last?.focus();
        lose(last as HTMLElement);
        await settled();
        expect(document.activeElement).toBe(document.body);
        expect(middle).not.toHaveFocus();
        expect(inMain).not.toHaveFocus();
      });
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
    const { buttons, dialog } = modalOf("Retry");
    const only = buttons[0] as HTMLButtonElement;
    only.focus();
    lose(only);
    await settled();
    expect(dialog).toHaveFocus();
    expect(dialog).toHaveAttribute("tabindex", "-1");
    expect(pageSearch).not.toHaveFocus();
  });
});

// (bt) A modal nested in another - the Decline to answer dialog drawn inside the Inbox drawer, each
// modal - is the one a loss inside it is answered in: a control an update takes from the inner
// modal, with no press of the reader's, while that modal stays open, leaves the focus on the inner
// modal's own stop, not on the outer one's, and nothing scrolls. With Chromium's `focusout` and
// without.
test("an uncaused loss in a modal nested in another lands on the inner modal's own stop", async () => {
  for (const withFocusout of [true, false]) {
    await scrollingNothing(async () => {
      await withRescue(async () => {
        mainOf("Before");
        const drawer = modalOf("Close");
        const decline = modalOf("Decline", "Cancel");
        drawer.dialog.append(decline.dialog);
        const confirm = decline.buttons[0] as HTMLButtonElement;
        confirm.focus();
        if (withFocusout) lose(confirm);
        else confirm.remove();
        await settled();
        expect(decline.dialog).toHaveFocus();
        expect(drawer.dialog).not.toHaveFocus();
      });
    });
  }
});

// (y) A dialog hidden while it stays mounted - the phone thread kept for a reply - has gone as
// much as one taken away. Hidden by an update, with no press of the reader's, it leaves the focus
// on the page: not on the control that opened it, nor on the hidden dialog itself, which is no
// modal open any more.
test("a dialog hidden with no press while it stays mounted leaves the focus on the page", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      const [, opener] = mainOf("Before", "Open thread", "After").buttons;
      const { buttons, dialog } = modalOf("Reply");
      const reply = buttons[0] as HTMLButtonElement;
      opener?.focus();
      reply.focus();
      // The update hides the dialog under the focus, and its `focusout` comes once it is hidden.
      dialog.hidden = true;
      reply.blur();
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(opener).not.toHaveFocus();
      expect(dialog).not.toHaveFocus();
    });
  });
});

// (z) The same loss lands on the same control in every engine. Two controls at the end of a
// region go in the commit of the reader's Enter on the last, which holds the focus. Chromium fires
// a `focusout` while both are still counted, Firefox and WebKit fire none and the rescue reads the
// removal; the reader pressed the same thing either way. Measured by hand on dispatch:local at
// c3373441, INFRA-3135 round 8: with the project List's last two pager controls taken away,
// Firefox and WebKit land on "‹ Previous", the control before them, and Chromium on the page's
// search box; in the Inbox drawer, Firefox and WebKit on "Decline to answer" and Chromium on Close
// at the drawer's top.
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
      fifth?.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" }));
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
  await scrollingNothing(async () => {
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
  });
});

// (ab) A loss the reader caused and can still see is answered as before: an element its box
// scrolled but kept in sight, and one scrolled out of sight and back into it before it went, each
// taken away by the reader's Enter on it, hand the focus to the control now at their place. Only
// the scroll that leaves nothing of it in sight counts.
test("a loss of an element still in sight is answered, however it was scrolled", async () => {
  await withRescue(async () => {
    const { box, buttons } = scrollBoxIn(mainOf("Before").main, "One", "Two", "Three");
    const [, second, third] = buttons;
    second?.focus();
    // Half of it above the box's top edge: the reader still sees the other half.
    placeAt(second as HTMLElement, 80);
    box.dispatchEvent(new Event("scroll"));
    enterPress(second as HTMLElement);
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
    enterPress(second as HTMLElement);
    second?.remove();
    await waitFor(() => expect(third).toHaveFocus());
  });
});

// (ac) In a dialog the same loss still keeps the focus inside it, so the page behind a modal one
// is never reached, and still scrolls nothing and focuses no row the reader did not pick: the
// Inbox drawer, whose list the reader scrolled away from the focused row before a live update
// took that row away, takes the focus itself.
test("a loss scrolled out of sight in a modal dialog leaves the focus on the dialog itself, scrolling nothing", async () => {
  await scrollingNothing(async () => {
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
  });
});

// (ad) The wait is held to the same rule: a control the reader's press disabled, and that they
// scrolled out of their sight while its write was out, does not get the focus back once it is
// enabled again, which would pull the page back to it. Left in sight it gets it back (c).
test("a control scrolled out of sight while its write is out is left alone when it is enabled again", async () => {
  await scrollingNothing(async () => {
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
  });
});

// (ae) A resize can put the focused element out of the reader's sight with nothing scrolled: a
// shorter window, or a zoom, shrinks the box a table is sized to under its focused row, and the
// virtual list may then stop drawing that row. The resize reads the sight again, against every
// box around the element that clips what overflows it - this box has never scrolled - so the
// row's removal after it moves nothing and scrolls nothing. A resize that leaves the row in its
// box changes nothing: the removal the reader's Enter on it makes is answered as before.
// INFRA-3135's review round 1, R10.
test("a resize that puts the focused row out of its box, then the row's removal, moves nothing", async () => {
  await scrollingNothing(async () => {
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
  });
  await withRescue(async () => {
    const { box, buttons } = scrollBoxIn(mainOf("Before").main, "One", "Two", "Three");
    box.style.overflowY = "auto";
    const [, second, third] = buttons;
    second?.focus();
    placeAt(second as HTMLElement, 300);
    // The row after it stands under it, inside the box, where the walk back finds it in sight.
    placeAt(third as HTMLElement, 340);
    window.dispatchEvent(new Event("resize"));
    enterPress(second as HTMLElement);
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
  await scrollingNothing(async () => {
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
  });
});

/** A box that scrolls by itself, laid out from 100 px to 400 px down the window, holding one row
 *  per label - an Inbox row, a `div` carrying the row marker where the Inbox draws an `<li>`, a
 *  row by the same rule (`ROW_SELECTOR`), and the one checkbox a row marks itself by, laid out in
 *  the box one under the other from its top - appended to `parent`. */
function rowBoxIn(
  parent: Element,
  ...labels: string[]
): { box: HTMLElement; rows: { mark: HTMLInputElement; row: HTMLElement }[] } {
  const box = document.createElement("div");
  box.style.overflowY = "auto";
  parent.append(box);
  placeAt(box, 100, 300);
  const rows = labels.map((label, index) => {
    const row = document.createElement("div");
    row.setAttribute("data-inbox-row", label);
    row.setAttribute(ROW_MARKER_ATTRIBUTE, "");
    row.tabIndex = -1;
    const mark = document.createElement("input");
    mark.type = "checkbox";
    mark.setAttribute("aria-label", `Select ${label}`);
    row.append(mark);
    placeAt(mark, 100 + index * 40);
    box.append(row);
    return { mark, row };
  });
  return { box, rows };
}

// (ag) A row a live update takes away while the reader can see it, with no press of theirs before
// it, is a loss they did not cause: the focus stays on the page, as it did before the page had a
// rescue - in no other row, not on the box the rows are drawn in - and nothing scrolls, so the
// reader's next key acts on no row they did not pick. INFRA-3135's review round 1, follow-up F4.
test("a row removed by a live update with no press leaves the focus on the page, in no row, scrolling nothing", async () => {
  await scrollingNothing(async () => {
    await withRescue(async () => {
      const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
      const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
      second.mark.focus();
      // In sight: the mark sits inside the box, which the window draws.
      placeAt(second.mark, 200);
      box.dispatchEvent(new Event("scroll"));
      second.row.remove();
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(box).not.toHaveFocus();
      expect(box).not.toHaveAttribute("tabindex");
      expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
      expect(rows[2]?.mark).not.toHaveFocus();
    });
  });
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
// no write - is a live update's, not theirs: the focus stays on the page, in no row, and nothing
// scrolls, as with no press at all (ag). The reader's next Space or `x` then marks no row they did
// not pick.
test("a row removed by a live update after the reader's press on it was answered hands the focus to no row", async () => {
  await scrollingNothing(async () => {
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
      expect(document.activeElement).toBe(document.body);
      expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
      expect(rows[2]?.mark).not.toHaveFocus();
    });
  });
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
// that is no doing of the reader's: the focus stays on the page, in no row, and nothing scrolls
// (ag).
test("a row removed after its pressed control's hold ended hands the focus to no row", async () => {
  await scrollingNothing(async () => {
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
      expect(document.activeElement).toBe(document.body);
      expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
      expect(rows[2]?.mark).not.toHaveFocus();
    });
  });
});

// (ax) The hold keeps its press live up to the write deadline (`SEND_DEADLINE_MS`) at most: a
// control its press's write holds that stays held past it - a write that never answers - keeps
// its press live no longer, so a live update taking its row away after that is no doing of the
// reader's: the focus stays on the page, in no row, and nothing scrolls (ag).
test("a row removed after its pressed control stayed held past the write deadline hands the focus to no row", async () => {
  jest.useFakeTimers();
  try {
    await scrollingNothing(async () => {
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
        expect(document.activeElement).toBe(document.body);
        expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
        expect(rows[2]?.mark).not.toHaveFocus();
      });
    });
  } finally {
    jest.useRealTimers();
  }
});

// (ay) A press on a control already held when it is pressed - the pager's Next on its last page, a
// select or a trigger held while an earlier write is out - is refused and leads to no write, so
// that hold is no doing of the press: the press is live a frame and a task after it and its
// release, as a press on a control held by nothing is (aq), and no longer. A live update taking
// the row away after that is no doing of the reader's: the focus stays on the page, in no row,
// and nothing scrolls (ag).
test("a row removed after a press on its control held before the press hands the focus to no row", async () => {
  await scrollingNothing(async () => {
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
      expect(document.activeElement).toBe(document.body);
      expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
      expect(rows[2]?.mark).not.toHaveFocus();
    });
  });
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
// reader's: the focus stays on the page, in no row, and nothing scrolls (ag).
test("a second press on a control its first press holds leaves the write deadline counted from the first release", async () => {
  jest.useFakeTimers();
  try {
    await scrollingNothing(async () => {
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
        // 20 s on, the reader presses the held control again and lets go, a frame and a task
        // before the clock moves on.
        jest.advanceTimersByTime(20_000);
        pointerPress(dismiss);
        await frame();
        jest.advanceTimersByTime(0);
        // The deadline from the first release passes with the control still held; the press
        // stops a frame and a task on.
        jest.advanceTimersByTime(SEND_DEADLINE_MS - 20_000);
        await frame();
        jest.advanceTimersByTime(0);
        expect(dismiss).toHaveFocus();
        second.row.remove();
        // The rescue's check, a frame and a task after the removal its watcher reads.
        await frame();
        await frame();
        jest.advanceTimersByTime(0);
        expect(document.activeElement).toBe(document.body);
        expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
        expect(rows[2]?.mark).not.toHaveFocus();
      });
    });
  } finally {
    jest.useRealTimers();
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
// of the reader's: the focus stays on the page, in no row, and nothing scrolls (ag).
test("a row its list's pick held, taken away after the hold ended, hands the focus to no row", async () => {
  await scrollingNothing(async () => {
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
      expect(document.activeElement).toBe(document.body);
      expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
      expect(rows[2]?.mark).not.toHaveFocus();
    });
  });
});

/** The page's writes, signed in as the app's are, for a case to hand the rescue as `app.tsx`
 *  hands it the app's (`useFocusRescue`'s `writes`). */
function signedInWrites(): Writes {
  return writesOf(signedIn());
}

/** A write `writes` makes at the reader's press, as a press's handler makes one, out until the
 *  case answers it: `land` answers it and `refuse` has the server refuse it, each waiting until
 *  the press that awaits it has its answer. */
function writeOf(writes: Writes): { land: () => Promise<void>; refuse: () => Promise<void> } {
  const answer = Promise.withResolvers<object>();
  const made = writes.run({
    detail: null,
    kind: plainWrite,
    label: "The rename of the group Crew",
    method: "PATCH",
    presentation: "indicator",
    write: () => answer.promise,
  });
  // A write the case leaves out is let go after it (`setup.ts`), which refuses its press.
  made.catch(() => undefined);
  return {
    land: async () => {
      answer.resolve({});
      await made;
    },
    refuse: async () => {
      answer.reject(new Error("Dispatch refused the write"));
      await made.catch(() => undefined);
    },
  };
}

// (cl) A press is the cause only of the loss of its own control and of what its own action takes
// away. Its action is the write it started and the holds on what answers that write. Once the
// write has landed with no such hold on, the press is answered a frame and a task later. A
// control it reads as that is held after that is held for nothing the press does: the session
// groups editor's Rename, once the update that carries its rename is drawn, held by
// `aria-disabled` because the name is the stored one. The card deleted on another device after
// that is a loss the reader did not cause, so the focus goes to the open modal dialog's own stop
// with no scroll, never to the control before the lost place, by Chromium's `focusout` as by none.
test("a press whose write has landed, its control then held with nothing to do, causes no later loss in its dialog", async () => {
  for (const withFocusout of [true, false]) {
    const writes = signedInWrites();
    await scrollingNothing(async () => {
      await withRescue(async () => {
        const { buttons, dialog } = modalOf("Close", "Members", "Rename");
        const [, members, rename] = buttons as [HTMLButtonElement, HTMLButtonElement, HTMLElement];
        rename.focus();
        enterPress(rename);
        const write = writeOf(writes);
        // Held while its write is out, and, once the update that carries it is drawn, because the
        // name it would set is the stored one.
        rename.setAttribute("aria-disabled", "true");
        await settled();
        await write.land();
        for (const _ of [0, 1, 2]) await settled();
        expect(rename).toHaveFocus();
        if (withFocusout) lose(rename);
        else rename.remove();
        await settled();
        expect(dialog).toHaveFocus();
        expect(members).not.toHaveFocus();
      }, writes);
    });
  }
});

// (cm) So for a press on another control of the dialog, as in (cl): Create group, its write
// landed, and the button held after it, disabled with its name box emptied. Its press is answered
// once the write has landed, so the card deleted on another device while the focus rests on its
// Rename, put there with no press, is no doing of the reader's, though a press in a dialog counts
// for a loss anywhere in it (`pressPlaces`). The focus goes to the dialog's own stop with no
// scroll, by Chromium's `focusout` as by none.
test("a press on another control of a dialog, its write landed and that control then held, causes no later loss in the dialog", async () => {
  for (const withFocusout of [true, false]) {
    const writes = signedInWrites();
    await scrollingNothing(async () => {
      await withRescue(async () => {
        const { buttons, dialog } = modalOf("Close", "Create group", "Rename");
        const [close, create, rename] = buttons as [HTMLElement, HTMLButtonElement, HTMLElement];
        create.focus();
        pointerPress(create);
        const write = writeOf(writes);
        // Disabled while its write is out, and after it, with its name box emptied.
        create.disabled = true;
        await settled();
        await write.land();
        rename.focus();
        for (const _ of [0, 1, 2]) await settled();
        if (withFocusout) lose(rename);
        else rename.remove();
        await settled();
        expect(dialog).toHaveFocus();
        expect(close).not.toHaveFocus();
      }, writes);
    });
  }
});

// (cn) A press whose write is out is live while that write is out, however many frames it takes
// and whether or not a control shows it held: the row the write takes away - Dismiss answered -
// is the reader's doing, and the focus goes to the control now at the row's place, as in (ar).
test("a press whose write is out makes the going of its control's row the reader's", async () => {
  const writes = signedInWrites();
  await withRescue(async () => {
    const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
    const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
    const third = rows[2] as { mark: HTMLInputElement; row: HTMLElement };
    const dismiss = buttonIn(second.row, "Dismiss");
    dismiss.focus();
    placeAt(dismiss, 200);
    box.dispatchEvent(new Event("scroll"));
    pointerPress(dismiss);
    writeOf(writes);
    for (const _ of [0, 1, 2]) await settled();
    expect(dismiss).toHaveFocus();
    second.row.remove();
    await waitFor(() => expect(third.mark).toHaveFocus());
  }, writes);
});

// (co) A control held through the re-read that draws what its write changed (`useReadBound`'s
// `holdThrough`) is held for the press's action while that read is out: the row the read takes
// away after the write has landed - the List's row whose Start next moved its issue up the queue
// - is still the reader's doing, and the focus goes to the control now at the row's place, as in
// (ar).
test("a press whose control is held through its write's re-read makes the going of its row the reader's", async () => {
  const writes = signedInWrites();
  const bound = renderHook(() => useReadBound());
  try {
    await withRescue(async () => {
      const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
      const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
      const third = rows[2] as { mark: HTMLInputElement; row: HTMLElement };
      const startNext = buttonIn(second.row, "Start next");
      startNext.focus();
      placeAt(startNext, 200);
      box.dispatchEvent(new Event("scroll"));
      pointerPress(startNext);
      const write = writeOf(writes);
      // Held while its write is out and through the read after it (`heldPress`).
      startNext.setAttribute("aria-disabled", "true");
      await settled();
      await write.land();
      // The write's `onSuccess` holds it through the list's read, as it lands.
      const reread = Promise.withResolvers<void>();
      const held = bound.result.current[1](reread.promise);
      for (const _ of [0, 1, 2]) await settled();
      expect(startNext).toHaveFocus();
      second.row.remove();
      await waitFor(() => expect(third.mark).toHaveFocus());
      reread.resolve();
      await held;
    }, writes);
  } finally {
    bound.unmount();
  }
});

// (cr) A press's action lasts as long as its write and then the re-read that answers it, whose
// hold has a deadline of its own (`useReadBound`, `REQUEST_DEADLINE_MS` from the read's start).
// So a write that lands within the write deadline (`SEND_DEADLINE_MS`) keeps its press live past
// that deadline while the hold through its re-read is still on, and until a frame and a task after
// that hold ends. The row the commit after the read takes away - the List's row whose Start next
// moved its issue up the queue, read again 25 s after the write landed - is still the reader's
// doing, and the focus goes to the control now at the row's place, as in (co).
test("a press whose re-read is still held past the write deadline stays live until that hold ends, and the row taken away then is the reader's", async () => {
  jest.useFakeTimers();
  const writes = signedInWrites();
  const bound = renderHook(() => useReadBound());
  try {
    await withRescue(async () => {
      const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
      const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
      const third = rows[2] as { mark: HTMLInputElement; row: HTMLElement };
      const startNext = buttonIn(second.row, "Start next");
      startNext.focus();
      placeAt(startNext, 200);
      box.dispatchEvent(new Event("scroll"));
      pointerPress(startNext);
      const write = writeOf(writes);
      // Held while its write is out and through the read after it (`heldPress`).
      startNext.setAttribute("aria-disabled", "true");
      // A frame and a task after the release the press watches its action.
      await frame();
      jest.advanceTimersByTime(0);
      // The write lands 10 s on, and its `onSuccess` holds it through the list's read.
      jest.advanceTimersByTime(10_000);
      await write.land();
      const reread = Promise.withResolvers<void>();
      const held = bound.result.current[1](reread.promise);
      // The write deadline passes with the read still out. A press it ended would end a frame
      // and a task on.
      jest.advanceTimersByTime(SEND_DEADLINE_MS - 10_000);
      await frame();
      jest.advanceTimersByTime(0);
      expect(startNext).toHaveFocus();
      // The read settles 5 s past the write deadline, within its own, and the commit after it
      // takes the row away.
      jest.advanceTimersByTime(5_000);
      reread.resolve();
      await held;
      second.row.remove();
      // The rescue's check, a frame and a task after the removal its watcher reads.
      await frame();
      await frame();
      jest.advanceTimersByTime(0);
      expect(third.mark).toHaveFocus();
    }, writes);
  } finally {
    bound.unmount();
    jest.useRealTimers();
  }
});

// (cs) A hold on what answers a press keeps it live past the write deadline until the hold ends,
// and at the latest until `SEND_DEADLINE_MS + REQUEST_DEADLINE_MS` after its release, the longest
// a write that lands within its deadline, and then its re-read, can take. So a hold that never
// ends - an update the press waits for that never comes - keeps the press live no longer than
// that. A row taken away a second short of that bound is the reader's doing, and the focus goes
// to the control now at the row's place, as in (ar). A row taken away once the bound has passed
// is no doing of the reader's: the focus stays on the page, in no row (ag).
test("a press whose answer hold never ends is live until the write and request deadlines have passed after its release, and no longer", async () => {
  jest.useFakeTimers();
  try {
    // Where the focus went after the row was taken away at each wait, and where it belongs.
    const landed: Record<string, string | null> = {};
    const waits = {
      "a second short of the bound": SEND_DEADLINE_MS + REQUEST_DEADLINE_MS - 1_000,
      "once the bound has passed": SEND_DEADLINE_MS + REQUEST_DEADLINE_MS,
    };
    for (const [named, wait] of Object.entries(waits)) {
      const writes = signedInWrites();
      await withRescue(async () => {
        const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
        const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
        const dismiss = buttonIn(second.row, "Dismiss");
        dismiss.focus();
        placeAt(dismiss, 200);
        box.dispatchEvent(new Event("scroll"));
        pointerPress(dismiss);
        const write = writeOf(writes);
        // A frame and a task after the release the press watches its action.
        await frame();
        jest.advanceTimersByTime(0);
        await write.land();
        // A hold on what answers the write, begun as it lands, that never ends.
        beginAnswerHold();
        jest.advanceTimersByTime(wait);
        // A press its bound ended ends a frame and a task on.
        await frame();
        jest.advanceTimersByTime(0);
        expect(dismiss).toHaveFocus();
        second.row.remove();
        // The rescue's check, a frame and a task after the removal its watcher reads.
        await frame();
        await frame();
        jest.advanceTimersByTime(0);
        landed[named] = document.activeElement === document.body ? "the page" : focusedName();
      }, writes);
    }
    expect(landed).toEqual({
      "a second short of the bound": "Select Three",
      "once the bound has passed": "the page",
    });
  } finally {
    jest.useRealTimers();
  }
});

// (ct) So for a press that started no write and holds its own control (`watchHold`): past the
// write deadline the control's hold keeps the press live no longer (ax), but a hold on what
// answers the press, begun while it is live and still on then, keeps it live until that hold
// ends, and a frame and a task after. The hold ends 5 s past the deadline, and the commit after it
// takes the row away: that is still the reader's doing, and the focus goes to the control now at
// the row's place, as in (ar).
test("a press that started no write stays live past the write deadline while a hold on what answers it is on, and the row taken away after that hold ends is the reader's", async () => {
  jest.useFakeTimers();
  try {
    await withRescue(async () => {
      const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
      const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
      const third = rows[2] as { mark: HTMLInputElement; row: HTMLElement };
      const dismiss = buttonIn(second.row, "Dismiss");
      dismiss.focus();
      placeAt(dismiss, 200);
      box.dispatchEvent(new Event("scroll"));
      pointerPress(dismiss);
      dismiss.setAttribute("aria-disabled", "true");
      // A hold on what answers the press, begun as its action runs.
      const answered = beginAnswerHold();
      // A frame and a task after the release the press finds its control held, and watches it.
      await frame();
      jest.advanceTimersByTime(0);
      // The write deadline passes with the control and the hold still on. A press it ended would
      // end a frame and a task on.
      jest.advanceTimersByTime(SEND_DEADLINE_MS);
      await frame();
      jest.advanceTimersByTime(0);
      expect(dismiss).toHaveFocus();
      // The hold ends 5 s past the deadline, and the commit after it takes the row away.
      jest.advanceTimersByTime(5_000);
      answered();
      second.row.remove();
      // The rescue's check, a frame and a task after the removal its watcher reads.
      await frame();
      await frame();
      jest.advanceTimersByTime(0);
      expect(third.mark).toHaveFocus();
    });
  } finally {
    jest.useRealTimers();
  }
});

// (cu) A press that started no write and holds no control is still live while a hold on what
// answers it is on: an action whose first write starts after the press's first frame, as the
// List's merge reads every issue before it moves any, begins its hold within that frame
// (`perform`'s `beginAnswerHold`). The press stays live until a frame and a task after the hold
// ends 5 s on, so the row the commit after that end takes away is the reader's doing, and the
// focus goes to the control now at the row's place, as in (ar).
test("a press on a control that is not held stays live while a hold on what answers it is on, and the row taken away after that hold ends is the reader's", async () => {
  jest.useFakeTimers();
  try {
    await withRescue(async () => {
      const { box, rows } = rowBoxIn(mainOf("Before").main, "One", "Two", "Three");
      const second = rows[1] as { mark: HTMLInputElement; row: HTMLElement };
      const third = rows[2] as { mark: HTMLInputElement; row: HTMLElement };
      const finish = buttonIn(second.row, "Finish");
      finish.focus();
      placeAt(finish, 200);
      box.dispatchEvent(new Event("scroll"));
      pointerPress(finish);
      // A hold on what answers the press, begun as its action runs; the control is never held.
      const answered = beginAnswerHold();
      // A frame and a task after the release the press finds no control held. A press that ended
      // there would be no cause of the row's going.
      await frame();
      jest.advanceTimersByTime(0);
      await frame();
      jest.advanceTimersByTime(0);
      expect(finish).toHaveFocus();
      // The hold ends 5 s on, and the commit after it takes the row away.
      jest.advanceTimersByTime(5_000);
      answered();
      second.row.remove();
      // The rescue's check, a frame and a task after the removal its watcher reads.
      await frame();
      await frame();
      jest.advanceTimersByTime(0);
      expect(third.mark).toHaveFocus();
    });
  } finally {
    jest.useRealTimers();
  }
});

// (cp) A press that starts no write keeps the rule of a control it holds, with the page's writes
// handed to the rescue as well: the pager's Next, held on the last page it led to, keeps its press
// live while it is held, so the pager drawn again without it is the reader's doing, and the focus
// walks back to Previous.
test("a press that starts no write stays live while it holds its own control, the page's writes handed to the rescue", async () => {
  const writes = signedInWrites();
  await withRescue(async () => {
    const [previous, next] = mainOf("Previous", "Next").buttons as [
      HTMLButtonElement,
      HTMLButtonElement,
    ];
    next.focus();
    pointerPress(next);
    await holdFor(next);
    expect(next).toHaveFocus();
    lose(next);
    await waitFor(() => expect(previous).toHaveFocus());
  }, writes);
});

// (cq) A press in a dialog counts for a loss anywhere in that dialog (`pressPlaces`), and the
// dialog a lost control was in is the one the rescue noted around it as it took the focus. Read
// from that note, a control taken away with no `focusout`, as Firefox and WebKit take it, is read
// in its dialog, as Chromium's `focusout` reads it while the control is still there. Right after
// the reader's press on another control of the dialog, the focus walks back inside the dialog to
// the control before the lost one, by Chromium's `focusout` as by none.
test("a control taken away with no focusout is read in the dialog the rescue noted it in, as with one", async () => {
  for (const withFocusout of [true, false]) {
    await withRescue(async () => {
      const { buttons } = modalOf("First", "Second", "Third");
      const [first, second, third] = buttons as [
        HTMLButtonElement,
        HTMLButtonElement,
        HTMLButtonElement,
      ];
      third.focus();
      pointerPress(first);
      if (withFocusout) lose(third);
      else third.remove();
      await settled();
      expect(second).toHaveFocus();
    });
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
      enterPress(dismiss);
      if (withFocusout) lose(dismiss);
      else dismiss.remove();
      await waitFor(() => expect(third).toHaveFocus());
    });
  }
  await scrollingNothing(async () => {
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
      enterPress(dismiss);
      dismiss.remove();
      await settled();
      expect(document.activeElement).toBe(document.body);
      expect(third).not.toHaveFocus();
    });
  });
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
  await scrollingNothing(async () => {
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
  });
});

// A press whose commit draws a disclosure where its control stood - the Artifacts tab's
// references Retry, whose read draws the Referenced by disclosure in place of the failed line -
// lands on the disclosure's summary, focused with no scroll. The keyboard reaches a `<details>`'s
// summary as it reaches a button, and it comes first in what the press drew there. With
// Chromium's `focusout` and without.
test("a control replaced by a disclosure hands the focus to the disclosure's summary, scrolling nothing", async () => {
  await scrollingNothing(async () => {
    for (const withFocusout of [true, false]) {
      await withRescue(async () => {
        const { buttons, main } = mainOf("Upload", "Remove");
        const remove = buttons[1] as HTMLButtonElement;
        const failed = document.createElement("div");
        failed.setAttribute("role", "alert");
        const retry = buttonIn(failed, "Retry");
        main.insertBefore(failed, remove);
        const references = document.createElement("div");
        const disclosure = document.createElement("details");
        const summary = document.createElement("summary");
        summary.textContent = "Referenced by (2)";
        const link = document.createElement("a");
        link.href = "/issues/OPS-1";
        link.textContent = "Depends on CORE-1";
        disclosure.append(summary, link);
        references.append(disclosure);
        retry.focus();
        enterPress(retry);
        // The read's answer takes the failed line away and draws the disclosure where it stood.
        const replace = () => {
          failed.remove();
          main.insertBefore(references, remove);
        };
        if (withFocusout) lose(retry, replace);
        else replace();
        await settled();
        expect(focusedName()).toBe("Referenced by (2)");
      });
    }
  });
});

/** A region drawn for one item, as the Inbox draws its reading column: marked with the item it
 *  shows (`SHOWN_ITEM_ATTRIBUTE`), holding the item's answer form first - its text box and its
 *  Send - and then its Snooze. */
interface ItemRegion {
  readonly answer: HTMLTextAreaElement;
  readonly region: HTMLElement;
  readonly snooze: HTMLButtonElement;
}

/** The region drawn for `item` (`ItemRegion`), not yet in the page. */
function itemRegion(item: string): ItemRegion {
  const region = document.createElement("div");
  region.setAttribute(SHOWN_ITEM_ATTRIBUTE, item);
  const form = document.createElement("form");
  const answer = document.createElement("textarea");
  answer.setAttribute("aria-label", `Your answer to ${item}`);
  form.append(answer);
  buttonIn(form, "Send");
  region.append(form);
  return { answer, region, snooze: buttonIn(region, `Snooze ${item}`) };
}

/** The reader's `press` on the Snooze of the region drawn for item `one`, in the main content
 *  between Before and After, whose commit takes that region away and draws `drawn` in its place:
 *  with the `focusout` Chromium fires for the Snooze, or with none, as Firefox and WebKit. */
function snoozeAway(
  press: (element: Element) => void,
  withFocusout: boolean,
  drawn: ItemRegion
): void {
  const { buttons, main } = mainOf("Before", "After");
  const after = buttons[1] as HTMLButtonElement;
  const shown = itemRegion("one");
  main.insertBefore(shown.region, after);
  shown.snooze.focus();
  press(shown.snooze);
  const redraw = () => {
    shown.region.remove();
    main.insertBefore(drawn.region, after);
  };
  if (withFocusout) lose(shown.snooze, redraw);
  else redraw();
}

// A press that takes away the item its region shows - Snooze in the head of the Inbox's reading
// column - and a commit that draws the region again in its place for the next item, its answer
// form first. What stands where the pressed control stood is the next item of the same column,
// not what the press drew there, so the rescue lands in no control of that form, whose text box
// would take the reader's next key as text: the loss is answered as where nothing new stands, and
// the focus walks back by count. By a pointer press and by Enter, with Chromium's `focusout` and
// without.
test("a press that takes away the item its region shows lands in no form of the next item drawn in its place", async () => {
  // Where each press landed, and where it belongs.
  const landed: Record<string, string> = {};
  const belongs: Record<string, string> = {};
  for (const [by, press] of Object.entries(PRESSES)) {
    for (const withFocusout of [true, false]) {
      const named = `by ${by}, ${withFocusout ? "with" : "without"} a focusout`;
      await withRescue(async () => {
        const drawn = itemRegion("two");
        snoozeAway(press, withFocusout, drawn);
        await settled();
        const form = drawn.region.querySelector("form");
        landed[named] =
          form?.contains(document.activeElement) === true
            ? `in the next item's form, on ${focusedName()}`
            : document.activeElement === document.body
              ? "on the page"
              : "on a control outside the next item's form";
      });
      belongs[named] = "on a control outside the next item's form";
    }
  }
  expect(landed).toEqual(belongs);
});

// The same press with the region drawn again for the same item - a form opened in place, an edit
// mode - is what the press drew where its control stood: the focus lands on the form's first
// control, focused with no scroll, as for any control replaced by content holding a form (aj). By
// a pointer press and by Enter, with Chromium's `focusout` and without.
test("a press whose region is drawn again for the same item hands the focus to its form's first control, scrolling nothing", async () => {
  await scrollingNothing(async () => {
    for (const press of Object.values(PRESSES)) {
      for (const withFocusout of [true, false]) {
        await withRescue(async () => {
          const drawn = itemRegion("one");
          const focus = spyOn(drawn.answer, "focus");
          snoozeAway(press, withFocusout, drawn);
          await settled();
          expect(drawn.answer).toHaveFocus();
          expect(focus).toHaveBeenCalledWith({ preventScroll: true });
        });
      }
    }
  });
});

/** The reader's `press` on the Snooze of the region drawn for item `one`, in the main content
 *  between Before and After, whose commit keeps that region, marks it with `item` and draws what
 *  it holds again - the item's answer form first, then its Snooze - as React keeps the Inbox's
 *  reading column wrapper and changes only its mark when the column shows another question: with
 *  the `focusout` Chromium fires for the Snooze, or with none, as Firefox and WebKit. Answers the
 *  region with what it holds now. */
function snoozeInPlace(
  press: (element: Element) => void,
  withFocusout: boolean,
  item: string
): ItemRegion {
  const { buttons, main } = mainOf("Before", "After");
  const after = buttons[1] as HTMLButtonElement;
  const shown = itemRegion("one");
  main.insertBefore(shown.region, after);
  shown.snooze.focus();
  press(shown.snooze);
  const drawn = itemRegion(item);
  const redraw = () => {
    shown.region.setAttribute(SHOWN_ITEM_ATTRIBUTE, item);
    shown.region.replaceChildren(...drawn.region.children);
  };
  if (withFocusout) lose(shown.snooze, redraw);
  else redraw();
  return { ...drawn, region: shown.region };
}

// A press that takes away the item its region shows, whose commit keeps the region, marks it with
// the next item and draws only what it holds again, as the Inbox keeps its reading column's
// wrapper when the selection moves on with the column still shown. What arrived in the region
// carries no mark of its own and is still the next item's, read off the region around it, so the
// rescue lands in no control of that item's form. By a pointer press and by Enter, with
// Chromium's `focusout` and without.
test("a press whose region stays, marked with the next item, lands in no form of what the region draws again", async () => {
  // Where each press landed, and where it belongs.
  const landed: Record<string, string> = {};
  const belongs: Record<string, string> = {};
  for (const [by, press] of Object.entries(PRESSES)) {
    for (const withFocusout of [true, false]) {
      const named = `by ${by}, ${withFocusout ? "with" : "without"} a focusout`;
      await withRescue(async () => {
        const drawn = snoozeInPlace(press, withFocusout, "two");
        await settled();
        const form = drawn.region.querySelector("form");
        landed[named] =
          form?.contains(document.activeElement) === true
            ? `in the next item's form, on ${focusedName()}`
            : document.activeElement === document.body
              ? "on the page"
              : "on a control outside the next item's form";
      });
      belongs[named] = "on a control outside the next item's form";
    }
  }
  expect(landed).toEqual(belongs);
});

// The same press whose commit keeps the region for the same item and draws what it holds again -
// a form opened in place - is what the press drew where its control stood: the focus lands on the
// form's first control, focused with no scroll. The Snooze went from inside the region, so once
// it has gone no mark is around it to read, and the rescue reads the item it was in as it took
// the focus. By a pointer press and by Enter, with Chromium's `focusout` and without.
test("a press whose region stays for the same item, drawing what it holds again, hands the focus to its form's first control, scrolling nothing", async () => {
  await scrollingNothing(async () => {
    for (const press of Object.values(PRESSES)) {
      for (const withFocusout of [true, false]) {
        await withRescue(async () => {
          const drawn = snoozeInPlace(press, withFocusout, "one");
          const focus = spyOn(drawn.answer, "focus");
          await settled();
          expect(drawn.answer).toHaveFocus();
          expect(focus).toHaveBeenCalledWith({ preventScroll: true });
        });
      }
    }
  });
});

// (cj) A caused walk-back passes over every control of another item shown in the lost control's
// place, as the landing on what stands there does (above). The reader's press on the Snooze of item
// one, the last control of the main content, takes item one away. The region in its place shows
// item two, drawn anew or kept with its mark changed, and holds item two's Send and Snooze at and
// before the lost place. The walk passes over both to Before, the nearest control before the
// region. By a pointer press and by Enter, with Chromium's `focusout` and without.
test("a caused walk-back passes over the controls of the next item shown in the lost control's place", async () => {
  // Where each press landed, and where it belongs.
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  for (const kept of [false, true]) {
    for (const [by, press] of Object.entries(PRESSES)) {
      for (const withFocusout of [true, false]) {
        const region = kept ? "kept, marked with item two" : "drawn anew for item two";
        const named = `the region ${region}, by ${by}, ${withFocusout ? "with" : "without"} a focusout`;
        await withRescue(async () => {
          const { main } = mainOf("Before");
          const shown = itemRegion("one");
          main.append(shown.region);
          shown.snooze.focus();
          press(shown.snooze);
          const next = itemRegion("two");
          const redraw = kept
            ? () => {
                shown.region.setAttribute(SHOWN_ITEM_ATTRIBUTE, "two");
                shown.region.replaceChildren(...next.region.children);
              }
            : () => {
                shown.region.remove();
                main.append(next.region);
              };
          if (withFocusout) lose(shown.snooze, redraw);
          else redraw();
          await settled();
          landed[named] = document.activeElement === document.body ? "the page" : focusedName();
        });
        belongs[named] = "Before";
      }
    }
  }
  expect(landed).toEqual(belongs);
});

// (ck) So for a walk back from the place a list was opened from. The reader's pick in a list that
// item one's Snooze opened takes the list away. Where the pick takes item one away too, and the
// region shows item two in its place, the walk from the Snooze's place passes over item two's
// controls to Before. Where it takes only the Snooze away, item one is still shown, and the walk
// lands on item one's Send, the nearest control before that place. With Chromium's `focusout` and
// without.
test("a walk back from a list's trigger its pick took away passes over the controls of another item, and not of the trigger's own", async () => {
  // Where each pick landed, and where it belongs.
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  for (const itemGoes of [true, false]) {
    for (const withFocusout of [true, false]) {
      const taken = itemGoes ? "item one taken away" : "the Snooze alone taken away";
      const named = `${taken}, ${withFocusout ? "with" : "without"} a focusout`;
      await withRescue(async () => {
        const { main } = mainOf("Before");
        const shown = itemRegion("one");
        main.append(shown.region);
        const list = openListFrom(shown.snooze);
        const next = itemRegion("two");
        const commit = () => {
          list.panel.remove();
          if (itemGoes) {
            shown.region.remove();
            main.append(next.region);
          } else {
            shown.snooze.remove();
          }
        };
        pointerPress(list.option);
        if (withFocusout) lose(list.box, commit);
        else commit();
        await settled();
        landed[named] = document.activeElement === document.body ? "the page" : focusedName();
      });
      belongs[named] = itemGoes ? "Before" : "Send";
    }
  }
  expect(landed).toEqual(belongs);
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
      enterPress(dismiss);
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
      enterPress(pressed);
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

// (bz) A caused walk-back (ak) passes over every control that takes typed text (`takesText`):
// the reader never chose that field, and their next key would be typed into it, as Delete group on
// the delivery editor's only group walked back to its "New group name" box. Delete pressed after a
// Title box lands on the control before that box; where a Note box now stands at Delete's place,
// the walk passes over it too. By Enter, with Chromium's `focusout` and without.
test("a caused walk-back passes over the text boxes at and before the lost place to the nearest control that takes no text", async () => {
  // Where each press landed, and where it belongs.
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  for (const noteAfter of [false, true]) {
    for (const withFocusout of [true, false]) {
      const after = noteAfter ? "a Note box" : "nothing";
      const named = `${after} after Delete, ${withFocusout ? "with" : "without"} a focusout`;
      await withRescue(async () => {
        const { main } = mainOf("Before");
        const title = document.createElement("input");
        title.setAttribute("aria-label", "Title");
        main.append(title);
        const pressed = buttonIn(main, "Delete");
        if (noteAfter) {
          const note = document.createElement("textarea");
          note.setAttribute("aria-label", "Note");
          main.append(note);
        }
        pressed.focus();
        enterPress(pressed);
        if (withFocusout) lose(pressed);
        else pressed.remove();
        await settled();
        landed[named] = document.activeElement === document.body ? "the page" : focusedName();
      });
      belongs[named] = "Before";
    }
  }
  expect(landed).toEqual(belongs);
});

// (ca) Where only controls that take typed text stand before the lost place, the caused walk-back
// of (bz) offers nothing, and the region's own stop takes the focus, as it does where nothing at
// all stands before it: the main content, behind its Search box and a Note box, and a modal
// dialog, behind the same two. By Enter, with Chromium's `focusout` and without.
test("a caused walk-back with only text boxes before the lost place lands on the region's own stop", async () => {
  // Where each press landed, and where it belongs.
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  for (const inDialog of [false, true]) {
    for (const withFocusout of [true, false]) {
      const where = inDialog ? "a modal dialog" : "the main content";
      const named = `in ${where}, ${withFocusout ? "with" : "without"} a focusout`;
      await withRescue(async () => {
        const { main } = mainOf();
        const region = inDialog ? modalOf().dialog : main;
        const search = document.createElement("input");
        search.type = "search";
        search.setAttribute("aria-label", "Search");
        const note = document.createElement("textarea");
        note.setAttribute("aria-label", "Note");
        region.append(search, note);
        const pressed = buttonIn(region, "Delete");
        pressed.focus();
        enterPress(pressed);
        if (withFocusout) lose(pressed);
        else pressed.remove();
        await settled();
        landed[named] =
          document.activeElement === region
            ? "the region's own stop"
            : document.activeElement === document.body
              ? "the page"
              : focusedName();
      });
      belongs[named] = "the region's own stop";
    }
  }
  expect(landed).toEqual(belongs);
});

// (cb) A caused walk-back (ak) passes over every control out of the reader's sight, as it passes
// over a text box (bz): a landing may scroll its control to its nearest edge, and the nearest
// control before the lost place can lie far from where the press put the reader. Jump to latest
// went once it had scrolled an agent's transcript to its end, and the walk landed on the
// transcript's last control, which scrolled the transcript back from its end, 2515 px down, to
// 1146 px, measured in Chromium. The nearest control in sight before the lost place takes the
// focus, past one its scroll box holds above that box's top edge and one the page holds above
// the window; where none is in sight, the region's own stop takes it. Nothing scrolls either
// way: the header the walk lands on stands wholly in sight, and only a landing partly in sight
// is scrolled, to its nearest edge (`land`). By Enter, with Chromium's `focusout` and without.
test("a caused walk-back passes over the controls out of sight to the nearest in sight, else the region's own stop", async () => {
  // Where each press landed and what it scrolled, and where it belongs.
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  const scrolled: Element[] = [];
  const scrolls = spyOn(Element.prototype, "scrollIntoView").mockImplementation(function (
    this: Element
  ) {
    scrolled.push(this);
  });
  try {
    for (const headerShown of [true, false]) {
      for (const withFocusout of [true, false]) {
        const named = `${headerShown ? "the header in sight" : "nothing in sight"}, ${withFocusout ? "with" : "without"} a focusout`;
        await withRescue(async () => {
          scrolled.length = 0;
          const { buttons, main } = mainOf("Header", "Above");
          const [header, above] = buttons as [HTMLButtonElement, HTMLButtonElement];
          placeAt(header, headerShown ? 20 : -200);
          placeAt(above, -1_000);
          // The transcript scrolls in a box of its own, 100 px to 400 px down, and holds its last
          // control above that box's top edge.
          const { box, buttons: inBox } = scrollBoxIn(main, "Last turn's fold");
          box.style.overflowY = "auto";
          placeAt(inBox[0] as HTMLElement, 40);
          const pressed = buttonIn(main, "Jump to latest");
          placeAt(pressed, 380);
          pressed.focus();
          enterPress(pressed);
          if (withFocusout) lose(pressed);
          else pressed.remove();
          await settled();
          const at =
            document.activeElement === main
              ? "the region's own stop"
              : document.activeElement === document.body
                ? "the page"
                : focusedName();
          landed[named] =
            `${at}, scrolling ${scrolled.length === 0 ? "nothing" : scrolled.map((element) => element.textContent).join(", ")}`;
        });
        belongs[named] = headerShown
          ? "Header, scrolling nothing"
          : "the region's own stop, scrolling nothing";
      }
    }
  } finally {
    scrolls.mockRestore();
  }
  expect(landed).toEqual(belongs);
});

// (cc) A hand-off lands only on a control the Tab order reaches (`IN_TAB_ORDER`): one the page
// took out of it with `tabindex="-1"` is a place no key could have taken the reader to. "66 more
// rows below" on the issue page's outline drew its rows in its place, each of their controls out
// of the Tab order, and the rescue landed on the first row's "more · 106 words", which opened
// that row's body and drew its words whole (Chromium, 390 px). What the press drew here holds no
// control the Tab order reaches, so the focus walks back, past a strip's scroll button the
// pointer alone uses, to the control before it. By Enter, with Chromium's `focusout` and without.
test("a hand-off lands on no control out of the Tab order, in what a press drew or on its walk back", async () => {
  // Where each press landed, and where it belongs.
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  for (const withFocusout of [true, false]) {
    const named = withFocusout ? "with a focusout" : "without a focusout";
    await withRescue(async () => {
      const { main } = mainOf("Before");
      buttonIn(main, "Scroll left").tabIndex = -1;
      const pressed = buttonIn(main, "66 more rows below");
      pressed.focus();
      enterPress(pressed);
      const rows = document.createElement("div");
      buttonIn(rows, "more · 106 words").tabIndex = -1;
      const leave = () => pressed.replaceWith(rows);
      if (withFocusout) lose(pressed, leave);
      else leave();
      await settled();
      landed[named] = document.activeElement === document.body ? "the page" : focusedName();
    });
    belongs[named] = "Before";
  }
  expect(landed).toEqual(belongs);
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
    for (const [by, press] of Object.entries(PRESSES)) {
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

// The same Unpin with the reader scrolled so far down that the row that moves up into the
// pressed row's place, and every control before it, stand above the window, as the Inbox's next
// row does at a phone's width once a card's Answer takes its tall row away. The row that moved up
// takes the focus only where the reader can see it, as a walk's landing does: here nothing in
// sight stands at the place or before it, so the main content's own stop takes the focus, with
// no scroll. By Enter, with Chromium's `focusout` and without.
test("a row that moves up into the place of a row the reader's press took away takes no focus while it stands out of the reader's sight", async () => {
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  for (const withFocusout of [true, false]) {
    const named = withFocusout ? "with a focusout" : "without a focusout";
    await scrollingNothing(async () => {
      await withRescue(async () => {
        const { main } = mainOf();
        const rows = pinnedAgentsIn(main, ["Alpha", "Beta", "Gamma"], []);
        for (const [index, { row }] of rows.entries()) {
          for (const control of row.querySelectorAll("a, button")) {
            placeAt(control, index === 1 ? 300 : -400 + index * 40);
          }
        }
        const { row, unpin } = rows[1] as { row: HTMLLIElement; unpin: HTMLButtonElement };
        unpin.focus();
        enterPress(unpin);
        const leave = () => row.remove();
        if (withFocusout) lose(unpin, leave);
        else leave();
        await settled();
        landed[named] =
          document.activeElement === main ? "the main content's own stop" : focusedName();
      });
    });
    belongs[named] = "the main content's own stop";
  }
  expect(landed).toEqual(belongs);
});

/** The Inbox's rows, each holding an ask card, in a list at the end of `parent`: one `<li>` per
 *  name, holding an `<article>`, a row too by its element alone, with a link named for the ask
 *  and then its `Answer <name>` button. */
function askCardRowsIn(
  parent: Element,
  names: readonly string[]
): { answer: HTMLButtonElement; row: HTMLLIElement }[] {
  const list = document.createElement("ul");
  parent.append(list);
  return names.map((name) => {
    const row = document.createElement("li");
    const card = document.createElement("article");
    const link = document.createElement("a");
    link.href = `/asks/${name}`;
    link.textContent = name;
    card.append(link);
    row.append(card);
    list.append(row);
    return { answer: buttonIn(card, `Answer ${name}`), row };
  });
}

// (bs) Rows nest: an ask card, an `<article>`, in an Inbox row, an `<li>`. The rescue reads every
// row around the lost control, nearest first. The reader's press on the second card's Answer that
// takes the whole second `<li>` away takes the card with it, and the row that left is the `<li>`:
// the third `<li>` moves up, and the focus lands on the same control in it, the third card's
// Answer, in every engine, as (bn) does for rows that do not nest. A press on a control the `<li>`
// draws outside its card, Expand, is a press in that row, so a loss inside the card is the
// reader's too: where the press puts the focus on the card's Answer and its commit takes that
// Answer away, the focus walks to the control now at Answer's place, Expand; where the commit
// takes the whole `<li>` away, the focus lands on the third card's Answer. Firefox and WebKit
// report a control taken away alone once it has left the page, when no row stands around it any
// more, so that case is read with Chromium's `focusout` alone. By a pointer press and by Enter,
// with Chromium's `focusout` and without.
test("a row inside a row: the outer row a press takes away hands the focus to the same control in the row that moves up, and a press in the outer row causes a loss in the inner one", async () => {
  const cases: {
    landing: string;
    name: string;
    /** Whether the press is on the row's own Expand, outside its card, rather than on the
     *  card's Answer. */
    onRow: boolean;
    /** What the press's commit takes away: the whole row, or the card's Answer alone. */
    takes: "answer" | "row";
  }[] = [
    {
      landing: "Answer Gamma",
      name: "Answer Beta takes its row away",
      onRow: false,
      takes: "row",
    },
    {
      landing: "Expand Beta",
      name: "Expand Beta takes the card's Answer away",
      onRow: true,
      takes: "answer",
    },
    {
      landing: "Answer Gamma",
      name: "Expand Beta takes its row away",
      onRow: true,
      takes: "row",
    },
  ];
  // Where each press landed, and where it belongs.
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  for (const { landing, name, onRow, takes } of cases) {
    for (const [by, press] of Object.entries(PRESSES)) {
      for (const withFocusout of takes === "answer" ? [true] : [true, false]) {
        const named = `${name}, by ${by}, ${withFocusout ? "with" : "without"} a focusout`;
        await withRescue(async () => {
          const rows = askCardRowsIn(mainOf().main, ["Alpha", "Beta", "Gamma"]);
          const { answer, row } = rows[1] as { answer: HTMLButtonElement; row: HTMLLIElement };
          if (onRow) {
            const expand = buttonIn(row, "Expand Beta");
            expand.focus();
            press(expand);
            // The press puts the focus on the card's Answer.
            answer.focus();
          } else {
            answer.focus();
            press(answer);
          }
          const leave = () => (takes === "row" ? row : answer).remove();
          if (withFocusout) lose(answer, leave);
          else leave();
          await settled();
          landed[named] = document.activeElement === document.body ? "the page" : focusedName();
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
    enterPress(pressed);
    // A live update moves the `+` after Before; the press's commit then takes it away and puts
    // its form where it stood.
    main.insertBefore(pressed, after);
    pressed.remove();
    main.insertBefore(drawn, after);
    await settled();
    expect(first).toHaveFocus();
  });
});

/** The issue title's editor, put in `main` before `after`: its text box, the title a save draws in
 *  the box's place - its heading, which takes no focus, and the Edit title button laid over it -
 *  and that save's commit (`replace`), which takes the box away and draws the title there. */
function titleEditorIn(
  main: HTMLElement,
  after: HTMLElement
): { edit: HTMLButtonElement; field: HTMLInputElement; replace: () => void } {
  const field = document.createElement("input");
  field.setAttribute("aria-label", "Issue title");
  main.insertBefore(field, after);
  const title = document.createElement("div");
  const heading = document.createElement("h1");
  heading.textContent = "Title";
  const edit = document.createElement("button");
  edit.type = "button";
  edit.setAttribute("aria-label", "Edit title");
  title.append(heading, edit);
  const replace = () => {
    field.remove();
    main.insertBefore(title, after);
  };
  return { edit, field, replace };
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
// title and its Edit title button in its place: nothing moves, so Edit title is not handed the
// focus and the reader's next key acts on the page.
test("a field the page's own blur commits leaves the focus on the page when its commit takes it away", async () => {
  await withRescue(async () => {
    const { buttons, main } = mainOf("Before", "After");
    const { edit, field, replace } = titleEditorIn(main, buttons[1] as HTMLButtonElement);
    saveOnBlur(field, replace);
    field.addEventListener("keydown", (event) => {
      if (event.key === "Enter") field.blur();
    });
    field.focus();
    enterPress(field);
    await settled();
    expect(field.isConnected).toBe(false);
    expect(edit.isConnected).toBe(true);
    expect(document.activeElement).toBe(document.body);
    expect(edit).not.toHaveFocus();
  });
});

// (ao) So is the reader's click on the page's text while a field holds the focus: the press lands
// on no control, so the engine moves the focus to the page, and the save that blur makes takes
// the field away a microtask later. Nothing moves.
test("a click on the page's text that ends a field's edit leaves the focus on the page", async () => {
  await withRescue(async () => {
    const { buttons, main } = mainOf("Before", "After");
    const { edit, field, replace } = titleEditorIn(main, buttons[1] as HTMLButtonElement);
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
    expect(edit.isConnected).toBe(true);
    expect(document.activeElement).toBe(document.body);
    expect(edit).not.toHaveFocus();
  });
});

// (ap) The same field taken away while it holds the focus is still a loss to answer, however late
// its commit comes: Chromium fires the `focusout` during the removal itself, so the field has gone
// by the rescue's microtask, and Firefox and WebKit fire none. The commit is the reader's Enter's,
// as the issue title's is, so the loss is theirs, and the focus lands on the Edit title button
// drawn in the field's place (a loss the reader did not cause lands on nothing drawn in its place:
// (bd)).
test("the same field taken away while focused, a microtask after the event, hands the focus to the Edit title button drawn in its place", async () => {
  for (const withFocusout of [true, false]) {
    await withRescue(async () => {
      const { buttons, main } = mainOf("Before", "After");
      const { edit, field, replace } = titleEditorIn(main, buttons[1] as HTMLButtonElement);
      field.focus();
      enterPress(field);
      queueMicrotask(() => (withFocusout ? lose(field, replace) : replace()));
      await settled();
      expect(edit).toHaveFocus();
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
// focus goes into no other row and nothing scrolls: in the modal drawer it lands on the drawer's
// own stop, by Chromium's `focusout` as by none. At 90fa8211 the Tab is passed over (`notePress`,
// `onHeldControl`), the pick stays the cause, and a press's place in a dialog is the whole dialog
// (`pressPlaces`): on Chromium's path the focus is walked into row Three and scrolled to it; with
// no `focusout` the detached mark reaches no dialog, so it lands on the box, and the two engines
// disagree.
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
        expect(box.closest('[role="dialog"]')).toHaveFocus();
        expect(document.activeElement?.closest("[data-inbox-row]")).toBeNull();
        expect(rows[2]?.mark).not.toHaveFocus();
      });
      expect(scrolls).not.toHaveBeenCalled();
    } finally {
      scrolls.mockRestore();
    }
  }
});

// (bd) A loss the reader did not cause leaves the focus on the page, whatever the page drew in the
// lost control's place. A live update withdraws the question whose answer form holds the focus and
// draws a Reply form where that card stood, its text box first. The focus lands on no control of
// that form - not its text box, where the reader's next key would be typed and a Reply could be
// sent, nor its Send - nor on the control before the place: it stays on the page, with no scroll,
// by Chromium's `focusout` as by none. The page's keys answer there, as they did before the page
// had a rescue: `j` pressed on the page runs the page's binding.
test("an uncaused loss whose place now holds a form leaves the focus on the page, out of its text box", async () => {
  for (const withFocusout of [true, false]) {
    await scrollingNothing(async () => {
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
          expect(pin).not.toHaveFocus();
          expect(document.activeElement).toBe(document.body);
          const landed = document.activeElement as HTMLElement;
          landed.dispatchEvent(
            new KeyboardEvent("keydown", { bubbles: true, cancelable: true, key: "j" })
          );
          expect(walked).toEqual(["j"]);
        } finally {
          keys.unmount();
        }
      });
    });
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
// cause (ag): the focus stays on the page - in no row and on no field that takes typed text, not
// on the Reply text box the update draws in the row's place - and nothing scrolls. Measured in
// Chromium with the `change` read as the reader's pick: the loss read as caused, and the focus
// landed on that Reply text box, where the reader's next key was typed. A `<textarea>` with a
// character key and then Enter, which inserts a line there, and an `<input>` with a character
// key, each by Chromium's `focusout` and by none. The issue title's Enter, which commits its
// `<input>`, is still a press (ap).
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
      await scrollingNothing(async () => {
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
          expect(document.activeElement).toBe(document.body);
          expect(replyBox).not.toHaveFocus();
        });
      });
    }
  }
});

/** A bubbling `keydown` of `key` at `code`, held with the modifiers `held` names. Its
 *  `getModifierState` answers from those modifiers, as a browser's does, since happy-dom reads
 *  `AltGraph` off `altKey` and would take Option for AltGr. */
function keydownHolding(key: string, code: string, held: readonly string[]): KeyboardEvent {
  const down = new KeyboardEvent("keydown", {
    altKey: held.includes("Alt"),
    bubbles: true,
    code,
    ctrlKey: held.includes("Control"),
    key,
    metaKey: held.includes("Meta"),
  });
  Object.defineProperty(down, "getModifierState", {
    value: (modifier: string) => held.includes(modifier),
  });
  return down;
}

// (bj) A character typed with a modifier in a field that takes typed text is no press either
// (`isPress`): AltGr, which Windows reports as Ctrl and Alt together with `AltGraph` held, types
// `@` on a German layout, and macOS's Option, Alt held alone, types `@` on many layouts. So the
// update of (bi), after either character in a `<textarea>` or an `<input>`, is a loss the reader
// did not cause: the focus stays on the page, in no row and on no field that takes typed text,
// and nothing scrolls. Each keydown holds its modifiers as a browser reports them
// (`keydownHolding`).
test("a character typed with AltGr or Option in a text field makes no press, so an uncaused removal of the field takes the uncaused path", async () => {
  const characters = [
    { code: "KeyQ", held: ["AltGraph", "Control", "Alt"] },
    { code: "KeyL", held: ["Alt"] },
  ];
  for (const { code, held } of characters) {
    for (const tag of ["textarea", "input"] as const) {
      for (const withFocusout of [true, false]) {
        await scrollingNothing(async () => {
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
            field.dispatchEvent(keydownHolding("@", code, held));
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
            expect(document.activeElement).toBe(document.body);
            expect(replyBox).not.toHaveFocus();
          });
        });
      }
    }
  }
});

/** Each key that acts on the page rather than on a text field's text, by its name: the key, the
 *  modifiers held with it, and the field it is pressed in - a `<textarea>`, save for Enter, which
 *  acts only in a single-line `<input>`. */
const ACTING_KEYS: readonly {
  held: readonly string[];
  key: string;
  name: string;
  tag: "input" | "textarea";
}[] = [
  { held: [], key: "Escape", name: "Escape", tag: "textarea" },
  { held: [], key: "Tab", name: "Tab", tag: "textarea" },
  { held: [], key: "Enter", name: "Enter in a single-line input", tag: "input" },
  { held: ["Control"], key: "Enter", name: "Ctrl+Enter", tag: "textarea" },
  { held: ["Meta"], key: "Enter", name: "Meta+Enter", tag: "textarea" },
  { held: ["Alt"], key: "Enter", name: "Alt+Enter", tag: "textarea" },
];

// (bx) In a field that takes typed text, a key that acts on the page rather than on the text is a
// press of the reader's (`isPress`): Escape, Tab, Enter in a single-line `<input>`, which commits
// it, and a key held with Ctrl, Meta or Alt that types no character, a shortcut such as Ctrl+Enter
// that sends. So the field's removal at that key's release is a loss the reader caused, and the
// focus walks back to the control before the field, where a loss they did not cause leaves it on
// the page (bi). Escape's own task is the reader leaving the field (`leaving`), so each removal
// comes a frame and a task after the key, at its release, which keeps the press live as a click's
// release does (`noteRelease`). With Chromium's `focusout` and with none.
test("in a text field, Escape, Tab, Enter in a single-line input and a key held with Ctrl, Meta or Alt that types no character are the reader's press, so the field's removal walks back to the control before it", async () => {
  // Where the focus went for each key, with Chromium's `focusout` and with none.
  const landed: Record<string, string | null> = {};
  const walkedBack: Record<string, string> = {};
  for (const { held, key, name, tag } of ACTING_KEYS) {
    for (const withFocusout of [true, false]) {
      const named = `${name}, ${withFocusout ? "with" : "without"} a focusout`;
      await withRescue(async () => {
        const { main } = mainOf("Before");
        const field = document.createElement(tag);
        field.setAttribute("aria-label", "Note");
        main.append(field);
        field.focus();
        field.dispatchEvent(keydownHolding(key, key, held));
        await settled();
        field.dispatchEvent(new KeyboardEvent("keyup", { bubbles: true, code: key, key }));
        if (withFocusout) lose(field);
        else field.remove();
        await settled();
        landed[named] = document.activeElement === document.body ? "the page" : focusedName();
      });
      walkedBack[named] = "Before";
    }
  }
  expect(landed).toEqual(walkedBack);
});

// (by) A press of the reader's is the cause of a loss for a frame and a task after it (ah, aq), or
// for 100 ms and a task where no frame comes, as WebKit can hold a page's frames back (r). With
// the frames held back, a removal 1 ms short of 100 ms after the reader's Enter on Second is a loss
// they caused, and the focus walks to Third, now at its place; a removal 100 ms and a task after
// the Enter is a loss they did not cause, and the focus stays on the page.
test("with the page's frames held back, a removal within 100 ms and a task of the reader's press is a loss they caused, and one after that is not", async () => {
  const frames = spyOn(window, "requestAnimationFrame").mockImplementation(() => 0);
  jest.useFakeTimers();
  try {
    // Where each removal left the focus, and where it belongs.
    const landed: Record<string, string | null> = {};
    const belongs: Record<string, string> = {};
    const cases = [
      { after: FRAME_WAIT_MS - 1, landing: "Third", name: "1 ms short of 100 ms after the press" },
      { after: FRAME_WAIT_MS, landing: "the page", name: "100 ms and a task after the press" },
    ];
    for (const { after, landing, name } of cases) {
      await withRescue(async () => {
        const second = mainOf("First", "Second", "Third").buttons[1] as HTMLButtonElement;
        second.focus();
        enterPress(second);
        jest.advanceTimersByTime(after);
        jest.advanceTimersByTime(0);
        lose(second);
        // The rescue's check, 100 ms and a task after the loss, with no frame.
        jest.advanceTimersByTime(FRAME_WAIT_MS);
        jest.advanceTimersByTime(0);
        landed[name] = document.activeElement === document.body ? "the page" : focusedName();
      });
      belongs[name] = landing;
    }
    expect(landed).toEqual(belongs);
  } finally {
    jest.useRealTimers();
    frames.mockRestore();
  }
});

// (bk) A date box fires its `change` at the reader's pick - in its picker, or with the digit that
// completes the date - and not on blur, as a text field does, so that change is a press of theirs
// (`isPress`). The search bar's Filter panel draws its Updated since box only while no date is
// set (`SearchPalette.tsx`), so the pick takes the focused box away, and that loss is one the
// reader caused. Its walk-back passes over the Author box before the date box, which takes typed
// text (bz), to the Status trigger. A loss the reader did not cause would leave the focus on the
// page (bp), so the landing tells the two apart.
test("a date picked in a date box is the reader's press, so the box's removal walks back to the nearest control before it that takes no typed text", async () => {
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
        expect(status).toHaveFocus();
        expect(author).not.toHaveFocus();
      });
    }
  }
});

// (bo) The page drops the date box at its `input`, which React's onChange answers, before any
// `change` reaches it: Firefox fires `input` alone as the digit that completes the date is typed,
// and Playwright's fill() - a value set without keys, as assistive technology sets it - fires
// `input`, then `change` on the box the page has already taken away, which no listener of the
// page hears. The date is the reader's all the same, so the box's removal is a loss they caused,
// and the focus walks back as in (bk): over the Author box, which takes typed text (bz), to the
// Status trigger. A loss the reader did not cause would leave the focus on the page (bp).
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
        expect(status).toHaveFocus();
        expect(author).not.toHaveFocus();
      });
    }
  }
});

/** The kinds of field whose value is typed text alone: an `<input>` of each text-entry type, a
 *  `<textarea>` and a content-editable element (`controlOf`). */
const TEXT_ENTRY_KINDS = [
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

// (bp) An `input` on a field that takes typed text is typing, and no press of the reader's
// (`isPress`), as a character typed there is none (bi): an `<input>` of type text, search, email,
// url, tel, password or number, a `<textarea>`, a content-editable element. So in the Filter panel
// of (bk), the field an update takes away at its `input`, before any `change`, is a loss the reader
// did not cause: the focus stays on the page, as it did before the page had a rescue, and nothing
// scrolls - where the date box of (bo), whose `input` is a pick, walks back to the Status trigger.
// With Chromium's `focusout` and with none.
test("an input on a field that takes typed text is no press, so the field taken away at it leaves the focus on the page", async () => {
  // Where the loss left the focus for each kind: on the page or not, and whether anything scrolled.
  const landed: Record<string, object> = {};
  const quiet: Record<string, object> = {};
  for (const kind of TEXT_ENTRY_KINDS) {
    for (const withFocusout of [true, false]) {
      const named = `${kind}, ${withFocusout ? "with" : "without"} a focusout`;
      const scrolls = spyOn(Element.prototype, "scrollIntoView");
      try {
        await withRescue(async () => {
          const field = controlOf(kind);
          focusInFilterPanel(field);
          field.dispatchEvent(new Event("input", { bubbles: true }));
          if (withFocusout) lose(field);
          else field.remove();
          await settled();
          landed[named] = {
            onPage: document.activeElement === document.body,
            scrolled: scrolls.mock.calls.length > 0,
          };
        });
      } finally {
        scrolls.mockRestore();
      }
      quiet[named] = { onPage: true, scrolled: false };
    }
  }
  expect(landed).toEqual(quiet);
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
  return focused === null ? null : nameOf(focused);
}

// (bl) Every control that fires its `change` at the reader's pick, as the date box of (bk) does,
// makes that change a press of theirs (`isPress`): a `<select>`, a checkbox, a radio, a file, range
// or color input, and each date and time box, which takes typed text all the same. So in the Filter
// panel of (bk), the control's removal in the task of that `change` alone - with no `input` before
// it, which makes a press of its own (bo) - is a loss the reader caused: the focus walks back past
// the Author box, which takes typed text (bz), to the Status trigger before it, as in (bk), where
// a loss the reader did not cause would leave it on the page (bm).
test("a change on a control that fires it at the reader's pick is their press, so the control's removal walks back to the nearest control before it that takes no typed text", async () => {
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
        control.dispatchEvent(new Event("change", { bubbles: true }));
        if (withFocusout) lose(control);
        else control.remove();
        await settled();
        landed[named] = focusedName();
      });
      walkedBack[named] = "Status";
    }
  }
  expect(landed).toEqual(walkedBack);
});

// (bm) A `change` on a field whose value is typed text alone - an `<input>` of type text, search,
// email, url, tel, password or number, a `<textarea>`, a content-editable element - is the field's
// commit on blur, which Chromium fires as an update takes the focused field away, and no press of
// the reader's (`isPress`), as in (bi). So in the Filter panel of (bk), the field's removal in the
// task of that `change` alone - with no `input` before it, whose rule (bp) reads - is a loss the
// reader did not cause: the focus stays on the page - not on the Author box, which takes typed
// text, nor on the Status trigger before it, in a panel that is no modal - and nothing scrolls.
test("a change on a field whose value is typed text is no press, so the field's removal takes the uncaused path", async () => {
  // Where the loss left the focus for each kind, with Chromium's `focusout` and with none: on the
  // page or not, and whether anything scrolled.
  const landed: Record<string, object> = {};
  const quiet: Record<string, object> = {};
  for (const kind of TEXT_ENTRY_KINDS) {
    for (const withFocusout of [true, false]) {
      const named = `${kind}, ${withFocusout ? "with" : "without"} a focusout`;
      const scrolls = spyOn(Element.prototype, "scrollIntoView");
      try {
        await withRescue(async () => {
          const field = controlOf(kind);
          focusInFilterPanel(field);
          // The field's commit on blur, which Chromium fires as the update takes it away.
          field.dispatchEvent(new Event("change", { bubbles: true }));
          if (withFocusout) lose(field);
          else field.remove();
          await settled();
          landed[named] = {
            onPage: document.activeElement === document.body,
            scrolled: scrolls.mock.calls.length > 0,
          };
        });
      } finally {
        scrolls.mockRestore();
      }
      quiet[named] = { onPage: true, scrolled: false };
    }
  }
  expect(landed).toEqual(quiet);
});

/** A line of buttons, one per label, that hands its own focus off (`useFocusPlace`) and arms
 *  that hand-off at its own presses (`armOnPress`), as a component does at its root: each button
 *  is counted in the line, and the line falls back to what `fallback` names. Drawn again with
 *  fewer labels, it takes the others away, as a press that takes its own control away does. */
function HandOffLine(props: {
  fallback: () => HTMLElement | null;
  labels: readonly string[];
}): ReactNode {
  const line = useRef<HTMLDivElement>(null);
  const noteFocus = useFocusPlace(
    (element) => (line.current?.contains(element) ? line.current : null),
    props.fallback
  );
  return createElement(
    "div",
    {
      onChangeCapture: armOnPress,
      onClickCapture: armOnPress,
      onFocus: noteFocus,
      onKeyDownCapture: armOnPress,
      ref: line,
    },
    props.labels.map((label) => createElement("button", { key: label, type: "button" }, label))
  );
}

/** One move a hand-off asks of the page: a focus, or a scroll into view, of the element it names
 *  (`nameOf`), with the options it asks with. */
interface Move {
  readonly move: "focus" | "scroll";
  readonly of: string | null;
  readonly options: unknown;
}

/** The name a case reads an element by: its `aria-label`, else its text. */
function nameOf(element: Element): string | null {
  return element.getAttribute("aria-label") ?? element.textContent;
}

/** Spies on every focus and every scroll into view the page is asked for, in order, until
 *  `restore`. A focus is made as asked; a scroll is only noted, as happy-dom scrolls nothing. */
function recordMoves(): { moves: Move[]; restore: () => void } {
  const moves: Move[] = [];
  const realFocus = HTMLElement.prototype.focus;
  const focus = spyOn(HTMLElement.prototype, "focus").mockImplementation(function (
    this: HTMLElement,
    options?: FocusOptions
  ) {
    moves.push({ move: "focus", of: nameOf(this), options });
    realFocus.call(this, options);
  });
  const scroll = spyOn(Element.prototype, "scrollIntoView").mockImplementation(function (
    this: Element,
    options?: boolean | ScrollIntoViewOptions
  ) {
    moves.push({ move: "scroll", of: nameOf(this), options });
  });
  return {
    moves,
    restore: () => {
      focus.mockRestore();
      scroll.mockRestore();
    },
  };
}

/** A press in a `HandOffLine` that takes its own button away. */
interface HandOffCase {
  /** The control the line falls back to, else none. */
  readonly fallback: HTMLElement | null;
  /** The line's buttons, by label, in order. */
  readonly labels: readonly string[];
  /** How far down the window each button named here is laid out, in px (`placeAt`). A button not
   *  named keeps happy-dom's own box, which reads as in sight. */
  readonly laidOut: Readonly<Record<string, number>>;
  /** The element the line is drawn at the end of. */
  readonly parent: Element;
  /** The button the press takes away. */
  readonly pressed: string;
}

/** Draws `handed`'s line, lays its buttons out, focuses the pressed one and clicks it, and takes
 *  it away, as the press's own commit does. Answers where the focus landed - the page, or the
 *  element by its name (`nameOf`) - and every move the hand-off asked of the page, in order
 *  (`recordMoves`). No rescue is mounted: the click arms the line's hand-off (`armOnPress`), so
 *  the loss reads as the reader's (`causedByReader`). */
function handOff(handed: HandOffCase): { landed: string | null; moves: Move[] } {
  const host = document.createElement("div");
  handed.parent.append(host);
  const fallback = () => handed.fallback;
  const view = render(createElement(HandOffLine, { fallback, labels: handed.labels }), {
    container: host,
  });
  try {
    for (const button of host.querySelectorAll("button")) {
      const top = handed.laidOut[button.textContent ?? ""];
      if (top !== undefined) placeAt(button, top);
    }
    const pressed = [...host.querySelectorAll("button")].find(
      (button) => button.textContent === handed.pressed
    );
    pressed?.focus();
    // The click and the commit after it come in one task, before the armed press ends; user-event
    // waits a task after every press.
    if (pressed !== undefined) fireEvent.click(pressed);
    const recorded = recordMoves();
    try {
      const left = handed.labels.filter((label) => label !== handed.pressed);
      view.rerender(createElement(HandOffLine, { fallback, labels: left }));
    } finally {
      recorded.restore();
    }
    const focused = document.activeElement;
    const landed = focused === null || focused === document.body ? "the page" : nameOf(focused);
    return { landed, moves: recorded.moves };
  } finally {
    disarm();
    view.unmount();
    host.remove();
  }
}

/** A `<select>` named Window at the end of `parent`, laid out `top` px down the window: the
 *  control a line falls back to, as the Delivery page's header falls back to its Window list's
 *  trigger. */
function windowSelectIn(parent: Element, top: number): HTMLElement {
  const select = controlOf("select");
  select.setAttribute("aria-label", "Window");
  parent.append(select);
  placeAt(select, top);
  return select;
}

/** Runs `body` with the page's main content in the document, named Main content, and takes it
 *  away after. */
function withMain<T>(body: (main: HTMLElement) => T): T {
  const { main } = mainOf();
  main.setAttribute("aria-label", "Main content");
  try {
    return body(main);
  } finally {
    main.remove();
    mainRef.current = null;
  }
}

/** The scroll `land` asks for: to the nearest edge, the least distance that shows the control
 *  whole, which moves nothing for a control wholly in sight. */
const NEAREST_EDGE = { block: "nearest", inline: "nearest" };

// (cd) A component's own hand-off (`useFocusPlace`) passes over every control out of the reader's
// sight, as the rescue's walk back does (cb): the control now at the lost one's place, under the
// window's bottom edge, and the one before the lost place, above the window's top edge. The
// nearest control in sight before them takes the focus. Where none of the line is in sight, the
// line's fallback, in sight, takes it.
test("a component's hand-off passes over its controls out of sight to the nearest in sight, else to its fallback", () => {
  // Where each hand-off landed, and where it belongs.
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  for (const oneShown of [true, false]) {
    const named = oneShown ? "One in sight" : "none of the line in sight";
    landed[named] = withMain(
      (main) =>
        handOff({
          fallback: windowSelectIn(main, 100),
          labels: ["One", "Two", "Three", "Four"],
          laidOut: { Four: 2_000, One: oneShown ? 300 : -500, Two: -100 },
          parent: main,
          pressed: "Three",
        }).landed
    );
    belongs[named] = oneShown ? "One" : "Window";
  }
  expect(landed).toEqual(belongs);
});

// (ce) Where nothing a component's hand-off can land on is in the reader's sight - the line's own
// control before the lost one and the line's fallback, each scrolled out above the window, as the
// Delivery page's Window list's trigger is for a reader scrolled below it - the focus goes, with
// no scroll, to the own stop of the region the line is in: the dialog or panel holding it, else the
// main content around it, else the page's main content for a line drawn outside it. With no main
// content in the page, nothing moves, and the focus stays on the page.
test("a component's hand-off with nothing in sight lands on its region's own stop, scrolling nothing", () => {
  // Where each hand-off landed and what it asked of the page, and where it belongs.
  const landed: Record<string, object> = {};
  const belongs: Record<string, object> = {};
  const ownStop = (name: string) => ({
    landed: name,
    moves: [{ move: "focus", of: name, options: { preventScroll: true } }],
  });
  /** The line's case, drawn at the end of `parent`, falling back to a select in `page`. */
  const lineIn = (parent: Element, page: Element) =>
    handOff({
      fallback: windowSelectIn(page, -100),
      labels: ["Before", "Clear"],
      laidOut: { Before: -200 },
      parent,
      pressed: "Clear",
    });
  landed["in a panel"] = withMain((main) => {
    const panel = document.createElement("div");
    panel.setAttribute("role", "dialog");
    panel.setAttribute("aria-label", "Filter");
    document.body.append(panel);
    try {
      return lineIn(panel, main);
    } finally {
      panel.remove();
    }
  });
  belongs["in a panel"] = ownStop("Filter");
  landed["in the main content"] = withMain((main) => lineIn(main, main));
  belongs["in the main content"] = ownStop("Main content");
  landed["outside the main content"] = withMain((main) => {
    const portal = document.createElement("div");
    document.body.append(portal);
    try {
      return lineIn(portal, main);
    } finally {
      portal.remove();
    }
  });
  belongs["outside the main content"] = ownStop("Main content");
  const page = document.createElement("div");
  document.body.append(page);
  try {
    landed["in a page with no main content"] = lineIn(page, page);
  } finally {
    page.remove();
  }
  belongs["in a page with no main content"] = { landed: "the page", moves: [] };
  expect(landed).toEqual(belongs);
});

// (cf) A control any part of which is in the reader's sight is one a component's hand-off lands
// on (`land`): focused with no scroll of the engine's own, then scrolled to its nearest edge, the
// least distance that shows it whole. The control before the lost one, cut by the window's top
// edge or by its bottom edge, takes the focus and is scrolled by that edge alone. The focus asks
// for `preventScroll`, so the engine's own rule for a focused control - Chromium's and Firefox's
// centring - moves nothing, and every engine shows the landing the same way.
test("a component's hand-off lands on a control partly in sight and scrolls it to its nearest edge", () => {
  // What each hand-off asked of the page, and what it belongs to ask.
  const moved: Record<string, Move[]> = {};
  const belongs: Record<string, Move[]> = {};
  for (const [edge, top] of [
    ["top", -20],
    ["bottom", window.innerHeight - 20],
  ] as const) {
    const named = `cut by the window's ${edge} edge`;
    moved[named] = withMain(
      (main) =>
        handOff({
          fallback: windowSelectIn(main, 100),
          labels: ["One", "Two"],
          laidOut: { One: top },
          parent: main,
          pressed: "Two",
        }).moves
    );
    belongs[named] = [
      { move: "focus", of: "One", options: { preventScroll: true } },
      { move: "scroll", of: "One", options: NEAREST_EDGE },
    ];
  }
  expect(moved).toEqual(belongs);
});

// (cg) A control wholly in the reader's sight takes the focus where a component's hand-off has
// always put it: the control before the lost one, else, where the line holds no other control, the
// line's fallback, as the Delivery page's Window list's trigger takes it from clear brush window.
// It asks for no scroll at all: the focus is taken with `preventScroll`, and a control wholly in
// sight needs no scroll to its nearest edge.
test("a component's hand-off lands on a control wholly in sight where it always did, and scrolls nothing", () => {
  // Where each hand-off landed and what it asked of the page, and where it belongs.
  const landed: Record<string, object> = {};
  const belongs: Record<string, object> = {};
  for (const [named, labels, target] of [
    ["the control before", ["One", "Two"], "One"],
    ["the fallback", ["Clear"], "Window"],
  ] as const) {
    landed[named] = withMain((main) =>
      handOff({
        fallback: windowSelectIn(main, 100),
        labels,
        laidOut: { One: 300 },
        parent: main,
        pressed: labels[labels.length - 1] ?? "",
      })
    );
    belongs[named] = {
      landed: target,
      moves: [{ move: "focus", of: target, options: { preventScroll: true } }],
    };
  }
  expect(landed).toEqual(belongs);
});

// A component's own hand-off lands on no field that takes typed text, its fallback included: the
// reader never chose it, and their next key would be typed into it. A line whose only control a
// press takes away falls back to a search box, as a selection header's Clear selection with no
// row matching and its select-all checkbox disabled has nothing else before it. The hand-off
// moves nothing then, and the page's rescue answers the loss by its walk back over the main
// content, to the Filter button before the line, which takes no text. With Chromium's `focusout`
// and without.
test("a component's hand-off passes over a fallback that takes typed text, and the rescue walks back to the control before the line", async () => {
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  for (const withFocusout of [true, false]) {
    const named = withFocusout ? "with a focusout" : "without a focusout";
    await withRescue(async () => {
      const { main } = mainOf();
      const search = controlOf("search");
      search.setAttribute("aria-label", "Search agents");
      main.append(search);
      buttonIn(main, "Filter");
      const host = document.createElement("div");
      main.append(host);
      const fallback = () => search;
      const view = render(createElement(HandOffLine, { fallback, labels: ["Clear selection"] }), {
        container: host,
      });
      try {
        const clear = host.querySelector("button") as HTMLButtonElement;
        clear.focus();
        enterPress(clear);
        const goes = () => view.rerender(createElement(HandOffLine, { fallback, labels: [] }));
        if (withFocusout) lose(clear, goes);
        else goes();
        await settled();
        landed[named] = focusedName();
      } finally {
        view.unmount();
      }
    });
    belongs[named] = "Filter";
  }
  expect(landed).toEqual(belongs);
});

/** The Inbox's Decline shape: a list of rows, each an `<li>` holding an ask card, with the first
 *  card's **Decline to answer** opening a modal dialog at the end of the body, as `useDialog`
 *  portals one, and the focus moved into the dialog's Cancel from that trigger, so the rescue
 *  notes the trigger as the dialog's opener. */
function declineDialogIn(main: HTMLElement): {
  cancel: HTMLButtonElement;
  decline: HTMLButtonElement;
  dialog: HTMLElement;
  rows: HTMLLIElement[];
} {
  const list = document.createElement("ul");
  main.append(list);
  const rows = ["the lease format", "the rollout order"].map((question) => {
    const row = document.createElement("li");
    const card = document.createElement("article");
    card.setAttribute("aria-label", `Which ${question}?`);
    row.append(card);
    list.append(row);
    return { card, row };
  });
  const [first, second] = rows as [{ card: HTMLElement; row: HTMLLIElement }, (typeof rows)[0]];
  buttonIn(first.card, "Reaches 1");
  const trigger = buttonIn(first.card, "Decline to answer");
  buttonIn(second.card, "Ship");
  const { buttons, dialog } = modalOf("Cancel", "Decline the question");
  const [cancel, decline] = buttons as [HTMLButtonElement, HTMLButtonElement];
  trigger.focus();
  enterPress(trigger);
  cancel.focus();
  return { cancel, decline, dialog, rows: rows.map(({ row }) => row) };
}

// (cv) A Tab inside the region a press was made in, while that press's own write is out, leaves
// the press live (`notePress`): the region's later going is the press's own doing. The Inbox's
// Decline to answer opens its dialog, the reader presses the dialog's own Decline, and that
// decline's write is out with the button held. The reader Tabs to Cancel, inside the dialog, and
// then the decline lands: the dialog goes, and the row the question was asked in goes with it.
// That loss is the reader's, so the focus is handed on from the place the dialog was opened from
// (`handOn`'s opener), and lands on the next question's Ship, the first control of the row that
// moved up into the place of the question's row (`controlsOfRowMovedUp`). Where the Tab ended the
// press, the loss read as one the reader did not cause, and the focus stayed on the page
// (measured at 390x664 in three engines). With Chromium's `focusout` for the control the dialog
// took away, and with none.
test("a Tab inside the region a press was made in leaves the press the cause of that region's going", async () => {
  for (const withFocusout of [true, false]) {
    const writes = signedInWrites();
    await withRescue(async () => {
      const { main } = mainOf("Before");
      const { cancel, decline, dialog, rows } = declineDialogIn(main);
      decline.focus();
      enterPress(decline);
      const write = writeOf(writes);
      // Held while the decline is out, never disabled, so it keeps its Tab stop.
      decline.setAttribute("aria-disabled", "true");
      await settled();
      // The reader's Tab, and the engine's move of the focus to the dialog's Cancel.
      decline.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Tab" }));
      cancel.focus();
      cancel.dispatchEvent(new KeyboardEvent("keyup", { bubbles: true, key: "Tab" }));
      expect(cancel).toHaveFocus();
      // The decline is still out several frames after the Tab, as the reader's wait is.
      for (const _ of [0, 1, 2]) await settled();
      await write.land();
      // The commit the decline's answer makes: the dialog goes, and the question's row with it.
      const goes = () => {
        dialog.remove();
        rows[0]?.remove();
      };
      if (withFocusout) lose(cancel, goes);
      else goes();
      await settled();
      expect(document.activeElement).not.toBe(document.body);
      expect(focusedName()).toBe("Ship");
    }, writes);
  }
});

// The same Decline at a phone's width, with the reader scrolled down to the question: the reader
// presses the dialog's own Decline, and once the decline lands the dialog goes with the
// question's row. The row that moves up into that row's place, and every control before it,
// stand above the window then. The loss is answered as the opener's own (`handOn`): no control
// in sight takes the focus, so the own stop of the main content takes it, with no scroll, and the
// focus is never left on the page's body. With Chromium's `focusout` for the control the dialog
// took away, and with none.
test("a dialog gone with its opener's row, with nothing in sight at the opener's place or before it, hands the focus to the main content's own stop", async () => {
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  for (const withFocusout of [true, false]) {
    const named = withFocusout ? "with a focusout" : "without a focusout";
    const writes = signedInWrites();
    await scrollingNothing(async () => {
      await withRescue(async () => {
        const { buttons, main } = mainOf("Before");
        placeAt(buttons[0] as HTMLElement, -2_000);
        const { decline, dialog, rows } = declineDialogIn(main);
        for (const control of rows[1]?.querySelectorAll("button") ?? []) placeAt(control, -300);
        decline.focus();
        enterPress(decline);
        const write = writeOf(writes);
        decline.setAttribute("aria-disabled", "true");
        await settled();
        await write.land();
        // The commit the decline's answer makes: the dialog goes, and the question's row with it.
        const goes = () => {
          dialog.remove();
          rows[0]?.remove();
        };
        if (withFocusout) lose(decline, goes);
        else goes();
        await settled();
        landed[named] =
          document.activeElement === main
            ? "the main content's own stop"
            : document.activeElement === document.body
              ? "the page"
              : focusedName();
      }, writes);
    });
    belongs[named] = "the main content's own stop";
  }
  expect(landed).toEqual(belongs);
});

// (cw) The Tab still ends the rescue's wait for the control the press disabled, whoever caused
// the loss (`readerMoved`, `noteKey`): the press stays live only to class a later loss in its
// region as caused. A page control disabled under its own press drops the focus to the page, the
// reader Tabs to the control before it, and the write is then refused and the control enabled
// again: the focus stays where the reader put it. The wait's own settle asks the same twice over,
// since it moves nothing while the focus is not lost (`focusLost`). The refused write ends the
// press the Tab left live, so a control taken away after that is a loss the reader did not
// cause, and the focus stays on the page.
test("a Tab inside the press's region still ends the give-back to a control the write's refusal enables again", async () => {
  const writes = signedInWrites();
  await withRescue(async () => {
    const [before, saving] = mainOf("Before", "Save", "After").buttons;
    const save = saving as HTMLButtonElement;
    save.focus();
    enterPress(save);
    const write = writeOf(writes);
    // Chromium fires the `focusout` just after the disable; happy-dom blurs no disabled button.
    save.blur();
    save.disabled = true;
    await settled();
    expect(document.activeElement).toBe(document.body);
    // The reader's Tab from the page to the control before the one that was disabled.
    document.body.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Tab" }));
    before?.focus();
    document.body.dispatchEvent(new KeyboardEvent("keyup", { bubbles: true, key: "Tab" }));
    await write.refuse();
    save.disabled = false;
    await settled();
    await settled();
    expect(before).toHaveFocus();
    // A live update takes the control the reader Tabbed to away, after their press is answered.
    before?.remove();
    await settled();
    expect(document.activeElement).toBe(document.body);
  }, writes);
});

// (cx) The control now at the lost control's place is one that moved into that place from the
// lost control's own row, card or toolbar (`atPlaceOf`). A control that only takes its index in
// the region's Tab order, from another row or item, stands at no place of its: the walk back
// passes it and goes on to the nearest control before the place, which is the lost control's own
// card. An Inbox card lists Unfollow and Unsubscribe for the question's recipients, and the next
// question's row draws its own Select checkbox after them. The reader's Unsubscribe takes its own
// chip away: the focus lands on Unfollow, in the card, and never on the next question's checkbox,
// which at the head took Unsubscribe's index (measured at 390x664 in three engines, INFRA-3135
// round 24). With Chromium's `focusout` and without.
test("a control lost from a card lands on the card's control before the place, not on the next row's control at its index", async () => {
  for (const withFocusout of [true, false]) {
    await withRescue(async () => {
      const { main } = mainOf("Before");
      const list = document.createElement("ul");
      main.append(list);
      const row = document.createElement("li");
      const card = document.createElement("article");
      row.append(card);
      list.append(row);
      const recipients = document.createElement("ul");
      card.append(recipients);
      const chips = ["Unfollow", "Unsubscribe"].map((label) => {
        const chip = document.createElement("li");
        recipients.append(chip);
        return { button: buttonIn(chip, label), chip };
      });
      const next = document.createElement("li");
      list.append(next);
      const mark = document.createElement("input");
      mark.type = "checkbox";
      mark.setAttribute("aria-label", "Select CORE-3");
      next.append(mark);
      const [unfollow, unsubscribe] = chips as [(typeof chips)[0], (typeof chips)[0]];
      unsubscribe.button.focus();
      enterPress(unsubscribe.button);
      // The write lands and the subscriber's chip goes with it.
      if (withFocusout) lose(unsubscribe.button, () => unsubscribe.chip.remove());
      else unsubscribe.chip.remove();
      await settled();
      expect(focusedName()).toBe("Unfollow");
      expect(unfollow.button).toHaveFocus();
    });
  }
});

// (cy) A landing the reader caused is focused with no scroll of the engine's own and is shown at
// its nearest edge where the reader can see a part of it but not the whole: the least distance
// that shows it whole (`land`). One wholly in sight is not scrolled at all. Without
// `preventScroll` the engine's own rule decides: Chromium and Firefox centre a control that is
// not wholly in view, and Firefox moved a landing half cut by a dialog's top edge 405 px
// (INFRA-3135, test handoff v21).
test("a caused landing partly in sight is shown at its nearest edge and no further, and one wholly in sight scrolls nothing", async () => {
  // What the rescue asked of the page for each landing, and what it belongs to ask.
  const moved: Record<string, Move[]> = {};
  const belongs: Record<string, Move[]> = {};
  for (const [named, top] of [
    ["partly in sight", -20],
    ["wholly in sight", 300],
  ] as const) {
    const recorded = recordMoves();
    try {
      await withRescue(async () => {
        const [first, second] = mainOf("One", "Two").buttons;
        placeAt(first as HTMLElement, top);
        second?.focus();
        enterPress(second as HTMLElement);
        lose(second as HTMLElement);
        await settled();
        expect(first).toHaveFocus();
      });
    } finally {
      recorded.restore();
    }
    moved[named] = recorded.moves.filter((move) => move.of === "One");
    belongs[named] = [
      { move: "focus", of: "One", options: { preventScroll: true } },
      ...(top < 0 ? [{ move: "scroll" as const, of: "One", options: NEAREST_EDGE }] : []),
    ];
  }
  expect(moved).toEqual(belongs);
});

/** An Inbox list in `main`, built by hand: a question's row holding its card, whose recipients are
 *  drawn as one list of chips per group - each chip the session's name link, then its remove
 *  button - for a follower's Unfollow and a subscriber's Unsubscribe, and the next question's row
 *  with its Select checkbox. Answers the subscribers' list and its Unsubscribe. */
function recipientsCard(main: HTMLElement): { chips: HTMLElement; remove: HTMLButtonElement } {
  const list = document.createElement("ul");
  main.append(list);
  const row = document.createElement("li");
  const card = document.createElement("article");
  row.append(card);
  list.append(row);
  // A list of one chip: the session's name link, then its remove button.
  const chipList = (name: string, label: string) => {
    const chips = document.createElement("ul");
    card.append(chips);
    const chip = document.createElement("li");
    chips.append(chip);
    const link = document.createElement("a");
    link.href = `/agents/${name}`;
    link.textContent = name;
    chip.append(link);
    return { chips, remove: buttonIn(chip, label) };
  };
  chipList("Replier", "Unfollow");
  const subscribers = chipList("Watcher", "Unsubscribe");
  const next = document.createElement("li");
  list.append(next);
  const mark = document.createElement("input");
  mark.type = "checkbox";
  mark.setAttribute("aria-label", "Select CORE-3");
  next.append(mark);
  return subscribers;
}

// (cz) The place of a control a commit takes away is counted off the spot it went from, in
// Chromium as in Firefox and WebKit. An Inbox card's subscriber chip draws the subscriber's name
// link before its Unsubscribe, and the chip's list goes with both once the write lands. Chromium
// fires the Unsubscribe's `focusout` while the link still stands before it: counted there, the
// place lies one past the place the list left, and the walk back starts at the next question's
// Select checkbox, which now stands after that place (measured at 390x664 in Chromium alone).
// Counted off the spot the list went from, the control now at the place is that checkbox, of
// another row, so the walk goes back to the card's Unfollow (`atPlaceOf`). With Chromium's
// `focusout` and without.
test("a control taken away with a control before it in its chip lands on the card's control before the place, not on the next row's control", async () => {
  const landed: Record<string, string | null> = {};
  for (const withFocusout of [true, false]) {
    await withRescue(async () => {
      const subscribers = recipientsCard(mainOf("Before").main);
      subscribers.remove.focus();
      enterPress(subscribers.remove);
      // The write lands, and the subscribers' list goes with the name link and the Unsubscribe.
      if (withFocusout) lose(subscribers.remove, () => subscribers.chips.remove());
      else subscribers.chips.remove();
      await settled();
      landed[withFocusout ? "with Chromium's focusout" : "without"] = focusedName();
    });
  }
  expect(landed).toEqual({ "with Chromium's focusout": "Unfollow", without: "Unfollow" });
});

// (da) The same place for a control disabled under the focus and then taken away. Its loss is
// noted at a `focusout` while it still stands in the page: every engine's at the disable, or
// Chromium's at the removal where that comes before the disable's own. Counted there, the place
// takes in the name link that goes with it, and the walk back starts at the next question's
// Select checkbox. Counted again off the removal that takes the control while the loss's check or
// its wait on the held control is out, the focus lands on the card's Unfollow, as for a control
// taken away with no `focusout`.
test("a control disabled under the focus and then taken away with a control before it lands on the card's control before the place", async () => {
  const ways = [
    "focusout at the disable",
    "Chromium's focusout at the removal",
    "no focusout",
  ] as const;
  const landed: Record<string, string | null> = {};
  for (const way of ways) {
    await withRescue(async () => {
      const subscribers = recipientsCard(mainOf("Before").main);
      const unsubscribe = subscribers.remove;
      unsubscribe.focus();
      enterPress(unsubscribe);
      if (way === "focusout at the disable") {
        // happy-dom blurs no disabled button, so the `focusout` comes just before the disable, as
        // in (c). The rescue then waits on the held control, until the write's answer takes the
        // subscribers' list away.
        unsubscribe.blur();
        unsubscribe.disabled = true;
        await settled();
        subscribers.chips.remove();
        await settled();
      } else {
        unsubscribe.disabled = true;
        if (way === "no focusout") subscribers.chips.remove();
        else lose(unsubscribe, () => subscribers.chips.remove());
      }
      await settled();
      landed[way] = focusedName();
    });
  }
  expect(landed).toEqual(Object.fromEntries(ways.map((way) => [way, "Unfollow"])));
});

/** The card of `recipientsCard`, drawn by React inside a list that hands its own focus off
 *  (`useFocusPlace`), the list its container. Drawn again without `subscribers`, it takes the
 *  subscribers' list away with the name link and the Unsubscribe. */
function RecipientsList(props: { readonly subscribers: boolean }): ReactNode {
  const list = useRef<HTMLUListElement>(null);
  const noteFocus = useFocusPlace(
    (element) => (list.current?.contains(element) ? list.current : null),
    () => null
  );
  // A list of one chip: the session's name link, then its remove button.
  const chipList = (name: string, label: string) =>
    createElement(
      "ul",
      { key: name },
      createElement(
        "li",
        null,
        createElement("a", { href: `/agents/${name}` }, name),
        createElement("button", { type: "button" }, label)
      )
    );
  return createElement(
    "ul",
    { onFocus: noteFocus, ref: list },
    createElement(
      "li",
      null,
      createElement(
        "article",
        null,
        chipList("Replier", "Unfollow"),
        props.subscribers ? chipList("Watcher", "Unsubscribe") : null
      )
    ),
    createElement(
      "li",
      null,
      createElement("input", { "aria-label": "Select CORE-3", type: "checkbox" })
    )
  );
}

// (db) A component's own hand-off counts the same place off the spot the control went from in
// its container (`useFocusPlace`). Counted while the Unsubscribe still stood there, the place takes
// in the name link that went with it, and the walk back starts at the next question's Select
// checkbox. With the page's rescue mounted, and with Chromium's `focusout` and without: the
// component's hand-off answers in the commit that takes the list away, before the rescue's check.
test("a component's hand-off for a control taken away with a control before it lands on the card's control before the place", async () => {
  const landed: Record<string, string | null> = {};
  for (const withFocusout of [true, false]) {
    await withRescue(async () => {
      const { main } = mainOf();
      const view = render(createElement(RecipientsList, { subscribers: true }), {
        container: main,
      });
      try {
        const unsubscribe = view.getByRole("button", { name: "Unsubscribe" });
        unsubscribe.focus();
        enterPress(unsubscribe);
        // The write lands, and the commit takes the subscribers' list away.
        const answer = () => view.rerender(createElement(RecipientsList, { subscribers: false }));
        if (withFocusout) lose(unsubscribe, answer);
        else answer();
        await settled();
        landed[withFocusout ? "with Chromium's focusout" : "without"] = focusedName();
      } finally {
        view.unmount();
      }
    });
  }
  expect(landed).toEqual({ "with Chromium's focusout": "Unfollow", without: "Unfollow" });
});

// (dc) The same place for a control hidden under the focus with the name link before it, its list
// hidden rather than taken away. Its loss can be noted at a `focusout` while the list still
// stands: one at a `blur()` before the hide, or at a disable before the list is hidden while the
// rescue waits on the held control. Counted there, the place takes in the name link hidden with
// it, and the walk back starts at the next question's Select checkbox. Counted where the control
// still stands as the loss is answered, the focus lands on the card's Unfollow, as it does for a
// `focusout` that comes once the list is hidden, as an engine's focus fixup fires it.
test("a control hidden under the focus with a control before it lands on the card's control before the place", async () => {
  const ways = [
    "focusout before the hide",
    "focusout at the disable, hidden in the wait",
    "focusout after the hide",
  ] as const;
  const landed: Record<string, string | null> = {};
  for (const way of ways) {
    await withRescue(async () => {
      const subscribers = recipientsCard(mainOf("Before").main);
      const unsubscribe = subscribers.remove;
      unsubscribe.focus();
      enterPress(unsubscribe);
      if (way === "focusout before the hide") {
        unsubscribe.blur();
        subscribers.chips.hidden = true;
      } else if (way === "focusout after the hide") {
        subscribers.chips.hidden = true;
        unsubscribe.blur();
      } else {
        // happy-dom blurs no disabled button, so the `focusout` comes just before the disable, as
        // in (c). The rescue then waits on the held control, until the write's answer hides the
        // subscribers' list.
        unsubscribe.blur();
        unsubscribe.disabled = true;
        await settled();
        subscribers.chips.hidden = true;
        await settled();
      }
      await settled();
      landed[way] = focusedName();
    });
  }
  expect(landed).toEqual(Object.fromEntries(ways.map((way) => [way, "Unfollow"])));
});

// (dd) The same place for the control a dialog was opened from. The reader opens a confirm
// dialog from the subscriber's Unsubscribe and presses its Confirm, which takes the dialog away,
// and the same commit takes the subscribers' list away with the name link and the Unsubscribe,
// or hides it. The focus goes back to where the dialog was opened from (`handOn`). The opener's
// place was counted as the focus left it for the dialog, with the name link standing before it,
// so the walk back would start at the next question's Select checkbox. Counted off the spot the
// opener went from, or where it still stands while hidden (`placeLeft`), the focus lands on the
// card's Unfollow. With Chromium's `focusout` for the dialog's Confirm and without.
test("a walk back from a dialog's opener that went or was hidden with a control before it lands on the card's control before the place", async () => {
  const landed: Record<string, string | null> = {};
  const belongs: Record<string, string> = {};
  for (const way of ["taken away", "hidden"] as const) {
    for (const withFocusout of [true, false]) {
      const named = `${way}, ${withFocusout ? "with Chromium's focusout" : "without"}`;
      await withRescue(async () => {
        const subscribers = recipientsCard(mainOf("Before").main);
        const { buttons, dialog } = modalOf("Confirm");
        const confirm = buttons[0] as HTMLButtonElement;
        subscribers.remove.focus();
        confirm.focus();
        enterPress(confirm);
        const answer = () => {
          dialog.remove();
          if (way === "hidden") subscribers.chips.hidden = true;
          else subscribers.chips.remove();
        };
        if (withFocusout) lose(confirm, answer);
        else answer();
        await settled();
        landed[named] = focusedName();
      });
      belongs[named] = "Unfollow";
    }
  }
  expect(landed).toEqual(belongs);
});

// The armed hand-off. Where no rescue is mounted, a component's own hand-off (`useFocusPlace`,
// `SelectionBar`) answers a loss as the reader's only while a press that armed it is still live
// (`armHandOff`, armed at a component's root by `armOnPress`), and leaves every other loss as the
// page did before it had any hand-off: the focus on the body, and nothing scrolled. No case below
// mounts the rescue. Each case is one row of the state table of INFRA-3135's t31: the state the
// press left, the event, and where the focus goes.

/** What an `ArmedLine` draws: one button per label, `held` ones held while their write is out
 *  (`aria-disabled`, as `heldPress` holds one), `disabled` ones disabled, after a disabled
 *  checkbox where `box` asks for one - the Agents page's select-all while no row matches - and
 *  the line itself only while `shown` (by default). */
interface ArmedLineProps {
  readonly box?: boolean;
  readonly disabled?: readonly string[];
  readonly held?: readonly string[];
  readonly labels: readonly string[];
  readonly shown?: boolean;
}

/** A component that hands its own focus off (`useFocusPlace`) and arms that hand-off at its own
 *  presses (`armOnPress`), as every component with a hand-off does at its root: a line of
 *  buttons, the container they are counted in, inside a root that stays when the line goes. */
function ArmedLine(props: ArmedLineProps): ReactNode {
  const line = useRef<HTMLDivElement>(null);
  const noteFocus = useFocusPlace(
    (element) => (line.current?.contains(element) ? line.current : null),
    () => null
  );
  const { box = false, disabled = [], held = [], labels, shown = true } = props;
  const buttons = labels.map((label) =>
    createElement(
      "button",
      {
        "aria-disabled": held.includes(label) || undefined,
        disabled: disabled.includes(label),
        key: label,
        type: "button",
      },
      label
    )
  );
  return createElement(
    "div",
    {
      onChangeCapture: armOnPress,
      onClickCapture: armOnPress,
      onFocus: noteFocus,
      onKeyDownCapture: armOnPress,
    },
    shown
      ? createElement(
          "div",
          { ref: line },
          box
            ? createElement("input", {
                "aria-label": "Select all",
                disabled: true,
                type: "checkbox",
              })
            : null,
          buttons
        )
      : null
  );
}

/** An `ArmedLine` drawn at the end of `parent`: its button named `label`, a redraw with new
 *  props - the commit a press, a write's answer or an update makes - and its removal. */
function armedLineIn(
  parent: Element,
  props: ArmedLineProps
): {
  button: (label: string) => HTMLButtonElement;
  redraw: (next: ArmedLineProps) => void;
  remove: () => void;
} {
  const host = document.createElement("div");
  parent.append(host);
  const view = render(createElement(ArmedLine, props), { container: host });
  return {
    button: (label) => {
      const found = [...host.querySelectorAll("button")].find(
        (button) => button.textContent === label
      );
      if (found === undefined) throw new Error(`no button ${label} is drawn`);
      return found;
    },
    redraw: (next) => view.rerender(createElement(ArmedLine, next)),
    remove: () => {
      view.unmount();
      host.remove();
    },
  };
}

/** The reader's click on `control`, through the event path a hand-off's root arms at
 *  (`armOnPress`): the focus a pointer's press gives a button in Chromium and Firefox, then the
 *  click itself. */
function clickPress(control: HTMLElement): void {
  control.focus();
  fireEvent.click(control);
}

/** Ends a press still armed as a case ends, as the reader's next pointer press on the page does,
 *  so that none outlives its case into the next one: a case that installs the fake clock would
 *  run that press's end on it, which is not that case's to move. */
function disarm(): void {
  document.body.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
}

/** Runs `body` with the page's main content in the document, holding one button per label, and
 *  takes it away after, with any press still armed (`disarm`). */
async function inMain<T>(
  labels: readonly string[],
  body: (main: HTMLElement) => Promise<T>
): Promise<T> {
  const { main } = mainOf(...labels);
  try {
    return await body(main);
  } finally {
    disarm();
    main.remove();
    mainRef.current = null;
  }
}

/** Where the focus is: the page, or the element by its name (`nameOf`). */
function landing(): string | null {
  const focused = document.activeElement;
  return focused === null || focused === document.body ? "the page" : nameOf(focused);
}

/** The labels every line below starts with. */
const LINE = ["One", "Two", "Three"] as const;

// (de) Armed by a control's own press, the commit of that press takes the focused control away:
// the focus goes to the control now at its place, which is in sight, and nothing scrolls.
test("an armed press whose commit takes its control away hands the focus to the control now at its place, scrolling nothing", async () => {
  const moved = await inMain([], async (main) => {
    const line = armedLineIn(main, { labels: LINE });
    try {
      clickPress(line.button("Two"));
      const recorded = recordMoves();
      try {
        line.redraw({ labels: ["One", "Three"] });
      } finally {
        recorded.restore();
      }
      return { landed: landing(), moves: recorded.moves };
    } finally {
      line.remove();
    }
  });
  expect(moved).toEqual({
    landed: "Three",
    moves: [{ move: "focus", of: "Three", options: { preventScroll: true } }],
  });
});

// (df) Armed, and the press's commit takes its whole container away - Clear filters with the
// chips line, Clear selection with the selection it empties - or leaves it no control the focus
// can reach - the Agents page's header, whose select-all is disabled while no row matches. The
// component's hand-off walks back itself over the region its container was in, the main content,
// from the place the control left: past the search box, which takes typed text, to the nearest
// control before it in sight, the Filter button.
test("an armed press whose commit leaves its container nothing walks back over the main content to the nearest control in sight", async () => {
  const landed: Record<string, string | null> = {};
  for (const way of ["the line goes", "the line keeps only a disabled box"] as const) {
    landed[way] = await inMain(["Filter"], async (main) => {
      const search = controlOf("search");
      search.setAttribute("aria-label", "Search agents");
      main.append(search);
      const kept = way !== "the line goes";
      const line = armedLineIn(main, { box: kept, labels: ["Clear selection"] });
      try {
        clickPress(line.button("Clear selection"));
        line.redraw(kept ? { box: true, labels: [] } : { labels: [], shown: false });
        return landing();
      } finally {
        line.remove();
      }
    });
  }
  expect(landed).toEqual({
    "the line goes": "Filter",
    "the line keeps only a disabled box": "Filter",
  });
});

// (dg) Armed, and the press's write held by its hold: the control held while the write is out
// (`heldPress`), or a hold on what answers it (`beginAnswerHold`). The write lands several frames
// later and its answer takes the control away: the focus goes to the control now at its place.
test("an armed press whose write is held hands the focus on when the write lands and takes its control away", async () => {
  const landed: Record<string, string | null> = {};
  for (const way of ["its control held", "a hold on what answers it"] as const) {
    landed[way] = await inMain([], async (main) => {
      const line = armedLineIn(main, { labels: LINE });
      try {
        clickPress(line.button("Two"));
        let answered: () => void = () => undefined;
        if (way === "its control held") line.redraw({ held: ["Two"], labels: LINE });
        else answered = beginAnswerHold();
        for (const _ of [0, 1, 2]) await settled();
        line.redraw({ labels: ["One", "Three"] });
        answered();
        return landing();
      } finally {
        line.remove();
      }
    });
  }
  expect(landed).toEqual({ "a hold on what answers it": "Three", "its control held": "Three" });
});

// (dh) Armed, offline, and the press's write held by its hold: the write landed, and its control
// is held through the re-read after it (`useReadBound`), which a paused fetch never answers. The
// hold ends at the request deadline with the network's reason, and the commit that lets the
// control go takes it away: the focus goes to the control now at its place, in that same render.
test("an armed press held offline hands the focus on in the render its hold's end at the request deadline takes its control away", async () => {
  jest.useFakeTimers();
  const online = onlineManager.isOnline();
  const bound = renderHook(() => useReadBound());
  try {
    act(() => onlineManager.setOnline(false));
    const landed = await inMain([], async (main) => {
      const line = armedLineIn(main, { labels: LINE });
      try {
        clickPress(line.button("Two"));
        line.redraw({ held: ["Two"], labels: LINE });
        const held = bound.result.current[1](new Promise<never>(() => undefined));
        await act(async () => {
          jest.advanceTimersByTime(REQUEST_DEADLINE_MS);
          await held;
        });
        line.redraw({ labels: ["One", "Three"] });
        return { landed: landing(), reason: bound.result.current[0] };
      } finally {
        line.remove();
      }
    });
    expect(landed).toEqual({ landed: "Three", reason: NETWORK_ERROR_MESSAGE });
  } finally {
    act(() => onlineManager.setOnline(online));
    bound.unmount();
    realClock();
  }
});

// (di) Armed, and the reader presses Tab inside the region the press was made in while its write
// is out: a panel's Rename, held, and the reader's Tab to the control after it in the panel. The
// press stays armed, so the write's answer, which lets Rename go and takes that control away, is
// still the press's doing: the focus goes to the control now at its place.
test("a Tab inside the region of an armed press whose write is out keeps it armed, and the loss hands the focus on", async () => {
  const panel = document.createElement("div");
  panel.setAttribute("aria-label", "Session groups");
  panel.setAttribute("role", "dialog");
  document.body.append(panel);
  const labels = ["One", "Two", "Three", "Four"];
  const line = armedLineIn(panel, { labels });
  try {
    const two = line.button("Two");
    const three = line.button("Three");
    clickPress(two);
    line.redraw({ held: ["Two"], labels });
    // The reader's Tab, and the engine's move of the focus to Three.
    two.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Tab" }));
    three.focus();
    three.dispatchEvent(new KeyboardEvent("keyup", { bubbles: true, key: "Tab" }));
    for (const _ of [0, 1, 2]) await settled();
    line.redraw({ labels: ["One", "Two", "Four"] });
    expect(landing()).toBe("Four");
  } finally {
    line.remove();
    disarm();
    panel.remove();
  }
});

/** A list that hands its own focus off and arms the hand-off at its own presses, as `ArmedLine`
 *  does: one row per name, each an `<li>` holding its Answer button and, after it, a panel the
 *  row opened in its own place (`role="dialog"`, a region of its own), whose Later `held` holds
 *  while its write is out. */
function ArmedRows(props: {
  readonly held: boolean;
  readonly names: readonly string[];
}): ReactNode {
  const list = useRef<HTMLUListElement>(null);
  const noteFocus = useFocusPlace(
    (element) => (list.current?.contains(element) ? list.current : null),
    () => null
  );
  return createElement(
    "ul",
    {
      onChangeCapture: armOnPress,
      onClickCapture: armOnPress,
      onFocus: noteFocus,
      onKeyDownCapture: armOnPress,
      ref: list,
    },
    props.names.map((name) =>
      createElement(
        "li",
        { key: name },
        createElement("button", { type: "button" }, `Answer ${name}`),
        createElement(
          "div",
          { "aria-label": `Snooze ${name}`, role: "dialog" },
          createElement(
            "button",
            { "aria-disabled": props.held || undefined, type: "button" },
            `Later ${name}`
          )
        )
      )
    )
  );
}

// (dj) Armed, and the reader acts again before the loss: another key, a pointer press, or a Tab
// out of the region the press was made in. The reader presses Later in a panel opened in the
// first row, whose write holds it, and the focus moves to the row's Answer, outside that panel.
// Then the write lands and the row goes with the Answer. That loss is the press's only while the
// press is armed - the row held the pressed Later - and then the walk back lands on Before, the
// control before the row. Each act of the reader's disarms it, and the loss is answered as main
// does: the focus stays on the body, and nothing scrolls.
test("another key, a pointer press or a Tab out of the press's region disarms it, and the loss leaves the focus on the page", async () => {
  const acts: Readonly<Record<string, (later: HTMLElement, answer: HTMLElement) => void>> = {
    "a Tab out of the panel": (later, answer) => {
      later.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Tab" }));
      answer.focus();
      answer.dispatchEvent(new KeyboardEvent("keyup", { bubbles: true, key: "Tab" }));
    },
    "another key": (_later, answer) => {
      answer.focus();
      answer.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "x" }));
      answer.dispatchEvent(new KeyboardEvent("keyup", { bubbles: true, key: "x" }));
    },
    "a pointer press on the page": (_later, answer) => {
      answer.focus();
      document.body.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    },
    "nothing after the press": (_later, answer) => answer.focus(),
  };
  const landed: Record<string, string | null> = {};
  const scrolls = spyOn(Element.prototype, "scrollIntoView");
  try {
    for (const [named, actAgain] of Object.entries(acts)) {
      landed[named] = await inMain(["Before"], async (main) => {
        const host = document.createElement("div");
        main.append(host);
        const names = ["One", "Two"];
        const view = render(createElement(ArmedRows, { held: false, names }), { container: host });
        try {
          const later = view.getByRole("button", { name: "Later One" });
          clickPress(later);
          view.rerender(createElement(ArmedRows, { held: true, names }));
          actAgain(later, view.getByRole("button", { name: "Answer One" }));
          await settled();
          view.rerender(createElement(ArmedRows, { held: false, names: ["Two"] }));
          return landing();
        } finally {
          view.unmount();
          host.remove();
        }
      });
    }
    expect(scrolls).not.toHaveBeenCalled();
  } finally {
    scrolls.mockRestore();
  }
  expect(landed).toEqual({
    "a Tab out of the panel": "the page",
    "a pointer press on the page": "the page",
    "another key": "the page",
    "nothing after the press": "Before",
  });
});

// (dk) Armed by a press that starts no write and holds nothing, a frame and a task pass after its
// commit with no loss: the press is disarmed, and an update that takes the control away after
// that is no doing of the reader's. The focus stays on the body, and nothing scrolls.
test("an armed press that holds nothing is disarmed a frame and a task on, and a later loss leaves the focus on the page", async () => {
  await scrollingNothing(async () => {
    await inMain([], async (main) => {
      const line = armedLineIn(main, { labels: LINE });
      try {
        clickPress(line.button("Two"));
        await settled();
        line.redraw({ labels: ["One", "Three"] });
        expect(document.activeElement).toBe(document.body);
      } finally {
        line.remove();
      }
    });
  });
});

// (dl) Armed by a press that starts a write, whose control is held through the re-read after the
// write lands (`useReadBound`): a loss inside that read is still the press's, and the focus goes
// to the control now at its place; once the read settles, the hold ends, and a frame and a task
// pass with no loss, the press is disarmed, and a loss after that leaves the focus on the page.
test("an armed press held through its write's re-read causes a loss inside that read, and none a frame and a task after the hold ends", async () => {
  const landed: Record<string, string | null> = {};
  const bound = renderHook(() => useReadBound());
  try {
    for (const when of ["inside the read", "after the read's hold ended"] as const) {
      landed[when] = await inMain([], async (main) => {
        const line = armedLineIn(main, { labels: LINE });
        const reread = Promise.withResolvers<void>();
        try {
          clickPress(line.button("Two"));
          line.redraw({ held: ["Two"], labels: LINE });
          await settled();
          // The write lands, and its `onSuccess` holds it through the list's read.
          const held = bound.result.current[1](reread.promise);
          for (const _ of [0, 1, 2]) await settled();
          if (when === "after the read's hold ended") {
            reread.resolve();
            await held;
            line.redraw({ labels: LINE });
            await settled();
          }
          line.redraw({ labels: ["One", "Three"] });
          reread.resolve();
          await held;
          return landing();
        } finally {
          line.remove();
        }
      });
    }
  } finally {
    bound.unmount();
  }
  expect(landed).toEqual({ "after the read's hold ended": "the page", "inside the read": "Three" });
});

// (dm) Armed, and the press's write is refused: the press disabled its own control while the
// write was out, which dropped the focus to the page, and the refusal enables it again. Nothing
// pulls the focus back to it, and a frame and a task later the press is disarmed: a loss after
// that leaves the focus on the page.
test("an armed press whose write is refused is disarmed, and the focus is not pulled back to its control", async () => {
  await scrollingNothing(async () => {
    await inMain([], async (main) => {
      const line = armedLineIn(main, { labels: LINE });
      try {
        const two = line.button("Two");
        clickPress(two);
        // The press disables its own control, and the engine drops the focus off it then;
        // happy-dom blurs no disabled button, so the focus is dropped first, as in (c).
        two.blur();
        line.redraw({ disabled: ["Two"], labels: LINE });
        for (const _ of [0, 1]) await settled();
        line.redraw({ labels: LINE });
        await settled();
        expect(document.activeElement).toBe(document.body);
        // Focused with no press, as the page's own keys put it there, and taken away by an update.
        two.focus();
        line.redraw({ labels: ["One", "Three"] });
        expect(document.activeElement).toBe(document.body);
      } finally {
        line.remove();
      }
    });
  });
});

// (dn) Armed, and a hold on what answers the press that never ends: the press stays armed until
// the write deadline and then the request deadline have passed from it, the longest a write that
// lands within its deadline and then its re-read can take, and no longer. A loss a second short
// of that bound is the press's, and the focus goes to the control now at its place; a refetch
// once the bound has passed, a frame and a task on, is no doing of the reader's.
test("an armed press whose hold never ends is disarmed at its bound, and a later refetch leaves the focus on the page", async () => {
  jest.useFakeTimers();
  const landed: Record<string, string | null> = {};
  try {
    const waits = {
      "a second short of the bound": SEND_DEADLINE_MS + REQUEST_DEADLINE_MS - 1_000,
      "once the bound has passed": SEND_DEADLINE_MS + REQUEST_DEADLINE_MS,
    };
    for (const [named, wait] of Object.entries(waits)) {
      landed[named] = await inMain([], async (main) => {
        const line = armedLineIn(main, { labels: LINE });
        try {
          clickPress(line.button("Two"));
          const answered = beginAnswerHold();
          jest.advanceTimersByTime(wait);
          // A press its bound ended ends a frame and a task on.
          await frame();
          jest.advanceTimersByTime(0);
          line.redraw({ labels: ["One", "Three"] });
          answered();
          return landing();
        } finally {
          line.remove();
        }
      });
    }
  } finally {
    realClock();
  }
  expect(landed).toEqual({
    "a second short of the bound": "Three",
    "once the bound has passed": "the page",
  });
});

// (do) Nothing armed: a refetch, a live update or another device takes the focused control away
// - a control of a component that hands its focus off, or the row holding it. That is as main
// does: the focus stays on the body, in no row, and nothing scrolls. The selection bar emptied
// the same way is `SelectionBar.test.tsx`'s.
test("with nothing armed, a control taken away by an update leaves the focus on the page, in no row, scrolling nothing", async () => {
  const landed: Record<string, string | null> = {};
  await scrollingNothing(async () => {
    landed["a control of a line"] = await inMain([], async (main) => {
      const line = armedLineIn(main, { labels: LINE });
      try {
        line.button("Two").focus();
        line.redraw({ labels: ["One", "Three"] });
        return landing();
      } finally {
        line.remove();
      }
    });
    landed["a row of a list"] = await inMain(["Before"], async (main) => {
      const host = document.createElement("div");
      main.append(host);
      const view = render(createElement(ArmedRows, { held: false, names: ["One", "Two"] }), {
        container: host,
      });
      try {
        view.getByRole("button", { name: "Answer One" }).focus();
        view.rerender(createElement(ArmedRows, { held: false, names: ["Two"] }));
        return landing();
      } finally {
        view.unmount();
        host.remove();
      }
    });
  });
  expect(landed).toEqual({ "a control of a line": "the page", "a row of a list": "the page" });
});

// (dp) Nothing armed, offline past the request deadline: a control held through a re-read the
// network never answers (`useReadBound`), focused with no press. Its hold ends as its own rule
// says, at the request deadline with the network's reason, and lets the control go, which keeps
// the focus. The update that then takes it away is no press's doing: the focus stays on the
// page, and nothing scrolls.
test("with nothing armed, a hold that ends offline at the request deadline keeps its own rule, and the later loss leaves the focus on the page", async () => {
  jest.useFakeTimers();
  const online = onlineManager.isOnline();
  const bound = renderHook(() => useReadBound());
  try {
    act(() => onlineManager.setOnline(false));
    await scrollingNothing(async () => {
      await inMain([], async (main) => {
        const line = armedLineIn(main, { labels: LINE });
        try {
          const two = line.button("Two");
          two.focus();
          line.redraw({ held: ["Two"], labels: LINE });
          const held = bound.result.current[1](new Promise<never>(() => undefined));
          await act(async () => {
            jest.advanceTimersByTime(REQUEST_DEADLINE_MS);
            await held;
          });
          line.redraw({ labels: LINE });
          expect(bound.result.current[0]).toBe(NETWORK_ERROR_MESSAGE);
          expect(two).toHaveFocus();
          line.redraw({ labels: ["One", "Three"] });
          expect(document.activeElement).toBe(document.body);
        } finally {
          line.remove();
        }
      });
    });
  } finally {
    act(() => onlineManager.setOnline(online));
    bound.unmount();
    realClock();
  }
});

// (dq) A keyboard reader arms what a pointer reader arms (`armOnPress`): at a component's root,
// Enter arms on any element - a list's text box picks its active row at Enter's `keydown`
// (`MultiSelect`) - and Space on an element that takes no typed text, where it presses. Space in
// a text box types, Escape is the reader leaving a control, and Tab or any other key moves or
// acts on nothing of the component's, so none of them arms.
test("Enter, and Space on a control that takes no typed text, arm a component's hand-off at its root, and no other key does", async () => {
  const armed: Record<string, boolean> = {};
  await inMain([], async (main) => {
    const host = document.createElement("div");
    main.append(host);
    const view = render(
      createElement(
        "div",
        { onKeyDownCapture: armOnPress },
        createElement("button", { type: "button" }, "Save"),
        createElement("input", { "aria-label": "Name", type: "text" })
      ),
      { container: host }
    );
    try {
      const save = view.getByRole("button", { name: "Save" });
      const name = view.getByRole("textbox", { name: "Name" });
      const keys = [
        ["Enter on a button", save, "Enter"],
        ["Space on a button", save, " "],
        ["Enter in a text box", name, "Enter"],
        ["Space in a text box", name, " "],
        ["Escape on a button", save, "Escape"],
        ["Tab on a button", save, "Tab"],
        ["x on a button", save, "x"],
      ] as const;
      for (const [named, target, key] of keys) {
        disarm();
        target.focus();
        // The arm is read in the task the key is pressed in, which user-event's keyboard waits past.
        fireEvent.keyDown(target, { key });
        armed[named] = causedByReader(target);
      }
    } finally {
      view.unmount();
      host.remove();
    }
  });
  expect(armed).toEqual({
    "Enter in a text box": true,
    "Enter on a button": true,
    "Escape on a button": false,
    "Space in a text box": false,
    "Space on a button": true,
    "Tab on a button": false,
    "x on a button": false,
  });
});
