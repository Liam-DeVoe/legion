import { expect, test } from "bun:test";
import { mkdirSync, symlinkSync, writeFileSync } from "node:fs";
import path from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";

// Each row's work directory, reserved here (the fixture's hook makes it before the rows run).
const works = {
  planted: temporaryDirectory("visible-world-planted", { real: true }),
  others: temporaryDirectory("visible-world-others", { real: true }),
  clean: temporaryDirectory("visible-world-clean", { real: true }),
  stray: temporaryDirectory("visible-world-stray", { real: true }),
};

// A work directory laid out as rig.sh lays it out: runs and profiles of random names, and the map's
// links that name them (rig.sh's header, "the map"), with `files` in the run's directory.
function runIn(work: string, files: Record<string, string>) {
  const run = path.join(work, "runs", "0123456789abcdef");
  mkdirSync(path.join(run, "cwd"), { recursive: true });
  mkdirSync(path.join(work, "maps", "profiles"), { recursive: true });
  mkdirSync(path.join(work, "maps", "runs"), { recursive: true });
  for (const [label, id] of [
    ["head", "1111111111111111"],
    ["ablate-head", "2222222222222222"],
  ] as const) {
    mkdirSync(path.join(work, "profiles", id), { recursive: true });
    writeFileSync(path.join(work, "profiles", id, "checkout"), `/checkouts/${id}\n`);
    symlinkSync(`../../profiles/${id}`, path.join(work, "maps", "profiles", label));
  }
  for (const [name, id] of [
    ["reviewer-own-head-sibling-fails-ablate-head-1", "0123456789abcdef"],
    ["reviewer-own-head-ci-only-head-2", "fedcba9876543210"],
  ] as const)
    symlinkSync(`../../runs/${id}`, path.join(work, "maps", "runs", name));
  writeFileSync(path.join(run, "pane.env"), `HOME=${run}/home\nOMP_PROFILE=skills\n`);
  writeFileSync(
    path.join(run, "prompt.txt"),
    "Continue a usage page. Issue: LWEVAL-1. Pull request: #702.\n"
  );
  for (const [name, text] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(run, name)), { recursive: true });
    writeFileSync(path.join(run, name), text);
  }
  return run;
}
const search = (run: string) => {
  const result = Bun.spawnSync(["bun", path.join(import.meta.dir, "visible-world.ts"), run]);
  return { code: result.exitCode, out: result.stdout.toString() };
};

// Catches: a search that reports clean on a run whose world names its label, because it reads
// neither a file's bytes nor its path.
test("a file in the run's tree that names the run's label is a hit, by its bytes and by its path", () => {
  const run = runIn(works.planted, {
    "cwd/notes.txt": "this run is ablate-head\n",
    "cwd/ablate-head.md": "x\n",
  });
  const { code, out } = search(run);
  expect(code).toBe(1);
  expect(out).toContain(`tree\t${run}/cwd/notes.txt\tlabel (own)\tablate-head\t`);
  expect(out).toContain(`tree\t${run}/cwd/ablate-head.md (its path)\tlabel (own)\tablate-head\t`);
});

// Catches: terms taken from the run's own name alone, so another case is no hit; and a case.txt
// beside the agent's working directory read as harmless.
test("another case's name, the word ablate and a case.txt are hits too", () => {
  const run = runIn(works.others, {
    "fixture.json": '{"case":"ci-only"}\n',
    "case.txt": "x\n",
    "tmp/a": "Ablate\n",
  });
  const { code, out } = search(run);
  expect(code).toBe(1);
  expect(out).toContain(`tree\t${run}/fixture.json\tcase\tci-only\t`);
  expect(out).toContain(`tree\t${run}/case.txt\tcase-file\tcase.txt\t`);
  expect(out).toContain(`tree\t${run}/tmp/a\tword\tablate\t`);
});

// Catches: a search that flags ordinary prose for a label that is a plain word (`head`). The
// directories above the test's own (the listing channel) are the host's, so they are left out.
test("a world that names nothing has no hit in its own files, and the word head in prose is no hit", () => {
  const run = runIn(works.clean, { "cwd/README.md": "Review the head of #702.\n" });
  const { out } = search(run);
  expect(out).toMatch(/^read: env 2, command 1, tree \d+/m);
  expect(
    out.split("\n").filter((line) => /^(env|command|tree|transcript|plugins|git)\t/.test(line))
  ).toEqual([]);
});

// Catches: a clean answer from a search that read nothing.
test("a run the map does not name is refused, not reported clean", () => {
  runIn(works.stray, {});
  const stray = path.join(works.stray, "runs", "9999999999999999");
  mkdirSync(stray);
  expect(search(stray).code).toBe(2);
});
