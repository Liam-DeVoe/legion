import { expect, test } from "bun:test";
import { existsSync, lstatSync, mkdirSync, symlinkSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { snapshotTag } from "./arms";
import { answerCall, READ_ONLY_TOOLS, readOnlyCall, removeOutsideLinks } from "./checkout";

const scratch = temporaryDirectory("skill-cuts-checkout", { real: true });

/** The made-up session's checkout root, as a written case names it. */
const ROOT = "/work/acme/proj";
/** A made-up secret in a file outside every copy, which no answer may show. */
const SECRET = fixtureSecret("outside");

/** A copy of a made-up checkout under `name`: docs/guide.md, a link inside it to that file, and
 *  links that leave it (to a file and a directory holding SECRET outside the copy) or resolve
 *  nowhere. */
function linkedCopy(name: string): string {
  const outside = path.join(scratch, `${name}-outside`);
  mkdirSync(path.join(outside, "dir"), { recursive: true });
  writeFileSync(path.join(outside, "secret.txt"), `${SECRET}\n`);
  writeFileSync(path.join(outside, "dir", "secret.txt"), `${SECRET}\n`);
  const dir = path.join(scratch, name);
  mkdirSync(path.join(dir, "docs"), { recursive: true });
  writeFileSync(path.join(dir, "docs", "guide.md"), "# Guide\n\nRun the app.\n");
  symlinkSync("docs/guide.md", path.join(dir, "inside.md"));
  symlinkSync(path.join(outside, "secret.txt"), path.join(dir, "leak.txt"));
  symlinkSync(path.join(outside, "dir"), path.join(dir, "docs", "up"));
  symlinkSync("missing.txt", path.join(dir, "gone"));
  return dir;
}

// Catches: a model-written read-only call of a written case's run that reads a file outside the
// copy of its checkout (a key file of the pod) through a symbolic link of the repository, or
// through a word the confinement check does not resolve, so its text reaches the model and the run
// records.
test("removeOutsideLinks removes each link that resolves outside the copy or nowhere and keeps one inside; no read, grep, glob or bash answer shows a file outside the copy", async () => {
  const dir = linkedCopy("copy");
  expect(removeOutsideLinks(dir)).toEqual(["docs/up", "gone", "leak.txt"]);
  expect(lstatSync(path.join(dir, "inside.md")).isSymbolicLink()).toBe(true);
  for (const removed of ["leak.txt", "docs/up", "gone"]) {
    expect({ removed, there: existsSync(path.join(dir, removed)) }).toEqual({
      removed,
      there: false,
    });
  }
  const copy = { dir, root: ROOT, cwd: ROOT, git: false };
  const answers = await Promise.all([
    answerCall(copy, "read", { path: "leak.txt" }, "hashline"),
    answerCall(copy, "grep", { pattern: SECRET.slice(0, 12), path: "." }, "hashline"),
    answerCall(copy, "bash", { command: "cat leak.txt" }, "hashline"),
    answerCall(copy, "bash", { command: `grep -R ${SECRET.slice(0, 12)} .` }, "hashline"),
    answerCall(copy, "bash", { command: "find -L . -type f | xargs cat" }, "hashline"),
    answerCall(copy, "bash", { command: "cat inside.md" }, "hashline"),
  ]);
  expect(answers.map((each) => ("why" in each ? `why: ${each.why}` : each.text))).toEqual([
    "Path 'leak.txt' not found",
    "No matches found",
    "cat: leak.txt: No such file or directory\n\n\nCommand exited with code 1",
    "(no output)\n\nCommand exited with code 1",
    "why: its command is not read-only",
    "# Guide\n\nRun the app.\n",
  ]);
  expect(JSON.stringify(answers)).not.toContain(SECRET);

  // A copy whose links were left in place: a read, grep or glob through one is not answered.
  const kept = { dir: linkedCopy("kept"), root: ROOT, cwd: ROOT, git: false };
  for (const [name, input] of [
    ["read", { path: "leak.txt" }],
    ["read", { path: "docs/up/secret.txt" }],
    ["grep", { pattern: "x", path: "leak.txt" }],
    ["grep", { pattern: "x", path: "docs/up" }],
    ["glob", { path: "docs/up/*.txt" }],
  ] as const) {
    const answer = await answerCall(kept, name, input, "hashline");
    expect({ name, input, answer }).toEqual({
      name,
      input,
      answer: { why: expect.stringContaining("through a symbolic link, outside the checkout") },
    });
  }

  // A bash word that may lead outside the copy, however it gets there, is not answered.
  const deeper = { ...copy, cwd: `${ROOT}/docs` };
  for (const [command, why] of [
    ["cat $HOME/x", "$HOME/x expands"],
    ["cat {/etc/hostname,x}", "{/etc/hostname,x} expands"],
    ["cat ~/x", "~/x is under ~"],
    ["cat .git", ".git names .git"],
    ["cat /etc/hostname", "/etc/hostname is outside the session's checkout"],
    ["grep -f/etc/hostname guide.md", "-f/etc/hostname is outside the session's checkout"],
    ["cd .. && cat ../x", "../x is outside the session's checkout"],
  ] as const) {
    const answer = await answerCall(deeper, "bash", { command }, "hashline");
    expect({ command, answer }).toEqual({
      command,
      answer: { why: expect.stringContaining(why) },
    });
  }
});

// Catches: a read of a code file the pin may summarize, or a ranged read, left unanswered (a run
// then judged on that read), a range answered past the lines the pin shows, or a read with another
// selector answered in a form the runner does not rebuild.
test("a read of a code file of 100 lines or more is answered unsummarized and marked so, one past 300 lines with its first 300 and the count of the rest; a ranged read with the lines of its range, a line before and three after, and the pin's count of the lines left; any other selector is not answered", async () => {
  const dir = path.join(scratch, "ranges");
  mkdirSync(path.join(dir, "src"), { recursive: true });
  mkdirSync(path.join(dir, "docs"), { recursive: true });
  const big = Array.from({ length: 120 }, (_, n) => `x${n + 1} = ${n + 1}\n`).join("");
  writeFileSync(path.join(dir, "src", "big.py"), big);
  writeFileSync(path.join(dir, "docs", "guide.md"), "# Guide\n\nRun the app.\n");
  const copy = { dir, root: ROOT, cwd: ROOT, git: false, answers: new Map() };
  const lines = big.replace(/\n$/, "").split("\n");
  const rows = (from: number, to: number) =>
    lines
      .slice(from - 1, to)
      .map((line, index) => `${from + index}:${line}`)
      .join("\n");
  const header = `[src/big.py#${snapshotTag(big)}]`;
  const read = (target: string) => answerCall(copy, "read", { path: target }, "hashline");
  expect(await read("src/big.py")).toEqual({
    text: `${header}\n${rows(1, 120)}`,
    isError: false,
    wholeText: true,
  });
  // A file past 300 lines, paged as the pin pages it unsummarized.
  const long = Array.from({ length: 350 }, (_, n) => `y${n + 1} = ${n + 1}\n`).join("");
  writeFileSync(path.join(dir, "src", "long.py"), long);
  const longRows = long
    .split("\n")
    .slice(0, 300)
    .map((line, index) => `${index + 1}:${line}`)
    .join("\n");
  expect(await read("src/long.py")).toEqual({
    text: `[src/long.py#${snapshotTag(long)}]\n${longRows}\n\n[50 more lines in file. Use :301 to continue]`,
    isError: false,
    wholeText: true,
  });
  for (const [target, text] of [
    [
      "src/big.py:10-12",
      `${header}\n${rows(9, 15)}\n\n[105 more lines in file. Use :16 to continue]`,
    ],
    ["src/big.py:5+2", `${header}\n${rows(4, 9)}\n\n[111 more lines in file. Use :10 to continue]`],
    ["src/big.py:1..2", `${header}\n${rows(1, 5)}\n\n[115 more lines in file. Use :6 to continue]`],
    [`${ROOT}/src/big.py:119-120`, `[src/big.py#${snapshotTag(big)}]\n${rows(118, 120)}`],
    // From line 2 on: no trailing context, and the rest of the file.
    [
      "docs/guide.md:2",
      `[docs/guide.md#${snapshotTag("# Guide\n\nRun the app.\n")}]\n1:# Guide\n2:\n3:Run the app.`,
    ],
  ] as const) {
    expect({ target, answer: await read(target) }).toEqual({
      target,
      answer: { text, isError: false },
    });
  }
  for (const target of [
    "src/big.py:raw",
    "src/big.py:1-2,5-6",
    "src/big.py:-5",
    "src/big.py:200",
  ]) {
    expect({ target, answer: await read(target) }).toEqual({
      target,
      answer: { why: expect.stringContaining(target) },
    });
  }
  // A read, grep or glob answer is kept on the copy and given again; a bash answer is not kept.
  await answerCall(copy, "bash", { command: "cat docs/guide.md" }, "hashline");
  expect([...copy.answers.keys()].every((key) => key.startsWith("read\0hashline\0"))).toBe(true);
  expect(copy.answers.size).toBe(11);
});

// Catches: an inspection step counted as an act, or an act (a write, a commit, a command run
// through a substitution) taken for a read-only call: the report's inspection floors and the
// replay's checkout rounds both read this one rule.
test("readOnlyCall takes the read-only tools, and a bash call whose every command, joined by pipes, &&, || or ;, is one of the read-only programs with no redirection to a file", () => {
  expect([...READ_ONLY_TOOLS]).toEqual(["read", "grep", "glob", "find", "ls"]);
  for (const tool of READ_ONLY_TOOLS) {
    expect({ tool, readOnly: readOnlyCall(tool, { path: "src" }) }).toEqual({
      tool,
      readOnly: true,
    });
  }
  for (const command of [
    "cd src && cat app.py",
    "cat README.md | head -20",
    "head -n 5 a.txt; tail -n 5 a.txt",
    "ls -la docs",
    "find . -name '*.py' -type f",
    "grep -rn 'TODO' src || grep -rn FIXME src",
    "rg -n 'def main' src",
    "wc -l src/app.py",
    "sed -n '1,20p' src/app.py",
    "nl -ba src/app.py | sed -n 1,5p",
    "stat src/app.py",
    "file docs/logo.png",
    "tree -L 2 src",
    "git log --oneline -5",
    "git show HEAD:src/app.py",
    "git diff --stat",
    "git status --short",
    "git grep -n main",
    "git ls-files | wc -l",
    "git blame src/app.py",
    "git -C src status",
    "git --no-pager log -1",
    "ls missing 2>/dev/null; ls",
    "cat src/app.py 2>&1 | head",
    'grep -n "a > b" src/app.py',
    "cat < src/app.py",
  ]) {
    expect({ command, readOnly: readOnlyCall("bash", { command }) }).toEqual({
      command,
      readOnly: true,
    });
  }
  for (const command of [
    "find . -name '*.pyc' -exec rm {} \\;",
    "find . -type d -execdir ls \\;",
    "find . -name '*.tmp' -delete",
    "find . -fprint found.txt",
    "cat src/app.py > copy.py",
    "grep -n main src/app.py >> notes.txt",
    "ls &> listing.txt",
    "FOO=bar cat src/app.py",
    "LIMIT=5",
    "cat $(ls src)",
    "cat `ls src`",
    'grep "$(whoami)" src/app.py',
    "git push",
    "git commit -m wip",
    "git -c core.pager=less log",
    "rm -rf build",
    "sed -i 's/a/b/' src/app.py",
    "sed 's/a/b/' src/app.py",
    "echo done",
    "python3 -c 'print(1)'",
    "cat src/app.py & cat README.md",
    "(cd src && ls)",
    "cat <(ls)",
    "/bin/cat src/app.py",
    "",
  ]) {
    expect({ command, readOnly: readOnlyCall("bash", { command }) }).toEqual({
      command,
      readOnly: false,
    });
  }
  // Any other tool, and a bash call with no command, is no read-only call.
  for (const [name, input] of [
    ["write", { path: "a.py", content: "" }],
    ["edit", { path: "a.py" }],
    ["todo", { op: "view" }],
    ["task", { prompt: "look" }],
    ["bash", {}],
    ["bash", { command: 3 }],
  ] as const) {
    expect({ name, readOnly: readOnlyCall(name, input) }).toEqual({ name, readOnly: false });
  }
});
