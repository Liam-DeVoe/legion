import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { Link, useLocation } from "react-router-dom";

import { api } from "../../api/client";
import {
  inboxQuery,
  prioritySummaryQuery,
  projectsQuery,
  userAgentStateQuery,
} from "../../api/queries";
import type { AuthenticatedUser } from "../../api/types";
import { ChevronIcon } from "../../components/DisclosureToggle";
import { PinButton } from "../../components/PinButton";
import {
  railActiveBg,
  railControlHoverBg,
  railCountBadge,
  railDangerText,
  railHoverBg,
  railHoverText,
  railMutedText,
  railNeedsYouBadgeBg,
  railNeedsYouBadgeText,
  railRowFocus,
  railSecondaryText,
  railText,
} from "../../theme/classes";
import { useLiveViewState } from "../agent-view/live-views";
import { useAgentPinWrite, usePinnedAgents } from "../agents/pins";
import { totalUnreadReplies, unreadRepliesLabel } from "../agents/unread";
import { useInboxCount } from "../inbox/useInboxCount";
import { PRIORITIES_PATH } from "../priorities/priority-routes";
import { AgentName, useAgentText } from "../refs/AgentName";
import {
  buildIssuePath,
  buildProjectPath,
  liveViewSessionOf,
  parseIssuePath,
  parseProjectPath,
  routeIsInbox,
} from "../refs/routes";

/** The rows the Agents heading folds, named so the heading's chevron can say what it controls. */
const PINNED_AGENTS_ID = "rail-pinned-agents";

/** One row of the rail: 32 px, 8 px of inset, one line. Every entry of the navigation is this
 *  box, so their words share one left edge and their counts one right edge. */
const ROW = "group relative flex min-h-8 items-center gap-2 rounded-md px-2 text-sm";
const ROW_RESTING = `${railSecondaryText} ${railHoverBg} ${railHoverText}`;
const ROW_CURRENT = `font-medium ${railActiveBg} ${railText}`;
/** The row's link, stretched over the whole row by its `::after` so a press anywhere on the row
 *  follows it, while a control beside it (Peek, the fold) stays a control of its own. */
const ROW_LINK = `flex min-w-0 flex-1 items-baseline gap-2 after:absolute after:inset-0 after:rounded-md ${railRowFocus}`;
/** A small control inside a row, drawn over the row's stretched link. */
const ROW_CONTROL = `relative flex size-6 shrink-0 items-center justify-center rounded ${railMutedText} ${railHoverText} ${railControlHoverBg}`;
const SECTION_LABEL = `px-2 pb-1 text-xs font-medium ${railMutedText}`;

/**
 * A count at the right edge of a row: the badge family's shape (20 px, 12 px type, 4 px
 * corners). Amber means the reader must act (`needsYou`); every other count is neutral. The row's
 * link already says the number, in words a screen reader reads with the link, so what is drawn
 * here is the same text as a generated box and is hidden from the accessibility tree: a second
 * copy in the document would be read twice and found twice.
 */
function RowBadge({
  needsYou = false,
  text,
  title,
}: {
  needsYou?: boolean;
  text: string;
  title?: string;
}): ReactNode {
  return (
    <span
      aria-hidden="true"
      className={`inline-flex h-5 min-w-5 shrink-0 items-center justify-center rounded px-1.5 text-xs font-medium tabular-nums before:content-[attr(data-count)] ${
        needsYou ? `${railNeedsYouBadgeBg} ${railNeedsYouBadgeText}` : railCountBadge
      }`}
      data-count={text}
      title={title}
    />
  );
}

/**
 * One pinned agent in the rail (INFRA-812, R1): its running state and name, both `AgentName`'s,
 * its unread replies, what its live view is doing (`live`, `live 30s` or `asleep`, the word the
 * registry computes), and the control that unpins it.
 *
 * The name is the link into the session's live view, and it `replace`s the address when the
 * reader is already in one: a move between pinned agents from the rail is one step of their
 * reading, so Back walks the pages they came from rather than every agent they looked at. Every
 * other link into a live view - `AgentName`'s elsewhere, the Inbox's, a pasted address - pushes.
 */
function PinnedAgentRow({
  onNavigate,
  onUnpin,
  replace,
  selected,
  sessionId,
  unread,
}: {
  onNavigate: (() => void) | undefined;
  onUnpin: () => void;
  replace: boolean;
  selected: boolean;
  sessionId: string;
  unread: number;
}): ReactNode {
  const name = useAgentText().sessionText(sessionId, undefined);
  const viewState = useLiveViewState(sessionId);
  return (
    <li
      aria-current={selected ? "page" : undefined}
      className={`flex min-h-8 items-center gap-2 rounded-md pr-1 pl-4 text-sm ${selected ? railActiveBg : railHoverBg}`}
    >
      <AgentName
        className="min-w-0 flex-1"
        onNavigate={onNavigate}
        replace={replace}
        sessionId={sessionId}
        storedTitle={undefined}
        tone="rail"
      />
      {unread === 0 ? null : (
        <span
          className={`inline-flex h-5 min-w-5 shrink-0 items-center justify-center rounded px-1.5 text-xs font-medium tabular-nums ${railNeedsYouBadgeBg} ${railNeedsYouBadgeText}`}
          title={unreadRepliesLabel(unread)}
        >
          {unread}
        </span>
      )}
      {/* What the session's view is doing, which only the registry knows: the rail renders the
          word and computes none of it (`live-views.ts`). */}
      <span className={`shrink-0 text-xs ${railMutedText}`}>{viewState}</span>
      <PinButton label={`Unpin ${name} from the sidebar`} onClick={onUnpin} onRail pinned quiet />
    </li>
  );
}

/**
 * One rail entry: its label, the words beside it, and its count when there is one. A project's
 * and a pinned issue's `detail` is its name, drawn muted on the same line and cut where the row
 * ends. A page's `detail` says what the page is: it is the link's `title` and part of its name,
 * and is not drawn (`detailShown` false), so the entry is one word on one line.
 */
function RailRow({
  badge,
  detail,
  detailShown = true,
  label,
  onNavigate,
  selected,
  to,
}: {
  badge: number | undefined;
  detail: string;
  detailShown?: boolean;
  label: string;
  onNavigate: (() => void) | undefined;
  selected: boolean;
  to: string;
}): ReactNode {
  return (
    <li className={`${ROW} ${selected ? ROW_CURRENT : ROW_RESTING}`}>
      <Link
        aria-current={selected ? "page" : undefined}
        className={ROW_LINK}
        onClick={onNavigate}
        title={detail}
        to={to}
      >
        <span
          className={`shrink-0 whitespace-nowrap ${detailShown ? `font-medium ${railText}` : ""}`}
        >
          {label}
        </span>
        <span className={detailShown ? `min-w-0 flex-1 truncate ${railMutedText}` : "sr-only"}>
          {detail}
        </span>
        {badge === undefined ? null : <span className="sr-only">{badge}</span>}
      </Link>
      {badge === undefined ? null : <RowBadge text={String(badge)} />}
    </li>
  );
}
export function Sidebar({
  onHide,
  onNavigate,
  onOpenInboxDrawer,
  user,
}: {
  onHide?: () => void;
  onNavigate?: () => void;
  onOpenInboxDrawer?: () => void;
  user: AuthenticatedUser;
}): ReactNode {
  const location = useLocation();
  const inbox = useQuery(inboxQuery());
  const pinned = useQuery({
    queryKey: ["issues", "pinned"],
    queryFn: () => api.listIssues({ pinned: true }),
  });
  const projects = useQuery(projectsQuery());
  // The number beside Priorities (INFRA-2947, R8): the two faults added - the work in flight
  // under no priority and the priority work parked in `backlog` or `icebox` - read from the
  // summary call alone, which answers neither list. An event that moves a relation, a status or
  // a parent refreshes it with the page (`api/event-refresh.ts`). A deployment that has written
  // no priority answers `empty` and no number is drawn, since nothing is yet off a list that
  // does not exist; a read that failed draws none either, rather than a zero it did not read.
  const prioritySummary = useQuery(prioritySummaryQuery()).data;
  const priorityFaults =
    prioritySummary === undefined || (prioritySummary.empty ?? "") !== ""
      ? undefined
      : prioritySummary.work_under_no_priority.count + prioritySummary.priority_work_parked.count;
  // One number on Inbox, composed in one place (`useInboxCount.ts`): the work waiting on
  // the reader plus the threads they have not read, which the Inbox lists beside those asks now.
  const inboxCount = useInboxCount();
  const agentState = useQuery(userAgentStateQuery()).data;
  const unreadReplies = totalUnreadReplies(agentState);
  // The rail's own list: the person's pinned agents, oldest pin first, and the control that
  // unpins one from here. The registry is told the whole set in the shell (`app.tsx`), not
  // here, because this rail is a sheet behind the Menu control below 1280 pixels and is not on
  // the page at all while the sidebar is collapsed.
  const pinnedAgents = usePinnedAgents();
  const writePin = useAgentPinWrite();
  const [agentsOpen, setAgentsOpen] = useState(true);
  // The live view the reader is in, which its row marks as the page and which makes a move to
  // another pinned agent a replacement rather than a step of its own.
  const currentLiveView = liveViewSessionOf(location.pathname);
  const currentIssue = parseIssuePath(location.pathname)?.key;
  const currentProject = parseProjectPath(location.pathname)?.project;
  const onInbox = routeIsInbox(location.pathname);
  const onAgents = location.pathname === "/agents";
  const onSettings = location.pathname === "/settings";

  const hideControl =
    onHide === undefined ? null : (
      <button
        aria-label="Hide sidebar"
        className={`absolute top-3 right-3 flex size-8 items-center justify-center rounded-md ${railMutedText} ${railHoverBg} ${railHoverText}`}
        onClick={onHide}
        title="Hide sidebar"
        type="button"
      >
        <svg aria-hidden="true" className="size-4" fill="none" viewBox="0 0 16 16">
          <path
            d="M9.5 4 5.5 8l4 4"
            stroke="currentColor"
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth="1.5"
          />
        </svg>
      </button>
    );

  if (inbox.isPending || pinned.isPending || projects.isPending) {
    return (
      <>
        {hideControl}
        <p className={`mt-4 px-2 text-sm ${railMutedText}`}>Loading navigation…</p>
      </>
    );
  }
  if (inbox.isError || pinned.isError || projects.isError) {
    return (
      <>
        {hideControl}
        <p className={`mt-4 px-2 text-sm ${railDangerText}`}>Could not load navigation.</p>
      </>
    );
  }

  return (
    <>
      {hideControl}
      <nav aria-label="Navigation" className="mt-4 space-y-5">
        <section>
          <ul className="space-y-0.5">
            <li className={`${ROW} ${onInbox ? ROW_CURRENT : ROW_RESTING}`}>
              <Link
                aria-current={onInbox ? "page" : undefined}
                className={ROW_LINK}
                onClick={onNavigate}
                to="/"
              >
                <span>Inbox</span>
                {inboxCount === 0 ? null : <span className="sr-only">{inboxCount}</span>}
              </Link>
              {/* The Inbox page is the Inbox already, so there is nothing to peek at from it.
                  The control shows while the pointer is on the row or the keyboard is on it,
                  and always where no pointer hovers. */}
              {onOpenInboxDrawer === undefined || onInbox ? null : (
                <button
                  aria-label="Peek at the inbox without leaving this page"
                  className={`relative h-6 shrink-0 rounded px-1.5 text-[13px] font-medium ${railMutedText} ${railHoverText} ${railControlHoverBg}`}
                  onClick={onOpenInboxDrawer}
                  title="Peek at the inbox without leaving this page"
                  type="button"
                >
                  Peek
                </button>
              )}
              {inboxCount === 0 ? null : <RowBadge needsYou text={String(inboxCount)} />}
            </li>
            <li>
              <div className={`${ROW} ${onAgents ? ROW_CURRENT : ROW_RESTING}`}>
                <Link
                  aria-current={onAgents ? "page" : undefined}
                  className={ROW_LINK}
                  onClick={onNavigate}
                  to="/agents"
                >
                  <span>Agents</span>
                  {unreadReplies === 0 ? null : (
                    <span className="sr-only">{unreadRepliesLabel(unreadReplies)}</span>
                  )}
                </Link>
                {unreadReplies === 0 ? null : (
                  <RowBadge needsYou text={unreadRepliesLabel(unreadReplies)} />
                )}
                {/* The heading stays the link to the Agents page, so the fold is a control of its
                    own beside it rather than the heading itself. */}
                <button
                  aria-controls={PINNED_AGENTS_ID}
                  aria-expanded={agentsOpen}
                  aria-label={agentsOpen ? "Hide pinned agents" : "Show pinned agents"}
                  className={`-mr-1 ${ROW_CONTROL}`}
                  onClick={() => setAgentsOpen(!agentsOpen)}
                  title={agentsOpen ? "Hide pinned agents" : "Show pinned agents"}
                  type="button"
                >
                  <ChevronIcon expanded={agentsOpen} />
                </button>
              </div>
              {agentsOpen ? (
                pinnedAgents.length === 0 ? (
                  <p
                    className={`py-1 pr-2 pl-4 text-xs leading-5 ${railMutedText}`}
                    id={PINNED_AGENTS_ID}
                  >
                    Pin an agent on the Agents page, or in its live view, to keep it here.
                  </p>
                ) : (
                  <ul className="mt-0.5 space-y-0.5" id={PINNED_AGENTS_ID}>
                    {pinnedAgents.map((sessionId) => (
                      <PinnedAgentRow
                        key={sessionId}
                        onNavigate={onNavigate}
                        onUnpin={() => writePin(sessionId, false)}
                        replace={currentLiveView !== undefined}
                        selected={sessionId === currentLiveView}
                        sessionId={sessionId}
                        unread={agentState?.[sessionId]?.unread_replies ?? 0}
                      />
                    ))}
                  </ul>
                )
              ) : null}
            </li>
            <RailRow
              badge={priorityFaults}
              detail="Priorities and the work under them"
              detailShown={false}
              label="Priorities"
              onNavigate={onNavigate}
              // The entry stands for the whole of the priorities, so it stays lit on one
              // priority's own page (`/priorities/<id>`) as well as on the list.
              selected={
                location.pathname === PRIORITIES_PATH ||
                location.pathname.startsWith(`${PRIORITIES_PATH}/`)
              }
              to={PRIORITIES_PATH}
            />
            <RailRow
              badge={undefined}
              detail="Deploys, merges, and pipeline failures"
              detailShown={false}
              label="Delivery"
              onNavigate={onNavigate}
              selected={location.pathname === "/delivery"}
              to="/delivery"
            />
            <RailRow
              badge={undefined}
              detail="CI and deploy step timings"
              detailShown={false}
              label="Pipeline"
              onNavigate={onNavigate}
              selected={location.pathname === "/pipeline"}
              to="/pipeline"
            />
          </ul>
        </section>
        {pinned.data.length === 0 ? null : (
          <section>
            <h2 className={SECTION_LABEL}>Pinned</h2>
            <ul className="space-y-0.5">
              {pinned.data.map((issue) => (
                <RailRow
                  badge={issue.open_asks === 0 ? undefined : issue.open_asks}
                  detail={issue.title}
                  key={issue.key}
                  label={issue.key}
                  onNavigate={onNavigate}
                  selected={issue.key === currentIssue}
                  to={buildIssuePath({ key: issue.key, kind: "issue" })}
                />
              ))}
            </ul>
          </section>
        )}
        <section>
          <h2 className={SECTION_LABEL}>Projects</h2>
          <ul className="space-y-0.5">
            {projects.data.map((project) => (
              <RailRow
                badge={
                  project.open_asks === undefined || project.open_asks === 0
                    ? undefined
                    : project.open_asks
                }
                detail={project.name}
                key={project.key}
                label={project.key}
                onNavigate={onNavigate}
                selected={project.key === currentProject}
                to={buildProjectPath({ kind: "project", project: project.key })}
              />
            ))}
          </ul>
        </section>
        {user.kind === "user" ? (
          <section>
            <ul>
              <li className={`${ROW} ${onSettings ? ROW_CURRENT : ROW_RESTING}`}>
                <Link
                  aria-current={onSettings ? "page" : undefined}
                  className={ROW_LINK}
                  onClick={onNavigate}
                  to="/settings"
                >
                  Settings
                </Link>
              </li>
            </ul>
          </section>
        ) : null}
      </nav>
    </>
  );
}
