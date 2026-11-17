import { expect, test } from "bun:test";
// biome-ignore lint/style/noRestrictedImports: the press case needs a press, a blur and a removal inside one task, and user-event's press awaits between its events.
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useRef } from "react";

import { emulateChromiumFocusFixup } from "./chromium-focus-fixup";
import { useFocusKeeper } from "./focus-keeper";

/** What has become of the region's Save control: drawn, disabled, removed, or drawn anew under
 *  another key, which React draws as a new element in the old one's place. */
type SaveControl = "drawn" | "disabled" | "removed" | "replaced";

/**
 * A region whose focus the keeper keeps: a Save control the props disable, remove or replace, a
 * sibling the props remove, and two fallback buttons. The first fallback finds Save's replacement
 * among what the change added; the other two are the fallback buttons, in order.
 */
function Region({
  firstFallbackDisabled = false,
  save = "drawn",
  sibling = true,
}: {
  firstFallbackDisabled?: boolean;
  save?: SaveControl;
  sibling?: boolean;
}) {
  const region = useRef<HTMLDivElement>(null);
  // A new array each render, as a caller writes it.
  useFocusKeeper(region, [
    ({ added, lost }) => added.find((element) => element.textContent === lost.textContent),
    ({ root }) => root.querySelector<HTMLElement>("[data-fallback='first']"),
    ({ root }) => root.querySelector<HTMLElement>("[data-fallback='second']"),
  ]);
  return (
    <div ref={region}>
      {save === "removed" ? null : (
        <button
          disabled={save === "disabled"}
          key={save === "replaced" ? "anew" : "first"}
          type="button"
        >
          Save
        </button>
      )}
      {sibling ? <button type="button">Sibling</button> : null}
      <button data-fallback="first" disabled={firstFallbackDisabled} type="button">
        First fallback
      </button>
      <button data-fallback="second" type="button">
        Second fallback
      </button>
    </div>
  );
}

function button(name: string): HTMLElement {
  return screen.getByRole("button", { name });
}

test("a focused control that turns disabled hands the focus to the first fallback", async () => {
  const stopFixup = emulateChromiumFocusFixup();
  const view = render(<Region />);
  try {
    act(() => button("Save").focus());
    view.rerender(<Region save="disabled" />);
    await waitFor(() => expect(button("First fallback")).toHaveFocus());
  } finally {
    view.unmount();
    stopFixup();
  }
});

test("a focused control that is removed hands the focus to the first fallback", async () => {
  const view = render(<Region />);
  try {
    act(() => button("Save").focus());
    view.rerender(<Region save="removed" />);
    await waitFor(() => expect(button("First fallback")).toHaveFocus());
  } finally {
    view.unmount();
  }
});

test("a focused control drawn anew hands the focus to the replacement a fallback finds", async () => {
  const view = render(<Region />);
  try {
    const before = button("Save");
    act(() => before.focus());
    view.rerender(<Region save="replaced" />);
    // Only the fallback that reads the change's added elements finds the new Save: without them,
    // the focus would land on the first fallback button.
    await waitFor(() => expect(button("Save")).toHaveFocus());
    expect(button("Save")).not.toBe(before);
  } finally {
    view.unmount();
  }
});

test("a fallback that cannot hold the focus is passed over for the next one", async () => {
  const view = render(<Region firstFallbackDisabled />);
  try {
    act(() => button("Save").focus());
    view.rerender(<Region firstFallbackDisabled save="removed" />);
    await waitFor(() => expect(button("Second fallback")).toHaveFocus());
  } finally {
    view.unmount();
  }
});

test("a focus the reader put on the body stays there when the control is then disabled", async () => {
  const stopFixup = emulateChromiumFocusFixup();
  const view = render(<Region />);
  try {
    act(() => button("Save").focus());
    act(() => button("Save").blur());
    // The keeper's check of the blur runs here.
    await act(async () => {});
    view.rerender(<Region save="disabled" />);
    await act(async () => {});
    expect(document.body).toHaveFocus();
  } finally {
    view.unmount();
    stopFixup();
  }
});

test("a focus on a control stays there when a sibling is removed", async () => {
  const view = render(<Region />);
  try {
    act(() => button("Save").focus());
    view.rerender(<Region sibling={false} />);
    await act(async () => {});
    expect(button("Save")).toHaveFocus();
  } finally {
    view.unmount();
  }
});

test("a press on the page is the reader's move, even when the control then goes", async () => {
  const view = render(<Region />);
  try {
    act(() => button("Save").focus());
    // A press on a blank part of the page blurs the control, and the blur's own handler may
    // remove it before the keeper's check runs. user-event cannot hold all three in one task: its
    // press awaits between its events, and the keeper's check would run in that gap.
    fireEvent.pointerDown(document.body);
    act(() => button("Save").blur());
    view.rerender(<Region save="removed" />);
    await act(async () => {});
    expect(document.body).toHaveFocus();
  } finally {
    view.unmount();
  }
});

test("a press on the focused control is no move of the reader's when its write disables it", async () => {
  const user = userEvent.setup();
  const stopFixup = emulateChromiumFocusFixup();
  const view = render(<Region />);
  try {
    act(() => button("Save").focus());
    // The press focuses nothing anew, as the control already holds the focus.
    await user.click(button("Save"));
    view.rerender(<Region save="disabled" />);
    await waitFor(() => expect(button("First fallback")).toHaveFocus());
  } finally {
    view.unmount();
    stopFixup();
  }
});
