import type { ReactNode } from "react";
import { Link } from "react-router-dom";

import {
  borderDefault,
  card,
  rowHoverBg,
  textMutedOnCanvas,
  textPrimaryOnCanvas,
} from "../../../theme/classes";
import { buildMachinePath } from "../../refs/routes";
import { type MachineSummary, sessionCountSentence } from "../machine-model";

/** The machines a live session names, one row each, each a link to its machine's page. */
export function MachineList({ machines }: { machines: readonly MachineSummary[] }): ReactNode {
  return (
    <ul className="space-y-2" data-testid="machine-list">
      {machines.map((machine) => (
        <li
          className={`rounded-lg border ${card} ${borderDefault} ${rowHoverBg}`}
          data-in-tmux={machine.inTmux}
          data-machine={machine.host}
          data-sessions={machine.sessions}
          data-testid="machine-row"
          key={machine.host}
        >
          {/* One line from `md`: the machine at the left, its counts at the right, so the
              counts read down a column. Below it the counts take a line under the name, and
              each line stretches to the row: below `xl` every link centres its items
              (styles.css's touch rule), which would centre both lines otherwise. */}
          <Link
            className="flex w-full flex-col gap-x-4 rounded-lg px-4 py-2.5 md:flex-row md:items-baseline md:justify-between"
            to={buildMachinePath(machine.host)}
          >
            <span
              className={`min-w-0 break-words text-sm font-medium max-md:self-stretch ${textPrimaryOnCanvas}`}
            >
              {machine.host}
            </span>
            <span
              className={`shrink-0 text-xs tabular-nums max-md:self-stretch ${textMutedOnCanvas}`}
            >
              {sessionCountSentence(machine)}
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}
