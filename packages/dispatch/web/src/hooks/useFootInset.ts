import { type RefObject, useLayoutEffect } from "react";

/**
 * The layers of the foot of the screen, from the bottom up. Everything at the foot of the screen
 * stacks through them, so none lies over another, whichever of them show together and at any
 * width. Each such element publishes into its layer how far up from the screen's bottom edge it
 * reaches (`useFootInset`), and each layer's property, on the document element, holds the highest
 * reach published into that layer or into any layer below it, so an element placed above a layer
 * reads that one property. A property is unset while it holds 0.
 *
 * Registering an element through `useFootInset` is what makes it a foot element: no rule can tell
 * from an element's style that a float may lie over it, since a composer laid out in the page's
 * flow at the bottom of a page that fills the screen is as much at the foot as one fixed there.
 * A registered element counts while it has a box on screen, wherever it sits and however it is
 * placed - fixed, sticky to the foot, absolute or in flow - unless it is sticky to the top
 * (`position: sticky` with a `top` other than `auto`, whatever its `bottom` says), which never
 * counts. A new foot element picks its layer here by name and joins this list.
 *
 * - `page` (`--foot-page-inset`): the page's own foot controls. Each keeps its own place, and the
 *   page lays them out against each other. Each one's reach counts at most half the screen's
 *   height (`publish`), so the floats above this layer always have the upper half of the screen
 *   and never leave it: a refused change whose Retry and Discard nobody can reach is worse than a
 *   float over part of a tall page element, and a page element taller than half the screen is a
 *   page of its own, whose upper part a float may lie over.
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
 *     screen, whose reach takes in the row laid out under it, and the live thread's own `Jump to
 *     latest`, just above the composer while it shows: both only in the view in front.
 * - `banner` (`--foot-banner-inset`): the update banner (`DeploymentResilience`), 1rem above the
 *   page layer.
 * - `status` (`--foot-status-inset`): the shell's status corner - Pending changes, the note on
 *   drafts the tab cannot keep, and the connection pill (`app.tsx`) - 1rem above the banner
 *   layer, and below `xl` never lower than its floor of 5rem.
 *
 * Two controls read the top layer, `--foot-status-inset`, and publish nothing: the Conversation's
 * `Jump to latest` and the chord indicator (`ChordIndicator`). Each sits 1rem above it, and never
 * lower than a floor of its own.
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
] as const;

/** The name of one layer of `FOOT_LAYERS`. */
export type FootLayer = (typeof FOOT_LAYERS)[number]["name"];

/** One element this hook holds: how far up from the screen's bottom edge it reaches - 0 while it
 *  is hidden, off the screen, or sticky to the top - and the measurement that reads that again
 *  where the element now stands. */
type FootControl = { measure: () => void; reach: number };

/** Each layer's own elements, so publish takes each layer's value from its own reaches and those
 *  of the layers below it. */
const layers = new Map<FootLayer, Map<Element, FootControl>>();

function controlsOf(layer: FootLayer): Map<Element, FootControl> {
  const controls = layers.get(layer) ?? new Map<Element, FootControl>();
  layers.set(layer, controls);
  return controls;
}

/**
 * Publishes every layer's value, from the bottom up: each holds the highest reach published into
 * it or into a layer below it, a page element's reach counted at most half the screen's height
 * (`FOOT_LAYERS` says why). When that changes any value the document element holds, it measures
 * again the elements of every layer above the lowest one whose value changed.
 *
 * These properties place elements, so a publish moves elements that never change size: the update
 * banner sits on the page layer, so a composer that grows pushes the banner up the screen with its
 * box untouched, and the status corner sits on the banner layer. Neither a `ResizeObserver` on a
 * moved element nor a `window` resize reports a move, so without this the banner would keep
 * publishing the reach it had where it stood before, the corner would stand on that stale reach,
 * and `Jump to latest`, placed by the corner's, would land inside the composer. No element is
 * placed on its own layer or a layer above it, so a change moves only the elements of the layers
 * above it, and those are the ones measured again.
 *
 * The comparison is against each property as the document element holds it, so what decides a
 * cascade is the published value itself rather than a number kept beside it. That is also what
 * ends the recursion: a publish that changes no value measures nothing again. The composer grows
 * and raises the page layer, the banner is measured where it now stands and raises the banner
 * layer, the corner is measured where that puts it and raises the status layer, which places no
 * element the hook holds, and the cascade stops.
 */
function publish(): void {
  const style = document.documentElement.style;
  let highest = 0;
  // The first layer whose elements a changed value moves; none while it is past the last.
  let moved: number = FOOT_LAYERS.length;
  for (const [index, { name, property }] of FOOT_LAYERS.entries()) {
    const most = name === "page" ? window.innerHeight / 2 : Number.POSITIVE_INFINITY;
    for (const control of controlsOf(name).values()) {
      highest = Math.max(highest, Math.min(control.reach, most));
    }
    const value = highest === 0 ? "" : `${highest}px`;
    if (style.getPropertyValue(property) === value) continue;
    moved = Math.min(moved, index + 1);
    if (value === "") style.removeProperty(property);
    else style.setProperty(property, value);
  }
  for (const { name } of FOOT_LAYERS.slice(moved)) {
    for (const control of controlsOf(name).values()) control.measure();
  }
}

/**
 * While `active`, makes `ref`'s element a foot element of `layer` (`FOOT_LAYERS` says what that
 * means): it measures the element whenever it or the screen changes size - a refusal's row grows
 * a composer upward, a widening moves it - or whenever a published layer moves it, and publishes
 * into `layer` how far up from the screen's bottom edge the element reaches: from its top edge
 * down, so a row laid out under it is taken in too. It counts while the element has a box on
 * screen, however it is placed, so it counts where it rests above the foot too - a sticky
 * element at the end of its scroll, or anything mid-screen under a short list - and the floats
 * ride above it, since a float lies over a foot element wherever their bands meet, not only where
 * the element touches the screen's bottom edge. An element sticky to the top (`position: sticky`
 * with a `top` other than `auto`) never counts, whatever its `bottom` says: the docked composer is
 * `fixed` with a `bottom` below `sm` and sticks to the top from `sm` up, keeping that `bottom`.
 *
 * An element that is not fixed moves when its scroll container scrolls, and when the content
 * around it grows or shrinks - agents joining or leaving the Agents list - and neither a
 * `ResizeObserver` on the element nor a `window` resize reports either move. So while such an
 * element has a box and is not sticky to the top, the hook also measures it on any scroll of a
 * container that holds it, once per animation frame, and whenever its parent changes size: the
 * parent element itself, or, where that is `display: contents` and has no box, the nearest
 * ancestor that has one, found again at each measure, since a resize can change which that is.
 */
export function useFootInset(
  ref: RefObject<HTMLElement | null>,
  active: boolean,
  layer: FootLayer
): void {
  useLayoutEffect(() => {
    const element = ref.current;
    if (!active || element === null) return;
    const controls = controlsOf(layer);
    const sizes = new ResizeObserver(() => control.measure());
    let frame = 0;
    const measureNextFrame = () => {
      if (frame !== 0) return;
      frame = requestAnimationFrame(() => {
        frame = 0;
        control.measure();
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
    // Reading the box flushes the style and layout a cascading publish has just changed, so the
    // element is measured where the new layers put it, not where it was painted last.
    const control: FootControl = {
      measure: () => {
        const box = element.getBoundingClientRect();
        const { position, top } = getComputedStyle(element);
        const placed = box.height > 0 && !(position === "sticky" && top !== "auto");
        follow(placed && position !== "fixed");
        const onScreen = box.top < window.innerHeight && box.bottom > 0;
        control.reach = placed && onScreen ? window.innerHeight - box.top : 0;
        publish();
      },
      reach: 0,
    };
    controls.set(element, control);
    sizes.observe(element);
    window.addEventListener("resize", control.measure);
    control.measure();
    return () => {
      follow(false);
      sizes.disconnect();
      window.removeEventListener("resize", control.measure);
      cancelAnimationFrame(frame);
      controls.delete(element);
      publish();
    };
  }, [active, layer, ref]);
}
