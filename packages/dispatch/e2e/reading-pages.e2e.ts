/**
 * The reading list's walk (INFRA-1702's R9, `e2e/reading-rows.ts`) over the two pages whose text
 * is no record of the reference graph and still reads through the one renderer: a component's
 * prose on its project's Architecture page, and a priority's description on the priority's own
 * page. Neither text is a `TextSource`, so a `#id` in it names nothing, but a bare key, an
 * address and a picked party in it are read as every stored text reads them (R6): each draws as
 * its issue's key and current title or as `@` and the session's name, and none is left as a raw
 * `dispatch://`. Both hosts are a page a reader reads, not a link or a button, so each of the
 * three is a link there.
 *
 * Each row writes `readingText` where its page reads it from: into the component model file the
 * fake GitHub serves, whose body is the component's prose, and into the priority's description
 * through the API.
 */
import { createPriority, putArchitectureSource, syncArchitectureSource } from "./api";
import { seedFakeGithub } from "./fake-github-helpers";
import { expectReadsReferences, readingText, seedReadingWorld } from "./reading-rows";
import { COMPONENT_ID, COMPONENT_REPO, COMPONENT_TITLE, RENDERED_BODY } from "./reference-surfaces";
import { expect, test } from "./test";

// SLOW(>=2s): the world's three issues, the fake GitHub's model read in by a source sync, and the
// Architecture page drawn with the component's details open.
test("component-prose: a component's prose draws a stored key, an address and a party through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  // CORE's architecture as reference-surfaces.ts seeds it: the fake GitHub serves the model and
  // the source sync reads it in. The model file's body, under its front matter, is the
  // component's prose, so that body is the text the row reads.
  await seedFakeGithub({
    [COMPONENT_REPO]: {
      contents: "read",
      files: {
        [`${COMPONENT_ID}.md`]: `---\ntitle: ${COMPONENT_TITLE}\n---\n${readingText(world)}\n`,
      },
      installation_id: 101,
    },
  });
  await putArchitectureSource("CORE", { branch: "main", repo: COMPONENT_REPO });
  const source = await syncArchitectureSource("CORE", COMPONENT_REPO);
  expect(source.last_error).toBeNull();

  await page.goto(`/projects/CORE/architecture?component=${COMPONENT_ID}`);
  const details = page.getByTestId("component-details");
  await expect(details.getByRole("heading", { level: 2, name: COMPONENT_TITLE })).toBeVisible();
  // The prose is the one rendered body of the details: the Code & definition section's text,
  // with the repository, the paths and the dependencies drawn beside it rather than in it.
  await expectReadsReferences(details.locator(RENDERED_BODY), world, "link");
});

// SLOW(>=2s): the world's three issues and the priority's own page drawn with its description.
test("priority-description: a priority's description draws a stored key, an address and a party through the renderer", async ({
  page,
}) => {
  const world = await seedReadingWorld();
  const priority = await createPriority({
    description: readingText(world),
    owner: "alice",
    title: "Keep one build",
  });

  await page.goto(`/priorities/${priority.id}`);
  // The group the description is drawn in for a person who may write it, which the default
  // context's alice is: the rendered text and the button that opens its editor, which is drawn
  // only while it holds the focus. The rendered text is no control, so its links stay links.
  const description = page.getByRole("group", { exact: true, name: "Description of P1" });
  await expectReadsReferences(description, world, "link");
});
