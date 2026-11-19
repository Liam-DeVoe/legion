#!/usr/bin/env python3
"""Run a `go test` command with each test binary in a PID and mount namespace of its own, whose new
/proc lists that binary's processes and none of the machine's, and fail it unless every package
that has tests ran there.

The machine's /proc lists every process of every user on it, so a test whose code under test judges
by what it finds there passes or fails by what else the machine runs. packages/daemon refuses such
a listing at each of its scanners (packages/daemon/internal/hostproc); this script holds the routes
no scanner sees, a raw read of the directory, a joined glob, an opened file handed to a helper, a
program such as pgrep, by giving each test binary a /proc that holds none of the machine's
processes. CI runs every test binary of packages/daemon under it (docs/testing.md, "A test that
lists the machine's /proc").

Usage, from the module's directory:

    ../../.github/scripts/private_proc.py go test -count=1 ./internal/launcher/

It runs the command with `-exec` set to this script, so go runs each test binary through it. For
each binary it runs, as root through `sudo -n`,

    unshare --pid --fork --mount-proc --kill-child -- <python> private_proc.py --init ...

whose PID 1 is this script's supervisor. The supervisor checks that it is PID 1, that the /proc it
sees is its namespace's own (its self link names 1) and that it lists no process but itself, then
starts the test binary beneath it as the user, groups, directory and environment go gave it, passes
it every signal it is sent, reaps every orphan of the namespace as a container's first process
does, and exits with the binary's status once the binary exits, which ends every process of the
namespace with it. The test binary is no PID 1, so a launcher test that asks whether its process is
the container's first is answered as on any machine, and a test that wants a PID 1 of its own still
makes one inside (internal/launcher's runAsPIDOne).

A namespace that cannot be made, or a supervisor that finds itself in no new one, fails that test
binary: nothing runs on the machine's /proc instead. Each supervisor that starts a binary writes the
binary's package directory to a record, and after the command this script holds the record to
`go list` over the same packages and build flags: every package with a test file must have run in a
namespace of its own, and a command that ran none, or a list with no package in it, fails. A go
command that fails keeps its own exit status.

It has no options of its own: everything after the script name is the go command, which must be
`go test` and must not set -exec itself.
"""

import os
import shlex
import signal
import subprocess
import sys
import tempfile
from pathlib import Path

SCRIPT = Path(__file__).resolve()

# The go test flags whose value is the next word, as `go help testflag` and `go help build` list
# them: a word after one of these is its value and no package.
VALUE_FLAGS = frozenset({
    "-bench", "-benchtime", "-blockprofile", "-blockprofilerate", "-count", "-coverpkg", "-coverprofile",
    "-covermode", "-cpu", "-cpuprofile", "-exec", "-fuzz", "-fuzzcachedir", "-fuzzminimizetime", "-fuzztime",
    "-gcflags", "-ldflags", "-list", "-memprofile", "-memprofilerate", "-mutexprofile", "-mutexprofilefraction",
    "-o", "-outputdir", "-overlay", "-p", "-parallel", "-pkgdir", "-run", "-shuffle", "-skip", "-tags",
    "-timeout", "-trace", "-vet", "-asmflags", "-mod", "-modfile", "-pgo", "-toolexec", "-C",
})

# The flags that change which files a package's test build holds, and so which packages have a
# test file to run: each is handed to `go list` as the command gave it.
BUILD_FLAGS = frozenset({"-tags", "-race"})

# What the supervisor exits with when it finds itself outside a namespace of its own.
NOT_PRIVATE = 125


def say(line: str) -> None:
    print(f"private-proc: {line}", file=sys.stderr, flush=True)


def split_go_test(args: list[str]) -> tuple[list[str], list[str]]:
    """The build flags `go list` must be given and the packages of a `go test` command's arguments
    (the words after `go test`): a word that starts with `-` is a flag, a VALUE_FLAGS word takes the
    next word as its value, `-args` ends the command's own words, and every other word is a package.
    A command naming no package tests the package in its directory."""
    build, packages, index = [], [], 0
    while index < len(args):
        word = args[index]
        if word == "-args":
            break
        if word.startswith("-"):
            name = word.split("=", 1)[0].replace("--", "-", 1)
            takes = name in VALUE_FLAGS and "=" not in word
            given = [word, args[index + 1]] if takes and index + 1 < len(args) else [word]
            if name in BUILD_FLAGS:
                build += given
            index += len(given)
            continue
        packages.append(word)
        index += 1
    return build, packages or ["."]


def expected_dirs(build: list[str], packages: list[str]) -> set[str]:
    """Every package directory of the command that holds a test file, as go list names it."""
    listed = subprocess.run(
        ["go", "list", *build, "-f", "{{if or .TestGoFiles .XTestGoFiles}}{{.Dir}}{{end}}", *packages],
        capture_output=True, text=True, check=False)
    if listed.returncode != 0:
        raise RuntimeError(f"go list exited {listed.returncode}: {listed.stderr.strip()}")
    return {os.path.realpath(line) for line in listed.stdout.splitlines() if line.strip()}


def run_command(argv: list[str]) -> int:
    """The wrapper: go test with -exec set to this script, then the record held to go list."""
    if argv[:2] != ["go", "test"]:
        say(f"give a go test command, for example: private_proc.py go test ./...; got {shlex.join(argv)}")
        return 2
    rest = argv[2:]
    if any(word == "-exec" or word.startswith("-exec=") for word in rest):
        say("the command sets -exec itself, which would run its test binaries outside a namespace of their own")
        return 2
    build, packages = split_go_test(rest)
    try:
        expected = expected_dirs(build, packages)
    except RuntimeError as error:
        say(f"cannot list the command's packages: {error}")
        return 1
    if not expected:
        say(f"no package of {shlex.join(packages)} has a test file: a list with nothing in it runs nothing, which is no pass")
        return 1
    fd, record = tempfile.mkstemp(prefix="private-proc-record-")
    os.close(fd)
    try:
        env = dict(os.environ, PRIVATE_PROC_RECORD=record)
        exec_word = shlex.join([sys.executable, str(SCRIPT), "--run"])
        code = subprocess.run(["go", "test", "-exec", exec_word, *rest], env=env, check=False).returncode
        ran = {os.path.realpath(line) for line in Path(record).read_text().splitlines() if line.strip()}
    finally:
        os.unlink(record)
    missing = sorted(expected - ran)
    if code != 0:
        if missing:
            say(f"{len(missing)} of the {len(expected)} packages with tests did not run, the go command having failed: "
                + ", ".join(missing))
        return code
    if not ran or missing:
        say(f"{len(ran)} test binaries ran in a namespace of their own, and {len(missing)} of the {len(expected)} packages "
            f"with tests did not: {', '.join(missing) or '(none listed)'}")
        return 1
    say(f"{len(ran)} test binaries ran, each beneath PID 1 of a PID namespace of its own, of the {len(expected)} "
        "packages with tests")
    return 0


def run_binary(argv: list[str]) -> int:
    """What go runs in place of a test binary: the binary, beneath this script's supervisor, in a
    PID and mount namespace of its own. The environment goes to the supervisor in a file only this
    user and root can read, rather than on its command line, which every user of the machine can."""
    if not argv:
        say("--run names no test binary")
        return 2
    record = os.environ.get("PRIVATE_PROC_RECORD", "")
    if not record:
        say("PRIVATE_PROC_RECORD is not set: run the go command through this script, not the binary alone")
        return 2
    fd, environ = tempfile.mkstemp(prefix="private-proc-env-")
    with os.fdopen(fd, "wb") as file:
        file.write(b"\0".join(f"{name}={value}".encode() for name, value in os.environ.items()))
    groups = ",".join(str(group) for group in os.getgroups()) or str(os.getgid())
    command = ["sudo", "-n", "unshare", "--pid", "--fork", "--mount-proc", "--kill-child", "--",
               sys.executable, str(SCRIPT), "--init", environ, str(os.getuid()), str(os.getgid()), groups,
               os.getcwd(), record, "--", *argv]
    # sudo runs as a child this process waits for, rather than in its place, so the file of the
    # environment is gone however the namespace ends, one that could not be made included, and
    # the signal go sends a binary past its deadline reaches sudo, which passes it on.
    try:
        process = subprocess.Popen(command)
    except OSError as error:
        os.unlink(environ)
        say(f"cannot run sudo to make the namespace: {error}")
        return 1
    for number in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP, signal.SIGQUIT):
        signal.signal(number, lambda received, _frame: process.send_signal(received))
    code = process.wait()
    if os.path.exists(environ):
        os.unlink(environ)
        say(f"the namespace for {argv[0]} was not made (sudo exited {code}); nothing ran on the machine's /proc instead")
        return code or 1
    return code if code >= 0 else 128 - code


def private_namespace() -> str:
    """'' when this process is PID 1 of a PID namespace whose /proc is its own and lists no other
    process; else what is wrong."""
    if os.getpid() != 1:
        return f"the supervisor is pid {os.getpid()}, not PID 1 of a namespace of its own"
    self_link = os.readlink("/proc/self")
    if self_link != "1":
        return f"/proc/self names {self_link}: this /proc is an outer namespace's, not this one's"
    others = sorted(int(name) for name in os.listdir("/proc") if name.isdigit() and name != "1")
    if others:
        return f"/proc lists {others} beside the supervisor: it is no new /proc"
    return ""


def supervise(argv: list[str]) -> int:
    """PID 1 of the namespace: check it, start the test binary as its user, forward signals, reap
    every orphan, and exit with the binary's status."""
    environ, uid, gid, groups, directory, record, separator, *binary = argv
    if separator != "--" or not binary:
        say("--init takes its six words, then --, then the test binary")
        return 2
    try:
        data = Path(environ).read_bytes()
    finally:
        os.unlink(environ)
    wrong = private_namespace()
    if wrong:
        say(f"no namespace of its own for {binary[0]}: {wrong}")
        return NOT_PRIVATE
    env = dict(entry.split("=", 1) for entry in data.decode().split("\0") if "=" in entry)
    with open(record, "a", encoding="utf-8") as file:
        file.write(directory + "\n")
    # The handlers come before the fork, so no signal sent the moment the binary starts is lost:
    # PID 1 of a namespace takes only the signals it has a handler for. An exec gives a caught
    # signal its default back, and the two Python ignores, SIGPIPE and SIGXFSZ, are given theirs
    # back in the child, so the binary and what it starts run as they would from a shell.
    child = 0

    def forward(received, _frame):
        if child:
            try:
                os.kill(child, received)
            except ProcessLookupError:
                pass

    for number in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP, signal.SIGQUIT, signal.SIGUSR1, signal.SIGUSR2):
        signal.signal(number, forward)
    child = os.fork()
    if child == 0:
        try:
            for number in (signal.SIGPIPE, signal.SIGXFSZ):
                signal.signal(number, signal.SIG_DFL)
            os.setgroups([int(group) for group in groups.split(",")])
            os.setgid(int(gid))
            os.setuid(int(uid))
            os.chdir(directory)
            os.execve(binary[0], binary, env)
        except OSError as error:
            say(f"cannot start {binary[0]}: {error}")
        os._exit(127)
    while True:
        try:
            pid, status = os.wait()
        except ChildProcessError:
            return 1
        if pid == child:
            code = os.waitstatus_to_exitcode(status)
            return code if code >= 0 else 128 - code


def main(argv: list[str]) -> int:
    if argv[:1] == ["--run"]:
        return run_binary(argv[1:])
    if argv[:1] == ["--init"]:
        return supervise(argv[1:])
    return run_command(argv)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
