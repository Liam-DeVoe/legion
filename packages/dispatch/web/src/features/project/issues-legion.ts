import type { LegionBoardColumnID } from "../../api/types";

/**
 * The Legion daemon's phases, in the daemon's own order: the ten columns of the board route, which
 * the Legion phase column, its filter, the Board grouped by Legion phase and Time per phase
 * follow. An issue is in a phase only while the daemon holds a card for it; its Dispatch status
 * is a column of its own and never merged with the phase.
 */
export const LEGION_PHASES = [
  { id: "queue", label: "Queue" },
  { id: "design", label: "Design" },
  { id: "planning", label: "Planning" },
  { id: "implementing", label: "Implementing" },
  { id: "testing", label: "Testing" },
  { id: "reviewing", label: "Reviewing" },
  { id: "accepting", label: "Accepting" },
  { id: "merging", label: "Merging" },
  { id: "awaiting_person", label: "Waiting on a person" },
  { id: "held", label: "Held" },
] as const satisfies readonly { id: LegionBoardColumnID; label: string }[];

export type LegionPhase = (typeof LEGION_PHASES)[number]["id"];

export const legionPhaseIds: readonly LegionPhase[] = LEGION_PHASES.map((phase) => phase.id);

const phaseLabels: ReadonlyMap<string, string> = new Map(
  LEGION_PHASES.map((phase) => [phase.id, phase.label])
);

export function isLegionPhase(value: string): value is LegionPhase {
  return phaseLabels.has(value);
}

/** A phase's name, or the value as it is for one this bundle does not know. */
export function phaseLabel(phase: string): string {
  return phaseLabels.get(phase) ?? phase;
}

/** Why a card waits, in the order the Legion queue's bar draws them: it is ready to start, the
 *  daemon has no capacity free, a blocker link holds it, or a person has not answered it. The
 *  values are the board route's. */
export const WAITING_REASONS = ["ready", "slot", "blocked", "person"] as const;
export type WaitingReason = (typeof WAITING_REASONS)[number];
const waitingLabels: Record<WaitingReason, string> = {
  ready: "Ready",
  slot: "No capacity",
  blocked: "Blocked",
  person: "Waiting on a person",
};
export function waitingLabel(reason: string): string {
  return (waitingLabels as Record<string, string>)[reason] ?? reason;
}

/** What an issue the daemon holds capacity for is doing, the Activity column's values in their
 *  order. The values are the board route's `slot_use`. */
export const ACTIVITIES = [
  "working",
  "between_turns",
  "held_children",
  "held_person",
  "held_idle",
] as const;
export type Activity = (typeof ACTIVITIES)[number];
const activityLabels: Record<Activity, string> = {
  working: "Working",
  between_turns: "Between turns",
  held_children: "Held for children",
  held_person: "Held for a person",
  held_idle: "Held, idle",
};
export function activityLabel(activity: string): string {
  return (activityLabels as Record<string, string>)[activity] ?? activity;
}
