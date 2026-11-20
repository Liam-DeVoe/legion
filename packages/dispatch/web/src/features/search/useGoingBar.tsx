import { useQuery } from "@tanstack/react-query";

import { goingCapabilityQuery } from "../../api/queries";
import type { GoingContextInput } from "../../api/types";
import { type CapabilityGap, capabilityGaps } from "./CapabilityNote";
import { type GoingAnswer, useGoingAnswer } from "./useGoingAnswer";

/** What the command bar draws of Dispatch's answer to the words in the box. */
export interface GoingBar {
  /** Whether this Dispatch answers at all: its agent runs, and the line on top offers them. */
  readonly answers: boolean;
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
}

/**
 * Dispatch's short answer to the words in the command bar: asked on the same pause the meaning
 * search waits for and only where this Dispatch answers at all, with the chip the bar acts through
 * and the project the filter chip narrows to. It is drawn once the hits it sits beside are on the
 * screen (`showHits`), so the list paints first and the answer joins it; the last answer drawn
 * again as the bar opens waits on nothing, since an empty box lists no hits, and nor does an
 * answer to words the person typed past, which the panel keeps through the pause and the new
 * words' hits until the next answer replaces it. The short answer only reads; Enter on the line on
 * top starts a run of the agent (`features/going`).
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
  const answers = capability.data?.answer.configured === true;
  const { answer, earlier } = useGoingAnswer({
    context,
    enabled: enabled && answers,
    login,
    project,
    words,
  });
  return {
    answers,
    gaps: capability.data === undefined ? [] : capabilityGaps(capability.data),
    shownAnswer:
      earlier ?? (answer !== undefined && (answer.replayed || showHits) ? answer : undefined),
    stale: earlier !== undefined,
  };
}
