import { expect, test } from "bun:test";
import { paneIssueBranch } from "./issue-branch";

/** A new issue's branch as the daemon allocates it: `legion/` and 16 lower-case hexadecimal
 * characters, with no key in it. */
const BRANCH = "legion/3f9a1c2b7d4e5f60";

test("the issue's branch is LEGION_BRANCH, a keyless one or a key's the daemon backfilled", () => {
  const pane = { LEGION_BRANCH: BRANCH, LEGION_ISSUE: "LEGION-208" };
  expect(paneIssueBranch(pane, "LEGION-208")).toBe(BRANCH);
  // An issue recorded before the daemon stored branches keeps `legion/<KEY>` as its stored branch,
  // and the pane is told that name like any other.
  expect(paneIssueBranch({ LEGION_BRANCH: "legion/LEGION-7" }, "LEGION-208")).toBe(
    "legion/LEGION-7"
  );
});

test("a pane of an older daemon, which sets no LEGION_BRANCH, takes legion/<issue>", () => {
  expect(paneIssueBranch({}, "LEGION-208")).toBe("legion/LEGION-208");
  expect(paneIssueBranch({ LEGION_BRANCH: "" }, "LEGION-208")).toBe("legion/LEGION-208");
});

test("a LEGION_BRANCH of another shape is refused naming the variable", () => {
  for (const value of [
    "legion/",
    "legion/-x",
    "legion/a/b",
    "legion/x#y",
    "legion/x y",
    " legion/x",
    "refs/heads/legion/x",
    "main",
  ]) {
    expect(() => paneIssueBranch({ LEGION_BRANCH: value }, "LEGION-208")).toThrow(
      new Error(
        `LEGION_BRANCH ${JSON.stringify(value)} is not an issue branch like legion/0123456789abcdef`
      )
    );
  }
});
