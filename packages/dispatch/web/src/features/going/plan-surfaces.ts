import type { GoingPlanRow } from "../../api/types";
import { argumentsOf, makesIssue } from "./plan-rows";

/**
 * Where a planned change is drawn (R4, plan.md settlement 7): on the page when the page shows its
 * item, and otherwise in the plan card. A change of an issue's field, link or claim shows on a list
 * or a board that shows the issue and on the issue's own page; a new issue shows on the list of its
 * parent - the project's issue list or board for an issue with no parent, the parent's Children tab
 * for one with a parent, and any list that shows the parent - and a change of a document's text on
 * that document's page. These five pages draw what they show, and every other page leaves the
 * whole plan to the card.
 */

/** What one page shows, which decides the plan rows it draws. */
export type PlanSurface =
  /** The project's issue list or board: its project and the issues it shows. */
  | { readonly kind: "issues"; readonly project: string; readonly issues: readonly string[] }
  /** One issue's page: the issue itself. */
  | { readonly kind: "issue"; readonly issue: string }
  /** One issue's Children tab: the issue and its children. */
  | { readonly kind: "children"; readonly issue: string; readonly children: readonly string[] }
  /** One document's page: its id, its name and slug, and who owns it. */
  | {
      readonly kind: "document";
      readonly id: string;
      readonly names: readonly string[];
      readonly owner: string;
    };

/** The pages that draw a planned change of an item they show, each by the file that mounts the
 *  drawing (`PlannedChanges`) and the kind of surface it gives it. `plan-surfaces.test.ts` holds
 *  that each of them draws the rows of its items. */
export const DRAWING_SURFACES = [
  { file: "features/project/IssuesTable.tsx", kind: "issues", page: "the issue list" },
  { file: "features/project/IssueBoard.tsx", kind: "issues", page: "the board" },
  { file: "features/issue-page/IssuePage.tsx", kind: "issue", page: "the issue page" },
  { file: "features/issue/ChildrenTab.tsx", kind: "children", page: "the issue's Children tab" },
  { file: "features/document/DocumentPage.tsx", kind: "document", page: "a document page" },
] as const satisfies readonly {
  readonly file: string;
  readonly kind: PlanSurface["kind"];
  readonly page: string;
}[];

/** The classes of change a list, a board or an issue page draws on the item it shows: a field, a
 *  link, a claim, a new issue. A message, a document change and the rest of the classes are no
 *  value of the item the page draws. */
const ITEM_CLASSES: Readonly<Record<string, true>> = {
  claim: true,
  create_plain: true,
  field: true,
  link: true,
};

/** The classes of change a document's page draws: its text, and a suggestion in it. */
const DOCUMENT_CLASSES: Readonly<Record<string, true>> = {
  create_text: true,
  suggestion: true,
  text: true,
};

/** Whether `value` is one of `keys`, as an issue key or a document name is written, whatever its
 *  case. */
function named(value: unknown, keys: readonly string[]): boolean {
  return (
    typeof value === "string" &&
    value !== "" &&
    keys.some((key) => key.toUpperCase() === value.toUpperCase())
  );
}

/** Whether `surface` draws `row`. */
export function drawsRow(surface: PlanSurface, row: GoingPlanRow): boolean {
  const args = argumentsOf(row);
  switch (surface.kind) {
    case "issues":
      if (makesIssue(row)) {
        return args.parent === undefined || args.parent === ""
          ? named(args.project, [surface.project])
          : named(args.parent, surface.issues);
      }
      return ITEM_CLASSES[row.class] === true && named(row.item || row.issue, surface.issues);
    case "issue":
      return (
        !makesIssue(row) &&
        ITEM_CLASSES[row.class] === true &&
        named(row.item || row.issue, [surface.issue])
      );
    case "children":
      return makesIssue(row)
        ? named(args.parent, [surface.issue])
        : ITEM_CLASSES[row.class] === true && named(row.item || row.issue, surface.children);
    case "document":
      return (
        DOCUMENT_CLASSES[row.class] === true &&
        (row.item === surface.id ||
          (named(args.artifact ?? args.name, surface.names) &&
            named(args.issue ?? args.project, [surface.owner])))
      );
  }
}

/** The numbers of the rows of `rows` that `surface` draws. */
export function rowsOnSurface(surface: PlanSurface, rows: readonly GoingPlanRow[]): number[] {
  return rows.filter((row) => drawsRow(surface, row)).map((row) => row.row);
}
