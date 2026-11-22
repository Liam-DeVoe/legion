import { type RefObject, useLayoutEffect } from "react";

/**
 * The layers of the foot of the screen, from the bottom up. Everything fixed at the screen's
 * bottom edge, or sticky to the foot of a scroll, stacks through them, so none lies over another,
 * whichever of them show together and at any width. Each such element publishes into its layer
 * how far up from the screen's bottom edge it reaches (`useFootInset`), and each layer's
 * property, on the document element, holds the highest reach published into that layer or into
 * any layer below it, so an element placed above a layer reads that one property. A property is
 * unset while it holds 0. A new foot element picks its layer here by name and joins this list.
 *
 * - `page` (`--foot-page-inset`): the page's own foot controls. Each keeps its own place, and the
 *   page lays them out against each other.
 *   - The docked composer below `sm`, and a composer a host's frame fixes at the foot: a phone
 *     thread's and a phone reply's in the Conversation, and a phone margin thread's (`ThreadCard`'s
 *     composer frame), all through `MentionComposer`.
 *   - The issues table's selection bar from `sm` up (`IssueTableSelection`). Below `sm` it sits
 *     under the header and publishes nothing.
 *   - The margin's sheet below `xl` while it is collapsed (`MarginSheet`).
 *   - The Agents page's broadcast composer, sticky to the foot of the page's scroll
 *     (`AgentsPage`), wherever it is on screen, stuck or resting at the end of the list: the
 *     wrapper that holds it with the sends, and on a narrow or short screen, where the wrapper has
 *     no box, the composer's own section.
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
export const FOOT_LAYERS = [
  { name: "page", property: "--foot-page-inset" },
  { name: "banner", property: "--foot-banner-inset" },
  { name: "status", property: "--foot-status-inset" },
] as const;

/** The name of one layer of `FOOT_LAYERS`. */
export type FootLayer = (typeof FOOT_LAYERS)[number]["name"];

/** One element this hook holds: how far up from the screen's bottom edge it reaches - 0 while it
 *  is laid out in the page, hidden, or not at the foot - and the measurement that reads that again
 *  where the element now stands. */
type FootControl = { measure: () => void; reach: number };

/** Each layer's own elements. The measurements are kept per layer, so a publisher of one never
 *  clears another's. */
const layers = new Map<FootLayer, Map<Element, FootControl>>();

function controlsOf(layer: FootLayer): Map<Element, FootControl> {
  const controls = layers.get(layer) ?? new Map<Element, FootControl>();
  layers.set(layer, controls);
  return controls;
}

/**
 * Publishes every layer's value, from the bottom up: each holds the highest reach published into
 * it or into a layer below it. When that changes any value the document element holds, it
 * measures every element the hook holds again, in every layer.
 *
 * These properties place elements, so a publish moves elements that never change size: the update
 * banner sits on the page layer, so a composer that grows pushes the banner up the screen with its
 * box untouched, and the status corner sits on the banner layer. Neither a `ResizeObserver` on a
 * moved element nor a `window` resize reports a move, so without this the banner would keep
 * publishing the reach it had where it stood before, the corner would stand on that stale reach,
 * and `Jump to latest`, placed by the corner's, would land inside the composer.
 *
 * The comparison is against each property as the document element holds it, so what decides a
 * cascade is the published value itself rather than a number kept beside it. That is also what
 * ends the recursion: a publish that changes no value measures nothing again. The composer grows
 * and raises the page layer, the banner is measured where it now stands and raises the banner
 * layer, the corner is measured where that puts it and raises the status layer, and the elements
 * measured once more have not moved, so no value changes and the cascade stops. No element is
 * placed on its own layer or a layer above it, so each step only moves elements of higher layers.
 */
function publish(): void {
  const style = document.documentElement.style;
  let highest = 0;
  let changed = false;
  for (const { name, property } of FOOT_LAYERS) {
    for (const control of controlsOf(name).values()) highest = Math.max(highest, control.reach);
    const value = highest === 0 ? "" : `${highest}px`;
    if (style.getPropertyValue(property) === value) continue;
    changed = true;
    if (value === "") style.removeProperty(property);
    else style.setProperty(property, value);
  }
  if (!changed) return;
  for (const controls of layers.values()) {
    for (const control of controls.values()) control.measure();
  }
}

/**
 * While `active`, measures `ref`'s element whenever it or the screen changes size - a refusal's
 * row grows a composer upward, a widening moves it into the page - or whenever a published layer
 * moves it, and publishes into `layer` how far up from the screen's bottom edge it reaches. It
 * counts while the element has a box and is fixed (`position: fixed`), or while it is sticky to
 * the foot (`position: sticky` with a `bottom` other than `auto`) and its box is on screen. A
 * float lies over a foot element wherever their bands meet, not only where the element touches
 * the screen's bottom edge, so a sticky element counts where it rests above the foot too - at the
 * end of its scroll, or mid-screen under a short list - and the floats ride above it. One sticky
 * to the top (`bottom: auto`) never counts.
 *
 * A sticky element moves when its scroll container scrolls, and when the content above it grows
 * or shrinks - agents joining or leaving the Agents list - and neither a `ResizeObserver` on the
 * element nor a `window` resize reports either move. So while the element is sticky to the foot
 * the hook also measures it on any scroll, once per animation frame, and whenever its parent
 * changes size: the parent element itself, or, where that is `display: contents` and has no box,
 * the nearest ancestor that has one.
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
    let following = false;
    let parent: Element | null = null;
    // The scroll listener captures, since a scroll event does not bubble: it hears the scroll of
    // any container.
    const follow = (sticky: boolean) => {
      if (sticky === following) return;
      following = sticky;
      if (sticky) {
        window.addEventListener("scroll", measureNextFrame, { capture: true, passive: true });
        parent = element.parentElement;
        while (parent !== null && getComputedStyle(parent).display === "contents") {
          parent = parent.parentElement;
        }
        if (parent !== null) sizes.observe(parent);
      } else {
        window.removeEventListener("scroll", measureNextFrame, { capture: true });
        if (parent !== null) sizes.unobserve(parent);
        parent = null;
      }
    };
    // Reading the box flushes the style and layout a cascading publish has just changed, so the
    // element is measured where the new layers put it, not where it was painted last.
    const control: FootControl = {
      measure: () => {
        const box = element.getBoundingClientRect();
        const { bottom, position } = getComputedStyle(element);
        const shown = box.height > 0;
        const stickyToFoot = shown && position === "sticky" && bottom !== "auto";
        follow(stickyToFoot);
        const counts =
          (shown && position === "fixed") ||
          (stickyToFoot && box.top < window.innerHeight && box.bottom > 0);
        control.reach = counts ? Math.max(0, window.innerHeight - box.top) : 0;
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
