package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/leakguard"
	"github.com/sjawhar/legion/daemon/internal/runtime"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// testIndex is an index of sandbox lines as the daemon's reader answers one, built at built and
// longer than one shimwire.LauncherBundlePayloadBytes, so its push is a sequence of slices. Its
// body stands in for an index's file, since the runtime pushes the bytes it is given and parses
// none. Its list's digest is the same whatever the build time: the builder's hourly build of a
// list that did not change differs from the build before it in its header alone, and the check in
// a pod reads UNKNOWN six hours after the build time of the index it holds, so that build is a
// new index all the same.
func testIndex(built time.Time) leakguard.Index {
	header := fmt.Sprintf("{\"built_at\":%q}\n", built.UTC().Format(time.RFC3339))
	body := append([]byte(header), bytes.Repeat([]byte{0x2a}, shimwire.LauncherBundlePayloadBytes+4096)...)
	return leakguard.Index{Body: body, SHA256: strings.Repeat("ab", 32), BuiltAt: built}
}

// digestOf is the hex sha256 of body, the digest a launcher_leak_check_index sequence declares
// for the index it carries and the launcher reports installed.
func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// indexAnswer is one answer of the daemon's reader of the index.
type indexAnswer struct {
	index leakguard.Index
	err   error
}

// scriptedIndexes stands in for the daemon's reader of the index (leakguard.IndexSource): its
// first read answers the first of answers, its second the second, and every read past the last
// answers the last. It counts its reads.
type scriptedIndexes struct {
	answers []indexAnswer

	mu    sync.Mutex
	calls int
}

func (s *scriptedIndexes) Read(context.Context) (leakguard.Index, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	answer := s.answers[min(s.calls, len(s.answers)-1)]
	s.calls++
	return answer.index, answer.err
}

func (s *scriptedIndexes) reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// readsOf scripts a reader that answers each of indexes in turn.
func readsOf(indexes ...leakguard.Index) *scriptedIndexes {
	s := &scriptedIndexes{}
	for _, index := range indexes {
		s.answers = append(s.answers, indexAnswer{index: index})
	}
	return s
}

// onceCache is a reader that keeps its first read for good, as leakguard.Source keeps its read of
// a tag of the check: the reader of a daemon that read the index once and pushed that one to every
// pod for its life.
type onceCache struct {
	reader LeakCheckIndexReader
	once   sync.Once
	index  leakguard.Index
	err    error
}

func (c *onceCache) Read(ctx context.Context) (leakguard.Index, error) {
	c.once.Do(func() { c.index, c.err = c.reader.Read(ctx) })
	return c.index, c.err
}

// countedReads counts the reads made of reader, whatever reader answers.
type countedReads struct {
	reader LeakCheckIndexReader
	calls  atomic.Int32
}

func (c *countedReads) Read(ctx context.Context) (leakguard.Index, error) {
	c.calls.Add(1)
	return c.reader.Read(ctx)
}

// indexLauncher is the launcher end of one fake pod launcher connection, which answers the index
// as the pod's launcher does: it rebuilds each launcher_leak_check_index sequence, answers its last
// slice Installed once the slices rebuild the digest they declare, and answers any slice of a
// sequence whose digest it has installed Installed at once. It answers a start OK, and answers no
// slice of a saved workspace bundle. It records every frame it was sent and the digest of each
// index it installed, in order, and keeps the slices it took across its connections, as one
// launcher process does across a reconnect. refuse has it refuse each slice of the index with that
// cause, and silent has it take each slice and answer none, as a launcher whose install failed, or
// is still running when the daemon's hop ends, does. hold, when set, has it close holding as the
// first slice of the index arrives and answer that slice only once hold is closed.
type indexLauncher struct {
	refuse        string
	silent        bool
	hold, holding chan struct{}

	mu        sync.Mutex
	sent      []shimwire.Frame
	installed []string
	held      map[string]map[int][]byte
}

func (l *indexLauncher) serve(conn net.Conn) {
	writer := shimwire.NewWriter(conn)
	if writer.WriteFrame(shimwire.LauncherState{}) != nil {
		return
	}
	reader := shimwire.NewReader(conn)
	for {
		line, err := reader.ReadLine()
		if err != nil {
			return
		}
		frame, err := shimwire.Decode(line)
		if err != nil {
			return
		}
		l.mu.Lock()
		l.sent = append(l.sent, frame)
		l.mu.Unlock()
		switch command := frame.(type) {
		case shimwire.LauncherLeakCheckIndex:
			if l.silent {
				continue
			}
			if l.hold != nil {
				close(l.holding)
				<-l.hold
				l.hold = nil
			}
			_ = writer.WriteFrame(l.take(command))
		case shimwire.LauncherStart:
			_ = writer.WriteFrame(shimwire.LauncherStartResult{ID: command.ID, OK: true, RunningGeneration: command.Generation})
		}
	}
}

// take is the answer to one slice of the index.
func (l *indexLauncher) take(slice shimwire.LauncherLeakCheckIndex) shimwire.LauncherLeakCheckIndexResult {
	answer := shimwire.LauncherLeakCheckIndexResult{ID: slice.ID, Index: slice.Index}
	if l.refuse != "" {
		answer.Error = l.refuse
		return answer
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if slices.Contains(l.installed, slice.SHA256) {
		answer.OK, answer.Installed = true, true
		return answer
	}
	if l.held == nil {
		l.held = map[string]map[int][]byte{}
	}
	if l.held[slice.ID] == nil {
		l.held[slice.ID] = map[int][]byte{}
	}
	l.held[slice.ID][slice.Index] = slice.Payload
	if len(l.held[slice.ID]) < slice.Count {
		answer.OK = true
		return answer
	}
	var body []byte
	for index := range slice.Count {
		body = append(body, l.held[slice.ID][index]...)
	}
	if digestOf(body) != slice.SHA256 {
		answer.Error = "the slices rebuild an index of another digest"
		return answer
	}
	l.installed = append(l.installed, slice.SHA256)
	answer.OK, answer.Installed = true, true
	return answer
}

func (l *indexLauncher) frames() []shimwire.Frame {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.sent)
}

func (l *indexLauncher) installs() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.installed)
}

// connectIndexLauncher accepts l as token's launcher in the pod podUID, its hello saying it takes
// the index where takes is set, and serves the connection on a pipe whose launcher end l answers.
func connectIndexLauncher(t *testing.T, d *launchers, token claim.Token, podUID string, takes bool, l *indexLauncher) *launcherSession {
	t.Helper()
	session := d.accept(token, testTree, rigNow, shimwire.LauncherHello{
		PodUID: podUID, LauncherID: "launcher-of-" + podUID, LeakCheckIndex: takes,
	})
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
	go session.ServeLauncher(server, bufio.NewReader(server), shimwire.NewWriter(server))
	go l.serve(peer)
	return session
}

// indexLaunchers is a launchers of its own, outside any rig, that reads the index from reader and
// logs to log.
func indexLaunchers(reader LeakCheckIndexReader, log *lockedLog) *launchers {
	d := newLaunchers(nil, nil, nil, nil, nil, slogTo(log))
	d.leakCheckIndex = reader
	return d
}

// polledLaunchers are two launchers that take the index, each of its own pod, after the daemon's
// poll of the index has read it three times.
type polledLaunchers struct {
	launchers map[claim.Token]*indexLauncher
	sessions  map[claim.Token]*launcherSession
}

// pollTwoLaunchers runs the daemon's poll of reader, every millisecond, against two launchers that
// take the index, until the poll has read reader three times: by then the push that followed each
// of the first two reads has ended, since the poll reads again only once every push of the read
// before has.
func pollTwoLaunchers(t *testing.T, reader LeakCheckIndexReader) polledLaunchers {
	t.Helper()
	counted := &countedReads{reader: reader}
	var logs lockedLog
	d := indexLaunchers(counted, &logs)
	got := polledLaunchers{launchers: map[claim.Token]*indexLauncher{}, sessions: map[claim.Token]*launcherSession{}}
	for _, token := range []claim.Token{workerToken, otherToken} {
		l := &indexLauncher{}
		got.launchers[token] = l
		got.sessions[token] = connectIndexLauncher(t, d, token, "uid-pod-of-"+string(token), true, l)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.pollLeakCheckIndex(ctx, time.Millisecond)
	}()
	testwait.Eventually(t, "the daemon's third read of the index", func() bool { return counted.calls.Load() >= 3 })
	cancel()
	select {
	case <-done:
	case <-time.After(testwait.Bounded(t, time.Minute, time.Second)):
		t.Fatal("the poll did not end with its context")
	}
	return got
}

// tookAThenB is why the two launchers did not each install a, then b, with the daemon holding each
// of them on b, or nil when they did.
func (p polledLaunchers) tookAThenB(a, b string) error {
	var errs []error
	for token, l := range p.launchers {
		if got := l.installs(); !slices.Equal(got, []string{a, b}) {
			errs = append(errs, fmt.Errorf("%s's launcher installed %s, want %s and then %s", token, shortDigests(got), a[:12], b[:12]))
		}
		if got := p.sessions[token].installedLeakCheckIndex(); got != b {
			errs = append(errs, fmt.Errorf("the daemon holds %s's launcher on the index %q, want %s", token, got, b))
		}
	}
	return errors.Join(errs...)
}

// shortDigests names digests by their first 12 hex, as a sequence's id does.
func shortDigests(digests []string) []string {
	short := make([]string, 0, len(digests))
	for _, digest := range digests {
		short = append(short, digest[:min(12, len(digest))])
	}
	return short
}

// Each index the daemon reads reaches every launcher that said it takes one: the reader gives A,
// then B, a build of the same list an hour later, and each of two launchers installs A and then
// B, the daemon holding each on B.
func TestEachNewIndexReachesEachCapableLauncher(t *testing.T) {
	a, b := testIndex(rigNow), testIndex(rigNow.Add(time.Hour))
	got := pollTwoLaunchers(t, readsOf(a, b))
	if err := got.tookAThenB(digestOf(a.Body), digestOf(b.Body)); err != nil {
		t.Fatal(err)
	}
}

// The control of TestEachNewIndexReachesEachCapableLauncher: a reader in a once-cache, the reader
// of a daemon that read the index once, leaves both launchers on A, and the assertion on B fails
// for it.
func TestAReaderInAOnceCacheLeavesEachLauncherOnTheFirstIndex(t *testing.T) {
	a, b := testIndex(rigNow), testIndex(rigNow.Add(time.Hour))
	got := pollTwoLaunchers(t, &onceCache{reader: readsOf(a, b)})
	for token, l := range got.launchers {
		if installs := l.installs(); !slices.Equal(installs, []string{digestOf(a.Body)}) {
			t.Fatalf("%s's launcher installed %s under a once-cache, want A alone, %s", token, shortDigests(installs), digestOf(a.Body)[:12])
		}
		if held := got.sessions[token].installedLeakCheckIndex(); held != digestOf(a.Body) {
			t.Fatalf("the daemon holds %s's launcher on %q under a once-cache, want A, %s", token, held, digestOf(a.Body))
		}
	}
	if err := got.tookAThenB(digestOf(a.Body), digestOf(b.Body)); err == nil {
		t.Fatal("the assertion on B passed for a reader that read once, so it cannot tell a daemon that reads the index once from one that reads each new one")
	}
}

// A launcher whose hello did not say it takes the index — one built before the frame, which ends
// its connection on a frame it does not model — is sent no frame of it, by the poll or by a start,
// while a launcher beside it that said so is pushed the index the same read gave.
func TestALauncherThatSaidNothingGetsNoIndex(t *testing.T) {
	a := testIndex(rigNow)
	var logs lockedLog
	d := indexLaunchers(readsOf(a), &logs)
	capable, older := &indexLauncher{}, &indexLauncher{}
	connectIndexLauncher(t, d, workerToken, "uid-pod-capable", true, capable)
	connectIndexLauncher(t, d, otherToken, "uid-pod-older", false, older)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Bounded(t, time.Minute, time.Second))
	defer cancel()

	d.refreshLeakCheckIndex(ctx)
	if installs := capable.installs(); !slices.Equal(installs, []string{digestOf(a.Body)}) {
		t.Fatalf("the launcher that takes the index installed %s, want the index read, %s", shortDigests(installs), digestOf(a.Body)[:12])
	}
	if err := d.start(ctx, otherToken, "uid-pod-older", shimwire.LauncherStart{ID: "start-of-older", Generation: 1, Argv: []string{"true"}}); err != nil {
		t.Fatalf("the older launcher's start: %v", err)
	}
	frames := older.frames()
	if countOf(frames, shimwire.TypeLauncherLeakCheckIndex) != 0 || countOf(frames, shimwire.TypeLauncherStart) != 1 || len(frames) != 1 {
		t.Fatalf("the launcher that said nothing was sent %v, want its start alone", frameTypes(frames))
	}
}

// A start pushes the index the daemon holds to a launcher that takes it before anything of the
// role's generation: every slice of it, in order, and then the start.
func TestAStartPushesTheIndexBeforeItsFirstGeneration(t *testing.T) {
	a := testIndex(rigNow)
	var logs lockedLog
	d := indexLaunchers(readsOf(a), &logs)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Bounded(t, time.Minute, time.Second))
	defer cancel()
	d.refreshLeakCheckIndex(ctx)
	l := &indexLauncher{}
	session := connectIndexLauncher(t, d, workerToken, "uid-pod-new", true, l)

	if err := d.start(ctx, workerToken, "uid-pod-new", shimwire.LauncherStart{ID: "start-of-first", Generation: 1, Argv: []string{"true"}}); err != nil {
		t.Fatalf("the start: %v", err)
	}
	frames := l.frames()
	start := slices.IndexFunc(frames, func(f shimwire.Frame) bool { _, ok := f.(shimwire.LauncherStart); return ok })
	if start < 2 || start != len(frames)-1 {
		t.Fatalf("the launcher was sent %v, want the index's slices and then the start", frameTypes(frames))
	}
	var body []byte
	for i, frame := range frames[:start] {
		slice, ok := frame.(shimwire.LauncherLeakCheckIndex)
		if !ok || slice.Index != i || slice.Count != start || slice.SHA256 != digestOf(a.Body) {
			t.Fatalf("the launcher was sent %v, want slice %d of the %d of %s at position %d", frameTypes(frames), i, start, digestOf(a.Body)[:12], i)
		}
		body = append(body, slice.Payload...)
	}
	if !bytes.Equal(body, a.Body) {
		t.Fatalf("the index crossed as %d bytes, want the %d the daemon read", len(body), len(a.Body))
	}
	if held := session.installedLeakCheckIndex(); held != digestOf(a.Body) {
		t.Fatalf("after the start the daemon holds the launcher on %q, want %s", held, digestOf(a.Body))
	}
}

// One push of the index to a launcher runs at a time: a start that comes while the poll pushes the
// index to the role's launcher waits for that push to end, finds the index installed, and sends
// the start alone, so the launcher is sent each slice once and then the start. Two pushes at once
// would send one sequence twice, and their answers, whose waiters share a name, would reach the
// wrong push.
func TestAStartWaitsForThePollsPushToTheSameLauncher(t *testing.T) {
	a := testIndex(rigNow)
	var logs lockedLog
	d := indexLaunchers(readsOf(a), &logs)
	l := &indexLauncher{hold: make(chan struct{}), holding: make(chan struct{})}
	session := connectIndexLauncher(t, d, workerToken, "uid-pod-busy", true, l)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Bounded(t, time.Minute, time.Second))
	defer cancel()

	polled := make(chan struct{})
	go func() {
		defer close(polled)
		d.refreshLeakCheckIndex(ctx)
	}()
	select {
	case <-l.holding:
	case <-ctx.Done():
		t.Fatal("the poll pushed no slice of the index")
	}
	started := make(chan error, 1)
	go func() {
		started <- d.start(ctx, workerToken, "uid-pod-busy", shimwire.LauncherStart{ID: "start-while-pushing", Generation: 1, Argv: []string{"true"}})
	}()
	// A start that did not wait would send a slice of its own while the poll's first is held; it
	// is given the time to before the poll's push goes on. The test's verdict does not rest on
	// this wait: a start that waits sends nothing in it.
	time.Sleep(50 * time.Millisecond)
	close(l.hold)
	if err := <-started; err != nil {
		t.Fatalf("the start: %v", err)
	}
	<-polled

	frames := l.frames()
	if len(frames) < 3 || countOf(frames, shimwire.TypeLauncherLeakCheckIndex) != len(frames)-1 || frames[len(frames)-1].FrameType() != shimwire.TypeLauncherStart {
		t.Fatalf("the launcher was sent %v, want each slice of the index once and then the start", frameTypes(frames))
	}
	for i, frame := range frames[:len(frames)-1] {
		if slice := frame.(shimwire.LauncherLeakCheckIndex); slice.Index != i {
			t.Fatalf("the launcher was sent slice %d at position %d of %v", slice.Index, i, frameTypes(frames))
		}
	}
	if held := session.installedLeakCheckIndex(); held != digestOf(a.Body) {
		t.Fatalf("the daemon holds the launcher on %q, want %s", held, digestOf(a.Body))
	}
	if text := logs.String(); strings.Contains(text, "level=WARN") {
		t.Fatalf("the daemon logged %q, want no warning", text)
	}
}

// A push of the index that fails — the launcher refuses it, or answers nothing within the hop's
// bound — is logged as a warning naming why, and the role's start is sent and answered all the
// same: the check in that pod reads the index it holds, or UNKNOWN with none, and says why. The
// silent launcher's hop is cut to 50ms so the test does not wait out the production bound; the
// refusing one's keeps that bound, since it answers.
func TestAFailedIndexPushDoesNotRefuseTheStart(t *testing.T) {
	const refusal = "the index folder could not be written: no space left on device"
	for name, tc := range map[string]struct {
		launcher *indexLauncher
		hop      time.Duration
		want     string
	}{
		"the launcher refuses the index":            {launcher: &indexLauncher{refuse: refusal}, want: refusal},
		"the launcher answers nothing within a hop": {launcher: &indexLauncher{silent: true}, hop: 50 * time.Millisecond, want: "within 50ms"},
	} {
		t.Run(name, func(t *testing.T) {
			a := testIndex(rigNow)
			var logs lockedLog
			d := indexLaunchers(readsOf(a), &logs)
			if tc.hop != 0 {
				d.leakCheckHop = tc.hop
			}
			ctx, cancel := context.WithTimeout(context.Background(), testwait.Bounded(t, time.Minute, time.Second))
			defer cancel()
			d.refreshLeakCheckIndex(ctx)
			session := connectIndexLauncher(t, d, workerToken, "uid-pod-failing", true, tc.launcher)

			if err := d.start(ctx, workerToken, "uid-pod-failing", shimwire.LauncherStart{ID: "start-after-failure", Generation: 1, Argv: []string{"true"}}); err != nil {
				t.Fatalf("the start failed with %v, want it started after the failed push", err)
			}
			frames := tc.launcher.frames()
			if countOf(frames, shimwire.TypeLauncherStart) != 1 || countOf(frames, shimwire.TypeLauncherLeakCheckIndex) == 0 {
				t.Fatalf("the launcher was sent %v, want a slice of the index and then the start", frameTypes(frames))
			}
			if held := session.installedLeakCheckIndex(); held != "" {
				t.Fatalf("the daemon holds the launcher on %q after the failed push, want none", held)
			}
			text := logs.String()
			if !strings.Contains(text, "level=WARN") || !strings.Contains(text, tc.want) {
				t.Fatalf("the daemon logged %q, want a warning naming %q", text, tc.want)
			}
		})
	}
}

// A read that fails logs one warning and keeps the newest index read: the push after it carries
// that index to a launcher that does not hold it.
func TestAFailedReadKeepsTheNewestIndex(t *testing.T) {
	a := testIndex(rigNow)
	const failure = "GitHub answered the read of the release with HTTP 502"
	reader := &scriptedIndexes{answers: []indexAnswer{{index: a}, {err: errors.New(failure)}}}
	var logs lockedLog
	d := indexLaunchers(reader, &logs)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Bounded(t, time.Minute, time.Second))
	defer cancel()
	d.refreshLeakCheckIndex(ctx)
	l := &indexLauncher{}
	session := connectIndexLauncher(t, d, workerToken, "uid-pod-late", true, l)

	d.refreshLeakCheckIndex(ctx)
	if reads := reader.reads(); reads != 2 {
		t.Fatalf("the daemon read the index %d times, want 2", reads)
	}
	if installs := l.installs(); !slices.Equal(installs, []string{digestOf(a.Body)}) || session.installedLeakCheckIndex() != digestOf(a.Body) {
		t.Fatalf("after the failed read the launcher installed %s and the daemon holds it on %q, want the index read before, %s",
			shortDigests(installs), session.installedLeakCheckIndex(), digestOf(a.Body)[:12])
	}
	text := logs.String()
	if got := strings.Count(text, "level=WARN"); got != 1 || !strings.Contains(text, failure) {
		t.Fatalf("the daemon logged %q, want one warning naming %q", text, failure)
	}
}

// A runtime given a reader of the index reads it as it starts and again at each of its polls, and
// its poll is ten minutes where the options give none: the daemon must read the index at least
// hourly, and ten minutes keeps a pod within about seventy minutes of a build.
func TestARuntimeGivenAnIndexReaderReadsItAtOnceAndAtEachPoll(t *testing.T) {
	polled := readsOf(testIndex(rigNow))
	g := newRig(t, nil, withOptions(func(o *Options) {
		o.LeakCheckIndex, o.LeakCheckIndexPoll = polled, 5*time.Millisecond
	}))
	g.eventually("the runtime's second read of the index", func() bool { return polled.reads() >= 2 })

	once := readsOf(testIndex(rigNow))
	g = newRig(t, nil, withOptions(func(o *Options) { o.LeakCheckIndex = once }))
	g.eventually("the runtime's first read of the index", func() bool { return once.reads() >= 1 })
	if g.r.leakCheckIndexPoll != 10*time.Minute {
		t.Fatalf("a runtime given no poll reads the index every %s, want 10m0s", g.r.leakCheckIndexPoll)
	}
}

// A poll below zero is a configuration that would stop the daemon: a ticker panics on it. The
// runtime refuses it, naming it.
func TestANegativeIndexPollIsRefused(t *testing.T) {
	opts := testOptions()
	opts.LeakCheckIndex, opts.LeakCheckIndexPoll = readsOf(testIndex(rigNow)), -time.Minute
	if _, err := configure(opts); err == nil || !strings.Contains(err.Error(), "leak check index poll") {
		t.Fatalf("configure with a poll of -1m answered %v, want the poll refused", err)
	}
}

// testStartReserve is the reserve the start-bound tests give a launchers, which a runtime sets to
// its termination grace: short enough that a test waits it out in about a second, and long enough
// for a start frame and its answer over a pipe.
const testStartReserve = time.Second

// bundleSeam is a store of one saved workspace bundle for any pod, counting how often it was
// asked for it: each ask is the bundle's one restore attempt spent. err, when set, is a store
// that does not answer the ask.
type bundleSeam struct {
	takes atomic.Int32
	err   error
}

func (b *bundleSeam) take(context.Context, string, string, time.Time) (runtime.WorkspaceBundle, runtime.WorkspaceBundleTake, error) {
	b.takes.Add(1)
	if b.err != nil {
		return runtime.WorkspaceBundle{}, runtime.WorkspaceBundleNone, b.err
	}
	return savedWorkspaceBundle(), runtime.WorkspaceBundleSend, nil
}

func (b *bundleSeam) restored(context.Context, string, int64) error { return nil }

// reserveLaunchers is a launchers with a saved bundle in seam (none where seam is nil), the newest
// index read from reader, and testStartReserve.
func reserveLaunchers(t *testing.T, reader LeakCheckIndexReader, seam *bundleSeam, log *lockedLog) *launchers {
	t.Helper()
	d := newLaunchers(nil, nil, nil, nil, nil, slogTo(log))
	if seam != nil {
		d.saved, d.restored = seam.take, seam.restored
	}
	d.leakCheckIndex = reader
	d.startReserve = testStartReserve
	d.refreshLeakCheckIndex(context.Background())
	return d
}

// A hop the start sends before its start frame and never stops on leaves the start its reserve:
// a launcher that answers no slice of it, under a start whose own time is shorter than the hop's
// bound, still gets its start frame and answers it. The index's push and the saved bundle's each
// end the reserve before the start's deadline.
func TestASilentHopLeavesTheStartItsReserve(t *testing.T) {
	for name, tc := range map[string]struct {
		launcher *indexLauncher
		seam     *bundleSeam
		takes    bool
		hop      string
	}{
		"the launcher answers no slice of the index":            {launcher: &indexLauncher{silent: true}, takes: true, hop: shimwire.TypeLauncherLeakCheckIndex},
		"the launcher answers no slice of the saved bundle hop": {launcher: &indexLauncher{}, seam: &bundleSeam{}, hop: shimwire.TypeLauncherBundle},
	} {
		t.Run(name, func(t *testing.T) {
			var logs lockedLog
			d := reserveLaunchers(t, readsOf(testIndex(rigNow)), tc.seam, &logs)
			connectIndexLauncher(t, d, workerToken, "uid-pod-silent", tc.takes, tc.launcher)
			ctx, cancel := context.WithTimeout(context.Background(), 2*testStartReserve)
			defer cancel()

			if err := d.start(ctx, workerToken, "uid-pod-silent", shimwire.LauncherStart{ID: "start-after-silence", Generation: 1, Argv: []string{"true"}}); err != nil {
				t.Fatalf("the start failed with %v, want it sent and answered within its reserve", err)
			}
			frames := tc.launcher.frames()
			if countOf(frames, tc.hop) == 0 || countOf(frames, shimwire.TypeLauncherStart) != 1 || frames[len(frames)-1].FrameType() != shimwire.TypeLauncherStart {
				t.Fatalf("the launcher was sent %v, want a slice of %s and then the start", frameTypes(frames), tc.hop)
			}
			if text := logs.String(); !strings.Contains(text, "level=WARN") {
				t.Fatalf("the daemon logged %q, want a warning for the hop that did not end", text)
			}
		})
	}
}

// A start with no more than its reserve left sends neither hop: no slice of the index, and no
// slice of a saved bundle. It still takes the bundle's one restore attempt for its pod, so no
// later start of the pod is sent the bundle over what this start's role does. It sends its start
// frame, and says why it sent nothing before it.
func TestAStartWithNoMoreThanItsReserveLeftSendsNeitherHop(t *testing.T) {
	var logs lockedLog
	seam := &bundleSeam{}
	d := reserveLaunchers(t, readsOf(testIndex(rigNow)), seam, &logs)
	d.startReserve = time.Minute
	l := &indexLauncher{}
	connectIndexLauncher(t, d, workerToken, "uid-pod-late", true, l)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Bounded(t, 30*time.Second, time.Second))
	defer cancel()

	if err := d.start(ctx, workerToken, "uid-pod-late", shimwire.LauncherStart{ID: "start-with-little-left", Generation: 1, Argv: []string{"true"}}); err != nil {
		t.Fatalf("the start: %v", err)
	}
	if frames := l.frames(); len(frames) != 1 || frames[0].FrameType() != shimwire.TypeLauncherStart {
		t.Fatalf("the launcher was sent %v, want its start alone", frameTypes(frames))
	}
	if takes := seam.takes.Load(); takes != 1 {
		t.Fatalf("the store was asked for the saved bundle %d time(s), want once: the ask is what closes this pod's one restore attempt", takes)
	}
	if text := logs.String(); strings.Count(text, "level=WARN") != 2 {
		t.Fatalf("the daemon logged %q, want a warning for each hop it did not send", text)
	}
}

// A start that skips the saved bundle's hop closes its pod's one restore attempt in the store, so
// a later start in the same pod, with all the time it needs, is sent no slice of the bundle: the
// pod's first role has worked in that workspace since, and a restore there would move the issue's
// bookmarks back over that work. The bundle stays whole in the store.
func TestALaterStartOfAPodWhoseBundleHopWasSkippedIsSentNoBundle(t *testing.T) {
	st, saved := leakCheckStore(t)
	var logs lockedLog
	d := newLaunchers(nil, st.TakeWorkspaceBundleForRestore, st.DeleteRestoredWorkspaceBundle, nil, nil, slogTo(&logs))
	d.startReserve = time.Hour
	l := &indexLauncher{}
	connectIndexLauncher(t, d, workerToken, "uid-pod-skipped", false, l)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Bounded(t, 30*time.Second, time.Second))
	defer cancel()
	if err := d.start(ctx, workerToken, "uid-pod-skipped", shimwire.LauncherStart{ID: "start-with-little-left", Generation: 1, Argv: []string{"true"}}); err != nil {
		t.Fatalf("the first start: %v", err)
	}

	d.startReserve = 0
	later, cancelLater := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelLater()
	err := d.start(later, workerToken, "uid-pod-skipped", shimwire.LauncherStart{ID: "a-later-start", Generation: 2, Argv: []string{"true"}})
	if got := countOf(l.frames(), shimwire.TypeLauncherBundle); got != 0 {
		t.Fatalf("the later start of the pod was sent %d slice(s) of the saved bundle (%v), want none: the first start closed the pod's attempt", got, frameTypes(l.frames()))
	}
	if err != nil {
		t.Fatalf("the later start: %v", err)
	}
	if size, held, err := st.WorkspaceBundleSize(context.Background(), testTree); err != nil || !held || size != len(saved.Bundle) {
		t.Fatalf("the store holds %d bytes (held=%t, %v), want the %d saved kept", size, held, err, len(saved.Bundle))
	}
	if text := logs.String(); !strings.Contains(text, "stays in the daemon's database") {
		t.Fatalf("the daemon logged %q, want the skipped send's warning to say where the work is", text)
	}
}

// A store that does not answer the ask for the saved bundle stops the start: its pod's one
// restore attempt may still be open, and a role started now would work in a workspace a later
// start could be sent the bundle over. No start frame goes, and the error says why; the
// supervisor's next launch asks again.
func TestAStoreThatDoesNotAnswerTheBundleAskStopsTheStart(t *testing.T) {
	var logs lockedLog
	seam := &bundleSeam{err: errors.New("take the workspace bundle of " + testTree + ": connection refused")}
	d := reserveLaunchers(t, readsOf(testIndex(rigNow)), seam, &logs)
	l := &indexLauncher{}
	connectIndexLauncher(t, d, workerToken, "uid-pod-unanswered", false, l)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Bounded(t, 30*time.Second, time.Second))
	defer cancel()

	err := d.start(ctx, workerToken, "uid-pod-unanswered", shimwire.LauncherStart{ID: "start-without-the-store", Generation: 1, Argv: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("the start answered %v, want it stopped naming the store's error", err)
	}
	if got := countOf(l.frames(), shimwire.TypeLauncherStart); got != 0 {
		t.Fatalf("the launcher was sent %v, want no start", frameTypes(l.frames()))
	}
}

// A push the poll began for a launcher connection that has since been replaced goes to the token's
// current connection, as each slice's request does, and holds the token's one push turn while it
// runs: a start on the replacement waits for it, finds the index installed, and sends its start
// alone. The launcher process is the same across the reconnect, so it keeps the slices it took.
func TestAPushForAReplacedConnectionHoldsTheTokensTurn(t *testing.T) {
	a := testIndex(rigNow)
	var logs lockedLog
	d := indexLaunchers(readsOf(a), &logs)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Bounded(t, time.Minute, time.Second))
	defer cancel()
	d.refreshLeakCheckIndex(ctx)
	l := &indexLauncher{hold: make(chan struct{}), holding: make(chan struct{})}
	replaced := connectIndexLauncher(t, d, workerToken, "uid-pod-reconnect", true, l)
	current := connectIndexLauncher(t, d, workerToken, "uid-pod-reconnect", true, l)

	pushed := make(chan error, 1)
	go func() { pushed <- d.serveLeakCheckIndex(ctx, replaced) }()
	select {
	case <-l.holding:
	case err := <-pushed:
		t.Fatalf("the push for the replaced connection ended (%v) having sent %v, want it pushed to the token's current connection", err, frameTypes(l.frames()))
	}
	started := make(chan error, 1)
	go func() {
		started <- d.start(ctx, workerToken, "uid-pod-reconnect", shimwire.LauncherStart{ID: "start-on-the-replacement", Generation: 1, Argv: []string{"true"}})
	}()
	// A start that did not wait for the token's turn would send a slice of its own while the
	// push's first is held; it is given the time to before the push goes on.
	time.Sleep(50 * time.Millisecond)
	close(l.hold)
	if err := <-pushed; err != nil {
		t.Fatalf("the push: %v", err)
	}
	if err := <-started; err != nil {
		t.Fatalf("the start: %v", err)
	}
	frames := l.frames()
	if len(frames) < 3 || countOf(frames, shimwire.TypeLauncherLeakCheckIndex) != len(frames)-1 || frames[len(frames)-1].FrameType() != shimwire.TypeLauncherStart {
		t.Fatalf("the launcher was sent %v, want each slice of the index once and then the start", frameTypes(frames))
	}
	if held := current.installedLeakCheckIndex(); held != digestOf(a.Body) {
		t.Fatalf("the daemon holds the current connection on %q, want %s", held, digestOf(a.Body))
	}
}
