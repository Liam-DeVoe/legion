import { createIssue, createProject, getIssueEvents, patchIssue } from "./api";
import { callTool, modelsLane, say, scriptChat } from "./fake-models-helpers";
import {
  ANSWER_ENDS_MS,
  answerPanel,
  askTheAgent,
  newestRun,
  openBarOver,
  prose,
  RUN_STEPS_MS,
  searchBox,
} from "./going-panel";
import { expect, test } from "./test";
import { asUser } from "./users";

// Check c3 of dispatch://INFRA-1588/artifact/verification-md (spec R3): a pause streams a short
// answer and the store records no write; Enter starts a run, each tool call shows as a step while
// it runs, and the answer links each item the fake read; and each number of a chart is the
// server's, from the chain the fake asks for over its read, never the number the fake writes in its
// text. Negative control: a bar that draws the numbers the model wrote shows the wrong number.

// SLOW(>=2s): a seeded issue and the pause's short answer.
test("a pause streams a short answer, and the store records no write", async ({ browser }) => {
  modelsLane();
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "Map the lunar craters" });
  const before = (await getIssueEvents(issue.key)).length;
  await scriptChat({ default: prose(`The craters are mapped in ${issue.key}.`) });
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await openBarOver(page, "/projects/CORE/issues");
    await searchBox(page).fill("where are the lunar craters mapped");
    await expect(answerPanel(page)).toHaveAttribute("data-state", "ended", {
      timeout: ANSWER_ENDS_MS,
    });
    expect((await getIssueEvents(issue.key)).length).toBe(before);
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): a run of two model calls and a read.
test("Enter starts a run: each tool call shows as a step while it runs, and the answer links each item it read", async ({
  browser,
}) => {
  modelsLane();
  test.setTimeout(90_000);
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "Map the lunar craters" });
  const words = "summarise the crater mapping";
  await scriptChat({
    default: prose("A short answer while the person pauses."),
    runs: [
      {
        turns: [
          // The read streams slowly, so its step shows working before its frame ends it.
          { ...callTool("dispatch_read", { issue: issue.key }), delayMs: 200 },
          say(`The mapping is in [${issue.key}](/issues/${issue.key}).`),
        ],
        words,
      },
    ],
  });
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await openBarOver(page, "/projects/CORE/issues");
    await askTheAgent(page, words);
    const run = newestRun(page);
    await expect(run.getByTestId("going-step").first()).toHaveAttribute("data-tool", "call_tool", {
      timeout: RUN_STEPS_MS,
    });
    await expect(run.getByTestId("going-step").first()).toHaveAttribute("data-running", "false", {
      timeout: RUN_STEPS_MS,
    });
    const answer = run.getByTestId("going-run-answer");
    await expect(answer.getByRole("link", { name: issue.key })).toHaveAttribute(
      "href",
      `/issues/${issue.key}`,
      { timeout: RUN_STEPS_MS }
    );
    await expect(run).toHaveAttribute("data-state", "done", { timeout: RUN_STEPS_MS });
  } finally {
    await context.close();
  }
});

// SLOW(>=2s): three seeded issues, a run of a read, a chart and an answer.
test("each number of a chart is the server's count over the read, and the number the model wrote shows nowhere in it", async ({
  browser,
}) => {
  modelsLane();
  test.setTimeout(90_000);
  await createProject({ key: "CORE", name: "Core" });
  for (const title of ["Grind the lens", "Silver the mirror", "Align the finder"]) {
    const made = await createIssue({ project: "CORE", title });
    if (title !== "Align the finder") {
      await patchIssue(made.key, { status: "in_progress" });
    }
  }
  const words = "chart CORE's issues by status";
  await scriptChat({
    default: prose("A short answer while the person pauses."),
    runs: [
      {
        turns: [
          callTool("dispatch_issues", { project: "CORE" }),
          callTool("draw_block", {
            chain: [{ field: "status", op: "group" }, { op: "count" }],
            kind: "chart",
            steps: [{ call: 1, step: 2 }],
            title: "CORE's issues by status",
            x: "status",
            y: "count",
          }),
          say("CORE holds 47 issues in progress."),
        ],
        words,
      },
    ],
  });
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await openBarOver(page, "/projects/CORE/issues");
    await askTheAgent(page, words);
    const run = newestRun(page);
    const chart = run.getByTestId("going-block");
    await expect(chart).toHaveAttribute("data-kind", "chart", { timeout: RUN_STEPS_MS });
    await expect(run).toHaveAttribute("data-state", "done", { timeout: RUN_STEPS_MS });
    await expect(chart).toContainText("2");
    await expect(chart).toContainText("1");
    await expect(chart).not.toContainText("47");
  } finally {
    await context.close();
  }
});
