import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type KeyboardEvent, type ReactNode, useState } from "react";

import { api } from "../../api/client";
import type { AnswerAskInput, Ask } from "../../api/types";
import { QueryError } from "../../components/QueryError";
import { RefusableButton } from "../../components/RefusableButton";
import { pageComposer, pageRowAct } from "../../theme/classes";
import { useHeldSend } from "../conversation/held-sends";
import { MentionComposer, type OutlineKind } from "../conversation/MentionComposer";
import { answerAskInput } from "../inbox/answer-ask";
import { useAnswerDraft } from "../inbox/answer-drafts";
import { DeclineControl } from "../inbox/DeclineControl";
import { useOutstandingAnswerWrite } from "../inbox/outstanding-answers";
import { invalidateOwnerReads } from "../inbox/owner-reads";
import { answerFailure } from "../inbox/useAskAnswerForm";
import { AgentName } from "../refs/AgentName";
import { firstLineOf } from "./issue-tree";
import { type AnswerTarget, type OutlineWriter, useOutlineWriter } from "./outline-writer";
import { authorText, pageChrome } from "./page-structure";
import { CONTROL_LABELS } from "./page-words";

/** The kinds the line writes, as its chips: a comment and a question (R16). */
const KINDS: readonly OutlineKind[] = [
  {
    kind: "comment",
    label: (
      <>
        <span aria-hidden="true">❝</span>
        <span {...pageChrome}>{CONTROL_LABELS.composer_comment}</span>
      </>
    ),
  },
  {
    kind: "ask",
    label: (
      <>
        <span aria-hidden="true">?</span>
        <span {...pageChrome}>{CONTROL_LABELS.composer_question}</span>
      </>
    ),
  },
];

/**
 * The one place to write on the issue page, at the foot of its outline (dispatch://INFRA-1196
 * R16, the prototype's `#composer`, the pictures page-v3-conversation-top-1440 and -390): one
 * line, which Enter sends, sticky at the foot of the screen while the outline runs on under it.
 *
 * It writes what today's model can write. With nothing set, a comment on the issue, which the
 * outline draws under the issue, or a question with the Question chip pressed, which is an ask
 * of the issue; the pictures' Requirement and Decision chips wait on dispatch://INFRA-1253 and
 * dispatch://INFRA-1150, and none is drawn that would write nothing. A row's Reply sets it to
 * reply to that row, its target named above the line: a comment written as a reply, which the
 * outline draws under the row it answers, and mentions work as on every composer
 * (`MentionComposer`). An open question's Answer, or one of its options, sets it to answer that
 * question: the line in the waiting colour, the question's options as its chips, Decline to
 * answer beside Send answer.
 *
 * With no filter on it is drawn. Under a filter, whose pictures draw no line, it is drawn only
 * while the reader has asked to write - a Reply or an Answer pressed there - or holds a send of
 * their own, so a refusal is never out of sight; and it is not drawn while a document is open in
 * place of the outline, whose own margin takes a comment on a selection.
 */
export function IssueComposer({
  documentOpen,
  filterOn,
  isClosed,
  issueKey,
}: {
  documentOpen: boolean;
  filterOn: boolean;
  isClosed: boolean;
  issueKey: string;
}): ReactNode {
  const writer = useOutlineWriter();
  const held = useHeldSend(writer?.sendKey);
  if (writer === undefined) return null;
  const asked = writer.reply !== null || writer.answer !== undefined;
  // Mounted, hidden, wherever it is not drawn - under a filter with nothing asked of it, and
  // while a document is open in place of the outline - so a draft the reader began is there
  // when they come back to it.
  const shown = !documentOpen && (!filterOn || asked || held !== undefined);
  return (
    <section
      aria-label={CONTROL_LABELS.write}
      className={pageComposer.foot}
      data-testid="issue-composer"
      hidden={!shown}
      {...authorText}
    >
      {writer.answer === undefined ? (
        <WriteLine isClosed={isClosed} issueKey={issueKey} writer={writer} />
      ) : (
        <AnswerLine
          // One line per question: what the reader typed and picked is that question's.
          key={writer.answer.askId}
          issueKey={issueKey}
          target={writer.answer}
          writer={writer}
        />
      )}
    </section>
  );
}

/** The line that writes a comment, a question, or a reply (`MentionComposer`'s outline look). */
function WriteLine({
  isClosed,
  issueKey,
  writer,
}: {
  isClosed: boolean;
  issueKey: string;
  writer: OutlineWriter;
}): ReactNode {
  const queryClient = useQueryClient();
  const reply = writer.reply;
  const editing = writer.editing;
  if (editing !== undefined) {
    // The reader's own comment, edited in the same line: its words in the box, Save writes them
    // back, and the outline draws the comment again from the read that answers after.
    return (
      <MentionComposer
        edit={editing}
        key={`edit:${editing.id}`}
        mutationKey={[...writer.sendKey, "edit", editing.id]}
        onClose={writer.clear}
        onSent={writer.clear}
        outline={{
          cancelReply: CONTROL_LABELS.write_on_issue,
          kinds: [],
          placeholder: CONTROL_LABELS.write_placeholder,
          send: CONTROL_LABELS.save,
          target: (
            <>
              <span {...pageChrome}>{CONTROL_LABELS.editing}</span>{" "}
              <span>{firstLineOf(editing.body).line}</span>
            </>
          ),
        }}
        owner={{ issueKey, kind: "issue" }}
        saveEdit={async (id, body, carried, bounds) => {
          const saved = await api.editComment(id, { body, ...carried }, bounds);
          await queryClient.invalidateQueries({ queryKey: ["comments", issueKey] });
          return saved;
        }}
      />
    );
  }
  // A reply is a comment under the row it answers: a question asked from the line has no row to
  // sit under in today's model, so the chips are drawn only while the line writes on the issue.
  const kind = reply === null ? writer.kind : "comment";
  return (
    <MentionComposer
      closed={isClosed}
      kind={kind}
      mutationKey={writer.sendKey}
      onCancelReply={() => writer.setReply(null)}
      onClose={() => (document.activeElement as HTMLElement | null)?.blur()}
      onKindChange={(next) => {
        if (next === "ask" || next === "comment") writer.setKind(next);
        return undefined;
      }}
      onSent={() => writer.setReply(null)}
      outline={{
        cancelReply: CONTROL_LABELS.write_on_issue,
        kinds: reply === null && !isClosed ? KINDS : [],
        placeholder: CONTROL_LABELS.write_placeholder,
        send: CONTROL_LABELS.send,
        target:
          reply === null ? undefined : (
            <>
              <span {...pageChrome}>{CONTROL_LABELS.replying_to}</span>{" "}
              <b className="font-semibold text-page-ink">
                <AgentName actor={reply.actor} inline />
              </b>{" "}
              <span>{firstLineOf(reply.excerpt).line}</span>
            </>
          ),
      }}
      owner={{ issueKey, kind: "issue" }}
      replyTo={reply}
    />
  );
}

/**
 * The line that answers an open question: the question named above it in the waiting colour,
 * what the reader picked beside it, the question's options as chips the keyboard reaches - the
 * row's own options are the pointer's - Decline to answer, and Send answer. A pick or words
 * alone answer it: words alone are the reader's own answer, as the old card's Other was.
 *
 * The write is the Inbox's (`useOutstandingAnswerWrite`), so the question's row leaves the Inbox
 * as the answer goes and comes back if Dispatch refuses it, and what the reader typed is the
 * question's draft (`answer-drafts.ts`), which every surface of that question shares.
 */
function AnswerLine({
  issueKey,
  target,
  writer,
}: {
  issueKey: string;
  target: AnswerTarget;
  writer: OutlineWriter;
}): ReactNode {
  const queryClient = useQueryClient();
  const asks = useQuery({
    queryFn: () => api.listIssueAsks(issueKey, "all"),
    queryKey: ["asks", issueKey],
  });
  const ask = (asks.data ?? []).find((one) => one.id === target.askId);
  const [draft, setDraft] = useAnswerDraft(target.askId);
  // A change of the reader's own answer starts from its words and is this line's own text, never
  // the question's shared draft: a recorded answer is not unsent text.
  const [changed, setChanged] = useState(target.replacing?.text ?? "");
  const text = target.replacing === undefined ? draft : changed;
  const setText = target.replacing === undefined ? setDraft : setChanged;
  const mutation = useOutstandingAnswerWrite({
    askId: target.askId,
    label: () => `Answer to “${ask?.question ?? ""}”`,
    removesRow: () => target.replacing === undefined,
    write: (input: AnswerAskInput, bounds) => api.answerAsk(target.askId, input, bounds),
    onSuccess: () => {
      setText("");
      writer.clear();
      if (ask !== undefined) invalidateOwnerReads(queryClient, ask);
      void queryClient.invalidateQueries({ queryKey: ["ask-thread", target.askId] });
    },
  });
  // A question answered or closed elsewhere while the line held it is not answered here.
  if (ask === undefined || (ask.state !== "open" && target.replacing === undefined)) return null;
  const refusal =
    target.selected.length === 0 && text.trim() === "" ? CONTROL_LABELS.answer_placeholder : null;
  const send = () => {
    if (refusal !== null || mutation.isPending) return;
    mutation.mutate(answerInput(ask, target, text));
  };
  const keys = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Escape") {
      event.preventDefault();
      writer.clear();
      return;
    }
    if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    send();
  };
  const failure = mutation.error === null ? null : answerFailure(mutation.error);
  return (
    <form
      aria-label={CONTROL_LABELS.answering}
      className={`${pageComposer.box} ${pageComposer.answering}`}
      data-issue-composer=""
      data-answering={target.askId}
      onSubmit={(event) => {
        event.preventDefault();
        send();
      }}
    >
      <fieldset
        className="flex min-w-0 flex-col gap-[3px] border-0 p-0"
        disabled={mutation.isPending}
      >
        <div
          className={`${pageComposer.target} ${pageComposer.targetAnswering}`}
          data-composer-target=""
        >
          <span className="min-w-0 truncate">
            <span {...pageChrome}>{CONTROL_LABELS.answering}</span>{" "}
            <b className="font-semibold text-page-ink">{firstLineOf(ask.question).line}</b>
            {target.selected.length === 0 ? null : (
              <>
                <span {...pageChrome}> · {CONTROL_LABELS.answer_chose}</span>{" "}
                <span>{target.selected.map((label) => `“${label}”`).join(", ")}</span>
              </>
            )}
          </span>
          <button
            aria-label={CONTROL_LABELS.write_on_issue}
            className={pageComposer.targetClear}
            onClick={writer.clear}
            type="button"
          >
            <span aria-hidden="true">×</span>
          </button>
        </div>
        <div className={pageComposer.line}>
          <textarea
            aria-label={CONTROL_LABELS.answer}
            className={pageComposer.field}
            onChange={(event) => setText(event.target.value)}
            onKeyDown={keys}
            placeholder={CONTROL_LABELS.answer_placeholder}
            rows={1}
            value={text}
          />
          <div className={pageComposer.controls}>
            {(ask.options ?? []).map((option) => (
              <button
                aria-pressed={target.selected.includes(option.label)}
                className={pageComposer.option}
                data-answer-option={option.label}
                key={option.label}
                onClick={() => writer.toggle(option.label)}
                type="button"
              >
                {option.label}
              </button>
            ))}
            {target.replacing === undefined ? (
              <DeclineControl
                ask={ask}
                decline={(id, input, bounds) => api.declineAsk(id, input, bounds)}
                look={{ box: "contents", button: `text-xs font-medium ${pageRowAct}` }}
                onDeclined={writer.clear}
              />
            ) : null}
            <RefusableButton
              busy={mutation.isPending ? "Sending…" : undefined}
              look="page-send-answer"
              refusal={refusal}
              type="submit"
            >
              <span {...pageChrome}>{CONTROL_LABELS.send_answer}</span>
            </RefusableButton>
          </div>
        </div>
        {failure === null ? null : (
          <QueryError
            message={failure.message}
            onRetry={failure.retryable ? send : undefined}
            retrying={mutation.isPending}
          />
        )}
      </fieldset>
    </form>
  );
}

/** The answer the line sends: the options picked, the reader's words, and, for a change of their
 *  own answer, the moment of the answer it replaces. */
function answerInput(ask: Ask, target: AnswerTarget, text: string): AnswerAskInput {
  const input = answerAskInput(ask, [...target.selected], text);
  return target.replacing === undefined
    ? input
    : { ...input, expected_answer_at: target.replacing.at };
}
