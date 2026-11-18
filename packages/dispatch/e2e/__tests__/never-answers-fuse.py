#!/usr/bin/env python3
"""A FUSE file system for a row of harness-postgres-holders.test.ts: one path in it, PATH/pgdata,
never answers a process of the row's own tree, as a path on a mount whose server hangs never
answers, while every other process gets its answer at once, so no other start on the machine
waits on it.

    usage: never-answers-fuse.py MOUNTPOINT ROW_PID

It mounts MOUNTPOINT with fusermount3, names `pgdata` in it once itself and holds it open, so the
kernel keeps that name and each later look at it asks this server again, and then prints
"mounted". A look at `pgdata` from a process whose parents include ROW_PID it never answers; one
from any other process it answers as a directory. It serves until ROW_PID ends, it is killed, or
600 s pass; its end ends each wait on it, and the row then unmounts MOUNTPOINT
(`fusermount3 -u -z`).
"""

import os
import select
import socket
import struct
import subprocess
import sys
import threading
import time

LOOKUP, FORGET, GETATTR, STATFS, INIT, OPENDIR, RELEASEDIR, INTERRUPT, BATCH_FORGET = (
    1, 2, 3, 17, 26, 27, 29, 36, 42)
NO_REPLY = {FORGET, INTERRUPT, BATCH_FORGET}
ENOENT, ENOSYS = 2, 38
ROOT, DATA = 1, 2

mountpoint, row = sys.argv[1], int(sys.argv[2])
me = os.getpid()
ends = time.monotonic() + 600

parent, child = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
subprocess.run(
    ["fusermount3", "-o", "fsname=never-answers,subtype=never-answers", "--", mountpoint],
    env=dict(os.environ, _FUSE_COMMFD=str(child.fileno())),
    pass_fds=[child.fileno()],
    check=True,
)
child.close()
_, fds, _, _ = socket.recv_fds(parent, 1, 1)
fuse = fds[0]


def attr(ino):
    # fuse_attr: ino, size, blocks, atime, mtime, ctime; their nanoseconds, mode, nlink, uid, gid,
    # rdev, blksize, flags.
    return struct.pack("<QQQQQQIIIIIIIIII", ino, 0, 0, 0, 0, 0, 0, 0, 0, 0o040755, 2,
                       os.getuid(), os.getgid(), 0, 4096, 0)


def reply(unique, error=0, body=b""):
    try:
        os.write(fuse, struct.pack("<IiQ", 16 + len(body), -error, unique) + body)
    except OSError:
        pass  # A request the kernel no longer waits for.


def of_row(pid):
    """Whether `pid`, the thread the kernel names as the asker, is of another process than this
    server's whose parents include ROW_PID, or is of ROW_PID itself: the row may be PID 1 of a PID
    namespace of its own."""
    try:
        with open(f"/proc/{pid}/status") as status:
            group = int(next(line for line in status if line.startswith("Tgid:")).split()[1])
    except (OSError, StopIteration):
        return False
    if group == me:
        return False
    while group > 0:
        if group == row:
            return True
        if group == 1:
            return False
        try:
            with open(f"/proc/{group}/stat") as stat:
                group = int(stat.read().rsplit(")", 1)[1].split()[1])
        except OSError:
            return False
    return False


def prime():
    # The server's own look at `pgdata`, which it answers: the kernel then holds the name, open.
    global held
    held = os.open(os.path.join(mountpoint, "pgdata"), os.O_RDONLY | os.O_DIRECTORY)
    print("mounted", flush=True)


held = None
while time.monotonic() < ends:
    try:
        os.kill(row, 0)
    except OSError:
        break
    if not select.select([fuse], [], [], 0.5)[0]:
        continue
    try:
        request = os.read(fuse, 1 << 20)
    except OSError:
        break
    length, opcode, unique, node, _, _, pid, _, _ = struct.unpack_from("<IIQQIIIHH", request)
    body = request[40:length]
    if opcode in NO_REPLY:
        continue
    if opcode == INIT:
        major, minor, readahead = struct.unpack_from("<III", body)
        reply(unique, body=struct.pack("<IIIIHHIIHHII24x", 7, min(minor, 31), readahead, 0,
                                       16, 12, 1 << 17, 1, 0, 0, 0, 0))
        threading.Thread(target=prime, daemon=True).start()
    elif opcode == LOOKUP and node == ROOT and body.rstrip(b"\0") == b"pgdata":
        if of_row(pid):
            continue  # Never answered.
        reply(unique, body=struct.pack("<QQQQII", DATA, 1, 0, 0, 0, 0) + attr(DATA))
    elif opcode == LOOKUP:
        reply(unique, ENOENT)
    elif opcode == GETATTR and node in (ROOT, DATA):
        reply(unique, body=struct.pack("<QII", 0, 0, 0) + attr(node))
    elif opcode == OPENDIR:
        reply(unique, body=struct.pack("<QII", 0, 0, 0))
    elif opcode == RELEASEDIR:
        reply(unique)
    elif opcode == STATFS:
        reply(unique, body=struct.pack("<QQQQQIIII24x", 0, 0, 0, 0, 0, 4096, 255, 4096, 0))
    else:
        reply(unique, ENOSYS)
