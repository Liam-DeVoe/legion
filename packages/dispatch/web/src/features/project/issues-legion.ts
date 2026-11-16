import type { LegionBoardColumnID, LegionBoardFacts } from "../../api/types";

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

/** The phases Time per phase draws, in the daemon's order: the Legion phases work is done in
 *  (Queue holds none), and Other last, the hours the board puts in no phase (`other`). */
export const WORK_PHASES = [
  ...LEGION_PHASES.filter(
    (phase): phase is Exclude<(typeof LEGION_PHASES)[number], { id: "queue" }> =>
      phase.id !== "queue"
  ),
  { id: "other", label: "Other" },
] as const;
export type WorkPhase = Exclude<LegionPhase, "queue"> | "other";
export const workPhaseIds: readonly WorkPhase[] = WORK_PHASES.map((phase) => phase.id);
const workPhaseLabels: ReadonlyMap<string, string> = new Map(
  WORK_PHASES.map((phase) => [phase.id, phase.label])
);
/** A work phase's name, or the value as it is for one this bundle does not know. */
export const workPhaseLabel = (phase: string): string => workPhaseLabels.get(phase) ?? phase;

/** The phase a key of `hours_by_phase` is drawn as: its own, or Other for one that is no work
 *  phase (Queue, or a key this bundle does not know). */
const workPhaseOf = (key: string): WorkPhase =>
  workPhaseLabels.has(key) ? (key as WorkPhase) : "other";

/** A row's hours by work phase, in `WORK_PHASES` order, only those with hours, and their sum. */
export function phaseHours(facts: LegionBoardFacts | undefined): {
  readonly phases: readonly (readonly [WorkPhase, number])[];
  readonly total: number;
} {
  const hours = new Map<WorkPhase, number>();
  for (const [key, value] of Object.entries(facts?.hours_by_phase ?? {})) {
    if (!(value > 0)) continue;
    const phase = workPhaseOf(key);
    hours.set(phase, (hours.get(phase) ?? 0) + value);
  }
  const phases = workPhaseIds.flatMap((phase) => {
    const value = hours.get(phase);
    return value === undefined ? [] : [[phase, value] as const];
  });
  return { phases, total: phases.reduce((sum, [, value]) => sum + value, 0) };
}

/** What holds a card up, the Why it waits column's values in their order, in this design's
 *  words: the daemon has not started it, it started and waits for capacity to go on, a blocker
 *  link holds it, or a person has not answered it. No value says "queue": the Queue column's
 *  In Legion queue is the one name of the queue. The daemon's rule (`CardWaiting`,
 *  `packages/envoy/internal/dispatch/legion/board_rules.go`) decides which; a card in the queue
 *  waits, and so does a root waiting on a person outside it. The values are the board route's
 *  `waiting.reason`. */
export const WAITING_REASONS = ["ready", "slot", "blocked", "person"] as const;
export type WaitingReason = (typeof WAITING_REASONS)[number];
const waitingLabels: Record<WaitingReason, string> = {
  ready: "Ready to start",
  slot: "Capacity to go on",
  blocked: "A blocker",
  person: "A person",
};
export function waitingLabel(reason: string): string {
  return (waitingLabels as Record<string, string>)[reason] ?? reason;
}

/** What the worker on an issue the daemon holds capacity for is doing, the Worker state
 *  column's values in their order. The values are the board route's `slot_use`. */
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
  held_children: "Waiting on child issues",
  held_person: "Waiting on a person",
  held_idle: "Idle",
};
export function activityLabel(activity: string): string {
  return (activityLabels as Record<string, string>)[activity] ?? activity;
}
