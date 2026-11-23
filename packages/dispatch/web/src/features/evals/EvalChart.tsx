import {
  type ColumnDef,
  type ColumnFiltersState,
  flexRender,
  type GroupingState,
  type PaginationState,
  type SortingState,
} from "@tanstack/react-table";
import { type ReactNode, useId, useMemo, useState } from "react";

import { DataTable, useDataTable } from "../../components/DataTable";
import {
  borderStrong,
  dangerText,
  dragHandleBg,
  successText,
  surfaceMutedBg,
  textMutedOnSurface,
  textPrimaryOnSurface,
  textPrimaryOnSurfaceMuted,
  textSecondaryOnCanvas,
} from "../../theme/classes";
import { lastUtcDate, utcDate } from "../delivery/lib/window";
import { type LayoutColumns, useTableLayout } from "../project/issues-table-layout";
import { ALL_ROWS } from "../project/issues-table-model";
import type {
  ChartModel,
  ChartPoint,
  ChartRenderer,
  ChartSeries,
  ChartValue,
  ValueState,
} from "./chart-model";

/** A drawn value's colour by its state: its own verdict, or muted where there is nothing to
 *  measure. */
const TONE: Readonly<Record<ValueState, string>> = {
  absent: textMutedOnSurface,
  met: successText,
  missed: dangerText,
  plain: "",
};

/** What the chart shows: its graph, or its table. */
type Mode = "graph" | "table";

/** The Graph | Table segmented toggle, as the Delivery page's Timeline | List toggle draws. */
function ModeToggle({ mode, onChange }: { mode: Mode; onChange: (mode: Mode) => void }): ReactNode {
  const item = (value: Mode, label: string) => (
    <button
      aria-pressed={mode === value}
      className={`h-6 px-2 text-xs font-medium first:rounded-l-md last:rounded-r-md ${
        mode === value ? `${surfaceMutedBg} ${textPrimaryOnSurfaceMuted}` : textSecondaryOnCanvas
      }`}
      onClick={() => onChange(value)}
      type="button"
    >
      {label}
    </button>
  );
  return (
    <fieldset aria-label="Graph or table" className={`flex rounded-md border ${borderStrong}`}>
      {item("graph", "Graph")}
      <span aria-hidden="true" className={`w-px ${dragHandleBg}`} />
      {item("table", "Table")}
    </fieldset>
  );
}

/** One series' window figures, each with its change from the compare range while that is shown,
 *  then what the series says of its input (spec R18) and the rows it counts by the project's
 *  default repository (spec R15), beside the value they qualify. They stand outside the graph and
 *  the table, which draw the series a day at a time, so they name no `data-value`. */
function SeriesFigures({ compare, series }: { compare: boolean; series: ChartSeries }): ReactNode {
  return (
    <div
      className={`flex flex-wrap items-baseline gap-x-3 gap-y-0.5 ${textPrimaryOnSurface}`}
      data-window-series={series.name}
    >
      {series.headline.map((id, index) => {
        const value = series.window.values[id];
        if (value === undefined) return null;
        const change = series.window.change?.[id];
        const label = series.statistics.find((statistic) => statistic.id === id)?.label ?? id;
        return (
          <span className="flex items-baseline gap-1" key={id}>
            <span
              className={`${index === 0 ? "text-lg" : "text-sm"} font-semibold ${TONE[value.state]}`}
              data-window-statistic={id}
            >
              {value.label}
            </span>
            <span className={`text-xs ${textMutedOnSurface}`}>{label.toLowerCase()}</span>
            {compare && change !== undefined ? (
              <span className={`text-xs ${textMutedOnSurface}`} data-window-change={id}>
                {change.value === null ? "no change to compare" : `${change.label} vs compare`}
              </span>
            ) : null}
          </span>
        );
      })}
      {series.dataNote === null ? null : (
        <span className={`text-xs ${textMutedOnSurface}`} data-note="data">
          {series.dataNote}
        </span>
      )}
      {series.repositoryNote === null ? null : (
        <span className={`text-xs ${textMutedOnSurface}`} data-note="repository">
          {series.repositoryNote}
        </span>
      )}
    </div>
  );
}

/** One row of a chart's table: a place on its axis, and each series' point there. */
interface TableRow {
  readonly id: string;
  readonly label: string;
  readonly points: readonly (ChartPoint | undefined)[];
}

/** Which of a point's values a column holds: its own, the compare range's, or the change. */
type Part = "values" | "compare" | "change";

/** One value of the table, named in the DOM as the graph names the value it draws. */
function TableValue(props: {
  chart: string;
  series: string;
  statistic: string;
  x: string;
  part: Part;
  value: ChartValue | undefined;
}): ReactNode {
  const { value } = props;
  if (value === undefined) return null;
  return (
    <span
      className={TONE[value.state]}
      data-chart={props.chart}
      data-range={props.part === "values" ? "window" : props.part}
      data-series={props.series}
      data-statistic={props.statistic}
      data-value={value.value === null ? undefined : String(value.value)}
      data-x={props.x}
    >
      {value.label}
    </span>
  );
}

/** The table's columns: the axis, then each statistic of each series, and with the compare range
 *  shown, each one's compare value and its change beside it. */
function tableColumns(model: ChartModel, compare: boolean): ColumnDef<TableRow>[] {
  const several = model.series.length > 1;
  const columns: ColumnDef<TableRow>[] = [
    {
      accessorFn: (row) => row.id,
      cell: ({ row }) => row.original.label,
      enableColumnFilter: false,
      header: model.axis.label,
      id: "x",
    },
  ];
  model.series.forEach((series, index) => {
    for (const statistic of series.statistics) {
      const name = several ? `${series.label} ${statistic.label.toLowerCase()}` : statistic.label;
      const column = (part: Part, header: string): ColumnDef<TableRow> => ({
        accessorFn: (row) => row.points[index]?.[part]?.[statistic.id]?.value ?? undefined,
        cell: ({ row }) => (
          <TableValue
            chart={model.name}
            part={part}
            series={series.name}
            statistic={statistic.id}
            value={row.original.points[index]?.[part]?.[statistic.id]}
            x={row.original.id}
          />
        ),
        enableColumnFilter: false,
        header,
        id: `${series.name}:${statistic.id}:${part}`,
        sortUndefined: "last",
      });
      columns.push(column("values", name));
      if (compare)
        columns.push(column("compare", `${name} compare`), column("change", `${name} change`));
    }
  });
  return columns;
}

/** The height of one row of a chart's table, which its rows are drawn at (`h-7`). */
const ROW_HEIGHT = 28;
/** Room for the table's header row and its border. */
const HEAD_HEIGHT = 48;
const NO_COUNTS: ReadonlyMap<string, number> = new Map();

/** A chart's table: the one table core (`components/DataTable.tsx`, plan D5), one row per place on
 *  the chart's axis. Its box is as tall as its rows, so every row is drawn and the table holds
 *  every number the graph draws in the page, as well as on the screen. */
function ChartTable({ compare, model }: { compare: boolean; model: ChartModel }): ReactNode {
  const columns = useMemo(() => tableColumns(model, compare), [model, compare]);
  const rows = useMemo(
    (): TableRow[] =>
      (model.series[0]?.points ?? []).map((point, index) => ({
        id: point.x,
        label: point.partial ? `${point.label} (so far)` : point.label,
        points: model.series.map((series) => series.points[index]),
      })),
    [model]
  );
  const layoutColumns = useMemo(
    (): LayoutColumns => ({
      ids: columns.flatMap((column) => (column.id === undefined ? [] : [column.id])),
    }),
    [columns]
  );
  const [sorting, setSorting] = useState<SortingState>([]);
  const [grouping, setGrouping] = useState<GroupingState>([]);
  const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([]);
  const [globalFilter, setGlobalFilter] = useState("");
  const [pagination, setPagination] = useState<PaginationState>({
    pageIndex: 0,
    pageSize: ALL_ROWS,
  });
  const [openFilter, setOpenFilter] = useState<string>();
  const layout = useTableLayout(
    `eval-chart:${model.name}${compare ? ":compare" : ""}`,
    layoutColumns
  );
  const state = { columnFilters, globalFilter, grouping, pagination, sorting };
  const table = useDataTable<TableRow>({
    columns,
    getRowId: (row) => row.id,
    layout: { ...layout, boxHeight: HEAD_HEIGHT + rows.length * ROW_HEIGHT },
    onColumnFiltersChange: setColumnFilters,
    onGlobalFilterChange: setGlobalFilter,
    onGroupingChange: setGrouping,
    onPaginationChange: setPagination,
    onSortingChange: setSorting,
    rows,
    state,
    viewKey: JSON.stringify(sorting),
  });
  return (
    <DataTable
      empty={`No ${model.axis.many}.`}
      filterCounts={NO_COUNTS}
      label={`${model.title} per ${model.axis.one}`}
      model={table}
      noun={{ many: model.axis.many, one: model.axis.one }}
      onOpenFilterChange={setOpenFilter}
      openFilter={openFilter}
      renderGroup={() => null}
      renderRow={({ index, measure, row }) => (
        <tr className="h-7" data-index={index} data-issue-row={row.id} key={row.id} ref={measure}>
          {row.getVisibleCells().map((cell) => (
            <td className="px-2 text-right tabular-nums first:text-left" key={cell.id}>
              {flexRender(cell.column.columnDef.cell, cell.getContext())}
            </td>
          ))}
        </tr>
      )}
      scrollTestId={`${model.name}-table-scroll`}
    />
  );
}

/**
 * The one chart component of the Evals tab (dispatch://INFRA-2918/artifact/plan-md, D4; spec R7):
 * it draws `model` through `renderer`, the renderer of its kind, under two controls. Graph or
 * Table: the table is the one table core, one row per place on the chart's axis, whose columns are
 * the model's values and, while the compare range is shown, each value's compare value and its
 * change, so it holds every number the graph draws. Compare: the header's compare range, shown by
 * default where the header names one and drawn as a muted second series; a chart that takes no
 * compare (`pending`, the merge timeline) has no such control. Both modes keep the reader's
 * compare choice. Each series' window figures stand above, with what the series says of its input
 * and of the rows it counts by default repository beside them, in a section as on a card.
 */
export function EvalChart({
  beside,
  model,
  renderer: Renderer,
}: {
  /** What stands beside the chart's title: a card's verdict. */
  beside?: ReactNode;
  model: ChartModel;
  renderer: ChartRenderer;
}): ReactNode {
  const titleId = useId();
  const [mode, setMode] = useState<Mode>("graph");
  // The reader's choice, kept apart from whether the header names a compare range: turned off
  // here, the compare stays off across Graph and Table and across a new answer.
  const [hidden, setHidden] = useState(false);
  const compareRange = model.comparable ? model.compareRange : null;
  const compare = compareRange !== null && !hidden;
  const compareEmpty =
    compare &&
    model.series.every((series) =>
      series.points.every((point) =>
        Object.values(point.compare ?? {}).every((value) => value.value === null)
      )
    );
  return (
    <figure
      aria-labelledby={titleId}
      className="m-0 flex min-w-0 flex-col gap-1"
      data-chart={model.name}
      data-chart-kind={model.kind}
    >
      <div className="flex flex-wrap items-center gap-2">
        <figcaption className={`mr-auto text-xs ${textMutedOnSurface}`} id={titleId}>
          {model.title}
        </figcaption>
        {beside}
        <ModeToggle mode={mode} onChange={setMode} />
        {model.comparable ? (
          <label className={`flex items-center gap-1 text-xs ${textSecondaryOnCanvas}`}>
            <input
              checked={compare}
              disabled={compareRange === null}
              onChange={(event) => setHidden(!event.target.checked)}
              type="checkbox"
            />
            Compare
          </label>
        ) : null}
      </div>
      {model.series.map((series) => (
        <SeriesFigures compare={compare} key={series.name} series={series} />
      ))}
      {compare && compareRange !== null ? (
        <p className={`text-[11px] ${textMutedOnSurface}`} data-compare-legend>
          {`compare ${utcDate(compareRange.from)} to ${lastUtcDate(compareRange.to)}`}
          {compareEmpty ? ": no data" : ""}
        </p>
      ) : null}
      {mode === "graph" ? (
        <Renderer compare={compare} model={model} />
      ) : (
        <ChartTable compare={compare} key={compare ? "compare" : "window"} model={model} />
      )}
    </figure>
  );
}
