import { expect, test } from "bun:test";
import { render } from "@testing-library/react";
import { type ReactNode, useRef } from "react";

import { FOOT_COMPOSER_INSET_PROPERTY, useFootInset } from "../../hooks/useFootInset";

/**
 * A box `height` px tall, laid out where `position` puts it, whose bottom edge stands `gap` px
 * above the screen's bottom edge. happy-dom lays nothing out, so the box's place on screen is
 * this harness's: the element answers `getBoundingClientRect` with it, and its position is its
 * own inline style, which `getComputedStyle` reads back.
 */
function FootBox({
  gap,
  height,
  position,
}: {
  gap: number;
  height: number;
  position: "static" | "sticky";
}): ReactNode {
  const box = useRef<HTMLElement | null>(null);
  useFootInset(box, true);
  return (
    <section
      ref={(node) => {
        box.current = node;
        if (node === null) return;
        node.getBoundingClientRect = () =>
          new DOMRect(0, window.innerHeight - gap - height, 400, height);
      }}
      style={{ position }}
    />
  );
}

/** What the document element publishes as the reach of the composers held at the screen's foot. */
function published(): string {
  return document.documentElement.style.getPropertyValue(FOOT_COMPOSER_INSET_PROPERTY);
}

// The issue page's box is not fixed: `sticky bottom-0` holds it at the foot of its column while
// the outline scrolls under it. Held at the screen's bottom edge it reaches up from there as a
// fixed composer does, so the shell's status corner rides above it; within a pixel of the edge is
// at it, since a box's edge lands on a fraction of a pixel. Once the page has moved it off that
// edge it covers nothing of the shell's and counts nothing, and a box laid out in the page counts
// nothing wherever it stands.
test("a sticky box held at the screen's foot publishes its reach, and one scrolled away publishes none", () => {
  const held = render(<FootBox gap={0} height={58} position="sticky" />);
  expect(published()).toBe("58px");
  held.unmount();
  expect(published()).toBe("");

  const nearlyHeld = render(<FootBox gap={0.5} height={58} position="sticky" />);
  expect(published()).toBe("58.5px");
  nearlyHeld.unmount();
  expect(published()).toBe("");

  const scrolledAway = render(<FootBox gap={120} height={58} position="sticky" />);
  expect(published()).toBe("");
  scrolledAway.unmount();

  const laidOut = render(<FootBox gap={0} height={58} position="static" />);
  expect(published()).toBe("");
  laidOut.unmount();
});
