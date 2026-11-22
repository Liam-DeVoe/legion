import {
  type ReactNode,
  type RefObject,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";

import type { GoingRun } from "../../api/types";
import { CopyButton } from "../../components/CopyButton";
import {
  borderDefault,
  dangerText,
  linkHoverText,
  linkText,
  primaryButton,
  secondaryButtonCompact,
  surfaceBg,
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
  newConversation,
  newestRun,
  type QueuedWords,
  type RunEntry,
  type StartingRun,
  shownState,
  useGoing,
} from "./going-store";
import { PlanCard } from "./PlanRows";
import { RunUndo } from "./RunUndo";
import { type EndWords, endWords, type RunState, runEnded } from "./run-states";
import { foldWords, RunTimer, StepSection } from "./step-section/StepSection";
import { type PlanAnswering, usePlan } from "./usePlan";

/** How a run that has not stopped or failed stands, in the person's words, for the line under its
 *  steps; a stop and a failure read as their end says (`endWords`). */
const STATE_TEXT: Readonly<Record<RunState, string>> = {
  done: "Done",
  failed: "Failed",
  idle: "",
  stopped_bound: "Stopped",
  stopped_person: "Stopped",
  waiting: "A plan waits for you",
  working: "Working…",
};

/** How near the foot of the conversation, in pixels, the person's scroll still follows it. */
const FOOT_SLACK = 24;

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
 * When something of `record`'s run happened, for the words that say when: the time this page read
 * it (`read`), or the time the record says the run finished, whichever is earlier. Nothing of a
 * run happens after it finished, so the record's time is the one for frames read again after the
 * run ended, as History opens them; undefined where neither is known.
 */
function earliest(read: number | undefined, record: GoingRun): number | undefined {
  const finished = record.finished_at === null ? undefined : Date.parse(record.finished_at);
  if (read === undefined || finished === undefined) return read ?? finished;
  return Math.min(read, finished);
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

/**
 * How a run that stopped or failed ended (design v2, state 9), in one sentence (`endWords`) and,
 * on the conversation's newest run (`actions`), its one action. A bound is a grey card, a pause
 * and not an error: "Stopped at Standard's time limit · read 14 items so far", then Continue (↵,
 * which Enter in the empty box also does) and, below Deep, "Continue in Deep". A Stop reads
 * "Stopped · kept what it did". A failure is its sentence and Try again, or "Start a new
 * conversation" where this one grew too long; a Dispatch with no answering model says so in the
 * one sentence of state 13, and offers nothing.
 */
function RunEnd({
  actions,
  level,
  onContinue,
  onRetry,
  words,
}: {
  readonly actions: boolean;
  readonly level: GoingRun["level"];
  readonly onContinue: (level?: GoingRun["level"]) => void;
  readonly onRetry: () => void;
  readonly words: EndWords;
}): ReactNode {
  if (words.noModel) {
    return (
      <p
        className={`text-sm ${textSecondaryOnSurface}`}
        data-testid="going-cannot-answer"
        role="status"
      >
        {words.text}
      </p>
    );
  }
  if (words.action === "continue") {
    return (
      <div
        className={`space-y-2 rounded-lg border p-3 ${borderDefault} ${surfaceMutedBg}`}
        data-testid="going-run-bound"
      >
        <p
          className={`text-sm ${textSecondaryOnSurface}`}
          data-testid="going-run-state"
          role="status"
        >
          {words.text}
        </p>
        {actions ? (
          <div className="flex flex-wrap items-center gap-2">
            <button
              className={primaryButton}
              data-testid="going-run-continue"
              onClick={() => onContinue()}
              type="button"
            >
              Continue
              <span aria-hidden="true" className="ml-2 font-normal">
                ↵
              </span>
            </button>
            {level === "deep" ? null : (
              <button
                className={secondaryButtonCompact}
                data-testid="going-run-continue-deep"
                onClick={() => onContinue("deep")}
                type="button"
              >
                Continue in Deep
              </button>
            )}
          </div>
        ) : null}
      </div>
    );
  }
  const action = words.action;
  return (
    <p
      className={`text-xs ${action === undefined ? textSecondaryOnSurface : dangerText}`}
      data-testid="going-run-state"
      role="status"
    >
      {words.text}
      {actions && action !== undefined ? (
        <>
          {" "}
          <button
            className={`underline ${linkText} ${linkHoverText}`}
            data-testid="going-run-action"
            onClick={action === "new" ? newConversation : onRetry}
            type="button"
          >
            {action === "new" ? "Start a new conversation" : "Try again"}
          </button>
        </>
      ) : null}
    </p>
  );
}

/** A block the conversation shows whole over its surface (the bar, or the panel): its run, and
 *  its place among the run's blocks. */
interface WholeBlock {
  readonly run: string;
  readonly block: number;
}

/** One run of the conversation: the person's words, its steps, the answer with its items as
 *  chips and blocks, its plan - as the card of the newest run (the one `answering` is given for),
 *  folded to one line in an older one - how it ended, with Continue on a bound and Try again on a
 *  failure, and Undo of a run with no plan that changed something. `asks` is whether its answer
 *  asks the person a question and waits for their answer; `onShowAll` opens one of its tables
 *  whole over the bar or the panel. */
function RunTurn({
  answering,
  asks,
  onContinue,
  onRetry,
  onShowAll,
  run,
}: {
  readonly answering: PlanAnswering | undefined;
  readonly asks: boolean;
  readonly onContinue: (run: string, level?: GoingRun["level"]) => void;
  readonly onRetry: (words: string) => void;
  readonly onShowAll: (whole: WholeBlock) => void;
  readonly run: RunEntry;
}): ReactNode {
  const newest = answering !== undefined;
  const own = usePlan(newest ? undefined : run.id, newest ? 0 : run.frames.planReads);
  const plan = answering ?? own;
  const state = shownState(run);
  const answer = run.frames.replies.at(-1);
  const waitsForSteps = thinking(run);
  const working = state === "working";
  // A call with no words draws no line, and nor does a change of an approved plan, which its
  // row's tick on the plan's card shows.
  const steps = run.frames.steps.filter((step) => step.label !== "" && step.kind !== "write");
  const ended = endWords(
    state,
    run.frames.end?.reason ?? run.record.stop_reason ?? "",
    run.record.level,
    foldWords(steps)
  );
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
        <AnswerBlock
          asOf={earliest(run.blocksAt[index], run.record)}
          block={block}
          // biome-ignore lint/suspicious/noArrayIndexKey: a block is its place in the run, and blocks are only ever added
          key={index}
          onShowAll={() => onShowAll({ block: index, run: run.id })}
        />
      ))}
      <PlanCard answering={plan} appliedAt={earliest(run.endedAt, run.record)} folded={!newest} />
      {state === "done" && answer !== undefined ? (
        <AnswerFoot answer={answer} asks={asks} onRetry={() => onRetry(run.record.words)} />
      ) : ended !== undefined ? (
        <RunEnd
          actions={newest}
          level={run.record.level}
          onContinue={(level) => onContinue(run.id, level)}
          onRetry={() => onRetry(run.record.words)}
          words={ended}
        />
      ) : // While the run works, "Thinking…" or its steps' lines with the run's timer say so.
      waitsForSteps || (working && steps.length > 0) ? null : (
        <p
          className={`text-xs ${textSecondaryOnSurface}`}
          data-testid="going-run-state"
          role="status"
        >
          {STATE_TEXT[state]}
        </p>
      )}
      {run.frames.plan === undefined && runEnded(state) && (run.record.issues ?? []).length > 0 ? (
        <RunUndo run={run.id} />
      ) : null}
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
 * The scroll of the conversation (design v2, state 8): it follows new output - `output` counts
 * what came - while the person is at its foot. Once they scroll up it stays where they are, and
 * `behind` turns true at the next output, for the "↓ New reply" pill; `follow` takes them back to
 * the foot and follows again.
 */
function useFollow(output: number): {
  readonly behind: boolean;
  readonly follow: () => void;
  readonly onScroll: () => void;
  readonly scroller: RefObject<HTMLDivElement | null>;
} {
  const scroller = useRef<HTMLDivElement>(null);
  const following = useRef(true);
  const [behind, setBehind] = useState(false);
  // biome-ignore lint/correctness/useExhaustiveDependencies: each new output is the reason to follow it
  useLayoutEffect(() => {
    const element = scroller.current;
    if (element === null) return;
    if (following.current) {
      element.scrollTop = element.scrollHeight;
    } else {
      setBehind(true);
    }
  }, [output]);
  return {
    behind,
    follow: () => {
      following.current = true;
      setBehind(false);
      const element = scroller.current;
      if (element !== null) element.scrollTop = element.scrollHeight;
    },
    onScroll: () => {
      const element = scroller.current;
      if (element === null) return;
      const atFoot = element.scrollHeight - element.scrollTop - element.clientHeight <= FOOT_SLACK;
      following.current = atFoot;
      if (atFoot) setBehind(false);
    },
    scroller,
  };
}

/**
 * The open conversation, as the bar over the page and the panel at the side both draw it (design
 * v2, states 2 to 9): one column, newest at the bottom, that fills the space its surface gives it
 * and scrolls inside it, following new output until the person scrolls up, when "↓ New reply"
 * brings them back. Each run in order, its question first in a grey bubble, "Thinking…" with a
 * timer until its first step or answer comes, its steps live as they arrive (`StepSection`),
 * "Reconnecting…" while its stream is dropped, its answer with each issue it names as a chip, its
 * blocks with their captions, its plan's card - an old turn's folded to one line - and how each
 * ended, with "Done" or "Waits for your answer" and Copy and Retry under an answer, Continue at a
 * bound and Try again after a failure; then the question of a run whose start is out, and the
 * words that wait for the newest run. A table's "Show all" opens it whole over the bar or the
 * panel that draws the conversation, and its Back or Esc closes it.
 * `answering` is the newest run's plan as the surface that draws this answers it, so Enter in that
 * surface's box and the card's Approve are one answer; `onRetry` starts a new run of a run's
 * words, as that surface's box starts one; `onContinue` continues a run a bound stopped, at
 * `level` where one is given.
 */
export function ConversationView({
  answering,
  onContinue,
  onRetry,
}: {
  readonly answering: PlanAnswering;
  readonly onContinue: (run: string, level?: GoingRun["level"]) => void;
  readonly onRetry: (words: string) => void;
}): ReactNode {
  const going = useGoing();
  const starting = going.starting;
  const [whole, setWhole] = useState<WholeBlock>();
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
  const scroll = useFollow(
    runs.reduce((sum, run) => sum + run.read + 1, 0) +
      (starting === undefined ? 0 : 1) +
      (queued === undefined ? 0 : 1)
  );
  const wholeRun = whole === undefined ? undefined : going.runs.get(whole.run);
  const wholeBlock = whole === undefined ? undefined : wholeRun?.frames.blocks[whole.block];
  // The table opens over the button that opened it, so the focus moves onto its Back.
  const back = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    if (whole !== undefined) back.current?.focus();
  }, [whole]);
  // The surface that holds the conversation - the bar's dialog, or the panel at the side - which
  // a table opened whole covers; the conversation's own area where it stands in neither.
  const view = useRef<HTMLDivElement>(null);
  const [surface, setSurface] = useState<HTMLElement | null>(null);
  useLayoutEffect(() => {
    setSurface(view.current?.parentElement?.closest<HTMLElement>('[role="dialog"], aside') ?? null);
  }, []);
  const wholeTable =
    whole === undefined || wholeRun === undefined || wholeBlock === undefined ? null : (
      <section
        aria-label="The whole table"
        className={`absolute inset-0 z-20 flex flex-col ${surfaceBg}`}
        data-testid="going-block-whole"
        onKeyDown={(event) => {
          if (event.key !== "Escape") return;
          // Esc closes the table first; the next one is the bar's.
          event.preventDefault();
          event.stopPropagation();
          setWhole(undefined);
        }}
      >
        <div className={`flex shrink-0 items-center border-b px-3 py-1.5 ${borderDefault}`}>
          <button
            className={secondaryButtonCompact}
            data-testid="going-block-whole-close"
            onClick={() => setWhole(undefined)}
            ref={back}
            type="button"
          >
            ‹ Back to the conversation
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-auto px-3 pb-3">
          <AnswerBlock
            asOf={earliest(wholeRun.blocksAt[whole.block], wholeRun.record)}
            block={wholeBlock}
            whole
          />
        </div>
      </section>
    );
  return (
    <div className="relative flex min-h-0 flex-1 flex-col" ref={view}>
      <div
        className="min-h-0 flex-1 overflow-y-auto px-3 py-3"
        data-testid="going-conversation-view"
        onScroll={scroll.onScroll}
        ref={scroll.scroller}
      >
        {runs.length === 0 && starting === undefined ? null : (
          <ol aria-label="Conversation" className="space-y-4" data-testid="going-conversation">
            {runs.map((run, index) => (
              <RunTurn
                answering={index === runs.length - 1 ? answering : undefined}
                asks={index === runs.length - 1 && asksPerson(going)}
                key={run.id}
                onContinue={onContinue}
                onRetry={onRetry}
                onShowAll={setWhole}
                run={run}
              />
            ))}
            {starting === undefined ? null : <StartingTurn starting={starting} />}
          </ol>
        )}
        {queued === undefined ? null : <QueuedTurn queued={queued} />}
      </div>
      {scroll.behind ? (
        <button
          className={`absolute bottom-3 left-1/2 -translate-x-1/2 rounded-full border px-3 py-1 text-xs font-medium shadow ${borderDefault} ${surfaceBg} ${textPrimaryOnSurface}`}
          data-testid="going-new-reply"
          onClick={scroll.follow}
          type="button"
        >
          ↓ New reply
        </button>
      ) : null}
      {wholeTable === null || surface === null ? wholeTable : createPortal(wholeTable, surface)}
    </div>
  );
}
