import { describe, expect, test } from "bun:test";
import { fileURLToPath } from "node:url";
import { isAncestor, nearestAncestorNamed } from "../process-ancestry";

describe("isAncestor", () => {
  // pid 40 <- 30 <- 20 <- 1, read through a scripted parent, so the rows hold the walk wherever
  // the test runner stands: as PID 1 of its PID namespace its own pid is 1 and its parent 0.
  const parents = new Map([
    [40, 30],
    [30, 20],
    [20, 1],
  ]);
  const parent = (pid: number) => parents.get(pid);

  test("finds the process itself and each process above it", () => {
    for (const candidate of ["40", "30", "20"]) {
      expect(isAncestor(candidate, 40, parent), candidate).toBe(true);
    }
    expect(isAncestor("30", 30, parent)).toBe(true);
  });

  test("finds no process below or beside the chain, and never init", () => {
    expect(isAncestor("40", 30, parent)).toBe(false);
    expect(isAncestor("50", 40, parent)).toBe(false);
    // Init is above every process, so being below it tells no chain apart, and it is never a
    // match, even where it is the process the walk starts at.
    expect(isAncestor("1", 40, parent)).toBe(false);
    expect(isAncestor("1", 1, parent)).toBe(false);
  });

  test("stops where a parent cannot be read", () => {
    expect(isAncestor("30", 40, () => undefined)).toBe(false);
  });

  // The machine's own parent reader, from inside a process two below the runner: a `bash` the
  // runner starts, which runs a `bun` of its own rather than in its own place. Neither is the
  // runner or its parent, so neither is pid 1 or 0 wherever the runner stands.
  test("reads this machine's chain: a process and its parent, and not a process that has ended", () => {
    const module = fileURLToPath(new URL("../process-ancestry.ts", import.meta.url));
    const script =
      `import { isAncestor } from ${JSON.stringify(module)};\n` +
      `const ended = Bun.spawnSync(["true"]).pid;\n` +
      "process.stdout.write(JSON.stringify({ pid: process.pid, parent: process.ppid, " +
      "self: isAncestor(String(process.pid)), above: isAncestor(String(process.ppid)), " +
      "ended: isAncestor(String(ended)) }));";
    const run = Bun.spawnSync(["bash", "-c", '"$0" -e "$1"; exit $?', process.execPath, script]);
    expect(run.stderr.toString()).toBe("");
    const read = JSON.parse(run.stdout.toString());
    expect(read.pid).toBeGreaterThan(1);
    expect(read.parent).toBeGreaterThan(1);
    expect({ self: read.self, above: read.above, ended: read.ended }).toEqual({
      self: true,
      above: true,
      ended: false,
    });
  });
});

describe("nearestAncestorNamed", () => {
  // pid 40 (bash) <- 30 (codex) <- 20 (bash) <- 10 (claude) <- 1
  const table = new Map([
    [40, { command: "bash", parent: 30 }],
    [30, { command: "codex", parent: 20 }],
    [20, { command: "bash", parent: 10 }],
    [10, { command: "claude", parent: 1 }],
  ]);
  const reader = {
    command: (pid: number) => table.get(pid)?.command,
    parent: (pid: number) => table.get(pid)?.parent,
  };

  test("answers the nearest of the names, walking up from the process", () => {
    expect(nearestAncestorNamed(["codex", "claude"], 40, reader)).toBe("codex");
    expect(nearestAncestorNamed(["codex", "claude"], 20, reader)).toBe("claude");
    expect(nearestAncestorNamed(["claude"], 40, reader)).toBe("claude");
  });

  test("reads Claude Code's npm binary, claude.exe, as claude", () => {
    // pid 3 (bash) <- 2 (claude.exe) <- 1 (codex)
    const npm = new Map([
      [3, { command: "bash", parent: 2 }],
      [2, { command: "claude.exe", parent: 5 }],
      [5, { command: "codex", parent: 1 }],
    ]);
    const npmReader = {
      command: (pid: number) => npm.get(pid)?.command,
      parent: (pid: number) => npm.get(pid)?.parent,
    };
    expect(nearestAncestorNamed(["codex", "claude"], 3, npmReader)).toBe("claude");
  });

  test("answers undefined when no ancestor runs as any of them", () => {
    expect(nearestAncestorNamed(["omp"], 40, reader)).toBeUndefined();
    expect(nearestAncestorNamed(["codex"], 999, reader)).toBeUndefined();
  });
});
