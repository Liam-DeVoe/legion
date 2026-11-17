/** The text boxes in the page now: a list's own box is one that appears when the list opens. */
export function textBoxes(): Element[] {
  return [
    ...document.querySelectorAll('input[type="search"], input[type="text"], input:not([type])'),
  ];
}

/** The list a text box narrows: the element it names in `aria-controls`. Only that list's
 *  options count, never a control of the page behind it or of another list. */
export function listOf(box: Element): HTMLElement {
  const id = box.getAttribute("aria-controls");
  const list = id === null ? null : document.getElementById(id);
  if (list === null) throw new Error("the text box names no list");
  return list;
}
