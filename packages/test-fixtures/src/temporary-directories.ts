import { afterAll, afterEach, beforeAll } from "bun:test";
import { randomBytes } from "node:crypto";
import { mkdirSync, mkdtempSync, realpathSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { onHookClock, spawnWithin } from "./row-clock";

/** What {@link temporaryDirectories} takes. */
export type TemporaryDirectoriesOptions = {
  /**
   * The directory the temporary ones are made in, `tmpdir()` by default. It is for a test that
   * needs another filesystem, such as a `tmpfs` for a rename that must cross one
   * (`packages/envoy-client/src/__tests__/envoy-config.test.ts` is the caller).
   */
  root?: string;
};

/** What {@link temporaryDirectory} takes. */
export type TemporaryDirectoryOptions = {
  /**
   * Returns the path under `realpathSync` of `tmpdir()`. A macOS `tmpdir()` is a symlink (`/var`
   * to `/private/var`), so a test that compares a path a child process printed with the path the
   * fixture returned needs the resolved one.
   */
  real?: boolean;
};

/**
 * Makes one fresh directory and returns its path. `suffix` goes into the directory's name, so a
 * test that makes several says which is which.
 */
export type TemporaryDirectoryMaker = (suffix?: string) => string;

/**
 * The limit each hook of this fixture gives Bun: 0, which Bun 1.4.4 reads as none, for a
 * synchronous hook and an asynchronous one alike (a probe's afterEach and afterAll of 6.5 s each
 * passed under it, where Bun's own 5 s failed the afterAll).
 *
 * What a hook waits for is the removal, and the removal runs as a process of its own
 * (`remove-trees.ts`) through spawnWithin, on a clock of its own at the limit the running file
 * states (`onHookClock` of `row-clock.ts`): a removal that ends passes however slow the disk, and
 * one that never ends is killed within the file's limit and fails its hook naming the directories
 * it was removing. A file that states no limit gives the removal no clock: in CI a removal there
 * that never ends is stopped by the job's own timeout, and its folder is not named.
 *
 * A limit of Bun's own would be the wrong bound. These hooks used to remove in this process, and
 * Bun judges a synchronous hook only after it returns, so a limit stopped no removal that never
 * returned and failed only one that had ended: on the devbox one removal of a row's folder of a
 * few files ran past Bun's 5 s at a 1-minute load of 476, and failed a row whose assertions had
 * all passed; a removal of a copy of the role texts took 36 s at a load of about 690
 * (`rule-search.test.ts`); and the afterAll of `score.test.ts`, removing its staged runs, ran past
 * a limit of 60 s at a load of 570.
 *
 * The making stays in this process: it is one `mkdirSync`, no tree to walk, under the same limit
 * of none.
 */
const NO_HOOK_LIMIT = 0;

/** The script each removal runs, as a process of its own. */
const REMOVAL_SCRIPT = join(import.meta.dir, "remove-trees.ts");

/**
 * Registers the removal of every directory the returned maker makes, and returns the maker.
 *
 * Call it at the test file's module scope, or in the body of a `describe`: it registers the
 * `afterEach` that removes the directories where it is called, and `bun:test` takes the scope of
 * a hook from the point of the call. The call itself makes nothing, so a scope whose hooks never
 * run leaves nothing behind.
 *
 * **Call the maker itself from a test body or from a `beforeEach`, never from a `beforeAll` and
 * never at module scope or in a `describe` body.** The maker's only removal is the `afterEach`
 * registered at the call, so a directory made in a `beforeAll` is removed after the first row of
 * the scope and every later row finds it gone; a whole file or `describe` that wants one
 * directory of its own takes {@link temporaryDirectory} instead. The maker makes its directory at
 * the call, and Bun runs a module body and a `describe` body in three cases where it then runs no
 * hook of that scope: a `bun test -t <pattern>` that selects no row of the file, one that selects
 * no row of the `describe`, and `describe.skipIf(true)`. In each of them a maker called there
 * makes its directory and nothing removes it again, and nothing runs at the end of a `bun test`
 * process to catch it.
 * {@link temporaryDirectory} is the form for one directory a whole file or `describe` shares: it
 * reserves the path at the call and makes the directory in a `beforeAll`, so the making and the
 * removal run together or not at all.
 *
 * The fixture's own module body registers nothing, because Bun evaluates an imported module once
 * per process: a hook there would land in the scope of the first file that imported it, and every
 * later file would leave its directories behind.
 *
 * The maker is synchronous, so a test body uses it without `await`.
 *
 * `options.root` is the directory the temporary ones are made in, for a test that needs another
 * filesystem.
 *
 * The `afterEach` attempts every directory even when one of them cannot be removed, since the
 * directories are independent and one refusal must not leave the rest on disk, and reports the
 * refusals together at the end.
 */
export function temporaryDirectories(
  prefix: string,
  options: TemporaryDirectoriesOptions = {}
): TemporaryDirectoryMaker {
  const root = options.root ?? tmpdir();
  const made: string[] = [];
  afterEach(() => {
    removeEvery(made.splice(0));
  }, NO_HOOK_LIMIT);
  return (suffix) => {
    const directory = mkdtempSync(join(root, nameTemplate(prefix, suffix)));
    made.push(directory);
    return directory;
  };
}

/**
 * Reserves one directory for the whole suite and returns its path: the one-shot form of
 * {@link temporaryDirectories}, for a test file or a `describe` body that needs a single scratch
 * directory every one of its tests reads.
 *
 * Call it at the test file's module scope, or in the body of a `describe`, never inside a test
 * body: it registers the `beforeAll` that makes the directory and the `afterAll` that removes it
 * where it is called, and `bun:test` takes the scope of a hook from the point of the call. The
 * path is reserved at the call and nothing is made there, so a scope Bun evaluates and then runs
 * no hook of — a `-t` filter that selects no row of the file or of the `describe`, and
 * `describe.skipIf(true)` — makes nothing and leaves nothing behind.
 *
 * Nothing exists at the path until that `beforeAll` has run. A caller writes into the directory
 * from a test body, or from a hook of its own registered below this call: Bun runs the hooks of a
 * scope in the order they were registered.
 *
 * `real` resolves `tmpdir()` once, at the call, and joins the reserved name onto the resolved
 * parent: `realpathSync` needs a path that exists, and the directory itself does not yet. This
 * form takes no `root`: nothing needs one directory for a whole file on another filesystem, and
 * the maker is what a cross-filesystem row uses.
 *
 * The name is the prefix, a dash and eight random hexadecimal characters, two more than
 * `mkdtemp` adds. The directory is made exclusively, so `mode` applies to it and a path that
 * somehow exists already fails the file rather than handing two suites one directory.
 */
export function temporaryDirectory(
  prefix: string,
  options: TemporaryDirectoryOptions = {}
): string {
  const parent = options.real === true ? realpathSync(tmpdir()) : tmpdir();
  const directory = join(
    parent,
    `${nameTemplate(prefix, undefined)}${randomBytes(4).toString("hex")}`
  );
  beforeAll(() => {
    mkdirSync(directory, { mode: 0o700 });
  }, NO_HOOK_LIMIT);
  afterAll(() => {
    removeEvery([directory]);
  }, NO_HOOK_LIMIT);
  return directory;
}

/** `-`, the one character the name template trims from a caller's prefix. */
const DASH = 45;

/**
 * The name both forms build on: the prefix, the suffix when the caller named one, and one dash,
 * whether or not the prefix already ends with dashes. `temporaryDirectories` hands it to
 * `mkdtemp`, which adds six random characters to it; `temporaryDirectory` adds eight of its own.
 *
 * The trailing dashes are counted rather than matched: a `/-+$/` on a caller's own string is a
 * polynomial regular expression on library input, which CodeQL refuses as a high-severity alert
 * (a prefix of many dashes would make it slow), and this costs one pass of the string's tail.
 */
function nameTemplate(prefix: string, suffix: string | undefined): string {
  const stem = suffix === undefined || suffix === "" ? prefix : `${prefix}-${suffix}`;
  let end = stem.length;
  while (end > 0 && stem.charCodeAt(end - 1) === DASH) end -= 1;
  return `${stem.slice(0, end)}-`;
}

/**
 * Removes every directory of `directories` in a process of its own (`remove-trees.ts`, which
 * removes them newest first, attempts each one even when another refuses, and reports the
 * refusals together), on the hook's own clock (NO_HOOK_LIMIT says why). Its working directory is
 * this package's, whatever the caller's is: bun reads a `bunfig.toml` from the directory it
 * starts in, and a test's own could preload something into the removal.
 */
function removeEvery(directories: readonly string[]): void {
  if (directories.length === 0) return;
  const removal = onHookClock(() =>
    spawnWithin(
      {
        part: `the removal of ${directories.join(", ")}`,
        boundMs: Number.POSITIVE_INFINITY,
      },
      [process.execPath, REMOVAL_SCRIPT, ...directories],
      { cwd: import.meta.dir }
    )
  );
  if (removal.exitCode !== 0) {
    const said = removal.stderr.toString().trim();
    throw new Error(
      said === "" ? `the removal of ${directories.join(", ")} exited ${removal.exitCode}` : said
    );
  }
}
