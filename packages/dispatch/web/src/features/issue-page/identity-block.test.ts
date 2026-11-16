import { expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";

import { useIdentityBlock } from "./identity-block";

/** Leaves the page `y` pixels down, as a scroll leaves `window.scrollY`. */
function scrolledTo(y: number): void {
  Object.defineProperty(window, "scrollY", { configurable: true, value: y, writable: true });
}

// R11 and decision 40: the identity block draws its shadow once the page has scrolled more than
// 40 px, as the prototype's `#hdr-sticky.stuck` does (`scrollY > 40`), and not at the first
// pixel; a place the page restores past 40 px draws it in the commit that restores it.
test("the header's shadow appears only after 40 px of scroll", () => {
  const scrollY = Object.getOwnPropertyDescriptor(window, "scrollY");
  const original = globalThis.ResizeObserver;
  // happy-dom has no `ResizeObserver`, and the block's height is no part of this case.
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  const block = { current: document.createElement("header") };
  try {
    scrolledTo(0);
    const scrolled = renderHook(() => useIdentityBlock(block));
    expect(scrolled.result.current.condensed).toBe(false);
    act(() => {
      scrolledTo(40);
      window.dispatchEvent(new Event("scroll"));
    });
    expect(scrolled.result.current.condensed).toBe(false);
    act(() => {
      scrolledTo(41);
      window.dispatchEvent(new Event("scroll"));
    });
    expect(scrolled.result.current.condensed).toBe(true);
    scrolled.unmount();

    scrolledTo(0);
    const restored = renderHook(() => useIdentityBlock(block, 41));
    expect(restored.result.current.condensed).toBe(true);
    restored.unmount();
  } finally {
    globalThis.ResizeObserver = original;
    if (scrollY === undefined) {
      Reflect.deleteProperty(window, "scrollY");
    } else {
      Object.defineProperty(window, "scrollY", scrollY);
    }
  }
});
