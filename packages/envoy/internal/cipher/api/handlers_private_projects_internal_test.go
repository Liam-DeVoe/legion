// This file is package api rather than api_test because it drives the held list of GET
// /v1/private-projects directly (heldPrivateList, server.dispatchPrivateKeys): the order of two
// reads whose clock samples are equal, and a read the watch's own request cancels, which no HTTP
// call can time precisely.
package api

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
)

// TestHeldPrivateListOrdersEquallyDatedReads checks both completion orders when concurrent
// requests sample the same clock value: the answer to the later read is the one held.
func TestHeldPrivateListOrdersEquallyDatedReads(t *testing.T) {
	for _, order := range [][]uint64{{2, 1}, {1, 2}} {
		var held heldPrivateList
		since := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
		for _, read := range order {
			keys := []string{"OTHER"}
			if read == 2 {
				keys = []string{"NEW", "OTHER"}
			}
			held.keep(keys, since, read)
		}
		keys, _ := held.held()
		if !slices.Equal(keys, []string{"NEW", "OTHER"}) {
			t.Fatalf("the list held after reads finished in order %v = %v, want [NEW OTHER]", order, keys)
		}
	}
}

// privateListRead is a Dispatch part of the list that a test scripts call by call.
type privateListRead func(context.Context) ([]projectaccess.PrivateProject, error)

func (read privateListRead) PrivateProjects(ctx context.Context) ([]projectaccess.PrivateProject, error) {
	return read(ctx)
}

// TestCanceledPrivateListReadLeavesTheHeldAnswerUnchanged cancels the watch's own request
// during its read, then checks that neither the keys nor their original expiry changed.
func TestCanceledPrivateListReadLeavesTheHeldAnswerUnchanged(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	s := &server{deps: Deps{
		now: func() time.Time { return now },
		PrivateProjects: privateListRead(func(ctx context.Context) ([]projectaccess.PrivateProject, error) {
			calls++
			switch calls {
			case 1:
				return []projectaccess.PrivateProject{{Key: "OTHER", SlackChannelsPresent: true}}, nil
			case 2:
				cancel()
				return nil, ctx.Err()
			default:
				return nil, errors.New("Dispatch is unavailable")
			}
		}),
	}}
	keys, err := s.dispatchPrivateKeys(context.Background())
	if err != nil || !slices.Equal(keys, []string{"OTHER"}) {
		t.Fatalf("the initial read = %v, %v, want [OTHER]", keys, err)
	}
	now = now.Add(time.Minute)
	keys, err = s.dispatchPrivateKeys(ctx)
	if err != nil || !slices.Equal(keys, []string{"OTHER"}) {
		t.Fatalf("the canceled read = %v, %v, want the held [OTHER]", keys, err)
	}
	now = now.Add(3*time.Minute + 30*time.Second)
	if keys, err := s.dispatchPrivateKeys(context.Background()); err == nil {
		t.Fatalf("the list at its original expiry = %v, want an error: cancellation must not restart the hold", keys)
	}
}
