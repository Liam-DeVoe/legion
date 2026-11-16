#!/usr/bin/env bun
// Stand-in for `gh` on a skill scenario's PATH. Each call is recorded to
// $GH_DATA_DIR/calls.jsonl (argv, the contents of any file or stdin it hands GitHub a body
// through, its exit status, the agent that made the call when its pane names one ($LEGION_ROLE,
// which production gives every pane and a run that boots one agent per role gives each), the
// pull request body it kept, if any, and the review it left, if any: its state and head, as
// answered), then answered from the
// first route in $GH_DATA_DIR/fixtures.json whose regex matches "gh <argv...>". Three
// answers are the run's own remote rather than a route's, since each is a fact of the run and no
// frozen answer could tell one run from another: the pull request's head, which a route carries
// and this answer replaces, and its diff and the comparison of two commits, which need no route
// at all (`branchDiff`, `comparison`). The checks of that head (`gh pr checks` and the pull
// request's `statusCheckRollup`) are the world's check-runs answer for the head the remote is at,
// reported on that head as GitHub reports a check run (`headRuns`, `reportedRuns`), and its merge
// state (`mergeStateStatus`, `mergeable_state`) the one the world reports for that head, held
// `BLOCKED` while a thread is open where its base requires them resolved (gh-threads.ts's
// `mergeStateOf`). The rest of a review's life is this
// file's own and no world's, since GitHub answers it from what the run has done: the create of a
// review, refused 422 where GitHub refuses it and otherwise answered with the state, head and
// body the call gives it (`reviewCreate`); the submit of a review (`…/reviews/<id>/events`), answered
// from the review's own state (`reviewSubmission`); and the read of one review this run created
// (`reviewRead`). A route that names an `answer` is answered from what the run did — the reviews
// it created and their inline comments, the comments it posted, a ref or a commit's combined
// status read on its remote, a commit's check runs reported on that commit, and a file or a
// directory at a ref of its remote (`fromRun`) — and a route that holds a `graph` answers GraphQL
// from it, paged as GitHub pages a connection (`graphAnswer`, with gh-routes.ts's parser and
// executor). The pull request's review threads are GitHub's state and so the run's: the world's
// threads, then the effect of every call the record holds on them — the threads a review's
// inline comments or a comment of its own open, each reply, by GraphQL or REST, and each
// resolution (gh-threads.ts's `threadState`) — and every reader of a thread or of a review
// comment, GraphQL or REST, reads that one state, while a write on a thread is answered from it
// (`threadCall`). A
// REST read's list is paged by `per_page` and `page`, and `--paginate` asks for every page, as gh
// does (`restPages`). A call no route matches and no answer of this file's own serves prints
// `gh: Not Found (HTTP 404)` and exits 1, and so does a GraphQL mutation the world's graph holds
// no answer for. Nothing is sent anywhere.
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
  FILE_FIELD_FLAGS,
  flagValue,
  isPost,
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
} from "./gh-routes";
// The pull request's review threads as the run has left them, and the merge state they leave it
// in: one replay of the record for this stand-in and the daemon stand-in.
import {
  commentsOf,
  graphqlOf,
  heldPullRequest,
  mergeStateOf,
  NOT_FOUND,
  recordedCalls,
  restComment,
  routeAnswer,
  type ThreadWorld,
  threadCall,
  threadState,
  worldLogin,
} from "./gh-threads";
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

// The run's directory, named in a variable of gh's own shape (its GH_CONFIG_DIR) rather than one
// naming the rig, since the bundle a run's PATH resolves `gh` to sets it in its first line and an
// agent can read that file. The words this push removes from everything an agent reads
// (run-names.ts's REMOVED_NAMES, and this file's own) are in neither the variable nor a refusal.
const run = process.env.GH_DATA_DIR;
if (!run) {
  console.error("gh: GH_DATA_DIR is unset");
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
const world = existsSync(`${run}/world.json`) ? worldOf(run, "gh") : undefined;

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
const graphqlCall =
  as === "gh" && args[0] === "api" && endpointOf(args) === "graphql"
    ? graphqlOf({ argv: args, files, stdin })
    : undefined;
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
/** Records the call with the status it exits with, then exits. `by` is the role the call's pane
 * names (LEGION_ROLE, as production names a pane's role), so a run that boots one agent per role
 * has each call say which role made it; a pane that names none records no `by`. A call that left
 * a review records it (`answered`) whatever its status: a submit answered 200 whose `--jq` then
 * failed exits 1, and GitHub holds the review all the same. */
function exit(status: number): never {
  const by = process.env.LEGION_ROLE || undefined;
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
/** What `git -C <run>/remote.git <args...>` writes to its stdout, byte for byte, or undefined
 * when the command fails: the read of a file's own bytes, whose trailing newline is the file's. */
function remoteBytes(...args: string[]): Buffer | undefined {
  const read = Bun.spawnSync(["git", "-C", `${run}/remote.git`, ...args]);
  return read.exitCode === 0 ? Buffer.from(read.stdout) : undefined;
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
 * the body it carries and when it was made. Its inline comments are threads of the pull request
 * (`threadState`). */
type OwnReview = {
  answer: Record<string, unknown>;
  id: string;
  state: string;
  head: string;
  body: string;
  at: string;
};
/** Every review this run created on `repo`'s pull request, by the id it was answered with, and
 * the one it has pending, or `undefined` when it has none, read off the run's record
 * (`recordedCalls`).
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
  for (const call of recordedCalls(RECORD)) {
    const { argv } = call;
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
    const answer = routeAnswer(routes, call.route);
    const id = answer?.id;
    if (answer === undefined || (typeof id !== "number" && typeof id !== "string")) continue;
    const review = {
      answer,
      id: String(id),
      ...call.review,
      body: text ?? "",
      at: call.at ?? "",
    };
    byId.set(review.id, review);
    if (review.state === "PENDING") pending = review;
  }
  return { byId, pending };
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
/** The world the threads are read against, and the pull request's review threads as this call
 * finds them: the world's, and every call the record holds. */
const threadWorld: ThreadWorld = { routes, world };
const threads = threadState(threadWorld, recordedCalls(RECORD));
/** What GitHub answered this call where it writes a thread, applied to `threads`: a refusal here
 * is GitHub's, and the REST comment it made, or the payloads of its mutation, its answer. */
const threaded = threadCall(threadWorld, threads, {
  at,
  as,
  argv: args,
  files,
  stdin,
  route: route?.match ?? null,
});
if (threaded !== undefined && "refused" in threaded) refuse(threaded.refused);
const submitted = reviewSubmission();
if (submitted !== undefined)
  answered = { state: String(submitted.state), head: String(submitted.commit_id) };
const created = reviewCreate();
/** This call's answer where it is this stand-in's own and no route's: the submit of the pending
 * review, the read of a review this run created, and a REST comment the run made on a thread.
 * Each stands over whatever route the call matched. */
const own =
  submitted ??
  reviewRead() ??
  (threaded !== undefined && "rest" in threaded ? threaded.rest : undefined);
/** A check run as GitHub's check-runs route reports it on the commit it ran on: the world's run —
 * its name, status, conclusion, the link of the app that ran it (`details_url`) and how long it
 * ran — with the ids GitHub gives it, the commit it ran on (`head_sha`), GitHub's own page of it
 * (`html_url`) and the times it ran, counted from when that commit reached the run's remote. */
interface ReportedRun extends CheckRun {
  readonly id: number;
  readonly head_sha: string;
  readonly details_url: string;
  readonly [field: string]: unknown;
}
/** How long GitHub takes from a push to the start of the first check run on it, which this world
 * fixes so every head reads alike. */
const QUEUED_MS = 3000;
/** The App GitHub reports a workflow's check runs as made by, GitHub Actions, as its check-runs
 * route names it: no name of this world's own. */
const GITHUB_ACTIONS = {
  id: 15368,
  slug: "github-actions",
  node_id: "MDM6QXBwMTUzNjg=",
  name: "GitHub Actions",
  owner: { login: "github", id: 9919, type: "Organization" },
};
/** When the commit `sha` reached the run's remote, in milliseconds: the first push of it the
 * remote's post-receive hook logged (`<run>/pushes.log`, `<at> <ref> <old> <new>`, which holds
 * the run's own pushes), or, for a commit the world was built with, its committer date. */
function reachedAt(sha: string): number | undefined {
  const log = existsSync(`${run}/pushes.log`) ? readFileSync(`${run}/pushes.log`, "utf8") : "";
  const pushed = log
    .split("\n")
    .map((line) => line.split(" "))
    .find((words) => words[3] === sha)?.[0];
  const parsed = Date.parse(pushed ?? fromRemote("show", "-s", "--format=%cI", sha) ?? "");
  return Number.isNaN(parsed) ? undefined : parsed;
}
/** The world's check runs `runs`, reported on the commit `sha` as GitHub reports a check run
 * (`ReportedRun`). The world's times say how long each ran and in what order, and GitHub's are
 * those runs moved to start once the commit reached the remote and the queue let them: a world
 * whose every head reported checks run on 2026-01-01, ten months before the commit they tested,
 * left a reviewer asking whether this world's CI ran at all (INFRA-764 round 24,
 * reviewer-text-findings-text-only-base-6). The ids are the commit's own, so a head reads the same
 * runs at every read. */
function reportedRuns(runs: readonly CheckRun[], sha: string): ReportedRun[] {
  const repo = world?.repo ?? "";
  const reached = reachedAt(sha);
  const starts = runs
    .map((one) => Date.parse(one.started_at ?? ""))
    .filter((time) => !Number.isNaN(time));
  const origin = starts.length === 0 ? undefined : Math.min(...starts);
  const moved = (time: string | undefined) => {
    const parsed = Date.parse(time ?? "");
    if (reached === undefined || origin === undefined || Number.isNaN(parsed)) return time;
    return new Date(reached + QUEUED_MS + parsed - origin).toISOString().replace(/\.\d{3}Z$/, "Z");
  };
  const seed = Number.parseInt(sha.slice(0, 8), 16) || 0;
  return runs.map((one, index) => {
    const id = 40_000_000_000 + seed * 8 + index;
    const page = `https://github.com/${repo}/runs/${id}`;
    const api = `https://api.github.com/repos/${repo}/check-runs/${id}`;
    return {
      ...one,
      id,
      node_id: `CR_kwDOAAAAAc8AAA${id}`,
      head_sha: sha,
      external_id: "",
      url: api,
      html_url: page,
      details_url:
        "details_url" in one && typeof one.details_url === "string"
          ? one.details_url
          : (one.html_url ?? page),
      started_at: moved(one.started_at),
      completed_at: moved(one.completed_at),
      output: {
        title: null,
        summary: null,
        text: null,
        annotations_count: 0,
        annotations_url: `${api}/annotations`,
      },
      check_suite: { id: 30_000_000_000 + seed },
      app: GITHUB_ACTIONS,
    };
  });
}
/** The check runs the world reports on the pull request's head as it stands, read at the call
 * from the run's own remote (`branchTip`) and the world's check-runs answer for that head
 * (`checkRunsOn`), reported as GitHub reports them (`reportedRuns`), or undefined for a run with
 * no world or no remote of its own. Every answer that reports the head's checks is built from
 * these, so a world that has a check still running on one head and none on the next — the push
 * that set an approval aside, then the head its checks concluded on — tells `gh pr checks`, the
 * pull request's own `statusCheckRollup` and the check-runs route one thing. */
function headRuns(): ReportedRun[] | undefined {
  const repo = world?.repo;
  const tip = branchTip();
  return repo === undefined || tip === undefined
    ? undefined
    : reportedRuns(checkRunsOn(routes, repo, tip), tip);
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
    return { bucket, line: `${run.name}\t${bucket}\t${seconds}s\t${run.details_url}` };
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
/** The comments this run posted on the pull request's conversation, oldest first: each
 * `gh pr comment` of the world's repository and each POST of `repos/<repo>/issues/<n>/comments`
 * the record holds exiting 0, with the body it posted and when it posted it. */
function conversation(): { body: string; at: string }[] {
  if (world === undefined) return [];
  const path = `repos/${world.repo}/issues/${world.pr}/comments`;
  const posted: { body: string; at: string }[] = [];
  for (const call of recordedCalls(RECORD)) {
    const { argv } = call;
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
  const url = String(heldPullRequest(routes).url ?? "");
  return {
    id,
    node_id: `IC_kwDOAAAAAc4AAB${id}`,
    html_url: `${url}#issuecomment-${id}`,
    issue_url: `https://api.github.com/repos/${world?.repo}/issues/${world?.pr}`,
    user: { login: worldLogin(routes), type: "Bot" },
    body: comment.body,
    created_at: comment.at,
    updated_at: comment.at,
    author_association: "NONE",
  };
}
/** Whether the call asks for a file's own bytes rather than the JSON that carries them: `gh api -H
 * "Accept: application/vnd.github.raw"`, GitHub's raw media type, in its versioned spelling
 * `vnd.github.v3.raw` and as `vnd.github.raw+json` too. */
const RAW_MEDIA_TYPE = /application\/vnd\.github(?:\.v3)?\.raw/;
/** Whether this call's answer is a file's own bytes, which gh prints as GitHub sent them: no
 * newline is added to a file that ends without one. */
let verbatim = false;
/** GitHub's `GET repos/<o>/<r>/contents/<path>`, read on the run's own remote at the call: the
 * ref the query string names (`?ref=`, or a `-f ref=` gh sends there on a GET), the base branch
 * where it names none, resolved to a commit, and the path at that commit. A file is its own
 * bytes under the raw media type (RAW_MEDIA_TYPE) and otherwise GitHub's JSON of it, its content
 * base64 in lines of 60 as GitHub writes it, its `sha` the blob's; a directory is the JSON list of
 * what it holds. A ref the remote cannot resolve is GitHub's 404 "No commit found for the ref", and
 * a path the commit does not hold is GitHub's 404. No route freezes a file: the head a reviewer
 * reads a fixed file at is the implementer's push, and measured in INFRA-764 round 24 the
 * reviewer of threads-closed-text-fixes-head-3 read `gh api
 * repos/example/widgets/contents/greet.ts?ref=<head> -H 'Accept: application/vnd.github.raw'`,
 * was refused 404 by a world that answered no contents read, and wrote that the 404s were the
 * sandbox's; the run was void and replaced, 2 of the 7 launches of that arm. */
function contentsAt(endpoint: string): unknown {
  const repo = world?.repo ?? "";
  let file = "";
  try {
    file = (/^repos\/[^/]+\/[^/]+\/contents(?:\/(.*))?$/.exec(endpoint)?.[1] ?? "")
      .split("/")
      .filter((segment) => segment !== "")
      .map((segment) => decodeURIComponent(segment))
      .join("/");
  } catch {
    refuse(NOT_FOUND);
  }
  const query = new URLSearchParams((endpointOf(args) ?? "").split("?")[1] ?? "");
  const fields = sentBody({ argv: args, stdin, files }) ?? {};
  const ref =
    query.get("ref") ?? (typeof fields.ref === "string" ? fields.ref : (world?.base ?? ""));
  const sha = REF.test(ref)
    ? fromRemote("rev-parse", "--verify", "--quiet", `${ref}^{commit}`)
    : undefined;
  if (sha === undefined || sha === "")
    refuse(`gh: No commit found for the ref ${ref} (HTTP 404)\n`);
  const object = file === "" ? `${sha}^{tree}` : `${sha}:${file}`;
  const kind = fromRemote("cat-file", "-t", object);
  const entry = (path: string, type: string, id: string, size: number) => {
    const encoded = path.split("/").map(encodeURIComponent).join("/");
    const self = `https://api.github.com/repos/${repo}/contents/${encoded}?ref=${ref}`;
    const html = `https://github.com/${repo}/${type === "dir" ? "tree" : "blob"}/${ref}/${encoded}`;
    const git = `https://api.github.com/repos/${repo}/git/${type === "dir" ? "trees" : "blobs"}/${id}`;
    return {
      name: path.split("/").pop() ?? path,
      path,
      sha: id,
      size,
      url: self,
      html_url: html,
      git_url: git,
      download_url:
        type === "file" ? `https://raw.githubusercontent.com/${repo}/${ref}/${encoded}` : null,
      type,
      _links: { self, git, html },
    };
  };
  if (kind === "blob") {
    const bytes = remoteBytes("cat-file", "blob", object) ?? Buffer.alloc(0);
    if (RAW_MEDIA_TYPE.test(joined)) {
      verbatim = true;
      return bytes.toString();
    }
    const { _links, ...described } = entry(
      file,
      "file",
      fromRemote("rev-parse", object) ?? "",
      bytes.length
    );
    const lines = bytes.toString("base64").match(/.{1,60}/g) ?? [];
    return {
      ...described,
      content: lines.map((line) => `${line}\n`).join(""),
      encoding: "base64",
      _links,
    };
  }
  if (kind !== "tree") refuse(NOT_FOUND);
  return (fromRemote("ls-tree", "-l", "-z", object) ?? "").split("\0").flatMap((line) => {
    const [, mode = "", type = "", id = "", size = "", name = ""] =
      /^(\d+) (\w+) ([0-9a-f]+) +(\S+)\t(.*)$/s.exec(line) ?? [];
    if (name === "") return [];
    const listed =
      type === "tree"
        ? "dir"
        : type === "commit"
          ? "submodule"
          : mode === "120000"
            ? "symlink"
            : "file";
    return [entry(file === "" ? name : `${file}/${name}`, listed, id, Number(size) || 0)];
  });
}
/** A route's answer read from what the run did (rig.sh's fixture_write names which, as `answer`),
 * with `held` the route's own `stdout`, what the world held before the run:
 *   - `reviews`: the world's reviews, then each review this run created as it stands;
 *   - `review`: the world's review the path names by id, or GitHub's 404 (a review this run
 *     created is `reviewRead`'s, read before the routes);
 *   - `review-comments`: every comment of the pull request's review threads as they stand
 *     (`threads`), or the ones of the review the path names, which is 404 where neither the world
 *     nor the run holds that review;
 *   - `issue-comments`: the world's conversation, then each comment the run posted on it;
 *   - `combined-status`: GitHub's combined status of the ref the path names, resolved on the run's
 *     own remote — the world's statuses, `state` `pending` where there are none, and 422 for a ref
 *     the remote has not got;
 *   - `git-ref`: the ref the path names on the run's own remote, or GitHub's 404;
 *   - `check-runs`: the world's check runs of the commit the path names, resolved on the run's own
 *     remote and reported on it as GitHub reports them (`reportedRuns`), and 422 for a ref the
 *     remote has not got;
 *   - `contents`: the file or the directory the path names at the ref the call names, on the run's
 *     own remote (`contentsAt`). */
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
      const listed = threads.list
        .flatMap((thread) =>
          commentsOf(thread).map((comment) => restComment(world, thread, comment))
        )
        .sort((one, other) => Number(one.id) - Number(other.id));
      if (id === undefined) return listed;
      const known =
        (world !== undefined && ownReviews(repo).byId.has(id)) ||
        reviewsHeld().some((review) => isObject(review) && String(review.id) === id);
      if (!known) refuse(NOT_FOUND);
      return listed.filter((comment) => String(comment.pull_request_review_id) === id);
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
    case "check-runs": {
      const ref = /\/commits\/([^/]+)\/check-runs$/.exec(path)?.[1] ?? "";
      const sha = REF.test(ref)
        ? fromRemote("rev-parse", "--verify", "--quiet", `${ref}^{commit}`)
        : undefined;
      if (sha === undefined || sha === "")
        refuse(`gh: No commit found for SHA: ${ref} (HTTP 422)\n`);
      const runs = reportedRuns(checkRunsOn(routes, repo, sha), sha);
      return { total_count: runs.length, check_runs: runs };
    }
    case "contents":
      return contentsAt(path);
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
  runs: ReportedRun[] | undefined
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
            __typename: "CheckRun",
            name: run.name,
            status: run.status.toUpperCase(),
            conclusion: (run.conclusion ?? "").toUpperCase(),
            startedAt: run.started_at ?? null,
            completedAt: run.completed_at ?? null,
            detailsUrl: run.details_url,
            workflowName: "",
          })),
        }
      : {};
  const state = mergeStateOf(threadWorld, threads, fixtures.mergeStates, tip);
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
 * stands (`current`) and whose review threads are the threads as they stand (`threads`); a
 * mutation from its `mutation` root, each field of it on its own, a field that writes a thread
 * answered with the payload of that write (`threaded`). With `--paginate` and a query that
 * takes `$endCursor`, gh asks again with the `endCursor` of the page it was answered until that
 * page has no next one, and so does this: each page is one answer, and a world that answered
 * every page alike would hold a `--paginate` loop for ever. */
function graphAnswer(
  graph: Record<string, unknown>,
  call: { operation: Operation | undefined; variables: Record<string, unknown> }
): GraphAnswer[] {
  const { operation } = call;
  if (operation === undefined) return [{ errors: [{ message: PARSE_ERROR }] }];
  if (operation.type !== "query") {
    const given = isObject(graph.mutation) ? graph.mutation : {};
    const own = threaded !== undefined && "fields" in threaded ? threaded.fields : [];
    const data: Record<string, unknown> = {};
    const errors: NonNullable<GraphAnswer["errors"]>[number][] = [];
    const fields = operation.selections.flatMap((one) => (one.kind === "field" ? [one] : []));
    for (const [index, selection] of fields.entries()) {
      const alias = selection.alias ?? selection.name;
      const written = own[index]?.alias === alias ? own[index] : undefined;
      if (written?.error !== undefined) errors.push(written.error);
      const payload = written?.payload === undefined ? given[selection.name] : written.payload;
      const answer = executeGraphql(
        { ...operation, selections: [selection] },
        { [selection.name]: payload },
        call.variables
      );
      if (answer.data === undefined || answer.data === null) return [answer];
      Object.assign(data, answer.data);
      errors.push(...(answer.errors ?? []));
    }
    return [errors.length === 0 ? { data } : { data, errors }];
  }
  const query = isObject(graph.query) ? graph.query : {};
  const repository = isObject(query.repository) ? query.repository : {};
  const pullRequest = isObject(repository.pullRequest) ? repository.pullRequest : {};
  const now = current(heldPullRequest(routes), branchTip(), headRuns());
  const root = {
    ...query,
    repository: {
      ...repository,
      pullRequest: { ...now, ...pullRequest, reviewThreads: threads.list },
    },
  };
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
if (text !== "")
  process.stdout.write(
    verbatim && filter === undefined ? text : text.endsWith("\n") ? text : `${text}\n`
  );
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
