import { ISSUE_STATUS_OPTIONS } from "@legion/contracts";
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
 * a write changes, so that last part follows what the call changed, and names no step that does
 * not work:
 *
 * - A create (`dispatch issue`) is refused only under a done parent, which the refusal names, and
 *   takes no flag that moves it: it prints how to reopen that parent first or file the issue
 *   elsewhere.
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
 * `issue` is absent from `args` when the owner came from `LEGION_ISSUE`, which a command run again
 * without `--issue` resolves the same way, so a line names `--issue` only where the call did.
 * Dispatch writes each issue's status into the message too, so a reader that takes no `issues`
 * still sees them. Undefined for any other error, and for a conflict that names no issue, which
 * prints as its message alone.
 */
export function parentStatusConflictLines(
  error: unknown,
  tool: string,
  args: Readonly<Record<string, unknown>>
): string[] | undefined {
  if (!dispatchAnswered(error, 409) || error.code !== "PARENT_STATUS_CONFLICT") return undefined;
  const issues = error.issues ?? [];
  if (issues.length === 0) return undefined;
  const parents = issues.filter((issue) => issue.relation === "parent").map(({ key }) => key);
  const children = issues.some((issue) => issue.relation === "child");
  return [
    messageFor(error),
    ...issues.map((issue) => `${issue.key} [${issue.status}] ${issue.title}`),
    ...nextSteps(tool, args, parents, children),
  ];
}

/** The fields of `dispatch issue-update` a status change takes: the status, the reason of a close,
 *  and the options of the status change route, which go with no other field. */
const STATUS_CHANGE_FIELDS: ReadonlySet<string> = new Set([
  "status",
  "reason",
  ...ISSUE_STATUS_OPTIONS,
]);

const CHILD_FLAGS =
  "dispatch issue-update --status takes --unlink-children, which removes its open children from it, or, with a status other than done, --include-children, which moves the open issues under the issue with it. A person signed in at a browser closes them with it on the issue's page.";

const PARENT_FLAG =
  "dispatch issue-update --status takes --include-parent, which moves each issue above the issue that is not in work to in_progress.";

/** What the caller of `tool` with `args` does next about the issues a refusal names: `parents`,
 *  each key of relation `parent`, and `children`, whether any is of relation `child`
 *  (`parentStatusConflictLines`). */
function nextSteps(
  tool: string,
  args: Readonly<Record<string, unknown>>,
  parents: readonly string[],
  children: boolean
): string[] {
  if (tool === "dispatch_issue") {
    return parents.map(
      (key) =>
        `dispatch issue takes no flag that moves a done parent. Reopen ${key} first, with dispatch issue-update --issue ${key} --status <a status in work, such as in_progress>, then run dispatch issue again; or run it with --parent naming an open issue, or with no --parent.`
    );
  }
  const update = tool === "dispatch_issue_update";
  // The fields the call wrote, the issue it names aside; only issue-update's are read here.
  const fields = update
    ? Object.keys(args).filter((field) => field !== "issue" && args[field] !== undefined)
    : [];
  const status = update ? optionalString(args, "status") : undefined;
  const parent = update ? optionalString(args, "parent") : undefined;
  const alone = `--status${status === "done" ? " and --reason" : ""} alone`;
  if (parent !== undefined && parent !== "") {
    // A child holds the rule only against the status the reparent also wrote. The flags that
    // move it go with the status alone, so the status change (with the reason of a close) goes
    // first, in a call of its own, and the reparent runs again without them.
    const issue = optionalString(args, "issue");
    const issueFlag = issue === undefined ? "" : ` --issue ${issue}`;
    const statusFlag = status === undefined || children ? "" : ` --status ${status}`;
    const kept = fields.filter(
      (field) => field !== "parent" && field !== "status" && !(children && field === "reason")
    );
    const otherFlags = kept.length > 0 ? ", with the call's other flags" : "";
    const reparent = `dispatch issue-update${issueFlag} --parent ${parent}${statusFlag}${otherFlags}`;
    const afterStatus = children ? ", after the status change," : "";
    return [
      ...(children
        ? [
            `${CHILD_FLAGS} Each goes with ${alone}, so run the status change first, in a call of its own, and the reparent after it: ${reparent}.`,
          ]
        : []),
      ...parents.map(
        (key) =>
          `dispatch issue-update takes no flag that moves the parent a reparent names. Move ${key} into work first, with dispatch issue-update --issue ${key} --status in_progress --include-parent, which also moves each issue above ${key} that is not in work; then${afterStatus} run the reparent again: ${reparent}.`
      ),
    ];
  }
  const others = fields.some((field) => !STATUS_CHANGE_FIELDS.has(field));
  const apart = (flags: string): string =>
    others
      ? ` ${flags} with ${alone}, so send the call's other changes in a call of their own.`
      : "";
  return [
    ...(children ? [`${CHILD_FLAGS}${apart("Each goes")}`] : []),
    ...(parents.length > 0 ? [`${PARENT_FLAG}${apart("It goes")}`] : []),
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
