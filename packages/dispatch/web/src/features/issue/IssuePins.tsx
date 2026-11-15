import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Link } from "react-router-dom";

import { api } from "../../api/client";
import { PinButton } from "../../components/PinButton";
import { linkText, pageInkText, pageQuietText } from "../../theme/classes";
import { useMarginDrawer } from "../margin/margin-slot";
import { buildIssuePath } from "../refs/routes";
import { PHONE_VIEWPORT_QUERY, useMediaQuery } from "../shell/useDialog";
import { firstLineOf } from "./issue-tree";
import { authorText, pageChrome } from "./page-structure";
import { CONTROL_LABELS } from "./page-words";
import { writePin } from "./pin-write";
import { conversationItemId, pinnedConversations } from "./pins";

/**
 * The reader's pins, as links to the pinned rows (dispatch://INFRA-1196 R14): each pinned ask,
 * comment and message by its first line, a link to its own address, which focuses its row in the
 * outline (R20), with its Unpin beside it. A pin of what the outline draws no row for - a thread
 * agents wrote among themselves - links to its address too, which opens the history at it, and an
 * old pin of a handoff opens the history. The pin itself is the focused row's, in its facts
 * (`FocusedNodeFacts.tsx`), and the history's own lines pin their events (`IssueHistory.tsx`).
 *
 * The words come from the reads the outline is drawn from, under its keys, so the list costs no
 * request of its own.
 */
export function IssuePins({
  dismissed,
  issueKey,
}: {
  dismissed: readonly string[];
  issueKey: string;
}): ReactNode {
  const queryClient = useQueryClient();
  const drawer = useMarginDrawer();
  const phone = useMediaQuery(PHONE_VIEWPORT_QUERY);
  const pins = pinnedConversations([...dismissed]);
  const reading = pins.length > 0;
  const asks = useQuery({
    enabled: reading,
    queryFn: () => api.listIssueAsks(issueKey, "all"),
    queryKey: ["asks", issueKey],
  });
  const comments = useQuery({
    enabled: reading,
    queryFn: () => api.listComments(issueKey),
    queryKey: ["comments", issueKey],
  });
  const messages = useQuery({
    enabled: reading,
    queryFn: () => api.listIssueMessages(issueKey),
    queryKey: ["issue-messages", issueKey],
  });
  const wordsOf = (kind: string, id: string): string | undefined => {
    if (kind === "ask") return asks.data?.find((ask) => ask.id === id)?.question;
    if (kind === "comment") return comments.data?.find((comment) => comment.id === id)?.body;
    if (kind !== "message") return undefined;
    return (messages.data ?? [])
      .flatMap((thread) => [thread.message, ...(thread.replies ?? [])])
      .find((message) => message.id === id)?.body;
  };
  return (
    <section aria-label={CONTROL_LABELS.pinned} data-testid="issue-pins">
      <h2 className={`text-sm font-semibold ${pageInkText}`} {...pageChrome}>
        {CONTROL_LABELS.pinned}
      </h2>
      {pins.length === 0 ? (
        <p className={`text-xs ${pageQuietText}`} {...pageChrome}>
          {CONTROL_LABELS.none}
        </p>
      ) : (
        <ul className="mt-1 flex min-w-0 flex-col gap-0.5">
          {pins.map((pin) => {
            const id = conversationItemId(pin.kind, pin.id);
            const words = wordsOf(pin.kind, pin.id);
            return (
              <li className="flex min-w-0 items-center gap-2 text-sm" data-pin={id} key={id}>
                <Link
                  className={`dispatch-inline-link min-w-0 flex-1 truncate underline ${linkText}`}
                  onClick={() => {
                    // On a phone Details covers the outline the link focuses a row of.
                    if (phone) drawer?.setOpen(false);
                  }}
                  to={
                    pin.kind === "handoff"
                      ? buildIssuePath({ key: issueKey, kind: "activity" })
                      : buildIssuePath({ id: pin.id, key: issueKey, kind: pin.kind })
                  }
                  {...authorText}
                >
                  {words === undefined ? pin.id : firstLineOf(words).line}
                </Link>
                <PinButton
                  label={CONTROL_LABELS.unpin}
                  onClick={() => writePin(queryClient, issueKey, { id, op: "unpin" })}
                  pinned
                  quiet
                />
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
