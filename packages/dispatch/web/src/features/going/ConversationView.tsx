import type { ReactNode } from "react";

import { CopyButton } from "../../components/CopyButton";
import {
  dangerText,
  linkHoverText,
  linkText,
  secondaryButtonCompact,
  surfaceMutedBg,
  textMutedOnSurface,
  textPrimaryOnSurface,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { MarkdownBody } from "../refs/MarkdownBody";
import { AnswerBlock } from "../search/blocks/AnswerBlock";
import {
  asksPerson,
  dropQueued,
  newestRun,
  type QueuedWords,
  type RunEntry,
  type StartingRun,
  shownState,
  useGoing,
} from "./going-store";
import { PlanCard } from "./PlanRows";
import { RunUndo } from "./RunUndo";
import { type RunState, runEnded } from "./run-states";
import { RunTimer, StepSection } from "./step-section/StepSection";
import { type PlanAnswering, usePlan } from "./usePlan";

/** How a run stands, in the person's words, for the line under its steps. */
const STATE_TEXT: Readonly<Record<RunState, string>> = {
  done: "Done",
  failed: "Failed",
  idle: "",
  stopped_bound: "Stopped at a bound",
  stopped_person: "Stopped",
  waiting: "A plan waits for you",
  working: "Working…",
};

/**
 * The live line under a question until its run's first step or answer comes (design v2, rule 4):
 * "Thinking…" and the time since the person asked, counted each second (`RunTimer`). A screen
 * reader hears "Thinking…" once.
 */
function Thinking({ since }: { readonly since: number }): ReactNode {
  return (
    <p className={`text-xs ${textMutedOnSurface}`} data-testid="going-thinking">
      <span role="status">Thinking…</span> <RunTimer since={since} />
    </p>
  );
}

/** The person's words, as a filled grey bubble: a question of the conversation, or words that
 *  wait for the run to end. */
function Words({ testId, words }: { readonly testId: string; readonly words: string }): ReactNode {
  return (
    <p
      className={`w-fit max-w-full rounded-lg px-3 py-2 text-sm break-words whitespace-pre-wrap ${surfaceMutedBg} ${textPrimaryOnSurface}`}
      data-testid={testId}
    >
      {words}
    </p>
  );
}

/** Whether `run` works and nothing of it has come yet: no step, no words of the model, no block
 *  and no plan. */
function thinking(run: RunEntry): boolean {
  const frames = run.frames;
  return (
    shownState(run) === "working" &&
    frames.steps.length === 0 &&
    frames.replies.length === 0 &&
    frames.working === "" &&
    frames.blocks.length === 0 &&
    frames.plan === undefined
  );
}

/**
 * How long `run` worked, in milliseconds, for the line its steps fold into: the working time its
 * record says, where the record was read once the run no longer worked (History lists it so, and
 * a stop answers it); else the page's clock from the person's question to the frame that put the
 * run's work to rest, for a run whose record was read while it worked or that started here.
 * Undefined while it works, and where neither says.
 */
function workedMs(run: RunEntry): number | undefined {
  const { record } = run;
  const recordEnded = record.state !== "queued" && record.state !== "working";
  if (recordEnded && record.used.working_ms > 0) {
    return record.used.working_ms;
  }
  return run.settledAt !== undefined && (!recordEnded || run.startedHere)
    ? run.settledAt - run.since
    : undefined;
}

/**
 * What stands under an answer (design v2, state 4), small and grey: "Done", or "Waits for your
 * answer" where the answer asks the person a question (R8); and on hover or focus, Copy, which
 * copies the answer's text, and Retry, a new run of the same words.
 */
function AnswerFoot({
  answer,
  asks,
  onRetry,
}: {
  readonly answer: string;
  readonly asks: boolean;
  readonly onRetry: () => void;
}): ReactNode {
  return (
    <div className="group flex flex-wrap items-center gap-1" data-testid="going-run-foot">
      <p className={`text-xs ${textMutedOnSurface}`} data-testid="going-run-state" role="status">
        {asks ? "Waits for your answer" : "Done"}
      </p>
      <div className="flex items-center opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100 pointer-coarse:opacity-100">
        <CopyButton label="Copy the answer" value={answer} what="answer" />
        <button
          className={`inline-flex min-h-11 items-center rounded-lg px-2 text-xs md:min-h-8 ${linkText} ${linkHoverText}`}
          data-testid="going-run-retry"
          onClick={onRetry}
          type="button"
        >
          Retry
        </button>
      </div>
    </div>
  );
}

/** One run of the conversation: the person's words, its steps, the answer with its items as
 *  chips and blocks, its plan where it is the newest (the one `answering` is given for), how it
 *  ended with Continue on a bound, and Undo once it changed something. `asks` is whether its
 *  answer asks the person a question and waits for their answer. */
function RunTurn({
  answering,
  asks,
  onContinue,
  onRetry,
  run,
}: {
  readonly answering: PlanAnswering | undefined;
  readonly asks: boolean;
  readonly onContinue: (run: string) => void;
  readonly onRetry: (words: string) => void;
  readonly run: RunEntry;
}): ReactNode {
  const newest = answering !== undefined;
  const own = usePlan(newest ? undefined : run.id, newest ? 0 : run.frames.planReads);
  const plan = answering ?? own;
  const state = shownState(run);
  const answer = run.frames.replies.at(-1);
  const end = run.frames.end;
  const waitsForSteps = thinking(run);
  const working = state === "working";
  // A call with no words draws no line, and nor does a change of an approved plan, which its
  // row's tick on the plan's card shows.
  const steps = run.frames.steps.filter((step) => step.label !== "" && step.kind !== "write");
  // Undo is offered on a run that changed something: a request of it landed, or a row of its
  // plan ran.
  const changed =
    (run.record.issues ?? []).length > 0 ||
    (plan.plan?.rows ?? []).some((row) => row.state === "done");
  return (
    <li className="space-y-2" data-run={run.id} data-state={state} data-testid="going-run">
      <Words testId="going-run-words" words={run.record.words} />
      {waitsForSteps ? <Thinking since={run.since} /> : null}
      <StepSection
        folded={!working && (answer !== undefined || run.frames.plan !== undefined)}
        live={working}
        since={run.since}
        steps={steps}
        workedMs={workedMs(run)}
      />
      {run.reconnecting ? (
        <p
          className={`text-xs ${textMutedOnSurface}`}
          data-testid="going-run-reconnecting"
          role="status"
        >
          Reconnecting…
        </p>
      ) : null}
      {answer === undefined ? null : (
        <div className={`text-sm ${textPrimaryOnSurface}`} data-testid="going-run-answer">
          <MarkdownBody chips markdown={answer} referenceText="" softBreaks="line" />
        </div>
      )}
      {run.frames.withheld > 0 ? (
        <p className={`text-xs ${textSecondaryOnSurface}`} data-testid="going-run-withheld">
          {run.frames.withheld === 1
            ? "The model's content filter held back one result, so this answer may miss it."
            : `The model's content filter held back ${run.frames.withheld} results, so this answer may miss them.`}
        </p>
      ) : null}
      {run.frames.blocks.map((block, index) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: a block is its place in the run, and blocks are only ever added
        <AnswerBlock block={block} key={index} />
      ))}
      {answering === undefined ? null : <PlanCard answering={answering} />}
      {state === "done" && answer !== undefined ? (
        <AnswerFoot answer={answer} asks={asks} onRetry={() => onRetry(run.record.words)} />
      ) : // While the run works, "Thinking…" or its steps' lines with the run's timer say so.
      waitsForSteps || (working && steps.length > 0) ? null : (
        <p
          className={`text-xs ${state === "failed" ? dangerText : textSecondaryOnSurface}`}
          data-testid="going-run-state"
          role="status"
        >
          {STATE_TEXT[state]}
          {end !== undefined && end.reason !== "" && state !== "done" && state !== "waiting"
            ? `: ${end.reason}`
            : null}
        </p>
      )}
      {state === "stopped_bound" ? (
        <button
          className={secondaryButtonCompact}
          data-testid="going-run-continue"
          onClick={() => onContinue(run.id)}
          type="button"
        >
          Continue
        </button>
      ) : null}
      {runEnded(state) && changed ? <RunUndo run={run.id} /> : null}
      {run.unreadable === undefined ? null : (
        <p className={`text-xs ${dangerText}`} role="alert">
          {run.unreadable}
        </p>
      )}
    </li>
  );
}

/** The question of a run whose start is out, with its "Thinking…" line from the moment of Enter
 *  (design v2, rule 4). */
function StartingTurn({ starting }: { readonly starting: StartingRun }): ReactNode {
  return (
    <li className="space-y-2" data-testid="going-run-starting">
      <Words testId="going-run-words" words={starting.words} />
      <Thinking since={starting.at} />
    </li>
  );
}

/** The words the person sent while the run worked (design v2, state 3): a grey bubble at the
 *  foot of the conversation, "Sends when this run ends", and × to drop them. */
function QueuedTurn({ queued }: { readonly queued: QueuedWords }): ReactNode {
  return (
    <div className="mt-4 space-y-1" data-testid="going-queued">
      <Words testId="going-queued-words" words={queued.words} />
      <p className={`flex items-center gap-1 text-xs ${textMutedOnSurface}`}>
        Sends when this run ends
        <button
          aria-label="Drop these words"
          className="rounded px-1 hover:underline"
          data-testid="going-queued-drop"
          onClick={() => dropQueued(queued)}
          title="Drop these words"
          type="button"
        >
          ×
        </button>
      </p>
    </div>
  );
}

/**
 * The open conversation, as the bar over the page and the panel at the side both draw it (design
 * v2, states 2 to 4): it fills the space its surface gives it and scrolls inside it. Each run in
 * order, its question first in a grey bubble, "Thinking…" with a timer until its first step or
 * answer comes, its steps live as they arrive (`StepSection`), "Reconnecting…" while its stream is
 * dropped, its answer with each issue it names as a chip, its blocks, the plan card of the newest
 * run, and how each ended, with "Done" or "Waits for your answer" and Copy and Retry under an
 * answer; then the question of a run whose start is out, and the words that wait for the newest
 * run. `answering` is the newest run's plan as the surface that draws this answers it, so Enter in
 * that surface's box and the card's Approve are one answer; `onRetry` starts a new run of a run's
 * words, as that surface's box starts one.
 */
export function ConversationView({
  answering,
  onContinue,
  onRetry,
}: {
  readonly answering: PlanAnswering;
  readonly onContinue: (run: string) => void;
  readonly onRetry: (words: string) => void;
}): ReactNode {
  const going = useGoing();
  const starting = going.starting;
  // A run that starts a conversation of its own takes the place of the one open.
  const ids = starting?.follows === false ? [] : (going.conversation ?? []);
  const runs = ids.flatMap((id) => {
    const run = going.runs.get(id);
    return run === undefined ? [] : [run];
  });
  const queued =
    going.queued !== undefined && going.queued.run === newestRun(going)?.id
      ? going.queued
      : undefined;
  return (
    <div className="min-h-0 flex-1 overflow-y-auto px-3 py-3" data-testid="going-conversation-view">
      {runs.length === 0 && starting === undefined ? null : (
        <ol aria-label="Conversation" className="space-y-4" data-testid="going-conversation">
          {runs.map((run, index) => (
            <RunTurn
              answering={index === runs.length - 1 ? answering : undefined}
              asks={index === runs.length - 1 && asksPerson(going)}
              key={run.id}
              onContinue={onContinue}
              onRetry={onRetry}
              run={run}
            />
          ))}
          {starting === undefined ? null : <StartingTurn starting={starting} />}
        </ol>
      )}
      {queued === undefined ? null : <QueuedTurn queued={queued} />}
    </div>
  );
}
