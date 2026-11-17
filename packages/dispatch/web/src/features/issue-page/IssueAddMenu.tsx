import { type ReactNode, useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

import type { IssueDetails } from "../../api/types";
import { pageHeaderControl, pageMenu, pageMenuItem } from "../../theme/classes";
import { assigneeControlName } from "../issue/AssigneeControl";
import { componentsControlName } from "../issue/IssueComponentsLine";
import { labelsControlName } from "../issue/IssueLabels";
import { repoControlName } from "../issue/RepoControl";
import { addPriorityControlName } from "../priorities/IssuePriorityLine";
import { roveFocus } from "../shell/roving";
import { blockedByControlName } from "./BlockedByControl";
import type { IssueField } from "./IssueFieldForms";
import { useMarginDrawer } from "./margin-slot";
import { PROPERTY_LABELS, pageChrome } from "./page-structure";
import { ANSWER_LINES, CONTROL_LABELS } from "./page-words";

/**
 * The one control that reaches a property this issue has no value for (dispatch://INFRA-1196
 * R13), in Details. Every property that has a value is drawn where it belongs - in the header or
 * in a row of Details - so nothing is hidden behind this; what it lists is exactly what is not
 * there yet, and picking one opens the control that sets it.
 *
 * Six of the fields it lists are edited by a control drawn elsewhere on the page (the assignee,
 * the labels, the issues this one waits on, the repository, the components and the priorities
 * it advances), so the menu presses those rather than holding a second copy of them. It finds
 * each by the name that control exports - `assigneeControlName`, `labelsControlName`,
 * `blockedByControlName`, `repoControlName`, `componentsControlName`, `addPriorityControlName` -
 * so the two sides cannot drift apart, and lists one only while the page draws it: a project
 * that maps no repository offers no repository to set.
 *
 * Which entries can be reached - which decides whether Add is drawn at all, and what its open
 * menu lists - is read from the page, never while the menu renders: during a render the page
 * still holds the previous commit, so a control renamed in the same commit as the issue's value
 * is not found by its new name. Read during the render, removing an issue's last label drew no
 * Add: in the render where the save landed the labels' control was still "Saving labels of
 * CORE-1", that render's commit renamed it "Add a label to CORE-1", and nothing drew the menu
 * again (dispatch://INFRA-3945 R5). So it is read after each commit of the menu, and again on
 * each change of the page that can draw, rename or remove a control an entry reaches, which a
 * `MutationObserver` on the document's body reports: most such changes are a commit of the
 * control's own component alone, which draws the menu no further time. Read after the menu's own
 * commits only, Add went when it opened the priorities' form, which replaces "Add a priority to
 * CORE-1", and stayed gone once Cancel drew that control again in a commit of
 * `IssuePriorityLine` alone; the repository's select and the components' picker, each drawn
 * once its own read answers, were never seen either (the review of 5906c38b).
 */
export function IssueAddMenu({
  isClosed,
  issue,
  onOpenField,
}: {
  isClosed: boolean;
  issue: IssueDetails;
  onOpenField: (field: IssueField) => void;
}): ReactNode {
  const [open, setOpen] = useState(false);
  // The labels of the entries whose control is on the page, in `emptyFields`' order, as `read`
  // last found them: none until the first commit, which the layout effect below reads before the
  // page is painted. A read that finds the list as it was keeps the array held, so it draws the
  // menu no further time, and the observer below, which also sees the menu's own drawing, cannot
  // loop.
  const [reachable, setReachable] = useState<readonly string[]>([]);
  const drawer = useMarginDrawer();
  const wrapper = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    // The menu opens with the focus on its first entry, so a reader on the keyboard is in it.
    menu.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
    const closeOutside = (event: PointerEvent) => {
      if (!(event.target instanceof Node) || wrapper.current?.contains(event.target) !== true) {
        setOpen(false);
      }
    };
    // Escape belongs to whatever the reader is in, as an anchored panel takes it
    // (`components/AnchoredPanel.tsx`): the menu takes a press from inside it or from Add,
    // closes and gives the focus back to Add. It reads the press in the capture phase and marks
    // it taken, so neither the phone's Details sheet, which holds the menu, nor Details' own
    // Escape at every other width closes Details on it.
    const closeOnEscape = (event: KeyboardEvent) => {
      if (
        event.key !== "Escape" ||
        !(event.target instanceof Node) ||
        wrapper.current?.contains(event.target) !== true
      ) {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      trigger.current?.focus();
      setOpen(false);
    };
    document.addEventListener("pointerdown", closeOutside);
    document.addEventListener("keydown", closeOnEscape, true);
    return () => {
      document.removeEventListener("pointerdown", closeOutside);
      document.removeEventListener("keydown", closeOnEscape, true);
    };
  }, [open]);

  const fields = emptyFields(issue);
  // The fields of the latest commit, which `read` reads: which entries the issue lists and the
  // names their controls have both follow the issue, and the observer that calls `read` is set
  // up once, so `read` takes them from here rather than from the render it was made in.
  const committed = useRef(fields);
  const read = useCallback(() => {
    const found = committed.current
      .filter((field) => field.reachable())
      .map((field) => field.label);
    setReachable((held) =>
      held.length === found.length && held.every((label, index) => label === found[index])
        ? held
        : found
    );
  }, []);
  // A render reads the previous commit, so the page is read once a commit is on it: after every
  // commit of the menu, since the issue's value and a control's name can change in the same one,
  // and on each change the observer reports, since a control drawn, renamed or removed by another
  // component's own commit draws the menu no further time. The header's assignee control is
  // outside Details, so the whole body is watched. An entry is reached by its control's name
  // alone (`pageControl`), so only the changes that draw, remove or rename an element are read.
  useLayoutEffect(() => {
    committed.current = fields;
    read();
  });
  useEffect(() => {
    const watching = new MutationObserver(read);
    watching.observe(document.body, {
      attributeFilter: ["aria-label"],
      attributes: true,
      childList: true,
      subtree: true,
    });
    return () => watching.disconnect();
  }, [read]);
  // The entries of this render's fields the page was last read to reach. A field that got a
  // value in this render is gone from them at once; one that lost it joins once the commit is read.
  const entries = fields.filter((field) => reachable.includes(field.label));

  if (isClosed || entries.length === 0) return null;
  return (
    <div className="relative flex shrink-0" ref={wrapper}>
      <button
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label={CONTROL_LABELS.add}
        className={pageHeaderControl}
        onClick={() => setOpen((shown) => !shown)}
        ref={trigger}
        type="button"
        {...pageChrome}
      >
        {CONTROL_LABELS.add}
      </button>
      {open ? (
        <div
          aria-label={CONTROL_LABELS.add}
          className={`${pageMenu} left-0`}
          // The arrow keys walk the entries, as they walk the page's other lists
          // (`shell/roving.ts`); the press is the menu's, and no key of the page's sees it.
          onKeyDown={(event) => {
            if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
            event.preventDefault();
            roveFocus(
              [...event.currentTarget.querySelectorAll<HTMLElement>('[role="menuitem"]')],
              event.target instanceof HTMLElement ? event.target : null,
              event.key === "ArrowDown" ? 1 : -1
            );
          }}
          ref={menu}
          role="menu"
        >
          {entries.map((field) => (
            <button
              className={pageMenuItem}
              key={field.label}
              onClick={() => {
                setOpen(false);
                field.open();
              }}
              role="menuitem"
              type="button"
              {...pageChrome}
            >
              {field.label}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );

  /** Every property of `issue` with no value, each with the control that sets it. */
  function emptyFields(details: IssueDetails): readonly AddableField[] {
    // The name of a control the panel draws is that control's own, built by the function it
    // exports: neither side spells it, so renaming one renames both and the entry cannot go
    // missing on a word nobody meant to change. A control of the header - the assignee - is
    // reached with Details closed, since on a phone Details covers it; a control of Details is
    // reached with Details open, since shut it takes neither a press nor the focus. The control is
    // scrolled into view first, since its popover is placed from the control's place on the screen.
    const atControl = (name: string) => ({
      open: () => {
        const control = pageControl(name);
        const reach = () => {
          control?.scrollIntoView({ block: "nearest" });
          if (control instanceof HTMLSelectElement) control.focus();
          else control?.click();
        };
        const inHeader = control?.closest("[data-issue-header]") != null;
        const shut = control?.closest("[hidden]") != null;
        if (drawer !== undefined && (inHeader ? drawer.open : shut && !drawer.open)) {
          drawer.setOpen(!inHeader);
          requestAnimationFrame(() => requestAnimationFrame(reach));
          return;
        }
        reach();
      },
      reachable: () => pageControl(name) !== null,
    });
    const fields: AddableField[] = [];
    if (details.parent === null) {
      fields.push({
        label: PROPERTY_LABELS.parent,
        open: () => onOpenField("parent"),
        reachable: () => true,
      });
    }
    if (details.assignee === null) {
      fields.push({
        label: PROPERTY_LABELS.assignee,
        ...atControl(assigneeControlName(details.key)),
      });
    }
    if ((details.labels ?? []).length === 0) {
      // Listed only while the issue has no label, so the control is found by the name it has
      // then: the words it draws and the issue's key ("Add a label to CORE-1"). A save in flight
      // renames it ("Saving labels of CORE-1"), and the busy control is not reached, so the name
      // is the one it has with no save in flight (`saving: false`).
      fields.push({
        label: PROPERTY_LABELS.labels,
        ...atControl(
          labelsControlName(details.key, {
            labels: [],
            saving: false,
            words: { ANSWER_LINES, CONTROL_LABELS },
          })
        ),
      });
    }
    if (details.route === null || details.route === "") {
      fields.push({
        label: PROPERTY_LABELS.route,
        open: () => onOpenField("route"),
        reachable: () => true,
      });
    }
    if ((details.blocked_by ?? []).length === 0) {
      fields.push({
        label: PROPERTY_LABELS.blocked_by,
        ...atControl(blockedByControlName(details.key, 0)),
      });
    }
    if (details.repo === null) {
      fields.push({
        label: PROPERTY_LABELS.repository,
        ...atControl(repoControlName(details.key)),
      });
    }
    if ((details.components.ids ?? []).length === 0) {
      // Listed only while the issue names no component, so the picker's trigger is found by the
      // name it has then, from the attachment the components line draws: the words it draws
      // and the issue's key ("Set components of CORE-1" on an issue not attached, or attached
      // to none). A save in flight renames it, and the busy trigger is not reached, so the name
      // is the one it has with no save in flight (`saving: false`).
      fields.push({
        label: PROPERTY_LABELS.components,
        ...atControl(
          componentsControlName(details.key, {
            components: details.components,
            saving: false,
            words: { ANSWER_LINES, CONTROL_LABELS },
          })
        ),
      });
    }
    if ((details.advances ?? []).length === 0) {
      fields.push({
        label: PROPERTY_LABELS.advances,
        ...atControl(addPriorityControlName(details.key)),
      });
    }
    return fields;
  }
}

/** One property with no value: what it is called, whether its control is on the page at all, and
 *  how to open it. */
interface AddableField {
  readonly label: string;
  readonly open: () => void;
  readonly reachable: () => boolean;
}

/** The control named `name`, anywhere on the page. The fields the panel draws are edited by
 *  their own controls; this is how the add menu reaches one instead of drawing it twice. */
function pageControl(name: string): HTMLElement | null {
  return document.querySelector<HTMLElement>(`[aria-label="${CSS.escape(name)}"]`);
}
