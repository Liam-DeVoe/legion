import { afterEach, expect, test } from "bun:test";
// biome-ignore lint/style/noRestrictedImports: the tooltip follows the pointer's x over a box this DOM does not lay out, so each move is one pointermove at a chosen clientX, which user-event's hover cannot place.
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { BarSlots, BarTooltip, barGeometry, tooltipDay } from "./BarSlots";
import { COLOR_PALETTE, COLOR_PALETTE_LIGHT } from "./lib/colorScale";
import { Sparkbars } from "./Sparkbars";

afterEach(cleanup);

test("an unmeasured chart leaves one unit of the slot each side of its bar", () => {
  expect(barGeometry(10, 7, 0)).toEqual({ width: 8, inset: 1 });
});

test("a bar is at most 24px wide and sits in the middle of its slot", () => {
  // Seven slots over 420px are 60px each: the bar takes 24 of them, 4 of the slot's 10 units.
  const bar = barGeometry(10, 7, 420);
  expect(bar.width).toBeCloseTo(4);
  expect(bar.inset).toBeCloseTo(3);
});

test("a bar in a narrow slot keeps 2px of air beside its neighbour", () => {
  // 28 slots over 280px are 10px each: the bar takes 8 of them.
  expect(barGeometry(10, 28, 280).width).toBeCloseTo(8);
});

test("the tooltip names a day as the Pipeline page does, and anything else as it is", () => {
  expect(tooltipDay("2026-09-27")).toBe("Sep 27");
  expect(tooltipDay("week 39")).toBe("week 39");
});

test("no tooltip and no tint until the pointer is over the chart", () => {
  render(
    <BarSlots className="h-7 w-full" count={2} tooltip={() => "a day"}>
      {() => <svg role="img" />}
    </BarSlots>
  );
  expect(screen.queryByRole("tooltip")).toBeNull();
  expect(screen.queryByTestId("bar-hover-band")).toBeNull();
});

test("a chart's day under the pointer shows its tooltip, and leaving the chart hides it", () => {
  const { container } = render(
    <Sparkbars
      format={(value) => `${value} deploys`}
      max={20}
      points={[
        { label: "2026-09-26", value: 10, partial: false },
        { label: "2026-09-27", value: null, partial: true },
      ]}
    />
  );
  const frame = container.firstElementChild as HTMLElement;
  frame.getBoundingClientRect = () =>
    ({ left: 0, top: 100, width: 200, height: 28, right: 200, bottom: 128 }) as DOMRect;

  fireEvent.pointerMove(frame, { clientX: 40 });
  expect(screen.getByRole("tooltip").textContent).toBe("Sep 2610 deploys");
  expect(screen.getByTestId("bar-hover-band").style.left).toBe("0%");

  fireEvent.pointerMove(frame, { clientX: 160 });
  expect(screen.getByRole("tooltip").textContent).toBe("Sep 27 · partial dayno runs");
  expect(screen.getByTestId("bar-hover-band").style.left).toBe("50%");

  fireEvent.pointerLeave(frame);
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("a tooltip's labelled rows pair each label with its figure", () => {
  render(
    <BarTooltip
      day="2026-09-26"
      rows={[
        { label: "median", value: "30m" },
        { label: "runs", value: "4" },
      ]}
    />
  );
  expect(screen.getByText("median").tagName).toBe("DT");
  expect(screen.getByText("30m").tagName).toBe("DD");
});

test("the light palette has a colour for every slot of the dark one", () => {
  expect(COLOR_PALETTE_LIGHT).toHaveLength(COLOR_PALETTE.length);
});
