/** The status's menu (dispatch://INFRA-2160) as the rows reach it, and the trigger of the
 *  priority, which opens the shared single pick's list (`MultiSelect`), as the Snooze's does. A
 *  trigger is a button named for its control ("Priority of <KEY>", "Status") and then for what it
 *  shows ("Priority of <KEY> P1", "Status In progress"), unless what it shows is the first words
 *  of that name already (an unset "Priority"), so it is found by the start of its name
 *  (`triggerNamed`). A menu or a list is portalled to the body, so it is found by the id its
 *  trigger's `aria-controls` names while it is open, never under the trigger. A status is a value
 *  of the menu (`menuitemradio`, the current one checked); a priority or a Snooze time is an
 *  `option` of the list (`listOf`, `pickFromList` in `filters.ts`). Nothing here reads the app's
 *  own modules. */
import { expect, type Locator, type Page } from "@playwright/test";

/** The name of the trigger of the control named `label`, a menu or a single pick: `label` alone,
 *  or `label` and then what the trigger shows, as `listTrigger` finds a single pick's trigger. */
function triggerNamed(label: string): RegExp {
  return new RegExp(`^${label.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}(?: |$)`);
}

/** The bulk Snooze as a menu, open: a `menu` named "Snooze selected asks". The merged bar draws
 *  no such menu - its Snooze is the shared single pick, whose list `listOf` finds from
 *  `listTrigger(page, "Snooze selected asks")` - so only a row that still reads the bar's Snooze
 *  as a menu calls this. */
export function bulkSnoozeMenu(page: Page): Locator {
  return page.getByRole("menu", { name: "Snooze selected asks" });
}

/** The issue header's Status: the trigger of the status menu, which shows the status it holds. */
export function statusTrigger(page: Page): Locator {
  return page.getByTestId("issue-header").getByRole("button", { name: triggerNamed("Status") });
}

/** The trigger of the priority pick of the issue `key`, inside `scope`: the button of the shared
 *  single pick (`MultiSelect`, `aria-haspopup="listbox"`), named `Priority of <KEY>` and then the
 *  priority it shows. A press opens its list (`listOf` in `filters.ts`), not a menu. */
export function priorityTrigger(scope: Locator | Page, key: string): Locator {
  return scope.getByRole("button", { name: triggerNamed(`Priority of ${key}`) });
}

/** The menu `trigger` holds open: the element its `aria-controls` names. */
export async function menuOf(trigger: Locator): Promise<Locator> {
  await expect(trigger, "the menu button holds its menu open").toHaveAttribute(
    "aria-expanded",
    "true"
  );
  // The wait above settled the open menu, so the id its trigger names now is the menu's own.
  const id = await trigger.getAttribute("aria-controls");
  if (id === null) throw new Error("the open menu button names no menu in aria-controls");
  return trigger.page().locator(`[id="${id}"]`);
}

/** The choice of `menu` named `name`, an action or a value. */
export function choiceOf(menu: Locator, name: string): Locator {
  return menu
    .getByRole("menuitem", { exact: true, name })
    .or(menu.getByRole("menuitemradio", { exact: true, name }));
}

/** A pointer's pick: a click on `trigger` opens its menu, and a click on the choice `name`
 *  writes it. */
export async function pickFromMenu(trigger: Locator, name: string): Promise<void> {
  await trigger.click();
  await choiceOf(await menuOf(trigger), name).click();
}
