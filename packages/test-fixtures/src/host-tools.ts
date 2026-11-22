import { accessSync, constants } from "node:fs";

/** The path of the program on this host's `PATH`, or null when the host does not have it. */
export function hostTool(name: string): string | null {
  return Bun.which(name);
}

/**
 * Whether this host is missing any of the named programs, for `describe.skipIf(...)` and
 * `test.skipIf(...)`. It prints one warning that names every missing program and the file whose
 * tests will not run, so a reader of the CI output knows what to install. With no names it is
 * false and prints nothing.
 *
 * Call it once, at the test file's module scope, and hand the result to every `skipIf` of the
 * file: a call per test would print the warning per test.
 */
export function skipWithout(...names: string[]): boolean {
  const missing = names.filter((name) => hostTool(name) === null);
  if (missing.length === 0) return false;
  const file = callerFile(new Error().stack);
  const where = file === undefined ? "in this file" : `in ${file}`;
  console.warn(
    `Skipped the tests ${where}. This host does not have: ${missing.join(", ")}. ` +
      "Install the missing programs and run the tests again."
  );
  return true;
}

/** What this host answers for a file a row reads: see `hostFile`. */
export interface HostFile {
  /** Whether the host has no such file: looking it up failed with ENOENT, the one answer a row
   *  that reads it may skip on (`test.skipIf(file.missing)`). */
  readonly missing: boolean;
  /** The error of any other failure of that lookup, EACCES among them, or undefined where the file
   *  can be read or is missing. Such a file is there and cannot be read, which is no reason to
   *  skip: a row that reads the file throws this first, so it fails naming the error. */
  readonly failed: Error | undefined;
}

/**
 * Whether this host can read the file at `path`, for a row that reads it: `missing` only where
 * the lookup failed with ENOENT, and every other failure in `failed`, for the row to throw.
 * `existsSync` answers false for both, so a row skipped on it skips, and says the file is missing,
 * where the file is there and its read is refused. Where the file is missing this prints one
 * warning, naming `path`, what is skipped (`skipped`) and the calling file, as `skipWithout` does.
 *
 * Call it once, at the test file's module scope, as `skipWithout`.
 */
export function hostFile(path: string, skipped: string): HostFile {
  try {
    accessSync(path, constants.R_OK);
    return { missing: false, failed: undefined };
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") {
      return { missing: false, failed: error as Error };
    }
  }
  const file = callerFile(new Error().stack);
  const where = file === undefined ? "in this file" : `in ${file}`;
  console.warn(`Skipped ${skipped} ${where}. This host has no ${path}.`);
  return { missing: true, failed: undefined };
}

/**
 * The file that called `skipWithout` or `hostFile`: the first frame of the stack that is not this
 * module's own. Without it the warning says only what is missing, and a reader of a run of
 * hundreds of files cannot tell whose tests did not run.
 *
 * A frame is `at <name> (<path>:<line>:<col>)`, or `at <path>:<line>:<col>` where it has no name.
 * The path is read whole, from the parenthesis or from `at ` up to the last `:<line>:<col>` of the
 * frame, so a checkout under a directory whose name holds a space — `/home/u/my repo/pkg/a.test.ts`
 * — is named as it is rather than cut at the space.
 */
function callerFile(stack: string | undefined): string | undefined {
  if (stack === undefined) return undefined;
  for (const line of stack.split("\n")) {
    const frame = line.trimEnd();
    const file = /\((.+):\d+:\d+\)$/.exec(frame)?.[1] ?? /^\s*at (.+):\d+:\d+$/.exec(frame)?.[1];
    if (file !== undefined && file !== import.meta.path) return file;
  }
  return undefined;
}
