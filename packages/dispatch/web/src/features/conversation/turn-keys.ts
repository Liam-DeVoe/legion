import { createContext } from "react";
import { flushSync } from "react-dom";

import { ROW_MARKER } from "../../components/row-marker";
import { focusVisibleRing } from "../../theme/classes";
import { buildDispatchReference, type DispatchReferenceRoute } from "../refs/routes";

/**
 * The Conversation tab's turns as its keys reach them (dispatch://INFRA-2160, R22).
 *
 * A turn is one conversation the tab lists - a comment, a message, an ask or a handoff - or one
 * reply inside it. Each takes the focus from the tab's own keys (`tabIndex={-1}`), not from Tab:
 * `j` and `k` walk them, and Enter, `o`, `r`, `y` and Shift+Y act on the one the focus is on
 * (`ConversationTab`). The replies are drawn by rows the Inbox and the Agents page draw too
 * (`ReplyTurn`, `TargetedMessageCard`, `ThreadCard`, `AskThread`), which take the focus only
 * where the tab provides `ConversationTurns`: on those pages they are drawn as before.
 */

/** Every turn and every reply inside one: what `j` and `k` walk, and where the focus stands on a
 *  turn, which the issue header's `a` and Shift+R stand down on (`IssueHeader`). */
export const TURN_ROW = "[data-turn], [data-reply]";

/** A conversation itself, never a reply inside one: the turn that holds its thread and folds. */
export const ROOT_TURN = "[data-turn]:not([data-reply])";

/** The control that opens and closes a turn's fold other than a comment's thread: a handoff's
 *  Show fields, and the earlier replies of a thread that folds them (`EarlierReplies`). */
export const TURN_FOLD = "[data-turn-fold]";

/** Whether the turns drawn below are the Conversation tab's, which its keys walk. */
export const ConversationTurns = createContext(false);

/** The ring a turn draws while the keyboard's focus is on it. */
export const turnFocusRing = `outline-none focus-visible:ring-2 ${focusVisibleRing}`;

/** What a turn spreads on itself inside the tab: the focus the tab's keys give it, the shared row
 *  marker (`ROW_MARKER`), since a turn is a row of the tab's list that the page's focus rescue
 *  lands no loss the reader did not cause in, and the `dispatch://` reference `y` copies
 *  (`data-turn-ref`) where it has one - a handoff, and a message to a session that names no
 *  issue, have none. Outside the tab, nothing. */
export function turnMarks(
  inTab: boolean,
  route: DispatchReferenceRoute | undefined
): { "data-row-marker"?: ""; "data-turn-ref"?: string; tabIndex?: number } {
  if (!inTab) return {};
  return route === undefined
    ? { ...ROW_MARKER, tabIndex: -1 }
    : { ...ROW_MARKER, "data-turn-ref": buildDispatchReference(route), tabIndex: -1 };
}

/** The first control `selector` names inside `turn` that is the turn's own, not one of a reply
 *  drawn inside it. */
export function ownControl(turn: HTMLElement, selector: string): HTMLElement | null {
  for (const control of turn.querySelectorAll<HTMLElement>(selector)) {
    if (control.closest(TURN_ROW) === turn) return control;
  }
  return null;
}

/** A reply box (`MentionComposer`, `AskReplyComposer`): where `r` puts the caret, on the
 *  Conversation tab and in the Inbox. */
export const REPLY_BOX = "[data-reply-box]";

/** Puts the caret in the reply box behind `opener`, an answered ask's "Write a reply"
 *  (`data-reply-opener`, `AskThreadDisclosure`), which draws its box under it as it is pressed:
 *  the press is flushed, so the box stands in the page when the panel the control names is
 *  read. */
export function openReplyBox(opener: HTMLElement): void {
  if (opener.getAttribute("aria-expanded") !== "true") flushSync(() => opener.click());
  document
    .getElementById(opener.getAttribute("aria-controls") ?? "")
    ?.querySelector<HTMLElement>(REPLY_BOX)
    ?.focus();
}
