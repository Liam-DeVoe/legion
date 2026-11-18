#!/bin/sh
set -eu
if [ $# -gt 0 ]; then A=$1; else
  A=$(mktemp -p "${TMPDIR:-/tmp}")
  dispatch doc-read --issue INFRA-3686 --artifact notification-audit.md > "$A"
fi
K=$(mktemp -p "${TMPDIR:-/tmp}"); E=$(mktemp -p "${TMPDIR:-/tmp}")
{ sed -n '/^var validNoticeKinds/,/^}/p' packages/daemon/internal/record/outbox.go
  grep -rhE --include=*.go --exclude=*_test.go ${KINDS_FILE:+"--exclude=$(basename "$KINDS_FILE")"} 'NoticeKind *= *"' packages/daemon/internal || true
  [ -z "${KINDS_FILE:-}" ] || cat "$KINDS_FILE"
} | grep -oE '"[a-z]+(-[a-z]+)*"' | tr -d '"' | sort -u > "$K"
grep -hoE 'case "[a-z_]+\.[a-z_]+"(, "[a-z_]+\.[a-z_]+")*:' packages/envoy/internal/dispatch/outbox/publisher.go \
  | grep -oE '"[a-z_]+\.[a-z_]+"' | tr -d '"' | sort -u > "$E"
cat "$K" "$E" | while read -r k; do
  grep -qF "\`$k\`" "$A" || echo "no row: $k"
done
nk=$(wc -l < "$K"); ne=$(wc -l < "$E")
echo "read $nk kinds and $ne event types"
rm -f "$K" "$E"
[ $# -gt 0 ] || rm -f "$A"
[ "$nk" -ge "${MIN_KINDS:-25}" ] && [ "$ne" -ge "${MIN_TYPES:-10}" ]
