/**
 * Read-only calls, and their answers from a copy of a written case's checkout.
 *
 * **The read-only rule** (`readOnlyCall`, the one definition the replay's checkout rounds and the
 * behaviour report's inspection floors read): a call of one of READ_ONLY_TOOLS (Oh My Pi's `read`,
 * `grep`, `glob`, `find`, `ls`), or a `bash` call (`parseScript`) whose command is lists joined by
 * `;` or line ends of pipelines joined by `&&` or `||` of commands joined by pipes, each command a
 * simple one, a subshell `( … )` or a group `{ …; }`, every simple command among them, and every
 * one inside a command substitution (`$(…)`, or a backtick with no `\` in it), one of
 * READ_ONLY_PROGRAMS (`commandReadsOnly`): `cd`, `cat`, `head`, `tail`, `ls`, `find` with none of
 * FIND_ACTIONS (`-exec`, `-execdir`, `-ok`, `-okdir`, `-delete`, `-fprint`, `-fprint0`,
 * `-fprintf`, `-fls`), `grep`, `rg` without `--pre`, `wc`, `sed` with `-n` and with no `-i` and no
 * `w`, `W` or `e` command, `nl`, `stat`, `file` without `-C`, `tree` without `-o`, `pwd`, `true`,
 * `echo`, `printf` (its `-v` naming a variable an assignment may set), `env` with no command
 * (options that print the environment, `-u NAME`, then assignments), `unset` (of no variable of
 * UNSET_PROTECTED: PATH, HOME, PWD, GIT_OPTIONAL_LOCKS), `export`, an assignment `NAME=value`
 * alone or before a command, `git` (with at most the global options `-C <dir>`, `--no-pager` and
 * `-P`) whose subcommand is one of READ_ONLY_GIT (`log`, `show`, `diff`, `status`, `grep`,
 * `ls-files`, `blame`, `rev-parse`), `worktree list`, `remote` (alone, `-v`, `show`, `get-url`)
 * or `branch` with no name (alone, `-a`, `-r`, `-v`, `--list`, `--show-current`), with no
 * `--output`, `-O`, `--open-files-in-pager`, `--ext-diff` or `--no-index` (with which `git diff`
 * reads files outside the repository), or `jj` (`jjReadsOnly`) `status`, `st`, `log`, `show`,
 * `diff`, `root`, `file show`, `bookmark list` or `workspace list`, with no `--tool`, `--config`,
 * `--config-toml` or `--config-file` (each may run a program). An assignment, `export` or `printf
 * -v` sets no variable of PROTECTED_NAME (one that picks a program, a library, a configuration or
 * a directory: PATH, HOME, IFS, CDPATH, PAGER, every GIT_*, JJ_*, LD_*, XDG_*, BASH*, …); each
 * affects only the call's own shell. A variable's expansion (`$NAME`, `${NAME}`) is one of a
 * variable the command assigns before it, or of HOME, PATH or PWD. A word of `find`, `rg`, `tree`,
 * `file`, `sed`, `git` or `jj` (whose options may write or run a program) takes one known value
 * (`walkScript`: a literal, or an expansion of a value the command assigned, quoted or holding no
 * blank or glob character). A redirection to a file anywhere makes the call not read-only (a
 * redirection to `/dev/null`, a copy of one stream into another such as `2>&1`, and a read of a
 * file with `<` are none). So do a process substitution, an arithmetic expansion or command, any
 * other expansion (`$?`, `$1`, `${X:-y}`, `$'…'`), a command run in the background (`&`), a
 * heredoc or herestring, a comment, a program named by its path or an expansion, and any other
 * command.
 *
 * **The answers** (`answerCall`): a written case's run (replay.ts's checkout rounds) answers a
 * read-only call from a copy of its context session's checkout (`CheckoutCopy`: the copy's
 * directory, and the session's checkout root and working directory it stands for), as the pinned
 * Oh My Pi (the tag v18.18.6-sami.20261119-162325 of cybermonklabs/oh-my-pi) gives it at its
 * default settings, every path of the session's (the copy's own never shows: a read's header,
 * grep's and glob's paths, and every path bash, git or jj prints, `pwd`, `git rev-parse
 * --show-toplevel`, `git worktree list`, `jj root` and error messages among them, names the
 * session's checkout root and working directory in place of the copy's), or says why it does not
 * (`Unanswered`). A call the copy answers in no form gives the run no verdict (replay.ts's
 * UnansweredRead): the run is made again, never judged on that call.
 *
 * **Confinement.** The calls are the model's, run on this machine, so none may read a file outside
 * the copy. cli.ts removes from each shared checkout, as it makes it, every symbolic link that resolves
 * outside it or not at all (`removeOutsideLinks`), so `cat` of a link, `grep -R`, `rg --follow`,
 * `find -L` and a `read` follow no link out; the runner therefore refuses no option that follows
 * links. Beside that, a `read`, `grep` or `glob` whose target resolves outside the copy is not
 * answered (`leavesCopy`), and a `bash` command is not answered when a word of it may lead outside
 * (`escapes`: a word whose expansion the runner cannot place inside the checkout, a brace
 * expansion, `~`, `.git`, an absolute path off the root, a relative one that leaves the root from
 * any directory a `cd` before it may have moved to, an option's attached path that does) or would
 * show a value of the runner's (`$HOME`, `$PATH`).
 *
 * - `read` (keys `path`, `i`): a whole read by path that resolves (cases.ts's `resolvedReadPath`,
 *   against the session's working directory) to a file under the checkout's root, in the form
 *   arms.ts writes a whole read by path (`pathAnswer`, its rows as the arm's reads by path show
 *   them), or Oh My Pi's not-found result `Path '<path>' not found` for a file the copy lacks. A
 *   file that is no prose is answered so too: a UTF-8 text file that is no notebook, SVG or
 *   document markit converts. The pin may summarize one of 100 lines or more (newlines and one
 *   more; read.ts lines 2519-2533, read-summary.ts's `trySummarize`,
 *   `read.summarize.minTotalLines` 100: a code file whose tree-sitter parse elides a body), which
 *   the runner cannot rebuild: such a file is answered unsummarized in place of the summary, the
 *   same in every arm, and the answer says so (`CheckoutAnswer.wholeText`, which the run records).
 *   A file of more than 300 lines is answered as the pin pages it unsummarized (`read.defaultLimit`
 *   300): its first 300 lines, then `[<n> more lines in file. Use :301 to continue]`. A read with
 *   one line range (arms.ts's `readRange`: `:N-M`, `:N..M`, `:N+K`, `:N`, `:N-`) is answered with
 *   the lines of its range in the pin's range form (arms.ts's `pathRangeAnswer`: the hashline line,
 *   a line before the range and three after it, then `[<n> more lines in file. Use :<next> to
 *   continue]`); where the pin widens a range of a code file to the brackets of the blocks it
 *   cuts, the lines alone; a range that starts past the file's end with the pin's notice, `Line
 *   <n> is beyond end of file (<lines> lines total). Use :1 to read from the start, or :<lines> to
 *   read the last line.` Not answered: a read with another selector (`:raw`, a list, `:-N`, …), of
 *   a directory, outside the root, under `~` or `.git`, and a file the arms refuse for another
 *   reason (`pathFormFault`: not UTF-8 text, a git conflict block, a page past the bytes a read
 *   shows).
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
 *   (packages/coding-agent/src/tools/glob.ts lines 197-375, path-utils.ts's `parseFindPattern`
 *   and `splitDelimitedPathEntry`): `path` (`.` when absent) a pattern, or a list of patterns
 *   joined by `;` (outside braces; unless the whole is a path of the checkout), each read from its
 *   first segment with a glob character (one that starts with a glob character is searched at
 *   every depth); a pattern with none is a directory, every file and directory under which
 *   matches, or a file, which answers itself. The matching files and directories (a directory's
 *   path ending in `/`), as a prefix-folded tree (`formatGroupedPaths`), or `No files found
 *   matching pattern`; dotfiles left out when `hidden` is false; then, each on a line of its own,
 *   `Requested limit <n> clamped to the max of 200` for a limit above it and `Skipped missing
 *   paths: <patterns>` for the patterns of a list whose directory is not there. Oh My Pi orders
 *   them by their time of change, which the copy does not keep, so in path order. Not answered: a
 *   list joined by commas or blanks, a base outside the root, one pattern (or every pattern of a
 *   list) whose base is not there or is a file under a glob (the pin's error is not rebuilt), and
 *   more matches than `limit` (200 when absent).
 * - `bash` (keys `command`, `cwd`, `timeout`, `i`): the command run in the copy (its working
 *   directory the session's mapped into the copy; the checkout's root in the command mapped to
 *   the copy, and the copy's path, as given and as its real path, back to the root in the
 *   output), by `bash -c` with its standard error joined to its output, an environment of its own
 *   (`shellEnvironment`: no variable of the runner's; PATH the system's and the directory of the
 *   runner's jj; HOME `/nonexistent`, so no file of the repository is read as the user's git or jj
 *   configuration even where the command unsets GIT_CONFIG_GLOBAL; git's system and global
 *   configuration none), within BASH_TIME_LIMIT_MS; its text as Oh My Pi's bash tool gives it
 *   (bash.ts lines 765-922): the output as printed, `(no output, exit code 0)` or `(no output)`
 *   for none, `Wall time: <s> seconds` after it when the run took a second or more, and `Command
 *   exited with code <n>` after it, an error, when it failed. Oh My Pi's bash interceptor is off
 *   at its default settings (exec/settings.ts lines 170-180), so no command is blocked. Its git
 *   commands run on the copy's own history: the copy is the shared checkout of the commit (cli.ts's
 *   `sharedCheckout`: a fetch of the commit 50 commits deep with no blobs, checked out with a
 *   detached HEAD), so `git log` shows at most 50 commits and then the shallow boundary, and `git
 *   status` names no branch (and shows each link removed as deleted); git runs with
 *   GIT_OPTIONAL_LOCKS=0, so `git status` writes no index into the shared checkout. Its jj
 *   commands run on the copy too: where the context session ran jj, cli.ts made the copy a
 *   colocated jj repository (`colocateJj`), so they answer as in a jj workspace at the commit;
 *   where it ran none, as jj does in a checkout that is no jj repository (`There is no jj repo`).
 *   Not answered: a git or jj command where the copy is GitHub's tarball instead (the fetch
 *   failed: `CheckoutCopy.git` false), a jj command where the copy could not be made a jj
 *   repository (`CheckoutCopy.jj` a why) or the runner has no jj, a command a word of which may
 *   lead outside the copy (`escapes`), an output past 50 KB (`tools.artifactSpillThreshold`, which
 *   Oh My Pi elides) or with a line past 768 characters (`tools.outputMaxColumns`), an output of
 *   1,000 characters or more of a command Oh My Pi's minimizer rewrites
 *   (crates/pi-shell/src/minimizer/engine.rs lines 16-125: a command of MINIMIZED_PROGRAMS in a
 *   command with no pipe), and a run past the time limit.
 * - `find` and `ls`: no capture declares them, so they are not answered.
 */
import { readdirSync, readFileSync, realpathSync, rmSync, statSync } from "node:fs";
import path from "node:path";
import { fileTag, type PathRows, pathAnswer, pathRangeAnswer, readRange, shownPath } from "./arms";
import { insideRoot, resolvedReadPath, textLines } from "./cases";
import { eachChunk, firstLine, textOf, timeLimit } from "./common";

/** The tools whose every call is read-only. */
export const READ_ONLY_TOOLS = ["read", "grep", "glob", "find", "ls"] as const;
/** The programs a read-only `bash` call's commands may run (see the file's header). */
const READ_ONLY_PROGRAMS = [
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
  "jj",
  "pwd",
  "true",
  "echo",
  "printf",
  "env",
  "unset",
  "export",
] as const;
/** The git subcommands a read-only `bash` call may run, beside `worktree list`, `remote` and
 *  `branch` with no name (`gitReadsOnly`). */
const READ_ONLY_GIT = [
  "log",
  "show",
  "diff",
  "status",
  "grep",
  "ls-files",
  "blame",
  "rev-parse",
] as const;
/** The options of `git branch` that name no branch to make, move or delete. */
const GIT_BRANCH_LISTS = /^(-[arvl]+|--all|--remotes|--verbose|--list|--show-current)$/;
/** The jj commands a read-only `bash` call may run: the words after `jj` and its options. */
const READ_ONLY_JJ = [
  "status",
  "st",
  "log",
  "show",
  "diff",
  "root",
  "file show",
  "bookmark list",
  "workspace list",
] as const;
/** jj's options that take the next word as their value. */
const JJ_VALUE_OPTIONS: Readonly<Record<string, true>> = {
  "-R": true,
  "--repository": true,
  "--at-op": true,
  "--at-operation": true,
  "--color": true,
};
/** jj's options that run a program or read a configuration that may (`ui.diff-formatter`). */
const JJ_RUNS = /^--(tool|config|config-toml|config-file)(=|$)/;
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
/** A shell variable's name. */
const NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;
/** The variables an assignment, `export` or `printf -v` may not set: each picks a program, a
 *  library, a configuration or a directory the shell or a read-only program reads. */
const PROTECTED_NAME =
  /^(PATH|HOME|PWD|OLDPWD|CDPATH|IFS|ENV|SHELLOPTS|GLOBIGNORE|PS4|PROMPT_COMMAND|SHELL|TMPDIR|PAGER|MANPAGER|EDITOR|VISUAL|MAGIC|RIPGREP_CONFIG_PATH|GCONV_PATH|LOCPATH|NLSPATH|GLIBC_TUNABLES|TERMINFO|TERMINFO_DIRS|(BASH|GIT_|JJ_|LD_|DYLD_|XDG_|LESS)\w*)$/;
/** The variables `unset` may not unset: with no PATH the shell runs a program of the checkout's,
 *  and the others are the answers' own (`shellEnvironment`). */
const UNSET_PROTECTED: Readonly<Record<string, true>> = {
  PATH: true,
  HOME: true,
  PWD: true,
  GIT_OPTIONAL_LOCKS: true,
};
/** The shell's own variables a read-only command may expand without assigning them. */
const SHELL_OWN: Readonly<Record<string, true>> = { HOME: true, PATH: true, PWD: true };
/** The read-only programs that open no file their words name. */
const NO_PATH_PROGRAMS: Readonly<Record<string, true>> = {
  pwd: true,
  true: true,
  echo: true,
  printf: true,
  env: true,
  unset: true,
  export: true,
};
/** The most values a word may take that the confinement follows. */
const MAX_VALUES = 64;

/** A part of a word: literal text, a variable's expansion (`$NAME`, `${NAME}`), or a command
 *  substitution (`$(…)` or a backtick, its text `source`), each in quotes or not (`quoted`). */
type WordPart =
  | { readonly kind: "text"; readonly text: string; readonly quoted: boolean }
  | { readonly kind: "var"; readonly name: string; readonly quoted: boolean }
  | {
      readonly kind: "sub";
      readonly script: Script;
      readonly source: string;
      readonly quoted: boolean;
    };
/** A word of a command, its parts in order. */
type Word = readonly WordPart[];
/** An assignment `NAME=value`. */
interface Assignment {
  readonly name: string;
  readonly value: Word;
}
/** A redirection: its operator (after its stream's digits) and its target. */
interface Redirect {
  readonly op: string;
  readonly target: Word;
}
/** A simple command: the assignments before its words, its words, and its redirections. */
interface SimpleCommand {
  readonly kind: "simple";
  readonly assignments: readonly Assignment[];
  readonly words: readonly Word[];
  readonly redirects: readonly Redirect[];
}
/** A subshell `( … )` or a group `{ …; }`, and its redirections. */
interface Group {
  readonly kind: "group";
  readonly subshell: boolean;
  readonly script: Script;
  readonly redirects: readonly Redirect[];
}
/** A parsed command: lists joined by `;` or line ends, each of pipelines joined by `&&` or `||`,
 *  each of commands joined by `|`. */
type Script = readonly (readonly (readonly (SimpleCommand | Group)[])[])[];

/** Thrown inside `parseScript` at a shell form the read-only rule does not take. */
class Untaken extends Error {}

/** `command` parsed, or undefined when it holds a shell form the read-only rule does not take: a
 *  background `&`, a heredoc or herestring, a process substitution, a comment, an arithmetic
 *  expansion or command, an expansion other than `$NAME`, `${NAME}`, `$(…)` and a backtick with no
 *  `\` in it, `;;`, `|&`, or a quote left open. */
function parseScript(command: string): Script | undefined {
  let at = 0;
  const fail = (): never => {
    throw new Untaken();
  };
  const peek = (offset = 0): string | undefined => command[at + offset];
  const skipBlanks = (newlines: boolean): void => {
    for (;;) {
      const ch = peek();
      if (ch === " " || ch === "\t" || (newlines && ch === "\n")) at++;
      else if (ch === "\\" && peek(1) === "\n") at += 2;
      else return;
    }
  };
  /** Whether the `}` at `at` is a word of its own, which closes a group where a command starts. */
  const closesGroup = (): boolean => {
    const next = peek(1);
    return peek() === "}" && (next === undefined || " \t\n;|&)<>".includes(next));
  };
  const list = (end: "" | ")" | "}"): Script => {
    const lists: (SimpleCommand | Group)[][][] = [];
    for (;;) {
      skipBlanks(true);
      const ch = peek();
      if (ch === undefined) {
        if (end !== "") fail();
        break;
      }
      if ((end === ")" && ch === ")") || (end === "}" && closesGroup())) break;
      lists.push(andOr());
      skipBlanks(false);
      const next = peek();
      if (next === ";") {
        if (peek(1) === ";" || peek(1) === "&") fail();
        at++;
      } else if (next === "\n") at++;
      else if (next !== undefined && next !== ")") fail();
    }
    if (lists.length === 0) fail();
    return lists;
  };
  const andOr = (): (SimpleCommand | Group)[][] => {
    const pipelines = [pipeline()];
    for (;;) {
      skipBlanks(false);
      const pair = `${peek() ?? ""}${peek(1) ?? ""}`;
      if (pair !== "&&" && pair !== "||") return pipelines;
      at += 2;
      skipBlanks(true);
      pipelines.push(pipeline());
    }
  };
  const pipeline = (): (SimpleCommand | Group)[] => {
    const commands = [commandAt()];
    for (;;) {
      skipBlanks(false);
      if (peek() !== "|" || peek(1) === "|") return commands;
      if (peek(1) === "&") fail();
      at++;
      skipBlanks(true);
      commands.push(commandAt());
    }
  };
  const commandAt = (): SimpleCommand | Group => {
    skipBlanks(false);
    const ch = peek();
    const subshell = ch === "(";
    if (subshell || (ch === "{" && " \t\n".includes(peek(1) ?? "x"))) {
      if (subshell && peek(1) === "(") fail();
      at++;
      const script = list(subshell ? ")" : "}");
      at++;
      const redirects: Redirect[] = [];
      for (;;) {
        skipBlanks(false);
        const redirect = redirectAt();
        if (redirect === undefined) break;
        redirects.push(redirect);
      }
      const next = peek();
      if (next !== undefined && !";\n|&)".includes(next)) fail();
      return { kind: "group", subshell, script, redirects };
    }
    return simpleAt();
  };
  const simpleAt = (): SimpleCommand => {
    const assignments: Assignment[] = [];
    const words: Word[] = [];
    const redirects: Redirect[] = [];
    for (;;) {
      skipBlanks(false);
      const ch = peek();
      if (ch === undefined || ";\n|)".includes(ch) || (ch === "&" && peek(1) !== ">")) break;
      const redirect = redirectAt();
      if (redirect !== undefined) {
        redirects.push(redirect);
        continue;
      }
      // A `(` here, a comment, or a `}` where a command starts.
      if (ch === "(" || ch === "#") fail();
      if (words.length === 0 && assignments.length === 0 && closesGroup()) fail();
      const word = wordAt();
      const assignment = words.length === 0 ? assignmentOf(word) : undefined;
      if (assignment === undefined) words.push(word);
      else assignments.push(assignment);
    }
    if (assignments.length === 0 && words.length === 0 && redirects.length === 0) fail();
    return { kind: "simple", assignments, words, redirects };
  };
  const redirectAt = (): Redirect | undefined => {
    const fd = /^\d*/.exec(command.slice(at))?.[0] ?? "";
    const start = at + fd.length;
    const ch = command[start];
    const next = command[start + 1];
    let op: string;
    if (ch === "&" && fd === "" && next === ">") op = command[start + 2] === ">" ? "&>>" : "&>";
    else if (ch === ">" || ch === "<") {
      if (next === "(") fail();
      op = ch;
      if (ch === ">" && (next === ">" || next === "|" || next === "&")) op += next;
      if (ch === "<" && (next === "<" || next === "&" || next === ">")) op += next;
      if (op === "<<") fail();
    } else return undefined;
    at = start + op.length;
    skipBlanks(false);
    const target = peek();
    if (target === undefined || " \t\n;&|()<>".includes(target)) fail();
    return { op: `${fd}${op}`, target: wordAt() };
  };
  const wordAt = (): Word => {
    const parts: WordPart[] = [];
    const text = (more: string, quoted: boolean): void => {
      const last = parts.at(-1);
      if (last?.kind === "text" && last.quoted === quoted) {
        parts[parts.length - 1] = { kind: "text", text: last.text + more, quoted };
      } else parts.push({ kind: "text", text: more, quoted });
    };
    const dollar = (quoted: boolean): void => {
      const next = peek(1);
      if (next === "(") {
        if (peek(2) === "(") fail();
        at += 2;
        const start = at;
        const script = list(")");
        parts.push({ kind: "sub", script, source: command.slice(start, at), quoted });
        at++;
        return;
      }
      if (next === "{") {
        const end = command.indexOf("}", at + 2);
        const name = end < 0 ? "" : command.slice(at + 2, end);
        if (!NAME.test(name)) fail();
        parts.push({ kind: "var", name, quoted });
        at = end + 1;
        return;
      }
      const name = /^[A-Za-z_][A-Za-z0-9_]*/.exec(command.slice(at + 1))?.[0];
      if (name !== undefined) {
        parts.push({ kind: "var", name, quoted });
        at += 1 + name.length;
        return;
      }
      // A special parameter, or (unquoted) `$'…'` or `$"…"`.
      if (next !== undefined && (/[0-9?#@*!$-]/.test(next) || (!quoted && `'"`.includes(next)))) {
        fail();
      }
      text("$", quoted);
      at++;
    };
    const backtick = (quoted: boolean): void => {
      const end = command.indexOf("`", at + 1);
      if (end < 0) fail();
      const source = command.slice(at + 1, end);
      const script = source.includes("\\") ? undefined : parseScript(source);
      if (script === undefined) fail();
      else parts.push({ kind: "sub", script, source, quoted });
      at = end + 1;
    };
    for (;;) {
      const ch = peek();
      if (ch === undefined || " \t\n;&|()<>".includes(ch)) break;
      if (ch === "'") {
        const end = command.indexOf("'", at + 1);
        if (end < 0) fail();
        text(command.slice(at + 1, end), true);
        at = end + 1;
      } else if (ch === '"') {
        at++;
        text("", true);
        for (;;) {
          const inner = peek();
          if (inner === undefined) fail();
          if (inner === '"') break;
          if (inner === "\\") {
            const escaped = peek(1);
            if (escaped === undefined) fail();
            else if ('"\\$`\n'.includes(escaped)) text(escaped === "\n" ? "" : escaped, true);
            else text(`\\${escaped}`, true);
            at += 2;
          } else if (inner === "$") dollar(true);
          else if (inner === "`") backtick(true);
          else {
            text(inner ?? "", true);
            at++;
          }
        }
        at++;
      } else if (ch === "\\") {
        const next = peek(1);
        if (next === undefined) fail();
        if (next !== "\n") text(next ?? "", true);
        at += 2;
      } else if (ch === "$") dollar(false);
      else if (ch === "`") backtick(false);
      else {
        text(ch, false);
        at++;
      }
    }
    if (parts.length === 0) fail();
    return parts;
  };
  try {
    return list("");
  } catch (error) {
    if (error instanceof Untaken) return undefined;
    throw error;
  }
}

/** The assignment `word` makes, `NAME=value` with its name and `=` unquoted, or undefined. */
function assignmentOf(word: Word): Assignment | undefined {
  const [first, ...rest] = word;
  if (first?.kind !== "text" || first.quoted) return undefined;
  const match = /^([A-Za-z_][A-Za-z0-9_]*)=/.exec(first.text);
  if (match?.[1] === undefined) return undefined;
  const after = first.text.slice(match[0].length);
  return {
    name: match[1],
    value: after === "" ? rest : [{ kind: "text", text: after, quoted: false }, ...rest],
  };
}

/** The text of `word`, its quotes taken off and each expansion as written (`$NAME`, `$(…)`). */
function wordText(word: Word): string {
  return word
    .map((part) =>
      part.kind === "text" ? part.text : part.kind === "var" ? `$${part.name}` : `$(${part.source})`
    )
    .join("");
}

/** The text of `word` when it expands nothing, else undefined. */
function literal(word: Word): string | undefined {
  return word.every((part) => part.kind === "text") ? wordText(word) : undefined;
}

/** What a variable or a word may hold at a point of a command: each value it may take, or
 *  undefined when one of them is unknown. */
type Values = ReadonlySet<string> | undefined;

/** A command's shell at a point of it: each variable the command assigned, exported or unset
 *  before it, with what it may hold; and every directory it may be in (the session's paths; none
 *  where the walk follows no directory). */
interface ShellState {
  readonly vars: Map<string, Values>;
  cwds: readonly string[];
}

/** A walk of a command (`walkScript`): `check`, why one simple command at its point fails, or
 *  undefined; `subValues`, what a command substitution may print, as a word's value. */
interface Walk {
  readonly check: (command: SimpleCommand, state: ShellState) => string | undefined;
  readonly subValues: (script: Script, state: ShellState) => Values;
}

/** `state` for a shell of its own: what that shell changes stays there. */
function forked(state: ShellState): ShellState {
  return { vars: new Map(state.vars), cwds: state.cwds };
}

/** What the variable `name` may hold at `state`: what the command gave it, or for `$PWD` each
 *  directory the shell may be in; undefined when unknown. */
function variableValues(name: string, state: ShellState): Values {
  if (state.vars.has(name)) return state.vars.get(name);
  return name === "PWD" && state.cwds.length > 0 ? new Set(state.cwds) : undefined;
}

/** Every value `word` may take at `state` (`walk.subValues` giving a substitution's), or undefined
 *  when one is unknown, or more than MAX_VALUES: an unquoted expansion the shell may split or match
 *  against files (empty, or holding a blank or a glob character) is unknown. */
function valuesOf(word: Word, state: ShellState, walk: Walk): Values {
  let values = new Set([""]);
  for (const part of word) {
    let held: Values;
    if (part.kind === "text") held = new Set([part.text]);
    else {
      held =
        part.kind === "var" ? variableValues(part.name, state) : walk.subValues(part.script, state);
      if (
        !part.quoted &&
        [...(held ?? [])].some((value) => value === "" || /[\s*?[]/.test(value))
      ) {
        held = undefined;
      }
    }
    if (held === undefined) return undefined;
    const next = new Set<string>();
    for (const before of values) for (const value of held) next.add(before + value);
    if (next.size > MAX_VALUES) return undefined;
    values = next;
  }
  return values;
}

/** Walks `script` from `state` (see the file's header): gives `walk.check` each simple command at
 *  its point (a group's redirections as a command of no words), each command substitution's
 *  before the command that holds it; and keeps in `state` what each command's assignments,
 *  `export`, `unset`, `printf -v` and `cd` change, only as possible where `maybe` (a pipeline
 *  after the first of an `&&` or `||` list), and only in the shell that runs it (a subshell, a
 *  substitution and each command of a pipeline of several run in one of their own). Returns the
 *  first why `walk.check` gives. */
function walkScript(
  script: Script,
  state: ShellState,
  maybe: boolean,
  walk: Walk
): string | undefined {
  for (const pipelines of script) {
    for (const [index, pipeline] of pipelines.entries()) {
      for (const command of pipeline) {
        const own = pipeline.length > 1 ? forked(state) : state;
        const why = walkCommand(command, own, maybe || index > 0, walk);
        if (why !== undefined) return why;
      }
    }
  }
  return undefined;
}

/** `walkScript` of one command. */
function walkCommand(
  command: SimpleCommand | Group,
  state: ShellState,
  maybe: boolean,
  walk: Walk
): string | undefined {
  const words =
    command.kind === "simple"
      ? [...command.words, ...command.assignments.map((each) => each.value)]
      : [];
  for (const word of [...words, ...command.redirects.map((each) => each.target)]) {
    for (const part of word) {
      if (part.kind !== "sub") continue;
      const why = walkScript(part.script, forked(state), false, walk);
      if (why !== undefined) return why;
    }
  }
  if (command.kind === "group") {
    const inner = command.subshell ? forked(state) : state;
    const why = walkScript(command.script, inner, maybe, walk);
    if (why !== undefined) return why;
    const redirects: SimpleCommand = {
      kind: "simple",
      assignments: [],
      words: [],
      redirects: command.redirects,
    };
    return walk.check(redirects, state);
  }
  const why = walk.check(command, state);
  if (why === undefined) changeState(command, state, maybe, walk);
  return why;
}

/** Keeps in `state` what the simple command `command` changes in its shell (see `walkScript`). */
function changeState(command: SimpleCommand, state: ShellState, maybe: boolean, walk: Walk): void {
  const set = (name: string, values: Values) => {
    const held = state.vars.get(name);
    if (!maybe) state.vars.set(name, values);
    else if (held === undefined || values === undefined) state.vars.set(name, undefined);
    else state.vars.set(name, new Set([...held, ...values]));
  };
  const [first, ...args] = command.words;
  if (first === undefined) {
    for (const { name, value } of command.assignments) set(name, valuesOf(value, state, walk));
    return;
  }
  const program = literal(first);
  const texts = args.map(literal);
  if (program === "export") {
    for (const word of args) {
      const assignment = assignmentOf(word);
      if (assignment !== undefined) set(assignment.name, valuesOf(assignment.value, state, walk));
    }
  } else if (program === "unset" && !texts.includes("-f")) {
    for (const name of texts) if (name !== undefined && NAME.test(name)) set(name, new Set([""]));
  } else if (program === "printf" && texts[0] === "-v" && texts[1] !== undefined) {
    set(texts[1], undefined);
  } else if (program === "cd") {
    const target = args.find((word) => !(literal(word) ?? "").startsWith("-"));
    const values = target === undefined ? undefined : valuesOf(target, state, walk);
    if (values === undefined) return;
    const moved = state.cwds.flatMap((at) => [...values].map((to) => path.posix.resolve(at, to)));
    state.cwds = [...new Set([...state.cwds, ...moved])];
  }
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
  const rest = args.slice(at + 1);
  const writes = rest.some(
    (arg) =>
      /^(--output(=|$)|-O|--open-files-in-pager|--ext-diff)/.test(arg) || arg === "--no-index"
  );
  if (writes) return false;
  switch (subcommand) {
    case "worktree":
      return rest[0] === "list";
    case "remote": {
      const [verb] = rest.filter((arg) => arg !== "-v" && arg !== "--verbose");
      return verb === undefined || verb === "show" || verb === "get-url";
    }
    case "branch":
      return rest.every((arg) => GIT_BRANCH_LISTS.test(arg));
    default:
      return (READ_ONLY_GIT as readonly (string | undefined)[]).includes(subcommand);
  }
}

/** Whether the jj command of `args` (the words after `jj`) only reads (see the file's header). */
function jjReadsOnly(args: readonly string[]): boolean {
  if (args.some((arg) => JJ_RUNS.test(arg))) return false;
  const named: string[] = [];
  for (let at = 0; at < args.length; at++) {
    const arg = args[at] ?? "";
    if (Object.hasOwn(JJ_VALUE_OPTIONS, arg)) at++;
    else if (!arg.startsWith("-")) named.push(arg);
  }
  const [first = "", second = ""] = named;
  const command = ["file", "bookmark", "workspace"].includes(first) ? `${first} ${second}` : first;
  return (READ_ONLY_JJ as readonly string[]).includes(command);
}

/** Whether `env`'s words `args` run no command: options that print the environment, then
 *  assignments. */
function envReadsOnly(args: readonly Word[]): boolean {
  let assigned = false;
  for (let at = 0; at < args.length; at++) {
    const word = args[at] ?? [];
    const text = literal(word);
    if (assignmentOf(word) !== undefined) {
      assigned = true;
      continue;
    }
    if (assigned || text === undefined) return false;
    if (text === "-u" || text === "--unset") {
      at++;
      if (!NAME.test(literal(args[at] ?? []) ?? "")) return false;
    } else if (!/^(-0|--null|-i|--ignore-environment|-|-u\w+|--unset=\w+)$/.test(text)) {
      return false;
    }
  }
  return true;
}

/** Whether the read-only program `program` with `args` (each word and its one value, undefined
 *  where it has several or one unknown) only reads (see the file's header). */
function commandReadsOnly(
  program: string,
  args: readonly { word: Word; value: string | undefined }[]
): boolean {
  const values = args.map((arg) => arg.value);
  const known = (each: (string | undefined)[]): each is string[] =>
    each.every((value) => value !== undefined);
  const texts = args.map((arg) => literal(arg.word));
  const settable = (name: string | undefined) =>
    name !== undefined && NAME.test(name) && !PROTECTED_NAME.test(name);
  switch (program) {
    case "find":
      return known(values) && values.every((arg) => !Object.hasOwn(FIND_ACTIONS, arg));
    case "rg":
      return known(values) && values.every((arg) => arg !== "--pre" && !arg.startsWith("--pre="));
    case "tree":
      return known(values) && values.every((arg) => arg !== "-o");
    case "file":
      return known(values) && values.every((arg) => arg !== "-C" && arg !== "--compile");
    case "git":
      return known(values) && gitReadsOnly(values);
    case "jj":
      return known(values) && jjReadsOnly(values);
    case "env":
      return envReadsOnly(args.map((arg) => arg.word));
    case "unset":
      return texts.every(
        (name) =>
          name === "-v" ||
          name === "-f" ||
          (name !== undefined && NAME.test(name) && !Object.hasOwn(UNSET_PROTECTED, name))
      );
    case "export":
      return args.every(
        ({ word }) => literal(word) === "-p" || settable(assignmentOf(word)?.name ?? literal(word))
      );
    case "printf":
      return (
        args.length === 0 ||
        (values[0] !== undefined && (values[0] !== "-v" || settable(values[1])))
      );
    case "sed": {
      if (!known(values)) return false;
      const flags = values.filter((arg) => arg.startsWith("-"));
      const quiet = flags.some(
        (arg) => arg === "--quiet" || arg === "--silent" || /^-[a-zA-Z]*n[a-zA-Z]*$/.test(arg)
      );
      const inPlace = flags.some((arg) => arg.startsWith("--in-place") || /^-[a-zA-Z]*i/.test(arg));
      const writes = values.some(
        (arg) =>
          !arg.startsWith("-") && (/[wW]\s+\S/.test(arg) || /(^|[^\\a-zA-Z])e(\s|;|}|$)/.test(arg))
      );
      return quiet && !inPlace && !writes;
    }
    default:
      return true;
  }
}

/** A walk that follows no command substitution's output. */
const READ_ONLY_WALK: Walk = {
  check: readOnlyFault,
  subValues: () => undefined,
};

/** Why the simple command `command` at `state` is not read-only (see the file's header), or
 *  undefined. */
function readOnlyFault(command: SimpleCommand, state: ShellState): string | undefined {
  // A word's one value, or undefined when it may take several or one unknown.
  const values = (word: Word) => {
    const held = valuesOf(word, state, READ_ONLY_WALK);
    return held?.size === 1 ? [...held][0] : undefined;
  };
  const words = [
    ...command.words,
    ...command.assignments.map((each) => each.value),
    ...command.redirects.map((each) => each.target),
  ];
  for (const part of words.flat()) {
    if (part.kind === "var" && !state.vars.has(part.name) && !Object.hasOwn(SHELL_OWN, part.name)) {
      return `it expands $${part.name}, which it does not assign`;
    }
  }
  for (const { name } of command.assignments) {
    if (PROTECTED_NAME.test(name)) return `it assigns ${name}`;
  }
  for (const { op, target } of command.redirects) {
    const value = values(target);
    if (value === undefined || !harmlessRedirect({ op, target: value })) {
      return `it redirects to ${wordText(target)}`;
    }
  }
  const [first, ...rest] = command.words;
  if (first === undefined) return undefined;
  const program = literal(first);
  if (program === undefined || !(READ_ONLY_PROGRAMS as readonly string[]).includes(program)) {
    return `it runs ${wordText(first)}`;
  }
  const args = rest.map((word) => ({ word, value: values(word) }));
  return commandReadsOnly(program, args) ? undefined : `its ${program} command may write`;
}

/** The parsed `bash` command `command` when it is read-only (see the file's header), else
 *  undefined. */
function readOnlyScript(command: unknown): Script | undefined {
  if (typeof command !== "string" || command.trim() === "") return undefined;
  const script = parseScript(command);
  if (script === undefined) return undefined;
  const fault = walkScript(script, { vars: new Map(), cwds: [] }, false, READ_ONLY_WALK);
  return fault === undefined ? script : undefined;
}

/** The simple commands of `script`, those of its groups and command substitutions among them. */
function simpleCommands(script: Script): SimpleCommand[] {
  return script.flat(2).flatMap((command): SimpleCommand[] => {
    const words =
      command.kind === "simple"
        ? [...command.words, ...command.assignments.map((each) => each.value)]
        : [];
    const subs = [...words, ...command.redirects.map((each) => each.target)]
      .flat()
      .flatMap((part) => (part.kind === "sub" ? simpleCommands(part.script) : []));
    return [...(command.kind === "simple" ? [command] : simpleCommands(command.script)), ...subs];
  });
}

/** Whether `script`, its groups or its substitutions hold a pipeline of several commands. */
function hasPipe(script: Script): boolean {
  return script.flat().some(
    (pipeline) =>
      pipeline.length > 1 ||
      pipeline.some((command) => {
        const words =
          command.kind === "simple"
            ? [...command.words, ...command.assignments.map((each) => each.value)]
            : [];
        const inner = command.kind === "group" ? [command.script] : [];
        const subs = [...words, ...command.redirects.map((each) => each.target)]
          .flat()
          .flatMap((part) => (part.kind === "sub" ? [part.script] : []));
        return [...inner, ...subs].some(hasPipe);
      })
  );
}

/** Whether the tool call `name` with `input` is read-only: the one rule of the replay's checkout
 *  rounds and the report's inspection floors (see the file's header). */
export function readOnlyCall(name: string | undefined, input: unknown): boolean {
  if ((READ_ONLY_TOOLS as readonly (string | undefined)[]).includes(name)) return true;
  if (name !== "bash" || typeof input !== "object" || input === null) return false;
  return readOnlyScript(Reflect.get(input, "command")) !== undefined;
}

// ---------------------------------------------------------------------------------------------
// Answers from a copy of the checkout.

/** A copy of a written case's checkout: `dir`, the copy on this machine; `root`, the checkout's
 *  directory on the session's machine (cases.ts's `checkout.root`); `cwd`, the session's working
 *  directory, inside `root`; `git`, whether the copy is a git checkout at the commit, whose history
 *  git commands read (else GitHub's tarball of the commit, which holds none); `jj`, where the
 *  context session ran jj (cli.ts), true when the copy was made a colocated jj repository
 *  (`colocateJj`), whose jj commands answer as in a jj workspace at the commit, else why it could
 *  not be, and no jj command is answered (absent, a jj command answers as jj does in a checkout
 *  that is no jj repository); `answers`, the answers given so far to its `read`, `grep` and `glob`
 *  calls, by tool, rows and input (the copy is read-only once made, so each such answer depends on
 *  those alone; a `bash` answer is not kept, its wall time being its own run's), which one stage's
 *  runs share (cli.ts); absent, none is kept. */
export interface CheckoutCopy {
  readonly dir: string;
  readonly root: string;
  readonly cwd: string;
  readonly git: boolean;
  readonly jj?: true | Unanswered;
  readonly answers?: Map<string, CheckoutAnswer | Unanswered>;
}

/** A call's answer: the text the tool gives, whether it is an error, and, for a read of a file the
 *  pin may show summarized, that it was given whole in place of the summary (`wholeText`). */
export interface CheckoutAnswer {
  readonly text: string;
  readonly isError: boolean;
  readonly wholeText?: true;
}

/** Why a call is not answered. */
export interface Unanswered {
  readonly why: string;
}

/** The directories a `bash` answer's shell finds its programs in, before the runner's jj's. */
const SYSTEM_PATH = ["/usr/local/bin", "/usr/bin", "/bin"];
/** The home directory of a `bash` answer's shell: outside the copy and on no machine, so no file
 *  the repository holds (a `.gitconfig`, a `.jjconfig.toml`) is read as the user's
 *  configuration, even where the command unsets GIT_CONFIG_GLOBAL. */
const NO_HOME = "/nonexistent";

/** The environment a `bash` answer runs in, and the copy's colocation (`colocateJj`): none of the
 *  runner's variables; its PATH SYSTEM_PATH then the directory of the runner's jj (`jj`, from the
 *  runner's PATH), its home NO_HOME, git's system and global configuration none, its pagers
 *  `cat`, and GIT_OPTIONAL_LOCKS=0, so `git status` writes no index into the shared checkout. */
function shellEnvironment(jj: string | undefined): Record<string, string> {
  const jjDir = jj === undefined ? undefined : path.dirname(jj);
  const dirs =
    jjDir === undefined || SYSTEM_PATH.includes(jjDir) ? SYSTEM_PATH : [...SYSTEM_PATH, jjDir];
  return {
    PATH: dirs.join(":"),
    HOME: NO_HOME,
    LANG: "C.UTF-8",
    TERM: "dumb",
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_CONFIG_SYSTEM: "/dev/null",
    GIT_CONFIG_GLOBAL: "/dev/null",
    GIT_PAGER: "cat",
    PAGER: "cat",
    GIT_OPTIONAL_LOCKS: "0",
  };
}

/** The longest one jj command of a copy's colocation may run: its snapshot reads every file of the
 *  checkout, as cli.ts's git checkout writes them (30 minutes there too). */
const JJ_TIME_LIMIT_MS = 1_800_000;

/** Makes the copy `dir` (a git checkout at its commit, its outside links removed) a colocated jj
 *  repository, as cli.ts makes the shared checkout of a session that ran jj: `jj git init
 *  --colocate`, then `jj status`, whose snapshot of the working copy (each link removed seen as
 *  deleted) is taken here, once, so that no answer's jj command writes to the copy. Each runs with
 *  the runner's jj in the answers' environment (`shellEnvironment`). True when made; else why not,
 *  with the `.jj` it made removed. */
export async function colocateJj(dir: string): Promise<true | Unanswered> {
  const jj = Bun.which("jj") ?? undefined;
  if (jj === undefined) return { why: "jj is not on the runner's PATH" };
  for (const args of [["git", "init", "--colocate"], ["status"]]) {
    const child = Bun.spawn([jj, ...args], {
      cwd: dir,
      env: shellEnvironment(jj),
      stdin: "ignore",
      stdout: "ignore",
      stderr: "pipe",
    });
    const limit = timeLimit(child, JJ_TIME_LIMIT_MS);
    const [stderr, code] = await Promise.all([textOf(child.stderr, limit), child.exited]);
    limit.clear();
    if (code !== 0 || limit.timedOut()) {
      rmSync(path.join(dir, ".jj"), { recursive: true, force: true });
      const said = limit.timedOut()
        ? `it ran past ${JJ_TIME_LIMIT_MS / 1000} s`
        : firstLine(stderr).split(dir).join(".");
      return { why: `jj ${args.join(" ")} failed: ${said}` };
    }
  }
  return true;
}

/** The longest a `bash` call's command may run in the copy before it is ended, unanswered. */
const BASH_TIME_LIMIT_MS = 30_000;
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
/** GlobTool's limit when the call gives none, and the largest it takes (glob.ts lines 46-47). */
const GLOB_LIMIT = 200;

/** Why `input` names an option the copy does not answer, or undefined. */
function otherKeys(input: Record<string, unknown>, keys: readonly string[]): string | undefined {
  const other = Object.keys(input).filter((key) => key !== "i" && !keys.includes(key));
  return other.length === 0
    ? undefined
    : `its options ${other.join(", ")} are none the copy answers`;
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
  const inside = full === undefined ? undefined : insideRoot(copy.root, full);
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
  const ranged = readRange(target);
  if (ranged !== undefined && ranged.range === undefined) {
    return {
      why: `${target} has the selector :${ranged.selector}, of a form the runner does not rebuild (it rebuilds one line range: :N-M, :N+K, :N)`,
    };
  }
  const read = ranged?.path ?? target;
  if (read.endsWith("/")) {
    return { why: `${target} is a read of a directory, whose listing the runner does not rebuild` };
  }
  const full = resolvedReadPath(read, { cwd: copy.cwd });
  const inside = full === undefined ? undefined : insideRoot(copy.root, full);
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
  const shown = shownPath(read, full, { cwd: copy.cwd });
  if (ranged?.range !== undefined) {
    const answer = pathRangeAnswer(copy.dir, {
      file: inside,
      target: read,
      shown,
      rows,
      ...ranged.range,
    });
    return typeof answer === "string" ? { why: `${target}: ${answer}` } : answer;
  }
  const answer = pathAnswer(copy.dir, {
    file: inside,
    target,
    source: full,
    shown,
    rows,
    checkoutRead: true,
  });
  if (typeof answer === "string") return { why: answer };
  const { text, isError, wholeText } = answer;
  return wholeText === true ? { text, isError, wholeText } : { text, isError };
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
    return insideRoot(real, resolved) === undefined;
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
      if (target !== undefined && insideRoot(real, target) !== undefined) continue;
      rmSync(path.join(dir, child), { force: true });
      removed.push(child);
    }
  };
  walk("");
  return removed.sort();
}

/** Every file and directory under `inside` (a directory of the checkout), each with its path
 *  inside the checkout and whether it is a directory, in path order; symbolic links, `.git` and
 *  `.jj` left out (the pin's walk skips `.git`, and a colocated `.jj` ignores all it holds). */
function entriesUnder(copy: CheckoutCopy, inside: string): { path: string; dir: boolean }[] {
  const found: { path: string; dir: boolean }[] = [];
  const walk = (at: string) => {
    for (const entry of readdirSync(path.join(copy.dir, at), { withFileTypes: true })) {
      if (entry.name === ".git" || entry.name === ".jj") continue;
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
    const lines = textLines(text);
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
    // One file past the page decides it: the rest of the checkout is not read.
    if (isDirectory && sections.length > GREP_FILE_LIMIT) {
      return { why: `matches in more than the ${GREP_FILE_LIMIT} files one page of grep shows` };
    }
  }
  if (sections.length === 0) return { text: "No matches found", isError: false };
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

/** Whether `entry` holds a `,`, a `;` or a blank outside braces, where the pin may read it as a
 *  list of paths (path-utils.ts's `hasTopLevelPathDelimiter`). */
function hasListDelimiter(entry: string): boolean {
  let depth = 0;
  for (let at = 0; at < entry.length; at++) {
    const ch = entry[at] ?? "";
    if (ch === "\\") at++;
    else if (ch === "{") depth++;
    else if (ch === "}") depth = Math.max(0, depth - 1);
    else if (depth === 0 && /[,;\s]/.test(ch)) return true;
  }
  return false;
}

/** The paths of the `glob` path `entry` (trimmed, its outer double quotes taken off), as the pin
 *  reads them (glob.ts lines 212-225, path-utils.ts's `splitDelimitedPathEntry`): split at each
 *  `;` outside braces, each part trimmed and the empty ones left out, unless the whole is a path
 *  of the checkout; `[entry]` when it holds no list delimiter; why not answered for a list the
 *  pin splits at commas or blanks only where its paths are there. */
function globEntries(copy: CheckoutCopy, entry: string): string[] | Unanswered {
  if (!hasListDelimiter(entry)) return [entry];
  const whole = searchTarget(copy, entry);
  if (!("why" in whole) && statSync(path.join(copy.dir, whole.inside), { throwIfNoEntry: false })) {
    return [entry];
  }
  if (!entry.includes(";")) return { why: `its path ${entry} is a list of commas or blanks` };
  const parts: string[] = [];
  let depth = 0;
  let start = 0;
  for (let at = 0; at <= entry.length; at++) {
    const ch = entry[at];
    if (ch === "\\") at++;
    else if (ch === "{") depth++;
    else if (ch === "}") depth = Math.max(0, depth - 1);
    else if (ch === undefined || (depth === 0 && ch === ";")) {
      const part = entry.slice(start, at).trim();
      const unquoted =
        part.length > 1 && part.startsWith('"') && part.endsWith('"') ? part.slice(1, -1) : part;
      if (unquoted !== "") parts.push(unquoted);
      start = at + 1;
    }
  }
  return parts.length === 0 ? { why: `its path ${entry} names no pattern` } : parts;
}

/** The `glob` call's answer (see the file's header). */
function globAnswer(
  copy: CheckoutCopy,
  input: Record<string, unknown>
): CheckoutAnswer | Unanswered {
  const other = otherKeys(input, ["path", "hidden", "gitignore", "limit"]);
  if (other !== undefined) return { why: other };
  const given = input.path ?? ".";
  if (typeof given !== "string") return { why: "its path is no text" };
  const trimmed = given.trim();
  const entry =
    trimmed.length > 1 && trimmed.startsWith('"') && trimmed.endsWith('"')
      ? trimmed.slice(1, -1)
      : trimmed;
  if (entry === "") return { why: "it names no pattern" };
  const entries = globEntries(copy, entry);
  if ("why" in entries) return entries;
  const requested = input.limit ?? GLOB_LIMIT;
  if (typeof requested !== "number" || !Number.isFinite(requested) || requested <= 0) {
    return { why: `its limit ${String(requested)} is no positive number` };
  }
  const limit = Math.min(GLOB_LIMIT, Math.max(1, Math.floor(requested)));
  // Each pattern's base, inside the checkout, and the glob under it (path-utils.ts's
  // `parseFindPattern`): a path with no glob character is its own base, every entry under it.
  const targets: { pattern: string; inside: string; glob: string; hasGlob: boolean }[] = [];
  for (const pattern of entries.map((each) => each.replace(/\\/g, "/"))) {
    if (/^\/+$/.test(pattern)) return { why: "it searches from /, which the pin refuses" };
    const segments = pattern.split("/");
    const first = segments.findIndex((segment) => /[*?[{]/.test(segment));
    const base = first < 0 ? pattern : segments.slice(0, first).join("/");
    let glob = first < 0 ? "**/*" : segments.slice(first).join("/");
    if (first === 0 && !glob.startsWith("**/")) glob = `**/${glob}`;
    const where = searchTarget(copy, base === "" ? "." : base);
    if ("why" in where) return where;
    if (leavesCopy(copy, where.inside)) {
      return { why: `its base ${base} resolves, through a symbolic link, outside the checkout` };
    }
    targets.push({ pattern, inside: where.inside, glob, hasGlob: first >= 0 });
  }
  const statOf = (inside: string) =>
    statSync(path.join(copy.dir, inside), { throwIfNoEntry: false });
  // In a list, a path whose base is not there is skipped and noted (glob.ts lines 236-248).
  const missing =
    targets.length > 1 ? targets.filter((each) => statOf(each.inside) === undefined) : [];
  const found = new Set<string>();
  const unique = new Set(
    targets.filter((each) => !missing.includes(each)).map((each) => each.pattern)
  );
  for (const target of targets) {
    if (missing.includes(target)) continue;
    const stats = statOf(target.inside);
    if (unique.size <= 1 && !stats?.isDirectory() && (target.hasGlob || !stats?.isFile())) {
      // The pin's error, `Path not found` or `Path is not a directory`, is not rebuilt.
      return {
        why: `its path ${target.pattern} is not found as a directory or a file, and the pin's error for it is not rebuilt`,
      };
    }
    const full = target.inside === "" ? copy.root : `${copy.root}/${target.inside}`;
    if (!target.hasGlob && stats?.isFile()) {
      found.add(relativeToCwd(copy, full));
      continue;
    }
    if (!stats?.isDirectory()) continue;
    const glob = new Bun.Glob(target.glob.replace(/\/+$/, ""));
    const directoryOnly = target.glob.endsWith("/");
    let matched = 0;
    for (const each of entriesUnder(copy, target.inside)) {
      const below = target.inside === "" ? each.path : each.path.slice(target.inside.length + 1);
      if (input.hidden === false && below.split("/").some((part) => part.startsWith("."))) continue;
      if ((directoryOnly && !each.dir) || !glob.match(below)) continue;
      matched++;
      found.add(`${relativeToCwd(copy, `${copy.root}/${each.path}`)}${each.dir ? "/" : ""}`);
    }
    if (matched > limit) return { why: `it finds more than its limit of ${limit} paths` };
  }
  if (missing.length === targets.length) {
    return {
      why: `its paths ${entries.join(", ")} are not found, and the pin's error for them is not rebuilt`,
    };
  }
  const notes = [
    ...(found.size > 0 && requested > GLOB_LIMIT
      ? [`Requested limit ${requested} clamped to the max of ${GLOB_LIMIT}`]
      : []),
    ...(missing.length > 0
      ? [`Skipped missing paths: ${missing.map((each) => each.pattern).join(", ")}`]
      : []),
  ];
  if (found.size === 0) {
    return { text: ["No files found matching pattern", ...notes].join("\n"), isError: false };
  }
  if (found.size > limit) return { why: `it finds more than its limit of ${limit} paths` };
  const lines = pathTreeEvents([...found].sort()).map((event) =>
    event.kind === "dir" ? `${"#".repeat(event.depth + 1)} ${event.name}/` : event.name
  );
  const text = lines.join("\n");
  return { text: notes.length === 0 ? text : `${text}\n\n${notes.join("\n")}`, isError: false };
}

/** The path an option word `word` names, as `escapes` reads it: a word that is no option is its
 *  own path; an option's is its value after its `=`, else from its first `/` or `~`, else none
 *  (""). */
function optionValue(word: string): string {
  if (!word.startsWith("-")) return word;
  const equals = word.indexOf("=");
  if (equals >= 0) return word.slice(equals + 1);
  const slash = word.search(/[/~]/);
  return slash < 0 ? "" : word.slice(slash);
}

/** Why the simple command `command` at `state` may reach a file outside the session's checkout or
 *  show a value of the runner's in place of the session's, or undefined (see `escapes`). */
function escapeFault(
  copy: CheckoutCopy,
  command: SimpleCommand,
  state: ShellState,
  walk: Walk
): string | undefined {
  const [first, ...args] = command.words;
  const words = [
    ...command.words,
    ...command.assignments.map((each) => each.value),
    ...command.redirects.map((each) => each.target),
  ];
  for (const word of words) {
    for (const part of word) {
      if (part.kind === "var" && !state.vars.has(part.name) && part.name !== "PWD") {
        return `${wordText(word)} expands $${part.name}, whose value is the runner's, not the session's`;
      }
    }
  }
  const program = first === undefined ? undefined : literal(first);
  const reaches = program !== undefined && !Object.hasOwn(NO_PATH_PROGRAMS, program);
  for (const word of [...(reaches ? args : []), ...command.redirects.map((each) => each.target)]) {
    const text = wordText(word);
    if (/\{[^}]*(,|\.\.)[^}]*\}/.test(text)) {
      return `${text} expands, and the runner does not check where its expansion leads`;
    }
    const values = valuesOf(word, state, walk);
    if (values === undefined) {
      return `${text} expands to a value the runner cannot place inside the session's checkout`;
    }
    for (const each of values) {
      const value = optionValue(each);
      if (value === "" || value === "/dev/null") continue;
      if (value.startsWith("~")) return `${text} is under ~, outside the session's checkout`;
      if (value.split("/").includes(".git")) return `${text} names .git`;
      const fulls = value.startsWith("/")
        ? [path.posix.normalize(value)]
        : state.cwds.map((at) => path.posix.resolve(at, value));
      if (fulls.some((full) => insideRoot(copy.root, full) === undefined)) {
        return `${text} is outside the session's checkout ${copy.root}`;
      }
    }
  }
  return undefined;
}

/** Why `script`, run from the working directory `cwd`, may reach a file outside the session's
 *  checkout or show a value of the runner's, or undefined. Each word a program may open (every
 *  word after the program but `pwd`'s, `true`'s, `echo`'s, `printf`'s, `env`'s, `unset`'s and
 *  `export`'s, and every redirection's target) is read at each value it may take (`walkScript`:
 *  a variable the command assigned, `$PWD` as each directory a `cd` before it may have moved to,
 *  `$(pwd)`, `$(git rev-parse --show-toplevel)` and, in a colocated copy, `$(jj root)`): one
 *  whose value the runner cannot know, or that holds a brace expansion, is not followed; nor is
 *  one under `~`, one naming `.git` (whose config names the checkout's source and whose files are
 *  the runner's), an absolute one not under the root, a relative one that leaves the root from
 *  any of those directories, or an option's value (after its `=`, or from its first `/` or `~`
 *  where it has none) that does so. Any other variable's expansion (`$HOME`, `$PATH`) would show
 *  the runner's value. Symbolic links are not followed here: the copy holds none that leaves it
 *  (`removeOutsideLinks`). */
function escapes(copy: CheckoutCopy, cwd: string, script: Script): string | undefined {
  const walk: Walk = {
    check: (command, state) => escapeFault(copy, command, state, walk),
    subValues: (sub, state) => {
      const [pipelines, ...more] = sub;
      const [commands, ...others] = pipelines ?? [];
      const [command, ...rest] = commands ?? [];
      if (more.length + others.length + rest.length > 0 || command?.kind !== "simple") {
        return undefined;
      }
      if (command.assignments.length + command.redirects.length > 0) return undefined;
      const words = command.words.map(literal);
      if (words.includes(undefined)) return undefined;
      switch (words.join(" ")) {
        case "pwd":
          return state.cwds.length > 0 ? new Set(state.cwds) : undefined;
        case "git rev-parse --show-toplevel":
          return copy.git ? new Set([copy.root]) : undefined;
        case "jj root":
          return copy.jj === true ? new Set([copy.root]) : undefined;
        default:
          return undefined;
      }
    },
  };
  return walkScript(script, { vars: new Map(), cwds: [cwd] }, false, walk);
}

/** The `bash` call's answer (see the file's header). */
async function bashAnswer(
  copy: CheckoutCopy,
  input: Record<string, unknown>
): Promise<CheckoutAnswer | Unanswered> {
  const other = otherKeys(input, ["command", "cwd", "timeout"]);
  if (other !== undefined) return { why: other };
  const command = input.command;
  const script = readOnlyScript(command);
  if (typeof command !== "string" || script === undefined) {
    return { why: "its command is not read-only" };
  }
  const programs = simpleCommands(script).map((each) => literal(each.words[0] ?? []) ?? "");
  if (!copy.git && programs.some((program) => program === "git" || program === "jj")) {
    return {
      why: "the copy is GitHub's tarball of the commit, which holds no history: the fetch of the commit failed",
    };
  }
  const jj = Bun.which("jj") ?? undefined;
  if (programs.includes("jj")) {
    if (copy.jj !== undefined && copy.jj !== true) {
      return {
        why: `the copy could not be made a colocated jj repository, so no jj command is answered: ${copy.jj.why}`,
      };
    }
    if (jj === undefined) return { why: "jj is not on the runner's PATH" };
  }
  let cwd = copy.cwd;
  if (typeof input.cwd === "string") {
    const resolved = resolvedReadPath(input.cwd, { cwd: copy.cwd });
    if (resolved === undefined || insideRoot(copy.root, resolved) === undefined) {
      return { why: `its working directory ${input.cwd} is outside the session's checkout` };
    }
    cwd = resolved;
  }
  const leads = escapes(copy, cwd, script);
  if (leads !== undefined) return { why: leads };
  const real = realpathSync(copy.dir);
  const localCwd = path.join(real, insideRoot(copy.root, cwd) ?? "");
  if (!statSync(localCwd, { throwIfNoEntry: false })?.isDirectory()) {
    return { why: `its working directory ${cwd} is no directory of the checkout` };
  }
  const mapped = command.split(copy.root).join(real);
  const started = performance.now();
  const child = Bun.spawn(["bash", "--noprofile", "--norc", "-c", `{ ${mapped}\n} 2>&1`], {
    cwd: localCwd,
    env: shellEnvironment(jj),
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
  // The copy's path, as its real path and as given, becomes the session's checkout root.
  const own = Buffer.concat(chunks).toString("utf8").split(real).join(copy.root);
  const output = copy.dir === real ? own : own.split(copy.dir).join(copy.root);
  if (output.split("\n").some((line) => line.length > BASH_COLUMNS)) {
    return { why: `a line of its output passes the ${BASH_COLUMNS} characters Oh My Pi shows` };
  }
  if (
    !hasPipe(script) &&
    output.length >= MINIMIZE_CHARS &&
    programs.some((program) => Object.hasOwn(MINIMIZED_PROGRAMS, program))
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

/** The answer to the `read`, `grep` or `glob` call `name` with `fields` from `copy` (see the
 *  file's header), its reads by path shown as `rows`, or why it is not answered. */
function fileAnswer(
  copy: CheckoutCopy,
  name: "read" | "grep" | "glob",
  fields: Record<string, unknown>,
  rows: PathRows
): CheckoutAnswer | Unanswered {
  switch (name) {
    case "read":
      return readAnswer(copy, fields, rows);
    case "grep":
      return grepAnswer(copy, fields, rows);
    case "glob":
      return globAnswer(copy, fields);
  }
}

/** The answer to the read-only call `name` with `input` from `copy` (see the file's header), its
 *  reads by path shown as `rows`, or why it is not answered. A `read`, `grep` or `glob` answer is
 *  kept in `copy.answers` and given again to the same call. */
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
  if (name === "read" || name === "grep" || name === "glob") {
    const key = `${name}\0${rows}\0${JSON.stringify(fields)}`;
    const held = copy.answers?.get(key);
    if (held !== undefined) return held;
    const answer = fileAnswer(copy, name, fields, rows);
    copy.answers?.set(key, answer);
    return answer;
  }
  if (name === "bash") return bashAnswer(copy, fields);
  return { why: `no capture declares a ${name} tool, so the runner rebuilds none of its forms` };
}
