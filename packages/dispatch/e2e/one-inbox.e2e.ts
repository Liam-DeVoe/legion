import { setLiveSessions } from "./agents";
import {
  createAsk,
  createComment,
  createIssue,
  createMessage,
  createProject,
  dashboardOrigin,
  markThreadRead,
  patchIssue,
  sessionCookie,
} from "./api";
import { type BaseServer, baseBuild, bootBaseBuild, stackCommand } from "./base-build";
import {
  asSession,
  seedAgentThread,
  seedAskThread,
  seedCommentThread,
  THREAD_SESSION,
} from "./inbox-threads";
import { sql } from "./psql";
import { sqlLiteral } from "./seed";
import { expect, type Page, test } from "./test";
import { asUser } from "./users";

/**
 * dispatch://INFRA-1152's browser rows of the one read, GET /api/v1/me/inbox.
 *
 * Everything here is made up: this repository is public, so nothing names a real person, host or
 * project.
 */

/** The person whose reads the comparison is planted a mismatch in, and who reads the page. */
const reader = "alice";
/** Somebody else, who writes in the reader's threads and holds no row of their own. */
const other = "bob";
/** The person the first comparison does not reach. */
const excluded = "carol";

/** The links of the comment reply chain at the outbox walk's bound, the depth
 *  `internal/dispatch/inboxcompare`'s ChainLinks copies. */
const chainLinks = 32;

/** The corpus the comparison reads: what c-one-list seeds, and the shapes the gate requires
 *  beside it, every one through the API as a person and a session write it but the reply chain,
 *  which only the release before could write (POST comments climbs reply_to to the thread's
 *  head). The chain is written as that release wrote it, and the backfill then runs over it as a
 *  deployment's runs before anybody reads the new read. */
async function seedCorpus(): Promise<{ issue: string }> {
  await setLiveSessions([THREAD_SESSION]);
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "Release checklist" });
  const closed = await createIssue({ project: "CORE", title: "Old rollout notes" });
  // An open ask a session asks, which waits on the reader, the project's default assignee.
  await createAsk(issue.key, { question: "Approve the release window?" }, asSession());
  // A comment thread the reader started, with a session's reply under it.
  await seedCommentThread(issue.key, "Is the checklist complete?", "Two items are still open.");
  // An issue message thread the reader started, with somebody else's reply under it.
  const root = await createMessage(issue.key, { body: "Who owns the rollback step?" });
  await createMessage(issue.key, { body: "I own it.", in_reply_to: root.id }, { login: other });
  // A direct message to a session, with that session's reply.
  await seedAgentThread({ body: "Please check the staging gate.", replies: ["The gate is open."] });
  // An ask the reader answered, with the session's reply in its thread.
  await seedAskThread(issue.key, "Ship on Friday?", "Friday it is.");
  // A comment by somebody else that names the reader, and one the reader wrote themselves.
  await createComment(issue.key, { body: `person:${reader}, please read this.` }, { login: other });
  await createComment(issue.key, { body: "A note to myself." });
  // A thread on the closed issue the reader wrote in and has already read.
  const read = await createComment(closed.key, { body: "Are the notes final?" });
  await createComment(closed.key, { body: "They are.", reply_to: read.id }, { login: other });
  await markThreadRead(read.id, new Date().toISOString());
  await patchIssue(closed.key, { status: "done" });
  // The excluded person signs in, so the database holds them among its people.
  await sessionCookie(excluded);
  await sql(`
    do $$
    declare
      parent uuid;
      link uuid;
    begin
      for depth in 0..${chainLinks} loop
        insert into comments (issue_key, author, body, reply_to, created_at)
        values (${sqlLiteral(issue.key)},
                case when depth % 2 = 0 then jsonb_build_object('kind', 'user', 'id', ${sqlLiteral(reader)})
                     else jsonb_build_object('kind', 'session', 'id', ${sqlLiteral(THREAD_SESSION.session_id)}) end,
                'A link of the chain, ' || depth || ' deep.', parent,
                now() - interval '1 hour' + depth * interval '1 second')
        returning id into link;
        parent := link;
      end loop;
    end $$`);
  const backfill = await stackCommand(["backfill-graph-store"]);
  expect(backfill.code, `backfill-graph-store:\n${backfill.stdout}${backfill.stderr}`).toBe(0);
  return { issue: issue.key };
}

/** A thread of the reader's as the release before writes it, through no writer of this build:
 *  their root and somebody else's reply under it, which the old trigger records as a Reply row of
 *  theirs and to which this build's writer appends no tuple. It is planted after the backfill ran,
 *  as a write an older build makes during a roll, so the one read does not answer it: the
 *  comparison's mismatch. */
async function plantMismatch(issue: string): Promise<{ root: string; reply: string }> {
  const author = (login: string) =>
    `jsonb_build_object('kind', 'user', 'id', ${sqlLiteral(login)})`;
  // Each insert answers its id from a select, since psql prints an insert's own command tag
  // beside what its returning clause answers.
  const root = await sql(`
    with written as (
      insert into comments (issue_key, author, body, created_at)
      values (${sqlLiteral(issue)}, ${author(reader)}, 'Written by the release before.', now())
      returning id)
    select id from written`);
  const reply = await sql(`
    with written as (
      insert into comments (issue_key, author, body, reply_to, created_at)
      values (${sqlLiteral(issue)}, ${author(other)}, 'Answered by the release before.',
              ${sqlLiteral(root)}::uuid, now() + interval '1 second')
      returning id)
    select id from written`);
  return { reply, root };
}

/** Takes the planted thread away again, with the Reply rows the old trigger wrote for it. */
async function clearMismatch(planted: { root: string; reply: string }): Promise<void> {
  await sql(
    `delete from user_threads where thread_id = ${sqlLiteral(planted.root)}::uuid`,
    `delete from comments where id in (${sqlLiteral(planted.reply)}::uuid, ${sqlLiteral(planted.root)}::uuid)`
  );
}

/** The four things the page states, read from the page as a person reads them. */
async function expectPageStates(
  page: Page,
  want: { differences: number; people: number; uncovered: number; met: boolean }
): Promise<void> {
  await page.goto("/inbox/comparison");
  await expect(page.getByRole("heading", { level: 1, name: "Inbox comparison" })).toBeVisible();
  const run = page.getByRole("definition");
  await expect(run).not.toHaveCount(0);
  await expect
    .soft(page.getByTestId("comparison-differences"), "the difference count")
    .toHaveText(String(want.differences));
  await expect
    .soft(page.getByTestId("comparison-people-checked"), "the people checked")
    .toHaveText(String(want.people));
  await expect
    .soft(page.getByTestId("comparison-people-uncovered"), "the uncovered count")
    .toHaveText(String(want.uncovered));
  await expect
    .soft(page.getByTestId("comparison-gate"), "the sentence that says whether the gate is met")
    .toHaveText(want.met ? "The gate is met." : "The gate is not met.");
}

test("the comparison page states the gate in plain words", async ({ browser }, testInfo) => {
  // The base build is compiled once per run, which the first row to need it pays for.
  test.setTimeout(1_200_000);
  const build = await baseBuild();
  test.skip("skip" in build, "skip" in build ? build.skip : "");
  if ("skip" in build) return;
  const { issue } = await seedCorpus();
  const planted = await plantMismatch(issue);
  const people = Number(await sql("select count(*) from people"));

  let base: BaseServer | undefined;
  const alice = await asUser(browser, reader);
  try {
    base = await bootBaseBuild(build.binary);
    const compare = async (exclude: readonly string[]) => {
      const run = await stackCommand([
        "inbox-compare",
        "--old-url",
        base?.url ?? "",
        "--new-url",
        dashboardOrigin,
        ...exclude.flatMap((login) => ["--exclude", login]),
      ]);
      await testInfo.attach(`inbox-compare ${exclude.length === 0 ? "clean" : "excluding"}`, {
        body: `exit ${run.code}\n${run.stdout}${run.stderr}`,
        contentType: "text/plain",
      });
      return run;
    };
    const page = await alice.newPage();

    const mismatched = await compare([excluded]);
    expect
      .soft(mismatched.code, `the first comparison:\n${mismatched.stdout}${mismatched.stderr}`)
      .toBe(1);
    await expectPageStates(page, { differences: 1, met: false, people, uncovered: 1 });

    await clearMismatch(planted);
    const clean = await compare([]);
    expect.soft(clean.code, `the second comparison:\n${clean.stdout}${clean.stderr}`).toBe(0);
    await expectPageStates(page, { differences: 0, met: true, people, uncovered: 0 });
  } finally {
    await alice.close();
    await base?.stop();
  }
});
