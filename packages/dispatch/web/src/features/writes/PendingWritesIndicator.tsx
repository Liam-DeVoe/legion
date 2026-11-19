import type { ReactNode } from "react";

import { apiErrorMessage } from "../../api/client";
import {
  borderDefault,
  calloutWarningBorder,
  card,
  dangerText,
  secondaryButtonCompact,
  textSecondaryOnSurface,
} from "../../theme/classes";
import {
  expiredRetryNote,
  SendExpiredError,
  SendInterruptedError,
  SendStoppedError,
  sameKeyRetryNote,
} from "./keyed-write";
import { StopTrying } from "./StopTrying";
import { heldForSignOut, indicatorWrites, uploadedPercent, waitingToSend } from "./shown-writes";
import { useWriteSelection, useWrites } from "./write-store";

/** Why the refused write was not saved, in the reader's words. */
function refusalReason(refusal: Error): string {
  if (refusal instanceof SendInterruptedError) {
    return `this page was closed before Dispatch confirmed it. ${sameKeyRetryNote("saved")}`;
  }
  if (refusal instanceof SendExpiredError) {
    return `Dispatch has not confirmed it. ${expiredRetryNote("saved")}`;
  }
  if (refusal instanceof SendStoppedError) {
    return `Dispatch has not confirmed it. ${sameKeyRetryNote("saved")}`;
  }
  return apiErrorMessage(refusal, "Dispatch could not be reached");
}

/**
 * The shell's one account of the writes no composer shows (`write-store.ts`'s `indicator`
 * writes): chrome, out of the page's flow in the corner the connection pill shares, and gone while
 * nothing is pending. Its status line counts the writes that went out - `Saving 2 changes…`, then,
 * once one is past its deadline and is being tried again, `Dispatch has not confirmed 2 changes —
 * still trying` - and it names each write the reader can act on or should see: one held for the
 * person's sign-out in another tab, `Waiting for a sign-out in another tab`; one waiting its turn
 * behind another that was never sent, `Waiting to send` with Discard; an upload whose body is
 * still going up, `Uploading… N%`, which is never "still trying" until an attempt of it has
 * failed; a late one with Stop trying; a refused one with why, Retry and Discard. A write still
 * within its deadline is only counted, since there is nothing to do about it yet.
 */
export function PendingWritesIndicator(): ReactNode {
  const writes = useWrites();
  const rows = useWriteSelection("indicator", indicatorWrites);
  if (rows.length === 0) return null;
  const out = rows.filter(
    (row) => row.status !== "refused" && !waitingToSend(row) && !heldForSignOut(row)
  );
  const late = out.some((row) => row.status === "late" && uploadedPercent(row) === undefined);
  const named = rows.filter(
    (row) =>
      row.status === "late" ||
      row.status === "refused" ||
      heldForSignOut(row) ||
      waitingToSend(row) ||
      uploadedPercent(row) !== undefined
  );
  const count = out.length === 1 ? "1 change" : `${out.length} changes`;
  const actionable = rows.some((row) => row.status === "late" || row.status === "refused");
  return (
    <section
      aria-label="Pending changes"
      className={`w-full max-w-md rounded-xl border px-3 py-2 text-xs shadow-lg ${card} ${
        actionable ? calloutWarningBorder : borderDefault
      }`}
      data-testid="pending-writes"
    >
      {out.length === 0 ? null : (
        <p aria-live="polite" className={`font-medium ${textSecondaryOnSurface}`} role="status">
          {late ? `Dispatch has not confirmed ${count} — still trying` : `Saving ${count}…`}
        </p>
      )}
      {named.length === 0 ? null : (
        <ul className={`max-h-48 space-y-1 overflow-y-auto ${out.length === 0 ? "" : "mt-2"}`}>
          {named.map((row) => {
            const uploaded = uploadedPercent(row);
            return (
              <li
                className="flex min-h-8 flex-wrap items-center gap-x-2 gap-y-1"
                data-pending-write={row.id}
                key={row.id}
              >
                {row.status === "refused" ? (
                  <>
                    <span className={`min-w-0 flex-1 ${dangerText}`}>
                      Not saved: {row.label} — {refusalReason(row.refusal)}
                    </span>
                    <button
                      className={secondaryButtonCompact}
                      onClick={() => writes.retry(row.id)}
                      type="button"
                    >
                      Retry
                    </button>
                    <button
                      className={secondaryButtonCompact}
                      onClick={() => writes.discard(row.id)}
                      type="button"
                    >
                      Discard
                    </button>
                  </>
                ) : heldForSignOut(row) ? (
                  <>
                    <span className={`min-w-0 flex-1 ${textSecondaryOnSurface}`}>
                      {row.label} — Waiting for a sign-out in another tab
                    </span>
                    {waitingToSend(row) ? (
                      <button
                        className={secondaryButtonCompact}
                        onClick={() => writes.discard(row.id)}
                        type="button"
                      >
                        Discard
                      </button>
                    ) : null}
                  </>
                ) : waitingToSend(row) ? (
                  <>
                    <span className={`min-w-0 flex-1 ${textSecondaryOnSurface}`}>
                      {row.label} — Waiting to send
                    </span>
                    <button
                      className={secondaryButtonCompact}
                      onClick={() => writes.discard(row.id)}
                      type="button"
                    >
                      Discard
                    </button>
                  </>
                ) : (
                  <>
                    <span className={`min-w-0 flex-1 ${textSecondaryOnSurface}`}>
                      {uploaded === undefined
                        ? row.label
                        : `${row.label} — Uploading… ${uploaded}%`}
                    </span>
                    {row.status === "late" ? (
                      <StopTrying onStop={() => writes.stop(row.id)} stopping={row.stopping} />
                    ) : null}
                  </>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
