package store

import (
	"context"
	"strings"
	"testing"
)

// answerQuestionsThreadReplyVersion is the version of the migration this file tests, so the test
// can migrate through everything before it whatever lands beside it.
const answerQuestionsThreadReplyVersion = 20261124021727

// 20261124021727 widens answer_questions.kind's check with thread_reply, the answer loop's
// question of a reply in a comment thread, answered in that thread: a question of that kind is
// recorded, one of each kind before it still is, and a kind the loop does not write is still
// refused. The negative control is the same question recorded before the migration, which the
// check refuses.
func TestMigrationAnswerQuestionsThreadReply(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	migrateThrough(t, store, answerQuestionsThreadReplyVersion-1)
	early := testerQuestion("q-early", "dispatch:c-early", "ses_tester", at(0))
	early.Kind = "thread_reply"
	if _, _, err := store.ReceiveQuestion(ctx, early); err == nil || !strings.Contains(err.Error(), "answer_questions_kind_check") {
		t.Fatalf("a thread reply recorded before the migration: %v; want the kind check to refuse it", err)
	}

	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate through the thread reply's kind: %v", err)
	}

	for i, kind := range []string{"message", "mention", "ask_reply", "envoy", "thread_reply"} {
		q := testerQuestion("q-"+kind, "dispatch:c-"+kind, "ses_tester", at(i+1))
		q.Kind = kind
		got, inserted, err := store.ReceiveQuestion(ctx, q)
		if err != nil || !inserted || got.Kind != kind {
			t.Errorf("a question of the kind %s = %+v, %t, %v; want it recorded", kind, got, inserted, err)
		}
	}
	other := testerQuestion("q-issue", "dispatch:c-issue", "ses_tester", at(9))
	other.Kind = "issue"
	if _, _, err := store.ReceiveQuestion(ctx, other); err == nil || !strings.Contains(err.Error(), "answer_questions_kind_check") {
		t.Errorf("a question of the kind issue: %v; want the kind check to refuse a kind the loop does not write", err)
	}
}
