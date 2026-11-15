import { type ReactNode, type RefObject, useLayoutEffect, useRef, useState } from "react";

import {
  pageInkText,
  pageOwnWordsRule,
  pageQuietText,
  pageRowAct,
  pageRowActs,
  pageRowOption,
  pageStateText,
  pageWordsText,
} from "../../../theme/classes";
import { bringOnScreen } from "../identity-block";
import type { TreeNode } from "../issue-tree";
import { NodeGlyph } from "../NodeGlyph";
import { type OutlineWriter, replyItemOf, takesAnswer } from "../outline-writer";
import { authorText, pageChrome } from "../page-structure";
import { CONTROL_LABELS, GLYPH_TOKENS } from "../page-words";

/**
 * The parts of an outline row the Decisions filter's rows draw as the conversation's rows draw
 * them (dispatch://INFRA-1196 R1, R6, R9, R16, R34, and the architect's ruling 1 on R1).
 *
 * Every function here is a copy of the function of the same name in `NodeRow.tsx`, which keeps
 * them to itself: `NodeOptions`, `RowActs`, `AskAnswer`, the cut (`useClamp`, `measureClamp`,
 * `blocksOf`) and the rule that brings a focused row on screen (`NodeRow`'s own layout effect,
 * here `useRowOnScreen`). They are copied word for word so that an ask reads and works in this
 * filter exactly as it does in the conversation, and the browser suite's c-row reads one row
 * wherever it is drawn. Integration exports the originals from `NodeRow.tsx`, points
 * `DecisionsRow.tsx` at them and deletes this file.
 */

/**
 * The choices an ask offers, under its question (the prototype's `.opts`): each by its own
 * words, the one the person chose filled and in the ink, the others hollow and grey, and while
 * the question waits on the reader every one in the waiting colour. The words an option carries
 * beside its name are drawn in the focused node's body (R6).
 *
 * An open question's options are the choices of its answer (R16, the prototype's `button.opt`):
 * a press picks one, or lets it go, and sets the line at the foot of the outline to answer the
 * question, where the keyboard picks them too; the one picked is drawn on the waiting tint.
 */
export function NodeOptions({
  body,
  node,
  writer,
}: {
  body: boolean;
  node: TreeNode;
  writer: OutlineWriter | undefined;
}): ReactNode {
  const chosen = new Set(node.answer?.selected ?? []);
  const answering = writer !== undefined && takesAnswer(node);
  const picks =
    writer?.answer !== undefined && writer.answer.nodeId === node.id ? writer.answer.selected : [];
  return (
    <ul className="my-0.5 text-sm leading-[21px]" data-node-options="">
      {(node.options ?? []).map((option) => {
        const picked = chosen.has(option.label);
        const words = (
          <>
            <span aria-hidden="true" className="w-3 shrink-0 text-center text-xs" {...pageChrome}>
              {picked || picks.includes(option.label) ? "●" : "○"}
            </span>
            <span className="min-w-0 break-words">
              <span {...authorText}>{option.label}</span>
              {body && option.description !== undefined ? (
                <span className={`font-normal ${pageQuietText}`}>
                  <span {...pageChrome}> — </span>
                  <span {...authorText}>{option.description}</span>
                </span>
              ) : null}
            </span>
          </>
        );
        return (
          <li
            className={
              answering
                ? ""
                : `flex items-baseline gap-2 ${
                    picked
                      ? `font-semibold ${pageInkText}`
                      : node.waiting
                        ? pageStateText.waiting
                        : pageQuietText
                  }`
            }
            data-node-option={picked ? "chosen" : ""}
            key={option.label}
          >
            {answering ? (
              // Out of the tab order, as every control inside a row is: the line the press sets
              // draws the same options as its own chips, which the keyboard reaches (R9).
              <button
                aria-pressed={picks.includes(option.label)}
                className={`dispatch-inline-link ${pageRowOption}`}
                data-pick-option={option.label}
                onClick={() => writer.pick(node, option.label)}
                tabIndex={-1}
                type="button"
              >
                {words}
              </button>
            ) : (
              words
            )}
          </li>
        );
      })}
    </ul>
  );
}

/**
 * What the reader can do with the focused row, on one quiet line under it (the prototype's
 * `.acts`, drawn on the focused row alone): Reply on every row that takes a reply - a comment, a
 * message, an ask whatever its state, and the issue itself, whose reply is a new comment on it
 * (R16, dispatch://INFRA-3677) - Answer on an open question, and Change answer on a question the
 * reader answered. Each sets the line at the foot of the outline and puts the caret in it; `r`
 * and `a` press the first two from the keyboard (`useTreeKeymap.ts`). A typed node - a
 * statement, a check, a decision - takes no reply in today's model, which holds no reply to a
 * block (dispatch://INFRA-1253), so it draws none.
 *
 * A press leaves the focus where it is until the action moves it to the line, as the row's other
 * controls do, so the press never lands on a row that moved under it.
 */
export function RowActs({
  node,
  ownAnswer,
  writer,
}: {
  node: TreeNode;
  ownAnswer: boolean;
  writer: OutlineWriter;
}): ReactNode {
  const acts: { readonly act: string; readonly label: string; readonly run: () => void }[] = [];
  if (takesAnswer(node)) {
    acts.push({ act: "answer", label: CONTROL_LABELS.answer, run: () => writer.answerNode(node) });
  }
  if (ownAnswer && node.type === "ask" && node.state === "answered") {
    acts.push({
      act: "change-answer",
      label: CONTROL_LABELS.change_answer,
      run: () => writer.changeAnswer(node),
    });
  }
  const item = replyItemOf(node);
  if (item !== undefined || node.type === "issue") {
    acts.push({
      act: "reply",
      label: CONTROL_LABELS.reply,
      run: () => (item === undefined ? writer.writeOnIssue() : writer.replyTo(item)),
    });
  }
  if (acts.length === 0) return null;
  return (
    <p className={pageRowActs} data-row-acts="">
      {acts.map(({ act, label, run }) => (
        <button
          className={`dispatch-inline-link ${pageRowAct}`}
          data-row-act={act}
          key={act}
          onClick={run}
          onMouseDown={(event) => event.preventDefault()}
          tabIndex={-1}
          type="button"
          {...pageChrome}
        >
          {label}
        </button>
      ))}
    </p>
  );
}

/**
 * A person's answer to an ask, which is part of the ask and no node of its own
 * (dispatch://INFRA-1150 R3, dispatch://INFRA-1196 R1): the page's word for it, who answered by
 * a name and a shape as every author is drawn (R9), what they chose where the ask offers no list
 * to mark it in, and what they wrote, whole and character for character, keeping the line breaks
 * they typed (R9, ruling 1). The reader's own answer carries the ink rule their own words carry
 * (R34).
 */
export function AskAnswer({
  answerer,
  node,
  own,
}: {
  answerer: string;
  node: TreeNode;
  own: boolean;
}): ReactNode {
  const answer = node.answer;
  if (answer === undefined) return null;
  const kind = answer.by.kind === "user" ? "person" : "agent";
  return (
    <div
      className={`mt-1 flex min-w-0 flex-col ${own ? pageOwnWordsRule : ""}`}
      data-node-answer=""
    >
      <p
        className={`flex min-w-0 flex-wrap items-center gap-x-1.5 text-xs leading-5 ${pageQuietText}`}
      >
        <span {...pageChrome}>{CONTROL_LABELS.answer}</span>
        <span className="inline-flex items-center gap-1" data-answer-author="">
          <NodeGlyph className="size-3 shrink-0" token={GLYPH_TOKENS.authors[kind]} />
          <span className={own ? `font-semibold ${pageInkText}` : ""} {...authorText}>
            {answerer}
          </span>
        </span>
      </p>
      {node.options !== undefined || answer.selected.length === 0 ? null : (
        <ul className={`flex min-w-0 flex-col text-sm leading-[21px] ${pageInkText}`}>
          {answer.selected.map((option) => (
            <li
              className="break-words font-semibold"
              data-answer-choice=""
              key={option}
              {...authorText}
            >
              {option}
            </li>
          ))}
        </ul>
      )}
      {answer.text === "" ? null : (
        <p
          className={`text-[15px] leading-6 whitespace-pre-wrap break-words ${own ? pageInkText : pageWordsText}`}
          data-answer-text=""
          {...authorText}
        >
          {answer.text}
        </p>
      )}
    </div>
  );
}

/**
 * Brings the focused row inside the viewport and clear of the sticky identity block after every
 * move, by the one rule the whole page places a target with (R9, `identity-block.ts`), and again
 * whenever the row grows under it.
 *
 * Except while a press inside the row is held. A press on an unfocused row focuses it on
 * mousedown, before the press ends, and a scroll then moves what the pointer went down on: the
 * press ends on something else and the control it was on never hears it. Measured at 1440 px on
 * the Decisions preset, a row whose top sat 24 px under the identity block scrolled 32 px
 * between mousedown and mouseup, and the reader's Keep was never sent. The reader pressed what
 * they could see, so there is nothing to bring on screen.
 *
 * Answers the row's own `onPointerDown`.
 */
export function useRowOnScreen(row: RefObject<HTMLElement | null>, focused: boolean): () => void {
  const pressed = useRef(false);
  const press = () => {
    pressed.current = true;
    const ended = new AbortController();
    const release = () => {
      pressed.current = false;
      ended.abort();
    };
    window.addEventListener("pointerup", release, { capture: true, signal: ended.signal });
    window.addEventListener("pointercancel", release, { capture: true, signal: ended.signal });
  };
  useLayoutEffect(() => {
    const element = row.current;
    if (!focused || element === null) return;
    const place = () => {
      if (!pressed.current) bringOnScreen(element);
    };
    place();
    if (typeof ResizeObserver === "undefined") return;
    const grown = new ResizeObserver(place);
    grown.observe(element);
    return () => grown.disconnect();
  }, [focused, row]);
  return press;
}

/** The prototype's `CUT_LINES`: how many lines of an agent's words or an answered question a row
 *  draws before it cuts them. */
const CUT_LINES = 6;

/** How many words a cut must hide before it is made: fewer are drawn whole. */
const CUT_WORDS = 20;

/** How far past six lines a text may run and still be drawn whole, as the prototype allows. */
const CUT_SLACK = 12;

/** What a row draws of words it cuts: how tall it draws them, how many words it holds back,
 *  and whether the cut was moved to the foot of a row of pictures the sixth line crossed. */
export interface Clamp {
  readonly height: number;
  readonly hidden: number;
  readonly pictures: boolean;
}

/**
 * Where a row cuts its words, read from the words as the browser laid them out, which is the
 * prototype's own reading (`measureClamps`): one layout read per row that may be cut - an
 * agent's words and an answered question - and again when its width or its words change, never
 * one for any other row. Undefined where the words are drawn whole.
 */
export function useClamp(
  words: RefObject<HTMLElement | null>,
  cuttable: boolean
): Clamp | undefined {
  const [clamp, setClamp] = useState<Clamp | undefined>(undefined);
  useLayoutEffect(() => {
    const element = words.current;
    if (!cuttable || element === null) {
      setClamp(undefined);
      return;
    }
    const measure = () => {
      const next = measureClamp(element);
      setClamp((held) =>
        held?.height === next?.height &&
        held?.hidden === next?.hidden &&
        held?.pictures === next?.pictures
          ? held
          : next
      );
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    // The words' own box is held to the cut while it is drawn, so their growing - a reading
    // that paints after the row, a picture that loads - is seen on what they are drawn in.
    for (const child of element.children) observer.observe(child);
    return () => observer.disconnect();
  }, [cuttable, words]);
  return cuttable ? clamp : undefined;
}

/** The cut of the words `element` holds, as the prototype's `measureClamps` reads it. */
function measureClamp(element: HTMLElement): Clamp | undefined {
  const pitch = Number.parseFloat(getComputedStyle(element).lineHeight);
  const limit = CUT_LINES * (Number.isFinite(pitch) ? pitch : 24);
  if (element.scrollHeight <= limit + CUT_SLACK) return undefined;
  const top = element.getBoundingClientRect().top;
  // The words a cut would hide, block by block: a block below the line whole, and of a block the
  // line crosses the share of its words below it.
  let hidden = 0;
  for (const block of blocksOf(element)) {
    const box = block.getBoundingClientRect();
    const from = box.top - top;
    const to = box.bottom - top;
    const count = (block.textContent ?? "").split(/\s+/u).filter(Boolean).length;
    if (from >= limit) hidden += count;
    else if (to > limit && box.height > 0) hidden += count * ((to - limit) / box.height);
  }
  const held = Math.round(hidden);
  if (held <= CUT_WORDS) return undefined;
  // A row of pictures the line crosses is drawn whole, and the cut moves to its foot.
  let height = limit;
  let pictures = false;
  for (const picture of element.querySelectorAll("img")) {
    const box = picture.getBoundingClientRect();
    if (box.top - top < limit && box.bottom - top > limit) {
      height = Math.max(height, box.bottom - top + 6);
      pictures = true;
    }
  }
  return { height, hidden: held, pictures };
}

/** The blocks of the words `element` holds: the paragraphs, lists and pictures of a text drawn
 *  from its reading, and the one paragraph of a text drawn as stored. */
function blocksOf(element: HTMLElement): Element[] {
  const drawn = element.querySelector(".dispatch-markdown");
  if (drawn !== null && drawn.children.length > 0) return [...drawn.children];
  const first = element.firstElementChild;
  return first === null ? [] : [first];
}
