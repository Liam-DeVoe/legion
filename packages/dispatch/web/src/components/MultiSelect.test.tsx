import { expect, test } from "bun:test";
// biome-ignore lint/style/noRestrictedImports: the outside-press sites below are a pointerdown with no release, which user-event has no form for.
import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";

import { panelOf } from "../__tests__/list-picks";
import { type ListMarks, MultiSelect } from "./MultiSelect";

function Host({ onCreate, options }: { onCreate?: (value: string) => void; options: string[] }) {
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<string[]>([]);
  return (
    <>
      <MultiSelect
        emptyMessage="Nothing here."
        label="Labels"
        onChange={setSelected}
        onCreate={onCreate}
        onOpenChange={setOpen}
        open={open}
        options={options}
        searchLabel="Search labels"
        selected={selected}
      />
      <output data-testid="selected">{selected.join(",")}</output>
    </>
  );
}

/** A single pick as a host draws one: the value it holds, every value the list handed it, what
 *  closed it each time, and a control outside the list to press, whose own action closes it. */
function SingleHost({
  focusOnOpen,
  initial,
  marks,
  options,
}: {
  focusOnOpen?: boolean;
  initial?: string;
  marks?: ListMarks;
  options: string[];
}) {
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState(initial);
  const [handed, setHanded] = useState<string[]>([]);
  const [closings, setClosings] = useState<string[]>([]);
  return (
    <>
      <MultiSelect
        emptyMessage="Nothing here."
        focusOnOpen={focusOnOpen}
        label="Epic"
        marks={marks}
        mode="single"
        onOpenChange={(next, closedBy) => {
          setOpen(next);
          if (!next) setClosings((before) => [...before, closedBy ?? "nothing"]);
        }}
        onPick={(picked) => {
          setValue(picked);
          setHanded((before) => [...before, picked]);
        }}
        open={open}
        options={options}
        searchLabel="Search epics"
        value={value}
      />
      <output data-testid="handed">{handed.join(",")}</output>
      <output data-testid="closings">{closings.join(",")}</output>
      <button onClick={() => setOpen(false)} type="button">
        Elsewhere
      </button>
    </>
  );
}

test("clicking rows toggles a multi-selection, the trigger counts it and Clear empties it", async () => {
  const user = userEvent.setup();
  const view = render(<Host options={["api", "docs", "frontend"]} />);
  try {
    const trigger = screen.getByRole("button", { name: "Labels" });
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await user.click(trigger);
    const listbox = screen.getByRole("listbox", { name: "Labels options" });
    expect(listbox).toHaveAttribute("aria-multiselectable", "true");
    expect(screen.getByRole("combobox", { name: "Search labels" })).toHaveFocus();

    await user.click(screen.getByRole("option", { name: "docs" }));
    await user.click(screen.getByRole("option", { name: "api" }));
    expect(screen.getByTestId("selected").textContent).toBe("docs,api");
    expect(screen.getByRole("option", { name: "docs" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("option", { name: "frontend" })).toHaveAttribute(
      "aria-selected",
      "false"
    );
    expect(screen.getByRole("button", { name: "Labels · 2" })).toHaveAttribute(
      "aria-expanded",
      "true"
    );

    await user.click(screen.getByRole("option", { name: "docs" }));
    expect(screen.getByTestId("selected").textContent).toBe("api");

    await user.click(screen.getByRole("button", { name: "Clear labels" }));
    expect(screen.getByTestId("selected").textContent).toBe("");
    expect(screen.queryByRole("button", { name: "Clear labels" })).not.toBeInTheDocument();
  } finally {
    view.unmount();
  }
});

test("the search narrows the rows and arrows, Enter and Escape drive them from the input", async () => {
  const user = userEvent.setup();
  const view = render(<Host options={["api", "docs", "frontend"]} />);
  try {
    const trigger = screen.getByRole("button", { name: "Labels" });
    await user.click(trigger);
    const search = screen.getByRole("combobox", { name: "Search labels" });

    await user.clear(search);
    await user.click(search);
    await user.paste("DOC");
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual(["docs"]);
    await user.clear(search);

    // The first row is active until an arrow moves it; Enter toggles the active row.
    expect(search).toHaveAttribute(
      "aria-activedescendant",
      screen.getByRole("option", { name: "api" }).id
    );
    await user.keyboard("{ArrowDown}");
    await user.keyboard("{ArrowDown}");
    expect(search).toHaveAttribute(
      "aria-activedescendant",
      screen.getByRole("option", { name: "frontend" }).id
    );
    await user.keyboard("{Enter}");
    expect(screen.getByTestId("selected").textContent).toBe("frontend");
    // ArrowUp wraps from the first row to the last.
    await user.keyboard("{ArrowUp}");
    await user.keyboard("{ArrowUp}");
    await user.keyboard("{ArrowUp}");
    expect(search).toHaveAttribute(
      "aria-activedescendant",
      screen.getByRole("option", { name: "frontend" }).id
    );

    await user.clear(search);
    await user.click(search);
    await user.paste("zzz");
    expect(screen.getByText("Nothing here.")).toBeTruthy();
    await user.keyboard("{Enter}");
    expect(screen.getByTestId("selected").textContent).toBe("frontend");

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(trigger).toHaveFocus();
  } finally {
    view.unmount();
  }
});

test("with onCreate, a search that matches no option offers a Create row that Enter takes", async () => {
  const user = userEvent.setup();
  const created: string[] = [];
  const view = render(<Host onCreate={(value) => created.push(value)} options={["docs"]} />);
  try {
    await user.click(screen.getByRole("button", { name: "Labels" }));
    const search = screen.getByRole("combobox", { name: "Search labels" });
    await user.clear(search);
    await user.click(search);
    await user.paste("Docs");
    expect(screen.queryByRole("option", { name: /Create/ })).not.toBeInTheDocument();
    await user.clear(search);
    await user.click(search);
    await user.paste(" urgent ");
    expect(screen.getByRole("option", { name: 'Create "urgent"' })).toBeTruthy();
    await user.keyboard("{ArrowDown}");
    await user.keyboard("{Enter}");
    expect(created).toEqual(["urgent"]);
    expect((search as HTMLInputElement).value).toBe("");
  } finally {
    view.unmount();
  }
});

test("an outside pointer press closes the popover", async () => {
  const user = userEvent.setup();
  const view = render(
    <>
      <Host options={["docs"]} />
      <button type="button">Elsewhere</button>
    </>
  );
  try {
    await user.click(screen.getByRole("button", { name: "Labels" }));
    expect(screen.getByRole("listbox")).toBeTruthy();
    // A press with no release: user-event's click would add the release and the click the
    // popover's pointerdown listener is deliberately tested without.
    fireEvent.pointerDown(screen.getByRole("option", { name: "docs" }));
    expect(screen.getByRole("listbox")).toBeTruthy();
    // The same press, landing outside: user-event has no press-only form for it either.
    fireEvent.pointerDown(screen.getByRole("button", { name: "Elsewhere" }));
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
  } finally {
    view.unmount();
  }
});

test("optionLabel renders the row text and the search matches it while the value stays the key", async () => {
  const user = userEvent.setup();
  const changes: string[][] = [];
  const view = render(
    <MultiSelect
      emptyMessage="Nothing here."
      label="Status"
      onChange={(next) => changes.push(next)}
      onOpenChange={() => undefined}
      open
      optionLabel={(value) => (value === "in_progress" ? "In progress" : value)}
      options={["todo", "in_progress"]}
      searchLabel="Search statuses"
      selected={[]}
    />
  );
  try {
    const search = screen.getByRole("combobox", { name: "Search statuses" });
    await user.clear(search);
    await user.click(search);
    await user.paste("in prog");
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual([
      "In progress",
    ]);
    await user.click(screen.getByRole("option", { name: "In progress" }));
    expect(changes).toEqual([["in_progress"]]);
  } finally {
    view.unmount();
  }
});

test("a row's detail reads after its label and a comma, spoken with its own words when given", () => {
  const view = render(
    <MultiSelect
      emptyMessage="Nothing here."
      label="Labels"
      onChange={() => undefined}
      onOpenChange={() => undefined}
      open
      optionDetail={(value) => (value === "api" ? "3" : "1")}
      optionDetailLabel={(value) => (value === "api" ? "3 issues" : "1 issue")}
      options={["api", "docs"]}
      searchLabel="Search labels"
      selected={[]}
    />
  );
  try {
    // The visible row runs the count into the label; its accessible name never does.
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual([
      "api3",
      "docs1",
    ]);
    expect(screen.getByRole("option", { name: "api, 3 issues" })).toBeDefined();
    expect(screen.getByRole("option", { name: "docs, 1 issue" })).toBeDefined();
  } finally {
    view.unmount();
  }
});

test("the compact size changes the trigger's look alone, and the control carries no data-control", async () => {
  const user = userEvent.setup();
  const opened: boolean[] = [];
  const view = render(
    <MultiSelect
      emptyMessage="Nothing here."
      label="Labels"
      onChange={() => undefined}
      onOpenChange={(next) => opened.push(next)}
      open={false}
      options={["api", "docs"]}
      searchLabel="Search labels"
      selected={["api"]}
      size="compact"
    />
  );
  try {
    const trigger = screen.getByRole("button", { name: "Labels · 1" });
    // The vocabulary's eleven names are the controls a page draws; this one is a part of them,
    // not one of them, so nothing reads it by that attribute.
    expect(trigger).not.toHaveAttribute("data-control");
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    await user.click(trigger);
    expect(opened).toEqual([true]);
  } finally {
    view.unmount();
  }
});

test("a single pick's listbox is not multiselectable, and only its current value's option is selected", async () => {
  const user = userEvent.setup();
  const view = render(<SingleHost initial="docs" options={["api", "docs", "frontend"]} />);
  try {
    // Named for the list whatever it shows, and the value it shows is what it says it holds.
    const trigger = screen.getByRole("button", { name: "Epic", description: "docs" });
    expect(trigger).toHaveTextContent("docs");
    await user.click(trigger);
    expect(screen.getByRole("listbox", { name: "Epic options" })).not.toHaveAttribute(
      "aria-multiselectable"
    );
    expect(screen.getByRole("option", { name: "docs" })).toHaveAttribute("aria-selected", "true");
    for (const name of ["api", "frontend"])
      expect(screen.getByRole("option", { name })).toHaveAttribute("aria-selected", "false");
    // One value is changed by picking another, so there is nothing to clear.
    expect(screen.queryByRole("button", { name: "Clear epic" })).not.toBeInTheDocument();
  } finally {
    view.unmount();
  }
});

// INFRA-3135, round 12: below 1280 px every button is a flex box (`styles.css`), whose own text
// ends in no ellipsis, so a trigger cut a long value mid-word; and no trigger showed that it
// opens a list. In both modes the trigger's text is an element of its own that truncates - a
// single pick's carries its whole value as its title and is still the trigger's description -
// with a hidden chevron after it; a host whose children are a look of their own, a badge, draws
// them alone. `e2e/table-controls.e2e.ts` reads the ellipsis and the chevron in a browser.
test("a trigger's text truncates in its own element before a hidden chevron, unless its host draws a look of its own", () => {
  const view = render(
    <>
      <SingleHost initial="docs" options={["api", "docs"]} />
      <Host options={["api"]} />
      <MultiSelect
        chevron={false}
        emptyMessage="Nothing here."
        label="Assignee"
        mode="single"
        onOpenChange={() => undefined}
        onPick={() => undefined}
        open={false}
        options={["bob"]}
        searchLabel="Search people"
        value="bob"
      >
        <span data-testid="badge">bob</span>
      </MultiSelect>
    </>
  );
  try {
    const single = screen.getByRole("button", { name: "Epic", description: "docs" });
    for (const trigger of [single, screen.getByRole("button", { name: "Labels" })]) {
      const chevron = trigger.querySelector("svg");
      expect(chevron).toHaveAttribute("aria-hidden", "true");
      expect(chevron?.previousElementSibling).toHaveClass("truncate");
    }
    expect(single.querySelector(".truncate")).toHaveAttribute("title", "docs");

    const badge = screen.getByRole("button", { name: "Assignee", description: "bob" });
    expect(badge.querySelector("svg")).toBeNull();
    expect(badge.querySelector(".truncate")).toBeNull();
    expect(screen.getByTestId("badge").parentElement?.parentElement).toBe(badge);
  } finally {
    view.unmount();
  }
});

test("a single pick closes the list, puts the focus on its trigger and hands over one value", async () => {
  const user = userEvent.setup();
  const view = render(<SingleHost options={["api", "docs", "frontend"]} />);
  try {
    const trigger = screen.getByRole("button", { name: "Epic" });
    await user.click(trigger);
    await user.click(screen.getByRole("option", { name: "docs" }));
    expect(screen.getByTestId("handed").textContent).toBe("docs");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(trigger).toHaveAttribute("aria-expanded", "false");
    expect(trigger).toHaveFocus();
    expect(trigger).toHaveTextContent("docs");

    // From the keyboard: the list opens on the value picked, the arrows move the active option
    // on from it, and Enter picks it.
    await user.keyboard("{Enter}");
    const search = screen.getByRole("combobox", { name: "Search epics" });
    expect(search).toHaveFocus();
    await user.keyboard("{ArrowDown}");
    expect(search).toHaveAttribute(
      "aria-activedescendant",
      screen.getByRole("option", { name: "frontend" }).id
    );
    await user.keyboard("{Enter}");
    expect(screen.getByTestId("handed").textContent).toBe("docs,frontend");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    expect(trigger).toHaveTextContent("frontend");
  } finally {
    view.unmount();
  }
});

test("Escape, Tab and an outside press close a single pick with nothing picked and its value kept", async () => {
  const user = userEvent.setup();
  const view = render(<SingleHost initial="api" options={["api", "docs"]} />);
  try {
    const trigger = screen.getByRole("button", { name: "Epic" });
    await user.click(trigger);
    await user.keyboard("{ArrowDown}");
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();

    // Tab and Shift+Tab leave the reader on the trigger rather than carrying them past it.
    for (const shift of [false, true]) {
      await user.click(trigger);
      await user.keyboard("{ArrowDown}");
      await user.tab({ shift });
      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(trigger).toHaveFocus();
    }

    await user.click(trigger);
    // A press with no release, as the multi-select's outside press above is.
    fireEvent.pointerDown(screen.getByRole("button", { name: "Elsewhere" }));
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();

    expect(screen.getByTestId("handed").textContent).toBe("");
    expect(trigger).toHaveTextContent("api");
  } finally {
    view.unmount();
  }
});

/** Makes the document keep naming a focused element once it has left the page, as an engine that
 *  moves the focus off a removed element only at its next rendering update does (the HTML spec's
 *  focus fixup, `focusLost`); happy-dom names the body at once, as Chromium does. Answers what
 *  puts the document back. */
function keepRemovedFocus(): () => void {
  let prototype: object | null = Object.getPrototypeOf(document);
  let accessor: PropertyDescriptor | undefined;
  while (prototype !== null && accessor === undefined) {
    accessor = Object.getOwnPropertyDescriptor(prototype, "activeElement");
    prototype = Object.getPrototypeOf(prototype);
  }
  const read = accessor?.get;
  if (read === undefined) throw new Error("expected document.activeElement to be an accessor");
  let last: Element | null = null;
  const note = (event: Event) => {
    if (event.target instanceof Element) last = event.target;
  };
  document.addEventListener("focus", note, true);
  Object.defineProperty(document, "activeElement", {
    configurable: true,
    get() {
      const focused: Element | null = read.call(document);
      return focused === document.body && last !== null && !last.isConnected ? last : focused;
    },
  });
  return () => {
    Reflect.deleteProperty(document, "activeElement");
    document.removeEventListener("focus", note, true);
  };
}

// INFRA-3135, round 12: the close returned the focus to the trigger only where it had fallen to
// the body, and an engine that moves the focus off a removed element only at its next rendering
// update still names the text box the close took away when the close runs, so there Tab or
// Shift+Tab out of the box left the focus to the page's rescue. A close the list makes itself puts
// the reader on the trigger in every engine; a press outside still leaves them where it put them.
test("where the engine still names a removed text box focused, Tab out of it puts the focus on the trigger and a press outside leaves it where it landed", async () => {
  const user = userEvent.setup();
  const restore = keepRemovedFocus();
  const view = render(<SingleHost initial="api" options={["api", "docs"]} />);
  try {
    const trigger = screen.getByRole("button", { name: "Epic" });
    for (const shift of [false, true]) {
      await user.click(trigger);
      expect(screen.getByRole("combobox", { name: "Search epics" })).toHaveFocus();
      await user.tab({ shift });
      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(
        trigger,
        `${shift ? "Shift+Tab" : "Tab"} left the focus off the trigger`
      ).toHaveFocus();
    }

    await user.click(trigger);
    const elsewhere = screen.getByRole("button", { name: "Elsewhere" });
    await user.click(elsewhere);
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(elsewhere).toHaveFocus();
    expect(screen.getByTestId("handed").textContent).toBe("");
  } finally {
    view.unmount();
    restore();
  }
});

test("a single pick's text box narrows its options, and Enter over none picks nothing", async () => {
  const user = userEvent.setup();
  const view = render(<SingleHost options={["api", "docs", "frontend"]} />);
  try {
    await user.click(screen.getByRole("button", { name: "Epic" }));
    const search = screen.getByRole("combobox", { name: "Search epics" });
    await user.click(search);
    await user.paste("FRONT");
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual(["frontend"]);
    await user.clear(search);
    await user.click(search);
    await user.paste("zzz");
    expect(screen.queryAllByRole("option")).toHaveLength(0);
    expect(screen.getByText("Nothing here.")).toBeTruthy();
    await user.keyboard("{Enter}");
    expect(screen.getByTestId("handed").textContent).toBe("");
    expect(screen.getByRole("listbox", { name: "Epic options" })).toBeTruthy();
  } finally {
    view.unmount();
  }
});

// INFRA-3135, round 12: the Agents composer's Issue list. Enter on the list opened over the issue
// already picked has to keep that issue, as Enter on a `<select>` does, so the list opens with its
// picked value active rather than its first option.
test("a single pick opens with its picked value active, so Enter keeps it and the arrows move on from it", async () => {
  const user = userEvent.setup();
  const view = render(<SingleHost initial="docs" options={["api", "docs", "frontend"]} />);
  try {
    const trigger = screen.getByRole("button", { name: "Epic" });
    await user.click(trigger);
    expect(screen.getByRole("combobox", { name: "Search epics" })).toHaveAttribute(
      "aria-activedescendant",
      screen.getByRole("option", { name: "docs" }).id
    );
    await user.keyboard("{Enter}");
    expect(screen.getByTestId("handed").textContent).toBe("docs");
    expect(trigger).toHaveTextContent("docs");

    await user.click(trigger);
    await user.keyboard("{ArrowDown}");
    expect(screen.getByRole("combobox", { name: "Search epics" })).toHaveAttribute(
      "aria-activedescendant",
      screen.getByRole("option", { name: "frontend" }).id
    );
  } finally {
    view.unmount();
  }
});

// A host whose next move depends on how the reader left the list - the Agents composer's, whose
// Escape goes one level out to the row - is told what closed it.
test("every close tells the host what closed the list: a pick, Escape, Tab, a press outside, the trigger", async () => {
  const user = userEvent.setup();
  const view = render(<SingleHost initial="api" options={["api", "docs"]} />);
  try {
    const trigger = screen.getByRole("button", { name: "Epic" });
    await user.click(trigger);
    await user.click(screen.getByRole("option", { name: "docs" }));
    await user.click(trigger);
    await user.keyboard("{Escape}");
    await user.click(trigger);
    await user.tab();
    await user.click(trigger);
    // A press with no release, as the outside presses above are.
    fireEvent.pointerDown(screen.getByRole("button", { name: "Elsewhere" }));
    await user.click(trigger);
    await user.click(trigger);
    expect(screen.getByTestId("closings").textContent).toBe("pick,escape,tab,press,trigger");
  } finally {
    view.unmount();
  }
});

// The Agents composer opens its Issue list before the issues have come and hands the focus in
// itself once they have, and finds the list's text box - portalled out of the row - through the
// panel the trigger names.
test("the trigger names the open list's panel, the host's marks are on the trigger and the text box, and focusOnOpen false leaves the focus", async () => {
  const user = userEvent.setup();
  const view = render(
    <SingleHost
      focusOnOpen={false}
      marks={{ search: { "data-test-search": "" }, trigger: { "data-test-trigger": "" } }}
      options={["api", "docs"]}
    />
  );
  try {
    const trigger = screen.getByRole("button", { name: "Epic" });
    expect(trigger).toHaveAttribute("data-test-trigger", "");
    expect(trigger).not.toHaveAttribute("aria-controls");
    await user.click(trigger);
    expect(trigger).toHaveFocus();
    const panel = panelOf(trigger);
    const search = screen.getByRole("combobox", { name: "Search epics" });
    expect(search).toHaveAttribute("data-test-search", "");
    expect(panel.contains(search)).toBe(true);
    expect(panel.contains(screen.getByRole("listbox", { name: "Epic options" }))).toBe(true);
  } finally {
    view.unmount();
  }
});

// The Agents page closes a row's Issue list from the row itself (`leaveAgentComposer`), with the
// reader on the row: the close leaves them there rather than pulling them onto the trigger.
test("a close the host makes while the focus is outside the list leaves the focus there", async () => {
  const user = userEvent.setup();
  const view = render(<SingleHost options={["api", "docs"]} />);
  try {
    await user.click(screen.getByRole("button", { name: "Epic" }));
    const elsewhere = screen.getByRole("button", { name: "Elsewhere" });
    act(() => elsewhere.focus());
    await user.keyboard("{Enter}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(elsewhere).toHaveFocus();
  } finally {
    view.unmount();
  }
});
