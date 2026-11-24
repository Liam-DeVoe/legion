import { screen, within } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";

import { panelOf } from "./list-picks";

/** The choice of the open list `trigger` opened that `name` names (`option`): only that list's
 *  choices count, never a control of the page behind it or of another list. The snooze, the
 *  priority and the status are single picks in a menu's place (`MultiSelect`'s `menu`). */
export function choiceOf(trigger: HTMLElement, name: string): HTMLElement {
  return within(panelOf(trigger)).getByRole("option", { name });
}

/** The names of the choices `trigger`'s list offers, in order - each choice's words, without the
 *  mark of the picked one or the digit drawn beside it - read with the list opened by a press on
 *  the trigger and closed again by Escape. */
export async function choicesOf(user: UserEvent, trigger: HTMLElement): Promise<string[]> {
  await user.click(trigger);
  const names = [...panelOf(trigger).querySelectorAll('[role="option"]')].map(
    (choice) => choice.querySelector(":scope > span:not([aria-hidden])")?.textContent ?? ""
  );
  await user.keyboard("{Escape}");
  return names;
}

/** Picks `name` from the list `trigger` opens, as a reader's two presses do: one on the trigger,
 *  one on the choice. */
export async function pickFromMenu(
  user: UserEvent,
  trigger: HTMLElement,
  name: string
): Promise<void> {
  await user.click(trigger);
  await user.click(choiceOf(trigger, name));
}

/** Whether a list is open anywhere on the page. */
export function menuOpen(): boolean {
  return screen.queryByRole("listbox") !== null;
}
