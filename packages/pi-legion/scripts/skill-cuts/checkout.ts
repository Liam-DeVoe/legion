/**
 * Read-only calls, and their answers from a copy of a written case's checkout.
 *
 * **The read-only rule** (`readOnlyCall`, the one definition the replay's checkout rounds and the
 * behaviour report's inspection floors read): a call of one of READ_ONLY_TOOLS (Oh My Pi's `read`,
 * `grep`, `glob`, `find`, `ls`), or a `bash` call whose command is a list of simple commands joined
 * only by pipes, `&&`, `||`, `;` or line ends, each one of READ_ONLY_PROGRAMS: `cd`, `cat`, `head`,
 * `tail`, `ls`, `find` with none of FIND_ACTIONS (`-exec`, `-execdir`, `-ok`, `-okdir`, `-delete`,
 * `-fprint`, `-fprint0`, `-fprintf`, `-fls`), `grep`, `rg` without `--pre`, `wc`, `sed` with `-n`
 * and with no `-i` and no `w`, `W` or `e` command, `nl`, `stat`, `file` without `-C`, `tree`
 * without `-o`, or `git` (with at most the global options `-C <dir>`, `--no-pager` and `-P`) whose
 * subcommand is one of READ_ONLY_GIT (`log`, `show`, `diff`, `status`, `grep`, `ls-files`, `blame`)
 * with no `--output`, `-O`, `--open-files-in-pager` or `--ext-diff`. A redirection to a file makes
 * the call not read-only (a redirection to `/dev/null`, a copy of one stream into another such as
 * `2>&1`, and a read of a file with `<` are none). So do a command substitution (`$(…)`), a
 * backtick, a process substitution, a variable assignment, a subshell or group, a command run in
 * the background (`&`), a heredoc, a comment, a program named by its path, and any other command.
 *
 * **The answers** (`answerCall`): a written case's run (replay.ts's checkout rounds) answers a
 * read-only call from a copy of its context session's checkout (`CheckoutCopy`: the copy's
 * directory, and the session's checkout root and working directory it stands for), as the pinned
 * Oh My Pi (the tag v18.18.6-sami.20261119-162325 of cybermonklabs/oh-my-pi) gives it at its
 * default settings, every path of the session's (the copy's own never shows), or says why it does
 * not (`Unanswered`). A call the copy cannot answer so is not answered: the reply is then the
 * judged step.
 *
 * **Confinement.** The calls are the model's, run on this machine, so none may read a file outside
 * the copy. cli.ts removes from each shared checkout, as it makes it, every symbolic link that resolves
 * outside it or not at all (`removeOutsideLinks`), so `cat` of a link, `grep -R`, `rg --follow`,
 * `find -L` and a `read` follow no link out; the runner therefore refuses no option that follows
 * links. Beside that, a `read`, `grep` or `glob` whose target resolves outside the copy is not
 * answered (`leavesCopy`), and a `bash` command is not answered when a word of it may lead outside
 * (`escapes`: a `$` or brace expansion, `~`, `.git`, an absolute path off the root, a relative one
 * that leaves the root from any directory a `cd` before it may have moved to, an option's attached
 * path that does).
 *
 * - `read` (keys `path`, `i`): a whole read by path that resolves (cases.ts's `resolvedReadPath`,
 *   against the session's working directory) to a file under the checkout's root, in the form
 *   arms.ts writes a whole read by path (`pathAnswer`, its rows as the arm's reads by path show
 *   them), or Oh My Pi's not-found result `Path '<path>' not found` for a file the copy lacks. A
 *   file that is no prose is answered so too where the pin shows it whole: a UTF-8 text file of
 *   fewer than 100 lines (newlines and one more) that is no notebook, SVG or document markit
 *   converts; the pin may summarize one of 100 lines or more (read.ts lines 2519-2533,
 *   read-summary.ts's `trySummarize`, `read.summarize.minTotalLines` 100: a code file whose
 *   tree-sitter parse elides a body), which the runner cannot tell, so it is not answered. Not
 *   answered either: a read with a selector (the pin's range form widens a range to its brackets,
 *   read.ts lines 2940-3040, which the runner does not rebuild), of a directory, outside the root,
 *   under `~` or `.git`, and a file the arms refuse for another reason (`pathFormFault`).
 * - `grep` (keys `pattern`, `path`, `case`, `gitignore`, `skip` of 0 or null, `i`): GrepTool's
 *   output (packages/coding-agent/src/tools/grep.ts lines 651-1316): each match line `*<n>:<line>`
 *   and each context line ` <n>:<line>` (`|` in place of `:` in a session that shows no hashline
 *   rows; packages/tui/src/tools/match-line-format.ts), one line before and three after each match
 *   (`grep.contextBefore`, `grep.contextAfter`, settings.ts lines 488-520), `...` between runs that
 *   do not touch; a directory's matches grouped under a prefix-folded tree of `#` headers, each
 *   file's `#<tag>` beside its name in a hashline session (packages/tui/src/tools/
 *   grouped-file-output.ts lines 65-154, packages/utils/src/path-tree.ts), a file's alone under
 *   `[<path>#<tag>]`; `No matches found` when none. Its paths are relative to the working directory
 *   when under it, else absolute (path-utils.ts lines 512-531); its files in path order, each file
 *   with a NUL byte left out; its regular expression as JavaScript's in Unicode mode, case-sensitive
 *   unless `case` is false. Not answered: a `path` list, a glob or a selector in `path`, a path
 *   outside the root or missing, a pattern over lines or with a look-around, a back-reference or a
 *   named group in Rust's own form, one JavaScript does not take, a shown line past the 512 bytes
 *   grep shows, more matches than grep keeps in a file (20 in a directory, 200 in a file) or files
 *   than one page shows (20), and an output past 50 KB.
 * - `glob` (keys `path`, `hidden`, `gitignore`, `limit`, `i`): GlobTool's output
 *   (packages/coding-agent/src/tools/glob.ts lines 284-375): the matching files and directories
 *   (a directory's path ending in `/`) as a prefix-folded tree (`formatGroupedPaths`), or `No
 *   files found matching pattern`; dotfiles left out when `hidden` is false. Oh My Pi orders them by
 *   their time of change, which the copy does not keep, so in path order. Not answered: a `path`
 *   list, a path with no glob character, a base outside the root, and more matches than `limit`
 *   (200 when absent).
 * - `bash` (keys `command`, `cwd`, `timeout`, `i`): the command run in the copy (its working
 *   directory the session's mapped into the copy; the checkout's root in the command mapped to
 *   the copy and back in the output), by `bash -c` with its standard error joined to its output,
 *   an environment of its own (no variable of the runner's), within BASH_TIME_LIMIT_MS; its text
 *   as Oh My Pi's bash tool gives it (bash.ts lines 765-922): the output as printed, `(no output,
 *   exit code 0)` or `(no output)` for none, `Wall time: <s> seconds` after it when the run took a
 *   second or more, and `Command exited with code <n>` after it, an error, when it failed. Oh My
 *   Pi's bash interceptor is off at its default settings (exec/settings.ts lines 170-180), so no
 *   command is blocked. Its git commands run on the copy's own history: the copy is the shared
 *   checkout of the commit (cli.ts's `sharedCheckout`: a fetch of the commit 50 commits deep with
 *   no blobs, checked out with a detached HEAD), so `git log` shows at most 50 commits and then
 *   the shallow boundary, and `git status` names no branch (and shows each link removed as
 *   deleted); git runs with GIT_OPTIONAL_LOCKS=0, so `git status` writes no index into the shared
 *   checkout. Not answered: a git command where the copy is GitHub's tarball instead (the fetch
 *   failed: `CheckoutCopy.git` false), a command a word of which may lead outside the copy
 *   (`escapes`), an
 *   output past 50 KB (`tools.artifactSpillThreshold`, which Oh My Pi elides) or with a line past
 *   768 characters (`tools.outputMaxColumns`), an output of 1,000 characters or more of a command
 *   Oh My Pi's minimizer rewrites (crates/pi-shell/src/minimizer/engine.rs lines 16-125: an
 *   unpiped command of MINIMIZED_PROGRAMS), and a run past the time limit.
 * - `find` and `ls`: no capture declares them, so they are not answered.
 */
import { readdirSync, readFileSync, realpathSync, rmSync, statSync } from "node:fs";
import path from "node:path";
import { type PathRows, pathAnswer, shownPath, snapshotTag } from "./arms";
import { resolvedReadPath } from "./cases";
import { eachChunk, timeLimit } from "./common";

/** The tools whose every call is read-only. */
export const READ_ONLY_TOOLS = ["read", "grep", "glob", "find", "ls"] as const;
/** The programs a read-only `bash` call's commands may run (see the file's header). */
export const READ_ONLY_PROGRAMS = [
  "cd",
  "cat",
  "head",
  "tail",
  "ls",
  "find",
  "grep",
  "rg",
  "wc",
  "sed",
  "nl",
  "stat",
  "file",
  "tree",
  "git",
] as const;
/** The git subcommands a read-only `bash` call may run. */
export const READ_ONLY_GIT = [
  "log",
  "show",
  "diff",
  "status",
  "grep",
  "ls-files",
  "blame",
] as const;
/** The actions of `find` that run a command or write a file. */
const FIND_ACTIONS: Readonly<Record<string, true>> = {
  "-exec": true,
  "-execdir": true,
  "-ok": true,
  "-okdir": true,
  "-delete": true,
  "-fprint": true,
  "-fprint0": true,
  "-fprintf": true,
  "-fls": true,
};

/** One simple command of a parsed `bash` command: its words, unquoted; its redirections; and
 *  whether it reads the output of the command before it through a pipe. */
interface SimpleCommand {
  readonly words: string[];
  readonly redirects: { op: string; target: string }[];
  readonly piped: boolean;
}

/** The simple commands of `command`, joined by pipes, `&&`, `||`, `;` or line ends, or undefined
 *  when it holds any other shell form (see the file's header). */
function simpleCommands(command: string): SimpleCommand[] | undefined {
  const commands: SimpleCommand[] = [];
  let words: string[] = [];
  let redirects: { op: string; target: string }[] = [];
  let piped = false;
  let word = "";
  let started = false;
  let quoted = false;
  let pendingRedirect: string | undefined;
  // How the command list stands: after a separator a command must follow, but for a line end or a
  // `;` at the very end.
  let separatorNeedsCommand = false;
  const endWord = (): boolean => {
    if (!started) return true;
    if (pendingRedirect !== undefined) {
      redirects.push({ op: pendingRedirect, target: word });
      pendingRedirect = undefined;
    } else {
      // A `{`, `}` or `#` word unquoted starts a group or a comment.
      if (!quoted && (word === "{" || word === "}" || word.startsWith("#"))) return false;
      words.push(word);
    }
    word = "";
    started = false;
    quoted = false;
    return true;
  };
  const endCommand = (nextPiped: boolean, needsCommand: boolean): boolean => {
    if (!endWord() || pendingRedirect !== undefined) return false;
    if (words.length === 0) {
      // An empty command is allowed only at the very start of a line list (`\n` or `;` alone).
      if (redirects.length > 0 || separatorNeedsCommand || nextPiped || needsCommand) return false;
      return true;
    }
    commands.push({ words, redirects, piped });
    words = [];
    redirects = [];
    piped = nextPiped;
    separatorNeedsCommand = needsCommand;
    return true;
  };
  for (let i = 0; i < command.length; i++) {
    const ch = command[i] ?? "";
    const next = command[i + 1];
    if (ch === "'") {
      const end = command.indexOf("'", i + 1);
      if (end < 0) return undefined;
      word += command.slice(i + 1, end);
      started = true;
      quoted = true;
      i = end;
      continue;
    }
    if (ch === '"') {
      let j = i + 1;
      for (; j < command.length; j++) {
        const inner = command[j];
        if (inner === '"') break;
        if (inner === "`" || (inner === "$" && command[j + 1] === "(")) return undefined;
        if (inner === "\\" && j + 1 < command.length) {
          const escaped = command[j + 1] ?? "";
          word += '"\\$`\n'.includes(escaped) ? (escaped === "\n" ? "" : escaped) : `\\${escaped}`;
          j++;
          continue;
        }
        word += inner;
      }
      if (j >= command.length) return undefined;
      started = true;
      quoted = true;
      i = j;
      continue;
    }
    if (ch === "\\") {
      if (next === undefined) return undefined;
      if (next !== "\n") {
        word += next;
        started = true;
      }
      i++;
      continue;
    }
    if (ch === "`" || ch === "(" || ch === ")") return undefined;
    if (ch === "$" && next === "(") return undefined;
    if (ch === " " || ch === "\t") {
      if (!endWord()) return undefined;
      continue;
    }
    if (ch === "\n") {
      if (!endCommand(false, false)) return undefined;
      continue;
    }
    if (ch === ";") {
      if (next === ";" || next === "&") return undefined;
      if (!endCommand(false, false)) return undefined;
      continue;
    }
    if (ch === "|") {
      if (next === "|") {
        if (!endCommand(false, true)) return undefined;
        i++;
      } else {
        if (next === "&") return undefined;
        if (!endCommand(true, true)) return undefined;
      }
      continue;
    }
    if (ch === "&") {
      if (next === "&") {
        if (!endCommand(false, true)) return undefined;
        i++;
        continue;
      }
      if (next === ">") {
        // `&>` or `&>>`: both streams to a file.
        if (!endWord()) return undefined;
        const op = command[i + 2] === ">" ? "&>>" : "&>";
        pendingRedirect = op;
        i += op.length - 1;
        continue;
      }
      return undefined;
    }
    if (ch === ">" || ch === "<") {
      // A redirection, its source stream the digits of the word before it, if any.
      const fd = started && !quoted && /^\d+$/.test(word) ? word : "";
      if (fd !== "") {
        word = "";
        started = false;
      } else if (!endWord()) return undefined;
      let op = ch;
      if (next === "(") return undefined;
      if (ch === ">" && (next === ">" || next === "|" || next === "&")) op += next;
      if (ch === "<" && (next === "<" || next === "&" || next === ">")) op += next;
      if (op === "<<") return undefined;
      i += op.length - 1;
      pendingRedirect = `${fd}${op}`;
      continue;
    }
    word += ch;
    started = true;
  }
  if (!endCommand(false, false)) return undefined;
  return commands.length > 0 && !separatorNeedsCommand ? commands : undefined;
}

/** Whether a redirection `op` to `target` writes no file: a copy of one stream into another, a
 *  write to `/dev/null`, or a read of a file. */
function harmlessRedirect({ op, target }: { op: string; target: string }): boolean {
  if (/^\d*[<>]&$/.test(op)) return /^(\d+|-)$/.test(target);
  if (/^\d*<$/.test(op)) return target !== "";
  return target === "/dev/null";
}

/** Whether the git command of `args` (the words after `git`) only reads (see the file's header). */
function gitReadsOnly(args: readonly string[]): boolean {
  let at = 0;
  for (; at < args.length; at++) {
    const arg = args[at] ?? "";
    if (arg === "-C") {
      at++;
      continue;
    }
    if (arg === "--no-pager" || arg === "-P") continue;
    break;
  }
  const subcommand = args[at];
  if (subcommand === undefined || !(READ_ONLY_GIT as readonly string[]).includes(subcommand)) {
    return false;
  }
  return args
    .slice(at + 1)
    .every(
      (arg) =>
        !/^(--output(=|$)|-O|--open-files-in-pager|--ext-diff)/.test(arg) && arg !== "--no-index"
    );
}

/** Whether the simple command `words` (unquoted) only reads (see the file's header). */
function commandReadsOnly(words: readonly string[]): boolean {
  const [program, ...args] = words;
  if (program === undefined) return false;
  if (!(READ_ONLY_PROGRAMS as readonly string[]).includes(program)) return false;
  switch (program) {
    case "find":
      return args.every((arg) => !Object.hasOwn(FIND_ACTIONS, arg));
    case "rg":
      return args.every((arg) => arg !== "--pre" && !arg.startsWith("--pre="));
    case "tree":
      return args.every((arg) => arg !== "-o");
    case "file":
      return args.every((arg) => arg !== "-C" && arg !== "--compile");
    case "git":
      return gitReadsOnly(args);
    case "sed": {
      const flags = args.filter((arg) => arg.startsWith("-"));
      const quiet = flags.some(
        (arg) => arg === "--quiet" || arg === "--silent" || /^-[a-zA-Z]*n[a-zA-Z]*$/.test(arg)
      );
      const inPlace = flags.some((arg) => arg.startsWith("--in-place") || /^-[a-zA-Z]*i/.test(arg));
      const writes = args.some(
        (arg) =>
          !arg.startsWith("-") && (/[wW]\s+\S/.test(arg) || /(^|[^\\a-zA-Z])e(\s|;|}|$)/.test(arg))
      );
      return quiet && !inPlace && !writes;
    }
    default:
      return true;
  }
}

/** The parsed read-only commands of a `bash` call's `command`, or undefined when it is none. */
function readOnlyCommands(command: unknown): SimpleCommand[] | undefined {
  if (typeof command !== "string" || command.trim() === "") return undefined;
  const commands = simpleCommands(command);
  if (commands === undefined) return undefined;
  for (const each of commands) {
    const [first] = each.words;
    if (first === undefined || /^[A-Za-z_][A-Za-z0-9_]*=/.test(first)) return undefined;
    if (!commandReadsOnly(each.words) || !each.redirects.every(harmlessRedirect)) return undefined;
  }
  return commands;
}

/** Whether the tool call `name` with `input` is read-only: the one rule of the replay's checkout
 *  rounds and the report's inspection floors (see the file's header). */
export function readOnlyCall(name: string | undefined, input: unknown): boolean {
  if ((READ_ONLY_TOOLS as readonly (string | undefined)[]).includes(name)) return true;
  if (name !== "bash" || typeof input !== "object" || input === null) return false;
  return readOnlyCommands(Reflect.get(input, "command")) !== undefined;
}

// ---------------------------------------------------------------------------------------------
// Answers from a copy of the checkout.

/** A copy of a written case's checkout: `dir`, the copy on this machine; `root`, the checkout's
 *  directory on the session's machine (cases.ts's `checkout.root`); `cwd`, the session's working
 *  directory, inside `root`; `git`, whether the copy is a git checkout at the commit, whose history
 *  git commands read (else GitHub's tarball of the commit, which holds none). */
export interface CheckoutCopy {
  readonly dir: string;
  readonly root: string;
  readonly cwd: string;
  readonly git: boolean;
}

/** A call's answer: the text the tool gives, and whether it is an error. */
export interface CheckoutAnswer {
  readonly text: string;
  readonly isError: boolean;
}

/** Why a call is not answered. */
export interface Unanswered {
  readonly why: string;
}

/** The longest a `bash` call's command may run in the copy before it is ended, unanswered. */
export const BASH_TIME_LIMIT_MS = 30_000;
/** The bytes and the characters of a line a bash or grep output may hold to be answered whole:
 *  Oh My Pi elides an output past its spill threshold (`tools.artifactSpillThreshold`, 50 KB) and
 *  cuts a line past `tools.outputMaxColumns` (768), grep's lines past DEFAULT_MAX_COLUMN (512). */
const OUTPUT_BYTES = 50 * 1024;
const BASH_COLUMNS = 768;
const GREP_COLUMNS = 512;
/** Oh My Pi's minimizer passes an output shorter than this through as it is (engine.rs line 17). */
const MINIMIZE_CHARS = 1_000;
/** The programs of a read-only command whose output Oh My Pi's minimizer rewrites, unpiped
 *  (crates/pi-shell/src/minimizer/filters/mod.rs lines 42-60). */
const MINIMIZED_PROGRAMS: Readonly<Record<string, true>> = {
  ls: true,
  tree: true,
  find: true,
  grep: true,
  rg: true,
  wc: true,
  cat: true,
  stat: true,
  git: true,
};
/** GrepTool's caps at the pin (grep.ts lines 83-96): files one page shows, matches kept in a file
 *  of several and of one, and its context lines. */
const GREP_FILE_LIMIT = 20;
const GREP_MULTI_FILE_MATCHES = 20;
const GREP_SINGLE_FILE_MATCHES = 200;
const GREP_CONTEXT_BEFORE = 1;
const GREP_CONTEXT_AFTER = 3;
/** GlobTool's limit when the call gives none (glob.ts line 46). */
const GLOB_LIMIT = 200;

/** Why `input` names an option the copy does not answer, or undefined. */
function otherKeys(input: Record<string, unknown>, keys: readonly string[]): string | undefined {
  const other = Object.keys(input).filter((key) => key !== "i" && !keys.includes(key));
  return other.length === 0
    ? undefined
    : `its options ${other.join(", ")} are none the copy answers`;
}

/** The path inside the checkout of the session's absolute path `full`, "" for the root itself,
 *  or undefined when it is outside the root. */
function insideRoot(copy: CheckoutCopy, full: string): string | undefined {
  if (full === copy.root) return "";
  return full.startsWith(`${copy.root}/`) ? full.slice(copy.root.length + 1) : undefined;
}

/** The path Oh My Pi's search tools show for the session's absolute path `full`: relative to the
 *  working directory when under it (`.` for it), else absolute (path-utils.ts lines 512-531). */
function relativeToCwd(copy: CheckoutCopy, full: string): string {
  if (full === copy.cwd) return ".";
  return full.startsWith(`${copy.cwd}/`) ? full.slice(copy.cwd.length + 1) : full;
}

/** The session's absolute path that the search target `target` names, inside the root, and its
 *  path in the checkout; or why not. */
function searchTarget(
  copy: CheckoutCopy,
  target: string
): { full: string; inside: string } | Unanswered {
  if (/[;,]/.test(target)) return { why: `its path ${target} is a list` };
  if (target.startsWith("!") || /[*?[\]{}]/.test(target)) {
    return { why: `its path ${target} holds a glob` };
  }
  if (target.slice(target.lastIndexOf("/") + 1).includes(":")) {
    return { why: `its path ${target} holds a selector` };
  }
  const full = resolvedReadPath(target, { cwd: copy.cwd });
  const inside = full === undefined ? undefined : insideRoot(copy, full);
  if (full === undefined || inside === undefined) {
    return { why: `its path ${target} is outside the session's checkout ${copy.root}` };
  }
  if (inside.split("/").includes(".git")) {
    return { why: `its path ${target} is under .git, which GitHub's tarball does not hold` };
  }
  return { full, inside };
}

/** The `read` call's answer (see the file's header). */
function readAnswer(
  copy: CheckoutCopy,
  input: Record<string, unknown>,
  rows: PathRows
): CheckoutAnswer | Unanswered {
  const other = otherKeys(input, ["path"]);
  if (other !== undefined) return { why: other };
  const target = input.path;
  if (typeof target !== "string") return { why: "it names no path" };
  if (target.endsWith("/") || target.slice(target.lastIndexOf("/") + 1).includes(":")) {
    return {
      why: `${target} is a read of a directory, or with a selector, whose range form the runner does not rebuild`,
    };
  }
  const full = resolvedReadPath(target, { cwd: copy.cwd });
  const inside = full === undefined ? undefined : insideRoot(copy, full);
  if (full === undefined || inside === undefined || inside === "") {
    return { why: `${target} is no file of the session's checkout ${copy.root}` };
  }
  if (inside.split("/").includes(".git")) {
    return { why: `${target} is under .git, which GitHub's tarball does not hold` };
  }
  if (leavesCopy(copy, inside)) {
    return { why: `${target} resolves, through a symbolic link, outside the checkout` };
  }
  if (statSync(path.join(copy.dir, inside), { throwIfNoEntry: false })?.isDirectory()) {
    return { why: `${target} is a directory, whose listing the runner does not rebuild` };
  }
  const answer = pathAnswer(copy.dir, {
    file: inside,
    target,
    source: full,
    shown: shownPath(target, full, { cwd: copy.cwd }),
    rows,
    text: true,
  });
  if (typeof answer === "string") return { why: answer };
  return { text: answer.text, isError: answer.isError };
}

/** Whether the file or directory `inside` of the copy, or the nearest of its parents that is
 *  there, resolves (its symbolic links followed) outside the copy's own directory. */
function leavesCopy(copy: CheckoutCopy, inside: string): boolean {
  const real = realpathSync(copy.dir);
  for (let at = path.join(copy.dir, inside); ; at = path.dirname(at)) {
    let resolved: string;
    try {
      resolved = realpathSync(at);
    } catch {
      if (at === copy.dir || at === path.dirname(at)) return false;
      continue;
    }
    return resolved !== real && !resolved.startsWith(`${real}/`);
  }
}

/** Removes from `dir` (a copy of a checkout, cli.ts) every symbolic link whose target, resolved,
 *  is outside `dir`'s own real directory, or does not resolve (a dangling link, a loop), so that
 *  no command run in the copy (`cat`, `grep -R`, `rg --follow`, `find -L`, a `read`) reads a file
 *  outside it through one; a link that resolves inside the copy stays. Returns the path inside
 *  `dir` of each link removed, in path order. */
export function removeOutsideLinks(dir: string): string[] {
  const real = realpathSync(dir);
  const removed: string[] = [];
  const walk = (at: string) => {
    for (const entry of readdirSync(path.join(dir, at), { withFileTypes: true })) {
      const child = at === "" ? entry.name : `${at}/${entry.name}`;
      if (entry.isDirectory()) {
        walk(child);
        continue;
      }
      if (!entry.isSymbolicLink()) continue;
      let target: string | undefined;
      try {
        target = realpathSync(path.join(dir, child));
      } catch {
        target = undefined;
      }
      if (target !== undefined && (target === real || target.startsWith(`${real}/`))) continue;
      rmSync(path.join(dir, child), { force: true });
      removed.push(child);
    }
  };
  walk("");
  return removed.sort();
}

/** Every file and directory under `inside` (a directory of the checkout), each with its path
 *  inside the checkout and whether it is a directory, in path order; symbolic links and `.git`
 *  left out. */
function entriesUnder(copy: CheckoutCopy, inside: string): { path: string; dir: boolean }[] {
  const found: { path: string; dir: boolean }[] = [];
  const walk = (at: string) => {
    for (const entry of readdirSync(path.join(copy.dir, at), { withFileTypes: true })) {
      if (entry.name === ".git") continue;
      const child = at === "" ? entry.name : `${at}/${entry.name}`;
      if (entry.isDirectory()) {
        found.push({ path: child, dir: true });
        walk(child);
      } else if (entry.isFile()) found.push({ path: child, dir: false });
    }
  };
  walk(inside);
  return found.sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
}

/** A prefix-folded tree of `paths` (pi-utils' `buildPathTree` and `walkPathTree`): each event a
 *  directory (its folded chain) or a file, with its depth. A path ending in `/` is a directory. */
function pathTreeEvents(
  paths: readonly string[]
): { kind: "dir" | "file"; depth: number; name: string; key: string }[] {
  interface Node {
    files: { name: string; key: string }[];
    fileNames: Set<string>;
    subdirs: { name: string; node: Node }[];
    dirIndex: Map<string, Node>;
  }
  const node = (): Node => ({ files: [], fileNames: new Set(), subdirs: [], dirIndex: new Map() });
  const root = node();
  for (const each of paths) {
    const isDir = each.endsWith("/");
    const trimmed = isDir ? each.slice(0, -1) : each;
    if (trimmed === "") continue;
    const segments = trimmed.split("/");
    const dirs = isDir ? segments.length : segments.length - 1;
    let at = root;
    for (let i = 0; i < dirs; i++) {
      const name = segments[i] ?? "";
      let child = at.dirIndex.get(name);
      if (child === undefined) {
        child = node();
        at.dirIndex.set(name, child);
        at.subdirs.push({ name, node: child });
      }
      at = child;
    }
    const name = segments.at(-1) ?? "";
    if (!isDir && !at.fileNames.has(name)) {
      at.fileNames.add(name);
      at.files.push({ name, key: each });
    }
  }
  const events: { kind: "dir" | "file"; depth: number; name: string; key: string }[] = [];
  const walk = (at: Node, depth: number) => {
    for (const file of at.files)
      events.push({ kind: "file", depth, name: file.name, key: file.key });
    for (const subdir of at.subdirs) {
      let dirNode = subdir.node;
      const parts = [subdir.name];
      while (dirNode.files.length === 0 && dirNode.subdirs.length === 1) {
        const only = dirNode.subdirs[0] as { name: string; node: Node };
        parts.push(only.name);
        dirNode = only.node;
      }
      events.push({ kind: "dir", depth, name: parts.join("/"), key: "" });
      walk(dirNode, depth + 1);
    }
  };
  walk(root, 0);
  return events;
}

/** Oh My Pi's snapshot tag of a file's text as a read or a grep shows it (arms.ts's
 *  `snapshotTag`, of the text less a leading byte order mark, each CRLF or CR as LF). */
function fileTag(text: string): string {
  return snapshotTag((text.startsWith("\uFEFF") ? text.slice(1) : text).replace(/\r\n?/g, "\n"));
}

/** The `grep` call's answer (see the file's header). */
function grepAnswer(
  copy: CheckoutCopy,
  input: Record<string, unknown>,
  rows: PathRows
): CheckoutAnswer | Unanswered {
  const other = otherKeys(input, ["pattern", "path", "case", "gitignore", "skip"]);
  if (other !== undefined) return { why: other };
  const { pattern, case: caseSensitive, skip } = input;
  if (typeof pattern !== "string" || pattern.trim() === "") return { why: "it names no pattern" };
  if (skip !== undefined && skip !== null && skip !== 0) return { why: "it skips files" };
  if (pattern.includes("\n") || pattern.includes("\\n")) return { why: "its pattern spans lines" };
  if (/\(\?(<?[=!]|P<)|\\[1-9]/.test(pattern)) {
    return { why: "its pattern holds a look-around, a back-reference or a Rust named group" };
  }
  let regex: RegExp;
  try {
    regex = new RegExp(pattern, caseSensitive === false ? "iu" : "u");
  } catch (error) {
    return { why: `its pattern is none JavaScript takes: ${String(error)}` };
  }
  const where = searchTarget(copy, typeof input.path === "string" ? input.path : ".");
  if ("why" in where) return where;
  if (leavesCopy(copy, where.inside)) {
    return {
      why: `its path ${String(input.path)} resolves, through a symbolic link, outside the checkout`,
    };
  }
  const stats = statSync(path.join(copy.dir, where.inside), { throwIfNoEntry: false });
  if (stats === undefined) return { why: `its path ${String(input.path)} is not in the checkout` };
  const isDirectory = stats.isDirectory();
  const files = isDirectory
    ? entriesUnder(copy, where.inside).flatMap((entry) => (entry.dir ? [] : [entry.path]))
    : [where.inside];
  const cap = isDirectory ? GREP_MULTI_FILE_MATCHES : GREP_SINGLE_FILE_MATCHES;
  const hashline = rows === "hashline";
  const sections: { shown: string; tag: string; lines: string[] }[] = [];
  for (const file of files) {
    const bytes = readFileSync(path.join(copy.dir, file));
    if (bytes.includes(0)) {
      if (!isDirectory) return { why: `${file} is no text file` };
      continue;
    }
    const text = bytes.toString("utf8");
    const lines = text.split("\n");
    if (lines.length > 1 && lines.at(-1) === "") lines.pop();
    const matches = lines.flatMap((line, index) => (regex.test(line) ? [index + 1] : []));
    if (matches.length === 0) continue;
    if (matches.length > cap)
      return { why: `${file} holds more than the ${cap} matches grep keeps` };
    const shownLines = new Set<number>();
    for (const at of matches) {
      for (let n = at - GREP_CONTEXT_BEFORE; n <= at + GREP_CONTEXT_AFTER; n++) {
        if (n >= 1 && n <= lines.length) shownLines.add(n);
      }
    }
    const isMatch = new Set(matches);
    const out: string[] = [];
    let last: number | undefined;
    for (const n of [...shownLines].sort((a, b) => a - b)) {
      const line = lines[n - 1] ?? "";
      if (Buffer.byteLength(line) > GREP_COLUMNS) {
        return { why: `line ${n} of ${file} passes the ${GREP_COLUMNS} bytes grep shows` };
      }
      if (last !== undefined && n > last + 1) out.push("...");
      out.push(`${isMatch.has(n) ? "*" : " "}${n}${hashline ? ":" : "|"}${line}`);
      last = n;
    }
    const full = file === "" ? copy.root : `${copy.root}/${file}`;
    sections.push({ shown: relativeToCwd(copy, full), tag: fileTag(text), lines: out });
  }
  if (sections.length === 0) return { text: "No matches found", isError: false };
  if (isDirectory && sections.length > GREP_FILE_LIMIT) {
    return { why: `matches in more than the ${GREP_FILE_LIMIT} files one page of grep shows` };
  }
  const model: string[] = [];
  if (isDirectory) {
    const byShown = new Map(sections.map((section) => [section.shown, section]));
    let emitted = false;
    for (const event of pathTreeEvents(sections.map((section) => section.shown))) {
      const hashes = "#".repeat(event.depth + 1);
      if (emitted && (event.depth === 0 || event.kind === "dir")) model.push("");
      emitted = true;
      if (event.kind === "dir") {
        model.push(`${hashes} ${event.name}/`);
        continue;
      }
      const section = byShown.get(event.key);
      if (section === undefined) continue;
      model.push(`${hashes} ${event.name}${hashline ? `#${section.tag}` : ""}`, ...section.lines);
    }
  } else {
    const [section] = sections;
    if (section !== undefined) {
      if (hashline) model.push(`[${section.shown}#${section.tag}]`);
      model.push(...section.lines);
    }
  }
  const text = model.join("\n");
  if (Buffer.byteLength(text) > OUTPUT_BYTES) {
    return { why: `its output passes the ${OUTPUT_BYTES} bytes grep shows` };
  }
  return { text, isError: false };
}

/** The `glob` call's answer (see the file's header). */
function globAnswer(
  copy: CheckoutCopy,
  input: Record<string, unknown>
): CheckoutAnswer | Unanswered {
  const other = otherKeys(input, ["path", "hidden", "gitignore", "limit"]);
  if (other !== undefined) return { why: other };
  const pattern = input.path;
  if (typeof pattern !== "string" || pattern === "") return { why: "it names no pattern" };
  if (pattern.includes(";")) return { why: `its path ${pattern} is a list` };
  const parts = pattern.split("/");
  const firstGlob = parts.findIndex((part) => /[*?[{]/.test(part));
  if (firstGlob < 0) return { why: `its path ${pattern} holds no glob` };
  const base = parts.slice(0, firstGlob).join("/");
  const rest = parts.slice(firstGlob).join("/");
  const where = searchTarget(copy, base === "" ? (pattern.startsWith("/") ? "/" : ".") : base);
  if ("why" in where) return where;
  if (leavesCopy(copy, where.inside)) {
    return { why: `its base ${base} resolves, through a symbolic link, outside the checkout` };
  }
  if (!statSync(path.join(copy.dir, where.inside), { throwIfNoEntry: false })?.isDirectory()) {
    return { text: "No files found matching pattern", isError: false };
  }
  const glob = new Bun.Glob(rest);
  const hidden = input.hidden !== false;
  const limit = typeof input.limit === "number" ? input.limit : GLOB_LIMIT;
  const found: string[] = [];
  for (const entry of entriesUnder(copy, where.inside)) {
    const below = where.inside === "" ? entry.path : entry.path.slice(where.inside.length + 1);
    if (!hidden && below.split("/").some((part) => part.startsWith("."))) continue;
    if (!glob.match(below)) continue;
    const full = `${copy.root}/${entry.path}`;
    found.push(`${relativeToCwd(copy, full)}${entry.dir ? "/" : ""}`);
  }
  if (found.length === 0) return { text: "No files found matching pattern", isError: false };
  if (found.length > limit) return { why: `it finds more than its limit of ${limit} paths` };
  const lines = pathTreeEvents(found).map((event) =>
    event.kind === "dir" ? `${"#".repeat(event.depth + 1)} ${event.name}/` : event.name
  );
  return { text: lines.join("\n"), isError: false };
}

/** Why the words of `commands`, run from the working directory `cwd`, may reach a file outside the
 *  session's checkout, or undefined: a word with a `$` or a brace expansion, whose expansion the
 *  runner does not follow; a word under `~`; one naming `.git` (whose config names the checkout's
 *  source and whose files are the runner's); an absolute one not under the root; a relative one that leaves
 *  the root from any working directory a `cd` before it may have moved to; and an option's value
 *  (after its `=`, or from its first `/` or `~` where it has none) that does so. Symbolic links are
 *  not followed here: the copy holds none that leaves it (`removeOutsideLinks`). */
function escapes(
  copy: CheckoutCopy,
  cwd: string,
  commands: readonly SimpleCommand[]
): string | undefined {
  let cwds = [cwd];
  for (const each of commands) {
    const [program, ...args] = each.words;
    for (const word of [...args, ...each.redirects.map((redirect) => redirect.target)]) {
      if (word.includes("$") || /\{[^}]*(,|\.\.)[^}]*\}/.test(word)) {
        return `${word} expands, and the runner does not check where its expansion leads`;
      }
      const slash = word.search(/[/~]/);
      const value = !word.startsWith("-")
        ? word
        : word.includes("=")
          ? word.slice(word.indexOf("=") + 1)
          : slash < 0
            ? ""
            : word.slice(slash);
      if (value === "" || value === "/dev/null") continue;
      if (value.startsWith("~")) return `${word} is under ~, outside the session's checkout`;
      if (value.split("/").includes(".git")) return `${word} names .git`;
      const fulls = value.startsWith("/")
        ? [path.posix.normalize(value)]
        : cwds.map((at) => path.posix.resolve(at, value));
      if (fulls.some((full) => insideRoot(copy, full) === undefined)) {
        return `${word} is outside the session's checkout ${copy.root}`;
      }
    }
    const target = program === "cd" ? args.find((arg) => !arg.startsWith("-")) : undefined;
    if (target !== undefined) {
      cwds = [...cwds, ...cwds.map((at) => path.posix.resolve(at, target))];
    }
  }
  return undefined;
}

/** The `bash` call's answer (see the file's header). */
async function bashAnswer(
  copy: CheckoutCopy,
  input: Record<string, unknown>
): Promise<CheckoutAnswer | Unanswered> {
  const other = otherKeys(input, ["command", "cwd", "timeout"]);
  if (other !== undefined) return { why: other };
  const command = input.command;
  const commands = readOnlyCommands(command);
  if (typeof command !== "string" || commands === undefined) {
    return { why: "its command is not read-only" };
  }
  if (!copy.git && commands.some((each) => each.words[0] === "git")) {
    return {
      why: "the copy is GitHub's tarball of the commit, which holds no history: the fetch of the commit failed",
    };
  }
  let cwd = copy.cwd;
  if (typeof input.cwd === "string") {
    const resolved = resolvedReadPath(input.cwd, { cwd: copy.cwd });
    if (resolved === undefined || insideRoot(copy, resolved) === undefined) {
      return { why: `its working directory ${input.cwd} is outside the session's checkout` };
    }
    cwd = resolved;
  }
  const leads = escapes(copy, cwd, commands);
  if (leads !== undefined) return { why: leads };
  const real = realpathSync(copy.dir);
  const localCwd = path.join(real, insideRoot(copy, cwd) ?? "");
  if (!statSync(localCwd, { throwIfNoEntry: false })?.isDirectory()) {
    return { why: `its working directory ${cwd} is no directory of the checkout` };
  }
  const mapped = command.split(copy.root).join(real);
  const started = performance.now();
  const child = Bun.spawn(["bash", "--noprofile", "--norc", "-c", `{ ${mapped}\n} 2>&1`], {
    cwd: localCwd,
    env: {
      PATH: "/usr/local/bin:/usr/bin:/bin",
      HOME: path.join(real, ".no-home"),
      LANG: "C.UTF-8",
      TERM: "dumb",
      GIT_CONFIG_NOSYSTEM: "1",
      GIT_CONFIG_GLOBAL: "/dev/null",
      GIT_PAGER: "cat",
      PAGER: "cat",
      GIT_OPTIONAL_LOCKS: "0",
    },
    stdin: "ignore",
    stdout: "pipe",
    stderr: "ignore",
  });
  const limit = timeLimit(child, BASH_TIME_LIMIT_MS);
  const chunks: Uint8Array[] = [];
  let bytes = 0;
  let over = false;
  await eachChunk(child.stdout, limit, (chunk) => {
    bytes += chunk.length;
    if (bytes > OUTPUT_BYTES) {
      over = true;
      child.kill();
      return;
    }
    chunks.push(chunk);
  });
  const exitCode = await child.exited;
  limit.clear();
  const wallTimeMs = performance.now() - started;
  if (limit.timedOut()) return { why: `it ran past the ${BASH_TIME_LIMIT_MS / 1000} s limit` };
  if (over) return { why: `its output passes the ${OUTPUT_BYTES} bytes Oh My Pi shows whole` };
  const output = Buffer.concat(chunks).toString("utf8").split(real).join(copy.root);
  if (output.split("\n").some((line) => line.length > BASH_COLUMNS)) {
    return { why: `a line of its output passes the ${BASH_COLUMNS} characters Oh My Pi shows` };
  }
  const piped = commands.some((each) => each.piped);
  if (
    !piped &&
    output.length >= MINIMIZE_CHARS &&
    commands.some((each) => Object.hasOwn(MINIMIZED_PROGRAMS, each.words[0] ?? ""))
  ) {
    return { why: "Oh My Pi's minimizer rewrites its output, which the runner does not rebuild" };
  }
  const lines = [
    output === "" ? (exitCode === 0 ? "(no output, exit code 0)" : "(no output)") : output,
  ];
  if (wallTimeMs >= 1000) lines.push("", `Wall time: ${(wallTimeMs / 1000).toFixed(2)} seconds`);
  if (exitCode !== 0) lines.push("", `Command exited with code ${exitCode}`);
  return { text: lines.join("\n"), isError: exitCode !== 0 };
}

/** The answer to the read-only call `name` with `input` from `copy` (see the file's header), its
 *  reads by path shown as `rows`, or why it is not answered. */
export async function answerCall(
  copy: CheckoutCopy,
  name: string | undefined,
  input: unknown,
  rows: PathRows
): Promise<CheckoutAnswer | Unanswered> {
  if (typeof input !== "object" || input === null || Array.isArray(input)) {
    return { why: "its input is no object" };
  }
  const fields = input as Record<string, unknown>;
  switch (name) {
    case "read":
      return readAnswer(copy, fields, rows);
    case "grep":
      return grepAnswer(copy, fields, rows);
    case "glob":
      return globAnswer(copy, fields);
    case "bash":
      return bashAnswer(copy, fields);
    default:
      return {
        why: `no capture declares a ${name} tool, so the runner rebuilds none of its forms`,
      };
  }
}
