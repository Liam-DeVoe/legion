import { expect, test } from "bun:test";
// fireEvent also gives one row's press below a click with no pointer press before it, which
// user-event has no form for either.
// biome-ignore lint/style/noRestrictedImports: the outside-press sites below are a pointerdown with no release, which user-event has no form for.
import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";

import { panelOf, pickNamed } from "../__tests__/list-picks";
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
  ariaDisabled,
  disabled,
  focusOnOpen,
  initial,
  marks,
  onCreate,
  onQueryChange,
  options,
}: {
  ariaDisabled?: boolean;
  disabled?: boolean;
  focusOnOpen?: boolean;
  initial?: string;
  marks?: ListMarks;
  onCreate?: (value: string) => boolean;
  onQueryChange?: (query: string) => void;
  options: string[];
}) {
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState(initial);
  const [handed, setHanded] = useState<string[]>([]);
  const [closings, setClosings] = useState<string[]>([]);
  return (
    <>
      <MultiSelect
        ariaDisabled={ariaDisabled}
        createLabel={(typed) => `Use "${typed}"`}
        disabled={disabled}
        emptyMessage="Nothing here."
        focusOnOpen={focusOnOpen}
        label="Epic"
        marks={marks}
        mode="single"
        onCreate={onCreate}
        onOpenChange={(next, closedBy) => {
          setOpen(next);
          if (!next) setClosings((before) => [...before, closedBy ?? "nothing"]);
        }}
        onPick={(picked) => {
          setValue(picked);
          setHanded((before) => [...before, picked]);
        }}
        onQueryChange={onQueryChange}
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
    const trigger = screen.getByRole("button", { name: pickNamed("Epic") });
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

// INFRA-3135, round 12: axe's `label-content-name-mismatch` (WCAG 2.5.3, Label in Name) failed on
// every single pick that showed a value: its trigger was named "Assignee of CORE-1" while it
// showed "alice", so the words on it were not in its name. Its name is the list's name, or the
// host's own, then what it shows, leaving out words the host hides from assistive technology,
// and no description repeats the value; a trigger that shows the list's name says it once.
test("a single pick's trigger is named for its list and then for what it shows", () => {
  const view = render(
    <>
      <SingleHost initial="docs" options={["api", "docs"]} />
      <MultiSelect
        emptyMessage="Nothing here."
        label="Issue"
        mode="single"
        onOpenChange={() => undefined}
        onPick={() => undefined}
        open={false}
        options={["CORE-1"]}
        searchLabel="Search issues"
        triggerAriaLabel="Choose issue"
        value="CORE-1"
      >
        Issue: CORE-1
      </MultiSelect>
      <MultiSelect
        chevron={false}
        emptyMessage="Nothing here."
        label="Repository"
        mode="single"
        onOpenChange={() => undefined}
        onPick={() => undefined}
        open={false}
        options={["acme/widgets"]}
        searchLabel="Search repositories"
        value="acme/widgets"
      >
        <span aria-hidden="true">acme/widgets</span>
        <span className="sr-only">acme/widgets, inherited</span>
      </MultiSelect>
      <MultiSelect
        emptyMessage="Nothing here."
        label="Project"
        mode="single"
        onOpenChange={() => undefined}
        onPick={() => undefined}
        open={false}
        options={["CORE"]}
        searchLabel="Search projects"
      />
    </>
  );
  try {
    const picked = screen.getByRole("button", { name: "Epic docs" });
    expect(picked).toHaveTextContent(/^docs$/);
    expect(picked).not.toHaveAccessibleDescription();
    expect(screen.getByRole("button", { name: "Choose issue Issue: CORE-1" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Repository acme/widgets, inherited" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Project" })).toHaveTextContent(/^Project$/);
  } finally {
    view.unmount();
  }
});

// INFRA-3135, round 12: below 1280 px every button is a flex box (`styles.css`), whose own text
// ends in no ellipsis, so a trigger cut a long value mid-word; and no trigger showed that it
// opens a list. In both modes the trigger's text is an element of its own that truncates - a
// single pick's carries its whole value as its title and is still what the trigger's name ends
// in - with a hidden chevron after it; a host whose children are a look of their own, a badge,
// draws them alone. `e2e/table-controls.e2e.ts` reads the ellipsis and the chevron in a browser.
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
    const single = screen.getByRole("button", { name: pickNamed("Epic") });
    for (const trigger of [single, screen.getByRole("button", { name: "Labels" })]) {
      const chevron = trigger.querySelector("svg");
      expect(chevron).toHaveAttribute("aria-hidden", "true");
      expect(chevron?.previousElementSibling).toHaveClass("truncate");
    }
    expect(single.querySelector(".truncate")).toHaveAttribute("title", "docs");

    const badge = screen.getByRole("button", { name: pickNamed("Assignee") });
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
    const trigger = screen.getByRole("button", { name: pickNamed("Epic") });
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

// An input method composes text in the list's text box from several keys and ends with Enter,
// which commits what it composed, or Escape, which cancels it: neither is the reader's pick or
// close. user-event has no form for a composing key, so each is fired with `isComposing`.
test("Enter, Escape and Tab that end or move an IME composition leave a single pick open with nothing picked", async () => {
  const user = userEvent.setup();
  const view = render(<SingleHost initial="api" options={["api", "docs"]} />);
  try {
    const trigger = screen.getByRole("button", { name: pickNamed("Epic") });
    await user.click(trigger);
    const box = screen.getByRole("combobox", { name: "Search epics" });
    await user.keyboard("{ArrowDown}");
    for (const key of ["Enter", "Escape", "Tab"]) {
      // user-event cannot mark a key as part of an IME composition, so the key is fired with
      // `isComposing` set.
      fireEvent.keyDown(box, { isComposing: true, key });
      expect(screen.queryByRole("listbox"), `${key} composing`).toBeInTheDocument();
      expect(box, `${key} composing`).toHaveFocus();
    }
    expect(screen.getByTestId("handed").textContent).toBe("");

    // The same keys with no composition are the reader's: Enter picks the active row.
    await user.keyboard("{Enter}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(screen.getByTestId("handed").textContent).toBe("docs");
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
    const trigger = screen.getByRole("button", { name: pickNamed("Epic") });
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

// A single pick whose host takes values its options lack - the issue header's Repository takes
// any `owner/name` - offers a typed text no option holds as a row of its own, in the host's words,
// and hands the text over trimmed. A text that names an option offers no such row; one an option
// merely contains does. The host's `true` closes the list as a pick does.
test("a single pick's onCreate row offers a text no option holds, and a taken one closes the list on the trigger", async () => {
  const user = userEvent.setup();
  const created: string[] = [];
  const view = render(
    <SingleHost
      onCreate={(value) => {
        created.push(value);
        return true;
      }}
      options={["api", "docs"]}
    />
  );
  try {
    const trigger = screen.getByRole("button", { name: "Epic" });
    await user.click(trigger);
    const search = screen.getByRole("combobox", { name: "Search epics" });
    await user.click(search);
    await user.paste("DOCS");
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual(["docs"]);
    await user.clear(search);
    await user.click(search);
    await user.paste(" urgent ");
    // No option holds it: the empty message says so, and the row under it takes the text.
    expect(screen.getByText("Nothing here.")).toBeTruthy();
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual([
      '+Use "urgent"',
    ]);
    await user.click(screen.getByRole("option", { name: 'Use "urgent"' }));
    expect(created).toEqual(["urgent"]);
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    expect(screen.getByTestId("closings").textContent).toBe("pick");
    expect(screen.getByTestId("handed").textContent).toBe("");

    // From the keyboard: Enter takes the row while it is the active one.
    await user.keyboard("{Enter}");
    await user.paste("ap");
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual([
      "api",
      '+Use "ap"',
    ]);
    await user.keyboard("{ArrowDown}");
    expect(screen.getByRole("combobox", { name: "Search epics" })).toHaveAttribute(
      "aria-activedescendant",
      screen.getByRole("option", { name: 'Use "ap"' }).id
    );
    await user.keyboard("{Enter}");
    expect(created).toEqual(["urgent", "ap"]);
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    expect(screen.getByTestId("handed").textContent).toBe("");
  } finally {
    view.unmount();
  }
});

// The host's `false` refuses the text: the list stays open with the text in its box and the
// focus in it, for the reader to correct.
test("a single pick's onCreate row the host refuses leaves the list open with the text in its box", async () => {
  const user = userEvent.setup();
  const created: string[] = [];
  const view = render(
    <SingleHost
      onCreate={(value) => {
        created.push(value);
        return false;
      }}
      options={["api"]}
    />
  );
  try {
    await user.click(screen.getByRole("button", { name: "Epic" }));
    const search = screen.getByRole("combobox", { name: "Search epics" }) as HTMLInputElement;
    await user.click(search);
    await user.paste("not one");
    await user.keyboard("{Enter}");
    await user.click(screen.getByRole("option", { name: 'Use "not one"' }));
    expect(created).toEqual(["not one", "not one"]);
    expect(screen.getByRole("listbox", { name: "Epic options" })).toBeTruthy();
    expect(search.value).toBe("not one");
    expect(search).toHaveFocus();
    expect(screen.getByTestId("closings").textContent).toBe("");
    expect(screen.getByTestId("handed").textContent).toBe("");
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
    const trigger = screen.getByRole("button", { name: pickNamed("Epic") });
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

/** Checks that the single pick stands open as a click on its trigger opens it: the focus in its
 *  text box, the search empty, and the picked value `active` the active option. */
function openedAsClicked(active: string): void {
  const search = screen.getByRole("combobox", { name: "Search epics" });
  expect(search).toHaveFocus();
  expect((search as HTMLInputElement).value).toBe("");
  expect(search).toHaveAttribute(
    "aria-activedescendant",
    screen.getByRole("option", { name: active }).id
  );
}

// The closed trigger opens on the keys the WAI-ARIA select-only combobox opens its list on,
// beside Enter and Space: ArrowDown and ArrowUp, with Alt (Option on a Mac) or without. Each of
// ArrowDown, Alt+ArrowDown and ArrowUp opens it as a click does, and moves no option.
test.each([
  ["ArrowDown", "{ArrowDown}"],
  ["Alt+ArrowDown", "{Alt>}{ArrowDown}{/Alt}"],
  ["ArrowUp", "{ArrowUp}"],
])("%s on the closed trigger opens the list as a click does", async (_name, keys) => {
  const user = userEvent.setup();
  const view = render(<SingleHost initial="docs" options={["api", "docs", "frontend"]} />);
  try {
    const trigger = screen.getByRole("button", { name: pickNamed("Epic") });
    act(() => trigger.focus());
    await user.keyboard(keys);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    openedAsClicked("docs");
  } finally {
    view.unmount();
  }
});

// A character typed on the closed trigger opens the list with that character in its text box,
// typed once, so the options are already narrowed by it and the next character follows it. Shift
// makes a character of its own.
test("a character typed on the closed trigger opens the list with it in the search, the options narrowed by it", async () => {
  const user = userEvent.setup();
  const queries: string[] = [];
  const view = render(
    <SingleHost
      onQueryChange={(query) => queries.push(query)}
      options={["api", "docs", "frontend"]}
    />
  );
  try {
    const trigger = screen.getByRole("button", { name: "Epic" });
    act(() => trigger.focus());
    await user.keyboard("p");
    const search = screen.getByRole("combobox", { name: "Search epics" }) as HTMLInputElement;
    expect(search).toHaveFocus();
    expect(search.value).toBe("p");
    expect([search.selectionStart, search.selectionEnd]).toEqual([1, 1]);
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual(["api"]);
    await user.keyboard("i");
    expect(search.value).toBe("pi");
    expect(queries).toEqual(["p", "pi"]);

    await user.keyboard("{Escape}");
    expect(trigger).toHaveFocus();
    await user.keyboard("{Shift>}D{/Shift}");
    expect((screen.getByRole("combobox", { name: "Search epics" }) as HTMLInputElement).value).toBe(
      "D"
    );
    expect(screen.getAllByRole("option").map((option) => option.textContent)).toEqual([
      "docs",
      "frontend",
    ]);
    expect(screen.getByTestId("handed").textContent).toBe("");
  } finally {
    view.unmount();
  }
});

// Space presses the trigger, as a click does, and types nothing into the search, even after a
// typed character opened the list before.
test("Space on the closed trigger still opens the list with an empty search", async () => {
  const user = userEvent.setup();
  const view = render(<SingleHost initial="docs" options={["api", "docs", "frontend"]} />);
  try {
    const trigger = screen.getByRole("button", { name: pickNamed("Epic") });
    act(() => trigger.focus());
    await user.keyboard("f");
    expect((screen.getByRole("combobox", { name: "Search epics" }) as HTMLInputElement).value).toBe(
      "f"
    );
    await user.keyboard("{Escape}");
    expect(trigger).toHaveFocus();
    await user.keyboard(" ");
    openedAsClicked("docs");
    expect(screen.getAllByRole("option")).toHaveLength(3);
  } finally {
    view.unmount();
  }
});

// A busy trigger (`aria-disabled`) and a disabled one open on none of the keys, whether or not the
// engine leaves the focus on them. The same keys open the trigger once it is enabled again.
test("a disabled or busy trigger opens on none of the keys that open an enabled one", async () => {
  const user = userEvent.setup();
  const keys = ["{ArrowDown}", "{Alt>}{ArrowDown}{/Alt}", "{ArrowUp}", "d"];
  const view = render(<SingleHost options={["api", "docs"]} />);
  try {
    const trigger = screen.getByRole("button", { name: "Epic" });
    for (const state of [{ ariaDisabled: true }, { disabled: true }]) {
      view.rerender(<SingleHost {...state} options={["api", "docs"]} />);
      for (const key of keys) {
        act(() => trigger.focus());
        await user.keyboard(key);
        expect(screen.queryByRole("listbox"), key).not.toBeInTheDocument();
        expect(trigger, key).toHaveAttribute("aria-expanded", "false");
      }
    }

    view.rerender(<SingleHost options={["api", "docs"]} />);
    for (const key of keys) {
      act(() => trigger.focus());
      await user.keyboard(key);
      expect(screen.queryByRole("listbox"), key).toBeInTheDocument();
      await user.keyboard("{Escape}");
    }
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
    const trigger = screen.getByRole("button", { name: pickNamed("Epic") });
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

/** A host that keeps the popover open as the search box's Tab asks it to close. The search
 *  box's Tab closes the list and stays put (the close hands the focus to the trigger), so the
 *  cases below put the focus on a row, the Create row or Clear as a screen reader's cursor puts it
 *  there: those are buttons, which it focuses. With `pickedFirst` it orders the rows as the labels' and the components'
 *  hosts do (`IssueLabels`, `ComponentPicker`): the picked ones first, each part by its name. With
 *  `dropPicked` it lists only the values not yet picked, so a row goes as it is picked. It stands
 *  for the row a press takes off the page in Details: a label only the open draft holds, which
 *  `IssueLabels` lists only while the draft holds it, goes as the reader unpicks it. */
function OpenHost({
  dropPicked = false,
  onCreate,
  options,
  pickedFirst = false,
}: {
  dropPicked?: boolean;
  onCreate?: (value: string) => void;
  options: string[];
  pickedFirst?: boolean;
}) {
  const [selected, setSelected] = useState<string[]>([]);
  const ordered = pickedFirst
    ? [...options].sort(
        (left, right) =>
          Number(selected.includes(right)) - Number(selected.includes(left)) ||
          left.localeCompare(right)
      )
    : options;
  return (
    <>
      <MultiSelect
        emptyMessage="Nothing here."
        label="Labels"
        onChange={setSelected}
        onCreate={onCreate}
        onOpenChange={() => undefined}
        open
        options={dropPicked ? ordered.filter((option) => !selected.includes(option)) : ordered}
        searchLabel="Search labels"
        selected={selected}
      />
      <output data-testid="selected">{selected.join(",")}</output>
    </>
  );
}

// Clear is drawn only while the pick holds a value, so a press of Clear takes Clear away under
// the focus. The focus goes to the search box, not to the body, where the popover's Escape no
// longer reaches it.
test("a keyboard press of Clear, by Enter or Space, empties the pick and the focus stays in the search box", async () => {
  const user = userEvent.setup();
  const view = render(<OpenHost options={["api", "docs"]} />);
  try {
    const search = screen.getByRole("combobox", { name: "Search labels" });
    expect(search).toHaveFocus();
    for (const key of ["{Enter}", " "]) {
      await user.keyboard("{Enter}");
      expect(screen.getByTestId("selected").textContent).toBe("api");
      // On Clear, as a screen reader's cursor puts the focus there.
      screen.getByRole("button", { name: "Clear labels" }).focus();
      expect(screen.getByRole("button", { name: "Clear labels" })).toHaveFocus();
      await user.keyboard(key);
      expect(screen.getByTestId("selected").textContent).toBe("");
      expect(screen.queryByRole("button", { name: "Clear labels" })).not.toBeInTheDocument();
      expect(search).toHaveFocus();
    }
  } finally {
    view.unmount();
  }
});

// The Create row is drawn only while the search names no option, so a press of it, which makes
// the value and empties the search, takes it away under the focus.
test("a keyboard press of the Create row makes the value and the focus stays in the search box", async () => {
  const user = userEvent.setup();
  const created: string[] = [];
  const view = render(<OpenHost onCreate={(value) => created.push(value)} options={["docs"]} />);
  try {
    const search = screen.getByRole("combobox", { name: "Search labels" });
    await user.click(search);
    await user.paste("urgent");
    screen.getByRole("option", { name: 'Create "urgent"' }).focus();
    expect(screen.getByRole("option", { name: 'Create "urgent"' })).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(created).toEqual(["urgent"]);
    expect(screen.queryByRole("option", { name: /Create/ })).not.toBeInTheDocument();
    expect(search).toHaveFocus();
  } finally {
    view.unmount();
  }
});

// The host's options are a read that can answer again while the popover is open, as a project's
// labels can come back without the one a row draws. That takes away the row the focus is on, which
// is no press of the reader's, so the focus stays on the page's body, as for every change the
// reader did not make.
test("a row that new options drop leaves the focus on the page's body", async () => {
  const view = render(<OpenHost options={["api", "docs"]} />);
  try {
    screen.getByRole("option", { name: "docs" }).focus();
    expect(screen.getByRole("option", { name: "docs" })).toHaveFocus();
    view.rerender(<OpenHost options={["api"]} />);
    expect(screen.queryByRole("option", { name: "docs" })).not.toBeInTheDocument();
    expect(document.body).toHaveFocus();
  } finally {
    view.unmount();
  }
});

// Options that come to hold the value the Create row offers make that value an option's row, so
// they take away the Create row the focus is on. That is no press of the reader's, so the focus
// stays on the page's body.
test("a Create row that new options name leaves the focus on the page's body", async () => {
  const user = userEvent.setup();
  const view = render(<OpenHost onCreate={() => undefined} options={["api"]} />);
  try {
    await user.click(screen.getByRole("combobox", { name: "Search labels" }));
    await user.paste("urgent");
    screen.getByRole("option", { name: 'Create "urgent"' }).focus();
    expect(screen.getByRole("option", { name: 'Create "urgent"' })).toHaveFocus();
    view.rerender(<OpenHost onCreate={() => undefined} options={["api", "urgent"]} />);
    expect(screen.queryByRole("option", { name: /Create/ })).not.toBeInTheDocument();
    expect(screen.getByRole("option", { name: "urgent" })).toBeTruthy();
    expect(document.body).toHaveFocus();
  } finally {
    view.unmount();
  }
});

// A pointer press of Clear leaves the focus in the search box, so Clear goes without it. That
// press answers no later loss: a Create row a screen reader's cursor focused, which new options
// then take away, is no press of the reader's, and its focus stays on the page's body.
test("a pointer press of Clear answers no later loss: a Create row new options take away leaves the focus on the page's body", async () => {
  const user = userEvent.setup();
  const view = render(<OpenHost onCreate={() => undefined} options={["api", "docs"]} />);
  try {
    const search = screen.getByRole("combobox", { name: "Search labels" });
    await user.keyboard("{Enter}");
    expect(screen.getByTestId("selected").textContent).toBe("api");
    await user.click(screen.getByRole("button", { name: "Clear labels" }));
    expect(screen.getByTestId("selected").textContent).toBe("");
    expect(search, "a pointer press of Clear took the focus out of the search box").toHaveFocus();
    await user.paste("urgent");
    const create = screen.getByRole("option", { name: 'Create "urgent"' });
    create.focus();
    view.rerender(<OpenHost onCreate={() => undefined} options={["api", "docs", "urgent"]} />);
    expect(screen.queryByRole("option", { name: /Create/ })).not.toBeInTheDocument();
    expect(
      document.body,
      "new options took the Create row away, and the focus left the page's body though the reader pressed nothing on it"
    ).toHaveFocus();
  } finally {
    view.unmount();
  }
});

/** Makes `parent` drop the focus of a child it moves, as Chromium does: a node taken out of the
 *  document to be put back elsewhere loses the focus to the body, where happy-dom keeps it on the
 *  node. React moves a keyed child with `insertBefore`, or with `appendChild` to the end. The
 *  patch is on `parent` alone, so it goes with that element. */
function dropFocusOnMove(parent: HTMLElement): void {
  const { appendChild, insertBefore } = parent;
  const blurMoved = (node: Node) => {
    const focused = document.activeElement;
    if (node.parentNode === parent && focused instanceof HTMLElement && node.contains(focused)) {
      focused.blur();
    }
  };
  parent.insertBefore = <T extends Node>(node: T, child: Node | null): T => {
    blurMoved(node);
    return insertBefore.call(parent, node, child) as T;
  };
  parent.appendChild = <T extends Node>(node: T): T => {
    blurMoved(node);
    return appendChild.call(parent, node) as T;
  };
}

// A host that orders the rows by the pick moves a row as the reader unpicks it. Chromium drops the
// focus of a node it moves to the page's body (`dropFocusOnMove` does that here), and React gives
// it back to the row as it ends the commit, since the row is still on the page. So a row that held
// the focus as it was pressed, as a screen reader's cursor puts it there, keeps it in its new
// place, and nothing takes it to the search box.
test("a focused row that moves as the reader unpicks it keeps the focus", async () => {
  const user = userEvent.setup();
  const view = render(<OpenHost options={["api", "bug", "docs"]} pickedFirst />);
  try {
    dropFocusOnMove(screen.getByRole("listbox", { name: "Labels options" }));
    await user.keyboard("{Enter}");
    await user.keyboard("{ArrowDown}");
    await user.keyboard("{Enter}");
    expect(screen.getByTestId("selected").textContent).toBe("api,bug");
    const api = screen.getByRole("option", { name: "api" });
    expect(screen.getAllByRole("option")[0], "the picked rows are not drawn first").toBe(api);
    api.focus();
    await user.click(api);
    expect(screen.getByTestId("selected").textContent).toBe("bug");
    expect(screen.getAllByRole("option")[1], "the unpicked row did not move").toBe(api);
    expect(api, "React keeps a moved row's focus, and the row lost it").toHaveFocus();
  } finally {
    view.unmount();
  }
});

// A host that does not order the rows by the pick leaves each row in its place as it is pressed,
// so a row that holds the focus keeps it, and the reader goes on from that row rather than from
// the search box.
test("a focused row that stays in its place as it is pressed keeps the focus", async () => {
  const user = userEvent.setup();
  const view = render(<OpenHost options={["api", "bug", "docs"]} />);
  try {
    const bug = screen.getByRole("option", { name: "bug" });
    bug.focus();
    await user.keyboard("{Enter}");
    expect(screen.getByTestId("selected").textContent).toBe("bug");
    expect(screen.getAllByRole("option")[1], "the picked row left its place").toBe(bug);
    expect(bug, "the row stayed in its place, and its pick took the focus off it").toHaveFocus();
    await user.keyboard(" ");
    expect(screen.getByTestId("selected").textContent).toBe("");
    expect(bug, "the row stayed in its place, and its unpick took the focus off it").toHaveFocus();
  } finally {
    view.unmount();
  }
});

// A press can take a row off the page, as `IssueLabels` does in Details with a label only its open
// draft holds, as the reader unpicks it; `dropPicked` stands for that, taking the row off as it is
// picked. The focus on that row falls to the page's body, where Escape no longer reaches the
// popover: React has no row to give it back to. A row that held the focus as it was pressed has
// that focus given to the search box once the press is drawn.
test("a focused row that its pick takes off the page gives the focus to the search box", async () => {
  const user = userEvent.setup();
  const view = render(<OpenHost dropPicked options={["api", "bug", "docs"]} />);
  try {
    const search = screen.getByRole("combobox", { name: "Search labels" });
    const bug = screen.getByRole("option", { name: "bug" });
    bug.focus();
    await user.keyboard("{Enter}");
    expect(screen.getByTestId("selected").textContent).toBe("bug");
    expect(bug.isConnected, "the picked row is still on the page").toBe(false);
    expect(
      search,
      "the pick took the row off the page, and the focus did not go to the search box"
    ).toHaveFocus();
  } finally {
    view.unmount();
  }
});

// Only a press of the row that holds the focus is noted, so a press of a row the focus is not on
// moves no focus, though its pick takes the row off the page. A pointer press focuses nothing
// (`onMouseDown`) and leaves the focus in the search box. A click with no pointer press, which the
// panel's taking of the focus on a pointer press inside it (`AnchoredPanel`) does not see, leaves
// the focus on the page's body, where a focused row that new options took away left it.
test("a press of a row the focus is not on, which its pick takes off the page, leaves the focus where it is", async () => {
  const user = userEvent.setup();
  const view = render(<OpenHost dropPicked options={["api", "bug", "docs"]} />);
  try {
    const search = screen.getByRole("combobox", { name: "Search labels" });
    expect(search).toHaveFocus();
    const bug = screen.getByRole("option", { name: "bug" });
    await user.click(bug);
    expect(screen.getByTestId("selected").textContent).toBe("bug");
    expect(bug.isConnected, "the picked row is still on the page").toBe(false);
    expect(search, "a pointer press of a row took the focus out of the search box").toHaveFocus();

    screen.getByRole("option", { name: "api" }).focus();
    view.rerender(<OpenHost dropPicked options={["bug", "docs"]} />);
    expect(
      document.body,
      "new options took the focused row away, and its focus did not fall to the page's body"
    ).toHaveFocus();
    const docs = screen.getByRole("option", { name: "docs" });
    // A click with no pointer press before it: user-event has no form for it.
    fireEvent.click(docs);
    expect(screen.getByTestId("selected").textContent).toBe("bug,docs");
    expect(docs.isConnected, "the picked row is still on the page").toBe(false);
    expect(
      document.body,
      "a press of a row the focus was not on moved the focus off the page's body"
    ).toHaveFocus();
  } finally {
    view.unmount();
  }
});

// A host can keep the pick as a row is pressed, so the press changes nothing it draws. The press
// of a focused row still asks for a commit of its own, which forgets the press, so the press
// answers no later loss: new options that then take that row away are no press of the reader's,
// and its focus stays on the page's body.
test("a focused row's press that changes nothing drawn answers no later loss: new options that drop the row leave the focus on the page's body", async () => {
  const user = userEvent.setup();
  const picker = (options: string[]) => (
    <MultiSelect
      emptyMessage="Nothing here."
      label="Labels"
      onChange={() => undefined}
      onOpenChange={() => undefined}
      open
      options={options}
      searchLabel="Search labels"
      selected={[]}
    />
  );
  const view = render(picker(["api", "bug"]));
  try {
    const bug = screen.getByRole("option", { name: "bug" });
    bug.focus();
    // The first press makes the row the active one, which the picker draws; the second changes
    // nothing drawn.
    await user.keyboard("{Enter}");
    await user.keyboard("{Enter}");
    expect(bug, "a press of a row that stays on the page took its focus").toHaveFocus();
    view.rerender(picker(["api"]));
    expect(bug.isConnected, "new options did not take the row away").toBe(false);
    expect(
      document.body,
      "new options took the row away, and the focus left the page's body though the reader's press took nothing away"
    ).toHaveFocus();
  } finally {
    view.unmount();
  }
});
