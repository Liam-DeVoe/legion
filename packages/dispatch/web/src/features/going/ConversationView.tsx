import { type ReactNode, useLayoutEffect, useRef } from "react";

import {
  dangerText,
  secondaryButtonCompact,
  surfaceMutedBg,
  textMutedOnSurface,
  textPrimaryOnSurface,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { MarkdownBody } from "../refs/MarkdownBody";
import { AnswerBlock } from "../search/blocks/AnswerBlock";
import {
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

/** The seconds since `since`, as the "Thinking…" line counts them: "0 s", "1 s", … */
export function elapsedWords(since: number, now: number): string {
  return `${Math.max(0, Math.floor((now - since) / 1000))} s`;
}

/**
 * The live line under a question until its run's first step or answer comes (design v2, rule 4):
 * "Thinking…" and the seconds since the person asked, counted each second. The timer writes the
 * seconds into the page itself, so a tick draws nothing else again; the seconds are hidden from a
 * screen reader, which hears "Thinking…" once.
 */
function Thinking({ since }: { readonly since: number }): ReactNode {
  const seconds = useRef<HTMLSpanElement>(null);
  useLayoutEffect(() => {
    const write = () => {
      if (seconds.current !== null) {
        seconds.current.textContent = elapsedWords(since, Date.now());
      }
    };
    write();
    const timer = window.setInterval(write, 1000);
    return () => window.clearInterval(timer);
  }, [since]);
  return (
    <p className={`text-xs ${textMutedOnSurface}`} data-testid="going-thinking">
      <span role="status">Thinking…</span> <span aria-hidden="true" ref={seconds} />
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

/** The steps of a run: one line for each tool call, its words in the present tense while it runs
 *  and in the past tense, or the sentence of a call that failed, once it ended. A call with no
 *  words draws no line, and nor does a change of an approved plan, which its row's tick on the
 *  plan's card shows. While calls run, the prose the model wrote beside them is a line of its own
 *  above them. */
function Steps({ run }: { readonly run: RunEntry }): ReactNode {
  const steps = run.frames.steps.filter((step) => step.label !== "" && step.kind !== "write");
  const working = steps.some((step) => step.running) ? run.frames.working : "";
  if (steps.length === 0 && working === "") {
    return null;
  }
  return (
    <div className="space-y-0.5">
      {working === "" ? null : (
        <p className={`truncate text-xs ${textMutedOnSurface}`} data-testid="going-step-words">
          {working}
        </p>
      )}
      <ol aria-label="Steps" className="space-y-0.5" data-testid="going-steps">
        {steps.map((step) => (
          <li
            className={`truncate text-xs ${step.refused ? dangerText : textMutedOnSurface}`}
            data-kind={step.kind}
            data-running={String(step.running)}
            data-testid="going-step"
            data-tool={step.tool}
            key={step.id}
          >
            {step.running ? `${step.label}…` : step.label}
          </li>
        ))}
      </ol>
    </div>
  );
}

/** One run of the conversation: the person's words, its steps, the answer and blocks, its plan
 *  where it is the newest (the one `answering` is given for), how it ended with Continue on a
 *  bound, and Undo once it changed something. */
function RunTurn({
  answering,
  onContinue,
  run,
}: {
  readonly answering: PlanAnswering | undefined;
  readonly onContinue: (run: string) => void;
  readonly run: RunEntry;
}): ReactNode {
  const newest = answering !== undefined;
  const own = usePlan(newest ? undefined : run.id, newest ? 0 : run.frames.planReads);
  const plan = answering ?? own;
  const state = shownState(run);
  const answer = run.frames.replies.at(-1);
  const end = run.frames.end;
  const waitsForSteps = thinking(run);
  // Undo is offered on a run that changed something: a request of it landed, or a row of its
  // plan ran.
  const changed =
    (run.record.issues ?? []).length > 0 ||
    (plan.plan?.rows ?? []).some((row) => row.state === "done");
  return (
    <li className="space-y-2" data-run={run.id} data-state={state} data-testid="going-run">
      <Words testId="going-run-words" words={run.record.words} />
      {waitsForSteps ? <Thinking since={run.since} /> : null}
      <Steps run={run} />
      {answer === undefined ? null : (
        <div className={`text-sm ${textPrimaryOnSurface}`} data-testid="going-run-answer">
          <MarkdownBody markdown={answer} referenceText="" softBreaks="line" />
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
      {waitsForSteps ? null : (
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
 * v2, state 2): it fills the space its surface gives it and scrolls inside it. Each run in order,
 * its question first in a grey bubble, "Thinking…" with a timer until its first step or answer
 * comes, its steps live as they arrive, its answer with its links, its blocks, the plan card of
 * the newest run, and how each ended; then the question of a run whose start is out, and the words
 * that wait for the newest run. `answering` is the newest run's plan as the surface that draws
 * this answers it, so Enter in that surface's box and the card's Approve are one answer.
 */
export function ConversationView({
  answering,
  onContinue,
}: {
  readonly answering: PlanAnswering;
  readonly onContinue: (run: string) => void;
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
              key={run.id}
              onContinue={onContinue}
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
