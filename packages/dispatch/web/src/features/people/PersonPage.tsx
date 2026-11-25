import { useInfiniteQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { api } from "../../api/client";
import { nextOffsetPage } from "../../api/queries";
import { EmptyState } from "../../components/EmptyState";
import { PageHeader } from "../../components/Layout";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import { LabelPill } from "../../components/Pill";
import { ShowMore } from "../../components/ShowMore";
import {
  borderDefault,
  card,
  dangerText,
  linkChrome,
  textMutedOnCanvas,
} from "../../theme/classes";
import { PostPreview } from "../refs/PostBody";
import { EditedMark } from "../refs/PostMarks";
import { postDeletion } from "../refs/post-marks";
import { parseIssuePath } from "../refs/routes";
import { Timestamp } from "../refs/Timestamp";
import { textSourceKind, textSourceOf } from "../refs/text-references";
import { useDocumentTitle } from "../shell/useDocumentTitle";

const PAGE_SIZE = 50;

/** What each tie reads as on the page. A row carries one or both: a comment a person wrote that
 *  was then delivered back to a session of theirs is authored and received at once. */
const RELATION_LABELS: Record<string, string> = {
  authored_by: "Authored",
  delivered_to: "Received",
};

/** `/people/<login>`: one person's page, from one `GET /api/v1/people/{login}/nodes` — what they
 *  wrote and what reached them, newest first, read from the node and tuple store. Every row is
 *  re-read from that route: the page holds no list of its own and walks no edge, because the
 *  route takes no kind, filter or traversal. A comment its author deleted keeps its row, drawn
 *  as the deleted line in place of its title (`DeletedBody`), and one its author edited says
 *  ` · edited` after its kind in the row's byline (`EditedMark`), as every surface that prints a
 *  post marks it (dispatch://INFRA-1523, R2 and R3). A row with neither mark draws exactly what
 *  it drew before the marks. */
export function PersonPage(): ReactNode {
  const { login = "" } = useParams();
  useDocumentTitle(`${login} · Dispatch`);
  const nodes = useInfiniteQuery({
    getNextPageParam: nextOffsetPage,
    initialPageParam: 0,
    queryFn: ({ pageParam }) => api.listPersonNodes(login, { limit: PAGE_SIZE, offset: pageParam }),
    queryKey: ["people", login, "nodes"],
  });

  if (nodes.isPending) return <LoadingSkeleton label="Loading this person's nodes" />;
  if (nodes.isError) {
    return <p className={dangerText}>Could not load this page: {nodes.error.message}</p>;
  }
  const rows = nodes.data.pages.flatMap((page) => page.rows ?? []);

  return (
    <section aria-label="Person">
      <PageHeader
        description="What this person wrote and what reached them, newest first."
        eyebrow={
          <Link className={linkChrome} to="/">
            ← Inbox
          </Link>
        }
        title={<span className="break-words">{nodes.data.pages[0]?.person ?? login}</span>}
      />
      {rows.length === 0 ? (
        <EmptyState
          label="Person empty state"
          message="Nothing this person wrote and nothing delivered to them is recorded yet."
        />
      ) : (
        <ul aria-label="Person nodes" className="space-y-2">
          {rows.map((row) => {
            const deleted = postDeletion(row);
            return (
              // What was written leads; under it one muted line says what it is and when, and
              // Open sits at the row's right, so a row is two lines and not four.
              <li
                className={`flex items-center gap-x-4 rounded-lg border px-4 py-3 ${card} ${borderDefault}`}
                key={`${row.kind}:${row.id}`}
              >
                <div className="min-w-0 flex-1">
                  {/* The line reaches the page with no tree: the person nodes read sends none, so
                      the preview parses it (`UNTREED_ROWS` in `refs/surface-lists.test.ts`). */}
                  <PostPreview
                    className="text-sm"
                    deleted={deleted}
                    fullTitle
                    lines={2}
                    markdown={row.title}
                    source={textSourceOf(
                      textSourceKind(row.kind),
                      row.id,
                      // The row carries no issue key; a comment on an issue links to that issue's
                      // conversation, so a role reference in its line is read by that issue.
                      parseIssuePath(row.href)?.key
                    )}
                    surface="person-node-title"
                  />
                  <div
                    className={`mt-1.5 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-xs ${textMutedOnCanvas}`}
                  >
                    {(row.relations ?? []).map((relation) => (
                      <LabelPill key={relation}>{RELATION_LABELS[relation] ?? relation}</LabelPill>
                    ))}
                    <span>
                      {row.kind}
                      <EditedMark at={row.edited_at} deleted={deleted} />
                    </span>
                    <span aria-hidden>·</span>
                    <Timestamp at={row.created_at} />
                  </div>
                </div>
                <Link
                  className={`inline-flex h-8 shrink-0 items-center text-sm font-medium ${linkChrome}`}
                  to={row.href}
                >
                  Open
                </Link>
              </li>
            );
          })}
        </ul>
      )}
      <ShowMore
        fetching={nodes.isFetchingNextPage}
        hasMore={nodes.hasNextPage}
        onFetch={() => void nodes.fetchNextPage()}
      />
    </section>
  );
}
