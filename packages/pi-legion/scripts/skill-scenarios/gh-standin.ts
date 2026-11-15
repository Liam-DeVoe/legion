#!/usr/bin/env bun
// Stand-in for `gh` on a skill scenario's PATH. Each call is recorded to
// $SKILL_SCENARIO_RUN/calls.jsonl (argv, the contents of any file or stdin it hands GitHub a body
// through, its exit status, the agent that made the call when the run names one
// ($SKILL_SCENARIO_AGENT, a run that boots one agent per role), the pull request body it
// kept, if any, and the review it left, if any: its state and head, as answered), then answered
// from the
// first route in $SKILL_SCENARIO_RUN/fixtures.json whose regex matches "gh <argv...>". Three
// answers are the run's own remote rather than a route's, since each is a fact of the run and no
// frozen answer could tell one run from another: the pull request's head, which a route carries
// and this answer replaces, and its diff and the comparison of two commits, which need no route
// at all (`branchDiff`, `comparison`). The checks of that head (`gh pr checks` and the pull
// request's `statusCheckRollup`) are the world's check-runs answer for the head the remote is at
// (`headRuns`), and its merge state (`mergeStateStatus`, `mergeable_state`) the one the world
// reports for that head (gh-call.ts's `mergeStateOn`). The rest of a review's life is this
// file's own and no world's, since GitHub answers it from what the run has done: the create of a
// review, refused 422 where GitHub refuses it and otherwise answered with the state, head and
// body the call gives it (`reviewCreate`); the submit of a review (`…/reviews/<id>/events`), answered
// from the review's own state (`reviewSubmission`); and the read of one review this run created
// (`reviewRead`). A route that names an `answer` is answered from what the run did — the reviews
// it created and their inline comments, the comments it posted, a ref or a commit's combined
// status read on its remote (`fromRun`) — and a route that holds a `graph` answers GraphQL from
// it, paged as GitHub pages a connection (`graphAnswer`, with gh-routes.ts's parser and
// executor). A REST read's list is paged by `per_page` and `page`, and `--paginate` asks for
// every page, as gh does (`restPages`). A call no route matches and no answer of this file's own
// serves prints `gh: Not Found (HTTP 404)` and exits 1, and so does a GraphQL mutation the
// world's graph holds no answer for. Nothing is sent anywhere.
// `--json a,b` picks fields and `--jq`/`-q` runs jq over each page, as gh does; `--slurp` prints
// the pages as one list. A pull request body edit (`gh pr edit --body`, `--body-file`, or a PATCH
// through `gh api`) is kept: every answer of the pull request then carries the new one, the
// edit's own answer among them, as GitHub's would, and the call's record carries it as `body`.
//
// The record opens with one line of its own, `{at, opened}`, written the first time this
// stand-in is invoked, as record-publishes.ts writes its `recording` line. rig.sh asks for that
// line alone before each worker run's agent starts (`--open`, its worker_boot), and it asks
// through the `gh` the pane's PATH resolves to rather than by this file's path, so the line says
// what the score reads it as: this run's own gh reached the stand-in. So a
// run whose
// agents called no gh and no bun leaves the record there holding that line and nothing else —
// the reading "no call" — while a record nothing wrote is absent, which is the rig error it has
// always been (score.ts's `unscored`). No score reads the line itself.
//   gh-standin.ts gh <args...>
//   gh-standin.ts gh --open   (what `$R/bin/gh --open` runs: the wrapper inserts the program word)
import { appendFileSync, existsSync, readFileSync, writeFileSync } from "node:fs";
import { z } from "zod";
// How a call's verb, its flags and its body are read, and the two review endpoints: one reader
// for this stand-in and for the score, imported rather than written again here. A call this
// stand-in answered as a submission and the score read as a read of the route — or the other
// way round — is a run scored against a world that answered it something else.
import {
  type CheckRun,
  checkRunsOn,
  createdHead,
  createdState,
  FIELD_FLAGS,
  flagValue,
  isPost,
  mergeStateOn,
  methodOf,
  reviewsRoute,
  routeFor,
  sentBody,
  submitsReview,
  submittedState,
  valueAt,
  worldOf,
} from "./gh-call";
// The GraphQL reader the sweep of the role texts reads their queries with, and the executor that
// answers a query from the world's graph: one reading of a query for the texts and the world.
import {
  endpointOf,
  executeGraphql,
  type GraphAnswer,
  isObject,
  type Operation,
  parseGraphql,
} from "./gh-routes";
import { CALLS, openingLine } from "./run-records";

/** rig.sh's worker_fixture writes each route as {match, methods?, stdout?, stderr?, exit?,
 * answer?, graph?}.
 * `methods` is the verbs that route answers, where it answers some and not others: the create of
 * a review is the POST of the path a GET of it reads the reviews from. A route that names none
 * answers whatever verb reaches it. The verb is read as the score reads it (`methodOf`) and
 * never by a regex over the call: a world that asked for `-X POST` written out sent a create
 * written `-f commit_id=<head>`, `--method=POST` or `-XPOST` to its GET route, recorded no
 * pending review for it, and refused the submit that followed where GitHub answers 200.
 * `answer` names a reading of the run's own record or remote this stand-in answers the route
 * with (`fromRun`), `stdout` being what the world held before the run; `graph` is the world's
 * state as GitHub's GraphQL schema names it, which a GraphQL call is answered from
 * (`graphAnswer`). */
const Fixtures = z.looseObject({
  routes: z.array(
    z.object({
      match: z.string(),
      methods: z.array(z.string()).optional(),
      stdout: z.unknown().optional(),
      stderr: z.string().optional(),
      exit: z.number().optional(),
      answer: z.string().optional(),
      graph: z.record(z.string(), z.unknown()).optional(),
    })
  ),
});

const run = process.env.SKILL_SCENARIO_RUN;
if (!run) {
  console.error("gh-standin: SKILL_SCENARIO_RUN is unset");
  process.exit(2);
}
/** This run's record of every call this stand-in answered, which it opens and reads back (the
 * pending review the run left) and the score reads after it. */
const RECORD = `${run}/${CALLS}`;
/** Opens the record with this stand-in's own line, which says it was there, and does nothing to
 * a record that is already open. */
function openRecord(): void {
  if (existsSync(RECORD)) return;
  appendFileSync(RECORD, openingLine(CALLS));
}
const invocation = Bun.argv.slice(2);
// `--open` is rig.sh asking for that line alone, before any agent of the run has made a call:
// `$R/bin/gh --open`, the stand-in reached the way the pane's PATH reaches it, where the wrapper
// inserts the program word. It is that flag on its own after that word, so no call of an
// agent's is taken for it — and `gh --open` is no command gh itself answers.
if (invocation.length === 2 && invocation[0] === "gh" && invocation[1] === "--open") {
  openRecord();
  process.exit(0);
}
/** The repository `repo` names when gh reads it as one of github.com, the host the world plays:
 * `github.com/OWNER/REPO` or `https://github.com/OWNER/REPO` (gh's `[HOST/]OWNER/REPO`, and the
 * URL it takes), as OWNER/REPO; `repo` as it stands for any other. */
function onGithub(repo: string): string {
  return /^(?:https?:\/\/)?github\.com\/([^/\s]+\/[^/\s]+?)(?:\.git)?\/?$/i.exec(repo)?.[1] ?? repo;
}
/** A gh call as the world answers it: `--hostname github.com` (in either spelling) left out, and
 * a `-R`/`--repo` of github.com's (`onGithub`) given as OWNER/REPO, wherever in the argv the flag
 * stands. github.com is gh's own default host, so GitHub answers such a call as it answers the
 * bare one, and every route of the world, and every reader of the record, reads it so: the
 * record keeps this argv. A call naming any other host is left as it stands, and no route of the
 * world answers it, as GitHub's never would. */
function asGithub(argv: readonly string[]): string[] {
  const read: string[] = [];
  for (let at = 0; at < argv.length; at++) {
    const word = argv[at] ?? "";
    if (word === "--hostname" && argv[at + 1]?.toLowerCase() === "github.com") at++;
    else if (word.toLowerCase() === "--hostname=github.com") continue;
    else if ((word === "-R" || word === "--repo") && at + 1 < argv.length)
      read.push(word, onGithub(argv[++at] ?? ""));
    else {
      const joined = /^(-R|--repo=)(.+)$/.exec(word);
      read.push(joined === null ? word : `${joined[1]}${onGithub(joined[2] ?? "")}`);
    }
  }
  return read;
}
const [as = "gh", ...given] = invocation;
const args = as === "gh" ? asGithub(given) : given;
const at = new Date().toISOString();
openRecord();
const fixtures = Fixtures.parse(JSON.parse(readFileSync(`${run}/fixtures.json`, "utf8")));
const routes = fixtures.routes;
/** The run's world (gh-call.ts's `World`, the one schema every program of the run reads it
 * by), or undefined when the run has none. This stand-in is the one reader that answers with no
 * world rather than exiting: a scenario that builds no world writes no world.json, and its calls
 * are answered from the routes alone, with the fixture's own head. Only a missing file is "no
 * world". A world.json that is there and is no world is the rig's own fault, and reading it as
 * none would serve the fixture's frozen head and diff to every call: a pushing role would read a
 * head that is not its own and stop, and a reviewer would read the diff without the fix it was
 * sent to read, with nothing anywhere saying why. So that file ends the call with 2, as it ends
 * every other program of the run (`worldOf`). */
const world = existsSync(`${run}/world.json`) ? worldOf(run, "gh-standin") : undefined;

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
// The verb is the one the score reads (`methodOf`), so `--method=patch` is the PATCH `-X PATCH`
// is.
const prEdit = as === "gh" && args[0] === "pr" && args[1] === "edit";
/** A `gh pr comment`, whose `-F/--body-file` names the file of the comment it posts, which the
 * record keeps so the conversation it leaves can be read back (`conversation`). */
const prComment = as === "gh" && args[0] === "pr" && args[1] === "comment";
const prPatch =
  as === "gh" &&
  args[0] === "api" &&
  methodOf(args) === "PATCH" &&
  args.some((arg) => /^\/?repos\/[^/]+\/[^/]+\/pulls\/\d+$/.test(arg));
let body: string | undefined;
/** The body this call left the pull request with, once the routes serve it. */
let kept: string | undefined;
/** The flags each file and each field is read by: `gh pr edit` takes the body's text by
 * `-b/--body` and its file by `-F/--body-file`, while the `-F` of a `gh api` call is a field
 * whose `@path` value is read out of a file, and `--input`/`--body-file` name a whole body's
 * file. Each is read in every spelling gh takes it in (`valueAt`), the spellings the body
 * reader reads too: read by exact match, an `--input=<file>` written against the flag was never
 * stored here, and `sentBody` then read the create that named it as a body of nothing. */
const PR_BODY_FLAGS = ["-b", "--body"];
const PR_BODY_FILE_FLAGS = ["-F", "--body-file"];
const INPUT_FLAGS = ["--input", "--body-file"];
const FILE_FIELD_FLAGS = ["-F", "--field"];
for (const index of args.keys()) {
  const edited = prEdit ? valueAt(args, index, PR_BODY_FLAGS) : undefined;
  const bodyFile = prEdit ? valueAt(args, index, PR_BODY_FILE_FLAGS) : undefined;
  const commentFile = prComment ? valueAt(args, index, PR_BODY_FILE_FLAGS) : undefined;
  const source = valueAt(args, index, INPUT_FLAGS);
  const field = valueAt(args, index, FIELD_FLAGS);
  // A flag with nothing after it, and one written against an empty value, names no file and no
  // field.
  if (edited) body = edited;
  else if (bodyFile) {
    files[bodyFile] = await readSource(bodyFile);
    body = files[bodyFile];
  } else if (commentFile) files[commentFile] = await readSource(commentFile);
  else if (source) files[source] = await readSource(source);
  else if (field) {
    const [key = "", ...rest] = field.split("=");
    let value = rest.join("=");
    if (valueAt(args, index, FILE_FIELD_FLAGS) !== undefined && value.startsWith("@")) {
      files[value.slice(1)] = await readSource(value.slice(1));
      value = files[value.slice(1)] ?? "";
    }
    if (prPatch && key === "body") body = value;
  }
}
const input = flagValue(args, ["--input"]);
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
const joined = `${as} ${args.join(" ")}`;
/** The verb gh sends this call with, which the routes that answer one verb and not another are
 * chosen by (`Fixtures`). */
const verb = methodOf(args);
/** The GraphQL operation a `gh api graphql` call sends, and its variables: the query its `query`
 * field or its `--input` body holds, read by the parser the sweep of the role texts reads them
 * with (gh-routes.ts), and every other field as a variable — a `-F` typed as gh types one (`true`,
 * `false`, `null`, an integer, the text of an `@file`), a `-f` as written — or an `--input`'s own
 * `variables`. `operation` is undefined for a query that is no GraphQL document. */
function readGraphql(): { operation: Operation | undefined; variables: Record<string, unknown> } {
  if (input !== undefined) {
    const sent = sentBody({ argv: args, stdin, files }) ?? {};
    return {
      operation: typeof sent.query === "string" ? parseGraphql(sent.query) : undefined,
      variables: isObject(sent.variables) ? sent.variables : {},
    };
  }
  let query = "";
  const variables: Record<string, unknown> = {};
  for (const index of args.keys()) {
    const pair = valueAt(args, index, FIELD_FLAGS);
    if (pair === undefined) continue;
    const equals = pair.indexOf("=");
    const name = equals < 0 ? pair : pair.slice(0, equals);
    const text = equals < 0 ? "" : pair.slice(equals + 1);
    let value: unknown = text;
    if (valueAt(args, index, FILE_FIELD_FLAGS) !== undefined) {
      if (text.startsWith("@")) value = files[text.slice(1)] ?? "";
      else if (text === "true" || text === "false") value = text === "true";
      else if (text === "null") value = null;
      else if (/^-?\d+$/.test(text)) value = Number(text);
    }
    if (name === "query") query = String(value);
    else variables[name] = value;
  }
  return { operation: parseGraphql(query), variables };
}
const graphqlCall =
  as === "gh" && args[0] === "api" && endpointOf(args) === "graphql" ? readGraphql() : undefined;
/** The route `routeFor` chooses for this call by that verb and the call's whole text (the rule
 * the daemon stand-in reads this world by too). */
const matched = routeFor(routes, args, as);
/** The route that answers this call: the one chosen, except for a mutation the world's graph
 * holds no answer for. GitHub answers such a mutation and the world does not, so it is refused as
 * every call the world has no answer for is, and `rig.sh health` reads it as the world answering
 * less than production. */
const route =
  matched?.graph !== undefined &&
  graphqlCall?.operation?.type === "mutation" &&
  !graphqlCall.operation.selections.every(
    (selection) =>
      selection.kind === "field" &&
      isObject(matched.graph?.mutation) &&
      selection.name in matched.graph.mutation
  )
    ? undefined
    : matched;
/** The review this call left on the pull request, as this stand-in answered it: its state and
 * the head it stands at. Set for a create the world answered with a review, for a submit of the
 * pending one, and for a `gh pr review`, which GitHub takes as a review at the pull request's
 * head; undefined for every other call. The daemon stand-in reads the run's reviews from these
 * and from nothing else (daemon-standin.ts's `roundOutcome`), so the approval the record moves on
 * is the one the world answered the agent with, read once, here. */
let answered: { state: string; head: string } | undefined;
/** Records the call with the status it exits with, then exits. `by` is the agent the run gave
 * this call's environment, so a run that boots one agent per role has each call say which role
 * made it; a run that names none records no `by`. A call that left a review records it
 * (`answered`) whatever its status: a submit answered 200 whose `--jq` then failed exits 1, and
 * GitHub holds the review all the same. */
function exit(status: number): never {
  const by = process.env.SKILL_SCENARIO_AGENT;
  appendFileSync(
    RECORD,
    `${JSON.stringify({ at, as, by, argv: args, files, stdin, route: route?.match ?? null, body: kept, review: answered, exit: status })}\n`
  );
  process.exit(status);
}
/** What `git -C <run>/remote.git <args...>` prints, or undefined when the command fails: the
 * run's own bare remote, read at the call. A run with no remote of its own fails every call. */
function fromRemote(...args: string[]): string | undefined {
  const read = Bun.spawnSync(["git", "-C", `${run}/remote.git`, ...args]);
  return read.exitCode === 0 ? read.stdout.toString().trim() : undefined;
}
// What GitHub answers for the pull request's head is the commit its branch points at, so a route
// whose object carries `headRefOid` answers the tip of the run's own remote rather than the
// fixture's first commit: a role that pushes and then reads the head, which the worker skill has
// every pushing role do, must see its own commit. The tip is read from the run's bare remote at
// the call, and a remote the run has not got leaves the fixture's value as it is.
function branchTip(): string | undefined {
  return world === undefined ? undefined : fromRemote("rev-parse", `refs/heads/${world.branch}`);
}
// What GitHub serves for `gh pr diff` is the pull request's diff as it stands, so the answer is
// the diff of the run's own remote at the call, the issue branch against the branch it is open
// against, rather than a diff frozen into the fixture, which would read the same whatever the
// run pushed. A role that reads the pull request after another role pushed to it — the reviewer,
// reading the fix the implementer pushed after an approval that left text threads open — must
// see what was pushed. `--name-only` answers the names alone, as `gh pr diff --name-only` does.
// A run with no world of its own has no remote to read, and its call falls through to the
// routes, where no world of rig.sh writes a `gh pr diff` (its round_world), so what such a call
// gets is the 404 below.
function branchDiff(nameOnly: boolean): string | undefined {
  if (world === undefined) return undefined;
  return fromRemote("diff", ...(nameOnly ? ["--name-only"] : []), world.base, world.branch);
}
/** `repos/<owner>/<repo>/compare/<base>...<head>`, with the query string `gh api` lets a caller
 * append. A ref may carry dots, and `...` is the separator, so each side is everything up to the
 * last `...`. */
const COMPARE = /^\/?repos\/[^/]+\/[^/]+\/compare\/([^?]+)\.\.\.([^?]+)(?:\?.*)?$/;
/** Whether the call asks for the comparison as a diff rather than as JSON: `gh api -H "Accept:
 * application/vnd.github.v3.diff"`, the media type the API documents for a diff, and the
 * `vnd.github.diff` spelling GitHub takes for the same answer. */
const DIFF_MEDIA_TYPE = /application\/vnd\.github(?:\.v3)?\.diff/;
/** A ref this stand-in will hand `git`. A compare ref is argv the caller wrote, and `git` reads
 * a leading `-` as an option wherever it falls, so a ref such as `--output=<path>...main` would
 * write a file; `--end-of-options`, which `git diff` does not take, cannot close that, so a ref
 * that starts with `-` is refused instead. There is no privilege boundary in this rig — the
 * agent that writes the ref already runs shell commands of its own — so this is a guard against
 * a ref that reads as an option and no sandbox. A refused ref is no comparison of this world,
 * and the call falls through to the routes, whose 404 is GitHub's answer for a ref it has
 * not got. */
const REF = /^[^-]/;
/** What GitHub answers for `gh api repos/<o>/<r>/compare/<base>...<head>`, read from the run's
 * own remote at the call, or undefined when this call is no such comparison or the remote cannot
 * resolve both refs. The rule has the implementer read that comparison to show its push changes
 * no code that runs (`skills/legion-worker/references/review-threads.md`, step 2), and the real
 * GitHub API answers it: measured on 2026-11-10, `gh api
 * repos/cybermonklabs/legion/compare/756789f8...main` printed `status`, `ahead_by`, `behind_by`
 * and 300 files and exited 0, while this stand-in answered `gh: Not Found (HTTP 404)` and the
 * implementer's comparison failed in all 10 text-fixes head runs of the batch before this. No
 * route serves it and none should: an answer frozen into the fixture would read the same
 * whatever the run pushed, where the two heads a comparison names are the run's own. `ahead_by`
 * and `behind_by` are the commits each side holds that the other has not, `status` is GitHub's
 * word for that pair, and `files` is the diff from the merge base, which is the change the head
 * carries, each file carrying the hunks of its own diff as `patch`, which is the field the
 * reviewer reads a change out of a comparison with and which this answered as `null` through
 * the recorded batch of 2026-11-10: a reviewer that asked for `.patch` got nothing and one fell
 * back to `jj file show`, working around the rig on the read step 3 of review-threads.md rests
 * on. The patch of each file is read with a `git diff` of its own path, since splitting one
 * combined diff on its `diff --git` headers would have to undo git's own quoting of a path; a
 * world of this rig holds four changed files. A call that asks for the diff media type
 * (DIFF_MEDIA_TYPE) is answered with the unified diff itself, as the API answers that `Accept`
 * header, and never with the JSON. Renames are not detected, so a moved file reads as one
 * added and one removed, as
 * GitHub's own answer does when it finds no rename. A ref the remote cannot resolve is no
 * comparison of this world, and the call falls through to the routes, whose 404 is GitHub's own
 * answer for a comparison of refs a repository has not got. */
function comparison(): string | Record<string, unknown> | undefined {
  if (as !== "gh" || args[0] !== "api") return undefined;
  // A comparison is GitHub's read of two refs, so a call that writes to that path is another
  // call and falls to the routes. The verb is the one the score reads (`methodOf`), never a
  // regex of this file's own: a write written `-XPOST`, `--method=post` or as a field with no
  // method was answered here with the two refs' diff, which told its caller a write it never
  // made had succeeded.
  if (verb !== "GET") return undefined;
  const named = args.map((arg) => COMPARE.exec(arg)).find((match) => match !== null);
  const [, base = "", head = ""] = named ?? [];
  if (base === "" || head === "") return undefined;
  if (!REF.test(base) || !REF.test(head)) return undefined;
  const range = [`${base}...${head}`];
  // The diff media type asks for the comparison as the diff itself, which is what the run's own
  // remote prints for the range; a remote that cannot resolve the two refs prints nothing and
  // the call falls through to the routes.
  if (DIFF_MEDIA_TYPE.test(joined)) return fromRemote("diff", "--no-renames", ...range);
  const ahead = fromRemote("rev-list", "--count", `${base}..${head}`);
  const behind = fromRemote("rev-list", "--count", `${head}..${base}`);
  const counts = fromRemote("diff", "--numstat", "--no-renames", ...range);
  const letters = fromRemote("diff", "--name-status", "--no-renames", ...range);
  if (ahead === undefined || behind === undefined) return undefined;
  if (counts === undefined || letters === undefined) return undefined;
  const aheadBy = Number(ahead);
  const behindBy = Number(behind);
  const status =
    aheadBy === 0 && behindBy === 0
      ? "identical"
      : behindBy === 0
        ? "ahead"
        : aheadBy === 0
          ? "behind"
          : "diverged";
  const words: Record<string, string> = { A: "added", M: "modified", D: "removed" };
  const statusOf = new Map(
    letters
      .split("\n")
      .filter((line) => line !== "")
      .map((line) => {
        const [letter = "", file = ""] = line.split("\t");
        return [file, words[letter.slice(0, 1)] ?? "modified"] as const;
      })
  );
  const files = counts
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => {
      const [added = "", deleted = "", filename = ""] = line.split("\t");
      // A binary file's counts are `-` in numstat, and GitHub reports 0 for one.
      const additions = Number(added) || 0;
      const deletions = Number(deleted) || 0;
      // GitHub's `patch` is the file's hunks alone, with no `diff --git` or `index` header, and
      // it is absent for a file git prints no hunk for, as GitHub leaves it out for a binary.
      const diff = fromRemote("diff", "--no-renames", ...range, "--", filename) ?? "";
      const hunks = diff.search(/^@@/m);
      return {
        filename,
        status: statusOf.get(filename) ?? "modified",
        additions,
        deletions,
        changes: additions + deletions,
        ...(hunks < 0 ? {} : { patch: diff.slice(hunks) }),
      };
    });
  return { status, ahead_by: aheadBy, behind_by: behindBy, total_commits: aheadBy, files };
}
/** The comparison this call asks for, when it asks for one the run's remote can answer. It is
 * read before the route check, since no route answers a comparison and a 404 there reads to the
 * agent as a repository that does not hold the two commits its own run made. */
const compared = comparison();
/** Refuses this call with GitHub's own text and its own status, where that refusal is decided:
 * the text goes to stderr as the call's whole answer and the record keeps the status, as the 404
 * below does. The answer the rest of this file shapes is the answer of a call that was taken, so
 * a refusal carried through it would have to be stepped around at each turn of that pipeline. */
function refuse(text: string): never {
  process.stderr.write(text);
  exit(1);
}
/** GitHub's own text for the submit of a review that is no longer pending. */
const NOT_PENDING =
  "gh: Unprocessable Entity (HTTP 422)\nCan not submit a review that is not pending.\n";
/** GitHub's own text for a submit that names no event, or one it does not take. The status and
 * the refusal are GitHub's — `event` is required on that call, and it answers 422 without it,
 * which is why a submit with none must not be answered from a route — while this sentence is
 * this stand-in's own, since GitHub's exact wording for the two cases is not measured here. */
const NO_EVENT =
  "gh: Unprocessable Entity (HTTP 422)\nA review is submitted with the event APPROVE, REQUEST_CHANGES or COMMENT.\n";
/** GitHub's own text for the create of a pending review while this user has one pending on the
 * pull request already. */
const ONE_PENDING =
  "gh: Unprocessable Entity (HTTP 422)\nUser can only have one pending review per pull request\n";
/** The create of a review naming an event GitHub does not take, and the create of a
 * `REQUEST_CHANGES` or `COMMENT` review with no body (gh-call.ts's `createdState`). GitHub
 * answers both 422; each sentence is this stand-in's own, as NO_EVENT's is, since GitHub's exact
 * wording for these two is not measured here. */
const CREATE_EVENT =
  "gh: Unprocessable Entity (HTTP 422)\nA review is created with the event APPROVE, REQUEST_CHANGES or COMMENT, or with none to leave it pending.\n";
const CREATE_BODY =
  "gh: Unprocessable Entity (HTTP 422)\nA review with the event REQUEST_CHANGES or COMMENT is created with a body.\n";
/** `POST …/pulls/<n>/reviews/<id>/events`, GitHub's submit of a review that is still pending.
 *
 * It is answered here rather than by a route because the answer turns on what this run has
 * already done: GitHub answers a pending review with the review itself, carrying the head it was
 * left at and the state the submission leaves it in, and every other id with 422. Almost every
 * review of these worlds is submitted by the call that created it — the create carries an
 * `event` — so a run reaches this endpoint by submitting a review that is no longer pending, and
 * 422 is the answer. A run that created a review without an event left a pending one, and that
 * is the case the review object answers, once.
 *
 * Measured on 2026-11-11, in the text-fixes head arm: a reviewer approved correctly at the head
 * its push made, then made this call again; the create route matched it and answered a review
 * object **with no `commit_id`** — an approval at no head, which GitHub never writes. The score
 * reads the last submission as the one that stands, found no head on it, and failed a run whose
 * reviewer had done its work. The world answering what GitHub refuses is this tree's recurring
 * defect, and the fix belongs here rather than in a reader taught to ignore a malformed
 * submission, which would hide the next invention.
 *
 * The refusal is this stand-in's and no world's, so it covers every world: `review_world` routed
 * no such call, and its generic POST route answered one `{id:6001}` and exit 0 — a reviewer that
 * approved wrongly and then sent `REQUEST_CHANGES` to this endpoint had that change request read
 * as the verdict that stands, which passes the defect control the arm exists to fail.
 *
 * A submit that names no event, or an event GitHub does not take, is refused here too (NO_EVENT)
 * rather than left to the routes: the create route answered such a call `{id}` and exit 0, so a
 * round that submitted nothing GitHub would take was told its review had been submitted. */
function reviewSubmission(): Record<string, unknown> | undefined {
  if (as !== "gh" || args[0] !== "api" || !isPost(args)) return undefined;
  // The repository of the run's own world: a submit on another repository's pull request is no
  // submit of this run's review, and a run that builds no world has no review of its own.
  const repo = world?.repo;
  if (repo === undefined) return undefined;
  const id = args.map((arg) => submitsReview(arg, repo)).find((named) => named !== undefined);
  if (id === undefined) return undefined;
  const pending = ownReviews(repo).pending;
  if (pending === undefined || pending.id !== id) refuse(NOT_PENDING);
  const body = sentBody({ argv: args, stdin, files }) ?? {};
  const state = submittedState(body.event);
  if (state === undefined) refuse(NO_EVENT);
  return { id: Number(id), commit_id: pending.head, state };
}
/** One review this run created on the world's pull request, as this stand-in answered it: the
 * world's answer to its create (the id, the URL and the user whose token the run calls with), the
 * state and the head the create — and, once it was submitted, the submit — were answered with,
 * the body it carries, the inline comments its create sent (`comments[]`) and when it was made. */
type OwnReview = {
  answer: Record<string, unknown>;
  id: string;
  state: string;
  head: string;
  body: string;
  comments: Record<string, unknown>[];
  at: string;
};
/** Every review this run created on `repo`'s pull request, by the id it was answered with, and
 * the one it has pending, or `undefined` when it has none. The record this reads is the
 * stand-in's own, `<run>/calls.jsonl`, whose first line is the opening line `openRecord` wrote
 * before any call of the run was answered and which is no call of an agent's.
 *
 * Each review is read off what this stand-in answered and recorded (`review`), and never worked
 * out again from the call: a create is a review where it was answered with one — a create GitHub
 * refuses, and one this world has no head for, recorded none — and its state and head are the
 * ones its answer carried, which is the head the pull request was at when it was made and not
 * the head it is at now. A submit is the submit of the pending review where it was answered with
 * the review: the id was the pending one's and the event one GitHub takes. The status a call was
 * recorded with says nothing about either — a submit answered 200 whose `--jq` then failed is
 * recorded with exit 1, and GitHub has submitted the review all the same — so a reader of the
 * status left it pending and answered the next submit of it 200 twice.
 *
 * The id is the one the create's own answer carried, read from the route the stand-in matched
 * that call to (`routeAnswer`), since the worlds answer a create with ids of their own: 5002 in
 * `approved_world` and 6001 in every other world (rig.sh). An id written here would answer for
 * one world and refuse the other. Every create of a world is answered with that one id, so the
 * review under an id is the last this run created with it. */
function ownReviews(repo: string): {
  byId: Map<string, OwnReview>;
  pending: OwnReview | undefined;
} {
  const byId = new Map<string, OwnReview>();
  let pending: OwnReview | undefined;
  for (const line of readFileSync(RECORD, "utf8").split("\n")) {
    if (line.trim() === "") continue;
    const call = JSON.parse(line) as {
      at?: string;
      argv?: string[];
      files?: Record<string, string>;
      stdin?: string;
      route?: string | null;
      review?: { state: string; head: string };
    };
    const argv = call.argv ?? [];
    if (call.review === undefined || argv[0] !== "api") continue;
    const sent = sentBody({ argv, stdin: call.stdin, files: call.files }) ?? {};
    const text = typeof sent.body === "string" && sent.body !== "" ? sent.body : undefined;
    const submitted = argv.map((arg) => submitsReview(arg, repo)).find((id) => id !== undefined);
    if (submitted !== undefined) {
      if (pending?.id === submitted) {
        pending.state = call.review.state;
        pending.body = text ?? pending.body;
        pending = undefined;
      }
      continue;
    }
    if (!argv.some((arg) => reviewsRoute(arg, repo))) continue;
    const answer = routeAnswer(call.route);
    const id = answer?.id;
    if (answer === undefined || (typeof id !== "number" && typeof id !== "string")) continue;
    const comments = Array.isArray(sent.comments) ? sent.comments.filter(isObject) : [];
    const review = {
      answer,
      id: String(id),
      ...call.review,
      body: text ?? "",
      comments,
      at: call.at ?? "",
    };
    byId.set(review.id, review);
    if (review.state === "PENDING") pending = review;
  }
  return { byId, pending };
}
/** The object the route a recorded call was matched to answers with, which the record names
 * (`route`, the route's own regex), or undefined when it answered no object. */
function routeAnswer(match: string | null | undefined): Record<string, unknown> | undefined {
  if (match === null || match === undefined) return undefined;
  const stdout = routes.find((candidate) => candidate.match === match)?.stdout;
  return typeof stdout === "object" && stdout !== null && !Array.isArray(stdout)
    ? (stdout as Record<string, unknown>)
    : undefined;
}
/** `POST …/pulls/<n>/reviews` on the world's own repository, GitHub's create of a review: the
 * state it leaves the review in and the head it creates it at, or undefined for every other
 * call. Both are the call's own, read by the rules score.ts reads the recorded create by
 * (gh-call.ts's `createdState` and `createdHead`), the head where the body names none being the
 * tip of the run's own remote at the call.
 *
 * A create GitHub refuses is refused here, 422, where that is decided: an event GitHub does not
 * take (`approve`, `PENDING`), a `REQUEST_CHANGES` or `COMMENT` with no body, and the create of a
 * pending review while this run has one pending already, which GitHub refuses as "User can only
 * have one pending review per pull request". The world's routes answered each of these 200 with
 * an id, and the last one replaced the pending review the round had been writing with a new one
 * at another head.
 *
 * The body is the call's own too, and GitHub answers the create with it (`body`, "" for a create
 * that sent none): a create answered without it left a reviewer reading its own footer back with
 * `--jq '(.body|capture(…))'` on null, and exit 1 on a review GitHub had taken
 * (round17-forty/reviewer-text-findings-defect-head-4/calls.jsonl:29). */
function reviewCreate(): { state: string; head: string | undefined; body: string } | undefined {
  if (as !== "gh" || args[0] !== "api" || !isPost(args)) return undefined;
  const repo = world?.repo;
  if (repo === undefined || !args.some((arg) => reviewsRoute(arg, repo))) return undefined;
  const sent = sentBody({ argv: args, stdin, files }) ?? {};
  const state = createdState(sent);
  if (state === undefined)
    refuse(submittedState(sent.event) === undefined ? CREATE_EVENT : CREATE_BODY);
  if (state === "PENDING" && ownReviews(repo).pending !== undefined) refuse(ONE_PENDING);
  const body = typeof sent.body === "string" ? sent.body : "";
  return { state, head: createdHead(sent, branchTip()), body };
}
/** `GET …/pulls/<n>/reviews/<id>`, with the query string `gh api` lets a caller append: the
 * repository it names and the id it reads. */
const REVIEW_READ = /^\/?repos\/(.+)\/pulls\/\d+\/reviews\/(\d+)(?:\?.*)?$/;
/** GitHub's read of one review of the world's pull request, for a review this run created: the
 * review as it stands — the world's answer to its create, with the state, the head and the body
 * it has now — or undefined for every other call, which the routes answer. A world's routes
 * cannot answer it, since it turns on what the run did: the list route answered the read of
 * review 6001 with every review of the pull request, and review_world's with `[]`, so a round
 * that read its own review back with `--jq '{id,state,commit_id}'` failed on its own approval
 * (`text-only-head-3` and `defect-base-3` of review 5887659447). */
function reviewRead(): Record<string, unknown> | undefined {
  if (as !== "gh" || args[0] !== "api" || methodOf(args) !== "GET") return undefined;
  const repo = world?.repo;
  if (repo === undefined) return undefined;
  const id = args.map((arg) => REVIEW_READ.exec(arg)).find((named) => named?.[1] === repo)?.[2];
  const review = id === undefined ? undefined : ownReviews(repo).byId.get(id);
  return review === undefined ? undefined : reviewObject(review);
}
/** A review this run created as GitHub answers it: the world's answer to its create, with the
 * state, the head and the body it has now, and the time it was submitted once it is no longer
 * pending. */
function reviewObject(review: OwnReview): Record<string, unknown> {
  const { answer, state, head, body, at: made } = review;
  return {
    ...answer,
    id: Number(review.id),
    state,
    commit_id: head,
    body,
    ...(state === "PENDING" ? {} : { submitted_at: made }),
  };
}
const submitted = reviewSubmission();
if (submitted !== undefined)
  answered = { state: String(submitted.state), head: String(submitted.commit_id) };
const created = reviewCreate();
/** This call's answer where it is this stand-in's own and no route's: the submit of the pending
 * review, and the read of a review this run created. Each stands over whatever route the call
 * matched. */
const own = submitted ?? reviewRead();
/** The check runs the world reports on the pull request's head as it stands, read at the call
 * from the run's own remote (`branchTip`) and the world's check-runs answer for that head
 * (`checkRunsOn`), or undefined for a run with no world or no remote of its own. Every answer
 * that reports the head's checks is built from these, so a world that has a check still running
 * on one head and none on the next — the push that set an approval aside, then the head its
 * checks concluded on — tells `gh pr checks`, the pull request's own `statusCheckRollup` and
 * the check-runs route one thing. */
function headRuns(): CheckRun[] | undefined {
  const repo = world?.repo;
  const tip = branchTip();
  return repo === undefined || tip === undefined ? undefined : checkRunsOn(routes, repo, tip);
}
/** `gh pr checks`, as gh prints it to a pipe: one line per check, its name, `pass`, `fail`,
 * `skipping` or `pending`, the seconds it ran and its link; and gh's own exit, 8 while a check is
 * pending and 1 once one failed. Undefined for any other call, and for a run with no world. */
function prChecks(): { text: string; status: number } | undefined {
  if (as !== "gh" || args[0] !== "pr" || args[1] !== "checks") return undefined;
  const runs = headRuns();
  if (runs === undefined) return undefined;
  const lines = runs.map((run) => {
    let bucket = "pending";
    if (run.status === "completed")
      bucket =
        run.conclusion === "skipped"
          ? "skipping"
          : ["success", "neutral"].includes(run.conclusion ?? "")
            ? "pass"
            : "fail";
    const seconds =
      run.started_at === undefined || run.completed_at === undefined
        ? 0
        : Math.round((Date.parse(run.completed_at) - Date.parse(run.started_at)) / 1000);
    return { bucket, line: `${run.name}\t${bucket}\t${seconds}s\t${run.html_url ?? ""}` };
  });
  const buckets = lines.map(({ bucket }) => bucket);
  return {
    text: lines.map(({ line }) => line).join("\n"),
    status: buckets.includes("fail") ? 1 : buckets.includes("pending") ? 8 : 0,
  };
}
const checked = prChecks();
/** The event each `gh pr review` flag submits, and the state GitHub leaves its review in. */
const PR_REVIEW_STATE: Record<string, string> = {
  "-a": "APPROVED",
  "--approve": "APPROVED",
  "-r": "CHANGES_REQUESTED",
  "--request-changes": "CHANGES_REQUESTED",
  "-c": "COMMENTED",
  "--comment": "COMMENTED",
};
// `gh pr review` submits a review at the pull request's head as it stands, on the repository the
// call names or, naming none, the checkout's own: the world's, where a route of the world
// answers it.
if (route !== undefined && as === "gh" && args[0] === "pr" && args[1] === "review") {
  const named = flagValue(args, ["-R", "--repo"]);
  const state = args.map((arg) => PR_REVIEW_STATE[arg]).find((one) => one !== undefined);
  const tip = branchTip();
  if (world !== undefined && (named === undefined || named === world.repo) && state && tip)
    answered = { state, head: tip };
}
/** The pull request's diff, where this call asks for one the run's remote can answer. It is read
 * before the route check for the same reason: rig.sh's worlds carry no `gh pr diff` route, since
 * a diff frozen into a fixture would read the same whatever the run pushed, and a 404 here reads
 * to the agent as a pull request carrying no change at all. */
const diffed =
  as === "gh" && args[0] === "pr" && args[1] === "diff"
    ? branchDiff(args.includes("--name-only"))
    : undefined;
if (
  !route &&
  compared === undefined &&
  diffed === undefined &&
  own === undefined &&
  checked === undefined
) {
  console.error(as === "gh" ? "gh: Not Found (HTTP 404)" : `unknown command: ${args.join(" ")}`);
  exit(1);
}
/** `answer` with `shape` applied to it, or to each of its elements when the answer is a list.
 * `gh pr list` answers the open pull requests as a list of the objects `gh pr view` answers one
 * of (rig.sh's fixture_write), so every rule over an answer's object is a rule over a listed
 * one: while the head, the field selection and the kept body reached the object alone, a role
 * that listed the pull request after a fix push read the head the fixture froze, and a listing
 * after a body edit read the body before it. A value that is no object is its own answer. */
function eachAnswer(answer: unknown, shape: (one: Record<string, unknown>) => unknown): unknown {
  const one = (value: unknown) =>
    typeof value === "object" && value !== null && !Array.isArray(value)
      ? shape(value as Record<string, unknown>)
      : value;
  return Array.isArray(answer) ? answer.map(one) : one(answer);
}
/** `answer` with each object of the pull request in it carrying `edited` as its body: the object
 * a route answers with is this world's pull request when it carries the head that pull request
 * is at (`headRefOid`, which the worlds write on that object and on no other). */
function withBody(answer: unknown, edited: string): unknown {
  return eachAnswer(answer, (one) =>
    "body" in one && "headRefOid" in one ? { ...one, body: edited } : one
  );
}
// A body edit is an edit of the pull request's own body, so it is kept on the pull request's own
// answers and on no others (`withBody`). Applied to every object that has a `body` at all, it
// overwrote the body of each review the reviews route answers, so after the implementer's body
// edit the approval already on the pull request read back as the pull request's own text.
if (body !== undefined) {
  const edited = routes.map((candidate) => {
    const stdout = withBody(candidate.stdout, body);
    return stdout === candidate.stdout ? candidate : { ...candidate, stdout };
  });
  writeFileSync(`${run}/fixtures.json`, JSON.stringify({ ...fixtures, routes: edited }));
  kept = body;
}
// The comparison, where the call asked for one, is the answer; an answer this stand-in gives of
// its own — a submit it took, the review itself, and the read of a review this run created —
// stands over whatever route matched it, while every other submit was refused where that was
// decided (`refuse`); every other call is its route's. The call that edited the body is
// answered with the body it left, since the route was read before the edit: GitHub answers a
// PATCH of the pull request with the pull request as that PATCH left it, and the world's route
// for it answered the body before the edit, where the generic write route had answered `{id,
// html_url}` and 23 recorded PATCH calls read `.body` back as null.
/** GitHub's 404, for a read of something the world has not got. */
const NOT_FOUND = "gh: Not Found (HTTP 404)\n";
/** The login the run's token acts as: the world's answer to `gh api user`. */
function login(): string {
  const user = routeFor(routes, ["api", "user"])?.stdout;
  return isObject(user) && typeof user.login === "string" ? user.login : "";
}
/** The pull request as the world's `gh pr view` route holds it, or an empty object. */
function pullRequestHeld(): Record<string, unknown> {
  const held = routeFor(routes, ["pr", "view"])?.stdout;
  return isObject(held) ? held : {};
}
/** The comments this run posted on the pull request's conversation, oldest first: each
 * `gh pr comment` of the world's repository and each POST of `repos/<repo>/issues/<n>/comments`
 * the record holds exiting 0, with the body it posted and when it posted it. */
function conversation(): { body: string; at: string }[] {
  if (world === undefined) return [];
  const path = `repos/${world.repo}/issues/${world.pr}/comments`;
  const posted: { body: string; at: string }[] = [];
  for (const line of readFileSync(RECORD, "utf8").split("\n")) {
    if (line.trim() === "") continue;
    const call = JSON.parse(line) as {
      at?: string;
      as?: string;
      argv?: string[];
      files?: Record<string, string>;
      stdin?: string;
      exit?: number;
    };
    const argv = call.argv ?? [];
    if (call.as !== "gh" || call.exit !== 0) continue;
    let text: string | undefined;
    if (argv[0] === "pr" && argv[1] === "comment") {
      const named = flagValue(argv, ["-R", "--repo"]);
      if (named !== undefined && named !== world.repo) continue;
      const file = flagValue(argv, ["-F", "--body-file"]);
      text =
        flagValue(argv, ["-b", "--body"]) ??
        (file === undefined ? undefined : file === "-" ? call.stdin : call.files?.[file]);
    } else if (
      argv[0] === "api" &&
      isPost(argv) &&
      argv.some((arg) => arg.replace(/^\//, "").split("?")[0] === path)
    ) {
      const sent = sentBody({ argv, stdin: call.stdin, files: call.files }) ?? {};
      text = typeof sent.body === "string" ? sent.body : undefined;
    }
    if (text !== undefined) posted.push({ body: text, at: call.at ?? "" });
  }
  return posted;
}
/** The conversation comment at `index` of `conversation()`, as GitHub's REST API answers it. */
function issueComment(comment: { body: string; at: string }, index: number) {
  const id = 8001 + index;
  const url = String(pullRequestHeld().url ?? "");
  return {
    id,
    node_id: `IC_kwDOAAAAAc4AAB${id}`,
    html_url: `${url}#issuecomment-${id}`,
    issue_url: `https://api.github.com/repos/${world?.repo}/issues/${world?.pr}`,
    user: { login: login(), type: "Bot" },
    body: comment.body,
    created_at: comment.at,
    updated_at: comment.at,
    author_association: "NONE",
  };
}
/** Every inline comment the run's review creates sent (`comments[]`), as GitHub's REST API
 * answers a pull request's review comments, in the order the reviews were created. */
function inlineComments(): Record<string, unknown>[] {
  if (world === undefined) return [];
  const url = String(pullRequestHeld().url ?? "");
  let next = 9001;
  return [...ownReviews(world.repo).byId.values()].flatMap((review) =>
    review.comments.map((comment) => {
      const id = next++;
      return {
        ...comment,
        id,
        pull_request_review_id: Number(review.id),
        side: comment.side ?? "RIGHT",
        commit_id: review.head,
        user: { login: login(), type: "Bot" },
        html_url: `${url}#discussion_r${id}`,
        created_at: review.at,
      };
    })
  );
}
/** A route's answer read from what the run did (rig.sh's fixture_write names which, as `answer`),
 * with `held` the route's own `stdout`, what the world held before the run:
 *   - `reviews`: the world's reviews, then each review this run created as it stands;
 *   - `review`: the world's review the path names by id, or GitHub's 404 (a review this run
 *     created is `reviewRead`'s, read before the routes);
 *   - `review-comments`: the inline comments of the run's review creates, all of them, or the
 *     ones of the review the path names, which is 404 where neither the world nor the run holds it;
 *   - `issue-comments`: the world's conversation, then each comment the run posted on it;
 *   - `combined-status`: GitHub's combined status of the ref the path names, resolved on the run's
 *     own remote — the world's statuses, `state` `pending` where there are none, and 422 for a ref
 *     the remote has not got;
 *   - `git-ref`: the ref the path names on the run's own remote, or GitHub's 404. */
function fromRun(kind: string, held: unknown): unknown {
  const repo = world?.repo ?? "";
  const path = (endpointOf(args) ?? "").replace(/^\//, "").split("?")[0] ?? "";
  const reviewsHeld = () => {
    const listed = routeFor(routes, ["api", `repos/${repo}/pulls/${world?.pr}/reviews`])?.stdout;
    return Array.isArray(listed) ? listed : [];
  };
  switch (kind) {
    case "reviews":
      return [
        ...(Array.isArray(held) ? held : []),
        ...(world === undefined ? [] : [...ownReviews(repo).byId.values()].map(reviewObject)),
      ];
    case "review": {
      const id = /\/reviews\/(\d+)$/.exec(path)?.[1];
      const found = (Array.isArray(held) ? held : []).find(
        (review) => isObject(review) && String(review.id) === id
      );
      if (found === undefined) refuse(NOT_FOUND);
      return found;
    }
    case "review-comments": {
      const id = /\/reviews\/(\d+)\/comments$/.exec(path)?.[1];
      if (id === undefined) return [...(Array.isArray(held) ? held : []), ...inlineComments()];
      const known =
        (world !== undefined && ownReviews(repo).byId.has(id)) ||
        reviewsHeld().some((review) => isObject(review) && String(review.id) === id);
      if (!known) refuse(NOT_FOUND);
      return inlineComments().filter((comment) => String(comment.pull_request_review_id) === id);
    }
    case "issue-comments":
      return [...(Array.isArray(held) ? held : []), ...conversation().map(issueComment)];
    case "combined-status": {
      const ref = /\/commits\/([^/]+)\/status$/.exec(path)?.[1] ?? "";
      const sha = REF.test(ref)
        ? fromRemote("rev-parse", "--verify", "--quiet", `${ref}^{commit}`)
        : undefined;
      if (sha === undefined || sha === "")
        refuse(`gh: No commit found for SHA: ${ref} (HTTP 422)\n`);
      const statuses = isObject(held) && Array.isArray(held.statuses) ? held.statuses : [];
      const states = statuses.map((status) => (isObject(status) ? status.state : undefined));
      const state = states.some((one) => one === "error" || one === "failure")
        ? "failure"
        : statuses.length === 0 || states.includes("pending")
          ? "pending"
          : "success";
      const commit = `https://api.github.com/repos/${repo}/commits/${sha}`;
      return {
        state,
        statuses,
        sha,
        total_count: statuses.length,
        repository: { full_name: repo },
        commit_url: commit,
        url: `${commit}/status`,
      };
    }
    case "git-ref": {
      const ref = /\/git\/ref\/(.+)$/.exec(path)?.[1] ?? "";
      const sha = REF.test(ref)
        ? fromRemote("rev-parse", "--verify", "--quiet", `refs/${ref}^{commit}`)
        : undefined;
      if (sha === undefined || sha === "") refuse(NOT_FOUND);
      return {
        ref: `refs/${ref}`,
        url: `https://api.github.com/repos/${repo}/git/refs/${ref}`,
        object: {
          sha,
          type: "commit",
          url: `https://api.github.com/repos/${repo}/git/commits/${sha}`,
        },
      };
    }
  }
  return held;
}
let output: unknown;
if (compared !== undefined) output = compared;
else if (own !== undefined) output = own;
else if (route?.answer !== undefined) output = fromRun(route.answer, route.stdout);
else output = route?.stdout ?? "";
if (kept !== undefined) output = withBody(output, kept);
// What GitHub answers the create of a review with is the review it created, and three of its
// fields are the call's own and no world's (`reviewCreate`): the state, which is `PENDING` for a
// create that names no event and the state its event leaves the review in for one that does,
// the head it was created at, and its body. The world's route carries what is the world's — the
// id it answers every create with, the URL and the user whose token the run calls with — since a
// review a run created has no id of this file's own. The create answered with neither state nor
// head is what left a round reading back `{id, html_url}`: it could not tell whether its
// approval had been submitted, and it submitted again.
if (
  created !== undefined &&
  typeof output === "object" &&
  output !== null &&
  !Array.isArray(output)
) {
  const { state, head, body: written } = created;
  output = { ...output, state, body: written, ...(head === undefined ? {} : { commit_id: head }) };
  if (head !== undefined) answered = { state, head };
}
/** The pull request `one` as it stands: at the tip of the run's own remote (`tip`), with the
 * checks of that head (`runs`) and the merge state the world reports for it, and with the
 * comments the run posted on its conversation after the world's own. A `one` that is no pull
 * request of the world (it carries no `headRefOid`) is its own answer. */
function current(
  one: Record<string, unknown>,
  tip: string | undefined,
  runs: CheckRun[] | undefined
): Record<string, unknown> {
  if (!("headRefOid" in one)) return one;
  const comments = Array.isArray(one.comments)
    ? {
        comments: [
          ...one.comments,
          ...conversation().map((comment, index) => {
            const rest = issueComment(comment, index);
            return {
              id: rest.node_id,
              author: { login: rest.user.login },
              authorAssociation: rest.author_association,
              body: rest.body,
              createdAt: rest.created_at,
              url: rest.html_url,
            };
          }),
        ],
      }
    : {};
  if (tip === undefined) return { ...one, ...comments };
  const nested =
    "head" in one && typeof one.head === "object" && one.head !== null
      ? { head: { ...one.head, sha: tip } }
      : {};
  const rollup =
    "statusCheckRollup" in one && runs !== undefined
      ? {
          statusCheckRollup: runs.map((run) => ({
            name: run.name,
            status: run.status.toUpperCase(),
            conclusion: (run.conclusion ?? "").toUpperCase(),
          })),
        }
      : {};
  const state = mergeStateOn(fixtures.mergeStates, tip);
  const merge = {
    ...("mergeStateStatus" in one ? { mergeStateStatus: state } : {}),
    ...("mergeable_state" in one ? { mergeable_state: state.toLowerCase() } : {}),
  };
  return { ...one, headRefOid: tip, ...nested, ...rollup, ...merge, ...comments };
}
// What GitHub answers for a pull request's head is read from the run's own remote, and only for
// an answer that carries one: the tip is a `git` call, which a call answered by any other route
// never makes. The checks the object reports (`statusCheckRollup`) are that head's own, in the
// spelling GitHub's GraphQL gives them (`headRuns`): a rollup frozen into the world read green
// on a head whose check the check-runs route said was still running. So is its merge state, in
// gh's spelling (`mergeStateStatus`) and the REST one (`mergeable_state`), which the daemon
// stand-in moves the record on (daemon-standin.ts's `roundOutcome`).
if (
  (Array.isArray(output) ? output : [output]).some(
    (one) => typeof one === "object" && one !== null && "headRefOid" in one
  )
) {
  const tip = branchTip();
  const runs = headRuns();
  output = eachAnswer(output, (one) => current(one, tip, runs));
}
if (checked !== undefined) output = checked.text;
// An empty diff is GitHub's own answer for a pull request whose branch carries no change, so it
// stands as the answer rather than falling back to the route's.
if (diffed !== undefined) output = diffed === "" ? "" : `${diffed}\n`;
/** GitHub's answer to a query that is no GraphQL document. GitHub refuses it with its parser's
 * own message, which names where in the query it stopped; this sentence is this stand-in's, since
 * that message is not measured here. */
const PARSE_ERROR = "Parse error: the query is no GraphQL document";
/** The `pageInfo` of the first connection `value` holds, depth first, which is the one gh follows
 * when it pages a GraphQL query. */
function pageInfoIn(value: unknown): Record<string, unknown> | undefined {
  if (Array.isArray(value)) return value.map(pageInfoIn).find((found) => found !== undefined);
  if (!isObject(value)) return undefined;
  if ("hasNextPage" in value) return value;
  return Object.values(value)
    .map(pageInfoIn)
    .find((found) => found !== undefined);
}
/** A GraphQL call answered from `graph`, the world's state as GitHub's schema names it (rig.sh's
 * fixture_write): a query from its `query` root, whose pull request is the world's own as it
 * stands (`current`), and a mutation from its `mutation` root. With `--paginate` and a query that
 * takes `$endCursor`, gh asks again with the `endCursor` of the page it was answered until that
 * page has no next one, and so does this: each page is one answer, and a world that answered
 * every page alike would hold a `--paginate` loop for ever. */
function graphAnswer(
  graph: Record<string, unknown>,
  call: { operation: Operation | undefined; variables: Record<string, unknown> }
): GraphAnswer[] {
  const { operation } = call;
  if (operation === undefined) return [{ errors: [{ message: PARSE_ERROR }] }];
  let root: Record<string, unknown>;
  if (operation.type === "query") {
    const query = isObject(graph.query) ? graph.query : {};
    const repository = isObject(query.repository) ? query.repository : {};
    const pullRequest = isObject(repository.pullRequest) ? repository.pullRequest : {};
    const now = current(pullRequestHeld(), branchTip(), headRuns());
    root = { ...query, repository: { ...repository, pullRequest: { ...now, ...pullRequest } } };
  } else root = isObject(graph.mutation) ? graph.mutation : {};
  const variables = { ...call.variables };
  const pages =
    args.includes("--paginate") && operation.variables.some(({ name }) => name === "endCursor");
  const answers: GraphAnswer[] = [];
  for (let page = 0; page < 100; page++) {
    const answer = executeGraphql(operation, root, variables);
    answers.push(answer);
    const info = pageInfoIn(answer.data);
    if (!pages || answer.errors !== undefined || info?.hasNextPage !== true) break;
    if (typeof info.endCursor !== "string") break;
    variables.endCursor = info.endCursor;
  }
  return answers;
}
/** The lists GitHub pages inside an object it answers: a commit's check runs, the workflow runs,
 * a combined status's statuses and a run's jobs. */
const PAGED = ["check_runs", "workflow_runs", "statuses", "jobs"];
/** A REST read's answer, paged as GitHub pages it: `per_page` items (30 unless the query string
 * names another, at most 100) from the page the query string names (1 unless it names another),
 * of the list the answer is or the one list it holds (`PAGED`); with `--paginate`, that page and
 * every page after it, which gh asks for in turn. An answer that is no list is its one page. */
function restPages(answer: unknown): unknown[] {
  const query = new URLSearchParams((endpointOf(args) ?? "").split("?")[1] ?? "");
  const perPage = Math.min(100, Math.max(1, Number(query.get("per_page") ?? 30) || 30));
  const first = Math.max(1, Number(query.get("page") ?? 1) || 1);
  const key = isObject(answer) ? PAGED.find((name) => Array.isArray(answer[name])) : undefined;
  const list = Array.isArray(answer)
    ? answer
    : isObject(answer) && key !== undefined
      ? (answer[key] as unknown[])
      : undefined;
  if (list === undefined) return [answer];
  const pageOf = (page: number) => {
    const items = list.slice((page - 1) * perPage, page * perPage);
    return isObject(answer) && key !== undefined ? { ...answer, [key]: items } : items;
  };
  const pages = [pageOf(first)];
  if (args.includes("--paginate"))
    for (let page = first + 1; (page - 1) * perPage < list.length; page++) pages.push(pageOf(page));
  return pages;
}
const graphPages =
  route?.graph !== undefined && graphqlCall !== undefined
    ? graphAnswer(route.graph, graphqlCall)
    : undefined;
/** The documents gh prints for this call: one per page it was answered with — a GraphQL page, a
 * REST page — and one for every other answer; with `--slurp`, the pages as one list. */
let docs: unknown[] =
  graphPages ??
  (as === "gh" && args[0] === "api" && verb === "GET" && typeof output !== "string"
    ? restPages(output)
    : [output]);
if (args.includes("--slurp")) docs = [docs];
const jsonIndex = args.indexOf("--json");
if (jsonIndex >= 0) {
  const fields = (args[jsonIndex + 1] ?? "").split(",");
  docs = docs.map((doc) =>
    eachAnswer(doc, (one) =>
      Object.fromEntries(fields.filter((field) => field in one).map((field) => [field, one[field]]))
    )
  );
}
const jqIndex = args.findIndex((arg) => arg === "--jq" || arg === "-q");
const filter = jqIndex >= 0 ? args[jqIndex + 1] : undefined;
const texts = docs.map((doc) => (typeof doc === "string" ? doc : JSON.stringify(doc)));
// gh runs `--jq` over each page on its own, and without it prints the pages of a list as the one
// list they make.
let text: string;
if (filter !== undefined) {
  text = "";
  for (const one of texts) {
    const jq = Bun.spawnSync(["jq", "-r", filter], { stdin: new TextEncoder().encode(one) });
    text += jq.stdout.toString();
    if (jq.exitCode !== 0) {
      process.stderr.write(jq.stderr.toString());
      exit(1);
    }
  }
} else if (docs.length > 1 && docs.every(Array.isArray)) text = JSON.stringify(docs.flat());
else text = texts.join("\n");
if (text !== "") process.stdout.write(text.endsWith("\n") ? text : `${text}\n`);
if (route?.stderr && own === undefined) process.stderr.write(route.stderr);
// GitHub answers a GraphQL query it refuses, or finds part of it naming nothing, with `errors`
// beside whatever data it found, and gh prints each and exits 1.
const graphErrors = (graphPages ?? []).flatMap((page) => page.errors ?? []);
for (const error of graphErrors) process.stderr.write(`gh: ${error.message}\n`);
// An answer this stand-in gave of its own is GitHub's own answer, whatever the route behind it
// says: the review and 200 for the one review this run left pending, which is every review of
// these worlds a run left pending itself, and for a review this run created. `gh pr checks`
// exits as gh does on the checks it printed.
if (checked !== undefined) exit(checked.status);
if (graphErrors.length > 0) exit(1);
exit(own === undefined ? (route?.exit ?? 0) : 0);
