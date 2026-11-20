import type {
  BasisLearnedAt,
  CheckBasis,
  CheckBlock,
  CheckCI,
  CheckPassStands,
  CheckResult,
  CheckResultRecorded,
  IssueCheckResults,
  SpecCheckResult,
} from "@legion/contracts";
import { actorText } from "../actor-text";
import type { DispatchClient } from "../dispatch-http";
import { printable, printableJson } from "../runner-log";
import {
  type CommandContext,
  type DispatchToolResult,
  defined,
  optionalBoolean,
  optionalNumber,
  optionalString,
  optionalStringArray,
  stringArg,
} from "./context";
import { dispatchAnswered, refusalWithCode } from "./failures";
import { observedAsKept } from "./observed-text";

/** How many characters of a reading `--brief` keeps. R2 of the spec sets the number: a reader
 *  who wants the gate alone on an issue holding hundreds of results reads this much of a reading
 *  that stands at none of the compared commits, and the whole of every other one. */
const BRIEF_READING_CHARACTERS = 200;

/** The three reasons a check compared its results with nothing, and the two a result's commit
 *  cannot be placed, in words a reader acts on. A check takes all three and a result all but
 *  no_pull_request_or_branch, so the check's set types this map and answers both. */
const COMPARISON_REASONS: Readonly<Record<NonNullable<CheckBlock["compared_nothing"]>, string>> = {
  no_pull_request_or_branch: "the issue has no pull request and no branch to compare with",
  repository_unreadable: "Dispatch cannot read the repository",
  read_failed: "a read Dispatch made did not answer",
};

/** When Dispatch learned a pull request's head, and which time that is: `read_at` is Dispatch's
 *  own read of the pull request, `updated_at` is GitHub's update time as a webhook carried it. */
function learnedText(learned: BasisLearnedAt): string {
  if (learned.kind === "read_at") return `Dispatch read it ${learned.at}`;
  return `GitHub updated it ${learned.at}`;
}

/** One thing a check compared its results with, as the head line names it. */
function basisText(basis: CheckBasis): string {
  const learned = basis.learned_at === undefined ? "" : ` (${learnedText(basis.learned_at)})`;
  if (basis.kind === "pull_request") {
    const number = basis.pull_request === undefined ? "" : `#${basis.pull_request} `;
    return `the head of pull request ${number}at ${basis.commit ?? "an unnamed commit"}${learned}`;
  }
  if (basis.kind === "branch") return `branch ${basis.branch ?? "unnamed"}${learned}`;
  return `the commit you named, ${basis.commit ?? ""}${learned}`;
}

/** Where a result's commit stands, as the basis it matched names that place. The head line has
 *  already printed each basis whole, so this names it short. */
function basisPlace(basis: CheckBasis): string {
  if (basis.kind === "pull_request") {
    return basis.pull_request === undefined
      ? "the head of a pull request"
      : `the head of pull request #${basis.pull_request}`;
  }
  if (basis.kind === "branch") return `on branch ${basis.branch ?? "unnamed"}`;
  return "the commit you named";
}

/** The first of R4's two facts: where one result's commit stands against what its check compared
 *  with. A Dispatch server that does not answer it says so rather than reading as another
 *  commit. */
function commitStandsText(stands: CheckResult["commit_stands"]): string {
  if (stands === undefined) {
    return "Where that commit stands is not known: this Dispatch server does not say";
  }
  if (stands.answer === "at_basis") {
    return stands.basis === undefined
      ? "That commit is one of the compared commits"
      : `That commit is ${basisPlace(stands.basis)}`;
  }
  if (stands.answer === "other_commit") return "That commit is none of the compared commits";
  // The reason is the server's to give and may be absent, so the answer stands on its own
  // where there is none.
  return stands.reason === undefined
    ? "Where that commit stands is not known"
    : `Where that commit stands is not known: ${COMPARISON_REASONS[stands.reason]}`;
}

/** R4's other fact: whether the check's words moved after the result was recorded. Both versions
 *  are named either way, because they are the handles a reader reads the two wordings with. */
function checkWordsText(check: CheckBlock, result: CheckResult): string {
  const versions = `result version ${result.check_version}, check version ${check.version}`;
  if (result.check_words_moved === undefined) {
    return `Whether the check's words moved is not known (${versions})`;
  }
  return result.check_words_moved
    ? `The check's words moved after this result (${versions})`
    : `The check's words did not move after this result (${versions})`;
}

/** R5 for one check: whether a pass of it stands, and from which roles. */
function passStandsText(pass: CheckPassStands | undefined): string {
  if (pass === undefined)
    return "Whether a pass stands is not known: this Dispatch server does not say";
  if (pass.answer === "yes") {
    const roles = pass.roles ?? [];
    return roles.length === 0
      ? "A pass stands: yes"
      : `A pass stands: yes, from ${roles.join(", ")}`;
  }
  if (pass.answer === "no") return "A pass stands: no";
  return "A pass stands: not known";
}

/** The reading one role recorded, under the line of its result at `indent`. `brief` cuts a reading
 *  whose result stands at none of the compared commits, and leaves every other reading whole: the
 *  reading is the record, and a reader who asked for the gate alone still reads the whole of what
 *  stands. */
function readingLines(result: CheckResult, brief: boolean, indent = "    "): string[] {
  const observed = result.observed;
  if (observed === "") return [`${indent}The role recorded no reading.`];
  const cut = brief && result.commit_stands?.answer !== "at_basis";
  if (cut && observed.length > BRIEF_READING_CHARACTERS) {
    return [
      `${indent}Reading: ${observed.slice(0, BRIEF_READING_CHARACTERS)}… (result ${result.id})`,
    ];
  }
  return [`${indent}Reading: ${observed}`];
}

/** One result, in the order the API served it: the verdict, the role, the whole commit, R4's two
 *  facts, the time, the id, the retraction that stands against it, and the reading. */
function resultLines(check: CheckBlock, result: CheckResult, brief: boolean): string[] {
  const lines = [
    `  - ${result.verdict} by ${result.role} at ${result.commit_sha}. ${commitStandsText(result.commit_stands)}. ${checkWordsText(check, result)}. Recorded ${result.created_at}. Result ${result.id}.`,
  ];
  const retracted = result.retracted;
  if (retracted !== undefined) {
    lines.push(
      `    Retracted by ${actorText(retracted.actor)} at ${retracted.created_at}. The reason: ${retracted.reason}`
    );
  }
  return [...lines, ...readingLines(result, brief)];
}

/** A check CI proves, which Dispatch marks with `ci`: no role records its result, so the check and
 *  its one result read as one line. A derived result names its verdict, its commit, the producer
 *  `ci`, the run and the conclusion that decided it, and where that commit stands, as a recorded
 *  result's line says it; a check the run did not decide says why, in the sentence Dispatch wrote.
 *  The check's words end the line. Whether a pass stands follows, from the same `pass_stands` and
 *  in the same words as a recorded check's head line, so the gate reads both kinds alike. The
 *  pull-request body line Dispatch wrote for the check follows, which the implementer copies, and
 *  then the run's reading: its conclusion, output and URL. */
function ciCheckLines(check: CheckBlock, ci: CheckCI, brief: boolean): string[] {
  const passStandsLine = `  ${passStandsText(check.pass_stands)}.`;
  const pullRequestLine = `  Pull request line: ${ci.line}`;
  const derived = check.results?.[0];
  if (derived === undefined) {
    return [
      `- ${check.block_id} (${check.when}): no result: ${ci.detail} ${check.text}`,
      passStandsLine,
      pullRequestLine,
    ];
  }
  const stands = commitStandsText(derived.commit_stands);
  return [
    `- ${check.block_id} (${check.when}): ${derived.verdict} at ${derived.commit_sha} by ${derived.role} (CI run ${ci.run}, ${ci.case}), ${stands.charAt(0).toLowerCase()}${stands.slice(1)}. ${check.text}`,
    passStandsLine,
    pullRequestLine,
    ...readingLines(derived, brief, "  "),
  ];
}

/** One check: a head line naming what it compared with and whether a pass of it stands, the
 *  check's own words once, then every result recorded against it. */
function checkLines(check: CheckBlock, brief: boolean): string[] {
  if (check.ci !== undefined) return ciCheckLines(check, check.ci, brief);
  const bases = check.compared_with ?? [];
  const reason = check.compared_nothing;
  const compared =
    bases.length > 0
      ? `Compared with ${bases.map(basisText).join(", ")}`
      : reason === undefined
        ? "Compared with nothing, and this Dispatch server does not say why"
        : `Compared with nothing: ${COMPARISON_REASONS[reason]}`;
  const results = check.results ?? [];
  return [
    `- ${check.block_id} (${check.when}). ${compared}. ${passStandsText(check.pass_stands)}.`,
    `  The check's words: ${check.text}`,
    ...(results.length === 0
      ? ["  No result is recorded for this check."]
      : results.flatMap((result) => resultLines(check, result, brief))),
  ];
}

/** What `dispatch results` prints: each check of the issue's Verification document, what Dispatch
 *  compared its results with, whether a pass of it stands, its own words once, and every result
 *  recorded against it in the order the API serves them. A check CI proves prints its derived
 *  result, or why it has none, as one line, then whether a pass of it stands (`ciCheckLines`). A
 *  check with no result says so, and an issue with no Verification document says that instead of
 *  printing an empty list: Dispatch answers `verification_artifact_id` "" for such an issue, never
 *  null, so that is what is compared. */
function formatCheckResults(results: IssueCheckResults, brief = false): string {
  if (results.verification_artifact_id === "") {
    return `${results.issue} has no Verification document, so it has no checks. Write one (verification.md) with a check block per thing that must be proved.`;
  }
  const checks = results.checks ?? [];
  if (checks.length === 0) {
    return `${results.issue}'s Verification document holds no check blocks.`;
  }
  const blocks = checks.map((check) => checkLines(check, brief).join("\n"));
  return [`Checks of ${results.issue}:`, ...blocks].join("\n\n");
}

/**
 * `dispatch result --project`: a CI job's record of one spec check, printed as the stored
 * in-toto statement. The job sends only observed text that the result can retain.
 */
export async function specCheckResultCommand(
  client: DispatchClient,
  args: Record<string, unknown>
): Promise<DispatchToolResult> {
  const project = stringArg(args, "project");
  const observed = optionalString(args, "observed");
  const stored = await client.recordSpecCheckResult(project, {
    commit: stringArg(args, "commit"),
    check: stringArg(args, "check"),
    verdict: stringArg(args, "verdict"),
    ...(observed === undefined ? {} : { observed: observedAsKept(observed) }),
  });
  const { predicate } = stored.attestation;
  return {
    text: JSON.stringify(stored, null, 2),
    details: {
      project,
      result: stored.id,
      seq: stored.seq,
      check: predicate.check,
      verdict: predicate.verdict,
    },
  };
}

/** One stored result of a check of the spec files, rendered safely for a runner log. */
function specCheckResultLine(result: SpecCheckResult): string {
  const { predicate, subject } = result.attestation;
  const leg = predicate.leg === "" ? "no leg" : `leg ${printable(predicate.leg)}`;
  const commit = subject?.[0]?.digest?.["gitCommit"] ?? "an unnamed commit";
  return `- #${result.seq} ${printable(predicate.check)}: ${printable(predicate.verdict)}. Job ${printable(predicate.job)}, ${leg}, run ${printable(predicate.run.run_id)} attempt ${predicate.run.attempt}, commit ${printable(commit)}.`;
}

/** `dispatch results --project`: stored spec-check results at one commit. */
async function specCheckResultsCommand(
  client: DispatchClient,
  args: Record<string, unknown>
): Promise<DispatchToolResult> {
  const project = stringArg(args, "project");
  const commit = stringArg(args, "commit");
  const check = optionalString(args, "check");
  const results = (await client.specCheckResults(project, commit, check)).results ?? [];
  const what = check === undefined ? project : `check ${printable(check)} of ${project}`;
  const text =
    optionalBoolean(args, "json") === true
      ? printableJson(JSON.stringify(results, null, 2))
      : results.length === 0
        ? `No result of ${what} is stored at ${printable(commit)}.`
        : [
            `Results of ${what} at ${printable(commit)}, newest first:`,
            ...results.map(specCheckResultLine),
          ].join("\n");
  return { text, details: { project, commit, results: results.length } };
}

/** Whether the CLI's `--dry-run` asks Dispatch what one issue result would answer. A CI result
 * records a real run, so its dry run sends nothing. */
export function dryRunAsksDispatch(tool: string, args: Readonly<Record<string, unknown>>): boolean {
  return (
    tool === "dispatch_result" && args["retract"] === undefined && args["project"] === undefined
  );
}

/** `dispatch result`: record one result of one check of an issue's Verification document, or,
 *  with `project`, a CI job's result of a check of the spec files. */
export async function resultCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { args, client, actor } = context;
  if (optionalString(args, "project") !== undefined) {
    return await specCheckResultCommand(client, args);
  }
  const retract = optionalString(args, "retract");
  if (retract !== undefined) {
    const retraction = await client.retractResult(retract, {
      actor,
      reason: stringArg(args, "reason"),
    });
    return {
      text: `Retracted result ${retraction.result_id}. ${actorText(retraction.actor)} retracted it at ${retraction.created_at}. The reason: ${retraction.reason}\n\nThe result stays readable, and it proves nothing from now on. Record a new result where the check still needs one.`,
      details: {
        result: retraction.result_id,
        reason: retraction.reason,
        created_at: retraction.created_at,
      },
    };
  }
  const issue = stringArg(args, "issue");
  // Each optional field is omitted rather than passed as `undefined`: the input's optional
  // properties are exact (`exactOptionalPropertyTypes`, which claude-envoy compiles this
  // file under), so `{ surface: undefined }` is not the same shape as `{}`.
  const optional = {
    ...defined("surface", optionalString(args, "surface")),
    ...defined("run", optionalString(args, "run")),
    ...defined("observed", optionalString(args, "observed")),
    ...defined("negative_control", optionalString(args, "negative_control")),
    ...defined("failed", optionalNumber(args, "failed")),
    ...defined("skipped", optionalNumber(args, "skipped")),
    ...defined("excluded", optionalNumber(args, "excluded")),
    ...defined("screenshots", optionalStringArray(args, "screenshots")),
  };
  const input = {
    actor,
    check: stringArg(args, "check"),
    role: stringArg(args, "role"),
    verdict: stringArg(args, "verdict") as "pass" | "fail",
    commit: stringArg(args, "commit"),
    ...optional,
  };
  if (context.input.dryRun === true) {
    const dry = await client.dryRunResult(issue, input);
    return {
      text: `Nothing was recorded: this is a dry run.\n\nPull request body line:\n${dry.line}`,
      details: { issue, check: input.check, verdict: input.verdict, line: dry.line },
    };
  }
  let recorded: CheckResultRecorded;
  try {
    recorded = await client.recordResult(issue, input);
  } catch (error) {
    // CHECK_CONTENDED is the one refusal of this route that asks for nothing but the same
    // call again: a document version reworded the check at both of the write's attempts, so
    // nothing was recorded and the next call resolves the check afresh. Every other refusal
    // it can give asks the caller to change something first, and reaches them as Dispatch
    // wrote it. Untouched, this one reaches them as prose with its code hidden, which reads
    // as a failure to work out rather than a write to repeat.
    if (dispatchAnswered(error, 409) && error.code === "CHECK_CONTENDED") {
      throw refusalWithCode(
        error,
        "the check moved twice while this result was being written, and nothing was recorded: run the same dispatch result command again"
      );
    }
    throw error;
  }
  return {
    text: `Recorded ${recorded.result.verdict} for check ${recorded.result.check_block_id} of ${recorded.result.check_issue_key} at ${recorded.result.commit_sha}, as ${recorded.result.role}.\n\nPull request body line:\n${recorded.line}`,
    details: {
      issue,
      result: recorded.result.id,
      check: recorded.result.check_block_id,
      verdict: recorded.result.verdict,
      line: recorded.line,
    },
  };
}

/** `dispatch results`: each check of an issue's Verification document with every result recorded
 *  against it, or, with `project`, the results CI jobs stored for checks of the spec files. */
export async function resultsCommand(context: CommandContext): Promise<DispatchToolResult> {
  const { args, client } = context;
  if (optionalString(args, "project") !== undefined) {
    return await specCheckResultsCommand(client, args);
  }
  const issue = stringArg(args, "issue");
  const at = optionalString(args, "at");
  const brief = optionalBoolean(args, "brief") === true;
  const results = await client.issueResults(issue, at);
  const checks = results.checks ?? [];
  return {
    text: formatCheckResults(results, brief),
    details: {
      issue,
      checks: checks.length,
      results: checks.reduce((total, check) => total + (check.results ?? []).length, 0),
    },
  };
}
