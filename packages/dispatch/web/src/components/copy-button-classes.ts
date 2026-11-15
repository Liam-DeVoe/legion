/**
 * The box of the icon-only `CopyButton`, in one place, because two surfaces draw it: the button
 * itself, and the space the issue page keeps beside the issue's key while the header is still to
 * come (`features/issue/issue-page-skeleton.tsx`, dispatch://INFRA-341). The space stands at the
 * header's own height only while it keeps this box at the button's own size: it is the key row's
 * height below `md`, and with the key badge it is where the title starts from `sm` up. A copy of
 * these classes in the space would part from the button the first time one of them changed.
 *
 * Data only, as `tab-classes.ts` is, so the shell's own chunk, which draws that space, takes
 * nothing of the button with it.
 */

/** The icon-only button's box: a 44 px tap target, 32 px from `md`, the icon centred in it. */
export const copyIconBoxClassName =
  "inline-flex min-h-11 min-w-11 shrink-0 items-center justify-center rounded-lg md:min-h-8 md:min-w-8";
