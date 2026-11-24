#!/usr/bin/env python3
"""Holds each citation under .github/ to the file it names, and refuses a citation of any other
shape there.

A citation names what it means inside a file, never a line of it: a line number names whatever
line a later edit moves there, and nothing reads a citation of a file the citing change does not
touch (.github/ci-notes/ci.md#a-citation-names-a-symbol-never-a-line). So a citation is one
exact token: a backtick, the path of a tracked file from the repository root as git ls-files
prints it (no ./ and no leading /), a hash, a name, a backtick. This docstring's own two are
`.github/workflows/ci.yaml#browser_image` and `.github/scripts/ci_citations.py#held`. Several
names are several tokens, each with its path.

What this check claims, and all it claims:
- A token whose path is no tracked file fails. A token whose name is an identifier (a letter or
  an underscore, then letters, digits, underscores and dots) fails unless the file's code holds
  it as a whole word with the file's comments removed, so a renamed symbol whose old name lives
  on in a comment still fails. A token with any other name fails unless the file holds it as
  text, not inside a longer word. A token whose name is a line number fails.
- A line number written beside a path word, in a spelling this check knows, fails: a colon, an
  L or a hash and the number right after the path, the path at the end of a line and the number
  at the start of the next, and the words line or lines with a number beside the path (before
  it, after it, in parentheses, after a possessive or a comma). In the same line, or the next
  one, so does a bare colon and number after an opening parenthesis, "at", "and" or a comma,
  and an l. or a hash and a number in parentheses, and the word line run into its number.
- A backticked text that is no token fails only where it stands beside a path word, in one of
  two forms: right after the path word, with nothing between them or a possessive, a comma, a
  colon or an opening parenthesis, on the path's line or wrapped onto the next; or followed by
  in, of or from and the path word. Either is the shape of a name written beside its path, a
  citation this check could not hold, so it is refused rather than passed unread: rewrite a
  citation as a token, and reword a text that is no citation so that it does not stand beside
  the path. Any other backticked text is read as prose and is not checked, so a citation
  written further from its path, as in "ci.yaml holds, among others, the `x` output", is
  neither held nor refused. That is the stated limit of this rule, which its test shows.
A path word is a word that is a tracked file's path, or the end of one after a slash that holds
a slash or a dot, or a capitalised file name such as CODEOWNERS or Dockerfile: browser.Dockerfile,
ci.yaml and scripts/build.ts are path words when a tracked file's path ends so.

The scope is every tracked text file under .github/, read as plain text: no comment, string,
heredoc or Markdown structure of the citing file is parsed, so a citation counts wherever it
stands. A tracked symlink is skipped, being another file's text, which is read at its own path
when that is under .github/; a file that is not UTF-8 is no text. Nothing outside .github/ is
read, held or refused. Measured on 2026-11-24: outside .github/ the tree held 1,320 strings of
the token's shape, tmux formats, owner/repo#n references, Markdown anchors, template literals and
HTML entities among them, and none a citation; not one names a tracked file by its root path,
and one names the end of one (a Markdown anchor in a TypeScript test). Under .github/ the 7
strings of that shape that were no citation were reworded, and every one left is a citation
token this check holds.

How the comments of a cited file are removed: in one pass from left to right that steps over
string literals, so neither a comment marker inside a string nor a quote inside a comment
starts anything. Python through its tokenize module; Go, TypeScript and JavaScript with '...',
"..." and backtick strings stepped over and line and block comments removed; CSS with its block
comments, outside '...' and "..."; Lean with its -- and /- -/ comments; go.mod and go.work
with their line comments; shell, YAML, TOML, jq, Dockerfiles, CODEOWNERS, ignore files and an
extension-free script that starts with #! with a hash comment at a line's start or after
whitespace, outside '...' and "..."; any other file, Markdown, JSON and lock files among them,
as it is written.

Usage: ci_citations.py   (no arguments)

It prints one problem a line, as file, line and message, and exits 1 when there is one, and
when the scan finds no token at all, which would mean it read nothing it could hold.
"""
import bisect
import io
import os
import re
import subprocess
import sys
import tokenize

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
SCOPE = ".github/"
BACKTICK = "\x60"


def token(path, name):
    """A citation token, built here so that this file's own text holds none it did not mean."""
    return BACKTICK + path + "#" + name + BACKTICK


# The token, with the backtick written as \x60 for the reason token() gives.
TOKEN = re.compile(r"\x60([^\x60\s#]+)#([^\x60\n]+)\x60")
IDENTIFIER = re.compile(r"^[A-Za-z_][\w.]*$")
LINE_NAME = re.compile(r"^L?\d+(?:\s*[-,:]\s*L?\d+)*$")
# A word that may be a path: path characters after none of them, behind any ./ ../ or /. A word
# inside a URL is never one, as each part of a URL's path follows a slash.
CANDIDATE = re.compile(r"(?<![\w.@/~$%+-])((?:\.\.?/)*/?)([\w@%+-][\w.@%+/-]*|\.[\w@%+-][\w.@%+/-]*)")
# A code span, which may wrap onto one more line as a comment or a paragraph wraps it.
CODE_SPAN = re.compile(r"(\x60+)([^\x60\n]+(?:\n[^\x60\n]+)?)\1(?!\x60)")
WRAP = re.compile(r"[ \t]*\n[ \t]*(?:#+|//+|\*|--)?[ \t]*")

# Whitespace that may wrap onto the next line, past the next line's comment marker.
GAP = r"(?:[ \t]+|[ \t]*\n[ \t]*(?:#+|//+|\*|--)?[ \t]*)"
GAP0 = f"(?:{GAP})?"
NUMBERS = rf"\d+(?:{GAP0}(?:-|–|to|and|,){GAP0}\d+)*\b"
# A line number right after a path word, which may be closed by a backtick or a quote.
AFTER_PATH = re.compile(
    rf"(?:[\x60\"]|'(?!s\b))?"
    rf"(?::L?\d|#L?\d|:[ \t]*\n[ \t]*(?:#+|//+|\*|--)?[ \t]*\d|{GAP}L\d+\b"
    rf"|(?:'s|’s)?,?{GAP0}\(?[ \t]*(?:(?:at|on|in){GAP})?[Ll]ines?{GAP}\d)")
# A line number written before a path word that starts where this match ends.
BEFORE_PATH = re.compile(rf"\b[Ll]ines?{GAP}{NUMBERS}{GAP}(?:of|in){GAP}(?:the{GAP})?[\x60\"']?$")
# Spellings that carry no path of their own, refused in a path word's line and the next.
NEAR_PATH = re.compile(r"\(l\.\s*\d+\)|\(#\d+\)|\bline\d+\b|(?:\(|\bat\s+|\band\s+|,\s*):\d+\b")
# What may stand between a path word, or the code span that is that word alone, and a backticked
# text right after it: a closing quote, a possessive, a comma, a colon or an opening parenthesis,
# on the same line or the next.
NAME_AFTER = re.compile(rf"(?:\"|'(?!s\b))?(?:'s|’s|,|:)?{GAP0}\(?[ \t]*")
# What may stand between a backticked text and the path word after it: in, of or from.
NAME_FIRST = re.compile(rf"{GAP}(?:in|of|from){GAP}(?:the{GAP})?[\"']?")

SLASH_KINDS = {"go", "ts", "tsx", "js", "mjs", "cjs"}
HASH_KINDS = {"sh", "bash", "yaml", "yml", "toml", "jq", "Dockerfile", "Makefile", "CODEOWNERS",
              "gitignore", "dockerignore", "shellcheckrc"}


class Repo:
    """The tracked files, and the files a path word names among them."""

    def __init__(self):
        out = subprocess.run(["git", "-C", ROOT, "ls-files", "-z"], capture_output=True, check=True).stdout
        self.files = sorted(f for f in out.decode("utf-8").split("\0") if f)
        self.exact = {f for f in self.files if os.path.isfile(os.path.join(ROOT, f))}
        self.named = {}
        for f in self.exact:
            parts = f.split("/")
            self.named.setdefault(f, set()).add(f)
            for i in range(1, len(parts)):
                tail = "/".join(parts[i:])
                # main or legion alone is an English word as often as a file name, so a bare
                # name is a path word only with a dot in it or a capital first letter.
                if "/" in tail or "." in tail or tail[:1].isupper():
                    self.named.setdefault(tail, set()).add(f)
        self.cache = {}

    def names(self, word):
        """The tracked files a word names: the file at that path, or those whose path ends so."""
        return self.named.get(word, set())

    def shown(self, word):
        """The root path of the one file a path word names, else the word as written."""
        found = self.names(word)
        return next(iter(found)) if len(found) == 1 else word

    def code(self, path):
        if path not in self.cache:
            try:
                with open(os.path.join(ROOT, path), encoding="utf-8") as f:
                    self.cache[path] = code_of(path, f.read())
            except (OSError, UnicodeDecodeError):
                self.cache[path] = None
        return self.cache[path]


# ---- comment removal: one pass from left to right that steps over string literals --------------

def kind_of(path, body):
    base = os.path.basename(path)
    if base == "Dockerfile" or base.endswith(".Dockerfile"):
        return "Dockerfile"
    if "." in base[1:]:
        return base.rsplit(".", 1)[1]
    if base.startswith("."):
        return base[1:]
    return "sh" if body.startswith("#!") else base


def string_end(body, i, multiline):
    """The index after the string literal whose quote is body[i]. A string that may not span
    lines ends at a newline it meets first, so a stray quote can hide no more than its line."""
    quote, j = body[i], i + 1
    while j < len(body):
        if body[j] == "\\":
            j += 2
        elif body[j] == quote:
            return j + 1
        elif body[j] == "\n" and not multiline:
            return j
        else:
            j += 1
    return len(body)


def strip_comments(body, line, block, quotes, multiline=""):
    """body without the comments a line marker and a block pair open, outside quotes. A block
    comment leaves its newlines, so a line of the code is where it was."""
    out, i, n = [], 0, len(body)
    while i < n:
        ch = body[i]
        if ch in quotes:
            j = string_end(body, i, ch in multiline)
            out.append(body[i:j])
            i = j
        elif line and body.startswith(line, i):
            j = body.find("\n", i)
            i = n if j < 0 else j
        elif block and body.startswith(block[0], i):
            j = body.find(block[1], i + len(block[0]))
            end = n if j < 0 else j + len(block[1])
            out.append("\n" * body.count("\n", i, end))
            i = end
        else:
            out.append(ch)
            i += 1
    return "".join(out)


def strip_lean(body):
    """Lean's -- comments and its /- -/ comments, which nest, outside "..." strings."""
    out, i, n, depth = [], 0, len(body), 0
    while i < n:
        if depth:
            if body.startswith("/-", i):
                depth, i = depth + 1, i + 2
            elif body.startswith("-/", i):
                depth, i = depth - 1, i + 2
            else:
                out.append("\n" if body[i] == "\n" else "")
                i += 1
        elif body[i] == '"':
            j = string_end(body, i, True)
            out.append(body[i:j])
            i = j
        elif body.startswith("--", i):
            j = body.find("\n", i)
            i = n if j < 0 else j
        elif body.startswith("/-", i):
            depth, i = 1, i + 2
        else:
            out.append(body[i])
            i += 1
    return "".join(out)


def strip_hash(body):
    """A # comment at a line's start or after whitespace, outside '...' and "..."."""
    out, i, n = [], 0, len(body)
    while i < n:
        ch = body[i]
        if ch in "'\"":
            j = string_end(body, i, False)
            out.append(body[i:j])
            i = j
        elif ch == "#" and (i == 0 or body[i - 1] in " \t\n"):
            j = body.find("\n", i)
            i = n if j < 0 else j
        else:
            out.append(ch)
            i += 1
    return "".join(out)


def strip_python(body):
    try:
        comments = [t for t in tokenize.generate_tokens(io.StringIO(body).readline) if t.type == tokenize.COMMENT]
    except (tokenize.TokenError, SyntaxError):
        return strip_hash(body)
    lines = body.split("\n")
    for t in reversed(comments):
        row, col = t.start
        lines[row - 1] = lines[row - 1][:col] + lines[row - 1][t.end[1]:]
    return "\n".join(lines)


def code_of(path, body):
    """A file's text without its comments, where its kind has a comment syntax."""
    kind = kind_of(path, body)
    if kind == "py":
        return strip_python(body)
    if kind in SLASH_KINDS:
        return strip_comments(body, "//", ("/*", "*/"), "'\"\x60", multiline="\x60")
    if kind == "css":
        return strip_comments(body, "", ("/*", "*/"), "'\"")
    if kind in ("mod", "work"):
        return strip_comments(body, "//", None, "\"\x60", multiline="\x60")
    if kind == "lean":
        return strip_lean(body)
    if kind in HASH_KINDS:
        return strip_hash(body)
    return body


def held(name, code):
    """Whether a file's code holds a name: an identifier as a whole word, other text not inside a
    longer word at either end that is a word character."""
    if code is None:
        return False
    pattern = re.escape(name)
    whole = IDENTIFIER.match(name) is not None
    if whole or re.match(r"\w", name):
        pattern = r"(?<!\w)" + pattern
    if whole or re.search(r"\w$", name):
        pattern += r"(?!\w)"
    return re.search(pattern, code) is not None


# ---- the scan -----------------------------------------------------------------------------------

def problems_in(body, repo, counted):
    """(line, message) for each problem of one citing file's text."""
    starts = [0] + [m.end() for m in re.finditer("\n", body)]

    def line(offset):
        return bisect.bisect_right(starts, offset)

    def line_end(number):
        return starts[number] - 1 if number < len(starts) else len(body)

    # Each token is held, then blanked, so the rules below read the text around the tokens.
    masked = list(body)
    for m in TOKEN.finditer(body):
        cited, name = m.group(1), m.group(2)
        masked[m.start():m.end()] = " " * (m.end() - m.start())
        counted["tokens"] += 1
        if cited not in repo.exact:
            yield line(m.start()), f"{BACKTICK}{cited}{BACKTICK} names no tracked file"
        elif LINE_NAME.match(name.strip()):
            yield line(m.start()), f"cites {BACKTICK}{cited}{BACKTICK} by a line number; write it as {token(cited, 'Name')}"
        elif not held(name, repo.code(cited)):
            yield line(m.start()), f"{BACKTICK}{cited}{BACKTICK} holds no {BACKTICK}{name}{BACKTICK}"
    text = "".join(masked)

    words = []
    for m in CANDIDATE.finditer(text):
        word = m.group(2).rstrip(".")
        if word and repo.names(word):
            words.append((m.start(), m.start(2) + len(word), word))

    refused = set()
    for start, end, word in words:
        shown = repo.shown(word)
        here = line(start)
        window = text[starts[here - 1]:line_end(min(here + 1, len(starts)))]
        if (AFTER_PATH.match(text, end) or BEFORE_PATH.search(text, max(0, start - 200), start)
                or NEAR_PATH.search(window)) and (here, shown) not in refused:
            refused.add((here, shown))
            yield here, (f"cites {BACKTICK}{shown}{BACKTICK} by a line number; "
                         f"write it as {token(shown, 'Name')}")

    # The code spans of the text, paired from left to right as Markdown pairs them, so a closing
    # backtick is never read as an opening one.
    spans = [(m.start(), m.end(), WRAP.sub(" ", m.group(2))) for m in CODE_SPAN.finditer(text)]
    opens = [s for s, _, _ in spans]
    for start, end, word in words:
        shown = repo.shown(word)
        # The word, or the code span that holds it and nothing else; a word inside a longer span,
        # such as a command, is no path a name is written beside.
        i = bisect.bisect_right(opens, start) - 1
        if i >= 0 and start < spans[i][1]:
            s, e, inner = spans[i]
            if not re.fullmatch(r"\s*['\"]?(?:\.\.?/)*/?" + re.escape(word) + r"\.?['\"]?\s*", inner):
                continue
            start, end = s, e
        j = bisect.bisect_left(opens, end)
        if j < len(spans) and NAME_AFTER.fullmatch(text, end, spans[j][0]) and spans[j][2].strip():
            inner = spans[j][2].strip()
            yield line(spans[j][0]), (f"{word} is followed by {BACKTICK}{inner}{BACKTICK}, which is no "
                                      f"citation token: write it as {token(shown, inner)}, or reword the "
                                      f"sentence so that the text does not follow the path")
        j = bisect.bisect_right(opens, start) - 1
        while j >= 0 and spans[j][1] > start:
            j -= 1
        if j >= 0 and NAME_FIRST.fullmatch(text, spans[j][1], start) and spans[j][2].strip():
            inner = spans[j][2].strip()
            yield line(spans[j][0]), (f"{BACKTICK}{inner}{BACKTICK} is followed by {word}, and is no "
                                      f"citation token: write it as {token(shown, inner)}, or reword "
                                      f"the sentence")


def scanned(repo):
    """Every tracked text file under .github/: no symlink, and UTF-8."""
    for path in repo.files:
        full = os.path.join(ROOT, path)
        if not path.startswith(SCOPE) or os.path.islink(full) or not os.path.isfile(full):
            continue
        try:
            with open(full, encoding="utf-8") as f:
                yield path, f.read()
        except UnicodeDecodeError:
            continue


def main(argv):
    if argv:
        print("ci_citations.py takes no arguments: it reads every tracked text file under .github/")
        return 2
    repo = Repo()
    counted = {"tokens": 0}
    files = holding = 0
    found = []
    for path, body in scanned(repo):
        files += 1
        before = counted["tokens"]
        found.extend(f"{path}:{number}: {message}" for number, message in sorted(problems_in(body, repo, counted)))
        holding += counted["tokens"] > before
    for problem in found:
        print(problem)
    if found:
        return 1
    if counted["tokens"] == 0:
        print(f"found no citation token in the {files} text files under {SCOPE}: a scan that finds none "
              "holds none, so this is a failure, not a pass")
        return 1
    print(f"{counted['tokens']} citation tokens in {holding} of the {files} text files under {SCOPE} "
          f"name text their files hold, and no citation there is of another shape")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
