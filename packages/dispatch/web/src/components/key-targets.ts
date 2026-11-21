/**
 * What an element is to a key pressed on it or a `change` fired on it: whether it takes typed
 * text, whether its `change` is the commit on blur of that text, and the mark of an open panel.
 * The keymap reads them to leave a control its own keys (`isEditableTarget`,
 * `features/shell/keymap.ts`), and the page's focus rescue to tell the reader's press from an
 * edit (`focus-place.ts`'s `isPress`). A module of its own with no import, so a module that draws
 * a control loads no keymap: a browser row imports a control's name from `AssigneeControl.tsx`,
 * which draws a `MultiSelect`, and the keymap reads `import.meta.env` as it loads, which
 * Playwright's process does not define.
 */

/** The attribute every open panel's element carries (`AnchoredPanel` writes it): what tells the
 *  keymap a key was pressed inside one (`isEditableTarget`), and the page's focus rescue that the
 *  panel is a dialog of its own (`focus-place.ts`). */
export const PANEL_ATTRIBUTE = "data-anchored-panel";

/** The `<input>` types whose value is typed text alone, so that their `change` is the commit on
 *  blur of what the reader typed, which Chromium fires as an update takes the focused field away.
 *  `TEXT_INPUT_TYPES` holds these and the date and time types besides, since a digit typed in
 *  one of those edits its value too; but a date or time box fires its `change` at the reader's
 *  pick - in its picker, or with the key that completes the value - as a `<select>` or a
 *  checkbox does, and not on blur (`commitsOnBlur`). */
const COMMIT_ON_BLUR_TYPES: Record<string, true> = {
  email: true,
  number: true,
  password: true,
  search: true,
  tel: true,
  text: true,
  url: true,
};

/** The `<input>` types that take typed text: those whose value is typed text alone
 *  (`COMMIT_ON_BLUR_TYPES`), and the date and time types, whose value a digit typed edits.
 *  Everything else an input can be - checkbox, radio, the button family, file, range, color -
 *  takes no text, so a letter or digit pressed while one has focus on the page is a shortcut, not
 *  typing: ticking a checkbox with the pointer must not silence the page's keys.
 *  `HTMLInputElement.type` normalises a missing or unknown attribute to `text`. */
const TEXT_INPUT_TYPES: Record<string, true> = {
  ...COMMIT_ON_BLUR_TYPES,
  date: true,
  "datetime-local": true,
  month: true,
  time: true,
  week: true,
};

/** Whether `element` takes typed text: a content-editable element, an `<input>` of a text-entry
 *  type (`TEXT_INPUT_TYPES`), or a `<textarea>`. A key pressed on one is the control's own
 *  (`isEditableTarget`), and the page's focus rescue reads a character typed in one as no press
 *  of the reader's (`focus-place.ts`'s `isPress`), since it only edits the field. */
export function takesText(element: Element): boolean {
  if (element instanceof HTMLElement && element.isContentEditable) {
    return true;
  }
  // An `<input>` takes typed text only in the text-entry types; a checkbox, radio or button
  // takes none.
  if (element instanceof HTMLInputElement) {
    return TEXT_INPUT_TYPES[element.type] === true;
  }
  return element.tagName === "TEXTAREA";
}

/** Whether a `change` on `element` is the commit on blur of the text typed in it, rather than the
 *  reader's pick: on an `<input>` whose value is typed text alone (`COMMIT_ON_BLUR_TYPES`), a
 *  `<textarea>`, or a content-editable element. Every other control fires its `change` at the
 *  pick - a `<select>`, a checkbox, a radio, a file, range or color input, a date or time box -
 *  so the page's focus rescue reads that `change` as a press of the reader's (`focus-place.ts`'s
 *  `isPress`). An `<input>` of the button family or of type hidden fires no `change`; it is
 *  answered false, so a `change` on one would read as a pick. */
export function commitsOnBlur(element: Element): boolean {
  if (element instanceof HTMLInputElement) {
    return COMMIT_ON_BLUR_TYPES[element.type] === true;
  }
  // Off an `<input>`, an element takes typed text (`takesText`) exactly where a `change` on it is
  // the commit on blur: a `<textarea>` or a content-editable element.
  return takesText(element);
}
