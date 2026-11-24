import { expect, test } from "bun:test";
import { renderHook } from "@testing-library/react";

import { INPUT_METHOD_MARKS } from "../__tests__/input-method-marks";
import { usePanelDismiss } from "./usePanelDismiss";

// The Escape that ends an IME composition is the composition's: the open panel stays open, the
// focus stays where it is, and the key goes on to the view behind. An Escape after the
// composition closes the panel and puts the focus back on its trigger. Chromium and Firefox mark
// that Escape `isComposing`.
test.each(
  INPUT_METHOD_MARKS
)("an Escape that ends an IME composition, marked by %s, leaves the open panel open", (_mark, composing) => {
  const trigger = document.createElement("button");
  const panel = document.createElement("div");
  const field = document.createElement("input");
  panel.append(field);
  document.body.append(trigger, panel);
  const changes: boolean[] = [];
  const behind: KeyboardEvent[] = [];
  const hearBehind = (event: KeyboardEvent) => behind.push(event);
  window.addEventListener("keydown", hearBehind);
  const view = renderHook(() =>
    usePanelDismiss({
      onOpenChange: (open) => changes.push(open),
      open: true,
      panelRef: { current: panel },
      triggerRef: { current: trigger },
    })
  );
  try {
    field.focus();
    // user-event has no form for a key that ends a composition, so the event is made by hand.
    field.dispatchEvent(
      new KeyboardEvent("keydown", {
        bubbles: true,
        cancelable: true,
        ...composing,
        key: "Escape",
      })
    );
    expect(changes, "the Escape that ended a composition closed the panel").toEqual([]);
    expect(document.activeElement, "that Escape moved the focus").toBe(field);
    expect(behind.length, "that Escape stopped short of the view behind").toBe(1);

    field.dispatchEvent(
      new KeyboardEvent("keydown", { bubbles: true, cancelable: true, key: "Escape" })
    );
    expect(changes).toEqual([false]);
    expect(document.activeElement).toBe(trigger);
    expect(behind.length, "the panel's Escape reached the view behind").toBe(1);
  } finally {
    view.unmount();
    window.removeEventListener("keydown", hearBehind);
    trigger.remove();
    panel.remove();
  }
});
