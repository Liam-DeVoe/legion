import type {
  BasisLearnedAt,
  CheckBasis,
  CheckBlock,
  CheckCI,
  CheckPassStands,
  CheckResult,
  IssueCheckResults,
} from "@legion/contracts";
import { checkWithNewerFields } from "@legion/contracts";
import { actorText } from "../actor-text";
import { printableStrings } from "../runner-log";

// check-results.ts is what `dispatch results --issue` prints: each check of an issue's
// Verification document, what Dispatch compared its results with, whether a pass of it stands,
// and every result recorded against it (formatCheckResults).

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
  if (basis.kind === "branch_head") {
    return `the head of branch ${basis.branch ?? "unnamed"} at ${basis.commit ?? "an unnamed commit"}`;
  }
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
  if (basis.kind === "branch_head") return `at the head of branch ${basis.branch ?? "unnamed"}`;
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

/** What one subject of a check holds where no pass stands there. */
function subjectReasonText(reason: string | undefined): string {
  if (reason === "fail") return "a fail";
  if (reason === "pass_and_fail") return "a pass and a fail of CI";
  return "no result";
}

/** Each subject of a check where no pass stands, with what it holds, or "" where there is none. */
function openSubjectsText(pass: CheckPassStands): string {
  return (pass.subjects ?? [])
    .filter((subject) => subject.answer !== "yes")
    .map((subject) => `${basisText(subject.basis)} holds ${subjectReasonText(subject.reason)}`)
    .join("; ");
}

/** R5 for one check: whether a pass of it stands, and from which roles; where none does, each
 *  head a pass must stand at that holds none, and what it holds instead. */
function passStandsText(pass: CheckPassStands | undefined): string {
  if (pass === undefined)
    return "Whether a pass stands is not known: this Dispatch server does not say";
  if (pass.answer === "yes") {
    const roles = pass.roles ?? [];
    return roles.length === 0
      ? "A pass stands: yes"
      : `A pass stands: yes, from ${roles.join(", ")}`;
  }
  const open = openSubjectsText(pass);
  if (pass.answer === "no")
    return open === "" ? "A pass stands: no" : `A pass stands: no, as ${open}`;
  if (open !== "") return `A pass stands: not known, as ${open}`;
  return pass.reason === "pass_and_fail"
    ? "A pass stands: not known, as CI holds a pass and a fail at one commit"
    : "A pass stands: not known";
}

/** The line of the reading one role recorded, under the line of its result at `indent`. The
 *  reading is one line however many it holds: its line breaks are written as `\n`, as every string
 *  of the answer is (`formatCheckResults`), so no line of a CI job's log reads as a line of this
 *  command's own. `brief` cuts a reading whose result stands at none of the compared commits, and
 *  leaves every other reading whole: the reading is the record, and a reader who asked for the
 *  gate alone still reads the whole of what stands. */
function readingLine(result: CheckResult, brief: boolean, indent = "    "): string {
  const observed = result.observed;
  if (observed === "") return `${indent}The role recorded no reading.`;
  const cut = brief && result.commit_stands?.answer !== "at_basis";
  if (cut && observed.length > BRIEF_READING_CHARACTERS) {
    return `${indent}Reading: ${observed.slice(0, BRIEF_READING_CHARACTERS)}… (result ${result.id})`;
  }
  return `${indent}Reading: ${observed}`;
}

/** One result, in the order the API served it: the verdict, the role, the branch a CI job's
 *  result was pushed on, the whole commit, R4's two facts, the time, the id, the retraction that
 *  stands against it, and the reading; then, for a result that counts for nothing, the reason
 *  Dispatch served in `not_counted`. */
function resultLines(check: CheckBlock, result: CheckResult, brief: boolean): string[] {
  const branch = result.branch === undefined ? "" : ` on branch ${result.branch}`;
  const lines = [
    `  - ${result.verdict} by ${result.role}${branch} at ${result.commit_sha}. ${commitStandsText(result.commit_stands)}. ${checkWordsText(check, result)}. Recorded ${result.created_at}. Result ${result.id}.`,
  ];
  const retracted = result.retracted;
  if (retracted !== undefined) {
    lines.push(
      `    Retracted by ${actorText(retracted.actor)} at ${retracted.created_at}. The reason: ${retracted.reason}`
    );
  }
  lines.push(readingLine(result, brief));
  const notCounted = result.not_counted ?? "";
  if (notCounted !== "") lines.push(`    It counts for nothing: ${notCounted}`);
  return lines;
}

/** What a derived result's id begins with, before its check run's own id, as Dispatch writes it
 *  (`ciResultIDPrefix` in packages/envoy/internal/dispatch/api/results_ci.go). */
const CI_RESULT_ID_PREFIX = "ci-run:";

/** A check CI proves, which Dispatch marks with `ci`: no role records its result. Its derived
 *  result, the one whose id begins with `ci-run:` wherever the API serves it, reads as one line
 *  with the check: its verdict, its commit, the producer `ci`, the run and the conclusion that
 *  decided it, and where that commit stands, as a recorded result's line says it; a check the run
 *  did not decide says why, in the sentence Dispatch wrote. The check's words end the line.
 *  Whether a pass stands follows, from the same `pass_stands` and in the same words as a recorded
 *  check's head line, so the gate reads both kinds alike. The pull-request body line Dispatch
 *  wrote for the check follows, which the implementer copies, except for an issue with no pull
 *  request head (`no_head`), which has no body to write it in; then the run's reading: its
 *  conclusion, output and URL. Each result the CI job recorded follows, in the order the API
 *  serves them, as a recorded result prints (`resultLines`). */
function ciCheckLines(check: CheckBlock, ci: CheckCI, brief: boolean): string[] {
  const results = check.results ?? [];
  const derived = results.find((result) => result.id.startsWith(CI_RESULT_ID_PREFIX));
  const recorded = results
    .filter((result) => result !== derived)
    .flatMap((result) => resultLines(check, result, brief));
  const passStandsLine = `  ${passStandsText(check.pass_stands)}.`;
  const pullRequestLine = ci.case === "no_head" ? [] : [`  Pull request line: ${ci.line}`];
  if (derived === undefined) {
    // A pass the CI job recorded may stand where the run derives nothing, so the head line says
    // so rather than "no result" above a line that says a pass stands.
    const head =
      recorded.length === 0
        ? "no result"
        : `its CI job's results below, and CI run ${ci.run} derives none`;
    return [
      `- ${check.block_id} (${check.when}): ${head}: ${ci.detail} ${check.text}`,
      passStandsLine,
      ...pullRequestLine,
      ...recorded,
    ];
  }
  const stands = commitStandsText(derived.commit_stands);
  return [
    `- ${check.block_id} (${check.when}): ${derived.verdict} at ${derived.commit_sha} by ${derived.role} (CI run ${ci.run}, ${ci.case}), ${stands.charAt(0).toLowerCase()}${stands.slice(1)}. ${check.text}`,
    passStandsLine,
    ...pullRequestLine,
    readingLine(derived, brief, "  "),
    ...recorded,
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
 *  result, or why it has none, as one line, then whether a pass of it stands, then each result
 *  its CI job recorded (`ciCheckLines`). A check with no result says so, and an issue with no
 *  Verification document says that instead of printing an empty list: Dispatch answers
 *  `verification_artifact_id` "" for such an issue, never null, so that is what is compared.
 *
 *  Every string of the answer is text this command did not write: a CI job's log, a role's
 *  reading, a check's words, a reason. Each is read as runnerlog.Printable writes it
 *  (`printableStrings`, runner-log.ts), so it prints inside a line of this command's own, its
 *  line breaks as `\n`, and no line it prints holds a "##[": no text from outside starts a line,
 *  and none reads as a line this command wrote, such as a check's head line or its
 *  `A pass stands:` line. */
export function formatCheckResults(answer: IssueCheckResults, brief = false): string {
  const results = printableStrings(answer);
  if (results.verification_artifact_id === "") {
    return `${results.issue} has no Verification document, so it has no checks. Write one (verification.md) with a check block per thing that must be proved.`;
  }
  const checks = results.checks ?? [];
  if (checks.length === 0) {
    return `${results.issue}'s Verification document holds no check blocks.`;
  }
  const blocks = checks.map((check) => checkLines(checkWithNewerFields(check), brief).join("\n"));
  return [`Checks of ${results.issue}:`, ...blocks].join("\n\n");
}
