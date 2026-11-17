import { expect, test } from "bun:test";
// biome-ignore lint/style/noRestrictedImports: this file keeps fireEvent for the bare mouse, pointer and touch presses a draggable parent must not see, which user-event has no form for
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { KeymapProvider } from "../features/shell/KeymapProvider";
import { useKeymap, useKeymapScope } from "../features/shell/keymap";
import { ChoiceMenu, type MenuChoice } from "./ChoiceMenu";

const CHOICES: readonly MenuChoice<string>[] = [
  { digit: "1", label: "Later today", value: "later" },
  { digit: "2", label: "Tomorrow", value: "tomorrow" },
  { digit: "3", label: "Next week", value: "week" },
];

/** A page with keys of its own on the keys a menu takes - the arrows, `j`, and a `2` that fires
 *  even in a text field - a row a key presses the trigger from, a control and some prose beside
 *  it, and the menu. Every key the page takes, and every value the menu writes, is noted. */
function Page({
  checked,
  chosen,
  held,
  taken,
}: {
  checked?: string;
  chosen: string[];
  held?: boolean;
  taken: string[];
}) {
  useKeymapScope("inbox");
  useKeymap("inbox", [
    { action: "inbox.next", run: (event) => taken.push(event.key) },
    { action: "inbox.arrows", run: (event) => taken.push(event.key) },
    { id: "two", inEditable: true, keys: "2", label: "Two", run: (event) => taken.push(event.key) },
  ]);
  return (
    <>
      <button type="button">Row</button>
      <ChoiceMenu
        checked={checked}
        choices={CHOICES}
        held={held}
        label="Snooze CORE-1"
        marks={{ "data-snooze-menu": "" }}
        onChoose={(value) => chosen.push(value)}
        triggerClassName=""
      >
        <span>{checked ?? "Snooze"}</span>
      </ChoiceMenu>
      <button type="button">Elsewhere</button>
      <p>Some prose beside the menu.</p>
    </>
  );
}

function renderPage(props: { checked?: string; held?: boolean } = {}) {
  const chosen: string[] = [];
  const taken: string[] = [];
  const user = userEvent.setup();
  const view = render(
    <KeymapProvider>
      <Page chosen={chosen} taken={taken} {...props} />
    </KeymapProvider>
  );
  const trigger = screen.getByRole("button", { name: "Snooze CORE-1" });
  const row = screen.getByRole("button", { name: "Row" });
  return { chosen, row, taken, trigger, unmount: view.unmount, user };
}

const menu = () => screen.queryByRole("menu", { name: "Snooze CORE-1" });
const item = (name: string) => screen.getByRole("menuitem", { name });

test("a press on the trigger opens a menu of its choices on the first, each with its digit and the menu's keys below", async () => {
  const { chosen, trigger, unmount, user } = renderPage();
  try {
    expect(trigger).toHaveAttribute("aria-haspopup", "menu");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(trigger).not.toHaveAttribute("aria-controls");
    await user.click(trigger);

    const opened = menu();
    expect(opened).toBeInTheDocument();
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    expect(trigger).toHaveAttribute("aria-controls", opened?.id ?? "");
    const items = within(opened as HTMLElement).getAllByRole("menuitem");
    expect(items.map((choice) => choice.textContent)).toEqual([
      "Later today1",
      "Tomorrow2",
      "Next week3",
    ]);
    // The digit is drawn for the eye and named to assistive tech as the item's shortcut.
    expect(items.map((choice) => choice.getAttribute("aria-keyshortcuts"))).toEqual([
      "1",
      "2",
      "3",
    ]);
    expect(item("Later today")).toHaveFocus();
    expect(items.map((choice) => choice.tabIndex)).toEqual([0, -1, -1]);
    expect(opened).toHaveTextContent(
      "↑ ↓ Home End to move · Enter or 1–3 to choose · Esc to close"
    );
    expect(chosen).toEqual([]);
  } finally {
    unmount();
  }
});

test("ArrowDown, ArrowUp, Home and End move the highlight, write nothing and reach no page key", async () => {
  const { chosen, taken, trigger, unmount, user } = renderPage();
  try {
    await user.click(trigger);
    await user.keyboard("{ArrowDown}");
    expect(item("Tomorrow")).toHaveFocus();
    expect(item("Tomorrow").tabIndex).toBe(0);
    expect(item("Later today").tabIndex).toBe(-1);
    await user.keyboard("{ArrowUp}");
    expect(item("Later today")).toHaveFocus();
    // The arrows wrap at the ends.
    await user.keyboard("{ArrowUp}");
    expect(item("Next week")).toHaveFocus();
    await user.keyboard("{ArrowDown}");
    expect(item("Later today")).toHaveFocus();
    await user.keyboard("{End}");
    expect(item("Next week")).toHaveFocus();
    await user.keyboard("{Home}");
    expect(item("Later today")).toHaveFocus();
    await user.keyboard("j");
    expect(menu()).toBeInTheDocument();
    expect(chosen).toEqual([]);
    expect(taken).toEqual([]);
  } finally {
    unmount();
  }
});

test("Enter writes the highlighted choice once, closes the menu and hands the focus back", async () => {
  const { chosen, trigger, unmount, user } = renderPage();
  try {
    await user.click(trigger);
    await user.keyboard("{ArrowDown}{Enter}");
    expect(chosen).toEqual(["tomorrow"]);
    expect(menu()).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    expect(trigger).toHaveAttribute("aria-expanded", "false");
  } finally {
    unmount();
  }
});

test("a click writes the choice it hits once", async () => {
  const { chosen, trigger, unmount, user } = renderPage();
  try {
    await user.click(trigger);
    await user.click(item("Next week"));
    expect(chosen).toEqual(["week"]);
    expect(menu()).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  } finally {
    unmount();
  }
});

test("a digit writes its choice at once, and no page key that takes the digit fires", async () => {
  const { chosen, taken, trigger, unmount, user } = renderPage();
  try {
    await user.click(trigger);
    await user.keyboard("2");
    expect(chosen).toEqual(["tomorrow"]);
    expect(taken).toEqual([]);
    expect(menu()).not.toBeInTheDocument();
    // A digit no choice carries does nothing in the menu.
    await user.click(trigger);
    await user.keyboard("7");
    expect(menu()).toBeInTheDocument();
    expect(chosen).toEqual(["tomorrow"]);
  } finally {
    unmount();
  }
});

test("Escape, Tab, Shift+Tab and a press outside close the menu and write nothing", async () => {
  const { chosen, trigger, unmount, user } = renderPage();
  try {
    await user.click(trigger);
    await user.keyboard("{ArrowDown}{Escape}");
    expect(menu()).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();

    await user.click(trigger);
    await user.keyboard("{ArrowDown}");
    await user.tab();
    expect(menu()).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();

    await user.click(trigger);
    await user.tab({ shift: true });
    expect(menu()).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();

    await user.click(trigger);
    await user.click(screen.getByText("Some prose beside the menu."));
    expect(menu()).not.toBeInTheDocument();

    // A second press on the trigger closes it too.
    await user.click(trigger);
    await user.click(trigger);
    expect(menu()).not.toBeInTheDocument();
    expect(chosen).toEqual([]);
  } finally {
    unmount();
  }
});

test("the focus goes back to the row a key pressed the trigger from, and to the trigger after a press on it", async () => {
  const { chosen, row, trigger, unmount, user } = renderPage();
  try {
    // A key's binding presses the trigger from the row, as the Inbox's `b` does: a click with no
    // pointer, while the focus is still on the row.
    act(() => row.focus());
    act(() => trigger.click());
    expect(item("Later today")).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(row).toHaveFocus();

    act(() => trigger.click());
    await user.keyboard("{ArrowDown}");
    await user.tab();
    expect(row).toHaveFocus();

    act(() => trigger.click());
    await user.keyboard("3");
    expect(chosen).toEqual(["week"]);
    expect(row).toHaveFocus();

    // A press outside on nothing that takes the focus hands it back to the row once the press is
    // over; a press on a control leaves it on that control, which the press acts on.
    act(() => trigger.click());
    await user.click(screen.getByText("Some prose beside the menu."));
    expect(menu()).not.toBeInTheDocument();
    expect(row).toHaveFocus();
    act(() => trigger.click());
    const elsewhere = screen.getByRole("button", { name: "Elsewhere" });
    await user.click(elsewhere);
    expect(menu()).not.toBeInTheDocument();
    expect(elsewhere).toHaveFocus();

    // Enter on the trigger is a press with no pointer too, from the trigger itself.
    act(() => trigger.focus());
    await user.keyboard("{Enter}");
    expect(item("Later today")).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(trigger).toHaveFocus();

    // A pointer's press is on the trigger, whatever held the focus before it.
    act(() => row.focus());
    await user.click(trigger);
    await user.keyboard("{Escape}");
    expect(trigger).toHaveFocus();
    expect(chosen).toEqual(["week"]);
  } finally {
    unmount();
  }
});

test("a menu of values draws radios with the current one checked, opens on it, and writes nothing when it is chosen again", async () => {
  const { chosen, trigger, unmount, user } = renderPage({ checked: "tomorrow" });
  try {
    // The trigger is described by what it draws: the value it holds.
    const described = document.getElementById(trigger.getAttribute("aria-describedby") ?? "");
    expect(described).toHaveTextContent("tomorrow");
    await user.click(trigger);
    const radios = screen.getAllByRole("menuitemradio");
    expect(radios.map((radio) => radio.getAttribute("aria-checked"))).toEqual([
      "false",
      "true",
      "false",
    ]);
    expect(screen.queryAllByRole("menuitem")).toEqual([]);
    expect(screen.getByRole("menuitemradio", { name: "Tomorrow" })).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(menu()).not.toBeInTheDocument();
    expect(chosen).toEqual([]);

    await user.click(trigger);
    await user.keyboard("{ArrowDown}{Enter}");
    expect(chosen).toEqual(["week"]);
  } finally {
    unmount();
  }
});

test("a held trigger keeps its focus and opens nothing", async () => {
  const { trigger, unmount, user } = renderPage({ held: true });
  try {
    expect(trigger).toHaveAttribute("aria-disabled", "true");
    expect(trigger).not.toBeDisabled();
    await user.click(trigger);
    expect(menu()).not.toBeInTheDocument();
    act(() => trigger.click());
    expect(menu()).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  } finally {
    unmount();
  }
});

test("a press on the trigger or in its menu stays out of a draggable parent", async () => {
  const reached: string[] = [];
  const user = userEvent.setup();
  const view = render(
    <article
      aria-label="CORE-1 Review the spec"
      onMouseDown={() => reached.push("mousedown")}
      onPointerDown={() => reached.push("pointerdown")}
      onTouchStart={() => reached.push("touchstart")}
    >
      <ChoiceMenu
        choices={CHOICES}
        label="Snooze CORE-1"
        marks={{}}
        onChoose={() => undefined}
        triggerClassName=""
      >
        Snooze
      </ChoiceMenu>
    </article>
  );
  try {
    const trigger = screen.getByRole("button", { name: "Snooze CORE-1" });
    // Bare mouse, pointer and touch presses: user-event sends a whole press sequence and cannot
    // deliver one of these events on its own.
    fireEvent.mouseDown(trigger);
    fireEvent.pointerDown(trigger);
    fireEvent.touchStart(trigger);
    await user.keyboard("{Tab}{Enter}");
    // The menu is portalled to the body, but it is a React child of the card.
    fireEvent.mouseDown(item("Tomorrow"));
    fireEvent.pointerDown(item("Tomorrow"));
    fireEvent.touchStart(item("Tomorrow"));
    expect(menu()).toBeInTheDocument();
    expect(reached).toEqual([]);
  } finally {
    view.unmount();
  }
});
