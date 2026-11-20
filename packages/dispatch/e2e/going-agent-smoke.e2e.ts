import { callTool, modelsLane, runCalls, say, scriptChat } from "./fake-models-helpers";
import { openBarOver, prose, searchBox, seedIssue } from "./going-panel";
import { expect, test } from "./test";
import { asUser } from "./users";

// The harness of the command bar's agent (plan task t16 of dispatch://INFRA-1588/artifact/plan-md):
// the server's agent process calls the fake writing model as it calls a deployment's, the fake
// answers a run from the turns a row scripts for its words, and records each call with the run's
// page and the tool results it gave the model. One run proves the whole path: the person's words
// typed in the bar, a call_tool of a read that the server answers as the person, and the answer
// the run ends with on the screen. Every row runs in the models lane alone (`bun run e2e:models`).

/** The words a person asks, which name no key and no title, so the line on top is Ask. */
const words = "which log keeps the quasar sightings";
const title = "Quasar sightings log";

// SLOW(>=2s): a seeded issue, the pause's short answer, and a run of two model calls and a read.
test("a question asked in the bar runs the agent: its read of the issue reaches the model and its answer shows", async ({
  browser,
}) => {
  modelsLane();
  test.setTimeout(90_000);
  const issue = await seedIssue("quasar sightings log", title);
  const answer = `The sightings are kept in ${issue.key}, ${title}.`;
  await scriptChat({
    default: prose("A short answer while the person pauses."),
    runs: [{ turns: [callTool("dispatch_read", { issue: issue.key }), say(answer)], words }],
  });
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await openBarOver(page, "/projects/CORE/issues");
    await searchBox(page).fill(words);
    await searchBox(page).press("Enter");

    await expect(page.getByText(answer)).toBeVisible({ timeout: 40_000 });
    const calls = await runCalls(words);
    expect(calls.map((call) => call.turn)).toEqual([0, 1]);
    // The run carries the page it was asked from, and its first call offers the agent's tools.
    expect(calls[0].context).toMatchObject({ path: "/projects/CORE/issues" });
    expect(calls[0].tools).toContain("call_tool");
    // The read was the server's answer to the run's person: the issue as Dispatch holds it.
    expect(calls[1].toolResults).toHaveLength(1);
    expect(calls[1].toolResults[0]).toContain(title);
  } finally {
    await context.close();
  }
});
