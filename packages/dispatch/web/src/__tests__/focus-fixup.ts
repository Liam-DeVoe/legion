/** The HTML focus fixup rule, which a browser runs and happy-dom does not: a focused control that
 *  becomes disabled loses the focus to the document's body
 *  (https://html.spec.whatwg.org/multipage/interaction.html#focus-fixup-rule). A test of a control
 *  that keeps a keyboard reader's focus through a state it cannot be pressed in runs this, so a
 *  `disabled` that would drop the focus in a browser drops it here too. The body is focused rather
 *  than the control blurred, because happy-dom's `blur()` refuses a disabled element. Returns what
 *  stops it. */
export function runFocusFixup(): () => void {
  const observer = new MutationObserver((records) => {
    for (const { target } of records) {
      if (
        target === document.activeElement &&
        target instanceof HTMLElement &&
        target.matches(":disabled")
      ) {
        document.body.focus();
      }
    }
  });
  observer.observe(document.body, {
    attributeFilter: ["disabled"],
    attributes: true,
    subtree: true,
  });
  return () => observer.disconnect();
}
