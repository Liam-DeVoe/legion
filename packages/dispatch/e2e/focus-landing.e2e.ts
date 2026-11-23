/** Where the keyboard focus lands when a control that holds it is taken away, on each of the
 *  eight pages of INFRA-3135's R2: by the reader's own press, by a live update the server draws on
 *  the open page, by the same reader's change made on another device, by the reader's press made
 *  while the browser is offline and left past the request deadline, and by a press whose write or
 *  read is held while it is out.
 *
 *  The rules each case holds. The page computes them for every case from what it draws
 *  (`focus-landing-oracle.ts`, which says how each step is read), so no landing is named by hand;
 *  a case that names one too checks that both agree.
 *
 *  A loss the reader caused - their press, online or offline:
 *
 *  - A control the cause leaves standing, in the Tab order and in the reader's sight, keeps the
 *    focus (`kept`): it was not lost.
 *  - Else the loss is answered in the region the control was lost from: the nearest dialog, panel
 *    or selection bar around it, else the page's main content, while that region stands. A dialog
 *    or a panel that has gone answers it at the control it was opened from: there, where that
 *    control stands, else as that control's own loss. A selection bar that has gone answers it by
 *    its own rule, below. In the region, the landing is, in this order:
 *    1. what the press drew in the control's place: the controls of the elements that arrived where
 *       the control, or the parent it went with, stood, a `<form>`'s first; none where what
 *       arrived shows another item (`data-shown-item`) (`drawn`);
 *    2. else the control now at the place, moved there by the same change, which means only (i) a
 *       control of the lost control's own row, card or toolbar that moved into its place, or (ii)
 *       in the row (`ROW_SELECTOR`) that now stands where the lost control's row stood, the control
 *       at the same place among that row's controls, of the same tag and role, else that row's
 *       first control. A control of another row or item that only takes the lost control's index
 *       in the Tab order is not at the place (`moved-up`);
 *    3. else the nearest control before the place, walking back in document order over the
 *       region's controls, passing over each that takes typed text, each the reader cannot see now
 *       (wholly out of the window or of a box around it that clips), each not in the Tab order
 *       (`tabindex="-1"`, `disabled`, inside `[hidden]`), and each in another item than the lost
 *       control's (`walk-back`);
 *    4. else the region's own stop (`stop`).
 *    Each landing of steps 1 to 3 is in the Tab order and at least partly in the reader's sight.
 *    Where the place is inside a component's own region that still stands where it stood - the
 *    Inbox's reading column, which names the item it shows, or the ask card's row in the Inbox's
 *    list, a row that takes the focus itself - the steps run inside that region first, and with
 *    nothing there the landing is that region's own stop (`own-stop`): a component hands the focus
 *    to its own region's stop whoever caused the loss.
 *  - A selection bar that goes hands the focus to a row of the list it acted on, by the checkbox
 *    that marks it, never to the body, with no scroll. With a row marked: to the row marked last
 *    where it stands, else to the row now at its place, when that row is wholly in sight with
 *    nothing drawn over it, else to the nearest such row before that place (`bar-marked`); with no
 *    row marked, to the first such row, below whatever sticky chrome the page draws (`bar-first`);
 *    else to the list's own stop (`stop`).
 *  - A Tab or Shift+Tab inside the region the press was made in, while the press's write is out,
 *    does not end the press: a case that Tabs inside a held dialog before the write lands still
 *    reads its landing by the rules above.
 *
 *  A loss the reader did not cause - a live update, or their other device - leaves the focus on
 *  the body (`uncaused-body`), or on an open modal dialog's own stop where the loss was inside one
 *  and never on the body then (`modal-stop`), or on a component's own region stop: an element that
 *  stood around the lost control and still stands, is no control and is not the main content, as
 *  the Inbox's reading column, an ask card's row, or a dialog or panel a page draws in its control
 *  row (`own-stop`).
 *
 *  A press whose write or read is held keeps the focus on the control the reader pressed, else on
 *  the control its gone dialog was opened from, or anywhere inside the open modal dialog around
 *  that control, while the write or read is out; once let through, it lands by the rules above.
 *
 *  Scroll. No box moves for a loss the reader caused, save the one scroll that shows a landing that
 *  was partly in sight whole, by the least distance, to its nearest edge: only a box around the
 *  landing, in the direction that shows it, by no more than that distance and a pixel (`nearest
 *  edge`). A box that keeps its view after the reader's press took rows away above it scrolled:
 *  the press frees the whole height of the rows it takes away. A box may move for a loss the
 *  reader caused only to keep what the reader sees where it stood when the height above the
 *  landing changed: the box moved by exactly the height drawn above the landing or closed above
 *  it, inside the box or outside it, and the landing stands on screen where the reader saw its
 *  place before the press - where it stood, or where the row it moved up into the place of stood
 *  - each within a pixel (`view kept (drawn above)`, `view kept (shrunk above)`). For a loss the
 *  reader did not cause, a box that moved by what the layout moved, keeping an element of its
 *  view where it stood on screen, did not scroll (`view kept`). For every cause a box the page
 *  clamped - its content grew shorter than its scroll - did not scroll (`clamped`), and neither
 *  did one that moved a pixel or less (`under 1 px`). A selection bar's hand-off and a region's
 *  own stop scroll nothing of their own, and no box moves for a held press while its write is
 *  out.
 *
 *  Each case prints one line, `LANDING <page> <case> <width> <engine> expected=<...> rule=<...>`
 *  and, where the case has them, `named=<...>` (whether a landing named by hand agrees),
 *  `held=<...>` (each reading while the write is out) and `passed=<...>` (what the walk back passed
 *  over, by kind), then `got=<...> scrolled=<...>`. A case fails where the focus is not on the
 *  computed landing, where a box moved that the rules let not move, where the landing named by
 *  hand disagrees, where the focus left a held control, and where the page did not stage the shape
 *  the case names (`Arranged.staged`). A case a page cannot reach prints its line with why.
 *
 *  Each page's cases are in `focus-landing-inbox.ts`, `focus-landing-lists.ts` and
 *  `focus-landing-pages.ts`, in rows of a few cases each. Every row runs at 1440x900 and at 390x664,
 *  each setting the window itself as the three-engine rows of `table-controls.e2e.ts` do, so the
 *  phone project runs none of them. A row is a test of its own, budgeted at 150 s: each case
 *  seeds what it needs and opens its page, in 3 to 8 s; the Inbox's bulk Dismiss and the List's
 *  bar cases seed 38 and 44 rows, in about 5 s more; a held press's read through the shared retry
 *  policy takes up to 8 s; an offline case runs the page's own clock past the deadline at once
 *  and then waits, online again, for its write to land, a few seconds; and Save delivery settings
 *  waits out the 30 s deadline on the wall clock. So a row of four cases runs in about 20 to 60 s,
 *  the settings row in about 45 s, and a case whose focus never lands adds the 5 s its wait for
 *  the landing gives up after.
 */

import { REQUEST_DEADLINE_MS } from "../web/src/api/client";
import type { Arranged, Case, CaseContext, Held, Scene } from "./focus-landing-cases";
import { DRAWER_SCENE, INBOX_SCENE } from "./focus-landing-inbox";
import { BOARD_SCENE, LIST_SCENE } from "./focus-landing-lists";
import {
  type HeldVerdict,
  installLanding,
  judgeAfter,
  judgeHeld,
  recordBefore,
  scrolledText,
  type Verdict,
} from "./focus-landing-oracle";
import { AGENTS_SCENE, ARTIFACTS_SCENE, DELIVERY_SCENE, SEARCH_SCENE } from "./focus-landing-pages";
import { nextFrames } from "./frames";
import { REAL_STACK_SURFACES, STUBBABLE_SURFACES, type SurfaceName } from "./table-controls";
import { expect, type Page, test } from "./test";

/** The two sizes each row runs at: a desktop window wide enough for the Inbox's reading column,
 *  and a phone's. */
const SIZES = [
  { height: 900, width: 1440 },
  { height: 664, width: 390 },
] as const;

/** How far past the request deadline (`REQUEST_DEADLINE_MS`) an offline case reads the focus, on
 *  the page's own clock: room for the timers that fire at the deadline and for the frames, tasks
 *  and commits they start. A write goes late at the same mark (`SEND_DEADLINE_MS`,
 *  web/src/features/writes/keyed-write.ts), and a hold through the read after a write ends at it
 *  (`readWithinBound`, web/src/api/read-bound.ts). */
const PAST_THE_DEADLINE_MS = 2_000;

/** How long a case of the reader's own waits for the focus to rest on the computed landing: the
 *  page's rescue lands a loss a frame and a task after it, and a component's hand-off in the
 *  commit that took the control away. */
const LANDING_WAIT_MS = 5_000;

/** The eight pages these rows read, INFRA-3135's: R8's two priority pages draw the project List's
 *  bar, whose hand-offs the List's scene reads, and the priorities table's, which hands the focus
 *  as that bar does. */
type LandingSurface = Exclude<SurfaceName, "priorities page" | "priority page">;

const SCENES: Readonly<Record<LandingSurface, Scene>> = {
  Inbox: INBOX_SCENE,
  "Inbox drawer": DRAWER_SCENE,
  "project List": LIST_SCENE,
  "project Board": BOARD_SCENE,
  "Agents page": AGENTS_SCENE,
  "Delivery page": DELIVERY_SCENE,
  "search bar": SEARCH_SCENE,
  "Artifacts tab": ARTIFACTS_SCENE,
};

/** Whether `surface` is one of the eight these rows read. */
function landingSurface(surface: SurfaceName): surface is LandingSurface {
  return surface in SCENES;
}

/** What names one case: its page, its name in its row, its window and its engine. */
interface CaseName {
  readonly browserName: string;
  readonly name: string;
  readonly size: { readonly height: number; readonly width: number };
  readonly surface: SurfaceName;
}

/** One case of a row: what it runs on, and how it is named. */
type Run = CaseContext & CaseName;

/** A case as a failure names it: its page, its name and its window. */
function where({ name, size, surface }: CaseName): string {
  return `${surface}, ${name}, ${size.width}x${size.height}`;
}

/** Prints a case's one line (the file's header names its fields). */
function print({ browserName, name, size, surface }: CaseName, fields: string): void {
  console.log(`LANDING "${surface}" ${name} ${size.width} ${browserName} ${fields}`);
}

/** The line of a case that ran, from its reading after the cause and its readings while held. */
function landingLine(verdict: Verdict, arranged: Arranged, held: readonly HeldVerdict[]): string {
  const fields = [`expected=${verdict.expected}`, `rule=${verdict.rule}`];
  if (arranged.named !== undefined) {
    fields.push(
      `named=${arranged.named.name} (${verdict.named === true ? "agrees" : "disagrees"})`
    );
  }
  if (held.length > 0) {
    const readings = held.map(
      (reading) =>
        `${reading.right ? "kept" : "lost"} on ${reading.got} (expected ${reading.expected}; scrolled ${scrolledText(reading.moves)})`
    );
    fields.push(`held=${readings.join(" then ")}`);
  }
  if (verdict.passed !== "") fields.push(`passed=${verdict.passed}`);
  fields.push(`got=${verdict.got}`, `scrolled=${scrolledText(verdict.moves)}`);
  return fields.join(" ");
}

/** Whether `check` holds, waiting for it as an assertion does. */
function passes(check: Promise<unknown>): Promise<boolean> {
  return check.then(
    () => true,
    () => false
  );
}

/** Reads a held press while its write or read is out, once and again after what the reader does
 *  then, records the page again with the focus where it rests, and lets the write or read through.
 *  Adds to `wrong` each reading that finds the focus off the held control or a box moved. */
async function readHold(
  page: Page,
  held: Held,
  caseName: string,
  wrong: string[]
): Promise<HeldVerdict[]> {
  // A focused control the browser disables under the press loses its focus in a rendering update;
  // two frames hold one.
  await nextFrames(page);
  await nextFrames(page);
  const readings: HeldVerdict[] = [];
  // One read while the write is out: the press's renders have run, and nothing else moves.
  readings.push(await judgeHeld(page));
  if (held.during !== undefined) {
    await held.during();
    await nextFrames(page);
    // One read after the reader's own move, its renders run, while the write is still out.
    readings.push(await judgeHeld(page));
  }
  for (const reading of readings) {
    if (!reading.right) {
      wrong.push(
        `${caseName}: while the write is out the focus is on ${reading.got}, not on ${reading.expected}`
      );
    }
    for (const move of reading.moves) {
      if (move.reason === null) {
        wrong.push(
          `${caseName}: while the write is out ${move.box} scrolled by ${move.x},${move.y} px`
        );
      }
    }
  }
  await recordBefore(page);
  await held.release();
  if (!(await passes(held.landed()))) {
    wrong.push(`${caseName}: the page never showed what the held write did once let through`);
  }
  return readings;
}

/** Adds to `wrong` what the reading after the cause finds against the rules and the case's own
 *  claims. */
function judged(caseName: string, verdict: Verdict, arranged: Arranged, wrong: string[]): void {
  if (!verdict.right) {
    wrong.push(
      `${caseName}: the focus is on ${verdict.got}, not on ${verdict.expected} (${verdict.rule})`
    );
  }
  if (verdict.behindModal) {
    wrong.push(`${caseName}: the focus is on ${verdict.got}, behind an open modal dialog`);
  }
  if (verdict.named === false && arranged.named !== undefined) {
    wrong.push(
      `${caseName}: the computed landing ${verdict.expected} and the landing named by hand, ${arranged.named.name}, disagree`
    );
  }
  for (const move of verdict.moves) {
    if (move.reason === null) {
      wrong.push(`${caseName}: ${move.box} scrolled by ${move.x},${move.y} px`);
    }
  }
  const staged = arranged.staged;
  if (staged?.rule !== undefined && staged.rule !== verdict.rule) {
    wrong.push(
      `${caseName}: the case stages a landing by ${staged.rule}, and the page's computation chose ${verdict.rule}`
    );
  }
  for (const kind of staged?.passes ?? []) {
    if (!verdict.passed.includes(`${kind}:`)) {
      wrong.push(
        `${caseName}: the case stages a walk back past a control of kind ${kind}, and it passed ${verdict.passed || "none"}`
      );
    }
  }
}

/**
 * Runs one case and adds what it found wrong to `wrong`. It arranges the case, which seeds, opens
 * the page and puts the focus on the control; records the page; makes the cause - offline, with
 * the browser offline from just before the press - and waits for the page to answer it; reads a
 * held press while its write is out and then lets it through; offline, runs the page's own clock
 * on until the request deadline has passed since the press; waits for the focus to rest, reads
 * where it rests against the rules, and prints the case's line. Whatever stops the case, it leaves
 * the seed as the next case needs it (`Arranged.after`).
 */
async function runCase(run: Run, entry: Case, wrong: string[]): Promise<void> {
  const { page } = run;
  const caseName = where(run);
  // The offline case installs the page's own clock before it opens its page, so every timer the
  // page it opens sets follows that clock: the request and write deadlines, the read bound, the
  // press's bounds and the rescue's frames and tasks. The clock keeps the wall clock's pace until
  // the case runs it on below, and it stays installed for the rest of the row.
  if (entry.offline === true) await page.clock.install();
  const arranged = await entry.arrange(run);
  try {
    if (!(await passes(expect(arranged.control).toBeFocused()))) {
      wrong.push(`${caseName}: the control the cause takes away never held the focus`);
      print(run, "expected=(not run) got=(the control never held the focus) scrolled=(not run)");
      return;
    }
    // One record just before the cause: the focus was waited for above, and nothing moves it or
    // scrolls the page until the cause.
    await recordBefore(page);
    const browserContext = page.context();
    try {
      if (entry.offline === true) {
        await browserContext.setOffline(true);
        await page.waitForFunction(() => !navigator.onLine);
      }
      // The press's time on the page's own clock, which the offline case installed.
      const pressed = entry.offline === true ? await page.evaluate(() => Date.now()) : 0;
      await arranged.cause();
      if (!(await passes(arranged.answered()))) {
        wrong.push(`${caseName}: the page never showed what the cause does`);
      }
      const held =
        arranged.held === undefined ? [] : await readHold(page, arranged.held, caseName, wrong);
      if (entry.offline === true) {
        // The offline case's claim is a span of time: the focus is read once the request deadline
        // has passed since the press, whatever the page does at that deadline. The page's clock
        // is run on to that mark rather than waited out, and every timer due on the way fires in
        // the order it falls due.
        const spent = (await page.evaluate(() => Date.now())) - pressed;
        const left = REQUEST_DEADLINE_MS + PAST_THE_DEADLINE_MS - spent;
        if (left > 0) await page.clock.runFor(left);
      }
      if (arranged.settled !== undefined && !(await passes(arranged.settled()))) {
        wrong.push(`${caseName}: the page never showed the end of what the cause started`);
      }
      // The page's rescue runs a frame and a task after a loss, and a component's hand-off in the
      // commit that took the control away, so both are done after these frames.
      await nextFrames(page);
      await nextFrames(page);
      if (arranged.caused) {
        await passes(
          expect
            .poll(async () => (await judgeAfter(page, true, [])).right, {
              timeout: LANDING_WAIT_MS,
            })
            .toBe(true)
        );
      }
      const named = arranged.named === undefined ? [] : await arranged.named.at.elementHandles();
      // One read once the focus has rested: after a loss the reader caused, the wait above ran
      // until the focus was on the computed landing or the wait ended; after one they did not,
      // every hand-off ran in the frames above. Nothing else moves the focus or scrolls the page.
      const verdict = await judgeAfter(page, arranged.caused, named);
      print(run, landingLine(verdict, arranged, held));
      judged(caseName, verdict, arranged, wrong);
    } finally {
      if (entry.offline === true) {
        await browserContext.setOffline(false);
        // The write the press left out lands once the browser is online again, so the row ends
        // with nothing pending, and the case's clean-up (`Arranged.after`) runs after that write.
        await expect(page.getByTestId("pending-writes")).toHaveCount(0, { timeout: 60_000 });
      }
    }
  } finally {
    await arranged.after?.();
  }
}

for (const surface of [...STUBBABLE_SURFACES, ...REAL_STACK_SURFACES].filter(landingSurface)) {
  const scene = SCENES[surface];
  for (const row of scene.rows) {
    for (const size of SIZES) {
      // SLOW(>=2s): a few cases, each seeding what it needs and opening its page, a device case
      // opening a second signed-in context, an offline case waiting for the write it left out to
      // land once the browser is online again, and the settings case waiting out the deadline.
      // `@firefox` and `@webkit`: a focused control taken away fires no `focusout` in these engines,
      // so the page's focus rescue sees that loss only by watching the page (`playwright.config.ts`).
      test(`a control holding the focus on the ${surface}, taken away by ${row.what}, hands the focus where the rules say, at ${size.width} @firefox @webkit`, async ({
        browser,
        browserName,
        page,
      }) => {
        // A row's cases take 20 to 60 s together (the file's header), well inside this bound.
        test.setTimeout(150_000);
        const seeded = await (row.seed ?? scene.seed)();
        await installLanding(page);
        await page.setViewportSize(size);
        const wrong: string[] = [];
        let n = 0;
        for (const [name, entry] of Object.entries(row.cases)) {
          n += 1;
          const named: CaseName = { browserName, name, size, surface };
          if ("none" in entry) {
            print(named, `expected=(no case: ${entry.none}) got=(not run) scrolled=(not run)`);
            continue;
          }
          if (entry.only !== undefined && entry.only.width !== size.width) {
            print(named, `expected=(no case: ${entry.only.none}) got=(not run) scrolled=(not run)`);
            continue;
          }
          // A case that cannot be arranged or caused is named, and the row goes on to the next
          // case, which opens its page afresh: each case's line is what the run is read by.
          await runCase(
            { ...named, browser, n, page, scene: seeded, width: size.width },
            entry,
            wrong
          ).catch((error: unknown) => {
            const first = String(error instanceof Error ? error.message : error).split("\n")[0];
            wrong.push(`${where(named)}: could not run: ${first}`);
            print(named, `expected=(not read) got=(could not run: ${first}) scrolled=(not read)`);
          });
        }
        expect(wrong, wrong.join("; ")).toEqual([]);
      });
    }
  }
}
