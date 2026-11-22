import { type RefObject, useLayoutEffect } from "react";

/**
 * The layers of the foot of the screen, from the bottom up. Everything fixed or stuck at the foot
 * of the screen stacks through them, so no float lies over a foot element and no float leaves the
 * screen, whichever of them show together and at any size.
 *
 * Registering an element through `useFootInset` is what makes it a foot element: no rule can tell
 * from an element's style that a float may lie over it, since a composer laid out in the page's
 * flow at the bottom of a page that fills the screen is as much at the foot as one fixed there.
 * A new foot element picks its layer here by name and joins this list.
 *
 * The elements of the `page` layer are obstacles. Each one's band is its box, clipped to the
 * screen, as offsets up from the screen's bottom edge. A page element counts while it has a box
 * on screen, wherever it sits and however it is placed - fixed, sticky to the foot, absolute or in
 * flow - unless it is sticky to the top (`position: sticky` with a `top` other than `auto`,
 * whatever its `bottom` says), which never counts.
 *
 * The elements of the layers above `page` are floats, all fixed. A float sits 1rem above the
 * property of the layer below its own, and never lower than its floor. Its floor is the custom
 * property `--foot-floor`, which its own classes set per breakpoint and its `bottom` reads. A float
 * that sets no floor has a floor of 0. So the hook places a float's band from that property, its
 * floor and its height, and never measures where the float is. A float with no box counts nothing.
 *
 * Each layer's property, on the document element, is chosen for the floats of the layer above it
 * (`publish`): the lowest value at which each of those floats sits in a free band. A free band
 * keeps 1rem away from every page element and every float of a lower layer. So a float sits on
 * its floor under every obstacle it clears, or 1rem above an obstacle. Where no band is free, the
 * property is the highest value at which the floats stay on the screen, or else 0. A property is
 * unset while it holds 0. No page element's band is capped: every page element is bounded - the
 * live view's message box at 40dvh, the Agents section at 50vh - so a band holds the floats
 * wherever the window is tall enough, and the fallback only keeps them on the screen.
 *
 * - `page` (`--foot-page-inset`): the page's own foot controls. Each keeps its own place, and the
 *   page lays them out against each other.
 *   - The docked composer below `sm`, and a composer a host's frame fixes at the foot: a phone
 *     thread's and a phone reply's in the Conversation, and a phone margin thread's (`ThreadCard`'s
 *     composer frame), all through `MentionComposer`. From `sm` up the docked composer sticks to
 *     the top and counts nothing.
 *   - The issues table's selection bar from `sm` up (`IssueTableSelection`). Below `sm` it sits
 *     under the header and publishes nothing.
 *   - The margin's sheet below `xl` while it is collapsed (`MarginSheet`).
 *   - The Agents page's broadcast composer, sticky to the foot of the page's scroll
 *     (`AgentsPage`), wherever it is on screen, stuck or resting at the end of the list: the
 *     wrapper that holds it with the sends, and, on a narrow or short screen, where the wrapper
 *     has no box, the composer's own section, which is what sticks there.
 *   - The agent live view's composer (`AgentThread`), in the flow of a page that fills the
 *     screen; the rows laid out under it, the row of pickers and the switch lines, in the one box
 *     that holds both (`AgentConversationPage`); and the live thread's own `Jump to latest`, just
 *     above the composer while it shows. All three count only in the view in front.
 * - `banner` (`--foot-banner-inset`): the update banner (`DeploymentResilience`), which reads the
 *   page layer's property and has no floor.
 * - `status` (`--foot-status-inset`): the shell's status corner - Pending changes, the note on
 *   drafts the tab cannot keep, and the connection pill (`app.tsx`). It reads the banner layer's
 *   property, and its floor is 5rem below `xl` and 0 from `xl` up.
 * - `top` (no property): the Conversation's `Jump to latest` (`ConversationTab`) while it shows,
 *   with a floor of 5rem, 9rem from `sm` up and 6rem from `xl` up; and the chord indicator
 *   (`ChordIndicator`) while a chord is pending, with a floor of 8rem, and 4rem from `xl` up.
 *   Both read the status layer's property. No float sits above them, so this layer publishes no
 *   property.
 *
 * Not in the stack: the search palette and the phone Contents sheet, each a whole-screen surface
 * over the page while it is open, with nothing at the foot beside it; the margin's expanded sheet
 * below `xl`, a modal dialog over the page; and `MultiSelect`'s sticky foot, which sticks to the
 * foot of its own popover, not of the screen.
 */
const FOOT_LAYERS = [
  { name: "page", property: "--foot-page-inset" },
  { name: "banner", property: "--foot-banner-inset" },
  { name: "status", property: "--foot-status-inset" },
  { name: "top", property: null },
] as const;

/** The name of one layer of `FOOT_LAYERS`. */
export type FootLayer = (typeof FOOT_LAYERS)[number]["name"];

/** A stretch of the screen's height, as offsets up from the screen's bottom edge: `lo` is its
 *  lower edge and `hi` its upper edge. */
interface Band {
  readonly hi: number;
  readonly lo: number;
}

/** A float as the hook holds it: its floor (`--foot-floor`) and its height, both in pixels. */
interface Float {
  readonly floor: number;
  readonly height: number;
}

/** The band of each page element that counts. */
const pageBands = new Map<Element, Band>();

/** The floats of each layer above `page` that have a box. */
const layerFloats = new Map<FootLayer, Map<Element, Float>>();

function floatsOf(layer: FootLayer): Map<Element, Float> {
  const floats = layerFloats.get(layer) ?? new Map<Element, Float>();
  layerFloats.set(layer, floats);
  return floats;
}

/** What 1rem is, in pixels: the root font size. */
function remPixels(): number {
  return Number.parseFloat(getComputedStyle(document.documentElement).fontSize);
}

/** A float's floor in pixels, from its `--foot-floor` in `px` or `rem`. An empty or other value
 *  is 0. */
function floorOf(element: Element): number {
  const floor = /^(\d*\.?\d+)(px|rem)$/.exec(
    getComputedStyle(element).getPropertyValue("--foot-floor").trim()
  );
  if (floor === null) return 0;
  const size = Number(floor[1]);
  return floor[2] === "px" ? size : size * remPixels();
}

/** The band of a float while the property it reads holds `inset`: on its floor, or `gap` above
 *  `inset`, whichever is higher, as its own `bottom` places it. */
function floatBand({ floor, height }: Float, inset: number, gap: number): Band {
  const lo = Math.max(floor, inset + gap);
  return { hi: lo + height, lo };
}

/**
 * The property for the floats of one layer: the lowest value at which no float of the layer
 * meets an obstacle. A float meets an obstacle where their bands come nearer than `gap`. The
 * values tried are 0 and then each obstacle's upper edge, from the lowest up, so a float sits on
 * its floor or `gap` above an obstacle. A value that puts a float past the top of the screen
 * (`screen`) is never taken. Where no value is free, it is the highest value tried that keeps the
 * floats on the screen, or else 0. A layer with no float gets 0.
 */
function freeInset(
  floats: readonly Float[],
  obstacles: readonly Band[],
  gap: number,
  screen: number
): number {
  let onScreen = 0;
  for (const inset of [0, ...obstacles.map(({ hi }) => hi).sort((a, b) => a - b)]) {
    const bands = floats.map((float) => floatBand(float, inset, gap));
    // A float's band only rises with the value, so no later value keeps the floats on the screen.
    if (bands.some(({ hi }) => hi > screen)) break;
    const free = bands.every(
      (band) =>
        !obstacles.some((obstacle) => band.lo < obstacle.hi + gap && band.hi > obstacle.lo - gap)
    );
    if (free) return inset;
    onScreen = inset;
  }
  return onScreen;
}

/**
 * Publishes every layer's property, from the bottom up (`FOOT_LAYERS` says what each holds). The
 * obstacles start as the page bands. For each layer that has a property, it chooses that property
 * for the floats of the layer above (`freeInset`), writes it, and adds those floats' bands at that
 * property to the obstacles of the layers higher up. The gap is 1rem.
 *
 * It measures nothing. A published property moves the floats that read it, but the hook places a
 * float's band from that property, not from where the float is. No page element reads a property,
 * so a published property moves none.
 */
function publish(): void {
  const style = document.documentElement.style;
  const gap = remPixels();
  const screen = window.innerHeight;
  const obstacles = [...pageBands.values()];
  for (const [index, { property }] of FOOT_LAYERS.entries()) {
    if (property === null) break;
    const floats = [...floatsOf(FOOT_LAYERS[index + 1].name).values()];
    const inset = freeInset(floats, obstacles, gap, screen);
    const value = inset === 0 ? "" : `${inset}px`;
    if (style.getPropertyValue(property) !== value) {
      if (value === "") style.removeProperty(property);
      else style.setProperty(property, value);
    }
    for (const float of floats) obstacles.push(floatBand(float, inset, gap));
  }
}

/**
 * While `active`, makes `ref`'s element a foot element of `layer` (`FOOT_LAYERS` says what that
 * means), and publishes every layer's property again each time it measures the element.
 *
 * A page element is measured whenever it or the screen changes size - a refusal's row grows a
 * composer upward, a widening moves it - and the hook holds its band: its box's top and bottom
 * edges, clipped to the screen, as offsets up from the screen's bottom edge. It counts while it
 * has a box on screen, however it is placed, so it counts where it rests above the foot too - a
 * sticky element at the end of its scroll, or anything mid-screen under a short list - and the
 * floats keep clear of it, since a float lies over a foot element wherever their bands meet, not
 * only where the element touches the screen's bottom edge. An element sticky to the top
 * (`position: sticky` with a `top` other than `auto`) never counts, whatever its `bottom` says:
 * the docked composer is `fixed` with a `bottom` below `sm` and sticks to the top from `sm` up,
 * keeping that `bottom`.
 *
 * A page element that is not fixed moves when its scroll container scrolls, and when the content
 * around it grows or shrinks - agents joining or leaving the Agents list - and neither a
 * `ResizeObserver` on the element nor a `window` resize reports either move. So while such an
 * element has a box and is not sticky to the top, the hook also measures it on any scroll of a
 * container that holds it, once per animation frame, and whenever its parent changes size: the
 * parent element itself, or, where that is `display: contents` and has no box, the nearest
 * ancestor that has one, found again at each measure, since a resize can change which that is.
 *
 * A float is measured only when its own size changes or the window resizes, and the hook holds
 * its height and its floor (`--foot-floor`), read again at each measure, since a window resize can
 * cross a breakpoint that changes the floor. A float with no box (height 0) counts nothing.
 */
export function useFootInset(
  ref: RefObject<HTMLElement | null>,
  active: boolean,
  layer: FootLayer
): void {
  useLayoutEffect(() => {
    const element = ref.current;
    if (!active || element === null) return;
    const sizes = new ResizeObserver(() => measure());
    let frame = 0;
    const measureNextFrame = () => {
      if (frame !== 0) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        measure();
      });
    };
    // The scroll listener captures, since a scroll event does not bubble: it hears the scroll of
    // any container, and measures for one that holds the element. A page's own scroll targets the
    // document, which holds every element.
    const measureOnScroll = (event: Event) => {
      if (event.target instanceof Node && event.target.contains(element)) measureNextFrame();
    };
    let following = false;
    let parent: Element | null = null;
    const follow = (moves: boolean) => {
      if (moves !== following) {
        following = moves;
        if (moves) {
          window.addEventListener("scroll", measureOnScroll, { capture: true, passive: true });
        } else {
          window.removeEventListener("scroll", measureOnScroll, { capture: true });
        }
      }
      let boxed = moves ? element.parentElement : null;
      while (boxed !== null && getComputedStyle(boxed).display === "contents") {
        boxed = boxed.parentElement;
      }
      if (boxed === parent) return;
      if (parent !== null) sizes.unobserve(parent);
      if (boxed !== null) sizes.observe(boxed);
      parent = boxed;
    };
    const floats = floatsOf(layer);
    const measurePage = () => {
      const box = element.getBoundingClientRect();
      const { position, top } = getComputedStyle(element);
      const placed = box.height > 0 && !(position === "sticky" && top !== "auto");
      follow(placed && position !== "fixed");
      const screen = window.innerHeight;
      if (placed && box.top < screen && box.bottom > 0) {
        pageBands.set(element, {
          hi: screen - Math.max(box.top, 0),
          lo: screen - Math.min(box.bottom, screen),
        });
      } else {
        pageBands.delete(element);
      }
      publish();
    };
    const measureFloat = () => {
      const { height } = element.getBoundingClientRect();
      if (height > 0) floats.set(element, { floor: floorOf(element), height });
      else floats.delete(element);
      publish();
    };
    const measure = layer === "page" ? measurePage : measureFloat;
    sizes.observe(element);
    window.addEventListener("resize", measure);
    measure();
    return () => {
      follow(false);
      sizes.disconnect();
      window.removeEventListener("resize", measure);
      cancelAnimationFrame(frame);
      pageBands.delete(element);
      floats.delete(element);
      publish();
    };
  }, [active, layer, ref]);
}
