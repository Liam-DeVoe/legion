import { type ReactNode, useId, useState } from "react";
import { Button } from "../../components/Button";
import { Input, Select } from "../../components/Field";
import { dangerText, textMutedOnCanvas, textSecondaryOnCanvas } from "../../theme/classes";
import {
  customRange,
  lastUtcDate,
  PRESET_LABELS,
  utcDate,
  WINDOW_PRESETS,
  type WindowChoice,
} from "./lib/window";

/** The Delivery page's window: a rolling preset (the last 7, 14, 28 or 84 days) or a custom range
 *  of whole UTC days. A custom range is applied only once both dates are set and it spans at most
 *  84 days, the server's limit; a longer one is refused here with the limit stated. */
export function WindowControl({
  value,
  onChange,
}: {
  value: WindowChoice;
  onChange: (next: WindowChoice) => void;
}): ReactNode {
  const id = useId();
  const [custom, setCustom] = useState(value.kind === "custom");
  const [fromDate, setFromDate] = useState(value.kind === "custom" ? utcDate(value.from) : "");
  const [toDate, setToDate] = useState(value.kind === "custom" ? lastUtcDate(value.to) : "");
  const [refused, setRefused] = useState<string | null>(null);

  const apply = () => {
    const range = customRange(fromDate, toDate);
    if ("refused" in range) {
      setRefused(range.refused);
      return;
    }
    setRefused(null);
    onChange({ kind: "custom", ...range });
  };

  return (
    <div
      className={`flex max-w-full flex-wrap items-center gap-x-3 gap-y-2 text-sm ${textSecondaryOnCanvas}`}
    >
      {/* Each label stays on its control's line when the row wraps. */}
      {/* The select's own value ("Last 28 days", "Custom range…") says what it sets, so its label
          is a screen reader's only. */}
      <span className="flex items-center gap-2">
        <label className="sr-only" htmlFor={`${id}-window`}>
          Window
        </label>
        <Select
          className="w-40"
          id={`${id}-window`}
          onChange={(event) => {
            const next = event.target.value;
            const preset = WINDOW_PRESETS.find((candidate) => candidate === next);
            if (preset === undefined) {
              setCustom(true);
              return;
            }
            setCustom(false);
            setRefused(null);
            onChange({ kind: "preset", preset });
          }}
          value={custom ? "custom" : value.kind === "preset" ? value.preset : "custom"}
        >
          {WINDOW_PRESETS.map((preset) => (
            <option key={preset} value={preset}>
              {PRESET_LABELS[preset]}
            </option>
          ))}
          <option value="custom">Custom range…</option>
        </Select>
      </span>
      {custom ? (
        <>
          <span className="flex items-center gap-2">
            <label htmlFor={`${id}-from`}>From</label>
            <Input
              className="w-36"
              id={`${id}-from`}
              onChange={(event) => setFromDate(event.target.value)}
              type="date"
              value={fromDate}
            />
          </span>
          <span className="flex items-center gap-2">
            <label htmlFor={`${id}-to`}>To</label>
            <Input
              className="w-36"
              id={`${id}-to`}
              onChange={(event) => setToDate(event.target.value)}
              type="date"
              value={toDate}
            />
            <span className={`text-xs ${textMutedOnCanvas}`}>(UTC)</span>
          </span>
          <Button onClick={apply}>Apply</Button>
          {refused === null ? null : (
            <p className={`basis-full text-xs ${dangerText}`} role="alert">
              {refused}
            </p>
          )}
        </>
      ) : null}
    </div>
  );
}
