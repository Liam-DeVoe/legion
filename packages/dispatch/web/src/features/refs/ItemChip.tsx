import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { borderDefault, textPrimaryOnSurface, textSecondaryOnSurface } from "../../theme/classes";
import { statusTone } from "../issue-page/issue-status";
import { statusText } from "../project/board-model";
import { issueSummaryQuery } from "./reference-target";

/**
 * An issue that a command-bar answer names, drawn as a chip (design v2, state 4): its key, a dot
 * in its status's colour and its title, on one line, with the title cut where the line ends. The
 * status and the title come from the issue's summary (`issueSummaryQuery`,
 * `GET /api/v1/issues/{key}/summary`), the one read that every reference to the issue on the page
 * shares. Until that read answers, and for an issue the reader cannot read, the chip is the key
 * alone. The status's word is the dot's tooltip, and a screen reader hears it in place of the dot.
 * `MarkdownBody` draws the chip inside the link it made of the reference, which opens the issue.
 */
export function ItemChip({ issue }: { readonly issue: string }): ReactNode {
  const summary = useQuery(issueSummaryQuery(issue)).data;
  return (
    <span
      className={`not-prose inline-flex max-w-full items-center gap-1.5 rounded-md border px-1.5 align-middle text-sm leading-6 font-normal ${borderDefault}`}
      data-testid="item-chip"
    >
      <span className={`shrink-0 font-semibold ${textPrimaryOnSurface}`}>{issue}</span>
      {summary === undefined ? null : (
        <>
          <span
            aria-hidden="true"
            className={`size-2 shrink-0 rounded-full bg-current ${statusTone(summary.status)}`}
            data-status={summary.status}
            data-testid="item-chip-status"
            title={statusText(summary.status)}
          />
          <span className="sr-only">{statusText(summary.status)}</span>
          <span className={`min-w-0 truncate ${textSecondaryOnSurface}`}>{summary.title}</span>
        </>
      )}
    </span>
  );
}
