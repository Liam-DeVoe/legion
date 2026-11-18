#!/usr/bin/env bun
/**
 * A stand-in for the Legion daemon that serves exactly the routes a phase worker's `legion` tool
 * and plugin hit in a skill scenario, of those the run's label's daemon registers (`ROUTES`): the
 * worker's claim registration (`claims/register`, `claims/ready`, the daemon's claim routes the
 * plugin boots through), the session's activity report (`claims/activity`, which the plugin posts
 * after each turn and the daemon answers 204 once the claim's secret authenticates it, as
 * `claims/ready` is: `s.activity` in packages/daemon/internal/api/claims.go), the outcome of each
 * turn (`claims/turn-outcome`, which this checkout's plugin posts after each turn: `turnOutcome`
 * there, answered with the run of refused turns, `TURN_OUTCOMES`), the idle-release records
 * (`claims/release-records`, the tool's `read_release_records`: `releaseRecords` there, answered
 * with none, since no pod of the rig is ever released), the grant the tool mints in-process before
 * each of its own daemon calls (`grants`), the issue's record (`GET /legion/v1/state`, the tool's
 * `read_record`), the phase completion (`handoff/complete`, the tool's `handoff_complete`) and
 * the return of the issue to an earlier phase (`phase/backward`, the tool's
 * `request_backward_move`), the two routes here that redeem a grant. No credential route and no
 * grant file: a pane's `gh` and
 * `git` read the rig's token from the gh files under its `GH_CONFIG_DIR`, which `daemon-pane.go`
 * writes, and no agent runs `legion` from bash. A route the label's daemon does not register is
 * answered 404 whatever this has a handler for, as that daemon's catch-all answers it; a route
 * it registers and this does not answer is answered 404 too, which `rig.sh health` reads as a
 * world the daemon would not have served (health.ts's `routes` rule).
 *
 * Every request and response body is the Go daemon's (`@legion/contracts/legion-api`): the
 * tool's grant request, its completion, and the state document. The grant rule mirrors the real
 * daemon's: a grant lives for 60 seconds and redeems any number of times while it lives; an
 * unknown or expired grant id answers 403 `{"error":"Invalid or expired grant"}`. Nothing is
 * single-use. A completion is answered as the daemon of the run's label's checkout answers it
 * (`LATE_ROLE` and `RECORD_ROUTE`, read at boot out of the checkout the run's built-from names:
 * daemon-pane.ts's `labelDaemon`, never by the label's name), on the phase the issue stands in
 * now (`served()`), never on the phase the role was booted in. First the label's route
 * (`packages/daemon/internal/api/handoff.go`, completion-answers.ts's `RecordRoute`), which
 * decides whether the completion is record-backed. This checkout's shape (`left`): `of :=
 * phase.Of(grant.Role, issue.Phase); word, writer, handoffPhase := phase.Handoff(of);
 * recordBacked := handoffPhase && writer == grant.Role && (of == issue.Phase || namesRecord)`.
 * An older daemon's (`issue`, main `ff2a10ef`'s among them): `phase.Handoff(issue.Phase)` and
 * `recordBacked := handoffPhase && writer == grant.Role`. A record-backed completion naming no
 * record (`handoffId` and a positive `handoffVersion`) is refused 400 HANDOFF_RECORD_REQUIRED,
 * and one naming a record is read back from the run's own Dispatch (`handoffRecord`, as
 * handoff.go's `handoffRecord` reads it: `GET /api/v1/handoffs/{id}`) and refused where that
 * daemon refuses it: 404 HANDOFF_RECORD_NOT_FOUND for a record Dispatch has not got, 409
 * HANDOFF_RECORD_MISMATCH for one of another issue, phase or version, 502 DISPATCH_READ_FAILED
 * where Dispatch does not answer, and 500 DISPATCH_UNAVAILABLE for a run that names no Dispatch
 * (DISPATCH_URL), and 403 HANDOFF_SESSION_MISMATCH for one another session than the claim's
 * wrote, the claim's being the one its grant was minted for (`handoffRecord` says what was
 * measured: every record an agent of round 25 wrote names the session that agent registered).
 * This took every named record as found, a success production does not give a completion
 * naming a record that is not there; a
 * completion naming one of a phase that ends with none is refused 409 HANDOFF_RECORD_MISMATCH.
 * Then the fact: a completion is one fact by its role, the phase the issue stands in, the
 * implementer's pass, what it reported and its verdict (handoff.go's `eventID`), marked
 * processed before the workflow reads it, so a call repeating one the workflow was handed,
 * taken or refused, is refused 409 HANDOFF_ALREADY_RECORDED (`FACTS`), the completions the
 * world's seeds stand for among them.
 * Then the workflow (`packages/daemon/internal/workflow/engine.go`, `handoff`): a completion from
 * the role whose phase the issue stands in is taken `{}` and moves nothing, since the record
 * moves on the review, as main's engine moves it (`servedPhase`). A completion from any other
 * role (a late role) is answered by the label's engine (completion-answers.ts's `LateRole`):
 *   - this checkout's `leftItsPhase` (`recorded`) takes it, `{}` and moving nothing, when the
 *     daemon started the role on the issue and the role's row holds no completion
 *     (`row.Completed()`). The completion is then one of the phase the role left (`phase.Of`),
 *     whose record it must name, or it is refused 409 HANDOFF_RECORD_REQUIRED
 *     (`phase.Handoff(reported)`, through `refused`). A role whose row holds a completion is
 *     refused 409 HANDOFF_NOT_CURRENT_PHASE. That holds for each role the world's seeds record
 *     a round of (`HISTORY`: threads-closed's implementer and its reviewer, whose seeded
 *     records are on their rows), for the roles the rig says completed with a commit (the
 *     [completed] argument), and for a role whose completion was taken here, since the engine
 *     writes the completion's commit and record on the row (`row.HandoffCommit`,
 *     `row.HandoffRecord`; `completed`). It read the [completed] roles alone, and took a
 *     threads-closed reviewer's first late completion, which production refuses.
 *     Every rig half is a worker the daemon started, so the refusal of a role the daemon never
 *     started is not modelled;
 *   - an older engine (`refused`, main `ff2a10ef`'s among them) has no `leftItsPhase` and refuses
 *     every late completion 409 HANDOFF_NOT_CURRENT_PHASE (`if RoleFor(issue.Phase) !=
 *     fact.Role`).
 * A completion the engine takes that names the record its role's row last named is refused 409
 * HANDOFF_NOT_NEW (`LAST_HANDOFF`). Each of HANDOFF_SESSION_MISMATCH, HANDOFF_ALREADY_RECORDED
 * and HANDOFF_NOT_NEW is answered only where the label's own daemon source holds the code
 * (`daemonAnswers`). The route's rules on a completion's own fields are not modelled: a tester's
 * verdict (TESTER_VERDICT_REQUIRED), a verdict from any other role (VERDICT_ROLE_FORBIDDEN), an
 * implementer's or a merger's commit (MISSING_FIELD) and a `wakeAt` (CHECK_WAKE_UNEXPECTED).
 * The phase a completion is of (`phase.Of`) is the served phase when the role works it and
 * otherwise the role's own, its first (below): the implementer's would be retro once the issue
 * stands in merging, where no run of this rig stands. The route's entry fence (`entryFence`:
 * HANDOFF_STALE_ENTRY, or HANDOFF_NOT_CURRENT_PHASE in another role's phase, for a completion of
 * a phase entry its role's row has moved past, decided by `behindEntryClause` on the head the
 * record names) is not modelled: it compares two entries, the one the claim serves and the one
 * the role's row names, and those differ only once the issue has entered the role's phase again
 * while its worker was on the earlier entry. No run of this rig enters a phase again: the issue
 * leaves `reviewing` at most once and stays where it moved (`servedPhase`), and every other
 * phase stands still, so a rig half's completion is always of the entry its row names.
 * The NOT_CURRENT_PHASE code and message are the daemon's own, read from its code at boot
 * (`NOT_CURRENT_PHASE`), because the skill has the worker quote them to its architect. The real
 * daemon's earlier fence, the 409 `HANDOFF_NO_RUN` of a completion it can attribute to
 * no run (`internal/api/handoff.go`, `noRunCompletion`), is not the one a rig half meets: both
 * halves are live registered workers serving generation 1, so the route finds a run and the
 * refusal is the workflow's. A role's phase is its first (daemon-pane.go's `phase <role>`),
 * which is narrower than the daemon, whose implementer also runs retro; no run of this rig
 * stands in retro. The body may carry every key any
 * label's contract sends (`CompleteRequest`). A backward move is the workflow's, as
 * engine.go's `backward` and `backwardRefusal` take it: it names no issue but this one
 * (`ISSUE_NOT_GRANTS`, 400, the daemon's refusal of a worker that moves another issue), and is
 * refused 409 BACKWARD_REFUSED, in the engine's own sentence, for a phase no backward row of the
 * issue's phase serves — any but an earlier one of the workflow's line (phase.go's `Order`) —
 * and for a role that does not run the issue's phase (phase.go's `RoleFor`). Taken, it answers
 * `{}` and moves the issue to that phase, clearing the completion of the role the move starts
 * there (engine.go's `transition` and `clearHandoff`), so every later read of the record and
 * every later completion read the phase it moved to; it answered `{}` for any `to` and moved
 * nothing, a success the daemon gives no move it refuses. The tree's linger (`TREE_LINGERING`)
 * is not modelled, since no tree of the rig leaves the workflow.
 *
 * The state document's record of the issue is production's (standin-record.ts, which says what
 * each member is): the tree's architect and a row for each role the world's history ran and
 * each agent the run booted, read back from the run's own Dispatch by the seeds <run
 * dir>/fixture.json names (`HISTORY`) and off the run's log of the stand-ins before this one;
 * this stand-in's own claim as its agent registers, readies and ends its turn (`OWN`); the pull
 * request as the world's gh answers it at the read (`worldPull`); the design gate as the run's
 * Dispatch holds the spec at the read (`gateNow`); and the slot, the entries and the counts of
 * the moves. Every move of the record — the round's, a backward move — is one `enter`: the
 * phase, the implementer's pass, pair and round, the start of the role that works the phase,
 * and the status that phase shows, written to the run's Dispatch as the daemon's own session
 * before the request is answered (`writeStatuses`).
 *
 * Every request appends one JSON line to the log file: `{at, method, path, status, role, code?,
 * grantId?, sessionId?, mintedGrantId?, handoffId?, handoffVersion?, to?, reason?, phase?,
 * failed?}`, the run's
 * record of what reached the daemon, each line naming the role this stand-in serves, which is
 * what tells one half's completion from the other's in the one log a run keeps
 * (completion-answers.ts); a refusal's line carries its code where the answer has one, a
 * completion's line carries the record it named and the phase it was answered on (`served()`
 * then), and a backward move's the phase it
 * asked for and why (score.ts threadsClosed). The record opens with one line of its own,
 * `{at, opened}`, written when this starts listening, as record-publishes.ts writes its
 * `recording` line: a log holding that line alone is a run whose agent never reached the
 * daemon, which is a reading of the run, where a log that is not there at all is one nothing
 * ever wrote, which is a rig error (score.ts's `unscored`). No score reads the line itself.
 *
 * Usage: `bun daemon-standin.ts <port> <log file> <boot token file> <project> <issue> <run dir>
 * <role> [phase] [completed]`. The daemon whose routes it answers is the one of the checkout
 * <run dir>/built-from names (`ROUTES`). The run's own Dispatch, which the world's history, the
 * spec's approval and a completion's handoff record are read back from and a move's status is
 * written to, is the one DISPATCH_URL names, reached with the bearer in the file
 * DISPATCH_TOKEN_FILE names (rig.sh's worker_boot gives both, the ones the run's panes read).
 * The state document holds that one issue, on the repository and default branch of the run's own
 * world, which it reads from <run dir>/world.json, as the gh stand-in does: a record naming
 * another repository sends the agent's review to one the world has not got, and a record naming
 * another branch sends its work onto a branch the world has not got (rig.sh's worker world
 * comment). The issue stands in [phase], or,
 * where the caller names none, in the first phase its role works (daemon-pane.go's `phase
 * <role>`): the implementer works implementing and retro, and the daemon starts it in either.
 * [completed] is the roles, comma-separated, whose row the run's world gives a handoff commit:
 * each completed its phase before the run began (`COMPLETED`). A run directory holding a
 * fixture.json is a run with seeds: each record it names is read back from the run's Dispatch
 * at the boot, and a boot that cannot read one exits 2 naming it.
 * An issue in `reviewing` moves on what the same run directory reports: its remote's tip, the
 * reviews of its world and of its gh record, and the merge state its world reports for that tip
 * (`servedPhase`).
 * Prints `listening on http://127.0.0.1:<port>` once the socket is open (port 0: the kernel's
 * pick).
 */
import { randomUUID } from "node:crypto";
import { appendFileSync, existsSync, readFileSync } from "node:fs";
import { appendFile } from "node:fs/promises";
import { isLegionRole, roleToken } from "@legion/contracts";
import {
  LegionGrantRequest,
  LegionHandoffCompleteRequest,
  LegionPhaseBackwardRequest,
  LegionReleaseRecordsRequest,
  LegionStateResponse,
  LegionTurnOutcomeRequest,
} from "@legion/contracts/legion-api";
import { z } from "zod";
import { COMPLETE_PATH } from "./completion-answers";
import {
  ISSUE_SLOT,
  labelCheckout,
  PHASE_SLOT,
  RECORD_PHASES,
  serves,
  standinFacts,
} from "./daemon-pane";
import { worldOf } from "./gh-call";
import {
  DECIDES,
  heldPullRequest,
  mergeStateOf,
  recordedCalls,
  submittedReviews,
  threadState,
} from "./gh-threads";
import { CALLS, Opened, openingLine } from "./run-records";
import {
  type CapabilityFacts,
  type GateFacts,
  type PullRequestFacts,
  phaseStatus,
  type RowFacts,
  stateBody,
} from "./standin-record";

const GRANT_TTL_MS = 60_000;
const [
  portArg,
  logFile,
  bootTokenFile,
  project,
  issue,
  runDir,
  role,
  phaseArg = "",
  completedArg = "",
] = Bun.argv.slice(2);
if (
  portArg === undefined ||
  logFile === undefined ||
  bootTokenFile === undefined ||
  project === undefined ||
  issue === undefined ||
  runDir === undefined ||
  role === undefined
) {
  console.error(
    "usage: bun daemon-standin.ts <port> <log file> <boot token file> <project> <issue> <run dir> <role> [phase] [completed]"
  );
  process.exit(2);
}
if (!isLegionRole(role)) {
  console.error(`${role} is not a Legion role`);
  process.exit(2);
}
/** The roles the run's world says completed their phase with a commit before the run began
 * (rig.sh's worker_boot COMPLETED): each one's row holds that commit (`HANDOFFS`), and a row
 * that holds a completion is one a `recorded` engine refuses a late completion from
 * (`completed`). */
const COMPLETED = completedArg.split(",").filter((named) => named !== "");
for (const named of COMPLETED) {
  if (!isLegionRole(named)) {
    console.error(`daemon-standin: [completed] names ${named}, which is not a Legion role`);
    process.exit(2);
  }
}
/** The run's world, read through the one reader every program of the run reads it with
 * (gh-call.ts's `worldOf`): this stand-in answers a `read_record` out of it, the record's
 * `repo` and `defaultBranch` being the repository the world is on and the branch its pull
 * request is open against; and it reads the round's outcome off the issue branch of the run's
 * own remote, whose tip is the pull request's head, and the pull request's number, whose
 * reviews the world's routes answer. */
const world = worldOf(runDir, "daemon-standin");
const { repo, base, branch: BRANCH, pr: PR } = world;
/** The owner of the run's repository, which is the one owner the project maps. */
const [REPO_OWNER = ""] = repo.split("/");
if (REPO_OWNER === "" || !repo.includes("/")) {
  console.error(`${repo} is not an <owner>/<name> repository`);
  process.exit(2);
}
/** Encoded exactly as the daemon encodes it (`legion-<project>-<key>-<role>`, lower-cased),
 * since the worker claims this token on the real Envoy and Envoy rejects uppercase. */
const ROLE_TOKEN = roleToken(project, issue, role);
/** The session the daemon writes every Dispatch status of the issue under, and the author of a
 * handoff record it carried: `legion-daemon:<PROJECT>`, PROJECT being the key's
 * (packages/daemon/internal/dispatch/client.go's `DaemonSession`). */
const DAEMON_SESSION = `legion-daemon:${issue.split("-")[0]}`;
/** The secret a registration answers, in the shape the daemon mints one (Go's `rand.Text()`, 26
 * letters and digits of base32: packages/daemon/internal/api/claims.go): a word of the rig's
 * (`rig-secret`) was what the agent's plugin held. Fixed, since daemon-standin.test.ts sends it. */
const SESSION_SECRET = "M4QZ7KWT2HXR5NBJ6YCPVD3LGE";
/** A fact of the run's label's daemon read at boot, or the boot failed with why it could not be
 * read: a run whose daemon cannot be read is never answered as some other daemon would answer
 * it. */
function bootRead<T>(read: () => T): T {
  try {
    return read();
  } catch (error) {
    console.error(`daemon-standin: ${error instanceof Error ? error.message : String(error)}`);
    process.exit(2);
  }
}
/** The checkout the run's label was built from (the run's built-from, daemon-pane.ts's
 * `labelCheckout`), whose daemon every fact below is read out of. */
const CHECKOUT = bootRead(() => labelCheckout(runDir));
/** Every fact of the run's label's daemon this answers by, read at boot by daemon-pane.ts's
 * `standinFacts`, which says what each is: its routes (`ROUTES`, the one answer to "does this
 * daemon serve that route", which `fetch` asks of every request), how it takes a completion
 * (`LATE_ROLE`, its engine's answer to a late role, and `RECORD_ROUTE`, the phase its route holds
 * a completion's record to, read out of the checkout's own source: completion-answers.ts's
 * `LabelDaemon`), its launch failure limit, its claim refusals, the phase the role's own worker is
 * started in and that phase's handoff word, the phase the issue stands in for this run (`PHASE`:
 * the caller's, or the role's own when it names none), and, for a run whose role can be late, the
 * daemon's own refusal of a late completion (`NOT_CURRENT_PHASE`), whose phase and issue are
 * filled in when it is sent. Each is read through daemon-pane.ts's facts cache, which rig.sh's
 * label_facts fills for every stand-in of a batch before any run starts, so a boot reads each out
 * of a file. A daemon whose completion shapes cannot be read fails the boot, rather than being
 * answered as some other daemon would answer it. */
const {
  routes: ROUTES,
  daemon: { lateRole: LATE_ROLE, recordRoute: RECORD_ROUTE },
  turnOutcomeLimit: TURN_OUTCOME_LIMIT,
  invalidSecret: INVALID_SECRET,
  invalidBootToken: INVALID_BOOT_TOKEN,
  staleGeneration: STALE_GENERATION,
  rolePhase: ROLE_PHASE,
  handoffWord: HANDOFF_WORD,
  phase: PHASE,
  notCurrentPhase: NOT_CURRENT_PHASE,
} = bootRead(() => standinFacts(CHECKOUT, role, phaseArg));
/** Whether the label's daemon gives the refusal `code` to a completion at all: its own source
 * of the completion route and of the workflow names it. HANDOFF_SESSION_MISMATCH,
 * HANDOFF_ALREADY_RECORDED and HANDOFF_NOT_NEW are this checkout's daemon's, each answered here
 * in its own sentence, and a label built from a daemon that holds none of one is never answered
 * it. */
const COMPLETION_SOURCE = bootRead(() =>
  ["api/handoff.go", "workflow/engine.go"]
    .map((file) => readFileSync(`${CHECKOUT}/packages/daemon/internal/${file}`, "utf8"))
    .join("\n")
);
function daemonAnswers(code: string): boolean {
  return COMPLETION_SOURCE.includes(`"${code}"`);
}
/** The answer of a claim refusal read at boot, in the daemon's shape (claim.Refusal marshals as
 * `{error}`). */
function refuse(refusal: { status: number; message: string }): Response {
  return json(refusal.status, { error: refusal.message });
}
/** The turn outcomes reported for this stand-in's one claim, by `<generation>:<sequence>`, each
 * whether the turn was refused: the daemon's window (supervise's `recordTurnOutcome`), where a
 * repeat of a key replaces it rather than adding one, kept in (generation, sequence) order and
 * trimmed to the newest `TURN_OUTCOME_LIMIT`. */
const TURN_OUTCOMES: { generation: number; sequence: number; refused: boolean }[] = [];
/** Whether the claim still holds the capability its registration's secret authenticates: until
 * a turn outcome fails the claim. The daemon fails it there (supervise's `fail`), and every write
 * of a failed claim clears its capability hash (`NormalizeClaim`, since `HoldsCapability` is
 * false for a failed claim), so each route that authenticates the secret (`authenticated` in
 * packages/daemon/internal/api/claims.go) answers it `InvalidSecret` from then on. */
let capabilityHeld = true;
/** Whether `secret` authenticates this claim, as the daemon's `authenticated` decides it. */
function authenticates(secret: string | undefined): boolean {
  return capabilityHeld && secret === SESSION_SECRET;
}

/** When this stand-in started: the daemon's boot in the state it serves, and the time of every
 * fact of the record that stood before any request reached it (`recordFacts`). */
const startedAt = new Date().toISOString();

/** The phase the stand-in serves now, which starts as the phase it was started for and moves as
 * main's engine moves it: `reviewRound` (`packages/daemon/internal/workflow/review.go`), and the
 * transitions its outcomes take (`workflow/table.go`). Only `reviewing` moves: the rig's run ends
 * with any other phase's completion, and no scenario reads the record after it.
 *
 * The review decides the round and the completion does not, so this moves on what the run's
 * world reports and never on a completion (`roundOutcome`, read at each state read): the head is
 * the tip of the run's own remote, the decision is the last `APPROVED` or `CHANGES_REQUESTED`
 * among the reviews the world held when the run began and those the run left, and GitHub's merge
 * state for the head is the one its base's rules leave it in with those reviews, the head's
 * checks and the run's review threads (gh-threads.ts's `mergeStateOf`, which the gh stand-in
 * answers `mergeStateStatus` with): a base that requires an approval holds it `BLOCKED` until one
 * stands, and one that requires conversation resolution until the last thread is resolved. It
 * does what `reviewRound` does with them:
 *   - a change request ends the round rejected whatever head it names and whether or not the
 *     reviewer has completed, and the issue goes back to `implementing` (`TriggerReviewRejected`).
 *     reviewer-text-findings' defect case reaches it, in `reviewing`;
 *   - an approval of the head (`classify.ApprovalStands`) ends the round approved when GitHub
 *     reports the head `CLEAN`, `UNSTABLE` or `HAS_HOOKS` (`record.MergeState.Mergeable`), and the
 *     issue moves to `accepting` (`TriggerReviewApproved`), on the approval alone
 *     (`review_test.go`'s "after the review alone"). Any other state — `BLOCKED`, which a check
 *     the base requires holds the head in while it runs, `BEHIND`, `DIRTY`, `UNKNOWN` — leaves
 *     the issue in `reviewing`, where the engine waits on GitHub's answer or reports the round
 *     stuck, and neither moves the record;
 *   - an approval of another head is set aside (`classify.ApprovalSetAside`, the round
 *     `stuckApprovedOtherCode`), which is what a push while the record is `reviewing` leaves, and
 *     the issue stays in `reviewing` until a review of the new head decides the round.
 * Once the record has moved it stays where it moved to: a push in `accepting` lands outside any
 * round (`review.go`'s `issue.Phase != phase.Reviewing`, `review_test.go`'s "the approval of the
 * head, then a push"). A reviewer's completion after its decision is a late one, answered as the
 * run's label's engine answers it (`handle`'s `handoff/complete`).
 *
 * The check runs of a head move nothing of their own, as they are none of `reviewRound`'s but for
 * the report of a stuck round (`classify.ChecksConcludedOnHead`): a check the base requires holds
 * the merge state `BLOCKED` until it passes, and the record moves on that state. Not modelled: a
 * push on its way (`classify.CodeOnItsWay`), since the run's remote takes each push whole and its
 * tip is never a head the pull request is still waiting for. */
let servedPhase = PHASE;

/** The routes of the run's world and the merge state it reports for each head (rig.sh's
 * fixture_write), read afresh at each look, since the gh stand-in rewrites the file when a call
 * edits the pull request's body. A run with no fixtures.json has no world to read and no round
 * that ends. */
const Fixtures = z.looseObject({
  routes: z.array(
    z.looseObject({
      match: z.string(),
      methods: z.array(z.string()).optional(),
      stdout: z.unknown().optional(),
      graph: z.unknown().optional(),
    })
  ),
  mergeStates: z.unknown().optional(),
});
/** The merge states the issue may advance on (`record.MergeState.Mergeable`). */
const MERGEABLE = ["CLEAN", "UNSTABLE", "HAS_HOOKS"];

/** The run's pull request as its world says it now, read from the files the gh stand-in answers
 * it from, so the record and gh say one thing: the head its branch is at on the run's remote,
 * the state and the merge state GitHub reports for that head (gh-threads.ts's `heldPullRequest`
 * and `mergeStateOf`), and the review that last decided the round, the last whose state decides
 * one (gh-threads.ts's DECIDES, `classify`'s `DecidesRound`: a COMMENT and a pending review
 * decide nothing, and leave the decision before them standing) among the reviews the world held
 * and those the run left (gh-threads.ts's `submittedReviews`), its head written whole where the
 * review names the pull request's head by a prefix. That is the round's decision, which main's
 * record holds (`api.PullRequestView`'s `reviewDecision`, "the latest review round's decision"),
 * and not GitHub's review decision, which counts each reviewer's approvals against the base's
 * rule (gh-threads.ts's `reviewDecisionOf`). `round` is whether the world holds a round to read
 * at all: a run with no remote or no fixtures.json has none, and its pull request is the one its
 * world.json names, open, with no merge state read. */
function worldPull(): { pull: PullRequestFacts; round: boolean } {
  const tip = Bun.spawnSync([
    "git",
    "-C",
    `${runDir}/remote.git`,
    "rev-parse",
    `refs/heads/${BRANCH}`,
  ]);
  const head = tip.exitCode === 0 ? tip.stdout.toString().trim() : world.head;
  if (tip.exitCode !== 0 || !existsSync(`${runDir}/fixtures.json`))
    return { pull: { number: PR, head, state: "OPEN" }, round: false };
  const { routes, mergeStates } = Fixtures.parse(
    JSON.parse(readFileSync(`${runDir}/fixtures.json`, "utf8"))
  );
  const record = `${runDir}/${CALLS}`;
  const calls = existsSync(record) ? recordedCalls(record) : [];
  const at = { routes, world };
  const named = submittedReviews(at, calls).findLast((review) => DECIDES.includes(review.state));
  const threads = threadState(at, calls);
  const held = heldPullRequest(routes).state;
  return {
    pull: {
      number: PR,
      head,
      state: typeof held === "string" ? held : "OPEN",
      mergeState: mergeStateOf(at, threads, mergeStates, head, calls),
      decision:
        named === undefined
          ? undefined
          : {
              state: named.state,
              head: named.head.length >= 7 && head.startsWith(named.head) ? head : named.head,
            },
    },
    round: true,
  };
}

/** The phase the review round moves the issue to on the pull request `pull` (`worldPull`), or
 * undefined while it stays in `reviewing` (`servedPhase`). */
function roundOutcome(pull: PullRequestFacts): "accepting" | "implementing" | undefined {
  if (pull.decision?.state === "CHANGES_REQUESTED") return "implementing";
  return pull.decision?.head === pull.head && MERGEABLE.includes(pull.mergeState ?? "")
    ? "accepting"
    : undefined;
}

interface LogLine {
  readonly at: string;
  readonly method: string;
  readonly path: string;
  readonly status: number;
  readonly role: string;
  readonly code?: string;
  readonly grantId?: string;
  readonly sessionId?: string;
  readonly mintedGrantId?: string;
  readonly handoffId?: string;
  readonly handoffVersion?: number;
  readonly to?: string;
  readonly reason?: string;
  readonly phase?: string;
  readonly failed?: boolean;
}

const expectedBootToken = (await Bun.file(bootTokenFile).text()).trim();
if (expectedBootToken.length === 0) {
  console.error(`boot token file ${bootTokenFile} is empty`);
  process.exit(2);
}

/** The grants the tool minted, each with the session it was minted for: the claim's own, which
 * the record a completion names is held to (`handoffRecord`). */
const grants = new Map<string, { readonly expiresAt: number; readonly sessionId: string }>();

/** `POST /legion/v1/handoff/complete`'s body: every key any label's contract sends, not this
 * checkout's alone, for the reason the state body carries every settings key (standin-record.ts's
 * `stateBody`): a
 * label's agents load the plugin of their own checkout. The base label, then built from main
 * `ff2a10ef`, sent `ready`, a boolean its contract requires on every completion; #2539 took it
 * out of this checkout's request, a strict object, so a stand-in parsing with this checkout's
 * alone answered every base completion 400 with zod's `unrecognized_keys`. Measured on
 * 2026-11-12: all 20 base runs of k1 and k2 logged `handoff/complete` 400 and nothing else, and
 * each scored a world of its own (review 5887659447, finding 1). The object stays strict, so a
 * key no label sends is still refused, and the daemon of that label answered a completion
 * carrying `ready` 200, as this does. A key a later contract drops stays here for as long as a
 * label the rig builds still sends it. */
const CompleteRequest = LegionHandoffCompleteRequest.extend({ ready: z.boolean().optional() });

/** The reviewer's decision the record holds: the round's, read from the world while the issue
 * stands in `reviewing` and kept as it stood when the issue left it, since a review outside a
 * round decides none (`api.PullRequestView`: "from the review that decided it until the next
 * round opens"). A stand-in started outside `reviewing` holds the one the world held then. */
let decided: PullRequestFacts["decision"];
let decisionRead = false;

/** The pull request the record holds on `read` of the world, after moving the record where the
 * round sends the issue the first time a read finds the round over while it is `reviewing`
 * (`roundOutcome`, `enter`). */
function settled(read = worldPull()): PullRequestFacts {
  if (servedPhase === "reviewing" || !decisionRead) {
    decided = read.pull.decision;
    decisionRead = true;
  }
  const outcome = servedPhase === "reviewing" && read.round ? roundOutcome(read.pull) : undefined;
  if (outcome !== undefined) enter(outcome, outcome === "implementing" ? "review" : undefined);
  return { ...read.pull, decision: decided };
}

/** The phase a state read serves now: `servedPhase`, as the world's round leaves it (`settled`). */
function served(): string {
  settled();
  return servedPhase;
}

function json(status: number, body: unknown): Response {
  return Response.json(body, { status });
}

function forbidden(message: string): Response {
  return json(403, { error: message });
}

/** A workflow refusal in the daemon's `Failure` shape, `{code, error}` (`writeFailure`). */
function failure(status: number, code: string, message: string): Response {
  return json(status, { code, error: message });
}

function stringField(body: unknown, key: string): string | undefined {
  if (typeof body !== "object" || body === null) return undefined;
  const value = (body as Record<string, unknown>)[key];
  return typeof value === "string" ? value : undefined;
}

function booleanField(body: unknown, key: string): boolean | undefined {
  if (typeof body !== "object" || body === null) return undefined;
  const value = (body as Record<string, unknown>)[key];
  return typeof value === "boolean" ? value : undefined;
}

function numberField(body: unknown, key: string): number | undefined {
  if (typeof body !== "object" || body === null) return undefined;
  const value = (body as Record<string, unknown>)[key];
  return typeof value === "number" ? value : undefined;
}

/** `{<key>: <value>}` for a value the request carried, and no key at all for one it did not: an
 * absent key is how a line says the body named none, which is what the log's readers take it for
 * (score.ts's DaemonLine). */
function carried<K extends string, V>(key: K, value: V | undefined): Record<K, V> | undefined {
  return value === undefined ? undefined : ({ [key]: value } as Record<K, V>);
}

/** Mirrors the real daemon's rule: present and not yet expired, nothing else. Each route parses
 * its body with its own contract schema first, exactly as the daemon's route table does, and
 * hands the parsed id here. */
function resolveGrant(grantId: string): Response | undefined {
  const grant = grants.get(grantId);
  if (!grant || grant.expiresAt <= Date.now()) return forbidden("Invalid or expired grant");
  return undefined;
}

/** The workflow's line of working phases, in the order it advances them (phase.go's `Order`):
 * the only phases a move back is read against, each with a backward row to every phase before
 * it (workflow/table.go's `backwardRows`, from phase.go's `BackwardPairs`). */
const WORKFLOW_ORDER = [
  "planning",
  "implementing",
  "testing",
  "reviewing",
  "accepting",
  "retro",
  "merging",
];

/** The role that works `phase` (phase.go's `RoleFor`), "" for a phase no role works. */
function roleFor(phase: string): string {
  switch (phase) {
    case "planning":
      return "planner";
    case "implementing":
    case "retro":
      return "implementer";
    case "testing":
      return "tester";
    case "reviewing":
      return "reviewer";
    case "admitted":
    case "accepting":
      return "architect";
    case "merging":
      return "merger";
  }
  return "";
}

/** Why the workflow refuses this role's move of the issue standing in `phase` back to `to`, in
 * the engine's own sentence (engine.go's `backwardRefusal`), or undefined where it takes it: a
 * target no backward row of `phase` serves — anything but an earlier phase of WORKFLOW_ORDER —
 * and a role that does not run `phase`. The contract refuses a move naming no target before it
 * is sent, so the engine's first case is never reached here. Both labels' daemons of INFRA-764
 * round 25 (the checkouts its runs' built-from name) word it so. */
function backwardRefusal(phase: string, to: string): string | undefined {
  const at = WORKFLOW_ORDER.indexOf(phase);
  const back = WORKFLOW_ORDER.indexOf(to);
  if (back < 0 || at < 0 || back >= at)
    return `a backward move reaches an earlier phase, and no backward row of ${phase} serves ${to}; the ${role}'s move of ${issue} changed nothing`;
  if (roleFor(phase) !== role)
    return `a backward move is the role running the issue's phase: ${phase} of ${issue} is the ${roleFor(phase)}'s, so the ${role}'s move to ${to} changed nothing`;
  return undefined;
}

/** The run's own Dispatch (rig.sh's worker_boot names it) and the file holding the bearer the
 * run reads it with, which a completion's handoff record is read back from (`handoffRecord`). */
const DISPATCH_URL = process.env.DISPATCH_URL ?? "";
const DISPATCH_TOKEN_FILE = process.env.DISPATCH_TOKEN_FILE ?? "";
/** A handoff record as Dispatch answers `GET /api/v1/handoffs/{id}`, as far as the daemon reads
 * it (dispatch/client.go's `handoffResponse`): what it is a record of, and who wrote it. */
const HandoffRecord = z.looseObject({
  issue_key: z.string(),
  phase: z.string(),
  version: z.number(),
  author: z.looseObject({ kind: z.string(), id: z.string() }).optional(),
});
/** A refusal Dispatch answers in its error body, `{code, error}` (dispatch/api's `errorBody`). */
const DispatchError = z.looseObject({ code: z.string().optional(), error: z.string().optional() });

/** `GET route` of the run's own Dispatch as the run's agent bearer, its JSON answer. Throws naming
 * the route and what Dispatch answered, or that the run names no Dispatch. */
async function dispatchRead(route: string): Promise<unknown> {
  if (DISPATCH_URL === "") throw new Error("the run names no Dispatch (DISPATCH_URL)");
  const token =
    DISPATCH_TOKEN_FILE === "" ? "" : (await Bun.file(DISPATCH_TOKEN_FILE).text()).trim();
  const answer = await fetch(`${DISPATCH_URL}${route}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  const text = await answer.text();
  if (!answer.ok)
    throw new Error(`Dispatch answered GET ${route} ${answer.status}: ${text.trim()}`);
  return JSON.parse(text);
}

/** The run's seeds, <run>/fixture.json (seed-handoffs.ts's TesterFixture, which rig.sh's
 * worker_run_world and its runners write before any agent of the run boots): the handoff record
 * each phase before the one under test left on the run's issue, by its handoff word. A run with
 * none seeded no history, and its record holds the booted role's claim alone. */
const SEEDS = `${runDir}/fixture.json`;
const Seed = z.object({ id: z.string().min(1), version: z.number().int().positive() });
const Seeds = z.looseObject({
  plan: Seed.optional(),
  implement: Seed.optional(),
  test: Seed.optional(),
  review: Seed.optional(),
});
/** The roles a seed records a phase of, in the order the workflow runs them, each with the word
 * its record is seeded under, the phase its completion was of, the verdict it reported (a
 * tester's alone reports one, and the seeded tester's passed: seed-handoffs.ts records its
 * passing result and the status its pass moves the issue to), and whether it named a commit:
 * the implementer's names its code commit and the tester's the head it tested, both the pull
 * request's head when the run began (world.json's `head`, which the seeds record the implement
 * handoff and both results at: rig.sh's worker_run_world); a planner's and a reviewer's name
 * none, as production's rows of those two carry none (production-shapes.json's `legion
 * read_record`). */
const SEEDED = [
  { role: "planner", word: "plan", phase: "planning", verdict: "", commit: false },
  { role: "implementer", word: "implement", phase: "implementing", verdict: "", commit: true },
  { role: "tester", word: "test", phase: "testing", verdict: "pass", commit: true },
  { role: "reviewer", word: "review", phase: "reviewing", verdict: "", commit: false },
] as const;
/** An actor as Dispatch names one, the issue as it answers `GET /api/v1/issues/{key}`, a seeded
 * record with its author, and the spec's approval as `GET /api/v1/artifacts/{id}` carries it
 * (dispatch/model's `ArtifactApproval`), each as far as the record reads it. */
const Actor = z.looseObject({ kind: z.string(), id: z.string() });
const FiledIssue = z.looseObject({
  created_at: z.string(),
  created_by: Actor.nullable(),
  primary_artifact_id: z.string().min(1),
});
const SeededHandoff = HandoffRecord.extend({ author: Actor });
const Spec = z.looseObject({
  approval: z
    .looseObject({
      state: z.string(),
      latest_version: z.number(),
      version: z.number().optional(),
      reason: z.string().nullable().optional(),
    })
    .optional(),
});

/** What the run's world seeded before any agent of it booted, read back from the run's own
 * Dispatch so the record and Dispatch say one thing: when the issue was filed, which is when the
 * seeds hand it to Legion and it takes its slot (seed.ts's `testerProofIssue` files it and writes
 * the daemon's `in_progress` in one step); the session that filed it, the tree's architect
 * (seed.ts's FILER); its spec, the document the design gate is on; and each seeded record with
 * the session that wrote it, the role's own. A record the run's Dispatch holds as another
 * issue's, phase's or version's is a world the rig seeded wrongly, refused here. */
interface History {
  readonly admittedAt: string;
  readonly architect: string;
  readonly spec: string;
  readonly seeded: readonly {
    readonly role: string;
    readonly session: string;
    readonly record: { readonly id: string; readonly version: number };
    readonly commit: boolean;
    readonly phase: string;
    readonly verdict: string;
  }[];
}
const seededHistory = async (): Promise<History> => {
  const seeds = Seeds.parse(JSON.parse(readFileSync(SEEDS, "utf8")));
  const filed = FiledIssue.parse(await dispatchRead(`/api/v1/issues/${encodeURIComponent(issue)}`));
  const seeded: History["seeded"][number][] = [];
  for (const { role: named, word, commit, phase, verdict } of SEEDED) {
    const record = seeds[word];
    if (record === undefined) continue;
    const held = SeededHandoff.parse(
      await dispatchRead(`/api/v1/handoffs/${encodeURIComponent(record.id)}`)
    );
    if (held.issue_key !== issue || held.phase !== word || held.version !== record.version)
      throw new Error(
        `${SEEDS} names ${record.id} as version ${record.version} of ${issue}'s ${word} handoff, which the run's Dispatch holds as version ${held.version} of ${held.issue_key}'s ${held.phase} handoff`
      );
    seeded.push({ role: named, session: held.author.id, record, commit, phase, verdict });
  }
  return {
    admittedAt: filed.created_at,
    architect: filed.created_by?.kind === "session" ? filed.created_by.id : "",
    spec: filed.primary_artifact_id,
    seeded,
  };
};
/** The run's history, or the boot failed with why it could not be read: a run whose seeds
 * cannot be read back is never answered with a record of some other history. */
let HISTORY: History | undefined;
if (existsSync(SEEDS)) {
  try {
    HISTORY = await seededHistory();
  } catch (error) {
    console.error(
      `daemon-standin: the history ${SEEDS} names could not be read back from the run's Dispatch: ${error instanceof Error ? error.message : String(error)}`
    );
    process.exit(2);
  }
}

/** A claim of the issue as this stand-in holds it (standin-record.ts's `ClaimFacts`). */
interface Claim {
  session: string;
  state: string;
  generation: number | undefined;
  capabilities: CapabilityFacts | undefined;
  served: boolean;
  pendingTask: { phase?: string; queuedAt: string } | undefined;
  lastTurnStart: string | undefined;
  lastTurnEnd: string | undefined;
  lastReady: string | undefined;
}
/** A claim in `state` under `session`, of the process `generation` names (none: no process). */
function claimOf(
  session: string,
  state: string,
  generation: number | undefined,
  served: boolean
): Claim {
  return {
    session,
    state,
    generation,
    capabilities: undefined,
    served,
    pendingTask: undefined,
    lastTurnStart: undefined,
    lastTurnEnd: undefined,
    lastReady: undefined,
  };
}
/** The processes the issue's pod has started so far: each claim's process is the next, as an
 * issue pod's launcher numbers them (`runtime.SandboxLocator.Generation`). */
let launches = 0;
function launched(): number {
  launches += 1;
  return launches;
}
/** The claims of the issue, by role (the architect's among them), and the handoff each role's
 * completion of its current phase named: what the record's `architect` and `workers` are built
 * from (`recordFacts`). */
const CLAIMS = new Map<string, Claim>();
const HANDOFFS = new Map<
  string,
  { commit: string | undefined; record: { id: string; version: number } | undefined }
>();
/** Whether `named`'s row holds a completion (`record.PhaseRow.Completed`): a handoff commit or a
 * handoff record, which a `recorded` engine refuses a late completion from (`leftItsPhase`). */
function completed(named: string): boolean {
  const held = HANDOFFS.get(named);
  return held?.commit !== undefined || held?.record !== undefined;
}
/** The record each role's row last named (`record.PhaseRow.LastHandoff`), which a move that
 * clears the row's handoff keeps, and the completions the workflow has been handed, each by the
 * key its route gives one (`factOf`). */
const LAST_HANDOFF = new Map<string, string>();
const FACTS = new Set<string>();
/** The key of one completion (handoff.go's `eventID`, less the issue and its generations, which
 * are one in a run): the role, the phase the issue stands in, the implementer's pass through
 * it, what the completion reported — the record it names where it is record-backed, else its
 * commit — and its verdict. */
function factOf(
  named: string,
  phase: string,
  passes: number,
  reported: string,
  verdict: string
): string {
  return [named, phase, passes, reported, verdict].join("\0");
}

/** A registration of `claim`'s agent under `session`: a claim that was registered before is
 * relaunched, a new process of the pod. */
function register(claim: Claim, session: string | undefined): void {
  if (claim.state !== "launching") claim.generation = launched();
  claim.session = session ?? claim.session;
  claim.state = "registered";
  claim.lastTurnStart = undefined;
}
/** `claim`'s agent saying at `at` that it can be prompted. A rig agent is `omp -p`, handed its
 * prompt at its launch, so the turn the daemon would record at the task's delivery
 * (supervise's `LastTurnStart`, `StateWorking`) begins where the agent first says so. */
function ready(claim: Claim, at: string): void {
  claim.lastReady = at;
  if (claim.state !== "registered") return;
  claim.state = "working";
  claim.lastTurnStart = at;
}
/** The end of a turn of `claim`'s agent at `at`, which retires the task the turn confirmed
 * (`servingGeneration`); a turn that failed the claim leaves it with no process. */
function endTurn(claim: Claim, at: string, failed: boolean): void {
  claim.lastTurnEnd = at;
  if (claim.pendingTask !== undefined) {
    claim.pendingTask = undefined;
    claim.served = true;
  }
  claim.state = failed ? "failed" : "idle";
  if (failed) claim.generation = undefined;
}

// The history first, in the order the workflow ran it: the architect, then each role whose
// record a seed wrote, each a claim whose turn ended before this daemon recorded it, so each is
// idle and carries no time of a turn (standin-record.ts's header). A seeded record stands for
// the completion that named it: it is on its role's row, it is the record that role last named,
// and it is a fact the workflow already took, of the role's own phase on the issue's first pass.
if (HISTORY !== undefined) {
  CLAIMS.set("architect", claimOf(HISTORY.architect, "idle", launched(), true));
  for (const seed of HISTORY.seeded) {
    CLAIMS.set(seed.role, claimOf(seed.session, "idle", launched(), true));
    HANDOFFS.set(seed.role, { commit: seed.commit ? world.head : undefined, record: seed.record });
    LAST_HANDOFF.set(seed.role, seed.record.id);
    FACTS.add(factOf(seed.role, seed.phase, 0, seed.record.id, seed.verdict));
  }
}
// A role the rig says completed its phase with a commit before the run began (`COMPLETED`) holds
// that commit on its row, the head the run began at, where no seed gave the row its record too.
for (const named of COMPLETED)
  if (!HANDOFFS.has(named)) HANDOFFS.set(named, { commit: world.head, record: undefined });
/** One line of the run's record of what reached its daemon, as an earlier stand-in of the run
 * wrote it (`LogLine`). */
const Logged = z.looseObject({
  at: z.string(),
  path: z.string(),
  status: z.number(),
  role: z.string().optional(),
  sessionId: z.string().optional(),
  failed: z.boolean().optional(),
});
// Then each agent the run booted before this one, read off the run's one record of what reached
// its daemon, which every stand-in of the run appends to (rig.sh's worker_boot): the session it
// registered, when it said it could be prompted and when its turn ended. rig.sh boots an agent
// only after the one before it has exited, so each such claim is idle now, or failed where a
// turn outcome failed it. threads-closed's reviewer reads the implementer's claim so.
if (existsSync(logFile)) {
  const earlier = new Set<Claim>();
  for (const text of readFileSync(logFile, "utf8").split("\n")) {
    if (text.trim() === "") continue;
    const value: unknown = JSON.parse(text);
    const line = Opened.safeParse(value).success ? undefined : Logged.safeParse(value).data;
    if (line?.role === undefined) continue;
    if (line.path === "/legion/v1/claims/register" && line.status === 200) {
      const claim = CLAIMS.get(line.role) ?? claimOf("", "launching", launched(), false);
      register(claim, line.sessionId);
      CLAIMS.set(line.role, claim);
      earlier.add(claim);
    }
    const claim = CLAIMS.get(line.role);
    if (claim === undefined || !earlier.has(claim)) continue;
    if (line.path === "/legion/v1/claims/ready" && line.status === 204) ready(claim, line.at);
    if (line.path === "/legion/v1/claims/turn-outcome" && line.status === 200)
      endTurn(claim, line.at, line.failed === true);
  }
  for (const claim of earlier) if (claim.state !== "failed") claim.state = "idle";
}
/** This stand-in's own claim, the booted agent's: a new process of its role's claim, which the
 * daemon is launching until the agent registers. */
const OWN = claimOf(
  CLAIMS.get(role)?.session ?? "",
  "launching",
  launched(),
  CLAIMS.get(role)?.served ?? false
);
CLAIMS.set(role, OWN);

/** The entry each role stands on and the entry its claim was last given a task of
 * (`record.PhaseRow.Entry`, `supervise.Claim.GivenEntry`). A start raises its role's entry
 * (workflow's `enter`), and the task it queues is of that entry, so the two are equal for a role
 * that holds a claim and the second is 0 for one that holds none. */
const ENTRIES = new Map<string, { entry: number; given: number }>();
function raise(named: string): void {
  const held = ENTRIES.get(named) ?? { entry: 0, given: 0 };
  held.entry += 1;
  if (CLAIMS.has(named)) held.given = held.entry;
  ENTRIES.set(named, held);
}
/** The task a start of `phase` queues to its role at `at` (`api.PendingTaskView`): of that phase,
 * and of no phase for the architect's. */
function taskOf(phase: string, at: string): NonNullable<Claim["pendingTask"]> {
  return roleFor(phase) === "architect" ? { queuedAt: at } : { phase, queuedAt: at };
}
// The entries the history raised: the architect's at the issue's admission, each seeded role's
// at the start of its phase, and the start of the phase the issue stands in, where that is the
// first of its role or the architect's second, its acceptance. The role working that phase holds
// its task until a turn of its retires it, unless its row already holds the phase's completion:
// tester-proof's tester and reviewer-text-findings' reviewer hold the task their prompt is, and
// threads-closed's halves hold none, since the daemon starts no phase for that loop.
raise("architect");
for (const { role: named } of HISTORY?.seeded ?? []) raise(named);
if (roleFor(PHASE) !== "" && (PHASE === "accepting" || !ENTRIES.has(roleFor(PHASE))))
  raise(roleFor(PHASE));
const WORKING = CLAIMS.get(roleFor(PHASE));
if (WORKING !== undefined && !completed(roleFor(PHASE)))
  WORKING.pendingTask = taskOf(PHASE, startedAt);

/** The implementer's counts, which every move back raises (workflow's `recordPass`: the pass and
 * the move's own pair) and a review that requested changes raises once more (`recordRound`). */
let pass = 0;
let rounds = 0;
const backwardMoves: Record<string, number> = {};
/** When the issue took the slot it holds, and when the daemon last saw anything of its tree move:
 * a request of the claim's that it took, or a move of the record. No move of the rig brings an
 * issue back before its review once it is past it (a backward move is its phase's own role's,
 * and `accepting` is the architect's, which no run boots), so a slot given up is never taken
 * again and the time it was taken at never moves. */
const admittedAt = HISTORY?.admittedAt ?? startedAt;
let movedAt = startedAt;

/** The Dispatch status the record holds: the one its phase shows, and for a stand-in started in
 * a phase with none of its own the hand-over's `in_progress` (`phaseStatus`). */
let shownStatus = phaseStatus(PHASE) || "in_progress";
/** The status writes the record's moves owe the run's Dispatch and have not finished, oldest
 * first (`writeStatuses`). */
const OWED: { status: string; observed: string; attempts: number; lastError?: string }[] = [];

/** Moves the record to `to`, as the engine's transition writes it (engine.go's `transition`):
 * the phase and, where the phase it enters shows another status than the record holds, that
 * status, queued for the run's Dispatch as the engine queues its outbox row (`Engine.status`);
 * for a move back (`back`), the implementer's pass and the move's pair, and its
 * round where a review requested the changes; and the start of the role that works
 * `to` — its handoff cleared (`clearHandoff`), its entry raised, and the task of that entry
 * queued to its claim, which the start makes where the role held none. The slot follows the
 * phase (`keepsSlot`, at each read). */
function enter(to: string, back: "review" | "move" | undefined): void {
  const from = servedPhase;
  const now = new Date().toISOString();
  servedPhase = to;
  movedAt = now;
  const shown = phaseStatus(to);
  if (shown !== "" && shown !== shownStatus) {
    OWED.push({ status: shown, observed: shownStatus, attempts: 0 });
    shownStatus = shown;
  }
  if (back !== undefined) {
    pass += 1;
    backwardMoves[`${from}-${to}`] = (backwardMoves[`${from}-${to}`] ?? 0) + 1;
    if (back === "review") rounds += 1;
  }
  const starts = roleFor(to);
  if (starts === "") return;
  HANDOFFS.delete(starts);
  const claim = CLAIMS.get(starts) ?? claimOf("", "queued", undefined, false);
  CLAIMS.set(starts, claim);
  raise(starts);
  claim.pendingTask = taskOf(to, now);
}

/** The run's issue as its Dispatch answers it, as far as a status write reads it. */
const ShownIssue = z.looseObject({ status: z.string() });
/** Writes each status the record's moves owe the run's Dispatch, in the order they were queued,
 * as the daemon's outbox runs a `dispatch_status` row (packages/daemon/internal/daemon/
 * outbox.go's `writeStatus`): it reads the issue, writes nothing where Dispatch already shows
 * the status or shows another than the one the move observed — a change someone made since,
 * which stands — and otherwise writes it through `PATCH /api/v1/issues/{key}` as the daemon's
 * own session, `legion-daemon:<PROJECT>` (dispatch/client.go's `SetStatus`, the session the
 * world's seeds write each status as: seed-handoffs.ts's DAEMON). So a `dispatch read` after a
 * move shows the status the record names; the record moved and Dispatch kept the status of the
 * phase it had left. Each request this stand-in answers runs the writes before its answer, and a
 * write the run's Dispatch does not take stays owed, with why, for the next (the state's
 * `pendingStatusWrites`), where the outbox waits out a backoff. The write carries no
 * `expected_sequence`, as the outbox's does: the read above it is of the same moment, and a
 * fenced write refused for an agent's own write of that moment would stand in the run's
 * Dispatch log as a refusal of that agent's (health.ts's rule 16). */
async function writeStatuses(): Promise<void> {
  for (let owed = OWED[0]; owed !== undefined; owed = OWED[0]) {
    try {
      const route = `/api/v1/issues/${encodeURIComponent(String(issue))}`;
      const shown = ShownIssue.parse(await dispatchRead(route)).status;
      if (shown === owed.observed) {
        const token =
          DISPATCH_TOKEN_FILE === "" ? "" : (await Bun.file(DISPATCH_TOKEN_FILE).text()).trim();
        const answer = await fetch(`${DISPATCH_URL}${route}`, {
          method: "PATCH",
          headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
          body: JSON.stringify({
            status: owed.status,
            actor: { kind: "session", id: DAEMON_SESSION },
          }),
        });
        if (!answer.ok)
          throw new Error(
            `Dispatch answered PATCH ${route} ${answer.status}: ${(await answer.text()).trim()}`
          );
      }
      OWED.shift();
    } catch (error) {
      owed.attempts += 1;
      owed.lastError = error instanceof Error ? error.message : String(error);
      return;
    }
  }
}

/** The contract the label's plugin declares at its registration (`pluginContract`, the
 * `legion.daemonApiVersion` of its package: packages/pi-legion/src/claim-session.ts), which
 * every claim of the issue's pod records, since they all load the one plugin: read from the
 * label's checkout until the agent registers, and the registration's own from then on. 0 is the
 * daemon's word for no contract recorded, which is a checkout whose package names none. */
const PluginPackage = z.looseObject({
  legion: z.looseObject({ daemonApiVersion: z.number().int().nonnegative() }),
});
let pluginContract = (() => {
  const file = `${CHECKOUT}/packages/pi-legion/package.json`;
  if (!existsSync(file)) return 0;
  return (
    PluginPackage.safeParse(JSON.parse(readFileSync(file, "utf8"))).data?.legion.daemonApiVersion ??
    0
  );
})();

/** A `claims/ready` as far as the record reads it: the capability report a session sends on one
 * of its own (`claim.CapabilityReport`). The daemon's own normalising of the rows (the live
 * table's order, a name it does not know dropped, a live row not reported marked open:
 * `capabilities.Normalize`) is not modelled: a row is `ok` or open as the session reported it,
 * an open one with the daemon's own words where it gave none (`capabilities.NewRow`). */
const ReadyReport = z.looseObject({
  capabilities: z
    .looseObject({
      measuredAt: z.string(),
      rows: z.array(
        z.looseObject({ name: z.string().min(1), ok: z.boolean(), detail: z.string() })
      ),
    })
    .optional(),
});

/** The design gate as the run's Dispatch holds the issue's spec now (dispatch/model's
 * `ArtifactApproval`), read at each state read since a write of the spec moves it: the document,
 * its latest version, the version a person approved — `approved` at the latest, `stale` at an
 * older one — and the reason of a request for changes at the latest. Undefined for a run that
 * seeded no history, and for a spec with no version, which is no registered gate. */
async function gateNow(): Promise<GateFacts | undefined> {
  if (HISTORY === undefined) return undefined;
  const { approval } = Spec.parse(
    await dispatchRead(`/api/v1/artifacts/${encodeURIComponent(HISTORY.spec)}`)
  );
  if (approval === undefined || approval.latest_version < 1) return undefined;
  const approved = approval.state === "approved" || approval.state === "stale";
  return {
    artifactId: HISTORY.spec,
    currentVersion: approval.latest_version,
    approvedVersion: approved && approval.version !== undefined ? approval.version : null,
    changesReason:
      approval.state === "changes_requested" && approval.version === approval.latest_version
        ? (approval.reason ?? "")
        : undefined,
  };
}

/** `GET /legion/v1/state`: the record of the run's issue as the world stands now
 * (standin-record.ts's `stateBody`), read through this checkout's own contract so a body it does
 * not describe is never served. A world that cannot be read — the run's Dispatch not answering
 * for the spec — is answered 500 with why, as a daemon that cannot build its state answers,
 * never with a record of what the world last said. */
const stateAnswer = async (): Promise<Response> => {
  try {
    const pullRequest = settled();
    await writeStatuses();
    const workers: Record<string, RowFacts> = {};
    for (const [named, claim] of CLAIMS) {
      if (named === "architect") continue;
      const handoff = HANDOFFS.get(named);
      workers[named] = { claim, handoffCommit: handoff?.commit, handoffRecord: handoff?.record };
    }
    const body = stateBody(
      {
        project,
        issue,
        phase: servedPhase,
        status: shownStatus,
        statusWrites: OWED,
        repo,
        defaultBranch: base,
        pluginContract,
        architect: CLAIMS.get("architect"),
        workers,
        pass,
        rounds,
        backwardMoves,
        pullRequest,
        gate: await gateNow(),
        admittedAt,
        entries: Object.fromEntries(ENTRIES),
        movedAt,
        now: new Date().toISOString(),
      },
      startedAt
    );
    return json(200, LegionStateResponse.parse(body));
  } catch (error) {
    return json(500, {
      error: `the state could not be built: ${error instanceof Error ? error.message : String(error)}`,
    });
  }
};

/** The refusal of a completion naming handoff record `id` at `version`, read back from the run's
 * own Dispatch as the daemon reads it (handoff.go's `handoffRecord`), or undefined where the
 * record is this issue's, of the phase's handoff word, at that version and written by `session`,
 * the claim's own. A record Dispatch
 * answers 404 for, or refuses with a code that says the id names nothing it holds
 * (dispatch.PermanentRefusal: a 4xx with a code, but 401, 403, 408, 429 and a contended
 * parent), is HANDOFF_RECORD_NOT_FOUND, 404; any other answer, or none, DISPATCH_READ_FAILED,
 * 502; a record of another issue, phase or version HANDOFF_RECORD_MISMATCH, 409; one another
 * session wrote HANDOFF_SESSION_MISMATCH, 403; and a run that
 * names no Dispatch DISPATCH_UNAVAILABLE, 500. Each sentence is the daemon's, the Dispatch error
 * in it as Go prints one (`<code>: <message>`). */
async function handoffRecord(
  id: string,
  version: number,
  session: string
): Promise<Response | undefined> {
  const write = `\`dispatch handoff-write --issue ${issue} --phase ${HANDOFF_WORD} --body-file <file>\``;
  const read = `\`dispatch handoff-read --issue ${issue} --phase ${HANDOFF_WORD}\``;
  if (DISPATCH_URL === "") return failure(500, "DISPATCH_UNAVAILABLE", "Dispatch is unavailable");
  const unanswered = (why: string) =>
    failure(
      502,
      "DISPATCH_READ_FAILED",
      `Dispatch did not answer the read of handoff record ${id} (${why}): complete again once Dispatch answers; this completion changed nothing`
    );
  let answer: Response;
  try {
    const token =
      DISPATCH_TOKEN_FILE === "" ? "" : (await Bun.file(DISPATCH_TOKEN_FILE).text()).trim();
    answer = await fetch(`${DISPATCH_URL}/api/v1/handoffs/${encodeURIComponent(id)}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
  } catch (error) {
    return unanswered(error instanceof Error ? error.message : String(error));
  }
  const text = await answer.text();
  if (!answer.ok) {
    let refusal: z.infer<typeof DispatchError> = {};
    try {
      refusal = DispatchError.parse(JSON.parse(text));
    } catch {
      refusal = { error: text.trim() };
    }
    const code = refusal.code ?? "";
    const message = refusal.error || answer.statusText;
    const why = code === "" ? message : `${code}: ${message}`;
    const { status } = answer;
    const permanent =
      code !== "" &&
      code !== "ISSUE_CHANGED" &&
      status >= 400 &&
      status < 500 &&
      ![401, 403, 408, 429].includes(status) &&
      !(status === 409 && code === "PARENT_CONTENDED");
    if (!permanent && status !== 404) return unanswered(why);
    return failure(
      404,
      "HANDOFF_RECORD_NOT_FOUND",
      `Dispatch has no handoff record ${id} (${why}): name the record ${read} shows, or write one with ${write}, then complete again; this completion changed nothing`
    );
  }
  const record = HandoffRecord.safeParse(JSON.parse(text));
  if (!record.success)
    return unanswered(`a handoff record no reader can take: ${record.error.message}`);
  const held = record.data;
  if (held.issue_key !== issue || held.phase !== HANDOFF_WORD || held.version !== version)
    return failure(
      409,
      "HANDOFF_RECORD_MISMATCH",
      `handoff record ${id} is version ${held.version} of ${held.issue_key}'s ${held.phase} handoff, and this completion is of ${issue}'s ${HANDOFF_WORD} handoff at version ${version}: name the record ${read} shows; this completion changed nothing`
    );
  // The record is the claim's own session's (handoff.go's `handoff.Author.IsSession(session)`):
  // one another session wrote is refused 403, and one the daemon's own session wrote, a record
  // carried from before handoffs moved to the Dispatch issue, with its own sentence. The claim's
  // session is the one its grant was minted for, which is the one it registered: measured on
  // INFRA-764 round 25's 40 worker runs, every one of the 31 handoff records an agent wrote
  // names as its author the session that agent registered with the stand-in. A record a seed
  // wrote for the role an agent now plays is another session's, the seed's, so a completion
  // naming it is refused as production refuses a claim naming a record an earlier session of
  // its role wrote, and told to write its own.
  const author = held.author;
  if (!daemonAnswers("HANDOFF_SESSION_MISMATCH")) return undefined;
  if (author?.kind === "session" && author.id === session) return undefined;
  const named = `handoff record ${id} (${HANDOFF_WORD} version ${held.version})`;
  if (author?.kind === "session" && author.id === DAEMON_SESSION)
    return failure(
      403,
      "HANDOFF_SESSION_MISMATCH",
      `${named} was written by the daemon's own session ${author.id}, as a record carried from before handoffs moved to the Dispatch issue is, not by this claim's session ${session}: read it with ${read}, write it again with ${write} from this session, then complete naming the new record; this completion changed nothing`
    );
  return failure(
    403,
    "HANDOFF_SESSION_MISMATCH",
    `${named} was written by ${author?.kind ?? ""} ${author?.id ?? ""}, not by this claim's session ${session}: write the handoff again with ${write} from this session, then complete naming the new record; this completion changed nothing`
  );
}

/** `POST /legion/v1/claims/turn-outcome` (`turnOutcome` in packages/daemon/internal/api/claims.go):
 * a body of neither caller or of both, a sequence or a generation of 0, and an outcome neither
 * `clean` nor `refused` are refused 400 by the body's contract; a boot token that is not the
 * launch's is refused `InvalidBootToken`, and a report under it for another generation than the
 * launch's, 1, `StaleGeneration`; a claim and secret the claim does not authenticate
 * (`authenticates`) `InvalidSecret`. A report is then recorded in the window (`TURN_OUTCOMES`),
 * and the answer is the run of refused turns the window ends with (supervise's `RefusedRun`) and
 * whether it reached the launch failure limit, which fails the claim: from then on the claim's
 * secret authenticates nothing (`capabilityHeld`), so the plugin's next report, which names the
 * claim with its secret, is refused `InvalidSecret`, as is every other route that reads the
 * secret. Not modelled: a report under the boot token on a failed claim, which the daemon hands
 * to the failed machine and refuses with the machine's own sentence for a request in a failed
 * claim. Only the Claude bridge reports under a boot token, no rig agent runs one, and a run
 * whose claim failed is unhealthy whatever comes after (health.ts's `failedClaim`), so this
 * answers it with its own sentence and names no daemon's. */
function turnOutcome(body: unknown): Response {
  const parsed = LegionTurnOutcomeRequest.safeParse(body);
  if (!parsed.success) return json(400, { error: parsed.error.message });
  const report = parsed.data;
  if ("bootToken" in report) {
    if (report.bootToken !== expectedBootToken) return refuse(INVALID_BOOT_TOKEN);
    if (report.generation !== 1) return refuse(STALE_GENERATION);
    if (!capabilityHeld)
      return json(500, {
        error: "daemon-standin models no boot-token report on a claim the daemon failed",
      });
  } else if (report.claimToken !== ROLE_TOKEN || !authenticates(report.secret)) {
    return refuse(INVALID_SECRET);
  }
  const turn = {
    generation: report.generation,
    sequence: report.sequence,
    refused: report.outcome === "refused",
  };
  const at = TURN_OUTCOMES.findIndex(
    (kept) =>
      kept.generation > turn.generation ||
      (kept.generation === turn.generation && kept.sequence >= turn.sequence)
  );
  const same = TURN_OUTCOMES[at];
  if (same !== undefined && same.generation === turn.generation && same.sequence === turn.sequence)
    TURN_OUTCOMES[at] = turn;
  else TURN_OUTCOMES.splice(at === -1 ? TURN_OUTCOMES.length : at, 0, turn);
  TURN_OUTCOMES.splice(0, Math.max(0, TURN_OUTCOMES.length - TURN_OUTCOME_LIMIT));
  let refusedRun = 0;
  for (let i = TURN_OUTCOMES.length - 1; i >= 0 && TURN_OUTCOMES[i]?.refused === true; i--)
    refusedRun++;
  const failed = refusedRun >= TURN_OUTCOME_LIMIT;
  if (failed) capabilityHeld = false;
  endTurn(OWN, new Date().toISOString(), failed);
  return json(200, { refusedRun, failed });
}

/** Answers one POST: the response, the grant it minted, and, for a completion that reached the
 * phase read, the phase it was answered on. Its cases are the routes this stand-in answers of
 * those its label's daemon registers (`ROUTES`, which `fetch` holds every request to first).
 * One route a phase worker's tool can reach is registered by both labels' daemons and answered
 * by no case: `claims/ready-for-update`, the tool's `ready_for_update`. It answers the update
 * message a pod replacement sends, and the rig opens no update round, no prompt names one, and
 * no kept run called it, so an agent reaches it only with a round it made up; the daemon's answer
 * to that is a sentence its supervisor builds in an unexported function, which this does not
 * restate. Such a call is answered 404, which health reads as a world the daemon would not have
 * served, so the run is unhealthy and never scored. */
async function handle(
  path: string,
  body: unknown
): Promise<{ response: Response; mintedGrantId?: string; phase?: string }> {
  switch (path) {
    case "/legion/v1/claims/turn-outcome":
      return { response: turnOutcome(body) };
    case "/legion/v1/claims/release-records": {
      // `releaseRecords` there: the body's contract, then the claim's secret, then the project's
      // records since the time named, of which the rig keeps none.
      const parsed = LegionReleaseRecordsRequest.safeParse(body);
      if (!parsed.success) return { response: json(400, { error: parsed.error.message }) };
      if (parsed.data.claimToken !== ROLE_TOKEN || !authenticates(parsed.data.secret)) {
        return { response: refuse(INVALID_SECRET) };
      }
      return { response: json(200, { releases: [], resumes: [], holds: [] }) };
    }
    case "/legion/v1/claims/register": {
      if (stringField(body, "bootToken") !== expectedBootToken) {
        return { response: refuse(INVALID_BOOT_TOKEN) };
      }
      // The claim is the registering session's from here, under the contract its plugin
      // declares (`register`, `pluginContract`).
      register(OWN, stringField(body, "sessionId"));
      pluginContract = numberField(body, "pluginContract") ?? pluginContract;
      return {
        response: json(200, {
          claimToken: ROLE_TOKEN,
          tree: issue,
          issue,
          role,
          generation: 1,
          secret: SESSION_SECRET,
          promptAgents: [],
        }),
      };
    }
    case "/legion/v1/claims/ready": {
      if (!authenticates(stringField(body, "secret"))) {
        return { response: refuse(INVALID_SECRET) };
      }
      // The agent can be prompted, and its turn begins (`ready`); a ready of the session's own
      // carries its capability report, which the claim keeps (`ReadyReport`).
      ready(OWN, new Date().toISOString());
      const report = ReadyReport.safeParse(body).data?.capabilities;
      if (report !== undefined)
        OWN.capabilities = {
          measuredAt: report.measuredAt,
          ok: report.rows.filter((row) => row.ok).map((row) => row.name),
          open: report.rows
            .filter((row) => !row.ok)
            .map((row) => ({ name: row.name, detail: row.detail.trim() || "no detail reported" })),
        };
      return { response: new Response(null, { status: 204 }) };
    }
    case "/legion/v1/claims/activity": {
      if (!authenticates(stringField(body, "secret"))) {
        return { response: refuse(INVALID_SECRET) };
      }
      return { response: new Response(null, { status: 204 }) };
    }
    case "/legion/v1/grants": {
      const parsed = LegionGrantRequest.safeParse(body);
      if (!parsed.success) return { response: json(400, { error: parsed.error.message }) };
      if (!authenticates(parsed.data.secret)) {
        return { response: refuse(INVALID_SECRET) };
      }
      const grantId = randomUUID();
      const expiresAt = Date.now() + GRANT_TTL_MS;
      grants.set(grantId, { expiresAt, sessionId: parsed.data.sessionId });
      return {
        response: json(200, { grantId, expiresAt: new Date(expiresAt).toISOString() }),
        mintedGrantId: grantId,
      };
    }
    case COMPLETE_PATH: {
      const parsed = CompleteRequest.safeParse(body);
      if (!parsed.success) return { response: json(400, { error: parsed.error.message }) };
      const refused = resolveGrant(parsed.data.grantId);
      if (refused) return { response: refused };
      // Both the route and the workflow read the phase the issue stands in now, the one a state
      // read serves, and never the phase this role was booted in. The earlier HANDOFF_NO_RUN
      // fence of the route is not reached: a rig half is a live registered worker serving
      // generation 1, so the route attributes its completion to a run; nor is the entry fence
      // (the module doc says why).
      const phase = served();
      const { handoffId, handoffVersion, commit } = parsed.data;
      const namesRecord = handoffId !== undefined || handoffVersion !== undefined;
      // The route (packages/daemon/internal/api/handoff.go), as the label's daemon has it
      // (`RECORD_ROUTE`). A phase's writer is the role the workflow starts on it. A `left` route
      // (this checkout's) holds the completion to the phase it is of (`phase.Of`): the served
      // phase when this role works it, and otherwise the one it left, its own; the completion is
      // record-backed when that phase ends with a record and it is the served phase or the
      // completion names a record. An `issue` route (an older daemon's) holds it to the served
      // phase, and it is record-backed exactly when the issue stands in this role's own phase and
      // that phase ends with a record.
      const of = RECORD_ROUTE === "left" ? ROLE_PHASE : phase;
      const recordBacked =
        RECORD_ROUTE === "left"
          ? HANDOFF_WORD !== "" && (phase === ROLE_PHASE || namesRecord)
          : phase === ROLE_PHASE && HANDOFF_WORD !== "";
      if (recordBacked) {
        // A record it names is read back from the run's own Dispatch and held to the issue, the
        // word and the version (`handoffRecord`).
        if (handoffId === undefined || handoffVersion === undefined) {
          const write = `\`dispatch handoff-write --issue ${issue} --phase ${HANDOFF_WORD} --body-file <file>\``;
          return {
            phase,
            response: failure(
              400,
              "HANDOFF_RECORD_REQUIRED",
              `phase ${of} of ${issue} ends with its ${HANDOFF_WORD} handoff recorded on the Dispatch issue, and this completion names no record (handoffId and a positive handoffVersion): write it with ${write}, then complete again; this completion changed nothing`
            ),
          };
        }
        const unread = await handoffRecord(
          handoffId,
          handoffVersion,
          grants.get(parsed.data.grantId)?.sessionId ?? ""
        );
        if (unread !== undefined) return { phase, response: unread };
      } else if (!RECORD_PHASES.includes(of) && namesRecord) {
        return {
          phase,
          response: failure(
            409,
            "HANDOFF_RECORD_MISMATCH",
            `phase ${of} of ${issue} ends with no handoff record, and this completion names one: complete without handoffId and handoffVersion; this completion changed nothing`
          ),
        };
      }
      // The fact (intake's `ApplyFact`): one phase's completion is identified by where the
      // issue stands — its phase and its pass through it — with the role and what it reported,
      // the record a record-backed completion names or else its commit, and the verdict
      // (handoff.go's `eventID`), and it is marked processed before the workflow reads it. So a
      // call that repeats one the workflow already answered, taken or refused, is answered 409
      // HANDOFF_ALREADY_RECORDED and reaches no workflow, and the completions the world's seeds
      // stand for are such facts too (`FACTS`). This answered a repeat as its first call was
      // answered, a second success among them, which production gives none of.
      const fact = factOf(
        role,
        phase,
        pass,
        recordBacked ? (handoffId ?? "") : (commit ?? ""),
        parsed.data.verdict
      );
      if (FACTS.has(fact) && daemonAnswers("HANDOFF_ALREADY_RECORDED")) {
        const received = recordBacked
          ? `naming handoff record ${handoffId} (${HANDOFF_WORD} version ${handoffVersion})`
          : `at commit ${commit ?? ""}`;
        return {
          phase,
          response: failure(
            409,
            "HANDOFF_ALREADY_RECORDED",
            `the ${role} completion of phase ${phase} (pass ${pass} through it) ${received} was already received; this call changed nothing`
          ),
        };
      }
      FACTS.add(fact);
      // The workflow (packages/daemon/internal/workflow/engine.go, `handoff`): a late role's
      // completion is answered by the label's engine (LATE_ROLE): an older engine refuses every
      // one, and this checkout's records it unless the role's row holds a completion
      // (`leftItsPhase`'s `row.Completed()`), which is a row the world's seeds completed, one
      // the rig says completed with a commit, and one whose completion was taken here
      // (`completed`). The refusal's code and message are the daemon's own, read from its code
      // at boot (NOT_CURRENT_PHASE), because the skill has the worker quote them to its
      // architect.
      if (phase !== ROLE_PHASE) {
        if (LATE_ROLE === "refused" || completed(role)) {
          if (NOT_CURRENT_PHASE === undefined)
            throw new Error(`no refusal was read at boot for ${role}'s completion in ${phase}`);
          return {
            phase,
            response: failure(
              409,
              NOT_CURRENT_PHASE.code,
              NOT_CURRENT_PHASE.message.replaceAll(PHASE_SLOT, phase).replaceAll(ISSUE_SLOT, issue)
            ),
          };
        }
        // `leftItsPhase`: the completion is of the phase this role left (`phase.Of`, the
        // role's own), and a phase that ends with a record requires the record it names.
        if (HANDOFF_WORD !== "" && handoffId === undefined) {
          return {
            phase,
            response: failure(
              409,
              "HANDOFF_RECORD_REQUIRED",
              `the ${role}'s completion of phase ${ROLE_PHASE} of ${issue} names no handoff record: write it with \`dispatch handoff-write --issue ${issue} --phase ${HANDOFF_WORD} --body-file <file>\`, then complete again; this completion changed nothing`
            ),
          };
        }
      }
      // A phase that ends with a record ends with a new one: the record the role's row last
      // named, its previous completion's or the one its seed stands for (`LAST_HANDOFF`), means
      // this completion wrote none (the engine's HANDOFF_NOT_NEW). A repeat of a whole
      // completion never reaches here (HANDOFF_ALREADY_RECORDED above): this is the same record
      // under another verdict, or in a phase the issue has since entered.
      if (
        HANDOFF_WORD !== "" &&
        handoffId !== undefined &&
        handoffId === LAST_HANDOFF.get(role) &&
        daemonAnswers("HANDOFF_NOT_NEW")
      ) {
        return {
          phase,
          response: failure(
            409,
            "HANDOFF_NOT_NEW",
            `the ${role} named handoff record ${handoffId} for its previous phase of ${issue}; write this phase's handoff with \`dispatch handoff-write --issue ${issue} --phase ${HANDOFF_WORD} --body-file <file>\` before completing`
          ),
        };
      }
      // A taken completion is written on the role's row, its commit as the row's handoff commit
      // and its record as the row's handoff record (engine.go's `handoff` writes both from the
      // completion, a commit or a record it does not name included), either of which completes
      // the row (`row.Completed()`), and it moves nothing: the review decides the round, and the
      // record moves when the world reports a decision that ends it (`servedPhase`,
      // `roundOutcome`).
      if (HANDOFF_WORD !== "" && handoffId !== undefined) LAST_HANDOFF.set(role, handoffId);
      HANDOFFS.set(role, {
        commit,
        record:
          handoffId === undefined || handoffVersion === undefined
            ? undefined
            : { id: handoffId, version: handoffVersion },
      });
      return { response: json(200, {}), phase };
    }
    case "/legion/v1/phase/backward": {
      const parsed = LegionPhaseBackwardRequest.safeParse(body);
      if (!parsed.success) return { response: json(400, { error: parsed.error.message }) };
      const refused = resolveGrant(parsed.data.grantId);
      if (refused) return { response: refused };
      if (parsed.data.issue !== undefined && parsed.data.issue !== issue) {
        return {
          response: failure(
            400,
            "ISSUE_NOT_GRANTS",
            `a phase worker moves only its own issue ${issue}, not ${parsed.data.issue}`
          ),
        };
      }
      // The workflow (engine.go's `backward`): a move it refuses changes nothing; one it takes
      // moves the issue to the phase it names, counts the move on the implementer's row and
      // starts the role that works that phase, clearing its completion (`enter`), so that role's
      // next completion is of that phase.
      const { to } = parsed.data;
      const why = backwardRefusal(served(), to);
      if (why !== undefined) return { response: failure(409, "BACKWARD_REFUSED", why) };
      enter(to, "move");
      return { response: json(200, {}) };
    }
    default:
      return { response: json(404, { error: `no route for ${path}` }) };
  }
}

const server = Bun.serve({
  hostname: "127.0.0.1",
  port: Number(portArg),
  async fetch(request) {
    const path = new URL(request.url).pathname;
    let body: unknown;
    if (request.method === "POST") {
      try {
        body = await request.json();
      } catch {
        body = undefined;
      }
    }
    // A route the label's daemon registers none of is answered as its catch-all answers it, 404,
    // whatever case `handle` has for it (`ROUTES`).
    const { response, mintedGrantId, phase } = !serves(ROUTES, request.method, path)
      ? { response: json(404, { error: `no route for ${request.method} ${path}` }) }
      : request.method === "POST"
        ? await handle(path, body)
        : request.method === "GET" && path === "/legion/v1/state"
          ? { response: await stateAnswer() }
          : { response: json(404, { error: `no route for ${request.method} ${path}` }) };
    // What the daemon last saw of the issue's tree move (`treeMovedAt`): a request of the
    // claim's that it took.
    if (request.method === "POST" && response.status < 300) movedAt = new Date().toISOString();
    // A move this request made owes the run's Dispatch its status, written before the answer
    // (`writeStatuses`); a state read has written its own before it built the state.
    if (request.method === "POST") await writeStatuses();
    // A refusal's line carries its code, the daemon's `Failure` code where the answer has one, so
    // a reader of the log can tell the daemon's own answer to a run (HANDOFF_NOT_CURRENT_PHASE)
    // from a body this stand-in could not take at all, whose 400 has none (score.ts's
    // `unscored`). Every line carries the request's method and path, the route health holds a
    // 404 to (health.ts's `routes`). A completion's line carries the phase it was answered on,
    // which no program reads (completion-answers.ts's `DaemonLine`): it is how a person reading
    // the run tells a late completion from one of the role's own phase. A turn outcome's line
    // carries whether its answer failed the claim, which health reads (health.ts's `failedClaim`).
    const refusal: unknown = response.status >= 400 ? await response.clone().json() : undefined;
    const outcome: unknown =
      path === "/legion/v1/claims/turn-outcome" && response.status === 200
        ? await response.clone().json()
        : undefined;
    const line: LogLine = {
      at: new Date().toISOString(),
      method: request.method,
      path,
      status: response.status,
      role,
      ...carried("code", stringField(refusal, "code")),
      ...carried("grantId", stringField(body, "grantId")),
      ...carried("sessionId", stringField(body, "sessionId")),
      ...carried("mintedGrantId", mintedGrantId),
      ...carried("handoffId", stringField(body, "handoffId")),
      ...carried("handoffVersion", numberField(body, "handoffVersion")),
      ...carried("to", stringField(body, "to")),
      ...carried("reason", stringField(body, "reason")),
      ...carried("phase", phase),
      ...carried("failed", booleanField(outcome, "failed")),
    };
    await appendFile(logFile, `${JSON.stringify(line)}\n`);
    return response;
  },
});
// The record is opened here, with one line of its own and before any request can be answered,
// so a run whose agent never reached the daemon leaves a log holding that line and nothing
// else — the reading "no move" — while a log nothing wrote is absent and is the rig error it
// has always been (score.ts's `unscored`). It is written synchronously, between the socket
// opening and the first handler turn, so it is the record's first line whatever arrives next.
/** The record's own name, the one the score knows it by (score.ts's RECORDS). The line itself
 * is the one every record of a run is opened with (run-records.ts's `openingLine`). */
const record = logFile.slice(logFile.lastIndexOf("/") + 1);
appendFileSync(logFile, openingLine(record));
console.log(`listening on http://127.0.0.1:${server.port}`);
