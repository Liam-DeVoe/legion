import { expect, test } from "bun:test";
import { existsSync, lstatSync, mkdirSync, symlinkSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
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
