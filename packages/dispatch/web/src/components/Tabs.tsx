import { type KeyboardEvent, type ReactNode, useEffect, useRef } from "react";

import { observeStrip } from "../hooks/observe-strip";
import { useScrollFadeTrailing } from "../hooks/useScrollFadeTrailing";
import { scrollFadeTrailingPx } from "../theme/classes";
import { Pill } from "./Pill";
import {
  tabClassName,
  tabCountClassName,
  tabListClassName,
  tabStripClassName,
} from "./tab-classes";

export interface TabDefinition<Id extends string> {
  id: Id;
  label: string;
  /** Shown as a small pill after the label; the tab's accessible name is `<label> <count>`. */
  count?: number;
}

/** The DOM id of the tablist button for `id` under `idPrefix`. A keyboard binding that
 *  activates a tab finds its button by this, so the format has one owner. */
export function tabButtonId(idPrefix: string, id: string): string {
  return `${idPrefix}-${id}-tab`;
}

/** A tablist may be narrower than its tabs (a phone, or the issue page beside its comment
 *  margin); a tab focused from the keyboard or selected must be fully visible, which Chromium's
 *  `focus()` alone does not guarantee. Only the tablist scrolls: `scrollIntoView` would also move
 *  the page, undoing the per-tab scroll position IssuePage restores when a panel comes back.
 *
 *  While tabs lie past its trailing edge, the tablist's last `scrollFadeTrailingPx` wear the
 *  trailing fade, so a tab is whole only clear of it: the tab stops that far inside the edge. A
 *  tab too near the end of the scroll for that stops at the end instead, where the fade is off. */
function revealTab(node: HTMLElement | null | undefined): void {
  const list = node?.parentElement;
  if (node === null || node === undefined || list === null || list === undefined) {
    return;
  }
  const listBox = list.getBoundingClientRect();
  const box = node.getBoundingClientRect();
  const visibleRight = listBox.right - scrollFadeTrailingPx;
  if (box.left < listBox.left) {
    list.scrollLeft += box.left - listBox.left;
  } else if (box.right > visibleRight) {
    list.scrollLeft += box.right - visibleRight;
  }
}

export function Tabs<Id extends string>({
  activeTab,
  ariaLabel,
  compact = false,
  idPrefix,
  onBeforeTabChange,
  onSelect,
  tabs,
  trailing,
}: {
  activeTab: Id;
  ariaLabel: string;
  compact?: boolean;
  idPrefix: string;
  onBeforeTabChange?: (current: Id) => void;
  onSelect: (tab: Id) => void;
  trailing?: ReactNode;
  tabs: readonly TabDefinition<Id>[];
}): ReactNode {
  const tabRefs = useRef<Partial<Record<Id, HTMLButtonElement | null>>>({});
  const pointerCapturedScroll = useRef(false);
  const tablistFade = useScrollFadeTrailing();

  const selectTab = (tab: Id) => {
    if (!pointerCapturedScroll.current) {
      onBeforeTabChange?.(activeTab);
    }
    pointerCapturedScroll.current = false;
    onSelect(tab);
  };
  const onTabKeyDown = (event: KeyboardEvent<HTMLButtonElement>, current: Id) => {
    const index = tabs.findIndex((tab) => tab.id === current);
    let nextIndex: number | undefined;
    if (event.key === "ArrowRight") {
      nextIndex = (index + 1) % tabs.length;
    } else if (event.key === "ArrowLeft") {
      nextIndex = (index - 1 + tabs.length) % tabs.length;
    } else if (event.key === "Home") {
      nextIndex = 0;
    } else if (event.key === "End") {
      nextIndex = tabs.length - 1;
    }
    if (nextIndex === undefined) {
      return;
    }
    event.preventDefault();
    const next = tabs[nextIndex];
    if (next === undefined) {
      return;
    }
    selectTab(next.id);
    const node = tabRefs.current[next.id];
    node?.focus();
    revealTab(node);
  };

  // Reveal the selected tab when it is chosen, when the list narrows (the comment margin opens
  // or the window narrows), when a tab's width changes (a live count grows), and when a tab is
  // added or taken away (the project's Architecture tab arriving before Issues). A preceding tab
  // can move the selection under the fade without changing the list's or selected tab's width.
  //
  // The reveal on choosing is in a frame of its own, never in the commit that mounted or
  // switched the tab: the two rectangles are the first thing on the page to ask for a layout, so
  // inside the commit they force the whole page's style and layout to be computed there. On a
  // phone at 4x CPU throttling that was 170-200 ms of the one task that mounts an issue page - a
  // task already over the 500 ms a reader feels as a freeze (dispatch://INFRA-341, R11). An
  // animation frame reads the layout the browser was about to compute anyway, and scrolls the
  // tablist before that frame is painted, so nothing a reader sees moves later than it did. A
  // tab the reader reached from the keyboard is still revealed in their own keystroke
  // (`onTabKeyDown`), where the measurement is all the task holds.
  useEffect(() => {
    const selected = tabRefs.current[activeTab];
    const frame = requestAnimationFrame(() => revealTab(selected));
    const list = selected?.parentElement;
    if (list === null || list === undefined) {
      return () => cancelAnimationFrame(frame);
    }
    const stop = observeStrip(list, () => revealTab(selected), { childList: true });
    return () => {
      cancelAnimationFrame(frame);
      stop();
    };
  }, [activeTab]);

  // Compact tabs sit tighter on phones, which is what keeps the shorter tablists on one line.
  // The issue's ten tabs, one carrying a count pill, no longer fit a 390 px viewport, nor the
  // issue page at 1440 px beside its comment margin: there the tablist scrolls sideways, as it
  // already did at 360 px, wears the trailing fade while tabs lie past its edge, and `revealTab`
  // keeps the focused or selected tab fully visible and clear of the fade. Every tab stays
  // reachable and keeps its 44 px target. The classes themselves are `tab-classes.ts`, which the
  // box the issue page keeps for this strip reads too.
  return (
    <div className={tabStripClassName(compact)}>
      <div
        aria-label={ariaLabel}
        className={`${tabListClassName(compact)} ${tablistFade.fade}`}
        ref={tablistFade.ref}
        role="tablist"
      >
        {tabs.map((tab) => (
          <button
            aria-controls={`${idPrefix}-${tab.id}-panel`}
            // The name has to contain the visible text, word for word, or a voice user who says
            // what the tab reads cannot press it (WCAG 2.5.3, axe's
            // `label-content-name-mismatch`). The pill's count is part of that text, and
            // parentheses around it are dropped before the two are compared, so the name spells
            // the count plainly.
            aria-label={tab.count === undefined ? undefined : `${tab.label} ${tab.count}`}
            aria-selected={activeTab === tab.id}
            className={tabClassName(compact, activeTab === tab.id)}
            id={tabButtonId(idPrefix, tab.id)}
            key={tab.id}
            onClick={() => selectTab(tab.id)}
            onKeyDown={(event) => onTabKeyDown(event, tab.id)}
            onPointerDown={() => {
              onBeforeTabChange?.(activeTab);
              pointerCapturedScroll.current = true;
            }}
            ref={(node) => {
              tabRefs.current[tab.id] = node;
            }}
            role="tab"
            tabIndex={activeTab === tab.id ? 0 : -1}
            type="button"
          >
            {tab.label}
            {/* The space is the one between the label and the count: a flex container does not
                render a white-space-only child, so this changes no pixel, and without it the
                label and the count read as one word against the name above. */}
            {tab.count === undefined ? null : (
              <>
                {" "}
                <Pill className={tabCountClassName}>{tab.count}</Pill>
              </>
            )}
          </button>
        ))}
      </div>
      {trailing}
    </div>
  );
}
