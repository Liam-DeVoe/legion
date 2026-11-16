import { useQueries } from "@tanstack/react-query";
import {
  type ComponentProps,
  memo,
  type ReactNode,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Link, useNavigate } from "react-router-dom";

import { artifactBlocksQuery } from "../../../api/queries";

import {
  focusVisibleRing,
  pageHairBorder,
  pageInkBorder,
  pageInkFill,
  pageInkText,
  pageLineBorder,
  pageMutedText,
  pagePaperBg,
  pageQuietText,
} from "../../../theme/classes";
import { buildReferencePath, parseNodeReference } from "../../refs/routes";
import { useKeymap } from "../../shell/keymap";
import { conversationPath } from "../decisions-filter/decisions-parts";
import { useSetFocusedNode } from "../focused-node";
import { IssueTree } from "../IssueTree";
import { bringOnScreen } from "../identity-block";
import { documentOfNode } from "../issue-artifacts";
import { firstLineOf, nodeWords, type TreeNode } from "../issue-tree";
import { DETAILS_ATTRIBUTE } from "../margin-slot";
import { useRowOnScreen } from "../NodeRow";
import { chipWords } from "../node-row-chip";
import { authorText, pageChrome } from "../page-structure";
import { CONTROL_LABELS, NODE_TYPE_NAMES, PLAN_WORDS, PRESET_NAMES } from "../page-words";
import {
  needsChip,
  type PlanBar,
  type PlanChartRow,
  type PlanCycle,
  type PlanEntry,
  planChart,
  planOrder,
} from "./plan-order";

/**
 * The Plan filter's body (dispatch://INFRA-1196 R26, ruling 5 of build-rulings-md, the picture
 * `page-v3-plan-1440.png` and the prototype's `.plan`).
 *
 * It opens on the chart of time: one bar for each step, starting after every task it needs has
 * ended, with the lines between a step and the steps it needs, the step's words beside its bar
 * and the statements it serves at the end of its row. One control switches to the ordered
 * steps, which are the outline's own rows in the same order (`IssueTree`): each task after every
 * task in its `needs`, each row drawing the task's words, the chips naming what it needs by the
 * names part F's view gives them, and the line naming the statement it serves, which is the
 * parent the filter draws as no row (R2).
 *
 * Tasks that need each other can be given no place of their own, so the order gives the cycle
 * one place (`plan-order.ts`): the chart draws it there as one line that lists its tasks, and
 * the steps draw that line over the tree and the cycle's tasks as rows at its place, so a step
 * that waits on one of them stands after it and an address naming one lands on a row (R26).
 *
 * The order and the chart's places are the whole plan's (`plan`, the Plan view with no words
 * filter on), and the view's own tasks (`root`) choose which places are drawn: a step where its
 * task is in the view, a cycle where any of its tasks is, and under Steps the view's tasks as
 * rows in that order. So under a words filter a drawn bar starts where it does on the whole
 * plan and names, after "needs", every task it needs that the plan holds, drawn or not, and a
 * curve joins two drawn bars alone (dispatch://INFRA-3938 R2 and R4, decision 3 of its plan).
 * Ordered from the view's tasks alone, a task the filter left would stand at 0 and need nothing.
 *
 * The node the address names takes the focus once per address for the whole view, which is
 * held here rather than in the chart or the tree, since each is drawn anew at every press of
 * the switch: a press on "Chart" or "Steps" is the reader taking over, and leaves the focus on
 * the button pressed (dispatch://INFRA-3938 R6 and R7; dispatch://INFRA-1196 R11). Held by the
 * chart, the address would take the focus back from "Chart" each time the chart is drawn again.
 *
 * ## What today's reads give, and what waits
 *
 * - **The plan's own order** is the order the tasks are written in, read from the blocks of
 *   the plan document the page already holds (`useIssueTree.ts` reads them under the same key,
 *   so this costs no request): the filter's view hands the tasks over grouped by the statement
 *   each serves, which is the spec's order and not the plan's (the tester's F19).
 * - **A bar's place** is worked out here from the `needs` the plan document holds. The place
 *   the server's plan read gives each task waits on dispatch://INFRA-1253 (R8, R11).
 * - **A bar's length** is the task's size, and the `plan_task` block declares none today, so
 *   every bar is one unit long and its row says "No size" (R26). Sizes, and with them the
 *   points of work and the longest path the picture names, wait on dispatch://INFRA-1253; the
 *   axis draws its units as lines and no numbers, since a number would be a size nobody
 *   recorded.
 * - **A step's state** is drawn nowhere: "the model holds none today, so the page draws none,
 *   and the states the pictures show are made up" (R26). dispatch://INFRA-1253 R7 and
 *   dispatch://INFRA-1950 R5.
 * - **Who carries a step out**, and a parent's children's tasks in its own Plan filter, wait on
 *   dispatch://INFRA-3577 and dispatch://INFRA-1253.
 */
export function PlanFilterBody({
  plan,
  root,
  ...tree
}: ComponentProps<typeof IssueTree> & {
  /** The whole plan's tasks, as the Plan view gives them with no words filter on
   *  (`slots/view-plan.tsx`): the order and the chart's places are worked out from them, and
   *  `root`, the view, chooses which of those places are drawn (dispatch://INFRA-3938 R4). */
  plan: readonly TreeNode[];
}): ReactNode {
  // Which of the two the reader is looking at: the chart, which the filter opens on, or the
  // ordered steps.
  const [steps, setSteps] = useState(false);
  const { artifacts } = tree;
  // The documents the plan's tasks are blocks of, and each one's block ids in the order it is
  // written: every task of the plan, so a words filter changes no task's place (R2).
  const documentIds = useMemo(
    () => [
      ...new Set(
        plan.flatMap((node) => {
          const held = documentOfNode(node.id, artifacts);
          return held === undefined ? [] : [held.artifact.id];
        })
      ),
    ],
    [artifacts, plan]
  );
  const written = useQueries({
    combine: blockIdsOf,
    queries: documentIds.map((id) => artifactBlocksQuery(id)),
  });
  const entries = useMemo(() => {
    const places = new Map<string, number>();
    let place = 0;
    documentIds.forEach((id, rank) => {
      for (const block of written[rank] ?? []) {
        places.set(`${id} ${block}`, place);
        place += 1;
      }
    });
    const placeOf = (node: TreeNode): number => {
      const held = documentOfNode(node.id, artifacts);
      const place =
        held === undefined ? undefined : places.get(`${held.artifact.id} ${held.blockId}`);
      return place ?? Number.POSITIVE_INFINITY;
    };
    // A task the page holds no place for keeps the place the view gave it, after the rest.
    const tasks = plan
      .map((node, index) => ({ index, node, place: placeOf(node) }))
      .sort((one, other) =>
        one.place === other.place ? one.index - other.index : one.place - other.place
      )
      .map((held) => held.node);
    return planOrder(tasks);
  }, [artifacts, documentIds, plan, written]);
  // The view's tasks by id, each as the view gives it and once, as the order holds it: the
  // places drawn are those holding one of them (R4).
  const inView = useMemo(() => {
    const held = new Map<string, TreeNode>();
    for (const node of root.children) if (!held.has(node.id)) held.set(node.id, node);
    return held;
  }, [root]);
  const cycles = useMemo(
    () =>
      entries.flatMap((entry) =>
        entry.kind === "cycle" && entry.tasks.some((task) => inView.has(task.id)) ? [entry] : []
      ),
    [entries, inView]
  );
  // The steps, in the plan's order, with the tasks of a cycle at the cycle's place: the view's
  // tasks alone (R4). Each row is its task as part F's view gives it: its `needs` chips carry
  // F's names - the needed task's first line, and the page's word for its kind where that task
  // holds no words - which are the names the chart draws for the same needs (`needsChip`), so
  // the switch changes the drawing alone (decision 66 of dispatch://INFRA-1196;
  // dispatch://INFRA-3938 R3, R4 and R6).
  const ordered = useMemo(() => {
    const rows = entries
      .flatMap((entry) => (entry.kind === "step" ? [entry.task] : entry.tasks))
      .flatMap((task) => {
        const node = inView.get(task.id);
        return node === undefined ? [] : [node];
      });
    return { ...root, children: rows, held: rows.reduce((held, row) => held + row.held + 1, 0) };
  }, [entries, inView, root]);
  // The node the address names, and the one the view has put the focus on or the reader went
  // past with a press of the switch: the address's node takes the focus while the two differ.
  const revealed = tree.reveal?.at(-1)?.id;
  const [taken, setTaken] = useState<string | undefined>(undefined);
  const landing = revealed !== undefined && revealed !== taken;
  const show = (next: boolean): void => {
    setTaken(revealed);
    setSteps(next);
  };
  const view = (pressed: boolean): string =>
    `rounded-full border px-3 py-0.5 text-sm leading-5 font-semibold ${focusVisibleRing} ${
      pressed
        ? `${pageInkFill} ${pageInkBorder}`
        : `${pageHairBorder} ${pagePaperBg} ${pageMutedText}`
    }`;
  const empty = root.children.length === 0;
  return (
    <div
      className="mt-3 mb-[120px] flex w-full min-w-0 flex-col max-[761px]:mt-0"
      data-plan-body=""
    >
      {/* R26's one control: the chart of time, or the ordered steps. A plan with no task has
          nothing to switch between. The box is 22 px under the view row, and 10 px at 760 px
          and below, as the prototype's `.plan` is (dispatch://INFRA-3938 R1 and its Sizes): D's
          column already stands 10 px under the row (`pt-2.5` of `[data-testid="issue-column"]`),
          so the box adds 12 px, and nothing at 760 px and below. The 120 px above the page's foot
          are the box's, so the steps keep them as the chart does. */}
      {empty ? null : (
        // biome-ignore lint/a11y/useSemanticElements: two toggles of one control; a fieldset would draw a frame the picture has none of.
        <div
          aria-label={PRESET_NAMES.plan}
          className="mb-2.5 flex min-w-0 flex-wrap gap-1.5"
          data-plan-switch=""
          role="group"
        >
          <button
            aria-pressed={!steps}
            className={view(!steps)}
            data-plan-view="chart"
            onClick={() => show(false)}
            type="button"
            {...pageChrome}
          >
            {PLAN_WORDS.chart}
          </button>
          <button
            aria-pressed={steps}
            className={view(steps)}
            data-plan-view="steps"
            onClick={() => show(true)}
            type="button"
            {...pageChrome}
          >
            {PLAN_WORDS.steps}
          </button>
        </div>
      )}
      {steps || empty ? (
        <>
          {cycles.map((cycle) => (
            <CycleLine cycle={cycle} key={cycleIds(cycle.tasks)} />
          ))}
          {/* The tree takes the address's node only while the view has not: drawn after a
              press of the switch, it leaves the focus on the button pressed (R6). */}
          <IssueTree {...tree} focusReveal={tree.focusReveal && landing} root={ordered} />
        </>
      ) : (
        <PlanChart
          entries={entries}
          holdsEscape={tree.holdsEscape}
          inView={inView}
          land={landing ? revealed : undefined}
          onLanded={setTaken}
          reveal={revealed}
        />
      )}
    </div>
  );
}

/** Each document's block ids in the order they are written, or undefined while its read is
 *  out. It is one function for every render, so the reads' answer is one value until a read
 *  changes (`useQueries`' `combine`). */
function blockIdsOf(
  answers: readonly { readonly data?: readonly { readonly id: string }[] | undefined }[]
): readonly (readonly string[] | undefined)[] {
  return answers.map((answer) => answer.data?.map((block) => block.id));
}

/** The key of a cycle's place: its tasks' ids, one space between two. */
function cycleIds(tasks: readonly TreeNode[]): string {
  return tasks.map((one) => one.id).join(" ");
}

/** One cycle, as one line that lists the tasks in it, each a link that lands on that task
 *  (R26). The line is the prototype's own line above the plan (`.psum`, proto-v3.html :490).
 *
 *  In the chart the line is a row of the tree, which is one stop of Tab, so its links take
 *  none and `f` reaches them. Above the steps it is outside the tree, so its links take Tab as
 *  any link does: a task of a cycle is reached by the keyboard in both drawings
 *  (dispatch://INFRA-3938 R7, dispatch://INFRA-1196 R11). */
function CycleLine({ cycle, inChart = false }: { cycle: PlanCycle; inChart?: boolean }): ReactNode {
  return (
    <p
      className={`mb-2.5 flex min-w-0 flex-wrap items-baseline gap-x-2 text-sm leading-5 ${pageMutedText}`}
      data-plan-cycle=""
    >
      <span {...pageChrome}>{PLAN_WORDS.cycle}</span>
      {cycle.tasks.map((task) => (
        <TaskLink inChart={inChart} key={task.id} task={task} />
      ))}
    </p>
  );
}

/** The task's own first line, which is the one line the page draws for it wherever it draws
 *  one (R1, R26: "each bar labelled with the task's first line"), from the server's reading of
 *  its text where the page holds one (`nodeWords`). A task that holds no words is named by the
 *  page's word for its kind, and never by its id (R22). The marks say whose words the line is,
 *  and F's name of a `needs` chip pointing at the task: the author's, or the page's own for a
 *  task that holds none, since both are then its kind's word. */
function taskLine(task: TreeNode): {
  readonly marks: Readonly<Record<string, string>>;
  readonly words: string;
} {
  const own = firstLineOf(nodeWords(task)).line;
  return own === ""
    ? { marks: pageChrome, words: NODE_TYPE_NAMES[task.type] }
    : { marks: authorText, words: own };
}

/** One task of a cycle, by its own first line, as the link a reader follows to it: in no tab
 *  order of its own where the line is a row of the chart (`CycleLine`). */
function TaskLink({ inChart, task }: { inChart: boolean; task: TreeNode }): ReactNode {
  const route = task.reference === undefined ? undefined : parseNodeReference(task.reference);
  const line = taskLine(task);
  return route === undefined ? (
    <span className="min-w-0 break-words" {...line.marks}>
      {line.words}
    </span>
  ) : (
    <Link
      className={`dispatch-inline-link min-w-0 break-words underline decoration-page-hair underline-offset-[3px] ${pageMutedText}`}
      {...(inChart ? { tabIndex: -1 } : {})}
      to={buildReferencePath(route)}
      {...line.marks}
    >
      {line.words}
    </Link>
  );
}

/** The chart's three columns, as the prototype's `.prow` divides them: the step, the time, and
 *  what the step serves; at 1240 px and below the checkpoint's narrower outer columns
 *  (dispatch://INFRA-3938 R5). At 760 px and below the head row is not drawn and each row is a
 *  card that lays its parts out itself (`BarRow`, `.pitem`). "And below" is one pixel over the
 *  width: Tailwind writes an arbitrary `max-[N]:` as `(width < N)`, so `max-[1241px]:` and
 *  `max-[761px]:` take 1240 px and 760 px themselves, as the header's `(max-width: 760px)` does
 *  (decision 72 of dispatch://INFRA-1196; `NodeRow.tsx`'s `NODE_ROW_LAYOUT`). */
const CHART_COLUMNS =
  "grid min-w-0 grid-cols-[330px_minmax(0,1fr)_320px] max-[1241px]:grid-cols-[260px_minmax(0,1fr)_220px]";

/** A column heading, in the prototype's `.cap`. */
const CAP = `text-xs leading-4 font-semibold tracking-[0.08em] uppercase ${pageQuietText}`;

/** The row the reader is on, a step's or a cycle's, as the prototype's `.prow.focused` draws
 *  it, and at 760 px and below its card as `.pitem.focused` does: a ring of ink outside a gap of
 *  paper, on the card's own paper, so the mark is not the tint alone (dispatch://INFRA-3938 R7). */
const FOCUSED =
  "bg-page-step-tint shadow-[inset_3px_0_0_var(--color-page-step)] max-[761px]:bg-page-paper max-[761px]:shadow-[0_0_0_2px_var(--color-page-paper),0_0_0_4px_var(--color-page-ink)]";

/** Every row of the chart, a step's and a cycle's, in the order it is drawn. */
const CHART_ROW = "[data-plan-bar-row], [data-plan-cycle-row]";

/** What the reader does that ends the hold of the address's row on screen: a key, a press, a
 *  wheel and a touch, as the outline's own list (`IssueTree.tsx`'s `READER_ACTS`, which that
 *  file keeps to itself). */
const READER_ACTS = ["keydown", "pointerdown", "wheel", "touchstart"] as const;

/** One place the chart draws: its row, the key its focus and its tab stop are held under, and
 *  the task it stands for, which is the step's own, or for a cycle the task its row gives to
 *  Details and to Enter. */
interface DrawnPlace {
  readonly key: string;
  readonly row: PlanChartRow;
  readonly task: TreeNode;
}

/**
 * The chart of time (R26, the prototype's `.chart`): one row per place in the plan's order. A
 * step's row draws its first line and the steps it needs, its bar, and the statements it
 * serves; a cycle's row lists the tasks that need each other. The lines between a bar and the
 * bars it needs are drawn over the rows from where the bars stand.
 *
 * The places and the axis are the whole plan's (`entries`), and the chart draws those that hold
 * a task of the view (`inView`): a bar a words filter leaves starts where it does on the whole
 * plan, and its curves are drawn to the bars drawn beside it (dispatch://INFRA-3938 R4).
 *
 * It is the outline the page draws under this view, so it is one tree named as the outline is,
 * and its rows are its items: the outline's own keys work it, under the ids and the labels the
 * outline registers them with (`useTreeKeymap.ts`), so `?` names them here too. The arrows and
 * `j`/`k` reach the bars in order and pass over a cycle's row, Home and End the first and the
 * last bar, `f` steps through the focused row's links, Escape gives a row back from its link and
 * leaves the chart from the row, and Enter goes to the row's task in the conversation, where it
 * is edited (dispatch://INFRA-3938 R7). The focus on a row puts its task in Details.
 *
 * A cycle's row takes the focus too, as a bar's does: a press on it, an address naming one of
 * its tasks, and the Tab stop where the chart draws no bar (decision 2 of dispatch://INFRA-3938's
 * plan). It stands for the task the address names, else its first, and from it the arrows go
 * to the step after it and the step before it. Were it to take no focus, an address naming a
 * task of a cycle would focus nothing and a plan whose every task is in a cycle would have no
 * Tab stop.
 *
 * The one Tab stop is the row the reader last stood on, else the first bar's, else the first
 * cycle's. Each row places itself on screen by the outline's one hook (`useRowOnScreen`), so the
 * keys move the focus without the browser's own scroll, which knows nothing of the band that
 * stays at the top of the page (R7).
 */
function PlanChart({
  entries,
  holdsEscape,
  inView,
  land,
  onLanded,
  reveal,
}: {
  entries: readonly PlanEntry[];
  /** Whether the page takes Escape from a row before the chart is left: the words filter's,
   *  which comes first in the key table's order (`slots/view-plan.tsx`, part N). */
  holdsEscape: boolean;
  /** The view's tasks by id: a place is drawn where it holds one of them. */
  inView: ReadonlyMap<string, unknown>;
  /** The node the address names while it is the view's to put the focus on: once per address
   *  for the whole view, and not past a press of the switch (`PlanFilterBody`), since the chart
   *  is drawn anew at every press. */
  land: string | undefined;
  /** Says the address's node has taken the focus. */
  onLanded: (id: string) => void;
  /** The node the address names: the task a cycle's row stands for where it is one of its own. */
  reveal: string | undefined;
}): ReactNode {
  const navigate = useNavigate();
  const setFocusedNode = useSetFocusedNode();
  const chart = useRef<HTMLDivElement | null>(null);
  const { end, rows: places } = useMemo(() => planChart(entries), [entries]);
  const rows = useMemo(
    () =>
      places.flatMap((row): DrawnPlace[] => {
        if (row.kind === "bar") {
          return inView.has(row.task.id) ? [{ key: row.task.id, row, task: row.task }] : [];
        }
        const task = row.tasks.find((one) => one.id === reveal) ?? row.tasks[0];
        return task === undefined || !row.tasks.some((one) => inView.has(one.id))
          ? []
          : [{ key: cycleIds(row.tasks), row, task }];
      }),
    [inView, places, reveal]
  );
  const bars = useMemo(
    () => places.flatMap((row) => (row.kind === "bar" && inView.has(row.task.id) ? [row] : [])),
    [inView, places]
  );
  const [focused, setFocused] = useState<string | undefined>(undefined);
  // Where the chart's one tab stop rests, which outlives the focus leaving it (R9).
  const [stop, setStop] = useState<string | undefined>(undefined);
  const tabStop = rows.some((place) => place.key === stop)
    ? stop
    : (rows.find((place) => place.row.kind === "bar") ?? rows[0])?.key;

  const takeFocus = useCallback(
    (key: string, task: TreeNode) => {
      setFocused(key);
      setStop(key);
      setFocusedNode({
        ...(task.author === undefined ? {} : { author: task.author }),
        id: task.id,
        ...(task.reference === undefined ? {} : { reference: task.reference }),
        title: taskLine(task).words,
        type: task.type,
      });
    },
    [setFocusedNode]
  );
  // An address that names a task puts the focus on its row, a cycle's included, and holds the
  // row on screen until the reader acts, as the outline's address does (`IssueTree.tsx`): the
  // rows around it are still arriving and push it down the page. A row already inside the
  // viewport is focused where it stands; one outside it is brought in by the focus, then placed
  // by the row's own rule (`useRowOnScreen`). A key, a press, a wheel or a touch ends the hold,
  // and so does a focus that has moved off the row.
  const landingHold = useRef<AbortController | undefined>(undefined);
  useEffect(() => () => landingHold.current?.abort(), []);
  useEffect(() => {
    if (land === undefined || !rows.some((place) => place.task.id === land)) return;
    const row = chart.current?.querySelector<HTMLElement>(
      `[data-node-row][data-node-id="${CSS.escape(land)}"]`
    );
    if (row == null) return;
    onLanded(land);
    const box = row.getBoundingClientRect();
    row.focus({ preventScroll: box.top >= 0 && box.bottom <= window.innerHeight });
    landingHold.current?.abort();
    const hold = new AbortController();
    landingHold.current = hold;
    for (const type of READER_ACTS) {
      window.addEventListener(type, () => hold.abort(), { capture: true, signal: hold.signal });
    }
    if (typeof ResizeObserver === "undefined") return;
    const grown = new ResizeObserver(() => {
      if (document.activeElement === row) bringOnScreen(row);
      else hold.abort();
    });
    grown.observe(document.body);
    hold.signal.addEventListener("abort", () => grown.disconnect());
  }, [land, onLanded, rows]);

  // The lines between a bar and the bars it needs, from where the browser laid the bars out:
  // from the end of the needed bar to the start of the one that needs it (the prototype's
  // `drawPlanLinks`). They are read again whenever the chart's box changes. A need whose bar is
  // not drawn - a cycle's task, or a step the words filter left - has no line.
  const [links, setLinks] = useState<{
    readonly height: number;
    readonly paths: readonly { readonly d: string; readonly of: readonly string[] }[];
    readonly width: number;
  }>({ height: 0, paths: [], width: 0 });
  useLayoutEffect(() => {
    const element = chart.current;
    if (element === null) return;
    const measure = () => {
      const frame = element.getBoundingClientRect();
      // Where each drawn bar stands, read once in a pass for every line that meets it.
      const boxes = new Map<string, { left: number; right: number; y: number }>();
      for (const drawn of element.querySelectorAll("[data-plan-bar]")) {
        const rect = drawn.getBoundingClientRect();
        boxes.set(drawn.getAttribute("data-plan-bar") ?? "", {
          left: rect.left - frame.left,
          right: rect.right - frame.left,
          y: rect.top - frame.top + rect.height / 2,
        });
      }
      const paths = bars.flatMap((bar) => {
        const to = boxes.get(bar.task.id);
        if (to === undefined) return [];
        return bar.needs.flatMap((needed) => {
          const from = boxes.get(needed.id);
          if (from === undefined) return [];
          const bend = Math.max(18, (to.left - from.right) / 2);
          return [
            {
              d: `M${from.right} ${from.y} C${from.right + bend} ${from.y} ${to.left - bend} ${to.y} ${to.left - 1} ${to.y}`,
              of: [needed.id, bar.task.id],
            },
          ];
        });
      });
      // A pass that finds every line where it was keeps the lines held, and draws nothing anew.
      setLinks((previous) =>
        previous.height === frame.height &&
        previous.width === frame.width &&
        previous.paths.length === paths.length &&
        paths.every((path, index) => {
          const held = previous.paths[index];
          return (
            held !== undefined &&
            held.d === path.d &&
            held.of.length === path.of.length &&
            held.of.every((id, at) => id === path.of[at])
          );
        })
          ? previous
          : { height: frame.height, paths, width: frame.width }
      );
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const resized = new ResizeObserver(measure);
    resized.observe(element);
    return () => resized.disconnect();
  }, [bars]);

  /** The chart's rows as the browser holds them, bars and cycles, in the order of `rows`. */
  const items = (): HTMLElement[] => [
    ...(chart.current?.querySelectorAll<HTMLElement>(CHART_ROW) ?? []),
  ];
  /** Which of `held` the reader is on, a link inside it included, or -1 when on none. */
  const at = (held: readonly HTMLElement[]): number => {
    const active = document.activeElement;
    return active === null ? -1 : held.findIndex((row) => row === active || row.contains(active));
  };
  /** Whether the reader is reading: on the page itself, or on a row of the chart. */
  const reading = (): boolean =>
    document.activeElement === null ||
    document.activeElement === document.body ||
    at(items()) !== -1;
  /** The first bar's row of `held` past `from`, going `by` rows at a time, or -1 where there is
   *  none: the keys that move the reader pass over a cycle's row (R7). */
  const barFrom = (held: readonly HTMLElement[], from: number, by: 1 | -1): number => {
    for (let index = from + by; index >= 0 && index < held.length; index += by) {
      if (held[index]?.hasAttribute("data-plan-bar-row") === true) return index;
    }
    return -1;
  };
  /** Moves the focus to the `index`th of `held`, where the row's own hook then places it on
   *  screen, clear of the band that stays at the top (`useRowOnScreen`, R7). */
  const moveTo = (held: readonly HTMLElement[], index: number): void => {
    held[index]?.focus({ preventScroll: true });
  };
  useKeymap("issue", [
    {
      id: "tree-next",
      keys: ["j", "ArrowDown"],
      label: "Move to the next row",
      palette: false,
      run: () => {
        const held = items();
        moveTo(held, barFrom(held, at(held), 1));
      },
      when: () => reading() && items().length > 0,
    },
    {
      // Up from the page itself goes to the first bar, as down does.
      id: "tree-previous",
      keys: ["k", "ArrowUp"],
      label: "Move to the previous row",
      palette: false,
      run: () => {
        const held = items();
        const on = at(held);
        moveTo(held, on === -1 ? barFrom(held, -1, 1) : barFrom(held, on, -1));
      },
      when: () => reading() && items().length > 0,
    },
    {
      id: "tree-first",
      keys: "Home",
      label: "Move to the first row",
      palette: false,
      run: () => {
        const held = items();
        moveTo(held, barFrom(held, -1, 1));
      },
      when: () => at(items()) !== -1,
    },
    {
      id: "tree-last",
      keys: "End",
      label: "Move to the last row",
      palette: false,
      run: () => {
        const held = items();
        moveTo(held, barFrom(held, held.length, -1));
      },
      when: () => at(items()) !== -1,
    },
    {
      // The statements a step serves and the tasks a cycle lists are links, and the chart is
      // one tab stop (R9), so a key reaches them; Enter on the link it left the focus on is the
      // browser's own.
      id: "tree-link",
      keys: "f",
      label: "Move to the next link on the focused row",
      palette: false,
      run: () => {
        const held = items();
        const links = [...(held[at(held)]?.querySelectorAll<HTMLElement>("a[href]") ?? [])];
        const on = links.indexOf(document.activeElement as HTMLElement);
        links[(on + 1) % links.length]?.focus({ preventScroll: true });
      },
      when: () => {
        const held = items();
        return (held[at(held)]?.querySelector("a[href]") ?? null) !== null;
      },
    },
    {
      // Enter on a row goes to its task in the conversation, where it is edited: the chart
      // draws each step and edits none. A cycle's row goes to the task it stands for. On a link
      // inside the row Enter is the link's own.
      id: "tree-edit",
      keys: "Enter",
      label: "Go to the focused step in the conversation",
      palette: false,
      run: () => {
        const task = rows[items().indexOf(document.activeElement as HTMLElement)]?.task;
        const path = task === undefined ? undefined : conversationPath(task);
        if (path !== undefined) navigate(path);
      },
      when: () => items().includes(document.activeElement as HTMLElement),
    },
    {
      // From a link the reader reached inside a row, Escape gives the row back, since the row
      // is where the keys that move through the chart are; from the row itself it leaves the
      // chart, once the page has cleared its words filter, which comes first in the key table's
      // order. The outline's own Escape, under its id (`useTreeKeymap.ts`'s `tree-leave`).
      id: "tree-leave",
      keys: "Escape",
      label: "Leave the control, then the tree",
      palette: false,
      run: () => {
        const held = items();
        const active = document.activeElement as HTMLElement | null;
        if (active !== null && held.includes(active)) {
          active.blur();
          return;
        }
        held[at(held)]?.focus({ preventScroll: true });
      },
      when: () => {
        const held = items();
        const onRow = held.includes(document.activeElement as HTMLElement);
        return at(held) !== -1 && !(holdsEscape && onRow);
      },
    },
  ]);

  return (
    <div
      className={`relative min-w-0 rounded-[10px] border pb-2 max-[761px]:rounded-none max-[761px]:border-0 max-[761px]:pb-0 ${pageLineBorder} ${pagePaperBg}`}
      data-plan-chart=""
      ref={chart}
    >
      {/* The head row's three columns are "Steps", the axis and "Serves" (dispatch://INFRA-3938
          R4): the axis is drawn in a cell of its own, as tall as the row, which holds it as a
          bar's track holds its own, so "Serves" stands over the statements' column and every
          line of the axis inside the time's column. Drawn straight into the grid, the axis would
          be no cell of it: "Serves" would take the second column and the lines would span the
          chart. */}
      <div
        className={`${CHART_COLUMNS} min-h-10 items-center max-[761px]:hidden`}
        data-plan-head=""
      >
        <span className={`px-3.5 ${CAP}`} {...pageChrome}>
          {PLAN_WORDS.steps}
        </span>
        <div className="relative min-w-0 self-stretch">
          <Units end={end} />
        </div>
        <span className={`px-3.5 ${CAP}`} {...pageChrome}>
          {PLAN_WORDS.serves}
        </span>
      </div>
      <div
        aria-label={CONTROL_LABELS.tree}
        className="min-w-0"
        onBlur={(event) => {
          const to = event.relatedTarget;
          if (to instanceof Node && event.currentTarget.contains(to)) return;
          setFocused(undefined);
          if (to instanceof Element && to.closest(`[${DETAILS_ATTRIBUTE}]`) !== null) return;
          setFocusedNode(undefined);
        }}
        role="tree"
      >
        {rows.map((place) =>
          place.row.kind === "cycle" ? (
            <CycleRow
              cycle={place.row}
              focused={focused === place.key}
              key={place.key}
              onFocus={() => takeFocus(place.key, place.task)}
              tabStop={tabStop === place.key}
              task={place.task}
            />
          ) : (
            <BarRow
              bar={place.row}
              end={end}
              focused={focused === place.key}
              key={place.key}
              onFocus={() => takeFocus(place.key, place.task)}
              tabStop={tabStop === place.key}
            />
          )
        )}
      </div>
      {/* Decoration over the rows: the words on each row say what a step needs. The lines of
          the step the reader is on are drawn heavier in the step's colour, as the prototype's
          are, and the rest in the quiet grey: the prototype's ink marks its longest path, which
          today's model holds no sizes to find. */}
      <svg
        aria-hidden="true"
        className="pointer-events-none absolute top-0 left-0 overflow-visible max-[761px]:hidden"
        data-plan-links=""
        height={links.height}
        width={links.width}
      >
        {links.paths.map((path) => {
          const mine = focused !== undefined && path.of.includes(focused);
          return (
            <path
              d={path.d}
              data-plan-link={path.of.join(" ")}
              fill="none"
              key={path.of.join(" ")}
              stroke={mine ? "var(--color-page-step)" : "var(--color-page-quiet)"}
              strokeWidth={mine ? 2 : 1.4}
            />
          );
        })}
      </svg>
    </div>
  );
}

/**
 * One cycle's row of the chart: R3's line for the tasks that need each other, and a row the
 * focus stands on as it does on a step's (decision 2 of dispatch://INFRA-3938's plan). It stands
 * for `task` - the task the address names where it is one of the cycle's, else its first - which
 * is its node id, so the page's `[data-node-row]` readers (D's `t p`, Details) reach it as any
 * row, and which it gives to Details when it takes the focus. `data-plan-cycle-row` holds every
 * task of it. It places itself on screen as every row does (`useRowOnScreen`, R7), and its
 * focus is marked as a step's is, not by colour alone.
 */
function CycleRow({
  cycle,
  focused,
  onFocus,
  tabStop,
  task,
}: {
  cycle: PlanCycle;
  focused: boolean;
  onFocus: () => void;
  tabStop: boolean;
  task: TreeNode;
}): ReactNode {
  const ref = useRef<HTMLDivElement | null>(null);
  const press = useRowOnScreen(ref, focused);
  return (
    <div
      aria-level={1}
      className={`border-t px-3.5 pt-2 outline-none max-[761px]:border-0 max-[761px]:px-0 ${pageLineBorder} ${
        focused ? FOCUSED : ""
      }`}
      data-node-id={task.id}
      data-node-row=""
      data-node-type={task.type}
      data-plan-cycle-row={cycleIds(cycle.tasks)}
      onFocus={(event) => {
        if (event.target === event.currentTarget) onFocus();
      }}
      onPointerDown={press}
      ref={ref}
      role="treeitem"
      tabIndex={tabStop ? 0 : -1}
    >
      <CycleLine cycle={cycle} inChart />
    </div>
  );
}

/** The axis: one faint line per unit of the chart (the prototype's `.grid-l`). It draws no
 *  number: a unit is a size, and today's plan holds none (R26). The last line is drawn against
 *  the column's right edge, so every line stands inside the column that holds the axis
 *  (dispatch://INFRA-3938 R4): at `left: 100%` it would be one pixel past it. */
const Units = memo(function Units({ end }: { end: number }): ReactNode {
  const unit = 100 / end;
  return (
    <span
      aria-hidden="true"
      className="absolute inset-0 block max-[761px]:hidden"
      data-plan-axis=""
    >
      {Array.from({ length: end + 1 }, (_unused, tick) => (
        <i
          className="absolute inset-y-0 w-px bg-page-line"
          // biome-ignore lint/suspicious/noArrayIndexKey: a line is its unit and nothing else.
          key={tick}
          style={tick === end ? { right: 0 } : { left: `${tick * unit}%` }}
        />
      ))}
    </span>
  );
});

/**
 * One step of the chart (the prototype's `.prow`, and its `.pitem` card at 760 px and below):
 * its first line, that the model holds no size for it and the steps it needs; its bar; and the
 * statements it serves. The row's name is what it draws, which begins with the task's first
 * line, so each bar is labelled with it (R26). The bar is the step's plain tint inside a dashed
 * border: the prototype's hatch is its legend's "Not started", a state today's model holds
 * none of, and the dash says the length is no size anybody wrote.
 *
 * A need is named by the task's own `needs` chip for it (`needsChip`), as part F's view names
 * it and the steps' row draws it (decision 66 of dispatch://INFRA-1196, dispatch://INFRA-3938
 * R3 and R4), in the needed task's marks: the author's words, or the page's own where that task
 * holds none, since F names it then by its kind's word.
 *
 * At 761 px and above the row is the chart's three columns. At 760 px and below it is one card
 * that draws, from the top, the task's first line, the track with the bar at its place and
 * length, and one line, wrapping where it must, with "No size", the needs and the statements
 * (dispatch://INFRA-3938 R5). Each part is drawn once, in the order the row's name reads them:
 * the card is a wrapping flex box, the columns' own boxes give their parts to it
 * (`display: contents`), and the first line and the track each take a line of their own ahead
 * of the rest (`order`), so the track, which says nothing to a screen reader, is the one part
 * drawn out of the document's order. The track stands 8 px under the first line and over the
 * line under it: 3 px of its own margin and the card's 5 px between two lines, which is also
 * what parts the chips' lines where they wrap.
 *
 * The focused row places itself on screen, clear of the band that stays at the top, by the
 * outline's one hook, and a press on it is left where the reader pressed (`useRowOnScreen`,
 * dispatch://INFRA-3938 R7).
 */
function BarRow({
  bar,
  end,
  focused,
  onFocus,
  tabStop,
}: {
  bar: PlanBar;
  /** How many units the chart's axis spans. */
  end: number;
  focused: boolean;
  onFocus: () => void;
  tabStop: boolean;
}): ReactNode {
  const task = bar.task;
  const line = taskLine(task);
  // How wide one unit of the chart is, as a percentage of its column.
  const unit = 100 / end;
  const ref = useRef<HTMLDivElement | null>(null);
  const press = useRowOnScreen(ref, focused);
  const serves = [
    ...(task.parentLine === undefined || task.parentLine.type !== "requirement"
      ? []
      : [{ line: task.parentLine.line, reference: task.parentLine.reference }]),
    ...task.alsoUnder
      .filter((chip) => chip.name !== "")
      .map((chip) => ({ line: chip.name, reference: chip.ref })),
  ];
  return (
    <div
      aria-level={1}
      className={`${CHART_COLUMNS} min-h-[54px] items-center border-t outline-none max-[761px]:my-2 max-[761px]:flex max-[761px]:flex-wrap max-[761px]:gap-x-1.5 max-[761px]:gap-y-[5px] max-[761px]:rounded-lg max-[761px]:border max-[761px]:border-l-4 max-[761px]:border-l-page-step max-[761px]:px-3 max-[761px]:py-2.5 ${pageLineBorder} ${
        focused ? FOCUSED : ""
      }`}
      data-node-id={task.id}
      data-node-row=""
      data-node-type={task.type}
      data-plan-bar-row=""
      data-plan-start={bar.start}
      onFocus={(event) => {
        if (event.target === event.currentTarget) onFocus();
      }}
      onPointerDown={press}
      ref={ref}
      role="treeitem"
      tabIndex={tabStop ? 0 : -1}
    >
      <div className="min-w-0 px-3.5 py-2 max-[761px]:contents">
        <p
          className={`text-[15px] leading-[22px] break-words ${pageInkText} max-[761px]:-order-2 max-[761px]:min-w-0 max-[761px]:basis-full`}
          data-node-words=""
          {...line.marks}
        >
          {line.words}
        </p>
        <p
          className={`mt-px flex min-w-0 flex-wrap gap-x-1.5 text-xs leading-[18px] ${pageQuietText} max-[761px]:contents`}
          data-plan-facts=""
        >
          <span data-plan-no-size="" {...pageChrome}>
            {PLAN_WORDS.no_size}
          </span>
          {bar.needs.length === 0 ? null : (
            <>
              <span {...pageChrome}>· {PLAN_WORDS.needs}</span>
              {bar.needs.flatMap((needed) => {
                // `planChart` reads each need from one of the task's `needs` chips, so every
                // need has its chip.
                const chip = needsChip(task, needed);
                return chip === undefined
                  ? []
                  : [
                      <span data-plan-need={needed.id} key={needed.id} {...taskLine(needed).marks}>
                        {chipWords(chip.name)}
                      </span>,
                    ];
              })}
            </>
          )}
        </p>
      </div>
      <div className="relative h-[54px] min-w-0 max-[761px]:-order-1 max-[761px]:my-[3px] max-[761px]:h-2.5 max-[761px]:basis-full max-[761px]:rounded-[5px] max-[761px]:bg-page-mark">
        <Units end={end} />
        <span
          aria-hidden="true"
          className="absolute top-4 block h-[22px] rounded-[5px] border-[1.5px] border-dashed border-page-step bg-page-step-tint max-[761px]:top-0 max-[761px]:h-full"
          data-plan-bar={task.id}
          style={{ left: `${bar.start * unit}%`, width: `${unit}%` }}
        />
      </div>
      <div
        className="flex min-w-0 flex-wrap items-start gap-[5px] px-3.5 py-2 max-[761px]:contents"
        data-plan-serves=""
      >
        {serves.map((one) => {
          const route = one.reference === undefined ? undefined : parseNodeReference(one.reference);
          const look = `rounded-[5px] border px-1.5 text-xs leading-[18px] font-semibold text-page-statement ${pageHairBorder}`;
          return route === undefined ? (
            <span className={look} data-plan-serve="" key={one.line} {...authorText}>
              {chipWords(one.line)}
            </span>
          ) : (
            <Link
              className={`dispatch-inline-link ${look}`}
              data-plan-serve=""
              key={one.reference}
              tabIndex={-1}
              to={buildReferencePath(route)}
              {...authorText}
            >
              {chipWords(one.line)}
            </Link>
          );
        })}
      </div>
    </div>
  );
}
