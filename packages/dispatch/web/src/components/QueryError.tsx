import type { ReactNode } from "react";

import { AGENT_LIST_REFILLING, isAgentListRefilling } from "../api/client";
import { dangerText, textSecondaryOnSurface } from "../theme/classes";

export interface QueryErrorProps {
  /** The small type of a line under a control row: the Issues tab's alerts under its view tabs. */
  compact?: boolean;
  /** The failure itself. An agent list that is refilling (`isAgentListRefilling`) is not a
   *  failure to report: the shared refilling notice replaces `message`, as status, not alert. */
  error?: unknown;
  message: string;
  /** Omitted for a failure a retry cannot get past; no Retry is offered then. */
  onRetry?: () => void;
  /** Retry's accessible name where Retry alone does not say what it reads again: a place that
   *  holds more than one Retry, as the alert line under the Issues tab's view tabs does. */
  retryLabel?: string;
  /** Disables Retry while the retried request is in flight. */
  retrying?: boolean;
}

/**
 * The line every view shows while Envoy's agent list refills, in place of any claim that an
 * agent is absent. One test id on every view, so a browser test finds it the same way on each.
 */
export function AgentListRefilling({ className }: { className?: string }): ReactNode {
  return (
    <p className={className} data-testid="agent-list-refilling" role="status">
      {AGENT_LIST_REFILLING}
    </p>
  );
}

/**
 * A retryable error state for a failed query or write, matching the Conversation
 * pin-state queue's affordance. The message breaks inside a word longer than the line, so a
 * whole error with a long token in it stays in the line.
 */
export function QueryError({
  compact = false,
  error,
  message,
  onRetry,
  retryLabel,
  retrying = false,
}: QueryErrorProps): ReactNode {
  const refilling = isAgentListRefilling(error);
  return (
    <div
      className={`flex flex-wrap items-center gap-3 ${compact ? "text-xs" : "text-sm"} ${refilling ? textSecondaryOnSurface : dangerText}`}
      role={refilling ? undefined : "alert"}
    >
      {refilling ? <AgentListRefilling /> : <p className="min-w-0 break-words">{message}</p>}
      {onRetry === undefined ? null : (
        <>
          {/* The space between the message and Retry: a flex container does not render a
              white-space-only child, so this changes no pixel, and without it the alert's text
              reads the message's last word and Retry as one word. */}{" "}
          <button
            aria-label={retryLabel}
            className="font-medium underline disabled:cursor-not-allowed disabled:opacity-50"
            disabled={retrying}
            onClick={onRetry}
            type="button"
          >
            Retry
          </button>
        </>
      )}
    </div>
  );
}
