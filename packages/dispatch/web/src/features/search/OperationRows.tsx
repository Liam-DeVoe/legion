import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useState } from "react";

import { ApiError, apiErrorMessage, goingPlanApproveRequest } from "../../api/client";
import { goingPlanQuery } from "../../api/queries";
import type {
  GoingDecisionEvent,
  GoingOffer,
  GoingPlanAnswer,
  GoingProposalEvent,
} from "../../api/types";
import {
  borderDefault,
  dangerText,
  secondaryButtonCompact,
  textMutedOnSurface,
  textPrimaryOnSurface,
} from "../../theme/classes";
import { useKeyedWrite } from "../writes/useKeyedWrite";
import { actionName, type Turn, targetOf } from "./going-turns";
import { barNoteClassName, type OfferRow, useWordsState } from "./offer";
import { answerSettled, type GoingAnswer } from "./useGoingAnswer";

/** The group the bar's operation rows sit in, above the hits, and the operation row's own id. */
export const OPERATION_GROUP_ID = "search-group-going";
export const OPERATION_OPTION_ID = "search-option-going-run";

/** The id of the offer row of `action`, one of the two best below the action threshold. */
export function offerOptionId(action: string): string {
  return `search-option-going-offer-${action}`;
}

type Decision = GoingDecisionEvent["data"];
type Proposal = GoingProposalEvent["data"];

/** The two actions the bar offers through offers of its own, which always ask (`NewIssueOffer`,
 *  `SendToAgentOffer`), rather than through a row of the operation group. */
const OWN_OFFERS: Readonly<Record<string, true>> = {
  dispatch_issue: true,
  dispatch_send_to_agent: true,
};

/** How a press came: a submit, which names the operation the decision chose at or above the
 *  action threshold (`auto`), or the person's pick of a row (`person`). */
type Approval = "auto" | "person";

/** What the person asked to run: the action the decision chose, `undefined` while the decision
 *  that names it has not arrived, and how the press came. */
interface Wanted {
  readonly action: string | undefined;
  readonly approved: Approval;
  /** The run current when the person pressed, whose plan alone the press shows (`isPressOf`);
   *  `undefined` for a press made before that run's id was known, until the preparing effect
   *  binds it to that run as the id arrives. */
  readonly pressedOn: string | undefined;
}

/**
 * Where the bar's operation stands for the words in the box, once the person submitted it or
 * picked an offer: waiting for the decision or the proposal, showing the run's plan, which waits
 * for the person's approval, an action the proposal offered nothing to run for, or one whose every
 * invocation names an item neither the page nor the words named, which runs only when the person
 * picks it (`picks`). Nothing submitted is no state at all, and so is a submit that had nothing to
 * run (`idle`). Every stage of a press is of the run the person pressed on (`pressedOn`): it is
 * idle once another run is current, so nothing of it is shown beside, or approved for, another
 * run.
 */
type Stage =
  | ({ readonly kind: "preparing"; readonly words: string } & Wanted)
  | { readonly kind: "plan"; readonly words: string; readonly pressedOn: string | undefined }
  | {
      readonly kind: "unoffered";
      readonly words: string;
      readonly action: string;
      readonly pressedOn: string | undefined;
    }
  | {
      readonly kind: "picks";
      readonly words: string;
      readonly action: string;
      readonly pressedOn: string | undefined;
    }
  | { readonly kind: "idle"; readonly words: string };

/** Whether a press made on the run `pressedOn` is a press of the run `runId`: a press shows only
 *  the plan of the run that was current when the person pressed, never another run's - a chip
 *  change asks the same words again, and another run of them is current. */
function isPressOf(pressedOn: string | undefined, runId: string | undefined): boolean {
  return pressedOn === runId;
}

/** The offer of `action` the proposal offered something to run for. */
function offerOf(proposal: Proposal | undefined, action: string): GoingOffer | undefined {
  return proposal?.offers.find((offer) => offer.action === action && offer.offered);
}

/** Why the proposal withheld every invocation of `action` it would otherwise have offered - one
 *  the bar can never send as written, such as a comment with a picture file, or an upload under the
 *  name of a document its owner holds, which would replace that document's text - in Dispatch's
 *  own words, each saying what the person can do instead; undefined where it withheld none. */
function withheldOf(proposal: Proposal | undefined, action: string): string | undefined {
  const withheld = proposal?.offers.find((offer) => offer.action === action)?.withheld ?? [];
  return withheld.length === 0 ? undefined : `Nothing to run: ${withheld.join(" ")}`;
}

/** The invocations of `offer` a press names, by their index in it: every one where the person
 *  picked the action itself, and otherwise - a submit - only those the proposal marked automatic.
 *  An invocation naming an item neither the page nor the person's words named came from text
 *  someone stored, and is named only by the row the person picks. */
function runIndexes(offer: GoingOffer | undefined, approved: Approval): number[] {
  return (offer?.invocations ?? []).flatMap((invocation, index) =>
    approved === "person" || invocation.automatic ? [index] : []
  );
}

/** `offer` with only the invocations at `indexes`, for a row that names their items alone. */
function offerWith(offer: GoingOffer, indexes: readonly number[]): GoingOffer {
  return { ...offer, invocations: offer.invocations.filter((_, index) => indexes.includes(index)) };
}

/** The items an offer acts on, in the words of one row: `CORE-1, CORE-2 and CORE-3`. */
function targetsOf(offer: GoingOffer | undefined): string {
  const targets = [
    ...new Set(
      (offer?.invocations ?? []).map((invocation) =>
        targetOf(invocation.arguments, invocation.target_argument)
      )
    ),
  ].filter((target) => target !== "");
  if (targets.length <= 1) return targets[0] ?? "";
  return `${targets.slice(0, -1).join(", ")} and ${targets.at(-1)}`;
}

function rowLabel(action: string, offer: GoingOffer | undefined): string {
  const targets = targetsOf(offer);
  return `Run ${actionName(action)}${targets === "" ? "" : ` on ${targets}`}`;
}

/**
 * The stage a submit (`wanted.action` undefined: the operation the decision chose at or above the
 * action threshold) or a pick comes to, from what the run has streamed so far. A run that has not
 * decided, or a decision whose proposal has not arrived, is `preparing`; an answer that ended
 * without either has nothing to run. A submit names only the invocations the proposal marked
 * automatic (`runIndexes`), and one with none is `picks`: the person picks the rest. Otherwise the
 * press shows the run's plan, which makes no change until the person approves it.
 */
function nextStage(words: string, wanted: Wanted, answer: GoingAnswer | undefined): Stage {
  if (answer === undefined || answer.runId === undefined || answer.decision === undefined) {
    return answer !== undefined && answerSettled(answer)
      ? { kind: "idle", words }
      : { ...wanted, kind: "preparing", words };
  }
  // The decision's choice at or above the action threshold, which Dispatch's `act` says, and
  // never one of the writes that always ask.
  const action =
    wanted.action ?? (answer.decision.act ? answer.decision.actions[0]?.action : undefined);
  if (action === undefined) return { kind: "idle", words };
  if (answer.proposal === undefined) {
    return answerSettled(answer)
      ? { action, kind: "unoffered", pressedOn: wanted.pressedOn, words }
      : { ...wanted, action, kind: "preparing", words };
  }
  const offer = offerOf(answer.proposal, action);
  if (offer === undefined) {
    return { action, kind: "unoffered", pressedOn: wanted.pressedOn, words };
  }
  if (runIndexes(offer, wanted.approved).length === 0) {
    return { action, kind: "picks", pressedOn: wanted.pressedOn, words };
  }
  return { kind: "plan", pressedOn: wanted.pressedOn, words };
}

/** One row of the bar's operation group, as the list draws it: its label, its trailing control,
 *  and the row the arrows, Enter and a click press. */
export interface OperationOption {
  readonly hint: ReactNode;
  readonly label: string;
  readonly row: OfferRow;
}

/** The bar's operations for the words in the box. */
export interface GoingOperations {
  /** The group of rows above the hits: the operation with its Run button where the decision
   *  acts, or the two best actions to pick from below the threshold; none while there is none. */
  readonly section:
    | { readonly id: string; readonly label: string; readonly options: readonly OperationOption[] }
    | undefined;
  /** The bar's submit (`$mod+Enter`, the Run button, Enter on the operation row). */
  readonly submit: () => void;
  /** What the operation is doing: preparing, the note that nothing runs, or the run's plan with
   *  its approval. */
  readonly panel: ReactNode;
  /** Whether the new-issue offer is offered, where the decision chose a new issue: once the
   *  proposal offers it. Undefined leaves the offer as it is. */
  readonly newIssue: { readonly offered: boolean } | undefined;
}

/**
 * The bar's operations, from the `decision` and `proposal` frames of the answer to the words in
 * the box. Where the decision acts - its choice at or above the action threshold and none of the
 * writes that always ask - the operation is one row above the hits with a Run button, and it runs
 * on submit: `$mod+Enter` anywhere in the bar, the Run button, or Enter on the row, which the
 * highlight reaches only when the person moves it there. A submit names only the invocations the
 * proposal marked automatic - each item they name is the page's or named in the words - and the
 * rest, which only text someone stored asked for, are a row the person picks. Below the threshold
 * the two best actions are rows of their own, and neither runs until the person picks it. A new
 * issue and a send to the agent on the issue go through the bar's own two offers, which always
 * ask. An invocation the proposal withheld beside others of its action it offered - one the bar
 * can never send as written, or an upload over a document its owner holds - is in no row, so the
 * panel says why, in Dispatch's words, for as long as the proposal stands.
 *
 * A press makes no change itself: it shows the plan the run proposed
 * (`GET /api/v1/going/runs/{id}/plan`), and the run makes its changes once the person approves
 * that plan (`POST /api/v1/going/runs/{id}/plan/approve`). A submit before the proposal arrives
 * says it is preparing and shows the plan when the proposal lands; one with no run yet asks at
 * once.
 */
export function useGoingOperations({
  answer,
  askNow,
  words,
}: {
  /** The answer to the words in the box, or the replay of the last one. */
  readonly answer: GoingAnswer | undefined;
  /** Asks for the words at once, answering whether an answer is coming (`useGoingAnswer`). */
  readonly askNow: () => boolean;
  /** The words in the box, trimmed. */
  readonly words: string;
}): GoingOperations {
  // A replay draws an answer the person already had, whose plan was theirs to approve then.
  const current = answer !== undefined && !answer.replayed ? answer : undefined;
  const runId = current?.runId;
  const decision: Decision | undefined = runId === undefined ? undefined : current?.decision;
  const proposal = decision === undefined ? undefined : current?.proposal;
  const [held, setStage] = useWordsState<Stage>(words);
  // Every stage of a press - preparing, the plan, and the note that the answer offered nothing to
  // run or that its items are the person's to pick - made on a run that is no longer current (a
  // chip change asked the same words again) is idle: it closes, so neither the preparing effect
  // nor an Approve reaches the run now current, and no note of the old run stays beside the new
  // run's rows (`isPressOf`). A press made before its run's id was known is of no run yet: the
  // preparing effect below binds it or closes it.
  const stage: Stage | undefined =
    held !== undefined &&
    held.kind !== "idle" &&
    held.pressedOn !== undefined &&
    !isPressOf(held.pressedOn, runId)
      ? { kind: "idle", words }
      : held;
  const begin = (wanted: Wanted) => setStage(nextStage(words, wanted, current));

  // A submit made before the run could say what to run waits for it: the decision and its
  // proposal, each as it arrives. One made before its run's id was known (`askNow` set a fresh
  // answer in the same event) is bound to that run as the id arrives, and goes on from there;
  // where the request it waited for was dropped first - a chip change, or the bar turning off -
  // it closes instead, so it can never bind to the run the next ask starts. That close reads the
  // drop as `current` going undefined: `useGoingAnswer` lets the answer go on every change of the
  // words, the chip or `enabled`, before it asks again after the pause, so no render shows the
  // next ask's answer to a press still waiting for the dropped one.
  const preparing = stage?.kind === "preparing" ? stage : undefined;
  // biome-ignore lint/correctness/useExhaustiveDependencies: `setStage` is the words state's own setter, and the stage changes only with what it is computed from
  useEffect(() => {
    if (preparing === undefined) return;
    if (preparing.pressedOn === undefined) {
      if (current === undefined) {
        setStage({ kind: "idle", words });
        return;
      }
      if (runId !== undefined) {
        setStage({ ...preparing, pressedOn: runId });
        return;
      }
    }
    const next = nextStage(words, preparing, current);
    if (next.kind !== "preparing") setStage(next);
  }, [preparing, current, runId, words]);

  const submit = () => {
    if (stage?.kind === "preparing") return;
    if (current === undefined && !askNow()) return;
    begin({ action: undefined, approved: "auto", pressedOn: runId });
  };

  const options: OperationOption[] = [];
  let sectionLabel = "";
  if (decision !== undefined) {
    const acted = decision.act ? decision.actions[0]?.action : undefined;
    if (acted !== undefined) {
      sectionLabel = "Operation";
      const actedOffer = offerOf(proposal, acted);
      const automatic = runIndexes(actedOffer, "auto");
      if (proposal === undefined || automatic.length > 0) {
        const waiting = stage?.kind === "preparing";
        const label = rowLabel(acted, actedOffer && offerWith(actedOffer, automatic));
        options.push({
          hint: waiting ? (
            <span className={`shrink-0 text-xs ${textMutedOnSurface}`}>Preparing…</span>
          ) : (
            <button
              className={secondaryButtonCompact}
              data-testid="going-run"
              onClick={(event) => {
                // The row's own press is the same submit; this one must not reach it as well.
                event.stopPropagation();
                submit();
              }}
              type="button"
            >
              Run
            </button>
          ),
          label,
          row: { id: OPERATION_OPTION_ID, kind: "offer", label, select: submit },
        });
      }
      // The invocations no submit names - each names an item neither the page nor the words
      // named - are a row of their own, which the person picks.
      const offered = (actedOffer?.invocations ?? []).flatMap((invocation, index) =>
        invocation.automatic ? [] : [index]
      );
      if (actedOffer !== undefined && offered.length > 0) {
        const label = rowLabel(acted, offerWith(actedOffer, offered));
        options.push({
          hint: <span className={`shrink-0 text-xs ${textMutedOnSurface}`}>Pick to run</span>,
          label,
          row: {
            id: offerOptionId(acted),
            kind: "offer",
            label,
            select: () => begin({ action: acted, approved: "person", pressedOn: runId }),
          },
        });
      }
    } else {
      sectionLabel = "Suggested actions: pick one to run it";
      for (const { action } of decision.actions) {
        if (action === "none" || OWN_OFFERS[action] === true) continue;
        if (proposal !== undefined && offerOf(proposal, action) === undefined) continue;
        const label = rowLabel(action, offerOf(proposal, action));
        options.push({
          hint: <span className={`shrink-0 text-xs ${textMutedOnSurface}`}>Pick to run</span>,
          label,
          row: {
            id: offerOptionId(action),
            kind: "offer",
            label,
            select: () => begin({ action, approved: "person", pressedOn: runId }),
          },
        });
      }
    }
  }

  const newIssue: GoingOperations["newIssue"] =
    runId === undefined ||
    current === undefined ||
    decision?.actions.some((decided) => decided.action === "dispatch_issue") !== true
      ? undefined
      : { offered: offerOf(proposal, "dispatch_issue") !== undefined };

  const stageShown = stage !== undefined && stage.kind !== "idle";
  // Why the proposal withheld some invocations of an action it still offered others of, in
  // Dispatch's own words: the rows show only what was offered, so nothing else says why an
  // invocation the answer wrote is missing from them.
  const withheldBeside = (proposal?.offers ?? []).flatMap((offer) =>
    offer.offered ? (offer.withheld ?? []) : []
  );
  const panel =
    !stageShown && withheldBeside.length === 0 ? null : (
      <section
        aria-label="Operation"
        className={`max-h-[30vh] shrink-0 overflow-y-auto border-t px-3 py-2 text-sm ${borderDefault}`}
        data-run={runId}
        data-testid="going-operation"
      >
        {stage?.kind === "preparing" ? (
          <p className={barNoteClassName} data-testid="going-operation-preparing" role="status">
            Preparing the operation: its plan shows as soon as the answer says what to run.
          </p>
        ) : null}
        {stage?.kind === "unoffered" ? (
          <p className={barNoteClassName} data-testid="going-operation-unoffered" role="status">
            {withheldOf(proposal, stage.action) ??
              `Nothing to run: the answer gave ${actionName(stage.action)} nothing it can run with.`}
          </p>
        ) : null}
        {stage?.kind === "picks" ? (
          <p className={barNoteClassName} data-testid="going-operation-picks" role="status">
            Nothing ran at once: the answer has {actionName(stage.action)} act on items neither this
            page nor your words named. Pick it to run it.
          </p>
        ) : null}
        {stage?.kind === "plan" && runId !== undefined ? <PlanStep runId={runId} /> : null}
        {withheldBeside.length === 0 ? null : (
          <p className={barNoteClassName} data-testid="going-operation-withheld" role="status">
            {withheldBeside.join(" ")}
          </p>
        )}
      </section>
    );

  return {
    newIssue,
    panel,
    section:
      options.length === 0 ? undefined : { id: OPERATION_GROUP_ID, label: sectionLabel, options },
    submit,
  };
}

/** The code Dispatch answers a plan read with for a run that proposed no plan. */
const PLAN_NOT_FOUND = "GOING_PLAN_NOT_FOUND";

/**
 * The run's latest plan (`GET /api/v1/going/runs/{id}/plan`): each of its rows and where it
 * stands, and, while the plan waits for the person, an Approve that approves every row as the plan
 * shows it (`POST /api/v1/going/runs/{id}/plan/approve`), after which the run makes its changes.
 * Nothing of the plan is made before that. A run that proposed no plan says so, and offers nothing
 * to approve.
 */
function PlanStep({ runId }: { readonly runId: string }): ReactNode {
  const queryClient = useQueryClient();
  const query = goingPlanQuery(runId);
  const plan = useQuery({ ...query, retry: false });
  const [refusal, setRefusal] = useState<string>();
  const approve = useKeyedWrite<GoingPlanAnswer, number>({
    inline: true,
    label: () => "Approve the command bar's plan",
    onError: (error) => setRefusal(apiErrorMessage(error, "The approval did not reach Dispatch.")),
    onSuccess: (answered) => {
      if (answered !== null) {
        queryClient.setQueryData(query.queryKey, answered);
        return;
      }
      void queryClient.invalidateQueries({ queryKey: query.queryKey });
    },
    request: (number) => goingPlanApproveRequest(runId, { plan: number }),
    target: () => `going-plan:${runId}`,
  });
  if (plan.isPending) {
    return (
      <p className={barNoteClassName} data-testid="going-plan-reading" role="status">
        Reading the run's plan…
      </p>
    );
  }
  if (plan.isError) {
    return (
      <p className={barNoteClassName} data-testid="going-plan-none" role="status">
        {plan.error instanceof ApiError && plan.error.code === PLAN_NOT_FOUND
          ? "This run proposed no plan, so nothing runs."
          : apiErrorMessage(plan.error, "The run's plan could not be read.")}
      </p>
    );
  }
  const answer = plan.data;
  return (
    <div data-plan={answer.plan} data-state={answer.state} data-testid="going-plan">
      {answer.summary === "" ? null : <p className={barNoteClassName}>{answer.summary}</p>}
      <ul className="space-y-1">
        {(answer.rows ?? []).map((row) => (
          <li
            className={`text-xs ${textPrimaryOnSurface}`}
            data-state={row.state}
            data-testid="going-plan-row"
            key={row.row}
          >
            {/* The change, the item it acts on, and where the row stands. */}
            {`${actionName(row.tool)}${row.item === "" ? "" : ` ${row.item}`}: ${row.state}`}
          </li>
        ))}
      </ul>
      {answer.state === "waiting" ? (
        <div className="mt-2 flex items-center gap-2">
          <button
            className={secondaryButtonCompact}
            data-testid="going-plan-approve"
            disabled={approve.isPending}
            onClick={() => {
              setRefusal(undefined);
              approve.mutate(answer.plan);
            }}
            type="button"
          >
            Approve
          </button>
          <span className={`text-xs ${textMutedOnSurface}`}>
            Nothing changes until you approve.
          </span>
        </div>
      ) : null}
      {refusal === undefined ? null : (
        <p className={`text-xs ${dangerText}`} data-testid="going-plan-refused" role="alert">
          {refusal}
        </p>
      )}
    </div>
  );
}

/** What a turn's undo says, in the shell's notice of the undo key. */
export function UndoSaid({ undo }: { readonly undo: Turn["undo"] }): ReactNode {
  if (undo === undefined) return null;
  const stopped =
    undo.stopped === 0
      ? ""
      : ` ${undo.stopped === 1 ? "One write" : `${undo.stopped} writes`} still waiting ${
          undo.stopped === 1 ? "was" : "were"
        } stopped before ${undo.stopped === 1 ? "it was" : "they were"} sent.`;
  if (undo.state === "undoing") {
    return (
      <p className={`text-xs ${textMutedOnSurface}`} data-testid="going-undo-said" role="status">
        Undoing…{stopped}
      </p>
    );
  }
  if (undo.state === "refused") {
    return (
      <p className={`text-xs ${dangerText}`} data-testid="going-undo-said" role="alert">
        Could not undo: {undo.message}
        {stopped}
      </p>
    );
  }
  return (
    <p className={`text-xs ${textMutedOnSurface}`} data-testid="going-undo-said" role="status">
      {undo.message ?? "Undone."}
      {stopped}
    </p>
  );
}
