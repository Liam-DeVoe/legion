import { type ReactNode, useId, useState } from "react";
import type { LegionBoardHeader } from "../../api/types";
import { ViewPanel } from "../../components/ViewPanel";
import {
  activeTabIndicatorBorder,
  activeTabIndicatorText,
  borderDefault,
  borderTransparent,
  dangerText,
  focusVisibleRing,
  surfaceBg,
  surfaceMutedBg,
  surfaceMutedHoverBg,
  surfaceMutedStrongBg,
  textMutedOnCanvas,
  textPrimaryOnCanvas,
  textSecondaryOnCanvas,
} from "../../theme/classes";
import { Timestamp } from "../refs/Timestamp";
import type { ActiveView, BuiltInView, SavedView, ViewLayout, ViewNameCheck } from "./issues-views";

/**
 * The Issues tab's own parts above the table: the row of view tabs with their counts and the
 * alert line under it, and the page's own controls in the control row (Save view, the (i) hint,
 * the Table and Board switch, the Board's toggles). None of them narrows the rows.
 */

const countClass = `rounded-full px-1.5 text-[11px] font-medium tabular-nums ${surfaceMutedBg} ${textSecondaryOnCanvas}`;

/** The face of the layout switch's options and of a Board toggle: filled while pressed. */
const toggleFace = (pressed: boolean) =>
  `min-h-11 rounded-md px-2 text-xs font-medium whitespace-nowrap md:min-h-7 ${focusVisibleRing} ${
    pressed ? surfaceMutedStrongBg : ""
  } ${textSecondaryOnCanvas}`;

const TOGGLE_BOX = `inline-flex shrink-0 rounded-lg border p-0.5 ${borderDefault} ${surfaceMutedBg}`;

const panelButton = `inline-flex min-h-11 shrink-0 items-center rounded-md border px-2 text-xs font-medium md:min-h-8 ${borderDefault} ${surfaceMutedHoverBg} ${textSecondaryOnCanvas} ${focusVisibleRing}`;

/** One tab: the view's name with the dot of a changed view on its line, and its count. */
function ViewTab({
  active,
  changed,
  count,
  name,
  onSelect,
}: {
  active: boolean;
  changed: boolean;
  count: number | undefined;
  name: string;
  onSelect: () => void;
}): ReactNode {
  const counted = count === undefined ? "" : `, ${count} ${count === 1 ? "issue" : "issues"}`;
  return (
    <button
      // Its view's name, its count where it shows one, and its mark.
      aria-label={`${name}${counted}${changed ? ", changed" : ""}`}
      aria-selected={active}
      // On a phone a tab's count stands under its name, so the tabs keep their row whatever
      // their counts; from `md` up the count follows the name on its line.
      className={`inline-flex min-h-11 shrink-0 flex-col items-center justify-center border-b-2 px-2 py-1 text-[13px] leading-tight whitespace-nowrap md:min-h-9 md:flex-row md:gap-1.5 md:py-2 ${focusVisibleRing} ${
        active
          ? `font-semibold ${activeTabIndicatorBorder} ${activeTabIndicatorText}`
          : `${borderTransparent} ${textSecondaryOnCanvas}`
      }`}
      onClick={onSelect}
      role="tab"
      type="button"
    >
      <span className="inline-flex items-center gap-1">
        {name}
        {changed ? (
          <span
            aria-hidden
            className={`size-1.5 rounded-full bg-current ${textPrimaryOnCanvas}`}
            data-view-changed=""
          />
        ) : null}
      </span>
      {count === undefined ? null : (
        // The space makes the tab's text read "Open 6", the words its name starts with, as a
        // speech user says them (WCAG 2.5.3). A flex box draws no space between its items.
        <>
          {" "}
          <span className={countClass}>{count}</span>
        </>
      )}
    </button>
  );
}

/** A failed read the alert line names: its whole error, and the way to read it again. */
export interface ReadFailure {
  readonly message: string;
  readonly retry: () => void;
  readonly retrying: boolean;
}

/** One alert of the line under the tabs: its words, then Retry where the read can be made again. */
function Alert({
  failure,
  retryName,
  text,
}: {
  failure?: ReadFailure;
  retryName?: string;
  text: string;
}): ReactNode {
  return (
    <p className="break-words" role="alert">
      {text}
      {failure === undefined ? null : (
        <>
          {" "}
          <button
            aria-label={retryName}
            className="font-semibold underline disabled:opacity-50"
            disabled={failure.retrying}
            onClick={failure.retry}
            type="button"
          >
            Retry
          </button>
        </>
      )}
    </p>
  );
}

/**
 * The row of view tabs: Open and Done, then each saved view, each with its count where the page
 * can count it and the dot of the view the reader changed, then the open saved view's delete
 * button after the tabs, since the tablist holds tabs alone. A press on a tab opens its view as
 * it was saved, so a press on the changed tab gives its view back. Under the row, the alert
 * line: a failed read of the daemon setting, the daemon's last read failed, or a failed read of
 * the board or of the closed rows' facts, each on a line of its own with its whole error.
 */
export function IssueViewTabs({
  active,
  boardFailure,
  changed,
  counts,
  daemonFailure,
  daemonReadError,
  factsFailure,
  onDelete,
  onSelect,
  onSelectSaved,
  saved,
  savedCounts,
  views,
}: {
  active: ActiveView;
  /** The read of the Legion board failed: the Legion cells show the error mark. */
  boardFailure: ReadFailure | undefined;
  /** The open view differs from its own: its tab shows the dot. */
  changed: boolean;
  /** Each built-in view's count by its id; a view the page cannot count now has none. */
  counts: ReadonlyMap<string, number | undefined>;
  /** The daemon setting's read failed, and no read has answered: the page is drawn as a
   *  project with no Legion daemon. */
  daemonFailure: ReadFailure | undefined;
  /** The board's header says the daemon's last read failed: its error, or "" for none given. */
  daemonReadError: string | undefined;
  /** The read of the closed rows' Legion facts failed. */
  factsFailure: ReadFailure | undefined;
  onDelete: (name: string) => void;
  onSelect: (id: BuiltInView["id"]) => void;
  onSelectSaved: (name: string) => void;
  saved: readonly SavedView[];
  /** Each saved view's count by its name; one the page cannot count now has none. */
  savedCounts: ReadonlyMap<string, number | undefined>;
  views: readonly BuiltInView[];
}): ReactNode {
  const alerts =
    daemonFailure !== undefined ||
    daemonReadError !== undefined ||
    boardFailure !== undefined ||
    factsFailure !== undefined;
  // The open saved view, whose delete button follows the tabs: a tablist holds tabs alone.
  const openSaved =
    active.kind === "saved" ? saved.find((view) => view.name === active.name) : undefined;
  return (
    <div className="flex flex-col gap-1">
      <div
        className={`flex flex-wrap items-center border-b md:flex-nowrap md:overflow-x-auto ${borderDefault}`}
      >
        <div
          aria-label="Views"
          className="flex flex-wrap items-center md:flex-nowrap"
          role="tablist"
        >
          {views.map((view) => {
            const on = active.kind === "built-in" && active.id === view.id;
            return (
              <ViewTab
                active={on}
                changed={on && changed}
                count={counts.get(view.id)}
                key={view.id}
                name={view.name}
                onSelect={() => onSelect(view.id)}
              />
            );
          })}
          {saved.map((view) => {
            const on = active.kind === "saved" && active.name === view.name;
            return (
              <ViewTab
                active={on}
                changed={on && changed}
                count={savedCounts.get(view.name)}
                key={view.name}
                name={view.name}
                onSelect={() => onSelectSaved(view.name)}
              />
            );
          })}
        </div>
        {openSaved === undefined ? null : (
          <button
            aria-label={`Delete saved view ${openSaved.name}`}
            className={`inline-flex min-h-11 min-w-11 items-center justify-center rounded text-xs md:min-h-8 md:min-w-8 ${textMutedOnCanvas} ${surfaceMutedHoverBg} ${focusVisibleRing}`}
            onClick={() => onDelete(openSaved.name)}
            title="Delete this saved view"
            type="button"
          >
            <span aria-hidden>✕</span>
          </button>
        )}
      </div>
      {alerts ? (
        <div className={`flex flex-col gap-0.5 text-xs ${dangerText}`}>
          {daemonFailure === undefined ? null : (
            <Alert
              failure={daemonFailure}
              retryName="Retry reading the Legion daemon setting"
              text={`Could not read the Legion daemon setting: ${daemonFailure.message}`}
            />
          )}
          {daemonReadError === undefined ? null : (
            <Alert
              text={
                daemonReadError === ""
                  ? "The last read of the Legion daemon failed."
                  : `The last read of the Legion daemon failed: ${daemonReadError}`
              }
            />
          )}
          {boardFailure === undefined ? null : (
            <Alert
              failure={boardFailure}
              retryName="Retry reading the Legion board"
              text={`Could not read the Legion board: ${boardFailure.message}`}
            />
          )}
          {factsFailure === undefined ? null : (
            <Alert
              failure={factsFailure}
              retryName="Retry reading the Legion columns"
              text={`Could not read the Legion columns of the closed issues: ${factsFailure.message}`}
            />
          )}
        </div>
      ) : null}
    </div>
  );
}

/**
 * **Save view**: a panel that asks for a name and saves the view under it. The name is checked
 * (`check`, `checkViewName`): a problem is said under the box, and a name a saved view has asks
 * to replace that view first. `initial` is the name the box opens with: the open saved view's.
 */
function SaveView({
  check,
  initial,
  onOpenChange,
  onSave,
  open,
}: {
  check: (name: string) => ViewNameCheck;
  initial: string;
  onOpenChange: (open: boolean) => void;
  onSave: (name: string) => void;
  open: boolean;
}): ReactNode {
  const inputId = useId();
  const [name, setName] = useState(initial);
  // The answer of the last press of Save, until the name changes.
  const [answer, setAnswer] = useState<ViewNameCheck | undefined>(undefined);
  return (
    <ViewPanel
      align="right"
      label="Save view"
      onOpenChange={(next) => {
        if (next) {
          setName(initial);
          setAnswer(undefined);
        }
        onOpenChange(next);
      }}
      open={open}
      size="compact"
      title="Save view"
    >
      {(close) => (
        <form
          className="flex w-full flex-col gap-2 p-2"
          onSubmit={(event) => {
            event.preventDefault();
            const checked = check(name);
            if (checked.kind === "new") {
              onSave(checked.name);
              close();
            } else setAnswer(checked);
          }}
        >
          <label
            className={`flex flex-col gap-1 text-xs ${textSecondaryOnCanvas}`}
            htmlFor={inputId}
          >
            Name of the view
          </label>
          <input
            // biome-ignore lint/a11y/noAutofocus: the panel opens to type a name
            autoFocus
            className={`min-h-11 rounded-md border px-2 text-sm md:min-h-8 ${borderDefault} ${surfaceBg}`}
            id={inputId}
            onChange={(event) => {
              setName(event.target.value);
              setAnswer(undefined);
            }}
            value={name}
          />
          {answer?.kind === "problem" ? (
            <p className={`text-xs ${dangerText}`} role="alert">
              {answer.problem}
            </p>
          ) : null}
          {answer?.kind === "replace" ? (
            <p className={`text-xs ${textSecondaryOnCanvas}`} role="alert">
              A saved view has the name {answer.replaces}. Replace it?
            </p>
          ) : null}
          <span className="flex gap-2">
            {answer?.kind === "replace" ? (
              <button
                className={panelButton}
                onClick={() => {
                  onSave(answer.name);
                  close();
                }}
                type="button"
              >
                Replace
              </button>
            ) : (
              <button className={panelButton} type="submit">
                Save
              </button>
            )}
            <button className={panelButton} onClick={close} type="button">
              Cancel
            </button>
          </span>
        </form>
      )}
    </ViewPanel>
  );
}

/**
 * The view's own controls in the control row, after the Columns menu and before the display
 * menu: **Save view**, in room the row keeps for it whether it shows or not, so its coming and
 * going moves no table row. It shows while the view is changed. `children` follow it: the (i)
 * hint's place.
 */
export function ViewControls({
  changed,
  check,
  children,
  initialName,
  onSave,
}: {
  changed: boolean;
  check: (name: string) => ViewNameCheck;
  children?: ReactNode;
  initialName: string;
  onSave: (name: string) => void;
}): ReactNode {
  const [open, setOpen] = useState(false);
  return (
    <>
      <span aria-hidden={!changed} className={`flex shrink-0 ${changed ? "" : "invisible"}`}>
        <SaveView
          check={check}
          initial={initialName}
          onOpenChange={setOpen}
          onSave={onSave}
          open={open && changed}
        />
      </span>
      {children}
    </>
  );
}

/**
 * The (i) hint, after Save view in a project with a Legion daemon: what the board's header says
 * of the daemon, and no other text. Capacity is the issues the daemon runs of its cap ("3 of
 * 4"), or the issues alone where the header names no cap; Trees is its tree count; Last read is
 * when Dispatch last read the daemon. Before any read, the panel says only that. Its trigger and
 * its panel are named "Legion daemon".
 */
export function DaemonHint({
  header,
  reading,
}: {
  /** The board's header, once the board has answered. */
  header: LegionBoardHeader | undefined;
  /** The board's read is still out. */
  reading: boolean;
}): ReactNode {
  const [open, setOpen] = useState(false);
  return (
    <ViewPanel
      align="right"
      label="i"
      onOpenChange={setOpen}
      open={open}
      title="Legion daemon"
      triggerClassName={`inline-flex size-11 shrink-0 items-center justify-center rounded-full border text-xs font-semibold italic md:size-8 ${borderDefault} ${surfaceMutedHoverBg} ${textSecondaryOnCanvas} ${focusVisibleRing}`}
    >
      {() => (
        <div className={`p-2 text-xs ${textSecondaryOnCanvas}`}>
          {header === undefined ? (
            <p>{reading ? "Loading…" : "Could not read the Legion board."}</p>
          ) : header.read_at === null ? (
            <p>Dispatch has not read the daemon yet.</p>
          ) : (
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1">
              <dt className={textMutedOnCanvas}>Capacity</dt>
              <dd className="tabular-nums">
                {header.slot_cap === null
                  ? header.slots_in_use
                  : `${header.slots_in_use} of ${header.slot_cap}`}
              </dd>
              <dt className={textMutedOnCanvas}>Trees</dt>
              <dd className="tabular-nums">{header.trees}</dd>
              <dt className={textMutedOnCanvas}>Last read</dt>
              <dd>
                <Timestamp at={header.read_at} />
              </dd>
            </dl>
          )}
        </div>
      )}
    </ViewPanel>
  );
}

/** The layout control: the table or the Board, of the same view. */
export function LayoutSwitch({
  layout,
  onLayout,
}: {
  layout: ViewLayout;
  onLayout: (layout: ViewLayout) => void;
}): ReactNode {
  return (
    <fieldset className={TOGGLE_BOX}>
      <legend className="sr-only">Layout</legend>
      {(["table", "board"] as const).map((option) => (
        <button
          aria-pressed={layout === option}
          className={toggleFace(layout === option)}
          key={option}
          onClick={() => onLayout(option)}
          type="button"
        >
          {option === "table" ? "Table" : "Board"}
        </button>
      ))}
    </fieldset>
  );
}

/** A choice of how the Board draws (Show Icebox, Lanes), which narrows nothing: a button that
 *  stays pressed, drawn as the layout switch's options are, and no chip, since a chip of the
 *  control row stands for a narrowing. */
export function ToggleButton({
  children,
  onChange,
  pressed,
}: {
  children: ReactNode;
  onChange: (pressed: boolean) => void;
  pressed: boolean;
}): ReactNode {
  return (
    <span className={TOGGLE_BOX}>
      <button
        aria-pressed={pressed}
        className={toggleFace(pressed)}
        onClick={() => onChange(!pressed)}
        type="button"
      >
        {children}
      </button>
    </span>
  );
}
