package testnats

import (
	"slices"
	"strings"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

// memberJetStream is a JetStream context on a connection to cluster member i alone, its requests
// bounded by jetStreamWait as Cluster.JetStream's are, closed when the test ends.
func memberJetStream(t *testing.T, cluster *Cluster, i int) natsgo.JetStreamContext {
	t.Helper()
	conn := Connect(t, cluster.Servers[i].URL())
	t.Cleanup(conn.Close)
	js, err := conn.JetStream(natsgo.MaxWait(jetStreamWait))
	if err != nil {
		t.Fatalf("open JetStream on member %s: %v", cluster.Name(i), err)
	}
	return js
}

// answeredRead reads stream through js until a read is answered, each read cut at updateReadWait,
// and returns that answer. A copy whose group has just lost its leader answers no read until it
// knows the leader is gone, so a lost read says nothing. It fails the test when no read is
// answered in a minute.
func answeredRead(t *testing.T, js natsgo.JetStreamContext, stream string) *natsgo.StreamInfo {
	t.Helper()
	var lost error
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); {
		read, err := js.StreamInfo(stream, natsgo.MaxWait(updateReadWait))
		if err == nil {
			return read
		}
		lost = err
	}
	t.Fatalf("no read of %s was answered within a minute; the last drew %v", stream, lost)
	return nil
}

// An update sent while the stream's group has no leader is answered by no one, and UpdateStream
// still returns once a read holds it. The stream has three copies on a cluster of five, so the
// metadata group keeps its quorum, and applies the update, when two members stop. The row stops
// the stream's leader and one other copy, and goes on only once the copy left running answers a
// read naming no leader; when that copy took the leadership instead, it starts both again and
// chooses anew. It keeps the two stopped until UpdateStream has returned, or until Cluster
// JetStream's wait for an answer, jetStreamWait, has passed with a margin, and then starts them
// again. A test that waits for the update's answer instead fails here at jetStreamWait.
func TestAnUpdateSentWhileTheStreamElectsItsLeaderIsFoundByARead(t *testing.T) {
	const stream = "ELECTING"
	cluster := StartCluster(t, 5)
	cluster.CreateStream(t, natsgo.StreamConfig{Name: stream, Subjects: []string{"electing.>"}, Replicas: 3, Storage: natsgo.FileStorage})
	var stopped []int
	var kept int
	var held *natsgo.StreamInfo
	for attempt := 1; held == nil; attempt++ {
		if attempt > 8 {
			t.Fatalf("in %d attempts the copy left running led %s after the stops each time, so no update went to a group with no leader", attempt-1, stream)
		}
		read := AwaitStreamCopies(t, cluster.JetStream(t), stream, 3)
		members := []int{cluster.Member(read.Cluster.Leader)}
		for _, peer := range read.Cluster.Replicas {
			members = append(members, cluster.Member(peer.Name))
		}
		if slices.Contains(members, -1) || len(slices.Compact(slices.Sorted(slices.Values(members)))) != 3 {
			t.Fatalf("the read of %s names members %v, not three distinct members of the cluster", stream, members)
		}
		stopped, kept = members[:2], members[2]
		for _, member := range stopped {
			cluster.Servers[member].Stop(t)
		}
		read = answeredRead(t, memberJetStream(t, cluster, kept), stream)
		t.Logf("attempt %d: stopped members %v; the copy left running, member %d, names leader %q", attempt, stopped, kept, read.Cluster.Leader)
		if read.Cluster.Leader == "" {
			held = read
			break
		}
		for _, member := range stopped {
			cluster.Servers[member].Start(t)
		}
		cluster.AwaitReady(t)
	}

	config := held.Config
	config.MaxAge = time.Hour
	recorder := &fatalRecorder{TB: t}
	js := memberJetStream(t, cluster, kept)
	var info *natsgo.StreamInfo
	done := make(chan struct{})
	go func() {
		defer close(done)
		info = UpdateStream(recorder, js, config)
	}()
	select {
	case <-done:
	case <-time.After(jetStreamWait + 2*time.Second):
	}
	for _, member := range stopped {
		cluster.Servers[member].Start(t)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatalf("UpdateStream did not return within 2m of the members' start again")
	}
	if recorder.message != "" || info == nil {
		t.Fatalf("the update sent while %s had no leader failed: %q", stream, recorder.message)
	}
	if info.Config.MaxAge != time.Hour {
		t.Fatalf("UpdateStream returned %s with age %s, want 1h0m0s", stream, info.Config.MaxAge)
	}
	if settled := AwaitStreamCopies(t, cluster.JetStream(t), stream, 3); settled.Config.MaxAge != time.Hour {
		t.Fatalf("once it has a leader again, %s holds age %s, want 1h0m0s", stream, settled.Config.MaxAge)
	}
}

// droppedUpdates sends no update of a stream at all and answers each with nats.go's timeout, as an
// update the server never applies would draw. Every other request is js's own.
type droppedUpdates struct{ natsgo.JetStreamContext }

func (droppedUpdates) UpdateStream(*natsgo.StreamConfig, ...natsgo.JSOpt) (*natsgo.StreamInfo, error) {
	return nil, natsgo.ErrTimeout
}

// deadlineRecorder is fatalRecorder with a test binary deadline of its own.
type deadlineRecorder struct {
	*fatalRecorder
	deadline time.Time
}

func (d deadlineRecorder) Deadline() (time.Time, bool) { return d.deadline, true }

// An update that never lands fails the test once the test's deadline, less updateMargin, has
// passed, and not before, naming the stream and the setting no read held.
func TestAnUpdateThatNeverLandsFailsAtTheTestsDeadlineNamingTheStreamAndTheSetting(t *testing.T) {
	const stream, waited = "NEVER", 2 * time.Second
	conn := Connect(t, URL(t))
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	info, err := js.AddStream(&natsgo.StreamConfig{Name: stream, Subjects: []string{"never.>"}})
	if err != nil {
		t.Fatalf("add stream: %v", err)
	}
	config := info.Config
	config.MaxAge = time.Hour
	recorder := deadlineRecorder{fatalRecorder: &fatalRecorder{TB: t}, deadline: time.Now().Add(updateMargin + waited)}
	began := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		UpdateStream(recorder, droppedUpdates{js}, config)
	}()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatalf("UpdateStream did not give up on an update that never lands within a minute of a deadline %s away", updateMargin+waited)
	}
	took := time.Since(began)
	if took < waited {
		t.Fatalf("UpdateStream gave up after %s, before the test's deadline less %s, %s away", took.Round(time.Millisecond), updateMargin, waited)
	}
	for _, want := range []string{"stream " + stream, "max_age: sent 1h0m0s, the stream holds 0s"} {
		if !strings.Contains(recorder.message, want) {
			t.Fatalf("UpdateStream failed with %q, want it to name %q", recorder.message, want)
		}
	}
}

// A refused update fails the test at once with the server's answer, and an answered one returns
// that answer.
func TestUpdateStreamReturnsTheAnswerAndFailsOnARefusal(t *testing.T) {
	conn := Connect(t, URL(t))
	t.Cleanup(conn.Close)
	js, err := conn.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	info, err := js.AddStream(&natsgo.StreamConfig{Name: "ANSWERED", Subjects: []string{"answered.>"}})
	if err != nil {
		t.Fatalf("add stream: %v", err)
	}
	config := info.Config
	config.MaxAge = time.Hour
	if got := UpdateStream(t, js, config); got.Config.MaxAge != time.Hour {
		t.Fatalf("UpdateStream returned age %s, want 1h0m0s", got.Config.MaxAge)
	}

	missing := config
	missing.Name = "MISSING"
	missing.Subjects = []string{"missing.>"}
	recorder := &fatalRecorder{TB: t}
	began := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		UpdateStream(recorder, js, missing)
	}()
	<-done
	if took := time.Since(began); took > 5*time.Second || !strings.Contains(recorder.message, "refused the update of stream MISSING") || !strings.Contains(recorder.message, "stream not found") {
		t.Fatalf("an update of a stream that does not exist failed after %s with %q, want at once with the server's refusal", took.Round(time.Millisecond), recorder.message)
	}
}
