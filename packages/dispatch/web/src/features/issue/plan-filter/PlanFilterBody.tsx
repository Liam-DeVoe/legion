import { type ComponentProps, type ReactNode, useMemo } from "react";
import { Link } from "react-router-dom";

import { pageMutedText } from "../../../theme/classes";
import { buildReferencePath, parseNodeReference } from "../../refs/routes";
import { IssueTree } from "../IssueTree";
import { firstLineOf, nodeWords, type TreeNode } from "../issue-tree";
import { authorText, freeze, pageChrome } from "../page-structure";
import { type PlanCycle, planOrder } from "./plan-order";

/**
 * The Plan filter's body: the plan's tasks as ordered steps (dispatch://INFRA-1196 R26).
 *
 * The steps are the outline's own rows, in the order `plan-order.ts` works out: each task after
 * every task in its `needs`, each row drawing the task's words whole, the chips naming what it
 * needs, and the line naming each statement it serves, which is the parent the filter draws as
 * no row (R2, R34, the architect's ruling 1 on R1). So one kind of row is drawn on this page and
 * the Plan filter adds the order to it.
 *
 * Tasks that need each other can be given no place in that order, so they are drawn as one line
 * that lists them (R26), above the steps: a step may wait on a task of a cycle, and no step is
 * drawn above a task it needs that way.
 *
 * What the pictures draw and the model does not hold yet is drawn nowhere: a step's state, the
 * points of work, the column naming who carries a step out, and the chart of time, which needs
 * each task's size and position (dispatch://INFRA-1253 R8 and R11, dispatch://INFRA-3570 and
 * dispatch://INFRA-3577 for the owner). R26: "A step's state is drawn where the model holds one
 * and nowhere else: the model holds none today, so the page draws none".
 */

/** What the Plan filter says of its own accord: the name of the line a cycle is drawn as, and
 *  the line a plan draws where it holds tasks and no step. Each says what is so and nothing
 *  about the page (R22). */
export const PLAN_WORDS = freeze({
  /** The line that lists tasks which need each other, so no order can put one after the rest. */
  cycle: "Steps that need each other",
  /** What the outline says where every task of the plan is in a cycle, in place of the line for
   *  an issue that holds no plan task at all (`EMPTY_STATE_LINES.plan`), which such a plan
   *  disproves. */
  cycles_only: "Every task of this plan needs another.",
} as const);

export function PlanFilterBody({ root, ...tree }: ComponentProps<typeof IssueTree>): ReactNode {
  const { cycles, ordered } = useMemo(() => {
    const entries = planOrder(root.children);
    const steps = entries.flatMap((entry) => (entry.kind === "step" ? [entry.task] : []));
    return {
      cycles: entries.flatMap((entry) => (entry.kind === "cycle" ? [entry] : [])),
      ordered: {
        ...root,
        children: steps,
        held: steps.reduce((held, step) => held + step.held + 1, 0),
      },
    };
  }, [root]);
  // The page draws one tree, whatever a filter leaves in it, so a plan whose every task is in a
  // cycle draws its cycles and a tree with no row; the tree's own line for an issue that holds
  // no plan task would be untrue there, and this is the line for it.
  return (
    <>
      {cycles.map((cycle) => (
        <CycleLine cycle={cycle} key={cycle.tasks.map((one) => one.id).join(" ")} />
      ))}
      <IssueTree
        {...tree}
        emptyLine={
          ordered.children.length === 0 && cycles.length > 0
            ? PLAN_WORDS.cycles_only
            : tree.emptyLine
        }
        root={ordered}
      />
    </>
  );
}

/** One cycle, as one line that lists the tasks in it, each a link that lands on that task
 *  (R26). The line is the prototype's own line above the plan (`.psum`, proto-v3.html :490). */
function CycleLine({ cycle }: { cycle: PlanCycle }): ReactNode {
  return (
    <p
      className={`mb-2.5 flex min-w-0 flex-wrap items-baseline gap-x-2 text-sm leading-5 ${pageMutedText}`}
      data-plan-cycle=""
    >
      <span {...pageChrome}>{PLAN_WORDS.cycle}</span>
      {cycle.tasks.map((task) => (
        <TaskLink key={task.id} task={task} />
      ))}
    </p>
  );
}

/** One task of a cycle, by its own first line (R1's first-line rule), as the link a reader
 *  follows to it: a task of a cycle is drawn in no step, and this is where they reach it. */
function TaskLink({ task }: { task: TreeNode }): ReactNode {
  const route = task.reference === undefined ? undefined : parseNodeReference(task.reference);
  const words = task.text === "" ? task.id : firstLineOf(nodeWords(task)).line;
  return route === undefined ? (
    <span className="min-w-0 break-words" {...authorText}>
      {words}
    </span>
  ) : (
    <Link
      className={`dispatch-inline-link min-w-0 break-words underline decoration-page-hair underline-offset-[3px] ${pageMutedText}`}
      to={buildReferencePath(route)}
      {...authorText}
    >
      {words}
    </Link>
  );
}
