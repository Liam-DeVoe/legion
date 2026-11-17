import { afterEach, expect, spyOn, test } from "bun:test";
import { act, render } from "@testing-library/react";

import { installIntersectionObserver } from "../../__tests__/intersection-observer";
import { CutWords } from "./CutWords";

/**
 * The page's one measurer of where each row's words are cut (`clamp-measurer.ts`, decision 196
 * of dispatch://INFRA-1196/artifact/part-decisions-md). Each row once measured its own words in
 * its own layout effect, after the rows before it had written to the page, so the browser laid
 * the page out again for each row.
 */

/** How many rows the page draws, and how many of them stand within a screen of the viewport. */
const ROWS = 60;
const NEAR = 50;

const WORDS = Array.from({ length: 100 }, (_unused, at) => `word${at + 1}`).join(" ");

const restores: (() => void)[] = [];

afterEach(() => {
  for (const restore of restores.splice(0).reverse()) restore();
});

// Decision 196: when many rows mount, the words of the ones near the viewport are measured in
// one batch, a frame after the intersection observer says where they stand. Each of them is
// read once, and every read of the batch comes before its first write: no read sees a row
// already cut. The rows away from the viewport are not read.
test("many rows' words are measured in one batch a frame, every read before any write", () => {
  const near = installIntersectionObserver();
  restores.push(near.restore);
  // Each frame the page asks for, run by the test.
  const frames: FrameRequestCallback[] = [];
  const frame = spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
    frames.push(callback);
    return frames.length;
  });
  const cancel = spyOn(window, "cancelAnimationFrame").mockImplementation(() => {});
  restores.push(
    () => frame.mockRestore(),
    () => cancel.mockRestore()
  );
  // Every words box lays out 600 px tall, 25 lines of 24 px; each read of a box's height notes
  // how many rows were drawn cut when it was made.
  const cutWhenRead: number[] = [];
  const own = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get: () => {
      cutWhenRead.push(document.querySelectorAll("[data-node-more]").length);
      return 600;
    },
  });
  restores.push(() => {
    if (own === undefined) Reflect.deleteProperty(HTMLElement.prototype, "scrollHeight");
    else Object.defineProperty(HTMLElement.prototype, "scrollHeight", own);
  });
  const box = spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(
    new DOMRect(0, 0, 600, 600)
  );
  restores.push(() => box.mockRestore());

  const view = render(
    <ol>
      {Array.from({ length: ROWS }, (_unused, at) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: the rows never move.
        <li key={at}>
          <CutWords className="" whole={false}>
            <p>{WORDS}</p>
          </CutWords>
        </li>
      ))}
    </ol>
  );
  restores.push(() => view.unmount());
  const words = [...view.container.querySelectorAll("[data-node-words]")];
  expect(words).toHaveLength(ROWS);
  expect(cutWhenRead, "a row read its words' layout as it mounted").toEqual([]);
  expect(frames, "a batch was asked for before anything said where the rows stand").toEqual([]);

  act(() => {
    for (const element of words.slice(0, NEAR)) near.show(element);
  });
  expect(frames, "the near rows asked for more than one batch").toHaveLength(1);
  act(() => {
    frames.shift()?.(0);
  });
  expect(cutWhenRead, "a near row was read other than once, or a far row was read").toHaveLength(
    NEAR
  );
  expect(
    cutWhenRead.filter((cut) => cut > 0),
    "a read of the batch came after a row of it was drawn cut"
  ).toEqual([]);
  expect(view.container.querySelectorAll("[data-node-more]")).toHaveLength(NEAR);
  expect(
    words.slice(NEAR).filter((element) => element.parentElement?.querySelector("[data-node-more]")),
    "a row away from the viewport was cut"
  ).toEqual([]);
  expect(frames, "the batch asked for another").toEqual([]);
});
