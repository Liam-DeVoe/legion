import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { goingCapabilityQuery } from "../../api/queries";
import type { GoingContextInput } from "../../api/types";
import { kbdHint } from "../../theme/classes";
import { DIALOG_SCOPE, MOD_KEY_LABEL, useKeymap } from "../shell/keymap";
import { type CapabilityGap, capabilityGaps } from "./CapabilityNote";
import { type GoingOperations, useGoingOperations } from "./OperationRows";
import { type GoingAnswer, useGoingAnswer } from "./useGoingAnswer";

/** What the command bar draws of Dispatch's answer to the words in the box. */
export interface GoingBar {
  /** The bar's operations, from the answer's decision and proposal (`useGoingOperations`). */
  readonly operations: GoingOperations;
  /** The answer to draw beside the hits: undefined until the hits it sits beside are shown, but
   *  for the last answer drawn again as the bar opens, and for an answer to words the person
   *  typed past, which stays until the answer to the words in the box has something of its own
   *  to draw (`useGoingAnswer`'s `earlier`). */
  readonly shownAnswer: GoingAnswer | undefined;
  /** Whether `shownAnswer` answers words the person has typed past. */
  readonly stale: boolean;
  /** What this Dispatch cannot do, which the bar's foot says once from the moment it opens
   *  (`CapabilityNote`): none until the capability read answers. */
  readonly gaps: readonly CapabilityGap[];
  /** The keyboard hint for the bar's submit, among the palette's own: none while there is no
   *  operation to run. */
  readonly runHint: ReactNode;
}

/**
 * Dispatch's answer to the words in the command bar and the operations it offers: the answer is
 * asked on the same pause the meaning search waits for and only where this Dispatch answers at
 * all, with the chip the bar acts through and the project the filter chip narrows to. It is drawn
 * once the hits it sits beside are on the screen (`showHits`), so the list paints first and the
 * answer joins it; the last answer drawn again as the bar opens waits on nothing, since an empty
 * box lists no hits, and nor does an answer to words the person typed past, which the panel
 * keeps through the pause and the new words' hits until the next answer replaces it. The
 * operations - the one the decision acts on, or the two best actions to pick from, as rows above
 * the hits, and what they wrote, one line each, under the list - come from the answer to the
 * words in the box alone, never from one typed past, and run on the bar's submit (`$mod+Enter`)
 * from anywhere in the bar, the box included, so Enter keeps its one meaning, opening the row
 * the highlight is on.
 */
export function useGoingBar({
  context,
  enabled,
  login,
  project,
  showHits,
  words,
}: {
  /** The chip, as the question is asked with it. */
  readonly context: GoingContextInput;
  /** Whether the words are a query the bar searches. */
  readonly enabled: boolean;
  /** The person asking; undefined while the page has not read it. */
  readonly login: string | undefined;
  /** The project the words are asked of, `""` for every project: the filter chip's. */
  readonly project: string;
  /** Whether the hits that answer the words are on the screen. */
  readonly showHits: boolean;
  /** The words in the box, trimmed. */
  readonly words: string;
}): GoingBar {
  const capability = useQuery(goingCapabilityQuery());
  const { answer, askNow, earlier } = useGoingAnswer({
    context,
    enabled: enabled && capability.data?.answer.configured === true,
    login,
    project,
    words,
  });
  const operations = useGoingOperations({ answer, askNow, words });
  useKeymap(DIALOG_SCOPE, [
    {
      id: "search-run",
      inEditable: true,
      keys: "$mod+Enter",
      label: "Run the bar's operation",
      palette: false,
      run: operations.submit,
    },
  ]);
  return {
    gaps: capability.data === undefined ? [] : capabilityGaps(capability.data),
    operations,
    runHint:
      operations.section === undefined ? null : (
        <span className="inline-flex items-center gap-1">
          <kbd className={`rounded px-1 py-0.5 font-medium ${kbdHint}`}>{MOD_KEY_LABEL}</kbd>
          <kbd className={`rounded px-1 py-0.5 font-medium ${kbdHint}`}>Enter</kbd>
          to run
        </span>
      ),
    shownAnswer:
      earlier ?? (answer !== undefined && (answer.replayed || showHits) ? answer : undefined),
    stale: earlier !== undefined,
  };
}
