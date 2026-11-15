import { expect, jest, test } from "bun:test";
import { render, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useState } from "react";
import { jump, realClock } from "../../__tests__/fake-clock";
import { userOnFakeClock } from "../../__tests__/user";
import { InPlaceField } from "./InPlaceField";
import { SAVE_AFTER_MS } from "./useInPlaceDraft";

/**
 * The field as a page holds it: `value` is the text the server answered with, and it moves only
 * when something writes it — the reader's own write coming back, or another writer's. The two
 * buttons stand for those two moments, so a test says when each happens rather than waiting on
 * a mutation it cannot see. Every text the field wrote is recorded in `saves`, in order.
 */
function Field({ saves = [] }: { saves?: string[] }): ReactNode {
  const [value, setValue] = useState("Old name");
  const [sent, setSent] = useState<string>();
  return (
    <>
      <InPlaceField
        label="Title"
        onSave={(next) => {
          saves.push(next);
          setSent(next);
        }}
        value={value}
      />
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

/** A field whose value nothing moves, with every text it wrote recorded in `saves`, in order. */
function recordingField() {
  const saves: string[] = [];
  const view = render(
    <InPlaceField label="Title" onSave={(next) => saves.push(next)} value="Old" />
  );
  return { field: within(view.container).getByRole("textbox", { name: "Title" }), saves, view };
}

// Enter leaves the field, and leaving is what writes: the same text sent twice would be a
// second write of a priority nobody changed again.
test("Enter writes what the reader typed once", async () => {
  const user = userEvent.setup();
  const { field, saves, view } = recordingField();
  await user.type(field, " new{Enter}");
  expect(saves).toEqual(["Old new"]);
  view.unmount();
});

test("leaving the field by Tab writes what the reader typed once", async () => {
  const user = userEvent.setup();
  const { field, saves, view } = recordingField();
  await user.type(field, " new");
  await user.tab();
  expect(saves).toEqual(["Old new"]);
  view.unmount();
});

// The rest writes the text, and the write is a round trip: `value` is still the old text until
// its answer lands. A reader who leaves inside that trip has written nothing new, so leaving
// must not send the same text again. The clock is the test's, so the rest is crossed rather than
// waited out.
test("Enter after the typing rested writes what the reader typed once", async () => {
  jest.useFakeTimers();
  const user = userOnFakeClock();
  const { field, saves, view } = recordingField();
  try {
    await user.type(field, " new");
    await jump(SAVE_AFTER_MS + 1);
    expect(saves).toEqual(["Old new"]);
    await user.keyboard("{Enter}");
    expect(saves).toEqual(["Old new"]);
  } finally {
    realClock();
    view.unmount();
  }
});

test("leaving by Tab after the typing rested writes what the reader typed once", async () => {
  jest.useFakeTimers();
  const user = userOnFakeClock();
  const { field, saves, view } = recordingField();
  try {
    await user.type(field, " new");
    await jump(SAVE_AFTER_MS + 1);
    expect(saves).toEqual(["Old new"]);
    await user.tab();
    expect(saves).toEqual(["Old new"]);
  } finally {
    realClock();
    view.unmount();
  }
});

// Once the write lands the text is the server's again, so what the reader types next is a new
// text, written once however many times they rest and leave.
test("after the write lands, the next text typed is written once", async () => {
  jest.useFakeTimers();
  const user = userOnFakeClock();
  const saves: string[] = [];
  const view = render(<Field saves={saves} />);
  const page = within(view.container);
  const field = page.getByRole("textbox", { name: "Title" });
  try {
    await user.type(field, " one");
    await jump(SAVE_AFTER_MS + 1);
    await user.click(page.getByRole("button", { name: "Land the write" }));
    await user.type(field, " two");
    await jump(SAVE_AFTER_MS + 1);
    await user.tab();
    expect(saves).toEqual(["Old name one", "Old name one two"]);
  } finally {
    realClock();
    view.unmount();
  }
});

// While the write is out the text the server will hold is the one the rest wrote, not `value`:
// a reader who deletes back to the old text before the answer lands and leaves is changing the
// text again, and that text is written.
test("typing back to the old text while the write is out writes the old text", async () => {
  jest.useFakeTimers();
  const user = userOnFakeClock();
  const saves: string[] = [];
  const view = render(<Field saves={saves} />);
  const field = within(view.container).getByRole("textbox", { name: "Title" });
  try {
    await user.type(field, " new");
    await jump(SAVE_AFTER_MS + 1);
    expect(saves).toEqual(["Old name new"]);
    await user.keyboard("{Backspace}".repeat(" new".length));
    expect((field as HTMLInputElement).value).toBe("Old name");
    await user.tab();
    expect(saves).toEqual(["Old name new", "Old name"]);
  } finally {
    realClock();
    view.unmount();
  }
});

// What the field last wrote stops counting once `value` moves: another writer may have changed
// the text since, and a reader who writes their text back is changing it again.
test("a text written before the value moved is written again when the reader writes it back", async () => {
  jest.useFakeTimers();
  const user = userOnFakeClock();
  const saves: string[] = [];
  const view = render(<Field saves={saves} />);
  const page = within(view.container);
  const field = page.getByRole("textbox", { name: "Title" });
  try {
    await user.type(field, " one");
    await jump(SAVE_AFTER_MS + 1);
    await user.click(page.getByRole("button", { name: "Land the write" }));
    await user.click(page.getByRole("button", { name: "Write it elsewhere" }));
    expect((field as HTMLInputElement).value).toBe("Written elsewhere");
    await user.clear(field);
    await user.type(field, "Old name one");
    await user.tab();
    expect(saves).toEqual(["Old name one", "Old name one"]);
  } finally {
    realClock();
    view.unmount();
  }
});
