import type { Agent } from "../../api/types";
import type { AgentText } from "./AgentName";
import { roleHolders } from "./role-holders";

/**
 * The parties a box can address: the sessions the registry lists and the roles they hold, which
 * are the roles Dispatch knows (`roleHolders`). One list, read by the shared text field's
 * delivery modes (`MentionComposer.tsx`) and by the reference picker's party rows
 * (`useRefPicker.ts`), so a session offered as a mention and the same session offered as a
 * reference can never be two different rules about who is running, and a role is offered only
 * where the reader links it and the server tells its holder.
 *
 * A module of its own rather than part of the field: the field hosts the picker, so the picker
 * cannot read these from the field without the two importing each other (`noImportCycles`).
 *
 * It costs no read of its own: its source is the shared registry query (`agentsQuery`, which
 * the shell polls), already fetched.
 */
export interface MentionOption {
  readonly capabilities: readonly string[];
  /** Where and when: the directory of the session the option is or that holds its role, and
   *  when that session was last seen. */
  readonly detail: string;
  /** The name of the session the option is, or of the one holding its role, as `useAgentText`
   *  gives it: the filter matches it, and a role option's detail tooltip names it. */
  readonly holder: string;
  /** The session the option is, or, for a role, the session holding it: `AgentName` draws it. */
  readonly sessionId: string;
  /** That session's title as the agent list gave it: its name when the registry cache has none. */
  readonly sessionTitle: string;
  readonly target: string;
  /** The text the mention inserts (`@Title`) and the option's accessible name: a role's name, or
   *  the session's name as `useAgentText` gives it. */
  readonly title: string;
}

function roleOptions(
  agents: readonly Agent[],
  text: Pick<AgentText, "sessionText">
): MentionOption[] {
  return [...roleHolders(agents).entries()]
    .sort(([left], [right]) => {
      const leftController = left.includes("controller");
      const rightController = right.includes("controller");
      return leftController === rightController
        ? left.localeCompare(right)
        : leftController
          ? -1
          : 1;
    })
    .map(([role, agent]) => ({
      capabilities: agent.capabilities ?? [],
      detail: `${agent.dir} · ${new Date(agent.last_seen).toLocaleString()}`,
      holder: text.sessionText(agent.session_id, agent.title),
      sessionId: agent.session_id,
      sessionTitle: agent.title,
      target: `role:${role}`,
      title: role,
    }));
}

export function mentionOptions(
  agents: readonly Agent[],
  text: Pick<AgentText, "sessionText">
): MentionOption[] {
  return [
    ...roleOptions(agents, text),
    ...[...agents]
      .sort(
        (left, right) => right.last_seen - left.last_seen || left.title.localeCompare(right.title)
      )
      .map((agent) => ({
        capabilities: agent.capabilities ?? [],
        detail: `${agent.dir} · ${new Date(agent.last_seen).toLocaleString()}`,
        holder: text.sessionText(agent.session_id, agent.title),
        sessionId: agent.session_id,
        sessionTitle: agent.title,
        target: `session:${agent.session_id}`,
        title: text.sessionText(agent.session_id, agent.title),
      })),
  ];
}
