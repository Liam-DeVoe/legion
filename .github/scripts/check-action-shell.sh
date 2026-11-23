#!/usr/bin/env bash
# Runs shellcheck at warning severity over the shell the workflows hold outside their own files:
# each `run:` body of a composite action under .github/actions/ whose shell is bash or sh, and each
# script of .github/scripts/ but the test harnesses (`*.test.sh`, `*test-lib.sh`). actionlint gives
# each inline run: body of a workflow file to shellcheck and reads no action and no script, so the
# step text R9 moved out of the workflows into actions and scripts would otherwise lose that check.
#
# A body's `${{ … }}` expressions are replaced by underscores of the same length before shellcheck
# reads it, as actionlint replaces them, and the three findings actionlint leaves out of a run:
# body for that reason are left out of a body here too: SC2050 and SC2194, a comparison or a case
# word an expression made constant, and SC2154, a variable the step's `env:` sets. A script is
# read whole. SHELLCHECK_IMAGE, where it is set, names the image whose shellcheck runs (lint gives
# it the actionlint image .github/ci-images.yaml pins), with the checkout mounted; else
# `shellcheck` on PATH runs. Each finding prints under the file, and the step for a body, and the
# check fails when any body or script has one.
#
# Run from anywhere: .github/scripts/check-action-shell.sh
# CI runs it in ci.yaml's lint job (.github/actions/lint/lint), and its test in the scripts job.
set -euo pipefail

cd "$(dirname "$0")/../.."
if [ -n "${SHELLCHECK_IMAGE:-}" ]; then
  shellcheck=(docker run --rm -v "$PWD:/repo" -w /repo --entrypoint shellcheck "$SHELLCHECK_IMAGE")
else
  shellcheck=(shellcheck)
fi
# Under the checkout, so a container that mounts it reads the bodies too.
work=$(mktemp -d -p . .action-shell.XXXXXX)
trap 'rm -rf "$work"' EXIT

# One file per body, and a listing of `<file>\t<where>` naming each, written by the YAML reader.
PYTHONDONTWRITEBYTECODE=1 python3 - "$work" <<'PY'
import pathlib
import re
import sys

import yaml

work = pathlib.Path(sys.argv[1])
listing = []
for index, path in enumerate(sorted(pathlib.Path(".github/actions").rglob("action.y*ml"))):
    document = yaml.safe_load(path.read_text()) or {}
    for number, step in enumerate(((document.get("runs") or {}).get("steps") or []), 1):
        if not isinstance(step, dict) or "run" not in step:
            continue
        if str(step.get("shell", "bash")).split()[0] not in ("bash", "sh"):
            continue
        body = re.sub(r"\$\{\{.*?\}\}", lambda m: "_" * len(m.group(0)), str(step["run"]), flags=re.S)
        name = f"{work.as_posix()}/{index}-{number}.sh"
        pathlib.Path(name).write_text("#!/usr/bin/env bash\n" + body)
        listing.append(f"{name}\t{path} step \"{step.get('name') or step.get('id') or number}\"")
(work / "listing").write_text("".join(line + "\n" for line in listing))
PY

mapfile -t bodies < <(cut -f1 "$work/listing")
scripts=()
for script in .github/scripts/*.sh; do
  case $script in *.test.sh | *test-lib.sh) continue ;; esac
  scripts+=("$script")
done

failed=0
# body <shellcheck output line>: the line with the body's file named by its action and step.
where() {
  local line=$1 file
  file=${line%%:*}
  printf '%s:%s\n' "$(grep -F -- "$file"$'\t' "$work/listing" | cut -f2)" "${line#*:}"
}
if ! found=$("${shellcheck[@]}" -S warning -f gcc --exclude=SC2050,SC2194,SC2154 "${bodies[@]}" 2>&1); then
  failed=1
  while IFS= read -r line; do
    [ -n "$line" ] && echo "::error::$(where "$line")"
  done <<<"$found"
fi
if ! found=$("${shellcheck[@]}" -S warning -f gcc "${scripts[@]}" 2>&1); then
  failed=1
  while IFS= read -r line; do
    [ -n "$line" ] && echo "::error file=${line%%:*}::$line"
  done <<<"$found"
fi
if [ "$failed" = 1 ]; then
  exit 1
fi
echo "shellcheck finds nothing at warning severity in the ${#bodies[@]} run: bodies of .github/actions or the ${#scripts[@]} scripts of .github/scripts"
