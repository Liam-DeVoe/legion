/** What `IssueMargin.test.tsx` and `IssueClaimControl.test.tsx` both read the focus with. It is
 *  a module of its own and not a test file, because a test file that imports another runs that
 *  file's cases too. */

/** Moves the focus to the page's body whenever the element that holds it turns disabled, as
 *  Chromium does and happy-dom does not, until the function it returns is called. */
export function blurWhenDisabled(): () => void {
  const watching = new MutationObserver(() => {
    const focused = document.activeElement;
    if (!(focused instanceof HTMLElement) || !focused.hasAttribute("disabled")) return;
    // happy-dom's `blur()` does nothing on a disabled element, so the attribute comes off for the
    // blur alone and goes back at once.
    const value = focused.getAttribute("disabled") ?? "";
    focused.removeAttribute("disabled");
    focused.blur();
    focused.setAttribute("disabled", value);
  });
  watching.observe(document, { attributeFilter: ["disabled"], attributes: true, subtree: true });
  return () => watching.disconnect();
}
