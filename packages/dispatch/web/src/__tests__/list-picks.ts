import { screen, within } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";

/** The text boxes in the page now: a list's own box is one that appears when the list opens. */
export function textBoxes(): Element[] {
  return [
    ...document.querySelectorAll('input[type="search"], input[type="text"], input:not([type])'),
  ];
}

/** The element `control` names in `aria-controls`, else a throw saying `missing`. */
function controlledBy(control: Element, missing: string): HTMLElement {
  const id = control.getAttribute("aria-controls");
  const element = id === null ? null : document.getElementById(id);
  if (element === null) throw new Error(missing);
  return element;
}

/** The list a text box narrows: the element it names in `aria-controls`. Only that list's
 *  options count, never a control of the page behind it or of another list. */
export function listOf(box: Element): HTMLElement {
  return controlledBy(box, "the text box names no list");
}

/** The panel of the list `trigger` opened: the list is portalled to the body, outside the
 *  host's own container, so it is found through the panel its trigger names (`aria-controls`). */
export function panelOf(trigger: Element): HTMLElement {
  return controlledBy(trigger, "the trigger names no open list");
}

/** Opens the single pick named `label` from its trigger and hands back its listbox, `<label>
 *  options`. The list is portalled to the body, outside the control's own container. */
export async function openList(user: UserEvent, label: string): Promise<HTMLElement> {
  await user.click(screen.getByRole("button", { name: label }));
  return screen.findByRole("listbox", { name: `${label} options` });
}

/** The open list's rows, each as its whole text: the tick of the picked one, then its label. */
export function rows(list: HTMLElement): string[] {
  return within(list)
    .getAllByRole("option")
    .map((option) => option.textContent ?? "");
}

/** Opens the single pick named `label` and picks the row named `name`, once it is in the list. */
export async function pick(user: UserEvent, label: string, name: string): Promise<void> {
  const list = await openList(user, label);
  await user.click(await within(list).findByRole("option", { name }));
}
