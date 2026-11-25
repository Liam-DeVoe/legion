import type { ReactNode } from "react";

import { borderStrong, textMutedOnCanvas } from "../theme/classes";

/** What a list, a table or a page says when it holds nothing: one muted line inside a dashed
 *  rule, and the one action that would fill it. */
export function EmptyState({
  action,
  label,
  message,
}: {
  action?: ReactNode;
  label: string;
  message: string;
}): ReactNode {
  return (
    <section
      aria-label={label}
      className={`rounded-lg border border-dashed px-4 py-8 text-center text-sm ${borderStrong} ${textMutedOnCanvas}`}
    >
      <p>{message}</p>
      {action === undefined ? null : <div className="mt-3">{action}</div>}
    </section>
  );
}
