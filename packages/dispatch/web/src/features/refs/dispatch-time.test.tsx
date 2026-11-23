import { afterEach, expect, test } from "bun:test";
import { cleanup, render } from "@testing-library/react";

import { instantOf, isTimeAddress, TimedWords, timeWords } from "./dispatch-time";

// A time the command bar's agent writes is a Markdown link to its instant under `dispatch-time:`.
// Words the page draws as plain text - a plan's summary, the line beside the agent's calls - draw
// each such link as a `<time>` of the instant showing its words, the full date on hover; an
// address that names no instant draws the words alone; and Copy takes the words a reader sees.

afterEach(cleanup);

const WRITTEN =
  "Closed [5 days ago](dispatch-time:2026-11-18T11:35:00Z), reopened [later](dispatch-time:yesterday).";

test("a time's address names its instant only where it is one RFC 3339 writes", () => {
  expect(isTimeAddress("dispatch-time:2026-11-18T11:35:00Z")).toBe(true);
  expect(isTimeAddress(" Dispatch-Time:2026-11-18T11:35Z")).toBe(true);
  expect(isTimeAddress("https://example.com")).toBe(false);
  expect(isTimeAddress(undefined)).toBe(false);
  expect(instantOf("dispatch-time:2026-11-18T11:35:00Z")).toBe("2026-11-18T11:35:00Z");
  expect(instantOf("dispatch-time:2026-11-18T11:35:00.250+01:00")).toBe(
    "2026-11-18T11:35:00.250+01:00"
  );
  expect(instantOf("dispatch-time:yesterday")).toBeUndefined();
  expect(instantOf("dispatch-time:2026-11-18")).toBeUndefined();
  expect(instantOf("dispatch-time:2026-13-45T99:99:00Z")).toBeUndefined();
});

test("Copy takes each time as the words it shows, never its address", () => {
  expect(timeWords(WRITTEN)).toBe("Closed 5 days ago, reopened later.");
  expect(timeWords("No time here: [DEMO-7](dispatch://DEMO-7).")).toBe(
    "No time here: [DEMO-7](dispatch://DEMO-7)."
  );
});

test("plain words draw each time link as a time with the full date on hover, and a bad one as its words", () => {
  const view = render(
    <p>
      <TimedWords text={WRITTEN} />
    </p>
  );
  const times = view.container.querySelectorAll("time");
  expect(times).toHaveLength(1);
  expect(times[0]?.textContent).toBe("5 days ago");
  expect(times[0]?.getAttribute("datetime")).toBe("2026-11-18T11:35:00Z");
  expect(times[0]?.getAttribute("title")).toBe(new Date("2026-11-18T11:35:00Z").toLocaleString());
  expect(view.container.textContent).toBe("Closed 5 days ago, reopened later.");
});

test("words with no time link draw as they are", () => {
  const view = render(
    <p>
      <TimedWords text="Set DEMO-10 to P2." />
    </p>
  );
  expect(view.container.textContent).toBe("Set DEMO-10 to P2.");
  expect(view.container.querySelector("time")).toBeNull();
});
