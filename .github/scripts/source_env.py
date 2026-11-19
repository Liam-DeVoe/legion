"""The environment a Go or TypeScript program gives a command it starts, read from a Source
(source_statements.py): a Go []string or an *exec.Cmd's own Env, a TypeScript env object or a call's
options. Each reader returns a list of elements in the order the program makes them (source_scan's
INHERIT, UNKNOWN and CLEAR, and ("set", NAME, VALUE)); the last element that may set a variable
decides it.

Imported with PYTHONPATH naming the directory this file lives in. Its own test is
source_scan.test.sh.
"""
from source_scan import INHERIT, UNKNOWN


def env_elements(src, a, b, depth=0, known=None):
    """The environment elements of an expression that builds one: in Go a []string (append of
    os.Environ() and "K=V" strings, a []string{...}, a name bound to either), in TypeScript an
    object ({ ...process.env, K: "v" }, a name bound to one). known maps a name to the
    elements it stands for (a Cmd's own Env, read before a later assignment appends to it)."""
    if b <= a:
        return [UNKNOWN]
    tokens = src.tokens
    name = src.name(a, b)
    if name:
        if known and name in known:
            return list(known[name])
        bound = src.bound(name, a) if depth < 4 else None
        if bound:
            return env_elements(src, *bound, depth + 1, known)
        return [UNKNOWN]
    if src.lang == "ts":
        if src.punct(a, "{") and src.partner.get(a) == b - 1:
            out = []
            for pa, pb in src.split(a + 1, b - 1):
                if src.punct(pa, "..."):
                    out.append(UNKNOWN)
                    continue
                colon = next((j for j in range(pa, pb) if src.punct(j, ":")), None)
                key_token = tokens[pa]
                key = key_token.value if key_token.kind in ("ident", "string") and colon == pa + 1 else None
                if colon is None and pb - pa == 1 and key_token.kind == "ident":
                    out.append(("set", key_token.value, None))
                elif key is None:
                    out.append(UNKNOWN)
                else:
                    out.append(("set", key, src.string(colon + 1, pb)))
            return out
        return [UNKNOWN]
    # Go.
    call = src.call_of(a, b)
    if call and call.callee == "os.Environ":
        return [INHERIT]
    if call and call.callee == "append" and call.args:
        out = list(env_elements(src, *call.args[0], depth, known))
        for ea, eb in call.args[1:]:
            if src.punct(eb - 1, "..."):
                out += env_elements(src, ea, eb - 1, depth, known)
            else:
                out.append(_go_setting(src, ea, eb))
        return out
    if src.punct(a, "[") and src.punct(a + 1, "]"):
        brace = next((j for j in range(a, b) if src.punct(j, "{")), None)
        if brace is not None and src.partner.get(brace) == b - 1:
            return [_go_setting(src, ea, eb) for ea, eb in src.split(brace + 1, b - 1)]
    if tokens[a].kind == "ident" and src.punct(a + 1, "[") and src.partner.get(a + 1) == b - 1:
        return env_elements(src, a, a + 1, depth, known)  # env[:len(env):len(env)]
    return [UNKNOWN]


def _go_setting(src, a, b):
    token = src.tokens[a]
    if token.kind == "string" and "=" in token.value:
        key, value = token.value.split("=", 1)
        return ("set", key, value if b - a == 1 else None)
    return UNKNOWN


def go_cmd_envs(src, call):
    """The *exec.Cmd that a call of exec.Command makes, and how its environment is set: the
    variable it is assigned to, and the rhs range of each `X.Env = ...` of that variable after
    it in the same declaration, in order, before the variable is assigned again; (None, []) when
    the call is assigned to no variable."""
    target = None
    for lhs, rhs, k in src.assignments():
        for index, (ra, rb) in enumerate(rhs):
            if ra == call.at and rb == call.close + 1 and index < len(lhs):
                target = src.name(*lhs[index])
        if target:
            break
    if not target:
        return None, []
    low, high = src.region(call.at)
    sets = []
    for lhs, rhs, k in src.assignments():
        if k <= call.close or not low <= k < high:
            continue
        names = [src.name(a, b) for a, b in lhs]
        if target in names:
            break
        sets += [rhs[index] for index, name in enumerate(names) if name == f"{target}.Env" and index < len(rhs)]
    return target, sets


def go_cmd_env(src, call):
    """The environment elements of the *exec.Cmd that a call of exec.Command makes: the last
    `X.Env = ...` go_cmd_envs finds. None when no Env is set (the command inherits the
    environment)."""
    target, sets = go_cmd_envs(src, call)
    env = None
    for a, b in sets:
        # A nil Env is the process's own; `X.Env = append(X.Env, ...)` extends the last.
        env = env_elements(src, a, b, known={f"{target}.Env": env if env is not None else [INHERIT]})
    return env


def ts_options_env(src, call, first):
    """The environment elements of a TypeScript call's options: the `env` of the first object
    argument from index `first` (or of the object a name there is bound to), else None."""
    for a, b in call.args[first:]:
        name = src.name(a, b)
        if name:
            bound = src.bound(name, a)
            if bound:
                a, b = bound
        if not (src.punct(a, "{") and src.partner.get(a) == b - 1):
            continue
        for pa, pb in src.split(a + 1, b - 1):
            key = src.tokens[pa]
            if key.value != "env" or key.kind not in ("ident", "string"):
                continue
            if pb - pa == 1:  # { env }
                bound = src.bound("env", pa)
                return env_elements(src, *bound) if bound else [UNKNOWN]
            if src.punct(pa + 1, ":"):
                return env_elements(src, pa + 2, pb)
    return None
