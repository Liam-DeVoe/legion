import type {
  InputHTMLAttributes,
  ReactNode,
  Ref,
  SelectHTMLAttributes,
  TextareaHTMLAttributes,
} from "react";

import {
  dangerText,
  fieldControl,
  fieldInvalid,
  textMutedOnCanvas,
  textSecondaryOnCanvas,
} from "../theme/classes";

/** `lg` is for a page's primary search box, the one control a page is used through. */
export type FieldSize = "lg" | "md" | "sm";

/** Where the field sits: on the page (`canvas`, the default), or inside a card (`recessed`),
 *  whose dark fill must read one level below the card's own. */
export type FieldSurface = keyof typeof fieldControl;

export interface FieldLook {
  /** Monospace, for a value that is an identifier: a repository, a path, a job name. */
  mono?: boolean;
  size?: FieldSize;
  surface?: FieldSurface;
}

const SIZES: Record<FieldSize, string> = {
  lg: "h-10 px-3 text-sm",
  md: "h-8 px-2.5 text-sm",
  sm: "h-7 px-2 text-[13px]",
};

/** The textarea's own sizes: it has no fixed height, only the same inset and type. */
const AREA_SIZES: Record<FieldSize, string> = {
  lg: "min-h-16 px-3 py-2 text-sm",
  md: "min-h-16 px-2.5 py-1.5 text-sm",
  sm: "min-h-14 px-2 py-1 text-[13px]",
};

function look(sizes: Record<FieldSize, string>, { mono, size, surface }: FieldLook): string {
  return `block min-w-0 rounded-md border ${sizes[size ?? "md"]} ${mono === true ? "font-mono" : ""} ${fieldControl[surface ?? "canvas"]} ${fieldInvalid}`;
}

/** The field fills its container unless the caller's placement names a width of its own: two
 *  width utilities on one element are settled by the stylesheet's order, not the attribute's. */
function widthOf(className: string | undefined): string {
  return className !== undefined && /(^|\s)w-/.test(className)
    ? className
    : `w-full ${className ?? ""}`;
}

/** An input's whole class string, for a host that cannot use `Input` (a library's own input).
 *  It fills its container. */
export function inputClassName(options: FieldLook = {}): string {
  return `${look(SIZES, options)} w-full`;
}

/** A native select's class string: the input's, with the native arrow replaced by `Select`'s
 *  own chevron, so it needs room at its trailing edge. */
export function selectClassName(options: FieldLook = {}): string {
  return `${look(SIZES, options)} w-full appearance-none truncate pr-7`;
}

export function textareaClassName(options: FieldLook = {}): string {
  return `${look(AREA_SIZES, options)} w-full`;
}

type Placement = {
  /** Placement only (a width, a margin, a grid position). The field is `w-full` unless this
   *  names a width (`w-48`). */
  className?: string;
};

export function Input({
  className,
  mono,
  ref,
  size,
  surface,
  ...rest
}: Omit<InputHTMLAttributes<HTMLInputElement>, "className" | "size"> &
  FieldLook &
  Placement & { ref?: Ref<HTMLInputElement> }): ReactNode {
  return (
    <input
      className={`${look(SIZES, { mono, size, surface })} ${widthOf(className)}`}
      ref={ref}
      {...rest}
    />
  );
}

export function Textarea({
  className,
  mono,
  ref,
  size,
  surface,
  ...rest
}: Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, "className"> &
  FieldLook &
  Placement & { ref?: Ref<HTMLTextAreaElement> }): ReactNode {
  return (
    <textarea
      className={`${look(AREA_SIZES, { mono, size, surface })} ${widthOf(className)}`}
      ref={ref}
      {...rest}
    />
  );
}

/**
 * A native `<select>` in the field's shape, with one chevron of its own in place of the
 * platform's arrow. `className` places the wrapper (its width); the select fills it.
 */
export function Select({
  children,
  className,
  mono,
  ref,
  size,
  surface,
  ...rest
}: Omit<SelectHTMLAttributes<HTMLSelectElement>, "className" | "size"> &
  FieldLook &
  Placement & { ref?: Ref<HTMLSelectElement> }): ReactNode {
  return (
    <span className={`relative inline-flex min-w-0 ${className ?? "w-full"}`}>
      <select className={selectClassName({ mono, size, surface })} ref={ref} {...rest}>
        {children}
      </select>
      <svg
        aria-hidden="true"
        className={`pointer-events-none absolute top-1/2 right-2 size-4 -translate-y-1/2 ${textMutedOnCanvas}`}
        fill="none"
        viewBox="0 0 16 16"
      >
        <path
          d="M4.5 6.5 8 10l3.5-3.5"
          stroke="currentColor"
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth="1.5"
        />
      </svg>
    </span>
  );
}

/** A field's label as a class string, for a host whose label is not `Field`'s (a `legend`). */
export const fieldLabelClassName = `block text-[13px] font-medium ${textSecondaryOnCanvas}`;
/** The line under a field that says what it is for. */
export const fieldHintClassName = `text-xs ${textMutedOnCanvas}`;

/**
 * One labelled field: the label above its control, a hint under it, and the error in the hint's
 * place. The label names the control that carries the id `htmlFor` (`useId()` gives one). `hint`
 * and `error` are plain lines: a control that should be described by one passes its own
 * `aria-describedby` and the same id as `hintId`.
 */
export function Field({
  children,
  className,
  error,
  hint,
  hintId,
  htmlFor,
  label,
}: {
  children: ReactNode;
  className?: string;
  error?: ReactNode;
  hint?: ReactNode;
  hintId?: string;
  htmlFor: string;
  label: ReactNode;
}): ReactNode {
  const below =
    error !== undefined && error !== null && error !== false ? (
      <p className={`text-xs ${dangerText}`} id={hintId} role="alert">
        {error}
      </p>
    ) : hint === undefined ? null : (
      <p className={fieldHintClassName} id={hintId}>
        {hint}
      </p>
    );
  return (
    <div className={`flex min-w-0 flex-col gap-1.5 ${className ?? ""}`}>
      <label className={fieldLabelClassName} htmlFor={htmlFor}>
        {label}
      </label>
      {children}
      {below}
    </div>
  );
}
