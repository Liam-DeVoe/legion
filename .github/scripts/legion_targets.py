"""The legion targets of .github/go-release-targets.json, for ci_job_reads.py's model of the go
commands CI runs.

`release-push.sh legion-targets` is the read of that list that every script and workflow step of
this repository runs, and its header names this module as the one other reader. The two must
not disagree unseen: release-push.test.sh gives both the repository's list and each form
legion-targets refuses, and wants the same lines from the first and a refusal from both for
each of the others.
"""
import json
from pathlib import Path


def legion_targets(path: str) -> list:
    """The legion targets the file at path lists, in the list's order."""
    return json.loads(Path(path).read_text())["legion"]
