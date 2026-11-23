import { expect, test } from "bun:test";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { goingState } from "./going-store";
import { BoxFrame, BoxRow } from "./RunControls";

// The mode control (design v3, state 12): a small text button at the left of the box that names
// the mode the next run takes, and its menu of the three modes, each with one line on what it is
// for and no model and no number. It is a menu button: the keys move in its menu, a choice puts
// the cursor back in the box, and Escape closes the menu alone, never the bar around it.

/** The box row as the bar and the panel draw it, inside a dialog that records each key it hears,
 *  as the bar's dialog closes on Escape. */
function renderRow(): string[] {
  const heard: string[] = [];
  render(
    <div aria-label="Search" onKeyDown={(event) => heard.push(event.key)} role="dialog">
      <BoxRow
        box={
          <BoxFrame chip="CORE-1" chipTitle="CORE-1">
            <input aria-label="Ask the command bar" type="text" />
          </BoxFrame>
        }
        hint={undefined}
        onStop={() => {}}
      />
    </div>
  );
  return heard;
}

test("the mode control names the mode at the left of the box, and its menu says what each is for", async () => {
  const user = userEvent.setup();
  renderRow();
  const row = screen.getByTestId("going-level").parentElement as HTMLElement;
  // The control comes first in the row, before the context chip and the box.
  expect(row.firstElementChild).toBe(screen.getByTestId("going-level"));
  const button = screen.getByTestId("going-level-button");
  expect(button).toHaveTextContent("Standard ▾");
  expect(button).toHaveAttribute("aria-haspopup", "menu");
  expect(button.getAttribute("title")).toContain("A change applies from the next message.");

  await user.click(button);
  const menu = screen.getByRole("menu", { name: "Mode" });
  const items = within(menu).getAllByRole("menuitemradio");
  expect(items.map((item) => item.textContent?.replace("✓", ""))).toEqual([
    "Fast — quick lookups",
    "Standard — most questions",
    "Deep — big changes and reports; can take minutes",
  ]);
  expect(items.map((item) => item.getAttribute("aria-checked"))).toEqual([
    "false",
    "true",
    "false",
  ]);
  // The menu opens with the focus on the mode that is on.
  expect(items[1]).toHaveFocus();

  // A choice sets the mode the next run takes, and puts the cursor back in the box.
  await user.click(within(menu).getByRole("menuitemradio", { name: /^Deep/ }));
  expect(goingState().level).toBe("deep");
  expect(screen.queryByRole("menu")).toBeNull();
  expect(button).toHaveTextContent("Deep ▾");
  expect(screen.getByRole("textbox", { name: "Ask the command bar" })).toHaveFocus();
});

test("the keys move in the mode menu, and Escape closes it without closing the bar", async () => {
  const user = userEvent.setup();
  const heard = renderRow();
  const button = screen.getByTestId("going-level-button");
  button.focus();

  // Down opens the menu on the mode that is on; Down and Up move in it, wrapping.
  await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("menuitemradio", { name: /^Standard/ })).toHaveFocus();
  await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("menuitemradio", { name: /^Deep/ })).toHaveFocus();
  await user.keyboard("{ArrowDown}");
  expect(screen.getByRole("menuitemradio", { name: /^Fast/ })).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("menu")).toBeNull();
  expect(button).toHaveFocus();
  expect(heard).not.toContain("Escape");
  expect(goingState().level).toBe("standard");

  // Enter on a mode chooses it.
  await user.keyboard("{Enter}");
  await user.keyboard("{ArrowUp}");
  expect(screen.getByRole("menuitemradio", { name: /^Fast/ })).toHaveFocus();
  await user.keyboard("{Enter}");
  expect(goingState().level).toBe("fast");
  expect(screen.queryByRole("menu")).toBeNull();
  expect(screen.getByRole("textbox", { name: "Ask the command bar" })).toHaveFocus();
});
