import { afterEach, expect, spyOn, test } from "bun:test";
import { act, render } from "@testing-library/react";

import { installIntersectionObserver } from "../../__tests__/intersection-observer";
import { CutWords } from "./CutWords";

/**
 * The page's one measurer of where each row's words are cut (`clamp-measurer.ts`, decisions 196
 * and 202 of dispatch://INFRA-1196/artifact/part-decisions-md). Each row once measured its own
 * words in its own layout effect, after the rows before it had written to the page, so the
 * browser laid the page out again for each row.
 */

/** How many rows the page draws, and how many of them stand within a screen of the viewport. */
const ROWS = 60;
const NEAR = 50;

const WORDS = Array.from({ length: 100 }, (_unused, at) => `word${at + 1}`).join(" ");

const restores: (() => void)[] = [];

afterEach(() => {
  for (const restore of restores.splice(0).reverse()) restore();
});

/** `ROWS` rows of cut words, laid out as 600 px boxes, with the frames the page asks for run by
 *  the test and a clock that moves `readMs` on each read of a box's height. Each such read notes
 *  how many rows were drawn cut when it was made, in the list of the frame it was made in. */
function drawRows(readMs: number) {
  const near = installIntersectionObserver();
  restores.push(near.restore);
  const frames: FrameRequestCallback[] = [];
  const frame = spyOn(window, "requestAnimationFrame").mockImplementation((callback) => {
    frames.push(callback);
    return frames.length;
  });
  const cancel = spyOn(window, "cancelAnimationFrame").mockImplementation(() => {});
  let clock = 0;
  const now = spyOn(performance, "now").mockImplementation(() => clock);
  restores.push(
    () => frame.mockRestore(),
    () => cancel.mockRestore(),
    () => now.mockRestore()
  );
  const cutWhenRead: number[][] = [[]];
  const own = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollHeight");
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get: () => {
      clock += readMs;
      cutWhenRead.at(-1)?.push(document.querySelectorAll("[data-node-more]").length);
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
  /** Runs the next frame the page asked for; its reads go in a list of their own. */
  const runFrame = () => {
    cutWhenRead.push([]);
    act(() => {
      frames.shift()?.(clock);
    });
  };
  return { cutWhenRead, frames, near, runFrame, view, words };
}

// Decision 196: when many rows mount, the words of the ones near the viewport are measured in
// one batch, a frame after the intersection observer says where they stand. Each of them is
// read once, and every read of the batch comes before its first write: no read sees a row
// already cut. The rows away from the viewport are not read.
test("many rows' words are measured in one batch a frame, every read before any write", () => {
  const { cutWhenRead, frames, near, runFrame, view, words } = drawRows(0);
  expect(words).toHaveLength(ROWS);
  expect(cutWhenRead.flat(), "a row read its words' layout as it mounted").toEqual([]);
  expect(frames, "a batch was asked for before anything said where the rows stand").toEqual([]);

  act(() => {
    for (const element of words.slice(0, NEAR)) near.show(element);
  });
  expect(frames, "the near rows asked for more than one batch").toHaveLength(1);
  runFrame();
  expect(
    cutWhenRead.flat(),
    "a near row was read other than once, or a far row was read"
  ).toHaveLength(NEAR);
  expect(
    cutWhenRead.flat().filter((cut) => cut > 0),
    "a read of the batch came after a row of it was drawn cut"
  ).toEqual([]);
  expect(view.container.querySelectorAll("[data-node-more]")).toHaveLength(NEAR);
  expect(
    words.slice(NEAR).filter((element) => element.parentElement?.querySelector("[data-node-more]")),
    "a row away from the viewport was cut"
  ).toEqual([]);
  expect(frames, "the batch asked for another").toEqual([]);
});

// Decision 202: a frame reads for at most 50 ms. With each read taking 10 ms, a batch of 50 near
// rows goes on over ten frames of five rows each; each frame reads all its rows before it draws
// any of them cut, and each frame cuts the rows it read.
test("a batch bigger than one slice goes on over the frames after it, each slice reading before it writes", () => {
  const { cutWhenRead, frames, near, runFrame, view, words } = drawRows(10);
  act(() => {
    for (const element of words.slice(0, NEAR)) near.show(element);
  });
  const slices: number[] = [];
  while (frames.length > 0) {
    const cutBefore = view.container.querySelectorAll("[data-node-more]").length;
    runFrame();
    const reads = cutWhenRead.at(-1) ?? [];
    slices.push(reads.length);
    expect(
      reads.filter((cut) => cut !== cutBefore),
      "a slice read after it had drawn a row cut"
    ).toEqual([]);
    expect(view.container.querySelectorAll("[data-node-more]")).toHaveLength(
      cutBefore + reads.length
    );
    if (slices.length > NEAR) throw new Error("the batch never ended");
  }
  expect(slices, "the batch was not cut into slices of 50 ms").toEqual(
    Array.from({ length: NEAR / 5 }, () => 5)
  );
});
