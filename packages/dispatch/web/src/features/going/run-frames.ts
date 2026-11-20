import type { GoingBlockData } from "../../api/types";
import { type RunEvent, type RunState, runNext } from "./run-states";

/**
 * One run as its frames have built it so far (`GET /api/v1/going/runs/{id}/events`): its steps -
 * each model reply and each tool call, a tool call showing as working from the reply that made it
 * until its own frame says how it ended - the prose the model wrote, the blocks the server drew,
 * the plan it proposed and how its turn ended. Every field is a frame's; nothing is guessed.
 */

/** A tool call of a run: its place among the run's steps (`id`, which never changes), its tool,
 *  its one-line note once it ended, and whether it was refused. */
export interface RunStep {
  readonly id: number;
  readonly tool: string;
  /** The step the call is, once its frame arrived; undefined while it runs. */
  readonly step: number | undefined;
  readonly note: string;
  readonly refused: boolean;
  readonly running: boolean;
}

/** How a run's turn ended, as its `end` frame says it. */
export interface RunEnd {
  readonly state: string;
  readonly reason: string;
  readonly offers: readonly string[];
}

/** A run as its frames say it is. */
export interface RunFrames {
  readonly state: RunState;
  readonly steps: readonly RunStep[];
  /** The model's prose, each reply's in order: the last is the answer. */
  readonly replies: readonly string[];
  readonly blocks: readonly GoingBlockData[];
  /** The plan's number and state as the last plan frame said them. */
  readonly plan: { readonly number: number; readonly state: string } | undefined;
  /** How many plan frames came: each tells the bar to read the plan again. */
  readonly planFrames: number;
  readonly end: RunEnd | undefined;
}

/** A run that has just started, with no frame yet. */
export const STARTED_RUN: RunFrames = {
  blocks: [],
  end: undefined,
  plan: undefined,
  planFrames: 0,
  replies: [],
  state: "working",
  steps: [],
};

function text(value: unknown): string {
  return typeof value === "string" ? value : "";
}

/** The event an `end` frame is to the run's state: a turn that waits on its plan, done, failed,
 *  stopped by a bound of its level (it offers Continue), or stopped by its person. */
function endEvent(end: RunEnd): RunEvent {
  switch (end.state) {
    case "waiting":
      return "propose_plan";
    case "done":
      return "end_done";
    case "failed":
      return "end_failed";
    default:
      return end.offers.includes("continue") ? "bound" : "end_stopped";
  }
}

/** The run with one more frame, `name` and `data` as its stream carried them. A frame of a name
 *  this page does not know is one a later Dispatch added, and changes nothing. */
export function withRunFrame(run: RunFrames, name: string, data: unknown): RunFrames {
  const fields: Readonly<Record<string, unknown>> =
    typeof data === "object" && data !== null ? (data as Record<string, unknown>) : {};
  switch (name) {
    case "reply": {
      const tools = Array.isArray(fields.tools)
        ? fields.tools.filter((tool): tool is string => typeof tool === "string")
        : [];
      const reply = text(fields.text);
      return {
        ...run,
        replies: reply === "" ? run.replies : [...run.replies, reply],
        state: runNext(run.state, "step").state,
        steps: [
          ...run.steps,
          ...tools.map((tool, index) => ({
            id: run.steps.length + index,
            note: "",
            refused: false,
            running: true,
            step: undefined,
            tool,
          })),
        ],
      };
    }
    case "tool": {
      const tool = text(fields.tool);
      const at = run.steps.findIndex((step) => step.running && step.tool === tool);
      const ended: RunStep = {
        id: at === -1 ? run.steps.length : at,
        note: text(fields.note),
        refused: fields.error === true,
        running: false,
        step: typeof fields.step === "number" ? fields.step : undefined,
        tool,
      };
      // The call's frame ends the first call of its tool still shown as running; a call no reply
      // named (a row of an approved plan the runner made) is a step of its own.
      const steps =
        at === -1
          ? [...run.steps, ended]
          : run.steps.map((step, index) => (index === at ? ended : step));
      return { ...run, state: runNext(run.state, "step").state, steps };
    }
    case "block":
      return typeof data === "object" && data !== null
        ? { ...run, blocks: [...run.blocks, data as GoingBlockData] }
        : run;
    case "plan": {
      const number = typeof fields.plan === "number" ? fields.plan : 0;
      const state = text(fields.state);
      return {
        ...run,
        plan: { number, state },
        planFrames: run.planFrames + 1,
        state: runNext(run.state, state === "waiting" ? "propose_plan" : "step").state,
      };
    }
    case "end": {
      const end: RunEnd = {
        offers: Array.isArray(fields.offers)
          ? fields.offers.filter((offer): offer is string => typeof offer === "string")
          : [],
        reason: text(fields.reason),
        state: text(fields.state),
      };
      // A run whose end comes is no longer running anything: a call still shown as working was cut.
      const steps = run.steps.map((step) => (step.running ? { ...step, running: false } : step));
      return { ...run, end, state: runNext(run.state, endEvent(end)).state, steps };
    }
    default:
      return run;
  }
}

/** The run's state as its record says it, for a run whose frames are not read yet (History). */
export function stateOfRecord(state: string, stopReason: string | null): RunState {
  switch (state) {
    case "queued":
    case "working":
      return "working";
    case "waiting":
      return "waiting";
    case "done":
      return "done";
    case "failed":
      return "failed";
    case "stopped":
      return stopReason?.startsWith("STOPPED:") === true ? "stopped_person" : "stopped_bound";
    default:
      return "idle";
  }
}
