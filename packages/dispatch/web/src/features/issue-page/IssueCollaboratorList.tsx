import { isLegionRole, LEGION_ROLES, type LegionRole } from "@legion/contracts";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { issueCollaboratorsQuery } from "../../api/queries";
import type {
  IssueCollaboratorAgent,
  IssueCollaboratorLegionAgent,
  IssueCollaboratorPerson,
  IssueCollaborators,
} from "../../api/types";
import {
  pageInkText,
  pageLineBorder,
  pageMutedText,
  pagePaperBg,
  pageQuietText,
} from "../../theme/classes";
import { AgentName } from "../refs/AgentName";
import { Timestamp } from "../refs/Timestamp";
import { DetailsErrorLine } from "./details-error";
import { PROPERTY_LABELS, pageChrome } from "./page-structure";
import { ANSWER_LINES, CONTROL_LABELS, EMPTY_STATE_LINES, STATE_LINES } from "./page-words";

/** The page's word for each Legion role's list, and for each way a collaborator worked the
 *  issue: every token the contract declares has one. */
const ROLE_WORDS: Readonly<Record<LegionRole, string>> = CONTROL_LABELS.collaborator_roles;
const REASON_WORDS: Readonly<
  Record<NonNullable<IssueCollaboratorPerson["reasons"]>[number], string>
> = CONTROL_LABELS.collaborator_reasons;

/** The three sections, in the order the reader meets them. Each holds the server's own
 *  order, which the contract states: the registry's agents first, by role in workflow order and
 *  then newest first, then the rest newest first, and people newest first. */
export interface CollaboratorSections {
  /** Legion agents by role in workflow order (`LEGION_ROLES`), each role's newest first. */
  readonly legion: readonly {
    readonly role: LegionRole;
    readonly agents: readonly IssueCollaboratorLegionAgent[];
  }[];
  /** Every other agent session, the most recently active first. */
  readonly others: readonly IssueCollaboratorAgent[];
  /** People, in the server's order (newest first). */
  readonly people: readonly IssueCollaboratorPerson[];
}

export function collaboratorSections(collaborators: IssueCollaborators): CollaboratorSections {
  /** Whether this row is held by a role this build knows, which is what puts it in a Legion
   *  section: a row with no Legion record, and one naming a role `LEGION_ROLES` does not hold,
   *  both belong to `others`. */
  const known = (agent: IssueCollaboratorAgent): agent is IssueCollaboratorLegionAgent =>
    agent.legion !== null && isLegionRole(agent.legion.role);
  const agents = collaborators.agents ?? [];
  const legion = agents.filter(known);
  return {
    // A section is the role's slice of the server's list, in the order it sent them: the server
    // ordered the registry's agents by role and then newest first, so a filter is the whole of
    // this. `LEGION_ROLES` decides which sections there are and where each one sits, which is
    // the same workflow order; a role nobody worked has no section.
    legion: LEGION_ROLES.flatMap((role) => {
      const agents = legion.filter((agent) => agent.legion.role === role);
      return agents.length === 0 ? [] : [{ agents, role }];
    }),
    // Every session no Legion section holds, so a registry entry naming a role this build does
    // not know is listed rather than dropped from the page. The server's order again.
    others: agents.filter((agent) => !known(agent)),
    people: collaborators.people ?? [],
  };
}

function SectionHeading({ children }: { children: ReactNode }): ReactNode {
  return (
    <h2 className={`text-sm font-semibold ${pageMutedText}`} {...pageChrome}>
      {children}
    </h2>
  );
}

/** One collaborator's row: who, then the line that says how they worked the issue, on the
 *  page's paper inside its line. `data-collaborator` marks the row's own name, which a test
 *  selects instead of the row's shape, since the detail line can name another session (the one
 *  that replaced this row's). */
function Row({ detail, name }: { detail: ReactNode; name: ReactNode }): ReactNode {
  return (
    <li className={`min-w-0 rounded-lg border p-3 break-words ${pageLineBorder} ${pagePaperBg}`}>
      <div className={`min-w-0 font-medium ${pageInkText}`} data-collaborator="">
        {name}
      </div>
      <div
        className={`mt-1 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-sm ${pageMutedText}`}
      >
        {detail}
      </div>
    </li>
  );
}

/** How a collaborator worked the issue - `assignee`, `claimed`, `posted`, `answered` - in the
 *  page's words, and when they last did. */
function Worked({
  lastAt,
  reasons,
}: {
  lastAt: string | null;
  reasons: IssueCollaboratorPerson["reasons"];
}): ReactNode {
  return (
    <>
      {(reasons ?? []).length === 0 ? null : (
        <span {...pageChrome}>
          {(reasons ?? []).map((reason) => REASON_WORDS[reason]).join(", ")}
        </span>
      )}
      {lastAt === null ? null : <Timestamp at={lastAt} />}
    </>
  );
}

/**
 * Everyone who worked the issue, running or not (`GET /issues/{key}/collaborators`): Legion's
 * agents by role, other agent sessions, and people. Each agent is an `AgentName` link to its live
 * conversation, its running state from the agents registry, so a replaced or stopped agent is one
 * click from the conversation it had; a person is their login as plain text, as everywhere.
 *
 * It is drawn in the People section of the issue page's one right-hand panel, behind that
 * section's own disclosure (dispatch://INFRA-1196 R12, `details-more.tsx`), where the nine-tab
 * page gave it a tab of its own.
 */
export function IssueCollaboratorList({ issueKey }: { issueKey: string }): ReactNode {
  const query = useQuery(issueCollaboratorsQuery(issueKey));
  if (query.isPending) {
    return (
      <p className={`text-sm ${pageQuietText}`} {...pageChrome}>
        {ANSWER_LINES.loading_collaborators}
      </p>
    );
  }
  if (query.isError) {
    return (
      <DetailsErrorLine
        message={ANSWER_LINES.collaborators_unreadable}
        onRetry={() => void query.refetch()}
        retrying={query.isFetching}
      />
    );
  }
  const collaborators = query.data;
  const sections = collaboratorSections(collaborators);
  const titles = new Map(
    (collaborators.agents ?? []).map((agent) => [agent.session_id, agent.title ?? undefined])
  );
  const unavailable = collaborators.legion_registry === "unavailable";
  const empty =
    (collaborators.agents ?? []).length === 0 && (collaborators.people ?? []).length === 0;
  return (
    <section aria-label={PROPERTY_LABELS.collaborators} className="min-w-0 space-y-6">
      {unavailable ? (
        <p className={`text-sm ${pageMutedText}`} role="status" {...pageChrome}>
          {STATE_LINES.legion_record_unreadable}
        </p>
      ) : null}
      {empty && !unavailable ? (
        <p className={`text-sm ${pageQuietText}`} {...pageChrome}>
          {EMPTY_STATE_LINES.no_collaborators}
        </p>
      ) : null}
      {sections.legion.length === 0 ? null : (
        <section aria-label={CONTROL_LABELS.collaborators_legion} className="space-y-3">
          <SectionHeading>{CONTROL_LABELS.collaborators_legion}</SectionHeading>
          {sections.legion.map(({ agents, role }) => (
            <section aria-label={ROLE_WORDS[role]} className="space-y-2" key={role}>
              <h3 className={`text-xs font-semibold uppercase ${pageQuietText}`} {...pageChrome}>
                {ROLE_WORDS[role]}
              </h3>
              <ul className="space-y-2">
                {agents.map((agent) => (
                  <Row
                    detail={
                      <>
                        <span>
                          <span {...pageChrome}>{CONTROL_LABELS.collaborator_started}</span>{" "}
                          <Timestamp at={agent.legion.started_at} />
                        </span>
                        {agent.legion.replaced_by === null ? null : (
                          <span data-replaced-by={agent.legion.replaced_by}>
                            <span {...pageChrome}>{CONTROL_LABELS.collaborator_replaced_by}</span>{" "}
                            <AgentName
                              inline
                              sessionId={agent.legion.replaced_by}
                              storedTitle={titles.get(agent.legion.replaced_by)}
                            />
                          </span>
                        )}
                      </>
                    }
                    key={agent.session_id}
                    name={
                      <AgentName
                        sessionId={agent.session_id}
                        storedTitle={agent.title ?? undefined}
                      />
                    }
                  />
                ))}
              </ul>
            </section>
          ))}
        </section>
      )}
      {sections.others.length === 0 ? null : (
        <section aria-label={CONTROL_LABELS.collaborators_others} className="space-y-2">
          <SectionHeading>{CONTROL_LABELS.collaborators_others}</SectionHeading>
          <ul className="space-y-2">
            {sections.others.map((agent) => (
              <Row
                detail={<Worked lastAt={agent.last_at} reasons={agent.reasons} />}
                key={agent.session_id}
                name={
                  <AgentName sessionId={agent.session_id} storedTitle={agent.title ?? undefined} />
                }
              />
            ))}
          </ul>
        </section>
      )}
      {sections.people.length === 0 ? null : (
        <section aria-label={CONTROL_LABELS.collaborators_people} className="space-y-2">
          <SectionHeading>{CONTROL_LABELS.collaborators_people}</SectionHeading>
          <ul className="space-y-2">
            {sections.people.map((person) => (
              <Row
                detail={<Worked lastAt={person.last_at} reasons={person.reasons} />}
                key={person.login}
                name={person.login}
              />
            ))}
          </ul>
        </section>
      )}
    </section>
  );
}
