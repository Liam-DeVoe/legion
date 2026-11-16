import type { ReactNode } from "react";

import type { IssueDetails, UserState } from "../../api/types";

/** What each of Details' three places for part M is handed: the issue as the details slot reads
 *  it, and the reader's own state of every issue. */
export interface DetailsMoreProps {
  readonly issue: IssueDetails;
  readonly state: UserState | undefined;
}

/** The people of the issue, under Opened by in Details' People section: its subscribers and its
 *  collaborators (dispatch://INFRA-1196 R13). Part M fills it; it draws nothing until then. */
export function DetailsPeople(_props: DetailsMoreProps): ReactNode {
  return null;
}

/** The facts of the row the reader's focus is on, between Details' sections and Pinned
 *  (dispatch://INFRA-1196 R12). Part M fills it; it draws nothing until then. */
export function DetailsFocusedNode(_props: DetailsMoreProps): ReactNode {
  return null;
}

/** The issue's history, under Pinned (dispatch://INFRA-1196 R15). Part M fills it; it draws
 *  nothing until then. */
export function DetailsHistory(_props: DetailsMoreProps): ReactNode {
  return null;
}
