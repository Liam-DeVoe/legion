import { type ReactNode, useState } from "react";

import {
  borderDefault,
  linkHoverText,
  linkText,
  surfaceBg,
  textSecondaryOnSurface,
} from "../../../theme/classes";
import { ItemChip } from "../../refs/ItemChip";
import { buildReferencePath } from "../../refs/routes";
import { type BlockOf, columnText, columnWords } from "./block-cells";

/** The cell classes every tabular block shares. */
export const blockCellClassName = `border-b px-2 py-1 text-left align-top ${borderDefault}`;
export const blockHeadClassName = `${blockCellClassName} font-medium ${textSecondaryOnSurface}`;

/** How many rows a table shows in an answer before "Show all" (design v2, state 5). */
export const TABLE_ROWS = 8;

/** An issue key, as a read names an issue: a row whose first column holds one is that issue,
 *  and that cell draws the issue as an item chip. */
const ISSUE_KEY = /^[A-Z][A-Z0-9]{1,9}-[1-9][0-9]*$/u;

/**
 * A table (design v2, state 5): one column for each field the spec names, in its order, headed in
 * the issue page's words, and one row for each row the read returned, each value in Dispatch's
 * words (`columnText`). A row whose first column is an issue's key draws that cell as the issue's
 * chip, a link to it. The header stays on top while the rows scroll under it. With more rows than
 * `TABLE_ROWS` and no `whole`, the table shows the first of them and "Show all 42 ›", which calls
 * `onShowAll` - the conversation opens the table whole over the bar or the panel - or, with none
 * given, shows every row in place.
 */
export function TableBlock({
  block,
  onShowAll,
  whole = false,
}: {
  readonly block: BlockOf<"table">;
  readonly onShowAll?: () => void;
  readonly whole?: boolean;
}): ReactNode {
  const [open, setOpen] = useState(false);
  const columns = block.spec.columns;
  const all = whole || open || block.rows.length <= TABLE_ROWS;
  const rows = all ? block.rows : block.rows.slice(0, TABLE_ROWS);
  return (
    <div className="space-y-1">
      <div className={`overflow-auto ${whole ? "" : "max-h-80"}`}>
        <table className="w-full text-xs" data-testid="going-block-table">
          <thead className={`sticky top-0 ${surfaceBg}`}>
            <tr>
              {columns.map((column) => (
                <th className={blockHeadClassName} key={column} scope="col">
                  {columnWords(column)}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, index) => (
              <tr
                // The rows are the read's, in its order, and carry no key of their own.
                // biome-ignore lint/suspicious/noArrayIndexKey: a block's rows never reorder.
                key={index}
              >
                {columns.map((column, at) => {
                  const value = row[column];
                  const issue =
                    at === 0 && typeof value === "string" && ISSUE_KEY.test(value)
                      ? value
                      : undefined;
                  return (
                    <td className={blockCellClassName} key={column}>
                      {issue === undefined ? (
                        columnText(column, value)
                      ) : (
                        <a
                          className="inline-flex max-w-full"
                          data-testid="going-block-item"
                          href={buildReferencePath({ key: issue, kind: "issue" })}
                        >
                          <ItemChip issue={issue} />
                        </a>
                      )}
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {all ? null : (
        <button
          className={`text-xs ${linkText} ${linkHoverText}`}
          data-testid="going-block-show-all"
          onClick={() => (onShowAll === undefined ? setOpen(true) : onShowAll())}
          type="button"
        >
          Show all {block.rows.length} ›
        </button>
      )}
    </div>
  );
}
