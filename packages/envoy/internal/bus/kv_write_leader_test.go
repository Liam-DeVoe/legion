package bus

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// sendStartAllowance is how much later than the code's own clock a send that Bounded starts may
// begin and read the clock with which a row's fake notes it: three times the latest such start
// measured, the sizing docs/testing.md gives a bound of this kind. On 2026-11-20, on a dev box
// with 288 CPUs, sends timed as these fakes time them (the code's clock beside its limit's timer,
// then the fake's inside the send) started at most 14.3 ms late over 6,000,000 starts at a
// one-minute load of 645, and at most 10.1 ms late over 6,000,000 at a load of 179. Under -race
// 12 of 6,000,000 started more than 25 ms late, the latest 307 ms, so a race run of a row that
// holds a gap to this allowance can fail by lateness about twice in a million sends. A row that
// holds a gap between two sends to a limit allows each gap this much, since the limit runs from
// the code's clock and the gap from the fake's: the margin comes from what the row measures, not
// from the pause the code makes between two sends (bucketAppliedInterval), which a gap holds too.
const sendStartAllowance = 43 * time.Millisecond

// leaderReturnBound is how long after a write's leader is back a write that leader's members
// dropped meanwhile may land: one read period (firstReadLimit), within which a read of the leader
// answered after one it left unanswered cuts the send (limitedWrite), the pause before the send
// after the write's read-back (bucketAppliedInterval), and sendStartAllowance for each of the
// eight goroutine starts and wakeups between that read's going out and the send that lands (the
// read's two, its verdict's, the cut send's, the read-back's, the pause's, the next send's two). A
// write whose send was waited out to its limit landed up to that whole limit after the leader's
// return, 9.58 s after a 10 s absence when the limit had doubled to 10 s.
const leaderReturnBound = firstReadLimit + bucketAppliedInterval + 8*sendStartAllowance

// leaderGone is a cluster whose JetStream metadata leader is gone until back, measured from
// began: every stream update, consumer update and account read sent before then is dropped, with
// no answer and no landing, as at a member that is not the metadata leader (jsStreamUpdateRequest,
// jsConsumerCreateRequest and jsAccountInfoRequest, v2.15.1 server/jetstream_api.go), and each sent
// after it is applied and answered at once. Every read of the stream and of its consumer is
// answered at once, as their own leaders answer them while the metadata leader is gone
// (jsStreamInfoRequest, jsConsumerInfoRequest). No server is behind it. A dropped request returns
// when the row ends (done), as nats.go's own wait would end it; its state is under mu, since a
// send the code abandons (Bounded) runs on beside the row.
type leaderGone struct {
	nats.JetStreamContext
	back time.Duration
	done <-chan struct{}

	mu       sync.Mutex
	began    time.Time
	stream   nats.StreamConfig
	consumer nats.ConsumerConfig
	writes   []time.Duration // when each update of the stream or the consumer went out
	reads    int             // account reads sent, the code's reads of the metadata leader
	landed   time.Duration   // when the write that landed went out, or -1
}

func newLeaderGone(t *testing.T, back time.Duration) *leaderGone {
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	return &leaderGone{
		back:     back,
		done:     done,
		stream:   nats.StreamConfig{Name: "KV_envoy_leader_gone_test", MaxAge: time.Minute},
		consumer: nats.ConsumerConfig{Durable: "leader-gone", FilterSubject: "notifications.>"},
		landed:   -1,
	}
}

// start sets the clock the leader's absence is measured on.
func (js *leaderGone) start() {
	js.mu.Lock()
	defer js.mu.Unlock()
	js.began = time.Now()
}

// taken notes a request going out, a write or an account read, and reports when it went out and
// whether the metadata leader took it; a request it did not take returns only when the row ends.
func (js *leaderGone) taken(write bool) (time.Duration, bool) {
	js.mu.Lock()
	at := time.Since(js.began)
	if write {
		js.writes = append(js.writes, at)
	} else {
		js.reads++
	}
	js.mu.Unlock()
	if at < js.back {
		<-js.done
		return at, false
	}
	return at, true
}

func (js *leaderGone) StreamInfo(string, ...nats.JSOpt) (*nats.StreamInfo, error) {
	js.mu.Lock()
	defer js.mu.Unlock()
	return &nats.StreamInfo{Config: js.stream}, nil
}

func (js *leaderGone) ConsumerInfo(_, name string, _ ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	js.mu.Lock()
	defer js.mu.Unlock()
	return &nats.ConsumerInfo{Name: name, Config: js.consumer}, nil
}

func (js *leaderGone) AccountInfo(...nats.JSOpt) (*nats.AccountInfo, error) {
	if _, ok := js.taken(false); !ok {
		return nil, nats.ErrTimeout
	}
	return &nats.AccountInfo{}, nil
}

func (js *leaderGone) UpdateStream(cfg *nats.StreamConfig, _ ...nats.JSOpt) (*nats.StreamInfo, error) {
	at, ok := js.taken(true)
	if !ok {
		return nil, nats.ErrTimeout
	}
	js.mu.Lock()
	defer js.mu.Unlock()
	js.stream = *cfg
	js.land(at)
	return &nats.StreamInfo{Config: js.stream}, nil
}

func (js *leaderGone) UpdateConsumer(_ string, cfg *nats.ConsumerConfig, _ ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	at, ok := js.taken(true)
	if !ok {
		return nil, nats.ErrTimeout
	}
	js.mu.Lock()
	defer js.mu.Unlock()
	js.consumer = *cfg
	js.land(at)
	return &nats.ConsumerInfo{Config: js.consumer}, nil
}

// land notes the first write that landed, sent at at. mu is held.
func (js *leaderGone) land(at time.Duration) {
	if js.landed < 0 {
		js.landed = at
	}
}

// sent is when each write went out, how many reads of the metadata leader went out, and when the
// write that landed went out (-1 when none did).
func (js *leaderGone) sent() ([]time.Duration, int, time.Duration) {
	js.mu.Lock()
	defer js.mu.Unlock()
	return slices.Clone(js.writes), js.reads, js.landed
}

// leaderGoneWrites are the writes a start makes from a read that only the JetStream metadata
// leader answers, each run against js as a start runs it: the update of a stream that sets its
// age (updateStream) and the move of a consumer's filter off the role lanes (moveLegacyFilter).
var leaderGoneWrites = []struct {
	name string
	run  func(ctx context.Context, js *leaderGone) error
}{
	{"update", func(ctx context.Context, js *leaderGone) error {
		info, err := js.StreamInfo(js.stream.Name)
		if err != nil {
			return err
		}
		_, err = updateStream(ctx, js, info,
			func(current *nats.StreamInfo) (nats.StreamConfig, bool) {
				aged := current.Config
				aged.MaxAge = time.Hour
				return aged, true
			},
			func(again *nats.StreamInfo) bool { return again.Config.MaxAge == time.Hour })
		return err
	}},
	{"consumer move", func(ctx context.Context, js *leaderGone) error {
		return moveLegacyFilter(ctx, js, readLimitOf(ctx), writeLimitOf(ctx), js.consumer.Durable, []string{"notifications.github.>"})
	}},
}

// A write the members dropped while its leader was gone lands within one read period of that
// leader's return (leaderReturnBound), not up to its whole limit later: a member that is not the
// JetStream metadata leader drops a stream's update and a consumer's with no answer, so the
// write's limit doubles at each send, and a send waited out to its limit, 5.04 s and then 9 s,
// held the write that long after the leader was back. The write is waited for while the code reads
// the leader (metadataLeader) every read period, and a read the leader answers after one it left
// unanswered ends the wait on the send at once (limitedWrite). Here the leader is gone 3 s, 6 s and
// 10 s, the absences of the deep audit of e141a8c91d, where the update landed 1.49 s, 3.57 s and
// 9.58 s after the leader's return.
func TestAWriteTheLeaderDroppedLandsWithinAReadPeriodOfItsReturn(t *testing.T) {
	for _, write := range leaderGoneWrites {
		for _, back := range []time.Duration{3 * time.Second, 6 * time.Second, 10 * time.Second} {
			t.Run(write.name+"/"+back.String(), func(t *testing.T) {
				t.Parallel()
				js := newLeaderGone(t, back)
				js.start()
				err := write.run(withReadLimit(t.Context()), js)
				writes, reads, landed := js.sent()
				if err != nil {
					t.Fatalf("the %s after the metadata leader was gone %s = %v, want it landed; sent at %v", write.name, back, err, writes)
				}
				late := landed - back
				t.Logf("the %s landed %s after the metadata leader was back, %d sends at %v, %d reads of the leader",
					write.name, late.Round(time.Millisecond), len(writes), writes, reads)
				if late < 0 || late > leaderReturnBound {
					t.Fatalf("the %s landed %s after the metadata leader was back (sent at %v), want within %s: one read period, %s, the pause before the send after the read-back and the goroutine starts between them",
						write.name, late.Round(time.Millisecond), writes, leaderReturnBound, firstReadLimit)
				}
			})
		}
	}
}

// With its leader gone the whole time, a write is still sent no faster than its own limit: each
// send comes at least that send's limit after the send before it, the limit doubling from
// firstReadLimit up to writeLimitCeiling, and the reads of the leader that a send waiting past one
// read period makes (limitedWrite), none of which the leader answers, send nothing early. The
// wait ends at its context after 12 s, with five sends at most, at about 0, 0.66, 1.94, 4.49 and
// 9.55 s.
func TestAWriteNoLeaderAnswersIsSentNoFasterThanItsLimit(t *testing.T) {
	for _, write := range leaderGoneWrites {
		t.Run(write.name, func(t *testing.T) {
			t.Parallel()
			js := newLeaderGone(t, time.Hour)
			ctx, cancel := context.WithTimeout(withReadLimit(t.Context()), 12*time.Second)
			defer cancel()
			js.start()
			err := write.run(ctx, js)
			writes, reads, landed := js.sent()
			t.Logf("the %s no leader answered: %d sends at %v, %d reads of the leader, then %v", write.name, len(writes), writes, reads, err)
			if !errors.Is(err, context.DeadlineExceeded) || landed >= 0 {
				t.Fatalf("the %s no leader answered = %v (landed at %s), want it to end at its context with nothing landed", write.name, err, landed)
			}
			if reads == 0 {
				t.Fatalf("the %s was sent %d times and the leader read none: no send waited past a read period", write.name, len(writes))
			}
			limit := firstReadLimit
			for i := 1; i < len(writes); i++ {
				if gap := writes[i] - writes[i-1]; gap < limit-sendStartAllowance {
					t.Fatalf("the %s was sent at %v: send %d came %s after the one before it, inside that send's own limit, %s, while no leader answered",
						write.name, writes, i+1, gap.Round(time.Millisecond), limit)
				}
				limit = min(2*limit, writeLimitCeiling)
			}
			if len(writes) > 5 {
				t.Fatalf("the %s was sent %d times in 12 s, at %v, want at most five: one for each limit, 630ms, 1.26s, 2.52s, 5.04s and 9s", write.name, len(writes), writes)
			}
		})
	}
}

// slowLaneWrites applies and answers each consumer update and each purge writeDelay after its
// send, while it answers every read, of the stream, of its consumer and of the account, at once:
// the server of slowWritesJetStream, for the two other writes a start makes from a read. A purge
// empties the stream's messages on the lane it names. No server is behind it; sent is when each
// write went out, measured from began.
type slowLaneWrites struct {
	nats.JetStreamContext
	began    time.Time
	mu       sync.Mutex
	consumer nats.ConsumerConfig
	lane     uint64
	sent     []time.Duration
}

func (js *slowLaneWrites) AccountInfo(...nats.JSOpt) (*nats.AccountInfo, error) {
	return &nats.AccountInfo{}, nil
}

func (js *slowLaneWrites) StreamInfo(_ string, opts ...nats.JSOpt) (*nats.StreamInfo, error) {
	js.mu.Lock()
	defer js.mu.Unlock()
	info := &nats.StreamInfo{}
	for _, opt := range opts {
		if request, ok := opt.(*nats.StreamInfoRequest); ok && request.SubjectsFilter != "" && js.lane > 0 {
			info.State.Subjects = map[string]uint64{request.SubjectsFilter: js.lane}
		}
	}
	return info, nil
}

func (js *slowLaneWrites) ConsumerInfo(_, name string, _ ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	js.mu.Lock()
	defer js.mu.Unlock()
	return &nats.ConsumerInfo{Name: name, Config: js.consumer}, nil
}

// note notes a write going out and holds it writeDelay.
func (js *slowLaneWrites) note() {
	js.mu.Lock()
	js.sent = append(js.sent, time.Since(js.began))
	js.mu.Unlock()
	time.Sleep(writeDelay)
}

func (js *slowLaneWrites) UpdateConsumer(_ string, cfg *nats.ConsumerConfig, _ ...nats.JSOpt) (*nats.ConsumerInfo, error) {
	js.note()
	js.mu.Lock()
	defer js.mu.Unlock()
	js.consumer = *cfg
	return &nats.ConsumerInfo{Config: js.consumer}, nil
}

func (js *slowLaneWrites) PurgeStream(string, ...nats.JSOpt) error {
	js.note()
	js.mu.Lock()
	defer js.mu.Unlock()
	js.lane = 0
	return nil
}

// A consumer move and a purge the server answers later than the first limit, beside reads it
// answers at once, are each sent again only after their own limit, as an update is
// (TestAWriteTheServerAnswersLaterThanItsReadsIsSentAgainOnlyAfterItsOwnLimit): each goes out under
// the run of writes (writeLimitOf), which a read's answer never sets back. Here each lands, and is
// answered, 2 s after its send, so it is sent at most three times, at about 0, 0.66 and 1.94 s. A
// move or a purge sent under the run of reads went again after every read limit, at 0, 0.66, 1.31
// and 1.97 s.
func TestAMoveAndAPurgeTheServerAnswersLaterThanItsReadsAreSentAgainOnlyAfterTheirOwnLimit(t *testing.T) {
	for _, write := range []struct {
		name string
		run  func(ctx context.Context, js *slowLaneWrites) error
	}{
		{"consumer move", func(ctx context.Context, js *slowLaneWrites) error {
			return moveLegacyFilter(ctx, js, readLimitOf(ctx), writeLimitOf(ctx), "slow-move", []string{"notifications.github.>"})
		}},
		{"purge", func(ctx context.Context, js *slowLaneWrites) error {
			return purgeRoleLane(ctx, js, roleLaneSubjects[0])
		}},
	} {
		t.Run(write.name, func(t *testing.T) {
			t.Parallel()
			js := &slowLaneWrites{consumer: nats.ConsumerConfig{Durable: "slow-move", FilterSubject: "notifications.>"}, lane: 3}
			ctx := withReadLimit(t.Context())
			js.began = time.Now()
			if err := write.run(ctx, js); err != nil {
				t.Fatalf("the %s the server answers after %s = %v, want it landed", write.name, writeDelay, err)
			}
			js.mu.Lock()
			sent := slices.Clone(js.sent)
			js.mu.Unlock()
			t.Logf("the %s the server answers after %s was sent %d times, at %v", write.name, writeDelay, len(sent), sent)
			limit := firstReadLimit
			for i := 1; i < len(sent); i++ {
				if gap := sent[i] - sent[i-1]; gap < limit-sendStartAllowance {
					t.Fatalf("the %s was sent at %v: send %d came %s after the one before it, inside that send's own limit, %s", write.name, sent, i+1, gap.Round(time.Millisecond), limit)
				}
				limit = min(2*limit, writeLimitCeiling)
			}
			if len(sent) > 3 {
				t.Fatalf("the %s was sent %d times, at %v, want at most three: one send for each write limit, 630ms, 1.26s and 2.52s, the last of which outlasts the %s answer", write.name, len(sent), sent, writeDelay)
			}
			if reads := readLimitOf(ctx).limit; reads != firstReadLimit {
				t.Fatalf("the run of reads ended at a limit of %s after reads answered at once, want %s: the writes set the reads' limit", reads, firstReadLimit)
			}
		})
	}
}
