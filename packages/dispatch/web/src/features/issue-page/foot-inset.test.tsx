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
  edge = "bottom",
  gap,
  height,
  position,
}: {
  /** The edge the box's own style holds it at: `bottom: 0` as the issue page's box is, or the
   *  top as today's docked composer is from 640 px up (`sm:sticky sm:top-0`), whose `bottom-16`
   *  stays set at every width, so its `bottom` is 64px too. */
  edge?: "bottom" | "top";
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
      style={edge === "bottom" ? { bottom: 0, position } : { bottom: 64, position, top: 0 }}
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

// Today's docked composer is sticky too from 640 px up, held at the top of its column
// (`sm:sticky sm:top-0`), and its `bottom-16` stays set, so its computed `bottom` is 64px. On a
// short, wide window its bottom edge can meet the screen's, and there it is still in the page,
// under nothing of the shell's, so it publishes nothing and the status corner stays where `main`
// draws it.
test("a sticky box held at the top publishes nothing, even where its bottom edge meets the screen's", () => {
  const docked = render(<FootBox edge="top" gap={0} height={58} position="sticky" />);
  expect(published()).toBe("");
  docked.unmount();
});
