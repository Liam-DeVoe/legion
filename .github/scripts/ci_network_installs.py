"""Rule 22 of check-ci-filters.sh: a required job waits on the network only under a bound of its
own, and its limits leave room for each such wait to end by name.

Every job of ci.yaml, and of a workflow it calls, is on the path of ci-ok, the check main's
ruleset requires, or is pr-title, the other one, so an install there that waits without a bound
fails a pull request for a cause outside its change: on 2026-11-20 run 44665181416's daemon-go job
waited in `sudo apt-get update` from 03:05:59Z until the job's 70-minute limit, on a mirror that
had sent an answer's headers and then all but stopped, which apt's own timeout, a wait for the
next byte, never ends. The one way such a job may wait on the network is through
.github/scripts/network-install.sh, which bounds each attempt, tries it a fixed number of times
and fails by name (its header says how each kind's bound was measured; `--bounds` prints its
table, which this rule reads).

ci_filter_coverage.main() runs check_network_installs over ci.yaml and the workflows it calls.
Its docstring says what the rule reads, refuses, counts and leaves alone.
"""

import hashlib
import json
import os
import posixpath
import re
import subprocess
from dataclasses import dataclass, field
from functools import cache

from ci_filter_coverage import REPO, error, load_repository_yaml, read_repository_text, squash, step_label
from shell_commands import STDIN_SHELLS, WRAPPERS, Scanner, command_of, pipelines_of, read_shell, substitution

NETWORK_INSTALL = ".github/scripts/network-install.sh"
ACTIONS_PREFIX = "./.github/actions/"
BUILD_ACTION = "docker/build-push-action@"
# The variables network-install.sh reads in place of its bounds for its own tests alone; this rule
# asks it for the real ones with them unset.
NETWORK_INSTALL_SEAMS = ("NETWORK_INSTALL_BOUND_SECONDS", "NETWORK_INSTALL_KILL_AFTER_SECONDS",
                         "NETWORK_INSTALL_PAUSE_SECONDS")
# GitHub's limit for a job or a step that sets no timeout-minutes.
DEFAULT_MINUTES = 360
# apt's own verbs that read a mirror, and the options of apt and apt-get that take a value.
APT = {"apt", "apt-get", "aptitude"}
APT_VERBS = {"update", "install", "reinstall", "upgrade", "dist-upgrade", "full-upgrade", "build-dep", "source",
             "download"}
APT_VALUE_OPTIONS = {"-o", "-c", "-t", "--option", "--config-file", "--target-release"}
# Every other installer, by its command name: the verbs of it that install from the network, and
# the options of it that take a value of their own and so may stand before the verb (`npm --prefix
# dir ci`, `bun --cwd dir install`); a `+toolchain` word (`cargo +nightly install`) is an option.
# npm's verbs hold its aliases (`clean-install`, `ic`, `cit`, …), and `bun ci` is Bun's frozen
# install (Bun 1.4.4's `bun ci --help` prints `Usage: bun install`). A yarn with no verb at all
# installs too (yarn's own documentation: "Running `yarn` with no command will run `yarn install`").
PIP_VALUE_OPTIONS = {"--proxy", "--cert", "--client-cert", "--log", "--timeout", "--retries", "--python",
                     "--cache-dir", "--exists-action", "--trusted-host"}
INSTALLERS = {
    "pip": ({"install", "download"}, PIP_VALUE_OPTIONS),
    "pip3": ({"install", "download"}, PIP_VALUE_OPTIONS),
    "pipx": ({"install"}, set()),
    "npm": ({"install", "i", "ci", "add", "clean-install", "ic", "install-clean", "cit", "it",
             "install-test", "install-ci-test"},
            {"--prefix", "-C", "--workspace", "-w", "--registry", "--cache", "--userconfig", "--loglevel"}),
    "pnpm": ({"install", "i", "add"}, {"-C", "--dir", "--filter", "-F", "--reporter", "--loglevel"}),
    "yarn": ({"install", "add"}, {"--cwd", "--cache-folder", "--modules-folder", "--registry"}),
    "bun": ({"install", "i", "add", "ci"}, {"--cwd", "-c", "--config", "--env-file", "-r", "--preload"}),
    "gem": ({"install"}, {"--config-file"}),
    "cargo": ({"install"}, {"-C", "--config", "-Z", "--color"}),
    "snap": ({"install"}, set()),
    "brew": ({"install"}, set()),
    "apk": ({"add", "update", "upgrade", "fetch"}, {"-X", "--repository", "-p", "--root", "--cache-dir",
                                                     "--keys-dir", "--arch", "--repositories-file"}),
}
# The kind network-install.sh bounds each installer's install under, where it is not the program's
# own name.
KIND_OF = {"pip3": "pip", "pipx": "pip"}
UV_VALUE_OPTIONS = {"--directory", "--project", "--cache-dir", "--python", "-p", "--config-file", "--color"}
UV_INSTALLS = (["pip", "install"], ["pip", "sync"], ["tool", "install"], ["tool", "run"], ["python", "install"],
               ["sync"], ["venv"], ["run"], ["add"], ["lock"])
DOCKER_VALUE_OPTIONS = {"--config", "-c", "--context", "-H", "--host", "-l", "--log-level", "--tlscacert",
                        "--tlscert", "--tlskey"}
# The options of `docker run` and `docker create` that take a value of their own, so the image is
# the first word after them that is no option.
DOCKER_RUN_VALUE_OPTIONS = {
    "-e", "--env", "--env-file", "-v", "--volume", "-w", "--workdir", "--name", "-p", "--publish", "--entrypoint",
    "--network", "--net", "-u", "--user", "--mount", "--cpus", "--memory", "-m", "--add-host", "--tmpfs", "--label",
    "-l", "--ipc", "--platform", "--pull", "--cidfile", "--device", "--dns", "--gpus", "-h", "--hostname",
    "--log-driver", "--log-opt", "--memory-swap", "--pid", "--restart", "--security-opt", "--shm-size",
    "--stop-signal", "--stop-timeout", "--ulimit", "--userns", "--uts", "--volumes-from", "-a", "--attach",
    "--cap-add", "--cap-drop", "--cgroupns", "--group-add", "--health-cmd", "--init-path", "--isolation",
    "--link", "--mac-address", "--runtime", "--sysctl", "--expose", "--cpu-shares", "-c", "--cpuset-cpus",
}
# The runners of a package's own command, with their options that take a value, the options that
# fetch a package the project does not hold (`npx --yes cowsay`), the kind of each runner's fetch,
# and the packages whose `install` downloads Playwright's browsers.
PACKAGE_RUNNERS = {"bunx": {"-p", "--package"}, "npx": {"-p", "--package"}, "pnpx": set()}
RUNNER_FETCHES = {"--yes", "-y", "-p", "--package"}
RUNNER_KIND = {"bunx": "bun", "npx": "npm", "pnpx": "pnpm"}
PLAYWRIGHT_PACKAGES = {"playwright", "@playwright/test", "playwright-core"}
# The hosts a curl may read with no bound of its own: this runner's own servers.
LOCAL_HOSTS = {"localhost", "0.0.0.0", "[::1]"}
# The Lean commands that download the toolchain lean-toolchain names when elan has not installed it.
LEAN_COMMANDS = {"lake", "lean", "leanc"}
# How a script's path or its directory is written, which the resolver reads as the script itself.
SCRIPT_PATH = {"$0", "${0}", "$BASH_SOURCE", "${BASH_SOURCE}", "${BASH_SOURCE[0]}"}
SCRIPT_DIRECTORY = {"${0%/*}", "${BASH_SOURCE%/*}", "${BASH_SOURCE[0]%/*}"}
# The variables that name a place outside the repository: a command there is no script of it.
OUTSIDE_VARIABLES = {"RUNNER_TEMP", "TMPDIR", "HOME", "RUNNER_TOOL_CACHE"}
# What the resolver gives a path outside the repository: known not to be a file of it.
OUTSIDE = "outside"
# The directories a Dockerfile's RUN finds a bare command in, which a COPY may fill.
IMAGE_PATH = ("/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin")
# What a pull the runner or a builder makes costs a job's limit: the runner pulls each `services:`
# and `container:` image with `docker pull` and no timeout of its own (actions/runner retries it
# only after a non-zero exit), and a builder pulls each FROM no earlier step left on the runner, and
# no workflow can give either pull a bound, so a job's or a step's limit is theirs. The slowest of
# the 304 green "Initialize containers" steps of the 20 newest merge-group runs pulled its one image
# in 62.7 s (dispatch-browser's webkit-alone, job 135946192641, the 1 GB browser image; round 3's
# evidence, measure/initialize-containers.txt). 2 × 62.7 s is 125.4 s, so 180 s, rounded up to the
# next minute as network-install.sh's kinds are.
PULL_SECONDS = 180
# The Dockerfiles whose RUN installs rule 22 counts against their build step's own limit rather
# than a bound of their own, each only while it holds the bytes whose sha256 is given. The browser
# image's recipe: browser-image.yaml builds it only when the recipe changed, under its step's own
# 2-minute limit, from the context .github/docker, which holds no network-install.sh; and any edit
# to the recipe moves its hash, so check-ci-images.sh's rule 10 already needs the image built
# again and its .github/ci-images.yaml row moved. That edit wraps or time-bounds its installs (a
# --max-time on its curl), at no further cost: the hash below stops matching, and this rule then
# reads the recipe as it reads every other.
PINNED_RECIPES = {
    ".github/docker/browser.Dockerfile": "b44a4dabdf97d209de663fe05d4b5e21234511703fe4f4827da9c5960ddf8b99",
}


def _from_verb(rest: list, value_options: set) -> list:
    """The words from the first that is no option on, each option's own value taken with it."""
    index = 0
    while index < len(rest):
        if rest[index] in value_options:
            index += 2
        elif rest[index].startswith(("-", "+")):
            index += 1
        else:
            return rest[index:]
    return []


def _playwright_install(rest: list, value_options: set) -> bool:
    """Whether a package runner's words (`playwright install chromium`, `-p @playwright/test
    playwright install`) run Playwright's browser download."""
    words = _from_verb(rest, value_options)
    package = words[0].rsplit("@", 1)[0] if words[:1] and "@" in words[0][1:] else (words[:1] or [""])[0]
    return package in PLAYWRIGHT_PACKAGES and _from_verb(words[1:], set())[:1] == ["install"]


def _literal(word: str) -> bool:
    """Whether a word is written out whole: no variable, expression, substitution or glob in it."""
    return not any(mark in word for mark in ("$", "EXPR", "`", "*", "?", "["))


@dataclass
class Docker:
    """A docker command: its verb as the rule names it, the image it runs or pulls where it names
    one, and its `--pull` policy."""
    verb: str
    image: str = ""
    pull: str = ""


def docker_command(name: str, rest: list) -> Docker | None:
    """The docker (or docker-compose) command the words make, where it can reach a registry: a pull
    (`docker pull`, `docker image pull`, `docker compose pull`), a container of an image (`docker
    run`, `docker create`, `docker container run|create`, `docker compose up|run|create`) or a
    build (`docker build`, `docker buildx build`, `docker image build`)."""
    command = _from_verb(rest, DOCKER_VALUE_OPTIONS) if name == "docker" else ["compose"] + rest
    if command[:1] == ["pull"] or command[:2] == ["image", "pull"]:
        return Docker("docker pull")
    if command[:1] == ["compose"]:
        verb = _from_verb(command[1:], {"-f", "--file", "-p", "--project-name", "--profile", "--env-file"})
        if verb[:1] == ["pull"]:
            return Docker("docker pull")
        if verb[:1] in (["up"], ["run"], ["create"]):
            pull = next((w.split("=", 1)[1] for w in verb if w.startswith("--pull=")), "")
            pull = pull or next((verb[i + 1] for i, w in enumerate(verb[:-1]) if w == "--pull"), "")
            return Docker(f"docker compose {verb[0]}", pull=pull)
        return None
    if command[:1] in (["build"],) or command[:2] in (["buildx", "build"], ["image", "build"]):
        return Docker("docker build")
    verb = command[1:2] if command[:1] == ["container"] else command[:1]
    if verb not in (["run"], ["create"]):
        return None
    words = command[2:] if command[:1] == ["container"] else command[1:]
    pull, index = "", 0
    while index < len(words) and words[index].startswith("-"):
        word = words[index]
        if word.startswith("--pull="):
            pull = word.split("=", 1)[1]
        elif word == "--pull" and index + 1 < len(words):
            pull = words[index + 1]
        if word in DOCKER_RUN_VALUE_OPTIONS:
            index += 1
        index += 1
    return Docker(f"docker {verb[0]}", words[index] if index < len(words) else "", pull)


def _curl_install(rest: list) -> bool:
    """Whether a curl reads a server outside this runner with no bound of its own: it names a
    literal URL of another host and no --max-time (curl's own retries and its --connect-timeout
    leave a body that trickles unbounded)."""
    if any(w in ("-m", "--max-time") or w.startswith("--max-time=") or re.fullmatch(r"-m[0-9.]+", w)
           for w in rest):
        return False
    for word in rest:
        match = re.match(r"^[A-Za-z][A-Za-z0-9+.-]*://([^/:?#]*)", word)
        if match and _literal(match.group(1)) and match.group(1):
            host = match.group(1).lower()
            if host not in LOCAL_HOSTS and not host.startswith("127."):
                return True
    return False


def network_install(words: list) -> tuple[str, str] | None:
    """The install a command makes from the network, as the words that say so (`apt-get update`,
    `bun install`) and its kind, network-install.sh's word for it (`apt`, `bun`, `docker`,
    `playwright`, …; an install that script has no kind for is of a kind named after its
    program), or None where the command makes none. A docker container of an image, a docker
    build and a Lean command are installs the job's reader decides on, since what an earlier step
    pulled or installed decides them."""
    name = words[0].rsplit("/", 1)[-1]
    rest = words[1:]
    if name in APT:
        verb = (_from_verb(rest, APT_VALUE_OPTIONS) or [""])[0]
        return (f"{name} {verb}", "apt") if verb in APT_VERBS else None
    if name == "wget":
        return ("wget", "wget") if _from_verb(rest, {"-O", "-o", "-P", "-e", "--output-document"}) else None
    if name == "curl":
        return ("curl", "curl") if _curl_install(rest) else None
    if name in ("docker", "docker-compose"):
        docker = docker_command(name, rest)
        if docker is None or docker.verb == "docker build" or docker.pull == "never":
            return None
        return (docker.verb, "docker")
    if name in PACKAGE_RUNNERS:
        if _playwright_install(rest, PACKAGE_RUNNERS[name]):
            return ("playwright install", "playwright")
        leading = rest[:len(rest) - len(_from_verb(rest, PACKAGE_RUNNERS[name]))]
        if any(word in RUNNER_FETCHES or word.startswith("--package=") for word in leading):
            return (name, RUNNER_KIND[name])
        return None
    if name == "bun" and _from_verb(rest, INSTALLERS["bun"][1])[:1] == ["x"] and \
            _playwright_install(_from_verb(rest, INSTALLERS["bun"][1])[1:], PACKAGE_RUNNERS["bunx"]):
        return ("playwright install", "playwright")
    if name in ("uv", "uvx") and "--offline" in rest:  # uv's own flag: "Disable network access"
        return None
    if name == "uvx":
        return ("uvx", "uv") if _from_verb(rest, UV_VALUE_OPTIONS | {"--from", "--with"}) else None
    if name == "uv":
        command = _from_verb(rest, UV_VALUE_OPTIONS)
        for verbs in UV_INSTALLS:
            if command[:len(verbs)] == verbs:
                return (f"uv {' '.join(verbs)}", "uv")
        return None
    if name.startswith("python") and rest[:2] == ["-m", "pip"] and "install" in rest[2:]:
        return (f"{name} -m pip install", "pip")
    if name == "go":
        fetch = go_fetch(rest)
        return (fetch, "go") if fetch else None
    if name == "elan":
        fetch = elan_fetch(rest)
        return (fetch, "elan") if fetch else None
    if name == "gh":
        command = _from_verb(rest, {"-R", "--repo"})
        return ("gh release download", "gh") if command[:2] == ["release", "download"] else None
    if name in INSTALLERS:
        verbs, value_options = INSTALLERS[name]
        command = _from_verb(rest, value_options)
        verb = (command or [""])[0]
        if name == "yarn" and not command and not {"--version", "-v", "--help", "-h"} & set(rest):
            return ("yarn install", "yarn")
        return (f"{name} {verb}", KIND_OF.get(name, name)) if verb in verbs else None
    return None


def go_fetch(rest: list) -> str:
    """The fetch a go command names, as words (`go mod download`, `go install <module>@<version>`),
    or "" where it names none. A step that names a fetch is an install; the modules a `go build`,
    `go test`, `go vet` or `go run` of a package of this repository downloads by itself are not,
    since no step names them (pull request #4434 moves those into one fetch step per job, which
    this rule then reads)."""
    if rest[:1] == ["-C"]:
        rest = rest[2:]
    words = [word for word in rest if not word.startswith("-")]
    if words[:1] == ["mod"] and words[1:2] in (["download"], ["tidy"], ["vendor"]):
        return f"go mod {words[1]}"
    if words[:1] == ["get"]:
        return "go get"
    if words[:1] in (["install"], ["run"]) and any("@" in word for word in words[1:]):
        return f"go {words[0]} <module>@<version>"
    return ""


def elan_fetch(rest: list) -> str:
    """The download an elan command makes of a Lean toolchain, as words (`elan toolchain install`,
    `elan default`, `elan override set`, `elan run`), or ""."""
    words = [word for word in rest if not word.startswith("-")]
    for verbs in (["toolchain", "install"], ["default"], ["override", "set"], ["run"]):
        if words[:len(verbs)] == verbs and len(words) > len(verbs):
            return f"elan {' '.join(verbs)}"
    return ""


@cache
def network_install_bounds() -> dict | str:
    """network-install.sh's table, read by running it with `--bounds` and its test seams unset:
    {kind: (bound s, attempts, s before SIGKILL, pause s)}, or why it could not be read."""
    script = REPO / NETWORK_INSTALL
    if not script.is_file():
        return f"{NETWORK_INSTALL} is not in the repository"
    env = {name: value for name, value in os.environ.items() if name not in NETWORK_INSTALL_SEAMS}
    result = subprocess.run(["sh", str(script), "--bounds"], capture_output=True, text=True, env=env,
                            timeout=60, check=False)
    try:
        if result.returncode != 0:
            raise ValueError(f"exit {result.returncode}")
        return {kind: tuple(int(field) for field in fields)
                for kind, *fields in (line.split() for line in result.stdout.splitlines())
                if len(fields) == 4}
    except ValueError as exc:
        return f"`{NETWORK_INSTALL} --bounds` did not print its table ({exc}): {squash(result.stderr)}"


def worst_case(bound: int, attempts: int, kill_after: int, pause: int) -> int:
    """The seconds a wrapped install can take before network-install.sh fails it by name: every
    attempt cut at its bound and killed after the wait for SIGTERM, with the pauses between them."""
    return attempts * (bound + kill_after) + (attempts - 1) * pause


def _minutes(holder: dict) -> int | None:
    """A job's or a step's timeout-minutes, GitHub's 360 where it sets none, or None where it is an
    expression this rule cannot read."""
    value = holder.get("timeout-minutes", DEFAULT_MINUTES)
    return value if isinstance(value, (int, float)) else None


@dataclass
class Site:
    """Where a command runs: the job and its workflow, the job's step it runs in, the file that
    holds the command (the workflow, a local action's action.yml, a repository script or a
    Dockerfile), the words that say how that file is reached from the job's step, the label of the
    step in that file, the directory a relative path is read from, a local action's directory
    ($GITHUB_ACTION_PATH), the repository script the text is ($0), the variables the text sets to a
    path the resolver establishes, and, for a Dockerfile's RUN, the stage's working directory and
    the image paths a COPY or a bind mount fills with a file of the repository."""
    path: str
    job_id: str
    step: str
    where: str
    via: str = ""
    label: str = ""
    base: str = ""
    action: str = ""
    script: str = ""
    env: dict = field(default_factory=dict)
    image: bool = False
    workdir: str = "/"
    files: dict = field(default_factory=dict)
    fakes: frozenset = frozenset()


def _fakes(shell, word: str) -> frozenset:
    """The commands a text puts first on the PATH it gives the command `word` runs: each file it
    writes into the directory that PATH names first. autodeploy_test.sh writes its fake docker to
    "$bin/docker" (its line 55, `cat >"$bin/docker"`) and runs autodeploy.sh under
    `PATH="$bin:$PATH"` (its line 334), so in envoy-go-checks every docker call of autodeploy.sh
    reaches that fake and no registry. In the script `word` runs, and the scripts that one runs in
    turn, a command of such a name is the fake and no install; every other command is read as
    anywhere else. A directory the PATH names after the runner's own is no fake's: the real
    command comes first there."""
    firsts = {value.split(":", 1)[0] for command, value in shell.paths if command == word}
    return frozenset(posixpath.basename(target) for target in shell.writes
                     if posixpath.dirname(target) and posixpath.dirname(target) in firsts)


def _normal(path: str) -> str:
    """An anchored path ("/" is the repository's root) with its `.` and `..` read, or OUTSIDE where
    a `..` climbs out of the root."""
    parts: list = []
    for part in path.split("/"):
        if part in ("", "."):
            continue
        if part == "..":
            if not parts:
                return OUTSIDE
            parts.pop()
        else:
            parts.append(part)
    return "/" + "/".join(parts)


def _head(word: str) -> tuple[str, str]:
    """A word's leading expansion (`$(…)`, `${…}`, `$NAME`, `$0`) and the rest of it."""
    if word.startswith("$("):
        end = Scanner.balanced(word, 1)
        return word[:end + 1], word[end + 1:]
    if word.startswith("${"):
        end = word.find("}")
        return (word[:end + 1], word[end + 1:]) if end > 0 else (word, "")
    match = re.match(r"\$([A-Za-z_][A-Za-z0-9_]*|[0-9])", word)
    return (match.group(0), word[match.end():]) if match else ("", word)


def _image_file(path: str, site: Site) -> str:
    """The anchored repository path an image path holds, where a COPY or a bind mount put a file of
    the repository there, or OUTSIDE."""
    normal = posixpath.normpath(path)
    return "/" + site.files[normal] if normal in site.files else OUTSIDE


def _anchor(word: str, site: Site, depth: int = 0) -> str | None:
    """The path a command word names, the one resolver of rule 22: an anchored repository path ("/"
    is the root), OUTSIDE for a place known to be outside the repository (an absolute path, a
    scratch directory, $RUNNER_TEMP, a `..` past the root), or None where the word cannot be
    resolved. It reads, from the site, a relative path (from the step's working directory, or a
    RUN's WORKDIR), $GITHUB_WORKSPACE, $GITHUB_ACTION_PATH in a local action, $0 and the script's
    directory however written (`$(dirname "$0")`, `${BASH_SOURCE[0]%/*}`, `$(cd "$(dirname "$0")/.."
    && pwd)`, `$(realpath …)`, `$(git rev-parse --show-toplevel)`), a variable the text set to one
    of those, and an image path a COPY or a bind mount of the Dockerfile filled."""
    if depth > 8 or not word:
        return None
    head, rest = _head(word)
    if not head:
        if not _literal(word.replace("[", "").replace("?", "")) or word.startswith(("<(", ">(", "`")):
            return None
        if word.startswith("/"):
            return _image_file(word, site) if site.image else OUTSIDE
        if site.image and "/" not in word:  # a bare command of a RUN: the image's PATH
            found = [f"{d}/{word}" for d in IMAGE_PATH if f"{d}/{word}" in site.files]
            return _image_file(found[0], site) if found else OUTSIDE
        if site.image:
            return _image_file(posixpath.join(site.workdir, word), site)
        return _normal(posixpath.join("/" + site.base, word))
    value = _expansion(head, site, depth)
    if value is None or value == OUTSIDE:
        return value
    if rest and (not rest.startswith("/") or not _literal(rest)):
        return None
    return _normal(value + rest)


def _inner_texts(word: str) -> list:
    """The text of each substitution a word holds (`$(…)`, `<(…)`, `>(…)`), wherever it stands in it."""
    texts, index = [], 0
    while index < len(word):
        if word.startswith(("$(", "<(", ">("), index) and not word.startswith("$((", index):
            end = Scanner.balanced(word, index + 1)
            texts.append(word[index + 2:end])
            index = end + 1
        else:
            index += 1
    return texts


def _own_commands(text: str) -> list:
    """The commands a substitution's text runs itself, in order, leaving out those of the
    substitutions inside their words, which shell_commands also gives (`cd "$(dirname "$0")/.." &&
    pwd` runs cd and pwd)."""
    commands = [command for pipeline in pipelines_of(text) for command in pipeline]
    nested = [inner for command in commands for word in command for inner_text in _inner_texts(word)
              for pipeline in pipelines_of(inner_text) for inner in pipeline]
    return [command for command in commands if command not in nested]


def _expansion(head: str, site: Site, depth: int) -> str | None:
    """What one expansion at the start of a word names, for _anchor."""
    if head in SCRIPT_PATH:
        return "/" + site.script if site.script else None
    if head in SCRIPT_DIRECTORY:
        return "/" + posixpath.dirname(site.script) if site.script else None
    match = re.fullmatch(r"\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?", head)
    if match:
        name = match.group(1)
        if name == "GITHUB_WORKSPACE":
            return "/"
        if name == "GITHUB_ACTION_PATH" and site.action:
            return "/" + site.action
        if name == "PWD" and not site.image:
            return _normal("/" + site.base)
        if name in site.env:
            return site.env[name]
        return OUTSIDE if name in OUTSIDE_VARIABLES else None
    text = substitution(head) if head.startswith("$(") else None
    if text is None:
        return None
    commands = _own_commands(text)
    if len(commands) == 2 and commands[0][0] == "cd" and commands[1][0] == "pwd":
        target = [w for w in commands[0][1:] if w not in ("--", "-P", "-L")]
        return _anchor(target[0], site, depth + 1) if len(target) == 1 else None
    if len(commands) != 1:
        return None
    command = commands[0]
    arguments = [w for w in command[1:] if w != "--" and not w.startswith("-")]
    if command[0] == "dirname" and len(arguments) == 1:
        value = _anchor(arguments[0], site, depth + 1)
        return value if value in (None, OUTSIDE) else posixpath.dirname(value)
    if (command[0] == "realpath" or command[:2] == ["readlink", "-f"]) and len(arguments) == 1:
        return _anchor(arguments[0], site, depth + 1)
    if command[0] == "git" and "rev-parse" in command and "--show-toplevel" in command:
        return "/"
    if command[0] == "pwd" and not site.image:
        return _normal("/" + site.base)
    if command[0] == "mktemp":
        return OUTSIDE
    return None


def _environment(assignments: list, site: Site) -> dict:
    """The variables a text sets to a path the resolver establishes, each from its assignments in
    order (each may read those before it). A variable assigned two different paths, or once to a
    value the resolver cannot read, is none of them."""
    env: dict = {}
    refused: set = set()
    for name, value in assignments:
        if name in refused:
            continue
        site.env = env
        anchored = _anchor(value, site) if value else None
        if anchored is None or env.get(name, anchored) != anchored:
            refused.add(name)
            env.pop(name, None)
        else:
            env[name] = anchored
    site.env = env
    return env


def _resolved(word: str, site: Site) -> str | None:
    """The repository-relative path a command word names, or None where it names none (outside the
    repository, or past the resolver)."""
    anchored = _anchor(word, site)
    return None if anchored in (None, OUTSIDE) else anchored[1:]


def _repository_script(word: str, site: Site) -> str | None:
    """The repository-relative path of the shell script a command word runs, where it names one
    this repository holds (a `.sh` file, or a file whose `#!` line names bash, sh, dash or zsh), read
    through the resolver from the site's directory, or, for a relative path, from the root; a bare
    name with no `/` is a program on PATH unless it ends in `.sh`, and in a Dockerfile's RUN it is
    the file a COPY put on the image's PATH. A word the resolver cannot place whose path after its
    first expansion names a shell script of the repository (`"$root/.github/scripts/x.sh"` in a
    test harness that runs its fixture's link to x.sh) is read as that script: reading it can only
    refuse more, and the wrapper itself is never placed this way (read_wrapped asks _resolved)."""
    if "/" not in word:
        if site.image:
            candidates = [_resolved(word, site)]
        elif not word.endswith(".sh"):
            return None
        else:
            candidates = [_resolved(word, site), _resolved(word, Site("", "", "", "", action=site.action))]
    else:
        candidates = [_resolved(word, site)]
        head, rest = _head(word)
        if not head and not word.startswith("/") and not site.image:
            candidates.append(_resolved(word, Site("", "", "", "", action=site.action)))
        if head and _anchor(word, site) is None and rest.startswith("/") and _literal(rest):
            candidates.append(_normal(rest)[1:])
    for candidate in candidates:
        if candidate and (REPO / candidate).is_file():
            if candidate.endswith(".sh"):
                return candidate
            first = read_repository_text(REPO / candidate).split("\n", 1)[0]
            interpreter = first[2:].split() if first.startswith("#!") else []
            if interpreter[:1] and interpreter[0].endswith("/env"):
                interpreter = interpreter[1:]
            if interpreter and interpreter[0].rsplit("/", 1)[-1] in STDIN_SHELLS:
                return candidate
    return None


def _unfound_script(word: str, site: Site) -> bool:
    """Whether a command word runs a shell script the resolver cannot place: a word ending in `.sh`
    that holds an expansion the resolver cannot read. A script it places outside the repository (a
    scratch directory's) is not one, and neither is one of the repository."""
    return word.rstrip('"\'').endswith(".sh") and _anchor(word, site) is None


def _fetch_fed_to_shell(words: list) -> str | None:
    """`curl piped into a shell` (or wget's) where a shell runs what a fetch printed through a
    substitution: `bash <(curl …)`, `source <(curl …)`, `sh -c "$(curl …)"`, `eval "$(curl …)"`,
    each of which shell_commands leaves as a command whose first word is that substitution."""
    text = substitution(words[0])
    if text is None:
        return None
    for command in (c for pipeline in pipelines_of(text, WRAPPERS) for c in pipeline):
        name = command[0].rsplit("/", 1)[-1]
        if name in ("curl", "wget"):
            return f"{name} piped into a shell"
    return None


def _action_steps(uses: str, seen: set, inputs: dict) -> list:
    """The steps of the local composite action a `uses:` names, with each one's file and the inputs
    it gets (the caller's `with:` over the action's defaults), and those of the local actions they
    use in turn, each action once. An action this repository does not hold is check_table's to
    refuse, so it yields no steps here."""
    path = uses[2:].rstrip("/")
    if path in seen:
        return []
    seen.add(path)
    for name in ("action.yml", "action.yaml"):
        if (REPO / path / name).is_file():
            document = load_repository_yaml(REPO / path / name) or {}
            declared = {key: str((value or {}).get("default", "")) for key, value in
                        (document.get("inputs") or {}).items()}
            given = {**declared, **{str(k): _input_text(v, inputs) for k, v in inputs.items()}}
            steps = []
            for step in (document.get("runs") or {}).get("steps") or []:
                steps.append((f"{path}/{name}", step, given))
                inner = str(step.get("uses", ""))
                if inner.startswith(ACTIONS_PREFIX):
                    steps += _action_steps(inner, seen, {k: _input_text(v, given) for k, v in
                                                          (step.get("with") or {}).items()})
            return steps
    return []


def _input_text(value, inputs: dict) -> str | None:
    """A `with:` or `env:` value as text, an `${{ inputs.NAME }}` read from the inputs given, or
    None where it is another expression."""
    if value is None:
        return None
    text = str(value)
    match = re.fullmatch(r"\$\{\{\s*inputs\.([A-Za-z0-9_-]+)\s*\}\}", text.strip())
    if match:
        return inputs.get(match.group(1))
    return None if "${{" in text else text


def _loop_values(loop, known: dict) -> list | None:
    """The values a loop's variable takes, one per pass: the words of a `for` written out whole, or
    the first field of each non-empty line a `while` or `until` reads from a variable the step's env
    gives as text (`done <<<"$IMAGES"`, an input of the action its calling step's `with:` names);
    None where the rule cannot read them."""
    if loop.keyword == "for" and loop.words is not None and all(_literal(w) for w in loop.words):
        return list(loop.words)
    if loop.feed and loop.feed[0] == "<<<":
        match = re.fullmatch(r"\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?", loop.feed[1])
        text = known.get(match.group(1)) if match else None
        if text is not None:
            return [line.split()[0] for line in text.split("\n") if line.strip()]
    return None


def _iterations(loops: tuple, known: dict) -> int | None:
    """How many times a pipeline runs inside its loops, each loop's passes multiplied; None where a
    loop's passes cannot be read."""
    total = 1
    for loop in loops:
        values = _loop_values(loop, known)
        if values is None:
            return None
        total *= len(values)
    return total


def _values(word: str, loops: tuple, known: dict) -> list:
    """The values a command word takes: the word itself where it is written out whole, or each value
    of the innermost loop variable it names alone (`"$image"`); none where the rule cannot read it."""
    if _literal(word):
        return [word]
    match = re.fullmatch(r"\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?", word)
    for loop in reversed(loops):
        if match and loop.variable == match.group(1):
            return _loop_values(loop, known) or []
    return []


def dockerfile_instructions(text: str) -> list:
    """A Dockerfile's instructions as (line, KEYWORD, arguments): each continued line joined, a
    comment line skipped (inside a continuation too, as Docker drops it), and a RUN's here-document
    body (`RUN <<EOF` … `EOF`) given as its script."""
    lines = text.split("\n")
    out = []
    index = 0
    while index < len(lines):
        line = lines[index]
        start = index + 1
        index += 1
        if not line.strip() or line.lstrip().startswith("#"):
            continue
        joined = line
        while joined.rstrip().endswith("\\") and index < len(lines):
            following = lines[index]
            index += 1
            if following.lstrip().startswith("#"):
                continue
            joined = joined.rstrip()[:-1] + " " + following
        keyword, _, arguments = joined.strip().partition(" ")
        heredoc = re.search(r"<<-?\s*['\"]?([A-Za-z_][A-Za-z0-9_]*)['\"]?", arguments) \
            if keyword.upper() == "RUN" else None
        if heredoc:
            body = []
            while index < len(lines) and lines[index].strip() != heredoc.group(1):
                body.append(lines[index])
                index += 1
            index += 1
            arguments = arguments[:heredoc.start()] + "\n".join(body)
        out.append((start, keyword.upper(), arguments.strip()))
    return out


def _flags(arguments: str) -> tuple[list, str]:
    """An instruction's leading `--name=value` flags and the rest of its arguments."""
    flags = []
    while arguments.startswith("--"):
        flag, _, arguments = arguments.partition(" ")
        flags.append(flag)
        arguments = arguments.lstrip()
    return flags, arguments


def _substitute(text: str, values: dict) -> str:
    """A Dockerfile word with its `${NAME}` and `$NAME` read from the ARG defaults and build-args."""
    return re.sub(r"\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?", lambda m: values.get(m.group(1), m.group(0)), text)


class NetworkInstalls:
    """Rule 22's reading of one job: each install its steps run, in the steps' own `run:`, in the
    local actions they use, in the repository scripts those run and in the Dockerfiles they build,
    refused unless wrapped; and what the job can spend before the last of its installs fails by
    name, held to the limits of each step and of the job."""

    def __init__(self, path: str, job_id: str, job: dict, problems: list) -> None:
        self.path, self.job_id, self.job, self.problems = path, job_id, job, problems
        self.limit = _minutes(job)
        self.bounds = network_install_bounds()
        self.spent = 0  # the seconds the job can have spent before the step being read
        self.parts: list = []  # what self.spent counts, as (what, seconds, why)
        self.pulled: set = set()  # the images an earlier wrapped `docker pull` of the job pulled
        self.elan = False  # whether an earlier wrapped install of a Lean toolchain ran
        self.runner_pulls()

    def refuse(self, site: Site, message: str) -> None:
        self.problems.append(error(site.where, f"job {self.job_id} of {self.path}{site.via} {message}"))

    def runner_pulls(self) -> None:
        """The images the runner pulls before the job's first step: each `services:` image and the
        job's `container:`. The runner runs `docker pull` with no timeout of its own and tries it
        again only after a non-zero exit, and a workflow can give it none, so the job's limit is
        their one bound: each counts PULL_SECONDS, what a healthy pull of the runner takes."""
        images = [str((service or {}).get("image", "")) for service in (self.job.get("services") or {}).values()]
        container = self.job.get("container")
        images.append(str(container.get("image", "")) if isinstance(container, dict) else str(container or ""))
        images = [image for image in images if image]
        for image in images:
            self.spent += PULL_SECONDS
            self.parts.append((f"the runner's pull of {image}", PULL_SECONDS, "runner"))
            self.pulled.add(image)  # on the runner before the job's first step
        if images and self.limit is not None and self.spent > self.limit * 60:
            self.problems.append(error(self.path, f"job {self.job_id} of {self.path} has timeout-minutes {self.limit}, "
                                                  f"{self.limit * 60} s, and the runner's own pulls of its service and "
                                                  f"container images before its first step take {self.spent} s, "
                                                  f"{PULL_SECONDS} s each ({', '.join(images)}): the runner gives such a "
                                                  f"pull no bound of its own, so the job's limit must leave room for "
                                                  f"them and for what the job runs after them"))
            self.limit = None

    def read_step(self, step: dict) -> None:
        label = step_label(step)
        directory = step.get("working-directory") or (((self.job.get("defaults") or {}).get("run") or {})
                                                       .get("working-directory")) or ""
        directory = "" if "${{" in str(directory) else str(directory)
        own = Site(self.path, self.job_id, label, self.path, label=label, base=directory)
        located = [(own, step, {})]
        uses = str(step.get("uses", ""))
        if uses.startswith(ACTIONS_PREFIX):
            given = {str(k): _input_text(v, {}) for k, v in (step.get("with") or {}).items()}
            for where, inner, inputs in _action_steps(uses, set(), given):
                located.append((Site(self.path, self.job_id, label, where,
                                     via=f", in {where}, which step \"{label}\" uses,", label=step_label(inner),
                                     action=where.rsplit("/", 1)[0]), inner, inputs))
        wrapped: list = []
        for site, inner, inputs in located:
            known = {str(k): _input_text(v, inputs) for k, v in (inner.get("env") or {}).items()}
            if isinstance(inner.get("run"), str):
                self.read_run(site, inner["run"], wrapped, set(), {k: v for k, v in known.items() if v is not None})
        if uses.startswith(BUILD_ACTION):
            options = step.get("with") or {}
            context = str(options.get("context") or ".")
            file = str(options.get("file") or posixpath.join(context, "Dockerfile"))
            self.read_build(own, step, file, context, str(options.get("build-args") or ""), wrapped)
            if str(options.get("load", "")).lower() == "true":  # the image it built is on the runner now
                tags = str(options.get("tags") or "").split("\n")
                self.pulled.update(tag.strip() for tag in tags if tag.strip())
        self.hold_to_limits(step, label, wrapped)

    def read_run(self, site: Site, run: str, wrapped: list, seen: set, known: dict) -> None:
        """Each pipeline of a `run:`, a script or a RUN (shell_commands.read_shell, looked through
        WRAPPERS), with the loops it stands in: its installs refused, its wrapped installs kept in
        `wrapped` once per time it runs, each repository script it runs read the same way, once,
        and each script the resolver cannot place refused."""
        shell = read_shell(run, WRAPPERS)
        _environment(shell.assignments, site)
        found: list = []
        unfound: list = []
        for pipeline, loops in zip(shell.pipelines, shell.loops):
            for index, words in enumerate(pipeline):
                name = words[0].rsplit("/", 1)[-1]
                if name == NETWORK_INSTALL.rsplit("/", 1)[-1]:
                    self.read_wrapped(site, words, wrapped, loops, known)
                    continue
                install = self.install_of(words, site, wrapped)
                if install:
                    found.append(install)
                if name in ("curl", "wget") and name not in site.fakes and \
                        any(later == [shell_name] for later in pipeline[index + 1:] for shell_name in STDIN_SHELLS):
                    found.append(f"{name} piped into a shell")
                fed = _fetch_fed_to_shell(words)
                if fed:
                    found.append(fed)
                script = _repository_script(words[0], site)
                if script and script not in seen:
                    seen.add(script)
                    parent = f"step \"{site.label}\"" if site.where == site.path else site.where
                    self.read_run(Site(site.path, site.job_id, site.step, script,
                                       via=f"{site.via}, in {script}, which {parent} runs,".replace(",,", ","),
                                       label=site.label, base=site.base, script=script, image=site.image,
                                       workdir=site.workdir, files=site.files, action=site.action,
                                       fakes=site.fakes | _fakes(shell, words[0])),
                                  read_repository_text(REPO / script), wrapped, seen, known)
                elif script is None and _unfound_script(words[0], site):
                    unfound.append(words[0])
        for install in dict.fromkeys(found):
            self.refuse(site, f"runs `{install}` in step \"{site.label}\": an install that waits on the network "
                              f"with no bound of its own, so a mirror or a registry that stops answering holds the "
                              f"job until its limit and fails it for a cause outside the change under test. Run "
                              f"it as `{NETWORK_INSTALL} <kind> -- <command>` (from a composite action, "
                              f"\"$GITHUB_ACTION_PATH/../../scripts/network-install.sh\"), which bounds each "
                              f"attempt by that kind's measured bound, tries it three times and fails by name; a "
                              f"kind it lacks is a line of its table, with a bound measured from that install's "
                              f"own runs. A tool every run needs belongs in the runner image or in a cached, "
                              f"pinned download instead")
        for word in dict.fromkeys(unfound):
            self.refuse(site, f"runs `{word}` in step \"{site.label}\": a shell script rule 22 cannot place, so it "
                              f"cannot read the installs that script runs. Name it by its path from the step's "
                              f"directory, from $GITHUB_WORKSPACE or $GITHUB_ACTION_PATH, or from a variable the "
                              f"script sets to its own directory (`here=$(cd \"$(dirname \"$0\")\" && pwd)`)")

    def install_of(self, words: list, site: Site, wrapped: list) -> str | None:
        """The install a command makes, as network_install names it, with what the job did before it
        read in: a container of an image an earlier wrapped `docker pull` of the job pulled, and a
        Lean command after a wrapped install of its toolchain, pull nothing; a docker build reads
        its Dockerfile."""
        name = words[0].rsplit("/", 1)[-1]
        if name in site.fakes:  # the fake a test put first on the PATH it runs this script under
            return None
        if name in LEAN_COMMANDS:
            return None if self.elan else f"{name} (elan's download of the Lean toolchain lean-toolchain names)"
        if name in ("docker", "docker-compose"):
            docker = docker_command(name, words[1:])
            if docker and docker.verb == "docker build":
                self.read_docker_build(site, words, wrapped)
                return None
            if docker and docker.verb in ("docker run", "docker create") and docker.pull != "always" and \
                    any(word in self.pulled for word in words if _literal(word)):
                return None  # it names an image an earlier step left on the runner
        install = network_install(words)
        return install[0] if install else None

    def read_wrapped(self, site: Site, words: list, wrapped: list, loops: tuple, known: dict) -> None:
        """A call of a script named network-install.sh: the repository's own, as `<kind> --
        <command>`, with a kind its table bounds, wrapping an install of that kind, in a loop whose
        iterations the rule can count. An image a wrapped `docker pull` pulls is on the runner for
        the steps after it: a literal one, or each value of the loop variable it pulls."""
        count = _iterations(loops, known)
        call = " ".join(words)
        if _resolved(words[0], site) != NETWORK_INSTALL:
            self.refuse(site, f"runs `{call}` in step \"{site.label}\": a network-install.sh that rule 22 cannot "
                              f"resolve to this repository's {NETWORK_INSTALL}, whose bounds are the measured ones")
            return
        if len(words) < 4 or words[2] != "--":
            self.refuse(site, f"runs `{call}` in step \"{site.label}\": {NETWORK_INSTALL} takes "
                              f"`<kind> -- <command>`, and refuses any other call when it runs")
            return
        kind, bounds = words[1], self.bounds
        if isinstance(bounds, str):
            self.refuse(site, f"runs `{call}` in step \"{site.label}\", and rule 22 cannot read the bounds that "
                              f"call waits under: {bounds}")
            return
        if kind not in bounds:
            self.refuse(site, f"runs `{call}` in step \"{site.label}\": {NETWORK_INSTALL} bounds no kind "
                              f"'{kind}' (its kinds: {', '.join(sorted(bounds))}), and refuses it when it runs. A "
                              f"new kind is a line of its table, with a bound measured from that install's own runs")
            return
        for pipeline in command_of(list(words[3:]), WRAPPERS):
            for command in pipeline:
                install = network_install(command)
                if install and install[1] != kind and not kind.startswith(install[1] + "-"):
                    self.refuse(site, f"runs `{install[0]}` through {NETWORK_INSTALL}'s kind '{kind}' in step "
                                      f"\"{site.label}\": that bound was measured on {kind}'s installs, and "
                                      f"`{install[0]}` is of kind '{install[1]}'")
                name = command[0].rsplit("/", 1)[-1]
                docker = docker_command(name, command[1:]) if name == "docker" else None
                if docker and docker.verb == "docker pull" and command[-1:]:
                    self.pulled.update(_values(command[-1], loops, known))
                if name == "elan" and elan_fetch(command[1:]) == "elan toolchain install":
                    self.elan = True
        if count is None:
            self.refuse(site, f"runs `{call}` in step \"{site.label}\" inside a loop whose iterations rule 22 "
                              f"cannot count, so it cannot hold the job's limit to every attempt of every iteration. "
                              f"Loop over words written out whole, or over the lines of a variable the step's env "
                              f"gives as text, such as an action input its calling step names")
            return
        wrapped.append((" ".join(words[3:]), kind, worst_case(*bounds[kind]), count))

    def read_docker_build(self, site: Site, words: list, wrapped: list) -> None:
        """A `docker build` a step runs: its Dockerfile (`-f`, or the context's Dockerfile), read as
        read_build reads a docker/build-push-action step's."""
        rest = _from_verb(words[1:], DOCKER_VALUE_OPTIONS)
        rest = rest[2:] if rest[:1] in (["buildx"], ["image"]) else rest[1:]
        file, context, args, index = "", "", [], 0
        while index < len(rest):
            word = rest[index]
            if word in ("-f", "--file") and index + 1 < len(rest):
                file, index = rest[index + 1], index + 2
            elif word.startswith("--file="):
                file, index = word.split("=", 1)[1], index + 1
            elif word == "--build-arg" and index + 1 < len(rest):
                args.append(rest[index + 1])
                index += 2
            elif word.startswith("-"):
                index += 2 if word in ("-t", "--tag", "--target", "--platform", "--build-context", "--cache-from",
                                       "--cache-to", "--output", "-o", "--secret", "--ssh", "--label") else 1
            else:
                context, index = word, index + 1
        context_path = _resolved(context or ".", site)
        dockerfile = _resolved(file, site) if file else (posixpath.join(context_path, "Dockerfile")
                                                         if context_path is not None else None)
        if context_path is None or dockerfile is None or not (REPO / dockerfile).is_file():
            self.refuse(site, f"runs `{' '.join(words)}` in step \"{site.label}\": a docker build whose Dockerfile "
                              f"and context rule 22 cannot place, so it cannot read the installs the build runs")
            return
        self.read_dockerfile(site, dockerfile, context_path, "\n".join(args), wrapped)

    def read_build(self, site: Site, step: dict, file: str, context: str, build_args: str, wrapped: list) -> None:
        """A docker/build-push-action step: the Dockerfile it builds, and that the step has a limit of
        its own for what the build waits on. A recipe PINNED_RECIPES names, while it holds the bytes
        it names, has its installs counted against that limit instead."""
        context_path = _resolved(context, Site("", "", "", "")) if "${{" not in context else None
        dockerfile = _resolved(file, Site("", "", "", "")) if "${{" not in file else None
        if context_path is None or dockerfile is None or not (REPO / dockerfile).is_file():
            self.refuse(site, f"builds `{file}` in step \"{site.label}\" with a context `{context}` rule 22 cannot "
                              f"place, so it cannot read the installs the build runs")
            return
        pinned = PINNED_RECIPES.get(dockerfile) == hashlib.sha256((REPO / dockerfile).read_bytes()).hexdigest()
        if pinned and "timeout-minutes" in step:
            return
        before = len(wrapped)
        self.read_dockerfile(site, dockerfile, context_path, build_args, wrapped)
        if len(wrapped) > before and "timeout-minutes" not in step:
            took = sum(seconds * count for _, _, seconds, count in wrapped[before:])
            self.refuse(site, f"builds {dockerfile} in step \"{site.label}\", whose builder pulls and wrapped "
                              f"installs can take {took} s before the last of them fails by name, with no "
                              f"timeout-minutes of its own: give the step a limit derived from them, so a pull the "
                              f"builder makes with no bound of its own ends in a cancellation that names this step")

    def read_dockerfile(self, site: Site, dockerfile: str, context: str, build_args: str, wrapped: list) -> None:
        """Each instruction of a Dockerfile a required job builds: every FROM of an image no earlier
        step left on the runner (a wrapped pull, or a build that loaded it) is a pull the builder
        makes with no bound of its own, which counts PULL_SECONDS, once per image; and every RUN is
        read as a step's `run:` is, in its stage's WORKDIR, with the files of the repository each
        COPY and each bind mount put in the image (a COPY from the context, or
        `--mount=type=bind,source=…`, so network-install.sh runs there by that path). BuildKit builds
        the stages one stage does not need from another side by side, so what the build can spend
        is the longest chain of stages the last one needs (its FROM, a COPY --from and a RUN
        --mount from= each make a stage need another): the installs and pulls of that chain alone
        go into `wrapped`."""
        values = dict(line.split("=", 1) for line in build_args.split("\n") if "=" in line)
        stages: dict = {}  # a stage's name: its files and WORKDIR so far
        index_of: dict = {}  # a stage's name or its number: its index
        costs: list = []  # each stage's own installs and pulls
        needs: list = []  # each stage's indexes of the stages it needs
        pulled: set = set()
        files: dict = {}
        workdir = "/"
        current = None
        for line, keyword, arguments in dockerfile_instructions(read_repository_text(REPO / dockerfile)):
            flags, rest = _flags(arguments)
            if keyword == "ARG" and "=" in rest and not costs:
                name, _, value = rest.partition("=")
                values.setdefault(name.strip(), value.strip().strip('"'))
            elif keyword == "FROM":
                parts = rest.split()
                image = _substitute(parts[0], values) if parts else ""
                files = dict(stages.get(image, {}).get("files", {}))
                workdir = stages.get(image, {}).get("workdir", "/")
                costs.append([])
                needs.append({index_of[image]} if image in index_of else set())
                index_of[str(len(costs) - 1)] = len(costs) - 1
                if image not in stages and image not in self.pulled | pulled and image not in ("scratch", ""):
                    pulled.add(image)
                    costs[-1].append((f"FROM {image} (the builder's pull, at {dockerfile}:{line})", "pull",
                                      PULL_SECONDS, 1))
                if len(parts) >= 3 and parts[1].upper() == "AS":
                    stages[parts[2]] = {"files": files, "workdir": workdir}
                    index_of[parts[2]] = len(costs) - 1
                    current = parts[2]
                else:
                    current = None
            elif not costs:
                continue
            elif keyword == "WORKDIR":
                workdir = posixpath.normpath(posixpath.join(workdir, _substitute(rest, values)))
            elif keyword in ("COPY", "ADD") and any(f.startswith("--from=") for f in flags):
                source = next(f for f in flags if f.startswith("--from=")).split("=", 1)[1]
                needs[-1].update({index_of[source]} if source in index_of else set())
            elif keyword in ("COPY", "ADD"):
                words = json.loads(rest) if rest.startswith("[") else rest.split()
                if len(words) >= 2:
                    target = posixpath.join(workdir, words[-1])
                    for source in words[:-1]:
                        repository = posixpath.normpath(posixpath.join(context, source))
                        into = posixpath.join(target, posixpath.basename(source)) \
                            if words[-1].endswith("/") or len(words) > 2 else target
                        files[posixpath.normpath(into)] = repository
            elif keyword == "RUN":
                mounts = {}
                for flag in flags:
                    options = dict(item.split("=", 1) for item in flag[len("--mount="):].split(",") if "=" in item) \
                        if flag.startswith("--mount=") else {}
                    if options.get("from") in index_of:
                        needs[-1].add(index_of[options["from"]])
                    elif options.get("type") == "bind" and "source" in options:
                        mounts[posixpath.normpath(posixpath.join(workdir, options.get("target", "")))] = \
                            posixpath.normpath(posixpath.join(context, options["source"]))
                run = " ".join(json.loads(rest)) if rest.startswith("[") else rest
                self.read_run(Site(site.path, site.job_id, site.step, dockerfile,
                                   via=f"{site.via}, in {dockerfile}, which step \"{site.label}\" builds,",
                                   label=f"RUN at line {line}", image=True, workdir=workdir,
                                   files={**files, **mounts}), run, costs[-1], set(), {})
            if keyword in ("WORKDIR", "COPY", "ADD") and current:
                stages[current] = {"files": files, "workdir": workdir}
        chain: dict = {}

        def longest(index: int) -> list:
            """The installs and pulls of the longest chain of stages ending at stage `index`."""
            if index not in chain:
                chain[index] = []  # a stage that needs itself is no chain
                before = max((longest(n) for n in needs[index] if n != index),
                             key=lambda entries: sum(s * c for _, _, s, c in entries), default=[])
                chain[index] = before + costs[index]
            return chain[index]

        if costs:
            wrapped += longest(len(costs) - 1)

    def hold_to_limits(self, step: dict, label: str, wrapped: list) -> None:
        """Every install of the step must be able to fail by name inside the step's own limit, and,
        counted with what the job can spend before the step (the runner's pulls of its images, the
        limit of each step before it that sets one, and the installs of each that sets none), inside
        the job's: a limit that ends first ends the job in GitHub's cancellation, which names
        nothing. The step then adds its own limit, or its installs where it sets none, to what the
        steps after it count."""
        took = sum(seconds * count for _, _, seconds, count in wrapped)
        step_limit = _minutes(step) if "timeout-minutes" in step else None

        def each(installs: list) -> str:
            return "; ".join(f"`{command}` ({kind}) {seconds} s" if count == 1 else
                             f"`{command}` ({kind}) {count} × {seconds} s = {count * seconds} s"
                             for command, kind, seconds, count in installs)

        why = (f"each install through {NETWORK_INSTALL}'s attempts, each cut at its bound and killed after the wait "
               f"for SIGTERM, with the pauses between them, and each pull a runner or a builder makes at "
               f"{PULL_SECONDS} s, what a healthy pull takes")
        remedy = (f"A registry that stalls would end it in GitHub's cancellation, which names nothing, before "
                  f"{NETWORK_INSTALL} names the install. Give it a limit derived from what it runs, or fewer tries, "
                  f"with the reason in the notes of .github/ci-notes/")
        if wrapped and step_limit is not None and took > step_limit * 60:
            self.problems.append(error(self.path, f"job {self.job_id} of {self.path}: step \"{label}\" has "
                                                  f"timeout-minutes {step_limit}, {step_limit * 60} s, and {took} s is "
                                                  f"what the wrapped installs can take before the last of them fails "
                                                  f"by name, {why} ({each(wrapped)}). {remedy}"))
        at = self.spent + took
        if wrapped and self.limit is not None and at > self.limit * 60:
            before = "; ".join(f"{what} {seconds} s" for what, seconds, _ in self.parts)
            self.problems.append(error(self.path, f"job {self.job_id} of {self.path} has timeout-minutes {self.limit}, "
                                                  f"{self.limit * 60} s, and {at} s is what, up to step \"{label}\", the "
                                                  f"job can spend before the last of its installs fails by name: "
                                                  f"{before + ' before it; ' if before else ''}then {why} "
                                                  f"({each(wrapped)}). {remedy}"))
            self.limit = None  # the job is named once
        cost = int(step_limit * 60) if step_limit is not None else took
        if cost:
            self.spent += cost
            self.parts.append((f"step \"{label}\"'s limit" if step_limit is not None else
                               f"step \"{label}\"'s installs ({each(wrapped)})", cost, "step"))


def check_network_installs(documents: dict, problems: list) -> None:
    """No job of ci.yaml, nor of a workflow it calls, nor a local composite action one of them uses,
    nor a shell script of this repository one of those runs, nor a Dockerfile one of them builds,
    waits on the network except through NETWORK_INSTALL, and every such wait can end by name inside
    the limits of its step and its job (the module's docstring says why).

    What it reads: each pipeline of each step's `run:` (shell_commands.read_shell, looked through
    its WRAPPERS: sudo, timeout, nice, ionice, nohup, setsid, stdbuf, xargs and command, and the
    `NAME=value` words, env, interpreters, `-c` strings and `eval` each of them may put before the
    command, until none is left), with the loops it stands in; the steps of each local action a step
    uses, and of the actions those use, with the inputs the calling step gives; each shell script of
    this repository one of those commands runs, and the scripts those run, each once, found by the
    one resolver (_anchor: a path from the step's directory, $GITHUB_WORKSPACE, $GITHUB_ACTION_PATH,
    $0 and the script's own directory however written, and a variable the text sets to one of
    those); the Dockerfile each docker/build-push-action step or `docker build` builds, its FROM
    images and its RUN instructions, with the repository files its COPY and bind mounts put in the
    image; and each job's `services:` and `container:` images. A Python, TypeScript or Go program a
    step runs is out of its reach.

    What it refuses: apt, apt-get and aptitude with a verb that reads a mirror; apk adding,
    updating or fetching; pip, pip3, pipx and `python -m pip` installing; uv installing, syncing,
    making a virtual environment or running a tool (`uv pip install`, `uv sync`, `uv venv`, `uv
    run`, `uvx`, …) without its --offline; npm (with its aliases `ci`, `clean-install`, `cit`, …),
    pnpm, yarn (a bare `yarn` too) and bun (`bun ci` too) installing or adding; npx, bunx or pnpx
    fetching a package the project does not hold (`--yes`, `--package`); gem, cargo, snap and brew
    installing; a docker pull (`docker pull`, `docker image pull`, `docker compose pull`), and a
    container of an image no earlier step left on the runner (`docker run`, `docker create`,
    `docker compose up`; an image is left there by a wrapped pull, by a build that loads it, and by
    the runner's pull of the job's `services:` and `container:` images), unless it carries
    `--pull=never`; Playwright's `install`, run by bunx, npx, pnpx or `bun x`; a go command that
    names a fetch (`go mod download`, `go mod tidy`, `go mod vendor`, `go get`, `go install` or `go
    run` of a <module>@<version>); elan installing a Lean toolchain (`elan toolchain install`,
    `elan default`, …), and lake or lean before such an install, since elan then checks github.com
    for a newer elan and downloads the toolchain lean-toolchain names, each a wait with no bound of
    its own; `gh release download`; a wget that names a URL; a curl of another host's literal URL
    with no --max-time; and a curl or a wget piped into a shell, or handed to one by a substitution
    (`bash <(curl …)`, `sh -c "$(curl …)"`). timeout(1) alone is refused too: it bounds the wait,
    but neither tries it again nor names what failed. A call of NETWORK_INSTALL passes when the
    resolver places it at the repository's own script (never by the name's suffix alone), names a
    kind its table bounds (`--bounds`) and wraps no install of another installer, whose bound was
    not measured on it, and it stands in a loop whose iterations the rule can count. A shell script
    the resolver cannot place is refused by name, since the rule cannot read what it runs; one whose
    path after its first expansion names a shell script of the repository is read as that script.

    What it counts: for each job, the runner's pull of each `services:` and `container:` image at
    PULL_SECONDS (a healthy pull, measured; the runner gives the pull no bound of its own and a
    workflow can give it none, so the job's limit is its one bound); each wrapped install at
    network-install.sh's worst case, times the iterations of the loops it stands in; for a build,
    the FROM pulls and the wrapped installs of the longest chain of stages its last stage needs, a
    pull at PULL_SECONDS; and each step's own limit, or its installs where it sets none. A job or a
    step whose limit ends before its installs can all fail by name is refused, and so is a build
    step that sets no limit of its own.

    What it leaves alone, each for a reason: a `uses:` of an action this repository does not hold
    (actions/setup-go, actions/cache), whose fetches are that action's own; `mise install`, whose
    client ends a stalled wait by itself: mise 2026.8.12 bounds each request, its body included, by
    `http_timeout`'s 30 s, and against a server that sent the answer's headers and then one byte
    every 5 s, `mise install` of the jj install-jj installs ended by itself after four attempts, in
    125 s; a curl with --max-time, as .github/scripts/ci-tools.sh's ci_tools_curl makes each pinned
    download, and a curl whose URL is a variable, which the rule cannot tell from one of this
    runner's own servers; a command a test put first on the PATH of the script it runs, which is
    that test's fake (_fakes); the RUN installs of a recipe PINNED_RECIPES names, while it holds the
    bytes named there, counted against its build step's own limit; and the modules a `go build`,
    `go test`, `go vet` or `go run` of this repository's own packages downloads by itself, which
    are no step's install: pull request #4434 moves them into one fetch step per job, which this
    rule then reads, so whichever of that pull request and this rule lands second wraps that step in
    NETWORK_INSTALL."""
    for path, document in documents.items():
        for job_id, job in ((document or {}).get("jobs") or {}).items():
            reader = NetworkInstalls(path, job_id, job or {}, problems)
            for step in (job or {}).get("steps") or []:
                reader.read_step(step)
