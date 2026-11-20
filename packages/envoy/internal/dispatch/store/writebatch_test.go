package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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

// A write phase whose arguments pass the send bound goes to Postgres as several batches in a row
// (Send), and those batches together run every statement of the phase once and in the order it
// was queued, whether the statement was queued with its arguments or with a build of them: no
// statement is dropped or sent twice where one part ends and the next begins. Each statement's
// callback runs once, in that same order.
//
// Control: change `start = end` in Send to `start = end + 1`, so each part after the first
// starts one statement late, and this fails, naming statements 3, 7 and 11 as missing.
func TestAWritePhasePastTheSendBoundRunsEveryStatementOnceAndInOrder(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin a transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `create temporary table parted_send (
		position bigserial primary key,
		statement int not null,
		bytes int not null
	) on commit drop`); err != nil {
		t.Fatalf("create the table the write phase inserts into: %v", err)
	}

	// Each statement carries a payload a little over a quarter of the bound, so no part holds
	// more than three statements. Every statement shares the one payload string.
	const statements = 12
	payload := strings.Repeat("x", sendBound/4+1)
	if total := statements * len(payload); total <= 2*sendBound {
		t.Fatalf("%d statements of %d payload bytes carry %d bytes, want more than twice the send bound of %d, so that Send must cut the phase into three parts or more",
			statements, len(payload), total, sendBound)
	}

	const insert = `insert into parted_send (statement, bytes) values ($1, length($2::text))`
	var batch WriteBatch
	var ran []int
	for index := range statements {
		var queued Queued
		if index%2 == 0 {
			queued = batch.Queue("insert a statement's row", insert, index, payload)
		} else {
			queued = batch.QueueBuilt("insert a statement's row", insert, func() ([]any, error) {
				return []any{index, payload}, nil
			})
		}
		queued.Exec(func(pgconn.CommandTag) error {
			ran = append(ran, index)
			return nil
		})
	}
	if err := batch.Send(ctx, tx); err != nil {
		t.Fatalf("send the write phase: %v", err)
	}

	rows, err := tx.Query(ctx, `select statement, bytes from parted_send order by position`)
	if err != nil {
		t.Fatalf("read the rows the write phase inserted: %v", err)
	}
	defer rows.Close()
	var inserted []int
	for rows.Next() {
		var statement, bytes int
		if err := rows.Scan(&statement, &bytes); err != nil {
			t.Fatalf("scan a row the write phase inserted: %v", err)
		}
		if bytes != len(payload) {
			t.Errorf("statement %d inserted a payload of %d bytes, want %d", statement, bytes, len(payload))
		}
		inserted = append(inserted, statement)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the rows the write phase inserted: %v", err)
	}

	want := make([]int, statements)
	for index := range want {
		want[index] = index
	}
	// everyOnceInOrder fails the test where got is not every statement once and in order, and
	// names the statements missing from got and those it holds more than once.
	everyOnceInOrder := func(what string, got []int) {
		t.Helper()
		if slices.Equal(got, want) {
			return
		}
		counts := make([]int, statements)
		for _, statement := range got {
			if statement >= 0 && statement < statements {
				counts[statement]++
			}
		}
		var missing, repeated []int
		for statement, count := range counts {
			if count == 0 {
				missing = append(missing, statement)
			}
			if count > 1 {
				repeated = append(repeated, statement)
			}
		}
		t.Errorf("%s statements %v, want %v, each once and in order: missing %v, repeated %v",
			what, got, want, missing, repeated)
	}
	everyOnceInOrder("the table holds rows, in insert order, of", inserted)
	everyOnceInOrder("the callbacks ran for", ran)
}
