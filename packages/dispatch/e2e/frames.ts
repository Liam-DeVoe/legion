import type { Page } from "@playwright/test";

/** Waits two rendering frames in `page`. Whatever the browser has just been given - a key press,
 *  a click, a viewport change - its own handler and the render React queued for it are both done
 *  within them, so a row that reads the page after this reads what that event produced rather
 *  than what was on screen before it. */
export function nextFrames(page: Page): Promise<void> {
  return page.evaluate(() => {
    const frames = Promise.withResolvers<void>();
    requestAnimationFrame(() => requestAnimationFrame(() => frames.resolve()));
    return frames.promise;
  });
}

/**
 * Waits out what a resize of `page` to `width` starts, so a row that reaches a width by a resize
 * reads what a load at that width draws. The caller has set the viewport. The wait runs to the
 * fourth frame after the first frame that draws the window at the new width (R), whose rendering
 * fires the resize event and each width query's change (`useMediaQuery`) before its frame
 * callbacks:
 *
 * 1. before R+1, React has drawn what those events asked for - the rail, the Contents pane, the
 *    view row's controls - and the observers of the band's and the view row's heights
 *    (`identity-block.ts`, `slots/view-row.tsx`) have published what R's layout measured; the
 *    header's labels, on an issue that has them, saw R's layout and read the line on R+1
 *    (`IssuePropertiesRow.tsx`);
 * 2. before R+2, that reading is drawn, and on R+1 an observer saw the layout of any render
 *    that landed after R;
 * 3. before R+3, the reading that observer asked for on R+2 is drawn;
 * 4. before R+4, a second reading is drawn where the first changed the labels' box's own width
 *    (seen on R+2, read on R+3).
 *
 * On a phone the window can stay wider than the viewport where the page overflows it, so the
 * wait for the first frame at the new width is bounded.
 */
export async function resizeSettled(page: Page, width: number): Promise<void> {
  await page.evaluate(async (width) => {
    const frame = () => {
      const drawn = Promise.withResolvers<void>();
      requestAnimationFrame(() => drawn.resolve());
      return drawn.promise;
    };
    for (let frames = 0; frames < 30; frames += 1) {
      await frame();
      if (window.innerWidth === width) break;
    }
    for (let frames = 0; frames < 4; frames += 1) await frame();
  }, width);
}
