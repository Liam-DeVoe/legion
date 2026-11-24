import { ISSUE_UPDATE_PATCH_FIELDS, issueProject } from "@legion/contracts";
import { commandLine } from "../dispatch-command";
import { DispatchGatewayError, DispatchServiceError } from "../dispatch-http";
import { messageFor } from "../errors";
import { optionalString } from "./context";

/**
 * A Dispatch refusal carrying its own code in the message the host shows: the code
 * (ISSUE_CLAIMED, CLAIM_CONTENDED, EXTERNAL_LINK_TAKEN, ...) is the part an agent acts on, and
 * the prose alone hides it. Only the message changes: every other field of the refusal
 * (`candidates`, `current`, `mismatches`, `issues`, `changed`) rides along, since a caller reads
 * them off the error it catches. An error that is not a refusal is returned as it is, so a caller's
 * `throw refusalWithCode(error)` rethrows it untouched. This returns rather than throws, so the
 * throw stays at the call site, where a reader of the handler sees that the path ends there.
 *
 * A gateway's answer stays a `DispatchGatewayError`, and the caller's account of what its call did
 * joins it in one sentence. Where the request may have reached Dispatch (`mayHaveReachedDispatch`),
 * the client's advice could judge only from the method (a write is told to check whether it took
 * effect), so that account, which knows what its call did, takes its place and must say what to do;
 * otherwise the request never reached Dispatch, and the account follows the client's advice.
 */
export function refusalWithCode(error: unknown, ...clauses: string[]): unknown {
  const suffix = clauses.filter((clause) => clause !== "").join("; ");
  const joined = suffix === "" ? "" : `; ${suffix}`;
  if (error instanceof DispatchGatewayError) {
    let told = error.message;
    if (joined !== "") {
      told = error.mayHaveReachedDispatch
        ? `${error.answer}${joined}`
        : `${error.answer}, so ${error.advice}${joined}`;
    }
    return new DispatchGatewayError(
      error.status,
      error.answer,
      error.advice,
      `${error.code}: ${told}`
    );
  }
  if (!(error instanceof DispatchServiceError)) return error;
  // A message that ends a sentence (a parent-status conflict's does) takes the clauses as a
  // sentence of their own, as withAccount joins its account.
  const told =
    joined !== "" && /[.!?]$/.test(error.message)
      ? `${error.message} ${suffix.charAt(0).toUpperCase()}${suffix.slice(1)}`
      : `${error.message}${joined}`;
  return new DispatchServiceError(error.code, error.status, `${error.code}: ${told}`, error);
}

/**
 * What a `409 PARENT_STATUS_CONFLICT` prints for the command `tool` it answered, called with
 * `args` as its caller typed them (`runDispatchCli`): the refusal's own message, then each issue
 * that holds the rule as `KEY [status] title`, then what to do next. Dispatch judges only the pairs
 * a write changes, so that last part follows what the call changed, and names no step that the
 * rule refuses:
 *
 * - A create (`dispatch issue`) is refused only under a done parent, which the refusal names, and
 *   takes no flag that moves it: it prints how to reopen that parent first or file the issue
 *   elsewhere. The reopen takes `--include-parent`, as a reparent's move does, because the rule
 *   also refuses a plain status change of that parent where the issue above it is done or not
 *   started.
 * - A reparent (`dispatch issue-update --parent P`) is refused for P, which the issue is not under
 *   yet, and no flag of the command moves P: `--include-parent` moves the issues above the issue
 *   as it stands, and goes with no `--parent`. So it prints how to move P into work first, then
 *   the reparent to run again, naming `--issue` and `--status` only where the call did. A child
 *   that holds the rule against the status the reparent also wrote prints the flags that move the
 *   children, which go with `--status` alone: the status change goes first, in a call of its own,
 *   and the reparent after it, without `--status`.
 * - A status change prints the flags of `dispatch issue-update --status` that move the issues.
 *   Those flags go with the status alone (and the reason of a close), so beside any other field
 *   it also says to send the other changes in a call of their own.
 *
 * Dispatch also refuses a credential scoped to one project each step that writes an issue of
 * another project (403 PROJECT_SCOPE). The CLI keeps no record of its credential's scope, which
 * only `GET /api/v1/whoami` answers, and it sends no request to print a refusal. So where an issue
 * the refusal names, which a flag of the line writes, is in another project than the issue the
 * call names, the line also says that a credential scoped to one project is refused that step,
 * and that a person, or a credential that names no project, must make it
 * (`otherProjectSentence`). The refusal names only the issues that hold the rule, so an issue of
 * another project that it does not name gets no such sentence. A create or a reparent under a
 * parent of another project is refused such a credential before the rule is judged, so the parent
 * that a create's or a reparent's refusal names gets none either.
 *
 * `issue` is absent from `args` when the owner came from `LEGION_ISSUE`, which a command run again
 * without `--issue` resolves the same way, so a line names `--issue` only where the call did.
 * `legionIssue` is that variable, which then tells the project of the issue the call names.
 * Dispatch writes each issue's status into the message too, so a reader that takes no `issues`
 * still sees them. Undefined for any other error, and for a conflict that names no issue, which
 * prints as its message alone.
 */
export function parentStatusConflictLines(
  error: unknown,
  tool: string,
  args: Readonly<Record<string, unknown>>,
  legionIssue?: string
): string[] | undefined {
  if (!dispatchAnswered(error, 409) || error.code !== "PARENT_STATUS_CONFLICT") return undefined;
  const issues = error.issues ?? [];
  if (issues.length === 0) return undefined;
  const parents = issues.filter((issue) => issue.relation === "parent").map(({ key }) => key);
  const children = issues.filter((issue) => issue.relation === "child").map(({ key }) => key);
  return [
    messageFor(error),
    ...issues.map((issue) => `${issue.key} [${issue.status}] ${issue.title}`),
    ...nextSteps(tool, args, parents, children, legionIssue),
  ];
}

const CHILD_FLAGS =
  "dispatch issue-update --status takes --unlink-children, which removes its open children from it, or, with a status other than done, --include-children, which moves the open issues under the issue with it. A person signed in at a browser closes them with it on the issue's page.";

const PARENT_FLAG =
  "dispatch issue-update --status takes --include-parent, which moves each issue above the issue that is not in work to in_progress.";

/** The sentence a line ends with where some of `keys`, issues a refusal names that a flag of the
 *  line writes, are in another project than `named`, the issue the call names: a credential
 *  scoped to one project is refused that step (`parentStatusConflictLines`). Empty where each is
 *  in the project of `named`, and where `named` is no Dispatch issue key (a GitHub issue), whose
 *  project the CLI cannot tell. */
function otherProjectSentence(named: string | undefined, keys: readonly string[]): string {
  const project = named === undefined ? undefined : issueProject(named);
  if (project === undefined) return "";
  const others = keys.filter((key) => {
    const other = issueProject(key);
    return other !== undefined && other !== project;
  });
  if (others.length === 0) return "";
  const one = others.length === 1;
  return ` ${others.join(", ")} ${one ? "is" : "are"} not in the project of ${named}. A credential scoped to one project is refused a step that writes ${one ? "it" : "them"} (403 PROJECT_SCOPE), so a person, or a credential that names no project, must make that step.`;
}

/** The call that moves `key` into work, with the clause on what else it moves, as each line of
 *  `nextSteps` that names a parent writes it. */
function includeParentCall(key: string): string {
  return `${commandLine("dispatch_issue_update", { issue: key, status: "in_progress", include_parent: true })}, which also moves each issue above ${key} that is not in work`;
}

/** What the caller of `tool` with `args` does next about the issues a refusal names: `parents`,
 *  each key of relation `parent`, and `children`, each key of relation `child`
 *  (`parentStatusConflictLines`). */
function nextSteps(
  tool: string,
  args: Readonly<Record<string, unknown>>,
  parents: readonly string[],
  children: readonly string[],
  legionIssue: string | undefined
): string[] {
  if (tool === "dispatch_issue") {
    return parents.map(
      (key) =>
        `dispatch issue takes no flag that moves a done parent. Reopen ${key} first, with ${includeParentCall(key)}, then run dispatch issue again; or run it with --parent naming an open issue, or with no --parent.`
    );
  }
  // Only dispatch issue-update's arguments are read here.
  const issueUpdate = tool === "dispatch_issue_update";
  const written: Readonly<Record<string, unknown>> = issueUpdate ? args : {};
  // The fields the call wrote, the issue it names aside.
  const fields = Object.keys(written).filter(
    (field) => field !== "issue" && written[field] !== undefined
  );
  const issue = optionalString(written, "issue");
  // The issue the call names, which LEGION_ISSUE supplies where the call has no --issue.
  const named = issueUpdate ? (issue ?? legionIssue) : undefined;
  const childHolds = children.length > 0;
  const childScope = otherProjectSentence(named, children);
  const status = optionalString(written, "status");
  const parent = optionalString(written, "parent");
  const alone = `--status${status === "done" ? " and --reason" : ""} alone`;
  if (parent !== undefined && parent !== "") {
    // A child holds the rule only against the status the reparent also wrote. The flags that
    // move it go with the status alone, so the status change (with the reason of a close) goes
    // first, in a call of its own, and the reparent runs again without them.
    const issueFlag = issue === undefined ? "" : ` --issue ${issue}`;
    const statusFlag = status === undefined || childHolds ? "" : ` --status ${status}`;
    const kept = fields.filter(
      (field) => field !== "parent" && field !== "status" && !(childHolds && field === "reason")
    );
    const otherFlags = kept.length > 0 ? ", with the call's other flags" : "";
    const reparent = `dispatch issue-update${issueFlag} --parent ${parent}${statusFlag}${otherFlags}`;
    const afterStatus = childHolds ? ", after the status change," : "";
    return [
      ...(childHolds
        ? [
            `${CHILD_FLAGS} Each goes with ${alone}, so run the status change first, in a call of its own, and the reparent after it: ${reparent}.${childScope}`,
          ]
        : []),
      ...parents.map(
        (key) =>
          `dispatch issue-update takes no flag that moves the parent a reparent names. Move ${key} into work first, with ${includeParentCall(key)}; then${afterStatus} run the reparent again: ${reparent}.`
      ),
    ];
  }
  const others = ISSUE_UPDATE_PATCH_FIELDS.some((field) => written[field] !== undefined);
  const apart = (subject: string): string =>
    others
      ? ` ${subject} with ${alone}, so send the call's other changes in a call of their own.`
      : "";
  return [
    ...(childHolds ? [`${CHILD_FLAGS}${apart("Each goes")}${childScope}`] : []),
    ...(parents.length > 0
      ? [`${PARENT_FLAG}${apart("It goes")}${otherProjectSentence(named, parents)}`]
      : []),
  ];
}

/** Whether Dispatch itself answered `status`; a gateway's answer of that status says nothing it decided. */
export function dispatchAnswered(error: unknown, status: number): error is DispatchServiceError {
  return error instanceof DispatchServiceError && error.fromDispatch && error.status === status;
}

/**
 * Whether a write that failed with `error` may have taken effect anyway. Only an answer sent
 * before the write could apply proves it did not: Dispatch's own refusal (a 4xx), or a gateway's
 * answer that never reached Dispatch (`mayHaveReachedDispatch`), which the client throws as such
 * only when no earlier attempt of the write may have reached Dispatch either. Dispatch's own 5xx
 * can come after it committed, and a timeout, a transport error or a `DispatchWriteUnsettledError`
 * (an earlier attempt may have reached Dispatch) says nothing either way.
 */
export function writeMayHaveLanded(error: unknown): boolean {
  if (error instanceof DispatchGatewayError) return error.mayHaveReachedDispatch;
  return !(error instanceof DispatchServiceError) || error.status >= 500;
}

/** The reason a call posted before its close: the message's id and its `dispatch://` reference. */
export interface ClosingNote {
  readonly id: string;
  readonly ref: string;
}

/** What a call whose closing reason landed says when its close then failed with `error`: where the
 *  reason is, and whether the close may have landed anyway, since a blind retry would post the
 *  reason a second time. A gateway's timeout or rate limit refused nothing there is to fix. */
export function closeAccount(error: unknown, closingNote: ClosingNote): string {
  const posted = `the reason already landed as message ${closingNote.id} (${closingNote.ref})`;
  const fix =
    error instanceof DispatchGatewayError && error.transient
      ? ""
      : "fix what refused the close, then ";
  return writeMayHaveLanded(error)
    ? `${posted}, and the close may or may not have taken effect. Read the issue's status before retrying: done means it closed; otherwise retry with a reason that points at message ${closingNote.id}, since retrying this call posts its reason again`
    : `${posted} but the issue did not close. Retrying this call posts its reason again, so ${fix}retry with a reason that points at message ${closingNote.id}`;
}

/**
 * An error that is no refusal (a timeout, a transport error) with `account`, the caller's clause
 * on what its call did, after its message: joined to it with "; ", or as a sentence of its own
 * after a message that ends one ("The operation timed out.", "… able to access the url?").
 */
export function withAccount(error: unknown, account: string): Error {
  const message = messageFor(error);
  const told = /[.!?]$/.test(message)
    ? `${message} ${account.charAt(0).toUpperCase()}${account.slice(1)}`
    : `${message}; ${account}`;
  return new Error(told, { cause: error });
}
