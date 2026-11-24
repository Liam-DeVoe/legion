/** The shared search, sort and filter controls as the eight stubbable pages of INFRA-4948's R8
 *  (six of them INFRA-3135's R2) draw them: one row of controls per page, nothing else between a
 *  page's title and its first row but the Inbox's header line, nothing a reader presses moving
 *  that first row, exactly the controls the design gives each page, one look across two pages of
 *  a size family, the Inbox's header line one line, and the Inbox still a list of rows with a
 *  reading pane beside it.
 *
 *  Every row reads what the page draws over one fixed answer - a box's height, a computed
 *  colour, the `data-control` values in the document - so each runs against a recording rather
 *  than against a harness (packages/dispatch/README.md, "The stubbed layer"). The Agents page
 *  and the Delivery page are read in `table-controls.e2e.ts` instead: a recording freezes a
 *  session's last-seen time and the delivery window's dates, so neither has a first row to
 *  measure to ten minutes after a re-recording. Everything that needs seeded counts, a write or
 *  a reload is in that file too.
 */
import { expect, type Locator, type Page } from "@playwright/test";

import { CONTROL, type ControlName } from "../web/src/components/control-names";
import { control, controlRow, openFilterPanel } from "./filters";
import { nextFrames } from "./frames";
import { stubbed } from "./stub/stub-test";
import {
  closeControlPanel,
  drawnControlNames,
  gapBoxes,
  heightOf,
  isPhoneProject,
  openSurface,
  pickInboxField,
  pickIssueFlag,
  resetScroll,
  STUBBABLE_SURFACES,
  SURFACE_CONTROLS,
  SURFACE_SEARCH_TERM,
  type Surface,
  type SurfaceName,
  type SurfaceScene,
  seedSurfaces,
  straysBetween,
  topOf,
  widthsOf,
} from "./table-controls";
import { assertPageFits } from "./unclipped";

/** One project of six issues, each holding an open ask and a label, with three artifacts on the
 *  first, and one priority with one issue in flight under it: every surface below has a list
 *  with a first row and a narrowing that keeps it. */
const test = stubbed(seedSurfaces);

/** How far below its first reading the top of a list's first row may sit once a narrowing is
 *  on: one more line of chips, which is a 44 px control and the row's own 8 px gap
 *  (`components/ControlRow.tsx`). */
const CHIP_LINE = 52;

/** The share of the two columns the Inbox's reading pane takes, which is
 *  `DEFAULT_PANE_WIDTH` in `web/src/features/inbox/PaneDivider.tsx` - a file this change does
 *  not touch, so it is the share the pane has at the commit before it. */
const PANE_SHARE = 0.6;

/** The Inbox's header line, on the page and in the drawer: the title, the search box and the link
 *  or the close button (`web/src/features/inbox/Inbox.tsx`). */
const HEADER_LINE = "[data-inbox-header-line]";

/** Six narrowings, as the Inbox's address writes them, under which the seed keeps every row: each
 *  of its asks is a question on CORE, so the list keeps them all under every kind and CORE. */
const SIX_INBOX_NARROWINGS = "/?type=question,decision,approval,agent,reply&project=CORE";

/** The same six, as the drawer's Filter panel picks them: a field, and its option. */
const SIX_INBOX_PICKS: readonly (readonly [string, RegExp])[] = [
  ["type", /^Question,/],
  ["type", /^Decision,/],
  ["type", /^Approval,/],
  ["type", /^Agent message,/],
  ["type", /^Threads,/],
  ["project", /^CORE,/],
];

/** One part of a header line as the row reads it: what it is, and its box. */
interface HeaderLinePart {
  readonly what: string;
  readonly height: number;
  readonly left: number;
  readonly right: number;
  readonly top: number;
}

/** The header line `root` draws, read in one pass: its parts in their order, its own height, and
 *  its search box's content width beside the width of the box's placeholder in the box's own
 *  font. A heading is named by its tag, so the page's `h1` reads as one. */
async function headerLineOf(root: Locator): Promise<{
  readonly height: number;
  readonly parts: readonly HeaderLinePart[];
  readonly placeholder: string;
  readonly placeholderWidth: number;
  readonly room: number;
}> {
  const line = root.locator(HEADER_LINE);
  await expect(line, "the surface draws no one header line").toHaveCount(1);
  return line.evaluate((node) => {
    const words = (element: Element) => (element.textContent ?? "").trim();
    const parts = [...node.children].map((child) => {
      const box = child.getBoundingClientRect();
      const field = child.matches('[data-control="search-box"]')
        ? child
        : child.querySelector('[data-control="search-box"]');
      const tag = child.tagName.toLowerCase();
      const what =
        field !== null
          ? "search box"
          : /^h[1-6]$/.test(tag)
            ? `${tag} "${words(child)}"`
            : `${tag === "a" ? "link" : tag} "${words(child)}"`;
      return { height: box.height, left: box.left, right: box.right, top: box.top, what };
    });
    const field = node.querySelector<HTMLInputElement>('[data-control="search-box"]');
    if (field === null) {
      return {
        height: node.getBoundingClientRect().height,
        parts,
        placeholder: "",
        placeholderWidth: 0,
        room: 0,
      };
    }
    const style = getComputedStyle(field);
    const probe = document.createElement("span");
    probe.textContent = field.placeholder;
    probe.style.cssText = "position: absolute; visibility: hidden; white-space: pre";
    for (const property of [
      "fontFamily",
      "fontSize",
      "fontStyle",
      "fontWeight",
      "letterSpacing",
    ] as const) {
      probe.style[property] = style[property];
    }
    document.body.append(probe);
    const placeholderWidth = probe.getBoundingClientRect().width;
    probe.remove();
    return {
      height: node.getBoundingClientRect().height,
      parts,
      placeholder: field.placeholder,
      placeholderWidth,
      room:
        field.clientWidth -
        Number.parseFloat(style.paddingLeft) -
        Number.parseFloat(style.paddingRight),
    };
  });
}

/** `k-one-row`'s reading of a header line at `where`: the parts `expected` in that order from its
 *  start, each after the one before it, every part's top within 1 px of the others', the line as
 *  tall as its tallest part within 1 px, so it is one line, and room in its search box for the
 *  whole placeholder. */
async function expectOneHeaderLine(
  root: Locator,
  where: string,
  expected: readonly string[]
): Promise<void> {
  const line = await headerLineOf(root);
  const said = `${where}: the header line, ${Math.round(line.height)}px tall, holds ${line.parts
    .map(
      (part) =>
        `${part.what} at ${Math.round(part.left)}-${Math.round(part.right)}px, its top at ${Math.round(part.top)}px, ${Math.round(part.height)}px tall`
    )
    .join("; ")}`;
  expect(
    line.parts.map((part) => part.what),
    `${said}, not ${expected.join(", ")}`
  ).toEqual(expected);
  for (const [index, part] of line.parts.entries()) {
    const before = line.parts[index - 1];
    if (before === undefined) continue;
    expect(part.left, `${said}: ${part.what} is not after ${before.what}`).toBeGreaterThanOrEqual(
      before.right - 0.5
    );
  }
  const tops = line.parts.map((part) => part.top);
  expect(
    Math.max(...tops) - Math.min(...tops),
    `${said}: its parts do not stand in one line`
  ).toBeLessThanOrEqual(1);
  const tallest = Math.max(...line.parts.map((part) => part.height));
  expect(
    Math.abs(line.height - tallest),
    `${said}: the line is not the height of its tallest part, ${Math.round(tallest)}px`
  ).toBeLessThanOrEqual(1);
  expect(
    line.placeholderWidth,
    `${where}: the search box has ${Math.round(line.room)}px for its placeholder "${line.placeholder}", which takes ${Math.round(line.placeholderWidth)}px in the box's font`
  ).toBeLessThanOrEqual(line.room);
}

/** The page's header line: its `h1`, the search box, and the link to the answers page. */
const PAGE_HEADER_LINE = ['h1 "Inbox"', "search box", 'link "Answered by you"'] as const;

/** The drawer's header line: its title, the search box, and its close button. */
const DRAWER_HEADER_LINE = ['h2 "Inbox"', "search box", 'button "Close"'] as const;

// SLOW(>=2s): eight pages opened at each of this project's widths, which is sixteen page loads.
test("each page draws one row of controls, and nothing else between its title and its first row @phone @pr", async ({
  page,
  scene,
}, testInfo) => {
  // Eight pages read at every width of the project: six were measured at 26-33 s a run in an
  // issue pod, over half the project's 60 s, so it states its own budget as its sibling row below
  // does.
  test.setTimeout(120_000);
  const phone = isPhoneProject(testInfo);
  const measured: string[] = [];
  for (const size of widthsOf(page, testInfo)) {
    if (!phone) await page.setViewportSize(size);
    for (const name of STUBBABLE_SURFACES) {
      const surface = await openSurface(page, name, scene, {
        phone,
        searchTerm: SURFACE_SEARCH_TERM,
      });
      const where = `${name} at ${size.width}x${size.height}`;
      await expect(surface.row, `${where}: not exactly one control row`).toHaveCount(1);
      const height = Math.round(await heightOf(surface.row));
      measured.push(`${where}: control row ${height}px`);
      expect(height, `${where}: the control row has no height`).toBeGreaterThan(0);
      // The Inbox and its drawer draw their search box in the header line, beside the title (R8),
      // and the header line and the control row are the two places between the title and the
      // first row that may draw a search box, a panel trigger or a chip. So there the line's box
      // is the page's search box, and the strays are read from under the line.
      const headed = name === "Inbox" || name === "Inbox drawer";
      const line = surface.root.locator(HEADER_LINE);
      if (headed) {
        await expect(line, `${where}: not exactly one header line`).toHaveCount(1);
        await expect(
          control(line, CONTROL.searchBox),
          `${where}: the header line holds no search box`
        ).toHaveCount(1);
      }
      const strays = await straysBetween(
        surface.root,
        headed
          ? await line.evaluate((node) => node.getBoundingClientRect().bottom)
          : await topOf(surface.head),
        await topOf(surface.firstRow)
      );
      expect(
        strays,
        `${where} (control row ${height}px): ${strays.join("; ")} is drawn between the title and the first row and is outside the control row${headed ? " and the header line" : ""}`
      ).toEqual([]);
      // At the phone's own width the row wraps inside itself rather than widening the page.
      if (phone) await assertPageFits(page);
    }
  }
  await testInfo.attach("control row heights", {
    body: measured.join("\n"),
    contentType: "text/plain",
  });
});

// SLOW(>=2s): the same eight pages, each driven through four acts at each of this project's widths.
test("no narrowing, panel, display menu or selection moves the first row of a list @phone @pr", async ({
  page,
  scene,
}, testInfo) => {
  // Eight pages, four acts each, read at every width of the project: six were measured at 33-47 s
  // a run in an issue pod, and once over the project's 60 s when three files shared the pod's
  // browser, so the row states a budget of its own rather than borrowing what a one-page row
  // needs.
  test.setTimeout(180_000);
  const phone = isPhoneProject(testInfo);
  for (const size of widthsOf(page, testInfo)) {
    if (!phone) await page.setViewportSize(size);
    for (const name of STUBBABLE_SURFACES) {
      const surface = await openSurface(page, name, scene, {
        phone,
        searchTerm: SURFACE_SEARCH_TERM,
      });
      const where = `${name} at ${size.width}x${size.height}`;
      await resetScroll(page, surface.firstRow);
      const first = await topOf(surface.firstRow);
      const rowFirst = Math.round(await heightOf(surface.row));
      const gapFirst = await gapBoxes(surface.root, (await topOf(surface.row)) + rowFirst, first);

      await surface.narrow();
      await expect(surface.firstRow).toBeVisible();
      await resetScroll(page, surface.firstRow);
      const narrowed = await topOf(surface.firstRow);
      const rowNarrowed = Math.round(await heightOf(surface.row));
      const gapNarrowed = await gapBoxes(
        surface.root,
        (await topOf(surface.row)) + rowNarrowed,
        narrowed
      );
      expect(
        narrowed - first,
        `${where}: one narrowing moved the first row from ${Math.round(first)}px to ${Math.round(narrowed)}px, while the control row went from ${rowFirst}px to ${rowNarrowed}px; between the row and the first row, before: ${gapFirst.join("; ") || "nothing"}; after: ${gapNarrowed.join("; ") || "nothing"}`
      ).toBeLessThanOrEqual(CHIP_LINE);
      expect(
        narrowed,
        `${where}: one narrowing moved the first row up, from ${Math.round(first)}px to ${Math.round(narrowed)}px`
      ).toBeGreaterThanOrEqual(first - 0.5);

      // Each of the remaining acts draws over the page rather than in it, so each leaves the
      // top exactly where the narrowing left it, and within the same one line of the first
      // reading.
      for (const [act, run] of [
        ["the filter panel opened", surface.panel],
        ["the display menu opened", surface.display],
        ["a row selected", surface.select],
      ] as const) {
        if (run === undefined) continue;
        await run();
        await resetScroll(page, surface.firstRow);
        const after = await topOf(surface.firstRow);
        expect(
          after,
          `${where}: ${act} moved the first row from ${Math.round(narrowed)}px to ${Math.round(after)}px`
        ).toBeCloseTo(narrowed, 0);
        expect(
          Math.abs(after - first),
          `${where}: ${act} left the first row at ${Math.round(after)}px against a first reading of ${Math.round(first)}px`
        ).toBeLessThanOrEqual(CHIP_LINE);
        // Shut as a reader shuts it: the panel holds the focus once it opens, and Escape from
        // inside it closes it. Left open, it covers the display menu's trigger on a phone, and
        // the next act's press lands on the panel until one of its retries scrolls it away.
        if (run === surface.panel) {
          await page.keyboard.press("Escape");
          await expect(
            control(controlRow(surface.root), CONTROL.panelTrigger),
            `${where}: Escape left the filter panel open`
          ).toHaveAttribute("aria-expanded", "false");
        }
      }
    }
  }
});

// SLOW(>=2s): eight pages, each opened, narrowed and - where it allows one - selected.
test("each page draws every shared control its design gives it, and no other @pr", async ({
  page,
  scene,
}) => {
  for (const name of STUBBABLE_SURFACES) {
    const surface = await openSurface(page, name, scene, {
      searchTerm: SURFACE_SEARCH_TERM,
    });
    const expected = [...SURFACE_CONTROLS[name]].sort();
    // Every control the page draws as it opens, read before anything is pressed, so a page
    // pointed at a copy of a control is named here rather than found missing by the act that
    // would have driven it. The chip and the selection bar are not among them: a chip is drawn
    // only under a narrowing and a selection bar only under a selection.
    for (const wanted of expected) {
      if (wanted === CONTROL.chip || wanted === CONTROL.selectionBar) continue;
      await expect(control(surface.root, wanted), `${name} draws no ${wanted}`).not.toHaveCount(0);
    }
    await surface.narrow();
    if (surface.select !== undefined) await surface.select();
    for (const wanted of expected) {
      await expect(control(surface.root, wanted), `${name} draws no ${wanted}`).not.toHaveCount(0);
    }
    const drawn = await drawnControlNames(surface.root);
    const unexpected = drawn.filter((value) => !expected.includes(value as ControlName)).sort();
    expect(
      unexpected,
      `${name} draws ${unexpected.join(", ")}, which the design does not give it`
    ).toEqual([]);
  }
});

// R8 draws **Filter** in the bar's own look (`BarLook`), so the one trigger is read across the
// two pages already on the bar's slots, the Board and the List, by everything their two sizes
// share - its fill, its border, its type and its corners - and the Inbox, which joins the slots
// last, still shares its search box and its chip with the Board.
test("one search box and chip across the Inbox and the Board, one bar trigger across the Board and the List, and the List's compact row behaving alike @pr", async ({
  page,
  scene,
}) => {
  const drawnStyle = async (scope: Locator, which: ControlName) =>
    control(scope, which)
      .first()
      .evaluate((node) => {
        const style = getComputedStyle(node);
        return {
          background: style.backgroundColor,
          border: `${style.borderTopWidth} ${style.borderColor}`,
          font: `${style.fontFamily} ${style.fontSize} ${style.fontWeight}`,
          height: Math.round(node.getBoundingClientRect().height),
          // The leading corner: a trigger with Clear filters beside it in its group squares its
          // trailing ones, and the Board, which has no Clear filters, keeps all four round.
          radius: style.borderTopLeftRadius,
        };
      });
  const look = async (name: "Inbox" | "project Board" | "project List") => {
    const surface = await openSurface(page, name, scene);
    await surface.narrow();
    const row = controlRow(surface.root);
    return {
      chip: await drawnStyle(row, CONTROL.chip),
      // The Inbox's search box stands in its header line, the Board's in its control row (R8).
      search: await drawnStyle(
        name === "Inbox" ? surface.root.locator(HEADER_LINE) : row,
        CONTROL.searchBox
      ),
      trigger: await drawnStyle(row, CONTROL.panelTrigger),
    };
  };
  await page.setViewportSize({ height: 900, width: 1440 });
  const inbox = await look("Inbox");
  const board = await look("project Board");
  expect(board.search, "the Board's search box is not the Inbox's").toEqual(inbox.search);
  expect(board.chip, "the Board's chip is not the Inbox's").toEqual(inbox.chip);

  // The List draws the compact size, so it is read for behaviour and for the shape of each
  // accessible name rather than for size, and its trigger for all but its height.
  const listed = await look("project List");
  const { height: _boardHeight, ...boardTrigger } = board.trigger;
  const { height: _listHeight, ...listTrigger } = listed.trigger;
  expect(listTrigger, "the List's bar trigger is not the Board's").toEqual(boardTrigger);
  const row = controlRow(page.getByTestId("main-content"));
  await expect(
    control(row, CONTROL.searchBox),
    "the List's search box is no searchbox with a name of its own"
  ).toHaveAttribute("aria-label", /^Search /);
  const trigger = control(row, CONTROL.panelTrigger).first();
  await expect(trigger, "the List's panel trigger opens no panel").toHaveAttribute(
    "aria-haspopup",
    "dialog"
  );
  await expect(trigger).toHaveAttribute("aria-expanded", "false");
  const chip = control(row, CONTROL.chip).first();
  await expect(chip, "the List's chip is not named as the Board's is").toHaveAttribute(
    "aria-label",
    /^Remove .+ filter$/
  );
  // And it behaves as the Board's does: the press takes that narrowing off and the chip goes.
  const chips = await control(row, CONTROL.chip).count();
  await chip.click();
  await expect(control(row, CONTROL.chip)).toHaveCount(chips - 1);
});

test("the Inbox is rows with their marks and no table, and opening one opens the reading pane at its own width @pr", async ({
  page,
  scene,
}) => {
  await page.setViewportSize({ height: 900, width: 1440 });
  const surface = await openSurface(page, "Inbox", scene);
  const rows = page.locator("[data-inbox-row]");
  await expect(rows, "the Inbox lists no rows").not.toHaveCount(0);
  expect(
    await page.locator("table").count(),
    "the Inbox draws a table, so its items are a grid of columns rather than rows"
  ).toBe(0);
  // Each item is one row element, carrying its own marks rather than a cell per field.
  const marked = await rows.evaluateAll((nodes) =>
    nodes.map((node) => node.querySelectorAll("td").length)
  );
  expect(marked, "an Inbox row holds table cells").toEqual(marked.map(() => 0));

  // One click on the row's age selects it, which is the gesture `e2e/inbox-pane.ts` uses: it
  // is the one part of a short row that is neither the owner link nor a control.
  await rows.first().locator("time").first().click();
  const pane = page.getByTestId("inbox-pane");
  await expect(pane, "opening an item opened no reading pane").toBeVisible();
  const share = await pane.evaluate((node) => {
    const columns = node.closest<HTMLElement>("div.grid");
    const list = columns?.firstElementChild as HTMLElement | null;
    const paneWidth = node.getBoundingClientRect().width;
    const listWidth = list?.getBoundingClientRect().width ?? 0;
    return { listWidth, paneWidth, share: paneWidth / (paneWidth + listWidth) };
  });
  expect(
    share.share,
    `the reading pane takes ${share.paneWidth}px beside a ${share.listWidth}px list, a share of ${share.share.toFixed(3)} rather than the ${PANE_SHARE} it has at the commit before this change`
  ).toBeCloseTo(PANE_SHARE, 2);
  await expect(surface.row, "the control row went with the reading pane").toHaveCount(1);
});

// SLOW(>=2s): the Inbox opened twice at each of this project's widths.
test("k-one-row: the Inbox's header line is one line, its search box after the h1 and before Answered by you @phone @pr", async ({
  page,
  scene,
}, testInfo) => {
  const phone = isPhoneProject(testInfo);
  for (const size of widthsOf(page, testInfo)) {
    if (!phone) await page.setViewportSize(size);
    const surface = await openSurface(page, "Inbox", scene, { phone });
    const where = `the Inbox at ${size.width}x${size.height}`;
    await expectOneHeaderLine(surface.root, `${where}, no narrowing on`, PAGE_HEADER_LINE);
    await page.goto(SIX_INBOX_NARROWINGS);
    await expect(surface.firstRow).toBeVisible();
    await expectOneHeaderLine(surface.root, `${where}, six narrowings on`, PAGE_HEADER_LINE);
  }
});

// SLOW(>=2s): the drawer opened, and six narrowings picked in it, at each of this project's widths.
test("k-one-row: the drawer's header line is one line, its search box after its title and before its close button @phone @pr", async ({
  page,
  scene,
}, testInfo) => {
  // Six picks of the Filter panel at each width, each opening the panel and a field's popover
  // and shutting both, so the row states a budget of its own as its siblings above do.
  test.setTimeout(120_000);
  const phone = isPhoneProject(testInfo);
  for (const size of widthsOf(page, testInfo)) {
    if (!phone) await page.setViewportSize(size);
    const surface = await openSurface(page, "Inbox drawer", scene, { phone });
    const where = `the Inbox drawer at ${size.width}x${size.height}`;
    await expectOneHeaderLine(surface.root, `${where}, no narrowing on`, DRAWER_HEADER_LINE);
    for (const [field, option] of SIX_INBOX_PICKS) {
      await pickInboxField(page, field, option, surface.root);
    }
    await expect(surface.firstRow).toBeVisible();
    await expectOneHeaderLine(surface.root, `${where}, six narrowings on`, DRAWER_HEADER_LINE);
  }
});

/** The stubbable pages on the control bar's slots (INFRA-4948's T13), which R8's own rows below
 *  read: a page joins this list as it moves onto the slots, so each row reads every page that
 *  has moved and none that has not. The Inbox, its drawer, the search bar and the Artifacts tab
 *  join it as they move. */
const BAR_PAGES: readonly SurfaceName[] = [
  "project List",
  "project Board",
  "priorities page",
  "priority page",
];

/** The bar's own width under which it takes its narrow layout (`components/ControlRow.tsx`). */
const NARROW_BELOW = 640;

/** The name of a bar's `+N more` chip, `<N> more filters`, and of the panel it opens. */
const MORE_CHIPS = /^\d+ more filters?$/;

/** A chip's remove control, as `filterChips` names every one. */
const REMOVE_CHIP = /^Remove .+ filter$/;

/** A computed background no fill paints. */
const NO_FILL = /^(transparent|rgba\(0, 0, 0, 0\))$/;

/** The three tables the priorities page draws, each region under a bar of its own: the
 *  priorities table's, and the project List's above each issues table. */
const PRIORITIES_TABLES = [
  "Priorities",
  "In flight under no priority",
  "Priority work in backlog or icebox",
] as const;

/** The narrowings each page is read under, as its address writes them, in the order a row takes
 *  them: the first one, two, three or all six, each one chip. Every page keeps its bar under all
 *  six, and the seed keeps rows under the first three. The Board narrows by every label it names
 *  at once and each seeded issue carries one, so its last two leave it no card; a priority's own
 *  issue has no ask, so `needs-you` leaves its table no row; no reading of a bar needs one. */
const NARROWINGS: Partial<Record<SurfaceName, readonly string[]>> = {
  "project List": [
    "needs-you=1",
    "label=frontend",
    "unclaimed=1",
    "unread=1",
    "status=todo",
    "f.key=CORE",
  ],
  "project Board": [
    "needs-you=1",
    "q=Rollout",
    "unclaimed=1",
    "unread=1",
    "label=frontend",
    "label=backend",
  ],
  "priorities page": [
    "p.f.owner=alice",
    "p.f.waiting=0",
    "p.f.in-flight=1",
    "p.f.parked=0",
    "p.f.closed=0",
    "p.f.title=books",
  ],
  "priority page": [
    "w.unclaimed=1",
    "w.status=in_progress",
    "w.f.key=BOOKS",
    "w.f.title=Audit",
    "needs-you=1",
    "unread=1",
  ],
};

/** The page `name` with its first `count` narrowings on. */
function narrowedAddress(name: SurfaceName, scene: SurfaceScene, count: number): string {
  const path =
    name === "priorities page"
      ? "/priorities"
      : name === "priority page"
        ? `/priorities/${scene.priority ?? ""}`
        : `/projects/${scene.project}/issues`;
  return `${path}?${(NARROWINGS[name] ?? []).slice(0, count).join("&")}`;
}

/** One control of a bar as a row reads it: what it is (its `data-control`, or `clear` for Clear
 *  filters), its accessible name, the words it shows - a badge's number among them - and its
 *  paint and box. */
interface BarControl {
  readonly background: string;
  readonly border: number;
  /** Whether it shares the box it stands in with the bar's trigger: the trigger's group. */
  readonly besideTrigger: boolean;
  readonly button: boolean;
  readonly control: string;
  readonly height: number;
  readonly name: string;
  readonly width: number;
  readonly words: string;
}

/** A bar read in one pass: its box, the controls of its line in their order, and its search
 *  box's room beside the width of its placeholder in the box's own font. */
interface BarReading {
  readonly controls: readonly BarControl[];
  readonly height: number;
  readonly placeholder: string;
  readonly placeholderWidth: number;
  readonly room: number;
  readonly width: number;
}

async function readBar(bar: Locator): Promise<BarReading> {
  return bar.evaluate((node) => {
    const trigger = node.querySelector('[data-control="panel-trigger"]');
    const controls = [...node.querySelectorAll<HTMLElement>("input, button, a[href]")]
      .filter((element) =>
        element.checkVisibility({ opacityProperty: true, visibilityProperty: true })
      )
      .map((element) => {
        const style = getComputedStyle(element);
        const box = element.getBoundingClientRect();
        // The name a screen reader hears: its label, else its words without what it hides.
        const spoken = element.cloneNode(true) as HTMLElement;
        for (const hidden of spoken.querySelectorAll('[aria-hidden="true"]')) hidden.remove();
        const name = (
          element.getAttribute("aria-label") ??
          element.getAttribute("title") ??
          spoken.textContent ??
          ""
        ).trim();
        return {
          background: style.backgroundColor,
          border:
            Number.parseFloat(style.borderTopWidth) +
            Number.parseFloat(style.borderRightWidth) +
            Number.parseFloat(style.borderBottomWidth) +
            Number.parseFloat(style.borderLeftWidth),
          besideTrigger:
            trigger !== null &&
            trigger !== element &&
            trigger.parentElement === element.parentElement,
          button: element.tagName === "BUTTON",
          control:
            element.getAttribute("data-control") ??
            (name === "Clear filters" ? "clear" : element.tagName.toLowerCase()),
          height: box.height,
          name,
          width: box.width,
          words: (element.textContent ?? "").trim(),
        };
      });
    const field = node.querySelector<HTMLInputElement>('input[data-control="search-box"]');
    let placeholderWidth = 0;
    let room = 0;
    if (field !== null) {
      const style = getComputedStyle(field);
      const probe = document.createElement("span");
      probe.textContent = field.placeholder;
      probe.style.cssText = "position: absolute; visibility: hidden; white-space: pre";
      for (const property of [
        "fontFamily",
        "fontSize",
        "fontStyle",
        "fontWeight",
        "letterSpacing",
      ] as const) {
        probe.style[property] = style[property];
      }
      document.body.append(probe);
      placeholderWidth = probe.getBoundingClientRect().width;
      probe.remove();
      room =
        field.clientWidth -
        Number.parseFloat(style.paddingLeft) -
        Number.parseFloat(style.paddingRight);
    }
    const box = node.getBoundingClientRect();
    return {
      controls,
      height: box.height,
      placeholder: field?.placeholder ?? "",
      placeholderWidth,
      room,
      width: box.width,
    };
  });
}

/** A bar as a failure names it: its width and height, and each control with its height. */
function saidOf(reading: BarReading): string {
  return `a ${Math.round(reading.width)}px bar ${Math.round(reading.height)}px tall holding ${reading.controls
    .map((one) => `${one.control} "${one.name}" (${Math.round(one.height)}px)`)
    .join(", ")}`;
}

/** Waits until the bar of `name` stands for the narrowings its address put on: **Filters** says
 *  their number, or - on the priorities table's bar, which has no trigger - its chips are drawn.
 *  The chips line reads its chips' widths after the commit that drew them and draws again by that
 *  reading, so two frames more hold both. */
async function settledBar(page: Page, name: SurfaceName, bar: Locator, count: number) {
  if (name === "priorities page") await expect(control(bar, CONTROL.chip).first()).toBeVisible();
  else await expect(control(bar, CONTROL.panelTrigger)).toHaveAccessibleName(`Filters · ${count}`);
  await nextFrames(page);
  await nextFrames(page);
}

/** The chips a wide bar stands for: the ones its line draws, then the ones its `+N more` lists in
 *  the panel it opens, read once the line and `N` add up to `count`; `N` itself is read against
 *  the panel. */
async function chipsOfBar(
  page: Page,
  bar: Locator,
  count: number,
  where: string
): Promise<{ line: string[]; listed: string[] }> {
  const line = bar.locator("[data-chip-strip]").getByRole("button", { name: REMOVE_CHIP });
  const more = bar.getByRole("button", { name: MORE_CHIPS });
  const held = async () =>
    (await more.count()) === 0
      ? 0
      : Number.parseInt((await more.getAttribute("aria-label")) ?? "0", 10);
  await expect
    .poll(async () => (await line.count()) + (await held()), {
      message: `${where}: the chips line and its +N more stand for another number than the ${count} narrowings on`,
    })
    .toBe(count);
  const names = (chips: Locator) =>
    chips.evaluateAll((all) => all.map((chip) => chip.getAttribute("aria-label") ?? ""));
  const drawn = await names(line);
  const n = await held();
  if (n === 0) return { line: drawn, listed: [] };
  await more.click();
  const panel = page.getByRole("dialog", { name: MORE_CHIPS });
  await expect(panel, `${where}: +N more opened no panel`).toBeVisible();
  const listed = await names(panel.getByRole("button", { name: REMOVE_CHIP }));
  expect(listed.length, `${where}: +${n} more lists ${listed.join(", ")}`).toBe(n);
  await page.keyboard.press("Escape");
  await expect(panel, `${where}: Escape left the panel of +N more open`).toHaveCount(0);
  return { line: drawn, listed };
}

/** `k-one-row`'s reading of one bar at `where`: one line, as tall as its tallest control within
 *  1 px; its first control the search box, with room for its whole placeholder; the page not
 *  scrolled sideways; and in the narrow layout no chip in its line - but the priorities table's one
 *  `+N more` - and **Filters**, the display menu and **More** showing no words but a badge's
 *  number, each with its name. */
async function expectOneBar(page: Page, name: SurfaceName, bar: Locator, where: string) {
  const reading = await readBar(bar);
  const said = saidOf(reading);
  const tallest = Math.max(...reading.controls.map((one) => one.height));
  expect(
    Math.abs(reading.height - tallest),
    `${where}: ${said}, so it is not one line as tall as its tallest control`
  ).toBeLessThanOrEqual(1);
  expect(reading.controls[0]?.control, `${where}: ${said}, its search box not first`).toBe(
    CONTROL.searchBox
  );
  expect(
    reading.placeholderWidth,
    `${where}: the search box has ${Math.round(reading.room)}px for its placeholder "${reading.placeholder}", which takes ${Math.round(reading.placeholderWidth)}px in the box's font`
  ).toBeLessThanOrEqual(reading.room);
  await assertPageFits(page);
  if (reading.width >= NARROW_BELOW) return;
  const chips = reading.controls.filter((one) => one.control === CONTROL.chip);
  const kept = name === "priorities page" ? chips.filter((one) => MORE_CHIPS.test(one.name)) : [];
  expect(
    chips.filter((one) => !kept.includes(one)).map((one) => one.name),
    `${where}: ${said}, chips in the narrow bar's line`
  ).toEqual([]);
  expect(kept.length, `${where}: ${said}, more than one +N more`).toBeLessThanOrEqual(1);
  for (const one of reading.controls) {
    if (
      one.control !== CONTROL.panelTrigger &&
      one.control !== CONTROL.displayMenu &&
      one.control !== CONTROL.moreMenu
    ) {
      continue;
    }
    expect(one.name, `${where}: ${said}, ${one.control} has no name`).not.toBe("");
    expect(one.words, `${where}: ${said}, ${one.control} shows words in the narrow layout`).toMatch(
      one.control === CONTROL.panelTrigger ? /^\d*$/ : /^$/
    );
  }
}

// SLOW(>=2s): four pages at each of this project's widths, each read with no narrowing and with six.
test("k-one-row: each page on the bar's slots draws one bar of one line above its table, its search box first and its placeholder whole @phone @pr", async ({
  page,
  scene,
}, testInfo) => {
  test.setTimeout(180_000);
  const phone = isPhoneProject(testInfo);
  for (const size of widthsOf(page, testInfo)) {
    if (!phone) await page.setViewportSize(size);
    for (const name of BAR_PAGES) {
      const surface = await openSurface(page, name, scene, { phone });
      const where = `${name} at ${size.width}x${size.height}`;
      await expect(
        surface.row,
        `${where}: not exactly one control bar above its table`
      ).toHaveCount(1);
      if (name === "priorities page") {
        for (const region of PRIORITIES_TABLES) {
          await expect(
            controlRow(surface.root.getByRole("region", { exact: true, name: region })),
            `${where}: not exactly one control bar above the table ${region}`
          ).toHaveCount(1);
        }
      }
      const strays = await straysBetween(
        surface.root,
        await topOf(surface.head),
        await topOf(surface.firstRow)
      );
      expect(
        strays,
        `${where}: ${strays.join("; ")} is drawn between the title and the first row, outside the bar`
      ).toEqual([]);
      await expectOneBar(page, name, surface.row, `${where}, no narrowing on`);
      await page.goto(narrowedAddress(name, scene, 6));
      await settledBar(page, name, surface.row, 6);
      await expectOneBar(page, name, surface.row, `${where}, six narrowings on`);
      if (phone && name === "priorities page") {
        // A bar with chips and no trigger keeps one `+N more` in its narrow line, which holds them.
        await page.goto(narrowedAddress(name, scene, 2));
        await settledBar(page, name, surface.row, 2);
        const line = (await readBar(surface.row)).controls.filter(
          (one) => one.control === CONTROL.chip
        );
        expect(
          line.map((one) => one.name),
          `${where}, two narrowings on: the narrow bar's chips`
        ).toEqual(["2 more filters"]);
      }
    }
  }
});

/** The controls the Design's table gives each page's bar with no narrowing on, each by what it is
 *  and its name, in the bar's order, in the layout its width gives it: the priorities table's
 *  **New priority** is an action of the line in the wide layout and an item of **More** in the
 *  narrow one, and no other page has an action. */
function barDesign(name: SurfaceName, narrow: boolean): string[] {
  if (name === "project Board") return ['search-box "Search issues"', 'panel-trigger "Filters"'];
  if (name === "priorities page") {
    return narrow
      ? [
          'search-box "Search priorities"',
          'columns-menu "Columns"',
          'display-menu "View"',
          'more-menu "More"',
        ]
      : [
          'search-box "Search priorities"',
          'bar-action "New priority"',
          'columns-menu "Columns"',
          'display-menu "View"',
        ];
  }
  return [
    'search-box "Search issues"',
    'panel-trigger "Filters"',
    'columns-menu "Columns"',
    'display-menu "View"',
  ];
}

// SLOW(>=2s): four pages, each read at rest and under one, two, three or six narrowings.
test("k-bar: each page on the bar's slots draws its design's controls and no other, quiet at rest, with Filters' count, its chips and its panel's list @phone @pr", async ({
  page,
  scene,
}, testInfo) => {
  test.setTimeout(240_000);
  const phone = isPhoneProject(testInfo);
  const size = phone
    ? (page.viewportSize() ?? { height: 664, width: 390 })
    : { height: 800, width: 1280 };
  if (!phone) await page.setViewportSize(size);
  for (const name of BAR_PAGES) {
    const where = `${name} at ${size.width}x${size.height}`;
    const surface: Surface = await openSurface(page, name, scene, { phone });
    const bar = surface.row;
    // At rest: no narrowing, nothing hovered, nothing focused.
    await page.mouse.move(size.width - 1, size.height - 1);
    await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
    const rest = await readBar(bar);
    const narrow = rest.width < NARROW_BELOW;
    const said = `${where}: ${saidOf(rest)}`;
    expect(
      rest.controls.map((one) => `${one.control} "${one.name}"`),
      `${said}, not its design's controls in the ${narrow ? "narrow" : "wide"} layout`
    ).toEqual(barDesign(name, narrow));
    const painted = rest.controls.filter(
      (one) => one.button && (one.border !== 0 || !NO_FILL.test(one.background))
    );
    expect(
      painted.map(
        (one) => `${one.control} "${one.name}": ${one.border}px border, ${one.background}`
      ),
      `${said}, buttons with a border or a fill at rest`
    ).toEqual([]);
    if (phone) {
      const small = rest.controls.filter(
        (one) => one.button && (one.width < 43.5 || one.height < 43.5)
      );
      expect(
        small.map(
          (one) =>
            `${one.control} "${one.name}" ${Math.round(one.width)}x${Math.round(one.height)}px`
        ),
        `${said}, buttons under the 44 px floor`
      ).toEqual([]);
    }
    if (narrow && name === "priorities page") {
      await control(bar, CONTROL.moreMenu).click();
      await expect(
        page.getByRole("menuitem", { name: "New priority" }),
        `${where}: More holds no New priority`
      ).toBeVisible();
      await page.keyboard.press("Escape");
    }
    if (name === "priorities page") {
      // No trigger: the chips stand in the line from the first, behind one `+N more`.
      if (narrow) continue;
      await page.goto(narrowedAddress(name, scene, 6));
      await settledBar(page, name, bar, 6);
      const { line, listed } = await chipsOfBar(page, bar, 6, `${where}, six narrowings on`);
      expect(new Set([...line, ...listed]).size, `${where}: six chips, each once`).toBe(6);
      continue;
    }
    const trigger = control(bar, CONTROL.panelTrigger);

    // One narrowing: Filters on a tint, its count in its name, and - on the List's bar in the wide
    // layout - Clear filters beside it in its group, a button of its own. The Board has none (D24).
    await page.goto(narrowedAddress(name, scene, 1));
    await settledBar(page, name, bar, 1);
    const one = await readBar(bar);
    const tinted = one.controls.find((each) => each.control === CONTROL.panelTrigger);
    expect(tinted?.background, `${where}, one narrowing on: Filters has no fill`).not.toMatch(
      NO_FILL
    );
    const clear = one.controls.filter((each) => each.control === "clear");
    if (narrow || name === "project Board") {
      expect(clear.length, `${where}, one narrowing on: ${saidOf(one)}, a Clear filters`).toBe(0);
    } else {
      const at = one.controls.findIndex((each) => each.control === CONTROL.panelTrigger);
      expect(
        one.controls[at + 1]?.control,
        `${where}, one narrowing on: ${saidOf(one)}, no Clear filters after Filters`
      ).toBe("clear");
      expect(clear[0]?.besideTrigger, `${where}: Clear filters is not in Filters' group`).toBe(
        true
      );
    }

    // The List's flag and column filter: counted as two, each listed at the panel's top with the
    // control that removes it.
    if (name === "project List") {
      await page.goto(`/projects/${scene.project}/issues?needs-you=1&label=frontend`);
      await settledBar(page, name, bar, 2);
      const panel = await openFilterPanel(page, surface.root);
      for (const chip of ["Needs you", "Label: frontend"]) {
        await expect(
          panel.getByRole("button", { exact: true, name: `Remove ${chip} filter` }),
          `${where}: the Filters panel does not list ${chip}`
        ).toHaveCount(1);
      }
      await closeControlPanel(surface.root);
    }

    // Six at 1280x800, three at the phone width: in the wide layout the chips that do not fit
    // stand behind one `+N more`, and in the narrow one none stands in the line, Filters' badge
    // says their number and no Clear filters is drawn; at every width the panel lists them all.
    const count = phone ? 3 : 6;
    await page.goto(narrowedAddress(name, scene, count));
    await settledBar(page, name, bar, count);
    const many = await readBar(bar);
    const at = `${where}, ${count} narrowings on`;
    if (many.width < NARROW_BELOW) {
      expect(
        many.controls.find((each) => each.control === CONTROL.panelTrigger)?.words,
        `${at}: ${saidOf(many)}, Filters' badge`
      ).toBe(String(count));
      expect(
        many.controls.filter((each) => each.control === CONTROL.chip || each.control === "clear"),
        `${at}: ${saidOf(many)}, chips or Clear filters in the narrow line`
      ).toEqual([]);
    } else {
      const { line, listed } = await chipsOfBar(page, bar, count, at);
      expect(new Set([...line, ...listed]).size, `${at}: ${count} chips, each once`).toBe(count);
    }
    const panel = await openFilterPanel(page, surface.root);
    await expect(
      panel.getByRole("button", { name: REMOVE_CHIP }),
      `${at}: the Filters panel's top does not list every narrowing with its remove control`
    ).toHaveCount(count);
    await closeControlPanel(surface.root);
    await expect(trigger).toHaveAttribute("aria-expanded", "false");
  }
});

// SLOW(>=2s): four pages at each of this project's widths, each driven through four acts.
test("k-no-growth: no narrowing, panel, display menu or selection moves the first row of a page on the bar's slots, in the layout its bar's width gives @phone @pr", async ({
  page,
  scene,
}, testInfo) => {
  test.setTimeout(240_000);
  const phone = isPhoneProject(testInfo);
  for (const size of widthsOf(page, testInfo)) {
    if (!phone) await page.setViewportSize(size);
    for (const name of BAR_PAGES) {
      const surface = await openSurface(page, name, scene, { phone });
      const width = await surface.row.evaluate((node) => node.getBoundingClientRect().width);
      const where = `${name} at ${size.width}x${size.height}, its bar ${Math.round(width)}px wide (the ${width < NARROW_BELOW ? "narrow" : "wide"} layout)`;
      await resetScroll(page, surface.firstRow);
      const first = await topOf(surface.firstRow);
      // One narrowing that draws a chip: the List's and the Board's Needs you, a priority's
      // Unclaimed (its one issue has no ask), and on the priorities table, which has no trigger,
      // the Owner pick its column filter writes into the address.
      const narrowing =
        name === "priorities page"
          ? async () => {
              await page.goto(narrowedAddress(name, scene, 1));
              await settledBar(page, name, surface.row, 1);
            }
          : name === "priority page"
            ? () => pickIssueFlag(page, "Unclaimed")
            : surface.narrow;
      for (const [act, run] of [
        ["one narrowing applied", narrowing],
        ["the filter panel opened", surface.panel],
        ["the display menu opened", surface.display],
        ["a row selected", surface.select],
      ] as const) {
        if (run === undefined) continue;
        await run();
        await expect(surface.firstRow, `${where}: ${act} left no first row`).toBeVisible();
        await resetScroll(page, surface.firstRow);
        const after = await topOf(surface.firstRow);
        expect(
          Math.abs(after - first),
          `${where}: ${act} moved the first row from ${Math.round(first)}px to ${Math.round(after)}px`
        ).toBeLessThanOrEqual(1);
        if (run === surface.panel) {
          await page.keyboard.press("Escape");
          await expect(
            control(surface.row, CONTROL.panelTrigger),
            `${where}: Escape left the filter panel open`
          ).toHaveAttribute("aria-expanded", "false");
        }
      }
    }
  }
});
