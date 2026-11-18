import { ISSUE_TITLE_MAX } from "@legion/contracts";
import { type ReactNode, useEffect, useLayoutEffect, useRef, useState } from "react";

import {
  bgTransparent,
  borderStrongHover,
  borderTransparent,
  focusBorder,
  pageInkText,
} from "../../theme/classes";
import type { IssueDrafts } from "../issue/useIssueDrafts";
import { hearPresses } from "./focus-keeper";
import { authorText } from "./page-structure";

/**
 * Row two of the header: the issue's title, which is the issue as a person names it, and the
 * editor it becomes when the reader presses it: a click, Enter or Space on the heading, or the
 * header's `e` key, which clicks it (`useHeaderKeys.ts`).
 *
 * The type is the prototype's `.title`: 600 weight, 22 px on 29 px from 761 px up, held to two
 * lines there, and 20 px on 26 px at 760 px and below, where nothing clamps it. It keeps that
 * size while the identity block is stuck to the top of the screen, as the approved picture of
 * the scrolled page draws it.
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
 *
 * Escape and Enter end the edit from the keyboard, and give the focus back to the heading the
 * reader opened the editor from, so the keyboard reader is where they were. A focus alone
 * never opens the editor: the page gives the focus back to the heading on its own - Details
 * closing onto the control that opened it, a return to the window - and an editor opened then
 * would take the next shortcut key as text of the title.
 */
export function IssueTitleRow({
  drafts,
  isClosed,
}: {
  drafts: IssueDrafts;
  isClosed: boolean;
}): ReactNode {
  const [editing, setEditing] = useState(false);
  // Whether a press of the reader's is under way, read by the editor's blur: a blur a press
  // caused keeps the heading at the editor's height until that press is over and its click has
  // been dispatched to the control under the pointer. The press is read from the page's one
  // reader of presses (`hearPresses`), which the focus keeper reads too, so it is over at the
  // first of the ways a press ends: its pointerup where it has moved the focus by then or is of a
  // button other than the primary one, its click, its pointercancel or its dragend as it becomes
  // a drag, its contextmenu, and the blur of the window. A tap's pointer is up before the browser
  // sends it the mousedown that blurs the editor, and the reader keeps a press of the primary
  // button that has moved no focus by its pointerup under way until its click, so that blur
  // still finds the tap under way. The presses are heard while the row is drawn, not only while
  // it edits: the blur a press causes ends the edit while the press is under way, so a hearing
  // taken away with the editor missed the press's end, and the next edit a key ended read a press
  // that was over and held the title unclamped.
  const pressing = useRef(false);
  const [held, setHeld] = useState(false);
  // Whether the heading is held at the editor's height for the press under way, which lets it go
  // as that press ends.
  const heldForPress = useRef(false);
  const heading = useRef<HTMLHeadingElement>(null);
  const editor = useRef<HTMLTextAreaElement | null>(null);
  // Set by a key that ends the edit, and read once the heading is drawn again: the focus goes
  // back to it.
  const returning = useRef(false);
  useEffect(() => {
    if (editing || !returning.current) return;
    heading.current?.focus({ preventScroll: true });
    returning.current = false;
  }, [editing]);
  useEffect(() => {
    const page = heading.current?.ownerDocument;
    if (page === undefined) return;
    return hearPresses(
      page,
      () => {
        pressing.current = true;
      },
      () => {
        pressing.current = false;
        if (!heldForPress.current) return;
        heldForPress.current = false;
        // The heading is clamped again one task later: a press of the primary button that moved
        // the focus is over at its pointerup, and its click follows in the same task, so the
        // click reaches the control under the pointer before the title shrinks from under it.
        window.setTimeout(() => setHeld(false), 0);
      }
    );
  }, []);
  // Ends the edit as the editor's blur does: the draft is sent, and a press under way holds the
  // heading at the editor's height until it ends.
  const endEdit = () => {
    drafts.requestTitleSubmit();
    if (pressing.current) {
      // The press under way lets the heading go as it ends.
      heldForPress.current = true;
      setHeld(true);
    }
    setEditing(false);
  };
  // Read through a ref by the window's listener below, which is taken once for the edit, so the
  // edit ends with the draft and the issue as they stand when the reader comes back.
  const latestEndEdit = useRef(endEdit);
  useLayoutEffect(() => {
    latestEndEdit.current = endEdit;
  });
  useEffect(() => {
    if (!editing) return;
    const view = heading.current?.ownerDocument.defaultView;
    if (view == null) return;
    // A switch to another window leaves the edit open (the editor's blur below), and the focus
    // can move while the reader is away, by the header's focus keeper or the page's own code,
    // with no blur reaching the editor: Chromium sends none while the page has no focus. An
    // editor that no longer holds the focus as the reader comes back ends the edit as its blur
    // would have.
    const returned = () => {
      if (view.document.activeElement !== editor.current) latestEndEdit.current();
    };
    view.addEventListener("focus", returned);
    return () => view.removeEventListener("focus", returned);
  }, [editing]);
  // An issue closed while its title is edited, as an event's refresh can close it while the
  // reader is away, takes no title (`useIssueDrafts` refuses one), and its disabled editor would
  // hold a draft nothing ends: the edit ends and drops the draft, and the heading reads the
  // issue's title.
  useEffect(() => {
    if (!editing || !isClosed) return;
    drafts.discardTitle();
    setEditing(false);
  }, [drafts, editing, isClosed]);

  const type = "text-xl leading-[26px] min-[761px]:text-[22px] min-[761px]:leading-[29px]";
  // The box both views share, to the pixel: the font, the wrapping and the rule the editor
  // draws on focus, which the heading keeps transparent. The rule and the padding inside it
  // are 5 px on each side and 1 px above and below, which the wrapper's negative margins take
  // back, so the words have the header's whole column - 362 px at 390 px, the prototype's own -
  // and the row is the prototype's height: 2 px above the words and none below.
  const box = `[grid-area:1/1] min-w-0 rounded-md border px-1 font-semibold tracking-[-0.012em] whitespace-pre-wrap [overflow-wrap:anywhere] ${type} ${pageInkText}`;
  const whole = editing || held;

  return (
    <div className="-mx-[5px] mt-px -mb-px grid min-w-0">
      <h1
        aria-hidden={editing ? true : undefined}
        className={`${box} ${borderTransparent} ${whole ? "" : "min-[761px]:line-clamp-2"} ${
          editing ? "invisible" : isClosed ? "" : `cursor-text ${borderStrongHover}`
        }`}
        onClick={() => {
          if (!isClosed) setEditing(true);
        }}
        onKeyDown={(event) => {
          if (!isClosed && (event.key === "Enter" || event.key === " ")) {
            event.preventDefault();
            setEditing(true);
          }
        }}
        ref={heading}
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
          onBlur={(event) => {
            // A switch to another window or tab blurs the editor but leaves it the page's focused
            // element, and the browser focuses it again as the reader comes back: they have not
            // left the editor, so it stays open with their draft, sends nothing and leaves the
            // press alone.
            if (!event.currentTarget.ownerDocument.hasFocus()) return;
            endEdit();
          }}
          // A title is one line of words: a line break typed or pasted is a space.
          onChange={(event) => drafts.writeTitle(event.target.value.replace(/\s*\n\s*/gu, " "))}
          onKeyDown={(event) => {
            // The Escape that cancels an IME composition and the Enter that confirms one are the
            // input method's keys, not the reader ending the edit. Safari sends them after its
            // compositionend, with `isComposing` false and keyCode 229 (WebKit bug 165004).
            if (event.nativeEvent.isComposing || event.keyCode === 229) return;
            if (event.key === "Escape") {
              drafts.discardTitle();
              returning.current = true;
              setEditing(false);
              return;
            }
            if (event.key === "Enter") {
              event.preventDefault();
              returning.current = true;
              event.currentTarget.blur();
            }
          }}
          ref={(node) => {
            editor.current = node;
            node?.focus();
          }}
          rows={1}
          value={drafts.title}
        />
      ) : null}
    </div>
  );
}
