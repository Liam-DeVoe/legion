"""The legion targets of .github/go-release-targets.json, for ci_job_reads.py's model of the go
commands CI runs.

`release-push.sh legion-targets` is the read of that list that every script and workflow step of
this repository runs, and its header names this module as the one other reader. The two must
not disagree unseen: release-push.test.sh gives both the repository's list and each form
legion-targets refuses, and wants the same lines from the first and a refusal from both for
each of the others. So this refuses what legion-targets refuses, by the same rule.
"""
import json
import re
from pathlib import Path

# Two names of lower-case letters and digits on each side of one /, as every GOOS and GOARCH go
# knows is: legion-targets' rule.
TARGET = re.compile(r"[a-z0-9]+/[a-z0-9]+")


def legion_targets(path: str) -> list:
    """The legion targets the file at path lists, in the list's order. A ValueError naming the
    file and why, as legion-targets refuses them: a file that is not JSON, a legion key that is
    no list, an empty list, and an entry that is not <os>/<arch>."""
    try:
        document = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:
        raise ValueError(f"{path}: could not read the legion targets: {error}") from None
    listed = document.get("legion") if isinstance(document, dict) else None
    if not isinstance(listed, list):
        raise ValueError(f"{path}: could not read the legion targets: its legion key is no list")
    if not listed:
        raise ValueError(f"{path} lists no legion target")
    for entry in listed:
        if not (isinstance(entry, str) and TARGET.fullmatch(entry)):
            raise ValueError(f"{path}: the legion target {json.dumps(entry)} is not <os>/<arch>, two names of "
                             "lower-case letters and digits on each side of one /")
    return listed
