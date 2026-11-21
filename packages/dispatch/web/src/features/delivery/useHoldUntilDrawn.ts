import { useEffect, useState } from "react";

import { beginAnswerHold } from "../../components/focus-place";
import { SEND_DEADLINE_MS } from "../writes/keyed-write";

/**
 * A hold on the controls of a component whose write has landed while the update that carries
 * the write is still to be drawn. `version` is the write time of the record the component draws.
 * The hold is on while `version` is still the one of the render whose `hold` was called. The
 * update that carries the write brings a new version, or takes the component away, and the hold
 * ends then. So a control the reader pressed stays held until that update. The hold is part of
 * the action of the reader's press that made the write (`beginAnswerHold`) from the call of
 * `hold` until the commit that ends it, so the page's rescue reads a loss that update makes as
 * the press's doing (`causedByReader`) and gives the focus back to the control once it is enabled
 * again. An update that never comes ends the hold at the app's write deadline
 * (`SEND_DEADLINE_MS`), counted from the latest `hold`, so no control is ever locked. Answers
 * whether the hold is on, and what puts it on: called as the write lands, never on a refusal,
 * whose control comes back with its error at once.
 */
export function useHoldUntilDrawn(
  version: string | null
): readonly [held: boolean, hold: () => void] {
  const [heldOn, setHeldOn] = useState<{
    /** Ends the hold's part in the press's action. */
    readonly answered: () => void;
    readonly version: string | null;
  } | null>(null);
  // A new version ends the hold. It is let go during render, so the effect's cleanup clears the
  // hold's timer then, and the timer does not run on to its end, and it ends the hold's part in
  // the press's action in the commit that draws the new version.
  if (heldOn !== null && heldOn.version !== version) setHeldOn(null);
  useEffect(() => {
    if (heldOn === null) return;
    const bound = window.setTimeout(() => setHeldOn(null), SEND_DEADLINE_MS);
    return () => {
      window.clearTimeout(bound);
      heldOn.answered();
    };
  }, [heldOn]);
  // Begun at the call, as the write lands, while the press that made the write is still live:
  // the commit that puts the hold on comes a task or more later, by when that press could have
  // ended.
  return [
    heldOn !== null && heldOn.version === version,
    () => setHeldOn({ answered: beginAnswerHold(), version }),
  ];
}
