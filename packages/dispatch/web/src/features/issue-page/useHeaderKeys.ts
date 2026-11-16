import type { IssuePriority } from "../../api/types";
import { outsideAskCard } from "../inbox/ask-card";
import type { IssuePriorityWrite } from "../issue/useIssuePriority";
import { useKeymap } from "../shell/keymap";

/**
 * What the header's keys act on. Every target is a control the header already renders - the
 * pin inside its overflow menu, which is drawn hidden while shut - so a key does exactly what a
 * click on it does. Scoping the lookup to `[data-issue-header]` keeps a child issue's row - it
 * carries the same accessible names - out of reach.
 */
const HEADER = "[data-issue-header]";
const PIN_TOGGLE = 'button[data-issue-action="pin"]';
const PRIORITY_SELECT = 'select[aria-label^="Priority of "]';
const STATUS_SELECT = 'select[aria-label="Status"]';
const TITLE_HEADING = 'h1[tabindex="0"]';

/** The header control `selector` names, or `null` when it is absent or refuses input (a closed
 *  issue, a save in flight): the one fact both the key's `run` and the `when` that offers it read,
 *  so a shortcut is listed exactly while its control would take a click. */
function usableControl(selector: string): HTMLElement | null {
  const node = document.querySelector(HEADER)?.querySelector<HTMLElement>(selector);
  if (node == null) {
    return null;
  }
  const disabled =
    (node instanceof HTMLButtonElement || node instanceof HTMLSelectElement) && node.disabled;
  return disabled ? null : node;
}

/**
 * The line at the outline's foot while it answers a question: the question's options are its
 * chips there, which the keyboard reaches, so its controls own the keys as an ask card's do.
 */
const ANSWER_LINE = "form[data-answering]";

/**
 * Whether focus is outside every `<input>`, every ask card and the line that answers a question.
 * `0`-`3` and `Shift+P` are the two keys that write the issue without focusing a control, so they
 * are the two that have to say where the reader is not: a digit typed straight after picking a
 * question's option belongs to that control's own surface, not to the issue's priority. The
 * registry passes single keys through every control that takes no typed text, so this is the
 * page's own question to answer.
 *
 * A focused `<button>` is deliberately not excluded. Every single-key binding in the registry
 * fires from one: a digit has no native meaning on a button, and after any click - a view, a
 * disclosure, Copy - focus sits on the button that was clicked, which is where a reader reaching
 * for the next shortcut stands. An ask card's buttons and the answer line's are the exception,
 * and that is what `outsideAskCard` (`inbox/ask-card.ts`, the one definition both pages read) and
 * `ANSWER_LINE` cover: there, the question's controls own the keys. `e` opens a control rather
 * than write, so a stray press is visible and undoable and it is offered throughout.
 */
function outsideInputsAndAskCards(): boolean {
  const active = document.activeElement;
  return (
    !(active instanceof HTMLInputElement) &&
    outsideAskCard() &&
    active?.closest(ANSWER_LINE) == null
  );
}

/**
 * The header's keys (dispatch://INFRA-1196 R11), the ones today's header binds, each with its
 * target on the new header: `s` focuses the status and `p` the priority, `0` to `3` set the
 * priority, `e` puts the caret in the title, `Shift+P` pins and unpins, and Escape leaves a
 * focused status or priority.
 *
 * They register into the `issue` scope the view row pushes (`useIssueKeymap.ts`); this hook
 * pushes none. The digits go through the header's one priority write, the one its picker and the
 * palette's Set priority rows use, so a refusal of any of them is said once, by the header.
 */
export function useHeaderKeys(priorityWrite: IssuePriorityWrite): void {
  useKeymap("issue", [
    {
      id: "status",
      keys: "s",
      label: "Focus the status",
      run: () => usableControl(STATUS_SELECT)?.focus(),
      when: () => usableControl(STATUS_SELECT) !== null,
    },
    {
      id: "priority",
      keys: "p",
      label: "Focus the priority",
      run: () => usableControl(PRIORITY_SELECT)?.focus(),
      when: () => usableControl(PRIORITY_SELECT) !== null,
    },
    {
      id: "set-priority",
      keys: ["0", "1", "2", "3"],
      label: "Set priority P0–P3",
      run: (event) => priorityWrite.submit(Number(event.key) as IssuePriority),
      when: () => usableControl(PRIORITY_SELECT) !== null && outsideInputsAndAskCards(),
    },
    {
      // The heading takes focus only while the issue is open: closed, its `tabIndex` is -1 and
      // `onFocus` does not open the editor, so the key is offered exactly while it edits.
      id: "title",
      keys: "e",
      label: "Edit the title",
      run: () => usableControl(TITLE_HEADING)?.focus(),
      when: () => usableControl(TITLE_HEADING) !== null,
    },
    {
      id: "pin",
      keys: "Shift+P",
      label: "Pin or unpin the issue",
      run: () => usableControl(PIN_TOGGLE)?.click(),
      when: () => usableControl(PIN_TOGGLE) !== null && outsideInputsAndAskCards(),
    },
    {
      // `s` and `p` put focus in a native select, where every single-key binding is suspended
      // and the next keystroke is the select's own type-ahead - `t` would pick Todo or Testing
      // rather than start a view's chord. Escape is the way back out, as it is on the Inbox
      // rows, the board's cards and the architecture rows.
      id: "back",
      inEditable: true,
      keys: "Escape",
      label: "Back out of the focused header control",
      run: () => (document.activeElement as HTMLElement | null)?.blur(),
      when: () => {
        const active = document.activeElement;
        return active === usableControl(STATUS_SELECT) || active === usableControl(PRIORITY_SELECT);
      },
    },
  ]);
}
