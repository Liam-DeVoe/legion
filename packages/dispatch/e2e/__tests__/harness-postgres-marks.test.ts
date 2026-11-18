/** Rows of the mark's shape (`e2e/harness-postgres-marks.ts`) for the check
 *  `v-no-leftover-postgres` (requirement R3 of pull request #4236): which paths a start trusts a
 *  mark and what it names by (`whyUntrusted`). A mark another user wrote needs a second user,
 *  which a test run has none of, so these rows read owners from a stubbed `lstat`. The start's
 *  removal of a dead stack's Postgres, which acts only on what these checks trust, has its rows in
 *  `harness-postgres-removal.test.ts`. */

import { expect, test } from "bun:test";
import type { Stats } from "node:fs";

import * as marks from "../harness-postgres-marks";

/** A stubbed `lstat` over `entries`, path to what it says; any other path is gone. */
function stubbedLstat(
  entries: Record<string, { kind: "directory" | "file" | "link"; uid: number; mode: number }>
) {
  return (path: string): Stats => {
    const entry = entries[path];
    if (entry === undefined) {
      throw Object.assign(new Error(`ENOENT: ${path}`), { code: "ENOENT" });
    }
    // The fields `whyUntrusted` reads, which is all a stub of the kernel's answer needs to hold.
    const stat = {
      uid: entry.uid,
      mode: entry.mode,
      isDirectory: () => entry.kind === "directory",
      isFile: () => entry.kind === "file",
      isSymbolicLink: () => entry.kind === "link",
    } as Stats;
    return stat;
  };
}

const me = 1000;
const other = 4242;
const root = { kind: "directory", uid: 0, mode: 0o755 } as const;
const sharedTmp = { kind: "directory", uid: 0, mode: 0o1777 } as const;

test("v-no-leftover-postgres: whyUntrusted: in /tmp, whose sticky bit lets another user make a directory of a guessed name first, a run directory of another user is untrusted, and one of this user's is trusted", () => {
  const theirs = stubbedLstat({
    "/": root,
    "/tmp": sharedTmp,
    "/tmp/dispatch-e2e-run-abcdef": { kind: "directory", uid: other, mode: 0o700 },
  });
  expect(marks.whyUntrusted("/tmp/dispatch-e2e-run-abcdef", me, "directory", theirs)).toBe(
    `/tmp/dispatch-e2e-run-abcdef belongs to user ${other}`
  );
  const mine = stubbedLstat({
    "/": root,
    "/tmp": sharedTmp,
    "/tmp/dispatch-e2e-run-abcdef": { kind: "directory", uid: me, mode: 0o700 },
  });
  expect(marks.whyUntrusted("/tmp/dispatch-e2e-run-abcdef", me, "directory", mine)).toBeUndefined();
});

test("v-no-leftover-postgres: whyUntrusted: a mark file of another user is untrusted, even in a run directory of this user's", () => {
  const file = "/tmp/dispatch-e2e-run-abcdef/postgres-marks/worker-0.json";
  const lstat = stubbedLstat({
    "/": root,
    "/tmp": sharedTmp,
    "/tmp/dispatch-e2e-run-abcdef": { kind: "directory", uid: me, mode: 0o700 },
    "/tmp/dispatch-e2e-run-abcdef/postgres-marks": { kind: "directory", uid: me, mode: 0o700 },
    [file]: { kind: "file", uid: other, mode: 0o600 },
  });
  expect(marks.whyUntrusted(file, me, "file", lstat)).toBe(`${file} belongs to user ${other}`);
});

test("v-no-leftover-postgres: whyUntrusted: a directory above the path that another user owns, or that others may write without a sticky bit, or that is a link, makes the path untrusted", () => {
  const leaf = { kind: "directory", uid: me, mode: 0o700 } as const;
  const above = (entry: { kind: "directory" | "file" | "link"; uid: number; mode: number }) =>
    marks.whyUntrusted(
      "/srv/run",
      me,
      "directory",
      stubbedLstat({ "/": root, "/srv": entry, "/srv/run": leaf })
    );
  expect(above({ kind: "directory", uid: other, mode: 0o755 })).toBe(
    `/srv belongs to user ${other}`
  );
  expect(above({ kind: "directory", uid: 0, mode: 0o777 })).toBe("/srv is writable by other users");
  expect(above({ kind: "link", uid: me, mode: 0o777 })).toBe("/srv is a symbolic link");
  expect(above({ kind: "directory", uid: me, mode: 0o755 })).toBeUndefined();
});
