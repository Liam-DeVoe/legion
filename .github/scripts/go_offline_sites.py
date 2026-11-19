"""Rule 1 of check-go-offline.sh: each place a file starts a go command, in each language, and
whether the command runs there with GOPROXY=off. A site is (0-based line, offline); which file is
read, and what a reason is, are the check's own; how a language is read is the source_*.py modules'.

Imported with PYTHONPATH naming the directory this file lives in. Its test is
check-go-offline.test.sh, with the cases it sources.
"""
import ast
import re

import source_scan as scan
from source_env import env_elements, go_cmd_env, ts_options_env
from source_python import python_bound, python_env_elements, python_scope, python_text, python_tree
from source_shell import ShWord, shell_c_script, shell_env_elements, shell_inner_scripts, shell_script
from source_statements import Source

SUBCOMMANDS = {"build", "run", "list", "install", "get", "mod", "test", "vet", "env", "generate", "work", "version"}
REASON = re.compile(r"\bgo-offline:\s*\S")
SETTING = re.compile(r"[A-Za-z_]\w*=")
# A file the reading below could find no site in is not read a statement at a time: its text holds
# none of what every site is spelled with. Go: an exec.Command, an exec.LookPath, or a "go" a
# filepath.Join ends in. TypeScript: a spawn or an exec of child_process or execa, Bun's $`...`, or
# a go a string ends in. Python: a go a string ends in, or a go command in a string. Shell: a go,
# a path ending /go or a variable followed by a subcommand, -C DIR between them.
_SUBCOMMAND = r"(?:" + "|".join(sorted(SUBCOMMANDS)) + r")\b"
_GAP = r"(?:\s|\\\n)+"
MAYBE = {
    "go": re.compile(r'exec\.(?:Command|LookPath)|"go"'),
    "ts": re.compile(r"\b(?:spawn|spawnSync|execFile\w*|execa\w*|exec|execSync|execAsync)\s*\(|\$\s*`|go[\"'`]"),
    "py": re.compile(r"go[\"']|\bgo\s+(?:-C\S*\s+\S+\s+)?" + _SUBCOMMAND),
    "sh": re.compile(r"(?:\bgo|/go|\$\{?\w+\}?)[\"'}]*" + _GAP + r"(?:-C(?:=\S*|" + _GAP + r"\S+)" + _GAP + r")?[\"']?"
                     + _SUBCOMMAND),
}


def offline(elements):
    """Whether the last element that can set GOPROXY sets it to off: a later GOPROXY, the inherited
    environment or one nothing here can read after it leaves the command online."""
    for element in reversed(elements):
        if element[0] != "set":
            return False
        if element[1] == "GOPROXY":
            return element[2] == "off"
    return False


def goish_text(text):
    """Whether a program's name, as text with each expansion as \\x00, is the go binary: go, or a
    path ending /go (`/usr/local/go/bin/go`, `$GOROOT/bin/go`)."""
    return text == "go" or bool(re.fullmatch(r"[\w.\x00/~@+-]*/go", text))


def go_subcommand(values, k):
    """Whether the words after a go at k are an optional -C DIR and a subcommand that loads a module."""
    rest = values[k + 1:]
    j = 2 if rest[:1] == ["-C"] else 1 if rest and rest[0].startswith("-C=") else 0
    return j < len(rest) and rest[j] in SUBCOMMANDS


def env_words(values):
    """(index of the program after `env` and its settings, the settings) of an argv that starts with
    env, else (0, [])."""
    if not values or values[0] is None or values[0].rsplit("/", 1)[-1] != "env":
        return 0, []
    k, settings = 1, []
    while k < len(values) and values[k] is not None and (values[k].startswith("-") or SETTING.match(values[k])):
        if SETTING.match(values[k]):
            settings.append(("set", *values[k].split("=", 1)))
        elif values[k] in ("-u", "--unset") and k + 1 < len(values):
            settings.append(("set", values[k + 1], None))
            k += 1
        elif values[k] in ("-i", "-"):
            settings.append(scan.CLEAR)
        k += 1
    return k, settings


# --- Shell: each simple command, its own settings, and the shell it hands more shell to.

def shell_sites(text, line=0, base=(), depth=0):
    """(line, offline) of each go command a shell text starts. Its environment: base (what a
    command that ran this text gave it), each `export` or `unset` of GOPROXY before it at the top
    level or in the function or subshell that holds it, its own NAME=value prefix and the settings
    of an `env` before the go."""
    commands = shell_script(text, line)
    go_names, exports, assigned = set(), [], {}
    for order, command in enumerate(commands):
        values = command.values()
        settings = list(command.assigns) if not values else []
        if values and values[0] in ("local", "declare", "typeset", "readonly", "export"):
            for word in command.words[1:]:
                name, eq, _ = word.value().partition("=")
                if eq and re.fullmatch(r"[A-Za-z_]\w*", name):
                    rest = list(word.parts)
                    rest[0] = ("lit", rest[0][1].partition("=")[2], None) if rest and rest[0][0] == "lit" else rest[0]
                    settings.append((name, ShWord(rest, word.start, word.line)))
        for name, value in settings:
            assigned[name] = value
            # go-offline: this check's own reading compares a word with "go" here; it starts nothing.
            inner = [c for kind, _, nested in value.parts if kind == "cmd" for c in nested or ()]
            if goish_text(value.value()) or any(c.values()[:1] in (["which"], ["command"], ["type"])
                                                 and c.values()[-1:] == ["go"] for c in inner):
                go_names.add(name)
        if values and values[0] in ("export", "unset") or values[:2] in (["declare", "-x"], ["export", "-n"]):
            unset = values[0] == "unset" or values[1:2] == ["-n"]
            for word in command.words[1:]:
                name, eq, value = word.value().partition("=")
                if name == "GOPROXY":
                    if unset:
                        element = ("set", name, None)
                    elif eq:
                        element = ("set", name, value if word.static else None)
                    else:  # GOPROXY=off; export GOPROXY
                        held = assigned.get(name)
                        element = ("set", name, held.value() if held is not None and held.static else None)
                    exports.append((order, command.frames, element))
    found = []
    for order, command in enumerate(commands):
        values = command.values()
        scope = [e for at, frames, e in exports if at < order and command.frames[:len(frames)] == frames]
        for k, word in enumerate(command.words):
            named = len(word.parts) == 1 and word.parts[0][0] == "param" and word.parts[0][1] in go_names
            if (named or goish_text(word.value())) and go_subcommand(values, k):
                found.append((word.line, offline([*base, *scope, *shell_env_elements(command, k)])))
        if depth < 3:
            for inner, inner_line in shell_inner_scripts(command):
                own = shell_env_elements(command, len(command.words))
                found += shell_sites(inner, inner_line, (*base, *scope, *own), depth + 1)
    return found


# --- Go: each exec.Command of the go binary, and the Env of the *exec.Cmd it makes.

def go_sites(src):
    consumed, found = set(), []

    def goish(a, b, depth=0):
        text = src.string(a, b)
        if text is not None:
            return goish_text(text)
        call = src.call_of(a, b)
        if call:
            if (call.callee == "exec.LookPath" and len(call.args) == 1 and src.string(*call.args[0]) == "go") or \
                    (call.name == "Join" and call.args and src.string(*call.args[-1]) == "go"):
                consumed.add(call.at)
                return True
            return False
        # go-offline: this check's own reading compares a string with "/go" here; it starts nothing.
        parts = src.split(a, b, "+")
        if len(parts) > 1:
            last = src.string(*parts[-1])
            return last is not None and last.endswith("/go")
        name = src.name(a, b)
        if name and depth < 4:
            return any([goish(*r, depth + 1) for r in src.bound_all(name, a)])
        return False

    def text_of(a, b):
        return "".join(src.string(*p) or "\x00" for p in src.split(a, b, "+"))

    for call in src.calls():
        first = {"exec.Command": 0, "exec.CommandContext": 1}.get(call.callee)
        if first is None or len(call.args) <= first:
            continue
        argv = call.args[first:]
        values = [src.string(*r) for r in argv]
        k, settings = env_words(values)
        env = go_cmd_env(src, call)
        env = [scan.INHERIT] if env is None else env
        if k < len(argv) and goish(*argv[k]):
            found.append((src.line(call.at), offline(env + settings)))
            continue
        script = shell_c_script(values)
        if script is not None:
            found += shell_sites(text_of(*argv[script]), src.line(argv[script][0]), env)
    for call in src.calls():
        if call.at in consumed:
            continue
        if (call.callee == "exec.LookPath" and len(call.args) == 1 and src.string(*call.args[0]) == "go") or \
                (call.name == "Join" and len(call.args) >= 2 and src.string(*call.args[-1]) == "go"
                 and src.string(*call.args[-2]) == "bin"):
            found.append((src.line(call.at), False))  # resolves the go binary something else starts
    return found


# --- TypeScript: each spawn of the go binary, Bun's $ and exec of a shell, and an argv array.

SPAWNS = {"spawn", "spawnSync", "execFile", "execFileSync", "execFileAsync", "execa", "execaSync"}
SHELL_CALLS = {"exec", "execSync", "execAsync"}


def ts_sites(src):
    found, handled = [], set()

    def strip_bang(a, b):
        return (a, b - 1) if b - a > 1 and src.punct(b - 1, "!") else (a, b)

    def goish(a, b, depth=0):
        a, b = strip_bang(a, b)
        token = src.tokens[a]
        if b - a == 1 and token.kind in ("string", "template"):
            return goish_text(src.template_text(token, lambda sub: "\x00") if token.kind == "template" else token.value)
        for op in ("??", "||"):
            parts = src.split_op(a, b, op)
            if len(parts) > 1:
                return any([goish(*p, depth) for p in parts])
        call = src.call_of(a, b)
        if call:
            return (call.name in ("which", "whichSync") and len(call.args) == 1 and src.string(*call.args[0]) == "go") \
                or (call.name in ("join", "resolve") and bool(call.args) and src.string(*call.args[-1]) == "go")
        name = src.name(a, b)
        if name and depth < 4:
            return any([goish(*r, depth + 1) for r in src.bound_all(name, a)])
        return False

    def substitute(sub):
        """A template substitution as shell text: go when it names the go binary."""
        inner = Source(src.text, "ts", sub)
        name = inner.name(0, len(inner.tokens)) if inner.tokens else None
        return "go" if name and any([goish(*r) for r in src.bound_all(name, len(src.tokens))]) else "\x00"

    def text_of(a, b):
        token = src.tokens[a]
        if b - a == 1 and token.kind == "template":
            return src.template_text(token, substitute)
        return "".join(src.string(*p) or "\x00" for p in src.split(a, b, "+"))

    def argv_site(argv, line, env):
        values = [src.string(*r) for r in argv]
        k, settings = env_words(values)
        if k < len(argv) and goish(*argv[k]):
            found.append((line, offline(env + settings)))
            return
        script = shell_c_script(values)
        if script is not None:
            found.extend(shell_sites(text_of(*argv[script]), src.line(argv[script][0]), env))

    for call in src.calls():
        line = src.line(call.at)
        if call.tagged:
            # Bun's $`...`: the template is shell; a .env({...}) after it is its environment.
            env = [scan.INHERIT]
            after = call.close + 1
            if src.punct(after, ".") and after + 2 < len(src.tokens) and src.tokens[after + 1].value == "env" \
                    and src.punct(after + 2, "(") and after + 2 in src.partner:
                inner = src.split(after + 3, src.partner[after + 2])
                env = env_elements(src, *inner[0]) if inner else env
            found.extend(shell_sites(text_of(call.open, call.open + 1), line, env))
        elif call.name in SPAWNS and call.args:
            # Each array the spawn reads is its argv's, so the pass over argv arrays below skips it.
            argv, options = src.ts_spawn_argv(call, handled)
            env = ts_options_env(src, call, options)
            # An argv bound to a name is read where it is spelled, as one given in place is.
            bound = argv and src.name(*call.args[0]) is not None
            argv_site(argv, src.line(argv[0][0]) if bound else line, [scan.INHERIT] if env is None else env)
        elif call.name in SHELL_CALLS and call.args and call.callee in (
                call.name, f"child_process.{call.name}", f"childProcess.{call.name}", f"cp.{call.name}"):
            env = ts_options_env(src, call, 1)
            found.extend(shell_sites(text_of(*call.args[0]), line, [scan.INHERIT] if env is None else env))
    # An argv array no spawn above read, that starts go and a subcommand: its call's options, if any.
    arguments = {r[0]: call for call in src.calls() for r in call.args}
    for i, token in enumerate(src.tokens):
        if not (token.kind == "punct" and token.value == "[" and i in src.partner) or i in handled:
            continue
        argv = src.split(i + 1, src.partner[i])
        values = [src.string(*r) for r in argv]
        k, settings = env_words(values)
        if k < len(argv) and goish(*argv[k]) and go_subcommand(values, k):
            holder = arguments.get(i)
            env = ts_options_env(src, holder, 1) if holder else None
            found.append((src.line(i), offline(([scan.INHERIT] if env is None else env) + settings)))
    return found


# --- Python: each argv list or tuple that starts go, each shell string, and a call's env=.

def python_sites(tree):
    consumed, found = set(), []

    def goish(node, depth=0):
        text = python_text(node)
        if text is not None:
            return goish_text(text)
        if isinstance(node, ast.Call):
            callee = ast.unparse(node.func)
            last = callee.rsplit(".", 1)[-1]
            args = [python_text(a) for a in node.args]
            # go-offline: this check's own reading compares a word with "go" here; it starts nothing.
            if (last == "which" and args[:1] == ["go"]) or (last == "join" and args and args[-1] == "go"):
                consumed.add(id(node))
                return True
            return False
        if isinstance(node, ast.Name) and depth < 4:
            bound = python_bound(node.id, node)
            return bound is not None and goish(bound, depth + 1)
        return False

    def text_of(node):
        return python_text(node, lambda value: "go" if goish(value) else "\x00")

    def env_of(call):
        if call is None:
            return [scan.INHERIT]
        env = next((k.value for k in call.keywords if k.arg == "env"), None)
        return [scan.INHERIT] if env is None else python_env_elements(env)

    def receiver(node):
        """The call an argv is given to: the one it is an argument of, or the first call after its
        assignment to a name that is given that name."""
        parent = getattr(node, "parent", None)
        if isinstance(parent, ast.Call) and node in parent.args:
            return parent
        if isinstance(parent, ast.Assign) and len(parent.targets) == 1 and isinstance(parent.targets[0], ast.Name):
            name = parent.targets[0].id
            for other in ast.walk(python_scope(node)):
                if isinstance(other, ast.Call) and other.lineno >= parent.lineno and any(
                        isinstance(a, ast.Name) and a.id == name for a in other.args):
                    return other
        return None

    def argv_site(nodes, line, call):
        values = [python_text(n) for n in nodes]
        k, settings = env_words(values)
        if k < len(nodes) and goish(nodes[k]):
            found.append((line, offline(env_of(call) + settings)))
            return True
        script = shell_c_script(values)
        if script is not None:
            found.extend(shell_sites(text_of(nodes[script]) or "", nodes[script].lineno - 1, env_of(call)))
            return True
        return False

    for node in ast.walk(tree):
        if isinstance(node, (ast.List, ast.Tuple)) and node.elts:
            argv_site(node.elts, node.lineno - 1, receiver(node))
        elif isinstance(node, ast.Call) and node.args:
            callee = ast.unparse(node.func)
            first = node.args[0]
            if callee.rsplit(".", 1)[-1] not in ("which", "join") and not isinstance(first, (ast.List, ast.Tuple)) \
                    and argv_site(node.args, node.lineno - 1, node):
                continue
            text = text_of(first)
            if text is not None and (callee in ("os.system", "os.popen") or callee.startswith("subprocess.")):
                found.extend(shell_sites(text, first.lineno - 1, env_of(node)))
    # go-offline: this check's own reading compares a word with "go" here; it starts nothing.
    for node in ast.walk(tree):
        if isinstance(node, ast.Call) and id(node) not in consumed and ast.unparse(node.func).endswith("which") \
                and [python_text(a) for a in node.args[:1]] == ["go"]:
            found.append((node.lineno - 1, False))  # resolves the go binary something else starts
    return found


def sites_of(path, text, lang):
    """(index, offline) of each line of a file that starts a go command: a line is offline when every
    go command it starts is."""
    if lang == "sh":
        found = shell_sites(text)
    elif lang == "py":
        tree = python_tree(text)
        if tree is None:
            return None
        found = python_sites(tree)
    else:
        source = Source(text, lang)
        found = go_sites(source) if lang == "go" else ts_sites(source)
    lines = {}
    for index, is_offline in found:
        lines[index] = lines.get(index, True) and is_offline
    return sorted(lines.items())


def reasoned(raw, index, lang):
    return scan.reasoned(raw, lang, REASON, [index])
