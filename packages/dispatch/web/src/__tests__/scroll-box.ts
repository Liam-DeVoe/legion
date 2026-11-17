/**
 * A sideways scroll box a test sizes. happy-dom lays nothing out, so `scrollWidth` and
 * `clientWidth` read 0 and `scrollLeft` takes any number; a strip whose rule reads them only runs
 * where a test says how wide it is.
 */

/** A box `client` px wide around `scroll` px of content. A test may change either afterwards. */
export interface ScrollBox {
  client: number;
  scroll: number;
}

/** `element` reads its widths from `box`, and its `scrollLeft` stops at both ends of the scroll,
 *  as a browser's does. */
export function layOutScrollBox(element: HTMLElement, box: ScrollBox): void {
  let left = 0;
  Object.defineProperties(element, {
    clientWidth: { configurable: true, get: () => box.client },
    scrollLeft: {
      configurable: true,
      get: () => left,
      set: (value: number) => {
        left = Math.min(Math.max(value, 0), Math.max(box.scroll - box.client, 0));
      },
    },
    scrollWidth: { configurable: true, get: () => box.scroll },
  });
}
