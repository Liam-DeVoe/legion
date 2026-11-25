import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { api } from "../../api/client";
import { EmptyState } from "../../components/EmptyState";
import { PageHeader } from "../../components/Layout";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import { LabelPill } from "../../components/Pill";
import {
  borderDefault,
  card,
  dangerText,
  linkChrome,
  rowHoverBg,
  textMutedOnCanvas,
  textPrimaryOnCanvas,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { capabilityLabel } from "../conversation/delivery";
import { AgentName } from "../refs/AgentName";
import { MarkdownPreview } from "../refs/MarkdownPreview";
import { ReferenceHostIs } from "../refs/reference-host";
import { Timestamp } from "../refs/Timestamp";
import { useDocumentTitle } from "../shell/useDocumentTitle";

/** Every broadcast this Dispatch has sent, newest first, so one sent minutes ago is reachable
 *  again after the sender navigated away from it. */
export function BroadcastsPage(): ReactNode {
  useDocumentTitle("Broadcasts · Dispatch");
  const broadcasts = useQuery({
    queryFn: () => api.listBroadcasts(),
    queryKey: ["broadcast", "list"],
  });

  if (broadcasts.isPending) return <LoadingSkeleton label="Loading broadcasts" />;
  if (broadcasts.isError) {
    return (
      <p className={dangerText}>
        Could not load broadcasts:{" "}
        {broadcasts.error instanceof Error ? broadcasts.error.message : "network error"}
      </p>
    );
  }

  return (
    <section aria-label="Broadcasts">
      <PageHeader
        description="One message sent to many agents, and how each of them answered."
        eyebrow={
          <Link className={linkChrome} to="/agents">
            ← Agents
          </Link>
        }
        title="Broadcasts"
      />
      {broadcasts.data.length === 0 ? (
        <EmptyState
          label="Broadcasts empty state"
          message="No broadcast has been sent from Dispatch yet."
        />
      ) : (
        <ul className="space-y-2">
          {broadcasts.data.map((sent) => (
            <li
              className={`rounded-lg border ${card} ${borderDefault} ${rowHoverBg}`}
              key={sent.id}
            >
              {/* The sender is drawn by `AgentName`, whose name links to its live conversation,
                  so it sits above the row's link rather than inside it: one link never holds
                  another. A long name wraps rather than widening the row. */}
              <p
                className={`min-w-0 break-words px-4 pt-3 text-sm font-medium ${textSecondaryOnSurface}`}
              >
                <AgentName actor={sent.author} />
              </p>
              {/* Below `xl` every link is `inline-flex` with centred items (styles.css's touch
                  rule, unlayered, so it beats `block` and `items-*`): the row stacks its two lines
                  explicitly and each stretches to the row's width. */}
              {/* Who, what, then how it went: the message is the row's own words, and the mode,
                  the age and the tally are the muted line under it. */}
              <ReferenceHostIs host="control">
                <Link
                  className="flex w-full flex-col rounded-b-lg px-4 pt-0.5 pb-3"
                  to={`/agents/broadcasts/${sent.id}`}
                >
                  {/* No `source`: why a broadcast is no record is the reading list's
                      `broadcasts-list-body` (`DISPATCH_READING_SURFACES`). */}
                  <MarkdownPreview
                    className={`self-stretch text-sm ${textPrimaryOnCanvas}`}
                    fullTitle
                    lines={1}
                    markdown={sent.body}
                    surface="broadcasts-list-body"
                    tree={sent.body_tree}
                  />
                  <p
                    className={`mt-1.5 flex flex-wrap items-center gap-x-1.5 gap-y-1 self-stretch text-xs ${textMutedOnCanvas}`}
                  >
                    <LabelPill>{capabilityLabel(sent.delivery)}</LabelPill>
                    <Timestamp at={sent.created_at} />
                    <span aria-hidden>·</span>
                    <span>
                      {sent.replies} of {sent.recipients} answered
                    </span>
                  </p>
                </Link>
              </ReferenceHostIs>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
