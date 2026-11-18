import { expect, test } from "bun:test";
import { act, render, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useState } from "react";
import { jump, onFakeClock } from "../../__tests__/fake-clock";
import { InPlaceField, SAVE_AFTER_MS } from "./InPlaceField";

/**
 * The field as a page holds it: `value` is the text the server answered with, and it moves only
 * when something writes it — the reader's own write coming back, or another writer's. The two
 * buttons stand for those two moments, so a test says when each happens rather than waiting on
 * a mutation it cannot see.
 */
function Field(): ReactNode {
  const [value, setValue] = useState("Old name");
  const [sent, setSent] = useState<string>();
  return (
    <>
      <InPlaceField label="Title" onSave={setSent} value={value} />
      <button onClick={() => setValue(sent ?? value)} type="button">
        Land the write
      </button>
      <button onClick={() => setValue("Written elsewhere")} type="button">
        Write it elsewhere
      </button>
    </>
  );
}

// Queries are scoped to this render's container: bun runs every test file in one document, and
// a sibling file's un-unmounted render would otherwise leak its fields into a `screen` query.
test("the text a reader left stands through the write, rather than the old text returning", async () => {
  const user = userEvent.setup();
  const view = render(<Field />);
  const page = within(view.container);
  const field = page.getByRole("textbox", { name: "Title" });
  await user.clear(field);
  await user.type(field, "New name");
  // Leaving the field sends the write. The answer is not back, so the value under the field is
  // still the old text; the reader must not see their own words undone while it travels.
  await user.tab();
  expect((field as HTMLInputElement).value).toBe("New name");
  await user.click(page.getByRole("button", { name: "Land the write" }));
  expect((field as HTMLInputElement).value).toBe("New name");
  view.unmount();
});

test("a text written elsewhere replaces what a field the reader is not holding shows", async () => {
  const user = userEvent.setup();
  const view = render(<Field />);
  const page = within(view.container);
  await user.click(page.getByRole("button", { name: "Write it elsewhere" }));
  expect((page.getByRole("textbox", { name: "Title" }) as HTMLInputElement).value).toBe(
    "Written elsewhere"
  );
  view.unmount();
});

/** The field as a page holds it while another person's write lands: every write the field sends
 *  is kept in `writes`, and `land.write` stands for the server's answer moving `value` under the
 *  field, as a refresh of the row does, without taking the focus from it. */
function HeldField({
  land,
  writes,
}: {
  land: { write?: (text: string) => void };
  writes: string[];
}): ReactNode {
  const [value, setValue] = useState("Old name");
  land.write = setValue;
  return <InPlaceField label="Title" onSave={(next) => writes.push(next)} value={value} />;
}

test("leaving a held field with nothing typed writes nothing, and shows what was written meanwhile", async () => {
  const user = userEvent.setup();
  const writes: string[] = [];
  const land: { write?: (text: string) => void } = {};
  const view = render(<HeldField land={land} writes={writes} />);
  const field = within(view.container).getByRole("textbox", { name: "Title" }) as HTMLInputElement;
  await user.click(field);
  // Another person's rename lands while the reader holds the field.
  act(() => land.write?.("Written elsewhere"));
  expect(document.activeElement).toBe(field);
  await user.tab();
  // The reader typed nothing, so leaving writes nothing. A write of "Old name" here takes the
  // other person's rename back, which nobody asked for, and the field goes on showing it.
  expect(writes).toEqual([]);
  expect(field.value).toBe("Written elsewhere");
  view.unmount();
});

/** The field as `HeldField` draws it, with what a test reads of it and the notice beside it. */
function heldField() {
  const writes: string[] = [];
  const land: { write?: (text: string) => void } = {};
  const view = render(<HeldField land={land} writes={writes} />);
  const page = within(view.container);
  return {
    field: page.getByRole("textbox", { name: "Title" }) as HTMLInputElement,
    land: (text: string) => act(() => land.write?.(text)),
    notice: () => page.queryByRole("alert", { name: "Title, changed by another person" }),
    view,
    writes,
  };
}

// The rows below type on the fake clock, with no wait between keys (`delay: null`), so the write
// that waits for the typing to rest goes only when a row moves the clock.
test("another person's text landing under the reader's typing is shown, theirs kept beside it, and written only when they use it", async () => {
  const user = userEvent.setup({ delay: null });
  const { field, land, notice, view, writes } = heldField();
  await onFakeClock(async () => {
    await user.type(field, ", mine");
    land("Written elsewhere");
    // The write that waited for the typing to rest is stopped: it would write over theirs.
    await jump(SAVE_AFTER_MS);
  });
  expect(writes).toEqual([]);
  expect(field.value).toBe("Written elsewhere");
  const kept = notice();
  if (kept === null) throw new Error("no notice beside the field");
  expect(kept.textContent).toContain("Another person changed this while you wrote. Your text:");
  expect(within(kept).getByText("Old name, mine")).toBeDefined();
  // Leaving the field writes nothing: it holds the server's text.
  await user.tab();
  expect(writes).toEqual([]);
  await user.click(within(kept).getByRole("button", { name: "Use my text" }));
  expect(writes).toEqual(["Old name, mine"]);
  expect(field.value).toBe("Old name, mine");
  expect(notice()).toBeNull();
  // Its round trip is the reader's own text coming back, and no other person's.
  land("Old name, mine");
  expect(notice()).toBeNull();
  view.unmount();
  expect(writes).toEqual(["Old name, mine"]);
});

test("keeping the other person's text lets the reader's go, and writes nothing", async () => {
  const user = userEvent.setup({ delay: null });
  const { field, land, notice, view, writes } = heldField();
  await onFakeClock(async () => {
    await user.type(field, ", mine");
    land("Written elsewhere");
  });
  const kept = notice();
  if (kept === null) throw new Error("no notice beside the field");
  await user.click(within(kept).getByRole("button", { name: "Keep their text" }));
  expect(notice()).toBeNull();
  expect(field.value).toBe("Written elsewhere");
  await user.tab();
  view.unmount();
  expect(writes).toEqual([]);
});

test("the answer to a write made as the reader types is their own text, trimmed as it is stored, and leaves the typing alone", async () => {
  const user = userEvent.setup({ delay: null });
  const { field, land, notice, view, writes } = heldField();
  await onFakeClock(async () => {
    await user.type(field, " now ");
    await jump(SAVE_AFTER_MS);
    expect(writes).toEqual(["Old name now"]);
    await user.type(field, "and then");
    land("Old name now");
  });
  expect(notice()).toBeNull();
  expect(field.value).toBe("Old name now and then");
  await user.tab();
  expect(writes).toEqual(["Old name now", "Old name now and then"]);
  view.unmount();
  expect(writes).toEqual(["Old name now", "Old name now and then"]);
});

test("a field taken off the page while its typing waits writes it", async () => {
  const user = userEvent.setup({ delay: null });
  const { field, view, writes } = heldField();
  await onFakeClock(async () => {
    await user.type(field, " now");
    expect(writes).toEqual([]);
    view.unmount();
    expect(writes).toEqual(["Old name now"]);
    await jump(SAVE_AFTER_MS);
  });
  expect(writes).toEqual(["Old name now"]);
});

test("the page going while the typing waits writes it, once", async () => {
  const user = userEvent.setup({ delay: null });
  const { field, view, writes } = heldField();
  await onFakeClock(async () => {
    await user.type(field, " now");
    expect(writes).toEqual([]);
    act(() => {
      window.dispatchEvent(new Event("pagehide"));
    });
    expect(writes).toEqual(["Old name now"]);
    // That was the write waiting for the typing to rest: none goes once the typing has rested.
    await jump(SAVE_AFTER_MS);
  });
  expect(writes).toEqual(["Old name now"]);
  view.unmount();
  expect(writes).toEqual(["Old name now"]);
});
