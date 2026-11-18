import { expect, test } from "bun:test";
import {
  ghReview,
  type Kept,
  keptRead,
  latestReviews,
  type ReviewCall,
  sentReview,
  threads,
} from "./review-keep";

const HEAD = "9f1a2b3c4d5e6f708192a3b4c5d6e7f809a1b2c3";
/** The template of each review the calls below send. */
const TEMPLATE = {
  id: 7001,
  comment_id: 7101,
  commit_id: HEAD,
  user: { login: "rig[bot]" },
  author_association: "NONE",
  html_url: "https://github.com/example/widgets/pull/1000",
  pull_request_url: "https://api.github.com/repos/example/widgets/pulls/1000",
  added_lines: { "a.ts": 3 },
};
const SENT_AT = "2026-03-04T10:00:00Z";
const NONE: Kept = { reviews: [], comments: [] };

/** A `gh api` POST of a review: its `-f`/`-F` fields, or the text of its `--input` file. */
const api = (fields: Record<string, unknown>, input?: object): ReviewCall => ({
  prReview: false,
  events: [],
  body: undefined,
  input: input === undefined ? undefined : JSON.stringify(input),
  fields,
});
/** A `gh pr review` with the event flags it names and its body. */
const prReview = (events: string[], body?: string): ReviewCall => ({
  prReview: true,
  events,
  body,
  input: undefined,
  fields: {},
});
/** What the run keeps once `call` is sent over `kept`, or null where the rig cannot state it. */
function send(kept: Kept, call: ReviewCall): Kept | null {
  const sent = sentReview(TEMPLATE, call, kept, SENT_AT);
  return sent === null
    ? null
    : { reviews: [...kept.reviews, sent.review], comments: [...kept.comments, ...sent.comments] };
}
/** A read through a route of `mode`, with the call's `id` group and `--json` fields. */
const read = (
  kept: Kept,
  mode: "list" | "one" | "pr" | "comments" | "review_comments",
  call: { id?: string; fields?: string[]; commented?: boolean } = {},
  stdout: unknown = { number: 1000 }
) =>
  keptRead(
    mode,
    kept,
    { id: call.id, fields: call.fields, commented: call.commented ?? false },
    { stdout }
  );

// Catches: a review sent with no event kept as anything but GitHub's pending review (no
// submission time), an event recorded under another state, and a body changed on its way in.
test("a review is kept in the state its event names, with its body as sent, under the next id", () => {
  const pending = send(NONE, api({ body: "draft" }));
  const approved = send(NONE, prReview(["--approve"], "Looks right."));
  const changes = send(NONE, api({}, { event: "REQUEST_CHANGES", body: "Fix it." }));
  expect(
    [pending, approved, changes].map((kept) => {
      const review = kept?.reviews[0];
      return [review?.id, review?.state, review?.body, review?.submitted_at ?? null];
    })
  ).toEqual([
    [7001, "PENDING", "draft", null],
    [7001, "APPROVED", "Looks right.", SENT_AT],
    [7001, "CHANGES_REQUESTED", "Fix it.", SENT_AT],
  ]);
  const second = send(approved as Kept, api({ event: "COMMENT", body: "And this." }));
  expect(second?.reviews.map((review) => [review.id, review.state])).toEqual([
    [7001, "APPROVED"],
    [7002, "COMMENTED"],
  ]);
});

// Catches: a review whose answer the rig cannot state kept rather than refused: a second pending
// review, a review on another commit than the head, a changes request or a comment with no body,
// an event GitHub does not take, `gh pr review` with no event or two, an input that is no JSON
// object, and a pending review that carries comments.
test("a review the rig cannot state is kept nowhere", () => {
  const pending = send(NONE, api({ body: "draft" })) as Kept;
  expect([
    send(pending, api({ body: "again" })),
    send(NONE, api({ event: "APPROVE", commit_id: "0".repeat(40) })),
    send(NONE, api({ event: "REQUEST_CHANGES" })),
    send(NONE, api({ event: "COMMENT", body: "" })),
    send(NONE, api({ event: "DISMISS", body: "x" })),
    send(NONE, prReview([])),
    send(NONE, prReview(["--approve", "--comment"], "x")),
    send(NONE, prReview(["--comment"])),
    send(NONE, api({}, ["not", "an", "object"])),
    send(NONE, api({}, { comments: [{ path: "a.ts", line: 1, body: "y" }] })),
  ]).toEqual(Array(10).fill(null));
});

// Catches: an inline comment off the diff kept rather than refused (another file, a line past the
// file's added lines, the left side, a start line at or after its line, no body), and a kept one
// with another id, review, path, line, body or commit than GitHub records.
test("an inline comment is kept on a line the diff adds, and refused anywhere else", () => {
  const comment = (fields: Record<string, unknown>) =>
    send(NONE, api({}, { event: "COMMENT", body: "x", comments: [{ body: "y", ...fields }] }));
  expect([
    comment({ path: "b.ts", line: 1 }),
    comment({ path: "a.ts", line: 4 }),
    comment({ path: "a.ts", line: 2, side: "LEFT" }),
    comment({ path: "a.ts", line: 2, start_line: 2 }),
    comment({ path: "a.ts", line: 2, body: "" }),
  ]).toEqual(Array(5).fill(null));
  const kept = comment({ path: "a.ts", line: 3, start_line: 1 }) as Kept;
  expect(
    kept.comments.map((c) => [
      c.id,
      c.pull_request_review_id,
      c.path,
      c.start_line,
      c.line,
      c.body,
      c.commit_id,
      c.html_url,
    ])
  ).toEqual([[7101, 7001, "a.ts", 1, 3, "y", HEAD, `${TEMPLATE.html_url}#discussion_r7101`]]);
});

// Catches: a read of what the role sent that answers another review than the one it names, the
// comments of another review, or anything but GitHub's measured 404 for an id no review has, and
// the lists in another order than sent.
test("the reviews and their comments read back by list and by id, and an unknown id is GitHub's 404", () => {
  const first = send(NONE, api({ body: "draft" })) as Kept;
  const kept = send(
    first,
    api({}, { event: "COMMENT", body: "See.", comments: [{ path: "a.ts", line: 3, body: "here" }] })
  ) as Kept;
  expect(read(kept, "list")).toEqual({ stdout: kept.reviews });
  expect(read(kept, "comments")).toEqual({ stdout: kept.comments });
  expect(read(kept, "one", { id: "7002" })).toEqual({ stdout: kept.reviews[1] });
  expect(read(kept, "review_comments", { id: "7002" })).toEqual({ stdout: kept.comments });
  expect(read(kept, "review_comments", { id: "7001" })).toEqual({ stdout: [] });
  expect([
    read(kept, "one", { id: "7003" }),
    read(kept, "review_comments", { id: "6001" }),
  ]).toEqual([{ github: "review-404" }, { github: "review-comments-404" }]);
  expect(read(NONE, undefined as never)).toBeUndefined();
});

// Catches: `gh pr view --json reviews` that loses a review, shows it in REST's shape rather than
// gh's (GraphQL's bare bot login, node id, null submission time while pending), a latestReviews
// the rig cannot name answered rather than refused, and the comments field answered after the
// role posted a comment the stand-in does not keep.
test("a pull request read carries the reviews in gh's fields, and refuses what the rig cannot name", () => {
  const approved = send(NONE, prReview(["--approve"], "Looks right.")) as Kept;
  const view = read(approved, "pr", { fields: ["reviews", "latestReviews"] });
  expect(view).toEqual({
    stdout: {
      number: 1000,
      reviews: [ghReview(approved.reviews[0] as Kept["reviews"][number])],
      latestReviews: latestReviews(approved.reviews),
      comments: [],
    },
  });
  expect(ghReview(approved.reviews[0] as Kept["reviews"][number])).toMatchObject({
    id: "PRR_7001",
    author: { login: "rig" },
    state: "APPROVED",
    submittedAt: SENT_AT,
    commit: { oid: HEAD },
  });
  expect(latestReviews(approved.reviews)?.map((review) => [review.id, review.commit])).toEqual([
    ["", { oid: "" }],
  ]);
  const pending = send(NONE, api({ body: "draft" })) as Kept;
  expect([
    read(pending, "pr", { fields: ["latestReviews"] }),
    read(NONE, "pr", { fields: ["comments"], commented: true }),
  ]).toEqual([null, null]);
  expect(read(pending, "pr", { fields: ["reviews"] })).toMatchObject({
    stdout: { reviews: [{ state: "PENDING", submittedAt: null }] },
  });
  // An answer that is no pull request object is the route's own, unchanged.
  expect(read(NONE, "pr", {}, "text")).toEqual({ stdout: "text" });
});

// Catches: threads that lose a kept comment, carry REST's login, or end their page on a cursor
// of another comment.
test("the review threads are the kept inline comments, one unresolved thread each", () => {
  expect(threads([])).toEqual({
    pageInfo: { hasNextPage: false, endCursor: null },
    totalCount: 0,
    nodes: [],
  });
  const kept = send(
    NONE,
    api({}, { event: "COMMENT", body: "See.", comments: [{ path: "a.ts", line: 3, body: "here" }] })
  ) as Kept;
  expect(threads(kept.comments)).toEqual({
    pageInfo: { hasNextPage: false, endCursor: btoa("cursor:v2:7101") },
    totalCount: 1,
    nodes: [
      {
        id: "PRRT_7101",
        isResolved: false,
        comments: { totalCount: 1, nodes: [{ author: { login: "rig" }, body: "here" }] },
      },
    ],
  });
});
