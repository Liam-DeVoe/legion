import { createIssue, createProject } from "./api";
import { modelsLane, runCalls, say, scriptChat } from "./fake-models-helpers";
import {
  askTheAgent,
  bar,
  newestRun,
  openBarOver,
  prose,
  RUN_STEPS_MS,
  searchBox,
  sidePanel,
} from "./going-panel";
import { expect, type Page, test } from "./test";
import { asUser } from "./users";

// Check c1 of dispatch://INFRA-1588/artifact/verification-md (spec R1): Ctrl-K opens the bar over
// the page and Esc closes it; Ctrl-Shift-K keeps the same conversation in a panel at the right,
// which stays open on every navigation and leaves the page usable; and a click on an issue of the
// list makes it the context of the next request, which the fake chat model reads. The models lane
// runs it (`bun run e2e:models`), and its Firefox row the `firefox` project
// (`bun run e2e:models:firefox`). Negative control: a panel that ignores a click on the page sends
// the next request with no issue.

async function seedList(): Promise<{ readonly first: string; readonly second: string }> {
  await createProject({ key: "CORE", name: "Core" });
  const first = await createIssue({ project: "CORE", title: "Chart the comet's tail" });
  const second = await createIssue({ project: "CORE", title: "Count the moons of Saturn" });
  return { first: first.key, second: second.key };
}

/** The panel at the side shows the conversation the bar held: the run of `words`. */
async function expectAtTheSide(page: Page, words: string): Promise<void> {
  await expect(bar(page)).toHaveCount(0);
  await expect(sidePanel(page)).toBeVisible();
  await expect(sidePanel(page).getByTestId("going-run-words").first()).toHaveText(words);
}

// SLOW(>=2s): a seeded issue and one open and close of the bar.
test("opens over the issue page", async ({ browser }) => {
  modelsLane();
  const { first } = await seedList();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await openBarOver(page, `/issues/${first}`);
    await expect(bar(page)).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(bar(page)).toHaveCount(0);
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): one open and close of the bar over the Inbox.
test("opens over the Inbox", async ({ browser }) => {
  modelsLane();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await openBarOver(page, "/");
    await expect(bar(page)).toBeVisible();
    await page.keyboard.press("Control+k");
    await expect(bar(page)).toHaveCount(0);
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): two runs of the fake chat model and a navigation.
test("Ctrl-Shift-K keeps the conversation at the side, and a click on an issue of the list is the next request's context", async ({
  browser,
}) => {
  modelsLane();
  test.setTimeout(120_000);
  const { second } = await seedList();
  const asked = "what is on this list";
  const next = "what does this one need";
  await scriptChat({
    default: prose("A short answer while the person pauses."),
    runs: [
      { turns: [say("Two issues about the sky.")], words: asked },
      { turns: [say("It needs a telescope.")], words: next },
    ],
  });
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await openBarOver(page, "/projects/CORE/issues");
    await askTheAgent(page, asked);
    await expect(newestRun(page).getByTestId("going-run-answer")).toHaveText(
      "Two issues about the sky.",
      { timeout: RUN_STEPS_MS }
    );
    await page.keyboard.press("Control+Shift+K");
    await expectAtTheSide(page, asked);

    // The list stays usable beside the panel: a click on its issue opens it, and names it.
    await page
      .getByRole("link", { name: new RegExp(second) })
      .first()
      .click();
    await expect(page).toHaveURL(new RegExp(`/issues/${second}`));
    await expect(sidePanel(page)).toBeVisible();
    const box = sidePanel(page).getByRole("textbox", { name: "Ask the command bar" });
    await box.fill(next);
    await box.press("Enter");
    await expect(sidePanel(page).getByText("It needs a telescope.")).toBeVisible({
      timeout: RUN_STEPS_MS,
    });
    const [call] = await runCalls(next);
    expect(call?.context).toMatchObject({ issue: second });
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): a run, the panel at the side, and two navigations.
test("the panel stays open on navigation", async ({ browser }) => {
  modelsLane();
  test.setTimeout(90_000);
  await seedList();
  const asked = "keep me company";
  await scriptChat({
    default: prose("A short answer while the person pauses."),
    runs: [{ turns: [say("Here beside you.")], words: asked }],
  });
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await openBarOver(page, "/projects/CORE/issues");
    await askTheAgent(page, asked);
    await bar(page).getByTestId("going-move").click();
    await expectAtTheSide(page, asked);
    // A navigation inside the page, by the rail's own link, and Back: neither closes the panel.
    await page
      .getByRole("link", { name: /^Inbox/ })
      .first()
      .click();
    await expect(page).toHaveURL(/\/$/);
    await expect(sidePanel(page)).toBeVisible();
    await page.goBack();
    await expect(page).toHaveURL(/\/projects\/CORE\/issues/);
    await expect(sidePanel(page)).toBeVisible();
    await expect(sidePanel(page).getByText("Here beside you.")).toBeVisible({
      timeout: RUN_STEPS_MS,
    });
  } finally {
    await context.close();
  }
});

// Firefox keeps Ctrl-Shift-K for its Web Console. The row tells whether the page gets that key in
// a real Firefox: where it does, the key keeps the conversation at the side; where it does not,
// Ctrl-K pressed while the bar is open does, and the button "Keep at the side" stays either way.
// SLOW(>=2s): one open of the bar and a move to the side.
test("in Firefox the conversation keeps at the side by Ctrl-Shift-K, or else by Ctrl-K in the open bar, and the button stays @firefox", async ({
  browser,
}) => {
  modelsLane();
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await openBarOver(page, "/");
    await expect(bar(page).getByRole("button", { name: "Keep at the side" })).toBeVisible();
    await searchBox(page).press("Control+Shift+K");
    if ((await sidePanel(page).count()) === 0) {
      await searchBox(page).press("Control+k");
    }
    await expect(sidePanel(page)).toBeVisible();
    await expect(bar(page)).toHaveCount(0);
  } finally {
    await context.close();
  }
});
