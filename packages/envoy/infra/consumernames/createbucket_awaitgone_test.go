package consumernames

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// After the delete of a cut restore's leftover, a clustered hub can still answer STREAM.INFO with
// the stream for a moment, while the meta group applies the removal: a -race run of
// TestACompletedRecoveryAdopted's three-server case read it present once right after the delete
// answered, and its rerun stopped UNKNOWN, where four runs of the same case passed. So the wait
// reads a stream that still answers present again, as it reads again a read the hub does not
// answer, until its deadline; only a stream still present at the deadline is UNKNOWN, naming the
// bucket, and any other failed read ends it with that error. Control: the wait that stopped at
// the first present answer (the case "present twice, then absent").
func TestTheLeftoversDeleteIsReadAgainWhileItStillReadsPresent(t *testing.T) {
	errOther := errors.New("the hub refused the read")
	for _, c := range []struct {
		name    string
		answers []error // nil: absent; errPresent: present; any other: the read's error
		within  time.Duration
		want    string // "" for nil, else text the error holds
		reads   int
	}{
		{"present twice, then absent", []error{errPresent, errPresent, nil}, 10 * time.Second, "", 3},
		{"a timeout, then absent", []error{nats.ErrTimeout, nil}, 10 * time.Second, "", 2},
		{"present until the deadline", []error{errPresent, errPresent, errPresent, errPresent, errPresent}, 1500 * time.Millisecond, "still present after its delete", 0},
		{"another failed read", []error{errOther}, 10 * time.Second, errOther.Error(), 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			reads := 0
			read := func(context.Context) (bool, error) {
				answer := c.answers[min(reads, len(c.answers)-1)]
				reads++
				if errors.Is(answer, errPresent) {
					return true, nil
				}
				return false, answer
			}
			err := createBucketAwaitGone(context.Background(), read, time.Now().Add(c.within))
			if c.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, c.want)
			}
			if c.reads > 0 {
				require.Equal(t, c.reads, reads, "the wait's reads")
			}
		})
	}
}

// errPresent stands, in an answer list above, for a read that finds the stream present.
var errPresent = errors.New("present")
