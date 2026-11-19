#!/usr/bin/env bun
// Stand-in for `gh` on a skill scenario's PATH. Each call is recorded to
// $SKILL_SCENARIO_RUN/calls.jsonl (argv, the request the routes read, the contents of any file or
// stdin it hands GitHub a body through, its exit status, the pull request body it kept, if any, and
// what became of any settlement it published), then answered from the first route in
// $SKILL_SCENARIO_RUN/fixtures.json whose regex matches the call's request. Nothing is sent
// anywhere but the settlement below.
// The stand-in does what gh does on its own side, and the routes answer what GitHub answers:
//   The request a route reads is the call as gh resolves it, one argument after another with one
//   space between, and every whitespace inside an argument written as \u001f, so a route sees where
//   each argument ends and no text inside a body or a filter reads as a flag of its own: `gh`, the
//   command and its subcommand, the positional arguments, then each flag under its long name with
//   its value after it (`-R x` and `--repo=x` are both `--repo x`). A `gh api` call is
//   `gh api <METHOD> <endpoint> <flags...>`: the method gh sends (the one -X/--method names, else
//   POST where the call carries a field or --input and GET where it carries neither, as gh 2.98.0
//   does, measured), and the endpoint with no leading slash or API host and with `{owner}` and
//   `{repo}` put in from $GH_REPO, as gh puts them in. A `gh api graphql` call is
//   `gh api POST graphql <query>`, the query in the canonical form gh-graphql.ts writes, its
//   variables put in from the call's fields, and its fields gone from the flags.
//   A long flag `gh api` does not take is refused as gh 2.98.0 refuses it, `unknown flag: <flag>`
//   and its usage, exit 1, before any request (GH_API_USAGE, measured). Another flag the rig does
//   not model for that command (FLAGS below), a `{branch}` or a `{owner}` with no $GH_REPO, or a
//   query gh-graphql.ts cannot read, is a request the rig does not model.
//   `--json a,b` names fields of gh's own list for that command (JSON_FIELDS, gh 2.98.0's, measured):
//   a name not on it is refused as gh refuses it, `Unknown JSON field: "<f>"` with the list, exit
//   1, before any request; a name on it that the route's answer does not hold, of the object or of
//   any object of a list, is a request the rig does not model. The fields picked print in the order
//   gh prints them, by name. `--jq`/`-q` runs jq over what the call prints, as gh does;
//   `--paginate` on a REST read follows every page a route's program gives (below), `--slurp`
//   with it prints the pages in one list, and `--slurp` with a `--jq` is not modeled.
// A call no route matches, and a call its route matched but does not model, is a request the rig
// does not model: it answers `rig: request not modeled: gh <argv...>` on stderr, exits 1, and its
// record carries `unmodeled: true`, so the run is the rig's error rather than the role's
// (score.ts). An error GitHub really answers is a route of its own, and a route's `github` names
// it as measured: the body GitHub sent and gh's line for it, read from github-answers/ (below).
// `gh api` prints a body as GitHub sent it, with no newline after it (measured).
// A pull request body edit (`gh pr edit --body`, `--body-file`, or a PATCH through `gh api`) is
// kept: every route answering with an object that has a `body` then answers with the new one, as
// GitHub's next read would, and the call's record carries it as `body`.
// A review the role sends is kept, and every read of what it sent answers from what was kept, as
// review-keep.ts states them: a route that carries `review` keeps the review a call sends, under
// that template, or refuses one whose answer the rig cannot state as unmodeled, and a route that
// carries `reviews` reads them back (`list`, `one`, `comments`, `review_comments`, or `pr`, the
// route's own pull request with the reviews beside it). A `pr` route's `comments` names the route
// that posts a comment on the pull request. A route's own reply that is unmodeled (a read that
// follows a write the stand-in does not keep) is refused before any of these reads.
// A route that carries `graphql` answers a GraphQL query from the world its answer holds (its
// stdout's `graphql` tree and `$refs`, gh-graphql.ts): the query's own selection of it, or GitHub's
// error for a name it cannot resolve, the body on stdout and `gh: <message>` on stderr, exit 1.
// The review threads of the tree are `{"$keep": "threads", "until": <match>}`: the kept inline
// comments, as review-keep.ts's threads() gives them; once the route `until` names has answered a
// call (a line comment posted on its own, which opens a thread the stand-in does not keep) they
// are a state the rig cannot state.
// A route answers a sequence rather than the one answer, and stops matching once it has answered
// its share, so one head reads red before a re-run and green after it:
//   `replies` is a list of {stdout?, stderr?, exit?, unmodeled?}, served in order: the first call
//   matching the route takes the first reply, the second the second, and a call past the end takes
//   the last again. A reply marked `unmodeled` is a state the rig cannot state, refused as such.
//   A route with no `replies` answers its own {stdout, stderr, exit} to every call.
//   `follows` names another route by its `match` text, and the sequence is then reached by the
//   calls that route has answered rather than by this route's own: a read of the head that follows
//   the re-run's route answers its first reply however many times it is read, and its second from
//   the moment the re-run is pressed. The head's state changes when the role acts, never when it
//   reads, so a role that reads the head twice in one turn reads the same state twice. A `follows`
//   that names no route's `match` is refused, with the rest of a file Fixtures does not parse.
//   `settled` is the reply that takes the place of a route's last reply once the rig settles the
//   re-run, after the turn that pressed it has ended (rig.sh's rerun_settle): until then a read
//   that follows the press answers the re-run's attempt still running. A body edit reaches it as
//   it reaches every reply.
//   `spends` is how many matching calls the route answers. After that many it matches nothing, and
//   the call falls through to the next route whose pattern matches it, or to the refusal.
// A route's answer can name what the call asked for: `$<name>` in its stdout or its stderr is the
// text the route's named group `name` matched in the request, so one route answers the combined
// status of each full commit sha the rig does not know with the sha the call gave, as GitHub
// does; a measured answer names the commit by the group its route's `sha` names (below). A
// `$<name>` that names no group of the match, or a group the match did not reach, is printed as
// it stands.
// A route's `program` is jq the stand-in runs over its answer before `--json` and `--jq`, with the
// call's flags as gh read them as `$flags` (each by its long name: `-bmain` is `"--branch":
// "main"`), the query of its request as `$query` (each parameter by its name, decoded, a GET's
// fields among them), whether the call carried `--json` as `$json`, and the moment of the call as
// `$now` (seconds since the epoch): a route answers a list filtered by the call's own flags and
// query with it, or gh's own text for what it answers. A program reads every flag and parameter
// it is handed or fails, and a program that fails is the rig's trouble: the call is refused as
// unmodeled.
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
// reader that takes no lock never parses a half-written file. Every refusal is decided before
// that write, while the lock is held, and releases it.
// A fixtures.json that Fixtures does not parse — a route with no `match`, or a `follows` that names
// no route's `match` — is refused: every call exits 2 with the reason and records nothing. A route
// that followed nothing would stay at its first reply for the whole run, so a typo in a fixture
// would keep a head red and be scored as the role's failure. `--check` is that parse alone, which
// rig.sh's worker_world runs once a run's fixtures are written, so such a file stops the run, a
// rig error, before any agent boots.
// `publish` is an envelope a route publishes once it has answered: the stand-in POSTs it as it
// stands to the run's own listener, the last ENVOY_URL line of <run>/pane.env, with the bearer the
// file its last ENVOY_TOKEN_FILE line names (the values rig.sh gave the run's agent), so a role
// that subscribes to the pull request's checks topic and ends its turn is woken by the settlement
// the fixture wrote. The fixture writes every field of that envelope (`topic`, `source`,
// `message`, its `payload`) and the stand-in fills in none of it. A publish that fails leaves the
// call's exit status to the route, since a non-zero one would read to the agent as a failed `gh`
// call; what became of it is the call's record `published`. `--publish <file>` is that same POST
// of the envelope the file holds, made on its own, with no call: rig.sh's red_report publishes
// the settlement of a check the rig reports with it, so the rig has one publisher. It prints what
// became of the envelope, as a call records it, and exits 0 when the listener answered 2xx and 1
// when it did not.
// No DISPATCH_*, ENVOY_* or NATS_* variable of the stand-in's own environment reaches anything it
// starts, as rig.sh drops them at its start: every program, `--jq` filter and lock the stand-in
// runs gets its environment less those, and none of them decides where a settlement goes. A
// stand-in run from a Legion pane, whose ENVOY_URL names the live relay, would otherwise hand that
// relay to each and publish a fixture's settlement there.
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
import { canonical, type Document, parse, project, Unmodeled } from "./gh-graphql";
import {
  type Kept,
  KeptComment,
  KeptReview,
  keptRead,
  ReviewMode,
  ReviewTemplate,
  sentReview,
  threads,
} from "./review-keep";

/** One answer: what a route serves a call, and what each entry of a route's `replies` serves. A
 * reply marked `unmodeled` is a state of the world the rig cannot state truthfully, such as the
 * comments a role posted, which the stand-in does not keep: the call is refused as one no route
 * models, and recorded so. */
const Reply = z.object({
  stdout: z.unknown().optional(),
  stderr: z.string().optional(),
  exit: z.number().optional(),
  unmodeled: z.boolean().optional(),
  /** An answer GitHub gave, as measured: the name of its files under github-answers/. */
  github: z.string().optional(),
  /** The match group whose text stands for the commit the measured answer names. */
  sha: z.string().optional(),
});
/** rig.sh's worker_fixture writes each route as {match, stdout?, stderr?, exit?}, and a route that
 * answers a sequence, follows another route's calls, spends itself or publishes a settlement adds
 * `replies`, `follows`, `spends` and `publish`. A `follows` names the `match` of a route of the
 * same file, and so does a `counts_as`: each call the route answers counts as a call of that route
 * as well, so a press of the re-run through another command moves every read that follows the
 * re-run. A `program` is jq the stand-in runs over the answer (the header). A route that keeps the
 * reviews the role sends carries `review`, one that reads them `reviews`, and one that answers
 * GraphQL `graphql` (the header). `answered`, `reviews` and `review_comments` at the top of the
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
        settled: Reply.optional(),
        answered: z.number().optional(),
        publish: z.unknown().optional(),
        review: ReviewTemplate.optional(),
        reviews: ReviewMode.optional(),
        comments: z.string().optional(),
        graphql: z.boolean().optional(),
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

/** The flags the rig models for each command, by every name gh takes them under, each with the
 * long name the request writes it as and whether it takes a value. A flag of none of these lists
 * is a request the rig does not model: the stand-in implements `--json`, `--jq`, `--paginate` and
 * `--slurp` itself, the routes the rest. */
type Flag = { long: string; value: boolean };
const flag = (long: string, value: boolean, short?: string): [string, Flag][] => [
  [long, { long, value }],
  ...(short === undefined ? [] : ([[short, { long, value }]] as [string, Flag][])),
];
const REPO = flag("--repo", true, "-R");
const JSON_OUT = [...flag("--json", true), ...flag("--jq", true, "-q")];
const BODY = [...flag("--body", true, "-b"), ...flag("--body-file", true, "-F")];
const FLAGS: Record<string, Record<string, Flag>> = {
  api: Object.fromEntries([
    ...flag("--method", true, "-X"),
    ...flag("--header", true, "-H"),
    ...flag("--raw-field", true, "-f"),
    ...flag("--field", true, "-F"),
    ...flag("--input", true),
    ...flag("--jq", true, "-q"),
    ...flag("--paginate", false),
    ...flag("--slurp", false),
  ]),
  "pr view": Object.fromEntries([...REPO, ...JSON_OUT]),
  "pr checks": Object.fromEntries([...REPO, ...JSON_OUT, ...flag("--required", false)]),
  "pr diff": Object.fromEntries([...REPO, ...flag("--name-only", false)]),
  "pr list": Object.fromEntries([
    ...REPO,
    ...JSON_OUT,
    ...flag("--head", true, "-H"),
    ...flag("--state", true, "-s"),
    ...flag("--base", true, "-B"),
    ...flag("--limit", true, "-L"),
  ]),
  "pr comment": Object.fromEntries([...REPO, ...BODY]),
  "pr edit": Object.fromEntries([...REPO, ...BODY]),
  "pr ready": Object.fromEntries([...REPO]),
  "pr review": Object.fromEntries([
    ...REPO,
    ...BODY,
    ...flag("--approve", false, "-a"),
    ...flag("--request-changes", false, "-r"),
    ...flag("--comment", false, "-c"),
  ]),
  "run view": Object.fromEntries([
    ...REPO,
    ...JSON_OUT,
    ...flag("--job", true, "-j"),
    ...flag("--attempt", true, "-a"),
    ...flag("--log", false),
    ...flag("--log-failed", false),
    ...flag("--verbose", false, "-v"),
  ]),
  "run list": Object.fromEntries([
    ...REPO,
    ...JSON_OUT,
    ...flag("--status", true, "-s"),
    ...flag("--workflow", true, "-w"),
    ...flag("--commit", true, "-c"),
    ...flag("--branch", true, "-b"),
    ...flag("--event", true, "-e"),
    ...flag("--limit", true, "-L"),
  ]),
  "run rerun": Object.fromEntries([
    ...REPO,
    ...flag("--job", true, "-j"),
    ...flag("--failed", false),
    ...flag("--debug", false, "-d"),
    ...flag("--help", false, "-h"),
  ]),
  "repo view": Object.fromEntries([...JSON_OUT]),
  "auth status": {},
  "--version": {},
  version: {},
};
/** The commit the measured answers that name one were measured for (github-answers/README.txt). */
const PROBE_SHA = "1111111111111111111111111111111111111111";
/** The long flags gh 2.98.0's `gh api` takes, and the usage it prints after `unknown flag: <flag>`
 * for any other, exit 1, before any request (measured with `--json`, which round 6's
 * trial-c-head-4 sent to `gh api`). */
const GH_API_FLAGS = [
  "--allow-escape-sequences",
  "--cache",
  "--field",
  "--header",
  "--hostname",
  "--include",
  "--input",
  "--jq",
  "--method",
  "--paginate",
  "--preview",
  "--raw-field",
  "--silent",
  "--slurp",
  "--template",
  "--verbose",
];
const GH_API_USAGE = [
  "Usage:  gh api <endpoint> [flags]",
  "",
  "Flags:",
  "      --allow-escape-sequences   Allow printing terminal escape sequences",
  '      --cache duration           Cache the response, e.g. "3600s", "60m", "1h"',
  '  -F, --field key=value          Add a typed parameter in key=value format (use "@<path>" or "@-" to read value from file or stdin)',
  "  -H, --header key:value         Add a HTTP request header in key:value format",
  '      --hostname string          The GitHub hostname for the request (default "github.com")',
  "  -i, --include                  Include HTTP response status line and headers in the output",
  '      --input file               The file to use as body for the HTTP request (use "-" to read from standard input)',
  "  -q, --jq string                Query to select values from the response using jq syntax",
  '  -X, --method string            The HTTP method for the request (default "GET")',
  "      --paginate                 Make additional HTTP requests to fetch all pages of results",
  "  -p, --preview strings          Opt into GitHub API previews (names should omit '-preview')",
  "  -f, --raw-field key=value      Add a string parameter in key=value format",
  "      --silent                   Do not print the response body",
  '      --slurp                    Use with "--paginate" to return an array of all pages of either JSON arrays or objects',
  '  -t, --template string          Format JSON output using a Go template; see "gh help formatting"',
  "      --verbose                  Include full HTTP request and response in the output",
  "  ",
  "",
].join("\n");
/** gh 2.98.0's own list of the fields `--json` takes, per command, measured with `--json bogus`. */
const PR_FIELDS = [
  "additions",
  "assignees",
  "author",
  "autoMergeRequest",
  "baseRefName",
  "baseRefOid",
  "body",
  "changedFiles",
  "closed",
  "closedAt",
  "closingIssuesReferences",
  "comments",
  "commits",
  "createdAt",
  "deletions",
  "files",
  "fullDatabaseId",
  "headRefName",
  "headRefOid",
  "headRepository",
  "headRepositoryOwner",
  "id",
  "isCrossRepository",
  "isDraft",
  "labels",
  "latestReviews",
  "maintainerCanModify",
  "mergeCommit",
  "mergeStateStatus",
  "mergeable",
  "mergedAt",
  "mergedBy",
  "milestone",
  "number",
  "potentialMergeCommit",
  "projectCards",
  "projectItems",
  "reactionGroups",
  "reviewDecision",
  "reviewRequests",
  "reviews",
  "state",
  "statusCheckRollup",
  "title",
  "updatedAt",
  "url",
];
const RUN_FIELDS = [
  "attempt",
  "conclusion",
  "createdAt",
  "databaseId",
  "displayTitle",
  "event",
  "headBranch",
  "headSha",
  "jobs",
  "name",
  "number",
  "startedAt",
  "status",
  "updatedAt",
  "url",
  "workflowDatabaseId",
  "workflowName",
];
/** gh 2.98.0's fields of `gh pr checks --json`, measured with `--json bogus` on
 * cybermonklabs/legion#3514 (2026-11-19, prchecks-json-bogus, requests.log line 37;
 * github-answers/README.txt). */
const PR_CHECK_FIELDS = [
  "bucket",
  "completedAt",
  "description",
  "event",
  "link",
  "name",
  "startedAt",
  "state",
  "workflow",
];
const JSON_FIELDS: Record<string, string[]> = {
  "pr view": PR_FIELDS,
  "pr list": PR_FIELDS,
  "pr checks": PR_CHECK_FIELDS,
  "run view": RUN_FIELDS,
  "run list": RUN_FIELDS.filter((field) => field !== "jobs"),
  "repo view": [
    "archivedAt",
    "assignableUsers",
    "codeOfConduct",
    "contactLinks",
    "createdAt",
    "defaultBranchRef",
    "deleteBranchOnMerge",
    "description",
    "diskUsage",
    "forkCount",
    "fundingLinks",
    "hasDiscussionsEnabled",
    "hasIssuesEnabled",
    "hasProjectsEnabled",
    "hasWikiEnabled",
    "homepageUrl",
    "id",
    "isArchived",
    "isBlankIssuesEnabled",
    "isEmpty",
    "isFork",
    "isInOrganization",
    "isMirror",
    "isPrivate",
    "isSecurityPolicyEnabled",
    "isTemplate",
    "isUserConfigurationRepository",
    "issueTemplates",
    "issues",
    "labels",
    "languages",
    "latestRelease",
    "licenseInfo",
    "mentionableUsers",
    "mergeCommitAllowed",
    "milestones",
    "mirrorUrl",
    "name",
    "nameWithOwner",
    "openGraphImageUrl",
    "owner",
    "parent",
    "primaryLanguage",
    "projects",
    "projectsV2",
    "pullRequestTemplates",
    "pullRequests",
    "pushedAt",
    "rebaseMergeAllowed",
    "repositoryTopics",
    "securityPolicyUrl",
    "squashMergeAllowed",
    "sshUrl",
    "stargazerCount",
    "templateRepository",
    "updatedAt",
    "url",
    "usesCustomOpenGraphImage",
    "viewerCanAdminister",
    "viewerDefaultCommitEmail",
    "viewerDefaultMergeMethod",
    "viewerHasStarred",
    "viewerPermission",
    "viewerPossibleCommitEmails",
    "viewerSubscription",
    "visibility",
    "watchers",
  ],
};

const [as = "gh", ...args] = Bun.argv.slice(2);
// The environment every child of the stand-in runs with: its own less every DISPATCH_*, ENVOY_*
// and NATS_* variable (the header). Bun hands a child the environment this process started with
// unless a spawn names one, so each spawn below names this one; publish reads the run's own
// listener from its pane.env.
const childEnv = Object.fromEntries(
  Object.entries(process.env).filter(([name]) => !/^(?:DISPATCH|ENVOY|NATS)_/.test(name))
);
const run = process.env.SKILL_SCENARIO_RUN;
if (!run) {
  console.error("gh-standin: SKILL_SCENARIO_RUN is unset");
  process.exit(2);
}
// `--publish <file>` answers no call: it is the publish below, of the envelope the file holds.
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
  const flock = Bun.spawnSync(["flock", "--exclusive", "0"], {
    env: childEnv,
    stdin: lock,
    stderr: "pipe",
  });
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
/** The request the routes read (the header), once the call is resolved as gh resolves it; while
 * it is undefined, the call is one the stand-in refuses before any route. */
let request: string | undefined;
let lock: number | undefined;
let route: z.infer<typeof Fixtures>["routes"][number] | undefined;
/** The body this call left the pull request with, once the routes serve it. */
let kept: string | undefined;
/** What became of the settlement a route publishes, where it names one. */
type Published = { status: number; body: string } | { error: string };
let published: Published | undefined;
/** Records the call with the status it exits with, then exits; `unmodeled` marks a request the
 * rig does not model, which the scorer takes for a rig error. */
function exit(status: number, unmodeled: boolean): never {
  appendFileSync(
    `${run}/calls.jsonl`,
    `${JSON.stringify({ at, as, argv: args, request, files, stdin, route: route?.match ?? null, ...(unmodeled ? { unmodeled: true } : {}), body: kept, published, exit: status })}\n`
  );
  process.exit(status);
}
/** Refuses this call as a request the rig does not model, releasing the fixtures lock where it is
 * held, and records it so. */
function refuse(): never {
  if (lock !== undefined) closeSync(lock);
  console.error(
    as === "gh"
      ? `rig: request not modeled: gh ${args.join(" ")}`
      : `unknown command: ${args.join(" ")}`
  );
  exit(1, true);
}
if (as !== "gh") refuse();

// The call as gh reads it: its command, its positional arguments and its flags.
const command =
  args[0] === "api" || args[0]?.startsWith("-") ? (args[0] ?? "") : args.slice(0, 2).join(" ");
const table = FLAGS[command];
if (table === undefined) refuse();
const positional: string[] = [];
const given: [Flag, string | undefined][] = [];
for (let index = command.split(" ").length; index < args.length; index += 1) {
  const arg = args[index] as string;
  if (!arg.startsWith("-") || arg === "-") {
    positional.push(arg);
    continue;
  }
  const equals = arg.startsWith("--") ? arg.indexOf("=") : -1;
  const name = equals >= 0 ? arg.slice(0, equals) : arg.startsWith("--") ? arg : arg.slice(0, 2);
  // gh refuses a long flag `gh api` does not have before it makes any request.
  if (command === "api" && arg.startsWith("--") && !GH_API_FLAGS.includes(name)) {
    process.stderr.write(`unknown flag: ${name}\n\n${GH_API_USAGE}`);
    exit(1, false);
  }
  const known = table[name];
  if (known === undefined) refuse();
  if (!known.value) {
    if (equals >= 0 || (!arg.startsWith("--") && arg.length > 2)) refuse();
    given.push([known, undefined]);
    continue;
  }
  const attached =
    equals >= 0
      ? arg.slice(equals + 1)
      : !arg.startsWith("--") && arg.length > 2
        ? arg.slice(2)
        : undefined;
  if (attached !== undefined) given.push([known, attached]);
  else {
    index += 1;
    if (index >= args.length) refuse();
    given.push([known, args[index]]);
  }
}
const flagValue = (long: string) => given.find(([f]) => f.long === long)?.[1];
const has = (long: string) => given.some(([f]) => f.long === long);
/** The fields a `--json` call names, which gh prints and no other; undefined with no `--json`. */
const fields = has("--json") ? (flagValue("--json") ?? "").split(",") : undefined;
const filter = flagValue("--jq");
// gh refuses a field not on its list before it makes any request.
const listed = JSON_FIELDS[command];
if (fields !== undefined) {
  if (listed === undefined) refuse();
  const unknown = fields.find((field) => !listed.includes(field));
  if (unknown !== undefined) {
    process.stderr.write(
      `Unknown JSON field: ${JSON.stringify(unknown)}\nAvailable fields:\n${listed.map((field) => `  ${field}\n`).join("")}`
    );
    exit(1, false);
  }
}

// What the call hands GitHub: a body, a review, fields and an --input.
const prEdit = command === "pr edit";
const prReview = command === "pr review";
let body: string | undefined;
/** The `-f`/`-F` fields of a `gh api` call, each as gh sends it: a `-F` value typed as gh types it
 * (true, false, null and an integer as JSON, `@file` the file's text) and a `-f` value a string. */
const apiFields: Record<string, unknown> = {};
/** The body of a `gh pr review`, from `-b/--body` or `-F/--body-file`. */
let reviewBody: string | undefined;
for (const [{ long }, value] of given) {
  if (value === undefined) continue;
  if (prEdit && long === "--body") body = value;
  else if (prReview && long === "--body") reviewBody = value;
  else if ((prEdit || prReview) && long === "--body-file") {
    files[value] = await readSource(value);
    if (prEdit) body = files[value];
    else reviewBody = files[value];
  } else if (long === "--input") files[value] = await readSource(value);
  else if (long === "--raw-field" || long === "--field") {
    const [key = "", ...rest] = value.split("=");
    const raw = rest.join("=");
    if (long === "--field" && raw.startsWith("@")) {
      files[raw.slice(1)] = await readSource(raw.slice(1));
      apiFields[key] = files[raw.slice(1)] ?? "";
    } else if (long === "--field" && /^(?:true|false|null|-?\d+)$/.test(raw))
      apiFields[key] = JSON.parse(raw);
    else apiFields[key] = raw;
  }
}
const input = flagValue("--input");
/** The request text of an argument: its whitespace written as \u001f (the header). */
const spaced = (arg: string) => arg.replace(/\s/g, "\u001f");
/** A field escaped as Go's url.QueryEscape escapes it, which is how gh writes a GET's fields. */
const queryEscape = (text: string) =>
  encodeURIComponent(text)
    .replace(/[!'()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`)
    .replace(/%20/g, "+");
const flagText = (skip: (long: string) => boolean) =>
  given
    .filter(([f]) => !skip(f.long))
    .flatMap(([f, value]) => (value === undefined ? [f.long] : [f.long, spaced(value)]));
let method: string | undefined;
let endpoint: string | undefined;
let document: Document | undefined;
if (command === "api") {
  const [path, ...more] = positional;
  if (path === undefined || more.length > 0) refuse();
  method = (
    flagValue("--method") ??
    (Object.keys(apiFields).length > 0 || input !== undefined ? "POST" : "GET")
  ).toUpperCase();
  let resolved = path.replace(/^https:\/\/api\.github\.com\//, "").replace(/^\//, "");
  if (/\{(?:owner|repo|branch)\}/.test(resolved)) {
    const repo = process.env.GH_REPO?.split("/").slice(-2);
    if (repo === undefined || repo.length !== 2 || resolved.includes("{branch}")) refuse();
    resolved = resolved
      .replaceAll("{owner}", repo[0] as string)
      .replaceAll("{repo}", repo[1] as string);
  }
  endpoint = resolved;
  if (endpoint === "graphql") {
    const { query, ...variables } = apiFields;
    if (typeof query !== "string" || method !== "POST" || input !== undefined) refuse();
    try {
      document = parse(query, variables);
    } catch (error) {
      if (error instanceof Unmodeled) refuse();
      throw error;
    }
    request = [
      "gh",
      "api",
      method,
      "graphql",
      spaced(canonical(document)),
      ...flagText((long) => long === "--method" || long === "--raw-field" || long === "--field"),
    ].join(" ");
  } else {
    // A GET sends its fields as the query, as gh 2.98.0 does (measured in the review of
    // 76cc2379: `-X GET …/actions/runs -f head_sha=… -f event=pull_request` sends
    // `GET …/actions/runs?event=pull_request&head_sha=…`): gh adds them to the query the
    // endpoint names and writes the whole query as Go's url.Values encodes it, every parameter by
    // name, each escaped as url.QueryEscape escapes it. A GET that also hands over a body
    // (--input) is not modeled. A REST read gh pages (--paginate) asks for 100 a page where it
    // names no per_page (measured, 2026-11-19, GH_DEBUG=api: `--paginate …/pulls/4148/comments`
    // sends `…/pulls/4148/comments?per_page=100`).
    const queried = method === "GET" && Object.keys(apiFields).length > 0;
    const paging = method === "GET" && has("--paginate");
    const [bare = "", named = ""] = endpoint.split(/\?(.*)/s);
    const pairs: [string, string][] = named
      .split("&")
      .filter((pair) => pair !== "")
      .map((pair): [string, string] => {
        const [name = "", ...value] = pair.split("=");
        try {
          return [
            decodeURIComponent(name.replace(/\+/g, " ")),
            decodeURIComponent(value.join("=").replace(/\+/g, " ")),
          ];
        } catch {
          return refuse();
        }
      });
    if (queried) {
      if (input !== undefined) refuse();
      pairs.push(
        ...Object.keys(apiFields).map((key): [string, string] => [key, String(apiFields[key])])
      );
    }
    const perPage = paging && !pairs.some(([name]) => name === "per_page");
    if (queried)
      endpoint = `${bare}?${[
        ...pairs,
        ...(perPage ? [["per_page", "100"] as [string, string]] : []),
      ]
        .map((pair, index) => ({ pair, index }))
        .sort((a, b) =>
          a.pair[0] < b.pair[0] ? -1 : a.pair[0] > b.pair[0] ? 1 : a.index - b.index
        )
        .map(({ pair: [key, value] }) => `${queryEscape(key)}=${queryEscape(value)}`)
        .join("&")}`;
    else if (perPage) endpoint += `${endpoint.includes("?") ? "&" : "?"}per_page=100`;
    request = [
      "gh",
      "api",
      method,
      spaced(endpoint),
      ...flagText(
        (long) => long === "--method" || (queried && (long === "--raw-field" || long === "--field"))
      ),
    ].join(" ");
  }
} else
  request = ["gh", ...command.split(" "), ...positional.map(spaced), ...flagText(() => false)].join(
    " "
  );
/** The query of the call's request, each parameter by its name, decoded as GitHub reads it (a `+`
 * a space, `%XX` its byte); a name given twice keeps its last value. A route program reads it. */
const query: Record<string, string> = {};
for (const pair of endpoint?.includes("?")
  ? endpoint.slice(endpoint.indexOf("?") + 1).split("&")
  : []) {
  if (pair === "") continue;
  const [name = "", ...value] = pair.split("=");
  try {
    query[decodeURIComponent(name.replace(/\+/g, " "))] = decodeURIComponent(
      value.join("=").replace(/\+/g, " ")
    );
  } catch {
    refuse();
  }
}
/** The call's flags as gh read them, each by its long name: its value, or true for a flag that
 * takes none (`-bmain` is `--branch: "main"`). A route program reads them, never the raw argv. */
const flags: Record<string, string | true> = Object.fromEntries(
  given.map(([f, value]) => [f.long, value ?? true])
);
if (has("--slurp") && (!has("--paginate") || filter !== undefined)) refuse();

// The call holds the run's fixtures lock from its read of fixtures.json to its write of what it
// changed there, below, and no longer: the answer it prints and the settlement it publishes read
// nothing of the file. The arguments, and any stdin they name, are read before the lock is taken,
// so a call whose stdin never closes holds no other call up.
lock = lockFixtures();
const fixtures = readFixtures();
/** The routes this call can match: a route that has answered its `spends` calls is spent, and the
 * call falls through to the next route whose pattern matches it. */
const routes = fixtures.routes.filter(
  (candidate) => candidate.spends === undefined || (candidate.answered ?? 0) < candidate.spends
);
const asked = request;
route = routes.find((candidate) => new RegExp(candidate.match, "s").test(asked));
// A call no route models is refused by name rather than answered with a GitHub error it might
// never have met: the rig has no answer for it, and a run that met one is the rig's to fix.
if (route === undefined) refuse();
const matched = route;
/** `stdout` as GitHub's next read would answer it, once this call edited the pull request's body:
 * an answer that is an object with a `body` answers the new one. */
function withBody(stdout: unknown, edit: string): unknown {
  return typeof stdout === "object" && stdout !== null && "body" in stdout
    ? { ...stdout, body: edit }
    : stdout;
}
/** The value rig.sh gave the run's agent for `name`: the last line naming it in <run>/pane.env,
 * the last being the one `env -i` takes (launch), or undefined where the file names none. */
function rigValue(name: string): string | undefined {
  let pane: string;
  try {
    pane = readFileSync(`${run}/pane.env`, "utf8");
  } catch {
    return undefined;
  }
  const line = pane.split("\n").findLast((candidate) => candidate.startsWith(`${name}=`));
  return line?.slice(name.length + 1);
}
/** Publishes the envelope a route names, as the settlement GitHub's checks would raise: a POST of
 * what the fixture wrote, nothing of it filled in here, to the run's own listener
 * (<ENVOY_URL>/v1/messages/publish, with the bearer the file ENVOY_TOKEN_FILE names, both as
 * pane.env holds them). A publish that fails is the stand-in's own trouble, not the agent's: it
 * is answered with what went wrong, for the call's record. The POST is bounded at five seconds,
 * as the rig's other probes are (rig.sh's pg_sql and nats_ready): a listener that takes the
 * request and never answers would otherwise hold the agent's own `gh` call until the run's
 * timeout, and the abort lands in the catch below as the call's `published.error`. */
async function publish(envelope: unknown): Promise<Published> {
  const url = rigValue("ENVOY_URL");
  const tokenFile = rigValue("ENVOY_TOKEN_FILE");
  if (!url || !tokenFile)
    return { error: `${run}/pane.env names no ENVOY_URL or ENVOY_TOKEN_FILE` };
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
/** The text each named group of the route's match took in this call, which `$<name>` in the
 * route's stderr stands for. */
const groups = new RegExp(matched.match, "s").exec(asked)?.groups ?? {};
/** The answer this call takes: the reply its route's sequence has reached — the first call takes
 * the first reply, a call past the end takes the last again — or the route's own answer. A route
 * that follows another reaches its sequence by that route's answered calls rather than its own, so
 * the state it answers turns on the call the role makes, not on the reads it makes. */
const replies = matched.replies ?? [];
const followed =
  matched.follows === undefined
    ? matched
    : fixtures.routes.find((candidate) => candidate.match === matched.follows);
const answeredBy = (match: string | undefined) =>
  fixtures.routes.find((candidate) => candidate.match === match)?.answered ?? 0;
/** The reviews the role has sent, and their inline comments, in the order it sent them. */
const keptReviews: Kept = {
  reviews: fixtures.reviews ?? [],
  comments: fixtures.review_comments ?? [],
};
const sequenced = replies[Math.min(followed?.answered ?? 0, replies.length - 1)] ?? matched;
if (sequenced.unmodeled) refuse();
/** Whether the role has posted a comment on the pull request, which the stand-in does not keep. */
const commented = matched.comments !== undefined && answeredBy(matched.comments) > 0;
const read = keptRead(
  matched.reviews,
  keptReviews,
  { id: groups.id, fields, commented },
  sequenced
);
if (read === null) refuse();
const answer: z.infer<typeof Reply> = read ?? sequenced;
/** The time GitHub stamps on what this call sends, to the second. */
const sentAt = at.replace(/\.\d+Z$/, "Z");
/** The review this call sends through a route that keeps one (review-keep.ts). */
const sent =
  matched.review === undefined
    ? undefined
    : sentReview(
        matched.review,
        {
          prReview,
          events: ["--approve", "--request-changes", "--comment"].filter(has),
          body: reviewBody,
          input: input === undefined ? undefined : (files[input] ?? ""),
          fields: apiFields,
        },
        keptReviews,
        sentAt
      );
if (sent === null) refuse();

// What the call prints, decided before the write below, so every refusal holds the lock.
// A route that keeps the review and writes no answer of its own answers the review, as GitHub's
// POST does.
let output: unknown =
  sent !== undefined && answer.stdout === undefined ? sent.review : (answer.stdout ?? "");
// `$<name>` in an answer's stdout or stderr stands for the text the match's group took: the commit
// a call named, for the combined status of a commit GitHub does not hold, say.
const named = (text: string, inJson: boolean) =>
  text.replace(/\$<(\w+)>/g, (whole, name: string) => {
    const took = groups[name];
    if (took === undefined) return whole;
    return inJson ? JSON.stringify(took).slice(1, -1) : took;
  });
if (typeof output === "string") output = named(output, false);
else if (typeof output === "object" && output !== null)
  output = JSON.parse(named(JSON.stringify(output), true));
let stderr = answer.stderr === undefined ? undefined : named(answer.stderr, false);
let status = answer.exit ?? 0;
// An error GitHub answers, as gh 2.98.0 printed it: the body GitHub sent on stdout, byte for byte,
// and gh's `gh: <message> (HTTP <status>)` on stderr, exit 1. Each answer is a pair of files under
// github-answers/, <name>.stdout and <name>.stderr, measured on 2026-11-18 with gh 2.98.0 against
// api.github.com on cybermonklabs/legion; github-answers/README.txt names the request each one
// answered. The answers that name a commit were measured for 1111…1111, which stands for the
// commit the route's `sha` group took.
if (answer.github !== undefined) {
  const measured = (part: "stdout" | "stderr") => {
    const text = readFileSync(`${import.meta.dir}/github-answers/${answer.github}.${part}`, "utf8");
    return answer.sha === undefined ? text : text.replaceAll(PROBE_SHA, groups[answer.sha] ?? "");
  };
  output = measured("stdout");
  stderr = measured("stderr");
  status = 1;
}
if (matched.graphql === true) {
  const world = output as { graphql?: unknown; $refs?: Record<string, unknown> };
  if (
    document === undefined ||
    typeof world !== "object" ||
    world === null ||
    world.graphql === undefined
  )
    refuse();
  const graphqlDocument = document;
  // A request GitHub refuses before it reads anything (gh-graphql.ts) answers its errors alone,
  // whatever the world holds (measured, github-answers/graphql-*).
  const refused = graphqlDocument.errors.length > 0;
  let projected: { data?: unknown; errors: unknown[] } = { errors: graphqlDocument.errors };
  if (!refused)
    try {
      projected = project(graphqlDocument, world.graphql, world.$refs ?? {}, (marker) => {
        if (marker.$keep !== "threads")
          throw new Unmodeled(`no keep named ${String(marker.$keep)}`);
        if (answeredBy(String(marker.until)) > 0)
          throw new Unmodeled("a line comment the stand-in does not keep");
        return threads(keptReviews.comments);
      });
    } catch (error) {
      if (error instanceof Unmodeled) refuse();
      throw error;
    }
  output = refused
    ? { errors: projected.errors }
    : projected.errors.length > 0
      ? { data: projected.data, errors: projected.errors }
      : { data: projected.data };
  if (projected.errors.length > 0) {
    if (filter !== undefined) refuse();
    stderr = `gh: ${(projected.errors[0] as { message?: unknown }).message}\n`;
    status = 1;
  }
}
// A program reads the world's own answer; GitHub's measured error for a state the world does not
// hold yet (an attempt before the re-run) is answered as it stands.
// A REST read gh pages (--paginate) gets every page the route's program gives, as gh follows
// GitHub's `Link: rel="next"` from page to page: GitHub links the next page of a list that
// states its count (`total_count`) while the pages so far hold fewer, and of a bare array while
// the page was full and another follows. A route with no program answers its one page.
let pages: unknown[] | undefined;
if (matched.program !== undefined && answer.github === undefined) {
  const world = output;
  const run = (asked: Record<string, string>): unknown => {
    const program = Bun.spawnSync(
      [
        "jq",
        "-c",
        "--argjson",
        "flags",
        JSON.stringify(flags),
        "--argjson",
        "query",
        JSON.stringify(asked),
        "--argjson",
        "json",
        String(fields !== undefined),
        "--argjson",
        "now",
        String(Math.floor(Date.parse(at) / 1000)),
        matched.program as string,
      ],
      { env: childEnv, stdin: new TextEncoder().encode(JSON.stringify(world)) }
    );
    if (program.exitCode !== 0) refuse();
    return JSON.parse(program.stdout.toString());
  };
  output = run(query);
  if (command === "api" && method === "GET" && endpoint !== "graphql" && has("--paginate")) {
    /** The list of a page: the page itself, or the one list beside the count of an object. */
    const listOf = (page: unknown): unknown[] | undefined => {
      if (Array.isArray(page)) return page;
      if (typeof page !== "object" || page === null || !("total_count" in page)) return undefined;
      const lists = Object.values(page).filter(Array.isArray);
      return lists.length === 1 ? lists[0] : undefined;
    };
    const per = Number(query.per_page);
    let number = Number(query.page ?? "1");
    pages = [output];
    for (;;) {
      const last = pages[pages.length - 1];
      const list = listOf(last);
      if (list === undefined || !(per > 0)) break;
      const more = Array.isArray(last)
        ? list.length === per
        : number * per < Number((last as { total_count: unknown }).total_count);
      if (!more) break;
      number += 1;
      const next = run({ ...query, page: String(number) });
      if (Array.isArray(next) && next.length === 0) break;
      pages.push(next);
    }
  }
}
if (fields !== undefined && status === 0) {
  // A route that answers text, or a field its answer does not hold, is not what --json reads; a
  // call GitHub refuses prints its error, and no fields.
  const records = Array.isArray(output) ? output : [output];
  if (
    records.some(
      (record) =>
        typeof record !== "object" ||
        record === null ||
        Array.isArray(record) ||
        fields.some((field) => !(field in record))
    )
  )
    refuse();
  const picked = (record: Record<string, unknown>) =>
    Object.fromEntries([...new Set(fields)].sort().map((field) => [field, record[field]]));
  output = Array.isArray(output) ? output.map(picked) : picked(output as Record<string, unknown>);
}
// `--slurp` prints every page in one list, and ends paging.
if (has("--slurp")) {
  output = pages ?? [output];
  pages = undefined;
}

// The count of calls each route has answered outlives this process in fixtures.json, with the body
// where this call edited the pull request's and the review it sent with its inline comments: one
// read-modify-write of the file carries them all.
const prPatch = method === "PATCH" && /^repos\/[^/]+\/[^/]+\/pulls\/\d+$/.test(endpoint ?? "");
if (prPatch && typeof apiFields.body === "string") body = apiFields.body;
if (prPatch && input !== undefined) {
  const parsed: unknown = JSON.parse(files[input] ?? "null");
  if (
    typeof parsed === "object" &&
    parsed !== null &&
    "body" in parsed &&
    typeof parsed.body === "string"
  )
    body = parsed.body;
}
const edit = body;
const edited = fixtures.routes.map((candidate) => ({
  ...candidate,
  answered:
    candidate === matched ||
    (matched.counts_as !== undefined && candidate.match === matched.counts_as)
      ? (candidate.answered ?? 0) + 1
      : candidate.answered,
  ...(edit === undefined
    ? {}
    : {
        stdout: withBody(candidate.stdout, edit),
        replies: candidate.replies?.map((reply) => ({
          ...reply,
          stdout: withBody(reply.stdout, edit),
        })),
        settled:
          candidate.settled === undefined
            ? undefined
            : { ...candidate.settled, stdout: withBody(candidate.settled.stdout, edit) },
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
          reviews: [...keptReviews.reviews, sent.review],
          review_comments: [...keptReviews.comments, ...sent.comments],
        }),
  })
);
renameSync(temporary, `${run}/fixtures.json`);
closeSync(lock);
lock = undefined;
if (edit !== undefined) kept = edit;
// The pages of a paged read print as gh 2.98.0 prints them (measured, 2026-11-19, two pages of
// cybermonklabs/legion: `pulls/4148/comments?per_page=3` printed one array of the six,
// `commits/<sha>/check-runs?…&per_page=1` the two objects one after the other, nothing between);
// a `--jq` filter runs over each page, its outputs one after another.
const printed = pages ?? [output];
let text = pages?.every(Array.isArray)
  ? JSON.stringify(pages.flat())
  : printed.map((page) => (typeof page === "string" ? page : JSON.stringify(page))).join("");
let filtered = false;
if (filter !== undefined && status === 0) {
  text = "";
  filtered = true;
  for (const page of printed) {
    const jq = Bun.spawnSync(["jq", "-r", filter], {
      env: childEnv,
      stdin: new TextEncoder().encode(typeof page === "string" ? page : JSON.stringify(page)),
    });
    text += jq.stdout.toString();
    if (jq.exitCode !== 0) {
      if (text !== "") process.stdout.write(text);
      process.stderr.write(jq.stderr.toString());
      exit(1, false);
    }
  }
}
// `gh api` prints the body GitHub sent as it stands, with no newline after it, whether the request
// succeeded or not, and a `--jq` filter's output as jq ends it (measured); every other command ends
// what it prints with a newline.
if (text !== "")
  process.stdout.write(
    (command === "api" && !filtered) || text.endsWith("\n") ? text : `${text}\n`
  );
if (stderr) process.stderr.write(stderr);
if (matched.publish !== undefined) published = await publish(matched.publish);
exit(status, false);
