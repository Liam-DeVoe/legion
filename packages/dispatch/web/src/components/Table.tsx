import type { HTMLAttributes, ReactNode, TdHTMLAttributes, ThHTMLAttributes } from "react";

import { borderDefault, rowHoverBg, textMutedOnCanvas } from "../theme/classes";

/**
 * The plain table (`DESIGN.md`): no box around it, so its first column starts at the page's left
 * edge, under the section heading above it. The header row is 12 px muted over a 1px rule, each
 * row 40 px over a rule of its own, and every cell has 12 px on each side except at the table's
 * two edges. It scrolls sideways inside its wrapper rather than widen the page. `DataTable` is
 * the other table: the sortable, filterable, virtualized one, which scrolls inside a box.
 *
 * `relative` is load-bearing: a table's `sr-only` header cell is absolutely positioned, and
 * without a positioned ancestor inside the scroll container its containing block is the page,
 * so it sits at the scrolled table's own x and widens the document past the viewport.
 */
export const tableWrapperClassName = "relative overflow-x-auto";
export const tableClassName = "w-full border-collapse text-left text-sm";
export const tableHeadRowClassName = `border-b ${borderDefault}`;
export const tableHeadCellClassName = `h-8 px-3 text-xs font-medium whitespace-nowrap first:pl-0 last:pr-0 ${textMutedOnCanvas}`;
export const tableRowClassName = `border-b ${borderDefault}`;
export const tableCellClassName = "h-10 px-3 py-1.5 align-middle first:pl-0 last:pr-0";

export function Table({
  children,
  className,
  head,
  ...rest
}: {
  "aria-label"?: string;
  "aria-labelledby"?: string;
  children: ReactNode;
  /** Placement only (the margin above it). */
  className?: string;
  /** The header row's cells: `Th`s. */
  head: ReactNode;
}): ReactNode {
  return (
    <div className={`${tableWrapperClassName} ${className ?? ""}`}>
      <table className={tableClassName} {...rest}>
        <thead>
          <tr className={tableHeadRowClassName}>{head}</tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

export function Th({
  children,
  className,
  ...rest
}: Omit<ThHTMLAttributes<HTMLTableCellElement>, "className"> & {
  children?: ReactNode;
  className?: string;
}): ReactNode {
  return (
    <th className={`${tableHeadCellClassName} ${className ?? ""}`} scope="col" {...rest}>
      {children}
    </th>
  );
}

/** A row. `hover` tints it under the pointer (`rowHoverBg`), for a table whose rows open
 *  something or are wide enough that the eye needs the line. `className` is placement only;
 *  `data-*` and the other row attributes pass through. */
export function Tr({
  children,
  className,
  hover = false,
  ...rest
}: Omit<HTMLAttributes<HTMLTableRowElement>, "className"> & {
  children?: ReactNode;
  className?: string;
  hover?: boolean;
  [key: `data-${string}`]: string | undefined;
}): ReactNode {
  return (
    <tr
      className={`${tableRowClassName} ${hover ? rowHoverBg : ""} ${className ?? ""}`.trim()}
      {...rest}
    >
      {children}
    </tr>
  );
}

/** A cell. `className` carries the cell's text tone and its own alignment, nothing of its box. */
export function Td({
  children,
  className,
  ...rest
}: Omit<TdHTMLAttributes<HTMLTableCellElement>, "className"> & {
  children?: ReactNode;
  className?: string;
}): ReactNode {
  return (
    <td className={`${tableCellClassName} ${className ?? ""}`} {...rest}>
      {children}
    </td>
  );
}
