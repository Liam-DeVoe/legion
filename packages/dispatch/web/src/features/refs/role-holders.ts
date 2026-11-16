import { createContext } from "react";

import type { Agent, IssueCollaboratorAgent } from "../../api/types";

/**
 * What makes a role known, defined once: a role is known while a session the agent registry
 * lists holds it, or while the collaborators of the issue the record is on record it, which are
 * that issue's Legion roles, running or not. The picker offers a role on this rule: the roles the
 * listed sessions hold, and the issue's stopped roles under "Not running" (`party-options.ts`).
 * The renderer names and links a role reference on the same rule (`reference-target.ts`). The
 * registry (`GET /api/v1/agents`) lists the live sessions and the unresponsive ones, each with
 * the roles it holds. The collaborators (`GET /api/v1/issues/{key}/collaborators`) list every
 * Legion role the issue ever had a session for (`legion.role_token`).
 *
 * Delivery follows the first half alone. A comment's role mention goes to the role's holder,
 * live or unresponsive, and fails at once where the role has none: the Go API's
 * `resolveDeliveryTarget`, through the Envoy client's `Role`. A stopped session holds no role, so
 * a role only its issue's collaborators record is linked and not told, as the writing box's hint
 * says.
 *
 * It is a module of its own rather than part of the picker's options, because the renderer reads
 * it too and the options module is the picker's.
 */

const holdersOf = new WeakMap<readonly Agent[], ReadonlyMap<string, Agent>>();

/** Every role the registry response `agents` lists a holder for, with that holder: built once per
 *  response and shared by every reader of it. */
export function roleHolders(agents: readonly Agent[]): ReadonlyMap<string, Agent> {
  const known = holdersOf.get(agents);
  if (known !== undefined) return known;
  const holders = new Map<string, Agent>();
  for (const agent of agents) {
    for (const role of agent.roles ?? []) holders.set(role, agent);
  }
  holdersOf.set(agents, holders);
  return holders;
}

const recordedOf = new WeakMap<readonly IssueCollaboratorAgent[], ReadonlySet<string>>();

/** No role recorded, for collaborators not read yet or an answer that lists no agent. */
const NO_ROLES: ReadonlySet<string> = new Set();

/** Every Legion role token an issue's collaborators `agents` record, running or not: built once
 *  per response and shared by every reader of it. */
export function recordedRoles(
  agents: readonly IssueCollaboratorAgent[] | null | undefined
): ReadonlySet<string> {
  if (agents === null || agents === undefined) return NO_ROLES;
  const known = recordedOf.get(agents);
  if (known !== undefined) return known;
  const roles = new Set<string>();
  for (const agent of agents) {
    if (agent.legion !== null) roles.add(agent.legion.role_token);
  }
  recordedOf.set(agents, roles);
  return roles;
}

/** The issue the record is on, where the text drawn inside is a record's (`TextSource.issue`),
 *  and undefined for a record on no issue or a text that is no record's. Each host of a record's
 *  text provides it to the references it draws: a rendered body, a one-line preview, the
 *  plain-text projection and the document editor. A reference then reads the collaborators of
 *  that issue and no other, and a host nested in another provides its own record's issue. */
export const RecordIssue = createContext<string | undefined>(undefined);
