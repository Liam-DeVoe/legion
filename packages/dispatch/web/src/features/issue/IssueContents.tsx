import { useQuery } from "@tanstack/react-query";
import { type KeyboardEvent, type ReactNode, useEffect, useMemo, useRef, useState } from "react";

import { whoAmIQuery } from "../../api/queries";
import {
  focusVisibleRing,
  pageGuideBg,
  pageHairBg,
  pageHairBorder,
  pageInkBg,
  pageInkBorder,
  pageInkText,
  pageIssueBg,
  pageLineBorder,
  pageMarkBg,
  pageMutedText,
  pagePaneBg,
  pagePaperBg,
  pagePersonBg,
  pageQuestionBg,
  pageQuietText,
} from "../../theme/classes";
import { useDialog } from "../shell/useDialog";
import { useFocusedNode } from "./focused-node";
import { nodeWords, type TreeNode } from "./issue-tree";
import { oneLineCut, useOneLineBox } from "./one-line";
import { authorText, pageChrome } from "./page-structure";
import { CONTROL_LABELS, NODE_TYPE_NAMES, sentenceCase } from "./page-words";
import { pathTo } from "./tree-fold";

/**
 * The Contents pane at the left of the issue page (dispatch://INFRA-1196 R14, R8, the
 * prototype's `#left`): the issue's three sentences under "In short", then the list of each
 * question and each of the reader's own threads, which go to their node in the outline. From
 * 1024 px up it stands beside the outline; below that the sentences are drawn above the outline
 * and the list is behind the Outline control beside the filters (`OutlineSheet`).
 *
 * The pane holds no property of the issue, and no sentence about itself: its two headings and
 * its one toggle are its whole chrome (R22).
 */
export function IssueContentsPane({
  brief,
  list,
}: {
  /** The issue's three sentences and the two rows under them (`IssueThreeThings`). */
  brief: ReactNode;
  /** The list (`ContentsList`). */
  list: ReactNode;
}): ReactNode {
  return (
    <aside
      aria-label={CONTROL_LABELS.contents}
      className={`sticky top-[var(--issue-identity-height,0px)] flex h-[calc(100dvh-var(--issue-identity-height,0px))] w-[240px] shrink-0 flex-col border-r min-[1241px]:w-[260px] ${pageLineBorder} ${pagePaneBg}`}
      data-testid="issue-contents"
    >
      {brief}
      {list}
    </aside>
  );
}

/**
 * The list below 1024 px: the Outline control beside the filters, and the sheet it opens over
 * the page, which holds the list and closes once an entry is chosen, as the prototype's `#sheet`
 * does. The three sentences are not in it: below that width they are above the outline (R8).
 */
export function OutlineSheet({ list }: { list: (close: () => void) => ReactNode }): ReactNode {
  const [open, setOpen] = useState(false);
  const closeButton = useRef<HTMLButtonElement>(null);
  const dialog = useDialog<HTMLDivElement>({
    initialFocusRef: closeButton,
    onClose: () => setOpen(false),
    open,
  });
  return (
    <>
      <button
        aria-expanded={open}
        aria-haspopup="dialog"
        className={`min-h-11 shrink-0 rounded-lg border px-3 text-sm font-semibold ${pageHairBorder} ${pagePaperBg} ${pageInkText} ${focusVisibleRing}`}
        onClick={() => setOpen(true)}
        type="button"
        {...pageChrome}
      >
        {CONTROL_LABELS.outline}
      </button>
      {open ? (
        <div
          aria-label={CONTROL_LABELS.contents}
          aria-modal="true"
          className={`fixed inset-x-0 top-14 bottom-0 z-50 flex flex-col rounded-t-[14px] shadow-[0_-10px_30px_rgba(0,0,0,0.2)] ${pagePaneBg} ${pageInkText}`}
          data-testid="issue-outline-sheet"
          ref={dialog.containerRef}
          role="dialog"
        >
          <div className="flex shrink-0 justify-end px-3.5 pt-2.5">
            <button
              className={`rounded-[7px] border px-3 py-[3px] text-sm font-medium ${pageHairBorder} ${pagePaperBg} ${pageMutedText} ${focusVisibleRing}`}
              onClick={() => setOpen(false)}
              ref={closeButton}
              type="button"
              {...pageChrome}
            >
              {CONTROL_LABELS.close}
            </button>
          </div>
          {list(() => setOpen(false))}
        </div>
      ) : null}
    </>
  );
}

/** One entry of the list: a question, or a thread the reader began, with the entries under it. */
export interface ContentsEntry {
  readonly children: readonly ContentsEntry[];
  readonly node: TreeNode;
}

/**
 * The entries of the list, read off the tree the outline draws: the issue itself first, then
 * each ask and each comment or message the reader wrote that begins a thread, each under the
 * nearest such entry above it in the tree, in the tree's own order. A node drawn under a second
 * parent is listed once, at its own place.
 */
export function contentsOf(root: TreeNode, viewer: string | undefined): ContentsEntry[] {
  const TURNS: Partial<Record<TreeNode["type"], true>> = { comment: true, message: true };
  const walk = (node: TreeNode, into: ContentsEntry[]): void => {
    for (const child of node.children) {
      const begunByReader =
        TURNS[child.type] === true &&
        TURNS[node.type] !== true &&
        child.author?.kind === "user" &&
        viewer !== undefined &&
        child.author.id === viewer;
      if (!child.transcluded && (child.type === "ask" || begunByReader)) {
        const children: ContentsEntry[] = [];
        walk(child, children);
        into.push({ children, node: child });
      } else {
        walk(child, into);
      }
    }
  };
  const below: ContentsEntry[] = [];
  walk(root, below);
  return [{ children: [], node: root }, ...below];
}

/** The pixels a row of the list spends beside its words, from the prototype's `.mrow`: its
 *  side margins and right padding, the 6 px it starts at and 16 px a level, the twisty's 12,
 *  the bullet's 7, and the two 7 px gaps between them. */
const ROW = { gap: 7, indent: 16, inset: 6, margins: 12, paddingRight: 6, twisty: 12, dot: 7 };

/** The row's words are 14 px: 0.875 rem. */
const ROW_TEXT_REM = 0.875;

/**
 * The list itself: its heading with the Focus branch toggle, and one row per entry, each its
 * node's first line by R1's rule, cut at a word to the row's width. It is one tab stop, as the
 * outline is: the arrow keys move inside it, the right and left arrows unfold and fold an entry,
 * and Enter or a press goes to that node in the outline. The entry the reader is in - the one
 * holding the node the focus is on, or last was on - is marked by a rule and a ground of its own.
 */
export function ContentsList({
  focusBranch,
  onFocusBranch,
  onGo,
  root,
}: {
  /** Whether the outline draws one branch at a time, the current one. */
  focusBranch: boolean;
  /** Turns Focus branch on or off, around the entry the reader is in. */
  onFocusBranch: (current: string | undefined) => void;
  /** Goes to the node in the outline. */
  onGo: (id: string) => void;
  /** The tree the outline draws. */
  root: TreeNode;
}): ReactNode {
  const viewer = useQuery(whoAmIQuery()).data?.login;
  const entries = useMemo(() => contentsOf(root, viewer), [root, viewer]);
  const entryIds = useMemo(() => {
    const ids = new Set<string>();
    const walk = (list: readonly ContentsEntry[]) => {
      for (const entry of list) {
        ids.add(entry.node.id);
        walk(entry.children);
      }
    };
    walk(entries);
    return ids;
  }, [entries]);
  // The entry the reader is in: the nearest entry at or above the node the focus is on. The
  // focus leaving the outline - for this list, say - leaves the entry the reader was last in.
  const focused = useFocusedNode()?.id;
  const [lastIn, setLastIn] = useState<string | undefined>(undefined);
  const inEntry = useMemo(() => {
    if (focused === undefined) return undefined;
    return (pathTo(root, focused) ?? [])
      .map((node) => node.id)
      .filter((id) => entryIds.has(id))
      .at(-1);
  }, [entryIds, focused, root]);
  useEffect(() => {
    if (inEntry !== undefined) setLastIn(inEntry);
  }, [inEntry]);
  const current = inEntry ?? (lastIn !== undefined && entryIds.has(lastIn) ? lastIn : undefined);

  const [folded, setFolded] = useState<ReadonlySet<string>>(() => new Set());
  const rows: { depth: number; entry: ContentsEntry; guides: number }[] = [];
  const walk = (list: readonly ContentsEntry[], depth: number) => {
    for (const entry of list) {
      rows.push({ depth, entry, guides: depth });
      if (!folded.has(entry.node.id)) walk(entry.children, depth + 1);
    }
  };
  walk(entries, 0);
  const fold = (id: string, shut: boolean) =>
    setFolded((held) => {
      const next = new Set(held);
      if (shut) next.add(id);
      else next.delete(id);
      return next;
    });

  const list = useRef<HTMLDivElement | null>(null);
  const box = useOneLineBox(list, true);
  // The list's one tab stop: the row the reader last moved to, else the entry they are in,
  // else the first.
  const [stop, setStop] = useState<string | undefined>(undefined);
  const ids = rows.map((row) => row.entry.node.id);
  const tabStop =
    stop !== undefined && ids.includes(stop) ? stop : (current ?? ids[0] ?? undefined);
  const moveTo = (id: string | undefined) => {
    if (id === undefined) return;
    setStop(id);
    list.current?.querySelector<HTMLElement>(`[data-contents-entry="${CSS.escape(id)}"]`)?.focus();
  };
  const onKeyDown = (event: KeyboardEvent<HTMLElement>, at: number) => {
    const row = rows[at];
    if (row === undefined) return;
    const id = row.entry.node.id;
    const holds = row.entry.children.length > 0;
    const keys: Record<string, () => void> = {
      ArrowDown: () => moveTo(ids[at + 1]),
      ArrowLeft: () => {
        if (holds && !folded.has(id)) fold(id, true);
        else
          moveTo(
            rows
              .slice(0, at)
              .filter((above) => above.depth < row.depth)
              .at(-1)?.entry.node.id
          );
      },
      ArrowRight: () => {
        if (holds && folded.has(id)) fold(id, false);
        else if (holds) moveTo(ids[at + 1]);
      },
      ArrowUp: () => moveTo(ids[at - 1]),
      End: () => moveTo(ids.at(-1)),
      Enter: () => onGo(id),
      Home: () => moveTo(ids[0]),
    };
    const run = keys[event.key];
    if (run === undefined) return;
    event.preventDefault();
    event.stopPropagation();
    run();
  };

  return (
    <>
      <div className="flex shrink-0 items-center justify-between gap-2 pt-2.5 pr-2.5 pb-0.5 pl-3.5">
        <h2
          className={`text-xs leading-4 font-semibold tracking-[0.08em] uppercase ${pageQuietText}`}
          {...pageChrome}
        >
          {CONTROL_LABELS.contents}
        </h2>
        <button
          aria-pressed={focusBranch}
          className={`inline-flex items-center gap-[7px] rounded-full border py-px pr-2.5 pl-1 text-xs leading-5 font-medium whitespace-nowrap ${pagePaperBg} ${
            focusBranch ? `${pageInkBorder} ${pageInkText}` : `${pageHairBorder} ${pageMutedText}`
          } ${focusVisibleRing}`}
          onClick={() => onFocusBranch(current)}
          type="button"
          {...pageChrome}
        >
          <span
            aria-hidden="true"
            className={`relative h-4 w-[26px] shrink-0 rounded-lg ${focusBranch ? pageInkBg : pageHairBg}`}
          >
            <span
              className={`absolute top-0.5 size-3 rounded-full ${pagePaperBg} ${
                focusBranch ? "left-3" : "left-0.5"
              }`}
            />
          </span>
          {CONTROL_LABELS.focus_branch}
        </button>
      </div>
      <div
        aria-label={CONTROL_LABELS.contents}
        className="min-h-0 flex-1 overflow-x-hidden overflow-y-auto pb-2"
        ref={list}
        role="tree"
      >
        {rows.map(({ depth, entry, guides }, at) => {
          const { node } = entry;
          const holds = entry.children.length > 0;
          const isCurrent = node.id === current;
          const words = nodeWords(node);
          const indent = ROW.inset + depth * ROW.indent;
          const cut =
            box === undefined
              ? undefined
              : oneLineCut({
                  after: 0,
                  before: indent + ROW.twisty + ROW.gap + ROW.dot + ROW.gap,
                  room: box.width - ROW.margins - ROW.paddingRight,
                  text: words,
                  widthOf: (text) => box.widthOf(text, ROW_TEXT_REM, isCurrent ? 600 : undefined),
                });
          const line = cut ?? { cut: false, line: words, under: false, wraps: false };
          return (
            <div
              aria-expanded={holds ? !folded.has(node.id) : undefined}
              aria-level={depth + 1}
              aria-selected={isCurrent}
              className={`relative mx-1.5 flex h-8 cursor-pointer items-center gap-[7px] rounded-md pr-1.5 text-sm leading-8 whitespace-nowrap select-none ${focusVisibleRing} ${
                isCurrent
                  ? `font-semibold shadow-[inset_2px_0_0_var(--color-page-ink)] ${pageMarkBg} ${pageInkText}`
                  : pageInkText
              }`}
              data-contents-entry={node.id}
              key={node.id}
              onClick={() => {
                setStop(node.id);
                onGo(node.id);
              }}
              onKeyDown={(event) => onKeyDown(event, at)}
              role="treeitem"
              style={{ paddingLeft: `${indent}px` }}
              tabIndex={node.id === tabStop ? 0 : -1}
            >
              {Array.from({ length: guides }, (_, level) => (
                <i
                  aria-hidden="true"
                  className={`absolute inset-y-0 w-px ${pageGuideBg}`}
                  // biome-ignore lint/suspicious/noArrayIndexKey: a guide is its level and nothing else.
                  key={level}
                  style={{ left: `${ROW.inset + 2 + level * ROW.indent + 6}px` }}
                />
              ))}
              {holds ? (
                <button
                  aria-label={folded.has(node.id) ? CONTROL_LABELS.unfold : CONTROL_LABELS.fold}
                  className={`h-8 w-3 shrink-0 text-center text-xs ${pageQuietText}`}
                  onClick={(event) => {
                    event.stopPropagation();
                    fold(node.id, !folded.has(node.id));
                  }}
                  tabIndex={-1}
                  type="button"
                >
                  <span aria-hidden="true">{folded.has(node.id) ? "▸" : "▾"}</span>
                </button>
              ) : (
                <span aria-hidden="true" className="w-3 shrink-0" />
              )}
              <span
                aria-hidden="true"
                className={`inline-block size-[7px] shrink-0 rounded-full ${
                  node.type === "issue"
                    ? pageIssueBg
                    : node.type === "ask"
                      ? pageQuestionBg
                      : pagePersonBg
                }`}
              />
              <span className="sr-only" {...pageChrome}>
                {sentenceCase(NODE_TYPE_NAMES[node.type])}
              </span>
              <span
                className={`min-w-0 flex-1 ${line.under || line.wraps ? "whitespace-normal break-words" : ""}`}
                data-contents-line=""
                {...authorText}
              >
                {line.cut ? `${line.line}…` : line.line}
              </span>
            </div>
          );
        })}
      </div>
    </>
  );
}
