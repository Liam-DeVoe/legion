import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";

import { KeymapProvider } from "../features/shell/KeymapProvider";
import { useKeymap, useKeymapScope } from "../features/shell/keymap";
import { DisplayMenu } from "./DisplayMenu";
import { MultiSelect } from "./MultiSelect";

/** A page with keys of its own - `j` and ArrowDown, as a table's next row takes them - and its
 *  display menu, a real panel. Every key the page's binding takes is noted in `taken`. */
function Page({ taken }: { taken: string[] }) {
  const [open, setOpen] = useState(false);
  const [sort, setSort] = useState("newest");
  useKeymapScope("inbox");
  useKeymap("inbox", [
    {
      id: "next",
      keys: ["j", "ArrowDown"],
      label: "Next row",
      palette: false,
      run: (event) => taken.push(event.key),
    },
  ]);
  return (
    <>
      <button type="button">On the page</button>
      <DisplayMenu
        label="View"
        onOpenChange={setOpen}
        open={open}
        sections={[
          {
            id: "sort",
            label: "Sort by",
            onChange: setSort,
            options: [
              { label: "Newest first", value: "newest" },
              { label: "Urgency", value: "urgency" },
            ],
            value: sort,
          },
        ]}
      />
    </>
  );
}

// INFRA-3135's review round 1, follow-up F5: the display menu replaced a select, where a page's
// single keys did nothing, with radios, which take no text; with the menu open, `j` on its radio
// walked the table behind it.
test("with the display menu open and its radio focused, the page's keys fire nothing, the arrows walk the radios, Escape closes it, and a key on the page with it open still fires", async () => {
  const user = userEvent.setup();
  const taken: string[] = [];
  const view = render(
    <KeymapProvider>
      <Page taken={taken} />
    </KeymapProvider>
  );
  try {
    const trigger = screen.getByRole("button", { name: "View" });
    await user.click(trigger);
    const newest = screen.getByRole("radio", { name: "Newest first" });
    expect(newest).toHaveFocus();

    await user.keyboard("j");
    expect(taken, "j on the menu's radio").toEqual([]);
    expect(newest).toHaveFocus();
    expect(screen.getByRole("dialog", { name: "View" })).toBeInTheDocument();

    // The arrows are the radio group's own: the next choice takes the focus and is chosen.
    await user.keyboard("{ArrowDown}");
    expect(taken, "ArrowDown on the menu's radio").toEqual([]);
    const urgency = screen.getByRole("radio", { name: "Urgency" });
    expect(urgency).toHaveFocus();
    expect(urgency).toBeChecked();

    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog", { name: "View" })).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();

    // Open, with the focus put back on the page: only a key pressed inside the panel is masked.
    await user.click(trigger);
    act(() => screen.getByRole("button", { name: "On the page" }).focus());
    await user.keyboard("j");
    expect(taken, "j on the page with the menu open").toEqual(["j"]);
    expect(screen.getByRole("dialog", { name: "View" })).toBeInTheDocument();
  } finally {
    view.unmount();
  }
});

/** The highest z-index `@legion/proof-editor`'s own stylesheet gives anything it draws over a
 *  document: its selection bar with Comment and Suggest, its mark popovers, its link card. */
function editorTopLayer(): number {
  const css = readFileSync(
    Bun.resolveSync("@legion/proof-editor/style.css", import.meta.dir),
    "utf8"
  );
  return Math.max(...[...css.matchAll(/z-index:\s*(\d+)/g)].map((layer) => Number(layer[1])));
}

/** A single pick a page draws, open or shut as the reader leaves it. */
function VersionList() {
  const [open, setOpen] = useState(false);
  return (
    <MultiSelect
      emptyMessage="No version matches that."
      label="Version"
      mode="single"
      onOpenChange={setOpen}
      onPick={() => {}}
      open={open}
      options={["", "1"]}
      searchLabel="Search versions"
    />
  );
}

// INFRA-3135, round 12: with text selected in a document, the editor's selection bar (fixed, at
// z-index 190) drew over the options of the Version list opened above it, and a press meant for
// an option landed on Suggest. An open panel - a list, a menu - is the reader's topmost layer,
// over every layer the document draws.
test("an open list or menu stands above every layer a document's own floating bars take", async () => {
  const user = userEvent.setup();
  const view = render(
    <KeymapProvider>
      <Page taken={[]} />
      <VersionList />
    </KeymapProvider>
  );
  try {
    const top = editorTopLayer();
    expect(top, "the editor's stylesheet names no layer").toBeGreaterThanOrEqual(190);
    await user.click(screen.getByRole("button", { name: "View" }));
    const menu = screen.getByRole("dialog", { name: "View" });
    expect(Number(menu.style.zIndex), "the display menu's layer").toBeGreaterThan(top);
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Version" }));
    const list = screen.getByRole("listbox", { name: "Version options" }).parentElement;
    expect(Number(list?.style.zIndex), "the Version list's layer").toBeGreaterThan(top);
  } finally {
    view.unmount();
  }
});
