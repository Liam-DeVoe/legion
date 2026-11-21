package cistore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
)

// The runs a record keeps waiting to stand for a name (State.Waiting) never cost a head its
// settlements, and never cost a name the run that would stand for it. The record below holds checks
// of a GitHub Actions suite whose event it does not know (900), each with two runs of other suites
// waiting behind it: an older success (901) and a newer failure (902), as large as the bound
// allows. A check of another App then takes it past the bound with every waiting run, and not with
// only the newest of each name. The write lands with the new check, it is not marked overflowed,
// and each name keeps its newest waiting run alone. When suite 900 then turns out to be a
// workflow_dispatch run, that failure stands for each name, as it would have with nothing dropped:
// no name is left with no check, so a red head does not read green.
func TestWaitingRunsGiveWayAtTheRecordsBoundButTheNewestOfEachName(t *testing.T) {
	conn, cleanup := connectNATS(t)
	defer cleanup()
	store := openStore(t, conn)
	h := head{"example-org", "example-repo", "61", "3a171e00000000000000000000000000000000ff"}

	entry := func(i int, id uint64, suite, conclusion string) Check {
		return Check{
			Name: fmt.Sprintf("dispatch-browser / binaries %04d", i), CheckRunID: checkRunID(id),
			URL:    fmt.Sprintf("https://github.com/example-org/example-repo/actions/runs/44860489318/job/%d", id),
			Status: "completed", Conclusion: conclusion, ObservedAt: "2026-11-21T03:21:34Z",
			SuiteID: suite, AppID: contracts.GithubActionsAppID,
		}
	}
	st := State{Owner: h.owner, Repo: h.repo, Number: h.number, SHA: h.sha, Checks: map[string]Check{}, Waiting: map[string]Check{}}
	size := func(st State) int {
		t.Helper()
		buf, err := encodeRecord(&st)
		if err != nil {
			t.Fatalf("encode the record: %v", err)
		}
		return len(buf)
	}
	for i := 0; ; i++ {
		standing := entry(i, 136713000000+uint64(i), "900", "success")
		older, newer := entry(i, 136711000000+uint64(i), "901", "success"), entry(i, 136712000000+uint64(i), "902", "failure")
		st.Checks[standing.Name] = standing
		st.Waiting[waitingKey(older.CheckRunID)] = older
		st.Waiting[waitingKey(newer.CheckRunID)] = newer
		if size(st) > maxRecordBytes {
			delete(st.Checks, standing.Name)
			delete(st.Waiting, waitingKey(older.CheckRunID))
			delete(st.Waiting, waitingKey(newer.CheckRunID))
			break
		}
	}
	names := len(st.Checks)
	st.Schema = recordSchema
	st.LastEventAt = time.Now().UnixMilli()
	buf, err := encodeRecord(&st)
	if err != nil {
		t.Fatalf("encode the seeded record: %v", err)
	}
	if _, err := store.watcher.KV().Put(h.key(), buf); err != nil {
		t.Fatalf("seed the record: %v", err)
	}

	added := contracts.CIObservation{
		Owner: h.owner, Repo: h.repo, Number: h.number, SHA: h.sha, AppID: "57789",
		CheckName: "CodeQL", CheckRunID: 136720000001, Status: "completed", Conclusion: "success",
		URL: "https://github.com/example-org/example-repo/runs/" + strings.Repeat("9", 4096), ObservedAt: "2026-11-21T03:30:00Z",
	}
	with := st
	with.Checks = cloneChecks(st.Checks)
	with.Checks[added.CheckName] = Check{Name: added.CheckName, CheckRunID: checkRunID(added.CheckRunID), URL: added.URL,
		Status: added.Status, Conclusion: added.Conclusion, ObservedAt: added.ObservedAt, AppID: added.AppID}
	trimmed := with
	trimmed.Waiting = cloneChecks(st.Waiting)
	if dropped := trimmed.newestWaiting(); dropped != names {
		t.Fatalf("keeping the newest waiting run of each name drops %d, want one of each of the %d names", dropped, names)
	}
	if size(with) <= maxRecordBytes || size(trimmed) > maxRecordBytes {
		t.Fatalf("the record with the added check is %d bytes with its %d waiting runs and %d with the newest of each name, want past the %d-byte bound only with all of them",
			size(with), len(with.Waiting), size(trimmed), maxRecordBytes)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.Record(ctx, added); err != nil {
		t.Fatalf("record a check past the bound its older waiting runs alone take the record past: %v", err)
	}
	got := getState(t, store, h.owner, h.repo, h.number, h.sha)
	if got.Overflowed || len(got.Waiting) != names || got.Checks[added.CheckName].CheckRunID != checkRunID(added.CheckRunID) || len(got.Checks) != names+1 {
		t.Fatalf("after the write the record is overflowed %v with %d waiting runs and %d checks (%v held), want not overflowed, %d waiting runs and the %d checks with %s",
			got.Overflowed, len(got.Waiting), len(got.Checks), got.Checks[added.CheckName], names, names+1, added.CheckName)
	}
	for _, check := range got.Waiting {
		if check.SuiteID != "902" {
			t.Fatalf("the record kept the waiting run %+v, want the newest of its name, suite 902's", check)
		}
	}

	if err := store.RecordRunEvent(ctx, contracts.CIObservation{Owner: h.owner, Repo: h.repo, Number: h.number, SHA: h.sha,
		SuiteID: "900", RunEvent: "workflow_dispatch"}); err != nil {
		t.Fatalf("record suite 900's event: %v", err)
	}
	got = getState(t, store, h.owner, h.repo, h.number, h.sha)
	failing := 0
	for name, check := range got.Checks {
		if name != added.CheckName && check.SuiteID == "902" && check.Conclusion == "failure" {
			failing++
		}
	}
	if failing != names || len(got.Checks) != names+1 {
		t.Fatalf("once suite 900 is a workflow_dispatch run, %d of %d names stand on suite 902's failure and the record holds %d checks, want all %d and %d checks",
			failing, names, len(got.Checks), names, names+1)
	}
}

// cloneChecks is a copy of checks.
func cloneChecks(checks map[string]Check) map[string]Check {
	out := make(map[string]Check, len(checks)+1)
	for key, check := range checks {
		out[key] = check
	}
	return out
}
