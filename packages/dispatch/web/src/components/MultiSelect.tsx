import { type KeyboardEvent, type ReactNode, useEffect, useId, useRef, useState } from "react";

import {
  borderDefault,
  borderStrong,
  card,
  cardHoverBorder,
  dangerText,
  inputClasses,
  selectedCardBg,
  selectedCardBorder,
  surfaceMutedBg,
  textMutedOnSurface,
  textSecondaryOnCanvas,
  textSecondaryOnSurface,
} from "../theme/classes";
import { AnchoredPanel } from "./AnchoredPanel";
import { type ControlName, type ControlSize, controlPadding } from "./control-names";
import { TruncatedText } from "./TruncatedText";

function fold(value: string | undefined): string {
  return (value ?? "").trim().toLocaleLowerCase();
}

interface SharedProps {
  /** Trigger content. A multi-select's defaults to the label with the selection count
   *  (`Labels · 2`), a single pick's to the picked value's row text, else to the label. */
  readonly children?: ReactNode;
  readonly disabled?: boolean;
  /** Which control of the vocabulary this is, written into the trigger's `data-control`: a
   *  column filter is a multi-select under its own name, while a multi-select a page draws for
   *  itself is a part of the vocabulary rather than one of its controls, and carries nothing. */
  readonly control?: ControlName;
  /** The trigger is busy with what the last close committed: it stays focusable, and says so
   *  rather than looking actionable and doing nothing. */
  readonly ariaDisabled?: boolean;
  /** Shown under the list, e.g. a host's validation message for a rejected `onCreate`. */
  readonly error?: string | null;
  /** Shown in place of the list when nothing is left to choose or nothing matches the search. */
  readonly emptyMessage: string;
  /** The concept the control picks from: the default trigger text, `Clear <label>`, the
   *  listbox's accessible name, and a single pick's trigger's accessible name. */
  readonly label: string;
  readonly onOpenChange: (open: boolean) => void;
  /** Every keystroke in the search input, so a host can drop a message it showed for the
   *  previous query (a rejected create). */
  readonly onQueryChange?: (query: string) => void;
  readonly open: boolean;
  /** The values the host stores (URL keys, label strings) in list order. */
  readonly options: readonly string[];
  /** Row text for a value; the search matches this text, and a single pick's trigger shows it
   *  for the picked value. Defaults to the value itself. */
  readonly optionLabel?: (value: string) => string;
  /** Muted text at the end of a value's row, such as how many items carry it, or `undefined`
   *  for a row with no detail at all. The row's accessible name reads it after the label and a
   *  comma ("api, 3"), never run together. */
  readonly optionDetail?: (value: string) => string | undefined;
  /** What the row's accessible name says in place of `optionDetail`'s text, such as "3 PRs" for
   *  a bare count. Defaults to that text. */
  readonly optionDetailLabel?: (value: string) => string;
  readonly searchLabel: string;
  /** How much room the default trigger takes; a `triggerClassName` decides for itself. */
  readonly size?: ControlSize;
  /** The trigger's accessible name in place of its text. A single pick's defaults to `label`,
   *  so its trigger is named for the list whatever value it shows. */
  readonly triggerAriaLabel?: string;
  readonly triggerClassName?: string;
}

/** Any number of values: a row ticks or unticks its value, and a `Clear` foot empties them. */
interface MultipleProps extends SharedProps {
  readonly mode?: "multiple";
  readonly onChange: (selected: string[]) => void;
  /** Enables the `Create "<query>"` row for a search that matches no option. */
  readonly onCreate?: (value: string) => void;
  readonly selected: readonly string[];
}

/** One value: a pick hands it to `onPick`, closes the list and puts the focus on the trigger. */
interface SingleProps extends SharedProps {
  readonly mode: "single";
  readonly onPick: (value: string) => void;
  /** The value picked now, which the list marks selected and the trigger shows; absent while
   *  the host holds none. */
  readonly value?: string;
}

export type MultiSelectProps = MultipleProps | SingleProps;

/**
 * The one searchable list a reader picks from: a trigger button and a portalled popover holding
 * a `combobox` search input that narrows the listbox under it. Arrow keys move the active option
 * and Enter takes it; Escape and Tab close with nothing taken, an outside pointer press closes
 * too, and closing returns focus to the trigger. The host owns `open`, so it can stage a draft on
 * open and commit on close.
 *
 * By default it picks many: the listbox is `aria-multiselectable`, a row (or Enter) ticks or
 * unticks its value in the checkbox drawn beside it, `onCreate` adds a row for a search that
 * matches no option, and a `Clear` foot empties the selection. `mode="single"` picks one: the
 * rows are plain options, the picked one `aria-selected` and marked, and a pick hands its one
 * value to `onPick`, closes the list and returns the focus to the trigger. Its trigger is named
 * for the list (`label`) whatever it shows, and while a value is picked the trigger's text is its
 * description, so a reader hears the list and the value staged in it, as a `<select>` reads.
 */
export function MultiSelect(props: MultiSelectProps): ReactNode {
  const {
    ariaDisabled = false,
    children,
    control,
    disabled = false,
    emptyMessage,
    error = null,
    label,
    onOpenChange,
    onQueryChange,
    open,
    options,
    optionDetail,
    optionDetailLabel,
    optionLabel = (value: string) => value,
    searchLabel,
    size = "comfortable",
    triggerAriaLabel,
    triggerClassName = `rounded-lg border text-sm font-semibold ${controlPadding[size]} ${borderDefault} ${surfaceMutedBg} ${textSecondaryOnCanvas}`,
  } = props;
  const single = props.mode === "single";
  // What is picked now, a single pick's value as a list of one, so both modes read it alike.
  const selected =
    props.mode !== "single" ? props.selected : props.value === undefined ? [] : [props.value];
  const onCreate = props.mode === "single" ? undefined : props.onCreate;
  const id = useId();
  const listboxId = `${id}-options`;
  const pickedId = `${id}-picked`;
  const optionId = (value: string) => `${id}-option-${encodeURIComponent(value)}`;
  const [query, setQuery] = useState("");
  const [activeValue, setActiveValue] = useState<string | undefined>(undefined);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const wasOpen = useRef(false);

  const searchText = query.trim();
  const needle = fold(searchText);
  // A host's label function can answer nothing for a value it does not know — a `?status=` of
  // another deploy, say — and a value with no label is still an option, read as itself.
  const labelOf = (option: string) => optionLabel(option) ?? option;
  const visibleOptions = options.filter((option) => fold(labelOf(option)).includes(needle));
  const createValue =
    onCreate !== undefined &&
    searchText !== "" &&
    !options.some((option) => fold(option) === fold(searchText))
      ? searchText
      : undefined;
  const rows = createValue === undefined ? visibleOptions : [...visibleOptions, createValue];
  const activeRow = activeValue !== undefined && rows.includes(activeValue) ? activeValue : rows[0];

  // What the reader picked, compared as the server compares a label: a URL that lower-cased
  // `Epic` still ticks the option the project spells `Epic`, and ticking it again removes it
  // rather than adding a second spelling.
  const isPicked = (value: string) => selected.some((current) => fold(current) === fold(value));
  // A single pick puts the focus on the trigger before it hands the value over, so a pick that
  // takes the trigger away - the search bar's Project list, gone once its narrowing is on - takes
  // it from the trigger's own place, from which the panel around it hands the focus on
  // (`useFocusPlace`), rather than from a list portalled outside that panel.
  const choose = (value: string) => {
    setActiveValue(value);
    if (props.mode === "single") {
      onOpenChange(false);
      triggerRef.current?.focus();
      props.onPick(value);
      return;
    }
    props.onChange(
      isPicked(value)
        ? selected.filter((current) => fold(current) !== fold(value))
        : [...selected, value]
    );
  };
  const create = (value: string) => {
    if (onCreate === undefined) return;
    onCreate(value);
    setQuery("");
    setActiveValue(value);
  };

  // Opening starts a fresh search and focuses it; closing returns focus to the trigger.
  useEffect(() => {
    if (open) {
      setQuery("");
      setActiveValue(undefined);
      inputRef.current?.focus();
    } else if (wasOpen.current) {
      triggerRef.current?.focus();
    }
    wasOpen.current = open;
  }, [open]);

  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "ArrowDown" && rows.length > 0) {
      event.preventDefault();
      const index = activeRow === undefined ? -1 : rows.indexOf(activeRow);
      setActiveValue(rows[(index + 1) % rows.length]);
      return;
    }
    if (event.key === "ArrowUp" && rows.length > 0) {
      event.preventDefault();
      const index = activeRow === undefined ? 0 : rows.indexOf(activeRow);
      setActiveValue(rows[(index - 1 + rows.length) % rows.length]);
      return;
    }
    if (event.key === "Enter" && activeRow !== undefined) {
      event.preventDefault();
      if (activeRow === createValue) {
        create(activeRow);
      } else {
        choose(activeRow);
      }
      return;
    }
    // Tab and Shift+Tab close the list and leave the reader on its trigger, as Escape does. The
    // close puts the focus back on the trigger before the browser's own Tab would run, which
    // would carry it on past the trigger: out of a filter panel altogether from its first
    // control, where a list inside the panel is to be left for its trigger (`useNestedPanels`).
    if (event.key === "Tab") {
      event.preventDefault();
      onOpenChange(false);
    }
  };

  const rowClassName = (active: boolean) =>
    `flex min-h-11 w-full items-center gap-2 border-b px-3 py-2 text-left text-sm last:border-b-0 ${borderDefault} ${textSecondaryOnSurface} ${cardHoverBorder} ${
      active ? `${selectedCardBg} ${selectedCardBorder}` : ""
    }`;

  return (
    <>
      <button
        aria-describedby={single && selected.length > 0 ? pickedId : undefined}
        aria-disabled={ariaDisabled || undefined}
        aria-expanded={open}
        aria-haspopup="listbox"
        aria-label={single ? (triggerAriaLabel ?? label) : triggerAriaLabel}
        className={triggerClassName}
        data-control={control}
        disabled={disabled}
        onClick={() => onOpenChange(!open)}
        ref={triggerRef}
        type="button"
      >
        {single ? (
          <span id={pickedId}>
            {children ?? (selected[0] === undefined ? label : labelOf(selected[0]))}
          </span>
        ) : (
          (children ?? (selected.length === 0 ? label : `${label} · ${selected.length}`))
        )}
      </button>
      {open ? (
        <AnchoredPanel
          anchorRef={triggerRef}
          className={`max-h-72 w-64 max-w-[calc(100vw-1rem)] overflow-auto rounded-lg border shadow-lg ${card} ${borderDefault}`}
          onDismiss={() => onOpenChange(false)}
        >
          <div className={`sticky top-0 border-b p-2 ${card}`}>
            <input
              aria-activedescendant={activeRow === undefined ? undefined : optionId(activeRow)}
              aria-autocomplete="list"
              aria-controls={listboxId}
              aria-expanded="true"
              aria-label={searchLabel}
              className={`block min-h-11 w-full rounded-lg px-3 py-2 text-sm outline-none ${inputClasses(true)}`}
              disabled={disabled}
              onChange={(event) => {
                setQuery(event.target.value);
                setActiveValue(undefined);
                onQueryChange?.(event.target.value);
              }}
              onKeyDown={handleKeyDown}
              placeholder={searchLabel}
              ref={inputRef}
              role="combobox"
              type="search"
              value={query}
            />
          </div>
          <div
            aria-label={`${label} options`}
            aria-multiselectable={single ? undefined : "true"}
            id={listboxId}
            role="listbox"
          >
            {visibleOptions.map((option) => {
              const isSelected = isPicked(option);
              const label = labelOf(option);
              const detail = optionDetail?.(option);
              const spokenDetail = optionDetailLabel?.(option) ?? detail;
              return (
                <button
                  aria-label={spokenDetail === undefined ? undefined : `${label}, ${spokenDetail}`}
                  aria-selected={isSelected}
                  className={rowClassName(activeRow === option)}
                  disabled={disabled}
                  id={optionId(option)}
                  key={option}
                  onClick={() => choose(option)}
                  onMouseDown={(event) => event.preventDefault()}
                  role="option"
                  type="button"
                >
                  <span
                    aria-hidden="true"
                    className={
                      single
                        ? "w-4 shrink-0 text-center"
                        : `grid size-4 shrink-0 place-items-center rounded border text-xs leading-none ${borderStrong}`
                    }
                  >
                    {isSelected ? "✓" : ""}
                  </span>
                  <TruncatedText className="min-w-0 flex-1" title={label}>
                    {label}
                  </TruncatedText>
                  {detail === undefined ? null : (
                    <span className={`shrink-0 text-xs tabular-nums ${textMutedOnSurface}`}>
                      {detail}
                    </span>
                  )}
                </button>
              );
            })}
            {createValue === undefined ? null : (
              <button
                aria-selected="false"
                className={rowClassName(activeRow === createValue)}
                disabled={disabled}
                id={optionId(createValue)}
                onClick={() => create(createValue)}
                onMouseDown={(event) => event.preventDefault()}
                role="option"
                type="button"
              >
                <span aria-hidden="true" className="w-4 shrink-0 text-center">
                  +
                </span>
                Create &quot;{createValue}&quot;
              </button>
            )}
            {rows.length === 0 ? (
              <p className={`px-3 py-2 text-sm ${textMutedOnSurface}`}>{emptyMessage}</p>
            ) : null}
          </div>
          {error === null ? null : (
            <p className={`border-t px-3 py-2 text-sm ${borderDefault} ${dangerText}`}>{error}</p>
          )}
          {props.mode === "single" || selected.length === 0 ? null : (
            <div className={`sticky bottom-0 border-t p-2 ${card}`}>
              <button
                className={`min-h-11 w-full rounded-lg px-3 py-2 text-sm font-medium ${surfaceMutedBg} ${textSecondaryOnSurface}`}
                disabled={disabled}
                onClick={() => props.onChange([])}
                onMouseDown={(event) => event.preventDefault()}
                type="button"
              >
                Clear {label.toLocaleLowerCase()}
              </button>
            </div>
          )}
        </AnchoredPanel>
      ) : null}
    </>
  );
}
