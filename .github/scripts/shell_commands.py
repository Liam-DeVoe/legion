"""Just enough shell to find each simple command's first word in a workflow step's `run:`, and the
pipeline each command stands in.

check-release-credentials.sh imports it from the repository it checks, running
`PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=.github/scripts python3` from that repository's root, and
classifies each command commands_of returns. check-ci-filters.sh's rule 22
(ci_workflow_rules.check_network_installs) reads each pipeline pipelines_of returns, looked
through WRAPPERS, and the shell scripts of this repository a step runs, the same way.

Command position is found by one quote-aware scan of the `run:` (its `${{ … }}` expressions
elided first), as bash reads it: a `#` starts a comment only at word start — the start of the run,
after unquoted whitespace or a newline, or after an unquoted `;`, `&`, `|`, `(`, `)`, `<`, `>` or
backtick, not `{` (`${#x}`, `$#` and `a#b` are words) — and the comment runs to the real newline,
so a backslash inside it continues nothing; escaped whitespace and an escaped `#` are word
characters (`echo a\\ #` has no comment); a backslash-newline joins lines outside single quotes,
whatever the number of backslashes before it (an escaped backslash is a character: `printf 'x' \\\\`
ends its command, `printf 'x' \\\\\\` continues it, and `a\\` + newline + `#` glues `a#`); a quote
carries across newlines, and `;`, `&`, `|` and `#` inside one are literal; `$(…)`, `<(…)`, `(…)`
and backticks open a command of their own, inside a double-quoted string too, and the string
resumes at the close; an unquoted `;`, `&`, `|`, `(`, `)` or newline ends a command, and a
redirection's target (`>/dev/null`, `>> "$GITHUB_OUTPUT"`, `2>&1`) is not one of its words. A
here-document's body (`<<EOF`, `<<-'EOF'`, up to the line that is its delimiter) is text the
command reads, not commands, so it is skipped; a here-string (`<<<`) is a redirection like any
other.

Pipelines: a lone `|` (or `|&`) joins the command before it to the one after it, a newline right
after it included, as bash reads `a |` + newline + `b`; `||`, `&&`, `;`, `&`, a newline and the
brackets of a subshell or a substitution end the pipeline.

Each command sheds its leading `VAR=value` assignments and the keywords that precede a command (if,
then, for, do, `{`, `}`, …), and env, time and exec with their options (`env -u CI A=b x` runs
`x`); the first word left is the command. An interpreter there (one of INTERPRETERS: bash, sh,
dash, zsh, source, `.`, by its name at any path) is looked through, past its flags and the option
name an `-o`, `+o`, `-O` or `+O` takes, to the word after it (`bash -o pipefail scripts/x.sh` runs
`scripts/x.sh`), and the script string behind its `-c`, like the words behind `eval`, is scanned
the same way (`sh -c 'bun install'`, `eval "bun install"` and `eval bun install` run `bun`). An
interpreter handed no script, or `-s`, reads its script from its standard input, and stands as its
own name alone (`curl … | sudo bash -s` is the pipeline `curl …` then `bash`).

WRAPPERS, the commands that run the command after their own options (sudo, timeout, nice, …),
are looked through only by a caller that asks (`through=WRAPPERS`): check-release-credentials.sh
names `sudo` itself as what it refuses, so it reads the wrapper's word. Looking through is one
loop: assignments, keywords, a wrapper, an interpreter or `eval` are each taken off the front until
none is left, so `sudo DEBIAN_FRONTEND=noninteractive bash -c 'timeout 60 apt-get update'` runs
`apt-get update`.
"""

import re

EXPRESSION = re.compile(r"\$\{\{.*?\}\}")
ASSIGNMENT = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*\+?=")
SKIP_WORDS = {
    "if", "then", "else", "elif", "fi", "do", "done", "while", "until", "for", "in",
    "!", "{", "}", "case", "esac",
}
# Looked through for every caller, as the keywords above are, with their options: each one's
# options that take a value of their own. env's `-S` splits its value into the words it runs.
KEYWORD_COMMANDS = {
    "env": {"-u", "--unset", "-C", "--chdir", "-S", "--split-string"},
    "time": set(),
    "exec": {"-a"},
}
# Transparent in command position: the command is the word after the interpreter and its flags (an
# `-o`, `+o`, `-O` or `+O`, alone or ending a cluster such as `-eo`, takes the next word as the
# option's name), or each command of the script string after its `-c`. `eval` runs the words after
# it as such a script string.
INTERPRETERS = {"bash", "sh", "dash", "zsh", "source", "."}
# The interpreters that read a script from their standard input when handed none.
STDIN_SHELLS = {"bash", "sh", "dash", "zsh"}
# The commands that run the command after their own options, each with its options that take a
# value of their own: `sudo -u root -E apt-get update` runs apt-get, and so do
# `timeout --kill-after=5 60 apt-get update` (timeout's duration is the word after its options)
# and `xargs -n1 docker pull`. A `NAME=value` after sudo's options is an assignment and is shed
# as one. `command -v x` and `command -V x` look x up and run nothing.
WRAPPERS = {
    "sudo": {"-u", "--user", "-g", "--group", "-C", "--close-from", "-D", "--chdir", "-h", "--host",
             "-p", "--prompt", "-r", "--role", "-t", "--type", "-U", "--other-user", "-T",
             "--command-timeout"},
    "timeout": {"-s", "--signal", "-k", "--kill-after"},
    "nice": {"-n", "--adjustment"},
    "ionice": {"-c", "--class", "-n", "--classdata", "-p", "--pid", "-P", "--pgid", "-u", "--uid"},
    "nohup": set(),
    "setsid": set(),
    "stdbuf": {"-i", "--input", "-o", "--output", "-e", "--error"},
    "xargs": {"-a", "--arg-file", "-d", "--delimiter", "-E", "-I", "-L", "--max-lines", "-n",
              "--max-args", "-P", "--max-procs", "-s", "--max-chars"},
    "command": set(),
}


class Scanner:
    """One quote-aware pass over a `run:`, as bash reads it (the module's rules), collecting each
    pipeline's simple commands. Between characters it holds the word being read, the words of the
    command being read, the commands of the pipeline being read, whether the last command ended at
    a pipe (so a newline continues the pipeline), whether the next character begins a word (where
    a `#` starts a comment), whether the next word is a redirection's target (dropped: `>/dev/null`
    names no command) and the delimiters of the here-documents whose bodies start at the next
    newline."""

    def __init__(self, through: dict | None) -> None:
        self.through = through or {}
        self.pipelines: list[list[list[str]]] = []
        self.pipeline: list[list[str]] = []
        self.piped = False
        self.words: list[str] = []
        self.word = ""
        self.started = False
        self.at_word_start = True
        self.redirect_target = False
        self.heredoc_next: bool | None = None
        self.heredocs: list[tuple[str, bool]] = []

    def add(self, char: str) -> None:
        self.word += char
        self.started = True
        self.at_word_start = False

    def end_word(self) -> None:
        if self.started:
            if not self.redirect_target:
                self.words.append(self.word)
            elif self.heredoc_next is not None:
                self.heredocs.append((self.word, self.heredoc_next))
                self.heredoc_next = None
            self.redirect_target = False
        self.word = ""
        self.started = False
        self.at_word_start = True

    def end_command(self, separator: str = "list") -> None:
        """Ends the command being read at a `separator`: a `pipe` keeps its pipeline open, a
        `newline` keeps it open right after a pipe, and anything else (`list`) ends it."""
        self.end_word()
        self.redirect_target = False
        if self.words:
            self.pipeline.append(self.words)
            self.words = []
            self.piped = separator == "pipe"
            if self.piped:
                return
        elif separator == "pipe" or (separator == "newline" and self.piped):
            return
        self.piped = False
        self.end_pipeline()

    def end_pipeline(self) -> None:
        """Each command of the pipeline read, reduced by command_of. A command that reduces to one
        pipeline (a plain command, or a `-c` string of one) stands in this one; a `-c` string or an
        `eval` of several stands apart, as its own pipelines."""
        joined: list[list[str]] = []
        for words in self.pipeline:
            reduced = command_of(words, self.through)
            if len(reduced) == 1:
                joined += reduced[0]
            else:
                self.pipelines += reduced
        if joined:
            self.pipelines.append(joined)
        self.pipeline = []

    def skip_heredocs(self, text: str, i: int) -> int:
        """The index of the newline that ends the last here-document body pending at the newline at
        `i`: each body runs to the line that is its delimiter (leading tabs aside, for `<<-`)."""
        position = i + 1
        for delimiter, strip_tabs in self.heredocs:
            while position < len(text):
                end = text.find("\n", position)
                line = text[position:] if end < 0 else text[position:end]
                position = len(text) if end < 0 else end + 1
                if (line.lstrip("\t") if strip_tabs else line) == delimiter:
                    break
        self.heredocs = []
        return position - 1

    def scan(self, text: str) -> list[list[list[str]]]:
        quote = None
        # The open `$(`, `<(`, `(` and backticks, innermost last, each with the quote it interrupted:
        # inside, an unquoted command of its own starts; at the close, the string resumes.
        open_subs: list[tuple[str, str | None]] = []
        i = 0
        while i < len(text):
            char = text[i]
            after = text[i + 1:i + 2]
            if quote == "'":
                if char == "'":
                    quote = None
                else:
                    self.add(char)
            elif char == "`":
                self.end_command()
                if open_subs and open_subs[-1][0] == "tick":
                    quote = open_subs.pop()[1]
                else:
                    open_subs.append(("tick", quote))
                    quote = None
            elif char == "$" and after == "(":
                self.add(char)  # the `$` and what precedes it stay with the outer word
                self.end_command()
                open_subs.append(("paren", quote))
                quote = None
                i += 1
            elif quote == '"':
                if char == '"':
                    quote = None
                elif char == "\\" and after in ("$", "`", '"', "\\"):
                    self.add(after)
                    i += 1
                elif char == "\\" and after == "\n":
                    i += 1
                else:
                    self.add(char)  # any other `\x` keeps both characters
            elif char == "#" and self.at_word_start:
                end = text.find("\n", i)  # the newline stays, to end the command below
                i = len(text) if end < 0 else end
                continue
            elif char == "\\" and after == "\n":
                i += 1
            elif char == "\\" and after:
                self.add(after)
                i += 1
            elif char in "'\"":
                quote = char
                self.started = True
                self.at_word_start = False
            elif char == "(" or (char in "<>" and after == "("):
                if char != "(":
                    i += 1
                self.end_command()
                open_subs.append(("paren", None))
            elif char == ")":
                self.end_command()
                if open_subs and open_subs[-1][0] == "paren":
                    quote = open_subs.pop()[1]
            elif char in "<>":
                self.end_word()
                self.redirect_target = True
                operator = char
                while text[i + 1:i + 2] in ("<", ">", "&", "|"):  # `>>`, `2>&1`, `<<<`, `>|`
                    i += 1
                    operator += text[i]
                if operator == "<<":  # a here-document, whose target word is its delimiter
                    self.heredoc_next = text[i + 1:i + 2] == "-"
                    if self.heredoc_next:
                        i += 1
            elif char == "|" and after != "|":
                self.end_command("pipe")
                if after == "&":  # `|&` pipes standard error too
                    i += 1
            elif char in ";&|":
                if char in "&|" and after == char:  # `&&` and `||`
                    i += 1
                self.end_command()
            elif char == "\n":
                self.end_command("newline")
                if self.heredocs:
                    i = self.skip_heredocs(text, i)
            elif char in " \t":
                self.end_word()
            else:
                self.add(char)
            i += 1
        self.end_command()
        return self.pipelines


def pipelines_of(run: str, through: dict | None = None) -> list[list[list[str]]]:
    """Each pipeline of a `run:`, as its commands in order (`curl … | sh` is one of two), each
    command as its words with the command first. `through` names the wrappers to look through
    (WRAPPERS, or none)."""
    return Scanner(through).scan(EXPRESSION.sub("EXPR", run))


def commands_of(run: str, through: dict | None = None) -> list[list[str]]:
    """Each simple command of a `run:`, as its words with the command first."""
    return [command for pipeline in pipelines_of(run, through) for command in pipeline]


def past_options(words: list[str], takes_value: set) -> list[str]:
    """The words after a command's leading options, each option's own value taken with it; `--`
    ends the options and is taken too."""
    while words and words[0].startswith("-") and words[0] != "-":
        option = words.pop(0)
        if option == "--":
            break
        if option in takes_value and words:
            words.pop(0)
    return words


def command_of(words: list[str], through: dict | None = None) -> list[list[list[str]]]:
    """The pipelines the words run, after the assignments, keywords and (when asked) wrappers
    before them: the words from the command on, as one pipeline of one command; or, when the
    command is an interpreter, the command it is handed (`bash -o pipefail scripts/x.sh` runs
    `scripts/x.sh`), the pipelines of the script string behind its `-c` (`sh -c 'bun install && go
    build'` runs `bun install` and `go build`), or, with no script, the interpreter's name alone,
    which reads its script from standard input; or, when it is `eval`, the pipelines of the words
    after it, joined (`eval "bun install"` and `eval bun install` both run `bun install`)."""
    through = through or {}
    while True:
        while words and (ASSIGNMENT.match(words[0]) or words[0] in SKIP_WORDS):
            words.pop(0)
        if not words:
            return []
        name = words[0].rsplit("/", 1)[-1]
        if name in KEYWORD_COMMANDS:
            words.pop(0)
            if name == "env" and words and words[0] in ("-S", "--split-string") and len(words) > 1:
                words = words[1].split() + words[2:]
            words = past_options(words, KEYWORD_COMMANDS[name])
            continue
        if name == "eval":
            return pipelines_of(" ".join(words[1:]), through)
        if name in INTERPRETERS:
            words.pop(0)
            from_stdin = False
            while words and words[0].startswith(("-", "+")):
                flag = words.pop(0)
                if flag in ("-", "--"):
                    break
                if flag.startswith("--"):
                    continue
                if "c" in flag[1:]:
                    return pipelines_of(words[0], through) if words else []
                if "s" in flag[1:] and flag.startswith("-"):
                    from_stdin = True
                if flag.endswith(("o", "O")) and words:
                    words.pop(0)
            if from_stdin or not words:
                return [[[name]]] if name in STDIN_SHELLS else []
            continue
        if name in through:
            words.pop(0)
            if name == "command" and words and words[0] in ("-v", "-V"):
                return []
            words = past_options(words, through[name])
            if name == "timeout" and words:
                words.pop(0)  # its duration
            continue
        return [[words]]
