import { DispatchGatewayError, DispatchNoRouteError, DispatchServiceError } from "../dispatch-http";
import type { Owner } from "../dispatch-owner";
import { messageFor } from "../errors";

/**
 * A Dispatch refusal carrying its own code in the message the host shows: the code
 * (ISSUE_CLAIMED, CLAIM_CONTENDED, EXTERNAL_LINK_TAKEN, ...) is the part an agent acts on, and
 * the prose alone hides it. Only the message changes: every other field of the refusal
 * (`candidates`, `current`, `mismatches`) rides along, since a caller reads them off the error
 * it catches. An error that is not a refusal is returned as it is, so a caller's
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
  return new DispatchServiceError(
    error.code,
    error.status,
    `${error.code}: ${error.message}${joined}`,
    error.candidates,
    error.current,
    error.mismatches
  );
}

/** Whether Dispatch itself answered `status`; a gateway's answer of that status says nothing it decided. */
export function dispatchAnswered(error: unknown, status: number): error is DispatchServiceError {
  return error instanceof DispatchServiceError && error.fromDispatch && error.status === status;
}

/** The message an older build's comment edit refuses the `actor` this client sends with: its
 *  `decodeJSON` (`api/server.go`) writes "invalid JSON body: " and the decoder's error, which Go's
 *  `encoding/json` words `json: unknown field "actor"`. */
const OLDER_COMMENT_EDIT_ACTOR = 'invalid JSON body: json: unknown field "actor"';

/**
 * The answer of `call`, with an older Dispatch's refusal of a post correction replaced by the one
 * sentence that names the real cause. The plugins release from main as this merges while
 * Dispatch's own server is deployed by hand (dispatch://INFRA-1464), so for a while these
 * commands run against a server with no route by which an agent edits or deletes a post or reads
 * the bodies one replaced: its router answers `404 NOT_FOUND` "no route for …" to both deletes,
 * the message edit and the history reads. Every client call retries such an answer, so what
 * arrives here is `DispatchNoRouteError`, which a server with the route never answers: a roll
 * puts the route in place within a minute, while a Dispatch that predates post corrections
 * answers that way until it is deployed, and only this sentence tells an agent which to expect.
 * Its comment edit, the one route that was there before, refuses this release's call in one of
 * two ways, by the credential. It first reads the person it acts for (`actingPerson`), which
 * answers a credential without the person scope `403 OAUTH_SCOPE_REQUIRED`; the route at this
 * head names no scope, so only an older build answers that code there. A credential with that
 * scope, a person's own token included, passes, and that build then decodes the body into an
 * input holding only `body`, `base_revision` and `blocks`, refusing an undeclared member; the
 * body this client sends always carries `actor`, so it answers `400 INVALID_JSON` naming the
 * unknown field `actor` before it compares the author. A build from #2461 on ignores an
 * undeclared member, so only one older than that answers it, and a person's own credential
 * reaches the comment edit of a newer build, which takes the edit and keeps no earlier body.
 * Only the comment edit reads either code as the server's age, and only in those exact forms: an
 * `INVALID_JSON` naming any other field is passed through. Without this, an agent is told its
 * credential lacks a scope, or that its own request was malformed, and goes asking a person for
 * one or rewriting the call.
 *
 * Where `post` names the post the call was for, Dispatch's own `404 NOT_FOUND` of that post is
 * rewritten to name it, since `not found` alone says neither which post nor whether the route
 * was there. A router's no-route 404 is not that answer and keeps the sentence above.
 */
export async function throughPostCorrections<T>(
  tool: string,
  url: string,
  call: Promise<T>,
  post?: { readonly kind: "comment" | "message"; readonly id: string }
): Promise<T> {
  try {
    return await call;
  } catch (error) {
    const commentEditIsAge =
      tool === "dispatch_edit_comment" &&
      ((dispatchAnswered(error, 403) && error.code === "OAUTH_SCOPE_REQUIRED") ||
        (dispatchAnswered(error, 400) &&
          error.code === "INVALID_JSON" &&
          error.message === OLDER_COMMENT_EDIT_ACTOR));
    if (!(error instanceof DispatchNoRouteError) && !commentEditIsAge) {
      // Dispatch's own `not found` names neither the post nor the id, so a reader cannot tell a
      // post that is not there from an issue or a route that is not: the id the call used says
      // which (dispatch://INFRA-1523).
      if (post !== undefined && dispatchNotFound(error)) {
        throw refusalWithCode(error, `this Dispatch holds no ${post.kind} ${post.id}`);
      }
      throw error;
    }
    throw new Error(
      `This Dispatch (${url}) predates post corrections: it has no route by which an agent edits ` +
        "or deletes a post or reads the bodies one replaced. Until it is deployed with this " +
        "release's server, a correction is one more post saying what changed."
    );
  }
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

/**
 * Whether Dispatch itself answered `404 NOT_FOUND` for the thing asked for. A router's no-route
 * 404, which every build gives a path that names no route, is a `DispatchNoRouteError` and is no
 * answer about the thing: it is what tells a correction that the server predates post
 * corrections, so it is never read as a missing post.
 */
function dispatchNotFound(error: unknown): error is DispatchServiceError {
  return (
    !(error instanceof DispatchNoRouteError) &&
    dispatchAnswered(error, 404) &&
    error.code === "NOT_FOUND"
  );
}

/**
 * The refusal a reference's own read of a post or an ask earns when Dispatch answers
 * `404 NOT_FOUND`: its `not found` names neither the item nor its owner, so a reader cannot tell
 * an item that is not there from an issue or a document that is not (dispatch://INFRA-1523).
 * The owner is read once for the whole command by `readOwner`, which the caller builds of the
 * reference's own owner and of a document it has already read: a reference whose issue Dispatch
 * does not hold is refused in those words, one whose document it does not hold meets that read's
 * own refusal, which names the document and lists the project's, and one whose owner stands
 * names the item that owner holds no row of.
 *
 * Only an answer that says the owner is missing changes what the caller is told. A read that
 * failed for any other reason - a gateway, a 5xx, a transport error - says nothing about the
 * owner, so the item's own refusal stands rather than being replaced by that failure: a reader
 * meeting a gateway error here would be told something other than what is true of the request
 * they made.
 *
 * The callers are the four correction commands, `dispatch resolve-comment`, and `dispatch read`
 * of a comment, a message or an ask reference. The ask commands (`dispatch edit-ask`,
 * `resolve-ask`, `follow`) resolve a reference of their own and do not run it.
 */
export async function refuseMissingOfRef(
  error: unknown,
  kind: "ask" | "comment" | "message",
  id: string,
  owner: Owner,
  ownerName: string,
  readOwner: () => Promise<unknown>
): Promise<unknown> {
  if (!dispatchNotFound(error)) return error;
  const missingOwner = await readOwner().then(
    () => undefined,
    (caught: unknown) => {
      if (owner.kind === "issue") {
        return dispatchAnswered(caught, 404)
          ? refusalWithCode(error, `this Dispatch holds no issue ${owner.issue}`)
          : undefined;
      }
      // A document that is not there is refused by its own resolution, which names it and lists
      // the project's documents (`resolveArtifact`, `documentReferenceProblem`) and is no answer
      // of Dispatch's. An answer or a failure of Dispatch's own - a gateway, a 5xx, a refusal of
      // another kind - says nothing about the document, so the item's refusal stands.
      return caught instanceof DispatchGatewayError ||
        caught instanceof DispatchServiceError ||
        caught instanceof DispatchNoRouteError
        ? undefined
        : caught;
    }
  );
  return missingOwner ?? refusalWithCode(error, `${ownerName} holds no ${kind} ${id}`);
}
