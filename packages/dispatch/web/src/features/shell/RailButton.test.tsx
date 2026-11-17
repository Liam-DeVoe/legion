import { expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { KeymapProvider } from "./KeymapProvider";
import { MOD_KEY_LABEL } from "./keymap";
import { RailButton } from "./RailButton";
import { userPreferenceStorageKey } from "./userPreference";

test("RailButton shows its action's keys, announces them and opens what it stands for", async () => {
  const user = userEvent.setup();
  let opened = 0;
  const view = render(
    <>
      <RailButton
        action="global.create"
        className="mt-1"
        label="New issue"
        onOpen={() => (opened += 1)}
      />
      <RailButton action="global.search" className="mt-4" label="Search" onOpen={() => {}} />
    </>
  );

  try {
    const button = screen.getByRole("button", { name: /new issue/i });
    expect(button).toHaveAttribute("aria-keyshortcuts", "C");
    expect(button.querySelector("kbd")?.textContent).toBe("c");
    await user.click(button);
    expect(opened).toBe(1);

    const search = screen.getByRole("button", { name: /search/i });
    expect(search).toHaveAttribute("aria-keyshortcuts", "Control+K Meta+K");
    expect(search.querySelector("kbd")?.textContent).toBe(MOD_KEY_LABEL === "⌘" ? "⌘K" : "Ctrl K");
  } finally {
    view.unmount();
  }
});

test("RailButton shows the person's own keys for its action, and none where they took them all", () => {
  const stored = userPreferenceStorageKey("alice", "shell.keymap");
  window.localStorage.setItem(
    stored,
    JSON.stringify({ "global.create": ["n"], "global.search": [] })
  );
  const view = render(
    <KeymapProvider login="alice">
      <RailButton action="global.create" className="mt-1" label="New issue" onOpen={() => {}} />
      <RailButton action="global.search" className="mt-4" label="Search" onOpen={() => {}} />
    </KeymapProvider>
  );

  try {
    const button = screen.getByRole("button", { name: /new issue/i });
    expect(button).toHaveAttribute("aria-keyshortcuts", "N");
    expect(button.querySelector("kbd")?.textContent).toBe("n");
    const search = screen.getByRole("button", { name: "Search" });
    expect(search).not.toHaveAttribute("aria-keyshortcuts");
    expect(search.querySelector("kbd")).toBeNull();
  } finally {
    view.unmount();
    window.localStorage.removeItem(stored);
  }
});
