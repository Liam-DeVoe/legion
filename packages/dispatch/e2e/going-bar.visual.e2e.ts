/** The command bar in the visual review: one row per state of the bar's design (design v3 of
 *  dispatch://INFRA-1588, "How it is judged"), each driving the bar into that state through a run
 *  of the fake models and comparing what it draws against a committed baseline, at 1440 x 900,
 *  over the page and, for states 2, 3, 4, 6 and 10, in the panel at the side.
 *
 *  WHERE IT RUNS. In the `visual-bar` project (e2e/playwright.config.ts), inside the pinned
 *  Playwright image that `e2e/visual-run.sh` starts, beside `e2e/visual.e2e.ts`'s pages and under
 *  that file's rules of place: the shots are only the same rendering there, every row skips
 *  itself where `DISPATCH_E2E_VISUAL_SEED` is unset, and nothing imported here reaches `./psql`,
 *  which the image does not carry. The bar's locators come from `e2e/going-locators.ts` and the
 *  fake models' scripts from `e2e/fake-models-helpers.ts`, both written for that. The stack that
 *  script starts serves the fake writing model and the fake decision model
 *  (`DISPATCH_E2E_MODELS=fake`, e2e/harness-models.ts), so every row also skips itself where it
 *  does not.
 *
 *  ONE SEED, AND NO ROW SEEDS. The issues these rows read, plan on and change are the review
 *  corpus's own (`seedBarIssues`, e2e/review-seed.ts): seven issues of project OPS, bob's, with
 *  no ask among them, which no page of the shot set draws, and the dome routed to the
 *  workspace's Planner, so a message to the Planner is placed on the dome. A row reads their
 *  keys from the seed file and scripts the fake models with them; it writes nothing before it
 *  opens the bar.
 *
 *  WHAT A ROW LEAVES. A row signs in as a login of its own (`bar-<state>`), so its runs, its
 *  conversations and History are its own: no other row's History lists them, and a plan that
 *  waits reaches that login's Inbox alone, never alice's, whose Inbox the pages' rows shoot. And
 *  each row ends what it starts: a run it holds working is stopped, a plan it leaves waiting is
 *  discarded, and a plan it applies is undone. What stays is a message on an issue of OPS, which
 *  Undo does not take back, and the one issue the filing row files, which no other row reads:
 *  the chart and the table read the seven seeded issues by key, and its words - a weather
 *  station on the roof - match none that another row searches for, so it is no hit of state
 *  1's search or of state 3's search step whichever runs first. So the rows draw the same in the
 *  normal order and in reverse (`e2e/visual-order.ts`), and the pages' rows draw the same
 *  whichever of the three projects runs first.
 *
 *  WHAT EACH ROW DOES THAT THE PAGES' ROWS DO NOT. A page row signs in, navigates and waits; a
 *  bar row drives the bar, which is the state under review: it presses Control+K, types, presses
 *  Enter, Escape and Control+Shift+K, and clicks the buttons the design names (Keep at the side,
 *  New, Close, History, Discard, Undo, the chip). A click leaves the pointer over what it
 *  clicked, and a hovered control draws in another colour, so the pointer is moved to the
 *  window's top left corner before each shot, where the bar's backdrop or the rail's own
 *  background is. The caret is hidden by `toHaveScreenshot`'s default (`caret: "hide"`), and a
 *  focus ring is the one the keys under review leave, which is the same on every run.
 *
 *  WHAT WOULD DRIFT BETWEEN TWO RUNS OF ONE STATE, and what is done about each:
 *
 *  - The clock. Each row installs the page's clock as `e2e/visual.e2e.ts` does
 *    (`page.clock.install`, not `pauseAt`), so the bar's own waits - the pause before the short
 *    answer, the decision's 500 ms, a refetch's interval - fire as they do for a person. It is
 *    installed at the row's own start rather than a minute after the seed, because what these
 *    rows draw is what the row wrote, at the server's own time: History and the Inbox stamp a
 *    conversation with the moment the server stored it, so a page clock pinned to the seed would
 *    read the gap between the seed and the row ("in 9 minutes"), which moves with the run's pace.
 *    At the row's start it reads "just now". Two states draw a time of day, which no install at
 *    the run's own time can hold still from one day to the next: a block's caption ("as of
 *    4:40 PM") and an applied plan's title ("Applied 3 changes · 4:40 PM"). Those two rows
 *    install the clock at 16:30 UTC of one fixed day instead (`fixedDay`), and set it forward
 *    to a minute of their own just before the Enter whose moment the shot draws
 *    (`setSystemTime`, which fires no timer), and the project draws times in en-US and in UTC.
 *    Only ever forward: the page's refresh scheduler reads a fetch that started later than a
 *    refresh was asked for as having answered it (`web/src/api/query-refresh.ts`), so a clock
 *    set back would keep an item chip on the title it read before a change.
 *  - Times. The `<time>` elements are masked, as `e2e/visual.e2e.ts` masks them, and each reads
 *    "just now" here, a box that holds its width. The words that count seconds from the
 *    person's Enter - the timer beside "Thinking…", the newest step line's timer and the time in
 *    the "Worked for 2 s · read 2 items" line the steps fold into - are masked too, each held to
 *    one width while the shot is taken (`e2e/going-bar.visual.css`, which says why), so the shot
 *    shows where each stands and the row reads the words around them with an assertion. The
 *    rail's Inbox entry is masked, for the reason `e2e/visual.e2e.ts` gives.
 *  - The run's pace. A run that worked for less than a second folds its steps into "Read 2
 *    items", with no time, and one that worked longer into "Worked for 2 s · read 2 items", so a
 *    run of the fake models, which answer at once, could draw either. Each run whose steps a shot
 *    draws folded takes one slow turn (`slow`), so it works for more than a second on every run.
 *  - Animations. `reducedMotion: "reduce"` turns the bar's shimmer, pulse and spinner off (each
 *    is a `motion-safe:` class), and `toHaveScreenshot` disables what is left, as it does for
 *    the pages.
 *  - The page under the bar. Every row opens the bar over project OPS's Documents tab, which
 *    holds no document and draws no time, and waits for it as `waitForRouteSettled` waits for a
 *    page of the shot set. Each shot is the window, not the full page: the bar is fixed to it.
 *  - The models. Each row scripts both fakes before it opens the bar - the writing model's runs,
 *    each by the words that start it, and its short answer, and the decision model's line - so a
 *    row draws its script's state whichever row ran before it. The comment above each row names
 *    the script that makes its state.
 *
 *  State 13's second half, a Dispatch with no answering model, is not drawn here: the bar reads
 *  whether there is one from the server's own settings, and the lane's one stack serves the fake
 *  models to every row. `e2e/going-unconfigured.e2e.ts` reads that sentence on the default
 *  stack, which has none.
 *
 *  The bounds are `e2e/visual.e2e.ts`'s - `threshold: 0`, `maxDiffPixels: 20` - which were
 *  measured on the pages; these rows' own drift has no measurement yet, and their first runs in
 *  the container are where it comes from. */
import { fileURLToPath } from "node:url";
import { type Browser, expect, type Locator, type Page, test } from "@playwright/test";

import { setLiveSessions } from "./agents";
import type { ChatReply, ChatRunScript, ChatTurn } from "./fake-chat-model";
import {
  callTool,
  decided,
  endlessStream,
  findTools,
  modelsLane,
  proposePlan,
  prose,
  refusedWith,
  say,
  scriptChat,
  scriptDecision,
} from "./fake-models-helpers";
import {
  ANSWER_ENDS_MS,
  answerPanel,
  answerText,
  askTheAgent,
  bar,
  lineOnTop,
  newestRun,
  openBar,
  planCard,
  planRows,
  RUN_STEPS_MS,
  searchBox,
  sidePanel,
} from "./going-locators";
import {
  type BarSeed,
  type ReviewedRoute,
  railInboxLink,
  readVisualSeed,
  type VisualSeed,
  waitForRouteSettled,
} from "./review-routes";
import { sessionIds } from "./session-ids";
import { asUser } from "./users";
import { inVisualOrder, visualOrder } from "./visual-order";
import { workspaceSessions } from "./workspace";

/** The JSON `e2e/visual-seed.ts` wrote on the host. Unset says this run is not the containerised
 *  one, which is every run but `e2e/visual-run.sh`'s. */
const seedPath = process.env.DISPATCH_E2E_VISUAL_SEED;

const SKIP_REASON =
  "The bar's states are compared against baselines, which are only the same rendering inside " +
  "the pinned Playwright image: run `e2e/visual-run.sh` from packages/dispatch, which seeds on " +
  "the host and takes every shot in that container. DISPATCH_E2E_VISUAL_SEED is unset.";

/** The style sheet applied while a shot is taken, which holds each of the words that count
 *  seconds to one width, so the mask over it is the same box on every run. */
const heldWhileShot = fileURLToPath(new URL("./going-bar.visual.css", import.meta.url));

/** 16:30 UTC on a day before any run of this lane: where a row whose shot draws a time of day
 *  installs the page's clock, ahead of the minutes it then sets the clock forward to. */
const FIXED_DAY = Date.parse("2026-11-02T16:30:00Z");
/** The minutes those rows set the clock to just before the Enter whose moment they draw, so
 *  that time reads "4:40 PM" or "4:45 PM" on every run. */
const AT_4_40_PM = Date.parse("2026-11-02T16:40:00Z");
const AT_4_45_PM = Date.parse("2026-11-02T16:45:00Z");

/** The page every row opens the bar over: project OPS's Documents tab, which holds no document
 *  and draws no time, so the page under the bar is the same in every shot. It is no page of the
 *  shot set, so it carries no layer; its one fact is its empty list. */
const pageUnderTheBar: ReviewedRoute = {
  kind: "reviewed",
  layers: [],
  name: "bar-page",
  path: (seed) => `/projects/${seed.bar.project}/documents`,
  pattern: "/projects/:key/documents",
  ready: (page) => [page.getByRole("region", { name: "Project documents empty state" })],
};

/** The short answer the fake writing model gives a pause in every row that types words. It links
 *  the telescope mount by the href the search's hit of it carries, so the answer panel draws the
 *  mount's key, which the server keeps as words, as the mount's chip. */
function shortAnswer(issues: BarSeed): ChatReply {
  return prose(`The telescope's mount waits for its calibration, ${naming(issues.mount)}.`);
}

// The live registry, put back after Playwright's own `globalSetup` cleared it, as
// `e2e/visual.e2e.ts` does and for its reason: the rail these shots draw counts the agents'
// replies, and in the reverse order this project's rows run before that file's.
test.beforeAll(async () => {
  if (seedPath === undefined) return;
  await setLiveSessions(workspaceSessions);
});

/** Scripts both fakes for one row: the writing model's `runs`, each by the words that start it,
 *  and `shortAnswer` for a pause; and the decision model's `line`, `none` for Ask. */
async function script(
  seed: VisualSeed,
  runs: readonly ChatRunScript[],
  line = "none"
): Promise<void> {
  await scriptChat({ default: shortAnswer(seed.bar), runs });
  await scriptDecision({ default: decided(line, 0.99) });
}

/** Opens `pageUnderTheBar`, waits for it to settle, and opens the bar over it. */
async function openTheBar(page: Page, seed: VisualSeed): Promise<void> {
  await page.goto(pageUnderTheBar.path(seed));
  await waitForRouteSettled(page, pageUnderTheBar);
  await openBar(page);
}

/** Compares the window against the baseline `name`, with the pointer off every control. Soft,
 *  as `e2e/visual.e2e.ts`'s shots are, so a row whose first shot differs still takes the rest.
 *  Each time of day and each count of seconds is masked. */
async function shoot(page: Page, name: string): Promise<void> {
  await page.mouse.move(0, 0);
  await expect.soft(page).toHaveScreenshot(`${name}.png`, {
    mask: [
      page.locator("time"),
      railInboxLink(page),
      page.getByTestId("going-thinking-timer"),
      page.getByTestId("going-step-timer"),
      page.getByTestId("going-steps-fold-time"),
    ],
    maxDiffPixels: 20,
    stylePath: heldWhileShot,
    threshold: 0,
  });
}

/** A turn that reads `issue` with `dispatch_read`. */
function read(issue: string): ChatTurn {
  return callTool("dispatch_read", { issue });
}

/** `turn`, written in one piece with 400 ms between its frames. A turn is four frames at least -
 *  the role, its words or its calls, the finish and the count of tokens - so it takes 1.2 s or
 *  more, and the run it is part of works for more than a second: the line its steps fold into
 *  then says how long, "Worked for 2 s · read 2 items", on every run (see the run's pace above). */
function slow(turn: ChatTurn): ChatTurn {
  return { ...turn, chunk: 4096, delayMs: 400 };
}

/** An answer naming `issue` by a link, which the bar draws as the issue's chip. */
function naming(issue: string): string {
  return `[${issue}](/issues/${issue})`;
}

/** Waits until every item chip under `where` has its status and title, which it reads after it
 *  draws its key. */
async function chipsRead(where: Locator, count: number): Promise<void> {
  await expect(where.getByTestId("item-chip")).toHaveCount(count);
  await expect(where.getByTestId("item-chip-status")).toHaveCount(count);
}

/** Moves the conversation to the panel at the side with its key. */
async function keepAtTheSide(page: Page): Promise<void> {
  await page.keyboard.press("Control+Shift+K");
  await expect(bar(page)).toHaveCount(0);
  await expect(sidePanel(page)).toBeVisible();
}

/** Stops the working run from the panel's empty box, with Escape. */
async function stopInThePanel(page: Page): Promise<void> {
  await sidePanel(page).getByRole("textbox", { name: "Ask the command bar" }).press("Escape");
  await expect(newestRun(page)).toHaveAttribute("data-state", "stopped_person", {
    timeout: RUN_STEPS_MS,
  });
}

/** Starts a new conversation from the bar's header, which brings the search back. */
async function newConversation(page: Page): Promise<void> {
  await bar(page).getByTestId("going-new").click();
  await expect(searchBox(page)).toHaveAttribute("placeholder", "Search or ask…");
}

/** The seven seeded issues, which the chart and the table read and nothing else. */
function barKeys(issues: BarSeed): string[] {
  const { craters, dome, eyepiece, finder, mirror, mount, shutter } = issues;
  return [mount, craters, mirror, dome, shutter, eyepiece, finder];
}

/** One state of the design: its number, what it shows, how a row reaches it, and whether its
 *  shots draw a time of day, so the row installs the clock on the fixed day. */
interface BarRow {
  readonly state: string;
  readonly shows: string;
  readonly reach: (page: Page, seed: VisualSeed) => Promise<void>;
  readonly fixedDay?: true;
}

const barRows: readonly BarRow[] = [
  {
    // State 1. Scripts a short answer and the decision `none`, so the line on top reads Ask. The
    // words are two of the mount's title, so the search, which matches every word, lists it, and
    // not the whole title, which would rank above the line on top. The short answer names the
    // mount by its key, which the panel draws as the mount's chip, in the text and nowhere else.
    reach: async (page, seed) => {
      const words = "telescope mount";
      await script(seed, []);
      await openTheBar(page, seed);
      await shoot(page, "01-empty");
      // "/" in the empty box opens the filters; the Filters button closes them.
      await searchBox(page).press("/");
      await expect(bar(page).getByTestId("search-filters-panel")).toBeVisible();
      await shoot(page, "01-filters");
      await bar(page).getByTestId("search-filters-button").click();
      await expect(bar(page).getByTestId("search-filters-panel")).toHaveCount(0);
      await searchBox(page).fill(words);
      await expect(bar(page).locator(`#search-option-issue-${seed.bar.mount}`)).toBeVisible();
      await expect(answerPanel(page)).toHaveAttribute("data-state", "ended", {
        timeout: ANSWER_ENDS_MS,
      });
      await expect(bar(page).getByTestId("search-line-deciding")).toHaveCount(0);
      await expect(lineOnTop(page)).toContainText(`Ask ${words}`);
      await chipsRead(answerText(page), 1);
      await expect(answerPanel(page).getByTestId("going-answer-item")).toHaveCount(0);
      await shoot(page, "01-typed");
    },
    shows: "the empty box, the filters, and typed words with the line on top",
    state: "01",
  },
  {
    // State 2. Scripts a run whose one model call streams without end and calls nothing, so the
    // bar holds the question and "Thinking…" until the row stops it.
    reach: async (page, seed) => {
      const words = "watch the sky over the observatory tonight";
      await script(seed, [{ turns: [endlessStream()], words }]);
      await openTheBar(page, seed);
      await askTheAgent(page, words);
      await expect(newestRun(page)).toHaveAttribute("data-state", "working");
      await expect(newestRun(page).getByTestId("going-thinking")).toBeVisible();
      await shoot(page, "02-thinking-bar");
      await keepAtTheSide(page);
      await expect(sidePanel(page).getByTestId("going-thinking")).toBeVisible();
      await shoot(page, "02-thinking-panel");
      await stopInThePanel(page);
    },
    shows: "the moment after Enter",
    state: "02",
  },
  {
    // State 3. Scripts a run that looks for a tool, reads two issues in one model call under
    // the words it wrote beside them, searches, then streams without end: four finished lines -
    // the look in its own words, since one step past the newest three folds into no line of its
    // own - the newest with the run's timer, and the run still working. Words sent while it works
    // wait under it; the row drops them before it stops the run.
    reach: async (page, seed) => {
      const words = "check the telescope work";
      const reading: ChatTurn = {
        text: "Reading the telescope's issues…",
        toolCalls: [
          {
            arguments: { arguments: { issue: seed.bar.mount }, tool: "dispatch_read" },
            name: "call_tool",
          },
          {
            arguments: { arguments: { issue: seed.bar.craters }, tool: "dispatch_read" },
            name: "call_tool",
          },
        ],
      };
      const searching: ChatTurn = {
        toolCalls: [
          { arguments: { project: seed.bar.project, query: "telescope" }, name: "search" },
        ],
      };
      await script(seed, [
        {
          turns: [findTools("list the issues of a project"), reading, searching, endlessStream()],
          words,
        },
      ]);
      await openTheBar(page, seed);
      await askTheAgent(page, words);
      const run = newestRun(page);
      const finished = '[data-testid="going-step"][data-running="false"]';
      await expect(run.locator(finished)).toHaveCount(4, { timeout: RUN_STEPS_MS });
      await expect(run.getByTestId("going-steps-older")).toHaveCount(0);
      await expect(run.getByTestId("going-step-label").first()).toHaveText(
        /^Looked for a way to \S/
      );
      await expect(run.getByTestId("going-step-words")).toContainText("Reading the telescope");
      // One timer, on the newest line; it is masked in the shot, so its words are read here.
      await expect(run.getByTestId("going-step-timer")).toHaveCount(1);
      await expect(run.getByTestId("going-step").last().getByTestId("going-step-timer")).toHaveText(
        /^\d+ s$/
      );
      await expect(run).toHaveAttribute("data-state", "working");
      await searchBox(page).fill("and the primary mirror too");
      await searchBox(page).press("Enter");
      await expect(bar(page).getByTestId("going-queued")).toBeVisible();
      await shoot(page, "03-working-bar");
      await keepAtTheSide(page);
      await expect(sidePanel(page).getByTestId("going-queued")).toBeVisible();
      await expect(sidePanel(page).locator(finished)).toHaveCount(4);
      await shoot(page, "03-working-panel");
      await sidePanel(page).getByTestId("going-queued-drop").click();
      await expect(sidePanel(page).getByTestId("going-queued")).toHaveCount(0);
      await stopInThePanel(page);
    },
    shows: "a run that works",
    state: "03",
  },
  {
    // State 4. Scripts a run that reads two issues, the first in a slow turn, and answers with
    // both as links, which the bar draws as chips under the line its steps fold into.
    reach: async (page, seed) => {
      const words = "what telescope work is still open";
      const { craters, mount } = seed.bar;
      await script(seed, [
        {
          turns: [
            slow(read(mount)),
            read(craters),
            say(
              `Two pieces of telescope work are open:\n\n- ${naming(mount)}\n- ${naming(craters)}`
            ),
          ],
          words,
        },
      ]);
      await openTheBar(page, seed);
      await askTheAgent(page, words);
      await expect(newestRun(page)).toHaveAttribute("data-state", "done", {
        timeout: RUN_STEPS_MS,
      });
      // The folded line's time is masked in the shot, so its words are read here.
      await expect(newestRun(page).getByTestId("going-steps-fold")).toHaveAccessibleName(
        /^Worked for \d+ s · read 2 items$/
      );
      await chipsRead(newestRun(page).getByTestId("going-run-answer"), 2);
      await shoot(page, "04-answer-bar");
      await keepAtTheSide(page);
      await chipsRead(sidePanel(page).getByTestId("going-run-answer"), 2);
      await shoot(page, "04-answer-panel");
    },
    shows: "an answer with its chips under the folded steps",
    state: "04",
  },
  {
    // State 5. Scripts two runs, each listing OPS's issues with `dispatch_issues` in a slow turn
    // and drawing a block over the seven seeded issues alone: a chart of them by status, then, in
    // a new conversation, a table of them by key. The clock is set forward, to 16:40 before the
    // chart's Enter and to 16:45 before the table's, since each caption says when its read was
    // made.
    fixedDay: true,
    reach: async (page, seed) => {
      const chartWords = "chart the telescope work by status";
      const tableWords = "list the telescope work";
      const listing = slow(callTool("dispatch_issues", { project: seed.bar.project }));
      const telescopeWork = { field: "key", in: barKeys(seed.bar), op: "filter" };
      // The listing is the first model call's tool call, so its result is step 2, call 1.
      const listed = [{ call: 1, step: 2 }];
      await script(seed, [
        {
          turns: [
            listing,
            callTool("draw_block", {
              chain: [telescopeWork, { field: "status", op: "group" }, { op: "count" }],
              kind: "chart",
              steps: listed,
              title: "Telescope work by status",
              x: "status",
              y: "count",
            }),
            say("Most of the telescope work is still in triage."),
          ],
          words: chartWords,
        },
        {
          turns: [
            listing,
            callTool("draw_block", {
              chain: [telescopeWork, { field: "key", op: "sort" }],
              columns: ["key", "status", "priority"],
              kind: "table",
              steps: listed,
              title: "Telescope work",
            }),
            say("Here is the telescope work, issue by issue."),
          ],
          words: tableWords,
        },
      ]);
      await openTheBar(page, seed);
      await page.clock.setSystemTime(AT_4_40_PM);
      await askTheAgent(page, chartWords);
      await expect(newestRun(page)).toHaveAttribute("data-state", "done", {
        timeout: RUN_STEPS_MS,
      });
      const chart = newestRun(page).getByTestId("going-block");
      await expect(chart).toHaveAttribute("data-kind", "chart");
      await expect(chart.getByTestId("going-block-chart")).toHaveAttribute(
        "data-chart-drawn",
        "true"
      );
      await expect(chart.getByTestId("going-block-caption")).toHaveText(
        /^Telescope work by status · as of 4:40\sPM$/
      );
      await expect(newestRun(page).getByTestId("going-steps-fold")).toHaveAccessibleName(
        /^Worked for \d+ s · /
      );
      await shoot(page, "05-chart");
      await newConversation(page);
      await page.clock.setSystemTime(AT_4_45_PM);
      await askTheAgent(page, tableWords);
      await expect(newestRun(page)).toHaveAttribute("data-state", "done", {
        timeout: RUN_STEPS_MS,
      });
      const table = newestRun(page).getByTestId("going-block");
      await expect(table).toHaveAttribute("data-kind", "table");
      await expect(table.getByTestId("going-block-caption")).toHaveText(
        /^Telescope work · as of 4:45\sPM$/
      );
      await chipsRead(table, barKeys(seed.bar).length);
      await expect(newestRun(page).getByTestId("going-steps-fold")).toHaveAccessibleName(
        /^Worked for \d+ s · /
      );
      await shoot(page, "05-table");
    },
    shows: "a chart, and a table",
    state: "05",
  },
  {
    // State 6. Scripts a run that proposes, in a slow turn, one row of each kind - a new issue, a
    // change of fields, a document's text, a message to an agent and a close - and waits. The
    // message goes to the Planner, whom the seed routes the dome to (`seedBarIssues`), so its row
    // names the session, the issue it is posted on and its delivery (R7). The row discards the
    // plan, so nothing is sent.
    reach: async (page, seed) => {
      const words = "plan a motor for the observatory dome";
      const { dome, mount, project, shutter } = seed.bar;
      await script(seed, [
        {
          afterwards: say("Discarded."),
          turns: [
            slow(
              proposePlan("Fit a motor to the dome, and retire the old shutter it replaces.", [
                {
                  arguments: {
                    parent: dome,
                    priority: 2,
                    project,
                    title: "Fit a motor to the dome",
                  },
                  tool: "dispatch_issue",
                },
                {
                  arguments: { issue: mount, labels: ["optics"], priority: 2 },
                  tool: "dispatch_issue_update",
                },
                {
                  arguments: {
                    artifact: "spec",
                    body: "The dome turns by motor once it has one, so the spec says so.",
                    issue: dome,
                    quote: "The dome turns by hand.",
                    replace_with: "The dome turns by motor.",
                  },
                  tool: "dispatch_suggest",
                },
                {
                  arguments: {
                    body: "A motor for the dome is planned.",
                    delivery: "btw",
                    session: sessionIds.planner,
                  },
                  tool: "dispatch_send_to_agent",
                },
                {
                  arguments: {
                    issue: shutter,
                    reason: "The motor's shutter replaces it.",
                    status: "done",
                  },
                  tool: "dispatch_issue_update",
                },
              ])
            ),
          ],
          words,
        },
      ]);
      await openTheBar(page, seed);
      await askTheAgent(page, words);
      await expect(newestRun(page)).toHaveAttribute("data-state", "waiting", {
        timeout: RUN_STEPS_MS,
      });
      await expect(planCard(page)).toHaveAttribute("data-state", "waiting", {
        timeout: RUN_STEPS_MS,
      });
      await expect(planRows(planCard(page))).toHaveCount(5);
      await expect(
        planCard(page)
          .getByTestId("item-chip")
          .filter({ hasNot: page.getByTestId("item-chip-status") })
      ).toHaveCount(0);
      // The message row names the session, the issue it is posted on by its chip, and the
      // delivery; the suggestion names the issue whose spec it changes by its chip.
      await expect(planCard(page).getByTestId("going-plan-message")).toHaveText(
        new RegExp(`^Message to Planner on ${dome}(?!\\d).* · as a BTW$`)
      );
      await expect(planCard(page).getByTestId("going-plan-document")).toHaveText(
        new RegExp(`^Suggest a change to spec on ${dome}(?!\\d)`)
      );
      await shoot(page, "06-plan-bar");
      await keepAtTheSide(page);
      await expect(planRows(planCard(page))).toHaveCount(5);
      await expect(
        planCard(page)
          .getByTestId("item-chip")
          .filter({ hasNot: page.getByTestId("item-chip-status") })
      ).toHaveCount(0);
      await shoot(page, "06-plan-panel");
      await planCard(page).getByTestId("going-plan-reject").click();
      await expect(planCard(page)).not.toHaveAttribute("data-state", "waiting", {
        timeout: RUN_STEPS_MS,
      });
    },
    shows: "a plan of each row kind that waits",
    state: "06",
  },
  {
    // State 7. Scripts a run that proposes, in a slow turn, two changes and a message on an issue,
    // then answers once they ran. The message is on the eyepiece and to no session: a message to
    // an agent that ran here would reach the workspace's live session, whose pages the pages'
    // rows shoot. The row approves with Enter, with the clock set forward to 16:40, since the
    // applied title says when; then it presses Undo, which takes both changes back and leaves the
    // message sent. Each shot waits for the finder scope's chip to read its title as the change,
    // then the Undo, left it.
    fixedDay: true,
    reach: async (page, seed) => {
      const words = "raise the eyepiece and rename the finder scope";
      const { eyepiece, finder } = seed.bar;
      await script(seed, [
        {
          afterwards: say("Done."),
          turns: [
            slow(
              proposePlan("Raise the eyepiece first, and give the finder scope its full name.", [
                { arguments: { issue: eyepiece, priority: 1 }, tool: "dispatch_issue_update" },
                {
                  arguments: { issue: finder, title: "Align the finder scope to the mount" },
                  tool: "dispatch_issue_update",
                },
                {
                  arguments: { body: "The eyepiece comes first now.", issue: eyepiece },
                  tool: "dispatch_message",
                },
              ])
            ),
            say("Raised the eyepiece, renamed the finder scope and said so on the eyepiece."),
          ],
          words,
        },
      ]);
      await openTheBar(page, seed);
      await askTheAgent(page, words);
      await expect(newestRun(page)).toHaveAttribute("data-state", "waiting", {
        timeout: RUN_STEPS_MS,
      });
      await expect(planCard(page)).toHaveAttribute("data-state", "waiting", {
        timeout: RUN_STEPS_MS,
      });
      await page.clock.setSystemTime(AT_4_40_PM);
      await searchBox(page).focus();
      await searchBox(page).press("Enter");
      await expect(newestRun(page)).toHaveAttribute("data-state", "done", {
        timeout: RUN_STEPS_MS,
      });
      await expect(planCard(page).getByTestId("going-plan-title")).toHaveText(
        /^Applied .* · 4:40\sPM$/
      );
      await expect(planCard(page).getByTestId("going-undo-button")).toBeVisible();
      const finderChip = planCard(page)
        .getByTestId("going-plan-row")
        .filter({ hasText: "Align the finder scope to the mount" })
        .getByTestId("item-chip");
      await expect(finderChip).toContainText("Align the finder scope to the mount");
      await shoot(page, "07-applied");
      await planCard(page).getByTestId("going-undo-button").click();
      await expect(planCard(page).getByTestId("going-undo-line")).toHaveCount(3, {
        timeout: RUN_STEPS_MS,
      });
      await expect(planCard(page).getByTestId("going-plan-title")).toHaveText(/^Undone/);
      await expect(finderChip).not.toContainText("to the mount");
      await shoot(page, "07-undone");
    },
    shows: "a plan applied, and undone",
    state: "07",
  },
  {
    // State 8. Scripts two runs of one conversation, each reading one issue in a slow turn and
    // answering with it as a link: the question, then the follow-up typed in the box under the
    // first answer.
    reach: async (page, seed) => {
      const asked = "what is the primary mirror waiting on";
      const followUp = "and the telescope mount";
      const { mirror, mount } = seed.bar;
      await script(seed, [
        {
          turns: [slow(read(mirror)), say(`The mirror waits for its grinding, ${naming(mirror)}.`)],
          words: asked,
        },
        {
          turns: [slow(read(mount)), say(`The mount waits for its calibration, ${naming(mount)}.`)],
          words: followUp,
        },
      ]);
      await openTheBar(page, seed);
      await askTheAgent(page, asked);
      await expect(newestRun(page)).toHaveAttribute("data-state", "done", {
        timeout: RUN_STEPS_MS,
      });
      await searchBox(page).fill(followUp);
      await searchBox(page).press("Enter");
      await expect(page.getByTestId("going-run")).toHaveCount(2, { timeout: RUN_STEPS_MS });
      await expect(newestRun(page)).toHaveAttribute("data-state", "done", {
        timeout: RUN_STEPS_MS,
      });
      await chipsRead(bar(page), 2);
      await expect(bar(page).getByTestId("going-steps-fold")).toHaveText([
        /^Worked for \d+ s · read 1 item›$/,
        /^Worked for \d+ s · read 1 item›$/,
      ]);
      await shoot(page, "08-follow-up");
    },
    shows: "a follow-up under the first answer",
    state: "08",
  },
  {
    // State 9. Three conversations at the Fast level, which the row chooses in the mode menu.
    // The first is scripted to call a tool in each of Fast's six model calls, the first of them
    // slow, so it stops at its step limit; the second reads one issue in a slow turn and then
    // streams without end, and the row stops it with Escape; the third is answered 500 twice,
    // the call and its one retry, so it fails. The steps of each run that stopped fold into one
    // line, which counts what the bound's card counts.
    reach: async (page, seed) => {
      const bounded = "read every telescope issue one by one";
      const stopped = "keep reading the telescope mount";
      const failed = "what is the finder scope waiting on";
      const { craters, dome, eyepiece, mirror, mount, shutter } = seed.bar;
      const later = [craters, mirror, dome, shutter, eyepiece].map(read);
      await script(seed, [
        { turns: [slow(read(mount)), ...later], words: bounded },
        { turns: [slow(read(mount)), endlessStream()], words: stopped },
        { turns: [refusedWith(500), refusedWith(500)], words: failed },
      ]);
      await openTheBar(page, seed);
      const level = bar(page).getByTestId("going-level-button");
      await level.focus();
      await level.press("Enter");
      // The menu opens on the mode that is on, Standard; Fast is the row above it.
      await page.keyboard.press("ArrowUp");
      await page.keyboard.press("Enter");
      await expect(level).toHaveText(/^Fast/);
      await askTheAgent(page, bounded);
      await expect(newestRun(page)).toHaveAttribute("data-state", "stopped_bound", {
        timeout: RUN_STEPS_MS,
      });
      await expect(newestRun(page).getByTestId("going-run-bound")).toBeVisible();
      await expect(newestRun(page).getByTestId("going-steps-fold")).toHaveAccessibleName(
        /^Worked for \d+ s · read 6 items$/
      );
      await expect(newestRun(page).getByTestId("going-steps-fold")).toHaveAttribute(
        "aria-expanded",
        "false"
      );
      await expect(newestRun(page).getByTestId("going-step")).toHaveCount(0);
      await expect(newestRun(page).getByTestId("going-run-state")).toHaveText(
        "Stopped at Fast's step limit · read 6 items so far"
      );
      await shoot(page, "09-bound");
      await newConversation(page);
      await askTheAgent(page, stopped);
      await expect(newestRun(page).getByTestId("going-step").first()).toHaveAttribute(
        "data-running",
        "false",
        { timeout: RUN_STEPS_MS }
      );
      await searchBox(page).press("Escape");
      await expect(newestRun(page)).toHaveAttribute("data-state", "stopped_person", {
        timeout: RUN_STEPS_MS,
      });
      await expect(newestRun(page).getByTestId("going-run-state")).toHaveText(
        "Stopped · kept what it did"
      );
      await expect(newestRun(page).getByTestId("going-steps-fold")).toHaveAccessibleName(
        /^Worked for \d+ s · read 1 item$/
      );
      await shoot(page, "09-stopped");
      await newConversation(page);
      await askTheAgent(page, failed);
      await expect(newestRun(page)).toHaveAttribute("data-state", "failed", {
        timeout: RUN_STEPS_MS,
      });
      await expect(newestRun(page).getByTestId("going-run-state")).toContainText(
        "Dispatch could not reach the model."
      );
      await shoot(page, "09-failed");
    },
    shows: "a stop at a bound, a Stop, and a failure",
    state: "09",
  },
  {
    // State 10. Scripts a run that reads one issue in a slow turn and answers with it as a link;
    // the row moves the answer to the side with the header's button. The panel's header moves it
    // back with an icon whose words are its tooltip, and its box holds the context chip.
    reach: async (page, seed) => {
      const words = "what does the observatory dome need";
      const { dome } = seed.bar;
      await script(seed, [
        {
          turns: [
            slow(read(dome)),
            say(`The dome turns by hand and needs a motor, ${naming(dome)}.`),
          ],
          words,
        },
      ]);
      await openTheBar(page, seed);
      await askTheAgent(page, words);
      await expect(newestRun(page)).toHaveAttribute("data-state", "done", {
        timeout: RUN_STEPS_MS,
      });
      await bar(page).getByRole("button", { name: "Keep at the side" }).click();
      await expect(bar(page)).toHaveCount(0);
      await expect(sidePanel(page)).toBeVisible();
      await chipsRead(sidePanel(page).getByTestId("going-run-answer"), 1);
      await expect(
        sidePanel(page).getByRole("button", { name: "Open over the page" })
      ).toHaveAttribute("title", "Open over the page");
      await expect(sidePanel(page).getByTestId("going-title")).toHaveText(words);
      await shoot(page, "10-side-panel");
    },
    shows: "the side panel",
    state: "10",
  },
  {
    // State 11. Scripts two conversations: an answer in prose, then a run that reads one issue
    // and streams without end. The row closes the bar while the second works, so the chip names
    // it; it opens it again from the chip, stops it, and opens History, which lists both.
    reach: async (page, seed) => {
      const answered = "summarise the eyepiece work";
      const working = "keep watching the eyepiece";
      await script(seed, [
        { turns: [say("The eyepiece waits for its polish.")], words: answered },
        { turns: [read(seed.bar.eyepiece), endlessStream()], words: working },
      ]);
      await openTheBar(page, seed);
      await askTheAgent(page, answered);
      await expect(newestRun(page)).toHaveAttribute("data-state", "done", {
        timeout: RUN_STEPS_MS,
      });
      await newConversation(page);
      await askTheAgent(page, working);
      await expect(newestRun(page).getByTestId("going-step").first()).toHaveAttribute(
        "data-running",
        "false",
        { timeout: RUN_STEPS_MS }
      );
      await bar(page).getByTestId("going-close").click();
      await expect(bar(page)).toHaveCount(0);
      const chip = page.getByTestId("going-chip");
      await expect(chip).toHaveAttribute("data-state", "working");
      await expect(chip.getByTestId("going-chip-text")).toHaveText(`Working · ${working}`);
      await shoot(page, "11-chip");
      await chip.getByTestId("going-chip-open").click();
      await expect(bar(page)).toBeVisible();
      await searchBox(page).press("Escape");
      await expect(newestRun(page)).toHaveAttribute("data-state", "stopped_person", {
        timeout: RUN_STEPS_MS,
      });
      await bar(page).getByTestId("going-history-button").click();
      const listed = bar(page).getByTestId("going-history-conversation");
      await expect(listed).toHaveCount(2);
      await expect(listed.first()).toHaveAttribute("data-state", "stopped_person");
      await expect(listed.last()).toHaveAttribute("data-state", "done");
      await shoot(page, "11-history");
    },
    shows: "the chip, and History",
    state: "11",
  },
  {
    // State 12. No run: the mode menu, opened from its button with Enter.
    reach: async (page, seed) => {
      await script(seed, []);
      await openTheBar(page, seed);
      const level = bar(page).getByTestId("going-level-button");
      await level.focus();
      await level.press("Enter");
      const menu = page.getByRole("menu", { name: "Mode" });
      await expect(menu).toBeVisible();
      await expect(menu.getByRole("menuitemradio", { checked: true })).toBeFocused();
      await shoot(page, "12-mode-menu");
      await page.keyboard.press("Escape");
      await expect(menu).toHaveCount(0);
    },
    shows: "the mode menu",
    state: "12",
  },
  {
    // State 13. Scripts the decision `dispatch_issue`, so the line on top is File as a new issue,
    // and the new-issue run Enter on it starts: one create of an issue of OPS, which the server
    // files at once. The bar closes, and the notice names the issue it filed. Its words share none
    // with another row's search, which would list the issue in the reverse order alone.
    reach: async (page, seed) => {
      const words = "we need a weather station on the roof";
      const { project } = seed.bar;
      await script(
        seed,
        [
          {
            afterwards: say("Filed."),
            turns: [
              proposePlan(`File a weather station on the roof in ${project}.`, [
                {
                  arguments: {
                    project,
                    spec: "We need a weather station on the roof.\n",
                    title: "A weather station on the roof",
                  },
                  tool: "dispatch_issue",
                },
              ]),
            ],
            words,
          },
        ],
        "dispatch_issue"
      );
      await openTheBar(page, seed);
      await searchBox(page).fill(words);
      await expect(lineOnTop(page)).toContainText(`File as a new issue in ${project}`, {
        timeout: RUN_STEPS_MS,
      });
      await expect(answerPanel(page)).toHaveAttribute("data-state", "ended", {
        timeout: ANSWER_ENDS_MS,
      });
      await expect(bar(page).getByTestId("search-line-deciding")).toHaveCount(0);
      await shoot(page, "13-filing-line");
      await searchBox(page).press("Enter");
      const notice = page.getByTestId("going-notice");
      await expect(notice.getByTestId("going-notice-text")).toHaveText(
        new RegExp(`^Filed ${project}-\\d+$`),
        { timeout: RUN_STEPS_MS }
      );
      await expect(bar(page)).toHaveCount(0);
      await shoot(page, "13-filed");
    },
    shows: "File as a new issue, and the notice of the filing",
    state: "13",
  },
];

/** One row's work: the skips, a login of its own, the page's clock, and the state's shots. */
async function drawState(browser: Browser, row: BarRow): Promise<void> {
  if (seedPath === undefined) {
    test.skip(true, SKIP_REASON);
    return;
  }
  modelsLane();
  const seed = readVisualSeed(seedPath);
  const context = await asUser(browser, `bar-${row.state}`);
  try {
    const page = await context.newPage();
    await page.clock.install({ time: row.fixedDay === true ? FIXED_DAY : Date.now() });
    await row.reach(page, seed);
  } finally {
    await context.close();
  }
}

// The rows in the order `DISPATCH_E2E_VISUAL_ORDER` asks for, as `e2e/visual.e2e.ts` runs its
// own (e2e/visual-order.ts says why the review runs in two orders).
for (const row of inVisualOrder(barRows, visualOrder())) {
  // SLOW(>=2s): a sign-in, a settled page, the bar driven through runs of the fake models, and
  // a screenshot comparison of each state the row reaches.
  test(`the command bar's state ${row.state}, ${row.shows}, matches its baselines`, async ({
    browser,
  }) => {
    test.setTimeout(150_000);
    await drawState(browser, row);
  });
}
