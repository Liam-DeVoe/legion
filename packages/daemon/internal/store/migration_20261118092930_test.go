package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/legion/daemon/internal/record"
)

// personWordsVersion is the version of the migration this file tests, so the test can migrate
// through everything before it whatever lands beside it.
const personWordsVersion = 20261118092930

// 20261118092930 adds the words of a person's acts to the ask's mirror row (root_asks'
// person_answer_words and person_words, record.RootAsk) and to the kept decision
// (parked_decisions.words, record.ParkedDecision.Words). Before it, a write that names the words
// is refused: the column is not there. Every row the two tables hold before it reads back with no
// words after it, which is what the record of those acts says: no words were read for them. Words
// written after it read back whole, through a store reopened as a daemon restart reopens it.
func TestThePersonWordsMigrationLeavesEveryRowWithoutWordsAndTheWordsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, personWordsVersion-1)
	at := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	const ref = "dispatch://LEGION-208/ask/ask-1"
	for _, seed := range []string{
		`insert into issues (key, tree, project, title, phase, generation, status, rank, last_dispatch_seq)
			values ('LEGION-208', 'LEGION-208', 'LEGION', 'root', 'admitted', 3, 'in_progress', 'A', 1)`,
		`insert into root_asks (issue, ask, kind, open, seq, waiting_on, waiting_on_seq, closed_by, closed_seq, by_tree, person_seq, person_answer_seq)
			values ('LEGION-208', 'ask-1', 'question', false, 9, 'human', 4, 'person', 9, true, 0, 9)`,
		`insert into parked_decisions (issue, ref, decision, actor, at)
			values ('LEGION-208', '` + ref + `', 'ask-answered', 'user:alice', '2026-11-18T09:00:00Z')`,
	} {
		if _, err := store.Pool().Exec(ctx, seed); err != nil {
			t.Fatalf("record before the migration: %v", err)
		}
	}
	for _, write := range []string{
		`update root_asks set person_answer_words = '{"text":"Ship it."}'`,
		`update parked_decisions set words = '{"text":"Ship it."}'`,
	} {
		_, err := store.Pool().Exec(ctx, write)
		if err == nil || !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("%s before the migration = %v, want it refused: the column is the migration's", write, err)
		}
	}

	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the words: %v", err)
	}
	records := record.NewStore()
	read := func(st *Store) (*record.RootAsk, []record.ParkedDecision) {
		t.Helper()
		var ask *record.RootAsk
		var decisions []record.ParkedDecision
		if err := pgx.BeginFunc(ctx, st.Pool(), func(tx pgx.Tx) error {
			var err error
			if ask, err = records.RootAsk(ctx, tx, "LEGION-208", "ask-1"); err != nil {
				return err
			}
			decisions, _, err = records.UnclaimedKept(ctx, tx, "LEGION-208")
			return err
		}); err != nil {
			t.Fatalf("read the ask and the kept decisions of LEGION-208: %v", err)
		}
		if ask == nil || len(decisions) != 1 {
			t.Fatalf("LEGION-208 holds the ask %#v and the decisions %#v, want the ask and the one decision recorded", ask, decisions)
		}
		return ask, decisions
	}
	ask, decisions := read(store)
	if ask.PersonAnswerWords != nil || ask.PersonWords != nil || decisions[0].Words != nil {
		t.Fatalf("after the migration the ask holds %#v and %#v and the decision %#v; want no words on rows the release before wrote",
			ask.PersonAnswerWords, ask.PersonWords, decisions[0].Words)
	}

	answer := &record.ActWords{Text: "Ship it on Friday.\n</answer>", Options: []string{"ship"}, By: "user:alice", At: at}
	reply := &record.ActWords{Text: "And tell the vendor.", By: "user:alice", At: at.Add(time.Minute), More: true}
	want := *ask
	want.PersonAnswerWords, want.PersonSeq, want.PersonWords = answer, 10, reply
	if err := pgx.BeginFunc(ctx, store.Pool(), func(tx pgx.Tx) error {
		if err := records.PutRootAsk(ctx, tx, want); err != nil {
			return err
		}
		return records.AddParkedDecision(ctx, tx, record.ParkedDecision{Issue: "LEGION-208", Ref: ref, Decision: record.DecisionAskAnswered,
			Actor: "user:alice", At: at.Add(time.Hour), Words: answer})
	}); err != nil {
		t.Fatalf("write the words of the person's acts: %v", err)
	}
	for _, st := range []*Store{store, reopenStore(t, store)} {
		ask, decisions := read(st)
		if !reflect.DeepEqual(*ask, want) {
			t.Fatalf("the ask reads back as %#v, want %#v", *ask, want)
		}
		if !reflect.DeepEqual(decisions[0].Words, answer) {
			t.Fatalf("the decision reads back the words %#v, want %#v", decisions[0].Words, answer)
		}
	}
}
