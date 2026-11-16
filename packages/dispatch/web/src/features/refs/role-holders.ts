import type { Agent } from "../../api/types";

/**
 * What makes a role known, defined once: a role is known while a session the agent registry
 * lists holds it. The picker offers a role on this rule (`party-options.ts`), the renderer names
 * and links a role reference on it (`reference-target.ts`), and it is the rule the server's
 * delivery follows, so a picked role is one the reader links and the server tells, as the
 * writing box's hint says: the registry (`GET /api/v1/agents`) lists the live sessions and the
 * unresponsive ones, each with the roles it holds, and a comment's role mention is delivered to
 * its holder, live or unresponsive, and fails where the role has none (the Go API's
 * `resolveDeliveryTarget`, through the Envoy client's `Role`). A stopped session holds no role,
 * so no role is known by a session that has stopped.
 *
 * A module of its own rather than part of the picker's options: the renderer reads it too, and
 * the options module is the picker's.
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
