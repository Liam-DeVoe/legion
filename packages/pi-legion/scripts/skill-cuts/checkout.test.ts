import { expect, test } from "bun:test";
import { existsSync, lstatSync, mkdirSync, symlinkSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { snapshotTag } from "./arms";
import {
  answerCall,
  type CheckoutCopy,
  colocateJj,
  READ_ONLY_TOOLS,
  readOnlyCall,
  removeOutsideLinks,
} from "./checkout";

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

/** The guide of the made-up checkout. */
const GUIDE = "# Guide\n\nRun the app.\n";

/** A git checkout of a made-up repository under `name`, as cli.ts's shared checkout is: docs/
 *  guide.md and src/app.py in one commit, checked out with a detached HEAD. */
function gitCopy(name: string): string {
  const dir = path.join(scratch, name);
  mkdirSync(path.join(dir, "docs"), { recursive: true });
  mkdirSync(path.join(dir, "src"), { recursive: true });
  writeFileSync(path.join(dir, "docs", "guide.md"), GUIDE);
  writeFileSync(path.join(dir, "src", "app.py"), "def main():\n    return 0\n");
  const git = (...args: string[]) => {
    const ran = Bun.spawnSync(["git", ...args], {
      cwd: dir,
      env: {
        PATH: process.env.PATH ?? "/usr/bin:/bin",
        HOME: dir,
        GIT_CONFIG_NOSYSTEM: "1",
        GIT_AUTHOR_NAME: "Acme",
        GIT_AUTHOR_EMAIL: "dev@acme.test",
        GIT_COMMITTER_NAME: "Acme",
        GIT_COMMITTER_EMAIL: "dev@acme.test",
      },
    });
    expect({ args, code: ran.exitCode, err: ran.stderr.toString() }).toMatchObject({ code: 0 });
    return ran.stdout.toString().trim();
  };
  git("init", "--quiet");
  git("add", ".");
  git("commit", "--quiet", "-m", "Add the guide");
  git("checkout", "--quiet", "--detach", git("rev-parse", "HEAD"));
  return dir;
}

/** The text of a `bash` answer of `copy` to `command` (run from `cwd`, when given), its wall time
 *  left out; the answer when it is none. */
async function bashText(
  copy: CheckoutCopy,
  command: string,
  cwd?: string
): Promise<string | { why: string }> {
  const answer = await answerCall(
    copy,
    "bash",
    { command, ...(cwd === undefined ? {} : { cwd }) },
    "hashline"
  );
  return "why" in answer ? answer : answer.text.replace(/\n\nWall time: \d+\.\d\d seconds/, "");
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
// then judged on that read; thermonuclear's runs on a range past a file's end were dropped as
// reads-past-last-round), a range answered past the lines the pin shows, or a read with another
// selector answered in a form the runner does not rebuild.
test("a read of a code file of 100 lines or more is answered unsummarized and marked so, one past 300 lines with its first 300 and the count of the rest; a ranged read with the lines of its range, a line before and three after, and the pin's count of the lines left; a range past the file's end with the pin's notice; any other selector is not answered", async () => {
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
    // A range that starts past the file's end: the pin's notice alone, no hashline line.
    [
      "src/big.py:200-210",
      "Line 200 is beyond end of file (120 lines total). Use :1 to read from the start, or :120 to read the last line.",
    ],
  ] as const) {
    expect({ target, answer: await read(target) }).toEqual({
      target,
      answer: { text, isError: false },
    });
  }
  for (const target of ["src/big.py:raw", "src/big.py:1-2,5-6", "src/big.py:-5"]) {
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
    'grep "$(whoami)" src/app.py',
    "git push",
    "git commit -m wip",
    "git -c core.pager=less log",
    "rm -rf build",
    "sed -i 's/a/b/' src/app.py",
    "sed 's/a/b/' src/app.py",
    "python3 -c 'print(1)'",
    "cat src/app.py & cat README.md",
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

/** The dev box's start of a session's bash call, with made-up variables of its own. */
const UNSET = "unset GIT_CONFIG_GLOBAL GIT_CONFIG_NOSYSTEM ACME_TOOLS_PIN ACME_TOOLS_IN";

// Catches: a written case's look at the work judged as its act because the read-only rule missed a
// form the dev box's sessions use (the defect: every judged step of test-driven-development's
// written cases was a bash call that starts with `unset`, prints `pwd`, or asks jj or git where it
// is), or an act (a branch made, a remote added, a jj change, a program the environment chooses, an
// option smuggled in a variable, a file written from a subshell) taken for a read-only call.
test("readOnlyCall takes pwd, true, echo, printf, env with no command, unset, export and assignments, git rev-parse, worktree list, remote and branch with no name, jj's read commands, and a subshell, group or substitution of read-only commands; it refuses each form that acts", () => {
  for (const command of [
    "pwd",
    "true",
    "echo done",
    "printf '%s\\n' a b",
    "printf -v line '%s' a",
    "env",
    "env -0",
    "env -i -u ACME_TOOLS_PIN",
    UNSET,
    "unset -v ACME_TOOLS_PIN",
    "export LC_ALL=C",
    "export ACME_MODE",
    "LIMIT=5",
    "FOO=bar cat src/app.py",
    // biome-ignore lint/suspicious/noTemplateCurlyInString: a shell command's own `${dir}`
    'dir=src && ls $dir && cat "${dir}/app.py"',
    "cat $HOME/.profile",
    "echo $PATH $PWD",
    "git rev-parse --show-toplevel",
    "git rev-parse --abbrev-ref HEAD",
    "git worktree list",
    "git worktree list --porcelain",
    "git remote",
    "git remote -v",
    "git remote show origin",
    "git remote get-url origin",
    "git branch",
    "git branch -a",
    "git branch -r",
    "git branch -v",
    "git branch -vv",
    "git branch --list",
    "git branch --show-current",
    "jj status",
    "jj st",
    "jj log -r @ --no-graph",
    "jj show",
    "jj diff --git",
    "jj root",
    "jj file show src/app.py",
    "jj bookmark list",
    "jj workspace list",
    "jj --no-pager -R . st",
    "(cd src && ls)",
    "(jj st || git status) | cat",
    "{ ls; pwd; } 2>/dev/null",
    'cd "$(git rev-parse --show-toplevel)" && ls',
    "cat $(ls src)",
    "cat `ls src`",
    "(jj root 2>/dev/null; git rev-parse --show-toplevel 2>/dev/null; true)",
    `cd ${ROOT} && ${UNSET} && pwd && (jj st || git status) && git worktree list`,
    `${UNSET}; cd ${ROOT} && pwd && ls -a && (jj workspace list 2>/dev/null; git worktree list 2>/dev/null; jj st 2>/dev/null | head -50)`,
    `cd ${ROOT} && ${UNSET} && pwd && ls -a && (jj root 2>/dev/null; git rev-parse --show-toplevel 2>/dev/null; git status --short 2>/dev/null)`,
  ]) {
    expect({ command, readOnly: readOnlyCall("bash", { command }) }).toEqual({
      command,
      readOnly: true,
    });
  }
  for (const command of [
    "git branch new-name",
    "git branch -d old",
    "git branch -m old new",
    "git branch --set-upstream-to=origin/main",
    "git remote add x y",
    "git remote set-url origin https://example.test/x.git",
    "git remote remove origin",
    "git worktree add ../x",
    "git worktree remove ../x",
    "jj new",
    "jj describe -m wip",
    "jj git fetch",
    "jj bookmark set main",
    "jj workspace add ../x",
    "jj diff --tool rm",
    "jj --config ui.pager=less log",
    "export X=$(rm -rf /)",
    "echo $(git push)",
    "(cat src/app.py > copy.py)",
    "(ls; cat src/app.py >> notes.txt) && pwd",
    "{ ls; } > listing.txt",
    "cd $(mktemp -d)",
    "env -u GIT_DIR git status",
    "env rm -rf build",
    "env -S 'rm -rf build'",
    "unset PATH",
    "PATH=. ls",
    "export PATH=.",
    "HOME=. git log",
    "GIT_EXTERNAL_DIFF=rm git diff",
    "export GIT_DIR=../other",
    "JJ_CONFIG=jj.toml jj diff",
    "RIPGREP_CONFIG_PATH=rg.conf rg main",
    "LD_PRELOAD=./x.so ls",
    "X=--pre=sh && rg $X main",
    "X='1e rm -rf .' && sed -n \"$X\" src/app.py",
    "cat $FOO",
    "echo $?",
    "echo $((1 + 2))",
    // biome-ignore lint/suspicious/noTemplateCurlyInString: a shell command's own `${X:-x}`
    "echo ${X:-x}",
    "printf -v 'a[$(id)]' x",
    "unset 'a[$(id)]'",
    "a[$(id)]=1",
    "( )",
    "{ ls }",
  ]) {
    expect({ command, readOnly: readOnlyCall("bash", { command }) }).toEqual({
      command,
      readOnly: false,
    });
  }
});

// Catches: an answer that names the copy's own path (a scratch path in a body moved pass rates
// before), `pwd` and git's and jj's answers of where the session is given at the runner's paths,
// a jj command of a session that ran jj answered with an error that session never saw, or a word
// whose expansion may lead outside the copy answered.
test("a copy made a colocated jj repository answers jj st and jj root, and pwd, git rev-parse --show-toplevel, git worktree list and errors, at the session's paths; no answer holds the copy's path; an expansion the runner cannot place inside the checkout is not answered", async () => {
  const dir = gitCopy("colocated");
  expect(await colocateJj(dir)).toBe(true);
  const copy = { dir, root: ROOT, cwd: ROOT, git: true, jj: true as const };
  const given: unknown[] = [];
  const text = async (command: string, cwd?: string) => {
    const answer = await bashText(copy, command, cwd);
    given.push(answer);
    return answer;
  };
  expect(await text("pwd")).toBe(`${ROOT}\n`);
  expect(await text("pwd", "docs")).toBe(`${ROOT}/docs\n`);
  expect(await text("cd docs && pwd && echo $PWD")).toBe(`${ROOT}/docs\n${ROOT}/docs\n`);
  expect(await text("git rev-parse --show-toplevel", "docs")).toBe(`${ROOT}\n`);
  expect(await text("git worktree list")).toMatch(
    new RegExp(`^${ROOT} +[0-9a-f]{7,} \\(detached HEAD\\)\\n$`)
  );
  expect(await text("jj root", "src")).toBe(`${ROOT}\n`);
  const status = await text("jj st");
  expect(status).toEqual(expect.stringContaining("The working copy has no changes."));
  expect(status).toEqual(expect.stringContaining("Add the guide"));
  expect(await text("jj workspace list")).toEqual(expect.stringMatching(/^default: /));
  expect(await text(`cd "$(git rev-parse --show-toplevel)" && pwd`, "docs")).toBe(`${ROOT}\n`);
  // biome-ignore lint/suspicious/noTemplateCurlyInString: a shell command's own `${dir}`
  expect(await text("dir=docs && cat $dir/guide.md && cat ${dir}/guide.md")).toBe(GUIDE + GUIDE);
  expect(await text("git branch --show-current")).toBe("(no output, exit code 0)");
  const shape = await text(
    `cd ${ROOT} && ${UNSET} && pwd && (jj st || git status) && git worktree list`
  );
  expect(shape).toEqual(expect.stringMatching(new RegExp(`^${ROOT}\\nThe working copy has no`)));
  expect(shape).toEqual(expect.stringMatching(new RegExp(`\\n${ROOT} +[0-9a-f]{7,} \\(detached`)));
  expect(await text(`cd ${ROOT}/nowhere`)).toBe(
    `bash: line 1: cd: ${ROOT}/nowhere: No such file or directory\n\n\nCommand exited with code 1`
  );
  expect(await text(`cat ${ROOT}/src/missing.py`)).toBe(
    `cat: ${ROOT}/src/missing.py: No such file or directory\n\n\nCommand exited with code 1`
  );
  expect(await text("env")).toEqual(expect.stringContaining("PATH="));
  given.push(
    await answerCall(copy, "read", { path: `${ROOT}/docs/guide.md` }, "hashline"),
    await answerCall(copy, "grep", { pattern: "Guide", path: ROOT }, "hashline"),
    await answerCall(copy, "glob", { path: `${ROOT}/**/*.md` }, "hashline"),
    await answerCall(copy, "read", { path: "docs/missing.md" }, "hashline")
  );
  expect(given.every((each) => typeof each === "string" || !("why" in Object(each)))).toBe(true);
  expect(JSON.stringify(given)).not.toContain(dir);
  expect(JSON.stringify(given)).not.toContain(scratch);

  // A word whose expansion the runner cannot place inside the checkout is not answered.
  for (const [command, why] of [
    ["cat $HOME/.profile", "$HOME/.profile expands"],
    ["echo $HOME", "$HOME"],
    ["dir=/etc && cat $dir/hostname", "is outside the session's checkout"],
    ['cd "$(git rev-parse --git-dir)" && ls', "expands"],
    ["ls missing || dir=docs; cat $dir/guide.md", "$dir/guide.md expands"],
  ] as const) {
    expect({ command, answer: await bashText(copy, command) }).toEqual({
      command,
      answer: { why: expect.stringContaining(why) },
    });
  }
}, 60_000);

// Catches: a jj command of a session that ran no jj answered as in a jj workspace, or one answered
// where the copy could not be made a jj repository (its answer then an error the session never
// saw) or holds no history.
test("a copy no session made a jj repository answers jj as jj does outside one; where its colocation failed, or the copy is GitHub's tarball, a jj command is not answered", async () => {
  const dir = gitCopy("plain");
  const plain = { dir, root: ROOT, cwd: ROOT, git: true };
  const answer = await answerCall(plain, "bash", { command: "jj st" }, "hashline");
  expect(answer).toMatchObject({ isError: true });
  expect("text" in answer ? answer.text : answer.why).toStartWith(
    'Error: There is no jj repo in "."'
  );
  const failed = { ...plain, jj: { why: "jj git init --colocate failed: no room" } };
  expect(await bashText(failed, "(jj root || git rev-parse --show-toplevel)")).toEqual({
    why: expect.stringContaining("jj git init --colocate failed: no room"),
  });
  expect(await bashText(failed, "git rev-parse --show-toplevel")).toBe(`${ROOT}\n`);
  expect(await bashText({ ...plain, git: false }, "jj root")).toEqual({
    why: expect.stringContaining("GitHub's tarball"),
  });
}, 60_000);

// Catches: a glob of a directory or of a file with no glob character, or of a `;` list of paths,
// left unanswered (the defect: thermonuclear's written cases dropped as reads-past-last-round on
// such calls, which the pin answers), answered in a form the pin does not give, a missing path
// answered as no match where the pin says it is not found, or an answer that shows jj's own files
// of a colocated copy, which the pin's walk leaves out.
test("a glob of a directory with no glob character lists it, one of a file answers the file, and a ;-separated list answers each of its paths, a missing one noted; a missing path is not answered; jj's .jj directory is in no answer", async () => {
  const dir = path.join(scratch, "globs");
  for (const [file, text] of [
    ["docs/guide.md", GUIDE],
    ["docs/api/ref.md", "# Ref\n"],
    ["src/app.py", "def main():\n    return 0\n"],
    [".jj/repo/store.md", "# Store\n"],
  ] as const) {
    mkdirSync(path.dirname(path.join(dir, file)), { recursive: true });
    writeFileSync(path.join(dir, file), text);
  }
  const copy = { dir, root: ROOT, cwd: ROOT, git: false };
  const glob = (target: string) => answerCall(copy, "glob", { path: target }, "hashline");
  for (const [target, text] of [
    ["docs", "# docs/\nguide.md\n## api/\nref.md"],
    [`${ROOT}/docs/api`, "# docs/api/\nref.md"],
    ["src/app.py", "# src/\napp.py"],
    [`${ROOT}/src/app.py`, "# src/\napp.py"],
    ["docs/*.md;src/*.py", "# docs/\nguide.md\n# src/\napp.py"],
    ["docs/*.md; missing/*.py", "# docs/\nguide.md\n\nSkipped missing paths: missing/*.py"],
    ["**/*.md", "# docs/\nguide.md\n## api/\nref.md"],
  ] as const) {
    expect({ target, answer: await glob(target) }).toEqual({
      target,
      answer: { text, isError: false },
    });
  }
  for (const [target, why] of [
    ["missing", "not found"],
    ["missing/*.md", "not found"],
    ["a/*.md;b/*.md", "not found"],
    ["src/app.py,docs/guide.md", "is a list"],
    ["docs/*.md;/etc/*.conf", "outside the session's checkout"],
  ] as const) {
    expect({ target, answer: await glob(target) }).toEqual({
      target,
      answer: { why: expect.stringContaining(why) },
    });
  }
  expect(await answerCall(copy, "grep", { pattern: "Store", path: "." }, "hashline")).toEqual({
    text: "No matches found",
    isError: false,
  });
}, 60_000);
