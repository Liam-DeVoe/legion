import { expect, test } from "bun:test";
import { TMUX_LAYOUT_MAX } from "@legion/contracts";

import { parseTmuxLayout, type TmuxLayoutPane } from "./tmux-layout";

// Recorded from tmux 3.5a, each the window's own `window_layout`.
/** One pane filling an 80x24 window. */
const ONE_PANE = "b262,80x24,0,0,5";
/** `%0` over `%1` beside `%2`, in an 80x24 window. */
const THREE_PANES_SMALL = "b05b,80x24,0,0[80x12,0,0,0,80x11,0,13{40x11,0,13,1,39x11,41,13,2}]";
/** The same three panes in a 200x50 window. */
const THREE_PANES_LARGE = "81f0,200x50,0,0[200x25,0,0,0,200x24,0,26{100x24,0,26,1,99x24,101,26,2}]";
/** What that window reports while `%1` is zoomed: the one pane that is on the screen. */
const ZOOMED = "ac9e,200x50,0,0,1";

// Recorded from tmux 3.8, which writes the JSON form, each the window's own `window_layout`: the
// same windows as above, built by the same commands on a fresh server, so their pane ids match.
/** One pane filling an 80x24 window. */
const JSON_ONE_PANE = '{"V":2,"L":{"t":"p","w":80,"h":24,"x":0,"y":0,"a":true,"i":0,"I":"%0"}}';
/** `%0` over `%1` beside `%2`, in an 80x24 window. */
const JSON_THREE_PANES_SMALL =
  '{"V":2,"L":{"t":"v","w":80,"h":24,"x":0,"y":0,"c":[{"t":"p","w":80,"h":12,"x":0,"y":0,"a":true,"i":0,"I":"%0"},{"t":"h","w":80,"h":11,"x":0,"y":13,"c":[{"t":"p","w":40,"h":11,"x":0,"y":13,"i":1,"I":"%1"},{"t":"p","w":39,"h":11,"x":41,"y":13,"i":2,"I":"%2"}]}]}}';
/** The same three panes in a 200x50 window. */
const JSON_THREE_PANES_LARGE =
  '{"V":2,"L":{"t":"v","w":200,"h":50,"x":0,"y":0,"c":[{"t":"p","w":200,"h":25,"x":0,"y":0,"a":true,"i":0,"I":"%0"},{"t":"h","w":200,"h":24,"x":0,"y":26,"c":[{"t":"p","w":100,"h":24,"x":0,"y":26,"i":1,"I":"%1"},{"t":"p","w":99,"h":24,"x":101,"y":26,"i":2,"I":"%2"}]}]}}';
/** One tiled pane filling an 80x24 window and, over it, the floating pane
 *  `new-pane -X 10 -Y 5 -x 30 -y 10` made, which version 1 has no way to state. */
const JSON_FLOATING =
  '{"V":2,"L":{"t":"v","w":80,"h":24,"x":0,"y":0,"c":[{"t":"p","w":80,"h":24,"x":0,"y":0,"a":true,"i":0,"I":"%0"},{"t":"p","w":28,"h":8,"x":11,"y":6,"i":1,"z":0,"I":"%1"}]}}';

interface Proportion {
  readonly paneId: string;
  readonly x: number;
  readonly y: number;
  readonly width: number;
  readonly height: number;
}

/** Each pane's box as a fraction of the window, which is what a drawing scales to its own box. */
function proportions(layout: string): Proportion[] {
  const parsed = parseTmuxLayout(layout);
  if (parsed === undefined) {
    throw new Error(`layout did not parse: ${layout}`);
  }
  const { root } = parsed;
  return parsed.panes.map((pane) => ({
    height: pane.height / root.height,
    paneId: pane.paneId,
    width: pane.width / root.width,
    x: pane.x / root.width,
    y: pane.y / root.height,
  }));
}

/**
 * Two windows of different sizes cannot hold one shape exactly: the divider is one cell whatever
 * the window's size, so it is 1/24 of the small window's height and 1/50 of the large one's, and
 * every difference between the two layouts below is exactly that share — 1/24 − 1/50 = 0.0217 of
 * the height, 1/80 − 1/200 = 0.0075 of the width. The check is therefore one percent of the
 * window on top of the divider's own share.
 */
const SAME_SHAPE = 0.01 + (1 / 24 - 1 / 50);

test("a window of one pane reads as that pane filling the window", () => {
  const pane: TmuxLayoutPane = { height: 24, kind: "pane", paneId: "%5", width: 80, x: 0, y: 0 };

  expect(parseTmuxLayout(ONE_PANE)).toEqual({ panes: [pane], root: pane });
});

test("a split window reads as its tree of boxes, each pane keeping tmux's `%` on its id", () => {
  const layout = parseTmuxLayout(THREE_PANES_SMALL);

  expect(layout?.root).toEqual({
    children: [
      { height: 12, kind: "pane", paneId: "%0", width: 80, x: 0, y: 0 },
      {
        children: [
          { height: 11, kind: "pane", paneId: "%1", width: 40, x: 0, y: 13 },
          { height: 11, kind: "pane", paneId: "%2", width: 39, x: 41, y: 13 },
        ],
        height: 11,
        into: "columns",
        kind: "split",
        width: 80,
        x: 0,
        y: 13,
      },
    ],
    height: 24,
    into: "rows",
    kind: "split",
    width: 80,
    x: 0,
    y: 0,
  });
  expect(layout?.panes.map((pane) => pane.paneId)).toEqual(["%0", "%1", "%2"]);
});

test("the divider tmux counts between two panes is left as tmux counted it", () => {
  const panes = parseTmuxLayout(THREE_PANES_SMALL)?.panes ?? [];
  const [top, left, right] = panes;

  // 40 + 1 + 39 is the window's 80, and 12 + 1 + 11 its 24: the missing cell is the divider, and
  // the parser neither adds it to a pane nor reports a gap of its own.
  expect(left.width + 1 + right.width).toBe(80);
  expect(top.height + 1 + left.height).toBe(24);
  expect(right.x).toBe(left.x + left.width + 1);
});

test("the same window at another size reads as the same shape", () => {
  const small = proportions(THREE_PANES_SMALL);
  const large = proportions(THREE_PANES_LARGE);

  expect(large.map((pane) => pane.paneId)).toEqual(small.map((pane) => pane.paneId));
  for (const [index, pane] of small.entries()) {
    const other = large[index];
    expect(Math.abs(pane.x - other.x)).toBeLessThanOrEqual(SAME_SHAPE);
    expect(Math.abs(pane.y - other.y)).toBeLessThanOrEqual(SAME_SHAPE);
    expect(Math.abs(pane.width - other.width)).toBeLessThanOrEqual(SAME_SHAPE);
    expect(Math.abs(pane.height - other.height)).toBeLessThanOrEqual(SAME_SHAPE);
  }
  // Where the window is split is the same in both, to the cell: half the height, half the width.
  expect([small[0].height, large[0].height]).toEqual([0.5, 0.5]);
  expect([small[1].width, large[1].width]).toEqual([0.5, 0.5]);
});

test("a zoomed window reads as the one pane that is on the screen", () => {
  expect(parseTmuxLayout(ZOOMED)?.panes).toEqual([
    { height: 50, kind: "pane", paneId: "%1", width: 200, x: 0, y: 0 },
  ]);
});

test("an unbalanced bracket is no layout", () => {
  expect(parseTmuxLayout("b05b,80x24,0,0[80x12,0,0,0,80x11,0,13")).toBeUndefined();
  expect(parseTmuxLayout("b05b,80x24,0,0[80x12,0,0,0,80x11,0,13}")).toBeUndefined();
});

test("a cell that is neither a pane nor a split is no layout", () => {
  expect(parseTmuxLayout("b262,80x24,0,0")).toBeUndefined();
  expect(parseTmuxLayout("b262,80x24,0,0,")).toBeUndefined();
});

test("a size that is not a number is no layout", () => {
  expect(parseTmuxLayout("b262,80xtall,0,0,5")).toBeUndefined();
  expect(parseTmuxLayout("b262,eightyx24,0,0,5")).toBeUndefined();
});

test("the empty string is no layout", () => {
  expect(parseTmuxLayout("")).toBeUndefined();
});

test("a string without tmux's checksum, or with anything after the root cell, is no layout", () => {
  expect(parseTmuxLayout("80x24,0,0,5")).toBeUndefined();
  expect(parseTmuxLayout("b262,80x24,0,0,5]")).toBeUndefined();
});

/** A layout of `levels` splits, each holding the next, with one pane at the bottom: eleven
 *  characters a level, which is how cheaply a sender can buy a level of recursion in the
 *  parser. */
function nested(levels: number): string {
  return `b262,${"80x24,0,0[".repeat(levels)}80x24,0,0,0${"]".repeat(levels)}`;
}

test("a layout past the cap the sender's own client holds to is no layout, however it nests", () => {
  const inside = nested(180);
  const past = nested(181);
  expect(inside.length).toBeLessThanOrEqual(TMUX_LAYOUT_MAX);
  expect(past.length).toBeGreaterThan(TMUX_LAYOUT_MAX);

  // The two strings differ by one split, so what refuses the longer one is its length and not
  // its shape: a page draws a window from whatever a session registered, and nothing between
  // the sender and the page applies this cap on the parser's behalf.
  expect(parseTmuxLayout(inside)?.panes).toEqual([
    { height: 24, kind: "pane", paneId: "%0", width: 80, x: 0, y: 0 },
  ]);
  expect(parseTmuxLayout(past)).toBeUndefined();
});

test("tmux 3.8's JSON form of a window reads as the same boxes as version 1's form of it", () => {
  for (const [json, string] of [
    [JSON_THREE_PANES_SMALL, THREE_PANES_SMALL],
    [JSON_THREE_PANES_LARGE, THREE_PANES_LARGE],
  ]) {
    const layout = parseTmuxLayout(json);
    expect(layout).toBeDefined();
    expect(layout).toEqual(parseTmuxLayout(string));
  }
});

test("a window of one pane in the JSON form reads as that pane, its id written with its `%`", () => {
  const pane: TmuxLayoutPane = { height: 24, kind: "pane", paneId: "%0", width: 80, x: 0, y: 0 };

  expect(parseTmuxLayout(JSON_ONE_PANE)).toEqual({ panes: [pane], root: pane });
});

test("a floating pane reads as a pane at its own box, after the tiled pane it sits over", () => {
  const layout = parseTmuxLayout(JSON_FLOATING);

  expect(layout?.panes).toEqual([
    { height: 24, kind: "pane", paneId: "%0", width: 80, x: 0, y: 0 },
    { height: 8, kind: "pane", paneId: "%1", width: 28, x: 11, y: 6 },
  ]);
  // tmux holds a floating pane among the children of a split, beside the tiled cells.
  expect(layout?.root).toMatchObject({ height: 24, into: "rows", kind: "split", width: 80 });
});

test("a floating pane's box may start left of or above the window, as tmux allows one to", () => {
  const offWindow =
    '{"V":2,"L":{"t":"v","w":80,"h":24,"x":0,"y":0,"c":[{"t":"p","w":80,"h":24,"x":0,"y":0,"i":0,"I":"%0"},{"t":"p","w":28,"h":8,"x":-3,"y":-2,"i":1,"z":0,"I":"%1"}]}}';

  expect(parseTmuxLayout(offWindow)?.panes.at(-1)).toEqual({
    height: 8,
    kind: "pane",
    paneId: "%1",
    width: 28,
    x: -3,
    y: -2,
  });
});

/** `JSON_ONE_PANE`'s cell with its `member` set to `value`; undefined leaves the member out, as
 *  `JSON.stringify` writes no member whose value is undefined. */
function onePaneWith(member: string, value: unknown): string {
  const cell = { a: true, h: 24, i: 0, I: "%0", t: "p", w: 80, x: 0, y: 0 };
  return JSON.stringify({ L: { ...cell, [member]: value }, V: 2 });
}

test("a JSON form of another version, or that is not whole JSON, is no layout", () => {
  expect(parseTmuxLayout(JSON_ONE_PANE.replace('"V":2', '"V":3'))).toBeUndefined();
  expect(parseTmuxLayout(JSON_ONE_PANE.replace('"V":2,', ""))).toBeUndefined();
  expect(parseTmuxLayout(JSON_ONE_PANE.slice(0, -1))).toBeUndefined();
  expect(parseTmuxLayout(`${JSON_ONE_PANE}}`)).toBeUndefined();
  expect(parseTmuxLayout('{"V":2}')).toBeUndefined();
  expect(parseTmuxLayout(`[${JSON_ONE_PANE}]`)).toBeUndefined();
});

test("a JSON cell that is neither a pane nor a split, or misses what its type needs, is no layout", () => {
  // The cell every case below changes one member of reads as a pane when nothing is changed.
  expect(parseTmuxLayout(onePaneWith("i", 0))?.panes).toHaveLength(1);
  expect(parseTmuxLayout(onePaneWith("t", "x"))).toBeUndefined();
  expect(parseTmuxLayout(onePaneWith("t", undefined))).toBeUndefined();
  // A pane names its id, written with its `%`.
  expect(parseTmuxLayout(onePaneWith("I", undefined))).toBeUndefined();
  expect(parseTmuxLayout(onePaneWith("I", "0"))).toBeUndefined();
  expect(parseTmuxLayout(onePaneWith("I", "%zero"))).toBeUndefined();
  // A pane holds no cells, and a split holds at least one.
  expect(parseTmuxLayout(onePaneWith("c", []))).toBeUndefined();
  const emptySplit = JSON_THREE_PANES_SMALL.replace(/"c":\[.*\]\}\}$/, '"c":[]}}');
  expect(emptySplit).toEndWith('"y":0,"c":[]}}');
  expect(parseTmuxLayout(emptySplit)).toBeUndefined();
  expect(parseTmuxLayout(onePaneWith("t", "v"))).toBeUndefined();
});

test("a JSON size or offset that is not a whole number, or a size below zero, is no layout", () => {
  // Whole numbers read, a size of zero and an offset below zero included.
  expect(parseTmuxLayout(onePaneWith("w", 0))?.panes[0]?.width).toBe(0);
  expect(parseTmuxLayout(onePaneWith("x", -1))?.panes[0]?.x).toBe(-1);
  expect(parseTmuxLayout(onePaneWith("w", "80"))).toBeUndefined();
  expect(parseTmuxLayout(onePaneWith("w", 80.5))).toBeUndefined();
  expect(parseTmuxLayout(onePaneWith("w", -80))).toBeUndefined();
  expect(parseTmuxLayout(onePaneWith("h", undefined))).toBeUndefined();
  expect(parseTmuxLayout(onePaneWith("x", "0"))).toBeUndefined();
  expect(parseTmuxLayout(onePaneWith("y", 0.5))).toBeUndefined();
});

/** The JSON form of `levels` splits, each holding the next, with one pane at the bottom: 42
 *  characters a level. */
function nestedJson(levels: number): string {
  const split = '{"t":"v","w":80,"h":24,"x":0,"y":0,"c":[';
  const pane = '{"t":"p","w":80,"h":24,"x":0,"y":0,"i":0,"I":"%0"}';
  return `{"V":2,"L":${split.repeat(levels)}${pane}${"]}".repeat(levels)}}`;
}

test("a JSON form past the same cap is no layout, however it nests", () => {
  const inside = nestedJson(46);
  const past = nestedJson(47);
  expect(inside.length).toBeLessThanOrEqual(TMUX_LAYOUT_MAX);
  expect(past.length).toBeGreaterThan(TMUX_LAYOUT_MAX);

  expect(parseTmuxLayout(inside)?.panes).toEqual([
    { height: 24, kind: "pane", paneId: "%0", width: 80, x: 0, y: 0 },
  ]);
  expect(parseTmuxLayout(past)).toBeUndefined();
});
