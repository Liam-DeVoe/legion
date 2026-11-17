import type { Page } from "@playwright/test";

declare global {
  interface Window {
    __dispatchCopied?: string[];
    /** `holdClipboard`'s clipboard: how many writes were made, and the resolve of each write
     *  still held. */
    __dispatchHeldClipboard?: { held: (() => void)[]; writes: number };
  }
}

/** Replaces the page's async clipboard with a recorder (before any navigation) so a test can
 *  assert what a copy button actually wrote, not just that it reported "Copied". */
export async function recordClipboard(page: Page): Promise<() => Promise<string[]>> {
  await page.addInitScript(() => {
    const copied: string[] = [];
    window.__dispatchCopied = copied;
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: async (text: string) => {
          copied.push(text);
        },
      },
    });
  });
  return () => page.evaluate(() => window.__dispatchCopied ?? []);
}

/** The page's clipboard, as `holdClipboard` replaces it. */
export interface HeldClipboard {
  /** Lets every held write land: each `writeText` promise still out resolves. */
  land(): Promise<void>;
  /** How many writes the page has made to the clipboard. */
  writes(): Promise<number>;
}

/**
 * Replaces the page's clipboard, before the page loads, with one that counts each
 * `navigator.clipboard.writeText` and holds the promise it returns until the row calls `land`,
 * so a row can press a copy control while its write is out. Called before the row's first
 * navigation, as `recordClipboard` is.
 */
export async function holdClipboard(page: Page): Promise<HeldClipboard> {
  await page.addInitScript(() => {
    const state = { held: [] as (() => void)[], writes: 0 };
    window.__dispatchHeldClipboard = state;
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: () => {
          state.writes += 1;
          const { promise, resolve } = Promise.withResolvers<void>();
          state.held.push(resolve);
          return promise;
        },
      },
    });
  });
  return {
    land: () =>
      page.evaluate(() => {
        for (const resolve of window.__dispatchHeldClipboard?.held.splice(0) ?? []) resolve();
      }),
    writes: () => page.evaluate(() => window.__dispatchHeldClipboard?.writes ?? 0),
  };
}
