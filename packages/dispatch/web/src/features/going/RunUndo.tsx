import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";

import { apiErrorMessage, goingRunUndoRequest } from "../../api/client";
import { goingConversationsQuery, goingPlanQuery } from "../../api/queries";
import type { GoingUndoAnswer, GoingUndoLine } from "../../api/types";
import {
  dangerText,
  secondaryButtonCompact,
  textMutedOnSurface,
  textPrimaryOnSurface,
} from "../../theme/classes";
import { AgentName } from "../refs/AgentName";
import { useKeyedWrite } from "../writes/useKeyedWrite";
import { setUndone, useGoingValue } from "./going-store";
import {
  fieldWords,
  keptValue,
  type UndoField,
  undoneChangeLabel,
  undoWords,
  valueWords,
} from "./plan-rows";
import { planTarget } from "./usePlan";

/** The Undo of one run as a surface presses it, and what it answered. */
export interface RunUndoing {
  /** What Undo answered, one line for each change of the run; undefined until it answered. */
  readonly lines: readonly GoingUndoLine[] | undefined;
  readonly undo: () => void;
  readonly pending: boolean;
  /** Why the Undo did not reach Dispatch, in words; undefined otherwise. */
  readonly refusal: string | undefined;
}

/**
 * Undo of one run (`POST /api/v1/going/runs/{id}/undo`), offered on the result of a run that
 * changed something while its conversation is in History: it writes each old value back, newest
 * first, closes each issue the run filed, rejects each of its suggestions still open, and names a
 * message sent, not undone. Its answer stays in the store (`GoingState.undone`), so the plan card
 * of the run and its folded line in a later turn show the same answer.
 */
export function useRunUndo(run: string): RunUndoing {
  const queryClient = useQueryClient();
  const lines = useGoingValue((of) => of.undone.get(run)?.lines);
  const [refusal, setRefusal] = useState<string>();
  const undo = useKeyedWrite<GoingUndoAnswer, string>({
    inline: true,
    label: () => "Undo the command bar's run",
    onError: (error) => setRefusal(apiErrorMessage(error, "The undo did not reach Dispatch.")),
    onSuccess: (answered) => {
      setUndone(run, answered?.lines ?? []);
      void queryClient.invalidateQueries({ queryKey: goingPlanQuery(run).queryKey });
      void queryClient.invalidateQueries({ queryKey: goingConversationsQuery().queryKey });
    },
    request: (id) => goingRunUndoRequest(id),
    target: planTarget,
  });
  return {
    lines,
    pending: undo.isPending,
    refusal,
    undo: () => {
      setRefusal(undefined);
      undo.mutate(run);
    },
  };
}

/** Who wrote a value Undo kept: a person by their login, an agent session by its name, and
 *  "someone else" where no event names one. */
function Writer({ by }: { readonly by: UndoField["by"] }): ReactNode {
  if (by === undefined) return "someone else";
  if (by.kind === "session") {
    return <AgentName kind="static" sessionId={by.id} storedTitle={undefined} />;
  }
  return by.kind === "user" ? by.id : `${by.kind} ${by.id}`;
}

/**
 * One field Undo kept as another writer left it (R6), which names it: "Title stays Bob's dome:
 * bob changed it after this run", the value in the issue page's words (`valueWords`), as the
 * server sends it as data. A field of an answer that names the value in its message alone, and
 * a field kept for another cause, reads the field and the server's message.
 */
function KeptField({ field }: { readonly field: UndoField }): ReactNode {
  const kept = keptValue(field);
  const named = field.field === "" ? "" : fieldWords(field.field);
  if (kept === undefined) {
    return named === "" ? field.message : `${named} kept: ${field.message}`;
  }
  return (
    <>
      {named === "" ? "It" : named} stays {valueWords(field.field, kept.now)}:{" "}
      <Writer by={kept.by} /> changed it after this run
    </>
  );
}

/**
 * What became of one change once its run was undone, in the person's words (design v2, state 7;
 * R6): each field kept as another writer left it, named with its value now; else "Reverted",
 * "Sent, not undone" for a message, or why it was not reverted.
 */
export function UndoResult({ line }: { readonly line: GoingUndoLine }): ReactNode {
  const kept = (line.fields ?? []).filter((field) => !field.ok && field.message !== "");
  if (line.undo === "not_undone" || kept.length === 0) return undoWords(line);
  return (
    <>
      {kept.map((field, index) => (
        <span key={field.field}>
          {index === 0 ? null : "; "}
          <KeptField field={field} />
        </span>
      ))}
    </>
  );
}

/**
 * Undo of a run that made no plan - a new issue filed from the line File as a new issue - on its
 * result: one button, then a line for each change it undid, the change named as its plan row
 * would name it ("File CORE-9: Reverted"). A run with a plan has its Undo on its plan card.
 */
export function RunUndo({ run }: { readonly run: string }): ReactNode {
  const undoing = useRunUndo(run);
  const lines = undoing.lines;
  return (
    <div className="space-y-1" data-run={run} data-testid="going-undo">
      {lines === undefined ? (
        <button
          className={secondaryButtonCompact}
          data-testid="going-undo-button"
          disabled={undoing.pending}
          onClick={undoing.undo}
          type="button"
        >
          Undo
        </button>
      ) : (
        <ul className={`text-xs ${textPrimaryOnSurface}`} data-testid="going-undo-lines">
          {lines.length === 0 ? (
            <li className={textMutedOnSurface}>Nothing of this run was left to undo.</li>
          ) : (
            lines.map((line, index) => (
              // One row can have two lines - a reason sent, not undone, and its close written
              // back - and the lines are one answer's, drawn once in Dispatch's order, so a line
              // is its place in that answer.
              // biome-ignore lint/suspicious/noArrayIndexKey: a line's place in the answer never changes
              <li data-testid="going-undo-line" key={index}>
                {undoneChangeLabel(line.tool, line.item)}: <UndoResult line={line} />
              </li>
            ))
          )}
        </ul>
      )}
      {undoing.refusal === undefined ? null : (
        <p className={`text-xs ${dangerText}`} role="alert">
          {undoing.refusal}
        </p>
      )}
    </div>
  );
}
