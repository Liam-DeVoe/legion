import type { ButtonHTMLAttributes, ReactNode, Ref } from "react";

import {
  borderDefault,
  buttonDanger,
  buttonGhost,
  buttonGhostMuted,
  buttonPressed,
  buttonPrimary,
  buttonSecondary,
  surfaceBg,
} from "../theme/classes";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "ghost-muted" | "danger";
export type ButtonSize = "md" | "sm";

const VARIANTS: Record<ButtonVariant, string> = {
  danger: buttonDanger,
  ghost: buttonGhost,
  "ghost-muted": buttonGhostMuted,
  primary: buttonPrimary,
  secondary: buttonSecondary,
};

/** Height, padding and type per size: 32 px and 14 px text by default, 28 px and 13 px text for
 *  a compact row or toolbar. 12 px is for metadata (badges, counts, hints), never a control.
 *  Below 1280 px `styles.css` lifts every control to a 44 px target, whatever is written here. */
const SIZES: Record<ButtonSize, string> = {
  md: "h-8 gap-1.5 px-3 text-sm",
  sm: "h-7 gap-1 px-2.5 text-[13px]",
};

/** The square form of each size, for a button that holds one icon and no words. */
const ICON_SIZES: Record<ButtonSize, string> = {
  md: "size-8",
  sm: "size-7",
};

/** A compact button draws at 28 px but takes a press over 32 px: an invisible `::after` reaches
 *  2 px above and below it, so a compact control is never a smaller target than a default one. */
const HIT_AREA: Record<ButtonSize, string> = {
  md: "",
  sm: "relative after:absolute after:inset-x-0 after:-inset-y-0.5 after:content-['']",
};

const SHAPE =
  "inline-flex shrink-0 items-center justify-center rounded-md font-medium whitespace-nowrap select-none";

export interface ButtonLook {
  /** A square button holding one icon. It needs an `aria-label`. */
  iconOnly?: boolean;
  size?: ButtonSize;
  variant?: ButtonVariant;
}

/**
 * The button's whole class string, for an element that must look like a button without being
 * `Button`: a router `Link` that is a call to action, a `summary`, a `label` over a file input.
 * A `<button>` uses `Button`.
 */
export function buttonClassName({
  iconOnly = false,
  size = "md",
  variant = "secondary",
}: ButtonLook = {}): string {
  return [
    SHAPE,
    iconOnly ? ICON_SIZES[size] : SIZES[size],
    HIT_AREA[size],
    VARIANTS[variant],
    buttonPressed,
  ]
    .filter(Boolean)
    .join(" ");
}

/**
 * The box of a view switch (List | Board, Timeline | List, All | Open | Closed): one bordered
 * 32 px control holding `ghost` `sm` toggle Buttons, the chosen one filled by `aria-pressed`.
 * Put it on the `fieldset` that names the group.
 */
export const segmentedClassName = `inline-flex shrink-0 items-center gap-0.5 rounded-md border p-px ${borderDefault} ${surfaceBg}`;

export interface ButtonProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "className">,
    ButtonLook {
  children?: ReactNode;
  /** Placement only (margin, `w-full`, grid position, `hidden` at a breakpoint). The look is
   *  the variant's: never a colour, a height, a radius or a padding. */
  className?: string;
  /** A press is in flight: the button is disabled, says so (`aria-busy`) and draws a spinner
   *  before its label. Change the label too where the wait is long ("Saving…"). */
  loading?: boolean;
  ref?: Ref<HTMLButtonElement>;
}

/**
 * The one button (`DESIGN.md`). `primary` is the filled accent, one per view; `secondary` the
 * default; `ghost` for an action inside a row or a toolbar; `danger` for what destroys.
 * `type` defaults to `button`, so a button inside a form submits only when it says `submit`.
 * A toggle passes `aria-pressed` and takes the pressed fill from it.
 */
export function Button({
  children,
  className,
  disabled,
  iconOnly = false,
  loading = false,
  size = "md",
  type = "button",
  variant = "secondary",
  ...rest
}: ButtonProps): ReactNode {
  return (
    <button
      aria-busy={loading || undefined}
      className={`${buttonClassName({ iconOnly, size, variant })} ${className ?? ""}`}
      disabled={disabled === true || loading}
      type={type}
      {...rest}
    >
      {loading ? <Spinner /> : null}
      {loading && iconOnly ? null : children}
    </button>
  );
}

/** The mark of a press in flight: a ring in the button's own text colour. */
function Spinner(): ReactNode {
  return (
    <svg
      aria-hidden="true"
      className="size-3.5 shrink-0 animate-spin motion-reduce:animate-none"
      fill="none"
      viewBox="0 0 16 16"
    >
      <circle cx="8" cy="8" opacity="0.25" r="6" stroke="currentColor" strokeWidth="2" />
      <path d="M14 8a6 6 0 0 0-6-6" stroke="currentColor" strokeLinecap="round" strokeWidth="2" />
    </svg>
  );
}
