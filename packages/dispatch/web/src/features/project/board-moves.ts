import { useQueryClient } from "@tanstack/react-query";
import { useCallback, useState } from "react";

import { ApiError, issuePatchRequest } from "../../api/client";
import { boardMoveMutationKey } from "../../api/queries";
import type { IssueSummary, UpdateIssueInput } from "../../api/types";
import { useKeyedWrite } from "../writes/useKeyedWrite";
import type { BoardChange } from "./board-model";
import { projectIssuesQueryKey } from "./issue-filters";

export const staleBoardMessage =
  "The board changed while you were moving this card - refreshed, try again.";

/** The issue as a move leaves it on the page until the server answers: its new status, its new
 *  priority and a rank between its new neighbours, where the move writes each. */
function movedIssue(issue: IssueSummary, { input, rank }: BoardChange): IssueSummary {
  return {
    ...issue,
    ...(typeof input.status === "string" ? { status: input.status } : {}),
    ...(input.priority === undefined ? {} : { priority: input.priority }),
    ...(rank === undefined ? {} : { rank }),
  };
}

/** What one board move sends: the issue (`key`) and its change (`input`). */
export interface BoardMoveVariables {
  readonly input: UpdateIssueInput;
  readonly key: string;
}

/**
 * The one path every Board move takes - pointer, touch and keyboard drops alike. `move` places
 * the card on the page at once, in every listing the views read that holds it (the open listing
 * and the reads of done issues, under `projectIssuesQueryKey(project, [])`), sends the PATCH the
 * Board's rules planned (`planMove`), and reads the listings again once the server has answered.
 * Moves on one Board are serialised through a single mutation scope, so a second drop is planned
 * from the first one's placement and never overtakes it on the wire. A listing the server
 * answered before the move never lands over the placement: a fetch already on the wire is
 * cancelled as the move writes, and one that starts while a move is unanswered waits for the
 * answer (`projectIssuesQuery`, keyed on `boardMoveMutationKey`). Any failure puts the listings
 * back, reads them again, and says so: a stale rank (`RANK_INPUT`, the neighbours moved under the
 * drag) gets `staleBoardMessage`; anything else the server's message.
 */
export function useBoardMoves(project: string): {
  error: string | undefined;
  move: (key: string, change: BoardChange) => Promise<void>;
} {
  const queryClient = useQueryClient();
  const [error, setError] = useState<string>();
  // The board says why a move was refused, over the card it rolled back. A move is one write to
  // its issue, so it goes in press order with every other write to that issue (`issue:<KEY>`) -
  // the header's status, its fields, a components save.
  const patch = useKeyedWrite({
    inline: true,
    label: ({ key }: BoardMoveVariables) => `${key} moved on the board`,
    target: ({ key }) => `issue:${key}`,
    request: ({ input, key }) => issuePatchRequest(key, input),
    mutationKey: boardMoveMutationKey(project),
    scope: { id: `board:${project}` },
  });
  const { mutateAsync } = patch;
  const move = useCallback(
    async (key: string, change: BoardChange) => {
      const listings = projectIssuesQueryKey(project, []);
      // A listing query holds the issues `listIssues` answers; the page reads and the counts
      // under the same prefix hold no such list, and no card.
      const held = queryClient
        .getQueriesData<unknown>({ queryKey: listings })
        .flatMap(([queryKey, data]) =>
          Array.isArray(data) && (data as IssueSummary[]).some((issue) => issue.key === key)
            ? [{ queryKey, issues: data as IssueSummary[] }]
            : []
        );
      for (const { queryKey, issues } of held) {
        // Not awaited: the cancel takes effect synchronously, and the write must land in this
        // tick so a second move is planned from it. The read below restores what it drops.
        void queryClient.cancelQueries({ queryKey, exact: true });
        queryClient.setQueryData(
          queryKey,
          issues.map((issue) => (issue.key === key ? movedIssue(issue, change) : issue))
        );
      }
      try {
        await mutateAsync({ input: change.input, key });
        setError(undefined);
      } catch (cause) {
        for (const { queryKey, issues } of held) queryClient.setQueryData(queryKey, issues);
        setError(
          cause instanceof ApiError
            ? cause.code === "RANK_INPUT"
              ? staleBoardMessage
              : cause.message
            : "Could not move the card."
        );
      } finally {
        await queryClient.invalidateQueries({ queryKey: listings });
      }
    },
    [mutateAsync, project, queryClient]
  );
  return { error, move };
}
