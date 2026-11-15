import { createHandoff, createIssue, createProject } from "./api";
import { historyEntries, historyPanel } from "./issue-page";
import { expect, type Page, test } from "./test";
import { asUser } from "./users";

// The session that writes both versions, as `dispatch handoff-write` does: under the agent bearer,
// naming itself, with the title its origin stamps on the write.
const implementer = {
  id: "e2e-handoff-implementer",
  kind: "session" as const,
  origin: { session_title: "Implementer (e2e)" },
};

/** An issue carrying v1 and v2 of its `implement` handoff, each body with a two-entry `proof`. */
async function seedHandoffs(): Promise<string> {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "Handoffs on the issue" });
  await createHandoff(
    issue.key,
    "implement",
    {
      schemaVersion: 1,
      phase: "implement",
      issue: issue.key,
      verified: true,
      summary: "First pass",
      proof: [
        { claim: "unit tests pass", command: "bun run test" },
        { claim: "types check", command: "bun run typecheck" },
      ],
    },
    implementer
  );
  await createHandoff(
    issue.key,
    "implement",
    {
      schemaVersion: 1,
      phase: "implement",
      issue: issue.key,
      verified: true,
      summary: "Second pass, review addressed",
      files: ["web/src/a.ts", "web/src/b.ts"],
      proof: [
        { claim: "browser suite green", command: "bun run e2e" },
        { claim: "lint clean", command: "bunx biome check" },
      ],
    },
    implementer
  );
  return issue.key;
}

/** The history's line for one handoff version, by the words the line reads. */
function handoffLine(page: Page, version: number) {
  return historyEntries(page).filter({
    hasText: `recorded the implement handoff, version ${version}`,
  });
}

// The conversation's handoff cards are the history's lines now (dispatch://INFRA-1196, the
// Design's "Where each thing goes": "The conversation's handoff cards, and the Activity tab -
// The history in the panel"), and each line keeps what the card opened: its fields and every
// proof entry, read when the reader opens them.
// @pr: The handoff surface's own row: each recorded version is a line of the history that opens to its fields and every proof entry.
test("the history lists each handoff version, and opening one shows its fields and every proof entry @pr", async ({
  browser,
}, testInfo) => {
  const key = await seedHandoffs();

  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.goto(`/issues/${key}/activity`);
    await expect(historyPanel(page)).toBeVisible();

    const lines = historyEntries(page).filter({ hasText: "recorded the implement handoff" });
    await expect(lines).toHaveCount(2);
    // Newest first: version 2 was written after version 1.
    await expect(lines.first()).toContainText("version 2");
    await expect(lines.last()).toContainText("version 1");

    const v1 = handoffLine(page, 1);
    const v2 = handoffLine(page, 2);
    await expect(v2).toContainText("Implementer (e2e)");
    await expect(v1).toContainText("Implementer (e2e)");

    const toggle = v2.getByRole("button", { name: "Show fields" });
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await toggle.click();
    await expect(v2.getByRole("button", { name: "Hide fields" })).toHaveAttribute(
      "aria-expanded",
      "true"
    );

    const field = (name: string) => v2.locator(`[data-handoff-field="${name}"]`);
    await expect(field("schemaVersion")).toContainText("1");
    await expect(field("phase")).toContainText("implement");
    await expect(field("issue")).toContainText(key);
    await expect(field("verified")).toContainText("true");
    await expect(field("summary")).toContainText("Second pass, review addressed");
    // An array field is indented JSON.
    await expect(field("files").locator("pre")).toContainText('"web/src/a.ts"');
    await expect(field("files").locator("pre")).toContainText('"web/src/b.ts"');
    // The proof list is one row per entry.
    const proof = field("proof");
    await expect(proof).toHaveCount(2);
    await expect(proof.nth(0)).toContainText("proof 1");
    await expect(proof.nth(0)).toContainText("browser suite green");
    await expect(proof.nth(1)).toContainText("proof 2");
    await expect(proof.nth(1)).toContainText("lint clean");
    // Version 1 stays folded: its body is not on the page.
    await expect(v1.locator("[data-handoff-field]")).toHaveCount(0);
    await expect(page.getByText("First pass", { exact: true })).toHaveCount(0);

    await page.screenshot({
      fullPage: true,
      path: testInfo.outputPath(`handoff-history-${testInfo.project.name}.png`),
    });
  } finally {
    await alice.close();
  }
});

// A handoff written while the history is open reaches it over the event stream: the write's
// `handoff.recorded` is prepended to the issue's `["events", key]` log the history reads
// (`prependEventToLog`), so the new version's line arrives with no reload.
test("a handoff written while the history is open arrives without a reload", async ({
  browser,
}) => {
  const key = await seedHandoffs();

  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.goto(`/issues/${key}/activity`);
    const cards = historyEntries(page).filter({ hasText: "recorded the implement handoff" });
    await expect(cards).toHaveCount(2);
    // A mark on this document alone: a reload would load a new one, which carries no attribute of
    // the test's, while a refresh of the list leaves the document as it is.
    await page.evaluate(() => document.documentElement.setAttribute("data-loaded-once", "yes"));

    await createHandoff(
      key,
      "implement",
      {
        schemaVersion: 1,
        phase: "implement",
        issue: key,
        verified: true,
        summary: "Third pass, written while the history was open",
        proof: [
          { claim: "browser suite green", command: "bun run e2e" },
          { claim: "lint clean", command: "bunx biome check" },
        ],
      },
      implementer
    );

    await expect(cards).toHaveCount(3);
    await expect(cards.first()).toContainText("version 3");
    await expect(handoffLine(page, 3)).toContainText("Implementer (e2e)");
    await expect(page.locator("html")).toHaveAttribute("data-loaded-once", "yes");
  } finally {
    await alice.close();
  }
});

// One line per version, and no second drawing of either anywhere on the page.
test("the history has one line per recorded handoff version", async ({ browser }) => {
  const key = await seedHandoffs();

  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.goto(`/issues/${key}/activity`);
    await expect(historyPanel(page)).toBeVisible();
    const rows = historyEntries(page);
    await expect(rows.filter({ hasText: "recorded the implement handoff" })).toHaveCount(2);
    for (const version of [1, 2]) {
      const line = `recorded the implement handoff, version ${version}`;
      await expect(rows.filter({ hasText: line })).toHaveCount(1);
    }
  } finally {
    await alice.close();
  }
});
