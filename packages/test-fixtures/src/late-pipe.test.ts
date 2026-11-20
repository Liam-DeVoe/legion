import { describe, expect, test } from "bun:test";
import { skipWithout } from "./host-tools";
import { readThroughLatePipe } from "./late-pipe";

const noPython = skipWithout("python3");
const BYTES = 200_000;
/** A Bun program's one write of BYTES bytes, which each case ends its own way. */
const WRITE = `process.stdout.write("x".repeat(${BYTES - 1}) + "\\n");`;

describe.skipIf(noPython)("readThroughLatePipe", () => {
  test("hands a program a one-page pipe, so process.exit after a write past it reaches the reader cut", async () => {
    const run = await readThroughLatePipe([process.execPath, "-e", `${WRITE} process.exit(0);`], {
      lateMs: 1_000,
    });
    expect(run.pipeBytes).toBe(4_096);
    expect(run.exitCode).toBe(0);
    expect(run.stdout.length).toBeLessThan(BYTES);
  }, 30_000);

  // The two cases above hold the pipe's size, not its reader's lateness: a process.exit right
  // after a write past a page is cut even by a reader that reads at once. Here the program pauses
  // 300 ms between its write and its exit, in which a reader that reads at once drains both pages;
  // a reader that is late, as every case of the consumers needs, does not. lateMs is far past the
  // pause so that bun's start-up on a loaded machine (seconds, at worst) cannot reach it.
  test("reads nothing before lateMs, so a program that pauses after two pages and then exits reaches it cut", async () => {
    const run = await readThroughLatePipe(
      [
        process.execPath,
        "-e",
        'process.stdout.write("x".repeat(8_191) + "\\n"); setTimeout(() => process.exit(0), 300);',
      ],
      { lateMs: 5_000 }
    );
    expect(run.pipeBytes).toBe(4_096);
    expect(run.exitCode).toBe(0);
    expect(run.stdout.length).toBeLessThan(8_192);
  }, 30_000);

  test("gets every byte of a program that lets its writes drain before it ends", async () => {
    const run = await readThroughLatePipe(
      [process.execPath, "-e", `${WRITE} process.exitCode = 3;`],
      { lateMs: 1_000 }
    );
    expect(run.exitCode).toBe(3);
    expect(run.stdout.length).toBe(BYTES);
  }, 30_000);

  test("kills a program held mid-write once its pipe is full, and reads only what the pipe held", async () => {
    // The write past a page waits on the reader, and the timer keeps the program from ending.
    const killed = await readThroughLatePipe(
      [process.execPath, "-e", `${WRITE} setTimeout(() => undefined, 60_000);`],
      { killWhenFullMs: 100 }
    );
    expect(killed.killedWhenFull).toBe(true);
    expect(killed.exitCode).toBeNull();
    expect(killed.stdout.length).toBe(killed.pipeBytes);
    // A program that ends before its pipe is full is read as it ends, never killed.
    const ended = await readThroughLatePipe(
      [process.execPath, "-e", 'process.stdout.write("x\\n");'],
      { killWhenFullMs: 100 }
    );
    expect(ended.killedWhenFull).toBe(false);
    expect(ended.exitCode).toBe(0);
    expect(new TextDecoder().decode(ended.stdout)).toBe("x\n");
  }, 30_000);

  test("hands the command its stdin, and kills one still running at the timeout", async () => {
    const echoed = await readThroughLatePipe(["cat"], { lateMs: 0, stdin: "fed\n" });
    expect(new TextDecoder().decode(echoed.stdout)).toBe("fed\n");
    const hung = await readThroughLatePipe(["sleep", "30"], { lateMs: 0, timeoutMs: 500 });
    expect(hung.exitCode).toBeNull();
  }, 30_000);
});
