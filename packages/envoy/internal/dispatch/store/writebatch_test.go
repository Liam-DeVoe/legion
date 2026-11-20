package store

import (
	"errors"
	"testing"
)

// A write phase past the send bound goes as several batches (partEnd): every statement once and
// in its order, each batch within the bound unless it is one statement past it alone, and a batch
// cut only where the next statement would take it past the bound.
func TestPartsKeepEveryStatementInOrderWithinTheBound(t *testing.T) {
	const bound = 8
	sizes := []int{3, 4, 2, 9, 1, 1, 12, 5, 8, 0}
	size := func(index int) (int, error) { return sizes[index], nil }
	sum := func(from, to int) int {
		total := 0
		for _, bytes := range sizes[from:to] {
			total += bytes
		}
		return total
	}

	var ends []int
	for start := 0; start < len(sizes); {
		end, err := partEnd(start, len(sizes), bound, size)
		if err != nil {
			t.Fatalf("cut the part from statement %d: %v", start, err)
		}
		if end <= start {
			t.Fatalf("the part from statement %d ends at %d, sending nothing", start, end)
		}
		if total := sum(start, end); total > bound && end-start > 1 {
			t.Errorf("statements %d to %d carry %d bytes, past the bound of %d", start, end-1, total, bound)
		}
		if end < len(sizes) && sum(start, end+1) <= bound {
			t.Errorf("the part from statement %d ends at %d although statement %d fits under the bound of %d", start, end, end, bound)
		}
		ends = append(ends, end)
		start = end
	}
	if len(ends) < 2 || ends[len(ends)-1] != len(sizes) {
		t.Fatalf("the parts end at %v, want the phase cut and every one of its %d statements sent", ends, len(sizes))
	}
}

// A statement whose arguments fail to build ends the part at itself, so Send sends the statements
// queued before it first and their own failure comes before the build's.
func TestAFailedBuildEndsThePartAtItsStatement(t *testing.T) {
	failed := errors.New("encode the arguments")
	size := func(index int) (int, error) {
		if index == 2 {
			return 0, failed
		}
		return 1, nil
	}
	if end, err := partEnd(0, 4, 8, size); end != 2 || !errors.Is(err, failed) {
		t.Fatalf("the part ends at %d with %v, want it to end at the failed statement 2 with its failure", end, err)
	}
	if end, err := partEnd(2, 4, 8, size); end != 2 || !errors.Is(err, failed) {
		t.Fatalf("a part starting at the failed statement ends at %d with %v, want 2 and its failure, so nothing of it is sent", end, err)
	}
}
