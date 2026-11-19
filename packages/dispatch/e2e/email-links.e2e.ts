import { fixture } from "../web/src/__tests__/pmdoc-fixtures";
import { createIssue, createProject, editArtifact, getArtifact, getIssueArtifactText } from "./api";
import { documentEditor, openSpec } from "./editor";
import { expect, test } from "./test";
import { asUser } from "./users";

// A bare email address in Markdown a document is written from - a spec an issue starts with, or
// a line an agent inserts - is an autolink, and the server stores it as a link to the address
// with the mail scheme: `[a@b.test](mailto:a@b.test)`. This family has no past-version reading
// row of its own: its first browser row reads the stored document in a browser, and what it reads
// is a link's href, which no paragraph reading holds. The editor and a past version's read-only
// view render that link with the same href, and the editor's link opener (proof-sdk's
// markdown-link-click.ts) hands it to `window.open`: on a modifier click in the editable editor,
// on a plain click in the read-only view. The document is the one fixture table's
// `email-autolinks-in-a-spec`, whose `pm_json` pins the two `mailto:` link marks without a
// browser and whose paragraphs `web/src/features/refs/fixture-table-readings.test.ts` reads in a
// DOM.

declare global {
  interface Window {
    __dispatchOpened?: string[];
  }
}

const agentSession = {
  actor: { kind: "session" as const, id: "e2e-email-links" },
  as: "agent" as const,
};

const entry = fixture("email-autolinks-in-a-spec");

/** The entry's blocks: the paragraph the issue starts with, then the two an agent inserts after
 *  it, so the two versions the past-version half of the row needs are the entry's own text. */
const [firstBlock, ...insertedBlocks] = entry.markdown.trimEnd().split("\n\n");

// @pr: The mailto family's one row on the pull-request lane: an address written into a spec is a link in the editor and in a past version.
test("an email address written into a spec is a mailto link in the editor and in a past version @pr", async ({
  browser,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({
    project: "CORE",
    spec: `${firstBlock}\n`,
    title: "Email addresses in specs",
  });

  // An agent writes the second address into the spec, as `dispatch doc-edit` does: a new version,
  // whose number the server assigns. The paragraph after it is what the editor shows once the
  // edit has reached it.
  await editArtifact(
    issue.primary_artifact_id,
    { ops: [{ after: "end", markdown: insertedBlocks.join("\n\n"), op: "insert" }] },
    agentSession
  );
  const newestVersion = async () =>
    Math.max(
      ...((await getArtifact(issue.primary_artifact_id)).versions ?? []).map(({ number }) => number)
    );
  await expect.poll(newestVersion).toBeGreaterThan(1);
  const written = await newestVersion();

  const stored = (await getIssueArtifactText(issue.key, "spec")).markdown;
  expect(stored).toContain("[first@b.test](mailto:first@b.test)");
  expect(stored).toContain("[second@b.test](mailto:second@b.test)");

  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    // The page's `window.open` records what it was asked to open and opens nothing.
    await page.addInitScript(() => {
      const opened: string[] = [];
      window.__dispatchOpened = opened;
      window.open = (url?: string | URL) => {
        opened.push(String(url));
        return null;
      };
    });
    const opened = () => page.evaluate(() => window.__dispatchOpened ?? []);

    await openSpec(page, issue.key, entry.reading.paragraphs[2].text);
    const editor = documentEditor(page);
    await expect(editor.getByRole("link", { name: "first@b.test" })).toHaveAttribute(
      "href",
      "mailto:first@b.test"
    );
    const second = editor.getByRole("link", { name: "second@b.test" });
    await expect(second).toHaveAttribute("href", "mailto:second@b.test");
    // The editable editor opens a link on a modifier click; a plain click there places the caret.
    await second.click({ modifiers: ["ControlOrMeta"] });
    await expect.poll(opened).toEqual(["mailto:second@b.test"]);

    // The past version renders the same links through the read-only view, which opens one on a
    // plain click.
    await page.getByRole("combobox", { name: "Version" }).selectOption(String(written));
    const versionView = page.getByTestId("version-view");
    const first = versionView.getByRole("link", { name: "first@b.test" });
    await expect(first).toHaveAttribute("href", "mailto:first@b.test");
    await expect(versionView.getByRole("link", { name: "second@b.test" })).toHaveAttribute(
      "href",
      "mailto:second@b.test"
    );
    await first.click();
    await expect.poll(opened).toEqual(["mailto:second@b.test", "mailto:first@b.test"]);
  } finally {
    await alice.close();
  }
});

// R4 of dispatch://INFRA-4224, written inline rather than taken from the fixture table: a resource
// path that holds a hyphenated service account is text, and the account named alone is one link to
// the whole address. Before the rule both readers, Go's and the editor's engine, linked the path's
// `hosts@…` alone, which names another account.
const account = "agent-hosts@example-project.iam.gserviceaccount.com";
const resourcePath = `projects/example-project/serviceAccounts/${account}`;
const grant = `Grant act-as on ${account}.`;

test("a resource path holding a hyphenated address is text, and the address alone is one mailto link", async ({
  browser,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({
    project: "CORE",
    spec: `${resourcePath}\n\n${grant}\n`,
    title: "A service account in a spec",
  });

  const stored = (await getIssueArtifactText(issue.key, "spec")).markdown;
  expect(stored).toContain(`${resourcePath}\n`);
  expect(stored).toContain(`Grant act-as on [${account}](mailto:${account}).`);

  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await openSpec(page, issue.key, grant);
    const editor = documentEditor(page);
    const path = editor.locator("p", { hasText: resourcePath });
    await expect(path).toHaveText(resourcePath);
    await expect(path.getByRole("link")).toHaveCount(0);
    const links = editor.getByRole("link");
    await expect(links).toHaveCount(1);
    await expect(links).toHaveText(account);
    await expect(links).toHaveAttribute("href", `mailto:${account}`);
  } finally {
    await alice.close();
  }
});
