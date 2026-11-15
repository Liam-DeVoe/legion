import { ISSUE_TITLE_MAX } from "@legion/contracts";
import { type ReactNode, useEffect, useRef, useState } from "react";

import {
  bgTransparent,
  borderStrongHover,
  borderTransparent,
  focusBorder,
  pageInkText,
} from "../../theme/classes";
import { authorText } from "./page-structure";
import type { IssueDrafts } from "./useIssueDrafts";

/**
 * Row two of the header: the issue's title, which is the issue as a person names it, and the
 * editor it becomes when the reader puts the caret in it.
 *
 * The type is the prototype's `.title`: 22 px on 29 px from the phone's width up, held to two
 * lines there, and 20 px on 26 px below it, where nothing clamps it (dispatch://INFRA-1196
 * R13). `condensed` takes the type down a step while the header is stuck to the top.
 *
 * The heading and the editor are one box. Both sit in one grid cell, and while the reader
 * edits, the heading stays in that cell unseen, holding the draft and drawn whole, so the
 * editor - a `textarea` with the heading's font and wrapping - is exactly the heading's size at
 * every length and the reader reads the whole title as they write it. An `<input>` was one line
 * whatever the title: ending the edit of a title that wraps grew the row back by a line between
 * the mousedown that blurred the input and the mouseup, so the press on a control under the
 * title landed on nothing. A title longer than the clamp is drawn whole while it is edited, so
 * the read view keeps that height until the press that ended the edit has been delivered, and
 * only then clamps it again.
 */
export function IssueTitleRow({
  condensed,
  drafts,
  editing,
  isClosed,
  onEditingChange,
}: {
  condensed: boolean;
  drafts: IssueDrafts;
  editing: boolean;
  isClosed: boolean;
  onEditingChange: (editing: boolean) => void;
}): ReactNode {
  // Whether a mouse button is down right now, read by the editor's blur: a blur a press caused
  // keeps the heading at the editor's height until that press is released and its click has
  // been dispatched. A tap's mousedown comes after its pointerup, so the mouse's own events are
  // the ones that bracket the press on every input.
  const pressing = useRef(false);
  const [held, setHeld] = useState(false);
  useEffect(() => {
    if (!editing) return;
    const down = () => {
      pressing.current = true;
    };
    const up = () => {
      pressing.current = false;
    };
    window.addEventListener("mousedown", down, true);
    window.addEventListener("mouseup", up, true);
    return () => {
      window.removeEventListener("mousedown", down, true);
      window.removeEventListener("mouseup", up, true);
    };
  }, [editing]);

  const type = condensed
    ? "text-base leading-6"
    : "text-xl leading-[26px] md:text-[22px] md:leading-[29px]";
  // The box both views share, to the pixel: the font, the wrapping and the rule the editor
  // draws on focus, which the heading keeps transparent.
  const box = `[grid-area:1/1] min-w-0 rounded-md border px-1 font-semibold tracking-[-0.012em] whitespace-pre-wrap [overflow-wrap:anywhere] ${type} ${pageInkText}`;
  const whole = editing || held;

  return (
    <div className="-mx-1 mt-0.5 grid min-w-0">
      <h1
        aria-hidden={editing ? true : undefined}
        className={`${box} ${borderTransparent} ${whole ? "" : "md:line-clamp-2"} ${
          editing ? "invisible" : isClosed ? "" : `cursor-text ${borderStrongHover}`
        }`}
        onClick={() => {
          if (!isClosed) onEditingChange(true);
        }}
        onFocus={() => {
          if (!isClosed) onEditingChange(true);
        }}
        onKeyDown={(event) => {
          if (!isClosed && (event.key === "Enter" || event.key === " ")) {
            event.preventDefault();
            onEditingChange(true);
          }
        }}
        tabIndex={isClosed || editing ? -1 : 0}
        {...authorText}
      >
        {/* A trailing space or an empty draft still holds a line, as the editor does. */}
        {drafts.title === "" || drafts.title.endsWith(" ") ? `${drafts.title}\u200b` : drafts.title}
      </h1>
      {editing ? (
        <textarea
          aria-describedby={drafts.titleError === null ? undefined : "issue-title-help"}
          aria-label="Issue title"
          className={`${box} resize-none overflow-hidden outline-none ${borderTransparent} ${bgTransparent} ${borderStrongHover} ${focusBorder}`}
          disabled={isClosed}
          maxLength={ISSUE_TITLE_MAX}
          onBlur={() => {
            drafts.requestTitleSubmit();
            if (pressing.current) {
              setHeld(true);
              // The click follows the mouseup in the same task, so a task queued from the mouseup
              // runs once the click has been dispatched to the control under the pointer.
              window.addEventListener("mouseup", () => window.setTimeout(() => setHeld(false), 0), {
                capture: true,
                once: true,
              });
            }
            onEditingChange(false);
          }}
          // A title is one line of words: a line break typed or pasted is a space.
          onChange={(event) => drafts.writeTitle(event.target.value.replace(/\s*\n\s*/gu, " "))}
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              drafts.discardTitle();
              onEditingChange(false);
              return;
            }
            if (event.key === "Enter") {
              event.preventDefault();
              event.currentTarget.blur();
            }
          }}
          ref={(node) => node?.focus()}
          rows={1}
          value={drafts.title}
        />
      ) : null}
    </div>
  );
}
