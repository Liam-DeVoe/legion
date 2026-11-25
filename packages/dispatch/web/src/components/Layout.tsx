import type { HTMLAttributes, ReactNode } from "react";

import { card, textMutedOnCanvas } from "../theme/classes";

/**
 * A page's head: its title at 22 px, one line under it saying what the page is, and its actions
 * at the right. `eyebrow` is the line above the title (a back link, a breadcrumb). The 24 px
 * under it is the gap to the page's first section, the same on every page.
 */
export function PageHeader({
  actions,
  description,
  eyebrow,
  title,
}: {
  actions?: ReactNode;
  description?: ReactNode;
  eyebrow?: ReactNode;
  title: ReactNode;
}): ReactNode {
  return (
    <header className="mb-6">
      {eyebrow === undefined ? null : <div className="mb-1 text-sm">{eyebrow}</div>}
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <h1 className="min-w-0 text-[22px] leading-8 font-semibold tracking-tight">{title}</h1>
        {actions === undefined ? null : (
          <div className="flex min-h-8 max-w-full min-w-0 flex-wrap items-center gap-2">
            {actions}
          </div>
        )}
      </div>
      {description === undefined ? null : (
        <p className={`mt-1 max-w-3xl text-sm ${textMutedOnCanvas}`}>{description}</p>
      )}
    </header>
  );
}

/**
 * A section's head inside a page: its heading at 15 px, an optional line under it and an
 * optional action at the right. 12 px separates it from the section's content; sections
 * themselves are 32 px apart (`space-y-8` on their container).
 */
export function SectionHeader({
  action,
  description,
  id,
  level = 2,
  title,
}: {
  action?: ReactNode;
  description?: ReactNode;
  /** The heading's id, for the section's `aria-labelledby`. */
  id?: string;
  level?: 2 | 3;
  title: ReactNode;
}): ReactNode {
  const Heading = level === 2 ? "h2" : "h3";
  return (
    <div className="mb-3">
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <Heading className="min-w-0 text-[15px] leading-6 font-semibold" id={id}>
          {title}
        </Heading>
        {action === undefined ? null : (
          <div className="flex max-w-full min-w-0 flex-wrap items-center gap-2">{action}</div>
        )}
      </div>
      {description === undefined ? null : (
        <p className={`mt-1 max-w-3xl text-sm ${textMutedOnCanvas}`}>{description}</p>
      )}
    </div>
  );
}

const CARD_PADDING = {
  md: "p-4",
  none: "",
  sm: "p-3",
} as const;

/** The card's class string, for a host that must be another element (a `form`, an `li`, a
 *  link). */
export function cardClassName(padding: keyof typeof CARD_PADDING = "md"): string {
  return `rounded-lg border ${card} ${CARD_PADDING[padding]}`;
}

/**
 * A card: the surface inside a 1px rule, 8 px corners, 16 px of padding. `padding="none"` is
 * for a card that holds a table or a list of rows that run to its edges.
 */
export function Card({
  children,
  className,
  padding = "md",
  ...rest
}: Omit<HTMLAttributes<HTMLDivElement>, "className"> & {
  children: ReactNode;
  /** Placement only. */
  className?: string;
  padding?: keyof typeof CARD_PADDING;
}): ReactNode {
  return (
    <div className={`${cardClassName(padding)} ${className ?? ""}`} {...rest}>
      {children}
    </div>
  );
}
