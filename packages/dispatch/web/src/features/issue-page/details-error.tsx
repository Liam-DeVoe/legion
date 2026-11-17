import {
  type ReactNode,
  type Ref,
  type RefCallback,
  type RefObject,
  useCallback,
  useLayoutEffect,
  useRef,
  useState,
} from "react";

import { AGENT_LIST_REFILLING, isAgentListRefilling } from "../../api/client";
import { pageFailedText, pageHeaderControl, pageQuietText } from "../../theme/classes";
import { pageChrome } from "./page-structure";
import { CONTROL_LABELS } from "./page-words";

/** What a row of Details is handed of a read or a write that failed. */
export interface DetailsErrorLineProps {
  /** The failure itself. An agent list that is refilling (`isAgentListRefilling`) is not a
   *  failure to report: the refilling line replaces `message`, as status, not alert. */
  readonly error?: unknown;
  /** Which failure of a write the line says (`SaidFailure.count`). The line stays while its
   *  Retry's write is out, so each new failure is drawn afresh by this, and a screen reader says
   *  it again even in the same words. */
  readonly failure?: number;
  readonly message: string;
  /** Omitted for a failure a retry cannot get past; no Retry is offered then. */
  readonly onRetry?: () => void;
  /** Takes the Retry, so the row can hand its focus on when the line goes (`useFocusHandOff`). */
  readonly retryRef?: Ref<HTMLButtonElement>;
}

/**
 * A read or a write of a row of Details that failed, said in that row with its Retry: the line
 * the shared `QueryError` draws, in the page's colours and with the page's word for its control.
 * An agent list that is refilling is said as the line every view says it in
 * (`AgentListRefilling`'s test id), as status rather than alert.
 *
 * Retry is never disabled, since Chromium moves the focus of a control that turns disabled to
 * the page's body, which would lose a reader who pressed it by keyboard: a press while the
 * request it sent is still out is the caller's to drop.
 */
export function DetailsErrorLine({
  error,
  failure,
  message,
  onRetry,
  retryRef,
}: DetailsErrorLineProps): ReactNode {
  return (
    <div className="flex flex-wrap items-center gap-3 text-sm">
      {isAgentListRefilling(error) ? (
        <p className={pageQuietText} data-testid="agent-list-refilling" role="status">
          {AGENT_LIST_REFILLING}
        </p>
      ) : (
        <p className={pageFailedText} key={failure} role="alert">
          {message}
        </p>
      )}
      {onRetry === undefined ? null : (
        <button
          className={pageHeaderControl}
          onClick={onRetry}
          ref={retryRef}
          type="button"
          {...pageChrome}
        >
          {CONTROL_LABELS.retry}
        </button>
      )}
    </div>
  );
}

/** What a row reads of a write whose failure it says: `useMutation`'s state of its last press. */
export interface RowWrite<Variables> {
  readonly error: unknown;
  readonly isError: boolean;
  readonly isPending: boolean;
  readonly submittedAt: number;
  readonly variables: Variables | undefined;
}

/** A failure of a write that a row of Details says, with the variables of the press that failed. */
export interface SaidFailure<Variables> {
  /** One more for each failure said while the line stayed (`DetailsErrorLineProps.failure`). */
  readonly count: number;
  readonly error: unknown;
  readonly submittedAt: number;
  readonly variables: Variables | undefined;
}

/**
 * The failure a row of Details says of `write`: its last press's while that press stands
 * failed, and the same one while the write is out again with the same variables, as a press of
 * the line's Retry sends it; `null` once the write lands, or while a write of other variables is
 * out. A write that is out reports no error, so a line drawn from the error alone went at the
 * press of its Retry, and the focus of a reader who pressed Retry by keyboard went with it to the
 * page's body, there to stay when the write failed again and the line came back.
 */
export function useSaidFailure<Variables>(
  write: RowWrite<Variables>
): SaidFailure<Variables> | null {
  const [said, setSaid] = useState<SaidFailure<Variables> | null>(null);
  let next = said;
  if (write.isError) {
    if (said === null || said.error !== write.error || said.submittedAt !== write.submittedAt) {
      next = {
        count: (said?.count ?? 0) + 1,
        error: write.error,
        submittedAt: write.submittedAt,
        variables: write.variables,
      };
    }
  } else if (
    !write.isPending ||
    said === null ||
    // Variables built afresh at each press, as a form's are, are the same press by their JSON.
    JSON.stringify(said.variables) !== JSON.stringify(write.variables)
  ) {
    next = null;
  }
  if (next !== said) setSaid(next);
  return next;
}

/**
 * The ref of an element that may leave the page with the focus inside it - a line whose write
 * landed, with its Retry - which hands that focus to `to` once the element has left, rather than
 * leave it on the page's body, where Chromium puts the focus of an element that leaves. The focus
 * moves as the commit that took the element away ends, so an element that commit draws in its
 * place can take it. A focus anywhere else is the reader's, and stays where it is.
 */
export function useFocusHandOff(to: RefObject<HTMLElement | null>): RefCallback<HTMLElement> {
  const left = useRef(false);
  useLayoutEffect(() => {
    if (!left.current) return;
    left.current = false;
    to.current?.focus({ preventScroll: true });
  });
  return useCallback((element: HTMLElement | null) => {
    if (element === null) return undefined;
    // React runs this before it takes the element out of the page, so the focus is still where
    // the reader left it.
    return () => {
      left.current = element.contains(document.activeElement);
    };
  }, []);
}
