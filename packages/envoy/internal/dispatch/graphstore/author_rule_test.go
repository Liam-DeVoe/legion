package graphstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// The root's R19 makes an author the person a credential proved, so the credentials of one
// person are one author, and nothing else joins two credentials: two sessions of one workload
// stay two authors, because every session of that workload carries the same workload, and the
// shared bearer proves nobody. This test writes a node under each writer through Write, so its
// made_by is stored as every write stores it, and reads it back under each reader through
// AuthorIsSQL bound with ActingCredential, the pair every author-private read binds. It also
// asks the Go half (ProvesAuthor and provedAuthor), so the two halves of the one rule cannot
// answer one pair differently. Last, it holds the write half of the rule: a node reserved for its
// author is refused on every credential that proves nobody, the shared bearer and a token naming
// an empty owner among them, and granted on every credential that proves somebody.
func TestTheAuthorIsThePersonACredentialProvedAndNothingElseJoinsTwo(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	named := func(name string) *string { return &name }

	cookie := model.Actor{Kind: "user", ID: "Alice"}
	// A personal token records the session its body names beside the person the token proves.
	token := model.Actor{Kind: "session", ID: "ses_typed_in_the_body", Owner: named("alice")}
	tokenRead := model.Actor{Kind: "session", ID: "", Owner: named("alice")}
	verified := model.Actor{Kind: "session", ID: "ses_verified_for_alice", Owner: named(" ALICE ")}
	bob := model.Actor{Kind: "user", ID: "bob"}
	workload := "system:serviceaccount:legion:legion-worker"
	podToken := model.Actor{Service: named(workload)}
	workloadSession := model.Actor{Kind: "session", ID: "ses_of_the_workload_1", Service: named(workload)}
	otherWorkloadSession := model.Actor{Kind: "session", ID: "ses_of_the_workload_2", Service: named(workload)}
	unowned := model.Actor{Kind: "session", ID: "ses_nobody_owns_1"}
	otherUnowned := model.Actor{Kind: "session", ID: "ses_nobody_owns_2"}
	bearer := model.Actor{}

	cases := []struct {
		name           string
		writer, reader model.Actor
		want           bool
	}{
		{"her cookie, then her personal token", cookie, tokenRead, true},
		{"her personal token, then her cookie", token, cookie, true},
		{"her cookie, then a session the broker verified as hers", cookie, verified, true},
		{"a session the broker verified as hers, then her personal token", verified, tokenRead, true},
		{"her cookie, then another person's cookie", cookie, bob, false},
		{"her personal token, then another person's cookie", token, bob, false},
		{"a session of a workload, then the same session", workloadSession, workloadSession, true},
		{"a session of a workload, then another session of that workload", workloadSession, otherWorkloadSession, false},
		{"a session of a workload, then that workload's own token", workloadSession, podToken, false},
		{"a workload's own token, then the same token", podToken, podToken, true},
		{"a session nobody owns, then the same session", unowned, unowned, true},
		{"a session nobody owns, then another such session", unowned, otherUnowned, false},
		{"a session nobody owns, then her cookie", unowned, cookie, false},
		{"the shared bearer, then the shared bearer", bearer, bearer, false},
		{"her cookie, then the shared bearer", cookie, bearer, false},
	}
	for index, row := range cases {
		id := fmt.Sprintf("author-rule-%02d", index)
		writeNode(t, ctx, database, Node{
			Kind: KindPerson, ID: id, Supertag: "person",
			Visibility: VisibilityProject, State: StateLive,
			MadeAt: at, MadeBy: row.writer, SourceEvent: NoEvent,
		})
		var stored bool
		if err := database.Pool.QueryRow(ctx,
			`select `+AuthorIsSQL("made_by", "$3")+` from graph_nodes where kind = $1 and id = $2`,
			KindPerson, id, ActingCredential(row.reader)).Scan(&stored); err != nil {
			t.Fatalf("%s: read the node under the rule: %v", row.name, err)
		}
		if stored != row.want {
			t.Errorf("%s: AuthorIsSQL answers %t, want %t", row.name, stored, row.want)
		}
		writer, proved := provedAuthor(row.writer)
		reader, read := provedAuthor(row.reader)
		if inGo := proved && read && writer == reader; inGo != row.want {
			t.Errorf("%s: the Go half answers %t (writer %+v, reader %+v), want %t", row.name, inGo, writer, reader, row.want)
		}
		if ProvesAuthor(row.reader) != read {
			t.Errorf("%s: ProvesAuthor answers %t for the reader, and provedAuthor %t", row.name, ProvesAuthor(row.reader), read)
		}
	}

	emptyOwner := model.Actor{Kind: "session", Owner: named("")}
	for _, writer := range []struct {
		name   string
		actor  model.Actor
		proves bool
	}{
		{"the shared bearer", bearer, false},
		{"a token naming an empty owner", emptyOwner, false},
		{"her cookie", cookie, true},
		{"her personal token", tokenRead, true},
		{"a session the broker verified as hers", verified, true},
		{"a session of a workload", workloadSession, true},
		{"a workload's own token", podToken, true},
		{"a session nobody owns", unowned, true},
	} {
		visibility, err := VisibilityFor(VisibilityAuthor, writer.actor)
		switch {
		case !writer.proves && !errors.Is(err, ErrAuthorNamesNobody):
			t.Errorf("a node reserved for its author, written on %s, gets %q (err %v), want the refusal ErrAuthorNamesNobody", writer.name, visibility, err)
		case writer.proves && (err != nil || visibility != VisibilityAuthor):
			t.Errorf("a node reserved for its author, written on %s, gets %q (err %v), want %q", writer.name, visibility, err, VisibilityAuthor)
		}
		if bound := ActingCredential(writer.actor); (bound != nil) != writer.proves {
			t.Errorf("%s binds %v as the acting author, want a value exactly when it proves somebody (%t)", writer.name, bound, writer.proves)
		}
	}
}
