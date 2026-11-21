import type { ReactNode } from "react";

/**
 * The mark of a control that folds what it holds: ▾ while it is open, ▸ while it is folded,
 * drawn one em square so it takes the size of the control's text, in its colour. It is a shape
 * and not text, as `IssuePresets.tsx`'s `MenuMark` is, because the page's typeface lacks both
 * characters (`page-typeface.test.ts`): as text, each is drawn in a face of the reader's own
 * machine, at that face's width. The control's own name or `aria-expanded` says whether it is
 * open, so the shape is hidden from assistive technology; `data-fold-mark` says which it draws.
 */
export function FoldMark({ open }: { readonly open: boolean }): ReactNode {
  return (
    <svg
      aria-hidden="true"
      className="inline-block size-[1em] shrink-0 align-middle"
      data-fold-mark={open ? "open" : "folded"}
      focusable="false"
      viewBox="0 0 16 16"
    >
      <path d={open ? "M4.5 6.5h7L8 10.5z" : "M6.5 4.5v7l4-3.5z"} fill="currentColor" />
    </svg>
  );
}
