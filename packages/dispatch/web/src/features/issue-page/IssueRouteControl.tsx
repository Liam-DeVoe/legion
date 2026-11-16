import type { ReactNode } from "react";

import { AGENT_LIST_REFILLING } from "../../api/client";
import type { IssueDetails } from "../../api/types";
import { focusVisibleRing, pageInkText, pageQuietText } from "../../theme/classes";
import {
  RouteReachUnknownMarker,
  UnreachableRouteMarker,
  UnresponsiveRouteMarker,
} from "../issue/RouteReach";
import { AgentName, useAgentText } from "../refs/AgentName";
import { sessionOfAddress } from "../refs/routes";
import { authorText, pageChrome } from "./page-structure";
import { CONTROL_LABELS } from "./page-words";
import { ROW_LINK } from "./row-references";
import type { IssueHeaderWrites } from "./useIssueHeaderWrites";

/**
 * Where a message on this issue goes, and whether it reaches anyone, in its row of Details
 * (dispatch://INFRA-1196 R13): the route as the control that opens its editor - a `session:`
 * route named as `AgentName` names an agent, with the agent's own live link beside it, a role
 * route as its address, and no route as the page's `None`, as every empty row of Details draws
 * it - and its reach in words. The header keeps one marker of its own, where the route reaches
 * nobody; every other reading of the route is here.
 *
 * The route is a value of Details, so it is drawn whole in the ink with the row's underline
 * (`ROW_LINK`), and never cut.
 */
export function IssueRouteControl({
  editing,
  isClosed,
  issue,
  onEdit,
  writes,
}: {
  /** Whether the route's editor is open under it, which hides the control that opens it. */
  editing: boolean;
  isClosed: boolean;
  issue: IssueDetails;
  onEdit: () => void;
  writes: IssueHeaderWrites;
}): ReactNode {
  const agentText = useAgentText();
  const { drafts } = writes;
  const routeSession = sessionOfAddress(drafts.route);
  const routeLabel = `Messages default to ${
    drafts.route === ""
      ? "no route"
      : routeSession === undefined
        ? drafts.route
        : agentText.sessionText(routeSession, undefined)
  }`;
  const routeControl = (name: ReactNode) => (
    <button
      aria-label={routeLabel}
      className={`inline-flex min-h-11 max-w-full min-w-0 items-center rounded-[7px] text-left text-sm [overflow-wrap:anywhere] outline-none focus-visible:ring-2 disabled:cursor-not-allowed md:min-h-0 ${ROW_LINK} ${pageInkText} ${focusVisibleRing}`}
      disabled={isClosed}
      onClick={onEdit}
      type="button"
      {...authorText}
    >
      {name}
    </button>
  );
  // The route's name in its control: the page's `None` where the issue has no route.
  const routeName =
    drafts.route === "" ? (
      <span className={pageQuietText} {...pageChrome}>
        {CONTROL_LABELS.none}
      </span>
    ) : (
      drafts.route
    );
  // A route whose reach is unknown says so; a listener refilling its agent list has not failed,
  // and says so rather than reading as a listener that did not answer.
  const reachKnown = issue.route === null || issue.route_status !== "unknown";
  const reachUnknown = reachKnown ? null : issue.route_status_reason === "refilling" ? (
    <span className={`shrink-0 text-xs ${pageQuietText}`} data-testid="issue-route-refilling">
      Route reach unknown: {AGENT_LIST_REFILLING}
    </span>
  ) : (
    <RouteReachUnknownMarker className="shrink-0" testId="issue-route-unknown" />
  );
  return (
    <>
      {editing ? null : routeSession === undefined ? (
        routeControl(routeName)
      ) : (
        <AgentName
          className="inline-flex min-w-0 items-center"
          control={routeControl}
          kind="control"
          sessionId={routeSession}
          storedTitle={undefined}
        />
      )}
      {/* Whether the route reaches anyone speaks for the saved route only, so a route being
          saved hides it until the next read judges it. */}
      {drafts.route === (issue.route ?? "") ? (
        <>
          <UnreachableRouteMarker issue={issue} testId="issue-details-route-unreachable" />
          <UnresponsiveRouteMarker issue={issue} />
          {reachUnknown}
        </>
      ) : null}
    </>
  );
}
