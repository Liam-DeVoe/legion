#!/usr/bin/env bash
# export-sessions.sh writes the writer sessions of INFRA-3231's dataset out of the transcript
# store, for the harness to read beside each case (spec R1). It runs on a machine whose `tq` reads
# the store; a Legion pod cannot reach the store today.
#
#   export-sessions.sh --sessions sessions.tsv --exclude exclude.txt --out <dir>
#
# --sessions  one line per session: "<session id>\t<cut time, RFC 3339>". `writing-check dataset
#             build` writes it as sessions.tsv. A session that writes several cases appears once,
#             with the latest cut time of its cases.
# --exclude   one session id per line: the sessions to leave out (the chief of staff's HDO
#             sessions, which its own rules name). None of their rows is read.
# --out       an empty directory. The script writes parts part-NNN.jsonl.gz (each at most 24 MiB)
#             and manifest.json, then prints the rows found for each session.
#
# Each row is one message part of the store's messages table, read with
# `tq sql --format JSONEachRow`: session_id, host, seq, ts, role, kind, text, raw_file, raw_line.
# Only rows at or before the session's cut time are read, and no row of kind `file` (the files
# beside a transcript, such as a tool's whole output, which are not the session itself). The query
# reads DISTINCT rows: the store keeps a second copy of a part loaded again after its
# deduplication window (transcript-store/README.md, "Tables"), and raw_file and raw_line keep
# two messages that share their text apart. A session the store holds no row of is reported with
# 0 rows; it is never read as an empty session, and nothing writes a part for it.
#
# The environment variable TQ names the tq command (default: tq), and BATCH the number of
# sessions one query reads (default: 50).
set -euo pipefail

usage() {
	echo "usage: export-sessions.sh --sessions <sessions.tsv> --exclude <exclude.txt> --out <dir>" >&2
	exit 2
}

sessions="" exclude="" out=""
while [ $# -gt 0 ]; do
	case "$1" in
	--sessions) sessions="${2:-}"; shift 2 ;;
	--exclude) exclude="${2:-}"; shift 2 ;;
	--out) out="${2:-}"; shift 2 ;;
	*) usage ;;
	esac
done
[ -n "$sessions" ] && [ -n "$exclude" ] && [ -n "$out" ] || usage
[ -r "$sessions" ] || { echo "export-sessions: cannot read $sessions" >&2; exit 2; }
[ -r "$exclude" ] || { echo "export-sessions: cannot read $exclude" >&2; exit 2; }
command -v python3 >/dev/null || { echo "export-sessions: python3 is required" >&2; exit 2; }
tq="${TQ:-tq}"
command -v "$tq" >/dev/null || { echo "export-sessions: $tq is not on PATH" >&2; exit 2; }
batch="${BATCH:-50}"
mkdir -p "$out"
if [ -n "$(ls -A "$out")" ]; then
	echo "export-sessions: $out is not empty" >&2
	exit 2
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The session list, checked: every id is a UUID and every cut time an RFC 3339 UTC time, so no
# text from either file can change the SQL the script builds. Excluded sessions are dropped here.
python3 - "$sessions" "$exclude" "$work/wanted.tsv" "$work/excluded.txt" <<'PY'
import re, sys
uuid = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
when = re.compile(r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$")
sessions_path, exclude_path, wanted_path, excluded_path = sys.argv[1:]
excluded = set()
for n, line in enumerate(open(exclude_path), 1):
    line = line.strip()
    if not line:
        continue
    if not uuid.match(line):
        sys.exit(f"export-sessions: {exclude_path}:{n} is not a session id")
    excluded.add(line)
cuts = {}
for n, line in enumerate(open(sessions_path), 1):
    line = line.rstrip("\n")
    if not line:
        continue
    parts = line.split("\t")
    if len(parts) != 2 or not uuid.match(parts[0]) or not when.match(parts[1]):
        sys.exit(f"export-sessions: {sessions_path}:{n} is not '<session id>\\t<RFC 3339 UTC time>'")
    sid, cut = parts
    cuts[sid] = max(cuts.get(sid, cut), cut)
left_out = sorted(s for s in cuts if s in excluded)
with open(wanted_path, "w") as w:
    for sid in sorted(cuts):
        if sid not in excluded:
            w.write(f"{sid}\t{cuts[sid]}\n")
with open(excluded_path, "w") as w:
    for sid in left_out:
        w.write(sid + "\n")
PY

# Read the rows, one query per batch of sessions, into one JSONL file.
: >"$work/rows.jsonl"
split -l "$batch" "$work/wanted.tsv" "$work/batch-"
for b in "$work"/batch-*; do
	[ -s "$b" ] || continue
	where=$(awk -F'\t' '{ printf "%s(session_id = '\''%s'\'' AND ts <= parseDateTime64BestEffort('\''%s'\'', 9))", (NR > 1 ? " OR " : ""), $1, $2 }' "$b")
	"$tq" sql --format JSONEachRow \
		"SELECT DISTINCT session_id, host, seq, ts, role, kind, text, raw_file, raw_line FROM messages WHERE kind != 'file' AND ($where) ORDER BY session_id, seq, raw_file, raw_line" \
		>>"$work/rows.jsonl"
done

# Parts: split the uncompressed rows at 24 MiB, on line boundaries, so each gzipped part is
# smaller than that and under Dispatch's 25 MiB file limit.
if [ -s "$work/rows.jsonl" ]; then
	(cd "$work" && split -C 24m -d -a 3 rows.jsonl part-)
	for p in "$work"/part-*; do
		gzip -n -9 <"$p" >"$out/$(basename "$p").jsonl.gz"
	done
fi

python3 - "$work/wanted.tsv" "$work/excluded.txt" "$work/rows.jsonl" "$out" <<'PY'
import hashlib, json, os, sys
wanted_path, excluded_path, rows_path, out = sys.argv[1:]
counts = {line.split("\t")[0]: 0 for line in open(wanted_path) if line.strip()}
with open(rows_path) as rows:
    for line in rows:
        if not line.strip():
            continue
        sid = json.loads(line)["session_id"]
        if sid not in counts:
            sys.exit(f"export-sessions: the store answered a row of session {sid}, which was not asked for")
        counts[sid] += 1
parts = []
for name in sorted(os.listdir(out)):
    if not name.endswith(".jsonl.gz"):
        continue
    data = open(os.path.join(out, name), "rb").read()
    parts.append({"name": name, "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()})
excluded = [line.strip() for line in open(excluded_path) if line.strip()]
manifest = {
    "schema": "infra-3231-session-export/1",
    "columns": ["session_id", "host", "seq", "ts", "role", "kind", "text", "raw_file", "raw_line"],
    "parts": parts,
    "rows_by_session": counts,
    "sessions_missing": sorted(s for s, n in counts.items() if n == 0),
    "sessions_left_out": len(excluded),
}
with open(os.path.join(out, "manifest.json"), "w") as f:
    json.dump(manifest, f, indent=1, sort_keys=True)
    f.write("\n")
for sid in sorted(counts):
    print(f"{sid}\t{counts[sid]}")
print(f"sessions: {len(counts)}, with rows: {sum(1 for n in counts.values() if n)}, "
      f"missing: {len(manifest['sessions_missing'])}, left out: {len(excluded)}, parts: {len(parts)}",
      file=sys.stderr)
PY
