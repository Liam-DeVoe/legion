import type { ReactNode } from "react";
import { Link } from "react-router-dom";

import type { IssueDetails } from "../../api/types";
import { EmptyState } from "../../components/EmptyState";
import { cardClassName } from "../../components/Layout";
import { StatusPill } from "../../components/Pill";
import { borderDefault, textMutedOnSurface, textPrimaryOnSurface } from "../../theme/classes";
import { statusText } from "../project/board-model";
import { referenceTriggerProps } from "../refs/RefPreview";
import { buildIssuePath } from "../refs/routes";
import { Timestamp } from "../refs/Timestamp";
import { GitHubLink } from "./GitHubLink";

export function ChildrenTab({ issue }: { issue: IssueDetails }): ReactNode {
  if ((issue.children ?? []).length === 0) {
    return <EmptyState label="Children empty state" message="No child issues" />;
  }

  // One card of rows, not a card per child: the children are a list, and a rule between rows
  // reads as one where a stack of boxes read as so many separate objects. Each row is the
  // child's name at the start and what the header shows of an issue - its status, how far it
  // has got, when it last moved - at the end, on one line where the row has the width.
  return (
    <ul className={`overflow-hidden ${cardClassName("none")}`}>
      {(issue.children ?? []).map((child) => (
        <li
          className={`flex flex-wrap items-center gap-x-4 gap-y-1 border-t px-4 py-3 first:border-t-0 ${borderDefault}`}
          key={child.key}
        >
          <Link
            className={`group min-w-0 flex-1 basis-48 text-sm ${textPrimaryOnSurface}`}
            to={buildIssuePath({ key: child.key, kind: "issue" })}
            {...referenceTriggerProps({ key: child.key, kind: "issue" })}
          >
            <span className={`font-mono text-xs ${textMutedOnSurface}`}>{child.key}</span>
            <span className={textMutedOnSurface}> · </span>
            <span className="font-medium underline-offset-2 group-hover:underline">
              {child.title}
            </span>
          </Link>
          <div
            className={`flex flex-wrap items-center gap-x-3 gap-y-1 text-xs ${textMutedOnSurface}`}
          >
            {/* The same pill the issue header shows, with the same lifecycle label: a reader
                two inches below `In progress` must not meet `in_progress`. */}
            <StatusPill>{statusText(child.status)}</StatusPill>
            <span className="tabular-nums whitespace-nowrap">
              {child.subtree_done}/{child.subtree_total} done
            </span>
            <Timestamp at={child.active_at} />
            {(child.external_links ?? []).map((link) => (
              <span className="flex items-center" key={link.url}>
                <GitHubLink link={link} />
              </span>
            ))}
          </div>
        </li>
      ))}
    </ul>
  );
}
