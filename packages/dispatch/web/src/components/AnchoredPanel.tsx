import {
  type ReactNode,
  type RefObject,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";

/** The panel's widest, before the 8 px margin it keeps from each viewport edge. */
const PANEL_WIDTH = 256;

/**
 * The one panel that hangs off a control: portalled to the body so no ancestor can clip it,
 * placed under its anchor and kept there while the page resizes or scrolls, and dismissed by
 * the same rules everywhere — a pointer press outside it or its anchor, or Escape, which also
 * returns focus to the anchor. Opening moves focus into the panel.
 *
 * The host owns whether it is open: it renders the panel or it does not. Every popover in
 * Dispatch is built on this, so a dismissal fix is made once (`MultiSelect`, and the issues
 * table's column filters, Columns menu and epic actions).
 */
export function AnchoredPanel({
  align = "left",
  anchorRef,
  children,
  className,
  flip = false,
  label,
  onDismiss,
  role,
}: {
  align?: "left" | "right";
  anchorRef: RefObject<HTMLElement | null>;
  children: ReactNode;
  className: string;
  /** Where there is no room under the anchor, stand above it rather than over it: a tooltip,
   *  which covers nothing the reader is pointing at. */
  flip?: boolean;
  /** The panel's accessible name, for a panel that names itself. */
  label?: string;
  onDismiss: () => void;
  role?: "dialog";
}): ReactNode {
  const panelRef = useRef<HTMLDivElement>(null);
  const [position, setPosition] = useState({ left: 0, top: 0 });
  const dismiss = useRef(onDismiss);
  dismiss.current = onDismiss;

  // Placed after every render - the anchor moves when its own row reflows - and on resize and
  // scroll, which is when the viewport coordinates it is placed at change.
  useLayoutEffect(() => {
    const place = () => {
      const anchor = anchorRef.current;
      const box = anchor?.getBoundingClientRect();
      // A panel hanging off nothing is one the reader cannot place: when its anchor has left
      // the page, or is drawn nowhere while the page around it is drawn — a row the virtualizer
      // took away, a control a narrow window folded — the panel closes rather than standing
      // where the anchor used to be. The second half is asked only of an environment that lays
      // anything out at all, so a test DOM with no layout keeps its panels.
      if (anchor === null || anchor === undefined || !anchor.isConnected) {
        dismiss.current();
        return;
      }
      if (box === undefined) return;
      const drawn = panelRef.current?.getBoundingClientRect();
      if (box.width === 0 && box.height === 0 && drawn !== undefined && drawn.height > 0) {
        dismiss.current();
        return;
      }
      // The window the reader sees: the document's own width and height, which leave out the
      // scroll bars a window's `innerWidth` counts, so a panel is never drawn under one.
      const viewWidth = document.documentElement.clientWidth || window.innerWidth;
      const viewHeight = document.documentElement.clientHeight || window.innerHeight;
      const width = Math.min(panelRef.current?.offsetWidth || PANEL_WIDTH, viewWidth - 16);
      const height = panelRef.current?.offsetHeight ?? 200;
      const left = Math.max(
        8,
        Math.min(align === "right" ? box.right - width : box.left, viewWidth - width - 8)
      );
      const below = box.bottom + 4;
      const above = box.top - 4 - height;
      const top =
        flip && below + height > viewHeight - 8 && above >= 8
          ? above
          : Math.max(8, Math.min(below, viewHeight - height - 8));
      setPosition((current) =>
        current.left === left && current.top === top ? current : { left, top }
      );
    };
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    };
  });

  useEffect(() => {
    const panel = panelRef.current;
    if (panel !== null && !panel.contains(document.activeElement))
      panel.querySelector<HTMLElement>("input, button, select, [tabindex]")?.focus();
    const outside = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) return;
      if (panelRef.current?.contains(target)) {
        // A press on the panel's own prose focuses nothing, which would leave focus on the
        // body and Escape with no owner. The panel takes it, so the reader stays in the panel.
        if (
          !(document.activeElement instanceof HTMLElement) ||
          !panelRef.current.contains(document.activeElement)
        )
          panelRef.current.focus();
        return;
      }
      if (anchorRef.current?.contains(target)) return;
      dismiss.current();
    };
    const dismissOnEscape = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      // Escape belongs to whatever the reader is in. This panel takes it only while the press
      // comes from inside the panel or from its anchor; a press in the command bar opened over
      // the panel, or in any other layer, travels on to whoever owns that layer. The listener
      // is still at the document's capture phase, so a panel inside a dialog closes itself and
      // leaves the dialog open.
      const target = event.target;
      const mine =
        target instanceof Node &&
        (panelRef.current?.contains(target) === true ||
          anchorRef.current?.contains(target) === true);
      if (!mine) return;
      event.preventDefault();
      event.stopPropagation();
      anchorRef.current?.focus();
      dismiss.current();
    };
    document.addEventListener("pointerdown", outside);
    document.addEventListener("keydown", dismissOnEscape, true);
    return () => {
      document.removeEventListener("pointerdown", outside);
      document.removeEventListener("keydown", dismissOnEscape, true);
    };
  }, [anchorRef]);

  const named = role === undefined ? {} : { role, "aria-label": label };
  return createPortal(
    <div
      className={className}
      ref={panelRef}
      style={{ position: "fixed", ...position }}
      tabIndex={-1}
      {...named}
    >
      {children}
    </div>,
    document.body
  );
}
