import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import {
  borderDefault,
  surfaceMutedBg,
  textPrimaryOnSurface,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { statusTone } from "../issue-page/issue-status";
import { statusText } from "../project/board-model";
import { issueSummaryQuery } from "./reference-target";

/**
 * An issue that a command-bar answer names, drawn as a chip (design v2, state 4): its key, a dot
 * in its status's colour and its title. The status and the title come from the issue's summary
 * (`issueSummaryQuery`, `GET /api/v1/issues/{key}/summary`), the one read that every reference to
 * the issue on the page shares. Until that read answers, and for an issue the reader cannot read,
 * the chip is the key alone. The status's word is the dot's tooltip, and a screen reader hears it
 * in place of the dot. `MarkdownBody` draws the chip inside the link it made of the reference,
 * which opens the issue.
 *
 * The chip takes the room its line has, and the lines under it: it is inline, as words are, so a
 * title too long for the rest of its line goes on onto the next one, each piece of it in the
 * chip's own frame (`box-decoration-break: clone`), and no title is cut where the bar has room for
 * it. Being words, it also keeps the punctuation the answer writes after it on its line, against
 * its frame, as a word keeps its comma: a frame of its own (an `inline-flex` box) would let a line
 * end between the chip and its comma. The key never breaks.
 */
export function ItemChip({ issue }: { readonly issue: string }): ReactNode {
  const summary = useQuery(issueSummaryQuery(issue)).data;
  return (
    <span
      className={`not-prose inline rounded-md border px-1 py-0.5 text-sm leading-6 font-normal box-decoration-clone ${borderDefault} ${surfaceMutedBg}`}
      data-testid="item-chip"
    >
      <span className={`font-semibold whitespace-nowrap ${textPrimaryOnSurface}`}>{issue}</span>
      {summary === undefined ? null : (
        <>
          <span
            aria-hidden="true"
            className={`mx-1.5 inline-block size-2 rounded-full bg-current align-middle ${statusTone(summary.status)}`}
            data-status={summary.status}
            data-testid="item-chip-status"
            title={statusText(summary.status)}
          />
          <span className="sr-only">{statusText(summary.status)}</span>
          <span className={`break-words ${textSecondaryOnSurface}`} data-testid="item-chip-title">
            {summary.title}
          </span>
        </>
      )}
    </span>
  );
}
