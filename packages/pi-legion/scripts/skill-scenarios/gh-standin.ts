#!/usr/bin/env bun
// Stand-in for `gh` on a skill scenario's PATH. Each call is recorded to
// $SKILL_SCENARIO_RUN/calls.jsonl (argv, the contents of any file or stdin it hands GitHub a body
// through, its exit status, the pull request body it kept, if any, and what became of any
// settlement it published), then answered from the first route in $SKILL_SCENARIO_RUN/fixtures.json
// whose regex matches "gh <argv...>". A call no route matches is a request the rig does not
// model: it answers `rig: request not modeled: gh <argv...>` on stderr, exits 1, and its record
// carries `unmodeled: true`, so the run is the rig's error rather than the role's (score.ts). A
// 404 GitHub really answers is a route of its own. Nothing is sent anywhere but the settlement
// below. `--json a,b` picks fields, of an object or of each object of a list, and `--jq`/`-q`
// runs jq, as gh does. A pull request body edit (`gh pr
// edit --body`, `--body-file`, or a PATCH through `gh api`) is kept: every route answering with an
// object that has a `body` then answers with the new one, as GitHub's next read would, and the
// call's record carries it as `body`.
// A review the role sends is kept: `gh api -X POST .../pulls/<n>/reviews` with an `--input` JSON
// object or `-f`/`-F` fields, or `gh pr review` with `--approve`, `--request-changes` or `--comment`
// and `-b/--body` or `-F/--body-file`, through a route that carries `review`, the template of each
// review it keeps. GitHub records it so: the state its event names (APPROVE is APPROVED,
// REQUEST_CHANGES is CHANGES_REQUESTED, COMMENT is COMMENTED, and no event is PENDING), the body
// exactly as sent, and the template's commit (the head), user and pull request, under the next id
// counted from the template's. A route that carries `reviews` answers from what was kept: `list`
// every review in the order the role sent them, `one` the review whose id the match's named group
// `id` took, or GitHub's 404 for an id no review the role sent has, and `pr` the object its own
// answer is with `reviews` and `latestReviews` beside it, in the fields gh 2.98.0 prints for
// `gh pr view --json` (measured): the review's node id, author's login, association, body,
// submission time, state and commit, the latest review with no id and no commit, since gh asks
// none for it. A pending review is in the list: GitHub shows a pending review to its author alone,
// and the rig's reader is the login that sent it. Which review GitHub answers as the latest once a
// pending or comment-only review is kept is not measured, so a call that asks `latestReviews` then
// is refused as unmodeled. A `pr` route's `comments` names the route that posts a comment on the
// pull request: its `comments` field is none until that route has answered a call, and since the
// stand-in keeps no comment, a call that asks it after is refused as unmodeled. The inline
// `comments` a review carries are kept with it, each as GitHub records a review comment: its path,
// line and side, its body exactly as sent, the head as its commit, the reviewer, and its review's
// id, under the next id counted from the template's `comment_id`. `comments` reads every one in
// the order sent, and `review_comments` those of the review whose id the match's `id` took, or
// GitHub's 404 for an id no review the role sent has. A route's own reply that is unmodeled (a
// read that follows a write the stand-in does not keep) is refused before any of these reads.
// `threads` answers the GraphQL review threads of the pull request in the fields the reviewer's
// prompt asks (a thread's id, whether it is resolved, and its last comment's author and body):
// each kept inline comment opens a thread of its own, unresolved, in the order sent, on one page,
// whose end cursor is an opaque one of the rig's. A review whose answer the rig cannot state (an
// event GitHub refuses, REQUEST_CHANGES or COMMENT with no body, a second pending review, a commit
// other than the template's, flags gh refuses, an inline comment on a path or line the template's
// `added_lines` does not hold, or on the left side) is kept nowhere and refused as unmodeled.
// A route answers a sequence rather than the one answer, and stops matching once it has answered
// its share, so one head reads red before a re-run and green after it:
//   `replies` is a list of {stdout?, stderr?, exit?}, served in order: the first call matching the
//   route takes the first reply, the second the second, and a call past the end takes the last
//   again. A route with no `replies` answers its own {stdout, stderr, exit} to every call.
//   `follows` names another route by its `match` text, and the sequence is then reached by the
//   calls that route has answered rather than by this route's own: a read of the head that follows
//   the re-run's route answers its first reply however many times it is read, and its second from
//   the moment the re-run is pressed. The head's state changes when the role acts, never when it
//   reads, so a role that reads the head twice in one turn reads the same state twice. A `follows`
//   that names no route's `match` is refused, with the rest of a file Fixtures does not parse.
//   `spends` is how many matching calls the route answers. After that many it matches nothing, and
//   the call falls through to the next route whose pattern matches it, or to the 404.
// A route's `stderr` can name what the call asked for: `$<name>` in it is the text the route's named
// group `name` matched in "gh <argv...>", so one route refuses each commit the rig does not know by
// the name the call gave it, as GitHub's `No commit found for SHA: <ref>` does. A `$<name>` that
// names no group of the match, or a group the match did not reach, is printed as it stands.
// The stand-in is a fresh process per call, so the count of calls a route has answered outlives it
// in fixtures.json, as the route's `answered`, written back with the body keep's own
// read-modify-write on every call a route answers. Each call holds an exclusive flock(2) on
// <run>/fixtures.lock from its read of fixtures.json to that write, so no two `gh` processes of
// one turn answer from the same read: two calls of one route take a reply each and count twice,
// and a call beside the re-run never writes back the re-run's count as it stood before the
// re-run. Bun has no file lock, so flock(1) of util-linux takes the lock on this process's own
// descriptor of the file, which it shares as its stdin; the lock is the open file's, so it
// outlasts that child and ends when this process closes the descriptor or exits. The write itself
// is atomic: a sibling temporary file in the run directory, renamed over fixtures.json, so a
// reader that takes no lock never parses a half-written file.
// A fixtures.json that Fixtures does not parse — a route with no `match`, or a `follows` that names
// no route's `match` — is refused: every call exits 2 with the reason and records nothing. A route
// that followed nothing would stay at its first reply for the whole run, so a typo in a fixture
// would keep a head red and be scored as the role's failure. `--check` is that parse alone, which
// rig.sh's worker_world runs once a run's fixtures are written, so such a file stops the run, a
// rig error, before any agent boots.
// `publish` is an envelope a route publishes once it has answered: the stand-in POSTs it as it
// stands to $ENVOY_URL/v1/messages/publish, with the bearer the file $ENVOY_TOKEN_FILE names, so a
// role that subscribes to the pull request's checks topic and ends its turn is woken by the
// settlement the fixture wrote. The fixture writes every field of that envelope (`topic`, `source`,
// `message`, its `payload`) and the stand-in fills in none of it. A publish that fails leaves the
// call's exit status to the route, since a non-zero one would read to the agent as a failed `gh`
// call; what became of it is the call's record `published`. `--publish <file>` is that same POST
// of the envelope the file holds, made on its own, with no run and no call: rig.sh's red_report
// publishes the settlement of a check the rig reports with it, so the rig has one publisher. It
// prints what became of the envelope, as a call records it, and exits 0 when the listener
// answered 2xx and 1 when it did not.
//   gh-standin.ts gh <args...>
//   gh-standin.ts --check
//   gh-standin.ts --publish <envelope file>
import {
  appendFileSync,
  closeSync,
  openSync,
  readFileSync,
  renameSync,
  writeFileSync,
} from "node:fs";
import { z } from "zod";

/** One answer: what a route serves a call, and what each entry of a route's `replies` serves. A
 * reply marked `unmodeled` is a state of the world the rig cannot state truthfully, such as the
 * comments a role posted, which the stand-in does not keep: the call is refused as one no route
 * models, and recorded so. */
const Reply = z.object({
  stdout: z.unknown().optional(),
  stderr: z.string().optional(),
  exit: z.number().optional(),
  unmodeled: z.boolean().optional(),
});
/** The template of each review a route keeps: the first review's id (each next review takes the
 * next number) and the first inline comment's, the commit every review is on, the reviewer and its
 * association with the repository, the pull request's page (a review's own is it with
 * `#pullrequestreview-<id>`, a comment's with `#discussion_r<id>`) and its API URL, and the lines
 * of each file the diff adds whole, which are the lines an inline comment can be on. */
const ReviewTemplate = z.object({
  id: z.number(),
  comment_id: z.number(),
  commit_id: z.string(),
  user: z.looseObject({ login: z.string() }),
  author_association: z.string(),
  html_url: z.string(),
  pull_request_url: z.string(),
  added_lines: z.record(z.string(), z.number()),
});
/** A review as the stand-in keeps it: GitHub's REST object for it, its fields in GitHub's order. */
const KeptReview = z.looseObject({
  id: z.number(),
  node_id: z.string(),
  user: z.looseObject({ login: z.string() }),
  body: z.string(),
  state: z.string(),
  html_url: z.string(),
  pull_request_url: z.string(),
  author_association: z.string(),
  _links: z.unknown(),
  submitted_at: z.string().optional(),
  commit_id: z.string(),
});
type KeptReview = z.infer<typeof KeptReview>;
/** An inline comment of a kept review: GitHub's REST object for a review comment. */
const KeptComment = z.looseObject({
  id: z.number(),
  pull_request_review_id: z.number(),
  user: z.looseObject({ login: z.string() }),
  body: z.string(),
});
type KeptComment = z.infer<typeof KeptComment>;
/** rig.sh's worker_fixture writes each route as {match, stdout?, stderr?, exit?}, and a route that
 * answers a sequence, follows another route's calls, spends itself or publishes a settlement adds
 * `replies`, `follows`, `spends` and `publish`. A `follows` names the `match` of a route of the
 * same file, and so does a `counts_as`: each call the route answers counts as a call of that route
 * as well, so a press of the re-run through another command moves every read that follows the
 * re-run. A `program` is jq the stand-in runs over the answer's stdout, with the call's arguments
 * as `$argv`, before `--json` and `--jq`: a route answers a list filtered by the call's own flags
 * or query with it. A route that keeps the reviews the role sends carries `review`, and one that
 * reads them `reviews` (the header). `answered`, `reviews` and `review_comments` at the top of the
 * file are the stand-in's own: the count of calls the route has answered, and the reviews and the
 * inline comments kept, in the order the role sent them. A route is parsed loosely, as the file
 * is, so a key of the rig's own outlives the write back. */
const Fixtures = z
  .looseObject({
    routes: z.array(
      z.looseObject({
        ...Reply.shape,
        match: z.string(),
        replies: z.array(Reply).optional(),
        follows: z.string().optional(),
        counts_as: z.string().optional(),
        program: z.string().optional(),
        spends: z.number().optional(),
        answered: z.number().optional(),
        publish: z.unknown().optional(),
        review: ReviewTemplate.optional(),
        reviews: z.enum(["list", "one", "pr", "comments", "review_comments", "threads"]).optional(),
        comments: z.string().optional(),
      })
    ),
    reviews: z.array(KeptReview).optional(),
    review_comments: z.array(KeptComment).optional(),
  })
  .superRefine(({ routes }, ctx) => {
    const matches = new Set(routes.map((route) => route.match));
    routes.forEach((route, index) => {
      for (const [key, named] of [
        ["follows", route.follows],
        ["counts_as", route.counts_as],
        ["comments", route.comments],
      ] as const) {
        if (named !== undefined && !matches.has(named))
          ctx.addIssue({
            code: "custom",
            path: ["routes", index, key],
            message: `route "${route.match}" ${{ follows: "follows", counts_as: "counts as", comments: "reads the comments posted by" }[key]} "${named}", which is no route's match`,
          });
      }
    });
  });

const [as = "gh", ...args] = Bun.argv.slice(2);
// `--publish <file>` reads no run: it is the publish below, of the envelope the file holds.
if (as === "--publish") {
  const [file] = args;
  if (file === undefined) {
    console.error("usage: gh-standin.ts --publish <envelope file>");
    process.exit(2);
  }
  const result = await publish(JSON.parse(readFileSync(file, "utf8")));
  console.log(JSON.stringify(result));
  process.exit("status" in result && result.status >= 200 && result.status < 300 ? 0 : 1);
}
const run = process.env.SKILL_SCENARIO_RUN;
if (!run) {
  console.error("gh-standin: SKILL_SCENARIO_RUN is unset");
  process.exit(2);
}
/** fixtures.json as Fixtures parses it. A file it refuses ends this process with status 2 and the
 * reason, recording no call: the trouble is the rig's, never the agent's. */
function readFixtures() {
  const parsed = Fixtures.safeParse(JSON.parse(readFileSync(`${run}/fixtures.json`, "utf8")));
  if (!parsed.success) {
    console.error(`gh-standin: ${run}/fixtures.json is refused:\n${z.prettifyError(parsed.error)}`);
    process.exit(2);
  }
  return parsed.data;
}
/** Takes the run's fixtures lock, an exclusive flock(2) on <run>/fixtures.lock, and answers the
 * descriptor that holds it: closing it, or this process ending, releases the lock. */
function lockFixtures(): number {
  const lock = openSync(`${run}/fixtures.lock`, "a");
  const flock = Bun.spawnSync(["flock", "--exclusive", "0"], { stdin: lock, stderr: "pipe" });
  if (flock.exitCode !== 0) {
    console.error(`gh-standin: flock ${run}/fixtures.lock: ${flock.stderr.toString().trim()}`);
    process.exit(2);
  }
  return lock;
}
if (as === "--check") {
  readFixtures();
  process.exit(0);
}
const at = new Date().toISOString();

let stdin: string | undefined;
const readStdin = async () => {
  stdin ??= await new Response(Bun.stdin.stream()).text();
  return stdin;
};
const files: Record<string, string> = {};
const readSource = async (source: string) =>
  source === "-" ? readStdin() : readFileSync(source, "utf8");
// A pull request body edit: `gh pr edit` takes it from `-b/--body` or `-F/--body-file`, a
// `gh api -X PATCH repos/<o>/<r>/pulls/<n>` from a `body` field or an `--input` JSON object.
const prEdit = as === "gh" && args[0] === "pr" && args[1] === "edit";
const prReview = as === "gh" && args[0] === "pr" && args[1] === "review";
const prPatch =
  as === "gh" &&
  args[0] === "api" &&
  /(^|\s)(-X|--method)\s?PATCH(\s|$)/.test(args.join(" ")) &&
  args.some((arg) => /^\/?repos\/[^/]+\/[^/]+\/pulls\/\d+$/.test(arg));
let body: string | undefined;
/** The body this call left the pull request with, once the routes serve it. */
let kept: string | undefined;
/** What became of the settlement a route publishes, where it names one. */
type Published = { status: number; body: string } | { error: string };
let published: Published | undefined;
/** The `-f`/`-F` fields of a `gh api` call, each as gh sends it (a `-F key=@file` the file's text). */
const apiFields: Record<string, string> = {};
/** The body of a `gh pr review`, from `-b/--body` or `-F/--body-file`. */
let reviewBody: string | undefined;
for (let index = 0; index < args.length; index += 1) {
  const arg = args[index] ?? "";
  const next = args[index + 1];
  if (!next) continue;
  if (prEdit && (arg === "-b" || arg === "--body")) body = next;
  else if (prReview && (arg === "-b" || arg === "--body")) reviewBody = next;
  else if ((prEdit || prReview) && (arg === "-F" || arg === "--body-file")) {
    files[next] = await readSource(next);
    if (prEdit) body = files[next];
    else reviewBody = files[next];
  } else if (arg === "--input" || arg === "--body-file") files[next] = await readSource(next);
  else if (arg === "-F" || arg === "--field" || arg === "-f" || arg === "--raw-field") {
    const [key = "", ...rest] = next.split("=");
    let value = rest.join("=");
    if ((arg === "-F" || arg === "--field") && value.startsWith("@")) {
      files[value.slice(1)] = await readSource(value.slice(1));
      value = files[value.slice(1)] ?? "";
    }
    apiFields[key] = value;
    if (prPatch && key === "body") body = value;
  }
}
const inputIndex = args.indexOf("--input");
const input = inputIndex >= 0 ? args[inputIndex + 1] : undefined;
if (prPatch && input !== undefined) {
  const parsed: unknown = JSON.parse(files[input] ?? "null");
  if (
    typeof parsed === "object" &&
    parsed !== null &&
    "body" in parsed &&
    typeof parsed.body === "string"
  ) {
    body = parsed.body;
  }
}
// The call holds the run's fixtures lock from its read of fixtures.json to its write of what it
// changed there, below, and no longer: the answer it prints and the settlement it publishes read
// nothing of the file. The arguments, and any stdin they name, are read before the lock is taken,
// so a call whose stdin never closes holds no other call up.
const lock = lockFixtures();
const fixtures = readFixtures();
/** The routes this call can match: a route that has answered its `spends` calls is spent, and the
 * call falls through to the next route whose pattern matches it. */
const routes = fixtures.routes.filter(
  (route) => route.spends === undefined || (route.answered ?? 0) < route.spends
);
const joined = `${as} ${args.join(" ")}`;
const route = routes.find((candidate) => new RegExp(candidate.match, "s").test(joined));
/** Records the call with the status it exits with, then exits. A call no route models is recorded
 * `unmodeled`, which the scorer takes for a rig error. */
function exit(status: number, unmodeled = !route): never {
  appendFileSync(
    `${run}/calls.jsonl`,
    `${JSON.stringify({ at, as, argv: args, files, stdin, route: route?.match ?? null, ...(unmodeled ? { unmodeled: true } : {}), body: kept, published, exit: status })}\n`
  );
  process.exit(status);
}
/** `stdout` as GitHub's next read would answer it, once this call edited the pull request's body:
 * an answer that is an object with a `body` answers the new one. */
function withBody(stdout: unknown, edit: string): unknown {
  return typeof stdout === "object" && stdout !== null && "body" in stdout
    ? { ...stdout, body: edit }
    : stdout;
}
/** Publishes the envelope a route names, as the settlement GitHub's checks would raise: a POST of
 * what the fixture wrote, nothing of it filled in here, to $ENVOY_URL/v1/messages/publish with the
 * bearer the file $ENVOY_TOKEN_FILE names. A publish that fails is the stand-in's own trouble, not
 * the agent's: it is answered with what went wrong, for the call's record. The POST is bounded at
 * five seconds, as the rig's other probes are (rig.sh's pg_sql and nats_ready): a listener that
 * takes the request and never answers would otherwise hold the agent's own `gh` call until the
 * run's timeout, and the abort lands in the catch below as the call's `published.error`. */
async function publish(envelope: unknown): Promise<Published> {
  const url = process.env.ENVOY_URL;
  const tokenFile = process.env.ENVOY_TOKEN_FILE;
  if (!url || !tokenFile) return { error: "ENVOY_URL or ENVOY_TOKEN_FILE is unset" };
  try {
    const token = readFileSync(tokenFile, "utf8").trim();
    const answer = await fetch(`${url.replace(/\/+$/, "")}/v1/messages/publish`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
      body: JSON.stringify(envelope),
      signal: AbortSignal.timeout(5000),
    });
    return { status: answer.status, body: (await answer.text()).trim() };
  } catch (error) {
    return { error: error instanceof Error ? error.message : String(error) };
  }
}
// A call no route models is refused by name rather than answered with a GitHub error it might
// never have met: the rig has no answer for it, and a run that met one is the rig's to fix.
if (!route) {
  console.error(
    as === "gh"
      ? `rig: request not modeled: gh ${args.join(" ")}`
      : `unknown command: ${args.join(" ")}`
  );
  exit(1);
}
/** The text each named group of the route's match took in this call, which `$<name>` in the
 * route's stderr stands for. */
const groups = new RegExp(route.match, "s").exec(joined)?.groups ?? {};
/** The answer this call takes: the reply its route's sequence has reached — the first call takes
 * the first reply, a call past the end takes the last again — or the route's own answer. A route
 * that follows another reaches its sequence by that route's answered calls rather than its own, so
 * the state it answers turns on the call the role makes, not on the reads it makes. */
const replies = route.replies ?? [];
const followed =
  route.follows === undefined
    ? route
    : fixtures.routes.find((candidate) => candidate.match === route.follows);
/** The fields a `--json` call names, which gh prints and no other. */
const jsonIndex = args.findIndex((arg) => arg === "--json" || arg.startsWith("--json="));
const fields = (
  args[jsonIndex]?.startsWith("--json=") ? args[jsonIndex].slice(7) : (args[jsonIndex + 1] ?? "")
).split(",");
/** The reviews the role has sent, and their inline comments, in the order it sent them. */
const reviewsKept = fixtures.reviews ?? [];
const commentsKept = fixtures.review_comments ?? [];
/** A read of what the role sent (the header): the reviews, the review the match's `id` names, the
 * inline comments, those of the review the `id` names, or the review threads they open; GitHub's
 * 404 for a review id no review the role sent has. */
function keptRead(): z.infer<typeof Reply> | undefined {
  if (route?.reviews === "list") return { stdout: reviewsKept };
  if (route?.reviews === "comments") return { stdout: commentsKept };
  if (route?.reviews === "threads") {
    const last = commentsKept.at(-1);
    const threads = commentsKept.map((comment) => ({
      id: `PRRT_${comment.id}`,
      isResolved: false,
      comments: { nodes: [{ author: { login: comment.user.login }, body: comment.body }] },
    }));
    const endCursor = last === undefined ? null : btoa(`cursor:v2:${last.id}`);
    const pageInfo = { hasNextPage: false, endCursor };
    return {
      stdout: {
        data: { repository: { pullRequest: { reviewThreads: { pageInfo, nodes: threads } } } },
      },
    };
  }
  if (route?.reviews !== "one" && route?.reviews !== "review_comments") return undefined;
  const one = reviewsKept.find((review) => String(review.id) === groups.id);
  if (one === undefined) return { stderr: "gh: Not Found (HTTP 404)\n", exit: 1 };
  return route.reviews === "one"
    ? { stdout: one }
    : { stdout: commentsKept.filter((comment) => comment.pull_request_review_id === one.id) };
}
/** Refuses this call as one the rig does not model, and records it so. */
function refuse(): never {
  closeSync(lock);
  console.error(`rig: request not modeled: gh ${args.join(" ")}`);
  exit(1, true);
}
const sequenced = replies[Math.min(followed?.answered ?? 0, replies.length - 1)] ?? route;
if (sequenced.unmodeled) refuse();
const answer = keptRead() ?? sequenced;
/** The time GitHub stamps on what this call sends, to the second. */
const sentAt = at.replace(/\.\d+Z$/, "Z");
/** The inline comments a review sends, each as GitHub records a review comment, under the next
 * ids; `null` where one is on a path or line the template's `added_lines` does not hold, on the
 * left side, or has no body, which GitHub refuses or the rig cannot state. */
function inlineComments(
  template: z.infer<typeof ReviewTemplate>,
  reviewId: number,
  comments: unknown[]
): KeptComment[] | null {
  const kept: KeptComment[] = [];
  for (const [index, comment] of comments.entries()) {
    if (typeof comment !== "object" || comment === null || Array.isArray(comment)) return null;
    const {
      path: file,
      line,
      side = "RIGHT",
      start_line: start = null,
      start_side: startSide = "RIGHT",
      body: text,
    } = comment as Record<string, unknown>;
    const lines = typeof file === "string" ? template.added_lines[file] : undefined;
    if (lines === undefined || typeof text !== "string" || text === "") return null;
    if (side !== "RIGHT" || startSide !== "RIGHT") return null;
    if (typeof line !== "number" || !Number.isInteger(line) || line < 1 || line > lines)
      return null;
    if (
      start !== null &&
      (typeof start !== "number" || !Number.isInteger(start) || start < 1 || start >= line)
    )
      return null;
    const id = template.comment_id + commentsKept.length + index;
    const url = template.pull_request_url.replace(/\/\d+$/, `/comments/${id}`);
    const html_url = `${template.html_url}#discussion_r${id}`;
    kept.push({
      url,
      pull_request_review_id: reviewId,
      id,
      node_id: `PRRC_${id}`,
      path: file,
      commit_id: template.commit_id,
      original_commit_id: template.commit_id,
      user: template.user,
      body: text,
      created_at: sentAt,
      updated_at: sentAt,
      html_url,
      pull_request_url: template.pull_request_url,
      author_association: template.author_association,
      _links: {
        self: { href: url },
        html: { href: html_url },
        pull_request: { href: template.pull_request_url },
      },
      start_line: start,
      original_start_line: start,
      start_side: start === null ? null : "RIGHT",
      line,
      original_line: line,
      side: "RIGHT",
      subject_type: "line",
    });
  }
  return kept;
}
/** The review this call sends through a route that keeps one, as GitHub records it, with its
 * inline comments; `null` where the rig cannot state GitHub's answer to it (the header). A
 * `gh pr review` takes its event from its one flag and its body from `-b` or `-F`; a `gh api` call
 * takes them from its `--input` JSON object, else from its `-f`/`-F` fields. */
function sentReview(
  template: z.infer<typeof ReviewTemplate>
): { review: KeptReview; comments: KeptComment[] } | null {
  let sent: Record<string, unknown> = apiFields;
  if (prReview) {
    const events = [
      ["-a", "--approve", "APPROVE"],
      ["-r", "--request-changes", "REQUEST_CHANGES"],
      ["-c", "--comment", "COMMENT"],
    ].filter(([short = "", long = ""]) => args.includes(short) || args.includes(long));
    if (events.length !== 1) return null;
    sent = { event: events[0]?.[2], body: reviewBody };
  } else if (input !== undefined) {
    let parsed: unknown;
    try {
      parsed = JSON.parse(files[input] ?? "");
    } catch {
      return null;
    }
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return null;
    sent = { ...parsed };
  }
  const { event, body: text = "", commit_id: commit, comments = [] } = sent;
  const states: Record<string, string> = {
    APPROVE: "APPROVED",
    REQUEST_CHANGES: "CHANGES_REQUESTED",
    COMMENT: "COMMENTED",
  };
  const state = event === undefined || event === "" ? "PENDING" : states[String(event)];
  if (state === undefined || typeof text !== "string") return null;
  if ((state === "CHANGES_REQUESTED" || state === "COMMENTED") && text === "") return null;
  if (state === "PENDING" && reviewsKept.some((review) => review.state === "PENDING")) return null;
  if (commit !== undefined && commit !== template.commit_id) return null;
  // A pending review's comments are pending too, which the rig does not state.
  if (!Array.isArray(comments) || (state === "PENDING" && comments.length > 0)) return null;
  const id = template.id + reviewsKept.length;
  const inline = inlineComments(template, id, comments);
  if (inline === null) return null;
  const html_url = `${template.html_url}#pullrequestreview-${id}`;
  return {
    review: {
      id,
      node_id: `PRR_${id}`,
      user: template.user,
      body: text,
      state,
      html_url,
      pull_request_url: template.pull_request_url,
      author_association: template.author_association,
      _links: { html: { href: html_url }, pull_request: { href: template.pull_request_url } },
      ...(state === "PENDING" ? {} : { submitted_at: sentAt }),
      commit_id: template.commit_id,
    },
    comments: inline,
  };
}
const sent = route.review === undefined ? undefined : sentReview(route.review);
if (sent === null) refuse();
/** `latestReviews` as gh prints it: the latest review of each author, and the rig's reviewer is
 * the one author; unknown once a pending or comment-only review is kept (the header). */
const latest = reviewsKept.some(
  (review) => review.state === "PENDING" || review.state === "COMMENTED"
)
  ? undefined
  : reviewsKept.slice(-1).map((review) => ({ ...ghReview(review), id: "", commit: { oid: "" } }));
/** Whether the role has posted a comment on the pull request, which the stand-in does not keep. */
const commented =
  (fixtures.routes.find((candidate) => candidate.match === route.comments)?.answered ?? 0) > 0;
if (
  route.reviews === "pr" &&
  jsonIndex >= 0 &&
  ((fields.includes("latestReviews") && !latest) || (fields.includes("comments") && commented))
)
  refuse();
/** A kept review in the fields gh 2.98.0 prints for `gh pr view --json reviews` (measured). */
function ghReview(review: KeptReview) {
  return {
    id: review.node_id,
    author: { login: review.user.login },
    authorAssociation: review.author_association,
    body: review.body,
    submittedAt: review.submitted_at ?? null,
    includesCreatedEdit: false,
    reactionGroups: [],
    state: review.state,
    commit: { oid: review.commit_id },
  };
}
// The count of calls each route has answered outlives this process in fixtures.json, with the body
// where this call edited the pull request's and the review it sent with its inline comments: one
// read-modify-write of the file carries them all.
const edited = fixtures.routes.map((candidate) => ({
  ...candidate,
  answered:
    candidate === route || (route.counts_as !== undefined && candidate.match === route.counts_as)
      ? (candidate.answered ?? 0) + 1
      : candidate.answered,
  ...(body === undefined
    ? {}
    : {
        stdout: withBody(candidate.stdout, body),
        replies: candidate.replies?.map((reply) => ({
          ...reply,
          stdout: withBody(reply.stdout, body),
        })),
      }),
}));
// The rename is what makes the write atomic: the temporary file is a sibling, in the run
// directory, so the rename is within one filesystem.
const temporary = `${run}/fixtures.${process.pid}.json`;
writeFileSync(
  temporary,
  JSON.stringify({
    ...fixtures,
    routes: edited,
    ...(sent === undefined
      ? {}
      : {
          reviews: [...reviewsKept, sent.review],
          review_comments: [...commentsKept, ...sent.comments],
        }),
  })
);
renameSync(temporary, `${run}/fixtures.json`);
closeSync(lock);
if (body !== undefined) kept = body;
// A route that keeps the review and writes no answer of its own answers the review, as GitHub's
// POST does.
let output: unknown =
  sent !== undefined && answer.stdout === undefined ? sent.review : (answer.stdout ?? "");
if (
  route.reviews === "pr" &&
  jsonIndex >= 0 &&
  typeof output === "object" &&
  output !== null &&
  !Array.isArray(output)
)
  output = {
    ...output,
    reviews: reviewsKept.map(ghReview),
    latestReviews: latest ?? [],
    comments: [],
  };
if (route.program !== undefined) {
  const program = Bun.spawnSync(
    ["jq", "-c", "--argjson", "argv", JSON.stringify(args), route.program],
    { stdin: new TextEncoder().encode(JSON.stringify(output)) }
  );
  if (program.exitCode !== 0) {
    process.stderr.write(`gh-standin: the route's program failed: ${program.stderr.toString()}`);
    exit(2);
  }
  output = JSON.parse(program.stdout.toString());
}
const picked = (value: unknown) => {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return value;
  const record: Record<string, unknown> = { ...value };
  return Object.fromEntries(
    fields.filter((field) => field in record).map((field) => [field, record[field]])
  );
};
if (jsonIndex >= 0) output = Array.isArray(output) ? output.map(picked) : picked(output);
let text = typeof output === "string" ? output : JSON.stringify(output);
const jqIndex = args.findIndex((arg) => arg === "--jq" || arg === "-q");
const filter = jqIndex >= 0 ? args[jqIndex + 1] : undefined;
if (filter !== undefined) {
  const jq = Bun.spawnSync(["jq", "-r", filter], { stdin: new TextEncoder().encode(text) });
  text = jq.stdout.toString();
  if (jq.exitCode !== 0) {
    process.stderr.write(jq.stderr.toString());
    exit(1);
  }
}
if (text !== "") process.stdout.write(text.endsWith("\n") ? text : `${text}\n`);
if (answer.stderr)
  process.stderr.write(
    answer.stderr.replace(/\$<(\w+)>/g, (named, name: string) => groups[name] ?? named)
  );
if (route.publish !== undefined) published = await publish(route.publish);
exit(answer.exit ?? 0);
