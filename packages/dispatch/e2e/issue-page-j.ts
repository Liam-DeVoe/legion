import { expect, type Locator, type Page } from "@playwright/test";

import { CONTROL_LABELS } from "../web/src/features/issue-page/page-words";
import { createComment, createIssue, createProject, getArtifact } from "./api";
import { PROJECT } from "./issue-layout-seed-support";
import { requirementBlockId } from "./issue-layout-seed-text";

/**
 * How the Spec view's rows read the new issue page's Spec view and its Contents slot
 * (`e2e/issue-page-j.e2e.ts`): the readers that `e2e/issue-page.ts` (the page's root and its
 * frame), `e2e/issue-page-g.ts` (one node's row, and the tree as rows) and `e2e/issue-page-i.ts`
 * (the outline) do not hold, and the seeds of the view's own.
 *
 * What the Spec view and the Contents slot draw, beyond what those files name:
 *
 * - **The issue's three sentences** `[data-testid="issue-three-things"]`, each an `li` whose
 *   label is `[data-page-chrome]` and whose sentence is `[data-author-text]`: in the Contents
 *   slot (the Contents pane and the Outline sheet), and as the Spec view's first group.
 * - **The Contents list**, one `[data-contents-entry="<node id>"]` per entry, its line
 *   `[data-contents-line]`, and above it the toggle named `CONTROL_LABELS.focus_branch`.
 * - **The Spec view** `[data-spec-body]`: its legend `[data-spec-legend]` of four
 *   `[data-spec-legend-mark]`, its column heads `[data-spec-head]`, its tree `[data-spec-tree]`,
 *   one `[data-spec-group]` per parent and one `[data-spec-run]` per run of statements, its
 *   decision cells `[data-spec-cell]` (`data-said`, `data-spec-statements`), its statements
 *   `[data-spec-statement]` (`data-spec-mark`), and under each statement the line
 *   `[data-spec-because]` naming its decision.
 */

// ---------------------------------------------------------------------------------------------
// The seed.
// ---------------------------------------------------------------------------------------------

/** One issue of `seedChildWithSpec`: its key, its spec's slug, and the block ids of the
 *  statements its spec holds, in document order. */
export interface IssueWithSpec {
  readonly key: string;
  readonly requirements: readonly string[];
  readonly specSlug: string;
}

export interface ChildWithSpecSeed {
  readonly child: IssueWithSpec;
  readonly parent: IssueWithSpec;
}

/** A statement of a seeded spec: one `requirement` block. */
export function statementBlock(id: string, words: string): string {
  return `:::requirement{#${id}}\n${words}\n:::`;
}

/** The slug of the spec `createIssue` made for an issue, by its artifact's id. */
async function specSlugOf(artifactId: string): Promise<string> {
  if (artifactId === "") throw new Error("the seeded issue holds no spec");
  return (await getArtifact(artifactId)).slug;
}

/**
 * A parent and its child, each with a spec of its own, in the project the issue-layout seeds
 * make: the parent's two statements, then the child's summary and its two statements, each in
 * words of its own. The child's Spec view draws its own two statements and none of its parent's
 * (R6; `useIssueTree.ts` reads the issue's own coverage). `e2e/issue-layout-seed.ts`'s seeds
 * give a parent no spec, so none of them can show that.
 */
export async function seedChildWithSpec(): Promise<ChildWithSpecSeed> {
  await createProject({ key: PROJECT, name: "Issue layout" });
  const parentRequirements = ["r-parent-1", "r-parent-2"] as const;
  const parent = await createIssue({
    force: true,
    project: PROJECT,
    spec: `${[
      statementBlock(parentRequirements[0], "The release ships on one day for every customer."),
      statementBlock(parentRequirements[1], "The release can be rolled back within ten minutes."),
    ].join("\n\n")}\n`,
    title: "The spring release ships as one",
  });
  const childRequirements = ["r-child-1", "r-child-2"] as const;
  const child = await createIssue({
    force: true,
    parent: parent.key,
    project: PROJECT,
    spec: `${[
      ':::callout{#summary title="What this issue is"}\n' +
        "The release notes ship with the release. They name every change a customer sees. " +
        "Each change links the pull request that made it.\n:::",
      statementBlock(childRequirements[0], "The notes list each change under the part it changed."),
      statementBlock(childRequirements[1], "The notes are published an hour before the release."),
    ].join("\n\n")}\n`,
    title: "The release notes ship with the spring release",
  });
  return {
    child: {
      key: child.key,
      requirements: childRequirements,
      specSlug: await specSlugOf(child.primary_artifact_id),
    },
    parent: {
      key: parent.key,
      requirements: parentRequirements,
      specSlug: await specSlugOf(parent.primary_artifact_id),
    },
  };
}

/** The issue of `seedTallSpec`: its key, its spec's artifact id and slug, the block ids of the
 *  statements its spec holds, in document order, and the id of its one comment. */
export interface TallSpecSeed {
  readonly commentId: string;
  readonly key: string;
  readonly requirements: readonly string[];
  readonly specArtifactId: string;
  readonly specSlug: string;
}

/** The words of each statement of `seedTallSpec`, in document order: thirty, each a sentence or
 *  two of its own. */
const TALL_SPEC_WORDS = [
  "An export holds every document of the workspace as it stood when the export began. A document changed while it runs waits for the next export.",
  "The export is one archive file, named for the workspace and the day it was made, so that two exports made on one day never share a name.",
  "Each document in the archive keeps the path it had in the workspace, with its folders written out as folders of the archive.",
  "A document's history travels with it: every saved version, oldest first, each with the name of the person who saved it and when.",
  "Comments are exported beside the document they were left on, in a file of their own, with each reply placed under the comment it answers.",
  "Pictures and other attached files are copied into the archive whole. A file over two gigabytes is listed by name and left out, and the list says why.",
  "The person who starts an export sees how far it has gone, as a count of the documents written out of the documents it will hold.",
  "An export that stops part way can be started again, and it goes on from the last document it finished rather than from the first.",
  "Only an owner of the workspace can start an export. Anyone else who opens the export page is told who the owners are and how to reach them.",
  "When the archive is ready, the person who started it gets one email holding a link to it. The link works for seven days and then stops.",
  "The link asks the person who opens it to sign in, and it opens only for the person who started the export or for another owner.",
  "Every export is written to the workspace's activity list: who started it, when it finished, and how large the archive came out.",
  "A workspace holds at most three exports at once. Starting a fourth removes the oldest, and the page says so before the fourth begins.",
  "Documents an owner marked private are left out of every export another owner starts, and the archive says how many were left out.",
  "Deleted documents still in the bin are not exported. Restoring one from the bin puts it into the next export like any other document.",
  "The archive opens with the tools every common computer already has, so no one needs to install anything to read what they exported.",
  "Each document is written in the format it was made in, with a copy in plain text beside it for any reader that cannot open that format.",
  "Names of people in the archive are the names they had when the export ran, even where a person has since left the workspace.",
  "Links between documents point to the linked document's place inside the archive, so they still work once the archive is unpacked elsewhere.",
  "A link to a page outside the workspace is kept as it was written. The export never follows it and never copies what it points to.",
  "An export of a workspace of ten thousand documents finishes within an hour on the plan that every paying workspace is on.",
  "While an export runs, the workspace stays open for writing. No one is asked to stop work, and no document is locked for the export.",
  "If the archive cannot be written, the person who started it is told in one sentence what went wrong and whether starting again will help.",
  "The settings page lists the exports the workspace holds, newest first, each with its size, its date and the person who started it.",
  "An owner can delete an export before its link runs out. The link then stops at once, for anyone who already holds it as well.",
  "The archive carries a short file at its top that says what the export holds, when it ran, and how to read the files inside it.",
  "Exports are kept in the same country as the workspace itself, and they never leave it while they wait to be downloaded.",
  "Every archive is checked once it is written, and an archive that does not open cleanly is never offered to anyone for download.",
  "A workspace on the free plan can export once a month. The page says on which day the next export can be started.",
  "Turning exports off for a workspace removes every export it holds and stops every link to one, and the activity list records who did it.",
] as const;

/**
 * One issue whose spec holds thirty statements, each a sentence or two of words of its own, so
 * that the Spec view's statements alone stand well past one screen at 1440 by 900, and one
 * comment on the issue itself, which answers no other and is anchored to nothing, in the project
 * the issue-layout seeds make. A row that lands on a row of the view below the first screen
 * reads it: the file's other seeds draw too few statements to put a row below the screen.
 */
export async function seedTallSpec(): Promise<TallSpecSeed> {
  await createProject({ key: PROJECT, name: "Issue layout" });
  const statements = TALL_SPEC_WORDS.map((words, index) => ({
    id: requirementBlockId(index + 1),
    words,
  }));
  const issue = await createIssue({
    force: true,
    project: PROJECT,
    spec: `${statements.map(({ id, words }) => statementBlock(id, words)).join("\n\n")}\n`,
    title: "A workspace can be exported as one archive",
  });
  const comment = await createComment(issue.key, {
    body: "Should an export hold the documents still in the bin, for an owner who asks for them?",
  });
  return {
    commentId: comment.id,
    key: issue.key,
    requirements: statements.map(({ id }) => id),
    specArtifactId: issue.primary_artifact_id,
    specSlug: await specSlugOf(issue.primary_artifact_id),
  };
}

// ---------------------------------------------------------------------------------------------
// The readers.
// ---------------------------------------------------------------------------------------------

/** The issue's three sentences under "In short", wherever they are drawn. */
export function threeThings(page: Page): Locator {
  return page.getByTestId("issue-three-things");
}

/** The author named inside `scope`, where the page names one. */
export function rowAuthor(scope: Locator): Locator {
  return scope.locator("[data-node-author]");
}

/** The rows of the Contents list, wherever it is drawn: the pane, or the Outline sheet. */
export function contentsEntries(scope: Locator): Locator {
  return scope.locator("[data-contents-entry]");
}

/** The Focus branch toggle above the Contents list in `scope`. */
export function focusBranchControl(scope: Locator): Locator {
  return scope.getByRole("button", { exact: true, name: CONTROL_LABELS.focus_branch });
}

/**
 * What is wrong with the line a Contents entry draws of `text`, read from the browser's own
 * layout in the face the page draws in now: the line must fit the box the entry leaves it, and
 * must hold the next word of `text` too wherever that word and the ellipsis fit there (R1's
 * first-line rule, cut at a word to the entry's width). Empty where the line is the right one.
 */
export function contentsLineFaults(entry: Locator, text: string): Promise<string[]> {
  return entry.evaluate((element, text) => {
    const line = element.querySelector<HTMLElement>("[data-contents-line]");
    if (line === null) return ["the entry draws no line"];
    const shown = line.textContent ?? "";
    const words = shown.endsWith("…") ? shown.slice(0, -1) : shown;
    if (!text.startsWith(words)) {
      return [`the entry draws "${shown}", which "${text}" does not begin with`];
    }
    const room = line.getBoundingClientRect().width;
    const drawn = document.createRange();
    drawn.selectNodeContents(line);
    const width = drawn.getBoundingClientRect().width;
    const faults: string[] = [];
    if (width > room + 1) {
      faults.push(`"${shown}" is ${width.toFixed(1)} px, past its ${room.toFixed(1)} px box`);
    }
    const next = text.slice(words.length).trim().split(" ")[0] ?? "";
    if (next === "") return faults;
    // The line with its next word, laid out beside it in the same face and size.
    const probe = line.cloneNode(false) as HTMLElement;
    probe.textContent = `${words} ${next}…`;
    probe.style.cssText =
      "position: absolute; visibility: hidden; flex: none; width: auto; white-space: nowrap";
    line.after(probe);
    const longer = probe.getBoundingClientRect().width;
    probe.remove();
    if (longer <= room - 2) {
      faults.push(
        `"${shown}" leaves out "${next}", though "${words} ${next}…" is ${longer.toFixed(1)} px, inside its ${room.toFixed(1)} px box`
      );
    }
    return faults;
  }, text);
}

/** One Decided by cell of the Spec view: whose words it draws, the statements its bracket
 *  spans, and its drawn text. */
export interface DecidedByCell {
  readonly ids: readonly string[];
  readonly said: string | null;
  readonly text: string;
}

/** Each Decided by cell of the Spec view, in the order they are drawn. */
export function decidedByCells(page: Page): Promise<DecidedByCell[]> {
  return page.evaluate(() =>
    [...document.querySelectorAll("[data-spec-cell]")].map((cell) => ({
      ids: (cell.getAttribute("data-spec-statements") ?? "").split(" ").filter(Boolean),
      said: cell.getAttribute("data-said"),
      text: (cell.textContent ?? "").replace(/\s+/g, " ").trim(),
    }))
  );
}

/** The one line under each statement of the Spec view that names the decision that shaped it,
 *  by statement id; `""` for a statement that draws none. */
export function becauseLines(page: Page): Promise<Record<string, string>> {
  return page.evaluate(() =>
    Object.fromEntries(
      [...document.querySelectorAll("[data-spec-statement]")].map((row) => [
        row.getAttribute("data-node-id") ?? "",
        (row.querySelector("[data-spec-because]")?.textContent ?? "").replace(/\s+/g, " ").trim(),
      ])
    )
  );
}

/** Where a box of the page is, in the viewport's pixels. */
export interface SpecBox {
  readonly bottom: number;
  readonly left: number;
  readonly right: number;
  readonly top: number;
  readonly width: number;
}

/** One run of the Spec view, `[data-spec-run]`: the box of its decision cell and of its first
 *  statement, each null where the run draws none. */
export interface SpecRun {
  readonly cell: SpecBox | null;
  readonly first: SpecBox | null;
}

/** Each run of the Spec view, in the order they are drawn. */
export function specRuns(page: Page): Promise<SpecRun[]> {
  return page.locator("[data-issue-page] [data-spec-run]").evaluateAll((runs) =>
    runs.map((run) => {
      const box = (element: Element | null) => {
        if (element === null) return null;
        const { bottom, left, right, top, width } = element.getBoundingClientRect();
        return { bottom, left, right, top, width };
      };
      return {
        cell: box(run.querySelector("[data-spec-cell]")),
        first: box(run.querySelector("[data-spec-statement]")),
      };
    })
  );
}

/** The text of each `[data-page-chrome]` element inside `scope`, which is the page's own words,
 *  with what a nested `[data-author-text]` holds left out: a person's or an agent's words inside
 *  the page's own, such as the count in "more · 180 words", are theirs (R7). */
export function ownWords(scope: Locator): Promise<string[]> {
  return scope.locator("[data-page-chrome]").evaluateAll((elements) =>
    elements.map((element) => {
      const own = element.cloneNode(true) as Element;
      for (const theirs of own.querySelectorAll("[data-author-text]")) theirs.replaceWith(" ");
      return own.textContent ?? "";
    })
  );
}

/** Fails unless `row`, the row an address landed on, stands inside the viewport: its top at or
 *  below the viewport's top and its bottom at or above the viewport's bottom. Each failure names
 *  `what` landed there, the row's top and bottom, and how tall the page is and how far it is
 *  scrolled, which together say where a row that left the screen went. */
export async function expectInViewport(page: Page, row: Locator, what: string): Promise<void> {
  const box = await row.boundingBox();
  expect(box, `${what} draws no box for the focused row`).not.toBeNull();
  const height = page.viewportSize()?.height ?? Number.NaN;
  const { scrollHeight, scrollY } = await page.evaluate(() => ({
    scrollHeight: document.documentElement.scrollHeight,
    scrollY: window.scrollY,
  }));
  const top = box?.y ?? Number.NaN;
  const bottom = top + (box?.height ?? Number.NaN);
  const where = `${what}: the focused row stands from ${top} px to ${bottom} px in a viewport ${height} px tall, on a page ${scrollHeight} px tall scrolled ${scrollY} px`;
  expect(top, `${where}, its top above the viewport`).toBeGreaterThanOrEqual(0);
  expect(bottom, `${where}, its bottom below the viewport`).toBeLessThanOrEqual(height);
}
