import { readFileSync } from "node:fs";
import { join } from "node:path";
import { z } from "zod";
import { holdsWord } from "./pattern.ts";

/**
 * The map of old names to new names: one entry per name this repository defines that Dispatch
 * text can hold, and the point from which the new name is true where Dispatch text says it.
 * The names of the other repositories come at run time from `--extra-map` (INFRA-2017's plan,
 * P7): their rows name the deployment's accounts and objects, which this public repository does
 * not hold. This file is one of the places the word may stand in the repository (R1, kind 4).
 *
 * `from` says when an entry applies to a project's text:
 *
 * - `{contract: n}`: a name the release of cybermonklabs/legion carries (a notice kind, a table,
 *   a skill's file name, a test's, a script's, a file's or an identifier's name). Its new name is
 *   true in a project whose daemon reads `contract.daemon` at or above `n`, or that the run names
 *   as having no Legion daemon (spec version 19). Every name of this repository's map but the
 *   Dispatch API names is gated so, at the contract this checkout records; `park_child` at the
 *   contract that removed it.
 * - `{dispatchHolds: "merge"}`: a name of Dispatch's API. It applies once the running Dispatch
 *   build (`/healthz` `commit`) holds the merge commit the run names.
 * - `"merged"`: kept for the rows `--extra-map` gives (the other repositories' names, each
 *   written into that file only once its condition holds), which apply now.
 */
export type From = { readonly contract: number } | { readonly dispatchHolds: "merge" } | "merged";

export interface MapEntry {
  readonly old: string;
  readonly new: string;
  /** A name a program reads (a field, a kind, a file), which the new text spells exactly; false
   *  for words of prose such as a heading. */
  readonly codeName: boolean;
  readonly from: From;
}

/** Where the daemon API contract of the commit this tool runs from is recorded. */
export const CONTRACT_FILE = "packages/contracts/fixtures/daemon-api/version.json";

/** The daemon contract that removed the child operation `park_child` (cybermonklabs/legion#4573). */
export const CHILD_OPERATION_REMOVED = 205;

const ContractFile = z.looseObject({ daemonApiVersion: z.int().min(1) });

/** The daemon API contract number of the checkout at `root`, read from its own fixture, never
 *  written here: the pull request that renames the daemon's names raises it. */
export function readContract(root: string): number {
  const text = readFileSync(join(root, CONTRACT_FILE), "utf8");
  const parsed = ContractFile.safeParse(JSON.parse(text));
  if (!parsed.success) throw new Error(`${CONTRACT_FILE} holds no daemonApiVersion`);
  return parsed.data.daemonApiVersion;
}

const dispatchMerge = { dispatchHolds: "merge" } as const;

/** The names of Dispatch's API this change renames, which wait for Dispatch's build. */
const DISPATCH_API_NAMES: readonly (readonly [string, string])[] = [
  ["priority_work_parked", "priority_work_in_backlog_or_icebox"],
  ["PriorityWorkParked", "PriorityWorkInBacklogOrIcebox"],
];

/**
 * Every other old name this change renames that Dispatch text can hold, with its new name: the
 * pull request's list of renamed files, wire and stored names, identifiers and tests, and the
 * skill files, scripts and variables it renames. Each is a name the release carries, so each
 * applies from the new contract. A file is listed by its base name, which also covers every path
 * that ends in it.
 */
const RELEASE_NAMES: readonly (readonly [string, string, boolean?])[] = [
  // The kept decisions: files.
  ["parked_decision.go", "kept_decision.go"],
  ["parked_decision_test.go", "kept_decision_test.go"],
  ["parked_owed_start_test.go", "backlog_owed_start_test.go"],
  // The kept decisions: wire and stored names.
  ["parked_decisions", "kept_decisions"],
  ["parked_decisions_issue", "kept_decisions_issue"],
  ["parked_decisions_pkey", "kept_decisions_pkey"],
  ["parked_decisions_issue_fkey", "kept_decisions_issue_fkey"],
  ["parked_decisions_claimed_by_fkey", "kept_decisions_claimed_by_fkey"],
  ["parked-decision", "kept-decision"],
  // The kept decisions: identifiers.
  ["ParkedDecision", "KeptDecision"],
  ["AddParkedDecision", "KeepDecision"],
  ["ParkedDecisions", "KeptDecisionsOf"],
  ["RelayParkedDecision", "RelayKeptDecision"],
  ["RelayNotParked", "RelayBackInWorkflow"],
  ["ParkedDecisionNotice", "KeptDecisionNotice"],
  ["ParkedDecisionTask", "UnclaimedKeptTask"],
  ["relayParkedDecision", "relayKeptDecision"],
  ["parkDecision", "relaySpecDecision"],
  ["parkedDecisionSource", "keptDecisionSource"],
  // The kept decisions: tests.
  [
    "TestTheMachinerysNoticeToAStoppedTreeWritesNoParkedDecision",
    "TestTheMachinerysNoticeToAStoppedTreeKeepsNoDecision",
  ],
  [
    "TestAParkedDecisionIsPublishedOnTheControllerTopic",
    "TestAKeptDecisionIsPublishedOnTheControllerTopic",
  ],
  [
    "TestAPersonsAnswerOnAParkedTreeGoesToTheController",
    "TestAPersonsAnswerOnATreeThatDoesNotRunGoesToTheController",
  ],
  [
    "TestAPersonsReplyOnAParkedChildNamesItsRoot",
    "TestAPersonsReplyOnAChildOfATreeThatDoesNotRunNamesItsRoot",
  ],
  [
    "TestAnOrphanedParkedChildTakesTheDecisionKeptOnIt",
    "TestAnOrphanedChildInBacklogTakesTheDecisionKeptOnIt",
  ],
  [
    "TestAParkedDecisionKeepsItsDecisionThroughTheRow",
    "TestAKeptDecisionKeepsItsDecisionThroughTheRow",
  ],
  [
    "TestAParkedDecisionKeepsTheNewestPerRefAndIsForgottenOnlyWhereItWasNamed",
    "TestAKeptDecisionKeepsTheNewestPerRefAndIsForgottenOnlyWhereItWasNamed",
  ],
  [
    "TestParkedDecisionsArriveWith20261101163106AndGoWithTheirIssue",
    "TestKeptDecisionsArriveWith20261101163106AndGoWithTheirIssue",
  ],
  [
    "TestAStartOwedBeforeAParkIsFinishedRatherThanCarriedAcrossIt",
    "TestAStartOwedBeforeAMoveToBacklogIsFinishedRatherThanCarriedAcrossIt",
  ],
  // The daemon's status words, the held reason and the child route.
  ["tree_parked", "tree_in_backlog_or_icebox"],
  ["/legion/v1/children/park", "/legion/v1/children/{op}"],
  ["You can park the tree.", "You can move the tree's root to backlog.", false],
  ["HeldTreeParked", "HeldTreeInBacklogOrIcebox"],
  ["releaseParkedClaim", "releaseOutOfWorkflowClaim"],
  ["releaseParkedClaims", "releaseOutOfWorkflowClaims"],
  ["parkedWakes", "delayedWakes"],
  ["parkedWakeBound", "delayedWakeBound"],
  ["parkedWait", "delayedWait"],
  ["parkedLaunch", "delayedLaunch"],
  [
    "ancestry-held-past-a-parked-root-on-its-chain",
    "ancestry-held-past-a-backlog-root-on-its-chain",
  ],
  [
    "TestARootHeldPastAParkedRootOnItsChainQueuesOnceTheTreeLingers",
    "TestARootHeldPastARootInBacklogOnItsChainQueuesOnceTheTreeLingers",
  ],
  [
    "TestAListedChildHoldsItsParentAndAParkedOneDoesNot",
    "TestAListedChildHoldsItsParentAndOneInBacklogDoesNot",
  ],
  [
    "TestReconcileReentersAParkedChildSetBackToTodoInABootSummary",
    "TestReconcileReentersAChildOutOfTheWorkflowSetBackToTodoInABootSummary",
  ],
  [
    "TestAParkAndATodoWhileThePromotionsWriteIsOnItsWayRunTheTreeAgain",
    "TestABacklogAndATodoWhileThePromotionsWriteIsOnItsWayRunTheTreeAgain",
  ],
  [
    "TestControllerStatusParksTheRecordWithoutWaitingForItsDispatchEvent",
    "TestControllerStatusMovesTheRecordToBacklogWithoutWaitingForItsDispatchEvent",
  ],
  [
    "TestWaveReleaseReentersAParkedChildFromItsOwnWrite",
    "TestWaveReleaseReentersABacklogChildFromItsOwnWrite",
  ],
  [
    "TestARootHeldPastAParkedRootOnItsChainTakesNoSlot",
    "TestARootHeldPastARootInBacklogOnItsChainTakesNoSlot",
  ],
  ["TestBootReleasesParkedClaims", "TestBootReleasesOutOfWorkflowClaims"],
  [
    "TestAHookPromptWaitsOutTheParkedWakeOfItsReleasedPod",
    "TestAHookPromptWaitsOutTheDelayedWakeOfItsReleasedPod",
  ],
  [
    "TestAHookPromptFollowsAnOperatorsResumeOfItsParkedWakeWithinTheBound",
    "TestAHookPromptFollowsAnOperatorsResumeOfItsDelayedWakeWithinTheBound",
  ],
  [
    "TestARelaunchParkedOverALiveProcessHoldsNoRow",
    "TestARelaunchDelayedOverALiveProcessHoldsNoRow",
  ],
  [
    "TestAPersonsApprovalOfAParkedRootsSpecIsQueuedByTheDaemonAgainstScratchDispatch",
    "TestAPersonsApprovalOfABacklogRootsSpecIsQueuedByTheDaemonAgainstScratchDispatch",
  ],
  [
    "TestAParkedChildsCatchUpEndsAfterAPersonMovesItToARunningStatus",
    "TestABacklogChildsCatchUpEndsAfterAPersonMovesItToARunningStatus",
  ],
  [
    "TestACancelRacingAParkLeavesNoResumeAboveTheClose",
    "TestACancelRacingAMoveToBacklogLeavesNoResumeAboveTheClose",
  ],
  [
    "TestAPersonsApprovalOfAnUnrecordedParkedRootsSpecIsQueuedByTheDaemon",
    "TestAPersonsApprovalOfAnUnrecordedBacklogOrIceboxRootsSpecIsQueuedByTheDaemon",
  ],
  [
    "TestOnlyAPersonsApprovalOfAParkedLabelledRootsPrimaryDocumentQueuesIt",
    "TestOnlyAPersonsApprovalOfABacklogOrIceboxLabelledRootsPrimaryDocumentQueuesIt",
  ],
  [
    "TestAPersonsApprovalOfARecordedParkedRootsSpecQueuesItAndRecordsItQuietly",
    "TestAPersonsApprovalOfARecordedBacklogRootsSpecQueuesItAndRecordsItQuietly",
  ],
  [
    "TestACarriedCopyOfAParkedRootsGateTakesTheApprovalWhileTheOwnerIsQueued",
    "TestACarriedCopyOfABacklogRootsGateTakesTheApprovalWhileTheOwnerIsQueued",
  ],
  // The controller's skill files, the devbox proof and its lib files, the solution page.
  ["park-asks.md", "backlog-asks.md"],
  ["parked-decisions.md", "kept-decisions.md"],
  ["parked-decision-devbox.sh", "kept-decision-devbox.sh"],
  ["parked-decision-cleanup.test.ts", "kept-decision-cleanup.test.ts"],
  ["parked-decision-teardown.test.ts", "kept-decision-teardown.test.ts"],
  ["parked-decision-verdict.jq", "kept-decision-verdict.jq"],
  ["parked-decision-verdict.test.ts", "kept-decision-verdict.test.ts"],
  [
    "dispatch-blocked-by-replaces-the-whole-list-and-a-park-stops-an-issue-without-finishing-it-INFRA-225.md",
    "dispatch-blocked-by-replaces-the-whole-list-and-a-move-to-backlog-stops-an-issue-without-finishing-it-INFRA-225.md",
  ],
  // Variables.
  ["PARKED_DECISION_EVIDENCE_DIR", "KEPT_DECISION_EVIDENCE_DIR"],
  ["PARKED_DECISION_UNTIL", "KEPT_DECISION_UNTIL"],
  ["DOCS_RESIDENCY_PARK_COMMIT_TRIAL", "DOCS_RESIDENCY_WEDGED_COMMIT_TRIAL"],
];

/** The map this repository holds, with `contract` the daemon API contract of this checkout. */
export function builtInMap(contract: number): readonly MapEntry[] {
  const fromContract = { contract } as const;
  return [
    {
      old: "park_child",
      new: "the owning architect's `backlog` status write on the child in Dispatch",
      codeName: true,
      from: { contract: CHILD_OPERATION_REMOVED },
    },
    ...DISPATCH_API_NAMES.map(([old, renamed]) => ({
      old,
      new: renamed,
      codeName: true,
      from: dispatchMerge,
    })),
    ...RELEASE_NAMES.map(([old, renamed, codeName = true]) => ({
      old,
      new: renamed,
      codeName,
      from: fromContract,
    })),
  ];
}

/** A line of an `--extra-map` file that is not an entry. */
export class MapFileError extends Error {
  override readonly name = "MapFileError";

  constructor(
    readonly line: number,
    reason: string
  ) {
    super(`--extra-map line ${line}: ${reason}`);
  }
}

const merged = "merged" as const;

/** Why a line is not an entry; `parseExtraMap` names the line. */
class NotAnEntry extends Error {}

function notAnEntry(reason: string): never {
  throw new NotAnEntry(reason);
}

function fromOf(value: unknown): From {
  if (value === "merged") return merged;
  if (typeof value !== "object" || value === null) notAnEntry('from is not "merged" or an object');
  if (Object.keys(value).length !== 1) notAnEntry("from names more than one rule");
  if ("contract" in value) {
    const contract = value.contract;
    if (typeof contract !== "number" || !Number.isInteger(contract) || contract < 1) {
      notAnEntry("from.contract is not a positive integer");
    }
    return { contract };
  }
  if ("dispatchHolds" in value && value.dispatchHolds === "merge") return dispatchMerge;
  return notAnEntry('from is none of {"contract": n}, {"dispatchHolds": "merge"} and "merged"');
}

function entryOf(value: unknown): MapEntry {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    notAnEntry("the line is not a JSON object");
  }
  const unknownKey = Object.keys(value).find(
    (key) => !["old", "new", "codeName", "from"].includes(key)
  );
  if (unknownKey !== undefined) notAnEntry(`unknown key ${JSON.stringify(unknownKey)}`);
  const old = "old" in value ? value.old : undefined;
  const renamed = "new" in value ? value.new : undefined;
  const codeName = "codeName" in value ? value.codeName : undefined;
  if (typeof old !== "string" || old.trim() === "") notAnEntry("old is not a name");
  if (typeof renamed !== "string" || renamed.trim() === "") notAnEntry("new is not a name");
  if (typeof codeName !== "boolean") notAnEntry("codeName is not true or false");
  return { old, new: renamed, codeName, from: fromOf("from" in value ? value.from : undefined) };
}

/**
 * The entries of an `--extra-map` file: JSON Lines, one entry
 * `{"old": …, "new": …, "codeName": true|false, "from": …}` per line, blank lines skipped. A
 * line that is not an entry refuses the whole file, naming the line, so no run reads half a map.
 */
export function parseExtraMap(text: string): MapEntry[] {
  const entries: MapEntry[] = [];
  const lines = text.split("\n");
  for (const [index, raw] of lines.entries()) {
    const line = raw.trim();
    if (line === "") continue;
    let value: unknown;
    try {
      value = JSON.parse(line);
    } catch {
      throw new MapFileError(index + 1, "the line is not JSON");
    }
    try {
      entries.push(entryOf(value));
    } catch (error) {
      if (error instanceof NotAnEntry) throw new MapFileError(index + 1, error.message);
      throw error;
    }
  }
  return entries;
}

/** What a project has reached, as the run read it: whether its daemon reads at or above a
 *  contract (or the run names it as having none), and whether Dispatch's build holds the merge. */
export interface Reach {
  daemonAtLeast(contract: number): boolean;
  readonly dispatchHoldsMerge: boolean;
}

/** Whether an entry's new name is true for a project's text now. */
export function applies(entry: MapEntry, reach: Reach): boolean {
  if (entry.from === "merged") return true;
  if ("contract" in entry.from) return reach.daemonAtLeast(entry.from.contract);
  return reach.dispatchHoldsMerge;
}

const NAME_CHARACTER = /[A-Za-z0-9_]/;

/**
 * The entry whose old name covers the text from `start` to `end`, the longest when more than one
 * does, or null. An old name counts only where it is a whole name: the characters on either side
 * of it are not letters, digits or `_`, so `park_child` inside `park_children` names nothing.
 */
export function nameAt(
  text: string,
  start: number,
  end: number,
  entries: readonly MapEntry[]
): MapEntry | null {
  let best: MapEntry | null = null;
  for (const entry of entries) {
    if (best !== null && entry.old.length <= best.old.length) continue;
    const earliest = Math.max(0, end - entry.old.length);
    for (let at = text.indexOf(entry.old, earliest); at !== -1 && at <= start; ) {
      const after = at + entry.old.length;
      const bounded =
        !NAME_CHARACTER.test(text.charAt(at - 1)) && !NAME_CHARACTER.test(text.charAt(after));
      if (after >= end && bounded) {
        best = entry;
        break;
      }
      at = text.indexOf(entry.old, at + 1);
    }
  }
  return best;
}

/** A Go test's name: `Test` and a name that goes on with a capital, a digit or `_`. */
const TEST_NAME = /\bTest[A-Z0-9_][A-Za-z0-9_]*/g;

/**
 * The Go test's name that covers the text from `start` to `end`, or null. The map lists the tests
 * this change renames; a test's name that holds the word and that no row names has no new name a
 * program can tell, so the tool keeps it (`code-name-not-yet`).
 */
export function testNameAt(text: string, start: number, end: number): string | null {
  for (const match of text.matchAll(TEST_NAME)) {
    if (match.index <= start && end <= match.index + match[0].length && holdsWord(match[0])) {
      return match[0];
    }
  }
  return null;
}

/** Every old name of the map that `text` holds as a whole name, each once, sorted. */
export function renamedNamesIn(text: string, entries: readonly MapEntry[]): string[] {
  const named = new Set<string>();
  for (const entry of entries) {
    for (let at = text.indexOf(entry.old); at !== -1; at = text.indexOf(entry.old, at + 1)) {
      const after = at + entry.old.length;
      if (!NAME_CHARACTER.test(text.charAt(at - 1)) && !NAME_CHARACTER.test(text.charAt(after))) {
        named.add(entry.old);
        break;
      }
    }
  }
  return [...named].sort();
}
