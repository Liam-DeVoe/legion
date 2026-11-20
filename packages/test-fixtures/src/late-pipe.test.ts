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

  test("gets every byte of a program that lets its writes drain before it ends", async () => {
    const run = await readThroughLatePipe(
      [process.execPath, "-e", `${WRITE} process.exitCode = 3;`],
      { lateMs: 1_000 }
    );
    expect(run.exitCode).toBe(3);
    expect(run.stdout.length).toBe(BYTES);
  }, 30_000);

  test("hands the command its stdin, and kills one still running at the timeout", async () => {
    const echoed = await readThroughLatePipe(["cat"], { lateMs: 0, stdin: "fed\n" });
    expect(new TextDecoder().decode(echoed.stdout)).toBe("fed\n");
    const hung = await readThroughLatePipe(["sleep", "30"], { lateMs: 0, timeoutMs: 500 });
    expect(hung.exitCode).toBeNull();
  }, 30_000);
});
