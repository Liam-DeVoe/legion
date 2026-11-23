import { type ReactNode, useId } from "react";

import type { DisplayTree, TextRewrite } from "../../api/types";
import { useTooltips, withoutEllipsis } from "../../components/tooltips";
import {
  badgeBlocking,
  badgeBlockingHoverBg,
  badgeLow,
  badgePrimary,
  badgePrimaryEdge,
  badgePrimaryHoverBg,
  badgePrimaryOutline,
  borderTransparent,
  groupFocusVisibleRing,
  pageFailedText,
  pageHairBorder,
  pageLabelChip,
  pagePaperBg,
} from "../../theme/classes";
import {
  type OriginalToggleProps,
  originalToggleProps,
  originalWording,
  toggleOriginal,
  useOriginalShown,
} from "./ask-rewrite";

/** Every state is the same box: a 1 px border, clear unless the state draws it (Original). */
const PILL =
  "inline-flex shrink-0 items-center rounded-full border px-2 py-px text-xs leading-4 font-medium whitespace-nowrap";
/** A pressable state's button is the pill's hit area: below 1280 px every button is a 44 px tap
 *  target (`styles.css`), and the pill inside keeps its own size; the focus ring is the pill's. */
const HIT = "group inline-flex shrink-0 items-center justify-center outline-none";
/** The words of the badge while a rewrite runs. */
const REWRITING = "Rewriting…";

/**
 * Each colour the badge draws inside `NoTooltips`, the new issue page's, by the role its colour
 * has on today's pages: Rewriting… is the page's label chip, the muted words on the pane;
 * Rewritten the questions' blue on its tint, its edge the blue under the pointer; Original the
 * same blue as an outline on the paper, the tint under the pointer; and Rewrite failed the
 * failed red on the paper inside the hairline, its edge the red under the pointer. The box keeps
 * its 1 px border in every state, drawn in a colour of the page's where today's is clear.
 */
const ON_PAGE = {
  failed: `${pageHairBorder} ${pagePaperBg} ${pageFailedText} hover:border-page-failed`,
  original: "border-page-question bg-page-paper text-page-question hover:bg-page-question-tint",
  rewriting: pageLabelChip,
  rewritten:
    "border-page-question-tint bg-page-question-tint text-page-question hover:border-page-question",
} as const;

/**
 * The one mark a rewrite leaves on an agent's text - an ask's card, a comment, a message, an
 * issue's title - beside who wrote it and when: **Rewriting…** while a person's Rewrite runs,
 * **Rewritten** once a rewrite is in place, whoever wrote it (a person's Rewrite, or the reviewer
 * before the reader saw the text), and **Rewrite failed** when Dispatch did not apply one.
 * Rewritten shows the original wording in place and reads **Original** until it is pressed again
 * (`toggleKey`, where the surface has one); Rewrite failed tries again (`onRetry`, and `r`), and
 * why it failed is its tooltip alone. `onRetry` is absent where nothing can be rewritten again
 * from the page. A state this release does not know draws nothing.
 *
 * Inside `NoTooltips` the badge draws no `title`: what its tooltip says - why a rewrite failed and
 * that a press tries again, or what a press shows - is its description, read after its name, as a
 * refused `RefusableButton` says its reason, so the words on the badge stay the state alone.
 * Rewriting… is named by its words, and every colour is the page's (`ON_PAGE`).
 */
export function RewriteBadge({
  onRetry,
  onToggle,
  rewrite,
  running = false,
  showingOriginal,
  toggleKey,
}: {
  onRetry?: () => void;
  onToggle: () => void;
  rewrite: Pick<TextRewrite, "reason" | "state"> | undefined;
  /** The reader's own rewrite is out, before the read says so. */
  running?: boolean;
  showingOriginal: boolean;
  /** The key that shows or hides the original on this surface, which the tooltip names. */
  toggleKey?: string;
}): ReactNode {
  const tooltips = useTooltips();
  const descriptionId = useId();
  // What the tooltip says: on a `title` where tooltips are drawn, else the badge's description.
  const told = (words: string) =>
    tooltips
      ? { description: null, props: { title: words } }
      : {
          description: (
            <span className="sr-only" id={descriptionId}>
              {words}
            </span>
          ),
          props: { "aria-describedby": descriptionId },
        };
  if (running || rewrite?.state === "rewriting") {
    return (
      <span
        aria-label={tooltips ? undefined : withoutEllipsis(REWRITING)}
        className={`${PILL} animate-pulse ${tooltips ? `${borderTransparent} ${badgeLow.bg} ${badgeLow.text}` : ON_PAGE.rewriting}`}
        data-ask-rewrite="rewriting"
        role="status"
      >
        {REWRITING}
      </span>
    );
  }
  if (rewrite?.state === "failed") {
    const reason = rewrite.reason ?? "Dispatch did not apply the rewrite.";
    const tip = told(onRetry === undefined ? reason : `${reason} Try again (r)`);
    return (
      <>
        <button
          {...tip.props}
          className={HIT}
          data-ask-rewrite="failed"
          onClick={onRetry}
          type="button"
        >
          <span
            className={`${PILL} ${groupFocusVisibleRing} ${tooltips ? `${borderTransparent} ${badgeBlocking.bg} ${badgeBlocking.text} ${badgeBlockingHoverBg}` : ON_PAGE.failed}`}
          >
            Rewrite failed
          </span>
        </button>
        {tip.description}
      </>
    );
  }
  if (rewrite?.state !== "rewritten") return null;
  const hint = toggleKey === undefined ? "" : ` (${toggleKey})`;
  const tone = tooltips
    ? showingOriginal
      ? badgePrimaryOutline
      : `${badgePrimaryEdge} ${badgePrimary.bg} ${badgePrimary.text} ${badgePrimaryHoverBg}`
    : showingOriginal
      ? ON_PAGE.original
      : ON_PAGE.rewritten;
  const tip = told(
    showingOriginal ? `Show the rewrite${hint}` : `Show the original wording${hint}`
  );
  return (
    <>
      <button
        {...tip.props}
        aria-pressed={showingOriginal}
        className={HIT}
        data-ask-rewrite={showingOriginal ? "original" : "rewritten"}
        onClick={onToggle}
        type="button"
      >
        <span className={`${PILL} ${groupFocusVisibleRing} ${tone}`}>
          {showingOriginal ? "Original" : "Rewritten"}
        </span>
      </button>
      {tip.description}
    </>
  );
}

/** A comment or a message as a read carries it: its words, the reading Go sent of them, and the
 *  reviewer's rewrite of it where it held the text and posted it rewritten. */
export interface RewritableText {
  readonly id: string;
  readonly body: string;
  readonly body_tree?: DisplayTree;
  readonly rewrite?: TextRewrite;
}

/** What a page draws of a comment or a message the reviewer may have rewritten
 *  (`useRewrittenText`). */
export interface RewrittenText {
  /** The badge, beside the text's author and time; nothing where no rewrite touched the text. */
  readonly badge: ReactNode;
  /** The Markdown the text shows: the agent's own wording while the badge shows it, else the
   *  body. */
  readonly markdown: string;
  /** The reading Go sent of the body, which the agent's wording has none of: `MarkdownBody` reads
   *  that wording itself. */
  readonly tree: DisplayTree | undefined;
  /** What the element holding the text spreads, so that `w` while it holds the focus does what a
   *  press on the badge does (`originalToggleProps`). */
  readonly toggle: OriginalToggleProps;
}

/**
 * A reply or a message the reviewer rewrote before the reader saw it (INFRA-3231's R7), as every
 * page draws one: its badge, with `w` as its key, shows the agent's wording in place, and `w`
 * while the text's own element holds the focus does the same. Every part of a page that draws
 * the text swaps together (`useOriginalShown`).
 */
export function useRewrittenText(text: RewritableText): RewrittenText {
  const original = originalWording(text.rewrite, useOriginalShown(text.id));
  return {
    badge: (
      <RewriteBadge
        onToggle={() => toggleOriginal(text.id)}
        rewrite={text.rewrite}
        showingOriginal={original !== undefined}
        toggleKey="w"
      />
    ),
    markdown: original?.body ?? text.body,
    toggle: originalToggleProps(text.id, text.rewrite?.state === "rewritten"),
    tree: original === undefined ? text.body_tree : undefined,
  };
}
