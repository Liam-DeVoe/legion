import type { MeInboxEntry, MeInboxResponse } from "../web/src/api/types";
import { setLiveSessions } from "./agents";
import {
  answerAsk,
  createAgentMessage,
  createAsk,
  createComment,
  createIssue,
  createMessage,
  createProject,
  declineAsk,
  getMeInbox,
  markThreadRead,
  patchIssue,
  replyToMessageDelivery,
} from "./api";
import { inboxPane, selectRow } from "./inbox-pane";
import { agentRowId, asSession, replyRowId, sessionActor, THREAD_SESSION } from "./inbox-threads";
import { drawnIds } from "./inbox-view";
import { inboxRead } from "./pick-writes";
import { sql } from "./psql";
import { sqlLiteral } from "./seed";
import { expandedState } from "./settled";
import { expect, type Page, type Request, test } from "./test";
import { asUser } from "./users";

/**
 * The browser rows of the one read, GET /api/v1/me/inbox.
 *
 * Everything here is made up: this repository is public, so nothing names a real person, host or
 * project.
 */

/** Somebody else, who writes in the reader's threads and holds no row of their own. */
const other = "bob";
/** A person a reference can name. A reference names a person by their login as Dispatch stores
 *  one, an email lowercased (`dispatch://person/<login>`), and no bare login, so `alice` and
 *  `bob`, the harness's own logins, are nobody a mention reaches. Dev sign-in records any login
 *  it is given, so this one signs in as they do. */
const named = "person-1@example.com";

/** Holds that the one read ties `login` to the comment `commentId` by a mention: the comment's
 *  mention of them landed as a mentions tuple into their person. Every row that asserts something
 *  of a mention reads this first, so a text grammar that leaves the comment naming nobody fails
 *  here, with that cause named, rather than as a row the page happens not to list. */
async function expectMentioned(login: string, commentId: string): Promise<void> {
  await expect
    .poll(
      async () => {
        const read = await getMeInbox({ kind: "mentions" }, { login });
        const entry = (read.entries ?? []).find(
          (candidate) => candidate.node.kind === "comment" && candidate.node.id === commentId
        );
        return entry?.relations?.map(({ direction, kind }) => `${kind} ${direction}`) ?? [];
      },
      {
        message: `the one read ties ${login} to comment ${commentId} by its mention of them (a reference names a person by an email-shaped login alone)`,
      }
    )
    .toContain("mentions inbound");
}

/** The row ids the page draws for the entries of one read of its own: an ask's own id, and a
 *  thread's `reply:` or `agent:` id, as the list names its rows (`rowId`). */
function pageRowIds(entries: readonly MeInboxEntry[] | null): string[] {
  return (entries ?? []).flatMap((entry) => {
    if (entry.ask !== undefined) return [entry.ask.id];
    if (entry.agent !== undefined) return [agentRowId(entry.agent.thread_id)];
    return entry.thread === undefined ? [] : [replyRowId(entry.thread.thread_id)];
  });
}

/** Narrows the page's own Type filter to the value named `label`, or takes that value off again:
 *  the option toggles. The bar's Filter control and the Type trigger each say whether what they
 *  open is open, read once the page has drawn them (`expandedState`). */
async function toggleType(page: Page, label: string): Promise<void> {
  const filter = page.locator("[data-inbox-filter]");
  if ((await expandedState("the Inbox's Filter", filter)) === "shut") await filter.click();
  const trigger = page.getByRole("button", { name: "Filter by type" });
  if ((await expandedState("the Type filter", trigger)) === "shut") await trigger.click();
  await page
    .getByRole("listbox", { name: "Type options" })
    .getByRole("option", { name: new RegExp(`^${label}, `) })
    .click();
  await page.keyboard.press("Escape");
}

test("the inbox lists everything that reached a person in one list", async ({ browser }) => {
  // The page reads as a person a reference can name (`named`), so the mention below reaches
  // them; every write of theirs is made as them.
  const asReader = { login: named };
  await setLiveSessions([THREAD_SESSION]);
  await createProject({ key: "CORE", name: "Core" }, asReader);
  const issue = await createIssue({ project: "CORE", title: "Release checklist" }, asReader);
  const closed = await createIssue({ project: "CORE", title: "Old rollout notes" }, asReader);
  // An open ask a session asks on the first issue, which waits on the reader, its assignee.
  await patchIssue(issue.key, { assignee: named }, asReader);
  const ask = await createAsk(issue.key, { question: "Approve the release window?" }, asSession());
  // A comment thread the reader started, with the session's reply under it.
  const comment = await createComment(issue.key, { body: "Is the checklist complete?" }, asReader);
  await createComment(
    issue.key,
    { body: "Two items are still open.", reply_to: comment.id },
    asSession()
  );
  // An issue message thread the reader started, with somebody else's reply under it.
  const message = await createMessage(issue.key, { body: "Who owns the rollback step?" }, asReader);
  await createMessage(
    issue.key,
    { body: "I own the rollback step.", in_reply_to: message.id },
    { login: other }
  );
  // A direct message to the session, with that session's reply.
  const direct = (
    await createAgentMessage(
      THREAD_SESSION.session_id,
      { body: "Please check the staging gate.", delivery: "btw" },
      asReader
    )
  ).id;
  await replyToMessageDelivery(
    direct,
    { attempt: 1, body: "The staging gate is open." },
    sessionActor()
  );
  // A comment by somebody else that names the reader by the address the text grammar reads
  // (`dispatch://person/<login>`), and one the reader wrote themselves.
  const mention = await createComment(
    issue.key,
    { body: `dispatch://person/${named}, please read the checklist.` },
    { login: other }
  );
  await expectMentioned(named, mention.id);
  const own = await createComment(issue.key, { body: "A note to myself." }, asReader);
  // On the second issue, closed: a thread the reader wrote in and has read. Its shape is
  // somebody else's root and the reader's reply under it, with nothing written after: today's
  // Inbox lists a thread of the reader's from the first write somebody else makes after theirs,
  // closed issue or not, so this thread is one the page does not list by today's rules.
  const closedRoot = await createComment(
    closed.key,
    { body: "Are the rollout notes final?" },
    { login: other }
  );
  await createComment(closed.key, { body: "They are final.", reply_to: closedRoot.id }, asReader);
  await markThreadRead(closedRoot.id, new Date().toISOString(), asReader);
  await patchIssue(closed.key, { status: "done" }, asReader);

  const context = await asUser(browser, named);
  try {
    const page = await context.newPage();
    // Every read of the one read the page sends, to read what it asked for.
    const oneReads: URL[] = [];
    page.on("request", (request: Request) => {
      const url = new URL(request.url());
      if (url.pathname === "/api/v1/me/inbox") oneReads.push(url);
    });
    const answered = page.waitForResponse((response) => inboxRead(new URL(response.url())));
    await page.goto("/");
    const entries = ((await (await answered).json()) as MeInboxResponse).entries;

    // One list of four rows: the open ask, the session's reply in the comment thread, the reply
    // in the issue message thread and the session's reply to the direct message, the replies
    // among the asks.
    const rows = {
      ask: ask.id,
      comment: replyRowId(comment.id),
      direct: agentRowId(direct),
      message: replyRowId(message.id),
    };
    const four = Object.values(rows).sort();
    await expect
      .poll(async () => (await drawnIds(page)).sort(), { message: "the rows of the one list" })
      .toEqual(four);
    // Nothing reached the reader that the page does not list: their own comment, the thread on
    // the closed issue and the mention of them are no rows of the page.
    for (const absent of [replyRowId(own.id), replyRowId(closedRoot.id), replyRowId(mention.id)]) {
      await expect(page.locator(`[data-inbox-row="${absent}"]`), absent).toHaveCount(0);
    }

    // Each row carries its first line, its author, and a link that opens the thread at its root.
    const refOf = (id: string) => {
      const entry = (entries ?? []).find((candidate) => candidate.node.id === id);
      return entry?.thread?.ref ?? entry?.agent?.ref;
    };
    const expected: readonly {
      id: string;
      firstLine: string;
      author: string;
      href: (href: string) => boolean;
    }[] = [
      {
        author: THREAD_SESSION.title,
        firstLine: "Approve the release window?",
        href: (href) => href.endsWith(`/asks/${ask.id}`),
        id: rows.ask,
      },
      {
        author: THREAD_SESSION.title,
        firstLine: "Is the checklist complete?",
        href: (href) => href === refOf(comment.id) && href.includes(comment.id),
        id: rows.comment,
      },
      {
        author: other,
        firstLine: "Who owns the rollback step?",
        href: (href) => href === refOf(message.id) && href.includes(message.id),
        id: rows.message,
      },
      {
        author: THREAD_SESSION.title,
        firstLine: "Please check the staging gate.",
        href: (href) => href === refOf(direct),
        id: rows.direct,
      },
    ];
    for (const row of expected) {
      const listed = page.locator(`[data-inbox-row="${row.id}"]`);
      await expect(listed, `${row.id}'s first line`).toContainText(row.firstLine);
      // The owner link names the thread at its root once the row has drawn it, read until it does.
      const owner = listed.locator("[data-inbox-owner]").first();
      await expect
        .poll(async () => row.href((await owner.getAttribute("href")) ?? ""), {
          message: `${row.id}'s link opens its thread at the root`,
        })
        .toBe(true);
      await selectRow(page, row.id);
      await expect(inboxPane(page), `${row.id}'s author in the reading pane`).toContainText(
        row.author
      );
    }

    // The page's own Type filter narrows the one list in the browser: Threads holds the two
    // Reply rows, and Agent message the one Agent row, out of the same list.
    await toggleType(page, "Threads");
    await expect
      .poll(async () => (await drawnIds(page)).sort())
      .toEqual([rows.comment, rows.message].sort());
    await toggleType(page, "Threads");
    await toggleType(page, "Agent message");
    await expect.poll(() => drawnIds(page)).toEqual([rows.direct]);
    await toggleType(page, "Agent message");
    await expect.poll(async () => (await drawnIds(page)).sort()).toEqual(four);

    // No narrowing reached the server: every read the page sent carries no relation kind.
    expect(oneReads.length, "the page's reads of the one read").toBeGreaterThan(0);
    for (const url of oneReads) {
      expect(url.searchParams.has("kind"), `${url.pathname}${url.search}`).toBe(false);
    }

    // The old Replies address lands on this list, narrowed to its threads, and on no page of its
    // own.
    await page.goto("/replies");
    await expect(page).toHaveURL(
      (url) => url.pathname === "/" && url.searchParams.get("type") === "reply"
    );
    await expect
      .poll(async () => (await drawnIds(page)).sort())
      .toEqual([rows.comment, rows.message].sort());
  } finally {
    await context.close();
  }
});

test("an answered ask stays in the threads a person takes part in", async ({ browser }) => {
  // The reader is a person a reference can name (`named`), so the mention below reaches them;
  // every write of theirs is made as them.
  const asReader = { login: named };
  await createProject({ key: "CORE", name: "Core" }, asReader);
  const issue = await createIssue({ project: "CORE", title: "Rollout gates" }, asReader);
  await patchIssue(issue.key, { assignee: named }, asReader);
  const asked = (question: string) =>
    createAsk(issue.key, { options: [{ label: "Yes" }, { label: "No" }], question }, asSession());
  const answeredByReader = await asked("Open the gate on Monday?");
  const answeredByOther = await asked("Keep the old gate?");
  const retracted = await asked("Freeze the schedule?");
  const declined = await asked("Page the on-call?");
  await answerAsk(answeredByReader.id, { expected_edited_at: null, selected: ["Yes"] }, asReader);
  await answerAsk(
    answeredByOther.id,
    { expected_edited_at: null, selected: ["No"] },
    { login: other }
  );
  await answerAsk(retracted.id, { expected_edited_at: null, selected: ["Yes"] }, asReader);
  // No route retracts an answer: an answered ask is closed, a resolve takes only an open one, and
  // an issue's reopen opens again only the asks its close withdrew. So the seed answers the third
  // ask through the API, which appends its ask.answered event and its answer tuples, and then
  // takes the answer off its row here, as the state a retraction leaves: resolved as retracted,
  // with no current answer, while the event and the tuples stay.
  await sql(`
    update asks
    set answer = null, state = 'resolved',
        resolution = jsonb_build_object(
          'kind', 'retracted', 'reason', 'The schedule moved.', 'at', now(),
          'actor', jsonb_build_object('kind', 'user', 'id', ${sqlLiteral(named)}))
    where id = ${sqlLiteral(retracted.id)}::uuid`);
  await declineAsk(declined.id, { note: "Not mine to call." }, asReader);
  // A thread the reader wrote in and has read, on the same issue: somebody else's root, the
  // reader's reply under it, nothing written after, so today's Inbox does not list it.
  const thread = await createComment(
    issue.key,
    { body: "Is the gate log kept?" },
    { login: other }
  );
  await createComment(issue.key, { body: "It is kept.", reply_to: thread.id }, asReader);
  await markThreadRead(thread.id, new Date().toISOString(), asReader);
  // A thread the reader was only mentioned in, and never wrote in: somebody else's comment that
  // names them by address. Its mention lands as a mentions tuple into their person.
  const mentioned = await createComment(
    issue.key,
    { body: `dispatch://person/${named}, the gate log moved.` },
    { login: other }
  );
  await expectMentioned(named, mentioned.id);
  await patchIssue(issue.key, { status: "done" }, asReader);

  // The one read asked as a caller, for the threads the reader takes part in.
  const taking = await getMeInbox({ kind: "participates_in" }, asReader);
  const tied = (kind: string, id: string) =>
    (taking.entries ?? []).find((entry) => entry.node.kind === kind && entry.node.id === id);
  const asks = [answeredByReader, answeredByOther, retracted, declined];
  const left = asks.find((ask) => tied("ask", ask.id) === undefined);
  expect(
    left,
    `the ask "${left?.question}" (${left?.id}) left the threads the reader takes part in`
  ).toBeUndefined();
  for (const ask of asks) {
    expect(
      tied("ask", ask.id)?.relations?.map(({ direction, kind }) => `${kind} ${direction}`),
      `what holds the reader in "${ask.question}"`
    ).toContain("participates_in outbound");
  }
  expect(
    tied("comment", thread.id)?.relations?.map(({ direction, kind }) => `${kind} ${direction}`),
    "what holds the reader in the thread they wrote in and read"
  ).toContain("participates_in outbound");

  // The page lists neither thread: these are answers of the read for its callers, and the Inbox
  // lists the rows it lists today, which are the rows its own read answered.
  const context = await asUser(browser, named);
  try {
    const page = await context.newPage();
    const answered = page.waitForResponse((response) => inboxRead(new URL(response.url())));
    await page.goto("/");
    const drawn = pageRowIds(((await (await answered).json()) as MeInboxResponse).entries);
    // The list the page draws once both its reads have answered - rows or its empty state - is
    // there before the rows are compared: an empty answer would otherwise match the page before
    // it drew anything.
    await expect(page.getByTestId("inbox-list"), "the Inbox page drew its list").toBeAttached();
    await expect.poll(async () => (await drawnIds(page)).sort()).toEqual([...drawn].sort());
    expect(drawn, "the rows the page draws").not.toContain(replyRowId(thread.id));
    expect(drawn, "the rows the page draws").not.toContain(replyRowId(mentioned.id));
    for (const absent of [replyRowId(thread.id), replyRowId(mentioned.id)]) {
      await expect(page.locator(`[data-inbox-row="${absent}"]`), absent).toHaveCount(0);
    }
  } finally {
    await context.close();
  }
});
