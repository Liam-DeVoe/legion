package store

import (
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// A write phase past the send bound goes as several batches (sendParts): every statement once and
// in its order, each batch within the bound unless it is one statement past it alone, and a batch
// cut only where the next statement would take it past the bound.
func TestSendPartsKeepsEveryStatementInOrderWithinTheBound(t *testing.T) {
	const bound = 8
	sizes := []int{3, 4, 2, 9, 1, 1, 12, 5, 8, 0}
	queued := make([]*pgx.QueuedQuery, len(sizes))
	for index, size := range sizes {
		queued[index] = &pgx.QueuedQuery{SQL: "select $1", Arguments: []any{strings.Repeat("x", size)}}
	}
	partSize := func(part []*pgx.QueuedQuery) int {
		total := 0
		for _, query := range part {
			total += argumentBytes(query.Arguments[0])
		}
		return total
	}

	parts := sendParts(queued, bound)
	if sent := slices.Concat(parts...); !slices.Equal(sent, queued) {
		t.Fatalf("the parts send %d statements, want the %d queued, each once and in order", len(sent), len(queued))
	}
	for index, part := range parts {
		if len(part) == 0 {
			t.Fatalf("part %d is empty", index)
		}
		if size := partSize(part); size > bound && len(part) > 1 {
			t.Errorf("part %d carries %d bytes in %d statements, past the bound of %d", index, size, len(part), bound)
		}
		if index+1 < len(parts) && partSize(part)+partSize(parts[index+1][:1]) <= bound {
			t.Errorf("part %d ends at %d bytes although the next statement fits under the bound of %d", index, partSize(part), bound)
		}
	}
	if len(parts) < 2 {
		t.Fatalf("the parts are %d, want the phase cut", len(parts))
	}
}
